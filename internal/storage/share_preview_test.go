package storage

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"replica/internal/apiclient"
	"replica/internal/service"
)

func newPreviewRuntime(t *testing.T, enabled bool, name string, data []byte) (*Runtime, string) {
	t.Helper()
	r := newShareEndpointRuntime(t, validationServer(t, 15, http.StatusOK))
	r.cfg.Sharing.ProgressiveLoading = enabled
	r.cfg.Sharing.ImageStorage = filepath.Join(t.TempDir(), "images")
	r.cfg.Sharing.ImageStorageLimitMB = 1
	r.setLocalConfig(nil, nil)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	r.setLocalState(
		[]apiclient.Replica{{ID: 3, InventoryID: 1, NodeID: "node-a", URI: root, Status: "active"}},
		[]apiclient.Share{{ID: 4, InventoryID: 1, ReplicaID: 3, Status: "active", UserPermissions: []apiclient.UserPermission{{UserID: 15, Permissions: []string{"read"}}}}},
		map[uint][]apiclient.ReplicaInventoryFile{3: {{FileID: 41, ReplicaID: 3, InventoryID: 1, RelativeURI: name, Size: int64(len(data)), InventoryStatus: "active", InventoryVersion: 24, ReplicaStatus: "synchronized", ReplicaVersion: 24}}},
	)
	return r, filepath.Join(root, name)
}

func previewRequest(r *Runtime, rangeHeader, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files/41/preview", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "41")
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Range", rangeHeader)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	r.ServeAuthenticatedShares(rec, req)
	return rec
}

func previewJPEG(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 120, B: 140, A: 255})
		}
	}
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	copy(data, buf.Bytes())
	return data
}

func TestSharePreviewProgressiveCacheRangesAndOriginalEndpoint(t *testing.T) {
	data := previewJPEG(t, int(service.ProgressiveJPEGThresholdBytes)+1)
	r, source := newPreviewRuntime(t, true, "photo.jpg", data)
	full := previewRequest(r, "", "")
	if full.Code != http.StatusOK || !bytes.Contains(full.Body.Bytes(), []byte{0xff, 0xc2}) {
		t.Fatalf("progressive response: %d %q", full.Code, full.Body.Bytes()[:min(100, full.Body.Len())])
	}
	img, err := jpeg.Decode(bytes.NewReader(full.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != 32 || img.Bounds().Dy() != 16 {
		t.Fatalf("decode preview: %v", err)
	}
	if full.Header().Get("ETag") != `"file-41-v24-preview"` || full.Header().Get("Content-Disposition") != `inline; filename="photo.jpg"` || full.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("headers: %v", full.Header())
	}
	// Content continues to return the original bytes with its original version tag.
	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files/41/content", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "41")
	req.Header.Set("Authorization", "Bearer user-token")
	content := httptest.NewRecorder()
	r.ServeAuthenticatedShares(content, req)
	if content.Code != 200 || !bytes.Equal(content.Body.Bytes(), data) || content.Header().Get("ETag") != `"file-41-v24"` {
		t.Fatal("content endpoint changed")
	}
	// Prove the next request can use the cached derivative without source IO.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	r.setLocalConfig(nil, nil)
	partial := previewRequest(r, "bytes=2-9", "")
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), full.Body.Bytes()[2:10]) {
		t.Fatalf("range status/body: %d %q", partial.Code, partial.Body.Bytes())
	}
	if partial.Header().Get("Content-Range") != "bytes 2-9/"+strconv.Itoa(full.Body.Len()) {
		t.Fatalf("range header: %v", partial.Header())
	}
	if rec := previewRequest(r, "", full.Header().Get("ETag")); rec.Code != 304 {
		t.Fatalf("conditional status: %d", rec.Code)
	}
	if rec := previewRequest(r, "bytes=bad", ""); rec.Code != 400 {
		t.Fatalf("malformed range: %d", rec.Code)
	}
	if rec := previewRequest(r, "bytes=999999-", ""); rec.Code != 416 {
		t.Fatalf("unsatisfiable range: %d", rec.Code)
	}
}

func TestSharePreviewOriginalFallback(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		size    int
	}{
		{"photo.jpg", false, int(service.ProgressiveJPEGThresholdBytes) + 1},
		{"photo.jpg", true, int(service.ProgressiveJPEGThresholdBytes)},
		{"photo.jpg", true, int(service.ProgressiveJPEGThresholdBytes) - 1},
		{"image.webp", true, int(service.ProgressiveJPEGThresholdBytes) + 1},
		{"video.mp4", true, int(service.ProgressiveJPEGThresholdBytes) + 1},
		{"document.pdf", true, 100},
	} {
		t.Run(test.name+strconv.Itoa(test.size)+strconv.FormatBool(test.enabled), func(t *testing.T) {
			data := make([]byte, test.size)
			copy(data, "unchanged original")
			r, _ := newPreviewRuntime(t, test.enabled, test.name, data)
			rec := previewRequest(r, "", "")
			if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || rec.Header().Get("ETag") != `"file-41-v24-original"` {
				t.Fatalf("original response: %d %v", rec.Code, rec.Header())
			}
			if rec := previewRequest(r, "bytes=0-8", ""); rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), data[:9]) {
				t.Fatal("original range not preserved")
			}
		})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, int(service.ProgressiveJPEGThresholdBytes)+1)
	copy(data, buf.Bytes())
	r, _ := newPreviewRuntime(t, true, "transparent.png", data)
	if rec := previewRequest(r, "", ""); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatal("transparent PNG changed")
	}
}

func TestSharePreviewAuthorizationAndStateErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Runtime)
		status int
	}{
		{"no permission", func(r *Runtime) { r.shares[0].UserPermissions = nil }, 403},
		{"inactive share", func(r *Runtime) { r.shares[0].Status = "inactive" }, 404},
		{"inactive replica", func(r *Runtime) { r.replicas[0].Status = "inactive" }, 404},
		{"wrong inventory", func(r *Runtime) { r.replicaFiles[3][0].InventoryID = 99 }, 404},
		{"inactive file", func(r *Runtime) { r.replicaFiles[3][0].InventoryStatus = "deleted" }, 404},
		{"unsynchronized", func(r *Runtime) { r.replicaFiles[3][0].ReplicaStatus = "pending" }, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := newPreviewRuntime(t, true, "photo.jpg", previewJPEG(t, int(service.ProgressiveJPEGThresholdBytes)+1))
			test.mutate(r)
			if rec := previewRequest(r, "", ""); rec.Code != test.status {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	r, _ := newPreviewRuntime(t, false, "photo.jpg", []byte("original"))
	req := httptest.NewRequest(http.MethodGet, "/api/share/shares/4/files/41/preview", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "41")
	rec := httptest.NewRecorder()
	r.ServeAuthenticatedShares(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	broken := make([]byte, int(service.ProgressiveJPEGThresholdBytes)+1)
	r, _ = newPreviewRuntime(t, true, "broken.jpg", broken)
	if rec := previewRequest(r, "", ""); rec.Code != 500 {
		t.Fatalf("generation failure status: %d", rec.Code)
	}
}
