package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewReviewerFallbackRecoversAndCompletes(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.CodeReviews = db.NewCodeReviewStore(mock)
	stores.SessionThreads = db.NewSessionThreadStore(mock)
	stores.SessionMessages = db.NewSessionMessageStore(mock)
	orgID, sessionID, mainThreadID, threadID, resultID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, HeadSHA: "captured-head"}
	metadata := models.CodeReviewSessionMetadata{CreatedAt: now.Add(-time.Minute), PromptRecordKey: stringPtr("review-prompts")}
	cfg := codeReviewRankedConfigForTest()
	policy := codeReviewPolicyRecordForTest(cfg)
	results, _ := codeReviewRankedResultsForTest(cfg, []codeReviewRankedAttemptForTest{
		{index: 0, status: models.CodeReviewAgentResultStatusFailed},
		{index: 1, status: models.CodeReviewAgentResultStatusCompleted},
	})
	results[0].RawOutput = stringPtr(codeReviewCapacityFailureForTest)
	results[1].RawOutput = stringPtr("The other independent review completed.")
	originalResults := append([]models.CodeReviewAgentResult(nil), results...)
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(codeReviewRankedResultRowsForTest(orgID, sessionID, results))
	recordKey := "review-prompts/reviewer-03-claude_code"
	var capturedPrompt string
	mock.ExpectQuery("INSERT INTO code_review_prompt_records").
		WithArgs(orgID, sessionID, recordKey, "reviewer", "claude_code", codeReviewFallbackArg(func(value any) bool {
			capturedPrompt, _ = value.(string)
			return capturedPrompt != ""
		}), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "record_key", "role", "agent_provider", "content", "metadata", "created_at"}).
			AddRow(uuid.New(), orgID, sessionID, recordKey, "reviewer", "claude_code", "captured", json.RawMessage(`{}`), now))
	sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusRunning, 0, nil, nil)
	setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)
	// The worker loads the primary identity, and the thread service verifies tenant ownership.
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mainThreadRow := workerSessionThreadRow(mainThreadID, sessionID, orgID, models.AgentTypeCodex, stringPtr(models.DefaultCodexModel), models.ThreadStatusIdle)
	setWorkerSessionThreadColumn(mainThreadRow, "label", "Main")
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newSessionThreadRows().AddRow(mainThreadRow...))
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	threadRow := workerSessionThreadRow(threadID, sessionID, orgID, models.AgentTypeClaudeCode, stringPtr(models.DefaultClaudeCodeModel), models.ThreadStatusRunning)
	setWorkerSessionThreadColumn(threadRow, "label", "Code review: claude_code (fallback 3)")
	setWorkerSessionThreadColumn(threadRow, "created_by_source", models.ThreadCreatedBySourceSystem)
	setWorkerSessionThreadColumn(threadRow, "execution_mode", models.ThreadExecutionModeReview)
	setWorkerSessionThreadColumn(threadRow, "filesystem_mode", models.ThreadFilesystemModeReadOnly)
	setWorkerSessionThreadColumn(threadRow, "current_turn", 1)
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newSessionThreadRows().AddRow(threadRow...))
	var recoveredState json.RawMessage
	mock.ExpectQuery("INSERT INTO code_review_agent_results").
		WithArgs(orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel), models.CodeReviewAgentRoleReviewer,
			models.CodeReviewAgentResultStatusQueued, (*string)(nil), codeReviewFallbackArg(func(value any) bool {
				recoveredState, _ = value.(json.RawMessage)
				state, ok := parseCodeReviewReviewerStructuredResult(recoveredState)
				return ok && state.ReviewerKey == "02:claude_code" && state.ThreadID == threadID.String() && state.PromptRecordKey == recordKey && state.ReadOnly
			})).
		WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel),
			models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusQueued, nil, json.RawMessage(`{}`), now))

	err := ensureCodeReviewReviewerThreads(context.Background(), stores, nil, zerolog.Nop(), job, models.PullRequest{}, policy, metadata, nil, models.CodeReviewVisualEvidenceSnapshot{})
	require.NoError(t, err, "a fallback already dispatched before result persistence should recover without sending another turn")
	require.Contains(t, capturedPrompt, job.HeadSHA, "fallback review must use the same captured PR head")
	require.NoError(t, mock.ExpectationsWereMet(), "recovery should create only the missing attempt and preserve the completed peer")

	fallback := models.CodeReviewAgentResult{ID: resultID, OrgID: orgID, SessionID: sessionID, AgentProvider: "claude_code", AgentModel: stringPtr(models.DefaultClaudeCodeModel),
		Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusQueued, StructuredResult: recoveredState, CreatedAt: now}
	results = append(results, fallback)
	require.False(t, codeReviewReviewerRosterTerminal(cfg, results, false), "the review should wait for its replacement report")
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(codeReviewRankedResultRowsForTest(orgID, sessionID, results))
	setWorkerSessionThreadColumn(threadRow, "status", models.ThreadStatusCompleted)
	setWorkerSessionThreadColumn(threadRow, "completed_at", &now)
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
		WillReturnRows(newSessionThreadRows().AddRow(threadRow...))
	raw := "The change is focused. No blocking issues found."
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, raw, nil, nil, nil, nil, "", now))
	var completedState json.RawMessage
	mock.ExpectQuery("UPDATE code_review_agent_results").WithArgs(models.CodeReviewAgentResultStatusCompleted, &raw, codeReviewFallbackArg(func(value any) bool {
		completedState, _ = value.(json.RawMessage)
		state, ok := parseCodeReviewReviewerStructuredResult(completedState)
		return ok && state.ThreadID == threadID.String() && state.Error == "" && state.CompletedAt == now.Format(time.RFC3339)
	}), orgID, resultID).
		WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "claude_code", stringPtr(models.DefaultClaudeCodeModel),
			models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusCompleted, &raw, recoveredState, now))
	err = harvestCodeReviewReviewerResults(context.Background(), stores, nil, zerolog.Nop(), job, policy, metadata, nil)
	require.NoError(t, err, "completed fallback should be harvested as usable reviewer evidence")
	require.NoError(t, mock.ExpectationsWereMet(), "harvest should update only the recovered replacement")

	fallback.Status = models.CodeReviewAgentResultStatusCompleted
	fallback.StructuredResult = completedState
	fallback.RawOutput = &raw
	finalResults := append(append([]models.CodeReviewAgentResult(nil), originalResults...), fallback)
	completed, failures := codeReviewReviewerEvidence(finalResults)
	require.Equal(t, []int{2, 1, 2}, []int{completed, failures, codeReviewRequiredReviewerQuorum(cfg, finalResults)}, "successful fallback should restore quorum while retaining the failed attempt")
	require.True(t, codeReviewReviewerRosterTerminal(cfg, finalResults, false), "unused fourth choice should not delay synthesis")
	require.Equal(t, originalResults, finalResults[:2], "fallback must preserve the original reports and failure history")
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(codeReviewRankedResultRowsForTest(orgID, sessionID, finalResults))
	err = ensureCodeReviewReviewerThreads(context.Background(), stores, nil, zerolog.Nop(), job, models.PullRequest{}, policy, metadata, nil, models.CodeReviewVisualEvidenceSnapshot{})
	require.NoError(t, err, "another worker poll should leave the unused fallback untouched")
	require.NoError(t, mock.ExpectationsWereMet(), "a satisfied reviewer count should cause no new prompt, result, or thread writes")
}

func codeReviewRankedResultRowsForTest(orgID, sessionID uuid.UUID, results []models.CodeReviewAgentResult) *pgxmock.Rows {
	rows := newCodeReviewAgentResultRows()
	for _, result := range results {
		rows.AddRow(result.ID, orgID, sessionID, result.AgentProvider, result.AgentModel, result.Role, result.Status, result.RawOutput, result.StructuredResult, result.CreatedAt)
	}
	return rows
}
