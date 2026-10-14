package github

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/stretchr/testify/require"
)

func TestHealthRequestOriginAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		origin   PullRequestSyncOrigin
		sweep    bool
		noReason bool
		expected string
	}{
		{name: "direct refresh", expected: "direct"},
		{name: "capture refresh without explicit reason", noReason: true, expected: "direct"},
		{name: "queued stale refresh", origin: PullRequestSyncOriginQueued, expected: "queued"},
		{name: "sweep with same stale reason", sweep: true, expected: "sweep"},
		{name: "sweep scope takes precedence", origin: PullRequestSyncOriginQueued, sweep: true, expected: "sweep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := telemetry.WithInstallationRequestMetadata(context.Background(), 42, "pr_health")
			ctx = WithPullRequestSyncOrigin(ctx, tt.origin)
			if !tt.noReason {
				ctx = WithPullRequestSyncReason(ctx, PullRequestSyncReasonStaleReconcile)
			}
			if tt.sweep {
				ctx = withReconciliationScope(ctx)
			}
			metadata := (&PRService{}).githubRequestMetadataForToken(ctx, "fixture")
			require.Equal(t, tt.expected, metadata.HealthOrigin, "actual read origin should distinguish budgeted sweeps from queued refreshes")
			expectedReason := "stale_reconcile"
			if tt.noReason {
				expectedReason = ""
			}
			require.Equal(t, expectedReason, metadata.SyncReason, "origin must not invent an operator trigger for unlabeled capture reads")
		})
	}
}
