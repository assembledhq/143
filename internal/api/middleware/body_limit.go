package middleware

import (
	"net/http"
)

// MaxBodySize returns middleware that limits request body size.
// Default limit is 1MB. Returns 413 Payload Too Large if exceeded.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1MB default
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"request body too large"}}`, http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
