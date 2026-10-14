package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type prepareCodeReviewWorkspacePayload struct {
	OrgID              uuid.UUID `json:"org_id"`
	MetadataID         uuid.UUID `json:"metadata_id"`
	SessionID          uuid.UUID `json:"session_id"`
	ExpectedGeneration int64     `json:"expected_generation"`
}

const codeReviewWorkspaceWait = 3 * time.Second
const codeReviewWorkspaceWaitLimit = agent.CodeReviewWorkspacePreparationTimeout

var errCodeReviewEndedParentRecovery = errors.New("ended review recovery could not prove exclusive ownership")

var errCodeReviewWorkspaceStopped = errors.New("code review stopped before workspace readiness")

type codeReviewWorkspaceGateFailure struct {
	Generation           int64
	PreparationStartedAt time.Time
	Err                  error
}

func (e *codeReviewWorkspaceGateFailure) Error() string { return e.Err.Error() }
func (e *codeReviewWorkspaceGateFailure) Unwrap() error { return e.Err }

func newCodeReviewWorkspaceGateFailure(generation int64, err error) error {
	return &codeReviewWorkspaceGateFailure{Generation: generation, Err: err}
}

// Only recognized finite messages can cross from persisted diagnostics into
// author-facing text. Raw command output and unknown errors never do.
func safeCodeReviewWorkspacePreparationError(detail string) error {
	if known, ok := knownCodeReviewWorkspacePreparationError(detail); ok {
		return known
	}
	return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, errors.New("preparation stopped"))
}

func knownCodeReviewWorkspacePreparationError(detail string) (error, bool) {
	detail = strings.TrimSpace(detail)
	timeoutPrefix := fmt.Sprintf("retryable job timed out after %s: ", codeReviewWorkspaceWaitLimit)
	timedOut := strings.HasPrefix(detail, timeoutPrefix)
	if timedOut {
		detail = strings.TrimPrefix(detail, timeoutPrefix)
	}
	cleanup := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, agent.ErrCodeReviewPreparationCleanup)
	if detail == cleanup.Error() {
		if timedOut {
			return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, errors.Join(agent.ErrCodeReviewPreparationCleanup, context.DeadlineExceeded)), true
		}
		return cleanup, true
	}
	integrity := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, agent.ErrCodeReviewSourceIntegrity)
	if detail == integrity.Error() {
		if timedOut {
			return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, context.DeadlineExceeded), true
		}
		return integrity, true
	}
	stages := []string{agent.CodeReviewPreparationStageClone, agent.CodeReviewPreparationStageHydration,
		agent.CodeReviewPreparationStageCheckout, agent.CodeReviewPreparationStageDependencySetup, agent.CodeReviewPreparationStageReadiness}
	causes := []error{context.DeadlineExceeded, context.Canceled, errors.New("preparation failed"), agent.ErrStalePullRequestHead}
	for _, stage := range stages {
		for _, cause := range causes {
			candidate := agent.NewCodeReviewWorkspacePreparationError(stage, cause)
			if detail == candidate.Error() {
				if timedOut {
					// The worker owns this exact prefix. Only its finite safe suffix
					// identifies a stage; the exhausted duration owns the outcome.
					if errors.Is(candidate, agent.ErrStalePullRequestHead) {
						return nil, false
					}
					candidate = agent.NewCodeReviewWorkspacePreparationError(stage, context.DeadlineExceeded)
				}
				return candidate, true
			}
		}
	}
	return nil, false
}

// A stage alone is insufficient retry evidence: malformed revisions, missing
// tools and deterministic command failures can occur in source stages too.
func codeReviewWorkspaceSourceFailureRetryable(err error) bool {
	var preparation *agent.CodeReviewWorkspacePreparationError
	if !errors.As(err, &preparation) {
		return false
	}
	switch preparation.Stage {
	case agent.CodeReviewPreparationStageClone, agent.CodeReviewPreparationStageHydration, agent.CodeReviewPreparationStageCheckout:
	default:
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agent.ErrStalePullRequestHead) || errors.Is(err, agent.ErrCodeReviewSourceIntegrity) || errors.Is(err, agent.ErrCodeReviewPreparationCleanup) {
		return false
	}
	if ghservice.ClassifyRetry(err, time.Now()).Retryable || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Git subprocesses report transport errors as stderr, rather than typed
	// network errors. Recognize only transport diagnostics, never whole stages.
	detail := strings.ToLower(preparation.Unwrap().Error())
	for _, transient := range []string{"could not resolve host:", "failed to connect to ", "connection reset by peer", "connection timed out", "operation timed out after ", "curl 28 ", "the requested url returned error: 500", "the requested url returned error: 502", "the requested url returned error: 503", "the requested url returned error: 504", "remote end hung up unexpectedly", "early eof", "tls connection was non-properly terminated"} {
		if strings.Contains(detail, transient) {
			return true
		}
	}
	return false
}

