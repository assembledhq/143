package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

const callerIntentMigrationHighestVersion = uint(389)

type callerIntentMigrationIndex struct {
	version                 uint
	name, parent, predicate string
	columns                 []string
	unique                  bool
}

func callerIntentMigrationIndexes() []callerIntentMigrationIndex {
	return []callerIntentMigrationIndex{
		{version: 385, name: "idx_cr_agent_results_dispatch_due", parent: "code_review_agent_results", columns: []string{"dispatch_due_at", "id"}, predicate: "(dispatch_due_at IS NOT NULL)"},
		{version: 386, name: "idx_srl_startup_send_due", parent: "session_review_loops", columns: []string{"startup_send_due_at", "id"}, predicate: "(startup_send_due_at IS NOT NULL)"},
		{version: 387, name: "idx_srl_passes_org_session_loop_id", parent: "session_review_loop_passes", columns: []string{"org_id", "session_id", "loop_id", "id"}, unique: true},
	}
}

func TestCallerSendIntentMigrationRoundTripPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(383), "construct the exact clean predecessor through the real migration driver")
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	before := callerIntentMigrationRows(t, ctx, conn, f.org, true)
	legacyIndexes := callerIntentMigrationLegacyIndexes(t, ctx, conn)
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 383, false)
	requireCallerIntentMigrationCatalog(t, ctx, conn, 383, false, false, false)
	// Each boundary depends on this database's preceding committed DDL. These
	// steps cannot run in parallel or inside a caller-owned transaction because
	// the three standalone concurrent index files must commit independently.
	for _, target := range []uint{384, 385, 386, 387, 388, 389, 388, 387, 386, 385, 384, 383, 384, 385, 386, 387, 388, 389} {
		require.NoError(t, migration.Migrate(target), "complete only the selected real caller-intent migration boundary")
		requireCallerIntentMigrationLedger(t, ctx, migration, conn, target, false)
		// Down from 389 retains a disabled singleton and already-validated
		// checks. Removing 384 resets both for the next independent up chain.
		if target == 389 {
			f.sawReady = true
		} else if target == 383 {
			f.sawReady = false
		}
		requireCallerIntentMigrationCatalog(t, ctx, conn, target, f.sawReady || target >= 388, f.sawReady, target == 389)
		require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, true)), "all existing synthetic graph fields must survive each successful up/down boundary unchanged")
		require.Equal(t, legacyIndexes, callerIntentMigrationLegacyIndexes(t, ctx, conn), "every preexisting index definition must survive the caller-intent roundtrip")
	}
}

