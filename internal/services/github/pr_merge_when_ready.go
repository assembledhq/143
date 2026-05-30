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
)

const mergeWhenReadyMergingStaleAfter = 15 * time.Minute

func (s *PRService) QueueMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("load pull request: %w", err)
	}
	if pr.Status != models.PullRequestStatusOpen {
		return nil, fmt.Errorf("%w: pull request status is %q", ErrPullRequestMergeWhenReadyNotQueueable, pr.Status)
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
