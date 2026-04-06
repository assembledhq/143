package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type EvalHandler struct {
	taskStore      *db.EvalTaskStore
	runStore       *db.EvalRunStore
	batchStore     *db.EvalBatchStore
	bootstrapStore *db.EvalBootstrapStore
	jobStore       *db.JobStore
	txStarter      db.TxStarter
	audit          *db.AuditEmitter
}

func NewEvalHandler(taskStore *db.EvalTaskStore, runStore *db.EvalRunStore, batchStore *db.EvalBatchStore, bootstrapStore *db.EvalBootstrapStore, jobStore *db.JobStore, txStarter db.TxStarter) *EvalHandler {
	return &EvalHandler{
		taskStore:      taskStore,
		runStore:       runStore,
		batchStore:     batchStore,
		bootstrapStore: bootstrapStore,
		jobStore:       jobStore,
		txStarter:      txStarter,
	}
}

func (h *EvalHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// validateScoringCriteria validates that scoring_criteria is a well-formed JSON array
// with valid grader_type values. Used by both CreateTask and UpdateTask.
func validateScoringCriteria(w http.ResponseWriter, r *http.Request, raw json.RawMessage) bool {
	var criteria []models.ScoringCriterion
	if err := json.Unmarshal(raw, &criteria); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CRITERIA", "scoring_criteria must be a valid JSON array of scoring criteria")
		return false
	}
	for _, c := range criteria {
		if err := c.GraderType.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_GRADER_TYPE", "invalid grader_type in scoring criteria: must be 'code_check' or 'llm_judge'")
			return false
		}
	}
	return true
}

// --- Eval Tasks ---

