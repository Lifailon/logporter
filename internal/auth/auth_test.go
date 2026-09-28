package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func withEnv(t *testing.T, env map[string]string, fn func()) {
	t.Helper()
	prev := make(map[string]string)
	for k, v := range env {
		prev[k] = os.Getenv(k)
		if err := os.Setenv(k, v); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for k := range env {
			if prev[k] == "" {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, prev[k])
			}
		}
	}()
	fn()
}

func TestDisabledByDefault(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "",
		"DASHBOARD_PASSWORD": "",
		"DASHBOARD_AUTH_TTL": "",
	}, func() {
		if Setup() {
			t.Fatal("auth must be disabled when credentials are not set")
		}
		if Enabled() {
			t.Fatal("Enabled() must be false")
		}
		if TTL() != 0 {
			t.Fatalf("TTL must be 0 when cache is unset, got %v", TTL())
		}
	})
}

func TestTTLZeroMeansNoExpiry(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
		"DASHBOARD_AUTH_TTL": "0",
	}, func() {
		if !Setup() {
			t.Fatal("auth must be enabled when credentials are set")
		}
		if TTL() != 0 {
			t.Fatalf("TTL must be 0 (session never expires) when DASHBOARD_AUTH_TTL=0")
		}
	})
}

func TestTTLSet(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
		"DASHBOARD_AUTH_TTL": "3600",
	}, func() {
		if !Setup() {
			t.Fatal("auth must be enabled when credentials are set")
		}
		if got := TTL(); got != time.Hour {
			t.Fatalf("TTL must be 1h, got %v", got)
		}
	})
}

func TestTokenRoundTrip(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		tok := sessionToken("admin")
		if !sessionValid(tok) {
			t.Fatal("freshly issued token must validate")
		}
		if sessionValid(tok[:len(tok)-2] + "x") {
			t.Fatal("tampered token must not validate")
		}
		if sessionValid(sessionToken("other")) {
			t.Fatal("token for another user must not validate")
		}
	})
}

func TestTokenExpired(t *testing.T) {
	stateMu.Lock()
	secret = []byte("fixed-test-secret")
	stateMu.Unlock()
	now := time.Now().Unix()
	payload := fmt.Sprintf("admin|%d|%d", now, now-1)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	tok := base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + sig))
	if sessionValid(tok) {
		t.Fatal("expired token must not validate")
	}
}

func TestMiddlewareRedirectsToLogin(t *testing.T) {
	var muxCalled bool
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			muxCalled = true
			_, _ = w.Write([]byte("dashboard"))
		})
		mw := Middleware(next, newTestLogger())

		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if muxCalled {
			t.Fatal("protected route must not reach the handler without a session")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status must be 401, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Sign in") {
			t.Fatal("login page expected in response body")
		}

		login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=admin"))
		login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		lrec := httptest.NewRecorder()
		mw.ServeHTTP(lrec, login)
		if lrec.Code != http.StatusFound {
			t.Fatalf("login must redirect, got %d", lrec.Code)
		}
		cookies := lrec.Result().Cookies()
		if len(cookies) == 0 || cookies[0].Name != cookieName {
			t.Fatal("session cookie must be set on login")
		}

		req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		req2.AddCookie(&http.Cookie{Name: cookieName, Value: cookies[0].Value})
		rec2 := httptest.NewRecorder()
		mw.ServeHTTP(rec2, req2)
		if !muxCalled {
			t.Fatal("handler must be reached with a valid session cookie")
		}

		bad := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=nope"))
		bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		brec := httptest.NewRecorder()
		mw.ServeHTTP(brec, bad)
		if brec.Code != http.StatusUnauthorized {
			t.Fatalf("bad login must return 401, got %d", brec.Code)
		}
		if !strings.Contains(brec.Body.String(), "Invalid username or password") {
			t.Fatal("error message expected on bad login page")
		}
	})
}

