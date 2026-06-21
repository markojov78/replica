package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
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
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/validate-user-token":
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
		req := httptest.NewRequest(http.MethodGet, "/api/share/shares", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares?replica_id=4&name=Vacation&page=1&count=1", nil)
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
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/replicas":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":           3,
				"inventory_id": 1,
				"node_id":      "node-a",
				"uri":          t.TempDir(),
				"status":       "active",
				"type":         "filesystem",
			}})
		case "/node/replica/3/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
		case "/node/shares":
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

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/1", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/s/public-link", nil)
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
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("second"), 0o600); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
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
				{FileID: 11, ReplicaID: 3, RelativeURI: "second.txt", Size: 6, InventoryStatus: "active", ReplicaStatus: "synchronized"},
				{FileID: 12, ReplicaID: 3, RelativeURI: "pending.txt", Size: 7, InventoryStatus: "active", ReplicaStatus: "pending"},
				{FileID: 13, ReplicaID: 3, RelativeURI: "deleted.txt", InventoryStatus: "deleted", ReplicaStatus: "synchronized"},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files?page=2&count=1", nil)
	req.SetPathValue("link_hash", "public-link")
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	var list shareFileListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if list.Page != 2 || list.Count != 1 || list.Total != 2 {
		t.Fatalf("file list metadata = page:%d count:%d total:%d, want 2/1/2", list.Page, list.Count, list.Total)
	}
	if len(list.Items) != 1 || list.Items[0].RelativeURI != "second.txt" {
		t.Fatalf("file list items = %+v, want second.txt page", list.Items)
	}
	if strings.Contains(rec.Body.String(), "pending.txt") || strings.Contains(rec.Body.String(), "deleted.txt") || strings.Contains(rec.Body.String(), `"files"`) {
		t.Fatalf("file list body = %s, want paginated items without unavailable files", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ready" {
		t.Fatalf("status/body = %d/%q, want 200/ready", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/12/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "12")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestServeAuthenticatedShareFilesAppliesDocumentedFiltersAndSorting(t *testing.T) {
	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	created := time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC)
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:          4,
			InventoryID: 1,
			ReplicaID:   3,
			Status:      "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      15,
				Permissions: []string{"read"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 30, ReplicaID: 3, InventoryID: 1, RelativeURI: "album/PhotoB.jpg", Size: 50, InventoryStatus: "active", ReplicaStatus: "synchronized", Created: created.Add(2 * time.Hour), Modified: created.Add(2 * time.Hour)},
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "other-album/photoA.png", Size: 20, InventoryStatus: "active", ReplicaStatus: "synchronized", Created: created, Modified: created},
				{FileID: 20, ReplicaID: 3, InventoryID: 1, RelativeURI: "docs/photo.txt", Size: 20, InventoryStatus: "active", ReplicaStatus: "synchronized", Created: created.Add(time.Hour), Modified: created.Add(time.Hour)},
				{FileID: 40, ReplicaID: 3, InventoryID: 1, RelativeURI: "album/note.txt", Size: 10, InventoryStatus: "active", ReplicaStatus: "synchronized", Created: created.Add(3 * time.Hour), Modified: created.Add(3 * time.Hour)},
				{FileID: 50, ReplicaID: 3, InventoryID: 2, RelativeURI: "album/photo-leak.jpg", Size: 90, InventoryStatus: "active", ReplicaStatus: "synchronized", Created: created.Add(4 * time.Hour), Modified: created.Add(4 * time.Hour)},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files?name=PHOTO&path=album&sort=size&order=desc&page=1&count=1", nil)
	req.SetPathValue("id", "4")
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	var list shareFileListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if list.Page != 1 || list.Count != 1 || list.Total != 2 {
		t.Fatalf("file list metadata = page:%d count:%d total:%d, want 1/1/2", list.Page, list.Count, list.Total)
	}
	if len(list.Items) != 1 || list.Items[0].FileID != 30 {
		t.Fatalf("file list items = %+v, want largest matching file first", list.Items)
	}
	if strings.Contains(rec.Body.String(), "docs/photo.txt") || strings.Contains(rec.Body.String(), "note.txt") || strings.Contains(rec.Body.String(), "photo-leak.jpg") {
		t.Fatalf("file list body = %s, want name/path filtered files from share inventory only", rec.Body.String())
	}
}

