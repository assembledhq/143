package privateconnector

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var (
	ErrNameRequired               = errors.New("connector name is required")
	ErrDeploymentTokenRequired    = errors.New("deployment token is required")
	errDeploymentTokenRequired    = ErrDeploymentTokenRequired
	ErrInvalidGatewayRegion       = errors.New("invalid gateway region")
	ErrInvalidPublicKey           = errors.New("invalid connector public key")
	ErrInvalidSignature           = errors.New("invalid connector signature")
	ErrInvalidHeartbeat           = errors.New("invalid heartbeat payload")
	ErrInlineSecretRejected       = errors.New("inline connector secrets are not allowed")
	ErrActionDispatchUnavailable  = errors.New("private connector action dispatch unavailable")
	ErrUnsupportedResourceProbe   = errors.New("private connector resource probe unsupported")
	ErrConfigPushUnavailable      = errors.New("private connector config push unavailable")
	ErrUnsupportedRuntimeResource = errors.New("private connector runtime resource unsupported")
	ErrInvalidRuntimeLeasePolicy  = errors.New("private connector runtime lease policy invalid")
	ErrRuntimeLeaseTokenRequired  = errors.New("private connector runtime lease token is required")
	ErrConnectorDisabled          = errors.New("private connector is disabled")
)

const (
	defaultRuntimeLeaseTTL            = 30 * time.Minute
	maxRuntimeLeaseTTL                = 2 * time.Hour
	defaultRuntimeLeaseMaxConnections = 4
	defaultRuntimeLeaseIdleTimeout    = 300
	defaultRuntimeLeaseProxyHost      = "private-connector-proxy.internal"
	defaultRuntimeLeasePostgresPort   = 5432
	defaultAutomationTokenTTL         = 90 * 24 * time.Hour
	defaultOfflineAlertAfter          = 60 * time.Second
)

type Store interface {
	CreateGroup(ctx context.Context, orgID uuid.UUID, group *models.PrivateConnectorGroup) error
	ListGroups(ctx context.Context, orgID uuid.UUID) ([]models.PrivateConnectorGroup, error)
	GetGroup(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error)
	UpdateGroupStatus(ctx context.Context, orgID, groupID uuid.UUID, status models.PrivateConnectorStatus) error
	UpdateGroupSettings(ctx context.Context, orgID, groupID uuid.UUID, healthAlertURL *string, offlineAlertAfterSeconds int) (models.PrivateConnectorGroup, error)
	DisableGroup(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error)
	CreateDeploymentToken(ctx context.Context, orgID uuid.UUID, token *models.PrivateConnectorDeploymentToken) error
	ListDeploymentTokens(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorDeploymentToken, error)
	RevokeDeploymentToken(ctx context.Context, orgID, tokenID, revokedBy uuid.UUID) (models.PrivateConnectorDeploymentToken, error)
	ConsumeDeploymentToken(ctx context.Context, rawToken, gatewayRegion, sourceIP string) (models.PrivateConnectorDeploymentToken, error)
	CreateInstance(ctx context.Context, orgID uuid.UUID, instance *models.PrivateConnectorInstance) error
	ListInstances(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorInstance, error)
	GetInstanceByID(ctx context.Context, instanceID uuid.UUID) (models.PrivateConnectorInstance, error)
	UpdateInstanceHeartbeat(ctx context.Context, orgID, instanceID uuid.UUID, version string, protocol models.PrivateConnectorProtocol, capabilities []string, heartbeatIntervalSeconds int) (models.PrivateConnectorInstance, error)
	RevokeInstance(ctx context.Context, orgID, instanceID, revokedBy uuid.UUID) (models.PrivateConnectorInstance, error)
	RotateInstancePublicKey(ctx context.Context, orgID, instanceID uuid.UUID, publicKey string) (models.PrivateConnectorInstance, error)
	MarkInstanceReconnecting(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error)
	MarkOfflineInstances(ctx context.Context, now time.Time) ([]models.PrivateConnectorHealthTransition, error)
	CreateResource(ctx context.Context, orgID uuid.UUID, resource *models.PrivateConnectorResource) error
	ListResources(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorResource, error)
	ListResourcesWithOnlineCapability(ctx context.Context, orgID uuid.UUID, resourceType models.PrivateConnectorResourceType, capability string) ([]models.PrivateConnectorResource, error)
	GetResource(ctx context.Context, orgID, resourceID uuid.UUID) (models.PrivateConnectorResource, error)
	UpdateResourceTestResult(ctx context.Context, orgID, resourceID uuid.UUID, status models.PrivateConnectorResourceStatus, testStatus, testError *string) (models.PrivateConnectorResource, error)
	CreateRuntimeLease(ctx context.Context, orgID uuid.UUID, lease *models.PrivateConnectorRuntimeLease) error
	CountActiveRuntimeLeases(ctx context.Context, orgID, resourceID uuid.UUID) (int, error)
	GetActiveRuntimeLease(ctx context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error)
	GetActiveRuntimeLeaseByToken(ctx context.Context, rawToken string) (models.PrivateConnectorRuntimeLease, error)
	RevokeRuntimeLease(ctx context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error)
	ListRecentActions(ctx context.Context, orgID, groupID uuid.UUID, limit int) ([]models.PrivateConnectorAction, error)
}

type Config struct {
	InstallerURL     string
	Now              func() time.Time
	ActionSigningKey ed25519.PrivateKey
	ActionGateway    ActionGateway
	HTTPClient       *http.Client
}

type Service struct {
	store            Store
	installerURL     string
	now              func() time.Time
	actionSigningKey ed25519.PrivateKey
	actionGateway    ActionGateway
	sessionNonces    *connector.NonceCache
	httpClient       *http.Client
}

type PrivateConnectorHealthTransition = models.PrivateConnectorHealthTransition

type ActionGateway interface {
	DispatchPrivateConnectorAction(ctx context.Context, req connector.ActionRequest, signature string) (connector.ActionResult, error)
}

type actionStore interface {
	RecordAction(ctx context.Context, orgID uuid.UUID, action *models.PrivateConnectorAction) error
	CompleteAction(ctx context.Context, orgID, actionID uuid.UUID, status models.PrivateConnectorActionStatus, errorCode, errorMessage *string, resultCount, durationMs *int) (models.PrivateConnectorAction, error)
}

