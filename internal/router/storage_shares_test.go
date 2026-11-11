package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// nodeUserAuthCoordinator is a mock coordinator for /node/users/auth/* relays. Node logins and refreshes
// issue numbered node tokens; requests to path are answered by respond.
type nodeUserAuthCoordinator struct {
	nodeAuthCalls int
	calls         []nodeUserAuthCall
}

type nodeUserAuthCall struct {
	authorization string
	body          map[string]string
}

func newNodeUserAuthCoordinator(t *testing.T, path string, respond func(w http.ResponseWriter, call int)) (*nodeUserAuthCoordinator, *httptest.Server) {
	t.Helper()
	mock := &nodeUserAuthCoordinator{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/node/auth/login", "/node/auth/refresh":
			mock.nodeAuthCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-access-token-" + strconv.Itoa(mock.nodeAuthCalls),
				"refresh_token":            "node-refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case path:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode relay body: %v", err)
			}
			mock.calls = append(mock.calls, nodeUserAuthCall{authorization: r.Header.Get("Authorization"), body: body})
			respond(w, len(mock.calls))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return mock, server
}

func writeShareTokenPair(w http.ResponseWriter, accessToken string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_id":                  15,
		"access_token":             accessToken,
		"refresh_token":            "refresh-token",
		"access_token_expires_at":  "2026-04-07T12:30:00Z",
		"refresh_token_expires_at": "2026-04-07T20:30:00Z",
	})
}

