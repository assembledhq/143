package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const codeReviewCapacityFailureForTest = "Selected model is at capacity. Please try a different model."

func codeReviewFailedModelForTest(agentType models.AgentType, model string, role models.CodeReviewAgentRole) models.CodeReviewAgentResult {
	return models.CodeReviewAgentResult{
		AgentProvider: string(agentType),
		AgentModel:    stringPtr(model),
		Role:          role,
		Status:        models.CodeReviewAgentResultStatusFailed,
		RawOutput:     stringPtr(codeReviewCapacityFailureForTest),
		StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
			Error: codeReviewCapacityFailureForTest,
		}),
	}
}

func TestCodeReviewModelUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected bool
	}{
		{name: "Codex capacity", message: "codex CLI exited with code 1: " + codeReviewCapacityFailureForTest, expected: true},
		{name: "case insensitive capacity", message: "Selected MODEL IS AT CAPACITY", expected: true},
		{name: "provider overload", message: `{"type":"overloaded_error"}`, expected: true},
		{name: "service temporarily unavailable", message: "503 Service Unavailable", expected: true},
		{name: "rate limited", message: "429 Too many requests", expected: true},
		{name: "subscription usage limit", message: "You have reached your usage limit", expected: true},
		{name: "sandbox capacity is unrelated", message: "sandbox capacity reached: 2/2 sandboxes active"},
		{name: "malformed synthesis is unrelated", message: "orchestrator synthesis is missing required fields"},
		{name: "invalid model is unrelated", message: "model does not exist"},
		{name: "cancellation is unrelated", message: "context canceled"},
		{name: "deadline is unrelated", message: "context deadline exceeded"},
		{name: "empty failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewModelUnavailable(tt.message), "only upstream model availability failures should trigger fallback")
		})
	}
}

func TestResolveCodeReviewOrchestratorRuntimeFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		results        func() []models.CodeReviewAgentResult
		available      map[models.AgentType]bool
		expectedAgent  models.AgentType
		expectedModel  string
		expectedEffort models.ReasoningEffort
		expectedReady  bool
	}{
		{
			name: "skips the model that already failed during review",
			results: func() []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleReviewer)}
			},
			expectedAgent: models.AgentTypeClaudeCode, expectedModel: models.DefaultClaudeCodeModel, expectedEffort: models.ReasoningEffortMax, expectedReady: true,
		},
		{
			name: "moves from failed primary to first fallback",
			results: func() []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator)}
			},
			expectedAgent: models.AgentTypeClaudeCode, expectedModel: models.DefaultClaudeCodeModel, expectedEffort: models.ReasoningEffortMax, expectedReady: true,
		},
		{
			name: "another model on the same provider can be a second fallback",
			results: func() []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{
					codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator),
					codeReviewFailedModelForTest(models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel, models.CodeReviewAgentRoleOrchestrator),
				}
			},
			expectedAgent: models.AgentTypeCodex, expectedModel: models.CodexModelGPT55, expectedEffort: models.ReasoningEffortMedium, expectedReady: true,
		},
		{
			name: "all attempted models exhaust the chain",
			results: func() []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{
					codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator),
					codeReviewFailedModelForTest(models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel, models.CodeReviewAgentRoleOrchestrator),
					codeReviewFailedModelForTest(models.AgentTypeCodex, models.CodexModelGPT55, models.CodeReviewAgentRoleOrchestrator),
				}
			},
			expectedAgent: models.AgentTypeCodex, expectedModel: models.DefaultCodexModel, expectedEffort: models.ReasoningEffortHigh,
		},
		{
			name: "unavailable credentials skip to next model",
			results: func() []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator)}
			},
			available:     map[models.AgentType]bool{models.AgentTypeCodex: true},
			expectedAgent: models.AgentTypeCodex, expectedModel: models.CodexModelGPT55, expectedEffort: models.ReasoningEffortMedium, expectedReady: true,
		},
		{
			name: "completed reviewer text cannot mark a model unavailable",
			results: func() []models.CodeReviewAgentResult {
				result := codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleReviewer)
				result.Status = models.CodeReviewAgentResultStatusCompleted
				return []models.CodeReviewAgentResult{result}
			},
			expectedAgent: models.AgentTypeCodex, expectedModel: models.DefaultCodexModel, expectedEffort: models.ReasoningEffortHigh, expectedReady: true,
		},
		{
			name: "legacy omitted model uses provider default",
			results: func() []models.CodeReviewAgentResult {
				result := codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleReviewer)
				result.AgentModel = nil
				result.StructuredResult = nil
				return []models.CodeReviewAgentResult{result}
			},
			expectedAgent: models.AgentTypeClaudeCode, expectedModel: models.DefaultClaudeCodeModel, expectedEffort: models.ReasoningEffortMax, expectedReady: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := models.DefaultCodeReviewPolicyConfig()
			cfg.AgentRoster.Orchestrator = models.AgentTypeCodex
			cfg.AgentRoster.OrchestratorModel = stringPtr(models.DefaultCodexModel)
			cfg.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeCodex}
			cfg.AgentRoster.ReviewerModels = []string{models.DefaultCodexModel, models.DefaultClaudeCodeModel, models.CodexModelGPT55}
			cfg.AgentRoster.ReviewerReasoningEfforts = []models.ReasoningEffort{models.ReasoningEffortLow, models.ReasoningEffortMax, models.ReasoningEffortMedium}
			var services *Services
			if tt.available != nil {
				services = &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: tt.available}}
			}
			selection, err := resolveCodeReviewOrchestratorAvailability(context.Background(), services, uuid.New(), cfg, tt.results())
			require.NoError(t, err, "saved runtime failures should allow fallback selection")
			require.Equal(t, codeReviewOrchestratorSelection{
				AgentType: tt.expectedAgent, AgentModel: stringPtr(tt.expectedModel), ReasoningEffort: reasoningEffortPtr(tt.expectedEffort), Available: tt.expectedReady,
			}, selection, "fallback should use the next unattempted configured model and its own reasoning effort")
			require.Equal(t, []codeReviewOrchestratorSelection{
				{AgentType: models.AgentTypeCodex, AgentModel: stringPtr(models.DefaultCodexModel), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortHigh), Available: true},
				{AgentType: models.AgentTypeClaudeCode, AgentModel: stringPtr(models.DefaultClaudeCodeModel), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortMax), Available: true},
				{AgentType: models.AgentTypeCodex, AgentModel: stringPtr(models.CodexModelGPT55), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortMedium), Available: true},
			}, codeReviewOrchestratorCandidates(cfg), "duplicate primary/reviewer models should be attempted only once")
		})
	}
}

