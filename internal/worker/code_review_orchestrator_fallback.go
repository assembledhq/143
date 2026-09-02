package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
)

// Use the captured roster, including each model's own reasoning effort. A
// capacity failure must not silently introduce an agent outside that policy.
func codeReviewOrchestratorCandidates(cfg models.CodeReviewPolicyConfig) []codeReviewOrchestratorSelection {
	candidates := []codeReviewOrchestratorSelection{{
		AgentType:       cfg.AgentRoster.Orchestrator,
		AgentModel:      codeReviewOrchestratorAgentModel(cfg),
		ReasoningEffort: reasoningEffortPtr(cfg.AgentRoster.ReasoningEffort),
		Available:       true,
	}}
	for idx, agentType := range cfg.AgentRoster.Reviewers {
		candidate := codeReviewOrchestratorSelection{
			AgentType:       agentType,
			AgentModel:      codeReviewReviewerAgentModel(cfg, idx, agentType),
			ReasoningEffort: reasoningEffortPtr(cfg.AgentRoster.ReviewerReasoningEffort(idx)),
			Available:       true,
		}
		duplicate := false
		for _, existing := range candidates {
			if existing.AgentType == candidate.AgentType && codeReviewAgentModelsEqual(existing.AgentModel, candidate.AgentModel) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func codeReviewModelUnavailable(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"model is at capacity", "model is overloaded", "overloaded_error", "server is overloaded", "service unavailable",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// Reuse the runtime's rate-limit vocabulary without changing credential
	// health: a model-capacity error does not mean its credential is unhealthy.
	return agent.CredentialFailureSignalFromResult(&agent.AgentResult{Error: message}, time.Now()).RateLimited
}

func codeReviewAgentModelUnavailable(result models.CodeReviewAgentResult) bool {
	if result.Status != models.CodeReviewAgentResultStatusFailed {
		return false
	}
	var state struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.StructuredResult, &state); err == nil && strings.TrimSpace(state.Error) != "" {
		return codeReviewModelUnavailable(state.Error)
	}
	return codeReviewModelUnavailable(stringPtrValue(result.RawOutput))
}

func codeReviewOrchestratorCandidateExhausted(candidate codeReviewOrchestratorSelection, results []models.CodeReviewAgentResult) bool {
	for _, result := range results {
		if result.AgentProvider != string(candidate.AgentType) {
			continue
		}
		model := result.AgentModel
		if model == nil || strings.TrimSpace(*model) == "" {
			model = codeReviewDefaultAgentModel(candidate.AgentType)
		}
		if !codeReviewAgentModelsEqual(model, candidate.AgentModel) {
			continue
		}
		if result.Role == models.CodeReviewAgentRoleOrchestrator || codeReviewAgentModelUnavailable(result) {
			return true
		}
	}
	return false
}

func codeReviewOrchestratorAttemptCount(results []models.CodeReviewAgentResult) int {
	count := 0
	for _, result := range results {
		if result.Role == models.CodeReviewAgentRoleOrchestrator {
			count++
		}
	}
	return count
}

func codeReviewOrchestratorNeedsFallback(results []models.CodeReviewAgentResult) bool {
	found := false
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator {
			continue
		}
		found = true
		if !codeReviewAgentModelUnavailable(result) {
			return false
		}
	}
	return found
}

type codeReviewFallbackThreadService interface {
	ListThreads(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionThread, error)
	CreateThread(context.Context, threadsvc.CreateThreadInput) (*models.SessionThread, error)
}

func ensureCodeReviewFallbackOrchestratorThread(ctx context.Context, threads codeReviewFallbackThreadService, job runCodeReviewPayload, selection codeReviewOrchestratorSelection, attempt int, fileScope []string) (*models.SessionThread, error) {
	// A used thread's provider is immutable. Keep each failed attempt intact,
	// and recover the next thread if dispatch succeeded before result storage.
	label := fmt.Sprintf("Code review synthesis: %s (fallback %d)", selection.AgentType, attempt)
	existing, err := threads.ListThreads(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return nil, fmt.Errorf("list code review fallback threads: %w", err)
	}
	for _, thread := range existing {
		if thread.Label == label && thread.CreatedBySource == models.ThreadCreatedBySourceSystem &&
			thread.AgentType == selection.AgentType && codeReviewAgentModelsEqual(thread.ModelOverride, selection.AgentModel) {
			return &thread, nil
		}
	}
	thread, err := threads.CreateThread(ctx, threadsvc.CreateThreadInput{
		SessionID:       job.SessionID,
		OrgID:           job.OrgID,
		AgentType:       string(selection.AgentType),
		Model:           stringPtrValue(selection.AgentModel),
		ReasoningEffort: selection.ReasoningEffort,
		Label:           label,
		FileScope:       fileScope,
		ExecutionMode:   models.ThreadExecutionModeReview,
		FilesystemMode:  models.ThreadFilesystemModeReadOnly,
		CreatedBySource: models.ThreadCreatedBySourceSystem,
	})
	if err != nil {
		return nil, fmt.Errorf("create code review fallback orchestrator thread: %w", err)
	}
	return thread, nil
}
