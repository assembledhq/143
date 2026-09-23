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

func codeReviewWorkspaceWaitError(reason string) error {
	delay, limit := codeReviewWorkspaceWait, codeReviewWorkspaceWaitLimit
	return &RetryableError{Err: fmt.Errorf("waiting for code review workspace: %s", reason), RetryAfter: &delay, MaxRetryDuration: &limit}
}

// ensureCodeReviewWorkspaceReady runs after deterministic early-stop and
// before the first reviewer thread is dispatched. A completed init job is not
// evidence of readiness; every attempt checks the current holder and owner.
func ensureCodeReviewWorkspaceReady(ctx context.Context, stores *Stores, services *Services, log zerolog.Logger, job runCodeReviewPayload) error {
	if services == nil || !services.CodeReviewWorkspacePreparationEnabled {
		return nil
	}
	if stores == nil || stores.CodeReviewWorkspaces == nil || stores.Sessions == nil || stores.Jobs == nil {
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
	if session.SnapshotKey != nil && session.WorkspaceGeneration > 0 {
		// Once a reviewer has used and checkpointed the workspace, the ordinary
		// M2 resume path owns recovery. Initial preparation only serves cold
		// sessions; it must not overwrite a later snapshot.
		return nil
	}
	target := session.WorkerNodeID
	if session.ContainerID == nil || target == nil {
		target, err = stores.Jobs.SelectWorkerWithSandboxCapacity(ctx, "")
		if err != nil {
			return fmt.Errorf("choose review workspace worker: %w", err)
		}
	}
	payload := prepareCodeReviewWorkspacePayload{
		OrgID: job.OrgID, MetadataID: metadata.ID, SessionID: session.ID,
		ExpectedGeneration: session.WorkspaceGeneration,
	}
	key := fmt.Sprintf("code_review_prepare:%s:%s:%d", metadata.ID, session.ID, session.WorkspaceGeneration)
	_, err = stores.Jobs.EnqueueWithOpts(ctx, job.OrgID, db.EnqueueOpts{
		Queue: "agent", JobType: models.JobTypePrepareCodeReviewWorkspace,
		Payload: payload, Priority: 6, DedupeKey: &key, MaxAttempts: 8,
		TargetNodeID: target,
	})
	if err != nil {
		return fmt.Errorf("enqueue code review workspace preparation: %w", err)
	}
	log.Info().Str("session_id", session.ID.String()).Int64("generation", session.WorkspaceGeneration).Msg("waiting for prepared code review workspace")
	return codeReviewWorkspaceWaitError("preparation pending")
}

func newPrepareCodeReviewWorkspaceHandler(stores *Stores, services *Services, log zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, raw json.RawMessage) (returnErr error) {
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
		if session.WorkspaceGeneration != input.ExpectedGeneration || session.Origin != models.SessionOriginCodeReview || session.RepositoryID == nil || *session.RepositoryID != metadata.RepositoryID {
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
			return nil // a newer generation, cancelled review, or lost lease won
		}
		log.Info().Str("session_id", input.SessionID.String()).Str("container_id", sandbox.ID).Msg("published prepared code review workspace")
		return nil
	}
}
