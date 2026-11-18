package main

import (
	"context"
	"encoding/json"
	"math"
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

func TestSessionLaneClaimSchemaMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh complete schema"},
		{name: "populated graph upgrades and validates independently", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := uint(344)
			if tt.upgrade {
				start = 340
			}
			require.NoError(t, migration.Migrate(start), "apply the exact full-chain lane-claim prerequisite or fresh final schema")
			fixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			seedLifecycleReferenceKeyMetadata(t, ctx, conn, fixture)
			f := fixture.executor
			var request uuid.UUID
			require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 ORDER BY id LIMIT 1`, f.org, f.session).Scan(&request), "select a real prior request before seeding the complete retained graph")
			epoch := int64(math.MaxInt64)
			insertDispatchIdentityRetentionRow(t, ctx, conn, dispatchIdentityRetentionRow{orgID: f.org, sessionID: f.session, threadID: &f.thread, requestID: &request, sessionEpoch: &epoch, kind: "answer", payload: `{"synthetic":"lane-claim-prerequisite"}`})
			_, err := insertCutoverMigrationRecord(ctx, conn, cutoverMigrationArguments(cutoverMigrationRecord{OrgID: f.org, SessionID: f.session, Version: math.MaxInt64, State: "normalizing", Normalization: json.RawMessage(`{"synthetic":9223372036854775807}`)}))
			require.NoError(t, err, "preserve a populated cutover rather than merely an empty prior table")
			_, err = insertWorkspaceMigrationRecord(ctx, conn, workspaceMigrationArguments(workspaceMigrationRecord{OrgID: f.org, SessionID: f.session, RequestID: request, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceGeneration: workspaceMigrationPointer(int64(math.MinInt64)), WorkspaceState: json.RawMessage(`null`)}}))
			require.NoError(t, err, "preserve the preceding workspace slice's exact NULL JSON and signed metadata")
			before := laneClaimMigrationPriorRows(t, ctx, conn, fixture)
			projection := cutoverMigrationLegacyProjection(t, ctx, conn, fixture)
			publicJSON, err := json.Marshal(projection)
			require.NoError(t, err, "capture the complete preexisting public model envelope")
			indexes := laneClaimMigrationPriorIndexes(t, ctx, conn)
			assertBoundary := func(version uint, validated bool) {
				actual, dirty, err := migration.Version()
				require.NoError(t, err, "read the exact real migration ledger boundary")
				require.Equal(t, version, actual, "each dispatch slot index lane slot and validation step must commit at its own version")
				require.False(t, dirty, "successful schema transitions must retain a clean migration ledger")
				require.Equal(t, before, laneClaimMigrationPriorRows(t, ctx, conn, fixture), "all prior graph fields including exact bigint workspace dispatch and cutover metadata must remain unchanged")
				actualProjection := cutoverMigrationLegacyProjection(t, ctx, conn, fixture)
				require.Equal(t, projection, actualProjection, "nullable claim representation must not alter any existing session lane request or execution model")
				actualJSON, err := json.Marshal(actualProjection)
				require.NoError(t, err, "encode the complete unchanged API projection after each schema transition")
				require.Equal(t, publicJSON, actualJSON, "claim metadata must not leak into or change existing public API JSON")
				require.Equal(t, indexes, laneClaimMigrationPriorIndexes(t, ctx, conn), "all preexisting public indexes must remain byte-identical")
				requireLaneClaimMigrationCatalog(t, ctx, conn, version, validated)
				var counts [3]int64
				require.NoError(t, conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_input_dispatches),(SELECT count(*) FROM session_lifecycle_cutovers),(SELECT count(*) FROM session_input_workspace_compatibility)`).Scan(&counts[0], &counts[1], &counts[2]), "count all prior dispatch cutover and workspace rows rather than only the selected graph")
				require.Equal(t, [3]int64{1, 1, 1}, counts, "schema up down validation and reapply must neither backfill new records nor remove existing passive records")
				if version >= 341 {
					var absent bool
					require.NoError(t, conn.QueryRow(ctx, `SELECT bool_and(admission_claim_token IS NULL AND admission_claim_turn IS NULL) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&absent), "inspect existing dispatch observations independently of model readers")
					require.True(t, absent, "old dispatches must retain unknown token and turn rather than receive inferred claim provenance")
				}
				if version >= 343 {
					var absent bool
					require.NoError(t, conn.QueryRow(ctx, `SELECT bool_and(admission_claim_dispatch_id IS NULL AND admission_claim_token IS NULL) FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&absent), "inspect all retained lanes rather than only the selected secondary thread")
					require.True(t, absent, "schema migration must not install any claim or infer one from running status parent epochs or existing dispatches")
				}
			}
			assertBoundary(start, start == 344)
			// Dependent schema boundaries deliberately share this isolated
			// connection; the two complete database fixtures run in parallel.
			if tt.upgrade {
				for _, version := range []uint{341, 342, 343, 344} {
					require.NoError(t, migration.Migrate(version), "install only the next independently committed schema boundary")
					assertBoundary(version, version == 344)
				}
			}
			for _, version := range []uint{343, 342, 341, 340} {
				require.NoError(t, migration.Steps(-1), "roll back one dependency in reverse order without CASCADE or rewriting prior metadata")
				// Validation is monotonic: 344 down intentionally retains the
				// validated constraints until 343 actually removes them.
				assertBoundary(version, version == 343)
			}
			for _, version := range []uint{341, 342, 343, 344} {
				require.NoError(t, migration.Migrate(version), "reapply nullable representation without manufacturing a claim")
				assertBoundary(version, version == 344)
			}
			requireLifecycleReferenceKeyLegacyInserts(t, ctx, conn)
			requireRequestEpochLegacyTransitions(t, ctx, conn)
			assertBoundary(344, true)
		})
	}
}

func laneClaimMigrationPriorRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) json.RawMessage {
	t.Helper()
	prior := workspaceMigrationPriorRows(t, ctx, conn, fixture)
	var raw json.RawMessage
	f := fixture.executor
	err := conn.QueryRow(ctx, `SELECT $3::jsonb || jsonb_build_object(
		'threads',(SELECT jsonb_agg(to_jsonb(t)-'admission_claim_dispatch_id'-'admission_claim_token' ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
		'dispatches',(SELECT jsonb_agg(to_jsonb(d)-'admission_claim_token'-'admission_claim_turn' ORDER BY d.id) FROM session_input_dispatches d WHERE d.org_id=$1 AND d.session_id=$2),
		'workspace_compatibility',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.request_id) FROM session_input_workspace_compatibility w WHERE w.org_id=$1 AND w.session_id=$2))`, f.org, f.session, prior).Scan(&raw)
	require.NoError(t, err, "normalize only this slice's four new columns while retaining every complete prior row")
	return raw
}

func laneClaimMigrationPriorIndexes(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes WHERE schemaname='public' AND indexname<>'idx_session_input_dispatches_lane_claim' ORDER BY tablename,indexname`)
	require.NoError(t, err, "inspect every prior index including all dispatch referenced-key and workspace indexes")
	actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "capture exact existing index definitions rather than only names or counts")
	return actual
}

func requireLaneClaimMigrationCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint, validated bool) {
	t.Helper()
	type column struct {
		Table    string  `db:"table_name"`
		Name     string  `db:"column_name"`
		Type     string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	wantColumns := []column{}
	if version >= 341 {
		wantColumns = append(wantColumns, column{Table: "session_input_dispatches", Name: "admission_claim_token", Type: "uuid", Nullable: "YES"}, column{Table: "session_input_dispatches", Name: "admission_claim_turn", Type: "integer", Nullable: "YES"})
	}
	if version >= 343 {
		wantColumns = append(wantColumns, column{Table: "session_threads", Name: "admission_claim_dispatch_id", Type: "uuid", Nullable: "YES"}, column{Table: "session_threads", Name: "admission_claim_token", Type: "uuid", Nullable: "YES"})
	}
	rows, err := conn.Query(ctx, `SELECT table_name,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND column_name IN ('admission_claim_dispatch_id','admission_claim_token','admission_claim_turn') ORDER BY table_name,column_name`)
	require.NoError(t, err, "inspect all claim columns without excluding possible accidental session-wide slots")
	columns, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect complete claim types NULL policies and default expressions")
	require.Equal(t, wantColumns, columns, "only dispatch token/turn and lane pointer/token may be added; all must be nullable and default-free")
	type constraint struct {
		Name       string `db:"name"`
		Definition string `db:"definition"`
		Validated  bool   `db:"validated"`
		Deferrable bool   `db:"deferrable"`
		Deferred   bool   `db:"deferred"`
		Delete     string `db:"delete"`
		Update     string `db:"update"`
		Match      string `db:"match"`
		ParentKey  string `db:"parent_key"`
	}
	wantConstraints := []constraint{}
	if version >= 343 {
		suffix := ""
		if !validated {
			suffix = " NOT VALID"
		}
		wantConstraints = []constraint{
			{Name: "session_threads_admission_claim_pair_check", Definition: "CHECK ((admission_claim_dispatch_id IS NULL) = (admission_claim_token IS NULL))" + suffix, Validated: validated},
			{Name: "session_threads_admission_claim_scope_fk", Definition: "FOREIGN KEY (org_id, session_id, id, admission_claim_dispatch_id, admission_claim_token) REFERENCES session_input_dispatches(org_id, session_id, thread_id, id, admission_claim_token)" + suffix, Validated: validated, Delete: "a", Update: "a", Match: "s", ParentKey: "idx_session_input_dispatches_lane_claim"},
		}
	}
	rows, err = conn.Query(ctx, `SELECT c.conname::text AS name,pg_get_constraintdef(c.oid,true) AS definition,c.convalidated AS validated,c.condeferrable AS deferrable,c.condeferred AS deferred,
		CASE WHEN c.contype='f' THEN c.confdeltype::text ELSE '' END AS delete,CASE WHEN c.contype='f' THEN c.confupdtype::text ELSE '' END AS update,
		CASE WHEN c.contype='f' THEN c.confmatchtype::text ELSE '' END AS match,COALESCE(i.relname::text,'') AS parent_key
		FROM pg_constraint c LEFT JOIN pg_class i ON i.oid=c.conindid
		WHERE c.conrelid='public.session_threads'::regclass AND c.conname IN ('session_threads_admission_claim_pair_check','session_threads_admission_claim_scope_fk') ORDER BY c.conname`)
	require.NoError(t, err, "inspect exact pair equality FK column order token binding validation and immediate NO ACTION policy")
	constraints, err := pgx.CollectRows(rows, pgx.RowToStructByName[constraint])
	require.NoError(t, err, "collect complete lane structural constraints and their physical referenced key")
	require.Equal(t, wantConstraints, constraints, "the pair must prevent MATCH SIMPLE half-null escape and the FK must bind the exact tenant session lane dispatch and token")
	type index struct {
		Name             string  `db:"name"`
		Definition       string  `db:"definition"`
		Unique           bool    `db:"unique"`
		Valid            bool    `db:"valid"`
		Ready            bool    `db:"ready"`
		Live             bool    `db:"live"`
		Immediate        bool    `db:"immediate"`
		Keys             int16   `db:"keys"`
		Attributes       int16   `db:"attributes"`
		Predicate        *string `db:"predicate"`
		Expression       *string `db:"expression"`
		NullsNotDistinct bool    `db:"nulls_not_distinct"`
	}
	wantIndexes := []index{}
	if version >= 342 {
		wantIndexes = []index{{Name: "idx_session_input_dispatches_lane_claim", Definition: "CREATE UNIQUE INDEX idx_session_input_dispatches_lane_claim ON public.session_input_dispatches USING btree (org_id, session_id, thread_id, id, admission_claim_token)", Unique: true, Valid: true, Ready: true, Live: true, Immediate: true, Keys: 5, Attributes: 5}}
	}
	rows, err = conn.Query(ctx, `SELECT c.relname::text AS name,pg_get_indexdef(i.indexrelid) AS definition,i.indisunique AS unique,i.indisvalid AS valid,i.indisready AS ready,i.indislive AS live,i.indimmediate AS immediate,i.indnkeyatts AS keys,i.indnatts AS attributes,pg_get_expr(i.indpred,i.indrelid) AS predicate,pg_get_expr(i.indexprs,i.indrelid) AS expression,i.indnullsnotdistinct AS nulls_not_distinct FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relnamespace='public'::regnamespace AND c.relname='idx_session_input_dispatches_lane_claim'`)
	require.NoError(t, err, "inspect exact unique parent index key order readiness and absence of predicates or expressions")
	indexes, err := pgx.CollectRows(rows, pgx.RowToStructByName[index])
	require.NoError(t, err, "collect the complete token-bound immediate parent index shape")
	require.Equal(t, wantIndexes, indexes, "the one new referenced key must be complete live valid ready unique immediate and default NULL-distinct")
}

func laneClaimDispatch(t *testing.T, ctx context.Context, database db.DBTX, fixture requestEpochMigrationFixture, thread *uuid.UUID, token *uuid.UUID, turn *int32) dispatchIdentityRecord {
	t.Helper()
	var stamp time.Time
	require.NoError(t, database.QueryRow(ctx, `SELECT '2026-01-01 00:00:00+00'::timestamptz`).Scan(&stamp), "decode the fixed synthetic timestamp through the same PostgreSQL codec as the complete dispatch record")
	record := newDispatchIdentityRecord(fixture, stamp)
	record.ThreadID = thread
	actual, err := insertDispatchIdentityRecord(ctx, database, dispatchIdentityArguments(record))
	require.NoError(t, err, "precreate one unbound synthetic dispatch before installing any lane pointer")
	require.Equal(t, record, actual, "legacy dispatch insertion must preserve its complete existing representation")
	_, err = database.Exec(ctx, `UPDATE session_input_dispatches SET admission_claim_token=$4,admission_claim_turn=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, record.OrgID, record.SessionID, record.ID, token, turn)
	require.NoError(t, err, "set raw nullable dispatch observations without activating admission or scheduling behavior")
	return record
}