func classifyCodeReviewWorkspacePreparationFailure(ctx context.Context, err error, startedAt time.Time) error {
	preparationErr := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, err)
	var typed *agent.CodeReviewWorkspacePreparationError
	errors.As(preparationErr, &typed)
	if errors.Is(err, agent.ErrCodeReviewPreparationCleanup) {
		// Never reuse a workspace or retry preparation when its previous command
		// may still be running. Preserve this cause when the caller or durable
		// preparation window also owns a cancellation/deadline.
		cause := typed.Unwrap()
		if !time.Now().Before(startedAt.Add(codeReviewWorkspaceWaitLimit)) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cause = errors.Join(cause, context.DeadlineExceeded)
		} else if ctx.Err() != nil {
			cause = errors.Join(cause, ctx.Err())
		}
		return &FatalError{Err: agent.NewCodeReviewWorkspacePreparationError(typed.Stage, cause)}
	}
	// A moved ref is revision evidence, rather than an operational failure,
	// even when the final fetch used the last of the preparation budget.
	if errors.Is(typed, agent.ErrStalePullRequestHead) {
		return &FatalError{Err: typed}
	}
	deadline := startedAt.Add(codeReviewWorkspaceWaitLimit)
	if !time.Now().Before(deadline) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &FatalError{Err: agent.NewCodeReviewWorkspacePreparationError(typed.Stage, context.DeadlineExceeded)}
	}
	if ctx.Err() != nil {
		return &FatalError{Err: agent.NewCodeReviewWorkspacePreparationError(typed.Stage, ctx.Err())}
	}
	if codeReviewWorkspaceSourceFailureRetryable(typed) {
		return &RetryableError{Err: typed, RetryAfter: durationPtr(codeReviewWorkspaceWait), MaxRetryDuration: durationPtr(codeReviewWorkspaceWaitLimit), RetryWindowStartedAt: &startedAt}
	}
	return &FatalError{Err: typed}
}

func codeReviewWorkspacePreparationFailureMessage(err error) string {
	err = agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, err)
	var preparation *agent.CodeReviewWorkspacePreparationError
	errors.As(err, &preparation)
	if errors.Is(err, agent.ErrCodeReviewPreparationCleanup) {
		return preparation.Error() + " This is an operational review failure."
	}
	if errors.Is(preparation, agent.ErrCodeReviewSourceIntegrity) && !errors.Is(preparation, context.Canceled) && !errors.Is(preparation, context.DeadlineExceeded) {
		return preparation.Error() + " This is an operational review failure."
	}
	guidance := "Check workspace capacity and sandbox availability, then request a new review."
	switch preparation.Stage {
	case agent.CodeReviewPreparationStageClone:
		guidance = "Check repository access and sandbox network connectivity, then request a new review."
	case agent.CodeReviewPreparationStageHydration:
		guidance = "Restore access to the pinned base and head Git objects, then request a new review."
	case agent.CodeReviewPreparationStageCheckout:
		guidance = "Check that the pinned pull request commit remains fetchable, then request a new review."
	case agent.CodeReviewPreparationStageDependencySetup:
		guidance = "Check the declared dependency setup and bootstrap commands, then request a new review."
	}
	return preparation.Error() + " This is an operational review failure. " + guidance
}

