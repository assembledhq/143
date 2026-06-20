package automations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPagerDutyEventTriggerService_TriggerPagerDutyEventCreatesRunForMatchingTrigger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	triggerID := uuid.New()
	repositoryID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	jobID := uuid.New()
	provider := models.AutomationEventProviderPagerDuty
	providerEventID := "evt-1"
	occurredAt := time.Date(2026, 6, 19, 12, 34, 56, 0, time.UTC)
	trigger := models.AutomationEventTrigger{
		ID:           triggerID,
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"],"priority_names":["P1","P2"]}`),
		RepositoryID: &repositoryID,
		Enabled:      true,
	}
	automation := models.Automation{
		ID:               automationID,
		OrgID:            orgID,
		RepositoryID:     &repositoryID,
		Name:             "P1 API incident",
		Goal:             "Investigate and open a fix if safe.",
		MaxConcurrent:    1,
		BaseBranch:       "main",
		IdentityScope:    models.AutomationIdentityScopeOrg,
		ScheduleType:     models.AutomationScheduleNone,
		ExecutionMode:    models.AutomationExecutionModeSequential,
		Enabled:          true,
		PrePRReviewLoops: 1,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: jobID}
	tx := &pagerDutyTxStarterFake{}
	audit := &pagerDutyEventAuditFake{}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	service.SetAuditEmitter(audit)
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: providerEventID,
		EventType:       models.PagerDutyEventIncidentTriggered,
		OccurredAt:      &occurredAt,
		Incident: models.PagerDutyIncident{
			OrgID:                  orgID,
			PagerDutyIntegrationID: pagerDutyIntegrationID,
			IncidentID:             "PABC123",
			Title:                  "API latency",
			Status:                 "triggered",
			Urgency:                strPtrPagerDutyEventTest("high"),
			PriorityName:           strPtrPagerDutyEventTest("P1"),
			ServiceID:              strPtrPagerDutyEventTest("PSVC"),
			ServiceName:            strPtrPagerDutyEventTest("api"),
		},
	})
	require.NoError(t, err, "TriggerPagerDutyEvent should create and enqueue matching automation runs")
	require.Equal(t, models.AutomationEventProviderPagerDuty, triggers.provider, "trigger listing should scope to PagerDuty provider")
	require.Equal(t, string(models.PagerDutyEventIncidentTriggered), triggers.eventType, "trigger listing should scope to the PagerDuty event type")
	require.Len(t, runs.runs, 1, "matching event should create exactly one automation run")
	run := runs.runs[0]
	require.Equal(t, models.AutomationTriggeredByProviderEvent, run.TriggeredBy, "run should record provider-event trigger source")
	require.Equal(t, models.AutomationRunStatusPending, run.Status, "matching unsaturated event should create a pending run")
	require.Equal(t, &triggerID, run.TriggerID, "run should preserve the trigger id")
	require.Equal(t, &provider, run.Provider, "run should preserve provider metadata")
	require.Equal(t, &providerEventID, run.ProviderEventID, "run should preserve provider event id for idempotency")
	require.Contains(t, run.GoalSnapshot, "PagerDuty incident", "goal snapshot should include structured incident context")
	require.Contains(t, run.GoalSnapshot, "PABC123", "goal snapshot should include incident id")
	require.JSONEq(t, `{"provider":"pagerduty","event_type":"incident.triggered","provider_event_id":"evt-1","pagerduty_integration_id":"`+pagerDutyIntegrationID.String()+`","incident_id":"PABC123","incident_status":"triggered","service_id":"PSVC","service_name":"api","urgency":"high","priority_name":"P1","occurred_at":"2026-06-19T12:34:56Z","repository_id":"`+repositoryID.String()+`","repository_source":"trigger"}`, string(run.TriggerContext), "trigger context should capture compact PagerDuty metadata")
	require.Equal(t, jobID, jobs.notifiedID, "created pending run should notify the queued automation job")
	require.True(t, tx.committed, "event-trigger run creation should commit its transaction")
	require.Equal(t, models.AuditActionAutomationRunTriggered, audit.params.Action, "PagerDuty event run creation should emit automation run audit")
	require.Equal(t, models.AuditActorWebhook, audit.actorType(), "PagerDuty event run creation should use webhook audit actor")
	require.Equal(t, automationID.String(), *audit.params.ResourceID, "PagerDuty event run audit should target the automation")
	var details map[string]any
	require.NoError(t, json.Unmarshal(audit.params.Details, &details), "PagerDuty event run audit details should decode")
	require.Equal(t, "pagerduty", details["provider"], "PagerDuty event run audit should record provider")
	require.Equal(t, "created", details["result"], "PagerDuty event run audit should record match result")
	require.Equal(t, run.ID.String(), details["automation_run_id"], "PagerDuty event run audit should record automation run id")
	require.Equal(t, "PABC123", details["incident_id"], "PagerDuty event run audit should record incident id")
}

