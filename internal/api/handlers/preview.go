package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// PreviewHandler handles all preview-related HTTP endpoints.
type PreviewHandler struct {
	manager         *preview.Manager
	store           *db.PreviewStore
	sessionStore    *db.SessionStore
	fileReader      sandbox.FileReader
	sandboxProvider agent.SandboxProvider
	snapshots       storage.SnapshotStore
	logger          zerolog.Logger
	audit           *db.AuditEmitter
}

// NewPreviewHandler creates a new PreviewHandler. fileReader is used to
// auto-detect .143/preview.json from the session's sandbox workspace when
// the client does not supply an explicit config; pass sandbox.NoOpFileReader
// in environments where workspace introspection is unavailable — its errors
// wrap sandbox.ErrFileNotFound so auto-detect cleanly falls through to the
// built-in default config instead of surfacing a 500.
//
// sandboxProvider and snapshots enable hydrate-on-demand: when Start Preview
// is hit on a session whose turn has already completed (container torn down,
// snapshot on disk), the handler creates a new container and restores the
// snapshot into it so the preview has a live workspace to run against.
// Both may be nil in test builds; the handler falls back to
// "NO_SANDBOX"/"SNAPSHOT_EXPIRED" errors in that case.
func NewPreviewHandler(manager *preview.Manager, store *db.PreviewStore, sessionStore *db.SessionStore, fileReader sandbox.FileReader, sandboxProvider agent.SandboxProvider, snapshots storage.SnapshotStore, logger zerolog.Logger) *PreviewHandler {
	return &PreviewHandler{
		manager:         manager,
		store:           store,
		sessionStore:    sessionStore,
		fileReader:      fileReader,
		sandboxProvider: sandboxProvider,
		snapshots:       snapshots,
		logger:          logger,
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

// requireManager checks that the preview manager is configured.
func (h *PreviewHandler) requireManager(w http.ResponseWriter, r *http.Request) bool {
	if h.manager == nil {
		writeError(w, r, http.StatusNotImplemented, "PREVIEW_NOT_AVAILABLE",
			"preview manager is not configured on this worker")
		return false
	}
	return true
}

// workspacePreviewConfigPath is the repo-relative path 143 looks at when a
// client calls StartPreview without supplying an explicit config.
const workspacePreviewConfigPath = ".143/preview.json"

// readWorkspacePreviewConfig attempts to read and parse .143/preview.json from
// the session's sandbox workspace. Returns:
//   - (cfg, nil)   when a valid committed config is found and parsed.
//   - (nil, nil)   for "no config to use" cases where the caller should fall
//     back to built-in defaults: no fileReader wired, the file is absent, or
//     its contents fail to parse (a malformed committed config is a user
//     authoring problem, not an infrastructure failure; surfacing it as a 500
//     would make the preview worse, not better, than the default).
//   - (nil, err)   for genuine infrastructure failures (docker exec failed,
//     context cancelled, sandbox gone) — the caller should surface these
//     instead of silently swapping in Node.js defaults for what may well be
//     a Go/Python/etc. project.
func (h *PreviewHandler) readWorkspacePreviewConfig(ctx context.Context, sb *agent.Sandbox, sessionID uuid.UUID) (*models.PreviewConfig, error) {
	if h.fileReader == nil {
		return nil, nil
	}
	content, _, err := h.fileReader.ReadFile(ctx, sb.ID, sb.WorkDir, workspacePreviewConfigPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrFileNotFound) {
			h.logger.Debug().
				Str("session_id", sessionID.String()).
				Str("path", workspacePreviewConfigPath).
				Msg("no committed preview config in workspace")
			return nil, nil
		}
		h.logger.Warn().
			Err(err).
			Str("session_id", sessionID.String()).
			Str("path", workspacePreviewConfigPath).
			Msg("failed to read committed preview config")
		return nil, fmt.Errorf("read %s: %w", workspacePreviewConfigPath, err)
	}
	cfg, err := preview.ParseConfig([]byte(content))
	if err != nil {
		h.logger.Warn().
			Err(err).
			Str("session_id", sessionID.String()).
			Str("path", workspacePreviewConfigPath).
			Msg("committed preview config failed to parse; falling back to defaults")
		return nil, nil
	}
	h.logger.Info().
		Str("session_id", sessionID.String()).
		Str("path", workspacePreviewConfigPath).
		Msg("using preview config from workspace")
	return cfg, nil
}

// acquireSandbox resolves a live sandbox for a preview start, picking between
// three strategies:
//   - Reuse: session.ContainerID is set; attach by ID.
//   - Hydrate: session has a snapshot and the sandbox was torn down; create a
//     new container, restore the snapshot, and publish the new container_id.
//   - Expired: no container and no usable snapshot; caller should return 410.
//
// The hydrated return distinguishes a freshly-created container from a reused
// one: it tells AbortReservation whether the caller's teardown path should
// destroy the sandbox (hydrate — we own it) or leave it alone (reuse — a turn
// still owns it).
//
// The returned error code steers the HTTP status: "NO_SANDBOX" (409),
// "SNAPSHOT_EXPIRED" (410), or empty for infrastructure failures (500).
func (h *PreviewHandler) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session) (sb *agent.Sandbox, hydrated bool, errCode string, err error) {
	// Reuse is only safe when the row believes the container is actually
	// running. A lingering container_id from a crashed worker or a session
	// whose sandbox_state has since moved to 'snapshotted'/'destroyed' should
	// fall through to hydrate/expired instead of attaching to a dead ID.
	if session.ContainerID != nil && *session.ContainerID != "" &&
		session.SandboxState == string(models.SandboxStateRunning) {
		candidate := &agent.Sandbox{
			ID:        *session.ContainerID,
			Provider:  "docker",
			WorkDir:   "/workspace",
			SessionID: session.ID.String(),
			OrgID:     session.OrgID.String(),
			Purpose:   "preview",
		}
		// Verify the container actually exists on the host. A row can drift
		// from reality when Docker is pruned out-of-band or a worker died
		// before the hold was released; attaching to a zombie ID would then
		// fail deeper in the preview start path with a confusing error.
		// A definitive "not found" falls through to hydrate; a transient
		// inspect error also falls through rather than attaching blindly
		// (hydrate will recreate the container cleanly).
		if h.sandboxProvider != nil {
			alive, inspectErr := h.sandboxProvider.IsAlive(ctx, candidate)
			if inspectErr != nil {
				h.logger.Warn().Err(inspectErr).
					Str("session_id", session.ID.String()).
					Str("container_id", candidate.ID).
					Msg("preview reuse: liveness check failed; falling through to hydrate")
			} else if alive {
				return candidate, false, "", nil
			} else {
				h.logger.Info().
					Str("session_id", session.ID.String()).
					Str("container_id", candidate.ID).
					Msg("preview reuse: recorded container no longer exists; falling through to hydrate")
			}
		} else {
			// No provider wired (e.g., cold handler): trust the row.
			return candidate, false, "", nil
		}
	}

	// No live container. Check whether we can hydrate one from a snapshot.
	if session.SnapshotKey == nil || *session.SnapshotKey == "" ||
		session.SandboxState == string(models.SandboxStateDestroyed) {
		return nil, false, "SNAPSHOT_EXPIRED", fmt.Errorf("session has no sandbox container and no usable snapshot; start a new turn to continue")
	}
	if h.sandboxProvider == nil || h.snapshots == nil {
		return nil, false, "NO_SANDBOX", fmt.Errorf("preview hydrate is not configured on this worker")
	}

	// Hydrate: build a SandboxConfig matching what the orchestrator uses so
	// the restored container has consistent resource limits and paths. Note
	// that WorkDir deliberately stays as the default — the snapshot tar
	// restores at absolute paths (it was taken with tar including abs
	// paths), so WorkDir only affects where ad-hoc commands run, and a
	// subsequent agent turn will wrap the sandbox with the repo-specific
	// WorkDir before executing.
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.SessionID = session.ID.String()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.Purpose = "preview_hydrate"

	sandbox, err := agent.HydrateSandboxFromSnapshot(ctx, h.sandboxProvider, h.snapshots, *session.SnapshotKey, sandboxCfg)
	if err != nil {
		return nil, false, "", fmt.Errorf("hydrate sandbox: %w", err)
	}

	// Publish the new container_id on the session so a concurrent ContinueSession
	// attaches to the same container (preview + turn coexistence). The CAS
	// inside PublishHydratedContainerID only writes when container_id IS NULL,
	// so if an orchestrator has already published a different ID we become the
	// loser: destroy the local container and return an error asking the caller
	// to retry (the retry's reuse path picks up the winner).
	actualID, err := h.sessionStore.PublishHydratedContainerID(ctx, orgID, session.ID, sandbox.ID)
	if err != nil {
		// We have a live container but couldn't publish its ID — tear it
		// down to avoid a hydrated orphan. This is rare (DB outage) and
		// safer than leaving an untracked container behind.
		_ = h.sandboxProvider.Destroy(context.Background(), sandbox)
		return nil, false, "", fmt.Errorf("publish container id: %w", err)
	}
	if actualID != sandbox.ID {
		_ = h.sandboxProvider.Destroy(context.Background(), sandbox)
		h.logger.Warn().
			Str("session_id", session.ID.String()).
			Str("winning_container_id", actualID).
			Str("losing_container_id", sandbox.ID).
			Msg("preview hydrate lost race to another holder; destroyed local container")
		return nil, false, "NO_SANDBOX", fmt.Errorf("another process attached to this session's sandbox first; please retry")
	}

	h.logger.Info().
		Str("session_id", session.ID.String()).
		Str("container_id", sandbox.ID).
		Msg("preview hydrate: new sandbox container created from snapshot")

	return sandbox, true, "", nil
}

