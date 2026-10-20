package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSessionScalarPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	fixturePool := newSessionPersistencePostgresPool(t)
	// This fixture-only application failure leaves PostgreSQL usable, unlike a
	// raised SQL error, so the test can prove genuine earlier staged writes.
	_, err := fixturePool.Exec(context.Background(), `CREATE FUNCTION test_reject_scalar_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
		CREATE TRIGGER test_reject_scalar_snapshot BEFORE INSERT ON session_diff_snapshots FOR EACH ROW
		WHEN (NEW.diff='synthetic rejected scalar snapshot') EXECUTE FUNCTION test_reject_scalar_snapshot()`)
	require.NoError(t, err, "install a synthetic snapshot-only failure in this generated isolated database")
	tests := []struct{ kind, variant, outcome string }{
		{kind: "result", outcome: "commit"}, {kind: "result", outcome: "caller rollback"},
		{kind: "result", variant: "nil", outcome: "commit"}, {kind: "result", variant: "nil", outcome: "caller rollback"},
		{kind: "result", variant: "head only", outcome: "commit"}, {kind: "result", variant: "head only", outcome: "caller rollback"},
		{kind: "result", variant: "empty diff", outcome: "commit"}, {kind: "result", variant: "empty diff", outcome: "caller rollback"},
		{kind: "status", outcome: "commit"}, {kind: "status", outcome: "caller rollback"},
		{kind: "result", outcome: "foreign org"}, {kind: "status", outcome: "foreign org"},
		{kind: "result", outcome: "deleted"}, {kind: "status", outcome: "deleted"},
		{kind: "result", outcome: "late snapshot failure"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.variant+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedSessionPersistencePostgres(t, ctx, fixturePool)
			switch tt.variant {
			case "nil":
				fixture.result = nil
			case "head only":
				fixture.result.Diff, fixture.result.DiffBaseCommitSHA = nil, nil
			case "empty diff":
				fixture.result.Diff = stringPtr("")
			}
			baseline, err := NewSessionStore(fixturePool).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "capture the complete pre-write returned-row baseline before changing soft-delete visibility")
			if tt.outcome == "deleted" {
				_, err := fixturePool.Exec(ctx, "UPDATE sessions SET deleted_at=$3 WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.sessionID, fixture.at)
				require.NoError(t, err, "soft-delete only this synthetic session before testing existing write scope")
			} else if tt.outcome == "late snapshot failure" {
				fixture.result.Diff = stringPtr("synthetic rejected scalar snapshot")
			}
			before := sessionPersistencePostgresRows(t, ctx, fixturePool, fixture)
			config := fixturePool.Config()
			config.MaxConns, config.MinConns = 1, 0
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.True(t, err == nil, "open a bounded one-connection pool without exposing private connection details")
			t.Cleanup(pool.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, pool)
			redis.SetTime(time.Now())
			owner, err := pool.Begin(ctx)
			require.NoError(t, err, "only the caller begins and occupies the pool's sole real connection")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&transactionID), "record the real caller transaction before scalar persistence")
			requested := fixture
			if tt.outcome == "foreign org" {
				requested.orgID = uuid.New()
			}
			session, effects, persistenceErr := callSessionScalarPersistence(store, tx, requested, tt.kind)
			require.Empty(t, tx.settlements, "no scalar seam may create a nested transaction, settle or retry the outer unit")
			require.Empty(t, redis.Keys(), "real enabled status and workspace publishers must remain silent before settlement")
			require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "another connection must see exactly the original entire row set while the owner is uncommitted")
			var afterTransactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterTransactionID), "every application-level result must leave the same transaction usable by its caller")
			require.Equal(t, transactionID, afterTransactionID, "the scalar operation cannot replace, savepoint or settle the original transaction")
			staged := sessionPersistencePostgresRows(t, ctx, owner, fixture)
			var beforeRows, stagedRows map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(before, &beforeRows), "decode the exact baseline row groups for unchanged-boundary comparisons")
			require.NoError(t, json.Unmarshal(staged, &stagedRows), "decode the exact owner-visible row groups without dropping individual columns")
			require.Equal(t, beforeRows["phase"], stagedRows["phase"], "standalone scalar persistence must leave the exact activity phase untouched")
			require.Equal(t, beforeRows["publish"], stagedRows["publish"], "standalone scalar persistence must not mutate any publication-state column")
			if tt.outcome == "foreign org" || tt.outcome == "deleted" || tt.outcome == "late snapshot failure" {
				require.ErrorIs(t, persistenceErr, pgx.ErrNoRows, "existing tenant, visibility and empty-returning application conflicts must retain their raw no-row error")
				require.Equal(t, models.Session{}, session, "rejected persistence must discard the returned session even after its earlier write succeeded")
				require.Equal(t, SessionPersistenceEffects{}, effects, "rejected persistence must discard all possible deferred effects")
				if tt.outcome == "late snapshot failure" {
					require.NotEqual(t, before, staged, "snapshot rejection must happen after genuine row and changeset-head writes rather than passing rollback proof vacuously")
					var diff, head string
					var status models.SessionStatus
					var snapshotCount int
					require.NoError(t, owner.QueryRow(ctx, `SELECT s.status,s.diff,c.head_sha,(SELECT count(*) FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2) FROM sessions s JOIN session_changesets c ON c.org_id=s.org_id AND c.session_id=s.id AND c.is_primary WHERE s.org_id=$1 AND s.id=$2`, fixture.orgID, fixture.sessionID).Scan(&status, &diff, &head, &snapshotCount), "inspect genuine staged row/head effects after the fixture suppressed its later snapshot insertion")
					require.Equal(t, models.SessionStatusCompleted, status, "the earlier result transition must remain staged for the owner to reject")
					require.Equal(t, *fixture.result.Diff, diff, "the earlier authoritative diff must be staged despite the later snapshot conflict")
					require.Equal(t, *fixture.result.DiffHeadCommitSHA, head, "the earlier primary head must be part of the same rejected unit")
					require.Zero(t, snapshotCount, "the fixture must suppress the actual snapshot insert rather than fail an unrelated earlier statement")
				} else {
					require.Equal(t, before, staged, "foreign or soft-deleted scope must stage no row, history, changeset, snapshot, phase or publication change even before rollback")
				}
				require.NoError(t, owner.Rollback(ctx), "only the caller rolls back the entire rejected unit")
				require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "all exact earlier effects must disappear when the owner aborts")
				return
			}
			require.NoError(t, persistenceErr, "scalar persistence must complete while its store's only connection is occupied by the caller")
			stagedSession, err := NewSessionStore(owner).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "inspect the complete projected session from the still-open owner transaction")
			expectedReturned := stagedSession
			// Preserve UPDATE RETURNING's original shape: the AFTER-trigger
			// branch mirror and later snapshot revision are not a reload.
			expectedReturned.TargetBranch = baseline.TargetBranch
			expectedReturned.LatestDiffSnapshotID = baseline.LatestDiffSnapshotID
			expectedReturned.WorkspaceRevision = baseline.WorkspaceRevision
			expectedReturned.WorkspaceRevisionUpdatedAt = baseline.WorkspaceRevisionUpdatedAt
			require.Equal(t, expectedReturned, session, "the standalone seam must return the exact original session projection, not a later fully refreshed row")
			requireSessionPersistencePostgresValues(t, ctx, owner, fixture, stagedSession, tt.kind)
			if tt.outcome == "caller rollback" {
				require.NoError(t, owner.Rollback(ctx), "the caller may reject even successful persistence as part of its larger unit")
				require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "caller rollback must restore every original row and column atomically")
				require.Empty(t, redis.Keys(), "discarded effects must never publish a rolled-back result or status")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the caller commits its complete composed unit")
			require.Equal(t, staged, sessionPersistencePostgresRows(t, ctx, fixturePool, fixture), "confirmed outer commit must make exactly the staged entire row set durable")
			require.Empty(t, redis.Keys(), "commit must not secretly invoke status or workspace effect adapters")
			store.PublishCommittedSessionPersistenceEffects(ctx, fixture.orgID, effects)
			requireSessionPersistenceWorkspaceEvent(t, redis, fixture, tt.kind == "result" && shouldPersistDiffSnapshot(fixture.result))
			store.PublishCommittedSessionStatus(ctx, fixture.orgID, session)
			requireSessionScalarStatusEvent(t, redis, fixture, session)
		})
	}
}
