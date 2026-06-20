package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	privateconnectorsvc "github.com/assembledhq/143/internal/services/privateconnector"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

type fakePrivateConnectorService struct {
	createReq         privateconnectorsvc.CreateConnectorRequest
	createOrgID       uuid.UUID
	createUserID      uuid.UUID
	createResult      privateconnectorsvc.CreateConnectorResult
	listOrgID         uuid.UUID
	listResult        []privateconnectorsvc.ConnectorSummary
	tokenOrgID        uuid.UUID
	tokenUserID       uuid.UUID
	tokenReq          privateconnectorsvc.CreateDeploymentTokenRequest
	tokenResult       privateconnectorsvc.CreateDeploymentTokenResult
	settingsOrgID     uuid.UUID
	settingsGroupID   uuid.UUID
	settingsReq       privateconnectorsvc.UpdateConnectorSettingsRequest
	settingsGroup     models.PrivateConnectorGroup
	disableOrgID      uuid.UUID
	disableGroupID    uuid.UUID
	disabledGroup     models.PrivateConnectorGroup
	registerReq       privateconnectorsvc.RegisterInstanceRequest
	registerResult    privateconnectorsvc.RegisterInstanceResult
	resourceOrgID     uuid.UUID
	resourceGroupID   uuid.UUID
	resourceUserID    uuid.UUID
	resourceReq       privateconnectorsvc.CreateResourceRequest
	resource          models.PrivateConnectorResource
	testOrgID         uuid.UUID
	testResourceID    uuid.UUID
	testResource      models.PrivateConnectorResource
	revokeTokenOrgID  uuid.UUID
	revokeTokenID     uuid.UUID
	revokeTokenUserID uuid.UUID
	revokedToken      models.PrivateConnectorDeploymentToken
	revokeInstOrgID   uuid.UUID
	revokeInstID      uuid.UUID
	revokeInstUserID  uuid.UUID
	revokedInstance   models.PrivateConnectorInstance
	rotateInstOrgID   uuid.UUID
	rotateInstID      uuid.UUID
	rotatedInstance   models.PrivateConnectorInstance
	reloadInstOrgID   uuid.UUID
	reloadInstID      uuid.UUID
	reloadedInstance  models.PrivateConnectorInstance
	updateInstOrgID   uuid.UUID
	updateInstID      uuid.UUID
	updatedInstance   models.PrivateConnectorInstance
	identityID        uuid.UUID
	identityBody      []byte
	identitySig       string
	identityInstance  models.PrivateConnectorInstance
	heartbeatID       uuid.UUID
	heartbeatBody     []byte
	heartbeatSig      string
	heartbeat         models.PrivateConnectorInstance
	sessionPayload    connector.SessionAuthPayload
	sessionSig        string
	sessionInstance   models.PrivateConnectorInstance
}

func (s *fakePrivateConnectorService) CreateConnector(_ context.Context, orgID, userID uuid.UUID, req privateconnectorsvc.CreateConnectorRequest) (privateconnectorsvc.CreateConnectorResult, error) {
	s.createOrgID = orgID
	s.createUserID = userID
	s.createReq = req
	return s.createResult, nil
}

func (s *fakePrivateConnectorService) ListConnectors(_ context.Context, orgID uuid.UUID) ([]privateconnectorsvc.ConnectorSummary, error) {
	s.listOrgID = orgID
	return s.listResult, nil
}

func (s *fakePrivateConnectorService) CreateDeploymentToken(_ context.Context, orgID, userID uuid.UUID, req privateconnectorsvc.CreateDeploymentTokenRequest) (privateconnectorsvc.CreateDeploymentTokenResult, error) {
	s.tokenOrgID = orgID
	s.tokenUserID = userID
	s.tokenReq = req
	return s.tokenResult, nil
}

func (s *fakePrivateConnectorService) UpdateConnectorSettings(_ context.Context, orgID, groupID uuid.UUID, req privateconnectorsvc.UpdateConnectorSettingsRequest) (models.PrivateConnectorGroup, error) {
	s.settingsOrgID = orgID
	s.settingsGroupID = groupID
	s.settingsReq = req
	return s.settingsGroup, nil
}

