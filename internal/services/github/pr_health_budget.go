package github

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/services/github/identity"
)

var errPullRequestBackgroundQuotaDeferred = errors.New("pull request background quota budget deferred")

func (s *PRService) backgroundQuotaFlightKey(ctx context.Context, key string) string {
	if PullRequestSyncReasonFromContext(ctx) == PullRequestSyncReasonStaleReconcile && reconciliationScopeFromContext(ctx) != nil && s.backgroundQuotaController.BackgroundQuotaEnabled() {
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