func TestPagerDutyEventTriggerService_TriggerPagerDutyEventSaturationCreatesSkippedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	triggerID := uuid.New()
	providerEventID := "evt-2"
	trigger := models.AutomationEventTrigger{
		ID:           triggerID,
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"]}`),
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		Name:          "P1 API incident",
		Goal:          "Investigate.",
		MaxConcurrent: 1,
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation, inFlight: 1}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: uuid.New()}
	tx := &pagerDutyTxStarterFake{}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: providerEventID,
		EventType:       models.PagerDutyEventIncidentTriggered,
		Incident: models.PagerDutyIncident{
			OrgID:      orgID,
			IncidentID: "PABC123",
			Title:      "API latency",
			Status:     "triggered",
			Urgency:    strPtrPagerDutyEventTest("high"),
			ServiceID:  strPtrPagerDutyEventTest("PSVC"),
		},
	})
	require.NoError(t, err, "TriggerPagerDutyEvent should record saturated events without launching work")
	require.Len(t, runs.runs, 1, "saturated matching event should still create an automation run record")
	require.Equal(t, models.AutomationRunStatusSkipped, runs.runs[0].Status, "saturated event should create a skipped run")
	require.NotNil(t, runs.runs[0].CompletedAt, "skipped run should be terminal immediately")
	require.NotNil(t, runs.runs[0].ResultSummary, "skipped run should explain why no session was launched")
	require.True(t, strings.Contains(*runs.runs[0].ResultSummary, "max_concurrent"), "skipped summary should identify max_concurrent saturation")
	require.Equal(t, uuid.Nil, jobs.enqueuedID, "saturated event should not enqueue an automation_run job")
	require.True(t, tx.committed, "saturated run record should commit")
}

func TestPagerDutyEventTriggerService_UsesServiceMappingRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	integrationID := uuid.New()
	automationRepoID := uuid.New()
	mappedRepoID := uuid.New()
	jobID := uuid.New()
	trigger := models.AutomationEventTrigger{
		ID:           uuid.New(),
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"]}`),
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		RepositoryID:  &automationRepoID,
		Name:          "Mapped service incident",
		Goal:          "Investigate.",
		MaxConcurrent: 1,
		BaseBranch:    "main",
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: jobID}
	tx := &pagerDutyTxStarterFake{}
	mappings := &pagerDutyServiceMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		PagerDutyServiceID:     "PSVC",
		PagerDutyServiceName:   "api",
		RepositoryID:           mappedRepoID,
		Enabled:                true,
	}}
	capabilities := &pagerDutyCapabilityResolverFake{}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	service.SetRepositoryResolver(mappings, nil)
	service.SetCapabilityResolver(capabilities)
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: "evt-3",
		EventType:       models.PagerDutyEventIncidentTriggered,
		Incident: models.PagerDutyIncident{
			OrgID:                  orgID,
			PagerDutyIntegrationID: integrationID,
			IncidentID:             "PABC123",
			Title:                  "API latency",
			Status:                 "triggered",
			ServiceID:              strPtrPagerDutyEventTest("PSVC"),
			ServiceName:            strPtrPagerDutyEventTest("api"),
		},
	})
	require.NoError(t, err, "TriggerPagerDutyEvent should use service mapping repository")
	require.Len(t, runs.runs, 1, "matching mapped event should create one run")
	require.JSONEq(t, `{"provider":"pagerduty","event_type":"incident.triggered","provider_event_id":"evt-3","pagerduty_integration_id":"`+integrationID.String()+`","incident_id":"PABC123","incident_status":"triggered","service_id":"PSVC","service_name":"api","repository_id":"`+mappedRepoID.String()+`","repository_source":"service_mapping"}`, string(runs.runs[0].TriggerContext), "trigger context should capture mapped repository")
	require.NotNil(t, capabilities.input.RepositoryID, "capability resolver should receive a repository")
	require.Equal(t, mappedRepoID, *capabilities.input.RepositoryID, "capabilities should resolve against the mapped repository")
}

