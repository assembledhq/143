package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// InternalPreviewHandler handles authenticated app->worker preview RPC.
type InternalPreviewHandler struct {
	preview   *PreviewHandler
	manager   *previewsvc.Manager
	sessions  *db.SessionStore
	canceller SessionCanceller
	nodeID    string
	secret    string
	logger    zerolog.Logger
}

func NewInternalPreviewHandler(preview *PreviewHandler, manager *previewsvc.Manager, nodeID, secret string, logger zerolog.Logger) *InternalPreviewHandler {
	return &InternalPreviewHandler{
		preview: preview,
		manager: manager,
		nodeID:  nodeID,
		secret:  secret,
		logger:  logger,
	}
}

func (h *InternalPreviewHandler) SetSessionCancelRuntime(sessions *db.SessionStore, canceller SessionCanceller) {
	h.sessions = sessions
	h.canceller = canceller
}

func (h *InternalPreviewHandler) authorize(w http.ResponseWriter, r *http.Request, action string) (*auth.PreviewTokenClaims, bool) {
	tokenStr := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization token")
		return nil, false
	}
	claims, err := auth.ValidatePreviewToken(h.secret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid preview token", err)
		return nil, false
	}
	if claims.TargetNodeID != h.nodeID {
		writeError(w, r, http.StatusForbidden, "WRONG_PREVIEW_WORKER", "preview token targets a different worker")
		return nil, false
	}
	if claims.Action != action {
		writeError(w, r, http.StatusForbidden, "PREVIEW_ACTION_MISMATCH", "preview token is not valid for this action")
		return nil, false
	}
	return claims, true
}

func (h *InternalPreviewHandler) requireInspector(w http.ResponseWriter, r *http.Request) (previewsvc.PreviewInspector, bool) {
	if h.manager == nil || h.manager.Inspector() == nil {
		writeError(w, r, http.StatusNotImplemented, "PREVIEW_INSPECTOR_NOT_AVAILABLE", "preview inspector is not configured on this worker")
		return nil, false
	}
	return h.manager.Inspector(), true
}

func (h *InternalPreviewHandler) StartPreview(w http.ResponseWriter, r *http.Request) {
	// startPreviewLocal can run snapshot restore + infra image pull +
	// readiness probes. The server's 15s WriteTimeout will otherwise drop
	// the connection mid-handler — the API caller then sees `EOF` and
	// returns 502 PREVIEW_WORKER_REQUEST_FAILED to the browser, hiding
	// the real error code (e.g. SANDBOX_BUSY). See helpers.go.
	clearWriteDeadline(w, r)

	claims, ok := h.authorize(w, r, "start")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())

	var body previewsvc.RemoteStartPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if body.OrgID != orgID {
		writeError(w, r, http.StatusForbidden, "ORG_MISMATCH", "preview token org does not match request")
		return
	}
	if claims.SessionID == nil || *claims.SessionID != body.SessionID {
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "preview token does not match the requested session")
		return
	}

	instance, previewErr := h.preview.startPreviewLocal(r.Context(), orgID, body.UserID, body.SessionID, startPreviewRequest{
		Config:        body.Config,
		BaseCommitSHA: body.BaseCommitSHA,
		ProfileName:   body.ProfileName,
	})
	if previewErr != nil {
		writePreviewHTTPError(w, r, previewErr)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[*models.PreviewInstance]{Data: instance})
}

func (h *InternalPreviewHandler) StopPreview(w http.ResponseWriter, r *http.Request) {
	previewID, err := uuid.Parse(chi.URLParam(r, "previewID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_ID", "invalid preview id")
		return
	}
	claims, ok := h.authorize(w, r, "stop")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())
	if claims.PreviewID == nil || *claims.PreviewID != previewID {
		writeError(w, r, http.StatusForbidden, "PREVIEW_MISMATCH", "preview token does not match the requested preview")
		return
	}
	if err := h.manager.StopPreview(r.Context(), orgID, previewID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}})
}

func (h *InternalPreviewHandler) RecyclePreview(w http.ResponseWriter, r *http.Request) {
	// Recycle = teardown + relaunch (image pulls + readiness probes); same
	// WriteTimeout-overrun risk as StartPreview.
	clearWriteDeadline(w, r)

	previewID, err := uuid.Parse(chi.URLParam(r, "previewID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_ID", "invalid preview id")
		return
	}
	claims, ok := h.authorize(w, r, "recycle")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())
	if claims.PreviewID == nil || *claims.PreviewID != previewID {
		writeError(w, r, http.StatusForbidden, "PREVIEW_MISMATCH", "preview token does not match the requested preview")
		return
	}
	var body previewsvc.RemoteRecyclePreviewRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
			return
		}
	}
	if previewErr := h.preview.recyclePreviewByID(r.Context(), orgID, previewID, startPreviewRequest{Config: body.Config}); previewErr != nil {
		writePreviewHTTPError(w, r, previewErr)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}})
}

