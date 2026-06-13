package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// evalMembershipStore is the org-membership lookup the eval SSE handlers use
// to validate explicit ?org_id= query params from EventSource clients (which
// can't send custom request headers). Mirrors pullRequestMembershipStore in
// pull_requests.go — kept as a local interface so tests can stub it without
// pulling in a full membership store implementation.
type evalMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

var (
	errEvalStreamOrgInvalid   = errors.New("invalid eval stream org")
	errEvalStreamOrgForbidden = errors.New("forbidden eval stream org")
	errEvalStreamUnauthorized = errors.New("unauthorized eval stream request")
)

type EvalHandler struct {
	taskStore        *db.EvalTaskStore
	runStore         *db.EvalRunStore
	batchStore       *db.EvalBatchStore
	bootstrapStore   *db.EvalBootstrapStore
	jobStore         *db.JobStore
	txStarter        db.TxStarter
	audit            *db.AuditEmitter
	batchStreams     *cache.EvalBatchStreams
	bootstrapStreams *cache.EvalBootstrapStreams
	memberships      evalMembershipStore
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

// SetBatchStreams wires the Redis-backed batch update fanout. Setting nil
// (or never calling this) disables the SSE endpoint and forces clients to
// fall back to the existing polling path.
func (h *EvalHandler) SetBatchStreams(streams *cache.EvalBatchStreams) {
	h.batchStreams = streams
}

// SetBootstrapStreams is the bootstrap (PR-history scan) counterpart to
// SetBatchStreams.
func (h *EvalHandler) SetBootstrapStreams(streams *cache.EvalBootstrapStreams) {
	h.bootstrapStreams = streams
}

// SetMembershipStore wires the org-membership store used by the SSE handlers
// to validate explicit ?org_id= query params for multi-org users on
// EventSource (which can't send X-Active-Org-ID).
func (h *EvalHandler) SetMembershipStore(store evalMembershipStore) {
	h.memberships = store
}

// publishBatchSignal exists separately from the worker's identical helper
// because the API and the worker live in different packages and pass
// different services structs through their call stacks. The worker uses
// publishEvalBatchSignal in worker/handlers.go; this handler-side variant
// fires from the StartBatch HTTP path so the user lands on the detail page
// with an event already in flight rather than waiting for the first run
// transition.
func (h *EvalHandler) publishBatchSignal(ctx context.Context, orgID, batchID uuid.UUID, status models.EvalBatchStatus) {
	if h.batchStreams == nil || batchID == uuid.Nil {
		return
	}
	if err := h.batchStreams.PublishUpdated(ctx, models.EvalBatchUpdatedEvent{
		BatchID:   batchID,
		OrgID:     orgID,
		Status:    status,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("batch_id", batchID.String()).Msg("failed to publish eval batch update event")
	}
}

func (h *EvalHandler) publishBootstrapSignal(ctx context.Context, orgID, runID uuid.UUID, status models.EvalBootstrapStatus, sessionID *uuid.UUID) {
	if h.bootstrapStreams == nil || runID == uuid.Nil {
		return
	}
	if err := h.bootstrapStreams.PublishUpdated(ctx, models.EvalBootstrapUpdatedEvent{
		BootstrapRunID: runID,
		OrgID:          orgID,
		Status:         status,
		SessionID:      sessionID,
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("bootstrap_run_id", runID.String()).Msg("failed to publish eval bootstrap update event")
	}
}

// streamOrgIDFromRequest mirrors pull_requests.go:streamOrgIDFromRequest.
// EventSource clients can't send X-Active-Org-ID headers, so multi-org users
// pass ?org_id= as a query string and we membership-check it here. Without
// this, a user whose session-hint last_org_id differs from their actively-
// viewed org would 404 on the SSE handshake.
func (h *EvalHandler) streamOrgIDFromRequest(r *http.Request) (uuid.UUID, error) {
	orgID := middleware.OrgIDFromContext(r.Context())
	requestedRaw := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedRaw == "" {
		return orgID, nil
	}

	requestedOrgID, err := uuid.Parse(requestedRaw)
	if err != nil {
		return uuid.Nil, errEvalStreamOrgInvalid
	}
	if requestedOrgID == orgID {
		return requestedOrgID, nil
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return uuid.Nil, errEvalStreamUnauthorized
	}
	if h.memberships == nil {
		return uuid.Nil, errors.New("membership store not configured")
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, requestedOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errEvalStreamOrgForbidden
		}
		return uuid.Nil, err
	}

	return requestedOrgID, nil
}

// validGitSHA matches a hex string of 4-40 characters (short or full SHA).
var validGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// validGitRef matches a branch name or tag (alphanumeric, dots, dashes, underscores, slashes).
var validGitRef = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// allowedModels is the set of models that can be used for eval runs.
var allowedModels = map[string]bool{
	"claude-opus-4-6":                 true,
	"claude-sonnet-4-6":               true,
	"codex":                           true,
	"gemini-cli":                      true,
	models.OpenCodeModelGPT54Mini:     true,
	models.OpenCodeModelClaudeHaiku45: true,
	models.OpenCodeModelDeepSeekChat:  true,
}

const (
	maxBatchTotalRuns    = 100
	minPassThreshold     = 0.0
	maxPassThreshold     = 1.0
	defaultPassThreshold = 0.7
)

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
	if !validGitSHA.MatchString(req.BaseCommitSHA) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHA", "base_commit_sha must be a valid git SHA (4-40 hex characters)")
		return
	}
	if req.SolutionCommitSHA != nil && *req.SolutionCommitSHA != "" && !validGitSHA.MatchString(*req.SolutionCommitSHA) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHA", "solution_commit_sha must be a valid git SHA (4-40 hex characters)")
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

	passThreshold := defaultPassThreshold
	if req.PassThreshold != nil {
		if *req.PassThreshold < minPassThreshold || *req.PassThreshold > maxPassThreshold {
			writeError(w, r, http.StatusBadRequest, "INVALID_THRESHOLD", "pass_threshold must be between 0.0 and 1.0")
			return
		}
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
		taskIDStr := task.ID.String()
		emitUserAudit(h.audit, r, models.AuditActionEvalTaskCreated, models.AuditResourceEvalTask, &taskIDStr,
			marshalAuditDetails(*zerolog.Ctx(r.Context()), evalTaskAuditSnapshot(&task)))
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
	before := task

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
		if *req.PassThreshold < minPassThreshold || *req.PassThreshold > maxPassThreshold {
			writeError(w, r, http.StatusBadRequest, "INVALID_THRESHOLD", "pass_threshold must be between 0.0 and 1.0")
			return
		}
		task.PassThreshold = *req.PassThreshold
	}
	if req.Complexity != nil {
		if err := req.Complexity.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_COMPLEXITY", "invalid complexity value")
			return
		}
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
		taskIDStr := task.ID.String()
		details := evalTaskAuditSnapshot(&task)
		if changes := evalTaskAuditDiff(&before, &task); len(changes) > 0 {
			details["changes"] = changes
		}
		emitUserAudit(h.audit, r, models.AuditActionEvalTaskUpdated, models.AuditResourceEvalTask, &taskIDStr,
			marshalAuditDetails(*zerolog.Ctx(r.Context()), details))
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalTask]{Data: task})
}

