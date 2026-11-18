package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type workspaceMigrationRecord struct {
	OrgID, SessionID, RequestID uuid.UUID
	Metadata                    db.InputWorkspaceCompatibilityMetadata
}

const workspaceMigrationColumns = `org_id,session_id,request_id,state,anchor_digest,context_scope_digest,workspace_generation,workspace_revision,snapshot_key,workspace_state,last_authority_kind,last_authority_receipt_id,reason,next_revalidation_at`

func workspaceMigrationArguments(record workspaceMigrationRecord) []any {
	m := record.Metadata
	return []any{record.OrgID, record.SessionID, record.RequestID, m.State, m.AnchorDigest, m.ContextScopeDigest, m.WorkspaceGeneration, m.WorkspaceRevision, m.SnapshotKey, m.WorkspaceState, m.LastAuthorityKind, m.LastAuthorityReceiptID, m.Reason, m.NextRevalidationAt}
}

func scanWorkspaceMigrationRecord(row pgx.Row) (workspaceMigrationRecord, error) {
	var record workspaceMigrationRecord
	m := &record.Metadata
	err := row.Scan(&record.OrgID, &record.SessionID, &record.RequestID, &m.State, &m.AnchorDigest, &m.ContextScopeDigest, &m.WorkspaceGeneration, &m.WorkspaceRevision, &m.SnapshotKey, &m.WorkspaceState, &m.LastAuthorityKind, &m.LastAuthorityReceiptID, &m.Reason, &m.NextRevalidationAt)
	return record, err
}

func insertWorkspaceMigrationRecord(ctx context.Context, database db.DBTX, arguments []any) (workspaceMigrationRecord, error) {
	return scanWorkspaceMigrationRecord(database.QueryRow(ctx, `INSERT INTO session_input_workspace_compatibility (`+workspaceMigrationColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+workspaceMigrationColumns, arguments...))
}

func workspaceMigrationPointer[T any](value T) *T { return &value }

func TestSessionInputWorkspaceRepresentationPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "apply the complete real workspace schema before testing raw reader round trips")
	tests := []struct {
		name     string
		threaded bool
		metadata func() db.InputWorkspaceCompatibilityMetadata
	}{
		{name: "incomplete certified threadless row is not authority", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified}
		}},
		{name: "pending threaded row keeps all unknown fields absent", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending}
		}},
		{name: "unavailable is an explicit stored label", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnavailable}
		}},
		{name: "unprovable is not inferred from NULL metadata", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable}
		}},
		{name: "present zero and empty values differ from absence", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, AnchorDigest: workspaceMigrationPointer(""), ContextScopeDigest: workspaceMigrationPointer(""), WorkspaceGeneration: workspaceMigrationPointer(int64(0)), WorkspaceRevision: workspaceMigrationPointer(int64(0)), SnapshotKey: workspaceMigrationPointer(""), LastAuthorityKind: workspaceMigrationPointer(""), LastAuthorityReceiptID: workspaceMigrationPointer(uuid.Nil), Reason: workspaceMigrationPointer("")}
		}},
		{name: "signed maximum bigint metadata keeps exact precision", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceGeneration: workspaceMigrationPointer(int64(math.MaxInt64)), WorkspaceRevision: workspaceMigrationPointer(int64(math.MaxInt64 - 7))}
		}},
		{name: "signed minimum and negative values remain raw observations", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, WorkspaceGeneration: workspaceMigrationPointer(int64(math.MinInt64)), WorkspaceRevision: workspaceMigrationPointer(int64(-1))}
		}},
		{name: "generation is independent of revision", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceGeneration: workspaceMigrationPointer(int64(17))}
		}},
		{name: "revision is independent of generation", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceRevision: workspaceMigrationPointer(int64(23))}
		}},
		{name: "JSON null is present while other metadata stays SQL NULL", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, WorkspaceState: json.RawMessage(`null`)}
		}},
		{name: "opaque JSON object retains integers above float64 precision", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, WorkspaceState: json.RawMessage(`{"future":9223372036854775807,"nested":[null,true]}`)}
		}},
		{name: "opaque JSON array is not a certificate envelope", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceState: json.RawMessage(`["future",null,42]`)}
		}},
		{name: "opaque scalar JSON is not proof", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, WorkspaceState: json.RawMessage(`9007199254740993`)}
		}},
		{name: "receipt is independently present without an authority kind", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, LastAuthorityReceiptID: workspaceMigrationPointer(uuid.New())}
		}},
		{name: "future authority kind does not infer a receipt", metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, LastAuthorityKind: workspaceMigrationPointer("future synthetic authority")}
		}},
		{name: "complete opaque metadata remains observational", threaded: true, metadata: func() db.InputWorkspaceCompatibilityMetadata {
			return db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, AnchorDigest: workspaceMigrationPointer("synthetic-anchor"), ContextScopeDigest: workspaceMigrationPointer("synthetic-scope"), WorkspaceGeneration: workspaceMigrationPointer(int64(7)), WorkspaceRevision: workspaceMigrationPointer(int64(11)), SnapshotKey: workspaceMigrationPointer("synthetic/snapshot"), WorkspaceState: json.RawMessage(`{"unverified":true}`), LastAuthorityKind: workspaceMigrationPointer("synthetic"), LastAuthorityReceiptID: workspaceMigrationPointer(uuid.New()), Reason: workspaceMigrationPointer("synthetic pending observation"), NextRevalidationAt: workspaceMigrationPointer(time.Date(2020, time.January, 2, 3, 4, 5, 123456000, time.UTC))}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			request := fixture.requests[0]
			if tt.threaded {
				request = fixture.requests[1]
			}
			before := workspaceMigrationRequestRow(t, ctx, actor, request)
			beforeAPI, err := json.Marshal(request)
			require.NoError(t, err, "capture the complete original public request JSON before adding unrelated metadata")
			want := workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: tt.metadata()}
			// Direct PostgreSQL codecs canonicalize JSONB and timestamptz without
			// decoding JSON numbers through float64 or collapsing JSON null to SQL NULL.
			originalJSON := want.Metadata.WorkspaceState
			require.NoError(t, actor.QueryRow(ctx, `SELECT $1::jsonb,$2::timestamptz`, want.Metadata.WorkspaceState, want.Metadata.NextRevalidationAt).Scan(&want.Metadata.WorkspaceState, &want.Metadata.NextRevalidationAt), "canonicalize only the driver's JSONB and timestamp representation")
			if originalJSON != nil {
				require.NotNil(t, want.Metadata.WorkspaceState, "present JSON including JSON null must stay distinct from absent SQL metadata")
				require.Equal(t, workspaceMigrationExactJSON(t, originalJSON), workspaceMigrationExactJSON(t, want.Metadata.WorkspaceState), "driver canonicalization must preserve the original JSON value with full integer precision rather than merely agree with a second codec read")
			}
			actual, err := insertWorkspaceMigrationRecord(ctx, actor, workspaceMigrationArguments(want))
			require.NoError(t, err, "raw workspace storage must accept the synthetic metadata without certifying or authorizing it")
			require.Equal(t, want, actual, "the complete inserted record must preserve every exact scalar pointer and JSON value")
			stored, err := scanWorkspaceMigrationRecord(actor.QueryRow(ctx, `SELECT `+workspaceMigrationColumns+` FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND request_id=$3`, request.OrgID, request.SessionID, request.ID))
			require.NoError(t, err, "read stored metadata independently of the production raw reader")
			require.Equal(t, want, stored, "persisted metadata must retain all NULL zero negative extreme digest receipt and opaque JSON distinctions")
			metadata, err := db.NewSessionHumanInputRequestStore(actor).GetWorkspaceCompatibility(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "read exact threaded or threadless request metadata without parent inference")
			require.Equal(t, want.Metadata, metadata, "the passive reader must return the complete stored observation without deriving eligibility or readiness")
			require.Equal(t, before, workspaceMigrationRequestRow(t, ctx, actor, request), "metadata inserts and observations must preserve every raw legacy request field and prior passive metadata slot")
			public, err := db.NewSessionHumanInputRequestStore(actor).GetByID(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "legacy request readers must remain usable with arbitrary workspace observations")
			require.Equal(t, request, public, "workspace representation must not add or change any existing public request model field")
			afterAPI, err := json.Marshal(public)
			require.NoError(t, err, "encode the unchanged public request envelope after raw observation")
			require.Equal(t, beforeAPI, afterAPI, "passive workspace metadata must not leak into or alter the existing request JSON API")
		})
	}
}

func TestSessionInputWorkspaceReaderScopePostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "install the full scoped workspace reader prerequisite")
	tests := []struct{ name, scope string }{
		{name: "wrong tenant cannot read a stored request", scope: "org"},
		{name: "wrong parent cannot read tenant metadata", scope: "session"},
		{name: "missing request has no inferred unprovable row", scope: "missing"},
		{name: "another real request with no metadata remains absent", scope: "unrecorded"},
		{name: "foreign tenant request cannot leak metadata", scope: "foreign"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			request := fixture.requests[0]
			_, err := insertWorkspaceMigrationRecord(ctx, actor, workspaceMigrationArguments(workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified}}))
			require.NoError(t, err, "store one exact row before probing missing or mismatched reader scopes")
			org, session, id := request.OrgID, request.SessionID, request.ID
			switch tt.scope {
			case "org":
				org = uuid.New()
			case "session":
				session = uuid.New()
			case "missing":
				id = uuid.New()
			case "unrecorded":
				id = fixture.requests[1].ID
			case "foreign":
				other := seedRequestEpochMigrationFixture(t, ctx, actor)
				_, writeErr := insertWorkspaceMigrationRecord(ctx, actor, workspaceMigrationArguments(workspaceMigrationRecord{OrgID: other.requests[0].OrgID, SessionID: other.requests[0].SessionID, RequestID: other.requests[0].ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnavailable}}))
				require.NoError(t, writeErr, "store actual foreign metadata so scope rejection cannot pass merely because that request lacks a row")
				id = other.requests[0].ID
			}
			actual, err := db.NewSessionHumanInputRequestStore(actor).GetWorkspaceCompatibility(ctx, org, session, id)
			require.ErrorIs(t, err, pgx.ErrNoRows, "an absent exact tenant/session/request row must stay missing rather than infer a classification")
			require.Equal(t, db.InputWorkspaceCompatibilityMetadata{}, actual, "failed scoped reads must never expose a partial or another request's metadata")
		})
	}
}

func TestSessionInputWorkspaceLegacyCompatibilityPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "install passive representation without converting legacy answer or cancellation owners")
	tests := []struct {
		name, action string
		threaded     bool
	}{
		{name: "threadless legacy answer", action: "answer"}, {name: "threaded legacy answer", action: "answer", threaded: true},
		{name: "threadless legacy cancellation", action: "cancel"}, {name: "threaded legacy cancellation", action: "cancel", threaded: true},
		{name: "archived parent and lane remain observable", action: "archive", threaded: true}, {name: "deleted parent remains observable", action: "delete"},
		{name: "expired request metadata remains observable", action: "expire", threaded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			request := fixture.requests[0]
			if tt.threaded {
				request = fixture.requests[1]
			}
			want := db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, WorkspaceGeneration: workspaceMigrationPointer(int64(math.MinInt64)), WorkspaceState: json.RawMessage(`null`), Reason: workspaceMigrationPointer("synthetic raw observation")}
			_, err := insertWorkspaceMigrationRecord(ctx, actor, workspaceMigrationArguments(workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: want}))
			require.NoError(t, err, "install deliberately incomplete metadata that must not become a new legacy behavior gate")
			if tt.action == "archive" || tt.action == "delete" || tt.action == "expire" {
				batch := &pgx.Batch{}
				if tt.action == "archive" {
					batch.Queue(`UPDATE sessions SET archived_at=now() WHERE org_id=$1 AND id=$2`, request.OrgID, request.SessionID)
					batch.Queue(`UPDATE session_threads SET archived_at=now() WHERE org_id=$1 AND session_id=$2`, request.OrgID, request.SessionID)
				} else if tt.action == "delete" {
					batch.Queue(`UPDATE sessions SET deleted_at=now() WHERE org_id=$1 AND id=$2`, request.OrgID, request.SessionID)
				} else {
					batch.Queue(`UPDATE session_human_input_requests SET expires_at='2020-01-01 00:00:00+00' WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID)
				}
				require.NoError(t, actor.SendBatch(ctx, batch).Close(), "change only the synthetic parent's existing visibility markers")
				before := workspaceMigrationRequestRow(t, ctx, actor, request)
				actual, readErr := db.NewSessionHumanInputRequestStore(actor).GetWorkspaceCompatibility(ctx, request.OrgID, request.SessionID, request.ID)
				require.NoError(t, readErr, "raw retained metadata must not be hidden by request expiry or archived/deleted parent filters")
				require.Equal(t, want, actual, "parent visibility must not rewrite unknown metadata or synthesize readiness")
				require.Equal(t, before, workspaceMigrationRequestRow(t, ctx, actor, request), "observing retained metadata must leave the complete request unchanged")
				return
			}
			user := uuid.New()
			_, err = actor.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Synthetic workspace metadata actor')`, user, request.OrgID, user.String()+"@example.invalid")
			require.NoError(t, err, "create an owned synthetic answer actor without changing authorization policy")
			tx, err := actor.Begin(ctx)
			require.NoError(t, err, "bound the unchanged legacy transition to one known transaction timestamp")
			defer func() { _ = tx.Rollback(ctx) }()
			var stamp time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&stamp), "capture the exact legacy answered_at value without timing approximations")
			answer := "Synthetic response"
			store := db.NewSessionHumanInputRequestStore(tx)
			var actual models.HumanInputRequest
			if tt.action == "answer" {
				actual, err = store.AnswerPending(ctx, request.OrgID, request.SessionID, request.ID, &answer, json.RawMessage(`{}`), user)
			} else {
				actual, err = store.CancelPending(ctx, request.OrgID, request.SessionID, request.ID, &answer, json.RawMessage(`{}`), user)
			}
			require.NoError(t, err, "the real legacy answer/cancel API must not enforce passive workspace metadata")
			expected := request
			expected.Status = models.HumanInputRequestStatusAnswered
			if tt.action == "cancel" {
				expected.Status = models.HumanInputRequestStatusCancelled
			}
			expected.AnswerText, expected.AnswerPayload, expected.AnsweredBy, expected.AnsweredAt = &answer, json.RawMessage(`{}`), &user, &stamp
			require.Equal(t, expected, actual, "the complete public result must retain exact legacy transition fields and all unchanged request fields")
			require.NoError(t, tx.Commit(ctx), "commit only the owned synthetic legacy transition")
			stored, err := db.NewSessionHumanInputRequestStore(actor).GetByID(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "read the full request after the known legacy commit")
			require.Equal(t, expected, stored, "committed legacy model behavior must remain exactly unchanged")
			metadata, err := db.NewSessionHumanInputRequestStore(actor).GetWorkspaceCompatibility(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "terminal answered/cancelled request metadata must remain readable without pending-only filtering")
			require.Equal(t, want, metadata, "legacy writes must neither clear nor manufacture any workspace certification metadata")
		})
	}
}