func storageOnlyShareAuthRequest(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("X-Forwarded-For", "10.9.9.9")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestStorageOnlyAuthLoginProxiesToCoordinator(t *testing.T) {
	mock, coordinator := newNodeUserAuthCoordinator(t, "/node/users/auth/login", func(w http.ResponseWriter, _ int) {
		writeShareTokenPair(w, "access-token")
	})
	defer coordinator.Close()

	recorder := storageOnlyShareAuthRequest(newStorageOnlyShareAuthHandler(t, coordinator.URL), "/api/share/auth/login", `{"username":"alice","password":"secret"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("login calls = %d, want 1", len(mock.calls))
	}
	call := mock.calls[0]
	if call.authorization != "Bearer node-access-token-1" {
		t.Fatalf("Authorization = %q, want node access token", call.authorization)
	}
	if call.body["username"] != "alice" || call.body["password"] != "secret" || call.body["source_address"] != "203.0.113.5" {
		t.Fatalf("relay body = %v, want credentials and client address", call.body)
	}
	if !strings.Contains(recorder.Body.String(), `"access_token":"access-token"`) {
		t.Fatalf("body = %s, want proxied coordinator response", recorder.Body.String())
	}
}

func TestStorageOnlyShareAuthRefreshProxiesToCoordinator(t *testing.T) {
	mock, coordinator := newNodeUserAuthCoordinator(t, "/node/users/auth/refresh", func(w http.ResponseWriter, _ int) {
		writeShareTokenPair(w, "new-access-token")
	})
	defer coordinator.Close()

	recorder := storageOnlyShareAuthRequest(newStorageOnlyShareAuthHandler(t, coordinator.URL), "/api/share/auth/refresh", `{"refresh_token":"refresh-token"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if len(mock.calls) != 1 || mock.calls[0].body["refresh_token"] != "refresh-token" || mock.calls[0].body["source_address"] != "203.0.113.5" {
		t.Fatalf("refresh calls = %+v, want one relay with client address", mock.calls)
	}
	if !strings.Contains(recorder.Body.String(), `"access_token":"new-access-token"`) {
		t.Fatalf("body = %s, want proxied coordinator response", recorder.Body.String())
	}
}

func TestStorageOnlyShareAuthLoginRetriesRejectedNodeToken(t *testing.T) {
	mock, coordinator := newNodeUserAuthCoordinator(t, "/node/users/auth/login", func(w http.ResponseWriter, call int) {
		if call == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"title":"Unauthorized","detail":"missing authenticated node"}`))
			return
		}
		writeShareTokenPair(w, "access-token")
	})
	defer coordinator.Close()

	recorder := storageOnlyShareAuthRequest(newStorageOnlyShareAuthHandler(t, coordinator.URL), "/api/share/auth/login", `{"username":"alice","password":"secret"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if len(mock.calls) != 2 || mock.calls[1].authorization != "Bearer node-access-token-2" {
		t.Fatalf("login calls = %+v, want retry with a new node token", mock.calls)
	}
}

func TestStorageOnlyShareAuthLoginDoesNotRetryUserFailure(t *testing.T) {
	mock, coordinator := newNodeUserAuthCoordinator(t, "/node/users/auth/login", func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"title":"Unauthorized","detail":"invalid username or password"}`))
	})
	defer coordinator.Close()

	recorder := storageOnlyShareAuthRequest(newStorageOnlyShareAuthHandler(t, coordinator.URL), "/api/share/auth/login", `{"username":"alice","password":"wrong"}`)

	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "invalid username or password") {
		t.Fatalf("status = %d body=%s, want relayed 401", recorder.Code, recorder.Body.String())
	}
	if len(mock.calls) != 1 || mock.nodeAuthCalls != 1 {
		t.Fatalf("login calls = %d node auth calls = %d, want 1 and 1", len(mock.calls), mock.nodeAuthCalls)
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
		case "/node/users/auth/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 15,
				"username":                "alice",
				"name":                    "Alice",
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
	if recorder.Body.String() != "{\"user_id\":15,\"username\":\"alice\",\"name\":\"Alice\",\"status\":\"active\"}\n" {
		t.Fatalf("body = %s, want user_id/username/name/status", recorder.Body.String())
	}
}

func TestCoordinatorStorageShareAuthUsesLocalAuthService(t *testing.T) {
	database := openRouterTestDB(t)
	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{Username: "alice", Name: "Alice", Status: model.UserStatusActive, Password: hashedPassword}).Error; err != nil {
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
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"user_id":1`) ||
		!strings.Contains(meRecorder.Body.String(), `"username":"alice"`) || !strings.Contains(meRecorder.Body.String(), `"name":"Alice"`) {
		t.Fatalf("me status/body = %d/%s, want user_id, username, and name", meRecorder.Code, meRecorder.Body.String())
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

func TestAuthenticatedPreviewRouteIsRegisteredAndGated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Config{App: config.AppConfig{Storage: true, NodeID: "node-a", NodeAddress: "http://node-a", CoordinatorURL: "http://coordinator.invalid", HeartbeatInterval: time.Minute}, Auth: config.AuthConfig{NodeSecret: "secret"}, Sharing: config.SharingConfig{Enabled: enabled}}
		runtime, err := storage.NewRuntime(cfg)
		if err != nil {
			t.Fatal(err)
		}
		handler := New(cfg, buildinfo.Info{Version: "test"}, nil, nil, nil, nil, nil, nil, nil, runtime)
		req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files/41/preview", nil)
		req.Header.Set("X-API-Version", "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		want := http.StatusNotFound
		if enabled {
			want = http.StatusUnauthorized
		}
		if rec.Code != want {
			t.Fatalf("enabled=%t status=%d, want %d; body=%s", enabled, rec.Code, want, rec.Body.String())
		}
	}
}

func TestPublicPreviewRouteRegistered(t *testing.T) {
	cfg := config.Config{App: config.AppConfig{Storage: true, NodeID: "node-a", NodeAddress: "http://node-a", CoordinatorURL: "http://coordinator.invalid", HeartbeatInterval: time.Minute}, Auth: config.AuthConfig{NodeSecret: "secret"}, Sharing: config.SharingConfig{Enabled: true}}
	runtime, err := storage.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerStorageShareRoutes(mux, services{storage: runtime})
	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files/41/preview", nil)
	_, pattern := mux.Handler(req)
	if pattern != "GET /s/{link_hash}/files/{file_id}/preview" {
		t.Fatalf("route pattern = %q", pattern)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("missing public share response = %d %s", rec.Code, rec.Body.String())
	}
}
