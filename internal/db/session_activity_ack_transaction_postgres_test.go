package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// This is a test-only observation at the real atomic owner's commit boundary,
// not a settlement hook in the production persistence implementation.
type inboxAckCommitProof struct {
	t               *testing.T
	pool            *pgxpool.Pool
	fixture         seedActivityTransactionFixture
	oldPhase        models.SessionActivityPhase
	before, staged  json.RawMessage
	at              time.Time
	repaired        bool
	replayed        bool
	failCommit      bool
	transactionID   int64
	transactionTime time.Time
}

func (p *inboxAckCommitProof) inspect(ctx context.Context, tx pgx.Tx) error {
	p.staged = seedActivityTransactionSnapshot(p.t, ctx, tx, p.fixture)
	require.Equal(p.t, p.before, seedActivityTransactionSnapshot(p.t, ctx, p.pool, p.fixture), "the real atomic owner must reach commit while an independent connection still sees the entire prior phase/inbox/batch/runtime state")
	var transactionID int64
	require.NoError(p.t, tx.QueryRow(ctx, "SELECT txid_current()").Scan(&transactionID), "the atomic owner must retain its exact outer transaction until settlement")
	require.Equal(p.t, p.transactionID, transactionID, "acknowledgment and replacement phase persistence must never switch or nest the outer transaction")
	if p.replayed {
		require.Equal(p.t, p.before, p.staged, "a started-batch replay must stage no second phase, receipt, timestamp or watermark mutation")
		return nil
	}
	require.NotEqual(p.t, p.before, p.staged, "commit and rollback evidence must follow real staged steering writes, not a vacuous no-op")
	old := inboxAckPostgresPhase(p.t, ctx, tx, p.fixture.orgID, p.oldPhase.ID)
	expectedOld := p.oldPhase
	if !p.repaired {
		at := p.at.In(p.transactionTime.Location())
		expectedOld.Status, expectedOld.BoundaryReason = models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundarySteered
		expectedOld.CompletedAt, expectedOld.UpdatedAt = &at, p.transactionTime
	}
	require.Equal(p.t, expectedOld, old, "the complete original phase must either close exactly once or retain its already-committed completion during historical repair")
	rows, err := tx.Query(ctx, "SELECT "+activityPhaseColumns+" FROM session_activity_phases WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND status='running'", p.fixture.orgID, p.fixture.sessionID, p.fixture.threadID)
	require.NoError(p.t, err, "read the staged replacement phase in the actual atomic transaction")
	phase, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	require.NoError(p.t, err, "the staged replacement must be the only running phase")
	first, last, at := int64(1), int64(2), p.at.In(p.transactionTime.Location())
	expectedPhase := models.SessionActivityPhase{ID: phase.ID, OrgID: p.fixture.orgID, SessionID: p.fixture.sessionID, ThreadID: p.fixture.threadID, TurnNumber: 2, PhaseNumber: 2, Status: models.ActivityPhaseStatusRunning, StartedAt: at, RuntimeID: &p.fixture.runtimeID, TriggerKind: models.ActivityPhaseTriggerInboxBatch, TriggerBatchID: phase.TriggerBatchID, TriggerSequenceStart: &first, TriggerSequenceEnd: &last, CreatedAt: p.transactionTime, UpdatedAt: p.transactionTime}
	require.Equal(p.t, expectedPhase, phase, "the complete replacement phase must retain exact scope, runtime, turn, next phase number, timestamp and inbox trigger")
	require.NotNil(p.t, phase.TriggerBatchID, "the replacement must be attributed to its exact durable steering receipt")
	batch, err := NewSessionActivityPhaseStore(tx).GetInboxDeliveryBatch(ctx, p.fixture.orgID, *phase.TriggerBatchID)
	require.NoError(p.t, err, "reload the exact started steering receipt inside the outer transaction")
	require.Equal(p.t, []int64{1, 2}, []int64{batch.SequenceStart, batch.SequenceEnd}, "the started receipt must cover exactly the positively confirmed contiguous range")
	require.Equal(p.t, []uuid.UUID{p.fixture.orgID, p.fixture.sessionID, p.fixture.threadID, p.fixture.runtimeID}, []uuid.UUID{batch.OrgID, batch.SessionID, batch.ThreadID, batch.RuntimeID}, "the started receipt must retain every original scope and owner identity")
	require.Equal(p.t, models.InboxDeliveryBatchStarted, batch.Status, "acknowledgment and replacement phase must start the same receipt")
	require.Equal(p.t, &at, batch.StartedAt, "the receipt's staged start time must match the replacement phase")
	require.Nil(p.t, batch.AbandonedAt, "historical repair must clear only the exact receipt's abandonment")
	var delivered, acknowledged, entries, applied int64
	require.NoError(p.t, tx.QueryRow(ctx, "SELECT last_delivered_sequence,last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2", p.fixture.orgID, p.fixture.runtimeID).Scan(&delivered, &acknowledged), "read both runtime watermarks before the actual outer commit")
	require.Equal(p.t, []int64{2, 2}, []int64{delivered, acknowledged}, "the same atomic transaction must retain delivery and advance acknowledgment to the exact confirmed end")
	require.NoError(p.t, tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE delivery_state='acked' AND applied_at=$4 AND runtime_id=$5)
		FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, p.fixture.orgID, p.fixture.sessionID, p.fixture.threadID, p.at, p.fixture.runtimeID).Scan(&entries, &applied), "inspect the exact acknowledged and phase-applied entries before settlement")
	require.Equal(p.t, []int64{2, 2}, []int64{entries, applied}, "every and only confirmed input must become acknowledged and applied with the replacement phase")
	if p.failCommit {
		return errors.New("synthetic outer steering commit failure")
	}
	return nil
}

func TestInboxAckAtomicTransactionFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	for _, outcome := range []string{"commit", "outer error", "late phase failure", "started replay", "replay range mismatch", "abandoned repair", "abandoned range mismatch", "missing confirmation", "duplicate confirmation", "uncertain input", "foreign lease"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			fixture, old := seedInboxAckTransactionPostgres(t, ctx, pool)
			at := time.Now().UTC().Truncate(time.Microsecond)
			if outcome == "started replay" || outcome == "replay range mismatch" {
				_, _, _, err := NewSessionActivityPhaseStore(pool).AcknowledgeInboxBatchAndStartPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, fixture.runtimeID, fixture.leaseToken, &old.ID, 2, 2, []int64{1, 2}, at)
				require.NoError(t, err, "create the first real committed steering boundary before testing started receipt replay")
			} else if outcome == "abandoned repair" || outcome == "abandoned range mismatch" {
				batch, completed, err := NewSessionActivityPhaseStore(pool).AcknowledgeInboxBatchWithTransition(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, fixture.runtimeID, fixture.leaseToken, &old.ID, 2, at)
				require.NoError(t, err, "create a historical acknowledged steering receipt through the real standalone owner")
				require.NotNil(t, completed, "historical acknowledgment must already have committed its original phase boundary")
				old = *completed
				_, err = NewSessionActivityPhaseStore(pool).AbandonInboxBatch(ctx, fixture.orgID, batch.ID, at)
				require.NoError(t, err, "retain the historical abandonment and uncertain entries before exact positive-receipt repair")
			} else if outcome == "uncertain input" {
				_, err := pool.Exec(ctx, "UPDATE thread_inbox_entries SET delivery_state='unknown_delivery' WHERE org_id=$1 AND thread_id=$2 AND sequence_no=1", fixture.orgID, fixture.threadID)
				require.NoError(t, err, "retain one uncertain entry that positive contiguous receipt validation must not skip")
			}
			if outcome == "late phase failure" {
				installInboxAckLateFailure(t, ctx, pool, fixture, old.ID)
			}
			before := seedActivityTransactionSnapshot(t, ctx, pool, fixture)
			configuration := pool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			ownedPool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open one fixture connection without publishing its DSN")
			t.Cleanup(ownedPool.Close)
			proof := &inboxAckCommitProof{t: t, pool: pool, fixture: fixture, oldPhase: old, before: before, at: at, repaired: outcome == "abandoned repair", replayed: outcome == "started replay", failCommit: outcome == "outer error"}
			starter := &inboxAckStarter{TxStarter: ownedPool, proof: proof}
			confirmed := []int64{2, 1}
			if outcome == "missing confirmation" || outcome == "replay range mismatch" || outcome == "abandoned range mismatch" {
				confirmed = []int64{2}
			} else if outcome == "duplicate confirmation" {
				confirmed = []int64{1, 1}
			}
			token := fixture.leaseToken
			if outcome == "foreign lease" {
				token = uuid.New()
			}
			phase, batch, completed, err := NewSessionActivityPhaseStore(starter).AcknowledgeInboxBatchAndStartPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, fixture.runtimeID, token, &old.ID, 2, 2, confirmed, at)
			require.NotNil(t, starter.owned, "the actual steering wrapper must own one real transaction")
			require.Equal(t, 0, starter.owned.nestedBegins, "the actual atomic caller must never create the former acknowledgment savepoint")
			require.Equal(t, 1, starter.owned.rollbacks, "the atomic owner must retain its original deferred cleanup on every outcome")
			if outcome == "commit" || outcome == "started replay" || outcome == "abandoned repair" {
				require.NoError(t, err, "valid exact steering receipts must complete through one outer transaction")
				require.Equal(t, 1, starter.owned.commits, "the actual atomic owner must commit exactly once")
				require.Equal(t, proof.staged, seedActivityTransactionSnapshot(t, ctx, pool, fixture), "outer commit must expose exactly the full staged phase/inbox/receipt/runtime state")
				require.Equal(t, models.ActivityPhaseStatusRunning, phase.Status, "the accepted result must return the exact running replacement")
				require.Equal(t, phase.TriggerBatchID, &batch.ID, "the returned replacement must reference the returned steering receipt")
				if outcome == "commit" {
					require.NotNil(t, completed, "fresh atomic steering must retain its exact original phase transition")
					require.Equal(t, old.ID, completed.ID, "fresh steering must close only its own current phase")
				} else {
					require.Nil(t, completed, "replayed and repaired historical boundaries must not claim a second old-phase transition")
				}
			} else {
				require.Error(t, err, "invalid receipts and controlled later failures must reject the entire atomic boundary")
				require.Equal(t, models.SessionActivityPhase{}, phase, "failed atomic work must return no accepted replacement phase")
				require.Equal(t, models.ThreadInboxDeliveryBatch{}, batch, "failed atomic work must return no accepted steering receipt")
				require.Nil(t, completed, "failed atomic work must not advertise a committed old-phase transition")
				require.Equal(t, before, seedActivityTransactionSnapshot(t, ctx, pool, fixture), "every rejected outer unit must preserve all original phase, input, receipt and watermark fields")
				switch outcome {
				case "outer error":
					require.EqualError(t, err, "commit atomic steering phase: synthetic outer steering commit failure", "the owner must preserve its original outer Commit error wrapper")
					require.Equal(t, 1, starter.owned.commits, "the outer error must occur only after the complete real staged boundary")
					require.NotEqual(t, before, proof.staged, "outer failure rollback must follow genuine earlier old-phase close, acknowledgment, replacement and watermark writes")
				case "late phase failure":
					var postgresErr *pgconn.PgError
					require.ErrorAs(t, err, &postgresErr, "the late failure must be an actual PostgreSQL replacement-phase rejection")
					require.Equal(t, []string{"23514", "synthetic_late_steering_phase"}, []string{postgresErr.Code, postgresErr.ConstraintName}, "the trigger must confirm real preceding old-phase, receipt, applied-input and watermark writes before rejecting the replacement")
					require.Equal(t, 0, starter.owned.commits, "real phase failure must prevent even an attempted outer commit")
				case "missing confirmation", "duplicate confirmation":
					require.ErrorIs(t, err, ErrInboxBatchNoPhaseTransition, "a new steering range requires every exact positive confirmed sequence")
				case "replay range mismatch", "abandoned range mismatch", "uncertain input":
					require.ErrorIs(t, err, ErrInboxBatchConflict, "historical repair and current input validation must never manufacture missing positive receipts")
				}
			}
		})
	}
}

func seedInboxAckTransactionPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (seedActivityTransactionFixture, models.SessionActivityPhase) {
	t.Helper()
	f := seedActivityTransactionPostgres(t, ctx, pool, false)
	phase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, f.orgID, f.sessionID, f.threadID, 2, &f.runtimeID, &f.leaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, f.requestedAt)
	require.NoError(t, err, "start the genuine original provider phase before live steering")
	var entries []models.ThreadInboxEntry
	for _, messageID := range f.messageIDs {
		entry, err := NewThreadInboxStore(pool).AppendForMessage(ctx, f.orgID, AppendThreadInboxEntryParams{SessionID: f.sessionID, ThreadID: f.threadID, MessageID: messageID, EntryType: models.ThreadInboxEntryTypeUserMessage})
		require.NoError(t, err, "append each live input with real durable inbox identity")
		entries = append(entries, entry)
	}
	claimed, err := NewThreadInboxStore(pool).MarkDeliveringForMessages(ctx, f.orgID, f.threadID, f.runtimeID, f.leaseToken, "synthetic-seed-worker", f.messageIDs)
	require.NoError(t, err, "claim only these live instructions for their exact scoped runtime")
	require.Equal(t, int64(2), claimed, "the real fixture must claim both contiguous input identities")
	for _, entry := range entries {
		changed, err := NewThreadInboxStore(pool).MarkDeliveredForEntry(ctx, f.orgID, f.threadID, f.runtimeID, f.leaseToken, "synthetic-seed-worker", entry.ID, entry.SequenceNo)
		require.NoError(t, err, "persist the legacy provider-write attempt before modeling its positive delivery receipt")
		require.Equal(t, int64(1), changed, "each exact live input must reach delivered state before acknowledgment")
	}
	return f, phase
}

