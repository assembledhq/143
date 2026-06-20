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

var privateConnectorGroupTestColumns = []string{
	"id", "org_id", "name", "environment", "gateway_region", "status",
	"health_alert_url", "offline_alert_after_seconds",
	"created_by_user_id", "disabled_at", "created_at", "updated_at",
}

var privateConnectorTokenTestColumns = []string{
	"id", "org_id", "connector_group_id", "name", "token_hash", "token_prefix",
	"preset", "max_registrations", "registration_count", "allowed_source_cidrs",
	"allowed_gateway_region", "expires_at", "last_used_at", "revoked_at",
	"revoked_by_user_id", "created_by_user_id", "created_at",
}

var privateConnectorInstanceTestColumns = []string{
	"id", "org_id", "connector_group_id", "deployment_token_id", "instance_name",
	"public_key", "status", "version", "protocol", "gateway_region", "capabilities",
	"last_heartbeat_at", "heartbeat_interval_seconds", "online_at", "offline_at",
	"revoked_at", "revoked_by_user_id", "created_at", "updated_at",
}

var privateConnectorResourceTestColumns = []string{
	"id", "org_id", "connector_group_id", "display_name", "resource_type", "mode",
	"config", "config_source", "config_version", "status", "last_test_status",
	"last_test_error", "last_successful_request_at", "last_error",
	"created_by_user_id", "created_at", "updated_at",
}

var privateConnectorRuntimeLeaseTestColumns = []string{
	"id", "org_id", "repository_id", "preview_id", "preview_runtime_id",
	"connector_group_id", "resource_id", "status", "access_mode",
	"target_host", "target_port", "target_database", "lease_token_hash",
	"lease_token_prefix", "max_connections", "idle_timeout_seconds",
	"byte_limit", "expires_at", "revoked_at", "created_at", "updated_at",
}

var privateConnectorActionTestColumns = []string{
	"id", "org_id", "connector_group_id", "connector_instance_id",
	"resource_id", "capability", "actor_type", "actor_id", "repository_id",
	"session_id", "preview_id", "request_nonce", "request_fingerprint",
	"status", "error_code", "error_message", "result_count", "duration_ms",
	"created_at", "completed_at",
}

func ptrInt(value int) *int {
	return &value
}

func TestGeneratePrivateConnectorDeploymentToken(t *testing.T) {
	t.Parallel()

	token, err := GeneratePrivateConnectorDeploymentToken()
	require.NoError(t, err, "GeneratePrivateConnectorDeploymentToken should create a bootstrap token")
	require.Contains(t, token, PrivateConnectorDeploymentTokenPrefix, "deployment tokens should use the connector token prefix")
	require.Equal(t, HashAPIToken(token), HashAPIToken(token), "deployment token hashing should be deterministic")
	require.Equal(t, token[:len(PrivateConnectorDeploymentTokenPrefix)+8], PrivateConnectorDeploymentTokenDisplayPrefix(token), "display prefix should include only a short token id")
}

