package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionLifecycleReferenceKeyMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "populated schema commits each referenced key independently", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := uint(331)
			if tt.upgrade {
				start = 328
			}
			require.NoError(t, migration.Migrate(start), "construct the real full schema at the selected referenced-key boundary")
			fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			seedLifecycleReferenceKeyMetadata(t, ctx, conn, fixture)
			before := lifecycleReferenceKeyRows(t, ctx, conn, fixture)
			requireLifecycleReferenceKeyGraphPrecision(t, ctx, conn, fixture, before)
			projection := executionEpochMigrationLegacyProjection(t, ctx, conn, fixture)
			legacyIndexes := lifecycleReferenceKeyLegacyIndexes(t, ctx, conn)
			requireLifecycleReferenceKeyBoundary(t, ctx, conn, migration, fixture, before, projection, legacyIndexes, start)
			if tt.upgrade {
				// Each standalone concurrent build commits before the next
				// table's build; these schema steps share one isolated database.
				for _, version := range []uint{329, 330, 331} {
					require.NoError(t, migration.Migrate(version), "build only the next table's exact tenant-scoped referenced key")
					requireLifecycleReferenceKeyBoundary(t, ctx, conn, migration, fixture, before, projection, legacyIndexes, version)
				}
			}
			requireLifecycleReferenceKeyLegacyInserts(t, ctx, conn)
			// No test dependent FK is installed here. The separate FK tests
			// prove rollback refusal while a referencing constraint exists.
			for _, version := range []uint{330, 329, 328} {
				require.NoError(t, migration.Steps(-1), "remove only the next standalone referenced-key index without CASCADE")
				requireLifecycleReferenceKeyBoundary(t, ctx, conn, migration, fixture, before, projection, legacyIndexes, version)
			}
			for _, version := range []uint{329, 330, 331} {
				require.NoError(t, migration.Migrate(version), "rebuild each concurrent referenced key after a clean rollback")
				requireLifecycleReferenceKeyBoundary(t, ctx, conn, migration, fixture, before, projection, legacyIndexes, version)
			}
			requireLifecycleReferenceKeyLegacyInserts(t, ctx, conn)
		})
	}
}

func seedLifecycleReferenceKeyMetadata(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) {
	t.Helper()
	f := fixture.executor
	// These isolated rows hold synthetic prior-slice observations only, not
	// evidence that any real runtime drained or acquired lifecycle authority.
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE thread_runtimes SET admission_session_epoch=9223372036854775806,admission_thread_epoch=0,drain_proof='{"synthetic_receipt":"example"}' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
	batch.Queue(`UPDATE session_executors SET admission_session_epoch=0,admission_thread_epoch=9223372036854775807 WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
	batch.Queue(`UPDATE thread_inbox_delivery_batches SET admission_session_epoch=47,admission_thread_epoch=53 WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
	require.NoError(t, conn.SendBatch(ctx, batch).Close(), "populate all previous execution metadata slots before constructing indexes")
}

func lifecycleReferenceKeyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) json.RawMessage {
	t.Helper()
	legacy := executionEpochMigrationLegacyRows(t, ctx, conn, fixture)
	f := fixture.executor
	var value json.RawMessage
	err := conn.QueryRow(ctx, `SELECT $3::jsonb || jsonb_build_object(
		'runtimes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM thread_runtimes r WHERE r.org_id=$1 AND r.session_id=$2),
		'executors',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM session_executors e WHERE e.org_id=$1 AND e.session_id=$2),
		'batches',(SELECT jsonb_agg(to_jsonb(b) ORDER BY b.id) FROM thread_inbox_delivery_batches b WHERE b.org_id=$1 AND b.session_id=$2))`, f.org, f.session, legacy).Scan(&value)
	require.NoError(t, err, "capture the complete legacy graph without excluding any previously installed passive metadata")
	return value
}

func lifecycleReferenceKeyLegacyIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes
		WHERE schemaname='public' AND tablename IN ('session_threads','session_human_input_requests','thread_runtimes')
		AND indexname NOT IN ('idx_session_threads_org_session_id','idx_session_human_input_requests_org_session_id','idx_thread_runtimes_org_session_thread_id') ORDER BY tablename,indexname`)
	require.NoError(t, err, "inspect every preexisting index on the three referenced parent tables")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect complete preexisting index definitions rather than comparing only index counts")
	return indexes
}

type lifecycleReferenceKeyIndex struct {
	Name             string   `db:"index_name"`
	Table            string   `db:"table_name"`
	Method           string   `db:"access_method"`
	Columns          []string `db:"column_names"`
	Options          []int16  `db:"column_options"`
	KeyCount         int      `db:"key_count"`
	AttributeCount   int      `db:"attribute_count"`
	Unique           bool     `db:"is_unique"`
	Valid            bool     `db:"is_valid"`
	Ready            bool     `db:"is_ready"`
	Live             bool     `db:"is_live"`
	Immediate        bool     `db:"is_immediate"`
	Primary          bool     `db:"is_primary"`
	NoPredicate      bool     `db:"no_predicate"`
	NoExpressions    bool     `db:"no_expressions"`
	NullsNotDistinct bool     `db:"nulls_not_distinct"`
}

func requireLifecycleReferenceKeyBoundary(t *testing.T, ctx context.Context, conn *pgx.Conn, migration *migrate.Migrate, fixture executionEpochMigrationFixture, before json.RawMessage, projection executionEpochMigrationProjection, legacyIndexes []string, version uint) {
	t.Helper()
	actualVersion, dirty, err := migration.Version()
	require.NoError(t, err, "read the exact concurrent-index migration ledger boundary")
	require.Equal(t, version, actualVersion, "each concurrent build or drop must reach its own selected schema version")
	require.False(t, dirty, "successful concurrent index changes must leave a clean migration ledger")
	require.Equal(t, before, lifecycleReferenceKeyRows(t, ctx, conn, fixture), "index builds drops and rebuilds must preserve every deterministic JSONB byte including bigint values above float64 precision")
	require.Equal(t, projection, executionEpochMigrationLegacyProjection(t, ctx, conn, fixture), "referenced-key prerequisites must not change any existing execution API field")
	require.Equal(t, legacyIndexes, lifecycleReferenceKeyLegacyIndexes(t, ctx, conn), "referenced-key migrations must preserve every preexisting parent index definition")
	want := []lifecycleReferenceKeyIndex{}
	if version >= 330 {
		want = append(want, lifecycleReferenceKeyIndex{Name: "idx_session_human_input_requests_org_session_id", Table: "session_human_input_requests", Method: "btree", Columns: []string{"org_id", "session_id", "id"}, Options: []int16{0, 0, 0}, KeyCount: 3, AttributeCount: 3, Unique: true, Valid: true, Ready: true, Live: true, Immediate: true, NoPredicate: true, NoExpressions: true})
	}
	if version >= 329 {
		want = append(want, lifecycleReferenceKeyIndex{Name: "idx_session_threads_org_session_id", Table: "session_threads", Method: "btree", Columns: []string{"org_id", "session_id", "id"}, Options: []int16{0, 0, 0}, KeyCount: 3, AttributeCount: 3, Unique: true, Valid: true, Ready: true, Live: true, Immediate: true, NoPredicate: true, NoExpressions: true})
	}
	if version >= 331 {
		want = append(want, lifecycleReferenceKeyIndex{Name: "idx_thread_runtimes_org_session_thread_id", Table: "thread_runtimes", Method: "btree", Columns: []string{"org_id", "session_id", "thread_id", "id"}, Options: []int16{0, 0, 0, 0}, KeyCount: 4, AttributeCount: 4, Unique: true, Valid: true, Ready: true, Live: true, Immediate: true, NoPredicate: true, NoExpressions: true})
	}
	rows, err := conn.Query(ctx, `SELECT index_class.relname::text AS index_name,parent.relname::text AS table_name,method.amname::text AS access_method,
		ARRAY(SELECT attribute.attname::text FROM unnest(i.indkey::smallint[]) WITH ORDINALITY key(attnum,position)
			JOIN pg_attribute attribute ON attribute.attrelid=i.indrelid AND attribute.attnum=key.attnum ORDER BY key.position) AS column_names,
		i.indoption::smallint[] AS column_options,i.indnkeyatts::int AS key_count,i.indnatts::int AS attribute_count,
		i.indisunique AS is_unique,i.indisvalid AS is_valid,i.indisready AS is_ready,i.indislive AS is_live,
		i.indimmediate AS is_immediate,i.indisprimary AS is_primary,i.indpred IS NULL AS no_predicate,
		i.indexprs IS NULL AS no_expressions,i.indnullsnotdistinct AS nulls_not_distinct
		FROM pg_index i JOIN pg_class index_class ON index_class.oid=i.indexrelid
		JOIN pg_class parent ON parent.oid=i.indrelid JOIN pg_namespace namespace ON namespace.oid=index_class.relnamespace
		JOIN pg_am method ON method.oid=index_class.relam
		WHERE namespace.nspname='public' AND index_class.relname IN ('idx_session_threads_org_session_id','idx_session_human_input_requests_org_session_id','idx_thread_runtimes_org_session_thread_id') ORDER BY index_class.relname`)
	require.NoError(t, err, "inspect the real key order uniqueness validity readiness and physical index shape")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[lifecycleReferenceKeyIndex])
	require.NoError(t, err, "collect all referenced-index properties including predicates expressions and INCLUDE shape")
	require.Equal(t, want, actual, "each boundary must expose exactly its complete unique ready valid unfiltered expression-free ordered referenced keys with no INCLUDE columns")
}

func requireLifecycleReferenceKeyGraphPrecision(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture, before json.RawMessage) {
	t.Helper()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin an isolated mutation that verifies exact graph comparison above float64 precision")
	defer func() { _ = tx.Rollback(ctx) }()
	f := fixture.executor
	var epoch int64
	err = tx.QueryRow(ctx, `UPDATE thread_runtimes SET admission_session_epoch=admission_session_epoch+1 WHERE org_id=$1 AND session_id=$2 AND id=$3 RETURNING admission_session_epoch`, f.org, f.session, fixture.runtimeID).Scan(&epoch)
	require.NoError(t, err, "change exactly one stored bigint unit without modifying any other graph field")
	require.Equal(t, int64(9223372036854775807), epoch, "the precision probe must reach the exact maximum bigint from its immediately preceding value")
	require.NotEqual(t, before, lifecycleReferenceKeyRows(t, ctx, tx.Conn(), fixture), "a one-unit bigint change must remain distinguishable even when both values would collapse to the same float64")
	require.NoError(t, tx.Rollback(ctx), "restore the original row before applying or inspecting referenced-key migrations")
	require.Equal(t, before, lifecycleReferenceKeyRows(t, ctx, conn, fixture), "rolling back the precision probe must restore the exact deterministic JSONB graph")
}

func requireLifecycleReferenceKeyLegacyInserts(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
	f := fixture.executor
	thread := models.SessionThread{OrgID: f.org, SessionID: f.session, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic post-index lane", Status: models.ThreadStatusIdle, CreatedBySource: models.ThreadCreatedBySourceUser, RecoveryEventHistory: json.RawMessage(`[]`)}
	require.NoError(t, db.NewSessionThreadStore(conn).Create(ctx, &thread, 10), "the unchanged legacy thread insert must remain accepted with the exact tenant/session key present")
	actualThread, err := db.NewSessionThreadStore(conn).GetByID(ctx, f.org, thread.ID)
	require.NoError(t, err, "read the complete legacy thread created after index installation")
	require.Equal(t, thread, actualThread, "the referenced-key prerequisite must preserve all legacy thread insert fields and defaults")
	request := models.HumanInputRequest{OrgID: f.org, SessionID: f.session, ThreadID: &thread.ID, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic post-index input", Body: "Provide a synthetic response"}
	require.NoError(t, db.NewSessionHumanInputRequestStore(conn).Create(ctx, &request), "the unchanged legacy request insert must remain accepted with its tenant/session key present")
	actualRequest, err := db.NewSessionHumanInputRequestStore(conn).GetByID(ctx, f.org, f.session, request.ID)
	require.NoError(t, err, "read the complete legacy request created after index installation")
	require.Equal(t, request, actualRequest, "the referenced-key prerequisite must preserve every legacy request field and normalized default")
	runtime, err := db.NewThreadRuntimeStore(conn).CreateStarting(ctx, f.org, db.CreateThreadRuntimeParams{SessionID: f.session, ThreadID: thread.ID, AgentType: models.DefaultDefaultAgentType, OwnerNodeID: f.host, LeaseToken: f.token, LeaseDuration: time.Minute})
	require.NoError(t, err, "the unchanged runtime allocator must remain accepted with the exact tenant/session/lane key present")
	actualRuntime, err := db.NewThreadRuntimeStore(conn).GetActiveByThread(ctx, f.org, thread.ID)
	require.NoError(t, err, "read the complete runtime allocated after index installation")
	require.Equal(t, runtime, actualRuntime, "the referenced-key prerequisite must preserve every legacy runtime identity lease field and default")
	epochs, err := db.NewLifecycleEpochStore(conn).GetRequest(ctx, f.org, f.session, request.ID)
	require.NoError(t, err, "observe the newly created request without deriving provenance from its parent or index")
	require.Equal(t, db.LifecycleRequestEpochState{}, epochs, "index installation must not populate passive request epochs")
	readiness, err := db.NewSessionHumanInputRequestStore(conn).GetLifecycleReadiness(ctx, f.org, f.session, request.ID)
	require.NoError(t, err, "observe newly created request readiness after index installation")
	require.Equal(t, db.HumanInputReadinessMetadata{}, readiness, "a referenced key must not create readiness classification or proof")
	runtimeMetadata, err := db.NewThreadRuntimeStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, thread.ID, runtime.ID)
	require.NoError(t, err, "observe newly allocated runtime metadata after index installation")
	require.Equal(t, db.RuntimeLifecycleMetadata{}, runtimeMetadata, "a referenced key must not infer allocation epochs or physical drain proof")
}
