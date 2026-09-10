package storage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"replica/internal/service"
)

func publicPreviewRuntime(t *testing.T, enabled bool, name string, data []byte) (*Runtime, string) {
	t.Helper()
	r, path := newPreviewRuntime(t, enabled, name, data)
	link := "public-link"
	r.shares[0].LinkHash = &link
	r.shares[0].AnonymousPermissions = []string{"read"}
	return r, path
}

func publicFileRequest(r *Runtime, endpoint, query, rangeHeader, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/s/public-link/files/41/"+endpoint+query, nil)
	req.SetPathValue("link_hash", "public-link")
	req.SetPathValue("file_id", "41")
	req.Header.Set("Range", rangeHeader)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	r.ServePublicShares(rec, req)
	return rec
}

func TestPublicPreviewMatchesAuthenticatedPreviewAndReusesCache(t *testing.T) {
	data := previewJPEG(t, int(service.ProgressiveJPEGThresholdBytes)+1)
	r, source := publicPreviewRuntime(t, true, "photo.jpg", data)
	generated := publicFileRequest(r, "preview", "", "", "")
	if generated.Code != http.StatusOK || !bytes.Contains(generated.Body.Bytes(), []byte{0xff, 0xc2}) {
		t.Fatalf("public generation = %d", generated.Code)
	}
	auth := previewRequest(r, "", "")
	if auth.Code != 200 {
		t.Fatalf("authenticated preview = %d", auth.Code)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	public := publicFileRequest(r, "preview", "", "", "")
	if public.Code != 200 || !bytes.Equal(public.Body.Bytes(), auth.Body.Bytes()) {
		t.Fatalf("public preview = %d", public.Code)
	}
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Disposition", "ETag", "Cache-Control", "Accept-Ranges"} {
		if public.Header().Get(header) != auth.Header().Get(header) {
			t.Fatalf("%s differs", header)
		}
	}
	partial := publicFileRequest(r, "preview", "", "bytes=2-9", "")
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), auth.Body.Bytes()[2:10]) {
		t.Fatalf("partial preview = %d", partial.Code)
	}
	if rec := publicFileRequest(r, "preview", "", "", public.Header().Get("ETag")); rec.Code != 304 {
		t.Fatalf("conditional preview = %d", rec.Code)
	}
	if rec := publicFileRequest(r, "preview", "", "bytes=bad", ""); rec.Code != 400 {
		t.Fatalf("malformed range = %d", rec.Code)
	}
	if rec := publicFileRequest(r, "preview", "", "bytes=999999-", ""); rec.Code != 416 {
		t.Fatalf("unsatisfiable range = %d", rec.Code)
	}
}

func TestPublicPreviewOriginalFallback(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		size    int
	}{
		{"disabled.jpg", false, int(service.ProgressiveJPEGThresholdBytes) + 1},
		{"small.jpg", true, int(service.ProgressiveJPEGThresholdBytes)},
		{"video.mp4", true, int(service.ProgressiveJPEGThresholdBytes) + 1},
		{"image.webp", true, int(service.ProgressiveJPEGThresholdBytes) + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := make([]byte, test.size)
			copy(data, "original bytes")
			r, _ := publicPreviewRuntime(t, test.enabled, test.name, data)
			rec := publicFileRequest(r, "preview", "", "", "")
			if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || rec.Header().Get("ETag") != `"file-41-v24-original"` {
				t.Fatalf("original preview = %d %v", rec.Code, rec.Header())
			}
		})
	}
}

func TestPublicContentDownloadParameter(t *testing.T) {
	data := []byte("original bytes")
	r, _ := publicPreviewRuntime(t, true, "photo.jpg", data)
	for _, test := range []struct {
		query, disposition string
		status             int
	}{
		{"", "inline", 200}, {"?download=false", "inline", 200}, {"?download=true", "attachment", 200},
		{"?download", "", 400}, {"?download=", "", 400}, {"?download=TRUE", "", 400}, {"?download=1", "", 400},
		{"?download=0", "", 400}, {"?download=yes", "", 400}, {"?download=%20true", "", 400}, {"?download=%zz", "", 400},
		{"?download=true&download=false", "", 400}, {"?download=true&download=true", "", 400},
	} {
		t.Run(test.query, func(t *testing.T) {
			rec := publicFileRequest(r, "content", test.query, "", "")
			if rec.Code != test.status {
				t.Fatalf("status=%d, want %d", rec.Code, test.status)
			}
			if test.status == 400 {
				return
			}
			if !bytes.Equal(rec.Body.Bytes(), data) || rec.Header().Get("Content-Disposition") != test.disposition+`; filename="photo.jpg"` || rec.Header().Get("ETag") != `"file-41-v24"` {
				t.Fatalf("content changed: %v", rec.Header())
			}
		})
	}
	rec := publicFileRequest(r, "content", "?download=true", "bytes=0-7", "")
	if rec.Code != 206 || string(rec.Body.Bytes()) != "original" || rec.Header().Get("Content-Disposition") != `attachment; filename="photo.jpg"` {
		t.Fatalf("download range = %d %v", rec.Code, rec.Header())
	}
}

func TestPublicPreviewAndContentEnforceAccessAndState(t *testing.T) {
	for _, endpoint := range []string{"preview", "content"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(*Runtime)
				status int
			}{
				{"no read", func(r *Runtime) { r.shares[0].AnonymousPermissions = nil }, 403},
				{"wrong link", func(r *Runtime) { link := "different"; r.shares[0].LinkHash = &link }, 404},
				{"inactive share", func(r *Runtime) { r.shares[0].Status = "inactive" }, 404},
				{"expired", func(r *Runtime) { expired := time.Now().Add(-time.Hour); r.shares[0].ShareExpiration = &expired }, 404},
				{"inactive replica", func(r *Runtime) { r.replicas[0].Status = "inactive" }, 404},
				{"wrong inventory", func(r *Runtime) { r.replicaFiles[3][0].InventoryID = 99 }, 404},
				{"inactive file", func(r *Runtime) { r.replicaFiles[3][0].InventoryStatus = "deleted" }, 404},
				{"pending file", func(r *Runtime) { r.replicaFiles[3][0].ReplicaStatus = "pending" }, 409},
			} {
				t.Run(test.name, func(t *testing.T) {
					r, _ := publicPreviewRuntime(t, false, "file.txt", []byte("original"))
					test.mutate(r)
					if rec := publicFileRequest(r, endpoint, "", "", ""); rec.Code != test.status {
						t.Fatalf("status = %d, want %d; %s", rec.Code, test.status, rec.Body.String())
					}
				})
			}
		})
	}
}
