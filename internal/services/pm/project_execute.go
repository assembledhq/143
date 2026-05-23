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
			if err := s.projects.UpdateStatus(ctx, orgID, pp.ProjectID, models.ProjectStatusCompleted); err != nil {
				s.logger.Warn().Err(err).Msg("failed to update project status to completed")
			}
			// Record cycle even on completion.
			s.recordProjectCycle(ctx, orgID, pp, planID, 0, 0)
			return nil
		case "needs_human_review":
			// Human review requested — leave the project active. The PM will
			// re-evaluate on the next scheduled cycle and can recommend
			// "needs_human_review" again if the issue is still unresolved.
			// We intentionally do NOT revert to draft because that would lose
			// the context that this project was actively running.
			s.logger.Info().Str("project_id", pp.ProjectID.String()).Msg("project cycle requested human review; project remains active")
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
	titleToID := make(map[string]uuid.UUID) // maps task title → UUID for dependency resolution
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
		titleToID[spec.Title] = task.ID
		tasksCreated++
	}

	// Resolve title-based depends_on references to UUIDs for dependency_graph mode.
	if project.ExecutionMode == models.ProjectExecModeDependencyGraph && tasksCreated > 0 {
		s.resolveDependsOnTitles(ctx, orgID, pp, titleToID)
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
	if settings.AutonomyLevel == models.AutonomyLevelManual {
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
		agentType = models.AgentType(*project.AgentType)
	}
	if agentType == "" {
		agentType = models.DefaultDefaultAgentType
	}

	// For dependency_graph mode, build a status lookup of all project tasks so
	// we can check whether each task's dependencies have been satisfied.
	var taskStatusByID map[uuid.UUID]models.ProjectTaskStatus
	if project.ExecutionMode == models.ProjectExecModeDependencyGraph {
		allTasks, err := s.projectTasks.ListByProject(ctx, orgID, project.ID, db.ProjectTaskFilters{})
		if err != nil {
			s.logger.Error().Err(err).Msg("failed to list all project tasks for dependency check")
			return 0
		}
		taskStatusByID = make(map[uuid.UUID]models.ProjectTaskStatus, len(allTasks))
		for _, t := range allTasks {
			taskStatusByID[t.ID] = t.Status
		}
	}

	dispatched := 0
	for i := range pending {
		if dispatched >= slotsAvailable {
			break
		}

		task := &pending[i]

		// Skip low-confidence tasks unless autonomy is auto_all.
		if task.Confidence != nil && *task.Confidence == "low" && settings.AutonomyLevel != models.AutonomyLevelAutoAll {
			continue
		}

		// In dependency_graph mode, only dispatch tasks whose dependencies are all completed.
		if project.ExecutionMode == models.ProjectExecModeDependencyGraph && len(task.DependsOn) > 0 {
			depStatus := checkDependenciesStatus(task.DependsOn, taskStatusByID)
			if depStatus == depStatusBlocked {
				// At least one dependency failed — mark this task as blocked.
				task.Status = models.ProjectTaskStatusBlocked
				if err := s.projectTasks.Update(ctx, task); err != nil {
					s.logger.Warn().Err(err).Str("task_id", task.ID.String()).Msg("failed to mark task as blocked")
				}
				continue
			}
			if depStatus == depStatusWaiting {
				// Dependencies still in progress — skip for now.
				continue
			}
			// depStatusReady: all dependencies completed, proceed with dispatch.
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

		// See models.SessionAutonomy doc for why this is not the
		// org-level AutonomyLevel automation policy.
		run := &models.Session{
			OrgID:          orgID,
			PrimaryIssueID: task.IssueID,
			AgentType:      agentType,
			Status:         models.SessionStatusPending,
			AutonomyLevel:  models.DefaultSessionAutonomy,
			TokenMode:      models.SessionTokenMode(tokenModeFromTaskComplexity(task.Complexity)),
			PMPlanID:       &planID,
			Title:          &task.Title,
			PMApproach:     &approach,
			PMReasoning:    &reasoning,
			ProjectTaskID:  &task.ID,
			ModelOverride:  project.ModelOverride,
			RepositoryID:   project.RepositoryID,
			TargetBranch:   &branchName,
		}
		if err := s.sessions.Create(ctx, run); err != nil {
			s.logger.Error().Err(err).Str("task_id", task.ID.String()).Msg("failed to create agent run for project task")
			continue
		}

		// Update task with agent run reference and branch name.
		task.Status = models.ProjectTaskStatusDelegated
		task.SessionID = &run.ID
		task.BranchName = &branchName
		if err := s.projectTasks.Update(ctx, task); err != nil {
			s.logger.Error().Err(err).Str("task_id", task.ID.String()).Msg("failed to update project task status")
			continue
		}

		// Enqueue the agent run job.
		dedupeKey := db.RunAgentDedupeKey(run.ID)
		payload := db.RunAgentPayload(run)
		if _, err := s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
			s.logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to enqueue project agent run")
			continue
		}

		dispatched++
	}

	return dispatched
}

