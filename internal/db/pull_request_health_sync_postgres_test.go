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

func newHealthSyncPostgres(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool, orgID, _, prID := newSchedulingPostgres(t)
	_, err := pool.Exec(context.Background(), `ALTER TABLE pull_requests ADD COLUMN status text NOT NULL DEFAULT 'open', ADD COLUMN ci_status text NOT NULL DEFAULT 'pending', ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(), ADD COLUMN head_ref text, ADD COLUMN body text`)
	require.NoError(t, err, "add ordinary PR fields to isolated scheduling fixture")
	var migrations strings.Builder
	for _, filename := range []string{"000094_pull_request_health.up.sql", "000194_pull_request_repair_head_identity.up.sql", "000257_pull_request_check_states.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", filename))
		require.NoError(t, err, "read actual health and projection schema")
		migrations.Write(migration)
		migrations.WriteByte('\n')
	}
	_, err = pool.Exec(context.Background(), migrations.String())
	require.NoError(t, err, "apply actual health and projection schema to owned test database")
	_, err = pool.Exec(context.Background(), `UPDATE pull_requests SET head_sha='old-head',base_sha='old-base' WHERE org_id=$1 AND id=$2`, orgID, prID)
	require.NoError(t, err, "establish original revision identity")
	return pool, orgID, prID
}

func healthSyncSnapshot(orgID, prID uuid.UUID, head, base string, version int64) PullRequestHealthSyncSnapshot {
	return PullRequestHealthSyncSnapshot{
		ExpectedHeadSHA: "old-head", ExpectedBaseSHA: "old-base", HeadSHA: head, BaseSHA: base, CheckStateVersion: version,
		CIStatus: models.PullRequestCIStatusSuccess,
		Summary:  models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean, ChecksConfirmed: true, Checks: []models.PullRequestCheckSummary{{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed, Provider: "test-provider"}}},
		Checks:   []models.PullRequestCheckState{{OrgID: orgID, PullRequestID: prID, HeadSHA: head, Source: models.PullRequestCheckSourceCheckRun, ExternalKey: "unit tests", Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed, Provider: "test-provider", ProviderSequence: version, ProviderUpdatedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)}},
	}
}

func TestApplyHealthSyncSlowReadCannotRegressNewHeadPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, prID := newHealthSyncPostgres(t)
	ctx := context.Background()
	store := NewPullRequestStore(pool)
	oldVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve older read before its delayed response")
	oldSnapshot := healthSyncSnapshot(orgID, prID, "old-head", "old-base", oldVersion)
	finish := make(chan struct{})
	type result struct {
		current models.PullRequestHealthCurrent
		applied bool
		err     error
	}
	oldDone := make(chan result, 1)
	go func() {
		<-finish
		current, applied, err := store.ApplyHealthSync(ctx, orgID, prID, oldSnapshot)
		oldDone <- result{current, applied, err}
	}()
	newVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve newer head-change read independently")
	newCurrent, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "new-head", "new-base", newVersion))
	require.NoError(t, err, "persist newer head and projection atomically")
	require.True(t, applied, "new read must establish current health")
	newCurrent, err = store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read canonical committed newer baseline")
	close(finish)
	old := <-oldDone
	require.NoError(t, old.err, "older completion must skip without retry failure")
	require.False(t, old.applied, "older completion must not overwrite a newer read")
	require.Equal(t, newCurrent, old.current, "older completion must return newer baseline intact")
	current, err := store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read durable health after both completions")
	require.Equal(t, newCurrent, current, "slow read must not regress summary, head, base, or reserved version")
	var head, base, ci, checkHead string
	var checkVersion, snapshots int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,base_sha,ci_status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&head, &base, &ci), "read hot PR state after delayed writer")
	require.Equal(t, []string{"new-head", "new-base", "success"}, []string{head, base, ci}, "older CI and identity values cannot escape the write guard")
	require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&checkHead, &checkVersion), "read newer check projection after delayed writer")
	require.Equal(t, "new-head", checkHead, "old completion must not delete newer-head projection")
	require.Equal(t, newVersion, checkVersion, "new projection ordering must survive old completion")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pull_request_health_snapshots WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&snapshots), "count committed health history")
	require.Equal(t, int64(1), snapshots, "skipped completion must not fabricate a health version")
}

func TestApplyHealthSyncPartialWebhookAndRollbackPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                               string
		headWebhook, baseWebhook, rejectCI bool
	}{
		{name: "partial head webhook wins", headWebhook: true},
		{name: "partial base webhook wins", baseWebhook: true},
		{name: "CI failure rolls all writes back", rejectCI: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newHealthSyncPostgres(t)
			ctx := context.Background()
			store := NewPullRequestStore(pool)
			version, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve baseline read")
			baseline, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", version))
			require.NoError(t, err, "persist original health baseline")
			require.True(t, applied, "baseline must be committed")
			baseline, err = store.GetHealthCurrent(ctx, orgID, prID)
			require.NoError(t, err, "read canonical committed original baseline")
			version, err = store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve delayed candidate")
			candidate := healthSyncSnapshot(orgID, prID, "old-head", "old-base", version)
			if tt.headWebhook {
				require.NoError(t, store.UpdateHeadSHA(ctx, orgID, prID, "webhook-head"), "simulate webhook invalidating only the hot PR head")
			}
			if tt.baseWebhook {
				require.NoError(t, store.UpdateGitHubSnapshot(ctx, orgID, prID, PullRequestGitHubSnapshot{GitHubPRURL: "https://github.com/example/repository/pull/1", Title: "Change", HeadSHA: new("old-head"), BaseSHA: new("webhook-base")}), "simulate webhook invalidating only the hot PR base")
			}
			if tt.rejectCI {
				_, err = pool.Exec(ctx, `ALTER TABLE pull_requests ADD CONSTRAINT test_ci_accept CHECK (ci_status <> 'failure')`)
				require.NoError(t, err, "inject a CI persistence failure after projection and summary savepoints")
				candidate.CIStatus = models.PullRequestCIStatusFailure
				candidate.Summary.NeedsAgentAction = true
			}
			_, applied, err = store.ApplyHealthSync(ctx, orgID, prID, candidate)
			if tt.rejectCI {
				require.Error(t, err, "CI failure must abort the outer atomic write")
			} else {
				require.NoError(t, err, "partial-row invalidation must produce a skipped result")
			}
			require.False(t, applied, "failed or invalidated candidate cannot change current health")
			current, err := store.GetHealthCurrent(ctx, orgID, prID)
			require.NoError(t, err, "read current health after skipped or aborted write")
			require.Equal(t, baseline, current, "guard or rollback must preserve full health baseline")
			var head string
			var projectionVersion int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&head, &projectionVersion), "read projection after skipped or aborted write")
			require.Equal(t, "old-head", head, "candidate must not delete the prior projection")
			require.Equal(t, baseline.CheckStateVersion, projectionVersion, "candidate must not leak a newer projection version through a savepoint")
			var ci string
			require.NoError(t, pool.QueryRow(ctx, `SELECT ci_status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&ci), "read CI after aborted or skipped update")
			require.Equal(t, "success", ci, "CI must remain consistent with the preserved baseline")
		})
	}
}
