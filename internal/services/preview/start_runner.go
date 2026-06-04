package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
)

const previewNoConfigMessage = "This repo has no .143/config.json committed with a preview section. Add one (see docs/guides/previews.md) so the preview knows what command to run."

var ErrSandboxBusy = errors.New("session sandbox is busy")

// StartRunner completes durable preview startup jobs after the API has
// reserved the preview row and enqueued start_preview.
type StartRunner struct {
	manager         *Manager
	previews        *db.PreviewStore
	sessions        *db.SessionStore
	repositories    *db.RepositoryStore
	orgs            agent.OrgSettingsReader
	fileReader      sandbox.FileReader
	sandboxProvider agent.SandboxProvider
	sandboxCapacity *agent.SandboxCapacityGate
	staticEgress    agent.StaticEgressRuntimeConfig
	snapshots       storage.SnapshotStore
	snapshotCache   previewStartupCache
	github          branchPreviewGitHub
	nodeID          string
	logger          zerolog.Logger
}

type StartRunnerConfig struct {
	Manager         *Manager
	Previews        *db.PreviewStore
	Sessions        *db.SessionStore
	Repositories    *db.RepositoryStore
	Orgs            agent.OrgSettingsReader
	FileReader      sandbox.FileReader
	SandboxProvider agent.SandboxProvider
	SandboxCapacity *agent.SandboxCapacityGate
	StaticEgress    agent.StaticEgressRuntimeConfig
	Snapshots       storage.SnapshotStore
	SnapshotCache   *SnapshotCache
	GitHub          branchPreviewGitHub
	NodeID          string
	Logger          zerolog.Logger
}

type branchPreviewGitHub interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

func NewStartRunner(cfg StartRunnerConfig) *StartRunner {
	var snapshotCache previewStartupCache
	if cfg.SnapshotCache != nil {
		snapshotCache = cfg.SnapshotCache
	} else if cfg.Manager != nil {
		snapshotCache = cfg.Manager.SnapshotCache()
	}
	return &StartRunner{
		manager:         cfg.Manager,
		previews:        cfg.Previews,
		sessions:        cfg.Sessions,
		repositories:    cfg.Repositories,
		orgs:            cfg.Orgs,
		fileReader:      cfg.FileReader,
		sandboxProvider: cfg.SandboxProvider,
		sandboxCapacity: cfg.SandboxCapacity,
		staticEgress:    cfg.StaticEgress,
		snapshots:       cfg.Snapshots,
		snapshotCache:   snapshotCache,
		github:          cfg.GitHub,
		nodeID:          cfg.NodeID,
		logger:          cfg.Logger,
	}
}

var gitCommitSHARe = regexp.MustCompile(`\A[0-9a-fA-F]{7,40}\z`)

type previewStartupCache interface {
	FindSnapshot(ctx context.Context, orgID, repoID uuid.UUID, snapshotKey string) (*CacheHit, error)
	RestoreSnapshot(ctx context.Context, sb *agent.Sandbox, hit *CacheHit) error
	CreateSnapshot(ctx context.Context, sb *agent.Sandbox, snapshotKey string, metadata SnapshotMetadata) error
}