func NewService(store Store, cfg Config) *Service {
	installerURL := strings.TrimSpace(cfg.InstallerURL)
	if installerURL == "" {
		installerURL = "https://get.143.dev/private-connector.sh"
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Service{
		store:            store,
		installerURL:     installerURL,
		now:              now,
		actionSigningKey: cfg.ActionSigningKey,
		actionGateway:    cfg.ActionGateway,
		sessionNonces:    connector.NewNonceCache(time.Minute),
		httpClient:       httpClient,
	}
}

func (s *Service) AuthorizeSession(ctx context.Context, payload connector.SessionAuthPayload, signature string) (models.PrivateConnectorInstance, error) {
	instance, err := s.store.GetInstanceByID(ctx, payload.InstanceID)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	publicKey, err := decodeEd25519PublicKey(instance.PublicKey)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	if err := connector.VerifySessionAuth(publicKey, payload, signature, connector.SessionAuthVerifyOptions{
		InstanceID: payload.InstanceID,
		Now:        s.now,
		NonceCache: s.sessionNonces,
	}); err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	return instance, nil
}

func (s *Service) DispatchPrivateConnectorAction(ctx context.Context, req integration.PrivateConnectorActionDispatchRequest) (json.RawMessage, error) {
	if s.actionGateway == nil || len(s.actionSigningKey) != ed25519.PrivateKeySize {
		return nil, ErrActionDispatchUnavailable
	}
	store, ok := s.store.(actionStore)
	if !ok {
		return nil, ErrActionDispatchUnavailable
	}
	now := s.now().UTC()
	actionReq := connector.ActionRequest{
		OrgID:       req.OrgID,
		ConnectorID: req.ConnectorID,
		ResourceID:  req.ResourceID,
		Capability:  strings.TrimSpace(req.Capability),
		RequestID:   uuid.New(),
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
		Params:      req.Params,
	}
	signature, err := connector.SignActionRequest(s.actionSigningKey, actionReq)
	if err != nil {
		return nil, err
	}
	action := models.PrivateConnectorAction{
		OrgID:              req.OrgID,
		ConnectorGroupID:   req.ConnectorID,
		ResourceID:         req.ResourceID,
		Capability:         actionReq.Capability,
		ActorType:          models.AuditActorSystem,
		ActorID:            "private_connector_dispatcher",
		RequestNonce:       actionReq.RequestID.String(),
		RequestFingerprint: privateConnectorActionFingerprint(actionReq),
		Status:             models.PrivateConnectorActionStatusRunning,
	}
	if err := store.RecordAction(ctx, req.OrgID, &action); err != nil {
		return nil, err
	}

	result, err := s.actionGateway.DispatchPrivateConnectorAction(ctx, actionReq, signature)
	if err != nil {
		code := "gateway_dispatch_failed"
		message := err.Error()
		if _, completeErr := store.CompleteAction(ctx, req.OrgID, action.ID, models.PrivateConnectorActionStatusFailed, &code, &message, nil, nil); completeErr != nil {
			return nil, errors.Join(err, completeErr)
		}
		return nil, err
	}
	resultCount := result.Metadata.ResultCount
	durationMs := result.Metadata.DurationMs
	if _, err := store.CompleteAction(ctx, req.OrgID, action.ID, models.PrivateConnectorActionStatusSucceeded, nil, nil, &resultCount, &durationMs); err != nil {
		return nil, err
	}
	return result.Payload, nil
}

func (s *Service) RequestIdentityRotation(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	params, err := json.Marshal(struct {
		InstanceID uuid.UUID `json:"instance_id"`
	}{InstanceID: instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	return s.dispatchConnectorControl(ctx, orgID, instanceID, connector.CapabilityRotateIdentity, params)
}

func (s *Service) RequestConfigReload(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	params, err := json.Marshal(struct {
		InstanceID uuid.UUID `json:"instance_id"`
	}{InstanceID: instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	return s.dispatchConnectorControl(ctx, orgID, instanceID, connector.CapabilityReloadConfig, params)
}

func (s *Service) RequestConnectorUpdate(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	params, err := json.Marshal(struct {
		InstanceID uuid.UUID `json:"instance_id"`
	}{InstanceID: instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	return s.dispatchConnectorControl(ctx, orgID, instanceID, connector.CapabilityTriggerUpdate, params)
}

func (s *Service) dispatchConnectorControl(ctx context.Context, orgID, instanceID uuid.UUID, capability string, params json.RawMessage) (models.PrivateConnectorInstance, error) {
	if s.actionGateway == nil || len(s.actionSigningKey) != ed25519.PrivateKeySize {
		return models.PrivateConnectorInstance{}, ErrActionDispatchUnavailable
	}
	instance, err := s.store.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	if instance.OrgID != orgID || instance.RevokedAt != nil || instance.Status == models.PrivateConnectorInstanceStatusRevoked {
		return models.PrivateConnectorInstance{}, ErrActionDispatchUnavailable
	}
	now := s.now().UTC()
	actionReq := connector.ActionRequest{
		OrgID:       orgID,
		ConnectorID: instance.ConnectorGroupID,
		ResourceID:  connector.ConnectorControlResourceID,
		Capability:  capability,
		RequestID:   uuid.New(),
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
		Params:      params,
	}
	signature, err := connector.SignActionRequest(s.actionSigningKey, actionReq)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	if _, err := s.actionGateway.DispatchPrivateConnectorAction(ctx, actionReq, signature); err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	refreshed, err := s.store.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	if refreshed.OrgID != orgID {
		return models.PrivateConnectorInstance{}, ErrActionDispatchUnavailable
	}
	return refreshed, nil
}

func (s *Service) ConnectorConfigPush(ctx context.Context, instance models.PrivateConnectorInstance) (connector.ConfigPushFrame, string, error) {
	if len(s.actionSigningKey) != ed25519.PrivateKeySize {
		return connector.ConfigPushFrame{}, "", ErrConfigPushUnavailable
	}
	resources, err := s.store.ListResources(ctx, instance.OrgID, instance.ConnectorGroupID)
	if err != nil {
		return connector.ConfigPushFrame{}, "", err
	}
	pushResources := make([]connector.ConfigPushResource, 0, len(resources))
	var version int64
	for _, resource := range resources {
		if resource.ConfigSource != models.PrivateConnectorConfigSourceUI || resource.Status == models.PrivateConnectorResourceStatusDisabled {
			continue
		}
		if resource.ConfigVersion > version {
			version = resource.ConfigVersion
		}
		pushResources = append(pushResources, connector.ConfigPushResource{
			ID:            resource.ID,
			DisplayName:   resource.DisplayName,
			ResourceType:  string(resource.ResourceType),
			Mode:          string(resource.Mode),
			ConfigSource:  string(resource.ConfigSource),
			ConfigVersion: resource.ConfigVersion,
			Config:        resource.Config,
		})
	}
	if len(pushResources) == 0 || version <= 0 {
		return connector.ConfigPushFrame{}, "", nil
	}
	sort.Slice(pushResources, func(i, j int) bool {
		return pushResources[i].ID.String() < pushResources[j].ID.String()
	})
	now := s.now().UTC()
	frame := connector.ConfigPushFrame{
		OrgID:       instance.OrgID,
		ConnectorID: instance.ConnectorGroupID,
		Version:     version,
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
		Resources:   pushResources,
	}
	signature, err := connector.SignConfigPush(s.actionSigningKey, frame)
	if err != nil {
		return connector.ConfigPushFrame{}, "", err
	}
	return frame, signature, nil
}

func (s *Service) TestResource(ctx context.Context, orgID, resourceID uuid.UUID) (models.PrivateConnectorResource, error) {
	resource, err := s.store.GetResource(ctx, orgID, resourceID)
	if err != nil {
		return models.PrivateConnectorResource{}, err
	}
	capability, params, err := resourceProbe(resource)
	if err != nil {
		return models.PrivateConnectorResource{}, err
	}
	_, probeErr := s.DispatchPrivateConnectorAction(ctx, integration.PrivateConnectorActionDispatchRequest{
		OrgID:       orgID,
		ConnectorID: resource.ConnectorGroupID,
		ResourceID:  resource.ID,
		Capability:  capability,
		Params:      params,
	})
	testStatus := "success"
	status := models.PrivateConnectorResourceStatusReady
	var testError *string
	if probeErr != nil {
		testStatus = "failed"
		status = models.PrivateConnectorResourceStatusError
		message := probeErr.Error()
		testError = &message
	}
	updated, updateErr := s.store.UpdateResourceTestResult(ctx, orgID, resource.ID, status, &testStatus, testError)
	if updateErr != nil {
		if probeErr != nil {
			return models.PrivateConnectorResource{}, errors.Join(probeErr, updateErr)
		}
		return models.PrivateConnectorResource{}, updateErr
	}
	return updated, nil
}

func resourceProbe(resource models.PrivateConnectorResource) (string, json.RawMessage, error) {
	switch resource.ResourceType {
	case models.PrivateConnectorResourceTypeVictoriaLogs:
		params, err := json.Marshal(struct {
			Limit int    `json:"limit"`
			Since string `json:"since"`
		}{Limit: 1, Since: "5m"})
		if err != nil {
			return "", nil, err
		}
		return "victorialogs.fields", params, nil
	case models.PrivateConnectorResourceTypePostgres:
		params, err := json.Marshal(struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}{Query: "SELECT 1", Limit: 1})
		if err != nil {
			return "", nil, err
		}
		return "postgres.query", params, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedResourceProbe, resource.ResourceType)
	}
}

func privateConnectorActionFingerprint(req connector.ActionRequest) string {
	h := sha256.New()
	h.Write([]byte(req.OrgID.String()))
	h.Write([]byte{0})
	h.Write([]byte(req.ConnectorID.String()))
	h.Write([]byte{0})
	h.Write([]byte(req.ResourceID.String()))
	h.Write([]byte{0})
	h.Write([]byte(req.Capability))
	h.Write([]byte{0})
	h.Write(normalizedActionFingerprintParams(req.Capability, req.Params))
	return hex.EncodeToString(h.Sum(nil))
}

var (
	sqlStringLiteralPattern = regexp.MustCompile(`'([^']|'')*'`)
	sqlNumberLiteralPattern = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
	sqlWhitespacePattern    = regexp.MustCompile(`\s+`)
)

func normalizedActionFingerprintParams(capability string, raw json.RawMessage) []byte {
	if !strings.HasPrefix(capability, "postgres.") {
		return raw
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return raw
	}
	if query, ok := params["query"].(string); ok {
		params["query"] = normalizeSQLFingerprint(query)
	}
	out, err := json.Marshal(params)
	if err != nil {
		return raw
	}
	return out
}

func normalizeSQLFingerprint(query string) string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	normalized = sqlStringLiteralPattern.ReplaceAllString(normalized, "?")
	normalized = sqlNumberLiteralPattern.ReplaceAllString(normalized, "?")
	normalized = sqlWhitespacePattern.ReplaceAllString(normalized, " ")
	return normalized
}

type CreateConnectorRequest struct {
	Name          string
	Environment   string
	GatewayRegion string
}

type CreateConnectorResult struct {
	Connector          models.PrivateConnectorGroup           `json:"connector"`
	DeploymentToken    models.PrivateConnectorDeploymentToken `json:"deployment_token"`
	RawDeploymentToken string                                 `json:"deployment_token_value"`
	InstallCommand     string                                 `json:"install_command"`
}

type CreateDeploymentTokenRequest struct {
	ConnectorGroupID     uuid.UUID
	Name                 string
	Preset               models.PrivateConnectorTokenPreset
	MaxRegistrations     *int
	AllowedSourceCIDRs   []string
	AllowedGatewayRegion *string
	ExpiresAt            *time.Time
	NoExpiry             bool
	TokenFilePath        string
}

type CreateDeploymentTokenResult struct {
	DeploymentToken    models.PrivateConnectorDeploymentToken `json:"deployment_token"`
	RawDeploymentToken string                                 `json:"deployment_token_value"`
	InstallCommand     string                                 `json:"install_command"`
}

type UpdateConnectorSettingsRequest struct {
	HealthAlertURL           *string
	OfflineAlertAfterSeconds int
}

type ConnectorSummary struct {
	Connector        models.PrivateConnectorGroup             `json:"connector"`
	Instances        []models.PrivateConnectorInstance        `json:"instances"`
	Resources        []models.PrivateConnectorResource        `json:"resources"`
	DeploymentTokens []models.PrivateConnectorDeploymentToken `json:"deployment_tokens"`
	RecentActions    []models.PrivateConnectorAction          `json:"recent_actions"`
}

func (s *Service) LogProviders(ctx context.Context, orgID uuid.UUID) ([]integration.LogProvider, error) {
	resources, err := s.store.ListResourcesWithOnlineCapability(ctx, orgID, models.PrivateConnectorResourceTypeVictoriaLogs, "victorialogs.query")
	if err != nil {
		return nil, err
	}
	providers := make([]integration.LogProvider, 0, len(resources))
	for _, resource := range resources {
		providers = append(providers, integration.NewPrivateConnectorLogProvider(integration.PrivateConnectorLogConfig{
			OrgID:       orgID,
			ConnectorID: resource.ConnectorGroupID,
			ResourceID:  resource.ID,
			Provider:    models.ProviderVictoriaLogs,
			Dispatcher:  s,
		}))
	}
	return providers, nil
}

func (s *Service) DatabaseProviders(ctx context.Context, orgID uuid.UUID) ([]integration.DatabaseProvider, error) {
	resources, err := s.store.ListResourcesWithOnlineCapability(ctx, orgID, models.PrivateConnectorResourceTypePostgres, "postgres.query")
	if err != nil {
		return nil, err
	}
	providers := make([]integration.DatabaseProvider, 0, len(resources))
	for _, resource := range resources {
		providers = append(providers, integration.NewPrivateConnectorDatabaseProvider(integration.PrivateConnectorDatabaseConfig{
			OrgID:       orgID,
			ConnectorID: resource.ConnectorGroupID,
			ResourceID:  resource.ID,
			Provider:    models.ProviderPostgres,
			Dispatcher:  s,
		}))
	}
	return providers, nil
}

func (s *Service) ListConnectors(ctx context.Context, orgID uuid.UUID) ([]ConnectorSummary, error) {
	groups, err := s.store.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectorSummary, 0, len(groups))
	for _, group := range groups {
		instances, err := s.store.ListInstances(ctx, orgID, group.ID)
		if err != nil {
			return nil, err
		}
		resources, err := s.store.ListResources(ctx, orgID, group.ID)
		if err != nil {
			return nil, err
		}
		tokens, err := s.store.ListDeploymentTokens(ctx, orgID, group.ID)
		if err != nil {
			return nil, err
		}
		actions, err := s.store.ListRecentActions(ctx, orgID, group.ID, 10)
		if err != nil {
			return nil, err
		}
		for i := range tokens {
			tokens[i].TokenHash = ""
		}
		out = append(out, ConnectorSummary{
			Connector:        group,
			Instances:        instances,
			Resources:        resources,
			DeploymentTokens: tokens,
			RecentActions:    actions,
		})
	}
	return out, nil
}

func (s *Service) CreateConnector(ctx context.Context, orgID, userID uuid.UUID, req CreateConnectorRequest) (CreateConnectorResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return CreateConnectorResult{}, ErrNameRequired
	}
	region := normalizeGatewayRegion(req.GatewayRegion)
	if err := validateGatewayRegion(region); err != nil {
		return CreateConnectorResult{}, err
	}

	group := models.PrivateConnectorGroup{
		OrgID:           orgID,
		Name:            name,
		Environment:     strings.TrimSpace(req.Environment),
		GatewayRegion:   region,
		Status:          models.PrivateConnectorStatusWaiting,
		CreatedByUserID: &userID,
	}
	if err := s.store.CreateGroup(ctx, orgID, &group); err != nil {
		return CreateConnectorResult{}, err
	}

	rawToken, err := db.GeneratePrivateConnectorDeploymentToken()
	if err != nil {
		return CreateConnectorResult{}, err
	}
	preset, maxRegistrations, expiresAt := db.PrivateConnectorInteractiveTokenDefaults(s.now().UTC())
	token := models.PrivateConnectorDeploymentToken{
		OrgID:                orgID,
		ConnectorGroupID:     group.ID,
		Name:                 "Interactive install",
		TokenHash:            db.HashAPIToken(rawToken),
		TokenPrefix:          db.PrivateConnectorDeploymentTokenDisplayPrefix(rawToken),
		Preset:               preset,
		MaxRegistrations:     maxRegistrations,
		AllowedGatewayRegion: &region,
		ExpiresAt:            expiresAt,
		CreatedByUserID:      &userID,
	}
	if err := s.store.CreateDeploymentToken(ctx, orgID, &token); err != nil {
		return CreateConnectorResult{}, err
	}

	return CreateConnectorResult{
		Connector:          group,
		DeploymentToken:    token,
		RawDeploymentToken: rawToken,
		InstallCommand:     s.installCommand(rawToken),
	}, nil
}

func (s *Service) CreateDeploymentToken(ctx context.Context, orgID, userID uuid.UUID, req CreateDeploymentTokenRequest) (CreateDeploymentTokenResult, error) {
	if req.ConnectorGroupID == uuid.Nil {
		return CreateDeploymentTokenResult{}, ErrNameRequired
	}
	group, err := s.store.GetGroup(ctx, orgID, req.ConnectorGroupID)
	if err != nil {
		return CreateDeploymentTokenResult{}, err
	}
	preset := req.Preset
	if preset == "" {
		preset = models.PrivateConnectorTokenPresetInteractive
	}
	if err := preset.Validate(); err != nil {
		return CreateDeploymentTokenResult{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if preset == models.PrivateConnectorTokenPresetAutomation {
			name = "Automation install"
		} else {
			name = "Interactive install"
		}
	}
	rawToken, err := db.GeneratePrivateConnectorDeploymentToken()
	if err != nil {
		return CreateDeploymentTokenResult{}, err
	}
	region := normalizeGatewayRegion(group.GatewayRegion)
	allowedRegion := req.AllowedGatewayRegion
	if allowedRegion == nil {
		allowedRegion = &region
	} else {
		normalized := normalizeGatewayRegion(*allowedRegion)
		if err := validateGatewayRegion(normalized); err != nil {
			return CreateDeploymentTokenResult{}, err
		}
		allowedRegion = &normalized
	}
	maxRegistrations := req.MaxRegistrations
	expiresAt := req.ExpiresAt
	if preset == models.PrivateConnectorTokenPresetInteractive {
		defaultPreset, defaultMax, defaultExpiry := db.PrivateConnectorInteractiveTokenDefaults(s.now().UTC())
		preset = defaultPreset
		if maxRegistrations == nil {
			maxRegistrations = defaultMax
		}
		if expiresAt == nil && !req.NoExpiry {
			expiresAt = defaultExpiry
		}
	}
	if preset == models.PrivateConnectorTokenPresetAutomation && expiresAt == nil && !req.NoExpiry {
		defaultExpiry := s.now().UTC().Add(defaultAutomationTokenTTL)
		expiresAt = &defaultExpiry
	}
	token := models.PrivateConnectorDeploymentToken{
		OrgID:                orgID,
		ConnectorGroupID:     group.ID,
		Name:                 name,
		TokenHash:            db.HashAPIToken(rawToken),
		TokenPrefix:          db.PrivateConnectorDeploymentTokenDisplayPrefix(rawToken),
		Preset:               preset,
		MaxRegistrations:     maxRegistrations,
		AllowedSourceCIDRs:   append([]string(nil), req.AllowedSourceCIDRs...),
		AllowedGatewayRegion: allowedRegion,
		ExpiresAt:            expiresAt,
		CreatedByUserID:      &userID,
	}
	if err := s.store.CreateDeploymentToken(ctx, orgID, &token); err != nil {
		return CreateDeploymentTokenResult{}, err
	}
	return CreateDeploymentTokenResult{
		DeploymentToken:    token,
		RawDeploymentToken: rawToken,
		InstallCommand:     s.installCommandWithOptions(rawToken, installCommandOptions{TokenFilePath: req.TokenFilePath}),
	}, nil
}

func (s *Service) UpdateConnectorSettings(ctx context.Context, orgID, groupID uuid.UUID, req UpdateConnectorSettingsRequest) (models.PrivateConnectorGroup, error) {
	offlineAlertAfterSeconds := req.OfflineAlertAfterSeconds
	if offlineAlertAfterSeconds <= 0 {
		offlineAlertAfterSeconds = int(defaultOfflineAlertAfter / time.Second)
	}
	if req.HealthAlertURL != nil {
		trimmed := strings.TrimSpace(*req.HealthAlertURL)
		if trimmed == "" {
			req.HealthAlertURL = nil
		} else {
			parsed, err := url.Parse(trimmed)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				return models.PrivateConnectorGroup{}, fmt.Errorf("invalid private connector health alert url")
			}
			req.HealthAlertURL = &trimmed
		}
	}
	return s.store.UpdateGroupSettings(ctx, orgID, groupID, req.HealthAlertURL, offlineAlertAfterSeconds)
}

func (s *Service) DisableConnector(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	return s.store.DisableGroup(ctx, orgID, groupID)
}

func (s *Service) RevokeDeploymentToken(ctx context.Context, orgID, tokenID, revokedBy uuid.UUID) (models.PrivateConnectorDeploymentToken, error) {
	return s.store.RevokeDeploymentToken(ctx, orgID, tokenID, revokedBy)
}

func (s *Service) RevokeInstance(ctx context.Context, orgID, instanceID, revokedBy uuid.UUID) (models.PrivateConnectorInstance, error) {
	return s.store.RevokeInstance(ctx, orgID, instanceID, revokedBy)
}

type RegisterInstanceRequest struct {
	DeploymentToken          string
	InstanceName             string
	PublicKey                string
	Version                  string
	Protocol                 models.PrivateConnectorProtocol
	GatewayRegion            string
	SourceIP                 string
	Capabilities             []string
	HeartbeatIntervalSeconds int
}

type RegisterInstanceResult struct {
	Instance                 models.PrivateConnectorInstance `json:"instance"`
	OrgID                    uuid.UUID                       `json:"org_id"`
	ConnectorGroupID         uuid.UUID                       `json:"connector_group_id"`
	GatewayRegion            string                          `json:"gateway_region"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

type RotateInstanceIdentityRequest struct {
	PublicKey string `json:"public_key"`
}

func (s *Service) RegisterInstance(ctx context.Context, req RegisterInstanceRequest) (RegisterInstanceResult, error) {
	rawToken := strings.TrimSpace(req.DeploymentToken)
	if rawToken == "" {
		return RegisterInstanceResult{}, ErrDeploymentTokenRequired
	}
	if _, err := decodeEd25519PublicKey(req.PublicKey); err != nil {
		return RegisterInstanceResult{}, err
	}
	protocol := req.Protocol
	if protocol == "" {
		protocol = models.PrivateConnectorProtocolWebSocket
	}
	if err := protocol.Validate(); err != nil {
		return RegisterInstanceResult{}, err
	}
	region := normalizeGatewayRegion(req.GatewayRegion)
	if err := validateGatewayRegion(region); err != nil {
		return RegisterInstanceResult{}, err
	}
	heartbeatIntervalSeconds := req.HeartbeatIntervalSeconds
	if heartbeatIntervalSeconds <= 0 {
		heartbeatIntervalSeconds = 5
	}
	instanceName := strings.TrimSpace(req.InstanceName)
	if instanceName == "" {
		instanceName = "connector"
	}

	token, err := s.store.ConsumeDeploymentToken(ctx, rawToken, region, strings.TrimSpace(req.SourceIP))
	if err != nil {
		return RegisterInstanceResult{}, err
	}
	if group, groupUpdateAllowed, err := s.connectorGroupAllowsStatusUpdate(ctx, token.OrgID, token.ConnectorGroupID); err != nil {
		return RegisterInstanceResult{}, err
	} else if !groupUpdateAllowed && group.Status == models.PrivateConnectorStatusDisabled {
		return RegisterInstanceResult{}, ErrConnectorDisabled
	}
	instance := models.PrivateConnectorInstance{
		OrgID:                    token.OrgID,
		ConnectorGroupID:         token.ConnectorGroupID,
		DeploymentTokenID:        &token.ID,
		InstanceName:             instanceName,
		PublicKey:                strings.TrimSpace(req.PublicKey),
		Status:                   models.PrivateConnectorInstanceStatusOnline,
		Version:                  strings.TrimSpace(req.Version),
		Protocol:                 protocol,
		GatewayRegion:            region,
		Capabilities:             normalizedCapabilities(req.Capabilities),
		HeartbeatIntervalSeconds: heartbeatIntervalSeconds,
	}
	if err := s.store.CreateInstance(ctx, token.OrgID, &instance); err != nil {
		return RegisterInstanceResult{}, err
	}
	if _, _, err := s.updateConnectorGroupStatusIfEnabled(ctx, token.OrgID, token.ConnectorGroupID, models.PrivateConnectorStatusOnline); err != nil {
		return RegisterInstanceResult{}, err
	}

	return RegisterInstanceResult{
		Instance:                 instance,
		OrgID:                    token.OrgID,
		ConnectorGroupID:         token.ConnectorGroupID,
		GatewayRegion:            region,
		HeartbeatIntervalSeconds: heartbeatIntervalSeconds,
	}, nil
}

func (s *Service) RotateInstanceIdentity(ctx context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error) {
	instance, err := s.store.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	publicKey, err := decodeEd25519PublicKey(instance.PublicKey)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || !ed25519.Verify(publicKey, body, signatureBytes) {
		return models.PrivateConnectorInstance{}, ErrInvalidSignature
	}
	var req RotateInstanceIdentityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("%w: %v", ErrInvalidPublicKey, err)
	}
	if _, err := decodeEd25519PublicKey(req.PublicKey); err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	return s.store.RotateInstancePublicKey(ctx, instance.OrgID, instance.ID, strings.TrimSpace(req.PublicKey))
}

type HeartbeatRequest struct {
	Version                  string                          `json:"version"`
	Protocol                 models.PrivateConnectorProtocol `json:"protocol"`
	Capabilities             []string                        `json:"capabilities"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

func (s *Service) RecordHeartbeat(ctx context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error) {
	instance, err := s.store.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	publicKey, err := decodeEd25519PublicKey(instance.PublicKey)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || !ed25519.Verify(publicKey, body, signatureBytes) {
		return models.PrivateConnectorInstance{}, ErrInvalidSignature
	}
	var req HeartbeatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("%w: %v", ErrInvalidHeartbeat, err)
	}
	return s.recordTrustedHeartbeat(ctx, instance, req)
}

func (s *Service) RecordSessionHeartbeat(ctx context.Context, instance models.PrivateConnectorInstance, frame connector.HeartbeatFrame) error {
	protocol := models.PrivateConnectorProtocol(frame.Protocol)
	if protocol == "" {
		protocol = models.PrivateConnectorProtocolWebSocket
	}
	_, err := s.recordTrustedHeartbeat(ctx, instance, HeartbeatRequest{
		Version:                  frame.Version,
		Protocol:                 protocol,
		Capabilities:             frame.Capabilities,
		HeartbeatIntervalSeconds: frame.HeartbeatIntervalSeconds,
	})
	return err
}

func (s *Service) RecordSessionDisconnect(ctx context.Context, instance models.PrivateConnectorInstance) error {
	if instance.ID == uuid.Nil || instance.OrgID == uuid.Nil {
		return nil
	}
	if instance.RevokedAt != nil || instance.Status == models.PrivateConnectorInstanceStatusRevoked {
		return nil
	}
	if _, err := s.store.MarkInstanceReconnecting(ctx, instance.OrgID, instance.ID); err != nil {
		return err
	}
	if _, _, err := s.updateConnectorGroupStatusIfEnabled(ctx, instance.OrgID, instance.ConnectorGroupID, models.PrivateConnectorStatusReconnecting); err != nil {
		return err
	}
	return nil
}

func (s *Service) MarkOfflineConnectors(ctx context.Context) (int, error) {
	transitions, err := s.store.MarkOfflineInstances(ctx, s.now().UTC())
	if err != nil {
		return 0, err
	}
	for _, transition := range transitions {
		if err := s.sendHealthAlert(ctx, transition.Group, transition.Instance, "connector_offline"); err != nil {
			return 0, err
		}
	}
	return len(transitions), nil
}

func (s *Service) StartHealthMonitor(ctx context.Context, interval time.Duration, logger zerolog.Logger) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.MarkOfflineConnectors(ctx); err != nil {
				logger.Warn().Err(err).Msg("private connector health monitor failed")
			}
		}
	}
}

