package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidateInternalToken(t *testing.T) {
	t.Parallel()

	secret := "test-secret-key-for-hmac-signing"
	orgID := uuid.New()

	token, err := GenerateInternalToken(secret, orgID, 5*time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Contains(t, token, ".")

	claims, err := ValidateInternalToken(secret, token)
	require.NoError(t, err)
	require.Equal(t, orgID, claims.OrgID)
	require.True(t, claims.ExpiresAt.After(time.Now()))
}

func TestValidateInternalToken_InvalidSignature(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	token, err := GenerateInternalToken("secret-one", orgID, 5*time.Minute)
	require.NoError(t, err)

	_, err = ValidateInternalToken("secret-two", token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid signature")
}

func TestValidateInternalToken_Expired(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()

	token, err := GenerateInternalToken(secret, orgID, -1*time.Minute)
	require.NoError(t, err)

	_, err = ValidateInternalToken(secret, token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token expired")
}

func TestValidateInternalToken_InvalidFormat(t *testing.T) {
	t.Parallel()

	_, err := ValidateInternalToken("secret", "no-dot-here")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid token format")
}

func TestValidateInternalToken_InvalidBase64(t *testing.T) {
	t.Parallel()

	_, err := ValidateInternalToken("secret", "!!!invalid.!!!base64")
	require.Error(t, err)
}

func TestGenerateInternalToken_DifferentOrgs(t *testing.T) {
	t.Parallel()

	secret := "shared-secret"
	org1 := uuid.New()
	org2 := uuid.New()

	token1, err := GenerateInternalToken(secret, org1, 5*time.Minute)
	require.NoError(t, err)

	token2, err := GenerateInternalToken(secret, org2, 5*time.Minute)
	require.NoError(t, err)

	require.NotEqual(t, token1, token2)

	claims1, err := ValidateInternalToken(secret, token1)
	require.NoError(t, err)
	require.Equal(t, org1, claims1.OrgID)

	claims2, err := ValidateInternalToken(secret, token2)
	require.NoError(t, err)
	require.Equal(t, org2, claims2.OrgID)
}
