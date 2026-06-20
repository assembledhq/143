package privateconnector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type fakeGatewayHeartbeatRecorder struct {
	instance           models.PrivateConnectorInstance
	frame              connector.HeartbeatFrame
	disconnectInstance models.PrivateConnectorInstance
	err                error
	called             chan struct{}
}

func (r *fakeGatewayHeartbeatRecorder) RecordSessionHeartbeat(_ context.Context, instance models.PrivateConnectorInstance, frame connector.HeartbeatFrame) error {
	r.instance = instance
	r.frame = frame
	if r.called != nil {
		close(r.called)
	}
	return r.err
}

func (r *fakeGatewayHeartbeatRecorder) RecordSessionDisconnect(_ context.Context, instance models.PrivateConnectorInstance) error {
	r.disconnectInstance = instance
	return r.err
}

type fakeGatewayConfigProvider struct {
	instance  models.PrivateConnectorInstance
	frame     connector.ConfigPushFrame
	signature string
	err       error
	called    chan struct{}
}

func (p *fakeGatewayConfigProvider) ConnectorConfigPush(_ context.Context, instance models.PrivateConnectorInstance) (connector.ConfigPushFrame, string, error) {
	p.instance = instance
	if p.called != nil {
		close(p.called)
	}
	return p.frame, p.signature, p.err
}

func TestGatewayDispatchesActionsOverWebSocketSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	resourceID := uuid.New()
	gateway := NewGateway(zerolog.Nop(), GatewayConfig{DispatchTimeout: time.Second})
	instance := models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
		Capabilities:     []string{"victorialogs.query"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, err, "test websocket should accept")
		err = gateway.ServeSession(r.Context(), conn, instance)
		require.NoError(t, err, "gateway session should close cleanly")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err, "connector websocket should dial")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		var msg connector.GatewayMessage
		require.NoError(t, wsjson.Read(ctx, conn, &msg), "connector should read action request")
		require.Equal(t, connector.GatewayMessageActionRequest, msg.Type, "gateway should send an action request")
		require.Equal(t, "victorialogs.query", msg.Request.Capability, "gateway should preserve action capability")
		require.Equal(t, json.RawMessage(`{"query":"service:api"}`), msg.Request.Params, "gateway should preserve action params")
		require.Equal(t, "signed-by-143", msg.Signature, "gateway should forward the server signature")
		err := wsjson.Write(ctx, conn, connector.GatewayMessage{
			Type:      connector.GatewayMessageActionResponse,
			RequestID: msg.Request.RequestID,
			Result: &connector.ActionResult{
				Payload:  json.RawMessage(`{"entries":[]}`),
				Metadata: connector.ActionMetadata{ResultCount: 3, DurationMs: 12},
			},
		})
		require.NoError(t, err, "connector should write action response")
	}()
	require.Eventually(t, func() bool {
		return gateway.ActiveSessionCount(groupID) == 1
	}, time.Second, 10*time.Millisecond, "gateway should register the active connector session")

	result, err := gateway.DispatchPrivateConnectorAction(ctx, connector.ActionRequest{
		OrgID:       orgID,
		ConnectorID: groupID,
		ResourceID:  resourceID,
		Capability:  "victorialogs.query",
		RequestID:   uuid.New(),
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(30 * time.Second),
		Params:      json.RawMessage(`{"query":"service:api"}`),
	}, "signed-by-143")
	require.NoError(t, err, "gateway should dispatch through an active connector session")
	require.Equal(t, 3, result.Metadata.ResultCount, "gateway should return connector result metadata")
	require.JSONEq(t, `{"entries":[]}`, string(result.Payload), "gateway should return connector result payload")
	<-responseDone

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, "closed"), "test connector should close websocket")
	require.Eventually(t, func() bool {
		return gateway.ActiveSessionCount(groupID) == 0
	}, time.Second, 10*time.Millisecond, "gateway should remove closed connector sessions")
}

