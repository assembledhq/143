package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// newExecutingCountPostgres provisions an isolated schema holding only the
// sessions columns CountExecutingByOrg reads, following
// newFleetCapacityPostgres. Unlike the full-migration proof below this runs
// on any PostgreSQL the developer has locally.
func newExecutingCountPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the executing count predicate proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "executing_count_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated test schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse pool settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE sessions (
			id                       uuid        PRIMARY KEY,
			org_id                   uuid        NOT NULL,
			status                   text        NOT NULL,
			runtime_hard_deadline_at timestamptz,
			deleted_at               timestamptz
		)`)
	require.NoError(t, err, "create sessions table")
	return pool
}

type executingCountRow struct {
	orgID       uuid.UUID
	status      models.SessionStatus
	hasDeadline bool
	deleted     bool
}

// TestSessionStore_CountExecutingByOrg_PostgresPredicates proves the count's
// predicates row by row: only live, running rows with an installed hard
// deadline in the caller's org count, and the excluded session is left out.
func TestSessionStore_CountExecutingByOrg_PostgresPredicates(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	otherOrgID := uuid.New()
	tests := []struct {
		name     string
		rows     []executingCountRow
		exclude  func(ids []uuid.UUID) uuid.UUID
		expected int
	}{
		{
			name: "running rows with a hard deadline count, queued and idle rows do not",
			rows: []executingCountRow{
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusRunning},
				{orgID: orgID, status: models.SessionStatusIdle, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusPending},
			},
			exclude:  func([]uuid.UUID) uuid.UUID { return uuid.Nil },
			expected: 2,
		},
		{
			name: "soft-deleted and other-org executing rows are skipped",
			rows: []executingCountRow{
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true, deleted: true},
				{orgID: otherOrgID, status: models.SessionStatusRunning, hasDeadline: true},
			},
			exclude:  func([]uuid.UUID) uuid.UUID { return uuid.Nil },
			expected: 1,
		},
		{
			name: "the excluded session is left out of the count",
			rows: []executingCountRow{
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
			},
			exclude:  func(ids []uuid.UUID) uuid.UUID { return ids[0] },
			expected: 1,
		},
		{
			name: "excluding a session that is not executing changes nothing",
			rows: []executingCountRow{
				{orgID: orgID, status: models.SessionStatusRunning, hasDeadline: true},
				{orgID: orgID, status: models.SessionStatusRunning},
			},
			exclude:  func(ids []uuid.UUID) uuid.UUID { return ids[1] },
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := newExecutingCountPostgres(t)
			ctx := context.Background()
			ids := make([]uuid.UUID, 0, len(tt.rows))
			for _, row := range tt.rows {
				id := uuid.New()
				ids = append(ids, id)
				var deadline, deleted *time.Time
				now := time.Now().UTC()
				if row.hasDeadline {
					deadline = &now
				}
				if row.deleted {
					deleted = &now
				}
				_, err := pool.Exec(ctx, `INSERT INTO sessions (id, org_id, status, runtime_hard_deadline_at, deleted_at) VALUES ($1, $2, $3, $4, $5)`,
					id, row.orgID, string(row.status), deadline, deleted)
				require.NoError(t, err, "seed session row")
			}

			count, err := NewSessionStore(pool).CountExecutingByOrg(ctx, orgID, tt.exclude(ids))
			require.NoError(t, err, "CountExecutingByOrg should query the seeded rows")
			require.Equal(t, tt.expected, count, "only live running rows with an installed hard deadline, other than the excluded one, should count")
		})
	}
}

// seedExecutingOrg creates a tenant for the executing-turn marker proof.
func seedExecutingOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	orgID := uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, orgID, "executing-test-"+orgID.String())
	require.NoError(t, err, "seed executing-turn tenant")
	return orgID
}

// newExecutingSession creates an idle session the way the API leaves one
// between turns.
func newExecutingSession(t *testing.T, store *SessionStore, orgID uuid.UUID) *models.Session {
	t.Helper()
	session := &models.Session{
		OrgID:            orgID,
		AgentType:        models.DefaultDefaultAgentType,
		Status:           models.SessionStatusIdle,
		AutonomyLevel:    models.DefaultSessionAutonomy,
		TokenMode:        models.SessionTokenModeLow,
		Origin:           models.SessionOriginManual,
		InteractionMode:  models.SessionInteractionModeInteractive,
		ValidationPolicy: models.SessionValidationPolicyOnSessionEnd,
	}
	require.NoError(t, store.Create(context.Background(), session), "create idle session")
	return session
}

// beginExecutingTurn claims the idle session as the API does and then runs
// BeginRuntime as the orchestrator does once the turn is admitted.
func beginExecutingTurn(t *testing.T, store *SessionStore, orgID, sessionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := store.ClaimIdle(ctx, orgID, sessionID)
	require.NoError(t, err, "claim idle session for a continuation")
	now := time.Now().UTC()
	require.NoError(t, store.BeginRuntime(ctx, orgID, sessionID, models.CheckpointCapabilityFullResume, now.Add(10*time.Minute), now.Add(30*time.Minute), now), "begin runtime for the admitted turn")
}

// TestSessionStore_CountExecutingByOrg_Postgres proves the executing-turn
// marker against the full schema: only a session that reached BeginRuntime
// and has not since completed, settled, been claimed again, or been deleted
// counts against the org quota, and the caller's own session can be excluded.
// pgxmock cannot prove this: the marker is the joint effect of several
// writers on one row. Every case owns a fresh org so the cases run in
// parallel against one migrated database.
func TestSessionStore_CountExecutingByOrg_Postgres(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := newFeedbackLifecyclePool(t)
	store := NewSessionStore(pool)

	// Each case leaves one session in a distinct lifecycle state inside its
	// own org; counted says whether CountExecutingByOrg must include it.
	tests := []struct {
		name string
		// arrange drives the case's session (created idle in orgID) into its
		// lifecycle state; it may use otherOrgID for a second tenant.
		arrange func(t *testing.T, orgID, otherOrgID, sessionID uuid.UUID)
		counted bool
	}{
		{
			name: "a turn past BeginRuntime is executing",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				beginExecutingTurn(t, store, orgID, sessionID)
			},
			counted: true,
		},
		{
			name: "a claimed continuation that has not reached BeginRuntime is queued, not executing",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				_, err := store.ClaimIdle(ctx, orgID, sessionID)
				require.NoError(t, err, "claim idle session")
			},
		},
		{
			name: "ClaimIdle clears deadlines a pre-existing idle row kept from its last turn",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				_, err := pool.Exec(ctx, `UPDATE sessions SET runtime_soft_deadline_at = now(), runtime_hard_deadline_at = now() WHERE id = $1 AND org_id = $2`, sessionID, orgID)
				require.NoError(t, err, "simulate a row written before deadlines were cleared at turn end")
				claimed, err := store.ClaimIdle(ctx, orgID, sessionID)
				require.NoError(t, err, "claim idle session with stale deadlines")
				require.Nil(t, claimed.RuntimeHardDeadlineAt, "ClaimIdle should clear the stale hard deadline")
				require.Nil(t, claimed.RuntimeSoftDeadlineAt, "ClaimIdle should clear the stale soft deadline")
			},
		},
		{
			name: "a completed turn returns the session to idle without the marker",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				beginExecutingTurn(t, store, orgID, sessionID)
				require.NoError(t, store.UpdateTurnComplete(ctx, orgID, sessionID, 1, &models.SessionResult{}, "agent-session", "snapshot-key"), "complete the turn")
				session, err := store.GetByID(ctx, orgID, sessionID)
				require.NoError(t, err, "reload completed session")
				require.Equal(t, models.SessionStatusIdle, session.Status, "turn completion should leave the session idle")
				require.Nil(t, session.RuntimeHardDeadlineAt, "turn completion should clear the hard deadline")
			},
		},
		{
			name: "a turn settled through UpdateStatus (the reaper's path) stops executing",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				beginExecutingTurn(t, store, orgID, sessionID)
				require.NoError(t, store.UpdateStatus(ctx, orgID, sessionID, models.SessionStatusFailed), "settle the session as failed")
				session, err := store.GetByID(ctx, orgID, sessionID)
				require.NoError(t, err, "reload failed session")
				require.Nil(t, session.RuntimeHardDeadlineAt, "settling should clear the hard deadline")
			},
		},
		{
			name: "a turn settled through UpdateResult stops executing",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				beginExecutingTurn(t, store, orgID, sessionID)
				errMsg := "cancelled by user"
				require.NoError(t, store.UpdateResult(ctx, orgID, sessionID, models.SessionStatusCancelled, &models.SessionResult{Error: &errMsg}), "settle the session as cancelled")
				session, err := store.GetByID(ctx, orgID, sessionID)
				require.NoError(t, err, "reload cancelled session")
				require.Nil(t, session.RuntimeHardDeadlineAt, "a terminal result should clear the hard deadline")
			},
		},
		{
			name: "a soft-deleted executing session no longer holds a slot",
			arrange: func(t *testing.T, orgID, _ uuid.UUID, sessionID uuid.UUID) {
				beginExecutingTurn(t, store, orgID, sessionID)
				require.NoError(t, store.SoftDelete(ctx, orgID, sessionID), "soft-delete the executing session")
			},
		},
		{
			name: "another org's executing turn is not counted",
			arrange: func(t *testing.T, _, otherOrgID uuid.UUID, _ uuid.UUID) {
				other := newExecutingSession(t, store, otherOrgID)
				beginExecutingTurn(t, store, otherOrgID, other.ID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := seedExecutingOrg(t, pool)
			otherOrgID := seedExecutingOrg(t, pool)
			session := newExecutingSession(t, store, orgID)
			tt.arrange(t, orgID, otherOrgID, session.ID)

			expected := 0
			if tt.counted {
				expected = 1
			}
			count, err := store.CountExecutingByOrg(ctx, orgID, uuid.Nil)
			require.NoError(t, err, "count executing sessions after arranging the case")
			require.Equal(t, expected, count, "only a session past BeginRuntime and not yet settled, re-claimed, or deleted should count")
			if !tt.counted {
				return
			}
			count, err = store.CountExecutingByOrg(ctx, orgID, session.ID)
			require.NoError(t, err, "count executing sessions excluding the executing one")
			require.Equal(t, 0, count, "excluding the executing session should leave nothing counted, so its own thread turns are not held by its slot")
			count, err = store.CountExecutingByOrg(ctx, orgID, uuid.New())
			require.NoError(t, err, "count executing sessions excluding an unrelated id")
			require.Equal(t, 1, count, "excluding an unrelated session should not change the count")
		})
	}
}