func TestCodeReviewOrchestratorCompletionAcrossFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		statuses      []models.CodeReviewAgentResultStatus
		terminal      bool
		fallback      bool
		expectedPhase codeReviewAgentPhase
	}{
		{name: "not started"},
		{name: "primary failed", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed}, terminal: true, fallback: true},
		{name: "fallback running", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusRunning}, expectedPhase: codeReviewAgentPhaseOrchestrator},
		{name: "fallback queued", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusQueued}, expectedPhase: codeReviewAgentPhaseOrchestrator},
		{name: "fallback completed", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusCompleted}, terminal: true},
		{name: "two capacity failures", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusFailed}, terminal: true, fallback: true},
		{name: "timeout stops retries", statuses: []models.CodeReviewAgentResultStatus{models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusTimedOut}, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var results []models.CodeReviewAgentResult
			var threads []models.SessionThread
			for _, status := range tt.statuses {
				threadID := uuid.New()
				result := codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator)
				result.Status = status
				result.StructuredResult = marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: threadID.String(), Error: codeReviewCapacityFailureForTest})
				results = append(results, result)
				threads = append(threads, models.SessionThread{ID: threadID, Status: models.ThreadStatusRunning})
			}
			require.Equal(t, tt.terminal, codeReviewOrchestratorTerminal(results), "historical failures must not complete an active fallback")
			require.Equal(t, tt.fallback, codeReviewOrchestratorNeedsFallback(results), "only terminal availability failures should request another model")
			require.Equal(t, tt.expectedPhase, codeReviewInFlightAgentPhaseFromState(models.DefaultCodeReviewPolicyConfig(), results, threads), "historical failures must not disable synthesis polling")
		})
	}
}

func TestHarvestCodeReviewOrchestratorCapacityFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      *string
		explanation string
	}{
		{name: "failure in transcript", output: stringPtr(codeReviewCapacityFailureForTest), explanation: codeReviewCapacityFailureForTest},
		{name: "failure without assistant output", explanation: codeReviewCapacityFailureForTest},
		{name: "generic explanation with provider error in output", output: stringPtr(codeReviewCapacityFailureForTest), explanation: "agent runtime failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			orgID, sessionID, threadID, resultID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Now().UTC()
			state := marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: threadID.String()})
			mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
				WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "codex", stringPtr(models.DefaultCodexModel), models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusRunning, nil, state, now))
			row := workerSessionThreadRow(threadID, sessionID, orgID, models.AgentTypeCodex, stringPtr(models.DefaultCodexModel), models.ThreadStatusFailed)
			setWorkerSessionThreadColumn(row, "failure_explanation", &tt.explanation)
			setWorkerSessionThreadColumn(row, "completed_at", &now)
			mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
				WillReturnRows(newSessionThreadRows().AddRow(row...))
			messages := newSessionMessageRows()
			if tt.output != nil {
				messages.AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, *tt.output, nil, nil, nil, nil, "", now)
			}
			mock.ExpectQuery("(?s)SELECT .*FROM session_messages").WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).WillReturnRows(messages)
			mock.ExpectQuery("UPDATE code_review_agent_results").
				WithArgs(models.CodeReviewAgentResultStatusFailed, stringPtr(codeReviewCapacityFailureForTest), codeReviewCapacityStateArg{}, orgID, resultID).
				WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "codex", stringPtr(models.DefaultCodexModel), models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusFailed, stringPtr(codeReviewCapacityFailureForTest), state, now))
			err = harvestCodeReviewOrchestratorResult(context.Background(), &Stores{
				CodeReviews: db.NewCodeReviewStore(mock), SessionThreads: db.NewSessionThreadStore(mock), SessionMessages: db.NewSessionMessageStore(mock),
			}, nil, zerolog.Nop(), runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}, codeReviewPolicyRecordForTest(models.DefaultCodeReviewPolicyConfig()), nil, models.CodeReviewVisualEvidenceSnapshot{})
			require.NoError(t, err, "capacity output should be saved as a runtime failure")
			require.NoError(t, mock.ExpectationsWereMet(), "capacity failure should not send a repair turn or create findings")
		})
	}
}

