package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
)

func TestServeAuthenticatedSharesFiltersReadableSharesAndCachesToken(t *testing.T) {
	validateCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/auth/validate-user-token":
			validateCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 15,
				"status":                  "active",
				"access_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newShareEndpointRuntime(t, server.URL)
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{
			{
				ID:        1,
				ReplicaID: 3,
				Status:    "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      15,
					Permissions: []string{"read"},
				}},
			},
			{
				ID:        2,
				ReplicaID: 3,
				Status:    "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      12,
					Permissions: []string{"read"},
				}},
			},
		},
		map[uint][]apiclient.ReplicaInventoryFile{},
	)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/shares", nil)
		req.Header.Set("Authorization", "Bearer user-token")
		rec := httptest.NewRecorder()

		runtime.ServeAuthenticatedShares(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
		}
		var list shareListBody
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("Unmarshal(shares) error = %v", err)
		}
		if list.Page != 1 || list.Count != 20 || list.Total != 1 {
			t.Fatalf("list metadata = page:%d count:%d total:%d, want 1/20/1", list.Page, list.Count, list.Total)
		}
		if len(list.Items) != 1 || list.Items[0].ID != 1 {
			t.Fatalf("shares = %+v, want only share 1", list.Items)
		}
	}
	if validateCalls != 1 {
		t.Fatalf("validateCalls = %d, want 1", validateCalls)
	}
}

func TestServeAuthenticatedSharesUsesCoordinatorListEnvelopeAndFilters(t *testing.T) {
	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{
			{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"},
			{ID: 4, NodeID: "node-a", URI: t.TempDir(), Status: "active"},
		},
		[]apiclient.Share{
			{
				ID:        1,
				ReplicaID: 3,
				Name:      "Vacation",
				Status:    "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      15,
					Permissions: []string{"read"},
				}},
			},
			{
				ID:        2,
				ReplicaID: 4,
				Name:      "Vacation",
				Status:    "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      15,
					Permissions: []string{"read"},
				}},
			},
		},
		map[uint][]apiclient.ReplicaInventoryFile{},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/shares?replica_id=4&name=Vacation&page=1&count=1", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	var list shareListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if list.Page != 1 || list.Count != 1 || list.Total != 1 {
		t.Fatalf("list metadata = page:%d count:%d total:%d, want 1/1/1", list.Page, list.Count, list.Total)
	}
	if len(list.Items) != 1 || list.Items[0].ID != 2 {
		t.Fatalf("items = %+v, want share 2", list.Items)
	}
}

func TestRuntimeRefreshLocalStateLoadsShareAssignments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":           3,
				"inventory_id": 1,
				"node_id":      "node-a",
				"uri":          t.TempDir(),
				"status":       "active",
				"type":         "filesystem",
			}})
		case "/internal/replica/3/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
		case "/internal/shares":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":                    1,
				"inventory_id":          1,
				"replica_id":            3,
				"name":                  "Vacation",
				"status":                "active",
				"anonymous_permissions": []string{"read"},
			}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	runtime := newShareEndpointRuntime(t, server.URL)
	if err := runtime.refreshLocalState(context.Background()); err != nil {
		t.Fatalf("refreshLocalState() error = %v", err)
	}

	shares := runtime.sharesSnapshot()
	if len(shares) != 1 || shares[0].ID != 1 || shares[0].ReplicaID != 3 {
		t.Fatalf("sharesSnapshot() = %+v, want loaded share assignment", shares)
	}
}

func TestServeAuthenticatedShareWithoutReadPermissionReturnsForbidden(t *testing.T) {
	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:        1,
			ReplicaID: 3,
			Status:    "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      12,
				Permissions: []string{"read"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/shares/1", nil)
	req.SetPathValue("id", "1")
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusForbidden)
	}
}

func TestServePublicShareRequiresAnonymousRead(t *testing.T) {
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"update"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/public/shares/public-link", nil)
	req.SetPathValue("link_hash", "public-link")
	rec := httptest.NewRecorder()

	runtime.ServePublicShares(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusForbidden)
	}

	runtime.shares[0].AnonymousPermissions = []string{"read"}
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
}

func TestServeShareFilesListsOnlySynchronizedActiveFilesAndStreamsLocalContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ready.txt"), []byte("ready"), 0o600); err != nil {
		t.Fatalf("WriteFile(ready) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pending.txt"), []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile(pending) error = %v", err)
	}

	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, RelativeURI: "ready.txt", Size: 5, InventoryStatus: "active", ReplicaStatus: "synchronized"},
				{FileID: 11, ReplicaID: 3, RelativeURI: "pending.txt", Size: 7, InventoryStatus: "active", ReplicaStatus: "pending"},
				{FileID: 12, ReplicaID: 3, RelativeURI: "deleted.txt", InventoryStatus: "deleted", ReplicaStatus: "synchronized"},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/public/shares/public-link/files", nil)
	req.SetPathValue("link_hash", "public-link")
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), "pending.txt") || strings.Contains(rec.Body.String(), "deleted.txt") || !strings.Contains(rec.Body.String(), "ready.txt") {
		t.Fatalf("file list body = %s, want only ready.txt", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/public/shares/public-link/files/10/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ready" {
		t.Fatalf("status/body = %d/%q, want 200/ready", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/public/shares/public-link/files/11/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "11")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func newShareEndpointRuntime(t *testing.T, coordinatorURL string) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    coordinatorURL,
			HeartbeatInterval: time.Minute,
		},
		Auth: config.AuthConfig{
			NodeSecret:                 "secret",
			ShareAPITokenCacheDuration: 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func validationServer(t *testing.T, userID uint, validateStatus int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/internal/auth/validate-user-token":
			if validateStatus != http.StatusOK {
				w.WriteHeader(validateStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 userID,
				"status":                  "active",
				"access_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}
