//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// seedAutomation inserts a per-target review automation through the real
// store so the row satisfies every automations CHECK constraint.
func seedAutomation(t *testing.T, pool *pgxpool.Pool, orgID, repoID uuid.UUID) models.Automation {
	t.Helper()
	a := models.Automation{
		OrgID:               orgID,
		RepositoryID:        &repoID,
		Name:                "front-end review",
		Goal:                "review against the design principles",
		ExecutionMode:       models.AutomationExecutionModeSequential,
		MaxConcurrent:       1,
		BaseBranch:          "main",
		PublishPolicy:       models.AutomationPublishPolicyNone,
		SessionContinuity:   models.AutomationSessionContinuityPerTarget,
		ScheduleType:        models.AutomationScheduleNone,
		Timezone:            "UTC",
		GitHubEventTriggers: []models.AutomationGitHubEvent{models.AutomationGitHubEventPullRequestUpdated},
		Enabled:             true,
		Priority:            50,
	}
	require.NoError(t, db.NewAutomationStore(pool).Create(context.Background(), &a), "seed automation")
	return a
}

// seedRun inserts a pending automation run for the automation.
func seedRun(t *testing.T, pool *pgxpool.Pool, a models.Automation) uuid.UUID {
	t.Helper()
	run := models.AutomationRun{
		AutomationID: a.ID,
		OrgID:        a.OrgID,
		TriggeredBy:  models.AutomationTriggeredByGitHub,
		GoalSnapshot: a.Goal,
		Status:       models.AutomationRunStatusPending,
	}
	created, err := db.NewAutomationRunStore(pool).CreateRun(context.Background(), &run)
	require.NoError(t, err, "seed run")
	require.True(t, created, "seed run should insert")
	return run.ID
}

// seedRunningJob inserts a jobs row in the running state under lockToken, the
// shape the attempt fences compare against.
func seedRunningJob(t *testing.T, pool *pgxpool.Pool, orgID, lockToken uuid.UUID) uuid.UUID {
	t.Helper()
	var jobID uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO jobs (org_id, queue, job_type, payload, status, lock_token, lease_expires_at)
		VALUES ($1, 'default', 'continue_session', '{}', 'running', $2, now() + interval '5 minutes')
		RETURNING id`, orgID, lockToken).Scan(&jobID)
	require.NoError(t, err, "seed running job")
	return jobID
}

func sessionOwnerMarker(t *testing.T, pool *pgxpool.Pool, orgID, sessionID uuid.UUID) *uuid.UUID {
	t.Helper()
	var marker *uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT automation_owner_generation_id FROM sessions WHERE id = $1 AND org_id = $2`, sessionID, orgID).Scan(&marker),
		"read session owner marker")
	return marker
}

