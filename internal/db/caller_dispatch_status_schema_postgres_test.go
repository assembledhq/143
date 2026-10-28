package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// One parallel root owns a latest-schema disposable database with three
// checkouts. DDL/control subcases deliberately run sequentially. Ordinary cases
// roll back completely; the final lock schedules explicitly commit synthetic
// transactions. These are raw-SQL boundary proofs: they do not advance
// the migration driver's ledger or prove built-CLI dirty/version handling.
func TestCallerDispatchStatusSchemaPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
	require.Equal(t, int32(3), pool.Config().MaxConns, "status DDL fixtures must retain the explicit three-checkout budget")
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgxpool.Pool)
	}{
		{name: "latest readiness remains passive", run: callerDispatchStatusPGPassive},
		{name: "readiness rejects catalog corruption", run: callerDispatchStatusPGReadiness},
		{name: "participation and actual Prepare require new readiness", run: callerDispatchStatusPGParticipation},
		{name: "390 refuses already enabled old schema", run: callerDispatchStatusPGEnabledEntry},
		{name: "highest down retains new status independently of due work", run: callerDispatchStatusPGHighestRefusal},
		{name: "allowance down rechecks after highest gate", run: callerDispatchStatusPGLowestRefusal},
		{name: "raw 392 389 392 restores exact original catalog and rows", run: callerDispatchStatusPGRoundTrip},
		{name: "highest down first blocks metadata writer", run: callerDispatchStatusPGDownFirst},
		// Last: this schedule deliberately commits retained new-code history.
		// The disposable database is dropped by its fixture owner, never erased
		// or reclassified just to permit another down migration.
		{name: "metadata writer first makes highest down refuse", run: callerDispatchStatusPGWriterFirst},
	}
	for _, tt := range tests {
		if !t.Run(tt.name, func(t *testing.T) {
			caseCtx, caseCancel := context.WithTimeout(ctx, 45*time.Second)
			defer caseCancel()
			tt.run(t, caseCtx, pool)
		}) {
			return
		}
	}
}

type callerDispatchStatusPGSnapshot struct {
	rows    []json.RawMessage
	control json.RawMessage
	catalog json.RawMessage
	ledger  json.RawMessage
}

func callerDispatchStatusPGSnapshotAt(t *testing.T, ctx context.Context, q DBTX, org uuid.UUID) callerDispatchStatusPGSnapshot {
	t.Helper()
	var out callerDispatchStatusPGSnapshot
	// Every column of each affected business relation is retained; no clock,
	// metadata classification, original payload or caller linkage is projected out.
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
		require.NoError(t, q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` r WHERE `+filter, org).Scan(&row), "retain every field of the exact synthetic tenant's affected business rows")
		out.rows = append(out.rows, row)
	}
	require.NoError(t, q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY singleton),'[]'::jsonb) FROM caller_send_intent_control c`).Scan(&out.control), "retain all control columns and their exact database timestamps")
	require.NoError(t, q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY version),'[]'::jsonb) FROM schema_migrations m`).Scan(&out.ledger), "retain the real fixture ledger without pretending raw SQL advances it")
	require.NoError(t, q.QueryRow(ctx, `SELECT jsonb_build_object(
'columns',(SELECT jsonb_agg(jsonb_build_array(table_name,column_name,data_type,is_nullable,column_default) ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('code_review_session_metadata','code_review_agent_results','session_review_loops','session_review_loop_passes','review_loop_start_intents','caller_send_command_stamps','caller_send_intent_control','automation_runs','session_publications')),
'constraints',(SELECT jsonb_agg(jsonb_build_array(c.conrelid::regclass::text,c.conname,pg_get_constraintdef(c.oid),c.convalidated,c.condeferrable,c.condeferred) ORDER BY c.conrelid::regclass::text,c.conname) FROM pg_constraint c WHERE c.connamespace='public'::regnamespace AND c.conrelid IN ('code_review_session_metadata'::regclass,'code_review_agent_results'::regclass,'session_review_loops'::regclass,'session_review_loop_passes'::regclass,'review_loop_start_intents'::regclass,'caller_send_command_stamps'::regclass,'caller_send_intent_control'::regclass,'automation_runs'::regclass,'session_publications'::regclass)),
'indexes',(SELECT jsonb_agg(jsonb_build_array(ix.relname,pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive,i.indisunique) ORDER BY ix.relname) FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid WHERE ix.relnamespace='public'::regnamespace),
'triggers',(SELECT jsonb_agg(jsonb_build_array(t.tgrelid::regclass::text,t.tgname,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY t.tgrelid::regclass::text,t.tgname) FROM pg_trigger t WHERE NOT t.tgisinternal AND t.tgrelid IN ('code_review_session_metadata'::regclass,'code_review_agent_results'::regclass,'session_review_loops'::regclass,'session_review_loop_passes'::regclass,'review_loop_start_intents'::regclass,'caller_send_command_stamps'::regclass,'automation_runs'::regclass,'session_publications'::regclass)),
'functions',(SELECT jsonb_agg(jsonb_build_array(p.proname,pg_get_function_identity_arguments(p.oid),pg_get_functiondef(p.oid)) ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)) FROM pg_proc p WHERE p.pronamespace='public'::regnamespace AND p.proname LIKE 'caller143\_%' ESCAPE '\' AND p.prokind='f'))`).Scan(&out.catalog), "retain complete affected columns, CHECK definitions/validation, trigger functions, and all preexisting public indexes")
	return out
}