func TestSessionInputWorkspaceRequestRetentionPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "install the exact request FK before physical-retention proofs")
	tests := []struct {
		name     string
		threaded bool
	}{{name: "threadless retained request"}, {name: "threaded retained request", threaded: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedRequestEpochMigrationFixture(t, ctx, actor)
			request := fixture.requests[0]
			if tt.threaded {
				request = fixture.requests[1]
			}
			want := workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified}}
			_, err := insertWorkspaceMigrationRecord(ctx, actor, workspaceMigrationArguments(want))
			require.NoError(t, err, "install the exact compatibility child before deleting its otherwise unreferenced request")
			before := workspaceMigrationRequestRow(t, ctx, actor, request)
			_, err = actor.Exec(ctx, `DELETE FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID)
			requireCutoverMigrationFailure(t, err, "23503", "session_input_workspace_compatibility_request_scope_fk", "", "retained request deletion")
			require.Equal(t, before, workspaceMigrationRequestRow(t, ctx, actor, request), "exact FK refusal must preserve the entire original request rather than cascade its evidence")
			tx, err := actor.Begin(ctx)
			require.NoError(t, err, "begin a disposable transaction to prove explicit dependency removal rather than introduce a retention policy")
			defer func() { _ = tx.Rollback(ctx) }()
			tag, err := tx.Exec(ctx, `DELETE FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND request_id=$3`, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "remove only the exact owned compatibility child inside the rolled-back proof unit")
			require.Equal(t, int64(1), tag.RowsAffected(), "the probe must remove its actual retained child rather than a nonexistent row")
			tag, err = tx.Exec(ctx, `DELETE FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, "the physical request becomes deletable only after its explicit compatibility dependency is removed")
			require.Equal(t, int64(1), tag.RowsAffected(), "prove actual physical deletion within the disposable unit")
			require.NoError(t, tx.Rollback(ctx), "restore both request and compatibility evidence instead of adopting a new cleanup policy")
			require.Equal(t, before, workspaceMigrationRequestRow(t, ctx, actor, request), "rollback must restore every original request field")
			stored, err := scanWorkspaceMigrationRecord(actor.QueryRow(ctx, `SELECT `+workspaceMigrationColumns+` FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND request_id=$3`, request.OrgID, request.SessionID, request.ID))
			require.NoError(t, err, "reload the exact compatibility child after dependency-probe rollback")
			require.Equal(t, want, stored, "rollback must restore all retained compatibility fields without reconstruction")
		})
	}
}

