// Package handlers — auth_scope.go
//
// Shared scope-resolution helpers for the OAuth subscription endpoints
// (codex-auth, claude-code-auth). Both flows expose the same scope semantics:
// org scope is admin-gated, personal scope coerces UserID from the request
// context. Centralising the gating here keeps the two handlers in lockstep.
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// errAdminRequired marks an org-scope mutation that lacks admin role.
var errAdminRequired = errors.New("admin role required for scope=org operations")

// errInvalidScope marks a malformed scope query/body value.
var errInvalidScope = errors.New("invalid scope: must be \"org\" or \"personal\"")

// errUnauthenticated marks a request that didn't carry a user context.
var errUnauthenticated = errors.New("unauthenticated")

// resolveOAuthScope builds a models.Scope from already-extracted request
// context, defaulting to org scope when scopeParam is empty. Personal scope
// is allowed for any authenticated user; org scope requires admin role.
//
// Callers must extract orgID + userID + activeRole at the handler entry
// point (so the org-id lint rule sees middleware.OrgIDFromContext directly
// in each handler body) and pass them in here.
func resolveOAuthScope(orgID uuid.UUID, userID uuid.UUID, activeRole, scopeParam string) (models.Scope, error) {
	switch models.CodingCredentialScope(scopeParam) {
	case models.CodingCredentialScopePersonal:
		uid := userID
		return models.Scope{OrgID: orgID, UserID: &uid}, nil
	case "", models.CodingCredentialScopeOrg:
		if activeRole != "admin" {
			return models.Scope{}, errAdminRequired
		}
		return models.Scope{OrgID: orgID}, nil
	default:
		return models.Scope{}, errInvalidScope
	}
}

// writeAuthScopeError translates the scope-resolution sentinels above to
// HTTP responses. Keeps the per-handler error-mapping noise down.
func writeAuthScopeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAdminRequired):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error(), err)
	case errors.Is(err, errInvalidScope):
		writeError(w, r, http.StatusBadRequest, "INVALID_SCOPE", err.Error(), err)
	case errors.Is(err, errUnauthenticated):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error(), err)
	default:
		writeError(w, r, http.StatusInternalServerError, "SCOPE_RESOLVE_FAILED", "failed to resolve scope", err)
	}
}