func TestPrivateConnectorStore_CreateGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	now := time.Now().UTC()
	group := &models.PrivateConnectorGroup{
		OrgID:           orgID,
		Name:            "Production VPC",
		Environment:     "production",
		GatewayRegion:   "us",
		Status:          models.PrivateConnectorStatusWaiting,
		CreatedByUserID: &userID,
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO private_connector_groups`).
		WithArgs(pgx.NamedArgs{
			"org_id":                      orgID,
			"name":                        "Production VPC",
			"environment":                 "production",
			"gateway_region":              "us",
			"status":                      models.PrivateConnectorStatusWaiting,
			"health_alert_url":            (*string)(nil),
			"offline_alert_after_seconds": 60,
			"created_by_user_id":          &userID,
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorGroupTestColumns).
			AddRow(groupID, orgID, "Production VPC", "production", "us", models.PrivateConnectorStatusWaiting, nil, 60, &userID, nil, now, now))

	err = NewPrivateConnectorStore(mock).CreateGroup(context.Background(), orgID, group)
	require.NoError(t, err, "CreateGroup should persist connector group metadata")
	require.Equal(t, groupID, group.ID, "CreateGroup should scan generated ID")
	require.Equal(t, orgID, group.OrgID, "CreateGroup should keep group scoped to the caller org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_ListGroupsByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM private_connector_groups WHERE org_id = @org_id`).
		WithArgs(pgx.NamedArgs{"org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(privateConnectorGroupTestColumns).
			AddRow(groupID, orgID, "Production VPC", "production", "us", models.PrivateConnectorStatusOffline, nil, 60, nil, nil, now, now))

	groups, err := NewPrivateConnectorStore(mock).ListGroups(context.Background(), orgID)
	require.NoError(t, err, "ListGroups should query connector groups by org")
	require.Equal(t, []models.PrivateConnectorGroup{{
		ID:                       groupID,
		OrgID:                    orgID,
		Name:                     "Production VPC",
		Environment:              "production",
		GatewayRegion:            "us",
		Status:                   models.PrivateConnectorStatusOffline,
		OfflineAlertAfterSeconds: 60,
		CreatedAt:                now,
		UpdatedAt:                now,
	}}, groups, "ListGroups should return only connector groups for the org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_UpdateGroupSettingsScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	now := time.Now().UTC()
	healthURL := "https://hooks.example.test/private-connectors"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`UPDATE private_connector_groups`).
		WithArgs(pgx.NamedArgs{
			"org_id":                      orgID,
			"id":                          groupID,
			"health_alert_url":            &healthURL,
			"offline_alert_after_seconds": 45,
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorGroupTestColumns).
			AddRow(groupID, orgID, "Production VPC", "production", "us", models.PrivateConnectorStatusOnline, &healthURL, 45, nil, nil, now, now))

	group, err := NewPrivateConnectorStore(mock).UpdateGroupSettings(context.Background(), orgID, groupID, &healthURL, 45)

	require.NoError(t, err, "UpdateGroupSettings should update the connector group scoped by org")
	require.Equal(t, groupID, group.ID, "UpdateGroupSettings should return the updated group")
	require.Equal(t, &healthURL, group.HealthAlertURL, "UpdateGroupSettings should return the configured health URL")
	require.Equal(t, 45, group.OfflineAlertAfterSeconds, "UpdateGroupSettings should return the configured offline threshold")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_DisableGroupScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`UPDATE private_connector_groups`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": groupID}).
		WillReturnRows(pgxmock.NewRows(privateConnectorGroupTestColumns).
			AddRow(groupID, orgID, "Production VPC", "production", "us", models.PrivateConnectorStatusDisabled, nil, 60, nil, &now, now, now))

	group, err := NewPrivateConnectorStore(mock).DisableGroup(context.Background(), orgID, groupID)

	require.NoError(t, err, "DisableGroup should disable a connector group scoped by org")
	require.Equal(t, models.PrivateConnectorStatusDisabled, group.Status, "DisableGroup should return disabled status")
	require.NotNil(t, group.DisabledAt, "DisableGroup should return disabled timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_MarkOfflineInstancesUsesGroupThreshold(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	now := time.Now().UTC()
	heartbeatAt := now.Add(-2 * time.Minute)
	instance := models.PrivateConnectorInstance{
		ID:                       instanceID,
		OrgID:                    orgID,
		ConnectorGroupID:         groupID,
		InstanceName:             "host-a",
		Status:                   models.PrivateConnectorInstanceStatusOffline,
		LastHeartbeatAt:          &heartbeatAt,
		HeartbeatIntervalSeconds: 5,
	}
	group := models.PrivateConnectorGroup{
		ID:                       groupID,
		OrgID:                    orgID,
		Name:                     "Production VPC",
		Status:                   models.PrivateConnectorStatusOffline,
		OfflineAlertAfterSeconds: 45,
	}
	instanceJSON, err := json.Marshal(instance)
	require.NoError(t, err, "test instance should marshal to JSON")
	groupJSON, err := json.Marshal(group)
	require.NoError(t, err, "test group should marshal to JSON")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`last_heartbeat_at < @now - make_interval\(secs => g\.offline_alert_after_seconds\)`).
		WithArgs(pgx.NamedArgs{"now": now}).
		WillReturnRows(pgxmock.NewRows([]string{"to_jsonb", "to_jsonb"}).AddRow(instanceJSON, groupJSON))

	transitions, err := NewPrivateConnectorStore(mock).MarkOfflineInstances(context.Background(), now)

	require.NoError(t, err, "MarkOfflineInstances should transition stale instances using group thresholds")
	require.Equal(t, []models.PrivateConnectorHealthTransition{{
		Group:    group,
		Instance: instance,
	}}, transitions, "MarkOfflineInstances should return transitioned instance and group")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_ConsumeDeploymentTokenByHash(t *testing.T) {
	t.Parallel()

	rawToken := "143pc_testtoken"
	tokenHash := HashAPIToken(rawToken)
	orgID := uuid.New()
	groupID := uuid.New()
	tokenID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)
	maxRegistrations := 1
	gatewayRegion := "us"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`(?s)UPDATE private_connector_deployment_tokens.*allowed_gateway_region.*allowed_source_cidrs`).
		WithArgs(pgx.NamedArgs{
			"token_hash":     tokenHash,
			"gateway_region": "us",
			"source_ip":      "203.0.113.10",
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorTokenTestColumns).
			AddRow(tokenID, orgID, groupID, "Interactive install", tokenHash, "143pc_test", models.PrivateConnectorTokenPresetInteractive,
				&maxRegistrations, 1, []string{}, &gatewayRegion, &expiresAt, &now, nil, nil, &userID, now))

	token, err := NewPrivateConnectorStore(mock).ConsumeDeploymentToken(context.Background(), rawToken, "us", "203.0.113.10")
	require.NoError(t, err, "ConsumeDeploymentToken should resolve and consume an active bootstrap token")
	require.Equal(t, orgID, token.OrgID, "ConsumeDeploymentToken should return the token org for bootstrap scoping")
	require.Equal(t, groupID, token.ConnectorGroupID, "ConsumeDeploymentToken should return the connector group")
	require.Equal(t, 1, token.RegistrationCount, "ConsumeDeploymentToken should increment registration count atomically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_CreateInstanceScopesByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	tokenID := uuid.New()
	instanceID := uuid.New()
	now := time.Now().UTC()
	instance := &models.PrivateConnectorInstance{
		OrgID:                    orgID,
		ConnectorGroupID:         groupID,
		DeploymentTokenID:        &tokenID,
		InstanceName:             "host-a",
		PublicKey:                "ed25519-public",
		Status:                   models.PrivateConnectorInstanceStatusOnline,
		Version:                  "v0.1.0",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		GatewayRegion:            "us",
		Capabilities:             []string{"victorialogs.query"},
		HeartbeatIntervalSeconds: 5,
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO private_connector_instances`).
		WithArgs(pgx.NamedArgs{
			"org_id":                     orgID,
			"connector_group_id":         groupID,
			"deployment_token_id":        &tokenID,
			"instance_name":              "host-a",
			"public_key":                 "ed25519-public",
			"status":                     models.PrivateConnectorInstanceStatusOnline,
			"version":                    "v0.1.0",
			"protocol":                   models.PrivateConnectorProtocolWebSocket,
			"gateway_region":             "us",
			"capabilities":               []string{"victorialogs.query"},
			"heartbeat_interval_seconds": 5,
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorInstanceTestColumns).
			AddRow(instanceID, orgID, groupID, &tokenID, "host-a", "ed25519-public", models.PrivateConnectorInstanceStatusOnline,
				"v0.1.0", models.PrivateConnectorProtocolWebSocket, "us", []string{"victorialogs.query"}, &now, 5, &now, nil, nil, nil, now, now))

	err = NewPrivateConnectorStore(mock).CreateInstance(context.Background(), orgID, instance)
	require.NoError(t, err, "CreateInstance should persist connector identity metadata")
	require.Equal(t, instanceID, instance.ID, "CreateInstance should scan generated instance ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_ListResourcesByGroupScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	now := time.Now().UTC()
	config := json.RawMessage(`{"url":"http://victorialogs:9428","limits":{"max_rows":500}}`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM private_connector_resources WHERE org_id = @org_id AND connector_group_id = @connector_group_id`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID}).
		WillReturnRows(pgxmock.NewRows(privateConnectorResourceTestColumns).
			AddRow(resourceID, orgID, groupID, "Production logs", models.PrivateConnectorResourceTypeVictoriaLogs,
				models.PrivateConnectorResourceModeLogs, config, models.PrivateConnectorConfigSourceUI, int64(1),
				models.PrivateConnectorResourceStatusConfigured, nil, nil, nil, nil, nil, now, now))

	resources, err := NewPrivateConnectorStore(mock).ListResources(context.Background(), orgID, groupID)
	require.NoError(t, err, "ListResources should query resources by org and connector group")
	require.Equal(t, []models.PrivateConnectorResource{{
		ID:               resourceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		DisplayName:      "Production logs",
		ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
		Mode:             models.PrivateConnectorResourceModeLogs,
		Config:           config,
		ConfigSource:     models.PrivateConnectorConfigSourceUI,
		ConfigVersion:    1,
		Status:           models.PrivateConnectorResourceStatusConfigured,
		CreatedAt:        now,
		UpdatedAt:        now,
	}}, resources, "ListResources should return resources scoped to the org and connector group")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_ListResourcesWithOnlineCapabilityScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	now := time.Now().UTC()
	config := json.RawMessage(`{"url_env":"143_CONNECTOR_VICTORIALOGS_URL"}`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT DISTINCT r\.id, r\.org_id, r\.connector_group_id.*r\.status IN \('configured', 'ready'\)`).
		WithArgs(pgx.NamedArgs{
			"org_id":        orgID,
			"resource_type": models.PrivateConnectorResourceTypeVictoriaLogs,
			"capability":    "victorialogs.query",
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorResourceTestColumns).
			AddRow(resourceID, orgID, groupID, "Production logs", models.PrivateConnectorResourceTypeVictoriaLogs,
				models.PrivateConnectorResourceModeLogs, config, models.PrivateConnectorConfigSourceUI, int64(1),
				models.PrivateConnectorResourceStatusReady, nil, nil, nil, nil, nil, now, now))

	resources, err := NewPrivateConnectorStore(mock).ListResourcesWithOnlineCapability(context.Background(), orgID, models.PrivateConnectorResourceTypeVictoriaLogs, "victorialogs.query")
	require.NoError(t, err, "ListResourcesWithOnlineCapability should query available resources by org")
	require.Equal(t, []models.PrivateConnectorResource{{
		ID:               resourceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		DisplayName:      "Production logs",
		ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
		Mode:             models.PrivateConnectorResourceModeLogs,
		Config:           config,
		ConfigSource:     models.PrivateConnectorConfigSourceUI,
		ConfigVersion:    1,
		Status:           models.PrivateConnectorResourceStatusReady,
		CreatedAt:        now,
		UpdatedAt:        now,
	}}, resources, "ListResourcesWithOnlineCapability should return ready resources backed by online capable instances")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_CreateRuntimeLeaseScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	leaseID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(30 * time.Minute)
	byteLimit := int64(1 << 30)
	lease := &models.PrivateConnectorRuntimeLease{
		OrgID:              orgID,
		RepositoryID:       repositoryID,
		PreviewID:          previewID,
		PreviewRuntimeID:   runtimeID,
		ConnectorGroupID:   groupID,
		ResourceID:         resourceID,
		Status:             models.PrivateConnectorRuntimeLeaseStatusActive,
		AccessMode:         models.PrivateConnectorRuntimeAccessModePostgresTCP,
		TargetHost:         "preview-db.internal",
		TargetPort:         5432,
		TargetDatabase:     "preview_123",
		LeaseTokenHash:     HashAPIToken("143pcl_secret"),
		LeaseTokenPrefix:   "143pcl_abcd",
		MaxConnections:     4,
		IdleTimeoutSeconds: 300,
		ByteLimit:          &byteLimit,
		ExpiresAt:          expiresAt,
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO private_connector_runtime_leases`).
		WithArgs(pgx.NamedArgs{
			"org_id":               orgID,
			"repository_id":        repositoryID,
			"preview_id":           previewID,
			"preview_runtime_id":   runtimeID,
			"connector_group_id":   groupID,
			"resource_id":          resourceID,
			"status":               models.PrivateConnectorRuntimeLeaseStatusActive,
			"access_mode":          models.PrivateConnectorRuntimeAccessModePostgresTCP,
			"target_host":          "preview-db.internal",
			"target_port":          5432,
			"target_database":      "preview_123",
			"lease_token_hash":     HashAPIToken("143pcl_secret"),
			"lease_token_prefix":   "143pcl_abcd",
			"max_connections":      4,
			"idle_timeout_seconds": 300,
			"byte_limit":           &byteLimit,
			"expires_at":           expiresAt,
		}).
		WillReturnRows(pgxmock.NewRows(privateConnectorRuntimeLeaseTestColumns).
			AddRow(leaseID, orgID, repositoryID, previewID, runtimeID, groupID, resourceID,
				models.PrivateConnectorRuntimeLeaseStatusActive, models.PrivateConnectorRuntimeAccessModePostgresTCP,
				"preview-db.internal", 5432, "preview_123", HashAPIToken("143pcl_secret"), "143pcl_abcd",
				4, 300, &byteLimit, expiresAt, nil, now, now))

	err = NewPrivateConnectorStore(mock).CreateRuntimeLease(context.Background(), orgID, lease)
	require.NoError(t, err, "CreateRuntimeLease should persist an org-scoped preview runtime lease")
	require.Equal(t, leaseID, lease.ID, "CreateRuntimeLease should scan generated lease ID")
	require.Equal(t, orgID, lease.OrgID, "CreateRuntimeLease should keep lease scoped to the caller org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_CountActiveRuntimeLeasesScopesOrgAndResource(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	resourceID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM private_connector_runtime_leases`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "resource_id": resourceID}).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := NewPrivateConnectorStore(mock).CountActiveRuntimeLeases(context.Background(), orgID, resourceID)

	require.NoError(t, err, "CountActiveRuntimeLeases should count active leases by org and resource")
	require.Equal(t, 2, count, "CountActiveRuntimeLeases should return the active lease count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_GetActiveRuntimeLeaseScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	leaseID := uuid.New()
	resourceID := uuid.New()
	groupID := uuid.New()
	repositoryID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM private_connector_runtime_leases WHERE org_id = @org_id AND id = @id`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": leaseID}).
		WillReturnRows(pgxmock.NewRows(privateConnectorRuntimeLeaseTestColumns).
			AddRow(leaseID, orgID, repositoryID, previewID, runtimeID, groupID, resourceID,
				models.PrivateConnectorRuntimeLeaseStatusActive, models.PrivateConnectorRuntimeAccessModePostgresTCP,
				"preview-db.internal", 5432, "preview_123", "sha256:token", "143pcl_abcd",
				4, 300, nil, expiresAt, nil, now, now))

	lease, err := NewPrivateConnectorStore(mock).GetActiveRuntimeLease(context.Background(), orgID, leaseID)
	require.NoError(t, err, "GetActiveRuntimeLease should load only active unexpired leases for the org")
	require.Equal(t, leaseID, lease.ID, "GetActiveRuntimeLease should return the requested lease")
	require.Equal(t, resourceID, lease.ResourceID, "GetActiveRuntimeLease should return the scoped resource")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_GetActiveRuntimeLeaseByToken(t *testing.T) {
	t.Parallel()

	rawToken := "143pcl_secret"
	tokenHash := HashAPIToken(rawToken)
	orgID := uuid.New()
	leaseID := uuid.New()
	resourceID := uuid.New()
	groupID := uuid.New()
	repositoryID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM private_connector_runtime_leases WHERE lease_token_hash = @lease_token_hash`).
		WithArgs(pgx.NamedArgs{"lease_token_hash": tokenHash}).
		WillReturnRows(pgxmock.NewRows(privateConnectorRuntimeLeaseTestColumns).
			AddRow(leaseID, orgID, repositoryID, previewID, runtimeID, groupID, resourceID,
				models.PrivateConnectorRuntimeLeaseStatusActive, models.PrivateConnectorRuntimeAccessModePostgresTCP,
				"preview-db.internal", 5432, "preview_123", tokenHash, "143pcl_abcd",
				4, 300, nil, expiresAt, nil, now, now))

	lease, err := NewPrivateConnectorStore(mock).GetActiveRuntimeLeaseByToken(context.Background(), rawToken)
	require.NoError(t, err, "GetActiveRuntimeLeaseByToken should resolve an active lease from the opaque token")
	require.Equal(t, orgID, lease.OrgID, "GetActiveRuntimeLeaseByToken should return the lease org for data-plane scoping")
	require.Equal(t, resourceID, lease.ResourceID, "GetActiveRuntimeLeaseByToken should return the target resource")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_RevokeRuntimeLeaseScopesOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	leaseID := uuid.New()
	resourceID := uuid.New()
	groupID := uuid.New()
	repositoryID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`UPDATE private_connector_runtime_leases`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": leaseID}).
		WillReturnRows(pgxmock.NewRows(privateConnectorRuntimeLeaseTestColumns).
			AddRow(leaseID, orgID, repositoryID, previewID, runtimeID, groupID, resourceID,
				models.PrivateConnectorRuntimeLeaseStatusRevoked, models.PrivateConnectorRuntimeAccessModePostgresTCP,
				"preview-db.internal", 5432, "preview_123", "sha256:token", "143pcl_abcd",
				4, 300, nil, expiresAt, &now, now, now))

	lease, err := NewPrivateConnectorStore(mock).RevokeRuntimeLease(context.Background(), orgID, leaseID)
	require.NoError(t, err, "RevokeRuntimeLease should revoke a lease by org and id")
	require.Equal(t, models.PrivateConnectorRuntimeLeaseStatusRevoked, lease.Status, "RevokeRuntimeLease should mark the lease revoked")
	require.NotNil(t, lease.RevokedAt, "RevokeRuntimeLease should record revocation time")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPrivateConnectorStore_ListRecentActionsScopesOrgAndGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	actionID := uuid.New()
	now := time.Now().UTC()
	resultCount := 1
	durationMs := 12

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM private_connector_actions`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID, "limit": 10}).
		WillReturnRows(pgxmock.NewRows(privateConnectorActionTestColumns).
			AddRow(actionID, orgID, groupID, nil, resourceID, "victorialogs.query",
				models.AuditActorSystem, "private_connector_dispatcher", nil, nil, nil,
				"nonce", "fingerprint", models.PrivateConnectorActionStatusSucceeded,
				nil, nil, &resultCount, &durationMs, now, &now))

	actions, err := NewPrivateConnectorStore(mock).ListRecentActions(context.Background(), orgID, groupID, 500)

	require.NoError(t, err, "ListRecentActions should query recent action audit rows by org and connector group")
	require.Equal(t, []models.PrivateConnectorAction{{
		ID:                 actionID,
		OrgID:              orgID,
		ConnectorGroupID:   groupID,
		ResourceID:         resourceID,
		Capability:         "victorialogs.query",
		ActorType:          models.AuditActorSystem,
		ActorID:            "private_connector_dispatcher",
		RequestNonce:       "nonce",
		RequestFingerprint: "fingerprint",
		Status:             models.PrivateConnectorActionStatusSucceeded,
		ResultCount:        ptrInt(1),
		DurationMs:         ptrInt(12),
		CreatedAt:          now,
		CompletedAt:        &now,
	}}, actions, "ListRecentActions should scan action rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
