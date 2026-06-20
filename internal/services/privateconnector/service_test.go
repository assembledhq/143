package privateconnector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	createdGroup          *models.PrivateConnectorGroup
	createdToken          *models.PrivateConnectorDeploymentToken
	groups                []models.PrivateConnectorGroup
	tokens                []models.PrivateConnectorDeploymentToken
	instances             []models.PrivateConnectorInstance
	resources             []models.PrivateConnectorResource
	recentActions         []models.PrivateConnectorAction
	availableResources    []models.PrivateConnectorResource
	consumedToken         models.PrivateConnectorDeploymentToken
	consumedRawToken      string
	consumedGatewayRegion string
	consumedSourceIP      string
	createdInstance       *models.PrivateConnectorInstance
	createdResource       *models.PrivateConnectorResource
	createdRuntimeLease   *models.PrivateConnectorRuntimeLease
	activeLeaseCount      int
	countedLeaseOrgID     uuid.UUID
	countedLeaseResource  uuid.UUID
	activeRuntimeLease    models.PrivateConnectorRuntimeLease
	activeLeaseToken      string
	revokedRuntimeLease   models.PrivateConnectorRuntimeLease
	rotatedInstance       models.PrivateConnectorInstance
	reconnectingInstance  models.PrivateConnectorInstance
	offlineTransitions    []PrivateConnectorHealthTransition
	instance              models.PrivateConnectorInstance
	resource              models.PrivateConnectorResource
	revokedTokenOrgID     uuid.UUID
	revokedTokenID        uuid.UUID
	revokedTokenUserID    uuid.UUID
	revokedInstanceOrgID  uuid.UUID
	revokedInstanceID     uuid.UUID
	revokedInstanceUserID uuid.UUID
	groupStatusOrgID      uuid.UUID
	groupStatusID         uuid.UUID
	groupStatus           models.PrivateConnectorStatus
	reconnectingOrgID     uuid.UUID
	reconnectingID        uuid.UUID
	offlineSweepTime      time.Time
	availableOrgID        uuid.UUID
	availableResourceType models.PrivateConnectorResourceType
	availableCapability   string
	updatedInstance       *models.PrivateConnectorInstance
	recordedAction        models.PrivateConnectorAction
	completedStatus       models.PrivateConnectorActionStatus
	completedErrorCode    *string
	completedErrorMessage *string
	completedResultCount  *int
	completedDurationMs   *int
	updatedGroupSettings  *models.PrivateConnectorGroup
	disabledGroupID       uuid.UUID
	disabledGroupOrgID    uuid.UUID
}

func (s *fakeStore) CreateGroup(_ context.Context, orgID uuid.UUID, group *models.PrivateConnectorGroup) error {
	group.ID = uuid.New()
	group.OrgID = orgID
	group.CreatedAt = time.Now().UTC()
	group.UpdatedAt = group.CreatedAt
	cp := *group
	s.createdGroup = &cp
	return nil
}

func (s *fakeStore) CreateDeploymentToken(_ context.Context, orgID uuid.UUID, token *models.PrivateConnectorDeploymentToken) error {
	token.ID = uuid.New()
	token.OrgID = orgID
	token.CreatedAt = time.Now().UTC()
	cp := *token
	s.createdToken = &cp
	return nil
}

func (s *fakeStore) ListGroups(_ context.Context, orgID uuid.UUID) ([]models.PrivateConnectorGroup, error) {
	out := make([]models.PrivateConnectorGroup, 0, len(s.groups))
	for _, group := range s.groups {
		if group.OrgID == orgID {
			out = append(out, group)
		}
	}
	return out, nil
}

func (s *fakeStore) GetGroup(_ context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	for _, group := range s.groups {
		if group.OrgID == orgID && group.ID == groupID {
			return group, nil
		}
	}
	return models.PrivateConnectorGroup{}, nil
}

func (s *fakeStore) UpdateGroupStatus(_ context.Context, orgID, groupID uuid.UUID, status models.PrivateConnectorStatus) error {
	s.groupStatusOrgID = orgID
	s.groupStatusID = groupID
	s.groupStatus = status
	return nil
}

func (s *fakeStore) UpdateGroupSettings(_ context.Context, orgID, groupID uuid.UUID, healthAlertURL *string, offlineAlertAfterSeconds int) (models.PrivateConnectorGroup, error) {
	group := models.PrivateConnectorGroup{
		ID:                       groupID,
		OrgID:                    orgID,
		HealthAlertURL:           healthAlertURL,
		OfflineAlertAfterSeconds: offlineAlertAfterSeconds,
	}
	s.updatedGroupSettings = &group
	return group, nil
}

func (s *fakeStore) DisableGroup(_ context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	group := models.PrivateConnectorGroup{ID: groupID, OrgID: orgID, Status: models.PrivateConnectorStatusDisabled}
	s.disabledGroupOrgID = orgID
	s.disabledGroupID = groupID
	return group, nil
}

func (s *fakeStore) ListDeploymentTokens(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.PrivateConnectorDeploymentToken, error) {
	return s.tokens, nil
}

func (s *fakeStore) RevokeDeploymentToken(_ context.Context, orgID uuid.UUID, tokenID, revokedBy uuid.UUID) (models.PrivateConnectorDeploymentToken, error) {
	s.revokedTokenOrgID = orgID
	s.revokedTokenID = tokenID
	s.revokedTokenUserID = revokedBy
	return models.PrivateConnectorDeploymentToken{ID: tokenID, RevokedByUserID: &revokedBy}, nil
}

func (s *fakeStore) ConsumeDeploymentToken(_ context.Context, rawToken, gatewayRegion, sourceIP string) (models.PrivateConnectorDeploymentToken, error) {
	s.consumedRawToken = rawToken
	s.consumedGatewayRegion = gatewayRegion
	s.consumedSourceIP = sourceIP
	if rawToken == "" {
		return models.PrivateConnectorDeploymentToken{}, errDeploymentTokenRequired
	}
	return s.consumedToken, nil
}