func (s *Service) recordTrustedHeartbeat(ctx context.Context, instance models.PrivateConnectorInstance, req HeartbeatRequest) (models.PrivateConnectorInstance, error) {
	protocol := req.Protocol
	if protocol == "" {
		protocol = models.PrivateConnectorProtocolWebSocket
	}
	if err := protocol.Validate(); err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	heartbeatIntervalSeconds := req.HeartbeatIntervalSeconds
	if heartbeatIntervalSeconds <= 0 {
		heartbeatIntervalSeconds = instance.HeartbeatIntervalSeconds
	}
	if heartbeatIntervalSeconds <= 0 {
		heartbeatIntervalSeconds = 5
	}
	updated, err := s.store.UpdateInstanceHeartbeat(ctx, instance.OrgID, instance.ID, strings.TrimSpace(req.Version), protocol, normalizedCapabilities(req.Capabilities), heartbeatIntervalSeconds)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	group, groupUpdated, err := s.updateConnectorGroupStatusIfEnabled(ctx, instance.OrgID, instance.ConnectorGroupID, models.PrivateConnectorStatusOnline)
	if err != nil {
		return models.PrivateConnectorInstance{}, err
	}
	if groupUpdated && (instance.Status == models.PrivateConnectorInstanceStatusOffline || instance.Status == models.PrivateConnectorInstanceStatusReconnecting) {
		if alertErr := s.sendHealthAlert(ctx, group, updated, "connector_online"); alertErr != nil {
			return models.PrivateConnectorInstance{}, alertErr
		}
	}
	return updated, nil
}