func (h *InternalPreviewHandler) StopActivePreviewForSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r, "stop_session")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())
	var body previewsvc.RemoteStopActivePreviewForSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if body.OrgID != orgID {
		writeError(w, r, http.StatusForbidden, "ORG_MISMATCH", "preview token org does not match request")
		return
	}
	if claims.SessionID == nil || *claims.SessionID != body.SessionID {
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "preview token does not match the requested session")
		return
	}
	stopped, err := h.manager.StopActivePreviewForSession(r.Context(), orgID, body.SessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_STOP_FAILED", "failed to stop active preview for session", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[previewsvc.RemoteStopActivePreviewForSessionResponse]{
		Data: previewsvc.RemoteStopActivePreviewForSessionResponse{Stopped: stopped},
	})
}

func (h *InternalPreviewHandler) CancelSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r, "cancel_session")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())

	sessionID, err := uuid.Parse(chi.URLParam(r, "sessionID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session id")
		return
	}
	var body previewsvc.RemoteCancelSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if body.OrgID != orgID {
		writeError(w, r, http.StatusForbidden, "ORG_MISMATCH", "cancel token org does not match request")
		return
	}
	if claims.SessionID == nil || *claims.SessionID != sessionID || body.SessionID != sessionID {
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "cancel token does not match the requested session")
		return
	}
	if h.canceller == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CANCEL_UNAVAILABLE", "session cancellation is not available")
		return
	}

	accepted := h.canceller.CancelSession(sessionID)
	if accepted && h.sessions != nil {
		if _, err := consumeSessionCancelRequestDetached(r.Context(), h.sessions, orgID, sessionID); err != nil {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to consume delivered session cancel request")
		}
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[previewsvc.RemoteCancelSessionResponse]{
		Data: previewsvc.RemoteCancelSessionResponse{Accepted: accepted},
	})
}

func (h *InternalPreviewHandler) CaptureScreenshot(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "screenshot")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var opts models.ScreenshotOpts
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.CaptureScreenshot(r.Context(), previewID.String(), opts)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SCREENSHOT_FAILED", "failed to capture screenshot", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.ScreenshotResult]{Data: result})
}

func (h *InternalPreviewHandler) InspectElement(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "inspect")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var body previewsvc.RemoteInspectElementRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.InspectElement(r.Context(), previewID.String(), body.X, body.Y)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INSPECT_FAILED", "failed to inspect element", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.ElementInfo]{Data: result})
}

func (h *InternalPreviewHandler) ReadConsole(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "console")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	result, err := inspector.ReadConsole(r.Context(), previewID.String())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONSOLE_READ_FAILED", "failed to read console messages", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[previewsvc.ConsoleMessage]{Data: result})
}

func (h *InternalPreviewHandler) ExecuteInteraction(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "interact")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var body previewsvc.RemoteExecuteInteractionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.ExecuteInteraction(r.Context(), previewID.String(), body.Steps)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERACTION_FAILED", "failed to execute interaction", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.InteractionResult]{Data: result})
}

func (h *InternalPreviewHandler) CaptureMultiViewport(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "multi_viewport")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var body models.MultiViewportOpts
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.CaptureMultiViewport(r.Context(), previewID.String(), body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MULTI_VIEWPORT_FAILED", "failed to capture multi-viewport screenshots", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.MultiViewportResult]{Data: result})
}

func (h *InternalPreviewHandler) ComputeVisualDiff(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "visual_diff")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var body previewsvc.RemoteComputeVisualDiffRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.ComputeVisualDiff(r.Context(), previewID.String(), body.BeforeSnapshotID, body.AfterSnapshotID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "VISUAL_DIFF_FAILED", "failed to compute visual diff", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.VisualDiff]{Data: result})
}

func (h *InternalPreviewHandler) RunAssertions(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "assert")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	_ = middleware.OrgIDFromContext(r.Context())
	inspector, ok := h.requireInspector(w, r)
	if !ok {
		return
	}
	var body previewsvc.RemoteRunAssertionsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	result, err := inspector.RunAssertions(r.Context(), previewID.String(), body.Assertions)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ASSERTIONS_FAILED", "failed to run assertions", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*previewsvc.AssertionResult]{Data: result})
}

func (h *InternalPreviewHandler) authorizePreviewAction(w http.ResponseWriter, r *http.Request, action string) (uuid.UUID, *auth.PreviewTokenClaims, bool) {
	previewID, err := uuid.Parse(chi.URLParam(r, "previewID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_ID", "invalid preview id")
		return uuid.Nil, nil, false
	}
	claims, ok := h.authorize(w, r, action)
	if !ok {
		return uuid.Nil, nil, false
	}
	if claims.PreviewID == nil || *claims.PreviewID != previewID {
		writeError(w, r, http.StatusForbidden, "PREVIEW_MISMATCH", "preview token does not match the requested preview")
		return uuid.Nil, nil, false
	}
	if action == "proxy" && (claims.RuntimeID == nil || claims.RuntimeEpoch <= 0) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_RUNTIME_MISMATCH", "preview token does not match an active runtime")
		return uuid.Nil, nil, false
	}
	return previewID, claims, true
}

