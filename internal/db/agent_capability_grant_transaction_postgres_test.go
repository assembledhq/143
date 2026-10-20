package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type approvedGrantPostgresCase struct {
	name                  string
	linked, commit        bool
	duplicate, foreignOrg bool
	deleted               bool
}

type approvedGrantConcurrentCase struct {
	name                                                     string
	historical, prelock, wrapper, duplicate, missingMetadata bool
}

func approvedGrantPostgresCases() []approvedGrantPostgresCase {
	return []approvedGrantPostgresCase{
		{name: "standalone commit", commit: true},
		{name: "linked commit", linked: true, commit: true},
		{name: "linked caller rollback", linked: true},
		{name: "duplicate preserves both snapshots", linked: true, duplicate: true},
		{name: "foreign org cannot append or mirror", linked: true, foreignOrg: true},
		{name: "soft-deleted session cannot append or mirror", linked: true, deleted: true},
	}
}

// This executes the real persistence method and SQL on a minimal synthetic
// schema. It is not the full production schema, activation or consent proof.
func TestAgentCapabilityApprovedGrantSyntheticPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range approvedGrantPostgresCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, runID := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, `
				ALTER TABLE sessions ADD COLUMN deleted_at timestamptz, ADD COLUMN status text NOT NULL DEFAULT 'idle';
				ALTER TABLE automation_runs ADD COLUMN capability_snapshot jsonb NOT NULL DEFAULT '[]';
				CREATE TABLE session_execution_metadata (
					session_id uuid PRIMARY KEY REFERENCES sessions(id), org_id uuid NOT NULL REFERENCES organizations(id),
					capability_snapshot jsonb NOT NULL DEFAULT '[]', updated_at timestamptz NOT NULL DEFAULT now());
				CREATE TABLE session_automation_links (
					session_id uuid PRIMARY KEY REFERENCES sessions(id), org_id uuid NOT NULL REFERENCES organizations(id),
					automation_run_id uuid NOT NULL REFERENCES automation_runs(id));
			`)
			require.NoError(t, err, "add only the columns and relations needed to run the production grant SQL on an isolated synthetic schema")
			_, err = pool.Exec(ctx, "INSERT INTO automation_runs(id,org_id,session_id) VALUES($1,$2,$3)", runID, orgID, sessionID)
			require.NoError(t, err, "seed a synthetic linked run without claiming full-schema validity")
			runApprovedGrantPostgresCase(t, pool, orgID, sessionID, runID, tt)
		})
	}
}

func TestAgentCapabilityApprovedGrantFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema approved-grant composition")
	}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	conn, err := pgx.Connect(versionCtx, raw)
	require.NoError(t, err, "connect only to the explicitly configured disposable test server for its version prerequisite")
	var version int
	err = conn.QueryRow(versionCtx, "SELECT current_setting('server_version_num')::integer").Scan(&version)
	closeErr := conn.Close(versionCtx)
	require.NoError(t, err, "read server version before the unmodified full migration chain")
	require.NoError(t, closeErr, "close the version prerequisite connection")
	if version < 150000 {
		t.Skip("the full migration chain requires PostgreSQL 15+; synthetic grant SQL is not full-schema proof")
	}
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range approvedGrantPostgresCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAutomationReaperFixture(t, pool)
			sessionID := fixture.session(t, string(models.SessionStatusIdle), nil, nil, nil)
			if !tt.linked {
				_, err := pool.Exec(context.Background(), "DELETE FROM session_automation_links WHERE org_id=$1 AND session_id=$2", fixture.orgID, sessionID)
				require.NoError(t, err, "prepare the standalone full-schema session without a linked automation snapshot")
			}
			runApprovedGrantPostgresCase(t, pool, fixture.orgID, sessionID, fixture.runID, tt)
		})
	}
	for _, tt := range []approvedGrantConcurrentCase{
		{name: "historical pre-fix joined SQL loses the earlier grant", historical: true},
		{name: "historical joined SQL with external prelock preserves both grants", historical: true, prelock: true},
		{name: "current InTx retains concurrent distinct grants"},
		{name: "current wrapper retains concurrent distinct grants", wrapper: true},
		{name: "current InTx detects the concurrently committed duplicate", duplicate: true},
		{name: "current wrapper detects the concurrently committed duplicate", wrapper: true, duplicate: true},
		{name: "current InTx retains both first grants without existing metadata", missingMetadata: true},
		{name: "current wrapper retains both first grants without existing metadata", wrapper: true, missingMetadata: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runApprovedGrantConcurrentFullSchemaCase(t, pool, tt)
		})
	}
	for _, tt := range []struct {
		name      string
		isolation pgx.TxIsoLevel
		wrapper   bool
	}{
		{name: "caller repeatable read rejected before writes", isolation: pgx.RepeatableRead},
		{name: "caller serializable rejected before writes", isolation: pgx.Serializable},
		{name: "caller read uncommitted rejected before writes", isolation: pgx.ReadUncommitted},
		{name: "wrapper repeatable read default rejected before writes", isolation: pgx.RepeatableRead, wrapper: true},
		{name: "wrapper serializable default rejected before writes", isolation: pgx.Serializable, wrapper: true},
		{name: "wrapper read uncommitted default rejected before writes", isolation: pgx.ReadUncommitted, wrapper: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runApprovedGrantUnsupportedIsolationPostgresCase(t, pool, tt.isolation, tt.wrapper)
		})
	}
}