// requireInspector returns the PreviewInspector or writes a 501 error response.
func (h *PreviewHandler) requireInspector(w http.ResponseWriter, r *http.Request) (preview.PreviewInspector, bool) {
	if h.manager == nil {
		writeError(w, r, http.StatusNotImplemented, "PREVIEW_INSPECTOR_NOT_AVAILABLE",
			"preview inspector (headless browser) is not configured on this worker")
		return nil, false
	}
	inspector := h.manager.Inspector()
	if inspector == nil {
		writeError(w, r, http.StatusNotImplemented, "PREVIEW_INSPECTOR_NOT_AVAILABLE",
			"preview inspector (headless browser) is not configured on this worker")
		return nil, false
	}
	return inspector, true
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview — Start a preview
// =============================================================================

type startPreviewRequest struct {
	Config        *models.PreviewConfig `json:"config"`
	BaseCommitSHA string                `json:"base_commit_sha"`
	ProfileName   string                `json:"profile_name"`
}

func defaultPreviewConfig() *models.PreviewConfig {
	return &models.PreviewConfig{
		Name:    "default",
		Primary: "app",
		Services: map[string]models.ServiceConfig{
			"app": {
				Command: []string{"npm", "start"},
				Port:    3000,
				Ready: models.ReadinessProbe{
					HTTPPath: "/",
				},
			},
		},
	}
}

func (h *PreviewHandler) StartPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return
	}

	var body startPreviewRequest
	// Tolerate empty body (e.g., frontend sends no config when auto-detecting).
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
			return
		}
	}
	// Look up the session to get its sandbox container.
	session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "session not found")
		return
	}

	// Reserve the preview BEFORE touching docker. This (a) short-circuits
	// capacity / existing-preview failures so a 503 can never leave a hydrated
	// container behind, and (b) acquires preview_holding_container=TRUE before
	// hydrate publishes container_id, so a concurrent turn release's
	// FinalizeContainerDestroy sees our hold and leaves the freshly-hydrated
	// container alone. The config passed here is "initial" — if the client
	// didn't supply one, we fall back to defaults and LaunchPreview will
	// overwrite the row if workspace autodetect resolves something different.
	initialConfig := body.Config
	if initialConfig == nil {
		initialConfig = defaultPreviewConfig()
	}
	input := preview.StartPreviewInput{
		SessionID:     sessionID,
		OrgID:         orgID,
		UserID:        user.ID,
		Config:        initialConfig,
		BaseCommitSHA: body.BaseCommitSHA,
		ProfileName:   body.ProfileName,
	}
	reservation, err := h.manager.ReservePreview(r.Context(), input)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview reserve failed")
		if errors.Is(err, preview.ErrPreviewCapacity) {
			writeError(w, r, http.StatusServiceUnavailable, "PREVIEW_CAPACITY_REACHED", err.Error())
			return
		}
		writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", "failed to start preview")
		return
	}

	// Three paths to a live sandbox, in preference order:
	//   1. Reuse — attach to an existing container (a turn is running or a
	//      prior preview already hydrated it).
	//   2. Hydrate — the container has been torn down but a snapshot exists;
	//      create a new container and restore the snapshot.
	//   3. SnapshotExpired — neither a container nor a usable snapshot exists.
	sb, hydrated, hydrateErrCode, hydrateErr := h.acquireSandbox(r.Context(), orgID, &session)
	if hydrateErr != nil {
		h.logger.Warn().Err(hydrateErr).
			Str("session_id", sessionID.String()).
			Str("error_code", hydrateErrCode).
			Msg("preview start: failed to acquire sandbox")
		// hydratedContainerID is "" — either we never hydrated, or
		// acquireSandbox's race-loss branch already destroyed the local
		// container before returning.
		h.manager.AbortReservation(r.Context(), reservation, "", fmt.Sprintf("acquire sandbox: %v", hydrateErr))
		switch hydrateErrCode {
		case "SNAPSHOT_EXPIRED":
			writeError(w, r, http.StatusGone, hydrateErrCode, hydrateErr.Error())
		case "NO_SANDBOX":
			writeError(w, r, http.StatusConflict, hydrateErrCode, hydrateErr.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_HYDRATE_FAILED", "failed to hydrate sandbox for preview", hydrateErr)
		}
		return
	}

	// Container id we'd need to tear down on later failures. Empty when we
	// reused an existing container — the turn still owns it.
	hydratedID := ""
	if hydrated {
		hydratedID = sb.ID
	}

	if body.Config == nil {
		// Auto-detect: first try to read .143/preview.json from the session's
		// workspace so repos with a committed config just work. Fall back to a
		// Node.js default (npm start, port 3000) only if no config is present.
		cfg, err := h.readWorkspacePreviewConfig(r.Context(), sb, sessionID)
		if err != nil {
			// Infrastructure failure (docker exec, sandbox gone, etc.) — do
			// not silently swap in Node.js defaults, which would start the
			// wrong preview for a non-Node project and time out after minutes.
			h.manager.AbortReservation(r.Context(), reservation, hydratedID, fmt.Sprintf("read workspace config: %v", err))
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_CONFIG_READ_FAILED", "failed to read preview config from workspace", err)
			return
		}
		if cfg != nil {
			input.Config = cfg
		} else {
			h.logger.Info().
				Str("session_id", sessionID.String()).
				Msg("no preview config provided or committed, using Node.js defaults (npm start, port 3000)")
			// input.Config already holds defaults from the reservation.
		}
	}
	input.Sandbox = sb

	instance, err := h.manager.LaunchPreview(r.Context(), reservation, input)
	if err != nil {
		h.manager.AbortReservation(r.Context(), reservation, hydratedID, fmt.Sprintf("launch: %v", err))
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview launch failed")
		writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", "failed to start preview")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[*models.PreviewInstance]{Data: instance})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview — Get preview status
// =============================================================================

func (h *PreviewHandler) GetPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireManager(w, r) {
		return
	}
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
	if !h.requireManager(w, r) {
		return
	}
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
	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	if err := h.manager.RecyclePreview(r.Context(), orgID, instance.ID); err != nil {
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
	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	token, err := h.manager.MintBootstrapToken(r.Context(), orgID, user.ID, instance.ID)
	if err != nil {
		h.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Msg("bootstrap token mint failed")
		writeError(w, r, http.StatusUnprocessableEntity, "BOOTSTRAP_TOKEN_FAILED", "failed to create bootstrap token")
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
	if !h.requireManager(w, r) {
		return
	}
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
	// Check for a config query parameter (base64-encoded JSON).
	configParam := r.URL.Query().Get("config")
	if configParam == "" {
		// No config provided — report not supported (full implementation would
		// read .143/preview.json from the repo via the GitHub API).
		result := models.PreviewDetectionResult{
			Readiness: models.PreviewReadinessNotSupported,
			ValidationErrors: []string{
				"no preview config provided; pass config as a base64-encoded query parameter or read .143/preview.json from the repository",
			},
		}
		writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewDetectionResult]{Data: result})
		return
	}

	// Decode the base64-encoded config JSON.
	configJSON, err := base64.RawURLEncoding.DecodeString(configParam)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "config parameter must be base64url-encoded JSON", err)
		return
	}

	cfg, err := preview.ParseConfig(configJSON)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "invalid preview configuration")
		return
	}

	result := preview.DetectReadiness(cfg)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewDetectionResult]{Data: result})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/screenshot — Capture a screenshot
