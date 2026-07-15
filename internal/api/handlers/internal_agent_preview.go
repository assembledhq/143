package handlers

import (
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// InternalAgentPreviewHandler authorizes sandbox tools with the session-scoped
// internal token, then delegates to the same preview handlers used by users.
type InternalAgentPreviewHandler struct {
	preview  *PreviewHandler
	sessions *db.SessionStore
	secret   string
	logger   zerolog.Logger
}

func NewInternalAgentPreviewHandler(preview *PreviewHandler, sessions *db.SessionStore, secret string, logger zerolog.Logger) *InternalAgentPreviewHandler {
	return &InternalAgentPreviewHandler{preview: preview, sessions: sessions, secret: secret, logger: logger}
}

func (h *InternalAgentPreviewHandler) authorize(w http.ResponseWriter, r *http.Request, requiredScope string) (*http.Request, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := auth.ValidateInternalToken(h.secret, token)
	if token == "" || err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "invalid sandbox token", err)
		return r, false
	}
	requestedID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session id")
		return r, false
	}
	if claims.SessionID == nil || *claims.SessionID != requestedID {
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "sandbox token is not authorized for this session")
		return r, false
	}
	if !hasInternalToolScope(claims.AllowedToolScopes, requiredScope) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOOL_NOT_AVAILABLE", "sandbox token does not allow this preview operation")
		return r, false
	}
	session, err := h.sessions.GetByID(r.Context(), claims.OrgID, requestedID)
	if err != nil || session.RepositoryID == nil || *session.RepositoryID != claims.RepoID {
		h.logger.Warn().Err(err).Str("session_id", requestedID.String()).Msg("sandbox preview authorization failed")
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "sandbox token is not authorized for this session")
		return r, false
	}
	ctx := middleware.WithOrgID(r.Context(), claims.OrgID)
	userID := uuid.Nil
	if session.TriggeredByUserID != nil {
		userID = *session.TriggeredByUserID
	}
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: claims.OrgID, Role: models.RoleMember})
	return r.WithContext(ctx), true
}

func (h *InternalAgentPreviewHandler) delegate(scope string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorized, ok := h.authorize(w, r, scope)
		if !ok {
			return
		}
		handler(w, authorized)
	}
}

func (h *InternalAgentPreviewHandler) Ensure(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:manage", h.preview.EnsurePreview)(w, r)
}
func (h *InternalAgentPreviewHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.GetPreview)(w, r)
}
func (h *InternalAgentPreviewHandler) Update(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:manage", h.preview.UpdatePreview)(w, r)
}
func (h *InternalAgentPreviewHandler) Restart(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:manage", h.preview.RestartPreview)(w, r)
}
func (h *InternalAgentPreviewHandler) Stop(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:manage", h.preview.StopPreview)(w, r)
}
func (h *InternalAgentPreviewHandler) Observe(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.Observe)(w, r)
}
func (h *InternalAgentPreviewHandler) Act(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:interact", h.preview.Act)(w, r)
}
func (h *InternalAgentPreviewHandler) BrowserControl(w http.ResponseWriter, r *http.Request) {
	middleware.OrgIDFromContext(r.Context())
	h.delegate("preview:read", h.preview.GetBrowserControl)(w, r)
}
func (h *InternalAgentPreviewHandler) RequestHumanHandoff(w http.ResponseWriter, r *http.Request) {
	middleware.OrgIDFromContext(r.Context())
	h.delegate("preview:interact", h.preview.RequestHumanHandoff)(w, r)
}
func (h *InternalAgentPreviewHandler) Screenshot(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.CaptureScreenshot)(w, r)
}
func (h *InternalAgentPreviewHandler) Console(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.ReadConsole)(w, r)
}
func (h *InternalAgentPreviewHandler) Inspect(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.InspectElement)(w, r)
}
func (h *InternalAgentPreviewHandler) Interact(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:interact", h.preview.ExecuteInteraction)(w, r)
}
func (h *InternalAgentPreviewHandler) MultiViewport(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.CaptureMultiViewport)(w, r)
}
func (h *InternalAgentPreviewHandler) VisualDiff(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.ComputeVisualDiff)(w, r)
}
func (h *InternalAgentPreviewHandler) Assert(w http.ResponseWriter, r *http.Request) {
	h.delegate("preview:read", h.preview.RunAssertions)(w, r)
}
