package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
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

// sandboxBusyAcquireRetries and sandboxBusyAcquireRetryDelay bound the
// in-process retry loop when a preview start loses the sandbox attach race.
// The winner needs a moment to finish wiring its container (network attach,
// auth socket) before the reuse path's liveness check accepts it; three
// 2-second retries cover that window without stretching the HTTP request
// noticeably against the launch that follows.
const (
	sandboxBusyAcquireRetries    = 3
	sandboxBusyAcquireRetryDelay = 2 * time.Second
)

// PreviewHandler handles all preview-related HTTP endpoints.
type PreviewHandler struct {
	manager           *preview.Manager
	store             *db.PreviewStore
	jobStore          *db.JobStore
	sessionStore      *db.SessionStore
	orgStore          agent.OrgSettingsReader
	repoStore         *db.RepositoryStore
	fileReader        sandbox.FileReader
	sandboxProvider   agent.SandboxProvider
	sandboxCapacity   *agent.SandboxCapacityGate
	staticEgress      agent.StaticEgressRuntimeConfig
	snapshots         storage.SnapshotStore
	uploads           storage.UploadStore
	workerSelector    *preview.WorkerSelector
	workerClient      *preview.WorkerPreviewClient
	localNodeID       string
	restartClassifier preview.PreviewRestartClassifier
	logger            zerolog.Logger
	audit             *db.AuditEmitter
	browserSessions   *preview.BrowserSessionService

	// sandboxBusyRetryDelay overrides sandboxBusyAcquireRetryDelay in tests;
	// zero means the production default.
	sandboxBusyRetryDelay time.Duration
}

func (h *PreviewHandler) SetBrowserSessionService(service *preview.BrowserSessionService) {
	h.browserSessions = service
}

// SetStaticEgressRuntime injects the worker-local static egress runtime for
// local preview hydration.
func (h *PreviewHandler) SetStaticEgressRuntime(orgs agent.OrgSettingsReader, runtime agent.StaticEgressRuntimeConfig) {
	h.orgStore = orgs
	h.staticEgress = runtime
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
		manager:           manager,
		store:             store,
		jobStore:          nil,
		sessionStore:      sessionStore,
		repoStore:         repoStore,
		fileReader:        fileReader,
		sandboxProvider:   sandboxProvider,
		snapshots:         snapshots,
		restartClassifier: preview.DefaultPreviewRestartClassifier{},
		logger:            logger,
	}
}