// =============================================================================

type captureScreenshotRequest struct {
	Path      string `json:"path"`
	ViewportW int    `json:"viewport_w"`
	ViewportH int    `json:"viewport_h"`
	FullPage  bool   `json:"full_page"`
	DelayMS   int    `json:"delay_ms"`
}

type captureScreenshotResponse struct {
	PageTitle     string                  `json:"page_title"`
	ConsoleErrors []models.ConsoleMessage `json:"console_errors,omitempty"`
	URL           string                  `json:"url"`
	CapturedAt    time.Time               `json:"captured_at"`
	PNGBase64     string                  `json:"png_base64"`
}

func (h *PreviewHandler) CaptureScreenshot(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body captureScreenshotRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	// Cap viewport dimensions to prevent absurdly large screenshots. A 4K
	// viewport (3840x2160) is generous; larger values waste memory encoding
	// the PNG as base64 in the JSON response.
	const maxViewportDim = 3840
	opts := models.DefaultScreenshotOpts()
	if body.Path != "" {
		opts.Path = body.Path
	}
	if body.ViewportW > 0 {
		opts.ViewportW = min(body.ViewportW, maxViewportDim)
	}
	if body.ViewportH > 0 {
		opts.ViewportH = min(body.ViewportH, maxViewportDim)
	}
	opts.FullPage = body.FullPage
	if body.DelayMS > 0 {
		opts.Delay = time.Duration(body.DelayMS) * time.Millisecond
	}

	result, err := inspector.CaptureScreenshot(r.Context(), instance.ID.String(), opts)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SCREENSHOT_FAILED", "failed to capture screenshot", err)
		return
	}

	resp := captureScreenshotResponse{
		PageTitle:     result.PageTitle,
		ConsoleErrors: result.ConsoleErrors,
		URL:           result.URL,
		CapturedAt:    result.CapturedAt,
		PNGBase64:     base64.StdEncoding.EncodeToString(result.PNG),
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[captureScreenshotResponse]{Data: resp})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/inspect — Inspect a DOM element
// =============================================================================

