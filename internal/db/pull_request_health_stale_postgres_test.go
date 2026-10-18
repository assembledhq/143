package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The background sweep passes now minus its configured stale threshold as the
// cutoff. Selection must honor that cutoff for synced rows while keeping
// never-synced open rows eligible at any threshold.
func TestListOpenStaleForHealthSyncHonorsCutoffPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		staleAfter time.Duration
		want       []string
	}{
		{name: "default threshold", staleAfter: 10 * time.Minute, want: []string{"never synced", "synced 4h ago", "synced 2h ago", "synced 30m ago"}},
		{name: "wider threshold", staleAfter: 3 * time.Hour, want: []string{"never synced", "synced 4h ago"}},
		{name: "widest threshold", staleAfter: 48 * time.Hour, want: []string{"never synced"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, otherOrgID := uuid.New(), uuid.New()
			for _, id := range []uuid.UUID{orgID, otherOrgID} {
				_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, id, "stale-sweep-"+id.String())
				require.NoError(t, err, "seed isolated tenant")
			}
			cutoff := now.Add(-tt.staleAfter)
			syncedAgo := func(age time.Duration) *time.Time {
				syncedAt := now.Add(-age)
				return &syncedAt
			}
			fixtures := []struct {
				label    string
				orgID    uuid.UUID
				status   string
				syncedAt *time.Time
			}{
				{label: "never synced", orgID: orgID, status: "open"},
				{label: "synced 4h ago", orgID: orgID, status: "open", syncedAt: syncedAgo(4 * time.Hour)},
				{label: "synced 2h ago", orgID: orgID, status: "open", syncedAt: syncedAgo(2 * time.Hour)},
				{label: "synced 30m ago", orgID: orgID, status: "open", syncedAt: syncedAgo(30 * time.Minute)},
				{label: "synced exactly at cutoff", orgID: orgID, status: "open", syncedAt: &cutoff},
				{label: "synced 5m ago", orgID: orgID, status: "open", syncedAt: syncedAgo(5 * time.Minute)},
				{label: "closed and never synced", orgID: orgID, status: "closed"},
				{label: "other tenant never synced", orgID: otherOrgID, status: "open"},
			}
			labels := make(map[uuid.UUID]string, len(fixtures))
			for i, fixture := range fixtures {
				id := uuid.New()
				labels[id] = fixture.label
				_, err := pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,status,github_state_synced_at)
					VALUES($1,$2,$3,$4,'example/app',$5,$6,$7)`,
					id, fixture.orgID, i+1, fmt.Sprintf("https://example.test/pull/%d", i+1), fixture.label, fixture.status, fixture.syncedAt)
				require.NoError(t, err, "seed pull request with synthetic sync age")
			}

			prs, err := NewPullRequestStore(pool).ListOpenStaleForHealthSync(ctx, orgID, cutoff, 200)
			require.NoError(t, err, "stale health selection should succeed")
			got := make([]string, 0, len(prs))
			for _, pr := range prs {
				got = append(got, labels[pr.ID])
			}
			require.Equal(t, tt.want, got, "selection should return never-synced rows first, then open rows synced strictly before the cutoff, for this tenant only")
		})
	}
}