func (s *fakeStore) CreateInstance(_ context.Context, orgID uuid.UUID, instance *models.PrivateConnectorInstance) error {
	instance.ID = uuid.New()
	instance.OrgID = orgID
	cp := *instance
	s.createdInstance = &cp
	return nil
}

func (s *fakeStore) ListInstances(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.PrivateConnectorInstance, error) {
	return s.instances, nil
}

func (s *fakeStore) RevokeInstance(_ context.Context, orgID uuid.UUID, instanceID, revokedBy uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.revokedInstanceOrgID = orgID
	s.revokedInstanceID = instanceID
	s.revokedInstanceUserID = revokedBy
	return models.PrivateConnectorInstance{ID: instanceID, RevokedByUserID: &revokedBy}, nil
}

func (s *fakeStore) RotateInstancePublicKey(_ context.Context, _ uuid.UUID, instanceID uuid.UUID, publicKey string) (models.PrivateConnectorInstance, error) {
	s.rotatedInstance = models.PrivateConnectorInstance{ID: instanceID, PublicKey: publicKey}
	return s.rotatedInstance, nil
}

func (s *fakeStore) GetInstanceByID(_ context.Context, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.instance.ID = instanceID
	return s.instance, nil
}

func (s *fakeStore) UpdateInstanceHeartbeat(_ context.Context, orgID, instanceID uuid.UUID, version string, protocol models.PrivateConnectorProtocol, capabilities []string, heartbeatIntervalSeconds int) (models.PrivateConnectorInstance, error) {
	updated := s.instance
	updated.OrgID = orgID
	updated.ID = instanceID
	updated.Version = version
	updated.Protocol = protocol
	updated.Capabilities = capabilities
	updated.HeartbeatIntervalSeconds = heartbeatIntervalSeconds
	updated.Status = models.PrivateConnectorInstanceStatusOnline
	s.updatedInstance = &updated
	return updated, nil
}

func (s *fakeStore) MarkInstanceReconnecting(_ context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.reconnectingOrgID = orgID
	s.reconnectingID = instanceID
	if s.reconnectingInstance.ID != uuid.Nil {
		return s.reconnectingInstance, nil
	}
	return models.PrivateConnectorInstance{ID: instanceID, OrgID: orgID, Status: models.PrivateConnectorInstanceStatusReconnecting}, nil
}

func (s *fakeStore) MarkOfflineInstances(_ context.Context, now time.Time) ([]PrivateConnectorHealthTransition, error) {
	s.offlineSweepTime = now
	return s.offlineTransitions, nil
}

func (s *fakeStore) CreateResource(_ context.Context, orgID uuid.UUID, resource *models.PrivateConnectorResource) error {
	resource.ID = uuid.New()
	resource.OrgID = orgID
	cp := *resource
	s.createdResource = &cp
	return nil
}

func (s *fakeStore) ListResources(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.PrivateConnectorResource, error) {
	return s.resources, nil
}

func (s *fakeStore) ListResourcesWithOnlineCapability(_ context.Context, orgID uuid.UUID, resourceType models.PrivateConnectorResourceType, capability string) ([]models.PrivateConnectorResource, error) {
	s.availableOrgID = orgID
	s.availableResourceType = resourceType
	s.availableCapability = capability
	return s.availableResources, nil
}

func (s *fakeStore) GetResource(_ context.Context, _ uuid.UUID, resourceID uuid.UUID) (models.PrivateConnectorResource, error) {
	if s.resource.ID != uuid.Nil {
		return s.resource, nil
	}
	return models.PrivateConnectorResource{ID: resourceID}, nil
}

func (s *fakeStore) UpdateResourceTestResult(_ context.Context, _ uuid.UUID, resourceID uuid.UUID, status models.PrivateConnectorResourceStatus, testStatus, testError *string) (models.PrivateConnectorResource, error) {
	return models.PrivateConnectorResource{ID: resourceID, Status: status, LastTestStatus: testStatus, LastTestError: testError}, nil
}

func (s *fakeStore) CreateRuntimeLease(_ context.Context, orgID uuid.UUID, lease *models.PrivateConnectorRuntimeLease) error {
	lease.ID = uuid.New()
	lease.OrgID = orgID
	cp := *lease
	s.createdRuntimeLease = &cp
	return nil
}

func (s *fakeStore) CountActiveRuntimeLeases(_ context.Context, orgID, resourceID uuid.UUID) (int, error) {
	s.countedLeaseOrgID = orgID
	s.countedLeaseResource = resourceID
	return s.activeLeaseCount, nil
}

func (s *fakeStore) GetActiveRuntimeLease(_ context.Context, _ uuid.UUID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error) {
	if s.activeRuntimeLease.ID != uuid.Nil {
		return s.activeRuntimeLease, nil
	}
	return models.PrivateConnectorRuntimeLease{ID: leaseID, Status: models.PrivateConnectorRuntimeLeaseStatusActive}, nil
}

func (s *fakeStore) GetActiveRuntimeLeaseByToken(_ context.Context, rawToken string) (models.PrivateConnectorRuntimeLease, error) {
	s.activeLeaseToken = rawToken
	return s.activeRuntimeLease, nil
}

func (s *fakeStore) RevokeRuntimeLease(_ context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error) {
	s.revokedRuntimeLease = models.PrivateConnectorRuntimeLease{ID: leaseID, OrgID: orgID, Status: models.PrivateConnectorRuntimeLeaseStatusRevoked}
	return s.revokedRuntimeLease, nil
}

func (s *fakeStore) ListRecentActions(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ int) ([]models.PrivateConnectorAction, error) {
	return s.recentActions, nil
}

