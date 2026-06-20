package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type PagerDutyIntegrationStore struct {
	db DBTX
}

func NewPagerDutyIntegrationStore(db DBTX) *PagerDutyIntegrationStore {
	return &PagerDutyIntegrationStore{db: db}
}

const pagerDutyIntegrationColumns = `id, org_id, integration_id, account_subdomain, service_region,
	oauth_mode, credential_ref, webhook_secret_ref, status, scopes, last_synced_at,
	last_health_check_at, last_error, default_repository_id, writeback_enabled,
	auto_create_webhook, created_by, created_at, updated_at, deleted_at`

func scanPagerDutyIntegration(row pgx.Row) (models.PagerDutyIntegration, error) {
	var integration models.PagerDutyIntegration
	err := row.Scan(
		&integration.ID,
		&integration.OrgID,
		&integration.IntegrationID,
		&integration.AccountSubdomain,
		&integration.ServiceRegion,
		&integration.OAuthMode,
		&integration.CredentialRef,
		&integration.WebhookSecretRef,
		&integration.Status,
		&integration.Scopes,
		&integration.LastSyncedAt,
		&integration.LastHealthCheckAt,
		&integration.LastError,
		&integration.DefaultRepositoryID,
		&integration.WritebackEnabled,
		&integration.AutoCreateWebhook,
		&integration.CreatedBy,
		&integration.CreatedAt,
		&integration.UpdatedAt,
		&integration.DeletedAt,
	)
	return integration, err
}

func collectPagerDutyIntegrations(rows pgx.Rows) ([]models.PagerDutyIntegration, error) {
	var integrations []models.PagerDutyIntegration
	for rows.Next() {
		integration, err := scanPagerDutyIntegration(rows)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, integration)
	}
	return integrations, rows.Err()
}

func (s *PagerDutyIntegrationStore) Create(ctx context.Context, integration *models.PagerDutyIntegration) error {
	if len(integration.Scopes) == 0 {
		integration.Scopes = []string{}
	}
	if integration.ServiceRegion == "" {
		integration.ServiceRegion = "us"
	}
	if integration.OAuthMode == "" {
		integration.OAuthMode = models.PagerDutyOAuthModeScoped
	}
	if integration.Status == "" {
		integration.Status = models.PagerDutyIntegrationStatusActive
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO pagerduty_integrations (
			org_id, integration_id, account_subdomain, service_region, oauth_mode,
			credential_ref, webhook_secret_ref, status, scopes, default_repository_id,
			writeback_enabled, auto_create_webhook, created_by
		) VALUES (
			@org_id, @integration_id, @account_subdomain, @service_region, @oauth_mode,
			@credential_ref, @webhook_secret_ref, @status, @scopes, @default_repository_id,
			@writeback_enabled, @auto_create_webhook, @created_by
		)
		RETURNING id, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                integration.OrgID,
			"integration_id":        integration.IntegrationID,
			"account_subdomain":     integration.AccountSubdomain,
			"service_region":        integration.ServiceRegion,
			"oauth_mode":            integration.OAuthMode,
			"credential_ref":        integration.CredentialRef,
			"webhook_secret_ref":    integration.WebhookSecretRef,
			"status":                integration.Status,
			"scopes":                integration.Scopes,
			"default_repository_id": integration.DefaultRepositoryID,
			"writeback_enabled":     integration.WritebackEnabled,
			"auto_create_webhook":   integration.AutoCreateWebhook,
			"created_by":            integration.CreatedBy,
		})
	if err := row.Scan(&integration.ID, &integration.CreatedAt, &integration.UpdatedAt); err != nil {
		return fmt.Errorf("create pagerduty integration: %w", err)
	}
	return nil
}

func (s *PagerDutyIntegrationStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_integrations
		WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL`, pagerDutyIntegrationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": id})
	return scanPagerDutyIntegration(row)
}

func (s *PagerDutyIntegrationStore) GetByIntegrationID(ctx context.Context, orgID, integrationID uuid.UUID) (models.PagerDutyIntegration, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_integrations
		WHERE org_id = @org_id AND integration_id = @integration_id AND deleted_at IS NULL`, pagerDutyIntegrationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID})
	return scanPagerDutyIntegration(row)
}