func (h *EvalHandler) ArchiveTask(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid task ID")
		return
	}
	var auditTask *models.EvalTask
	if h.audit != nil {
		task, err := h.taskStore.GetByID(r.Context(), orgID, taskID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "NOT_FOUND", "eval task not found or already archived")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval task", err)
			return
		}
		auditTask = &task
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
		taskIDStr := taskID.String()
		details := map[string]any{"eval_task_id": taskID.String()}
		if auditTask != nil {
			details = evalTaskAuditSnapshot(auditTask)
		}
		details["changes"] = map[string]any{
			"archived_at": auditChange(nil, "set"),
		}
		emitUserAudit(h.audit, r, models.AuditActionEvalTaskArchived, models.AuditResourceEvalTask, &taskIDStr,
			marshalAuditDetails(*zerolog.Ctx(r.Context()), details))
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Eval Runs ---

func (h *EvalHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
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
	if !allowedModels[req.Model] {
		writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", "model must be one of: claude-opus-4-6, claude-sonnet-4-6, codex, gemini-cli")
		return
	}
	if req.ConfigRef != nil && *req.ConfigRef != "" {
		if !validGitRef.MatchString(*req.ConfigRef) && !validGitSHA.MatchString(*req.ConfigRef) {
			writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG_REF", "config_ref must be a valid branch name or git SHA")
			return
		}
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
	jobID, err := txJobStore.Enqueue(ctx, orgID, "eval", "run_eval", payload, 5, nil)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue eval run", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit eval run transaction", err)
		return
	}

	if h.audit != nil {
		runIDStr := run.ID.String()
		emitUserAudit(h.audit, r, models.AuditActionEvalRunStarted, models.AuditResourceEvalRun, &runIDStr,
			marshalAuditDetails(*zerolog.Ctx(r.Context()), evalRunAuditDetails(&run, jobID)))
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

	// Validate each config has a valid model and config_ref
	for i, cfg := range req.Configs {
		if cfg.Model == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", fmt.Sprintf("configs[%d].model is required", i))
			return
		}
		if !allowedModels[cfg.Model] {
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL",
				fmt.Sprintf("configs[%d].model must be one of: claude-opus-4-6, claude-sonnet-4-6, codex, gemini-cli", i))
			return
		}
		if cfg.ConfigRef != nil && *cfg.ConfigRef != "" {
			if !validGitRef.MatchString(*cfg.ConfigRef) && !validGitSHA.MatchString(*cfg.ConfigRef) {
				writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG_REF",
					fmt.Sprintf("configs[%d].config_ref must be a valid branch name or git SHA", i))
				return
			}
		}
	}

	// Cap total runs to prevent resource exhaustion
	totalRuns := len(req.TaskIDs) * len(req.Configs)
	if totalRuns > maxBatchTotalRuns {
		writeError(w, r, http.StatusBadRequest, "BATCH_TOO_LARGE",
			fmt.Sprintf("batch would create %d runs, maximum is %d (reduce tasks or configs)", totalRuns, maxBatchTotalRuns))
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
		batchIDStr := batch.ID.String()
		emitUserAudit(h.audit, r, models.AuditActionEvalBatchStarted, models.AuditResourceEvalBatch, &batchIDStr,
			marshalAuditDetails(*zerolog.Ctx(r.Context()), evalBatchAuditDetails(&batch, req.TaskIDs, len(req.Configs))))
	}

	// Wake any in-flight detail-page SSE subscribers (e.g. the user who just
	// hit "Start batch" and is being redirected) so they don't have to wait
	// for the first run state transition before seeing the new batch.
	h.publishBatchSignal(r.Context(), orgID, batch.ID, batch.Status)

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.EvalBatch]{Data: batch})
}

