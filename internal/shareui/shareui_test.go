package shareui

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
	"replica/internal/storage"
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
		!strings.Contains(rec.Body.String(), `/share/auth/login`) {
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
		ContentPath:  "/share/shares/4/files/10/content",
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
	if !strings.Contains(list, `href="/share/shares/4/files/10/content"`) {
		t.Fatalf("list view = %s, want authenticated content link", list)
	}

	file.ThumbnailURL = "/s/public-link/files/10/thumbnail?size=256"
	file.ContentPath = "/w/public-link/files/10/content"
	file.DownloadPath = "/w/public-link/files/10/content"
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
		`href="/w/public-link/files/10/content"`,
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
			ContentPath:          "/share/shares/4/files/10/content",
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

func TestAuthenticatedUIContentRouteRequiresCookieAuth(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/share/shares/4/files/10/content", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "10")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET authenticated content without cookie status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
	}
}

func TestShareUILoginSetsHttpOnlyAuthCookies(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/share/auth/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST login status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	cookies := rec.Result().Cookies()
	var accessCookie, refreshCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case shareUIAccessCookie:
			accessCookie = cookie
		case shareUIRefreshCookie:
			refreshCookie = cookie
		}
	}
	if accessCookie == nil || refreshCookie == nil {
		t.Fatalf("cookies = %+v, want access and refresh cookies", cookies)
	}
	if !accessCookie.HttpOnly || !refreshCookie.HttpOnly || accessCookie.Path != "/share" || refreshCookie.Path != "/share" {
		t.Fatalf("cookies = %+v, want HttpOnly cookies scoped to /share", cookies)
	}
}

func TestShareUIDeletePostRedirectsToCanonicalSharePageAndGetStaysMethodNotAllowed(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	form := url.Values{
		"version": {"2"},
		"page":    {"3"},
		"count":   {"40"},
		"thumb":   {"256"},
		"view":    {"grid"},
	}
	req := httptest.NewRequest(http.MethodPost, "/share/shares/5/files/216/delete", strings.NewReader(form.Encode()))
	req.AddCookie(&http.Cookie{Name: shareUIAccessCookie, Value: "user-token", Path: "/share"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusSeeOther)
	}
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q parse error = %v", location, err)
	}
	if parsed.Path != "/share/shares/5" {
		t.Fatalf("Location path = %q, want /share/shares/5", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"page": "3", "count": "40", "thumb": "256", "view": "grid"} {
		if got := query.Get(key); got != want {
			t.Fatalf("Location %s = %q in %q, want %q", key, got, location, want)
		}
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/shares/5/files/216/delete", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusMethodNotAllowed)
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

func newShareUIRuntime(t *testing.T) *storage.Runtime {
	t.Helper()
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                  15,
				"access_token":             "user-access-token",
				"refresh_token":            "user-refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/validate-user-token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 15,
				"username":                "jsmith",
				"status":                  "active",
				"access_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		default:
			t.Fatalf("unexpected coordinator path %q", r.URL.Path)
		}
	}))
	t.Cleanup(coordinator.Close)

	runtime, err := storage.NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    coordinator.URL,
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
