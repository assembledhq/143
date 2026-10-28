package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// The existing helper owns admin+migrator+inspector, exactly three clients.
// Reuse inspector as both the metadata transaction holder and the sequential
// statistics reader; never dial a fourth observer or concurrently use a pgx.Conn.
//
//nolint:paralleltest // Sequential full-chain lock schedules retain the helper's three-client reservation through dirty-ledger cleanup.
func TestCallerDispatchStatusMigrationLockTimeoutPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		start, target, dirty   uint
		file, mode             string
		validated, statusReady bool
	}{
		{name: "highest_392_down", start: 392, target: 391, dirty: 391, file: "000392_caller_dispatch_status_readiness.down.sql", mode: "ShareRowExclusiveLock", validated: true, statusReady: true},
		{name: "lowest_390_down", start: 390, target: 389, dirty: 389, file: "000390_caller_dispatch_status_schema.down.sql", mode: "AccessExclusiveLock"},
		{name: "install_390_up", start: 389, target: 390, dirty: 390, file: "000390_caller_dispatch_status_schema.up.sql", mode: "AccessExclusiveLock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential full-chain cases keep the existing helper's three-client
			// reservation through dirty-ledger inspection and generated DB cleanup.
			migration, inspector := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			require.NoError(t, migration.Migrate(tt.start), "reach the exact real clean predecessor before holding metadata against the selected appended DDL")
			requireCallerIntentMigrationLedger(t, ctx, migration, inspector, tt.start, false)
			requireCallerDispatchStatusMigrationCatalog(t, ctx, inspector, tt.start, tt.validated, tt.statusReady)
			f := seedCallerIntentMigrationLegacy(t, ctx, inspector)
			metadataID := seedCallerDispatchStatusMigrationMetadata(t, ctx, inspector, f)
			before := callerDispatchStatusTimeoutSnapshotAt(t, ctx, inspector, f.org)
			var migratorPIDs []int32
			var queryBytes int
			require.NoError(t, inspector.QueryRow(ctx, `SELECT COALESCE(array_agg(pid ORDER BY pid),ARRAY[]::integer[]) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND pid<>pg_backend_pid()`).Scan(&migratorPIDs), "identify the one existing migrator backend in this generated database before creating a holder transaction")
			require.Equal(t, 1, len(migratorPIDs), "only the existing migration connection may accompany this generated database's inspector; no fourth observer is opened")
			migratorPID := migratorPIDs[0]
			require.NoError(t, inspector.QueryRow(ctx, `SELECT setting::integer FROM pg_settings WHERE name='track_activity_query_size'`).Scan(&queryBytes), "read the real server's activity-text capacity rather than assume long migrations are fully visible")
			require.Greater(t, queryBytes, 1, "the actual server must retain a usable finite query-text buffer")
			raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", tt.file))
			require.NoError(t, err, "read only the exact selected real migration source for positive backend/query attribution")
			holder, err := inspector.Begin(ctx)
			require.NoError(t, err, "reuse the existing inspector connection for the real long metadata holder")
			finished := make(chan struct{})
			started := false
			callerDispatchStatusTimeoutCleanup(t, holder, &started, finished)
			_, err = holder.Exec(ctx, `UPDATE code_review_session_metadata SET status_message='Synthetic metadata lock-timeout holder' WHERE org_id=$1 AND id=$2`, f.org, metadataID)
			require.NoError(t, err, "hold a normal uncommitted metadata UPDATE, not an artificial exclusive lock or a fourth connection")
			var hasMetadataLock bool
			require.NoError(t, holder.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='relation' AND relation='code_review_session_metadata'::regclass AND mode='RowExclusiveLock' AND granted)`).Scan(&hasMetadataLock), "positively verify the holder's real metadata RowExclusive relation lock before starting migration")
			require.True(t, hasMetadataLock, "the timeout must be caused by the actual metadata writer relation lock")
			wallCtx, wallCancel := context.WithTimeout(ctx, 20*time.Second)
			defer wallCancel()
			result := make(chan callerDispatchStatusTimeoutOutcome, 1)
			started = true
			began := time.Now()
			go func() {
				migrationErr := migration.Migrate(tt.target)
				result <- callerDispatchStatusTimeoutOutcome{err: migrationErr, elapsed: time.Since(began)}
				close(finished)
			}()
			callerDispatchStatusTimeoutWait(t, wallCtx, holder, migratorPID, int32(inspector.PgConn().PID()), string(raw), queryBytes, tt.mode, finished)
			var actual callerDispatchStatusTimeoutOutcome
			select {
			case actual = <-result:
			case <-wallCtx.Done():
				require.FailNow(t, "the real migration's unchanged five-second DDL lock_timeout must return before the independent twenty-second wall guard")
			}
			// The normal path keeps metadata held until the actual driver has
			// returned. No shortened lock_timeout, NOWAIT, statement timeout,
			// forged PostgreSQL error or client cancellation supplies this result.
			var migrationErr database.Error
			require.ErrorAs(t, actual.err, &migrationErr, "the real migration driver must retain its actual failed DDL wrapper")
			var lockErr *pq.Error
			require.ErrorAs(t, migrationErr.OrigErr, &lockErr, "the migration driver must preserve the real PostgreSQL timeout cause")
			require.Equal(t, pq.ErrorCode("55P03"), lockErr.Code, "the unchanged five-second server DDL budget must produce actual lock_not_available")
			require.Equal(t, "canceling statement due to lock timeout", lockErr.Message, "only the actual lock_timeout diagnostic proves this gate, not a deadlock, NOWAIT or client wall cancellation")
			require.GreaterOrEqual(t, actual.elapsed, 5*time.Second, "the actual driver must spend the unchanged full five-second DDL lock budget behind metadata")
			require.Less(t, actual.elapsed, 20*time.Second, "the server timeout must finish inside the independent test wall guard")
			require.NoError(t, holder.Rollback(ctx), "release the metadata holder only after the actual migration timeout has returned, restoring its full original business row")
			requireCallerIntentMigrationLedger(t, ctx, migration, inspector, tt.dirty, true)
			after := callerDispatchStatusTimeoutSnapshotAt(t, ctx, inspector, f.org)
			require.Equal(t, before.rows, after.rows, "failed transactional DDL and holder rollback must preserve every complete tenant business row")
			require.Equal(t, before.control, after.control, "timeout must preserve every readiness/participation field and exact control timestamp")
			require.Equal(t, before.catalog, after.catalog, "metadata lock timeout must leave all columns, CHECK definitions/validation, functions, triggers and existing concurrent indexes unchanged")
			expectedLedger, marshalErr := json.Marshal([]map[string]any{{"version": tt.dirty, "dirty": true}})
			require.NoError(t, marshalErr, "encode only the exact expected real dirty-ledger version and flag")
			require.JSONEq(t, string(expectedLedger), string(after.ledger), "the independent real schema_migrations snapshot must contain exactly the attempted target's dirty row")
			require.NotEqual(t, before.ledger, after.ledger, "the real driver must expose the failed attempt as dirty rather than leave the previous clean ledger unchanged")
			requireCallerDispatchStatusMigrationCatalog(t, ctx, inspector, tt.start, tt.validated, tt.statusReady)
			requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, inspector, tt.dirty, tt.target)
			require.Equal(t, after, callerDispatchStatusTimeoutSnapshotAt(t, ctx, inspector, f.org), "dirty diagnostics, known-repair refusal and ordinary retry refusal must preserve the exact failed ledger and complete retained state")
		})
	}
}

type callerDispatchStatusTimeoutOutcome struct {
	err     error
	elapsed time.Duration
}

type callerDispatchStatusTimeoutSnapshot struct {
	rows                     []json.RawMessage
	control, catalog, ledger json.RawMessage
}

func callerDispatchStatusTimeoutSnapshotAt(t *testing.T, ctx context.Context, conn *pgx.Conn, org uuid.UUID) callerDispatchStatusTimeoutSnapshot {
	t.Helper()
	var out callerDispatchStatusTimeoutSnapshot
	for _, table := range []string{
		"organizations", "sessions", "session_threads", "integrations", "repositories", "pull_requests", "code_review_policies",
		"code_review_session_metadata", "code_review_agent_results", "jobs", "session_review_loops", "session_review_loop_passes",
		"review_loop_start_intents", "caller_send_command_stamps", "automation_runs", "session_publications", "session_messages", "thread_inbox_entries",
	} {
		filter := "org_id=$1"
		if table == "organizations" {
			filter = "id=$1"
		}
		var row json.RawMessage
		require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` r WHERE `+filter, org).Scan(&row), "capture every field of the complete synthetic tenant business graph before and after failed real-driver DDL")
		out.rows = append(out.rows, row)
	}
	require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY singleton),'[]'::jsonb) FROM caller_send_intent_control c`).Scan(&out.control), "capture every actual global readiness/participation field and database timestamp")
	require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY version),'[]'::jsonb) FROM schema_migrations m`).Scan(&out.ledger), "capture the real driver-owned default ledger without writes, Force or synthetic history")
	out.catalog = callerIntentMigrationCompleteCatalog(t, ctx, conn)
	return out
}

func callerDispatchStatusTimeoutWait(t *testing.T, ctx context.Context, holder pgx.Tx, migrator, holderPID int32, query string, queryBytes int, mode string, finished <-chan struct{}) {
	t.Helper()
	require.NotEqual(t, holderPID, migrator, "the existing migrator must be distinct from the existing inspection/holder backend")
	if len(query) >= queryBytes {
		query = query[:queryBytes-1] // The three finite migration sources are ASCII.
	}
	for {
		select {
		case <-finished:
			require.FailNow(t, "observe the exact migrator metadata wait before accepting its eventual timeout")
		case <-ctx.Done():
			require.FailNow(t, "the real metadata wait must be positively attributed within the independent finite wall guard")
		default:
		}
		_, err := holder.Exec(ctx, `SELECT pg_stat_clear_snapshot()`)
		require.NoError(t, err, "refresh activity statistics inside the existing holder transaction before each sequential observation")
		var blocked bool
		require.NoError(t, holder.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid=$1::integer AND a.state='active' AND a.wait_event_type='Lock' AND a.query=$3 AND $2::integer=ANY(pg_blocking_pids(a.pid)) AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='relation' AND l.relation='code_review_session_metadata'::regclass AND l.mode=$4 AND NOT l.granted))`, migrator, holderPID, query, mode).Scan(&blocked), "use only sequential holder-connection queries to prove the real migrator PID, server-retained DDL text, metadata lock mode and exact blocker")
		if blocked {
			return
		}
	}
}

func callerDispatchStatusTimeoutCleanup(t *testing.T, holder pgx.Tx, started *bool, finished <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// On assertion/wall-guard failure, release our own blocker so even a
		// timeout-ablation can finish its one selected migration. Join before
		// the earlier fixture cleanup closes the migrator or drops its database.
		err := holder.Rollback(cleanupCtx)
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("release the owned synthetic metadata timeout holder: %v", err)
		}
		if *started {
			select {
			case <-finished:
			case <-cleanupCtx.Done():
				t.Error("the released real migration must join before driver and generated-database cleanup")
			}
		}
	})
}
