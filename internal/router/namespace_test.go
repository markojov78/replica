package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/storage"
)

func TestCoordinatorStorageNamespacesCoexistAndOldRoutesAreRemoved(t *testing.T) {
	cfg := config.Config{
		App: config.AppConfig{
			Coordinator:       true,
			Storage:           true,
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    "http://coordinator",
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

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/admin/auth/me"},
		{http.MethodGet, "/node/auth/me"},
		{http.MethodGet, "/transfer/replicas/1/files/1/content?version=1"},
		{http.MethodGet, "/api/share/shares"},
		{http.MethodGet, "/api/share/shares/1/files/1/thumbnail"},
		{http.MethodPost, "/s/missing-link"},
		{http.MethodGet, "/dashboard"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("new namespace path %s returned 404", route.path)
		}
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/auth/me"},
		{http.MethodGet, "/internal/auth/me"},
		{http.MethodGet, "/internal/replicas/1/files/1/content?version=1"},
		{http.MethodGet, "/api/shares"},
		{http.MethodPost, "/api/public/shares/missing-link"},
		{http.MethodGet, "/admin"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("old namespace path %s status = %d, want 404", route.path, recorder.Code)
		}
	}
}
