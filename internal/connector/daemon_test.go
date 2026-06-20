package connector

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestLoadOrCreateIdentityPersistsEd25519KeyWithRestrictedMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "identity.key")

	identity, err := LoadOrCreateIdentity(path)
	require.NoError(t, err, "LoadOrCreateIdentity should create a missing identity key")
	require.Len(t, identity.PrivateKey, ed25519.PrivateKeySize, "identity should contain an Ed25519 private key")
	require.Len(t, identity.PublicKey, ed25519.PublicKeySize, "identity should expose the matching Ed25519 public key")

	info, err := os.Stat(path)
	require.NoError(t, err, "identity file should be written")
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "identity key should be readable only by the connector user")

	loaded, err := LoadOrCreateIdentity(path)
	require.NoError(t, err, "LoadOrCreateIdentity should load an existing identity key")
	require.Equal(t, identity.PrivateKey, loaded.PrivateKey, "existing identity should be stable across restarts")
	require.Equal(t, identity.PublicKeyBase64(), loaded.PublicKeyBase64(), "public key encoding should be stable across restarts")
}

func TestBootstrapRegistersOnceAndPersistsState(t *testing.T) {
	t.Parallel()

	instanceID := uuid.New()
	orgID := uuid.New()
	groupID := uuid.New()
	var registerCalls int
	var registeredPublicKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method, "bootstrap should POST to the registration endpoint")
		require.Equal(t, "/api/v1/private-connector/register", r.URL.Path, "bootstrap should call the private connector registration endpoint")
		registerCalls++

		var req registerInstancePayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "registration request should be valid JSON")
		require.Equal(t, "143pc_bootstrap", req.DeploymentToken, "registration should send the deployment token only during first bootstrap")
		require.Equal(t, "edge-a", req.InstanceName, "registration should include configured instance name")
		require.Equal(t, []string{"victorialogs.query"}, req.Capabilities, "registration should advertise configured capabilities")
		registeredPublicKey = req.PublicKey

		writeTestJSON(t, w, models.SingleResponse[registerInstanceResultPayload]{
			Data: registerInstanceResultPayload{
				Instance: models.PrivateConnectorInstance{
					ID:                       instanceID,
					OrgID:                    orgID,
					ConnectorGroupID:         groupID,
					PublicKey:                req.PublicKey,
					HeartbeatIntervalSeconds: 5,
				},
				OrgID:                    orgID,
				ConnectorGroupID:         groupID,
				GatewayRegion:            "us",
				HeartbeatIntervalSeconds: 5,
			},
		})
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := DaemonConfig{
		APIURL:                   server.URL,
		DeploymentToken:          "143pc_bootstrap",
		IdentityPath:             filepath.Join(dir, "identity.key"),
		StatePath:                filepath.Join(dir, "state.json"),
		InstanceName:             "edge-a",
		Version:                  "v0.1.0",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		GatewayRegion:            "us",
		Capabilities:             []string{"victorialogs.query"},
		HeartbeatIntervalSeconds: 5,
		HTTPClient:               server.Client(),
	}

	first, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err, "Bootstrap should register a connector with a bootstrap token")
	require.Equal(t, instanceID, first.State.InstanceID, "Bootstrap should persist the registered instance id")
	require.NotEmpty(t, registeredPublicKey, "registration should send the generated public key")
	require.Equal(t, registeredPublicKey, first.Identity.PublicKeyBase64(), "registration should send the persisted identity public key")

	stateInfo, err := os.Stat(cfg.StatePath)
	require.NoError(t, err, "Bootstrap should write registration state")
	require.Equal(t, os.FileMode(0o600), stateInfo.Mode().Perm(), "registration state should be readable only by the connector user")

	secondCfg := cfg
	secondCfg.DeploymentToken = ""
	secondCfg.APIURL = "http://127.0.0.1:1"
	second, err := Bootstrap(context.Background(), secondCfg)
	require.NoError(t, err, "Bootstrap should reuse persisted state without a deployment token")
	require.Equal(t, first.State, second.State, "Bootstrap should reuse the durable registration state")
	require.Equal(t, 1, registerCalls, "Bootstrap should not register again once state exists")
}

