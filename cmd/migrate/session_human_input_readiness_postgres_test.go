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

func TestSessionHumanInputReadinessMigrationPostgres(t *testing.T) {
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
				require.NoError(t, migration.Migrate(324), "construct the preceding full schema including passive request epochs")
				fixture = seedRequestReadinessMigrationFixture(t, ctx, conn)
				before = requestReadinessMigrationLegacyRows(t, ctx, conn, fixture)
				requireRequestReadinessMigrationBoundary(t, ctx, conn, migration, fixture, before, 324)
			}
			require.NoError(t, migration.Migrate(325), "apply the real migration chain through nullable readiness representation")
			if !tt.upgrade {
				fixture = seedRequestReadinessMigrationFixture(t, ctx, conn)
				before = requestReadinessMigrationLegacyRows(t, ctx, conn, fixture)
			}
			requireRequestReadinessMigrationBoundary(t, ctx, conn, migration, fixture, before, 325)
			requireRequestReadinessMigrationUnknown(t, ctx, conn, fixture)
			newFixture := seedRequestReadinessMigrationFixture(t, ctx, conn)
			requireRequestReadinessMigrationUnknown(t, ctx, conn, newFixture)
			requireRequestReadinessMigrationScope(t, ctx, conn, fixture, newFixture)
			requireRequestReadinessMigrationRoundTrip(t, ctx, conn, fixture)
			requireRequestReadinessLegacyTransitions(t, ctx, conn)

			// Assertions and migrations share one isolated connection and run
			// sequentially; the independent database fixtures run in parallel.
			beforeRollback := requestReadinessMigrationLegacyRows(t, ctx, conn, fixture)
			require.JSONEq(t, string(before), string(beforeRollback), "passive metadata writes and observations must preserve every legacy field and previously installed request epoch")
			require.NoError(t, migration.Steps(-1), "remove only readiness columns after their metadata reader has been rolled back")
			requireRequestReadinessMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 324)
			require.NoError(t, migration.Migrate(325), "reapply readiness slots without inventing historical classifications or proofs")
			requireRequestReadinessMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 325)
			requireRequestReadinessMigrationUnknown(t, ctx, conn, fixture)
			requireRequestReadinessMigrationUnknown(t, ctx, conn, newFixture)
		})
	}
}

func seedRequestReadinessMigrationFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) requestEpochMigrationFixture {
	t.Helper()
	fixture := seedRequestEpochMigrationFixture(t, ctx, conn)
	// Preserve independently populated migration-324 epochs and an already
	// expired request; neither parent epochs nor expires_at imply readiness.
	for i, request := range fixture.requests {
		_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET admission_session_epoch=43,admission_thread_epoch=47,expires_at='2020-01-01 00:00:00+00' WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "seed existing passive epochs and expiry independently of future readiness metadata")
		stored, err := db.NewSessionHumanInputRequestStore(conn).GetByID(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "capture the complete legacy threaded or threadless request before migration assertions")
		fixture.requests[i] = stored
	}
	return fixture
}

func requestReadinessMigrationLegacyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
		'requests',(SELECT jsonb_agg(to_jsonb(r)-'legacy_request'-'proof_policy_version'-'enforcement_version'-'classification_version'-'readiness_state'-'readiness_deadline_at'-'readiness_proof' ORDER BY r.id) FROM session_human_input_requests r WHERE r.org_id=$1 AND r.session_id=$2))`, fixture.epochs.orgID, fixture.epochs.sessionID).Scan(&value)
	require.NoError(t, err, "capture every legacy request and parent field including both existing admission epochs")
	return value
}

func requireRequestReadinessMigrationBoundary(t *testing.T, ctx context.Context, conn *pgx.Conn, migration *migrate.Migrate, fixture requestEpochMigrationFixture, before json.RawMessage, version uint) {
	t.Helper()
	actualVersion, dirty, err := migration.Version()
	require.NoError(t, err, "read the exact readiness migration ledger boundary")
	require.Equal(t, version, actualVersion, "readiness schema transitions must reach their selected migration version")
	require.False(t, dirty, "readiness upgrades and rollbacks must leave the migration ledger clean")
	require.JSONEq(t, string(before), string(requestReadinessMigrationLegacyRows(t, ctx, conn, fixture)), "upgrade rollback and reapply must preserve every legacy field and the previously installed admission epochs")
	type column struct {
		Name     string  `db:"column_name"`
		DataType string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	want := []column{}
	if version == 325 {
		want = []column{
			{Name: "classification_version", DataType: "integer", Nullable: "YES"},
			{Name: "enforcement_version", DataType: "integer", Nullable: "YES"},
			{Name: "legacy_request", DataType: "boolean", Nullable: "YES"},
			{Name: "proof_policy_version", DataType: "integer", Nullable: "YES"},
			{Name: "readiness_deadline_at", DataType: "timestamp with time zone", Nullable: "YES"},
			{Name: "readiness_proof", DataType: "jsonb", Nullable: "YES"},
			{Name: "readiness_state", DataType: "text", Nullable: "YES"},
		}
	}
	rows, err := conn.Query(ctx, `SELECT column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_human_input_requests' AND column_name IN ('legacy_request','proof_policy_version','enforcement_version','classification_version','readiness_state','readiness_deadline_at','readiness_proof') ORDER BY column_name`)
	require.NoError(t, err, "inspect the real PostgreSQL readiness column definitions")
	got, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect all seven readiness field types nullability and defaults")
	require.Equal(t, want, got, "readiness metadata must have exactly the seven nullable default-free scalar representations")
	for _, request := range fixture.requests {
		actual, err := db.NewSessionHumanInputRequestStore(conn).GetByID(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "legacy request reads must work at every readiness migration boundary")
		require.Equal(t, request, actual, "passive readiness schema steps must not alter any public request field")
	}
}

func requireRequestReadinessMigrationUnknown(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) {
	t.Helper()
	for _, request := range fixture.requests {
		actual, err := db.NewSessionHumanInputRequestStore(conn).GetLifecycleReadiness(ctx, request.OrgID, request.SessionID, request.ID)
		require.NoError(t, err, "read passive metadata on default-created threaded and threadless requests")
		require.Equal(t, db.HumanInputReadinessMetadata{}, actual, "creation must not infer legacy classification versions readiness deadlines or proofs from request or parent state")
	}
}

func requireRequestReadinessMigrationScope(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture, other requestEpochMigrationFixture) {
	t.Helper()
	request := fixture.requests[0]
	tests := []struct {
		name                        string
		orgID, sessionID, requestID uuid.UUID
	}{
		{name: "foreign organization", orgID: other.epochs.orgID, sessionID: request.SessionID, requestID: request.ID},
		{name: "wrong parent session", orgID: request.OrgID, sessionID: other.epochs.sessionID, requestID: request.ID},
		{name: "foreign request", orgID: request.OrgID, sessionID: request.SessionID, requestID: other.requests[0].ID},
		{name: "missing request", orgID: request.OrgID, sessionID: request.SessionID, requestID: uuid.New()},
	}
	// These cases share one connection and deliberately run sequentially.
	for _, tt := range tests {
		actual, err := db.NewSessionHumanInputRequestStore(conn).GetLifecycleReadiness(ctx, tt.orgID, tt.sessionID, tt.requestID)
		require.ErrorIs(t, err, pgx.ErrNoRows, tt.name+" must not expose metadata outside the exact request scope")
		require.Equal(t, db.HumanInputReadinessMetadata{}, actual, tt.name+" must not return partially populated readiness metadata")
	}
}