func (s *PagerDutyIntegrationStore) GetByAccount(ctx context.Context, orgID uuid.UUID, accountSubdomain, serviceRegion string) (models.PagerDutyIntegration, error) {
	region := strings.TrimSpace(serviceRegion)
	if region == "" {
		region = "us"
	}
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_integrations
		WHERE org_id = @org_id
		  AND account_subdomain IS NOT DISTINCT FROM NULLIF(@account_subdomain, '')
		  AND service_region = @service_region
		  AND deleted_at IS NULL`, pagerDutyIntegrationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":            orgID,
		"account_subdomain": strings.TrimSpace(accountSubdomain),
		"service_region":    region,
	})
	return scanPagerDutyIntegration(row)
}

func (s *PagerDutyIntegrationStore) ListActive(ctx context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error) {
	return s.listByStatuses(ctx, orgID, "active", []string{string(models.PagerDutyIntegrationStatusActive)})
}

func (s *PagerDutyIntegrationStore) ListManageable(ctx context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error) {
	return s.listByStatuses(ctx, orgID, "manageable", []string{
		string(models.PagerDutyIntegrationStatusActive),
		string(models.PagerDutyIntegrationStatusDegraded),
	})
}

func (s *PagerDutyIntegrationStore) listByStatuses(ctx context.Context, orgID uuid.UUID, label string, statuses []string) ([]models.PagerDutyIntegration, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_integrations
		WHERE org_id = @org_id AND status = ANY(@statuses) AND deleted_at IS NULL
		ORDER BY created_at DESC`, pagerDutyIntegrationColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "statuses": statuses})
	if err != nil {
		return nil, fmt.Errorf("query %s pagerduty integrations: %w", label, err)
	}
	defer rows.Close()
	return collectPagerDutyIntegrations(rows)
}

func (s *PagerDutyIntegrationStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.PagerDutyIntegrationStatus, lastError *string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pagerduty_integrations
		SET status = @status, last_error = @last_error, updated_at = now()
		WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "id": id, "status": status, "last_error": lastError})
	if err != nil {
		return fmt.Errorf("update pagerduty integration status: %w", err)
	}
	return nil
}

