package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestOrchestrator_DeliverThreadInboxWritesEntriesToLiveThreadHandle(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()
	payload, err := json.Marshal(map[string]any{
		"content": "hello live",
	})
	require.NoError(t, err, "test payload should marshal")

	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:                    runtimeID,
			OrgID:                 orgID,
			SessionID:             sessionID,
			ThreadID:              threadID,
			AgentType:             models.AgentTypeClaudeCode,
			Status:                models.ThreadRuntimeStatusLive,
			OwnerNodeID:           "worker-a",
			LeaseToken:            leaseToken,
			LastDeliveredSequence: 0,
			LastAckedSequence:     0,
		},
	}
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{
			{
				ID:            uuid.New(),
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    1,
				MessageID:     22,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       payload,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeInputAdapter{prefix: "provider:"},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err = orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	require.NoError(t, err, "DeliverThreadInbox should deliver pending input to the live handle")
	require.Equal(t, []byte("provider:hello live\n"), handle.StdinBuffer(), "DeliverThreadInbox should write provider-formatted input to stdin")
	require.Equal(t, []markDeliveredEntryCall{{threadID: threadID, runtimeID: runtimeID, entryID: inbox.deliverable[0].ID, sequenceNo: 1}}, inbox.deliveredEntries, "DeliverThreadInbox should mark the exact entry delivered before acking it")
	require.Equal(t, []commitDeliveryCall{{runtimeID: runtimeID, leaseToken: leaseToken, threadID: threadID, delivered: 1, acked: 1}}, runtimes.commitCalls, "DeliverThreadInbox should atomically ack entries and advance runtime cursors with the runtime lease")
}

func TestOrchestrator_DeliverThreadInboxRoutesToRuntimeOwner(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:          uuid.New(),
			OrgID:       orgID,
			SessionID:   sessionID,
			ThreadID:    threadID,
			Status:      models.ThreadRuntimeStatusLive,
			OwnerNodeID: "worker-b",
			LeaseToken:  uuid.New(),
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		ThreadCancels:  NewThreadCancelRegistry(zerolog.Nop()),
		ThreadRuntimes: runtimes,
		ThreadInbox:    &fakeThreadInboxStore{},
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err := orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	var ownerErr *ThreadRuntimeOwnedElsewhereError
	require.ErrorAs(t, err, &ownerErr, "DeliverThreadInbox should return a routable owner error when another worker owns the runtime")
	require.Equal(t, "worker-b", ownerErr.OwnerNodeID, "owner error should expose the worker that owns the live handle")
}

func TestOrchestrator_DeliverThreadInboxLeavesPendingWhenHandleUnavailable(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	payload, err := json.Marshal(map[string]any{
		"content": "will replay later",
	})
	require.NoError(t, err, "test payload should marshal")

	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:          uuid.New(),
			OrgID:       orgID,
			SessionID:   sessionID,
			ThreadID:    threadID,
			AgentType:   models.AgentTypeClaudeCode,
			Status:      models.ThreadRuntimeStatusLive,
			OwnerNodeID: "worker-a",
			LeaseToken:  uuid.New(),
		},
	}
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{
			{
				ID:            uuid.New(),
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    2,
				MessageID:     25,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       payload,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeInputAdapter{prefix: ""},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err = orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	require.NoError(t, err, "DeliverThreadInbox should leave input pending when the local handle is not yet attached")
	require.Empty(t, inbox.deliveredEntries, "DeliverThreadInbox should not mark delivered entries without a handle write")
	require.Empty(t, inbox.ackedThrough, "DeliverThreadInbox should not ack entries without a handle write")
	require.Empty(t, runtimes.commitCalls, "DeliverThreadInbox should not advance cursors when nothing was delivered")
}

func TestOrchestrator_DeliverThreadInboxLeavesPendingWhenAdapterHasNoLiveInputFormatter(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	payload, err := json.Marshal(map[string]any{
		"content": "do not write raw stdin",
	})
	require.NoError(t, err, "test payload should marshal")

	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:          uuid.New(),
			OrgID:       orgID,
			SessionID:   sessionID,
			ThreadID:    threadID,
			AgentType:   models.AgentTypeClaudeCode,
			Status:      models.ThreadRuntimeStatusLive,
			OwnerNodeID: "worker-a",
			LeaseToken:  uuid.New(),
		},
	}
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{
			{
				ID:            uuid.New(),
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    2,
				MessageID:     25,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       payload,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeAdapter{},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err = orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	require.NoError(t, err, "DeliverThreadInbox should leave input pending when the provider has no native live input formatter")
	require.Empty(t, handle.StdinBuffer(), "DeliverThreadInbox should not write raw input to providers without native live input support")
	require.Empty(t, inbox.deliveredEntries, "DeliverThreadInbox should not mark delivered entries without provider-native acceptance")
	require.Empty(t, inbox.ackedThrough, "DeliverThreadInbox should not ack entries without provider-native acceptance")
	require.Empty(t, runtimes.commitCalls, "DeliverThreadInbox should not advance cursors when provider-native delivery is unsupported")
}

func TestOrchestrator_DeliverThreadInboxDeadLettersInvalidPayloadAndContinues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()
	validPayload, err := json.Marshal(map[string]any{"content": "second"})
	require.NoError(t, err, "test payload should marshal")

	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:                    runtimeID,
			OrgID:                 orgID,
			SessionID:             sessionID,
			ThreadID:              threadID,
			AgentType:             models.AgentTypeClaudeCode,
			Status:                models.ThreadRuntimeStatusLive,
			OwnerNodeID:           "worker-a",
			LeaseToken:            leaseToken,
			LastDeliveredSequence: 0,
			LastAckedSequence:     0,
		},
	}
	badEntryID := uuid.New()
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{
			{
				ID:            badEntryID,
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    1,
				MessageID:     30,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       json.RawMessage(`{"content":`),
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
			{
				ID:            uuid.New(),
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    2,
				MessageID:     31,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       validPayload,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeInputAdapter{prefix: ""},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err = orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	require.NoError(t, err, "DeliverThreadInbox should quarantine poison entries and keep delivering later entries")
	require.Equal(t, []deadLetterCall{{threadID: threadID, entryID: badEntryID}}, inbox.deadLetterCalls, "invalid payload should be marked dead-letter for explicit recovery")
	require.Equal(t, []byte("second\n"), handle.StdinBuffer(), "valid entries after a dead-letter should still reach the live handle")
	require.Equal(t, []markDeliveredEntryCall{{threadID: threadID, runtimeID: runtimeID, entryID: inbox.deliverable[1].ID, sequenceNo: 2}}, inbox.deliveredEntries, "valid entries should be marked delivered before commit")
	require.Equal(t, []commitDeliveryCall{{runtimeID: runtimeID, leaseToken: leaseToken, threadID: threadID, delivered: 2, acked: 2}}, runtimes.commitCalls, "DeliverThreadInbox should atomically ack through successfully accepted later entries")
}

func TestOrchestrator_DeliverThreadInboxMarksWriteFailureForRetry(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	payload, err := json.Marshal(map[string]any{"content": "will fail"})
	require.NoError(t, err, "test payload should marshal")

	handle := newThreadCancelTestHandle()
	handle.writeErr = fmt.Errorf("stdin closed")
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:          runtimeID,
			OrgID:       orgID,
			SessionID:   sessionID,
			ThreadID:    threadID,
			AgentType:   models.AgentTypeClaudeCode,
			Status:      models.ThreadRuntimeStatusLive,
			OwnerNodeID: "worker-a",
			LeaseToken:  uuid.New(),
		},
	}
	entryID := uuid.New()
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{{
			ID:            entryID,
			OrgID:         orgID,
			SessionID:     sessionID,
			ThreadID:      threadID,
			SequenceNo:    3,
			MessageID:     33,
			EntryType:     models.ThreadInboxEntryTypeUserMessage,
			Payload:       payload,
			DeliveryState: models.ThreadInboxDeliveryStatePending,
		}},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeInputAdapter{prefix: ""},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	err = orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID)

	require.Error(t, err, "DeliverThreadInbox should surface handle write failures")
	require.Equal(t, []deliveryFailedCall{{threadID: threadID, runtimeID: runtimeID, entryID: entryID}}, inbox.deliveryFailedCalls, "DeliverThreadInbox should record write failures so the row is not stranded as delivering")
	require.Empty(t, inbox.deliveredEntries, "failed writes must not be marked delivered")
	require.Empty(t, runtimes.commitCalls, "failed writes must not advance runtime cursors")
}

