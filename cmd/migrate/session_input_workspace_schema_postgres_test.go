package main

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionInputWorkspaceSchemaMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh complete schema"},
		{name: "populated prior graph upgrades each hot-parent boundary", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := uint(340)
			if tt.upgrade {
				start = 337
			}
			require.NoError(t, migration.Migrate(start), "apply the exact real full-schema workspace compatibility prerequisite")
			fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			seedLifecycleReferenceKeyMetadata(t, ctx, conn, fixture)
			f := fixture.executor
			var requestID uuid.UUID
			require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 ORDER BY id LIMIT 1`, f.org, f.session).Scan(&requestID), "select an actual prior request for the retained dispatch identity")
			epoch := int64(math.MaxInt64)
			insertDispatchIdentityRetentionRow(t, ctx, conn, dispatchIdentityRetentionRow{orgID: f.org, sessionID: f.session, threadID: &f.thread, requestID: &requestID, sessionEpoch: &epoch, kind: "answer", payload: `{"synthetic":"workspace-prerequisite"}`})
			_, err := insertCutoverMigrationRecord(ctx, conn, cutoverMigrationArguments(cutoverMigrationRecord{OrgID: f.org, SessionID: f.session, Version: math.MaxInt64, State: "normalizing", Normalization: json.RawMessage(`{"synthetic":9223372036854775807}`)}))
			require.NoError(t, err, "retain a populated prior cutover row rather than testing preservation of an empty table")
			before := workspaceMigrationPriorRows(t, ctx, conn, fixture)
			projection := cutoverMigrationLegacyProjection(t, ctx, conn, fixture)
			indexes := workspaceMigrationPriorIndexes(t, ctx, conn)
			assertBoundary := func(version uint) {
				actual, dirty, err := migration.Version()
				require.NoError(t, err, "read the migration ledger after each separate workspace representation boundary")
				require.Equal(t, version, actual, "each table or hot-parent FK must settle at its exact own migration version")
				require.False(t, dirty, "successful workspace schema transitions must leave a clean ledger")
				require.Equal(t, before, workspaceMigrationPriorRows(t, ctx, conn, fixture), "workspace migration must preserve every prior graph field including dispatch and cutover metadata without float conversion")
				require.Equal(t, projection, cutoverMigrationLegacyProjection(t, ctx, conn, fixture), "all prior public session lane request and execution models must remain identical")
				require.Equal(t, indexes, workspaceMigrationPriorIndexes(t, ctx, conn), "every preexisting index including the cutover primary index must remain byte-identical")
				requireWorkspaceMigrationCatalog(t, ctx, conn, version)
			}
			assertBoundary(start)
			// These migration boundaries intentionally share one connection and
			// run sequentially; each independent database category is parallel.
			if tt.upgrade {
				for _, version := range []uint{338, 339, 340} {
					require.NoError(t, migration.Migrate(version), "install each workspace table/session/request boundary independently")
					assertBoundary(version)
				}
			}
			for _, version := range []uint{339, 338, 337} {
				require.NoError(t, migration.Steps(-1), "remove only the next FK or empty workspace table without CASCADE")
				assertBoundary(version)
			}
			for _, version := range []uint{338, 339, 340} {
				require.NoError(t, migration.Migrate(version), "reapply passive representation without deriving a compatibility row")
				assertBoundary(version)
			}
		})
	}
}

func workspaceMigrationPriorRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) json.RawMessage {
	t.Helper()
	prior := cutoverMigrationLegacyRows(t, ctx, conn, fixture)
	var value json.RawMessage
	err := conn.QueryRow(ctx, `SELECT $3::jsonb || jsonb_build_object('cutovers',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.session_id) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2))`, fixture.executor.org, fixture.executor.session, prior).Scan(&value)
	require.NoError(t, err, "capture complete prior rows including already installed cutover representation")
	return value
}

func workspaceMigrationPriorIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes WHERE schemaname='public' AND tablename<>'session_input_workspace_compatibility' ORDER BY tablename,indexname`)
	require.NoError(t, err, "inspect every public index except only the new workspace table's own indexes")
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect complete index definitions including the prior cutover primary key")
	return indexes
}

func requireWorkspaceMigrationCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint) {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.session_input_workspace_compatibility') IS NOT NULL`).Scan(&exists), "inspect workspace table existence at each schema boundary")
	require.Equal(t, version >= 338, exists, "only migration 338 creates or removes workspace representation")
	if !exists {
		return
	}
	var count int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_input_workspace_compatibility`).Scan(&count), "inspect table contents independently of the new raw reader")
	require.Equal(t, int64(0), count, "up down and reapply must never backfill or classify a request")
	type column struct {
		Name     string  `db:"column_name"`
		Type     string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	wantColumns := []column{
		{Name: "org_id", Type: "uuid", Nullable: "NO"}, {Name: "session_id", Type: "uuid", Nullable: "NO"}, {Name: "request_id", Type: "uuid", Nullable: "NO"}, {Name: "state", Type: "text", Nullable: "NO"},
		{Name: "anchor_digest", Type: "text", Nullable: "YES"}, {Name: "context_scope_digest", Type: "text", Nullable: "YES"}, {Name: "workspace_generation", Type: "bigint", Nullable: "YES"}, {Name: "workspace_revision", Type: "bigint", Nullable: "YES"},
		{Name: "snapshot_key", Type: "text", Nullable: "YES"}, {Name: "workspace_state", Type: "jsonb", Nullable: "YES"}, {Name: "last_authority_kind", Type: "text", Nullable: "YES"}, {Name: "last_authority_receipt_id", Type: "uuid", Nullable: "YES"}, {Name: "reason", Type: "text", Nullable: "YES"}, {Name: "next_revalidation_at", Type: "timestamp with time zone", Nullable: "YES"},
	}
	rows, err := conn.Query(ctx, `SELECT column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_input_workspace_compatibility' ORDER BY ordinal_position`)
	require.NoError(t, err, "inspect every column's exact declaration order type nullability and default")
	columns, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect the entire workspace column shape rather than selected fields")
	require.Equal(t, wantColumns, columns, "all raw provenance must remain independently nullable and no column may infer a default")
	type constraint struct {
		Name       string `db:"name"`
		Kind       string `db:"kind"`
		Definition string `db:"definition"`
		Validated  bool   `db:"validated"`
		Deferrable bool   `db:"deferrable"`
		Deferred   bool   `db:"deferred"`
		Delete     string `db:"delete"`
		Update     string `db:"update"`
		Match      string `db:"match"`
	}
	wantConstraints := []constraint{
		{Name: "session_input_workspace_compatibility_org_id_fkey", Kind: "f", Definition: "FOREIGN KEY (org_id) REFERENCES organizations(id)", Validated: true, Delete: "a", Update: "a", Match: "s"},
		{Name: "session_input_workspace_compatibility_pkey", Kind: "p", Definition: "PRIMARY KEY (org_id, request_id)", Validated: true},
		{Name: "session_input_workspace_compatibility_state_check", Kind: "c", Definition: "CHECK (state = ANY (ARRAY['certified'::text, 'pending'::text, 'unavailable'::text, 'unprovable'::text]))", Validated: true},
	}
	if version >= 339 {
		wantConstraints = append(wantConstraints, constraint{Name: "session_input_workspace_compatibility_session_scope_fk", Kind: "f", Definition: "FOREIGN KEY (org_id, session_id) REFERENCES sessions(org_id, id)", Validated: true, Delete: "a", Update: "a", Match: "s"})
	}
	if version >= 340 {
		wantConstraints = append(wantConstraints, constraint{Name: "session_input_workspace_compatibility_request_scope_fk", Kind: "f", Definition: "FOREIGN KEY (org_id, session_id, request_id) REFERENCES session_human_input_requests(org_id, session_id, id)", Validated: true, Delete: "a", Update: "a", Match: "s"})
	}
	slices.SortFunc(wantConstraints, func(a, b constraint) int { return strings.Compare(a.Name, b.Name) })
	rows, err = conn.Query(ctx, `SELECT conname::text AS name,contype::text AS kind,pg_get_constraintdef(oid,true) AS definition,convalidated AS validated,condeferrable AS deferrable,condeferred AS deferred,CASE WHEN contype='f' THEN confdeltype::text ELSE '' END AS delete,CASE WHEN contype='f' THEN confupdtype::text ELSE '' END AS update,CASE WHEN contype='f' THEN confmatchtype::text ELSE '' END AS match FROM pg_constraint WHERE conrelid='public.session_input_workspace_compatibility'::regclass AND contype<>'n' ORDER BY conname`)
	require.NoError(t, err, "inspect the complete reviewed constraint set including FK actions and absence of semantic proof checks")
	constraints, err := pgx.CollectRows(rows, pgx.RowToStructByName[constraint])
	require.NoError(t, err, "collect exact primary state and parent relationships at the current boundary")
	require.Equal(t, wantConstraints, constraints, "workspace references must be validated immediate MATCH SIMPLE NO ACTION with no hidden numeric digest JSON or authority constraints")
	type index struct {
		Name             string
		Definition       string
		Primary          bool
		Unique           bool
		Valid            bool
		Ready            bool
		Live             bool
		Immediate        bool
		Keys             int16
		Attributes       int16
		Predicate        *string
		Expression       *string
		NullsNotDistinct bool `db:"nulls_not_distinct"`
	}
	pending := `(state = 'pending'::text)`
	wantIndexes := []index{
		{Name: "idx_session_input_workspace_compatibility_due", Definition: "CREATE INDEX idx_session_input_workspace_compatibility_due ON public.session_input_workspace_compatibility USING btree (next_revalidation_at, request_id) WHERE (state = 'pending'::text)", Valid: true, Ready: true, Live: true, Immediate: true, Keys: 2, Attributes: 2, Predicate: &pending},
		{Name: "idx_session_input_workspace_compatibility_session", Definition: "CREATE INDEX idx_session_input_workspace_compatibility_session ON public.session_input_workspace_compatibility USING btree (org_id, session_id, request_id)", Valid: true, Ready: true, Live: true, Immediate: true, Keys: 3, Attributes: 3},
		{Name: "session_input_workspace_compatibility_pkey", Definition: "CREATE UNIQUE INDEX session_input_workspace_compatibility_pkey ON public.session_input_workspace_compatibility USING btree (org_id, request_id)", Primary: true, Unique: true, Valid: true, Ready: true, Live: true, Immediate: true, Keys: 2, Attributes: 2},
	}
	rows, err = conn.Query(ctx, `SELECT c.relname AS name,pg_get_indexdef(i.indexrelid) AS definition,i.indisprimary AS primary,i.indisunique AS unique,i.indisvalid AS valid,i.indisready AS ready,i.indislive AS live,i.indimmediate AS immediate,i.indnkeyatts AS keys,i.indnatts AS attributes,pg_get_expr(i.indpred,i.indrelid) AS predicate,pg_get_expr(i.indexprs,i.indrelid) AS expression,i.indnullsnotdistinct AS nulls_not_distinct FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid='public.session_input_workspace_compatibility'::regclass ORDER BY c.relname`)
	require.NoError(t, err, "inspect all three physical indexes including partial predicate key order and readiness")
	indexes, err := pgx.CollectRows(rows, pgx.RowToStructByName[index])
	require.NoError(t, err, "collect exact index definitions without tolerating extra expressions INCLUDE columns or inferred uniqueness")
	require.Equal(t, wantIndexes, indexes, "the PK session lookup and pending-only due index must have precisely the reviewed complete shapes")
}

