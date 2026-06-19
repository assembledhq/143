package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// runtimeAuthEntry caches the active runtime identity used to validate proxy
// token claims, so the per-request DB check in authorizePreviewAction only
// fires once per TTL per preview.
type runtimeAuthEntry struct {
	validUntil   time.Time
	runtimeID    uuid.UUID
	runtimeEpoch int
	workerNodeID string
}

// runtimeAuthCacheTTL bounds how long a cached runtime identity is trusted.
const runtimeAuthCacheTTL = 5 * time.Second

// InternalPreviewHandler handles authenticated app->worker preview RPC.
type InternalPreviewHandler struct {
	preview   *PreviewHandler
	manager   *previewsvc.Manager
	sessions  *db.SessionStore
	canceller SessionCanceller
	nodeID    string
	keyring   auth.PreviewTokenKeyring
	logger    zerolog.Logger

	// runtimeAuthCache short-circuits the per-request active-runtime check.
	runtimeAuthMu    sync.RWMutex
	runtimeAuthCache map[uuid.UUID]*runtimeAuthEntry

	// previewTransports pools upstream connections into preview containers,
	// keyed by preview ID. Without pooling, every proxied request resolved
	// the preview from the DB and dialed a fresh container connection.
	transportMu       sync.Mutex
	previewTransports map[uuid.UUID]*http.Transport
}

func NewInternalPreviewHandler(preview *PreviewHandler, manager *previewsvc.Manager, nodeID, secret string, logger zerolog.Logger) *InternalPreviewHandler {
	keyring, err := auth.NewPreviewTokenKeyring([]string{secret})
	if err != nil {
		keyring = auth.PreviewTokenKeyring{}
	}
	return NewInternalPreviewHandlerWithKeyring(preview, manager, nodeID, keyring, logger)
}

func NewInternalPreviewHandlerWithKeyring(preview *PreviewHandler, manager *previewsvc.Manager, nodeID string, keyring auth.PreviewTokenKeyring, logger zerolog.Logger) *InternalPreviewHandler {
	return &InternalPreviewHandler{
		preview:           preview,
		manager:           manager,
		nodeID:            nodeID,
		keyring:           keyring,
		logger:            logger,
		runtimeAuthCache:  make(map[uuid.UUID]*runtimeAuthEntry),
		previewTransports: make(map[uuid.UUID]*http.Transport),
	}
}

func (h *InternalPreviewHandler) SetSessionCancelRuntime(sessions *db.SessionStore, canceller SessionCanceller) {
	h.sessions = sessions
	h.canceller = canceller
}

func (h *InternalPreviewHandler) authorize(w http.ResponseWriter, r *http.Request, action string) (*auth.PreviewTokenClaims, bool) {
	tokenStr := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tokenStr == "" {
		markPreviewWorkerError(w)
		w.Header().Set(auth.PreviewWorkerAuthDetailHeader, "missing")
		h.logPreviewAuthorizationRejected(r, "missing_token", action, nil, nil)
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization token")
		return nil, false
	}
	claims, err := h.keyring.Validate(tokenStr)
	if err != nil {
		markPreviewWorkerError(w)
		// Surface a coarse, non-sensitive failure class on the trusted
		// worker→gateway hop so the app-side logs (which ship reliably) can
		// distinguish a divergent secret (bad_signature) from clock skew
		// (expired) without leaking crypto detail to the browser.
		w.Header().Set(auth.PreviewWorkerAuthDetailHeader, auth.ClassifyPreviewTokenError(err))
		h.logPreviewAuthorizationRejected(r, "invalid_token", action, nil, err)
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid preview token", err)
		return nil, false
	}
	if claims.TargetNodeID != h.nodeID {
		markPreviewWorkerError(w)
		h.logPreviewAuthorizationRejected(r, "wrong_worker", action, claims, nil)
		writeError(w, r, http.StatusForbidden, "WRONG_PREVIEW_WORKER", "preview token targets a different worker")
		return nil, false
	}
	if claims.Action != action {
		markPreviewWorkerError(w)
		h.logPreviewAuthorizationRejected(r, "action_mismatch", action, claims, nil)
		writeError(w, r, http.StatusForbidden, "PREVIEW_ACTION_MISMATCH", "preview token is not valid for this action")
		return nil, false
	}
	return claims, true
}

