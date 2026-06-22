package pagerduty

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var ErrPagerDutySessionAlreadyRunning = errors.New("pagerduty session already running")

// pagerDutyIncidentSessionCooldown is the minimum time between incident-triggered
// sessions for the same issue. It prevents a flapping incident from repeatedly
// spawning fresh sessions once a prior session has finished.
const pagerDutyIncidentSessionCooldown = 10 * time.Minute

// lockedIncidentSessionStore is the optional atomic-create capability used to
// dedup concurrent incident sessions. *db.SessionStore implements it.
type lockedIncidentSessionStore interface {
	CreateForIncident(ctx context.Context, orgID, issueID uuid.UUID, cooldown time.Duration, session *models.Session) (created bool, existing models.Session, err error)
}

type StartSessionInput struct {
	OrgID           uuid.UUID
	Incident        models.PagerDutyIncident
	RepositoryID    uuid.UUID
	BaseBranch      *string
	UserID          *uuid.UUID
	Message         string
	ProviderEventID string
}

type startSessionStore interface {
	Create(ctx context.Context, session *models.Session) error
	ListByIssue(ctx context.Context, orgID, issueID uuid.UUID) ([]models.Session, error)
}

type startSessionMessageStore interface {
	Create(ctx context.Context, msg *models.SessionMessage) error
}

type startSessionJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type sessionStartWritebacker interface {
	OnSessionStarted(ctx context.Context, orgID uuid.UUID, incident models.PagerDutyIncident, session models.Session) error
}

type sessionStartProviderStateStore interface {
	UpsertBySessionIssue(ctx context.Context, orgID, sessionID, issueID uuid.UUID, state db.PagerDutyProviderState) error
}

type SessionStarter struct {
	sessions      startSessionStore
	messages      startSessionMessageStore
	jobs          startSessionJobStore
	writebacker   sessionStartWritebacker
	providerState sessionStartProviderStateStore
	logger        zerolog.Logger
}

func NewSessionStarter(sessions startSessionStore, messages startSessionMessageStore, jobs startSessionJobStore, logger zerolog.Logger) *SessionStarter {
	return &SessionStarter{sessions: sessions, messages: messages, jobs: jobs, logger: logger}
}

func (s *SessionStarter) SetWritebacker(writebacker sessionStartWritebacker) {
	s.writebacker = writebacker
}

func (s *SessionStarter) SetProviderStateStore(store sessionStartProviderStateStore) {
	s.providerState = store
}

func (s *SessionStarter) StartSession(ctx context.Context, input StartSessionInput) (models.Session, error) {
	if s == nil || s.sessions == nil || s.jobs == nil {
		return models.Session{}, fmt.Errorf("PagerDuty session starter dependencies are not configured")
	}
	if input.OrgID == uuid.Nil {
		return models.Session{}, fmt.Errorf("org_id is required")
	}
	if input.Incident.IncidentID == "" {
		return models.Session{}, fmt.Errorf("PagerDuty incident id is required")
	}
	if input.RepositoryID == uuid.Nil {
		return models.Session{}, fmt.Errorf("repository_id is required")
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		message = BuildIncidentSessionPrompt(input.Incident)
	}
	title := "PagerDuty: " + firstNonEmpty(input.Incident.Title, input.Incident.IncidentID)
	session := &models.Session{
		OrgID:             input.OrgID,
		PrimaryIssueID:    input.Incident.IssueID,
		Origin:            models.SessionOriginManual,
		InteractionMode:   models.SessionInteractionModeInteractive,
		ValidationPolicy:  models.SessionValidationPolicyOnSessionEnd,
		AgentType:         models.DefaultDefaultAgentType,
		Status:            models.SessionStatusPending,
		AutonomyLevel:     models.DefaultSessionAutonomy,
		TokenMode:         models.DefaultSessionTokenMode,
		TriggeredByUserID: input.UserID,
		Title:             &title,
		PMApproach:        &title,
		TargetBranch:      input.BaseBranch,
		RepositoryID:      &input.RepositoryID,
	}

	hasIssue := input.Incident.IssueID != nil && *input.Incident.IssueID != uuid.Nil
	switch {
	case hasIssue:
		// Prefer the atomic, advisory-locked create when the store supports it
		// (production *db.SessionStore). This closes the read-then-write race
		// where two concurrent webhook deliveries for the same incident each
		// spawn a session, and enforces a restart cooldown for flapping
		// incidents. Test fakes that don't implement it fall back to the
		// best-effort check below.
		if locker, ok := s.sessions.(lockedIncidentSessionStore); ok {
			created, existing, err := locker.CreateForIncident(ctx, input.OrgID, *input.Incident.IssueID, pagerDutyIncidentSessionCooldown, session)
			if err != nil {
				return models.Session{}, fmt.Errorf("create PagerDuty incident session: %w", err)
			}
			if !created {
				return existing, ErrPagerDutySessionAlreadyRunning
			}
		} else {
			existingSessions, err := s.sessions.ListByIssue(ctx, input.OrgID, *input.Incident.IssueID)
			if err != nil {
				return models.Session{}, fmt.Errorf("list existing PagerDuty incident sessions: %w", err)
			}
			for _, existing := range existingSessions {
				if sessionActiveForPagerDutyIncident(existing.Status) {
					return existing, ErrPagerDutySessionAlreadyRunning
				}
			}
			if err := s.sessions.Create(ctx, session); err != nil {
				return models.Session{}, fmt.Errorf("create PagerDuty incident session: %w", err)
			}
		}
	default:
		if err := s.sessions.Create(ctx, session); err != nil {
			return models.Session{}, fmt.Errorf("create PagerDuty incident session: %w", err)
		}
	}
	if s.providerState != nil && input.Incident.IssueID != nil && *input.Incident.IssueID != uuid.Nil {
		state := pagerDutyProviderStateFromIncident(input.Incident, input.ProviderEventID)
		if err := s.providerState.UpsertBySessionIssue(ctx, input.OrgID, session.ID, *input.Incident.IssueID, state); err != nil {
			s.logger.Warn().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("incident_id", input.Incident.IncidentID).
				Msg("failed to write PagerDuty provider state")
		}
	}
	if s.messages != nil {
		msg := &models.SessionMessage{
			SessionID:  session.ID,
			OrgID:      input.OrgID,
			ThreadID:   session.PrimaryThreadID,
			TurnNumber: 0,
			Role:       models.MessageRoleUser,
			Content:    message,
		}
		if input.UserID != nil {
			msg.UserID = input.UserID
		}
		if err := s.messages.Create(ctx, msg); err != nil {
			s.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to create initial PagerDuty incident session message")
		}
	}
	dedupeKey := db.RunAgentDedupeKey(session.ID)
	if _, err := s.jobs.Enqueue(ctx, input.OrgID, "agent", "run_agent", db.RunAgentPayload(session), 5, &dedupeKey); err != nil {
		return models.Session{}, fmt.Errorf("enqueue PagerDuty incident session: %w", err)
	}
	if s.writebacker != nil {
		if err := s.writebacker.OnSessionStarted(ctx, input.OrgID, input.Incident, *session); err != nil {
			s.logger.Warn().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("incident_id", input.Incident.IncidentID).
				Msg("failed to write PagerDuty session start note")
		}
	}
	return *session, nil
}

