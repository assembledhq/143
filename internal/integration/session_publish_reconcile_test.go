//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// seedPublishActionJob inserts a backing publish-action job for a session via
// the real JobStore, then forces its status / attempts / age so a test can
// reproduce a specific worker-death scenario. Returns the job ID.
func seedPublishActionJob(t *testing.T, pool *pgxpool.Pool, orgID, sessionID uuid.UUID, jobType, status string, attempts, maxAttempts int, ageBack time.Duration) uuid.UUID {
	t.Helper()
	return seedPublishActionJobWithLease(t, pool, orgID, sessionID, jobType, status, attempts, maxAttempts, ageBack, nil)
}

// seedPublishActionJobWithLease additionally sets lease_expires_at relative to
// now (positive = unexpired/live lease, negative = expired), or leaves it NULL
// when leaseFromNow is nil.
func seedPublishActionJobWithLease(t *testing.T, pool *pgxpool.Pool, orgID, sessionID uuid.UUID, jobType, status string, attempts, maxAttempts int, ageBack time.Duration, leaseFromNow *time.Duration) uuid.UUID {
	t.Helper()
	jobs := db.NewJobStore(pool)
	payload := map[string]string{"session_id": sessionID.String(), "org_id": orgID.String()}
	jobID, err := jobs.Enqueue(context.Background(), orgID, "agent", jobType, payload, 5, nil)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`UPDATE jobs SET status = $2, attempts = $3, max_attempts = $4, created_at = now() - $5::interval WHERE id = $1`,
		jobID, status, attempts, maxAttempts, ageBack.String())
	require.NoError(t, err)
	if leaseFromNow != nil {
		_, err = pool.Exec(context.Background(),
			`UPDATE jobs SET lease_expires_at = now() + $2::interval WHERE id = $1`,
			jobID, leaseFromNow.String())
		require.NoError(t, err)
	}
	return jobID
}

func setPublishColumn(t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID, column, state string) {
	t.Helper()
	// #nosec G202 -- column is a test-controlled literal, not user input.
	_, err := pool.Exec(context.Background(),
		`UPDATE sessions SET `+column+` = $2 WHERE id = $1`, sessionID, state)
	require.NoError(t, err)
}

func publishColumn(t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID, column string) (string, string) {
	t.Helper()
	var state, errMsg string
	errCol := map[string]string{
		"pr_push_state":         "pr_push_error",
		"pr_creation_state":     "pr_creation_error",
		"branch_creation_state": "branch_creation_error",
	}[column]
	err := pool.QueryRow(context.Background(),
		`SELECT `+column+`, COALESCE(`+errCol+`, '') FROM sessions WHERE id = $1`, sessionID).
		Scan(&state, &errMsg)
	require.NoError(t, err)
	return state, errMsg
}