func (h *EvalHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	filters := models.EvalTaskListFilters{
		Limit: queryInt(r, "limit", 50),
	}

	if source := r.URL.Query().Get("source"); source != "" {
		s := models.EvalTaskSource(source)
		if err := s.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE", "invalid source filter value")
			return
		}
		filters.Source = &s
	}

	if complexity := r.URL.Query().Get("complexity"); complexity != "" {
		c := models.EvalComplexity(complexity)
		if err := c.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_COMPLEXITY", "invalid complexity filter value")
			return
		}
		filters.Complexity = &c
	}

	if r.URL.Query().Get("archived") == "true" {
		archived := true
		filters.Archived = &archived
	}

	if tagsParam := r.URL.Query().Get("tags"); tagsParam != "" {
		tags := strings.Split(tagsParam, ",")
		var cleaned []string
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t != "" {
				cleaned = append(cleaned, t)
			}
		}
		if len(cleaned) > 0 {
			filters.Tags = cleaned
		}
	}

	if cursorParam := r.URL.Query().Get("cursor"); cursorParam != "" {
		t, err := time.Parse(time.RFC3339Nano, cursorParam)
		if err == nil {
			filters.Cursor = &t
		}
	}

	tasks, err := h.taskStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list eval tasks", err)
		return
	}
	if tasks == nil {
		tasks = []models.EvalTask{}
	}

	var nextCursor string
	if len(tasks) == filters.Limit {
		nextCursor = tasks[len(tasks)-1].CreatedAt.Format(time.RFC3339Nano)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.EvalTask]{
		Data: tasks,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *EvalHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		RepoID            uuid.UUID              `json:"repo_id"`
		Name              string                 `json:"name"`
		Description       string                 `json:"description"`
		BaseCommitSHA     string                 `json:"base_commit_sha"`
		SolutionCommitSHA *string                `json:"solution_commit_sha"`
		SolutionDiff      *string                `json:"solution_diff"`
		IssueDescription  string                 `json:"issue_description"`
		IssueContext      json.RawMessage        `json:"issue_context"`
		ScoringCriteria   json.RawMessage        `json:"scoring_criteria"`
		PassThreshold     *float64               `json:"pass_threshold"`
		Source            *models.EvalTaskSource `json:"source"`
		SourcePRNumber    *int                   `json:"source_pr_number"`
		Complexity        *models.EvalComplexity `json:"complexity"`
		Tags              []string               `json:"tags"`
		ContextOverrides  json.RawMessage        `json:"context_overrides"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if req.BaseCommitSHA == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "base_commit_sha is required")
		return
	}
	if req.IssueDescription == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "issue_description is required")
		return
	}
	if req.RepoID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "repo_id is required")
		return
	}

	if req.ScoringCriteria != nil {
		if !validateScoringCriteria(w, r, req.ScoringCriteria) {
			return
		}
	}

	source := models.EvalTaskSourceManual
	if req.Source != nil {
		if err := req.Source.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE", "invalid source value")
			return
		}
		source = *req.Source
	}

	complexity := models.EvalComplexityModerate
	if req.Complexity != nil {
		if err := req.Complexity.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_COMPLEXITY", "invalid complexity value")
			return
		}
		complexity = *req.Complexity
	}

	passThreshold := 0.7
	if req.PassThreshold != nil {
		passThreshold = *req.PassThreshold
	}

	issueContext := json.RawMessage(`{}`)
	if req.IssueContext != nil {
		issueContext = req.IssueContext
	}

	contextOverrides := json.RawMessage(`{}`)
	if req.ContextOverrides != nil {
		contextOverrides = req.ContextOverrides
	}

	scoringCriteria := json.RawMessage(`[]`)
	if req.ScoringCriteria != nil {
		scoringCriteria = req.ScoringCriteria
	}

	tags := []string{}
	if req.Tags != nil {
		tags = req.Tags
	}

	task := models.EvalTask{
		OrgID:             orgID,
		RepoID:            req.RepoID,
		Name:              req.Name,
		Description:       req.Description,
		BaseCommitSHA:     req.BaseCommitSHA,
		SolutionCommitSHA: req.SolutionCommitSHA,
		SolutionDiff:      req.SolutionDiff,
		IssueDescription:  req.IssueDescription,
		IssueContext:      issueContext,
		ContextOverrides:  contextOverrides,
		ScoringCriteria:   scoringCriteria,
		PassThreshold:     passThreshold,
		Source:            source,
		SourcePRNumber:    req.SourcePRNumber,
		Complexity:        complexity,
		Tags:              tags,
		CreatedBy:         &user.ID,
	}

	if err := h.taskStore.Create(r.Context(), &task); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create eval task", err)
		return
	}

	if h.audit != nil {
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionEvalTaskCreated,
			ResourceType: models.AuditResourceEvalTask,
			ResourceID:   strPtr(task.ID.String()),
		})
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.EvalTask]{Data: task})
}

func (h *EvalHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	task, err := h.taskStore.GetByID(r.Context(), orgID, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval task not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval task", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalTask]{Data: task})
}

func (h *EvalHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	task, err := h.taskStore.GetByID(r.Context(), orgID, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval task not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval task", err)
		return
	}

	var req struct {
		Name             *string                `json:"name"`
		Description      *string                `json:"description"`
		IssueDescription *string                `json:"issue_description"`
		IssueContext     json.RawMessage        `json:"issue_context"`
		ScoringCriteria  json.RawMessage        `json:"scoring_criteria"`
		PassThreshold    *float64               `json:"pass_threshold"`
		Complexity       *models.EvalComplexity `json:"complexity"`
		Tags             []string               `json:"tags"`
		ContextOverrides json.RawMessage        `json:"context_overrides"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Name != nil {
		task.Name = *req.Name
	}
	if req.Description != nil {
		task.Description = *req.Description
	}
	if req.IssueDescription != nil {
		task.IssueDescription = *req.IssueDescription
	}
	if req.IssueContext != nil {
		task.IssueContext = req.IssueContext
	}
	if req.ScoringCriteria != nil {
		if !validateScoringCriteria(w, r, req.ScoringCriteria) {
			return
		}
		task.ScoringCriteria = req.ScoringCriteria
	}
	if req.PassThreshold != nil {
		task.PassThreshold = *req.PassThreshold
	}
	if req.Complexity != nil {
		task.Complexity = *req.Complexity
	}
	if req.Tags != nil {
		task.Tags = req.Tags
	}
	if req.ContextOverrides != nil {
		task.ContextOverrides = req.ContextOverrides
	}

	if err := h.taskStore.Update(r.Context(), &task); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update eval task", err)
		return
	}

	if h.audit != nil {
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionEvalTaskUpdated,
			ResourceType: models.AuditResourceEvalTask,
			ResourceID:   strPtr(task.ID.String()),
		})
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalTask]{Data: task})
}

