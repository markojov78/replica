package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"replica/internal/apiclient"

	"github.com/gorilla/websocket"
)

func TestRemovableAvailability(t *testing.T) {
	for _, name := range []string{"missing", "empty", "temporary only", "file", "subdirectory"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			switch name {
			case "missing":
				root = filepath.Join(root, "absent")
			case "temporary only":
				writeRemovableTestFile(t, root, TemporaryWritePrefix+"test", "partial")
			case "file":
				writeRemovableTestFile(t, root, "file.txt", "data")
			case "subdirectory":
				if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := checkReplicaAvailable(apiclient.Replica{Type: "removable", URI: root})
			wantPresent := name == "file" || name == "subdirectory"
			if wantPresent && err != nil || !wantPresent && !errors.Is(err, errRemovableUnavailable) {
				t.Fatalf("availability = %v, want present=%v", err, wantPresent)
			}
			if err := checkReplicaAvailable(apiclient.Replica{Type: "filesystem", URI: root}); err != nil {
				t.Fatalf("ordinary filesystem availability changed: %v", err)
			}
		})
	}
}

func TestRemovableTargetedScanRejectsAbsentRoot(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, targets := range [][]string{{"a.txt"}, {"a.txt", "b.txt"}} {
			root := t.TempDir()
			if missing {
				root = filepath.Join(root, "absent")
			}
			states, err := scanReplicaStates(context.Background(), apiclient.Replica{Type: "removable", URI: root}, NewFilesystemScanner(), nil, targets...)
			if !errors.Is(err, errRemovableUnavailable) || states != nil {
				t.Fatalf("missing=%v targets=%v: states=%v error=%v", missing, targets, states, err)
			}
		}
	}
}

type removableTestScanner func() ([]FileState, error)

func (s removableTestScanner) Scan(context.Context, string, map[string]FileState, ...string) ([]FileState, error) {
	return s()
}

func TestRemovableScanDiscardsResultsAfterRemovalOrReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "media")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		writeRemovableTestFile(t, root, "file.txt", "data")
		scanner := removableTestScanner(func() ([]FileState, error) {
			if err := os.Rename(root, root+"-removed"); err != nil {
				t.Fatal(err)
			}
			if replace {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				writeRemovableTestFile(t, root, "other.txt", "other")
			}
			return []FileState{}, nil
		})
		states, err := scanReplicaStates(context.Background(), apiclient.Replica{Type: "removable", URI: root}, scanner, nil)
		if !errors.Is(err, errRemovableUnavailable) || states != nil {
			t.Fatalf("replace=%v: states=%v error=%v", replace, states, err)
		}
	}
}

func TestRemovableWriterRejectsMissingAndEmptyRoots(t *testing.T) {
	for _, missing := range []bool{false, true} {
		root := t.TempDir()
		if missing {
			root = filepath.Join(root, "absent")
		}
		writer := &FilesystemWriter{removable: true}
		if err := writer.Save(context.Background(), root, "nested/file.txt", strings.NewReader("data"), 4); !errors.Is(err, errRemovableUnavailable) {
			t.Fatalf("Save() = %v", err)
		}
		if err := writer.Delete(context.Background(), root, "file.txt"); !errors.Is(err, errRemovableUnavailable) {
			t.Fatalf("Delete() = %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "nested")); !os.IsNotExist(err) {
			t.Fatalf("writer created a directory on absent media: %v", err)
		}
		if missing {
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("writer recreated root: %v", err)
			}
		}
	}
}

type removableTestReader struct {
	io.Reader
	once       sync.Once
	beforeRead func()
}

func (r *removableTestReader) Read(p []byte) (int, error) {
	r.once.Do(r.beforeRead)
	return r.Reader.Read(p)
}

