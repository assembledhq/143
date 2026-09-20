package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// automationModelCandidates returns the ordered model chain a run dispatches
// against. The run's config snapshot wins over the live automation row: an
// automation edited mid-chain must not re-rank a run already in flight, which
// is the same snapshot-first rule automationRunIdentityScope applies to
// identity. A snapshot written before fallback models existed reports ok=false,
// and only then do we read the live row.
func automationModelCandidates(run models.AutomationRun, automation models.Automation) ([]models.AutomationModelRank, error) {
	ranks, ok, err := models.AutomationModelRanksFromConfigSnapshot(run.ConfigSnapshot)
	if err != nil {
		return nil, fmt.Errorf("resolve automation model ranks: %w", err)
	}
	if !ok {
		ranks = automation.ModelRanks()
	}

	// Collapse consecutive duplicates the way codeReviewOrchestratorCandidates
	// compares roster entries: a fallback that repeats the rank before it would
	// otherwise burn an attempt re-running the model that just failed.
	// Non-adjacent repeats are kept, because a chain that deliberately returns
	// to an earlier model after trying another one is a valid retry strategy.
	candidates := make([]models.AutomationModelRank, 0, len(ranks))
	for _, rank := range ranks {
		if len(candidates) > 0 && automationModelRanksEqual(candidates[len(candidates)-1], rank) {
			continue
		}
		candidates = append(candidates, rank)
	}
	return candidates, nil
}

// automationModelRanksEqual treats reasoning effort as part of a rank's
// identity. Re-trying the same model at a cheaper level is a legitimate chain,
// so collapsing on (agent, model) alone would silently discard it.
func automationModelRanksEqual(a, b models.AutomationModelRank) bool {
	return automationModelRankAgentType(a) == automationModelRankAgentType(b) &&
		automationModelRankModel(a) == automationModelRankModel(b) &&
		automationModelRankEffort(a) == automationModelRankEffort(b)
}

func automationModelRankEffort(rank models.AutomationModelRank) string {
	if rank.ReasoningEffort == nil {
		return ""
	}
	return strings.TrimSpace(string(*rank.ReasoningEffort))
}

func automationModelRankAgentType(rank models.AutomationModelRank) string {
	return strings.TrimSpace(stringPtrValue(rank.AgentType))
}

func automationModelRankModel(rank models.AutomationModelRank) string {
	return strings.TrimSpace(stringPtrValue(rank.Model))
}

// automationModelCandidatesRemaining drops every candidate the run has already
// spent a session on. This — not a session count — is what advances the chain:
// pre-flight availability can skip a rank without spawning a session, so the
// Nth session is not necessarily rank N, and resuming by count would re-dispatch
// the model that just failed while never reaching the tail of the chain.
//
// Matching on (agent, model) rather than on position also means a rank that
// merely repeats an earlier one is never spent twice.
func automationModelCandidatesRemaining(candidates []models.AutomationModelRank, attempts []models.AutomationRunAttempt) []models.AutomationModelRank {
	if len(attempts) == 0 {
		return candidates
	}
	remaining := make([]models.AutomationModelRank, 0, len(candidates))
	for _, candidate := range candidates {
		spent := false
		for _, attempt := range attempts {
			if attempt.Matches(candidate) {
				spent = true
				break
			}
		}
		if !spent {
			remaining = append(remaining, candidate)
		}
	}
	return remaining
}

// automationModelCandidateLabels names the ranks considered on this dispatch,
// for the result summary a user reads when the chain is exhausted.
func automationModelCandidateLabels(candidates []models.AutomationModelRank) string {
	labels := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		labels = append(labels, automationModelCandidateLabel(candidate))
	}
	return strings.Join(labels, ", ")
}

func automationModelCandidateLabel(rank models.AutomationModelRank) string {
	if model := automationModelRankModel(rank); model != "" {
		return model
	}
	if agentType := automationModelRankAgentType(rank); agentType != "" {
		return agentType
	}
	return "default model"
}

// resolveAutomationModelSelection returns the first candidate whose agent has a
// usable credential for that specific model, with its index in the list it was
// given. Availability is checked lazily so a chain configured but never reached
// costs no credential lookups.
//
// Nil services mean "assume available", matching
// resolveCodeReviewReviewerAvailability: tests and trimmed-down workers still
// dispatch rather than failing every run for want of an availability service.
func resolveAutomationModelSelection(ctx context.Context, services *Services, orgID uuid.UUID, userID *uuid.UUID, candidates []models.AutomationModelRank) (models.AutomationModelRank, int, bool, error) {
	if len(candidates) == 0 {
		return models.AutomationModelRank{}, 0, false, nil
	}
	if services == nil || services.CodingAgents == nil {
		return candidates[0], 0, true, nil
	}

	for idx := 0; idx < len(candidates); idx++ {
		candidate := candidates[idx]
		agentType := automationModelRankAgentType(candidate)
		if agentType == "" {
			// The agent is resolved downstream by the handler's ladder (org
			// default, then the built-in default), so there is nothing to look
			// up here yet. Treat the rank as usable rather than skipping it —
			// leaving the agent unset is the common automation configuration,
			// and skipping would strand those runs with no candidate at all.
			return candidate, idx, true, nil
		}
		available, err := services.CodingAgents.IsAgentAvailable(ctx, orgID, userID, models.AgentType(agentType), automationModelRankModel(candidate))
		if err != nil {
			return models.AutomationModelRank{}, 0, false, fmt.Errorf("resolve automation model rank %d availability: %w", idx, err)
		}
		if available {
			return candidate, idx, true, nil
		}
	}
	return models.AutomationModelRank{}, 0, false, nil
}
