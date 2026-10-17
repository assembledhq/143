package agent

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func newThreadDeliveryPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL for live delivery lifecycle tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect disposable PostgreSQL")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close test admin") })
	name := "thread_delivery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err, "create isolated live-delivery database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		require.NoError(t, err, "drop only this isolated database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable PostgreSQL URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve current migration chain")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize full lifecycle migrations")
	require.NoError(t, migration.Up(), "exercise complete current schema")
	sourceErr, dbErr := migration.Close()
	require.NoError(t, sourceErr, "close migration source")
	require.NoError(t, dbErr, "close migration connection")
	config, err := pgxpool.ParseConfig(parsed.String())
	require.NoError(t, err, "parse isolated live delivery pool")
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "open isolated live-delivery pool")
	t.Cleanup(pool.Close)
	return pool
}

func seedThreadDeliverySession(t *testing.T, pool *pgxpool.Pool) *models.Session {
	t.Helper()
	orgID := uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, orgID, "delivery-test-"+orgID.String())
	require.NoError(t, err, "create isolated delivery tenant")
	session := &models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, db.NewSessionStore(pool).Create(context.Background(), session), "create durable primary thread")
	return session
}

func appendDeliveryUser(t *testing.T, pool *pgxpool.Pool, session *models.Session, content string) models.ThreadInboxEntry {
	t.Helper()
	message := &models.SessionMessage{OrgID: session.OrgID, SessionID: session.ID, ThreadID: session.PrimaryThreadID, Role: models.MessageRoleUser, TurnNumber: 1, Content: content}
	require.NoError(t, db.NewSessionMessageStore(pool).Create(context.Background(), message), "append synthetic user input")
	entry, err := db.NewThreadInboxStore(pool).AppendForMessage(context.Background(), session.OrgID, db.AppendThreadInboxEntryParams{SessionID: session.ID, ThreadID: *session.PrimaryThreadID, MessageID: message.ID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: []byte(`{"content":"` + content + `"}`)})
	require.NoError(t, err, "accept exact user input in durable inbox")
	return entry
}

type faultingDeliveryRuntimeStore struct {
	*db.ThreadRuntimeStore
	failures atomic.Int32
}

func (s *faultingDeliveryRuntimeStore) CommitInboxDeliveryWithLease(ctx context.Context, orgID, runtimeID, token, threadID uuid.UUID, node string, delivered, acked int64, confirmed []int64) (bool, error) {
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return false, errors.New("transient acknowledgment failure")
	}
	return s.ThreadRuntimeStore.CommitInboxDeliveryWithLease(ctx, orgID, runtimeID, token, threadID, node, delivered, acked, confirmed)
}

type faultingDeliveryPhaseStore struct {
	*db.SessionActivityPhaseStore
	failures atomic.Int32
}

func (s *faultingDeliveryPhaseStore) AcknowledgeInboxBatchAndStartPhase(ctx context.Context, orgID, sessionID, threadID, runtimeID, token uuid.UUID, phaseID *uuid.UUID, turn int, end int64, confirmed []int64, at time.Time) (models.SessionActivityPhase, models.ThreadInboxDeliveryBatch, *models.SessionActivityPhase, error) {
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return models.SessionActivityPhase{}, models.ThreadInboxDeliveryBatch{}, nil, errors.New("transient phase acknowledgment failure")
	}
	return s.SessionActivityPhaseStore.AcknowledgeInboxBatchAndStartPhase(ctx, orgID, sessionID, threadID, runtimeID, token, phaseID, turn, end, confirmed, at)
}

