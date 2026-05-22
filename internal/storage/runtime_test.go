package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"dropoutbox/internal/config"
)

func TestRuntimeAuthenticatesRefreshesAndReportsHeartbeat(t *testing.T) {
	var mu sync.Mutex
	loginCalls := 0
	refreshCalls := 0
	heartbeatCalls := 0

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
		done := loginCalls >= 1 && refreshCalls >= 1 && heartbeatCalls >= 2
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("loginCalls=%d refreshCalls=%d heartbeatCalls=%d", loginCalls, refreshCalls, heartbeatCalls)
}
