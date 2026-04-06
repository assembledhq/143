package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// PreviewHandler handles all preview-related HTTP endpoints.
type PreviewHandler struct {
	manager *preview.Manager
	store   *db.PreviewStore
	logger  zerolog.Logger
	audit   *db.AuditEmitter
}

// NewPreviewHandler creates a new PreviewHandler.
func NewPreviewHandler(manager *preview.Manager, store *db.PreviewStore, logger zerolog.Logger) *PreviewHandler {
	return &PreviewHandler{
		manager: manager,
		store:   store,
		logger:  logger,
	}
}

// SetAuditEmitter injects the audit emitter for logging preview events.
func (h *PreviewHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// =============================================================================
// Helpers
// =============================================================================

func (h *PreviewHandler) getActivePreview(w http.ResponseWriter, r *http.Request) (*models.PreviewInstance, bool) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return nil, false
	}

	instance, err := h.store.GetActivePreviewForSession(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NO_ACTIVE_PREVIEW", "no active preview for this session")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
		}
		return nil, false
	}

	return instance, true
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview — Start a preview
// =============================================================================

type startPreviewRequest struct {
	Config        *models.PreviewConfig `json:"config"`
	BaseCommitSHA string                `json:"base_commit_sha"`
	ProfileName   string                `json:"profile_name"`
}

func (h *PreviewHandler) StartPreview(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return
	}

	var body startPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if body.Config == nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_CONFIG", "preview config is required")
		return
	}

	input := preview.StartPreviewInput{
		SessionID:     sessionID,
		OrgID:         orgID,
		UserID:        user.ID,
		Config:        body.Config,
		BaseCommitSHA: body.BaseCommitSHA,
		ProfileName:   body.ProfileName,
	}

	instance, err := h.manager.StartPreview(r.Context(), input)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[*models.PreviewInstance]{Data: instance})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview — Get preview status
// =============================================================================

func (h *PreviewHandler) GetPreview(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	status, err := h.manager.GetStatus(r.Context(), orgID, instance.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview status", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewStatusResponse]{Data: status})
}

// =============================================================================
// DELETE /api/v1/sessions/{id}/preview — Stop a preview
// =============================================================================

func (h *PreviewHandler) StopPreview(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	if err := h.manager.StopPreview(r.Context(), orgID, instance.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/restart — Restart a preview
// =============================================================================

func (h *PreviewHandler) RestartPreview(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	// For MVP, restart = stop the existing preview. The client should start a new one.
	if err := h.manager.StopPreview(r.Context(), orgID, instance.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_RESTART_FAILED", "failed to restart preview", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/logs — Get preview logs
// =============================================================================

func (h *PreviewHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	logs, err := h.store.ListLogsByPreview(r.Context(), orgID, instance.ID, nil)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get logs", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewLog]{Data: logs})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/services — Get per-service status
// =============================================================================

func (h *PreviewHandler) GetServices(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	services, err := h.store.ListServicesByPreview(r.Context(), orgID, instance.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get services", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewService]{Data: services})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/bootstrap — Mint a bootstrap token
// =============================================================================

func (h *PreviewHandler) MintBootstrapToken(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	token, err := h.manager.MintBootstrapToken(r.Context(), orgID, user.ID, instance.ID)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "BOOTSTRAP_TOKEN_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{
		"token":      token,
		"preview_id": instance.ID.String(),
	}})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/snapshots — Get screenshot timeline
// =============================================================================

func (h *PreviewHandler) GetSnapshots(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	snapshots, err := h.store.ListSnapshotsByPreview(r.Context(), orgID, instance.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get snapshots", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewSnapshot]{Data: snapshots})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/extend — Extend preview TTL
// =============================================================================

func (h *PreviewHandler) ExtendTTL(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	if err := h.manager.ExtendTTL(r.Context(), orgID, instance.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTEND_TTL_FAILED", "failed to extend TTL", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "extended"}})
}

// =============================================================================
// GET /api/v1/repos/{owner}/{repo}/preview/detect — Detect preview readiness
// =============================================================================

func (h *PreviewHandler) DetectReadiness(w http.ResponseWriter, r *http.Request) {
	// For MVP, return a placeholder. Full implementation requires reading
	// .143/preview.json from the repo via the GitHub API.
	result := map[string]string{
		"readiness": string(models.PreviewReadinessNotSupported),
		"reason":    "preview readiness detection not yet implemented",
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: result})
}

// =============================================================================
// Preview Inspector stubs (require headless browser — not yet implemented)
// =============================================================================

func inspectorNotAvailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, "PREVIEW_INSPECTOR_NOT_AVAILABLE",
		"preview inspector (headless browser) is not yet implemented")
}

// CaptureScreenshot handles POST .../screenshot.
func (h *PreviewHandler) CaptureScreenshot(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// InspectElement handles POST .../inspect.
func (h *PreviewHandler) InspectElement(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// ReadConsole handles GET .../console.
func (h *PreviewHandler) ReadConsole(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// SubmitDesignFeedback handles POST .../design-feedback.
func (h *PreviewHandler) SubmitDesignFeedback(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// ExecuteInteraction handles POST .../interact.
func (h *PreviewHandler) ExecuteInteraction(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// CaptureMultiViewport handles POST .../multi-viewport.
func (h *PreviewHandler) CaptureMultiViewport(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// ComputeVisualDiff handles POST .../visual-diff.
func (h *PreviewHandler) ComputeVisualDiff(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}

// RunAssertions handles POST .../assert.
func (h *PreviewHandler) RunAssertions(w http.ResponseWriter, r *http.Request) {
	inspectorNotAvailable(w, r)
}
