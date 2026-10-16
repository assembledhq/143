package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type failedSteeringPhaseDatabase struct {
	*pgxpool.Pool
	failures atomic.Int32
}

func (s *failedSteeringPhaseDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &failedSteeringPhaseTransaction{Tx: tx, failures: &s.failures}, nil
}

type failedSteeringPhaseTransaction struct {
	pgx.Tx
	failures *atomic.Int32
}

func (s *failedSteeringPhaseTransaction) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &failedSteeringPhaseTransaction{Tx: tx, failures: s.failures}, nil
}

func (s *failedSteeringPhaseTransaction) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "INSERT INTO session_activity_phases") && s.failures.CompareAndSwap(1, 0) {
		return nil, errors.New("injected phase insert failure after acknowledgment")
	}
	return s.Tx.Query(ctx, query, args...)
}

func TestThreadDeliveryPostgresExactLowSequenceReceipts(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	for _, tt := range []struct {
		name      string
		phase     bool
		ambiguous bool
		close     bool
	}{
		{name: "confirmed retry below phase watermark", phase: true},
		{name: "confirmed retry below plain watermark"},
		{name: "ambiguous retry blocks later delivery", ambiguous: true},
		{name: "ambiguous phase retry blocks later delivery", phase: true, ambiguous: true},
		{name: "closure acknowledges only exact confirmed receipt", ambiguous: true, close: true},
		{name: "phase closure acknowledges only exact confirmed receipt", phase: true, ambiguous: true, close: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			threadID := *session.PrimaryThreadID
			first := appendDeliveryUser(t, pool, session, "retried low input")
			prior := appendDeliveryUser(t, pool, session, "prior handled input")
			registry := NewThreadCancelRegistry(zerolog.Nop())
			registry.Register(threadID, func() {})
			runtimeStore := &faultingDeliveryRuntimeStore{ThreadRuntimeStore: db.NewThreadRuntimeStore(pool)}
			orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: runtimeStore, ThreadInbox: db.NewThreadInboxStore(pool), ThreadCancels: registry, NodeID: "test-worker", Logger: zerolog.Nop()})
			control, err := orch.startThreadRuntimeControl(ctx, session, threadID, &Sandbox{ID: "receipt-test"}, nil, zerolog.Nop())
			require.NoError(t, err, "create current receipt owner")
			_, err = pool.Exec(ctx, `WITH failed AS (UPDATE thread_inbox_entries SET delivery_state='dead_letter' WHERE org_id=$1 AND id=$2),
handled AS (UPDATE thread_inbox_entries SET delivery_state='acked',runtime_id=$4,applied_at=now(),acked_at=now() WHERE org_id=$1 AND id=$3)
UPDATE thread_runtimes SET last_delivered_sequence=2,last_acked_sequence=2 WHERE org_id=$1 AND id=$4`, session.OrgID, first.ID, prior.ID, control.runtime.ID)
			require.NoError(t, err, "retain a handled terminal prefix with an older recoverable failure")
			_, err = db.NewThreadInboxStore(pool).RetryRecoverable(ctx, session.OrgID, threadID, first.ID, false)
			require.NoError(t, err, "explicit retry reopens only the failed low sequence")
			var execution *activityPhaseExecution
			phaseStore := &faultingDeliveryPhaseStore{SessionActivityPhaseStore: db.NewSessionActivityPhaseStore(pool)}
			if tt.phase {
				orch.activityPhases = NewActivityPhaseService(phaseStore, zerolog.Nop())
				execution, err = newActivityPhaseExecution(ctx, orch.activityPhases, zerolog.Nop(), session.OrgID, session.ID, threadID, 1, &control.runtime.ID, &control.runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial})
				require.NoError(t, err, "start real phase above the reopened sequence")
				orch.activeActivityPhases.Store(threadID, execution)
			}
			handle := &orderedInboxWriteHandle{threadCancelTestHandle: newThreadCancelTestHandle()}
			handle.onWrite = func(writeCtx context.Context, input []byte) error {
				if tt.ambiguous {
					return errors.New("provider acceptance is unknown")
				}
				return handle.threadCancelTestHandle.WriteInput(writeCtx, input)
			}
			control.Attacher(registry).Attach(handle)
			err = orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID)
			if tt.ambiguous {
				require.Error(t, err, "ambiguous acceptance must retain its durable attempt marker")
			} else {
				require.NoError(t, err, "confirmed low sequence must acknowledge without a backwards phase batch")
			}
			later := appendDeliveryUser(t, pool, session, "later confirmed input")
			if tt.close {
				// A retained positive receipt can coexist with older uncertainty
				// after an interrupted legacy attempt; closure must not infer a range.
				_, err = pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='delivered',runtime_id=$3,delivered_at=now() WHERE org_id=$1 AND id=$2`, session.OrgID, later.ID, control.runtime.ID)
				require.NoError(t, err, "retain exact later accepted receipt without altering earlier uncertainty")
				orch.rememberConfirmedThreadInput(control.runtime, threadID, later.SequenceNo)
				if tt.phase {
					phaseStore.failures.Store(2)
				} else {
					runtimeStore.failures.Store(2)
				}
				require.Error(t, orch.flushConfirmedThreadInput(ctx, control.runtime, threadID, false), "failed immediate acknowledgment must retain only the exact confirmed later input")
				control.Close(ctx, models.ThreadRuntimeStatusClosed, "completed", "")
			} else {
				err = orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID)
				require.NoError(t, err, "subsequent polling must either progress confirmed input or preserve the uncertainty barrier")
			}
			type receipt struct {
				State   string
				Applied bool
			}
			read := func(entryID uuid.UUID) receipt {
				t.Helper()
				var result receipt
				require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, entryID).Scan(&result.State, &result.Applied), "read exact input receipt")
				return result
			}
			expectedFirst, expectedLater := receipt{"acked", true}, receipt{"acked", true}
			if tt.ambiguous {
				expectedFirst, expectedLater = receipt{"delivered", false}, receipt{"pending", false}
			}
			if tt.close {
				expectedFirst, expectedLater = receipt{"unknown_delivery", false}, receipt{"acked", true}
			}
			require.Equal(t, expectedFirst, read(first.ID), "scalar watermarks must never bless an uncertain lower input")
			require.Equal(t, expectedLater, read(later.ID), "later input must respect the durable uncertainty barrier")
			if !tt.close {
				control.Close(ctx, models.ThreadRuntimeStatusClosed, "completed", "")
			}
		})
	}
}

func TestThreadDeliveryPostgresTerminalFenceForgetsReceipt(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	for _, tt := range []struct {
		name     string
		replaced bool
	}{
		{name: "terminal runtime"},
		{name: "replacement lease", replaced: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			threadID := *session.PrimaryThreadID
			orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: db.NewThreadRuntimeStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), NodeID: "test-worker", Logger: zerolog.Nop()})
			control, err := orch.startThreadRuntimeControl(ctx, session, threadID, &Sandbox{ID: "fenced-receipt"}, nil, zerolog.Nop())
			require.NoError(t, err, "create owner whose final acknowledgment will be fenced")
			entry := appendDeliveryUser(t, pool, session, "previous owner receipt")
			_, err = pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='delivered',runtime_id=$3 WHERE org_id=$1 AND id=$2`, session.OrgID, entry.ID, control.runtime.ID)
			require.NoError(t, err, "retain old owner's attempted delivery")
			orch.rememberConfirmedThreadInput(control.runtime, threadID, entry.SequenceNo)
			if tt.replaced {
				_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET lease_token=$3 WHERE org_id=$1 AND id=$2`, session.OrgID, control.runtime.ID, uuid.New())
			} else {
				_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET status='closed',closed_at=now() WHERE org_id=$1 AND id=$2`, session.OrgID, control.runtime.ID)
			}
			require.NoError(t, err, "establish positive loss of original runtime authority")
			control.Close(ctx, models.ThreadRuntimeStatusClosed, "completed", "")
			_, retained := orch.confirmedThreadInput.Load(confirmedThreadInputKeyFor(control.runtime, threadID))
			require.False(t, retained, "a terminal lease fence must release the obsolete in-memory receipt")
			var state string
			var applied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, entry.ID).Scan(&state, &applied), "inspect data after fenced cleanup")
			require.Equal(t, "delivered", state, "an obsolete owner must not mutate the successor's delivery state")
			require.False(t, applied, "a fenced receipt cannot claim durable application")
		})
	}
}