func callerDispatchStatusPGFlags(t *testing.T, ctx context.Context, q DBTX, want [3]bool) {
	t.Helper()
	var actual [3]bool
	require.NoError(t, q.QueryRow(ctx, `SELECT schema_ready,dispatch_status_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&actual[0], &actual[1], &actual[2]), "read all three actual readiness and participation flags")
	require.Equal(t, want, actual, "old readiness, new readiness and explicit participation must remain separate gates")
}

func callerDispatchStatusPGSQLState(t *testing.T, err error, code, message string) {
	t.Helper()
	var cause *pgconn.PgError
	require.ErrorAs(t, err, &cause, "the refusal must come from actual PostgreSQL DDL or the installed function")
	require.Equal(t, code, cause.Code, "the actual database refusal must retain its stable SQLSTATE")
	require.Equal(t, message, cause.Message, "the actual refusal must distinguish retained status from ordinary unresolved work")
}

func callerDispatchStatusPGApply(t *testing.T, ctx context.Context, tx pgx.Tx, version int, direction string) {
	t.Helper()
	_, err := tx.Exec(ctx, callerDispatchStatusMigrationSQL(t, version, direction))
	require.NoError(t, err, "apply actual raw status migration %d %s without forcing or changing the driver ledger", version, direction)
}

func callerDispatchStatusPGPassive(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	callerDispatchStatusPGFlags(t, ctx, pool, [3]bool{true, true, false})
	var actual json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_array(
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_v2_check' AND convalidated),
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_check'),
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='caller_send_intent_control'::regclass AND conname='caller_send_dispatch_status_enablement_check' AND convalidated))`).Scan(&actual), "read the distinct validated widened allowance and the new participation CHECK from the real catalog")
	require.JSONEq(t, `[true,false,true]`, string(actual), "latest readiness must positively identify the widened CHECK and absence of its old name")
	org, _ := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	_, err := tx.Exec(ctx, `SELECT caller143_require_send_intents_enabled()`)
	callerDispatchStatusPGSQLState(t, err, "55000", "caller_send_intents_disabled")
	require.NoError(t, tx.Rollback(ctx), "end the refused passive-participation transaction before independent readback")
	require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, pool, org), "the latest passive schema must refuse writes without changing rows, control, catalog or ledger")
}

