package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPagerDutyIngestEventHandlerProcessesPayload(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	eventID := uuid.New()
	processor := &pagerDutyProcessorFake{}
	handler := newPagerDutyIngestEventHandler(processor, zerolog.Nop())
	payload, err := json.Marshal(map[string]string{
		"org_id":   orgID.String(),
		"event_id": eventID.String(),
	})
	require.NoError(t, err, "test payload should marshal")

	err = handler(context.Background(), models.JobTypePagerDutyIngestEvent, payload)
	require.NoError(t, err, "PagerDuty ingest handler should process valid payload")
	require.Equal(t, orgID, processor.orgID, "handler should pass org id to processor")
	require.Equal(t, eventID, processor.eventID, "handler should pass event id to processor")
}

func TestPagerDutySyncHandlerProcessesOrgPayload(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	syncer := &pagerDutySyncerFake{}
	handler := newPagerDutySyncHandler(syncer, zerolog.Nop())
	payload, err := json.Marshal(map[string]string{
		"org_id": orgID.String(),
	})
	require.NoError(t, err, "test payload should marshal")

	err = handler(context.Background(), models.JobTypePagerDutySync, payload)
	require.NoError(t, err, "PagerDuty sync handler should process valid payload")
	require.Equal(t, orgID, syncer.orgID, "handler should pass org id to syncer")
}

type pagerDutyProcessorFake struct {
	orgID   uuid.UUID
	eventID uuid.UUID
}

func (p *pagerDutyProcessorFake) ProcessInboundEvent(_ context.Context, orgID, eventID uuid.UUID) error {
	p.orgID = orgID
	p.eventID = eventID
	return nil
}

type pagerDutySyncerFake struct {
	orgID uuid.UUID
}

func (p *pagerDutySyncerFake) SyncOrg(_ context.Context, orgID uuid.UUID) (pagerdutysvc.SyncResult, error) {
	p.orgID = orgID
	return pagerdutysvc.SyncResult{IntegrationCount: 1, IncidentCount: 2}, nil
}
