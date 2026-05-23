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
	"github.com/assembledhq/143/internal/repoconfig"
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
	jobStore        *db.JobStore
	sessionStore    *db.SessionStore
	repoStore       *db.RepositoryStore
	fileReader      sandbox.FileReader
	sandboxProvider agent.SandboxProvider
	sandboxCapacity *agent.SandboxCapacityGate
	snapshots       storage.SnapshotStore
	workerSelector  *preview.WorkerSelector
	workerClient    *preview.WorkerPreviewClient
	localNodeID     string
	logger          zerolog.Logger
	audit           *db.AuditEmitter
}

// NewPreviewHandler creates a new PreviewHandler. fileReader is used to
// auto-detect repo preview config from the session's sandbox workspace when
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
func NewPreviewHandler(manager *preview.Manager, store *db.PreviewStore, sessionStore *db.SessionStore, repoStore *db.RepositoryStore, fileReader sandbox.FileReader, sandboxProvider agent.SandboxProvider, snapshots storage.SnapshotStore, logger zerolog.Logger) *PreviewHandler {
	return &PreviewHandler{
		manager:         manager,
		store:           store,
		jobStore:        nil,
		sessionStore:    sessionStore,
		repoStore:       repoStore,
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

// SetJobStore injects the durable job queue used for async preview startup.
func (h *PreviewHandler) SetJobStore(jobStore *db.JobStore) {
	h.jobStore = jobStore
}

// SetWorkerRuntime wires worker routing for app-node preview execution.
func (h *PreviewHandler) SetWorkerRuntime(selector *preview.WorkerSelector, client *preview.WorkerPreviewClient, localNodeID string) {
	h.workerSelector = selector
	h.workerClient = client
	h.localNodeID = localNodeID
}

// SetSandboxCapacityGate injects the local live-sandbox admission gate used
// before preview hydrate creates a container.
func (h *PreviewHandler) SetSandboxCapacityGate(gate *agent.SandboxCapacityGate) {
	h.sandboxCapacity = gate
}

type previewHTTPError struct {
	status  int
	code    string
	message string
	err     error
}

func (e *previewHTTPError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return e.err.Error()
	}
	return e.message
}

func newPreviewHTTPError(status int, code, message string, err error) *previewHTTPError {
	return &previewHTTPError{status: status, code: code, message: message, err: err}
}

func writePreviewHTTPError(w http.ResponseWriter, r *http.Request, err *previewHTTPError) {
	if err == nil {
		return
	}
	if err.err != nil {
		writeError(w, r, err.status, err.code, err.message, err.err)
		return
	}
	writeError(w, r, err.status, err.code, err.message)
}

func (h *PreviewHandler) workerRoutingEnabled() bool {
	return h.workerSelector != nil && h.workerClient != nil
}

func (h *PreviewHandler) isLocalWorker(worker preview.WorkerNode) bool {
	return worker.ID != "" && worker.ID == h.localNodeID
}

func (h *PreviewHandler) writeWorkerClientError(w http.ResponseWriter, r *http.Request, err error) {
	if reqErr, ok := preview.AsWorkerRequestError(err); ok {
		writeError(w, r, reqErr.StatusCode, reqErr.Code, reqErr.Message)
		return
	}
	writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_REQUEST_FAILED", "preview worker request failed", err)
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

func (h *PreviewHandler) resolvePreviewWorker(ctx context.Context, workerNodeID string) (preview.WorkerNode, error) {
	if h.workerSelector == nil {
		return preview.WorkerNode{}, fmt.Errorf("worker selector is not configured")
	}
	return h.workerSelector.ResolveNode(ctx, workerNodeID)
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

// readWorkspacePreviewConfig attempts to read and parse workspace preview
// config from the session's sandbox workspace using .143/config.json with a
// nested "preview" section.
// Returns:
//   - (cfg, nil)   when a valid committed config is found and parsed.
//   - (nil, nil)   for "no config to use" cases where the caller should fall
//     back to the no-config path: no fileReader wired or the file is absent.
//   - (nil, err)   for genuine infrastructure failures (docker exec failed,
//     context cancelled, sandbox gone) or invalid committed config. The caller
//     should surface these instead of reporting that the file is absent.
func (h *PreviewHandler) readWorkspacePreviewConfig(ctx context.Context, sb *agent.Sandbox, sessionID uuid.UUID) (*models.PreviewConfig, error) {
	if h.fileReader == nil {
		return nil, nil
	}
	content, _, err := h.fileReader.ReadFile(ctx, sb.ID, sb.WorkDir, repoconfig.ConfigPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrFileNotFound) {
			h.logger.Debug().
				Str("session_id", sessionID.String()).
				Str("path", repoconfig.ConfigPath).
				Msg("no committed preview config in workspace")
			return nil, nil
		}
		h.logger.Warn().
			Err(err).
			Str("session_id", sessionID.String()).
			Str("path", repoconfig.ConfigPath).
			Msg("failed to read committed preview config")
		return nil, fmt.Errorf("read %s: %w", repoconfig.ConfigPath, err)
	}
	cfg, err := preview.ParseConfig([]byte(content))
	if err != nil {
		h.logger.Warn().
			Err(err).
			Str("session_id", sessionID.String()).
			Str("path", repoconfig.ConfigPath).
			Msg("committed preview config failed to parse")
		return nil, fmt.Errorf("%w: parse %s: %w", preview.ErrInvalidConfig, repoconfig.ConfigPath, err)
	}
	h.logger.Info().
		Str("session_id", sessionID.String()).
		Str("path", repoconfig.ConfigPath).
		Msg("using preview config from workspace")
	return cfg, nil
}

// acquireSandboxResult is the structured return from acquireSandbox. Wrapping
// the four logical outputs in a struct keeps the caller's error-handling
// branches legible: you can name the fields at the call site instead of
// re-reading a four-value signature every time.
type acquireSandboxResult struct {
	// Sandbox is the live sandbox handle the preview should run against.
	// Only valid when Err == nil.
	Sandbox *agent.Sandbox
	// Hydrated is true when the sandbox was freshly created from a snapshot
	// (we own it — teardown must destroy), false when we attached to an
	// existing container (a turn still owns it — leave it alone on abort).
	Hydrated bool
	// ErrCode, when non-empty, is the HTTP error code to surface:
	// "NO_SANDBOX" (409), "SANDBOX_BUSY" (409), "SNAPSHOT_UNAVAILABLE" (409),
	// "SNAPSHOT_EXPIRED" (410). Empty for infrastructure failures that should
	// map to 500 PREVIEW_HYDRATE_FAILED.
	ErrCode string
	// Err is the underlying error for logging and user messaging. Always
	// non-nil when acquisition failed; always nil when Sandbox is non-nil.
	Err error
}

// resolveSandboxWorkDir returns the absolute path inside the sandbox where
// the session's repo is checked out. Mirrors orchestrator.go's logic
// (HomeDir + "/" + slug) so file-reads against sb.WorkDir resolve to the same
// place the agent uses. Falls back to /workspace when there's no attached
// repo or the lookup fails — losing auto-detect is preferable to refusing to
// hydrate at all.
func (h *PreviewHandler) resolveSandboxWorkDir(ctx context.Context, session *models.Session) string {
	defaults := agent.DefaultSandboxConfig()
	if session.RepositoryID == nil || h.repoStore == nil {
		return defaults.WorkDir
	}
	repo, err := h.repoStore.GetByID(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		h.logger.Warn().Err(err).
			Str("session_id", session.ID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Msg("preview: repo lookup for sandbox WorkDir failed; falling back to default")
		return defaults.WorkDir
	}
	slug := agent.SlugForRepo(repo.FullName)
	if slug == "" {
		return defaults.WorkDir
	}
	return defaults.HomeDir + "/" + slug
}

// acquireSandbox resolves a live sandbox for a preview start, picking between
// three strategies:
//   - Reuse: session.ContainerID is set; attach by ID.
//   - Hydrate: session has a snapshot and the sandbox was torn down; create a
//     new container, restore the snapshot, and publish the new container_id.
//   - Expired/Unavailable: no container and no usable snapshot; caller should
//     return 410 only when the reaper explicitly expired the snapshot.
func (h *PreviewHandler) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session, cfg *models.PreviewConfig) acquireSandboxResult {
	workDir := h.resolveSandboxWorkDir(ctx, session)

	// Reuse is only safe when the row believes the container is actually
	// running. A lingering container_id from a crashed worker or a session
	// whose sandbox_state has since moved to 'snapshotted'/'destroyed' should
	// fall through to hydrate/expired instead of attaching to a dead ID.
	if session.ContainerID != nil && *session.ContainerID != "" &&
		session.SandboxState == models.SandboxStateRunning {
		candidate := &agent.Sandbox{
			ID:        *session.ContainerID,
			Provider:  "docker",
			WorkDir:   workDir,
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
				return acquireSandboxResult{Sandbox: candidate}
			} else {
				h.logger.Info().
					Str("session_id", session.ID.String()).
					Str("container_id", candidate.ID).
					Msg("preview reuse: recorded container no longer exists; falling through to hydrate")
			}
		} else {
			// No provider wired (e.g., cold handler): trust the row.
			return acquireSandboxResult{Sandbox: candidate}
		}
	}

	// No live container. Check whether we can hydrate one from a snapshot.
	if session.SandboxState == models.SandboxStateDestroyed {
		return acquireSandboxResult{
			ErrCode: "SNAPSHOT_EXPIRED",
			Err:     fmt.Errorf("this session's sandbox snapshot has expired; send a new message to rebuild it"),
		}
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return acquireSandboxResult{
			ErrCode: "SNAPSHOT_UNAVAILABLE",
			Err:     fmt.Errorf("session has no live sandbox and no saved snapshot; send a new message to rebuild it"),
		}
	}
	if h.sandboxProvider == nil || h.snapshots == nil {
		return acquireSandboxResult{
			ErrCode: "NO_SANDBOX",
			Err:     fmt.Errorf("preview hydrate is not configured on this worker"),
		}
	}

	// Hydrate: build a SandboxConfig matching what the orchestrator uses so
	// the restored container has consistent resource limits and paths.
	// WorkDir resolves from the session's repo (HomeDir + "/" + slug) so
	// downstream sandbox commands land in the same path the orchestrator uses.
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = workDir
	sandboxCfg.SessionID = session.ID.String()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.Purpose = "preview_hydrate"
	preview.ApplyResourceLimitsToSandboxConfig(&sandboxCfg, cfg)

	// Pre-hydrate race check: re-read just container_id and bail early if a
	// peer (typically a continue_session turn) has published one since we
	// read `session` at the top of startPreviewLocal. This is a *latency*
	// optimization layered on top of clearWriteDeadline (StartPreview):
	// the deadline fix already prevents the slow path's 502 EOF, so the
	// CAS inside PublishHydratedContainerID is sufficient for correctness.
	// The peek's value is sub-100ms user feedback and avoiding ~20s of
	// pointless snapshot restore + container create + destroy churn when
	// we already know we'll lose.
	//
	// Only container_id is rechecked: sandbox_state and snapshot_key from
	// the original `session` row are still trusted. A reaper expiring the
	// snapshot in this window would slip past the peek and fail in
	// HydrateSandboxFromSnapshot below — same behavior as before this peek
	// existed, so out of scope for this fix.
	winningID, freshErr := h.sessionStore.PeekContainerID(ctx, orgID, session.ID)
	switch {
	case freshErr != nil:
		// Fail open: the CAS in PublishHydratedContainerID still catches the
		// race after restore. Log so a regression in the optimization is
		// visible in prod (e.g. DB blips making us silently fall back to
		// the slow path).
		h.logger.Warn().Err(freshErr).
			Str("session_id", session.ID.String()).
			Msg("preview hydrate: pre-hydrate peek failed; falling through to CAS race detection")
	case winningID != "":
		h.logger.Info().
			Str("session_id", session.ID.String()).
			Str("winning_container_id", winningID).
			Msg("preview hydrate: peer published container_id before restore; returning SANDBOX_BUSY without hydrating")
		return acquireSandboxResult{
			ErrCode: "SANDBOX_BUSY",
			Err:     fmt.Errorf("another process attached to this session's sandbox first; please retry"),
		}
	}

	var capacityReservation *agent.SandboxCapacityReservation
	if h.sandboxCapacity != nil {
		var capErr error
		capacityReservation, capErr = h.sandboxCapacity.Acquire(ctx, agent.SandboxCapacityRequest{
			Purpose:   sandboxCfg.Purpose,
			SessionID: sandboxCfg.SessionID,
			OrgID:     sandboxCfg.OrgID,
		})
		if capErr != nil {
			return acquireSandboxResult{
				ErrCode: preview.PreviewCapacityCode,
				Err:     fmt.Errorf("%w: %w", preview.ErrPreviewCapacity, capErr),
			}
		}
		defer capacityReservation.Release()
	}

	sandbox, err := agent.HydrateSandboxFromSnapshot(ctx, h.sandboxProvider, h.snapshots, *session.SnapshotKey, sandboxCfg)
	if capacityReservation != nil {
		capacityReservation.Release()
	}
	if err != nil {
		// The storage layer found no blob at the recorded snapshot key — the
		// DB row is out of sync with the object store (reaped out-of-band,
		// deleted manually, or never saved). Surface this distinctly from true
		// retention expiry so the UI can explain that the session can be
		// rebuilt, but not restored.
		if errors.Is(err, agent.ErrSnapshotMissing) {
			return acquireSandboxResult{
				ErrCode: "SNAPSHOT_UNAVAILABLE",
				Err:     fmt.Errorf("session snapshot is unavailable in storage; send a new message to rebuild it"),
			}
		}
		return acquireSandboxResult{Err: fmt.Errorf("hydrate sandbox: %w", err)}
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
		return acquireSandboxResult{Err: fmt.Errorf("publish container id: %w", err)}
	}
	if actualID != sandbox.ID {
		_ = h.sandboxProvider.Destroy(context.Background(), sandbox)
		h.logger.Warn().
			Str("session_id", session.ID.String()).
			Str("winning_container_id", actualID).
			Str("losing_container_id", sandbox.ID).
			Msg("preview hydrate lost race to another holder; destroyed local container")
		return acquireSandboxResult{
			ErrCode: "SANDBOX_BUSY",
			Err:     fmt.Errorf("another process attached to this session's sandbox first; please retry"),
		}
	}

	h.logger.Info().
		Str("session_id", session.ID.String()).
		Str("container_id", sandbox.ID).
		Msg("preview hydrate: new sandbox container created from snapshot")

	return acquireSandboxResult{Sandbox: sandbox, Hydrated: true}
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

// classifyLaunchError converts a preview-launch error into the most specific
// HTTP error code we can. Before this existed, every launch failure surfaced
// as a generic 422 PREVIEW_START_FAILED with the message "failed to start
// preview" — the actual cause (missing image, unhealthy container, init
// script crash, readiness timeout) was logged but never reached the user, so
// the frontend rendered the unhelpful "Failed to start preview: failed to
// start preview".
//
// We pick out the provider sentinels from internal/services/preview/errors.go
// and map each to a stable, debuggable error code. The user-visible message
// always includes the underlying cause so an operator can act on it without
// digging through Grafana.
func classifyLaunchError(err error) *previewHTTPError {
	if err == nil {
		return nil
	}
	classified := preview.ClassifyLaunchFailure(err)
	return newPreviewHTTPError(http.StatusUnprocessableEntity, classified.Code, classified.Message, err)
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview — Start a preview
// =============================================================================

type startPreviewRequest struct {
	Config        *models.PreviewConfig `json:"config"`
	BaseCommitSHA string                `json:"base_commit_sha"`
	ProfileName   string                `json:"profile_name"`
}

// reservationPlaceholderConfig returns a minimal valid config used solely to
// satisfy ValidateConfig at reservation time when the client hasn't supplied
// one. The real workspace repo config is loaded after hydrate
// and either replaces this placeholder or causes the reservation to abort
// with PREVIEW_NO_CONFIG. This config is never executed.
func reservationPlaceholderConfig() *models.PreviewConfig {
	return &models.PreviewConfig{
		Name:    "placeholder",
		Primary: "app",
		Services: map[string]models.ServiceConfig{
			"app": {
				Command: []string{"true"},
				Port:    3000,
				Ready: models.ReadinessProbe{
					HTTPPath: "/",
				},
			},
		},
	}
}

func (h *PreviewHandler) decodeStartPreviewBody(r *http.Request) (startPreviewRequest, *previewHTTPError) {
	var body startPreviewRequest
	// Tolerate empty body (e.g., frontend sends no config when auto-detecting).
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return body, newPreviewHTTPError(http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		}
	}
	return body, nil
}

func (h *PreviewHandler) enqueueStartPreviewJob(ctx context.Context, orgID, userID uuid.UUID, session models.Session, worker preview.WorkerNode, body startPreviewRequest) (*models.PreviewInstance, *previewHTTPError) {
	if h.jobStore == nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_QUEUE_UNAVAILABLE", "preview start queue is not configured", nil)
	}
	initialConfig := body.Config
	if initialConfig == nil {
		initialConfig = reservationPlaceholderConfig()
	}
	input := preview.StartPreviewInput{
		SessionID:     session.ID,
		OrgID:         orgID,
		UserID:        userID,
		Config:        initialConfig,
		BaseCommitSHA: body.BaseCommitSHA,
		ProfileName:   body.ProfileName,
	}

	tx, err := h.store.Begin(ctx)
	if err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_FAILED", "failed to start preview", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reservation, err := h.manager.ReservePreviewForWorkerInTx(ctx, tx, input, worker.ID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("preview reserve failed")
		if errors.Is(err, preview.ErrPreviewCapacity) {
			return nil, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, preview.PreviewCapacityMessage, err)
		}
		if errors.Is(err, preview.ErrInvalidConfig) {
			return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_CONFIG_INVALID", preview.InvalidConfigMessage(err), err)
		}
		return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", "failed to start preview", err)
	}

	dedupeKey := "start_preview:" + session.ID.String()
	targetNodeID := worker.ID
	jobID, err := h.jobStore.EnqueueInTxWithOpts(ctx, tx, orgID, db.EnqueueOpts{
		Queue:   "preview",
		JobType: models.JobTypeStartPreview,
		Payload: preview.StartPreviewJobPayload{
			OrgID:         orgID,
			UserID:        userID,
			SessionID:     session.ID,
			PreviewID:     reservation.ID,
			Config:        body.Config,
			BaseCommitSHA: body.BaseCommitSHA,
			ProfileName:   body.ProfileName,
		},
		Priority:     5,
		DedupeKey:    &dedupeKey,
		TargetNodeID: &targetNodeID,
	})
	if err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_ENQUEUE_FAILED", "failed to enqueue preview startup", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_FAILED", "failed to start preview", err)
	}
	h.jobStore.Notify(ctx, jobID)
	return reservation, nil
}