func TestPagerDutyEventTriggerService_UnmappedRepositoryCreatesSkippedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	integrationID := uuid.New()
	trigger := models.AutomationEventTrigger{
		ID:           uuid.New(),
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"]}`),
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		Name:          "Unmapped service incident",
		Goal:          "Investigate.",
		MaxConcurrent: 1,
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: uuid.New()}
	tx := &pagerDutyTxStarterFake{}
	mappings := &pagerDutyServiceMappingStoreFake{}
	integrations := &pagerDutyProviderIntegrationStoreFake{}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	service.SetRepositoryResolver(mappings, integrations)
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: "evt-unmapped",
		EventType:       models.PagerDutyEventIncidentTriggered,
		Incident: models.PagerDutyIncident{
			OrgID:                  orgID,
			PagerDutyIntegrationID: integrationID,
			IncidentID:             "PABC123",
			Title:                  "API latency",
			Status:                 "triggered",
			Urgency:                strPtrPagerDutyEventTest("high"),
			ServiceID:              strPtrPagerDutyEventTest("PSVC"),
		},
	})

	require.NoError(t, err, "TriggerPagerDutyEvent should record unmapped incidents as skipped runs")
	require.Len(t, runs.runs, 1, "unmapped matching event should create a run record")
	require.Equal(t, models.AutomationRunStatusSkipped, runs.runs[0].Status, "unmapped event should be skipped")
	require.NotNil(t, runs.runs[0].ResultSummary, "unmapped skipped run should explain why no session launched")
	require.Contains(t, *runs.runs[0].ResultSummary, "repository_unmapped", "unmapped skipped summary should include stable reason")
	require.Equal(t, uuid.Nil, jobs.enqueuedID, "unmapped event should not enqueue work")
	require.True(t, tx.committed, "unmapped skipped run should commit")
}

func TestPagerDutyEventTriggerService_UsesSharedOrgDefaultRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	integrationID := uuid.New()
	sharedRepoID := uuid.New()
	trigger := models.AutomationEventTrigger{
		ID:           uuid.New(),
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"]}`),
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		Name:          "Shared default incident",
		Goal:          "Investigate.",
		MaxConcurrent: 1,
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: uuid.New()}
	tx := &pagerDutyTxStarterFake{}
	mappings := &pagerDutyServiceMappingStoreFake{}
	integrations := &pagerDutyProviderIntegrationStoreFake{}
	defaults := &pagerDutyDefaultRepositoryLoaderFake{repoID: &sharedRepoID}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	service.SetRepositoryResolver(mappings, integrations)
	service.SetDefaultRepositoryResolver(defaults)
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: "evt-shared-default",
		EventType:       models.PagerDutyEventIncidentTriggered,
		Incident: models.PagerDutyIncident{
			OrgID:                  orgID,
			PagerDutyIntegrationID: integrationID,
			IncidentID:             "PABC123",
			Title:                  "API latency",
			Status:                 "triggered",
			Urgency:                strPtrPagerDutyEventTest("high"),
			ServiceID:              strPtrPagerDutyEventTest("PSVC"),
		},
	})

	require.NoError(t, err, "TriggerPagerDutyEvent should fall back to the shared org default repository")
	require.Equal(t, orgID, defaults.orgID, "shared default lookup should be scoped by org")
	require.Len(t, runs.runs, 1, "matching event should create a run")
	require.JSONEq(t, `{"provider":"pagerduty","event_type":"incident.triggered","provider_event_id":"evt-shared-default","pagerduty_integration_id":"`+integrationID.String()+`","incident_id":"PABC123","incident_status":"triggered","service_id":"PSVC","urgency":"high","repository_id":"`+sharedRepoID.String()+`","repository_source":"org_default"}`, string(runs.runs[0].TriggerContext), "trigger context should capture the shared default repository")
	require.NotEqual(t, uuid.Nil, jobs.enqueuedID, "shared-default event should enqueue work")
}