func laneClaimSet(ctx context.Context, database db.DBTX, fixture epochMigrationFixture, dispatch, token *uuid.UUID) error {
	_, err := database.Exec(ctx, `UPDATE session_threads SET admission_claim_dispatch_id=$4,admission_claim_token=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.orgID, fixture.sessionID, fixture.threadID, dispatch, token)
	return err
}

func TestSessionLaneClaimStructuralConstraintsPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(344), "install the complete real schema before exercising token-bound lane membership")
	tests := []struct {
		name, scenario, code, constraint string
	}{
		{name: "both NULL remains unclaimed", scenario: "absent"},
		{name: "exact pointer and token is structurally representable", scenario: "exact"},
		{name: "pointer without token", scenario: "pointer-only", code: "23514", constraint: "session_threads_admission_claim_pair_check"},
		{name: "token without pointer", scenario: "token-only", code: "23514", constraint: "session_threads_admission_claim_pair_check"},
		{name: "another tenant cannot supply a claim", scenario: "org", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "another parent in the same tenant cannot supply a claim", scenario: "session", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "sibling dispatch cannot supply a lane claim", scenario: "lane", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "nonexistent dispatch", scenario: "missing", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "wrong receipt token", scenario: "token", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "NULL parent token cannot match a present lane token", scenario: "null-token", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
		{name: "threadless dispatch cannot match a lane", scenario: "threadless", code: "23503", constraint: "session_threads_admission_claim_scope_fk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			source := fixture
			thread := &fixture.epochs.threadID
			token := uuid.New()
			parentToken := &token
			switch tt.scenario {
			case "org":
				source = seedRequestEpochMigrationFixture(t, ctx, actor)
				thread = &source.epochs.threadID
			case "session":
				other := laneClaimSiblingSession(t, ctx, actor, fixture.epochs.orgID)
				source.epochs = other
				thread = &source.epochs.threadID
			case "lane":
				lane := laneClaimSiblingThread(t, ctx, actor, fixture.epochs)
				thread = &lane
			case "threadless":
				thread = nil
			case "null-token":
				parentToken = nil
			}
			dispatch := laneClaimDispatch(t, ctx, actor, source, thread, parentToken, nil)
			want := db.LifecycleAdmissionClaimState{DispatchID: &dispatch.ID, Token: &token}
			switch tt.scenario {
			case "absent":
				want = db.LifecycleAdmissionClaimState{}
			case "pointer-only":
				want.Token = nil
			case "token-only":
				want.DispatchID = nil
			case "missing":
				id := uuid.New()
				want.DispatchID = &id
			case "token":
				wrong := uuid.New()
				want.Token = &wrong
			}
			before, err := db.NewSessionStore(actor).GetByID(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
			require.NoError(t, err, "capture the complete unchanged parent model before a structural claim write")
			err = laneClaimSet(ctx, actor, fixture.epochs, want.DispatchID, want.Token)
			if tt.code != "" {
				requireDispatchIdentityFailure(t, err, tt.code, tt.constraint, "", tt.name)
				want = db.LifecycleAdmissionClaimState{}
			} else {
				require.NoError(t, err, "accept only the complete absent or exact token-bound pair")
			}
			actual, err := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
			require.NoError(t, err, "read the exact lane after an accepted or rolled-back structural update")
			require.Equal(t, want, actual, "constraint failures must leave no partial claim while exact pairs remain raw observations")
			after, err := db.NewSessionStore(actor).GetByID(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
			require.NoError(t, err, "read the complete parent after the synthetic lane update")
			require.Equal(t, before, after, "claim representation must not change parent running projection or introduce session-wide admission authority")
		})
	}
}

func laneClaimSiblingThread(t *testing.T, ctx context.Context, database db.DBTX, fixture epochMigrationFixture) uuid.UUID {
	t.Helper()
	lane := &models.SessionThread{OrgID: fixture.orgID, SessionID: fixture.sessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic independent claim lane", Status: models.ThreadStatusIdle}
	require.NoError(t, db.NewSessionThreadStore(database).Create(ctx, lane, 10), "create an independent sibling through unchanged legacy insertion")
	return lane.ID
}

func laneClaimSiblingSession(t *testing.T, ctx context.Context, database db.DBTX, org uuid.UUID) epochMigrationFixture {
	t.Helper()
	session := &models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, db.NewSessionStore(database).Create(ctx, session), "create a real other parent in the same tenant for an exact-scope rejection")
	fixture := epochMigrationFixture{orgID: org, sessionID: session.ID}
	fixture.threadID = laneClaimSiblingThread(t, ctx, database, fixture)
	return fixture
}

func TestSessionLaneClaimRawRepresentationPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(344), "install nullable claim representation and the exact raw reader prerequisite")
	tests := []struct {
		name, scenario string
		turn           *int32
	}{
		{name: "legacy inserts remain entirely unknown", scenario: "absent"},
		{name: "present all-zero UUIDs differ from NULL", scenario: "zero", turn: workspaceMigrationPointer(int32(0))},
		{name: "minimum signed turn remains a raw observation", scenario: "claim", turn: workspaceMigrationPointer(int32(math.MinInt32))},
		{name: "maximum signed turn preserves exact precision", scenario: "claim", turn: workspaceMigrationPointer(int32(math.MaxInt32))},
		{name: "negative turn without token remains independent", scenario: "turn-only", turn: workspaceMigrationPointer(int32(-1))},
		{name: "token without turn remains independent", scenario: "token-only"},
		{name: "archived parent and lane stay observable", scenario: "archived"},
		{name: "deleted parent and archived lane stay observable", scenario: "deleted"},
		{name: "sibling claims coexist without parent ownership", scenario: "siblings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			token := uuid.New()
			if tt.scenario == "zero" {
				token = uuid.Nil
			}
			var parentToken *uuid.UUID
			if tt.scenario != "absent" && tt.scenario != "turn-only" {
				parentToken = &token
			}
			dispatch := laneClaimDispatch(t, ctx, actor, fixture, &fixture.epochs.threadID, parentToken, tt.turn)
			if tt.scenario == "zero" {
				_, err := actor.Exec(ctx, `UPDATE session_input_dispatches SET id=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID, uuid.Nil)
				require.NoError(t, err, "a stored zero dispatch identity is raw representation rather than inferred absence")
				dispatch.ID = uuid.Nil
			}
			var actualToken *uuid.UUID
			var actualTurn *int32
			require.NoError(t, actor.QueryRow(ctx, `SELECT admission_claim_token,admission_claim_turn FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID).Scan(&actualToken, &actualTurn), "read independently nullable raw dispatch claim observations")
			require.Equal(t, parentToken, actualToken, "SQL NULL and a present all-zero receipt token must remain distinct")
			require.Equal(t, tt.turn, actualTurn, "unknown zero negative and both signed integer extremes must retain their exact raw values")
			want := db.LifecycleAdmissionClaimState{}
			if tt.scenario != "absent" && tt.scenario != "turn-only" && tt.scenario != "token-only" {
				want = db.LifecycleAdmissionClaimState{DispatchID: &dispatch.ID, Token: &token}
				require.NoError(t, laneClaimSet(ctx, actor, fixture.epochs, want.DispatchID, want.Token), "store a complete structurally scoped lane pair without inferring turn or epoch authority")
			}
			if tt.scenario == "archived" || tt.scenario == "deleted" {
				batch := &pgx.Batch{}
				batch.Queue(`UPDATE session_threads SET archived_at=now() WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				if tt.scenario == "archived" {
					batch.Queue(`UPDATE sessions SET archived_at=now() WHERE org_id=$1 AND id=$2`, fixture.epochs.orgID, fixture.epochs.sessionID)
				} else {
					batch.Queue(`UPDATE sessions SET deleted_at=now() WHERE org_id=$1 AND id=$2`, fixture.epochs.orgID, fixture.epochs.sessionID)
				}
				require.NoError(t, actor.SendBatch(ctx, batch).Close(), "retain raw claim pointers while changing only existing parent visibility markers")
			}
			if tt.scenario == "siblings" {
				before, err := db.NewSessionStore(actor).GetByID(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
				require.NoError(t, err, "capture the unchanged parent projection while the first lane already has a claim")
				sibling := fixture
				sibling.epochs.threadID = laneClaimSiblingThread(t, ctx, actor, fixture.epochs)
				otherToken := uuid.New()
				other := laneClaimDispatch(t, ctx, actor, sibling, &sibling.epochs.threadID, &otherToken, nil)
				require.NoError(t, laneClaimSet(ctx, actor, sibling.epochs, &other.ID, &otherToken), "a second independent sibling must not collide with a shared session admission pointer")
				otherActual, err := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, sibling.epochs.orgID, sibling.epochs.sessionID, sibling.epochs.threadID)
				require.NoError(t, err, "read the independently installed sibling lane pair")
				require.Equal(t, db.LifecycleAdmissionClaimState{DispatchID: &other.ID, Token: &otherToken}, otherActual, "each sibling must preserve its own exact dispatch and token")
				after, err := db.NewSessionStore(actor).GetByID(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
				require.NoError(t, err, "read the complete parent model after two independent lane claims")
				require.Equal(t, before, after, "passive independent claims must not perform parent status arbitration or shared admission installation")
			}
			actual, err := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
			require.NoError(t, err, "observe exact retained lane representation without active or visibility filters")
			require.Equal(t, want, actual, "the reader must preserve the exact pair including unknown and present-zero values without returning authority")
		})
	}
}

func TestSessionLaneClaimReaderScopePostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(344), "install the complete real schema before the exact raw-reader isolation probes")
	tests := []struct{ name, scope string }{
		{name: "wrong tenant cannot read a known claim", scope: "org"},
		{name: "wrong parent cannot read a known tenant lane", scope: "session"},
		{name: "foreign tenant lane cannot leak its claim", scope: "foreign"},
		{name: "missing lane cannot synthesize an empty observation", scope: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			token := uuid.New()
			dispatch := laneClaimDispatch(t, ctx, actor, fixture, &fixture.epochs.threadID, &token, nil)
			require.NoError(t, laneClaimSet(ctx, actor, fixture.epochs, &dispatch.ID, &token), "create an actual readable pair before probing another exact scope")
			org, session, lane := fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID
			switch tt.scope {
			case "org":
				org = uuid.New()
			case "session":
				session = uuid.New()
			case "missing":
				lane = uuid.New()
			case "foreign":
				other := seedRequestEpochMigrationFixture(t, ctx, actor)
				otherToken := uuid.New()
				otherDispatch := laneClaimDispatch(t, ctx, actor, other, &other.epochs.threadID, &otherToken, nil)
				require.NoError(t, laneClaimSet(ctx, actor, other.epochs, &otherDispatch.ID, &otherToken), "store a real foreign claim so isolation cannot pass merely because the other lane is unclaimed")
				lane = other.epochs.threadID
			}
			actual, err := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, org, session, lane)
			require.ErrorIs(t, err, pgx.ErrNoRows, "a missing exact tenant parent lane tuple must remain absent")
			require.Equal(t, db.LifecycleAdmissionClaimState{}, actual, "a failed scoped read must never expose another lane's pointer token or partial scan")
		})
	}
}

