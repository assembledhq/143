package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sessionreview"
)

// SessionReviewHandler exposes the session-native review endpoints. Reviews
// are a single follow-up turn on the existing session; this handler is a
// thin transport layer around sessionreview.Service.
type SessionReviewHandler struct {
	service *sessionreview.Service
	logger  zerolog.Logger
	audit   *db.AuditEmitter
}

func NewSessionReviewHandler(service *sessionreview.Service, logger zerolog.Logger) *SessionReviewHandler {
	return &SessionReviewHandler{
		service: service,
		logger:  logger,
	}
}

func (h *SessionReviewHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// Capabilities handles GET /api/v1/sessions/{id}/review-capabilities.
// Returns the modes the agent supports natively and whether the session is
// in a state that can be reviewed right now.
func (h *SessionReviewHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	caps, err := h.service.Capabilities(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, sessionreview.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REVIEW_CAPABILITIES_FAILED", "failed to load review capabilities", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionReviewCapabilities]{Data: *caps})
}

// Start handles POST /api/v1/sessions/{id}/review with body { "mode": ... }.
// Enqueues a review continuation turn on the session.
func (h *SessionReviewHandler) Start(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	body := struct {
		Mode models.SessionReviewMode `json:"mode"`
	}{}
	// An empty body is allowed and means "default mode". Decode errors that
	// aren't EOF are real problems and should be surfaced.
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}

	resp, err := h.service.StartReview(r.Context(), orgID, sessionID, user.ID, body.Mode)
	if err != nil {
		switch {
		case errors.Is(err, sessionreview.ErrSessionNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		case errors.Is(err, sessionreview.ErrSnapshotExpired):
			writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "session sandbox has expired and can no longer be reviewed")
		case errors.Is(err, sessionreview.ErrNoChangesToReview):
			writeError(w, r, http.StatusConflict, "NO_CHANGES", "session has no changes to review yet")
		case errors.Is(err, sessionreview.ErrSessionNotResumable):
			writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be idle or paused to start a review")
		case errors.Is(err, sessionreview.ErrAgentReviewUnsupported):
			writeError(w, r, http.StatusNotImplemented, "REVIEW_UNSUPPORTED", "this agent does not support native review")
		case errors.Is(err, sessionreview.ErrReviewModeUnsupported):
			writeError(w, r, http.StatusBadRequest, "MODE_UNSUPPORTED", "requested review mode is not supported by this agent")
		default:
			writeError(w, r, http.StatusInternalServerError, "REVIEW_START_FAILED", "failed to start review", err)
		}
		return
	}

	if h.audit != nil {
		sid := resp.SessionID
		emitUserAuditWithSession(h.audit, r, models.AuditActionSessionReviewRequested, models.AuditResourceSession, nil, &sid, nil,
			marshalAuditDetails(h.logger, map[string]any{
				"session_id": resp.SessionID.String(),
				"mode":       string(resp.Mode),
			}))
	}

	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.SessionReviewResponse]{Data: *resp})
}