func TestPagerDutyEventTriggerService_CooldownCreatesSkippedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	triggerID := uuid.New()
	repoID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	trigger := models.AutomationEventTrigger{
		ID:           triggerID,
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentAnnotated)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"],"cooldown_minutes":30}`),
		RepositoryID: &repoID,
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		Name:          "Cooldown incident",
		Goal:          "Investigate.",
		MaxConcurrent: 2,
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true, recentProviderTriggerRuns: 1}
	jobs := &pagerDutyJobStoreFake{jobID: uuid.New()}
	tx := &pagerDutyTxStarterFake{}

	service := NewPagerDutyEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	service.now = func() time.Time { return now }
	err := service.TriggerPagerDutyEvent(ctx, pagerdutysvc.EventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: "evt-cooldown",
		EventType:       models.PagerDutyEventIncidentAnnotated,
		Incident: models.PagerDutyIncident{
			OrgID:      orgID,
			IncidentID: "PABC123",
			Title:      "API latency",
			Status:     "triggered",
			Urgency:    strPtrPagerDutyEventTest("high"),
			ServiceID:  strPtrPagerDutyEventTest("PSVC"),
		},
	})

	require.NoError(t, err, "TriggerPagerDutyEvent should record cooldown skips without failing delivery")
	require.Equal(t, now.Add(-30*time.Minute), runs.recentSince, "cooldown check should use the configured lookback")
	require.Equal(t, triggerID, runs.recentTriggerID, "cooldown check should scope to the matched trigger")
	require.Len(t, runs.runs, 1, "cooldown match should create a visible run record")
	require.Equal(t, models.AutomationRunStatusSkipped, runs.runs[0].Status, "cooldown match should be skipped")
	require.NotNil(t, runs.runs[0].ResultSummary, "cooldown skipped run should include a summary")
	require.Contains(t, *runs.runs[0].ResultSummary, "cooldown", "cooldown summary should include stable reason")
	require.Equal(t, uuid.Nil, jobs.enqueuedID, "cooldown skipped run should not enqueue work")
}

func TestPagerDutyTriggerMatchesFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filter   json.RawMessage
		incident models.PagerDutyIncident
		expected bool
	}{
		{
			name:   "matches service urgency priority and title",
			filter: json.RawMessage(`{"service_ids":["PSVC"],"urgencies":["high"],"priority_names":["P1"],"title_contains":"latency"}`),
			incident: models.PagerDutyIncident{
				IncidentID:   "P1",
				Title:        "API latency",
				ServiceID:    strPtrPagerDutyEventTest("PSVC"),
				Urgency:      strPtrPagerDutyEventTest("high"),
				PriorityName: strPtrPagerDutyEventTest("P1"),
			},
			expected: true,
		},
		{
			name:   "rejects different service",
			filter: json.RawMessage(`{"service_ids":["OTHER"],"urgencies":["high"]}`),
			incident: models.PagerDutyIncident{
				IncidentID: "P1",
				Title:      "API latency",
				ServiceID:  strPtrPagerDutyEventTest("PSVC"),
				Urgency:    strPtrPagerDutyEventTest("high"),
			},
			expected: false,
		},
		{
			name:   "matches custom fields from raw data",
			filter: json.RawMessage(`{"service_ids":["PSVC"],"custom_fields":{"environment":["production"]}}`),
			incident: models.PagerDutyIncident{
				IncidentID: "P1",
				Title:      "API latency",
				ServiceID:  strPtrPagerDutyEventTest("PSVC"),
				RawData:    json.RawMessage(`{"custom_fields":{"environment":"production"}}`),
			},
			expected: true,
		},
		{
			name:   "rejects unmatched custom fields",
			filter: json.RawMessage(`{"service_ids":["PSVC"],"custom_fields":{"environment":["production"]}}`),
			incident: models.PagerDutyIncident{
				IncidentID: "P1",
				Title:      "API latency",
				ServiceID:  strPtrPagerDutyEventTest("PSVC"),
				RawData:    json.RawMessage(`{"custom_fields":{"environment":"staging"}}`),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches, err := pagerDutyTriggerMatches(tt.filter, tt.incident)
			require.NoError(t, err, "pagerDutyTriggerMatches should parse valid filters")
			require.Equal(t, tt.expected, matches, "pagerDutyTriggerMatches should evaluate PagerDuty filters")
		})
	}
}

type pagerDutyEventTriggerStoreFake struct {
	triggers  []models.AutomationEventTrigger
	provider  models.AutomationEventProvider
	eventType string
}

type pagerDutyServiceMappingStoreFake struct {
	mapping models.PagerDutyServiceRepoMapping
}

func (s *pagerDutyServiceMappingStoreFake) GetByServiceID(_ context.Context, _ uuid.UUID, _ uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error) {
	if s.mapping.PagerDutyServiceID != serviceID {
		return models.PagerDutyServiceRepoMapping{}, pgx.ErrNoRows
	}
	return s.mapping, nil
}

type pagerDutyProviderIntegrationStoreFake struct {
	integration models.PagerDutyIntegration
}

func (s *pagerDutyProviderIntegrationStoreFake) GetByID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.integration.ID == uuid.Nil {
		return models.PagerDutyIntegration{}, pgx.ErrNoRows
	}
	return s.integration, nil
}

type pagerDutyCapabilityResolverFake struct {
	input agentcapabilities.ResolveInput
}

func (s *pagerDutyCapabilityResolverFake) ResolveForSession(_ context.Context, in agentcapabilities.ResolveInput) ([]models.AgentCapabilitySnapshotItem, error) {
	s.input = in
	return nil, nil
}

func (s *pagerDutyEventTriggerStoreFake) ListEnabledByProviderEvent(_ context.Context, _ uuid.UUID, provider models.AutomationEventProvider, eventType string) ([]models.AutomationEventTrigger, error) {
	s.provider = provider
	s.eventType = eventType
	return s.triggers, nil
}

type pagerDutyAutomationStoreFake struct {
	automation models.Automation
	inFlight   int
}

func (s *pagerDutyAutomationStoreFake) LockByIDForUpdate(_ context.Context, _ pgx.Tx, _ uuid.UUID, automationID uuid.UUID) (models.Automation, error) {
	if s.automation.ID != automationID {
		return models.Automation{}, errPagerDutyEventTestUnexpectedAutomation
	}
	return s.automation, nil
}

func (s *pagerDutyAutomationStoreFake) CountInFlightRuns(_ context.Context, _ pgx.Tx, _ uuid.UUID, _ uuid.UUID) (int, error) {
	return s.inFlight, nil
}

type pagerDutyAutomationRunStoreFake struct {
	runs                      []models.AutomationRun
	created                   bool
	recentProviderTriggerRuns int
	recentOrgID               uuid.UUID
	recentAutomationID        uuid.UUID
	recentTriggerID           uuid.UUID
	recentProvider            models.AutomationEventProvider
	recentSince               time.Time
}

