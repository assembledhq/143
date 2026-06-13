package middleware

import (
	"net/http"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

// LogContext returns middleware that injects a zerolog.Logger into the request
// context enriched with org_id, user_id, and request_id. Handlers retrieve it
// via zerolog.Ctx(r.Context()). Must be registered after Auth and OrgContext.
func LogContext(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFromContext(r.Context())
			orgID := OrgIDFromContext(r.Context())
			reqID := chiMiddleware.GetReqID(r.Context())
			apiClient := APIClientFromContext(r.Context())
			apiToken := APITokenFromContext(r.Context())
			apiVersion := r.Header.Get(APIVersionHeader)

			ctx := WithAPIVersion(r.Context(), apiVersion)
			logCtx := logger.With().
				Str("org_id", orgID.String()).
				Str("request_id", reqID)
			if user != nil {
				logCtx = logCtx.Str("user_id", user.ID.String())
			}
			if apiClient != nil {
				logCtx = logCtx.Str("api_client_id", apiClient.ID.String())
			}
			if apiToken != nil {
				logCtx = logCtx.Str("api_token_id", apiToken.ID.String())
			}
			if apiVersion != "" {
				logCtx = logCtx.Str("api_version", apiVersion)
			}
			l := logCtx.Logger()
			ctx = l.WithContext(ctx)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