func (h *EvalHandler) ListBatches(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	limit := queryInt(r, "limit", 20)

	batches, err := h.batchStore.ListByOrg(r.Context(), orgID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list eval batches", err)
		return
	}
	if batches == nil {
		batches = []models.EvalBatch{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.EvalBatch]{Data: batches})
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

// StreamBatchUpdates serves a per-batch SSE stream that emits an event each
// time an EvalBatchUpdatedEvent for this batch arrives over Redis pub/sub.
// The frontend uses this to replace the prior 5s React Query poll on
// /evals/batch/{batchId}; on receipt of an event the client invalidates its
// detail-query cache, which fetches the full EvalBatchDetail (batch + runs).
//
// Authorization: the request goes through the org-scoped middleware chain.
// EventSource clients can't send X-Active-Org-ID, so multi-org users pass
// ?org_id= and we membership-check it via streamOrgIDFromRequest (mirrors
// pull_requests.go). The batch is then verified to belong to the resolved
// org via batchStore.GetByID before the SSE handshake completes — failing
// fast keeps the error path symmetric with the REST GET handler and prevents
// callers from probing other orgs' batch IDs over the SSE channel.
//
// Channel scoping: the Redis pub/sub channel is keyed per batch
// (`{batch:UUID}:eval_batches`), so the subscription only receives events
// for the batch this client is watching — no server-side filter required
// and no cross-batch fanout cost.
//
// Connection lifetime: the auth check is one-shot at handshake. If the
// batch is deleted mid-stream the connection stays open and the per-batch
// pub/sub channel simply goes silent until the request context is canceled
// (browser navigation, logout, or proxy idle timeout). Mirrors the pull-
// request stream's behavior; revisit if/when batch deletion becomes a
// surfaced UX action.
//
// Degraded mode: if streams are not configured (no Redis) or the circuit
// breaker is open, this returns 503. The frontend treats 503 as "fall back
// to polling" rather than a fatal error so the user still sees progress
// while Redis is recovering.
func (h *EvalHandler) StreamBatchUpdates(w http.ResponseWriter, r *http.Request) {
	if h.batchStreams == nil || !h.batchStreams.Available() {
		http.Error(w, "eval batch streams unavailable", http.StatusServiceUnavailable)
		return
	}
	// Satisfy the org-scoping lint (org_id_lint_test.go) — the real
	// resolution happens inside streamOrgIDFromRequest below, which can
	// promote a query-string ?org_id= for multi-org users on EventSource.
	_ = middleware.OrgIDFromContext(r.Context())

	batchID, err := uuid.Parse(chi.URLParam(r, "batchId"))
	if err != nil {
		http.Error(w, "invalid batch ID", http.StatusBadRequest)
		return
	}

	orgID, err := h.streamOrgIDFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errEvalStreamOrgInvalid):
			http.Error(w, "invalid eval stream org", http.StatusBadRequest)
		case errors.Is(err, errEvalStreamOrgForbidden):
			http.Error(w, "forbidden eval stream org", http.StatusForbidden)
		case errors.Is(err, errEvalStreamUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "failed to authorize eval stream", http.StatusInternalServerError)
		}
		return
	}

	if _, err := h.batchStore.GetByID(r.Context(), orgID, batchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "eval batch not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load eval batch", http.StatusInternalServerError)
		return
	}

	sw := sse.NewWriter(w)
	if sw == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sub, err := h.batchStreams.Subscribe(batchID)
	if err != nil {
		http.Error(w, "eval batch streams unavailable", http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()

	logger := zerolog.Ctx(r.Context())
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Msg("failed to write eval batch stream heartbeat")
				return
			}
			sw.Flush()
		case event, ok := <-sub.C:
			if !ok {
				logger.Warn().Str("reason", sub.CloseReason()).Msg("eval batch update subscription closed")
				return
			}
			if err := sw.WriteEvent(sse.EventType("eval_batch.updated"), event); err != nil {
				logger.Warn().Err(err).Str("batch_id", event.BatchID.String()).Msg("failed to write eval batch update event")
				return
			}
			sw.Flush()
		}
	}
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

	// Publish the initial pending event so the bootstrap detail sheet can
	// render an empty-but-active state without polling for the first signal.
	h.publishBootstrapSignal(r.Context(), orgID, run.ID, run.Status, run.SessionID)

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

