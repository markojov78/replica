package shareui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		!strings.Contains(rec.Body.String(), `Authorization`) {
		t.Fatalf("share.js body = %s, want HTMX auth header handling", rec.Body.String())
	}
}
