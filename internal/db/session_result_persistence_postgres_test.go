package db

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	fixturePool := newSessionPersistencePostgresPool(t)
	tests := []struct{ kind, outcome string }{}
	for _, kind := range []string{"result", "status", "turn"} {
		for _, outcome := range []string{"commit", "caller rollback", "phase failure", "foreign org"} {
			tests = append(tests, struct{ kind, outcome string }{kind, outcome})
		}
		if kind != "status" {
			for _, outcome := range []string{"nil result", "head only"} {
				tests = append(tests, struct{ kind, outcome string }{kind, outcome})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedSessionPersistencePostgres(t, ctx, fixturePool)
			if tt.outcome == "nil result" {
				fixture.result = nil
			} else if tt.outcome == "head only" {
				fixture.result.Diff, fixture.result.DiffBaseCommitSHA = nil, nil
			}
			before := sessionPersistencePostgresRows(t, ctx, fixturePool, fixture)
			baseline, err := NewSessionStore(fixturePool).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "record the complete baseline session before the caller begins its unit")
			config := fixturePool.Config()
			config.MaxConns, config.MinConns = 1, 0
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.True(t, err == nil, "open a one-connection pool without publishing connection details")
			t.Cleanup(pool.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, pool)
			owner, err := pool.Begin(ctx)
			require.NoError(t, err, "only the outer caller begins and owns this real transaction")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&transactionID), "record the actual outer transaction identity before persistence")
			requested := fixture
			if tt.outcome == "phase failure" {
				requested.phaseID = uuid.New()
			} else if tt.outcome == "foreign org" {
				requested.orgID = uuid.New()
			}
			phase, session, effects, persistenceErr := callSessionPersistenceInTx(store, tx, requested, tt.kind)
			require.Empty(t, tx.settlements, "no persistence path may create a savepoint, commit or abort the owner's real transaction")
			require.Empty(t, redis.Keys(), "real enabled publishers must remain silent throughout every uncommitted path")
			require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "an independent connection must still see exactly the original rows before the owner commits")
			if tt.outcome == "phase failure" || tt.outcome == "foreign org" {
				require.Error(t, persistenceErr, "a missing phase or tenant scope must reject the entire persistence unit")
				require.Equal(t, models.SessionActivityPhase{}, phase, "a rejected unit must not return an accepted phase")
				require.Equal(t, models.Session{}, session, "a rejected unit must not return an accepted session")
				require.Equal(t, SessionPersistenceEffects{}, effects, "a rejected unit must discard any workspace effects collected before its phase failure")
				// These failures are application-level no-row results, not SQL
				// errors, so PostgreSQL leaves the owner's transaction usable.
				// Inspect that transaction before rollback to distinguish genuine
				// scope isolation from changes that only disappear when aborted.
				ownerVisible := sessionPersistencePostgresRows(t, ctx, owner, fixture)
				if tt.outcome == "foreign org" {
					require.Equal(t, before, ownerVisible, "a foreign organization must not stage any row, changeset, snapshot or phase mutation even inside the rejected transaction")
				} else {
					require.NotEqual(t, before, ownerVisible, "missing-phase rejection must occur after genuine earlier writes so the whole-unit rollback proof cannot pass vacuously")
					var stagedStatus models.SessionStatus
					var stagedSnapshots int
					require.NoError(t, owner.QueryRow(ctx, "SELECT status,(SELECT count(*) FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2) FROM sessions WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.sessionID).Scan(&stagedStatus, &stagedSnapshots), "inspect the earlier staged session and snapshot writes before the caller aborts")
					expectedStatus, expectedSnapshots := models.SessionStatusCompleted, 0
					if tt.kind != "status" {
						expectedSnapshots = 1
					}
					if tt.kind == "turn" {
						expectedStatus = models.SessionStatusIdle
					}
					require.Equal(t, expectedStatus, stagedStatus, "phase rejection must leave the earlier staged session projection for the outer owner to roll back")
					require.Equal(t, expectedSnapshots, stagedSnapshots, "phase rejection must leave exactly the earlier staged authoritative snapshot, when this operation creates one")
				}
				require.NoError(t, owner.Rollback(ctx), "only the caller rolls back all earlier writes after persistence rejection")
				require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "row, changeset, diff-history, snapshot, workspace, publish-state and phase effects must all disappear on rejection")
				return
			}
			require.NoError(t, persistenceErr, "the complete real persistence unit must compose in the already occupied single-connection pool")
			var afterTransactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterTransactionID), "the owner must retain its live transaction after persistence returns")
			require.Equal(t, transactionID, afterTransactionID, "persistence may not replace or settle the outer transaction")
			staged := sessionPersistencePostgresRows(t, ctx, owner, fixture)
			stagedSession, err := NewSessionStore(owner).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "inspect all projected session fields within the still-uncommitted unit")
			if tt.kind != "turn" {
				expectedReturned := stagedSession
				// The existing result method returns the row before its later
				// diff-snapshot revision update and AFTER-trigger branch mirror;
				// extraction must not silently turn it into a later reload.
				expectedReturned.LatestDiffSnapshotID = baseline.LatestDiffSnapshotID
				expectedReturned.WorkspaceRevision = baseline.WorkspaceRevision
				expectedReturned.WorkspaceRevisionUpdatedAt = baseline.WorkspaceRevisionUpdatedAt
				expectedReturned.TargetBranch = baseline.TargetBranch
				require.Equal(t, expectedReturned, session, "the InTx seam must preserve the legacy operation's complete returned session shape")
			}
			requireSessionPersistencePostgresValues(t, ctx, owner, fixture, stagedSession, tt.kind)
			if tt.outcome == "caller rollback" {
				require.NoError(t, owner.Rollback(ctx), "the outer owner may reject an otherwise successful composed persistence unit")
				require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "outer rollback must restore the entire exact original row set, not just status")
				require.Empty(t, redis.Keys(), "captured effects alone must never publish a rolled-back boundary")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the caller commits the combined result and phase boundary")
			require.Equal(t, staged, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "the owner's commit must expose exactly the complete staged rows without another independent write")
			require.Empty(t, redis.Keys(), "confirmed commit alone must not invoke an in-memory effect adapter implicitly")
			phaseRows, err := fixturePool.Query(ctx, "SELECT "+activityPhaseColumns+" FROM session_activity_phases WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.phaseID)
			require.NoError(t, err, "reload the exact committed activity phase")
			durablePhase, err := pgx.CollectOneRow(phaseRows, pgx.RowToStructByName[models.SessionActivityPhase])
			require.NoError(t, err, "collect every field of the durable activity phase")
			require.Equal(t, phase, durablePhase, "the returned phase must exactly match the row made durable by the outer commit")
			store.PublishCommittedSessionPersistenceEffects(ctx, uuid.New(), effects)
			require.Empty(t, redis.Keys(), "effect publication must reject another organization's opaque result")
			store.PublishCommittedSessionPersistenceEffects(ctx, fixture.orgID, effects)
			requireSessionPersistenceWorkspaceEvent(t, redis, fixture, tt.kind != "status" && shouldPersistDiffSnapshot(fixture.result))
		})
	}
}

func seedSessionPersistencePostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) sessionPersistenceFixture {
	t.Helper()
	fixture := newSessionPersistenceFixture()
	_, err := pool.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,'synthetic persistence fixture')", fixture.orgID)
	require.NoError(t, err, "create an isolated synthetic tenant for this parallel case")
	session := &models.Session{OrgID: fixture.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create the session and its primary thread through the real store")
	fixture.sessionID, fixture.threadID = session.ID, *session.PrimaryThreadID
	_, err = pool.Exec(ctx, `UPDATE sessions SET diff='synthetic prior diff',diff_stats='{"added":4,"removed":1,"files_changed":2}',diff_history='[]',workspace_revision=8 WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "seed an authoritative previous diff and revision to detect nil-result clobbering")
	updated, err := pool.Exec(ctx, `UPDATE session_changesets SET head_sha='synthetic-prior-head' WHERE org_id=$1 AND session_id=$2 AND is_primary`, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "seed the existing store-created primary changeset whose head belongs to the same atomic boundary")
	require.Equal(t, int64(1), updated.RowsAffected(), "the real session create path must already have exactly one primary changeset")
	_, err = pool.Exec(ctx, `INSERT INTO session_publish_state(org_id,session_id,pr_creation_state,pr_creation_error,pr_push_state,pr_push_error,pr_push_error_code,branch_creation_state,branch_creation_error) VALUES($1,$2,'failed','synthetic creation failure','pushing','synthetic active push','generic','pushing','synthetic active branch') ON CONFLICT(session_id) DO UPDATE SET pr_creation_state='failed',pr_creation_error='synthetic creation failure',pr_push_state='pushing',pr_push_error='synthetic active push',pr_push_error_code='generic',branch_creation_state='pushing',branch_creation_error='synthetic active branch'`, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "seed publish-state fields including in-flight guards that turn completion must preserve")
	phase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, 1, nil, nil, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, fixture.at.Add(-time.Minute))
	require.NoError(t, err, "create a genuine running activity phase for this exact session and thread")
	fixture.phaseID = phase.ID
	return fixture
}

func sessionPersistencePostgresRows(t *testing.T, ctx context.Context, database DBTX, fixture sessionPersistenceFixture) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
		'phase',(SELECT to_jsonb(p) FROM session_activity_phases p WHERE org_id=$1 AND id=$3),
		'publish',(SELECT jsonb_agg(to_jsonb(p) ORDER BY session_id) FROM session_publish_state p WHERE org_id=$1 AND session_id=$2),
		'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM session_changesets c WHERE org_id=$1 AND session_id=$2),
		'snapshots',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM session_diff_snapshots d WHERE org_id=$1 AND session_id=$2))`, fixture.orgID, fixture.sessionID, fixture.phaseID).Scan(&rows)
	require.NoError(t, err, "capture every column in every row touched by the persistence unit for this exact tenant and session")
	return rows
}