// TestIntegration_ListStuckPublishActionSessions exercises the Fix 3 backstop
// detection against a real Postgres, covering each worker-death and
// still-healthy scenario the SQL must distinguish.
func TestIntegration_ListStuckPublishActionSessions(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)
	ctx := context.Background()
	store := db.NewSessionStore(pool)

	newSession := func() uuid.UUID {
		return seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusIdle}).ID
	}

	// A: orphaned push — job kept dying so attempts climbed past max but it was
	// never dead-lettered; column wedged at 'pushing'. STUCK.
	orphanedPush := newSession()
	setPublishColumn(t, pool, orphanedPush, "pr_push_state", "pushing")
	seedPublishActionJob(t, pool, orgID, orphanedPush, "push_pr_changes", "running", 5, 3, 30*time.Minute)

	// B: legitimately running push — job still has retry budget. NOT stuck.
	runningPush := newSession()
	setPublishColumn(t, pool, runningPush, "pr_push_state", "pushing")
	seedPublishActionJob(t, pool, orgID, runningPush, "push_pr_changes", "running", 1, 3, 30*time.Minute)

	// C: fresh queued push — pending job, attempts left, just enqueued. NOT stuck.
	freshPush := newSession()
	setPublishColumn(t, pool, freshPush, "pr_push_state", "queued")
	seedPublishActionJob(t, pool, orgID, freshPush, "push_pr_changes", "pending", 0, 3, 30*time.Second)

	// D: dead-lettered create-PR with a column still wedged at 'pushing'. STUCK.
	orphanedPR := newSession()
	setPublishColumn(t, pool, orphanedPR, "pr_creation_state", "pushing")
	seedPublishActionJob(t, pool, orgID, orphanedPR, "open_pr", "dead_letter", 3, 3, 30*time.Minute)

	// E: already-terminal column. NOT stuck.
	failedPush := newSession()
	setPublishColumn(t, pool, failedPush, "pr_push_state", "failed")
	seedPublishActionJob(t, pool, orgID, failedPush, "push_pr_changes", "dead_letter", 3, 3, 30*time.Minute)

	// F: in-flight column but its backing job is too new to act on yet. NOT stuck.
	youngOrphan := newSession()
	setPublishColumn(t, pool, youngOrphan, "branch_creation_state", "queued")
	seedPublishActionJob(t, pool, orgID, youngOrphan, "create_branch", "dead_letter", 3, 3, time.Minute)

	// G: slow but healthy *final* attempt — running with attempts == max but a
	// live (unexpired) lease, so a worker is actively on it. NOT stuck.
	liveLease := 5 * time.Minute
	liveFinalAttempt := newSession()
	setPublishColumn(t, pool, liveFinalAttempt, "pr_push_state", "pushing")
	seedPublishActionJobWithLease(t, pool, orgID, liveFinalAttempt, "push_pr_changes", "running", 3, 3, 30*time.Minute, &liveLease)

	got, err := store.ListStuckPublishActionSessions(ctx, orgID, time.Now().Add(-15*time.Minute), 50)
	require.NoError(t, err)

	gotSet := map[uuid.UUID]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	require.True(t, gotSet[orphanedPush], "orphaned push should be detected")
	require.True(t, gotSet[orphanedPR], "dead-lettered create-PR should be detected")
	require.False(t, gotSet[runningPush], "healthy running push must not be detected")
	require.False(t, gotSet[freshPush], "freshly-queued push must not be detected")
	require.False(t, gotSet[failedPush], "already-terminal column must not be detected")
	require.False(t, gotSet[youngOrphan], "too-new backing job must not be detected yet")
	require.False(t, gotSet[liveFinalAttempt], "a live-leased final attempt must not be force-failed mid-run")
	require.Len(t, got, 2)
}

// TestIntegration_FailInFlightPublishActions verifies the guarded force-fail:
// it flips only in-flight columns, is idempotent, and leaves terminal columns
// untouched.
func TestIntegration_FailInFlightPublishActions(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)
	ctx := context.Background()
	store := db.NewSessionStore(pool)

	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusIdle}).ID
	setPublishColumn(t, pool, session, "pr_push_state", "pushing")

	changed, err := store.FailInFlightPublishActions(ctx, orgID, session, "boom")
	require.NoError(t, err)
	require.True(t, changed, "first reconcile should transition the wedged column")

	state, errMsg := publishColumn(t, pool, session, "pr_push_state")
	require.Equal(t, "failed", state)
	require.Equal(t, "boom", errMsg)

	// Idempotent: nothing is in flight anymore, so a second call is a no-op.
	changed, err = store.FailInFlightPublishActions(ctx, orgID, session, "boom again")
	require.NoError(t, err)
	require.False(t, changed, "second reconcile should be a no-op")
	state, errMsg = publishColumn(t, pool, session, "pr_push_state")
	require.Equal(t, "failed", state)
	require.Equal(t, "boom", errMsg, "the existing error must not be overwritten")
}

// TestIntegration_FailInFlightPublishActions_PreservesTerminalColumns ensures a
// column that legitimately advanced to a terminal state between the scan and
// the write is never clobbered.
func TestIntegration_FailInFlightPublishActions_PreservesTerminalColumns(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)
	ctx := context.Background()
	store := db.NewSessionStore(pool)

	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusIdle}).ID
	// pr_push wedged, but pr_creation already succeeded — must be left alone.
	setPublishColumn(t, pool, session, "pr_push_state", "pushing")
	setPublishColumn(t, pool, session, "pr_creation_state", "succeeded")

	changed, err := store.FailInFlightPublishActions(ctx, orgID, session, "boom")
	require.NoError(t, err)
	require.True(t, changed)

	pushState, _ := publishColumn(t, pool, session, "pr_push_state")
	require.Equal(t, "failed", pushState, "wedged push column should fail")
	prState, _ := publishColumn(t, pool, session, "pr_creation_state")
	require.Equal(t, "succeeded", prState, "terminal create-PR column must be preserved")
}
