package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionThreadStorePostgresArchiveWinsAgainstQueuedAppend(t *testing.T) {
	t.Parallel()

	pool := newThreadArchivalPostgresTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	orgID, sessionID, threadID := seedThreadArchivalPostgresThread(t, pool)

	archiveTx, err := pool.Begin(ctx)
	require.NoError(t, err, "test should begin the archiving transaction")
	t.Cleanup(func() {
		rollbackErr := archiveTx.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			require.NoError(t, rollbackErr, "archive transaction cleanup should rollback cleanly")
		}
	})

	archived, err := NewSessionThreadStore(archiveTx).Archive(ctx, orgID, sessionID, threadID)
	require.NoError(t, err, "archive transaction should mark the thread archived before commit")
	require.NotNil(t, archived.ArchivedAt, "archive transaction should stamp archived_at")

	appendConn := acquireThreadArchivalTestConn(t, ctx, pool)
	appendPID := backendPID(t, ctx, appendConn)
	appendDone := make(chan error, 1)
	appendStarted := make(chan struct{})
	go func() {
		close(appendStarted)
		_, appendErr := NewThreadInboxStore(appendConn).AppendForMessage(ctx, orgID, AppendThreadInboxEntryParams{
			SessionID: sessionID,
			ThreadID:  threadID,
			MessageID: 42,
			EntryType: models.ThreadInboxEntryTypeUserMessage,
			Payload:   json.RawMessage(`{"message_id":42}`),
		})
		appendDone <- appendErr
	}()

	<-appendStarted
	waitForBlockedBackend(t, ctx, archiveTx, appendPID)

	require.NoError(t, archiveTx.Commit(ctx), "archive transaction should commit cleanly")
	require.ErrorIs(t, waitForResult(t, ctx, appendDone), pgx.ErrNoRows, "append should reject the archived thread after the archive commits")

	err = NewSessionThreadStore(pool).IncrementPendingMessages(ctx, orgID, threadID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "pending counter should reject archived threads after archive wins")
}

func TestSessionThreadStorePostgresQueuedAppendWinsAgainstArchive(t *testing.T) {
	t.Parallel()

	pool := newThreadArchivalPostgresTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	orgID, sessionID, threadID := seedThreadArchivalPostgresThread(t, pool)

	queueTx, err := pool.Begin(ctx)
	require.NoError(t, err, "test should begin the queueing transaction")
	t.Cleanup(func() {
		rollbackErr := queueTx.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			require.NoError(t, rollbackErr, "queue transaction cleanup should rollback cleanly")
		}
	})

	queueInbox := NewThreadInboxStore(queueTx)
	entry, err := queueInbox.AppendForMessage(ctx, orgID, AppendThreadInboxEntryParams{
		SessionID: sessionID,
		ThreadID:  threadID,
		MessageID: 77,
		EntryType: models.ThreadInboxEntryTypeUserMessage,
		Payload:   json.RawMessage(`{"message_id":77}`),
	})
	require.NoError(t, err, "queueing transaction should append a pending inbox entry")
	require.Equal(t, int64(1), entry.SequenceNo, "first queued message should take sequence 1")

	err = NewSessionThreadStore(queueTx).IncrementPendingMessages(ctx, orgID, threadID)
	require.NoError(t, err, "queueing transaction should increment pending_message_count before commit")

	archiveConn := acquireThreadArchivalTestConn(t, ctx, pool)
	archivePID := backendPID(t, ctx, archiveConn)
	archiveDone := make(chan error, 1)
	archiveStarted := make(chan struct{})
	go func() {
		close(archiveStarted)
		_, archiveErr := NewSessionThreadStore(archiveConn).Archive(ctx, orgID, sessionID, threadID)
		archiveDone <- archiveErr
	}()

	<-archiveStarted
	waitForBlockedBackend(t, ctx, queueTx, archivePID)

	require.NoError(t, queueTx.Commit(ctx), "queueing transaction should commit cleanly")
	require.ErrorIs(t, waitForResult(t, ctx, archiveDone), pgx.ErrNoRows, "archive should refuse the thread once the committed row shows queued work")

	var pendingCount int
	err = pool.QueryRow(ctx, `
		SELECT pending_message_count
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3`,
		orgID, sessionID, threadID).Scan(&pendingCount)
	require.NoError(t, err, "test should reload the thread after the queueing commit")
	require.Equal(t, 1, pendingCount, "queueing commit should persist the pending message count that blocks archive")
}

func acquireThreadArchivalTestConn(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err, "test should acquire a dedicated contender connection")
	t.Cleanup(conn.Release)
	return conn
}

func backendPID(t *testing.T, ctx context.Context, conn interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) int32 {
	t.Helper()

	var pid int32
	err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
	require.NoError(t, err, "test should read the contender backend pid")
	return pid
}