func requireSessionPersistencePostgresValues(t *testing.T, ctx context.Context, tx pgx.Tx, fixture sessionPersistenceFixture, session models.Session, kind string) {
	t.Helper()
	expectedStatus, expectedTurn := models.SessionStatusCompleted, 0
	if kind == "turn" {
		expectedStatus, expectedTurn = models.SessionStatusIdle, 2
	}
	require.Equal(t, expectedStatus, session.Status, "the existing result/status/turn projection must remain unchanged")
	require.Equal(t, expectedTurn, session.CurrentTurn, "turn metadata must advance only through the original turn-completion path")
	var marker *string
	var registryRows int
	require.NoError(t, tx.QueryRow(ctx, "SELECT current_setting('lifecycle143.writer_version',true),(SELECT count(*) FROM lifecycle_writer_instances)").Scan(&marker, &registryRows), "inspect passive authority state in the actual caller-owned transaction")
	require.Nil(t, marker, "the persistence-only seam must not install a lifecycle writer-version GUC")
	require.Zero(t, registryRows, "the persistence-only seam must not create writer-instance observations")
	var head string
	require.NoError(t, tx.QueryRow(ctx, "SELECT head_sha FROM session_changesets WHERE org_id=$1 AND session_id=$2 AND is_primary", fixture.orgID, fixture.sessionID).Scan(&head), "inspect the real primary changeset head in the same transaction")
	if kind == "status" || fixture.result == nil {
		require.Equal(t, "synthetic-prior-head", head, "status-only and nil-result paths must preserve the previous changeset head")
	} else {
		require.Equal(t, *fixture.result.DiffHeadCommitSHA, head, "result and turn paths must atomically persist the supplied primary changeset head")
	}
	if kind == "status" || !shouldPersistDiffSnapshot(fixture.result) {
		require.Equal(t, stringPtr("synthetic prior diff"), session.Diff, "a missing authoritative diff must preserve the previously persisted diff")
		require.Equal(t, int64(8), session.WorkspaceRevision, "diff-free persistence must not create a workspace revision")
		require.Equal(t, json.RawMessage(`[]`), session.DiffHistory, "diff-free persistence must not append a fabricated history entry")
	} else {
		require.Equal(t, fixture.result.Diff, session.Diff, "the supplied authoritative diff must persist unchanged")
		require.Equal(t, int64(9), session.WorkspaceRevision, "one authoritative snapshot must bump the workspace revision exactly once")
		var snapshots []struct {
			Turn                     int
			Diff, Base, Head, Source string
			Dirty                    bool
		}
		rows, err := tx.Query(ctx, "SELECT turn_number,diff,base_commit_sha,head_commit_sha,source,workspace_dirty FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2 ORDER BY sequence_number", fixture.orgID, fixture.sessionID)
		require.NoError(t, err, "read all real snapshots written in this unit")
		snapshots, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Turn                     int
			Diff, Base, Head, Source string
			Dirty                    bool
		}])
		require.NoError(t, err, "collect every persisted snapshot rather than asserting a count only")
		turn := 0
		if kind == "turn" {
			turn = 2
		}
		require.Equal(t, []struct {
			Turn                     int
			Diff, Base, Head, Source string
			Dirty                    bool
		}{{Turn: turn, Diff: *fixture.result.Diff, Base: *fixture.result.DiffBaseCommitSHA, Head: *fixture.result.DiffHeadCommitSHA, Source: "turn_complete", Dirty: true}}, snapshots, "the exact snapshot content, attribution and turn must survive extraction")
	}
	if kind == "turn" {
		require.Equal(t, stringPtr("synthetic-agent"), session.AgentSessionID, "turn completion must retain the supplied agent context")
		require.Equal(t, stringPtr("synthetic-checkpoint"), session.SnapshotKey, "turn completion must retain the existing checkpoint bookkeeping")
		require.Equal(t, models.PRCreationStateIdle, session.PRCreationState, "turn completion must reset the prior creation failure")
		require.Nil(t, session.PRCreationError, "turn completion must clear its previous PR-creation error")
		require.Equal(t, models.PRPushState("pushing"), session.PRPushState, "turn completion must not overwrite an in-flight push")
		require.Equal(t, stringPtr("synthetic active push"), session.PRPushError, "turn completion must retain an in-flight push's existing diagnostics")
		require.Equal(t, models.BranchCreationState("pushing"), session.BranchCreationState, "turn completion must not overwrite an in-flight branch operation")
	}
}

