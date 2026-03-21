package middleware

import (
	"net/http"

	"github.com/rs/zerolog"
)

// OrgContext middleware ensures the org_id is set from the authenticated user.
// This is effectively a no-op since Auth already sets org_id, but it serves as
// a validation layer that rejects requests with no org context.
func OrgContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgID := OrgIDFromContext(r.Context())
		if orgID.String() == "00000000-0000-0000-0000-000000000000" {
			zerolog.Ctx(r.Context()).Warn().Msg("request rejected: no organization context")
			http.Error(w, `{"error":{"code":"FORBIDDEN","message":"no organization context"}}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