//nolint:paralleltest // Index/DDL subcases serialize the fixed three-client migration reservation and dirty-ledger teardown.
func TestCallerSendIntentStandaloneDirtyMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scenario string }{
		{name: "valid wrong definition conflicts", scenario: "wrong"},
		{name: "genuine invalid index conflicts", scenario: "invalid"},
		{name: "missing down target remains dirty", scenario: "down"},
	}
	// Each case owns a fresh complete database. Keep these DDL cases sequential
	// so the host's -parallel=1 reservation uses only the helper's three held
	// connections, including when the preceding database has a dirty ledger.
	for _, index := range callerIntentMigrationIndexes() {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%d/%s", index.version, tt.name), func(t *testing.T) {
				migration, conn := newContentMigrationPostgres(t, true)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				var conflictBefore json.RawMessage
				failedVersion := index.version
				if tt.scenario == "down" {
					require.NoError(t, migration.Migrate(index.version), "install the exact concurrent migration before its genuine missing-target down probe")
					_, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{index.name}.Sanitize())
					require.NoError(t, err, "remove only this generated database's exact index before testing the actual driver drop")
					requireAdmissionLookupMigrationError(t, migration.Steps(-1), "42704")
					failedVersion--
				} else {
					require.NoError(t, migration.Migrate(index.version-1), "construct this concurrent file's exact clean predecessor")
					_, err := conn.Exec(ctx, "CREATE TABLE caller_send_migration_index_probe(value integer NOT NULL)")
					require.NoError(t, err, "create only a synthetic wrong-parent index relation in this generated database")
					if tt.scenario == "invalid" {
						_, err = conn.Exec(ctx, "INSERT INTO caller_send_migration_index_probe VALUES(1),(1)")
						require.NoError(t, err, "seed real duplicate values before the failed concurrent unique build")
					}
					_, err = conn.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+pgx.Identifier{index.name}.Sanitize()+" ON caller_send_migration_index_probe(value)")
					if tt.scenario == "invalid" {
						var duplicate *pgconn.PgError
						require.True(t, errors.As(err, &duplicate), "the invalid index fixture must come from a real PostgreSQL error")
						require.Equal(t, "23505", duplicate.Code, "actual duplicate data must cause the invalid concurrent unique index")
					} else {
						require.NoError(t, err, "install a valid wrong-parent object with the real migration's exact name")
					}
					conflictBefore = callerIntentMigrationIndexObject(t, ctx, conn, index.name)
					var valid bool
					var parent string
					require.NoError(t, conn.QueryRow(ctx, `SELECT i.indisvalid,p.relname::text FROM pg_index i JOIN pg_class p ON p.oid=i.indrelid WHERE i.indexrelid=to_regclass('public.'||$1)`, index.name).Scan(&valid, &parent), "inspect the actual retained same-name index rather than fabricate catalog state")
					require.Equal(t, "caller_send_migration_index_probe", parent, "the conflicting name must belong only to the synthetic wrong-parent relation")
					require.Equal(t, tt.scenario != "invalid", valid, "valid and genuinely invalid conflicts must remain distinct fixtures")
					requireAdmissionLookupMigrationError(t, migration.Migrate(index.version), "42P07")
				}
				requireCallerIntentMigrationLedger(t, ctx, migration, conn, failedVersion, true)
				requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, conn, failedVersion, index.version)
				if tt.scenario != "down" {
					require.True(t, bytes.Equal(conflictBefore, callerIntentMigrationIndexObject(t, ctx, conn, index.name)), "failed migration and refused repair/retry must preserve the conflicting index's complete definition and validity")
				} else {
					var absent bool
					require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.'||$1) IS NULL`, index.name).Scan(&absent), "inspect the exact missing down target after refusal")
					require.True(t, absent, "failed down and dirty-state handling must not recreate an absent target")
				}
			})
		}
	}
}

func TestCallerSendIntentHighestRollbackFloorLedgerPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(callerIntentMigrationHighestVersion), "install the real validated highest caller-intent boundary before testing its first down")
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 389, false)
	requireCallerIntentMigrationCatalog(t, ctx, conn, 389, true, true, true)
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	seedCallerIntentMigrationPendingReviewer(t, ctx, conn, f)
	before := callerIntentMigrationRows(t, ctx, conn, f.org, false)
	catalog := callerIntentMigrationCompleteCatalog(t, ctx, conn)
	downErr := migration.Steps(-1)
	requireAdmissionLookupMigrationError(t, downErr, "55000")
	var migrationErr database.Error
	require.True(t, errors.As(downErr, &migrationErr), "the highest down must preserve the real driver's failing statement")
	var original *pq.Error
	require.True(t, errors.As(migrationErr.OrigErr, &original), "the highest down must retain the original PostgreSQL guard cause")
	require.Equal(t, "unresolved_caller_send_prevents_rollback", original.Message, "only the genuine unresolved-evidence guard may satisfy this highest-first refusal")
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 388, true)
	require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, false)), "the first failed down must preserve every retained row and the exact enabled control state")
	require.True(t, bytes.Equal(catalog, callerIntentMigrationCompleteCatalog(t, ctx, conn)), "highest rollback refusal must precede every FK/index/column removal and preserve the complete installed catalog")
	requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, conn, 388, 389)
	require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, false)), "refused dirty repair and retry must preserve all committed unresolved evidence")
	require.True(t, bytes.Equal(catalog, callerIntentMigrationCompleteCatalog(t, ctx, conn)), "diagnostic handling must not mutate the protected catalog")
}

func TestCallerSendIntentMigrationCommandPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for the actual caller-intent migration command")
	}
	binary := filepath.Join(t.TempDir(), "migrate")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	var buildOutput bytes.Buffer
	build.Stdout, build.Stderr = &buildOutput, &buildOutput
	if err := build.Run(); err != nil {
		t.Fatal("build the actual migration executable without publishing its captured output")
	}
	workingDirectory := callerIntentMigrationCommandSource(t)
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(383), "construct the actual clean predecessor ledger before invoking the real up command")
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	beforeLegacy := callerIntentMigrationRows(t, ctx, conn, f.org, true)
	conn = runCallerIntentMigrationCommand(t, ctx, binary, workingDirectory, "up", false, conn)
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 389, false)
	requireCallerIntentMigrationCatalog(t, ctx, conn, 389, true, true, true)
	require.True(t, callerIntentMigrationRowsEqual(beforeLegacy, callerIntentMigrationRows(t, ctx, conn, f.org, true)), "the actual binary up must preserve the existing legacy graph")
	seedCallerIntentMigrationPendingReviewer(t, ctx, conn, f)
	before := callerIntentMigrationRows(t, ctx, conn, f.org, false)
	catalog := callerIntentMigrationCompleteCatalog(t, ctx, conn)
	// A positively committed unresolved reviewer forces the very first 389
	// down to refuse. Never invoke unguarded all-schema down below this slice.
	conn = runCallerIntentMigrationCommand(t, ctx, binary, workingDirectory, "down", true, conn)
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 388, true)
	require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, false)), "the actual binary's failed down must preserve committed unresolved evidence and control flags")
	require.True(t, bytes.Equal(catalog, callerIntentMigrationCompleteCatalog(t, ctx, conn)), "the actual binary must stop before the first reverse FK or concurrent index drop")
	requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, conn, 388, 389)
}

type callerIntentMigrationFixture struct {
	org, session, thread, legacyReviewer, legacyLoop, legacyPass uuid.UUID
	sawReady                                                     bool
}

