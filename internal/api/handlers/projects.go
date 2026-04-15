package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type ProjectHandler struct {
	projectStore      *db.ProjectStore
	projectTaskStore  *db.ProjectTaskStore
	projectCycleStore *db.ProjectCycleStore
	attachmentStore   *db.ProjectAttachmentStore
	specStore         *db.ProjectSpecStore
	jobStore          *db.JobStore
	audit             *db.AuditEmitter
}

// SetAuditEmitter injects the audit emitter for logging project events.
func (h *ProjectHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func NewProjectHandler(
	projectStore *db.ProjectStore,
	projectTaskStore *db.ProjectTaskStore,
	projectCycleStore *db.ProjectCycleStore,
	attachmentStore *db.ProjectAttachmentStore,
	specStore *db.ProjectSpecStore,
) *ProjectHandler {
	return &ProjectHandler{
		projectStore:      projectStore,
		projectTaskStore:  projectTaskStore,
		projectCycleStore: projectCycleStore,
		attachmentStore:   attachmentStore,
		specStore:         specStore,
	}
}

// SetJobStore injects the job store for enqueuing project_cycle jobs.
func (h *ProjectHandler) SetJobStore(jobStore *db.JobStore) {
	h.jobStore = jobStore
}

// ProjectDetailResponse combines a project with its tasks, cycles, attachments, and specs.
type ProjectDetailResponse struct {
	Project      models.Project             `json:"project"`
	Tasks        []models.ProjectTask       `json:"tasks"`
	RecentCycles []models.ProjectCycle      `json:"recent_cycles"`
	Attachments  []models.ProjectAttachment `json:"attachments"`
	Specs        []models.ProjectSpec       `json:"specs"`
}

// validStatusTransition checks whether a project status transition is allowed.
func validStatusTransition(from, to models.ProjectStatus) bool {
	switch from {
	case models.ProjectStatusDraft:
		return to == models.ProjectStatusActive || to == models.ProjectStatusCompleted
	case models.ProjectStatusActive:
		return to == models.ProjectStatusCompleted
	default:
		return false
	}
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.ProjectFilters{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	if search := r.URL.Query().Get("search"); search != "" {
		filters.Search = search
	}

	if v := r.URL.Query().Get("proposed_by_pm"); v == "true" || v == "false" {
		b := v == "true"
		filters.ProposedByPM = &b
	}

	if repoIDStr := r.URL.Query().Get("repository_id"); repoIDStr != "" {
		repoID, err := uuid.Parse(repoIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = repoID
	}

	projects, err := h.projectStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list projects", err)
		return
	}
	if projects == nil {
		projects = []models.Project{}
	}

	var nextCursor string
	if len(projects) > 0 && len(projects) == filters.Limit {
		nextCursor = projects[len(projects)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Project]{
		Data: projects,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	tasks, err := h.projectTaskStore.ListByProject(r.Context(), orgID, projectID, db.ProjectTaskFilters{})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_TASKS_FAILED", "failed to list project tasks", err)
		return
	}
	if tasks == nil {
		tasks = []models.ProjectTask{}
	}

	cycles, err := h.projectCycleStore.ListByProject(r.Context(), orgID, projectID, 10)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_CYCLES_FAILED", "failed to list project cycles", err)
		return
	}
	if cycles == nil {
		cycles = []models.ProjectCycle{}
	}

	var attachments []models.ProjectAttachment
	if h.attachmentStore != nil {
		attachments, err = h.attachmentStore.ListByProject(r.Context(), orgID, projectID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_ATTACHMENTS_FAILED", "failed to list project attachments", err)
			return
		}
	}
	if attachments == nil {
		attachments = []models.ProjectAttachment{}
	}

	var specs []models.ProjectSpec
	if h.specStore != nil {
		specs, err = h.specStore.ListByProject(r.Context(), orgID, projectID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_SPECS_FAILED", "failed to list project specs", err)
			return
		}
	}
	if specs == nil {
		specs = []models.ProjectSpec{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[ProjectDetailResponse]{
		Data: ProjectDetailResponse{
			Project:      project,
			Tasks:        tasks,
			RecentCycles: cycles,
			Attachments:  attachments,
			Specs:        specs,
		},
	})
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		Title              string  `json:"title"`
		Goal               string  `json:"goal"`
		RepositoryID       string  `json:"repository_id"`
		Scope              *string `json:"scope"`
		CompletionCriteria *string `json:"completion_criteria"`
		ExecutionMode      *string `json:"execution_mode"`
		MaxConcurrent      *int    `json:"max_concurrent"`
		Priority           *int    `json:"priority"`
		BaseBranch         *string `json:"base_branch"`
		AgentType          *string `json:"agent_type"`
		Model              *string `json:"model"`
		ScheduleEnabled    *bool   `json:"schedule_enabled"`
		ScheduleInterval   *int    `json:"schedule_interval"`
		ScheduleUnit       *string `json:"schedule_unit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title == "" || req.Goal == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "title and goal are required")
		return
	}

	repoID, err := uuid.Parse(req.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
		return
	}

	execMode := models.ProjectExecModeSequential
	if req.ExecutionMode != nil {
		execMode = models.ProjectExecMode(*req.ExecutionMode)
		if err := execMode.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", err.Error())
			return
		}
	}

	maxConcurrent := 1
	if req.MaxConcurrent != nil && *req.MaxConcurrent > 0 {
		maxConcurrent = *req.MaxConcurrent
	}

	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}

	baseBranch := "main"
	if req.BaseBranch != nil && *req.BaseBranch != "" {
		baseBranch = *req.BaseBranch
	}

	if req.AgentType != nil && *req.AgentType != "" {
		if err := models.AgentType(*req.AgentType).Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
			return
		}
	}

	if req.Model != nil && *req.Model != "" {
		agentType := models.AgentTypeClaudeCode
		if req.AgentType != nil && *req.AgentType != "" {
			agentType = models.AgentType(*req.AgentType)
		}
		if err := models.ValidateModelForAgentType(agentType, *req.Model); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			return
		}
	}

	scheduleEnabled := false
	scheduleInterval := 1
	scheduleUnit := "days"
	if req.ScheduleEnabled != nil {
		scheduleEnabled = *req.ScheduleEnabled
	}
	if req.ScheduleInterval != nil && *req.ScheduleInterval > 0 {
		if *req.ScheduleInterval > 365 {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_INTERVAL", "schedule_interval must be between 1 and 365")
			return
		}
		scheduleInterval = *req.ScheduleInterval
	}
	if req.ScheduleUnit != nil && *req.ScheduleUnit != "" {
		scheduleUnit = *req.ScheduleUnit
		if err := models.ScheduleUnit(scheduleUnit).Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_UNIT", err.Error())
			return
		}
	}

	// Compute next_run_at when schedule is enabled at creation time.
	var nextRunAt *time.Time
	if scheduleEnabled {
		now := time.Now()
		next := models.NextRunTime(now, scheduleInterval, scheduleUnit)
		nextRunAt = &next
	}

	project := models.Project{
		OrgID:              orgID,
		RepositoryID:       &repoID,
		Title:              req.Title,
		Goal:               req.Goal,
		Scope:              req.Scope,
		CompletionCriteria: req.CompletionCriteria,
		Status:             models.ProjectStatusDraft,
		Priority:           priority,
		ExecutionMode:      execMode,
		MaxConcurrent:      maxConcurrent,
		AutoMerge:          false,
		BaseBranch:         baseBranch,
		AgentType:          req.AgentType,
		ModelOverride:      req.Model,
		ScheduleEnabled:    scheduleEnabled,
		ScheduleInterval:   scheduleInterval,
		ScheduleUnit:       scheduleUnit,
		NextRunAt:          nextRunAt,
		CreatedBy:          &user.ID,
	}

	if err := h.projectStore.Create(r.Context(), &project); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create project", err)
		return
	}

	projIDStr := project.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectCreated, models.AuditResourceProject, &projIDStr, nil, &project.ID, nil)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Project]{Data: project})
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req struct {
		Title              *string `json:"title"`
		Goal               *string `json:"goal"`
		Scope              *string `json:"scope"`
		CompletionCriteria *string `json:"completion_criteria"`
		Status             *string `json:"status"`
		Priority           *int    `json:"priority"`
		ExecutionMode      *string `json:"execution_mode"`
		MaxConcurrent      *int    `json:"max_concurrent"`
		AutoMerge          *bool   `json:"auto_merge"`
		BaseBranch         *string `json:"base_branch"`
		CurrentPhase       *string `json:"current_phase"`
		ScheduleEnabled    *bool   `json:"schedule_enabled"`
		ScheduleInterval   *int    `json:"schedule_interval"`
		ScheduleUnit       *string `json:"schedule_unit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title != nil {
		project.Title = *req.Title
	}
	if req.Goal != nil {
		project.Goal = *req.Goal
	}
	if req.Scope != nil {
		project.Scope = req.Scope
	}
	if req.CompletionCriteria != nil {
		project.CompletionCriteria = req.CompletionCriteria
	}
	if req.Status != nil {
		newStatus := models.ProjectStatus(*req.Status)
		if err := newStatus.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", err.Error())
			return
		}
		if !validStatusTransition(project.Status, newStatus) {
			writeError(w, r, http.StatusBadRequest, "INVALID_TRANSITION", "invalid status transition")
			return
		}
		project.Status = newStatus
	}
	if req.Priority != nil {
		project.Priority = *req.Priority
	}
	if req.ExecutionMode != nil {
		execMode := models.ProjectExecMode(*req.ExecutionMode)
		if err := execMode.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", err.Error())
			return
		}
		project.ExecutionMode = execMode
	}
	if req.MaxConcurrent != nil {
		project.MaxConcurrent = *req.MaxConcurrent
	}
	if req.AutoMerge != nil {
		project.AutoMerge = *req.AutoMerge
	}
	if req.BaseBranch != nil {
		project.BaseBranch = *req.BaseBranch
	}
	if req.CurrentPhase != nil {
		project.CurrentPhase = req.CurrentPhase
	}
	if req.ScheduleInterval != nil && *req.ScheduleInterval > 0 {
		if *req.ScheduleInterval > 365 {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_INTERVAL", "schedule_interval must be between 1 and 365")
			return
		}
		project.ScheduleInterval = *req.ScheduleInterval
	}
	if req.ScheduleUnit != nil && *req.ScheduleUnit != "" {
		unit := models.ScheduleUnit(*req.ScheduleUnit)
		if err := unit.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_UNIT", err.Error())
			return
		}
		project.ScheduleUnit = *req.ScheduleUnit
	}
	if req.ScheduleEnabled != nil {
		wasEnabled := project.ScheduleEnabled
		project.ScheduleEnabled = *req.ScheduleEnabled
		// When enabling schedule, compute next_run_at if not already set.
		if *req.ScheduleEnabled && !wasEnabled {
			now := time.Now()
			next := models.NextRunTime(now, project.ScheduleInterval, project.ScheduleUnit)
			project.NextRunAt = &next
		}
		// When disabling schedule, clear next_run_at.
		if !*req.ScheduleEnabled && wasEnabled {
			project.NextRunAt = nil
		}
	}

	if err := h.projectStore.Update(r.Context(), &project); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update project", err)
		return
	}

	updatedProjIDStr := projectID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectUpdated, models.AuditResourceProject, &updatedProjIDStr, nil, &projectID, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Project]{Data: project})
}

