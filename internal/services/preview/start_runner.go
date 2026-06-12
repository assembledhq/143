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
var ErrPreviewCachePrewarmCapacitySkipped = errors.New("preview cache prewarm skipped because sandbox capacity is unavailable")

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
	dependencyCache PreviewPathCache
	prewarmEnabled  bool
	prewarmTimeout  time.Duration
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
	DependencyCache PreviewPathCache
	PrewarmEnabled  bool
	PrewarmTimeout  time.Duration
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
		dependencyCache: cfg.DependencyCache,
		prewarmEnabled:  cfg.PrewarmEnabled,
		prewarmTimeout:  cfg.PrewarmTimeout,
		logger:          cfg.Logger,
	}
}

var gitCommitSHARe = regexp.MustCompile(`\A[0-9a-fA-F]{7,40}\z`)

type previewStartupCache interface {
	FindSnapshot(ctx context.Context, orgID, repoID uuid.UUID, snapshotKey string) (*CacheHit, error)
	FindBaseSnapshot(ctx context.Context, orgID, repoID uuid.UUID, baseKey, excludeCommitSHA string) (*CacheHit, error)
	RestoreSnapshot(ctx context.Context, sb *agent.Sandbox, hit *CacheHit) error
	ApplyPartialInvalidation(ctx context.Context, sb *agent.Sandbox, hit *CacheHit, gitDiff []byte) error
	CreateSnapshot(ctx context.Context, sb *agent.Sandbox, snapshotKey string, metadata SnapshotMetadata) error
}

