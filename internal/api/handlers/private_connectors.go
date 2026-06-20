package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	privateconnectorsvc "github.com/assembledhq/143/internal/services/privateconnector"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"nhooyr.io/websocket"
)

type privateConnectorService interface {
	CreateConnector(ctx context.Context, orgID, userID uuid.UUID, req privateconnectorsvc.CreateConnectorRequest) (privateconnectorsvc.CreateConnectorResult, error)
	ListConnectors(ctx context.Context, orgID uuid.UUID) ([]privateconnectorsvc.ConnectorSummary, error)
	CreateDeploymentToken(ctx context.Context, orgID, userID uuid.UUID, req privateconnectorsvc.CreateDeploymentTokenRequest) (privateconnectorsvc.CreateDeploymentTokenResult, error)
	UpdateConnectorSettings(ctx context.Context, orgID, groupID uuid.UUID, req privateconnectorsvc.UpdateConnectorSettingsRequest) (models.PrivateConnectorGroup, error)
	DisableConnector(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error)
	RevokeDeploymentToken(ctx context.Context, orgID, tokenID, revokedBy uuid.UUID) (models.PrivateConnectorDeploymentToken, error)
	RegisterInstance(ctx context.Context, req privateconnectorsvc.RegisterInstanceRequest) (privateconnectorsvc.RegisterInstanceResult, error)
	RevokeInstance(ctx context.Context, orgID, instanceID, revokedBy uuid.UUID) (models.PrivateConnectorInstance, error)
	RequestIdentityRotation(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error)
	RequestConfigReload(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error)
	RequestConnectorUpdate(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error)
	RotateInstanceIdentity(ctx context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error)
	CreateResource(ctx context.Context, orgID, groupID, userID uuid.UUID, req privateconnectorsvc.CreateResourceRequest) (models.PrivateConnectorResource, error)
	TestResource(ctx context.Context, orgID, resourceID uuid.UUID) (models.PrivateConnectorResource, error)
	RecordHeartbeat(ctx context.Context, instanceID uuid.UUID, body []byte, signature string) (models.PrivateConnectorInstance, error)
	AuthorizeSession(ctx context.Context, payload connector.SessionAuthPayload, signature string) (models.PrivateConnectorInstance, error)
}

type PrivateConnectorHandler struct {
	service privateConnectorService
	gateway *privateconnectorsvc.Gateway
}

func NewPrivateConnectorHandler(service privateConnectorService) *PrivateConnectorHandler {
	return &PrivateConnectorHandler{service: service}
}

func (h *PrivateConnectorHandler) SetGateway(gateway *privateconnectorsvc.Gateway) {
	h.gateway = gateway
}

func (h *PrivateConnectorHandler) CreateConnector(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}

	var body struct {
		Name          string `json:"name"`
		Environment   string `json:"environment"`
		GatewayRegion string `json:"gateway_region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	result, err := h.service.CreateConnector(r.Context(), orgID, user.ID, privateconnectorsvc.CreateConnectorRequest{
		Name:          body.Name,
		Environment:   body.Environment,
		GatewayRegion: body.GatewayRegion,
	})
	if err != nil {
		writePrivateConnectorError(w, r, err, "CREATE_CONNECTOR_FAILED", "failed to create private connector")
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[privateconnectorsvc.CreateConnectorResult]{Data: result})
}

func (h *PrivateConnectorHandler) ListConnectors(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	connectors, err := h.service.ListConnectors(r.Context(), orgID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "LIST_CONNECTORS_FAILED", "failed to list private connectors")
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[privateconnectorsvc.ConnectorSummary]{Data: connectors})
}

func (h *PrivateConnectorHandler) CreateDeploymentToken(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	groupID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name                 string                             `json:"name"`
		Preset               models.PrivateConnectorTokenPreset `json:"preset"`
		MaxRegistrations     *int                               `json:"max_registrations"`
		AllowedSourceCIDRs   []string                           `json:"allowed_source_cidrs"`
		AllowedGatewayRegion *string                            `json:"allowed_gateway_region"`
		ExpiresAt            *time.Time                         `json:"expires_at"`
		NoExpiry             bool                               `json:"no_expiry"`
		TokenFilePath        string                             `json:"token_file_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	result, err := h.service.CreateDeploymentToken(r.Context(), orgID, user.ID, privateconnectorsvc.CreateDeploymentTokenRequest{
		ConnectorGroupID:     groupID,
		Name:                 body.Name,
		Preset:               body.Preset,
		MaxRegistrations:     body.MaxRegistrations,
		AllowedSourceCIDRs:   body.AllowedSourceCIDRs,
		AllowedGatewayRegion: body.AllowedGatewayRegion,
		ExpiresAt:            body.ExpiresAt,
		NoExpiry:             body.NoExpiry,
		TokenFilePath:        body.TokenFilePath,
	})
	if err != nil {
		writePrivateConnectorError(w, r, err, "CREATE_TOKEN_FAILED", "failed to create private connector token")
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[privateconnectorsvc.CreateDeploymentTokenResult]{Data: result})
}

