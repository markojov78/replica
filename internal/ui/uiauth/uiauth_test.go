package uiauth

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCSRFValidation(t *testing.T) {
	cookies := Cookies{CSRFName: "csrf", Path: "/ui"}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/ui/action", nil)
	req.AddCookie(&http.Cookie{Name: "csrf", Value: "token"})
	if err := ValidateCSRF(req, cookies); err == nil {
		t.Fatal("ValidateCSRF() accepted a request without a token header")
	}
	req.Header.Set("X-CSRF-Token", "token")
	req.Header.Set("Origin", "http://example.com")
	if err := ValidateCSRF(req, cookies); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if err := ValidateCSRF(req, cookies); err == nil {
		t.Fatal("ValidateCSRF() accepted a cross-site request")
	}
}

func TestRefreshGroupCoalescesAndCleansUp(t *testing.T) {
	group := newRefreshGroup(4, 10*time.Millisecond)
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	refresh := func() (TokenPair, error) {
		calls.Add(1)
		close(start)
		<-release
		return TokenPair{AccessToken: "new-access", RefreshToken: "new-refresh"}, nil
	}

	var wg sync.WaitGroup
	results := make([]TokenPair, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = group.Do("rotating-refresh-token", refresh)
		}(i)
	}
	<-start
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	for _, result := range results {
		if result.AccessToken != "new-access" || result.RefreshToken != "new-refresh" {
			t.Fatalf("result = %+v", result)
		}
	}
	time.Sleep(20 * time.Millisecond)
	group.mu.Lock()
	if len(group.inFlight) != 0 {
		t.Fatalf("in-flight entries = %d, want 0", len(group.inFlight))
	}
	group.mu.Unlock()
}

func TestCookiesAreScopedAndHTTPOnly(t *testing.T) {
	cookies := SharedCookies("csrf", "/ui")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://example.com/ui/login", nil)
	cookies.SetAuth(rec, req, TokenPair{AccessToken: "a", RefreshToken: "r", AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(2 * time.Hour)})
	set := rec.Result().Cookies()
	found := 0
	for _, cookie := range set {
		if cookie.Name != AccessCookieName && cookie.Name != RefreshCookieName {
			continue
		}
		found++
		if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
			t.Fatalf("cookie = %+v", cookie)
		}
	}
	if found != 2 {
		t.Fatalf("shared auth cookies = %d, want 2", found)
	}
}

func TestSetAuthClearsLegacyCookiePaths(t *testing.T) {
	cookies := SharedCookies("csrf", "/ui")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/ui/login", nil)
	cookies.SetAuth(rec, req, TokenPair{AccessToken: "a", RefreshToken: "r", AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(2 * time.Hour)})
	want := map[string]string{
		"replica_admin_access":  "/dashboard",
		"replica_admin_refresh": "/dashboard",
		"replica_share_access":  "/share",
		"replica_share_refresh": "/share",
	}
	for _, cookie := range rec.Result().Cookies() {
		if path, ok := want[cookie.Name]; ok && cookie.Path == path && cookie.MaxAge < 0 {
			delete(want, cookie.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("legacy cookies not cleared: %v", want)
	}
}

func TestClearAuthPreservesCSRF(t *testing.T) {
	cookies := Cookies{AccessName: "access", RefreshName: "refresh", CSRFName: "csrf", Path: "/ui"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ui", nil)
	cookies.ClearAuth(rec, req)
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "csrf" {
			t.Fatal("ClearAuth() changed the CSRF cookie")
		}
	}
}