// StartReservedBranchPreview completes a target-owned branch preview by
// creating a fresh sandbox, cloning the repository, checking out the pinned
// commit, resolving the preview config, and launching the runtime.
func (r *StartRunner) StartReservedBranchPreview(ctx context.Context, payload StartBranchPreviewJobPayload) error {
	if r == nil || r.manager == nil || r.previews == nil || r.repositories == nil || r.sandboxProvider == nil {
		return fmt.Errorf("branch preview start runner is not configured")
	}
	reservation, err := r.previews.GetPreviewInstance(ctx, payload.OrgID, payload.PreviewID)
	if err != nil {
		return fmt.Errorf("get reserved branch preview: %w", err)
	}
	if reservation.Status != models.PreviewStatusStarting {
		return fmt.Errorf("reserved branch preview is not starting (status=%s)", reservation.Status)
	}
	if reservation.PreviewTargetID == nil || *reservation.PreviewTargetID != payload.PreviewTargetID {
		r.abort(ctx, reservation, "", "preview reservation no longer matches target")
		return fmt.Errorf("reserved branch preview target mismatch")
	}
	if err := r.reassignReservationWorkerIfNeeded(ctx, payload.OrgID, payload.PreviewID, reservation); err != nil {
		return fmt.Errorf("reassign branch preview worker: %w", err)
	}

	target, err := r.previews.GetPreviewTarget(ctx, payload.OrgID, payload.PreviewTargetID)
	if err != nil {
		r.abort(ctx, reservation, "", fmt.Sprintf("target lookup: %v", err))
		return fmt.Errorf("get preview target: %w", err)
	}
	if target.RepositoryID != payload.RepositoryID || target.CommitSHA != payload.CommitSHA {
		r.abort(ctx, reservation, "", "preview target changed before startup")
		return fmt.Errorf("preview target payload mismatch")
	}
	if !gitCommitSHARe.MatchString(target.CommitSHA) {
		r.abort(ctx, reservation, "", "preview target commit sha is invalid")
		return fmt.Errorf("invalid commit sha")
	}
	repo, err := r.repositories.GetByID(ctx, payload.OrgID, target.RepositoryID)
	if err != nil {
		r.abort(ctx, reservation, "", fmt.Sprintf("repository lookup: %v", err))
		return fmt.Errorf("get repository: %w", err)
	}
	if !repo.IsActive() {
		r.abort(ctx, reservation, "", "repository is disconnected")
		return fmt.Errorf("repository is disconnected")
	}
	if r.github == nil {
		r.abort(ctx, reservation, "", "GitHub is not configured on this worker")
		return fmt.Errorf("github is not configured")
	}

	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = sandboxCfg.HomeDir + "/" + agent.SlugForRepo(repo.FullName)
	sandboxCfg.SessionID = reservation.ID.String()
	sandboxCfg.OrgID = payload.OrgID.String()
	sandboxCfg.Purpose = "branch_preview"
	ApplyResourceLimitsToSandboxConfig(&sandboxCfg, payload.Config)
	if err := r.applyBranchPreviewSandboxNetwork(ctx, payload.OrgID, &sandboxCfg); err != nil {
		r.abort(ctx, reservation, "", fmt.Sprintf("resolve sandbox network: %v", err))
		return fmt.Errorf("resolve sandbox network: %w", err)
	}

	var capacityReservation *agent.SandboxCapacityReservation
	if r.sandboxCapacity != nil {
		var capErr error
		capacityReservation, capErr = r.sandboxCapacity.Acquire(ctx, agent.SandboxCapacityRequest{
			Purpose: sandboxCfg.Purpose, SessionID: sandboxCfg.SessionID, OrgID: sandboxCfg.OrgID,
		})
		if capErr != nil {
			r.registerCapacityDeadLetter(ctx, reservation)
			return fmt.Errorf("%s: %w: %w", PreviewCapacityCode, ErrPreviewCapacity, capErr)
		}
		defer capacityReservation.Release()
	}

	sb, err := r.sandboxProvider.Create(ctx, sandboxCfg)
	if err != nil {
		r.abort(ctx, reservation, "", fmt.Sprintf("create sandbox: %v", err))
		return fmt.Errorf("create sandbox: %w", err)
	}
	token, err := r.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		r.abort(ctx, reservation, sb.ID, fmt.Sprintf("get GitHub token: %v", err))
		return fmt.Errorf("get github token: %w", err)
	}
	if err := r.previews.UpdatePreviewPhase(ctx, payload.OrgID, payload.PreviewID, "checkout"); err != nil {
		r.logger.Warn().Err(err).Str("preview_id", payload.PreviewID.String()).Msg("failed to persist checkout preview phase")
	}
	checkoutStarted := time.Now()
	if err := r.sandboxProvider.CloneRepo(ctx, sb, repo.CloneURL, target.Branch, token); err != nil {
		metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "clone")
		r.abort(ctx, reservation, sb.ID, fmt.Sprintf("clone repository: %v", err))
		return fmt.Errorf("clone repository: %w", err)
	}
	var checkoutErr bytes.Buffer
	exitCode, err := r.sandboxProvider.Exec(ctx, sb, "git checkout --detach "+target.CommitSHA, io.Discard, &checkoutErr)
	if err != nil {
		metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "checkout")
		r.abort(ctx, reservation, sb.ID, fmt.Sprintf("checkout commit: %v", err))
		return fmt.Errorf("checkout commit: %w", err)
	}
	if exitCode != 0 {
		metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "checkout")
		msg := checkoutErr.String()
		r.abort(ctx, reservation, sb.ID, "checkout commit failed: "+msg)
		return fmt.Errorf("checkout commit failed with code %d: %s", exitCode, msg)
	}
	metrics.RecordBranchPreviewCheckout(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, time.Since(checkoutStarted))
	metrics.RecordBranchPreviewPhaseDuration(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "checkout", time.Since(checkoutStarted))

	if err := r.previews.UpdatePreviewPhase(ctx, payload.OrgID, payload.PreviewID, "config"); err != nil {
		r.logger.Warn().Err(err).Str("preview_id", payload.PreviewID.String()).Msg("failed to persist config preview phase")
	}
	configStarted := time.Now()
	cfg := payload.Config
	if cfg == nil {
		cfg, err = r.readWorkspacePreviewConfig(ctx, sb, uuid.Nil, target.PreviewConfigName)
		if err != nil {
			metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "config")
			r.abort(ctx, reservation, sb.ID, fmt.Sprintf("read workspace config: %v", err))
			return fmt.Errorf("PREVIEW_CONFIG_READ_FAILED: %w", err)
		}
		if cfg == nil {
			metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "config_missing")
			r.abort(ctx, reservation, sb.ID, previewNoConfigMessage)
			return fmt.Errorf("PREVIEW_NO_CONFIG: %s", previewNoConfigMessage)
		}
	}
	if target.PreviewConfigName != "" && cfg.Name == "" {
		cfg.Name = target.PreviewConfigName
	}
	metrics.RecordBranchPreviewPhaseDuration(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "config", time.Since(configStarted))
	if err := r.previews.UpdatePreviewTargetConfigDigest(ctx, payload.OrgID, payload.PreviewTargetID, computeConfigDigest(cfg)); err != nil {
		// Abort rather than warn: a stale digest on the target causes future
		// "config changed" comparisons to produce wrong results. If we can't
		// persist the resolved config digest, don't launch.
		metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "config_digest")
		r.abort(ctx, reservation, sb.ID, fmt.Sprintf("persist config digest: %v", err))
		return fmt.Errorf("persist config digest: %w", err)
	}

	input := StartPreviewInput{
		SessionID:                 uuid.Nil,
		PreviewTargetID:           payload.PreviewTargetID,
		OrgID:                     payload.OrgID,
		UserID:                    payload.UserID,
		Config:                    cfg,
		Sandbox:                   sb,
		RepositoryID:              target.RepositoryID,
		BaseCommitSHA:             target.CommitSHA,
		ProfileName:               payload.ProfileName,
		RequestID:                 derefStringPtr(target.RequestID),
		MetricsSource:             string(target.SourceType),
		MetricsRepositoryFullName: repo.FullName,
	}
	startupCacheKey := r.maybeRestoreBranchPreviewStartupCache(ctx, payload.OrgID, target.RepositoryID, target.CommitSHA, sb, cfg)
	metrics.AddBranchPreviewConcurrency(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, 1)
	defer metrics.AddBranchPreviewConcurrency(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, -1)
	_, err = r.manager.LaunchPreview(ctx, reservation, input)
	if err != nil {
		classified := ClassifyLaunchFailure(err)
		r.abort(ctx, reservation, sb.ID, classified.Message)
		r.logger.Warn().Err(err).Str("preview_id", payload.PreviewID.String()).Msg("branch preview launch failed")
		return fmt.Errorf("%s: %s: %w", classified.Code, classified.Message, err)
	}
	r.createBranchPreviewStartupCache(ctx, payload.OrgID, target.RepositoryID, startupCacheKey, sb, cfg)
	return nil
}