func TestThreadDeliveryPostgresConfirmedWritesRecoverWithoutReplayingInput(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	for _, tt := range []struct {
		name                                                   string
		failures                                               int32
		cancelCaller, laterPoll, closeAfter, interleave, phase bool
	}{
		{name: "one transient acknowledgment", failures: 1},
		{name: "later poll retries only acknowledgment", failures: 2, laterPoll: true},
		{name: "successful write survives canceled caller", cancelCaller: true},
		{name: "closure persists successful write", failures: 2, closeAfter: true},
		{name: "canceled delivery interleaves with Close", failures: 2, cancelCaller: true, closeAfter: true, interleave: true},
		{name: "phase acknowledgment recovers on later poll", failures: 2, laterPoll: true, phase: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			session := seedThreadDeliverySession(t, pool)
			threadID := *session.PrimaryThreadID
			runtimeStore := &faultingDeliveryRuntimeStore{ThreadRuntimeStore: db.NewThreadRuntimeStore(pool)}
			phaseStore := &faultingDeliveryPhaseStore{SessionActivityPhaseStore: db.NewSessionActivityPhaseStore(pool)}
			if tt.phase {
				phaseStore.failures.Store(tt.failures)
			} else {
				runtimeStore.failures.Store(tt.failures)
			}
			registry := NewThreadCancelRegistry(zerolog.Nop())
			registry.Register(threadID, func() {})
			orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: runtimeStore, ThreadInbox: db.NewThreadInboxStore(pool), ThreadCancels: registry, NodeID: "test-worker", Logger: zerolog.Nop()})
			if tt.phase {
				orch.activityPhases = NewActivityPhaseService(phaseStore, zerolog.Nop())
			}
			control, err := orch.startThreadRuntimeControl(context.Background(), session, threadID, &Sandbox{ID: "test-container"}, nil, zerolog.Nop())
			require.NoError(t, err, "create exact durable runtime owner")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writeStarted, releaseWrite := make(chan struct{}), make(chan struct{})
			handle := &orderedInboxWriteHandle{threadCancelTestHandle: newThreadCancelTestHandle()}
			var writes atomic.Int32
			handle.onWrite = func(ctx context.Context, data []byte) error {
				writes.Add(1)
				if tt.interleave {
					close(writeStarted)
					<-releaseWrite
				}
				err := handle.threadCancelTestHandle.WriteInput(ctx, data)
				if tt.cancelCaller {
					cancel()
				}
				return err
			}
			control.Attacher(registry).Attach(handle)
			if tt.phase {
				execution, err := newActivityPhaseExecution(context.Background(), orch.activityPhases, zerolog.Nop(), session.OrgID, session.ID, threadID, 1, &control.runtime.ID, &control.runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial})
				require.NoError(t, err, "start real activity phase before live steering")
				orch.activeActivityPhases.Store(threadID, execution)
			}
			first := appendDeliveryUser(t, pool, session, "first input")
			delivered := make(chan error, 1)
			go func() { delivered <- orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID) }()
			closed := make(chan struct{})
			if tt.interleave {
				<-writeStarted
				go func() {
					control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
					close(closed)
				}()
				cancel()
				close(releaseWrite)
			}
			err = <-delivered
			if tt.failures >= 2 {
				require.Error(t, err, "exhausted immediate retry should retain confirmation for later reconciliation")
			} else {
				require.NoError(t, err, "transient or canceled acknowledgment should finish on detached retry")
			}
			if tt.laterPoll {
				second := appendDeliveryUser(t, pool, session, "second input")
				require.NoError(t, orch.DeliverThreadInbox(context.Background(), session.OrgID, session.ID, threadID), "next poll should reconcile confirmed input before processing later input")
				var state string
				require.NoError(t, pool.QueryRow(context.Background(), `SELECT delivery_state FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, second.ID).Scan(&state), "reload later user input")
				require.Equal(t, "acked", state, "later input should progress after successful acknowledgment retry")
			}
			if tt.closeAfter && !tt.interleave {
				control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
			}
			if tt.interleave {
				<-closed
			}
			var state string
			var applied bool
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, first.ID).Scan(&state, &applied), "reload exact confirmed user input")
			require.Equal(t, "acked", state, "confirmed successful input must not become unknown solely because its first acknowledgment failed")
			require.True(t, applied, "successful acknowledgment retry must preserve applied input")
			expectedWrites := int32(1)
			expectedBytes := "first input\n"
			if tt.laterPoll {
				expectedWrites = 2
				expectedBytes += "second input\n"
			}
			require.Equal(t, expectedWrites, writes.Load(), "acknowledgment retries must never replay provider input")
			require.Equal(t, []byte(expectedBytes), handle.StdinBuffer(), "provider must receive each distinct instruction exactly once")
			if !tt.closeAfter {
				control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
			}
		})
	}
}

func TestThreadDeliveryPostgresPollerWaitsForAdmissionAndHandle(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	session := seedThreadDeliverySession(t, pool)
	threadID := *session.PrimaryThreadID
	seed := appendDeliveryUser(t, pool, session, "initial seed")
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: db.NewThreadRuntimeStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), ThreadCancels: registry, NodeID: "test-worker", Logger: zerolog.Nop()})
	orch.activityPhases = NewActivityPhaseService(db.NewSessionActivityPhaseStore(pool), zerolog.Nop())
	control, err := orch.startThreadRuntimeControl(context.Background(), session, threadID, &Sandbox{ID: "test-container"}, []int64{seed.MessageID}, zerolog.Nop())
	require.NoError(t, err, "create starting owner with claimed initial seed")
	queued := appendDeliveryUser(t, pool, session, "queued after seed")
	blocker, err := pool.Begin(context.Background())
	require.NoError(t, err, "hold seed admission before provider startup")
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = blocker.Exec(context.Background(), `SELECT id FROM session_threads WHERE org_id=$1 AND id=$2 FOR UPDATE`, session.OrgID, threadID)
	require.NoError(t, err, "serialize initial admission behind thread row")
	type admissionResult struct {
		execution *activityPhaseExecution
		err       error
	}
	admission := make(chan admissionResult, 1)
	go func() {
		execution, err := newSeedActivityPhaseExecution(context.Background(), orch.activityPhases, zerolog.Nop(), session.OrgID, session.ID, threadID, 1, control.runtime.ID, control.runtime.LeaseToken, control.seedMessageIDs)
		admission <- admissionResult{execution, err}
	}()
	completedPolls := make(chan struct{}, 1)
	failures := make(chan error, 1)
	stop := control.StartInboxPoller(context.Background(), 5*time.Millisecond, func(ctx context.Context) error {
		// Stopping the test poller must join an in-flight database operation,
		// rather than cancel that operation and record shutdown as a failure.
		pollCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := orch.DeliverThreadInbox(pollCtx, session.OrgID, session.ID, threadID)
		if err != nil {
			select {
			case failures <- err:
			default:
			}
		}
		select {
		case completedPolls <- struct{}{}:
		default:
		}
		return err
	})
	defer stop()
	waitForPoll := func() {
		t.Helper()
		select {
		case <-completedPolls:
		case <-time.After(5 * time.Second):
			t.Fatal("poller should complete delivery while the test owns its admission boundary")
		}
	}
	waitForPoll()
	waitForPoll()
	var state string
	var delivered int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT delivery_state FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, seed.ID).Scan(&state), "read seed during concurrent pre-start polls")
	require.Equal(t, "delivering", state, "pre-attachment polling must not write an uncertain seed delivery marker")
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT last_delivered_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2`, session.OrgID, control.runtime.ID).Scan(&delivered), "read pre-start cursor")
	require.Equal(t, int64(0), delivered, "pre-start polling must not advance provider attempt cursor")
	require.NoError(t, blocker.Commit(context.Background()), "allow initial atomic seed admission")
	result := <-admission
	require.NoError(t, result.err, "initial admission must succeed despite concurrent polling")
	control.seedMessageIDs = nil
	orch.activeActivityPhases.Store(threadID, result.execution)
	handle := newThreadCancelTestHandle()
	control.Attacher(registry).Attach(handle)
	deliveryDeadline := time.NewTimer(5 * time.Second)
	defer deliveryDeadline.Stop()
delivery:
	for {
		select {
		case <-completedPolls:
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT delivery_state FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, queued.ID).Scan(&state), "read queued input after a completed poll")
			if state == "acked" {
				break delivery
			}
		case <-deliveryDeadline.C:
			t.Fatal("poller should deliver queued input after initial admission and handle attachment")
		}
	}
	stop()
	select {
	case err := <-failures:
		require.NoError(t, err, "pre-start polling must never corrupt initial seed admission")
	default:
	}
	require.Equal(t, []byte("queued after seed\n"), handle.StdinBuffer(), "initial seed must never be reinjected as live stdin")
	control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
}