func runApprovedGrantPostgresCase(t *testing.T, pool *pgxpool.Pool, orgID, sessionID, runID uuid.UUID, tt approvedGrantPostgresCase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := newApprovedGrantTestFixture()
	fixture.orgID, fixture.sessionID, fixture.runID = orgID, sessionID, runID
	before, err := json.Marshal(fixture.existing)
	require.NoError(t, err, "encode the complete original synthetic grant snapshot")
	_, err = pool.Exec(ctx, `INSERT INTO session_execution_metadata(session_id,org_id,capability_snapshot)
		VALUES($1,$2,$3::jsonb) ON CONFLICT(session_id) DO UPDATE SET capability_snapshot=EXCLUDED.capability_snapshot`, sessionID, orgID, string(before))
	require.NoError(t, err, "seed the session's exact original snapshot")
	_, err = pool.Exec(ctx, "UPDATE automation_runs SET capability_snapshot=$3::jsonb WHERE org_id=$1 AND id=$2", orgID, runID, string(before))
	require.NoError(t, err, "seed the run's independent original snapshot")
	if tt.linked {
		_, err = pool.Exec(ctx, `INSERT INTO session_automation_links(session_id,org_id,automation_run_id)
			VALUES($1,$2,$3) ON CONFLICT(session_id) DO UPDATE SET automation_run_id=EXCLUDED.automation_run_id`, sessionID, orgID, runID)
		require.NoError(t, err, "associate only the exact owned session and run")
	}
	var expectedDeletedAt *time.Time
	if tt.deleted {
		deletedAt := time.Date(2026, 1, 3, 1, 0, 0, 0, time.UTC)
		expectedDeletedAt = &deletedAt
		_, err = pool.Exec(ctx, "UPDATE sessions SET deleted_at=$3 WHERE org_id=$1 AND id=$2", orgID, sessionID, deletedAt)
		require.NoError(t, err, "soft-delete the owned session before the caller starts its transaction")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "the caller should own the grant's transaction boundary")
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, "UPDATE sessions SET status='failed' WHERE org_id=$1 AND id=$2", orgID, sessionID)
	require.NoError(t, err, "stage an earlier session change in the same caller-owned unit")
	requestedOrg := orgID
	if tt.foreignOrg {
		requestedOrg = uuid.New()
	}
	item := fixture.item
	if tt.duplicate {
		item = fixture.existing[0]
	}
	actual, err := NewAgentCapabilityPolicyStore(nil).AppendApprovedSessionGrantInTx(ctx, requestedOrg, tx, sessionID, item)
	want := append(append([]models.AgentCapabilitySnapshotItem{}, fixture.existing...), item)
	switch {
	case tt.foreignOrg:
		require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant must not read or update the owned session or linked run")
		require.Nil(t, actual, "foreign tenant must receive no capability snapshot")
	case tt.deleted:
		require.ErrorIs(t, err, pgx.ErrNoRows, "soft-deleted session must not append or mirror an approved grant")
		require.Nil(t, actual, "soft-deleted session must receive no capability snapshot")
	case tt.duplicate:
		require.ErrorIs(t, err, ErrCapabilityAlreadyGranted, "duplicate should preserve the legacy typed duplicate result")
		requireApprovedGrantJSON(t, fixture.existing, actual, "duplicate should return every original field without appending")
	default:
		require.NoError(t, err, "the real SQL should stage both owned snapshots without a store pool")
		requireApprovedGrantJSON(t, want, actual, "the staged result should retain all existing and new capability fields")
	}
	var stagedStatus string
	require.NoError(t, tx.QueryRow(ctx, "SELECT status FROM sessions WHERE org_id=$1 AND id=$2", orgID, sessionID).Scan(&stagedStatus), "the persistence method must leave the caller transaction open and usable")
	require.Equal(t, "failed", stagedStatus, "the method must not roll back the caller's earlier work")
	wantStatus := "idle"
	expectedSession, expectedRun := fixture.existing, fixture.existing
	if tt.commit {
		require.NoError(t, tx.Commit(ctx), "only the caller should publish the composed unit")
		wantStatus, expectedSession = "failed", want
		if tt.linked {
			expectedRun = want
		}
	} else {
		require.NoError(t, tx.Rollback(ctx), "the caller must be able to discard all prior and grant writes together")
	}
	var persistedSession, persistedRun []models.AgentCapabilitySnapshotItem
	var status string
	var deletedAt *time.Time
	err = pool.QueryRow(ctx, `SELECT s.status,s.deleted_at,sem.capability_snapshot,ar.capability_snapshot
		FROM sessions s JOIN session_execution_metadata sem ON sem.org_id=s.org_id AND sem.session_id=s.id
		JOIN automation_runs ar ON ar.org_id=s.org_id AND ar.id=$3
		WHERE s.org_id=$1 AND s.id=$2`, orgID, sessionID, runID).Scan(&status, &deletedAt, &persistedSession, &persistedRun)
	require.NoError(t, err, "inspect complete durable session and automation snapshots after owner settlement")
	require.Equal(t, wantStatus, status, "earlier caller work must share the grant's actual commit or rollback")
	if deletedAt != nil {
		utc := deletedAt.UTC()
		deletedAt = &utc
	}
	require.Equal(t, expectedDeletedAt, deletedAt, "grant rejection and caller rollback must preserve the exact prior deletion state")
	requireApprovedGrantJSON(t, expectedSession, persistedSession, "durable session snapshot must match the full expected commit outcome")
	requireApprovedGrantJSON(t, expectedRun, persistedRun, "only a linked committed run may mirror the full approved snapshot")
}

