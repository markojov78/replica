package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dropoutbox/internal/apiclient"
	"dropoutbox/internal/config"

	"github.com/gorilla/websocket"
)

const bootstrapRetryInterval = 5 * time.Second
const commandWebSocketPath = "/internal/nodes/ws"
const watcherReportSettleDelay = 250 * time.Millisecond

type Runtime struct {
	client            *apiclient.Client
	heartbeatInterval time.Duration
	wsConnected       atomic.Bool
	wsDialer          *websocket.Dialer

	stateMu        sync.RWMutex
	replicas       []apiclient.Replica
	replicaFiles   map[uint][]apiclient.ReplicaInventoryFile
	transferKey    string
	transferTokens map[uint]string

	commandCh chan apiclient.Command
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	client, err := apiclient.New(cfg)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		client:            client,
		heartbeatInterval: cfg.App.HeartbeatInterval,
		wsDialer:          websocket.DefaultDialer,
		replicaFiles:      make(map[uint][]apiclient.ReplicaInventoryFile),
		transferTokens:    make(map[uint]string),
		commandCh:         make(chan apiclient.Command, 128),
	}, nil
}

func (r *Runtime) Start(ctx context.Context) {
	go r.run(ctx)
}

func (r *Runtime) run(ctx context.Context) {
	pair, replicas, commands, ok := r.bootstrap(ctx)
	if !ok {
		return
	}

	r.startReplicaWatchers(ctx, replicas)
	go r.commandProcessor(ctx)
	for _, command := range commands {
		r.enqueueCommand(command)
	}
	go r.refreshLoop(ctx, pair)
	go r.commandLoop(ctx)
	go r.heartbeatLoop(ctx)
}

func (r *Runtime) bootstrap(ctx context.Context) (*apiclient.NodeTokenPair, []apiclient.Replica, []apiclient.Command, bool) {
	for {
		pair, err := r.client.Authenticate(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, nil, nil, false
			}
			log.Printf("storage runtime authenticate failed: %v", err)
			continue
		}
		r.setTransferTokenPublicKey(pair.TransferTokenPublicKey)

		report, err := r.client.ReportAvailability(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, nil, nil, false
			}
			log.Printf("storage runtime initial heartbeat failed: %v", err)
			continue
		}

		replicas, err := r.refreshLocalState(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, nil, nil, false
			}
			log.Printf("storage runtime state bootstrap failed: %v", err)
			continue
		}

		log.Printf("storage runtime connected to coordinator as node_id=%s replicas=%d", r.client.NodeID(), len(replicas))
		return pair, replicas, report.Commands, true
	}
}

func (r *Runtime) refreshLocalState(ctx context.Context) ([]apiclient.Replica, error) {
	replicas, err := r.client.ListOwnReplicas(ctx)
	if err != nil {
		return nil, err
	}

	replicaFiles := make(map[uint][]apiclient.ReplicaInventoryFile, len(replicas))
	for _, replica := range replicas {
		files, err := r.client.ListReplicaInventoryFiles(ctx, replica.ID)
		if err != nil {
			return nil, err
		}
		replicaFiles[replica.ID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
	}

	r.setLocalState(replicas, replicaFiles)
	log.Printf("storage runtime refreshed local state replicas=%d", len(replicas))
	return replicas, nil
}

func (r *Runtime) refreshReplicaFiles(ctx context.Context, replicaID uint) ([]apiclient.ReplicaInventoryFile, error) {
	files, err := r.client.ListReplicaInventoryFiles(ctx, replicaID)
	if err != nil {
		return nil, err
	}
	r.setReplicaFiles(replicaID, files)
	return files, nil
}

func (r *Runtime) setLocalState(replicas []apiclient.Replica, replicaFiles map[uint][]apiclient.ReplicaInventoryFile) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.replicas = append([]apiclient.Replica(nil), replicas...)
	r.replicaFiles = make(map[uint][]apiclient.ReplicaInventoryFile, len(replicaFiles))
	for replicaID, files := range replicaFiles {
		r.replicaFiles[replicaID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
	}
}

func (r *Runtime) setReplicaFiles(replicaID uint, files []apiclient.ReplicaInventoryFile) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	if r.replicaFiles == nil {
		r.replicaFiles = make(map[uint][]apiclient.ReplicaInventoryFile)
	}
	r.replicaFiles[replicaID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
}

func (r *Runtime) setTransferTokenPublicKey(publicKey string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.transferKey = publicKey
}

func (r *Runtime) transferTokenPublicKey() string {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.transferKey
}

func (r *Runtime) setReplicaTransferToken(replicaID uint, token string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.transferTokens == nil {
		r.transferTokens = make(map[uint]string)
	}
	r.transferTokens[replicaID] = token
}

func (r *Runtime) replicaTransferToken(replicaID uint) string {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.transferTokens[replicaID]
}

func (r *Runtime) findReplica(replicaID uint) (apiclient.Replica, bool) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	for _, replica := range r.replicas {
		if replica.ID == replicaID {
			return replica, true
		}
	}
	return apiclient.Replica{}, false
}

