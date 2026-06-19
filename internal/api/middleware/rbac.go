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
			if APITokenFromContext(r.Context()) != nil {
				required := requiredAPIScope(r.Method, r.URL.Path)
				if required == "" {
					writeError(w, http.StatusForbidden, "FORBIDDEN", "API token is not allowed to access this route")
					return
				}
				RequireAPIScope(required)(next).ServeHTTP(w, r)
				return
			}

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

func requiredAPIScope(method, path string) string {
	switch {
	case method == http.MethodGet && (path == "/api/v1/sessions" || strings.HasPrefix(path, "/api/v1/sessions/")):
		return "sessions:read"
	case method == http.MethodPost && path == "/api/v1/sessions":
		return "sessions:create"
	case method == http.MethodPost && strings.HasSuffix(path, "/messages") && strings.HasPrefix(path, "/api/v1/sessions/"):
		return "sessions:write"
	case method == http.MethodPost && (strings.HasSuffix(path, "/cancel") || strings.HasSuffix(path, "/end")) && strings.HasPrefix(path, "/api/v1/sessions/"):
		return "sessions:cancel"
	case method == http.MethodPost && strings.HasSuffix(path, "/retry") && strings.HasPrefix(path, "/api/v1/sessions/"):
		return "sessions:write"
	case method == http.MethodPost && (strings.HasSuffix(path, "/pr") || strings.HasSuffix(path, "/branch")) && strings.HasPrefix(path, "/api/v1/sessions/"):
		return "sessions:publish"
	case method == http.MethodGet && (path == "/api/v1/automations" || strings.HasPrefix(path, "/api/v1/automations/")):
		return "automations:read"
	case method == http.MethodPost && path == "/api/v1/automations":
		return "automations:create"
	case (method == http.MethodPatch || method == http.MethodDelete) && strings.HasPrefix(path, "/api/v1/automations/"):
		return "automations:write"
	case method == http.MethodPost && strings.HasSuffix(path, "/run") && strings.HasPrefix(path, "/api/v1/automations/"):
		return "automations:run"
	case method == http.MethodPost && (strings.HasSuffix(path, "/pause") || strings.HasSuffix(path, "/resume")) && strings.HasPrefix(path, "/api/v1/automations/"):
		return "automations:write"
	case method == http.MethodGet && strings.HasPrefix(path, "/api/v1/previews"):
		return "previews:read"
	case method == http.MethodPost && path == "/api/v1/previews":
		return "previews:create"
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/previews/") && (strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/restart")):
		return "previews:stop"
	default:
		return ""
	}
}
