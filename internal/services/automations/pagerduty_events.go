package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type pagerDutyEventTriggerStore interface {
	ListEnabledByProviderEvent(ctx context.Context, orgID uuid.UUID, provider models.AutomationEventProvider, eventType string) ([]models.AutomationEventTrigger, error)
}

type pagerDutyServiceMappingStore interface {
	GetByServiceID(ctx context.Context, orgID, integrationID uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error)
}

type pagerDutyProviderIntegrationStore interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error)
}

type pagerDutyDefaultRepositoryLoader interface {
	LoadDefaultWorkRepositoryID(ctx context.Context, orgID uuid.UUID) (*uuid.UUID, error)
}

type pagerDutyEventAutomationStore interface {
	LockByIDForUpdate(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (models.Automation, error)
	CountInFlightRuns(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (int, error)
}

type pagerDutyEventAutomationRunStore interface {
	CreateRunInTx(ctx context.Context, tx pgx.Tx, r *models.AutomationRun) (bool, error)
	CountRecentProviderTriggerRuns(ctx context.Context, tx pgx.Tx, orgID, automationID, triggerID uuid.UUID, provider models.AutomationEventProvider, since time.Time) (int, error)
}

type pagerDutyEventJobStore interface {
	EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	Notify(ctx context.Context, jobID uuid.UUID)
}

type pagerDutyEventTxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type pagerDutyEventAuditEmitter interface {
	EmitWebhookAction(ctx context.Context, params db.WebhookActionParams)
}

type PagerDutyEventTriggerService struct {
	triggers     pagerDutyEventTriggerStore
	automations  pagerDutyEventAutomationStore
	runs         pagerDutyEventAutomationRunStore
	jobs         pagerDutyEventJobStore
	txStarter    pagerDutyEventTxStarter
	mappings     pagerDutyServiceMappingStore
	integrations pagerDutyProviderIntegrationStore
	defaultRepos pagerDutyDefaultRepositoryLoader
	capabilities githubCapabilityResolver
	audit        pagerDutyEventAuditEmitter
	metrics      *metrics.PagerDutyMetrics
	logger       zerolog.Logger
	now          func() time.Time
}

func NewPagerDutyEventTriggerService(
	triggers pagerDutyEventTriggerStore,
	automations pagerDutyEventAutomationStore,
	runs pagerDutyEventAutomationRunStore,
	jobs pagerDutyEventJobStore,
	txStarter pagerDutyEventTxStarter,
	logger zerolog.Logger,
) *PagerDutyEventTriggerService {
	return &PagerDutyEventTriggerService{
		triggers:    triggers,
		automations: automations,
		runs:        runs,
		jobs:        jobs,
		txStarter:   txStarter,
		logger:      logger,
		now:         time.Now,
	}
}

func (s *PagerDutyEventTriggerService) SetCapabilityResolver(resolver githubCapabilityResolver) {
	s.capabilities = resolver
}

func (s *PagerDutyEventTriggerService) SetRepositoryResolver(mappings pagerDutyServiceMappingStore, integrations pagerDutyProviderIntegrationStore) {
	s.mappings = mappings
	s.integrations = integrations
}

func (s *PagerDutyEventTriggerService) SetDefaultRepositoryResolver(loader pagerDutyDefaultRepositoryLoader) {
	s.defaultRepos = loader
}

func (s *PagerDutyEventTriggerService) SetMetrics(metrics *metrics.PagerDutyMetrics) {
	s.metrics = metrics
}

func (s *PagerDutyEventTriggerService) SetAuditEmitter(audit pagerDutyEventAuditEmitter) {
	s.audit = audit
}

func (s *PagerDutyEventTriggerService) TriggerPagerDutyEvent(ctx context.Context, req pagerdutysvc.EventTriggerRequest) error {
	if s == nil || s.triggers == nil || s.automations == nil || s.runs == nil || s.jobs == nil || s.txStarter == nil {
		return nil
	}
	if err := req.EventType.Validate(); err != nil {
		return err
	}

	triggers, err := s.triggers.ListEnabledByProviderEvent(ctx, req.OrgID, models.AutomationEventProviderPagerDuty, string(req.EventType))
	if err != nil {
		return fmt.Errorf("list pagerduty event triggers: %w", err)
	}
	if len(triggers) == 0 {
		s.recordAutomationMatch(ctx, req.EventType, "no_trigger")
	}
	for _, trigger := range triggers {
		matches, err := pagerDutyTriggerMatches(trigger.Filter, req.Incident)
		if err != nil {
			s.recordAutomationMatch(ctx, req.EventType, "filter_error")
			return fmt.Errorf("match pagerduty trigger %s: %w", trigger.ID, err)
		}
		if !matches {
			s.recordAutomationMatch(ctx, req.EventType, "filter_miss")
			continue
		}
		result, err := s.createRunForTrigger(ctx, trigger, req)
		s.recordAutomationMatch(ctx, req.EventType, result)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PagerDutyEventTriggerService) createRunForTrigger(ctx context.Context, trigger models.AutomationEventTrigger, req pagerdutysvc.EventTriggerRequest) (string, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return "tx_begin_failed", fmt.Errorf("begin pagerduty automation run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	automation, err := s.automations.LockByIDForUpdate(ctx, tx, req.OrgID, trigger.AutomationID)
	if err != nil {
		return "automation_lock_failed", fmt.Errorf("lock pagerduty event automation: %w", err)
	}

	status := models.AutomationRunStatusPending
	var completedAt *time.Time
	var resultSummary *string
	result := "created"
	if !automation.Enabled {
		status = models.AutomationRunStatusSkipped
		result = "automation_disabled"
		now := s.now()
		completedAt = &now
		summary := "automation disabled before PagerDuty incident-triggered run could start"
		resultSummary = &summary
	} else {
		inFlight, err := s.automations.CountInFlightRuns(ctx, tx, req.OrgID, automation.ID)
		if err != nil {
			return "count_in_flight_failed", fmt.Errorf("count pagerduty automation in-flight runs: %w", err)
		}
		maxConcurrent := automation.MaxConcurrent
		if maxConcurrent <= 0 {
			maxConcurrent = 1
		}
		if inFlight >= maxConcurrent {
			status = models.AutomationRunStatusSkipped
			result = "max_concurrent"
			now := s.now()
			completedAt = &now
			summary := fmt.Sprintf("max_concurrent saturated for PagerDuty incident trigger (%d/%d in flight)", inFlight, maxConcurrent)
			resultSummary = &summary
		}
	}
	filter, err := parsePagerDutyTriggerFilter(trigger.Filter)
	if err != nil {
		return "filter_decode_failed", err
	}
	if status == models.AutomationRunStatusPending && filter.CooldownMinutes > 0 {
		since := s.now().Add(-time.Duration(filter.CooldownMinutes) * time.Minute)
		recent, err := s.runs.CountRecentProviderTriggerRuns(ctx, tx, req.OrgID, automation.ID, trigger.ID, models.AutomationEventProviderPagerDuty, since)
		if err != nil {
			return "cooldown_count_failed", fmt.Errorf("count pagerduty automation cooldown runs: %w", err)
		}
		if recent > 0 {
			status = models.AutomationRunStatusSkipped
			result = "cooldown"
			now := s.now()
			completedAt = &now
			summary := fmt.Sprintf("cooldown active for PagerDuty trigger (%d prior run(s) in the last %d minutes)", recent, filter.CooldownMinutes)
			resultSummary = &summary
		}
	}

	configSnapshot, err := automation.BuildConfigSnapshot()
	if err != nil {
		return "config_snapshot_failed", fmt.Errorf("build pagerduty automation config snapshot: %w", err)
	}
	repository, err := s.resolveRepository(ctx, trigger, automation, req)
	if err != nil {
		return "repository_resolution_failed", err
	}
	if status == models.AutomationRunStatusPending && repository.RepositoryID == nil {
		status = models.AutomationRunStatusSkipped
		result = "repository_unmapped"
		now := s.now()
		completedAt = &now
		summary := "repository_unmapped: PagerDuty incident service is not mapped to a repository"
		resultSummary = &summary
	}
	configSnapshot, err = withPagerDutyEventSnapshot(configSnapshot, trigger, req, repository)
	if err != nil {
		return "snapshot_failed", err
	}
	triggerContext, err := pagerDutyTriggerContext(trigger, req, repository)
	if err != nil {
		return "trigger_context_failed", err
	}

	provider := models.AutomationEventProviderPagerDuty
	providerEventID := req.ProviderEventID
	run := models.AutomationRun{
		AutomationID:    automation.ID,
		OrgID:           automation.OrgID,
		TriggeredBy:     models.AutomationTriggeredByProviderEvent,
		TriggerID:       &trigger.ID,
		Provider:        &provider,
		ProviderEventID: &providerEventID,
		TriggerContext:  triggerContext,
		GoalSnapshot:    pagerDutyEventGoalSnapshot(automation.Goal, req),
		ConfigSnapshot:  configSnapshot,
		Status:          status,
		CompletedAt:     completedAt,
		ResultSummary:   resultSummary,
	}
	if status == models.AutomationRunStatusPending && s.capabilities != nil {
		snapshot, err := s.capabilities.ResolveForSession(ctx, agentcapabilities.ResolveInput{
			OrgID:         automation.OrgID,
			RepositoryID:  repository.RepositoryID,
			SessionOrigin: models.SessionOriginAutomation,
			AutomationID:  &automation.ID,
		})
		if err != nil {
			return "capability_resolution_failed", fmt.Errorf("resolve pagerduty-triggered automation capabilities: %w", err)
		}
		run.CapabilitySnapshot = snapshot
	}

	created, err := s.runs.CreateRunInTx(ctx, tx, &run)
	if err != nil {
		return "run_create_failed", fmt.Errorf("create pagerduty-triggered automation run: %w", err)
	}
	if !created {
		if err := tx.Commit(ctx); err != nil {
			return "commit_failed", fmt.Errorf("commit pagerduty automation run tx: %w", err)
		}
		return "duplicate", nil
	}

	var jobID uuid.UUID
	if status == models.AutomationRunStatusPending {
		dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
		payload := map[string]string{
			"org_id":            automation.OrgID.String(),
			"automation_id":     automation.ID.String(),
			"automation_run_id": run.ID.String(),
		}
		jobID, err = s.jobs.EnqueueInTx(ctx, tx, automation.OrgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
		if err != nil {
			return "enqueue_failed", fmt.Errorf("enqueue pagerduty-triggered automation run: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "commit_failed", fmt.Errorf("commit pagerduty automation run tx: %w", err)
	}
	s.emitPagerDutyRunAudit(ctx, automation, trigger, run, req, repository, result)
	if jobID != uuid.Nil {
		s.jobs.Notify(ctx, jobID)
	}
	return result, nil
}

func (s *PagerDutyEventTriggerService) emitPagerDutyRunAudit(ctx context.Context, automation models.Automation, trigger models.AutomationEventTrigger, run models.AutomationRun, req pagerdutysvc.EventTriggerRequest, repository pagerDutyRepositoryResolution, result string) {
	if s == nil || s.audit == nil {
		return
	}
	details := map[string]any{
		"provider":           "pagerduty",
		"event_type":         string(req.EventType),
		"provider_event_id":  req.ProviderEventID,
		"incident_id":        req.Incident.IncidentID,
		"incident_status":    req.Incident.Status,
		"automation_run_id":  run.ID.String(),
		"trigger_id":         trigger.ID.String(),
		"run_status":         string(run.Status),
		"result":             result,
		"repository_source":  repository.Source,
		"pagerduty_title":    req.Incident.Title,
		"pagerduty_priority": stringPtrValue(req.Incident.PriorityName),
		"pagerduty_urgency":  stringPtrValue(req.Incident.Urgency),
	}
	if repository.RepositoryID != nil && *repository.RepositoryID != uuid.Nil {
		details["repository_id"] = repository.RepositoryID.String()
	}
	rawDetails, err := json.Marshal(details)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to marshal PagerDuty automation run audit details")
		return
	}
	resourceID := automation.ID.String()
	s.audit.EmitWebhookAction(ctx, db.WebhookActionParams{
		OrgID:        automation.OrgID,
		ProviderName: "pagerduty",
		Action:       models.AuditActionAutomationRunTriggered,
		ResourceType: models.AuditResourceAutomation,
		ResourceID:   &resourceID,
		Details:      rawDetails,
	})
}

func (s *PagerDutyEventTriggerService) recordAutomationMatch(ctx context.Context, eventType models.PagerDutyEventType, result string) {
	if s == nil {
		return
	}
	s.metrics.RecordAutomationMatch(ctx, string(eventType), result)
}

type pagerDutyTriggerFilter struct {
	ServiceIDs      []string            `json:"service_ids"`
	TeamIDs         []string            `json:"team_ids"`
	Statuses        []string            `json:"statuses"`
	Urgencies       []string            `json:"urgencies"`
	PriorityNames   []string            `json:"priority_names"`
	IncidentTypes   []string            `json:"incident_types"`
	TitleContains   string              `json:"title_contains"`
	CustomFields    map[string][]string `json:"custom_fields"`
	CooldownMinutes int                 `json:"cooldown_minutes"`
}

func pagerDutyTriggerMatches(raw json.RawMessage, incident models.PagerDutyIncident) (bool, error) {
	filter, err := parsePagerDutyTriggerFilter(raw)
	if err != nil {
		return false, err
	}
	if !matchesStringPtrFilter(filter.ServiceIDs, incident.ServiceID, true) {
		return false, nil
	}
	if !matchesAnyString(filter.TeamIDs, incident.TeamIDs, true) {
		return false, nil
	}
	if !matchesStringFilter(filter.Statuses, incident.Status, true) {
		return false, nil
	}
	if !matchesStringPtrFilter(filter.Urgencies, incident.Urgency, true) {
		return false, nil
	}
	if !matchesStringPtrFilter(filter.PriorityNames, incident.PriorityName, true) {
		return false, nil
	}
	if !matchesStringPtrFilter(filter.IncidentTypes, incident.IncidentType, true) {
		return false, nil
	}
	if strings.TrimSpace(filter.TitleContains) != "" &&
		!strings.Contains(strings.ToLower(incident.Title), strings.ToLower(strings.TrimSpace(filter.TitleContains))) {
		return false, nil
	}
	if !matchesPagerDutyCustomFields(filter.CustomFields, incident.RawData) {
		return false, nil
	}
	return true, nil
}

func parsePagerDutyTriggerFilter(raw json.RawMessage) (pagerDutyTriggerFilter, error) {
	filter := pagerDutyTriggerFilter{}
	if len(raw) == 0 {
		return filter, nil
	}
	if err := json.Unmarshal(raw, &filter); err != nil {
		return pagerDutyTriggerFilter{}, fmt.Errorf("decode pagerduty trigger filter: %w", err)
	}
	return filter, nil
}

func matchesStringPtrFilter(allowed []string, value *string, fold bool) bool {
	if value == nil {
		return len(allowed) == 0
	}
	return matchesStringFilter(allowed, *value, fold)
}

func matchesStringFilter(allowed []string, value string, fold bool) bool {
	if len(allowed) == 0 {
		return true
	}
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(candidate)
		if fold {
			if strings.EqualFold(candidate, value) {
				return true
			}
			continue
		}
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesAnyString(allowed []string, values []string, fold bool) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, value := range values {
		if matchesStringFilter(allowed, value, fold) {
			return true
		}
	}
	return false
}

func matchesPagerDutyCustomFields(allowed map[string][]string, raw json.RawMessage) bool {
	if len(allowed) == 0 {
		return true
	}
	if len(raw) == 0 {
		return false
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	custom := firstPagerDutyCustomFieldsMap(decoded)
	if len(custom) == 0 {
		return false
	}
	for key, expected := range allowed {
		values := pagerDutyCustomFieldValues(custom[key])
		if len(values) == 0 {
			return false
		}
		matched := false
		for _, value := range values {
			if matchesStringFilter(expected, value, true) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func firstPagerDutyCustomFieldsMap(decoded map[string]any) map[string]any {
	if custom, ok := decoded["custom_fields"].(map[string]any); ok {
		return custom
	}
	if incident, ok := decoded["incident"].(map[string]any); ok {
		if custom, ok := incident["custom_fields"].(map[string]any); ok {
			return custom
		}
	}
	return nil
}

func pagerDutyCustomFieldValues(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := []string{}
		for _, item := range v {
			out = append(out, pagerDutyCustomFieldValues(item)...)
		}
		return out
	case map[string]any:
		out := []string{}
		for _, key := range []string{"value", "display_value", "name", "summary"} {
			out = append(out, pagerDutyCustomFieldValues(v[key])...)
		}
		return out
	default:
		return nil
	}
}

type pagerDutyRepositoryResolution struct {
	RepositoryID *uuid.UUID
	Source       string
	BaseBranch   *string
}

func (s *PagerDutyEventTriggerService) resolveRepository(ctx context.Context, trigger models.AutomationEventTrigger, automation models.Automation, req pagerdutysvc.EventTriggerRequest) (pagerDutyRepositoryResolution, error) {
	if trigger.RepositoryID != nil {
		return pagerDutyRepositoryResolution{RepositoryID: trigger.RepositoryID, Source: "trigger"}, nil
	}
	if s.mappings != nil && req.Incident.ServiceID != nil && req.Incident.PagerDutyIntegrationID != uuid.Nil {
		mapping, err := s.mappings.GetByServiceID(ctx, req.OrgID, req.Incident.PagerDutyIntegrationID, *req.Incident.ServiceID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pagerDutyRepositoryResolution{}, fmt.Errorf("lookup PagerDuty service repository mapping: %w", err)
		}
		if err == nil && mapping.Enabled {
			return pagerDutyRepositoryResolution{
				RepositoryID: &mapping.RepositoryID,
				Source:       "service_mapping",
				BaseBranch:   mapping.BaseBranch,
			}, nil
		}
	}
	if s.integrations != nil && req.Incident.PagerDutyIntegrationID != uuid.Nil {
		integration, err := s.integrations.GetByID(ctx, req.OrgID, req.Incident.PagerDutyIntegrationID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pagerDutyRepositoryResolution{}, fmt.Errorf("lookup PagerDuty integration default repository: %w", err)
		}
		if err == nil && integration.DefaultRepositoryID != nil {
			return pagerDutyRepositoryResolution{RepositoryID: integration.DefaultRepositoryID, Source: "integration_default"}, nil
		}
	}
	if s.defaultRepos != nil {
		repositoryID, err := s.defaultRepos.LoadDefaultWorkRepositoryID(ctx, req.OrgID)
		if err != nil {
			return pagerDutyRepositoryResolution{}, fmt.Errorf("lookup shared default work repository: %w", err)
		}
		if repositoryID != nil {
			return pagerDutyRepositoryResolution{RepositoryID: repositoryID, Source: "org_default"}, nil
		}
	}
	return pagerDutyRepositoryResolution{Source: "repository_unmapped"}, nil
}

func withPagerDutyEventSnapshot(raw json.RawMessage, trigger models.AutomationEventTrigger, req pagerdutysvc.EventTriggerRequest, repository pagerDutyRepositoryResolution) (json.RawMessage, error) {
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decode config snapshot: %w", err)
		}
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	pd := map[string]any{
		"event_type":        string(req.EventType),
		"provider_event_id": req.ProviderEventID,
		"incident_id":       req.Incident.IncidentID,
		"incident_status":   req.Incident.Status,
		"title":             req.Incident.Title,
	}
	if req.Incident.PagerDutyIntegrationID != uuid.Nil {
		pd["pagerduty_integration_id"] = req.Incident.PagerDutyIntegrationID.String()
	}
	addStringPtr(pd, "service_id", req.Incident.ServiceID)
	addStringPtr(pd, "service_name", req.Incident.ServiceName)
	addStringPtr(pd, "urgency", req.Incident.Urgency)
	addStringPtr(pd, "priority_name", req.Incident.PriorityName)
	addStringPtr(pd, "incident_url", req.Incident.HTMLURL)
	if req.OccurredAt != nil {
		pd["occurred_at"] = req.OccurredAt.Format(time.RFC3339)
	}
	if repository.Source != "" && repository.RepositoryID != nil {
		pd["repository_id"] = repository.RepositoryID.String()
		pd["repository_source"] = repository.Source
		if repository.BaseBranch != nil && strings.TrimSpace(*repository.BaseBranch) != "" {
			pd["base_branch"] = strings.TrimSpace(*repository.BaseBranch)
		}
	} else if trigger.RepositoryID != nil {
		pd["repository_id"] = trigger.RepositoryID.String()
	} else if repository.Source != "" {
		pd["repository_source"] = repository.Source
	}
	decoded["pagerduty"] = pd
	out, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode config snapshot: %w", err)
	}
	return out, nil
}

func pagerDutyTriggerContext(trigger models.AutomationEventTrigger, req pagerdutysvc.EventTriggerRequest, repository pagerDutyRepositoryResolution) (json.RawMessage, error) {
	context := map[string]any{
		"provider":          string(models.AutomationEventProviderPagerDuty),
		"event_type":        string(req.EventType),
		"provider_event_id": req.ProviderEventID,
		"incident_id":       req.Incident.IncidentID,
		"incident_status":   req.Incident.Status,
	}
	if req.Incident.PagerDutyIntegrationID != uuid.Nil {
		context["pagerduty_integration_id"] = req.Incident.PagerDutyIntegrationID.String()
	}
	addStringPtr(context, "service_id", req.Incident.ServiceID)
	addStringPtr(context, "service_name", req.Incident.ServiceName)
	addStringPtr(context, "urgency", req.Incident.Urgency)
	addStringPtr(context, "priority_name", req.Incident.PriorityName)
	if req.OccurredAt != nil {
		context["occurred_at"] = req.OccurredAt.Format(time.RFC3339)
	}
	if repository.Source != "" && repository.RepositoryID != nil {
		context["repository_id"] = repository.RepositoryID.String()
		context["repository_source"] = repository.Source
		if repository.BaseBranch != nil && strings.TrimSpace(*repository.BaseBranch) != "" {
			context["base_branch"] = strings.TrimSpace(*repository.BaseBranch)
		}
	} else if trigger.RepositoryID != nil {
		context["repository_id"] = trigger.RepositoryID.String()
	} else if repository.Source != "" {
		context["repository_source"] = repository.Source
	}
	out, err := json.Marshal(context)
	if err != nil {
		return nil, fmt.Errorf("encode pagerduty trigger context: %w", err)
	}
	return out, nil
}

func pagerDutyEventGoalSnapshot(goal string, req pagerdutysvc.EventTriggerRequest) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(goal))
	b.WriteString("\n\nPagerDuty incident:\n")
	writeGoalLine(&b, "ID", req.Incident.IncidentID)
	if req.Incident.IncidentNumber != nil {
		writeGoalLine(&b, "Number", fmt.Sprintf("%d", *req.Incident.IncidentNumber))
	}
	writeGoalLine(&b, "Event", string(req.EventType))
	writeGoalLine(&b, "Status", req.Incident.Status)
	writeGoalLine(&b, "Urgency", stringValue(req.Incident.Urgency))
	writeGoalLine(&b, "Priority", stringValue(req.Incident.PriorityName))
	writeGoalLine(&b, "Service", stringValue(req.Incident.ServiceName))
	writeGoalLine(&b, "Escalation policy", stringValue(req.Incident.EscalationPolicyName))
	writeGoalLine(&b, "Incident type", stringValue(req.Incident.IncidentType))
	writeGoalLine(&b, "Incident URL", stringValue(req.Incident.HTMLURL))
	if req.OccurredAt != nil {
		writeGoalLine(&b, "Event time", req.OccurredAt.Format(time.RFC3339))
	}
	if req.Incident.LatestNote != nil && strings.TrimSpace(*req.Incident.LatestNote) != "" {
		b.WriteString("Latest note:\n")
		b.WriteString(strings.TrimSpace(*req.Incident.LatestNote))
		b.WriteByte('\n')
	}
	b.WriteString("\nInvestigate the incident, produce a fix if it is straightforward and safe, or produce a clear diagnostic summary. Do not mutate PagerDuty incident state unless an explicit tool supports the requested action.")
	return strings.TrimSpace(b.String())
}

func writeGoalLine(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func addStringPtr(target map[string]any, key string, value *string) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return
	}
	target[key] = strings.TrimSpace(*value)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
