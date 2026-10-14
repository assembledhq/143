package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewInsightStore_ProjectRecentDecisionsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		existing, terminal   bool
		stale, newCompletion bool
		newLifecycle         bool
		nullCompletion       bool
		expected             int64
	}{
		{name: "missing projection", expected: 1},
		{name: "stale nonterminal", existing: true, stale: true, expected: 1},
		{name: "current nonterminal", existing: true},
		{name: "newer completed terminal decision", existing: true, terminal: true, newCompletion: true, expected: 1},
		{name: "terminal with newer lifecycle", existing: true, terminal: true, newLifecycle: true, expected: 1},
		{name: "missing completion falls back to creation", existing: true, terminal: true, nullCompletion: true, expected: 1},
		{name: "current terminal", existing: true, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, _ := newMaintenancePostgres(t)
			ctx := context.Background()
			completed := time.Now().UTC().Add(-2 * time.Hour)
			var completedAt *time.Time
			if !tt.nullCompletion {
				completedAt = &completed
			}
			f := seedMaintenanceReview(t, pool, completedAt)
			foreign := seedMaintenanceReview(t, pool, &completed)
			store := NewCodeReviewInsightStore(pool)
			staleBefore := time.Now().UTC().Add(-time.Hour)
			if tt.existing {
				require.NoError(t, store.ProjectDecision(ctx, f.org, f.session), "seed existing real outcome projection")
				maintenanceExec(t, pool, `UPDATE code_review_decision_outcomes SET terminal=$3 WHERE org_id=$1 AND session_id=$2`, f.org, f.session, tt.terminal)
			}
			if tt.stale {
				maintenanceExec(t, pool, `UPDATE code_review_decision_outcomes SET projection_updated_at=now()-interval '4 hours' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			}
			if tt.newCompletion || tt.nullCompletion {
				maintenanceExec(t, pool, `UPDATE code_review_decision_outcomes SET projection_updated_at=now()-interval '4 days',merged=true,merged_at=now()-interval '1 day',lifecycle_observed_at=now(),observed_until=now() WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			}
			if tt.newLifecycle {
				maintenanceExec(t, pool, `INSERT INTO code_review_pull_request_lifecycle_observations(org_id,pull_request_id,merged,merged_at,terminal,observed_at) VALUES($1,$2,true,now()-interval '1 hour',true,now())`, f.org, f.pr)
			}
			projected, err := store.ProjectRecentDecisions(ctx, f.org, staleBefore, 100)
			require.NoError(t, err, "selector must parse against real metadata schema without updated_at")
			require.Equal(t, tt.expected, projected, "selector should follow the expected freshness branch")
			var decision string
			var reasons []string
			var merged, terminal bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT decision,reason_codes,merged,terminal FROM code_review_decision_outcomes WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&decision, &reasons, &merged, &terminal), "read repaired outcome")
			require.Equal(t, "approved", decision, "projection should retain exact completed decision")
			require.Equal(t, []string{"a", "z"}, reasons, "projection should sort and deduplicate nonempty reason codes")
			if tt.newCompletion || tt.nullCompletion || tt.newLifecycle {
				require.True(t, merged, "reprojection must preserve newer provider merge facts")
				require.True(t, terminal, "reprojection must preserve newer provider terminal facts")
			}
			projected, err = store.ProjectRecentDecisions(ctx, f.org, staleBefore, 100)
			require.NoError(t, err, "repeat projection should remain safe")
			require.Equal(t, int64(0), projected, "fixed cutoff should not repeatedly select current projections")
			var foreignCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_decision_outcomes WHERE org_id=$1 AND session_id=$2`, foreign.org, foreign.session).Scan(&foreignCount), "read other tenant projection count")
			require.Equal(t, 0, foreignCount, "tenant repair must not project another org's completed decision")
		})
	}
}

func TestCodeReviewInsightStore_ProjectRecentDecisionsPostgresBatch(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	ctx := context.Background()
	completed := time.Now().UTC().Add(-time.Hour)
	f := seedMaintenanceReview(t, pool, &completed)
	expected := []string{f.session.String()}
	for range 2 {
		session, metadata := uuid.New(), uuid.New()
		maintenanceExec(t, pool, `INSERT INTO sessions VALUES($1,$2)`, session, f.org)
		maintenanceExec(t, pool, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,decision,review_output_key,completed_at)
VALUES($1,$2,$3,$4,$5,$6,'base','head','app_reviewer','completed','approved',$3::uuid::text,$7)`, metadata, f.org, session, f.repository, f.pr, f.policy, completed)
		expected = append(expected, session.String())
	}
	store := NewCodeReviewInsightStore(pool)
	cutoff := time.Now().UTC().Add(-time.Minute)
	for _, tt := range []struct{ expected int64 }{{2}, {1}, {0}} {
		actual, err := store.ProjectRecentDecisions(ctx, f.org, cutoff, 2)
		require.NoError(t, err, "bounded projection batch should execute against real schema")
		require.Equal(t, tt.expected, actual, "bounded batches should progress without revisiting repaired rows")
	}
	rows, err := pool.Query(ctx, `SELECT session_id::text FROM code_review_decision_outcomes WHERE org_id=$1`, f.org)
	require.NoError(t, err, "read all bounded projections")
	defer rows.Close()
	actual := []string{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id), "scan bounded projection identity")
		actual = append(actual, id)
	}
	require.NoError(t, rows.Err(), "bounded projection identities should finish without errors")
	require.ElementsMatch(t, expected, actual, "all exact tenant session identities should be projected")
}
