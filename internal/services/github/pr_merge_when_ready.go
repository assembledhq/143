package github

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

var (
	ErrPullRequestMergeWhenReadyNotQueueable = errors.New("pull request cannot be queued for merge when ready")
	ErrPullRequestMergeWhenReadyInProgress   = errors.New("pull request merge when ready is already in progress")
	// ErrPullRequestMergeWhenReadyChecksPending signals that a merge-when-ready
	// request looks mergeable only because GitHub has not registered any check
	// runs yet for a freshly requested head. The worker handler translates this
	// into a short retryable delay so we re-evaluate once checks appear (or
	// conclude the repo genuinely has no CI after the grace window) instead of
	// merging in the eventual-consistency gap right after PR creation.
	ErrPullRequestMergeWhenReadyChecksPending = errors.New("pull request merge when ready: checks have not registered yet")
)

const mergeWhenReadyMergingStaleAfter = 15 * time.Minute

// mergeWhenReadyChecksRegisterGrace is how long we wait for GitHub to register
// check runs on a freshly requested head before concluding the repository has
// no CI and proceeding to merge. GitHub creates check runs (status queued)
// within a few seconds of a push and the list-check-runs API reflects them
// immediately, so this is a several-fold margin over the eventual-consistency
// window where the PR reports clean with an empty check-runs list. The grace
// only ever delays repos with no CI — once any check registers, CanMerge gates
// on it directly and this window no longer applies.
const mergeWhenReadyChecksRegisterGrace = 30 * time.Second

func (s *PRService) QueueMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("load pull request: %w", err)
	}
	if pr.Status != models.PullRequestStatusOpen {
		return nil, fmt.Errorf("%w: pull request status is %q", ErrPullRequestMergeWhenReadyNotQueueable, pr.Status)
	}
	if err := s.ensureStackParentMerged(ctx, pr); err != nil {
		return nil, err
	}
	if pr.MergeWhenReadyState == models.PullRequestMergeWhenReadyStateMerging {
		return nil, ErrPullRequestMergeWhenReadyInProgress
	}
	if pr.MergeWhenReadyState == models.PullRequestMergeWhenReadyStateSucceeded {
		return nil, fmt.Errorf("%w: pull request already merged", ErrPullRequestMergeWhenReadyNotQueueable)
	}

	health, err := s.currentMergeWhenReadyHealth(ctx, pr)
	if err != nil {
		return nil, err
	}
	if reason, queueable := mergeWhenReadyQueueability(health); !queueable {
		return nil, fmt.Errorf("%w: %s", ErrPullRequestMergeWhenReadyNotQueueable, reason)
	}
	if err := s.ensureMergeWhenReadyAuthAvailable(ctx, orgID, pr, userID); err != nil {
		return nil, err
	}

	status, err := s.pullRequests.QueueMergeWhenReady(ctx, orgID, pullRequestID, userID, health.HeadSHA, health.HealthVersion)
	if err != nil {
		return nil, fmt.Errorf("queue merge when ready: %w", err)
	}

	pr.MergeWhenReadyState = status.State
	s.enqueueMergeWhenReadyProcessing(ctx, pr)
	return &status, nil
}

func (s *PRService) CancelMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("load pull request: %w", err)
	}
	switch pr.MergeWhenReadyState {
	case models.PullRequestMergeWhenReadyStateMerging:
		return nil, ErrPullRequestMergeWhenReadyInProgress
	case models.PullRequestMergeWhenReadyStateSucceeded:
		return nil, fmt.Errorf("%w: pull request already merged", ErrPullRequestMergeWhenReadyNotQueueable)
	case models.PullRequestMergeWhenReadyStateOff:
		status := mergeWhenReadyStatusFromPullRequest(pr)
		return &status, nil
	}

	status, err := s.pullRequests.CancelMergeWhenReady(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("cancel merge when ready: %w", err)
	}
	return &status, nil
}