func (s *fakeStore) RecordAction(_ context.Context, orgID uuid.UUID, action *models.PrivateConnectorAction) error {
	action.ID = uuid.New()
	action.OrgID = orgID
	s.recordedAction = *action
	return nil
}

func (s *fakeStore) CompleteAction(_ context.Context, _ uuid.UUID, actionID uuid.UUID, status models.PrivateConnectorActionStatus, errorCode, errorMessage *string, resultCount, durationMs *int) (models.PrivateConnectorAction, error) {
	s.completedStatus = status
	s.completedErrorCode = errorCode
	s.completedErrorMessage = errorMessage
	s.completedResultCount = resultCount
	s.completedDurationMs = durationMs
	action := s.recordedAction
	action.ID = actionID
	action.Status = status
	action.ErrorCode = errorCode
	action.ErrorMessage = errorMessage
	action.ResultCount = resultCount
	action.DurationMs = durationMs
	return action, nil
}

func TestServiceCreateConnectorCreatesInteractiveInstallToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	svc := NewService(store, Config{
		InstallerURL: "https://get.143.dev/private-connector.sh",
		Now:          func() time.Time { return now },
	})

	result, err := svc.CreateConnector(context.Background(), orgID, userID, CreateConnectorRequest{
		Name:          " Production VPC ",
		Environment:   " production ",
		GatewayRegion: "eu",
	})
	require.NoError(t, err, "CreateConnector should create connector metadata and a bootstrap token")

	require.Equal(t, "Production VPC", store.createdGroup.Name, "CreateConnector should trim connector name")
	require.Equal(t, "production", store.createdGroup.Environment, "CreateConnector should trim environment label")
	require.Equal(t, "eu", store.createdGroup.GatewayRegion, "CreateConnector should persist selected gateway region")
	require.Equal(t, models.PrivateConnectorStatusWaiting, store.createdGroup.Status, "new connector should wait for registration")
	require.Equal(t, &userID, store.createdGroup.CreatedByUserID, "connector should record the creating user")
	require.Equal(t, store.createdGroup.ID, store.createdToken.ConnectorGroupID, "deployment token should target the new connector group")
	require.Equal(t, models.PrivateConnectorTokenPresetInteractive, store.createdToken.Preset, "default token should use the interactive preset")
	require.NotNil(t, store.createdToken.MaxRegistrations, "interactive token should cap registrations")
	require.Equal(t, 1, *store.createdToken.MaxRegistrations, "interactive token should be single-use by default")
	require.NotNil(t, store.createdToken.ExpiresAt, "interactive token should expire")
	require.Equal(t, now.Add(24*time.Hour), *store.createdToken.ExpiresAt, "interactive token should expire after roughly 24 hours")
	require.Equal(t, db.HashAPIToken(result.RawDeploymentToken), store.createdToken.TokenHash, "raw token should only be stored by hash")
	require.Contains(t, result.InstallCommand, result.RawDeploymentToken, "install command should include the one-time token once")
	require.Contains(t, result.InstallCommand, "143_CONNECTOR_TOKEN=", "install command should use the documented token env var")
}

func TestServiceCreateConnectorInstallCommandIncludesGatewayPublicKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	store := &fakeStore{}
	svc := NewService(store, Config{
		InstallerURL:     "https://get.143.dev/private-connector.sh",
		ActionSigningKey: privateKey,
	})

	result, err := svc.CreateConnector(context.Background(), orgID, userID, CreateConnectorRequest{Name: "Production VPC", GatewayRegion: "us"})

	require.NoError(t, err, "CreateConnector should create connector metadata")
	require.Contains(t, result.InstallCommand, "143_CONNECTOR_GATEWAY_PUBLIC_KEY=", "install command should pin the gateway public key")
	require.Contains(t, result.InstallCommand, base64.StdEncoding.EncodeToString(publicKey), "install command should include the derived public key")
	require.NotContains(t, result.InstallCommand, base64.StdEncoding.EncodeToString(privateKey), "install command must never expose the gateway private key")
}

func TestServiceUpdateConnectorSettingsPersistsHealthPolicy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	healthURL := "https://hooks.example.test/private-connector"
	store := &fakeStore{}
	svc := NewService(store, Config{})

	group, err := svc.UpdateConnectorSettings(context.Background(), orgID, groupID, UpdateConnectorSettingsRequest{
		HealthAlertURL:           &healthURL,
		OfflineAlertAfterSeconds: 45,
	})

	require.NoError(t, err, "UpdateConnectorSettings should persist valid health settings")
	require.Equal(t, groupID, group.ID, "UpdateConnectorSettings should return updated connector group")
	require.NotNil(t, store.updatedGroupSettings, "UpdateConnectorSettings should call the store")
	require.Equal(t, &healthURL, store.updatedGroupSettings.HealthAlertURL, "UpdateConnectorSettings should pass webhook URL")
	require.Equal(t, 45, store.updatedGroupSettings.OfflineAlertAfterSeconds, "UpdateConnectorSettings should pass offline alert threshold")
}

func TestServiceDisableConnectorDisablesGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	store := &fakeStore{}
	svc := NewService(store, Config{})

	group, err := svc.DisableConnector(context.Background(), orgID, groupID)

	require.NoError(t, err, "DisableConnector should disable a connector group")
	require.Equal(t, models.PrivateConnectorStatusDisabled, group.Status, "DisableConnector should return disabled group state")
	require.Equal(t, orgID, store.disabledGroupOrgID, "DisableConnector should scope by org")
	require.Equal(t, groupID, store.disabledGroupID, "DisableConnector should target requested group")
}