func (s *Service) updateConnectorGroupStatusIfEnabled(ctx context.Context, orgID, groupID uuid.UUID, status models.PrivateConnectorStatus) (models.PrivateConnectorGroup, bool, error) {
	group, canUpdate, err := s.connectorGroupAllowsStatusUpdate(ctx, orgID, groupID)
	if err != nil || !canUpdate {
		return group, canUpdate, err
	}
	if err := s.store.UpdateGroupStatus(ctx, orgID, groupID, status); err != nil {
		return models.PrivateConnectorGroup{}, false, err
	}
	group.Status = status
	return group, true, nil
}

func (s *Service) connectorGroupAllowsStatusUpdate(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, bool, error) {
	if groupID == uuid.Nil {
		return models.PrivateConnectorGroup{}, false, nil
	}
	group, err := s.store.GetGroup(ctx, orgID, groupID)
	if err != nil {
		return models.PrivateConnectorGroup{}, false, err
	}
	if group.Status == models.PrivateConnectorStatusDisabled {
		return group, false, nil
	}
	return group, true, nil
}

func (s *Service) sendHealthAlert(ctx context.Context, group models.PrivateConnectorGroup, instance models.PrivateConnectorInstance, event string) error {
	if group.HealthAlertURL == nil || strings.TrimSpace(*group.HealthAlertURL) == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"event":                 event,
		"org_id":                group.OrgID,
		"connector_group_id":    group.ID,
		"connector_group_name":  group.Name,
		"connector_instance_id": instance.ID,
		"instance_name":         instance.InstanceName,
		"status":                instance.Status,
		"version":               instance.Version,
		"occurred_at":           s.now().UTC(),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(*group.HealthAlertURL), strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("private connector health alert returned status %s", resp.Status)
	}
	return nil
}