func (h *InternalPreviewHandler) logPreviewAuthorizationRejected(r *http.Request, failureKind, requestedAction string, claims *auth.PreviewTokenClaims, err error) {
	event := h.logger.Warn()
	if err != nil {
		event = event.Err(err)
	}
	event = addInternalPreviewRequestLogFields(event, r).
		Str("failure_kind", failureKind).
		Str("requested_preview_action", requestedAction).
		Str("local_worker_node_id", h.nodeID)
	if claims != nil {
		event = addPreviewTokenClaimLogFields(event, claims)
	}
	event.Msg("internal preview authorization rejected")
}

func markPreviewWorkerError(w http.ResponseWriter) {
	w.Header().Set(auth.PreviewWorkerErrorHeader, "1")
}

func (h *InternalPreviewHandler) AuthCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, "auth_check"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{
		Data: map[string]string{"node_id": h.nodeID},
	})
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
	h.evictPreviewTransport(previewID)
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
	h.evictPreviewTransport(previewID)
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
		markPreviewWorkerError(w)
		h.logPreviewRuntimeAuthorizationRejected(r, "preview_mismatch", action, previewID, claims, runtimeClaimsValidationResult{})
		writeError(w, r, http.StatusForbidden, "PREVIEW_MISMATCH", "preview token does not match the requested preview")
		return uuid.Nil, nil, false
	}
	if action == "proxy" && (claims.RuntimeID == nil || claims.RuntimeEpoch <= 0) {
		markPreviewWorkerError(w)
		h.logPreviewRuntimeAuthorizationRejected(r, "missing_runtime_identity", action, previewID, claims, runtimeClaimsValidationResult{})
		writeError(w, r, http.StatusForbidden, "PREVIEW_RUNTIME_MISMATCH", "preview token does not match an active runtime")
		return uuid.Nil, nil, false
	}
	if action == "proxy" && h.preview != nil && h.preview.store != nil {
		result := h.runtimeClaimsValidation(r.Context(), claims, previewID)
		if !result.valid {
			markPreviewWorkerError(w)
			h.logPreviewRuntimeAuthorizationRejected(r, result.failureKind, action, previewID, claims, result)
			writeError(w, r, http.StatusForbidden, "PREVIEW_RUNTIME_MISMATCH", "preview token does not match an active runtime")
			return uuid.Nil, nil, false
		}
	}
	return previewID, claims, true
}

// runtimeClaimsValidation checks proxy token claims against the active runtime
// through a short TTL cache. A cached match is trusted; a miss or mismatch
// always re-resolves from the DB before rejecting, so a freshly recycled
// preview (new runtime epoch) is never penalized by a stale cache entry.
type runtimeClaimsValidationResult struct {
	valid       bool
	failureKind string
	dbErr       error
	active      *models.PreviewRuntime
	cacheHit    bool
	cached      *runtimeAuthEntry
}

func (h *InternalPreviewHandler) runtimeClaimsValidation(ctx context.Context, claims *auth.PreviewTokenClaims, previewID uuid.UUID) runtimeClaimsValidationResult {
	matches := func(entry *runtimeAuthEntry) bool {
		return entry.runtimeID == *claims.RuntimeID &&
			entry.runtimeEpoch == claims.RuntimeEpoch &&
			entry.workerNodeID == claims.TargetNodeID
	}
	now := time.Now()
	h.runtimeAuthMu.RLock()
	entry, ok := h.runtimeAuthCache[previewID]
	h.runtimeAuthMu.RUnlock()
	if ok && now.Before(entry.validUntil) && matches(entry) {
		return runtimeClaimsValidationResult{valid: true, cacheHit: true, cached: entry}
	}
	runtime, err := h.preview.store.GetActivePreviewRuntime(ctx, claims.OrgID, previewID)
	if err != nil {
		return runtimeClaimsValidationResult{failureKind: "active_runtime_lookup_failed", dbErr: err, cacheHit: ok, cached: entry}
	}
	entry = &runtimeAuthEntry{
		validUntil:   now.Add(runtimeAuthCacheTTL),
		runtimeID:    runtime.ID,
		runtimeEpoch: runtime.RuntimeEpoch,
		workerNodeID: runtime.WorkerNodeID,
	}
	h.runtimeAuthMu.Lock()
	if len(h.runtimeAuthCache) > 4096 {
		// Safety valve against unbounded growth across short-lived previews.
		h.runtimeAuthCache = make(map[uuid.UUID]*runtimeAuthEntry)
	}
	h.runtimeAuthCache[previewID] = entry
	h.runtimeAuthMu.Unlock()
	if matches(entry) {
		return runtimeClaimsValidationResult{valid: true, active: runtime}
	}
	return runtimeClaimsValidationResult{failureKind: "runtime_mismatch", active: runtime, cacheHit: ok}
}

