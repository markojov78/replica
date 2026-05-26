package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dropoutbox/internal/config"

	"github.com/gorilla/websocket"
)

func TestRuntimeAuthenticatesRefreshesAndReportsHeartbeat(t *testing.T) {
	var mu sync.Mutex
	loginCalls := 0
	refreshCalls := 0
	heartbeatCalls := 0
	replicaCalls := 0
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
		done := loginCalls >= 1 && refreshCalls >= 1 && heartbeatCalls >= 2 && replicaCalls >= 1 && wsConnections >= 1
		mu.Unlock()
		if done && strings.Contains(logOutput.String(), "got command id=7 type=refresh_state status=pending") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("loginCalls=%d refreshCalls=%d heartbeatCalls=%d replicaCalls=%d wsConnections=%d logs=%q", loginCalls, refreshCalls, heartbeatCalls, replicaCalls, wsConnections, logOutput.String())
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
