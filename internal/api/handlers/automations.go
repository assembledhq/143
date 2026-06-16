package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	automationStore       *db.AutomationStore
	automationRunStore    *db.AutomationRunStore
	repoStore             automationRepoLookup
	orgStore              automationOrgLookup
	codingCredentialStore automationCodingCredentialLookup
	jobStore              *db.JobStore
	audit                 *db.AuditEmitter
	pool                  db.TxStarter // needed for transactional RunNow
	logger                zerolog.Logger
}

// automationRepoLookup is the slice of *db.RepositoryStore needed to verify
// that a repository_id supplied by the client belongs to the caller's org.
type automationRepoLookup interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type automationOrgLookup interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
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
	if raw := r.URL.Query().Get("created_after"); raw != "" {
		parsed, ok := parseOptionalRFC3339(w, r, &raw)
		if !ok {
			return
		}
		filters.CreatedAfter = parsed
	}
	if raw := r.URL.Query().Get("created_before"); raw != "" {
		parsed, ok := parseOptionalRFC3339(w, r, &raw)
		if !ok {
			return
		}
		filters.CreatedBefore = parsed
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
		Name                string                            `json:"name"`
		Goal                string                            `json:"goal"`
		IconType            models.AutomationIconType         `json:"icon_type"`
		IconValue           string                            `json:"icon_value"`
		RepositoryID        string                            `json:"repository_id"`
		Scope               *string                           `json:"scope"`
		AgentType           *models.AgentType                 `json:"agent_type"`
		Model               *string                           `json:"model"`
		ReasoningEffort     models.ReasoningEffort            `json:"reasoning_effort"`
		ExecutionMode       *models.ProjectExecMode           `json:"execution_mode"`
		MaxConcurrent       *int                              `json:"max_concurrent"`
		BaseBranch          *string                           `json:"base_branch"`
		IdentityScope       *models.AutomationIdentityScope   `json:"identity_scope"`
		ScheduleType        *models.AutomationScheduleType    `json:"schedule_type"`
		IntervalValue       *int                              `json:"interval_value"`
		IntervalUnit        *models.ScheduleUnit              `json:"interval_unit"`
		IntervalRunAt       *string                           `json:"interval_run_at"`
		CronExpression      *string                           `json:"cron_expression"`
		Timezone            *string                           `json:"timezone"`
		ProductTriggers     []models.AutomationProductTrigger `json:"triggers"`
		GitHubEventTriggers []models.AutomationGitHubEvent    `json:"github_event_triggers"`
		GitHubEventFilters  json.RawMessage                   `json:"github_event_filters"`
		Metadata            json.RawMessage                   `json:"metadata"`
		Priority            *int                              `json:"priority"`
		PrePRReviewLoops    *int                              `json:"pre_pr_review_loops"`
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
	iconType, iconValue, err := resolveAutomationIcon(req.IconType, req.IconValue)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ICON", err.Error())
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
	if token := middleware.APITokenFromContext(r.Context()); token != nil && repoID != nil && !apiTokenAllowsRepository(token, *repoID) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "API token is not allowed to access this repository")
		return
	}

	var reqAgentType *string
	if req.AgentType != nil {
		agentTypeString := string(*req.AgentType)
		reqAgentType = &agentTypeString
	}
	agentType, modelOverride, err := resolveAutomationAgentAndModel(nil, nil, reqAgentType, req.Model)
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
	effectiveAgentType, err := h.defaultAutomationAgentType(r.Context(), orgID, agentType)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
		return
	}
	reasoningOverride, err := parseReasoningEffortForAgent(effectiveAgentType, string(req.ReasoningEffort))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REASONING_EFFORT", err.Error())
		return
	}

	scheduleType := models.AutomationScheduleInterval
	if req.ScheduleType != nil {
		scheduleType = *req.ScheduleType
		if err := scheduleType.Validate(); err != nil {
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
	if scheduleType == models.AutomationScheduleNone && (req.IntervalValue != nil || req.IntervalUnit != nil || req.IntervalRunAt != nil || req.CronExpression != nil) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "schedule fields must not be set when schedule_type is none")
		return
	}

	// Interval fields are only meaningful for interval schedules; cron schedules
	// persist them as NULL. The DB CHECK constraint
	// (chk_automations_schedule_fields) also enforces this XOR relationship.
	var intervalValuePtr *int
	var intervalUnitPtr *models.ScheduleUnit
	var intervalRunAtPtr *string
	if scheduleType == models.AutomationScheduleInterval {
		intervalValue := 1
		intervalUnit := models.ScheduleUnitDays
		if req.IntervalValue != nil {
			if *req.IntervalValue <= 0 || *req.IntervalValue > 365 {
				writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL", "interval_value must be between 1 and 365")
				return
			}
			intervalValue = *req.IntervalValue
		}
		if req.IntervalUnit != nil && *req.IntervalUnit != "" {
			if err := req.IntervalUnit.Validate(); err != nil {
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

	execMode := models.AutomationExecutionModeSequential
	if req.ExecutionMode != nil && *req.ExecutionMode != "" {
		if err := req.ExecutionMode.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", err.Error())
			return
		}
		execMode = models.AutomationExecutionMode(*req.ExecutionMode)
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
	prePRReviewLoops := 0
	if models.AgentSupportsNativeReview(effectiveAgentType) {
		prePRReviewLoops = 1
	}
	if req.PrePRReviewLoops != nil {
		if *req.PrePRReviewLoops < 0 || *req.PrePRReviewLoops > 5 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRE_PR_REVIEW_LOOPS", "pre_pr_review_loops must be between 0 and 5")
			return
		}
		prePRReviewLoops = *req.PrePRReviewLoops
	}
	if err := validatePrePRReviewLoopsForAgent(prePRReviewLoops, effectiveAgentType); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PRE_PR_REVIEW_LOOPS", err.Error())
		return
	}
	githubEventTriggers, err := resolveAutomationGitHubEventTriggers(req.ProductTriggers, req.GitHubEventTriggers)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_GITHUB_EVENT_TRIGGERS", err.Error())
		return
	}
	githubEventFilters, err := validateAutomationGitHubEventFilters(req.GitHubEventFilters)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_GITHUB_EVENT_FILTERS", err.Error())
		return
	}
	if scheduleType == models.AutomationScheduleNone && len(githubEventTriggers) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_TRIGGER", "event-only automations require at least one GitHub event trigger")
		return
	}

	automation := models.Automation{
		OrgID:               orgID,
		RepositoryID:        repoID,
		Name:                name,
		Goal:                goal,
		IconType:            iconType,
		IconValue:           iconValue,
		Scope:               req.Scope,
		AgentType:           agentType,
		ModelOverride:       modelOverride,
		ReasoningEffort:     reasoningOverride,
		ExecutionMode:       execMode,
		MaxConcurrent:       maxConcurrent,
		BaseBranch:          baseBranch,
		IdentityScope:       identityScope,
		PrePRReviewLoops:    prePRReviewLoops,
		ScheduleType:        scheduleType,
		IntervalValue:       intervalValuePtr,
		IntervalUnit:        intervalUnitPtr,
		IntervalRunAt:       intervalRunAtPtr,
		CronExpression:      cronExpressionPtr,
		Timezone:            timezone,
		GitHubEventTriggers: githubEventTriggers,
		GitHubEventFilters:  githubEventFilters,
		Enabled:             true,
		Priority:            priority,
		ExternalMetadata:    req.Metadata,
	}
	if user != nil {
		automation.CreatedBy = &user.ID
	}

	if automation.ScheduleType != models.AutomationScheduleNone {
		// Centralise schedule branching in ComputeNextRunAt so a new schedule
		// kind only has to be added there.
		now := time.Now()
		next, err := automation.ComputeNextRunAt(now)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
			return
		}
		automation.NextRunAt = &next
	}

	if err := h.automationStore.Create(r.Context(), &automation); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create automation", err)
		return
	}

	idStr := automation.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationCreated, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, automationAuditSnapshot(&automation)))
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Automation]{Data: automation})
}