// StartReservedPreview completes one reserved preview. It marks the preview
// failed for all user-actionable and infrastructure startup failures so the UI
// can keep showing the persisted diagnostics after the job dead-letters.
func (r *StartRunner) StartReservedPreview(ctx context.Context, payload StartPreviewJobPayload) error {
	if r == nil || r.manager == nil || r.previews == nil || r.sessions == nil {
		return fmt.Errorf("preview start runner is not configured")
	}
	reservation, err := r.previews.GetPreviewInstance(ctx, payload.OrgID, payload.PreviewID)
	if err != nil {
		return fmt.Errorf("get reserved preview: %w", err)
	}
	if reservation.Status != models.PreviewStatusStarting {
		return fmt.Errorf("reserved preview is not starting (status=%s)", reservation.Status)
	}
	// Skip revision validation for jobs created before workspace revision tracking
	// was deployed (backward compatibility for rolling deploys).
	if !payload.WorkspaceRevisionUpdatedAt.IsZero() {
		if reservation.SourceWorkspaceRevision == nil ||
			*reservation.SourceWorkspaceRevision != payload.WorkspaceRevision {
			r.abort(ctx, reservation, "", "preview reservation no longer matches workspace revision")
			return fmt.Errorf("reserved preview workspace revision mismatch")
		}
	}
	if err := r.reassignReservationWorkerIfNeeded(ctx, payload.OrgID, payload.PreviewID, reservation); err != nil {
		return fmt.Errorf("reassign preview worker: %w", err)
	}

	session, err := r.sessions.GetByID(ctx, payload.OrgID, payload.SessionID)
	if err != nil {
		r.abort(ctx, reservation, "", fmt.Sprintf("session lookup: %v", err))
		return fmt.Errorf("get session: %w", err)
	}

	acq := r.acquireSandbox(ctx, payload.OrgID, &session, payload.Config)
	if acq.Err != nil {
		r.logger.Warn().Err(acq.Err).
			Str("session_id", payload.SessionID.String()).
			Str("error_code", acq.ErrCode).
			Msg("preview start job: failed to acquire sandbox")
		if errors.Is(acq.Err, ErrPreviewCapacity) {
			r.registerCapacityDeadLetter(ctx, reservation)
			return fmt.Errorf("%s: %w", acq.ErrCode, acq.Err)
		}
		if errors.Is(acq.Err, agent.ErrStaleSandboxIDCleared) {
			return fmt.Errorf("%s: %w", acq.ErrCodeOr("STALE_SANDBOX_CLEARED"), acq.Err)
		}
		if errors.Is(acq.Err, agent.ErrSandboxOnDifferentNode) {
			r.registerSandboxBusyDeadLetter(ctx, reservation)
			return fmt.Errorf("%s: %w", acq.ErrCodeOr("SANDBOX_WRONG_NODE"), acq.Err)
		}
		if acq.ErrCode == "SANDBOX_BUSY" {
			r.registerSandboxBusyDeadLetter(ctx, reservation)
			return fmt.Errorf("%s: %w: %v", acq.ErrCode, ErrSandboxBusy, acq.Err)
		}
		r.abort(ctx, reservation, "", fmt.Sprintf("acquire sandbox: %v", acq.Err))
		return fmt.Errorf("%s: %w", acq.ErrCodeOr("PREVIEW_HYDRATE_FAILED"), acq.Err)
	}

	input := StartPreviewInput{
		SessionID:                  payload.SessionID,
		OrgID:                      payload.OrgID,
		UserID:                     payload.UserID,
		Config:                     payload.Config,
		Sandbox:                    acq.Sandbox,
		RepositoryID:               uuidPointerValue(session.RepositoryID),
		BaseCommitSHA:              payload.BaseCommitSHA,
		ProfileName:                payload.ProfileName,
		WorkspaceRevision:          payload.WorkspaceRevision,
		WorkspaceRevisionUpdatedAt: payload.WorkspaceRevisionUpdatedAt,
	}
	hydratedID := ""
	if acq.Hydrated {
		hydratedID = acq.Sandbox.ID
	}

	if input.Config == nil {
		cfg, err := r.readWorkspacePreviewConfig(ctx, acq.Sandbox, payload.SessionID, "")
		if err != nil {
			if errors.Is(err, ErrInvalidConfig) {
				msg := InvalidConfigMessage(err)
				r.abort(ctx, reservation, hydratedID, msg)
				return fmt.Errorf("PREVIEW_CONFIG_INVALID: %s: %w", msg, err)
			}
			r.abort(ctx, reservation, hydratedID, fmt.Sprintf("read workspace config: %v", err))
			return fmt.Errorf("PREVIEW_CONFIG_READ_FAILED: %w", err)
		}
		if cfg == nil {
			r.abort(ctx, reservation, hydratedID, previewNoConfigMessage)
			return fmt.Errorf("PREVIEW_NO_CONFIG: %s", previewNoConfigMessage)
		}
		input.Config = cfg
	}

	_, err = r.manager.LaunchPreview(ctx, reservation, input)
	if err != nil {
		classified := ClassifyLaunchFailure(err)
		r.abort(ctx, reservation, hydratedID, classified.Message)
		r.logger.Warn().Err(err).Str("session_id", payload.SessionID.String()).Msg("preview launch failed")
		return fmt.Errorf("%s: %s: %w", classified.Code, classified.Message, err)
	}

	if r.nodeID != "" && (acq.Hydrated || (session.ContainerID != nil && *session.ContainerID != "")) {
		containerID := acq.Sandbox.ID
		if err := r.sessions.SetWorkerNodeIDForContainer(ctx, payload.OrgID, payload.SessionID, containerID, r.nodeID); err != nil {
			r.logger.Warn().Err(err).
				Str("session_id", payload.SessionID.String()).
				Str("container_id", containerID).
				Str("worker_node_id", r.nodeID).
				Msg("failed to persist session worker ownership")
		}
	}
	return nil
}