func TestSendHeartbeatSignsExactPayload(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	instanceID := uuid.New()
	var sawHeartbeat bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method, "heartbeat should POST to the heartbeat endpoint")
		require.Equal(t, "/api/v1/private-connector/instances/"+instanceID.String()+"/heartbeat", r.URL.Path, "heartbeat should target the registered instance")
		var raw map[string]any
		body := readTestBody(t, r)
		require.NoError(t, json.Unmarshal(body, &raw), "heartbeat body should be valid JSON")
		signature, err := base64.StdEncoding.DecodeString(r.Header.Get(SignatureHeader))
		require.NoError(t, err, "heartbeat signature should be base64 encoded")
		require.True(t, ed25519.Verify(identity.PublicKey, body, signature), "heartbeat signature should cover the exact request body")
		require.Equal(t, "v0.1.0", raw["version"], "heartbeat should include connector version")
		sawHeartbeat = true
		writeTestJSON(t, w, models.SingleResponse[models.PrivateConnectorInstance]{Data: models.PrivateConnectorInstance{ID: instanceID}})
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	err = client.SendHeartbeat(context.Background(), identity, ConnectorState{InstanceID: instanceID}, HeartbeatPayload{
		Version:                  "v0.1.0",
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		Capabilities:             []string{"victorialogs.query"},
		HeartbeatIntervalSeconds: 5,
	})
	require.NoError(t, err, "SendHeartbeat should post a signed heartbeat")
	require.True(t, sawHeartbeat, "test server should receive the heartbeat")
}

func TestDaemonClientRotateInstanceIdentitySignsWithCurrentIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity.key")
	identity, err := LoadOrCreateIdentity(identityPath)
	require.NoError(t, err, "test identity should be created")
	instanceID := uuid.New()
	serverSawPublicKey := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/private-connector/instances/"+instanceID.String()+"/identity", r.URL.Path, "identity rotation should target the instance endpoint")
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr, "test server should read rotation body")
		signature, decodeErr := base64.StdEncoding.DecodeString(r.Header.Get(SignatureHeader))
		require.NoError(t, decodeErr, "rotation signature should be base64")
		require.True(t, ed25519.Verify(identity.PublicKey, body, signature), "rotation request should be signed by current identity")
		var req struct {
			PublicKey string `json:"public_key"`
		}
		require.NoError(t, json.Unmarshal(body, &req), "rotation body should decode")
		serverSawPublicKey <- req.PublicKey
		writeTestJSON(t, w, models.SingleResponse[models.PrivateConnectorInstance]{Data: models.PrivateConnectorInstance{ID: instanceID, PublicKey: req.PublicKey}})
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	newIdentity, err := client.RotateInstanceIdentity(context.Background(), identity, ConnectorState{InstanceID: instanceID}, identityPath)

	require.NoError(t, err, "RotateInstanceIdentity should submit and persist a new identity")
	require.NotEqual(t, identity.PublicKeyBase64(), newIdentity.PublicKeyBase64(), "rotation should generate a replacement identity")
	select {
	case publicKey := <-serverSawPublicKey:
		require.Equal(t, newIdentity.PublicKeyBase64(), publicKey, "server should receive the replacement public key")
	case <-time.After(time.Second):
		require.Fail(t, "server should receive rotation request")
	}
	loaded, err := LoadOrCreateIdentity(identityPath)
	require.NoError(t, err, "rotated identity should reload")
	require.Equal(t, newIdentity.PublicKeyBase64(), loaded.PublicKeyBase64(), "rotated identity should be persisted")
}

