package pagerduty

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWritebackService_OnSessionStartedAddsNoteWhenEnabled(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
		Title:                  "checkout degraded",
		IssueID:                &issueID,
	}
	integrations := &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
		ID:               integrationID,
		OrgID:            orgID,
		CredentialRef:    "org_credential:" + credentialID.String(),
		WritebackEnabled: true,
		Status:           models.PagerDutyIntegrationStatusActive,
	}}
	credentials := &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}}
	client := &writebackClientFake{noteID: "note-123"}
	providerState := &writebackProviderStateFake{}
	service := NewWritebackService(WritebackDeps{
		Integrations:  integrations,
		Credentials:   credentials,
		Client:        client,
		ProviderState: providerState,
		FrontendURL:   "https://app.example.com",
	})

	err := service.OnSessionStarted(context.Background(), orgID, incident, models.Session{ID: sessionID, PrimaryIssueID: &issueID})

	require.NoError(t, err, "OnSessionStarted should write back when enabled")
	require.Equal(t, "PINCIDENT", client.incidentID, "writeback should target the PagerDuty incident")
	require.Contains(t, client.note, "143 session started", "writeback note should describe the start event")
	require.Contains(t, client.note, "https://app.example.com/sessions/"+sessionID.String(), "writeback note should include the session URL")
	require.Equal(t, orgID, providerState.orgID, "writeback should append note id in the org")
	require.Equal(t, sessionID, providerState.sessionID, "writeback should append note id to the session issue link")
	require.Equal(t, issueID, providerState.issueID, "writeback should append note id to the PagerDuty issue link")
	require.Equal(t, "note-123", providerState.noteID, "writeback should persist the PagerDuty note id")
}

func TestWritebackService_OnSessionStartedSkipsWhenDisabled(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	service := NewWritebackService(WritebackDeps{
		Integrations: &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:               integrationID,
			OrgID:            orgID,
			WritebackEnabled: false,
			Status:           models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &writebackCredentialReaderFake{},
		Client:      &writebackClientFake{},
	})

	err := service.OnSessionStarted(context.Background(), orgID, models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
	}, models.Session{ID: uuid.New()})

	require.NoError(t, err, "disabled writeback should be a no-op")
	require.Empty(t, service.deps.Client.(*writebackClientFake).note, "disabled writeback should not call the client")
}

func TestWritebackService_OnSessionStartedMarksIntegrationDegradedAndAuditsFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
		IssueID:                &issueID,
	}
	integrations := &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
		ID:               integrationID,
		OrgID:            orgID,
		CredentialRef:    "org_credential:" + credentialID.String(),
		WritebackEnabled: true,
		Status:           models.PagerDutyIntegrationStatusActive,
	}}
	audit := &writebackAuditFake{}
	clientErr := errors.New("pagerduty unavailable")
	service := NewWritebackService(WritebackDeps{
		Integrations: integrations,
		Credentials:  &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}},
		Client:       &writebackClientFake{noteErr: clientErr},
		Audit:        audit,
	})

	err := service.OnSessionStarted(context.Background(), orgID, incident, models.Session{ID: sessionID, OrgID: orgID, PrimaryIssueID: &issueID})

	require.ErrorIs(t, err, clientErr, "OnSessionStarted should return the PagerDuty writeback error to the caller")
	require.True(t, integrations.updateCalled, "writeback failure should update integration health")
	require.Equal(t, models.PagerDutyIntegrationStatusDegraded, integrations.updatedStatus, "writeback failure should mark integration degraded")
	require.NotNil(t, integrations.updatedLastError, "writeback failure should persist last_error")
	require.Contains(t, *integrations.updatedLastError, "pagerduty unavailable", "writeback failure should include the provider error")
	require.Equal(t, models.AuditActionIntegrationWriteback, audit.params.Action, "writeback failure should emit integration writeback audit")
	require.Equal(t, models.AuditResourceIntegration, audit.params.ResourceType, "writeback audit should target the integration resource")
	require.Equal(t, integrationID.String(), *audit.params.ResourceID, "writeback audit should identify the provider integration")
	var details map[string]any
	require.NoError(t, json.Unmarshal(audit.params.Details, &details), "writeback audit details should decode")
	require.Equal(t, "failed", details["result"], "writeback audit should record failed result")
	require.Equal(t, "session_start", details["kind"], "writeback audit should record writeback kind")
	require.Equal(t, "PINCIDENT", details["incident_id"], "writeback audit should record incident id")
}

