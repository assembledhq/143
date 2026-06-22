package pagerduty

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseEventEnvelopeAndNormalizeIncident(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	occurredAt := time.Date(2026, 6, 19, 12, 34, 56, 0, time.UTC)
	account := "acme"
	payload := json.RawMessage(`{
		"event": {
			"id": "evt-1",
			"event_type": "incident.triggered",
			"resource_type": "incident",
			"occurred_at": "2026-06-19T12:34:56Z",
			"data": {
				"id": "PABC123",
				"incident_number": 42,
				"html_url": "https://acme.pagerduty.com/incidents/PABC123",
				"title": "API latency",
				"summary": "API latency on checkout",
				"status": "triggered",
				"urgency": "high",
				"priority": {"id": "P1", "summary": "P1"},
				"service": {"id": "PSVC", "summary": "api"},
				"escalation_policy": {"id": "PEP", "summary": "Core Platform"},
				"teams": [{"id": "PT1", "summary": "Platform"}],
				"assignments": [{"assignee": {"id": "PU1", "summary": "Ada"}}],
				"incident_type": {"name": "security_incident"},
				"note": {"content": "Investigating elevated latency"}
			}
		}
	}`)

	parsed, err := ParseEvent(payload)
	require.NoError(t, err, "ParseEvent should decode PagerDuty v3 event envelopes")
	require.Equal(t, "evt-1", parsed.ProviderEventID, "ParseEvent should capture the provider event id")
	require.Equal(t, models.PagerDutyEventIncidentTriggered, parsed.EventType, "ParseEvent should preserve the event type")
	require.Equal(t, "PABC123", parsed.Incident.ID, "ParseEvent should extract the incident id")
	require.Equal(t, &occurredAt, parsed.OccurredAt, "ParseEvent should parse occurred_at")

	normalized, err := NormalizeEvent(orgID, models.PagerDutyIntegration{
		ID:               pagerDutyIntegrationID,
		OrgID:            orgID,
		IntegrationID:    &genericIntegrationID,
		AccountSubdomain: &account,
	}, parsed)
	require.NoError(t, err, "NormalizeEvent should turn parsed incident data into 143 models")
	require.Equal(t, "PABC123", normalized.Issue.ExternalID, "normalized issue should dedupe by PagerDuty incident id")
	require.Equal(t, models.IssueSourcePagerDuty, normalized.Issue.Source, "normalized issue should use the PagerDuty source")
	require.Equal(t, genericIntegrationID, normalized.Issue.SourceIntegrationID, "normalized issue should link to the generic integration row")
	require.Equal(t, "critical", normalized.Issue.Severity, "P1 high-urgency incidents should normalize as critical")
	require.Equal(t, models.IssueStatusOpen, normalized.IssueStatus, "triggered incidents should keep the 143 issue open")
	require.Contains(t, normalized.Issue.Description, "Investigating elevated latency", "normalized issue should include latest note context")
	require.ElementsMatch(t, []string{
		"pagerduty", "pagerduty_service:api", "pagerduty_service_id:PSVC",
		"pagerduty_priority:P1", "pagerduty_urgency:high", "pagerduty_team_id:PT1",
		"pagerduty_escalation_policy:Core Platform", "pagerduty_incident_type:security_incident",
	}, normalized.Issue.Tags, "normalized issue should include useful PagerDuty routing tags")
	require.Equal(t, pagerDutyIntegrationID, normalized.Incident.PagerDutyIntegrationID, "incident mirror should link to provider integration")
	require.Equal(t, int64(42), *normalized.Incident.IncidentNumber, "incident mirror should preserve incident number")
	require.Equal(t, &occurredAt, normalized.Incident.TriggeredAt, "incident mirror should set triggered_at from the event time")
}