// StreamBootstrapUpdates serves a per-bootstrap-run SSE stream that wakes
// whenever an EvalBootstrapUpdatedEvent for this run arrives over Redis
// pub/sub. Replaces the prior 3s React Query poll on the bootstrap detail
// sheet. Same authorization, channel-scoping, lifetime, and degraded-mode
// semantics as StreamBatchUpdates above.
func (h *EvalHandler) StreamBootstrapUpdates(w http.ResponseWriter, r *http.Request) {
	if h.bootstrapStreams == nil || !h.bootstrapStreams.Available() {
		http.Error(w, "eval bootstrap streams unavailable", http.StatusServiceUnavailable)
		return
	}
	// See StreamBatchUpdates — same lint-satisfying pattern.
	_ = middleware.OrgIDFromContext(r.Context())

	runID, err := uuid.Parse(chi.URLParam(r, "runId"))
	if err != nil {
		http.Error(w, "invalid bootstrap run ID", http.StatusBadRequest)
		return
	}

	orgID, err := h.streamOrgIDFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errEvalStreamOrgInvalid):
			http.Error(w, "invalid eval stream org", http.StatusBadRequest)
		case errors.Is(err, errEvalStreamOrgForbidden):
			http.Error(w, "forbidden eval stream org", http.StatusForbidden)
		case errors.Is(err, errEvalStreamUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "failed to authorize eval stream", http.StatusInternalServerError)
		}
		return
	}

	if _, err := h.bootstrapStore.GetByID(r.Context(), orgID, runID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "bootstrap run not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load bootstrap run", http.StatusInternalServerError)
		return
	}

	sw := sse.NewWriter(w)
	if sw == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sub, err := h.bootstrapStreams.Subscribe(runID)
	if err != nil {
		http.Error(w, "eval bootstrap streams unavailable", http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()

	logger := zerolog.Ctx(r.Context())
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Msg("failed to write eval bootstrap stream heartbeat")
				return
			}
			sw.Flush()
		case event, ok := <-sub.C:
			if !ok {
				logger.Warn().Str("reason", sub.CloseReason()).Msg("eval bootstrap update subscription closed")
				return
			}
			if err := sw.WriteEvent(sse.EventType("eval_bootstrap.updated"), event); err != nil {
				logger.Warn().Err(err).Str("bootstrap_run_id", event.BootstrapRunID.String()).Msg("failed to write eval bootstrap update event")
				return
			}
			sw.Flush()
		}
	}
}

