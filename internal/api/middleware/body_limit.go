package middleware

import (
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
)

const (
	bytesPerKiB int64 = 1024
	bytesPerMiB       = 1024 * bytesPerKiB

	// DefaultMaxBodyBytes is the default JSON API request body limit.
	DefaultMaxBodyBytes = bytesPerMiB
)

// MaxBodySize returns middleware that limits request body size.
// Default limit is 1MB. Returns 413 Payload Too Large if exceeded.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return MaxBodySizeForPaths(maxBytes, nil)
}

// MaxBodySizeForPaths returns middleware that applies a default request body
// limit, with exact path overrides for endpoints that intentionally accept
// larger bodies.
func MaxBodySizeForPaths(defaultMaxBytes int64, pathLimits map[string]int64) func(http.Handler) http.Handler {
	if defaultMaxBytes <= 0 {
		defaultMaxBytes = DefaultMaxBodyBytes
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			maxBytes := defaultMaxBytes
			if pathMaxBytes, ok := pathLimits[r.URL.Path]; ok && pathMaxBytes > 0 {
				maxBytes = pathMaxBytes
			}

			if r.ContentLength > maxBytes {
				zerolog.Ctx(r.Context()).Warn().Int64("content_length", r.ContentLength).Int64("max_bytes", maxBytes).Msg("request body too large")
				writePayloadTooLarge(w, maxBytes)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func writePayloadTooLarge(w http.ResponseWriter, maxBytes int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	fmt.Fprintf(w, `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"Request body too large (max %s)"}}`+"\n", formatByteLimit(maxBytes))
}

func formatByteLimit(bytes int64) string {
	switch {
	case bytes >= bytesPerMiB && bytes%bytesPerMiB == 0:
		return fmt.Sprintf("%d MB", bytes/bytesPerMiB)
	case bytes >= bytesPerKiB && bytes%bytesPerKiB == 0:
		return fmt.Sprintf("%d KB", bytes/bytesPerKiB)
	case bytes == 1:
		return "1 byte"
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}