func (s *PagerDutyIntegrationStore) UpdateLastSyncedAt(ctx context.Context, orgID, id uuid.UUID, syncedAt time.Time) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pagerduty_integrations
		SET last_synced_at = @last_synced_at, last_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL`,
		pgx.NamedArgs{
			"org_id":         orgID,
			"id":             id,
			"last_synced_at": syncedAt,
		})
	if err != nil {
		return fmt.Errorf("update pagerduty integration last_synced_at: %w", err)
	}
	return nil
}

func (s *PagerDutyIntegrationStore) UpdateSettings(ctx context.Context, orgID uuid.UUID, settings models.PagerDutyIntegrationSettings) (models.PagerDutyIntegration, error) {
	if settings.Status == "" {
		settings.Status = models.PagerDutyIntegrationStatusActive
	}
	query := fmt.Sprintf(`UPDATE pagerduty_integrations
		SET default_repository_id = @default_repository_id,
			writeback_enabled = @writeback_enabled,
			auto_create_webhook = @auto_create_webhook,
			status = @status,
			last_error = @last_error,
			updated_at = now()
		WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL
			AND (
				@default_repository_id IS NULL
				OR EXISTS (
					SELECT 1 FROM repositories r
					WHERE r.id = @default_repository_id
						AND r.org_id = @org_id
						AND r.status = 'active'
				)
			)
		RETURNING %s`, pagerDutyIntegrationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                orgID,
		"id":                    settings.ID,
		"default_repository_id": settings.DefaultRepositoryID,
		"writeback_enabled":     settings.WritebackEnabled,
		"auto_create_webhook":   settings.AutoCreateWebhook,
		"status":                settings.Status,
		"last_error":            settings.LastError,
	})
	updated, err := scanPagerDutyIntegration(row)
	if err != nil {
		return models.PagerDutyIntegration{}, fmt.Errorf("update pagerduty integration settings: %w", err)
	}
	return updated, nil
}

func (s *PagerDutyIntegrationStore) DeactivateAll(ctx context.Context, orgID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pagerduty_integrations
		SET status = 'inactive', deleted_at = COALESCE(deleted_at, now()), updated_at = now()
		WHERE org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return fmt.Errorf("deactivate pagerduty integrations: %w", err)
	}
	return nil
}

type PagerDutyServiceRepoMappingStore struct {
	db DBTX
}

func NewPagerDutyServiceRepoMappingStore(db DBTX) *PagerDutyServiceRepoMappingStore {
	return &PagerDutyServiceRepoMappingStore{db: db}
}

const pagerDutyServiceRepoMappingColumns = `id, org_id, pagerduty_integration_id,
	pagerduty_service_id, pagerduty_service_name, pagerduty_team_id, repository_id,
	base_branch, enabled, created_by, created_at, updated_at`

func (s *PagerDutyServiceRepoMappingStore) Upsert(ctx context.Context, mapping *models.PagerDutyServiceRepoMapping) error {
	row := s.db.QueryRow(ctx, `
		INSERT INTO pagerduty_service_repo_mappings (
			org_id, pagerduty_integration_id, pagerduty_service_id, pagerduty_service_name,
			pagerduty_team_id, repository_id, base_branch, enabled, created_by
		)
		SELECT @org_id, @pagerduty_integration_id, @pagerduty_service_id, @pagerduty_service_name,
			@pagerduty_team_id, @repository_id, @base_branch, @enabled, @created_by
		FROM pagerduty_integrations pdi
		JOIN repositories r
			ON r.id = @repository_id
			AND r.org_id = @org_id
			AND r.status = 'active'
		WHERE pdi.id = @pagerduty_integration_id
			AND pdi.org_id = @org_id
			AND pdi.deleted_at IS NULL
		ON CONFLICT (org_id, pagerduty_integration_id, pagerduty_service_id) DO UPDATE
		SET pagerduty_service_name = EXCLUDED.pagerduty_service_name,
			pagerduty_team_id = EXCLUDED.pagerduty_team_id,
			repository_id = EXCLUDED.repository_id,
			base_branch = EXCLUDED.base_branch,
			enabled = EXCLUDED.enabled,
			updated_at = now()
		RETURNING id, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                   mapping.OrgID,
			"pagerduty_integration_id": mapping.PagerDutyIntegrationID,
			"pagerduty_service_id":     mapping.PagerDutyServiceID,
			"pagerduty_service_name":   mapping.PagerDutyServiceName,
			"pagerduty_team_id":        mapping.PagerDutyTeamID,
			"repository_id":            mapping.RepositoryID,
			"base_branch":              mapping.BaseBranch,
			"enabled":                  mapping.Enabled,
			"created_by":               mapping.CreatedBy,
		})
	if err := row.Scan(&mapping.ID, &mapping.CreatedAt, &mapping.UpdatedAt); err != nil {
		return fmt.Errorf("upsert pagerduty service repo mapping: %w", err)
	}
	return nil
}