func TestServiceCreateDeploymentTokenAutomationPreset(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	maxRegistrations := 10
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{groups: []models.PrivateConnectorGroup{{
		ID:            groupID,
		OrgID:         orgID,
		Name:          "Production VPC",
		GatewayRegion: "us",
	}}}
	svc := NewService(store, Config{Now: func() time.Time { return now }})

	result, err := svc.CreateDeploymentToken(context.Background(), orgID, userID, CreateDeploymentTokenRequest{
		ConnectorGroupID:   groupID,
		Name:               "Terraform",
		Preset:             models.PrivateConnectorTokenPresetAutomation,
		MaxRegistrations:   &maxRegistrations,
		AllowedSourceCIDRs: []string{"203.0.113.0/24"},
		TokenFilePath:      "/run/secrets/143-connector-token",
	})

	require.NoError(t, err, "CreateDeploymentToken should create an automation deployment token")
	require.Equal(t, models.PrivateConnectorTokenPresetAutomation, store.createdToken.Preset, "automation request should persist automation preset")
	require.Equal(t, &maxRegistrations, store.createdToken.MaxRegistrations, "automation request should persist max registrations")
	require.Equal(t, []string{"203.0.113.0/24"}, store.createdToken.AllowedSourceCIDRs, "automation request should persist source CIDR policy")
	require.NotNil(t, store.createdToken.ExpiresAt, "automation tokens should default to a bounded expiry")
	require.Equal(t, now.Add(90*24*time.Hour), *store.createdToken.ExpiresAt, "automation default expiry should be 90 days")
	require.Contains(t, result.InstallCommand, "143_CONNECTOR_TOKEN_FILE='/run/secrets/143-connector-token'", "automation install command should support token files")
	require.NotContains(t, result.InstallCommand, result.RawDeploymentToken, "token-file install command should not inline the raw token")
}

func TestServiceCreateDeploymentTokenAllowsAutomationNoExpiry(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	store := &fakeStore{groups: []models.PrivateConnectorGroup{{ID: groupID, OrgID: orgID, GatewayRegion: "eu"}}}
	svc := NewService(store, Config{})

	_, err := svc.CreateDeploymentToken(context.Background(), orgID, userID, CreateDeploymentTokenRequest{
		ConnectorGroupID: groupID,
		Preset:           models.PrivateConnectorTokenPresetAutomation,
		NoExpiry:         true,
	})

	require.NoError(t, err, "CreateDeploymentToken should allow explicit no-expiry automation tokens")
	require.Nil(t, store.createdToken.ExpiresAt, "no-expiry automation token should not persist an expiry")
	require.NotNil(t, store.createdToken.AllowedGatewayRegion, "automation token should inherit connector region by default")
	require.Equal(t, "eu", *store.createdToken.AllowedGatewayRegion, "automation token should inherit group gateway region")
}

func TestServiceLogProvidersReturnAvailableVictoriaLogsResources(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		availableResources: []models.PrivateConnectorResource{{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
			Mode:             models.PrivateConnectorResourceModeLogs,
			Status:           models.PrivateConnectorResourceStatusConfigured,
		}},
	}
	svc := NewService(store, Config{})

	providers, err := svc.LogProviders(context.Background(), orgID)

	require.NoError(t, err, "LogProviders should list private connector log providers")
	require.Equal(t, orgID, store.availableOrgID, "LogProviders should scope discovery to the org")
	require.Equal(t, models.PrivateConnectorResourceTypeVictoriaLogs, store.availableResourceType, "LogProviders should discover VictoriaLogs resources")
	require.Equal(t, "victorialogs.query", store.availableCapability, "LogProviders should require an online query-capable instance")
	require.Len(t, providers, 1, "LogProviders should return one provider per available private connector resource")
	require.Equal(t, models.ProviderVictoriaLogs, providers[0].Name(), "LogProviders should expose VictoriaLogs through the shared log provider contract")
}

func TestServiceDatabaseProvidersReturnAvailablePostgresResources(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		availableResources: []models.PrivateConnectorResource{{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			ResourceType:     models.PrivateConnectorResourceTypePostgres,
			Mode:             models.PrivateConnectorResourceModeAgentReadOnly,
			Status:           models.PrivateConnectorResourceStatusConfigured,
		}},
	}
	svc := NewService(store, Config{})

	providers, err := svc.DatabaseProviders(context.Background(), orgID)

	require.NoError(t, err, "DatabaseProviders should list private connector database providers")
	require.Equal(t, orgID, store.availableOrgID, "DatabaseProviders should scope discovery to the org")
	require.Equal(t, models.PrivateConnectorResourceTypePostgres, store.availableResourceType, "DatabaseProviders should discover Postgres resources")
	require.Equal(t, "postgres.query", store.availableCapability, "DatabaseProviders should require an online query-capable instance")
	require.Len(t, providers, 1, "DatabaseProviders should return one provider per available private connector resource")
	require.Equal(t, models.ProviderPostgres, providers[0].Name(), "DatabaseProviders should expose Postgres through the shared database provider contract")
}