func (h *PreviewHandler) startPreviewLocal(ctx context.Context, orgID, userID, sessionID uuid.UUID, body startPreviewRequest) (*models.PreviewInstance, *previewHTTPError) {
	// Look up the session to get its sandbox container.
	session, err := h.sessionStore.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return nil, newPreviewHTTPError(http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", err)
	}

	// Reserve the preview BEFORE touching docker. This (a) short-circuits
	// capacity / existing-preview failures so a 503 can never leave a hydrated
	// container behind, and (b) acquires preview_holding_container=TRUE before
	// hydrate publishes container_id, so a concurrent turn release's
	// FinalizeContainerDestroy sees our hold and leaves the freshly-hydrated
	// container alone. When the client didn't supply a config we reserve with
	// a benign placeholder solely to satisfy ValidateConfig; the real config
	// is loaded post-hydrate from the workspace and either replaces the
	// placeholder before LaunchPreview or aborts the reservation with
	// PREVIEW_NO_CONFIG. The placeholder is never executed.
	initialConfig := body.Config
	if initialConfig == nil {
		initialConfig = reservationPlaceholderConfig()
	}
	input := preview.StartPreviewInput{
		SessionID:     sessionID,
		OrgID:         orgID,
		UserID:        userID,
		Config:        initialConfig,
		BaseCommitSHA: body.BaseCommitSHA,
		ProfileName:   body.ProfileName,
	}
	reservation, err := h.manager.ReservePreview(ctx, input)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview reserve failed")
		if errors.Is(err, preview.ErrPreviewCapacity) {
			return nil, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, preview.PreviewCapacityMessage, err)
		}
		if errors.Is(err, preview.ErrInvalidConfig) {
			return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_CONFIG_INVALID", preview.InvalidConfigMessage(err), err)
		}
		return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", "failed to start preview", err)
	}

	// Three paths to a live sandbox, in preference order:
	//   1. Reuse — attach to an existing container (a turn is running or a
	//      prior preview already hydrated it).
	//   2. Hydrate — the container has been torn down but a snapshot exists;
	//      create a new container and restore the snapshot.
	//   3. SnapshotExpired / SnapshotUnavailable — neither a container nor a
	//      usable snapshot exists.
	acq := h.acquireSandbox(ctx, orgID, &session, input.Config)
	if acq.Err != nil {
		h.logger.Warn().Err(acq.Err).
			Str("session_id", sessionID.String()).
			Str("error_code", acq.ErrCode).
			Msg("preview start: failed to acquire sandbox")
		// hydratedContainerID is "" — either we never hydrated, or
		// acquireSandbox's race-loss branch already destroyed the local
		// container before returning.
		abortReason := fmt.Sprintf("acquire sandbox: %v", acq.Err)
		if errors.Is(acq.Err, preview.ErrPreviewCapacity) {
			abortReason = preview.PreviewCapacityMessage
		}
		h.manager.AbortReservation(ctx, reservation, "", abortReason)
		switch acq.ErrCode {
		case "SNAPSHOT_EXPIRED":
			return nil, newPreviewHTTPError(http.StatusGone, acq.ErrCode, acq.Err.Error(), acq.Err)
		case "SNAPSHOT_UNAVAILABLE":
			return nil, newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
		case "NO_SANDBOX":
			return nil, newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
		case "SANDBOX_BUSY":
			return nil, newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
		case preview.PreviewCapacityCode:
			return nil, newPreviewHTTPError(http.StatusServiceUnavailable, acq.ErrCode, preview.PreviewCapacityMessage, acq.Err)
		default:
			return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_HYDRATE_FAILED", "failed to hydrate sandbox for preview", acq.Err)
		}
	}
	sb := acq.Sandbox

	// Container id we'd need to tear down on later failures. Empty when we
	// reused an existing container — the turn still owns it.
	hydratedID := ""
	if acq.Hydrated {
		hydratedID = sb.ID
	}

	if body.Config == nil {
		// Auto-detect: read preview config from the session's workspace.
		// We deliberately do NOT fall back to a generic "npm start on :3000"
		// default — for any repo without that file, that fallback exits within
		// seconds and the user waits ~90s for the readiness probe to give up.
		// Returning a clear PREVIEW_NO_CONFIG error is strictly more useful.
		cfg, err := h.readWorkspacePreviewConfig(ctx, sb, sessionID)
		if err != nil {
			if errors.Is(err, preview.ErrInvalidConfig) {
				msg := preview.InvalidConfigMessage(err)
				h.manager.AbortReservation(ctx, reservation, hydratedID, msg)
				return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_CONFIG_INVALID", msg, err)
			}
			h.manager.AbortReservation(ctx, reservation, hydratedID, fmt.Sprintf("read workspace config: %v", err))
			return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_CONFIG_READ_FAILED", "failed to read preview config from workspace", err)
		}
		if cfg == nil {
			h.manager.AbortReservation(ctx, reservation, hydratedID, "no committed preview config")
			return nil, newPreviewHTTPError(
				http.StatusUnprocessableEntity,
				"PREVIEW_NO_CONFIG",
				"This repo has no .143/config.json committed with a preview section. Add one (see docs/guides/previews.md) so the preview knows what command to run.",
				nil,
			)
		}
		input.Config = cfg
	}
	input.Sandbox = sb

	instance, err := h.manager.LaunchPreview(ctx, reservation, input)
	if err != nil {
		h.manager.AbortReservation(ctx, reservation, hydratedID, fmt.Sprintf("launch: %v", err))
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview launch failed")
		return nil, classifyLaunchError(err)
	}

	if h.localNodeID != "" && (acq.Hydrated || (session.ContainerID != nil && *session.ContainerID != "")) {
		containerID := sb.ID
		if err := h.sessionStore.SetWorkerNodeIDForContainer(ctx, orgID, sessionID, containerID, h.localNodeID); err != nil {
			h.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Str("container_id", containerID).
				Str("worker_node_id", h.localNodeID).
				Msg("failed to persist session worker ownership")
		}
	}

	return instance, nil
}