// Only the initial reviewer turn inherits the cold preparation checkpoint.
// Synthesis, evidence rechecks and later turns have their own durable job budget.
func initialCodeReviewPreparationDeadline(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session, thread *models.SessionThread, evidenceRecheck bool) (*time.Time, error) {
	if session.Origin != models.SessionOriginCodeReview || thread == nil || evidenceRecheck || thread.CurrentTurn != 0 ||
		thread.ExecutionMode != models.ThreadExecutionModeReview || thread.FilesystemMode != models.ThreadFilesystemModeReadOnly {
		return nil, nil
	}
	if stores == nil || stores.CodeReviews == nil || stores.Jobs == nil {
		return nil, fmt.Errorf("initial code review preparation deadline stores unavailable")
	}
	role, known, roleErr := stores.CodeReviews.ResolveAgentRoleForThread(ctx, session.OrgID, session.ID, thread.ID)
	if roleErr == nil && known && role == models.CodeReviewAgentRoleOrchestrator {
		return nil, nil
	}
	metadata, err := stores.CodeReviews.GetBySessionID(ctx, session.OrgID, session.ID)
	if err != nil {
		return nil, fmt.Errorf("load initial reviewer preparation metadata: %w", err)
	}
	startedAt, err := stores.Jobs.FirstCodeReviewWorkspacePreparationCreatedAt(ctx, session.OrgID, metadata.ID, session.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load initial reviewer preparation checkpoint: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var durable bool
		startedAt, durable = jobctx.JobCreatedAtFromContext(ctx)
		if !durable {
			return nil, &RetryableError{Err: errors.New("initial reviewer preparation has no durable job checkpoint"), RetryAfter: durationPtr(codeReviewWorkspaceWait)}
		}
	}
	if roleErr != nil {
		logger.Warn().Err(roleErr).Str("thread_id", thread.ID.String()).Msg("using conservative initial preparation deadline after role lookup failure")
	}
	deadline := startedAt.Add(agent.CodeReviewWorkspacePreparationTimeout)
	return &deadline, nil
}

func codeReviewWorkspaceWaitError(reason string) *RetryableError {
	delay, limit := codeReviewWorkspaceWait, codeReviewWorkspaceWaitLimit
	return &RetryableError{Err: fmt.Errorf("waiting for code review workspace: %s", reason), RetryAfter: &delay, MaxRetryDuration: &limit}
}

func codeReviewControllerWorkspaceWaitError(reason string, startedAt time.Time) error {
	retry := codeReviewWorkspaceWaitError(reason)
	retry.RetryWindowStartedAt = &startedAt
	// The readiness gate owns the absolute deadline and projects a terminal
	// operational failure. Let it perform the final check under its review fence.
	retry.BypassMaxRetryDuration = true
	return retry
}

func codeReviewWorkspacePreparationKey(reviewID, sessionID uuid.UUID, generation int64) string {
	return fmt.Sprintf("code_review_prepare:%s:%s:%d", reviewID, sessionID, generation)
}

func codeReviewWorkspaceColdEligible(session models.Session, repositoryID uuid.UUID) bool {
	return session.Origin == models.SessionOriginCodeReview &&
		session.RepositoryID != nil && *session.RepositoryID == repositoryID &&
		session.SnapshotKey == nil
}

// A prior enqueue is the durable checkpoint that preflight and deterministic
// early-stop have already run. While preparation is pending, the controller
// checks readiness before repeating expensive GitHub calls. It refreshes
// GitHub once more after readiness, immediately before reviewer fan-out.
func codeReviewWorkspacePreparationStarted(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload) (bool, error) {
	if services == nil || !services.CodeReviewWorkspacePreparationEnabled {
		return false, nil
	}
	if stores == nil || stores.Sessions == nil || stores.Jobs == nil {
		return false, fmt.Errorf("code review workspace preparation is enabled without required stores")
	}
	if _, err := stores.Sessions.WorkspaceGenerationForReview(ctx, job.OrgID, job.SessionID); err != nil {
		return false, fmt.Errorf("validate review session before preparation checkpoint: %w", err)
	}
	_, err := stores.Jobs.FirstCodeReviewWorkspacePreparationCreatedAt(ctx, job.OrgID, job.MetadataID, job.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load review workspace preparation checkpoint: %w", err)
	}
	return true, nil
}

func ensureCodeReviewWorkspaceReady(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload) error {
	return ensureCodeReviewWorkspaceReadyWithRecovery(ctx, stores, services, log, job, false)
}

