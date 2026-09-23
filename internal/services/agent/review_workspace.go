package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// PrepareCodeReviewWorkspace creates an unpublished sandbox for one active
// review. The caller must publish it under the preparation job's lease or
// destroy it. No reviewer can observe this container until publication.
func (o *Orchestrator) PrepareCodeReviewWorkspace(ctx context.Context, session *models.Session, expectedHead string) (_ *Sandbox, returnErr error) {
	if o == nil || o.provider == nil || o.sandboxCapacity == nil || o.env == nil || o.repositories == nil || o.github == nil {
		return nil, fmt.Errorf("code review workspace preparation dependencies unavailable")
	}
	if session == nil || session.Origin != models.SessionOriginCodeReview || session.RepositoryID == nil || session.ContainerID != nil || session.SnapshotKey != nil {
		return nil, fmt.Errorf("code review workspace is not eligible for cold preparation")
	}
	prNumber, head, ok, err := codeReviewCheckoutContextFromSession(session)
	if err != nil {
		return nil, fmt.Errorf("code review workspace revision does not match active review: %w", err)
	}
	if !ok || head != expectedHead {
		return nil, fmt.Errorf("code review workspace revision does not match active review")
	}
	branch := sessionWorkingBranch(session, &models.Issue{})
	if branch == "" {
		return nil, fmt.Errorf("code review session is missing a working branch")
	}
	log := o.logger.With().Str("org_id", session.OrgID.String()).Str("session_id", session.ID.String()).Str("review_head_sha", head).Logger()
	stage := observability.BeginStage(true, log, "review_workspace_prepare")
	defer func() { stage.End(observability.StageOutcome(ctx, returnErr)) }()

	repo, err := o.repositories.GetByID(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("load review repository: %w", err)
	}
	token, err := o.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("get review repository installation token: %w", err)
	}
	cfg := DefaultSandboxConfig()
	cfg.SessionID = session.ID.String()
	cfg.OrgID = session.OrgID.String()
	cfg.Purpose = "prepare_code_review_workspace"
	if jobID, ok := jobctx.JobIDFromContext(ctx); ok {
		cfg.PreparationJobID = jobID.String()
	}
	cfg.Env = o.env.ResolveForModel(ctx, session.OrgID, session.AgentType, session.TriggeredByUserID, stringPtrValue(session.ModelOverride))
	if cfg.Env == nil {
		cfg.Env = make(map[string]string)
	}
	applyDirectModelOverrideEnv(session.AgentType, session.ModelOverride, cfg.Env)
	cfg.Env[sandboxauth.WorkingBranchEnvVar] = branch
	o.injectInternalAPIEnv(ctx, session, session.RepositoryID, nil, &cfg, log)
	if err := ApplyOrgSandboxNetworkSettings(ctx, o.orgs, session.OrgID, o.staticEgress, &cfg); err != nil {
		return nil, fmt.Errorf("apply review sandbox network settings: %w", err)
	}
	slug, err := o.sessionRepoSlug(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("resolve review workspace directory: %w", err)
	}
	if slug != "" {
		cfg.WorkDir = cfg.HomeDir + "/" + slug
	}
	if _, ok := cfg.Env["HOME"]; !ok {
		cfg.Env["HOME"] = cfg.HomeDir
	}
	// The credential helper's directory bind mount must exist when Docker
	// creates the container. Reviewer executors reopen the socket on reuse.
	defer o.closeSandboxAuth(session.ID, log)
	if _, err := o.prepareSandboxGitHubAuth(ctx, session, &repo, token, &cfg, log); err != nil {
		return nil, fmt.Errorf("prepare review sandbox github auth: %w", err)
	}

	capacityStage := observability.BeginStage(true, log, "capacity_admission")
	reservation, err := o.sandboxCapacity.Acquire(ctx, SandboxCapacityRequest{
		Purpose: "prepare_code_review_workspace", SessionID: session.ID.String(), OrgID: session.OrgID.String(),
	})
	capacityStage.End(observability.StageOutcome(ctx, err))
	if err != nil {
		return nil, err
	}
	defer reservation.Release()
	createStage := observability.BeginStage(true, log, "sandbox_create")
	sandbox, err := o.provider.Create(ctx, cfg)
	createStage.End(observability.StageOutcome(ctx, err))
	if err != nil {
		return nil, fmt.Errorf("create review sandbox: %w", err)
	}
	// The live-container counter now sees this sandbox; release the temporary
	// process-local reservation while clone and checkout proceed.
	reservation.Release()
	transferred := false
	defer func() {
		if transferred {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if destroyErr := o.provider.Destroy(cleanupCtx, sandbox); destroyErr != nil {
			log.Warn().Err(destroyErr).Str("sandbox_container_id", sandbox.ID).Msg("failed to destroy unpublished review workspace")
		}
	}()
	sandbox.Env = cloneStringMap(cfg.Env)
	cloneBranch := repo.DefaultBranch
	if session.TargetBranch != nil && *session.TargetBranch != "" {
		cloneBranch = *session.TargetBranch
	}
	cloneStage := observability.BeginStage(true, log, "repository_clone")
	cloneErr := o.provider.CloneRepo(ctx, sandbox, repo.CloneURL, cloneBranch, token)
	cloneStage.End(observability.StageOutcome(ctx, cloneErr))
	if cloneErr != nil {
		return nil, fmt.Errorf("clone review repository: %w", cloneErr)
	}
	gitAuthStage := observability.BeginStage(true, log, "git_auth_bootstrap")
	o.runSandboxGitBootstrap(ctx, sandbox, cfg.WorkDir, log)
	gitAuthStage.End("attempted")
	checkoutStage := observability.BeginStage(true, log, "review_head_checkout")
	checkoutErr := o.checkoutExpectedPullRequestHead(ctx, sandbox, repo.CloneURL, token, prNumber, branch, head)
	checkoutStage.End(observability.StageOutcome(ctx, checkoutErr))
	if checkoutErr != nil {
		return nil, checkoutErr
	}
	if err := prepareSandboxRepositoryForSession(ctx, o.provider, session, sandbox, cfg.WorkDir, repositoryPreparationMinimalReview, log); err != nil {
		return nil, fmt.Errorf("prepare review repository: %w", err)
	}
	// Ownership of a successful sandbox transfers to the caller. Its durable
	// publication transaction supplies the review holder and worker identity.
	transferred = true
	return sandbox, nil
}