func TestOrchestrator_DeliverThreadInboxSerializesConcurrentCalls(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()
	payload, err := json.Marshal(map[string]any{"content": "one"})
	require.NoError(t, err, "test payload should marshal")

	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)
	runtimes := &fakeThreadRuntimeStore{
		active: models.ThreadRuntime{
			ID:          runtimeID,
			OrgID:       orgID,
			SessionID:   sessionID,
			ThreadID:    threadID,
			AgentType:   models.AgentTypeClaudeCode,
			Status:      models.ThreadRuntimeStatusLive,
			OwnerNodeID: "worker-a",
			LeaseToken:  leaseToken,
		},
	}
	var activeLists atomic.Int32
	var concurrentList atomic.Bool
	inbox := &fakeThreadInboxStore{
		deliverable: []models.ThreadInboxEntry{
			{
				ID:            uuid.New(),
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				SequenceNo:    1,
				MessageID:     22,
				EntryType:     models.ThreadInboxEntryTypeUserMessage,
				Payload:       payload,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			},
		},
		listFn: func() {
			if activeLists.Add(1) > 1 {
				concurrentList.Store(true)
			}
			time.Sleep(25 * time.Millisecond)
			activeLists.Add(-1)
		},
	}
	orch := NewOrchestrator(OrchestratorConfig{
		Adapters: map[models.AgentType]AgentAdapter{
			models.AgentTypeClaudeCode: fakeThreadRuntimeInputAdapter{prefix: ""},
		},
		ThreadCancels:  registry,
		ThreadRuntimes: runtimes,
		ThreadInbox:    inbox,
		NodeID:         "worker-a",
		Logger:         zerolog.Nop(),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			require.NoError(t, orch.DeliverThreadInbox(context.Background(), orgID, sessionID, threadID), "concurrent delivery should not fail")
		}()
	}
	wg.Wait()

	require.False(t, concurrentList.Load(), "DeliverThreadInbox should serialize delivery per thread to avoid duplicate handle writes")
}

