package agent_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type restartAdmissionInbox struct {
	*initialSeedAdmissionInbox
	mu           sync.Mutex
	applied      map[int64]bool
	admitted     [][]int64
	claimed      [][]int64
	recoveryIDs  []int64
	unsettledIDs []int64
}

func (s *restartAdmissionInbox) ListUnsettledAppliedMessageIDs(_ context.Context, _, _, _ uuid.UUID, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []int64
	for _, id := range ids {
		for _, unfinished := range s.unsettledIDs {
			if id == unfinished {
				result = append(result, id)
			}
		}
	}
	return result, nil
}

func (s *restartAdmissionInbox) ListAppliedMessageIDs(_ context.Context, _, _, _ uuid.UUID, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []int64{}
	for _, id := range ids {
		if s.applied[id] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (s *restartAdmissionInbox) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, ids []int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitted = append(s.admitted, append([]int64(nil), ids...))
	for _, id := range ids {
		s.applied[id] = true
	}
	return int64(len(ids)), nil
}
func (s *restartAdmissionInbox) MarkDeliveringForMessages(_ context.Context, _, _, _, _ uuid.UUID, _ string, ids []int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = append(s.claimed, append([]int64(nil), ids...))
	return int64(len(ids)), nil
}
func (s *restartAdmissionInbox) IsMessageAcked(_ context.Context, _, _ uuid.UUID, id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied[id], nil
}
func newRestartAdmissionInbox(applied ...int64) *restartAdmissionInbox {
	s := &restartAdmissionInbox{initialSeedAdmissionInbox: &initialSeedAdmissionInbox{}, applied: map[int64]bool{}}
	for _, id := range applied {
		s.applied[id] = true
	}
	return s
}

func TestRunAgent_RestartRetainsTaskWithoutReadmittingAppliedInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                     string
		recover, legacy, sibling bool
	}{
		{name: "second run_agent after provider failure"},
		{name: "automatic recovery without checkpoint", recover: true},
		{name: "legacy primary input", legacy: true},
		{name: "newer sibling cannot replace primary task", sibling: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org := testOrg()
			issue := testIssue(org)
			run := testRun(org, issue.ID)
			thread := uuid.New()
			run.PrimaryThreadID = &thread
			d := defaultDeps()
			inbox := newRestartAdmissionInbox()
			runtimes := &reviewSetupLifecycleRuntimeStore{}
			d.threadInbox, d.threadRuntimes = inbox, runtimes
			msgThread := &thread
			if tt.legacy {
				msgThread = nil
			}
			d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: org, SessionID: run.ID, ThreadID: msgThread, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Preserved primary task"}}
			var contexts []string
			d.adapter.prepareFn = func(_ context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
				contexts = append(contexts, input.UserMessage)
				return &agent.AgentPrompt{UserPrompt: input.UserMessage}, nil
			}
			executions := 0
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions++
				if executions == 1 {
					return nil, errors.New("synthetic worker interruption after admission")
				}
				return &agent.AgentResult{ExitCode: 0, Summary: "Recovered task"}, nil
			}
			orch := buildOrchestrator(d)
			require.ErrorContains(t, orch.RunAgent(context.Background(), run), "synthetic worker interruption", "first execution must fail after durable seed admission")
			if tt.sibling {
				sibling := uuid.New()
				require.NoError(t, d.messages.Create(context.Background(), &models.SessionMessage{ID: 20, OrgID: org, SessionID: run.ID, ThreadID: &sibling, Role: models.MessageRoleUser, TurnNumber: 9, Content: "Unrelated sibling task"}), "append newer sibling input between restarts")
			}
			var err error
			if tt.recover {
				run.CurrentTurn, run.SnapshotKey, run.AgentSessionID = 0, nil, nil
				err = orch.RecoverSession(context.Background(), run)
			} else {
				err = orch.RunAgent(context.Background(), run)
			}
			require.NoError(t, err, "restart must keep the task as context without trying strict seed admission twice")
			require.Equal(t, []string{"Preserved primary task", "Preserved primary task"}, contexts, "primary task survives deliberate restart and sibling message ordering")
			require.Equal(t, [][]int64{{1}}, inbox.admitted, "only first execution admits the original user message")
			require.Equal(t, [][]int64{{1}}, inbox.claimed, "restart must not reclaim an already applied seed")
			require.Equal(t, int64(2), runtimes.created.Load(), "each execution must retain durable runtime ownership")
			require.Equal(t, 2, executions, "one original execution and one authorized task restart are expected")
		})
	}
}

