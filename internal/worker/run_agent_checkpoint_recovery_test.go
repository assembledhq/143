package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type ownedRunAgentRecoveryStub struct {
	*orchestratorServiceStub
	recoverOwnedFn func(context.Context, *models.Session, *agent.ContinueSessionOptions) error
	calls          int
}

func (s *ownedRunAgentRecoveryStub) RecoverSessionWithOptions(ctx context.Context, session *models.Session, opts *agent.ContinueSessionOptions) error {
	s.calls++
	return s.recoverOwnedFn(ctx, session, opts)
}

func TestRunAgentHandlerRejectsMalformedCheckpointRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "unknown purpose", edit: func(p map[string]any) { p["execution_purpose"] = "unknown" }},
		{name: "explicit checkpoint retry is not a recorded attempt", edit: func(p map[string]any) { p["execution_purpose"] = "checkpoint_retry" }},
		{name: "unproven lineage", edit: func(p map[string]any) { p["recovery_unproven"] = true }},
		{name: "missing runtime", edit: func(p map[string]any) { delete(p, "recovery_runtime_id") }},
		{name: "zero runtime", edit: func(p map[string]any) { p["recovery_runtime_id"] = uuid.Nil.String() }},
		{name: "invalid thread", edit: func(p map[string]any) { p["thread_id"] = "invalid" }},
		{name: "missing queued boundary", edit: func(p map[string]any) { delete(p, "queued_message_id") }},
		{name: "nonpositive queued boundary", edit: func(p map[string]any) { p["queued_message_id"] = "0" }},
		{name: "negative preserved identity", edit: func(p map[string]any) { p["recovery_message_ids"] = []int64{-1} }},
		{name: "malformed preserved identities", edit: func(p map[string]any) { p["recovery_message_ids"] = []string{"invalid"} }},
		{name: "too many preserved identities", edit: func(p map[string]any) { p["recovery_message_ids"] = make([]int64, 1001) }},
		{name: "mixed automation turn", edit: func(p map[string]any) { p["automation_run_id"] = uuid.NewString() }},
		{name: "mixed human input", edit: func(p map[string]any) { p["human_input_request_id"] = uuid.NewString() }},
		{name: "mixed publication action", edit: func(p map[string]any) { p["post_success_action"] = "push_pr_changes" }},
		{name: "runtime without purpose", edit: func(p map[string]any) { delete(p, "execution_purpose") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := map[string]any{"session_id": uuid.NewString(), "org_id": uuid.NewString(), "thread_id": uuid.NewString(), "execution_purpose": "checkpoint_recovery", "recovery_runtime_id": uuid.NewString(), "queued_message_id": "7", "recovery_message_ids": []int64{6, 7}}
			tt.edit(payload)
			raw, err := json.Marshal(payload)
			require.NoError(t, err, "encode malformed internal recovery marker")
			require.NotPanics(t, func() {
				err = newRunAgentHandler(nil, nil, zerolog.Nop())(context.Background(), "run_agent", raw)
			}, "malformed recovery must be rejected before accessing stores or provider dependencies")
			require.Error(t, err, "malformed or unproven recovery must never fall back to fresh execution")
		})
	}
}

