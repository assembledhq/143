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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionHumanInputEpochMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "legacy requests upgrade rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var fixture requestEpochMigrationFixture
			var before json.RawMessage
			if tt.upgrade {
				require.NoError(t, migration.Migrate(323), "construct the full preceding schema before adding request epochs")
				fixture = seedRequestEpochMigrationFixture(t, ctx, conn)
				before = requestEpochMigrationLegacyRows(t, ctx, conn, fixture)
				requireRequestEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, 323)
			}
			require.NoError(t, migration.Migrate(324), "apply the real migration chain through nullable request admission epochs")
			if !tt.upgrade {
				fixture = seedRequestEpochMigrationFixture(t, ctx, conn)
				before = requestEpochMigrationLegacyRows(t, ctx, conn, fixture)
			}
			requireRequestEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, 324)
			requireRequestEpochMigrationUnknown(t, ctx, conn, fixture)
			newFixture := seedRequestEpochMigrationFixture(t, ctx, conn)
			requireRequestEpochMigrationUnknown(t, ctx, conn, newFixture)
			requireRequestEpochMigrationScope(t, ctx, conn, fixture, newFixture)
			requireRequestEpochMigrationRoundTrip(t, ctx, conn, fixture)
			requireRequestEpochLegacyTransitions(t, ctx, conn)

			// Schema changes and assertions share this isolated connection and
			// run sequentially; independent database fixtures run in parallel.
			beforeRollback := requestEpochMigrationLegacyRows(t, ctx, conn, fixture)
			require.NoError(t, migration.Steps(-1), "remove only the passive request columns after rolling back their readers")
			requireRequestEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 323)
			require.NoError(t, migration.Migrate(324), "reapply nullable request metadata without inferring historical admission epochs")
			requireRequestEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 324)
			requireRequestEpochMigrationUnknown(t, ctx, conn, fixture)
			requireRequestEpochMigrationUnknown(t, ctx, conn, newFixture)
		})
	}
}

type requestEpochMigrationFixture struct {
	epochs   epochMigrationFixture
	requests []models.HumanInputRequest
}

func seedRequestEpochMigrationFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) requestEpochMigrationFixture {
	t.Helper()
	fixture := requestEpochMigrationFixture{epochs: seedEpochMigrationFixture(t, ctx, conn)}
	_, err := conn.Exec(ctx, `UPDATE sessions SET admission_epoch=23 WHERE org_id=$1 AND id=$2`, fixture.epochs.orgID, fixture.epochs.sessionID)
	require.NoError(t, err, "seed a nonzero synthetic session epoch before legacy request creation")
	_, err = conn.Exec(ctx, `UPDATE session_threads SET admission_epoch=31 WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
	require.NoError(t, err, "seed an independent nonzero thread epoch before legacy request creation")
	store := db.NewSessionHumanInputRequestStore(conn)
	// Each fixture owns one connection; both legacy creation variants are
	// deliberately sequential while independent fixtures run in parallel.
	for _, threadID := range []*uuid.UUID{nil, &fixture.epochs.threadID} {
		request := models.HumanInputRequest{
			OrgID: fixture.epochs.orgID, SessionID: fixture.epochs.sessionID, ThreadID: threadID,
			TurnNumber: 1, AgentType: models.DefaultDefaultAgentType,
			Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic input", Body: "Provide a synthetic response",
		}
		require.NoError(t, store.Create(ctx, &request), "legacy threaded and threadless creation must omit passive request epochs")
		stored, err := store.GetByID(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "capture the complete legacy request representation after default normalization")
		fixture.requests = append(fixture.requests, stored)
	}
	return fixture
}

func requestEpochMigrationLegacyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
		'requests',(SELECT jsonb_agg(to_jsonb(r)-'admission_session_epoch'-'admission_thread_epoch' ORDER BY r.id) FROM session_human_input_requests r WHERE r.org_id=$1 AND r.session_id=$2))`, fixture.epochs.orgID, fixture.epochs.sessionID).Scan(&value)
	require.NoError(t, err, "capture every legacy request field and the complete parent epoch state independently of new columns")
	return value
}