func TestContinueSession_UsesOnlyUnappliedActionableInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                            string
		recovery, automaticRecovery, nilOpts, duplicate bool
	}{
		{name: "ordinary scoped continuation"},
		{name: "ordinary nil options initializes primary runtime", nilOpts: true},
		{name: "explicit checkpoint retry retains applied task context", recovery: true},
		{name: "automatic checkpoint recovery retains task context", automaticRecovery: true},
		{name: "applied queued message is a no-op", duplicate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org := testOrg()
			issue := testIssue(org)
			session := testRun(org, issue.ID)
			primary, sibling := uuid.New(), uuid.New()
			session.PrimaryThreadID = &primary
			session.Origin = models.SessionOriginManual
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.InteractionMode = models.SessionInteractionModeInteractive
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.AgentSessionID = strPtr("accepted-provider-context")
			// A provider ID is a durable checkpoint for this adapter. Reusing an
			// existing sandbox keeps the test focused on message admission.
			session.ContainerID = strPtr("synthetic-running-sandbox")
			session.SandboxState = models.SandboxStateRunning
			d := defaultDeps()
			d.adapter.resumeMode = agent.ResumeBySessionID
			inbox := newRestartAdmissionInbox(1)
			runtimes := &reviewSetupLifecycleRuntimeStore{}
			d.threadInbox, d.threadRuntimes = inbox, runtimes
			oldKey, newKey := org.String()+"/2026-05/old.png", org.String()+"/2026-05/new.png"
			d.uploads = &mockUploadStore{files: map[string]mockUploadFile{oldKey: {body: bytes.Repeat([]byte{1}, 3), contentType: "image/png"}, newKey: {body: bytes.Repeat([]byte{2}, 3), contentType: "image/png"}}}
			d.messages.messages = []models.SessionMessage{
				{ID: 1, OrgID: org, SessionID: session.ID, ThreadID: &primary, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Old applied task", Attachments: []string{"/api/v1/uploads/files/" + oldKey}},
				{ID: 3, OrgID: org, SessionID: session.ID, ThreadID: &sibling, Role: models.MessageRoleUser, TurnNumber: 9, Content: "Sibling must stay isolated"},
			}
			if !tt.recovery && !tt.automaticRecovery && !tt.duplicate {
				d.messages.messages = append(d.messages.messages, models.SessionMessage{ID: 2, OrgID: org, SessionID: session.ID, ThreadID: &primary, Role: models.MessageRoleUser, TurnNumber: 2, Content: "New pending follow-up", Attachments: []string{"/api/v1/uploads/files/" + newKey}})
			}
			executions := 0
			var actionable string
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions++
				actionable = prompt.UserMessage
				if actionable == "" {
					actionable = prompt.UserPrompt
				}
				return &agent.AgentResult{Summary: "Done", ExitCode: 0}, nil
			}
			opts := &agent.ContinueSessionOptions{ThreadID: &primary}
			if tt.nilOpts {
				opts = nil
			}
			if tt.recovery {
				opts.ExecutionPurpose = models.SessionExecutionPurposeCheckpointRetry
			}
			if tt.duplicate {
				id := int64(1)
				opts.QueuedMessageID = &id
			}
			orch := buildOrchestrator(d)
			var err error
			if tt.automaticRecovery {
				err = orch.RecoverSession(context.Background(), session)
			} else {
				err = orch.ContinueSession(context.Background(), session, opts)
			}
			if tt.duplicate {
				require.ErrorIs(t, err, agent.ErrContinuationInputAlreadyApplied, "already admitted queued ID must short-circuit before a recovery phase")
				require.Equal(t, 0, executions, "duplicate queued input cannot invoke provider")
				require.Equal(t, int64(0), runtimes.created.Load(), "duplicate input cannot allocate another runtime")
				require.Empty(t, d.sessions.getStatusUpdates(), "duplicate input cannot transition the session into running")
				return
			}
			require.NoError(t, err, "selected continuation should execute once with scoped task context")
			require.Equal(t, 1, executions, "one continuation should launch exactly one provider call")
			require.Equal(t, int64(1), runtimes.created.Load(), "ordinary nil options and automatic recovery must retain runtime admission")
			require.NotContains(t, actionable, "Sibling must stay isolated", "actionable context must remain in the selected primary thread")
			if tt.recovery || tt.automaticRecovery {
				require.Contains(t, actionable, "Old applied task", "explicit task recovery retains original instruction as context")
				require.Empty(t, inbox.admitted, "task context must not be admitted as new input")
				require.Equal(t, []string{oldKey}, d.uploads.opened, "explicit task recovery may rematerialize original task attachments as context")
			} else {
				require.Contains(t, actionable, "New pending follow-up", "ordinary prompt must contain the new unadmitted instruction")
				require.NotContains(t, actionable, "Old applied task", "an applied task without unfinished-runtime proof must stay in prior conversation history")
				require.Equal(t, [][]int64{{2}}, inbox.admitted, "only new pending input should enter the next provider phase")
				require.Equal(t, []string{newKey}, d.uploads.opened, "ordinary continuation may materialize only unadmitted input attachments")
			}
		})
	}
}