func TestWritebackService_OnSessionStartedClearsDegradedHealthAndAuditsSuccess(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	lastErr := "previous writeback failed"
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
		IssueID:                &issueID,
	}
	integrations := &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
		ID:               integrationID,
		OrgID:            orgID,
		CredentialRef:    "org_credential:" + credentialID.String(),
		WritebackEnabled: true,
		Status:           models.PagerDutyIntegrationStatusDegraded,
		LastError:        &lastErr,
	}}
	audit := &writebackAuditFake{}
	service := NewWritebackService(WritebackDeps{
		Integrations: integrations,
		Credentials:  &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}},
		Client:       &writebackClientFake{noteID: "note-123"},
		Audit:        audit,
	})

	err := service.OnSessionStarted(context.Background(), orgID, incident, models.Session{ID: sessionID, OrgID: orgID, PrimaryIssueID: &issueID})

	require.NoError(t, err, "OnSessionStarted should succeed when PagerDuty accepts the note")
	require.True(t, integrations.updateCalled, "successful writeback should clear prior degraded health")
	require.Equal(t, models.PagerDutyIntegrationStatusActive, integrations.updatedStatus, "successful writeback should mark integration active")
	require.Nil(t, integrations.updatedLastError, "successful writeback should clear last_error")
	var details map[string]any
	require.NoError(t, json.Unmarshal(audit.params.Details, &details), "writeback audit details should decode")
	require.Equal(t, "sent", details["result"], "writeback audit should record sent result")
	require.Equal(t, "note-123", details["note_id"], "writeback audit should record returned note id")
}

func TestWritebackService_OnAutomationSessionCompleteCreatesStatusUpdate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	provider := models.AutomationEventProviderPagerDuty
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
		IssueID:                &issueID,
	}
	incidents := &writebackIncidentStoreFake{incident: incident}
	client := &writebackClientFake{}
	service := NewWritebackService(WritebackDeps{
		Integrations: &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:               integrationID,
			OrgID:            orgID,
			CredentialRef:    "org_credential:" + credentialID.String(),
			WritebackEnabled: true,
			Status:           models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}},
		Incidents:   incidents,
		Client:      client,
		FrontendURL: "https://app.example.com",
	})

	err := service.OnAutomationSessionComplete(context.Background(),
		models.Session{ID: sessionID, OrgID: orgID, PrimaryIssueID: &issueID},
		models.AutomationRun{
			ID:             uuid.New(),
			OrgID:          orgID,
			Provider:       &provider,
			TriggerContext: json.RawMessage(`{"incident_id":"PINCIDENT","pagerduty_integration_id":"` + integrationID.String() + `"}`),
		},
		models.SessionStatusCompleted,
		"fixed checkout",
	)

	require.NoError(t, err, "OnAutomationSessionComplete should write PagerDuty status updates")
	require.Equal(t, integrationID, incidents.getIntegrationID, "OnAutomationSessionComplete should scope incident lookup by PagerDuty integration")
	require.Equal(t, "PINCIDENT", client.statusIncidentID, "status update should target the PagerDuty incident")
	require.Contains(t, client.statusUpdate, "completed", "status update should include the completion outcome")
	require.Contains(t, client.statusUpdate, "fixed checkout", "status update should include the session summary")
	require.Contains(t, client.statusUpdate, "https://app.example.com/sessions/"+sessionID.String(), "status update should include the session URL")
	require.Empty(t, client.note, "automation completion should use status updates rather than incident notes")
}

func TestWritebackService_OnAutomationSessionCompleteSkipsNoopRuns(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	provider := models.AutomationEventProviderPagerDuty
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
	}
	client := &writebackClientFake{}
	service := NewWritebackService(WritebackDeps{
		Integrations: &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:               integrationID,
			OrgID:            orgID,
			CredentialRef:    "org_credential:" + credentialID.String(),
			WritebackEnabled: true,
			Status:           models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}},
		Incidents:   &writebackIncidentStoreFake{incident: incident},
		Client:      client,
	})

	err := service.OnAutomationSessionComplete(context.Background(),
		models.Session{ID: uuid.New(), OrgID: orgID},
		models.AutomationRun{
			ID:             uuid.New(),
			OrgID:          orgID,
			Provider:       &provider,
			TriggerContext: json.RawMessage(`{"incident_id":"PINCIDENT"}`),
			Status:         models.AutomationRunStatusCompletedNoop,
		},
		models.SessionStatusCompleted,
		"no changes needed",
	)

	require.NoError(t, err, "no-op PagerDuty automation completion should not fail")
	require.Empty(t, client.statusUpdate, "no-op PagerDuty automation completion should not create a status update")
	require.Empty(t, client.note, "no-op PagerDuty automation completion should not create a note")
}

