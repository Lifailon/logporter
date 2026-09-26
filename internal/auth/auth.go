package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed auth.tmpl
var templateFS embed.FS

const (
	cookieName       = "lp_session"
	loginURL         = "/login"
	logoutURL        = "/logout"
	persistentMaxAge = 31536000
)

var (
	stateMu sync.RWMutex
	enabled bool
	user    string
	pass    string
	secret  []byte
	ttl     time.Duration
)

// Reads environment variables and returns information on whether authorization is enabled
func Setup() bool {
	u := os.Getenv("DASHBOARD_USERNAME")
	p := os.Getenv("DASHBOARD_PASSWORD")
	ttlEnv := strings.TrimSpace(os.Getenv("DASHBOARD_AUTH_TTL"))

	stateMu.Lock()
	defer stateMu.Unlock()
	user, pass = u, p
	enabled = u != "" && p != ""
	ttl = 0
	if ttlEnv != "" {
		if n, err := strconv.Atoi(ttlEnv); err == nil && n > 0 {
			ttl = time.Duration(n) * time.Second
		}
	}
	secret = nil
	if enabled {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			secret = fmt.Appendf(nil, "lp-secret-%d", time.Now().UnixNano())
		}
	}
	return enabled
}

// Check that authorization is active
func Enabled() bool {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return enabled
}

// TTL (Time to Live) session
func TTL() time.Duration {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return ttl
}

// Generates a token signed using HMAC-SHA256
func sessionToken(u string) string {
	now := time.Now().Unix()
	var exp int64
	stateMu.RLock()
	st := ttl
	stateMu.RUnlock()
	if st > 0 {
		exp = now + int64(st.Seconds())
	}
	payload := fmt.Sprintf("%s|%d|%d", u, now, exp)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// Checks whether the token is valid for the configured user and has not expired
func sessionValid(tok string) bool {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return false
	}
	parts := strings.Split(string(b), "|")
	if len(parts) != 4 {
		return false
	}
	u, nowS, expS, sig := parts[0], parts[1], parts[2], parts[3]
	_, err1 := strconv.ParseInt(nowS, 10, 64)
	exp, err2 := strconv.ParseInt(expS, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	if exp > 0 && time.Now().Unix() > exp {
		return false
	}
	stateMu.RLock()
	cfgUser, cfgSecret := user, secret
	stateMu.RUnlock()
	if subtle.ConstantTimeCompare([]byte(u), []byte(cfgUser)) != 1 {
		return false
	}
	mac := hmac.New(sha256.New, cfgSecret)
	mac.Write([]byte(strings.Join(parts[:3], "|")))
	want := fmt.Sprintf("%x", mac.Sum(nil))
	return len(want) == len(sig) &&
		subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

// Checks whether the request contains a valid session cookie
func authorize(r *http.Request) bool {
	if !Enabled() {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return sessionValid(c.Value)
}

var loginPage = template.Must(template.ParseFS(templateFS, "auth.tmpl"))

// Renders the standalone login page
func writeLoginPage(w http.ResponseWriter, errMsg string, status int, logger *slog.Logger) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(status)
	if err := loginPage.Execute(w, struct{ Error string }{errMsg}); err != nil {
		logger.Error("login page render", "error", err)
	}
}

// Login handler
func handleLogin(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	if r.Method != http.MethodPost {
		writeLoginPage(w, "", http.StatusOK, logger)
		return
	}
	u := r.PostFormValue("username")
	p := r.PostFormValue("password")
	stateMu.RLock()
	ok := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
	stateMu.RUnlock()
	if !ok {
		writeLoginPage(w, "Invalid username or password", http.StatusUnauthorized, logger)
		return
	}
	maxAge := persistentMaxAge
	stateMu.RLock()
	st := ttl
	stateMu.RUnlock()
	if st > 0 {
		maxAge = int(st.Seconds())
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessionToken(u),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
	logger.Info("auth: user logged in", "user", u)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout handler
func handleLogout(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, loginURL, http.StatusFound)
}

// Processing authorization requests
func Middleware(next http.Handler, logger *slog.Logger) http.Handler {
	if !Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case loginURL:
			handleLogin(w, r, logger)
			return
		case logoutURL:
			handleLogout(w, r, logger)
			return
		case "/health", "/metrics", "/favicon.ico":
			next.ServeHTTP(w, r)
			return
		}
		if authorize(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		writeLoginPage(w, "", http.StatusUnauthorized, logger)
	})
}