func (s *PRService) ProcessMergeWhenReady(ctx context.Context, orgID, pullRequestID uuid.UUID) error {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return fmt.Errorf("load pull request: %w", err)
	}
	if pr.MergeWhenReadyState != models.PullRequestMergeWhenReadyStateQueued && !isStaleMergeWhenReadyMerging(pr, time.Now()) {
		return nil
	}
	if pr.Status != models.PullRequestStatusOpen {
		return s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, "Pull request is no longer open.")
	}
	if err := s.ensureStackParentMerged(ctx, pr); err != nil {
		return s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, err.Error())
	}

	if err := s.SyncPullRequestState(ctx, orgID, pullRequestID); err != nil && !errors.Is(err, ErrPullRequestMergeabilityPending) {
		return fmt.Errorf("sync pull request before merge when ready: %w", err)
	}

	pr, err = s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return fmt.Errorf("reload pull request: %w", err)
	}
	if pr.MergeWhenReadyState != models.PullRequestMergeWhenReadyStateQueued && !isStaleMergeWhenReadyMerging(pr, time.Now()) {
		return nil
	}
	health, err := s.buildPullRequestHealthResponse(ctx, pr)
	if err != nil {
		return fmt.Errorf("build pull request health: %w", err)
	}
	if pr.MergeWhenReadyHeadSHA != "" && health.HeadSHA != "" && pr.MergeWhenReadyHeadSHA != health.HeadSHA {
		return s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, "PR head changed after merge when ready was enabled.")
	}

	if health.CanMerge {
		if mergeWhenReadyShouldWaitForChecks(pr, health, time.Now()) {
			// Mergeable only because no check runs have registered yet for a
			// freshly requested head. Defer rather than merge: right after PR
			// creation GitHub reports the PR clean with an empty check-runs list
			// before CI registers, so merging here would land the PR before its
			// checks even start. The retryable error re-evaluates once checks
			// appear (flipping CanMerge to false until they pass) or, after the
			// grace window, treats the repo as genuinely having no CI.
			return ErrPullRequestMergeWhenReadyChecksPending
		}
		if pr.MergeWhenReadyRequestedBy == nil {
			return s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, "Merge when ready is missing the requesting user.")
		}
		claimed, err := s.pullRequests.ClaimMergeWhenReadyForProcessing(ctx, orgID, pullRequestID, time.Now().Add(-mergeWhenReadyMergingStaleAfter))
		if err != nil {
			return fmt.Errorf("mark merge when ready merging: %w", err)
		}
		if !claimed {
			return nil
		}
		if _, err := s.mergePullRequest(ctx, orgID, pullRequestID, *pr.MergeWhenReadyRequestedBy, &pr.MergeWhenReadyHeadSHA); err != nil {
			if errors.Is(err, ErrPullRequestNotYetMergeable) {
				// The pre-merge re-sync caught a transient block (checks still
				// running, mergeability still computing) after we had already
				// claimed the merge. Release the claim back to queued so the
				// next sync/reconcile retries instead of failing the user's
				// merge-when-ready request with a misleading error.
				if releaseErr := s.pullRequests.ReleaseMergeWhenReadyClaim(ctx, orgID, pullRequestID); releaseErr != nil {
					return fmt.Errorf("merge when ready hit a transient block, then releasing the claim failed: %w", releaseErr)
				}
				return ErrPullRequestMergeWhenReadyChecksPending
			}
			reason := mergeWhenReadyFailureMessage(err)
			if markErr := s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, reason); markErr != nil {
				return fmt.Errorf("merge when ready failed (%s), then marking failed failed: %w", reason, markErr)
			}
			return nil
		}
		return s.pullRequests.MarkMergeWhenReadySucceeded(ctx, orgID, pullRequestID)
	}

	if reason, queueable := mergeWhenReadyQueueability(health); !queueable {
		return s.pullRequests.MarkMergeWhenReadyFailed(ctx, orgID, pullRequestID, reason)
	}
	return nil
}

func (s *PRService) ensureMergeWhenReadyAuthAvailable(ctx context.Context, orgID uuid.UUID, pr models.PullRequest, userID uuid.UUID) error {
	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if s.orgs != nil {
		org, err := s.orgs.GetByID(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load org settings for merge when ready: %w", err)
		}
		parsed, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			return fmt.Errorf("parse org settings for merge when ready: %w", err)
		}
		orgSettings = parsed
	}
	if orgSettings.PRAuthorship != models.PRAuthorshipUserRequired {
		return nil
	}

	repo, err := s.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return fmt.Errorf("load repository for merge when ready auth: %w", err)
	}
	resolverSession := &models.Session{OrgID: orgID, TriggeredByUserID: &userID}
	if _, err := s.identityResolver().Resolve(ctx, resolverSession, &repo, orgSettings, ""); err != nil {
		return err
	}
	return nil
}

func mergeWhenReadyStatusFromPullRequest(pr models.PullRequest) models.PullRequestMergeWhenReadyStatus {
	return models.PullRequestMergeWhenReadyStatus{
		State:                  pr.MergeWhenReadyState,
		RequestedByUserID:      pr.MergeWhenReadyRequestedBy,
		RequestedAt:            pr.MergeWhenReadyRequestedAt,
		RequestedHeadSHA:       pr.MergeWhenReadyHeadSHA,
		RequestedHealthVersion: pr.MergeWhenReadyHealthVersion,
		LastError:              pr.MergeWhenReadyError,
	}
}

