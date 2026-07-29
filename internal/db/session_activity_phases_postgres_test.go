package db

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionActivityPhaseStorePostgresConcurrentStartsAllowOneRunningPhase(t *testing.T) {
	t.Parallel()

	pool := newActivityPhasePostgresTestPool(t)
	ctx := context.Background()
	orgID, sessionID, threadID := seedActivityPhasePostgresThread(t, pool)
	runtimeID, leaseToken := seedActivityPhasePostgresRuntime(t, pool, orgID, sessionID, threadID)
	store := NewSessionActivityPhaseStore(pool)
	startedAt := time.Now().UTC()

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.StartPhase(
				ctx, orgID, sessionID, threadID, 1, &runtimeID, &leaseToken,
				models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, startedAt,
			)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "concurrent starts should persist exactly one running phase")

	var phaseCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM session_activity_phases
		WHERE org_id = $1 AND thread_id = $2 AND status = 'running'`,
		orgID, threadID).Scan(&phaseCount)
	require.NoError(t, err, "test should count running phases after concurrent starts")
	require.Equal(t, 1, phaseCount, "partial unique index should enforce one running phase per thread")
}

func TestSessionActivityPhaseStorePostgresConcurrentBatchRetriesCreateOnePhase(t *testing.T) {
	t.Parallel()

	pool := newActivityPhasePostgresTestPool(t)
	ctx := context.Background()
	orgID, sessionID, threadID := seedActivityPhasePostgresThread(t, pool)
	runtimeID, leaseToken := seedActivityPhasePostgresRuntime(t, pool, orgID, sessionID, threadID)
	_, err := pool.Exec(ctx, `
		INSERT INTO thread_inbox_entries (
			org_id, session_id, thread_id, sequence_no, runtime_id, delivery_state
		) VALUES
			($1, $2, $3, 1, $4, 'delivered'),
			($1, $2, $3, 2, $4, 'delivered')`,
		orgID, sessionID, threadID, runtimeID)
	require.NoError(t, err, "test should seed one delivered instruction batch")

	store := NewSessionActivityPhaseStore(pool)
	acknowledgedAt := time.Now().UTC()
	start := make(chan struct{})
	type acknowledgeResult struct {
		batch models.ThreadInboxDeliveryBatch
		err   error
	}
	results := make(chan acknowledgeResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			batch, err := store.AcknowledgeInboxBatch(
				ctx, orgID, sessionID, threadID, runtimeID, leaseToken, nil, 2, acknowledgedAt,
			)
			results <- acknowledgeResult{batch: batch, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var batchID uuid.UUID
	for result := range results {
		require.NoError(t, result.err, "concurrent acknowledgment retry should return durable success")
		if batchID == uuid.Nil {
			batchID = result.batch.ID
		}
		require.Equal(t, batchID, result.batch.ID, "concurrent acknowledgment retries should return the same durable batch")
	}

	start = make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.StartPhase(
				ctx, orgID, sessionID, threadID, 1, nil, &leaseToken,
				models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInboxBatch, BatchID: &batchID},
				time.Now().UTC(),
			)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "concurrent execution-resume retries should start one phase")

	var phaseCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM session_activity_phases
		WHERE org_id = $1 AND trigger_batch_id = $2`,
		orgID, batchID).Scan(&phaseCount)
	require.NoError(t, err, "test should count phases for the acknowledged batch")
	require.Equal(t, 1, phaseCount, "one acknowledged batch should create exactly one phase")
}

func TestSessionActivityPhaseStorePostgresExpiredAcknowledgmentDoesNotDeadlockReconciliation(t *testing.T) {
	t.Parallel()

	pool := newActivityPhasePostgresTestPool(t)
	ctx := context.Background()
	orgID, sessionID, threadID := seedActivityPhasePostgresThread(t, pool)
	runtimeID, leaseToken := seedActivityPhasePostgresRuntime(t, pool, orgID, sessionID, threadID)
	store := NewSessionActivityPhaseStore(pool)
	phase, err := store.StartPhase(
		ctx, orgID, sessionID, threadID, 1, &runtimeID, &leaseToken,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().UTC().Add(-2*time.Minute),
	)
	require.NoError(t, err, "test should start a phase before expiring its runtime")
	_, err = pool.Exec(ctx, `
		INSERT INTO thread_inbox_entries (
			org_id, session_id, thread_id, sequence_no, runtime_id, delivery_state
		) VALUES ($1, $2, $3, 1, $4, 'delivered')`,
		orgID, sessionID, threadID, runtimeID)
	require.NoError(t, err, "test should seed a delivered instruction")
	_, err = pool.Exec(ctx, `
		UPDATE thread_runtimes
		SET lease_expires_at = now() - interval '1 minute'
		WHERE org_id = $1 AND id = $2`,
		orgID, runtimeID)
	require.NoError(t, err, "test should expire the runtime lease")

	raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	t.Cleanup(cancel)
	ackErr := make(chan error, 1)
	reconcileResult := make(chan struct {
		transitioned bool
		err          error
	}, 1)
	start := make(chan struct{})
	go func() {
		<-start
		_, err := store.AcknowledgeInboxBatch(
			raceCtx, orgID, sessionID, threadID, runtimeID, leaseToken, &phase.ID, 1, time.Now().UTC(),
		)
		ackErr <- err
	}()
	go func() {
		<-start
		transitioned, err := store.ReconcileStrandedPhase(
			raceCtx, orgID, phase.ID, time.Now().UTC(), time.Now().UTC(),
		)
		reconcileResult <- struct {
			transitioned bool
			err          error
		}{transitioned: transitioned, err: err}
	}()
	close(start)

	require.Error(t, <-ackErr, "expired runtime acknowledgment should be rejected during reconciliation")
	result := <-reconcileResult
	require.NoError(t, result.err, "runtime reconciliation should complete without a lock-order deadlock")
	require.True(t, result.transitioned, "runtime reconciliation should terminally close the expired phase")
}

