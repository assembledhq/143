package middleware

import (
	"net/http"
	"slices"
)

// RequireRole returns middleware that restricts access to users with one of the given roles.
// Must be applied AFTER Auth middleware (which sets user in context).
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFromContext(r.Context())
			if user == nil {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
				return
			}

			if !slices.Contains(roles, user.Role) {
				writeError(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