func callerDispatchStatusPGReadiness(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	tests := []struct {
		name     string
		mutation string
	}{
		{name: "new allowance missing", mutation: `ALTER TABLE code_review_session_metadata DROP CONSTRAINT code_review_session_metadata_status_code_v2_check`},
		{name: "new allowance not validated", mutation: `ALTER TABLE code_review_session_metadata DROP CONSTRAINT code_review_session_metadata_status_code_v2_check; ALTER TABLE code_review_session_metadata ADD CONSTRAINT code_review_session_metadata_status_code_v2_check CHECK(status_code IS NULL OR status_code IN ('github_rate_limited','github_unavailable','reviewer_failed','worker_failed','review_loop_detected','code_reviewer_dispatch_unresolved')) NOT VALID`},
		{name: "old allowance name still present", mutation: `ALTER TABLE code_review_session_metadata ADD CONSTRAINT code_review_session_metadata_status_code_check CHECK(status_code IS NULL OR status_code IN ('github_rate_limited','github_unavailable','reviewer_failed','worker_failed','review_loop_detected'))`},
		{name: "new enablement CHECK missing", mutation: `ALTER TABLE caller_send_intent_control DROP CONSTRAINT caller_send_dispatch_status_enablement_check`},
		{name: "old readiness false", mutation: `UPDATE caller_send_intent_control SET schema_ready=false WHERE singleton`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential: every probe changes shared schema/control within its own outer rollback.
			org, _ := codeReviewDispatchPreparePGFixture(t, ctx, pool)
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			callerDispatchStatusPGApply(t, ctx, tx, 392, "down")
			_, err := tx.Exec(ctx, tt.mutation)
			require.NoError(t, err, "install only this isolated catalog/control corruption before the readiness probe")
			before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
			err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, execErr := probe.Exec(ctx, callerDispatchStatusMigrationSQL(t, 392, "up"))
				return execErr
			})
			callerDispatchStatusPGSQLState(t, err, "55000", "caller_dispatch_status_schema_not_ready")
			require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "refused readiness must preserve every corrupted catalog field, complete row and control timestamp exactly")
			require.NoError(t, tx.Rollback(ctx), "remove only this isolated corruption by ordinary transaction rollback")
			callerDispatchStatusPGFlags(t, ctx, pool, [3]bool{true, true, false})
		})
	}
}

