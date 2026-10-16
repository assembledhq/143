package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type continuationAttemptRecord struct {
	orgID, jobID, jobToken, sessionID, threadID, runtimeID, runtimeToken uuid.UUID
	messageID                                                            int64
	taskMessageIDs                                                       []int64
}

type continuationAttemptJobStore struct {
	agent.JobStore
	changed bool
	err     error
	calls   []continuationAttemptRecord
	event   func(string)
}

func (s *continuationAttemptJobStore) RecordContinuationAttemptWithLease(_ context.Context, orgID, jobID, jobToken, sessionID, threadID, runtimeID, runtimeToken uuid.UUID, messageID int64, taskMessageIDs []int64) (bool, error) {
	s.calls = append(s.calls, continuationAttemptRecord{orgID, jobID, jobToken, sessionID, threadID, runtimeID, runtimeToken, messageID, append([]int64(nil), taskMessageIDs...)})
	s.event("record")
	return s.changed, s.err
}

type continuationAttemptInbox struct {
	*restartAdmissionInbox
	event func(string)
}

func (s *continuationAttemptInbox) MarkAckedForSeedMessages(ctx context.Context, orgID, threadID, runtimeID, token uuid.UUID, ids []int64) (int64, error) {
	s.event("admit")
	return s.restartAdmissionInbox.MarkAckedForSeedMessages(ctx, orgID, threadID, runtimeID, token, ids)
}

type continuationAttemptRuntimeClose struct {
	orgID, runtimeID, token uuid.UUID
}

type continuationAttemptRuntimeStore struct {
	*reviewSetupLifecycleRuntimeStore
	closed []continuationAttemptRuntimeClose
	event  func(string)
}

func (s *continuationAttemptRuntimeStore) MarkTerminalWithLease(ctx context.Context, orgID, runtimeID, token uuid.UUID, status models.ThreadRuntimeStatus, reason, lastError string) (bool, error) {
	s.closed = append(s.closed, continuationAttemptRuntimeClose{orgID, runtimeID, token})
	s.event("close")
	return s.reviewSetupLifecycleRuntimeStore.MarkTerminalWithLease(ctx, orgID, runtimeID, token, status, reason, lastError)
}