// SetAuditEmitter injects the audit emitter for logging preview events.
func (h *PreviewHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetUploadStore injects the upload store used for user-visible preview tool
// artifacts such as screenshots.
func (h *PreviewHandler) SetUploadStore(store storage.UploadStore) {
	h.uploads = store
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

type ensurePreviewResponse struct {
	Action         string                              `json:"action"`
	Instance       *models.PreviewInstance             `json:"instance"`
	PreviewURL     string                              `json:"preview_url,omitempty"`
	BrowserContext *models.PreviewBrowserContextStatus `json:"browser_context,omitempty"`
}

func (h *PreviewHandler) ensurePreviewResponse(ctx context.Context, orgID uuid.UUID, action string, instance *models.PreviewInstance) ensurePreviewResponse {
	resp := ensurePreviewResponse{Action: action, Instance: instance}
	if h.manager != nil && instance != nil {
		if status, err := h.manager.GetStatus(ctx, orgID, instance.ID); err == nil && status != nil {
			resp.PreviewURL = status.PreviewOrigin
		}
	}
	if h.browserSessions != nil && instance != nil && instance.SessionID != uuid.Nil {
		status, err := h.browserSessions.EnsureIdentity(ctx, orgID, instance.SessionID, instance.ID, browserPolicyForInstance(instance))
		if err == nil {
			resp.BrowserContext = status
		} else {
			h.logger.Warn().Err(err).Str("session_id", instance.SessionID.String()).Msg("failed to ensure session browser identity")
		}
	}
	return resp
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

func previewCapacityMessage(err error) string {
	var capacityErr *preview.CapacityError
	if errors.As(err, &capacityErr) {
		return capacityErr.UserMessage()
	}
	return preview.PreviewCapacityMessage
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
	writePreviewHTTPError(w, r, workerClientHTTPError(err))
}

func workerClientHTTPError(err error) *previewHTTPError {
	if reqErr, ok := preview.AsWorkerRequestError(err); ok {
		return newPreviewHTTPError(reqErr.StatusCode, reqErr.Code, reqErr.Message, nil)
	}
	return newPreviewHTTPError(http.StatusBadGateway, "PREVIEW_WORKER_REQUEST_FAILED", "preview worker request failed", err)
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

func (h *PreviewHandler) getPreviewTarget(w http.ResponseWriter, r *http.Request) (*models.PreviewInstance, bool) {
	if previewIDParam := chi.URLParam(r, "preview_id"); previewIDParam != "" {
		previewID, err := uuid.Parse(previewIDParam)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_ID", "invalid preview id")
			return nil, false
		}
		instance, err := h.store.GetPreviewInstance(r.Context(), middleware.OrgIDFromContext(r.Context()), previewID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found")
			} else {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
			}
			return nil, false
		}
		return instance, true
	}
	return h.getActivePreview(w, r)
}

func (h *PreviewHandler) lookupActivePreviewForRequest(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID) (*models.PreviewInstance, *previewHTTPError) {
	instance, err := h.store.GetActivePreviewForSession(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
	}
	return instance, nil
}

func parsePreviewSessionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID")
		return uuid.Nil, false
	}
	return sessionID, true
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
func (h *PreviewHandler) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session, reservation *models.PreviewInstance) acquireSandboxResult {
	workDir := h.resolveSandboxWorkDir(ctx, session)
	expectedNetwork, expectedErr := agent.ExpectedSandboxNetwork(ctx, h.orgStore, orgID, h.staticEgress)
	if expectedErr != nil {
		return acquireSandboxResult{ErrCode: "STATIC_EGRESS_UNAVAILABLE", Err: expectedErr}
	}

	// Reuse is only safe when the row believes the container is actually
	// running. A lingering container_id from a crashed worker or a session
	// whose sandbox_state has since moved to 'snapshotted'/'destroyed' should
	// fall through to hydrate/expired instead of attaching to a dead ID.
	if session.ContainerID != nil && *session.ContainerID != "" &&
		session.SandboxState == models.SandboxStateRunning {
		// HomeDir matches the orchestrator/hydrate path so the home-rooted
		// package-manager cache (npm's ~/.npm, etc.) can restore. Without it,
		// reused session sandboxes fail package-manager cache restore with
		// "sandbox home dir is required" and re-download deps every launch.
		candidate := &agent.Sandbox{
			ID:        *session.ContainerID,
			Provider:  "docker",
			WorkDir:   workDir,
			HomeDir:   agent.DefaultSandboxConfig().HomeDir,
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
				if match, mismatchErr := agent.SandboxNetworkMatches(ctx, h.sandboxProvider, candidate, expectedNetwork, h.staticEgress.NetworkName); mismatchErr != nil {
					h.logger.Warn().Err(mismatchErr).
						Str("session_id", session.ID.String()).
						Str("container_id", candidate.ID).
						Msg("preview reuse: network check failed; falling through to hydrate")
				} else if !match {
					return acquireSandboxResult{ErrCode: "NETWORK_SETTING_RESTART_REQUIRED", Err: fmt.Errorf("restart environment to apply network setting")}
				} else {
					return acquireSandboxResult{Sandbox: candidate}
				}
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
	preview.ApplyPreviewInstanceResourceLimitsToSandboxConfig(&sandboxCfg, reservation)
	if err := agent.ApplyOrgSandboxNetworkSettings(ctx, h.orgStore, orgID, h.staticEgress, &sandboxCfg); err != nil {
		return acquireSandboxResult{
			ErrCode: "STATIC_EGRESS_UNAVAILABLE",
			Err:     err,
		}
	}

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
func classifyLaunchError(err error, memoryLimitMB int) *previewHTTPError {
	if err == nil {
		return nil
	}
	classified := preview.ClassifyLaunchFailure(err, memoryLimitMB)
	return newPreviewHTTPError(http.StatusUnprocessableEntity, classified.Code, classified.Message, err)
}

func classifyAcquireSandboxError(acq acquireSandboxResult) *previewHTTPError {
	switch acq.ErrCode {
	case "SNAPSHOT_EXPIRED":
		return newPreviewHTTPError(http.StatusGone, acq.ErrCode, acq.Err.Error(), acq.Err)
	case "SNAPSHOT_UNAVAILABLE":
		return newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
	case "NO_SANDBOX":
		return newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
	case "SANDBOX_BUSY":
		return newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
	case "NETWORK_SETTING_RESTART_REQUIRED":
		return newPreviewHTTPError(http.StatusConflict, acq.ErrCode, acq.Err.Error(), acq.Err)
	case "STATIC_EGRESS_UNAVAILABLE":
		return newPreviewHTTPError(http.StatusServiceUnavailable, acq.ErrCode, acq.Err.Error(), acq.Err)
	case preview.PreviewCapacityCode:
		return newPreviewHTTPError(http.StatusServiceUnavailable, acq.ErrCode, preview.PreviewCapacityMessage, acq.Err)
	default:
		return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_HYDRATE_FAILED", "failed to hydrate sandbox for preview", acq.Err)
	}
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

func uuidValue(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
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
		SessionID:                  session.ID,
		OrgID:                      orgID,
		UserID:                     userID,
		Config:                     initialConfig,
		RepositoryID:               uuidValue(session.RepositoryID),
		BaseCommitSHA:              body.BaseCommitSHA,
		ProfileName:                body.ProfileName,
		WorkspaceRevision:          session.WorkspaceRevision,
		WorkspaceRevisionUpdatedAt: session.WorkspaceRevisionUpdatedAt,
	}

	tx, err := h.store.Begin(ctx)
	if err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_FAILED", "failed to start preview", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reservation, err := h.manager.ReservePreviewForWorkerInTx(ctx, tx, input, worker.ID, worker.BaseURL)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("preview reserve failed")
		if errors.Is(err, preview.ErrPreviewCapacity) {
			return nil, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, previewCapacityMessage(err), err)
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
			OrgID:                      orgID,
			UserID:                     userID,
			SessionID:                  session.ID,
			PreviewID:                  reservation.ID,
			Config:                     body.Config,
			BaseCommitSHA:              body.BaseCommitSHA,
			ProfileName:                body.ProfileName,
			WorkspaceRevision:          session.WorkspaceRevision,
			WorkspaceRevisionUpdatedAt: session.WorkspaceRevisionUpdatedAt,
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
		SessionID:                  sessionID,
		OrgID:                      orgID,
		UserID:                     userID,
		Config:                     initialConfig,
		RepositoryID:               uuidValue(session.RepositoryID),
		BaseCommitSHA:              body.BaseCommitSHA,
		ProfileName:                body.ProfileName,
		WorkspaceRevision:          session.WorkspaceRevision,
		WorkspaceRevisionUpdatedAt: session.WorkspaceRevisionUpdatedAt,
	}
	reservation, err := h.manager.ReservePreview(ctx, input)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview reserve failed")
		if errors.Is(err, preview.ErrPreviewCapacity) {
			return nil, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, previewCapacityMessage(err), err)
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
	hydrateStarted := time.Now()
	acq := h.acquireSandbox(ctx, orgID, &session, reservation)
	// Losing the attach race means a competing holder (typically a
	// continue_session turn) just published a live container — exactly what
	// the reuse path attaches to. Retrying the acquire after a short delay
	// converts the most common preview start failure ("another process
	// attached first") into a successful attach instead of aborting the
	// reservation and making the user click again.
	retryDelay := h.sandboxBusyRetryDelay
	if retryDelay <= 0 {
		retryDelay = sandboxBusyAcquireRetryDelay
	}
	for attempt := 1; acq.ErrCode == "SANDBOX_BUSY" && attempt <= sandboxBusyAcquireRetries && ctx.Err() == nil; attempt++ {
		h.logger.Info().
			Str("session_id", sessionID.String()).
			Int("attempt", attempt).
			Msg("preview start: sandbox busy; retrying acquire against the winning container")
		select {
		case <-ctx.Done():
		case <-time.After(retryDelay):
		}
		// Re-read the session row: the winner published a container_id that
		// the reuse path needs to see.
		if fresh, freshErr := h.sessionStore.GetByID(ctx, orgID, sessionID); freshErr == nil {
			session = fresh
		}
		acq = h.acquireSandbox(ctx, orgID, &session, reservation)
	}
	metrics.RecordSessionPreviewPhaseDuration(ctx, orgID.String(), "hydrate", time.Since(hydrateStarted))
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
		return nil, classifyAcquireSandboxError(acq)
	}
	sb := acq.Sandbox

	// Container id we'd need to tear down on later failures. Empty when we
	// reused an existing container — the turn still owns it.
	hydratedID := ""
	if acq.Hydrated {
		hydratedID = sb.ID
	}

	if body.Config == nil {
		configStarted := time.Now()
		// Auto-detect: read preview config from the session's workspace.
		// We deliberately do NOT fall back to a generic "npm start on :3000"
		// default — for any repo without that file, that fallback exits within
		// seconds and the user waits ~90s for the readiness probe to give up.
		// Returning a clear PREVIEW_NO_CONFIG error is strictly more useful.
		cfg, err := h.readWorkspacePreviewConfig(ctx, sb, sessionID)
		metrics.RecordSessionPreviewPhaseDuration(ctx, orgID.String(), "config", time.Since(configStarted))
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
		return nil, classifyLaunchError(err, reservation.MemoryLimitMB)
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

func (h *PreviewHandler) startPreviewFromRequest(ctx context.Context, orgID, userID, sessionID uuid.UUID, body startPreviewRequest) (*models.PreviewInstance, int, *previewHTTPError) {
	h.supersedeSessionPrewarmForUserStart(ctx, orgID, sessionID)
	if !h.workerRoutingEnabled() {
		instance, localErr := h.startPreviewLocal(ctx, orgID, userID, sessionID, body)
		if localErr != nil {
			return nil, 0, localErr
		}
		return instance, http.StatusCreated, nil
	}

	session, err := h.sessionStore.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return nil, 0, newPreviewHTTPError(http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", err)
	}
	reqs, err := h.workerSelectionRequirements(ctx, orgID)
	if err != nil {
		return nil, 0, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_WORKER_SELECTION_FAILED", "failed to read network settings", err)
	}
	repoID := uuid.Nil
	if session.RepositoryID != nil {
		repoID = *session.RepositoryID
	}
	var cachePlacements []preview.WorkerCachePlacement
	if repoID != uuid.Nil {
		if body.Config != nil {
			configDigest, digestErr := preview.ComputePreviewConfigDigest(body.Config)
			if digestErr != nil {
				h.logger.Warn().Err(digestErr).Str("session_id", sessionID.String()).Msg("failed to compute preview config digest for dependency cache placement")
			}
			if paths, enabled := preview.ResolvePreviewInstallCachePaths(body.Config.Install); enabled && len(paths) > 0 {
				computedPlacementKey, placementErr := preview.ComputePreviewDependencyCachePlacementKey(orgID, repoID, body.Config.Name, configDigest, body.Config.Install, paths)
				if placementErr != nil {
					h.logger.Warn().Err(placementErr).Str("session_id", sessionID.String()).Msg("failed to compute preview dependency cache placement key")
				} else {
					cachePlacements = append(cachePlacements, preview.WorkerCachePlacement{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: computedPlacementKey})
				}
			}
			if paths, _, enabled := preview.ResolvePreviewInstallPackageManagerCachePaths(body.Config.Install); enabled && len(paths) > 0 {
				computedPlacementKey, placementErr := preview.ComputePreviewDependencyCachePlacementKey(orgID, repoID, body.Config.Name, configDigest, body.Config.Install, paths)
				if placementErr != nil {
					h.logger.Warn().Err(placementErr).Str("session_id", sessionID.String()).Msg("failed to compute preview package-manager cache placement key")
				} else {
					cachePlacements = append(cachePlacements, preview.WorkerCachePlacement{Kind: models.PreviewCacheKindPackageManager, PlacementKey: computedPlacementKey})
				}
			}
			if paths, enabled := preview.ResolvePreviewBuildCachePaths(body.Config.Install); enabled && len(paths) > 0 {
				buildCacheKey, keyErr := preview.ComputePreviewBuildCacheKey(orgID, repoID, body.Config.Name, configDigest, body.Config.Install, paths)
				if keyErr != nil {
					h.logger.Warn().Err(keyErr).Str("session_id", sessionID.String()).Msg("failed to compute preview build cache placement key")
				} else {
					cachePlacements = append(cachePlacements, preview.WorkerCachePlacement{Kind: models.PreviewCacheKindBuildArtifact, PlacementKey: buildCacheKey})
				}
			}
		}
		if len(cachePlacements) == 0 {
			computedPlacementKey, placementErr := preview.ComputePreviewDependencyCacheRepoPlacementKey(orgID, repoID)
			if placementErr != nil {
				h.logger.Warn().Err(placementErr).Str("session_id", sessionID.String()).Msg("failed to compute preview dependency cache placement key")
			} else {
				cachePlacements = append(cachePlacements, preview.WorkerCachePlacement{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: computedPlacementKey, Approximate: true})
			}
		}
	}
	worker, err := h.workerSelector.SelectStartNodeWithCachePlacementsAndRequirements(ctx, orgID, &session, repoID, cachePlacements, reqs)
	var deadOwnerErr *preview.LiveSessionWorkerOwnerNotRoutableError
	if errors.As(err, &deadOwnerErr) {
		refreshed, recoveryErr := h.clearDeadLiveSessionPreviewOwner(ctx, orgID, session, deadOwnerErr)
		if recoveryErr != nil {
			return nil, 0, recoveryErr
		}
		session = refreshed
		worker, err = h.workerSelector.SelectStartNodeWithCachePlacementsAndRequirements(ctx, orgID, &session, repoID, cachePlacements, reqs)
	}
	if err != nil {
		switch {
		case errors.Is(err, preview.ErrLegacySessionWorkerOwnership):
			return nil, 0, newPreviewHTTPError(http.StatusConflict, "PREVIEW_WORKER_OWNERSHIP_REQUIRED", "live sandbox is missing worker ownership metadata; send a new message to rebuild it", nil)
		case errors.Is(err, preview.ErrNoPreviewWorkers):
			return nil, 0, newPreviewHTTPError(http.StatusServiceUnavailable, "PREVIEW_NO_WORKERS", previewNoWorkersMessage(reqs), nil)
		default:
			return nil, 0, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_WORKER_SELECTION_FAILED", "failed to select preview worker", err)
		}
	}
	instance, asyncErr := h.enqueueStartPreviewJob(ctx, orgID, userID, session, worker, body)
	if asyncErr != nil {
		return nil, 0, asyncErr
	}
	return instance, http.StatusAccepted, nil
}

func (h *PreviewHandler) supersedeSessionPrewarmForUserStart(ctx context.Context, orgID, sessionID uuid.UUID) {
	if h == nil || h.store == nil {
		return
	}
	rows, err := h.store.SupersedeActiveSessionPreviewPrewarmRuns(ctx, orgID, sessionID, "skipped_user_started", "user explicitly started preview")
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to supersede active session preview prewarm runs")
		return
	}
	if rows > 0 {
		metrics.RecordSessionPreviewPrewarmSkipped(ctx, orgID.String(), "user_started")
	}
}

func (h *PreviewHandler) clearDeadLiveSessionPreviewOwner(ctx context.Context, orgID uuid.UUID, session models.Session, ownerErr *preview.LiveSessionWorkerOwnerNotRoutableError) (models.Session, *previewHTTPError) {
	containerID := ""
	workerNodeID := ""
	if ownerErr != nil {
		containerID = ownerErr.ContainerID
		workerNodeID = ownerErr.WorkerNodeID
	}
	if containerID == "" && session.ContainerID != nil {
		containerID = *session.ContainerID
	}
	if workerNodeID == "" && session.WorkerNodeID != nil {
		workerNodeID = *session.WorkerNodeID
	}
	if containerID == "" {
		return session, newPreviewHTTPError(
			http.StatusInternalServerError,
			"PREVIEW_STALE_SANDBOX_CLEAR_FAILED",
			"failed to clear stale preview sandbox ownership",
			fmt.Errorf("stale live session owner is missing container id"),
		)
	}

	cleared, err := h.sessionStore.ClearContainerID(ctx, orgID, session.ID, containerID)
	if err != nil {
		return session, newPreviewHTTPError(
			http.StatusInternalServerError,
			"PREVIEW_STALE_SANDBOX_CLEAR_FAILED",
			"failed to clear stale preview sandbox ownership",
			err,
		)
	}
	log := h.logger.With().
		Str("session_id", session.ID.String()).
		Str("container_id", containerID).
		Str("worker_node_id", workerNodeID).
		Logger()
	if cleared {
		log.Warn().Msg("cleared stale preview live-session worker ownership before worker selection retry")
	} else {
		log.Info().Msg("stale preview live-session worker ownership clear lost CAS; refetching before worker selection retry")
	}

	refreshed, err := h.sessionStore.GetByID(ctx, orgID, session.ID)
	if err != nil {
		return session, newPreviewHTTPError(
			http.StatusInternalServerError,
			"PREVIEW_STALE_SANDBOX_REFRESH_FAILED",
			"failed to refresh session after clearing stale preview sandbox ownership",
			err,
		)
	}
	return refreshed, nil
}

func previewNoWorkersMessage(reqs preview.WorkerSelectionRequirements) string {
	if !reqs.StaticEgressRequired {
		return "no preview-capable workers are available"
	}
	if reqs.StaticEgressPublicIP == "" {
		return "Static egress is enabled, but no public IP is configured. Disable static egress or configure STATIC_EGRESS_PUBLIC_IP."
	}
	return fmt.Sprintf("Static egress is enabled, but no preview workers are verified for %s. Disable static egress or provision workers.", reqs.StaticEgressPublicIP)
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

	instance, status, startErr := h.startPreviewFromRequest(r.Context(), orgID, user.ID, sessionID, body)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	writeJSON(w, status, models.SingleResponse[*models.PreviewInstance]{Data: instance})
}

func (h *PreviewHandler) workerSelectionRequirements(ctx context.Context, orgID uuid.UUID) (preview.WorkerSelectionRequirements, error) {
	if h == nil {
		return preview.WorkerSelectionRequirements{}, nil
	}
	return previewWorkerSelectionRequirements(ctx, h.orgStore, orgID, h.staticEgress.PublicIP)
}

func previewWorkerSelectionRequirements(ctx context.Context, orgStore agent.OrgSettingsReader, orgID uuid.UUID, staticEgressPublicIP string) (preview.WorkerSelectionRequirements, error) {
	if orgStore == nil {
		return preview.WorkerSelectionRequirements{}, nil
	}
	org, err := orgStore.GetByID(ctx, orgID)
	if err != nil {
		return preview.WorkerSelectionRequirements{}, err
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return preview.WorkerSelectionRequirements{}, err
	}
	return preview.WorkerSelectionRequirements{
		StaticEgressRequired: settings.SandboxNetwork.StaticEgressEnabled,
		StaticEgressPublicIP: staticEgressPublicIP,
	}, nil
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
				if prewarm := h.sessionPreviewPrewarmStatus(r.Context(), orgID, sessionID); prewarm != nil {
					writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewStatusResponse]{Data: &models.PreviewStatusResponse{
						Services: []models.PreviewService{},
						Prewarm:  prewarm,
					}})
					return
				}
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
	status.Freshness = h.previewFreshness(r.Context(), orgID, sessionID, status.Instance)
	status.RecommendedUpdateMode = h.recommendedPreviewUpdateMode(status.Instance, status.Freshness, true)
	status.Prewarm = h.sessionPreviewPrewarmStatus(r.Context(), orgID, sessionID)

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewStatusResponse]{Data: status})
}

