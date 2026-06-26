package automations

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLinearEventTriggerService_TriggerLinearIssueEventCreatesRunForMatchingTrigger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	triggerID := uuid.New()
	repositoryID := uuid.New()
	jobID := uuid.New()
	provider := models.AutomationEventProviderLinear
	providerEventID := "linear-delivery-1"
	occurredAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	trigger := models.AutomationEventTrigger{
		ID:           triggerID,
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderLinear,
		EventTypes:   []string{string(models.LinearAutomationEventIssueCreated)},
		Filter:       json.RawMessage(`{"team_keys":["ENG"],"labels":["bug"],"priorities":["urgent"],"state_types":["triage"],"issue_types":["bug"]}`),
		RepositoryID: &repositoryID,
		Enabled:      true,
	}
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		RepositoryID:  &repositoryID,
		Name:          "Linear bug triage",
		Goal:          "Fix new Linear bugs when safe.",
		MaxConcurrent: 1,
		BaseBranch:    "main",
		Enabled:       true,
	}
	triggers := &pagerDutyEventTriggerStoreFake{triggers: []models.AutomationEventTrigger{trigger}}
	automations := &pagerDutyAutomationStoreFake{automation: automation}
	runs := &pagerDutyAutomationRunStoreFake{created: true}
	jobs := &pagerDutyJobStoreFake{jobID: jobID}
	tx := &pagerDutyTxStarterFake{}

	service := NewLinearEventTriggerService(triggers, automations, runs, jobs, tx, testLoggerPagerDutyEvents())
	err := service.TriggerLinearIssueEvent(ctx, LinearIssueEventTriggerRequest{
		OrgID:           orgID,
		ProviderEventID: providerEventID,
		EventType:       models.LinearAutomationEventIssueCreated,
		OccurredAt:      &occurredAt,
		Issue: LinearIssueEvent{
			ID:           "lin-issue-1",
			Identifier:   "ENG-123",
			Title:        "Checkout button fails",
			URL:          "https://linear.app/acme/issue/ENG-123",
			Description:  "Clicking checkout fails.",
			Priority:     1,
			PriorityName: "urgent",
			StateName:    "Triage",
			StateType:    "triage",
			TeamKey:      "ENG",
			TeamName:     "Engineering",
			Labels:       []string{"bug", "checkout"},
			IssueType:    "bug",
		},
	})

	require.NoError(t, err, "TriggerLinearIssueEvent should create and enqueue matching automation runs")
	require.Equal(t, models.AutomationEventProviderLinear, triggers.provider, "trigger listing should scope to Linear provider")
	require.Equal(t, string(models.LinearAutomationEventIssueCreated), triggers.eventType, "trigger listing should scope to Linear issue-created events")
	require.Len(t, runs.runs, 1, "matching Linear event should create exactly one automation run")
	run := runs.runs[0]
	require.Equal(t, models.AutomationTriggeredByProviderEvent, run.TriggeredBy, "run should record provider-event trigger source")
	require.Equal(t, models.AutomationRunStatusPending, run.Status, "matching Linear event should create a pending run")
	require.Equal(t, &triggerID, run.TriggerID, "run should preserve the trigger id")
	require.Equal(t, &provider, run.Provider, "run should preserve provider metadata")
	require.Equal(t, &providerEventID, run.ProviderEventID, "run should preserve provider event id for idempotency")
	require.Contains(t, run.GoalSnapshot, "Linear issue", "goal snapshot should include structured Linear context")
	require.Contains(t, run.GoalSnapshot, "ENG-123", "goal snapshot should include the Linear identifier")
	require.JSONEq(t, `{"provider":"linear","event_type":"issue.created","provider_event_id":"linear-delivery-1","issue_id":"lin-issue-1","identifier":"ENG-123","title":"Checkout button fails","url":"https://linear.app/acme/issue/ENG-123","team_key":"ENG","team_name":"Engineering","state_name":"Triage","state_type":"triage","priority":"urgent","priority_number":1,"labels":["bug","checkout"],"issue_type":"bug","occurred_at":"2026-06-25T12:00:00Z","repository_id":"`+repositoryID.String()+`","repository_source":"trigger"}`, string(run.TriggerContext), "trigger context should capture compact Linear metadata")
	require.Equal(t, jobID, jobs.notifiedID, "created pending run should notify the queued automation job")
	require.True(t, tx.committed, "event-trigger run creation should commit its transaction")
}

func TestLinearTriggerMatchesFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filter   json.RawMessage
		issue    LinearIssueEvent
		expected bool
	}{
		{
			name:   "matches team labels state priority and issue type",
			filter: json.RawMessage(`{"team_keys":["ENG"],"labels":["bug"],"state_types":["triage"],"priorities":["urgent"],"issue_types":["bug"]}`),
			issue: LinearIssueEvent{
				TeamKey:      "ENG",
				Labels:       []string{"bug", "checkout"},
				StateType:    "triage",
				PriorityName: "urgent",
				IssueType:    "bug",
			},
			expected: true,
		},
		{
			name:   "rejects missing label",
			filter: json.RawMessage(`{"labels":["bug"]}`),
			issue:  LinearIssueEvent{Labels: []string{"feature"}},
		},
		{
			name:     "matches title text case-insensitively",
			filter:   json.RawMessage(`{"title_contains":"checkout"}`),
			issue:    LinearIssueEvent{Title: "Checkout button fails"},
			expected: true,
		},
		{
			name:   "rejects different issue type",
			filter: json.RawMessage(`{"issue_types":["bug"]}`),
			issue:  LinearIssueEvent{IssueType: "feature"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches, err := linearTriggerMatches(tt.filter, tt.issue)
			require.NoError(t, err, "linearTriggerMatches should parse valid filters")
			require.Equal(t, tt.expected, matches, "linearTriggerMatches should evaluate Linear filters")
		})
	}
}