func (r *StartRunner) reassignReservationWorkerIfNeeded(ctx context.Context, orgID, previewID uuid.UUID, reservation *models.PreviewInstance) error {
	if reservation == nil || !shouldReassignPreviewWorker("", reservation.WorkerNodeID, r.nodeID) {
		return nil
	}
	endpointURL := ""
	if r.manager != nil {
		endpointURL = r.manager.previewInternalBaseURL
	}
	if err := r.previews.ReassignPreviewWorker(ctx, orgID, previewID, r.nodeID, endpointURL); err != nil {
		return err
	}
	reservation.WorkerNodeID = r.nodeID
	return nil
}

func shouldReassignPreviewWorker(_ string, reservationWorkerNode, claimingWorkerNode string) bool {
	return claimingWorkerNode != "" && claimingWorkerNode != reservationWorkerNode
}

func (r *StartRunner) registerCapacityDeadLetter(ctx context.Context, reservation *models.PreviewInstance) {
	if r == nil || r.manager == nil || reservation == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		if deadLetterErr != nil {
			r.logger.Warn().Err(deadLetterErr).
				Str("preview_id", reservation.ID.String()).
				Str("session_id", reservation.SessionID.String()).
				Msg("preview start dead-lettered after capacity retries")
		}
		r.manager.AbortReservation(hookCtx, reservation, "", PreviewCapacityRetryExhaustedMessage)
	})
}

