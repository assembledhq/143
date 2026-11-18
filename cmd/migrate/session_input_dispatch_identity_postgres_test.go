package main

import (
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

func TestSessionInputDispatchIdentityBehaviorPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgx.Conn, requestEpochMigrationFixture, time.Time)
	}{
		{name: "complete records and raw representation", run: requireDispatchIdentityRecords},
		{name: "structural constraints", run: requireDispatchIdentityConstraints},
		{name: "exact parent membership", run: requireDispatchIdentityScope},
		{name: "uniqueness and independent identities", run: requireDispatchIdentityUniqueness},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			require.NoError(t, migration.Migrate(335), "apply the complete real schema through each separate dispatch parent FK")
			var count int64
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_input_dispatches`).Scan(&count), "inspect the new dispatch table before introducing synthetic test rows")
			require.Equal(t, int64(0), count, "the foundation must create no input identities or runnable work on its own")
			fixture := seedRequestEpochMigrationFixture(t, ctx, conn)
			before := requestEpochMigrationLegacyRows(t, ctx, conn, fixture)
			var stamp time.Time
			require.NoError(t, conn.QueryRow(ctx, `SELECT '2026-01-01 12:00:00+00'::timestamptz`).Scan(&stamp), "decode a fixed synthetic timestamp through the same driver as complete records")
			tt.run(t, ctx, conn, fixture, stamp)
			require.JSONEq(t, string(before), string(requestEpochMigrationLegacyRows(t, ctx, conn, fixture)), "synthetic identity storage must not change any existing request or parent lifecycle field")
		})
	}
}

type dispatchIdentityRecord struct {
	ID                   uuid.UUID
	OrgID                uuid.UUID
	SessionID            uuid.UUID
	ThreadID             *uuid.UUID
	SessionEpoch         *int64
	ThreadEpoch          *int64
	MessageID            *int64
	RequestID            *uuid.UUID
	Kind                 string
	IngressKind          string
	IngressKey           string
	PayloadDigest        string
	ValidatedPayload     json.RawMessage
	NormalizationVersion int32
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

const dispatchIdentityColumns = `id,org_id,session_id,thread_id,session_epoch,thread_epoch,message_id,request_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,created_at,updated_at`

func newDispatchIdentityRecord(fixture requestEpochMigrationFixture, stamp time.Time) dispatchIdentityRecord {
	return dispatchIdentityRecord{
		ID: uuid.New(), OrgID: fixture.epochs.orgID, SessionID: fixture.epochs.sessionID,
		Kind: "initial", IngressKind: "synthetic", IngressKey: uuid.NewString(), PayloadDigest: "synthetic-digest",
		ValidatedPayload: json.RawMessage(`{}`), NormalizationVersion: 0, CreatedAt: stamp, UpdatedAt: stamp,
	}
}

func dispatchIdentityArguments(record dispatchIdentityRecord) []any {
	return []any{record.ID, record.OrgID, record.SessionID, record.ThreadID, record.SessionEpoch, record.ThreadEpoch,
		record.MessageID, record.RequestID, record.Kind, record.IngressKind, record.IngressKey, record.PayloadDigest,
		record.ValidatedPayload, record.NormalizationVersion, record.CreatedAt, record.UpdatedAt}
}

func insertDispatchIdentityRecord(ctx context.Context, database db.DBTX, arguments []any) (dispatchIdentityRecord, error) {
	return scanDispatchIdentityRecord(database.QueryRow(ctx, `INSERT INTO session_input_dispatches (`+dispatchIdentityColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING `+dispatchIdentityColumns, arguments...))
}

func scanDispatchIdentityRecord(row pgx.Row) (dispatchIdentityRecord, error) {
	var record dispatchIdentityRecord
	err := row.Scan(&record.ID, &record.OrgID, &record.SessionID, &record.ThreadID, &record.SessionEpoch,
		&record.ThreadEpoch, &record.MessageID, &record.RequestID, &record.Kind, &record.IngressKind,
		&record.IngressKey, &record.PayloadDigest, &record.ValidatedPayload, &record.NormalizationVersion,
		&record.CreatedAt, &record.UpdatedAt)
	return record, err
}

