package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSessionLifecycleEpochMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "legacy rows upgrade rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var fixture epochMigrationFixture
			var before json.RawMessage
			if tt.upgrade {
				require.NoError(t, migration.Migrate(321), "construct the full legacy schema before introducing passive epochs")
				fixture = seedEpochMigrationFixture(t, ctx, conn)
				before = epochMigrationLegacyRows(t, ctx, conn, fixture)
				requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, 321, 0, 0)
			}
			require.NoError(t, migration.Migrate(322), "commit session columns before taking the thread table's DDL lock")
			if !tt.upgrade {
				fixture = seedEpochMigrationFixture(t, ctx, conn)
				before = epochMigrationLegacyRows(t, ctx, conn, fixture)
			}
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, 322, 3, 0)
			session, err := db.NewLifecycleEpochStore(conn).GetSession(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "session metadata is readable before the separate thread migration")
			require.Equal(t, db.LifecycleEpochState{}, session, "the session-only boundary preserves legacy defaults")
			require.NoError(t, migration.Migrate(323), "add thread columns in a separate migration transaction")
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, 323, 3, 3)
			requireEpochMigrationState(t, ctx, conn, fixture, db.LifecycleEpochState{}, db.LifecycleEpochState{})

			// These checks share this isolated fixture: changing or rolling back
			// its schema cannot run concurrently with the fixture's store calls.
			newFixture := seedEpochMigrationFixture(t, ctx, conn)
			requireEpochMigrationState(t, ctx, conn, newFixture, db.LifecycleEpochState{}, db.LifecycleEpochState{})
			requireEpochMigrationColumns(t, ctx, conn)
			requireEpochMigrationIsolation(t, ctx, conn, fixture, newFixture)
			requireEpochMigrationRoundTrip(t, ctx, conn, fixture)
			requireEpochMigrationNullability(t, ctx, conn, fixture)
			beforeRollback := epochMigrationLegacyRows(t, ctx, conn, fixture)
			sessionBeforeRollback, err := db.NewLifecycleEpochStore(conn).GetSession(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "record session metadata before independently rolling back thread columns")
			require.NoError(t, migration.Steps(-1), "remove only thread columns after rolling back their readers")
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 322, 3, 0)
			session, err = db.NewLifecycleEpochStore(conn).GetSession(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "session metadata remains readable after the thread-only rollback")
			require.Equal(t, sessionBeforeRollback, session, "thread rollback must not remove or reset session metadata")
			require.NoError(t, migration.Steps(-1), "remove session columns in their own migration transaction")
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 321, 0, 0)
			_, err = db.NewSessionStore(conn).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "legacy session reads must continue to work after schema rollback")
			_, err = db.NewSessionThreadStore(conn).GetByID(ctx, fixture.orgID, fixture.threadID)
			require.NoError(t, err, "legacy thread reads must continue to work after schema rollback")
			require.NoError(t, migration.Migrate(322), "reapply only session metadata without changing legacy data")
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 322, 3, 0)
			require.NoError(t, migration.Migrate(323), "reapply thread metadata in a separate transaction")
			requireEpochMigrationBoundary(t, ctx, conn, migration, fixture, beforeRollback, 323, 3, 3)
			requireEpochMigrationState(t, ctx, conn, fixture, db.LifecycleEpochState{}, db.LifecycleEpochState{})
		})
	}
}

type epochMigrationFixture struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	threadID  uuid.UUID
}

func seedEpochMigrationFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) epochMigrationFixture {
	t.Helper()
	orgID := uuid.New()
	_, err := conn.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,'synthetic epoch migration tenant')", orgID)
	require.NoError(t, err, "create a fresh synthetic organization for each legacy fixture")
	session := &models.Session{
		OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow,
		Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive,
		ValidationPolicy: models.SessionValidationPolicyOnSessionEnd,
	}
	require.NoError(t, db.NewSessionStore(conn).Create(ctx, session), "create a session through unchanged legacy SQL that omits epoch columns")
	thread := &models.SessionThread{
		OrgID: orgID, SessionID: session.ID, AgentType: models.DefaultDefaultAgentType,
		Label: "synthetic secondary lane", Status: models.ThreadStatusIdle,
	}
	require.NoError(t, db.NewSessionThreadStore(conn).Create(ctx, thread, 10), "create a secondary thread through unchanged legacy SQL that omits epoch columns")
	return epochMigrationFixture{orgID: orgID, sessionID: session.ID, threadID: thread.ID}
}

func epochMigrationLegacyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture epochMigrationFixture) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s)-'admission_epoch'-'end_epoch'-'lifecycle_version' FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',(SELECT jsonb_agg(to_jsonb(t)-'admission_epoch'-'end_epoch'-'lifecycle_version' ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
		'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM session_changesets c WHERE c.org_id=$1 AND c.session_id=$2))`, fixture.orgID, fixture.sessionID).Scan(&rows)
	require.NoError(t, err, "capture every legacy session, primary/secondary thread and changeset field independently of passive columns")
	return rows
}