type CreateResourceRequest struct {
	DisplayName  string
	ResourceType models.PrivateConnectorResourceType
	Mode         models.PrivateConnectorResourceMode
	Config       json.RawMessage
}

func (s *Service) CreateResource(ctx context.Context, orgID, groupID, userID uuid.UUID, req CreateResourceRequest) (models.PrivateConnectorResource, error) {
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		return models.PrivateConnectorResource{}, ErrNameRequired
	}
	if err := req.ResourceType.Validate(); err != nil {
		return models.PrivateConnectorResource{}, err
	}
	if err := req.Mode.Validate(); err != nil {
		return models.PrivateConnectorResource{}, err
	}
	config := req.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if err := validateUIManagedConfig(config); err != nil {
		return models.PrivateConnectorResource{}, err
	}
	resource := models.PrivateConnectorResource{
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		DisplayName:      displayName,
		ResourceType:     req.ResourceType,
		Mode:             req.Mode,
		Config:           config,
		ConfigSource:     models.PrivateConnectorConfigSourceUI,
		ConfigVersion:    1,
		Status:           models.PrivateConnectorResourceStatusConfigured,
		CreatedByUserID:  &userID,
	}
	if err := s.store.CreateResource(ctx, orgID, &resource); err != nil {
		return models.PrivateConnectorResource{}, err
	}
	return resource, nil
}