func TestSessionInputWorkspaceDependencyRefusalPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(340), "install the reviewed schema before a synthetic downstream dependency")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedRequestEpochMigrationFixture(t, ctx, conn)
	request := fixture.requests[0]
	want := workspaceMigrationRecord{OrgID: request.OrgID, SessionID: request.SessionID, RequestID: request.ID, Metadata: db.InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending}}
	_, err := insertWorkspaceMigrationRecord(ctx, conn, workspaceMigrationArguments(want))
	require.NoError(t, err, "retain real workspace metadata before testing non-cascading dependency refusal")
	_, err = conn.Exec(ctx, `CREATE TABLE workspace_compatibility_dependency_probe(org_id uuid NOT NULL REFERENCES organizations(id),request_id uuid NOT NULL,FOREIGN KEY(org_id,request_id) REFERENCES session_input_workspace_compatibility(org_id,request_id))`)
	require.NoError(t, err, "create an ordinary synthetic child only inside the generated owned database")
	_, err = conn.Exec(ctx, `INSERT INTO workspace_compatibility_dependency_probe(org_id,request_id) VALUES($1,$2)`, request.OrgID, request.ID)
	require.NoError(t, err, "prove the exact tenant/request primary key supports a real scoped reference")
	dependencyBefore := workspaceMigrationDependencyState(t, ctx, conn, request)
	_, err = conn.Exec(ctx, `DROP TABLE session_input_workspace_compatibility`)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "direct down-DDL must refuse an unknown dependent FK instead of using CASCADE")
	require.Equal(t, "2BP01", pgErr.Code, "the refusal must be PostgreSQL dependent-objects-still-exist")
	stored, err := scanWorkspaceMigrationRecord(conn.QueryRow(ctx, `SELECT `+workspaceMigrationColumns+` FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND request_id=$3`, request.OrgID, request.SessionID, request.ID))
	require.NoError(t, err, "read retained metadata after direct DDL refusal")
	require.Equal(t, want, stored, "non-cascading DDL refusal must preserve all actual compatibility data")
	require.Equal(t, dependencyBefore, workspaceMigrationDependencyState(t, ctx, conn, request), "direct DDL refusal must preserve the actual downstream row FK definitions and all workspace index definitions")
	version, dirty, err := migration.Version()
	require.NoError(t, err, "read the unchanged ledger after direct DDL rather than a failed migration")
	require.Equal(t, uint(340), version, "a direct refused DROP must not alter migration version")
	require.False(t, dirty, "a direct refused DROP leaves the clean ledger unchanged; this is not a failed-down-migration ledger claim")
}

func workspaceMigrationExactJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value), "decode synthetic JSON with exact json.Number values rather than float64")
	return value
}

func workspaceMigrationDependencyState(t *testing.T, ctx context.Context, conn *pgx.Conn, request models.HumanInputRequest) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'child',(SELECT to_jsonb(p) FROM workspace_compatibility_dependency_probe p WHERE p.org_id=$1 AND p.request_id=$2),
		'constraints',(SELECT jsonb_agg(pg_get_constraintdef(c.oid,true) ORDER BY c.conname) FROM pg_constraint c WHERE c.conrelid='workspace_compatibility_dependency_probe'::regclass),
		'indexes',(SELECT jsonb_agg(i.indexdef ORDER BY i.indexname) FROM pg_indexes i WHERE i.schemaname='public' AND i.tablename='session_input_workspace_compatibility'))`, request.OrgID, request.ID).Scan(&raw)
	require.NoError(t, err, "capture the complete synthetic dependency row FK definitions and workspace indexes without per-record queries")
	return raw
}

func workspaceMigrationRequestRow(t *testing.T, ctx context.Context, conn *pgx.Conn, request models.HumanInputRequest) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(r) FROM session_human_input_requests r WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID).Scan(&raw), "read every raw field of only the exact synthetic request")
	return raw
}