func TestThreadRuntimeHandleAttacher_MarksRuntimeLiveAndAttachesHandle(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()
	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	runtimes := &fakeThreadRuntimeStore{}
	attacher := newThreadRuntimeHandleAttacher(threadRuntimeHandleAttacherConfig{
		Registry:      registry,
		ThreadID:      threadID,
		RuntimeStore:  runtimes,
		OrgID:         orgID,
		RuntimeID:     runtimeID,
		LeaseToken:    leaseToken,
		LeaseDuration: time.Minute,
		Logger:        zerolog.Nop(),
	})

	attacher.Attach(handle)
	err := registry.DeliverInput(context.Background(), threadID, []byte("attached\n"))
	attacher.Detach()
	errAfterDetach := registry.DeliverInput(context.Background(), threadID, []byte("detached\n"))

	require.NoError(t, err, "Attach should make the handle addressable through the thread registry")
	require.ErrorIs(t, errAfterDetach, ErrThreadHandleUnavailable, "Detach should remove the handle from the thread registry")
	require.Equal(t, []markLiveCall{{runtimeID: runtimeID, leaseToken: leaseToken, handleID: "thread-test-handle"}}, runtimes.liveCalls, "Attach should mark the runtime live with the provider handle id")
}

func TestOrchestrator_StartThreadRuntimeControlCreatesRuntimeHolderAndAcksSeedMessages(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	model := "gpt-5"
	runtimes := &fakeThreadRuntimeStore{
		createRuntime: models.ThreadRuntime{
			ID:         runtimeID,
			OrgID:      orgID,
			SessionID:  sessionID,
			ThreadID:   threadID,
			LeaseToken: uuid.New(),
		},
	}
	inbox := &fakeThreadInboxStore{}
	holders := &fakeSessionSandboxHolderStore{}
	orch := &Orchestrator{
		threadRuntimes: runtimes,
		threadInbox:    inbox,
		sandboxHolders: holders,
		nodeID:         "worker-a",
		logger:         zerolog.Nop(),
	}
	session := &models.Session{
		ID:            sessionID,
		OrgID:         orgID,
		AgentType:     models.AgentTypeCodex,
		ModelOverride: &model,
	}
	sandbox := &Sandbox{ID: "container-1"}

	control, err := orch.startThreadRuntimeControl(context.Background(), session, threadID, sandbox, []int64{10, 11}, zerolog.Nop())
	require.NoError(t, err, "startThreadRuntimeControl should create durable runtime ownership")
	require.NotNil(t, control, "startThreadRuntimeControl should return lifecycle control when stores are configured")
	require.Len(t, runtimes.createCalls, 1, "startThreadRuntimeControl should create one runtime row")
	require.Equal(t, sessionID, runtimes.createCalls[0].SessionID, "runtime should be scoped to the session")
	require.Equal(t, threadID, runtimes.createCalls[0].ThreadID, "runtime should be scoped to the thread")
	require.Equal(t, sessionID, runtimes.createCalls[0].SandboxID, "runtime should use the session id as the logical shared sandbox id")
	require.Equal(t, "container-1", runtimes.createCalls[0].ContainerID, "runtime should record the live provider container id")
	require.Equal(t, models.AgentTypeCodex, runtimes.createCalls[0].AgentType, "runtime should record the adapter type")
	require.Equal(t, model, runtimes.createCalls[0].Model, "runtime should record the effective model")
	require.Equal(t, "worker-a", runtimes.createCalls[0].OwnerNodeID, "runtime should record the owning worker node")
	require.NotEqual(t, uuid.Nil, runtimes.createCalls[0].LeaseToken, "runtime should be fenced by a lease token")
	require.Len(t, holders.createCalls, 1, "startThreadRuntimeControl should create a sandbox holder")
	require.Equal(t, models.SessionSandboxHolderKindThreadRuntime, holders.createCalls[0].HolderKind, "holder should identify thread-runtime ownership")
	require.Equal(t, runtimeID, holders.createCalls[0].HolderID, "holder id should match the runtime id")
	require.Equal(t, []deliveringForMessagesCall{{runtimeID: runtimeID, ownerNodeID: "worker-a", messageIDs: []int64{10, 11}}}, inbox.deliveringForMessagesCalls, "seed prompt messages should be claimed until the provider process is live")

	control.Close(context.Background(), models.ThreadRuntimeStatusClosed, "completed", "")
	require.Equal(t, []terminalCall{{runtimeID: runtimeID, status: models.ThreadRuntimeStatusClosed, stopReason: "completed", lastError: ""}}, runtimes.terminalCalls, "Close should mark the runtime terminal")
	require.Equal(t, []releaseHolderCall{{sessionID: sessionID, holderID: runtimeID, kind: models.SessionSandboxHolderKindThreadRuntime}}, holders.releaseCalls, "Close should release the sandbox holder")
}