type CreateRuntimeLeaseRequest struct {
	RepositoryID     uuid.UUID
	Repository       string
	PreviewID        uuid.UUID
	PreviewRuntimeID uuid.UUID
	ResourceID       uuid.UUID
	TTL              time.Duration
}

type CreateRuntimeLeaseResult struct {
	Lease         models.PrivateConnectorRuntimeLease `json:"lease"`
	RawLeaseToken string                              `json:"lease_token"`
	DatabaseURL   string                              `json:"database_url"`
}

type runtimeLeasePolicy struct {
	TargetHost             string   `json:"target_host"`
	TargetPort             int      `json:"target_port"`
	TargetDatabase         string   `json:"target_database"`
	ProxyHost              string   `json:"proxy_host"`
	ProxyPort              int      `json:"proxy_port"`
	AllowedRepositories    []string `json:"allowed_repositories"`
	AllowedRepositoryIDs   []string `json:"allowed_repository_ids"`
	MaxActiveLeases        int      `json:"max_active_leases"`
	MaxLeaseDuration       string   `json:"max_lease_duration"`
	MaxConnections         int      `json:"max_connections"`
	IdleTimeoutSeconds     int      `json:"idle_timeout_seconds"`
	ByteLimit              *int64   `json:"byte_limit"`
	maxLeaseDurationParsed time.Duration
}

