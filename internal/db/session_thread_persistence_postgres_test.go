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

func TestThreadPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	// Reuse the generated-database fixture and complete, unchanged migration
	// chain. Persistence writes target only that generated database; the
	// supplied database is used for administrative fixture creation and cleanup.
	fixturePool := newSessionPersistencePostgresPool(t)
	tests := []struct{ kind, outcome string }{}
	for _, kind := range []string{"status", "complete", "result", "turn"} {
		for _, outcome := range []string{"commit", "caller rollback", "foreign org", "archived", "later state"} {
			tests = append(tests, struct{ kind, outcome string }{kind, outcome})
		}
		if kind == "result" || kind == "turn" {
			for _, outcome := range []string{"nil result", "nil diff", "empty diff"} {
				tests = append(tests, struct{ kind, outcome string }{kind, outcome})
			}
		}
		if kind == "complete" || kind == "turn" {
			tests = append(tests, struct{ kind, outcome string }{kind, "empty provider"})
		}
		if kind == "status" {
			for _, outcome := range []string{"running", "idle"} {
				tests = append(tests, struct{ kind, outcome string }{kind, outcome})
			}
		}
		if kind == "result" {
			tests = append(tests, struct{ kind, outcome string }{kind, "idle"})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedThreadPersistencePostgres(t, ctx, fixturePool)
			if tt.outcome == "archived" {
				_, err := fixturePool.Exec(ctx, "UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.threadID, fixture.at)
				require.NoError(t, err, "seed an archived row to retain the legacy mutation and publication distinction")
			}
			before := threadPersistencePostgresSnapshot(t, ctx, fixturePool, fixture)
			baseline := threadPersistencePostgresThread(t, ctx, fixturePool, fixture)
			configuration := fixturePool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			pool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open a one-connection pool only on this generated fixture without publishing connection details")
			t.Cleanup(pool.Close)
			store, redis := threadPersistenceStoreWithStreams(t, pool)
			owner, err := pool.Begin(ctx)
			require.NoError(t, err, "the outer caller alone must occupy the pool's only connection")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			var transactionTime time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current(),now()").Scan(&transactionID, &transactionTime), "record the actual transaction identity and timestamp before the composed write")
			requested := fixture
			switch tt.outcome {
			case "foreign org":
				requested.orgID = uuid.New()
			case "nil result":
				requested.result = nil
			case "nil diff":
				requested.result.Diff = nil
			case "empty diff":
				empty := ""
				requested.result.Diff = &empty
			case "empty provider":
				requested.agentSessionID = ""
			case "running":
				requested.status = models.ThreadStatusRunning
			case "idle":
				requested.status = models.ThreadStatusIdle
			}
			effects, persistenceErr := callThreadPersistenceInTx(ctx, store, tx, requested, tt.kind)
			require.Empty(t, tx.settlements, "no explicit thread persistence path may open a savepoint, commit or roll back the outer unit")
			require.Empty(t, redis.Keys(), "real configured publishers must remain silent before the caller's settlement")
			require.Equal(t, before, threadPersistencePostgresSnapshot(t, ctx, fixturePool, fixture), "an independent connection must see every original session and thread field while the caller's mutation remains uncommitted")
			var afterTransactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterTransactionID), "the outer transaction must remain usable for further composition, including validation rejection")
			require.Equal(t, transactionID, afterTransactionID, "thread persistence must retain the same outer PostgreSQL transaction")
			if tt.outcome == "foreign org" || tt.outcome == "nil result" {
				require.Error(t, persistenceErr, "tenant mismatch and missing result must reject the requested operation")
				require.Equal(t, ThreadPersistenceEffects{}, effects, "rejected writes must return no accepted notification effects")
				require.Equal(t, before, threadPersistencePostgresSnapshot(t, ctx, owner, fixture), "scope and input rejection must stage no mutation even before rollback")
				require.NoError(t, owner.Rollback(ctx), "only the outer caller may abort a rejected unit")
				require.Equal(t, before, threadPersistencePostgresSnapshot(t, ctx, fixturePool, fixture), "rejection must leave the exact original rows durable")
				return
			}
			require.NoError(t, persistenceErr, "thread persistence must succeed without borrowing the occupied pool connection")
			require.Equal(t, ThreadPersistenceEffects{orgID: fixture.orgID, threadID: fixture.threadID, runtimeChanged: true}, effects, "successful effects must carry only the exact tenant and thread to reload after commit")
			staged := threadPersistencePostgresSnapshot(t, ctx, owner, fixture)
			stagedThread := threadPersistencePostgresThread(t, ctx, owner, fixture)
			expected := expectedThreadPersistencePostgresRow(baseline, requested, tt.kind, transactionTime)
			require.Equal(t, expected, stagedThread, "the complete projected thread must retain every legacy timestamp, nil/empty, failure, archive and exact-turn behavior")
			require.NotEqual(t, before, staged, "rollback and commit assertions must follow a genuine staged mutation rather than a vacuous no-op")
			if tt.outcome == "caller rollback" {
				require.NoError(t, owner.Rollback(ctx), "the owner may reject a successful persistence result as part of a larger unit")
				require.Equal(t, before, threadPersistencePostgresSnapshot(t, ctx, fixturePool, fixture), "outer rollback must restore all original session and thread fields")
				require.Empty(t, redis.Keys(), "discarded effects must not advertise rolled-back state")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the outer caller may make the combined unit durable")
			require.Equal(t, staged, threadPersistencePostgresSnapshot(t, ctx, fixturePool, fixture), "confirmed commit must expose exactly the staged rows without an independent write")
			require.Empty(t, redis.Keys(), "a database commit alone must not publish an in-memory effect")
			if tt.outcome == "later state" {
				_, err := fixturePool.Exec(ctx, "UPDATE session_threads SET status='pending',current_turn=19 WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.threadID)
				require.NoError(t, err, "simulate a later committed write before publishing the earlier reload notification")
				stagedThread.Status, stagedThread.CurrentTurn = models.ThreadStatusPending, 19
			}
			store.PublishCommittedThreadPersistenceEffects(ctx, uuid.New(), effects)
			require.Empty(t, redis.Keys(), "a foreign organization must not use another tenant's captured reload identity")
			store.PublishCommittedThreadPersistenceEffects(ctx, fixture.orgID, effects)
			if tt.outcome == "archived" {
				require.Empty(t, redis.Keys(), "the existing visible-thread reload must omit archived rows even though the legacy mutation accepts them")
			} else {
				requireThreadPersistenceEvent(t, redis, models.ThreadRuntimeEvent{
					SessionID: stagedThread.SessionID, ThreadID: stagedThread.ID, OrgID: stagedThread.OrgID,
					Status: stagedThread.Status, AgentSessionID: stagedThread.AgentSessionID, CurrentTurn: stagedThread.CurrentTurn,
					PendingMessageCount: stagedThread.PendingMessageCount, LastActivityAt: stagedThread.LastActivityAt,
					StartedAt: stagedThread.StartedAt, CompletedAt: stagedThread.CompletedAt,
					FailureExplanation: stagedThread.FailureExplanation, FailureCategory: stagedThread.FailureCategory,
				})
			}
		})
	}
}

func seedThreadPersistencePostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) threadPersistenceFixture {
	t.Helper()
	fixture := newThreadPersistenceFixture()
	_, err := pool.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,'synthetic thread persistence fixture')", fixture.orgID)
	require.NoError(t, err, "create this test case's isolated synthetic tenant")
	session := &models.Session{OrgID: fixture.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create a genuine session and primary thread through the unchanged production path")
	fixture.sessionID, fixture.threadID = session.ID, *session.PrimaryThreadID
	_, err = pool.Exec(ctx, `UPDATE session_threads
		SET status='running',current_turn=7,agent_session_id='synthetic-prior-provider',
		result_summary='synthetic prior summary',diff='synthetic prior diff',
		failure_explanation='synthetic prior explanation',failure_category='synthetic_prior_category',
		last_activity_at=$3,started_at=$3,completed_at=$3,cancel_requested_at=$3
		WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.threadID, fixture.at)
	require.NoError(t, err, "seed every compatibility-sensitive field, including a higher previous turn and pending cancellation")
	return fixture
}

func threadPersistencePostgresSnapshot(t *testing.T, ctx context.Context, conn DBTX, f threadPersistenceFixture) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'thread',to_jsonb(t))
		FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
		WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3`, f.orgID, f.sessionID, f.threadID).Scan(&rows)
	require.NoError(t, err, "capture the complete tenant-scoped session and thread rows, not only selected status fields")
	return rows
}

func threadPersistencePostgresThread(t *testing.T, ctx context.Context, conn DBTX, f threadPersistenceFixture) models.SessionThread {
	t.Helper()
	rows, err := conn.Query(ctx, "SELECT "+sessionThreadSelectColumns+" FROM session_threads WHERE org_id=$1 AND id=$2", f.orgID, f.threadID)
	require.NoError(t, err, "read all projected thread fields, including archived rows needed for legacy characterization")
	thread, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.SessionThread])
	require.NoError(t, err, "collect the exact complete projected thread")
	return thread
}

func expectedThreadPersistencePostgresRow(before models.SessionThread, f threadPersistenceFixture, kind string, at time.Time) models.SessionThread {
	expected := before
	switch kind {
	case "status":
		expected.Status = f.status
		if f.status == models.ThreadStatusRunning {
			expected.StartedAt, expected.CancelRequestedAt = &at, nil
		} else if f.status == models.ThreadStatusCompleted || f.status == models.ThreadStatusFailed || f.status == models.ThreadStatusCancelled {
			expected.CompletedAt = &at
		}
	case "complete":
		expected.Status, expected.CurrentTurn, expected.LastActivityAt = models.ThreadStatusIdle, f.turn, &at
		if f.agentSessionID != "" {
			expected.AgentSessionID = &f.agentSessionID
		}
	case "result":
		expected.Status, expected.ResultSummary = f.status, f.result.ResultSummary
		if f.status == models.ThreadStatusCompleted || f.status == models.ThreadStatusFailed || f.status == models.ThreadStatusCancelled {
			expected.CompletedAt = &at
		}
		expected.FailureExplanation, expected.FailureCategory = f.result.FailureExplanation, f.result.FailureCategory
		if f.result.FailureExplanation == nil {
			expected.FailureExplanation = f.result.Error
		}
	default:
		expected.Status, expected.CurrentTurn, expected.LastActivityAt = models.ThreadStatusIdle, f.turn, &at
		expected.AgentSessionID, expected.ResultSummary = &f.agentSessionID, f.result.ResultSummary
		expected.FailureExplanation, expected.FailureCategory = nil, nil
	}
	if (kind == "result" || kind == "turn") && f.result.Diff != nil {
		expected.Diff = f.result.Diff
	}
	return expected
}
