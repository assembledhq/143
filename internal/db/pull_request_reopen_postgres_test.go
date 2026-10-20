package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReopenIfUnchangedPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		initial, concurrent       models.PullRequestStatus
		newerProvider, foreignOrg bool
		projectionFailure         bool
		newHead                   bool
		accepted                  bool
	}{
		{name: "missed reopen refreshes health for a changed base", accepted: true},
		{name: "missed reopen refreshes health for a changed head", newHead: true, accepted: true},
		{name: "newer provider close cannot be reopened", newerProvider: true},
		{name: "concurrent close with equal provider time wins", concurrent: models.PullRequestStatusClosed},
		{name: "concurrent merge with equal provider time wins", concurrent: models.PullRequestStatusMerged},
		{name: "merged mirror cannot be reopened", initial: models.PullRequestStatusMerged},
		{name: "foreign tenant cannot reopen the PR", foreignOrg: true},
		{name: "projection failure rolls lifecycle and mirror back", projectionFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newHealthSyncPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `ALTER TABLE pull_requests ADD COLUMN merged_at timestamptz, ADD UNIQUE (org_id,id);
				ALTER TABLE code_review_session_metadata ADD COLUMN decision text;
				CREATE TABLE code_review_pull_request_lifecycle_observations (
					org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE, pull_request_id uuid NOT NULL,
					merged boolean NOT NULL, merged_at timestamptz, terminal boolean NOT NULL,
					observed_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
					PRIMARY KEY (org_id, pull_request_id),
					FOREIGN KEY (org_id,pull_request_id) REFERENCES pull_requests(org_id,id) ON DELETE CASCADE
				)`)
			require.NoError(t, err, "add lifecycle schema to isolated health fixture")
			store := NewPullRequestStore(pool)
			version, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve original health capture")
			_, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", version))
			require.NoError(t, err, "persist original health before the missed reopen")
			require.True(t, applied, "baseline health must exist before lifecycle closes")
			observedAt := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
			priorObservedAt := observedAt.Add(-time.Minute)
			if tt.newerProvider {
				priorObservedAt = observedAt.Add(time.Minute)
			}
			status := tt.initial
			if status == "" {
				status = models.PullRequestStatusClosed
			}
			insights := NewCodeReviewInsightStore(pool)
			var mergedAt *time.Time
			if status == models.PullRequestStatusMerged {
				mergedAt = new(priorObservedAt)
			}
			accepted, err := insights.recordPullRequestLifecycle(ctx, orgID, prID, status == models.PullRequestStatusMerged, mergedAt, true, priorObservedAt)
			require.NoError(t, err, "record the prior terminal lifecycle observation")
			require.True(t, accepted, "fixture must establish its terminal mirror")
			var expectedUpdatedAt time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT updated_at FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&expectedUpdatedAt), "capture the mirror watermark before reading GitHub")
			if tt.concurrent != "" {
				if tt.concurrent == models.PullRequestStatusMerged {
					mergedAt = new(observedAt)
				}
				accepted, err = insights.recordPullRequestLifecycle(ctx, orgID, prID, tt.concurrent == models.PullRequestStatusMerged, mergedAt, true, observedAt)
				require.NoError(t, err, "commit a lifecycle webhook while provider read is in flight")
				require.True(t, accepted, "concurrent terminal event must advance the mirror")
				status = tt.concurrent
				priorObservedAt = observedAt
			}
			if tt.projectionFailure {
				_, err = pool.Exec(ctx, `ALTER TABLE code_review_session_metadata RENAME COLUMN decision TO unavailable_decision`)
				require.NoError(t, err, "inject failure after the conditional lifecycle write")
			}
			requestOrg := orgID
			if tt.foreignOrg {
				requestOrg = uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO organizations(id) VALUES($1)`, requestOrg)
				require.NoError(t, err, "establish a distinct tenant for the ownership regression")
			}
			accepted, err = store.ReopenIfUnchanged(ctx, requestOrg, prID, expectedUpdatedAt, observedAt)
			if tt.projectionFailure {
				require.ErrorContains(t, err, "list pull request decisions", "projection failure must abort the lifecycle repair")
			} else {
				require.NoError(t, err, "conditional reopen must return an explicit applied or superseded result")
			}
			require.Equal(t, tt.accepted, accepted, "only an unchanged closed mirror with current provider evidence may reopen")
			var actualStatus models.PullRequestStatus
			var actualMergedAt *time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,merged_at FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&actualStatus, &actualMergedAt), "read the committed PR lifecycle")
			if actualMergedAt != nil {
				actualMergedAt = new(actualMergedAt.UTC())
			}
			require.Equal(t, mergedAt, actualMergedAt, "rejected repair must preserve the provider merge timestamp")
			if tt.accepted {
				status = models.PullRequestStatusOpen
				priorObservedAt = observedAt
			}
			require.Equal(t, status, actualStatus, "skipped or failed repair must preserve terminal state")
			var actualObservedAt time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT observed_at FROM code_review_pull_request_lifecycle_observations WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&actualObservedAt), "read committed provider ordering after repair")
			require.Equal(t, priorObservedAt, actualObservedAt.UTC(), "rejected or aborted repair must not advance the lifecycle watermark")
			if tt.foreignOrg {
				var observations int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_pull_request_lifecycle_observations WHERE org_id=$1`, requestOrg).Scan(&observations), "inspect foreign-tenant writes after rejected repair")
				require.Equal(t, 0, observations, "the CTE observation must roll back when tenant-scoped mirror update is rejected")
			}
			if !tt.accepted {
				return
			}
			version, err = store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve fresh health for the repaired open mirror")
			head := "old-head"
			if tt.newHead {
				head = "new-head"
			}
			current, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, head, "new-base", version))
			require.NoError(t, err, "refresh GitHub health after repairing the missed reopen")
			require.True(t, applied, "open mirror must no longer reject the authoritative health write")
			require.Equal(t, []string{head, "new-base"}, []string{current.HeadSHA, current.BaseSHA}, "health must cover the exact current revision, including base-only movement")
		})
	}
}
