package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

// internalSessionGetter is the narrow slice of *db.SessionStore that internal
// session-scoped handlers need. Accepting an interface keeps these handlers
// unit-testable without a database.
type internalSessionGetter interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

func authorizeInternalSession(w http.ResponseWriter, r *http.Request, signingSecret string, sessionStore internalSessionGetter) (*auth.InternalTokenClaims, models.Session, bool) {
	tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization token")
		return nil, models.Session{}, false
	}
	claims, err := auth.ValidateInternalToken(signingSecret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token", err)
		return nil, models.Session{}, false
	}
	if claims.SessionID == nil {
		writeError(w, r, http.StatusForbidden, "SESSION_REQUIRED", "session-scoped token required")
		return nil, models.Session{}, false
	}
	session, err := sessionStore.GetByID(r.Context(), claims.OrgID, *claims.SessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return nil, models.Session{}, false
	}
	if session.RepositoryID == nil || *session.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusForbidden, "REPO_MISMATCH", "token is not authorized for this session repository")
		return nil, models.Session{}, false
	}
	return claims, session, true
}
