package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

type InternalSandboxAuthBroker interface {
	Acquire(ctx context.Context, orgID, sessionID, holderID uuid.UUID) (string, error)
	Release(ctx context.Context, orgID, sessionID, holderID uuid.UUID) error
}

type InternalSandboxAuthHandler struct {
	broker  InternalSandboxAuthBroker
	nodeID  string
	keyring auth.PreviewTokenKeyring
	logger  zerolog.Logger
}

func NewInternalSandboxAuthHandler(broker InternalSandboxAuthBroker, nodeID string, keyring auth.PreviewTokenKeyring, logger zerolog.Logger) *InternalSandboxAuthHandler {
	return &InternalSandboxAuthHandler{
		broker:  broker,
		nodeID:  nodeID,
		keyring: keyring,
		logger:  logger,
	}
}

func (h *InternalSandboxAuthHandler) Acquire(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r, sandboxauth.BrokerActionAcquire)
	if !ok {
		return
	}
	var body sandboxauth.BrokerAcquireRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if !h.validateRequestScope(w, r, claims, body.OrgID, body.SessionID, body.HolderID) {
		return
	}
	if h.broker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SANDBOX_AUTH_BROKER_UNAVAILABLE", "sandbox auth broker is not configured")
		return
	}
	socketPath, err := h.broker.Acquire(r.Context(), body.OrgID, body.SessionID, body.HolderID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SANDBOX_AUTH_ACQUIRE_FAILED", "failed to acquire sandbox auth socket", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[sandboxauth.BrokerAcquireResponse]{
		Data: sandboxauth.BrokerAcquireResponse{SocketPath: socketPath},
	})
}

func (h *InternalSandboxAuthHandler) Release(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r, sandboxauth.BrokerActionRelease)
	if !ok {
		return
	}
	var body sandboxauth.BrokerReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if !h.validateRequestScope(w, r, claims, body.OrgID, body.SessionID, body.HolderID) {
		return
	}
	if h.broker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SANDBOX_AUTH_BROKER_UNAVAILABLE", "sandbox auth broker is not configured")
		return
	}
	if err := h.broker.Release(r.Context(), body.OrgID, body.SessionID, body.HolderID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SANDBOX_AUTH_RELEASE_FAILED", "failed to release sandbox auth socket", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[sandboxauth.BrokerReleaseResponse]{
		Data: sandboxauth.BrokerReleaseResponse{Released: true},
	})
}

func (h *InternalSandboxAuthHandler) authorize(w http.ResponseWriter, r *http.Request, action string) (*auth.PreviewTokenClaims, bool) {
	tokenStr := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tokenStr == "" {
		markPreviewWorkerError(w)
		w.Header().Set(auth.PreviewWorkerAuthDetailHeader, "missing")
		h.logAuthorizationRejected(r, "missing_token", action, nil, nil)
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization token")
		return nil, false
	}
	claims, err := h.keyring.Validate(tokenStr)
	if err != nil {
		markPreviewWorkerError(w)
		w.Header().Set(auth.PreviewWorkerAuthDetailHeader, auth.ClassifyPreviewTokenError(err))
		h.logAuthorizationRejected(r, "invalid_token", action, nil, err)
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid sandbox auth token", err)
		return nil, false
	}
	if claims.TargetNodeID != h.nodeID {
		markPreviewWorkerError(w)
		h.logAuthorizationRejected(r, "wrong_worker", action, claims, nil)
		writeError(w, r, http.StatusForbidden, "WRONG_SANDBOX_AUTH_WORKER", "sandbox auth token targets a different worker")
		return nil, false
	}
	if claims.Action != action {
		markPreviewWorkerError(w)
		h.logAuthorizationRejected(r, "action_mismatch", action, claims, nil)
		writeError(w, r, http.StatusForbidden, "SANDBOX_AUTH_ACTION_MISMATCH", "sandbox auth token is not valid for this action")
		return nil, false
	}
	return claims, true
}

func (h *InternalSandboxAuthHandler) validateRequestScope(w http.ResponseWriter, r *http.Request, claims *auth.PreviewTokenClaims, orgID, sessionID, holderID uuid.UUID) bool {
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ORG_ID", "org id is required")
		return false
	}
	if sessionID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "session id is required")
		return false
	}
	if holderID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_HOLDER_ID", "holder id is required")
		return false
	}
	if claims.OrgID != orgID {
		writeError(w, r, http.StatusForbidden, "ORG_MISMATCH", "sandbox auth token org does not match request")
		return false
	}
	if claims.SessionID == nil || *claims.SessionID != sessionID {
		writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "sandbox auth token does not match the requested session")
		return false
	}
	return true
}

func (h *InternalSandboxAuthHandler) logAuthorizationRejected(r *http.Request, failureKind, requestedAction string, claims *auth.PreviewTokenClaims, err error) {
	event := h.logger.Warn()
	if err != nil {
		event = event.Err(err)
	}
	event = addInternalPreviewRequestLogFields(event, r).
		Str("failure_kind", failureKind).
		Str("requested_sandbox_auth_action", requestedAction).
		Str("local_worker_node_id", h.nodeID)
	if claims != nil {
		event = addPreviewTokenClaimLogFields(event, claims)
	}
	event.Msg("internal sandbox auth authorization rejected")
}