func seedCallerIntentMigrationLegacy(t *testing.T, ctx context.Context, conn *pgx.Conn) callerIntentMigrationFixture {
	t.Helper()
	f := callerIntentMigrationFixture{org: uuid.New(), legacyReviewer: uuid.New(), legacyLoop: uuid.New(), legacyPass: uuid.New()}
	_, err := conn.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic caller migration tenant')`, f.org)
	require.NoError(t, err, "seed only a synthetic tenant in the independently generated migration database")
	session := models.Session{OrgID: f.org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow}
	require.NoError(t, db.NewSessionStore(conn).Create(ctx, &session), "use the real session writer and its same-connection transaction for the legacy graph")
	require.NotNil(t, session.PrimaryThreadID, "the actual session writer must create a concrete legacy primary thread")
	f.session, f.thread = session.ID, *session.PrimaryThreadID
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,structured_result) VALUES($1,$2,$3,'codex','reviewer','completed','{"synthetic_legacy_int64":9223372036854775807}')`, f.legacyReviewer, f.org, f.session)
	batch.Queue(`INSERT INTO session_review_loops(id,org_id,session_id,thread_id,status,source,agent_type,max_passes,latest_summary) VALUES($1,$2,$3,$4,'failed','manual','codex',1,'Synthetic legacy review')`, f.legacyLoop, f.org, f.session, f.thread)
	batch.Queue(`INSERT INTO session_review_loop_passes(id,org_id,session_id,loop_id,pass_index,status,summary) VALUES($1,$2,$3,$4,1,'failed','Synthetic legacy pass')`, f.legacyPass, f.org, f.session, f.legacyLoop)
	require.NoError(t, conn.SendBatch(ctx, batch).Close(), "commit actual old all-NULL reviewer/loop/pass rows without manufacturing caller intent")
	return f
}

func seedCallerIntentMigrationPendingReviewer(t *testing.T, ctx context.Context, conn *pgx.Conn, f callerIntentMigrationFixture) {
	t.Helper()
	_, err := conn.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "explicitly opt in only this disposable fixture before installing a genuine rollback guard branch")
	owner := uuid.New()
	key := "cr-dispatch:v1:" + owner.String()
	// This is the migration's minimal SQL scope shape, not a full caller codec
	// command or evidence of any actual send, runtime claim, or publication.
	input, err := json.Marshal(map[string]any{"version": 1, "purpose": "code-review-reviewer-dispatch", "org_id": f.org, "session_id": f.session, "thread_id": f.thread, "client_message_id": key})
	require.NoError(t, err, "encode a finite synthetic SQL scope fixture without live operational data")
	wake, err := json.Marshal(map[string]any{"job_type": "run_code_review", "queue": "agent", "dedupe_key": "synthetic-migration-wake", "payload": map[string]any{"org_id": f.org, "session_id": f.session}})
	require.NoError(t, err, "encode the retained synthetic wake without enqueuing a real job")
	stamp := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	_, err = conn.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,
dispatch_version,dispatch_key,dispatch_input,dispatch_digest,dispatch_thread_id,dispatch_state,
dispatch_attempt_seq,dispatch_due_at,dispatch_writer_seq,dispatch_updated_at,dispatch_wake)
VALUES($1,$2,$3,'codex','reviewer','queued',1,$4,$5,repeat('a',64),$6,'pending',0,$7,0,$7,$8)`, owner, f.org, f.session, key, string(input), f.thread, stamp, string(wake))
	require.NoError(t, err, "commit a complete independently visible pending reviewer rather than hold uncommitted evidence across migration DDL")
	var pending int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='pending' AND dispatch_due_at IS NOT NULL`, f.org, f.session, owner).Scan(&pending), "independently verify the exact committed unresolved guard branch before invoking down")
	require.Equal(t, 1, pending, "the highest-first refusal fixture must positively contain one complete unresolved caller")
}

func requireCallerIntentMigrationLedger(t *testing.T, ctx context.Context, migration *migrate.Migrate, conn *pgx.Conn, version uint, dirty bool) {
	t.Helper()
	actualVersion, actualDirty, err := migration.Version()
	require.NoError(t, err, "the actual migration driver must expose its current ledger after successful or failed DDL")
	require.Equal(t, version, actualVersion, "the driver must retain the exact requested or attempted predecessor boundary")
	require.Equal(t, dirty, actualDirty, "only successful driver completion may clear the dirty state")
	type ledger struct {
		Version int64
		Dirty   bool
	}
	rows, err := conn.Query(ctx, `SELECT version,dirty FROM schema_migrations ORDER BY version`)
	require.NoError(t, err, "inspect the real default schema_migrations table independently of the migration driver's Version method")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledger, error) {
		var value ledger
		err := row.Scan(&value.Version, &value.Dirty)
		return value, err
	})
	require.NoError(t, err, "collect every actual default-ledger row without using a synthetic history table")
	require.Equal(t, []ledger{{Version: int64(version), Dirty: dirty}}, actual, "the independent real ledger must contain exactly the same single version and dirty flag")
}

func requireCallerIntentMigrationDirtyRefusals(t *testing.T, ctx context.Context, migration *migrate.Migrate, conn *pgx.Conn, version, retryTarget uint) {
	t.Helper()
	recorder := &lifecycleReferenceDirtyRepairRecorder{Migrate: migration}
	repairedVersion, repaired, err := repairKnownDirtyMigration(recorder)
	require.EqualError(t, err, fmt.Sprintf("database is dirty at version %d; refusing repair because only versions 267 and 281 are allowlisted", version), "caller-intent migration failures must never join the historical automatic repair allowlist")
	require.Zero(t, repairedVersion, "refused repair must not fabricate a successful version receipt")
	require.False(t, repaired, "known-dirty repair must refuse the new caller-intent boundary")
	require.Zero(t, recorder.forceCalls, "refused repair must never Force the real dirty migration ledger")
	var retry migrate.ErrDirty
	require.True(t, errors.As(migration.Migrate(retryTarget), &retry), "ordinary retry must refuse a dirty ledger instead of executing more DDL")
	require.Equal(t, int(version), retry.Version, "ordinary retry must identify the same exact dirty boundary")
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, version, true)
}

func callerIntentMigrationPrivateColumns(prefix string) []string {
	suffixes := []string{"version", "key", "input", "digest", "thread_id", "state", "attempt_id", "attempt_seq", "attempted_at", "due_at", "observe_deadline", "attention_at", "observed_at", "message_id", "resolution", "detail", "writer_seq", "updated_at"}
	if prefix == "dispatch_" {
		suffixes = append(suffixes, "wake")
	} else {
		suffixes = append(suffixes, "pass_id")
	}
	columns := make([]string, len(suffixes))
	for i, suffix := range suffixes {
		columns[i] = prefix + suffix
	}
	sort.Strings(columns)
	return columns
}

func callerIntentMigrationRows(t *testing.T, ctx context.Context, conn *pgx.Conn, org uuid.UUID, normalize bool) []json.RawMessage {
	t.Helper()
	tables := []string{"sessions", "session_threads", "code_review_session_metadata", "code_review_agent_results", "session_review_loops", "session_review_loop_passes", "automation_runs", "session_publications"}
	if !normalize {
		tables = append(tables, "review_loop_start_intents", "caller_send_command_stamps")
	}
	values := make([]json.RawMessage, 0, len(tables)+1)
	for _, table := range tables {
		remove := []string{}
		if normalize {
			switch table {
			case "code_review_agent_results":
				remove = callerIntentMigrationPrivateColumns("dispatch_")
			case "session_review_loops":
				remove = callerIntentMigrationPrivateColumns("startup_send_")
			case "automation_runs", "session_publications":
				remove = []string{"review_start_id"}
			}
		}
		var value json.RawMessage
		err := conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r)-$2::text[] ORDER BY (to_jsonb(r)-$2::text[])::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` r WHERE org_id=$1`, org, remove).Scan(&value)
		require.NoError(t, err, "capture all complete retained synthetic graph rows without float64 conversion")
		values = append(values, value)
	}
	if !normalize {
		var control json.RawMessage
		require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY singleton),'[]'::jsonb) FROM caller_send_intent_control c`).Scan(&control), "include the exact global control row in failed-migration preservation proof")
		values = append(values, control)
	}
	return values
}

func callerIntentMigrationRowsEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func callerIntentMigrationLegacyIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename||':'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='public' AND indexname NOT IN ('idx_cr_agent_results_dispatch_due','idx_srl_startup_send_due','idx_srl_passes_org_session_loop_id') AND tablename NOT IN ('caller_send_intent_control','review_loop_start_intents','caller_send_command_stamps') ORDER BY tablename,indexname`)
	require.NoError(t, err, "snapshot complete preexisting public index definitions independently of the new caller-intent relations")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect exact legacy index definitions rather than compare only object counts")
	return indexes
}

func callerIntentMigrationIndexObject(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, `SELECT jsonb_build_object('parent',p.relname,'definition',pg_get_indexdef(i.indexrelid),'valid',i.indisvalid,'ready',i.indisready,'live',i.indislive,'unique',i.indisunique) FROM pg_index i JOIN pg_class p ON p.oid=i.indrelid WHERE i.indexrelid=to_regclass('public.'||$1)`, name).Scan(&out), "snapshot the actual exact named index's parent definition and physical validity")
	return out
}

func callerIntentMigrationCompleteCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
'columns',(SELECT jsonb_agg(jsonb_build_array(table_name,column_name,data_type,is_nullable,column_default) ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('code_review_agent_results','session_review_loops','session_review_loop_passes','review_loop_start_intents','caller_send_command_stamps','caller_send_intent_control','automation_runs','session_publications')),
'constraints',(SELECT jsonb_agg(jsonb_build_array(c.conrelid::regclass::text,c.conname,pg_get_constraintdef(c.oid),c.convalidated,c.condeferrable,c.condeferred) ORDER BY c.conrelid::regclass::text,c.conname) FROM pg_constraint c WHERE c.connamespace='public'::regnamespace AND c.conrelid IN ('code_review_agent_results'::regclass,'session_review_loops'::regclass,'session_review_loop_passes'::regclass,'review_loop_start_intents'::regclass,'caller_send_command_stamps'::regclass,'caller_send_intent_control'::regclass,'automation_runs'::regclass,'session_publications'::regclass)),
'indexes',(SELECT jsonb_agg(jsonb_build_array(ix.relname,pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive) ORDER BY ix.relname) FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid WHERE ix.relnamespace='public'::regnamespace),
'triggers',(SELECT jsonb_agg(jsonb_build_array(t.tgrelid::regclass::text,t.tgname,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY t.tgrelid::regclass::text,t.tgname) FROM pg_trigger t WHERE NOT t.tgisinternal AND t.tgrelid IN ('code_review_agent_results'::regclass,'session_review_loops'::regclass,'session_review_loop_passes'::regclass,'review_loop_start_intents'::regclass,'caller_send_command_stamps'::regclass,'automation_runs'::regclass,'session_publications'::regclass)))`).Scan(&out)
	require.NoError(t, err, "retain complete installed column constraint trigger and index shape before the highest-first refusal")
	var statusCatalog json.RawMessage
	err = conn.QueryRow(ctx, `SELECT jsonb_build_object(
'columns',(SELECT jsonb_agg(jsonb_build_array(column_name,data_type,is_nullable,column_default) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name='code_review_session_metadata'),
'constraints',(SELECT jsonb_agg(jsonb_build_array(conname,pg_get_constraintdef(oid),convalidated,condeferrable,condeferred) ORDER BY conname) FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass),
'triggers',(SELECT jsonb_agg(jsonb_build_array(tgname,tgenabled,pg_get_triggerdef(oid)) ORDER BY tgname) FROM pg_trigger WHERE NOT tgisinternal AND tgrelid='code_review_session_metadata'::regclass),
'functions',(SELECT jsonb_agg(jsonb_build_array(p.proname,pg_get_function_identity_arguments(p.oid),pg_get_functiondef(p.oid)) ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)) FROM pg_proc p WHERE p.pronamespace='public'::regnamespace AND left(p.proname,10)='caller143_'))`).Scan(&statusCatalog)
	require.NoError(t, err, "include complete metadata schema and exact caller function definitions rather than omit the appended prerequisite")
	complete, err := json.Marshal([]json.RawMessage{out, statusCatalog})
	require.NoError(t, err, "retain the entire original catalog plus the independent status-schema catalog without losing precision")
	return complete
}

func requireCallerIntentMigrationCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint, validated, singleton, ready bool) {
	t.Helper()
	for _, table := range []string{"caller_send_intent_control", "review_loop_start_intents", "caller_send_command_stamps"} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.'||$1) IS NOT NULL`, table).Scan(&exists), "inspect the actual caller-intent storage relation at this committed boundary")
		require.Equal(t, version >= 384, exists, "only the storage migration may install or remove caller-intent relations")
	}
	for _, owner := range []struct{ table, prefix string }{{"code_review_agent_results", "dispatch_"}, {"session_review_loops", "startup_send_"}} {
		columns := callerIntentMigrationPrivateColumns(owner.prefix)
		rows, err := conn.Query(ctx, `SELECT column_name::text FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=ANY($2::text[]) ORDER BY column_name`, owner.table, columns)
		require.NoError(t, err, "inspect all nineteen private owner columns without relying on a model projection")
		actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "collect the exact installed private-column allowlist")
		expected := []string{}
		if version >= 384 {
			expected = columns
		}
		require.Equal(t, expected, actual, "storage down/up must remove and recreate exactly the nineteen passive owner columns")
	}
	type indexShape struct {
		Name, Parent, Method                                 string
		Columns                                              []string
		Options                                              []int16
		KeyCount, AttributeCount                             int
		Unique, Valid, Ready, Live, Immediate, NoExpressions bool
		Predicate                                            string
	}
	rows, err := conn.Query(ctx, `SELECT ix.relname::text,p.relname::text,a.amname::text,ARRAY(SELECT att.attname::text FROM unnest(i.indkey::smallint[]) WITH ORDINALITY k(num,pos) JOIN pg_attribute att ON att.attrelid=i.indrelid AND att.attnum=k.num ORDER BY k.pos),i.indoption::smallint[],i.indnkeyatts::int,i.indnatts::int,i.indisunique,i.indisvalid,i.indisready,i.indislive,i.indimmediate,i.indexprs IS NULL,COALESCE(pg_get_expr(i.indpred,i.indrelid),'') FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_class p ON p.oid=i.indrelid JOIN pg_am a ON a.oid=ix.relam WHERE ix.relnamespace='public'::regnamespace AND ix.relname IN ('idx_cr_agent_results_dispatch_due','idx_srl_startup_send_due','idx_srl_passes_org_session_loop_id') ORDER BY ix.relname`)
	require.NoError(t, err, "inspect every physical property of the actual independently committed concurrent indexes")
	actualIndexes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (indexShape, error) {
		var value indexShape
		err := row.Scan(&value.Name, &value.Parent, &value.Method, &value.Columns, &value.Options, &value.KeyCount, &value.AttributeCount, &value.Unique, &value.Valid, &value.Ready, &value.Live, &value.Immediate, &value.NoExpressions, &value.Predicate)
		return value, err
	})
	require.NoError(t, err, "collect complete concurrent index definitions including key order predicate and absence of INCLUDE expressions")
	expectedIndexes := []indexShape{}
	for _, index := range callerIntentMigrationIndexes() {
		if version >= index.version {
			expectedIndexes = append(expectedIndexes, indexShape{Name: index.name, Parent: index.parent, Method: "btree", Columns: index.columns, Options: make([]int16, len(index.columns)), KeyCount: len(index.columns), AttributeCount: len(index.columns), Unique: index.unique, Valid: true, Ready: true, Live: true, Immediate: true, NoExpressions: true, Predicate: index.predicate})
		}
	}
	sort.Slice(expectedIndexes, func(i, j int) bool { return expectedIndexes[i].Name < expectedIndexes[j].Name })
	require.Equal(t, expectedIndexes, actualIndexes, "each boundary must expose exactly the correct ready valid live concurrent indexes")
	var passFK bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='session_review_loops'::regclass AND conname='fk_srl_startup_pass' AND convalidated AND condeferrable AND condeferred AND confrelid='session_review_loop_passes'::regclass)`).Scan(&passFK), "inspect the exact validated deferred reverse startup-pass FK")
	require.Equal(t, version >= 388, passFK, "validation up/down must add or remove the reverse startup-pass FK independently of its concurrent key")
	if version < 384 {
		return
	}
	var retained [6]int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents),(SELECT count(*) FROM caller_send_command_stamps),(SELECT count(*) FROM code_review_agent_results WHERE dispatch_version IS NOT NULL),(SELECT count(*) FROM session_review_loops WHERE startup_send_version IS NOT NULL),(SELECT count(*) FROM automation_runs WHERE review_start_id IS NOT NULL),(SELECT count(*) FROM session_publications WHERE review_start_id IS NOT NULL)`).Scan(&retained[0], &retained[1], &retained[2], &retained[3], &retained[4], &retained[5]), "inspect every passive owner/start/stamp/caller slot before any explicit fixture opt-in")
	require.Equal(t, [6]int64{}, retained, "migration application and clean roundtrips must never infer retained commands or caller bindings")
	var total, valid int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*)::int,count(*) FILTER(WHERE convalidated)::int FROM pg_constraint WHERE connamespace='public'::regnamespace AND conname IN ('chk_cra_dispatch_send','fk_cra_dispatch_thread','chk_srl_startup_send','fk_srl_startup_thread','chk_review_loop_start_intent','chk_caller_send_stamp','fk_automation_review_start','fk_publication_review_start')`).Scan(&total, &valid), "inspect all independently validated caller-intent checks and scoped references")
	require.Equal(t, 8, total, "the storage boundary must install every validation prerequisite without inference")
	expectedValid := 0
	if validated {
		expectedValid = 8
	}
	require.Equal(t, expectedValid, valid, "constraint validation is monotone until storage removal rather than being falsely reset by validation down")
	type control struct{ Ready, Enabled bool }
	controlRows, err := conn.Query(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control ORDER BY singleton`)
	require.NoError(t, err, "read the actual interlock row without assuming storage creation also creates readiness")
	actualControl, err := pgx.CollectRows(controlRows, func(row pgx.CollectableRow) (control, error) {
		var value control
		err := row.Scan(&value.Ready, &value.Enabled)
		return value, err
	})
	require.NoError(t, err, "collect the complete passive readiness singleton")
	expectedControl := []control{}
	if singleton {
		expectedControl = append(expectedControl, control{Ready: ready, Enabled: false})
	}
	require.Equal(t, expectedControl, actualControl, "the highest gate must be ready only after up and never implicitly enable intent writes")
}

