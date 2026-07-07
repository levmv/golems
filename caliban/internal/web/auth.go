package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/caliban/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName    = "caliban_session"
	defaultSessionTTL    = 30 * 24 * time.Hour
	minPasswordRunes     = 8
	loginAttemptWindow   = 15 * time.Minute
	maxLoginAttempts     = 8
	sessionTouchInterval = time.Minute
)

// AuthConfig enables the single-user web auth layer.
type AuthConfig struct {
	Enabled    bool
	SessionTTL time.Duration
}

func (c AuthConfig) withDefaults() AuthConfig {
	if c.SessionTTL <= 0 {
		c.SessionTTL = defaultSessionTTL
	}
	return c
}

type authStore interface {
	WebAuthPasswordHash(ctx context.Context) (string, bool, error)
	CreateWebSession(ctx context.Context, sess store.WebSession) error
	WebSession(ctx context.Context, tokenHash string) (store.WebSession, bool, error)
	TouchWebSession(ctx context.Context, tokenHash string, at time.Time) error
	DeleteWebSession(ctx context.Context, tokenHash string) (bool, error)
	DeleteExpiredWebSessions(ctx context.Context, at time.Time) (int64, error)
}

type loginRequest struct {
	Password string `json:"password"`
}

type loginAttempt struct {
	Count   int
	ResetAt time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func HashPassword(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(strings.TrimSpace(password)) < minPasswordRunes {
		return fmt.Errorf("password must be at least %d characters", minPasswordRunes)
	}
	return nil
}

func (t *Transport) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if !t.auth.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if isPublicAuthRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !t.requestAuthenticated(r) {
			t.unauthorized(w, r)
			return
		}
		if !sameOriginForUnsafeMethod(r) {
			writeError(w, http.StatusForbidden, "invalid request origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
}

func isPublicAuthRoute(r *http.Request) bool {
	return (r.Method == http.MethodGet && r.URL.Path == "/login") ||
		(r.Method == http.MethodPost && r.URL.Path == "/api/auth/login")
}

func (t *Transport) loginPage(w http.ResponseWriter, r *http.Request) {
	if !t.auth.Enabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if t.requestAuthenticated(r) {
		http.Redirect(w, r, safeNextPath(r), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginHTML))
}

func (t *Transport) login(w http.ResponseWriter, r *http.Request) {
	if !t.auth.Enabled {
		writeError(w, http.StatusNotFound, "web auth is not enabled")
		return
	}
	key := clientKey(r)
	now := time.Now().UTC()
	if !t.loginLimiter.Allow(key, now) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	auth, ok := t.authStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "web auth storage is not configured")
		return
	}
	hash, ok, err := auth.WebAuthPasswordHash(r.Context())
	if err != nil {
		t.logf("web: read auth password: %v", err)
		writeError(w, http.StatusInternalServerError, "could not verify password")
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "web password is not set")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		t.loginLimiter.RecordFailure(key, now)
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		t.logf("web: create session token: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	expiresAt := now.Add(t.auth.SessionTTL)
	_, _ = auth.DeleteExpiredWebSessions(r.Context(), now)
	if err := auth.CreateWebSession(r.Context(), store.WebSession{
		TokenHash:  tokenHash,
		UserAgent:  r.UserAgent(),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}); err != nil {
		t.logf("web: create session: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	t.loginLimiter.Clear(key)
	setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (t *Transport) logout(w http.ResponseWriter, r *http.Request) {
	if auth, ok := t.authStore(); ok {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			_, _ = auth.DeleteWebSession(r.Context(), sessionTokenHash(cookie.Value))
		}
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (t *Transport) requestAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	auth, ok := t.authStore()
	if !ok {
		return false
	}
	tokenHash := sessionTokenHash(cookie.Value)
	sess, ok, err := auth.WebSession(r.Context(), tokenHash)
	if err != nil {
		t.logf("web: read session: %v", err)
		return false
	}
	if !ok {
		return false
	}
	now := time.Now().UTC()
	if !sess.ExpiresAt.After(now) {
		_, _ = auth.DeleteWebSession(r.Context(), tokenHash)
		return false
	}
	if now.Sub(sess.LastSeenAt) >= sessionTouchInterval {
		if err := auth.TouchWebSession(r.Context(), tokenHash, now); err != nil {
			t.logf("web: touch session: %v", err)
		}
	}
	return true
}

func (t *Transport) authStore() (authStore, bool) {
	if t.store == nil {
		return nil, false
	}
	auth, ok := t.store.(authStore)
	return auth, ok
}

func (t *Transport) unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if wantsHTML(r) {
		next := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+next, http.StatusFound)
		return
	}
	writeError(w, http.StatusUnauthorized, "authentication required")
}

func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/html") {
		return true
	}
	path := strings.Trim(r.URL.Path, "/")
	return accept == "" && (path == "" || !strings.Contains(path, "."))
}

func sameOriginForUnsafeMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		// Non-browser clients usually omit both headers. For browsers, this
		// check is paired with SameSite=Lax cookies; modern browsers send
		// Origin on unsafe cross-site requests.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	scheme := requestScheme(r)
	host := requestHost(r)
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, host)
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https") {
		return "https"
	}
	for _, part := range strings.Split(r.Header.Get("Forwarded"), ";") {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "proto=https") {
			return "https"
		}
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if host := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	return r.Host
}

func firstForwardedValue(value string) string {
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

func requestIsHTTPS(r *http.Request) bool {
	return requestScheme(r) == "https"
}

func safeNextPath(r *http.Request) string {
	next := strings.TrimSpace(r.URL.Query().Get("next"))
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func newSessionToken() (token string, tokenHash string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw[:])
	return token, sessionTokenHash(token), nil
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (l *loginLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]loginAttempt)
	}
	a := l.attempts[key]
	if a.ResetAt.IsZero() || !now.Before(a.ResetAt) {
		delete(l.attempts, key)
		return true
	}
	return a.Count < maxLoginAttempts
}

func (l *loginLimiter) RecordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]loginAttempt)
	}
	a := l.attempts[key]
	if a.ResetAt.IsZero() || !now.Before(a.ResetAt) {
		a = loginAttempt{ResetAt: now.Add(loginAttemptWindow)}
	}
	a.Count++
	l.attempts[key] = a
}

func (l *loginLimiter) Clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

const loginHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Caliban Login</title>
    <style>
      :root {
        color-scheme: light dark;
        font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        background: #f4f5f7;
        color: #111827;
      }
      body {
        min-height: 100vh;
        margin: 0;
        display: grid;
        place-items: center;
      }
      main {
        width: min(360px, calc(100vw - 32px));
      }
      h1 {
        margin: 0 0 18px;
        font-size: 24px;
        font-weight: 650;
        letter-spacing: 0;
      }
      form {
        display: grid;
        gap: 12px;
      }
      input, button {
        box-sizing: border-box;
        width: 100%;
        height: 42px;
        border-radius: 8px;
        font: inherit;
      }
      input {
        border: 1px solid #d1d5db;
        padding: 0 12px;
        background: #fff;
        color: #111827;
      }
      button {
        border: 0;
        background: #111827;
        color: #fff;
        cursor: pointer;
      }
      button:disabled {
        opacity: .64;
        cursor: default;
      }
      p {
        min-height: 20px;
        margin: 10px 0 0;
        color: #b91c1c;
        font-size: 14px;
      }
      @media (prefers-color-scheme: dark) {
        :root {
          background: #111827;
          color: #f9fafb;
        }
        input {
          border-color: #374151;
          background: #1f2937;
          color: #f9fafb;
        }
        button {
          background: #f9fafb;
          color: #111827;
        }
      }
    </style>
  </head>
  <body>
    <main>
      <h1>Caliban</h1>
      <form>
        <input name="password" type="password" autocomplete="current-password" placeholder="Password" required autofocus>
        <button type="submit">Sign in</button>
      </form>
      <p role="alert"></p>
    </main>
    <script>
      const form = document.querySelector("form");
      const button = document.querySelector("button");
      const error = document.querySelector("p");
      const params = new URLSearchParams(location.search);
      const rawNext = params.get("next") || "/";
      const next = rawNext.startsWith("/") && !rawNext.startsWith("//") ? rawNext : "/";
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        error.textContent = "";
        button.disabled = true;
        try {
          const password = new FormData(form).get("password") || "";
          const response = await fetch("/api/auth/login", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ password }),
          });
          if (!response.ok) {
            let message = "Sign in failed.";
            try {
              const body = await response.json();
              message = body.error || message;
            } catch {}
            throw new Error(message);
          }
          location.assign(next);
        } catch (err) {
          error.textContent = err instanceof Error ? err.message : "Sign in failed.";
        } finally {
          button.disabled = false;
        }
      });
    </script>
  </body>
</html>`
