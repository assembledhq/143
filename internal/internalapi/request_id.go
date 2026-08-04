package internalapi

import "strings"

// ParentRequestIDHeader correlates a trusted app-to-worker request with the
// originating public API request. It is diagnostic metadata, not authorization.
const ParentRequestIDHeader = "X-143-Parent-Request-ID"

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
