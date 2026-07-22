package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/security"
	"replica/internal/ui/uiauth"
)

func TestDashboardBFFCookieLoginMeLogoutAndCSRF(t *testing.T) {
	database := openRouterTestDB(t)
	password, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{Name: "admin", Password: password, Status: model.UserStatusActive}).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	auth := newRouterTestAuthService(database)
	handler := New(config.Config{App: config.AppConfig{Coordinator: true, NodeID: "coordinator"}}, buildinfo.Info{}, auth, nil, nil, nil, nil, nil, nil, nil)

	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/dashboard/login", nil))
	csrf := responseCookie(t, loginPage.Result().Cookies(), "replica_admin_csrf")
	probe := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/auth/me", nil)
	req.AddCookie(csrf)
	handler.ServeHTTP(probe, req)
	if probe.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated me status = %d, want %d", probe.Code, http.StatusUnauthorized)
	}
	for _, cookie := range probe.Result().Cookies() {
		if cookie.Name == "replica_admin_csrf" && cookie.MaxAge < 0 {
			t.Fatal("unauthenticated me cleared the login CSRF cookie")
		}
	}

	missingCSRF := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/dashboard/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(missingCSRF, req)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("login without CSRF status = %d, want %d", missingCSRF.Code, http.StatusForbidden)
	}

	login := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/dashboard/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(csrf)
	handler.ServeHTTP(login, req)
	if login.Code != http.StatusNoContent || strings.Contains(login.Body.String(), "access_token") || strings.Contains(login.Body.String(), "refresh_token") {
		t.Fatalf("login status/body = %d %q", login.Code, login.Body.String())
	}
	access := responseCookie(t, login.Result().Cookies(), uiauth.AccessCookieName)
	refresh := responseCookie(t, login.Result().Cookies(), uiauth.RefreshCookieName)
	csrf = responseCookie(t, login.Result().Cookies(), "replica_admin_csrf")
	if !access.HttpOnly || !refresh.HttpOnly || access.Path != "/" || refresh.Path != "/" {
		t.Fatalf("auth cookies = %+v %+v", access, refresh)
	}

	me := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/dashboard/api/auth/me", nil)
	req.AddCookie(access)
	req.AddCookie(refresh)
	handler.ServeHTTP(me, req)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"username":"admin"`) {
		t.Fatalf("me status/body = %d %s", me.Code, me.Body.String())
	}

	logout := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/dashboard/logout", nil)
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(access)
	req.AddCookie(refresh)
	req.AddCookie(csrf)
	handler.ServeHTTP(logout, req)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d body=%s", logout.Code, logout.Body.String())
	}
	for _, name := range []string{uiauth.AccessCookieName, uiauth.RefreshCookieName, "replica_admin_csrf"} {
		if cookie := responseCookie(t, logout.Result().Cookies(), name); cookie.MaxAge >= 0 {
			t.Fatalf("cleared cookie %s MaxAge = %d", name, cookie.MaxAge)
		}
	}
	if _, err := auth.Refresh(refresh.Value); err == nil {
		t.Fatal("Refresh() succeeded after dashboard logout")
	}
}

func responseCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}