func (s *restartAdmissionInbox) ListAppliedRecoveryMessageIDs(_ context.Context, _, _, _ uuid.UUID, id int64, _ []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recoveryIDs) > 0 {
		return append([]int64(nil), s.recoveryIDs...), nil
	}
	return []int64{id}, nil
}

func TestContinueSession_NativeRecoveryPreservesAppliedBatchAndAttachments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose models.SessionExecutionPurpose
	}{{"explicit checkpoint retry", models.SessionExecutionPurposeCheckpointRetry}, {"owned job recovery", models.SessionExecutionPurposeCheckpointRecovery}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org := testOrg()
			issue := testIssue(org)
			session := testRun(org, issue.ID)
			thread := uuid.New()
			session.PrimaryThreadID = &thread
			session.Origin = models.SessionOriginManual
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.AgentSessionID = strPtr("checkpoint-before-combined-seed")
			session.ContainerID = strPtr("synthetic-existing-sandbox")
			session.SandboxState = models.SandboxStateRunning
			session.InteractionMode = models.SessionInteractionModeInteractive
			session.ValidationPolicy = models.SessionValidationPolicySkip
			d := defaultDeps()
			d.adapter.resumeMode = agent.ResumeBySessionID
			inbox := newRestartAdmissionInbox(10, 12, 13)
			inbox.recoveryIDs = []int64{12, 13}
			runtime := &reviewSetupLifecycleRuntimeStore{}
			d.threadInbox, d.threadRuntimes = inbox, runtime
			firstKey, secondKey := org.String()+"/2026-05/first.png", org.String()+"/2026-05/second.png"
			d.uploads = &mockUploadStore{files: map[string]mockUploadFile{firstKey: {body: []byte("first"), contentType: "image/png"}, secondKey: {body: []byte("second"), contentType: "image/png"}}}
			d.messages.messages = []models.SessionMessage{
				{ID: 10, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Unrelated previously applied task"},
				{ID: 11, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Previous checkpoint response"},
				{ID: 12, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 2, Content: "First combined instruction", Attachments: []string{"/api/v1/uploads/files/" + firstKey}},
				{ID: 13, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 2, Content: "Second combined instruction", Attachments: []string{"/api/v1/uploads/files/" + secondKey}},
			}
			var promptSeen *agent.AgentPrompt
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				promptSeen = prompt
				return &agent.AgentResult{Summary: "Recovered combined task", ExitCode: 0}, nil
			}
			opts := &agent.ContinueSessionOptions{ThreadID: &thread, ExecutionPurpose: tt.purpose}
			if tt.purpose == models.SessionExecutionPurposeCheckpointRecovery {
				id := int64(13)
				old := uuid.New()
				opts.QueuedMessageID = &id
				opts.RecoveryRuntimeID = &old
				opts.RecoveryMessageIDs = []int64{12, 13}
			}
			var err error
			if tt.purpose == models.SessionExecutionPurposeCheckpointRecovery {
				err = buildOrchestrator(d).RecoverSessionWithOptions(context.Background(), session, opts)
			} else {
				err = buildOrchestrator(d).ContinueSession(context.Background(), session, opts)
			}
			require.NoError(t, err, "native recovery must retain the complete admitted task context")
			require.NotNil(t, promptSeen, "recovered task must execute once")
			require.True(t, promptSeen.Continuation, "test must exercise native provider resume, which does not reconstruct scoped history")
			require.Contains(t, promptSeen.UserMessage, "First combined instruction\n\n---\n\nSecond combined instruction", "both admitted seed instructions must survive a checkpoint that predates their batch")
			require.NotContains(t, promptSeen.UserMessage, "Unrelated previously applied task", "prior applied conversation must not become actionable recovery context")
			require.Equal(t, []string{firstKey, secondKey}, d.uploads.opened, "both original task attachments must be rematerialized in message order")
			require.Empty(t, inbox.admitted, "context recovery cannot admit applied inputs again")
			require.Empty(t, inbox.claimed, "context recovery cannot reclaim already applied seeds")
		})
	}
}

