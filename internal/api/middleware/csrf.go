package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

const (
	// CSRFCookieName is the name of the CSRF double-submit cookie.
	CSRFCookieName = "csrf_token"
	// CSRFHeaderName is the header the frontend sends the token in.
	CSRFHeaderName = "X-CSRF-Token"

	csrfTokenBytes = 32
)

// CSRF returns middleware that enforces double-submit cookie CSRF protection
// on state-changing HTTP methods. Safe methods (GET, HEAD, OPTIONS) are
// skipped. Requests with a Bearer Authorization header are also skipped
// because the browser does not attach those automatically.
func CSRF(signingKey string, logger zerolog.Logger) func(http.Handler) http.Handler {
	key := []byte(signingKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip CSRF for bearer-token authenticated requests.
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				next.ServeHTTP(w, r)
				return
			}

			// Safe methods: skip validation but ensure token cookie exists.
			if isSafeMethod(r.Method) {
				ensureCSRFCookie(w, r, key, logger)
				next.ServeHTTP(w, r)
				return
			}

			// State-changing method: validate token.
			cookie, err := r.Cookie(CSRFCookieName)
			if err != nil {
				writeError(w, http.StatusForbidden, "CSRF_FAILED", "missing CSRF cookie")
				return
			}

			header := r.Header.Get(CSRFHeaderName)
			if header == "" {
				writeError(w, http.StatusForbidden, "CSRF_FAILED", "missing CSRF header")
				return
			}

			if !validSignedToken(cookie.Value, key) {
				writeError(w, http.StatusForbidden, "CSRF_FAILED", "invalid CSRF token")
				return
			}

			// Compare cookie and header values (constant-time).
			if !hmac.Equal([]byte(cookie.Value), []byte(header)) {
				writeError(w, http.StatusForbidden, "CSRF_FAILED", "CSRF token mismatch")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GenerateSignedToken creates a signed CSRF token with format "payload.signature".
// Exported so the auth handler can set the cookie at login time.
func GenerateSignedToken(key []byte) (string, error) {
	raw := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("csrf: generate random token: %w", err)
	}
	payload := hex.EncodeToString(raw)
	sig := csrfHMAC(payload, key)
	return payload + "." + sig, nil
}

// SetCSRFCookie sets the CSRF double-submit cookie on the response.
// Exported so the auth handler can set it at login time.
// Returns an error if token generation fails.
func SetCSRFCookie(w http.ResponseWriter, r *http.Request, key []byte) error {
	token, err := GenerateSignedToken(key)
	if err != nil {
		return err
	}
	writeCSRFCookie(w, r, token)
	return nil
}

// ExtendCSRFCookie re-emits the existing valid CSRF cookie with a fresh
// MaxAge so its lifetime stays aligned with a sliding session refresh.
// If no valid cookie is present, a new signed token is issued instead.
// Keeps the same token value when possible so in-flight requests that
// already read document.cookie continue to validate.
func ExtendCSRFCookie(w http.ResponseWriter, r *http.Request, key []byte) error {
	if c, err := r.Cookie(CSRFCookieName); err == nil && validSignedToken(c.Value, key) {
		writeCSRFCookie(w, r, c.Value)
		return nil
	}
	return SetCSRFCookie(w, r, key)
}

func writeCSRFCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()), // match session cookie lifetime
		HttpOnly: false,                     // frontend JS must read this
		SameSite: http.SameSiteLaxMode,
		Secure:   IsRequestSecure(r),
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// ensureCSRFCookie sets the CSRF cookie if it doesn't already exist or
// if the existing token has an invalid signature. Errors from token
// generation are silently ignored (best-effort on safe methods).
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, key []byte, logger zerolog.Logger) {
	if c, err := r.Cookie(CSRFCookieName); err == nil && validSignedToken(c.Value, key) {
		return // already has a valid token
	}
	if err := SetCSRFCookie(w, r, key); err != nil {
		logger.Warn().Err(err).Msg("failed to set CSRF cookie")
	}
}

// IsRequestSecure reports whether the request arrived over HTTPS, either
// directly or via a TLS-terminating reverse proxy. Exported so other
// packages can set the Secure attribute on cookies consistently.
func IsRequestSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil || strings.EqualFold(r.URL.Scheme, "https") {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		parts := strings.Split(proto, ",")
		if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), "https") {
			return true
		}
	}
	if forwarded := strings.ToLower(r.Header.Get("Forwarded")); strings.Contains(forwarded, "proto=https") {
		return true
	}
	return false
}

func validSignedToken(token string, key []byte) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expected := csrfHMAC(parts[0], key)
	return hmac.Equal([]byte(parts[1]), []byte(expected))
}

func csrfHMAC(message string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
