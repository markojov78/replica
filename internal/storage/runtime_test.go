package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"

	"github.com/gorilla/websocket"
)

func TestRuntimeAuthenticatesRefreshesAndReportsHeartbeat(t *testing.T) {
	var mu sync.Mutex
	loginCalls := 0
	refreshCalls := 0
	heartbeatCalls := 0
	replicaCalls := 0
	commandUpdateCalls := 0
	wsConnections := 0

	logOutput := captureLogs(t)

	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch r.URL.Path {
		case "/internal/auth/login":
			loginCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(1200 * time.Millisecond),
				"refresh_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		case "/internal/auth/refresh":
			refreshCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "refreshed-access-token",
				"refresh_token":            "refreshed-refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(1200 * time.Millisecond),
				"refresh_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		case "/internal/nodes":
			heartbeatCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":   "node-a",
				"address":   "http://node-a:8081",
				"last_seen": time.Now().UTC().Format(time.RFC3339),
				"commands":  []any{},
			})
		case "/internal/replicas":
			replicaCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/internal/commands/7":
			if r.Method != http.MethodPatch {
				t.Fatalf("command update method = %s, want PATCH", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() command update body error = %v", err)
			}
			if got := body["status"]; got != "completed" {
				t.Fatalf("command update status = %v, want completed", got)
			}
			commandUpdateCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         7,
				"node_id":    "node-a",
				"type":       "refresh_state",
				"status":     "completed",
				"payload":    map[string]any{"placeholder": true},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T11:59:00Z",
			})
		case "/internal/nodes/ws":
			if got := r.Header.Get("Authorization"); got == "" {
				t.Fatalf("Authorization header missing for websocket request")
			}

			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("Upgrade() error = %v", err)
			}
			wsConnections++

			err = conn.WriteJSON(map[string]any{
				"id":         7,
				"node_id":    "node-a",
				"type":       "refresh_state",
				"status":     "pending",
				"payload":    map[string]any{"placeholder": true},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T11:59:00Z",
			})
			if err != nil {
				t.Fatalf("WriteJSON() error = %v", err)
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: 200 * time.Millisecond,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runtime.Start(ctx)

	deadline := time.Now().Add(1800 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := loginCalls >= 1 && refreshCalls >= 1 && heartbeatCalls >= 2 && replicaCalls >= 2 && commandUpdateCalls >= 1 && wsConnections >= 1
		mu.Unlock()
		if done &&
			strings.Contains(logOutput.String(), "got command id=7 type=refresh_state status=pending") &&
			strings.Contains(logOutput.String(), "storage runtime command completed id=7") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("loginCalls=%d refreshCalls=%d heartbeatCalls=%d replicaCalls=%d commandUpdateCalls=%d wsConnections=%d logs=%q", loginCalls, refreshCalls, heartbeatCalls, replicaCalls, commandUpdateCalls, wsConnections, logOutput.String())
}

func TestRuntimeProcessesFallbackCommandsWhenWebSocketUnavailable(t *testing.T) {
	logOutput := captureLogs(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":   "node-a",
				"address":   "http://node-a:8081",
				"last_seen": time.Now().UTC().Format(time.RFC3339),
				"commands": []any{
					map[string]any{
						"id":         11,
						"node_id":    "node-a",
						"type":       "scan_replica",
						"status":     "pending",
						"payload":    map[string]any{"replica_id": 3},
						"created_at": "2026-05-21T11:59:00Z",
						"updated_at": "2026-05-21T11:59:00Z",
					},
				},
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: 200 * time.Millisecond,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	runtime.Start(ctx)

	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(logOutput.String(), "got command id=11 type=scan_replica status=pending") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("fallback command log missing, logs=%q", logOutput.String())
}

func TestRuntimeDeduplicatesCompletedRefreshStateCommand(t *testing.T) {
	var mu sync.Mutex
	replicaCalls := 0
	commandUpdateCalls := 0
	firstUpdate := make(chan struct{})
	firstUpdateClosed := false

	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":   "node-a",
				"address":   "http://node-a:8081",
				"last_seen": time.Now().UTC().Format(time.RFC3339),
				"commands":  []any{},
			})
		case "/internal/replicas":
			mu.Lock()
			replicaCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/internal/commands/7":
			if r.Method != http.MethodPatch {
				t.Fatalf("command update method = %s, want PATCH", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() command update body error = %v", err)
			}
			if got := body["status"]; got != "completed" {
				t.Fatalf("command update status = %v, want completed", got)
			}

			mu.Lock()
			commandUpdateCalls++
			if !firstUpdateClosed {
				close(firstUpdate)
				firstUpdateClosed = true
			}
			mu.Unlock()

			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         7,
				"node_id":    "node-a",
				"type":       "refresh_state",
				"status":     "completed",
				"payload":    map[string]any{},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T11:59:00Z",
			})
		case "/internal/nodes/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("Upgrade() error = %v", err)
			}
			defer conn.Close()

			command := map[string]any{
				"id":         7,
				"node_id":    "node-a",
				"type":       "refresh_state",
				"status":     "pending",
				"payload":    map[string]any{},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T11:59:00Z",
			}
			if err := conn.WriteJSON(command); err != nil {
				t.Fatalf("WriteJSON(first) error = %v", err)
			}

			select {
			case <-firstUpdate:
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for first command update")
			}

			if err := conn.WriteJSON(command); err != nil {
				t.Fatalf("WriteJSON(duplicate) error = %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runtime.Start(ctx)

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := replicaCalls == 2 && commandUpdateCalls >= 2
		gotReplicaCalls := replicaCalls
		mu.Unlock()
		if done {
			return
		}
		if gotReplicaCalls > 2 {
			t.Fatalf("replicaCalls=%d, want duplicate command not to refresh state again", gotReplicaCalls)
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("replicaCalls=%d commandUpdateCalls=%d", replicaCalls, commandUpdateCalls)
}

func TestRuntimeScanReplicaReportsCreatedAndChangedFiles(t *testing.T) {
	var mu sync.Mutex
	fileListCalls := 0
	reportCalls := 0
	commandUpdateCalls := 0
	var gotReport struct {
		Files []struct {
			FileID       *uint     `json:"file_id"`
			Action       string    `json:"action"`
			RelativeURI  string    `json:"relative_uri"`
			FileSize     int64     `json:"file_size"`
			FileHash     string    `json:"file_hash"`
			CreatedTime  time.Time `json:"created_time"`
			ModifiedTime time.Time `json:"modified_time"`
		} `json:"files"`
	}

	replicaRoot := t.TempDir()
	unchangedPath := filepath.Join(replicaRoot, "unchanged.txt")
	changedPath := filepath.Join(replicaRoot, "changed.txt")
	newPath := filepath.Join(replicaRoot, "new.txt")

	if err := os.WriteFile(unchangedPath, []byte("unchanged"), 0o644); err != nil {
		t.Fatalf("WriteFile(unchanged) error = %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o644); err != nil {
		t.Fatalf("WriteFile(changed) error = %v", err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile(new) error = %v", err)
	}

	unchangedModified := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	changedModified := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	newModified := time.Date(2026, 5, 21, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(unchangedPath, unchangedModified, unchangedModified); err != nil {
		t.Fatalf("Chtimes(unchanged) error = %v", err)
	}
	if err := os.Chtimes(changedPath, changedModified, changedModified); err != nil {
		t.Fatalf("Chtimes(changed) error = %v", err)
	}
	if err := os.Chtimes(newPath, newModified, newModified); err != nil {
		t.Fatalf("Chtimes(new) error = %v", err)
	}

	unchangedHash, err := hashFileBLAKE3(context.Background(), unchangedPath)
	if err != nil {
		t.Fatalf("hashFileBLAKE3(unchanged) error = %v", err)
	}
	changedHash, err := hashFileBLAKE3(context.Background(), changedPath)
	if err != nil {
		t.Fatalf("hashFileBLAKE3(changed) error = %v", err)
	}
	newHash, err := hashFileBLAKE3(context.Background(), newPath)
	if err != nil {
		t.Fatalf("hashFileBLAKE3(new) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":   "node-a",
				"address":   "http://node-a:8081",
				"last_seen": time.Now().UTC().Format(time.RFC3339),
				"commands": []any{
					map[string]any{
						"id":         21,
						"node_id":    "node-a",
						"type":       "scan_replica",
						"status":     "pending",
						"payload":    map[string]any{"replica_id": 3},
						"created_at": "2026-05-21T11:59:00Z",
						"updated_at": "2026-05-21T11:59:00Z",
					},
				},
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           3,
					"inventory_id": 2,
					"node_id":      "node-a",
					"uri":          replicaRoot,
					"status":       "active",
					"type":         "filesystem",
				},
			})
		case "/internal/replica/3/files":
			switch r.Method {
			case http.MethodGet:
				mu.Lock()
				fileListCalls++
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"files": []map[string]any{
						{
							"file_id":           10,
							"replica_id":        3,
							"inventory_id":      2,
							"relative_uri":      "unchanged.txt",
							"size":              9,
							"hash":              unchangedHash,
							"inventory_status":  "active",
							"inventory_version": 1,
							"replica_status":    "synchronized",
							"replica_version":   1,
							"created":           unchangedModified.Format(time.RFC3339),
							"modified":          unchangedModified.Format(time.RFC3339),
						},
						{
							"file_id":           11,
							"replica_id":        3,
							"inventory_id":      2,
							"relative_uri":      "changed.txt",
							"size":              7,
							"hash":              "old-hash",
							"inventory_status":  "active",
							"inventory_version": 1,
							"replica_status":    "synchronized",
							"replica_version":   1,
							"created":           changedModified.Add(-time.Hour).Format(time.RFC3339),
							"modified":          changedModified.Add(-time.Hour).Format(time.RFC3339),
						},
					},
				})
			case http.MethodPost:
				if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
					t.Fatalf("Authorization = %q, want Bearer access-token", got)
				}
				if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
					t.Fatalf("Decode(report) error = %v", err)
				}
				mu.Lock()
				reportCalls++
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("method = %s, want GET or POST", r.Method)
			}
		case "/internal/commands/21":
			if r.Method != http.MethodPatch {
				t.Fatalf("command update method = %s, want PATCH", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if got := body["status"]; got != "completed" {
				t.Fatalf("command status = %v, want completed", got)
			}
			mu.Lock()
			commandUpdateCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         21,
				"node_id":    "node-a",
				"type":       "scan_replica",
				"status":     "completed",
				"payload":    map[string]any{"replica_id": 3},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T12:00:00Z",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runtime.Start(ctx)

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := fileListCalls >= 2 && reportCalls >= 1 && commandUpdateCalls >= 1
		mu.Unlock()
		if done {
			if len(gotReport.Files) != 2 {
				t.Fatalf("len(gotReport.Files) = %d, want 2; files=%+v", len(gotReport.Files), gotReport.Files)
			}

			byURI := map[string]struct {
				FileID       *uint     `json:"file_id"`
				Action       string    `json:"action"`
				RelativeURI  string    `json:"relative_uri"`
				FileSize     int64     `json:"file_size"`
				FileHash     string    `json:"file_hash"`
				CreatedTime  time.Time `json:"created_time"`
				ModifiedTime time.Time `json:"modified_time"`
			}{}
			for _, file := range gotReport.Files {
				byURI[file.RelativeURI] = file
			}

			changed, ok := byURI["changed.txt"]
			if !ok {
				t.Fatalf("changed.txt report missing; files=%+v", gotReport.Files)
			}
			if changed.FileID == nil || *changed.FileID != 11 {
				t.Fatalf("changed.FileID = %v, want 11", changed.FileID)
			}
			if changed.FileHash != changedHash {
				t.Fatalf("changed.FileHash = %q, want %q", changed.FileHash, changedHash)
			}
			if changed.Action != "updated" {
				t.Fatalf("changed.Action = %q, want updated", changed.Action)
			}

			created, ok := byURI["new.txt"]
			if !ok {
				t.Fatalf("new.txt report missing; files=%+v", gotReport.Files)
			}
			if created.FileID != nil {
				t.Fatalf("created.FileID = %v, want nil", created.FileID)
			}
			if created.FileHash != newHash {
				t.Fatalf("created.FileHash = %q, want %q", created.FileHash, newHash)
			}
			if created.Action != "created" {
				t.Fatalf("created.Action = %q, want created", created.Action)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("fileListCalls=%d reportCalls=%d commandUpdateCalls=%d report=%+v", fileListCalls, reportCalls, commandUpdateCalls, gotReport)
}

func TestRuntimeScanReplicaRefreshesLocalStateBeforeScan(t *testing.T) {
	replicaRoot := t.TempDir()
	filePath := filepath.Join(replicaRoot, "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var mu sync.Mutex
	reportCalls := 0
	var commandCompleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":             7,
					"inventory_id":   3,
					"inventory_type": "file",
					"node_id":        "node-a",
					"uri":            replicaRoot,
					"status":         "active",
					"type":           "filesystem",
				},
			})
		case "/internal/replica/7/files":
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
					{
						"file_id":           10,
						"replica_id":        7,
						"inventory_id":      3,
						"relative_uri":      "file.txt",
						"size":              0,
						"hash":              "",
						"inventory_status":  "active",
						"inventory_version": 0,
						"replica_status":    "synchronized",
						"replica_version":   0,
					},
				}})
			case http.MethodPost:
				var body struct {
					Files []apiclient.ReplicaFileReport `json:"files"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("Decode(report) error = %v", err)
				}
				if len(body.Files) != 1 || body.Files[0].RelativeURI != "file.txt" || body.Files[0].Action != "updated" || body.Files[0].FileID == nil || *body.Files[0].FileID != 10 {
					t.Fatalf("reported files = %+v, want updated file_id=10 file.txt", body.Files)
				}
				mu.Lock()
				reportCalls++
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("method = %s, want GET or POST", r.Method)
			}
		case "/internal/commands/118":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("command status = %q, want completed", body.Status)
			}
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         118,
				"node_id":    "node-a",
				"type":       "scan_replica",
				"status":     "completed",
				"payload":    map[string]any{"replica_id": 7},
				"created_at": "2026-06-02T08:03:42Z",
				"updated_at": "2026-06-02T08:03:43Z",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	payload, err := json.Marshal(map[string]any{"replica_id": 7})
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ok := runtime.handleCommand(ctx, apiclient.Command{
		ID:      118,
		NodeID:  "node-a",
		Type:    "scan_replica",
		Status:  "pending",
		Payload: payload,
	})
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}
	mu.Lock()
	initialReportCalls := reportCalls
	mu.Unlock()
	if initialReportCalls != 1 {
		t.Fatalf("reportCalls = %d, want 1 after scan", initialReportCalls)
	}
	if !commandCompleted {
		t.Fatal("command was not marked completed")
	}
	if !runtime.replicaWatcherExists(7) {
		t.Fatal("scan_replica did not create watcher for newly loaded replica")
	}

	runtime.watcherMu.Lock()
	firstWatcher := runtime.watchers[7]
	runtime.watcherMu.Unlock()
	replica, ok := runtime.findReplica(7)
	if !ok {
		t.Fatal("replica 7 missing from local state")
	}
	if err := runtime.ensureReplicaWatcher(ctx, replica); err != nil {
		t.Fatalf("ensureReplicaWatcher(second) error = %v", err)
	}
	runtime.watcherMu.Lock()
	secondWatcher := runtime.watchers[7]
	runtime.watcherMu.Unlock()
	if secondWatcher != firstWatcher {
		t.Fatal("ensureReplicaWatcher created duplicate watcher")
	}

	if err := os.WriteFile(filePath, []byte("changed content"), 0o644); err != nil {
		t.Fatalf("WriteFile(changed) error = %v", err)
	}
	reportDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(reportDeadline) {
		mu.Lock()
		gotReportCalls := reportCalls
		mu.Unlock()
		if gotReportCalls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	gotReportCalls := reportCalls
	mu.Unlock()
	if gotReportCalls < 2 {
		t.Fatalf("reportCalls = %d, want watcher to report edit after scan", gotReportCalls)
	}

	firstWatcher.cancel()
	deadline := time.Now().Add(time.Second)
	for runtime.replicaWatcherExists(7) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.replicaWatcherExists(7) {
		t.Fatal("canceled watcher was not removed from runtime state")
	}
}

func TestReplicaScanTargetsReturnsAllFileSetFiles(t *testing.T) {
	targets, err := replicaScanTargets(apiclient.Replica{InventoryType: "file"}, []apiclient.ReplicaInventoryFile{
		{RelativeURI: "file1.jpg"},
		{RelativeURI: "subfolder/file2.jpg"},
	})
	if err != nil {
		t.Fatalf("replicaScanTargets() error = %v", err)
	}
	if !reflect.DeepEqual(targets, []string{"file1.jpg", "subfolder/file2.jpg"}) {
		t.Fatalf("replicaScanTargets() = %+v, want all file-set files", targets)
	}
}

func TestRuntimeRefreshLocalStateSkipsDeletedReplicaFiles(t *testing.T) {
	deletedFileListRequested := false
	commandCompleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           8,
					"inventory_id": 3,
					"node_id":      "node-a",
					"uri":          "/deleted",
					"status":       "deleted",
					"type":         "filesystem",
				},
			})
		case "/internal/replica/8/files":
			deletedFileListRequested = true
			t.Fatalf("deleted replica files should not be requested")
		case "/internal/commands/120":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("command status = %q, want completed", body.Status)
			}
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         120,
				"node_id":    "node-a",
				"type":       "refresh_state",
				"status":     "completed",
				"payload":    map[string]any{},
				"created_at": "2026-06-14T08:03:42Z",
				"updated_at": "2026-06-14T08:03:43Z",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	cancelCalls := 0
	runtime.watchers[8] = &runningReplicaWatcher{cancel: func() { cancelCalls++ }}
	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:     120,
		NodeID: "node-a",
		Type:   "refresh_state",
		Status: "pending",
	})
	replicas := runtime.replicas
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}
	if !commandCompleted {
		t.Fatal("refresh_state command was not marked completed")
	}
	if len(replicas) != 1 || replicas[0].Status != "deleted" {
		t.Fatalf("replicas = %+v, want one deleted replica", replicas)
	}
	if deletedFileListRequested {
		t.Fatal("deleted replica files were requested")
	}
	if files := runtime.replicaFilesSnapshot(8); len(files) != 0 {
		t.Fatalf("replicaFilesSnapshot(8) = %+v, want empty", files)
	}
	if cancelCalls != 1 || runtime.replicaWatcherExists(8) {
		t.Fatalf("deleted watcher cancelCalls = %d exists = %t, want 1 and false", cancelCalls, runtime.replicaWatcherExists(8))
	}
}

func TestRuntimeStopReplicaWatcherIsIdempotent(t *testing.T) {
	runtime := &Runtime{watchers: make(map[uint]*runningReplicaWatcher)}
	cancelCalls := 0
	runtime.watchers[8] = &runningReplicaWatcher{
		cancel: func() {
			cancelCalls++
		},
	}

	runtime.stopReplicaWatcher(8)
	runtime.stopReplicaWatcher(8)

	if cancelCalls != 1 {
		t.Fatalf("cancelCalls = %d, want 1", cancelCalls)
	}
	if runtime.replicaWatcherExists(8) {
		t.Fatal("watcher still exists after stopReplicaWatcher")
	}
}

func TestRuntimeStopDeletedReplicaWatchers(t *testing.T) {
	runtime := &Runtime{watchers: make(map[uint]*runningReplicaWatcher)}
	deletedCancelCalls := 0
	activeCancelCalls := 0
	runtime.watchers[8] = &runningReplicaWatcher{cancel: func() { deletedCancelCalls++ }}
	runtime.watchers[9] = &runningReplicaWatcher{cancel: func() { activeCancelCalls++ }}
	runtime.setLocalState([]apiclient.Replica{
		{ID: 8, Status: "deleted"},
		{ID: 9, Status: "active"},
	}, nil)

	runtime.stopDeletedReplicaWatchers()

	if deletedCancelCalls != 1 {
		t.Fatalf("deletedCancelCalls = %d, want 1", deletedCancelCalls)
	}
	if activeCancelCalls != 0 {
		t.Fatalf("activeCancelCalls = %d, want 0", activeCancelCalls)
	}
	if runtime.replicaWatcherExists(8) {
		t.Fatal("deleted replica watcher still exists")
	}
	if !runtime.replicaWatcherExists(9) {
		t.Fatal("active replica watcher was stopped")
	}
}

func TestRuntimeScanReplicaSkipsDeletedReplica(t *testing.T) {
	commandCompleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replica/8/files":
			t.Fatalf("deleted replica files should not be requested")
		case "/internal/commands/119":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("command status = %q, want completed", body.Status)
			}
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         119,
				"node_id":    "node-a",
				"type":       "scan_replica",
				"status":     "completed",
				"payload":    map[string]any{"replica_id": 8},
				"created_at": "2026-06-02T08:03:42Z",
				"updated_at": "2026-06-02T08:03:43Z",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	runtime.setLocalState([]apiclient.Replica{{
		ID:     8,
		NodeID: "node-a",
		URI:    "/deleted",
		Status: "deleted",
		Type:   "filesystem",
	}}, nil)

	payload, err := json.Marshal(map[string]any{"replica_id": 8})
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}
	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      119,
		NodeID:  "node-a",
		Type:    "scan_replica",
		Status:  "pending",
		Payload: payload,
	})
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}
	if !commandCompleted {
		t.Fatal("command was not marked completed")
	}
}

func TestRuntimeReconcileReplicaTransfersPendingFiles(t *testing.T) {
	destinationRoot := t.TempDir()
	type replicaFileStatusUpdate struct {
		Status  string
		Version uint
	}
	var statusUpdates []replicaFileStatusUpdate
	commandCompleted := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           4,
					"inventory_id": 2,
					"node_id":      "node-a",
					"uri":          destinationRoot,
					"status":       "active",
					"type":         "filesystem",
				},
			})
		case "/internal/replica/4/files":
			if got := r.URL.Query().Get("status"); got != "" && got != "pending" {
				t.Fatalf("status query = %q, want empty or pending", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{
						"file_id":           10,
						"replica_id":        4,
						"inventory_id":      2,
						"relative_uri":      "nested/file.txt",
						"size":              7,
						"hash":              "hash",
						"inventory_status":  "active",
						"inventory_version": 5,
						"replica_status":    "pending",
						"replica_version":   0,
						"created":           "2026-05-21T11:00:00Z",
						"modified":          "2026-05-21T12:00:00Z",
					},
				},
			})
		case "/internal/replicas/3/files/10/content":
			if got := r.URL.Query().Get("version"); got != "5" {
				t.Fatalf("version query = %q, want 5", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer transfer-token" {
				t.Fatalf("Authorization = %q, want Bearer transfer-token", got)
			}
			_, _ = w.Write([]byte("content"))
		case "/internal/replica/4/files/10":
			if r.Method != http.MethodPatch {
				t.Fatalf("method = %s, want PATCH", r.Method)
			}
			var body replicaFileStatusUpdate
			type statusUpdateBody struct {
				Status  string `json:"status"`
				Version uint   `json:"version"`
			}
			var decoded statusUpdateBody
			if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
				t.Fatalf("Decode(status update) error = %v", err)
			}
			body = replicaFileStatusUpdate{Status: decoded.Status, Version: decoded.Version}
			statusUpdates = append(statusUpdates, body)
			w.WriteHeader(http.StatusNoContent)
		case "/internal/commands/31":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("command status = %q, want completed", body.Status)
			}
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         31,
				"node_id":    "node-a",
				"type":       "reconcile_replica",
				"status":     "completed",
				"payload":    map[string]any{},
				"created_at": "2026-05-21T11:59:00Z",
				"updated_at": "2026-05-21T12:00:00Z",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	payload := map[string]any{
		"source_node_address":    server.URL,
		"source_node_id":         "node-source",
		"source_replica_id":      3,
		"destination_replica_id": 4,
		"transfer_token":         "transfer-token",
	}
	payloadData, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}

	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      31,
		NodeID:  "node-a",
		Type:    "reconcile_replica",
		Status:  "pending",
		Payload: payloadData,
	})
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}

	data, err := os.ReadFile(filepath.Join(destinationRoot, "nested", "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("copied content = %q, want content", string(data))
	}
	if len(statusUpdates) != 1 || statusUpdates[0].Status != "synchronized" || statusUpdates[0].Version != 5 {
		t.Fatalf("statusUpdates = %+v, want synchronized version=5", statusUpdates)
	}
	if !commandCompleted {
		t.Fatal("command was not marked completed")
	}
}

func TestRuntimeReconcileReplicaDeletesPendingDeletedFiles(t *testing.T) {
	destinationRoot := t.TempDir()
	localPath := filepath.Join(destinationRoot, "deleted.txt")
	if err := os.WriteFile(localPath, []byte("old content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	type replicaFileStatusUpdate struct {
		Status  string
		Version uint
	}
	var statusUpdates []replicaFileStatusUpdate
	commandCompleted := false
	contentRequested := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 4, "inventory_id": 2, "node_id": "node-a", "uri": destinationRoot, "status": "active", "type": "filesystem"},
			})
		case "/internal/replica/4/files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{
						"file_id":           10,
						"replica_id":        4,
						"inventory_id":      2,
						"relative_uri":      "deleted.txt",
						"size":              0,
						"hash":              "hash",
						"inventory_status":  "deleted",
						"inventory_version": 5,
						"replica_status":    "pending",
						"replica_version":   4,
						"created":           "2026-05-21T11:00:00Z",
						"modified":          "2026-05-21T12:00:00Z",
					},
				},
			})
		case "/internal/replicas/3/files/10/content":
			contentRequested = true
			http.Error(w, "should not transfer deleted file", http.StatusInternalServerError)
		case "/internal/replica/4/files/10":
			var body struct {
				Status  string `json:"status"`
				Version uint   `json:"version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(status update) error = %v", err)
			}
			statusUpdates = append(statusUpdates, replicaFileStatusUpdate{Status: body.Status, Version: body.Version})
			w.WriteHeader(http.StatusNoContent)
		case "/internal/commands/34":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("command status = %q, want completed", body.Status)
			}
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 34, "node_id": "node-a", "type": "reconcile_replica", "status": "completed"})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime := newRuntimeForTest(t, server.URL)
	payloadData := reconcilePayloadForTest(t, server.URL)

	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      34,
		NodeID:  "node-a",
		Type:    "reconcile_replica",
		Status:  "pending",
		Payload: payloadData,
	})
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}
	if contentRequested {
		t.Fatal("content endpoint was requested for deleted file")
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(localPath) error = %v, want not exist", err)
	}
	if len(statusUpdates) != 1 || statusUpdates[0].Status != "synchronized" || statusUpdates[0].Version != 5 {
		t.Fatalf("statusUpdates = %+v, want synchronized version=5", statusUpdates)
	}
	if !commandCompleted {
		t.Fatal("command was not marked completed")
	}
}