func (s *PagerDutyServiceRepoMappingStore) GetByServiceID(ctx context.Context, orgID, integrationID uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_service_repo_mappings
		WHERE org_id = @org_id AND pagerduty_integration_id = @integration_id
		  AND pagerduty_service_id = @service_id AND enabled = true`, pagerDutyServiceRepoMappingColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID, "service_id": serviceID})
	if err != nil {
		return models.PagerDutyServiceRepoMapping{}, fmt.Errorf("query pagerduty service repo mapping: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PagerDutyServiceRepoMapping])
}

func (s *PagerDutyServiceRepoMappingStore) ListByIntegration(ctx context.Context, orgID, integrationID uuid.UUID) ([]models.PagerDutyServiceRepoMapping, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_service_repo_mappings
		WHERE org_id = @org_id AND pagerduty_integration_id = @integration_id
		ORDER BY pagerduty_service_name ASC`, pagerDutyServiceRepoMappingColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID})
	if err != nil {
		return nil, fmt.Errorf("query pagerduty service repo mappings: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PagerDutyServiceRepoMapping])
}

type PagerDutyIncidentStore struct {
	db DBTX
}

func NewPagerDutyIncidentStore(db DBTX) *PagerDutyIncidentStore {
	return &PagerDutyIncidentStore{db: db}
}

type PagerDutyIncidentListFilter struct {
	IntegrationID *uuid.UUID
	Status        string
	ServiceID     string
	Limit         int
}

const pagerDutyIncidentColumns = `id, org_id, pagerduty_integration_id, issue_id,
	incident_id, incident_number, html_url, title, status, urgency, priority_id,
	priority_name, service_id, service_name, escalation_policy_id,
	escalation_policy_name, incident_type, assigned_user_ids, team_ids, latest_note,
	raw_data, triggered_at, acknowledged_at, resolved_at, last_event_at, created_at, updated_at`

func (s *PagerDutyIncidentStore) Upsert(ctx context.Context, incident *models.PagerDutyIncident) error {
	if incident.AssignedUserIDs == nil {
		incident.AssignedUserIDs = []string{}
	}
	if incident.TeamIDs == nil {
		incident.TeamIDs = []string{}
	}
	if len(incident.RawData) == 0 {
		incident.RawData = json.RawMessage(`{}`)
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO pagerduty_incidents (
			org_id, pagerduty_integration_id, issue_id, incident_id, incident_number,
			html_url, title, status, urgency, priority_id, priority_name, service_id,
			service_name, escalation_policy_id, escalation_policy_name, incident_type,
			assigned_user_ids, team_ids, latest_note, raw_data, triggered_at,
			acknowledged_at, resolved_at, last_event_at
		) VALUES (
			@org_id, @pagerduty_integration_id, @issue_id, @incident_id, @incident_number,
			@html_url, @title, @status, @urgency, @priority_id, @priority_name, @service_id,
			@service_name, @escalation_policy_id, @escalation_policy_name, @incident_type,
			@assigned_user_ids, @team_ids, @latest_note, @raw_data, @triggered_at,
			@acknowledged_at, @resolved_at, @last_event_at
		)
		ON CONFLICT (org_id, pagerduty_integration_id, incident_id) DO UPDATE
		SET issue_id = COALESCE(EXCLUDED.issue_id, pagerduty_incidents.issue_id),
			incident_number = EXCLUDED.incident_number,
			html_url = EXCLUDED.html_url,
			title = EXCLUDED.title,
			status = EXCLUDED.status,
			urgency = EXCLUDED.urgency,
			priority_id = EXCLUDED.priority_id,
			priority_name = EXCLUDED.priority_name,
			service_id = EXCLUDED.service_id,
			service_name = EXCLUDED.service_name,
			escalation_policy_id = EXCLUDED.escalation_policy_id,
			escalation_policy_name = EXCLUDED.escalation_policy_name,
			incident_type = EXCLUDED.incident_type,
			assigned_user_ids = EXCLUDED.assigned_user_ids,
			team_ids = EXCLUDED.team_ids,
			latest_note = EXCLUDED.latest_note,
			raw_data = EXCLUDED.raw_data,
			triggered_at = COALESCE(EXCLUDED.triggered_at, pagerduty_incidents.triggered_at),
			acknowledged_at = COALESCE(EXCLUDED.acknowledged_at, pagerduty_incidents.acknowledged_at),
			resolved_at = COALESCE(EXCLUDED.resolved_at, pagerduty_incidents.resolved_at),
			last_event_at = GREATEST(COALESCE(pagerduty_incidents.last_event_at, '-infinity'::timestamptz), COALESCE(EXCLUDED.last_event_at, '-infinity'::timestamptz)),
			updated_at = now()
		RETURNING id, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                   incident.OrgID,
			"pagerduty_integration_id": incident.PagerDutyIntegrationID,
			"issue_id":                 incident.IssueID,
			"incident_id":              incident.IncidentID,
			"incident_number":          incident.IncidentNumber,
			"html_url":                 incident.HTMLURL,
			"title":                    incident.Title,
			"status":                   incident.Status,
			"urgency":                  incident.Urgency,
			"priority_id":              incident.PriorityID,
			"priority_name":            incident.PriorityName,
			"service_id":               incident.ServiceID,
			"service_name":             incident.ServiceName,
			"escalation_policy_id":     incident.EscalationPolicyID,
			"escalation_policy_name":   incident.EscalationPolicyName,
			"incident_type":            incident.IncidentType,
			"assigned_user_ids":        incident.AssignedUserIDs,
			"team_ids":                 incident.TeamIDs,
			"latest_note":              incident.LatestNote,
			"raw_data":                 incident.RawData,
			"triggered_at":             incident.TriggeredAt,
			"acknowledged_at":          incident.AcknowledgedAt,
			"resolved_at":              incident.ResolvedAt,
			"last_event_at":            incident.LastEventAt,
		})
	if err := row.Scan(&incident.ID, &incident.CreatedAt, &incident.UpdatedAt); err != nil {
		return fmt.Errorf("upsert pagerduty incident: %w", err)
	}
	return nil
}

