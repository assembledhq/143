package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// newAutomationTargetPostgres provisions an isolated schema with the minimal
// parent tables migration 000290 references, applies the real migration, and
// returns a pool bound to that schema. Skips without TEST_DATABASE_URL, which
// CI's backend-test job exports.
func newAutomationTargetPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the automation target schema proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "automation_targets_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated test schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse pool settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations(id uuid PRIMARY KEY);
		CREATE TABLE automations(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, deleted_at timestamptz);
		CREATE TABLE repositories(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL);
		CREATE TABLE sessions(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, turn_holding_container boolean NOT NULL DEFAULT false);
		CREATE TABLE automation_runs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, automation_id uuid NOT NULL, status text NOT NULL DEFAULT 'pending', triggered_at timestamptz NOT NULL DEFAULT now());
		CREATE TABLE session_messages(id bigserial PRIMARY KEY, org_id uuid NOT NULL);
		CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, status text NOT NULL DEFAULT 'pending', lock_token uuid, lease_expires_at timestamptz);`)
	require.NoError(t, err, "create parent tables")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000290_automation_target_session_continuity.up.sql"))
	require.NoError(t, err, "read migration")
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "apply migration 000290")
	return pool
}

type automationTargetFixture struct {
	orgID        uuid.UUID
	automationID uuid.UUID
	repoID       uuid.UUID
}

func seedAutomationTargetFixture(t *testing.T, pool *pgxpool.Pool) automationTargetFixture {
	t.Helper()
	ctx := context.Background()
	f := automationTargetFixture{orgID: uuid.New(), automationID: uuid.New(), repoID: uuid.New()}
	_, err := pool.Exec(ctx, `INSERT INTO organizations VALUES($1)`, f.orgID)
	require.NoError(t, err, "seed org")
	_, err = pool.Exec(ctx, `INSERT INTO automations(id, org_id) VALUES($1,$2)`, f.automationID, f.orgID)
	require.NoError(t, err, "seed automation")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id, org_id) VALUES($1,$2)`, f.repoID, f.orgID)
	require.NoError(t, err, "seed repository")
	return f
}

func seedPostgresSession(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(), `INSERT INTO sessions(org_id) VALUES($1) RETURNING id`, orgID).Scan(&id), "seed session")
	return id
}

func seedPostgresRun(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(), `INSERT INTO automation_runs(org_id, automation_id) VALUES($1,$2) RETURNING id`, f.orgID, f.automationID).Scan(&id), "seed run")
	return id
}

func seedPostgresRunningJob(t *testing.T, pool *pgxpool.Pool, orgID, lockToken uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO jobs(org_id, status, lock_token, lease_expires_at) VALUES($1,'running',$2, now() + interval '5 minutes') RETURNING id`,
		orgID, lockToken).Scan(&id), "seed running job")
	return id
}

func postgresOwnerMarker(t *testing.T, pool *pgxpool.Pool, orgID, sessionID uuid.UUID) *uuid.UUID {
	t.Helper()
	var marker *uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT automation_owner_generation_id FROM sessions WHERE id = $1 AND org_id = $2`, sessionID, orgID).Scan(&marker),
		"read owner marker")
	return marker
}

