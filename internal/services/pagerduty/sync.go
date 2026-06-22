package pagerduty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	defaultSyncIncidentLimit = 100
	defaultSyncLookback      = 24 * time.Hour
)

var ErrPagerDutyUnauthorized = errors.New("pagerduty credential unauthorized")

type IncidentListRequest struct {
	Since time.Time
	Limit int
}

type SyncResult struct {
	IntegrationCount       int
	IncidentCount          int
	FailedIntegrationCount int
}

type SyncIncidentClient interface {
	ListIncidents(ctx context.Context, cfg models.PagerDutyConfig, req IncidentListRequest) ([]Incident, error)
}

type syncIntegrationStore interface {
	ListActive(ctx context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error)
	UpdateLastSyncedAt(ctx context.Context, orgID, id uuid.UUID, syncedAt time.Time) error
	UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.PagerDutyIntegrationStatus, lastError *string) error
}

type syncCredentialReader interface {
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
}

type SyncerDeps struct {
	Integrations syncIntegrationStore
	Credentials  syncCredentialReader
	Client       SyncIncidentClient
	Ingester     issueIngester
	Issues       issueStatusUpdater
	Incidents    incidentStore
	Now          func() time.Time
	Metrics      *metrics.PagerDutyMetrics
	Logger       zerolog.Logger
}

type Syncer struct {
	deps SyncerDeps
}

func NewSyncer(deps SyncerDeps) *Syncer {
	if deps.Client == nil {
		deps.Client = NewRESTSyncClient(nil, "")
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if setter, ok := deps.Client.(interface {
		SetMetrics(*metrics.PagerDutyMetrics)
	}); ok {
		setter.SetMetrics(deps.Metrics)
	}
	return &Syncer{deps: deps}
}

func (s *Syncer) SyncOrg(ctx context.Context, orgID uuid.UUID) (SyncResult, error) {
	if s == nil || s.deps.Integrations == nil || s.deps.Credentials == nil || s.deps.Client == nil ||
		s.deps.Ingester == nil || s.deps.Issues == nil || s.deps.Incidents == nil {
		return SyncResult{}, errors.New("pagerduty sync dependencies are incomplete")
	}
	integrations, err := s.deps.Integrations.ListActive(ctx, orgID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list active pagerduty integrations: %w", err)
	}

	result := SyncResult{IntegrationCount: len(integrations)}
	for _, integration := range integrations {
		incidentCount, syncErr := s.syncIntegration(ctx, orgID, integration)
		if syncErr != nil {
			result.FailedIntegrationCount++
			s.deps.Logger.Error().
				Err(syncErr).
				Str("org_id", orgID.String()).
				Str("pagerduty_integration_id", integration.ID.String()).
				Msg("failed to sync PagerDuty integration")
			if shouldDegradePagerDutyIntegration(syncErr) {
				message := truncatePagerDutySyncError(syncErr)
				if err := s.deps.Integrations.UpdateStatus(ctx, orgID, integration.ID, models.PagerDutyIntegrationStatusDegraded, &message); err != nil {
					s.deps.Logger.Warn().
						Err(err).
						Str("org_id", orgID.String()).
						Str("pagerduty_integration_id", integration.ID.String()).
						Msg("failed to mark PagerDuty integration degraded after sync failure")
				}
			}
			continue
		}
		result.IncidentCount += incidentCount
	}
	return result, nil
}

func (s *Syncer) syncIntegration(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration) (int, error) {
	credentialID, err := pagerDutyCredentialIDFromRef(integration.CredentialRef)
	if err != nil {
		return 0, fmt.Errorf("parse pagerduty credential reference: %w", err)
	}
	credential, err := s.deps.Credentials.GetByID(ctx, orgID, credentialID)
	if err != nil {
		return 0, fmt.Errorf("load pagerduty credential: %w", err)
	}
	cfg, ok := credential.Config.(models.PagerDutyConfig)
	if !ok {
		return 0, fmt.Errorf("stored credential is not a PagerDuty credential: %T", credential.Config)
	}

	syncStart := s.deps.Now().UTC()
	since := syncStart.Add(-defaultSyncLookback)
	if integration.LastSyncedAt != nil && !integration.LastSyncedAt.IsZero() {
		since = integration.LastSyncedAt.UTC()
	}
	incidents, err := s.deps.Client.ListIncidents(ctx, cfg, IncidentListRequest{
		Since: since,
		Limit: defaultSyncIncidentLimit,
	})
	if err != nil {
		return 0, fmt.Errorf("list pagerduty incidents: %w", err)
	}

	for _, incident := range incidents {
		if err := s.syncIncident(ctx, orgID, integration, incident); err != nil {
			return 0, err
		}
	}
	if err := s.deps.Integrations.UpdateLastSyncedAt(ctx, orgID, integration.ID, syncStart); err != nil {
		return 0, fmt.Errorf("update pagerduty sync watermark: %w", err)
	}
	return len(incidents), nil
}

func (s *Syncer) syncIncident(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration, incident Incident) error {
	if incident.ID == "" {
		return errors.New("polled pagerduty incident is missing id")
	}
	occurredAt := incident.UpdatedAt
	if occurredAt == nil {
		occurredAt = incident.CreatedAt
	}
	rawPayload := incident.RawData
	if len(rawPayload) == 0 {
		rawPayload = json.RawMessage(`{}`)
	}
	parsed := ParsedEvent{
		ProviderEventID: pagerDutySyncEventIDPrefix + incident.ID,
		EventType:       eventTypeForPolledIncident(incident),
		OccurredAt:      occurredAt,
		Incident:        incident,
		RawPayload:      rawPayload,
	}
	normalized, err := NormalizeEvent(orgID, integration, parsed)
	if err != nil {
		return fmt.Errorf("normalize polled pagerduty incident: %w", err)
	}
	issue, err := s.deps.Ingester.IngestNormalized(ctx, orgID, normalized.Issue)
	if err != nil {
		return fmt.Errorf("ingest polled pagerduty incident issue: %w", err)
	}
	if issue == nil || issue.ID == uuid.Nil {
		return errors.New("ingest polled pagerduty incident returned no issue id")
	}
	// Upsert first so the incident row holds the authoritative, recency-guarded
	// status, then derive the issue status from it (out-of-order safety; same
	// rationale as the webhook processor).
	normalized.Incident.IssueID = &issue.ID
	if err := s.deps.Incidents.Upsert(ctx, &normalized.Incident); err != nil {
		return fmt.Errorf("upsert polled pagerduty incident: %w", err)
	}
	if err := s.deps.Issues.UpdateStatus(ctx, orgID, issue.ID, IssueStatusForIncidentStatus(normalized.Incident.Status)); err != nil {
		return fmt.Errorf("update polled pagerduty issue status: %w", err)
	}
	return nil
}

func eventTypeForPolledIncident(incident Incident) models.PagerDutyEventType {
	switch strings.ToLower(strings.TrimSpace(incident.Status)) {
	case "resolved":
		return models.PagerDutyEventIncidentResolved
	case "acknowledged":
		return models.PagerDutyEventIncidentAcknowledged
	default:
		return models.PagerDutyEventIncidentTriggered
	}
}

func shouldDegradePagerDutyIntegration(err error) bool {
	return errors.Is(err, ErrPagerDutyUnauthorized) ||
		strings.Contains(err.Error(), "credential")
}

func truncatePagerDutySyncError(err error) string {
	message := err.Error()
	const maxLen = 500
	if len(message) > maxLen {
		return message[:maxLen]
	}
	return message
}

func pagerDutyCredentialIDFromRef(ref string) (uuid.UUID, error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(ref), "org_credential:")
	if !ok {
		return uuid.Nil, fmt.Errorf("unsupported credential ref %q", ref)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

type RESTSyncClient struct {
	httpClient *http.Client
	baseURL    string
	metrics    *metrics.PagerDutyMetrics
}

func NewRESTSyncClient(httpClient *http.Client, baseURL string) *RESTSyncClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.pagerduty.com"
	}
	return &RESTSyncClient{httpClient: httpClient, baseURL: baseURL}
}

