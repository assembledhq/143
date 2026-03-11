package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

var multiDash = regexp.MustCompile(`-{2,}`)

// executeProjectPlan creates project tasks from the PM's plan output,
// dispatches them via agent runs (respecting execution mode), records
// a project cycle, and updates project progress.
func (s *Service) executeProjectPlan(ctx context.Context, orgID uuid.UUID, pp *ProjectPlan, settings models.OrgSettings, planID uuid.UUID) error {
	project, err := s.projects.GetByID(ctx, orgID, pp.ProjectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// Apply status recommendation if the PM suggests a transition.
	if pp.StatusRecommendation != "" {
		switch pp.StatusRecommendation {
		case "completed":
			if err := s.projects.UpdateStatus(ctx, orgID, pp.ProjectID, string(models.ProjectStatusCompleted)); err != nil {
				s.logger.Warn().Err(err).Msg("failed to update project status to completed")
			}
			// Record cycle even on completion.
			s.recordProjectCycle(ctx, orgID, pp, planID, 0, 0)
			return nil
		case "needs_human_review":
			if err := s.projects.UpdateStatus(ctx, orgID, pp.ProjectID, string(models.ProjectStatusPaused)); err != nil {
				s.logger.Warn().Err(err).Msg("failed to update project status to paused for human review")
			}
			s.recordProjectCycle(ctx, orgID, pp, planID, 0, 0)
			return nil
		}
	}

	// Update project lessons learned if PM provided new ones.
	if len(pp.LessonsLearned) > 0 {
		project.LessonsLearned = append(project.LessonsLearned, pp.LessonsLearned...)
		raw, err := json.Marshal(project.LessonsLearned)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to marshal lessons learned")
		}
		project.LessonsLearnedRaw = raw
		if err := s.projects.Update(ctx, &project); err != nil {
			s.logger.Warn().Err(err).Msg("failed to update project lessons learned")
		}
	}

	// Update current phase if PM specified one.
	if pp.CurrentPhase != "" {
		project.CurrentPhase = &pp.CurrentPhase
		if err := s.projects.Update(ctx, &project); err != nil {
			s.logger.Warn().Err(err).Msg("failed to update project current phase")
		}
	}

	// Get next batch number.
	maxBatch, err := s.projectTasks.GetMaxBatchNumber(ctx, orgID, pp.ProjectID)
	if err != nil {
		return fmt.Errorf("get max batch number: %w", err)
	}
	nextBatch := maxBatch + 1

	// Create new tasks from the PM's plan.
	tasksCreated := 0
	for i, spec := range pp.NewTasks {
		task := &models.ProjectTask{
			ProjectID:   pp.ProjectID,
			OrgID:       orgID,
			Title:       spec.Title,
			SortOrder:   i + 1,
			BatchNumber: nextBatch,
			Status:      models.ProjectTaskStatusPending,
			MaxRetries:  2,
		}
		if spec.Description != "" {
			task.Description = &spec.Description
		}
		if spec.Approach != "" {
			task.Approach = &spec.Approach
		}
		if spec.Reasoning != "" {
			task.Reasoning = &spec.Reasoning
		}
		if spec.Complexity != "" {
			task.Complexity = &spec.Complexity
		}
		if spec.Confidence != "" {
			task.Confidence = &spec.Confidence
		}

		if err := s.projectTasks.Create(ctx, task); err != nil {
			s.logger.Error().Err(err).Str("task_title", spec.Title).Msg("failed to create project task")
			continue
		}
		tasksCreated++
	}

	// Dispatch eligible pending tasks based on execution mode.
	dispatched := s.dispatchProjectTasks(ctx, orgID, &project, settings, planID)

	// Record cycle.
	s.recordProjectCycle(ctx, orgID, pp, planID, tasksCreated, dispatched)

	// Update project progress counts.
	if err := s.projects.UpdateProgress(ctx, orgID, pp.ProjectID); err != nil {
		s.logger.Warn().Err(err).Msg("failed to update project progress")
	}

	return nil
}

// dispatchProjectTasks dispatches eligible pending tasks for a project,
// respecting the execution mode constraints. Returns the count of dispatched tasks.
func (s *Service) dispatchProjectTasks(ctx context.Context, orgID uuid.UUID, project *models.Project, settings models.OrgSettings, planID uuid.UUID) int {
	if settings.AutonomyLevel == "manual" {
		return 0
	}

	slotsAvailable := s.canDispatchForProject(ctx, orgID, project)
	if slotsAvailable <= 0 {
		return 0
	}

	// Fetch pending tasks ordered by batch_number, sort_order.
	pending, err := s.projectTasks.ListByProject(ctx, orgID, project.ID, db.ProjectTaskFilters{Status: string(models.ProjectTaskStatusPending)})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to list pending project tasks for dispatch")
		return 0
	}

	agentType := settings.DefaultAgentType
	if project.AgentType != nil && *project.AgentType != "" {
		agentType = *project.AgentType
	}
	if agentType == "" {
		agentType = models.DefaultDefaultAgentType
	}

	dispatched := 0
	for i := range pending {
		if dispatched >= slotsAvailable {
			break
		}

		task := &pending[i]

		// Skip low-confidence tasks unless autonomy is auto_all.
		if task.Confidence != nil && *task.Confidence == "low" && settings.AutonomyLevel != "auto_all" {
			continue
		}

		branchName := generateProjectBranchName(project.ID, task.BatchNumber, task.SortOrder, task.Title)

		approach := ""
		if task.Approach != nil {
			approach = *task.Approach
		}
		reasoning := ""
		if task.Reasoning != nil {
			reasoning = *task.Reasoning
		}

		run := &models.AgentRun{
			OrgID:         orgID,
			IssueID:       placeholderIssueID(task),
			AgentType:     agentType,
			Status:        string(models.AgentRunStatusPending),
			AutonomyLevel: settings.AutonomyLevel,
			TokenMode:     tokenModeFromTaskComplexity(task.Complexity),
			PMPlanID:      &planID,
			PMApproach:    &approach,
			PMReasoning:   &reasoning,
			ProjectTaskID: &task.ID,
			ModelOverride: project.ModelOverride,
		}
		if err := s.agentRuns.Create(ctx, run); err != nil {
			s.logger.Error().Err(err).Str("task_id", task.ID.String()).Msg("failed to create agent run for project task")
			continue
		}

		// Update task with agent run reference and branch name.
		task.Status = models.ProjectTaskStatusDelegated
		task.AgentRunID = &run.ID
		task.BranchName = &branchName
		if err := s.projectTasks.Update(ctx, task); err != nil {
			s.logger.Error().Err(err).Str("task_id", task.ID.String()).Msg("failed to update project task status")
			continue
		}

		// Enqueue the agent run job.
		payload := map[string]string{
			"agent_run_id": run.ID.String(),
			"org_id":       orgID.String(),
		}
		if _, err := s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, nil); err != nil {
			s.logger.Error().Err(err).Str("agent_run_id", run.ID.String()).Msg("failed to enqueue project agent run")
			continue
		}

		dispatched++
	}

	return dispatched
}