// branchPreviewStartupCacheKeys carries the cache keys computed for one branch
// preview start. The zero value means caching is disabled for this start.
type branchPreviewStartupCacheKeys struct {
	SnapshotKey string
	BaseKey     string
	CommitSHA   string
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
	ApplyPreviewInstanceResourceLimitsToSandboxConfig(&sandboxCfg, reservation)
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
	startupCacheKeys, cacheErr := r.maybeRestoreBranchPreviewStartupCache(ctx, payload.OrgID, target.RepositoryID, target.CommitSHA, sb, cfg)
	if cacheErr != nil {
		// Only unrecoverable workspace states reach here (a failed partial
		// restore that could not be re-checked-out from git). Launching from
		// an inconsistent tree would serve wrong code — fail the start.
		metrics.RecordBranchPreviewStartupFailure(ctx, payload.OrgID.String(), string(target.SourceType), repo.FullName, "startup_cache_recovery")
		r.abort(ctx, reservation, sb.ID, fmt.Sprintf("restore startup cache: %v", cacheErr))
		return fmt.Errorf("restore startup cache: %w", cacheErr)
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
	if r.createBranchPreviewStartupCache(ctx, payload.OrgID, target.RepositoryID, startupCacheKeys, sb, cfg) {
		if err := r.previews.UpdatePreviewTargetSnapshotKey(ctx, payload.OrgID, payload.PreviewTargetID, startupCacheKeys.SnapshotKey); err != nil {
			r.logger.Warn().Err(err).
				Str("preview_target_id", payload.PreviewTargetID.String()).
				Str("snapshot_key", startupCacheKeys.SnapshotKey).
				Msg("failed to persist branch preview startup snapshot key")
		}
	}
	if payload.StopAfterReady {
		if err := r.manager.StopPreviewWithReason(ctx, payload.OrgID, payload.PreviewID, models.PreviewStoppedReasonWarmPolicy); err != nil {
			r.logger.Warn().Err(err).
				Str("preview_id", payload.PreviewID.String()).
				Msg("failed to stop warm-policy branch preview after startup snapshot")
		}
	}
	return nil
}

func (r *StartRunner) PrewarmPreviewCaches(ctx context.Context, payload PreviewCachePrewarmJobPayload) error {
	if r == nil || r.manager == nil || r.sandboxProvider == nil {
		return fmt.Errorf("preview cache prewarm runner is not configured")
	}
	if !r.prewarmEnabled {
		r.logger.Debug().Msg("preview cache prewarm skipped because runtime flag is disabled")
		return nil
	}
	scopeKey := previewCachePrewarmScopeKey(payload)
	if scopeKey == "" {
		return fmt.Errorf("preview cache prewarm payload is missing scope identity")
	}
	r.createPreviewCachePrewarmRun(ctx, payload, scopeKey)
	r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "running", "", "", "", "", false)
	started := time.Now()
	timeout := r.prewarmTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sb, cfg, cleanup, err := r.preparePreviewCachePrewarmSandbox(runCtx, payload)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		if errors.Is(err, ErrPreviewCachePrewarmCapacitySkipped) {
			r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_capacity", "", "", "", err.Error(), true)
			metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_capacity", time.Since(started))
			return err
		}
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "failed", "", "", "", err.Error(), true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "failed", time.Since(started))
		return fmt.Errorf("prepare preview cache prewarm sandbox: %w", err)
	}
	if sb == nil || cfg == nil || cfg.Install == nil {
		r.logger.Debug().Msg("preview cache prewarm skipped because preview install config is absent")
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_no_install", "", "", "", "", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_no_install", time.Since(started))
		return nil
	}
	if !previewInstallPrewarmEnabled(cfg.Install) {
		r.logger.Debug().Msg("preview cache prewarm skipped by repo config")
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_disabled", "", "", "", "", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_disabled", time.Since(started))
		return nil
	}
	if len(cfg.Install.Lockfiles) == 0 {
		r.logger.Debug().Msg("preview cache prewarm skipped because install lockfiles are absent")
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_no_lockfiles", "", "", "", "", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_no_lockfiles", time.Since(started))
		return nil
	}
	dependencyPaths, dependencyEnabled := ResolvePreviewInstallCachePaths(cfg.Install)
	packageManagerPaths, packageManagers, packageManagerEnabled := ResolvePreviewInstallPackageManagerCachePaths(cfg.Install)
	if (!dependencyEnabled || len(dependencyPaths) == 0) && (!packageManagerEnabled || len(packageManagerPaths) == 0) {
		r.logger.Debug().Msg("preview cache prewarm skipped because no effective cache paths were found")
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_no_paths", "", "", "", "", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_no_paths", time.Since(started))
		return nil
	}
	opts := StartPreviewOptions{
		OrgID:        payload.OrgID,
		RepositoryID: payload.RepositoryID,
		SessionID:    payload.SessionID,
		ConfigDigest: payload.ConfigDigest,
	}
	if opts.ConfigDigest == "" {
		opts.ConfigDigest = computeConfigDigest(cfg)
	}
	dependencyCacheKey, packageManagerCacheKey := r.previewCachePrewarmKeys(runCtx, sb, cfg, dependencyEnabled, dependencyPaths, packageManagerEnabled, packageManagerPaths, packageManagers)
	r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "running", packageManagerCacheKey, dependencyCacheKey, opts.ConfigDigest, "", false)
	if warm, warmErr := r.previewCachesAlreadyWarm(runCtx, sb, cfg, opts, dependencyEnabled, dependencyPaths, packageManagerEnabled, packageManagerPaths, packageManagers); warmErr != nil {
		r.logger.Warn().Err(warmErr).Msg("preview cache prewarm warm-check failed; continuing")
	} else if warm {
		r.logger.Info().
			Str("repository_id", payload.RepositoryID.String()).
			Str("source", string(payload.Source)).
			Msg("preview cache prewarm skipped because caches are already warm")
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "skipped_warm", packageManagerCacheKey, dependencyCacheKey, opts.ConfigDigest, "", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "skipped_warm", time.Since(started))
		return nil
	}
	prewarmProvider, ok := r.manager.provider.(PreviewCachePrewarmProvider)
	if !ok || prewarmProvider == nil {
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "failed", packageManagerCacheKey, dependencyCacheKey, opts.ConfigDigest, "preview provider does not support cache prewarm", true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "failed", time.Since(started))
		return fmt.Errorf("preview provider does not support cache prewarm")
	}
	if err := prewarmProvider.PrewarmPreviewInstallCaches(runCtx, sb, cfg, opts, nil); err != nil {
		r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "failed", packageManagerCacheKey, dependencyCacheKey, opts.ConfigDigest, err.Error(), true)
		metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "failed", time.Since(started))
		return fmt.Errorf("prewarm preview install caches: %w", err)
	}
	r.updatePreviewCachePrewarmRun(ctx, payload, scopeKey, "succeeded", packageManagerCacheKey, dependencyCacheKey, opts.ConfigDigest, "", true)
	metrics.RecordPreviewCachePrewarmRun(ctx, payload.OrgID.String(), string(payload.Source), "succeeded", time.Since(started))
	return nil
}

