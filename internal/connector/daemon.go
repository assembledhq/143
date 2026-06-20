package connector

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const SignatureHeader = "X-143-Connector-Signature"

var (
	ErrIdentityPathRequired    = errors.New("connector identity path is required")
	ErrStatePathRequired       = errors.New("connector state path is required")
	ErrDeploymentTokenRequired = errors.New("connector deployment token is required")
)

type Identity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

func (i Identity) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(i.PublicKey)
}

func LoadOrCreateIdentity(path string) (Identity, error) {
	if strings.TrimSpace(path) == "" {
		return Identity{}, ErrIdentityPathRequired
	}
	if raw, err := os.ReadFile(path); err == nil {
		key, err := decodePrivateKey(raw)
		if err != nil {
			return Identity{}, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return Identity{}, fmt.Errorf("restrict connector identity key: %w", err)
		}
		return identityFromPrivateKey(key), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("read connector identity key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Identity{}, fmt.Errorf("create connector identity directory: %w", err)
	}
	identity, err := GenerateIdentity()
	if err != nil {
		return Identity{}, err
	}
	if err := SaveIdentity(path, identity); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func GenerateIdentity() (Identity, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, fmt.Errorf("generate connector identity key: %w", err)
	}
	return identityFromPrivateKey(privateKey), nil
}

func SaveIdentity(path string, identity Identity) error {
	if strings.TrimSpace(path) == "" {
		return ErrIdentityPathRequired
	}
	if len(identity.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("write connector identity key: expected %d bytes, got %d", ed25519.PrivateKeySize, len(identity.PrivateKey))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create connector identity directory: %w", err)
	}
	if err := writeRestrictedFile(path, []byte(base64.StdEncoding.EncodeToString(identity.PrivateKey)+"\n")); err != nil {
		return fmt.Errorf("write connector identity key: %w", err)
	}
	return nil
}

func identityFromPrivateKey(privateKey ed25519.PrivateKey) Identity {
	return Identity{
		PrivateKey: privateKey,
		PublicKey:  privateKey.Public().(ed25519.PublicKey),
	}
}

func decodePrivateKey(raw []byte) (ed25519.PrivateKey, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode connector identity key: %w", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("decode connector identity key: expected %d bytes, got %d", ed25519.PrivateKeySize, len(key))
	}
	return ed25519.PrivateKey(key), nil
}

type DaemonConfig struct {
	APIURL                   string
	DeploymentToken          string
	ConfigPath               string
	IdentityPath             string
	StatePath                string
	InstanceName             string
	Version                  string
	Protocol                 models.PrivateConnectorProtocol
	GatewayRegion            string
	Capabilities             []string
	HeartbeatIntervalSeconds int
	HTTPClient               *http.Client
}

