package connector

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVerifyActionRequestAcceptsValidSignedRequestAndRejectsReplay(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	connectorID := uuid.New()
	resourceID := uuid.New()
	req := ActionRequest{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceID:  resourceID,
		Capability:  "victorialogs.query",
		RequestID:   uuid.New(),
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
	}
	signature, err := SignActionRequest(privateKey, req)
	require.NoError(t, err, "SignActionRequest should sign canonical action payloads")
	cache := NewNonceCache(30 * time.Second)

	err = VerifyActionRequest(publicKey, req, signature, VerifyOptions{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
		Now:         func() time.Time { return now.Add(5 * time.Second) },
		NonceCache:  cache,
	})
	require.NoError(t, err, "VerifyActionRequest should accept a valid signed request")

	err = VerifyActionRequest(publicKey, req, signature, VerifyOptions{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
		Now:         func() time.Time { return now.Add(6 * time.Second) },
		NonceCache:  cache,
	})
	require.ErrorIs(t, err, ErrActionReplay, "VerifyActionRequest should reject replayed request IDs")
}

func TestVerifyActionRequestRejectsInvalidTrustBoundary(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	connectorID := uuid.New()
	resourceID := uuid.New()
	req := ActionRequest{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceID:  resourceID,
		Capability:  "victorialogs.query",
		RequestID:   uuid.New(),
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
	}
	signature, err := SignActionRequest(privateKey, req)
	require.NoError(t, err, "SignActionRequest should sign canonical action payloads")

	tests := []struct {
		name    string
		req     ActionRequest
		sig     string
		opts    VerifyOptions
		wantErr error
	}{
		{
			name: "rejects tampered signature",
			req:  req,
			sig:  base64.StdEncoding.EncodeToString([]byte("bad signature")),
			opts: VerifyOptions{
				OrgID:       orgID,
				ConnectorID: connectorID,
				ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
				Now:         func() time.Time { return now },
				NonceCache:  NewNonceCache(30 * time.Second),
			},
			wantErr: ErrActionSignature,
		},
		{
			name: "rejects wrong org",
			req:  req,
			sig:  signature,
			opts: VerifyOptions{
				OrgID:       uuid.New(),
				ConnectorID: connectorID,
				ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
				Now:         func() time.Time { return now },
				NonceCache:  NewNonceCache(30 * time.Second),
			},
			wantErr: ErrActionUnauthorized,
		},
		{
			name: "rejects unknown resource",
			req:  req,
			sig:  signature,
			opts: VerifyOptions{
				OrgID:       orgID,
				ConnectorID: connectorID,
				ResourceIDs: map[uuid.UUID]struct{}{},
				Now:         func() time.Time { return now },
				NonceCache:  NewNonceCache(30 * time.Second),
			},
			wantErr: ErrActionUnauthorized,
		},
		{
			name: "rejects expired request",
			req:  req,
			sig:  signature,
			opts: VerifyOptions{
				OrgID:       orgID,
				ConnectorID: connectorID,
				ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
				Now:         func() time.Time { return now.Add(time.Minute) },
				NonceCache:  NewNonceCache(30 * time.Second),
			},
			wantErr: ErrActionExpired,
		},
		{
			name: "rejects clock skew",
			req:  req,
			sig:  signature,
			opts: VerifyOptions{
				OrgID:       orgID,
				ConnectorID: connectorID,
				ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
				Now:         func() time.Time { return now.Add(-20 * time.Second) },
				NonceCache:  NewNonceCache(30 * time.Second),
			},
			wantErr: ErrActionClockSkew,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifyActionRequest(publicKey, tt.req, tt.sig, tt.opts)
			require.ErrorIs(t, err, tt.wantErr, "VerifyActionRequest should reject the expected trust-boundary violation")
		})
	}
}

func TestSignAndVerifySessionAuth(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	payload := SessionAuthPayload{
		InstanceID: uuid.New(),
		Nonce:      uuid.New(),
		IssuedAt:   now,
	}

	signature, err := SignSessionAuth(privateKey, payload)
	require.NoError(t, err, "SignSessionAuth should sign canonical session auth payloads")
	err = VerifySessionAuth(publicKey, payload, signature, SessionAuthVerifyOptions{
		InstanceID: payload.InstanceID,
		Now:        func() time.Time { return now },
		NonceCache: NewNonceCache(time.Minute),
	})
	require.NoError(t, err, "VerifySessionAuth should accept a fresh signed session auth payload")
}

func TestSignAndVerifyConfigPush(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	connectorID := uuid.New()
	frame := ConfigPushFrame{
		OrgID:       orgID,
		ConnectorID: connectorID,
		Version:     7,
		IssuedAt:    now,
		ExpiresAt:   now.Add(30 * time.Second),
		Resources: []ConfigPushResource{{
			ID:            uuid.New(),
			DisplayName:   "Production logs",
			ResourceType:  "victorialogs",
			Mode:          "logs",
			ConfigSource:  "ui",
			ConfigVersion: 7,
		}},
	}
	signature, err := SignConfigPush(privateKey, frame)
	require.NoError(t, err, "SignConfigPush should sign canonical config frames")

	err = VerifyConfigPush(publicKey, frame, signature, ConfigPushVerifyOptions{
		OrgID:       orgID,
		ConnectorID: connectorID,
		MinVersion:  6,
		Now:         func() time.Time { return now.Add(5 * time.Second) },
	})
	require.NoError(t, err, "VerifyConfigPush should accept signed config for the active connector")

	err = VerifyConfigPush(publicKey, frame, signature, ConfigPushVerifyOptions{
		OrgID:       orgID,
		ConnectorID: uuid.New(),
		MinVersion:  6,
		Now:         func() time.Time { return now.Add(5 * time.Second) },
	})
	require.ErrorIs(t, err, ErrActionUnauthorized, "VerifyConfigPush should reject config for a different connector")

	err = VerifyConfigPush(publicKey, frame, signature, ConfigPushVerifyOptions{
		OrgID:       orgID,
		ConnectorID: connectorID,
		MinVersion:  8,
		Now:         func() time.Time { return now.Add(5 * time.Second) },
	})
	require.ErrorIs(t, err, ErrConfigPushStale, "VerifyConfigPush should reject rollback config versions")
}

func TestVerifySessionAuthRejectsInvalidScopeAndReplay(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "ed25519 key generation should succeed")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	payload := SessionAuthPayload{
		InstanceID: uuid.New(),
		Nonce:      uuid.New(),
		IssuedAt:   now,
	}
	signature, err := SignSessionAuth(privateKey, payload)
	require.NoError(t, err, "test session signature should sign")
	cache := NewNonceCache(time.Minute)

	err = VerifySessionAuth(publicKey, payload, signature, SessionAuthVerifyOptions{
		InstanceID: uuid.New(),
		Now:        func() time.Time { return now },
	})
	require.ErrorIs(t, err, ErrActionUnauthorized, "VerifySessionAuth should reject a mismatched instance id")

	require.NoError(t, VerifySessionAuth(publicKey, payload, signature, SessionAuthVerifyOptions{
		InstanceID: payload.InstanceID,
		Now:        func() time.Time { return now },
		NonceCache: cache,
	}), "first session auth use should pass")
	err = VerifySessionAuth(publicKey, payload, signature, SessionAuthVerifyOptions{
		InstanceID: payload.InstanceID,
		Now:        func() time.Time { return now },
		NonceCache: cache,
	})
	require.ErrorIs(t, err, ErrActionReplay, "VerifySessionAuth should reject replayed nonces")
}