func isConstraintViolation(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

const (
	pgUniqueViolation = "23505"
	pgCheckViolation  = "23514"
)

// TestAutomationTargets_GenerationLifecycle proves the migration 000290
// invariants against a real Postgres: one active generation per target,
// the executing CHECK, one executing run per target, and the two
// ownership-release paths of retirement.
func TestAutomationTargets_GenerationLifecycle(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "acme/web")
	automation := seedAutomation(t, pool, orgID, repoID)
	first := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	second := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	store := db.NewAutomationTargetStore(pool)

	// Create the target and its first generation.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin ownership transaction")
	target, err := store.LockOrCreate(ctx, tx, orgID, automation.ID, repoID, models.AutomationTargetKindGitHubPullRequest, "1234")
	require.NoError(t, err, "first lock-or-create should insert the target")
	require.Equal(t, 0, target.ActiveGeneration, "a new target has no generation")
	gen1, err := store.InsertGeneration(ctx, tx, orgID, target.ID, first.ID)
	require.NoError(t, err, "first generation should insert")
	require.Equal(t, 1, gen1.Generation, "first generation is numbered 1")
	require.NoError(t, tx.Commit(ctx), "commit ownership transaction")
	require.Equal(t, &gen1.ID, sessionOwnerMarker(t, pool, orgID, first.ID), "session should be marked automation-owned")

	// A second lock-or-create returns the same row.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err, "begin second lock")
	again, err := store.LockOrCreate(ctx, tx, orgID, automation.ID, repoID, models.AutomationTargetKindGitHubPullRequest, "1234")
	require.NoError(t, err, "lock-or-create should find the existing target")
	require.Equal(t, target.ID, again.ID, "same identity should map to the same target row")
	require.Equal(t, 1, again.ActiveGeneration, "active generation should have advanced")

	// The partial unique index rejects a second active generation.
	_, err = store.InsertGeneration(ctx, tx, orgID, target.ID, second.ID)
	require.True(t, isConstraintViolation(err, pgUniqueViolation), "second active generation should violate the active-generation index, got %v", err)
	require.NoError(t, tx.Rollback(ctx), "rollback rejected insert")

	// The executing CHECK requires session, job, and start time together.
	runID := seedRun(t, pool, automation)
	_, err = pool.Exec(ctx, `
		UPDATE automation_runs SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2
		WHERE id = $3 AND org_id = $4`, target.ID, first.ID, runID, orgID)
	require.True(t, isConstraintViolation(err, pgCheckViolation), "executing without job_id should violate the executing CHECK, got %v", err)

	lockToken := uuid.New()
	jobID := seedRunningJob(t, pool, orgID, lockToken)
	_, err = pool.Exec(ctx, `
		UPDATE automation_runs
		SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2,
			job_id = $3, execution_started_at = now(), attempt = 1, attempt_lock_token = $5
		WHERE id = $4 AND org_id = $6`, target.ID, first.ID, jobID, runID, lockToken, orgID)
	require.NoError(t, err, "a complete reservation should satisfy the executing CHECK")

	// One executing run per target across generations.
	otherRunID := seedRun(t, pool, automation)
	_, err = pool.Exec(ctx, `
		UPDATE automation_runs
		SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2,
			job_id = $3, execution_started_at = now()
		WHERE id = $4 AND org_id = $5`, target.ID, first.ID, jobID, otherRunID, orgID)
	require.True(t, isConstraintViolation(err, pgUniqueViolation), "second executing run should violate the one-executing-per-target index, got %v", err)

	// Retiring while a run executes defers the ownership release.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err, "begin retirement")
	retired, err := store.RetireGeneration(ctx, tx, orgID, gen1.ID, models.AutomationTargetRetiredManualReset)
	require.NoError(t, err, "retirement during execution should succeed")
	require.NoError(t, tx.Commit(ctx), "commit retirement")
	require.True(t, retired.OwnershipReleasePending, "an executing generation must keep the owner marker until completion")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, retired.Status, "generation should be retired")
	require.Equal(t, &gen1.ID, sessionOwnerMarker(t, pool, orgID, first.ID), "owner marker should remain while the turn executes")
	wokeTarget, err := store.GetByID(ctx, orgID, target.ID)
	require.NoError(t, err, "reload target")
	require.NotNil(t, wokeTarget.WakeRequestedAt, "retirement should write the wake outbox")

	// Retiring twice is rejected.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err, "begin second retirement")
	_, err = store.RetireGeneration(ctx, tx, orgID, gen1.ID, models.AutomationTargetRetiredManualReset)
	require.ErrorIs(t, err, db.ErrAutomationTargetGenerationNotActive, "a retired generation cannot be retired again")
	require.NoError(t, tx.Rollback(ctx), "rollback second retirement")

	// The turn finishes; a new generation on an idle session releases now.
	_, err = pool.Exec(ctx, `UPDATE automation_runs SET dispatch_state = 'done' WHERE id = $1 AND org_id = $2`, runID, orgID)
	require.NoError(t, err, "finish the executing run")
	tx, err = pool.Begin(ctx)
	require.NoError(t, err, "begin second generation")
	gen2, err := store.InsertGeneration(ctx, tx, orgID, target.ID, second.ID)
	require.NoError(t, err, "second generation should insert once the first is retired")
	require.Equal(t, 2, gen2.Generation, "generations increment")
	require.NoError(t, tx.Commit(ctx), "commit second generation")

	active, err := store.GetActiveGeneration(ctx, nil, orgID, target.ID)
	require.NoError(t, err, "active generation lookup")
	require.Equal(t, gen2.ID, active.ID, "the new generation should be active")

	tx, err = pool.Begin(ctx)
	require.NoError(t, err, "begin idle retirement")
	retired2, err := store.RetireGeneration(ctx, tx, orgID, gen2.ID, models.AutomationTargetRetiredContinuityDisabled)
	require.NoError(t, err, "idle retirement should succeed")
	require.NoError(t, tx.Commit(ctx), "commit idle retirement")
	require.False(t, retired2.OwnershipReleasePending, "an idle generation releases ownership immediately")
	require.Nil(t, sessionOwnerMarker(t, pool, orgID, second.ID), "owner marker should be cleared for an idle session")

	_, err = store.GetActiveGeneration(ctx, nil, orgID, target.ID)
	require.ErrorIs(t, err, db.ErrAutomationTargetGenerationNotFound, "no generation should be active after retirement")

	// The retirement CHECK rejects an inconsistent row.
	_, err = pool.Exec(ctx, `UPDATE automation_target_sessions SET retired_at = NULL WHERE id = $1 AND org_id = $2`, gen2.ID, orgID)
	require.True(t, isConstraintViolation(err, pgCheckViolation), "retired row without retired_at should violate the retirement CHECK, got %v", err)
}

