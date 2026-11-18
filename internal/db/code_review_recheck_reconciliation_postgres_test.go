package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRecheckReconciliationFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	tests := []struct {
		name       string
		reconciled bool
		commit     bool
	}{
		{name: "cancelled commit", reconciled: true, commit: true},
		{name: "failed commit", reconciled: true, commit: true},
		{name: "caller rollback", reconciled: true},
		{name: "exact idle replay", reconciled: true, commit: true},
		{name: "foreign org"},
		{name: "missing receipt"},
		{name: "nonterminal receipt"},
		{name: "owner released"},
		{name: "runtime not drained"},
		{name: "successor turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedRecheckReconciliation(t, ctx, pool, tt.name)
			before := recheckReconciliationRows(t, ctx, pool, fixture)
			callerPool := newRecheckReconciliationCallerPool(t, ctx, pool)
			owner, err := callerPool.Begin(ctx)
			require.NoError(t, err, "the caller alone occupies its pool's only connection")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			var stamp json.RawMessage
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current(),to_jsonb(transaction_timestamp())").Scan(&transactionID, &stamp), "capture the exact outer transaction and its SQL now timestamp")
			orgID, assessmentID := fixture.OrgID, fixture.AssessmentID
			if tt.name == "foreign org" {
				orgID = uuid.New()
			} else if tt.name == "missing receipt" {
				assessmentID = uuid.New()
			}
			reconciled, err := NewCodeReviewRecheckStore(callerPool).ReconcileDrainedTerminalTurnInTx(ctx, orgID, tx, assessmentID)
			require.NoError(t, err, "the supplied transaction must handle exact no-op and successful reconciliation branches")
			require.Equal(t, tt.reconciled, reconciled, "only the same terminal, physically drained conversation may be reconciled")
			require.Empty(t, tx.settlements, "reconciliation must never Begin, create a savepoint, Commit or Rollback")
			var afterID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterID), "the outer transaction must remain usable after reconciliation")
			require.Equal(t, transactionID, afterID, "the caller retains the exact original transaction")
			require.Equal(t, before, recheckReconciliationRows(t, ctx, pool, fixture), "independent readers must see exactly the original complete graph before settlement")
			staged := recheckReconciliationRows(t, ctx, owner, fixture)
			if tt.reconciled {
				requireRecheckReconciliationGraph(t, before, staged, stamp, fixture.ExpectedTurn, tt.name != "exact idle replay")
			} else {
				require.Equal(t, before, staged, "foreign scope, active runtime, missing owner and successor turn must not even stage a mutation")
			}
			if !tt.commit {
				require.NoError(t, owner.Rollback(ctx), "only the caller may reject its complete staged reconciliation unit")
				require.Equal(t, before, recheckReconciliationRows(t, ctx, pool, fixture), "outer rollback must retain every original root, thread and domain row")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the outer caller makes reconciliation durable")
			require.Equal(t, staged, recheckReconciliationRows(t, ctx, pool, fixture), "commit must expose exactly the earlier staged graph with no independent writes")
		})
	}
}

