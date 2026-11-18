package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type admissionLookupMigrationIndex struct {
	version uint
	name    string
}

func admissionLookupMigrationIndexes() []admissionLookupMigrationIndex {
	return []admissionLookupMigrationIndex{{381, "idx_admission_active_repair_lookup"}, {382, "idx_admission_pending_question_lookup"}}
}

func requireAdmissionLookupMigrationError(t *testing.T, err error, code string) {
	t.Helper()
	var migrationErr database.Error
	require.ErrorAs(t, err, &migrationErr, "the actual migration driver must preserve its failed standalone statement")
	var original *pq.Error
	require.ErrorAs(t, migrationErr.OrigErr, &original, "retain the real PostgreSQL SQLSTATE rather than infer failure from a dirty ledger")
	require.Equal(t, pq.ErrorCode(code), original.Code, "the standalone migration must fail for the exact intended database condition")
}

func TestAdmissionLookupStandaloneDirtyMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scenario string }{
		{"valid wrong-definition name fails closed", "wrong"},
		{"invalid same-name build fails closed", "invalid"},
		{"missing down target fails visibly dirty", "down"},
	}
	for _, index := range admissionLookupMigrationIndexes() {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%d/%s", index.version, tt.name), func(t *testing.T) {
				t.Parallel()
				migration, conn := newContentMigrationPostgres(t, true)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				var conflictBefore json.RawMessage
				if tt.scenario == "down" {
					require.NoError(t, migration.Migrate(index.version), "install exactly this standalone concurrent index before its down failure")
					_, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY "+index.name)
					require.NoError(t, err, "remove only the disposable exact index to make the real down encounter a genuine absent target")
					requireAdmissionLookupMigrationError(t, migration.Steps(-1), "42704")
				} else {
					require.NoError(t, migration.Migrate(index.version-1), "construct the exact predecessor boundary including the eventual A2 index rollback floor")
					_, err := conn.Exec(ctx, "CREATE TABLE admission_lookup_name_probe(value integer NOT NULL)")
					require.NoError(t, err, "create a disposable wrong-parent table without altering the production lookup table")
					if tt.scenario == "invalid" {
						_, err = conn.Exec(ctx, "INSERT INTO admission_lookup_name_probe VALUES(1),(1)")
						require.NoError(t, err, "seed the deliberate duplicate before a genuine concurrent unique build")
					}
					_, err = conn.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+index.name+" ON admission_lookup_name_probe(value)")
					if tt.scenario == "invalid" {
						var duplicate *pgconn.PgError
						require.ErrorAs(t, err, &duplicate, "leave a real invalid schema-wide name through PostgreSQL's failed concurrent build")
						require.Equal(t, "23505", duplicate.Code, "the invalid fixture must result from actual duplicate data")
						requireAdmissionLookupInvalidProbe(t, ctx, conn, index.name)
					} else {
						require.NoError(t, err, "install a valid same-name object with the wrong parent and key shape")
					}
					conflictBefore = admissionLookupConflictingObject(t, ctx, conn, index.name)
					requireAdmissionLookupMigrationError(t, migration.Migrate(index.version), "42P07")
				}
				expectedVersion := index.version
				if tt.scenario == "down" {
					expectedVersion--
				}
				version, dirty, err := migration.Version()
				require.NoError(t, err, "read the exact real failed standalone migration ledger")
				require.Equal(t, expectedVersion, version, "up and down failures must retain their exact attempted ledger boundary")
				require.True(t, dirty, "neither failed build nor failed drop may masquerade as a clean migration")
				recorder := &lifecycleReferenceDirtyRepairRecorder{Migrate: migration}
				repairedVersion, repaired, err := repairKnownDirtyMigration(recorder)
				require.Error(t, err, "automatic known-dirty repair must refuse this new standalone boundary")
				require.Zero(t, repairedVersion, "refusal must not invent a repaired-version receipt")
				require.False(t, repaired, "the new migration must not join the legacy automatic repair allowlist")
				require.Zero(t, recorder.forceCalls, "no failure path may Force the real dirty migration ledger")
				var retry migrate.ErrDirty
				require.ErrorAs(t, migration.Migrate(index.version), &retry, "ordinary retry must remain fail closed while the ledger is dirty")
				require.Equal(t, int(expectedVersion), retry.Version, "retry refusal must retain the same dirty boundary")
				if tt.scenario != "down" {
					require.Equal(t, conflictBefore, admissionLookupConflictingObject(t, ctx, conn, index.name), "failed migration and refused repair/retry must preserve the conflicting object's parent definition validity and readiness")
				}
			})
		}
	}
}