type codeReviewCapacityStateArg struct{}

func (codeReviewCapacityStateArg) Match(value any) bool {
	raw, ok := value.(json.RawMessage)
	if !ok {
		return false
	}
	state, ok := parseCodeReviewOrchestratorStructuredResult(raw)
	return ok && state.Error == codeReviewCapacityFailureForTest && state.CompletedAt != "" &&
		!state.SynthesisValidated && state.SynthesisRepairCount == 0 && !state.SynthesisRepairPending
}

type codeReviewFallbackThreadStub struct {
	existing   []models.SessionThread
	created    *models.SessionThread
	archived   *models.SessionThread
	input      *threadsvc.CreateThreadInput
	archiveID  uuid.UUID
	listErr    error
	archiveErr error
	createErr  error
}

func (s *codeReviewFallbackThreadStub) ListThreads(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionThread, error) {
	return s.existing, s.listErr
}

func (s *codeReviewFallbackThreadStub) CreateThread(_ context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	s.input = &input
	if s.createErr != nil {
		return nil, s.createErr
	}
	if codeReviewVisibleThreadCount(s.existing) >= models.MaxThreadsPerSession {
		return nil, db.ErrThreadLimitReached
	}
	if s.created != nil {
		created := *s.created
		s.existing = append(s.existing, created)
		return &created, nil
	}
	created := models.SessionThread{
		ID:              uuid.New(),
		SessionID:       input.SessionID,
		OrgID:           input.OrgID,
		AgentType:       models.AgentType(input.AgentType),
		ModelOverride:   stringPtr(input.Model),
		ReasoningEffort: input.ReasoningEffort,
		Label:           input.Label,
		FileScope:       append([]string(nil), input.FileScope...),
		Status:          models.ThreadStatusIdle,
		CreatedBySource: input.CreatedBySource,
		ExecutionMode:   input.ExecutionMode,
		FilesystemMode:  input.FilesystemMode,
	}
	s.existing = append(s.existing, created)
	return &created, nil
}

func (s *codeReviewFallbackThreadStub) ArchiveThread(_ context.Context, _, _, threadID uuid.UUID) (models.SessionThread, error) {
	s.archiveID = threadID
	if s.archiveErr != nil {
		return models.SessionThread{}, s.archiveErr
	}
	for i := range s.existing {
		if s.existing[i].ID != threadID {
			continue
		}
		now := time.Now().UTC()
		s.existing[i].ArchivedAt = &now
		if s.archived != nil {
			archived := *s.archived
			archived.ID = threadID
			return archived, nil
		}
		return s.existing[i], nil
	}
	return models.SessionThread{}, threadsvc.ErrThreadNotFound
}

func codeReviewTerminalReviewerResultForThread(threadID uuid.UUID, agentType models.AgentType, model string) models.CodeReviewAgentResult {
	return models.CodeReviewAgentResult{
		AgentProvider: string(agentType),
		AgentModel:    stringPtr(model),
		Role:          models.CodeReviewAgentRoleReviewer,
		Status:        models.CodeReviewAgentResultStatusCompleted,
		StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ThreadID:    threadID.String(),
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		}),
	}
}

func codeReviewOrchestratorResultForThread(threadID uuid.UUID, agentType models.AgentType, model string, status models.CodeReviewAgentResultStatus) models.CodeReviewAgentResult {
	result := codeReviewFailedModelForTest(agentType, model, models.CodeReviewAgentRoleOrchestrator)
	result.Status = status
	result.StructuredResult = marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
		ThreadID:    threadID.String(),
		Error:       stringPtrValue(result.RawOutput),
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if status != models.CodeReviewAgentResultStatusFailed {
		result.RawOutput = nil
	}
	return result
}

