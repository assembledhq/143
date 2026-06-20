package privateconnector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

var (
	ErrConnectorUnavailable  = errors.New("private connector unavailable")
	ErrActionDispatchTimeout = errors.New("private connector action dispatch timed out")
)

type GatewayConfig struct {
	DispatchTimeout   time.Duration
	HeartbeatRecorder HeartbeatRecorder
	ConfigProvider    ConfigProvider
}

type HeartbeatRecorder interface {
	RecordSessionHeartbeat(ctx context.Context, instance models.PrivateConnectorInstance, frame connector.HeartbeatFrame) error
	RecordSessionDisconnect(ctx context.Context, instance models.PrivateConnectorInstance) error
}

type ConfigProvider interface {
	ConnectorConfigPush(ctx context.Context, instance models.PrivateConnectorInstance) (connector.ConfigPushFrame, string, error)
}

type Gateway struct {
	logger          zerolog.Logger
	dispatchTimeout time.Duration
	heartbeat       HeartbeatRecorder
	configProvider  ConfigProvider

	mu       sync.Mutex
	sessions map[uuid.UUID]map[uuid.UUID]*gatewaySession
	next     map[uuid.UUID]int
}

func NewGateway(logger zerolog.Logger, cfg GatewayConfig) *Gateway {
	timeout := cfg.DispatchTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Gateway{
		logger:          logger,
		dispatchTimeout: timeout,
		heartbeat:       cfg.HeartbeatRecorder,
		configProvider:  cfg.ConfigProvider,
		sessions:        make(map[uuid.UUID]map[uuid.UUID]*gatewaySession),
		next:            make(map[uuid.UUID]int),
	}
}

func (g *Gateway) SetHeartbeatRecorder(recorder HeartbeatRecorder) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heartbeat = recorder
}

func (g *Gateway) SetConfigProvider(provider ConfigProvider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.configProvider = provider
}

func (g *Gateway) ActiveSessionCount(connectorGroupID uuid.UUID) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sessions[connectorGroupID])
}

func (g *Gateway) ServeSession(ctx context.Context, conn *websocket.Conn, instance models.PrivateConnectorInstance) error {
	session := newGatewaySession(g, conn, instance)
	g.addSession(session)
	defer g.removeSession(ctx, session)
	session.pushConfig(ctx)
	return session.readLoop(ctx)
}

func (g *Gateway) DispatchPrivateConnectorAction(ctx context.Context, req connector.ActionRequest, signature string) (connector.ActionResult, error) {
	session := g.pickSession(req.ConnectorID, req.Capability)
	if session == nil {
		return connector.ActionResult{}, ErrConnectorUnavailable
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, g.dispatchTimeout)
	defer cancel()
	result, err := session.dispatch(dispatchCtx, req, signature)
	if errors.Is(err, context.DeadlineExceeded) {
		return connector.ActionResult{}, ErrActionDispatchTimeout
	}
	if err != nil {
		return connector.ActionResult{}, err
	}
	return result, nil
}

func (g *Gateway) addSession(session *gatewaySession) {
	g.mu.Lock()
	defer g.mu.Unlock()
	groupID := session.instance.ConnectorGroupID
	if g.sessions[groupID] == nil {
		g.sessions[groupID] = make(map[uuid.UUID]*gatewaySession)
	}
	if old := g.sessions[groupID][session.instance.ID]; old != nil {
		old.close(websocket.StatusPolicyViolation, "replaced by a newer connector session")
	}
	g.sessions[groupID][session.instance.ID] = session
}

func (g *Gateway) removeSession(ctx context.Context, session *gatewaySession) {
	g.mu.Lock()
	groupID := session.instance.ConnectorGroupID
	if current := g.sessions[groupID][session.instance.ID]; current != session {
		g.mu.Unlock()
		return
	}
	delete(g.sessions[groupID], session.instance.ID)
	if len(g.sessions[groupID]) == 0 {
		delete(g.sessions, groupID)
		delete(g.next, groupID)
	}
	recorder := g.heartbeat
	g.mu.Unlock()
	if recorder != nil {
		if err := recorder.RecordSessionDisconnect(ctx, session.instance); err != nil {
			g.logger.Warn().
				Err(err).
				Str("connector_instance_id", session.instance.ID.String()).
				Msg("failed to mark private connector session reconnecting")
		}
	}
	session.failPending(ErrConnectorUnavailable)
}