func (h *PreviewHandler) sessionPreviewPrewarmStatus(ctx context.Context, orgID, sessionID uuid.UUID) *models.PreviewPrewarmStatus {
	if h == nil || h.store == nil || h.sessionStore == nil {
		return nil
	}
	run, err := h.store.GetLatestSessionPreviewPrewarmRun(ctx, orgID, sessionID, models.PreviewSpeculativeDecisionWarmCandidate)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session preview prewarm state")
		}
		return nil
	}
	session, err := h.sessionStore.GetByID(ctx, orgID, sessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for preview prewarm state")
		return nil
	}
	state := ""
	switch run.Status {
	case "queued", "running":
		state = "warming"
	case "succeeded":
		if run.WorkspaceRevision == session.WorkspaceRevision {
			if ok := h.sessionWarmPreviewStartupCacheAvailable(ctx, orgID, session, run, nil); !ok {
				return nil
			}
			state = "warm"
		} else {
			if _, markErr := h.store.SupersedeStaleSessionPreviewWarmRuns(ctx, orgID, sessionID, session.WorkspaceRevision); markErr != nil {
				h.logger.Warn().Err(markErr).Str("session_id", sessionID.String()).Msg("failed to mark stale session preview warm run")
			}
			return nil
		}
	case "failed":
		// This status endpoint is called by the Preview panel, so a current
		// revision failure becomes user-actionable on the first panel open.
		if run.WorkspaceRevision == session.WorkspaceRevision {
			state = "failed"
		}
	}
	if state == "" {
		return nil
	}
	// Record that the user has seen the prewarm panel so future failed states
	// become visible.
	if markErr := h.store.MarkSessionPreviewPrewarmPanelOpened(ctx, orgID, sessionID); markErr != nil {
		h.logger.Warn().Err(markErr).Str("session_id", sessionID.String()).Msg("failed to mark session preview prewarm panel opened")
	}
	var estimate *int
	if state == "warm" {
		seconds := 30
		estimate = &seconds
	}
	status := &models.PreviewPrewarmStatus{
		State:                 state,
		WorkspaceRevision:     run.WorkspaceRevision,
		ResumeEstimateSeconds: estimate,
	}
	if state == "failed" {
		status.Error = run.Error
		if run.PreviewID != nil {
			status.PreviewID = run.PreviewID.String()
		}
	}
	return status
}

func (h *PreviewHandler) warmSessionPreviewForResume(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewInstance, *previewHTTPError) {
	if h == nil || h.store == nil || h.sessionStore == nil {
		return nil, nil
	}
	run, err := h.store.GetLatestSessionPreviewPrewarmRun(ctx, orgID, sessionID, models.PreviewSpeculativeDecisionWarmCandidate)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session preview prewarm run", err)
	}
	if run.Status != "succeeded" || run.PreviewID == nil {
		return nil, nil
	}
	session, err := h.sessionStore.GetByID(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, newPreviewHTTPError(http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", err)
		}
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session for warm preview resume", err)
	}
	if run.WorkspaceRevision != session.WorkspaceRevision {
		return nil, nil
	}
	instance, err := h.store.GetPreviewInstance(ctx, orgID, *run.PreviewID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load warm preview instance", err)
	}
	if instance.SessionID != sessionID {
		return nil, nil
	}
	if !instance.Status.IsTerminal() {
		return nil, nil
	}
	reason, err := h.store.GetPreviewStoppedReason(ctx, orgID, instance.ID)
	if err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load warm preview stop reason", err)
	}
	if reason != models.PreviewStoppedReasonSessionPrewarmPolicy {
		return nil, nil
	}
	if instance.SourceWorkspaceRevision == nil || *instance.SourceWorkspaceRevision != session.WorkspaceRevision {
		return nil, nil
	}
	if ok := h.sessionWarmPreviewStartupCacheAvailable(ctx, orgID, session, run, instance); !ok {
		return nil, nil
	}
	return instance, nil
}

func (h *PreviewHandler) sessionWarmPreviewStartupCacheAvailable(ctx context.Context, orgID uuid.UUID, session models.Session, run *models.SessionPreviewPrewarmRun, instance *models.PreviewInstance) bool {
	if h == nil || h.store == nil || run == nil || run.PreviewID == nil || session.RepositoryID == nil {
		return false
	}
	if instance == nil {
		loaded, err := h.store.GetPreviewInstance(ctx, orgID, *run.PreviewID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load warm preview instance for startup cache check")
			}
			return false
		}
		instance = loaded
	}
	if instance == nil || strings.TrimSpace(instance.WorkerNodeID) == "" {
		return false
	}
	ok, err := h.store.HasSessionPreviewStartupCache(ctx, orgID, *session.RepositoryID, session.ID, run.WorkspaceRevision, instance.WorkerNodeID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to verify session preview startup cache")
		return false
	}
	return ok
}

func (h *PreviewHandler) sessionPreviewStartMetricPath(ctx context.Context, orgID, sessionID uuid.UUID) string {
	if h == nil || h.store == nil {
		return "cold_start"
	}
	run, err := h.store.GetLatestSessionPreviewPrewarmRun(ctx, orgID, sessionID, models.PreviewSpeculativeDecisionCache)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load cache prewarm run for preview start metric")
		}
		return "cold_start"
	}
	if run.Status == "succeeded" {
		metrics.RecordSessionPrewarmOpenAfterPrewarm(ctx, orgID.String(), "cache")
		return "prewarm_cache"
	}
	return "cold_start"
}

func (h *PreviewHandler) resumeWarmSessionPreview(ctx context.Context, orgID uuid.UUID, instance *models.PreviewInstance) *previewHTTPError {
	if instance == nil {
		return nil
	}
	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if err != nil {
			return newPreviewHTTPError(http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
		}
		if h.isLocalWorker(worker) {
			if err := h.manager.ResumeStoppedWarmPreview(ctx, orgID, instance.ID); err != nil {
				return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_WARM_RESUME_FAILED", "failed to resume warm preview", err)
			}
			return nil
		}
		if err := h.workerClient.ResumeWarmPreview(ctx, worker, orgID, instance.ID); err != nil {
			return workerClientHTTPError(err)
		}
		return nil
	}
	if err := h.manager.ResumeStoppedWarmPreview(ctx, orgID, instance.ID); err != nil {
		return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_WARM_RESUME_FAILED", "failed to resume warm preview", err)
	}
	return nil
}

func (h *PreviewHandler) previewFreshness(ctx context.Context, orgID, sessionID uuid.UUID, instance *models.PreviewInstance) *models.PreviewFreshness {
	if instance == nil {
		return nil
	}
	if instance.SessionID == uuid.Nil || instance.SessionID != sessionID {
		return &models.PreviewFreshness{State: models.PreviewFreshnessUnknown, Reason: "not_session_preview"}
	}
	if h.sessionStore == nil {
		h.logger.Warn().Str("session_id", sessionID.String()).Msg("preview freshness: session store is not configured")
		return &models.PreviewFreshness{
			State:                             models.PreviewFreshnessUnknown,
			PreviewWorkspaceRevision:          instance.SourceWorkspaceRevision,
			PreviewWorkspaceRevisionUpdatedAt: instance.SourceWorkspaceRevisionUpdatedAt,
			Reason:                            "preview_revision_missing",
		}
	}

	session, err := h.sessionStore.GetByID(ctx, orgID, sessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("preview freshness: failed to load session")
		return &models.PreviewFreshness{
			State:                             models.PreviewFreshnessUnknown,
			PreviewWorkspaceRevision:          instance.SourceWorkspaceRevision,
			PreviewWorkspaceRevisionUpdatedAt: instance.SourceWorkspaceRevisionUpdatedAt,
			Reason:                            "preview_revision_missing",
		}
	}
	restartReasons := h.previewRestartReasons(ctx, orgID, &session, instance)
	return computePreviewFreshness(&session, instance, restartReasons)
}

func (h *PreviewHandler) recommendedPreviewUpdateMode(instance *models.PreviewInstance, freshness *models.PreviewFreshness, reloadBrowser bool) models.PreviewUpdateMode {
	if h.restartClassifier == nil || instance == nil {
		return ""
	}
	return h.restartClassifier.SelectUpdateMode(instance.Status, freshness, reloadBrowser, previewPrimaryServiceSupportsHMR(instance))
}

// previewPrimaryServiceSupportsHMR reports whether the preview's primary service
// declared HMR support in its config. The config is read from the instance's
// persisted recycle config (already loaded with the instance row), so this adds
// no query and runs only on the cold update/status path. A missing or
// unparseable config is treated as no-HMR so the classifier falls back to the
// safe soft-restart contract.
func previewPrimaryServiceSupportsHMR(instance *models.PreviewInstance) bool {
	if instance == nil || len(instance.RecycleConfig) <= 2 {
		return false
	}
	var cfg models.PreviewConfig
	if err := json.Unmarshal(instance.RecycleConfig, &cfg); err != nil {
		return false
	}
	return cfg.PrimaryServiceSupportsHMR()
}

