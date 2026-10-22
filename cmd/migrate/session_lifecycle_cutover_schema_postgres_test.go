package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSessionLifecycleCutoverSchemaMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "populated schema installs its session FK independently", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := uint(337)
			if tt.upgrade {
				start = 335
			}
			require.NoError(t, migration.Migrate(start), "construct the exact full-schema cutover prerequisite boundary")
			fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			seedLifecycleReferenceKeyMetadata(t, ctx, conn, fixture)
			f := fixture.executor
			var requestID uuid.UUID
			require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 ORDER BY id LIMIT 1`, f.org, f.session).Scan(&requestID), "select the existing synthetic request before storing a passive dispatch identity")
			message, sessionEpoch, threadEpoch := int64(math.MaxInt64), int64(math.MaxInt64-1), int64(0)
			insertDispatchIdentityRetentionRow(t, ctx, conn, dispatchIdentityRetentionRow{orgID: f.org, sessionID: f.session, threadID: &f.thread, requestID: &requestID, sessionEpoch: &sessionEpoch, threadEpoch: &threadEpoch, messageID: &message, kind: "answer", payload: `{"synthetic":"retained-dispatch"}`})
			before := cutoverMigrationLegacyRows(t, ctx, conn, fixture)
			projection := cutoverMigrationLegacyProjection(t, ctx, conn, fixture)
			indexes := cutoverMigrationLegacyIndexes(t, ctx, conn)
			assertBoundary := func(want uint) {
				version, dirty, err := migration.Version()
				require.NoError(t, err, "read the real migration ledger after each cutover schema boundary")
				require.Equal(t, want, version, "table and session FK must each settle at their own migration version")
				require.False(t, dirty, "successful cutover schema boundaries must leave the migration ledger clean")
				require.Equal(t, before, cutoverMigrationLegacyRows(t, ctx, conn, fixture), "cutover schema changes must preserve every prior graph byte including precise bigint metadata and stored dispatch identities")
				require.Equal(t, projection, cutoverMigrationLegacyProjection(t, ctx, conn, fixture), "cutover representation must preserve all existing session thread request and execution API fields")
				require.Equal(t, indexes, cutoverMigrationLegacyIndexes(t, ctx, conn), "cutover schema changes must preserve every preexisting public-schema index")
				requireCutoverMigrationCatalog(t, ctx, conn, want)
			}
			assertBoundary(start)
			if tt.upgrade {
				for _, version := range []uint{336, 337} {
					require.NoError(t, migration.Migrate(version), "install the passive cutover table and its one hot-parent FK in separate transactions")
					assertBoundary(version)
				}
			}
			// One owned connection runs these dependent boundaries sequentially;
			// the two full-schema fixture categories run in parallel.
			for _, version := range []uint{336, 335} {
				require.NoError(t, migration.Steps(-1), "remove only the cutover session FK or empty table without cascading dependencies")
				assertBoundary(version)
			}
			for _, version := range []uint{336, 337} {
				require.NoError(t, migration.Migrate(version), "reapply the empty cutover representation without creating any operational row")
				assertBoundary(version)
			}
			requireLifecycleReferenceKeyLegacyInserts(t, ctx, conn)
			assertBoundary(337)
		})
	}
}

func cutoverMigrationLegacyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) json.RawMessage {
	t.Helper()
	prior := lifecycleReferenceKeyRows(t, ctx, conn, fixture)
	f := fixture.executor
	var raw json.RawMessage
	err := conn.QueryRow(ctx, `SELECT $3::jsonb || jsonb_build_object(
		'organization',(SELECT to_jsonb(o) FROM organizations o WHERE o.id=$1),
		'dispatches',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM session_input_dispatches d WHERE d.org_id=$1 AND d.session_id=$2),
		'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM session_changesets c WHERE c.org_id=$1 AND c.session_id=$2),
		'messages',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id,m.created_at) FROM session_messages m WHERE m.org_id=$1 AND m.session_id=$2),
		'inbox',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM thread_inbox_entries i WHERE i.org_id=$1 AND i.session_id=$2),
		'phases',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM session_activity_phases p WHERE p.org_id=$1 AND p.session_id=$2))`, f.org, f.session, prior).Scan(&raw)
	require.NoError(t, err, "capture the complete synthetic pre-cutover graph without float64 conversion or omitted passive fields")
	return raw
}

type cutoverLegacyProjection struct {
	Session   models.Session
	Threads   []models.SessionThread
	Thread    models.SessionThread
	Requests  []models.HumanInputRequest
	Execution executionEpochMigrationProjection
}

func cutoverMigrationLegacyProjection(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) cutoverLegacyProjection {
	t.Helper()
	f := fixture.executor
	session, err := db.NewSessionStore(conn).GetByID(ctx, f.org, f.session)
	require.NoError(t, err, "read the unchanged public session model independently of cutover storage")
	threadStore := db.NewSessionThreadStore(conn)
	threads, err := threadStore.ListBySessionWithOptions(ctx, f.org, f.session, true)
	require.NoError(t, err, "read every existing lane through the unchanged batched list projection")
	// List uses its existing lightweight diff marker. Keep one independent full
	// fixture-thread projection; the raw graph separately preserves all row bytes.
	thread, err := threadStore.GetByID(ctx, f.org, f.thread)
	require.NoError(t, err, "read the known fixture thread's full model without per-lane query loops")
	requests, err := db.NewSessionHumanInputRequestStore(conn).ListBySession(ctx, f.org, f.session, db.HumanInputRequestFilters{})
	require.NoError(t, err, "read every existing human-input model through the unchanged batched session API")
	slices.SortFunc(threads, func(a, b models.SessionThread) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	slices.SortFunc(requests, func(a, b models.HumanInputRequest) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	return cutoverLegacyProjection{Session: session, Threads: threads, Thread: thread, Requests: requests, Execution: executionEpochMigrationLegacyProjection(t, ctx, conn, fixture)}
}

func cutoverMigrationLegacyIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes WHERE schemaname='public' AND tablename<>'session_lifecycle_cutovers' ORDER BY tablename,indexname`)
	require.NoError(t, err, "inspect all existing public indexes including dispatch and referenced-key prerequisites")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect complete index definitions rather than counts or selected names")
	return indexes
}

func requireCutoverMigrationCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint) {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.session_lifecycle_cutovers') IS NOT NULL`).Scan(&exists), "inspect cutover table existence at the exact schema boundary")
	require.Equal(t, version >= 336, exists, "only migration 336 may create or remove cutover representation")
	if !exists {
		return
	}
	var count int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_lifecycle_cutovers`).Scan(&count), "inspect operational rows independently of any future cutover reader")
	require.Equal(t, int64(0), count, "schema installation rollback and reapply must never backfill normalize or activate a session")
	type column struct {
		Name     string  `db:"column_name"`
		Type     string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	wantColumns := []column{
		{Name: "org_id", Type: "uuid", Nullable: "NO"}, {Name: "session_id", Type: "uuid", Nullable: "NO"},
		{Name: "version", Type: "bigint", Nullable: "NO"}, {Name: "state", Type: "text", Nullable: "NO"},
		{Name: "legacy_input_cutoff_id", Type: "bigint", Nullable: "YES"}, {Name: "normalization", Type: "jsonb", Nullable: "YES"},
		{Name: "activated_at", Type: "timestamp with time zone", Nullable: "YES"}, {Name: "minimum_writer_version", Type: "integer", Nullable: "YES"},
		{Name: "resumability_capability", Type: "jsonb", Nullable: "YES"},
	}
	rows, err := conn.Query(ctx, `SELECT column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_lifecycle_cutovers' ORDER BY ordinal_position`)
	require.NoError(t, err, "inspect all cutover columns in exact physical declaration order")
	columns, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect storage types nullability and absent defaults for every cutover field")
	require.Equal(t, wantColumns, columns, "version state and all optional provenance must be supplied explicitly without default inference")
	type constraint struct {
		Name       string `db:"constraint_name"`
		Kind       string `db:"constraint_type"`
		Definition string `db:"definition"`
		Validated  bool   `db:"is_validated"`
		Deferrable bool   `db:"is_deferrable"`
		Deferred   bool   `db:"is_deferred"`
	}
	wantConstraints := []constraint{
		{Name: "session_lifecycle_cutovers_cutoff_check", Kind: "c", Definition: "CHECK (legacy_input_cutoff_id IS NULL OR legacy_input_cutoff_id > 0)", Validated: true},
		{Name: "session_lifecycle_cutovers_minimum_writer_check", Kind: "c", Definition: "CHECK (minimum_writer_version IS NULL OR minimum_writer_version >= 0)", Validated: true},
		{Name: "session_lifecycle_cutovers_org_id_fkey", Kind: "f", Definition: "FOREIGN KEY (org_id) REFERENCES organizations(id)", Validated: true},
		{Name: "session_lifecycle_cutovers_pkey", Kind: "p", Definition: "PRIMARY KEY (org_id, session_id)", Validated: true},
	}
	if version >= 337 {
		wantConstraints = append(wantConstraints, constraint{Name: "session_lifecycle_cutovers_session_scope_fk", Kind: "f", Definition: "FOREIGN KEY (org_id, session_id) REFERENCES sessions(org_id, id)", Validated: true})
	}
	wantConstraints = append(wantConstraints,
		constraint{Name: "session_lifecycle_cutovers_state_check", Kind: "c", Definition: "CHECK (state = ANY (ARRAY['shadow'::text, 'normalizing'::text, 'active'::text]))", Validated: true},
		constraint{Name: "session_lifecycle_cutovers_version_check", Kind: "c", Definition: "CHECK (version >= 0)", Validated: true})
	// Column nullability is checked above. Servers that expose NOT NULL entries
	// in pg_constraint must not turn that same column property into extra FKs or
	// CHECKs in this independently verified relationship/structure contract.
	rows, err = conn.Query(ctx, `SELECT conname::text AS constraint_name,contype::text AS constraint_type,pg_get_constraintdef(oid,true) AS definition,convalidated AS is_validated,condeferrable AS is_deferrable,condeferred AS is_deferred FROM pg_constraint WHERE conrelid='public.session_lifecycle_cutovers'::regclass AND contype<>'n' ORDER BY conname`)
	require.NoError(t, err, "inspect the entire cutover constraint set including the absence of any cutoff-message FK")
	constraints, err := pgx.CollectRows(rows, pgx.RowToStructByName[constraint])
	require.NoError(t, err, "collect every exact check primary-key and reference definition rather than names alone")
	require.Equal(t, wantConstraints, constraints, "the passive table must contain only its exact reviewed structural and immediate parent-reference constraints")
	type reference struct {
		Name           string `db:"constraint_name"`
		Delete, Update string
		Match          string
	}
	wantReferences := []reference{{Name: "session_lifecycle_cutovers_org_id_fkey", Delete: "a", Update: "a", Match: "s"}}
	if version >= 337 {
		wantReferences = append(wantReferences, reference{Name: "session_lifecycle_cutovers_session_scope_fk", Delete: "a", Update: "a", Match: "s"})
	}
	rows, err = conn.Query(ctx, `SELECT conname::text AS constraint_name,confdeltype::text AS delete,confupdtype::text AS update,confmatchtype::text AS match FROM pg_constraint WHERE conrelid='public.session_lifecycle_cutovers'::regclass AND contype='f' ORDER BY conname`)
	require.NoError(t, err, "inspect both parent-reference actions and MATCH SIMPLE behavior")
	references, err := pgx.CollectRows(rows, pgx.RowToStructByName[reference])
	require.NoError(t, err, "collect explicit retention actions instead of inferring them from default DDL syntax")
	require.Equal(t, wantReferences, references, "parent references must be immediate validated MATCH SIMPLE NO ACTION without evidence-destroying cascades")
	rows, err = conn.Query(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename='session_lifecycle_cutovers' ORDER BY indexname`)
	require.NoError(t, err, "inspect every installed cutover index rather than only checking the primary-key name")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect exact cutover index definitions")
	require.Equal(t, []string{"CREATE UNIQUE INDEX session_lifecycle_cutovers_pkey ON public.session_lifecycle_cutovers USING btree (org_id, session_id)"}, indexes, "the passive cutover table must add only its composite tenant/session primary index")
	var ready bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT bool_and(indisprimary AND indisunique AND indisvalid AND indisready AND indislive AND indimmediate AND indpred IS NULL AND indexprs IS NULL AND indnkeyatts=2 AND indnatts=2 AND NOT indnullsnotdistinct) FROM pg_index WHERE indrelid='public.session_lifecycle_cutovers'::regclass`).Scan(&ready), "inspect physical primary-index readiness and absence of predicates expressions or INCLUDE columns")
	require.True(t, ready, "the sole tenant/session primary index must be valid live immediate and ready")
}