// TestAutomationTargets_CrossOrgParentsRejected proves the same-org
// validation in LockOrCreate: a repository from another org never produces
// a target row even though the UUID foreign key alone would allow it.
func TestAutomationTargets_CrossOrgParentsRejected(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	otherOrgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "acme/web")
	foreignRepoID := seedRepository(t, pool, otherOrgID, "other/web")
	automation := seedAutomation(t, pool, orgID, repoID)
	store := db.NewAutomationTargetStore(pool)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin")
	defer tx.Rollback(ctx) //nolint:errcheck // rollback of a rolled-back tx is a no-op
	_, err = store.LockOrCreate(ctx, tx, orgID, automation.ID, foreignRepoID, models.AutomationTargetKindGitHubPullRequest, "1")
	require.ErrorIs(t, err, db.ErrAutomationTargetNotFound, "a repository outside the org must not create a target")
	_, err = store.LockOrCreate(ctx, tx, otherOrgID, automation.ID, repoID, models.AutomationTargetKindGitHubPullRequest, "1")
	require.ErrorIs(t, err, db.ErrAutomationTargetNotFound, "an automation outside the caller's org must not create a target")
}

// TestAutomationRunResults_Fences proves the double fence on the result
// marker: the run row must be executing under the attempt token for the job,
// and the jobs row must still be running under the same token.
func TestAutomationRunResults_Fences(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "acme/web")
	automation := seedAutomation(t, pool, orgID, repoID)
	session := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	targetStore := db.NewAutomationTargetStore(pool)
	resultStore := db.NewAutomationRunResultStore(pool)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin")
	target, err := targetStore.LockOrCreate(ctx, tx, orgID, automation.ID, repoID, models.AutomationTargetKindGitHubPullRequest, "7")
	require.NoError(t, err, "create target")
	_, err = targetStore.InsertGeneration(ctx, tx, orgID, target.ID, session.ID)
	require.NoError(t, err, "insert generation")
	require.NoError(t, tx.Commit(ctx), "commit")

	lockToken := uuid.New()
	jobID := seedRunningJob(t, pool, orgID, lockToken)
	runID := seedRun(t, pool, automation)
	_, err = pool.Exec(ctx, `
		UPDATE automation_runs
		SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2,
			job_id = $3, execution_started_at = now(), attempt = 1, attempt_lock_token = $4
		WHERE id = $5 AND org_id = $6`, target.ID, session.ID, jobID, lockToken, runID, orgID)
	require.NoError(t, err, "reserve run")

	marker := func(token uuid.UUID, attempt int) *models.AutomationRunResult {
		return &models.AutomationRunResult{
			RunID: runID, OrgID: orgID, Attempt: attempt, AttemptLockToken: token,
			ThreadID: uuid.New(), TurnNumber: 1, Outcome: models.AutomationRunResultTurnCompleted,
			ReviewComplete: true, NativeContext: true,
		}
	}

	ok, err := resultStore.Write(ctx, nil, orgID, jobID, marker(uuid.New(), 1))
	require.NoError(t, err, "write with a stale token should not error")
	require.False(t, ok, "a stale attempt token must be rejected")
	_, err = resultStore.GetByRun(ctx, orgID, runID)
	require.ErrorIs(t, err, db.ErrAutomationRunResultNotFound, "no marker should exist after a rejected write")

	ok, err = resultStore.Write(ctx, nil, orgID, uuid.New(), marker(lockToken, 1))
	require.NoError(t, err, "write for another job should not error")
	require.False(t, ok, "a marker for a job the run is not executing under must be rejected")

	first := marker(lockToken, 1)
	ok, err = resultStore.Write(ctx, nil, orgID, jobID, first)
	require.NoError(t, err, "write under the live lease should succeed")
	require.True(t, ok, "the owner's marker should be accepted")
	require.False(t, first.RecordedAt.IsZero(), "the stored marker should carry its recorded time")

	// A duplicate write by the same attempt leaves the marker unchanged.
	duplicate := marker(lockToken, 1)
	duplicate.ReviewComplete = false
	ok, err = resultStore.Write(ctx, nil, orgID, jobID, duplicate)
	require.NoError(t, err, "same-attempt duplicate should not error")
	require.True(t, ok, "the same attempt is still the owner")
	stored, err := resultStore.GetByRun(ctx, orgID, runID)
	require.NoError(t, err, "read marker")
	require.Equal(t, *first, stored, "a same-attempt duplicate must not change the stored marker")

	// Once the job lease is gone, the same token can no longer write.
	_, err = pool.Exec(ctx, `UPDATE jobs SET status = 'pending', lock_token = NULL WHERE id = $1 AND org_id = $2`, jobID, orgID)
	require.NoError(t, err, "reclaim the job")
	ok, err = resultStore.Write(ctx, nil, orgID, jobID, marker(lockToken, 1))
	require.NoError(t, err, "write after reclaim should not error")
	require.False(t, ok, "a worker whose job was reclaimed cannot write a marker")

}