func (h *PrivateConnectorHandler) UpdateConnectorSettings(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	groupID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		HealthAlertURL           *string `json:"health_alert_url"`
		OfflineAlertAfterSeconds int     `json:"offline_alert_after_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	group, err := h.service.UpdateConnectorSettings(r.Context(), orgID, groupID, privateconnectorsvc.UpdateConnectorSettingsRequest{
		HealthAlertURL:           body.HealthAlertURL,
		OfflineAlertAfterSeconds: body.OfflineAlertAfterSeconds,
	})
	if err != nil {
		writePrivateConnectorError(w, r, err, "UPDATE_CONNECTOR_FAILED", "failed to update private connector")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorGroup]{Data: group})
}

func (h *PrivateConnectorHandler) DisableConnector(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	groupID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	group, err := h.service.DisableConnector(r.Context(), orgID, groupID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "DISABLE_CONNECTOR_FAILED", "failed to disable private connector")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorGroup]{Data: group})
}

func (h *PrivateConnectorHandler) RegisterInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeploymentToken          string                          `json:"deployment_token"`
		InstanceName             string                          `json:"instance_name"`
		PublicKey                string                          `json:"public_key"`
		Version                  string                          `json:"version"`
		Protocol                 models.PrivateConnectorProtocol `json:"protocol"`
		GatewayRegion            string                          `json:"gateway_region"`
		Capabilities             []string                        `json:"capabilities"`
		HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	result, err := h.service.RegisterInstance(r.Context(), privateconnectorsvc.RegisterInstanceRequest{
		DeploymentToken:          body.DeploymentToken,
		InstanceName:             body.InstanceName,
		PublicKey:                body.PublicKey,
		Version:                  body.Version,
		Protocol:                 body.Protocol,
		GatewayRegion:            body.GatewayRegion,
		SourceIP:                 privateConnectorSourceIP(r),
		Capabilities:             body.Capabilities,
		HeartbeatIntervalSeconds: body.HeartbeatIntervalSeconds,
	})
	if err != nil {
		writePrivateConnectorError(w, r, err, "REGISTER_CONNECTOR_FAILED", "failed to register private connector")
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[privateconnectorsvc.RegisterInstanceResult]{Data: result})
}

func privateConnectorSourceIP(r *http.Request) string {
	for _, value := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip := parsePrivateConnectorIP(value); ip != "" {
			return ip
		}
	}
	if ip := parsePrivateConnectorIP(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		if ip := parsePrivateConnectorIP(host); ip != "" {
			return ip
		}
	}
	return parsePrivateConnectorIP(r.RemoteAddr)
}

func parsePrivateConnectorIP(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	if value == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.String()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return parsePrivateConnectorIP(host)
	}
	return ""
}

func (h *PrivateConnectorHandler) RevokeDeploymentToken(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	tokenID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	token, err := h.service.RevokeDeploymentToken(r.Context(), orgID, tokenID, user.ID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "REVOKE_TOKEN_FAILED", "failed to revoke private connector token")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorDeploymentToken]{Data: token})
}

func (h *PrivateConnectorHandler) RevokeInstance(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	instance, err := h.service.RevokeInstance(r.Context(), orgID, instanceID, user.ID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "REVOKE_INSTANCE_FAILED", "failed to revoke private connector instance")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) RequestIdentityRotation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	instance, err := h.service.RequestIdentityRotation(r.Context(), orgID, instanceID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "ROTATE_INSTANCE_FAILED", "failed to rotate private connector identity")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) RequestConfigReload(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	instance, err := h.service.RequestConfigReload(r.Context(), orgID, instanceID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "RELOAD_CONNECTOR_FAILED", "failed to reload private connector config")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) RequestConnectorUpdate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	instance, err := h.service.RequestConnectorUpdate(r.Context(), orgID, instanceID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "UPDATE_CONNECTOR_FAILED", "failed to update private connector")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) RotateInstanceIdentity(w http.ResponseWriter, r *http.Request) {
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	signature := r.Header.Get(connector.SignatureHeader)
	if signature == "" {
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REQUIRED", "connector signature is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "failed to read request body", err)
		return
	}
	instance, err := h.service.RotateInstanceIdentity(r.Context(), instanceID, body, signature)
	if err != nil {
		writePrivateConnectorError(w, r, err, "ROTATE_INSTANCE_IDENTITY_FAILED", "failed to rotate connector identity")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) CreateResource(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	groupID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		DisplayName  string                              `json:"display_name"`
		ResourceType models.PrivateConnectorResourceType `json:"resource_type"`
		Mode         models.PrivateConnectorResourceMode `json:"mode"`
		Config       json.RawMessage                     `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	resource, err := h.service.CreateResource(r.Context(), orgID, groupID, user.ID, privateconnectorsvc.CreateResourceRequest{
		DisplayName:  body.DisplayName,
		ResourceType: body.ResourceType,
		Mode:         body.Mode,
		Config:       body.Config,
	})
	if err != nil {
		writePrivateConnectorError(w, r, err, "CREATE_RESOURCE_FAILED", "failed to create private connector resource")
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PrivateConnectorResource]{Data: resource})
}