func (r *StartRunner) preparePreviewCachePrewarmSandbox(ctx context.Context, payload PreviewCachePrewarmJobPayload) (*agent.Sandbox, *models.PreviewConfig, func(), error) {
	switch payload.Source {
	case PreviewCachePrewarmSourceBranch:
		return r.prepareBranchPreviewCachePrewarmSandbox(ctx, payload)
	case PreviewCachePrewarmSourceSession:
		return r.prepareSessionPreviewCachePrewarmSandbox(ctx, payload)
	default:
		return nil, nil, nil, fmt.Errorf("invalid prewarm source %q", payload.Source)
	}
}

func (r *StartRunner) prepareBranchPreviewCachePrewarmSandbox(ctx context.Context, payload PreviewCachePrewarmJobPayload) (*agent.Sandbox, *models.PreviewConfig, func(), error) {
	if r.previews == nil || r.repositories == nil || r.github == nil {
		return nil, nil, nil, fmt.Errorf("branch preview cache prewarm dependencies are not configured")
	}
	if payload.PreviewTargetID == uuid.Nil || payload.CommitSHA == "" || !gitCommitSHARe.MatchString(payload.CommitSHA) {
		return nil, nil, nil, fmt.Errorf("branch preview cache prewarm payload is invalid")
	}
	target, err := r.previews.GetPreviewTarget(ctx, payload.OrgID, payload.PreviewTargetID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get preview target: %w", err)
	}
	if target.RepositoryID != payload.RepositoryID || target.CommitSHA != payload.CommitSHA {
		return nil, nil, nil, fmt.Errorf("preview target payload mismatch")
	}
	repo, err := r.repositories.GetByID(ctx, payload.OrgID, payload.RepositoryID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get repository: %w", err)
	}
	if !repo.IsActive() {
		return nil, nil, nil, fmt.Errorf("repository is disconnected")
	}

	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = sandboxCfg.HomeDir + "/" + agent.SlugForRepo(repo.FullName)
	sandboxCfg.SessionID = payload.PreviewTargetID.String()
	sandboxCfg.OrgID = payload.OrgID.String()
	sandboxCfg.Purpose = "preview_cache_prewarm"
	if err := r.applyBranchPreviewSandboxNetwork(ctx, payload.OrgID, &sandboxCfg); err != nil {
		return nil, nil, nil, fmt.Errorf("resolve sandbox network: %w", err)
	}
	release, err := r.acquirePrewarmCapacity(ctx, sandboxCfg.Purpose, sandboxCfg.SessionID, sandboxCfg.OrgID)
	if err != nil {
		return nil, nil, nil, err
	}
	sb, err := r.sandboxProvider.Create(ctx, sandboxCfg)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, nil, fmt.Errorf("create sandbox: %w", err)
	}
	cleanup := r.previewCachePrewarmCleanup(sb, release)
	token, err := r.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("get github token: %w", err)
	}
	if err := r.sandboxProvider.CloneRepo(ctx, sb, repo.CloneURL, target.Branch, token); err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("clone repository: %w", err)
	}
	var checkoutErr bytes.Buffer
	exitCode, err := r.sandboxProvider.Exec(ctx, sb, "git checkout --detach "+target.CommitSHA, io.Discard, &checkoutErr)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("checkout commit: %w", err)
	}
	if exitCode != 0 {
		cleanup()
		return nil, nil, nil, fmt.Errorf("checkout commit failed with code %d: %s", exitCode, checkoutErr.String())
	}
	cfg, err := r.readWorkspacePreviewConfig(ctx, sb, uuid.Nil, target.PreviewConfigName)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	if cfg != nil && target.PreviewConfigName != "" && cfg.Name == "" {
		cfg.Name = target.PreviewConfigName
	}
	return sb, cfg, cleanup, nil
}