// AcceptBootstrapCandidates creates eval tasks from selected bootstrap candidates.
func (h *EvalHandler) AcceptBootstrapCandidates(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		BootstrapRunID   uuid.UUID `json:"bootstrap_run_id"`
		CandidateIndices []int     `json:"candidate_indices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.BootstrapRunID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "bootstrap_run_id is required")
		return
	}
	if len(req.CandidateIndices) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "candidate_indices is required (at least one index)")
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

	// Validate all indices are in range before creating anything
	for _, idx := range req.CandidateIndices {
		if idx < 0 || idx >= len(candidates) {
			writeError(w, r, http.StatusBadRequest, "INVALID_INDEX",
				fmt.Sprintf("candidate index %d is out of range (0-%d)", idx, len(candidates)-1))
			return
		}
	}

	// Wrap all task creations in a transaction for atomicity
	ctx := r.Context()
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txTaskStore := db.NewEvalTaskStore(tx)

	var created []models.EvalTask
	for _, idx := range req.CandidateIndices {
		c := candidates[idx]
		criteriaJSON, err := json.Marshal(c.ScoringCriteria)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "MARSHAL_FAILED",
				fmt.Sprintf("failed to marshal scoring criteria for candidate %d", idx), err)
			return
		}
		prNum := c.PRNumber

		task := models.EvalTask{
			OrgID:             orgID,
			RepoID:            run.RepoID,
			Name:              c.PRTitle,
			Description:       fmt.Sprintf("Bootstrapped from PR #%d", c.PRNumber),
			BaseCommitSHA:     c.BaseCommitSHA,
			SolutionCommitSHA: &c.SolutionCommitSHA,
			SolutionDiff:      &c.SolutionDiff,
			IssueDescription:  c.IssueDescription,
			ScoringCriteria:   criteriaJSON,
			PassThreshold:     defaultPassThreshold,
			Source:            models.EvalTaskSourcePRBootstrap,
			SourcePRNumber:    &prNum,
			Complexity:        c.Complexity,
			CreatedBy:         &user.ID,
		}
		if err := txTaskStore.Create(ctx, &task); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED",
				fmt.Sprintf("failed to create task from candidate %d", idx), err)
			return
		}
		created = append(created, task)
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit accepted candidates", err)
		return
	}

	writeJSON(w, http.StatusCreated, models.ListResponse[models.EvalTask]{Data: created})
}
