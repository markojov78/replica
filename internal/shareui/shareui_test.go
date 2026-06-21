package shareui

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
)

func TestRegisterServesLoginAndStaticAssets(t *testing.T) {
	mux := http.NewServeMux()
	if err := Register(mux, nil); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /share status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `data-share-login-form`) ||
		!strings.Contains(rec.Body.String(), `/api/share/auth/login`) {
		t.Fatalf("GET /share body = %s, want sharing login form", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/static/share.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET static status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `htmx:configRequest`) ||
		!strings.Contains(rec.Body.String(), `Authorization`) ||
		!strings.Contains(rec.Body.String(), `thumbnail_size`) {
		t.Fatalf("share.js body = %s, want HTMX auth header handling", rec.Body.String())
	}
}

func TestShareFileTemplateRendersListAndGridModes(t *testing.T) {
	file := fileView{
		ReplicaInventoryFile: apiclient.ReplicaInventoryFile{
			FileID:           10,
			RelativeURI:      "album/photo.jpg",
			Size:             12345,
			InventoryVersion: 4,
			Modified:         time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC),
		},
		Name:         "photo.jpg",
		Type:         "Image (JPG)",
		DownloadPath: "/api/share/shares/4/files/10/content",
	}

	list := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       "list",
	})
	if !strings.Contains(list, "<table>") || strings.Contains(list, `class="file-grid"`) {
		t.Fatalf("list view = %s, want table and no grid", list)
	}

	file.ThumbnailURL = "/s/public-link/files/10/thumbnail?size=256"
	file.DownloadPath = "/s/public-link/files/10/content"
	grid := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Public:         true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read", "update", "delete"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/w/public-link",
		APIBasePath:    "/s/public-link",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  256,
		ViewMode:       "grid",
	})
	for _, want := range []string{
		`class="file-grid"`,
		`src="/s/public-link/files/10/thumbnail?size=256"`,
		`href="/s/public-link/files/10/content"`,
		`Replace`,
		`Delete`,
	} {
		if !strings.Contains(grid, want) {
			t.Fatalf("grid view = %s, want %q", grid, want)
		}
	}
}

func TestAuthenticatedGridUsesAPIThumbnailDataSource(t *testing.T) {
	html := renderShareTemplate(t, pageData{
		Title:         "Photos",
		Authenticated: true,
		Share:         apiclient.Share{ID: 4, Name: "Photos"},
		Files: []fileView{{
			ReplicaInventoryFile: apiclient.ReplicaInventoryFile{FileID: 10, RelativeURI: "photo.jpg", Size: 100, InventoryVersion: 4},
			Name:                 "photo.jpg",
			Type:                 "Image (JPG)",
			DownloadPath:         "/api/share/shares/4/files/10/content",
		}},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 512},
		ThumbnailSize:  512,
		ViewMode:       "grid",
	})
	if !strings.Contains(html, `data-auth-src="/api/share/shares/4/files/10/thumbnail?size=512"`) {
		t.Fatalf("authenticated grid = %s, want API thumbnail data source with selected size", html)
	}
	if strings.Contains(html, `Delete`) || strings.Contains(html, `Replace`) {
		t.Fatalf("authenticated read-only grid = %s, want no update/delete controls", html)
	}
}

func renderShareTemplate(t *testing.T, data pageData) string {
	t.Helper()
	pages, err := template.New("shareui-test").Funcs(templateFuncs()).ParseFS(assets, "templates/*.html")
	if err != nil {
		t.Fatalf("ParseFS() error = %v", err)
	}
	var out bytes.Buffer
	if err := pages.ExecuteTemplate(&out, "share_files", data); err != nil {
		t.Fatalf("ExecuteTemplate(share_files) error = %v", err)
	}
	return out.String()
}
