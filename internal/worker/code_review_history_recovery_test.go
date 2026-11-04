package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHistoryDispatchRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		started       bool
		fallback      bool
		changedModel  bool
		changedAgent  bool
		completedTurn bool
		retargeted    bool
		expired       bool
	}{
		{name: "interrupted dispatch and edited description finish incomplete"},
		{name: "interrupted dispatch and changed model finish incomplete", changedModel: true},
		{name: "idle thread already retargeted by an earlier retry finishes incomplete", changedModel: true, retargeted: true},
		{name: "interrupted dispatch and changed provider finish incomplete", changedAgent: true},
		{name: "started primary recovers saved inputs without redispatch", started: true},
		{name: "started primary ignores new provider availability", started: true, changedAgent: true},
		{name: "started fallback recovers the bound attempt", started: true, fallback: true},
		{name: "started fallback recovers after the dispatch deadline", started: true, fallback: true, expired: true},
		{name: "unsent fallback stays undispatched after the deadline", fallback: true, expired: true},
		{name: "completed turn recovers missing result for harvest", started: true, completedTurn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
			job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, HeadSHA: "reviewed-head", RequestContext: &codereviewsvc.ReviewRequestContext{UseReviewHistory: true}}
			cfg := models.DefaultCodeReviewPolicyConfig()
			cfg.AgentRoster.Orchestrator = models.AgentTypeCodex
			cfg.AgentRoster.OrchestratorModel = stringPtr(models.DefaultCodexModel)
			policy := codeReviewPolicyRecordForTest(cfg)
			metadata := models.CodeReviewSessionMetadata{CreatedAt: time.Now(), PromptRecordKey: stringPtr("captured-prompts")}
			if tt.expired {
				metadata.CreatedAt = time.Now().Add(-24 * time.Hour)
			}
			provider, model, attempt := models.AgentTypeCodex, models.DefaultCodexModel, 0
			var results []models.CodeReviewAgentResult
			if tt.fallback {
				attempt, provider, model = 1, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel
				results = []models.CodeReviewAgentResult{codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator)}
			}
			visual := models.CodeReviewVisualEvidenceSnapshot{}
			originalPR := models.PullRequest{Title: "Original title"}
			originalHash := codeReviewDescriptionInputHash(originalPR, visual)
			key := codeReviewOrchestratorPromptKey("captured-prompts", attempt, provider)
			record := models.CodeReviewPromptRecord{OrgID: orgID, SessionID: sessionID, RecordKey: key, Role: "orchestrator", AgentProvider: string(provider), Metadata: mustMarshalCodeReviewJSON(map[string]any{
				"head_sha": job.HeadSHA, "policy_version": policy.Version, "agent_model": model,
				"input_hash": originalHash, "visual_evidence_hash": visual.CanonicalHash(),
			})}
			// This is the checkpoint committed before SendMessage. A transient
			// send failure or a crash before its result write leaves it intact.
			checkpointStore := &historyPromptRecords{}
			_, err := prepareCodeReviewHistoryPrompt(context.Background(), checkpointStore, job, threadID, record, func(*prompts.CodeReviewHistoryPromptData) string { return "immutable original prompt" })
			require.NoError(t, err, "save the original pre-dispatch checkpoint")
			saved := *checkpointStore.record
			mock.ExpectQuery("(?s)SELECT .*FROM code_review_prompt_records").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "record_key", "role", "agent_provider", "content", "metadata", "created_at"}).
					AddRow(uuid.New(), orgID, sessionID, key, "orchestrator", string(provider), saved.Content, saved.Metadata, time.Now()))
			status := models.ThreadStatusIdle
			if tt.started {
				status = models.ThreadStatusRunning
			}
			if tt.completedTurn {
				status = models.ThreadStatusCompleted
			}
			threadRow := workerSessionThreadRow(threadID, sessionID, orgID, provider, stringPtr(model), status)
			setWorkerSessionThreadColumn(threadRow, "current_turn", 0)
			if tt.retargeted {
				setWorkerSessionThreadColumn(threadRow, "model_override", stringPtr(models.CodexModelGPT55))
			}
			if tt.started {
				setWorkerSessionThreadColumn(threadRow, "current_turn", 1)
			}
			if tt.fallback {
				setWorkerSessionThreadColumn(threadRow, "execution_mode", models.ThreadExecutionModeReview)
				setWorkerSessionThreadColumn(threadRow, "filesystem_mode", models.ThreadFilesystemModeReadOnly)
			}
			mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"org_id": orgID, "id": threadID}).WillReturnRows(newSessionThreadRows().AddRow(threadRow...))
			expectedStatus := models.CodeReviewAgentResultStatusFailed
			if tt.started {
				expectedStatus = models.CodeReviewAgentResultStatusRunning
			}
			var savedState codeReviewOrchestratorStructuredResult
			expiredUnsent := tt.expired && !tt.started
			if !expiredUnsent {
				mock.ExpectQuery("INSERT INTO code_review_agent_results").WithArgs(orgID, sessionID, string(provider), stringPtr(model), models.CodeReviewAgentRoleOrchestrator, expectedStatus,
					pgxmock.AnyArg(), codeReviewFallbackArg(func(value any) bool {
						encoded, ok := value.(json.RawMessage)
						if !ok {
							return false
						}
						savedState, ok = parseCodeReviewOrchestratorStructuredResult(encoded)
						return ok
					})).WillReturnRows(newCodeReviewAgentResultRows().AddRow(uuid.New(), orgID, sessionID, string(provider), stringPtr(model), models.CodeReviewAgentRoleOrchestrator, expectedStatus, nil, json.RawMessage(`{}`), time.Now()))
			}
			currentPR := models.PullRequest{Title: "Edited after dispatch was interrupted"}
			services := &Services{}
			if tt.changedModel {
				cfg.AgentRoster.OrchestratorModel = stringPtr(models.CodexModelGPT55)
				policy = codeReviewPolicyRecordForTest(cfg)
			}
			if tt.changedAgent {
				services.CodingAgents = codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{models.AgentTypeClaudeCode: true}}
			}
			err = ensureCodeReviewOrchestratorThread(context.Background(), stores, services, zerolog.Nop(), job, currentPR, nil, policy, metadata, nil, results, nil, visual)
			require.NoError(t, err, "checkpoint recovery must settle the attempt without a generic job retry")
			if expiredUnsent {
				require.NoError(t, mock.ExpectationsWereMet(), "an expired unsent fallback must not create an agent result or send a new turn")
				return
			}
			expectedState := codeReviewOrchestratorStructuredResult{ThreadID: threadID.String(), PromptRecordKey: key, DescriptionInputHash: originalHash, ReadOnly: tt.fallback}
			if !tt.started {
				require.Contains(t, savedState.Error, codeReviewHistoryInputsChangedMessage, "unsent changed inputs should produce an actionable terminal failure")
				require.NotEmpty(t, savedState.CompletedAt, "failed attempt should have a terminal timestamp")
				expectedState.Error, expectedState.CompletedAt = savedState.Error, savedState.CompletedAt
			}
			require.Equal(t, expectedState, savedState, "recovery must bind the result to the original prompt and original description hash")
			require.NotEqual(t, codeReviewDescriptionInputHash(currentPR, visual), savedState.DescriptionInputHash, "a recovered result must not claim that the edited description was reviewed")
			require.Equal(t, 1, checkpointStore.writes, "recovery must not overwrite the initial prompt checkpoint")
			require.NoError(t, mock.ExpectationsWereMet(), "recovery must only read the checkpoint and thread and save a result, without updating a thread or sending another message")
			persisted := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: expectedStatus, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(savedState)}
			err = ensureCodeReviewOrchestratorThread(context.Background(), nil, nil, zerolog.Nop(), job, currentPR, nil, policy, metadata, nil, append(results, persisted), nil, visual)
			require.NoError(t, err, "the settled attempt must not repeat the same dispatch check on the next poll")
			if !tt.started {
				require.Equal(t, "143 stopped the final synthesis because its saved review inputs changed before it could start. The automated review is incomplete; request a new full review to use the updated inputs.", codeReviewOrchestratorOperationalSummary([]models.CodeReviewAgentResult{persisted}, []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonOrchestratorSynthesisInvalid}}), "the public review should explain how to recover from changed inputs")
			}
		})
	}
}
