package handlers

import (
	"context"
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
	"github.com/rs/zerolog"
)

// automationListDefaultLimit and automationListMaxLimit must match the
// store's internal clamp in AutomationStore.ListByOrg/ListByAutomation so
// that the limit we pass in equals the bound actually applied — otherwise
// the next_cursor check `len(results) == filters.Limit` stops pagination
// one page early when a caller asks for more than the store will return.
const (
	automationListDefaultLimit = 25
	automationListMaxLimit     = 100

	// automationStatsDefaultWindow is the default since→until span used when
	// the caller omits ?since (or both). automationStatsMaxWindow is the
	// hard cap; a wider window would force a scan past the
	// (org_id, automation_id, triggered_at DESC) index's hot range. Keep
	// these in sync with the frontend STATS_WINDOW_DAYS (default) — the
	// UI doesn't expose a picker yet but still displays "last 30 days".
	automationStatsDefaultWindow = 30 * 24 * time.Hour
	automationStatsMaxWindow     = 90 * 24 * time.Hour
)

type AutomationHandler struct {
	automationStore    *db.AutomationStore
	automationRunStore *db.AutomationRunStore
	repoStore          automationRepoLookup
	orgStore           automationOrgLookup
	orgCredentialStore automationOrgCredentialLookup
	userCredentialStore automationUserCredentialLookup
	codingAuthStore    automationCodingAuthLookup
	codingCredentialStore automationCodingCredentialLookup
	jobStore           *db.JobStore
	audit              *db.AuditEmitter
	pool               db.TxStarter // needed for transactional RunNow
	logger             zerolog.Logger
}

// automationRepoLookup is the slice of *db.RepositoryStore needed to verify
// that a repository_id supplied by the client belongs to the caller's org.
type automationRepoLookup interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type automationOrgLookup interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

type automationOrgCredentialLookup interface {
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

type automationUserCredentialLookup interface {
	ListTeamDefaults(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedUserCredential, error)
}

type automationCodingAuthLookup interface {
	ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error)
}

type automationCodingCredentialLookup interface {
	ListByScope(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error)
}

func NewAutomationHandler(automationStore *db.AutomationStore, automationRunStore *db.AutomationRunStore) *AutomationHandler {
	return &AutomationHandler{
		automationStore:    automationStore,
		automationRunStore: automationRunStore,
		logger:             zerolog.Nop(),
	}
}

func (h *AutomationHandler) SetJobStore(jobStore *db.JobStore) {
	h.jobStore = jobStore
}

