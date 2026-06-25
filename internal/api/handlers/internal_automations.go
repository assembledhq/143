package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

type internalAutomationDelegate interface {
	Create(w http.ResponseWriter, r *http.Request)
	Update(w http.ResponseWriter, r *http.Request)
	RunNow(w http.ResponseWriter, r *http.Request)
	Pause(w http.ResponseWriter, r *http.Request)
	Resume(w http.ResponseWriter, r *http.Request)
}

type internalAutomationLookup interface {
	GetByID(ctx context.Context, orgID, automationID uuid.UUID) (models.Automation, error)
}

type InternalAutomationHandler struct {
	delegate      internalAutomationDelegate
	sessionStore  internalSessionGetter
	automation    internalAutomationLookup
	signingSecret string
}

func NewInternalAutomationHandler(delegate internalAutomationDelegate, sessionStore internalSessionGetter, automation internalAutomationLookup, signingSecret string) *InternalAutomationHandler {
	return &InternalAutomationHandler{
		delegate:      delegate,
		sessionStore:  sessionStore,
		automation:    automation,
		signingSecret: signingSecret,
	}
}

func (h *InternalAutomationHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := h.authorize(w, r)
	if !ok {
		return
	}
	body, ok := h.readAndAuthorizePayloadRepository(w, r, claims, true)
	if !ok {
		return
	}
	h.delegate.Create(w, h.requestWithOrgAndBody(r, session.OrgID, body))
}

func (h *InternalAutomationHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !h.authorizeAutomationID(w, r, claims) {
		return
	}
	body, ok := h.readAndAuthorizePayloadRepository(w, r, claims, false)
	if !ok {
		return
	}
	h.delegate.Update(w, h.requestWithOrgAndBody(r, session.OrgID, body))
}

func (h *InternalAutomationHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	h.forwardAutomationAction(w, r, h.delegate.RunNow)
}

func (h *InternalAutomationHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.forwardAutomationAction(w, r, h.delegate.Pause)
}

func (h *InternalAutomationHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.forwardAutomationAction(w, r, h.delegate.Resume)
}

func (h *InternalAutomationHandler) forwardAutomationAction(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	claims, session, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !h.authorizeAutomationID(w, r, claims) {
		return
	}
	next(w, h.requestWithOrgAndBody(r, session.OrgID, nil))
}

func (h *InternalAutomationHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, models.Session, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessionStore)
	if !ok {
		return nil, models.Session{}, false
	}
	if claims.SessionOrigin == string(models.SessionOriginAutomationGoalImprovement) {
		writeError(w, r, http.StatusForbidden, "TOOL_NOT_AVAILABLE", "automation management is not available to automation goal improvement sessions")
		return nil, models.Session{}, false
	}
	return claims, session, true
}

func (h *InternalAutomationHandler) readAndAuthorizePayloadRepository(w http.ResponseWriter, r *http.Request, claims *auth.InternalTokenClaims, requireRepo bool) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "failed to read request body", err)
		return nil, false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "request body must be a JSON object", err)
		return nil, false
	}
	rawRepo, hasRepo := payload["repository_id"]
	if rawIdentityScope, hasIdentityScope := payload["identity_scope"]; hasIdentityScope && string(rawIdentityScope) != "null" {
		var identityScope models.AutomationIdentityScope
		if err := json.Unmarshal(rawIdentityScope, &identityScope); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", "identity_scope must be a string", err)
			return nil, false
		}
		if identityScope != "" && identityScope != models.AutomationIdentityScopeOrg {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", "session automation tools must use identity_scope=org")
			return nil, false
		}
	}
	if !hasRepo || string(rawRepo) == "null" {
		if requireRepo {
			writeError(w, r, http.StatusBadRequest, "MISSING_REPOSITORY_ID", "repository_id is required for session-created automations")
			return nil, false
		}
		return body, true
	}
	var repoID uuid.UUID
	if err := json.Unmarshal(rawRepo, &repoID); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository_id must be a UUID", err)
		return nil, false
	}
	if repoID != claims.RepoID {
		writeError(w, r, http.StatusForbidden, "REPO_MISMATCH", "session automation tools may only manage automations for the session repository")
		return nil, false
	}
	return body, true
}

func (h *InternalAutomationHandler) authorizeAutomationID(w http.ResponseWriter, r *http.Request, claims *auth.InternalTokenClaims) bool {
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return false
	}
	automation, err := h.automation.GetByID(r.Context(), claims.OrgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found", err)
		return false
	}
	if automation.RepositoryID == nil || *automation.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusForbidden, "REPO_MISMATCH", "session automation tools may only manage automations for the session repository")
		return false
	}
	return true
}

func (h *InternalAutomationHandler) requestWithOrgAndBody(r *http.Request, orgID uuid.UUID, body []byte) *http.Request {
	ctx := middleware.WithOrgID(r.Context(), orgID)
	if body == nil {
		body = []byte(`{}`)
	}
	next := r.Clone(ctx)
	next.Body = io.NopCloser(bytes.NewReader(body))
	return next
}