func (h *InternalPreviewHandler) logPreviewRuntimeAuthorizationRejected(r *http.Request, failureKind, requestedAction string, previewID uuid.UUID, claims *auth.PreviewTokenClaims, result runtimeClaimsValidationResult) {
	if failureKind == "" {
		failureKind = "runtime_mismatch"
	}
	event := h.logger.Warn()
	if result.dbErr != nil {
		event = event.Err(result.dbErr)
	}
	event = addInternalPreviewRequestLogFields(event, r).
		Str("failure_kind", failureKind).
		Str("requested_preview_action", requestedAction).
		Str("preview_id", previewID.String()).
		Str("local_worker_node_id", h.nodeID).
		Bool("runtime_auth_cache_hit", result.cacheHit)
	if claims != nil {
		event = addPreviewTokenClaimLogFields(event, claims).
			Str("claimed_worker_node_id", claims.TargetNodeID)
	}
	if result.cached != nil {
		event = event.
			Str("cached_runtime_id", result.cached.runtimeID.String()).
			Int("cached_runtime_epoch", result.cached.runtimeEpoch).
			Str("cached_worker_node_id", result.cached.workerNodeID).
			Time("cached_runtime_valid_until", result.cached.validUntil)
	}
	if result.active != nil {
		event = event.
			Str("active_runtime_id", result.active.ID.String()).
			Int("active_runtime_epoch", result.active.RuntimeEpoch).
			Str("active_worker_node_id", result.active.WorkerNodeID).
			Str("active_endpoint_url", result.active.EndpointURL).
			Str("active_preview_handle", result.active.PreviewHandle).
			Int("active_primary_port", result.active.PrimaryPort).
			Str("active_runtime_status", string(result.active.Status)).
			Time("active_runtime_lease_expires_at", result.active.LeaseExpiresAt).
			Time("active_runtime_last_heartbeat_at", result.active.LastHeartbeatAt)
	}
	event.Msg("internal preview runtime authorization rejected")
}

func addInternalPreviewRequestLogFields(event *zerolog.Event, r *http.Request) *zerolog.Event {
	requestMethod := ""
	requestHost := ""
	requestPath := ""
	queryPresent := false
	if r != nil {
		requestMethod = r.Method
		requestHost = r.Host
		if r.URL != nil {
			requestPath = r.URL.Path
			queryPresent = r.URL.RawQuery != ""
		}
	}
	return event.
		Str("request_method", requestMethod).
		Str("request_host", requestHost).
		Str("request_path", requestPath).
		Bool("query_present", queryPresent)
}

func addPreviewTokenClaimLogFields(event *zerolog.Event, claims *auth.PreviewTokenClaims) *zerolog.Event {
	event = event.
		Str("org_id", claims.OrgID.String()).
		Str("target_worker_node_id", claims.TargetNodeID).
		Str("token_action", claims.Action).
		Time("token_expires_at", claims.ExpiresAt)
	if claims.SessionID != nil {
		event = event.Str("session_id", claims.SessionID.String())
	}
	if claims.PreviewID != nil {
		event = event.Str("preview_id", claims.PreviewID.String())
	}
	if claims.RuntimeID != nil {
		event = event.Str("claimed_runtime_id", claims.RuntimeID.String())
	}
	if claims.RuntimeEpoch > 0 {
		event = event.Int("claimed_runtime_epoch", claims.RuntimeEpoch)
	}
	return event
}

func (h *InternalPreviewHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	previewID, claims, ok := h.authorizePreviewAction(w, r, "proxy")
	if !ok {
		return
	}
	r = r.WithContext(middleware.WithOrgID(r.Context(), claims.OrgID))
	orgID := middleware.OrgIDFromContext(r.Context())

	if isWebSocketUpgrade(r) {
		h.handleWebSocketProxy(w, r, orgID, previewID, claims)
		return
	}
	h.handleHTTPProxy(w, r, orgID, previewID, claims)
}