func (h *PreviewHandler) previewRestartReasons(ctx context.Context, orgID uuid.UUID, session *models.Session, instance *models.PreviewInstance) []models.PreviewRestartReason {
	if h.restartClassifier == nil || h.sessionStore == nil || session == nil || instance == nil {
		return nil
	}
	if session.LatestDiffSnapshotID == nil || !previewStatusCanRefreshForFreshness(instance.Status) {
		return nil
	}
	if instance.SourceWorkspaceRevision == nil || *instance.SourceWorkspaceRevision >= session.WorkspaceRevision {
		return nil
	}
	snapshot, err := h.sessionStore.GetLatestDiffSnapshot(ctx, orgID, session.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("preview freshness: failed to load latest diff snapshot for restart classification")
		}
		return nil
	}
	paths := preview.ChangedPathsFromUnifiedDiff(snapshot.Diff)
	return h.restartClassifier.Classify(paths)
}

func computePreviewFreshness(session *models.Session, instance *models.PreviewInstance, restartReasons []models.PreviewRestartReason) *models.PreviewFreshness {
	if session == nil || instance == nil {
		return nil
	}
	freshness := &models.PreviewFreshness{
		State:                             models.PreviewFreshnessCurrent,
		CurrentWorkspaceRevision:          session.WorkspaceRevision,
		CurrentWorkspaceRevisionUpdatedAt: session.WorkspaceRevisionUpdatedAt,
		PreviewWorkspaceRevision:          instance.SourceWorkspaceRevision,
		PreviewWorkspaceRevisionUpdatedAt: instance.SourceWorkspaceRevisionUpdatedAt,
		RuntimeWorkspaceRevision:          instance.RuntimeWorkspaceRevision,
		RuntimeWorkspaceRevisionUpdatedAt: instance.RuntimeWorkspaceRevisionUpdatedAt,
		RuntimeWorkspaceRevisionSource:    instance.RuntimeWorkspaceRevisionSource,
	}
	if instance.SourceWorkspaceRevision == nil {
		freshness.State = models.PreviewFreshnessUnknown
		freshness.Reason = "preview_revision_missing"
		return freshness
	}
	if instance.Status == models.PreviewStatusStarting && *instance.SourceWorkspaceRevision == session.WorkspaceRevision {
		freshness.State = models.PreviewFreshnessUpdating
		freshness.Reason = "preview_starting"
		return freshness
	}
	if *instance.SourceWorkspaceRevision < session.WorkspaceRevision && len(restartReasons) > 0 {
		if !previewStatusCanRefreshForFreshness(instance.Status) {
			freshness.State = models.PreviewFreshnessUnknown
			freshness.Reason = "preview_not_refreshable"
			return freshness
		}
		freshness.State = models.PreviewFreshnessRestartRequired
		freshness.RestartRequired = true
		freshness.RestartReasons = restartReasons
		freshness.Reason = "restart_required"
		return freshness
	}
	latestKnownRevision := instance.SourceWorkspaceRevision
	if instance.RuntimeWorkspaceRevision != nil && *instance.RuntimeWorkspaceRevision > *latestKnownRevision {
		latestKnownRevision = instance.RuntimeWorkspaceRevision
	}
	if *latestKnownRevision < session.WorkspaceRevision {
		if !previewStatusCanRefreshForFreshness(instance.Status) {
			freshness.State = models.PreviewFreshnessUnknown
			freshness.Reason = "preview_not_refreshable"
			return freshness
		}
		freshness.State = models.PreviewFreshnessOutOfDate
		freshness.Reason = "session_changed_after_preview_start"
		return freshness
	}
	if instance.RuntimeWorkspaceRevision != nil &&
		*instance.RuntimeWorkspaceRevision >= session.WorkspaceRevision &&
		instance.RuntimeWorkspaceRevisionSource == models.PreviewRuntimeRevisionSourceHMR &&
		*instance.SourceWorkspaceRevision < session.WorkspaceRevision {
		freshness.State = models.PreviewFreshnessLiveUpdated
		freshness.Reason = "preview_live_updated"
		return freshness
	}
	return freshness
}

func previewStatusCanRefreshForFreshness(status models.PreviewStatus) bool {
	switch status {
	case models.PreviewStatusReady,
		models.PreviewStatusPartiallyReady,
		models.PreviewStatusUnhealthy,
		models.PreviewStatusFailed:
		return true
	default:
		return false
	}
}

// =============================================================================
// DELETE /api/v1/sessions/{id}/preview — Stop a preview
// =============================================================================

func (h *PreviewHandler) StopPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
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

func (h *PreviewHandler) refreshedConfigForRecycle(ctx context.Context, instance *models.PreviewInstance, body startPreviewRequest) (*models.PreviewConfig, *previewHTTPError) {
	if body.Config != nil {
		return body.Config, nil
	}
	if h.fileReader == nil {
		return nil, nil
	}
	if len(instance.RecycleSandbox) <= 2 {
		return nil, nil
	}
	var sb agent.Sandbox
	if err := json.Unmarshal(instance.RecycleSandbox, &sb); err != nil {
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_CONFIG_READ_FAILED", "failed to read preview config from workspace", err)
	}
	cfg, err := h.readWorkspacePreviewConfig(ctx, &sb, instance.SessionID)
	if err != nil {
		if errors.Is(err, preview.ErrInvalidConfig) {
			msg := preview.InvalidConfigMessage(err)
			return nil, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_CONFIG_INVALID", msg, err)
		}
		return nil, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_CONFIG_READ_FAILED", "failed to read preview config from workspace", err)
	}
	if cfg == nil {
		return nil, newPreviewHTTPError(
			http.StatusUnprocessableEntity,
			"PREVIEW_NO_CONFIG",
			"This repo has no .143/config.json committed with a preview section. Add one (see docs/guides/previews.md) so the preview knows what command to run.",
			nil,
		)
	}
	return cfg, nil
}

func (h *PreviewHandler) recyclePreviewInstance(ctx context.Context, orgID uuid.UUID, instance *models.PreviewInstance, body startPreviewRequest) *previewHTTPError {
	cfg, cfgErr := h.refreshedConfigForRecycle(ctx, instance, body)
	if cfgErr != nil {
		return cfgErr
	}
	if h.sessionStore == nil {
		return newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "session store not configured", nil)
	}
	session, err := h.sessionStore.GetByID(ctx, orgID, instance.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return newPreviewHTTPError(http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", err)
		}
		return newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session for recycle", err)
	}
	if err := h.manager.RecyclePreviewWithConfigAndRevision(ctx, orgID, instance.ID, cfg, session.WorkspaceRevision, session.WorkspaceRevisionUpdatedAt); err != nil {
		if errors.Is(err, preview.ErrInvalidConfig) {
			return newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_CONFIG_INVALID", preview.InvalidConfigMessage(err), err)
		}
		return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_RESTART_FAILED", "failed to restart preview", err)
	}
	return nil
}

func (h *PreviewHandler) recyclePreviewByID(ctx context.Context, orgID, previewID uuid.UUID, body startPreviewRequest) *previewHTTPError {
	instance, err := h.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
	}
	return h.recyclePreviewInstance(ctx, orgID, instance, body)
}

// softRestartPreviewByID performs a soft restart on the worker that owns the
// preview, loading the instance's session to stamp the runtime workspace
// revision. It mirrors recyclePreviewByID so that worker-routed (remote) soft
// restarts advance the runtime revision the same way the local path does;
// without this, freshness would stay out_of_date and `preview update` would
// never converge for multi-worker deployments.
func (h *PreviewHandler) softRestartPreviewByID(ctx context.Context, orgID, previewID uuid.UUID) *previewHTTPError {
	instance, err := h.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get preview", err)
	}
	var revision int64
	var revisionUpdatedAt time.Time
	if h.sessionStore != nil {
		session, err := h.sessionStore.GetByID(ctx, orgID, instance.SessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return newPreviewHTTPError(http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", err)
			}
			return newPreviewHTTPError(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session for soft restart", err)
		}
		revision = session.WorkspaceRevision
		revisionUpdatedAt = session.WorkspaceRevisionUpdatedAt
	}
	if revisionUpdatedAt.IsZero() {
		if err := h.manager.SoftRestartPreview(ctx, orgID, previewID); err != nil {
			if errors.Is(err, preview.ErrSoftRestartUnsupported) {
				return newPreviewHTTPError(http.StatusNotImplemented, preview.PreviewSoftRestartUnsupportedCode, "preview provider does not support soft restart", err)
			}
			return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_SOFT_RESTART_FAILED", "failed to soft restart preview", err)
		}
		return nil
	}
	if err := h.manager.SoftRestartPreviewWithRevision(ctx, orgID, previewID, revision, revisionUpdatedAt); err != nil {
		if errors.Is(err, preview.ErrSoftRestartUnsupported) {
			return newPreviewHTTPError(http.StatusNotImplemented, preview.PreviewSoftRestartUnsupportedCode, "preview provider does not support soft restart", err)
		}
		return newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_SOFT_RESTART_FAILED", "failed to soft restart preview", err)
	}
	return nil
}