func TestSessionLaneClaimTransactionAndRetentionPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(344), "install the complete immediate lane claim constraints before ordering and retention proofs")
	tests := []struct{ name, scenario string }{
		{name: "dispatch before pointer commits atomically", scenario: "ordered"},
		{name: "pointer before dispatch fails immediately and rolls back", scenario: "pointer-first"},
		{name: "referenced dispatch deletion is NO ACTION", scenario: "delete"},
		{name: "referenced token mutation is NO ACTION", scenario: "token"},
		{name: "referenced dispatch identity mutation is NO ACTION", scenario: "identity"},
		{name: "explicit pair clear permits owned dispatch cleanup", scenario: "clear"},
		{name: "clear rotate reinstall is atomic", scenario: "rotate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			token := uuid.New()
			tx, err := actor.Begin(ctx)
			require.NoError(t, err, "bound every synthetic install or retention proof to one explicit transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			if tt.scenario == "pointer-first" {
				id := uuid.New()
				err = laneClaimSet(ctx, tx, fixture.epochs, &id, &token)
				requireDispatchIdentityFailure(t, err, "23503", "session_threads_admission_claim_scope_fk", "", "pointer-first insertion")
				_, err = tx.Exec(ctx, `SELECT 1`)
				requireDispatchIdentityFailure(t, err, "25P02", "", "", "the pointer-first transaction must already be aborted before any dispatch can be inserted")
				require.NoError(t, tx.Rollback(ctx), "roll back the failed pointer-first transaction rather than permitting a separately committed claim")
				actual, readErr := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				require.NoError(t, readErr, "read the lane after the failed claim-install transaction")
				require.Equal(t, db.LifecycleAdmissionClaimState{}, actual, "a pointer-first failure must leave no partial claim after rollback")
				return
			}
			dispatch := laneClaimDispatch(t, ctx, tx, fixture, &fixture.epochs.threadID, &token, nil)
			require.NoError(t, laneClaimSet(ctx, tx, fixture.epochs, &dispatch.ID, &token), "the precreated exact unbound dispatch must permit a pointer in the same transaction")
			want := db.LifecycleAdmissionClaimState{DispatchID: &dispatch.ID, Token: &token}
			inside, err := db.NewLifecycleEpochStore(tx).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
			require.NoError(t, err, "read a structurally complete claim within its own still-open transaction")
			require.Equal(t, want, inside, "the exact lane must point at its precreated dispatch and matching receipt token")
			switch tt.scenario {
			case "ordered":
				_, observer := lifecycleReferenceProbeConnection(t, conn)
				outside, readErr := db.NewLifecycleEpochStore(observer).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				require.NoError(t, readErr, "read the prior committed lane without waiting for the installer transaction")
				require.Equal(t, db.LifecycleAdmissionClaimState{}, outside, "a second connection must see neither an uncommitted pointer nor a partial install")
				var count int64
				require.NoError(t, observer.QueryRow(ctx, `SELECT count(*) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID).Scan(&count), "inspect dispatch visibility from the same independent observer before commit")
				require.Equal(t, int64(0), count, "an uncommitted claim installation must not expose its precreated dispatch separately")
				require.NoError(t, tx.Commit(ctx), "commit the synthetic dispatch and complete pair together")
				outside, readErr = db.NewLifecycleEpochStore(observer).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				require.NoError(t, readErr, "read the single committed dispatch/pointer installation")
				require.Equal(t, want, outside, "both pieces must become visible after the same commit")
				require.NoError(t, observer.QueryRow(ctx, `SELECT count(*) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID).Scan(&count), "inspect the exact dispatch after the single successful install commit")
				require.Equal(t, int64(1), count, "the committed pointer must have its exact dispatch visible in the same committed state")
			case "clear":
				require.NoError(t, laneClaimSet(ctx, tx, fixture.epochs, nil, nil), "explicit cleanup must clear both pointer and token together")
				command, deleteErr := tx.Exec(ctx, `DELETE FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID)
				require.NoError(t, deleteErr, "an explicitly cleared pointer permits deletion of the owned synthetic dispatch without cascade")
				require.Equal(t, int64(1), command.RowsAffected(), "explicit synthetic cleanup must remove exactly its one dispatch")
				actual, readErr := db.NewLifecycleEpochStore(tx).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				require.NoError(t, readErr, "observe the explicitly cleared lane while cleanup remains uncommitted")
				require.Equal(t, db.LifecycleAdmissionClaimState{}, actual, "explicit cleanup must leave a complete absent pair rather than a dangling token")
			case "rotate":
				require.NoError(t, laneClaimSet(ctx, tx, fixture.epochs, nil, nil), "rotation must explicitly clear the complete old pair before changing the referenced token")
				rotated := uuid.New()
				command, rotateErr := tx.Exec(ctx, `UPDATE session_input_dispatches SET admission_claim_token=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, dispatch.OrgID, dispatch.SessionID, dispatch.ID, rotated)
				require.NoError(t, rotateErr, "a cleared lane permits an explicit token rotation in the same bounded synthetic transaction")
				require.Equal(t, int64(1), command.RowsAffected(), "rotation must change exactly its one retained dispatch")
				require.NoError(t, laneClaimSet(ctx, tx, fixture.epochs, &dispatch.ID, &rotated), "reinstall only the exact same lane and dispatch with the rotated matching receipt")
				require.NoError(t, tx.Commit(ctx), "commit explicit clear rotation and reinstall as one synthetic operation")
				actual, readErr := db.NewLifecycleEpochStore(actor).GetThreadClaim(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
				require.NoError(t, readErr, "read the committed replacement token pair")
				require.Equal(t, db.LifecycleAdmissionClaimState{DispatchID: &dispatch.ID, Token: &rotated}, actual, "the exact replacement receipt must become visible without leaving a mismatched live pair")
			default:
				query := `DELETE FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`
				arguments := []any{dispatch.OrgID, dispatch.SessionID, dispatch.ID}
				code, constraint := "23503", "session_threads_admission_claim_scope_fk"
				if tt.scenario == "token" {
					query = `UPDATE session_input_dispatches SET admission_claim_token=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`
					arguments = append(arguments, uuid.New())
				} else if tt.scenario == "identity" {
					query = `UPDATE session_input_dispatches SET id=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`
					arguments = append(arguments, uuid.New())
				}
				_, err = tx.Exec(ctx, query, arguments...)
				requireDispatchIdentityFailure(t, err, code, constraint, "", tt.name)
			}
		})
	}
}

func TestSessionLaneClaimReferencedIndexPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scenario string }{
		{name: "invalid schema-wide name conflict remains visibly dirty", scenario: "invalid"},
		{name: "valid wrong-definition name conflict is not skipped", scenario: "wrong"},
		{name: "dependent exact token FK prevents index removal", scenario: "dependent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if tt.scenario == "dependent" {
				require.NoError(t, migration.Migrate(344), "install the real exact-token lane FK before probing referenced-index deletion")
				_, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY idx_session_input_dispatches_lane_claim`)
				requireDispatchIdentityFailure(t, err, "2BP01", "", "", "the real lane FK's parent index dependency")
				_, err = conn.Exec(ctx, `DROP INDEX CONCURRENTLY idx_session_input_dispatches_lane_claim CASCADE`)
				requireDispatchIdentityFailure(t, err, "0A000", "", "", "concurrent CASCADE cannot silently remove the exact-token lane FK")
				requireLaneClaimMigrationCatalog(t, ctx, conn, 344, true)
				actual, dirty, err := migration.Version()
				require.NoError(t, err, "inspect the migration ledger after direct dependency refusal probes")
				require.Equal(t, uint(344), actual, "direct index removal refusal must not change the installed migration version")
				require.False(t, dirty, "the direct refusal must preserve the prior clean ledger")
				return
			}
			require.NoError(t, migration.Migrate(341), "construct the precise clean boundary before the standalone concurrent referenced-key build")
			_, err := conn.Exec(ctx, `CREATE TABLE lifecycle_probe_duplicate_index(value integer NOT NULL)`)
			require.NoError(t, err, "create only an owned disposable wrong-parent index fixture")
			if tt.scenario == "invalid" {
				_, err = conn.Exec(ctx, `INSERT INTO lifecycle_probe_duplicate_index(value) VALUES(1),(1)`)
				require.NoError(t, err, "seed deliberate duplicates on a synthetic table rather than altering target identity keys")
				// A genuine failed build on another table leaves this schema-wide
				// name invalid. It is not a claim to simulate interruption while
				// building the production target index itself.
				_, err = conn.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY idx_session_input_dispatches_lane_claim ON lifecycle_probe_duplicate_index(value)`)
				var duplicate *pgconn.PgError
				require.ErrorAs(t, err, &duplicate, "the duplicate fixture must fail through the real concurrent PostgreSQL index path")
				require.Equal(t, "23505", duplicate.Code, "the invalid name fixture must come from an actual uniqueness build failure")
				requireLifecycleReferenceInvalidIndex(t, ctx, conn, "idx_session_input_dispatches_lane_claim")
			} else {
				_, err = conn.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY idx_session_input_dispatches_lane_claim ON lifecycle_probe_duplicate_index(value)`)
				require.NoError(t, err, "install a genuinely valid same-name index with the wrong parent and shape")
			}
			err = migration.Migrate(342)
			var migrationErr database.Error
			require.ErrorAs(t, err, &migrationErr, "the real standalone index migration must reject both valid and invalid same-name objects")
			var named *pq.Error
			require.ErrorAs(t, migrationErr.OrigErr, &named, "retain the migration driver's original PostgreSQL error for the exact build failure")
			require.Equal(t, pq.ErrorCode("42P07"), named.Code, "name conflicts must fail rather than be masked by IF NOT EXISTS")
			require.Contains(t, named.Message, "idx_session_input_dispatches_lane_claim", "the rejected build must identify its exact schema-wide conflicting name")
			version, dirty, err := migration.Version()
			require.NoError(t, err, "read the real dirty ledger after the failed concurrent migration")
			require.Equal(t, uint(342), version, "the failed standalone build must retain its exact attempted version")
			require.True(t, dirty, "a concurrent referenced-key failure must remain visibly dirty")
			recorder := &lifecycleReferenceDirtyRepairRecorder{Migrate: migration}
			repairedVersion, repaired, err := repairKnownDirtyMigration(recorder)
			require.EqualError(t, err, "database is dirty at version 342; refusing repair because only versions 267 and 281 are allowlisted", "known-dirty repair must refuse this new referenced-key migration")
			require.Equal(t, uint(0), repairedVersion, "refusal must not invent a repaired migration receipt")
			require.False(t, repaired, "the new build must never become an automatic dirty-repair candidate")
			require.Equal(t, 0, recorder.forceCalls, "refusal must not invoke Force against the real migration ledger")
			var retry migrate.ErrDirty
			require.ErrorAs(t, migration.Migrate(342), &retry, "ordinary retry must remain blocked without masking the conflicting index")
			require.Equal(t, 342, retry.Version, "retry refusal must retain the same exact dirty boundary")
			var parent string
			require.NoError(t, conn.QueryRow(ctx, `SELECT p.relname::text FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_class p ON p.oid=i.indrelid WHERE c.relnamespace='public'::regnamespace AND c.relname='idx_session_input_dispatches_lane_claim'`).Scan(&parent), "reinspect the original conflicting object after both repair and retry refusal")
			require.Equal(t, "lifecycle_probe_duplicate_index", parent, "refusal must preserve evidence rather than dropping or replacing the named wrong object")
		})
	}
}