func callerDispatchStatusPGParticipation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	callerDispatchStatusPGApply(t, ctx, tx, 392, "down")
	callerDispatchStatusPGFlags(t, ctx, tx, [3]bool{true, false, false})
	err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
		_, execErr := probe.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton`)
		return execErr
	})
	var cause *pgconn.PgError
	require.ErrorAs(t, err, &cause, "the new enablement invariant must reject old-ready/new-not-ready participation in actual PostgreSQL")
	require.Equal(t, "23514", cause.Code, "participation without new readiness must violate a real CHECK")
	require.Equal(t, "caller_send_dispatch_status_enablement_check", cause.ConstraintName, "the new readiness invariant must own this refusal")
	// This state is otherwise impossible. Drop only the isolated CHECK to model
	// catalog corruption and independently prove the installed function and actual
	// Prepare enabled CTE both reject old=true/new=false/enabled=true.
	_, err = tx.Exec(ctx, `ALTER TABLE caller_send_intent_control DROP CONSTRAINT caller_send_dispatch_status_enablement_check; UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton`)
	require.NoError(t, err, "construct explicitly labelled isolated constraint corruption without disabling real owner triggers")
	before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
		_, execErr := probe.Exec(ctx, `SELECT caller143_require_send_intents_enabled()`)
		return execErr
	})
	callerDispatchStatusPGSQLState(t, err, "55000", "caller_send_intents_disabled")
	actual, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationDisabled, "actual Prepare must reject the new readiness bypass before any result or wake insert reaches a trigger")
	require.Equal(t, PreparedCodeReviewDispatch{}, actual, "a disabled Prepare cannot expose a provisional result or candidate wake")
	require.Equal(t, [2]int{}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "the actual disabled preparation statement must create neither its result nor its original wake")
	require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "both independent participation guards must preserve complete rows, corrupted catalog, control and ledger")
	require.NoError(t, tx.Rollback(ctx), "discard isolated corruption before proving the normal explicitly enabled fixture")
	tx = callerSendStampPGBegin(t, ctx, pool, false)
	_, err = tx.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "explicitly enable only this latest-schema synthetic transaction after both readiness gates")
	actual, err = PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.NoError(t, err, "actual Prepare must install a fresh pending owner when all three flags permit it")
	require.Equal(t, in.ResultID, actual.Result().ID, "positive participation must return the exact caller-preminted result")
	require.True(t, actual.WakeInserted(), "positive participation must genuinely insert the original wake")
	require.Equal(t, [2]int{1, 1}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "positive participation must install exactly the owned result and wake inside its caller transaction")
	require.NoError(t, tx.Rollback(ctx), "rollback the synthetic positive installation without deleting retained evidence")
}

func callerDispatchStatusPGEnabledEntry(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, _ := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	for _, version := range []int{392, 391, 390} {
		callerDispatchStatusPGApply(t, ctx, tx, version, "down")
	}
	_, err := tx.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "model an explicitly enabled original-389 database before the appended interlock exists")
	before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
		_, execErr := probe.Exec(ctx, callerDispatchStatusMigrationSQL(t, 390, "up"))
		return execErr
	})
	callerDispatchStatusPGSQLState(t, err, "55000", "caller_send_intents_must_be_disabled_for_status_schema")
	require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "390 enabled-entry refusal must preserve the exact original catalog, old functions, complete control row and all business rows")
	require.NoError(t, tx.Rollback(ctx), "discard the synthetic old-schema participation setup by transaction rollback")
}

func callerDispatchStatusPGHighestRefusal(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	tests := []struct {
		name    string
		pending bool
	}{
		{name: "new status and actual pending dispatch", pending: true},
		{name: "new status without any due tuple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential: each real DDL refusal owns and rolls back the shared control state.
			org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			if tt.pending {
				_, err := tx.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
				require.NoError(t, err, "explicitly enable the isolated fixture before actual pending-owner preparation")
				_, err = PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
				require.NoError(t, err, "construct genuine unresolved work through the actual preparation statement")
			}
			_, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
			require.NoError(t, err, "retain the adopted business classification without erasure or remapping")
			var due int
			require.NoError(t, tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM code_review_agent_results WHERE dispatch_due_at IS NOT NULL)+(SELECT count(*) FROM session_review_loops WHERE startup_send_due_at IS NOT NULL)+(SELECT count(*) FROM review_loop_start_intents s WHERE caller143_start_unresolved(s.org_id,s.id))`).Scan(&due), "positively count all real unresolved rollback categories before the highest down")
			wantDue := 0
			if tt.pending {
				wantDue = 1
			}
			require.Equal(t, wantDue, due, "the no-due case must prove retained classification is independently sufficient while the pending case proves diagnostic priority")
			before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
			err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, execErr := probe.Exec(ctx, callerDispatchStatusMigrationSQL(t, 392, "down"))
				return execErr
			})
			callerDispatchStatusPGSQLState(t, err, "55000", "retained_dispatch_unresolved_status_prevents_rollback")
			require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "highest-first refusal must retain every business row, participation flag, CHECK/function/index and unchanged raw-SQL ledger")
			require.NoError(t, tx.Rollback(ctx), "remove only the uncommitted synthetic fixture by rolling back its owner transaction")
		})
	}
}