func TestServiceConnectorConfigPushSignsUIResources(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		resources: []models.PrivateConnectorResource{
			{
				ID:               resourceID,
				OrgID:            orgID,
				ConnectorGroupID: groupID,
				DisplayName:      "Production logs",
				ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
				Mode:             models.PrivateConnectorResourceModeLogs,
				Config:           json.RawMessage(`{"base_url":"http://victorialogs:9428","max_rows":100}`),
				ConfigSource:     models.PrivateConnectorConfigSourceUI,
				ConfigVersion:    4,
				Status:           models.PrivateConnectorResourceStatusConfigured,
			},
			{
				ID:               uuid.New(),
				OrgID:            orgID,
				ConnectorGroupID: groupID,
				DisplayName:      "Disabled DB",
				ResourceType:     models.PrivateConnectorResourceTypePostgres,
				Mode:             models.PrivateConnectorResourceModeAgentReadOnly,
				Config:           json.RawMessage(`{"dsn_env":"PROD_DATABASE_URL"}`),
				ConfigSource:     models.PrivateConnectorConfigSourceUI,
				ConfigVersion:    5,
				Status:           models.PrivateConnectorResourceStatusDisabled,
			},
		},
	}
	svc := NewService(store, Config{
		ActionSigningKey: privateKey,
		Now:              func() time.Time { return now },
	})

	frame, signature, err := svc.ConnectorConfigPush(context.Background(), models.PrivateConnectorInstance{
		OrgID:            orgID,
		ConnectorGroupID: groupID,
	})

	require.NoError(t, err, "ConnectorConfigPush should render and sign resource config")
	require.Equal(t, orgID, frame.OrgID, "config push should be scoped to the instance org")
	require.Equal(t, groupID, frame.ConnectorID, "config push should be scoped to the connector group")
	require.Equal(t, int64(4), frame.Version, "config push version should use the highest enabled resource config version")
	require.Len(t, frame.Resources, 1, "config push should exclude disabled resources")
	require.Equal(t, resourceID, frame.Resources[0].ID, "config push should include UI-managed resource")
	require.JSONEq(t, `{"base_url":"http://victorialogs:9428","max_rows":100}`, string(frame.Resources[0].Config), "config push should preserve resource config bytes")
	require.NoError(t, connector.VerifyConfigPush(publicKey, frame, signature, connector.ConfigPushVerifyOptions{
		OrgID:       orgID,
		ConnectorID: groupID,
		MinVersion:  frame.Version,
		Now:         func() time.Time { return now.Add(time.Second) },
	}), "config push signature should verify with the gateway public key")
}

func TestServiceRevokeDeploymentToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	store := &fakeStore{}
	svc := NewService(store, Config{})

	token, err := svc.RevokeDeploymentToken(context.Background(), orgID, tokenID, userID)

	require.NoError(t, err, "RevokeDeploymentToken should delegate to the store")
	require.Equal(t, tokenID, token.ID, "RevokeDeploymentToken should return the revoked token")
	require.Equal(t, orgID, store.revokedTokenOrgID, "RevokeDeploymentToken should scope by org")
	require.Equal(t, tokenID, store.revokedTokenID, "RevokeDeploymentToken should pass token id")
	require.Equal(t, userID, store.revokedTokenUserID, "RevokeDeploymentToken should record revoking user")
}

func TestServiceRevokeInstance(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()
	store := &fakeStore{}
	svc := NewService(store, Config{})

	instance, err := svc.RevokeInstance(context.Background(), orgID, instanceID, userID)

	require.NoError(t, err, "RevokeInstance should delegate to the store")
	require.Equal(t, instanceID, instance.ID, "RevokeInstance should return the revoked instance")
	require.Equal(t, orgID, store.revokedInstanceOrgID, "RevokeInstance should scope by org")
	require.Equal(t, instanceID, store.revokedInstanceID, "RevokeInstance should pass instance id")
	require.Equal(t, userID, store.revokedInstanceUserID, "RevokeInstance should record revoking user")
}

func TestServiceRegisterInstanceConsumesDeploymentTokenAndStoresIdentity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	tokenID := uuid.New()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")

	store := &fakeStore{
		consumedToken: models.PrivateConnectorDeploymentToken{
			ID:               tokenID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		},
	}
	svc := NewService(store, Config{})

	result, err := svc.RegisterInstance(context.Background(), RegisterInstanceRequest{
		DeploymentToken:          "143pc_bootstrap",
		InstanceName:             "host-a",
		PublicKey:                base64.StdEncoding.EncodeToString(publicKey),
		Version:                  "v0.1.0",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		GatewayRegion:            "us",
		SourceIP:                 "203.0.113.10",
		Capabilities:             []string{"victorialogs.query"},
		HeartbeatIntervalSeconds: 5,
	})
	require.NoError(t, err, "RegisterInstance should exchange deployment token for durable identity metadata")
	require.Equal(t, "143pc_bootstrap", store.consumedRawToken, "RegisterInstance should consume the provided deployment token")
	require.Equal(t, "us", store.consumedGatewayRegion, "RegisterInstance should enforce token gateway-region policy at consume time")
	require.Equal(t, "203.0.113.10", store.consumedSourceIP, "RegisterInstance should enforce token source CIDR policy at consume time")
	require.Equal(t, orgID, store.createdInstance.OrgID, "registered instance should use the token org")
	require.Equal(t, groupID, store.createdInstance.ConnectorGroupID, "registered instance should join the token group")
	require.Equal(t, &tokenID, store.createdInstance.DeploymentTokenID, "registered instance should retain bootstrap token provenance")
	require.Equal(t, "host-a", store.createdInstance.InstanceName, "registered instance should persist reported instance name")
	require.Equal(t, base64.StdEncoding.EncodeToString(publicKey), store.createdInstance.PublicKey, "registered instance should persist the Ed25519 public key")
	require.Equal(t, result.Instance.ID, store.createdInstance.ID, "RegisterInstance should return the created instance")
	require.Equal(t, orgID, store.groupStatusOrgID, "RegisterInstance should update the connector group in the token org")
	require.Equal(t, groupID, store.groupStatusID, "RegisterInstance should mark the registered connector group online")
	require.Equal(t, models.PrivateConnectorStatusOnline, store.groupStatus, "RegisterInstance should mark connector group online")
}

func TestServiceRegisterInstanceRejectsInvalidPublicKey(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	svc := NewService(store, Config{})

	_, err := svc.RegisterInstance(context.Background(), RegisterInstanceRequest{
		DeploymentToken: "143pc_bootstrap",
		InstanceName:    "host-a",
		PublicKey:       base64.StdEncoding.EncodeToString([]byte("not-ed25519")),
		Protocol:        models.PrivateConnectorProtocolWebSocket,
	})
	require.ErrorIs(t, err, ErrInvalidPublicKey, "RegisterInstance should reject non-Ed25519 public keys")
	require.Nil(t, store.createdInstance, "RegisterInstance should fail before creating an instance")
}

