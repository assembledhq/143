package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

type internalSandboxAuthBrokerStub struct {
	acquirePath string
	acquireErr  error
	releaseErr  error

	acquireCalls int
	releaseCalls int
	orgID        uuid.UUID
	sessionID    uuid.UUID
	holderID     uuid.UUID
}

func (s *internalSandboxAuthBrokerStub) Acquire(_ context.Context, orgID, sessionID, holderID uuid.UUID) (string, error) {
	s.acquireCalls++
	s.orgID = orgID
	s.sessionID = sessionID
	s.holderID = holderID
	if s.acquireErr != nil {
		return "", s.acquireErr
	}
	return s.acquirePath, nil
}

func (s *internalSandboxAuthBrokerStub) Release(_ context.Context, orgID, sessionID, holderID uuid.UUID) error {
	s.releaseCalls++
	s.orgID = orgID
	s.sessionID = sessionID
	s.holderID = holderID
	return s.releaseErr
}

func newInternalSandboxAuthTestHandler(t *testing.T, broker *internalSandboxAuthBrokerStub) (*InternalSandboxAuthHandler, auth.PreviewTokenKeyring) {
	t.Helper()
	keyring, err := auth.NewPreviewTokenKeyring([]string{"worker-secret"})
	require.NoError(t, err, "test keyring should be valid")
	return NewInternalSandboxAuthHandler(broker, "worker-1", keyring, zerolog.Nop()), keyring
}

func signedSandboxAuthRequest(t *testing.T, keyring auth.PreviewTokenKeyring, action string, claimsOrgID, claimsSessionID uuid.UUID, body any) *http.Request {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err, "test request body should marshal")
	token, err := keyring.Generate(auth.PreviewTokenClaims{
		OrgID:        claimsOrgID,
		TargetNodeID: "worker-1",
		SessionID:    &claimsSessionID,
		Action:       action,
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	require.NoError(t, err, "test token should sign")
	req := httptest.NewRequest(http.MethodPost, "/internal/sandbox-auth/acquire", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestInternalSandboxAuthHandler_AcquireAndRelease(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	socketPath := "/var/run/143/sandbox-auth/" + sessionID.String() + "/sock"
	broker := &internalSandboxAuthBrokerStub{acquirePath: socketPath}
	handler, keyring := newInternalSandboxAuthTestHandler(t, broker)

	acquireReq := signedSandboxAuthRequest(t, keyring, "sandbox_auth_acquire", orgID, sessionID, sandboxauth.BrokerAcquireRequest{
		OrgID:     orgID,
		SessionID: sessionID,
		HolderID:  holderID,
	})
	acquireRR := httptest.NewRecorder()
	handler.Acquire(acquireRR, acquireReq)
	require.Equal(t, http.StatusOK, acquireRR.Code, "Acquire should return 200 for a valid signed request")
	var acquireResp models.SingleResponse[sandboxauth.BrokerAcquireResponse]
	require.NoError(t, json.Unmarshal(acquireRR.Body.Bytes(), &acquireResp), "Acquire response should be valid JSON")
	require.Equal(t, socketPath, acquireResp.Data.SocketPath, "Acquire should return the broker socket path")
	require.Equal(t, 1, broker.acquireCalls, "Acquire should call the broker once")
	require.Equal(t, holderID, broker.holderID, "Acquire should pass the holder id to the broker")

	releaseReq := signedSandboxAuthRequest(t, keyring, "sandbox_auth_release", orgID, sessionID, sandboxauth.BrokerReleaseRequest{
		OrgID:     orgID,
		SessionID: sessionID,
		HolderID:  holderID,
	})
	releaseRR := httptest.NewRecorder()
	handler.Release(releaseRR, releaseReq)
	require.Equal(t, http.StatusOK, releaseRR.Code, "Release should return 200 for a valid signed request")
	var releaseResp models.SingleResponse[sandboxauth.BrokerReleaseResponse]
	require.NoError(t, json.Unmarshal(releaseRR.Body.Bytes(), &releaseResp), "Release response should be valid JSON")
	require.True(t, releaseResp.Data.Released, "Release should acknowledge a successful release")
	require.Equal(t, 1, broker.releaseCalls, "Release should call the broker once")
	require.Equal(t, holderID, broker.holderID, "Release should pass the holder id to the broker")
}

func TestInternalSandboxAuthHandler_AcquireRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	broker := &internalSandboxAuthBrokerStub{acquirePath: "/tmp/sock"}
	handler, keyring := newInternalSandboxAuthTestHandler(t, broker)

	tests := []struct {
		name           string
		tokenAction    string
		tokenOrgID     uuid.UUID
		tokenSessionID uuid.UUID
		body           sandboxauth.BrokerAcquireRequest
		expectedStatus int
	}{
		{
			name:           "wrong action",
			tokenAction:    "sandbox_auth_release",
			tokenOrgID:     orgID,
			tokenSessionID: sessionID,
			body:           sandboxauth.BrokerAcquireRequest{OrgID: orgID, SessionID: sessionID, HolderID: holderID},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "org mismatch",
			tokenAction:    "sandbox_auth_acquire",
			tokenOrgID:     uuid.New(),
			tokenSessionID: sessionID,
			body:           sandboxauth.BrokerAcquireRequest{OrgID: orgID, SessionID: sessionID, HolderID: holderID},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "session mismatch",
			tokenAction:    "sandbox_auth_acquire",
			tokenOrgID:     orgID,
			tokenSessionID: uuid.New(),
			body:           sandboxauth.BrokerAcquireRequest{OrgID: orgID, SessionID: sessionID, HolderID: holderID},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "zero holder",
			tokenAction:    "sandbox_auth_acquire",
			tokenOrgID:     orgID,
			tokenSessionID: sessionID,
			body:           sandboxauth.BrokerAcquireRequest{OrgID: orgID, SessionID: sessionID},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := signedSandboxAuthRequest(t, keyring, tt.tokenAction, tt.tokenOrgID, tt.tokenSessionID, tt.body)
			rr := httptest.NewRecorder()
			handler.Acquire(rr, req)
			require.Equal(t, tt.expectedStatus, rr.Code, "Acquire should reject invalid signed requests before broker mutation")
		})
	}
}

func TestInternalSandboxAuthHandler_ReleaseSurfacesBrokerError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	broker := &internalSandboxAuthBrokerStub{releaseErr: errors.New("lost broker")}
	handler, keyring := newInternalSandboxAuthTestHandler(t, broker)
	req := signedSandboxAuthRequest(t, keyring, "sandbox_auth_release", orgID, sessionID, sandboxauth.BrokerReleaseRequest{
		OrgID:     orgID,
		SessionID: sessionID,
		HolderID:  holderID,
	})

	rr := httptest.NewRecorder()
	handler.Release(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code, "Release should surface broker release failures")
	require.Equal(t, 1, broker.releaseCalls, "Release should call broker before surfacing the failure")
}
