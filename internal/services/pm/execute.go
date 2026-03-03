package pm

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func (s *Service) executePlan(ctx context.Context, orgID uuid.UUID, plan *Plan, settings models.OrgSettings, productContext *models.ProductContext) error {
	maxConcurrent := settings.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = models.DefaultMaxConcurrentRuns
	}

	running, err := s.agentRuns.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return err
	}

	available := maxConcurrent - running
	if available < 0 {
		available = 0
	}

	delegated := 0
	for i := range plan.Tasks {
		task := &plan.Tasks[i]

		if len(task.IssueIDs) == 0 {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		if delegated >= available {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		if settings.AutonomyLevel == "manual" {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		if task.Confidence == models.PMTaskConfidenceLow && settings.AutonomyLevel != "auto_all" {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		primaryIssueID := task.IssueIDs[0]
		agentType := settings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}

		run := &models.AgentRun{
			IssueID:       primaryIssueID,
			OrgID:         orgID,
			AgentType:     agentType,
			Status:        "pending",
			AutonomyLevel: settings.AutonomyLevel,
			TokenMode:     tokenModeFromComplexity(task.Complexity),
			PMPlanID:      &plan.ID,
			PMApproach:    &task.Approach,
			PMReasoning:   &task.Reasoning,
		}
		if err := s.agentRuns.Create(ctx, run); err != nil {
			s.logger.Error().Err(err).Msg("failed to create agent run from PM plan")
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		for _, issueID := range task.IssueIDs {
			if err := s.issues.UpdateStatus(ctx, orgID, issueID, "triaged"); err != nil {
				s.logger.Warn().Err(err).Str("issue_id", issueID.String()).Msg("failed to mark issue as triaged")
			}
		}

		payload := map[string]string{
			"agent_run_id": run.ID.String(),
			"org_id":       orgID.String(),
		}
		if _, err := s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, nil); err != nil {
			s.logger.Error().Err(err).Str("agent_run_id", run.ID.String()).Msg("failed to enqueue agent run job")
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		task.AgentRunID = &run.ID
		task.Status = models.PMTaskStatusDelegated
		delegated++
	}

	model, err := planToModel(plan, productContext)
	if err != nil {
		return err
	}
	return s.plans.Update(ctx, model)
}
