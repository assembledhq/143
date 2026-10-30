package main

import (
	"context"
	"encoding/json"
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

//nolint:paralleltest // Sequential full-chain cases retain the fixture's three-client reservation through dirty-ledger cleanup.
func TestCallerSendIntentMigrationLockTimeoutPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, relation, mode string
		start, target              uint
	}{
		{name: "storage_384_up", file: "000384_caller_send_intents.up.sql", relation: "code_review_agent_results", mode: "AccessExclusiveLock", start: 383, target: 384},
		{name: "storage_384_down", file: "000384_caller_send_intents.down.sql", relation: "code_review_agent_results", mode: "AccessExclusiveLock", start: 384, target: 383},
		{name: "validation_388_up", file: "000388_caller_send_intent_validation.up.sql", relation: "session_review_loops", mode: "ShareRowExclusiveLock", start: 387, target: 388},
		{name: "validation_388_down", file: "000388_caller_send_intent_validation.down.sql", relation: "session_review_loops", mode: "AccessExclusiveLock", start: 388, target: 387},
		{name: "readiness_389_up", file: "000389_caller_send_intent_readiness.up.sql", relation: "caller_send_intent_control", mode: "ShareRowExclusiveLock", start: 388, target: 389},
		{name: "readiness_389_down", file: "000389_caller_send_intent_readiness.down.sql", relation: "caller_send_intent_control", mode: "ShareRowExclusiveLock", start: 389, target: 388},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each case owns admin+migrator+inspector. Reuse the inspector for
			// the holder and sequential lock observations, never a fourth client.
			migration, inspector := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			require.NoError(t, migration.Migrate(tt.start), "reach the exact clean predecessor through the real migration driver")
			requireCallerIntentMigrationLedger(t, ctx, migration, inspector, tt.start, false)
			f := seedCallerIntentMigrationLegacy(t, ctx, inspector)
			requireCallerIntentMigrationCatalog(t, ctx, inspector, tt.start, tt.start >= 388, tt.start >= 389, tt.start >= 389)
			before := callerIntentTimeoutSnapshotAt(t, ctx, inspector, f.org, tt.start)
			var migratorPIDs []int32
			var queryBytes int
			require.NoError(t, inspector.QueryRow(ctx, `SELECT COALESCE(array_agg(pid ORDER BY pid),ARRAY[]::integer[]) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND pid<>pg_backend_pid()`).Scan(&migratorPIDs), "identify the sole existing migration backend before starting the holder")
			require.Equal(t, 1, len(migratorPIDs), "the fixture must use only its existing migration and inspection connections in the generated database")
			require.NoError(t, inspector.QueryRow(ctx, `SELECT setting::integer FROM pg_settings WHERE name='track_activity_query_size'`).Scan(&queryBytes), "read the server's actual retained query-text capacity")
			require.Greater(t, queryBytes, 1, "the server must retain a usable migration query prefix")
			raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", tt.file))
			require.NoError(t, err, "read the exact repository migration for backend attribution without rewriting its timeout")
			holder, err := inspector.Begin(ctx)
			require.NoError(t, err, "reuse the existing inspector as the conflicting writer")
			finished := make(chan struct{})
			started := false
			callerDispatchStatusTimeoutCleanup(t, holder, &started, finished)
			switch tt.relation {
			case "code_review_agent_results":
				_, err = holder.Exec(ctx, `UPDATE code_review_agent_results SET status=status WHERE org_id=$1 AND id=$2`, f.org, f.legacyReviewer)
			case "session_review_loops":
				// RowExclusive permits the preceding VALIDATE statements but
				// blocks ADD FOREIGN KEY. Its timeout must undo those validations.
				_, err = holder.Exec(ctx, `UPDATE session_review_loops SET latest_summary=latest_summary WHERE org_id=$1 AND id=$2`, f.org, f.legacyLoop)
			case "caller_send_intent_control":
				// The pre-readiness table is empty. UPDATE still takes its normal
				// RowExclusive relation lock without inserting or enabling a row.
				_, err = holder.Exec(ctx, `UPDATE caller_send_intent_control SET updated_at=updated_at WHERE singleton`)
			}
			require.NoError(t, err, "hold an ordinary uncommitted writer lock without activating caller intents")
			var held bool
			require.NoError(t, holder.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='relation' AND relation=$1::regclass AND mode='RowExclusiveLock' AND granted)`, tt.relation).Scan(&held), "positively identify the holder's granted writer lock")
			require.True(t, held, "the real relation writer must be responsible for blocking migration DDL")
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
			callerMigrationLockTimeoutWait(t, wallCtx, holder, migratorPIDs[0], int32(inspector.PgConn().PID()), string(raw), queryBytes, tt.relation, tt.mode, finished)
			var actual callerDispatchStatusTimeoutOutcome
			select {
			case actual = <-result:
			case <-wallCtx.Done():
				require.FailNow(t, "the real five-second server lock timeout must finish before the independent twenty-second wall guard")
			}
			var migrationErr database.Error
			require.ErrorAs(t, actual.err, &migrationErr, "the real migration driver must return its failed DDL wrapper")
			var lockErr *pq.Error
			require.ErrorAs(t, migrationErr.OrigErr, &lockErr, "the driver must retain the actual PostgreSQL error")
			require.Equal(t, pq.ErrorCode("55P03"), lockErr.Code, "the held relation must exhaust the migration's server-side lock budget")
			require.Equal(t, "canceling statement due to lock timeout", lockErr.Message, "client cancellation, NOWAIT and statement timeout must not stand in for lock_timeout")
			require.GreaterOrEqual(t, actual.elapsed, 5*time.Second, "the unchanged migration must wait its full five-second lock budget")
			require.Less(t, actual.elapsed, 20*time.Second, "the server must abort before the independent wall guard expires")
			require.NoError(t, holder.Rollback(ctx), "release the synthetic blocker only after the driver has returned its real lock timeout")
			requireCallerIntentMigrationLedger(t, ctx, migration, inspector, tt.target, true)
			after := callerIntentTimeoutSnapshotAt(t, ctx, inspector, f.org, tt.start)
			require.Equal(t, before, after, "failed DDL must preserve all business rows, control flags, catalog definitions and validation state, including objects absent before storage installation")
			requireCallerIntentMigrationCatalog(t, ctx, inspector, tt.start, tt.start >= 388, tt.start >= 389, tt.start >= 389)
			requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, inspector, tt.target, tt.target)
			require.Equal(t, after, callerIntentTimeoutSnapshotAt(t, ctx, inspector, f.org, tt.start), "dirty-ledger diagnostics and refused retries must not alter retained state or enable participation")
		})
	}
}

type callerIntentTimeoutSnapshot struct {
	rows    []json.RawMessage
	catalog json.RawMessage
}

func callerIntentTimeoutSnapshotAt(t *testing.T, ctx context.Context, conn *pgx.Conn, org uuid.UUID, version uint) callerIntentTimeoutSnapshot {
	t.Helper()
	out := callerIntentTimeoutSnapshot{rows: callerIntentMigrationRows(t, ctx, conn, org, version < 384)}
	// Name-based catalog joins also work before 384, when the three caller
	// tables and every caller function must still be absent after rollback.
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
'columns',(SELECT jsonb_agg(jsonb_build_array(table_name,column_name,data_type,is_nullable,column_default) ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name=ANY($1::text[])),
'constraints',(SELECT jsonb_agg(jsonb_build_array(r.relname,c.conname,pg_get_constraintdef(c.oid),c.convalidated,c.condeferrable,c.condeferred) ORDER BY r.relname,c.conname) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE c.connamespace='public'::regnamespace AND r.relname=ANY($1::text[])),
'indexes',(SELECT jsonb_agg(jsonb_build_array(ix.relname,pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive) ORDER BY ix.relname) FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid WHERE ix.relnamespace='public'::regnamespace),
'triggers',(SELECT jsonb_agg(jsonb_build_array(r.relname,t.tgname,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY r.relname,t.tgname) FROM pg_trigger t JOIN pg_class r ON r.oid=t.tgrelid WHERE NOT t.tgisinternal AND r.relnamespace='public'::regnamespace AND r.relname=ANY($1::text[])),
'functions',(SELECT jsonb_agg(jsonb_build_array(p.proname,pg_get_function_identity_arguments(p.oid),pg_get_functiondef(p.oid)) ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)) FROM pg_proc p WHERE p.pronamespace='public'::regnamespace AND left(p.proname,10)='caller143_'))`,
		[]string{"code_review_agent_results", "session_review_loops", "session_review_loop_passes", "review_loop_start_intents", "caller_send_command_stamps", "caller_send_intent_control", "automation_runs", "session_publications"}).Scan(&out.catalog)
	require.NoError(t, err, "capture complete caller-schema definitions without assuming that storage installation has committed")
	return out
}