func (h *EvalHandler) ArchiveTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	if err := h.taskStore.Archive(r.Context(), orgID, taskID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval task not found or already archived")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ARCHIVE_FAILED", "failed to archive eval task", err)
		return
	}

	if h.audit != nil {
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionEvalTaskArchived,
			ResourceType: models.AuditResourceEvalTask,
			ResourceID:   strPtr(taskID.String()),
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Eval Runs ---

func (h *EvalHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	// Verify task exists and belongs to this org
	if _, err := h.taskStore.GetByID(r.Context(), orgID, taskID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval task not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval task", err)
		return
	}

	var req struct {
		Model            string          `json:"model"`
		ConfigRef        *string         `json:"config_ref"`
		ContextOverrides json.RawMessage `json:"context_overrides"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Model == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "model is required")
		return
	}

	contextOverrides := json.RawMessage(`{}`)
	if req.ContextOverrides != nil {
		contextOverrides = req.ContextOverrides
	}

	// Create run + enqueue job atomically in a transaction
	ctx := r.Context()
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRunStore := db.NewEvalRunStore(tx)
	txJobStore := db.NewJobStore(tx)

	run := models.EvalRun{
		TaskID:           taskID,
		OrgID:            orgID,
		Model:            req.Model,
		ConfigRef:        req.ConfigRef,
		ContextOverrides: contextOverrides,
	}

	if err := txRunStore.Create(ctx, &run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create eval run", err)
		return
	}

	payload := map[string]string{
		"eval_run_id": run.ID.String(),
		"org_id":      orgID.String(),
	}
	if _, err := txJobStore.Enqueue(ctx, orgID, "eval", "run_eval", payload, 5, nil); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue eval run", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit eval run transaction", err)
		return
	}

	if h.audit != nil {
		h.audit.EmitUserAction(ctx, db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionEvalRunStarted,
			ResourceType: models.AuditResourceEvalRun,
			ResourceID:   strPtr(run.ID.String()),
		})
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.EvalRun]{Data: run})
}

func (h *EvalHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}

	limit := queryInt(r, "limit", 50)
	runs, err := h.runStore.ListByTask(r.Context(), orgID, taskID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list eval runs", err)
		return
	}
	if runs == nil {
		runs = []models.EvalRun{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.EvalRun]{
		Data: runs,
		Meta: models.PaginationMeta{},
	})
}

func (h *EvalHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "runId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval run not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval run", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalRun]{Data: run})
}

// --- Batch Runs ---

func (h *EvalHandler) StartBatch(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		Name    string      `json:"name"`
		TaskIDs []uuid.UUID `json:"task_ids"`
		Configs []struct {
			Model            string          `json:"model"`
			ConfigRef        *string         `json:"config_ref"`
			ContextOverrides json.RawMessage `json:"context_overrides"`
		} `json:"configs"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if len(req.TaskIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "task_ids is required")
		return
	}
	if len(req.Configs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "configs is required (at least one configuration)")
		return
	}

	// Validate all task IDs belong to this org in a single query
	validCount, err := h.taskStore.CountByIDs(r.Context(), orgID, req.TaskIDs)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to validate tasks", err)
		return
	}
	if validCount != len(req.TaskIDs) {
		writeError(w, r, http.StatusBadRequest, "INVALID_TASK_ID",
			fmt.Sprintf("one or more task IDs not found or do not belong to this organization (expected %d, found %d)", len(req.TaskIDs), validCount))
		return
	}

	totalRuns := len(req.TaskIDs) * len(req.Configs)

	// Create batch + all runs + enqueue jobs atomically in a transaction
	ctx := r.Context()
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txBatchStore := db.NewEvalBatchStore(tx)
	txRunStore := db.NewEvalRunStore(tx)
	txJobStore := db.NewJobStore(tx)

	batch := models.EvalBatch{
		OrgID:     orgID,
		Name:      req.Name,
		Status:    models.EvalBatchStatusRunning,
		TaskCount: len(req.TaskIDs),
		RunCount:  totalRuns,
		CreatedBy: &user.ID,
	}
	if err := txBatchStore.Create(ctx, &batch); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create eval batch", err)
		return
	}

	for _, taskID := range req.TaskIDs {
		for _, cfg := range req.Configs {
			contextOverrides := json.RawMessage(`{}`)
			if cfg.ContextOverrides != nil {
				contextOverrides = cfg.ContextOverrides
			}

			run := models.EvalRun{
				TaskID:           taskID,
				OrgID:            orgID,
				BatchID:          &batch.ID,
				Model:            cfg.Model,
				ConfigRef:        cfg.ConfigRef,
				ContextOverrides: contextOverrides,
			}
			if err := txRunStore.Create(ctx, &run); err != nil {
				writeError(w, r, http.StatusInternalServerError, "CREATE_RUN_FAILED", "failed to create eval run", err)
				return
			}

			payload := map[string]string{
				"eval_run_id": run.ID.String(),
				"org_id":      orgID.String(),
				"batch_id":    batch.ID.String(),
			}
			if _, err := txJobStore.Enqueue(ctx, orgID, "eval", "run_eval", payload, 5, nil); err != nil {
				writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue eval run", err)
				return
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit batch transaction", err)
		return
	}

	if h.audit != nil {
		h.audit.EmitUserAction(ctx, db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionEvalBatchStarted,
			ResourceType: models.AuditResourceEvalBatch,
			ResourceID:   strPtr(batch.ID.String()),
		})
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.EvalBatch]{Data: batch})
}