func TestKeepSessionRunningIfSiblingRuntimesActive(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	sessions := &fakeSessionStatusUpdater{}
	holders := &fakeSessionSandboxHolderStore{activeThreadRuntimeCount: 1}

	keepSessionRunningIfSiblingRuntimesActive(context.Background(), sessions, holders, orgID, sessionID, zerolog.Nop())

	require.Equal(t, []models.SessionStatus{models.SessionStatusRunning}, sessions.statuses, "session should be marked running while another thread runtime holder remains active")
	require.Equal(t, []models.SandboxState{models.SandboxStateRunning}, sessions.sandboxStates, "sandbox state should stay running while another thread runtime holder remains active")
}

func TestKeepSessionRunningIfSiblingRuntimesActiveNoopsWithoutActiveHolders(t *testing.T) {
	t.Parallel()

	sessions := &fakeSessionStatusUpdater{}
	holders := &fakeSessionSandboxHolderStore{activeThreadRuntimeCount: 0}

	keepSessionRunningIfSiblingRuntimesActive(context.Background(), sessions, holders, uuid.New(), uuid.New(), zerolog.Nop())

	require.Empty(t, sessions.statuses, "session status should be left alone when no sibling runtime holders remain")
}

func TestKeepSessionRunningIfSiblingRuntimesActiveIgnoresPreviewHolders(t *testing.T) {
	t.Parallel()

	sessions := &fakeSessionStatusUpdater{}
	holders := &fakeSessionSandboxHolderStore{activeCount: 1}

	keepSessionRunningIfSiblingRuntimesActive(context.Background(), sessions, holders, uuid.New(), uuid.New(), zerolog.Nop())

	require.Empty(t, sessions.statuses, "session status should be left alone when only non-runtime sandbox holders remain")
	require.Empty(t, sessions.sandboxStates, "sandbox state should be left alone when only non-runtime sandbox holders remain")
}