func (s *PagerDutyIncidentStore) List(ctx context.Context, orgID uuid.UUID, filter PagerDutyIncidentListFilter) ([]models.PagerDutyIncident, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where := []string{"org_id = @org_id"}
	if filter.IntegrationID != nil {
		where = append(where, "pagerduty_integration_id = @integration_id")
	}
	if filter.Status != "" {
		where = append(where, "status = @status")
	}
	if filter.ServiceID != "" {
		where = append(where, "service_id = @service_id")
	}
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_incidents
		WHERE %s
		ORDER BY COALESCE(last_event_at, updated_at, created_at) DESC, id DESC
		LIMIT @limit`, pagerDutyIncidentColumns, strings.Join(where, " AND "))
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"integration_id": filter.IntegrationID,
		"status":         filter.Status,
		"service_id":     filter.ServiceID,
		"limit":          int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("query pagerduty incidents: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PagerDutyIncident])
}

func (s *PagerDutyIncidentStore) GetByIncidentID(ctx context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_incidents
		WHERE org_id = @org_id AND pagerduty_integration_id = @integration_id AND incident_id = @incident_id`, pagerDutyIncidentColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID, "incident_id": incidentID})
	if err != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("query pagerduty incident: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PagerDutyIncident])
}

func (s *PagerDutyIncidentStore) GetLatestByIncidentID(ctx context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_incidents
		WHERE org_id = @org_id AND incident_id = @incident_id
		ORDER BY COALESCE(last_event_at, updated_at, created_at) DESC, id DESC
		LIMIT 1`, pagerDutyIncidentColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "incident_id": incidentID})
	if err != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("query pagerduty incident by provider id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PagerDutyIncident])
}

func (s *PagerDutyIncidentStore) GetByIssueID(ctx context.Context, orgID, issueID uuid.UUID) (models.PagerDutyIncident, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_incidents
		WHERE org_id = @org_id AND issue_id = @issue_id
		ORDER BY COALESCE(last_event_at, updated_at, created_at) DESC, id DESC
		LIMIT 1`, pagerDutyIncidentColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "issue_id": issueID})
	if err != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("query pagerduty incident by issue id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PagerDutyIncident])
}

type PagerDutyInboundEventStore struct {
	db DBTX
}

func NewPagerDutyInboundEventStore(db DBTX) *PagerDutyInboundEventStore {
	return &PagerDutyInboundEventStore{db: db}
}