func TestSessionInputWorkspaceStructuralRejectionPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "install all workspace structural constraints before parallel rejection cases")
	tests := []struct {
		name                     string
		position                 int
		value                    any
		code, column, constraint string
		omitState, duplicate     bool
	}{
		{name: "null tenant", position: 0, code: "23502", column: "org_id"},
		{name: "null session", position: 1, code: "23502", column: "session_id"},
		{name: "null request", position: 2, code: "23502", column: "request_id"},
		{name: "null state", position: 3, code: "23502", column: "state"},
		{name: "omitted state has no default", omitState: true, code: "23502", column: "state"},
		{name: "empty state", position: 3, value: "", code: "23514", constraint: "session_input_workspace_compatibility_state_check"},
		{name: "unknown state", position: 3, value: "future", code: "23514", constraint: "session_input_workspace_compatibility_state_check"},
		{name: "case is not normalized", position: 3, value: "CERTIFIED", code: "23514", constraint: "session_input_workspace_compatibility_state_check"},
		{name: "one row per tenant request", duplicate: true, code: "23505", constraint: "session_input_workspace_compatibility_pkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			request := fixture.requests[0]
			record := workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified}}
			arguments := workspaceMigrationArguments(record)
			if tt.duplicate {
				_, err := insertWorkspaceMigrationRecord(ctx, actor, arguments)
				require.NoError(t, err, "establish the original exact request row before rejecting duplicate metadata")
				arguments[3] = models.InputWorkspaceCompatibilityStatePending
			} else {
				arguments[tt.position] = tt.value
			}
			var err error
			if tt.omitState {
				_, err = actor.Exec(ctx, `INSERT INTO session_input_workspace_compatibility(org_id,session_id,request_id) VALUES($1,$2,$3)`, request.OrgID, request.SessionID, request.ID)
			} else {
				_, err = insertWorkspaceMigrationRecord(ctx, actor, arguments)
			}
			requireCutoverMigrationFailure(t, err, tt.code, tt.constraint, tt.column, tt.name)
			var count int64
			require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND request_id=$3`, request.OrgID, request.SessionID, request.ID).Scan(&count), "inspect only this rejected case's exact scope")
			wantCount := int64(0)
			if tt.duplicate {
				wantCount = 1
				actual, readErr := db.NewSessionHumanInputRequestStore(actor).GetWorkspaceCompatibility(ctx, request.OrgID, request.SessionID, request.ID)
				require.NoError(t, readErr, "read the original metadata after duplicate rejection")
				require.Equal(t, record.Metadata, actual, "duplicate rejection must preserve the entire first row rather than merge or replace it")
			}
			require.Equal(t, wantCount, count, "rejection must leave no partial row and must retain an existing duplicate's original row")
		})
	}
}

func TestSessionInputWorkspaceForeignKeyBoundariesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		version    uint
		constraint string
		scope      string
	}{
		{name: "missing tenant at organization-only boundary", version: 338, constraint: "session_input_workspace_compatibility_org_id_fkey", scope: "org"},
		{name: "missing session before request FK exists", version: 339, constraint: "session_input_workspace_compatibility_session_scope_fk", scope: "missing session"},
		{name: "foreign tenant session before request FK exists", version: 339, constraint: "session_input_workspace_compatibility_session_scope_fk", scope: "foreign session"},
		{name: "missing request with a valid exact session", version: 340, constraint: "session_input_workspace_compatibility_request_scope_fk", scope: "missing request"},
		{name: "foreign tenant request with a valid exact session", version: 340, constraint: "session_input_workspace_compatibility_request_scope_fk", scope: "foreign request"},
		{name: "same tenant wrong parent request", version: 340, constraint: "session_input_workspace_compatibility_request_scope_fk", scope: "sibling request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			require.NoError(t, migration.Migrate(tt.version), "isolate the intended FK so PostgreSQL trigger order cannot satisfy the wrong rejection proof")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := seedRequestEpochMigrationFixture(t, ctx, conn)
			request := fixture.requests[0]
			record := workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending}}
			switch tt.scope {
			case "org":
				record.OrgID = uuid.New()
			case "missing session":
				record.SessionID = uuid.New()
			case "foreign session":
				other := seedEpochMigrationFixture(t, ctx, conn)
				record.SessionID = other.sessionID
			case "missing request":
				record.RequestID = uuid.New()
			case "foreign request":
				other := seedRequestEpochMigrationFixture(t, ctx, conn)
				record.RequestID = other.requests[0].ID
			case "sibling request":
				other := newDispatchSiblingScope(t, ctx, conn, request.OrgID)
				record.RequestID = other.requests[0].ID
			}
			_, err := insertWorkspaceMigrationRecord(ctx, conn, workspaceMigrationArguments(record))
			requireCutoverMigrationFailure(t, err, "23503", tt.constraint, "", tt.name)
			var count int64
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_input_workspace_compatibility WHERE org_id=$1 AND request_id=$2`, record.OrgID, record.RequestID).Scan(&count), "check the rejected exact tenant/request identity without reading another tenant")
			require.Equal(t, int64(0), count, "a failed parent association must leave no compatibility row")
		})
	}
}