func requireEpochMigrationBoundary(t *testing.T, ctx context.Context, conn *pgx.Conn, migration *migrate.Migrate, fixture epochMigrationFixture, before json.RawMessage, version uint, sessionColumns, threadColumns int) {
	t.Helper()
	actualVersion, dirty, err := migration.Version()
	require.NoError(t, err, "read the real migration ledger at each upgrade and rollback boundary")
	require.Equal(t, version, actualVersion, "the selected schema boundary must match its exact migration version")
	require.False(t, dirty, "successful transitions must leave the migration ledger clean")
	require.JSONEq(t, string(before), string(epochMigrationLegacyRows(t, ctx, conn, fixture)), "schema transitions must preserve every preexisting field and linked row")
	var counts [2]int
	err = conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE table_name='sessions'), count(*) FILTER (WHERE table_name='session_threads') FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('sessions','session_threads') AND column_name IN ('admission_epoch','end_epoch','lifecycle_version')`).Scan(&counts[0], &counts[1])
	require.NoError(t, err, "inspect the passive columns independently on each hot table")
	require.Equal(t, [2]int{sessionColumns, threadColumns}, counts, "each migration step must add or remove only its own table's passive columns")
}

func requireEpochMigrationColumns(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	type column struct {
		Table    string  `db:"table_name"`
		Name     string  `db:"column_name"`
		DataType string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	zero := "0"
	want := []column{
		{Table: "session_threads", Name: "admission_epoch", DataType: "bigint", Nullable: "NO", Default: &zero},
		{Table: "session_threads", Name: "end_epoch", DataType: "bigint", Nullable: "YES"},
		{Table: "session_threads", Name: "lifecycle_version", DataType: "smallint", Nullable: "NO", Default: &zero},
		{Table: "sessions", Name: "admission_epoch", DataType: "bigint", Nullable: "NO", Default: &zero},
		{Table: "sessions", Name: "end_epoch", DataType: "bigint", Nullable: "YES"},
		{Table: "sessions", Name: "lifecycle_version", DataType: "smallint", Nullable: "NO", Default: &zero},
	}
	rows, err := conn.Query(ctx, `SELECT table_name,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('sessions','session_threads') AND column_name IN ('admission_epoch','end_epoch','lifecycle_version') ORDER BY table_name,column_name`)
	require.NoError(t, err, "inspect the actual types, defaults and nullability installed by the migration")
	got, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect all six column definitions without reducing verification to a row count")
	require.Equal(t, want, got, "sessions and threads must have exactly the documented passive field representations")
}

func requireEpochMigrationState(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture epochMigrationFixture, sessionWant, threadWant db.LifecycleEpochState) {
	t.Helper()
	store := db.NewLifecycleEpochStore(conn)
	session, err := store.GetSession(ctx, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "read session epoch representation through the new tenant-scoped accessor")
	require.Equal(t, sessionWant, session, "session accessor must preserve all epoch/version values and nullable end state")
	thread, err := store.GetThread(ctx, fixture.orgID, fixture.sessionID, fixture.threadID)
	require.NoError(t, err, "read thread epoch representation through the new tenant- and parent-scoped accessor")
	require.Equal(t, threadWant, thread, "thread accessor must preserve its own independent epoch/version values")
}

func requireEpochMigrationIsolation(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture, other epochMigrationFixture) {
	t.Helper()
	store := db.NewLifecycleEpochStore(conn)
	tests := []struct {
		name string
		read func() (db.LifecycleEpochState, error)
	}{
		{name: "session rejects another tenant", read: func() (db.LifecycleEpochState, error) { return store.GetSession(ctx, other.orgID, fixture.sessionID) }},
		{name: "thread rejects another tenant", read: func() (db.LifecycleEpochState, error) {
			return store.GetThread(ctx, other.orgID, fixture.sessionID, fixture.threadID)
		}},
		{name: "thread rejects another parent", read: func() (db.LifecycleEpochState, error) {
			return store.GetThread(ctx, fixture.orgID, other.sessionID, fixture.threadID)
		}},
	}
	// The migration fixture has one connection; these scope checks are
	// intentionally sequential while their parent fixtures run in parallel.
	for _, tt := range tests {
		got, err := tt.read()
		require.ErrorIs(t, err, pgx.ErrNoRows, tt.name+" without exposing a foreign resource")
		require.Equal(t, db.LifecycleEpochState{}, got, tt.name+" with no partially populated state")
	}
}

func requireEpochMigrationRoundTrip(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture epochMigrationFixture) {
	t.Helper()
	sessions, threads := db.NewSessionStore(conn), db.NewSessionThreadStore(conn)
	beforeSession, err := sessions.GetByID(ctx, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "record the complete legacy session projection before passive metadata is populated")
	beforeThread, err := threads.GetByID(ctx, fixture.orgID, fixture.threadID)
	require.NoError(t, err, "record the complete legacy thread projection before passive metadata is populated")
	end := int64(1<<62 + 7)
	sessionWant := db.LifecycleEpochState{AdmissionEpoch: end + 1, EndEpoch: &end, LifecycleVersion: 7}
	threadWant := db.LifecycleEpochState{AdmissionEpoch: 11, LifecycleVersion: 3}
	_, err = conn.Exec(ctx, `UPDATE sessions SET admission_epoch=$3,end_epoch=$4,lifecycle_version=$5 WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID, sessionWant.AdmissionEpoch, sessionWant.EndEpoch, sessionWant.LifecycleVersion)
	require.NoError(t, err, "seed large synthetic future metadata without granting runtime authority")
	_, err = conn.Exec(ctx, `UPDATE session_threads SET admission_epoch=$4,end_epoch=$5,lifecycle_version=$6 WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.orgID, fixture.sessionID, fixture.threadID, threadWant.AdmissionEpoch, threadWant.EndEpoch, threadWant.LifecycleVersion)
	require.NoError(t, err, "seed independent thread metadata with a null end epoch")
	requireEpochMigrationState(t, ctx, conn, fixture, sessionWant, threadWant)
	afterSession, err := sessions.GetByID(ctx, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "legacy session reads must tolerate additive columns with nonzero values")
	require.Equal(t, beforeSession, afterSession, "passive fields must not change any existing session projection field")
	afterThread, err := threads.GetByID(ctx, fixture.orgID, fixture.threadID)
	require.NoError(t, err, "legacy thread reads must tolerate additive columns with nonzero values")
	require.Equal(t, beforeThread, afterThread, "passive fields must not change any existing thread projection field")
	sessionJSON, err := json.Marshal(afterSession)
	require.NoError(t, err, "serialize the unchanged public session representation")
	threadJSON, err := json.Marshal(afterThread)
	require.NoError(t, err, "serialize the unchanged public thread representation")
	for _, key := range []string{"admission_epoch", "end_epoch", "lifecycle_version"} {
		require.NotContains(t, string(sessionJSON), `"`+key+`"`, "passive DB metadata must not expand the session API contract")
		require.NotContains(t, string(threadJSON), `"`+key+`"`, "passive DB metadata must not expand the thread API contract")
	}
	require.NoError(t, sessions.UpdateStatus(ctx, fixture.orgID, fixture.sessionID, models.SessionStatusIdle), "legacy session status writes must remain accepted with passive future metadata")
	require.NoError(t, threads.UpdateStatus(ctx, fixture.orgID, fixture.threadID, models.ThreadStatusRunning), "legacy thread status writes must remain accepted with passive future metadata")
	requireEpochMigrationState(t, ctx, conn, fixture, sessionWant, threadWant)
	// Both nullable end states must round-trip independently of session status.
	_, err = conn.Exec(ctx, `UPDATE sessions SET end_epoch=NULL WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "restore a null end epoch without interpreting legacy status")
	sessionWant.EndEpoch = nil
	threadEnd := int64(9)
	_, err = conn.Exec(ctx, `UPDATE session_threads SET end_epoch=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.orgID, fixture.sessionID, fixture.threadID, threadEnd)
	require.NoError(t, err, "store a concrete thread end epoch independently of its running projection")
	threadWant.EndEpoch = &threadEnd
	requireEpochMigrationState(t, ctx, conn, fixture, sessionWant, threadWant)
}

func requireEpochMigrationNullability(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture epochMigrationFixture) {
	t.Helper()
	tests := []struct{ name, query string }{
		{name: "session admission epoch", query: `UPDATE sessions SET admission_epoch=NULL WHERE org_id=$1 AND id=$2`},
		{name: "session lifecycle version", query: `UPDATE sessions SET lifecycle_version=NULL WHERE org_id=$1 AND id=$2`},
		{name: "thread admission epoch", query: `UPDATE session_threads SET admission_epoch=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "thread lifecycle version", query: `UPDATE session_threads SET lifecycle_version=NULL WHERE org_id=$1 AND session_id=$2`},
	}
	// Failed autocommit statements must leave this isolated connection usable.
	for _, tt := range tests {
		_, err := conn.Exec(ctx, tt.query, fixture.orgID, fixture.sessionID)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, tt.name+" must reject null through the real schema")
		require.Equal(t, "23502", pgErr.Code, tt.name+" must fail specifically on its not-null contract")
	}
}