func (r *StartRunner) prepareSessionPreviewCachePrewarmSandbox(ctx context.Context, payload PreviewCachePrewarmJobPayload) (*agent.Sandbox, *models.PreviewConfig, func(), error) {
	if r.sessions == nil || r.snapshots == nil {
		return nil, nil, nil, fmt.Errorf("session preview cache prewarm dependencies are not configured")
	}
	if payload.SessionID == uuid.Nil {
		return nil, nil, nil, fmt.Errorf("session preview cache prewarm payload is invalid")
	}
	session, err := r.sessions.GetByID(ctx, payload.OrgID, payload.SessionID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get session: %w", err)
	}
	if session.RepositoryID == nil || *session.RepositoryID != payload.RepositoryID {
		return nil, nil, nil, fmt.Errorf("session repository mismatch")
	}
	if sb, cleanup, ok, liveErr := r.prepareLiveSessionPreviewCachePrewarmSandbox(ctx, payload, &session); liveErr != nil {
		return nil, nil, nil, liveErr
	} else if ok {
		cfg, err := r.readWorkspacePreviewConfig(ctx, sb, payload.SessionID, payload.PreviewConfigName)
		if err != nil {
			cleanup()
			return nil, nil, nil, err
		}
		return sb, cfg, cleanup, nil
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		r.logger.Debug().Str("session_id", payload.SessionID.String()).Msg("preview cache prewarm skipped because session snapshot is unavailable")
		return nil, nil, nil, nil
	}
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = r.resolveSandboxWorkDir(ctx, &session)
	sandboxCfg.SessionID = payload.SessionID.String()
	sandboxCfg.OrgID = payload.OrgID.String()
	sandboxCfg.Purpose = "preview_cache_prewarm"
	if err := agent.ApplyOrgSandboxNetworkSettings(ctx, r.orgs, payload.OrgID, r.staticEgress, &sandboxCfg); err != nil {
		return nil, nil, nil, fmt.Errorf("resolve sandbox network: %w", err)
	}
	release, err := r.acquirePrewarmCapacity(ctx, sandboxCfg.Purpose, sandboxCfg.SessionID, sandboxCfg.OrgID)
	if err != nil {
		return nil, nil, nil, err
	}
	sb, err := agent.HydrateSandboxFromSnapshot(ctx, r.sandboxProvider, r.snapshots, *session.SnapshotKey, sandboxCfg)
	if err != nil {
		if release != nil {
			release()
		}
		if errors.Is(err, agent.ErrSnapshotMissing) {
			r.logger.Debug().Str("session_id", payload.SessionID.String()).Msg("preview cache prewarm skipped because session snapshot blob is missing")
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("hydrate sandbox: %w", err)
	}
	cleanup := r.previewCachePrewarmCleanup(sb, release)
	cfg, err := r.readWorkspacePreviewConfig(ctx, sb, payload.SessionID, payload.PreviewConfigName)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	return sb, cfg, cleanup, nil
}

func (r *StartRunner) prepareLiveSessionPreviewCachePrewarmSandbox(ctx context.Context, payload PreviewCachePrewarmJobPayload, session *models.Session) (*agent.Sandbox, func(), bool, error) {
	if r == nil || r.sandboxProvider == nil || session == nil || session.ContainerID == nil || *session.ContainerID == "" {
		return nil, nil, false, nil
	}
	if r.nodeID == "" || session.WorkerNodeID == nil || *session.WorkerNodeID != r.nodeID || session.SandboxState != models.SandboxStateRunning {
		return nil, nil, false, nil
	}
	source := &agent.Sandbox{
		ID:        *session.ContainerID,
		WorkDir:   r.resolveSandboxWorkDir(ctx, session),
		HomeDir:   agent.DefaultSandboxConfig().HomeDir,
		SessionID: session.ID.String(),
		OrgID:     session.OrgID.String(),
		Purpose:   "preview_cache_prewarm_source",
	}
	alive, err := r.sandboxProvider.IsAlive(ctx, source)
	if err != nil {
		return nil, nil, false, fmt.Errorf("inspect live session sandbox: %w", err)
	}
	if !alive {
		return nil, nil, false, nil
	}
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.WorkDir = source.WorkDir
	sandboxCfg.SessionID = payload.SessionID.String()
	sandboxCfg.OrgID = payload.OrgID.String()
	sandboxCfg.Purpose = "preview_cache_prewarm"
	if err := agent.ApplyOrgSandboxNetworkSettings(ctx, r.orgs, payload.OrgID, r.staticEgress, &sandboxCfg); err != nil {
		return nil, nil, false, fmt.Errorf("resolve sandbox network: %w", err)
	}
	release, err := r.acquirePrewarmCapacity(ctx, sandboxCfg.Purpose, sandboxCfg.SessionID, sandboxCfg.OrgID)
	if err != nil {
		return nil, nil, false, err
	}
	sb, err := r.sandboxProvider.Create(ctx, sandboxCfg)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, false, fmt.Errorf("create sandbox: %w", err)
	}
	cleanup := r.previewCachePrewarmCleanup(sb, release)
	reader, err := r.sandboxProvider.Snapshot(ctx, source)
	if err != nil {
		cleanup()
		return nil, nil, false, fmt.Errorf("snapshot live session sandbox: %w", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			r.logger.Warn().Err(closeErr).Str("session_id", session.ID.String()).Msg("failed to close live session prewarm snapshot")
		}
	}()
	if err := r.sandboxProvider.Restore(ctx, sb, reader); err != nil {
		cleanup()
		return nil, nil, false, fmt.Errorf("restore live session sandbox: %w", err)
	}
	return sb, cleanup, true, nil
}

