package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	taskStore          *db.EvalTaskStore
	runStore           *db.EvalRunStore
	batchStore         *db.EvalBatchStore
	bootstrapStore     *db.EvalBootstrapStore
	datasetStore       *db.EvalDatasetStore
	releaseGateStore   *db.EvalReleaseGateStore
	repositoryStore    *db.RepositoryStore
	sessionStore       *db.SessionStore
	jobStore           *db.JobStore
	txStarter          db.TxStarter
	audit              *db.AuditEmitter
	batchStreams       *cache.EvalBatchStreams
	bootstrapStreams   *cache.EvalBootstrapStreams
	memberships        evalMembershipStore
	candidateValidator evalCandidateValidator
}

type evalCandidateValidator interface {
	ValidateEvalCandidate(ctx context.Context, orgID uuid.UUID, repoID uuid.UUID, candidate models.EvalBootstrapCandidate) error
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

func (h *EvalHandler) SetSessionStore(store *db.SessionStore) {
	h.sessionStore = store
}

func (h *EvalHandler) SetDatasetStore(store *db.EvalDatasetStore) {
	h.datasetStore = store
}

func (h *EvalHandler) SetReleaseGateStore(store *db.EvalReleaseGateStore) {
	h.releaseGateStore = store
}

func (h *EvalHandler) SetRepositoryStore(store *db.RepositoryStore) {
	h.repositoryStore = store
}

func (h *EvalHandler) SetCandidateValidator(validator evalCandidateValidator) {
	h.candidateValidator = validator
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

// --- Eval Datasets and Release Gates ---

func (h *EvalHandler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	if h.datasetStore == nil {
		writeError(w, r, http.StatusNotImplemented, "DATASETS_DISABLED", "eval datasets are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var repoID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("repository_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository_id must be a valid UUID")
			return
		}
		repoID = &parsed
	}
	datasets, err := h.datasetStore.ListByOrg(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list eval datasets", err)
		return
	}
	if datasets == nil {
		datasets = []models.EvalDataset{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.EvalDataset]{Data: datasets})
}

func (h *EvalHandler) CreateDataset(w http.ResponseWriter, r *http.Request) {
	if h.datasetStore == nil {
		writeError(w, r, http.StatusNotImplemented, "DATASETS_DISABLED", "eval datasets are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	var req struct {
		RepositoryID  *uuid.UUID             `json:"repository_id"`
		Name          string                 `json:"name"`
		DatasetType   models.EvalDatasetType `json:"dataset_type"`
		Description   string                 `json:"description"`
		SourceSummary string                 `json:"source_summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if req.DatasetType == "" {
		req.DatasetType = models.EvalDatasetTypeGolden
	}
	if err := req.DatasetType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DATASET_TYPE", "dataset_type must be golden, shadow, or adversarial")
		return
	}
	if req.RepositoryID != nil {
		if h.repositoryStore == nil {
			writeError(w, r, http.StatusNotImplemented, "REPOSITORY_VALIDATION_DISABLED", "repository validation is not configured")
			return
		}
		if _, err := h.repositoryStore.GetByID(r.Context(), orgID, *req.RepositoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository was not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to validate repository", err)
			return
		}
	}
	var createdBy *uuid.UUID
	if user != nil {
		createdBy = &user.ID
	}
	dataset := &models.EvalDataset{
		OrgID:           orgID,
		RepositoryID:    req.RepositoryID,
		Name:            req.Name,
		DatasetType:     req.DatasetType,
		Status:          models.EvalDatasetStatusActive,
		Description:     req.Description,
		SourceSummary:   req.SourceSummary,
		CreatedByUserID: createdBy,
	}
	if err := h.datasetStore.Create(r.Context(), dataset); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create eval dataset", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.EvalDataset]{Data: *dataset})
}

func (h *EvalHandler) AddDatasetTask(w http.ResponseWriter, r *http.Request) {
	if h.datasetStore == nil {
		writeError(w, r, http.StatusNotImplemented, "DATASETS_DISABLED", "eval datasets are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	datasetID, err := uuid.Parse(chi.URLParam(r, "datasetId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DATASET_ID", "dataset id must be a valid UUID")
		return
	}
	var req struct {
		TaskID   uuid.UUID `json:"task_id"`
		SliceKey string    `json:"slice_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if req.TaskID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "task_id is required")
		return
	}
	task, err := h.datasetStore.AddTask(r.Context(), orgID, datasetID, req.TaskID, strings.TrimSpace(req.SliceKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "DATASET_OR_TASK_NOT_FOUND", "dataset or task was not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ADD_TASK_FAILED", "failed to add task to eval dataset", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalDatasetTask]{Data: task})
}

func (h *EvalHandler) ListReleaseGates(w http.ResponseWriter, r *http.Request) {
	if h.releaseGateStore == nil {
		writeError(w, r, http.StatusNotImplemented, "RELEASE_GATES_DISABLED", "eval release gates are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	gates, err := h.releaseGateStore.ListActive(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list eval release gates", err)
		return
	}
	if gates == nil {
		gates = []models.EvalReleaseGate{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.EvalReleaseGate]{Data: gates})
}

func (h *EvalHandler) UpsertReleaseGate(w http.ResponseWriter, r *http.Request) {
	if h.releaseGateStore == nil {
		writeError(w, r, http.StatusNotImplemented, "RELEASE_GATES_DISABLED", "eval release gates are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	var req struct {
		GateName            string          `json:"gate_name"`
		Enabled             *bool           `json:"enabled"`
		DatasetID           *uuid.UUID      `json:"dataset_id"`
		MinPassAt1          *float64        `json:"min_pass_at_1"`
		MinPassAtK          *float64        `json:"min_pass_at_k"`
		MaxPolicyViolations *int            `json:"max_policy_violations"`
		MaxRegressionDelta  *float64        `json:"max_regression_delta"`
		CanaryStages        json.RawMessage `json:"canary_stages"`
		RollbackRules       json.RawMessage `json:"rollback_rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.GateName = strings.TrimSpace(req.GateName)
	if req.GateName == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "gate_name is required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	minPassAt1 := 0.8
	if req.MinPassAt1 != nil {
		minPassAt1 = *req.MinPassAt1
	}
	minPassAtK := 0.8
	if req.MinPassAtK != nil {
		minPassAtK = *req.MinPassAtK
	}
	maxPolicyViolations := 0
	if req.MaxPolicyViolations != nil {
		maxPolicyViolations = *req.MaxPolicyViolations
	}
	maxRegressionDelta := 0.0
	if req.MaxRegressionDelta != nil {
		maxRegressionDelta = *req.MaxRegressionDelta
	}
	if minPassAt1 < 0 || minPassAt1 > 1 || minPassAtK < 0 || minPassAtK > 1 || maxPolicyViolations < 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_GATE", "release gate thresholds must be within valid ranges")
		return
	}
	if req.DatasetID != nil {
		if h.datasetStore == nil {
			writeError(w, r, http.StatusNotImplemented, "DATASET_VALIDATION_DISABLED", "eval dataset validation is not configured")
			return
		}
		if _, err := h.datasetStore.GetByID(r.Context(), orgID, *req.DatasetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "DATASET_NOT_FOUND", "eval dataset was not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "DATASET_LOOKUP_FAILED", "failed to validate eval dataset", err)
			return
		}
	}
	var updatedBy *uuid.UUID
	if user != nil {
		updatedBy = &user.ID
	}
	gate := &models.EvalReleaseGate{
		OrgID:               orgID,
		GateName:            req.GateName,
		Enabled:             enabled,
		DatasetID:           req.DatasetID,
		MinPassAt1:          minPassAt1,
		MinPassAtK:          minPassAtK,
		MaxPolicyViolations: maxPolicyViolations,
		MaxRegressionDelta:  maxRegressionDelta,
		CanaryStages:        req.CanaryStages,
		RollbackRules:       req.RollbackRules,
		UpdatedByUserID:     updatedBy,
		Active:              true,
	}
	if err := h.releaseGateStore.Upsert(r.Context(), gate); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to save eval release gate", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalReleaseGate]{Data: *gate})
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
	user := middleware.UserFromContext(r.Context())
	var session *models.Session
	if h.sessionStore != nil {
		txSessionStore := db.NewSessionStore(tx)
		session = evalRunSessionFromTask(orgID, task, req.Model, req.ConfigRef, user)
		if err := txSessionStore.CreateInTx(ctx, tx, session); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_SESSION_FAILED", "failed to create eval run session", err)
			return
		}
	}

	run := models.EvalRun{
		TaskID:           taskID,
		OrgID:            orgID,
		Model:            req.Model,
		ConfigRef:        req.ConfigRef,
		ContextOverrides: contextOverrides,
	}
	if session != nil {
		run.SessionID = &session.ID
		run.ThreadID = session.PrimaryThreadID
	}

	if err := txRunStore.Create(ctx, &run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create eval run", err)
		return
	}

	payload := map[string]string{
		"eval_run_id": run.ID.String(),
		"org_id":      orgID.String(),
	}
	queue := "eval"
	jobType := "run_eval"
	var dedupeKey *string
	var enqueuePayload any = payload
	if session != nil {
		queue = "agent"
		jobType = "run_agent"
		runAgentDedupeKey := db.RunAgentDedupeKey(session.ID)
		dedupeKey = &runAgentDedupeKey
		enqueuePayload = db.RunAgentPayload(session)
	}
	jobID, err := txJobStore.Enqueue(ctx, orgID, queue, jobType, enqueuePayload, 5, dedupeKey)
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

func evalRunAgentType(model string) models.AgentType {
	switch model {
	case "gemini-cli":
		return models.AgentTypeGeminiCLI
	case "claude-opus-4-6", "claude-sonnet-4-6":
		return models.AgentTypeClaudeCode
	default:
		return models.AgentTypeCodex
	}
}

func evalRunSessionFromTask(orgID uuid.UUID, task models.EvalTask, model string, configRef *string, user *models.User) *models.Session {
	title := "Eval: " + task.Name
	agentType := evalRunAgentType(model)
	baseCommitSHA := task.BaseCommitSHA
	var modelOverride *string
	if model != "codex" && model != "gemini-cli" {
		modelOverride = &model
	}
	var createdBy *uuid.UUID
	if user != nil {
		createdBy = &user.ID
	}
	configLine := "Use the repository's default runtime configuration."
	if configRef != nil && strings.TrimSpace(*configRef) != "" {
		configLine = fmt.Sprintf("Use eval config ref %s. Apply that configuration before starting the task.", strings.TrimSpace(*configRef))
	}
	inputManifestMap := map[string]any{
		"eval_task_id":    task.ID.String(),
		"base_commit_sha": task.BaseCommitSHA,
		"model":           model,
	}
	if task.SolutionCommitSHA != nil && *task.SolutionCommitSHA != "" {
		inputManifestMap["solution_commit_sha"] = *task.SolutionCommitSHA
	}
	if configRef != nil && strings.TrimSpace(*configRef) != "" {
		inputManifestMap["config_ref"] = strings.TrimSpace(*configRef)
	}
	inputManifest, err := json.Marshal(inputManifestMap)
	if err != nil {
		inputManifest = json.RawMessage(`{}`)
	}
	prompt := fmt.Sprintf(`Run this coding-agent eval exactly as specified.

Repository setup:
- Start from base commit %s before changing code.
- %s
- Do not inspect or apply the known solution diff.

Task:
%s

When finished, leave the working tree with only the changes needed to solve the task.`, task.BaseCommitSHA, configLine, task.IssueDescription)
	return &models.Session{
		OrgID:             orgID,
		Origin:            models.SessionOriginEvalRun,
		InteractionMode:   models.SessionInteractionModeSingleRun,
		ValidationPolicy:  models.SessionValidationPolicyOnSessionEnd,
		AgentType:         agentType,
		Status:            models.SessionStatusPending,
		AutonomyLevel:     models.DefaultSessionAutonomy,
		TokenMode:         models.SessionTokenModeHigh,
		ModelOverride:     modelOverride,
		TriggeredByUserID: createdBy,
		Title:             &title,
		PMApproach:        &prompt,
		RepositoryID:      &task.RepoID,
		BaseCommitSHA:     &baseCommitSHA,
		InputManifest:     inputManifest,
	}
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
	tasksByID := make(map[uuid.UUID]models.EvalTask, len(req.TaskIDs))
	if h.sessionStore != nil {
		for _, taskID := range req.TaskIDs {
			task, err := h.taskStore.GetByID(r.Context(), orgID, taskID)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch eval task", err)
				return
			}
			tasksByID[taskID] = task
		}
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
	var txSessionStore *db.SessionStore
	if h.sessionStore != nil {
		txSessionStore = db.NewSessionStore(tx)
	}

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
			var session *models.Session
			if txSessionStore != nil {
				task := tasksByID[taskID]
				session = evalRunSessionFromTask(orgID, task, cfg.Model, cfg.ConfigRef, user)
				if err := txSessionStore.CreateInTx(ctx, tx, session); err != nil {
					writeError(w, r, http.StatusInternalServerError, "CREATE_SESSION_FAILED", "failed to create eval run session", err)
					return
				}
			}

			run := models.EvalRun{
				TaskID:           taskID,
				OrgID:            orgID,
				BatchID:          &batch.ID,
				Model:            cfg.Model,
				ConfigRef:        cfg.ConfigRef,
				ContextOverrides: contextOverrides,
			}
			if session != nil {
				run.SessionID = &session.ID
				run.ThreadID = session.PrimaryThreadID
			}
			if err := txRunStore.Create(ctx, &run); err != nil {
				writeError(w, r, http.StatusInternalServerError, "CREATE_RUN_FAILED", "failed to create eval run", err)
				return
			}

			queue := "eval"
			jobType := "run_eval"
			enqueuePayload := any(map[string]string{
				"eval_run_id": run.ID.String(),
				"org_id":      orgID.String(),
				"batch_id":    batch.ID.String(),
			})
			var dedupeKey *string
			if session != nil {
				queue = "agent"
				jobType = "run_agent"
				runAgentDedupeKey := db.RunAgentDedupeKey(session.ID)
				dedupeKey = &runAgentDedupeKey
				enqueuePayload = db.RunAgentPayload(session)
			}
			if _, err := txJobStore.Enqueue(ctx, orgID, queue, jobType, enqueuePayload, 5, dedupeKey); err != nil {
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

func (h *EvalHandler) StartCompare(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	type compareConfig struct {
		Model            string          `json:"model"`
		ConfigRef        *string         `json:"config_ref"`
		ContextOverrides json.RawMessage `json:"context_overrides"`
	}
	var req struct {
		Name             string          `json:"name"`
		TaskIDs          []uuid.UUID     `json:"task_ids"`
		BaselineConfig   compareConfig   `json:"baseline_config"`
		CandidateConfigs []compareConfig `json:"candidate_configs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if len(req.TaskIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "task_ids is required")
		return
	}
	if strings.TrimSpace(req.BaselineConfig.Model) == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "baseline_config.model is required")
		return
	}
	if len(req.CandidateConfigs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "candidate_configs is required")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Compare config"
	}
	configs := make([]compareConfig, 0, 1+len(req.CandidateConfigs))
	configs = append(configs, req.BaselineConfig)
	configs = append(configs, req.CandidateConfigs...)
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"task_ids": req.TaskIDs,
		"configs":  configs,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COMPARE_BUILD_FAILED", "failed to build compare batch", err)
		return
	}
	forward := r.Clone(r.Context())
	forward.Body = io.NopCloser(bytes.NewReader(body))
	h.StartBatch(w, forward)
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
	var gateDecisions []models.EvalReleaseGateDecision
	if h.releaseGateStore != nil {
		gateDecisions, err = h.releaseGateStore.ListDecisionsByBatch(r.Context(), orgID, batchID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list release gate decisions", err)
			return
		}
	}
	if gateDecisions == nil {
		gateDecisions = []models.EvalReleaseGateDecision{}
	}

	detail := models.EvalBatchDetail{
		EvalBatch:     batch,
		Runs:          runs,
		GateDecisions: gateDecisions,
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

	if h.sessionStore != nil && h.txStarter != nil {
		h.bootstrapSessionBacked(w, r, orgID, user, req.RepoID)
		return
	}
	writeError(w, r, http.StatusInternalServerError, "SESSION_BACKING_REQUIRED", "eval bootstrap requires session-backed execution")
}

func (h *EvalHandler) bootstrapSessionBacked(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, user *models.User, repoID uuid.UUID) {
	ctx := r.Context()
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start bootstrap transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txSessionStore := db.NewSessionStore(tx)
	txBootstrapStore := db.NewEvalBootstrapStore(tx)
	txJobStore := db.NewJobStore(tx)

	title := "Bootstrap eval tasks"
	prompt := "Analyze this repository's pull request history and add high-quality candidate eval tasks with `143-tools eval add`."
	var createdBy *uuid.UUID
	if user != nil {
		createdBy = &user.ID
	}
	session := &models.Session{
		OrgID:             orgID,
		Origin:            models.SessionOriginEvalBootstrap,
		InteractionMode:   models.SessionInteractionModeSingleRun,
		ValidationPolicy:  models.SessionValidationPolicyOnSessionEnd,
		AgentType:         models.AgentTypeCodex,
		Status:            models.SessionStatusPending,
		AutonomyLevel:     models.DefaultSessionAutonomy,
		TokenMode:         models.SessionTokenModeLow,
		TriggeredByUserID: createdBy,
		Title:             &title,
		PMApproach:        &prompt,
		RepositoryID:      &repoID,
	}
	if err := txSessionStore.CreateInTx(ctx, tx, session); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_SESSION_FAILED", "failed to create eval bootstrap session", err)
		return
	}
	if session.PrimaryThreadID == nil || *session.PrimaryThreadID == uuid.Nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_SESSION_FAILED", "failed to create eval bootstrap session thread")
		return
	}

	run := models.EvalBootstrapRun{
		OrgID:     orgID,
		RepoID:    repoID,
		Status:    models.EvalBootstrapStatusPending,
		SessionID: &session.ID,
		ThreadID:  session.PrimaryThreadID,
		CreatedBy: createdBy,
	}
	if err := txBootstrapStore.Create(ctx, &run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create bootstrap run", err)
		return
	}
	dedupeKey := db.RunAgentDedupeKey(session.ID)
	if _, err := txJobStore.Enqueue(ctx, orgID, "agent", "run_agent", db.RunAgentPayload(session), 5, &dedupeKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue eval bootstrap session", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit eval bootstrap session", err)
		return
	}

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
		h.attachNormalizedBootstrapCandidates(r.Context(), orgID, &run)
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
	h.attachNormalizedBootstrapCandidates(r.Context(), orgID, &run)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.EvalBootstrapRun]{Data: run})
}