func requireRequestEpochMigrationBoundary(t *testing.T, ctx context.Context, conn *pgx.Conn, migration *migrate.Migrate, fixture requestEpochMigrationFixture, before json.RawMessage, version uint) {
	t.Helper()
	actualVersion, dirty, err := migration.Version()
	require.NoError(t, err, "read the real migration ledger at each request epoch boundary")
	require.Equal(t, version, actualVersion, "request metadata migration must reach its exact historical version")
	require.False(t, dirty, "request epoch transitions must leave the migration ledger clean")
	require.JSONEq(t, string(before), string(requestEpochMigrationLegacyRows(t, ctx, conn, fixture)), "upgrade rollback and reapply must preserve every legacy request and parent field")
	type column struct {
		Name     string  `db:"column_name"`
		DataType string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	want := []column{}
	if version == 324 {
		want = []column{
			{Name: "admission_session_epoch", DataType: "bigint", Nullable: "YES"},
			{Name: "admission_thread_epoch", DataType: "bigint", Nullable: "YES"},
		}
	}
	rows, err := conn.Query(ctx, `SELECT column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_human_input_requests' AND column_name IN ('admission_session_epoch','admission_thread_epoch') ORDER BY column_name`)
	require.NoError(t, err, "inspect request epoch types nullability and lack of defaults through PostgreSQL catalogs")
	got, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect complete request epoch column definitions at the selected boundary")
	require.Equal(t, want, got, "request epoch columns must be nullable bigints with no defaults and disappear on rollback")
	legacy := db.NewSessionHumanInputRequestStore(conn)
	for _, request := range fixture.requests {
		actual, err := legacy.GetByID(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "legacy request reads must work at every migration boundary")
		require.Equal(t, request, actual, "the complete public request projection must remain unchanged by passive migration steps")
	}
}

func requireRequestEpochMigrationUnknown(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) {
	t.Helper()
	for _, request := range fixture.requests {
		var raw db.LifecycleRequestEpochState
		err := conn.QueryRow(ctx, `SELECT admission_session_epoch,admission_thread_epoch FROM session_human_input_requests WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID).Scan(&raw.AdmissionSessionEpoch, &raw.AdmissionThreadEpoch)
		require.NoError(t, err, "read nullable request metadata independently of legacy models")
		require.Equal(t, db.LifecycleRequestEpochState{}, raw, "both threaded and threadless requests must retain unknown epochs despite nonzero parent values")
		actual, err := db.NewLifecycleEpochStore(conn).GetRequest(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "read passive admission metadata using the scoped request accessor")
		require.Equal(t, raw, actual, "the accessor must preserve both absent epochs without filling either from parent state")
	}
}

func requireRequestEpochMigrationScope(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture, other requestEpochMigrationFixture) {
	t.Helper()
	request := fixture.requests[0]
	tests := []struct {
		name                        string
		orgID, sessionID, requestID uuid.UUID
	}{
		{name: "another tenant", orgID: other.epochs.orgID, sessionID: request.SessionID, requestID: request.ID},
		{name: "another parent session", orgID: request.OrgID, sessionID: other.epochs.sessionID, requestID: request.ID},
		{name: "another request", orgID: request.OrgID, sessionID: request.SessionID, requestID: other.requests[0].ID},
	}
	// Scope cases share one connection and are intentionally sequential.
	for _, tt := range tests {
		state, err := db.NewLifecycleEpochStore(conn).GetRequest(ctx, tt.orgID, tt.sessionID, tt.requestID)
		require.ErrorIs(t, err, pgx.ErrNoRows, tt.name+" must not expose request metadata outside its exact scope")
		require.Equal(t, db.LifecycleRequestEpochState{}, state, tt.name+" must return no partial metadata")
	}
}

func requireRequestEpochMigrationRoundTrip(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) {
	t.Helper()
	tests := []struct {
		name string
		want func() db.LifecycleRequestEpochState
	}{
		{name: "present zeros", want: func() db.LifecycleRequestEpochState {
			session, thread := int64(0), int64(0)
			return db.LifecycleRequestEpochState{AdmissionSessionEpoch: &session, AdmissionThreadEpoch: &thread}
		}},
		{name: "large independent epochs", want: func() db.LifecycleRequestEpochState {
			session, thread := int64(math.MaxInt64-1), int64(math.MaxInt64)
			return db.LifecycleRequestEpochState{AdmissionSessionEpoch: &session, AdmissionThreadEpoch: &thread}
		}},
		{name: "both epochs reset to unknown", want: func() db.LifecycleRequestEpochState { return db.LifecycleRequestEpochState{} }},
		{name: "session epoch only", want: func() db.LifecycleRequestEpochState {
			session := int64(29)
			return db.LifecycleRequestEpochState{AdmissionSessionEpoch: &session}
		}},
		{name: "thread epoch only", want: func() db.LifecycleRequestEpochState {
			thread := int64(37)
			return db.LifecycleRequestEpochState{AdmissionThreadEpoch: &thread}
		}},
	}
	// These synthetic updates test representation only and share one isolated
	// connection. The database fixtures, rather than these cases, run in parallel.
	for _, request := range fixture.requests {
		beforeJSON, err := json.Marshal(request)
		require.NoError(t, err, "serialize the complete legacy request before populating passive metadata")
		for _, tt := range tests {
			want := tt.want()
			_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET admission_session_epoch=$4,admission_thread_epoch=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID, want.AdmissionSessionEpoch, want.AdmissionThreadEpoch)
			require.NoError(t, err, tt.name+" must be representable independently for threaded and threadless requests")
			actual, err := db.NewLifecycleEpochStore(conn).GetRequest(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, tt.name+" must round-trip through the real PostgreSQL driver")
			require.Equal(t, want, actual, tt.name+" must retain exact bigint values and independent nullability")
			legacy, err := db.NewSessionHumanInputRequestStore(conn).GetByID(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, tt.name+" must remain readable through the unchanged legacy store")
			require.Equal(t, request, legacy, tt.name+" must preserve every public request model field")
			afterJSON, err := json.Marshal(legacy)
			require.NoError(t, err, "serialize the public request after passive metadata changes")
			require.JSONEq(t, string(beforeJSON), string(afterJSON), "passive request epochs must not alter any existing API field or add new fields")
		}
		for _, key := range []string{"admission_session_epoch", "admission_thread_epoch"} {
			require.NotContains(t, string(beforeJSON), `"`+key+`"`, "passive request metadata must remain absent from the public API representation")
		}
	}
}

func requireRequestEpochLegacyTransitions(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	actions := []struct {
		name   string
		status models.HumanInputRequestStatus
		write  func(*db.SessionHumanInputRequestStore, models.HumanInputRequest, *string, uuid.UUID) (models.HumanInputRequest, error)
	}{
		{
			name: "legacy answer", status: models.HumanInputRequestStatusAnswered,
			write: func(store *db.SessionHumanInputRequestStore, request models.HumanInputRequest, answer *string, actor uuid.UUID) (models.HumanInputRequest, error) {
				return store.AnswerPending(ctx, request.OrgID, request.SessionID, request.ID, answer, json.RawMessage(`{}`), actor)
			},
		},
		{
			name: "legacy cancellation", status: models.HumanInputRequestStatusCancelled,
			write: func(store *db.SessionHumanInputRequestStore, request models.HumanInputRequest, answer *string, actor uuid.UUID) (models.HumanInputRequest, error) {
				return store.CancelPending(ctx, request.OrgID, request.SessionID, request.ID, answer, json.RawMessage(`{}`), actor)
			},
		},
	}
	// Legacy writes, visibility transitions and their assertions share this
	// isolated connection, so each action owns fresh rows and runs sequentially.
	for _, action := range actions {
		fixture := seedRequestEpochMigrationFixture(t, ctx, conn)
		actor := uuid.New()
		_, err := conn.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Synthetic request actor')`, actor, fixture.epochs.orgID, actor.String()+"@example.invalid")
		require.NoError(t, err, "create a synthetic actor for real legacy answer and cancellation writes")
		sessionEpoch, threadEpoch := int64(43), int64(47)
		epochs := db.LifecycleRequestEpochState{AdmissionSessionEpoch: &sessionEpoch, AdmissionThreadEpoch: &threadEpoch}
		expectedRequests := make([]models.HumanInputRequest, 0, len(fixture.requests))
		for _, request := range fixture.requests {
			_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET admission_session_epoch=$4,admission_thread_epoch=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID, sessionEpoch, threadEpoch)
			require.NoError(t, err, "populate both passive epochs before exercising the unchanged legacy transition")
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "bound the legacy transition to a known PostgreSQL transaction timestamp")
			defer func() { _ = tx.Rollback(ctx) }()
			var answeredAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&answeredAt), "record the exact timestamp used by legacy answer persistence")
			answer := "Synthetic response"
			actual, err := action.write(db.NewSessionHumanInputRequestStore(tx), request, &answer, actor)
			require.NoError(t, err, action.name+" must accept passive metadata on both threaded and threadless requests")
			expected := request
			expected.Status = action.status
			expected.AnswerText = &answer
			expected.AnswerPayload = json.RawMessage(`{}`)
			expected.AnsweredBy = &actor
			expected.AnsweredAt = &answeredAt
			require.Equal(t, expected, actual, action.name+" must return the complete unchanged legacy transition result")
			require.NoError(t, tx.Commit(ctx), "commit the real legacy transition before checking durable passive metadata")
			requireRequestEpochLegacyProjection(t, ctx, conn, expected, epochs, action.name)

			var past time.Time
			require.NoError(t, conn.QueryRow(ctx, `SELECT '2020-01-01 00:00:00+00'::timestamptz`).Scan(&past), "decode a fixed expired timestamp with the same driver location used for stored requests")
			visibility := []struct {
				name   string
				status models.HumanInputRequestStatus
			}{
				{name: "expired pending request", status: models.HumanInputRequestStatusPending},
				{name: "expired terminal request", status: models.HumanInputRequestStatusExpired},
				{name: "superseded request", status: models.HumanInputRequestStatusSuperseded},
			}
			for _, state := range visibility {
				_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET status=$4,expires_at=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, expected.OrgID, expected.SessionID, expected.ID, state.status, past)
				require.NoError(t, err, "prepare synthetic "+state.name+" without modifying either passive epoch")
				expected.Status, expected.ExpiresAt = state.status, &past
				requireRequestEpochLegacyProjection(t, ctx, conn, expected, epochs, state.name)
			}
			expectedRequests = append(expectedRequests, expected)
		}
		_, err = db.NewSessionThreadStore(conn).Archive(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
		require.NoError(t, err, "archive the synthetic idle secondary thread through the existing store")
		for _, request := range expectedRequests {
			requireRequestEpochLegacyProjection(t, ctx, conn, request, epochs, "archived request thread")
		}
		archived, err := db.NewSessionStore(conn).ArchiveSystem(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
		require.NoError(t, err, "archive the synthetic parent through the existing system operation")
		require.True(t, archived, "the fresh synthetic session must enter archived state")
		for _, request := range expectedRequests {
			requireRequestEpochLegacyProjection(t, ctx, conn, request, epochs, "archived parent session")
		}
		require.NoError(t, db.NewSessionStore(conn).SoftDelete(ctx, fixture.epochs.orgID, fixture.epochs.sessionID), "soft delete the parent while retaining its requests for audit")
		for _, request := range expectedRequests {
			requireRequestEpochLegacyProjection(t, ctx, conn, request, epochs, "deleted parent session")
		}
	}
}