func (h *PreviewHandler) ensurePreview(w http.ResponseWriter, r *http.Request) {
	clearWriteDeadline(w, r)
	clickStarted := time.Now()

	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, ok := parsePreviewSessionID(w, r)
	if !ok {
		return
	}
	body, reqErr := h.decodeStartPreviewBody(r)
	if reqErr != nil {
		writePreviewHTTPError(w, r, reqErr)
		return
	}

	instance, activeErr := h.lookupActivePreviewForRequest(r.Context(), orgID, sessionID)
	if activeErr != nil {
		writePreviewHTTPError(w, r, activeErr)
		return
	}
	if instance == nil {
		warmInstance, warmErr := h.warmSessionPreviewForResume(r.Context(), orgID, sessionID)
		if warmErr != nil {
			writePreviewHTTPError(w, r, warmErr)
			return
		}
		if warmInstance != nil {
			if resumeErr := h.resumeWarmSessionPreview(r.Context(), orgID, warmInstance); resumeErr != nil {
				writePreviewHTTPError(w, r, resumeErr)
				return
			}
			metrics.RecordSessionPrewarmOpenAfterPrewarm(r.Context(), orgID.String(), "warm_candidate")
			metrics.RecordSessionPrewarmClickToReady(r.Context(), orgID.String(), "warm_resume", time.Since(clickStarted))
			refreshed, err := h.store.GetPreviewInstance(r.Context(), orgID, warmInstance.ID)
			if err != nil {
				refreshed = warmInstance
			}
			writeJSON(w, http.StatusOK, models.SingleResponse[ensurePreviewResponse]{
				Data: h.ensurePreviewResponse(r.Context(), orgID, "resumed", refreshed),
			})
			return
		}
		started, _, startErr := h.startPreviewFromRequest(r.Context(), orgID, user.ID, sessionID, body)
		if startErr != nil {
			writePreviewHTTPError(w, r, startErr)
			return
		}
		metrics.RecordSessionPrewarmClickToReady(r.Context(), orgID.String(), h.sessionPreviewStartMetricPath(r.Context(), orgID, sessionID), time.Since(clickStarted))
		writeJSON(w, http.StatusAccepted, models.SingleResponse[ensurePreviewResponse]{
			Data: h.ensurePreviewResponse(r.Context(), orgID, "started", started),
		})
		return
	}
	if instance.Status == models.PreviewStatusStarting {
		metrics.RecordSessionPrewarmClickToReady(r.Context(), orgID.String(), "live_reuse", time.Since(clickStarted))
		writeJSON(w, http.StatusAccepted, models.SingleResponse[ensurePreviewResponse]{
			Data: h.ensurePreviewResponse(r.Context(), orgID, "already_starting", instance),
		})
		return
	}

	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
			return
		}
		if h.isLocalWorker(worker) {
			if recycleErr := h.recyclePreviewInstance(r.Context(), orgID, instance, body); recycleErr != nil {
				writePreviewHTTPError(w, r, recycleErr)
				return
			}
		} else {
			if err := h.workerClient.RecyclePreview(r.Context(), worker, orgID, instance.ID, body.Config); err != nil {
				h.writeWorkerClientError(w, r, err)
				return
			}
		}
	} else {
		if recycleErr := h.recyclePreviewInstance(r.Context(), orgID, instance, body); recycleErr != nil {
			writePreviewHTTPError(w, r, recycleErr)
			return
		}
	}
	refreshed, err := h.store.GetPreviewInstance(r.Context(), orgID, instance.ID)
	if err != nil {
		refreshed = instance
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[ensurePreviewResponse]{
		Data: h.ensurePreviewResponse(r.Context(), orgID, "restarted", refreshed),
	})
}

// EnsurePreview handles POST /api/v1/sessions/{id}/preview/ensure.
func (h *PreviewHandler) EnsurePreview(w http.ResponseWriter, r *http.Request) {
	// Keep the org-context access visible to the handler tenancy lint; the
	// shared implementation below also reads this value.
	middleware.OrgIDFromContext(r.Context())
	h.ensurePreview(w, r)
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
	sessionID, ok := parsePreviewSessionID(w, r)
	if !ok {
		return
	}
	body, reqErr := h.decodeStartPreviewBody(r)
	if reqErr != nil {
		writePreviewHTTPError(w, r, reqErr)
		return
	}
	userID, ok := previewRequestUserID(r.Context(), middleware.UserFromContext(r.Context()))
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	instance, action, restartErr := h.RestartSessionPreview(r.Context(), orgID, userID, sessionID, body)
	if restartErr != nil {
		writePreviewHTTPError(w, r, restartErr)
		return
	}
	switch action {
	case sessionPreviewRestartStarted, sessionPreviewRestartAlreadyStarting:
		writeJSON(w, http.StatusAccepted, models.SingleResponse[ensurePreviewResponse]{
			Data: h.ensurePreviewResponse(r.Context(), orgID, action, instance),
		})
	default:
		writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}})
	}
}

// Actions returned by RestartSessionPreview.
const (
	sessionPreviewRestartStarted         = "started"
	sessionPreviewRestartAlreadyStarting = "already_starting"
	sessionPreviewRestartRestarting      = "restarting"
)

// RestartSessionPreview restarts the preview for a session regardless of its
// current state: with no active instance (stopped, failed, expired, or never
// started) a fresh preview is started — re-hydrating the sandbox if needed —
// while an active instance is recycled in place. It returns the resulting
// instance and the action taken. Used by the session-scoped restart endpoint
// and by the generic preview restart endpoint for previews without a branch
// target.
func (h *PreviewHandler) RestartSessionPreview(ctx context.Context, orgID, userID, sessionID uuid.UUID, body startPreviewRequest) (*models.PreviewInstance, string, *previewHTTPError) {
	if h.manager == nil {
		return nil, "", newPreviewHTTPError(http.StatusNotImplemented, "PREVIEW_NOT_AVAILABLE", "preview manager is not configured on this worker", nil)
	}
	instance, activeErr := h.lookupActivePreviewForRequest(ctx, orgID, sessionID)
	if activeErr != nil {
		return nil, "", activeErr
	}
	if instance == nil {
		started, _, startErr := h.startPreviewFromRequest(ctx, orgID, userID, sessionID, body)
		if startErr != nil {
			return nil, "", startErr
		}
		return started, sessionPreviewRestartStarted, nil
	}
	if instance.Status == models.PreviewStatusStarting {
		return instance, sessionPreviewRestartAlreadyStarting, nil
	}

	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if err != nil {
			return nil, "", newPreviewHTTPError(http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", err)
		}
		if h.isLocalWorker(worker) {
			if recycleErr := h.recyclePreviewInstance(ctx, orgID, instance, body); recycleErr != nil {
				return nil, "", recycleErr
			}
		} else {
			if err := h.workerClient.RecyclePreview(ctx, worker, orgID, instance.ID, body.Config); err != nil {
				return nil, "", workerClientHTTPError(err)
			}
		}
	} else {
		if recycleErr := h.recyclePreviewInstance(ctx, orgID, instance, body); recycleErr != nil {
			return nil, "", recycleErr
		}
	}

	// Re-read so callers that render the instance see the post-recycle state.
	if refreshed, err := h.store.GetPreviewInstance(ctx, orgID, instance.ID); err == nil {
		instance = refreshed
	}
	return instance, sessionPreviewRestartRestarting, nil
}

func (h *PreviewHandler) UpdatePreview(w http.ResponseWriter, r *http.Request) {
	clearWriteDeadline(w, r)

	if !h.requireManager(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, ok := parsePreviewSessionID(w, r)
	if !ok {
		return
	}
	userID, ok := previewRequestUserID(r.Context(), middleware.UserFromContext(r.Context()))
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var body models.PreviewUpdateRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
			return
		}
	}
	reloadBrowser := true
	if body.ReloadBrowser != nil {
		reloadBrowser = *body.ReloadBrowser
	}
	if body.Path == "" {
		body.Path = "/"
	}
	if body.ForceMode != "" && body.ForceMode.Validate() != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_UPDATE_MODE_UNSUPPORTED", "requested force_mode is not supported")
		return
	}

	instance, activeErr := h.lookupActivePreviewForRequest(r.Context(), orgID, sessionID)
	if activeErr != nil {
		writePreviewHTTPError(w, r, activeErr)
		return
	}
	if instance == nil {
		if body.ForceMode == models.PreviewUpdateModeBrowserReload {
			writeError(w, r, http.StatusConflict, "PREVIEW_NOT_READY", "preview is not ready for browser reload")
			return
		}
		started, _, startErr := h.startPreviewFromRequest(r.Context(), orgID, userID, sessionID, startPreviewRequest{Config: body.Config})
		if startErr != nil {
			writePreviewHTTPError(w, r, startErr)
			return
		}
		resp := models.PreviewUpdateResponse{
			PreviewID: started.ID,
			SessionID: sessionID,
			Mode:      models.PreviewUpdateModeColdRelaunch,
			Action:    models.PreviewUpdateActionStarted,
			Status:    started.Status,
			Message:   "preview cold relaunch started",
		}
		h.recordPreviewUpdateLog(r.Context(), orgID, started.ID, "info", "preview cold relaunch started for update", map[string]any{
			"mode": models.PreviewUpdateModeColdRelaunch,
			"path": body.Path,
		})
		h.emitPreviewToolAudit(r, models.AuditActionPreviewUpdated, started, map[string]any{
			"tool":   "preview_update",
			"mode":   models.PreviewUpdateModeColdRelaunch,
			"action": resp.Action,
		})
		if body.Wait {
			h.waitForPreviewReady(r.Context(), orgID, resp.PreviewID, &resp)
		}
		writeJSON(w, http.StatusAccepted, models.SingleResponse[models.PreviewUpdateResponse]{Data: resp})
		return
	}
	if instance.Status == models.PreviewStatusStarting {
		writeError(w, r, http.StatusConflict, "PREVIEW_UPDATE_CONFLICT", "preview start or update is already in progress")
		return
	}

	freshness := h.previewFreshness(r.Context(), orgID, sessionID, instance)
	if freshness != nil && freshness.State == models.PreviewFreshnessUpdating {
		writeError(w, r, http.StatusConflict, "PREVIEW_UPDATE_CONFLICT", "preview start or update is already in progress")
		return
	}
	mode := h.recommendedPreviewUpdateMode(instance, freshness, reloadBrowser)
	if body.ForceMode != "" {
		mode = body.ForceMode
	}
	if mode == models.PreviewUpdateModeSoftServiceRestart && body.Config != nil {
		mode = models.PreviewUpdateModeFullRecycle
	}

	resp := models.PreviewUpdateResponse{
		PreviewID: instance.ID,
		SessionID: sessionID,
		Mode:      mode,
		Status:    instance.Status,
		Freshness: freshness,
	}
	status, statusErr := h.manager.GetStatus(r.Context(), orgID, instance.ID)
	if statusErr == nil && status != nil {
		resp.PreviewURL = status.PreviewOrigin
	}
	h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "info", "preview update requested", map[string]any{
		"mode":           mode,
		"force_mode":     body.ForceMode,
		"reload_browser": reloadBrowser,
		"path":           body.Path,
		"freshness":      freshness,
	})

	switch mode {
	case models.PreviewUpdateModeNoopCurrent:
		resp.Action = models.PreviewUpdateActionAlreadyCurrent
		resp.Message = "preview is already current"
		h.emitPreviewToolAudit(r, models.AuditActionPreviewUpdated, instance, map[string]any{
			"tool":   "preview_update",
			"mode":   mode,
			"action": resp.Action,
		})
		writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewUpdateResponse]{Data: resp})
		return
	case models.PreviewUpdateModeBrowserReload:
		if !previewStatusBrowserReady(instance.Status) {
			h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "warn", "preview update browser reload rejected", map[string]any{
				"mode":   mode,
				"status": instance.Status,
			})
			writeError(w, r, http.StatusConflict, "PREVIEW_NOT_READY", "preview is not ready for browser reload")
			return
		}
		if reloadBrowser {
			if err := h.reloadPreviewBrowser(r.Context(), orgID, instance, body.Path); err != nil {
				if _, ok := preview.AsWorkerRequestError(err); ok {
					h.writeWorkerClientError(w, r, err)
					return
				}
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_UPDATE_FAILED", "failed to reload preview browser", err)
				return
			}
		}
		resp.Action = models.PreviewUpdateActionUpdated
		resp.Message = "browser reloaded"
		h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "info", "preview browser reloaded", map[string]any{
			"mode": mode,
			"path": body.Path,
		})
		h.emitPreviewToolAudit(r, models.AuditActionPreviewUpdated, instance, map[string]any{
			"tool":   "preview_update",
			"mode":   mode,
			"action": resp.Action,
		})
		writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewUpdateResponse]{Data: resp})
		return
	case models.PreviewUpdateModeSoftServiceRestart:
		if err := h.softRestartPreviewForUpdate(r.Context(), orgID, instance, sessionID); err != nil {
			if errors.Is(err, preview.ErrSoftRestartUnsupported) {
				h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "info", "preview soft restart unsupported, falling back to full recycle", map[string]any{
					"requested_mode": mode,
				})
				h.executePreviewRecycleUpdate(w, r, orgID, userID, sessionID, instance, body, resp, models.PreviewUpdateModeFullRecycle)
				return
			}
			h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "warn", "preview soft restart failed", map[string]any{
				"mode":  mode,
				"error": err.Error(),
			})
			if _, ok := preview.AsWorkerRequestError(err); ok {
				h.writeWorkerClientError(w, r, err)
				return
			}
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_UPDATE_FAILED", "failed to soft restart preview", err)
			return
		}
		if refreshed, err := h.store.GetPreviewInstance(r.Context(), orgID, instance.ID); err == nil {
			resp.Status = refreshed.Status
		}
		resp.Action = models.PreviewUpdateActionRestarting
		resp.Message = "preview service restart started"
		if body.Wait {
			h.waitForPreviewReady(r.Context(), orgID, resp.PreviewID, &resp)
		}
		h.recordPreviewUpdateLog(r.Context(), orgID, instance.ID, "info", "preview soft restart started", map[string]any{
			"mode": mode,
		})
		h.emitPreviewToolAudit(r, models.AuditActionPreviewUpdated, instance, map[string]any{
			"tool":   "preview_update",
			"mode":   mode,
			"action": resp.Action,
		})
		writeJSON(w, http.StatusAccepted, models.SingleResponse[models.PreviewUpdateResponse]{Data: resp})
		return
	case models.PreviewUpdateModeFullRecycle, models.PreviewUpdateModeColdRelaunch:
		h.executePreviewRecycleUpdate(w, r, orgID, userID, sessionID, instance, body, resp, mode)
		return
	default:
		writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_UPDATE_MODE_UNSUPPORTED", "requested update mode is not supported")
	}
}

