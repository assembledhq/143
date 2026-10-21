package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type seedActivityObservedWrite struct {
	stage string
	rows  int64
}

type seedActivityObservedTx struct {
	*sessionPersistenceOwnedTx
	writes []seedActivityObservedWrite
}

func (tx *seedActivityObservedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil {
		query := strings.Join(strings.Fields(sql), " ")
		for _, stage := range []struct{ prefix, name string }{
			{"WITH missing AS", "materialize"}, {"UPDATE thread_inbox_entries e", "claim"},
			{"UPDATE thread_inbox_entries SET delivery_state", "ack"}, {"UPDATE thread_inbox_entries SET applied_at", "apply"},
			{"WITH terminal_owners", "retire"}, {"UPDATE thread_runtimes", "watermark"},
		} {
			if strings.HasPrefix(query, stage.prefix) {
				tx.writes = append(tx.writes, seedActivityObservedWrite{stage: stage.name, rows: tag.RowsAffected()})
				break
			}
		}
	}
	return tag, err
}

func TestSeedActivityTransactionFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	// The complete unchanged migrations and all persistence writes run in one
	// generated database. The supplied database is only the fixture admin target.
	fixturePool := newSessionPersistencePostgresPool(t)
	for _, outcome := range []string{"commit", "caller rollback", "later caller failure", "phase conflict", "foreign organization", "foreign runtime", "foreign message", "wrong lease", "expired lease", "nil ids", "zero time", "duplicate ids", "replay", "message composition", "message composition rollback", "durable inbox", "threadless legacy"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			fixture := seedActivityTransactionPostgres(t, ctx, fixturePool, outcome == "threadless legacy")
			var foreign *seedActivityTransactionFixture
			if outcome == "foreign organization" || outcome == "foreign runtime" || outcome == "foreign message" {
				other := seedActivityTransactionPostgres(t, ctx, fixturePool, false)
				foreign = &other
			}
			if outcome == "expired lease" {
				_, err := fixturePool.Exec(ctx, "UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.runtimeID)
				require.NoError(t, err, "expire only this fixture's active runtime before seed persistence")
			} else if outcome == "phase conflict" {
				_, err := NewSessionActivityPhaseStore(fixturePool).StartPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, 1, &fixture.runtimeID, &fixture.leaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, fixture.requestedAt.Add(-time.Second))
				require.NoError(t, err, "create a running phase owned by the same active runtime so retirement cannot mask the later unique-index failure")
			} else if outcome == "durable inbox" {
				for _, messageID := range fixture.messageIDs {
					_, err := NewThreadInboxStore(fixturePool).AppendForMessage(ctx, fixture.orgID, AppendThreadInboxEntryParams{SessionID: fixture.sessionID, ThreadID: fixture.threadID, MessageID: messageID, EntryType: models.ThreadInboxEntryTypeUserMessage})
					require.NoError(t, err, "materialize the existing durable inbox identity through its real store before seed persistence")
				}
			}
			before := seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture)
			var foreignBefore json.RawMessage
			if foreign != nil {
				foreignBefore = seedActivityTransactionSnapshot(t, ctx, fixturePool, *foreign)
			}
			configuration := fixturePool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			pool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open an isolated one-connection pool without printing connection details")
			t.Cleanup(pool.Close)
			owner, err := pool.Begin(ctx)
			require.NoError(t, err, "the caller must occupy the pool's only connection before composing seed persistence")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &seedActivityObservedTx{sessionPersistenceOwnedTx: &sessionPersistenceOwnedTx{Tx: owner}}
			var transactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&transactionID), "record the exact outer transaction before materialization and phase start")
			requested := fixture
			switch outcome {
			case "foreign organization":
				requested.orgID = foreign.orgID
			case "foreign runtime":
				requested.runtimeID, requested.leaseToken = foreign.runtimeID, foreign.leaseToken
			case "foreign message":
				requested.messageIDs = foreign.messageIDs
			case "wrong lease":
				requested.leaseToken = uuid.New()
			case "nil ids":
				requested.messageIDs = nil
			case "zero time":
				requested.requestedAt = time.Time{}
			case "duplicate ids":
				requested.messageIDs = []int64{fixture.messageIDs[0], fixture.messageIDs[1], fixture.messageIDs[1]}
			case "message composition", "message composition rollback":
				message := &models.SessionMessage{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: &fixture.threadID, TurnNumber: fixture.turn, Role: models.MessageRoleUser, Content: "synthetic caller-owned input"}
				require.NoError(t, NewSessionMessageStore(owner).Create(ctx, message), "compose a new message in the caller's same transaction before starting its provider seed")
				requested.messageIDs = append(append([]int64{}, fixture.messageIDs...), message.ID)
			}
			phase, batch, persistenceErr := callSeedActivityInTx(ctx, NewSessionActivityPhaseStore(pool), tx, requested)
			require.Empty(t, tx.settlements, "seed persistence and its transitive phase helper must never settle or nest the caller's transaction")
			require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "an independent connection must still see the complete original message, inbox, batch, phase and runtime state before settlement")
			if foreign != nil {
				require.Equal(t, foreignBefore, seedActivityTransactionSnapshot(t, ctx, fixturePool, *foreign), "a supplied foreign identity must not change the other tenant's durable rows")
			}
			if outcome == "phase conflict" {
				var postgresErr *pgconn.PgError
				require.ErrorAs(t, persistenceErr, &postgresErr, "late phase rejection must come from the actual PostgreSQL uniqueness constraint")
				require.Equal(t, []string{"23505", "idx_session_activity_phases_one_running"}, []string{postgresErr.Code, postgresErr.ConstraintName}, "the exact late running-phase constraint must fail after seed mutation, not an earlier fixture or lease error")
				require.Equal(t, []seedActivityObservedWrite{{"materialize", 2}, {"claim", 2}, {"ack", 2}, {"apply", 2}, {"retire", 0}}, tx.writes, "the real late failure must follow genuine materialization, ownership, acknowledgment and application writes")
				require.Equal(t, models.SessionActivityPhase{}, phase, "the failed insert must return no accepted phase")
				require.Nil(t, batch, "the failed insert must return no accepted batch")
				require.NoError(t, owner.Rollback(ctx), "only the owner rolls back the aborted transaction and all preceding seed writes")
				require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "late phase rejection must roll back all earlier seed rows without advancing the runtime watermark")
				return
			}
			var afterTransactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterTransactionID), "the caller's transaction must remain usable after successful persistence or non-SQL input/scope rejection")
			require.Equal(t, transactionID, afterTransactionID, "all seed and transitive phase writes must retain the original outer transaction identity")
			if outcome == "foreign organization" || outcome == "foreign runtime" || outcome == "foreign message" || outcome == "wrong lease" || outcome == "expired lease" || outcome == "nil ids" || outcome == "zero time" {
				require.Error(t, persistenceErr, "foreign, stale or invalid seed inputs must be rejected")
				require.Equal(t, models.SessionActivityPhase{}, phase, "a rejected seed must return no provider phase")
				require.Nil(t, batch, "a rejected seed must return no batch receipt")
				require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, owner, fixture), "scope, lease and validation rejection must stage no mutation even before rollback")
				if foreign != nil {
					require.Equal(t, foreignBefore, seedActivityTransactionSnapshot(t, ctx, owner, *foreign), "foreign relationships must remain unchanged inside the rejected transaction as well as outside it")
				}
				require.NoError(t, owner.Rollback(ctx), "only the caller may abort the rejected seed unit")
				require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "rejected input must leave the complete original durable rows")
				return
			}
			require.NoError(t, persistenceErr, "the full seed operation must compose while the receiver's only connection is already occupied")
			require.NotNil(t, batch, "a valid durable seed must retain exact inbox-batch attribution")
			seedCount := int64(2)
			materialized := int64(2)
			if outcome == "message composition" || outcome == "message composition rollback" {
				seedCount, materialized = 3, 3
			} else if outcome == "durable inbox" {
				materialized = 0
			}
			require.Equal(t, []seedActivityObservedWrite{{"materialize", materialized}, {"claim", seedCount}, {"ack", seedCount}, {"apply", seedCount}, {"retire", 0}, {"watermark", 1}}, tx.writes, "successful composition must execute each real seed mutation exactly once and advance its watermark only after phase insertion")
			staged := seedActivityTransactionSnapshot(t, ctx, owner, fixture)
			require.NotEqual(t, before, staged, "commit and rollback checks must follow genuine staged writes across the seed domains")
			requireSeedActivityTransactionPostgresRows(t, ctx, owner, requested, phase, *batch, seedCount)
			if outcome == "caller rollback" || outcome == "later caller failure" || outcome == "message composition rollback" {
				if outcome == "later caller failure" {
					_, laterErr := owner.Exec(ctx, "SELECT 1 / 0")
					var postgresErr *pgconn.PgError
					require.ErrorAs(t, laterErr, &postgresErr, "the deliberately later caller operation must fail only after complete successful seed persistence")
					require.Equal(t, "22012", postgresErr.Code, "the outer failure must be the controlled later division error, not an early seed failure")
				}
				require.NoError(t, owner.Rollback(ctx), "the caller may abort after a complete seed result or any later composed failure")
				require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "outer rollback must restore exact messages, inbox identities, batch receipts, phases and runtime watermarks")
				if outcome == "caller rollback" {
					retry, err := pool.Begin(ctx)
					require.NoError(t, err, "begin a fresh outer unit after discarding the successful first attempt")
					defer func() { _ = retry.Rollback(context.Background()) }()
					_, _, err = callSeedActivityInTx(ctx, NewSessionActivityPhaseStore(nil), retry, fixture)
					require.NoError(t, err, "a rolled-back seed remains replayable rather than falsely recorded as applied")
					require.NoError(t, retry.Rollback(ctx), "discard this retry proof without leaving another provider phase")
				}
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the caller may make seed acknowledgment, phase and watermark writes durable")
			require.Equal(t, staged, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "outer commit must expose the complete exact staged rows without a second independent transaction")
			if outcome == "replay" {
				_, err := NewSessionActivityPhaseStore(pool).CompletePhase(ctx, fixture.orgID, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now().UTC())
				require.NoError(t, err, "finish the first phase so running-phase uniqueness cannot explain replay rejection")
				committed := seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture)
				retry, err := pool.Begin(ctx)
				require.NoError(t, err, "start a fresh transaction to test already committed seed rejection")
				defer func() { _ = retry.Rollback(context.Background()) }()
				replayedPhase, replayedBatch, err := callSeedActivityInTx(ctx, NewSessionActivityPhaseStore(nil), retry, fixture)
				require.ErrorIs(t, err, ErrInboxBatchConflict, "already applied committed seeds must not create a second provider admission")
				require.Equal(t, models.SessionActivityPhase{}, replayedPhase, "replay must return no accepted phase")
				require.Nil(t, replayedBatch, "replay must return no accepted batch")
				require.Equal(t, committed, seedActivityTransactionSnapshot(t, ctx, retry, fixture), "a rejected replay must stage no replacement phase or receipt")
				require.NoError(t, retry.Rollback(ctx), "only the caller rolls back the rejected replay")
				require.Equal(t, committed, seedActivityTransactionSnapshot(t, ctx, fixturePool, fixture), "replay rejection must preserve the exact first committed admission")
			}
		})
	}
}

func seedActivityTransactionPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool, threadless bool) seedActivityTransactionFixture {
	t.Helper()
	fixture := newSeedActivityTransactionFixture()
	_, err := pool.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,'synthetic seed transaction fixture')", fixture.orgID)
	require.NoError(t, err, "create an isolated synthetic tenant for the full-schema seed proof")
	session := &models.Session{OrgID: fixture.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create the genuine session and primary thread through the production store")
	fixture.sessionID, fixture.threadID = session.ID, *session.PrimaryThreadID
	runtime, err := NewThreadRuntimeStore(pool).CreateStarting(ctx, fixture.orgID, CreateThreadRuntimeParams{SessionID: fixture.sessionID, ThreadID: fixture.threadID, SandboxID: uuid.New(), AgentType: models.DefaultDefaultAgentType, OwnerNodeID: "synthetic-seed-worker", LeaseToken: fixture.leaseToken, LeaseDuration: 5 * time.Minute})
	require.NoError(t, err, "create the legacy scoped live lease through the unchanged runtime store")
	fixture.runtimeID = runtime.ID
	fixture.messageIDs = nil
	for _, content := range []string{"synthetic first seed", "synthetic second seed"} {
		threadID := &fixture.threadID
		if threadless {
			threadID = nil
		}
		message := &models.SessionMessage{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: threadID, TurnNumber: fixture.turn, Role: models.MessageRoleUser, Content: content}
		require.NoError(t, NewSessionMessageStore(pool).Create(ctx, message), "create a real user message predating durable inbox identity")
		fixture.messageIDs = append(fixture.messageIDs, message.ID)
	}
	fixture.requestedAt = time.Now().UTC().Truncate(time.Microsecond)
	return fixture
}