func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	// With only three statuses (draft, active, completed), "completed" is the sole
	// terminal state. Deleting a project marks it completed (which also sets
	// completed_at).
	if err := h.projectStore.UpdateStatus(r.Context(), orgID, projectID, string(models.ProjectStatusCompleted)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to mark project as done", err)
		return
	}

	deletedProjIDStr := projectID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectDeleted, models.AuditResourceProject, &deletedProjIDStr, nil, &projectID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) Start(w http.ResponseWriter, r *http.Request) {
	h.transitionStatus(w, r, models.ProjectStatusActive)
}

// RunNow enqueues an immediate project_cycle job for the project.
func (h *ProjectHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	if h.jobStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_CONFIGURED", "job store not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	if project.Status != models.ProjectStatusActive {
		writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "project must be active to run")
		return
	}

	dedupeKey := fmt.Sprintf("project_cycle:%s", projectID.String())
	payload := map[string]string{
		"org_id":     orgID.String(),
		"project_id": projectID.String(),
	}
	jobID, err := h.jobStore.Enqueue(r.Context(), orgID, "default", models.JobTypeProjectCycle, payload, 5, &dedupeKey)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue project cycle job", err)
		return
	}

	runNowProjIDStr := projectID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectRunTriggered, models.AuditResourceProject, &runNowProjIDStr, nil, &projectID, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{
		Data: map[string]string{"job_id": jobID.String()},
	})
}