func TestRunGatewaySessionSignsSessionAndDispatchesActions(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	gatewayPublicKey, gatewayPrivateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test gateway key should be generated")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	resourceID := uuid.New()
	requestID := uuid.New()
	issuedAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.Add(time.Minute)
	params := json.RawMessage(`{"query":"service:api"}`)
	registry := NewProviderRegistry()
	provider := &testGatewayProvider{resourceID: resourceID}
	require.NoError(t, registry.Register(provider), "test provider should register")
	serverSawSessionAuth := make(chan SessionAuthPayload, 1)
	serverSawResponse := make(chan GatewayMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/private-connector/instances/"+instanceID.String()+"/session", r.URL.Path, "gateway session should target the registered instance")
		nonce, parseErr := uuid.Parse(r.URL.Query().Get("nonce"))
		require.NoError(t, parseErr, "session nonce query parameter should be a UUID")
		sessionIssuedAt, parseErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("issued_at"))
		require.NoError(t, parseErr, "session issued_at query parameter should be RFC3339")
		payload := SessionAuthPayload{InstanceID: instanceID, Nonce: nonce, IssuedAt: sessionIssuedAt}
		require.NoError(t, VerifySessionAuth(identity.PublicKey, payload, r.Header.Get(SignatureHeader), SessionAuthVerifyOptions{
			InstanceID: instanceID,
			Now:        func() time.Time { return sessionIssuedAt },
		}), "gateway session auth should be signed by the connector identity")
		serverSawSessionAuth <- payload
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, acceptErr, "test gateway should accept websocket")
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		req := ActionRequest{
			OrgID:       orgID,
			ConnectorID: groupID,
			ResourceID:  resourceID,
			Capability:  "victorialogs.query",
			RequestID:   requestID,
			IssuedAt:    issuedAt,
			ExpiresAt:   expiresAt,
			Params:      params,
		}
		signature, signErr := SignActionRequest(gatewayPrivateKey, req)
		require.NoError(t, signErr, "test action request should sign")
		require.NoError(t, wsjson.Write(r.Context(), conn, GatewayMessage{
			Type:      GatewayMessageActionRequest,
			RequestID: requestID,
			Request:   &req,
			Signature: signature,
		}), "test gateway should send an action request")
		var response GatewayMessage
		require.NoError(t, wsjson.Read(r.Context(), conn, &response), "test gateway should receive an action response")
		serverSawResponse <- response
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.RunGatewaySession(ctx, identity, ConnectorState{
			InstanceID:       instanceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		}, registry, GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			ResourceIDs:      map[uuid.UUID]struct{}{resourceID: {}},
			Heartbeat: HeartbeatPayload{
				Version:                  "v0.1.0",
				Protocol:                 models.PrivateConnectorProtocolWebSocket,
				Capabilities:             []string{"victorialogs.query"},
				HeartbeatIntervalSeconds: 5,
			},
			Now: func() time.Time { return issuedAt },
		})
	}()

	select {
	case payload := <-serverSawSessionAuth:
		require.Equal(t, instanceID, payload.InstanceID, "RunGatewaySession should authenticate the registered instance")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive signed session auth before timeout")
	}
	select {
	case response := <-serverSawResponse:
		require.Equal(t, GatewayMessageActionResponse, response.Type, "RunGatewaySession should respond to action requests")
		require.Equal(t, requestID, response.RequestID, "RunGatewaySession should correlate action responses by request id")
		require.Nil(t, response.Error, "RunGatewaySession should not return an error for authorized provider actions")
		require.NotNil(t, response.Result, "RunGatewaySession should include provider result payload")
		require.JSONEq(t, `{"ok":true}`, string(response.Result.Payload), "RunGatewaySession should return the provider payload")
		require.Equal(t, requestID, provider.sawRequest.RequestID, "provider should receive the signed gateway action request")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive action response before timeout")
	}
	cancel()
	select {
	case err := <-errCh:
		require.True(t, errors.Is(err, context.Canceled) || err == nil, "RunGatewaySession should stop cleanly after context cancellation: %v", err)
	case <-time.After(time.Second):
		require.Fail(t, "RunGatewaySession should exit after context cancellation")
	}
}