func TestServePublicShareFilesAppliesStableSortingAndValidatesQuery(t *testing.T) {
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 30, ReplicaID: 3, InventoryID: 1, RelativeURI: "z/photo2.jpg", Size: 20, InventoryStatus: "active", ReplicaStatus: "synchronized"},
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "a/photo1.jpg", Size: 20, InventoryStatus: "active", ReplicaStatus: "synchronized"},
				{FileID: 20, ReplicaID: 3, InventoryID: 1, RelativeURI: "m/photo3.jpg", Size: 30, InventoryStatus: "active", ReplicaStatus: "synchronized"},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files", nil)
	req.SetPathValue("link_hash", "public-link")
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	var list shareFileListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if len(list.Items) != 3 || list.Items[0].FileID != 10 || list.Items[1].FileID != 20 || list.Items[2].FileID != 30 {
		t.Fatalf("file list items = %+v, want default id asc sort", list.Items)
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files?sort=size&order=asc", nil)
	req.SetPathValue("link_hash", "public-link")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	list = shareFileListBody{}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if len(list.Items) != 3 || list.Items[0].FileID != 30 || list.Items[1].FileID != 10 || list.Items[2].FileID != 20 {
		t.Fatalf("file list items = %+v, want stable size sort preserving ties", list.Items)
	}

	for _, target := range []string{
		"/s/public-link/files?sort=relative_uri",
		"/s/public-link/files?order=up",
		"/s/public-link/files?page=0",
		"/s/public-link/files?count=bad",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("link_hash", "public-link")
		rec = httptest.NewRecorder()
		runtime.ServePublicShares(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s, want %d", target, rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	}
}

func TestServeAuthenticatedShareFileContentFollowsDocumentedResponse(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "photo.jpg"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("WriteFile(photo) error = %v", err)
	}

	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:          4,
			InventoryID: 1,
			ReplicaID:   3,
			Status:      "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      15,
				Permissions: []string{"read"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{
					FileID:           41,
					ReplicaID:        3,
					InventoryID:      1,
					RelativeURI:      "album/photo.jpg",
					Size:             10,
					InventoryStatus:  "active",
					InventoryVersion: 24,
					ReplicaStatus:    "synchronized",
					ReplicaVersion:   24,
				},
			},
		},
	)
	if err := os.MkdirAll(filepath.Join(root, "album"), 0o700); err != nil {
		t.Fatalf("MkdirAll(album) error = %v", err)
	}
	if err := os.Rename(filepath.Join(root, "photo.jpg"), filepath.Join(root, "album", "photo.jpg")); err != nil {
		t.Fatalf("Rename(photo) error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files/41/content", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "41")
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Fatalf("status/body = %d/%q, want 200/full content", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("Content-Length = %q, want 10", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("ETag") != `"file-41-v24"` {
		t.Fatalf("ETag = %q, want file/version ETag", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" {
		t.Fatalf("Cache-Control = %q, want private revalidation", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Disposition") != `inline; filename="photo.jpg"` {
		t.Fatalf("Content-Disposition = %q, want inline filename", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", rec.Header().Get("Accept-Ranges"))
	}
}

func TestServePublicShareFileContentRangeAndErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "video.mp4"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("WriteFile(video) error = %v", err)
	}
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "video.mp4", Size: 10, InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "synchronized", ReplicaVersion: 4},
				{FileID: 11, ReplicaID: 3, InventoryID: 2, RelativeURI: "other.txt", Size: 5, InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1},
				{FileID: 12, ReplicaID: 3, InventoryID: 1, RelativeURI: "pending.txt", Size: 5, InventoryStatus: "active", InventoryVersion: 2, ReplicaStatus: "pending", ReplicaVersion: 1},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("range status/body = %d/%q, want 206/2345", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", rec.Header().Get("Content-Range"))
	}
	if rec.Header().Get("ETag") != `"file-10-v4"` {
		t.Fatalf("range ETag = %q, want file/version ETag", rec.Header().Get("ETag"))
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Range", "bytes=bad")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed range status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Range", "bytes=20-30")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusRequestedRangeNotSatisfiable)
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/11/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "11")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("different inventory status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNotFound)
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/12/content", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "12")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsynchronized status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestServeAuthenticatedShareFileThumbnailStreamsGeneratedLocalImage(t *testing.T) {
	root := t.TempDir()
	writeSharePNG(t, filepath.Join(root, "photo.png"), 64, 32)

	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:          1,
			InventoryID: 1,
			ReplicaID:   3,
			Status:      "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      15,
				Permissions: []string{"read"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{
					FileID:           10,
					ReplicaID:        3,
					InventoryID:      1,
					RelativeURI:      "photo.png",
					Size:             1024,
					InventoryStatus:  "active",
					InventoryVersion: 4,
					ReplicaStatus:    "synchronized",
					ReplicaVersion:   4,
				},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/1/files/10/thumbnail?size=256", nil)
	req.SetPathValue("id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable public cache", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("ETag") != `"file-10-v4-s256"` {
		t.Fatalf("ETag = %q, want file/version/size ETag", rec.Header().Get("ETag"))
	}
	img, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("jpeg.Decode(response) error = %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
		t.Fatalf("thumbnail size = %dx%d, want no upscale 64x32", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestServePublicShareFileThumbnailReturnsGenericSVG(t *testing.T) {
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{
					FileID:           10,
					ReplicaID:        3,
					InventoryID:      1,
					RelativeURI:      "document.pdf",
					Size:             100,
					InventoryStatus:  "active",
					InventoryVersion: 4,
					ReplicaStatus:    "synchronized",
					ReplicaVersion:   4,
				},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/thumbnail", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec := httptest.NewRecorder()

	runtime.ServePublicShares(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("Content-Type = %q, want image/svg+xml", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "PDF") {
		t.Fatalf("body = %s, want PDF generic thumbnail", rec.Body.String())
	}
}

func TestServeShareFileThumbnailErrors(t *testing.T) {
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "photo.png", InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "synchronized", ReplicaVersion: 4},
				{FileID: 11, ReplicaID: 3, InventoryID: 1, RelativeURI: "pending.png", InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "pending", ReplicaVersion: 3},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files/10/thumbnail?size=999", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid size status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/s/public-link/files/11/thumbnail", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "11")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsynchronized status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestServeAuthenticatedShareFilesUsesCoordinatorListEnvelope(t *testing.T) {
	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, NodeID: "node-a", URI: t.TempDir(), Status: "active"}},
		[]apiclient.Share{{
			ID:        1,
			ReplicaID: 3,
			Status:    "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      15,
				Permissions: []string{"read"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, RelativeURI: "a.txt", InventoryStatus: "active", ReplicaStatus: "synchronized"},
				{FileID: 11, ReplicaID: 3, RelativeURI: "b.txt", InventoryStatus: "active", ReplicaStatus: "synchronized"},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/1/files?page=1&count=1", nil)
	req.SetPathValue("id", "1")
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()

	runtime.ServeAuthenticatedShares(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	var list shareFileListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if list.Page != 1 || list.Count != 1 || list.Total != 2 {
		t.Fatalf("file list metadata = page:%d count:%d total:%d, want 1/1/2", list.Page, list.Count, list.Total)
	}
	if len(list.Items) != 1 || list.Items[0].RelativeURI != "a.txt" {
		t.Fatalf("file list items = %+v, want first file", list.Items)
	}
	if strings.Contains(rec.Body.String(), `"files"`) {
		t.Fatalf("file list body = %s, want items envelope", rec.Body.String())
	}
}

func TestServeAuthenticatedShareWriteEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}

	runtime := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, InventoryType: "folder", NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:          1,
			InventoryID: 1,
			ReplicaID:   3,
			Status:      "active",
			UserPermissions: []apiclient.UserPermission{{
				UserID:      15,
				Permissions: []string{"read", "create", "update", "delete"},
			}},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{
					FileID:           10,
					ReplicaID:        3,
					InventoryID:      1,
					RelativeURI:      "existing.txt",
					InventoryStatus:  "active",
					InventoryVersion: 4,
					ReplicaStatus:    "synchronized",
					ReplicaVersion:   4,
				},
			},
		},
	)

	body, contentType := multipartShareUpload(t, "nested/new.txt", "new content")
	req := httptest.NewRequest(http.MethodPost, "/api/share/shares/1/files", body)
	req.SetPathValue("id", "1")
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	runtime.ServeAuthenticatedShares(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}
	if got, err := os.ReadFile(filepath.Join(root, "nested", "new.txt")); err != nil || string(got) != "new content" {
		t.Fatalf("created file = %q, err=%v, want new content", string(got), err)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/share/shares/1/files/10/content", strings.NewReader("updated"))
	req.SetPathValue("id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("If-Match", `"4"`)
	rec = httptest.NewRecorder()
	runtime.ServeAuthenticatedShares(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("update status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}
	if got, err := os.ReadFile(filepath.Join(root, "existing.txt")); err != nil || string(got) != "updated" {
		t.Fatalf("updated file = %q, err=%v, want updated", string(got), err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/share/shares/1/files/10", nil)
	req.SetPathValue("id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("If-Match", `"4"`)
	rec = httptest.NewRecorder()
	runtime.ServeAuthenticatedShares(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
	if _, err := os.Stat(filepath.Join(root, "existing.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat err = %v, want not exist", err)
	}
}

func TestServePublicShareWriteEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "public.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(public) error = %v", err)
	}
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, InventoryType: "folder", NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read", "create", "update", "delete"},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{
					FileID:           10,
					ReplicaID:        3,
					InventoryID:      1,
					RelativeURI:      "public.txt",
					InventoryStatus:  "active",
					InventoryVersion: 4,
					ReplicaStatus:    "synchronized",
					ReplicaVersion:   4,
				},
			},
		},
	)

	body, contentType := multipartShareUpload(t, "upload.txt", "upload")
	req := httptest.NewRequest(http.MethodPost, "/s/public-link/files", body)
	req.SetPathValue("link_hash", "public-link")
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("public create status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}
	if got, err := os.ReadFile(filepath.Join(root, "upload.txt")); err != nil || string(got) != "upload" {
		t.Fatalf("public created file = %q, err=%v, want upload", string(got), err)
	}

	req = httptest.NewRequest(http.MethodPut, "/s/public-link/files/10/content", strings.NewReader("updated"))
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("If-Match", `"4"`)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("public update status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}

	req = httptest.NewRequest(http.MethodDelete, "/s/public-link/files/10", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("If-Match", `"4"`)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("public delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
}

func TestServeShareWriteEndpointErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	linkHash := "public-link"
	folderLinkHash := "folder-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{
			{ID: 3, InventoryID: 1, InventoryType: "file", NodeID: "node-a", URI: root, Status: "active"},
			{ID: 4, InventoryID: 2, InventoryType: "folder", NodeID: "node-a", URI: root, Status: "active"},
		},
		[]apiclient.Share{
			{
				ID:                   1,
				InventoryID:          1,
				ReplicaID:            3,
				Status:               "active",
				LinkHash:             &linkHash,
				AnonymousPermissions: []string{"read"},
			},
			{
				ID:                   2,
				InventoryID:          2,
				ReplicaID:            4,
				Status:               "active",
				LinkHash:             &folderLinkHash,
				AnonymousPermissions: []string{"read", "create"},
			},
		},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "existing.txt", InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "synchronized", ReplicaVersion: 4},
				{FileID: 11, ReplicaID: 3, InventoryID: 1, RelativeURI: "pending.txt", InventoryStatus: "active", InventoryVersion: 5, ReplicaStatus: "pending", ReplicaVersion: 4},
			},
			4: {
				{FileID: 20, ReplicaID: 4, InventoryID: 2, RelativeURI: "folder.txt", InventoryStatus: "active", InventoryVersion: 7, ReplicaStatus: "synchronized", ReplicaVersion: 7},
			},
		},
	)

	body, contentType := multipartShareUpload(t, "new.txt", "new")
	req := httptest.NewRequest(http.MethodPost, "/s/public-link/files", body)
	req.SetPathValue("link_hash", "public-link")
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing create status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusForbidden)
	}

	runtime.shares[0].AnonymousPermissions = []string{"read", "create"}
	body, contentType = multipartShareUpload(t, "new.txt", "new")
	req = httptest.NewRequest(http.MethodPost, "/s/public-link/files", body)
	req.SetPathValue("link_hash", "public-link")
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("file inventory create status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}

	body, contentType = multipartShareUpload(t, "folder.txt", "new")
	req = httptest.NewRequest(http.MethodPost, "/s/folder-link/files", body)
	req.SetPathValue("link_hash", "folder-link")
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}

	body, contentType = multipartShareUpload(t, "../escape.txt", "new")
	req = httptest.NewRequest(http.MethodPost, "/s/folder-link/files", body)
	req.SetPathValue("link_hash", "folder-link")
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid relative uri status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodPut, "/s/public-link/files/10/content", strings.NewReader("updated"))
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing update permission status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusForbidden)
	}

	runtime.shares[0].AnonymousPermissions = []string{"read", "update", "delete"}
	req = httptest.NewRequest(http.MethodPut, "/s/public-link/files/10/content", strings.NewReader("updated"))
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusPreconditionRequired)
	}

	req = httptest.NewRequest(http.MethodPut, "/s/public-link/files/10/content", strings.NewReader("updated"))
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("If-Match", "4")
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodDelete, "/s/public-link/files/10", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "10")
	req.Header.Set("If-Match", `"3"`)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("version conflict status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}

	req = httptest.NewRequest(http.MethodDelete, "/s/public-link/files/11", nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "11")
	req.Header.Set("If-Match", `"5"`)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsynchronized status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}

	req = httptest.NewRequest(http.MethodPut, "/s/public-link/files/20/content", strings.NewReader("updated"))
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "20")
	req.Header.Set("If-Match", `"7"`)
	rec = httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("different inventory status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNotFound)
	}
}

func multipartShareUpload(t *testing.T, relativeURI string, content string) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("relative_uri", relativeURI); err != nil {
		t.Fatalf("WriteField(relative_uri) error = %v", err)
	}
	part, err := writer.CreateFormFile("file", "upload")
	if err != nil {
		t.Fatalf("CreateFormFile(file) error = %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("Write(file) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(multipart) error = %v", err)
	}
	return body, writer.FormDataContentType()
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
		Sharing: config.SharingConfig{
			ThumbnailSizes:             []int{128, 256},
			ThumbnailDefaultSize:       128,
			ThumbnailsGenerateForVideo: false,
			FfmpegPath:                 "ffmpeg",
			ThumbnailStorage:           t.TempDir(),
			ThumbnailStorageLimitMB:    500,
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func writeSharePNG(t *testing.T, path string, width int, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 180, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(png) error = %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
}

func validationServer(t *testing.T, userID uint, validateStatus int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/validate-user-token":
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