type inspectElementRequest struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func (h *PreviewHandler) InspectElement(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body inspectElementRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	// Max coordinate is generous but prevents obviously absurd values.
	const maxCoordinate = 10000
	if body.X < 0 || body.Y < 0 || body.X > maxCoordinate || body.Y > maxCoordinate {
		writeError(w, r, http.StatusBadRequest, "INVALID_COORDINATES",
			fmt.Sprintf("x and y must be between 0 and %d", maxCoordinate))
		return
	}

	element, err := inspector.InspectElement(r.Context(), instance.ID.String(), body.X, body.Y)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INSPECT_FAILED", "failed to inspect element", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.ElementInfo]{Data: element})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/console — Read console messages
// =============================================================================

func (h *PreviewHandler) ReadConsole(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	messages, err := inspector.ReadConsole(r.Context(), instance.ID.String())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONSOLE_READ_FAILED", "failed to read console messages", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[preview.ConsoleMessage]{Data: messages})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/design-feedback — Submit design feedback
// =============================================================================

func (h *PreviewHandler) SubmitDesignFeedback(w http.ResponseWriter, r *http.Request) {
	// Design feedback is stored as a log entry — it does not require the
	// headless browser inspector. It only needs an active preview.
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body models.DesignModeFeedback
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	if body.Type == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_TYPE", "feedback type is required")
		return
	}

	// Store the design feedback as a preview log entry so it appears in the
	// session timeline and is available to the agent.
	metadata, _ := json.Marshal(body)
	log := &models.PreviewLog{
		PreviewInstanceID: instance.ID,
		OrgID:             middleware.OrgIDFromContext(r.Context()),
		Level:             "info",
		Step:              models.PreviewLogStepDesignFeedback,
		Message:           "design feedback submitted",
		Metadata:          metadata,
	}
	if err := h.store.CreatePreviewLog(r.Context(), log); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store design feedback", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{
		"status":     "accepted",
		"preview_id": instance.ID.String(),
	}})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/interact — Execute browser interactions