func TestThreadRuntimeControl_StartHeartbeatCancelsRunOnLeaseLoss(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.New()
	sessionID := uuid.New()
	runtimes := &fakeThreadRuntimeStore{heartbeatOK: false}
	holders := &fakeSessionSandboxHolderStore{heartbeatOK: true}
	control := &threadRuntimeControl{
		runtime: models.ThreadRuntime{
			ID:         runtimeID,
			OrgID:      uuid.New(),
			SessionID:  sessionID,
			ThreadID:   uuid.New(),
			LeaseToken: uuid.New(),
		},
		leaseDuration: time.Minute,
		runtimes:      runtimes,
		holders:       holders,
		logger:        zerolog.Nop(),
	}
	var cancelled atomic.Bool

	stop := control.StartHeartbeat(context.Background(), 5*time.Millisecond, func() {
		cancelled.Store(true)
	})
	defer stop()

	require.Eventually(t, func() bool {
		return cancelled.Load()
	}, time.Second, 5*time.Millisecond, "heartbeat should cancel the run when runtime lease renewal is fenced")
	require.NotEmpty(t, runtimes.heartbeatCalls, "heartbeat should renew the runtime lease")
}

func TestThreadRuntimeControl_StartInboxPollerKeepsDeliveringUntilStopped(t *testing.T) {
	t.Parallel()

	control := &threadRuntimeControl{logger: zerolog.Nop()}
	var calls atomic.Int32

	stop := control.StartInboxPoller(context.Background(), 5*time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	})
	require.Eventually(t, func() bool {
		return calls.Load() >= 2
	}, time.Second, 5*time.Millisecond, "inbox poller should repeatedly call the delivery function")
	stop()
	afterStop := calls.Load()
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, afterStop, calls.Load(), "inbox poller should stop after the returned stop function is called")
}

type fakeThreadRuntimeStore struct {
	active         models.ThreadRuntime
	getErr         error
	createRuntime  models.ThreadRuntime
	createCalls    []db.CreateThreadRuntimeParams
	advanceCalls   []advanceDeliveryCall
	commitCalls    []commitDeliveryCall
	liveCalls      []markLiveCall
	terminalCalls  []terminalCall
	heartbeatOK    bool
	heartbeatCalls []uuid.UUID
}

type advanceDeliveryCall struct {
	runtimeID  uuid.UUID
	leaseToken uuid.UUID
	delivered  int64
	acked      int64
}

type commitDeliveryCall struct {
	runtimeID  uuid.UUID
	leaseToken uuid.UUID
	threadID   uuid.UUID
	delivered  int64
	acked      int64
}

type markLiveCall struct {
	runtimeID  uuid.UUID
	leaseToken uuid.UUID
	handleID   string
}

type terminalCall struct {
	runtimeID  uuid.UUID
	status     models.ThreadRuntimeStatus
	stopReason string
	lastError  string
}