func TestContinueSessionRecordsOwnedAttemptBeforeProviderAdmission(t *testing.T) {
	t.Parallel()
	recordErr := errors.New("continuation attempt write unavailable")
	tests := []struct {
		name       string
		changed    bool
		recovery   bool
		err        error
		wantErr    error
		wantEvents []string
	}{
		{name: "recorded attempt admits provider", changed: true, wantEvents: []string{"record", "admit", "execute", "close"}},
		{name: "recorded checkpoint recovery admits provider", changed: true, recovery: true, wantEvents: []string{"record", "admit", "execute", "close"}},
		{name: "write failure blocks provider", err: recordErr, wantErr: recordErr, wantEvents: []string{"record", "close"}},
		{name: "fenced job lease blocks provider", wantErr: agent.ErrThreadRuntimeLeaseLost, wantEvents: []string{"record", "close"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			threadID, siblingThreadID, jobID, jobToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			session.PrimaryThreadID = &threadID
			session.Origin = models.SessionOriginManual
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.InteractionMode = models.SessionInteractionModeInteractive
			session.ValidationPolicy = models.SessionValidationPolicySkip
			d := defaultDeps()
			d.issues.issue = issue
			if tt.recovery {
				session.AgentSessionID = strPtr("owned-provider-checkpoint")
				session.ContainerID = strPtr("owned-existing-sandbox")
				session.SandboxState = models.SandboxStateRunning
				d.adapter.resumeMode = agent.ResumeBySessionID
			}
			d.messages.messages = []models.SessionMessage{
				{ID: 1, OrgID: orgID, SessionID: session.ID, ThreadID: &threadID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "Previously admitted task"},
				{ID: 2, OrgID: orgID, SessionID: session.ID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "First pending instruction"},
				{ID: 3, OrgID: orgID, SessionID: session.ID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Queued instruction boundary"},
				{ID: 4, OrgID: orgID, SessionID: session.ID, ThreadID: &siblingThreadID, TurnNumber: 9, Role: models.MessageRoleUser, Content: "Unrelated sibling instruction"},
				{ID: 5, OrgID: orgID, SessionID: session.ID, ThreadID: &threadID, TurnNumber: 3, Role: models.MessageRoleUser, Content: "Input after this job's boundary"},
			}
			var events []string
			event := func(name string) { events = append(events, name) }
			sibling := models.ThreadRuntime{ID: uuid.New(), OrgID: orgID, SessionID: session.ID, ThreadID: siblingThreadID, LeaseToken: uuid.New(), Status: models.ThreadRuntimeStatusLive}
			runtimes := &continuationAttemptRuntimeStore{
				reviewSetupLifecycleRuntimeStore: &reviewSetupLifecycleRuntimeStore{runtime: map[uuid.UUID]models.ThreadRuntime{siblingThreadID: sibling}},
				event:                            event,
			}
			inbox := &continuationAttemptInbox{restartAdmissionInbox: newRestartAdmissionInbox(1), event: event}
			jobs := &continuationAttemptJobStore{JobStore: d.jobs, changed: tt.changed, err: tt.err, event: event}
			var owned models.ThreadRuntime
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				event("execute")
				require.Equal(t, []string{"record", "admit", "execute"}, events, "attempt ownership must be durable before both input admission and provider execution")
				return &agent.AgentResult{Summary: "Completed queued task", ExitCode: 0}, nil
			}
			jobs.event = func(name string) {
				runtimes.mu.Lock()
				owned = runtimes.runtime[threadID]
				runtimes.mu.Unlock()
				event(name)
			}
			orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
				Provider: d.provider, Adapters: map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
				Sessions: d.sessions, SessionThreads: &mockSessionThreadStore{}, SessionLogs: d.logs,
				SessionMessages: d.messages, SessionQuestions: d.questions, HumanInputRequests: d.humanInputs,
				Issues: d.issues, Repositories: d.repos, Jobs: jobs, GitHub: d.github,
				CodingCredentials: codingCredsForTest(d), Credentials: d.creds, Snapshots: d.snapshots,
				ThreadRuntimes: runtimes, ThreadInbox: inbox, Logger: zerolog.Nop(), MaxConcurrent: 3,
			})
			boundary := int64(3)
			ctx := jobctx.WithJobID(jobctx.WithLockToken(context.Background(), jobToken), jobID)
			opts := &agent.ContinueSessionOptions{ThreadID: &threadID, QueuedMessageID: &boundary}
			var err error
			if tt.recovery {
				opts.ExecutionPurpose = models.SessionExecutionPurposeCheckpointRecovery
				recoveryRuntimeID := uuid.New()
				opts.RecoveryRuntimeID = &recoveryRuntimeID
				opts.RecoveryMessageIDs = []int64{2, 3}
				err = orchestrator.RecoverSessionWithOptions(ctx, session, opts)
			} else {
				err = orchestrator.ContinueSession(ctx, session, opts)
			}
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "failed attempt ownership must stop continuation with its retryable cause")
			} else {
				require.NoError(t, err, "durably recorded attempt should execute its selected pending input")
			}
			require.Equal(t, tt.wantEvents, events, "rejected ownership cannot admit input or invoke the provider")
			require.NotEqual(t, uuid.Nil, owned.ID, "test must allocate and record a real durable runtime owner")
			require.NotEqual(t, uuid.Nil, owned.LeaseToken, "runtime recording must carry its generated lease token")
			require.Equal(t, []continuationAttemptRecord{{orgID, jobID, jobToken, session.ID, threadID, owned.ID, owned.LeaseToken, boundary, []int64{2, 3}}}, jobs.calls, "record must bind exact tenant, job lease, session, thread, runtime lease, and original input boundary")
			require.Equal(t, []continuationAttemptRuntimeClose{{orgID, owned.ID, owned.LeaseToken}}, runtimes.closed, "completion or record rejection must close only the runtime owned by this attempt")
			runtimes.mu.Lock()
			remaining := runtimes.runtime
			runtimes.mu.Unlock()
			require.Equal(t, map[uuid.UUID]models.ThreadRuntime{siblingThreadID: sibling}, remaining, "closing this attempt must preserve the sibling runtime unchanged")
		})
	}
}
