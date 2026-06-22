package pagerduty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type writebackIntegrationStore interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error)
	UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.PagerDutyIntegrationStatus, lastError *string) error
}

type writebackCredentialReader interface {
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
}

type writebackIncidentStore interface {
	GetByIncidentID(ctx context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
	GetLatestByIncidentID(ctx context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
	GetByIssueID(ctx context.Context, orgID, issueID uuid.UUID) (models.PagerDutyIncident, error)
}

type WritebackClient interface {
	AddIncidentNote(ctx context.Context, cfg models.PagerDutyConfig, incidentID, note string) (string, error)
	CreateIncidentStatusUpdate(ctx context.Context, cfg models.PagerDutyConfig, incidentID, body string) error
}

type writebackProviderStateStore interface {
	AppendWritebackNoteID(ctx context.Context, orgID, sessionID, issueID uuid.UUID, noteID string) error
}

type writebackAuditEmitter interface {
	EmitSystemAction(ctx context.Context, params db.SystemActionParams)
}

type WritebackDeps struct {
	Integrations  writebackIntegrationStore
	Credentials   writebackCredentialReader
	Incidents     writebackIncidentStore
	ProviderState writebackProviderStateStore
	Client        WritebackClient
	Audit         writebackAuditEmitter
	FrontendURL   string
	Metrics       *metrics.PagerDutyMetrics
	Logger        zerolog.Logger
}

type WritebackService struct {
	deps WritebackDeps
}

func NewWritebackService(deps WritebackDeps) *WritebackService {
	if deps.Client == nil {
		deps.Client = RESTWritebackClient{metrics: deps.Metrics}
	}
	deps.FrontendURL = strings.TrimRight(deps.FrontendURL, "/")
	return &WritebackService{deps: deps}
}

func (s *WritebackService) OnSessionStarted(ctx context.Context, orgID uuid.UUID, incident models.PagerDutyIncident, session models.Session) error {
	note := "143 session started for PagerDuty incident " + incident.IncidentID + "."
	if sessionURL := s.sessionURL(session.ID); sessionURL != "" {
		note += "\nSession: " + sessionURL
	}
	return s.addIncidentNote(ctx, orgID, incident, note, "session_start", session.ID, session.PrimaryIssueID)
}

func (s *WritebackService) OnAutomationSessionComplete(ctx context.Context, session models.Session, automationRun models.AutomationRun, status models.SessionStatus, summary string) error {
	if automationRun.Provider == nil || *automationRun.Provider != models.AutomationEventProviderPagerDuty {
		return nil
	}
	if automationRun.Status == models.AutomationRunStatusCompletedNoop {
		return nil
	}
	incident, ok, err := s.incidentForAutomationRun(ctx, automationRun)
	if err != nil || !ok {
		return err
	}
	outcome := "completed"
	if status != models.SessionStatusCompleted {
		outcome = "failed"
	}
	note := fmt.Sprintf("143 automation session %s for PagerDuty incident %s.", outcome, incident.IncidentID)
	if summary = strings.TrimSpace(summary); summary != "" {
		note += "\nSummary: " + truncateWritebackText(summary, 800)
	}
	if sessionURL := s.sessionURL(session.ID); sessionURL != "" {
		note += "\nSession: " + sessionURL
	}
	return s.createIncidentStatusUpdate(ctx, automationRun.OrgID, incident, note, "session_"+outcome)
}

func (s *WritebackService) OnSessionComplete(ctx context.Context, session models.Session, status models.SessionStatus, summary string) error {
	if s == nil || s.deps.Incidents == nil || session.PrimaryIssueID == nil || *session.PrimaryIssueID == uuid.Nil || session.AutomationRunID != nil {
		return nil
	}
	outcome, ok := pagerDutySessionWritebackOutcome(status)
	if !ok {
		return nil
	}
	incident, err := s.deps.Incidents.GetByIssueID(ctx, session.OrgID, *session.PrimaryIssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	body := fmt.Sprintf("143 session %s for PagerDuty incident %s.", outcome, incident.IncidentID)
	if summary = strings.TrimSpace(summary); summary != "" {
		body += "\nSummary: " + truncateWritebackText(summary, 800)
	}
	if sessionURL := s.sessionURL(session.ID); sessionURL != "" {
		body += "\nSession: " + sessionURL
	}
	return s.createIncidentStatusUpdate(ctx, session.OrgID, incident, body, "manual_session_"+outcome)
}

func (s *WritebackService) OnPROpened(ctx context.Context, session models.Session, pr models.PullRequest) error {
	if s == nil || s.deps.Incidents == nil || session.PrimaryIssueID == nil || *session.PrimaryIssueID == uuid.Nil {
		return nil
	}
	incident, err := s.deps.Incidents.GetByIssueID(ctx, session.OrgID, *session.PrimaryIssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	note := fmt.Sprintf("143 opened a pull request for PagerDuty incident %s.", incident.IncidentID)
	if pr.GitHubPRURL != "" {
		note += "\nPR: " + pr.GitHubPRURL
	}
	if pr.Title != "" {
		note += "\nTitle: " + truncateWritebackText(pr.Title, 300)
	}
	if sessionURL := s.sessionURL(session.ID); sessionURL != "" {
		note += "\nSession: " + sessionURL
	}
	return s.addIncidentNote(ctx, session.OrgID, incident, note, "pr_opened", session.ID, session.PrimaryIssueID)
}

func (s *WritebackService) addIncidentNote(ctx context.Context, orgID uuid.UUID, incident models.PagerDutyIncident, note string, kind string, sessionID uuid.UUID, issueID *uuid.UUID) error {
	noteID, err := s.withPagerDutyWritebackCredential(ctx, orgID, incident, kind, func(cfg models.PagerDutyConfig) (string, error) {
		return s.deps.Client.AddIncidentNote(ctx, cfg, incident.IncidentID, note)
	})
	if err != nil {
		return err
	}
	if noteID != "" && s.deps.ProviderState != nil && sessionID != uuid.Nil && issueID != nil && *issueID != uuid.Nil {
		if err := s.deps.ProviderState.AppendWritebackNoteID(ctx, orgID, sessionID, *issueID, noteID); err != nil {
			s.deps.Logger.Warn().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("issue_id", issueID.String()).
				Str("incident_id", incident.IncidentID).
				Msg("failed to persist PagerDuty writeback note id")
		}
	}
	return nil
}

func (s *WritebackService) createIncidentStatusUpdate(ctx context.Context, orgID uuid.UUID, incident models.PagerDutyIncident, body string, kind string) error {
	_, err := s.withPagerDutyWritebackCredential(ctx, orgID, incident, kind, func(cfg models.PagerDutyConfig) (string, error) {
		return "", s.deps.Client.CreateIncidentStatusUpdate(ctx, cfg, incident.IncidentID, body)
	})
	return err
}

func (s *WritebackService) withPagerDutyWritebackCredential(ctx context.Context, orgID uuid.UUID, incident models.PagerDutyIncident, kind string, write func(models.PagerDutyConfig) (string, error)) (string, error) {
	result := "sent"
	defer func() {
		if s != nil {
			s.deps.Metrics.RecordWriteback(ctx, kind, result)
		}
	}()
	if s == nil || s.deps.Integrations == nil || s.deps.Credentials == nil || s.deps.Client == nil {
		result = "unavailable"
		return "", nil
	}
	if incident.PagerDutyIntegrationID == uuid.Nil || strings.TrimSpace(incident.IncidentID) == "" {
		result = "skipped_missing_incident"
		return "", nil
	}
	pd, err := s.deps.Integrations.GetByID(ctx, orgID, incident.PagerDutyIntegrationID)
	if err != nil {
		result = "integration_lookup_failed"
		return "", err
	}
	if !pd.WritebackEnabled || (pd.Status != models.PagerDutyIntegrationStatusActive && pd.Status != models.PagerDutyIntegrationStatusDegraded) {
		result = "skipped_disabled"
		return "", nil
	}
	credentialID, err := pagerDutyCredentialIDFromRef(pd.CredentialRef)
	if err != nil {
		result = "credential_ref_invalid"
		s.recordPagerDutyWritebackOutcome(ctx, orgID, pd, incident, kind, result, "", err)
		return "", err
	}
	credential, err := s.deps.Credentials.GetByID(ctx, orgID, credentialID)
	if err != nil {
		result = "credential_lookup_failed"
		s.recordPagerDutyWritebackOutcome(ctx, orgID, pd, incident, kind, result, "", err)
		return "", err
	}
	cfg, ok := credential.Config.(models.PagerDutyConfig)
	if !ok {
		result = "credential_invalid"
		err := fmt.Errorf("stored credential is not a PagerDuty credential: %T", credential.Config)
		s.recordPagerDutyWritebackOutcome(ctx, orgID, pd, incident, kind, result, "", err)
		return "", err
	}
	noteID, err := write(cfg)
	if err != nil {
		result = "failed"
		s.recordPagerDutyWritebackOutcome(ctx, orgID, pd, incident, kind, result, "", err)
		return "", err
	}
	noteID = strings.TrimSpace(noteID)
	s.recordPagerDutyWritebackOutcome(ctx, orgID, pd, incident, kind, result, noteID, nil)
	return noteID, nil
}

func (s *WritebackService) recordPagerDutyWritebackOutcome(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration, incident models.PagerDutyIncident, kind, result, noteID string, writeErr error) {
	if s == nil {
		return
	}
	s.updatePagerDutyWritebackHealth(ctx, orgID, integration, writeErr)
	s.emitPagerDutyWritebackAudit(ctx, orgID, integration, incident, kind, result, noteID, writeErr)
}

func (s *WritebackService) updatePagerDutyWritebackHealth(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration, writeErr error) {
	if s == nil || s.deps.Integrations == nil {
		return
	}
	if writeErr != nil {
		lastError := truncateWritebackText(writeErr.Error(), 1000)
		if err := s.deps.Integrations.UpdateStatus(ctx, orgID, integration.ID, models.PagerDutyIntegrationStatusDegraded, &lastError); err != nil {
			s.deps.Logger.Warn().
				Err(err).
				Str("pagerduty_integration_id", integration.ID.String()).
				Msg("failed to mark PagerDuty writeback integration degraded")
		}
		return
	}
	if integration.Status == models.PagerDutyIntegrationStatusActive && integration.LastError == nil {
		return
	}
	if err := s.deps.Integrations.UpdateStatus(ctx, orgID, integration.ID, models.PagerDutyIntegrationStatusActive, nil); err != nil {
		s.deps.Logger.Warn().
			Err(err).
			Str("pagerduty_integration_id", integration.ID.String()).
			Msg("failed to clear PagerDuty writeback integration health")
	}
}

func (s *WritebackService) emitPagerDutyWritebackAudit(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration, incident models.PagerDutyIncident, kind, result, noteID string, writeErr error) {
	if s == nil || s.deps.Audit == nil {
		return
	}
	details := map[string]any{
		"provider":     "pagerduty",
		"kind":         kind,
		"result":       result,
		"incident_id":  incident.IncidentID,
		"integration":  integration.ID.String(),
		"writeback_on": integration.WritebackEnabled,
	}
	if noteID != "" {
		details["note_id"] = noteID
	}
	if writeErr != nil {
		details["error"] = truncateWritebackText(writeErr.Error(), 1000)
	}
	if incident.IssueID != nil && *incident.IssueID != uuid.Nil {
		details["issue_id"] = incident.IssueID.String()
	}
	rawDetails, err := json.Marshal(details)
	if err != nil {
		s.deps.Logger.Warn().Err(err).Msg("failed to marshal PagerDuty writeback audit details")
		return
	}
	resourceID := integration.ID.String()
	s.deps.Audit.EmitSystemAction(ctx, db.SystemActionParams{
		OrgID:        orgID,
		ActorID:      "pagerduty",
		Action:       models.AuditActionIntegrationWriteback,
		ResourceType: models.AuditResourceIntegration,
		ResourceID:   &resourceID,
		Details:      rawDetails,
	})
}

func (s *WritebackService) incidentForAutomationRun(ctx context.Context, run models.AutomationRun) (models.PagerDutyIncident, bool, error) {
	ref := pagerDutyIncidentRefFromAutomationRun(run)
	if ref.IncidentID == "" || s == nil || s.deps.Incidents == nil {
		return models.PagerDutyIncident{}, false, nil
	}
	var incident models.PagerDutyIncident
	var err error
	if ref.PagerDutyIntegrationID != uuid.Nil {
		incident, err = s.deps.Incidents.GetByIncidentID(ctx, run.OrgID, ref.PagerDutyIntegrationID, ref.IncidentID)
	} else {
		incident, err = s.deps.Incidents.GetLatestByIncidentID(ctx, run.OrgID, ref.IncidentID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.PagerDutyIncident{}, false, nil
		}
		return models.PagerDutyIncident{}, false, err
	}
	return incident, true, nil
}

type pagerDutyAutomationIncidentRef struct {
	IncidentID             string
	PagerDutyIntegrationID uuid.UUID
}

func pagerDutySessionWritebackOutcome(status models.SessionStatus) (string, bool) {
	switch status {
	case models.SessionStatusCompleted:
		return "completed", true
	case models.SessionStatusFailed:
		return "failed", true
	case models.SessionStatusNeedsHumanGuidance:
		return "needs human guidance", true
	default:
		return "", false
	}
}

func pagerDutyIncidentRefFromAutomationRun(run models.AutomationRun) pagerDutyAutomationIncidentRef {
	if ref := pagerDutyIncidentRefFromJSON(run.TriggerContext); ref.IncidentID != "" {
		return ref
	}
	return pagerDutyIncidentRefFromJSON(run.ConfigSnapshot)
}

func pagerDutyIncidentRefFromJSON(raw json.RawMessage) pagerDutyAutomationIncidentRef {
	if len(raw) == 0 {
		return pagerDutyAutomationIncidentRef{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return pagerDutyAutomationIncidentRef{}
	}
	var out pagerDutyAutomationIncidentRef
	if id, ok := decoded["incident_id"].(string); ok && strings.TrimSpace(id) != "" {
		out.IncidentID = strings.TrimSpace(id)
	}
	if id, ok := decoded["pagerduty_integration_id"].(string); ok {
		out.PagerDutyIntegrationID = parsePagerDutyUUID(id)
	}
	if pd, ok := decoded["pagerduty"].(map[string]any); ok {
		if out.IncidentID == "" {
			if id, ok := pd["incident_id"].(string); ok && strings.TrimSpace(id) != "" {
				out.IncidentID = strings.TrimSpace(id)
			}
		}
		if out.PagerDutyIntegrationID == uuid.Nil {
			if id, ok := pd["pagerduty_integration_id"].(string); ok {
				out.PagerDutyIntegrationID = parsePagerDutyUUID(id)
			}
		}
	}
	return out
}

func parsePagerDutyUUID(value string) uuid.UUID {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func (s *WritebackService) sessionURL(sessionID uuid.UUID) string {
	if s == nil || s.deps.FrontendURL == "" || sessionID == uuid.Nil {
		return ""
	}
	return s.deps.FrontendURL + "/sessions/" + sessionID.String()
}

func truncateWritebackText(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}

type RESTWritebackClient struct {
	metrics *metrics.PagerDutyMetrics
}

func (c RESTWritebackClient) AddIncidentNote(ctx context.Context, cfg models.PagerDutyConfig, incidentID, note string) (string, error) {
	noteID, err := pagerDutyProviderForConfig(cfg).AddIncidentNote(ctx, incidentID, note)
	c.recordAPIRequest(ctx, "add_note", err)
	return noteID, err
}

func (c RESTWritebackClient) CreateIncidentStatusUpdate(ctx context.Context, cfg models.PagerDutyConfig, incidentID, body string) error {
	err := pagerDutyProviderForConfig(cfg).CreateIncidentStatusUpdate(ctx, incidentID, body)
	c.recordAPIRequest(ctx, "create_status_update", err)
	return err
}

func (c RESTWritebackClient) recordAPIRequest(ctx context.Context, endpoint string, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	c.metrics.RecordAPIRequest(ctx, endpoint, result)
}

func pagerDutyProviderForConfig(cfg models.PagerDutyConfig) *integration.PagerDutyIncidentProvider {
	return integration.NewPagerDutyIncidentProvider(integration.PagerDutyProviderConfig{
		AccessToken:      cfg.AccessToken,
		BaseURL:          cfg.APIBaseURL(),
		WritebackEnabled: true,
	})
}