func TestEnsureCodeReviewFallbackOrchestratorThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		attempt         int
		existingThreads func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread
		results         func(threads []models.SessionThread) []models.CodeReviewAgentResult
		primaryThreadID func(threads []models.SessionThread) uuid.UUID
		listErr         error
		archiveErr      error
		createErr       error
		expectArchiveID func(threads []models.SessionThread) uuid.UUID
		expectErrIs     error
		expectCreate    bool
		expectReuse     bool
	}{
		{
			name:         "creates a separate synthesis thread below the visible-thread limit",
			expectCreate: true,
		},
		{
			name: "reuses deterministic fallback before reclaiming any slot",
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{{
					ID:              uuid.New(),
					OrgID:           job.OrgID,
					SessionID:       job.SessionID,
					AgentType:       selection.AgentType,
					ModelOverride:   selection.AgentModel,
					Label:           "Code review synthesis: claude_code (fallback 1)",
					CreatedBySource: models.ThreadCreatedBySourceSystem,
					Status:          models.ThreadStatusCompleted,
				}}
			},
			expectReuse: true,
		},
		{
			name: "archives one persisted terminal reviewer to make room for the first fallback",
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusIdle},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeOpenCode, Label: "Reviewer 3", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
				}
			},
			results: func(threads []models.SessionThread) []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{
					codeReviewTerminalReviewerResultForThread(threads[1].ID, models.AgentTypeCodex, models.DefaultCodexModel),
					codeReviewTerminalReviewerResultForThread(threads[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel),
					codeReviewTerminalReviewerResultForThread(threads[3].ID, models.AgentTypeOpenCode, models.OpenCodeModelGPT55),
				}
			},
			primaryThreadID: func(threads []models.SessionThread) uuid.UUID { return threads[0].ID },
			expectArchiveID: func(threads []models.SessionThread) uuid.UUID { return threads[1].ID },
			expectCreate:    true,
		},
		{
			name:    "archives a failed prior fallback before touching reviewer evidence on the second fallback",
			attempt: 2,
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusIdle},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, ModelOverride: selection.AgentModel, Label: "Code review synthesis: claude_code (fallback 1)", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusFailed},
				}
			},
			results: func(threads []models.SessionThread) []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{
					codeReviewTerminalReviewerResultForThread(threads[1].ID, models.AgentTypeCodex, models.DefaultCodexModel),
					codeReviewTerminalReviewerResultForThread(threads[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel),
					codeReviewOrchestratorResultForThread(threads[3].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel, models.CodeReviewAgentResultStatusFailed),
				}
			},
			primaryThreadID: func(threads []models.SessionThread) uuid.UUID { return threads[0].ID },
			expectArchiveID: func(threads []models.SessionThread) uuid.UUID { return threads[3].ID },
			expectCreate:    true,
		},
		{
			name: "leaves Main, active, user-created, and unpersisted threads untouched when no safe archival slot exists",
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceUser, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeOpenCode, Label: "Reviewer 3", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusRunning},
				}
			},
			results: func(threads []models.SessionThread) []models.CodeReviewAgentResult {
				duplicateRunning := codeReviewTerminalReviewerResultForThread(threads[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel)
				duplicateRunning.Status = models.CodeReviewAgentResultStatusRunning
				duplicateRunning.StructuredResult = marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
					ThreadID: threads[2].ID.String(),
				})
				return []models.CodeReviewAgentResult{
					codeReviewTerminalReviewerResultForThread(threads[0].ID, models.AgentTypeCodex, models.DefaultCodexModel),
					codeReviewTerminalReviewerResultForThread(threads[1].ID, models.AgentTypeCodex, models.DefaultCodexModel),
					codeReviewTerminalReviewerResultForThread(threads[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel),
					duplicateRunning,
					codeReviewTerminalReviewerResultForThread(threads[3].ID, models.AgentTypeOpenCode, models.OpenCodeModelGPT55),
				}
			},
			primaryThreadID: func(threads []models.SessionThread) uuid.UUID { return threads[0].ID },
			expectErrIs:     errCodeReviewFallbackCapacityExhausted,
		},
		{
			name: "protects terminal threads with pending queued follow-up work",
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusIdle},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted, PendingMessageCount: 1},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeOpenCode, Label: "Reviewer 3", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
				}
			},
			results: func(threads []models.SessionThread) []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{
					codeReviewTerminalReviewerResultForThread(threads[1].ID, models.AgentTypeCodex, models.DefaultCodexModel),
					codeReviewTerminalReviewerResultForThread(threads[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel),
					codeReviewTerminalReviewerResultForThread(threads[3].ID, models.AgentTypeOpenCode, models.OpenCodeModelGPT55),
				}
			},
			primaryThreadID: func(threads []models.SessionThread) uuid.UUID { return threads[0].ID },
			expectArchiveID: func(threads []models.SessionThread) uuid.UUID { return threads[2].ID },
			expectCreate:    true,
		},
		{
			name: "gracefully treats an archive race as exhausted fallback capacity",
			existingThreads: func(job runCodeReviewPayload, selection codeReviewOrchestratorSelection) []models.SessionThread {
				return []models.SessionThread{
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusIdle},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
					{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeOpenCode, Label: "Reviewer 3", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
				}
			},
			results: func(threads []models.SessionThread) []models.CodeReviewAgentResult {
				return []models.CodeReviewAgentResult{codeReviewTerminalReviewerResultForThread(threads[1].ID, models.AgentTypeCodex, models.DefaultCodexModel)}
			},
			primaryThreadID: func(threads []models.SessionThread) uuid.UUID { return threads[0].ID },
			archiveErr:      threadsvc.ErrThreadActive,
			expectArchiveID: func(threads []models.SessionThread) uuid.UUID { return threads[1].ID },
			expectErrIs:     errCodeReviewFallbackCapacityExhausted,
		},
		{
			name:         "gracefully treats a create limit race as exhausted fallback capacity",
			createErr:    db.ErrThreadLimitReached,
			expectErrIs:  errCodeReviewFallbackCapacityExhausted,
			expectCreate: true,
		},
		{name: "propagates list failure", listErr: errors.New("list failed"), expectErrIs: errors.New("list failed")},
		{name: "propagates create failure", createErr: errors.New("create failed"), expectErrIs: errors.New("create failed"), expectCreate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
			selection := codeReviewOrchestratorSelection{AgentType: models.AgentTypeClaudeCode, AgentModel: stringPtr(models.DefaultClaudeCodeModel), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortMax), Available: true}
			thread := models.SessionThread{
				ID:              uuid.New(),
				OrgID:           job.OrgID,
				SessionID:       job.SessionID,
				AgentType:       selection.AgentType,
				ModelOverride:   selection.AgentModel,
				Label:           "Code review synthesis: claude_code (fallback 1)",
				CreatedBySource: models.ThreadCreatedBySourceSystem,
				Status:          models.ThreadStatusIdle,
			}
			attempt := tt.attempt
			if attempt == 0 {
				attempt = 1
			}
			thread.Label = fmt.Sprintf("Code review synthesis: claude_code (fallback %d)", attempt)
			stub := &codeReviewFallbackThreadStub{created: &thread, archived: &models.SessionThread{}, listErr: tt.listErr, archiveErr: tt.archiveErr, createErr: tt.createErr}
			if tt.existingThreads != nil {
				stub.existing = tt.existingThreads(job, selection)
			}
			var results []models.CodeReviewAgentResult
			if tt.results != nil {
				results = tt.results(stub.existing)
			}
			primaryThreadID := uuid.Nil
			if tt.primaryThreadID != nil {
				primaryThreadID = tt.primaryThreadID(stub.existing)
			}
			actual, err := ensureCodeReviewFallbackOrchestratorThread(context.Background(), stub, job, selection, attempt, []string{"handler.go"}, primaryThreadID, results)
			if tt.expectErrIs != nil {
				if tt.expectErrIs == errCodeReviewFallbackCapacityExhausted {
					require.ErrorIs(t, err, errCodeReviewFallbackCapacityExhausted, "fallback slot exhaustion should stop retries without bubbling a worker infrastructure error")
				} else {
					require.ErrorContains(t, err, tt.expectErrIs.Error(), "unexpected storage failures should still propagate")
				}
				require.Nil(t, actual, "failed fallback allocation should not return a thread")
				if tt.expectCreate {
					require.NotNil(t, stub.input, "creation races should still capture the attempted thread input")
				} else {
					require.Nil(t, stub.input, "failed fallback allocation should not create a new thread input")
				}
				if tt.expectArchiveID == nil {
					require.Equal(t, uuid.Nil, stub.archiveID, "protected threads should not be archived")
				}
				return
			}
			require.NoError(t, err, "fallback thread should be available")
			if tt.expectReuse {
				require.Equal(t, &stub.existing[0], actual, "fallback should reuse the deterministic thread that already exists for this provider and attempt")
				require.Nil(t, stub.input, "retrying the controller must reuse its existing fallback thread")
			} else {
				require.Equal(t, &thread, actual, "fallback should preserve the newly created thread and its execution state")
				if tt.expectCreate {
					require.NotNil(t, stub.input, "fallback creation should use the selected model on a new thread")
				}
				require.Equal(t, &threadsvc.CreateThreadInput{
					SessionID: job.SessionID, OrgID: job.OrgID, AgentType: string(selection.AgentType), Model: *selection.AgentModel,
					ReasoningEffort: selection.ReasoningEffort, Label: thread.Label, FileScope: []string{"handler.go"},
					ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly, CreatedBySource: models.ThreadCreatedBySourceSystem,
				}, stub.input, "fallback should preserve model settings and use a fresh read-only synthesis thread")
			}
			if tt.expectArchiveID != nil {
				require.Equal(t, tt.expectArchiveID(stub.existing), stub.archiveID, "fallback should reclaim exactly one eligible persisted terminal thread when the visible-thread cap is full")
			} else {
				require.Equal(t, uuid.Nil, stub.archiveID, "fallback creation should not archive any thread unless the visible-thread cap requires it")
			}
		})
	}
}

