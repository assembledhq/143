package pm

import (
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func (s *Service) executePlan(ctx context.Context, orgID uuid.UUID, plan *Plan, settings models.OrgSettings, productContext *models.ProductContext) error {
	maxConcurrent := settings.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = models.DefaultMaxConcurrentRuns
	}

	running, err := s.sessions.CountRunningByOrg(ctx, orgID)
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

		if settings.AutonomyLevel == models.AutonomyLevelManual {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		if task.Confidence == models.PMTaskConfidenceLow && settings.AutonomyLevel != models.AutonomyLevelAutoAll {
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		primaryIssueID := task.IssueIDs[0]
		agentType := settings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}

		// Look up issue to get its repository ID for the session.
		var repoID *uuid.UUID
		if primaryIssue, issueErr := s.issues.GetByID(ctx, orgID, primaryIssueID); issueErr == nil {
			repoID = primaryIssue.RepositoryID
		}

		// SessionAutonomy is a per-run knob distinct from the org-level
		// AutonomyLevel automation policy; see models.SessionAutonomy doc.
		run := &models.Session{
			PrimaryIssueID: &primaryIssueID,
			OrgID:          orgID,
			AgentType:      agentType,
			Status:         models.SessionStatusPending,
			AutonomyLevel:  models.DefaultSessionAutonomy,
			TokenMode:      models.SessionTokenMode(tokenModeFromComplexity(task.Complexity)),
			PMPlanID:       &plan.ID,
			Title:          &task.Title,
			PMApproach:     &task.Approach,
			PMReasoning:    &task.Reasoning,
			RepositoryID:   repoID,
		}
		if err := s.sessions.Create(ctx, run); err != nil {
			s.logger.Error().Err(err).Msg("failed to create agent run from PM plan")
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}
		if err := s.createInitialDelegatedSessionMessage(ctx, orgID, run); err != nil {
			s.logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to create initial PM-delegated session message")
		}

		for _, issueID := range task.IssueIDs {
			if err := s.issues.UpdateStatus(ctx, orgID, issueID, models.IssueStatusTriaged); err != nil {
				s.logger.Warn().Err(err).Str("issue_id", issueID.String()).Msg("failed to mark issue as triaged")
			}
		}

		dedupeKey := db.RunAgentDedupeKey(run.ID)
		payload := db.RunAgentPayload(run)
		if _, err := s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
			s.logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to enqueue agent run job")
			task.Status = models.PMTaskStatusSkippedCapacity
			continue
		}

		task.SessionID = &run.ID
		task.Status = models.PMTaskStatusDelegated
		delegated++
	}

	model, err := planToModel(plan, productContext)
	if err != nil {
		return err
	}
	return s.plans.Update(ctx, model)
}

func (s *Service) createInitialDelegatedSessionMessage(ctx context.Context, orgID uuid.UUID, run *models.Session) error {
	if s.sessionMessages == nil || run == nil || run.PMApproach == nil {
		return nil
	}
	content := strings.TrimSpace(*run.PMApproach)
	if content == "" {
		return nil
	}
	msg := &models.SessionMessage{
		SessionID:  run.ID,
		OrgID:      orgID,
		ThreadID:   run.PrimaryThreadID,
		TurnNumber: 0,
		Role:       models.MessageRoleUser,
		Content:    content,
	}
	if err := s.sessionMessages.Create(ctx, msg); err != nil {
		return fmt.Errorf("create initial PM-delegated session message: %w", err)
	}
	return nil
}
