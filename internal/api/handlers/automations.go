package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// automationListDefaultLimit and automationListMaxLimit must match the
// store's internal clamp in AutomationStore.ListByOrg/ListByAutomation so
// that the limit we pass in equals the bound actually applied — otherwise
// the next_cursor check `len(results) == filters.Limit` stops pagination
// one page early when a caller asks for more than the store will return.
const (
	automationListDefaultLimit = 25
	automationListMaxLimit     = 100
)

type AutomationHandler struct {
	automationStore    *db.AutomationStore
	automationRunStore *db.AutomationRunStore
	repoStore          automationRepoLookup
	jobStore           *db.JobStore
	audit              *db.AuditEmitter
	pool               db.TxStarter // needed for transactional RunNow
}

// automationRepoLookup is the slice of *db.RepositoryStore needed to verify
// that a repository_id supplied by the client belongs to the caller's org.
type automationRepoLookup interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

func NewAutomationHandler(automationStore *db.AutomationStore, automationRunStore *db.AutomationRunStore) *AutomationHandler {
	return &AutomationHandler{
		automationStore:    automationStore,
		automationRunStore: automationRunStore,
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

// SetPool wires the transaction starter used by RunNow to create the run row
// and enqueue the job atomically.
func (h *AutomationHandler) SetPool(pool db.TxStarter) {
	h.pool = pool
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
		ScheduleType   *string `json:"schedule_type"`
		IntervalValue  *int    `json:"interval_value"`
		IntervalUnit   *string `json:"interval_unit"`
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
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", err.Error())
		return
	}

	scheduleType := models.AutomationScheduleInterval
	if req.ScheduleType != nil {
		scheduleType = *req.ScheduleType
		// ValidateAutomationScheduleSupported enforces both the allowed set
		// AND the phase-2 cron gate (see models.AutomationCronSupported).
		// Keeping both checks in one call means a future flip of the cron
		// flag updates Create, Update, and any future caller in lockstep.
		if err := models.ValidateAutomationScheduleSupported(scheduleType); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_TYPE", err.Error())
			return
		}
	}

	// Default interval: 1 day.
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

	timezone := "UTC"
	if req.Timezone != nil && *req.Timezone != "" {
		if err := validateTimezone(*req.Timezone); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TIMEZONE", err.Error())
			return
		}
		timezone = *req.Timezone
	}
	// Interval schedules ignore timezone, so rejecting non-UTC here matches
	// the DB CHECK and keeps the API honest about what the value does.
	if scheduleType == models.AutomationScheduleInterval && timezone != "UTC" {
		writeError(w, r, http.StatusBadRequest, "INVALID_TIMEZONE", "timezone must be UTC for interval schedules")
		return
	}

	priority := 50
	if req.Priority != nil {
		if *req.Priority < 0 || *req.Priority > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRIORITY", "priority must be between 0 and 100")
			return
		}
		priority = *req.Priority
	}

	// Compute next_run_at. Only interval is supported here; cron was rejected
	// above, so a missing next_run_at would be a code bug.
	now := time.Now()
	next := models.NextRunTime(now, intervalValue, intervalUnit)
	nextRunAt := &next

	automation := models.Automation{
		OrgID:          orgID,
		RepositoryID:   repoID,
		Name:           name,
		Goal:           goal,
		Scope:          req.Scope,
		AgentType:      req.AgentType,
		ModelOverride:  req.Model,
		ExecutionMode:  execMode,
		MaxConcurrent:  maxConcurrent,
		BaseBranch:     baseBranch,
		ScheduleType:   scheduleType,
		IntervalValue:  &intervalValue,
		IntervalUnit:   &intervalUnit,
		CronExpression: req.CronExpression,
		Timezone:       timezone,
		NextRunAt:      nextRunAt,
		Enabled:        true,
		CreatedBy:      &user.ID,
		Priority:       priority,
	}

	if err := h.automationStore.Create(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create automation", err)
		return
	}

	idStr := automation.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationCreated, models.AuditResourceAutomation, &idStr, nil, nil, nil)
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
		ScheduleType   *string `json:"schedule_type"`
		IntervalValue  *int    `json:"interval_value"`
		IntervalUnit   *string `json:"interval_unit"`
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
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", err.Error())
			return
		}
		automation.RepositoryID = repoID
	}
	if req.AgentType != nil {
		automation.AgentType = req.AgentType
	}
	if req.Model != nil {
		automation.ModelOverride = req.Model
	}
	if req.ExecutionMode != nil {
		if !validExecutionModes[*req.ExecutionMode] {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", "execution_mode must be sequential, parallel, or dependency_graph")
			return
		}
		automation.ExecutionMode = *req.ExecutionMode
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
	if req.ScheduleType != nil {
		// Shared gate with Create: rejects both invalid schedule types and
		// schedule types the current build does not implement (cron today).
		if err := models.ValidateAutomationScheduleSupported(*req.ScheduleType); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_TYPE", err.Error())
			return
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
	if req.CronExpression != nil {
		automation.CronExpression = req.CronExpression
		scheduleChanged = true
	}
	if scheduleChanged && automation.Enabled {
		now := time.Now()
		if automation.ScheduleType == models.AutomationScheduleInterval && automation.IntervalValue != nil && automation.IntervalUnit != nil {
			next := models.NextRunTime(now, *automation.IntervalValue, *automation.IntervalUnit)
			automation.NextRunAt = &next
		}
		// TODO(phase-3): when cron lands (design doc 48 §6.2), also recompute
		// next_run_at from automation.CronExpression here. Today cron is
		// rejected earlier in this handler, so a cron schedule_change cannot
		// reach this block — but a pure CronExpression edit on an already-cron
		// automation would silently leave next_run_at stale.
	}

	// Interval schedules ignore timezone. Enforce after all partial updates so
	// a change to either field alone is still caught (matches the DB CHECK).
	if automation.ScheduleType == models.AutomationScheduleInterval && automation.Timezone != "UTC" {
		writeError(w, r, http.StatusBadRequest, "INVALID_TIMEZONE", "timezone must be UTC for interval schedules")
		return
	}

	if err := h.automationStore.Update(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationUpdated, models.AuditResourceAutomation, &idStr, nil, nil, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}

	if err := h.automationStore.SoftDelete(r.Context(), orgID, automationID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationDeleted, models.AuditResourceAutomation, &idStr, nil, nil, nil)
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
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationPaused, models.AuditResourceAutomation, &idStr, nil, nil, nil)
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

	// Defense-in-depth: reject resume for schedule types the build doesn't yet
	// implement. Create/Update already block cron via the same gate, so a cron
	// row shouldn't exist today — but if one slipped in (legacy row, manual DB
	// edit, future migration) we'd resume it with NextRunAt=NULL and the
	// scheduler would silently never fire it. Fail loudly instead.
	if err := models.ValidateAutomationScheduleSupported(automation.ScheduleType); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE_TYPE", err.Error())
		return
	}

	automation.Enabled = true
	automation.PausedBy = nil
	automation.PausedAt = nil

	// Recompute next run time.
	now := time.Now()
	if automation.ScheduleType == models.AutomationScheduleInterval && automation.IntervalValue != nil && automation.IntervalUnit != nil {
		next := models.NextRunTime(now, *automation.IntervalValue, *automation.IntervalUnit)
		automation.NextRunAt = &next
	}
	// TODO(phase-3): when cron lands, also recompute next_run_at from
	// automation.CronExpression here. The gate above currently prevents a cron
	// row from reaching this point; remove the gate in the same PR that wires
	// the cron next_run_at computation.

	if err := h.automationStore.Update(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to resume automation", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationResumed, models.AuditResourceAutomation, &idStr, nil, nil, nil)
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
	if _, err := h.jobStore.EnqueueInTx(r.Context(), tx, orgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue automation run job", err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit transaction", err)
		return
	}

	idStr := automationID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationRunTriggered, models.AuditResourceAutomation, &idStr, nil, nil, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationRun]{Data: run})
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
	switch req.Action {
	case "pause":
		ids, err := h.automationStore.BulkUpdateEnabled(r.Context(), orgID, req.AutomationIDs, false, &user.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "BULK_FAILED", "failed to pause automations", err)
			return
		}
		affected = ids
		auditAction = models.AuditActionAutomationPaused
	case "resume":
		ids, err := h.automationStore.BulkUpdateEnabled(r.Context(), orgID, req.AutomationIDs, true, &user.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "BULK_FAILED", "failed to resume automations", err)
			return
		}
		affected = ids
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
	for _, id := range affected {
		idStr := id.String()
		emitUserAuditWithSession(h.audit, r, auditAction, models.AuditResourceAutomation, &idStr, nil, nil, nil)
	}

	w.WriteHeader(http.StatusNoContent)
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
