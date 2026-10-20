package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
			pool, orgID, prID := newReopenPostgres(t)
			ctx := context.Background()
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

func TestReopenIfUnchangedPreservesMidStatementClosePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.PullRequestStatus
	}{
		{name: "close commits while repair waits on PR lock", status: models.PullRequestStatusClosed},
		{name: "merge commits while repair waits on PR lock", status: models.PullRequestStatusMerged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newReopenPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			observedAt := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
			accepted, err := NewCodeReviewInsightStore(pool).recordPullRequestLifecycle(ctx, orgID, prID, false, nil, true, observedAt)
			require.NoError(t, err, "seed the missed closed lifecycle before resolving the mirror")
			require.True(t, accepted, "fixture must begin with a closed mirror")
			var expectedUpdatedAt time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT updated_at FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&expectedUpdatedAt), "capture the local mirror watermark before the GitHub read")
			terminal, err := pool.Begin(ctx)
			require.NoError(t, err, "start the competing lifecycle transaction")
			defer func() { _ = terminal.Rollback(ctx) }()
			_, err = terminal.Exec(ctx, `SELECT id FROM pull_requests WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, prID)
			require.NoError(t, err, "hold the PR row while repair reads the old committed snapshot")
			contender, err := pool.Acquire(ctx)
			require.NoError(t, err, "use an independent repair connection")
			defer contender.Release()
			pid := contender.Conn().PgConn().PID()
			type result struct {
				accepted bool
				err      error
			}
			done := make(chan result, 1)
			go func() {
				accepted, err := NewPullRequestStore(contender).ReopenIfUnchanged(ctx, orgID, prID, expectedUpdatedAt, observedAt.Add(time.Minute))
				done <- result{accepted: accepted, err: err}
			}()
			// Poll only database lock state: the regression must prove the repair
			// reached its outer UPDATE before the competing lifecycle commits.
			require.Eventually(t, func() bool {
				var blocked bool
				probeErr := pool.QueryRow(ctx, `SELECT wait_event_type = 'Lock' AND query LIKE '%WITH lifecycle AS%' FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&blocked)
				return probeErr == nil && blocked
			}, 5*time.Second, time.Millisecond, "repair must block on the PR row after reading its pre-close snapshot")
			require.NoError(t, NewPullRequestStore(terminal).UpdateStatus(ctx, orgID, prID, tt.status), "commit a new terminal state while repair is mid-statement")
			require.NoError(t, terminal.Commit(ctx), "release the PR lock with the competing terminal state committed")
			select {
			case actual := <-done:
				require.NoError(t, actual.err, "raced repair should return a clean superseded result")
				require.False(t, actual.accepted, "PostgreSQL must recheck the updated_at fence after its row-lock wait")
			case <-ctx.Done():
				t.Fatal("repair did not resume after the competing terminal transaction committed")
			}
			var actualStatus models.PullRequestStatus
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&actualStatus), "read the terminal winner after both transactions finish")
			require.Equal(t, tt.status, actualStatus, "mid-statement repair cannot reopen the closed or merged PR")
			var actualObservedAt time.Time
			var isTerminal bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT observed_at,terminal FROM code_review_pull_request_lifecycle_observations WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&actualObservedAt, &isTerminal), "read the lifecycle observation after the repair rolls back")
			require.Equal(t, observedAt, actualObservedAt.UTC(), "the repair's provisional lifecycle upsert must roll back when the outer CAS loses")
			require.True(t, isTerminal, "provisional open state must not leak from the rejected repair")
		})
	}
}

func newReopenPostgres(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool, orgID, prID := newHealthSyncPostgres(t)
	_, err := pool.Exec(context.Background(), `ALTER TABLE pull_requests ADD COLUMN merged_at timestamptz, ADD UNIQUE (org_id,id);
		ALTER TABLE code_review_session_metadata ADD COLUMN decision text`)
	require.NoError(t, err, "add lifecycle dependencies to isolated health fixture")
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000283_code_review_decision_outcomes.up.sql"))
	require.NoError(t, err, "read the real lifecycle migration")
	const table = "CREATE TABLE code_review_pull_request_lifecycle_observations"
	_, after, found := strings.Cut(string(migration), table)
	require.True(t, found, "migration must define the lifecycle observation table")
	definition, _, found := strings.Cut(after, ";")
	require.True(t, found, "lifecycle table definition must terminate")
	_, err = pool.Exec(context.Background(), table+definition)
	require.NoError(t, err, "apply the actual lifecycle table definition and composite tenant foreign key")
	return pool, orgID, prID
}