func (s *fakePrivateConnectorService) DisableConnector(_ context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	s.disableOrgID = orgID
	s.disableGroupID = groupID
	return s.disabledGroup, nil
}

func (s *fakePrivateConnectorService) RegisterInstance(_ context.Context, req privateconnectorsvc.RegisterInstanceRequest) (privateconnectorsvc.RegisterInstanceResult, error) {
	s.registerReq = req
	return s.registerResult, nil
}

func (s *fakePrivateConnectorService) CreateResource(_ context.Context, orgID, groupID, userID uuid.UUID, req privateconnectorsvc.CreateResourceRequest) (models.PrivateConnectorResource, error) {
	s.resourceOrgID = orgID
	s.resourceGroupID = groupID
	s.resourceUserID = userID
	s.resourceReq = req
	return s.resource, nil
}

func (s *fakePrivateConnectorService) TestResource(_ context.Context, orgID, resourceID uuid.UUID) (models.PrivateConnectorResource, error) {
	s.testOrgID = orgID
	s.testResourceID = resourceID
	return s.testResource, nil
}

func (s *fakePrivateConnectorService) RevokeDeploymentToken(_ context.Context, orgID, tokenID, userID uuid.UUID) (models.PrivateConnectorDeploymentToken, error) {
	s.revokeTokenOrgID = orgID
	s.revokeTokenID = tokenID
	s.revokeTokenUserID = userID
	return s.revokedToken, nil
}

func (s *fakePrivateConnectorService) RevokeInstance(_ context.Context, orgID, instanceID, userID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.revokeInstOrgID = orgID
	s.revokeInstID = instanceID
	s.revokeInstUserID = userID
	return s.revokedInstance, nil
}

func (s *fakePrivateConnectorService) RequestIdentityRotation(_ context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.rotateInstOrgID = orgID
	s.rotateInstID = instanceID
	return s.rotatedInstance, nil
}

func (s *fakePrivateConnectorService) RequestConfigReload(_ context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.reloadInstOrgID = orgID
	s.reloadInstID = instanceID
	return s.reloadedInstance, nil
}

func (s *fakePrivateConnectorService) RequestConnectorUpdate(_ context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	s.updateInstOrgID = orgID
	s.updateInstID = instanceID
	return s.updatedInstance, nil
}

func (s *fakePrivateConnectorService) RotateInstanceIdentity(_ context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error) {
	s.identityID = instanceID
	s.identityBody = append([]byte(nil), body...)
	s.identitySig = signature
	return s.identityInstance, nil
}

func (s *fakePrivateConnectorService) RecordHeartbeat(_ context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error) {
	s.heartbeatID = instanceID
	s.heartbeatBody = append([]byte(nil), body...)
	s.heartbeatSig = signature
	return s.heartbeat, nil
}

func (s *fakePrivateConnectorService) AuthorizeSession(_ context.Context, payload connector.SessionAuthPayload, signature string) (models.PrivateConnectorInstance, error) {
	s.sessionPayload = payload
	s.sessionSig = signature
	return s.sessionInstance, nil
}

func withPrivateConnectorAuth(req *http.Request, orgID, userID uuid.UUID) *http.Request {
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	return req.WithContext(ctx)
}