func (s *pagerDutyAutomationRunStoreFake) CreateRunInTx(_ context.Context, _ pgx.Tx, run *models.AutomationRun) (bool, error) {
	run.ID = uuid.New()
	s.runs = append(s.runs, *run)
	return s.created, nil
}

func (s *pagerDutyAutomationRunStoreFake) CountRecentProviderTriggerRuns(_ context.Context, _ pgx.Tx, orgID, automationID, triggerID uuid.UUID, provider models.AutomationEventProvider, since time.Time) (int, error) {
	s.recentOrgID = orgID
	s.recentAutomationID = automationID
	s.recentTriggerID = triggerID
	s.recentProvider = provider
	s.recentSince = since
	return s.recentProviderTriggerRuns, nil
}

type pagerDutyDefaultRepositoryLoaderFake struct {
	orgID  uuid.UUID
	repoID *uuid.UUID
	err    error
}

func (s *pagerDutyDefaultRepositoryLoaderFake) LoadDefaultWorkRepositoryID(_ context.Context, orgID uuid.UUID) (*uuid.UUID, error) {
	s.orgID = orgID
	if s.err != nil {
		return nil, s.err
	}
	return s.repoID, nil
}

type pagerDutyJobStoreFake struct {
	jobID      uuid.UUID
	enqueuedID uuid.UUID
	notifiedID uuid.UUID
}

func (s *pagerDutyJobStoreFake) EnqueueInTx(_ context.Context, _ pgx.Tx, _ uuid.UUID, _ string, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
	s.enqueuedID = s.jobID
	return s.jobID, nil
}

func (s *pagerDutyJobStoreFake) Notify(_ context.Context, jobID uuid.UUID) {
	s.notifiedID = jobID
}

type pagerDutyEventAuditFake struct {
	params db.WebhookActionParams
}

func (s *pagerDutyEventAuditFake) EmitWebhookAction(_ context.Context, params db.WebhookActionParams) {
	s.params = params
}

func (s *pagerDutyEventAuditFake) actorType() models.AuditActorType {
	if s.params.ProviderName == "" {
		return ""
	}
	return models.AuditActorWebhook
}

type pagerDutyTxStarterFake struct {
	committed bool
}

func (s *pagerDutyTxStarterFake) Begin(_ context.Context) (pgx.Tx, error) {
	return &pagerDutyTxFake{starter: s}, nil
}

type pagerDutyTxFake struct {
	starter *pagerDutyTxStarterFake
}

func (tx *pagerDutyTxFake) Begin(context.Context) (pgx.Tx, error) { return tx, nil }
func (tx *pagerDutyTxFake) Commit(context.Context) error {
	tx.starter.committed = true
	return nil
}
func (tx *pagerDutyTxFake) Rollback(context.Context) error { return nil }
func (tx *pagerDutyTxFake) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errPagerDutyEventTestUnexpectedAutomation
}
func (tx *pagerDutyTxFake) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (tx *pagerDutyTxFake) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (tx *pagerDutyTxFake) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errPagerDutyEventTestUnexpectedAutomation
}
func (tx *pagerDutyTxFake) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errPagerDutyEventTestUnexpectedAutomation
}
func (tx *pagerDutyTxFake) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errPagerDutyEventTestUnexpectedAutomation
}
func (tx *pagerDutyTxFake) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (tx *pagerDutyTxFake) Conn() *pgx.Conn                                  { return nil }

type pagerDutyEventTestError struct{}

func (pagerDutyEventTestError) Error() string { return "unexpected pagerduty event test call" }

var errPagerDutyEventTestUnexpectedAutomation = pagerDutyEventTestError{}

func strPtrPagerDutyEventTest(v string) *string {
	return &v
}

func testLoggerPagerDutyEvents() zerolog.Logger {
	return zerolog.Nop()
}