func TestGatewayRecordsWebSocketHeartbeatFrames(t *testing.T) {
	t.Parallel()

	groupID := uuid.New()
	instanceID := uuid.New()
	recorder := &fakeGatewayHeartbeatRecorder{called: make(chan struct{})}
	gateway := NewGateway(zerolog.Nop(), GatewayConfig{DispatchTimeout: time.Second, HeartbeatRecorder: recorder})
	instance := models.PrivateConnectorInstance{
		ID:               instanceID,
		ConnectorGroupID: groupID,
		Capabilities:     []string{"victorialogs.query"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, err, "test websocket should accept")
		err = gateway.ServeSession(r.Context(), conn, instance)
		require.NoError(t, err, "gateway session should close cleanly")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err, "connector websocket should dial")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	err = wsjson.Write(ctx, conn, connector.GatewayMessage{
		Type: connector.GatewayMessageHeartbeat,
		Heartbeat: &connector.HeartbeatFrame{
			Version:                  "v0.1.2",
			Protocol:                 "websocket",
			Capabilities:             []string{"victorialogs.query"},
			HeartbeatIntervalSeconds: 5,
		},
	})
	require.NoError(t, err, "connector should send heartbeat frame")

	select {
	case <-recorder.called:
	case <-ctx.Done():
		require.Fail(t, "gateway should record websocket heartbeat before timeout")
	}
	require.Equal(t, instanceID, recorder.instance.ID, "heartbeat recorder should receive the session instance")
	require.Equal(t, "v0.1.2", recorder.frame.Version, "heartbeat recorder should receive heartbeat frame metadata")
	require.Equal(t, []string{"victorialogs.query"}, recorder.frame.Capabilities, "heartbeat recorder should receive advertised capabilities")
}

func TestGatewayPushesConnectorConfigOnSessionStart(t *testing.T) {
	t.Parallel()

	groupID := uuid.New()
	instanceID := uuid.New()
	resourceID := uuid.New()
	provider := &fakeGatewayConfigProvider{
		called: make(chan struct{}),
		frame: connector.ConfigPushFrame{
			OrgID:       uuid.New(),
			ConnectorID: groupID,
			Version:     2,
			Resources: []connector.ConfigPushResource{{
				ID:            resourceID,
				DisplayName:   "Production logs",
				ResourceType:  "victorialogs",
				ConfigVersion: 2,
			}},
		},
		signature: "signed-config",
	}
	gateway := NewGateway(zerolog.Nop(), GatewayConfig{DispatchTimeout: time.Second, ConfigProvider: provider})
	instance := models.PrivateConnectorInstance{
		ID:               instanceID,
		ConnectorGroupID: groupID,
		Capabilities:     []string{"victorialogs.query"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, err, "test websocket should accept")
		err = gateway.ServeSession(r.Context(), conn, instance)
		require.NoError(t, err, "gateway session should close cleanly")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err, "connector websocket should dial")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	var msg connector.GatewayMessage
	require.NoError(t, wsjson.Read(ctx, conn, &msg), "connector should receive config push")

	require.Equal(t, connector.GatewayMessageConfigPush, msg.Type, "gateway should send config push after session registration")
	require.Equal(t, "signed-config", msg.Signature, "gateway should include config authorization signature")
	require.NotNil(t, msg.Config, "config push should include typed config payload")
	require.Equal(t, resourceID, msg.Config.Resources[0].ID, "config push should include configured private resource")
	require.Equal(t, instanceID, provider.instance.ID, "config provider should receive the connected instance")
}

func TestGatewayHeartbeatRecorderErrorsDoNotCloseSession(t *testing.T) {
	t.Parallel()

	groupID := uuid.New()
	recorder := &fakeGatewayHeartbeatRecorder{called: make(chan struct{}), err: errors.New("db unavailable")}
	gateway := NewGateway(zerolog.Nop(), GatewayConfig{DispatchTimeout: time.Second, HeartbeatRecorder: recorder})
	instance := models.PrivateConnectorInstance{
		ID:               uuid.New(),
		ConnectorGroupID: groupID,
		Capabilities:     []string{"victorialogs.query"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, err, "test websocket should accept")
		err = gateway.ServeSession(r.Context(), conn, instance)
		require.NoError(t, err, "gateway session should close cleanly")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err, "connector websocket should dial")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	err = wsjson.Write(ctx, conn, connector.GatewayMessage{Type: connector.GatewayMessageHeartbeat, Heartbeat: &connector.HeartbeatFrame{Version: "v0.1.2"}})
	require.NoError(t, err, "connector should send heartbeat frame")

	select {
	case <-recorder.called:
	case <-ctx.Done():
		require.Fail(t, "gateway should call heartbeat recorder before timeout")
	}
	require.Eventually(t, func() bool {
		return gateway.ActiveSessionCount(groupID) == 1
	}, time.Second, 10*time.Millisecond, "heartbeat persistence errors should not drop the active session")
}

func TestGatewayRejectsDispatchWithoutHealthySession(t *testing.T) {
	t.Parallel()

	gateway := NewGateway(zerolog.Nop(), GatewayConfig{DispatchTimeout: time.Second})
	_, err := gateway.DispatchPrivateConnectorAction(context.Background(), connector.ActionRequest{
		OrgID:       uuid.New(),
		ConnectorID: uuid.New(),
		ResourceID:  uuid.New(),
		Capability:  "victorialogs.query",
		RequestID:   uuid.New(),
	}, "signed-by-143")
	require.ErrorIs(t, err, ErrConnectorUnavailable, "gateway should reject dispatch without a live connector session")
}
