package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
	"replica/internal/service"

	"github.com/gorilla/websocket"
)

const bootstrapRetryInterval = 5 * time.Second
const commandWebSocketPath = "/node/nodes/ws"
const watcherReportSettleDelay = 250 * time.Millisecond

type Runtime struct {
	client            *apiclient.Client
	heartbeatInterval time.Duration
	wsConnected       atomic.Bool
	wsDialer          *websocket.Dialer

	stateMu         sync.RWMutex
	replicas        []apiclient.Replica
	shares          []apiclient.Share
	config          []apiclient.ConfigItem
	storageProfiles map[string]config.StorageProfileConfig
	replicaFiles    map[uint][]apiclient.ReplicaInventoryFile
	transferKey     string
	nodePublicKey   string
	nodePrivateKey  string
	transferTokens  map[uint]string

	shareAuthMu                sync.Mutex
	shareTokenCache            map[string]shareTokenCacheEntry
	shareAPITokenCacheDuration time.Duration

	watcherMu sync.Mutex
	watchers  map[uint]*runningReplicaWatcher

	commandCh chan apiclient.Command

	cfg       config.Config
	thumbnail *service.ThumbnailService
}

type runningReplicaWatcher struct {
	uri                string
	inventoryType      string
	targetRelativeURIs []string
	cancel             context.CancelFunc
}

type shareTokenCacheEntry struct {
	userID      uint
	username    string
	status      string
	tokenExpiry time.Time
	validatedAt time.Time
	expiresAt   time.Time
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	client, err := apiclient.New(cfg)
	if err != nil {
		return nil, err
	}
	nodePublicKey, nodePrivateKey, err := service.GenerateTransferKeyPairPEM()
	if err != nil {
		return nil, err
	}
	client.SetNodePublicKey(nodePublicKey)

	return &Runtime{
		client:                     client,
		heartbeatInterval:          cfg.App.HeartbeatInterval,
		wsDialer:                   websocket.DefaultDialer,
		storageProfiles:            make(map[string]config.StorageProfileConfig),
		replicaFiles:               make(map[uint][]apiclient.ReplicaInventoryFile),
		nodePublicKey:              nodePublicKey,
		nodePrivateKey:             nodePrivateKey,
		transferTokens:             make(map[uint]string),
		shareTokenCache:            make(map[string]shareTokenCacheEntry),
		shareAPITokenCacheDuration: cfg.Auth.ShareAPITokenCacheDuration,
		watchers:                   make(map[uint]*runningReplicaWatcher),
		commandCh:                  make(chan apiclient.Command, 128),
		cfg:                        cfg,
		thumbnail:                  service.NewThumbnailService(cfg),
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

		err = r.refreshLocalState(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, nil, nil, false
			}
			log.Printf("storage runtime state bootstrap failed: %v", err)
			continue
		}
		if err := r.reportStartupLocalChanges(ctx); err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, nil, nil, false
			}
			log.Printf("storage runtime startup scan failed: %v", err)
			continue
		}

		log.Printf("storage runtime connected to coordinator as node_id=%s replicas=%d", r.client.NodeID(), len(r.replicas))
		return pair, r.replicas, report.Commands, true
	}
}

