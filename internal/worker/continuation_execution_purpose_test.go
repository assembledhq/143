package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestContinueSessionHandlerRejectsForgedRecoveryModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		extra map[string]any
	}{
		{"unknown purpose", map[string]any{"execution_purpose": "replay_unknown"}},
		{"unproven legacy recovery", map[string]any{"execution_purpose": "checkpoint_recovery", "recovery_unproven": true}},
		{"missing recovery runtime", map[string]any{"execution_purpose": "checkpoint_recovery", "thread_id": uuid.NewString(), "queued_message_id": "1"}},
		{"invalid preserved task identity", map[string]any{"execution_purpose": "checkpoint_recovery", "thread_id": uuid.NewString(), "queued_message_id": "1", "recovery_runtime_id": uuid.NewString(), "recovery_message_ids": []int64{-1}}},
		{"malformed preserved task identity", map[string]any{"execution_purpose": "checkpoint_recovery", "thread_id": uuid.NewString(), "queued_message_id": "1", "recovery_runtime_id": uuid.NewString(), "recovery_message_ids": []string{"unknown"}}},
		{"wrong purpose type", map[string]any{"execution_purpose": true}},
		{"missing thread", map[string]any{"execution_purpose": "checkpoint_retry"}},
		{"invalid thread", map[string]any{"execution_purpose": "checkpoint_retry", "thread_id": "invalid"}},
		{"zero thread", map[string]any{"execution_purpose": "checkpoint_retry", "thread_id": uuid.Nil.String()}},
		{"queued input cannot become retry", map[string]any{"execution_purpose": "checkpoint_retry", "thread_id": uuid.NewString(), "queued_message_id": "1"}},
		{"human input cannot become retry", map[string]any{"execution_purpose": "checkpoint_retry", "thread_id": uuid.NewString(), "human_input_request_id": uuid.NewString()}},
		{"automation cannot become retry", map[string]any{"execution_purpose": "checkpoint_retry", "thread_id": uuid.NewString(), "automation_run_id": uuid.NewString()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.extra)
			require.NoError(t, err, "encode invalid internal continuation payload")
			err = newContinueSessionHandler(nil, nil, zerolog.Nop())(context.Background(), "continue_session", raw)
			require.Error(t, err, "invalid purpose must fail before stores, credentials or provider access")
		})
	}
}

func TestContinueSessionHandlerPropagatesExecutionPurposeAndSkipsAppliedInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  models.SessionExecutionPurpose
		applied  bool
		recorded bool
	}{
		{name: "checkpoint task recovery", purpose: models.SessionExecutionPurposeCheckpointRetry},
		{name: "checkpoint retry after pre-admission attempt recording", purpose: models.SessionExecutionPurposeCheckpointRetry, recorded: true},
		{name: "duplicate queued input", applied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			org, session, issue, thread := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			snapshot := "checkpoint-synthetic"
			mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(session, org).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(workerSessionRow(session, issue, org, models.SessionStatusIdle, 2, nil, &snapshot)...))
			orch := &orchestratorServiceStub{continueSessionFn: func(_ context.Context, _ *models.Session, opts *agent.ContinueSessionOptions) error {
				require.NotNil(t, opts, "purpose must reach the orchestrator in execution-scoped options")
				require.Equal(t, tt.purpose, opts.ExecutionPurpose, "worker must preserve the validated purpose exactly")
				if tt.purpose == models.SessionExecutionPurposeCheckpointRetry {
					require.Nil(t, opts.QueuedMessageID, "recorded task retry must not become a new queued input request")
				}
				if tt.applied {
					return agent.ErrContinuationInputAlreadyApplied
				}
				return nil
			}}
			payload := map[string]any{"session_id": session.String(), "org_id": org.String(), "thread_id": thread.String(), "execution_purpose": tt.purpose}
			if tt.applied {
				payload["queued_message_id"] = "7"
			}
			if tt.recorded {
				payload["continuation_runtime_id"] = uuid.NewString()
				payload["continuation_message_id"] = "7"
				payload["continuation_task_message_ids"] = []int64{6, 7}
			}
			raw, err := json.Marshal(payload)
			require.NoError(t, err, "marshal scoped continuation")
			err = newContinueSessionHandler(stores, &Services{Orchestrator: orch}, zerolog.Nop())(context.Background(), "continue_session", raw)
			require.NoError(t, err, "duplicate application is a successful job no-op, while explicit retry retains its context purpose")
			require.Equal(t, 1, orch.continueSessionCalls, "handler delegates once without executing post-success work for duplicate input")
			require.NoError(t, mock.ExpectationsWereMet(), "no additional state writes or publication effects are permitted for applied input")
		})
	}
}