func (r *Runtime) startReplicaWatchers(ctx context.Context, replicas []apiclient.Replica) {
	for _, replica := range replicas {
		watcher, err := GetWatcher(ctx, replica.URI)
		if err != nil {
			log.Printf("storage runtime watcher setup skipped replica_id=%d uri=%s error=%v", replica.ID, replica.URI, err)
			continue
		}

		changeCh, errCh, err := watcher.Watch(ctx, replica.URI)
		if err != nil {
			log.Printf("storage runtime watcher start failed replica_id=%d uri=%s error=%v", replica.ID, replica.URI, err)
			continue
		}

		log.Printf("storage runtime watcher started replica_id=%d uri=%s", replica.ID, replica.URI)

		go func(replica apiclient.Replica, changeCh <-chan FileChange, errCh <-chan error) {
			for changeCh != nil || errCh != nil {
				select {
				case <-ctx.Done():
					return
				case err, ok := <-errCh:
					if !ok {
						errCh = nil
						continue
					}
					log.Printf("storage runtime watcher error replica_id=%d uri=%s error=%v", replica.ID, replica.URI, err)
				case change, ok := <-changeCh:
					if !ok {
						changeCh = nil
						continue
					}
					log.Printf("storage runtime replica change replica_id=%d uri=%s change_type=%s relative_uri=%s previous_relative_uri=%s state=%s",
						replica.ID,
						replica.URI,
						change.ChangeType,
						change.RelativeURI,
						optionalString(change.PreviousRelativeURI),
						formatFileState(change.State),
					)
					if err := r.reportWatcherChange(ctx, replica, change); err != nil {
						log.Printf("storage runtime watcher report failed replica_id=%d uri=%s change_type=%s relative_uri=%s error=%v", replica.ID, replica.URI, change.ChangeType, change.RelativeURI, err)
					}
				}
			}
		}(replica, changeCh, errCh)
	}
}

func (r *Runtime) reportWatcherChange(ctx context.Context, replica apiclient.Replica, change FileChange) error {
	if change.ChangeType != FileChangeTypeCreated && change.ChangeType != FileChangeTypeModified {
		return nil
	}
	if strings.TrimSpace(change.RelativeURI) == "" {
		return nil
	}

	if !sleepContext(ctx, watcherReportSettleDelay) {
		return ctx.Err()
	}

	state, ok, err := r.currentFileState(ctx, replica.URI, change.RelativeURI)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	reports := replicaFileReports(r.replicaFilesSnapshot(replica.ID), []FileState{state})
	if len(reports) == 0 {
		return nil
	}

	if err := r.client.ReportReplicaFiles(ctx, replica.ID, reports); err != nil {
		return err
	}
	if _, err := r.refreshReplicaFiles(ctx, replica.ID); err != nil {
		return err
	}

	log.Printf("storage runtime watcher reported files replica_id=%d count=%d", replica.ID, len(reports))
	return nil
}

func (r *Runtime) currentFileState(ctx context.Context, replicaURI, relativeURI string) (FileState, bool, error) {
	scanner, err := GetScanner(ctx, replicaURI)
	if err != nil {
		return FileState{}, false, err
	}
	states, err := scanner.Scan(ctx, replicaURI)
	if err != nil {
		return FileState{}, false, err
	}
	for _, state := range states {
		if state.RelativeURI == relativeURI {
			return state, true, nil
		}
	}
	return FileState{}, false, nil
}

func (r *Runtime) replicaFilesSnapshot(replicaID uint) []apiclient.ReplicaInventoryFile {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return append([]apiclient.ReplicaInventoryFile(nil), r.replicaFiles[replicaID]...)
}