func (h *PreviewHandler) StartPreview(w http.ResponseWriter, r *http.Request) {
	// Preview start can take ≫15s (snapshot restore + infra image pull +
	// readiness probes). Clear the per-request write deadline so the
	// server's 15s WriteTimeout doesn't kill the connection mid-handler
	// and turn a real error code into a 502 EOF.
	clearWriteDeadline(w, r)

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

	body, reqErr := h.decodeStartPreviewBody(r)
	if reqErr != nil {
		writePreviewHTTPError(w, r, reqErr)
		return
	}

	if !h.workerRoutingEnabled() {
		instance, localErr := h.startPreviewLocal(r.Context(), orgID, user.ID, sessionID, body)
		if localErr != nil {
			writePreviewHTTPError(w, r, localErr)
			return
		}
		writeJSON(w, http.StatusCreated, models.SingleResponse[*models.PreviewInstance]{Data: instance})
		return
	}

	session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "session not found")
		return
	}
	worker, err := h.workerSelector.SelectStartNode(r.Context(), orgID, &session)
	if err != nil {
		switch {
		case errors.Is(err, preview.ErrLegacySessionWorkerOwnership):
			writeError(w, r, http.StatusConflict, "PREVIEW_WORKER_OWNERSHIP_REQUIRED", "live sandbox is missing worker ownership metadata; send a new message to rebuild it")
		case errors.Is(err, preview.ErrNoPreviewWorkers):
			writeError(w, r, http.StatusServiceUnavailable, "PREVIEW_NO_WORKERS", "no preview-capable workers are available")
		default:
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_WORKER_SELECTION_FAILED", "failed to select preview worker", err)
		}
		return
	}
	instance, asyncErr := h.enqueueStartPreviewJob(r.Context(), orgID, user.ID, session, worker, body)
	if asyncErr != nil {
		writePreviewHTTPError(w, r, asyncErr)
		return
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[*models.PreviewInstance]{Data: instance})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview — Get preview status
// =============================================================================