func TestRecheckReconciliationSchedulingOwnerFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	_, err := pool.Exec(context.Background(), `CREATE TABLE synthetic_recheck_owner_rejections(pull_request_id uuid PRIMARY KEY,session_id uuid NOT NULL,thread_id uuid NOT NULL,expected_turn integer NOT NULL);
CREATE FUNCTION synthetic_recheck_owner_reject() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected synthetic_recheck_owner_rejections%ROWTYPE;
BEGIN
 SELECT * INTO expected FROM synthetic_recheck_owner_rejections WHERE pull_request_id=NEW.pull_request_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM sessions WHERE org_id=NEW.org_id AND id=expected.session_id AND status='idle' AND code_review_owner_pr_id=NEW.pull_request_id)
 OR NOT EXISTS(SELECT 1 FROM session_threads WHERE org_id=NEW.org_id AND id=expected.thread_id AND session_id=expected.session_id AND status='idle' AND current_turn=expected.expected_turn AND completed_at=transaction_timestamp())
 THEN RAISE EXCEPTION 'synthetic owner probe did not observe both earlier reconciliation writes'; END IF;
 RAISE EXCEPTION 'synthetic late reconciliation owner failure';
END $$;
CREATE TRIGGER synthetic_recheck_owner_reject BEFORE UPDATE ON code_review_pr_state FOR EACH ROW EXECUTE FUNCTION synthetic_recheck_owner_reject();`)
	require.NoError(t, err, "install a generated-database-only final owner-write failure with nonvacuous root and thread assertions")
	for _, tt := range []struct {
		name string
		fail bool
	}{{name: "confirmed commit"}, {name: "late owner failure", fail: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedRecheckReconciliation(t, ctx, pool, "cancelled commit")
			var repoID, prID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, "SELECT repository_id,pull_request_id FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.AssessmentID).Scan(&repoID, &prID), "resolve this exact synthetic assessment's real domain ownership")
			_, err := pool.Exec(ctx, "INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id) VALUES($1,$2,$3)", fixture.OrgID, repoID, prID)
			require.NoError(t, err, "seed the existing scheduler row before capturing exact rollback state")
			if tt.fail {
				_, err := pool.Exec(ctx, "INSERT INTO synthetic_recheck_owner_rejections VALUES($1,$2,$3,$4)", prID, fixture.SessionID, fixture.ThreadID, fixture.ExpectedTurn)
				require.NoError(t, err, "scope the genuine late owner SQL failure to this test's exact conversation")
			}
			before := recheckReconciliationRows(t, ctx, pool, fixture)
			var scheduleBefore json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, "SELECT to_jsonb(s) FROM code_review_pr_state s WHERE org_id=$1 AND pull_request_id=$2", fixture.OrgID, prID).Scan(&scheduleBefore), "capture every original scheduling column")
			callerPool := newRecheckReconciliationCallerPool(t, ctx, pool)
			starter := &recheckPersistenceStarter{Pool: callerPool}
			schedules := NewCodeReviewScheduleStore(starter)
			publications := newRecheckReconciliationPublications(t, pool, fixture)
			schedules.SetStreams(publications.streams, zerolog.Nop())
			var staged, stamp json.RawMessage
			err = schedules.WithLockedPR(ctx, fixture.OrgID, repoID, prID, func(tx pgx.Tx, _ *models.CodeReviewPRState) error {
				stampErr := tx.QueryRow(ctx, "SELECT to_jsonb(transaction_timestamp())").Scan(&stamp)
				require.NoError(t, stampErr, "the real scheduler owns the root transaction timestamp")
				reconciled, err := NewCodeReviewRecheckStore(tx).ReconcileDrainedTerminalTurnInTx(ctx, fixture.OrgID, tx, fixture.AssessmentID)
				if err != nil {
					return err
				}
				require.True(t, reconciled, "the actual scheduling owner callback must stage its drained terminal conversation")
				staged = recheckReconciliationRows(t, ctx, tx, fixture)
				requireRecheckReconciliationGraph(t, before, staged, stamp, fixture.ExpectedTurn, true)
				require.Equal(t, before, recheckReconciliationRows(t, ctx, pool, fixture), "even both staged reconciliation writes remain invisible before the owner settles")
				require.Empty(t, publications.snapshot(), "an enabled real scheduling publisher must remain silent inside the callback")
				return nil
			})
			require.Equal(t, 1, starter.begins, "WithLockedPR must be the sole transaction owner")
			require.Zero(t, starter.tx.nestedBegins, "the real locked-PR callback may not create even a savepoint")
			after := recheckReconciliationRows(t, ctx, pool, fixture)
			if tt.fail {
				require.ErrorContains(t, err, "synthetic late reconciliation owner failure", "the final scheduling write must fail only after the trigger observes both earlier staged mutations")
				require.NotEqual(t, before, staged, "late failure must roll back real earlier writes, not pass as an early no-op")
				require.Equal(t, before, after, "the real owner's late failure must roll back the root and thread together")
				var scheduleAfter json.RawMessage
				require.NoError(t, pool.QueryRow(ctx, "SELECT to_jsonb(s) FROM code_review_pr_state s WHERE org_id=$1 AND pull_request_id=$2", fixture.OrgID, prID).Scan(&scheduleAfter), "reload the complete scheduler row after its genuine SQL error")
				require.Equal(t, scheduleBefore, scheduleAfter, "late failure must preserve every scheduling column as well as the conversation")
				require.Equal(t, 0, starter.tx.commits, "a failed owner write must never try to commit")
				require.Equal(t, 1, starter.tx.rollbacks, "the scheduler must abort its entire failed unit exactly once")
				require.Empty(t, publications.snapshot(), "failed outer settlement must not publish a scheduling update")
				return
			}
			require.NoError(t, err, "the existing scheduling owner must commit the complete reconciliation sequence")
			require.Equal(t, staged, after, "the owner commit must expose exactly the graph staged in its callback")
			require.Equal(t, 1, starter.tx.commits, "only the scheduler commits the complete unit")
			events := publications.snapshot()
			require.Len(t, events, 1, "the unchanged scheduler publication policy emits exactly one update after a confirmed owner commit")
			expected := models.CodeReviewUpdatedEvent{OrgID: fixture.OrgID, PullRequestID: &prID, SchedulingState: models.CodeReviewScheduleIdle, UpdatedAt: events[0].event.UpdatedAt}
			require.False(t, expected.UpdatedAt.IsZero(), "the unchanged scheduling event must retain its observation timestamp")
			require.Equal(t, expected, events[0].event, "the existing scheduler event shape must remain exact without a new lifecycle event")
			require.Equal(t, "143:stream:{org:"+fixture.OrgID.String()+"}:code_reviews", events[0].channel, "the existing scheduling event must remain scoped to the same tenant channel")
			require.NoError(t, events[0].err, "the enabled publisher must be observed without query or decoding errors")
			require.Equal(t, after, events[0].visible, "publication must observe the already committed root and thread from an independent connection")
		})
	}
}

