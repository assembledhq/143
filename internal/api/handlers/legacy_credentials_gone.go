package handlers

import (
	"net/http"
)

// LegacyCredentialsGone serves 410 Gone for the retired legacy credential
// endpoints:
//
//   - /api/v1/settings/credentials/{personal,team,resolved}
//   - /api/v1/settings/coding-auths (+ /reorder, /{id})
//
// Coding-agent credentials are managed via /api/v1/coding-credentials
// (scope=personal|org|resolved); see design doc 65 (unified coding
// credentials), PR 5 cleanup.
func LegacyCredentialsGone(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusGone, "gone",
		"this endpoint has been removed — use /api/v1/coding-credentials (scope=personal|org|resolved)")
}
