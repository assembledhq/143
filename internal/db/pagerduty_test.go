package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPagerDutyIncidentStore_UpsertFiltersByOrgAndIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	integrationID := uuid.New()
	issueID := uuid.New()
	incidentRowID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	incident := &models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IssueID:                &issueID,
		IncidentID:             "PABC123",
		IncidentNumber:         int64PtrPagerDutyTest(42),
		HTMLURL:                strPtrPagerDutyTest("https://acme.pagerduty.com/incidents/PABC123"),
		Title:                  "API latency",
		Status:                 "triggered",
		Urgency:                strPtrPagerDutyTest("high"),
		PriorityName:           strPtrPagerDutyTest("P1"),
		ServiceID:              strPtrPagerDutyTest("PSVC"),
		ServiceName:            strPtrPagerDutyTest("api"),
		EscalationPolicyID:     strPtrPagerDutyTest("PEP"),
		EscalationPolicyName:   strPtrPagerDutyTest("Core Platform"),
		AssignedUserIDs:        []string{"PU1"},
		TeamIDs:                []string{"PT1"},
		LatestNote:             strPtrPagerDutyTest("Investigating"),
		RawData:                json.RawMessage(`{"id":"PABC123"}`),
		TriggeredAt:            &now,
		LastEventAt:            &now,
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO pagerduty_incidents").
		WithArgs(pgx.NamedArgs{
			"org_id":                   orgID,
			"pagerduty_integration_id": integrationID,
			"issue_id":                 &issueID,
			"incident_id":              "PABC123",
			"incident_number":          int64PtrPagerDutyTest(42),
			"html_url":                 strPtrPagerDutyTest("https://acme.pagerduty.com/incidents/PABC123"),
			"title":                    "API latency",
			"status":                   "triggered",
			"urgency":                  strPtrPagerDutyTest("high"),
			"priority_id":              (*string)(nil),
			"priority_name":            strPtrPagerDutyTest("P1"),
			"service_id":               strPtrPagerDutyTest("PSVC"),
			"service_name":             strPtrPagerDutyTest("api"),
			"escalation_policy_id":     strPtrPagerDutyTest("PEP"),
			"escalation_policy_name":   strPtrPagerDutyTest("Core Platform"),
			"incident_type":            (*string)(nil),
			"assigned_user_ids":        []string{"PU1"},
			"team_ids":                 []string{"PT1"},
			"latest_note":              strPtrPagerDutyTest("Investigating"),
			"raw_data":                 json.RawMessage(`{"id":"PABC123"}`),
			"triggered_at":             &now,
			"acknowledged_at":          (*time.Time)(nil),
			"resolved_at":              (*time.Time)(nil),
			"last_event_at":            &now,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status", "last_event_at", "created_at", "updated_at"}).AddRow(incidentRowID, "triggered", &now, now, now))

	store := NewPagerDutyIncidentStore(mock)
	err = store.Upsert(ctx, incident)
	require.NoError(t, err, "Upsert should persist PagerDuty incident mirror")
	require.Equal(t, incidentRowID, incident.ID, "Upsert should scan generated PagerDuty incident id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAutomationEventTriggerStore_ListEnabledByProviderEventScopesOrgProviderAndEvent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	automationID := uuid.New()
	triggerID := uuid.New()
	repositoryID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, automation_id, provider").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"provider":   models.AutomationEventProviderPagerDuty,
			"event_type": string(models.PagerDutyEventIncidentTriggered),
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "automation_id", "provider", "event_types", "filter", "repository_id", "enabled", "created_at", "updated_at",
		}).AddRow(
			triggerID, orgID, automationID, string(models.AutomationEventProviderPagerDuty),
			[]string{string(models.PagerDutyEventIncidentTriggered)}, json.RawMessage(`{"service_ids":["PSVC"]}`), &repositoryID, true, now, now,
		))

	store := NewAutomationEventTriggerStore(mock)
	triggers, err := store.ListEnabledByProviderEvent(ctx, orgID, models.AutomationEventProviderPagerDuty, string(models.PagerDutyEventIncidentTriggered))
	require.NoError(t, err, "ListEnabledByProviderEvent should query trigger rows")
	require.Equal(t, []models.AutomationEventTrigger{{
		ID:           triggerID,
		OrgID:        orgID,
		AutomationID: automationID,
		Provider:     models.AutomationEventProviderPagerDuty,
		EventTypes:   []string{string(models.PagerDutyEventIncidentTriggered)},
		Filter:       json.RawMessage(`{"service_ids":["PSVC"]}`),
		RepositoryID: &repositoryID,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}}, triggers, "ListEnabledByProviderEvent should return matching org/provider/event triggers")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_GetByIntegrationIDScopesOrgAndProviderIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	integrationID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	account := "acme"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, integration_id, account_subdomain").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIntegrationColumnSlice()).AddRow(
			pagerDutyIntegrationID, orgID, &integrationID, &account, "us",
			models.PagerDutyOAuthModeScoped, "org_credential:pagerduty", (*string)(nil),
			models.PagerDutyIntegrationStatusActive, []string{"incidents.read"}, (*time.Time)(nil),
			(*time.Time)(nil), (*string)(nil), (*uuid.UUID)(nil), true, false,
			(*uuid.UUID)(nil), now, now, (*time.Time)(nil),
		))

	store := NewPagerDutyIntegrationStore(mock)
	integration, err := store.GetByIntegrationID(ctx, orgID, integrationID)
	require.NoError(t, err, "GetByIntegrationID should query the org-scoped PagerDuty integration")
	require.Equal(t, pagerDutyIntegrationID, integration.ID, "GetByIntegrationID should return the provider integration row")
	require.Equal(t, &integrationID, integration.IntegrationID, "GetByIntegrationID should preserve the generic integration link")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_GetByAccountScopesOrgAccountAndRegion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	integrationID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	account := "acme"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, integration_id, account_subdomain").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "account_subdomain": "acme", "service_region": "eu"}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIntegrationColumnSlice()).AddRow(
			pagerDutyIntegrationID, orgID, &integrationID, &account, "eu",
			models.PagerDutyOAuthModeScoped, "org_credential:pagerduty", (*string)(nil),
			models.PagerDutyIntegrationStatusActive, []string{"incidents.read"}, (*time.Time)(nil),
			(*time.Time)(nil), (*string)(nil), (*uuid.UUID)(nil), true, false,
			(*uuid.UUID)(nil), now, now, (*time.Time)(nil),
		))

	store := NewPagerDutyIntegrationStore(mock)
	integration, err := store.GetByAccount(ctx, orgID, " acme ", "eu")
	require.NoError(t, err, "GetByAccount should query the org-scoped PagerDuty account")
	require.Equal(t, pagerDutyIntegrationID, integration.ID, "GetByAccount should return the matching provider integration row")
	require.Equal(t, &account, integration.AccountSubdomain, "GetByAccount should preserve the matched account")
	require.Equal(t, "eu", integration.ServiceRegion, "GetByAccount should preserve the matched service region")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_ListManageableIncludesActiveAndDegraded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	activeID := uuid.New()
	degradedID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, integration_id, account_subdomain").
		WithArgs(pgx.NamedArgs{
			"org_id":   orgID,
			"statuses": []string{string(models.PagerDutyIntegrationStatusActive), string(models.PagerDutyIntegrationStatusDegraded)},
		}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIntegrationColumnSlice()).
			AddRow(
				activeID, orgID, (*uuid.UUID)(nil), (*string)(nil), "us",
				models.PagerDutyOAuthModeScoped, "org_credential:pagerduty", (*string)(nil),
				models.PagerDutyIntegrationStatusActive, []string{}, (*time.Time)(nil),
				(*time.Time)(nil), (*string)(nil), (*uuid.UUID)(nil), true, false,
				(*uuid.UUID)(nil), now, now, (*time.Time)(nil),
			).
			AddRow(
				degradedID, orgID, (*uuid.UUID)(nil), (*string)(nil), "us",
				models.PagerDutyOAuthModeScoped, "org_credential:pagerduty", (*string)(nil),
				models.PagerDutyIntegrationStatusDegraded, []string{}, (*time.Time)(nil),
				(*time.Time)(nil), (*string)(nil), (*uuid.UUID)(nil), true, false,
				(*uuid.UUID)(nil), now, now, (*time.Time)(nil),
			))

	store := NewPagerDutyIntegrationStore(mock)
	integrations, err := store.ListManageable(ctx, orgID)

	require.NoError(t, err, "ListManageable should query manageable PagerDuty integrations")
	require.Equal(t, []models.PagerDutyIntegration{
		{
			ID:               activeID,
			OrgID:            orgID,
			ServiceRegion:    "us",
			OAuthMode:        models.PagerDutyOAuthModeScoped,
			CredentialRef:    "org_credential:pagerduty",
			Status:           models.PagerDutyIntegrationStatusActive,
			Scopes:           []string{},
			WritebackEnabled: true,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		{
			ID:               degradedID,
			OrgID:            orgID,
			ServiceRegion:    "us",
			OAuthMode:        models.PagerDutyOAuthModeScoped,
			CredentialRef:    "org_credential:pagerduty",
			Status:           models.PagerDutyIntegrationStatusDegraded,
			Scopes:           []string{},
			WritebackEnabled: true,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
	}, integrations, "ListManageable should return active and degraded PagerDuty integrations")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_UpdateSettingsScopesOrgAndReturnsRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	integrationID := uuid.New()
	repoID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	account := "acme"
	lastError := "token expired"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("UPDATE pagerduty_integrations").
		WithArgs(pgx.NamedArgs{
			"org_id":                orgID,
			"id":                    pagerDutyIntegrationID,
			"default_repository_id": &repoID,
			"writeback_enabled":     false,
			"auto_create_webhook":   true,
			"status":                models.PagerDutyIntegrationStatusDegraded,
			"last_error":            &lastError,
		}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIntegrationColumnSlice()).AddRow(
			pagerDutyIntegrationID, orgID, &integrationID, &account, "us",
			models.PagerDutyOAuthModeScoped, "org_credential:pagerduty", (*string)(nil),
			models.PagerDutyIntegrationStatusDegraded, []string{"incidents.read"}, (*time.Time)(nil),
			&now, &lastError, &repoID, false, true,
			(*uuid.UUID)(nil), now, now, (*time.Time)(nil),
		))

	store := NewPagerDutyIntegrationStore(mock)
	updated, err := store.UpdateSettings(ctx, orgID, models.PagerDutyIntegrationSettings{
		ID:                  pagerDutyIntegrationID,
		DefaultRepositoryID: &repoID,
		WritebackEnabled:    false,
		AutoCreateWebhook:   true,
		Status:              models.PagerDutyIntegrationStatusDegraded,
		LastError:           &lastError,
	})

	require.NoError(t, err, "UpdateSettings should update the org-scoped PagerDuty integration")
	require.Equal(t, pagerDutyIntegrationID, updated.ID, "UpdateSettings should return the updated provider integration")
	require.Equal(t, &repoID, updated.DefaultRepositoryID, "UpdateSettings should return the new default repository")
	require.Equal(t, models.PagerDutyIntegrationStatusDegraded, updated.Status, "UpdateSettings should return the new status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_UpdateLastSyncedAtScopesOrg(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	syncedAt := time.Date(2026, 6, 19, 18, 30, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectExec("UPDATE pagerduty_integrations").
		WithArgs(pgx.NamedArgs{
			"org_id":         orgID,
			"id":             pagerDutyIntegrationID,
			"last_synced_at": syncedAt,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewPagerDutyIntegrationStore(mock)
	err = store.UpdateLastSyncedAt(ctx, orgID, pagerDutyIntegrationID, syncedAt)

	require.NoError(t, err, "UpdateLastSyncedAt should persist the org-scoped reconciliation watermark")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyServiceRepoMappingStore_UpsertScopesRepositoryAndIntegrationToOrg(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	repositoryID := uuid.New()
	mappingID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	baseBranch := "main"
	teamID := "PTEAM"
	mapping := &models.PagerDutyServiceRepoMapping{
		OrgID:                  orgID,
		PagerDutyIntegrationID: pagerDutyIntegrationID,
		PagerDutyServiceID:     "PSVC",
		PagerDutyServiceName:   "API",
		PagerDutyTeamID:        &teamID,
		RepositoryID:           repositoryID,
		BaseBranch:             &baseBranch,
		Enabled:                true,
		CreatedBy:              &userID,
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("(?s)INSERT INTO pagerduty_service_repo_mappings.*FROM pagerduty_integrations.*JOIN repositories").
		WithArgs(pgx.NamedArgs{
			"org_id":                   orgID,
			"pagerduty_integration_id": pagerDutyIntegrationID,
			"pagerduty_service_id":     "PSVC",
			"pagerduty_service_name":   "API",
			"pagerduty_team_id":        &teamID,
			"repository_id":            repositoryID,
			"base_branch":              &baseBranch,
			"enabled":                  true,
			"created_by":               &userID,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mappingID, now, now))

	store := NewPagerDutyServiceRepoMappingStore(mock)
	err = store.Upsert(ctx, mapping)
	require.NoError(t, err, "Upsert should persist org-scoped PagerDuty service mapping")
	require.Equal(t, mappingID, mapping.ID, "Upsert should scan the mapping id")
	require.Equal(t, now, mapping.CreatedAt, "Upsert should scan created_at")
	require.Equal(t, now, mapping.UpdatedAt, "Upsert should scan updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIntegrationStore_DeactivateAllScopesOrg(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectExec("UPDATE pagerduty_integrations").
		WithArgs(pgx.NamedArgs{"org_id": orgID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	store := NewPagerDutyIntegrationStore(mock)
	err = store.DeactivateAll(ctx, orgID)

	require.NoError(t, err, "DeactivateAll should deactivate PagerDuty integrations for the org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIncidentStore_ListScopesOrgAndFilters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	incidentRowID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	incidentNumber := int64(42)
	htmlURL := "https://acme.pagerduty.com/incidents/PABC123"
	urgency := "high"
	priority := "P1"
	serviceID := "PSVC"
	serviceName := "api"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, pagerduty_integration_id").
		WithArgs(pgx.NamedArgs{
			"org_id":         orgID,
			"integration_id": &pagerDutyIntegrationID,
			"status":         "triggered",
			"service_id":     "PSVC",
			"limit":          int32(25),
		}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIncidentColumnSlice()).AddRow(
			incidentRowID, orgID, pagerDutyIntegrationID, (*uuid.UUID)(nil),
			"PABC123", &incidentNumber, &htmlURL, "API latency", "triggered",
			&urgency, (*string)(nil), &priority, &serviceID, &serviceName,
			(*string)(nil), (*string)(nil), (*string)(nil), []string{}, []string{},
			(*string)(nil), json.RawMessage(`{"id":"PABC123"}`), &now, (*time.Time)(nil),
			(*time.Time)(nil), &now, now, now,
		))

	store := NewPagerDutyIncidentStore(mock)
	incidents, err := store.List(ctx, orgID, PagerDutyIncidentListFilter{
		IntegrationID: &pagerDutyIntegrationID,
		Status:        "triggered",
		ServiceID:     "PSVC",
		Limit:         25,
	})

	require.NoError(t, err, "List should query PagerDuty incidents for the org")
	require.Equal(t, []models.PagerDutyIncident{{
		ID:                     incidentRowID,
		OrgID:                  orgID,
		PagerDutyIntegrationID: pagerDutyIntegrationID,
		IncidentID:             "PABC123",
		IncidentNumber:         &incidentNumber,
		HTMLURL:                &htmlURL,
		Title:                  "API latency",
		Status:                 "triggered",
		Urgency:                &urgency,
		PriorityName:           &priority,
		ServiceID:              &serviceID,
		ServiceName:            &serviceName,
		AssignedUserIDs:        []string{},
		TeamIDs:                []string{},
		RawData:                json.RawMessage(`{"id":"PABC123"}`),
		TriggeredAt:            &now,
		LastEventAt:            &now,
		CreatedAt:              now,
		UpdatedAt:              now,
	}}, incidents, "List should return matching PagerDuty incidents")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyIncidentStore_GetLatestByIncidentIDScopesOrg(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	incidentRowID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	serviceID := "PSVC"
	serviceName := "api"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, pagerduty_integration_id").
		WithArgs(pgx.NamedArgs{
			"org_id":      orgID,
			"incident_id": "PABC123",
		}).
		WillReturnRows(pgxmock.NewRows(pagerDutyIncidentColumnSlice()).AddRow(
			incidentRowID, orgID, pagerDutyIntegrationID, (*uuid.UUID)(nil),
			"PABC123", (*int64)(nil), (*string)(nil), "API latency", "acknowledged",
			(*string)(nil), (*string)(nil), (*string)(nil), &serviceID, &serviceName,
			(*string)(nil), (*string)(nil), (*string)(nil), []string{}, []string{},
			(*string)(nil), json.RawMessage(`{"id":"PABC123"}`), (*time.Time)(nil), &now,
			(*time.Time)(nil), &now, now, now,
		))

	store := NewPagerDutyIncidentStore(mock)
	incident, err := store.GetLatestByIncidentID(ctx, orgID, "PABC123")

	require.NoError(t, err, "GetLatestByIncidentID should query the org-scoped PagerDuty incident")
	require.Equal(t, incidentRowID, incident.ID, "GetLatestByIncidentID should return the incident row")
	require.Equal(t, &serviceID, incident.ServiceID, "GetLatestByIncidentID should scan the incident service id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyInboundEventStore_GetByIDScopesOrg(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	integrationID := uuid.New()
	deliveryID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 34, 56, 0, time.UTC)
	incidentID := "PABC123"
	resourceType := "incident"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("SELECT id, org_id, pagerduty_integration_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": eventID}).
		WillReturnRows(pgxmock.NewRows(pagerDutyInboundEventColumnSlice()).AddRow(
			eventID, orgID, &integrationID, &deliveryID, "evt-1",
			models.PagerDutyEventIncidentTriggered, &resourceType, &incidentID, &now,
			json.RawMessage(`{"event":{"id":"evt-1"}}`), json.RawMessage(`{"X-PagerDuty-Webhook-Delivery-ID":["delivery-1"]}`),
			"received", (*string)(nil), now, (*time.Time)(nil),
		))

	store := NewPagerDutyInboundEventStore(mock)
	event, err := store.GetByID(ctx, orgID, eventID)
	require.NoError(t, err, "GetByID should query the org-scoped PagerDuty inbound event")
	require.Equal(t, eventID, event.ID, "GetByID should return the inbound event row")
	require.Equal(t, &incidentID, event.IncidentID, "GetByID should scan incident id from the ledger")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPagerDutyInboundEventStore_CreateOrGetReturnsExistingEventOnConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	integrationID := uuid.New()
	deliveryID := uuid.New()
	now := time.Date(2026, 6, 19, 13, 0, 0, 0, time.UTC)
	incidentID := "PABC123"
	resourceType := "incident"
	event := &models.PagerDutyInboundEvent{
		OrgID:                  orgID,
		PagerDutyIntegrationID: &integrationID,
		WebhookDeliveryID:      &deliveryID,
		ProviderEventID:        "evt-1",
		EventType:              models.PagerDutyEventIncidentTriggered,
		ResourceType:           &resourceType,
		IncidentID:             &incidentID,
		Payload:                json.RawMessage(`{"event":{"id":"evt-1"}}`),
		Headers:                json.RawMessage(`{"X-PagerDuty-Webhook-Delivery-ID":["delivery-1"]}`),
		Status:                 "received",
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO pagerduty_inbound_events").
		WithArgs(pgx.NamedArgs{
			"org_id":                   orgID,
			"pagerduty_integration_id": &integrationID,
			"webhook_delivery_id":      &deliveryID,
			"provider_event_id":        "evt-1",
			"event_type":               models.PagerDutyEventIncidentTriggered,
			"resource_type":            &resourceType,
			"incident_id":              &incidentID,
			"occurred_at":              (*time.Time)(nil),
			"payload":                  json.RawMessage(`{"event":{"id":"evt-1"}}`),
			"headers":                  json.RawMessage(`{"X-PagerDuty-Webhook-Delivery-ID":["delivery-1"]}`),
			"status":                   "received",
		}).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT id, org_id, pagerduty_integration_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "provider_event_id": "evt-1"}).
		WillReturnRows(pgxmock.NewRows(pagerDutyInboundEventColumnSlice()).AddRow(
			eventID, orgID, &integrationID, &deliveryID, "evt-1",
			models.PagerDutyEventIncidentTriggered, &resourceType, &incidentID, &now,
			json.RawMessage(`{"event":{"id":"evt-1"}}`), json.RawMessage(`{"X-PagerDuty-Webhook-Delivery-ID":["delivery-1"]}`),
			"received", (*string)(nil), now, (*time.Time)(nil),
		))

	created, err := NewPagerDutyInboundEventStore(mock).CreateOrGet(ctx, event)
	require.NoError(t, err, "CreateOrGet should load the existing event after a provider event conflict")
	require.False(t, created, "CreateOrGet should report that the inbound event already existed")
	require.Equal(t, eventID, event.ID, "CreateOrGet should populate the existing event id for retry enqueue")
	require.Equal(t, "evt-1", event.ProviderEventID, "CreateOrGet should keep the provider event identity")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func pagerDutyIntegrationColumnSlice() []string {
	return []string{
		"id", "org_id", "integration_id", "account_subdomain", "service_region",
		"oauth_mode", "credential_ref", "webhook_secret_ref", "status", "scopes",
		"last_synced_at", "last_health_check_at", "last_error", "default_repository_id",
		"writeback_enabled", "auto_create_webhook", "created_by", "created_at",
		"updated_at", "deleted_at",
	}
}

func pagerDutyInboundEventColumnSlice() []string {
	return []string{
		"id", "org_id", "pagerduty_integration_id", "webhook_delivery_id",
		"provider_event_id", "event_type", "resource_type", "incident_id",
		"occurred_at", "payload", "headers", "status", "error_message",
		"created_at", "processed_at",
	}
}

func pagerDutyIncidentColumnSlice() []string {
	return []string{
		"id", "org_id", "pagerduty_integration_id", "issue_id",
		"incident_id", "incident_number", "html_url", "title", "status",
		"urgency", "priority_id", "priority_name", "service_id", "service_name",
		"escalation_policy_id", "escalation_policy_name", "incident_type",
		"assigned_user_ids", "team_ids", "latest_note", "raw_data",
		"triggered_at", "acknowledged_at", "resolved_at", "last_event_at",
		"created_at", "updated_at",
	}
}

func int64PtrPagerDutyTest(v int64) *int64 {
	return &v
}

func strPtrPagerDutyTest(v string) *string {
	return &v
}