func inboxAckPostgresPhase(t *testing.T, ctx context.Context, conn DBTX, orgID, phaseID uuid.UUID) models.SessionActivityPhase {
	t.Helper()
	rows, err := conn.Query(ctx, "SELECT "+activityPhaseColumns+" FROM session_activity_phases WHERE org_id=$1 AND id=$2", orgID, phaseID)
	require.NoError(t, err, "read the complete exact scoped activity phase")
	phase, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	require.NoError(t, err, "collect all phase fields for exact staging and rollback comparison")
	return phase
}

func installInboxAckLateFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f seedActivityTransactionFixture, oldPhaseID uuid.UUID) {
	t.Helper()
	name := "test_steering_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	// The rejection is reached only after the real old-phase close, receipt
	// allocation/start, entry acknowledgment/application and watermark update.
	query := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
	 IF NEW.org_id='%s'::uuid AND NEW.thread_id='%s'::uuid AND NEW.trigger_kind='inbox_batch' THEN
	  IF EXISTS(SELECT 1 FROM session_activity_phases WHERE org_id=NEW.org_id AND id='%s'::uuid AND status='completed' AND boundary_reason='steered')
	   AND (SELECT count(*) FROM thread_inbox_entries WHERE org_id=NEW.org_id AND thread_id=NEW.thread_id AND delivery_state='acked' AND applied_at IS NOT NULL)=2
	   AND EXISTS(SELECT 1 FROM thread_inbox_delivery_batches WHERE org_id=NEW.org_id AND id=NEW.trigger_batch_id AND status='started')
	   AND EXISTS(SELECT 1 FROM thread_runtimes WHERE org_id=NEW.org_id AND id=NEW.runtime_id AND last_acked_sequence=2)
	  THEN RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='synthetic_late_steering_phase',MESSAGE='synthetic late replacement failure';
	  ELSE RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='synthetic_steering_fixture_incomplete',MESSAGE='synthetic earlier writes missing';
	  END IF;
	 END IF;
	 RETURN NEW;
	END $$;
	CREATE TRIGGER %s BEFORE INSERT ON session_activity_phases FOR EACH ROW EXECUTE FUNCTION %s()`, identifier, f.orgID, f.threadID, oldPhaseID, identifier, identifier)
	_, err := pool.Exec(ctx, query)
	require.NoError(t, err, "install a narrowly scoped real late-failure check only inside the generated test database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(ctx, "DROP TRIGGER "+identifier+" ON session_activity_phases; DROP FUNCTION "+identifier+"()")
		require.NoError(t, err, "remove only this generated fixture's late-failure instrumentation")
	})
}