func (r *StartRunner) acquirePrewarmCapacity(ctx context.Context, purpose, sessionID, orgID string) (func(), error) {
	if r.sandboxCapacity == nil {
		return nil, nil
	}
	reservation, err := r.sandboxCapacity.Acquire(ctx, agent.SandboxCapacityRequest{
		Purpose: purpose, SessionID: sessionID, OrgID: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPreviewCachePrewarmCapacitySkipped, err)
	}
	return reservation.Release, nil
}

func (r *StartRunner) previewCachePrewarmCleanup(sb *agent.Sandbox, release func()) func() {
	return func() {
		defer func() {
			if release != nil {
				release()
			}
		}()
		if r == nil || r.sandboxProvider == nil || sb == nil {
			return
		}
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.sandboxProvider.Destroy(destroyCtx, sb); err != nil {
			r.logger.Warn().Err(err).Str("sandbox_id", sb.ID).Msg("failed to destroy preview cache prewarm sandbox")
		}
	}
}

func (r *StartRunner) previewCachesAlreadyWarm(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, opts StartPreviewOptions, dependencyEnabled bool, dependencyPaths []string, packageManagerEnabled bool, packageManagerPaths, packageManagers []string) (bool, error) {
	if r.dependencyCache == nil || cfg == nil || cfg.Install == nil {
		return false, nil
	}
	dependencyWarm := !dependencyEnabled || len(dependencyPaths) == 0
	if !dependencyWarm {
		cacheKey, _, err := ComputePreviewDependencyCacheKey(ctx, r.sandboxProvider, sb, cfg.Install, dependencyPaths)
		if err != nil {
			return false, err
		}
		hit, err := r.dependencyCache.FindPathCache(ctx, opts.OrgID, opts.RepositoryID, models.PreviewCacheKindInstallArtifact, cacheKey)
		if err != nil {
			return false, err
		}
		dependencyWarm = hit != nil
	}
	packageManagerWarm := !packageManagerEnabled || len(packageManagerPaths) == 0
	if !packageManagerWarm {
		cacheKey, _, err := ComputePreviewPackageManagerCacheKey(ctx, r.sandboxProvider, sb, cfg.Install, packageManagerPaths, packageManagers)
		if err != nil {
			return false, err
		}
		hit, err := r.dependencyCache.FindPathCache(ctx, opts.OrgID, opts.RepositoryID, models.PreviewCacheKindPackageManager, cacheKey)
		if err != nil {
			return false, err
		}
		packageManagerWarm = hit != nil
	}
	return dependencyWarm && packageManagerWarm, nil
}

func previewInstallPrewarmEnabled(install *models.PreviewInstallConfig) bool {
	if install == nil || install.Cache == nil {
		return true
	}
	if install.Cache.Enabled != nil && !*install.Cache.Enabled {
		return false
	}
	if install.Cache.Prewarm == nil || install.Cache.Prewarm.Enabled == nil {
		return true
	}
	return *install.Cache.Prewarm.Enabled
}

func (r *StartRunner) previewCachePrewarmKeys(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, dependencyEnabled bool, dependencyPaths []string, packageManagerEnabled bool, packageManagerPaths, packageManagers []string) (string, string) {
	if cfg == nil || cfg.Install == nil {
		return "", ""
	}
	var dependencyKey string
	if dependencyEnabled && len(dependencyPaths) > 0 {
		if key, _, err := ComputePreviewDependencyCacheKey(ctx, r.sandboxProvider, sb, cfg.Install, dependencyPaths); err == nil {
			dependencyKey = key
		}
	}
	var packageManagerKey string
	if packageManagerEnabled && len(packageManagerPaths) > 0 {
		if key, _, err := ComputePreviewPackageManagerCacheKey(ctx, r.sandboxProvider, sb, cfg.Install, packageManagerPaths, packageManagers); err == nil {
			packageManagerKey = key
		}
	}
	return dependencyKey, packageManagerKey
}