func TestServiceHeartbeatVerifiesInstanceSignature(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	instanceID := uuid.New()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	payload := HeartbeatRequest{
		Version:                  "v0.1.1",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		Capabilities:             []string{"victorialogs.query", "victorialogs.fields"},
		HeartbeatIntervalSeconds: 5,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err, "heartbeat payload should marshal")
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, body))

	store := &fakeStore{
		instance: models.PrivateConnectorInstance{
			ID:               instanceID,
			OrgID:            orgID,
			ConnectorGroupID: uuid.New(),
			PublicKey:        base64.StdEncoding.EncodeToString(publicKey),
			Status:           models.PrivateConnectorInstanceStatusOnline,
		},
	}
	svc := NewService(store, Config{})

	updated, err := svc.RecordHeartbeat(context.Background(), instanceID, body, signature)
	require.NoError(t, err, "RecordHeartbeat should accept a valid Ed25519 signature")
	require.Equal(t, "v0.1.1", updated.Version, "RecordHeartbeat should persist connector version")
	require.Equal(t, []string{"victorialogs.query", "victorialogs.fields"}, store.updatedInstance.Capabilities, "RecordHeartbeat should persist advertised capabilities")
	require.Equal(t, orgID, store.groupStatusOrgID, "RecordHeartbeat should update connector group status in the instance org")
	require.Equal(t, store.instance.ConnectorGroupID, store.groupStatusID, "RecordHeartbeat should update the instance connector group")
	require.Equal(t, models.PrivateConnectorStatusOnline, store.groupStatus, "RecordHeartbeat should mark connector group online")

	_, err = svc.RecordHeartbeat(context.Background(), instanceID, body, base64.StdEncoding.EncodeToString([]byte("bad signature")))
	require.ErrorIs(t, err, ErrInvalidSignature, "RecordHeartbeat should reject invalid signatures")
}

func TestServiceHeartbeatDoesNotReenableDisabledGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	body, err := json.Marshal(HeartbeatRequest{
		Version:                  "v0.1.1",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		Capabilities:             []string{"victorialogs.query"},
		HeartbeatIntervalSeconds: 5,
	})
	require.NoError(t, err, "heartbeat payload should marshal")
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, body))
	store := &fakeStore{
		instance: models.PrivateConnectorInstance{
			ID:               instanceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			PublicKey:        base64.StdEncoding.EncodeToString(publicKey),
			Status:           models.PrivateConnectorInstanceStatusOnline,
		},
		groups: []models.PrivateConnectorGroup{{
			ID:     groupID,
			OrgID:  orgID,
			Status: models.PrivateConnectorStatusDisabled,
		}},
	}
	svc := NewService(store, Config{})

	updated, err := svc.RecordHeartbeat(context.Background(), instanceID, body, signature)

	require.NoError(t, err, "RecordHeartbeat should still accept a valid daemon heartbeat")
	require.Equal(t, instanceID, updated.ID, "RecordHeartbeat should return the updated instance")
	require.Empty(t, store.groupStatus, "RecordHeartbeat should not mark a disabled connector group online")
}

func TestServiceRecordSessionDisconnectMarksReconnecting(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	store := &fakeStore{}
	svc := NewService(store, Config{})

	err := svc.RecordSessionDisconnect(context.Background(), models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
	})

	require.NoError(t, err, "RecordSessionDisconnect should persist reconnecting state")
	require.Equal(t, orgID, store.reconnectingOrgID, "RecordSessionDisconnect should scope reconnecting update by org")
	require.Equal(t, instanceID, store.reconnectingID, "RecordSessionDisconnect should target disconnected instance")
	require.Equal(t, groupID, store.groupStatusID, "RecordSessionDisconnect should update connector group status")
	require.Equal(t, models.PrivateConnectorStatusReconnecting, store.groupStatus, "RecordSessionDisconnect should mark group reconnecting")
}

func TestServiceRecordSessionDisconnectDoesNotReenableDisabledGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	store := &fakeStore{
		groups: []models.PrivateConnectorGroup{{
			ID:     groupID,
			OrgID:  orgID,
			Status: models.PrivateConnectorStatusDisabled,
		}},
	}
	svc := NewService(store, Config{})

	err := svc.RecordSessionDisconnect(context.Background(), models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
	})

	require.NoError(t, err, "RecordSessionDisconnect should persist instance reconnecting state")
	require.Equal(t, instanceID, store.reconnectingID, "RecordSessionDisconnect should still mark the instance reconnecting")
	require.Empty(t, store.groupStatus, "RecordSessionDisconnect should not move disabled groups back to reconnecting")
}

func TestServiceMarkOfflineConnectorsUsesSweepTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{offlineTransitions: []PrivateConnectorHealthTransition{{
		Group:    models.PrivateConnectorGroup{ID: uuid.New(), OrgID: uuid.New(), Name: "Production VPC"},
		Instance: models.PrivateConnectorInstance{ID: uuid.New(), Status: models.PrivateConnectorInstanceStatusOffline},
	}}}
	svc := NewService(store, Config{Now: func() time.Time { return now }})

	count, err := svc.MarkOfflineConnectors(context.Background())

	require.NoError(t, err, "MarkOfflineConnectors should complete offline sweep")
	require.Equal(t, 1, count, "MarkOfflineConnectors should report transitioned instances")
	require.Equal(t, now, store.offlineSweepTime, "MarkOfflineConnectors should pass current time so the store can apply per-group thresholds")
}

