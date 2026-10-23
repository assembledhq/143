package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionInputDispatchSchemaMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "populated schema installs each parent foreign key independently", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := uint(335)
			if tt.upgrade {
				start = 331
			}
			require.NoError(t, migration.Migrate(start), "construct the selected full schema before dispatch foundation assertions")
			fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			seedLifecycleReferenceKeyMetadata(t, ctx, conn, fixture)
			before := lifecycleReferenceKeyRows(t, ctx, conn, fixture)
			projection := executionEpochMigrationLegacyProjection(t, ctx, conn, fixture)
			indexes := dispatchIdentityLegacyIndexes(t, ctx, conn)
			assertBoundary := func(version uint) {
				actualVersion, dirty, err := migration.Version()
				require.NoError(t, err, "read the exact dispatch schema migration ledger boundary")
				require.Equal(t, version, actualVersion, "each parent foreign key must commit at its own migration version")
				require.False(t, dirty, "successful dispatch schema changes must leave a clean migration ledger")
				require.Equal(t, before, lifecycleReferenceKeyRows(t, ctx, conn, fixture), "dispatch foundation migrations must preserve every legacy graph byte including precise bigint and prior passive metadata")
				require.Equal(t, projection, executionEpochMigrationLegacyProjection(t, ctx, conn, fixture), "dispatch foundation migrations must preserve every existing execution API field")
				require.Equal(t, indexes, dispatchIdentityLegacyIndexes(t, ctx, conn), "dispatch foundation migrations must preserve all existing indexes including the referenced-key prerequisites")
				requireDispatchIdentityCatalog(t, ctx, conn, version)
			}
			assertBoundary(start)
			if tt.upgrade {
				for _, version := range []uint{332, 333, 334, 335} {
					require.NoError(t, migration.Migrate(version), "install the next dispatch table or single-hot-parent foreign key transaction")
					assertBoundary(version)
				}
			}
			assertBoundary(335)
			// Migration boundaries share this isolated connection and run
			// sequentially; complete database fixtures are parallel instead.
			for _, version := range []uint{334, 333, 332, 331} {
				require.NoError(t, migration.Steps(-1), "roll back exactly one dispatch FK or the empty foundation table without CASCADE")
				assertBoundary(version)
			}
			for _, version := range []uint{332, 333, 334, 335} {
				require.NoError(t, migration.Migrate(version), "reapply the empty dispatch schema in the same independently committed sequence")
				assertBoundary(version)
			}
			requireLifecycleReferenceKeyLegacyInserts(t, ctx, conn)
			assertBoundary(335)
		})
	}
}

func dispatchIdentityLegacyIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes WHERE schemaname='public' AND tablename <> 'session_input_dispatches' ORDER BY tablename,indexname`)
	require.NoError(t, err, "inspect every preexisting public-schema index without excluding prerequisite referenced keys")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "capture exact preexisting index definitions before the dispatch foundation migration")
	return indexes
}

func requireDispatchIdentityCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint) {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.session_input_dispatches') IS NOT NULL`).Scan(&exists), "inspect whether the dispatch foundation table exists at this boundary")
	require.Equal(t, version >= 332, exists, "only migration 332 may add or remove the dispatch identity table")
	if !exists {
		return
	}
	var count int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_input_dispatches`).Scan(&count), "inspect dispatch rows independently of any future production reader")
	require.Equal(t, int64(0), count, "schema installation and rollback must not backfill or produce runnable or historical dispatch rows")
	type column struct {
		Name     string  `db:"column_name"`
		DataType string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	want := []column{
		{Name: "id", DataType: "uuid", Nullable: "NO", Default: requestReadinessMigrationPtr("gen_random_uuid()")},
		{Name: "org_id", DataType: "uuid", Nullable: "NO"}, {Name: "session_id", DataType: "uuid", Nullable: "NO"},
		{Name: "thread_id", DataType: "uuid", Nullable: "YES"}, {Name: "session_epoch", DataType: "bigint", Nullable: "YES"},
		{Name: "thread_epoch", DataType: "bigint", Nullable: "YES"}, {Name: "message_id", DataType: "bigint", Nullable: "YES"},
		{Name: "request_id", DataType: "uuid", Nullable: "YES"}, {Name: "kind", DataType: "text", Nullable: "NO"},
		{Name: "ingress_kind", DataType: "text", Nullable: "NO"}, {Name: "ingress_key", DataType: "text", Nullable: "NO"},
		{Name: "payload_digest", DataType: "text", Nullable: "NO"}, {Name: "validated_payload", DataType: "jsonb", Nullable: "NO"},
		{Name: "normalization_version", DataType: "integer", Nullable: "NO"},
		{Name: "created_at", DataType: "timestamp with time zone", Nullable: "NO", Default: requestReadinessMigrationPtr("now()")},
		{Name: "updated_at", DataType: "timestamp with time zone", Nullable: "NO", Default: requestReadinessMigrationPtr("now()")},
	}
	rows, err := conn.Query(ctx, `SELECT column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_input_dispatches' ORDER BY ordinal_position`)
	require.NoError(t, err, "inspect every dispatch column including the absence of future admission consumer claim and scheduling state")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect the exact passive identity schema column types nullability order and defaults")
	require.Equal(t, want, actual, "only UUID and timestamp identity defaults may be installed; no epoch payload normalization or authority default may be invented")
	requireDispatchIdentityForeignKeys(t, ctx, conn, version)
	requireDispatchIdentityChecks(t, ctx, conn)
	requireDispatchIdentityIndexes(t, ctx, conn)
}