// Once the first gate has observed a prepared workspace, the controller runs
// the live GitHub preflight. If the holder expires during that work, retrying
// preparation would repeat the whole preflight; reviewer launch can instead
// use the existing M2 container recovery path.
func ensureCodeReviewWorkspaceReadyAfterPreflight(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload) error {
	return ensureCodeReviewWorkspaceReadyWithRecovery(ctx, stores, services, log, job, true)
}

// A completed initializer is not proof of readiness. Both gates check the
// current holder, review revision, and session before dispatching reviewers.
func ensureCodeReviewWorkspaceReadyWithRecovery(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload, afterPreflight bool) error {
	if services == nil || !services.CodeReviewWorkspacePreparationEnabled {
		return nil
	}
	if stores == nil || stores.CodeReviewWorkspaces == nil || stores.CodeReviews == nil || stores.Sessions == nil || stores.Jobs == nil {
		return fmt.Errorf("code review workspace preparation is enabled without required stores")
	}
	ready, err := stores.CodeReviewWorkspaces.Readiness(ctx, job.OrgID, job.MetadataID, job.SessionID, job.HeadSHA)
	if err != nil {
		return err
	}
	if ready.Ready {
		return nil
	}
	metadata, err := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("load review before workspace enqueue: %w", err)
	}
	if metadata.ID != job.MetadataID || metadata.HeadSHA != job.HeadSHA || metadata.Stale || codeReviewMetadataTerminal(metadata.Status) {
		return errCodeReviewWorkspaceStopped
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("load review session before workspace enqueue: %w", err)
	}
	if session.Status == models.SessionStatusCancelled || session.Status == models.SessionStatusFailed || session.Status == models.SessionStatusCompleted {
		return errCodeReviewWorkspaceStopped
	}
	key := codeReviewWorkspacePreparationKey(metadata.ID, session.ID, session.WorkspaceGeneration)
	startedAt, startErr := stores.Jobs.FirstCodeReviewWorkspacePreparationCreatedAt(ctx, job.OrgID, metadata.ID, session.ID)
	if startErr != nil && !errors.Is(startErr, pgx.ErrNoRows) {
		return fmt.Errorf("load review workspace wait start: %w", startErr)
	}
	latestStatus, failureDetail, statusErr := stores.Jobs.LatestCodeReviewWorkspacePreparationOutcome(ctx, job.OrgID, "agent", key)
	if statusErr != nil && !errors.Is(statusErr, pgx.ErrNoRows) {
		return fmt.Errorf("load review workspace preparation status: %w", statusErr)
	}
	terminalPreparation := latestStatus == models.JobStatusFailed || latestStatus == models.JobStatusDeadLetter || latestStatus == models.JobStatusCancelled
	var preparationFailure error
	if terminalPreparation {
		preparationFailure = safeCodeReviewWorkspacePreparationError(failureDetail)
		if errors.Is(preparationFailure, agent.ErrStalePullRequestHead) {
			// Fetch proved the review revision moved. Reach live synchronization
			// before any timeout/failure projection or initializer re-enqueue.
			return &codeReviewWorkspaceGateFailure{Generation: session.WorkspaceGeneration, PreparationStartedAt: startedAt, Err: preparationFailure}
		}
	}
	initialBudgetExpired := startErr == nil && !time.Now().Before(startedAt.Add(codeReviewWorkspaceWaitLimit))
	if terminalPreparation {
		if initialBudgetExpired {
			var typed *agent.CodeReviewWorkspacePreparationError
			errors.As(preparationFailure, &typed)
			cause := error(context.DeadlineExceeded)
			if errors.Is(preparationFailure, agent.ErrCodeReviewPreparationCleanup) {
				cause = errors.Join(typed.Unwrap(), cause)
			}
			preparationFailure = agent.NewCodeReviewWorkspacePreparationError(typed.Stage, cause)
		}
		return newCodeReviewWorkspaceGateFailure(session.WorkspaceGeneration, preparationFailure)
	}
	// Initial preparation has already completed once reviewer execution begins.
	// A later synthesis/continuation turn uses its own durable execution-job
	// deadline rather than inheriting an expired cold-start window.
	if metadata.Phase != nil && (*metadata.Phase == models.CodeReviewPhaseReviewing || *metadata.Phase == models.CodeReviewPhaseSynthesizing) {
		return nil
	}
	if initialBudgetExpired {
		cancelled, cancelErr := stores.Jobs.CancelActiveCodeReviewPreparation(ctx, job.OrgID, "agent", key)
		if cancelErr != nil {
			log.Warn().Err(cancelErr).Str("session_id", session.ID.String()).Msg("could not cancel timed-out review workspace preparation")
		}
		log.Warn().Str("session_id", session.ID.String()).Int64("cancelled_preparation_jobs", cancelled).
			Msg("review workspace preparation exhausted its total budget")
		return newCodeReviewWorkspaceGateFailure(session.WorkspaceGeneration,
			agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, context.DeadlineExceeded))
	}
	if session.ContainerID != nil && session.WorkerNodeID == nil {
		// A legacy in-flight review predates durable worker ownership. The
		// existing continuation path knows how to handle it; this gate must
		// not guess which Docker daemon owns the old container.
		log.Warn().Str("session_id", session.ID.String()).Msg("using legacy review workspace path without a recorded owner")
		return nil
	}
	if !codeReviewWorkspaceColdEligible(session, metadata.RepositoryID) {
		// A checkpoint, legacy session, or repository mismatch belongs to the
		// ordinary per-reviewer recovery path. The preparation handler makes
		// the same decision so this gate cannot wait for an impossible job.
		log.Warn().Str("session_id", session.ID.String()).Msg("using ordinary reviewer workspace path for ineligible preparation session")
		return nil
	}
	if afterPreflight {
		if latestStatus == models.JobStatusPending || latestStatus == models.JobStatusRunning {
			return codeReviewControllerWorkspaceWaitError("preparation pending", startedAt)
		}
		log.Warn().Str("session_id", session.ID.String()).
			Msg("prepared workspace lost readiness during preflight; recovering within the preparation budget")
		return nil
	}
	payload := prepareCodeReviewWorkspacePayload{
		OrgID: job.OrgID, MetadataID: metadata.ID, SessionID: session.ID,
		ExpectedGeneration: session.WorkspaceGeneration,
	}
	switch latestStatus {
	case models.JobStatusPending, models.JobStatusRunning:
		// The unique dedupe owner is already preparing this generation.
	default:
		target := session.WorkerNodeID
		if session.ContainerID == nil || target == nil {
			target, err = stores.Jobs.SelectWorkerWithSandboxCapacity(ctx, "")
			if err != nil {
				return fmt.Errorf("choose review workspace worker: %w", err)
			}
		}
		_, err = stores.Jobs.EnqueueWithOpts(ctx, job.OrgID, db.EnqueueOpts{
			Queue: "agent", JobType: models.JobTypePrepareCodeReviewWorkspace,
			Payload: payload, Priority: 6, DedupeKey: &key, MaxAttempts: 8,
			TargetNodeID: target,
		})
		if err != nil {
			return fmt.Errorf("enqueue code review workspace preparation: %w", err)
		}
	}
	if errors.Is(startErr, pgx.ErrNoRows) {
		startedAt, err = stores.Jobs.FirstCodeReviewWorkspacePreparationCreatedAt(ctx, job.OrgID, metadata.ID, session.ID)
		if err != nil {
			return fmt.Errorf("load review workspace wait start after enqueue: %w", err)
		}
	}
	log.Debug().Str("session_id", session.ID.String()).Int64("generation", session.WorkspaceGeneration).Msg("waiting for prepared code review workspace")
	return codeReviewControllerWorkspaceWaitError("preparation pending", startedAt)
}