func (h *EvalHandler) attachNormalizedBootstrapCandidates(ctx context.Context, orgID uuid.UUID, run *models.EvalBootstrapRun) {
	if h.bootstrapStore == nil || run == nil || run.ID == uuid.Nil {
		return
	}
	rows, err := h.bootstrapStore.ListCandidatesByRun(ctx, orgID, run.ID)
	if err != nil || len(rows) == 0 {
		return
	}
	payloads := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		candidate := row.Candidate()
		rawCandidate, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(rawCandidate, &payload); err != nil {
			continue
		}
		payload["id"] = row.ID.String()
		payload["status"] = string(row.Status)
		payload["candidate_index"] = row.CandidateIndex
		if row.RejectionReason != nil {
			payload["rejection_reason"] = *row.RejectionReason
		}
		if row.AcceptedTaskID != nil {
			payload["accepted_task_id"] = row.AcceptedTaskID.String()
			payload["created_task_id"] = row.AcceptedTaskID.String()
		}
		enriched, err := json.Marshal(payload)
		if err != nil {
			payloads = append(payloads, row.Payload)
			continue
		}
		payloads = append(payloads, enriched)
	}
	raw, err := json.Marshal(payloads)
	if err != nil {
		return
	}
	run.Candidates = raw
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
		BootstrapRunID   uuid.UUID   `json:"bootstrap_run_id"`
		CandidateIndices []int       `json:"candidate_indices"`
		CandidateIDs     []uuid.UUID `json:"candidate_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.BootstrapRunID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "bootstrap_run_id is required")
		return
	}
	if len(req.CandidateIndices) == 0 && len(req.CandidateIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "candidate_indices or candidate_ids is required")
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

	var candidateRows []models.EvalBootstrapCandidateRow
	if len(req.CandidateIDs) > 0 {
		for _, candidateID := range req.CandidateIDs {
			row, err := h.bootstrapStore.GetCandidateByID(r.Context(), orgID, candidateID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE_ID", "candidate not found")
					return
				}
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch candidate", err)
				return
			}
			if row.BootstrapRunID != req.BootstrapRunID {
				writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE_ID", "candidate does not belong to bootstrap run")
				return
			}
			candidateRows = append(candidateRows, row)
		}
	} else if rows, err := h.bootstrapStore.ListCandidatesByRun(r.Context(), orgID, req.BootstrapRunID); err == nil && len(rows) > 0 {
		for _, idx := range req.CandidateIndices {
			if idx < 0 || idx >= len(rows) {
				writeError(w, r, http.StatusBadRequest, "INVALID_INDEX",
					fmt.Sprintf("candidate index %d is out of range (0-%d)", idx, len(rows)-1))
				return
			}
			candidateRows = append(candidateRows, rows[idx])
		}
	}

	var candidates []models.EvalBootstrapCandidate
	if len(candidateRows) > 0 {
		for _, row := range candidateRows {
			if row.Status != models.EvalBootstrapCandidateStatusProposed {
				writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE_STATUS", "only proposed candidates can be accepted")
				return
			}
			candidates = append(candidates, row.Candidate())
		}
	} else {
		var allCandidates []models.EvalBootstrapCandidate
		if err := json.Unmarshal(run.Candidates, &candidates); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PARSE_FAILED", "failed to parse bootstrap candidates", err)
			return
		}
		allCandidates = candidates

		// Validate all indices are in range before creating anything
		candidates = []models.EvalBootstrapCandidate{}
		for _, idx := range req.CandidateIndices {
			if idx < 0 || idx >= len(allCandidates) {
				writeError(w, r, http.StatusBadRequest, "INVALID_INDEX",
					fmt.Sprintf("candidate index %d is out of range (0-%d)", idx, len(allCandidates)-1))
				return
			}
			candidates = append(candidates, allCandidates[idx])
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
	for i, c := range candidates {
		if err := validateBootstrapCandidateForAcceptance(c); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE", fmt.Sprintf("candidate %d is invalid: %s", i, err.Error()))
			return
		}
		if h.candidateValidator != nil {
			if err := h.candidateValidator.ValidateEvalCandidate(r.Context(), orgID, run.RepoID, c); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE", fmt.Sprintf("candidate %d failed repository validation: %s", i, err.Error()), err)
				return
			}
		}
		criteriaJSON, err := json.Marshal(c.ScoringCriteria)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "MARSHAL_FAILED",
				fmt.Sprintf("failed to marshal scoring criteria for candidate %d", i), err)
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
				fmt.Sprintf("failed to create task from candidate %d", i), err)
			return
		}
		if len(candidateRows) > i {
			txBootstrapStore := db.NewEvalBootstrapStore(tx)
			var reviewedBy *uuid.UUID
			if user != nil {
				reviewedBy = &user.ID
			}
			if err := txBootstrapStore.MarkCandidateAccepted(ctx, orgID, candidateRows[i].ID, task.ID, reviewedBy); err != nil {
				writeError(w, r, http.StatusInternalServerError, "UPDATE_CANDIDATE_FAILED",
					fmt.Sprintf("failed to mark candidate %d accepted", i), err)
				return
			}
		}
		created = append(created, task)
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit accepted candidates", err)
		return
	}

	writeJSON(w, http.StatusCreated, models.ListResponse[models.EvalTask]{Data: created})
}

func validateBootstrapCandidateForAcceptance(c models.EvalBootstrapCandidate) error {
	if strings.TrimSpace(c.PRTitle) == "" {
		return errors.New("pr_title is required")
	}
	if !validGitSHA.MatchString(strings.TrimSpace(c.BaseCommitSHA)) {
		return errors.New("base_commit_sha must be a valid git SHA")
	}
	if !validGitSHA.MatchString(strings.TrimSpace(c.SolutionCommitSHA)) {
		return errors.New("solution_commit_sha must be a valid git SHA")
	}
	if strings.TrimSpace(c.SolutionDiff) == "" {
		return errors.New("solution_diff is required")
	}
	if strings.TrimSpace(c.IssueDescription) == "" {
		return errors.New("issue_description is required")
	}
	if err := c.Complexity.Validate(); err != nil {
		return err
	}
	if len(c.ScoringCriteria) == 0 {
		return errors.New("scoring_criteria is required")
	}
	for i, criterion := range c.ScoringCriteria {
		if strings.TrimSpace(criterion.Name) == "" {
			return fmt.Errorf("scoring_criteria[%d].name is required", i)
		}
		if err := criterion.GraderType.Validate(); err != nil {
			return fmt.Errorf("scoring_criteria[%d].grader_type: %w", i, err)
		}
	}
	return nil
}

// ReviewBootstrapCandidate records reviewer disposition for a single candidate without creating an eval task.
func (h *EvalHandler) ReviewBootstrapCandidate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	candidateID, err := uuid.Parse(chi.URLParam(r, "candidate_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid candidate ID")
		return
	}

	var req struct {
		Status          models.EvalBootstrapCandidateStatus `json:"status"`
		RejectionReason *string                             `json:"rejection_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.Status == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "status is required")
		return
	}
	if err := req.Status.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "invalid candidate status")
		return
	}
	if req.Status == models.EvalBootstrapCandidateStatusAccepted {
		writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "use the accept endpoint to accept candidates")
		return
	}

	user := middleware.UserFromContext(r.Context())
	var reviewedBy *uuid.UUID
	if user != nil {
		reviewedBy = &user.ID
	}
	if req.RejectionReason != nil {
		reason := strings.TrimSpace(*req.RejectionReason)
		if reason == "" {
			req.RejectionReason = nil
		} else {
			req.RejectionReason = &reason
		}
	}

	if err := h.bootstrapStore.UpdateCandidateReview(r.Context(), orgID, candidateID, req.Status, req.RejectionReason, reviewedBy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "candidate not found or already accepted")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update candidate review", err)
		return
	}

	type reviewCandidateResponse struct {
		CandidateID     uuid.UUID                           `json:"candidate_id"`
		Status          models.EvalBootstrapCandidateStatus `json:"status"`
		RejectionReason *string                             `json:"rejection_reason,omitempty"`
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[reviewCandidateResponse]{
		Data: reviewCandidateResponse{
			CandidateID:     candidateID,
			Status:          req.Status,
			RejectionReason: req.RejectionReason,
		},
	})
}