// Token refresh loop
func (r *Runtime) refreshLoop(ctx context.Context, pair *apiclient.NodeTokenPair) {
	current := pair
	for {
		delay := refreshDelay(time.Now().UTC(), current.AccessTokenExpiresAt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		nextPair, err := r.client.Refresh(ctx)
		if err != nil {
			log.Printf("storage runtime token refresh failed: %v", err)
			nextPair, err = r.client.Authenticate(ctx)
			if err != nil {
				log.Printf("storage runtime re-authenticate failed: %v", err)
				if !sleepContext(ctx, bootstrapRetryInterval) {
					return
				}
				continue
			}
		}

		current = nextPair
		r.setTransferTokenPublicKey(nextPair.TransferTokenPublicKey)
	}
}

func (r *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report, err := r.client.ReportAvailability(ctx)
			if err != nil {
				log.Printf("storage runtime heartbeat failed: %v", err)
				continue
			}

			for _, command := range report.Commands {
				r.enqueueCommand(command)
			}
		}
	}
}

func (r *Runtime) commandLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		if err := r.listenForCommands(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("storage runtime websocket listener failed: %v", err)
		}

		if !sleepContext(ctx, bootstrapRetryInterval) {
			return
		}
	}
}

func (r *Runtime) listenForCommands(ctx context.Context) error {
	accessToken, err := r.client.AccessToken(ctx)
	if err != nil {
		return err
	}

	wsURL, err := r.client.WebSocketURL(commandWebSocketPath)
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)

	conn, _, err := r.wsDialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return err
	}
	defer conn.Close()

	r.wsConnected.Store(true)
	defer r.wsConnected.Store(false)

	log.Printf("storage runtime websocket connected to %s", wsURL)

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		var command apiclient.Command
		if err := conn.ReadJSON(&command); err != nil {
			return err
		}
		r.enqueueCommand(command)
	}
}

func (r *Runtime) enqueueCommand(command apiclient.Command) {
	log.Printf("storage runtime got command id=%d type=%s status=%s payload=%s", command.ID, command.Type, command.Status, formatPayload(command.Payload))
	r.commandCh <- command
}

func (r *Runtime) commandProcessor(ctx context.Context) {
	pending := make(map[uint]apiclient.Command)
	completed := make(map[uint]struct{})

	for {
		select {
		case <-ctx.Done():
			return
		case command := <-r.commandCh:
			if command.ID == 0 {
				log.Printf("storage runtime ignored command with missing id type=%s", command.Type)
				continue
			}
			if _, ok := completed[command.ID]; ok {
				r.markCommandCompleted(ctx, command.ID)
				continue
			}
			if _, ok := pending[command.ID]; ok {
				continue
			}
			pending[command.ID] = command

			for len(pending) > 0 {
				nextID, next := nextCommand(pending)
				delete(pending, nextID)
				if r.handleCommand(ctx, next) {
					completed[next.ID] = struct{}{}
				}
			}
		}
	}
}

func nextCommand(commands map[uint]apiclient.Command) (uint, apiclient.Command) {
	var minID uint
	var selected apiclient.Command
	for id, command := range commands {
		if minID == 0 || id < minID {
			minID = id
			selected = command
		}
	}
	return minID, selected
}

func (r *Runtime) handleCommand(ctx context.Context, command apiclient.Command) bool {
	switch command.Type {
	case "refresh_state":
		if _, err := r.refreshLocalState(ctx); err != nil {
			r.markCommandFailed(ctx, command.ID, err)
			return false
		}
		return r.markCommandCompleted(ctx, command.ID)
	case "scan_replica":
		if err := r.scanReplica(ctx, command); err != nil {
			r.markCommandFailed(ctx, command.ID, err)
			return false
		}
		return r.markCommandCompleted(ctx, command.ID)
	case "reconcile_replica":
		if err := r.reconcileReplica(ctx, command); err != nil {
			r.markCommandFailed(ctx, command.ID, err)
			return false
		}
		return r.markCommandCompleted(ctx, command.ID)
	default:
		log.Printf("storage runtime command type not implemented id=%d type=%s", command.ID, command.Type)
		return false
	}
}

type scanReplicaCommandPayload struct {
	ReplicaID uint `json:"replica_id"`
}

type reconcileReplicaCommandPayload struct {
	SourceNodeAddress    string `json:"source_node_address"`
	SourceNodeID         string `json:"source_node_id"`
	SourceReplicaID      uint   `json:"source_replica_id"`
	DestinationReplicaID uint   `json:"destination_replica_id"`
	TransferToken        string `json:"transfer_token"`
}

