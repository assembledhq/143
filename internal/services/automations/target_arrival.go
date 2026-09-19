package automations

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: arrival bookkeeping (design doc 125, "Head
// Authority" and "Waiting and Coalescing"). Runs of a per_target automation
// that carry a pull request are attached to their automation_targets row
// inside the run-creation transaction, which also locks the target so
// head observation and push acceptance are serialized with dispatch.

const (
	// githubActionSynchronize is the pull_request action for a push.
	githubActionSynchronize = "synchronize"
	githubActionOpened      = "opened"
	githubActionReopened    = "reopened"
	githubActionReadyReview = "ready_for_review"

	// automationTargetWaitingCap bounds the runs waiting per target.
	automationTargetWaitingCap = 10
	// automationHeadAmbiguityWindow is how long ambiguous push candidates
	// wait for a dispatch-time lookup before the deadline sweep converts
	// them to unresolved.
	automationHeadAmbiguityWindow = 30 * time.Minute
)

// githubAutomationTargetStore is the target-store surface arrival needs.
type githubAutomationTargetStore interface {
	LockOrCreate(ctx context.Context, tx pgx.Tx, orgID, automationID, repositoryID uuid.UUID, kind models.AutomationTargetKind, key string) (models.AutomationTarget, error)
	AdoptHead(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, headSHA string, updatedAt *time.Time) (int, error)
	TouchObservedHead(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, headSHA string, updatedAt time.Time) error
	MarkHeadResolutionPending(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, deadline time.Time) error
	SetLifecycle(ctx context.Context, q db.DBTX, orgID, targetID uuid.UUID, state models.AutomationTargetLifecycleState) error
}

// githubAutomationArrivalRunStore is the run-store surface arrival needs,
// kept separate from githubAutomationRunStore so existing callers that do
// not wire continuity keep compiling.
type githubAutomationArrivalRunStore interface {
	RecordArrival(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, arrival db.AutomationRunArrival) error
	TerminalizeUnstarted(ctx context.Context, q db.DBTX, orgID, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, supersededBy *uuid.UUID, summary string) (bool, error)
	SupersedeWaitingPush(ctx context.Context, tx pgx.Tx, orgID, targetID, newRunID uuid.UUID, newEpoch int) (int64, error)
	CountWaiting(ctx context.Context, q db.DBTX, orgID, targetID uuid.UUID) (int, error)
	MarkWaiting(ctx context.Context, q db.DBTX, orgID, runID uuid.UUID) (bool, error)
}

// SetTargetStores enables per-target arrival bookkeeping. Without it, runs
// of per_target automations are created without a target and dispatch as
// ordinary per-run sessions.
func (s *GitHubEventTriggerService) SetTargetStores(targets githubAutomationTargetStore, runs githubAutomationArrivalRunStore) {
	s.targets = targets
	s.arrivalRuns = runs
}

// isPushArrival reports whether the request is a pull request push: only
// these are ever skipped or superseded on head grounds.
func isPushArrival(req GitHubEventTriggerRequest) bool {
	return req.Event == models.AutomationGitHubEventPullRequestUpdated && req.PullRequestAction == githubActionSynchronize
}

// carriesObservableHead reports whether the delivery carries the PR head
// together with a GitHub-side timestamp that can order it.
func carriesObservableHead(req GitHubEventTriggerRequest) bool {
	if req.HeadSHA == "" {
		return false
	}
	switch req.PullRequestAction {
	case githubActionSynchronize, githubActionOpened, githubActionReopened, githubActionReadyReview:
		return true
	default:
		return false
	}
}

// recordTargetArrival attaches the run to its target inside tx and applies
// head observation, push acceptance, the lifecycle gate, and the waiting
// cap. It returns false when the run was terminalized at arrival and must
// not be dispatched.
func (s *GitHubEventTriggerService) recordTargetArrival(ctx context.Context, tx pgx.Tx, automation models.Automation, run models.AutomationRun, req GitHubEventTriggerRequest) (bool, error) {
	if s.targets == nil || s.arrivalRuns == nil {
		return true, nil
	}
	if automation.SessionContinuity.OrDefault() != models.AutomationSessionContinuityPerTarget {
		return true, nil
	}
	if req.PullRequestNumber <= 0 || req.RepositoryID == uuid.Nil {
		return true, nil
	}
	orgID := automation.OrgID
	target, err := s.targets.LockOrCreate(ctx, tx, orgID, automation.ID, req.RepositoryID, models.AutomationTargetKindGitHubPullRequest, strconv.Itoa(req.PullRequestNumber))
	if err != nil {
		return false, fmt.Errorf("lock automation target: %w", err)
	}

	// Lifecycle: a reopened delivery reopens the target; a merged event is
	// the final turn on a merged target; anything else after close is
	// skipped.
	switch {
	case req.PullRequestAction == githubActionReopened && target.LifecycleState != models.AutomationTargetLifecycleOpen:
		if err := s.targets.SetLifecycle(ctx, tx, orgID, target.ID, models.AutomationTargetLifecycleOpen); err != nil {
			return false, err
		}
		target.LifecycleState = models.AutomationTargetLifecycleOpen
	case req.Event == models.AutomationGitHubEventPullRequestMerged && target.LifecycleState != models.AutomationTargetLifecycleMerged:
		if err := s.targets.SetLifecycle(ctx, tx, orgID, target.ID, models.AutomationTargetLifecycleMerged); err != nil {
			return false, err
		}
		target.LifecycleState = models.AutomationTargetLifecycleMerged
	}
	// A pull_request delivery describes the PR's current state, so it
	// refreshes the openness evidence that dispatch revalidates before
	// creating a generation.
	if target.LifecycleState == models.AutomationTargetLifecycleOpen && req.PullRequestAction != "" {
		if err := s.targets.SetLifecycle(ctx, tx, orgID, target.ID, models.AutomationTargetLifecycleOpen); err != nil {
			return false, err
		}
	}
	arrival := db.AutomationRunArrival{
		TargetID:             target.ID,
		GitHubAction:         req.PullRequestAction,
		PullRequestUpdatedAt: req.PullRequestUpdatedAt,
	}
	if target.LifecycleState != models.AutomationTargetLifecycleOpen && req.Event != models.AutomationGitHubEventPullRequestMerged {
		if err := s.arrivalRuns.RecordArrival(ctx, tx, orgID, run.ID, arrival); err != nil {
			return false, err
		}
		if _, err := s.arrivalRuns.TerminalizeUnstarted(ctx, tx, orgID, run.ID, models.AutomationRunOutcomePRClosed, nil, "pull request is no longer open"); err != nil {
			return false, err
		}
		return false, nil
	}

	waiting, err := s.arrivalRuns.CountWaiting(ctx, tx, orgID, target.ID)
	if err != nil {
		return false, err
	}
	if waiting >= automationTargetWaitingCap {
		if err := s.arrivalRuns.RecordArrival(ctx, tx, orgID, run.ID, arrival); err != nil {
			return false, err
		}
		if _, err := s.arrivalRuns.TerminalizeUnstarted(ctx, tx, orgID, run.ID, models.AutomationRunOutcomeWaitOverflow, nil, fmt.Sprintf("more than %d runs are already waiting for this pull request", automationTargetWaitingCap)); err != nil {
			return false, err
		}
		return false, nil
	}

	push := isPushArrival(req)
	authoritative := models.AutomationRunHeadAuthoritative
	ambiguous := models.AutomationRunHeadAmbiguous
	newer := carriesObservableHead(req) && req.PullRequestUpdatedAt != nil &&
		(target.ObservedHeadUpdatedAt == nil || req.PullRequestUpdatedAt.After(*target.ObservedHeadUpdatedAt))
	sameHead := req.HeadSHA != "" && target.ObservedHeadSHA != nil && req.HeadSHA == *target.ObservedHeadSHA
	older := carriesObservableHead(req) && req.PullRequestUpdatedAt != nil && target.ObservedHeadUpdatedAt != nil &&
		req.PullRequestUpdatedAt.Before(*target.ObservedHeadUpdatedAt)

	switch {
	case newer && !sameHead:
		epoch, err := s.targets.AdoptHead(ctx, tx, orgID, target.ID, req.HeadSHA, req.PullRequestUpdatedAt)
		if err != nil {
			return false, err
		}
		arrival.HeadEpoch = &epoch
		arrival.HeadResolution = &authoritative
		if push {
			if _, err := s.arrivalRuns.SupersedeWaitingPush(ctx, tx, orgID, target.ID, run.ID, epoch); err != nil {
				return false, err
			}
		}
	case sameHead:
		// A same-head delivery joins the observed epoch. A newer timestamp
		// for the same head (an edit, a force-push back to it) advances the
		// observed timestamp so a delayed older delivery cannot outrank it,
		// but opens no new epoch.
		epoch := target.HeadEpoch
		arrival.HeadEpoch = &epoch
		arrival.HeadResolution = &authoritative
		if req.PullRequestUpdatedAt != nil && (target.ObservedHeadUpdatedAt == nil || req.PullRequestUpdatedAt.After(*target.ObservedHeadUpdatedAt)) {
			if err := s.targets.TouchObservedHead(ctx, tx, orgID, target.ID, req.HeadSHA, *req.PullRequestUpdatedAt); err != nil {
				return false, err
			}
		}
	case push && older:
		if err := s.arrivalRuns.RecordArrival(ctx, tx, orgID, run.ID, arrival); err != nil {
			return false, err
		}
		if _, err := s.arrivalRuns.TerminalizeUnstarted(ctx, tx, orgID, run.ID, models.AutomationRunOutcomeStaleHead, nil, "a newer push for this pull request was already observed"); err != nil {
			return false, err
		}
		return false, nil
	case push:
		// Equal timestamp with a different head, or a missing timestamp:
		// never resolved by guessing. The candidate waits, visibly, for the
		// dispatch-time lookup or the ambiguity deadline.
		arrival.HeadResolution = &ambiguous
		if err := s.targets.MarkHeadResolutionPending(ctx, tx, orgID, target.ID, s.now().Add(automationHeadAmbiguityWindow)); err != nil {
			return false, err
		}
		if err := s.arrivalRuns.RecordArrival(ctx, tx, orgID, run.ID, arrival); err != nil {
			return false, err
		}
		if _, err := s.arrivalRuns.MarkWaiting(ctx, tx, orgID, run.ID); err != nil {
			return false, err
		}
		return true, nil
	default:
		// Non-push events at an unobserved head execute with a null epoch
		// and can never move the baseline.
	}
	if err := s.arrivalRuns.RecordArrival(ctx, tx, orgID, run.ID, arrival); err != nil {
		return false, err
	}
	return true, nil
}