func (g *Gateway) pickSession(groupID uuid.UUID, capability string) *gatewaySession {
	g.mu.Lock()
	defer g.mu.Unlock()
	byInstance := g.sessions[groupID]
	if len(byInstance) == 0 {
		return nil
	}
	candidates := make([]*gatewaySession, 0, len(byInstance))
	for _, session := range byInstance {
		if session.supports(capability) {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	idx := g.next[groupID] % len(candidates)
	g.next[groupID] = (g.next[groupID] + 1) % len(candidates)
	return candidates[idx]
}

type gatewaySession struct {
	gateway  *Gateway
	conn     *websocket.Conn
	instance models.PrivateConnectorInstance

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uuid.UUID]chan gatewayActionResponse
}

type gatewayActionResponse struct {
	result connector.ActionResult
	err    error
}

func newGatewaySession(gateway *Gateway, conn *websocket.Conn, instance models.PrivateConnectorInstance) *gatewaySession {
	return &gatewaySession{
		gateway:  gateway,
		conn:     conn,
		instance: instance,
		pending:  make(map[uuid.UUID]chan gatewayActionResponse),
	}
}

func (s *gatewaySession) supports(capability string) bool {
	for _, supported := range s.instance.Capabilities {
		if supported == capability {
			return true
		}
	}
	return false
}

func (s *gatewaySession) dispatch(ctx context.Context, req connector.ActionRequest, signature string) (connector.ActionResult, error) {
	ch := make(chan gatewayActionResponse, 1)
	s.mu.Lock()
	s.pending[req.RequestID] = ch
	s.mu.Unlock()
	defer s.forget(req.RequestID)

	msg := connector.GatewayMessage{
		Type:      connector.GatewayMessageActionRequest,
		RequestID: req.RequestID,
		Request:   &req,
		Signature: signature,
	}
	s.writeMu.Lock()
	err := wsjson.Write(ctx, s.conn, msg)
	s.writeMu.Unlock()
	if err != nil {
		return connector.ActionResult{}, fmt.Errorf("write private connector action request: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.result, resp.err
	case <-ctx.Done():
		return connector.ActionResult{}, ctx.Err()
	}
}

func (s *gatewaySession) readLoop(ctx context.Context) error {
	for {
		var msg connector.GatewayMessage
		if err := wsjson.Read(ctx, s.conn, &msg); err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch msg.Type {
		case connector.GatewayMessageActionResponse:
			s.complete(msg)
		case connector.GatewayMessageHeartbeat:
			s.recordHeartbeat(ctx, msg)
		case connector.GatewayMessageConfigAck:
			s.recordConfigAck(msg)
		default:
			s.gateway.logger.Warn().
				Str("type", msg.Type).
				Str("connector_instance_id", s.instance.ID.String()).
				Msg("private connector gateway ignored unsupported message")
		}
	}
}

func (s *gatewaySession) pushConfig(ctx context.Context) {
	s.gateway.mu.Lock()
	provider := s.gateway.configProvider
	s.gateway.mu.Unlock()
	if provider == nil {
		return
	}
	frame, signature, err := provider.ConnectorConfigPush(ctx, s.instance)
	if err != nil {
		s.gateway.logger.Warn().
			Err(err).
			Str("connector_instance_id", s.instance.ID.String()).
			Msg("failed to build private connector config push")
		return
	}
	if frame.Version <= 0 {
		return
	}
	s.writeMu.Lock()
	err = wsjson.Write(ctx, s.conn, connector.GatewayMessage{
		Type:      connector.GatewayMessageConfigPush,
		Config:    &frame,
		Signature: signature,
	})
	s.writeMu.Unlock()
	if err != nil {
		s.gateway.logger.Warn().
			Err(err).
			Str("connector_instance_id", s.instance.ID.String()).
			Msg("failed to send private connector config push")
	}
}

func (s *gatewaySession) recordConfigAck(msg connector.GatewayMessage) {
	if msg.ConfigAck == nil {
		return
	}
	event := s.gateway.logger.Info()
	if !msg.ConfigAck.Applied {
		event = s.gateway.logger.Warn()
	}
	if msg.Error != nil {
		event = event.Str("error_code", msg.Error.Code).Str("error_message", msg.Error.Message)
	}
	event.
		Int64("config_version", msg.ConfigAck.Version).
		Bool("applied", msg.ConfigAck.Applied).
		Str("connector_instance_id", s.instance.ID.String()).
		Msg("private connector config push acknowledged")
}

func (s *gatewaySession) recordHeartbeat(ctx context.Context, msg connector.GatewayMessage) {
	if msg.Heartbeat == nil {
		return
	}
	s.gateway.mu.Lock()
	recorder := s.gateway.heartbeat
	s.gateway.mu.Unlock()
	if recorder == nil {
		return
	}
	if err := recorder.RecordSessionHeartbeat(ctx, s.instance, *msg.Heartbeat); err != nil {
		s.gateway.logger.Warn().
			Err(err).
			Str("connector_instance_id", s.instance.ID.String()).
			Msg("failed to persist private connector websocket heartbeat")
	}
}

func (s *gatewaySession) complete(msg connector.GatewayMessage) {
	s.mu.Lock()
	ch := s.pending[msg.RequestID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	if msg.Error != nil {
		ch <- gatewayActionResponse{err: errors.New(msg.Error.Message)}
		return
	}
	if msg.Result == nil {
		ch <- gatewayActionResponse{err: errors.New("private connector returned empty action result")}
		return
	}
	ch <- gatewayActionResponse{result: *msg.Result}
}

func (s *gatewaySession) forget(requestID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, requestID)
}

func (s *gatewaySession) failPending(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for requestID, ch := range s.pending {
		ch <- gatewayActionResponse{err: err}
		delete(s.pending, requestID)
	}
}

func (s *gatewaySession) close(status websocket.StatusCode, reason string) {
	_ = s.conn.Close(status, reason)
}