func (h *ProjectHandler) transitionStatus(w http.ResponseWriter, r *http.Request, target models.ProjectStatus) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	if !validStatusTransition(project.Status, target) {
		writeError(w, r, http.StatusBadRequest, "INVALID_TRANSITION", "invalid status transition")
		return
	}

	if err := h.projectStore.UpdateStatus(r.Context(), orgID, projectID, string(target)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update project status", err)
		return
	}

	// Map status transitions to audit actions.
	var auditAction models.AuditAction
	switch target {
	case models.ProjectStatusActive:
		auditAction = models.AuditActionProjectStarted
	case models.ProjectStatusCompleted:
		auditAction = models.AuditActionProjectCompleted
	}
	if auditAction != "" {
		transitionProjIDStr := projectID.String()
		emitUserAuditWithSession(h.audit, r, auditAction, models.AuditResourceProject, &transitionProjIDStr, nil, &projectID, nil)
	}

	project.Status = target
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Project]{Data: project})
}

// Task sub-endpoints

func (h *ProjectHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	// Verify project exists
	if _, err := h.projectStore.GetByID(r.Context(), orgID, projectID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
		Approach    *string `json:"approach"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "title is required")
		return
	}

	maxBatch, err := h.projectTaskStore.GetMaxBatchNumber(r.Context(), orgID, projectID)
	if err != nil {
		maxBatch = 0
	}

	task := models.ProjectTask{
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       req.Title,
		Description: req.Description,
		Approach:    req.Approach,
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: maxBatch + 1,
	}

	if err := h.projectTaskStore.Create(r.Context(), &task); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create task", err)
		return
	}

	createTaskIDStr := task.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectTaskCreated, models.AuditResourceProjectTask, &createTaskIDStr, nil, &projectID, nil)

	// Update project progress counts
	if err := h.projectStore.UpdateProgress(r.Context(), orgID, projectID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update project progress")
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.ProjectTask]{Data: task})
}

func (h *ProjectHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	task, err := h.projectTaskStore.GetByID(r.Context(), orgID, taskID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}
	if task.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	var req struct {
		Title        *string `json:"title"`
		Description  *string `json:"description"`
		Approach     *string `json:"approach"`
		Status       *string `json:"status"`
		OutcomeNotes *string `json:"outcome_notes"`
		Complexity   *string `json:"complexity"`
		Confidence   *string `json:"confidence"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	// Seed-task metadata (title, description, approach, complexity, confidence)
	// can only be edited while the parent project is proposed or draft.
	// Only fetch the parent project when seed fields are actually being modified.
	seedFieldsModified := req.Title != nil || req.Description != nil || req.Approach != nil || req.Complexity != nil || req.Confidence != nil
	if seedFieldsModified {
		project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
		if err != nil {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
			return
		}
		if project.Status != models.ProjectStatusDraft {
			writeError(w, r, http.StatusConflict, "PROJECT_NOT_EDITABLE",
				"task metadata can only be edited while the project is in draft status")
			return
		}
	}

	if req.Title != nil {
		task.Title = *req.Title
	}
	if req.Description != nil {
		task.Description = req.Description
	}
	if req.Approach != nil {
		task.Approach = req.Approach
	}
	if req.Complexity != nil {
		task.Complexity = req.Complexity
	}
	if req.Confidence != nil {
		task.Confidence = req.Confidence
	}
	if req.Status != nil {
		newStatus := models.ProjectTaskStatus(*req.Status)
		if err := newStatus.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", err.Error())
			return
		}
		task.Status = newStatus
	}
	if req.OutcomeNotes != nil {
		task.OutcomeNotes = req.OutcomeNotes
	}

	if err := h.projectTaskStore.Update(r.Context(), &task); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update task", err)
		return
	}

	updateTaskIDStr := taskID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectTaskUpdated, models.AuditResourceProjectTask, &updateTaskIDStr, nil, &projectID, nil)

	if err := h.projectStore.UpdateProgress(r.Context(), orgID, task.ProjectID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update project progress")
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectTask]{Data: task})
}