// Historical cases execute explicitly retained pre-fix SQL, not the repaired
// production method. They record why old deployed writers remain a floor/drain
// gate even when current wrappers and InTx callers preserve fresh snapshots.
func runApprovedGrantConcurrentFullSchemaCase(t *testing.T, database *pgxpool.Pool, tt approvedGrantConcurrentCase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config := database.Config()
	config.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "reserve independent owner, contender, and lock-observer connections")
	t.Cleanup(pool.Close)
	owned := newAutomationReaperFixture(t, pool)
	sessionID := owned.session(t, string(models.SessionStatusIdle), nil, nil, nil)
	fixture := newApprovedGrantTestFixture()
	if tt.missingMetadata {
		fixture.existing = []models.AgentCapabilitySnapshotItem{}
	}
	before, err := json.Marshal(fixture.existing)
	require.NoError(t, err, "encode the complete original concurrent grant snapshot")
	_, err = pool.Exec(ctx, `INSERT INTO session_execution_metadata(session_id,org_id,capability_snapshot)
		VALUES($1,$2,$3::jsonb) ON CONFLICT(session_id) DO UPDATE SET capability_snapshot=EXCLUDED.capability_snapshot`, sessionID, owned.orgID, string(before))
	require.NoError(t, err, "seed the isolated session snapshot before either concurrent grant")
	if tt.missingMetadata {
		_, err = pool.Exec(ctx, "DELETE FROM session_execution_metadata WHERE org_id=$1 AND session_id=$2", owned.orgID, sessionID)
		require.NoError(t, err, "exercise concurrent first grants without an existing execution metadata row")
	}
	_, err = pool.Exec(ctx, "UPDATE automation_runs SET capability_snapshot=$3::jsonb WHERE org_id=$1 AND id=$2", owned.orgID, owned.runID, string(before))
	require.NoError(t, err, "seed the isolated linked automation snapshot before either concurrent grant")
	owner, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin the earlier grant's caller-owned transaction")
	defer func() { _ = owner.Rollback(context.Background()) }()
	var lockedSession uuid.UUID
	err = owner.QueryRow(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", owned.orgID, sessionID).Scan(&lockedSession)
	require.NoError(t, err, "hold the exact session before staging the earlier grant")
	require.Equal(t, sessionID, lockedSession, "the earlier grant must lock only its owned session")
	ownerPID := owner.Conn().PgConn().PID()
	grantA := fixture.item
	wantA := append(append([]models.AgentCapabilitySnapshotItem{}, fixture.existing...), grantA)
	stagedA, err := NewAgentCapabilityPolicyStore(nil).AppendApprovedSessionGrantInTx(ctx, owned.orgID, owner, sessionID, grantA)
	require.NoError(t, err, "stage grant A while retaining the caller's session lock")
	requireApprovedGrantJSON(t, wantA, stagedA, "grant A should stage the complete first snapshot")
	contenderConfig := database.Config()
	contenderConfig.MaxConns, contenderConfig.MinConns, contenderConfig.MinIdleConns = 1, 0, 0
	contenderPool, err := pgxpool.NewWithConfig(ctx, contenderConfig)
	require.NoError(t, err, "reserve a separate one-connection pool for either the actual wrapper or the supplied transaction")
	defer contenderPool.Close()
	contenderConn, err := contenderPool.Acquire(ctx)
	require.NoError(t, err, "identify the contender's sole backend before its call begins")
	contenderPID := contenderConn.Conn().PgConn().PID()
	contenderConn.Release()
	grantB := fixture.item
	if !tt.duplicate {
		grantB.ID = models.AgentCapabilityPRHistory
		grantB.DisplayName = "Pull request history"
		requestB := uuid.New()
		grantB.HumanInputRequestID = &requestB
	}
	var beforeCommitSession, beforeCommitRun []models.AgentCapabilitySnapshotItem
	if !tt.missingMetadata {
		err = pool.QueryRow(ctx, `SELECT sem.capability_snapshot, ar.capability_snapshot
			FROM session_execution_metadata sem JOIN automation_runs ar ON ar.org_id=sem.org_id AND ar.id=$3
			WHERE sem.org_id=$1 AND sem.session_id=$2`, owned.orgID, sessionID, owned.runID).Scan(&beforeCommitSession, &beforeCommitRun)
		require.NoError(t, err, "inspect both snapshots from a separate connection before the owner commits")
		requireApprovedGrantJSON(t, fixture.existing, beforeCommitSession, "staged grant A must not publish its session snapshot before caller commit")
		requireApprovedGrantJSON(t, fixture.existing, beforeCommitRun, "staged grant A must not publish its automation snapshot before caller commit")
	}
	type result struct {
		snapshot []models.AgentCapabilitySnapshotItem
		err      error
	}
	started := make(chan struct{})
	finished := make(chan result, 1)
	contenderDone := make(chan struct{})
	go func() {
		defer close(contenderDone)
		close(started)
		if tt.wrapper {
			snapshot, err := NewAgentCapabilityPolicyStore(contenderPool).AppendApprovedSessionGrant(ctx, owned.orgID, sessionID, grantB)
			finished <- result{snapshot: snapshot, err: err}
			return
		}
		contender, err := contenderPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			finished <- result{err: err}
			return
		}
		defer func() { _ = contender.Rollback(context.Background()) }()
		if tt.prelock {
			var id uuid.UUID
			if err := contender.QueryRow(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", owned.orgID, sessionID).Scan(&id); err != nil {
				finished <- result{err: err}
				return
			}
		}
		var snapshot []models.AgentCapabilitySnapshotItem
		if tt.historical {
			snapshot, err = appendApprovedGrantHistoricalJoinedInTx(ctx, contender, owned.orgID, sessionID, grantB)
		} else {
			snapshot, err = NewAgentCapabilityPolicyStore(nil).AppendApprovedSessionGrantInTx(ctx, owned.orgID, contender, sessionID, grantB)
		}
		if err == nil {
			err = contender.Commit(ctx)
		}
		finished <- result{snapshot: snapshot, err: err}
	}()
	defer func() {
		cancel()
		<-contenderDone
	}()
	<-started
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid=$1 AND wait_event_type='Lock'
			AND $2=ANY(pg_blocking_pids(pid)))`, contenderPID, ownerPID).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond, "grant B must be observed waiting on grant A's exact backend before A commits")
	require.NoError(t, owner.Commit(ctx), "publish grant A only after PostgreSQL confirms grant B's lock wait")
	var actual result
	select {
	case actual = <-finished:
	case <-ctx.Done():
		t.Fatal("concurrent grant B must finish after grant A releases the session")
	}
	if tt.duplicate {
		require.ErrorIs(t, actual.err, ErrCapabilityAlreadyGranted, "the contender must see grant A's committed duplicate after its parent-lock wait")
	} else {
		require.NoError(t, actual.err, "grant B should settle its own transaction after the observed wait")
	}
	want := wantA
	if tt.historical && !tt.prelock {
		want = append(append([]models.AgentCapabilitySnapshotItem{}, fixture.existing...), grantB)
	} else if !tt.duplicate {
		want = append(append([]models.AgentCapabilitySnapshotItem{}, wantA...), grantB)
	}
	requireApprovedGrantJSON(t, want, actual.snapshot, "the returned snapshot must match the exact characterized statement-order outcome")
	var persistedSession, persistedRun []models.AgentCapabilitySnapshotItem
	err = pool.QueryRow(ctx, `SELECT sem.capability_snapshot,ar.capability_snapshot
		FROM session_execution_metadata sem JOIN session_automation_links sal
		ON sal.org_id=sem.org_id AND sal.session_id=sem.session_id
		JOIN automation_runs ar ON ar.org_id=sal.org_id AND ar.id=sal.automation_run_id
		WHERE sem.org_id=$1 AND sem.session_id=$2`, owned.orgID, sessionID).Scan(&persistedSession, &persistedRun)
	require.NoError(t, err, "inspect both full-schema durable snapshots after both callers commit")
	requireApprovedGrantJSON(t, want, persistedSession, "current persistence must retain every committed grant; only explicitly historical SQL may reproduce pre-fix loss")
	requireApprovedGrantJSON(t, want, persistedRun, "the linked automation snapshot must exactly mirror the characterized committed session snapshot")
}

func requireApprovedGrantJSON(t *testing.T, expected, actual []models.AgentCapabilitySnapshotItem, message string) {
	t.Helper()
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err, "the complete expected capability snapshot should encode")
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err, "the complete actual capability snapshot should encode")
	require.JSONEq(t, string(expectedJSON), string(actualJSON), message)
}