func isPostgresViolation(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

const (
	pgUniqueViolation = "23505"
	pgCheckViolation  = "23514"
)

func reserveExecutingRun(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture, runID, targetID, sessionID, jobID, lockToken uuid.UUID) error {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE automation_runs
		SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2,
			job_id = $3, execution_started_at = now(), attempt = 1, attempt_lock_token = $4
		WHERE id = $5 AND org_id = $6`, targetID, sessionID, jobID, lockToken, runID, f.orgID)
	return err
}

// TestAutomationTargetsPostgres proves migration 000290 and the target,
// generation, and result-marker stores against a real PostgreSQL: the
// same-org guard, one active generation per target, the executing CHECK and
// index, both ownership-release paths of retirement, the wake outbox
// ordering, the result marker fences, and the down migration.
func TestAutomationTargetsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool, automationTargetFixture)
	}{
		{"cross-org parents never create a target", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			other := seedAutomationTargetFixture(t, pool)
			store := NewAutomationTargetStore(pool)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			defer tx.Rollback(ctx) //nolint:errcheck // rollback after rollback is a no-op
			_, err = store.LockOrCreate(ctx, tx, f.orgID, f.automationID, other.repoID, models.AutomationTargetKindGitHubPullRequest, "1")
			require.ErrorIs(t, err, ErrAutomationTargetNotFound, "a repository outside the org must not create a target")
			_, err = store.LockOrCreate(ctx, tx, other.orgID, f.automationID, other.repoID, models.AutomationTargetKindGitHubPullRequest, "1")
			require.ErrorIs(t, err, ErrAutomationTargetNotFound, "an automation outside the org must not create a target")
			_, err = store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "1")
			require.NoError(t, err, "same-org parents create the target")
		}},
		{"generation lifecycle and ownership release", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			store := NewAutomationTargetStore(pool)
			first := seedPostgresSession(t, pool, f.orgID)
			second := seedPostgresSession(t, pool, f.orgID)

			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			target, err := store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "1234")
			require.NoError(t, err, "create target")
			require.Equal(t, 0, target.ActiveGeneration, "new target has no generation")
			gen1, err := store.InsertGeneration(ctx, tx, f.orgID, target.ID, first)
			require.NoError(t, err, "insert first generation")
			require.Equal(t, 1, gen1.Generation, "first generation is 1")
			require.NoError(t, tx.Commit(ctx), "commit")
			require.Equal(t, &gen1.ID, postgresOwnerMarker(t, pool, f.orgID, first), "session is marked owned")

			tx, err = pool.Begin(ctx)
			require.NoError(t, err, "begin")
			again, err := store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "1234")
			require.NoError(t, err, "re-lock existing target")
			require.Equal(t, target.ID, again.ID, "same identity maps to the same row")
			require.Equal(t, 1, again.ActiveGeneration, "active generation advanced")
			_, err = store.InsertGeneration(ctx, tx, f.orgID, target.ID, second)
			require.True(t, isPostgresViolation(err, pgUniqueViolation), "second active generation must violate the active index, got %v", err)
			require.NoError(t, tx.Rollback(ctx), "rollback")

			runID := seedPostgresRun(t, pool, f)
			_, err = pool.Exec(ctx, `UPDATE automation_runs SET dispatch_state = 'executing', target_id = $1, target_generation = 1, session_id = $2 WHERE id = $3`, target.ID, first, runID)
			require.True(t, isPostgresViolation(err, pgCheckViolation), "executing without job_id must violate the executing CHECK, got %v", err)

			lockToken := uuid.New()
			jobID := seedPostgresRunningJob(t, pool, f.orgID, lockToken)
			require.NoError(t, reserveExecutingRun(t, pool, f, runID, target.ID, first, jobID, lockToken), "complete reservation satisfies the CHECK")
			otherRun := seedPostgresRun(t, pool, f)
			err = reserveExecutingRun(t, pool, f, otherRun, target.ID, first, jobID, lockToken)
			require.True(t, isPostgresViolation(err, pgUniqueViolation), "second executing run must violate the one-executing index, got %v", err)

			tx, err = pool.Begin(ctx)
			require.NoError(t, err, "begin")
			retired, err := store.RetireGeneration(ctx, tx, f.orgID, gen1.ID, models.AutomationTargetRetiredManualReset)
			require.NoError(t, err, "retire during execution")
			require.NoError(t, tx.Commit(ctx), "commit")
			require.True(t, retired.OwnershipReleasePending, "executing generation defers the release")
			require.Equal(t, &gen1.ID, postgresOwnerMarker(t, pool, f.orgID, first), "marker stays while the turn executes")
			woke, err := store.GetByID(ctx, f.orgID, target.ID)
			require.NoError(t, err, "reload target")
			require.NotNil(t, woke.WakeRequestedAt, "retirement writes the wake outbox")

			tx, err = pool.Begin(ctx)
			require.NoError(t, err, "begin")
			_, err = store.RetireGeneration(ctx, tx, f.orgID, gen1.ID, models.AutomationTargetRetiredManualReset)
			require.ErrorIs(t, err, ErrAutomationTargetGenerationNotActive, "retired generation cannot be retired again")
			require.NoError(t, tx.Rollback(ctx), "rollback")

			_, err = pool.Exec(ctx, `UPDATE automation_runs SET dispatch_state = 'done' WHERE id = $1`, runID)
			require.NoError(t, err, "finish run")
			tx, err = pool.Begin(ctx)
			require.NoError(t, err, "begin")
			gen2, err := store.InsertGeneration(ctx, tx, f.orgID, target.ID, second)
			require.NoError(t, err, "insert second generation after retirement")
			require.Equal(t, 2, gen2.Generation, "generations increment")
			require.NoError(t, tx.Commit(ctx), "commit")
			active, err := store.GetActiveGeneration(ctx, nil, f.orgID, target.ID)
			require.NoError(t, err, "active lookup")
			require.Equal(t, gen2.ID, active.ID, "new generation is active")

			tx, err = pool.Begin(ctx)
			require.NoError(t, err, "begin")
			retiredAll, err := store.RetireActiveGenerationsForAutomation(ctx, tx, f.orgID, f.automationID, models.AutomationTargetRetiredContinuityDisabled)
			require.NoError(t, err, "retire every active generation")
			require.NoError(t, tx.Commit(ctx), "commit")
			require.Len(t, retiredAll, 1, "only the active generation is retired")
			require.False(t, retiredAll[0].OwnershipReleasePending, "idle generation releases now")
			require.Nil(t, postgresOwnerMarker(t, pool, f.orgID, second), "marker cleared for the idle session")
			_, err = store.GetActiveGeneration(ctx, nil, f.orgID, target.ID)
			require.ErrorIs(t, err, ErrAutomationTargetGenerationNotFound, "nothing active after retirement")

			_, err = pool.Exec(ctx, `UPDATE automation_target_sessions SET retired_at = NULL WHERE id = $1`, gen2.ID)
			require.True(t, isPostgresViolation(err, pgCheckViolation), "retired row without retired_at must violate the retirement CHECK, got %v", err)
		}},
		{"wake outbox clears only when no newer request exists", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			store := NewAutomationTargetStore(pool)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			target, err := store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "9")
			require.NoError(t, err, "create target")
			require.NoError(t, tx.Commit(ctx), "commit")

			firstAt, err := store.RequestWake(ctx, nil, f.orgID, target.ID)
			require.NoError(t, err, "first wake request")
			_, err = pool.Exec(ctx, `UPDATE automation_targets SET wake_requested_at = wake_requested_at + interval '1 second' WHERE id = $1`, target.ID)
			require.NoError(t, err, "simulate a newer request")
			cleared, err := store.ClearWake(ctx, nil, f.orgID, target.ID, firstAt)
			require.NoError(t, err, "clear with a stale timestamp")
			require.False(t, cleared, "a newer request must survive the clear")
			newer, err := store.GetByID(ctx, f.orgID, target.ID)
			require.NoError(t, err, "reload")
			require.NotNil(t, newer.WakeRequestedAt, "newer request still recorded")
			// A transaction that started earlier can write a later request with
			// an older now(); the clear must compare the exact observed marker.
			_, err = pool.Exec(ctx, `UPDATE automation_targets SET wake_requested_at = $2::timestamptz - interval '1 second' WHERE id = $1`, target.ID, *newer.WakeRequestedAt)
			require.NoError(t, err, "simulate a later request carrying an older timestamp")
			cleared, err = store.ClearWake(ctx, nil, f.orgID, target.ID, *newer.WakeRequestedAt)
			require.NoError(t, err, "clear against an older marker")
			require.False(t, cleared, "a request with an older timestamp written after the observation must survive")
			current, err := store.GetByID(ctx, f.orgID, target.ID)
			require.NoError(t, err, "reload")
			cleared, err = store.ClearWake(ctx, nil, f.orgID, target.ID, *current.WakeRequestedAt)
			require.NoError(t, err, "clear with the current timestamp")
			require.True(t, cleared, "the current request clears")
			require.NoError(t, store.SetLifecycle(ctx, nil, f.orgID, target.ID, models.AutomationTargetLifecycleMerged), "set lifecycle")
			merged, err := store.GetByID(ctx, f.orgID, target.ID)
			require.NoError(t, err, "reload")
			require.Equal(t, models.AutomationTargetLifecycleMerged, merged.LifecycleState, "lifecycle recorded")
			require.NotNil(t, merged.LifecycleUpdatedAt, "lifecycle timestamp recorded")
		}},
		{"result marker fences", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			targets := NewAutomationTargetStore(pool)
			results := NewAutomationRunResultStore(pool)
			session := seedPostgresSession(t, pool, f.orgID)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			target, err := targets.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "7")
			require.NoError(t, err, "create target")
			_, err = targets.InsertGeneration(ctx, tx, f.orgID, target.ID, session)
			require.NoError(t, err, "insert generation")
			require.NoError(t, tx.Commit(ctx), "commit")

			lockToken := uuid.New()
			jobID := seedPostgresRunningJob(t, pool, f.orgID, lockToken)
			runID := seedPostgresRun(t, pool, f)
			require.NoError(t, reserveExecutingRun(t, pool, f, runID, target.ID, session, jobID, lockToken), "reserve run")
			marker := func(token uuid.UUID) *models.AutomationRunResult {
				return &models.AutomationRunResult{
					RunID: runID, OrgID: f.orgID, Attempt: 1, AttemptLockToken: token,
					ThreadID: uuid.New(), TurnNumber: 1, Outcome: models.AutomationRunResultTurnCompleted,
					ReviewComplete: true, NativeContext: true,
				}
			}

			ok, err := results.Write(ctx, nil, f.orgID, jobID, marker(uuid.New()))
			require.NoError(t, err, "stale token write")
			require.False(t, ok, "stale attempt token is rejected")
			ok, err = results.Write(ctx, nil, f.orgID, uuid.New(), marker(lockToken))
			require.NoError(t, err, "other job write")
			require.False(t, ok, "a job the run is not executing under is rejected")
			_, err = results.GetByRun(ctx, f.orgID, runID)
			require.ErrorIs(t, err, ErrAutomationRunResultNotFound, "no marker after rejected writes")

			first := marker(lockToken)
			ok, err = results.Write(ctx, nil, f.orgID, jobID, first)
			require.NoError(t, err, "owner write")
			require.True(t, ok, "owner's marker is accepted")
			require.False(t, first.RecordedAt.IsZero(), "recorded time returned")

			// A duplicate end-of-attempt callback must not change the evidence.
			duplicate := marker(lockToken)
			duplicate.ReviewComplete = false
			duplicate.Outcome = models.AutomationRunResultAgentFailed
			ok, err = results.Write(ctx, nil, f.orgID, jobID, duplicate)
			require.NoError(t, err, "same-attempt duplicate write")
			require.True(t, ok, "the same attempt is still the owner")
			stored, err := results.GetByRun(ctx, f.orgID, runID)
			require.NoError(t, err, "read marker")
			require.Equal(t, *first, stored, "a same-attempt duplicate leaves the stored marker unchanged")
			require.Equal(t, *first, *duplicate, "the duplicate write returns the stored marker")

			// A newer attempt under a new lease replaces the marker, and a
			// failed outcome never records review_complete even when asked.
			secondToken := uuid.New()
			secondJob := seedPostgresRunningJob(t, pool, f.orgID, secondToken)
			_, err = pool.Exec(ctx, `UPDATE automation_runs SET attempt = 2, attempt_lock_token = $1, job_id = $2 WHERE id = $3`, secondToken, secondJob, runID)
			require.NoError(t, err, "claim a second attempt")
			replacement := marker(secondToken)
			replacement.Attempt = 2
			replacement.Outcome = models.AutomationRunResultAgentFailed
			replacement.ReviewComplete = true
			ok, err = results.Write(ctx, nil, f.orgID, secondJob, replacement)
			require.NoError(t, err, "newer attempt write")
			require.True(t, ok, "a newer attempt replaces the marker")
			stored, err = results.GetByRun(ctx, f.orgID, runID)
			require.NoError(t, err, "read replaced marker")
			require.Equal(t, 2, stored.Attempt, "the newer attempt's marker is stored")
			require.Equal(t, models.AutomationRunResultAgentFailed, stored.Outcome, "the newer outcome is stored")
			require.False(t, stored.ReviewComplete, "review_complete is coerced false for a failed turn")
			ok, err = results.Write(ctx, nil, f.orgID, jobID, marker(lockToken))
			require.NoError(t, err, "older attempt write after replacement")
			require.False(t, ok, "an older attempt cannot write once a newer one exists")

			// Once the job lease is gone, the current attempt and token can no
			// longer write even though every run-side fence still matches.
			_, err = pool.Exec(ctx, `UPDATE jobs SET status = 'pending', lock_token = NULL WHERE id = $1`, secondJob)
			require.NoError(t, err, "reclaim job")
			reclaimed := marker(secondToken)
			reclaimed.Attempt = 2
			reclaimed.Outcome = models.AutomationRunResultCancelled
			ok, err = results.Write(ctx, nil, f.orgID, secondJob, reclaimed)
			require.NoError(t, err, "write after reclaim")
			require.False(t, ok, "a reclaimed job's token cannot write")
			stored, err = results.GetByRun(ctx, f.orgID, runID)
			require.NoError(t, err, "read marker after reclaim")
			require.Equal(t, models.AutomationRunResultAgentFailed, stored.Outcome, "a reclaimed job's write leaves the marker unchanged")
		}},
		{"disabling continuity waits for a target whose first generation is uncommitted", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			store := NewAutomationTargetStore(pool)
			session := seedPostgresSession(t, pool, f.orgID)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			target, err := store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "7")
			require.NoError(t, err, "create target with no generation")
			require.NoError(t, tx.Commit(ctx), "commit")

			// A first dispatch holds the target lock while inserting generation
			// 1; the committed row still says active_generation = 0, so the
			// continuity switch must wait on the lock rather than skip the
			// target on that stale value.
			dispatch, err := pool.Begin(ctx)
			require.NoError(t, err, "begin dispatch")
			_, err = store.LockByID(ctx, dispatch, f.orgID, target.ID)
			require.NoError(t, err, "dispatch locks the target")
			gen1, err := store.InsertGeneration(ctx, dispatch, f.orgID, target.ID, session)
			require.NoError(t, err, "dispatch inserts the first generation")

			type outcome struct {
				retired []models.AutomationTargetSession
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				bulk, err := pool.Begin(ctx)
				if err != nil {
					done <- outcome{err: err}
					return
				}
				retired, err := store.RetireActiveGenerationsForAutomation(ctx, bulk, f.orgID, f.automationID, models.AutomationTargetRetiredContinuityDisabled)
				if err != nil {
					_ = bulk.Rollback(ctx)
					done <- outcome{err: err}
					return
				}
				done <- outcome{retired: retired, err: bulk.Commit(ctx)}
			}()
			require.Eventually(t, func() bool {
				var waiting int
				err := pool.QueryRow(ctx, `
					SELECT count(*) FROM pg_stat_activity
					WHERE wait_event_type = 'Lock' AND query ILIKE '%FROM automation_targets%'`).Scan(&waiting)
				return err == nil && waiting > 0
			}, 10*time.Second, 20*time.Millisecond, "the continuity switch should block on the target lock")
			require.NoError(t, dispatch.Commit(ctx), "commit dispatch")

			result := <-done
			require.NoError(t, result.err, "the continuity switch should succeed once the lock is released")
			require.Len(t, result.retired, 1, "the newly inserted generation is retired")
			require.Equal(t, gen1.ID, result.retired[0].ID, "the generation committed by the dispatch is the one retired")
			_, err = store.GetActiveGeneration(ctx, nil, f.orgID, target.ID)
			require.ErrorIs(t, err, ErrAutomationTargetGenerationNotFound, "no generation stays active after the switch")
			require.Nil(t, postgresOwnerMarker(t, pool, f.orgID, session), "the session is released")
		}},
		{"disabling continuity retires a generation inserted while it waited for the target lock", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			store := NewAutomationTargetStore(pool)
			first := seedPostgresSession(t, pool, f.orgID)
			second := seedPostgresSession(t, pool, f.orgID)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin")
			target, err := store.LockOrCreate(ctx, tx, f.orgID, f.automationID, f.repoID, models.AutomationTargetKindGitHubPullRequest, "42")
			require.NoError(t, err, "create target")
			gen1, err := store.InsertGeneration(ctx, tx, f.orgID, target.ID, first)
			require.NoError(t, err, "insert first generation")
			require.NoError(t, tx.Commit(ctx), "commit")

			// A dispatch transaction holds the target lock while it replaces
			// the generation; the continuity switch must wait for it and then
			// retire the replacement, not the row it might have read earlier.
			dispatch, err := pool.Begin(ctx)
			require.NoError(t, err, "begin dispatch")
			_, err = store.LockByID(ctx, dispatch, f.orgID, target.ID)
			require.NoError(t, err, "dispatch locks the target")

			type outcome struct {
				retired []models.AutomationTargetSession
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				bulk, err := pool.Begin(ctx)
				if err != nil {
					done <- outcome{err: err}
					return
				}
				retired, err := store.RetireActiveGenerationsForAutomation(ctx, bulk, f.orgID, f.automationID, models.AutomationTargetRetiredContinuityDisabled)
				if err != nil {
					_ = bulk.Rollback(ctx)
					done <- outcome{err: err}
					return
				}
				done <- outcome{retired: retired, err: bulk.Commit(ctx)}
			}()
			require.Eventually(t, func() bool {
				var waiting int
				err := pool.QueryRow(ctx, `
					SELECT count(*) FROM pg_stat_activity
					WHERE wait_event_type = 'Lock' AND query ILIKE '%FROM automation_targets%'`).Scan(&waiting)
				return err == nil && waiting > 0
			}, 10*time.Second, 20*time.Millisecond, "the continuity switch should block on the target lock")

			_, err = store.RetireGeneration(ctx, dispatch, f.orgID, gen1.ID, models.AutomationTargetRetiredAgentConfigChanged)
			require.NoError(t, err, "dispatch retires the first generation")
			gen2, err := store.InsertGeneration(ctx, dispatch, f.orgID, target.ID, second)
			require.NoError(t, err, "dispatch inserts the replacement")
			require.NoError(t, dispatch.Commit(ctx), "commit dispatch")

			result := <-done
			require.NoError(t, result.err, "the continuity switch should succeed once the lock is released")
			require.Len(t, result.retired, 1, "exactly the replacement generation is retired")
			require.Equal(t, gen2.ID, result.retired[0].ID, "the generation active at lock time is the one retired")
			_, err = store.GetActiveGeneration(ctx, nil, f.orgID, target.ID)
			require.ErrorIs(t, err, ErrAutomationTargetGenerationNotFound, "no generation stays active after the switch")
			require.Nil(t, postgresOwnerMarker(t, pool, f.orgID, second), "the replacement's session is released")
		}},
		{"down migration removes every object", func(t *testing.T, pool *pgxpool.Pool, f automationTargetFixture) {
			ctx := context.Background()
			down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000290_automation_target_session_continuity.down.sql"))
			require.NoError(t, err, "read down migration")
			_, err = pool.Exec(ctx, string(down))
			require.NoError(t, err, "apply down migration")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `
				SELECT count(*) FROM information_schema.tables
				WHERE table_schema = current_schema()
				  AND table_name IN ('automation_targets', 'automation_target_sessions', 'automation_run_results')`).Scan(&count), "count tables")
			require.Equal(t, 0, count, "down migration drops the new tables")
			require.NoError(t, pool.QueryRow(ctx, `
				SELECT count(*) FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND ((table_name = 'automations' AND column_name = 'session_continuity')
				    OR (table_name = 'sessions' AND column_name = 'automation_owner_generation_id')
				    OR (table_name = 'session_messages' AND column_name = 'automation_run_id')
				    OR (table_name = 'automation_runs' AND column_name IN ('target_id', 'dispatch_state', 'attempt')))`).Scan(&count), "count columns")
			require.Equal(t, 0, count, "down migration drops the new columns")
			_, err = pool.Exec(ctx, `SELECT 1 FROM automation_runs LIMIT 1`)
			require.NoError(t, err, "parent tables survive the down migration")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newAutomationTargetPostgres(t)
			tt.run(t, pool, seedAutomationTargetFixture(t, pool))
		})
	}
}
