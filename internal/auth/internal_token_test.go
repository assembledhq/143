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
	repoID := uuid.New()

	token, err := GenerateInternalToken(secret, orgID, repoID, 5*time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Contains(t, token, ".")

	claims, err := ValidateInternalToken(secret, token)
	require.NoError(t, err)
	require.Equal(t, orgID, claims.OrgID)
	require.Equal(t, repoID, claims.RepoID)
	require.True(t, claims.ExpiresAt.After(time.Now()))
}

func TestValidateInternalToken_InvalidSignature(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	token, err := GenerateInternalToken("secret-one", orgID, uuid.New(), 5*time.Minute)
	require.NoError(t, err)

	_, err = ValidateInternalToken("secret-two", token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid signature")
}

func TestValidateInternalToken_Expired(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()

	token, err := GenerateInternalToken(secret, orgID, uuid.New(), -1*time.Minute)
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

func TestGenerateSessionToken_IncludesSessionID(t *testing.T) {
	t.Parallel()

	secret := "test-secret-key-for-hmac-signing"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()

	token, err := GenerateSessionToken(secret, orgID, repoID, sessionID, 5*time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := ValidateInternalToken(secret, token)
	require.NoError(t, err)
	require.Equal(t, orgID, claims.OrgID)
	require.Equal(t, repoID, claims.RepoID)
	require.NotNil(t, claims.SessionID, "session-scoped token must include session ID")
	require.Equal(t, sessionID, *claims.SessionID)
	require.Nil(t, claims.ThreadID, "session-scoped token should not include thread ID by default")
}

func TestGenerateSessionThreadToken_IncludesThreadID(t *testing.T) {
	t.Parallel()

	secret := "test-secret-key-for-hmac-signing"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	token, err := GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, 5*time.Minute)
	require.NoError(t, err, "GenerateSessionThreadToken should sign a token")

	claims, err := ValidateInternalToken(secret, token)
	require.NoError(t, err, "ValidateInternalToken should accept the signed thread token")
	require.NotNil(t, claims.SessionID, "thread token should include session ID")
	require.Equal(t, sessionID, *claims.SessionID, "thread token should preserve session ID")
	require.NotNil(t, claims.ThreadID, "thread token should include source thread ID")
	require.Equal(t, threadID, *claims.ThreadID, "thread token should preserve source thread ID")
}

func TestGenerateSessionThreadTokenWithClaims_IncludesEvalToolClaims(t *testing.T) {
	t.Parallel()

	secret := "test-secret-key-for-hmac-signing"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	bootstrapRunID := uuid.New()

	token, err := GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, &threadID, []string{"eval:add"}, "eval_bootstrap", &bootstrapRunID, 5*time.Minute)
	require.NoError(t, err, "GenerateSessionThreadTokenWithClaims should sign a token")

	claims, err := ValidateInternalToken(secret, token)
	require.NoError(t, err, "ValidateInternalToken should accept the signed token")
	require.Equal(t, []string{"eval:add"}, claims.AllowedToolScopes, "token should preserve allowed tool scopes")
	require.Equal(t, "eval_bootstrap", claims.SessionOrigin, "token should preserve session origin")
	require.NotNil(t, claims.EvalBootstrapRunID, "token should include eval bootstrap run id")
	require.Equal(t, bootstrapRunID, *claims.EvalBootstrapRunID, "token should preserve eval bootstrap run id")
}

func TestGenerateInternalToken_HasNoSessionID(t *testing.T) {
	t.Parallel()

	token, err := GenerateInternalToken("secret", uuid.New(), uuid.New(), 5*time.Minute)
	require.NoError(t, err)

	claims, err := ValidateInternalToken("secret", token)
	require.NoError(t, err)
	require.Nil(t, claims.SessionID, "repo-scoped token must not include session ID")
}

func TestGenerateInternalToken_DifferentOrgs(t *testing.T) {
	t.Parallel()

	secret := "shared-secret"
	org1 := uuid.New()
	org2 := uuid.New()

	token1, err := GenerateInternalToken(secret, org1, uuid.New(), 5*time.Minute)
	require.NoError(t, err)

	token2, err := GenerateInternalToken(secret, org2, uuid.New(), 5*time.Minute)
	require.NoError(t, err)

	require.NotEqual(t, token1, token2)

	claims1, err := ValidateInternalToken(secret, token1)
	require.NoError(t, err)
	require.Equal(t, org1, claims1.OrgID)

	claims2, err := ValidateInternalToken(secret, token2)
	require.NoError(t, err)
	require.Equal(t, org2, claims2.OrgID)
}