// executePreviewRecycleUpdate performs a full recycle of the session preview
// and writes the update response. It is shared by the full_recycle/cold_relaunch
// update modes and by the soft_service_restart fallback path when the active
// provider does not support soft restarts.
func (h *PreviewHandler) executePreviewRecycleUpdate(
	w http.ResponseWriter,
	r *http.Request,
	orgID, userID, sessionID uuid.UUID,
	instance *models.PreviewInstance,
	body models.PreviewUpdateRequest,
	resp models.PreviewUpdateResponse,
	mode models.PreviewUpdateMode,
) {
	resp.Mode = mode
	restarted, _, restartErr := h.RestartSessionPreview(r.Context(), orgID, userID, sessionID, startPreviewRequest{Config: body.Config})
	if restartErr != nil {
		writePreviewHTTPError(w, r, restartErr)
		return
	}
	if restarted != nil {
		resp.PreviewID = restarted.ID
		resp.Status = restarted.Status
		refreshedFreshness := h.previewFreshness(r.Context(), orgID, sessionID, restarted)
		resp.Freshness = refreshedFreshness
	}
	resp.Action = models.PreviewUpdateActionRestarting
	resp.Message = "preview restart started"
	if body.Wait {
		h.waitForPreviewReady(r.Context(), orgID, resp.PreviewID, &resp)
	}
	h.recordPreviewUpdateLog(r.Context(), orgID, resp.PreviewID, "info", "preview restart started for update", map[string]any{
		"mode": mode,
	})
	auditInstance := instance
	if restarted != nil {
		auditInstance = restarted
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewUpdated, auditInstance, map[string]any{
		"tool":   "preview_update",
		"mode":   mode,
		"action": resp.Action,
	})
	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.PreviewUpdateResponse]{Data: resp})
}

func previewStatusBrowserReady(status models.PreviewStatus) bool {
	switch status {
	case models.PreviewStatusReady, models.PreviewStatusPartiallyReady, models.PreviewStatusUnhealthy:
		return true
	default:
		return false
	}
}

func (h *PreviewHandler) recordPreviewUpdateLog(ctx context.Context, orgID, previewID uuid.UUID, level, message string, metadata map[string]any) {
	if h.store == nil {
		return
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		h.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("preview update log metadata marshal failed")
		raw = json.RawMessage(`{}`)
	}
	if err := h.store.CreatePreviewLog(ctx, &models.PreviewLog{
		PreviewInstanceID: previewID,
		OrgID:             orgID,
		Level:             level,
		Step:              models.PreviewLogStepUpdate,
		Message:           message,
		Metadata:          raw,
	}); err != nil {
		h.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to create preview update log")
	}
}

func (h *PreviewHandler) softRestartPreviewForUpdate(ctx context.Context, orgID uuid.UUID, instance *models.PreviewInstance, sessionID uuid.UUID) error {
	var revision int64
	var revisionUpdatedAt time.Time
	if h.sessionStore != nil {
		if session, err := h.sessionStore.GetByID(ctx, orgID, sessionID); err == nil {
			revision = session.WorkspaceRevision
			revisionUpdatedAt = session.WorkspaceRevisionUpdatedAt
		} else {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("soft restart: failed to load session revision; restarting without revision stamp")
		}
	}
	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if err != nil {
			return err
		}
		if !h.isLocalWorker(worker) {
			return h.workerClient.SoftRestartPreview(ctx, worker, orgID, instance.ID)
		}
	}
	if revisionUpdatedAt.IsZero() {
		return h.manager.SoftRestartPreview(ctx, orgID, instance.ID)
	}
	return h.manager.SoftRestartPreviewWithRevision(ctx, orgID, instance.ID, revision, revisionUpdatedAt)
}

func (h *PreviewHandler) reloadPreviewBrowser(ctx context.Context, orgID uuid.UUID, instance *models.PreviewInstance, path string) error {
	step := models.InteractionStep{Action: "navigate", Value: path, WaitFor: "load", Timeout: 10 * time.Second}
	if h.workerRoutingEnabled() {
		worker, err := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if err != nil {
			return err
		}
		if h.isLocalWorker(worker) {
			inspector := h.manager.Inspector()
			if inspector == nil {
				return fmt.Errorf("preview inspector is not configured")
			}
			_, err = inspector.ExecuteInteraction(ctx, instance.ID.String(), []models.InteractionStep{step})
			return err
		}
		_, err = h.workerClient.ExecuteInteraction(ctx, worker, orgID, instance.ID, []models.InteractionStep{step})
		return err
	}
	inspector := h.manager.Inspector()
	if inspector == nil {
		return fmt.Errorf("preview inspector is not configured")
	}
	_, err := inspector.ExecuteInteraction(ctx, instance.ID.String(), []models.InteractionStep{step})
	return err
}

func (h *PreviewHandler) waitForPreviewReady(ctx context.Context, orgID, previewID uuid.UUID, resp *models.PreviewUpdateResponse) {
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
			status, err := h.manager.GetStatus(waitCtx, orgID, previewID)
			if err != nil || status == nil || status.Instance == nil {
				continue
			}
			resp.Status = status.Instance.Status
			resp.PreviewURL = status.PreviewOrigin
			if status.Instance.Status == models.PreviewStatusReady ||
				status.Instance.Status == models.PreviewStatusPartiallyReady ||
				status.Instance.Status.IsTerminal() {
				return
			}
		}
	}
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

	var logs []models.PreviewLog
	if r.URL.Query().Get("tail") == "true" {
		logs, err = h.store.ListLatestLogsByPreview(r.Context(), orgID, instance.ID)
	} else {
		logs, err = h.store.ListLogsByPreview(r.Context(), orgID, instance.ID, nil)
	}
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
	instance, ok := h.getPreviewTarget(w, r)
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
	instance, ok := h.getPreviewTarget(w, r)
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
	instance, ok := h.getPreviewTarget(w, r)
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
	instance, ok := h.getPreviewTarget(w, r)
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
	Path         string `json:"path"`
	ViewportW    int    `json:"viewport_w"`
	ViewportH    int    `json:"viewport_h"`
	FullPage     bool   `json:"full_page"`
	DelayMS      int    `json:"delay_ms"`
	InlineBase64 *bool  `json:"inline_base64"`
}

type captureScreenshotResponse struct {
	PageTitle     string                  `json:"page_title"`
	ConsoleErrors []models.ConsoleMessage `json:"console_errors,omitempty"`
	URL           string                  `json:"url"`
	Viewport      models.ViewportSpec     `json:"viewport"`
	Artifact      *models.PreviewArtifact `json:"artifact,omitempty"`
	CapturedAt    time.Time               `json:"captured_at"`
	PNGBase64     string                  `json:"png_base64,omitempty"`
}

type observePreviewRequest struct {
	Path                  string `json:"path"`
	ViewportW             int    `json:"viewport_w"`
	ViewportH             int    `json:"viewport_h"`
	FullPage              bool   `json:"full_page"`
	DelayMS               int    `json:"delay_ms"`
	Selector              string `json:"selector"`
	IncludeDOM            bool   `json:"include_dom"`
	MaxSemanticBytes      int    `json:"max_semantic_bytes"`
	InlineBase64          bool   `json:"inline_base64"`
	ConsoleCursor         int64  `json:"console_cursor"`
	PreserveConsoleCursor bool   `json:"preserve_console_cursor"`
	ReadOnly              bool   `json:"read_only"`
	Ephemeral             bool   `json:"ephemeral"`
	SkipSemantic          bool   `json:"skip_semantic"`
}

type actPreviewRequest struct {
	Steps            []models.InteractionStep `json:"steps"`
	ViewportW        int                      `json:"viewport_w"`
	ViewportH        int                      `json:"viewport_h"`
	Selector         string                   `json:"selector"`
	IncludeDOM       bool                     `json:"include_dom"`
	MaxSemanticBytes int                      `json:"max_semantic_bytes"`
	InlineBase64     bool                     `json:"inline_base64"`
	ConsoleCursor    int64                    `json:"console_cursor"`
	Ephemeral        bool                     `json:"ephemeral"`
}

func browserPolicyForInstance(instance *models.PreviewInstance) preview.BrowserSessionPolicy {
	policy := preview.BrowserSessionPolicy{PersistSession: true, DefaultViewport: models.ViewportSpec{Name: "desktop", Width: 1440, Height: 900}, AllowedPaths: []string{"/**"}}
	if instance == nil || len(instance.RecycleConfig) == 0 {
		return policy
	}
	var cfg models.PreviewConfig
	if json.Unmarshal(instance.RecycleConfig, &cfg) != nil {
		return policy
	}
	if cfg.Browser.DefaultViewport.Width > 0 || len(cfg.Browser.AllowedPaths) > 0 {
		policy.PersistSession = cfg.Browser.PersistSession
	}
	if cfg.Browser.DefaultViewport.Width > 0 && cfg.Browser.DefaultViewport.Height > 0 {
		policy.DefaultViewport = cfg.Browser.DefaultViewport
	}
	if len(cfg.Browser.AllowedPaths) > 0 {
		policy.AllowedPaths = append([]string(nil), cfg.Browser.AllowedPaths...)
	}
	return policy
}