func (r *StartRunner) createPreviewCachePrewarmRun(ctx context.Context, payload PreviewCachePrewarmJobPayload, scopeKey string) {
	if r == nil || r.previews == nil {
		return
	}
	run := &models.PreviewCachePrewarmRun{
		OrgID:             payload.OrgID,
		RepoID:            payload.RepositoryID,
		Source:            string(payload.Source),
		SourceID:          previewCachePrewarmSourceID(payload),
		CacheScopeKey:     scopeKey,
		WorkerNodeID:      r.nodeID,
		Status:            "pending",
		JobID:             nonNilJobID(payload.JobID),
		ConfigDigest:      payload.ConfigDigest,
		CommitSHA:         payload.CommitSHA,
		WorkspaceRevision: payload.WorkspaceRevision,
	}
	if _, err := r.previews.UpsertPreviewCachePrewarmRun(ctx, run); err != nil {
		r.logger.Warn().Err(err).Str("cache_scope_key", scopeKey).Msg("failed to upsert preview cache prewarm run")
	}
}

func (r *StartRunner) updatePreviewCachePrewarmRun(ctx context.Context, payload PreviewCachePrewarmJobPayload, scopeKey, status, packageManagerCacheKey, dependencyCacheKey, configDigest, errMsg string, completed bool) {
	if r == nil || r.previews == nil {
		return
	}
	if err := r.previews.UpdatePreviewCachePrewarmRunStatus(ctx, payload.OrgID, payload.RepositoryID, scopeKey, status, packageManagerCacheKey, dependencyCacheKey, configDigest, errMsg, completed); err != nil {
		r.logger.Warn().Err(err).Str("cache_scope_key", scopeKey).Str("status", status).Msg("failed to update preview cache prewarm run")
	}
}

func nonNilJobID(jobID uuid.UUID) *uuid.UUID {
	if jobID == uuid.Nil {
		return nil
	}
	return &jobID
}

func PreviewCachePrewarmScopeKey(payload PreviewCachePrewarmJobPayload) string {
	return previewCachePrewarmScopeKey(payload)
}

func PreviewCachePrewarmSourceID(payload PreviewCachePrewarmJobPayload) string {
	return previewCachePrewarmSourceID(payload)
}

func previewCachePrewarmScopeKey(payload PreviewCachePrewarmJobPayload) string {
	switch payload.Source {
	case PreviewCachePrewarmSourceSession:
		if payload.SessionID == uuid.Nil {
			return ""
		}
		if payload.ConfigDigest != "" {
			return fmt.Sprintf("preview_cache_prewarm:session:%s:%d:%s", payload.SessionID, payload.WorkspaceRevision, payload.ConfigDigest)
		}
		return fmt.Sprintf("preview_cache_prewarm:session:%s:%d", payload.SessionID, payload.WorkspaceRevision)
	case PreviewCachePrewarmSourceBranch:
		if payload.PreviewTargetID == uuid.Nil || payload.CommitSHA == "" {
			return ""
		}
		return fmt.Sprintf("preview_cache_prewarm:branch:%s:%s:%s", payload.PreviewTargetID, payload.CommitSHA, payload.PreviewConfigName)
	default:
		return ""
	}
}

