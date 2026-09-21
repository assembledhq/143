package handlers

import (
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

// InternalToolGate enforces the per-target tool allowlist (design doc 125)
// on the internal API: a session token carrying the allowlist marker may
// call a route only when the token also carries that route's tool scope.
// Tokens without the marker (ordinary sessions) are unaffected; every
// handler still applies its own capability and scope checks afterwards.
type InternalToolGate struct {
	signingSecret string
}

func NewInternalToolGate(signingSecret string) *InternalToolGate {
	return &InternalToolGate{signingSecret: signingSecret}
}

// Require wraps next so an allowlisted token must carry the scope for
// tool ("namespace:action"). An invalid or missing token is left to the
// handler's own authorization so error shapes stay unchanged.
func (g *InternalToolGate) Require(tool string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g != nil && g.signingSecret != "" {
			tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if tokenStr != "" {
				if claims, err := auth.ValidateInternalToken(g.signingSecret, tokenStr); err == nil &&
					models.HasToolScope(claims.AllowedToolScopes, models.PerTargetToolAllowlistScope) &&
					!models.HasToolScope(claims.AllowedToolScopes, models.ToolScope(tool)) {
					writeError(w, r, http.StatusForbidden, "TOOL_NOT_ALLOWED", "this tool is not available to a per-target automation turn")
					return
				}
			}
		}
		next(w, r)
	}
}
