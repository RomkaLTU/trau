package webserver

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ErrTokenRequired is returned when a non-loopback bind has no bearer token
// configured. The hub controls an autonomous, merge-capable system, so an
// exposed bind without a token is a refusal, not a warning.
var ErrTokenRequired = errors.New("a non-loopback bind requires a bearer token (set SERVE_TOKEN)")

// Loopback reports whether bind resolves to a loopback-only address, the one
// case where the API needs no token. Anything it cannot prove is loopback —
// including an empty bind, which listens on every interface — is treated as
// exposed so the policy fails closed.
func Loopback(bind string) bool {
	host := strings.TrimSpace(bind)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DialHost normalizes a bind into a host that can be dialed: a wildcard bind
// listens on every interface, so loopback is the address that reaches it.
func DialHost(bind string) string {
	switch strings.TrimSpace(bind) {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return bind
}

// CheckExposure enforces the exposure policy at startup: loopback binds are
// free, any other bind must carry a token. It is the gate that keeps a control
// surface from ever coming up reachable without authentication.
func CheckExposure(bind, token string) error {
	if Loopback(bind) {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("refusing to serve on %s: %w", bind, ErrTokenRequired)
	}
	return nil
}

// serveTokenCookie mirrors the bearer token for requests the browser issues
// without a header — an <img src> pointing at an attachment's bytes, or a
// download link — so those load on an exposed bind. The SPA sets it from the same
// token it stores for fetches, so it grants nothing the header would not.
const serveTokenCookie = "trau_serve_token"

// requireToken rejects any request that carries neither the expected bearer
// token nor the cookie mirroring it, comparing in constant time.
func requireToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenAuthorized(r, want) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenAuthorized reports whether the request presents the serve token, by the
// Authorization header the SPA sends on fetches or the cookie it mirrors the
// token into for requests the browser makes without a header.
func tokenAuthorized(r *http.Request, want []byte) bool {
	if got, ok := bearerToken(r.Header.Get("Authorization")); ok && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
		return true
	}
	if c, err := r.Cookie(serveTokenCookie); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), want) == 1 {
		return true
	}
	return false
}

func bearerToken(header string) (string, bool) {
	const scheme = "Bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return header[len(scheme):], true
}
