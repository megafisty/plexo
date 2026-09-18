package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
)

const (
	// sessionCookie is the shared-Plexo-password session cookie name.
	sessionCookie = "plexo_session"
	// sessionMaxAge keeps a browser signed in for 30 days.
	sessionMaxAge = 30 * 24 * 60 * 60
	// sessionLabel domain-separates the cookie HMAC.
	sessionLabel = "plexo-session-v1"
)

// SessionAuth guards browser access with one shared password. On success it
// stores an opaque HMAC of the password in a persistent HttpOnly cookie, so a
// browser stays authenticated across reloads and restarts without the client
// ever holding the password. With an empty password, auth is not required.
type SessionAuth struct {
	password string
	token    string // hex HMAC; empty when auth is disabled
}

// NewSessionAuth returns a gate for the given password.
func NewSessionAuth(password string) *SessionAuth {
	a := &SessionAuth{password: password}
	if password != "" {
		mac := hmac.New(sha256.New, []byte(password))
		mac.Write([]byte(sessionLabel))
		a.token = hex.EncodeToString(mac.Sum(nil))
	}
	return a
}

// Required reports whether a password is configured.
func (a *SessionAuth) Required() bool { return a != nil && a.password != "" }

// requiredFor reports whether a request must authenticate. Loopback callers
// (same machine) are never asked for the password, even when one is set.
func (a *SessionAuth) requiredFor(r *http.Request) bool {
	return a.Required() && !isLoopback(r)
}

// Authenticated reports whether a request may proceed: true when no password is
// configured or the caller is on loopback, otherwise whether it carries the
// session cookie. The cookie comparison is constant-time so it cannot be probed
// byte by byte.
func (a *SessionAuth) Authenticated(r *http.Request) bool {
	if !a.requiredFor(r) {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.token)) == 1
}

// isLoopback reports whether the request originated on the same machine.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handle serves /api/session: GET probes, POST logs in, DELETE logs out.
func (a *SessionAuth) Handle(w http.ResponseWriter, r *http.Request) {
	// The probe reflects live auth state, so it must never be stored.
	w.Header().Set("Cache-Control", cacheNoStore)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]bool{
			"authRequired":  a.requiredFor(r),
			"authenticated": a.Authenticated(r),
		})
	case http.MethodPost:
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.password)) != 1 {
			http.Error(w, "invalid password", http.StatusUnauthorized)
			return
		}
		a.setCookie(w, a.token, sessionMaxAge)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		a.setCookie(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *SessionAuth) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