// =============================================================================

const (
	maxInteractionSteps    = 20
	maxInteractionDuration = 60 * time.Second
	maxAssertions          = 50
)

type executeInteractionRequest struct {
	Steps []models.InteractionStep `json:"steps"`
}

func (h *PreviewHandler) ExecuteInteraction(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body executeInteractionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	if len(body.Steps) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_STEPS", "at least one interaction step is required")
		return
	}
	if len(body.Steps) > maxInteractionSteps {
		writeError(w, r, http.StatusBadRequest, "TOO_MANY_STEPS",
			fmt.Sprintf("at most %d interaction steps allowed", maxInteractionSteps))
		return
	}

	// Enforce the max total duration per the design doc (60 seconds).
	ctx, cancel := context.WithTimeout(r.Context(), maxInteractionDuration)
	defer cancel()

	result, err := inspector.ExecuteInteraction(ctx, instance.ID.String(), body.Steps)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERACTION_FAILED", "failed to execute interaction", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.InteractionResult]{Data: result})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/multi-viewport — Multi-viewport capture
// =============================================================================

const maxViewportsPerCapture = 5

type captureMultiViewportRequest struct {
	Path      string                `json:"path"`
	Viewports []models.ViewportSpec `json:"viewports"`
	DelayMS   int                   `json:"delay_ms"`
}