func TestContinueSession_FollowUpPreservesUnfinishedTaskContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                 string
		snapshot, freshProvider, startupNote bool
	}{
		{name: "native resume in existing sandbox"},
		{name: "native resume from older snapshot", snapshot: true},
		{name: "first provider turn from older snapshot", snapshot: true, freshProvider: true},
		{name: "startup error note does not hide failed task", snapshot: true, startupNote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org := testOrg()
			issue := testIssue(org)
			session := testRun(org, issue.ID)
			thread, sibling := uuid.New(), uuid.New()
			session.PrimaryThreadID = &thread
			session.Origin = models.SessionOriginManual
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.InteractionMode = models.SessionInteractionModeInteractive
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.AgentSessionID = strPtr("checkpoint-before-failed-task")
			session.ContainerID = strPtr("synthetic-existing-sandbox")
			session.SandboxState = models.SandboxStateRunning
			d := defaultDeps()
			d.adapter.resumeMode = agent.ResumeBySessionID
			if tt.snapshot {
				session.ContainerID = nil
				session.SandboxState = models.SandboxStateNone
				session.SnapshotKey = strPtr("snapshots/synthetic/prior-task.tar")
				d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("prior checkpoint")}
			}
			if tt.freshProvider {
				session.AgentSessionID = nil
			}
			inbox := newRestartAdmissionInbox(10, 12)
			inbox.unsettledIDs = []int64{12}
			d.threadInbox, d.threadRuntimes = inbox, &reviewSetupLifecycleRuntimeStore{}
			oldKey, newKey := org.String()+"/2026-05/task.png", org.String()+"/2026-05/follow-up.png"
			d.uploads = &mockUploadStore{files: map[string]mockUploadFile{oldKey: {body: []byte("task"), contentType: "image/png"}, newKey: {body: []byte("follow-up"), contentType: "image/png"}}}
			d.messages.messages = []models.SessionMessage{
				{ID: 10, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Unrelated settled task"},
				{ID: 11, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Completed previous task"},
				{ID: 12, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 2, Content: "Unfinished admitted task", Attachments: []string{"/api/v1/uploads/files/" + oldKey}},
			}
			if tt.startupNote {
				d.messages.messages = append(d.messages.messages, models.SessionMessage{ID: 13, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleAssistant, TurnNumber: 2, Content: "Authentication failed before retry execution"})
			}
			d.messages.messages = append(d.messages.messages,
				models.SessionMessage{ID: 14, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 2, Content: "New follow-up changes the request", Attachments: []string{"/api/v1/uploads/files/" + newKey}},
				models.SessionMessage{ID: 15, OrgID: org, SessionID: session.ID, ThreadID: &sibling, Role: models.MessageRoleUser, TurnNumber: 2, Content: "Sibling task stays isolated"})
			var seen *agent.AgentPrompt
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				seen = prompt
				return &agent.AgentResult{Summary: "Finished amended task", ExitCode: 0}, nil
			}
			require.NoError(t, buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &thread}), "follow-up must retain unfinished task context across restore paths")
			require.NotNil(t, seen, "follow-up must execute once")
			text := seen.UserMessage
			if text == "" {
				text = seen.UserPrompt
			}
			require.Contains(t, text, "Previous task context from an interrupted attempt:", "old applied input must be labeled as prior context")
			require.Contains(t, text, "Unfinished admitted task", "provider checkpoint may predate the failed turn")
			require.Contains(t, text, "Current message:\nNew follow-up changes the request", "new input remains the current instruction")
			require.NotContains(t, seen.UserMessage, "Unrelated settled task", "native continuation cannot reintroduce settled task instructions")
			require.NotContains(t, text, "Sibling task stays isolated", "context lookup remains thread scoped")
			require.Equal(t, []string{oldKey, newKey}, d.uploads.opened, "both unfinished task and current input attachments must survive restore")
			require.Equal(t, [][]int64{{14}}, inbox.admitted, "only the new follow-up may be admitted again")
			require.Equal(t, [][]int64{{14}}, inbox.claimed, "prior context cannot be reclaimed as provider input")
		})
	}
}
