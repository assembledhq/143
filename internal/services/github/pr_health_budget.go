package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

var errPullRequestBackgroundQuotaDeferred = errors.New("pull request background quota budget deferred")

func (s *PRService) backgroundQuotaFlightKey(ctx context.Context, key string, resolution *identity.Resolution) string {
	if s.backgroundQuotaFlightNeedsResolution(ctx) && resolution != nil && resolution.Source == identity.SourceApp && s.backgroundQuotaController.EnforcesBackgroundQuota(resolution.InstallationID) {
		return key + ":quota-sweep"
	}
	return key
}

func (s *PRService) admitBackgroundPullRequestSync(ctx context.Context, orgID, pullRequestID uuid.UUID, resolution *identity.Resolution) bool {
	if PullRequestSyncReasonFromContext(ctx) != PullRequestSyncReasonStaleReconcile || reconciliationScopeFromContext(ctx) == nil || resolution == nil || resolution.Source != identity.SourceApp {
		return true
	}
	return s.backgroundQuotaController.AdmitBackground(ctx, resolution.InstallationID, orgID.String()+":"+pullRequestID.String())
}

func (s *PRService) backgroundQuotaFlightNeedsResolution(ctx context.Context) bool {
	return PullRequestSyncReasonFromContext(ctx) == PullRequestSyncReasonStaleReconcile && reconciliationScopeFromContext(ctx) != nil && s.backgroundQuotaController.BackgroundQuotaEnforcementEnabled()
}

type resolvedPullRequestHealthSync struct {
	pullRequest models.PullRequest
	repository  models.Repository
	resolution  *identity.Resolution
}

func (s *PRService) resolvePullRequestHealthSync(ctx context.Context, orgID, pullRequestID uuid.UUID) (*resolvedPullRequestHealthSync, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	repo, err := s.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			anyStatusRepo, anyStatusErr := s.repos.GetByFullNameAnyStatus(ctx, orgID, pr.GitHubRepo)
			if anyStatusErr == nil && repoBlocksPullRequestHealthSync(anyStatusRepo) {
				return nil, fmt.Errorf("%w: %s", ErrPullRequestRepositoryDisconnected, pr.GitHubRepo)
			}
			if anyStatusErr != nil && !errors.Is(anyStatusErr, pgx.ErrNoRows) {
				return nil, fmt.Errorf("load repository for pull request health sync: %w", anyStatusErr)
			}
		}
		return nil, fmt.Errorf("load repository for pull request health sync: %w", err)
	}
	resolution, err := s.getInstallationResolutionForRepo(ctx, orgID, &repo)
	if err != nil {
		return nil, fmt.Errorf("load installation token for pull request health sync: %w", err)
	}
	return &resolvedPullRequestHealthSync{pullRequest: pr, repository: repo, resolution: resolution}, nil
}

func (s *PRService) syncBudgetedPullRequestState(ctx context.Context, readKey string, orgID, pullRequestID uuid.UUID, resolved *resolvedPullRequestHealthSync) error {
	if err := reconciliationScopeFromContext(ctx).deferral(resolved.resolution); err != nil {
		return err
	}
	if !s.admitBackgroundPullRequestSync(ctx, orgID, pullRequestID, resolved.resolution) {
		return errPullRequestBackgroundQuotaDeferred
	}
	return s.syncAdmittedPullRequestState(ctx, readKey, orgID, pullRequestID, resolved)
}

func (s *PRService) syncAdmittedPullRequestState(ctx context.Context, readKey string, orgID, pullRequestID uuid.UUID, resolved *resolvedPullRequestHealthSync) error {
	result := s.prHealthSyncGroup.DoChan(readKey, func() (any, error) {
		return nil, s.syncResolvedPullRequestState(ctx, orgID, pullRequestID, resolved, true)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case completed := <-result:
		reconciliationScopeFromContext(ctx).observe(nil, completed.Err)
		return completed.Err
	}
}