func TestRunGatewaySessionHandlesRotateIdentityControlAction(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	gatewayPublicKey, gatewayPrivateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test gateway key should be generated")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	requestID := uuid.New()
	issuedAt := time.Date(2026, 6, 20, 13, 0, 0, 0, time.UTC)
	rotated := make(chan struct{}, 1)
	serverSawResponse := make(chan GatewayMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, acceptErr, "test gateway should accept websocket")
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		req := ActionRequest{
			OrgID:       orgID,
			ConnectorID: groupID,
			ResourceID:  ConnectorControlResourceID,
			Capability:  CapabilityRotateIdentity,
			RequestID:   requestID,
			IssuedAt:    issuedAt,
			ExpiresAt:   issuedAt.Add(time.Minute),
			Params:      json.RawMessage(`{"instance_id":"` + instanceID.String() + `"}`),
		}
		signature, signErr := SignActionRequest(gatewayPrivateKey, req)
		require.NoError(t, signErr, "test rotate request should sign")
		require.NoError(t, wsjson.Write(r.Context(), conn, GatewayMessage{
			Type:      GatewayMessageActionRequest,
			RequestID: requestID,
			Request:   &req,
			Signature: signature,
		}), "test gateway should send rotate action")
		var response GatewayMessage
		require.NoError(t, wsjson.Read(r.Context(), conn, &response), "test gateway should receive rotate response")
		serverSawResponse <- response
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.RunGatewaySession(ctx, identity, ConnectorState{
			InstanceID:       instanceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		}, NewProviderRegistry(), GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			RotateIdentityFunc: func(context.Context) (string, error) {
				rotated <- struct{}{}
				return "new-public-key", nil
			},
			Heartbeat: HeartbeatPayload{HeartbeatIntervalSeconds: 5},
			Now:       func() time.Time { return issuedAt },
		})
	}()

	select {
	case <-rotated:
	case <-ctx.Done():
		require.Fail(t, "rotate identity callback should run before timeout")
	}
	select {
	case response := <-serverSawResponse:
		require.Equal(t, GatewayMessageActionResponse, response.Type, "rotate control action should produce an action response")
		require.Equal(t, requestID, response.RequestID, "rotate response should preserve request id")
		require.Nil(t, response.Error, "authorized rotate control action should not return an error")
		require.NotNil(t, response.Result, "rotate control action should return a payload")
		require.JSONEq(t, `{"public_key":"new-public-key"}`, string(response.Result.Payload), "rotate response should include new public key")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive rotate response before timeout")
	}
	cancel()
	select {
	case err := <-errCh:
		require.True(t, errors.Is(err, context.Canceled) || err == nil, "RunGatewaySession should stop cleanly after rotation test: %v", err)
	case <-time.After(time.Second):
		require.Fail(t, "RunGatewaySession should exit after context cancellation")
	}
}

