package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSessionInputDispatchIdentityDoesNotAuthorizePostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(335), "install the passive dispatch identity and all three independent parent references")
	tests := []struct {
		name, lane, payload          string
		request, threadless, message bool
		explicitZero                 bool
	}{
		{name: "threaded request with another valid lane", request: true, lane: "other", payload: `{"synthetic":"unvalidated"}`},
		{name: "threaded request with null dispatch lane", request: true, lane: "none", payload: `{"synthetic":"unvalidated"}`},
		{name: "threadless request with a dispatch lane", request: true, threadless: true, payload: `{"synthetic":"unvalidated"}`},
		{name: "threadless request with null dispatch lane", request: true, threadless: true, lane: "none", payload: `null`},
		{name: "fabricated message identity and json null", message: true, payload: `null`},
		{name: "fabricated message and claimed json authority", message: true, payload: `{"org_id":"unrelated-synthetic-org","job_id":"unbound-synthetic-job","authorized":true}`},
		{name: "present zero epochs are not current parent epochs", explicitZero: true, payload: `{"synthetic":"unvalidated"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			_, err := actor.Exec(ctx, `UPDATE sessions SET admission_epoch=23 WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "seed an explicitly nonzero parent session epoch without activating any lifecycle protocol")
			_, err = actor.Exec(ctx, `UPDATE session_threads SET admission_epoch=31 WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "seed nonzero lane epochs so absent dispatch epochs cannot be mistaken for current identity")
			row := dispatchIdentityRetentionRow{orgID: fixture.orgID, sessionID: fixture.sessionID, threadID: &fixture.threadID, kind: "initial", payload: tt.payload}
			if tt.request {
				request := &models.HumanInputRequest{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: &fixture.threadID, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic dispatch authority boundary", Body: "Synthetic input"}
				if tt.threadless {
					request.ThreadID = nil
				}
				require.NoError(t, db.NewSessionHumanInputRequestStore(actor).Create(ctx, request), "create the exact legacy request referenced only by its org session and request triple")
				row.requestID, row.kind = &request.ID, "answer"
			}
			if tt.lane == "none" {
				row.threadID = nil
			} else if tt.lane == "other" {
				var other uuid.UUID
				require.NoError(t, actor.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id<>$3 ORDER BY id LIMIT 1`, fixture.orgID, fixture.sessionID, fixture.threadID).Scan(&other), "select another real lane in the same session instead of bypassing the strong thread reference")
				row.threadID = &other
			}
			if tt.message {
				messageID := int64(1<<60) + 7
				row.messageID, row.kind = &messageID, "message"
				var exists bool
				require.NoError(t, actor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_messages WHERE org_id=$1 AND id=$2)`, fixture.orgID, messageID).Scan(&exists), "check the synthetic message identity before inserting an unbound passive dispatch")
				require.False(t, exists, "this boundary probe must use a message that does not exist for the tenant")
			}
			if tt.explicitZero {
				zero := int64(0)
				row.sessionEpoch, row.threadEpoch = &zero, &zero
			}
			id := insertDispatchIdentityRetentionRow(t, ctx, actor, row)
			var sessionEpoch, threadEpoch *int64
			var storedLane *uuid.UUID
			var matchesPayload bool
			require.NoError(t, actor.QueryRow(ctx, `SELECT session_epoch,thread_epoch,thread_id,validated_payload=$4::jsonb FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.orgID, fixture.sessionID, id, tt.payload).Scan(&sessionEpoch, &threadEpoch, &storedLane, &matchesPayload), "read raw dispatch identities independently of any future canonical consumer")
			require.Equal(t, row.sessionEpoch, sessionEpoch, "an absent session epoch must stay unknown and a supplied zero must remain zero rather than inherit the parent")
			require.Equal(t, row.threadEpoch, threadEpoch, "an absent thread epoch must stay unknown and cannot derive authority from the current lane")
			require.Equal(t, row.threadID, storedLane, "the request triple FK intentionally does not enforce null-safe request-lane equality")
			require.True(t, matchesPayload, "a stored validated_payload column name is not proof that raw JSON or embedded job ownership was validated")
			before := dispatchIdentityRetentionJSON(t, ctx, actor, fixture.orgID, fixture.sessionID, id)
			_, err = actor.Exec(ctx, `UPDATE sessions SET admission_epoch=24 WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "change only the synthetic parent epoch after storing passive identity")
			_, err = actor.Exec(ctx, `UPDATE session_threads SET admission_epoch=32 WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "change only the synthetic lane epochs without inventing a dispatch migration or writer")
			require.Equal(t, before, dispatchIdentityRetentionJSON(t, ctx, actor, fixture.orgID, fixture.sessionID, id), "parent metadata changes must not backfill unknown epochs or rebind any passive dispatch field")
		})
	}
}