func TestThreadDeliveryPostgresCanceledDefiniteRejectionRemainsReplayable(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	session := seedThreadDeliverySession(t, pool)
	threadID := *session.PrimaryThreadID
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	orch := NewOrchestrator(OrchestratorConfig{ThreadRuntimes: db.NewThreadRuntimeStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), ThreadCancels: registry, NodeID: "test-worker", Logger: zerolog.Nop()})
	control, err := orch.startThreadRuntimeControl(context.Background(), session, threadID, &Sandbox{ID: "test-container"}, nil, zerolog.Nop())
	require.NoError(t, err, "create live-delivery owner")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &orderedInboxWriteHandle{threadCancelTestHandle: newThreadCancelTestHandle(), onWrite: func(context.Context, []byte) error { cancel(); return ErrInputNotOpen }}
	control.Attacher(registry).Attach(handle)
	entry := appendDeliveryUser(t, pool, session, "closed stdin input")
	require.NoError(t, orch.DeliverThreadInbox(ctx, session.OrgID, session.ID, threadID), "definite rejection reset should survive canceled poll context")
	control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
	var state string
	var unowned, unapplied bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT delivery_state,runtime_id IS NULL,applied_at IS NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, session.OrgID, entry.ID).Scan(&state, &unowned, &unapplied), "reload exact never-sent input after closure")
	require.Equal(t, "pending", state, "canceled definite rejection must not become unknown delivery")
	require.True(t, unowned, "never-sent input must remain claimable by continuation")
	require.True(t, unapplied, "never-sent input must not be marked applied")
	require.Empty(t, handle.StdinBuffer(), "definite rejection must send no provider bytes")
}

