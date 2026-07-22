package uiauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	AccessCookieName  = "replica_access"
	RefreshCookieName = "replica_refresh"
	AuthCookiePath    = "/"
)

var legacyAuthCookies = []struct {
	name string
	path string
}{
	{"replica_admin_access", "/dashboard"},
	{"replica_admin_refresh", "/dashboard"},
	{"replica_share_access", "/share"},
	{"replica_share_refresh", "/share"},
}

type Cookies struct {
	AccessName  string
	RefreshName string
	CSRFName    string
	Path        string
	CSRFPath    string
}

func SharedCookies(csrfName, csrfPath string) Cookies {
	return Cookies{AccessName: AccessCookieName, RefreshName: RefreshCookieName, CSRFName: csrfName, Path: AuthCookiePath, CSRFPath: csrfPath}
}

type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

func (c Cookies) Access(r *http.Request) string  { return cookieValue(r, c.AccessName) }
func (c Cookies) Refresh(r *http.Request) string { return cookieValue(r, c.RefreshName) }

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func (c Cookies) SetAuth(w http.ResponseWriter, r *http.Request, pair TokenPair) {
	c.clearLegacyAuth(w, r)
	setCookie(w, r, c.AccessName, pair.AccessToken, pair.AccessExpiresAt, true, c.Path)
	setCookie(w, r, c.RefreshName, pair.RefreshToken, pair.RefreshExpiresAt, true, c.Path)
}

func (c Cookies) Clear(w http.ResponseWriter, r *http.Request) {
	c.ClearAuth(w, r)
	setCookie(w, r, c.CSRFName, "", time.Unix(0, 0).UTC(), false, c.csrfPath(), -1)
}

func (c Cookies) ClearAuth(w http.ResponseWriter, r *http.Request) {
	for _, cookie := range []struct {
		name     string
		httpOnly bool
	}{
		{c.AccessName, true}, {c.RefreshName, true},
	} {
		setCookie(w, r, cookie.name, "", time.Unix(0, 0).UTC(), cookie.httpOnly, c.Path, -1)
	}
	c.clearLegacyAuth(w, r)
}

func (c Cookies) clearLegacyAuth(w http.ResponseWriter, r *http.Request) {
	for _, cookie := range legacyAuthCookies {
		setCookie(w, r, cookie.name, "", time.Unix(0, 0).UTC(), true, cookie.path, -1)
	}
}

func (c Cookies) csrfPath() string {
	if c.CSRFPath != "" {
		return c.CSRFPath
	}
	return c.Path
}

func (c Cookies) EnsureCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	if token := cookieValue(r, c.CSRFName); token != "" {
		return token, nil
	}
	return c.RotateCSRF(w, r)
}

func (c Cookies) RotateCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	setCookie(w, r, c.CSRFName, token, time.Now().UTC().Add(24*time.Hour), false, c.csrfPath())
	return token, nil
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, expires time.Time, httpOnly bool, path string, maxAge ...int) {
	cookie := &http.Cookie{Name: name, Value: value, Path: path, Expires: expires, HttpOnly: httpOnly, Secure: requestHTTPS(r), SameSite: http.SameSiteLaxMode}
	if len(maxAge) > 0 {
		cookie.MaxAge = maxAge[0]
	}
	http.SetCookie(w, cookie)
}

func requestHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func ValidateCSRF(r *http.Request, cookies Cookies) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return errors.New("cross-site request rejected")
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		expectedScheme := "http"
		if requestHTTPS(r) {
			expectedScheme = "https"
		}
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) || !strings.EqualFold(parsed.Scheme, expectedScheme) {
			return errors.New("invalid request origin")
		}
	}
	want := cookieValue(r, cookies.CSRFName)
	got := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if got == "" {
		got = strings.TrimSpace(r.FormValue("csrf_token"))
	}
	if want == "" || got == "" || len(want) != len(got) || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return errors.New("invalid CSRF token")
	}
	return nil
}

type refreshCall struct {
	done chan struct{}
	pair TokenPair
	err  error
}

type RefreshGroup struct {
	mu        sync.Mutex
	inFlight  map[[32]byte]*refreshCall
	max       int
	resultTTL time.Duration
}

var sharedUserRefreshes = NewRefreshGroup(256)

func SharedUserRefreshes() *RefreshGroup { return sharedUserRefreshes }

func NewRefreshGroup(max int) *RefreshGroup {
	return newRefreshGroup(max, 2*time.Second)
}

func newRefreshGroup(max int, resultTTL time.Duration) *RefreshGroup {
	if max < 1 {
		max = 128
	}
	return &RefreshGroup{inFlight: make(map[[32]byte]*refreshCall), max: max, resultTTL: resultTTL}
}

func (g *RefreshGroup) Do(refreshToken string, fn func() (TokenPair, error)) (TokenPair, error) {
	key := sha256.Sum256([]byte(refreshToken))
	g.mu.Lock()
	if call := g.inFlight[key]; call != nil {
		g.mu.Unlock()
		<-call.done
		return call.pair, call.err
	}
	if len(g.inFlight) >= g.max {
		g.mu.Unlock()
		return TokenPair{}, errors.New("too many concurrent token refreshes")
	}
	call := &refreshCall{done: make(chan struct{})}
	g.inFlight[key] = call
	g.mu.Unlock()

	call.pair, call.err = fn()
	close(call.done)
	time.AfterFunc(g.resultTTL, func() {
		g.mu.Lock()
		if g.inFlight[key] == call {
			delete(g.inFlight, key)
		}
		g.mu.Unlock()
	})
	return call.pair, call.err
}