func (h *PreviewHandler) Observe(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}
	if instance.SessionID == uuid.Nil {
		writeError(w, r, http.StatusUnprocessableEntity, "SESSION_BROWSER_REQUIRED", "observe requires a session preview")
		return
	}
	var body observePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	opts := models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{Path: body.Path, ViewportW: body.ViewportW, ViewportH: body.ViewportH, FullPage: body.FullPage, Delay: time.Duration(body.DelayMS) * time.Millisecond}, Selector: body.Selector, IncludeDOM: body.IncludeDOM, MaxSemanticBytes: body.MaxSemanticBytes, ConsoleCursor: body.ConsoleCursor, PreserveConsoleCursor: body.PreserveConsoleCursor, ReadOnly: body.ReadOnly, SkipSemantic: body.SkipSemantic}
	policy := browserPolicyForInstance(instance)
	var result *models.PreviewObservation
	var err error
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(r.Context(), instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) && h.browserSessions != nil {
			result, err = h.browserSessions.Observe(r.Context(), orgID, instance.SessionID, instance.ID, policy, opts)
		} else {
			result, err = h.workerClient.Observe(r.Context(), worker, orgID, instance.SessionID, instance.ID, preview.RemoteObserveRequest{SessionID: instance.SessionID, Policy: policy, Options: opts})
		}
	} else if h.browserSessions != nil {
		result, err = h.browserSessions.Observe(r.Context(), orgID, instance.SessionID, instance.ID, policy, opts)
	} else {
		err = preview.ErrBrowserUnavailable
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
		writeBrowserSessionError(w, r, err)
		return
	}
	result.Ready = instance.Status == models.PreviewStatusReady || instance.Status == models.PreviewStatusPartiallyReady
	if result.Screenshot != nil && !body.Ephemeral {
		attachPreviewArtifacts(r.Context(), h, instance.OrgID, instance, result.Screenshot, "observation")
	}
	if result.Screenshot != nil && !body.InlineBase64 {
		result.Screenshot.PNG = nil
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{"tool": "preview_observe", "path": body.Path})
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewObservation]{Data: result})
}

func (h *PreviewHandler) Act(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}
	if instance.SessionID == uuid.Nil {
		writeError(w, r, http.StatusUnprocessableEntity, "SESSION_BROWSER_REQUIRED", "act requires a session preview")
		return
	}
	var body actPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if len(body.Steps) == 0 || len(body.Steps) > maxInteractionSteps {
		writeError(w, r, http.StatusBadRequest, "INVALID_STEPS", fmt.Sprintf("steps must contain between 1 and %d actions", maxInteractionSteps))
		return
	}
	normalizeInteractionStepTimeouts(body.Steps)
	opts := models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{ViewportW: body.ViewportW, ViewportH: body.ViewportH}, Selector: body.Selector, IncludeDOM: body.IncludeDOM, MaxSemanticBytes: body.MaxSemanticBytes, ConsoleCursor: body.ConsoleCursor}
	policy := browserPolicyForInstance(instance)
	ctx, cancel := context.WithTimeout(r.Context(), maxInteractionDuration)
	defer cancel()
	var result *models.PreviewActResult
	var err error
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) && h.browserSessions != nil {
			result, err = h.browserSessions.Act(ctx, orgID, instance.SessionID, instance.ID, policy, body.Steps, opts)
		} else {
			result, err = h.workerClient.Act(ctx, worker, orgID, instance.SessionID, instance.ID, preview.RemoteActRequest{SessionID: instance.SessionID, Policy: policy, Steps: body.Steps, Options: opts})
		}
	} else if h.browserSessions != nil {
		result, err = h.browserSessions.Act(ctx, orgID, instance.SessionID, instance.ID, policy, body.Steps, opts)
	} else {
		err = preview.ErrBrowserUnavailable
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
		writeBrowserSessionError(w, r, err)
		return
	}
	h.writeActResult(w, r, instance, result, body.InlineBase64, body.Ephemeral, "preview_act")
}

func (h *PreviewHandler) writeActResult(w http.ResponseWriter, r *http.Request, instance *models.PreviewInstance, result *models.PreviewActResult, inlineBase64, ephemeral bool, tool string) {
	if result.Observation != nil {
		result.Observation.Ready = instance.Status == models.PreviewStatusReady || instance.Status == models.PreviewStatusPartiallyReady
	}
	if result.Observation != nil && result.Observation.Screenshot != nil && !ephemeral {
		attachPreviewArtifacts(r.Context(), h, instance.OrgID, instance, result.Observation.Screenshot, "action_observation")
		if !inlineBase64 {
			result.Observation.Screenshot.PNG = nil
		}
	}
	if result.Interaction != nil && !ephemeral {
		for i := range result.Interaction.Steps {
			stepScreenshot := result.Interaction.Steps[i].Screenshot
			if stepScreenshot == nil {
				continue
			}
			attachPreviewArtifacts(r.Context(), h, instance.OrgID, instance, stepScreenshot, "action_step")
			if !inlineBase64 {
				stepScreenshot.PNG = nil
			}
		}
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{"tool": tool})
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewActResult]{Data: result})
}

func (h *PreviewHandler) GetBrowserControl(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}
	if h.browserSessions == nil || instance.SessionID == uuid.Nil {
		writeError(w, r, http.StatusServiceUnavailable, "BROWSER_UNAVAILABLE", "preview browser is unavailable")
		return
	}
	if _, err := h.browserSessions.EnsureIdentity(r.Context(), orgID, instance.SessionID, instance.ID, browserPolicyForInstance(instance)); err != nil {
		writeBrowserSessionError(w, r, err)
		return
	}
	status, err := h.browserSessions.GetControl(r.Context(), orgID, instance.SessionID)
	if err != nil {
		writeBrowserSessionError(w, r, err)
		return
	}
	if user := middleware.UserFromContext(r.Context()); user != nil && status.LeaseOwnerID != nil {
		status.IsLeaseOwner = user.ID == *status.LeaseOwnerID
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewBrowserControlStatus]{Data: status})
}

func (h *PreviewHandler) AcquireHumanControl(w http.ResponseWriter, r *http.Request) {
	middleware.OrgIDFromContext(r.Context())
	h.changeBrowserControl(w, r, "acquire")
}

func (h *PreviewHandler) ReturnAgentControl(w http.ResponseWriter, r *http.Request) {
	middleware.OrgIDFromContext(r.Context())
	h.changeBrowserControl(w, r, "return")
}

func (h *PreviewHandler) RequestHumanHandoff(w http.ResponseWriter, r *http.Request) {
	middleware.OrgIDFromContext(r.Context())
	h.changeBrowserControl(w, r, "handoff")
}

func (h *PreviewHandler) changeBrowserControl(w http.ResponseWriter, r *http.Request, action string) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}
	if h.browserSessions == nil || instance.SessionID == uuid.Nil {
		writeError(w, r, http.StatusServiceUnavailable, "BROWSER_UNAVAILABLE", "preview browser is unavailable")
		return
	}
	var body models.PreviewBrowserControlRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
			return
		}
	}
	var status *models.PreviewBrowserControlStatus
	var err error
	switch action {
	case "handoff":
		status, err = h.browserSessions.RequestHandoff(r.Context(), orgID, instance.SessionID, body.Reason)
	case "acquire", "return":
		user := middleware.UserFromContext(r.Context())
		if user == nil || user.ID == uuid.Nil {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if action == "acquire" {
			status, err = h.browserSessions.AcquireHumanControl(r.Context(), orgID, instance.SessionID, user.ID, time.Duration(body.DurationSeconds)*time.Second)
		} else {
			status, err = h.browserSessions.ReturnAgentControl(r.Context(), orgID, instance.SessionID, user.ID)
		}
	}
	if err != nil {
		writeBrowserSessionError(w, r, err)
		return
	}
	if user := middleware.UserFromContext(r.Context()); user != nil && status.LeaseOwnerID != nil {
		status.IsLeaseOwner = user.ID == *status.LeaseOwnerID
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{"tool": "preview_control_" + action, "control_state": status.State})
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PreviewBrowserControlStatus]{Data: status})
}

func (h *PreviewHandler) HumanAct(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil || user.ID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	var body actPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if len(body.Steps) == 0 || len(body.Steps) > maxInteractionSteps {
		writeError(w, r, http.StatusBadRequest, "INVALID_STEPS", fmt.Sprintf("steps must contain between 1 and %d actions", maxInteractionSteps))
		return
	}
	normalizeInteractionStepTimeouts(body.Steps)
	opts := models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{ViewportW: body.ViewportW, ViewportH: body.ViewportH}, Selector: body.Selector, IncludeDOM: body.IncludeDOM, MaxSemanticBytes: body.MaxSemanticBytes, ConsoleCursor: body.ConsoleCursor}
	policy := browserPolicyForInstance(instance)
	ctx, cancel := context.WithTimeout(r.Context(), maxInteractionDuration)
	defer cancel()
	var result *models.PreviewActResult
	var err error
	if h.workerRoutingEnabled() {
		worker, resolveErr := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
		if resolveErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
			return
		}
		if h.isLocalWorker(worker) && h.browserSessions != nil {
			result, err = h.browserSessions.ActAsHuman(ctx, orgID, instance.SessionID, instance.ID, user.ID, policy, body.Steps, opts)
		} else {
			result, err = h.workerClient.ActAsHuman(ctx, worker, orgID, instance.SessionID, instance.ID, user.ID, preview.RemoteActRequest{SessionID: instance.SessionID, Policy: policy, Steps: body.Steps, Options: opts})
		}
	} else if h.browserSessions != nil {
		result, err = h.browserSessions.ActAsHuman(ctx, orgID, instance.SessionID, instance.ID, user.ID, policy, body.Steps, opts)
	} else {
		err = preview.ErrBrowserUnavailable
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
		} else {
			writeBrowserSessionError(w, r, err)
		}
		return
	}
	h.writeActResult(w, r, instance, result, body.InlineBase64, body.Ephemeral, "preview_human_act")
}

func (h *PreviewHandler) persistPreviewScreenshotArtifact(ctx context.Context, orgID, previewID uuid.UUID, kind string, result *models.ScreenshotResult) *models.PreviewArtifact {
	if h.uploads == nil || result == nil || len(result.PNG) == 0 {
		return nil
	}
	artifactID := uuid.NewString()
	now := time.Now()
	key := fmt.Sprintf("%s/%s/preview-artifacts/%s/%s.png", orgID, now.Format("2006-01"), previewID, artifactID)
	url, err := h.uploads.Save(ctx, key, bytes.NewReader(result.PNG), "image/png")
	if err != nil {
		h.logger.Warn().Err(err).
			Str("preview_id", previewID.String()).
			Str("artifact_kind", kind).
			Msg("failed to persist preview screenshot artifact")
		return nil
	}
	artifact := &models.PreviewArtifact{
		ID:          artifactID,
		Kind:        kind,
		ContentType: "image/png",
		URL:         url,
		StorageKey:  key,
		Bytes:       len(result.PNG),
		CreatedAt:   now,
	}
	result.Artifact = artifact
	return artifact
}