func seedActivityTransactionSnapshot(t *testing.T, ctx context.Context, conn DBTX, f seedActivityTransactionFixture) json.RawMessage {
	t.Helper()
	var snapshot json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'messages',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM session_messages m WHERE m.org_id=$1 AND m.session_id=$2),'[]'::jsonb),
		'inbox',COALESCE((SELECT jsonb_agg(to_jsonb(e) ORDER BY e.sequence_no,e.id) FROM thread_inbox_entries e WHERE e.org_id=$1 AND e.session_id=$2 AND e.thread_id=$3),'[]'::jsonb),
		'batches',COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.id) FROM thread_inbox_delivery_batches b WHERE b.org_id=$1 AND b.session_id=$2 AND b.thread_id=$3),'[]'::jsonb),
		'runtimes',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM thread_runtimes r WHERE r.org_id=$1 AND r.session_id=$2 AND r.thread_id=$3),'[]'::jsonb),
		'phases',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.phase_number,p.id) FROM session_activity_phases p WHERE p.org_id=$1 AND p.session_id=$2 AND p.thread_id=$3),'[]'::jsonb))`, f.orgID, f.sessionID, f.threadID).Scan(&snapshot)
	require.NoError(t, err, "capture every field in the message, inbox, receipt, runtime and phase domains with exact tenant scope")
	return snapshot
}

func requireSeedActivityTransactionPostgresRows(t *testing.T, ctx context.Context, conn pgx.Tx, f seedActivityTransactionFixture, phase models.SessionActivityPhase, batch models.ThreadInboxDeliveryBatch, count int64) {
	t.Helper()
	rows, err := conn.Query(ctx, "SELECT "+activityPhaseColumns+" FROM session_activity_phases WHERE org_id=$1 AND id=$2", f.orgID, phase.ID)
	require.NoError(t, err, "read the complete phase staged inside the still caller-owned transaction")
	durablePhase, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	require.NoError(t, err, "collect the exact phase returned by seed persistence")
	require.Equal(t, phase, durablePhase, "the returned phase must exactly match all staged database fields")
	durableBatch, err := NewSessionActivityPhaseStore(conn).GetInboxDeliveryBatch(ctx, f.orgID, batch.ID)
	require.NoError(t, err, "read the complete staged batch through the original tenant-scoped accessor")
	// The legacy operation returns its allocated batch after setting StartedAt
	// from the normalized input, rather than reloading the later started row.
	// pgx may decode the same timestamptz in time.Local; retain that established
	// returned-value distinction instead of changing the production contract.
	expectedBatch := durableBatch
	startedAt := f.requestedAt.Truncate(time.Microsecond)
	expectedBatch.StartedAt = &startedAt
	require.Equal(t, expectedBatch, batch, "the returned batch must retain every allocated field and the legacy input-time StartedAt representation")
	require.True(t, durableBatch.StartedAt != nil && durableBatch.StartedAt.Equal(startedAt), "the staged batch's start timestamp must be the exact same instant even when pgx uses a different time location")
	require.Equal(t, []int64{1, count}, []int64{batch.SequenceStart, batch.SequenceEnd}, "the batch must cover the exact contiguous unique seed range")
	require.Equal(t, models.InboxDeliveryBatchStarted, batch.Status, "seed acknowledgment and phase admission must start the same exact batch")
	require.Equal(t, f.requestedAt.Truncate(time.Microsecond).UTC(), phase.StartedAt.UTC(), "seed phase start time must retain the exact normalized instant independently of pgx's decoded time location")
	var delivered, acknowledged int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT last_delivered_sequence,last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2", f.orgID, f.runtimeID).Scan(&delivered, &acknowledged), "read both staged runtime watermarks after provider-phase insertion")
	require.Equal(t, []int64{count, count}, []int64{delivered, acknowledged}, "the complete seed range must advance both watermarks in the same outer transaction")
	var accepted, applied int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE delivery_state='acked' AND applied_at=$5 AND acked_at=$5 AND delivered_at=$5 AND runtime_id=$4)
		FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, f.orgID, f.sessionID, f.threadID, f.runtimeID, phase.StartedAt).Scan(&accepted, &applied), "read exact acknowledged-and-applied rows for this provider seed")
	require.Equal(t, []int64{count, count}, []int64{accepted, applied}, "every and only contiguous seed identity must be durably acknowledged and applied with its phase start time")
}
