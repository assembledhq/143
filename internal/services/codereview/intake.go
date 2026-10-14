package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// persistReviewIntake commits GitHub delivery identity, pending intent, and its
// wake before credential preparation or snapshot loading can fail. It uses the
// existing scheduler envelope so older workers can recover the same intent.
func (s *Service) persistReviewIntake(ctx context.Context, input ReviewChangedInput, mode models.CodeReviewRequestMode, force bool) (ReviewRequestedResult, bool, error) {
	identity := reviewIntakeIdentity(input)
	if identity == "" {
		return ReviewRequestedResult{}, false, fmt.Errorf("explicit review requires delivery identity")
	}
	hash, err := reviewRequestHash(input.PullRequestID, mode, input.RequestContext, force)
	if err != nil {
		return ReviewRequestedResult{}, false, err
	}
	result := ReviewRequestedResult{Processed: true, Deferred: true}
	var duplicate bool
	err = s.scheduling.store.WithLockedPR(ctx, input.OrgID, input.RepositoryID, input.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		if err := db.SettleCompletedFullReviewJoins(ctx, tx, input.OrgID, input.PullRequestID); err != nil {
			return err
		}
		generation := state.Generation
		if generation == 0 {
			generation = 1
		}
		id, replay, err := db.RecordCodeReviewRequest(ctx, tx, input.OrgID, input.RepositoryID, input.PullRequestID, "github", identity, mode, hash, generation, nil)
		if err != nil {
			return err
		}
		duplicate = replay
		if replay {
			request, err := db.NewCodeReviewScheduleStore(tx).GetRequestByIdentity(ctx, input.OrgID, "github", identity)
			if err != nil {
				return err
			}
			result.Reused = true
			result.Deferred = request.Status == "pending" || request.Status == "joined"
			if request.SessionID != nil {
				result.SessionID = *request.SessionID
			}
			if request.Status == "cancelled" {
				result.IgnoredReason = "cancelled"
			}
			return nil
		}
		var pending scheduledReviewIntent
		if state.PendingInput != nil {
			if err := json.Unmarshal(state.PendingInput, &pending); err != nil {
				return err
			}
		}
		latest, err := db.NewCodeReviewStore(tx).GetLatestByPullRequest(ctx, input.OrgID, input.PullRequestID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Reserve the forced generation now. A worker from before this change
		// already understands Force and must not reuse a completed team review.
		force = force || (err == nil && input.ChangeReason == explicitReviewRequestChangeReason &&
			input.TriggerSource == models.CodeReviewTriggerSourceTeamReviewer && latest.Status == models.CodeReviewSessionStatusCompleted)
		if force && !pending.Force {
			state.Generation++
		}
		if state.Generation > generation {
			if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET target_generation=$3 WHERE org_id=$1 AND id=$2 AND status='pending'`, input.OrgID, id, state.Generation); err != nil {
				return err
			}
		}
		force = force || pending.Force
		// Older workers ignore NeedsSnapshot, so retain a complete target they
		// can safely dispatch and validate during a rolling deployment. Prefer
		// an already hydrated pending target; otherwise use the validated webhook.
		if !pending.NeedsSnapshot && reviewIntakeHasTarget(pending.Input) {
			input.GitHubRepo = pending.Input.GitHubRepo
			input.GitHubPRNumber = pending.Input.GitHubPRNumber
			input.GitHubPRURL = pending.Input.GitHubPRURL
			input.PullRequestTitle = pending.Input.PullRequestTitle
			input.PullRequestAuthor = pending.Input.PullRequestAuthor
			input.HeadSHA = pending.Input.HeadSHA
			input.BaseSHA = pending.Input.BaseSHA
			input.FromFork = pending.Input.FromFork
		}
		state.PendingInput, err = db.EncodeCodeReviewScheduleInput(scheduledReviewIntent{Input: input, Mode: mode, Force: force, Inputs: pending.Inputs, NeedsSnapshot: true})
		if err != nil {
			return err
		}
		now := s.scheduling.now()
		state.PendingRequestID = &id
		if state.FirstPendingAt == nil {
			state.FirstPendingAt = &now
		}
		state.State = models.CodeReviewScheduleWaiting
		state.WaitReason = models.CodeReviewWaitContext
		state.EligibleAt = nil
		state.RetryAt = &now
		return db.UpsertCodeReviewWake(ctx, tx, input.OrgID, input.PullRequestID, now)
	})
	return result, duplicate, err
}

func isGitHubReviewIntake(input ReviewChangedInput, mode models.CodeReviewRequestMode, requesterID *uuid.UUID) bool {
	return input.ExplicitRequest && requesterID == nil && mode == models.CodeReviewReviewNow &&
		(input.ChangeReason == explicitReviewRequestChangeReason || input.ChangeReason == commentMentionChangeReason)
}

func reviewIntakeHasTarget(input ReviewChangedInput) bool {
	return input.GitHubRepo != "" && input.GitHubPRNumber > 0 && input.HeadSHA != "" && input.BaseSHA != ""
}

func reviewIntakeIdentity(input ReviewChangedInput) string {
	if identity := strings.TrimSpace(input.GitHubDeliveryID); identity != "" {
		return identity
	}
	return strings.TrimSpace(input.ChangeKey)
}

func reviewSnapshotRetryAt(cause error, now time.Time) time.Time {
	delay := time.Minute
	classification := ghservice.ClassifyRetry(cause, now)
	if classification.RetryAfter != nil && *classification.RetryAfter > delay {
		delay = *classification.RetryAfter
	}
	return now.Add(delay)
}

func (s *Service) waitForReviewIntakeSnapshot(ctx context.Context, input ReviewChangedInput, cause error) error {
	s.logger.Warn().Err(cause).Str("org_id", input.OrgID.String()).Str("pull_request_id", input.PullRequestID.String()).Msg("review intake waiting for GitHub")
	at := reviewSnapshotRetryAt(cause, s.scheduling.now())
	return s.scheduling.store.WithLockedPR(ctx, input.OrgID, input.RepositoryID, input.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		if state.PendingInput == nil {
			return nil
		}
		var pending scheduledReviewIntent
		if err := json.Unmarshal(state.PendingInput, &pending); err != nil {
			return err
		}
		if !pending.NeedsSnapshot || reviewIntakeIdentity(pending.Input) != reviewIntakeIdentity(input) {
			return nil
		}
		if state.RetryAt != nil && state.RetryAt.After(at) {
			at = *state.RetryAt
		}
		state.State = models.CodeReviewScheduleWaiting
		state.WaitReason = models.CodeReviewWaitContext
		state.RetryAt = &at
		state.EligibleAt = nil
		return db.UpsertCodeReviewWake(ctx, tx, input.OrgID, input.PullRequestID, at)
	})
}