func requireDispatchIdentityRecords(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture, stamp time.Time) {
	t.Helper()
	// Cases share one isolated connection and run sequentially. The four
	// independent full-schema fixture categories run in parallel.
	for _, kind := range []string{"message", "answer", "denial", "initial", "retry", "recovery"} {
		record := newDispatchIdentityRecord(fixture, stamp)
		record.Kind = kind
		if kind == "answer" || kind == "denial" {
			record.RequestID = &fixture.requests[0].ID
		}
		actual, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		require.NoError(t, err, kind+" must be representable before any message consumer or claim is installed")
		require.Equal(t, record, actual, kind+" must round-trip every identity and metadata field without inferring epochs")
	}
	tests := []struct {
		name   string
		update func(*dispatchIdentityRecord)
	}{
		{name: "explicit zero epochs", update: func(record *dispatchIdentityRecord) {
			session, thread := int64(0), int64(0)
			record.ThreadID, record.SessionEpoch, record.ThreadEpoch = &fixture.epochs.threadID, &session, &thread
		}},
		{name: "complete large metadata", update: func(record *dispatchIdentityRecord) {
			session, thread, message := int64(math.MaxInt64-1), int64(math.MaxInt64), int64(math.MaxInt64)
			record.ThreadID, record.SessionEpoch, record.ThreadEpoch = &fixture.epochs.threadID, &session, &thread
			record.MessageID, record.RequestID = &message, &fixture.requests[1].ID
			record.Kind, record.NormalizationVersion = "recovery", math.MaxInt32
			record.ValidatedPayload = json.RawMessage(`{"synthetic": true}`)
		}},
		{name: "session epoch without a lane epoch", update: func(record *dispatchIdentityRecord) {
			session := int64(17)
			record.SessionEpoch = &session
		}},
		{name: "lane epoch without inferred session epoch", update: func(record *dispatchIdentityRecord) {
			thread := int64(19)
			record.ThreadID, record.ThreadEpoch = &fixture.epochs.threadID, &thread
		}},
		{name: "thread identity with unknown epochs", update: func(record *dispatchIdentityRecord) {
			record.ThreadID = &fixture.epochs.threadID
		}},
		{name: "JSON null is stored explicitly", update: func(record *dispatchIdentityRecord) { record.ValidatedPayload = json.RawMessage(`null`) }},
		{name: "future JSON array is stored raw", update: func(record *dispatchIdentityRecord) { record.ValidatedPayload = json.RawMessage(`["future", null]`) }},
		{name: "future JSON scalar is stored raw", update: func(record *dispatchIdentityRecord) { record.ValidatedPayload = json.RawMessage(`42`) }},
		{name: "future JSON string is stored raw", update: func(record *dispatchIdentityRecord) { record.ValidatedPayload = json.RawMessage(`"future"`) }},
	}
	for _, tt := range tests {
		record := newDispatchIdentityRecord(fixture, stamp)
		tt.update(&record)
		actual, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		require.NoError(t, err, tt.name+" must not depend on any runtime readiness or authority decision")
		require.Equal(t, record, actual, tt.name+" must preserve the exact complete row including independent nullability")
	}

	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "open a fixture transaction to independently identify timestamp defaults")
	defer func() { _ = tx.Rollback(ctx) }()
	var now time.Time
	require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&now), "record the exact database timestamp used by both defaults")
	want := newDispatchIdentityRecord(fixture, now)
	actual, err := scanDispatchIdentityRecord(tx.QueryRow(ctx, `INSERT INTO session_input_dispatches
		(org_id,session_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+dispatchIdentityColumns,
		want.OrgID, want.SessionID, want.Kind, want.IngressKind, want.IngressKey, want.PayloadDigest, want.ValidatedPayload, want.NormalizationVersion))
	require.NoError(t, err, "only identity and timestamp defaults may fill omitted fields")
	require.NotEqual(t, uuid.Nil, actual.ID, "the UUID default must create a concrete synthetic dispatch identity")
	want.ID = actual.ID
	require.Equal(t, want, actual, "default insertion must populate only UUID and timestamps while all optional identities remain absent")
	require.NoError(t, tx.Commit(ctx), "commit the complete default-generated identity before checking persistence")
	persisted, err := scanDispatchIdentityRecord(conn.QueryRow(ctx, `SELECT `+dispatchIdentityColumns+` FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, want.OrgID, want.SessionID, want.ID))
	require.NoError(t, err, "read the generated identity using the exact tenant and session")
	require.Equal(t, want, persisted, "the complete default-generated identity must remain unchanged after commit")
}

func requireDispatchIdentityConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture, stamp time.Time) {
	t.Helper()
	tests := []struct {
		name       string
		position   int
		value      any
		code       string
		constraint string
		column     string
	}{
		{name: "unknown kind", position: 8, value: "future", code: "23514", constraint: "session_input_dispatches_kind_check"},
		{name: "empty kind", position: 8, value: "", code: "23514", constraint: "session_input_dispatches_kind_check"},
		{name: "empty ingress kind", position: 9, value: "", code: "23514", constraint: "session_input_dispatches_ingress_kind_check"},
		{name: "blank ingress kind", position: 9, value: "   ", code: "23514", constraint: "session_input_dispatches_ingress_kind_check"},
		{name: "empty ingress key", position: 10, value: "", code: "23514", constraint: "session_input_dispatches_ingress_key_check"},
		{name: "blank ingress key", position: 10, value: "   ", code: "23514", constraint: "session_input_dispatches_ingress_key_check"},
		{name: "empty digest", position: 11, value: "", code: "23514", constraint: "session_input_dispatches_payload_digest_check"},
		{name: "blank digest", position: 11, value: "   ", code: "23514", constraint: "session_input_dispatches_payload_digest_check"},
		{name: "negative normalization", position: 13, value: int32(-1), code: "23514", constraint: "session_input_dispatches_normalization_version_check"},
		{name: "negative session epoch", position: 4, value: int64(-1), code: "23514", constraint: "session_input_dispatches_session_epoch_check"},
		{name: "thread epoch without lane", position: 5, value: int64(0), code: "23514", constraint: "session_input_dispatches_thread_epoch_lane_check"},
		{name: "zero message identity", position: 6, value: int64(0), code: "23514", constraint: "session_input_dispatches_message_id_check"},
		{name: "negative message identity", position: 6, value: int64(-1), code: "23514", constraint: "session_input_dispatches_message_id_check"},
		{name: "answer without request", position: 8, value: "answer", code: "23514", constraint: "session_input_dispatches_request_kind_check"},
		{name: "denial without request", position: 8, value: "denial", code: "23514", constraint: "session_input_dispatches_request_kind_check"},
		{name: "SQL null organization", position: 1, code: "23502", column: "org_id"},
		{name: "SQL null session", position: 2, code: "23502", column: "session_id"},
		{name: "SQL null kind", position: 8, code: "23502", column: "kind"},
		{name: "SQL null ingress kind", position: 9, code: "23502", column: "ingress_kind"},
		{name: "SQL null ingress key", position: 10, code: "23502", column: "ingress_key"},
		{name: "SQL null digest", position: 11, code: "23502", column: "payload_digest"},
		{name: "SQL null payload", position: 12, code: "23502", column: "validated_payload"},
		{name: "SQL null normalization", position: 13, code: "23502", column: "normalization_version"},
	}
	// Deliberately sequential: each rejected autocommit statement must leave
	// this fixture connection usable for its next independent case.
	for _, tt := range tests {
		arguments := dispatchIdentityArguments(newDispatchIdentityRecord(fixture, stamp))
		arguments[tt.position] = tt.value
		_, err := insertDispatchIdentityRecord(ctx, conn, arguments)
		requireDispatchIdentityFailure(t, err, tt.code, tt.constraint, tt.column, tt.name)
	}
	arguments := dispatchIdentityArguments(newDispatchIdentityRecord(fixture, stamp))
	arguments[3], arguments[5] = fixture.epochs.threadID, int64(-1)
	_, err := insertDispatchIdentityRecord(ctx, conn, arguments)
	requireDispatchIdentityFailure(t, err, "23514", "session_input_dispatches_thread_epoch_check", "", "negative thread epoch with a real lane")
	record := newDispatchIdentityRecord(fixture, stamp)
	_, err = conn.Exec(ctx, `INSERT INTO session_input_dispatches(org_id,session_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, record.OrgID, record.SessionID, record.Kind, record.IngressKind, record.IngressKey, record.PayloadDigest, record.ValidatedPayload)
	requireDispatchIdentityFailure(t, err, "23502", "", "normalization_version", "omitted normalization version")
}

func requireDispatchIdentityFailure(t *testing.T, err error, code, constraint, column, label string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, label+" must be rejected by the real PostgreSQL schema")
	require.Equal(t, code, pgErr.Code, label+" must fail with the precise structural SQLSTATE")
	require.Equal(t, constraint, pgErr.ConstraintName, label+" must fail at the intended constraint rather than an unrelated rule")
	require.Equal(t, column, pgErr.ColumnName, label+" must identify the expected not-null column when applicable")
}

func newDispatchSiblingScope(t *testing.T, ctx context.Context, conn *pgx.Conn, orgID uuid.UUID) requestEpochMigrationFixture {
	t.Helper()
	session := models.Session{
		OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow,
		Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive,
		ValidationPolicy: models.SessionValidationPolicyOnSessionEnd,
	}
	require.NoError(t, db.NewSessionStore(conn).Create(ctx, &session), "create another real session in the same synthetic tenant")
	thread := models.SessionThread{OrgID: orgID, SessionID: session.ID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic sibling scope", Status: models.ThreadStatusIdle}
	require.NoError(t, db.NewSessionThreadStore(conn).Create(ctx, &thread, 10), "create an exact lane in the other synthetic session")
	request := models.HumanInputRequest{OrgID: orgID, SessionID: session.ID, ThreadID: &thread.ID, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic input", Body: "Provide a synthetic response"}
	require.NoError(t, db.NewSessionHumanInputRequestStore(conn).Create(ctx, &request), "create a request whose tenant matches but parent session differs")
	return requestEpochMigrationFixture{epochs: epochMigrationFixture{orgID: orgID, sessionID: session.ID, threadID: thread.ID}, requests: []models.HumanInputRequest{request}}
}

func requireDispatchIdentityScope(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture, stamp time.Time) {
	t.Helper()
	other := seedRequestEpochMigrationFixture(t, ctx, conn)
	sibling := newDispatchSiblingScope(t, ctx, conn, fixture.epochs.orgID)
	tests := []struct {
		name       string
		update     func(*dispatchIdentityRecord)
		constraint string
	}{
		{name: "another tenant's session", update: func(record *dispatchIdentityRecord) { record.SessionID = other.epochs.sessionID }, constraint: "session_input_dispatches_session_scope_fk"},
		{name: "missing session", update: func(record *dispatchIdentityRecord) { record.SessionID = uuid.New() }, constraint: "session_input_dispatches_session_scope_fk"},
		{name: "another tenant's thread", update: func(record *dispatchIdentityRecord) { record.ThreadID = &other.epochs.threadID }, constraint: "session_input_dispatches_thread_scope_fk"},
		{name: "same tenant's other session thread", update: func(record *dispatchIdentityRecord) { record.ThreadID = &sibling.epochs.threadID }, constraint: "session_input_dispatches_thread_scope_fk"},
		{name: "missing thread", update: func(record *dispatchIdentityRecord) { id := uuid.New(); record.ThreadID = &id }, constraint: "session_input_dispatches_thread_scope_fk"},
		{name: "another tenant's request", update: func(record *dispatchIdentityRecord) { record.RequestID = &other.requests[0].ID }, constraint: "session_input_dispatches_request_scope_fk"},
		{name: "same tenant's other session request", update: func(record *dispatchIdentityRecord) { record.RequestID = &sibling.requests[0].ID }, constraint: "session_input_dispatches_request_scope_fk"},
		{name: "missing request", update: func(record *dispatchIdentityRecord) { id := uuid.New(); record.RequestID = &id }, constraint: "session_input_dispatches_request_scope_fk"},
	}
	for _, tt := range tests {
		record := newDispatchIdentityRecord(fixture, stamp)
		tt.update(&record)
		_, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		requireDispatchIdentityFailure(t, err, "23503", tt.constraint, "", tt.name)
	}
	for _, request := range fixture.requests {
		record := newDispatchIdentityRecord(fixture, stamp)
		record.Kind, record.RequestID, record.ThreadID = "answer", &request.ID, request.ThreadID
		actual, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		require.NoError(t, err, "both threaded and legacy threadless requests must satisfy exact parent membership")
		require.Equal(t, record, actual, "request-scoped identity storage must not fill missing epochs or infer a primary lane")
	}
}

func requireDispatchIdentityUniqueness(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture, stamp time.Time) {
	t.Helper()
	other := seedRequestEpochMigrationFixture(t, ctx, conn)
	sibling := newDispatchSiblingScope(t, ctx, conn, fixture.epochs.orgID)
	first := newDispatchIdentityRecord(fixture, stamp)
	actual, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(first))
	require.NoError(t, err, "establish an exact synthetic ingress identity")
	require.Equal(t, first, actual, "the first ingress identity must persist every supplied field")
	tests := []struct {
		name       string
		update     func(*dispatchIdentityRecord)
		constraint string
	}{
		{name: "duplicate ingress", update: func(record *dispatchIdentityRecord) { record.IngressKey = first.IngressKey }, constraint: "session_input_dispatches_ingress_key"},
		{name: "changed digest cannot create duplicate ingress", update: func(record *dispatchIdentityRecord) {
			record.IngressKey, record.PayloadDigest = first.IngressKey, "different-synthetic-digest"
		}, constraint: "session_input_dispatches_ingress_key"},
		{name: "different kind cannot create duplicate ingress", update: func(record *dispatchIdentityRecord) { record.IngressKey, record.Kind = first.IngressKey, "retry" }, constraint: "session_input_dispatches_ingress_key"},
		{name: "ingress identity spans sessions in the same tenant", update: func(record *dispatchIdentityRecord) {
			record.IngressKey, record.SessionID = first.IngressKey, sibling.epochs.sessionID
		}, constraint: "session_input_dispatches_ingress_key"},
		{name: "duplicate UUID", update: func(record *dispatchIdentityRecord) { record.ID = first.ID }, constraint: "session_input_dispatches_pkey"},
	}
	for _, tt := range tests {
		record := newDispatchIdentityRecord(fixture, stamp)
		tt.update(&record)
		_, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		requireDispatchIdentityFailure(t, err, "23505", tt.constraint, "", tt.name)
	}
	separations := []struct {
		name   string
		update func(*dispatchIdentityRecord)
	}{
		{name: "different ingress kind", update: func(record *dispatchIdentityRecord) {
			record.IngressKind, record.IngressKey = "other-synthetic-ingress", first.IngressKey
		}},
		{name: "different tenant", update: func(record *dispatchIdentityRecord) {
			record.OrgID, record.SessionID, record.IngressKey = other.epochs.orgID, other.epochs.sessionID, first.IngressKey
		}},
		{name: "multiple absent message identities", update: func(record *dispatchIdentityRecord) {}},
		{name: "first waiting answer identity", update: func(record *dispatchIdentityRecord) {
			record.Kind, record.RequestID = "answer", &fixture.requests[0].ID
		}},
		{name: "another waiting answer identity for the same request", update: func(record *dispatchIdentityRecord) {
			record.Kind, record.RequestID = "answer", &fixture.requests[0].ID
		}},
	}
	for _, tt := range separations {
		record := newDispatchIdentityRecord(fixture, stamp)
		tt.update(&record)
		actual, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		require.NoError(t, err, tt.name+" must remain a distinct identity without introducing premature bound-request uniqueness")
		require.Equal(t, record, actual, tt.name+" must persist the exact complete row")
	}

	message := int64(501)
	firstMessage := newDispatchIdentityRecord(fixture, stamp)
	firstMessage.Kind, firstMessage.MessageID = "message", &message
	actual, err = insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(firstMessage))
	require.NoError(t, err, "store a synthetic message identity without claiming partitioned-message ownership validation")
	require.Equal(t, firstMessage, actual, "the synthetic message identity must remain exact")
	for _, sessionID := range []uuid.UUID{fixture.epochs.sessionID, sibling.epochs.sessionID} {
		record := newDispatchIdentityRecord(fixture, stamp)
		record.SessionID, record.MessageID = sessionID, &message
		_, err := insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
		requireDispatchIdentityFailure(t, err, "23505", "idx_session_input_dispatches_message", "", "same tenant message identity across sessions and ingress keys")
	}
	record := newDispatchIdentityRecord(other, stamp)
	record.MessageID = &message
	actual, err = insertDispatchIdentityRecord(ctx, conn, dispatchIdentityArguments(record))
	require.NoError(t, err, "another tenant may retain the same synthetic message number independently")
	require.Equal(t, record, actual, "message uniqueness must remain tenant-scoped rather than global")
}