func TestSessionInputDispatchParentDeleteRetainsIdentityPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, constraint string
		version          uint
	}{
		{name: "organization", constraint: "session_input_dispatches_org_id_fkey", version: 332},
		{name: "session", constraint: "session_input_dispatches_session_scope_fk", version: 335},
		{name: "thread", constraint: "session_input_dispatches_thread_scope_fk", version: 335},
		{name: "request", constraint: "session_input_dispatches_request_scope_fk", version: 335},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			require.NoError(t, migration.Migrate(tt.version), "apply the real schema at the exact reference-retention proof boundary")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			row := dispatchIdentityRetentionRow{orgID: uuid.New(), sessionID: uuid.New(), kind: "initial", payload: `{"synthetic":"retained-reference"}`}
			query := `DELETE FROM organizations WHERE id=$1`
			parentQuery := `SELECT EXISTS(SELECT 1 FROM organizations WHERE id=$1)`
			args := []any{row.orgID}
			if tt.name == "organization" {
				// At the initial table-only boundary the synthetic organization has
				// no sessions or other children. This isolates the new direct org FK
				// rather than mistaking existing session retention for this policy.
				_, err := conn.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic dispatch organization retention')`, row.orgID)
				require.NoError(t, err, "create only this isolated organization's dispatch parent")
			} else {
				fixture := seedEpochMigrationFixture(t, ctx, conn)
				row.orgID, row.sessionID = fixture.orgID, fixture.sessionID
				query = `DELETE FROM sessions WHERE org_id=$1 AND id=$2`
				parentQuery = `SELECT EXISTS(SELECT 1 FROM sessions WHERE org_id=$1 AND id=$2)`
				args = []any{fixture.orgID, fixture.sessionID}
				if tt.name == "thread" {
					row.threadID = &fixture.threadID
					query = `DELETE FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3`
					parentQuery = `SELECT EXISTS(SELECT 1 FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3)`
					args = append(args, fixture.threadID)
				} else if tt.name == "request" {
					request := &models.HumanInputRequest{OrgID: fixture.orgID, SessionID: fixture.sessionID, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic request retention", Body: "Synthetic input"}
					require.NoError(t, db.NewSessionHumanInputRequestStore(conn).Create(ctx, request), "create a threadless request so only the intended dispatch request FK blocks its deletion")
					row.requestID, row.kind = &request.ID, "answer"
					query = `DELETE FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 AND id=$3`
					parentQuery = `SELECT EXISTS(SELECT 1 FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 AND id=$3)`
					args = append(args, request.ID)
				}
			}
			id := insertDispatchIdentityRetentionRow(t, ctx, conn, row)
			before := dispatchIdentityRetentionJSON(t, ctx, conn, row.orgID, row.sessionID, id)
			var deleteAction, updateAction string
			var validated bool
			require.NoError(t, conn.QueryRow(ctx, `SELECT confdeltype::text,confupdtype::text,convalidated FROM pg_constraint WHERE conrelid='session_input_dispatches'::regclass AND contype='f' AND conname=$1`, tt.constraint).Scan(&deleteAction, &updateAction, &validated), "inspect the exact dispatch FK rather than infer its retention action from an unrelated error")
			require.Equal(t, []string{"a", "a"}, []string{deleteAction, updateAction}, "parent reference actions must retain PostgreSQL's default NO ACTION rather than CASCADE")
			require.True(t, validated, "the intended parent reference must be a validated real PostgreSQL foreign key")
			var deletionErr error
			if tt.name == "session" {
				// Migration 48 deliberately turns ordinary DELETE into soft delete.
				// Preserve and characterize that behavior before testing NO ACTION
				// with its existing explicit hard-delete gate, only in this fixture.
				tag, err := conn.Exec(ctx, query, args...)
				require.NoError(t, err, "ordinary session DELETE must retain its existing soft-delete response")
				require.Equal(t, int64(0), tag.RowsAffected(), "the existing before-delete trigger must suppress physical session removal")
				var softDeleted bool
				require.NoError(t, conn.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM sessions WHERE org_id=$1 AND id=$2`, row.orgID, row.sessionID).Scan(&softDeleted), "inspect the existing session soft-delete marker")
				require.True(t, softDeleted, "normal deletion must still mark the session deleted rather than treat FK retention as new policy")
				require.Equal(t, before, dispatchIdentityRetentionJSON(t, ctx, conn, row.orgID, row.sessionID, id), "ordinary soft deletion must preserve all passive dispatch identity fields")
				var beforeSession json.RawMessage
				sessionJSONQuery := `SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2`
				require.NoError(t, conn.QueryRow(ctx, sessionJSONQuery, row.orgID, row.sessionID).Scan(&beforeSession), "capture every session field after its legitimate existing soft-delete mutation")
				var gateBefore *string
				gateQuery := `SELECT NULLIF(current_setting('app.allow_hard_delete',true),'')`
				require.NoError(t, conn.QueryRow(ctx, gateQuery).Scan(&gateBefore), "observe the hard-delete gate before entering the explicit probe transaction")
				require.Nil(t, gateBefore, "the ordinary fixture connection must not enable hard deletion outside the probe transaction")
				tx, err := conn.Begin(ctx)
				require.NoError(t, err, "begin an isolated hard-delete reference probe")
				defer func() { _ = tx.Rollback(ctx) }()
				_, err = tx.Exec(ctx, `SET LOCAL app.allow_hard_delete='true'`)
				require.NoError(t, err, "use the existing explicit hard-delete test gate only inside the owned disposable transaction")
				_, deletionErr = tx.Exec(ctx, query, args...)
				require.NoError(t, tx.Rollback(ctx), "roll back the complete hard-delete probe before inspecting retained rows")
				var afterSession json.RawMessage
				require.NoError(t, conn.QueryRow(ctx, sessionJSONQuery, row.orgID, row.sessionID).Scan(&afterSession), "read the original retained session after complete hard-delete rollback")
				require.Equal(t, beforeSession, afterSession, "failed hard deletion must roll back every session field while preserving the earlier legitimate soft-delete marker")
				var gateAfter *string
				require.NoError(t, conn.QueryRow(ctx, gateQuery).Scan(&gateAfter), "inspect the normalized transaction-local hard-delete gate after rollback")
				require.Equal(t, gateBefore, gateAfter, "rollback must leave the hard-delete gate semantically unset outside the probe transaction")
			} else {
				_, deletionErr = conn.Exec(ctx, query, args...)
			}
			var pgErr *pgconn.PgError
			require.ErrorAs(t, deletionErr, &pgErr, "physically deleting the referenced parent must not cascade away a dispatch identity")
			require.Equal(t, "23503", pgErr.Code, "parent deletion must fail as an actual foreign-key reference violation")
			require.Equal(t, tt.constraint, pgErr.ConstraintName, "the deletion must be blocked by this exact new dispatch reference")
			require.Equal(t, "session_input_dispatches", pgErr.TableName, "an unrelated legacy table's retention must not satisfy this proof")
			require.Equal(t, before, dispatchIdentityRetentionJSON(t, ctx, conn, row.orgID, row.sessionID, id), "failed parent deletion must preserve the complete dispatch without cascading, rewriting or losing provenance")
			var exists bool
			require.NoError(t, conn.QueryRow(ctx, parentQuery, args...).Scan(&exists), "inspect the exact referenced parent after the rejected deletion")
			require.True(t, exists, "the rejected parent deletion must leave its original row present")
			version, dirty, err := migration.Version()
			require.NoError(t, err, "inspect the ledger after a direct data-deletion refusal rather than a migration attempt")
			require.Equal(t, tt.version, version, "a direct rejected parent delete must not change the migration boundary")
			require.False(t, dirty, "a direct rejected parent delete must leave the clean migration ledger unchanged")
		})
	}
}

func TestSessionInputDispatchScopeKeyRetainsDependentReferencePostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(335), "construct the full passive identity schema before probing a future exact-scoped reference")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedEpochMigrationFixture(t, ctx, conn)
	id := insertDispatchIdentityRetentionRow(t, ctx, conn, dispatchIdentityRetentionRow{orgID: fixture.orgID, sessionID: fixture.sessionID, kind: "initial", payload: `{"synthetic":"scope-reference"}`})
	// Ordinary synthetic child tables are confined to this generated database.
	// This probe installs no production claim, consumer or retention policy.
	_, err := conn.Exec(ctx, `CREATE TABLE lifecycle_probe_dispatch_reference(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,dispatch_id uuid NOT NULL,CONSTRAINT fk_lifecycle_probe_dispatch_scope FOREIGN KEY(org_id,session_id,dispatch_id) REFERENCES session_input_dispatches(org_id,session_id,id))`)
	require.NoError(t, err, "the real dispatch scope key must support an exact org/session/dispatch foreign key")
	tests := []struct {
		name, mismatch string
		accepted       bool
	}{
		{name: "exact dispatch membership", accepted: true},
		{name: "foreign organization", mismatch: "org"},
		{name: "wrong session", mismatch: "session"},
		{name: "wrong dispatch", mismatch: "dispatch"},
	}
	var retainedChild uuid.UUID
	// These ordered probes share one connection and preserve the accepted child
	// for the dependent DROP refusal afterward; independent databases run in parallel.
	for _, tt := range tests {
		org, session, dispatch := fixture.orgID, fixture.sessionID, id
		switch tt.mismatch {
		case "org":
			org = uuid.New()
		case "session":
			session = uuid.New()
		case "dispatch":
			dispatch = uuid.New()
		}
		childID := uuid.New()
		_, err = conn.Exec(ctx, `INSERT INTO lifecycle_probe_dispatch_reference(id,org_id,session_id,dispatch_id) VALUES($1,$2,$3,$4)`, childID, org, session, dispatch)
		if tt.accepted {
			require.NoError(t, err, tt.name+" must be accepted by the real composite scope key")
			retainedChild = childID
		} else {
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr, tt.name+" must not establish a scoped child reference")
			require.Equal(t, "23503", pgErr.Code, tt.name+" must fail with an exact PostgreSQL FK violation")
			require.Equal(t, "fk_lifecycle_probe_dispatch_scope", pgErr.ConstraintName, tt.name+" must fail on the intended exact dispatch scope")
		}
	}
	before := dispatchIdentityRetentionJSON(t, ctx, conn, fixture.orgID, fixture.sessionID, id)
	var beforeTable, beforeIndex, beforeFK int64
	var beforeValid bool
	catalogQuery := `SELECT fk.confrelid::bigint,fk.conindid::bigint,fk.oid::bigint,i.indisvalid FROM pg_constraint fk JOIN pg_index i ON i.indexrelid=fk.conindid WHERE fk.conrelid='lifecycle_probe_dispatch_reference'::regclass AND fk.conname='fk_lifecycle_probe_dispatch_scope'`
	require.NoError(t, conn.QueryRow(ctx, catalogQuery).Scan(&beforeTable, &beforeIndex, &beforeFK, &beforeValid), "capture the real synthetic-child to dispatch-scope-index dependency")
	require.True(t, beforeValid, "the future reference must use a valid real parent index")
	_, err = conn.Exec(ctx, `DROP TABLE session_input_dispatches`)
	var dependencyErr *pgconn.PgError
	require.ErrorAs(t, err, &dependencyErr, "default RESTRICT must refuse to remove a dispatch table supporting an existing foreign key")
	require.Equal(t, "2BP01", dependencyErr.Code, "dependent-table drop must fail as dependent objects rather than silently cascade")
	require.Contains(t, dependencyErr.Detail, "fk_lifecycle_probe_dispatch_scope", "the direct refusal must identify the retained synthetic reference")
	require.Equal(t, before, dispatchIdentityRetentionJSON(t, ctx, conn, fixture.orgID, fixture.sessionID, id), "a direct rejected DROP must preserve every field of the referenced dispatch")
	var afterTable, afterIndex, afterFK int64
	var afterValid bool
	require.NoError(t, conn.QueryRow(ctx, catalogQuery).Scan(&afterTable, &afterIndex, &afterFK, &afterValid), "inspect the same table index and foreign key after the refused DROP")
	require.Equal(t, []int64{beforeTable, beforeIndex, beforeFK}, []int64{afterTable, afterIndex, afterFK}, "dependent DROP refusal must preserve exact catalog identities rather than recreate or replace them")
	require.True(t, afterValid, "the referenced index must remain valid after the refused DROP")
	var actualOrg, actualSession, actualDispatch uuid.UUID
	require.NoError(t, conn.QueryRow(ctx, `SELECT org_id,session_id,dispatch_id FROM lifecycle_probe_dispatch_reference WHERE id=$1`, retainedChild).Scan(&actualOrg, &actualSession, &actualDispatch), "read the original accepted synthetic child after the refused DROP")
	require.Equal(t, []uuid.UUID{fixture.orgID, fixture.sessionID, id}, []uuid.UUID{actualOrg, actualSession, actualDispatch}, "the exact child reference must survive along with its parent")
	version, dirty, err := migration.Version()
	require.NoError(t, err, "inspect the ledger after direct DROP refusal without attempting a down migration")
	require.Equal(t, uint(335), version, "a direct dependency probe must leave the completed migration version unchanged")
	require.False(t, dirty, "direct dependency refusal is not a failed-down migration and must retain a clean ledger")
}

type dispatchIdentityRetentionRow struct {
	orgID, sessionID          uuid.UUID
	threadID, requestID       *uuid.UUID
	sessionEpoch, threadEpoch *int64
	messageID                 *int64
	kind, payload             string
}

func insertDispatchIdentityRetentionRow(t *testing.T, ctx context.Context, conn *pgx.Conn, row dispatchIdentityRetentionRow) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := conn.QueryRow(ctx, `INSERT INTO session_input_dispatches(org_id,session_id,thread_id,session_epoch,thread_epoch,message_id,request_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'synthetic_test',$9,'synthetic-opaque-digest',$10::jsonb,0) RETURNING id`, row.orgID, row.sessionID, row.threadID, row.sessionEpoch, row.threadEpoch, row.messageID, row.requestID, row.kind, uuid.NewString(), row.payload).Scan(&id)
	require.NoError(t, err, "insert passive synthetic identity with all caller-required fields and no production admission or dispatch writer")
	return id
}

func dispatchIdentityRetentionJSON(t *testing.T, ctx context.Context, conn *pgx.Conn, orgID, sessionID, id uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(d) FROM session_input_dispatches d WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, sessionID, id).Scan(&raw), "capture every scoped dispatch field without interpreting its unvalidated payload or nullable epochs")
	return raw
}