func TestPrivateConnectorHandler_CreateConnector(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	tokenID := uuid.New()
	service := &fakePrivateConnectorService{
		createResult: privateconnectorsvc.CreateConnectorResult{
			Connector: models.PrivateConnectorGroup{
				ID:            groupID,
				OrgID:         orgID,
				Name:          "Production VPC",
				Environment:   "production",
				GatewayRegion: "us",
				Status:        models.PrivateConnectorStatusWaiting,
			},
			DeploymentToken: models.PrivateConnectorDeploymentToken{
				ID:               tokenID,
				OrgID:            orgID,
				ConnectorGroupID: groupID,
				Name:             "Interactive install",
				TokenPrefix:      "143pc_abcd1234",
				Preset:           models.PrivateConnectorTokenPresetInteractive,
			},
			RawDeploymentToken: "143pc_secret",
			InstallCommand:     "curl -fsSL https://get.143.dev/private-connector.sh | sudo 143_CONNECTOR_TOKEN='143pc_secret' bash",
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors", bytes.NewReader([]byte(`{"name":"Production VPC","environment":"production","gateway_region":"us"}`)))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.CreateConnector(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "CreateConnector should return created for a new connector")
	require.Equal(t, orgID, service.createOrgID, "CreateConnector should pass active org to the service")
	require.Equal(t, userID, service.createUserID, "CreateConnector should pass authenticated user to the service")
	require.Equal(t, "Production VPC", service.createReq.Name, "CreateConnector should decode connector name")
	var resp models.SingleResponse[privateconnectorsvc.CreateConnectorResult]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "CreateConnector response should be valid JSON")
	require.Equal(t, "143pc_secret", resp.Data.RawDeploymentToken, "CreateConnector should return the raw token only at creation time")
	require.Equal(t, "143pc_abcd1234", resp.Data.DeploymentToken.TokenPrefix, "CreateConnector should return safe token metadata")
}

func TestPrivateConnectorHandler_ListConnectors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	service := &fakePrivateConnectorService{
		listResult: []privateconnectorsvc.ConnectorSummary{{
			Connector: models.PrivateConnectorGroup{ID: groupID, OrgID: orgID, Name: "Production VPC"},
			Instances: []models.PrivateConnectorInstance{{
				ID:     uuid.New(),
				Status: models.PrivateConnectorInstanceStatusOnline,
			}},
		}},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/private-connectors", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.ListConnectors(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ListConnectors should return connector summaries")
	require.Equal(t, orgID, service.listOrgID, "ListConnectors should pass active org to the service")
	var resp models.ListResponse[privateconnectorsvc.ConnectorSummary]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "ListConnectors response should be valid JSON")
	require.Equal(t, groupID, resp.Data[0].Connector.ID, "ListConnectors should return connector metadata")
}