func newPrepareCodeReviewWorkspaceHandler(stores *Stores, services *Services, log zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, raw json.RawMessage) (returnErr error) {
		if services != nil && !services.CodeReviewWorkspacePreparationEnabled {
			return nil // a fleet rollout disabled preparation before this queued job claimed
		}
		if stores == nil || stores.CodeReviewWorkspaces == nil || stores.CodeReviews == nil || stores.Sessions == nil || stores.Jobs == nil || services == nil || services.CodeReviewWorkspacePreparer == nil || services.SandboxProvider == nil {
			return fmt.Errorf("code review workspace preparation dependencies unavailable")
		}
		var input prepareCodeReviewWorkspacePayload
		if err := json.Unmarshal(raw, &input); err != nil {
			return fmt.Errorf("decode code review workspace job: %w", err)
		}
		if input.OrgID == uuid.Nil || input.MetadataID == uuid.Nil || input.SessionID == uuid.Nil || input.ExpectedGeneration < 0 {
			return fmt.Errorf("invalid code review workspace job identity")
		}
		jobID, hasJob := jobctx.JobIDFromContext(ctx)
		lockToken, hasToken := jobctx.LockTokenFromContext(ctx)
		ownerNodeID, hasOwner := jobctx.WorkerNodeIDFromContext(ctx)
		if !hasJob || !hasToken || !hasOwner || strings.TrimSpace(ownerNodeID) == "" {
			return fmt.Errorf("code review workspace job is missing lease or worker identity")
		}
		log = log.With().
			Str("org_id", input.OrgID.String()).
			Str("session_id", input.SessionID.String()).
			Str("review_id", input.MetadataID.String()).
			Str("job_id", jobID.String()).
			Str("worker_node_id", ownerNodeID).Logger()
		stage := observability.BeginStage(true, log, "review_workspace_prepare_job")
		defer func() { stage.End(observability.StageOutcome(ctx, returnErr)) }()
		metadata, err := stores.CodeReviews.GetBySessionID(ctx, input.OrgID, input.SessionID)
		if err != nil {
			return fmt.Errorf("load review for workspace preparation: %w", err)
		}
		if metadata.ID != input.MetadataID || metadata.Stale || codeReviewMetadataTerminal(metadata.Status) {
			return nil
		}
		policy, err := stores.CodeReviews.GetPolicyByID(ctx, input.OrgID, metadata.PolicyID)
		if err != nil {
			return fmt.Errorf("load captured review policy before workspace preparation: %w", err)
		}
		if policy.ID != metadata.PolicyID || policy.OrgID != input.OrgID {
			return fmt.Errorf("captured review policy changed before workspace preparation")
		}
		session, err := stores.Sessions.GetByID(ctx, input.OrgID, input.SessionID)
		if err != nil {
			return fmt.Errorf("load review session for workspace preparation: %w", err)
		}
		if session.Status == models.SessionStatusCancelled || session.Status == models.SessionStatusFailed || session.Status == models.SessionStatusCompleted {
			return nil
		}
		if session.WorkspaceGeneration != input.ExpectedGeneration || !codeReviewWorkspaceColdEligible(session, metadata.RepositoryID) {
			return nil
		}
		startedAt, startErr := stores.Jobs.FirstCodeReviewWorkspacePreparationCreatedAt(ctx, input.OrgID, input.MetadataID, input.SessionID)
		if startErr != nil && !errors.Is(startErr, pgx.ErrNoRows) {
			return fmt.Errorf("load durable review preparation budget: %w", startErr)
		}
		if errors.Is(startErr, pgx.ErrNoRows) {
			var ok bool
			startedAt, ok = jobctx.JobCreatedAtFromContext(ctx)
			if !ok {
				return &FatalError{Err: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, errors.New("durable preparation checkpoint unavailable"))}
			}
		}
		deadline := startedAt.Add(agent.CodeReviewWorkspacePreparationTimeout)
		defer func() {
			var retry *RetryableError
			if errors.As(returnErr, &retry) {
				retry.RetryWindowStartedAt = &startedAt
				retry.MaxRetryDuration = durationPtr(codeReviewWorkspaceWaitLimit)
				retry.BypassMaxRetryDuration = false
			}
		}()
		if !time.Now().Before(deadline) {
			return &FatalError{Err: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, context.DeadlineExceeded)}
		}
		preparationCtx := agent.WithCodeReviewWorkspacePreparationDeadline(ctx, deadline)
		if session.ContainerID != nil {
			if session.WorkerNodeID == nil {
				return codeReviewWorkspaceWaitError("container has no recorded owner")
			}
			if *session.WorkerNodeID != ownerNodeID {
				healthy, err := stores.Jobs.IsHealthyWorkerNode(ctx, *session.WorkerNodeID)
				if err != nil {
					return fmt.Errorf("check review workspace owner: %w", err)
				}
				if healthy {
					zero := time.Duration(0)
					return &RetryableError{Err: fmt.Errorf("review workspace belongs to %s", *session.WorkerNodeID), RetryAfter: &zero, TargetNodeID: session.WorkerNodeID, BypassMaxRetryDuration: true}
				}
				// Never assume a merely draining worker's container is gone. The
				// store only permits remote cleanup after the node is marked dead.
				cleared, err := stores.CodeReviewWorkspaces.ReconcileMissing(ctx, input.OrgID, input.MetadataID, input.SessionID, *session.ContainerID, *session.WorkerNodeID, ownerNodeID)
				if err != nil {
					return err
				}
				if !cleared {
					return codeReviewWorkspaceWaitError("owner is unavailable but not recoverable")
				}
			} else {
				probeCtx, probeCancel := context.WithDeadline(preparationCtx, deadline)
				alive, err := services.SandboxProvider.IsAlive(probeCtx, &agent.Sandbox{ID: *session.ContainerID, Provider: "docker"})
				probeCancel()
				if err != nil {
					return &RetryableError{Err: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, err), RetryAfter: durationPtr(codeReviewWorkspaceWait), MaxRetryDuration: durationPtr(codeReviewWorkspaceWaitLimit), RetryWindowStartedAt: &startedAt}
				}
				if alive {
					ready, err := stores.CodeReviewWorkspaces.Readiness(ctx, input.OrgID, input.MetadataID, input.SessionID, metadata.HeadSHA)
					if err != nil {
						return err
					}
					if ready.Ready {
						return nil
					}
					p := db.PublishCodeReviewWorkspaceParams{
						OrgID: input.OrgID, ReviewID: input.MetadataID, SessionID: input.SessionID,
						PolicyID: metadata.PolicyID, RepositoryID: metadata.RepositoryID,
						JobID: jobID, JobLockToken: lockToken, OwnerNodeID: ownerNodeID,
						ContainerID: *session.ContainerID, ExpectedHead: metadata.HeadSHA,
						ExpectedGeneration: input.ExpectedGeneration,
					}
					rearmed, err := stores.CodeReviewWorkspaces.RearmExisting(ctx, input.OrgID, p)
					if err != nil {
						return err
					}
					if rearmed {
						return nil
					}
					return codeReviewWorkspaceWaitError("live container has no active review holder")
				}
				cleared, err := stores.CodeReviewWorkspaces.ReconcileMissing(ctx, input.OrgID, input.MetadataID, input.SessionID, *session.ContainerID, ownerNodeID, ownerNodeID)
				if err != nil {
					return err
				}
				if !cleared {
					return codeReviewWorkspaceWaitError("missing container recovery raced another holder")
				}
			}
		}
		// Refresh after any recovery. The publication transaction still checks
		// generation, review state, node heartbeat, and this job's live lease.
		session, err = stores.Sessions.GetByID(ctx, input.OrgID, input.SessionID)
		if err != nil {
			return fmt.Errorf("reload review session before preparing: %w", err)
		}
		if session.ContainerID != nil || session.SnapshotKey != nil || session.WorkspaceGeneration != input.ExpectedGeneration {
			return nil
		}
		sandbox, err := services.CodeReviewWorkspacePreparer.PrepareCodeReviewWorkspace(preparationCtx, &session, metadata.HeadSHA)
		if err != nil {
			if errors.Is(err, agent.ErrStalePullRequestHead) || !time.Now().Before(deadline) || preparationCtx.Err() != nil {
				return classifyCodeReviewWorkspacePreparationFailure(preparationCtx, err, startedAt)
			}
			if errors.Is(err, agent.ErrSandboxCapacityReached) {
				log.Warn().Err(err).Str("current_node_id", ownerNodeID).
					Msg("review workspace preparation rejected by local sandbox capacity")
				alternate, selectErr := stores.Jobs.SelectWorkerWithSandboxCapacity(ctx, ownerNodeID)
				if selectErr != nil {
					log.Warn().Err(selectErr).Msg("could not select alternate review workspace worker after capacity rejection")
				} else if alternate != nil {
					delay := codeReviewWorkspaceWait
					limit := codeReviewWorkspaceWaitLimit
					log.Info().Str("target_node_id", *alternate).Msg("redirecting cold review preparation to available worker")
					return &RetryableError{Err: err, RetryAfter: &delay, TargetNodeID: alternate, MaxRetryDuration: &limit, RetryWindowStartedAt: &startedAt}
				}
				return codeReviewWorkspaceWaitError("worker sandbox capacity reached")
			}
			return classifyCodeReviewWorkspacePreparationFailure(preparationCtx, err, startedAt)
		}
		published, publishErr := stores.CodeReviewWorkspaces.PublishPrepared(ctx, input.OrgID, db.PublishCodeReviewWorkspaceParams{
			OrgID: input.OrgID, ReviewID: input.MetadataID, SessionID: input.SessionID,
			PolicyID: metadata.PolicyID, RepositoryID: metadata.RepositoryID,
			JobID: jobID, JobLockToken: lockToken, OwnerNodeID: ownerNodeID,
			ContainerID: sandbox.ID, ExpectedHead: metadata.HeadSHA,
			ExpectedGeneration: input.ExpectedGeneration,
		})
		if !published {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := services.SandboxProvider.Destroy(cleanupCtx, sandbox); err != nil {
				log.Warn().Err(err).Str("container_id", sandbox.ID).Msg("failed to destroy losing review workspace")
			}
		}
		if publishErr != nil {
			return publishErr
		}
		if !published {
			reason := "fenced_or_cancelled"
			checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			ready, checkErr := stores.CodeReviewWorkspaces.Readiness(checkCtx, input.OrgID, input.MetadataID, input.SessionID, metadata.HeadSHA)
			checkCancel()
			if checkErr != nil {
				log.Warn().Err(checkErr).Msg("could not classify losing review workspace publication")
			} else if ready.Ready && ready.ContainerID != sandbox.ID {
				reason = "sibling_published"
			}
			log.Info().Str("session_id", input.SessionID.String()).Str("container_id", sandbox.ID).
				Str("publication_loss_reason", reason).Msg("code review workspace publication lost")
			return nil
		}
		log.Info().Str("session_id", input.SessionID.String()).Str("container_id", sandbox.ID).Msg("published prepared code review workspace")
		return nil
	}
}

