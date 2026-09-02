package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
)

var errCodeReviewFallbackCapacityExhausted = errors.New("code review fallback capacity exhausted")

const codeReviewOrchestratorFallbackCapacityUnavailableMessage = "orchestrator skipped because no fallback thread slot was safely available"

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
	ArchiveThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error)
}

func ensureCodeReviewFallbackOrchestratorThread(ctx context.Context, threads codeReviewFallbackThreadService, job runCodeReviewPayload, selection codeReviewOrchestratorSelection, attempt int, fileScope []string, primaryThreadID uuid.UUID, results []models.CodeReviewAgentResult) (*models.SessionThread, error) {
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
	if codeReviewVisibleThreadCount(existing) >= models.MaxThreadsPerSession {
		candidate := codeReviewFallbackArchivalCandidate(existing, results, primaryThreadID)
		if candidate == nil {
			return nil, errCodeReviewFallbackCapacityExhausted
		}
		if _, err := threads.ArchiveThread(ctx, job.OrgID, job.SessionID, candidate.ID); err != nil {
			if codeReviewFallbackCapacityRace(err) {
				return nil, errCodeReviewFallbackCapacityExhausted
			}
			return nil, fmt.Errorf("archive code review fallback slot: %w", err)
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
		if errors.Is(err, db.ErrThreadLimitReached) {
			return nil, errCodeReviewFallbackCapacityExhausted
		}
		return nil, fmt.Errorf("create code review fallback orchestrator thread: %w", err)
	}
	return thread, nil
}

func codeReviewVisibleThreadCount(threads []models.SessionThread) int {
	count := 0
	for _, thread := range threads {
		if thread.ArchivedAt == nil {
			count++
		}
	}
	return count
}

func codeReviewFallbackArchivalCandidate(threads []models.SessionThread, results []models.CodeReviewAgentResult, primaryThreadID uuid.UUID) *models.SessionThread {
	resultStates := codeReviewAgentThreadResultStates(results)
	var best *models.SessionThread
	bestPriority := codeReviewFallbackThreadPriorityUnavailable
	for i := range threads {
		thread := &threads[i]
		priority, ok := codeReviewFallbackArchivalPriority(*thread, resultStates, primaryThreadID)
		if !ok || priority >= bestPriority {
			continue
		}
		best = thread
		bestPriority = priority
	}
	return best
}

type codeReviewAgentThreadResultState struct {
	terminal         bool
	nonTerminal      bool
	orchestrator     bool
	orchestratorFail bool
}

func codeReviewAgentThreadResultStates(results []models.CodeReviewAgentResult) map[uuid.UUID]codeReviewAgentThreadResultState {
	states := make(map[uuid.UUID]codeReviewAgentThreadResultState, len(results))
	for _, result := range results {
		threadID, ok := codeReviewAgentResultThreadID(result)
		if !ok {
			continue
		}
		state := states[threadID]
		if codeReviewReviewerResultTerminal(result.Status) {
			state.terminal = true
		} else {
			state.nonTerminal = true
		}
		if result.Role == models.CodeReviewAgentRoleOrchestrator {
			state.orchestrator = true
			if result.Status == models.CodeReviewAgentResultStatusFailed {
				state.orchestratorFail = true
			}
		}
		states[threadID] = state
	}
	return states
}

func codeReviewAgentResultThreadID(result models.CodeReviewAgentResult) (uuid.UUID, bool) {
	var rawThreadID string
	switch result.Role {
	case models.CodeReviewAgentRoleReviewer:
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		if !ok {
			return uuid.Nil, false
		}
		rawThreadID = strings.TrimSpace(state.ThreadID)
	case models.CodeReviewAgentRoleOrchestrator:
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok {
			return uuid.Nil, false
		}
		rawThreadID = strings.TrimSpace(state.ThreadID)
	default:
		return uuid.Nil, false
	}
	if rawThreadID == "" {
		return uuid.Nil, false
	}
	threadID, err := uuid.Parse(rawThreadID)
	if err != nil {
		return uuid.Nil, false
	}
	return threadID, true
}

const codeReviewFallbackThreadPriorityUnavailable = 100

func codeReviewFallbackArchivalPriority(thread models.SessionThread, states map[uuid.UUID]codeReviewAgentThreadResultState, primaryThreadID uuid.UUID) (int, bool) {
	if thread.ArchivedAt != nil ||
		thread.CreatedBySource != models.ThreadCreatedBySourceSystem ||
		thread.ID == primaryThreadID ||
		thread.PendingMessageCount > 0 {
		return codeReviewFallbackThreadPriorityUnavailable, false
	}
	switch thread.Status {
	case models.ThreadStatusCompleted, models.ThreadStatusFailed, models.ThreadStatusCancelled:
	default:
		return codeReviewFallbackThreadPriorityUnavailable, false
	}
	state, ok := states[thread.ID]
	if !ok || !state.terminal || state.nonTerminal {
		return codeReviewFallbackThreadPriorityUnavailable, false
	}
	if state.orchestratorFail {
		return 0, true
	}
	if state.orchestrator {
		return 1, true
	}
	return 2, true
}

func codeReviewFallbackCapacityRace(err error) bool {
	return errors.Is(err, threadsvc.ErrThreadActive) ||
		errors.Is(err, threadsvc.ErrThreadNotFound) ||
		errors.Is(err, threadsvc.ErrCannotArchiveLastThread)
}