func TestSessionActivityPhaseStorePostgresStrandedListExcludesActiveRuntimeLessPhase(t *testing.T) {
	t.Parallel()

	pool := newActivityPhasePostgresTestPool(t)
	ctx := context.Background()
	orgID, sessionID, activeThreadID := seedActivityPhasePostgresThread(t, pool)
	inactiveThreadID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO session_threads (id, org_id, session_id, status)
		VALUES ($1, $2, $3, 'idle')`,
		inactiveThreadID, orgID, sessionID)
	require.NoError(t, err, "test should seed an inactive thread")

	store := NewSessionActivityPhaseStore(pool)
	startedAt := time.Now().UTC().Add(-10 * time.Minute)
	activePhase, err := store.StartPhase(
		ctx, orgID, sessionID, activeThreadID, 1, nil, nil,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, startedAt,
	)
	require.NoError(t, err, "test should start an old runtime-less phase on the active thread")
	inactivePhase, err := store.StartPhase(
		ctx, orgID, sessionID, inactiveThreadID, 1, nil, nil,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, startedAt,
	)
	require.NoError(t, err, "test should start an old runtime-less phase on the inactive thread")

	phases, err := store.ListStrandedRunning(ctx, orgID, time.Now().UTC().Add(-2*time.Minute), 100)
	require.NoError(t, err, "stranded listing should query runtime-less phases")
	require.Equal(t, []models.SessionActivityPhase{inactivePhase}, phases, "bounded reconciliation candidates should exclude active runtime-less phases before ordering and limiting")
	require.NotEqual(t, activePhase.ID, inactivePhase.ID, "test phases should have distinct durable identities")
}

func newActivityPhasePostgresTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL activity phase concurrency tests")
	}

	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "test should parse TEST_DATABASE_URL")
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")

	schema := "test_activity_phases_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated activity phase schema")

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "test should parse the isolated pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "test should connect with the isolated search path")
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated activity phase schema")
		adminPool.Close()
	})

	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations (id uuid PRIMARY KEY);
		CREATE TABLE sessions (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL REFERENCES organizations(id)
		);
		CREATE TABLE session_threads (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL REFERENCES organizations(id),
			session_id uuid NOT NULL REFERENCES sessions(id),
			status text NOT NULL
		);
		CREATE TABLE thread_runtimes (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL REFERENCES organizations(id),
			session_id uuid NOT NULL REFERENCES sessions(id),
			thread_id uuid NOT NULL REFERENCES session_threads(id),
			lease_token uuid,
			status text NOT NULL,
			lease_expires_at timestamptz,
			last_acked_sequence bigint NOT NULL DEFAULT 0,
			heartbeat_at timestamptz,
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE thread_inbox_entries (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			org_id uuid NOT NULL REFERENCES organizations(id),
			session_id uuid NOT NULL REFERENCES sessions(id),
			thread_id uuid NOT NULL REFERENCES session_threads(id),
			sequence_no bigint NOT NULL,
			runtime_id uuid REFERENCES thread_runtimes(id),
			delivery_state text NOT NULL,
			acked_at timestamptz,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`)
	require.NoError(t, err, "test should create the pre-migration activity lifecycle schema")

	migration, err := os.ReadFile("../../migrations/000262_session_activity_phases.up.sql")
	require.NoError(t, err, "test should read the activity phase migration")
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err, "test should apply the activity phase migration")
	return pool
}

func seedActivityPhasePostgresThread(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()

	orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, orgID)
	require.NoError(t, err, "test should seed an organization")
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id, org_id) VALUES ($1, $2)`, sessionID, orgID)
	require.NoError(t, err, "test should seed a session")
	_, err = pool.Exec(ctx, `
		INSERT INTO session_threads (id, org_id, session_id, status)
		VALUES ($1, $2, $3, 'running')`,
		threadID, orgID, sessionID)
	require.NoError(t, err, "test should seed a running thread")
	return orgID, sessionID, threadID
}

func seedActivityPhasePostgresRuntime(t *testing.T, pool *pgxpool.Pool, orgID, sessionID, threadID uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()

	runtimeID, leaseToken := uuid.New(), uuid.New()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO thread_runtimes (
			id, org_id, session_id, thread_id, lease_token, status, lease_expires_at
		) VALUES ($1, $2, $3, $4, $5, 'live', now() + interval '10 minutes')`,
		runtimeID, orgID, sessionID, threadID, leaseToken)
	require.NoError(t, err, "test should seed a live runtime with a current lease")
	return runtimeID, leaseToken
}