// Saved work from a completed parent can finish through the normal live gates
// without reopening its workspace or launching any reviewer/synthesis thread.
func recoverEndedCodeReviewParent(ctx context.Context, stores *Stores, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (resumeSaved, settled bool, err error) {
	if stores.Sessions == nil || stores.CodeReviewAssessments == nil {
		return false, false, nil
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return false, false, err
	}
	if session.Status != models.SessionStatusCompleted && session.Status != models.SessionStatusFailed {
		return false, false, nil
	}
	// Legacy reviews without an assessment retain their existing path.
	if _, err := stores.CodeReviewAssessments.GetBySessionID(ctx, job.OrgID, job.SessionID); errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	}
	if stores.ThreadSendTx == nil {
		return false, false, fmt.Errorf("ended full review recovery requires transaction support")
	}
	jobID, _ := jobctx.JobIDFromContext(ctx)
	token, _ := jobctx.LockTokenFromContext(ctx)
	resumed, err := db.NewCodeReviewScheduleStore(stores.ThreadSendTx).ReconcileEndedFullReview(ctx, job.OrgID, db.EndedFullReviewParams{
		RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, SessionID: job.SessionID, MetadataID: job.MetadataID, PolicyID: job.PolicyID,
		JobID: jobID, JobLockToken: token, HeadSHA: job.HeadSHA, OutputKey: job.OutputKey, SessionStatus: session.Status, SessionTurn: session.CurrentTurn, TriggeringDisputeID: job.TriggeringDisputeID,
	}, func(results []models.CodeReviewAgentResult) bool {
		return codeReviewSavedResultsComplete(policy, results)
	})
	return resumed, err == nil && !resumed, err
}

func codeReviewSavedResultsComplete(policy models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult) bool {
	for _, result := range results {
		if !codeReviewReviewerResultTerminal(result.Status) {
			return false
		}
	}
	_, usableSynthesis := codeReviewOrchestratorEvidence(results)
	return codeReviewReviewerRosterTerminal(policy, results, false) && codeReviewHasUsableReviewerOutput(results) && usableSynthesis
}
