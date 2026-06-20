package privateconnector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestServiceAuthorizeSessionVerifiesStoredInstanceKey(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test connector identity should generate")
	instanceID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:        instanceID,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		Status:    models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{Now: func() time.Time { return now }})
	payload := connector.SessionAuthPayload{
		InstanceID: instanceID,
		Nonce:      uuid.New(),
		IssuedAt:   now,
	}
	signature, err := connector.SignSessionAuth(privateKey, payload)
	require.NoError(t, err, "test session auth should sign")

	instance, err := svc.AuthorizeSession(context.Background(), payload, signature)

	require.NoError(t, err, "AuthorizeSession should accept a valid connector session signature")
	require.Equal(t, instanceID, instance.ID, "AuthorizeSession should return the authorized connector instance")

	_, err = svc.AuthorizeSession(context.Background(), payload, signature)
	require.ErrorIs(t, err, connector.ErrActionReplay, "AuthorizeSession should reject replayed session nonces")
}

func TestServiceAuthorizeSessionRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test connector identity should generate")
	instanceID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:        instanceID,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		Status:    models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{Now: func() time.Time { return now }})

	_, err = svc.AuthorizeSession(context.Background(), connector.SessionAuthPayload{
		InstanceID: instanceID,
		Nonce:      uuid.New(),
		IssuedAt:   now,
	}, "not-base64")

	require.ErrorIs(t, err, connector.ErrActionSignature, "AuthorizeSession should reject invalid signatures")
}

func TestServiceRotateInstanceIdentityVerifiesCurrentIdentity(t *testing.T) {
	t.Parallel()

	oldPublic, oldPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "old connector identity should generate")
	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "new connector identity should generate")
	orgID := uuid.New()
	instanceID := uuid.New()
	body, err := json.Marshal(RotateInstanceIdentityRequest{
		PublicKey: base64.StdEncoding.EncodeToString(newPublic),
	})
	require.NoError(t, err, "rotation request should encode")
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(oldPrivate, body))
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:        instanceID,
		OrgID:     orgID,
		PublicKey: base64.StdEncoding.EncodeToString(oldPublic),
		Status:    models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{})

	rotated, err := svc.RotateInstanceIdentity(context.Background(), instanceID, body, signature)

	require.NoError(t, err, "RotateInstanceIdentity should accept signatures from the current identity")
	require.Equal(t, instanceID, rotated.ID, "RotateInstanceIdentity should return the rotated instance")
	require.Equal(t, base64.StdEncoding.EncodeToString(newPublic), store.rotatedInstance.PublicKey, "RotateInstanceIdentity should persist the new public key")
}