func attachPreviewArtifacts(ctx context.Context, h *PreviewHandler, orgID uuid.UUID, instance *models.PreviewInstance, result *models.ScreenshotResult, kind string) {
	if instance == nil || result == nil {
		return
	}
	h.persistPreviewScreenshotArtifact(ctx, orgID, instance.ID, kind, result)
}

func bindSessionBrowser(inspector preview.PreviewInspector, instance *models.PreviewInstance) {
	if instance == nil || instance.SessionID == uuid.Nil {
		return
	}
	if binder, ok := inspector.(preview.SessionBrowserBinder); ok {
		binder.BindSessionBrowser(instance.ID.String(), instance.SessionID.String())
	}
}

func (h *PreviewHandler) emitPreviewToolAudit(r *http.Request, action models.AuditAction, instance *models.PreviewInstance, details map[string]any) {
	if h.audit == nil || instance == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["preview_id"] = instance.ID.String()
	if instance.SessionID != uuid.Nil {
		details["session_id"] = instance.SessionID.String()
	}
	resourceID := instance.ID.String()
	raw := marshalAuditDetails(h.logger, details)
	var sessionID *uuid.UUID
	if instance.SessionID != uuid.Nil {
		sessionID = &instance.SessionID
	}
	emitUserAuditWithSession(h.audit, r, action, models.AuditResourcePreview, &resourceID, sessionID, nil, raw)
}

func (h *PreviewHandler) CaptureScreenshot(w http.ResponseWriter, r *http.Request) {
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
	}
	instance, ok := h.getPreviewTarget(w, r)
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
			bindSessionBrowser(inspector, instance)
			result, err = inspector.CaptureScreenshot(r.Context(), instance.ID.String(), opts)
		} else {
			result, err = h.workerClient.CaptureScreenshot(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, opts)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		bindSessionBrowser(inspector, instance)
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
	if result.Viewport.Width == 0 && result.Viewport.Height == 0 {
		result.Viewport = models.ViewportSpec{Width: opts.ViewportW, Height: opts.ViewportH}
	}
	attachPreviewArtifacts(r.Context(), h, middleware.OrgIDFromContext(r.Context()), instance, result, "screenshot")

	// When an artifact was persisted, callers get a stable reference and don't
	// need the full PNG inlined; defaulting it off keeps large base64 blobs out
	// of agent context. Without an artifact (upload store unconfigured) we still
	// inline for compatibility. An explicit inline_base64 always wins.
	inlineBase64 := result.Artifact == nil
	if body.InlineBase64 != nil {
		inlineBase64 = *body.InlineBase64
	}

	resp := captureScreenshotResponse{
		PageTitle:     result.PageTitle,
		ConsoleErrors: result.ConsoleErrors,
		URL:           result.URL,
		Viewport:      result.Viewport,
		Artifact:      result.Artifact,
		CapturedAt:    result.CapturedAt,
	}
	if inlineBase64 {
		resp.PNGBase64 = base64.StdEncoding.EncodeToString(result.PNG)
	}
	auditDetails := map[string]any{"tool": "preview_screenshot"}
	if result.Artifact != nil {
		auditDetails["artifact_id"] = result.Artifact.ID
		auditDetails["artifact_url"] = result.Artifact.URL
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewScreenshotCaptured, instance, auditDetails)

	writeJSON(w, http.StatusOK, models.SingleResponse[captureScreenshotResponse]{Data: resp})
}

// =============================================================================
// POST /api/v1/sessions/{id}/preview/inspect — Inspect a DOM element
// =============================================================================

type inspectElementRequest struct {
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Selector string `json:"selector,omitempty"`
}

func (h *PreviewHandler) InspectElement(w http.ResponseWriter, r *http.Request) {
	if !h.workerRoutingEnabled() {
		if _, ok := h.requireInspector(w, r); !ok {
			return
		}
	}
	instance, ok := h.getPreviewTarget(w, r)
	if !ok {
		return
	}

	var body inspectElementRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	body.Selector = strings.TrimSpace(body.Selector)
	if body.Selector == "" {
		// Max coordinate is generous but prevents obviously absurd values.
		const maxCoordinate = 10000
		if body.X < 0 || body.Y < 0 || body.X > maxCoordinate || body.Y > maxCoordinate {
			writeError(w, r, http.StatusBadRequest, "INVALID_COORDINATES",
				fmt.Sprintf("x and y must be between 0 and %d", maxCoordinate))
			return
		}
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
			if body.Selector != "" {
				element, err = inspector.InspectElementBySelector(r.Context(), instance.ID.String(), body.Selector)
			} else {
				element, err = inspector.InspectElement(r.Context(), instance.ID.String(), body.X, body.Y)
			}
		} else {
			if body.Selector != "" {
				element, err = h.workerClient.InspectElementBySelector(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.Selector)
			} else {
				element, err = h.workerClient.InspectElement(r.Context(), worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.X, body.Y)
			}
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		if body.Selector != "" {
			element, err = inspector.InspectElementBySelector(r.Context(), instance.ID.String(), body.Selector)
		} else {
			element, err = inspector.InspectElement(r.Context(), instance.ID.String(), body.X, body.Y)
		}
	}
	if err != nil {
		if _, ok := preview.AsWorkerRequestError(err); ok {
			h.writeWorkerClientError(w, r, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INSPECT_FAILED", "failed to inspect element", err)
		return
	}

	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool": "preview_inspect",
	})
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
	instance, ok := h.getPreviewTarget(w, r)
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
	if level := strings.TrimSpace(r.URL.Query().Get("level")); level != "" {
		filtered := messages[:0]
		for _, msg := range messages {
			if strings.EqualFold(msg.Level, level) {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool":  "preview_console",
		"count": len(messages),
	})
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
	instance, ok := h.getPreviewTarget(w, r)
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
	normalizeInteractionStepTimeouts(body.Steps)

	// Enforce the max total duration per the design doc (60 seconds).
	ctx, cancel := context.WithTimeout(r.Context(), maxInteractionDuration)
	defer cancel()
	if instance.SessionID != uuid.Nil && h.browserSessions != nil {
		orgID := middleware.OrgIDFromContext(r.Context())
		policy := browserPolicyForInstance(instance)
		var actResult *models.PreviewActResult
		var actErr error
		if h.workerRoutingEnabled() {
			worker, resolveErr := h.resolvePreviewWorker(ctx, instance.WorkerNodeID)
			if resolveErr != nil {
				writeError(w, r, http.StatusBadGateway, "PREVIEW_WORKER_RESOLUTION_FAILED", "failed to resolve preview worker", resolveErr)
				return
			}
			if h.isLocalWorker(worker) && h.browserSessions != nil {
				actResult, actErr = h.browserSessions.Act(ctx, orgID, instance.SessionID, instance.ID, policy, body.Steps, models.PreviewObservationOpts{})
			} else {
				actResult, actErr = h.workerClient.Act(ctx, worker, orgID, instance.SessionID, instance.ID, preview.RemoteActRequest{SessionID: instance.SessionID, Policy: policy, Steps: body.Steps})
			}
		} else if h.browserSessions != nil {
			actResult, actErr = h.browserSessions.Act(ctx, orgID, instance.SessionID, instance.ID, policy, body.Steps, models.PreviewObservationOpts{})
		} else {
			actErr = preview.ErrBrowserUnavailable
		}
		if actErr != nil {
			if _, ok := preview.AsWorkerRequestError(actErr); ok {
				h.writeWorkerClientError(w, r, actErr)
			} else {
				writeBrowserSessionError(w, r, actErr)
			}
			return
		}
		if actResult == nil || actResult.Interaction == nil {
			writeError(w, r, http.StatusInternalServerError, "INTERACTION_FAILED", "browser interaction returned no result")
			return
		}
		for i := range actResult.Interaction.Steps {
			if actResult.Interaction.Steps[i].Screenshot != nil {
				attachPreviewArtifacts(r.Context(), h, orgID, instance, actResult.Interaction.Steps[i].Screenshot, "interaction_screenshot")
			}
		}
		h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{"tool": "preview_interact", "step_count": len(body.Steps)})
		writeJSON(w, http.StatusOK, models.SingleResponse[*models.InteractionResult]{Data: actResult.Interaction})
		return
	}

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
			bindSessionBrowser(inspector, instance)
			result, err = inspector.ExecuteInteraction(ctx, instance.ID.String(), body.Steps)
		} else {
			result, err = h.workerClient.ExecuteInteraction(ctx, worker, middleware.OrgIDFromContext(r.Context()), instance.ID, body.Steps)
		}
	} else {
		inspector, inspectorOK := h.requireInspector(w, r)
		if !inspectorOK {
			return
		}
		bindSessionBrowser(inspector, instance)
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
	orgID := middleware.OrgIDFromContext(r.Context())
	for i := range result.Steps {
		if result.Steps[i].Screenshot != nil {
			attachPreviewArtifacts(r.Context(), h, orgID, instance, result.Steps[i].Screenshot, "interaction_screenshot")
		}
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool":       "preview_interact",
		"step_count": len(body.Steps),
	})

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.InteractionResult]{Data: result})
}

func normalizeInteractionStepTimeouts(steps []models.InteractionStep) {
	for i := range steps {
		if steps[i].Timeout == 0 && steps[i].TimeoutMS > 0 {
			steps[i].Timeout = time.Duration(steps[i].TimeoutMS) * time.Millisecond
		}
	}
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
	instance, ok := h.getPreviewTarget(w, r)
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
	orgID := middleware.OrgIDFromContext(r.Context())
	for i := range result.Captures {
		result.Captures[i].Screenshot.Viewport = result.Captures[i].Viewport
		attachPreviewArtifacts(r.Context(), h, orgID, instance, &result.Captures[i].Screenshot, "multi_viewport_screenshot")
	}
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool":          "preview_multi_viewport",
		"capture_count": len(result.Captures),
	})

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
	instance, ok := h.getPreviewTarget(w, r)
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
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool":               "preview_visual_diff",
		"pixel_diff_percent": diff.PixelDiffPercent,
	})

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
	instance, ok := h.getPreviewTarget(w, r)
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
	h.emitPreviewToolAudit(r, models.AuditActionPreviewToolInvoked, instance, map[string]any{
		"tool":            "preview_assert",
		"assertion_count": len(body.Assertions),
		"passed":          result.Passed,
		"failed":          result.Failed,
	})

	writeJSON(w, http.StatusOK, models.SingleResponse[*preview.AssertionResult]{Data: result})
}