func TestRuntimeReconcileReplicaDeletesUnknownDownstreamFiles(t *testing.T) {
	destinationRoot := t.TempDir()
	unknownPath := filepath.Join(destinationRoot, "unknown.txt")
	if err := os.WriteFile(unknownPath, []byte("unknown"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	commandCompleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 4, "inventory_id": 2, "node_id": "node-a", "uri": destinationRoot, "status": "active", "type": "filesystem", "upstream_replica_id": 3},
			})
		case "/internal/replica/4/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
		case "/internal/commands/35":
			commandCompleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 35, "node_id": "node-a", "type": "reconcile_replica", "status": "completed"})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	payload := reconcilePayloadForTest(t, server.URL)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	decoded["delete_relative_uris"] = []string{"unknown.txt"}
	payload, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}

	runtime := newRuntimeForTest(t, server.URL)
	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      35,
		NodeID:  "node-a",
		Type:    "reconcile_replica",
		Status:  "pending",
		Payload: payload,
	})
	if !ok {
		t.Fatal("handleCommand() = false, want true")
	}
	if _, err := os.Stat(unknownPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(unknownPath) error = %v, want not exist", err)
	}
	if !commandCompleted {
		t.Fatal("command was not marked completed")
	}
}

func TestRuntimeReconcileReplicaMarksTerminalFileErrorAndContinues(t *testing.T) {
	destinationRoot := t.TempDir()
	var statusUpdates []struct {
		FileID  string
		Status  string
		Version uint
	}
	commandFailed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 4, "inventory_id": 2, "node_id": "node-a", "uri": destinationRoot, "status": "active", "type": "filesystem"},
			})
		case "/internal/replica/4/files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"file_id": 10, "replica_id": 4, "inventory_id": 2, "relative_uri": "missing.txt", "inventory_status": "active", "inventory_version": 5, "replica_status": "pending", "replica_version": 0, "created": "2026-05-21T11:00:00Z", "modified": "2026-05-21T12:00:00Z"},
					{"file_id": 11, "replica_id": 4, "inventory_id": 2, "relative_uri": "ok.txt", "inventory_status": "active", "inventory_version": 6, "replica_status": "pending", "replica_version": 0, "created": "2026-05-21T11:00:00Z", "modified": "2026-05-21T12:00:00Z"},
				},
			})
		case "/internal/replicas/3/files/10/content":
			http.Error(w, "missing", http.StatusNotFound)
		case "/internal/replicas/3/files/11/content":
			_, _ = w.Write([]byte("ok"))
		case "/internal/replica/4/files/10", "/internal/replica/4/files/11":
			var body struct {
				Status  string `json:"status"`
				Version uint   `json:"version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(status update) error = %v", err)
			}
			statusUpdates = append(statusUpdates, struct {
				FileID  string
				Status  string
				Version uint
			}{FileID: filepath.Base(r.URL.Path), Status: body.Status, Version: body.Version})
			w.WriteHeader(http.StatusNoContent)
		case "/internal/commands/32":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode(command update) error = %v", err)
			}
			if body.Status != "failed" {
				t.Fatalf("command status = %q, want failed", body.Status)
			}
			commandFailed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 32, "node_id": "node-a", "type": "reconcile_replica", "status": "failed"})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime := newRuntimeForTest(t, server.URL)
	payloadData := reconcilePayloadForTest(t, server.URL)

	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      32,
		NodeID:  "node-a",
		Type:    "reconcile_replica",
		Status:  "pending",
		Payload: payloadData,
	})
	if ok {
		t.Fatal("handleCommand() = true, want false")
	}
	if !commandFailed {
		t.Fatal("command was not marked failed")
	}
	if len(statusUpdates) != 2 {
		t.Fatalf("statusUpdates = %+v, want 2 updates", statusUpdates)
	}
	if statusUpdates[0].FileID != "10" || statusUpdates[0].Status != "error" {
		t.Fatalf("first status update = %+v, want file 10 error", statusUpdates[0])
	}
	if statusUpdates[1].FileID != "11" || statusUpdates[1].Status != "synchronized" || statusUpdates[1].Version != 6 {
		t.Fatalf("second status update = %+v, want file 11 synchronized version 6", statusUpdates[1])
	}
	data, err := os.ReadFile(filepath.Join(destinationRoot, "ok.txt"))
	if err != nil {
		t.Fatalf("ReadFile(ok.txt) error = %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("ok.txt = %q, want ok", string(data))
	}
}

func TestRuntimeReconcileReplicaAuthErrorStopsWithoutFileStatusUpdates(t *testing.T) {
	destinationRoot := t.TempDir()
	statusUpdateCalls := 0
	commandFailed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 4, "inventory_id": 2, "node_id": "node-a", "uri": destinationRoot, "status": "active", "type": "filesystem"},
			})
		case "/internal/replica/4/files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"file_id": 10, "replica_id": 4, "inventory_id": 2, "relative_uri": "auth.txt", "inventory_status": "active", "inventory_version": 5, "replica_status": "pending", "replica_version": 0, "created": "2026-05-21T11:00:00Z", "modified": "2026-05-21T12:00:00Z"},
				},
			})
		case "/internal/replicas/3/files/10/content":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "/internal/replica/4/files/10":
			statusUpdateCalls++
			w.WriteHeader(http.StatusNoContent)
		case "/internal/commands/33":
			commandFailed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 33, "node_id": "node-a", "type": "reconcile_replica", "status": "failed"})
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime := newRuntimeForTest(t, server.URL)
	ok := runtime.handleCommand(context.Background(), apiclient.Command{
		ID:      33,
		NodeID:  "node-a",
		Type:    "reconcile_replica",
		Status:  "pending",
		Payload: reconcilePayloadForTest(t, server.URL),
	})
	if ok {
		t.Fatal("handleCommand() = true, want false")
	}
	if !commandFailed {
		t.Fatal("command was not marked failed")
	}
	if statusUpdateCalls != 0 {
		t.Fatalf("statusUpdateCalls = %d, want 0", statusUpdateCalls)
	}
}

func newRuntimeForTest(t *testing.T, coordinatorURL string) *Runtime {
	t.Helper()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    coordinatorURL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: time.Hour,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func reconcilePayloadForTest(t *testing.T, sourceNodeAddress string) json.RawMessage {
	t.Helper()

	payload := map[string]any{
		"source_node_address":    sourceNodeAddress,
		"source_node_id":         "node-source",
		"source_replica_id":      3,
		"destination_replica_id": 4,
		"transfer_token":         "transfer-token",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}
	return data
}

func TestRuntimeReportsStartupLocalChanges(t *testing.T) {
	replicaRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(replicaRoot, "changed.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("WriteFile(changed) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(replicaRoot, "unchanged.txt"), []byte("unchanged"), 0o644); err != nil {
		t.Fatalf("WriteFile(unchanged) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(replicaRoot, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile(new) error = %v", err)
	}

	changedHash, err := hashFileBLAKE3(context.Background(), filepath.Join(replicaRoot, "changed.txt"))
	if err != nil {
		t.Fatalf("hashFileBLAKE3(changed) error = %v", err)
	}
	unchangedHash, err := hashFileBLAKE3(context.Background(), filepath.Join(replicaRoot, "unchanged.txt"))
	if err != nil {
		t.Fatalf("hashFileBLAKE3(unchanged) error = %v", err)
	}

	var gotReport struct {
		Files []apiclient.ReplicaFileReport `json:"files"`
	}
	reportCalls := 0
	refreshCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replica/3/files":
			switch r.Method {
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
					t.Fatalf("Decode(report) error = %v", err)
				}
				reportCalls++
				w.WriteHeader(http.StatusNoContent)
			case http.MethodGet:
				refreshCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
			default:
				t.Fatalf("method = %s, want GET or POST", r.Method)
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime := newRuntimeForTest(t, server.URL)
	replica := apiclient.Replica{ID: 3, InventoryID: 2, NodeID: "node-a", URI: replicaRoot, Status: "active", Type: "filesystem"}
	runtime.setLocalState(
		[]apiclient.Replica{replica},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 2, RelativeURI: "changed.txt", Size: 3, Hash: "old-hash", InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1},
				{FileID: 11, ReplicaID: 3, InventoryID: 2, RelativeURI: "unchanged.txt", Size: 9, Hash: unchangedHash, InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1},
				{FileID: 12, ReplicaID: 3, InventoryID: 2, RelativeURI: "missing.txt", Size: 7, Hash: "missing-hash", InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1},
			},
		},
	)

	if err := runtime.reportStartupLocalChanges(context.Background()); err != nil {
		t.Fatalf("reportStartupLocalChanges() error = %v", err)
	}

	if reportCalls != 1 || refreshCalls != 1 {
		t.Fatalf("reportCalls=%d refreshCalls=%d, want 1/1", reportCalls, refreshCalls)
	}
	if len(gotReport.Files) != 3 {
		t.Fatalf("len(gotReport.Files) = %d, want 3; files=%+v", len(gotReport.Files), gotReport.Files)
	}
	byURI := make(map[string]apiclient.ReplicaFileReport, len(gotReport.Files))
	for _, file := range gotReport.Files {
		byURI[file.RelativeURI] = file
	}
	if changed := byURI["changed.txt"]; changed.Action != "updated" || changed.FileID == nil || *changed.FileID != 10 || changed.FileHash == nil || *changed.FileHash != changedHash {
		t.Fatalf("changed report = %+v, want updated file_id=10 hash=%s", changed, changedHash)
	}
	if created := byURI["new.txt"]; created.Action != "created" || created.FileID != nil || created.FileHash == nil || *created.FileHash == "" {
		t.Fatalf("created report = %+v, want created without file_id", created)
	}
	if deleted := byURI["missing.txt"]; deleted.Action != "deleted" || deleted.FileID == nil || *deleted.FileID != 12 || deleted.FileHash != nil || deleted.FileSize != nil {
		t.Fatalf("deleted report = %+v, want deleted file_id=12 without content fields", deleted)
	}
	if _, ok := byURI["unchanged.txt"]; ok {
		t.Fatalf("unchanged.txt was reported: %+v", byURI["unchanged.txt"])
	}
}

func TestRuntimeStartupScanSkipsPendingDownstreamMissingRoot(t *testing.T) {
	reportCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/replica/3/files" && r.Method == http.MethodPost {
			reportCalls++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("unexpected path %s %q", r.Method, r.URL.RequestURI())
	}))
	defer server.Close()

	runtime := newRuntimeForTest(t, server.URL)
	upstreamID := uint(2)
	replica := apiclient.Replica{
		ID:                3,
		InventoryID:       2,
		NodeID:            "node-a",
		URI:               filepath.Join(t.TempDir(), "missing-replica-root"),
		Status:            "active",
		Type:              "filesystem",
		UpstreamReplicaID: &upstreamID,
	}
	runtime.setLocalState(
		[]apiclient.Replica{replica},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 2, RelativeURI: "file.txt", Size: 7, Hash: "hash", InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "pending", ReplicaVersion: 0},
			},
		},
	)

	if err := runtime.reportStartupLocalChanges(context.Background()); err != nil {
		t.Fatalf("reportStartupLocalChanges() error = %v", err)
	}
	if reportCalls != 0 {
		t.Fatalf("reportCalls = %d, want 0", reportCalls)
	}
}

func TestRuntimeStartsReplicaWatcherAndLogsChanges(t *testing.T) {
	logOutput := captureLogs(t)
	replicaRoot := t.TempDir()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":   "node-a",
				"address":   "http://node-a:8081",
				"last_seen": time.Now().UTC().Format(time.RFC3339),
				"commands":  []any{},
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           3,
					"inventory_id": 2,
					"node_id":      "node-a",
					"uri":          replicaRoot,
					"status":       "active",
					"type":         "filesystem",
				},
			})
		case "/internal/replica/3/files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "http://node-a:8081",
			HeartbeatInterval: 200 * time.Millisecond,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runtime.Start(ctx)

	startDeadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(startDeadline) {
		if strings.Contains(logOutput.String(), "storage runtime watcher started replica_id=3") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := os.WriteFile(filepath.Join(replicaRoot, "new.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	changeDeadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(changeDeadline) {
		logs := logOutput.String()
		if strings.Contains(logs, "storage runtime replica change replica_id=3") &&
			strings.Contains(logs, "relative_uri=new.txt") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("replica change log missing, logs=%q", logOutput.String())
}

func TestRuntimeReportsWatcherCreatedFile(t *testing.T) {
	replicaRoot := t.TempDir()
	filePath := filepath.Join(replicaRoot, "new.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var gotReport struct {
		Files []struct {
			FileID      *uint  `json:"file_id"`
			Action      string `json:"action"`
			RelativeURI string `json:"relative_uri"`
			FileSize    int64  `json:"file_size"`
			FileHash    string `json:"file_hash"`
		} `json:"files"`
	}
	reportCalls := 0
	refreshCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "access-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replica/3/files":
			switch r.Method {
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
					t.Fatalf("Decode(report) error = %v", err)
				}
				reportCalls++
				w.WriteHeader(http.StatusNoContent)
			case http.MethodGet:
				refreshCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{
					"files": []map[string]any{
						{
							"file_id":           10,
							"replica_id":        3,
							"inventory_id":      2,
							"relative_uri":      "new.txt",
							"size":              7,
							"hash":              "hash",
							"inventory_status":  "active",
							"inventory_version": 1,
							"replica_status":    "synchronized",
							"replica_version":   1,
							"created":           "2026-05-21T11:00:00Z",
							"modified":          "2026-05-21T12:00:00Z",
						},
					},
				})
			default:
				t.Fatalf("method = %s, want GET or POST", r.Method)
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "http://node-a:8081",
		},
		Auth: config.AuthConfig{NodeSecret: "node-secret"},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 2, NodeID: "node-a", URI: replicaRoot, Status: "active", Type: "filesystem"}},
		map[uint][]apiclient.ReplicaInventoryFile{3: {}},
	)

	err = runtime.reportWatcherChange(context.Background(), apiclient.Replica{ID: 3, URI: replicaRoot}, FileChange{
		RelativeURI: "new.txt",
		ChangeType:  FileChangeTypeCreated,
	})
	if err != nil {
		t.Fatalf("reportWatcherChange() error = %v", err)
	}

	if reportCalls != 1 || refreshCalls != 1 {
		t.Fatalf("reportCalls=%d refreshCalls=%d, want 1/1", reportCalls, refreshCalls)
	}
	if len(gotReport.Files) != 1 {
		t.Fatalf("len(gotReport.Files) = %d, want 1", len(gotReport.Files))
	}
	if gotReport.Files[0].FileID != nil {
		t.Fatalf("FileID = %v, want nil for new file", gotReport.Files[0].FileID)
	}
	if gotReport.Files[0].Action != "created" || gotReport.Files[0].RelativeURI != "new.txt" || gotReport.Files[0].FileSize != 7 || gotReport.Files[0].FileHash == "" {
		t.Fatalf("reported file = %+v, want new.txt size=7 with hash", gotReport.Files[0])
	}
}

func TestReplicaFileReportsIgnoreTimestampOnlyDifference(t *testing.T) {
	fileID := uint(10)
	reports := replicaFileReports(
		[]apiclient.ReplicaInventoryFile{
			{
				FileID:          fileID,
				RelativeURI:     "same.txt",
				Size:            7,
				Hash:            "content-hash",
				InventoryStatus: "active",
				Modified:        time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
			},
		},
		[]FileState{
			{
				RelativeURI: "same.txt",
				Size:        7,
				Hash:        "content-hash",
				Modified:    time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
			},
		},
	)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want 0", len(reports))
	}
}

func TestReplicaFileReportsReportHashDifference(t *testing.T) {
	fileID := uint(10)
	reports := replicaFileReports(
		[]apiclient.ReplicaInventoryFile{
			{
				FileID:          fileID,
				RelativeURI:     "same.txt",
				Size:            7,
				Hash:            "old-hash",
				InventoryStatus: "active",
				Modified:        time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
			},
		},
		[]FileState{
			{
				RelativeURI: "same.txt",
				Size:        7,
				Hash:        "new-hash",
				Modified:    time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
			},
		},
	)
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1", len(reports))
	}
	if reports[0].FileID == nil || *reports[0].FileID != fileID || reports[0].FileHash == nil || *reports[0].FileHash != "new-hash" || reports[0].Action != "updated" {
		t.Fatalf("reports[0] = %+v, want file_id=%d hash=new-hash", reports[0], fileID)
	}
}

func TestReplicaFileReportsReportDeletedMissingFile(t *testing.T) {
	fileID := uint(10)
	reports := replicaFileReports(
		[]apiclient.ReplicaInventoryFile{
			{
				FileID:          fileID,
				RelativeURI:     "missing.txt",
				Size:            7,
				Hash:            "hash",
				InventoryStatus: "active",
			},
		},
		nil,
	)
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1", len(reports))
	}
	if reports[0].Action != "deleted" || reports[0].FileID == nil || *reports[0].FileID != fileID || reports[0].RelativeURI != "missing.txt" {
		t.Fatalf("reports[0] = %+v, want deleted missing.txt file_id=%d", reports[0], fileID)
	}
	if reports[0].FileSize != nil || reports[0].FileHash != nil || reports[0].CreatedTime != nil || reports[0].ModifiedTime != nil {
		t.Fatalf("reports[0] = %+v, want deleted report without content fields", reports[0])
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buffer bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)

	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	return &buffer
}