type ConnectorState struct {
	InstanceID               uuid.UUID                       `json:"instance_id"`
	OrgID                    uuid.UUID                       `json:"org_id"`
	ConnectorGroupID         uuid.UUID                       `json:"connector_group_id"`
	GatewayRegion            string                          `json:"gateway_region"`
	Protocol                 models.PrivateConnectorProtocol `json:"protocol"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

type BootstrapResult struct {
	Identity Identity
	State    ConnectorState
}

func Bootstrap(ctx context.Context, cfg DaemonConfig) (BootstrapResult, error) {
	if strings.TrimSpace(cfg.StatePath) == "" {
		return BootstrapResult{}, ErrStatePathRequired
	}
	identity, err := LoadOrCreateIdentity(cfg.IdentityPath)
	if err != nil {
		return BootstrapResult{}, err
	}
	state, err := LoadConnectorState(cfg.StatePath)
	if err == nil {
		return BootstrapResult{Identity: identity, State: state}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return BootstrapResult{}, err
	}
	token := strings.TrimSpace(cfg.DeploymentToken)
	if token == "" {
		return BootstrapResult{}, ErrDeploymentTokenRequired
	}

	client := NewDaemonClient(cfg.APIURL, cfg.HTTPClient)
	state, err = client.RegisterInstance(ctx, registerInstancePayload{
		DeploymentToken:          token,
		InstanceName:             cfg.InstanceName,
		PublicKey:                identity.PublicKeyBase64(),
		Version:                  cfg.Version,
		Protocol:                 cfg.Protocol,
		GatewayRegion:            cfg.GatewayRegion,
		Capabilities:             cfg.Capabilities,
		HeartbeatIntervalSeconds: cfg.HeartbeatIntervalSeconds,
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := SaveConnectorState(cfg.StatePath, state); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{Identity: identity, State: state}, nil
}

func LoadConnectorState(path string) (ConnectorState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ConnectorState{}, err
	}
	var state ConnectorState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ConnectorState{}, fmt.Errorf("decode connector state: %w", err)
	}
	if state.InstanceID == uuid.Nil {
		return ConnectorState{}, fmt.Errorf("decode connector state: missing instance_id")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return ConnectorState{}, fmt.Errorf("restrict connector state: %w", err)
	}
	return state, nil
}

func SaveConnectorState(path string, state ConnectorState) error {
	if strings.TrimSpace(path) == "" {
		return ErrStatePathRequired
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create connector state directory: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connector state: %w", err)
	}
	raw = append(raw, '\n')
	return writeRestrictedFile(path, raw)
}

func writeRestrictedFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	return file.Sync()
}

type DaemonClient struct {
	baseURL string
	client  *http.Client
}

func NewDaemonClient(baseURL string, client *http.Client) *DaemonClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &DaemonClient{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

type registerInstancePayload struct {
	DeploymentToken          string                          `json:"deployment_token"`
	InstanceName             string                          `json:"instance_name"`
	PublicKey                string                          `json:"public_key"`
	Version                  string                          `json:"version"`
	Protocol                 models.PrivateConnectorProtocol `json:"protocol"`
	GatewayRegion            string                          `json:"gateway_region"`
	Capabilities             []string                        `json:"capabilities"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

type registerInstanceResultPayload struct {
	Instance                 models.PrivateConnectorInstance `json:"instance"`
	OrgID                    uuid.UUID                       `json:"org_id"`
	ConnectorGroupID         uuid.UUID                       `json:"connector_group_id"`
	GatewayRegion            string                          `json:"gateway_region"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

func (c *DaemonClient) RegisterInstance(ctx context.Context, payload registerInstancePayload) (ConnectorState, error) {
	var resp models.SingleResponse[registerInstanceResultPayload]
	if err := c.postJSON(ctx, "/api/v1/private-connector/register", "", payload, &resp); err != nil {
		return ConnectorState{}, err
	}
	protocol := payload.Protocol
	if protocol == "" {
		protocol = resp.Data.Instance.Protocol
	}
	if protocol == "" {
		protocol = models.PrivateConnectorProtocolWebSocket
	}
	return ConnectorState{
		InstanceID:               resp.Data.Instance.ID,
		OrgID:                    resp.Data.OrgID,
		ConnectorGroupID:         resp.Data.ConnectorGroupID,
		GatewayRegion:            resp.Data.GatewayRegion,
		Protocol:                 protocol,
		HeartbeatIntervalSeconds: resp.Data.HeartbeatIntervalSeconds,
	}, nil
}

type HeartbeatPayload struct {
	Version                  string                          `json:"version"`
	Protocol                 models.PrivateConnectorProtocol `json:"protocol"`
	Capabilities             []string                        `json:"capabilities"`
	HeartbeatIntervalSeconds int                             `json:"heartbeat_interval_seconds"`
}

func (c *DaemonClient) SendHeartbeat(ctx context.Context, identity Identity, state ConnectorState, payload HeartbeatPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(identity.PrivateKey, raw))
	var resp models.SingleResponse[models.PrivateConnectorInstance]
	return c.postRawJSON(ctx, "/api/v1/private-connector/instances/"+state.InstanceID.String()+"/heartbeat", signature, raw, &resp)
}

func (c *DaemonClient) RotateInstanceIdentity(ctx context.Context, current Identity, state ConnectorState, identityPath string) (Identity, error) {
	next, err := GenerateIdentity()
	if err != nil {
		return Identity{}, err
	}
	raw, err := json.Marshal(struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: next.PublicKeyBase64()})
	if err != nil {
		return Identity{}, fmt.Errorf("encode identity rotation request: %w", err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(current.PrivateKey, raw))
	var resp models.SingleResponse[models.PrivateConnectorInstance]
	if err := c.postRawJSON(ctx, "/api/v1/private-connector/instances/"+state.InstanceID.String()+"/identity", signature, raw, &resp); err != nil {
		return Identity{}, err
	}
	if err := SaveIdentity(identityPath, next); err != nil {
		return Identity{}, err
	}
	return next, nil
}

type GatewaySessionConfig struct {
	GatewayPublicKey   ed25519.PublicKey
	ResourceIDs        map[uuid.UUID]struct{}
	ResourceIDsFunc    func() map[uuid.UUID]struct{}
	Heartbeat          HeartbeatPayload
	HeartbeatFunc      func() HeartbeatPayload
	ConfigHandler      ConfigPushHandler
	RotateIdentityFunc func(context.Context) (string, error)
	ReloadConfigFunc   func(context.Context) error
	UpdateFunc         func(context.Context) (UpdateResult, error)
	NonceCache         *NonceCache
	Now                func() time.Time
}

type UpdateResult struct {
	Started bool   `json:"started"`
	Message string `json:"message,omitempty"`
}

type ConfigApplyResult struct {
	Version      int64
	Capabilities []string
}

type ConfigPushHandler interface {
	ApplyConfigPush(ctx context.Context, frame ConfigPushFrame) (ConfigApplyResult, error)
}

func (c *DaemonClient) RunGatewaySession(ctx context.Context, identity Identity, state ConnectorState, registry *ProviderRegistry, cfg GatewaySessionConfig) error {
	if len(cfg.GatewayPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: invalid gateway public key", ErrActionSignature)
	}
	if registry == nil {
		return fmt.Errorf("%w: provider registry is nil", ErrCapabilityUnsupported)
	}
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now().UTC()
	}
	sessionAuth := SessionAuthPayload{
		InstanceID: state.InstanceID,
		Nonce:      uuid.New(),
		IssuedAt:   now,
	}
	signature, err := SignSessionAuth(identity.PrivateKey, sessionAuth)
	if err != nil {
		return fmt.Errorf("sign gateway session auth: %w", err)
	}
	sessionURL := c.websocketURL("/api/v1/private-connector/instances/" + state.InstanceID.String() + "/session")
	q := sessionURL.Query()
	q.Set("nonce", sessionAuth.Nonce.String())
	q.Set("issued_at", sessionAuth.IssuedAt.Format(time.RFC3339Nano))
	sessionURL.RawQuery = q.Encode()
	headers := http.Header{}
	headers.Set(SignatureHeader, signature)
	conn, _, err := websocket.Dial(ctx, sessionURL.String(), &websocket.DialOptions{
		HTTPClient:      c.client,
		HTTPHeader:      headers,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("connect gateway session: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "connector session closed")

	nonceCache := cfg.NonceCache
	if nonceCache == nil {
		nonceCache = NewNonceCache(5 * time.Minute)
	}
	writeCtx, cancelWrites := context.WithCancel(ctx)
	defer cancelWrites()
	var writeMu sync.Mutex
	errCh := make(chan error, 1)
	if cfg.HeartbeatIntervalSeconds() > 0 {
		go func() {
			errCh <- c.writeGatewayHeartbeats(writeCtx, conn, &writeMu, cfg.HeartbeatPayload)
		}()
	}

	for {
		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
		default:
		}
		var msg GatewayMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			status := websocket.CloseStatus(err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
				return nil
			}
			return fmt.Errorf("read gateway message: %w", err)
		}
		if msg.Type == GatewayMessageConfigPush {
			response := c.handleGatewayConfigPush(ctx, state, cfg, msg)
			writeMu.Lock()
			err := wsjson.Write(ctx, conn, response)
			writeMu.Unlock()
			if err != nil {
				return fmt.Errorf("write gateway config ack: %w", err)
			}
			if response.ConfigAck != nil && response.ConfigAck.Applied {
				if err := c.writeGatewayHeartbeat(ctx, conn, &writeMu, cfg.HeartbeatPayload()); err != nil {
					return err
				}
			}
			continue
		}
		if msg.Type != GatewayMessageActionRequest || msg.Request == nil {
			continue
		}
		response := c.handleGatewayAction(ctx, registry, state, cfg, nonceCache, msg)
		writeMu.Lock()
		err := wsjson.Write(ctx, conn, response)
		writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("write gateway action response: %w", err)
		}
	}
}

func (cfg GatewaySessionConfig) HeartbeatIntervalSeconds() int {
	payload := cfg.HeartbeatPayload()
	if payload.HeartbeatIntervalSeconds > 0 {
		return payload.HeartbeatIntervalSeconds
	}
	return 30
}

func (cfg GatewaySessionConfig) HeartbeatPayload() HeartbeatPayload {
	if cfg.HeartbeatFunc != nil {
		return cfg.HeartbeatFunc()
	}
	return cfg.Heartbeat
}

func (cfg GatewaySessionConfig) AllowedResourceIDs() map[uuid.UUID]struct{} {
	if cfg.ResourceIDsFunc != nil {
		out := cfg.ResourceIDsFunc()
		if out == nil {
			out = make(map[uuid.UUID]struct{})
		}
		out[ConnectorControlResourceID] = struct{}{}
		return out
	}
	out := make(map[uuid.UUID]struct{}, len(cfg.ResourceIDs))
	for id := range cfg.ResourceIDs {
		out[id] = struct{}{}
	}
	out[ConnectorControlResourceID] = struct{}{}
	return out
}

func (c *DaemonClient) writeGatewayHeartbeats(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, payload func() HeartbeatPayload) error {
	interval := time.Duration(payload().HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.writeGatewayHeartbeat(ctx, conn, writeMu, payload()); err != nil {
				return fmt.Errorf("write gateway heartbeat: %w", err)
			}
		}
	}
}

func (c *DaemonClient) writeGatewayHeartbeat(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, payload HeartbeatPayload) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return wsjson.Write(ctx, conn, GatewayMessage{
		Type: GatewayMessageHeartbeat,
		Heartbeat: &HeartbeatFrame{
			Version:                  payload.Version,
			Protocol:                 string(payload.Protocol),
			Capabilities:             payload.Capabilities,
			HeartbeatIntervalSeconds: payload.HeartbeatIntervalSeconds,
		},
	})
}

func (c *DaemonClient) handleGatewayAction(ctx context.Context, registry *ProviderRegistry, state ConnectorState, cfg GatewaySessionConfig, nonceCache *NonceCache, msg GatewayMessage) GatewayMessage {
	req := *msg.Request
	if err := VerifyActionRequest(cfg.GatewayPublicKey, req, msg.Signature, VerifyOptions{
		OrgID:       state.OrgID,
		ConnectorID: state.ConnectorGroupID,
		ResourceIDs: cfg.AllowedResourceIDs(),
		Now:         cfg.Now,
		NonceCache:  nonceCache,
	}); err != nil {
		return gatewayActionError(msg.RequestID, "unauthorized", err)
	}
	if req.Capability == CapabilityRotateIdentity {
		if cfg.RotateIdentityFunc == nil {
			return gatewayActionError(msg.RequestID, "control_unsupported", errors.New("connector identity rotation is not configured"))
		}
		publicKey, err := cfg.RotateIdentityFunc(ctx)
		if err != nil {
			return gatewayActionError(msg.RequestID, "identity_rotation_failed", err)
		}
		payload, err := json.Marshal(struct {
			PublicKey string `json:"public_key"`
		}{PublicKey: publicKey})
		if err != nil {
			return gatewayActionError(msg.RequestID, "identity_rotation_failed", err)
		}
		return GatewayMessage{
			Type:      GatewayMessageActionResponse,
			RequestID: req.RequestID,
			Result:    &ActionResult{Payload: payload},
		}
	}
	if req.Capability == CapabilityReloadConfig {
		if cfg.ReloadConfigFunc == nil {
			return gatewayActionError(msg.RequestID, "control_unsupported", errors.New("connector config reload is not configured"))
		}
		if err := cfg.ReloadConfigFunc(ctx); err != nil {
			return gatewayActionError(msg.RequestID, "config_reload_failed", err)
		}
		return GatewayMessage{
			Type:      GatewayMessageActionResponse,
			RequestID: req.RequestID,
			Result:    &ActionResult{Payload: json.RawMessage(`{"reloaded":true}`)},
		}
	}
	if req.Capability == CapabilityTriggerUpdate {
		if cfg.UpdateFunc == nil {
			return gatewayActionError(msg.RequestID, "control_unsupported", errors.New("connector update is not configured"))
		}
		result, err := cfg.UpdateFunc(ctx)
		if err != nil {
			return gatewayActionError(msg.RequestID, "update_failed", err)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return gatewayActionError(msg.RequestID, "update_failed", err)
		}
		return GatewayMessage{
			Type:      GatewayMessageActionResponse,
			RequestID: req.RequestID,
			Result:    &ActionResult{Payload: payload},
		}
	}
	result, err := registry.Dispatch(ctx, req)
	if err != nil {
		return gatewayActionError(msg.RequestID, "provider_error", err)
	}
	return GatewayMessage{
		Type:      GatewayMessageActionResponse,
		RequestID: req.RequestID,
		Result:    &result,
	}
}

func (c *DaemonClient) handleGatewayConfigPush(ctx context.Context, state ConnectorState, cfg GatewaySessionConfig, msg GatewayMessage) GatewayMessage {
	if msg.Config == nil {
		return gatewayConfigAck(0, false, nil, "config_invalid", errors.New("config push payload is required"))
	}
	if err := VerifyConfigPush(cfg.GatewayPublicKey, *msg.Config, msg.Signature, ConfigPushVerifyOptions{
		OrgID:       state.OrgID,
		ConnectorID: state.ConnectorGroupID,
		Now:         cfg.Now,
	}); err != nil {
		return gatewayConfigAck(msg.Config.Version, false, nil, "config_unauthorized", err)
	}
	if cfg.ConfigHandler == nil {
		return gatewayConfigAck(msg.Config.Version, false, nil, "config_unsupported", errors.New("connector config push handler is not configured"))
	}
	result, err := cfg.ConfigHandler.ApplyConfigPush(ctx, *msg.Config)
	if err != nil {
		code := "config_apply_failed"
		if errors.Is(err, ErrConfigPushStale) {
			code = "config_stale"
		}
		return gatewayConfigAck(msg.Config.Version, false, nil, code, err)
	}
	return gatewayConfigAck(result.Version, true, result.Capabilities, "", nil)
}

func gatewayActionError(requestID uuid.UUID, code string, err error) GatewayMessage {
	return GatewayMessage{
		Type:      GatewayMessageActionResponse,
		RequestID: requestID,
		Error: &ActionError{
			Code:    code,
			Message: err.Error(),
		},
	}
}

func gatewayConfigAck(version int64, applied bool, capabilities []string, code string, err error) GatewayMessage {
	msg := GatewayMessage{
		Type: GatewayMessageConfigAck,
		ConfigAck: &ConfigAckFrame{
			Version:      version,
			Applied:      applied,
			Capabilities: capabilities,
		},
	}
	if err != nil {
		msg.Error = &ActionError{Code: code, Message: err.Error()}
	}
	return msg
}

func (c *DaemonClient) websocketURL(path string) *url.URL {
	raw := c.baseURL + path
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{Scheme: "ws", Host: strings.TrimPrefix(c.baseURL, "http://"), Path: path}
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return u
}

func (c *DaemonClient) postJSON(ctx context.Context, path string, signature string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode connector request: %w", err)
	}
	return c.postRawJSON(ctx, path, signature, raw, out)
}

func (c *DaemonClient) postRawJSON(ctx context.Context, path string, signature string, raw []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set(SignatureHeader, signature)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("connector API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode connector API response: %w", err)
	}
	return nil
}