func TestServiceListConnectorsIncludesInstancesResourcesAndTokens(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	store := &fakeStore{
		groups: []models.PrivateConnectorGroup{{
			ID:    groupID,
			OrgID: orgID,
			Name:  "Production VPC",
		}},
		instances: []models.PrivateConnectorInstance{{
			ID:               uuid.New(),
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			Status:           models.PrivateConnectorInstanceStatusOnline,
		}},
		resources: []models.PrivateConnectorResource{{
			ID:               uuid.New(),
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			DisplayName:      "Production logs",
		}},
		tokens: []models.PrivateConnectorDeploymentToken{{
			ID:               uuid.New(),
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			TokenHash:        "sha256:secret",
			TokenPrefix:      "143pc_safe",
		}},
	}
	svc := NewService(store, Config{})

	connectors, err := svc.ListConnectors(context.Background(), orgID)
	require.NoError(t, err, "ListConnectors should load connector summaries")
	require.Len(t, connectors, 1, "ListConnectors should return the org connector")
	require.Len(t, connectors[0].Instances, 1, "ListConnectors should include connector instances")
	require.Len(t, connectors[0].Resources, 1, "ListConnectors should include configured resources")
	require.Len(t, connectors[0].DeploymentTokens, 1, "ListConnectors should include deployment token metadata")
	require.Empty(t, connectors[0].DeploymentTokens[0].TokenHash, "ListConnectors should not expose token hashes")
}

func TestServiceCreateResourceRejectsInlineSecrets(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	svc := NewService(store, Config{})

	_, err := svc.CreateResource(context.Background(), uuid.New(), uuid.New(), uuid.New(), CreateResourceRequest{
		DisplayName:  "Production DB",
		ResourceType: models.PrivateConnectorResourceTypePostgres,
		Mode:         models.PrivateConnectorResourceModeAgentReadOnly,
		Config:       json.RawMessage(`{"dsn":"postgres://readonly:secret@db/prod"}`),
	})
	require.ErrorIs(t, err, ErrInlineSecretRejected, "CreateResource should reject UI-managed inline credentials")
	require.Nil(t, store.createdResource, "CreateResource should fail before writing unsafe config")

	resource, err := svc.CreateResource(context.Background(), uuid.New(), uuid.New(), uuid.New(), CreateResourceRequest{
		DisplayName:  "Production DB",
		ResourceType: models.PrivateConnectorResourceTypePostgres,
		Mode:         models.PrivateConnectorResourceModeAgentReadOnly,
		Config:       json.RawMessage(`{"dsn_env":"PROD_READONLY_DATABASE_URL"}`),
	})
	require.NoError(t, err, "CreateResource should allow secret references through environment variable names")
	require.Equal(t, resource.ID, store.createdResource.ID, "CreateResource should persist safe resource config")
}

func TestServiceCreateRuntimeLeaseValidatesPreviewPostgresResource(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		resource: models.PrivateConnectorResource{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			ResourceType:     models.PrivateConnectorResourceTypePostgres,
			Mode:             models.PrivateConnectorResourceModePreviewRuntime,
			Status:           models.PrivateConnectorResourceStatusConfigured,
			Config: json.RawMessage(`{
				"target_host":"preview-db.internal",
				"target_port":5432,
				"target_database":"preview_123",
				"max_connections":4,
				"idle_timeout_seconds":120,
				"byte_limit":1048576
			}`),
		},
	}
	svc := NewService(store, Config{Now: func() time.Time { return now }})

	result, err := svc.CreateRuntimeLease(context.Background(), orgID, CreateRuntimeLeaseRequest{
		RepositoryID:     repositoryID,
		PreviewID:        previewID,
		PreviewRuntimeID: runtimeID,
		ResourceID:       resourceID,
		TTL:              20 * time.Minute,
	})

	require.NoError(t, err, "CreateRuntimeLease should create a bounded preview runtime lease")
	require.NotEmpty(t, result.RawLeaseToken, "CreateRuntimeLease should return the lease token only once")
	require.Equal(t, repositoryID, store.createdRuntimeLease.RepositoryID, "runtime lease should be scoped to the repository")
	require.Equal(t, previewID, store.createdRuntimeLease.PreviewID, "runtime lease should be scoped to the preview")
	require.Equal(t, runtimeID, store.createdRuntimeLease.PreviewRuntimeID, "runtime lease should be scoped to the preview runtime")
	require.Equal(t, groupID, store.createdRuntimeLease.ConnectorGroupID, "runtime lease should target the resource connector group")
	require.Equal(t, resourceID, store.createdRuntimeLease.ResourceID, "runtime lease should target the preview resource")
	require.Equal(t, models.PrivateConnectorRuntimeLeaseStatusActive, store.createdRuntimeLease.Status, "runtime lease should start active")
	require.Equal(t, models.PrivateConnectorRuntimeAccessModePostgresTCP, store.createdRuntimeLease.AccessMode, "runtime lease should expose postgres tcp only")
	require.Equal(t, "preview-db.internal", store.createdRuntimeLease.TargetHost, "runtime lease should persist allowed target host")
	require.Equal(t, 5432, store.createdRuntimeLease.TargetPort, "runtime lease should persist allowed target port")
	require.Equal(t, "preview_123", store.createdRuntimeLease.TargetDatabase, "runtime lease should persist allowed target database")
	require.Equal(t, 4, store.createdRuntimeLease.MaxConnections, "runtime lease should persist max connection policy")
	require.Equal(t, 120, store.createdRuntimeLease.IdleTimeoutSeconds, "runtime lease should persist idle timeout policy")
	require.NotNil(t, store.createdRuntimeLease.ByteLimit, "runtime lease should persist byte limit policy")
	require.Equal(t, int64(1048576), *store.createdRuntimeLease.ByteLimit, "runtime lease should persist configured byte limit")
	require.Equal(t, now.Add(20*time.Minute), store.createdRuntimeLease.ExpiresAt, "runtime lease should honor requested TTL under the cap")
	require.Equal(t, db.HashAPIToken(result.RawLeaseToken), store.createdRuntimeLease.LeaseTokenHash, "runtime lease should store only a token hash")
	require.Contains(t, result.DatabaseURL, result.RawLeaseToken, "runtime database URL should carry the preview-scoped lease token")
}