func (h *InternalPreviewHandler) handleHTTPProxy(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID, claims *auth.PreviewTokenClaims) {
	originalReq := r
	backendPath := trimInternalPreviewProxyPath(r.URL.Path, previewID)
	if backendPath == "" {
		backendPath = "/"
	}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "preview-target"
			req.URL.Path = backendPath
			req.RequestURI = ""
			req.Host = "preview-target"
			req.Header.Del("Authorization")
			stripPreviewAccessCookies(req)
		},
		Transport: h.previewTransport(orgID, previewID),
		ModifyResponse: func(resp *http.Response) error {
			// Strip control-plane markers a previewed app might set so it can't
			// spoof a worker auth/routing failure to the public gateway.
			resp.Header.Del(auth.PreviewWorkerErrorHeader)
			resp.Header.Del(auth.PreviewWorkerAuthDetailHeader)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			addInternalPreviewProxyLogFields(h.logger.Warn().Err(err), originalReq, orgID, previewID, claims, backendPath).
				Msg("internal preview proxy error")
			http.Error(w, "preview unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// previewTransport returns the pooled upstream transport for a preview,
// creating it on first use. Pooled connections are keyed per preview so
// container streams are reused across requests instead of dialed fresh each
// time. Connections to a torn-down container close on the container side and
// fall out of the pool, so a recycle converges on the new container without
// explicit invalidation; eviction hooks just tighten the window.
func (h *InternalPreviewHandler) previewTransport(orgID, previewID uuid.UUID) http.RoundTripper {
	h.transportMu.Lock()
	defer h.transportMu.Unlock()
	if transport, ok := h.previewTransports[previewID]; ok {
		return transport
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := h.manager.DialPreview(ctx, orgID, previewID)
			if err != nil {
				return nil, fmt.Errorf("dial preview: %w", err)
			}
			return conn, nil
		},
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     60 * time.Second,
		// Preserve end-to-end content negotiation: the browser's own
		// Accept-Encoding is forwarded by the gateway, and the previous
		// raw-conn transport never decompressed bodies.
		DisableCompression: true,
	}
	if len(h.previewTransports) > 1024 {
		// Safety valve against unbounded growth across short-lived previews.
		for id, old := range h.previewTransports {
			old.CloseIdleConnections()
			delete(h.previewTransports, id)
		}
	}
	h.previewTransports[previewID] = transport
	return transport
}

// evictPreviewTransport drops the pooled transport for a preview and closes
// its idle connections. Called when the preview is stopped or recycled on
// this worker.
func (h *InternalPreviewHandler) evictPreviewTransport(previewID uuid.UUID) {
	h.transportMu.Lock()
	transport, ok := h.previewTransports[previewID]
	if ok {
		delete(h.previewTransports, previewID)
	}
	h.transportMu.Unlock()
	if ok {
		transport.CloseIdleConnections()
	}
}

func (h *InternalPreviewHandler) handleWebSocketProxy(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID, claims *auth.PreviewTokenClaims) {
	backendPath := trimInternalPreviewProxyPath(r.URL.Path, previewID)
	if backendPath == "" {
		backendPath = "/"
	}
	backendConn, err := h.manager.DialPreview(r.Context(), orgID, previewID)
	if err != nil {
		addInternalPreviewProxyLogFields(h.logger.Warn().Err(err), r, orgID, previewID, claims, backendPath).
			Msg("internal websocket dial failed")
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
		addInternalPreviewProxyLogFields(h.logger.Warn().Err(err), r, orgID, previewID, claims, backendPath).
			Msg("internal websocket: failed to forward upgrade request")
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

func addInternalPreviewProxyLogFields(event *zerolog.Event, r *http.Request, orgID, previewID uuid.UUID, claims *auth.PreviewTokenClaims, backendPath string) *zerolog.Event {
	requestPath := ""
	requestHost := ""
	requestMethod := ""
	queryPresent := false
	secFetchDest := ""
	if r != nil {
		requestHost = r.Host
		requestMethod = r.Method
		secFetchDest = r.Header.Get("Sec-Fetch-Dest")
		if r.URL != nil {
			requestPath = r.URL.Path
			queryPresent = r.URL.RawQuery != ""
		}
	}

	event = event.
		Str("org_id", orgID.String()).
		Str("preview_id", previewID.String()).
		Str("request_method", requestMethod).
		Str("request_host", requestHost).
		Str("request_path", requestPath).
		Bool("query_present", queryPresent).
		Str("sec_fetch_dest", secFetchDest).
		Str("backend_path", backendPath)

	if claims == nil {
		return event
	}

	event = event.
		Str("target_node_id", claims.TargetNodeID).
		Int("runtime_epoch", claims.RuntimeEpoch)
	if claims.RuntimeID != nil {
		event = event.Str("runtime_id", claims.RuntimeID.String())
	}
	return event
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
