package pagerduty

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSessionStarter_StartSessionCreatesSessionMessageAndJob(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	serviceName := "API"
	incident := models.PagerDutyIncident{
		OrgID:       orgID,
		IssueID:     &issueID,
		IncidentID:  "PABC123",
		Title:       "API latency",
		Status:      "triggered",
		ServiceName: &serviceName,
	}
	sessions := &sessionStarterSessionStoreFake{sessionID: sessionID, threadID: threadID}
	messages := &sessionStarterMessageStoreFake{}
	jobs := &sessionStarterJobStoreFake{}
	starter := NewSessionStarter(sessions, messages, jobs, zerolog.Nop())

	session, err := starter.StartSession(context.Background(), StartSessionInput{
		OrgID:        orgID,
		Incident:     incident,
		RepositoryID: repoID,
		UserID:       &userID,
		Message:      "Investigate",
	})

	require.NoError(t, err, "StartSession should create and enqueue a PagerDuty incident session")
	require.Equal(t, sessionID, session.ID, "StartSession should return the created session")
	require.Equal(t, repoID, *sessions.session.RepositoryID, "StartSession should use the resolved repository")
	require.Equal(t, issueID, *sessions.session.PrimaryIssueID, "StartSession should link the mirrored PagerDuty issue as primary")
	require.Equal(t, models.SessionOriginManual, sessions.session.Origin, "StartSession should mark responder-started sessions as manual")
	require.Equal(t, "Investigate", messages.message.Content, "StartSession should persist the responder message")
	require.Equal(t, threadID, *messages.message.ThreadID, "StartSession should attach the initial message to the primary thread")
	require.Equal(t, "agent", jobs.queue, "StartSession should enqueue the agent queue")
	require.Equal(t, "run_agent", jobs.jobType, "StartSession should enqueue run_agent")
	require.Equal(t, orgID, jobs.orgID, "StartSession should enqueue the job in the org")
}

func TestSessionStarter_StartSessionWritesPagerDutyProviderState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issueID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	integrationID := uuid.New()
	number := int64(123)
	serviceID := "PSVC"
	serviceName := "API"
	incidentURL := "https://example.pagerduty.com/incidents/PABC123"
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		IssueID:                &issueID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PABC123",
		IncidentNumber:         &number,
		Title:                  "API latency",
		Status:                 "triggered",
		ServiceID:              &serviceID,
		ServiceName:            &serviceName,
		HTMLURL:                &incidentURL,
	}
	sessions := &sessionStarterSessionStoreFake{sessionID: sessionID, threadID: threadID}
	messages := &sessionStarterMessageStoreFake{}
	jobs := &sessionStarterJobStoreFake{}
	providerState := &sessionStarterProviderStateStoreFake{}
	starter := NewSessionStarter(sessions, messages, jobs, zerolog.Nop())
	starter.SetProviderStateStore(providerState)

	session, err := starter.StartSession(context.Background(), StartSessionInput{
		OrgID:           orgID,
		Incident:        incident,
		RepositoryID:    repoID,
		ProviderEventID: "evt-123",
		Message:         "Investigate",
	})

	require.NoError(t, err, "StartSession should not fail when provider state is recorded")
	require.Equal(t, sessionID, session.ID, "StartSession should return the created session")
	require.Equal(t, orgID, providerState.orgID, "provider state write should be org-scoped")
	require.Equal(t, sessionID, providerState.sessionID, "provider state write should target the created session")
	require.Equal(t, issueID, providerState.issueID, "provider state write should target the PagerDuty issue link")
	require.Equal(t, "PABC123", providerState.state.IncidentID, "provider state should capture the PagerDuty incident id")
	require.Equal(t, integrationID.String(), providerState.state.IntegrationID, "provider state should capture the integration id")
	require.Equal(t, &number, providerState.state.IncidentNumber, "provider state should capture the incident number")
	require.Equal(t, incidentURL, providerState.state.IncidentURL, "provider state should capture the incident URL")
	require.Equal(t, serviceID, providerState.state.ServiceID, "provider state should capture the PagerDuty service id")
	require.Equal(t, serviceName, providerState.state.ServiceName, "provider state should capture the PagerDuty service name")
	require.Equal(t, "evt-123", providerState.state.TriggerEventID, "provider state should capture the triggering event id")
}

func TestSessionStarter_StartSessionRejectsExistingActiveIssueSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issueID := uuid.New()
	existingSessionID := uuid.New()
	incident := models.PagerDutyIncident{
		OrgID:      orgID,
		IssueID:    &issueID,
		IncidentID: "PABC123",
		Title:      "API latency",
		Status:     "triggered",
	}
	sessions := &sessionStarterSessionStoreFake{existing: []models.Session{{
		ID:     existingSessionID,
		OrgID:  orgID,
		Status: models.SessionStatusRunning,
	}}}
	messages := &sessionStarterMessageStoreFake{}
	jobs := &sessionStarterJobStoreFake{}
	starter := NewSessionStarter(sessions, messages, jobs, zerolog.Nop())

	session, err := starter.StartSession(context.Background(), StartSessionInput{
		OrgID:        orgID,
		Incident:     incident,
		RepositoryID: repoID,
		Message:      "Investigate",
	})

	require.ErrorIs(t, err, ErrPagerDutySessionAlreadyRunning, "StartSession should reject a second active session for the same PagerDuty issue")
	require.Equal(t, existingSessionID, session.ID, "StartSession should return the active session that caused the conflict")
	require.Empty(t, messages.message.Content, "StartSession should not create a message when an active session already exists")
	require.Empty(t, jobs.queue, "StartSession should not enqueue a job when an active session already exists")
}

type sessionStarterSessionStoreFake struct {
	sessionID uuid.UUID
	threadID  uuid.UUID
	session   models.Session
	existing  []models.Session
}

func (s *sessionStarterSessionStoreFake) Create(_ context.Context, session *models.Session) error {
	session.ID = s.sessionID
	session.PrimaryThreadID = &s.threadID
	s.session = *session
	return nil
}

func (s *sessionStarterSessionStoreFake) ListByIssue(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.Session, error) {
	return s.existing, nil
}

type sessionStarterMessageStoreFake struct {
	message models.SessionMessage
}

func (s *sessionStarterMessageStoreFake) Create(_ context.Context, msg *models.SessionMessage) error {
	s.message = *msg
	return nil
}

type sessionStarterJobStoreFake struct {
	orgID   uuid.UUID
	queue   string
	jobType string
	payload any
}

func (s *sessionStarterJobStoreFake) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, _ int, _ *string) (uuid.UUID, error) {
	s.orgID = orgID
	s.queue = queue
	s.jobType = jobType
	s.payload = payload
	return uuid.New(), nil
}

type sessionStarterProviderStateStoreFake struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	issueID   uuid.UUID
	state     db.PagerDutyProviderState
}

func (s *sessionStarterProviderStateStoreFake) UpsertBySessionIssue(_ context.Context, orgID, sessionID, issueID uuid.UUID, state db.PagerDutyProviderState) error {
	s.orgID = orgID
	s.sessionID = sessionID
	s.issueID = issueID
	s.state = state
	return nil
}