func callerDispatchStatusPGLowestRefusal(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	callerDispatchStatusPGApply(t, ctx, tx, 392, "down")
	callerDispatchStatusPGApply(t, ctx, tx, 391, "down")
	_, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "retain new-code history after a previously clean highest down to exercise the independent allowance-removal boundary")
	before := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
		_, execErr := probe.Exec(ctx, callerDispatchStatusMigrationSQL(t, 390, "down"))
		return execErr
	})
	callerDispatchStatusPGSQLState(t, err, "55000", "retained_dispatch_unresolved_status_prevents_rollback")
	require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "lower-boundary recheck must refuse before dropping the widened CHECK, replacing functions or removing the new flag")
	require.NoError(t, tx.Rollback(ctx), "restore the latest isolated fixture without erasing committed history")
}

func callerDispatchStatusPGOriginalFunction(t *testing.T, name string) string {
	t.Helper()
	original := callerSendSchemaSQL(t, "000384_caller_send_intents.up.sql")
	start := strings.Index(original, "CREATE FUNCTION "+name+"(")
	require.GreaterOrEqual(t, start, 0, "the exact original migration must own the function restored at 389")
	end := strings.Index(original[start:], "\n$$;")
	require.GreaterOrEqual(t, end, 0, "the exact original function must have a complete dollar-quoted definition")
	return strings.Replace(original[start:start+end+4], "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)
}

func callerDispatchStatusPGRoundTrip(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	_, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='review_loop_detected',status_message='Synthetic retained old classification' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "retain an old-only complete metadata row before exact schema restoration")
	_, err = tx.Exec(ctx, `UPDATE caller_send_intent_control SET updated_at=now() WHERE singleton`)
	require.NoError(t, err, "use the transaction's real stable clock for exact full-control-row round trips")
	latest := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	for _, version := range []int{392, 391, 390} {
		callerDispatchStatusPGApply(t, ctx, tx, version, "down")
	}
	original := callerDispatchStatusPGSnapshotAt(t, ctx, tx, org)
	// Reapply the original source, not a retyped expected CHECK or function.
	// Equal catalogs before/after prove 390 down restored the exact five-value
	// name/definition/validation and both original 384 function bodies.
	_, err = tx.Exec(ctx, callerSendSchemaSQL(t, "000299_code_review_loop_status.up.sql"))
	require.NoError(t, err, "the restored original status constraint name must remain compatible with its original owning migration")
	for _, name := range []string{"caller143_require_send_intents_enabled", "caller143_assert_send_rollback_safe"} {
		_, err = tx.Exec(ctx, callerDispatchStatusPGOriginalFunction(t, name))
		require.NoError(t, err, "reapply only the exact original function source to independently compare the restored catalog")
	}
	require.Equal(t, original, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "original-source CHECK and function reinstallation must make no catalog, validation or full-row difference at 389")
	for cycle := 0; cycle < 2; cycle++ {
		callerDispatchStatusPGApply(t, ctx, tx, 390, "up")
		callerDispatchStatusPGFlags(t, ctx, tx, [3]bool{true, false, false})
		var validated bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_v2_check'`).Scan(&validated), "read the real split-validation boundary immediately after allowance installation")
		require.False(t, validated, "390 must install the new allowance NOT VALID without granting readiness")
		callerDispatchStatusPGApply(t, ctx, tx, 391, "up")
		callerDispatchStatusPGFlags(t, ctx, tx, [3]bool{true, false, false})
		callerDispatchStatusPGApply(t, ctx, tx, 392, "up")
		callerDispatchStatusPGFlags(t, ctx, tx, [3]bool{true, true, false})
		require.Equal(t, latest, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "raw re-up must restore all complete business rows, three flags, validated catalog, original indexes and untouched ledger exactly")
		if cycle == 0 {
			for _, version := range []int{392, 391, 390} {
				callerDispatchStatusPGApply(t, ctx, tx, version, "down")
			}
			require.Equal(t, original, callerDispatchStatusPGSnapshotAt(t, ctx, tx, org), "repeated clean raw down must restore the same complete 389 catalog and rows without remapping business status")
		}
	}
	require.NoError(t, tx.Rollback(ctx), "discard only the raw-SQL test transaction while leaving the driver's genuine latest ledger unchanged")
}