func (h *PreviewHandler) GetPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return
	}
	instance, err := h.store.GetActivePreviewForSession(r.Context(), orgID, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
			return
		}
		instance, err = h.store.GetLatestTerminalPreviewForSession(r.Context(), orgID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "NO_ACTIVE_PREVIEW", "no active preview for this session")
			} else {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
			}
			return
		}
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

	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
			return
		}
		if h.isLocalWorker(worker) {
			if err := h.manager.StopPreview(r.Context(), orgID, instance.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
				return
			}
		} else {
			if err := h.workerClient.StopPreview(r.Context(), worker, orgID, instance.ID); err != nil {
				h.writeWorkerClientError(w, r, err)
				return
			}
		}
	} else {
		if err := h.manager.StopPreview(r.Context(), orgID, instance.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/restart — Restart a preview
// =============================================================================

func (h *PreviewHandler) RestartPreview(w http.ResponseWriter, r *http.Request) {
	// Recycle tears down + relaunches; same WriteTimeout-overrun risk as
	// StartPreview (image pulls + readiness probes), so clear the deadline.
	clearWriteDeadline(w, r)

	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
			return
		}
		if h.isLocalWorker(worker) {
			if err := h.manager.RecyclePreview(r.Context(), orgID, instance.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_RESTART_FAILED", "failed to restart preview", err)
				return
			}
		} else {
			if err := h.workerClient.RecyclePreview(r.Context(), worker, orgID, instance.ID); err != nil {
				h.writeWorkerClientError(w, r, err)
				return
			}
		}
	} else {
		if err := h.manager.RecyclePreview(r.Context(), orgID, instance.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_RESTART_FAILED", "failed to restart preview", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/logs — Get preview logs
// =============================================================================

func (h *PreviewHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return
	}

	instance, err := h.store.GetActivePreviewForSession(r.Context(), orgID, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
			return
		}
		instance, err = h.store.GetLatestFailedPreviewForSession(r.Context(), orgID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "NO_ACTIVE_PREVIEW", "no active preview for this session")
			} else {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
			}
			return
		}
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
// PATCH /api/v1/sessions/{id}/preview/lifetime — Set preview lifetime
// =============================================================================

type setPreviewLifetimeRequest struct {
	DurationSeconds int64 `json:"duration_seconds"`
}

func (h *PreviewHandler) SetLifetime(w http.ResponseWriter, r *http.Request) {
	if !h.requireManager(w, r) {
		return
	}

	var body setPreviewLifetimeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}

	minSeconds := int64(preview.MinLifetimeTTL.Seconds())
	maxSeconds := int64(preview.DefaultHardTTL.Seconds())
	if body.DurationSeconds < minSeconds || body.DurationSeconds > maxSeconds {
		writeError(w, r, http.StatusBadRequest, "INVALID_DURATION",
			fmt.Sprintf("duration_seconds must be between %d and %d", minSeconds, maxSeconds))
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	previousExpiry := instance.ExpiresAt
	newExpiry, err := h.manager.SetLifetime(r.Context(), orgID, instance.ID, time.Duration(body.DurationSeconds)*time.Second)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SET_PREVIEW_LIFETIME_FAILED", "failed to set preview lifetime", err)
		return
	}

	resourceID := instance.SessionID.String()
	details, err := json.Marshal(map[string]any{
		"preview_id":          instance.ID.String(),
		"previous_expires_at": previousExpiry,
		"new_expires_at":      newExpiry,
		"duration_seconds":    body.DurationSeconds,
		"direction":           previewLifetimeDirection(previousExpiry, newExpiry),
	})
	if err != nil {
		h.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to marshal preview lifetime audit details")
		details = json.RawMessage(`{}`)
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionPreviewLifetimeSet, models.AuditResourceSession, &resourceID, &instance.SessionID, nil, details)

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{
		"status":     "updated",
		"expires_at": newExpiry,
	}})
}

