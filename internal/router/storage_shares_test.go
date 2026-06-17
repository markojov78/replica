package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/storage"
)

func TestStorageOnlyAuthLoginProxiesToCoordinator(t *testing.T) {
	loginCalls := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/auth/login" {
			t.Fatalf("path = %q, want /api/admin/auth/login", r.URL.Path)
		}
		loginCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":                  15,
			"access_token":             "access-token",
			"refresh_token":            "refresh-token",
			"access_token_expires_at":  "2026-04-07T12:30:00Z",
			"refresh_token_expires_at": "2026-04-07T20:30:00Z",
		})
	}))
	defer coordinator.Close()

	cfg := config.Config{
		App: config.AppConfig{
			Storage:           true,
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    coordinator.URL,
			HeartbeatInterval: time.Minute,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	}
	runtime, err := storage.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	handler := New(cfg, buildinfo.Info{Version: "test"}, nil, nil, nil, nil, nil, nil, nil, runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/share/auth/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if loginCalls != 1 {
		t.Fatalf("loginCalls = %d, want 1", loginCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"access_token":"access-token"`) {
		t.Fatalf("body = %s, want proxied coordinator response", recorder.Body.String())
	}
}
