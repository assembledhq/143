package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestAutomationRunStore_CallbackWaitsForSuccessorPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target models.AutomationRunStatus
	}{
		{name: "stale failure", target: models.AutomationRunStatusFailed},
		{name: "stale completion", target: models.AutomationRunStatusCompleted},
		{name: "stale promotion", target: models.AutomationRunStatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, applicationName := newAutomationCallbackPostgresPool(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			store := NewAutomationRunStore(pool)
			orgID, runID, predecessorID, successorID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			createdAt := time.Now().UTC().Add(-time.Minute)
			_, err := pool.Exec(ctx, `INSERT INTO automation_runs (id, org_id, status) VALUES ($1, $2, 'pending')`, runID, orgID)
			require.NoError(t, err, "seed the logical run between fallback attempts")
			_, err = pool.Exec(ctx, `INSERT INTO sessions (id, org_id, created_at) VALUES ($1, $2, $3)`, predecessorID, orgID, createdAt)
			require.NoError(t, err, "seed the predecessor session")
			_, err = pool.Exec(ctx, `INSERT INTO session_automation_links (org_id, automation_run_id, session_id) VALUES ($1, $2, $3)`, orgID, runID, predecessorID)
			require.NoError(t, err, "link the predecessor to its logical run")

			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin atomic successor dispatch")
			defer func() { _ = tx.Rollback(ctx) }()
			claimed, err := store.ClaimPendingForPerRunInTx(ctx, tx, orgID, runID, predecessorID)
			require.NoError(t, err, "claim the successor while holding the logical run lock")
			require.True(t, claimed, "the successor should win the pending run claim")

			type transitionResult struct {
				changed bool
				err     error
			}
			result := make(chan transitionResult, 1)
			go func() {
				changed, transitionErr := store.TransitionStatusForSession(ctx, orgID, runID, predecessorID, models.AutomationRunStatusRunning, tt.target, nil, nil)
				result <- transitionResult{changed: changed, err: transitionErr}
			}()
			require.Eventually(t, func() bool {
				var blocked bool
				queryErr := pool.QueryRow(ctx, `SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity
					WHERE application_name = $1 AND wait_event_type = 'Lock'
				)`, applicationName).Scan(&blocked)
				return queryErr == nil && blocked
			}, 5*time.Second, 10*time.Millisecond, "the stale callback must wait for the successor's uncommitted run claim")

			_, err = tx.Exec(ctx, `INSERT INTO sessions (id, org_id, created_at) VALUES ($1, $2, $3)`, successorID, orgID, createdAt.Add(time.Second))
			require.NoError(t, err, "insert the successor while the callback is blocked")
			_, err = tx.Exec(ctx, `INSERT INTO session_automation_links (org_id, automation_run_id, session_id) VALUES ($1, $2, $3)`, orgID, runID, successorID)
			require.NoError(t, err, "link the successor in the same transaction as its claim")
			require.NoError(t, tx.Commit(ctx), "publish the running successor and its ownership together")

			select {
			case actual := <-result:
				require.NoError(t, actual.err, "the superseded callback should finish without an error")
				require.False(t, actual.changed, "the callback must read the successor committed after it began waiting")
			case <-ctx.Done():
				t.Fatal("the superseded callback did not finish after the successor committed")
			}
			var status models.AutomationRunStatus
			err = pool.QueryRow(ctx, `SELECT status FROM automation_runs WHERE id = $1 AND org_id = $2`, runID, orgID).Scan(&status)
			require.NoError(t, err, "read the logical run after the stale callback")
			require.Equal(t, models.AutomationRunStatusRunning, status, "the successor must retain the running logical run")

			changed, err := store.TransitionStatusForSession(ctx, orgID, runID, successorID, models.AutomationRunStatusRunning, tt.target, nil, nil)
			require.NoError(t, err, "the current successor should be allowed to transition its run")
			require.True(t, changed, "ownership fencing must still accept the current session")
			err = pool.QueryRow(ctx, `SELECT status FROM automation_runs WHERE id = $1 AND org_id = $2`, runID, orgID).Scan(&status)
			require.NoError(t, err, "read the successor's accepted transition")
			require.Equal(t, tt.target, status, "the run should record exactly the current successor's requested status")
		})
	}
}

func TestAutomationRunStore_OwnershipFencesTenantPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status models.AutomationRunStatus
		claim  bool
	}{
		{name: "completion cannot lock another tenant's run", status: models.AutomationRunStatusRunning},
		{name: "dispatch cannot claim another tenant's run", status: models.AutomationRunStatusPending, claim: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, _ := newAutomationCallbackPostgresPool(t)
			ctx := t.Context()
			store := NewAutomationRunStore(pool)
			orgID, otherOrgID, runID := uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO automation_runs (id, org_id, status) VALUES ($1, $2, $3)`, runID, orgID, tt.status)
			require.NoError(t, err, "seed a run owned by a different tenant from the caller")
			var changed bool
			if tt.claim {
				tx, beginErr := pool.Begin(ctx)
				require.NoError(t, beginErr, "begin the cross-tenant dispatch attempt")
				defer func() { _ = tx.Rollback(ctx) }()
				changed, err = store.ClaimPendingForPerRunInTx(ctx, tx, otherOrgID, runID, uuid.Nil)
			} else {
				changed, err = store.TransitionStatusForSession(ctx, otherOrgID, runID, uuid.New(), tt.status, models.AutomationRunStatusFailed, nil, nil)
			}
			require.ErrorIs(t, err, pgx.ErrNoRows, "a tenant-scoped ownership operation must not find another tenant's run")
			require.False(t, changed, "a cross-tenant ownership operation must not report a state change")
			var actual models.AutomationRunStatus
			err = pool.QueryRow(ctx, `SELECT status FROM automation_runs WHERE id = $1 AND org_id = $2`, runID, orgID).Scan(&actual)
			require.NoError(t, err, "read the run through its actual tenant")
			require.Equal(t, tt.status, actual, "another tenant's request must leave the run's exact state unchanged")
		})
	}
}

func newAutomationCallbackPostgresPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL callback ownership concurrency tests")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL for callback ownership tests")
	schema := "automation_callback_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create an isolated schema for each parallel callback race")
	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse callback ownership pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "create the schema-scoped callback ownership pool")
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "remove only this test's isolated callback schema")
		admin.Close()
	})
	_, err = pool.Exec(ctx, `CREATE TABLE automation_runs (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, status text NOT NULL,
		dispatch_state text, wait_reason text, wait_started_at timestamptz,
		completed_at timestamptz, result_summary text, updated_at timestamptz
	);
	CREATE TABLE sessions (
		id uuid PRIMARY KEY, org_id uuid NOT NULL,
		created_at timestamptz NOT NULL, deleted_at timestamptz
	);
	CREATE TABLE session_automation_links (
		org_id uuid NOT NULL, automation_run_id uuid NOT NULL, session_id uuid NOT NULL
	)`)
	require.NoError(t, err, "create the minimal durable run and session ownership tables")
	return pool, schema
}