func previewLifetimeDirection(previous, next time.Time) string {
	switch {
	case next.After(previous):
		return "extended"
	case next.Before(previous):
		return "shortened"
	default:
		return "unchanged"
	}
}

// =============================================================================
// GET /api/v1/repos/{owner}/{repo}/preview/detect — Detect preview readiness
// =============================================================================

func (h *PreviewHandler) DetectReadiness(w http.ResponseWriter, r *http.Request) {
	// Check for a config query parameter (base64-encoded JSON).
	configParam := r.URL.Query().Get("config")
	if configParam == "" {
		// No config provided — report not supported (full implementation would
		// read repo preview config from the repo via the GitHub API).
		result := models.PreviewDetectionResult{
			Readiness: models.PreviewReadinessNotSupported,
			ValidationErrors: []string{
				"no preview config provided; pass config as a base64-encoded query parameter or read .143/config.json from the repository",
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var result *models.ScreenshotResult
	var err error
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			result, err = inspector.CaptureScreenshot(r.Context(), instance.ID.String(), opts)
		} else {
			result, err = h.workerClient.CaptureScreenshot(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, opts)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		result, err = inspector.CaptureScreenshot(r.Context(), instance.ID.String(), opts)
	}
	if err != nil {
		if h.workerRoutingEnabled() {
			if _, ok := preview.AsWorkerRequestError(err); ok {
				h.writeWorkerClientError(w, r, err)
				return
			}
		}
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var element *models.ElementInfo
	var err error
	if h.workerRoutingEnabled() {
		var worker preview.WorkerNode
		worker, err = h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			element, err = inspector.InspectElement(r.Context(), instance.ID.String(), body.X, body.Y)
		} else {
			element, err = h.workerClient.InspectElement(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.X, body.Y)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		element, err = inspector.InspectElement(r.Context(), instance.ID.String(), body.X, body.Y)
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INSPECT_FAILED", "failed to inspect element", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.ElementInfo]{Data: element})
}

// =============================================================================
// GET /api/v1/sessions/{id}/preview/console — Read console messages
// =============================================================================

func (h *PreviewHandler) ReadConsole(w http.ResponseWriter, r *http.Request) {
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
	}
	instance, ok := h.getActivePreview(w, r)
	if !ok {
		return
	}

	var messages []preview.ConsoleMessage
	var err error
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			messages, err = inspector.ReadConsole(r.Context(), instance.ID.String())
		} else {
			messages, err = h.workerClient.ReadConsole(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		messages, err = inspector.ReadConsole(r.Context(), instance.ID.String())
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var result *models.InteractionResult
	var err error
	if h.workerRoutingEnabled() {
		var worker preview.WorkerNode
		worker, err = h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			result, err = inspector.ExecuteInteraction(ctx, instance.ID.String(), body.Steps)
		} else {
			result, err = h.workerClient.ExecuteInteraction(ctx, worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.Steps)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		result, err = inspector.ExecuteInteraction(ctx, instance.ID.String(), body.Steps)
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var (
		result *models.MultiViewportResult
		err    error
	)
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			result, err = inspector.CaptureMultiViewport(r.Context(), instance.ID.String(), opts)
		} else {
			result, err = h.workerClient.CaptureMultiViewport(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, opts)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		result, err = inspector.CaptureMultiViewport(r.Context(), instance.ID.String(), opts)
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var (
		diff *models.VisualDiff
		err  error
	)
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			diff, err = inspector.ComputeVisualDiff(r.Context(), instance.ID.String(), body.BeforeSnapshotID, body.AfterSnapshotID)
		} else {
			diff, err = h.workerClient.ComputeVisualDiff(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.BeforeSnapshotID, body.AfterSnapshotID)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		diff, err = inspector.ComputeVisualDiff(r.Context(), instance.ID.String(), body.BeforeSnapshotID, body.AfterSnapshotID)
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
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
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
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

	var (
		result *preview.AssertionResult
		err    error
	)
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) {
			inspector, inspectorOK := h.requireInspector(w, r)
			if !inspectorOK {
				return
			}
			result, err = inspector.RunAssertions(r.Context(), instance.ID.String(), body.Assertions)
		} else {
			result, err = h.workerClient.RunAssertions(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.Assertions)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		result, err = inspector.RunAssertions(r.Context(), instance.ID.String(), body.Assertions)
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ASSERTIONS_FAILED", "failed to run assertions", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*preview.AssertionResult]{Data: result})
}