func callerDispatchStatusPGWait(t *testing.T, ctx context.Context, observer *pgxpool.Conn, contender, holder uint32, query string, finished <-chan struct{}) {
	t.Helper()
	require.NotEqual(t, holder, contender, "the waiting statement must have a different actual PostgreSQL backend from its holder")
	for {
		select {
		case <-finished:
			require.FailNow(t, "the exact contending statement finished before its holder-specific lock wait was observed")
		case <-ctx.Done():
			require.FailNow(t, "the metadata lock wait must be proven within the finite case context")
		default:
		}
		var blocked bool
		require.NoError(t, observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid=$1::integer AND a.state='active' AND a.wait_event_type='Lock' AND a.query=$3 AND $2::integer=ANY(pg_blocking_pids(a.pid)))`, contender, holder, query).Scan(&blocked), "observe the exact PID, actual query text, PostgreSQL Lock wait and specific blocking backend")
		if blocked {
			return
		}
	}
}

func callerDispatchStatusPGConnections(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [3]*pgxpool.Conn {
	t.Helper()
	var conns [3]*pgxpool.Conn
	for i := range conns {
		conn, err := pool.Acquire(ctx)
		require.NoError(t, err, "reserve exactly one holder, one contender and one independent catalog observer")
		conns[i] = conn
		t.Cleanup(conn.Release)
	}
	require.NotEqual(t, conns[0].Conn().PgConn().PID(), conns[1].Conn().PgConn().PID(), "holder and contender must use different real database sessions")
	require.NotEqual(t, conns[1].Conn().PgConn().PID(), conns[2].Conn().PgConn().PID(), "the lock observer must remain independent of the contending session")
	return conns
}

func callerDispatchStatusPGRaceCleanup(t *testing.T, cancel context.CancelFunc, started *bool, finished <-chan struct{}, txs ...pgx.Tx) {
	t.Helper()
	// Registered before the goroutine starts. Cancel, join, then rollback before
	// releasing any checkout, including on an assertion failure during the wait.
	t.Cleanup(func() {
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if *started {
			select {
			case <-finished:
			case <-cleanupCtx.Done():
				t.Error("the cancelled contending PostgreSQL statement must join before its checkout is released")
				// Destroy the actual sessions on this failure path so a failed
				// cancellation cannot return an in-use connection to the pool.
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				for _, tx := range txs {
					if err := tx.Conn().Close(closeCtx); err != nil {
						t.Errorf("close the failed synthetic race session: %v", err)
					}
				}
				select {
				case <-finished:
				case <-closeCtx.Done():
					t.Error("the contending statement did not join after its actual sessions were closed")
				}
				return
			}
		}
		for _, tx := range txs {
			err := tx.Rollback(cleanupCtx)
			if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("release the synthetic race transaction without erasing retained history: %v", err)
			}
		}
	})
}

func callerDispatchStatusPGDownFirst(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	conns := callerDispatchStatusPGConnections(t, ctx, pool)
	down, err := conns[0].Begin(ctx)
	require.NoError(t, err, "own the actual highest-down transaction before the conditional metadata writer")
	writer, err := conns[1].Begin(ctx)
	require.NoError(t, err, "own a separate conditional metadata-writer transaction")
	raceCtx, raceCancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	started := false
	callerDispatchStatusPGRaceCleanup(t, raceCancel, &started, finished, down, writer)
	callerDispatchStatusPGApply(t, ctx, down, 392, "down")
	expected := callerDispatchStatusPGSnapshotAt(t, ctx, down, org)
	const query = `UPDATE code_review_session_metadata m SET status_code='code_reviewer_dispatch_unresolved' WHERE m.org_id=$1 AND m.id=$2 AND EXISTS(SELECT 1 FROM code_review_agent_results r WHERE r.org_id=m.org_id AND r.session_id=m.session_id AND r.dispatch_due_at IS NOT NULL)`
	type outcome struct {
		tag pgconn.CommandTag
		err error
	}
	result := make(chan outcome, 1)
	started = true
	go func() {
		tag, execErr := writer.Exec(raceCtx, query, org, in.MetadataID)
		result <- outcome{tag: tag, err: execErr}
		close(finished)
	}()
	callerDispatchStatusPGWait(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), conns[0].Conn().PgConn().PID(), query, finished)
	require.NoError(t, down.Commit(ctx), "commit highest-first disablement only after the exact metadata writer is observed blocked behind it")
	select {
	case actual := <-result:
		require.NoError(t, actual.err, "the metadata writer must resume after the committed highest down without a deadlock")
		require.Equal(t, int64(0), actual.tag.RowsAffected(), "the resumed conditional writer must not add the new classification without unresolved work")
	case <-ctx.Done():
		require.FailNow(t, "the blocked conditional metadata writer must complete within the finite case context")
	}
	require.NoError(t, writer.Commit(ctx), "settle the actual no-op metadata writer before independent complete-row readback")
	require.Equal(t, expected, callerDispatchStatusPGSnapshotAt(t, ctx, conns[2], org), "down-first ordering must preserve every business field and the exact committed disabled control/catalog state")
	callerDispatchStatusPGFlags(t, ctx, conns[2], [3]bool{true, false, false})
	// Restore only readiness in this no-history synthetic database. The raw
	// ledger remains unchanged, and no migration opts participation back in.
	restore, err := conns[2].Begin(ctx)
	require.NoError(t, err, "own a separate real transaction for passive readiness restoration")
	defer func() { _ = restore.Rollback(context.Background()) }()
	callerDispatchStatusPGApply(t, ctx, restore, 392, "up")
	require.NoError(t, restore.Commit(ctx), "restore the latest passive gate after the clean down-first schedule")
	callerDispatchStatusPGFlags(t, ctx, conns[2], [3]bool{true, true, false})
}

func callerDispatchStatusPGWriterFirst(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	conns := callerDispatchStatusPGConnections(t, ctx, pool)
	writer, err := conns[0].Begin(ctx)
	require.NoError(t, err, "own the real metadata writer before the highest rollback gate starts")
	down, err := conns[1].Begin(ctx)
	require.NoError(t, err, "own a separate real highest-down transaction")
	raceCtx, raceCancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	started := false
	callerDispatchStatusPGRaceCleanup(t, raceCancel, &started, finished, writer, down)
	_, err = writer.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "hold an actual uncommitted retained classification under the normal metadata-first writer lock")
	expected := callerDispatchStatusPGSnapshotAt(t, ctx, writer, org)
	query := callerDispatchStatusMigrationSQL(t, 392, "down")
	result := make(chan error, 1)
	started = true
	go func() {
		_, execErr := down.Exec(raceCtx, query)
		result <- execErr
		close(finished)
	}()
	callerDispatchStatusPGWait(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), conns[0].Conn().PgConn().PID(), query, finished)
	require.NoError(t, writer.Commit(ctx), "commit retained history only after the exact highest-down PID/query is proved waiting on this metadata writer")
	select {
	case actual := <-result:
		callerDispatchStatusPGSQLState(t, actual, "55000", "retained_dispatch_unresolved_status_prevents_rollback")
	case <-ctx.Done():
		require.FailNow(t, "highest down must finish its post-wait retained-history check within the finite case context")
	}
	require.NoError(t, down.Rollback(ctx), "settle the genuinely refused raw down before independent committed-state inspection")
	require.Equal(t, expected, callerDispatchStatusPGSnapshotAt(t, ctx, conns[2], org), "writer-first rollback refusal must retain the exact committed metadata history and every control/catalog/business field")
	callerDispatchStatusPGFlags(t, ctx, conns[2], [3]bool{true, true, false})
}