func (h *InternalPreviewHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "proxy")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())

	if isWebSocketUpgrade(r) {
		h.handleWebSocketProxy(w, r, orgID, previewID)
		return
	}
	h.handleHTTPProxy(w, r, orgID, previewID)
}

func (h *InternalPreviewHandler) handleHTTPProxy(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "preview-target"
			req.URL.Path = trimInternalPreviewProxyPath(req.URL.Path, previewID)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
			req.RequestURI = ""
			req.Host = "preview-target"
			req.Header.Del("Authorization")
			stripPreviewAccessCookies(req)
		},
		Transport: &internalPreviewTransport{
			manager:   h.manager,
			orgID:     orgID,
			previewID: previewID,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("internal preview proxy error")
			http.Error(w, "preview unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (h *InternalPreviewHandler) handleWebSocketProxy(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
	backendConn, err := h.manager.DialPreview(r.Context(), orgID, previewID)
	if err != nil {
		h.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("internal websocket dial failed")
		http.Error(w, "preview unavailable", http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		h.logger.Warn().Err(err).Msg("internal websocket hijack failed")
		return
	}
	defer clientConn.Close()

	backendReq := cloneWebSocketRequestForInternalProxy(r, previewID)
	if err := backendReq.Write(backendConn); err != nil {
		h.logger.Warn().Err(err).Msg("internal websocket: failed to forward upgrade request")
		return
	}
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		_, _ = clientBuf.Read(buffered)
		_, _ = backendConn.Write(buffered)
	}

	done := make(chan struct{})
	go func() {
		if watcher := h.manager.HMRWatcher(); watcher != nil {
			copyWithHMRSnoopToClient(h.logger, watcher, clientConn, backendConn, previewID)
		} else if _, copyErr := io.Copy(clientConn, backendConn); copyErr != nil {
			h.logger.Debug().Err(copyErr).Str("preview_id", previewID.String()).Msg("internal websocket backend->client copy ended")
		}
		close(done)
	}()
	if _, err := io.Copy(backendConn, clientConn); err != nil {
		h.logger.Debug().Err(err).Str("preview_id", previewID.String()).Msg("internal websocket client->backend copy ended")
	}
	<-done
}

type internalPreviewTransport struct {
	manager   *previewsvc.Manager
	orgID     uuid.UUID
	previewID uuid.UUID
}

type connClosingBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connClosingBody) Close() error {
	bodyErr := b.ReadCloser.Close()
	connErr := b.conn.Close()
	if bodyErr != nil {
		return bodyErr
	}
	return connErr
}

func (t *internalPreviewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	conn, err := t.manager.DialPreview(req.Context(), t.orgID, t.previewID)
	if err != nil {
		return nil, fmt.Errorf("dial preview: %w", err)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read response: %w", err)
	}
	resp.Body = &connClosingBody{ReadCloser: resp.Body, conn: conn}
	return resp, nil
}

func cloneWebSocketRequestForInternalProxy(req *http.Request, previewID uuid.UUID) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.URL.Path = trimInternalPreviewProxyPath(cloned.URL.Path, previewID)
	if cloned.URL.Path == "" {
		cloned.URL.Path = "/"
	}
	cloned.RequestURI = ""
	cloned.Header.Del("Authorization")
	stripPreviewAccessCookies(cloned)
	return cloned
}

func stripPreviewAccessCookies(req *http.Request) {
	cookies := req.Cookies()
	req.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != "__Host-preview_session" && c.Name != "preview_session" {
			req.AddCookie(c)
		}
	}
}

func trimInternalPreviewProxyPath(path string, previewID uuid.UUID) string {
	prefix := fmt.Sprintf("/internal/preview/%s/proxy", previewID.String())
	if strings.HasPrefix(path, prefix) {
		trimmed := strings.TrimPrefix(path, prefix)
		if trimmed == "" {
			return "/"
		}
		return trimmed
	}
	return path
}

func copyWithHMRSnoopToClient(logger zerolog.Logger, watcher *previewsvc.HMRWatcher, dst io.Writer, src io.Reader, previewID uuid.UUID) {
	buf := make([]byte, 32*1024)
	var overlap []byte
	const overlapSize = 512
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			data := buf[:n]
			combined := data
			if len(overlap) > 0 {
				combined = append(overlap, data...)
			}
			watcher.OnWebSocketMessage(previewID, combined)
			if len(data) >= overlapSize {
				overlap = make([]byte, overlapSize)
				copy(overlap, data[len(data)-overlapSize:])
			} else {
				overlap = make([]byte, len(data))
				copy(overlap, data)
			}
			if _, writeErr := dst.Write(data); writeErr != nil {
				logger.Debug().Err(writeErr).Str("preview_id", previewID.String()).Msg("internal websocket backend->client write ended")
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				logger.Debug().Err(readErr).Str("preview_id", previewID.String()).Msg("internal websocket backend->client read ended")
			}
			return
		}
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