func (s *fakeThreadRuntimeStore) CreateStarting(_ context.Context, orgID uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	s.createCalls = append(s.createCalls, params)
	runtime := s.createRuntime
	if runtime.ID == uuid.Nil {
		runtime.ID = uuid.New()
	}
	runtime.OrgID = orgID
	runtime.SessionID = params.SessionID
	runtime.ThreadID = params.ThreadID
	if runtime.LeaseToken == uuid.Nil {
		runtime.LeaseToken = params.LeaseToken
	}
	return runtime, nil
}

func (s *fakeThreadRuntimeStore) GetActiveByThread(context.Context, uuid.UUID, uuid.UUID) (models.ThreadRuntime, error) {
	if s.getErr != nil {
		return models.ThreadRuntime{}, s.getErr
	}
	return s.active, nil
}

func (s *fakeThreadRuntimeStore) MarkLiveWithLease(_ context.Context, _ uuid.UUID, runtimeID, leaseToken uuid.UUID, runtimeHandleID string, _ time.Duration) (bool, error) {
	s.liveCalls = append(s.liveCalls, markLiveCall{runtimeID: runtimeID, leaseToken: leaseToken, handleID: runtimeHandleID})
	return true, nil
}

func (s *fakeThreadRuntimeStore) HeartbeatWithLease(_ context.Context, _ uuid.UUID, runtimeID, _ uuid.UUID, _ time.Duration) (bool, error) {
	s.heartbeatCalls = append(s.heartbeatCalls, runtimeID)
	return s.heartbeatOK, nil
}

func (s *fakeThreadRuntimeStore) AdvanceDeliveryWithLease(_ context.Context, _ uuid.UUID, runtimeID, leaseToken uuid.UUID, deliveredSequence, ackedSequence int64) (bool, error) {
	s.advanceCalls = append(s.advanceCalls, advanceDeliveryCall{runtimeID: runtimeID, leaseToken: leaseToken, delivered: deliveredSequence, acked: ackedSequence})
	return true, nil
}

func (s *fakeThreadRuntimeStore) CommitInboxDeliveryWithLease(_ context.Context, _ uuid.UUID, runtimeID, leaseToken, threadID uuid.UUID, _ string, deliveredSequence, ackedSequence int64) (bool, error) {
	s.commitCalls = append(s.commitCalls, commitDeliveryCall{runtimeID: runtimeID, leaseToken: leaseToken, threadID: threadID, delivered: deliveredSequence, acked: ackedSequence})
	return true, nil
}

func (s *fakeThreadRuntimeStore) MarkTerminalWithLease(_ context.Context, _ uuid.UUID, runtimeID, _ uuid.UUID, status models.ThreadRuntimeStatus, stopReason, lastError string) (bool, error) {
	s.terminalCalls = append(s.terminalCalls, terminalCall{runtimeID: runtimeID, status: status, stopReason: stopReason, lastError: lastError})
	return true, nil
}

type fakeThreadInboxStore struct {
	deliverable                []models.ThreadInboxEntry
	deliveredThrough           []int64
	deliveredEntries           []markDeliveredEntryCall
	ackedThrough               []int64
	ackForMessagesCalls        []ackForMessagesCall
	deliveringForMessagesCalls []deliveringForMessagesCall
	deadLetterCalls            []deadLetterCall
	deliveryFailedCalls        []deliveryFailedCall
	listFn                     func()
}

type deadLetterCall struct {
	threadID uuid.UUID
	entryID  uuid.UUID
}

type markDeliveredEntryCall struct {
	threadID   uuid.UUID
	runtimeID  uuid.UUID
	entryID    uuid.UUID
	sequenceNo int64
}

type deliveryFailedCall struct {
	threadID  uuid.UUID
	runtimeID uuid.UUID
	entryID   uuid.UUID
}

func (s *fakeThreadInboxStore) AppendForMessage(context.Context, uuid.UUID, db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	return models.ThreadInboxEntry{}, nil
}