func (h *AutomationHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetRepositoryStore wires the repo store used to validate repository_id
// belongs to the request org on Create/Update.
func (h *AutomationHandler) SetRepositoryStore(repoStore automationRepoLookup) {
	h.repoStore = repoStore
}

func (h *AutomationHandler) SetOrgStore(orgStore automationOrgLookup) {
	h.orgStore = orgStore
}

func (h *AutomationHandler) SetOrgCredentialStore(store automationOrgCredentialLookup) {
	h.orgCredentialStore = store
}

func (h *AutomationHandler) SetUserCredentialStore(store automationUserCredentialLookup) {
	h.userCredentialStore = store
}

func (h *AutomationHandler) SetCodingAuthStore(store automationCodingAuthLookup) {
	h.codingAuthStore = store
}

func (h *AutomationHandler) SetCodingCredentialStore(store automationCodingCredentialLookup) {
	h.codingCredentialStore = store
}

// SetPool wires the transaction starter used by RunNow to create the run row
// and enqueue the job atomically.
func (h *AutomationHandler) SetPool(pool db.TxStarter) {
	h.pool = pool
}

// SetLogger wires a logger used for non-fatal diagnostics that should reach
// stderr but not change the HTTP response (e.g. cron-fixup failures during
// bulk resume — the row was still resumed but its next_run_at could not be
// recomputed).
func (h *AutomationHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

func (h *AutomationHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	filters := db.AutomationFilters{
		Limit:  clampListLimit(queryInt(r, "limit", automationListDefaultLimit), automationListDefaultLimit, automationListMaxLimit),
		Cursor: r.URL.Query().Get("cursor"),
		Search: r.URL.Query().Get("search"),
	}
	if v := r.URL.Query().Get("enabled"); v == "true" || v == "false" {
		b := v == "true"
		filters.Enabled = &b
	}

	automations, err := h.automationStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list automations", err)
		return
	}
	if automations == nil {
		automations = []models.Automation{}
	}

	var nextCursor string
	if len(automations) > 0 && len(automations) == filters.Limit {
		nextCursor = automations[len(automations)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Automation]{
		Data: automations,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *AutomationHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	automation, err := h.automationStore.GetByID(r.Context(), orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		Name           string  `json:"name"`
		Goal           string  `json:"goal"`
		RepositoryID   string  `json:"repository_id"`
		Scope          *string `json:"scope"`
		AgentType      *string `json:"agent_type"`
		Model          *string `json:"model"`
		ExecutionMode  *string `json:"execution_mode"`
		MaxConcurrent  *int    `json:"max_concurrent"`
		BaseBranch     *string `json:"base_branch"`
		IdentityScope  *string `json:"identity_scope"`
		ScheduleType   *string `json:"schedule_type"`
		IntervalValue  *int    `json:"interval_value"`
		IntervalUnit   *string `json:"interval_unit"`
		IntervalRunAt  *string `json:"interval_run_at"`
		CronExpression *string `json:"cron_expression"`
		Timezone       *string `json:"timezone"`
		Priority       *int    `json:"priority"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	goal := strings.TrimSpace(req.Goal)
	if name == "" || goal == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name and goal are required")
		return
	}
	if err := validateAutomationNameAndGoal(name, goal); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", err.Error())
		return
	}

	repoID, err := h.resolveRepositoryID(r.Context(), orgID, req.RepositoryID)
	if err != nil {
		if errors.Is(err, errRepoDisconnected) {
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to create automations")
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", err.Error())
		return
	}

	agentType, modelOverride, err := resolveAutomationAgentAndModel(nil, nil, req.AgentType, req.Model)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "model"):
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		}
		return
	}
	if err := h.validateAutomationModelAvailability(r.Context(), orgID, agentType, modelOverride); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		return
	}

	scheduleType := models.AutomationScheduleInterval
	if req.ScheduleType != nil {
		scheduleType = *req.ScheduleType
		if err := models.ValidateAutomationScheduleType(scheduleType); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_TYPE", err.Error())
			return
		}
	}

	// Reject cross-typed schedule fields up front rather than silently dropping
	// them: a cron payload that also includes interval_value almost certainly
	// reflects a client bug, and silent normalisation would mask it.
	if scheduleType == models.AutomationScheduleCron && (req.IntervalValue != nil || req.IntervalUnit != nil || req.IntervalRunAt != nil) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "interval_value, interval_unit, and interval_run_at must not be set for cron schedules")
		return
	}
	if scheduleType == models.AutomationScheduleInterval && req.CronExpression != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "cron_expression must not be set for interval schedules")
		return
	}

	// Interval fields are only meaningful for interval schedules; cron schedules
	// persist them as NULL. The DB CHECK constraint
	// (chk_automations_schedule_fields) also enforces this XOR relationship.
	var intervalValuePtr *int
	var intervalUnitPtr *string
	var intervalRunAtPtr *string
	if scheduleType == models.AutomationScheduleInterval {
		intervalValue := 1
		intervalUnit := "days"
		if req.IntervalValue != nil {
			if *req.IntervalValue <= 0 || *req.IntervalValue > 365 {
				writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL", "interval_value must be between 1 and 365")
				return
			}
			intervalValue = *req.IntervalValue
		}
		if req.IntervalUnit != nil && *req.IntervalUnit != "" {
			if err := models.ScheduleUnit(*req.IntervalUnit).Validate(); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL_UNIT", err.Error())
				return
			}
			intervalUnit = *req.IntervalUnit
		}
		intervalValuePtr = &intervalValue
		intervalUnitPtr = &intervalUnit
		if req.IntervalRunAt != nil && strings.TrimSpace(*req.IntervalRunAt) != "" {
			runAt := strings.TrimSpace(*req.IntervalRunAt)
			if err := models.ValidateIntervalRunAt(runAt); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL_RUN_AT", err.Error())
				return
			}
			intervalRunAtPtr = &runAt
		}
	}

	var cronExpressionPtr *string
	if scheduleType == models.AutomationScheduleCron {
		if req.CronExpression == nil || strings.TrimSpace(*req.CronExpression) == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_CRON_EXPRESSION", "cron_expression is required for cron schedules")
			return
		}
		expr := strings.TrimSpace(*req.CronExpression)
		if err := models.ValidateCronExpression(expr); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CRON_EXPRESSION", err.Error())
			return
		}
		cronExpressionPtr = &expr
	}

	execMode := "sequential"
	if req.ExecutionMode != nil && *req.ExecutionMode != "" {
		if !validExecutionModes[*req.ExecutionMode] {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", "execution_mode must be sequential, parallel, or dependency_graph")
			return
		}
		execMode = *req.ExecutionMode
	}

	maxConcurrent := 1
	if req.MaxConcurrent != nil {
		if *req.MaxConcurrent <= 0 || *req.MaxConcurrent > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_MAX_CONCURRENT", "max_concurrent must be between 1 and 100")
			return
		}
		maxConcurrent = *req.MaxConcurrent
	}

	baseBranch := "main"
	if req.BaseBranch != nil && *req.BaseBranch != "" {
		if err := validateBaseBranch(*req.BaseBranch); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BASE_BRANCH", err.Error())
			return
		}
		baseBranch = *req.BaseBranch
	}

	identityScope := models.AutomationIdentityScopeOrg
	if req.IdentityScope != nil && *req.IdentityScope != "" {
		identityScope = models.AutomationIdentityScope(*req.IdentityScope)
		if err := identityScope.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", err.Error())
			return
		}
	}

	timezone := "UTC"
	if req.Timezone != nil && *req.Timezone != "" {
		if err := validateTimezone(*req.Timezone); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TIMEZONE", err.Error())
			return
		}
		timezone = *req.Timezone
	}

	priority := 50
	if req.Priority != nil {
		if *req.Priority < 0 || *req.Priority > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRIORITY", "priority must be between 0 and 100")
			return
		}
		priority = *req.Priority
	}

	automation := models.Automation{
		OrgID:          orgID,
		RepositoryID:   repoID,
		Name:           name,
		Goal:           goal,
		Scope:          req.Scope,
		AgentType:      agentType,
		ModelOverride:  modelOverride,
		ExecutionMode:  execMode,
		MaxConcurrent:  maxConcurrent,
		BaseBranch:     baseBranch,
		IdentityScope:  identityScope,
		ScheduleType:   scheduleType,
		IntervalValue:  intervalValuePtr,
		IntervalUnit:   intervalUnitPtr,
		IntervalRunAt:  intervalRunAtPtr,
		CronExpression: cronExpressionPtr,
		Timezone:       timezone,
		Enabled:        true,
		CreatedBy:      &user.ID,
		Priority:       priority,
	}

	// Centralise schedule branching in ComputeNextRunAt so a new schedule kind
	// (event-based, combined interval+cron, etc.) only has to be added there.
	now := time.Now()
	next, err := automation.ComputeNextRunAt(now)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
		return
	}
	automation.NextRunAt = &next

	if err := h.automationStore.Create(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create automation", err)
		return
	}

	idStr := automation.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationCreated, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, automationAuditSnapshot(&automation)))
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	automation, err := h.automationStore.GetByID(r.Context(), orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}
	// Snapshot before applying partial updates so the audit diff can report
	// exact before/after values. Copying the value is cheap (no pointer fields
	// are mutated in place below — the handler replaces pointers wholesale).
	before := automation

	var req struct {
		Name           *string `json:"name"`
		Goal           *string `json:"goal"`
		Scope          *string `json:"scope"`
		RepositoryID   *string `json:"repository_id"`
		AgentType      *string `json:"agent_type"`
		Model          *string `json:"model"`
		ExecutionMode  *string `json:"execution_mode"`
		MaxConcurrent  *int    `json:"max_concurrent"`
		BaseBranch     *string `json:"base_branch"`
		IdentityScope  *string `json:"identity_scope"`
		ScheduleType   *string `json:"schedule_type"`
		IntervalValue  *int    `json:"interval_value"`
		IntervalUnit   *string `json:"interval_unit"`
		IntervalRunAt  *string `json:"interval_run_at"`
		CronExpression *string `json:"cron_expression"`
		Timezone       *string `json:"timezone"`
		Priority       *int    `json:"priority"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name must not be empty")
			return
		}
		if len(trimmed) > automationNameMaxLength {
			writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", fmt.Sprintf("name must be at most %d characters", automationNameMaxLength))
			return
		}
		automation.Name = trimmed
	}
	if req.Goal != nil {
		trimmed := strings.TrimSpace(*req.Goal)
		if trimmed == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "goal must not be empty")
			return
		}
		if len(trimmed) > automationGoalMaxLength {
			writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", fmt.Sprintf("goal must be at most %d characters", automationGoalMaxLength))
			return
		}
		automation.Goal = trimmed
	}
	if req.Scope != nil {
		automation.Scope = req.Scope
	}
	if req.RepositoryID != nil {
		repoID, err := h.resolveRepositoryID(r.Context(), orgID, *req.RepositoryID)
		if err != nil {
			if errors.Is(err, errRepoDisconnected) {
				writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to assign automations")
				return
			}
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", err.Error())
			return
		}
		automation.RepositoryID = repoID
	}

	if req.AgentType != nil || req.Model != nil {
		agentType, modelOverride, err := resolveAutomationAgentAndModel(automation.AgentType, automation.ModelOverride, req.AgentType, req.Model)
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "model"):
				writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			default:
				writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
			}
			return
		}
		automation.AgentType = agentType
		automation.ModelOverride = modelOverride
		if err := h.validateAutomationModelAvailability(r.Context(), orgID, automation.AgentType, automation.ModelOverride); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			return
		}
	}
	if req.ExecutionMode != nil {
		if !validExecutionModes[*req.ExecutionMode] {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", "execution_mode must be sequential, parallel, or dependency_graph")
			return
		}
		automation.ExecutionMode = *req.ExecutionMode
	}
	if req.IdentityScope != nil {
		identityScope := models.AutomationIdentityScope(*req.IdentityScope)
		if err := identityScope.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", err.Error())
			return
		}
		if identityScope.OrDefault() == models.AutomationIdentityScopePersonal && automation.CreatedBy == nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", "identity_scope=personal requires automation.created_by")
			return
		}
		automation.IdentityScope = identityScope.OrDefault()
	}
	if req.MaxConcurrent != nil {
		if *req.MaxConcurrent <= 0 || *req.MaxConcurrent > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_MAX_CONCURRENT", "max_concurrent must be between 1 and 100")
			return
		}
		automation.MaxConcurrent = *req.MaxConcurrent
	}
	if req.BaseBranch != nil {
		if err := validateBaseBranch(*req.BaseBranch); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BASE_BRANCH", err.Error())
			return
		}
		automation.BaseBranch = *req.BaseBranch
	}
	if req.Timezone != nil {
		if err := validateTimezone(*req.Timezone); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TIMEZONE", err.Error())
			return
		}
		automation.Timezone = *req.Timezone
	}
	if req.Priority != nil {
		if *req.Priority < 0 || *req.Priority > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRIORITY", "priority must be between 0 and 100")
			return
		}
		automation.Priority = *req.Priority
	}

	// Handle schedule changes — recompute next_run_at.
	scheduleChanged := false

	// Determine the effective schedule type for this PATCH so we can reject
	// cross-typed companion fields before mutating the model. The previous
	// behaviour silently dropped mismatched fields (e.g. cron_expression sent
	// against an interval automation) which masked client bugs.
	effectiveScheduleType := automation.ScheduleType
	if req.ScheduleType != nil {
		if err := models.ValidateAutomationScheduleType(*req.ScheduleType); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_TYPE", err.Error())
			return
		}
		effectiveScheduleType = *req.ScheduleType
	}
	if effectiveScheduleType == models.AutomationScheduleCron && (req.IntervalValue != nil || req.IntervalUnit != nil || req.IntervalRunAt != nil) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "interval_value, interval_unit, and interval_run_at must not be set for cron schedules")
		return
	}
	if effectiveScheduleType == models.AutomationScheduleInterval && req.CronExpression != nil && strings.TrimSpace(*req.CronExpression) != "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "cron_expression must not be set for interval schedules")
		return
	}

	if req.ScheduleType != nil {
		// A schedule_type *switch* must carry the new type's companion fields
		// in the same PATCH. Without this, we'd surface the error downstream
		// at ComputeNextRunAt with a less precise message.
		if *req.ScheduleType != automation.ScheduleType {
			switch *req.ScheduleType {
			case models.AutomationScheduleInterval:
				if req.IntervalValue == nil {
					writeError(w, r, http.StatusBadRequest, "MISSING_INTERVAL_VALUE", "switching to schedule_type=interval requires interval_value")
					return
				}
				if req.IntervalUnit == nil || *req.IntervalUnit == "" {
					writeError(w, r, http.StatusBadRequest, "MISSING_INTERVAL_UNIT", "switching to schedule_type=interval requires interval_unit")
					return
				}
				// Switching away from cron: clear the cron field so the row
				// satisfies chk_automations_schedule_fields.
				automation.CronExpression = nil
			case models.AutomationScheduleCron:
				if req.CronExpression == nil || strings.TrimSpace(*req.CronExpression) == "" {
					writeError(w, r, http.StatusBadRequest, "MISSING_CRON_EXPRESSION", "switching to schedule_type=cron requires cron_expression")
					return
				}
				// Switching away from interval: clear interval fields.
				automation.IntervalValue = nil
				automation.IntervalUnit = nil
				automation.IntervalRunAt = nil
			}
		}
		automation.ScheduleType = *req.ScheduleType
		scheduleChanged = true
	}
	if req.IntervalValue != nil {
		if *req.IntervalValue <= 0 || *req.IntervalValue > 365 {
			writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL", "interval_value must be between 1 and 365")
			return
		}
		automation.IntervalValue = req.IntervalValue
		scheduleChanged = true
	}
	if req.IntervalUnit != nil {
		if err := models.ScheduleUnit(*req.IntervalUnit).Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL_UNIT", err.Error())
			return
		}
		automation.IntervalUnit = req.IntervalUnit
		scheduleChanged = true
	}
	if req.IntervalRunAt != nil {
		trimmed := strings.TrimSpace(*req.IntervalRunAt)
		if trimmed == "" {
			automation.IntervalRunAt = nil
		} else {
			if err := models.ValidateIntervalRunAt(trimmed); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL_RUN_AT", err.Error())
				return
			}
			automation.IntervalRunAt = &trimmed
		}
		scheduleChanged = true
	}
	if req.CronExpression != nil {
		trimmed := strings.TrimSpace(*req.CronExpression)
		// Clearing cron_expression on a cron automation (without switching
		// schedule_type in the same PATCH) would leave the row unschedulable.
		// ComputeNextRunAt would eventually reject it, but with a generic
		// INVALID_SCHEDULE message — surface the precise reason here instead.
		if trimmed == "" && effectiveScheduleType == models.AutomationScheduleCron {
			writeError(w, r, http.StatusBadRequest, "MISSING_CRON_EXPRESSION", "cron_expression cannot be cleared on a cron automation; switch schedule_type=interval in the same request to change schedule types")
			return
		}
		if trimmed != "" {
			if err := models.ValidateCronExpression(trimmed); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CRON_EXPRESSION", err.Error())
				return
			}
		}
		if trimmed == "" {
			automation.CronExpression = nil
		} else {
			automation.CronExpression = &trimmed
		}
		scheduleChanged = true
	}

	// A timezone change alone should recompute next_run_at for schedules
	// that evaluate wall-clock targets — cron expressions, and interval
	// rows with interval_run_at. For an interval row without interval_run_at
	// the recompute is a no-op (NextRunTime ignores timezone) but triggering
	// it is cheap and keeps the control flow uniform.
	if req.Timezone != nil {
		scheduleChanged = true
	}

	if scheduleChanged && automation.Enabled {
		next, err := automation.ComputeNextRunAt(time.Now())
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
			return
		}
		automation.NextRunAt = &next
	}

	if err := h.automationStore.Update(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update automation", err)
		return
	}

	// Skip audit emit when no user-editable field actually changed — otherwise
	// a no-op PATCH (e.g. a client re-sending the current values) would pollute
	// the timeline with empty "updated" rows.
	if changes := automationAuditDiff(&before, &automation); len(changes) > 0 {
		idStr := automationID.String()
		details := map[string]any{
			"name":    automation.Name,
			"changes": changes,
		}
		emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationUpdated, models.AuditResourceAutomation, &idStr, nil, nil,
			marshalAuditDetails(h.logger, details))
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	// Fetch before delete so the audit entry can record the name and schedule
	// context. The row is gone once SoftDelete returns, and chasing it through
	// a tombstone query later is worse than one extra SELECT.
	automation, err := h.automationStore.GetByID(r.Context(), orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	if err := h.automationStore.SoftDelete(r.Context(), orgID, automationID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationDeleted, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, automationAuditSnapshot(&automation)))
	w.WriteHeader(http.StatusNoContent)
}