const pagerDutyInboundEventColumns = `id, org_id, pagerduty_integration_id,
	webhook_delivery_id, provider_event_id, event_type, resource_type, incident_id,
	occurred_at, payload, headers, status, error_message, created_at, processed_at`

func scanPagerDutyInboundEvent(row pgx.Row) (models.PagerDutyInboundEvent, error) {
	var event models.PagerDutyInboundEvent
	var eventType string
	err := row.Scan(
		&event.ID,
		&event.OrgID,
		&event.PagerDutyIntegrationID,
		&event.WebhookDeliveryID,
		&event.ProviderEventID,
		&eventType,
		&event.ResourceType,
		&event.IncidentID,
		&event.OccurredAt,
		&event.Payload,
		&event.Headers,
		&event.Status,
		&event.ErrorMessage,
		&event.CreatedAt,
		&event.ProcessedAt,
	)
	event.EventType = models.PagerDutyEventType(eventType)
	return event, err
}

func (s *PagerDutyInboundEventStore) GetByID(ctx context.Context, orgID, eventID uuid.UUID) (models.PagerDutyInboundEvent, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_inbound_events
		WHERE org_id = @org_id AND id = @id`, pagerDutyInboundEventColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": eventID})
	return scanPagerDutyInboundEvent(row)
}

func (s *PagerDutyInboundEventStore) CreateOrGet(ctx context.Context, event *models.PagerDutyInboundEvent) (bool, error) {
	if len(event.Headers) == 0 {
		event.Headers = json.RawMessage(`{}`)
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO pagerduty_inbound_events (
			org_id, pagerduty_integration_id, webhook_delivery_id, provider_event_id,
			event_type, resource_type, incident_id, occurred_at, payload, headers, status
		) VALUES (
			@org_id, @pagerduty_integration_id, @webhook_delivery_id, @provider_event_id,
			@event_type, @resource_type, @incident_id, @occurred_at, @payload, @headers, @status
		)
		ON CONFLICT (org_id, provider_event_id) DO NOTHING
		RETURNING id, created_at`,
		pgx.NamedArgs{
			"org_id":                   event.OrgID,
			"pagerduty_integration_id": event.PagerDutyIntegrationID,
			"webhook_delivery_id":      event.WebhookDeliveryID,
			"provider_event_id":        event.ProviderEventID,
			"event_type":               event.EventType,
			"resource_type":            event.ResourceType,
			"incident_id":              event.IncidentID,
			"occurred_at":              event.OccurredAt,
			"payload":                  event.Payload,
			"headers":                  event.Headers,
			"status":                   event.Status,
		})
	err := row.Scan(&event.ID, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := s.getInboundEventByProviderEventID(ctx, event.OrgID, event.ProviderEventID)
		if lookupErr != nil {
			return false, lookupErr
		}
		*event = existing
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create pagerduty inbound event: %w", err)
	}
	return true, nil
}

func (s *PagerDutyInboundEventStore) getInboundEventByProviderEventID(ctx context.Context, orgID uuid.UUID, providerEventID string) (models.PagerDutyInboundEvent, error) {
	query := fmt.Sprintf(`SELECT %s FROM pagerduty_inbound_events
		WHERE org_id = @org_id AND provider_event_id = @provider_event_id`, pagerDutyInboundEventColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "provider_event_id": providerEventID})
	event, err := scanPagerDutyInboundEvent(row)
	if err != nil {
		return models.PagerDutyInboundEvent{}, fmt.Errorf("query existing pagerduty inbound event: %w", err)
	}
	return event, nil
}

func (s *PagerDutyInboundEventStore) MarkProcessed(ctx context.Context, orgID, eventID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pagerduty_inbound_events
		SET status = 'processed', processed_at = now(), error_message = NULL
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": eventID})
	if err != nil {
		return fmt.Errorf("mark pagerduty inbound event processed: %w", err)
	}
	return nil
}

func (s *PagerDutyInboundEventStore) MarkFailed(ctx context.Context, orgID, eventID uuid.UUID, message string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pagerduty_inbound_events
		SET status = 'failed', processed_at = now(), error_message = @message
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": eventID, "message": message})
	if err != nil {
		return fmt.Errorf("mark pagerduty inbound event failed: %w", err)
	}
	return nil
}