func (s *Service) CreateRuntimeLease(ctx context.Context, orgID uuid.UUID, req CreateRuntimeLeaseRequest) (CreateRuntimeLeaseResult, error) {
	resource, err := s.store.GetResource(ctx, orgID, req.ResourceID)
	if err != nil {
		return CreateRuntimeLeaseResult{}, err
	}
	if resource.ResourceType != models.PrivateConnectorResourceTypePostgres ||
		resource.Mode != models.PrivateConnectorResourceModePreviewRuntime ||
		resource.Status == models.PrivateConnectorResourceStatusDisabled {
		return CreateRuntimeLeaseResult{}, ErrUnsupportedRuntimeResource
	}
	policy, err := parseRuntimeLeasePolicy(resource.Config)
	if err != nil {
		return CreateRuntimeLeaseResult{}, err
	}
	if err := enforceRuntimeRepositoryPolicy(policy, req); err != nil {
		return CreateRuntimeLeaseResult{}, err
	}
	if policy.MaxActiveLeases > 0 {
		active, err := s.store.CountActiveRuntimeLeases(ctx, orgID, resource.ID)
		if err != nil {
			return CreateRuntimeLeaseResult{}, err
		}
		if active >= policy.MaxActiveLeases {
			return CreateRuntimeLeaseResult{}, fmt.Errorf("%w: active lease limit reached", ErrInvalidRuntimeLeasePolicy)
		}
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = defaultRuntimeLeaseTTL
	}
	if ttl > maxRuntimeLeaseTTL {
		ttl = maxRuntimeLeaseTTL
	}
	if policy.maxLeaseDurationParsed > 0 && ttl > policy.maxLeaseDurationParsed {
		ttl = policy.maxLeaseDurationParsed
	}
	rawToken, err := db.GeneratePrivateConnectorRuntimeLeaseToken()
	if err != nil {
		return CreateRuntimeLeaseResult{}, err
	}
	lease := models.PrivateConnectorRuntimeLease{
		OrgID:              orgID,
		RepositoryID:       req.RepositoryID,
		PreviewID:          req.PreviewID,
		PreviewRuntimeID:   req.PreviewRuntimeID,
		ConnectorGroupID:   resource.ConnectorGroupID,
		ResourceID:         resource.ID,
		Status:             models.PrivateConnectorRuntimeLeaseStatusActive,
		AccessMode:         models.PrivateConnectorRuntimeAccessModePostgresTCP,
		TargetHost:         policy.TargetHost,
		TargetPort:         policy.TargetPort,
		TargetDatabase:     policy.TargetDatabase,
		LeaseTokenHash:     db.HashAPIToken(rawToken),
		LeaseTokenPrefix:   db.PrivateConnectorRuntimeLeaseTokenDisplayPrefix(rawToken),
		MaxConnections:     policy.MaxConnections,
		IdleTimeoutSeconds: policy.IdleTimeoutSeconds,
		ByteLimit:          policy.ByteLimit,
		ExpiresAt:          s.now().UTC().Add(ttl),
	}
	if err := s.store.CreateRuntimeLease(ctx, orgID, &lease); err != nil {
		return CreateRuntimeLeaseResult{}, err
	}
	return CreateRuntimeLeaseResult{
		Lease:         lease,
		RawLeaseToken: rawToken,
		DatabaseURL:   runtimeLeaseDatabaseURL(lease, rawToken, policy),
	}, nil
}

func (s *Service) RevokeRuntimeLease(ctx context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error) {
	return s.store.RevokeRuntimeLease(ctx, orgID, leaseID)
}

func (s *Service) AuthorizeRuntimeLease(ctx context.Context, rawToken string) (models.PrivateConnectorRuntimeLease, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return models.PrivateConnectorRuntimeLease{}, ErrRuntimeLeaseTokenRequired
	}
	return s.store.GetActiveRuntimeLeaseByToken(ctx, rawToken)
}

