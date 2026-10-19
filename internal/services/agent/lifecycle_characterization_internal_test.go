package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDrainLifecycleCharacterization_QueuedAnswerAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		sessionScope bool
		sibling      bool
		deduplicated bool
		enqueueFails bool
	}{
		{name: "same-thread queued answer"},
		{name: "legacy session message without an inbox", sessionScope: true},
		{name: "sibling thread uses its existing consumer key", sibling: true},
		{name: "dedupe no-op retains pending answer", sibling: true, deduplicated: true},
		{name: "enqueue failure retains pending answer", enqueueFails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, finishingThreadID, queuedThreadID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			workerID := "worker-owning-runtime"
			containerID := "retained-runtime"
			session := &models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusAwaitingInput, WorkerNodeID: &workerID, ContainerID: &containerID}
			var callerThreadID, messageThreadID *uuid.UUID
			if !tt.sessionScope {
				callerThreadID = &finishingThreadID
				messageThreadID = callerThreadID
				if tt.sibling {
					messageThreadID = &queuedThreadID
				}
			}
			messages := &drainStubMessages{messages: []models.SessionMessage{
				{ID: 6, OrgID: orgID, SessionID: sessionID, ThreadID: messageThreadID, UserID: &userID, Role: models.MessageRoleUser, TurnNumber: 4, Content: "Use the existing table."},
				{ID: 7, OrgID: orgID, SessionID: sessionID, ThreadID: messageThreadID, UserID: &userID, Role: models.MessageRoleUser, TurnNumber: 5, Content: "A later message must not replace the queued identity."},
			}}
			jobs := &drainStubJobs{deduplicated: tt.deduplicated}
			if tt.enqueueFails {
				jobs.err = errors.New("queue unavailable")
			}
			threads := &drainStubThreads{}
			inputs := &drainStubHumanInputs{request: models.HumanInputRequest{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ThreadID: messageThreadID, Status: models.HumanInputRequestStatusPending}}
			orchestrator := newDrainOrchestrator(messages, &drainStubSessions{session: *session}, jobs, threads, inputs)
			orchestrator.drainQueuedMessagesAfterProcessedID(context.Background(), session, 5, callerThreadID, zerolog.Nop())
			var expectedEnqueues []drainStubEnqueue
			var expectedCleared []uuid.UUID
			if !tt.enqueueFails {
				payload := map[string]string{"session_id": sessionID.String(), "org_id": orgID.String(), "queued_message_id": "6"}
				dedupeKey := continueSessionDrainDedupeKey(sessionID, 6)
				if messageThreadID != nil {
					payload["thread_id"] = messageThreadID.String()
					if tt.sibling {
						dedupeKey = continueSessionDedupeKey(*messageThreadID)
					}
					if !tt.deduplicated {
						expectedCleared = []uuid.UUID{*messageThreadID}
					}
				}
				expectedEnqueues = []drainStubEnqueue{{queue: "agent", jobType: "continue_session", payload: payload, dedupeKey: dedupeKey, targetNodeID: workerID}}
			}
			require.Equal(t, expectedEnqueues, jobs.enqueues, "drain should preserve exact queued-message identity, target thread, worker, and competing dedupe family")
			require.Equal(t, expectedCleared, threads.clearedThreadIDs, "only a newly inserted continuation may clear its queued thread's recovery signal")
			require.Equal(t, []drainStubHumanInputAnswer(nil), inputs.sessionAnswers, "runtime drain must defer session input answering to the worker")
			require.Equal(t, []drainStubHumanInputAnswer(nil), inputs.threadAnswers, "runtime drain must defer thread input answering even when an input store is configured")
		})
	}
}