func callerIntentMigrationCommandSource(t *testing.T) string {
	t.Helper()
	return callerIntentMigrationCommandSourceThrough(t, callerIntentMigrationHighestVersion)
}

func callerIntentMigrationCommandSourceThrough(t *testing.T, highest uint) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "migrations")
	require.NoError(t, os.Mkdir(directory, 0700), "create only a private test-owned source directory for the real executable")
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err, "read the canonical repository migration source rather than invent a synthetic predecessor ledger")
	var copiedHighest uint
	copiedSliceFiles := 0
	for _, source := range files {
		name := filepath.Base(source)
		prefix, _, ok := strings.Cut(name, "_")
		require.True(t, ok, "canonical migration filenames must identify their real version")
		version, err := strconv.ParseUint(prefix, 10, 32)
		require.NoError(t, err, "read the actual canonical migration version before selecting the pinned complete chain")
		if uint(version) > highest {
			continue
		}
		body, err := os.ReadFile(source)
		require.NoError(t, err, "read every actual migration in the complete prerequisite chain through the pinned S1 boundary")
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), body, 0600), "copy exact unmodified repository migration bytes to a test-owned executable source")
		if uint(version) > copiedHighest {
			copiedHighest = uint(version)
		}
		if version >= 384 {
			copiedSliceFiles++
		}
	}
	require.Equal(t, highest, copiedHighest, "the real executable's complete source must stop at the exact requested caller-intent boundary")
	require.Equal(t, 2*int(highest-383), copiedSliceFiles, "the pinned real source must include both directions of every migration from 384 through the requested highest")
	return root
}