func (h *ProjectHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	task, err := h.projectTaskStore.GetByID(r.Context(), orgID, taskID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}
	if task.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	if err := h.projectTaskStore.Delete(r.Context(), orgID, taskID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete task", err)
		return
	}

	deleteTaskIDStr := taskID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectTaskDeleted, models.AuditResourceProjectTask, &deleteTaskIDStr, nil, &projectID, nil)

	if err := h.projectStore.UpdateProgress(r.Context(), orgID, task.ProjectID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update project progress")
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) RetryTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	task, err := h.projectTaskStore.GetByID(r.Context(), orgID, taskID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}
	if task.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	if task.Status != models.ProjectTaskStatusFailed {
		writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "only failed tasks can be retried")
		return
	}

	task.Status = models.ProjectTaskStatusPending
	task.RetryCount++

	if err := h.projectTaskStore.Update(r.Context(), &task); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to retry task", err)
		return
	}

	retryTaskIDStr := taskID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionProjectTaskRetried, models.AuditResourceProjectTask, &retryTaskIDStr, nil, &projectID, nil)

	if err := h.projectStore.UpdateProgress(r.Context(), orgID, task.ProjectID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update project progress")
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectTask]{Data: task})
}

// Cycle endpoints

func (h *ProjectHandler) ListCycles(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	limit := queryInt(r, "limit", 20)

	cycles, err := h.projectCycleStore.ListByProject(r.Context(), orgID, projectID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list cycles", err)
		return
	}
	if cycles == nil {
		cycles = []models.ProjectCycle{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ProjectCycle]{
		Data: cycles,
		Meta: models.PaginationMeta{},
	})
}

func (h *ProjectHandler) GetCycle(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	cycleID, err := uuid.Parse(chi.URLParam(r, "cycleId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid cycle ID")
		return
	}

	cycle, err := h.projectCycleStore.GetByID(r.Context(), orgID, cycleID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "cycle not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectCycle]{Data: cycle})
}

// ProposalSummary returns a count of PM-proposed draft projects for the org.
// GET /api/v1/projects/proposals/summary
func (h *ProjectHandler) ProposalSummary(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	pmTrue := true
	count, err := h.projectStore.Count(r.Context(), orgID, db.ProjectFilters{Status: string(models.ProjectStatusDraft), ProposedByPM: &pmTrue})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COUNT_FAILED", "failed to count proposals", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]int{
			"count": count,
		},
	})
}
