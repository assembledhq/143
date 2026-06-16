package auth

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidatePreviewToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	token, err := GeneratePreviewToken("secret", PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: "worker-1",
		SessionID:    &sessionID,
		PreviewID:    &previewID,
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	require.NoError(t, err, "GeneratePreviewToken should create a signed token")

	claims, err := ValidatePreviewToken("secret", token)
	require.NoError(t, err, "ValidatePreviewToken should accept a valid token")
	require.Equal(t, orgID, claims.OrgID, "validated claims should preserve org scope")
	require.Equal(t, "worker-1", claims.TargetNodeID, "validated claims should preserve the target worker")
	require.NotNil(t, claims.SessionID, "validated claims should preserve the session scope")
	require.Equal(t, sessionID, *claims.SessionID, "validated claims should preserve the session id")
	require.NotNil(t, claims.PreviewID, "validated claims should preserve the preview scope")
	require.Equal(t, previewID, *claims.PreviewID, "validated claims should preserve the preview id")
	require.Equal(t, "proxy", claims.Action, "validated claims should preserve the action")
}

func TestPreviewTokenKeyring_SignsWithFirstSecretAndValidatesAllSecrets(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	keyring, err := NewPreviewTokenKeyring([]string{"new-secret", "old-secret"})
	require.NoError(t, err, "NewPreviewTokenKeyring should accept multiple non-empty secrets")

	token, err := keyring.Generate(PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: "worker-1",
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	require.NoError(t, err, "PreviewTokenKeyring.Generate should sign with the first configured secret")

	_, err = ValidatePreviewToken("old-secret", token)
	require.Error(t, err, "tokens signed by the first configured secret should not validate with secondary secrets")

	claims, err := keyring.Validate(token)
	require.NoError(t, err, "PreviewTokenKeyring.Validate should accept a token signed by any configured secret")
	require.Equal(t, orgID, claims.OrgID, "validated keyring claims should preserve org scope")

	oldToken, err := GeneratePreviewToken("old-secret", PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: "worker-1",
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	require.NoError(t, err, "GeneratePreviewToken should create a legacy signed token")

	claims, err = keyring.Validate(oldToken)
	require.NoError(t, err, "PreviewTokenKeyring.Validate should accept tokens signed by secondary secrets during rotation")
	require.Equal(t, orgID, claims.OrgID, "validated secondary-secret claims should preserve org scope")
}

func TestNewPreviewTokenKeyring_RejectsEmptySecrets(t *testing.T) {
	t.Parallel()

	_, err := NewPreviewTokenKeyring([]string{"", "  "})
	require.Error(t, err, "NewPreviewTokenKeyring should reject an empty effective secret list")
}

func TestValidatePreviewToken_InvalidSignature(t *testing.T) {
	t.Parallel()

	token, err := GeneratePreviewToken("secret-one", PreviewTokenClaims{
		OrgID:        uuid.New(),
		TargetNodeID: "worker-1",
		Action:       "stop",
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	require.NoError(t, err, "GeneratePreviewToken should create a signed token")

	_, err = ValidatePreviewToken("secret-two", token)
	require.Error(t, err, "ValidatePreviewToken should reject a token signed with a different secret")
}

func TestValidatePreviewToken_Expired(t *testing.T) {
	t.Parallel()

	token, err := GeneratePreviewToken("secret", PreviewTokenClaims{
		OrgID:        uuid.New(),
		TargetNodeID: "worker-1",
		Action:       "start",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	require.NoError(t, err, "GeneratePreviewToken should create a signed token")

	_, err = ValidatePreviewToken("secret", token)
	require.Error(t, err, "ValidatePreviewToken should reject an expired token")
}

func TestValidatePreviewToken_InvalidFormatAndPayload(t *testing.T) {
	t.Parallel()

	_, err := ValidatePreviewToken("secret", "missing-dot")
	require.Error(t, err, "ValidatePreviewToken should reject malformed tokens")

	_, err = ValidatePreviewToken("secret", "###.###")
	require.Error(t, err, "ValidatePreviewToken should reject invalid base64 segments")

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"` + uuid.New().String() + `","target_node_id":"worker-1","action":"proxy","exp":"` + time.Now().Add(time.Minute).Format(time.RFC3339Nano) + `"}`))
	_, err = ValidatePreviewToken("secret", payload+".###")
	require.Error(t, err, "ValidatePreviewToken should reject invalid signature segments")

	tokenPayload := base64.RawURLEncoding.EncodeToString([]byte("not-json"))
	tokenSig := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	_, err = ValidatePreviewToken("secret", tokenPayload+"."+tokenSig)
	require.Error(t, err, "ValidatePreviewToken should reject non-JSON payloads")
}

func TestValidatePreviewTokenClassifiesFailures(t *testing.T) {
	t.Parallel()

	previewID := uuid.New()
	signed := func(secret string, exp time.Time) string {
		token, err := GeneratePreviewToken(secret, PreviewTokenClaims{
			OrgID:        uuid.New(),
			TargetNodeID: "worker-1",
			PreviewID:    &previewID,
			Action:       "proxy",
			ExpiresAt:    exp,
		})
		require.NoError(t, err, "GeneratePreviewToken should sign the test token")
		return token
	}

	tests := []struct {
		name       string
		token      string
		validate   func(token string) (*PreviewTokenClaims, error)
		wantErr    error
		wantDetail string
	}{
		{
			name:       "bad signature",
			token:      signed("right-secret", time.Now().Add(time.Minute)),
			validate:   func(tok string) (*PreviewTokenClaims, error) { return ValidatePreviewToken("wrong-secret", tok) },
			wantErr:    ErrPreviewTokenBadSignature,
			wantDetail: "bad_signature",
		},
		{
			name:       "expired",
			token:      signed("right-secret", time.Now().Add(-time.Minute)),
			validate:   func(tok string) (*PreviewTokenClaims, error) { return ValidatePreviewToken("right-secret", tok) },
			wantErr:    ErrPreviewTokenExpired,
			wantDetail: "expired",
		},
		{
			name:       "malformed",
			token:      "not-a-token",
			validate:   func(tok string) (*PreviewTokenClaims, error) { return ValidatePreviewToken("right-secret", tok) },
			wantErr:    ErrPreviewTokenMalformed,
			wantDetail: "malformed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.validate(tc.token)
			require.Error(t, err, "validation should fail")
			require.ErrorIs(t, err, tc.wantErr, "error should classify as the expected sentinel")
			require.Equal(t, tc.wantDetail, ClassifyPreviewTokenError(err), "ClassifyPreviewTokenError should map to the expected detail")
		})
	}

	require.Equal(t, "", ClassifyPreviewTokenError(nil), "nil error should classify as empty")
	require.Equal(t, "unknown", ClassifyPreviewTokenError(errors.New("boom")), "unrecognized error should classify as unknown")
}

func TestKeyringValidatePrefersExpiredOverSignatureMismatch(t *testing.T) {
	t.Parallel()

	// Token signed with secret-b but expired. A keyring whose first secret is
	// secret-a (signature mismatch) and second is secret-b (expired) should
	// report the expired classification — the diagnostic that matters.
	token, err := GeneratePreviewToken("secret-b", PreviewTokenClaims{
		OrgID:        uuid.New(),
		TargetNodeID: "worker-1",
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	require.NoError(t, err, "GeneratePreviewToken should sign the test token")

	keyring, err := NewPreviewTokenKeyring([]string{"secret-a", "secret-b"})
	require.NoError(t, err, "NewPreviewTokenKeyring should accept the test secrets")

	_, err = keyring.Validate(token)
	require.Error(t, err, "keyring should reject an expired token")
	require.ErrorIs(t, err, ErrPreviewTokenExpired, "keyring should preserve the expired classification through its wrapper")
	require.Equal(t, "expired", ClassifyPreviewTokenError(err), "expired should win over signature mismatches from other secrets")
}