func (r *StartRunner) registerSandboxBusyDeadLetter(ctx context.Context, reservation *models.PreviewInstance) {
	if r == nil || r.manager == nil || reservation == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		if deadLetterErr != nil {
			r.logger.Warn().Err(deadLetterErr).
				Str("preview_id", reservation.ID.String()).
				Str("session_id", reservation.SessionID.String()).
				Msg("preview start dead-lettered after sandbox-busy retries")
		}
		r.manager.AbortReservation(hookCtx, reservation, "", "Preview could not start because the session sandbox stayed busy. Try again after the current agent turn finishes.")
	})
}

func (r *StartRunner) abort(ctx context.Context, reservation *models.PreviewInstance, hydratedID, reason string) {
	if r.manager == nil || reservation == nil {
		return
	}
	r.manager.AbortReservation(ctx, reservation, hydratedID, reason)
}

type StartFailure struct {
	Code    string
	Message string
}

func ClassifyLaunchFailure(err error) StartFailure {
	if err == nil {
		return StartFailure{}
	}
	cause := err.Error()
	switch {
	case errors.Is(err, ErrInfraImageUnavailable):
		return StartFailure{Code: "PREVIEW_INFRA_IMAGE_UNAVAILABLE", Message: "preview infrastructure image is not available on this worker. The image could not be pulled from its registry — check the worker's network egress and registry credentials. Details: " + cause}
	case errors.Is(err, ErrInfraStartFailed):
		return StartFailure{Code: "PREVIEW_INFRA_START_FAILED", Message: "preview infrastructure container failed to start. Details: " + cause}
	case errors.Is(err, ErrInfraUnhealthy):
		return StartFailure{Code: "PREVIEW_INFRA_UNHEALTHY", Message: "preview infrastructure container did not become healthy in time. The container started but its health check (e.g. pg_isready) never passed. Details: " + cause}
	case errors.Is(err, ErrInitScriptFailed):
		return StartFailure{Code: "PREVIEW_INIT_SCRIPT_FAILED", Message: "preview init script failed. Check the script referenced in .143/config.json. Details: " + cause}
	case errors.Is(err, ErrInstallFailed):
		return StartFailure{Code: "PREVIEW_INSTALL_FAILED", Message: "preview install failed before services started. Check the preview.install command in .143/config.json. Details: " + cause}
	case errors.Is(err, ErrServiceNotReady):
		return StartFailure{Code: "PREVIEW_SERVICE_NOT_READY", Message: "preview service did not pass its readiness probe. The service may have crashed at boot, taken too long to start, or be listening on a different port than declared in .143/config.json. Details: " + cause}
	case errors.Is(err, ErrInvalidConfig):
		return StartFailure{Code: "PREVIEW_CONFIG_INVALID", Message: InvalidConfigMessage(err)}
	default:
		return StartFailure{Code: "PREVIEW_START_FAILED", Message: "failed to start preview: " + cause}
	}
}

type acquireSandboxResult struct {
	Sandbox  *agent.Sandbox
	Hydrated bool
	ErrCode  string
	Err      error
}

func (r acquireSandboxResult) ErrCodeOr(fallback string) string {
	if r.ErrCode != "" {
		return r.ErrCode
	}
	return fallback
}