func TestThreadDeliveryPostgresRestartInputAdmission(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	tests := []struct {
		name                      string
		retry, duplicate, unknown bool
		expected                  []int64
		alreadyApplied            bool
	}{
		{name: "ordinary continuation skips applied history", expected: []int64{2}},
		{name: "checkpoint retry retains applied task only as context", retry: true, expected: []int64{}},
		{name: "applied queued ID ignores newer pending input", duplicate: true, expected: []int64{}, alreadyApplied: true},
		{name: "ordinary unknown delivery remains blocked", unknown: true, expected: []int64{1}},
		{name: "retry purpose cannot replay unknown delivery", retry: true, unknown: true, expected: []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			first := appendDeliveryUser(t, pool, session, "preserved user task")
			orch := NewOrchestrator(OrchestratorConfig{SessionMessages: db.NewSessionMessageStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), ThreadRuntimes: db.NewThreadRuntimeStore(pool), NodeID: "test-worker", Logger: zerolog.Nop()})
			if tt.unknown {
				_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='unknown_delivery' WHERE org_id=$1 AND id=$2`, session.OrgID, first.ID)
				require.NoError(t, err, "seed historical uncertain delivery without applied proof")
			} else {
				control, err := orch.startThreadRuntimeControl(ctx, session, *session.PrimaryThreadID, &Sandbox{ID: uuid.NewString()}, []int64{first.MessageID}, zerolog.Nop())
				require.NoError(t, err, "create original task owner for exact applied-runtime evidence")
				_, _, err = db.NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, session.OrgID, session.ID, *session.PrimaryThreadID, 1, control.runtime.ID, control.runtime.LeaseToken, []int64{first.MessageID}, time.Now())
				require.NoError(t, err, "admit original task under its exact runtime")
				control.seedMessageIDs = nil
				control.Close(ctx, models.ThreadRuntimeStatusClosed, "completed", "")
			}
			if !tt.retry && !tt.unknown {
				appendDeliveryUser(t, pool, session, "new pending input")
			}
			opts := &ContinueSessionOptions{ThreadID: session.PrimaryThreadID}
			if tt.retry {
				opts.ExecutionPurpose = models.SessionExecutionPurposeCheckpointRetry
			}
			if tt.duplicate {
				opts.QueuedMessageID = &first.MessageID
			}
			input, err := orch.resolveContinuationInput(ctx, session, opts)
			require.NoError(t, err, "read scoped retry and queued input without mutating admission")
			require.Equal(t, tt.alreadyApplied, input.alreadyApplied, "only positive applied evidence can skip a duplicate job")
			var sequences []int64
			for _, message := range input.pending {
				if message.ID == first.MessageID {
					sequences = append(sequences, 1)
				} else {
					sequences = append(sequences, 2)
				}
			}
			if len(sequences) == 0 {
				sequences = []int64{}
			}
			require.Equal(t, tt.expected, sequences, "only unadmitted instructions may enter a new seed range")
			if tt.retry {
				require.Equal(t, "preserved user task", input.latest.Content, "explicit retry keeps task context while applied seed IDs remain excluded")
			}
			if tt.unknown {
				control, err := orch.startThreadRuntimeControl(ctx, session, *session.PrimaryThreadID, &Sandbox{ID: uuid.NewString()}, threadRuntimeSeedMessageIDs(input.pending), zerolog.Nop())
				require.NoError(t, err, "create a fenced attempt without claiming unknown input")
				_, _, err = db.NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, session.OrgID, session.ID, *session.PrimaryThreadID, 2, control.runtime.ID, control.runtime.LeaseToken, threadRuntimeSeedMessageIDs(input.pending), time.Now())
				require.ErrorIs(t, err, db.ErrInboxBatchConflict, "neither ordinary execution nor a retry purpose may bypass explicit unknown-delivery recovery")
				control.Close(ctx, models.ThreadRuntimeStatusFailed, "blocked unknown", "")
				var after string
				require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, session.OrgID, session.ID, *session.PrimaryThreadID, first.ID).Scan(&after), "read blocked input after attempt closure")
				require.Equal(t, "unknown_delivery", after, "failed admission and closure preserve uncertainty without replaying input")
			}
		})
	}
}

func TestThreadDeliveryPostgresRecoveryClassifiesActualLaterAssistantReceipt(t *testing.T) {
	t.Parallel()
	pool := newThreadDeliveryPostgresPool(t)
	tests := []struct {
		name                        string
		completed, settled, sibling bool
	}{{name: "unfinished initial task"}, {name: "fully settled final response", completed: true, settled: true}, {name: "settled own turn with running sibling", completed: true, settled: true, sibling: true}, {name: "final response before lifecycle completion", completed: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedThreadDeliverySession(t, pool)
			first := appendDeliveryUser(t, pool, session, "original task")
			runtime, err := db.NewThreadRuntimeStore(pool).CreateStarting(ctx, session.OrgID, db.CreateThreadRuntimeParams{SessionID: session.ID, ThreadID: *session.PrimaryThreadID, SandboxID: session.ID, AgentType: session.AgentType, OwnerNodeID: "test-worker", LeaseToken: uuid.New(), LeaseDuration: time.Minute})
			require.NoError(t, err, "create original owned provider attempt")
			phase, _, err := db.NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, session.OrgID, session.ID, *session.PrimaryThreadID, 1, runtime.ID, runtime.LeaseToken, []int64{first.MessageID}, time.Now())
			require.NoError(t, err, "admit original input before testing response recovery")
			if tt.completed {
				_, err = db.NewSessionActivityPhaseStore(pool).CompletePhase(ctx, session.OrgID, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now())
				require.NoError(t, err, "persist exact terminal provider boundary")
				phaseID := phase.ID
				require.NoError(t, db.NewSessionMessageStore(pool).Create(ctx, &models.SessionMessage{OrgID: session.OrgID, SessionID: session.ID, ThreadID: session.PrimaryThreadID, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Preserved final response", ActivityPhaseID: &phaseID}), "persist real later assistant history after queued input")
			}
			changed, err := db.NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, session.OrgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "ended", "")
			require.NoError(t, err, "retire original runtime without mutating its response")
			require.True(t, changed, "original runtime lease must be owned")
			if tt.settled {
				status := "idle"
				if tt.sibling {
					status = "running"
				}
				_, err = pool.Exec(ctx, `UPDATE sessions SET status=$3,current_turn=1 WHERE org_id=$1 AND id=$2`, session.OrgID, session.ID, status)
				require.NoError(t, err, "settle shared metadata or retain a healthy sibling's running status")
				_, err = pool.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=1,last_activity_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, session.OrgID, *session.PrimaryThreadID)
				require.NoError(t, err, "record own thread completion after exact final response")
			}
			orch := NewOrchestrator(OrchestratorConfig{SessionMessages: db.NewSessionMessageStore(pool), ThreadInbox: db.NewThreadInboxStore(pool), Logger: zerolog.Nop()})
			opts := &ContinueSessionOptions{ThreadID: session.PrimaryThreadID, QueuedMessageID: &first.MessageID, ExecutionPurpose: models.SessionExecutionPurposeCheckpointRecovery, RecoveryRuntimeID: &runtime.ID, RecoveryMessageIDs: []int64{first.MessageID}}
			input, err := orch.resolveContinuationInput(ctx, session, opts)
			if tt.completed && !tt.settled {
				require.ErrorIs(t, err, ErrContinuationRecoveryNeedsReconciliation, "partial lifecycle cannot be acknowledged or re-executed after real final output")
				return
			}
			require.NoError(t, err, "exact original queued boundary must survive later assistant history")
			require.Equal(t, tt.settled, input.alreadyApplied, "only fully settled provider completion is a successful no-op")
			require.Empty(t, input.pending, "applied original input must never become a new seed")
			if !tt.completed {
				require.Equal(t, []int64{first.MessageID}, threadRuntimeSeedMessageIDs(input.recoveryTask), "unfinished task retains original applied context before first checkpoint")
			}
		})
	}
}
