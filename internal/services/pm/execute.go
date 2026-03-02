package pm

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func (s *Service) executePlan(ctx context.Context, orgID uuid.UUID, plan *Plan) error {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return err
	}
	settings := models.ParseOrgSettings(org.Settings)

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
			_ = s.issues.UpdateStatus(ctx, orgID, issueID, "triaged")
		}

		payload := map[string]string{
			"agent_run_id": run.ID.String(),
			"org_id":       orgID.String(),
		}
		_, _ = s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, nil)

		task.AgentRunID = &run.ID
		task.Status = models.PMTaskStatusDelegated
		delegated++
	}

	model, err := planToModel(plan, nil)
	if err != nil {
		return err
	}
	return s.plans.Update(ctx, model)
}
