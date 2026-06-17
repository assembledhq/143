package handlers

import (
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func authorizeInternalSession(w http.ResponseWriter, r *http.Request, signingSecret string, sessionStore *db.SessionStore) (*auth.InternalTokenClaims, models.Session, bool) {
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