func parseRuntimeLeasePolicy(raw json.RawMessage) (runtimeLeasePolicy, error) {
	var policy runtimeLeasePolicy
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &policy); err != nil {
			return runtimeLeasePolicy{}, fmt.Errorf("%w: %v", ErrInvalidRuntimeLeasePolicy, err)
		}
	}
	policy.TargetHost = strings.TrimSpace(policy.TargetHost)
	if policy.TargetHost == "" {
		return runtimeLeasePolicy{}, fmt.Errorf("%w: target_host is required", ErrInvalidRuntimeLeasePolicy)
	}
	if policy.TargetPort == 0 {
		policy.TargetPort = defaultRuntimeLeasePostgresPort
	}
	if policy.TargetPort < 1 || policy.TargetPort > 65535 {
		return runtimeLeasePolicy{}, fmt.Errorf("%w: target_port is invalid", ErrInvalidRuntimeLeasePolicy)
	}
	if policy.MaxConnections <= 0 {
		policy.MaxConnections = defaultRuntimeLeaseMaxConnections
	}
	if policy.IdleTimeoutSeconds <= 0 {
		policy.IdleTimeoutSeconds = defaultRuntimeLeaseIdleTimeout
	}
	if policy.ByteLimit != nil && *policy.ByteLimit <= 0 {
		return runtimeLeasePolicy{}, fmt.Errorf("%w: byte_limit must be positive", ErrInvalidRuntimeLeasePolicy)
	}
	policy.TargetDatabase = strings.TrimSpace(policy.TargetDatabase)
	policy.ProxyHost = strings.TrimSpace(policy.ProxyHost)
	if policy.ProxyHost == "" {
		policy.ProxyHost = defaultRuntimeLeaseProxyHost
	}
	if policy.ProxyPort == 0 {
		policy.ProxyPort = policy.TargetPort
	}
	if policy.ProxyPort < 1 || policy.ProxyPort > 65535 {
		return runtimeLeasePolicy{}, fmt.Errorf("%w: proxy_port is invalid", ErrInvalidRuntimeLeasePolicy)
	}
	if strings.TrimSpace(policy.MaxLeaseDuration) != "" {
		duration, err := time.ParseDuration(strings.TrimSpace(policy.MaxLeaseDuration))
		if err != nil || duration <= 0 {
			return runtimeLeasePolicy{}, fmt.Errorf("%w: max_lease_duration is invalid", ErrInvalidRuntimeLeasePolicy)
		}
		policy.maxLeaseDurationParsed = duration
	}
	return policy, nil
}

func enforceRuntimeRepositoryPolicy(policy runtimeLeasePolicy, req CreateRuntimeLeaseRequest) error {
	if len(policy.AllowedRepositoryIDs) == 0 && len(policy.AllowedRepositories) == 0 {
		return nil
	}
	repositoryID := strings.TrimSpace(req.RepositoryID.String())
	repositoryName := strings.ToLower(strings.TrimSpace(req.Repository))
	for _, allowed := range policy.AllowedRepositoryIDs {
		if strings.EqualFold(strings.TrimSpace(allowed), repositoryID) {
			return nil
		}
	}
	for _, allowed := range policy.AllowedRepositories {
		if repositoryName != "" && strings.EqualFold(strings.TrimSpace(allowed), repositoryName) {
			return nil
		}
	}
	return fmt.Errorf("%w: repository is not allowed for preview resource", ErrInvalidRuntimeLeasePolicy)
}

func runtimeLeaseDatabaseURL(lease models.PrivateConnectorRuntimeLease, rawToken string, policy runtimeLeasePolicy) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword("lease", rawToken),
		Host:   net.JoinHostPort(policy.ProxyHost, strconv.Itoa(policy.ProxyPort)),
		Path:   "/" + lease.TargetDatabase,
	}
	q := u.Query()
	q.Set("lease_id", lease.ID.String())
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Service) installCommand(rawToken string) string {
	return s.installCommandWithOptions(rawToken, installCommandOptions{})
}

type installCommandOptions struct {
	TokenFilePath string
}

func (s *Service) installCommandWithOptions(rawToken string, opts installCommandOptions) string {
	tokenPart := fmt.Sprintf("143_CONNECTOR_TOKEN='%s'", rawToken)
	if path := strings.TrimSpace(opts.TokenFilePath); path != "" {
		tokenPart = fmt.Sprintf("143_CONNECTOR_TOKEN_FILE=%s", shellQuote(path))
	}
	if len(s.actionSigningKey) == ed25519.PrivateKeySize {
		publicKey := s.actionSigningKey.Public().(ed25519.PublicKey)
		return fmt.Sprintf("curl -fsSL %s | \\\n  sudo %s \\\n  143_CONNECTOR_GATEWAY_PUBLIC_KEY='%s' bash",
			s.installerURL, tokenPart, base64.StdEncoding.EncodeToString(publicKey))
	}
	return fmt.Sprintf("curl -fsSL %s | \\\n  sudo %s bash", s.installerURL, tokenPart)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func decodeEd25519PublicKey(raw string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, ErrInvalidPublicKey
	}
	return ed25519.PublicKey(key), nil
}

func normalizeGatewayRegion(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		return "us"
	}
	return region
}

func validateGatewayRegion(region string) error {
	switch region {
	case "us", "eu":
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrInvalidGatewayRegion, region)
	}
}

func normalizedCapabilities(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, cap := range in {
		cap = strings.TrimSpace(cap)
		if cap == "" {
			continue
		}
		if _, ok := seen[cap]; ok {
			continue
		}
		seen[cap] = struct{}{}
		out = append(out, cap)
	}
	return out
}

func validateUIManagedConfig(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid resource config JSON: %w", err)
	}
	return rejectInlineSecrets(value, "")
}

func rejectInlineSecrets(value any, key string) error {
	if key != "" && isInlineSecretKey(key) {
		return fmt.Errorf("%w: %s must be provided by local environment/file reference", ErrInlineSecretRejected, key)
	}
	switch v := value.(type) {
	case map[string]any:
		for childKey, childValue := range v {
			if err := rejectInlineSecrets(childValue, childKey); err != nil {
				return err
			}
		}
	case []any:
		for _, childValue := range v {
			if err := rejectInlineSecrets(childValue, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func isInlineSecretKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || strings.HasSuffix(key, "_env") || strings.HasSuffix(key, "_file") || strings.HasSuffix(key, "_ref") {
		return false
	}
	switch key {
	case "dsn", "admin_dsn", "password", "api_key", "auth_token", "access_token", "refresh_token", "token", "secret", "credential", "webhook_secret":
		return true
	}
	return strings.HasSuffix(key, "_password") ||
		strings.HasSuffix(key, "_token") ||
		strings.HasSuffix(key, "_secret") ||
		strings.HasSuffix(key, "_credential")
}