// canDispatchForProject returns how many tasks can be dispatched for this project
// based on execution mode and currently running tasks.
func (s *Service) canDispatchForProject(ctx context.Context, orgID uuid.UUID, project *models.Project) int {
	runningCount, err := s.projectTasks.CountByProjectAndStatus(ctx, orgID, project.ID, models.ProjectTaskStatusRunning)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to count running project tasks")
		return 0
	}
	delegatedCount, err := s.projectTasks.CountByProjectAndStatus(ctx, orgID, project.ID, models.ProjectTaskStatusDelegated)
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
	case models.ProjectExecModeParallel, models.ProjectExecModeDependencyGraph:
		// Both modes allow parallel execution up to max_concurrent. For
		// dependency_graph mode, actual eligibility is further filtered in
		// dispatchProjectTasks based on whether each task's dependencies
		// are satisfied.
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

// depStatus represents the aggregate state of a task's dependencies.
type depStatus int

const (
	depStatusReady   depStatus = iota // all deps completed
	depStatusWaiting                  // some deps still pending/running
	depStatusBlocked                  // at least one dep failed/cancelled
)

// checkDependenciesStatus evaluates whether a task's dependencies allow dispatch.
func checkDependenciesStatus(dependsOn []uuid.UUID, statusByID map[uuid.UUID]models.ProjectTaskStatus) depStatus {
	for _, depID := range dependsOn {
		status, ok := statusByID[depID]
		if !ok {
			// Unknown dependency — treat as waiting (may not be created yet).
			return depStatusWaiting
		}
		switch status {
		case models.ProjectTaskStatusCompleted:
			continue
		case models.ProjectTaskStatusFailed, models.ProjectTaskStatusCancelled, models.ProjectTaskStatusSkipped, models.ProjectTaskStatusBlocked:
			return depStatusBlocked
		default:
			return depStatusWaiting
		}
	}
	return depStatusReady
}

// resolveDependsOnTitles converts title-based dependency references in new tasks
// to UUID-based references. The PM agent specifies dependencies by title since
// IDs don't exist at plan time. After creating the tasks, we resolve titles to
// the actual task UUIDs. Also looks up existing tasks in the project for
// cross-batch references.
func (s *Service) resolveDependsOnTitles(ctx context.Context, orgID uuid.UUID, pp *ProjectPlan, titleToID map[string]uuid.UUID) {
	// Build a broader title→ID map from all existing tasks in the project.
	existing, err := s.projectTasks.ListByProject(ctx, orgID, pp.ProjectID, db.ProjectTaskFilters{})
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list existing tasks for dependency resolution")
		return
	}
	allTitleToID := make(map[string]uuid.UUID, len(existing))
	for _, t := range existing {
		allTitleToID[t.Title] = t.ID
	}
	// Overlay the newly created tasks (which take precedence).
	for title, id := range titleToID {
		allTitleToID[title] = id
	}

	// Resolve each new task's depends_on titles to UUIDs.
	for _, spec := range pp.NewTasks {
		if len(spec.DependsOn) == 0 {
			continue
		}
		taskID, ok := titleToID[spec.Title]
		if !ok {
			continue
		}

		var resolved []uuid.UUID
		for _, depTitle := range spec.DependsOn {
			if depID, found := allTitleToID[depTitle]; found {
				resolved = append(resolved, depID)
			} else {
				s.logger.Warn().Str("task", spec.Title).Str("missing_dep", depTitle).Msg("dependency title not found — skipping")
			}
		}

		if len(resolved) > 0 {
			task, err := s.projectTasks.GetByID(ctx, orgID, taskID)
			if err != nil {
				s.logger.Warn().Err(err).Str("task_id", taskID.String()).Msg("failed to fetch task for dependency update")
				continue
			}
			task.DependsOn = resolved
			if err := s.projectTasks.Update(ctx, &task); err != nil {
				s.logger.Warn().Err(err).Str("task_id", taskID.String()).Msg("failed to update task dependencies")
			}
		}
	}
}

// tokenModeFromTaskComplexity maps a task's complexity string to a token mode.
func tokenModeFromTaskComplexity(complexity *string) string {
	if complexity == nil {
		return "low"
	}
	return tokenModeFromComplexity(models.PMTaskComplexity(*complexity))
}