func TestRunGatewaySessionHandlesUpdateControlAction(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	gatewayPublicKey, gatewayPrivateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test gateway key should be generated")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	requestID := uuid.New()
	issuedAt := time.Date(2026, 6, 20, 13, 30, 0, 0, time.UTC)
	updated := make(chan struct{}, 1)
	serverSawResponse := make(chan GatewayMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, acceptErr, "test gateway should accept websocket")
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		req := ActionRequest{
			OrgID:       orgID,
			ConnectorID: groupID,
			ResourceID:  ConnectorControlResourceID,
			Capability:  CapabilityTriggerUpdate,
			RequestID:   requestID,
			IssuedAt:    issuedAt,
			ExpiresAt:   issuedAt.Add(time.Minute),
			Params:      json.RawMessage(`{"instance_id":"` + instanceID.String() + `"}`),
		}
		signature, signErr := SignActionRequest(gatewayPrivateKey, req)
		require.NoError(t, signErr, "test update request should sign")
		require.NoError(t, wsjson.Write(r.Context(), conn, GatewayMessage{
			Type:      GatewayMessageActionRequest,
			RequestID: requestID,
			Request:   &req,
			Signature: signature,
		}), "test gateway should send update action")
		var response GatewayMessage
		require.NoError(t, wsjson.Read(r.Context(), conn, &response), "test gateway should receive update response")
		serverSawResponse <- response
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.RunGatewaySession(ctx, identity, ConnectorState{
			InstanceID:       instanceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		}, NewProviderRegistry(), GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			UpdateFunc: func(context.Context) (UpdateResult, error) {
				updated <- struct{}{}
				return UpdateResult{Started: true, Message: "update command started"}, nil
			},
			Heartbeat: HeartbeatPayload{HeartbeatIntervalSeconds: 5},
			Now:       func() time.Time { return issuedAt },
		})
	}()

	select {
	case <-updated:
	case <-ctx.Done():
		require.Fail(t, "update callback should run before timeout")
	}
	select {
	case response := <-serverSawResponse:
		require.Equal(t, GatewayMessageActionResponse, response.Type, "update control action should produce an action response")
		require.Equal(t, requestID, response.RequestID, "update response should preserve request id")
		require.Nil(t, response.Error, "authorized update control action should not return an error")
		require.NotNil(t, response.Result, "update control action should return a payload")
		require.JSONEq(t, `{"started":true,"message":"update command started"}`, string(response.Result.Payload), "update response should include update result")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive update response before timeout")
	}
	cancel()
	select {
	case err := <-errCh:
		require.True(t, errors.Is(err, context.Canceled) || err == nil, "RunGatewaySession should stop cleanly after update test: %v", err)
	case <-time.After(time.Second):
		require.Fail(t, "RunGatewaySession should exit after context cancellation")
	}
}

func TestRunGatewaySessionAppliesSignedConfigPush(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	gatewayPublicKey, gatewayPrivateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test gateway key should be generated")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	resourceID := uuid.New()
	frame := ConfigPushFrame{
		OrgID:       orgID,
		ConnectorID: groupID,
		Version:     3,
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
		Resources: []ConfigPushResource{{
			ID:            resourceID,
			DisplayName:   "Production logs",
			ResourceType:  "victorialogs",
			Mode:          "logs",
			ConfigSource:  "ui",
			ConfigVersion: 3,
			Config:        json.RawMessage(`{"base_url":"http://victorialogs:9428"}`),
		}},
	}
	signature, err := SignConfigPush(gatewayPrivateKey, frame)
	require.NoError(t, err, "test config push should sign")
	handler := &testConfigPushHandler{result: ConfigApplyResult{
		Version:      3,
		Capabilities: []string{"victorialogs.query"},
	}}
	serverSawAck := make(chan GatewayMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, acceptErr, "test gateway should accept websocket")
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		require.NoError(t, wsjson.Write(r.Context(), conn, GatewayMessage{
			Type:      GatewayMessageConfigPush,
			Config:    &frame,
			Signature: signature,
		}), "test gateway should send signed config")
		var ack GatewayMessage
		require.NoError(t, wsjson.Read(r.Context(), conn, &ack), "test gateway should receive config ack")
		serverSawAck <- ack
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.RunGatewaySession(ctx, identity, ConnectorState{
			InstanceID:       instanceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		}, NewProviderRegistry(), GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			ConfigHandler:    handler,
			Now:              func() time.Time { return now },
		})
	}()

	select {
	case ack := <-serverSawAck:
		require.Equal(t, GatewayMessageConfigAck, ack.Type, "RunGatewaySession should acknowledge config pushes")
		require.NotNil(t, ack.ConfigAck, "config ack should include structured status")
		require.True(t, ack.ConfigAck.Applied, "config ack should report successful application")
		require.Equal(t, int64(3), ack.ConfigAck.Version, "config ack should report accepted version")
		require.Equal(t, frame, handler.frame, "config handler should receive the verified config frame")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive config ack before timeout")
	}
	select {
	case err := <-errCh:
		require.True(t, err == nil || errors.Is(err, context.Canceled), "RunGatewaySession should stop cleanly after config test: %v", err)
	case <-time.After(time.Second):
		require.Fail(t, "RunGatewaySession should exit after websocket close")
	}
}