func TestWritebackService_OnSessionCompleteCreatesStatusUpdateForManualPagerDutySession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PINCIDENT",
		IssueID:                &issueID,
	}
	client := &writebackClientFake{}
	service := NewWritebackService(WritebackDeps{
		Integrations: &writebackIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:               integrationID,
			OrgID:            orgID,
			CredentialRef:    "org_credential:" + credentialID.String(),
			WritebackEnabled: true,
			Status:           models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &writebackCredentialReaderFake{credential: &models.DecryptedCredential{Config: models.PagerDutyConfig{AccessToken: "token"}}},
		Incidents:   &writebackIncidentStoreFake{incident: incident},
		Client:      client,
		FrontendURL: "https://app.example.com",
	})

	err := service.OnSessionComplete(context.Background(),
		models.Session{ID: sessionID, OrgID: orgID, PrimaryIssueID: &issueID},
		models.SessionStatusFailed,
		"tests failed",
	)

	require.NoError(t, err, "OnSessionComplete should write terminal PagerDuty manual session updates")
	require.Equal(t, "PINCIDENT", client.statusIncidentID, "manual session status update should target the PagerDuty incident")
	require.Contains(t, client.statusUpdate, "failed", "manual session status update should include the failure outcome")
	require.Contains(t, client.statusUpdate, "tests failed", "manual session status update should include the summary")
	require.Contains(t, client.statusUpdate, "https://app.example.com/sessions/"+sessionID.String(), "manual session status update should include the session URL")
}

type writebackIntegrationStoreFake struct {
	integration       models.PagerDutyIntegration
	updateCalled      bool
	updatedStatus     models.PagerDutyIntegrationStatus
	updatedLastError  *string
	updateIntegration uuid.UUID
}

func (s *writebackIntegrationStoreFake) GetByID(_ context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.integration.OrgID != orgID || s.integration.ID != id {
		return models.PagerDutyIntegration{}, errUnexpectedPagerDutyWritebackTestLookup
	}
	return s.integration, nil
}

func (s *writebackIntegrationStoreFake) UpdateStatus(_ context.Context, orgID, id uuid.UUID, status models.PagerDutyIntegrationStatus, lastError *string) error {
	if s.integration.OrgID != orgID || s.integration.ID != id {
		return errUnexpectedPagerDutyWritebackTestLookup
	}
	s.updateCalled = true
	s.updatedStatus = status
	s.updatedLastError = lastError
	s.updateIntegration = id
	s.integration.Status = status
	s.integration.LastError = lastError
	return nil
}

type writebackCredentialReaderFake struct {
	credential *models.DecryptedCredential
}

func (s *writebackCredentialReaderFake) GetByID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*models.DecryptedCredential, error) {
	return s.credential, nil
}

type writebackClientFake struct {
	incidentID       string
	note             string
	noteID           string
	noteErr          error
	statusIncidentID string
	statusUpdate     string
	statusErr        error
}

func (s *writebackClientFake) AddIncidentNote(_ context.Context, _ models.PagerDutyConfig, incidentID, note string) (string, error) {
	s.incidentID = incidentID
	s.note = note
	if s.noteErr != nil {
		return "", s.noteErr
	}
	return s.noteID, nil
}

func (s *writebackClientFake) CreateIncidentStatusUpdate(_ context.Context, _ models.PagerDutyConfig, incidentID, body string) error {
	s.statusIncidentID = incidentID
	s.statusUpdate = body
	if s.statusErr != nil {
		return s.statusErr
	}
	return nil
}

type writebackAuditFake struct {
	params db.SystemActionParams
}

func (s *writebackAuditFake) EmitSystemAction(_ context.Context, params db.SystemActionParams) {
	s.params = params
}

type writebackIncidentStoreFake struct {
	incident         models.PagerDutyIncident
	getIntegrationID uuid.UUID
}

func (s *writebackIncidentStoreFake) GetLatestByIncidentID(_ context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	if s.incident.OrgID != orgID || s.incident.IncidentID != incidentID {
		return models.PagerDutyIncident{}, errUnexpectedPagerDutyWritebackTestLookup
	}
	return s.incident, nil
}

func (s *writebackIncidentStoreFake) GetByIncidentID(_ context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	s.getIntegrationID = integrationID
	if s.incident.OrgID != orgID || s.incident.PagerDutyIntegrationID != integrationID || s.incident.IncidentID != incidentID {
		return models.PagerDutyIncident{}, errUnexpectedPagerDutyWritebackTestLookup
	}
	return s.incident, nil
}

func (s *writebackIncidentStoreFake) GetByIssueID(_ context.Context, orgID, issueID uuid.UUID) (models.PagerDutyIncident, error) {
	if s.incident.OrgID != orgID || s.incident.IssueID == nil || *s.incident.IssueID != issueID {
		return models.PagerDutyIncident{}, errUnexpectedPagerDutyWritebackTestLookup
	}
	return s.incident, nil
}

type writebackProviderStateFake struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	issueID   uuid.UUID
	noteID    string
}

func (s *writebackProviderStateFake) AppendWritebackNoteID(_ context.Context, orgID, sessionID, issueID uuid.UUID, noteID string) error {
	s.orgID = orgID
	s.sessionID = sessionID
	s.issueID = issueID
	s.noteID = noteID
	return nil
}

var errUnexpectedPagerDutyWritebackTestLookup = errUnexpectedPagerDutySyncTestLookup
