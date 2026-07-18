// Package internalapi defines the URL contract shared by sandbox clients and
// the platform's internal API. Base URLs are origins; callers append the
// /api/v1/internal route prefix themselves.
package internalapi

import "strings"

const routePrefix = "/api/v1/internal"

// NormalizeBaseURL converts both the canonical origin-only form and the legacy
// path-qualified form into an origin-only base URL. Supporting the legacy form
// keeps already-running sandboxes functional while new launches adopt the
// canonical contract.
func NormalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(base, routePrefix)
}

// NormalizeInternalBaseURL returns the canonical path-qualified base expected
// by integration clients whose operation paths are relative to the internal
// API (for example, /issues and /session-tabs).
func NormalizeInternalBaseURL(raw string) string {
	base := NormalizeBaseURL(raw)
	if base == "" {
		return ""
	}
	return base + routePrefix
}