func TestMiddlewareAllowsHealthAndMetrics(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		var hit string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit = r.URL.Path
			_, _ = w.Write([]byte("ok"))
		})
		mw := Middleware(next, newTestLogger())
		for _, path := range []string{"/health", "/metrics"} {
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if hit != path {
				t.Fatalf("%s must reach the handler without a session", path)
			}
		}
	})
}

func TestMiddlewareJSON401ForAPI(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		mw := Middleware(next, newTestLogger())
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard/refresh", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("API must return 401, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "unauthorized") {
			t.Fatal("JSON error payload expected")
		}
	})
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAuthorizeAllowedWhenDisabled(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "",
		"DASHBOARD_PASSWORD": "",
	}, func() {
		Setup()
		if !authorize(&http.Request{}) {
			t.Fatal("authorize must allow every request when auth is disabled")
		}
	})
}

func TestSetupInvalidTTL(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
		"DASHBOARD_AUTH_TTL": "abc",
	}, func() {
		if !Setup() {
			t.Fatal("auth must be enabled when credentials are set")
		}
		if TTL() != 0 {
			t.Fatalf("an unparseable TTL must default to 0, got %v", TTL())
		}
	})
}

func TestTokenRoundTripWithTTL(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
		"DASHBOARD_AUTH_TTL": "3600",
	}, func() {
		Setup()
		tok := sessionToken("admin")
		if !sessionValid(tok) {
			t.Fatal("freshly issued token with an expiry must validate")
		}
	})
}

func TestSessionValidMalformed(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		if sessionValid("%%%not-base64%%%") {
			t.Fatal("non-base64 token must not validate")
		}
		badParts := base64.RawURLEncoding.EncodeToString([]byte("admin|123|45"))
		if sessionValid(badParts) {
			t.Fatal("token with a wrong number of parts must not validate")
		}
		badNow := base64.RawURLEncoding.EncodeToString([]byte("admin|zzz|45|deadbeef"))
		if sessionValid(badNow) {
			t.Fatal("token with an unparseable timestamp must not validate")
		}
		badExp := base64.RawURLEncoding.EncodeToString([]byte("admin|10|xyz|deadbeef"))
		if sessionValid(badExp) {
			t.Fatal("token with an unparseable expiry must not validate")
		}
	})
}

func TestMiddlewareDisabledPassesThrough(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "",
		"DASHBOARD_PASSWORD": "",
	}, func() {
		Setup()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		mw := Middleware(next, newTestLogger())
		for _, path := range []string{"/dashboard", "/login", "/api/x", "/health"} {
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
				t.Fatalf("auth-disabled middleware must pass %s through, got %d", path, rec.Code)
			}
		}
	})
}

func TestMiddlewareFaviconAllowed(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		var hit string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit = r.URL.Path
			_, _ = w.Write([]byte("ico"))
		})
		mw := Middleware(next, newTestLogger())
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
		if hit != "/favicon.ico" {
			t.Fatal("favicon must reach the handler without a session")
		}
	})
}

func TestMiddlewareLogout(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		var hit bool
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
		mw := Middleware(next, newTestLogger())
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logout", nil))
		if hit {
			t.Fatal("logout must not reach the inner handler")
		}
		if rec.Code != http.StatusFound {
			t.Fatalf("logout must redirect, got %d", rec.Code)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 || cookies[0].Name != cookieName || cookies[0].MaxAge != -1 {
			t.Fatal("logout must clear the session cookie")
		}
	})
}

func TestLoginGetShowsPage(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
	}, func() {
		Setup()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		mw := Middleware(next, newTestLogger())
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /login must render the page, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Sign in") {
			t.Fatal("login page expected in response body")
		}
	})
}

func TestLoginSetsTTLMaxAge(t *testing.T) {
	withEnv(t, map[string]string{
		"DASHBOARD_USERNAME": "admin",
		"DASHBOARD_PASSWORD": "admin",
		"DASHBOARD_AUTH_TTL": "3600",
	}, func() {
		Setup()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		mw := Middleware(next, newTestLogger())
		login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=admin"))
		login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, login)
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 || cookies[0].MaxAge != 3600 {
			t.Fatalf("cookie MaxAge must match the TTL, got %+v", cookies)
		}
	})
}