const sessionPersistenceDatabaseCleanupTimeout = time.Minute

// Cleanup must mark failure without FailNow: a fatal assertion during a
// parent's cleanup can skip Go's named test report after parallel subtests.
type sessionPersistenceCleanupReporter interface {
	Helper()
	Errorf(string, ...any)
}

func dropSessionPersistencePostgresDatabase(t sessionPersistenceCleanupReporter, admin DBTX, databaseName string) {
	t.Helper()
	// Full-schema database removal shares the server with other parallel
	// fixtures. Give it its own bounded budget, independent of test work, while
	// retaining ordinary DROP so an owned-connection leak remains a failure.
	ctx, cancel := context.WithTimeout(context.Background(), sessionPersistenceDatabaseCleanupTimeout)
	defer cancel()
	if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Errorf("drop only the generated fixture after owned connections close, without forced termination: %v", err)
	}
}

func newSessionPersistencePostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema session persistence proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the supplied disposable test server without printing its DSN")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close this fixture's administrator connection: %v", err)
		}
	})
	var serverVersion int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion), "read the actual server version before creating a fixture")
	require.GreaterOrEqual(t, serverVersion, 150000, "the complete unchanged schema requires PostgreSQL 15 or newer")
	name := "session_persistence_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this test's independently generated database")
	t.Cleanup(func() {
		dropSessionPersistencePostgresDatabase(t, admin, name)
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the fixture URI without publishing its connection details")
	query := target.Query()
	query.Del("dbname")
	query.Del("database")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	isolated := target.String()
	configuration, err := pgx.ParseConfig(isolated)
	require.True(t, err == nil && configuration.Database == name, "the fixture and migration drivers must target only the generated database")
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve the complete unmodified repository migration chain")
	migration, err := migrate.New("file://"+source, isolated)
	require.True(t, err == nil, "open the real migration driver only on the generated fixture")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		if sourceErr != nil {
			t.Errorf("close the complete migration source before fixture cleanup: %v", sourceErr)
		}
		if databaseErr != nil {
			t.Errorf("close the migration connection before fixture cleanup: %v", databaseErr)
		}
	})
	require.NoError(t, migration.Up(), "apply the entire real production schema before exercising persistence composition")
	pool, err := pgxpool.New(ctx, isolated)
	require.True(t, err == nil, "open only the fully migrated generated test database without printing its DSN")
	t.Cleanup(pool.Close)
	return pool
}