func (s *fakeThreadInboxStore) ListDeliverableAfter(context.Context, uuid.UUID, uuid.UUID, int64, int) ([]models.ThreadInboxEntry, error) {
	if s.listFn != nil {
		s.listFn()
	}
	out := make([]models.ThreadInboxEntry, len(s.deliverable))
	copy(out, s.deliverable)
	return out, nil
}

func (s *fakeThreadInboxStore) ClaimDeliverableAfter(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, int64, int) ([]models.ThreadInboxEntry, error) {
	if s.listFn != nil {
		s.listFn()
	}
	out := make([]models.ThreadInboxEntry, len(s.deliverable))
	copy(out, s.deliverable)
	return out, nil
}

func (s *fakeThreadInboxStore) MarkDeliveredThrough(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ string, sequenceNo int64) (int64, error) {
	s.deliveredThrough = append(s.deliveredThrough, sequenceNo)
	return 1, nil
}

func (s *fakeThreadInboxStore) MarkDeliveredForEntry(_ context.Context, _ uuid.UUID, threadID, runtimeID uuid.UUID, _ string, entryID uuid.UUID, sequenceNo int64) (int64, error) {
	s.deliveredEntries = append(s.deliveredEntries, markDeliveredEntryCall{threadID: threadID, runtimeID: runtimeID, entryID: entryID, sequenceNo: sequenceNo})
	return 1, nil
}

func (s *fakeThreadInboxStore) MarkAckedThrough(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, sequenceNo int64) (int64, error) {
	s.ackedThrough = append(s.ackedThrough, sequenceNo)
	return 1, nil
}

func (s *fakeThreadInboxStore) MarkDeadLetter(_ context.Context, _ uuid.UUID, threadID, entryID uuid.UUID, _ string) (models.ThreadInboxEntry, error) {
	s.deadLetterCalls = append(s.deadLetterCalls, deadLetterCall{threadID: threadID, entryID: entryID})
	return models.ThreadInboxEntry{ID: entryID, ThreadID: threadID, DeliveryState: models.ThreadInboxDeliveryStateDeadLetter}, nil
}

func (s *fakeThreadInboxStore) MarkDeliveryFailed(_ context.Context, _ uuid.UUID, threadID, runtimeID, entryID uuid.UUID, _ string, _ int) (models.ThreadInboxEntry, error) {
	s.deliveryFailedCalls = append(s.deliveryFailedCalls, deliveryFailedCall{threadID: threadID, runtimeID: runtimeID, entryID: entryID})
	return models.ThreadInboxEntry{ID: entryID, ThreadID: threadID, DeliveryState: models.ThreadInboxDeliveryStatePending}, nil
}

type ackForMessagesCall struct {
	runtimeID  uuid.UUID
	messageIDs []int64
}

type deliveringForMessagesCall struct {
	runtimeID   uuid.UUID
	ownerNodeID string
	messageIDs  []int64
}

func (s *fakeThreadInboxStore) MarkAckedForMessages(_ context.Context, _ uuid.UUID, _ uuid.UUID, runtimeID uuid.UUID, messageIDs []int64) (int64, error) {
	copied := make([]int64, len(messageIDs))
	copy(copied, messageIDs)
	s.ackForMessagesCalls = append(s.ackForMessagesCalls, ackForMessagesCall{runtimeID: runtimeID, messageIDs: copied})
	return int64(len(messageIDs)), nil
}

func (s *fakeThreadInboxStore) MarkDeliveringForMessages(_ context.Context, _ uuid.UUID, _ uuid.UUID, runtimeID uuid.UUID, ownerNodeID string, messageIDs []int64) (int64, error) {
	copied := make([]int64, len(messageIDs))
	copy(copied, messageIDs)
	s.deliveringForMessagesCalls = append(s.deliveringForMessagesCalls, deliveringForMessagesCall{runtimeID: runtimeID, ownerNodeID: ownerNodeID, messageIDs: copied})
	return int64(len(messageIDs)), nil
}

func (s *fakeThreadInboxStore) CountPendingByThread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, fmt.Errorf("not implemented")
}

func (s *fakeThreadInboxStore) IsMessageAcked(context.Context, uuid.UUID, uuid.UUID, int64) (bool, error) {
	return false, nil
}