func TestEnsureCodeReviewFallbackOrchestratorThreadSequentialAttemptsStayWithinVisibleLimit(t *testing.T) {
	t.Parallel()

	job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
	primaryThreadID := uuid.New()
	firstSelection := codeReviewOrchestratorSelection{AgentType: models.AgentTypeClaudeCode, AgentModel: stringPtr(models.DefaultClaudeCodeModel), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortMax), Available: true}
	secondSelection := codeReviewOrchestratorSelection{AgentType: models.AgentTypeCodex, AgentModel: stringPtr(models.CodexModelGPT55), ReasoningEffort: reasoningEffortPtr(models.ReasoningEffortMedium), Available: true}
	stub := &codeReviewFallbackThreadStub{
		existing: []models.SessionThread{
			{ID: primaryThreadID, OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusIdle},
			{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeCodex, Label: "Reviewer 1", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
			{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeClaudeCode, Label: "Reviewer 2", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
			{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, AgentType: models.AgentTypeOpenCode, Label: "Reviewer 3", CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
		},
	}
	firstResults := []models.CodeReviewAgentResult{
		codeReviewTerminalReviewerResultForThread(stub.existing[1].ID, models.AgentTypeCodex, models.DefaultCodexModel),
		codeReviewTerminalReviewerResultForThread(stub.existing[2].ID, models.AgentTypeClaudeCode, models.DefaultClaudeCodeModel),
		codeReviewTerminalReviewerResultForThread(stub.existing[3].ID, models.AgentTypeOpenCode, models.OpenCodeModelGPT55),
	}

	firstFallback, err := ensureCodeReviewFallbackOrchestratorThread(context.Background(), stub, job, firstSelection, 1, []string{"handler.go"}, primaryThreadID, firstResults)
	require.NoError(t, err, "first fallback should reclaim one reviewer slot and create a fallback thread")
	require.Equal(t, models.MaxThreadsPerSession, codeReviewVisibleThreadCount(stub.existing), "first fallback should keep the visible thread count within the session cap")

	firstFallback.Status = models.ThreadStatusFailed
	for i := range stub.existing {
		if stub.existing[i].ID == firstFallback.ID {
			stub.existing[i].Status = models.ThreadStatusFailed
			break
		}
	}
	secondResults := append(append([]models.CodeReviewAgentResult{}, firstResults...), codeReviewOrchestratorResultForThread(firstFallback.ID, firstSelection.AgentType, models.DefaultClaudeCodeModel, models.CodeReviewAgentResultStatusFailed))

	secondFallback, err := ensureCodeReviewFallbackOrchestratorThread(context.Background(), stub, job, secondSelection, 2, []string{"handler.go"}, primaryThreadID, secondResults)
	require.NoError(t, err, "second fallback should archive the failed prior fallback before creating the next one")
	require.Equal(t, firstFallback.ID, stub.archiveID, "second fallback should reclaim the failed prior fallback before touching persisted reviewer evidence")
	require.NotEqual(t, firstFallback.ID, secondFallback.ID, "second fallback should use a new deterministic thread after archiving the failed prior fallback")
	require.Equal(t, models.MaxThreadsPerSession, codeReviewVisibleThreadCount(stub.existing), "sequential fallback attempts should never exceed the visible thread cap")
	quorum, failures := codeReviewReviewerEvidence(secondResults)
	require.Equal(t, 3, quorum, "sequential fallback attempts should preserve the original completed reviewer quorum")
	require.Equal(t, 0, failures, "sequential fallback attempts should not convert persisted reviewer evidence into failures")
}

func TestEnsureCodeReviewOrchestratorFallbackStops(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status models.CodeReviewAgentResultStatus
		error  string
		age    time.Duration
	}{
		{name: "running attempt", status: models.CodeReviewAgentResultStatusRunning},
		{name: "completed attempt", status: models.CodeReviewAgentResultStatusCompleted},
		{name: "invalid synthesis", status: models.CodeReviewAgentResultStatusFailed, error: "invalid orchestrator synthesis"},
		{name: "timed out attempt", status: models.CodeReviewAgentResultStatusTimedOut},
		{name: "expired review budget", status: models.CodeReviewAgentResultStatusFailed, error: codeReviewCapacityFailureForTest, age: time.Hour},
		{name: "all candidates exhausted", status: models.CodeReviewAgentResultStatusFailed, error: codeReviewCapacityFailureForTest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := models.DefaultCodeReviewPolicyConfig()
			var results []models.CodeReviewAgentResult
			for _, candidate := range codeReviewOrchestratorCandidates(cfg) {
				result := codeReviewFailedModelForTest(candidate.AgentType, *candidate.AgentModel, models.CodeReviewAgentRoleOrchestrator)
				result.Status = tt.status
				result.StructuredResult = marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{Error: tt.error})
				result.RawOutput = stringPtr(tt.error)
				results = append(results, result)
			}
			err := ensureCodeReviewOrchestratorThread(context.Background(), nil, nil, zerolog.Nop(), runCodeReviewPayload{}, models.PullRequest{}, nil,
				codeReviewPolicyRecordForTest(cfg), models.CodeReviewSessionMetadata{CreatedAt: time.Now().Add(-tt.age)}, nil, results, nil, models.CodeReviewVisualEvidenceSnapshot{})
			require.NoError(t, err, "terminal, running, expired, or exhausted attempts must not dispatch more synthesis work")
		})
	}
}