func (r *Runtime) refreshLocalState(ctx context.Context) error {
	replicas, err := r.client.ListOwnReplicas(ctx)
	if err != nil {
		return err
	}
	shares, err := r.client.ListOwnShares(ctx)
	if err != nil {
		return err
	}

	replicaFiles := make(map[uint][]apiclient.ReplicaInventoryFile, len(replicas))
	for _, replica := range replicas {
		if replicaIsActive(replica) {
			files, err := r.client.ListReplicaInventoryFiles(ctx, replica.ID)
			if err != nil {
				return err
			}
			replicaFiles[replica.ID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
		}
	}

	r.setLocalState(replicas, shares, replicaFiles)
	if err := r.refreshLocalConfig(ctx); err != nil {
		return err
	}
	log.Printf("storage runtime refreshed local state replicas=%d shares=%d", len(replicas), len(shares))
	return nil
}

func (r *Runtime) refreshLocalConfig(ctx context.Context) error {
	items, err := r.client.GetConfig(ctx)
	if err != nil {
		return err
	}
	encryptedProfiles, err := r.client.GetStorageProfiles(ctx)
	if err != nil {
		return err
	}
	profiles, err := decryptStorageProfiles(r.nodePrivateKey, encryptedProfiles)
	if err != nil {
		return err
	}
	r.setLocalConfig(items, profiles)
	log.Printf("storage runtime refreshed config items=%d storage_profiles=%d", len(items), len(profiles))
	return nil
}

func (r *Runtime) refreshReplicaFiles(ctx context.Context, replicaID uint) ([]apiclient.ReplicaInventoryFile, error) {
	if replica, ok := r.findReplica(replicaID); ok && !replicaIsActive(replica) {
		r.setReplicaFiles(replicaID, nil)
		return nil, nil
	}
	files, err := r.client.ListReplicaInventoryFiles(ctx, replicaID)
	if err != nil {
		return nil, err
	}
	r.setReplicaFiles(replicaID, files)
	return files, nil
}

func (r *Runtime) getReplicaFiles(id uint) map[string]FileState {
	files := r.replicaFiles[id]

	result := make(map[string]FileState, len(files))

	for _, file := range files {
		result[file.RelativeURI] = FileState{
			RelativeURI: file.RelativeURI,
			Size:        file.Size,
			Hash:        file.Hash,
			Created:     file.Created,
			Modified:    file.Modified,
		}
	}

	return result
}

func (r *Runtime) reportStartupLocalChanges(ctx context.Context) error {
	for _, replica := range r.replicas {
		if !replicaIsActive(replica) {
			log.Printf("storage runtime startup scan skipped inactive replica_id=%d status=%s uri=%s", replica.ID, replica.Status, replica.URI)
			continue
		}
		if replicaHasPendingFiles(r.replicaFilesSnapshot(replica.ID)) {
			log.Printf("storage runtime startup scan skipped pending replica_id=%d uri=%s", replica.ID, replica.URI)
			continue
		}

		scanner, err := GetScanner(ctx, replica.URI)
		if err != nil {
			return fmt.Errorf("startup scanner replica_id=%d uri=%s: %w", replica.ID, replica.URI, err)
		}

		targetRelativeURIs, err := replicaScanTargets(replica, r.replicaFilesSnapshot(replica.ID))
		if err != nil {
			return fmt.Errorf("startup scanner replica_id=%d uri=%s: %w", replica.ID, replica.URI, err)
		}
		states, err := scanner.Scan(ctx, replica.URI, r.getReplicaFiles(replica.ID), targetRelativeURIs...)
		if err != nil {
			return fmt.Errorf("startup scan replica_id=%d uri=%s: %w", replica.ID, replica.URI, err)
		}

		reports := replicaFileReports(r.replicaFilesSnapshot(replica.ID), states)
		if len(reports) == 0 {
			log.Printf("storage runtime startup scan detected no reportable changes replica_id=%d", replica.ID)
			continue
		}

		if err := r.client.ReportReplicaFiles(ctx, replica.ID, reports); err != nil {
			return fmt.Errorf("startup report replica_id=%d: %w", replica.ID, err)
		}
		if _, err := r.refreshReplicaFiles(ctx, replica.ID); err != nil {
			return fmt.Errorf("startup refresh replica_id=%d: %w", replica.ID, err)
		}
		log.Printf("storage runtime startup scan reported files replica_id=%d count=%d", replica.ID, len(reports))
	}
	return nil
}

func (r *Runtime) setLocalState(replicas []apiclient.Replica, shares []apiclient.Share, replicaFiles map[uint][]apiclient.ReplicaInventoryFile) {
	if replicaFiles == nil {
		replicaFiles = make(map[uint][]apiclient.ReplicaInventoryFile)
	}

	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.replicas = append([]apiclient.Replica(nil), replicas...)
	r.shares = append([]apiclient.Share(nil), shares...)
	r.replicaFiles = make(map[uint][]apiclient.ReplicaInventoryFile, len(replicaFiles))
	for replicaID, files := range replicaFiles {
		r.replicaFiles[replicaID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
	}
}

func (r *Runtime) setLocalConfig(items []apiclient.ConfigItem, storageProfiles map[string]config.StorageProfileConfig) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.config = append([]apiclient.ConfigItem(nil), items...)
	r.storageProfiles = cloneStorageProfiles(storageProfiles)
	r.cfg = configFromNodeItems(r.cfg, items)
	r.cfg.Storage.Profiles = cloneStorageProfiles(storageProfiles)
	r.thumbnail = service.NewThumbnailService(r.cfg)
}

func (r *Runtime) configSnapshot() []apiclient.ConfigItem {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return append([]apiclient.ConfigItem(nil), r.config...)
}

func (r *Runtime) storageProfilesSnapshot() map[string]config.StorageProfileConfig {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return cloneStorageProfiles(r.storageProfiles)
}

func (r *Runtime) thumbnailSnapshot() (*service.ThumbnailService, config.Config) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.thumbnail, r.cfg
}