type fakeSessionSandboxHolderStore struct {
	createCalls              []db.CreateSessionSandboxHolderParams
	releaseCalls             []releaseHolderCall
	heartbeatOK              bool
	activeCount              int
	activeThreadRuntimeCount int
}

type releaseHolderCall struct {
	sessionID uuid.UUID
	holderID  uuid.UUID
	kind      models.SessionSandboxHolderKind
}

func (s *fakeSessionSandboxHolderStore) CreateActive(_ context.Context, _ uuid.UUID, params db.CreateSessionSandboxHolderParams) (models.SessionSandboxHolder, error) {
	s.createCalls = append(s.createCalls, params)
	return models.SessionSandboxHolder{
		ID:         uuid.New(),
		SessionID:  params.SessionID,
		HolderKind: params.HolderKind,
		HolderID:   params.HolderID,
		LeaseToken: params.LeaseToken,
	}, nil
}

func (s *fakeSessionSandboxHolderStore) ReleaseWithLease(_ context.Context, _ uuid.UUID, sessionID uuid.UUID, kind models.SessionSandboxHolderKind, holderID, _ uuid.UUID) (bool, error) {
	s.releaseCalls = append(s.releaseCalls, releaseHolderCall{sessionID: sessionID, holderID: holderID, kind: kind})
	return true, nil
}

func (s *fakeSessionSandboxHolderStore) HeartbeatWithLease(context.Context, uuid.UUID, uuid.UUID, models.SessionSandboxHolderKind, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return s.heartbeatOK, nil
}

func (s *fakeSessionSandboxHolderStore) CountActiveBySession(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return s.activeCount, nil
}

func (s *fakeSessionSandboxHolderStore) CountActiveThreadRuntimesBySession(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return s.activeThreadRuntimeCount, nil
}

func (s *fakeSessionSandboxHolderStore) CountActiveThreadRuntimesBySessionExcluding(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (int, error) {
	return s.activeThreadRuntimeCount, nil
}

type fakeSessionStatusUpdater struct {
	statuses      []models.SessionStatus
	sandboxStates []models.SandboxState
}

func (s *fakeSessionStatusUpdater) UpdateStatus(_ context.Context, _ uuid.UUID, _ uuid.UUID, status models.SessionStatus) error {
	s.statuses = append(s.statuses, status)
	return nil
}

func (s *fakeSessionStatusUpdater) UpdateSandboxState(_ context.Context, _ uuid.UUID, _ uuid.UUID, state models.SandboxState) error {
	s.sandboxStates = append(s.sandboxStates, state)
	return nil
}

type fakeThreadRuntimeAdapter struct{}

func (fakeThreadRuntimeAdapter) Name() models.AgentType {
	return models.AgentTypeClaudeCode
}

func (fakeThreadRuntimeAdapter) PreparePrompt(context.Context, *AgentInput) (*AgentPrompt, error) {
	return &AgentPrompt{}, nil
}

func (fakeThreadRuntimeAdapter) Execute(context.Context, *Sandbox, *AgentPrompt, chan<- LogEntry) (*AgentResult, error) {
	return &AgentResult{}, nil
}

func (fakeThreadRuntimeAdapter) ResumeMode() SessionResumeMode {
	return ResumeBySessionID
}

type fakeThreadRuntimeInputAdapter struct {
	fakeThreadRuntimeAdapter
	prefix string
}

func (a fakeThreadRuntimeInputAdapter) FormatThreadRuntimeInput(entry models.ThreadInboxEntry) ([]byte, error) {
	input, err := formatThreadInboxRuntimeInput(entry)
	if err != nil {
		return nil, err
	}
	return append([]byte(a.prefix), input...), nil
}

func (a fakeThreadRuntimeInputAdapter) ThreadRuntimeLiveInputProtocol() ThreadRuntimeLiveInputProtocol {
	return ThreadRuntimeLiveInputProtocol{
		Mode:                 ThreadRuntimeLiveInputProtocolOpenHandle,
		DeliversToOpenHandle: true,
	}
}