func requireRequestEpochLegacyProjection(t *testing.T, ctx context.Context, conn *pgx.Conn, expected models.HumanInputRequest, epochs db.LifecycleRequestEpochState, label string) {
	t.Helper()
	actual, err := db.NewSessionHumanInputRequestStore(conn).GetByID(ctx, expected.OrgID, expected.SessionID, expected.ID)
	require.NoError(t, err, label+" must remain visible through the legacy request accessor")
	require.Equal(t, expected, actual, label+" must preserve the complete expected legacy request model")
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err, "serialize the complete expected legacy request")
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err, "serialize the complete persisted legacy request")
	require.JSONEq(t, string(expectedJSON), string(actualJSON), label+" must preserve the full legacy API representation")
	for _, key := range []string{"admission_session_epoch", "admission_thread_epoch"} {
		require.NotContains(t, string(actualJSON), `"`+key+`"`, "passive metadata must not appear in the public request projection")
	}
	actualEpochs, err := db.NewLifecycleEpochStore(conn).GetRequest(ctx, expected.OrgID, expected.SessionID, expected.ID)
	require.NoError(t, err, label+" must not hide a passive request observation based on lifecycle projections")
	require.Equal(t, epochs, actualEpochs, label+" must leave both recorded epochs intact without inferring parent state")
}