func isStaleMergeWhenReadyMerging(pr models.PullRequest, now time.Time) bool {
	return pr.MergeWhenReadyState == models.PullRequestMergeWhenReadyStateMerging &&
		pr.MergeWhenReadyUpdatedAt != nil &&
		pr.MergeWhenReadyUpdatedAt.Before(now.Add(-mergeWhenReadyMergingStaleAfter))
}

func (s *PRService) currentMergeWhenReadyHealth(ctx context.Context, pr models.PullRequest) (*models.PullRequestHealthResponse, error) {
	if pr.GitHubStateSyncedAt == nil || pr.HealthVersion == 0 {
		if err := s.SyncPullRequestState(ctx, pr.OrgID, pr.ID); err != nil && !errors.Is(err, ErrPullRequestMergeabilityPending) {
			return nil, fmt.Errorf("sync pull request before queueing merge when ready: %w", err)
		}
		refreshed, err := s.pullRequests.GetByID(ctx, pr.OrgID, pr.ID)
		if err != nil {
			return nil, fmt.Errorf("reload pull request after sync: %w", err)
		}
		pr = refreshed
	}
	health, err := s.buildPullRequestHealthResponse(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("build pull request health: %w", err)
	}
	return health, nil
}

func mergeWhenReadyQueueability(health *models.PullRequestHealthResponse) (string, bool) {
	if health.Status != models.PullRequestStatusOpen {
		return "Pull request is no longer open.", false
	}
	if health.HeadSHA == "" {
		return "Pull request head is not known yet.", false
	}
	if len(health.ActiveRepairs) > 0 {
		return "Wait for the active repair session to finish before enabling merge when ready.", false
	}
	if health.HasConflicts || health.CanResolveConflicts || health.MergeState == models.PullRequestMergeStateConflicted {
		return "Resolve conflicts before enabling merge when ready.", false
	}
	if health.FailingTestCount > 0 || health.CanFixTests {
		return "Fix failing checks before enabling merge when ready.", false
	}
	for _, check := range health.Checks {
		if check.Status == models.PullRequestCheckStatusFailed {
			return "Fix failing checks before enabling merge when ready.", false
		}
	}
	return "", true
}

// mergeWhenReadyShouldWaitForChecks reports whether a CanMerge=true snapshot is
// trustworthy only because GitHub has not yet registered any check runs for the
// requested head. Within the grace window after the merge-when-ready request we
// hold off so CI has a chance to register; once any check appears the checks
// list is non-empty and CanMerge already gates on it, and after the window we
// treat an empty list as "no CI configured" and let the merge proceed.
func mergeWhenReadyShouldWaitForChecks(pr models.PullRequest, health *models.PullRequestHealthResponse, now time.Time) bool {
	if len(health.Checks) > 0 {
		return false
	}
	if pr.MergeWhenReadyRequestedAt == nil {
		return false
	}
	return now.Sub(*pr.MergeWhenReadyRequestedAt) < mergeWhenReadyChecksRegisterGrace
}

// mergeBlockIsTransient reports whether a CanMerge=false health snapshot is
// blocked by a condition expected to clear on its own — GitHub still computing
// mergeability, required checks still running, or branch protection temporarily
// blocking — rather than a terminal condition a human must fix (merge conflicts
// or failed checks). It mirrors mergeWhenReadyQueueability's wait-vs-fail split
// so the pre-merge re-sync inside mergePullRequest classifies blocks the same
// way the queueing gate does.
func mergeBlockIsTransient(health *models.PullRequestHealthResponse) bool {
	if health.HasConflicts || health.CanResolveConflicts || health.MergeState == models.PullRequestMergeStateConflicted {
		return false
	}
	if health.FailingTestCount > 0 || health.CanFixTests {
		return false
	}
	for _, check := range health.Checks {
		if check.Status == models.PullRequestCheckStatusFailed {
			return false
		}
	}
	return true
}

func mergeWhenReadyFailureMessage(err error) string {
	switch {
	case errors.Is(err, ErrGitHubUserAuthRequired):
		return "Connect your GitHub account to merge this pull request as yourself."
	case errors.Is(err, ErrGitHubUserAuthRepoAccessDenied):
		return "Your GitHub account cannot access this repository for merge."
	case errors.Is(err, ErrNoMergeMethodAllowed):
		return "Repository does not allow any merge method."
	case errors.Is(err, ErrPullRequestNotMergeable):
		return "Pull request is not in a mergeable state."
	case errors.Is(err, ErrPullRequestHeadChanged):
		return "PR head changed after merge when ready was enabled."
	default:
		return "Failed to merge pull request."
	}
}