// canDispatchForProject returns how many tasks can be dispatched for this project
// based on execution mode and currently running tasks.
func (s *Service) canDispatchForProject(ctx context.Context, orgID uuid.UUID, project *models.Project) int {
	runningCount, err := s.projectTasks.CountByProjectAndStatus(ctx, orgID, project.ID, string(models.ProjectTaskStatusRunning))
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to count running project tasks")
		return 0
	}
	delegatedCount, err := s.projectTasks.CountByProjectAndStatus(ctx, orgID, project.ID, string(models.ProjectTaskStatusDelegated))
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to count delegated project tasks")
		return 0
	}

	activeCount := runningCount + delegatedCount

	switch project.ExecutionMode {
	case models.ProjectExecModeSequential:
		if activeCount > 0 {
			return 0
		}
		return 1
	case models.ProjectExecModeParallel:
		maxConcurrent := project.MaxConcurrent
		if maxConcurrent <= 0 {
			maxConcurrent = 1
		}
		remaining := maxConcurrent - activeCount
		if remaining < 0 {
			return 0
		}
		return remaining
	default:
		// dependency_graph not implemented yet; treat as sequential.
		if activeCount > 0 {
			return 0
		}
		return 1
	}
}

// recordProjectCycle creates a ProjectCycle record for this PM planning cycle.
func (s *Service) recordProjectCycle(ctx context.Context, orgID uuid.UUID, pp *ProjectPlan, planID uuid.UUID, tasksCreated, tasksDispatched int) {
	// Get current cycle number.
	cycles, err := s.projectCycles.ListByProject(ctx, orgID, pp.ProjectID, 1)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to list project cycles")
		return
	}
	cycleNumber := 1
	if len(cycles) > 0 {
		cycleNumber = cycles[0].CycleNumber + 1
	}

	var progressPct *int
	if pp.ProgressPct > 0 {
		p := pp.ProgressPct
		progressPct = &p
	}

	decisions, err := json.Marshal(map[string]interface{}{
		"new_tasks":  pp.NewTasks,
		"skipped":    pp.SkippedTasks,
		"dispatched": tasksDispatched,
		"status_rec": pp.StatusRecommendation,
		"lessons":    pp.LessonsLearned,
	})
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to marshal cycle decisions")
	}

	cycle := &models.ProjectCycle{
		ProjectID:             pp.ProjectID,
		OrgID:                 orgID,
		PMPlanID:              &planID,
		CycleNumber:           cycleNumber,
		Analysis:              pp.CycleAnalysis,
		Decisions:             decisions,
		ProgressPct:           progressPct,
		TasksCreatedThisCycle: tasksCreated,
	}

	if err := s.projectCycles.Create(ctx, cycle); err != nil {
		s.logger.Error().Err(err).Msg("failed to create project cycle")
	}
}

// generateProjectBranchName creates a branch name for a project task.
// Format: 143/project-{shortID}/{batch}-{order}-{slug}
func generateProjectBranchName(projectID uuid.UUID, batchNumber, sortOrder int, title string) string {
	shortID := projectID.String()[:8]
	slug := slugifyTitle(title, 40)
	return fmt.Sprintf("143/project-%s/%d-%d-%s", shortID, batchNumber, sortOrder, slug)
}

// slugifyTitle converts a title to a URL-safe slug.
func slugifyTitle(title string, maxLen int) string {
	s := strings.ToLower(title)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	s = multiDash.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxLen {
		s = s[:maxLen]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

// placeholderIssueID returns the task's associated issue ID if it has one,
// otherwise returns uuid.Nil. The agent_runs store maps uuid.Nil to SQL NULL.
func placeholderIssueID(task *models.ProjectTask) uuid.UUID {
	if task.IssueID != nil {
		return *task.IssueID
	}
	return uuid.Nil
}

// tokenModeFromTaskComplexity maps a task's complexity string to a token mode.
func tokenModeFromTaskComplexity(complexity *string) string {
	if complexity == nil {
		return "low"
	}
	return tokenModeFromComplexity(models.PMTaskComplexity(*complexity))
}
