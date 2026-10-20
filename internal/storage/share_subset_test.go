package storage

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"replica/internal/apiclient"

	"github.com/zeebo/blake3"
)

type subsetShareCoordinator struct {
	mu      sync.Mutex
	reports [][]apiclient.ReplicaFileReport
	reject  bool
}

func (c *subsetShareCoordinator) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"node_id": "node-a", "access_token": "access", "refresh_token": "refresh", "access_token_expires_at": time.Now().Add(time.Hour), "refresh_token_expires_at": time.Now().Add(2 * time.Hour)})
		case req.URL.Path == "/node/replica/3/files" && req.Method == http.MethodPost:
			var body struct {
				Files []apiclient.ReplicaFileReport `json:"files"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode report: %v", err)
			}
			c.mu.Lock()
			c.reports = append(c.reports, body.Files)
			reject := c.reject
			c.mu.Unlock()
			if reject {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"invalid replica file update"}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(req.URL.Path, "/node/replica/3/files"):
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newSubsetShareRuntime(t *testing.T, coordinatorURL string, root string) *Runtime {
	t.Helper()
	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, coordinatorURL)
	runtime.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, InventoryType: "folder", NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{
			ID:                   1,
			InventoryID:          1,
			ReplicaID:            3,
			Status:               "active",
			LinkHash:             &linkHash,
			AnonymousPermissions: []string{"read", "create", "update", "delete"},
			FileCount:            1,
			FileIDs:              []uint{10},
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "shared.txt", InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "synchronized", ReplicaVersion: 4},
				{FileID: 11, ReplicaID: 3, InventoryID: 1, RelativeURI: "hidden.txt", InventoryStatus: "active", InventoryVersion: 2, ReplicaStatus: "synchronized", ReplicaVersion: 2},
			},
		},
	)
	return runtime
}

func servePublicSubset(runtime *Runtime, method, target string, body *strings.Reader, pathValues map[string]string, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	req.SetPathValue("link_hash", "public-link")
	for key, value := range pathValues {
		req.SetPathValue(key, value)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	runtime.ServePublicShares(rec, req)
	return rec
}

func TestSubsetShareExposesOnlyMemberFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"shared.txt", "hidden.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime := newSubsetShareRuntime(t, "http://coordinator", root)

	rec := servePublicSubset(runtime, http.MethodGet, "/s/public-link/files", nil, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list shareFileListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].FileID != 10 {
		t.Fatalf("list = %+v, want only file 10", list)
	}

	rec = servePublicSubset(runtime, http.MethodGet, "/s/public-link", nil, nil, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "file_ids") || !strings.Contains(rec.Body.String(), `"file_count":1`) {
		t.Fatalf("share body = %s, want file_count without file_ids", rec.Body.String())
	}

	if rec := servePublicSubset(runtime, http.MethodGet, "/s/public-link/files/10/content", nil, map[string]string{"file_id": "10"}, nil); rec.Code != http.StatusOK {
		t.Fatalf("member content status = %d body=%s", rec.Code, rec.Body.String())
	}
	for name, tt := range map[string]struct {
		method, target string
		headers        map[string]string
	}{
		"content":   {http.MethodGet, "/s/public-link/files/11/content", nil},
		"thumbnail": {http.MethodGet, "/s/public-link/files/11/thumbnail", nil},
		"replace":   {http.MethodPut, "/s/public-link/files/11/content", map[string]string{"If-Match": `"2"`}},
		"delete":    {http.MethodDelete, "/s/public-link/files/11", map[string]string{"If-Match": `"2"`}},
	} {
		var body *strings.Reader
		if tt.method == http.MethodPut {
			body = strings.NewReader("changed")
		}
		if rec := servePublicSubset(runtime, tt.method, tt.target, body, map[string]string{"file_id": "11"}, tt.headers); rec.Code != http.StatusNotFound {
			t.Errorf("%s of hidden file status = %d body=%s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "hidden.txt")); err != nil || string(got) != "hidden.txt" {
		t.Fatalf("hidden file changed: %q err=%v", got, err)
	}
}

func TestSubsetShareUploadReportsShareID(t *testing.T) {
	root := t.TempDir()
	coordinator := &subsetShareCoordinator{}
	server := coordinator.server(t)
	runtime := newSubsetShareRuntime(t, server.URL, root)
	runtime.wsConnected.Store(true)

	body, contentType := multipartShareUpload(t, "album/new.txt", "new content")
	rec := servePublicSubset(runtime, http.MethodPost, "/s/public-link/files", strings.NewReader(body.String()), nil, map[string]string{"Content-Type": contentType})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.reports) != 1 || len(coordinator.reports[0]) != 1 {
		t.Fatalf("reports = %+v, want one report with one file", coordinator.reports)
	}
	report := coordinator.reports[0][0]
	sum := blake3.Sum256([]byte("new content"))
	if report.Action != "created" || report.RelativeURI != "album/new.txt" || report.ShareID == nil || *report.ShareID != 1 ||
		report.FileSize == nil || *report.FileSize != int64(len("new content")) || report.FileHash == nil || *report.FileHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("report = %+v, want created album/new.txt for share 1 with size and BLAKE3 hash", report)
	}
	if got, err := os.ReadFile(filepath.Join(root, "album", "new.txt")); err != nil || string(got) != "new content" {
		t.Fatalf("uploaded file = %q err=%v", got, err)
	}
}

func TestSubsetShareUploadRollsBackWhenCoordinatorRejects(t *testing.T) {
	root := t.TempDir()
	coordinator := &subsetShareCoordinator{reject: true}
	server := coordinator.server(t)
	runtime := newSubsetShareRuntime(t, server.URL, root)
	runtime.wsConnected.Store(true)

	body, contentType := multipartShareUpload(t, "new.txt", "new content")
	rec := servePublicSubset(runtime, http.MethodPost, "/s/public-link/files", strings.NewReader(body.String()), nil, map[string]string{"Content-Type": contentType})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upload status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadGateway)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected upload was not rolled back: %v", err)
	}
}

func TestSubsetShareUploadErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hidden.txt"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := &subsetShareCoordinator{}
	server := coordinator.server(t)
	runtime := newSubsetShareRuntime(t, server.URL, root)

	upload := func(relativeURI string) *httptest.ResponseRecorder {
		body, contentType := multipartShareUpload(t, relativeURI, "content")
		return servePublicSubset(runtime, http.MethodPost, "/s/public-link/files", strings.NewReader(body.String()), nil, map[string]string{"Content-Type": contentType})
	}

	if rec := upload("offline.txt"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline upload status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusServiceUnavailable)
	}
	if _, err := os.Stat(filepath.Join(root, "offline.txt")); !os.IsNotExist(err) {
		t.Fatalf("offline upload was written: %v", err)
	}

	runtime.wsConnected.Store(true)
	if rec := upload("hidden.txt"); rec.Code != http.StatusConflict {
		t.Fatalf("hidden path upload status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "hidden.txt")); string(got) != "hidden" {
		t.Fatalf("hidden file overwritten: %q", got)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.reports) != 0 {
		t.Fatalf("reports = %d, want none", len(coordinator.reports))
	}
}