func requireAdmissionLookupInvalidProbe(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	var parent string
	var valid bool
	err := conn.QueryRow(ctx, "SELECT p.relname,i.indisvalid FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_class p ON p.oid=i.indrelid WHERE ix.oid=to_regclass('public.'||$1)", name).Scan(&parent, &valid)
	require.NoError(t, err, "inspect the real named index retained after the deliberate failed concurrent build")
	require.Equal(t, "admission_lookup_name_probe", parent, "the invalid name must belong to the exact disposable wrong-parent table")
	require.False(t, valid, "duplicate data must leave a genuinely invalid index rather than a fabricated catalog state")
}

func admissionLookupConflictingObject(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	err := conn.QueryRow(ctx, "SELECT jsonb_build_object('parent',p.relname,'definition',pg_get_indexdef(i.indexrelid),'valid',i.indisvalid,'ready',i.indisready) FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_class p ON p.oid=i.indrelid WHERE ix.oid=to_regclass('public.'||$1)", name).Scan(&out)
	require.NoError(t, err, "snapshot the real valid or invalid same-name object before and after fail-closed migration handling")
	return out
}

func admissionLookupCommandIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn, excludeNew bool) []string {
	t.Helper()
	query := "SELECT tablename||':'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='public' AND tablename IN ('pull_request_repair_runs','session_questions')"
	if excludeNew {
		query += " AND indexname NOT IN ('idx_admission_active_repair_lookup','idx_admission_pending_question_lookup')"
	}
	rows, err := conn.Query(ctx, query+" ORDER BY tablename,indexname")
	require.NoError(t, err, "snapshot exact complete index definitions on both admission discovery tables")
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect legacy index definitions rather than compare only counts")
	return out
}

func admissionLookupCommandRows(t *testing.T, ctx context.Context, conn *pgx.Conn, org uuid.UUID) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	err := conn.QueryRow(ctx, "SELECT jsonb_build_object('sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY id),'[]'::jsonb) FROM sessions s WHERE org_id=$1),'questions',(SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY id),'[]'::jsonb) FROM session_questions q WHERE org_id=$1),'repairs',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]'::jsonb) FROM pull_request_repair_runs r WHERE org_id=$1),'dispatches',(SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY id),'[]'::jsonb) FROM session_input_dispatches d WHERE org_id=$1))", org).Scan(&out)
	require.NoError(t, err, "snapshot all synthetic session repair question and retained-dispatch fields exactly")
	return out
}