func (r *StartRunner) resolveSandboxWorkDir(ctx context.Context, session *models.Session) string {
	defaults := agent.DefaultSandboxConfig()
	if session.RepositoryID == nil || r.repositories == nil {
		return defaults.WorkDir
	}
	repo, err := r.repositories.GetByID(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		r.logger.Warn().Err(err).
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

func (r *StartRunner) applyBranchPreviewSandboxNetwork(ctx context.Context, orgID uuid.UUID, cfg *agent.SandboxConfig) error {
	if r == nil {
		return nil
	}
	return agent.ApplyOrgSandboxNetworkSettings(ctx, r.orgs, orgID, r.staticEgress, cfg)
}

func (r *StartRunner) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session, cfg *models.PreviewConfig) acquireSandboxResult {
	workDir := r.resolveSandboxWorkDir(ctx, session)
	expectedNetwork, expectedErr := agent.ExpectedSandboxNetwork(ctx, r.orgs, orgID, r.staticEgress)
	if expectedErr != nil {
		return acquireSandboxResult{ErrCode: "STATIC_EGRESS_UNAVAILABLE", Err: expectedErr}
	}
	if session.ContainerID != nil && *session.ContainerID != "" &&
		session.SandboxState == models.SandboxStateRunning {
		if ownerCheck := r.checkLiveContainerWorker(session.WorkerNodeID); ownerCheck.Err != nil {
			return ownerCheck
		}
		candidate := &agent.Sandbox{
			ID:        *session.ContainerID,
			Provider:  "docker",
			WorkDir:   workDir,
			SessionID: session.ID.String(),
			OrgID:     session.OrgID.String(),
			Purpose:   "preview",
		}
		if r.sandboxProvider != nil {
			alive, inspectErr := r.sandboxProvider.IsAlive(ctx, candidate)
			if inspectErr != nil {
				r.logger.Warn().Err(inspectErr).Str("session_id", session.ID.String()).Str("container_id", candidate.ID).Msg("preview reuse: liveness check failed; falling through to hydrate")
			} else if alive {
				if match, mismatchErr := agent.SandboxNetworkMatches(ctx, r.sandboxProvider, candidate, expectedNetwork, r.staticEgress.NetworkName); mismatchErr != nil {
					r.logger.Warn().Err(mismatchErr).Str("session_id", session.ID.String()).Str("container_id", candidate.ID).Msg("preview reuse: network check failed; falling through to hydrate")
				} else if !match {
					return acquireSandboxResult{ErrCode: "NETWORK_SETTING_RESTART_REQUIRED", Err: fmt.Errorf("restart environment to apply network setting")}
				} else {
					return acquireSandboxResult{Sandbox: candidate}
				}
			}
		} else {
			return acquireSandboxResult{Sandbox: candidate}
		}
	}

	if session.SandboxState == models.SandboxStateDestroyed {
		return acquireSandboxResult{ErrCode: "SNAPSHOT_EXPIRED", Err: fmt.Errorf("this session's sandbox snapshot has expired; send a new message to rebuild it")}
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return acquireSandboxResult{ErrCode: "SNAPSHOT_UNAVAILABLE", Err: fmt.Errorf("session has no live sandbox and no saved snapshot; send a new message to rebuild it")}
	}
	if r.sandboxProvider == nil || r.snapshots == nil {
		return acquireSandboxResult{ErrCode: "NO_SANDBOX", Err: fmt.Errorf("preview hydrate is not configured on this worker")}
	}

	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = workDir
	sandboxCfg.SessionID = session.ID.String()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.Purpose = "preview_hydrate"
	ApplyResourceLimitsToSandboxConfig(&sandboxCfg, cfg)
	if err := agent.ApplyOrgSandboxNetworkSettings(ctx, r.orgs, orgID, r.staticEgress, &sandboxCfg); err != nil {
		return acquireSandboxResult{ErrCode: "STATIC_EGRESS_UNAVAILABLE", Err: err}
	}

	winningID, winningWorkerID, freshErr := r.sessions.PeekContainerOwnership(ctx, orgID, session.ID)
	switch {
	case freshErr != nil:
		r.logger.Warn().Err(freshErr).Str("session_id", session.ID.String()).Msg("preview hydrate: pre-hydrate peek failed; falling through to CAS race detection")
	case winningID != "":
		if ownerCheck := r.checkLiveContainerWorker(&winningWorkerID); ownerCheck.Err != nil {
			return ownerCheck
		}
		if cleared, clearErr := r.clearStalePreviewContainer(ctx, orgID, session, winningID, workDir); clearErr != nil {
			return acquireSandboxResult{ErrCode: "STALE_SANDBOX_CLEAR_FAILED", Err: clearErr}
		} else if cleared {
			return acquireSandboxResult{ErrCode: "STALE_SANDBOX_CLEARED", Err: fmt.Errorf("%w", agent.ErrStaleSandboxIDCleared)}
		}
		return acquireSandboxResult{ErrCode: "SANDBOX_BUSY", Err: fmt.Errorf("another process attached to this session's sandbox first; please retry")}
	}

	var capacityReservation *agent.SandboxCapacityReservation
	if r.sandboxCapacity != nil {
		var capErr error
		capacityReservation, capErr = r.sandboxCapacity.Acquire(ctx, agent.SandboxCapacityRequest{
			Purpose: sandboxCfg.Purpose, SessionID: sandboxCfg.SessionID, OrgID: sandboxCfg.OrgID,
		})
		if capErr != nil {
			return acquireSandboxResult{ErrCode: PreviewCapacityCode, Err: fmt.Errorf("%w: %w", ErrPreviewCapacity, capErr)}
		}
		defer capacityReservation.Release()
	}

	sandbox, err := agent.HydrateSandboxFromSnapshot(ctx, r.sandboxProvider, r.snapshots, *session.SnapshotKey, sandboxCfg)
	if err != nil {
		if errors.Is(err, agent.ErrSnapshotMissing) {
			return acquireSandboxResult{ErrCode: "SNAPSHOT_UNAVAILABLE", Err: fmt.Errorf("session snapshot is unavailable in storage; send a new message to rebuild it")}
		}
		return acquireSandboxResult{Err: fmt.Errorf("hydrate sandbox: %w", err)}
	}

	actualID, err := r.sessions.PublishHydratedContainerID(ctx, orgID, session.ID, sandbox.ID)
	if err != nil {
		destroyCtx, destroyCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = r.sandboxProvider.Destroy(destroyCtx, sandbox)
		destroyCancel()
		return acquireSandboxResult{Err: fmt.Errorf("publish container id: %w", err)}
	}
	if actualID != sandbox.ID {
		destroyCtx, destroyCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = r.sandboxProvider.Destroy(destroyCtx, sandbox)
		destroyCancel()
		return acquireSandboxResult{ErrCode: "SANDBOX_BUSY", Err: fmt.Errorf("another process attached to this session's sandbox first; please retry")}
	}

	return acquireSandboxResult{Sandbox: sandbox, Hydrated: true}
}

func (r *StartRunner) checkLiveContainerWorker(workerNodeID *string) acquireSandboxResult {
	if r == nil || r.nodeID == "" {
		return acquireSandboxResult{}
	}
	owner := ""
	if workerNodeID != nil {
		owner = strings.TrimSpace(*workerNodeID)
	}
	if owner == "" {
		return acquireSandboxResult{
			ErrCode: "SANDBOX_BUSY",
			Err:     fmt.Errorf("%w: session sandbox worker ownership is not recorded yet", ErrSandboxBusy),
		}
	}
	if owner != r.nodeID {
		return acquireSandboxResult{
			ErrCode: "SANDBOX_WRONG_NODE",
			Err:     fmt.Errorf("%w: session sandbox belongs to worker %s", agent.ErrSandboxOnDifferentNode, owner),
		}
	}
	return acquireSandboxResult{}
}

func (r *StartRunner) clearStalePreviewContainer(ctx context.Context, orgID uuid.UUID, session *models.Session, containerID, workDir string) (bool, error) {
	if r == nil || r.sandboxProvider == nil || r.sessions == nil || session == nil || containerID == "" {
		return false, nil
	}
	candidate := &agent.Sandbox{
		ID:        containerID,
		Provider:  "docker",
		WorkDir:   workDir,
		SessionID: session.ID.String(),
		OrgID:     session.OrgID.String(),
		Purpose:   "preview_hydrate",
	}
	alive, inspectErr := r.sandboxProvider.IsAlive(ctx, candidate)
	if inspectErr != nil {
		r.logger.Warn().Err(inspectErr).
			Str("session_id", session.ID.String()).
			Str("container_id", containerID).
			Msg("preview hydrate: stale container probe failed; leaving row for retry/reconciler")
		return false, nil
	}
	if alive {
		return false, nil
	}
	cleared, clearErr := r.sessions.ClearContainerID(ctx, orgID, session.ID, containerID)
	if clearErr != nil {
		r.logger.Warn().Err(clearErr).
			Str("session_id", session.ID.String()).
			Str("container_id", containerID).
			Msg("preview hydrate: failed to clear stale container_id")
		return false, fmt.Errorf("clear stale container_id: %w", clearErr)
	}
	if !cleared {
		r.logger.Info().
			Str("session_id", session.ID.String()).
			Str("container_id", containerID).
			Msg("preview hydrate: stale container clear lost CAS")
		return false, nil
	}
	r.logger.Info().
		Str("session_id", session.ID.String()).
		Str("container_id", containerID).
		Msg("preview hydrate: cleared stale container_id; retrying against clean row")
	return true, nil
}

func (r *StartRunner) readWorkspacePreviewConfig(ctx context.Context, sb *agent.Sandbox, sessionID uuid.UUID, previewConfigName string) (*models.PreviewConfig, error) {
	if r.fileReader == nil {
		return nil, nil
	}
	content, _, err := r.fileReader.ReadFile(ctx, sb.ID, sb.WorkDir, repoconfig.ConfigPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrFileNotFound) {
			r.logger.Debug().Str("session_id", sessionID.String()).Str("path", repoconfig.ConfigPath).Msg("no committed preview config in workspace")
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", repoconfig.ConfigPath, err)
	}
	cfg, err := ParseNamedConfig([]byte(content), previewConfigName)
	if err != nil {
		r.logger.Warn().Err(err).Str("session_id", sessionID.String()).Str("path", repoconfig.ConfigPath).Msg("committed preview config failed to parse")
		return nil, fmt.Errorf("%w: parse %s: %w", ErrInvalidConfig, repoconfig.ConfigPath, err)
	}
	return cfg, nil
}

func (r *StartRunner) maybeRestoreBranchPreviewStartupCache(ctx context.Context, orgID, repoID uuid.UUID, commitSHA string, sb *agent.Sandbox, cfg *models.PreviewConfig) string {
	if r == nil || r.snapshotCache == nil || r.sandboxProvider == nil || sb == nil || cfg == nil || commitSHA == "" {
		return ""
	}
	if previewConfigHasRuntimeSecretFiles(cfg) {
		// Runtime secret files are written into the shared workspace during
		// LaunchPreview. The startup cache snapshots that workspace after
		// launch, so do not read or write cache entries for these configs:
		// otherwise worker-local cache blobs could retain plaintext secrets.
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Msg("branch preview startup cache skipped because config delivers preview secrets as files")
		return ""
	}
	snapshotKey, err := r.computeBranchPreviewStartupCacheKey(ctx, sb, cfg, commitSHA)
	if err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("commit_sha", commitSHA).
			Msg("branch preview startup cache key unavailable; launching cold")
		return ""
	}
	hit, err := r.snapshotCache.FindSnapshot(ctx, orgID, repoID, snapshotKey)
	if err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", snapshotKey).
			Msg("branch preview startup cache lookup failed; launching cold")
		return snapshotKey
	}
	if hit == nil {
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Str("snapshot_key", snapshotKey).
			Msg("branch preview startup cache miss")
		return snapshotKey
	}
	if err := r.snapshotCache.RestoreSnapshot(ctx, sb, hit); err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", snapshotKey).
			Msg("branch preview startup cache restore failed; launching cold")
		return snapshotKey
	}
	r.logger.Info().
		Str("repository_id", repoID.String()).
		Str("snapshot_key", snapshotKey).
		Msg("branch preview startup cache restored")
	return snapshotKey
}

