package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
)

const codeReviewReviewerCapacityUnavailableMessage = "reviewer skipped because no fallback thread slot was safely available"

func codeReviewReviewerSlotsFilled(cfg models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult) int {
	byKey := codeReviewReviewerResultsByKey(results)
	filled := 0
	for idx, agentType := range cfg.AgentRoster.Reviewers {
		result, ok := byKey[codeReviewReviewerKey(idx, agentType)]
		if ok && (!codeReviewReviewerResultTerminal(result.Status) || codeReviewReviewerResultHasUsableOutput(result)) {
			filled++
		}
	}
	return filled
}

func resolveCodeReviewReviewerAvailability(ctx context.Context, services *Services, orgID uuid.UUID, cfg models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult, timedOut bool) ([]codeReviewReviewerSelection, error) {
	if timedOut {
		return nil, nil
	}
	existing := codeReviewReviewerResultsByKey(results)
	filled := codeReviewReviewerSlotsFilled(cfg, results)
	selections := make([]codeReviewReviewerSelection, 0)
	for idx, agentType := range cfg.AgentRoster.Reviewers {
		if _, ok := existing[codeReviewReviewerKey(idx, agentType)]; ok {
			continue
		}
		if filled >= cfg.AgentRoster.EffectiveReviewerCount() {
			break
		}
		available := true
		if services != nil && services.CodingAgents != nil {
			var err error
			available, err = services.CodingAgents.IsAgentAvailable(ctx, orgID, nil, agentType, stringPtrValue(codeReviewReviewerAgentModel(cfg, idx, agentType)))
			if err != nil {
				return nil, fmt.Errorf("resolve code review reviewer %s availability: %w", agentType, err)
			}
		}
		selections = append(selections, codeReviewReviewerSelection{Index: idx, AgentType: agentType, Available: available})
		if available {
			filled++
		}
	}
	return selections, nil
}

func ensureCodeReviewReviewerThread(ctx context.Context, stores *Stores, threads codeReviewFallbackThreadService, input threadsvc.CreateThreadInput, fallback bool, results []models.CodeReviewAgentResult) (*models.SessionThread, error) {
	if !fallback {
		thread, err := threads.CreateThread(ctx, input)
		if !errors.Is(err, db.ErrThreadLimitReached) {
			return thread, err
		}
	}
	session, err := stores.Sessions.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load code review session for reviewer fallback: %w", err)
	}
	primaryThreadID, err := primaryThreadIDForSession(ctx, stores, session)
	if err != nil {
		return nil, fmt.Errorf("resolve primary thread before reviewer fallback: %w", err)
	}
	return ensureCodeReviewFallbackThread(ctx, threads, input, primaryThreadID, results)
}

func failedCodeReviewReviewerResult(job runCodeReviewPayload, index int, agentType models.AgentType, agentModel *string, status models.CodeReviewAgentResultStatus, message string, unavailable bool) *models.CodeReviewAgentResult {
	return &models.CodeReviewAgentResult{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		AgentProvider: string(agentType),
		AgentModel:    agentModel,
		Role:          models.CodeReviewAgentRoleReviewer,
		Status:        status,
		RawOutput:     &message,
		StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ReviewerKey:   codeReviewReviewerKey(index, agentType),
			ReviewerIndex: index,
			Unavailable:   unavailable,
			Error:         message,
			CompletedAt:   time.Now().UTC().Format(time.RFC3339),
		}),
	}
}