func requireDispatchIdentityForeignKeys(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint) {
	t.Helper()
	type foreignKey struct {
		Name       string   `db:"constraint_name"`
		Columns    []string `db:"column_names"`
		Parent     string   `db:"parent_table"`
		ParentCols []string `db:"parent_columns"`
		Delete     string   `db:"on_delete"`
		Update     string   `db:"on_update"`
		Match      string   `db:"match_type"`
		Deferrable bool     `db:"is_deferrable"`
		Deferred   bool     `db:"is_deferred"`
		Validated  bool     `db:"is_validated"`
	}
	want := []foreignKey{{Name: "session_input_dispatches_org_id_fkey", Columns: []string{"org_id"}, Parent: "public.organizations", ParentCols: []string{"id"}, Delete: "a", Update: "a", Match: "s", Validated: true}}
	if version >= 335 {
		want = append(want, foreignKey{Name: "session_input_dispatches_request_scope_fk", Columns: []string{"org_id", "session_id", "request_id"}, Parent: "public.session_human_input_requests", ParentCols: []string{"org_id", "session_id", "id"}, Delete: "a", Update: "a", Match: "s", Validated: true})
	}
	if version >= 333 {
		want = append(want, foreignKey{Name: "session_input_dispatches_session_scope_fk", Columns: []string{"org_id", "session_id"}, Parent: "public.sessions", ParentCols: []string{"org_id", "id"}, Delete: "a", Update: "a", Match: "s", Validated: true})
	}
	if version >= 334 {
		want = append(want, foreignKey{Name: "session_input_dispatches_thread_scope_fk", Columns: []string{"org_id", "session_id", "thread_id"}, Parent: "public.session_threads", ParentCols: []string{"org_id", "session_id", "id"}, Delete: "a", Update: "a", Match: "s", Validated: true})
	}
	rows, err := conn.Query(ctx, `SELECT c.conname::text AS constraint_name,
		ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY key(attnum,position) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum ORDER BY key.position) AS column_names,
		n.nspname || '.' || p.relname AS parent_table,
		ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY key(attnum,position) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=key.attnum ORDER BY key.position) AS parent_columns,
		c.confdeltype::text AS on_delete,c.confupdtype::text AS on_update,c.confmatchtype::text AS match_type,
		c.condeferrable AS is_deferrable,c.condeferred AS is_deferred,c.convalidated AS is_validated
		FROM pg_constraint c JOIN pg_class p ON p.oid=c.confrelid JOIN pg_namespace n ON n.oid=p.relnamespace
		WHERE c.conrelid='public.session_input_dispatches'::regclass AND c.contype='f' ORDER BY c.conname`)
	require.NoError(t, err, "inspect every real dispatch foreign key and exact tenant session lane membership tuple")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[foreignKey])
	require.NoError(t, err, "collect complete FK deletion update match deferrability and validation policies")
	require.Equal(t, want, actual, "each migration must add only its one validated immediate MATCH SIMPLE NO ACTION parent relationship without cascades or message ownership claims")
}

