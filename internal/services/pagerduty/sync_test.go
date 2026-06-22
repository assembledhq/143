package pagerduty

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSyncer_SyncOrgIngestsPolledIncidents(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	genericIntegrationID := uuid.New()
	credentialID := uuid.New()
	issueID := uuid.New()
	lastSyncedAt := time.Date(2026, 6, 19, 17, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 19, 18, 0, 0, 0, time.UTC)
	serviceID := "P123"
	serviceName := "Checkout"
	urgency := "high"
	updatedAt := time.Date(2026, 6, 19, 17, 45, 0, 0, time.UTC)
	raw, err := json.Marshal(map[string]any{
		"id":              "PINCIDENT",
		"incident_number": 42,
		"title":           "checkout degraded",
		"status":          "triggered",
		"urgency":         urgency,
		"service": map[string]any{
			"id":      serviceID,
			"summary": serviceName,
		},
	})
	require.NoError(t, err, "test raw incident should marshal")

	integrations := &syncIntegrationStoreFake{
		integrations: []models.PagerDutyIntegration{
			{
				ID:            pagerDutyIntegrationID,
				OrgID:         orgID,
				IntegrationID: &genericIntegrationID,
				CredentialRef: "org_credential:" + credentialID.String(),
				Status:        models.PagerDutyIntegrationStatusActive,
				LastSyncedAt:  &lastSyncedAt,
			},
		},
	}
	credentials := &syncCredentialReaderFake{
		credential: &models.DecryptedCredential{
			Config: models.PagerDutyConfig{AccessToken: "token", TokenType: "Bearer"},
		},
	}
	client := &syncIncidentClientFake{
		incidents: []Incident{
			{
				ID:             "PINCIDENT",
				IncidentNumber: int64PtrSyncTest(42),
				Title:          "checkout degraded",
				Status:         "triggered",
				Urgency:        &urgency,
				ServiceID:      &serviceID,
				ServiceName:    &serviceName,
				CreatedAt:      &lastSyncedAt,
				UpdatedAt:      &updatedAt,
				RawData:        raw,
			},
		},
	}
	ingester := &syncIssueIngesterFake{issueID: issueID}
	issues := &syncIssueStatusUpdaterFake{}
	incidents := &syncIncidentStoreFake{}

	syncer := NewSyncer(SyncerDeps{
		Integrations: integrations,
		Credentials:  credentials,
		Client:       client,
		Ingester:     ingester,
		Issues:       issues,
		Incidents:    incidents,
		Now:          func() time.Time { return now },
	})

	result, err := syncer.SyncOrg(context.Background(), orgID)
	require.NoError(t, err, "SyncOrg should reconcile active PagerDuty integrations")
	require.Equal(t, SyncResult{IntegrationCount: 1, IncidentCount: 1}, result, "SyncOrg should report reconciled integrations and incidents")
	require.Equal(t, credentialID, credentials.credentialID, "SyncOrg should resolve the credential from the integration reference")
	require.Equal(t, IncidentListRequest{Since: lastSyncedAt, Limit: defaultSyncIncidentLimit}, client.request, "SyncOrg should fetch incidents updated since the integration watermark")
	require.Equal(t, "PINCIDENT", ingester.issue.ExternalID, "SyncOrg should dedupe normalized issues by PagerDuty incident id")
	require.Equal(t, models.IssueSourcePagerDuty, ingester.issue.Source, "SyncOrg should use the PagerDuty issue source")
	require.Equal(t, models.IssueStatusOpen, issues.status, "SyncOrg should mirror triggered incidents as open issues")
	require.Equal(t, issueID, *incidents.incident.IssueID, "SyncOrg should link the mirrored incident to the normalized issue")
	require.Equal(t, serviceID, *incidents.incident.ServiceID, "SyncOrg should preserve service metadata")
	require.Equal(t, now, integrations.lastSyncedAt, "SyncOrg should advance the integration sync watermark after a successful poll")
}

type syncIntegrationStoreFake struct {
	integrations []models.PagerDutyIntegration
	lastSyncedAt time.Time
}

func (s *syncIntegrationStoreFake) ListActive(_ context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error) {
	for _, integration := range s.integrations {
		if integration.OrgID != orgID {
			return nil, errUnexpectedPagerDutySyncTestLookup
		}
	}
	return s.integrations, nil
}

func (s *syncIntegrationStoreFake) UpdateLastSyncedAt(_ context.Context, orgID, id uuid.UUID, syncedAt time.Time) error {
	for _, integration := range s.integrations {
		if integration.OrgID == orgID && integration.ID == id {
			s.lastSyncedAt = syncedAt
			return nil
		}
	}
	return errUnexpectedPagerDutySyncTestLookup
}

func (s *syncIntegrationStoreFake) UpdateStatus(_ context.Context, orgID, id uuid.UUID, _ models.PagerDutyIntegrationStatus, _ *string) error {
	for _, integration := range s.integrations {
		if integration.OrgID == orgID && integration.ID == id {
			return nil
		}
	}
	return errUnexpectedPagerDutySyncTestLookup
}

type syncCredentialReaderFake struct {
	credential   *models.DecryptedCredential
	credentialID uuid.UUID
}

func (s *syncCredentialReaderFake) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error) {
	s.credentialID = id
	return s.credential, nil
}

type syncIncidentClientFake struct {
	request   IncidentListRequest
	incidents []Incident
}

func (s *syncIncidentClientFake) ListIncidents(_ context.Context, _ models.PagerDutyConfig, req IncidentListRequest) ([]Incident, error) {
	s.request = req
	return s.incidents, nil
}

type syncIssueIngesterFake struct {
	issueID uuid.UUID
	issue   ingestion.NormalizedIssue
}

func (s *syncIssueIngesterFake) IngestNormalized(_ context.Context, _ uuid.UUID, issue ingestion.NormalizedIssue) (*models.Issue, error) {
	s.issue = issue
	return &models.Issue{ID: s.issueID}, nil
}

type syncIssueStatusUpdaterFake struct {
	status models.IssueStatus
}

func (s *syncIssueStatusUpdaterFake) UpdateStatus(_ context.Context, _ uuid.UUID, _ uuid.UUID, status models.IssueStatus) error {
	s.status = status
	return nil
}

type syncIncidentStoreFake struct {
	incident models.PagerDutyIncident
}

func (s *syncIncidentStoreFake) Upsert(_ context.Context, incident *models.PagerDutyIncident) error {
	s.incident = *incident
	return nil
}

func int64PtrSyncTest(v int64) *int64 {
	return &v
}

var errUnexpectedPagerDutySyncTestLookup = errors.New("unexpected PagerDuty sync test lookup")