type codeReviewFallbackArg func(any) bool

func (match codeReviewFallbackArg) Match(value any) bool { return match(value) }

func TestCodeReviewOrchestratorFallbackRecoversAndCompletes(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.CodeReviews = db.NewCodeReviewStore(mock)
	stores.SessionThreads = db.NewSessionThreadStore(mock)
	stores.SessionMessages = db.NewSessionMessageStore(mock)
	orgID, sessionID, threadID, resultID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, HeadSHA: "reviewed-head"}
	// Reviewer time must not consume the separate synthesis dispatch budget.
	metadata := models.CodeReviewSessionMetadata{CreatedAt: now.Add(-time.Hour), PromptRecordKey: stringPtr("captured-prompts")}
	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.Orchestrator = models.AgentTypeCodex
	cfg.AgentRoster.OrchestratorModel = stringPtr(models.DefaultCodexModel)
	policy := codeReviewPolicyRecordForTest(cfg)
	failed := codeReviewFailedModelForTest(models.AgentTypeCodex, models.DefaultCodexModel, models.CodeReviewAgentRoleOrchestrator)
	failed.ID = uuid.New()
	reviewer := models.CodeReviewAgentResult{
		ID: uuid.New(), AgentProvider: "claude_code", AgentModel: stringPtr(models.DefaultClaudeCodeModel),
		Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted,
		RawOutput: stringPtr("Completed reviewer evidence must survive fallback."),
		StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ReviewerKey: codeReviewReviewerKey(1, models.AgentTypeClaudeCode), CompletedAt: now.Format(time.RFC3339),
		}),
	}
	results := []models.CodeReviewAgentResult{failed, reviewer}
	recordKey := "captured-prompts/orchestrator-fallback-01-claude_code"
	var capturedPrompt string
	mock.ExpectQuery("INSERT INTO code_review_prompt_records").
		WithArgs(orgID, sessionID, recordKey, "orchestrator", "claude_code", codeReviewFallbackArg(func(value any) bool {
			capturedPrompt, _ = value.(string)
			return capturedPrompt != ""
		}), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "record_key", "role", "agent_provider", "content", "metadata", "created_at"}).
			AddRow(uuid.New(), orgID, sessionID, recordKey, "orchestrator", "claude_code", "captured", json.RawMessage(`{}`), now))
	sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusRunning, 0, nil, nil)
	setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mainThreadID := uuid.New()
	mainThreadRow := workerSessionThreadRow(mainThreadID, sessionID, orgID, models.AgentTypeCodex, stringPtr(models.DefaultCodexModel), models.ThreadStatusIdle)
	setWorkerSessionThreadColumn(mainThreadRow, "label", "Main")
	setWorkerSessionThreadColumn(mainThreadRow, "created_by_source", models.ThreadCreatedBySourceSystem)
	threadRow := workerSessionThreadRow(threadID, sessionID, orgID, models.AgentTypeClaudeCode, stringPtr(models.DefaultClaudeCodeModel), models.ThreadStatusRunning)
	setWorkerSessionThreadColumn(threadRow, "label", "Code review synthesis: claude_code (fallback 1)")
	setWorkerSessionThreadColumn(threadRow, "created_by_source", models.ThreadCreatedBySourceSystem)
	setWorkerSessionThreadColumn(threadRow, "execution_mode", models.ThreadExecutionModeReview)
	setWorkerSessionThreadColumn(threadRow, "filesystem_mode", models.ThreadFilesystemModeReadOnly)
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newSessionThreadRows().AddRow(mainThreadRow...).AddRow(threadRow...))
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newSessionThreadRows().AddRow(threadRow...))
	var runningState json.RawMessage
	mock.ExpectQuery("INSERT INTO code_review_agent_results").
		WithArgs(orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel), models.CodeReviewAgentRoleOrchestrator,
			models.CodeReviewAgentResultStatusRunning, (*string)(nil), codeReviewFallbackArg(func(value any) bool {
				runningState, _ = value.(json.RawMessage)
				state, ok := parseCodeReviewOrchestratorStructuredResult(runningState)
				return ok && state.ThreadID == threadID.String() && state.PromptRecordKey == recordKey && state.ReadOnly
			})).
		WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel),
			models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusRunning, nil, json.RawMessage(`{}`), now))

	err := ensureCodeReviewOrchestratorThread(context.Background(), stores, nil, zerolog.Nop(), job, models.PullRequest{}, nil, policy, metadata, nil, results, nil, models.CodeReviewVisualEvidenceSnapshot{})
	require.NoError(t, err, "a dispatched fallback should recover its result without sending another turn")
	require.Contains(t, capturedPrompt, *reviewer.RawOutput, "fallback synthesis should reuse the completed review")
	require.Contains(t, capturedPrompt, job.HeadSHA, "fallback synthesis should stay scoped to the captured PR head")
	require.NotContains(t, capturedPrompt, "/review ", "fallback should synthesize existing reviews rather than dispatch another native review")
	require.NoError(t, mock.ExpectationsWereMet(), "recovery should create only its missing result, without changing Main or redispatching reviewers")

	running := models.CodeReviewAgentResult{ID: resultID, OrgID: orgID, SessionID: sessionID, AgentProvider: "claude_code", AgentModel: stringPtr(models.DefaultClaudeCodeModel),
		Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusRunning, StructuredResult: runningState, CreatedAt: now}
	results = append(results, running)
	require.False(t, codeReviewOrchestratorTerminal(results), "the failed primary must not finish the review while fallback is running")

	raw := `{"approval_recommended":true,"description_assessments":[{"key":"description","status":"satisfied","evidence_basis":"pull_request_description","evidence_ids":[],"reason":"Clear intent."}],"findings":[],"human_review_reasons":[],"scope_mismatch":false,"unresolved_uncertainty":false,"reviewer_disagreement":false,"prompt_injection_detected":false,"summary":"The change is focused.","review_summary":"The completed review supports the change.","risk_notes":[]}`
	raw = "```json\n" + raw + "\n```"
	resultRows := newCodeReviewAgentResultRows()
	for _, result := range results {
		resultRows.AddRow(result.ID, orgID, sessionID, result.AgentProvider, result.AgentModel, result.Role, result.Status, result.RawOutput, result.StructuredResult, now)
	}
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).WillReturnRows(resultRows)
	setWorkerSessionThreadColumn(threadRow, "status", models.ThreadStatusCompleted)
	setWorkerSessionThreadColumn(threadRow, "completed_at", &now)
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).WillReturnRows(newSessionThreadRows().AddRow(threadRow...))
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, raw, nil, nil, nil, nil, "", now))
	var completedState json.RawMessage
	mock.ExpectQuery("UPDATE code_review_agent_results").WithArgs(models.CodeReviewAgentResultStatusCompleted, &raw, codeReviewFallbackArg(func(value any) bool {
		completedState, _ = value.(json.RawMessage)
		state, ok := parseCodeReviewOrchestratorStructuredResult(completedState)
		return ok && state.SynthesisValidated && state.Error == ""
	}), orgID, resultID).
		WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel),
			models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusCompleted, &raw, runningState, now))
	err = harvestCodeReviewOrchestratorResult(context.Background(), stores, nil, zerolog.Nop(), job, policy, nil, models.CodeReviewVisualEvidenceSnapshot{})
	require.NoError(t, err, "the successful fallback should be harvested despite the failed primary")
	require.NoError(t, mock.ExpectationsWereMet(), "harvest should update only the fallback result")
	completed := running
	completed.Status = models.CodeReviewAgentResultStatusCompleted
	completed.StructuredResult = completedState
	finalResults := []models.CodeReviewAgentResult{failed, reviewer, completed}
	present, usable := codeReviewOrchestratorEvidence(finalResults)
	require.Equal(t, []bool{true, true, true, false}, []bool{present, usable, codeReviewOrchestratorTerminal(finalResults), codeReviewOrchestratorNeedsFallback(finalResults)}, "validated fallback should complete synthesis without starting another attempt")
	quorum, failures := codeReviewReviewerEvidence(finalResults)
	require.Equal(t, []int{1, 0}, []int{quorum, failures}, "fallback attempts must not count toward reviewer quorum or discard completed reviewers")
	require.Equal(t, "The change is focused.", strings.TrimSpace(codeReviewOrchestratorSynthesisFromResults(finalResults).Summary), "final decision should use the successful fallback's synthesis")
}