func seedRecheckReconciliation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, variant string) models.CodeReviewRecheckTurnCompletion {
	t.Helper()
	fixture := seedRecheckPersistenceCompletion(t, ctx, pool, "head only")
	status := "cancelled"
	if variant == "failed commit" {
		status = "failed"
	}
	_, err := pool.Exec(ctx, "UPDATE code_review_recheck_dispatches SET status=$3,completed_at=now(),failure_detail='synthetic terminal' WHERE org_id=$1 AND assessment_id=$2", fixture.OrgID, fixture.AssessmentID, status)
	require.NoError(t, err, "seed this exact terminal receipt without claiming dispatch or admission proof")
	_, err = pool.Exec(ctx, "UPDATE sessions SET status='cancelled',completed_at=now() WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.SessionID)
	require.NoError(t, err, "retain the original physically drained but logically cancelled review parent")
	_, err = pool.Exec(ctx, "UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.ThreadID)
	require.NoError(t, err, "retain the cancelled exact pre-completion thread turn")
	switch variant {
	case "exact idle replay":
		_, err = pool.Exec(ctx, "UPDATE session_threads SET status='idle',current_turn=$3 WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.ThreadID, fixture.ExpectedTurn)
	case "successor turn":
		_, err = pool.Exec(ctx, "UPDATE session_threads SET status='running',current_turn=$3 WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.ThreadID, fixture.ExpectedTurn+1)
	case "runtime not drained":
		_, err = pool.Exec(ctx, "UPDATE sessions SET container_id='synthetic-active-workspace' WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.SessionID)
	case "owner released":
		_, err = pool.Exec(ctx, "UPDATE sessions SET code_review_owner_pr_id=NULL WHERE org_id=$1 AND id=$2", fixture.OrgID, fixture.SessionID)
	case "nonterminal receipt":
		_, err = pool.Exec(ctx, "UPDATE code_review_recheck_dispatches SET status='running',completed_at=NULL WHERE org_id=$1 AND assessment_id=$2", fixture.OrgID, fixture.AssessmentID)
	}
	require.NoError(t, err, "configure only the selected synthetic isolation or exact-turn case")
	return fixture
}

func newRecheckReconciliationCallerPool(t *testing.T, ctx context.Context, source *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	configuration := source.Config()
	configuration.MaxConns, configuration.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	require.True(t, err == nil, "open only the generated fixture's one-connection caller pool without printing a DSN")
	t.Cleanup(pool.Close)
	return pool
}

func recheckReconciliationRows(t *testing.T, ctx context.Context, conn DBTX, fixture models.CodeReviewRecheckTurnCompletion) json.RawMessage {
	t.Helper()
	rows, err := recheckPersistenceRows(ctx, conn, fixture)
	require.NoError(t, err, "capture every column of the complete synthetic root, thread, phase, transcript, workspace, receipt, assessment and job graph")
	return rows
}

func requireRecheckReconciliationGraph(t *testing.T, before, staged, stamp json.RawMessage, turn int, threadChanged bool) {
	t.Helper()
	var expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &expected), "decode all original graph fields for an exact expected-state comparison")
	var session, thread map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expected["session"], &session), "retain every unchanged original session column")
	require.NoError(t, json.Unmarshal(expected["thread"], &thread), "retain every unchanged original thread column")
	session["status"], session["last_activity_at"] = json.RawMessage(`"idle"`), stamp
	var err error
	expected["session"], err = json.Marshal(session)
	require.NoError(t, err, "encode only the two original session reconciliation assignments")
	if threadChanged {
		thread["status"], thread["completed_at"], thread["last_activity_at"] = json.RawMessage(`"idle"`), stamp, stamp
		thread["current_turn"], err = json.Marshal(turn)
		require.NoError(t, err, "encode the receipt's exact expected turn rather than advancing a successor")
		expected["thread"], err = json.Marshal(thread)
		require.NoError(t, err, "encode only the original exact-turn thread reconciliation assignments")
	}
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode the complete expected graph including every untouched domain and transcript row")
	require.JSONEq(t, string(encoded), string(staged), "reconciliation must change exactly the existing status, turn and transaction-timestamp fields, retaining all domain authority and unrelated data")
}

type recheckReconciliationPublication struct {
	channel string
	event   models.CodeReviewUpdatedEvent
	visible json.RawMessage
	err     error
}

type recheckReconciliationPublications struct {
	streams *cache.CodeReviewStreams
	mu      sync.Mutex
	events  []recheckReconciliationPublication
}

func newRecheckReconciliationPublications(t *testing.T, pool *pgxpool.Pool, fixture models.CodeReviewRecheckTurnCompletion) *recheckReconciliationPublications {
	t.Helper()
	redis := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "enable the real scheduling publisher so missing wiring cannot hide an early event")
	t.Cleanup(func() { require.NoError(t, client.Close(), "close this test's enabled Redis publisher") })
	observations := &recheckReconciliationPublications{streams: cache.NewCodeReviewStreams(client, zerolog.Nop())}
	redis.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if !strings.EqualFold(command, "publish") {
			return false
		}
		observation := recheckReconciliationPublication{}
		if len(args) != 2 {
			observation.err = errors.New("unexpected synthetic publication argument count")
		} else {
			observation.channel = args[0]
			observation.err = json.Unmarshal([]byte(args[1]), &observation.event)
		}
		if observation.err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			observation.visible, observation.err = recheckPersistenceRows(ctx, pool, fixture)
			cancel()
		}
		observations.mu.Lock()
		observations.events = append(observations.events, observation)
		observations.mu.Unlock()
		return false
	})
	return observations
}

func (p *recheckReconciliationPublications) snapshot() []recheckReconciliationPublication {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recheckReconciliationPublication(nil), p.events...)
}