func (h *AutomationHandler) Pause(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	automation, err := h.automationStore.GetByID(r.Context(), orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	if !automation.Enabled {
		writeError(w, r, http.StatusBadRequest, "ALREADY_PAUSED", "automation is already paused")
		return
	}

	now := time.Now()
	automation.Enabled = false
	automation.PausedBy = &user.ID
	automation.PausedAt = &now
	automation.NextRunAt = nil

	if err := h.automationStore.Update(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to pause automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationPaused, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, automationAuditSnapshot(&automation)))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) Resume(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	automation, err := h.automationStore.GetByID(r.Context(), orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	if automation.Enabled {
		writeError(w, r, http.StatusBadRequest, "ALREADY_ENABLED", "automation is already enabled")
		return
	}

	automation.Enabled = true
	automation.PausedBy = nil
	automation.PausedAt = nil

	// Centralised schedule branching — see ComputeNextRunAt. If the stored
	// schedule fields are malformed (e.g. a cron row with a missing expression
	// from a legacy import), surface the error instead of silently leaving
	// next_run_at stale so the scheduler never fires it.
	next, err := automation.ComputeNextRunAt(time.Now())
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
		return
	}
	automation.NextRunAt = &next

	if err := h.automationStore.Update(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to resume automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationResumed, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, automationAuditSnapshot(&automation)))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: automation})
}