func (r *Runtime) sharesSnapshot() []apiclient.Share {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return append([]apiclient.Share(nil), r.shares...)
}

func (r *Runtime) validateShareAPIToken(ctx context.Context, token string) (*apiclient.ValidatedUserToken, error) {
	hash := shareTokenHash(token)
	now := time.Now().UTC()

	r.shareAuthMu.Lock()
	if entry, ok := r.shareTokenCache[hash]; ok && now.Before(entry.expiresAt) {
		result := &apiclient.ValidatedUserToken{
			UserID:               entry.userID,
			Username:             entry.username,
			Status:               entry.status,
			AccessTokenExpiresAt: entry.tokenExpiry,
		}
		r.shareAuthMu.Unlock()
		return result, nil
	}
	r.shareAuthMu.Unlock()

	validated, err := r.client.ValidateUserToken(ctx, token)
	if err != nil {
		return nil, err
	}

	cacheDuration := r.shareAPITokenCacheDuration
	if cacheDuration <= 0 {
		cacheDuration = 5 * time.Minute
	}
	cacheExpires := now.Add(cacheDuration)
	if validated.AccessTokenExpiresAt.Before(cacheExpires) {
		cacheExpires = validated.AccessTokenExpiresAt
	}
	if !cacheExpires.After(now) {
		return nil, errShareTokenInvalid
	}

	r.shareAuthMu.Lock()
	if r.shareTokenCache == nil {
		r.shareTokenCache = make(map[string]shareTokenCacheEntry)
	}
	r.shareTokenCache[hash] = shareTokenCacheEntry{
		userID:      validated.UserID,
		username:    validated.Username,
		status:      validated.Status,
		tokenExpiry: validated.AccessTokenExpiresAt,
		validatedAt: now,
		expiresAt:   cacheExpires,
	}
	r.shareAuthMu.Unlock()

	return validated, nil
}

func shareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) setReplicaFiles(replicaID uint, files []apiclient.ReplicaInventoryFile) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	if r.replicaFiles == nil {
		r.replicaFiles = make(map[uint][]apiclient.ReplicaInventoryFile)
	}
	r.replicaFiles[replicaID] = append([]apiclient.ReplicaInventoryFile(nil), files...)
}

func configFromNodeItems(cfg config.Config, items []apiclient.ConfigItem) config.Config {
	for _, item := range items {
		switch item.Key {
		case config.SettingSharingThumbnailSizes:
			var value []int
			if err := json.Unmarshal(item.Value, &value); err == nil && len(value) > 0 {
				cfg.Sharing.ThumbnailSizes = append([]int(nil), value...)
			}
		case config.SettingSharingThumbnailDefaultSize:
			var value int
			if err := json.Unmarshal(item.Value, &value); err == nil && value > 0 {
				cfg.Sharing.ThumbnailDefaultSize = value
			}
		case config.SettingSharingThumbnailsGenerateForVideo:
			var value bool
			if err := json.Unmarshal(item.Value, &value); err == nil {
				cfg.Sharing.ThumbnailsGenerateForVideo = value
			}
		case config.SettingSharingVideoInlineMaxSizeMB:
			var value int
			if err := json.Unmarshal(item.Value, &value); err == nil && value > 0 {
				cfg.Sharing.VideoInlineMaxSizeMB = value
			}
		case config.SettingSharingVideoPlaybackEnabled:
			var value bool
			if err := json.Unmarshal(item.Value, &value); err == nil {
				cfg.Sharing.VideoPlaybackEnabled = value
			}
		}
	}
	return cfg
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

func (r *Runtime) nodeTransferPublicKey() string {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.nodePublicKey
}

func (r *Runtime) nodeTransferPrivateKey() string {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.nodePrivateKey
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
		if err := r.ensureReplicaWatcher(ctx, replica); err != nil {
			log.Printf("storage runtime watcher setup skipped replica_id=%d uri=%s error=%v", replica.ID, replica.URI, err)
		}
	}
}