func (r *StartRunner) createBranchPreviewStartupCache(ctx context.Context, orgID, repoID uuid.UUID, snapshotKey string, sb *agent.Sandbox, cfg *models.PreviewConfig) {
	if r == nil || r.snapshotCache == nil || sb == nil || snapshotKey == "" {
		return
	}
	if previewConfigHasRuntimeSecretFiles(cfg) {
		// See maybeRestoreBranchPreviewStartupCache for why secret-file
		// configs are excluded from the workspace snapshot cache.
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Str("snapshot_key", snapshotKey).
			Msg("branch preview startup cache creation skipped because config delivers preview secrets as files")
		return
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	if err := r.snapshotCache.CreateSnapshot(cacheCtx, sb, snapshotKey, SnapshotMetadata{OrgID: orgID, RepoID: repoID}); err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", snapshotKey).
			Msg("failed to create branch preview startup cache")
	}
}

func (r *StartRunner) computeBranchPreviewStartupCacheKey(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, commitSHA string) (string, error) {
	lockfiles := branchPreviewStartupCacheLockfiles(cfg)
	var lockInput bytes.Buffer
	for _, lockfile := range lockfiles {
		cleanPath, err := cleanBranchPreviewStartupCachePath(lockfile)
		if err != nil {
			return "", fmt.Errorf("preview.install.lockfiles path %q: %w", lockfile, err)
		}
		body, err := r.sandboxProvider.ReadFile(ctx, sb, cleanPath)
		if err != nil {
			return "", fmt.Errorf("read preview.install lockfile %q: %w", cleanPath, err)
		}
		lockInput.WriteString(cleanPath)
		lockInput.WriteByte(0)
		lockInput.Write(body)
		lockInput.WriteByte(0)
	}
	return ComputeSnapshotKey(lockInput.Bytes(), commitSHA, computeConfigDigest(cfg)), nil
}

func branchPreviewStartupCacheLockfiles(cfg *models.PreviewConfig) []string {
	if cfg == nil || cfg.Install == nil || len(cfg.Install.Lockfiles) == 0 {
		return nil
	}
	lockfiles := append([]string(nil), cfg.Install.Lockfiles...)
	sort.Strings(lockfiles)
	return lockfiles
}

func previewConfigHasRuntimeSecretFiles(cfg *models.PreviewConfig) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.RuntimeSecretFiles) > 0 {
		return true
	}
	for _, ref := range SecretBundleRefs(cfg) {
		if len(ref.Files) > 0 {
			return true
		}
	}
	return false
}

func cleanBranchPreviewStartupCachePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("path must be relative")
	}
	clean := path.Clean(trimmed)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("path must stay inside the repository")
	}
	return clean, nil
}