// RunNow creates a manual automation run and enqueues the job atomically so a
// failed enqueue cannot leave an orphaned pending run row behind.
//
// Manual runs leave scheduled_time NULL, so the unique idempotency index
// (which is partial: WHERE scheduled_time IS NOT NULL) does NOT dedupe them.
// We enforce throttling here via CountInFlightRuns inside the same tx: a
// user who double-clicks "Run now" should not spawn N parallel jobs that
// collectively blow past max_concurrent.
func (h *AutomationHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	if h.jobStore == nil || h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_CONFIGURED", "job store or pool not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_BEGIN_FAILED", "failed to begin transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	automation, err := h.automationStore.LockByIDForUpdate(r.Context(), tx, orgID, automationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	// Refuse manual runs on paused automations. A paused automation is one the
	// user has explicitly disabled; letting Run-now bypass that would make the
	// pause toggle misleading (and could fire runs the user no longer wants).
	if !automation.Enabled {
		writeError(w, r, http.StatusConflict, "AUTOMATION_PAUSED", "automation is paused; resume it before running")
		return
	}

	configSnapshot, err := automation.BuildConfigSnapshot()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONFIG_SNAPSHOT_FAILED", "failed to build config snapshot", err)
		return
	}

	// Throttle against max_concurrent inside the tx so a rapid double-click
	// serializes on the automation row before checking capacity. CountInFlightRuns
	// counts pending + running, matching the scheduler's throttle semantics.
	inFlight, err := h.automationStore.CountInFlightRuns(r.Context(), tx, orgID, automation.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COUNT_FAILED", "failed to count in-flight runs", err)
		return
	}
	if inFlight >= automation.MaxConcurrent {
		writeError(w, r, http.StatusConflict, "DUPLICATE_RUN", "a run is already in progress")
		return
	}

	run := models.AutomationRun{
		AutomationID:      automation.ID,
		OrgID:             automation.OrgID,
		TriggeredBy:       models.AutomationTriggeredByManual,
		TriggeredByUserID: &user.ID,
		GoalSnapshot:      automation.Goal,
		ConfigSnapshot:    configSnapshot,
		Status:            models.AutomationRunStatusPending,
	}

	created, err := h.automationRunStore.CreateRunInTx(r.Context(), tx, &run)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_RUN_FAILED", "failed to create automation run", err)
		return
	}
	if !created {
		// Unreachable for manual runs (scheduled_time is NULL, so the partial
		// unique index never fires). Kept for defense-in-depth if a future
		// change widens the idempotency key.
		writeError(w, r, http.StatusConflict, "DUPLICATE_RUN", "a run is already in progress")
		return
	}

	dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
	payload := map[string]string{
		"org_id":            orgID.String(),
		"automation_id":     automation.ID.String(),
		"automation_run_id": run.ID.String(),
	}
	jobID, err := h.jobStore.EnqueueInTx(r.Context(), tx, orgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue automation run job", err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit transaction", err)
		return
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	h.jobStore.Notify(notifyCtx, jobID)

	idStr := automationID.String()
	details := map[string]any{
		"name":              automation.Name,
		"automation_run_id": run.ID.String(),
		"via":               "manual",
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationRunTriggered, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, details))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationRun]{Data: run})
}

