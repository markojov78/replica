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
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"
	"replica/internal/service"
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
		Sharing: config.SharingConfig{Enabled: true},
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

func TestStorageOnlyShareAuthRefreshProxiesToCoordinator(t *testing.T) {
	refreshCalls := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/auth/refresh" {
			t.Fatalf("path = %q, want /api/admin/auth/refresh", r.URL.Path)
		}
		refreshCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":                  15,
			"access_token":             "new-access-token",
			"refresh_token":            "new-refresh-token",
			"access_token_expires_at":  "2026-04-07T12:30:00Z",
			"refresh_token_expires_at": "2026-04-07T20:30:00Z",
		})
	}))
	defer coordinator.Close()

	handler := newStorageOnlyShareAuthHandler(t, coordinator.URL)
	req := httptest.NewRequest(http.MethodPost, "/api/share/auth/refresh", strings.NewReader(`{"refresh_token":"refresh-token"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"access_token":"new-access-token"`) {
		t.Fatalf("body = %s, want proxied coordinator response", recorder.Body.String())
	}
}

func TestStorageOnlyShareAuthMeUsesCoordinatorIntrospection(t *testing.T) {
	validateCalls := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-access-token",
				"refresh_token":            "node-refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/validate-user-token":
			validateCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 15,
				"username":                "alice",
				"status":                  "active",
				"access_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer coordinator.Close()

	handler := newStorageOnlyShareAuthHandler(t, coordinator.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/share/auth/me", nil)
	req.Header.Set("Authorization", "Bearer user-access-token")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if validateCalls != 1 {
		t.Fatalf("validateCalls = %d, want 1", validateCalls)
	}
	if recorder.Body.String() != "{\"user_id\":15,\"username\":\"alice\",\"status\":\"active\"}\n" {
		t.Fatalf("body = %s, want user_id/username/status", recorder.Body.String())
	}
}

func TestCoordinatorStorageShareAuthUsesLocalAuthService(t *testing.T) {
	database := openRouterTestDB(t)
	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{Name: "alice", Status: model.UserStatusActive, Password: hashedPassword}).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	authService := service.NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		repository.NewNodeRepository(database),
		repository.NewNodeTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)

	cfg := config.Config{
		App: config.AppConfig{
			Coordinator:       true,
			Storage:           true,
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    "http://coordinator.invalid",
			HeartbeatInterval: time.Minute,
		},
		Auth:    config.AuthConfig{NodeSecret: "node-secret"},
		Sharing: config.SharingConfig{Enabled: true},
	}
	runtime, err := storage.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	handler := New(cfg, buildinfo.Info{Version: "test"}, authService, nil, nil, nil, nil, nil, nil, runtime)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/share/auth/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginReq)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s, want %d", loginRecorder.Code, loginRecorder.Body.String(), http.StatusOK)
	}
	var pair tokenPairBody
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &pair); err != nil {
		t.Fatalf("Unmarshal(login) error = %v", err)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/share/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	meRecorder := httptest.NewRecorder()
	handler.ServeHTTP(meRecorder, meReq)
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"user_id":1`) || !strings.Contains(meRecorder.Body.String(), `"username":"alice"`) {
		t.Fatalf("me status/body = %d/%s, want user_id and username", meRecorder.Code, meRecorder.Body.String())
	}

	refreshReq := httptest.NewRequest(http.MethodPost, "/api/share/auth/refresh", strings.NewReader(`{"refresh_token":"`+pair.RefreshToken+`"}`))
	refreshReq.Header.Set("Content-Type", "application/json")
	refreshRecorder := httptest.NewRecorder()
	handler.ServeHTTP(refreshRecorder, refreshReq)
	if refreshRecorder.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s, want %d", refreshRecorder.Code, refreshRecorder.Body.String(), http.StatusOK)
	}
}

func TestStorageSharingDisabledGatesSharingRoutesOnly(t *testing.T) {
	cfg := config.Config{
		App: config.AppConfig{
			Storage:           true,
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    "http://coordinator.invalid",
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

	for _, tc := range []struct {
		name   string
		method string
		path   string
		api    bool
	}{
		{name: "authenticated API", method: http.MethodGet, path: "/api/share/shares", api: true},
		{name: "anonymous API", method: http.MethodGet, path: "/s/public-link", api: true},
		{name: "sharing UI", method: http.MethodGet, path: "/share"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusNotFound)
			}
			if tc.api && !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("Content-Type = %q, want JSON error response", recorder.Header().Get("Content-Type"))
			}
		})
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/transfer/replicas/1/files/1/content?version=1", nil))
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("transfer status = %d body=%s, want transfer route not gated by sharing", recorder.Code, recorder.Body.String())
	}
}

func newStorageOnlyShareAuthHandler(t *testing.T, coordinatorURL string) http.Handler {
	t.Helper()
	cfg := config.Config{
		App: config.AppConfig{
			Storage:           true,
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    coordinatorURL,
			HeartbeatInterval: time.Minute,
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
		Sharing: config.SharingConfig{Enabled: true},
	}
	runtime, err := storage.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return New(cfg, buildinfo.Info{Version: "test"}, nil, nil, nil, nil, nil, nil, nil, runtime)
}