func requireRequestReadinessMigrationRoundTrip(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture requestEpochMigrationFixture) {
	t.Helper()
	var deadline time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT '2030-02-03 04:05:06.123456+00'::timestamptz`).Scan(&deadline), "decode a fixed synthetic readiness deadline using the PostgreSQL driver")
	tests := []struct {
		name string
		want db.HumanInputReadinessMetadata
	}{
		{name: "all unknown"},
		{name: "present false", want: db.HumanInputReadinessMetadata{LegacyRequest: requestReadinessMigrationPtr(false)}},
		{name: "present true", want: db.HumanInputReadinessMetadata{LegacyRequest: requestReadinessMigrationPtr(true)}},
		{name: "present proof version zero", want: db.HumanInputReadinessMetadata{ProofPolicyVersion: requestReadinessMigrationPtr(int32(0))}},
		{name: "present enforcement version zero", want: db.HumanInputReadinessMetadata{EnforcementVersion: requestReadinessMigrationPtr(int32(0))}},
		{name: "present classification version zero", want: db.HumanInputReadinessMetadata{ClassificationVersion: requestReadinessMigrationPtr(int32(0))}},
		{name: "complete signed integer precision", want: db.HumanInputReadinessMetadata{ProofPolicyVersion: requestReadinessMigrationPtr(int32(math.MaxInt32)), EnforcementVersion: requestReadinessMigrationPtr(int32(math.MinInt32)), ClassificationVersion: requestReadinessMigrationPtr(int32(123456789))}},
		{name: "unclassified state", want: db.HumanInputReadinessMetadata{ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessStateUnclassified)}},
		{name: "ready state", want: db.HumanInputReadinessMetadata{ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessStateReady)}},
		{name: "waiting state", want: db.HumanInputReadinessMetadata{ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessStateWaiting)}},
		{name: "recovery required state", want: db.HumanInputReadinessMetadata{ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessStateRecoveryRequired)}},
		{name: "future state is an observation", want: db.HumanInputReadinessMetadata{ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessState("future_unrecognized_state"))}},
		{name: "independent readiness deadline", want: db.HumanInputReadinessMetadata{ReadinessDeadlineAt: &deadline}},
		{name: "JSON null is not SQL NULL", want: db.HumanInputReadinessMetadata{ReadinessProof: json.RawMessage(`null`)}},
		{name: "synthetic opaque proof", want: db.HumanInputReadinessMetadata{ReadinessProof: json.RawMessage(`{"synthetic_receipt":"example","version":7}`)}},
		{name: "complete metadata", want: completeRequestReadinessMigrationMetadata(deadline)},
		{name: "all unknown restored"},
	}
	// Cases deliberately reuse one isolated connection to prove independent
	// NULL transitions; database fixtures are parallel rather than these writes.
	for _, request := range fixture.requests {
		for _, tt := range tests {
			want := tt.want
			if want.ReadinessProof != nil {
				var canonical json.RawMessage
				require.NoError(t, conn.QueryRow(ctx, `SELECT $1::jsonb`, want.ReadinessProof).Scan(&canonical), "canonicalize the expected synthetic JSON without treating JSON null as SQL NULL")
				require.NotNil(t, canonical, tt.name+" must remain present JSON rather than collapse into SQL NULL")
				require.JSONEq(t, string(want.ReadinessProof), string(canonical), tt.name+" must preserve the original expected JSON value during canonicalization")
				want.ReadinessProof = canonical
			}
			installRequestReadinessMigrationMetadata(t, ctx, conn, request, want)
			actual, err := db.NewSessionHumanInputRequestStore(conn).GetLifecycleReadiness(ctx, request.OrgID, request.SessionID, request.ID)
			require.NoError(t, err, tt.name+" must round-trip through the actual PostgreSQL codec")
			require.Equal(t, want, actual, tt.name+" must preserve every scalar pointer and JSON representation without interpretation")
			requireRequestReadinessLegacyProjection(t, ctx, conn, request, want, tt.name)
		}
	}
}

func requestReadinessMigrationPtr[T any](value T) *T { return &value }

func completeRequestReadinessMigrationMetadata(deadline time.Time) db.HumanInputReadinessMetadata {
	return db.HumanInputReadinessMetadata{
		LegacyRequest: requestReadinessMigrationPtr(false), ProofPolicyVersion: requestReadinessMigrationPtr(int32(7)),
		EnforcementVersion: requestReadinessMigrationPtr(int32(11)), ClassificationVersion: requestReadinessMigrationPtr(int32(13)),
		ReadinessState: requestReadinessMigrationPtr(models.HumanInputReadinessStateWaiting), ReadinessDeadlineAt: &deadline,
		ReadinessProof: json.RawMessage(`{"version": 7, "synthetic_receipt": "example"}`),
	}
}

func installRequestReadinessMigrationMetadata(t *testing.T, ctx context.Context, conn *pgx.Conn, request models.HumanInputRequest, value db.HumanInputReadinessMetadata) {
	t.Helper()
	_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET legacy_request=$4,proof_policy_version=$5,enforcement_version=$6,classification_version=$7,readiness_state=$8,readiness_deadline_at=$9,readiness_proof=$10 WHERE org_id=$1 AND session_id=$2 AND id=$3`, request.OrgID, request.SessionID, request.ID, value.LegacyRequest, value.ProofPolicyVersion, value.EnforcementVersion, value.ClassificationVersion, value.ReadinessState, value.ReadinessDeadlineAt, value.ReadinessProof)
	require.NoError(t, err, "install synthetic passive metadata without classifying answering or claiming the request")
}