// linkSessionToRun records sessionID as a session created by runID through
// session_automation_links, the historical run-to-session relationship.
func linkSessionToRun(t *testing.T, pool *pgxpool.Pool, orgID, runID, sessionID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO session_automation_links (session_id, org_id, automation_run_id)
		VALUES ($1, $2, $3)`, sessionID, orgID, runID)
	require.NoError(t, err, "link session to run")
}

// TestAutomationRunList_SessionLookup proves the run list's session
// projection after the continuity rewrite: the executing session on the run
// row wins when present, and the historical fallback still picks the newest
// live linked session even when a newer linked session was soft-deleted.
func TestAutomationRunList_SessionLookup(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "acme/web")
	automation := seedAutomation(t, pool, orgID, repoID)
	runStore := db.NewAutomationRunStore(pool)

	// Run A: executing session recorded on the run row, plus an unrelated link.
	runA := seedRun(t, pool, automation)
	executing := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	linked := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	linkSessionToRun(t, pool, orgID, runA, linked.ID)
	_, err := pool.Exec(ctx, `UPDATE automation_runs SET session_id = $1 WHERE id = $2 AND org_id = $3`, executing.ID, runA, orgID)
	require.NoError(t, err, "record the executing session")

	// Run B: two linked sessions, the newer one soft-deleted.
	runB := seedRun(t, pool, automation)
	older := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	newer := seedSession(t, pool, orgID, sessionOpts{RepositoryID: &repoID})
	linkSessionToRun(t, pool, orgID, runB, older.ID)
	linkSessionToRun(t, pool, orgID, runB, newer.ID)
	_, err = pool.Exec(ctx, `UPDATE sessions SET created_at = created_at + interval '1 minute' WHERE id = $1 AND org_id = $2`, newer.ID, orgID)
	require.NoError(t, err, "make the second session the newest")
	_, err = pool.Exec(ctx, `UPDATE sessions SET deleted_at = now() WHERE id = $1 AND org_id = $2`, newer.ID, orgID)
	require.NoError(t, err, "soft-delete the newest linked session")

	runs, err := runStore.ListByAutomation(ctx, orgID, automation.ID, db.AutomationRunFilters{Limit: 10})
	require.NoError(t, err, "list runs")
	byID := map[uuid.UUID]models.AutomationRun{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	require.Len(t, byID, 2, "both runs are listed")

	require.NotNil(t, byID[runA].Session, "run A projects a session")
	require.Equal(t, executing.ID, byID[runA].Session.ID, "the executing session on the run row wins over the creation link")
	require.NotNil(t, byID[runA].SessionID, "run A exposes its executing session id")
	require.Equal(t, executing.ID, *byID[runA].SessionID, "run A's session_id is the executing session")

	require.NotNil(t, byID[runB].Session, "run B falls back to a linked session")
	require.Equal(t, older.ID, byID[runB].Session.ID, "the fallback skips the soft-deleted newest session and returns the live one")
	require.Nil(t, byID[runB].SessionID, "run B has no executing session of its own")
}