func TestRunAgentHandlerDispatchesExactCheckpointRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.SessionStatus
		result error
	}{
		{name: "running checkpoint continuation", status: models.SessionStatusRunning},
		{name: "pending after retry handoff", status: models.SessionStatusPending},
		{name: "idle after provider completion", status: models.SessionStatusIdle, result: agent.ErrContinuationInputAlreadyApplied},
		{name: "completed input replay", status: models.SessionStatusCompleted, result: fmt.Errorf("completed receipt: %w", agent.ErrContinuationInputAlreadyApplied)},
		{name: "partial lifecycle requires reconciliation", status: models.SessionStatusIdle, result: agent.ErrContinuationRecoveryNeedsReconciliation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			orgID, sessionID, issueID, threadID, runtimeID, jobID, jobToken := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			snapshot := "synthetic-checkpoint"
			mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(sessionID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(workerSessionRow(sessionID, issueID, orgID, tt.status, 2, nil, &snapshot)...))
			mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(sessionID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(workerSessionRow(sessionID, issueID, orgID, tt.status, 2, nil, &snapshot)...))
			boundary := int64(7)
			expected := &agent.ContinueSessionOptions{ThreadID: &threadID, QueuedMessageID: &boundary, ExecutionPurpose: models.SessionExecutionPurposeCheckpointRecovery, RecoveryRuntimeID: &runtimeID, RecoveryMessageIDs: []int64{6, 7}}
			orch := &ownedRunAgentRecoveryStub{orchestratorServiceStub: &orchestratorServiceStub{}}
			orch.recoverOwnedFn = func(ctx context.Context, session *models.Session, opts *agent.ContinueSessionOptions) error {
				require.Equal(t, expected, opts, "worker must preserve the exact recorded runtime and input lineage")
				require.Equal(t, orgID, session.OrgID, "recovery must remain scoped to the payload organization")
				require.Equal(t, sessionID, session.ID, "recovery must target the recorded session")
				gotJob, hasJob := jobctx.JobIDFromContext(ctx)
				gotToken, hasToken := jobctx.LockTokenFromContext(ctx)
				require.True(t, hasJob && hasToken, "checkpoint recovery must retain the owned job lease context")
				require.Equal(t, jobID, gotJob, "recovery must preserve the claiming job identity")
				require.Equal(t, jobToken, gotToken, "recovery must preserve the claiming lease token")
				return tt.result
			}
			raw, err := json.Marshal(map[string]any{"session_id": sessionID.String(), "org_id": orgID.String(), "thread_id": threadID.String(), "execution_purpose": "checkpoint_recovery", "recovery_runtime_id": runtimeID.String(), "queued_message_id": "7", "recovery_message_ids": []int64{6, 7}})
			require.NoError(t, err, "encode exact internal recovery lineage")
			ctx := jobctx.WithJobID(jobctx.WithLockToken(context.Background(), jobToken), jobID)
			err = newRunAgentHandler(stores, &Services{Orchestrator: orch}, zerolog.Nop())(ctx, "run_agent", raw)
			if errors.Is(tt.result, agent.ErrContinuationRecoveryNeedsReconciliation) {
				require.ErrorIs(t, err, tt.result, "partial lifecycle reconciliation must remain retryable without rewriting provider output")
			} else {
				require.NoError(t, err, "recovery success or already applied input must complete the job without a fresh run")
			}
			require.Equal(t, 1, orch.calls, "recorded recovery must use the typed path exactly once despite session status changes")
			require.Equal(t, 0, orch.runAgentCalls, "recorded recovery must not restart the session through the fresh path")
			require.Equal(t, 0, orch.recoverSessionCalls, "recorded lineage must never fall back to options-free recovery")
			require.NoError(t, mock.ExpectationsWereMet(), "recovery may read session state but must not perform extra state mutations")
		})
	}
}

func TestRunAgentHandlerRejectsUnsupportedExactRecovery(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(map[string]any{"session_id": uuid.NewString(), "org_id": uuid.NewString(), "thread_id": uuid.NewString(), "execution_purpose": "checkpoint_recovery", "recovery_runtime_id": uuid.NewString(), "queued_message_id": "7", "recovery_message_ids": []int64{7}})
	require.NoError(t, err, "encode valid recovery for an unsupported orchestrator")
	orch := &orchestratorServiceStub{}
	require.NotPanics(t, func() {
		err = newRunAgentHandler(nil, &Services{Orchestrator: orch}, zerolog.Nop())(context.Background(), "run_agent", raw)
	}, "unsupported typed recovery must fail before stores or provider access")
	var fatal *FatalError
	require.ErrorAs(t, err, &fatal, "missing exact recovery support must fail closed instead of losing lineage")
	require.Equal(t, 0, orch.runAgentCalls+orch.recoverSessionCalls, "unsupported recovery cannot invoke an unscoped fallback")
}