func (r *Runtime) ensureReplicaWatcher(ctx context.Context, replica apiclient.Replica) error {
	if !replicaIsActive(replica) {
		return fmt.Errorf("inactive replica status=%s", replica.Status)
	}

	targetRelativeURIs, err := replicaWatcherTargets(replica, r.replicaFilesSnapshot(replica.ID))
	if err != nil {
		return err
	}

	r.watcherMu.Lock()
	defer r.watcherMu.Unlock()
	if r.replicaWatcherExistsLocked(replica.ID) {
		return nil
	}

	watcher, err := GetWatcher(ctx, replica.URI)
	if err != nil {
		return err
	}
	watcherCtx, cancel := context.WithCancel(ctx)
	changeCh, errCh, err := watcher.Watch(watcherCtx, replica.URI, targetRelativeURIs)
	if err != nil {
		cancel()
		return err
	}

	running := &runningReplicaWatcher{
		uri:                replica.URI,
		inventoryType:      replica.InventoryType,
		targetRelativeURIs: targetRelativeURIs,
		cancel:             cancel,
	}
	r.watchers[replica.ID] = running
	log.Printf("storage runtime watcher started replica_id=%d uri=%s", replica.ID, replica.URI)

	go r.consumeReplicaWatcher(watcherCtx, replica, running, changeCh, errCh)
	return nil
}

func (r *Runtime) replicaWatcherExists(replicaID uint) bool {
	r.watcherMu.Lock()
	defer r.watcherMu.Unlock()
	return r.replicaWatcherExistsLocked(replicaID)
}

func (r *Runtime) replicaWatcherExistsLocked(replicaID uint) bool {
	_, exists := r.watchers[replicaID]
	return exists
}

func (r *Runtime) stopReplicaWatcher(replicaID uint) {
	r.watcherMu.Lock()
	running, exists := r.watchers[replicaID]
	if exists {
		delete(r.watchers, replicaID)
	}
	r.watcherMu.Unlock()

	if exists {
		running.cancel()
		log.Printf("storage runtime watcher stopped replica_id=%d", replicaID)
	}
}

func (r *Runtime) stopDeletedReplicaWatchers() {
	r.stateMu.RLock()
	deletedReplicaIDs := make([]uint, 0)
	for _, replica := range r.replicas {
		if replica.Status == "deleted" {
			deletedReplicaIDs = append(deletedReplicaIDs, replica.ID)
		}
	}
	r.stateMu.RUnlock()

	for _, replicaID := range deletedReplicaIDs {
		r.stopReplicaWatcher(replicaID)
	}
}

func (r *Runtime) consumeReplicaWatcher(ctx context.Context, replica apiclient.Replica, running *runningReplicaWatcher, changeCh <-chan FileChange, errCh <-chan error) {
	defer func() {
		r.watcherMu.Lock()
		if r.watchers[replica.ID] == running {
			delete(r.watchers, replica.ID)
		}
		r.watcherMu.Unlock()
	}()

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
}