// BulkCronFixupFailure is the wire representation of a cron row that was
// resumed but whose next_run_at could not be recomputed. Surfacing these to the
// client lets the UI explain why a "resumed" automation isn't firing instead
// of the user having to scrape server logs.
type BulkCronFixupFailure struct {
	AutomationID string `json:"automation_id"`
	Reason       string `json:"reason"`
}

// BulkResponse is the JSON body returned by the bulk endpoint. `affected` is
// the set of automation IDs that actually changed (filtered by the store to
// exclude cross-tenant / already-deleted IDs); `fixup_failures` is non-empty
// only for resume with cron rows whose cron_expression no longer parses.
type BulkResponse struct {
	Affected      []string               `json:"affected"`
	FixupFailures []BulkCronFixupFailure `json:"fixup_failures"`
}

// Bulk handles bulk pause/resume/delete operations.
func (h *AutomationHandler) Bulk(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		Action        string      `json:"action"`
		AutomationIDs []uuid.UUID `json:"automation_ids"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if len(req.AutomationIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_IDS", "automation_ids must not be empty")
		return
	}

	var auditAction models.AuditAction
	var affected []uuid.UUID
	var fixupFailures []db.CronFixupFailure
	switch req.Action {
	case "pause":
		ids, _, err := h.automationStore.BulkUpdateEnabled(r.Context(), orgID, req.AutomationIDs, false, &user.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "BULK_FAILED", "failed to pause automations", err)
			return
		}
		affected = ids
		auditAction = models.AuditActionAutomationPaused
	case "resume":
		ids, failures, err := h.automationStore.BulkUpdateEnabled(r.Context(), orgID, req.AutomationIDs, true, &user.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "BULK_FAILED", "failed to resume automations", err)
			return
		}
		// Cron-fixup failures: the row was resumed but its next_run_at is
		// NULL, so the scheduler will skip it. Log per-automation so an
		// operator chasing "why isn't this firing?" can grep by ID.
		for _, f := range failures {
			h.logger.Warn().
				Str("automation_id", f.AutomationID.String()).
				Str("org_id", orgID.String()).
				Str("reason", f.Reason).
				Msg("cron automation resumed without a computable next_run_at; scheduler will skip it until cron_expression is fixed")
		}
		affected = ids
		fixupFailures = failures
		auditAction = models.AuditActionAutomationResumed
	case "delete":
		ids, err := h.automationStore.BulkSoftDelete(r.Context(), orgID, req.AutomationIDs)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "BULK_FAILED", "failed to delete automations", err)
			return
		}
		affected = ids
		auditAction = models.AuditActionAutomationDeleted
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_ACTION", "action must be pause, resume, or delete")
		return
	}

	// Emit one audit event per actually-affected automation. IDs from other
	// tenants or stale/deleted rows are filtered out at the store layer and
	// must not pollute the audit log (cross-tenant probing would otherwise
	// leave ghost events behind).
	//
	// Tag each entry with via=bulk so the viewer can collapse related rows
	// that share the same request_id. A per-automation name lookup would cost
	// an extra round-trip per ID; the resource_id is enough to let the UI
	// hydrate on demand.
	//
	// Cron-fixup rows (resume path only) get an additional
	// fixup_failure_reason so an auditor reading "automation.resumed" isn't
	// misled: the row was flipped enabled but the scheduler will skip it
	// until the cron_expression is fixed. Without this, a broken cron row
	// looks identical in the audit log to a healthy resumed row.
	fixupByID := make(map[uuid.UUID]string, len(fixupFailures))
	for _, f := range fixupFailures {
		fixupByID[f.AutomationID] = f.Reason
	}
	bulkSize := len(affected)
	sharedBulkDetails := marshalAuditDetails(h.logger, map[string]any{
		"via":       "bulk",
		"bulk_size": bulkSize,
	})
	for _, id := range affected {
		idStr := id.String()
		details := sharedBulkDetails
		if reason, ok := fixupByID[id]; ok {
			details = marshalAuditDetails(h.logger, map[string]any{
				"via":                  "bulk",
				"bulk_size":            bulkSize,
				"fixup_failure_reason": reason,
				"scheduler_will_fire":  false,
			})
		}
		emitUserAuditWithSession(h.audit, r, auditAction, models.AuditResourceAutomation, &idStr, nil, nil, details)
	}

	resp := BulkResponse{
		Affected:      make([]string, 0, len(affected)),
		FixupFailures: make([]BulkCronFixupFailure, 0, len(fixupFailures)),
	}
	for _, id := range affected {
		resp.Affected = append(resp.Affected, id.String())
	}
	for _, f := range fixupFailures {
		resp.FixupFailures = append(resp.FixupFailures, BulkCronFixupFailure{
			AutomationID: f.AutomationID.String(),
			Reason:       f.Reason,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListRuns returns paginated runs for an automation.
func (h *AutomationHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	// Verify the automation belongs to this org.
	if _, err := h.automationStore.GetByID(r.Context(), orgID, automationID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	filters := db.AutomationRunFilters{
		Limit:  clampListLimit(queryInt(r, "limit", automationListDefaultLimit), automationListDefaultLimit, automationListMaxLimit),
		Cursor: r.URL.Query().Get("cursor"),
	}

	runs, err := h.automationRunStore.ListByAutomation(r.Context(), orgID, automationID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list runs", err)
		return
	}
	if runs == nil {
		runs = []models.AutomationRun{}
	}

	var nextCursor string
	if len(runs) > 0 && len(runs) == filters.Limit {
		nextCursor = runs[len(runs)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.AutomationRun]{
		Data: runs,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Stats returns per-day run aggregates for an automation.
//
// Query params:
//   - since: RFC3339 lower bound (inclusive). Defaults to until -
//     automationStatsDefaultWindow.
//   - until: RFC3339 upper bound (exclusive). Defaults to now.
//
// The window is capped at automationStatsMaxWindow so a malformed since=
// can't trigger a table scan — the automation_runs index on (org_id,
// automation_id, triggered_at DESC) still handles everything inside that
// window cheaply.
func (h *AutomationHandler) Stats(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	// Validate query params before hitting the DB — a malformed since/until
	// should return 400 without burning an automations lookup.
	//
	// Quantize the default `until` to the start of the current minute so two
	// callers who both omit ?until in the same wall-clock minute compute the
	// same window. Frontends that pass an explicit RFC3339 ?until keep their
	// own value, so this only matters when the caller is relying on server-
	// side defaults.
	now := time.Now().UTC().Truncate(time.Minute)
	until := now
	if v := r.URL.Query().Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_UNTIL", "until must be RFC3339")
			return
		}
		until = t.UTC()
	}
	since := until.Add(-automationStatsDefaultWindow)
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SINCE", "since must be RFC3339")
			return
		}
		since = t.UTC()
	}
	if !since.Before(until) {
		writeError(w, r, http.StatusBadRequest, "INVALID_WINDOW", "since must be before until")
		return
	}
	if until.Sub(since) > automationStatsMaxWindow {
		writeError(w, r, http.StatusBadRequest, "WINDOW_TOO_LARGE", fmt.Sprintf("window must be <= %d days", int(automationStatsMaxWindow/(24*time.Hour))))
		return
	}

	// Verify the automation belongs to this org. Without this, a leaked
	// automation UUID from another tenant could be probed via the stats
	// endpoint (the aggregate query does filter by org_id, but a 404 here
	// is cheaper than an empty bucket set and removes the ambiguity).
	if _, err := h.automationStore.GetByID(r.Context(), orgID, automationID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found")
		return
	}

	stats, err := h.automationRunStore.GetStats(r.Context(), orgID, automationID, since, until)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STATS_FAILED", "failed to compute stats", err)
		return
	}
	if stats.Buckets == nil {
		stats.Buckets = []models.AutomationRunStatsBucket{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationRunStats]{Data: stats})
}

// GetRun returns a single run detail.
func (h *AutomationHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "rid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// GetByID enforces (org_id, automation_id, run_id) — a leaked run UUID
	// from another tenant is not readable.
	run, err := h.automationRunStore.GetByID(r.Context(), orgID, automationID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationRun]{Data: run})
}