func waitForBlockedBackend(t *testing.T, ctx context.Context, locker interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, blockedPID int32) {
	t.Helper()

	lockerPID := backendPID(t, ctx, locker)
	var observed atomic.Bool
	require.Eventually(t, func() bool {
		var isBlocked bool
		err := locker.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM unnest(pg_blocking_pids($1)) AS blocker_pid
				WHERE blocker_pid = $2
			)`,
			blockedPID, lockerPID).Scan(&isBlocked)
		require.NoError(t, err, "test should query pg_blocking_pids for the contender")
		if isBlocked {
			observed.Store(true)
		}
		return isBlocked
	}, 2*time.Second, 20*time.Millisecond, "contender backend should block on the owner transaction before commit")
	require.True(t, observed.Load(), "test should observe the contender blocked by the owner transaction")
}

func waitForResult[T any](t *testing.T, ctx context.Context, resultCh <-chan T) T {
	t.Helper()

	select {
	case result := <-resultCh:
		return result
	case <-ctx.Done():
		require.FailNow(t, "timed out waiting for contender result", "context error: %v", ctx.Err())
		var zero T
		return zero
	}
}

func newThreadArchivalPostgresTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL thread archival concurrency tests")
	}

	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "test should parse TEST_DATABASE_URL")
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")

	schema := "test_thread_archival_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated thread archival schema")

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "test should parse the isolated pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "test should connect with the isolated search path")
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated thread archival schema")
		adminPool.Close()
	})

	_, err = pool.Exec(ctx, `
		CREATE FUNCTION gen_random_uuid() RETURNS uuid
		LANGUAGE sql
		AS $$
			SELECT (
				substr(hash, 1, 8) || '-' ||
				substr(hash, 9, 4) || '-' ||
				substr(hash, 13, 4) || '-' ||
				substr(hash, 17, 4) || '-' ||
				substr(hash, 21, 12)
			)::uuid
			FROM (
				SELECT md5(random()::text || clock_timestamp()::text) AS hash
			) s;
		$$;
		CREATE TABLE organizations (
			id uuid PRIMARY KEY
		);
		CREATE TABLE sessions (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL REFERENCES organizations(id)
		);
		CREATE TABLE session_threads (
			id uuid PRIMARY KEY,
			session_id uuid NOT NULL REFERENCES sessions(id),
			org_id uuid NOT NULL REFERENCES organizations(id),
			agent_type text NOT NULL DEFAULT '',
			model_override text,
			reasoning_effort text,
			label text NOT NULL DEFAULT '',
			instructions text NOT NULL DEFAULT '',
			file_scope jsonb,
			status text NOT NULL,
			agent_session_id text,
			current_turn integer NOT NULL DEFAULT 0,
			last_activity_at timestamptz NOT NULL DEFAULT now(),
			result_summary text,
			diff text,
			failure_explanation text,
			failure_category text,
			started_at timestamptz,
			completed_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now(),
			created_by_source text NOT NULL DEFAULT 'user',
			created_by_thread_id uuid,
			archived_at timestamptz,
			base_snapshot_key text,
			cost_cents double precision NOT NULL DEFAULT 0,
			pending_message_count integer NOT NULL DEFAULT 0,
			cancel_requested_at timestamptz,
			runtime_stop_reason text NOT NULL DEFAULT '',
			runtime_graceful_stop_at timestamptz,
			recovery_state text NOT NULL DEFAULT '',
			recovery_reason text NOT NULL DEFAULT '',
			recovery_event_history jsonb NOT NULL DEFAULT '[]'::jsonb,
			execution_mode text NOT NULL DEFAULT 'work',
			filesystem_mode text NOT NULL DEFAULT 'read_write'
		);
		CREATE TABLE thread_inbox_entries (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			org_id uuid NOT NULL REFERENCES organizations(id),
			session_id uuid NOT NULL REFERENCES sessions(id),
			thread_id uuid NOT NULL REFERENCES session_threads(id),
			sequence_no bigint NOT NULL,
			message_id bigint,
			client_message_id text,
			entry_type text NOT NULL,
			payload jsonb NOT NULL DEFAULT '{}'::jsonb,
			delivery_state text NOT NULL,
			delivery_attempts integer NOT NULL DEFAULT 0,
			last_error text,
			owner_node_id text,
			runtime_id uuid,
			accepted_at timestamptz NOT NULL DEFAULT now(),
			delivered_at timestamptz,
			acked_at timestamptz,
			applied_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX thread_inbox_entries_client_message_unique
			ON thread_inbox_entries (org_id, thread_id, client_message_id)
			WHERE client_message_id IS NOT NULL;`)
	require.NoError(t, err, "test should create the minimal thread archival schema")
	return pool
}

func seedThreadArchivalPostgresThread(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	siblingThreadID := uuid.New()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, orgID)
	require.NoError(t, err, "test should seed an organization")
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id, org_id) VALUES ($1, $2)`, sessionID, orgID)
	require.NoError(t, err, "test should seed a session")
	_, err = pool.Exec(ctx, `
		INSERT INTO session_threads (id, session_id, org_id, status, label)
		VALUES
			($1, $2, $3, 'completed', 'Target thread'),
			($4, $2, $3, 'completed', 'Visible sibling')`,
		threadID, sessionID, orgID, siblingThreadID)
	require.NoError(t, err, "test should seed two visible completed threads so archiving remains allowed absent queued work")

	return orgID, sessionID, threadID
}