func (h *AutomationHandler) CreatePublic(w http.ResponseWriter, r *http.Request) {
	if middleware.APITokenFromContext(r.Context()) != nil {
		h.CreateExternal(w, r)
		return
	}
	h.Create(w, r)
}

func (h *AutomationHandler) CreateExternal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string  `json:"name"`
		Goal         string  `json:"goal"`
		RepositoryID string  `json:"repository_id"`
		Scope        *string `json:"scope"`
		Schedule     struct {
			Type          models.AutomationScheduleType `json:"type"`
			Cron          *string                       `json:"cron"`
			IntervalValue *int                          `json:"interval_value"`
			IntervalUnit  *models.ScheduleUnit          `json:"interval_unit"`
			RunAt         *string                       `json:"run_at"`
			Timezone      *string                       `json:"timezone"`
		} `json:"schedule"`
		Triggers  []models.AutomationGitHubEvent `json:"triggers"`
		Execution struct {
			Mode          *models.ProjectExecMode `json:"mode"`
			MaxConcurrent *int                    `json:"max_concurrent"`
		} `json:"execution"`
		Agent struct {
			Type            *models.AgentType      `json:"type"`
			Model           *string                `json:"model"`
			ReasoningEffort models.ReasoningEffort `json:"reasoning_effort"`
		} `json:"agent"`
		PullRequest struct {
			BaseBranch       *string `json:"base_branch"`
			PrePRReviewLoops *int    `json:"pre_pr_review_loops"`
		} `json:"pull_request"`
		Identity struct {
			Scope *models.AutomationIdentityScope `json:"scope"`
		} `json:"identity"`
		Enabled  *bool           `json:"enabled"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	identityScope := models.AutomationIdentityScopeOrg
	if req.Identity.Scope != nil && *req.Identity.Scope != "" {
		identityScope = *req.Identity.Scope
	}
	if identityScope != models.AutomationIdentityScopeOrg {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDENTITY_SCOPE", "external API automations must use identity.scope=org")
		return
	}
	scheduleType := req.Schedule.Type
	if scheduleType == "" {
		scheduleType = models.AutomationScheduleInterval
	}
	flat := map[string]any{
		"name":                  req.Name,
		"goal":                  req.Goal,
		"repository_id":         req.RepositoryID,
		"scope":                 req.Scope,
		"schedule_type":         scheduleType,
		"execution_mode":        req.Execution.Mode,
		"max_concurrent":        req.Execution.MaxConcurrent,
		"agent_type":            req.Agent.Type,
		"model":                 req.Agent.Model,
		"reasoning_effort":      req.Agent.ReasoningEffort,
		"base_branch":           req.PullRequest.BaseBranch,
		"identity_scope":        identityScope,
		"pre_pr_review_loops":   req.PullRequest.PrePRReviewLoops,
		"timezone":              req.Schedule.Timezone,
		"metadata":              req.Metadata,
		"github_event_triggers": req.Triggers,
	}
	if scheduleType == models.AutomationScheduleCron {
		flat["cron_expression"] = req.Schedule.Cron
	} else {
		flat["interval_value"] = req.Schedule.IntervalValue
		flat["interval_unit"] = req.Schedule.IntervalUnit
		flat["interval_run_at"] = req.Schedule.RunAt
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENCODE_FAILED", "failed to encode automation request", err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.Create(w, r)
}

func resolveAutomationIcon(iconType models.AutomationIconType, iconValue string) (models.AutomationIconType, string, error) {
	if err := iconType.Validate(); err != nil {
		return "", "", err
	}
	resolvedType := iconType.OrDefault()
	resolvedValue := strings.TrimSpace(iconValue)
	resolvedValue = models.AutomationIconValueOrDefault(resolvedValue)
	if len([]rune(resolvedValue)) > 16 {
		return "", "", fmt.Errorf("icon_value must be at most 16 characters")
	}
	return resolvedType, resolvedValue, nil
}

func validateAutomationGitHubEventTriggers(events []models.AutomationGitHubEvent) ([]models.AutomationGitHubEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	seen := make(map[models.AutomationGitHubEvent]struct{}, len(events))
	out := make([]models.AutomationGitHubEvent, 0, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return nil, err
		}
		if _, ok := seen[event]; ok {
			continue
		}
		seen[event] = struct{}{}
		out = append(out, event)
	}
	return out, nil
}

func resolveAutomationGitHubEventTriggers(productTriggers []models.AutomationProductTrigger, rawEvents []models.AutomationGitHubEvent) ([]models.AutomationGitHubEvent, error) {
	if len(productTriggers) > 0 && len(rawEvents) > 0 {
		return nil, fmt.Errorf("send either triggers or github_event_triggers, not both")
	}
	if len(productTriggers) == 0 {
		return validateAutomationGitHubEventTriggers(rawEvents)
	}
	events := make([]models.AutomationGitHubEvent, 0, len(productTriggers))
	for _, trigger := range productTriggers {
		if err := trigger.Validate(); err != nil {
			return nil, err
		}
		switch trigger {
		case models.AutomationProductTriggerPROpened:
			events = append(events, models.AutomationGitHubEventPullRequestOpened)
		case models.AutomationProductTriggerPRUpdated:
			events = append(events, models.AutomationGitHubEventPullRequestUpdated)
		case models.AutomationProductTriggerPRFeedback:
			events = append(events,
				models.AutomationGitHubEventIssueCommentCreated,
				models.AutomationGitHubEventPullRequestReviewSubmitted,
				models.AutomationGitHubEventPullRequestReviewCommentCreated,
			)
		case models.AutomationProductTriggerChecksCompleted:
			events = append(events,
				models.AutomationGitHubEventCheckSuiteCompleted,
				models.AutomationGitHubEventCheckRunCompleted,
			)
		case models.AutomationProductTriggerPRMerged:
			events = append(events, models.AutomationGitHubEventPullRequestMerged)
		}
	}
	return validateAutomationGitHubEventTriggers(events)
}

func validateAutomationGitHubEventFilters(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var filters models.AutomationGitHubEventFilters
	if err := json.Unmarshal(raw, &filters); err != nil {
		return nil, fmt.Errorf("github_event_filters must be a JSON object")
	}
	normalize := func(values []string) []string {
		out := make([]string, 0, len(values))
		seen := map[string]struct{}{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, value)
		}
		return out
	}
	filters.BaseBranches = normalize(filters.BaseBranches)
	filters.Authors = normalize(filters.Authors)
	filters.Paths = normalize(filters.Paths)
	filters.FeedbackTypes = normalize(filters.FeedbackTypes)
	filters.ReviewStates = normalize(filters.ReviewStates)
	out, err := json.Marshal(filters)
	if err != nil {
		return nil, fmt.Errorf("encode github_event_filters: %w", err)
	}
	return out, nil
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
		Name                *string                            `json:"name"`
		Goal                *string                            `json:"goal"`
		IconType            *models.AutomationIconType         `json:"icon_type"`
		IconValue           *string                            `json:"icon_value"`
		Scope               *string                            `json:"scope"`
		RepositoryID        *string                            `json:"repository_id"`
		AgentType           *models.AgentType                  `json:"agent_type"`
		Model               *string                            `json:"model"`
		ReasoningEffort     *models.ReasoningEffort            `json:"reasoning_effort"`
		ExecutionMode       *models.ProjectExecMode            `json:"execution_mode"`
		MaxConcurrent       *int                               `json:"max_concurrent"`
		BaseBranch          *string                            `json:"base_branch"`
		IdentityScope       *models.AutomationIdentityScope    `json:"identity_scope"`
		ScheduleType        *models.AutomationScheduleType     `json:"schedule_type"`
		IntervalValue       *int                               `json:"interval_value"`
		IntervalUnit        *models.ScheduleUnit               `json:"interval_unit"`
		IntervalRunAt       *string                            `json:"interval_run_at"`
		CronExpression      *string                            `json:"cron_expression"`
		Timezone            *string                            `json:"timezone"`
		ProductTriggers     *[]models.AutomationProductTrigger `json:"triggers"`
		GitHubEventTriggers *[]models.AutomationGitHubEvent    `json:"github_event_triggers"`
		GitHubEventFilters  *json.RawMessage                   `json:"github_event_filters"`
		Priority            *int                               `json:"priority"`
		PrePRReviewLoops    *int                               `json:"pre_pr_review_loops"`
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
	if req.IconType != nil || req.IconValue != nil {
		iconType := automation.IconType
		iconValue := automation.IconValue
		if req.IconType != nil {
			iconType = *req.IconType
		}
		if req.IconValue != nil {
			iconValue = *req.IconValue
		}
		resolvedType, resolvedValue, err := resolveAutomationIcon(iconType, iconValue)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ICON", err.Error())
			return
		}
		automation.IconType = resolvedType
		automation.IconValue = resolvedValue
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
		var reqAgentType *string
		if req.AgentType != nil {
			agentTypeString := string(*req.AgentType)
			reqAgentType = &agentTypeString
		}
		agentType, modelOverride, err := resolveAutomationAgentAndModel(automation.AgentType, automation.ModelOverride, reqAgentType, req.Model)
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
	if req.AgentType != nil || req.Model != nil || req.ReasoningEffort != nil {
		effectiveAgentType, err := h.defaultAutomationAgentType(r.Context(), orgID, automation.AgentType)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
			return
		}
		raw := ""
		if req.ReasoningEffort != nil {
			raw = string(*req.ReasoningEffort)
		} else if automation.ReasoningEffort != nil {
			raw = string(*automation.ReasoningEffort)
		}
		reasoningOverride, err := parseReasoningEffortForAgent(effectiveAgentType, raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REASONING_EFFORT", err.Error())
			return
		}
		automation.ReasoningEffort = reasoningOverride
	}
	if req.ExecutionMode != nil {
		if err := req.ExecutionMode.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXECUTION_MODE", err.Error())
			return
		}
		automation.ExecutionMode = models.AutomationExecutionMode(*req.ExecutionMode)
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
	if req.PrePRReviewLoops != nil {
		if *req.PrePRReviewLoops < 0 || *req.PrePRReviewLoops > 5 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRE_PR_REVIEW_LOOPS", "pre_pr_review_loops must be between 0 and 5")
			return
		}
		automation.PrePRReviewLoops = *req.PrePRReviewLoops
	}
	if req.AgentType != nil || req.Model != nil || req.PrePRReviewLoops != nil {
		effectiveAgentType, err := h.defaultAutomationAgentType(r.Context(), orgID, automation.AgentType)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
			return
		}
		if err := validatePrePRReviewLoopsForAgent(automation.PrePRReviewLoops, effectiveAgentType); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PRE_PR_REVIEW_LOOPS", err.Error())
			return
		}
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
	if req.ProductTriggers != nil || req.GitHubEventTriggers != nil {
		var productTriggers []models.AutomationProductTrigger
		if req.ProductTriggers != nil {
			productTriggers = *req.ProductTriggers
		}
		var rawTriggers []models.AutomationGitHubEvent
		if req.GitHubEventTriggers != nil {
			rawTriggers = *req.GitHubEventTriggers
		}
		githubEventTriggers, err := resolveAutomationGitHubEventTriggers(productTriggers, rawTriggers)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_GITHUB_EVENT_TRIGGERS", err.Error())
			return
		}
		automation.GitHubEventTriggers = githubEventTriggers
	}
	if req.GitHubEventFilters != nil {
		githubEventFilters, err := validateAutomationGitHubEventFilters(*req.GitHubEventFilters)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_GITHUB_EVENT_FILTERS", err.Error())
			return
		}
		automation.GitHubEventFilters = githubEventFilters
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
		if err := req.ScheduleType.Validate(); err != nil {
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
	if effectiveScheduleType == models.AutomationScheduleNone &&
		(req.IntervalValue != nil || req.IntervalUnit != nil || req.IntervalRunAt != nil || req.CronExpression != nil) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", "schedule fields must not be set when schedule_type is none")
		return
	}
	if effectiveScheduleType == models.AutomationScheduleNone && len(automation.GitHubEventTriggers) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_TRIGGER", "event-only automations require at least one GitHub event trigger")
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
			case models.AutomationScheduleNone:
				automation.IntervalValue = nil
				automation.IntervalUnit = nil
				automation.IntervalRunAt = nil
				automation.CronExpression = nil
				automation.NextRunAt = nil
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
		if err := req.IntervalUnit.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_INTERVAL_UNIT", err.Error())
			return
		}
		intervalUnit := *req.IntervalUnit
		automation.IntervalUnit = &intervalUnit
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
		if automation.ScheduleType == models.AutomationScheduleNone {
			automation.NextRunAt = nil
		} else {
			next, err := automation.ComputeNextRunAt(time.Now())
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
				return
			}
			automation.NextRunAt = &next
		}
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
	if user != nil {
		automation.PausedBy = &user.ID
	}
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

	if automation.ScheduleType == models.AutomationScheduleNone {
		automation.NextRunAt = nil
	} else {
		// Centralised schedule branching — see ComputeNextRunAt. If the stored
		// schedule fields are malformed, surface the error instead of silently
		// leaving next_run_at stale so the scheduler never fires it.
		next, err := automation.ComputeNextRunAt(time.Now())
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
			return
		}
		automation.NextRunAt = &next
	}

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
		AutomationID:   automation.ID,
		OrgID:          automation.OrgID,
		TriggeredBy:    models.AutomationTriggeredByManual,
		GoalSnapshot:   automation.Goal,
		ConfigSnapshot: configSnapshot,
		Status:         models.AutomationRunStatusPending,
	}
	if user != nil {
		run.TriggeredByUserID = &user.ID
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
