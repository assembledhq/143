package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services/agent"
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
const codeReviewWorkspaceWaitLimit = 8 * time.Minute

var errCodeReviewWorkspaceStopped = errors.New("code review stopped before workspace readiness")

func codeReviewWorkspaceWaitError(reason string) *RetryableError {
	delay, limit := codeReviewWorkspaceWait, codeReviewWorkspaceWaitLimit
	return &RetryableError{Err: fmt.Errorf("waiting for code review workspace: %s", reason), RetryAfter: &delay, MaxRetryDuration: &limit}
}

func codeReviewControllerWorkspaceWaitError(reason string, startedAt time.Time) error {
	retry := codeReviewWorkspaceWaitError(reason)
	retry.RetryWindowStartedAt = &startedAt
	// The readiness gate owns the eight-minute deadline and falls back to the
	// ordinary reviewer path. The worker's age+delay check would otherwise
	// dead-letter the review on the final scheduled poll before it can fall back.
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
	if services == nil || !services.CodeReviewWorkspacePreparationEnabled || !services.CodeReviewExecutorPlacementEnabled {
		return false, nil
	}
	if stores == nil || stores.Sessions == nil || stores.Jobs == nil {
		return false, fmt.Errorf("code review workspace preparation is enabled without required stores")
	}
	generation, err := stores.Sessions.WorkspaceGenerationForReview(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return false, fmt.Errorf("load review workspace generation before preparation checkpoint: %w", err)
	}
	key := codeReviewWorkspacePreparationKey(job.MetadataID, job.SessionID, generation)
	_, err = stores.Jobs.FirstJobCreatedAtByDedupeKey(ctx, job.OrgID, "agent", key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load review workspace preparation checkpoint: %w", err)
	}
	return true, nil
}

func ensureCodeReviewWorkspaceReady(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload) error {
	return ensureCodeReviewWorkspaceReadyWithFallback(ctx, stores, services, log, job, false)
}

// Once the first gate has observed a prepared workspace, the controller runs
// the live GitHub preflight. If the holder expires during that work, retrying
// preparation would repeat the whole preflight; reviewer launch can instead
// use the existing M2 container recovery path.
func ensureCodeReviewWorkspaceReadyAfterPreflight(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload) error {
	return ensureCodeReviewWorkspaceReadyWithFallback(ctx, stores, services, log, job, true)
}

// A completed initializer is not proof of readiness. Both gates check the
// current holder, review revision, and session before dispatching reviewers.
func ensureCodeReviewWorkspaceReadyWithFallback(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload, afterPreflight bool) error {
	if services == nil || !services.CodeReviewWorkspacePreparationEnabled || !services.CodeReviewExecutorPlacementEnabled {
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
		log.Warn().Str("session_id", session.ID.String()).
			Msg("prepared workspace lost readiness during preflight; using ordinary reviewer path")
		return nil
	}
	payload := prepareCodeReviewWorkspacePayload{
		OrgID: job.OrgID, MetadataID: metadata.ID, SessionID: session.ID,
		ExpectedGeneration: session.WorkspaceGeneration,
	}
	key := codeReviewWorkspacePreparationKey(metadata.ID, session.ID, session.WorkspaceGeneration)
	startedAt, startErr := stores.Jobs.FirstJobCreatedAtByDedupeKey(ctx, job.OrgID, "agent", key)
	if startErr != nil && !errors.Is(startErr, pgx.ErrNoRows) {
		return fmt.Errorf("load review workspace wait start: %w", startErr)
	}
	if startErr == nil && !time.Now().Before(startedAt.Add(codeReviewWorkspaceWaitLimit)) {
		cancelled, cancelErr := stores.Jobs.CancelActiveCodeReviewPreparation(ctx, job.OrgID, "agent", key)
		if cancelErr != nil {
			// An uncertain cancellation cannot bypass the session/holder CAS in
			// either workspace path, so let the review proceed after logging it.
			log.Warn().Err(cancelErr).Str("session_id", session.ID.String()).Msg("could not cancel timed-out review workspace preparation")
		}
		log.Warn().Str("session_id", session.ID.String()).Int64("cancelled_preparation_jobs", cancelled).
			Msg("review workspace preparation exceeded wait limit; using ordinary reviewer path")
		return nil
	}
	latestStatus, statusErr := stores.Jobs.LatestJobStatusByDedupeKey(ctx, job.OrgID, "agent", key)
	if statusErr != nil && !errors.Is(statusErr, pgx.ErrNoRows) {
		return fmt.Errorf("load review workspace preparation status: %w", statusErr)
	}
	switch latestStatus {
	case models.JobStatusFailed, models.JobStatusDeadLetter, models.JobStatusCancelled:
		log.Warn().Str("session_id", session.ID.String()).Str("job_status", string(latestStatus)).
			Msg("using ordinary reviewer workspace path after preparation job stopped")
		return nil
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
		startedAt, err = stores.Jobs.FirstJobCreatedAtByDedupeKey(ctx, job.OrgID, "agent", key)
		if err != nil {
			return fmt.Errorf("load review workspace wait start after enqueue: %w", err)
		}
	}
	log.Debug().Str("session_id", session.ID.String()).Int64("generation", session.WorkspaceGeneration).Msg("waiting for prepared code review workspace")
	return codeReviewControllerWorkspaceWaitError("preparation pending", startedAt)
}

func newPrepareCodeReviewWorkspaceHandler(stores *Stores, services *Services, log zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, raw json.RawMessage) (returnErr error) {
		if services != nil && (!services.CodeReviewWorkspacePreparationEnabled || !services.CodeReviewExecutorPlacementEnabled) {
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
				alive, err := services.SandboxProvider.IsAlive(ctx, &agent.Sandbox{ID: *session.ContainerID, Provider: "docker"})
				if err != nil {
					return &RetryableError{Err: fmt.Errorf("probe review workspace owner container: %w", err), RetryAfter: durationPtr(codeReviewWorkspaceWait), MaxRetryDuration: durationPtr(codeReviewWorkspaceWaitLimit)}
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
		sandbox, err := services.CodeReviewWorkspacePreparer.PrepareCodeReviewWorkspace(ctx, &session, metadata.HeadSHA)
		if err != nil {
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
					return &RetryableError{Err: err, RetryAfter: &delay, TargetNodeID: alternate, MaxRetryDuration: &limit}
				}
				return codeReviewWorkspaceWaitError("worker sandbox capacity reached")
			}
			return err
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