type cutoverMigrationRecord struct {
	OrgID, SessionID       uuid.UUID
	Version                int64
	State                  string
	Cutoff                 *int64
	Normalization          json.RawMessage
	ActivatedAt            *time.Time
	MinimumWriter          *int32
	ResumabilityCapability json.RawMessage
}

const cutoverMigrationColumns = `org_id,session_id,version,state,legacy_input_cutoff_id,normalization,activated_at,minimum_writer_version,resumability_capability`

func cutoverMigrationArguments(record cutoverMigrationRecord) []any {
	return []any{record.OrgID, record.SessionID, record.Version, record.State, record.Cutoff, record.Normalization, record.ActivatedAt, record.MinimumWriter, record.ResumabilityCapability}
}

func scanCutoverMigrationRecord(row pgx.Row) (cutoverMigrationRecord, error) {
	var record cutoverMigrationRecord
	err := row.Scan(&record.OrgID, &record.SessionID, &record.Version, &record.State, &record.Cutoff, &record.Normalization, &record.ActivatedAt, &record.MinimumWriter, &record.ResumabilityCapability)
	return record, err
}

func insertCutoverMigrationRecord(ctx context.Context, database db.DBTX, arguments []any) (cutoverMigrationRecord, error) {
	return scanCutoverMigrationRecord(database.QueryRow(ctx, `INSERT INTO session_lifecycle_cutovers (`+cutoverMigrationColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+cutoverMigrationColumns, arguments...))
}

func TestSessionLifecycleCutoverRepresentationPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(337), "apply the real complete schema before synthetic cutover representation cases")
	tests := []struct {
		name   string
		update func(*cutoverMigrationRecord)
	}{
		{name: "explicit version zero and shadow state with unknown provenance"},
		{name: "normalizing preserves JSON null independently of SQL null", update: func(record *cutoverMigrationRecord) {
			cutoff, writer := int64(1), int32(0)
			record.State, record.Cutoff, record.MinimumWriter = "normalizing", &cutoff, &writer
			record.Normalization = json.RawMessage(`null`)
		}},
		{name: "maximum integer metadata and opaque active label", update: func(record *cutoverMigrationRecord) {
			cutoff, writer := int64(math.MaxInt64), int32(math.MaxInt32)
			stamp := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
			record.Version, record.State, record.Cutoff, record.MinimumWriter, record.ActivatedAt = math.MaxInt64, "active", &cutoff, &writer, &stamp
			record.Normalization, record.ResumabilityCapability = json.RawMessage(`{"synthetic": true}`), json.RawMessage(`{"future": true}`)
		}},
		{name: "raw future arrays", update: func(record *cutoverMigrationRecord) {
			record.Normalization, record.ResumabilityCapability = json.RawMessage(`[]`), json.RawMessage(`["future", null]`)
		}},
		{name: "raw future scalars", update: func(record *cutoverMigrationRecord) {
			record.Normalization, record.ResumabilityCapability = json.RawMessage(`42`), json.RawMessage(`"future"`)
		}},
		{name: "capability JSON null does not populate normalization", update: func(record *cutoverMigrationRecord) {
			record.ResumabilityCapability = json.RawMessage(`null`)
		}},
		{name: "active state alone does not populate activation or capability proof", update: func(record *cutoverMigrationRecord) { record.State = "active" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			before := epochMigrationLegacyRows(t, ctx, actor, fixture)
			want := cutoverMigrationRecord{OrgID: fixture.orgID, SessionID: fixture.sessionID, State: "shadow"}
			if tt.update != nil {
				tt.update(&want)
			}
			if want.ActivatedAt != nil {
				var stamp time.Time
				require.NoError(t, actor.QueryRow(ctx, `SELECT $1::timestamptz`, *want.ActivatedAt).Scan(&stamp), "normalize only the existing driver timestamp location representation before exact row comparison")
				want.ActivatedAt = &stamp
			}
			actual, err := insertCutoverMigrationRecord(ctx, actor, cutoverMigrationArguments(want))
			require.NoError(t, err, "structural cutover storage must accept explicit identity and raw metadata without interpreting proof")
			require.Equal(t, want, actual, "every cutover field must round-trip exactly including nullable JSON and full-width integer boundaries")
			stored, err := scanCutoverMigrationRecord(actor.QueryRow(ctx, `SELECT `+cutoverMigrationColumns+` FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID))
			require.NoError(t, err, "read back only the exact synthetic tenant/session cutover")
			require.Equal(t, want, stored, "persisted passive metadata must not infer epochs normalization state or a capability envelope")
			require.Equal(t, before, epochMigrationLegacyRows(t, ctx, actor, fixture), "storing cutover observations must not mutate any legacy session thread or changeset field")
		})
	}
}

func TestSessionLifecycleCutoverStructuralRejectionPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(337), "install all cutover constraints before testing exact structural rejections")
	tests := []struct {
		name, constraint, column string
		position                 int
		value                    any
		code                     string
		omit                     string
	}{
		{name: "null organization", position: 0, code: "23502", column: "org_id"},
		{name: "null session", position: 1, code: "23502", column: "session_id"},
		{name: "null version", position: 2, code: "23502", column: "version"},
		{name: "null state", position: 3, code: "23502", column: "state"},
		{name: "omitted version has no default", omit: "version", code: "23502", column: "version"},
		{name: "omitted state has no default", omit: "state", code: "23502", column: "state"},
		{name: "negative version", position: 2, value: int64(-1), code: "23514", constraint: "session_lifecycle_cutovers_version_check"},
		{name: "empty state", position: 3, value: "", code: "23514", constraint: "session_lifecycle_cutovers_state_check"},
		{name: "unknown state", position: 3, value: "future", code: "23514", constraint: "session_lifecycle_cutovers_state_check"},
		{name: "zero cutoff", position: 4, value: int64(0), code: "23514", constraint: "session_lifecycle_cutovers_cutoff_check"},
		{name: "negative cutoff", position: 4, value: int64(-1), code: "23514", constraint: "session_lifecycle_cutovers_cutoff_check"},
		{name: "negative minimum writer", position: 7, value: int32(-1), code: "23514", constraint: "session_lifecycle_cutovers_minimum_writer_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			record := cutoverMigrationRecord{OrgID: fixture.orgID, SessionID: fixture.sessionID, State: "shadow"}
			var err error
			switch tt.omit {
			case "version":
				_, err = actor.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,state) VALUES($1,$2,$3)`, record.OrgID, record.SessionID, record.State)
			case "state":
				_, err = actor.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version) VALUES($1,$2,$3)`, record.OrgID, record.SessionID, record.Version)
			default:
				arguments := cutoverMigrationArguments(record)
				arguments[tt.position] = tt.value
				_, err = insertCutoverMigrationRecord(ctx, actor, arguments)
			}
			requireCutoverMigrationFailure(t, err, tt.code, tt.constraint, tt.column, tt.name)
			var count int64
			require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID).Scan(&count), "inspect only the rejected case's exact tenant/session")
			require.Equal(t, int64(0), count, "a rejected structural record must not leave any partial cutover metadata")
		})
	}
}

func requireCutoverMigrationFailure(t *testing.T, err error, code, constraint, column, label string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, label+" must fail at the actual PostgreSQL representation boundary")
	require.Equal(t, code, pgErr.Code, label+" must preserve the exact expected SQLSTATE")
	require.Equal(t, constraint, pgErr.ConstraintName, label+" must fail at the intended constraint rather than an unrelated relationship")
	require.Equal(t, column, pgErr.ColumnName, label+" must identify the expected not-null column when applicable")
}

func TestSessionLifecycleCutoverMembershipPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(337), "install the exact tenant/session reference before exercising synthetic membership")
	tests := []struct{ name string }{{name: "missing organization"}, {name: "missing session"}, {name: "foreign session"}, {name: "duplicate tenant session"}, {name: "same tenant different sessions remain independent"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			want := cutoverMigrationRecord{OrgID: fixture.orgID, SessionID: fixture.sessionID, State: "shadow"}
			constraint := "session_lifecycle_cutovers_session_scope_fk"
			code := "23503"
			switch tt.name {
			case "missing organization":
				want.OrgID = uuid.New()
				constraint = "session_lifecycle_cutovers_org_id_fkey"
			case "missing session":
				want.SessionID = uuid.New()
			case "foreign session":
				foreign := seedEpochMigrationFixture(t, ctx, actor)
				want.SessionID = foreign.sessionID
			case "duplicate tenant session":
				_, err := insertCutoverMigrationRecord(ctx, actor, cutoverMigrationArguments(want))
				require.NoError(t, err, "establish the exact tenant/session identity before attempting a conflicting second row")
				want.Version, want.State = 17, "active"
				constraint, code = "session_lifecycle_cutovers_pkey", "23505"
			case "same tenant different sessions remain independent":
				_, err := insertCutoverMigrationRecord(ctx, actor, cutoverMigrationArguments(want))
				require.NoError(t, err, "create the first cutover without supplying an implicit version or state")
				sibling := newDispatchSiblingScope(t, ctx, actor, fixture.orgID)
				want.SessionID, want.Version, want.State = sibling.epochs.sessionID, math.MaxInt64, "normalizing"
				actual, err := insertCutoverMigrationRecord(ctx, actor, cutoverMigrationArguments(want))
				require.NoError(t, err, "the composite primary key must permit another session in the same tenant")
				require.Equal(t, want, actual, "independent session metadata must retain its exact own row values")
				return
			}
			_, err := insertCutoverMigrationRecord(ctx, actor, cutoverMigrationArguments(want))
			requireCutoverMigrationFailure(t, err, code, constraint, "", tt.name)
			if tt.name == "duplicate tenant session" {
				stored, err := scanCutoverMigrationRecord(actor.QueryRow(ctx, `SELECT `+cutoverMigrationColumns+` FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID))
				require.NoError(t, err, "reload the first cutover after rejecting a conflicting version and state")
				require.Equal(t, cutoverMigrationRecord{OrgID: fixture.orgID, SessionID: fixture.sessionID, State: "shadow"}, stored, "duplicate identity rejection must preserve every field of the original row rather than merge or replace it")
			}
		})
	}
}

func TestSessionLifecycleCutoverParentRetentionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, constraint string
		version          uint
	}{
		{name: "organization", constraint: "session_lifecycle_cutovers_org_id_fkey", version: 336},
		{name: "session", constraint: "session_lifecycle_cutovers_session_scope_fk", version: 337},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			require.NoError(t, migration.Migrate(tt.version), "use the exact cutover FK boundary needed to isolate its retention effect")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			record := cutoverMigrationRecord{OrgID: uuid.New(), SessionID: uuid.New(), State: "shadow"}
			query := `DELETE FROM organizations WHERE id=$1`
			args := []any{record.OrgID}
			if tt.name == "organization" {
				_, err := conn.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic cutover retention')`, record.OrgID)
				require.NoError(t, err, "create only the owned synthetic organization so no legacy parent relationship can satisfy this retention proof")
			} else {
				fixture := seedEpochMigrationFixture(t, ctx, conn)
				record.OrgID, record.SessionID = fixture.orgID, fixture.sessionID
				query, args = `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, []any{record.OrgID, record.SessionID}
			}
			_, err := insertCutoverMigrationRecord(ctx, conn, cutoverMigrationArguments(record))
			require.NoError(t, err, "store the exact synthetic parent reference before probing deletion")
			before := cutoverMigrationRowJSON(t, ctx, conn, record.OrgID, record.SessionID)
			var deletionErr error
			if tt.name == "session" {
				tag, err := conn.Exec(ctx, query, args...)
				require.NoError(t, err, "ordinary session deletion must preserve the existing soft-delete trigger behavior")
				require.Equal(t, int64(0), tag.RowsAffected(), "ordinary session DELETE must remain suppressed rather than physically deleting retained lifecycle evidence")
				var softDeleted bool
				require.NoError(t, conn.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM sessions WHERE org_id=$1 AND id=$2`, record.OrgID, record.SessionID).Scan(&softDeleted), "observe the existing session soft-delete marker")
				require.True(t, softDeleted, "adding a cutover FK must not replace or disable the existing soft-deletion policy")
				require.Equal(t, before, cutoverMigrationRowJSON(t, ctx, conn, record.OrgID, record.SessionID), "ordinary soft deletion must leave all cutover metadata untouched")
				var parentBefore json.RawMessage
				parentQuery := `SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2`
				require.NoError(t, conn.QueryRow(ctx, parentQuery, record.OrgID, record.SessionID).Scan(&parentBefore), "capture the complete session after its legitimate soft-delete transition")
				var gateBefore *string
				gateQuery := `SELECT NULLIF(current_setting('app.allow_hard_delete',true),'')`
				require.NoError(t, conn.QueryRow(ctx, gateQuery).Scan(&gateBefore), "inspect the existing hard-delete gate before the owned probe transaction")
				require.Nil(t, gateBefore, "the fixture must not enable hard deletion outside the explicit probe transaction")
				tx, err := conn.Begin(ctx)
				require.NoError(t, err, "begin an owned disposable transaction solely to exercise physical FK retention")
				defer func() { _ = tx.Rollback(ctx) }()
				_, err = tx.Exec(ctx, `SET LOCAL app.allow_hard_delete='true'`)
				require.NoError(t, err, "use the existing hard-delete test gate only inside this owned disposable transaction")
				_, deletionErr = tx.Exec(ctx, query, args...)
				require.NoError(t, tx.Rollback(ctx), "roll back the full physical-delete probe before inspecting preserved data")
				var parentAfter json.RawMessage
				require.NoError(t, conn.QueryRow(ctx, parentQuery, record.OrgID, record.SessionID).Scan(&parentAfter), "read every retained session field after rolling back physical deletion")
				require.Equal(t, parentBefore, parentAfter, "the failed hard-delete probe must restore every parent field while preserving its earlier soft-delete marker")
				var gateAfter *string
				require.NoError(t, conn.QueryRow(ctx, gateQuery).Scan(&gateAfter), "inspect the transaction-local hard-delete gate after rollback")
				require.Equal(t, gateBefore, gateAfter, "the probe must leave hard deletion semantically unset outside its transaction")
			} else {
				_, deletionErr = conn.Exec(ctx, query, args...)
			}
			var pgErr *pgconn.PgError
			require.ErrorAs(t, deletionErr, &pgErr, "physical parent deletion must fail on a real retained cutover relationship")
			require.Equal(t, "23503", pgErr.Code, "parent retention must be a PostgreSQL FK violation rather than a synthetic application error")
			require.Equal(t, tt.constraint, pgErr.ConstraintName, "the exact new cutover parent FK must cause the refusal")
			require.Equal(t, "session_lifecycle_cutovers", pgErr.TableName, "an unrelated legacy child's retention must not satisfy this probe")
			require.Equal(t, before, cutoverMigrationRowJSON(t, ctx, conn, record.OrgID, record.SessionID), "rejected physical deletion must preserve every cutover field without cascade or normalization")
			version, dirty, err := migration.Version()
			require.NoError(t, err, "inspect the ledger after direct data deletion rather than a failed schema rollback")
			require.Equal(t, tt.version, version, "direct parent-deletion refusal must not alter the migration version")
			require.False(t, dirty, "direct data refusal must leave the existing clean migration ledger unchanged")
		})
	}
}

func cutoverMigrationRowJSON(t *testing.T, ctx context.Context, conn *pgx.Conn, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(c) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2`, orgID, sessionID).Scan(&raw), "capture every cutover field without interpreting a state label cutoff watermark or capability JSON as proof")
	return raw
}
