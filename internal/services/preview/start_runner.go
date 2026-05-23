package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
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

// StartRunner completes durable preview startup jobs after the API has
// reserved the preview row and enqueued start_preview.
type StartRunner struct {
	manager         *Manager
	previews        *db.PreviewStore
	sessions        *db.SessionStore
	repositories    *db.RepositoryStore
	fileReader      sandbox.FileReader
	sandboxProvider agent.SandboxProvider
	sandboxCapacity *agent.SandboxCapacityGate
	snapshots       storage.SnapshotStore
	github          branchPreviewGitHub
	nodeID          string
	logger          zerolog.Logger
}

type StartRunnerConfig struct {
	Manager         *Manager
	Previews        *db.PreviewStore
	Sessions        *db.SessionStore
	Repositories    *db.RepositoryStore
	FileReader      sandbox.FileReader
	SandboxProvider agent.SandboxProvider
	SandboxCapacity *agent.SandboxCapacityGate
	Snapshots       storage.SnapshotStore
	GitHub          branchPreviewGitHub
	NodeID          string
	Logger          zerolog.Logger
}

type branchPreviewGitHub interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

func NewStartRunner(cfg StartRunnerConfig) *StartRunner {
	return &StartRunner{
		manager:         cfg.Manager,
		previews:        cfg.Previews,
		sessions:        cfg.Sessions,
		repositories:    cfg.Repositories,
		fileReader:      cfg.FileReader,
		sandboxProvider: cfg.SandboxProvider,
		sandboxCapacity: cfg.SandboxCapacity,
		snapshots:       cfg.Snapshots,
		github:          cfg.GitHub,
		nodeID:          cfg.NodeID,
		logger:          cfg.Logger,
	}
}

var gitCommitSHARe = regexp.MustCompile(`\A[0-9a-fA-F]{7,40}\z`)

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
	if deadTarget, ok := jobctx.DeadTargetNodeFromContext(ctx); ok && shouldReassignPreviewWorker(deadTarget, reservation.WorkerNodeID, r.nodeID) {
		if err := r.previews.UpdatePreviewWorkerNodeID(ctx, payload.OrgID, payload.PreviewID, r.nodeID); err != nil {
			return fmt.Errorf("reassign branch preview worker: %w", err)
		}
		reservation.WorkerNodeID = r.nodeID
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
		r.logger.Warn().Err(err).
			Str("preview_id", payload.PreviewID.String()).
			Str("preview_target_id", payload.PreviewTargetID.String()).
			Msg("failed to persist resolved preview config digest")
	}

	input := StartPreviewInput{
		SessionID:                 uuid.Nil,
		PreviewTargetID:           payload.PreviewTargetID,
		OrgID:                     payload.OrgID,
		UserID:                    payload.UserID,
		Config:                    cfg,
		Sandbox:                   sb,
		BaseCommitSHA:             target.CommitSHA,
		ProfileName:               payload.ProfileName,
		RequestID:                 target.RequestID,
		MetricsSource:             string(target.SourceType),
		MetricsRepositoryFullName: repo.FullName,
	}
	metrics.AddBranchPreviewConcurrency(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, 1)
	defer metrics.AddBranchPreviewConcurrency(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, -1)
	_, err = r.manager.LaunchPreview(ctx, reservation, input)
	if err != nil {
		classified := ClassifyLaunchFailure(err)
		r.abort(ctx, reservation, sb.ID, classified.Message)
		r.logger.Warn().Err(err).Str("preview_id", payload.PreviewID.String()).Msg("branch preview launch failed")
		return fmt.Errorf("%s: %s: %w", classified.Code, classified.Message, err)
	}
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
	if deadTarget, ok := jobctx.DeadTargetNodeFromContext(ctx); ok && shouldReassignPreviewWorker(deadTarget, reservation.WorkerNodeID, r.nodeID) {
		if err := r.previews.UpdatePreviewWorkerNodeID(ctx, payload.OrgID, payload.PreviewID, r.nodeID); err != nil {
			return fmt.Errorf("reassign preview worker: %w", err)
		}
		reservation.WorkerNodeID = r.nodeID
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
		r.abort(ctx, reservation, "", fmt.Sprintf("acquire sandbox: %v", acq.Err))
		return fmt.Errorf("%s: %w", acq.ErrCodeOr("PREVIEW_HYDRATE_FAILED"), acq.Err)
	}

	input := StartPreviewInput{
		SessionID:     payload.SessionID,
		OrgID:         payload.OrgID,
		UserID:        payload.UserID,
		Config:        payload.Config,
		Sandbox:       acq.Sandbox,
		BaseCommitSHA: payload.BaseCommitSHA,
		ProfileName:   payload.ProfileName,
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

func shouldReassignPreviewWorker(deadTargetNode, reservationWorkerNode, claimingWorkerNode string) bool {
	return deadTargetNode != "" && claimingWorkerNode != "" && claimingWorkerNode != reservationWorkerNode
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

func (r *StartRunner) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session, cfg *models.PreviewConfig) acquireSandboxResult {
	workDir := r.resolveSandboxWorkDir(ctx, session)
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
		if r.sandboxProvider != nil {
			alive, inspectErr := r.sandboxProvider.IsAlive(ctx, candidate)
			if inspectErr != nil {
				r.logger.Warn().Err(inspectErr).Str("session_id", session.ID.String()).Str("container_id", candidate.ID).Msg("preview reuse: liveness check failed; falling through to hydrate")
			} else if alive {
				return acquireSandboxResult{Sandbox: candidate}
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

	winningID, freshErr := r.sessions.PeekContainerID(ctx, orgID, session.ID)
	switch {
	case freshErr != nil:
		r.logger.Warn().Err(freshErr).Str("session_id", session.ID.String()).Msg("preview hydrate: pre-hydrate peek failed; falling through to CAS race detection")
	case winningID != "":
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
		_ = r.sandboxProvider.Destroy(context.Background(), sandbox)
		return acquireSandboxResult{Err: fmt.Errorf("publish container id: %w", err)}
	}
	if actualID != sandbox.ID {
		_ = r.sandboxProvider.Destroy(context.Background(), sandbox)
		return acquireSandboxResult{ErrCode: "SANDBOX_BUSY", Err: fmt.Errorf("another process attached to this session's sandbox first; please retry")}
	}

	return acquireSandboxResult{Sandbox: sandbox, Hydrated: true}
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