func TestPrivateConnectorHandler_CreateDeploymentToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	tokenID := uuid.New()
	maxRegistrations := 5
	service := &fakePrivateConnectorService{
		tokenResult: privateconnectorsvc.CreateDeploymentTokenResult{
			DeploymentToken: models.PrivateConnectorDeploymentToken{
				ID:               tokenID,
				OrgID:            orgID,
				ConnectorGroupID: groupID,
				Name:             "Terraform",
				TokenPrefix:      "143pc_auto1234",
				Preset:           models.PrivateConnectorTokenPresetAutomation,
			},
			RawDeploymentToken: "143pc_secret",
			InstallCommand:     "curl -fsSL https://get.143.dev/private-connector.sh | sudo 143_CONNECTOR_TOKEN_FILE='/run/secrets/143-token' bash",
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/"+groupID.String()+"/tokens", bytes.NewReader([]byte(`{
		"name":"Terraform",
		"preset":"automation",
		"max_registrations":5,
		"allowed_source_cidrs":["203.0.113.0/24"],
		"no_expiry":true,
		"token_file_path":"/run/secrets/143-token"
	}`)))
	req = withPrivateConnectorAuth(req, orgID, userID)
	req = req.WithContext(withChiParam(req.Context(), "id", groupID.String()))

	rr := httptest.NewRecorder()
	handler.CreateDeploymentToken(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "CreateDeploymentToken should return created")
	require.Equal(t, orgID, service.tokenOrgID, "CreateDeploymentToken should use request org")
	require.Equal(t, userID, service.tokenUserID, "CreateDeploymentToken should use request user")
	require.Equal(t, groupID, service.tokenReq.ConnectorGroupID, "CreateDeploymentToken should target route connector group")
	require.Equal(t, models.PrivateConnectorTokenPresetAutomation, service.tokenReq.Preset, "CreateDeploymentToken should decode automation preset")
	require.Equal(t, &maxRegistrations, service.tokenReq.MaxRegistrations, "CreateDeploymentToken should decode max registrations")
	require.Equal(t, []string{"203.0.113.0/24"}, service.tokenReq.AllowedSourceCIDRs, "CreateDeploymentToken should decode source CIDRs")
	require.True(t, service.tokenReq.NoExpiry, "CreateDeploymentToken should decode no-expiry flag")
	require.Equal(t, "/run/secrets/143-token", service.tokenReq.TokenFilePath, "CreateDeploymentToken should decode token file path")
	var resp models.SingleResponse[privateconnectorsvc.CreateDeploymentTokenResult]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "CreateDeploymentToken response should decode")
	require.Equal(t, "143pc_secret", resp.Data.RawDeploymentToken, "CreateDeploymentToken should return one-time raw token")
}

func TestPrivateConnectorHandler_UpdateConnectorSettings(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	healthURL := "https://hooks.example.test/private-connectors"
	service := &fakePrivateConnectorService{
		settingsGroup: models.PrivateConnectorGroup{
			ID:                       groupID,
			OrgID:                    orgID,
			Name:                     "Production VPC",
			HealthAlertURL:           &healthURL,
			OfflineAlertAfterSeconds: 45,
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/private-connectors/"+groupID.String(), bytes.NewReader([]byte(`{
		"health_alert_url":"https://hooks.example.test/private-connectors",
		"offline_alert_after_seconds":45
	}`)))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withChiParam(req.Context(), "id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.UpdateConnectorSettings(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "UpdateConnectorSettings should return ok")
	require.Equal(t, orgID, service.settingsOrgID, "UpdateConnectorSettings should pass active org")
	require.Equal(t, groupID, service.settingsGroupID, "UpdateConnectorSettings should pass connector group id")
	require.NotNil(t, service.settingsReq.HealthAlertURL, "UpdateConnectorSettings should decode health alert URL")
	require.Equal(t, healthURL, *service.settingsReq.HealthAlertURL, "UpdateConnectorSettings should decode health alert URL value")
	require.Equal(t, 45, service.settingsReq.OfflineAlertAfterSeconds, "UpdateConnectorSettings should decode offline threshold")
	var resp models.SingleResponse[models.PrivateConnectorGroup]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "UpdateConnectorSettings response should decode")
	require.Equal(t, groupID, resp.Data.ID, "UpdateConnectorSettings should return updated connector")
}

func TestPrivateConnectorHandler_DisableConnector(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	service := &fakePrivateConnectorService{
		disabledGroup: models.PrivateConnectorGroup{
			ID:     groupID,
			OrgID:  orgID,
			Status: models.PrivateConnectorStatusDisabled,
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/"+groupID.String()+"/disable", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withChiParam(req.Context(), "id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.DisableConnector(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "DisableConnector should return ok")
	require.Equal(t, orgID, service.disableOrgID, "DisableConnector should pass active org")
	require.Equal(t, groupID, service.disableGroupID, "DisableConnector should pass connector group id")
	var resp models.SingleResponse[models.PrivateConnectorGroup]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "DisableConnector response should decode")
	require.Equal(t, models.PrivateConnectorStatusDisabled, resp.Data.Status, "DisableConnector should return disabled connector")
}

func TestPrivateConnectorHandler_CreateResource(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	service := &fakePrivateConnectorService{
		resource: models.PrivateConnectorResource{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			DisplayName:      "Production logs",
			ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
			Mode:             models.PrivateConnectorResourceModeLogs,
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/"+groupID.String()+"/resources", bytes.NewReader([]byte(`{
		"display_name":"Production logs",
		"resource_type":"victorialogs",
		"mode":"logs",
		"config":{"url":"http://victorialogs:9428"}
	}`)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", groupID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.CreateResource(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "CreateResource should return created")
	require.Equal(t, orgID, service.resourceOrgID, "CreateResource should pass active org to the service")
	require.Equal(t, groupID, service.resourceGroupID, "CreateResource should pass connector group id")
	require.Equal(t, userID, service.resourceUserID, "CreateResource should pass authenticated user")
	require.Equal(t, models.PrivateConnectorResourceTypeVictoriaLogs, service.resourceReq.ResourceType, "CreateResource should decode resource type")
	var resp models.SingleResponse[models.PrivateConnectorResource]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "CreateResource response should be valid JSON")
	require.Equal(t, resourceID, resp.Data.ID, "CreateResource should return resource metadata")
}

func TestPrivateConnectorHandler_RegisterInstance(t *testing.T) {
	t.Parallel()

	instanceID := uuid.New()
	groupID := uuid.New()
	orgID := uuid.New()
	service := &fakePrivateConnectorService{
		registerResult: privateconnectorsvc.RegisterInstanceResult{
			Instance: models.PrivateConnectorInstance{
				ID:               instanceID,
				OrgID:            orgID,
				ConnectorGroupID: groupID,
				InstanceName:     "host-a",
			},
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			GatewayRegion:    "us",
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connector/register", bytes.NewReader([]byte(`{
		"deployment_token":"143pc_bootstrap",
		"instance_name":"host-a",
		"public_key":"pub",
		"version":"v0.1.0",
		"protocol":"websocket",
		"gateway_region":"us",
		"capabilities":["victorialogs.query"]
	}`)))
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.4")
	rr := httptest.NewRecorder()

	handler.RegisterInstance(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "RegisterInstance should return created after bootstrap exchange")
	require.Equal(t, "143pc_bootstrap", service.registerReq.DeploymentToken, "RegisterInstance should decode deployment token")
	require.Equal(t, "203.0.113.10", service.registerReq.SourceIP, "RegisterInstance should pass the first forwarded client IP for token policy enforcement")
	require.Equal(t, []string{"victorialogs.query"}, service.registerReq.Capabilities, "RegisterInstance should decode capabilities")
	var resp models.SingleResponse[privateconnectorsvc.RegisterInstanceResult]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "RegisterInstance response should be valid JSON")
	require.Equal(t, instanceID, resp.Data.Instance.ID, "RegisterInstance should return instance metadata")
}

func TestPrivateConnectorSourceIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		headers  map[string]string
		remote   string
		expected string
	}{
		{
			name:     "uses first forwarded client ip",
			headers:  map[string]string{"X-Forwarded-For": "203.0.113.10, 10.0.0.4"},
			remote:   "10.0.0.4:443",
			expected: "203.0.113.10",
		},
		{
			name:     "falls back to x-real-ip",
			headers:  map[string]string{"X-Real-IP": "2001:db8::1"},
			remote:   "10.0.0.4:443",
			expected: "2001:db8::1",
		},
		{
			name:     "falls back to remote address host",
			headers:  map[string]string{},
			remote:   "198.51.100.7:443",
			expected: "198.51.100.7",
		},
		{
			name:     "rejects invalid forwarded values",
			headers:  map[string]string{"X-Forwarded-For": "unknown"},
			remote:   "198.51.100.7:443",
			expected: "198.51.100.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connector/register", nil)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			req.RemoteAddr = tt.remote

			require.Equal(t, tt.expected, privateConnectorSourceIP(req), "privateConnectorSourceIP should derive the source IP used for token policy checks")
		})
	}
}

func TestPrivateConnectorHandler_HeartbeatRequiresSignatureHeader(t *testing.T) {
	t.Parallel()

	instanceID := uuid.New()
	service := &fakePrivateConnectorService{heartbeat: models.PrivateConnectorInstance{ID: instanceID}}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connector/instances/"+instanceID.String()+"/heartbeat", bytes.NewReader([]byte(`{"version":"v0.1.1"}`)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", instanceID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.Heartbeat(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "Heartbeat should reject unsigned requests")
	require.Empty(t, service.heartbeatSig, "Heartbeat should not call the service without a signature")

	req = httptest.NewRequest(http.MethodPost, "/api/v1/private-connector/instances/"+instanceID.String()+"/heartbeat", bytes.NewReader([]byte(`{"version":"v0.1.1"}`)))
	req.Header.Set("X-143-Connector-Signature", "signed")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr = httptest.NewRecorder()

	handler.Heartbeat(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Heartbeat should accept signed requests and delegate signature verification to the service")
	require.Equal(t, instanceID, service.heartbeatID, "Heartbeat should pass path instance id to the service")
	require.Equal(t, "signed", service.heartbeatSig, "Heartbeat should pass signature header to the service")
	require.JSONEq(t, `{"version":"v0.1.1"}`, string(service.heartbeatBody), "Heartbeat should verify the exact request body bytes")
}

func TestPrivateConnectorHandler_SessionRegistersGateway(t *testing.T) {
	t.Parallel()

	instanceID := uuid.New()
	groupID := uuid.New()
	service := &fakePrivateConnectorService{
		sessionInstance: models.PrivateConnectorInstance{
			ID:               instanceID,
			ConnectorGroupID: groupID,
			Status:           models.PrivateConnectorInstanceStatusOnline,
			Capabilities:     []string{"victorialogs.query"},
		},
	}
	gateway := privateconnectorsvc.NewGateway(zerolog.Nop(), privateconnectorsvc.GatewayConfig{DispatchTimeout: time.Second})
	handler := NewPrivateConnectorHandler(service)
	handler.SetGateway(gateway)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", instanceID.String())
		handler.Session(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)))
	}))
	defer server.Close()
	payload := connector.SessionAuthPayload{
		InstanceID: instanceID,
		Nonce:      uuid.New(),
		IssuedAt:   time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
	}
	header := http.Header{}
	header.Set(connector.SignatureHeader, "signed-session")
	url := "ws" + strings.TrimPrefix(server.URL, "http") +
		"?nonce=" + payload.Nonce.String() +
		"&issued_at=" + payload.IssuedAt.Format(time.RFC3339Nano)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	require.NoError(t, err, "Session should accept an authorized websocket upgrade")
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	require.Equal(t, payload, service.sessionPayload, "Session should authorize the exact session auth payload")
	require.Equal(t, "signed-session", service.sessionSig, "Session should pass the signature header to the service")
	require.Eventually(t, func() bool {
		return gateway.ActiveSessionCount(groupID) == 1
	}, time.Second, 10*time.Millisecond, "Session should register the websocket with the gateway")
}

func TestPrivateConnectorHandler_TestResource(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	resourceID := uuid.New()
	service := &fakePrivateConnectorService{
		testResource: models.PrivateConnectorResource{
			ID:           resourceID,
			OrgID:        orgID,
			ResourceType: models.PrivateConnectorResourceTypeVictoriaLogs,
			Status:       models.PrivateConnectorResourceStatusReady,
		},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/resources/"+resourceID.String()+"/test", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", resourceID.String())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.TestResource(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "TestResource should return OK")
	require.Equal(t, orgID, service.testOrgID, "TestResource should use active org scope")
	require.Equal(t, resourceID, service.testResourceID, "TestResource should pass resource id")
	var resp models.SingleResponse[models.PrivateConnectorResource]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "TestResource response should be valid JSON")
	require.Equal(t, models.PrivateConnectorResourceStatusReady, resp.Data.Status, "TestResource should return updated resource health")
}

func TestPrivateConnectorHandler_RevokeDeploymentToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	service := &fakePrivateConnectorService{
		revokedToken: models.PrivateConnectorDeploymentToken{ID: tokenID, OrgID: orgID, RevokedByUserID: &userID},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/private-connectors/tokens/"+tokenID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", tokenID.String())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.RevokeDeploymentToken(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RevokeDeploymentToken should return OK")
	require.Equal(t, orgID, service.revokeTokenOrgID, "RevokeDeploymentToken should use active org scope")
	require.Equal(t, tokenID, service.revokeTokenID, "RevokeDeploymentToken should pass token id")
	require.Equal(t, userID, service.revokeTokenUserID, "RevokeDeploymentToken should pass current user id")
}

func TestPrivateConnectorHandler_RevokeInstance(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()
	service := &fakePrivateConnectorService{
		revokedInstance: models.PrivateConnectorInstance{ID: instanceID, OrgID: orgID, RevokedByUserID: &userID},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/private-connectors/instances/"+instanceID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", instanceID.String())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.RevokeInstance(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RevokeInstance should return OK")
	require.Equal(t, orgID, service.revokeInstOrgID, "RevokeInstance should use active org scope")
	require.Equal(t, instanceID, service.revokeInstID, "RevokeInstance should pass instance id")
	require.Equal(t, userID, service.revokeInstUserID, "RevokeInstance should pass current user id")
}

func TestPrivateConnectorHandler_RequestIdentityRotation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()
	service := &fakePrivateConnectorService{
		rotatedInstance: models.PrivateConnectorInstance{ID: instanceID, OrgID: orgID},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/instances/"+instanceID.String()+"/rotate", nil)
	req = withPrivateConnectorAuth(req, orgID, userID)
	req = req.WithContext(withChiParam(req.Context(), "id", instanceID.String()))
	rr := httptest.NewRecorder()

	handler.RequestIdentityRotation(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RequestIdentityRotation should return the rotated instance")
	require.Equal(t, orgID, service.rotateInstOrgID, "RequestIdentityRotation should use active org")
	require.Equal(t, instanceID, service.rotateInstID, "RequestIdentityRotation should target route instance")
}

func TestPrivateConnectorHandler_RequestConfigReload(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()
	service := &fakePrivateConnectorService{
		reloadedInstance: models.PrivateConnectorInstance{ID: instanceID, OrgID: orgID},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/instances/"+instanceID.String()+"/reload", nil)
	req = withPrivateConnectorAuth(req, orgID, userID)
	req = req.WithContext(withChiParam(req.Context(), "id", instanceID.String()))
	rr := httptest.NewRecorder()

	handler.RequestConfigReload(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RequestConfigReload should return the reloaded instance")
	require.Equal(t, orgID, service.reloadInstOrgID, "RequestConfigReload should use active org")
	require.Equal(t, instanceID, service.reloadInstID, "RequestConfigReload should target route instance")
}

func TestPrivateConnectorHandler_RequestConnectorUpdate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()
	service := &fakePrivateConnectorService{
		updatedInstance: models.PrivateConnectorInstance{ID: instanceID, OrgID: orgID},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connectors/instances/"+instanceID.String()+"/update", nil)
	req = withPrivateConnectorAuth(req, orgID, userID)
	req = req.WithContext(withChiParam(req.Context(), "id", instanceID.String()))
	rr := httptest.NewRecorder()

	handler.RequestConnectorUpdate(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RequestConnectorUpdate should return the updated instance")
	require.Equal(t, orgID, service.updateInstOrgID, "RequestConnectorUpdate should use active org")
	require.Equal(t, instanceID, service.updateInstID, "RequestConnectorUpdate should target route instance")
}

func TestPrivateConnectorHandler_RotateInstanceIdentity(t *testing.T) {
	t.Parallel()

	instanceID := uuid.New()
	service := &fakePrivateConnectorService{
		identityInstance: models.PrivateConnectorInstance{ID: instanceID, PublicKey: "new-key"},
	}
	handler := NewPrivateConnectorHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/private-connector/instances/"+instanceID.String()+"/identity", bytes.NewReader([]byte(`{"public_key":"new-key"}`)))
	req.Header.Set(connector.SignatureHeader, "signed")
	req = req.WithContext(withChiParam(req.Context(), "id", instanceID.String()))
	rr := httptest.NewRecorder()

	handler.RotateInstanceIdentity(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RotateInstanceIdentity should return the updated instance")
	require.Equal(t, instanceID, service.identityID, "RotateInstanceIdentity should target route instance")
	require.JSONEq(t, `{"public_key":"new-key"}`, string(service.identityBody), "RotateInstanceIdentity should pass exact request body to service")
	require.Equal(t, "signed", service.identitySig, "RotateInstanceIdentity should pass connector signature")
}