func (r *Runtime) reconcileReplica(ctx context.Context, command apiclient.Command) error {
	var payload reconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return fmt.Errorf("invalid reconcile_replica payload: %w", err)
	}
	if payload.SourceNodeAddress == "" || payload.SourceNodeID == "" || payload.SourceReplicaID == 0 || payload.DestinationReplicaID == 0 || payload.TransferToken == "" {
		return fmt.Errorf("invalid reconcile_replica payload: missing required field")
	}
	r.setReplicaTransferToken(payload.DestinationReplicaID, payload.TransferToken)

	if _, err := r.refreshLocalState(ctx); err != nil {
		return err
	}

	destination, ok := r.findReplica(payload.DestinationReplicaID)
	if !ok {
		return fmt.Errorf("destination replica %d not found in local state", payload.DestinationReplicaID)
	}

	writer, err := GetWriter(ctx, destination.URI)
	if err != nil {
		return err
	}

	pendingFiles, err := r.client.ListReplicaInventoryFiles(ctx, payload.DestinationReplicaID, "pending")
	if err != nil {
		return err
	}
	for _, pendingFile := range pendingFiles {
		if pendingFile.InventoryVersion == 0 {
			return fmt.Errorf("replica %d file %d has missing inventory version", payload.DestinationReplicaID, pendingFile.FileID)
		}
	}

	var failures []string
	for _, pendingFile := range pendingFiles {
		token := r.replicaTransferToken(payload.DestinationReplicaID)
		content, err := r.client.TransferReplicaFileContent(ctx, payload.SourceNodeAddress, payload.SourceReplicaID, pendingFile.FileID, pendingFile.InventoryVersion, token)
		if err != nil {
			failure := fmt.Errorf("transfer replica_id=%d file_id=%d version=%d: %w", payload.DestinationReplicaID, pendingFile.FileID, pendingFile.InventoryVersion, err)
			if isReconcileAuthError(err) {
				return failure
			}
			if isReconcileFileError(err) {
				if markErr := r.markReplicaFileError(ctx, payload.DestinationReplicaID, pendingFile.FileID, failure); markErr != nil {
					return markErr
				}
			}
			failures = append(failures, failure.Error())
			continue
		}

		saveErr := writer.Save(ctx, destination.URI, pendingFile.RelativeURI, content)
		closeErr := content.Close()
		if saveErr != nil {
			failure := fmt.Errorf("write replica_id=%d file_id=%d relative_uri=%s: %w", payload.DestinationReplicaID, pendingFile.FileID, pendingFile.RelativeURI, saveErr)
			if isReconcileAuthError(saveErr) {
				return failure
			}
			if isReconcileFileError(saveErr) {
				if markErr := r.markReplicaFileError(ctx, payload.DestinationReplicaID, pendingFile.FileID, failure); markErr != nil {
					return markErr
				}
			}
			failures = append(failures, failure.Error())
			continue
		}
		if closeErr != nil {
			failure := fmt.Errorf("close transfer content replica_id=%d file_id=%d: %w", payload.DestinationReplicaID, pendingFile.FileID, closeErr)
			if isReconcileAuthError(closeErr) {
				return failure
			}
			if isReconcileFileError(closeErr) {
				if markErr := r.markReplicaFileError(ctx, payload.DestinationReplicaID, pendingFile.FileID, failure); markErr != nil {
					return markErr
				}
			}
			failures = append(failures, failure.Error())
			continue
		}

		version := pendingFile.InventoryVersion
		if err := r.client.UpdateReplicaFileStatus(ctx, payload.DestinationReplicaID, pendingFile.FileID, "synchronized", &version, nil); err != nil {
			failure := fmt.Errorf("mark synchronized replica_id=%d file_id=%d version=%d: %w", payload.DestinationReplicaID, pendingFile.FileID, version, err)
			if isReconcileAuthError(err) {
				return failure
			}
			failures = append(failures, failure.Error())
			continue
		}
		r.markLocalReplicaFileSynchronized(payload.DestinationReplicaID, pendingFile.FileID, version)
		log.Printf("storage runtime reconcile_replica copied file replica_id=%d file_id=%d relative_uri=%s version=%d", payload.DestinationReplicaID, pendingFile.FileID, pendingFile.RelativeURI, version)
	}

	if len(failures) > 0 {
		return fmt.Errorf("reconcile_replica failed files=%d errors=%s", len(failures), strings.Join(failures, "; "))
	}

	log.Printf("storage runtime reconcile_replica completed replica_id=%d files=%d source_replica_id=%d source_node_id=%s", payload.DestinationReplicaID, len(pendingFiles), payload.SourceReplicaID, payload.SourceNodeID)
	return nil
}