func (c *RESTSyncClient) SetMetrics(metrics *metrics.PagerDutyMetrics) {
	c.metrics = metrics
}

func (c *RESTSyncClient) ListIncidents(ctx context.Context, cfg models.PagerDutyConfig, req IncidentListRequest) ([]Incident, error) {
	token := strings.TrimSpace(cfg.AccessToken)
	if token == "" {
		return nil, ErrPagerDutyUnauthorized
	}
	limit := req.Limit
	if limit <= 0 || limit > defaultSyncIncidentLimit {
		limit = defaultSyncIncidentLimit
	}
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	if !req.Since.IsZero() {
		values.Set("since", req.Since.UTC().Format(time.RFC3339))
	}
	for _, status := range []string{"triggered", "acknowledged", "resolved"} {
		values.Add("statuses[]", status)
	}

	var response struct {
		Incidents []json.RawMessage `json:"incidents"`
	}
	if err := c.do(ctx, http.MethodGet, c.baseURL+"/incidents?"+values.Encode(), cfg, nil, &response); err != nil {
		c.metrics.RecordAPIRequest(ctx, "sync_list_incidents", "error")
		return nil, err
	}
	c.metrics.RecordAPIRequest(ctx, "sync_list_incidents", "ok")
	incidents := make([]Incident, 0, len(response.Incidents))
	for _, raw := range response.Incidents {
		incident, err := parseAPIIncident(raw)
		if err != nil {
			return nil, err
		}
		incidents = append(incidents, incident)
	}
	return incidents, nil
}

func (c *RESTSyncClient) do(ctx context.Context, method, endpoint string, cfg models.PagerDutyConfig, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AccessToken))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrPagerDutyUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(raw) > 0 {
			return fmt.Errorf("pagerduty API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return fmt.Errorf("pagerduty API returned %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	return decoder.Decode(out)
}

func parseAPIIncident(raw json.RawMessage) (Incident, error) {
	var m map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&m); err != nil {
		return Incident{}, fmt.Errorf("decode pagerduty API incident: %w", err)
	}
	incident, err := parseIncident(m)
	if err != nil {
		return Incident{}, err
	}
	incident.UpdatedAt = parseTime(firstString(m, "updated_at"))
	if len(incident.RawData) == 0 {
		incident.RawData = raw
	}
	return incident, nil
}