func TestParseEventNormalizesResourceActionEventType(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{
		"event": {
			"id": "evt-1",
			"resource_type": "incident",
			"action": "triggered",
			"data": {
				"id": "PABC123",
				"title": "API latency",
				"status": "triggered"
			}
		}
	}`)

	parsed, err := ParseEvent(payload)

	require.NoError(t, err, "ParseEvent should accept resource/action PagerDuty event shapes")
	require.Equal(t, models.PagerDutyEventIncidentTriggered, parsed.EventType, "ParseEvent should normalize incident action into event type")
	require.Equal(t, "PABC123", parsed.Incident.ID, "ParseEvent should parse incident data")
}

func TestParseEventSanitizesRawPayloads(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{
		"headers": {"Authorization": "Bearer secret-token"},
		"event": {
			"id": "evt-1",
			"event_type": "incident.triggered",
			"data": {
				"id": "PABC123",
				"title": "API latency",
				"status": "triggered",
				"responder": {
					"email": "alice@example.com",
					"phone_number": "+15555550123",
					"contact_methods": [{"address": "alice@example.com"}]
				},
				"custom_details": {
					"api_token": "pd-token-secret",
					"safe_field": "kept"
				}
			}
		}
	}`)

	parsed, err := ParseEvent(payload)

	require.NoError(t, err, "ParseEvent should parse payloads before sanitizing raw copies")
	require.NotContains(t, string(parsed.RawPayload), "secret-token", "raw payload should redact authorization headers")
	require.NotContains(t, string(parsed.RawPayload), "alice@example.com", "raw payload should redact responder contact methods")
	require.NotContains(t, string(parsed.Incident.RawData), "pd-token-secret", "incident raw data should redact token-like fields")
	require.Contains(t, string(parsed.Incident.RawData), "safe_field", "sanitization should preserve non-sensitive context")
}

func TestNormalizeResolvedIncidentClosesIssue(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	occurredAt := time.Date(2026, 6, 19, 13, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{
		"event": {
			"id": "evt-2",
			"event_type": "incident.resolved",
			"resource_type": "incident",
			"occurred_at": "2026-06-19T13:00:00Z",
			"data": {
				"id": "PABC123",
				"title": "API latency",
				"status": "resolved",
				"urgency": "low",
				"service": {"id": "PSVC", "summary": "api"}
			}
		}
	}`)

	parsed, err := ParseEvent(payload)
	require.NoError(t, err, "ParseEvent should decode resolved incident events")
	normalized, err := NormalizeEvent(orgID, models.PagerDutyIntegration{
		ID:            pagerDutyIntegrationID,
		OrgID:         orgID,
		IntegrationID: &genericIntegrationID,
	}, parsed)
	require.NoError(t, err, "NormalizeEvent should handle resolved incidents")
	require.Equal(t, models.IssueStatusFixed, normalized.IssueStatus, "resolved PagerDuty incidents should close the 143 issue")
	require.Equal(t, &occurredAt, normalized.Incident.ResolvedAt, "incident mirror should set resolved_at from the event time")
	require.Equal(t, "low", normalized.Issue.Severity, "low urgency without a priority should normalize as low severity")
}

func TestEventCanTriggerAutomations(t *testing.T) {
	t.Parallel()
	humanNote := "Investigating elevated error rate"
	writebackNote := PagerDutyWritebackNotePrefix + "session started for PagerDuty incident PABC."

	// Non-annotation events always trigger.
	if !eventCanTriggerAutomations(models.PagerDutyEventIncidentTriggered, nil) {
		t.Fatal("triggered events should always be allowed to trigger automations")
	}
	// Human annotation triggers; 143-authored annotation does not (loop break).
	if !eventCanTriggerAutomations(models.PagerDutyEventIncidentAnnotated, &humanNote) {
		t.Fatal("a human annotation should be allowed to trigger automations")
	}
	if eventCanTriggerAutomations(models.PagerDutyEventIncidentAnnotated, &writebackNote) {
		t.Fatal("a 143-authored annotation must not trigger automations")
	}
	// Safe default: an annotation with no resolvable note is treated as ours.
	if eventCanTriggerAutomations(models.PagerDutyEventIncidentStatusUpdatePublished, nil) {
		t.Fatal("a status update with no note should be blocked (safe default, no loop)")
	}
}

func TestIsWritebackAuthoredNote(t *testing.T) {
	t.Parallel()
	if !IsWritebackAuthoredNote("  " + PagerDutyWritebackNotePrefix + "opened a pull request...") {
		t.Fatal("a 143-prefixed note should be recognized as writeback-authored")
	}
	if IsWritebackAuthoredNote("143-incidents are noisy today") {
		t.Fatal("a note that merely contains 143 should not be misclassified")
	}
}