func requireDispatchIdentityChecks(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	type check struct {
		Name       string `db:"constraint_name"`
		Definition string `db:"definition"`
		Validated  bool   `db:"is_validated"`
	}
	want := []check{
		{Name: "session_input_dispatches_ingress_key_check", Definition: "CHECK (btrim(ingress_key) <> ''::text)", Validated: true},
		{Name: "session_input_dispatches_ingress_kind_check", Definition: "CHECK (btrim(ingress_kind) <> ''::text)", Validated: true},
		{Name: "session_input_dispatches_kind_check", Definition: "CHECK (kind = ANY (ARRAY['message'::text, 'answer'::text, 'denial'::text, 'initial'::text, 'retry'::text, 'recovery'::text]))", Validated: true},
		{Name: "session_input_dispatches_message_id_check", Definition: "CHECK (message_id IS NULL OR message_id > 0)", Validated: true},
		{Name: "session_input_dispatches_normalization_version_check", Definition: "CHECK (normalization_version >= 0)", Validated: true},
		{Name: "session_input_dispatches_payload_digest_check", Definition: "CHECK (btrim(payload_digest) <> ''::text)", Validated: true},
		{Name: "session_input_dispatches_request_kind_check", Definition: "CHECK ((kind <> ALL (ARRAY['answer'::text, 'denial'::text])) OR request_id IS NOT NULL)", Validated: true},
		{Name: "session_input_dispatches_session_epoch_check", Definition: "CHECK (session_epoch IS NULL OR session_epoch >= 0)", Validated: true},
		{Name: "session_input_dispatches_thread_epoch_check", Definition: "CHECK (thread_epoch IS NULL OR thread_epoch >= 0)", Validated: true},
		{Name: "session_input_dispatches_thread_epoch_lane_check", Definition: "CHECK (thread_epoch IS NULL OR thread_id IS NOT NULL)", Validated: true},
	}
	rows, err := conn.Query(ctx, `SELECT conname::text AS constraint_name,pg_get_constraintdef(oid,true) AS definition,convalidated AS is_validated FROM pg_constraint WHERE conrelid='public.session_input_dispatches'::regclass AND contype='c' ORDER BY conname`)
	require.NoError(t, err, "inspect the complete installed passive identity CHECK contract")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[check])
	require.NoError(t, err, "collect every validated CHECK rather than asserting only its constraint name")
	require.Equal(t, want, actual, "the foundation must enforce exactly its finite identity shape without adding consumer admission readiness or execution policy")
}

func requireDispatchIdentityIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	want := []string{
		"CREATE INDEX idx_session_input_dispatches_lane ON public.session_input_dispatches USING btree (org_id, session_id, thread_id)",
		"CREATE UNIQUE INDEX idx_session_input_dispatches_message ON public.session_input_dispatches USING btree (org_id, message_id) WHERE (message_id IS NOT NULL)",
		"CREATE INDEX idx_session_input_dispatches_request ON public.session_input_dispatches USING btree (org_id, session_id, request_id) WHERE (request_id IS NOT NULL)",
		"CREATE UNIQUE INDEX session_input_dispatches_ingress_key ON public.session_input_dispatches USING btree (org_id, ingress_kind, ingress_key)",
		"CREATE UNIQUE INDEX session_input_dispatches_pkey ON public.session_input_dispatches USING btree (id)",
		"CREATE UNIQUE INDEX session_input_dispatches_scope_key ON public.session_input_dispatches USING btree (org_id, session_id, id)",
	}
	rows, err := conn.Query(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename='session_input_dispatches' ORDER BY indexname`)
	require.NoError(t, err, "inspect exact dispatch key order uniqueness predicates and lack of INCLUDE expressions or scheduler indexes")
	actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect every complete dispatch index definition")
	require.Equal(t, want, actual, "only primary ingress message lane request and future-parent identity indexes belong to this passive slice")
	var ready bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT bool_and(indisvalid AND indisready AND indislive AND indimmediate AND indexprs IS NULL AND indnkeyatts=indnatts) FROM pg_index WHERE indrelid='public.session_input_dispatches'::regclass`).Scan(&ready), "inspect physical index readiness validity and absence of included or expression attributes")
	require.True(t, ready, "all six exact dispatch indexes must be live ready valid immediate and expression-free")
}