func (h *PrivateConnectorHandler) TestResource(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	resourceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	resource, err := h.service.TestResource(r.Context(), orgID, resourceID)
	if err != nil {
		writePrivateConnectorError(w, r, err, "TEST_RESOURCE_FAILED", "failed to test private connector resource")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorResource]{Data: resource})
}

func (h *PrivateConnectorHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	signature := r.Header.Get(connector.SignatureHeader)
	if signature == "" {
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REQUIRED", "connector signature is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "failed to read request body", err)
		return
	}
	instance, err := h.service.RecordHeartbeat(r.Context(), instanceID, body, signature)
	if err != nil {
		writePrivateConnectorError(w, r, err, "HEARTBEAT_FAILED", "failed to record connector heartbeat")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PrivateConnectorInstance]{Data: instance})
}

func (h *PrivateConnectorHandler) Session(w http.ResponseWriter, r *http.Request) {
	if h.gateway == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PRIVATE_CONNECTOR_GATEWAY_UNAVAILABLE", "private connector gateway is unavailable")
		return
	}
	instanceID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	signature := r.Header.Get(connector.SignatureHeader)
	if signature == "" {
		writeError(w, r, http.StatusUnauthorized, "SIGNATURE_REQUIRED", "connector signature is required")
		return
	}
	nonce, err := uuid.Parse(r.URL.Query().Get("nonce"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_AUTH", "session nonce must be a valid UUID", err)
		return
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("issued_at"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_AUTH", "session issued_at must be an RFC3339 timestamp", err)
		return
	}
	instance, err := h.service.AuthorizeSession(r.Context(), connector.SessionAuthPayload{
		InstanceID: instanceID,
		Nonce:      nonce,
		IssuedAt:   issuedAt,
	}, signature)
	if err != nil {
		writePrivateConnectorError(w, r, err, "SESSION_AUTH_FAILED", "failed to authorize connector session")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	})
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("connector_instance_id", instanceID.String()).Msg("failed to accept private connector websocket session")
		return
	}
	if err := h.gateway.ServeSession(r.Context(), conn, instance); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("connector_instance_id", instance.ID.String()).Msg("private connector websocket session ended")
	}
}

func writePrivateConnectorError(w http.ResponseWriter, r *http.Request, err error, fallbackCode, fallbackMessage string) {
	switch {
	case errors.Is(err, privateconnectorsvc.ErrNameRequired),
		errors.Is(err, privateconnectorsvc.ErrDeploymentTokenRequired),
		errors.Is(err, privateconnectorsvc.ErrInvalidGatewayRegion),
		errors.Is(err, privateconnectorsvc.ErrInvalidPublicKey),
		errors.Is(err, privateconnectorsvc.ErrInvalidHeartbeat),
		errors.Is(err, privateconnectorsvc.ErrUnsupportedResourceProbe),
		errors.Is(err, privateconnectorsvc.ErrConnectorDisabled):
		writeError(w, r, http.StatusBadRequest, "INVALID_PRIVATE_CONNECTOR_REQUEST", err.Error(), err)
	case errors.Is(err, privateconnectorsvc.ErrInvalidSignature),
		errors.Is(err, connector.ErrActionSignature),
		errors.Is(err, connector.ErrActionUnauthorized),
		errors.Is(err, connector.ErrActionExpired),
		errors.Is(err, connector.ErrActionClockSkew),
		errors.Is(err, connector.ErrActionReplay):
		writeError(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "invalid connector signature", err)
	default:
		writeError(w, r, http.StatusInternalServerError, fallbackCode, fallbackMessage, err)
	}
}
