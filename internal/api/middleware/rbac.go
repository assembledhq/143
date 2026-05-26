package middleware

import (
	"net/http"
	"slices"
	"strings"
)

// RequireRole returns middleware that restricts access to users whose role in
// the active org is one of the given roles. Must be applied AFTER Auth
// middleware (which resolves the active membership and sets the active role
// in context). Reads ActiveRoleFromContext rather than user.Role so that a
// user who is admin in org A but viewer in org B is correctly gated when
// operating as a viewer of B.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFromContext(r.Context())
			if user == nil {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
				return
			}

			role := ActiveRoleFromContext(r.Context())
			if role == "preview_api_token" {
				if strings.HasPrefix(r.URL.Path, "/api/v1/previews") {
					next.ServeHTTP(w, r)
					return
				}
				writeError(w, http.StatusForbidden, "FORBIDDEN", "preview API tokens are limited to preview routes")
				return
			}
			if !slices.Contains(roles, role) {
				writeError(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
