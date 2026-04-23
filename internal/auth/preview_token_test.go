package auth

import (
	"encoding/base64"
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