func (r *Runtime) reportWatcherChange(ctx context.Context, replica apiclient.Replica, change FileChange) error {
	currentReplica, ok := r.findReplica(replica.ID)
	if !ok || !replicaIsActive(currentReplica) {
		return nil
	}
	if change.ChangeType != FileChangeTypeCreated && change.ChangeType != FileChangeTypeModified && change.ChangeType != FileChangeTypeDeleted {
		return nil
	}
	if strings.TrimSpace(change.RelativeURI) == "" {
		return nil
	}
	if currentReplica.UpstreamReplicaID == nil && replicaHasPendingFiles(r.replicaFilesSnapshot(replica.ID)) {
		return nil
	}

	if change.ChangeType == FileChangeTypeDeleted {
		report, ok := deletedReplicaFileReport(r.replicaFilesSnapshot(replica.ID), change.RelativeURI)
		if !ok {
			return nil
		}
		if err := r.client.ReportReplicaFiles(ctx, replica.ID, []apiclient.ReplicaFileReport{report}); err != nil {
			return err
		}
		if _, err := r.refreshReplicaFiles(ctx, replica.ID); err != nil {
			return err
		}
		log.Printf("storage runtime watcher reported files replica_id=%d count=%d", replica.ID, 1)
		return nil
	}

	if !sleepContext(ctx, watcherReportSettleDelay) {
		return ctx.Err()
	}

	var state FileState
	if change.State != nil && change.State.RelativeURI == change.RelativeURI {
		state = *change.State
	} else {
		var ok bool
		var err error
		state, ok, err = r.currentFileState(ctx, replica, change.RelativeURI, r.getReplicaFiles(replica.ID))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	reports := replicaFileReportsForStates(r.replicaFilesSnapshot(replica.ID), []FileState{state}, false)
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

func (r *Runtime) currentFileState(ctx context.Context, replica apiclient.Replica, relativeURI string, oldStates map[string]FileState) (FileState, bool, error) {
	scanner, err := GetScanner(ctx, replica.URI)
	if err != nil {
		return FileState{}, false, err
	}
	targetRelativeURIs, err := replicaScanTargets(replica, r.replicaFilesSnapshot(replica.ID))
	if err != nil {
		return FileState{}, false, err
	}
	states, err := scanner.Scan(ctx, replica.URI, oldStates, targetRelativeURIs...)
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
		if err := r.refreshLocalState(ctx); err != nil {
			r.markCommandFailed(ctx, command.ID, err)
			return false
		}
		r.stopDeletedReplicaWatchers()
		return r.markCommandCompleted(ctx, command.ID)
	case "refresh_config":
		if err := r.refreshLocalConfig(ctx); err != nil {
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
	SourceNodeAddress    string   `json:"source_node_address"`
	SourceNodeID         string   `json:"source_node_id"`
	SourceReplicaID      uint     `json:"source_replica_id"`
	DestinationReplicaID uint     `json:"destination_replica_id"`
	TransferToken        string   `json:"transfer_token"`
	DeleteRelativeURIs   []string `json:"delete_relative_uris"`
}

func (r *Runtime) reconcileReplica(ctx context.Context, command apiclient.Command) error {
	var payload reconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return fmt.Errorf("invalid reconcile_replica payload: %w", err)
	}
	if payload.SourceNodeAddress == "" || payload.SourceNodeID == "" || payload.SourceReplicaID == 0 || payload.DestinationReplicaID == 0 || payload.TransferToken == "" {
		return fmt.Errorf("invalid reconcile_replica payload: missing required field")
	}

	r.stopReplicaWatcher(payload.DestinationReplicaID) // stop watcher during reconcile

	r.setReplicaTransferToken(payload.DestinationReplicaID, payload.TransferToken)

	if err := r.refreshLocalState(ctx); err != nil {
		return err
	}

	destination, ok := r.findReplica(payload.DestinationReplicaID)
	if !ok {
		return fmt.Errorf("destination replica %d not found in local state", payload.DestinationReplicaID)
	}
	if !replicaIsActive(destination) {
		log.Printf("storage runtime reconcile_replica skipped inactive replica_id=%d status=%s uri=%s", destination.ID, destination.Status, destination.URI)
		return nil
	}

	writer, err := GetWriter(ctx, destination.URI)
	if err != nil {
		return err
	}
	for _, relativeURI := range payload.DeleteRelativeURIs {
		if err := writer.Delete(ctx, destination.URI, relativeURI); err != nil {
			return fmt.Errorf("delete unknown replica_id=%d relative_uri=%s: %w", payload.DestinationReplicaID, relativeURI, err)
		}
		log.Printf("storage runtime reconcile_replica deleted unknown file replica_id=%d relative_uri=%s", payload.DestinationReplicaID, relativeURI)
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
		if pendingFile.InventoryStatus == "deleted" {
			if err := writer.Delete(ctx, destination.URI, pendingFile.RelativeURI); err != nil {
				failure := fmt.Errorf("delete replica_id=%d file_id=%d relative_uri=%s: %w", payload.DestinationReplicaID, pendingFile.FileID, pendingFile.RelativeURI, err)
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
			log.Printf("storage runtime reconcile_replica deleted file replica_id=%d file_id=%d relative_uri=%s version=%d", payload.DestinationReplicaID, pendingFile.FileID, pendingFile.RelativeURI, version)
			continue
		}

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

		saveErr := writer.Save(ctx, destination.URI, pendingFile.RelativeURI, content, pendingFile.Size)
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

	// enable watcher
	replica, ok := r.findReplica(payload.DestinationReplicaID)
	if ok {
		watcherErr := r.ensureReplicaWatcher(ctx, replica)
		if watcherErr != nil {
			return fmt.Errorf("error starting watcher for replica_id=%d, error=%s", payload.DestinationReplicaID, watcherErr)
		}
	} else {
		return fmt.Errorf("replica_id=%d not found in local state", payload.DestinationReplicaID)
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
		if err := r.refreshLocalState(ctx); err != nil {
			return err
		}

		replica, ok = r.findReplica(payload.ReplicaID)
		if !ok {
			return fmt.Errorf("replica %d not found in local state", payload.ReplicaID)
		}
	}
	if !replicaIsActive(replica) {
		log.Printf("storage runtime scan_replica skipped inactive replica_id=%d status=%s uri=%s", replica.ID, replica.Status, replica.URI)
		return nil
	}

	files, err := r.refreshReplicaFiles(ctx, payload.ReplicaID)
	if err != nil {
		return err
	}

	scanner, err := GetScanner(ctx, replica.URI)
	if err != nil {
		return err
	}
	targetRelativeURIs, err := replicaScanTargets(replica, files)
	if err != nil {
		return err
	}
	states, err := scanner.Scan(ctx, replica.URI, r.getReplicaFiles(replica.ID), targetRelativeURIs...)
	if err != nil {
		return err
	}

	reports := replicaFileReports(files, states)
	if len(reports) > 0 {
		if err := r.client.ReportReplicaFiles(ctx, payload.ReplicaID, reports); err != nil {
			return err
		}
		log.Printf("storage runtime scan_replica reported files replica_id=%d count=%d", payload.ReplicaID, len(reports))
	} else {
		log.Printf("storage runtime scan_replica detected no reportable changes replica_id=%d", payload.ReplicaID)
	}

	if err := r.ensureReplicaWatcher(ctx, replica); err != nil {
		return fmt.Errorf("ensure watcher replica_id=%d: %w", replica.ID, err)
	}
	return nil
}

func replicaFileReports(files []apiclient.ReplicaInventoryFile, states []FileState) []apiclient.ReplicaFileReport {
	return replicaFileReportsForStates(files, states, true)
}

func replicaIsActive(replica apiclient.Replica) bool {
	return replica.Status == "" || replica.Status == "active"
}

func replicaScanTargets(replica apiclient.Replica, files []apiclient.ReplicaInventoryFile) ([]string, error) {
	if replica.InventoryType != "file" {
		return nil, nil
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("file inventory replica must have at least one inventory file")
	}
	targets := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.RelativeURI) == "" {
			return nil, fmt.Errorf("file inventory replica has empty relative uri")
		}
		targets = append(targets, file.RelativeURI)
	}
	return targets, nil
}

func replicaWatcherTargets(replica apiclient.Replica, files []apiclient.ReplicaInventoryFile) ([]string, error) {
	return replicaScanTargets(replica, files)
}

func replicaHasPendingFiles(files []apiclient.ReplicaInventoryFile) bool {
	for _, file := range files {
		if file.ReplicaStatus == "pending" {
			return true
		}
	}
	return false
}

func replicaFileReportsForStates(files []apiclient.ReplicaInventoryFile, states []FileState, includeDeletes bool) []apiclient.ReplicaFileReport {
	activeFilesByURI := make(map[string]apiclient.ReplicaInventoryFile, len(files))
	for _, file := range files {
		if file.InventoryStatus == "active" {
			activeFilesByURI[file.RelativeURI] = file
		}
	}

	reports := make([]apiclient.ReplicaFileReport, 0)
	seenURIs := make(map[string]struct{}, len(states))
	for _, state := range states {
		seenURIs[state.RelativeURI] = struct{}{}
		file, ok := activeFilesByURI[state.RelativeURI]
		if ok && sameReplicaFileContent(file, state) {
			continue
		}

		action := "created"
		var fileID *uint
		if ok {
			action = "updated"
			id := file.FileID
			fileID = &id
		}
		fileSize := state.Size
		fileHash := state.Hash
		createdTime := state.Created
		modifiedTime := state.Modified
		report := apiclient.ReplicaFileReport{
			Action:       action,
			RelativeURI:  state.RelativeURI,
			FileID:       fileID,
			FileSize:     &fileSize,
			FileHash:     &fileHash,
			CreatedTime:  &createdTime,
			ModifiedTime: &modifiedTime,
		}
		reports = append(reports, report)
	}
	if includeDeletes {
		for _, file := range files {
			if file.InventoryStatus != "active" {
				continue
			}
			if _, ok := seenURIs[file.RelativeURI]; ok {
				continue
			}
			report, ok := deletedReplicaFileReport([]apiclient.ReplicaInventoryFile{file}, file.RelativeURI)
			if ok {
				reports = append(reports, report)
			}
		}
	}
	return reports
}

func deletedReplicaFileReport(files []apiclient.ReplicaInventoryFile, relativeURI string) (apiclient.ReplicaFileReport, bool) {
	for _, file := range files {
		if file.InventoryStatus != "active" || file.RelativeURI != relativeURI {
			continue
		}
		fileID := file.FileID
		return apiclient.ReplicaFileReport{
			FileID:      &fileID,
			Action:      "deleted",
			RelativeURI: file.RelativeURI,
		}, true
	}
	return apiclient.ReplicaFileReport{}, false
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

func decryptStorageProfiles(privateKeyPEM string, encryptedProfiles []apiclient.EncryptedStorageProfile) (map[string]config.StorageProfileConfig, error) {
	privateKey, err := parseStorageProfilePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}

	profiles := make(map[string]config.StorageProfileConfig, len(encryptedProfiles))
	for _, encryptedProfile := range encryptedProfiles {
		name := strings.TrimSpace(encryptedProfile.Name)
		if name == "" {
			continue
		}
		profile, err := decryptStorageProfile(privateKey, encryptedProfile)
		if err != nil {
			return nil, fmt.Errorf("decrypt storage profile %q: %w", name, err)
		}
		profiles[name] = profile
	}
	return profiles, nil
}

func parseStorageProfilePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("missing storage profile private key")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return privateKey, nil
}

func decryptStorageProfile(privateKey *rsa.PrivateKey, encryptedProfile apiclient.EncryptedStorageProfile) (config.StorageProfileConfig, error) {
	encryptedKey, err := base64.StdEncoding.DecodeString(encryptedProfile.EncryptedKey)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	nonce, err := base64.StdEncoding.DecodeString(encryptedProfile.Nonce)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	payload, err := base64.StdEncoding.DecodeString(encryptedProfile.Payload)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}
	plaintext, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return config.StorageProfileConfig{}, err
	}

	var plaintextProfile struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	if err := json.Unmarshal(plaintext, &plaintextProfile); err != nil {
		return config.StorageProfileConfig{}, err
	}
	return config.StorageProfileConfig{
		Endpoint:        plaintextProfile.Endpoint,
		Region:          plaintextProfile.Region,
		AccessKeyID:     plaintextProfile.AccessKeyID,
		SecretAccessKey: plaintextProfile.SecretAccessKey,
	}, nil
}

func cloneStorageProfiles(profiles map[string]config.StorageProfileConfig) map[string]config.StorageProfileConfig {
	cloned := make(map[string]config.StorageProfileConfig, len(profiles))
	for name, profile := range profiles {
		cloned[name] = profile
	}
	return cloned
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
