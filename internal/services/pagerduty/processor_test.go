package pagerduty

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProcessor_ProcessInboundEventUpsertsIssueIncidentAndTriggersAutomations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	webhookIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	issueID := uuid.New()
	occurredAt := time.Date(2026, 6, 19, 12, 34, 56, 0, time.UTC)
	payload := json.RawMessage(`{
		"event": {
			"id": "evt-1",
			"event_type": "incident.triggered",
			"resource_type": "incident",
			"occurred_at": "2026-06-19T12:34:56Z",
			"data": {
				"id": "PABC123",
				"title": "API latency",
				"status": "triggered",
				"urgency": "high",
				"priority": {"id": "P1", "summary": "P1"},
				"service": {"id": "PSVC", "summary": "api"}
			}
		}
	}`)

	events := &processorInboundEventStore{
		event: models.PagerDutyInboundEvent{
			ID:                     eventID,
			OrgID:                  orgID,
			PagerDutyIntegrationID: &pagerDutyIntegrationID,
			ProviderEventID:        "evt-1",
			EventType:              models.PagerDutyEventIncidentTriggered,
			Payload:                payload,
			Status:                 "received",
		},
	}
	integrations := &processorIntegrationStore{
		integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &webhookIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		},
	}
	ingester := &processorIssueIngester{
		issue: &models.Issue{ID: issueID, OrgID: orgID, ExternalID: "PABC123", Source: models.IssueSourcePagerDuty},
	}
	issueStatus := &processorIssueStatusUpdater{}
	incidents := &processorIncidentStore{}
	triggers := &processorEventTriggerer{}

	processor := NewProcessor(ProcessorDeps{
		Events:       events,
		Integrations: integrations,
		Ingester:     ingester,
		Issues:       issueStatus,
		Incidents:    incidents,
		Triggers:     triggers,
	})
	err := processor.ProcessInboundEvent(ctx, orgID, eventID)
	require.NoError(t, err, "ProcessInboundEvent should process a valid PagerDuty incident event")
	require.Equal(t, eventID, events.processedID, "processor should mark the inbound event processed")
	require.Equal(t, "PABC123", ingester.normalized.ExternalID, "processor should ingest a normalized PagerDuty issue")
	require.Equal(t, models.IssueStatusOpen, issueStatus.status, "processor should mirror active PagerDuty status to the issue")
	require.Equal(t, issueID, *incidents.incident.IssueID, "processor should link the incident mirror to the normalized issue")
	require.Equal(t, &occurredAt, incidents.incident.TriggeredAt, "processor should preserve incident timing on the mirror")
	require.Equal(t, "evt-1", triggers.req.ProviderEventID, "processor should pass the provider event id to automation trigger matching")
	require.Equal(t, "PABC123", triggers.req.Incident.IncidentID, "processor should pass incident context to automation trigger matching")
}

type processorInboundEventStore struct {
	event       models.PagerDutyInboundEvent
	processedID uuid.UUID
	failedID    uuid.UUID
}

func (s *processorInboundEventStore) GetByID(_ context.Context, orgID, eventID uuid.UUID) (models.PagerDutyInboundEvent, error) {
	if s.event.OrgID != orgID || s.event.ID != eventID {
		return models.PagerDutyInboundEvent{}, errProcessorTestUnexpectedLookup
	}
	return s.event, nil
}

func (s *processorInboundEventStore) MarkProcessed(_ context.Context, _ uuid.UUID, eventID uuid.UUID) error {
	s.processedID = eventID
	return nil
}

func (s *processorInboundEventStore) MarkFailed(_ context.Context, _ uuid.UUID, eventID uuid.UUID, _ string) error {
	s.failedID = eventID
	return nil
}

type processorIntegrationStore struct {
	integration models.PagerDutyIntegration
}

func (s *processorIntegrationStore) GetByID(_ context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.integration.OrgID != orgID || s.integration.ID != id {
		return models.PagerDutyIntegration{}, errProcessorTestUnexpectedLookup
	}
	return s.integration, nil
}

type processorIssueIngester struct {
	normalized ingestion.NormalizedIssue
	issue      *models.Issue
}

func (s *processorIssueIngester) IngestNormalized(_ context.Context, _ uuid.UUID, issue ingestion.NormalizedIssue) (*models.Issue, error) {
	s.normalized = issue
	return s.issue, nil
}

type processorIssueStatusUpdater struct {
	status models.IssueStatus
}

func (s *processorIssueStatusUpdater) UpdateStatus(_ context.Context, _ uuid.UUID, _ uuid.UUID, status models.IssueStatus) error {
	s.status = status
	return nil
}

type processorIncidentStore struct {
	incident models.PagerDutyIncident
}

func (s *processorIncidentStore) Upsert(_ context.Context, incident *models.PagerDutyIncident) error {
	s.incident = *incident
	return nil
}

type processorEventTriggerer struct {
	req EventTriggerRequest
}

func (s *processorEventTriggerer) TriggerPagerDutyEvent(_ context.Context, req EventTriggerRequest) error {
	s.req = req
	return nil
}

var errProcessorTestUnexpectedLookup = errUnexpectedProcessorTestLookup{}

type errUnexpectedProcessorTestLookup struct{}

func (errUnexpectedProcessorTestLookup) Error() string { return "unexpected processor test lookup" }