func (h *PreviewHandler) CaptureMultiViewport(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body captureMultiViewportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	viewports := body.Viewports
	if len(viewports) == 0 {
		viewports = models.DefaultViewports()
	}
	if len(viewports) > maxViewportsPerCapture {
		writeError(w, r, http.StatusBadRequest, "TOO_MANY_VIEWPORTS",
			fmt.Sprintf("at most %d viewports allowed per capture", maxViewportsPerCapture))
		return
	}

	opts := models.MultiViewportOpts{
		Path:      body.Path,
		Viewports: viewports,
	}
	if opts.Path == "" {
		opts.Path = "/"
	}
	if body.DelayMS > 0 {
		opts.Delay = time.Duration(body.DelayMS) * time.Millisecond
	}

	result, err := inspector.CaptureMultiViewport(r.Context(), instance.ID.String(), opts)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MULTI_VIEWPORT_FAILED", "failed to capture multi-viewport screenshots", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.MultiViewportResult]{Data: result})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/visual-diff — Compute visual diff
// =============================================================================

type computeVisualDiffRequest struct {
	BeforeSnapshotID string `json:"before_snapshot_id"`
	AfterSnapshotID  string `json:"after_snapshot_id"`
}

func (h *PreviewHandler) ComputeVisualDiff(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body computeVisualDiffRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	if body.BeforeSnapshotID == "" || body.AfterSnapshotID == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_SNAPSHOT_IDS", "before_snapshot_id and after_snapshot_id are required")
		return
	}

	diff, err := inspector.ComputeVisualDiff(r.Context(), instance.ID.String(), body.BeforeSnapshotID, body.AfterSnapshotID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "VISUAL_DIFF_FAILED", "failed to compute visual diff", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.VisualDiff]{Data: diff})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/assert — Run visual assertions
// =============================================================================

type runAssertionsRequest struct {
	Assertions []preview.Assertion `json:"assertions"`
}

func (h *PreviewHandler) RunAssertions(w http.ResponseWriter, r *http.Request) {
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var body runAssertionsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	if len(body.Assertions) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_ASSERTIONS", "at least one assertion is required")
		return
	}
	if len(body.Assertions) > maxAssertions {
		writeError(w, r, http.StatusBadRequest, "TOO_MANY_ASSERTIONS",
			fmt.Sprintf("at most %d assertions allowed per call", maxAssertions))
		return
	}

	result, err := inspector.RunAssertions(r.Context(), instance.ID.String(), body.Assertions)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ASSERTIONS_FAILED", "failed to run assertions", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*preview.AssertionResult]{Data: result})
}