func TestRemovableWriterDoesNotPublishIntoReplacementRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRemovableTestFile(t, root, "file.txt", "original")
	reader := &removableTestReader{Reader: strings.NewReader("replacement"), beforeRead: func() {
		if err := os.Rename(root, root+"-removed"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		writeRemovableTestFile(t, root, "other.txt", "other")
	}}
	err := (&FilesystemWriter{removable: true}).Save(context.Background(), root, "file.txt", reader, 11)
	if !errors.Is(err, errRemovableUnavailable) {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("wrote to replacement root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root+"-removed", "file.txt"))
	if err != nil || string(data) != "original" {
		t.Fatalf("original changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(root + "-removed")
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file not cleaned up: %v, %v", entries, err)
	}
}

func TestRemovableWriterVerifiesContentAndDeletesLastEntry(t *testing.T) {
	root := t.TempDir()
	writeRemovableTestFile(t, root, "file.txt", "original")
	writer := &FilesystemWriter{removable: true}
	if err := writer.SaveVerified(context.Background(), root, "file.txt", strings.NewReader("data"), 4, "wrong"); !errors.Is(err, ErrFileIntegrityMismatch) {
		t.Fatalf("SaveVerified() = %v", err)
	}
	hash, err := hashReaderBLAKE3(context.Background(), strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveVerified(context.Background(), root, "file.txt", strings.NewReader("data"), 4, hash); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delete(context.Background(), root, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := removableRootInfo(root); !errors.Is(err, errRemovableUnavailable) {
		t.Fatalf("empty root must now be unavailable: %v", err)
	}
}

func writeRemovableTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type removableCoordinator struct {
	mu            sync.Mutex
	replicas      []apiclient.Replica
	files         map[uint][]apiclient.ReplicaInventoryFile
	reports       map[uint][]apiclient.ReplicaFileReport
	statusUpdates []string
	websocket     chan struct{}
	onTransfer    func()
}

func newRemovableCoordinator(t *testing.T, replicas []apiclient.Replica, files map[uint][]apiclient.ReplicaInventoryFile) (*Runtime, *removableCoordinator, string) {
	t.Helper()
	c := &removableCoordinator{replicas: replicas, files: files, reports: make(map[uint][]apiclient.ReplicaFileReport), websocket: make(chan struct{}, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/node/nodes/ws" {
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, req, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			select {
			case c.websocket <- struct{}{}:
			default:
			}
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch req.URL.Path {
		case "/node/auth/login":
			json.NewEncoder(w).Encode(map[string]any{"node_id": "node-a", "access_token": "access", "refresh_token": "refresh", "access_token_expires_at": time.Now().Add(time.Hour), "refresh_token_expires_at": time.Now().Add(2 * time.Hour)})
		case "/node/nodes":
			json.NewEncoder(w).Encode(map[string]any{"node_id": "node-a", "commands": []any{}})
		case "/node/replicas":
			json.NewEncoder(w).Encode(c.replicas)
		case "/node/shares", "/node/config", "/node/config/storage-profiles":
			json.NewEncoder(w).Encode([]any{})
		case "/transfer/replicas/2/files/10/content":
			if c.onTransfer != nil {
				c.onTransfer()
			}
			io.WriteString(w, "data")
		default:
			var id uint
			if _, err := fmt.Sscanf(req.URL.Path, "/node/replica/%d/files", &id); err != nil {
				t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
				w.WriteHeader(500)
				return
			}
			switch req.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(map[string]any{"files": c.files[id]})
			case http.MethodPost:
				var body struct {
					Files []apiclient.ReplicaFileReport `json:"files"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				c.reports[id] = append(c.reports[id], body.Files...)
				w.WriteHeader(http.StatusNoContent)
			case http.MethodPatch:
				var body struct {
					Status string `json:"status"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				c.statusUpdates = append(c.statusUpdates, body.Status)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method: %s", req.Method)
				w.WriteHeader(500)
			}
		}
	}))
	t.Cleanup(server.Close)
	r := newRuntimeForTest(t, server.URL)
	r.setLocalState(replicas, nil, files)
	return r, c, server.URL
}

func TestStartupFailureIsLocalAndWebSocketConnects(t *testing.T) {
	healthy := t.TempDir()
	writeRemovableTestFile(t, healthy, "file.txt", "data")
	replicas := []apiclient.Replica{
		{ID: 1, URI: filepath.Join(t.TempDir(), "absent"), Type: "removable", Status: "active"},
		{ID: 2, URI: filepath.Join(t.TempDir(), "absent"), Type: "filesystem", Status: "active"},
		{ID: 3, URI: "s3://bucket/prefix", Type: "storage", StorageProfile: "missing", Status: "active"},
		{ID: 4, URI: healthy, Type: "filesystem", Status: "active"},
	}
	r, coordinator, _ := newRemovableCoordinator(t, replicas, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.Start(ctx)
	select {
	case <-coordinator.websocket:
	case <-time.After(5 * time.Second):
		t.Fatal("startup never connected WebSocket")
	}
	waitRemovableTest(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return len(coordinator.reports[4]) == 1 && r.replicaWatcherExists(4)
	})
	for _, id := range []uint{1, 2, 3} {
		if _, failed := r.replicaFailures.Load(id); !failed {
			t.Fatalf("replica %d not scheduled for retry", id)
		}
	}
}

func TestRemovableRecoveryAfterMissingRootAndEmptyMountPoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRemovableTestFile(t, root, "file.txt", "data")
	hash, err := hashFileBLAKE3(context.Background(), filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	replica := apiclient.Replica{ID: 1, URI: root, Type: "removable", Status: "active"}
	files := map[uint][]apiclient.ReplicaInventoryFile{1: {{FileID: 10, ReplicaID: 1, RelativeURI: "file.txt", Size: 4, Hash: hash, InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1}}}
	r, c, _ := newRemovableCoordinator(t, []apiclient.Replica{replica}, files)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.ensureReplicaWatcher(ctx, replica); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-removed"); err != nil {
		t.Fatal(err)
	}
	r.recoverReplica(ctx, 1)
	if r.replicaWatcherExists(1) {
		t.Fatal("watcher retained for missing root")
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.reportWatcherChange(ctx, replica, FileChange{ChangeType: FileChangeTypeDeleted, RelativeURI: "file.txt"}); !errors.Is(err, errRemovableUnavailable) {
		t.Fatalf("watcher reported removal: %v", err)
	}
	if err := r.reportStartupLocalChanges(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	reportCount := len(c.reports[1])
	c.mu.Unlock()
	if reportCount != 0 {
		t.Fatal("missing/empty root reported inventory deletions")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	writeRemovableTestFile(t, root+"-removed", "new.txt", "new")
	if err := os.Rename(root+"-removed", root); err != nil {
		t.Fatal(err)
	}
	r.retryReplicaInitialization(ctx)
	waitRemovableTest(t, func() bool { return r.replicaWatcherExists(1) })
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reports[1]) != 1 || c.reports[1][0].Action != "created" || c.reports[1][0].RelativeURI != "new.txt" {
		t.Fatalf("reconnection reports = %+v", c.reports[1])
	}
}

func TestRemovableReconcileAbsencePreservesPendingFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	replica := apiclient.Replica{ID: 1, URI: root, Type: "removable", Status: "active"}
	files := map[uint][]apiclient.ReplicaInventoryFile{1: {{FileID: 10, ReplicaID: 1, RelativeURI: "file.txt", InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "pending"}}}
	r, c, url := newRemovableCoordinator(t, []apiclient.Replica{replica}, files)
	payload, err := json.Marshal(reconcileReplicaCommandPayload{SourceNodeAddress: url, SourceNodeID: "source", SourceReplicaID: 2, DestinationReplicaID: 1, TransferToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	err = r.reconcileReplica(context.Background(), apiclient.Command{Payload: payload})
	if !errors.Is(err, errRemovableUnavailable) {
		t.Fatalf("reconcile = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("recreated root: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.statusUpdates) != 0 {
		t.Fatalf("changed pending status: %v", c.statusUpdates)
	}
}

func TestRemovableReconcileInterruptedTransferThenReconnect(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRemovableTestFile(t, root, "file.txt", "old")
	hash, err := hashReaderBLAKE3(context.Background(), strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	replica := apiclient.Replica{ID: 1, URI: root, Type: "removable", Status: "active"}
	files := map[uint][]apiclient.ReplicaInventoryFile{1: {{FileID: 10, ReplicaID: 1, RelativeURI: "file.txt", Size: 4, Hash: hash, InventoryStatus: "active", InventoryVersion: 2, ReplicaStatus: "pending", ReplicaVersion: 1}}}
	r, c, url := newRemovableCoordinator(t, []apiclient.Replica{replica}, files)
	c.onTransfer = func() {
		if err := os.Rename(root, root+"-removed"); err != nil {
			t.Error(err)
		}
	}
	payload, err := json.Marshal(reconcileReplicaCommandPayload{SourceNodeAddress: url, SourceNodeID: "source", SourceReplicaID: 2, DestinationReplicaID: 1, TransferToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	err = r.reconcileReplica(ctx, apiclient.Command{Payload: payload})
	if !errors.Is(err, errRemovableUnavailable) {
		t.Fatalf("interrupted reconcile = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("recreated absent destination: %v", err)
	}
	c.mu.Lock()
	updates := len(c.statusUpdates)
	c.onTransfer = nil
	c.mu.Unlock()
	if updates != 0 {
		t.Fatal("interrupted transfer changed pending status")
	}
	if err := os.Rename(root+"-removed", root); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileReplica(ctx, apiclient.Command{Payload: payload}); err != nil {
		t.Fatalf("reconcile after reconnection: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(data) != "data" {
		t.Fatalf("reconnected contents = %q, %v", data, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.statusUpdates) != 1 || c.statusUpdates[0] != "synchronized" {
		t.Fatalf("status updates = %v", c.statusUpdates)
	}
}

func TestRemovableRecoveryDoesNotRestartWatcherDuringReplicaWork(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	replica := apiclient.Replica{ID: 1, URI: root, Type: "removable", Status: "active"}
	r, _, _ := newRemovableCoordinator(t, []apiclient.Replica{replica}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mu := r.replicaWorkMutex(1)
	mu.Lock()
	r.retryReplicaInitialization(ctx)
	if r.replicaWatcherExists(1) {
		mu.Unlock()
		t.Fatal("recovery restarted watcher during replica work")
	}
	mu.Unlock()
	r.retryReplicaInitialization(ctx)
	waitRemovableTest(t, func() bool { return r.replicaWatcherExists(1) })
}

func TestRemovableSharingCannotCreateMissingRoot(t *testing.T) {
	r := &Runtime{}
	root := filepath.Join(t.TempDir(), "absent")
	err := r.createShareFileOperation(context.Background(), apiclient.Share{}, apiclient.Replica{ID: 1, URI: root, Type: "removable", InventoryType: "folder"}, "file.txt", strings.NewReader("data"), 4)
	if !errors.Is(err, errShareLocalStorageFailed) {
		t.Fatalf("createShareFileOperation = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("sharing recreated root: %v", err)
	}
}

func TestRemovableUnavailableTransferUsesExistingNotFoundResponse(t *testing.T) {
	r, token := newTransferTestRuntime(t, t.TempDir())
	r.stateMu.Lock()
	r.replicas[0].Type = "removable"
	r.stateMu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "/transfer/replicas/1/files/10/content?version=7", nil)
	req.SetPathValue("replica_id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeReplicaFileContent(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func waitRemovableTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for replica recovery")
}