func TestThreadDeliveryPostgresAtomicSteeringRejectsForeignOrStaleReceipt(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	for _, name := range []string{"foreign organization", "foreign session", "foreign thread", "wrong lease", "expired lease", "incomplete exact receipts"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			threadID := *session.PrimaryThreadID
			orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: db.NewThreadRuntimeStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), NodeID: "test-worker", Logger: zerolog.Nop()})
			control, err := orch.startThreadRuntimeControl(ctx, session, threadID, &Sandbox{ID: "scoped-steering"}, nil, zerolog.Nop())
			require.NoError(t, err, "create original atomic steering owner")
			phases := db.NewSessionActivityPhaseStore(pool)
			phase, err := phases.StartPhase(ctx, session.OrgID, session.ID, threadID, 1, &control.runtime.ID, &control.runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now())
			require.NoError(t, err, "start original phase before testing authority fences")
			entry := appendDeliveryUser(t, pool, session, "scoped confirmed receipt")
			_, err = db.NewThreadInboxStore(pool).ClaimDeliverableAfter(ctx, session.OrgID, threadID, control.runtime.ID, control.runtime.LeaseToken, "test-worker", 0, 1)
			require.NoError(t, err, "claim exact input for the current runtime")
			_, err = db.NewThreadInboxStore(pool).MarkDeliveredForEntry(ctx, session.OrgID, threadID, control.runtime.ID, control.runtime.LeaseToken, "test-worker", entry.ID, entry.SequenceNo)
			require.NoError(t, err, "retain pre-write attempt while testing acknowledgment scope")
			orgID, sessionID, requestedThread, token := session.OrgID, session.ID, threadID, control.runtime.LeaseToken
			confirmed := []int64{entry.SequenceNo}
			switch name {
			case "foreign organization":
				orgID = uuid.New()
			case "foreign session":
				sessionID = uuid.New()
			case "foreign thread":
				requestedThread = uuid.New()
			case "wrong lease":
				token = uuid.New()
			case "expired lease":
				_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 minute' WHERE org_id=$1 AND id=$2`, session.OrgID, control.runtime.ID)
				require.NoError(t, err, "expire original runtime without granting new authority")
			case "incomplete exact receipts":
				confirmed = []int64{entry.SequenceNo + 1}
			}
			_, _, _, err = phases.AcknowledgeInboxBatchAndStartPhase(ctx, orgID, sessionID, requestedThread, control.runtime.ID, token, &phase.ID, 1, entry.SequenceNo, confirmed, time.Now())
			require.Error(t, err, "a foreign, expired or incomplete receipt cannot transition a provider phase")
			var status, state string
			var applied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM session_activity_phases WHERE org_id=$1 AND id=$2`, session.OrgID, phase.ID).Scan(&status), "inspect legitimate owner's phase after rejected transition")
			require.Equal(t, "running", status, "rejected scope must preserve the original phase")
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, entry.ID).Scan(&state, &applied), "inspect exact input after rejected transition")
			require.Equal(t, "delivered", state, "rejected scope must preserve uncertain pre-write state")
			require.False(t, applied, "rejected scope cannot mark input applied")
		})
	}
}

func TestThreadDeliveryPostgresLateReceiptDoesNotResurrectPhase(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	for _, tt := range []struct {
		name               string
		failure, abandoned bool
	}{
		{name: "receipt after final phase completion"},
		{name: "phase insert failure rolls back acknowledgment", failure: true},
		{name: "historical abandoned batch repairs under exact receipt", abandoned: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			threadID := *session.PrimaryThreadID
			registry := NewThreadCancelRegistry(zerolog.Nop())
			registry.Register(threadID, func() {})
			phaseDatabase := &failedSteeringPhaseDatabase{Pool: pool}
			phaseStore := db.NewSessionActivityPhaseStore(phaseDatabase)
			orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: db.NewThreadRuntimeStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), ThreadCancels: registry, NodeID: "test-worker", Logger: zerolog.Nop()})
			orch.activityPhases = NewActivityPhaseService(phaseStore, zerolog.Nop())
			control, err := orch.startThreadRuntimeControl(ctx, session, threadID, &Sandbox{ID: "late-receipt"}, nil, zerolog.Nop())
			require.NoError(t, err, "create exact late receipt owner")
			execution, err := newActivityPhaseExecution(ctx, orch.activityPhases, zerolog.Nop(), session.OrgID, session.ID, threadID, 1, &control.runtime.ID, &control.runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial})
			require.NoError(t, err, "start original provider phase")
			orch.activeActivityPhases.Store(threadID, execution)
			if tt.failure {
				phaseDatabase.failures.Store(1)
			} else if !tt.abandoned {
				require.NoError(t, execution.complete(ctx, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse), "persist final response before late delivery reconciliation")
			}
			handle := newThreadCancelTestHandle()
			control.Attacher(registry).Attach(handle)
			first := appendDeliveryUser(t, pool, session, "accepted late input")
			if tt.failure || tt.abandoned {
				_, err := db.NewThreadInboxStore(pool).ClaimDeliverableAfter(ctx, session.OrgID, threadID, control.runtime.ID, control.runtime.LeaseToken, "test-worker", 0, 1)
				require.NoError(t, err, "claim exact historical confirmed input")
				_, err = db.NewThreadInboxStore(pool).MarkDeliveredForEntry(ctx, session.OrgID, threadID, control.runtime.ID, control.runtime.LeaseToken, "test-worker", first.ID, first.SequenceNo)
				require.NoError(t, err, "retain durable pre-write attempt for the historical batch")
				require.NoError(t, handle.WriteInput(ctx, []byte("accepted late input\n")), "positively confirm historical input once")
				orch.rememberConfirmedThreadInput(control.runtime, threadID, first.SequenceNo)
				if tt.failure {
					_, _, _, err := phaseStore.AcknowledgeInboxBatchAndStartPhase(ctx, session.OrgID, session.ID, threadID, control.runtime.ID, control.runtime.LeaseToken, execution.phaseID(), 1, first.SequenceNo, []int64{first.SequenceNo}, time.Now())
					require.ErrorContains(t, err, "injected phase insert failure", "fail the replacement insert after real acknowledgment writes")
					var status string
					var batches int
					var acknowledged int64
					require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM session_activity_phases WHERE org_id=$1 AND id=$2`, session.OrgID, *execution.phaseID()).Scan(&status), "read prior phase after failed outer transaction")
					require.Equal(t, "running", status, "failed replacement must roll back closing the old phase")
					require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_delivery_batches WHERE org_id=$1 AND runtime_id=$2`, session.OrgID, control.runtime.ID).Scan(&batches), "read batch admission after rollback")
					require.Zero(t, batches, "failed replacement must not leave acknowledged or abandoned batches")
					require.NoError(t, pool.QueryRow(ctx, `SELECT last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2`, session.OrgID, control.runtime.ID).Scan(&acknowledged), "read runtime cursor after failed replacement")
					require.Zero(t, acknowledged, "failed replacement must roll back acknowledgment cursor advancement")
				} else {
					batch, _, err := phaseStore.AcknowledgeInboxBatchWithTransition(ctx, session.OrgID, session.ID, threadID, control.runtime.ID, control.runtime.LeaseToken, execution.phaseID(), first.SequenceNo, time.Now())
					require.NoError(t, err, "persist the historical acknowledgment before phase-start failure")
					_, err = phaseStore.AbandonInboxBatch(ctx, session.OrgID, batch.ID, time.Now())
					require.NoError(t, err, "preserve the prior implementation's abandoned metadata state")
				}
			}
			require.NoError(t, orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID), "known accepted input must reconcile without reopening a closed or abandoned phase")
			appendDeliveryUser(t, pool, session, "accepted following input")
			require.NoError(t, orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID), "later polls must not remain gated on a permanently abandoned batch")
			if tt.failure || tt.abandoned {
				message := &models.SessionMessage{OrgID: session.OrgID, SessionID: session.ID, ThreadID: &threadID, ActivityPhaseID: execution.phaseID(), TurnNumber: 1, Role: models.MessageRoleAssistant, Content: "provider turn finished"}
				require.NoError(t, execution.persistAssistantAndComplete(ctx, message, models.ActivityPhaseBoundaryFinalResponse), "repaired steering must retain a durable final provider outcome")
			}
			var total, running int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='running') FROM session_activity_phases WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, session.OrgID, session.ID, threadID).Scan(&total, &running), "inspect phase history after late receipt reconciliation")
			expectedTotal := 1
			if tt.failure || tt.abandoned {
				expectedTotal = 3
			}
			require.Equal(t, expectedTotal, total, "only live steering may create replacement phases; a completed final phase stays final")
			require.Zero(t, running, "receipt reconciliation must not resurrect a final phase")
			var applied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, first.ID).Scan(&applied), "read known accepted input after failed metadata transition")
			require.True(t, applied, "confirmed input remains applied despite phase metadata failure")
			require.Equal(t, []byte("accepted late input\naccepted following input\n"), handle.StdinBuffer(), "reconciliation must never repeat provider input")
			control.Close(ctx, models.ThreadRuntimeStatusClosed, "completed", "")
		})
	}
}