func TestServiceCreateRuntimeLeaseEnforcesRepositoryAllowlist(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	allowedRepoID := uuid.New()
	disallowedRepoID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		resource: models.PrivateConnectorResource{
			ID:           resourceID,
			OrgID:        orgID,
			ResourceType: models.PrivateConnectorResourceTypePostgres,
			Mode:         models.PrivateConnectorResourceModePreviewRuntime,
			Status:       models.PrivateConnectorResourceStatusConfigured,
			Config: json.RawMessage(`{
				"target_host":"preview-db.internal",
				"target_database":"preview_123",
				"allowed_repository_ids":["` + allowedRepoID.String() + `"]
			}`),
		},
	}
	svc := NewService(store, Config{})

	_, err := svc.CreateRuntimeLease(context.Background(), orgID, CreateRuntimeLeaseRequest{
		RepositoryID:     disallowedRepoID,
		PreviewID:        uuid.New(),
		PreviewRuntimeID: uuid.New(),
		ResourceID:       resourceID,
	})

	require.ErrorIs(t, err, ErrInvalidRuntimeLeasePolicy, "CreateRuntimeLease should deny repositories outside the preview resource allowlist")
	require.Nil(t, store.createdRuntimeLease, "CreateRuntimeLease should fail before issuing a lease token")
}

func TestServiceCreateRuntimeLeaseEnforcesActiveLeaseLimit(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		activeLeaseCount: 2,
		resource: models.PrivateConnectorResource{
			ID:           resourceID,
			OrgID:        orgID,
			ResourceType: models.PrivateConnectorResourceTypePostgres,
			Mode:         models.PrivateConnectorResourceModePreviewRuntime,
			Status:       models.PrivateConnectorResourceStatusConfigured,
			Config: json.RawMessage(`{
				"target_host":"preview-db.internal",
				"target_database":"preview_123",
				"max_active_leases":2
			}`),
		},
	}
	svc := NewService(store, Config{})

	_, err := svc.CreateRuntimeLease(context.Background(), orgID, CreateRuntimeLeaseRequest{
		RepositoryID:     repositoryID,
		PreviewID:        uuid.New(),
		PreviewRuntimeID: uuid.New(),
		ResourceID:       resourceID,
	})

	require.ErrorIs(t, err, ErrInvalidRuntimeLeasePolicy, "CreateRuntimeLease should deny leases beyond the configured active cap")
	require.Equal(t, orgID, store.countedLeaseOrgID, "CreateRuntimeLease should count active leases within org")
	require.Equal(t, resourceID, store.countedLeaseResource, "CreateRuntimeLease should count active leases for the requested resource")
	require.Nil(t, store.createdRuntimeLease, "CreateRuntimeLease should fail before issuing a lease token")
}

func TestServiceCreateRuntimeLeaseRejectsNonPreviewResource(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	store := &fakeStore{
		resource: models.PrivateConnectorResource{
			ID:           uuid.New(),
			OrgID:        orgID,
			ResourceType: models.PrivateConnectorResourceTypePostgres,
			Mode:         models.PrivateConnectorResourceModeAgentReadOnly,
			Status:       models.PrivateConnectorResourceStatusConfigured,
			Config:       json.RawMessage(`{"target_host":"preview-db.internal","target_port":5432}`),
		},
	}
	svc := NewService(store, Config{})

	_, err := svc.CreateRuntimeLease(context.Background(), orgID, CreateRuntimeLeaseRequest{
		RepositoryID:     uuid.New(),
		PreviewID:        uuid.New(),
		PreviewRuntimeID: uuid.New(),
		ResourceID:       store.resource.ID,
	})

	require.ErrorIs(t, err, ErrUnsupportedRuntimeResource, "CreateRuntimeLease should reject non-preview-runtime resources")
	require.Nil(t, store.createdRuntimeLease, "CreateRuntimeLease should fail before writing an unsafe lease")
}

func TestServiceAuthorizeRuntimeLeaseByToken(t *testing.T) {
	t.Parallel()

	leaseID := uuid.New()
	store := &fakeStore{
		activeRuntimeLease: models.PrivateConnectorRuntimeLease{
			ID:     leaseID,
			OrgID:  uuid.New(),
			Status: models.PrivateConnectorRuntimeLeaseStatusActive,
		},
	}
	svc := NewService(store, Config{})

	lease, err := svc.AuthorizeRuntimeLease(context.Background(), " 143pcl_secret ")

	require.NoError(t, err, "AuthorizeRuntimeLease should resolve active leases from opaque tokens")
	require.Equal(t, leaseID, lease.ID, "AuthorizeRuntimeLease should return the active lease")
	require.Equal(t, "143pcl_secret", store.activeLeaseToken, "AuthorizeRuntimeLease should trim the opaque token")

	_, err = svc.AuthorizeRuntimeLease(context.Background(), " ")
	require.ErrorIs(t, err, ErrRuntimeLeaseTokenRequired, "AuthorizeRuntimeLease should reject blank lease tokens")
}

func TestServiceRevokeRuntimeLease(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	leaseID := uuid.New()
	store := &fakeStore{}
	svc := NewService(store, Config{})

	lease, err := svc.RevokeRuntimeLease(context.Background(), orgID, leaseID)

	require.NoError(t, err, "RevokeRuntimeLease should delegate to the store")
	require.Equal(t, leaseID, lease.ID, "RevokeRuntimeLease should return revoked lease")
	require.Equal(t, orgID, store.revokedRuntimeLease.OrgID, "RevokeRuntimeLease should scope revocation to org")
	require.Equal(t, models.PrivateConnectorRuntimeLeaseStatusRevoked, store.revokedRuntimeLease.Status, "RevokeRuntimeLease should mark revoked")
}
