package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/security"
	"replica/internal/ui/dashboard"
	shareui "replica/internal/ui/share"
	"replica/internal/ui/uiauth"
)

func TestCombinedUIUsesOneSharedBrowserSession(t *testing.T) {
	database := openRouterTestDB(t)
	password, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{Name: "shared-user", Password: password, Status: model.UserStatusActive}).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	auth := newRouterTestAuthService(database)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		user, err := auth.Me(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": user.ID, "username": user.Username, "status": user.Status})
	})
	mux := http.NewServeMux()
	if err := dashboard.Register(mux, api, config.Config{}, auth); err != nil {
		t.Fatalf("dashboard.Register() error = %v", err)
	}
	if err := shareui.Register(mux, nil, auth); err != nil {
		t.Fatalf("share.Register() error = %v", err)
	}

	dashboardCookies := loginDashboardUI(t, mux)
	assertSharedAuthMe(t, mux, "/dashboard/api/auth/me", dashboardCookies)
	assertSharedAuthMe(t, mux, "/share/api/auth/me", dashboardCookies)
	assertClearsSharedAndLegacyCookies(t, logoutUI(t, mux, "/dashboard/logout", "replica_admin_csrf", dashboardCookies))

	shareCookies := loginShareUI(t, mux)
	assertSharedAuthMe(t, mux, "/share/api/auth/me", shareCookies)
	assertSharedAuthMe(t, mux, "/dashboard/api/auth/me", shareCookies)
	assertClearsSharedAndLegacyCookies(t, logoutUI(t, mux, "/share/logout", "replica_share_csrf", shareCookies))
}

func loginDashboardUI(t *testing.T, handler http.Handler) []*http.Cookie {
	t.Helper()
	return loginUI(t, handler, "/dashboard/login", "/dashboard/auth/login", "replica_admin_csrf")
}

func loginShareUI(t *testing.T, handler http.Handler) []*http.Cookie {
	t.Helper()
	return loginUI(t, handler, "/share", "/share/auth/login", "replica_share_csrf")
}

func loginUI(t *testing.T, handler http.Handler, pagePath, loginPath, csrfName string) []*http.Cookie {
	t.Helper()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, pagePath, nil))
	csrf := responseCookie(t, page.Result().Cookies(), csrfName)
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(`{"username":"shared-user","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(csrf)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST %s status/body = %d %s", loginPath, rec.Code, rec.Body.String())
	}
	access := responseCookie(t, rec.Result().Cookies(), uiauth.AccessCookieName)
	refresh := responseCookie(t, rec.Result().Cookies(), uiauth.RefreshCookieName)
	csrf = responseCookie(t, rec.Result().Cookies(), csrfName)
	return []*http.Cookie{access, refresh, csrf}
}

func assertSharedAuthMe(t *testing.T, handler http.Handler, path string, cookies []*http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "shared-user") {
		t.Fatalf("GET %s status/body = %d %s", path, rec.Code, rec.Body.String())
	}
}

func logoutUI(t *testing.T, handler http.Handler, path, csrfName string, cookies []*http.Cookie) []*http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
		if cookie.Name == csrfName {
			req.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s status/body = %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Result().Cookies()
}

func assertClearsSharedAndLegacyCookies(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	want := map[string]string{
		uiauth.AccessCookieName:  "/",
		uiauth.RefreshCookieName: "/",
		"replica_admin_access":   "/dashboard",
		"replica_admin_refresh":  "/dashboard",
		"replica_share_access":   "/share",
		"replica_share_refresh":  "/share",
	}
	for _, cookie := range cookies {
		if path, ok := want[cookie.Name]; ok && cookie.Path == path && cookie.MaxAge < 0 {
			delete(want, cookie.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("cookies not cleared: %v", want)
	}
}