func previewCachePrewarmSourceID(payload PreviewCachePrewarmJobPayload) string {
	switch payload.Source {
	case PreviewCachePrewarmSourceSession:
		return payload.SessionID.String()
	case PreviewCachePrewarmSourceBranch:
		return payload.PreviewTargetID.String()
	default:
		return ""
	}
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

	acq := r.acquireSandbox(ctx, payload.OrgID, &session, reservation)
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

func (r *StartRunner) acquireSandbox(ctx context.Context, orgID uuid.UUID, session *models.Session, reservation *models.PreviewInstance) acquireSandboxResult {
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
	ApplyPreviewInstanceResourceLimitsToSandboxConfig(&sandboxCfg, reservation)
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

func (r *StartRunner) maybeRestoreBranchPreviewStartupCache(ctx context.Context, orgID, repoID uuid.UUID, commitSHA string, sb *agent.Sandbox, cfg *models.PreviewConfig) (branchPreviewStartupCacheKeys, error) {
	if r == nil || r.snapshotCache == nil || r.sandboxProvider == nil || sb == nil || cfg == nil || commitSHA == "" {
		return branchPreviewStartupCacheKeys{}, nil
	}
	if previewConfigHasRuntimeSecretFiles(cfg) {
		// Runtime secret files are written into the shared workspace during
		// LaunchPreview. The startup cache snapshots that workspace after
		// launch, so do not read or write cache entries for these configs:
		// otherwise worker-local cache blobs could retain plaintext secrets.
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Msg("branch preview startup cache skipped because config delivers preview secrets as files")
		return branchPreviewStartupCacheKeys{}, nil
	}
	keys, err := r.computeBranchPreviewStartupCacheKeys(ctx, sb, cfg, commitSHA)
	if err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("commit_sha", commitSHA).
			Msg("branch preview startup cache key unavailable; launching cold")
		return branchPreviewStartupCacheKeys{}, nil
	}
	hit, err := r.snapshotCache.FindSnapshot(ctx, orgID, repoID, keys.SnapshotKey)
	if err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", keys.SnapshotKey).
			Msg("branch preview startup cache lookup failed; launching cold")
		return keys, nil
	}
	if hit == nil {
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Str("snapshot_key", keys.SnapshotKey).
			Msg("branch preview startup cache exact miss; trying base snapshot")
		return keys, r.maybeRestoreBaseSnapshot(ctx, orgID, repoID, keys, sb)
	}
	if err := r.snapshotCache.RestoreSnapshot(ctx, sb, hit); err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", keys.SnapshotKey).
			Msg("branch preview startup cache restore failed; launching cold")
		return keys, nil
	}
	r.logger.Info().
		Str("repository_id", repoID.String()).
		Str("snapshot_key", keys.SnapshotKey).
		Msg("branch preview startup cache restored")
	return keys, nil
}

// maxPartialInvalidationDiffBytes caps the git diff streamed out of the
// sandbox for partial snapshot invalidation. Past this size, patching a base
// snapshot stops being meaningfully cheaper than a cold build.
const maxPartialInvalidationDiffBytes int64 = 32 * 1024 * 1024

// maybeRestoreBaseSnapshot handles an exact-key miss by looking for a
// snapshot with the same base key (lockfiles + config digest) at an older
// commit, restoring it, and applying the git diff up to the current commit.
// The returned error is non-nil only when the workspace was mutated and could
// not be restored to a consistent state — the caller must fail the start.
func (r *StartRunner) maybeRestoreBaseSnapshot(ctx context.Context, orgID, repoID uuid.UUID, keys branchPreviewStartupCacheKeys, sb *agent.Sandbox) error {
	baseHit, err := r.snapshotCache.FindBaseSnapshot(ctx, orgID, repoID, keys.BaseKey, keys.CommitSHA)
	if err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("base_key", keys.BaseKey).
			Msg("branch preview base snapshot lookup failed; launching cold")
		return nil
	}
	if baseHit == nil {
		return nil
	}
	// Compute the diff before ApplyPartialInvalidation touches the workspace:
	// a diff failure (e.g. the base commit was force-pushed away) then falls
	// back to a cold start with the freshly checked-out tree intact.
	diff, err := r.gitDiffInSandbox(ctx, sb, baseHit.Entry.CommitSHA, keys.CommitSHA)
	if err != nil {
		r.logger.Info().Err(err).
			Str("repository_id", repoID.String()).
			Str("base_commit", baseHit.Entry.CommitSHA).
			Str("commit_sha", keys.CommitSHA).
			Msg("branch preview base snapshot diff unavailable; launching cold")
		return nil
	}
	if err := r.snapshotCache.ApplyPartialInvalidation(ctx, sb, baseHit, diff); err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("base_commit", baseHit.Entry.CommitSHA).
			Msg("branch preview partial invalidation failed; re-checking out workspace")
		if recoverErr := r.recoverWorkspaceFromGit(ctx, sb); recoverErr != nil {
			return fmt.Errorf("recover workspace after failed partial restore: %w", recoverErr)
		}
		return nil
	}
	r.logger.Info().
		Str("repository_id", repoID.String()).
		Str("base_commit", baseHit.Entry.CommitSHA).
		Str("commit_sha", keys.CommitSHA).
		Int("diff_bytes", len(diff)).
		Msg("branch preview startup cache restored from base snapshot")
	return nil
}

