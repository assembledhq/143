package pagerduty

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
)

type inboundEventStore interface {
	GetByID(ctx context.Context, orgID, eventID uuid.UUID) (models.PagerDutyInboundEvent, error)
	MarkProcessed(ctx context.Context, orgID, eventID uuid.UUID) error
	MarkFailed(ctx context.Context, orgID, eventID uuid.UUID, message string) error
}

type integrationStore interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error)
}

type issueIngester interface {
	IngestNormalized(ctx context.Context, orgID uuid.UUID, issue ingestion.NormalizedIssue) (*models.Issue, error)
}

type issueStatusUpdater interface {
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error
}

type incidentStore interface {
	Upsert(ctx context.Context, incident *models.PagerDutyIncident) error
}

type eventTriggerer interface {
	TriggerPagerDutyEvent(ctx context.Context, req EventTriggerRequest) error
}

type ProcessorDeps struct {
	Events       inboundEventStore
	Integrations integrationStore
	Ingester     issueIngester
	Issues       issueStatusUpdater
	Incidents    incidentStore
	Triggers     eventTriggerer
	Metrics      *metrics.PagerDutyMetrics
}

type Processor struct {
	deps ProcessorDeps
}

type EventTriggerRequest struct {
	OrgID           uuid.UUID
	PagerDuty       models.PagerDutyIntegration
	ProviderEventID string
	EventType       models.PagerDutyEventType
	ResourceType    *string
	OccurredAt      *time.Time
	Incident        models.PagerDutyIncident
	Issue           models.Issue
	Normalized      NormalizedEvent
}

func NewProcessor(deps ProcessorDeps) *Processor {
	return &Processor{deps: deps}
}

func (p *Processor) ProcessInboundEvent(ctx context.Context, orgID, eventID uuid.UUID) error {
	var pagerDutyMetrics *metrics.PagerDutyMetrics
	if p != nil {
		pagerDutyMetrics = p.deps.Metrics
	}
	result := "failed"
	defer func() {
		pagerDutyMetrics.RecordIngestJob(ctx, result)
	}()

	if p == nil {
		result = "noop"
		return nil
	}
	if p.deps.Events == nil || p.deps.Integrations == nil || p.deps.Ingester == nil || p.deps.Issues == nil || p.deps.Incidents == nil {
		return errors.New("pagerduty processor dependencies are incomplete")
	}

	event, err := p.deps.Events.GetByID(ctx, orgID, eventID)
	if err != nil {
		return fmt.Errorf("load pagerduty inbound event: %w", err)
	}
	if event.Status == "processed" {
		result = "already_processed"
		return nil
	}

	if event.PagerDutyIntegrationID == nil || *event.PagerDutyIntegrationID == uuid.Nil {
		return p.failEvent(ctx, orgID, eventID, errors.New("pagerduty inbound event is missing integration id"))
	}

	integration, err := p.deps.Integrations.GetByID(ctx, orgID, *event.PagerDutyIntegrationID)
	if err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("load pagerduty integration: %w", err))
	}
	parsed, err := ParseEvent(event.Payload)
	if err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("parse pagerduty event: %w", err))
	}
	if parsed.ProviderEventID == "" {
		parsed.ProviderEventID = event.ProviderEventID
	}
	normalized, err := NormalizeEvent(orgID, integration, parsed)
	if err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("normalize pagerduty event: %w", err))
	}

	issue, err := p.deps.Ingester.IngestNormalized(ctx, orgID, normalized.Issue)
	if err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("ingest pagerduty issue: %w", err))
	}
	if issue == nil || issue.ID == uuid.Nil {
		return p.failEvent(ctx, orgID, eventID, errors.New("ingest pagerduty issue returned no issue id"))
	}

	// Upsert the incident first so its row reflects the authoritative,
	// recency-guarded status (the DB upsert only advances latest-state columns
	// for events that are at least as recent as what is stored). We then derive
	// the issue status from that merged incident status rather than from this
	// event in isolation — otherwise a delayed/redelivered incident.triggered
	// arriving after incident.resolved would reopen an already-fixed issue.
	normalized.Incident.IssueID = &issue.ID
	if err := p.deps.Incidents.Upsert(ctx, &normalized.Incident); err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("upsert pagerduty incident: %w", err))
	}

	issueStatus := IssueStatusForIncidentStatus(normalized.Incident.Status)
	if err := p.deps.Issues.UpdateStatus(ctx, orgID, issue.ID, issueStatus); err != nil {
		return p.failEvent(ctx, orgID, eventID, fmt.Errorf("update pagerduty issue status: %w", err))
	}

	if p.deps.Triggers != nil && eventCanTriggerAutomations(parsed.EventType) {
		if err := p.deps.Triggers.TriggerPagerDutyEvent(ctx, EventTriggerRequest{
			OrgID:           orgID,
			PagerDuty:       integration,
			ProviderEventID: parsed.ProviderEventID,
			EventType:       parsed.EventType,
			ResourceType:    parsed.ResourceType,
			OccurredAt:      parsed.OccurredAt,
			Incident:        normalized.Incident,
			Issue:           *issue,
			Normalized:      normalized,
		}); err != nil {
			return p.failEvent(ctx, orgID, eventID, fmt.Errorf("trigger pagerduty automations: %w", err))
		}
	}

	if err := p.deps.Events.MarkProcessed(ctx, orgID, eventID); err != nil {
		return fmt.Errorf("mark pagerduty inbound event processed: %w", err)
	}
	result = "processed"
	return nil
}

func (p *Processor) failEvent(ctx context.Context, orgID, eventID uuid.UUID, err error) error {
	if p.deps.Events != nil {
		if markErr := p.deps.Events.MarkFailed(ctx, orgID, eventID, err.Error()); markErr != nil {
			return fmt.Errorf("%w; additionally failed to mark pagerduty inbound event failed: %v", err, markErr)
		}
	}
	return err
}