func TestAdmissionLookupInactiveMigrationRoundTripPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, migration.Migrate(380), "begin at the exact eventual A2-only schema floor before A1 indexes")
	org, session, pr := uuid.New(), uuid.New(), uuid.New()
	batch := &pgx.Batch{}
	batch.Queue("INSERT INTO organizations(id,name) VALUES($1,'Synthetic admission lookup rollback')", org)
	batch.Queue("INSERT INTO sessions(id,org_id,agent_type,status) VALUES($2,$1,'codex','idle')", org, session)
	batch.Queue("INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title) VALUES($3,$1,$2,9,'https://example.invalid/pr/9','synthetic/lookup','Synthetic retained history')", org, session, pr)
	err := conn.SendBatch(ctx, batch).Close()
	require.NoError(t, err, "create only inactive legacy synthetic identity rows before the standalone builds")
	batch = &pgx.Batch{}
	batch.Queue("INSERT INTO pull_request_repair_runs(org_id,pull_request_id,session_id,action_type,health_version,head_sha,base_sha,active) VALUES($1,$3,$2,'fix_tests',1,'old-head','base',false)", org, session, pr)
	batch.Queue("INSERT INTO session_questions(org_id,session_id,question_text,status) VALUES($1,$2,'Synthetic answered history','answered')", org, session)
	err = conn.SendBatch(ctx, batch).Close()
	require.NoError(t, err, "retain released repair and answered question history without activating lifecycle behavior")
	before, legacy := admissionLookupCommandRows(t, ctx, conn, org), admissionLookupCommandIndexes(t, ctx, conn, true)
	for _, target := range []uint{381, 382, 383, 382, 381, 380, 381, 382, 383} {
		require.NoError(t, migration.Migrate(target), "sequential inactive up/down must build or drop each standalone index in its own file")
		version, dirty, err := migration.Version()
		require.NoError(t, err, "inspect every successful standalone or floor boundary")
		require.Equal(t, target, version, "the migration ledger must reach the exact requested boundary")
		require.False(t, dirty, "each completed inactive step must leave a clean ledger")
		require.Equal(t, before, admissionLookupCommandRows(t, ctx, conn, org), "all index-only round trips must leave every raw retained history field unchanged")
		require.Equal(t, legacy, admissionLookupCommandIndexes(t, ctx, conn, true), "all legacy indexes must survive removal and rebuilding of the two new lookup indexes")
		var installed []string
		rows, err := conn.Query(ctx, "SELECT relname::text FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN ('idx_admission_active_repair_lookup','idx_admission_pending_question_lookup') ORDER BY relname")
		require.NoError(t, err, "inspect the exact new index presence at each boundary")
		installed, err = pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "collect only actual new index relations")
		expected := []string{}
		if target >= 381 {
			expected = append(expected, "idx_admission_active_repair_lookup")
		}
		if target >= 382 {
			expected = append(expected, "idx_admission_pending_question_lookup")
		}
		require.Equal(t, expected, installed, "standalone down/up must remove and rebuild only the intended exact new indexes")
	}
}

func TestAdmissionLookupHighestRollbackFloorLedgerPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, migration.Migrate(383), "install the highest admission rollback floor after both concurrent indexes")
	org, session := uuid.New(), uuid.New()
	batch := &pgx.Batch{}
	batch.Queue("INSERT INTO organizations(id,name) VALUES($1,'Synthetic admission highest floor')", org)
	batch.Queue("INSERT INTO sessions(id,org_id,agent_type,status) VALUES($2,$1,'codex','idle')", org, session)
	batch.Queue("INSERT INTO session_input_dispatches(id,org_id,session_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,next_attempt_at) VALUES(gen_random_uuid(),$1,$2,'message','synthetic_waiting','highest-floor',repeat('a',64),'{}',1,'waiting','none',now())", org, session)
	err := conn.SendBatch(ctx, batch).Close()
	require.NoError(t, err, "retain a real waiting input without a positive stamp or active cutover that could mask its independent floor branch")
	before, indexes := admissionLookupCommandRows(t, ctx, conn, org), admissionLookupCommandIndexes(t, ctx, conn, false)
	requireAdmissionLookupMigrationError(t, migration.Steps(-1), "55000")
	version, dirty, err := migration.Version()
	require.NoError(t, err, "inspect the driver ledger after the first supported down refuses")
	require.Equal(t, uint(382), version, "the failed highest preflight must record its exact predecessor dirty boundary before any index drop")
	require.True(t, dirty, "a refused floor must leave a visibly dirty ledger requiring deliberate repair-forward")
	require.Equal(t, before, admissionLookupCommandRows(t, ctx, conn, org), "highest-floor refusal must leave retained input and session fields unchanged")
	require.Equal(t, indexes, admissionLookupCommandIndexes(t, ctx, conn, false), "neither new nor legacy lookup index may be removed before the first refusal")
}