// gitDiffInSandbox produces `git diff --binary old new` from the sandbox's
// clone. Both commits must exist locally; CloneRepo performs a full clone, so
// only history rewrites (force pushes) lose the base commit.
func (r *StartRunner) gitDiffInSandbox(ctx context.Context, sb *agent.Sandbox, oldCommit, newCommit string) ([]byte, error) {
	if !gitCommitSHARe.MatchString(oldCommit) || !gitCommitSHARe.MatchString(newCommit) {
		return nil, fmt.Errorf("invalid commit sha for diff")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	counter := &cappedCountingWriter{limit: maxPartialInvalidationDiffBytes}
	cmd := fmt.Sprintf("cd %s && git diff --binary %s %s", shellQuote(sb.WorkDir), oldCommit, newCommit)
	exitCode, err := r.sandboxProvider.Exec(ctx, sb, cmd, io.MultiWriter(&stdout, counter), &stderr)
	if counter.exceeded {
		return nil, fmt.Errorf("diff exceeds %d bytes; cold build is cheaper", maxPartialInvalidationDiffBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("exec git diff: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("git diff exited %d: %s", exitCode, stderr.String())
	}
	return stdout.Bytes(), nil
}

// recoverWorkspaceFromGit rebuilds the working tree from the sandbox's git
// clone after a failed partial restore left stale files behind. HEAD is the
// pinned preview commit (checked out detached earlier in the start flow).
func (r *StartRunner) recoverWorkspaceFromGit(ctx context.Context, sb *agent.Sandbox) error {
	cmd := fmt.Sprintf(
		"find %s -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} + && cd %s && git checkout -f HEAD -- .",
		shellQuote(sb.WorkDir), shellQuote(sb.WorkDir),
	)
	var stderr bytes.Buffer
	exitCode, err := r.sandboxProvider.Exec(ctx, sb, cmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("exec workspace recovery: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("workspace recovery exited %d: %s", exitCode, stderr.String())
	}
	return nil
}

func (r *StartRunner) createBranchPreviewStartupCache(ctx context.Context, orgID, repoID uuid.UUID, keys branchPreviewStartupCacheKeys, sb *agent.Sandbox, cfg *models.PreviewConfig) bool {
	if r == nil || r.snapshotCache == nil || sb == nil || keys.SnapshotKey == "" {
		return false
	}
	if previewConfigHasRuntimeSecretFiles(cfg) {
		// See maybeRestoreBranchPreviewStartupCache for why secret-file
		// configs are excluded from the workspace snapshot cache.
		r.logger.Debug().
			Str("repository_id", repoID.String()).
			Str("snapshot_key", keys.SnapshotKey).
			Msg("branch preview startup cache creation skipped because config delivers preview secrets as files")
		return false
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	metadata := SnapshotMetadata{OrgID: orgID, RepoID: repoID, BaseKey: keys.BaseKey, CommitSHA: keys.CommitSHA}
	if err := r.snapshotCache.CreateSnapshot(cacheCtx, sb, keys.SnapshotKey, metadata); err != nil {
		r.logger.Warn().Err(err).
			Str("repository_id", repoID.String()).
			Str("snapshot_key", keys.SnapshotKey).
			Msg("failed to create branch preview startup cache")
		return false
	}
	return true
}

func (r *StartRunner) computeBranchPreviewStartupCacheKeys(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, commitSHA string) (branchPreviewStartupCacheKeys, error) {
	lockfiles := branchPreviewStartupCacheLockfiles(cfg)
	var lockInput bytes.Buffer
	for _, lockfile := range lockfiles {
		cleanPath, err := cleanBranchPreviewStartupCachePath(lockfile)
		if err != nil {
			return branchPreviewStartupCacheKeys{}, fmt.Errorf("preview.install.lockfiles path %q: %w", lockfile, err)
		}
		body, err := r.sandboxProvider.ReadFile(ctx, sb, cleanPath)
		if err != nil {
			return branchPreviewStartupCacheKeys{}, fmt.Errorf("read preview.install lockfile %q: %w", cleanPath, err)
		}
		lockInput.WriteString(cleanPath)
		lockInput.WriteByte(0)
		lockInput.Write(body)
		lockInput.WriteByte(0)
	}
	configDigest := computeConfigDigest(cfg)
	return branchPreviewStartupCacheKeys{
		SnapshotKey: ComputeSnapshotKey(lockInput.Bytes(), commitSHA, configDigest),
		BaseKey:     ComputeSnapshotBaseKey(lockInput.Bytes(), configDigest),
		CommitSHA:   commitSHA,
	}, nil
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