func (h *EvalHandler) GetBatch(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "batchId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid batch ID")
		return
	}

	batch, err := h.batchStore.GetByID(r.Context(), orgID, batchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval batch not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval batch", err)
		return
	}

	runs, err := h.runStore.ListByBatch(r.Context(), orgID, batchID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list batch runs", err)
		return
	}
	if runs == nil {
		runs = []models.EvalRun{}
	}

	detail := models.EvalBatchDetail{
		EvalBatch: batch,
		Runs:      runs,
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalBatchDetail]{Data: detail})
}

// --- Bootstrap ---

// Bootstrap triggers a PR history scan to discover eval task candidates.
func (h *EvalHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		RepoID uuid.UUID `json:"repo_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.RepoID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "repo_id is required")
		return
	}

	run := models.EvalBootstrapRun{
		OrgID:     orgID,
		RepoID:    req.RepoID,
		Status:    models.EvalBootstrapStatusPending,
		CreatedBy: &user.ID,
	}
	if err := h.bootstrapStore.Create(r.Context(), &run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create bootstrap run", err)
		return
	}

	// Enqueue the bootstrap job
	_, err := h.jobStore.Enqueue(r.Context(), orgID, "eval", "run_eval_bootstrap", map[string]string{
		"bootstrap_run_id": run.ID.String(),
		"org_id":           orgID.String(),
		"repo_id":          req.RepoID.String(),
	}, 0, nil)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue bootstrap job", err)
		return
	}

	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.EvalBootstrapRun]{Data: run})
}

// GetBootstrapCandidates returns the candidates from a bootstrap run.
func (h *EvalHandler) GetBootstrapCandidates(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	// Get specific run by ID or latest for a repo
	if runIDStr := r.URL.Query().Get("bootstrap_run_id"); runIDStr != "" {
		runID, err := uuid.Parse(runIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid bootstrap_run_id")
			return
		}
		run, err := h.bootstrapStore.GetByID(r.Context(), orgID, runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "NOT_FOUND", "bootstrap run not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch bootstrap run", err)
			return
		}
		writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalBootstrapRun]{Data: run})
		return
	}

	repoIDStr := r.URL.Query().Get("repo_id")
	if repoIDStr == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_PARAM", "repo_id or bootstrap_run_id query param required")
		return
	}
	repoID, err := uuid.Parse(repoIDStr)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repo_id")
		return
	}

	run, err := h.bootstrapStore.GetLatestByOrg(r.Context(), orgID, repoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "no bootstrap runs found for this repo")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch bootstrap run", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalBootstrapRun]{Data: run})
}

// AcceptBootstrapCandidates creates eval tasks from selected bootstrap candidates.
func (h *EvalHandler) AcceptBootstrapCandidates(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		BootstrapRunID uuid.UUID `json:"bootstrap_run_id"`
		CandidateIndices []int   `json:"candidate_indices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.BootstrapRunID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "bootstrap_run_id is required")
		return
	}

	run, err := h.bootstrapStore.GetByID(r.Context(), orgID, req.BootstrapRunID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "bootstrap run not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch bootstrap run", err)
		return
	}

	if run.Status != models.EvalBootstrapStatusCompleted {
		writeError(w, r, http.StatusBadRequest, "NOT_READY", "bootstrap run is not completed")
		return
	}

	var candidates []models.EvalBootstrapCandidate
	if err := json.Unmarshal(run.Candidates, &candidates); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PARSE_FAILED", "failed to parse bootstrap candidates", err)
		return
	}

	var created []models.EvalTask
	for _, idx := range req.CandidateIndices {
		if idx < 0 || idx >= len(candidates) {
			continue
		}
		c := candidates[idx]
		criteriaJSON, _ := json.Marshal(c.ScoringCriteria)
		prNum := c.PRNumber

		task := models.EvalTask{
			OrgID:            orgID,
			RepoID:           run.RepoID,
			Name:             c.PRTitle,
			Description:      fmt.Sprintf("Bootstrapped from PR #%d", c.PRNumber),
			BaseCommitSHA:    c.BaseCommitSHA,
			SolutionCommitSHA: &c.SolutionCommitSHA,
			SolutionDiff:     &c.SolutionDiff,
			IssueDescription: c.IssueDescription,
			ScoringCriteria:  criteriaJSON,
			PassThreshold:    0.7,
			Source:           models.EvalTaskSourcePRBootstrap,
			SourcePRNumber:   &prNum,
			Complexity:       c.Complexity,
			CreatedBy:        &user.ID,
		}
		if err := h.taskStore.Create(r.Context(), &task); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED",
				fmt.Sprintf("failed to create task from candidate %d", idx), err)
			return
		}
		created = append(created, task)
	}

	writeJSON(w, http.StatusCreated, models.ListResponse[models.EvalTask]{Data: created})
}
