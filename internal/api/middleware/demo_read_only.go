package middleware

import "net/http"

func DemoReadOnly(enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isDemoReadOnlyAllowed(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, "DEMO_READ_ONLY", "This public demo is read-only.")
		})
	}
}

func isDemoReadOnlyAllowed(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPost:
		return path == "/api/v1/auth/demo" || path == "/api/v1/auth/logout"
	default:
		return false
	}
}