type AutomationEventTriggerStore struct {
	db TxStarter
}

func NewAutomationEventTriggerStore(db TxStarter) *AutomationEventTriggerStore {
	return &AutomationEventTriggerStore{db: db}
}

const automationEventTriggerColumns = `id, org_id, automation_id, provider, event_types,
	filter, repository_id, enabled, created_at, updated_at`

func scanAutomationEventTrigger(row pgx.Row) (models.AutomationEventTrigger, error) {
	var trigger models.AutomationEventTrigger
	var provider string
	err := row.Scan(
		&trigger.ID,
		&trigger.OrgID,
		&trigger.AutomationID,
		&provider,
		&trigger.EventTypes,
		&trigger.Filter,
		&trigger.RepositoryID,
		&trigger.Enabled,
		&trigger.CreatedAt,
		&trigger.UpdatedAt,
	)
	trigger.Provider = models.AutomationEventProvider(provider)
	return trigger, err
}

func collectAutomationEventTriggers(rows pgx.Rows) ([]models.AutomationEventTrigger, error) {
	var triggers []models.AutomationEventTrigger
	for rows.Next() {
		trigger, err := scanAutomationEventTrigger(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, trigger)
	}
	return triggers, rows.Err()
}

func (s *AutomationEventTriggerStore) ReplaceForAutomation(ctx context.Context, orgID, automationID uuid.UUID, triggers []models.AutomationEventTrigger) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace automation event triggers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM automation_event_triggers
		WHERE org_id = @org_id AND automation_id = @automation_id`,
		pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}); err != nil {
		return fmt.Errorf("delete automation event triggers: %w", err)
	}
	for i := range triggers {
		triggers[i].OrgID = orgID
		triggers[i].AutomationID = automationID
		if len(triggers[i].Filter) == 0 {
			triggers[i].Filter = json.RawMessage(`{}`)
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO automation_event_triggers (
				org_id, automation_id, provider, event_types, filter, repository_id, enabled
			) VALUES (
				@org_id, @automation_id, @provider, @event_types, @filter, @repository_id, @enabled
			)
			RETURNING id, created_at, updated_at`,
			pgx.NamedArgs{
				"org_id":        triggers[i].OrgID,
				"automation_id": triggers[i].AutomationID,
				"provider":      triggers[i].Provider,
				"event_types":   triggers[i].EventTypes,
				"filter":        triggers[i].Filter,
				"repository_id": triggers[i].RepositoryID,
				"enabled":       triggers[i].Enabled,
			})
		if err := row.Scan(&triggers[i].ID, &triggers[i].CreatedAt, &triggers[i].UpdatedAt); err != nil {
			return fmt.Errorf("insert automation event trigger: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace automation event triggers: %w", err)
	}
	return nil
}

func (s *AutomationEventTriggerStore) ListByAutomation(ctx context.Context, orgID, automationID uuid.UUID) ([]models.AutomationEventTrigger, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_event_triggers
		WHERE org_id = @org_id AND automation_id = @automation_id
		ORDER BY created_at ASC`, automationEventTriggerColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "automation_id": automationID})
	if err != nil {
		return nil, fmt.Errorf("query automation event triggers: %w", err)
	}
	defer rows.Close()
	return collectAutomationEventTriggers(rows)
}

func (s *AutomationEventTriggerStore) ListEnabledByProviderEvent(ctx context.Context, orgID uuid.UUID, provider models.AutomationEventProvider, eventType string) ([]models.AutomationEventTrigger, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_event_triggers
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND enabled = true
		  AND @event_type = ANY(event_types)
		ORDER BY created_at ASC`, automationEventTriggerColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"provider":   provider,
		"event_type": eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("query enabled automation event triggers: %w", err)
	}
	defer rows.Close()
	return collectAutomationEventTriggers(rows)
}