func requireRequestReadinessLegacyTransitions(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	actions := []struct {
		name   string
		status models.HumanInputRequestStatus
		write  func(*db.SessionHumanInputRequestStore, models.HumanInputRequest, *string, uuid.UUID) (models.HumanInputRequest, error)
	}{
		{name: "legacy answer", status: models.HumanInputRequestStatusAnswered, write: func(store *db.SessionHumanInputRequestStore, request models.HumanInputRequest, answer *string, actor uuid.UUID) (models.HumanInputRequest, error) {
			return store.AnswerPending(ctx, request.OrgID, request.SessionID, request.ID, answer, json.RawMessage(`{}`), actor)
		}},
		{name: "legacy cancellation", status: models.HumanInputRequestStatusCancelled, write: func(store *db.SessionHumanInputRequestStore, request models.HumanInputRequest, answer *string, actor uuid.UUID) (models.HumanInputRequest, error) {
			return store.CancelPending(ctx, request.OrgID, request.SessionID, request.ID, answer, json.RawMessage(`{}`), actor)
		}},
	}
	// Transitions and visibility changes share one connection. Each action
	// creates fresh requests and executes sequentially within its own database.
	for _, action := range actions {
		fixture := seedRequestReadinessMigrationFixture(t, ctx, conn)
		actor := uuid.New()
		_, err := conn.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Synthetic readiness actor')`, actor, fixture.epochs.orgID, actor.String()+"@example.invalid")
		require.NoError(t, err, "create a synthetic actor for unchanged legacy answer and cancel operations")
		var deadline time.Time
		require.NoError(t, conn.QueryRow(ctx, `SELECT '2030-02-03 04:05:06.123456+00'::timestamptz`).Scan(&deadline), "decode the exact synthetic deadline installed before legacy transitions")
		metadata := completeRequestReadinessMigrationMetadata(deadline)
		expectedRequests := make([]models.HumanInputRequest, 0, len(fixture.requests))
		for _, request := range fixture.requests {
			installRequestReadinessMigrationMetadata(t, ctx, conn, request, metadata)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "bound the legacy transition to one known PostgreSQL timestamp")
			defer func() { _ = tx.Rollback(ctx) }()
			var answeredAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&answeredAt), "capture the exact timestamp used by the real answer or cancellation")
			answer := "Synthetic response"
			actual, err := action.write(db.NewSessionHumanInputRequestStore(tx), request, &answer, actor)
			require.NoError(t, err, action.name+" must remain accepted without interpreting installed readiness metadata")
			expected := request
			expected.Status, expected.AnswerText, expected.AnswerPayload = action.status, &answer, json.RawMessage(`{}`)
			expected.AnsweredBy, expected.AnsweredAt = &actor, &answeredAt
			require.Equal(t, expected, actual, action.name+" must return its complete unchanged legacy result")
			require.NoError(t, tx.Commit(ctx), "commit the legacy transition before checking durable metadata")
			requireRequestReadinessLegacyProjection(t, ctx, conn, expected, metadata, action.name)
			visibility := []struct {
				name   string
				status models.HumanInputRequestStatus
			}{
				{name: "expired pending request", status: models.HumanInputRequestStatusPending},
				{name: "expired terminal request", status: models.HumanInputRequestStatusExpired},
				{name: "superseded request", status: models.HumanInputRequestStatusSuperseded},
			}
			for _, state := range visibility {
				_, err := conn.Exec(ctx, `UPDATE session_human_input_requests SET status=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, expected.OrgID, expected.SessionID, expected.ID, state.status)
				require.NoError(t, err, "prepare "+state.name+" without touching installed readiness fields or request expiry")
				expected.Status = state.status
				requireRequestReadinessLegacyProjection(t, ctx, conn, expected, metadata, state.name)
			}
			expectedRequests = append(expectedRequests, expected)
		}
		_, err = db.NewSessionThreadStore(conn).Archive(ctx, fixture.epochs.orgID, fixture.epochs.sessionID, fixture.epochs.threadID)
		require.NoError(t, err, "archive the synthetic idle secondary thread using the legacy store")
		for _, request := range expectedRequests {
			requireRequestReadinessLegacyProjection(t, ctx, conn, request, metadata, "archived request thread")
		}
		archived, err := db.NewSessionStore(conn).ArchiveSystem(ctx, fixture.epochs.orgID, fixture.epochs.sessionID)
		require.NoError(t, err, "archive the synthetic parent using the existing system operation")
		require.True(t, archived, "the synthetic parent must enter archived state before the visibility check")
		for _, request := range expectedRequests {
			requireRequestReadinessLegacyProjection(t, ctx, conn, request, metadata, "archived parent session")
		}
		require.NoError(t, db.NewSessionStore(conn).SoftDelete(ctx, fixture.epochs.orgID, fixture.epochs.sessionID), "soft delete the parent while retaining requests for audit")
		for _, request := range expectedRequests {
			requireRequestReadinessLegacyProjection(t, ctx, conn, request, metadata, "deleted parent session")
		}
	}
}

func requireRequestReadinessLegacyProjection(t *testing.T, ctx context.Context, conn *pgx.Conn, expected models.HumanInputRequest, metadata db.HumanInputReadinessMetadata, label string) {
	t.Helper()
	sessionEpoch, threadEpoch := int64(43), int64(47)
	requireRequestEpochLegacyProjection(t, ctx, conn, expected, db.LifecycleRequestEpochState{AdmissionSessionEpoch: &sessionEpoch, AdmissionThreadEpoch: &threadEpoch}, label)
	actual, err := db.NewSessionHumanInputRequestStore(conn).GetLifecycleReadiness(ctx, expected.OrgID, expected.SessionID, expected.ID)
	require.NoError(t, err, label+" must remain observable without filtering legacy status expiry or parent visibility")
	require.Equal(t, metadata, actual, label+" must preserve all installed passive readiness metadata without interpretation")
	publicJSON, err := json.Marshal(expected)
	require.NoError(t, err, "serialize the complete public request projection after passive readiness changes")
	for _, key := range []string{"legacy_request", "proof_policy_version", "enforcement_version", "classification_version", "readiness_state", "readiness_deadline_at", "readiness_proof"} {
		require.NotContains(t, string(publicJSON), `"`+key+`"`, "passive readiness metadata must not expand the public API representation")
	}
}