func pagerDutyProviderStateFromIncident(incident models.PagerDutyIncident, providerEventID string) db.PagerDutyProviderState {
	state := db.PagerDutyProviderState{
		IncidentID:       incident.IncidentID,
		IncidentNumber:   incident.IncidentNumber,
		IncidentURL:      stringPtrValue(incident.HTMLURL),
		ServiceID:        stringPtrValue(incident.ServiceID),
		ServiceName:      stringPtrValue(incident.ServiceName),
		TriggerEventID:   strings.TrimSpace(providerEventID),
		WritebackNoteIDs: []string{},
	}
	if incident.PagerDutyIntegrationID != uuid.Nil {
		state.IntegrationID = incident.PagerDutyIntegrationID.String()
	}
	return state
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func sessionActiveForPagerDutyIncident(status models.SessionStatus) bool {
	for _, active := range models.ActiveStatuses {
		if status == active {
			return true
		}
	}
	return false
}

func BuildIncidentSessionPrompt(incident models.PagerDutyIncident) string {
	lines := []string{
		"PagerDuty incident",
		"ID: " + incident.IncidentID,
	}
	if incident.IncidentNumber != nil {
		lines = append(lines, fmt.Sprintf("Number: %d", *incident.IncidentNumber))
	}
	if incident.Status != "" {
		lines = append(lines, "Status: "+incident.Status)
	}
	if incident.Urgency != nil && *incident.Urgency != "" {
		lines = append(lines, "Urgency: "+*incident.Urgency)
	}
	if incident.PriorityName != nil && *incident.PriorityName != "" {
		lines = append(lines, "Priority: "+*incident.PriorityName)
	}
	if incident.ServiceName != nil && *incident.ServiceName != "" {
		lines = append(lines, "Service: "+*incident.ServiceName)
	}
	if incident.EscalationPolicyName != nil && *incident.EscalationPolicyName != "" {
		lines = append(lines, "Escalation policy: "+*incident.EscalationPolicyName)
	}
	if incident.HTMLURL != nil && *incident.HTMLURL != "" {
		lines = append(lines, "Incident URL: "+*incident.HTMLURL)
	}
	if incident.LatestNote != nil && *incident.LatestNote != "" {
		lines = append(lines, "Latest note: "+*incident.LatestNote)
	}
	lines = append(lines, "", "Investigate the incident, produce a fix or a clear diagnostic summary, and do not mutate PagerDuty state unless explicitly requested.")
	return strings.Join(lines, "\n")
}
