package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"replica/internal/apiclient"
	"replica/internal/model"
	"replica/internal/service"
)

type fakeLogUploader struct {
	batches  [][]service.ShareAccessLogLine
	err      error
	rejected []apiclient.LogRejection
}

func (u *fakeLogUploader) UploadLogs(_ context.Context, lines []byte) (*apiclient.LogUploadResult, error) {
	if u.err != nil {
		return nil, u.err
	}
	var batch []service.ShareAccessLogLine
	scanner := bufio.NewScanner(bytes.NewReader(lines))
	var last uint64
	for scanner.Scan() {
		var line service.ShareAccessLogLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, err
		}
		batch = append(batch, line)
		last = max(last, line.ID)
	}
	u.batches = append(u.batches, batch)
	return &apiclient.LogUploadResult{LastID: last, Rejected: u.rejected}, nil
}

func bufferLineIDs(t *testing.T, path string) []uint64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var ids []uint64
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if raw == "" {
			continue
		}
		var line service.ShareAccessLogLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("line %q: %v", raw, err)
		}
		ids = append(ids, line.ID)
	}
	return ids
}

func TestNodeLogBufferAssignsIDsAfterCoordinatorCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.jsonl")
	// A line left from before a restart.
	if err := os.WriteFile(path, []byte(`{"id":12,"type":"share_access"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buffer := newNodeLogBuffer(path)

	buffer.RecordShareAccess(service.ShareAccessLogLine{Action: "read", Source: "http"})
	if got := bufferLineIDs(t, path); len(got) != 1 {
		t.Fatalf("line written before the coordinator cursor was known: %v", got)
	}

	// Coordinator has stored up to 10; the local file continues at 12.
	buffer.ObserveCoordinator(&apiclient.LogsStatus{LastID: 10})
	buffer.RecordShareAccess(service.ShareAccessLogLine{Action: "list", Source: "http"})
	if got := bufferLineIDs(t, path); len(got) != 3 || got[1] != 13 || got[2] != 14 {
		t.Fatalf("ids = %v, want [12 13 14]", got)
	}

	uploader := &fakeLogUploader{}
	more, err := buffer.UploadOnce(context.Background(), uploader)
	if err != nil || more {
		t.Fatalf("UploadOnce() = %v, %v", more, err)
	}
	if len(uploader.batches) != 1 || len(uploader.batches[0]) != 3 {
		t.Fatalf("batches = %+v, want one batch with ids 12-14", uploader.batches)
	}
	if got := bufferLineIDs(t, path); len(got) != 0 {
		t.Fatalf("confirmed lines kept after compaction: %v", got)
	}
}

func TestNodeLogBufferKeepsLinesOnUploadFailureAndResendsAfterRegression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.jsonl")
	buffer := newNodeLogBuffer(path)
	buffer.ObserveCoordinator(&apiclient.LogsStatus{LastID: 0})
	buffer.RecordShareAccess(service.ShareAccessLogLine{Action: "read", Source: "http"})
	buffer.RecordShareAccess(service.ShareAccessLogLine{Action: "read", Source: "http"})

	failing := &fakeLogUploader{err: errors.New("connection refused")}
	if _, err := buffer.UploadOnce(context.Background(), failing); err == nil {
		t.Fatal("UploadOnce() succeeded with a failing uploader")
	}
	if got := bufferLineIDs(t, path); len(got) != 2 {
		t.Fatalf("lines after failed upload = %v, want 2", got)
	}

	// Rejected lines are reported but not retried.
	uploader := &fakeLogUploader{rejected: []apiclient.LogRejection{{ID: 2, Line: 2, Error: "invalid"}}}
	if _, err := buffer.UploadOnce(context.Background(), uploader); err != nil {
		t.Fatalf("UploadOnce() error = %v", err)
	}
	if got := bufferLineIDs(t, path); len(got) != 0 {
		t.Fatalf("lines after upload = %v, want none", got)
	}

	// A coordinator cursor behind the confirmed id re-sends lines still in the file.
	buffer.RecordShareAccess(service.ShareAccessLogLine{Action: "read", Source: "http"})
	buffer.ObserveCoordinator(&apiclient.LogsStatus{LastID: 1})
	more, err := buffer.UploadOnce(context.Background(), uploader)
	if err != nil || more {
		t.Fatalf("UploadOnce() = %v, %v", more, err)
	}
	if last := uploader.batches[len(uploader.batches)-1]; len(last) != 1 || last[0].ID != 3 {
		t.Fatalf("last batch = %+v, want id 3", last)
	}
}

func TestShareAccessLogRecordsPublicAccessWithoutLinkHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.jsonl")
	linkHash := "secret-link-hash"
	r := &Runtime{
		nodeLogs:     newNodeLogBuffer(path),
		shares:       []apiclient.Share{{ID: 4, ReplicaID: 9, LinkHash: &linkHash}},
		replicaFiles: map[uint][]apiclient.ReplicaInventoryFile{9: {{FileID: 41, ReplicaID: 9, RelativeURI: "docs/a.pdf"}}},
	}
	r.nodeLogs.ObserveCoordinator(&apiclient.LogsStatus{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{link_hash}/files/{file_id}/content", r.ShareAccessLog(model.ShareAccessLogActionRead, true, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	mux.HandleFunc("DELETE /api/share/shares/{id}/files/{file_id}", r.ShareAccessLog(model.ShareAccessLogActionDelete, false, func(w http.ResponseWriter, req *http.Request) {
		NoteShareAccessUser(req.Context(), 15)
		writeStorageShareError(w, http.StatusForbidden, "share delete permission required")
	}))

	req := httptest.NewRequest(http.MethodGet, "/s/"+linkHash+"/files/41/content", nil)
	req = req.WithContext(service.ContextWithSourceAddress(req.Context(), "203.0.113.5"))
	mux.ServeHTTP(httptest.NewRecorder(), req)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/share/shares/4/files/41", nil))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(data), linkHash) {
		t.Fatalf("buffer contains the link hash: %s", data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2", lines)
	}
	var read, deleted service.ShareAccessLogLine
	if err := json.Unmarshal([]byte(lines[0]), &read); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &deleted); err != nil {
		t.Fatal(err)
	}
	if read.Type != "share_access" || read.Action != "read" || !read.Success || !read.Anonymous || read.ShareID == nil || *read.ShareID != 4 ||
		read.FileID == nil || *read.FileID != 41 || read.Path != "docs/a.pdf" || read.Bytes == nil || *read.Bytes != 5 ||
		read.SourceAddress != "203.0.113.5" || read.Source != "http" || read.UserID != nil {
		t.Fatalf("read line = %+v", read)
	}
	if deleted.Action != "delete" || deleted.Success || deleted.Anonymous || deleted.UserID == nil || *deleted.UserID != 15 ||
		deleted.Message != "share delete permission required" || deleted.Bytes != nil || deleted.Path != "docs/a.pdf" {
		t.Fatalf("delete line = %+v", deleted)
	}
}
