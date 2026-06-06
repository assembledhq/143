package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

const (
	apiClientContextKey  contextKey = "api_client"
	apiTokenContextKey   contextKey = "api_token"
	apiVersionContextKey contextKey = "api_version"

	APIVersionHeader = "143-Version"
)

func APIClientFromContext(ctx context.Context) *models.APIClient {
	client, _ := ctx.Value(apiClientContextKey).(*models.APIClient)
	return client
}

func APITokenFromContext(ctx context.Context) *models.APIToken {
	token, _ := ctx.Value(apiTokenContextKey).(*models.APIToken)
	return token
}

func APIVersionFromContext(ctx context.Context) string {
	version, _ := ctx.Value(apiVersionContextKey).(string)
	return version
}

func WithAPIVersion(ctx context.Context, version string) context.Context {
	version = strings.TrimSpace(version)
	if version == "" {
		return ctx
	}
	return context.WithValue(ctx, apiVersionContextKey, version)
}

func WithAPIIdentity(ctx context.Context, client *models.APIClient, token *models.APIToken) context.Context {
	if client != nil {
		ctx = context.WithValue(ctx, apiClientContextKey, client)
		ctx = WithOrgID(ctx, client.OrgID)
	}
	if token != nil {
		ctx = context.WithValue(ctx, apiTokenContextKey, token)
	}
	return ctx
}

func RequireAPIScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := APITokenFromContext(r.Context())
			if token == nil {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API token required")
				return
			}
			for _, tokenScope := range token.Scopes {
				if tokenScope == scope {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeErrorDetails(w, http.StatusForbidden, "FORBIDDEN", "API token is missing required scope", map[string]string{
				"required_scope": scope,
			})
		})
	}
}
