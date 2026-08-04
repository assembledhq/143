package internalapi

import (
	"net/http"
	"strings"
)

// ParentRequestIDHeader correlates a trusted app-to-worker request with the
// originating public API request. It is diagnostic metadata, not authorization.
const ParentRequestIDHeader = "X-143-Parent-Request-ID"

// RoutePrefix is the path prefix for service-to-service routes, which are
// authenticated by the preview worker token rather than by an end user.
const RoutePrefix = "/internal/"

// TrustedParentRequestID returns correlation metadata only for requests on the
// internal service-to-service surface. Any client can set the header, so
// honoring it on public routes would let a caller attach someone else's request
// id to their own log lines and forge correlation chains.
func TrustedParentRequestID(r *http.Request) string {
	if r == nil || r.URL == nil || !strings.HasPrefix(r.URL.Path, RoutePrefix) {
		return ""
	}
	return NormalizeParentRequestID(r.Header.Get(ParentRequestIDHeader))
}

// NormalizeParentRequestID bounds and validates correlation metadata before it
// crosses a service boundary or is attached to structured logs.
func NormalizeParentRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:/", char) {
			continue
		}
		return ""
	}
	return value
}