func runCallerIntentMigrationCommand(t *testing.T, ctx context.Context, binary, directory, direction string, refused bool, inspector *pgx.Conn) *pgx.Conn {
	t.Helper()
	return runCallerIntentMigrationCommandWithRefusal(t, ctx, binary, directory, direction, refused, inspector, "unresolved_caller_send_prevents_rollback", 388)
}

func runCallerIntentMigrationCommandWithRefusal(t *testing.T, ctx context.Context, binary, directory, direction string, refused bool, inspector *pgx.Conn, reason string, dirtyVersion uint) *pgx.Conn {
	t.Helper()
	config := inspector.Config().Copy()
	raw := inspector.Config().ConnString()
	// The helper retains one admin and one migrator connection. Release this
	// inspector before the real child so held capacity never exceeds three.
	// pgx.Close is explicitly safe on the already-closed original at cleanup.
	require.NoError(t, inspector.Close(ctx), "release the inspection connection before running the real migration child within the three-connection cap")
	childCtx, childCancel := context.WithTimeout(ctx, 90*time.Second)
	defer childCancel()
	child := exec.CommandContext(childCtx, binary, direction)
	child.Dir = directory
	child.Env = callerIntentMigrationCommandEnvironment(raw)
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	err := child.Run() // Run joins the owned child before any inspector reopens.
	if refused {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "the real down command must fail after starting, not because it could not execute")
		require.Equal(t, 1, exit.ExitCode(), "the actual migration executable must report its highest-first rollback refusal")
		require.True(t, strings.Contains(stderr.String(), reason), "captured diagnostics must identify the exact intended rollback guard without publishing raw command output")
		require.True(t, strings.Contains(stderr.String(), fmt.Sprintf("Version: %d", dirtyVersion)) && strings.Contains(stderr.String(), "Dirty:   true"), "the actual executable must diagnose its exact predecessor dirty ledger")
		require.False(t, strings.Contains(stdout.String(), "Migrations rolled back successfully."), "a refused first down must never print a success receipt")
	} else {
		require.True(t, err == nil, "the actual migration child must complete up without exposing captured connection or SQL diagnostics")
		require.True(t, strings.HasSuffix(stdout.String(), "Migrations applied successfully.\n"), "the actual executable must return its normal successful-up framing")
		require.Zero(t, stderr.Len(), "a successful actual caller-intent up must not emit failure diagnostics")
	}
	require.NoError(t, childCtx.Err(), "the actual child outcome must be observed before the test-owned deadline rather than inferred from cancellation")
	conn, connectErr := pgx.ConnectConfig(ctx, config)
	require.True(t, connectErr == nil, "reopen only the generated database inspector after the owned child has been joined without exposing connection diagnostics")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, conn.Close(closeCtx), "close only the reopened generated-database inspector")
	})
	return conn
}

func callerIntentMigrationCommandEnvironment(raw string) []string {
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DATABASE_URL", "PGAPPNAME", "PGOPTIONS", "PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "LOG_LEVEL":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "DATABASE_URL="+raw, "LOG_LEVEL=info")
}