func (r *Runtime) markReplicaFileError(ctx context.Context, replicaID, fileID uint, fileErr error) error {
	message := fileErr.Error()
	if err := r.client.UpdateReplicaFileStatus(ctx, replicaID, fileID, "error", nil, &message); err != nil {
		return fmt.Errorf("mark error replica_id=%d file_id=%d: %w", replicaID, fileID, err)
	}
	return nil
}

func isReconcileAuthError(err error) bool {
	var apiErr *apiclient.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

func isReconcileFileError(err error) bool {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist)
}

func (r *Runtime) markLocalReplicaFileSynchronized(replicaID, fileID, version uint) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	files := r.replicaFiles[replicaID]
	for i := range files {
		if files[i].FileID == fileID {
			files[i].ReplicaStatus = "synchronized"
			files[i].ReplicaVersion = version
			files[i].InventoryVersion = version
			break
		}
	}
	r.replicaFiles[replicaID] = files
}

func (r *Runtime) scanReplica(ctx context.Context, command apiclient.Command) error {
	var payload scanReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return fmt.Errorf("invalid scan_replica payload: %w", err)
	}
	if payload.ReplicaID == 0 {
		return fmt.Errorf("invalid scan_replica payload: missing replica_id")
	}

	replica, ok := r.findReplica(payload.ReplicaID)
	if !ok {
		return fmt.Errorf("replica %d not found in local state", payload.ReplicaID)
	}

	files, err := r.refreshReplicaFiles(ctx, payload.ReplicaID)
	if err != nil {
		return err
	}

	scanner, err := GetScanner(ctx, replica.URI)
	if err != nil {
		return err
	}
	states, err := scanner.Scan(ctx, replica.URI)
	if err != nil {
		return err
	}

	reports := replicaFileReports(files, states)
	if len(reports) == 0 {
		log.Printf("storage runtime scan_replica detected no reportable changes replica_id=%d", payload.ReplicaID)
		return nil
	}

	if err := r.client.ReportReplicaFiles(ctx, payload.ReplicaID, reports); err != nil {
		return err
	}
	log.Printf("storage runtime scan_replica reported files replica_id=%d count=%d", payload.ReplicaID, len(reports))
	return nil
}

func replicaFileReports(files []apiclient.ReplicaInventoryFile, states []FileState) []apiclient.ReplicaFileReport {
	activeFilesByURI := make(map[string]apiclient.ReplicaInventoryFile, len(files))
	for _, file := range files {
		if file.InventoryStatus != "active" {
			continue
		}
		activeFilesByURI[file.RelativeURI] = file
	}

	reports := make([]apiclient.ReplicaFileReport, 0)
	for _, state := range states {
		file, ok := activeFilesByURI[state.RelativeURI]
		if ok && sameReplicaFileContent(file, state) {
			continue
		}

		report := apiclient.ReplicaFileReport{
			RelativeURI:  state.RelativeURI,
			FileSize:     state.Size,
			FileHash:     state.Hash,
			CreatedTime:  state.Created,
			ModifiedTime: state.Modified,
		}
		if ok {
			fileID := file.FileID
			report.FileID = &fileID
		}
		reports = append(reports, report)
	}
	return reports
}

func sameReplicaFileContent(file apiclient.ReplicaInventoryFile, state FileState) bool {
	return file.RelativeURI == state.RelativeURI &&
		file.Size == state.Size &&
		file.Hash == state.Hash
}

func (r *Runtime) markCommandCompleted(ctx context.Context, commandID uint) bool {
	if _, err := r.client.UpdateCommand(ctx, commandID, "completed", nil); err != nil {
		log.Printf("storage runtime command completion failed id=%d error=%v", commandID, err)
		return false
	}
	log.Printf("storage runtime command completed id=%d", commandID)
	return true
}

func (r *Runtime) markCommandFailed(ctx context.Context, commandID uint, commandErr error) {
	message := commandErr.Error()
	if _, err := r.client.UpdateCommand(ctx, commandID, "failed", &message); err != nil {
		log.Printf("storage runtime command failure report failed id=%d command_error=%v report_error=%v", commandID, commandErr, err)
		return
	}
	log.Printf("storage runtime command failed id=%d error=%v", commandID, commandErr)
}

func formatPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return "{}"
	}
	return string(payload)
}

func formatFileState(state *FileState) string {
	if state == nil {
		return "{}"
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func refreshDelay(now, expiresAt time.Time) time.Duration {
	until := expiresAt.Sub(now)
	if until <= time.Second {
		return time.Second
	}

	lead := time.Minute
	if until <= 2*time.Minute {
		lead = until / 2
	}

	delay := until - lead
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