func TestRunGatewaySessionRejectsUnsignedConfigPush(t *testing.T) {
	t.Parallel()

	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity.key"))
	require.NoError(t, err, "test identity should be created")
	gatewayPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test gateway key should be generated")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	groupID := uuid.New()
	frame := ConfigPushFrame{
		OrgID:       orgID,
		ConnectorID: groupID,
		Version:     4,
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
	}
	handler := &testConfigPushHandler{}
	serverSawAck := make(chan GatewayMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, acceptErr, "test gateway should accept websocket")
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		require.NoError(t, wsjson.Write(r.Context(), conn, GatewayMessage{
			Type:   GatewayMessageConfigPush,
			Config: &frame,
		}), "test gateway should send unsigned config")
		var ack GatewayMessage
		require.NoError(t, wsjson.Read(r.Context(), conn, &ack), "test gateway should receive config error ack")
		serverSawAck <- ack
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.RunGatewaySession(ctx, identity, ConnectorState{
			InstanceID:       uuid.New(),
			OrgID:            orgID,
			ConnectorGroupID: groupID,
		}, NewProviderRegistry(), GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			ConfigHandler:    handler,
			Now:              func() time.Time { return now },
		})
	}()

	select {
	case ack := <-serverSawAck:
		require.Equal(t, GatewayMessageConfigAck, ack.Type, "RunGatewaySession should acknowledge rejected config pushes")
		require.NotNil(t, ack.ConfigAck, "config ack should include structured status")
		require.False(t, ack.ConfigAck.Applied, "config ack should reject unsigned config")
		require.NotNil(t, ack.Error, "config ack should include an error for rejected config")
		require.Equal(t, "config_unauthorized", ack.Error.Code, "config ack should report authorization failures distinctly")
		require.Equal(t, ConfigPushFrame{}, handler.frame, "config handler should not see unauthorized config")
	case <-ctx.Done():
		require.Fail(t, "gateway should receive config error ack before timeout")
	}
	cancel()
	select {
	case err := <-errCh:
		require.True(t, err == nil || errors.Is(err, context.Canceled), "RunGatewaySession should stop cleanly after config test: %v", err)
	case <-time.After(time.Second):
		require.Fail(t, "RunGatewaySession should exit after websocket close")
	}
}

type testConfigPushHandler struct {
	frame  ConfigPushFrame
	result ConfigApplyResult
	err    error
}

func (h *testConfigPushHandler) ApplyConfigPush(_ context.Context, frame ConfigPushFrame) (ConfigApplyResult, error) {
	h.frame = frame
	return h.result, h.err
}

type testGatewayProvider struct {
	resourceID uuid.UUID
	sawRequest ActionRequest
}

func (p *testGatewayProvider) Name() string { return "test-gateway" }

func (p *testGatewayProvider) Version() string { return "v1" }

func (p *testGatewayProvider) Capabilities() []string { return []string{"victorialogs.query"} }

func (p *testGatewayProvider) HandleAction(_ context.Context, req ActionRequest) (ActionResult, error) {
	if req.ResourceID != p.resourceID {
		return ActionResult{}, ErrResourceUnauthorized
	}
	p.sawRequest = req
	return ActionResult{Payload: json.RawMessage(`{"ok":true}`), Metadata: ActionMetadata{ResultCount: 1}}, nil
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value), "test response should encode")
}

func readTestBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err, "request body should read")
	return body
}
