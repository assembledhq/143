package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestParsePRAuthorMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		expected  prAuthorMode
		expectErr bool
	}{
		{name: "empty defaults to auto", raw: "", expected: prAuthorModeAuto},
		{name: "user", raw: "user", expected: prAuthorModeUser},
		{name: "app", raw: " app ", expected: prAuthorModeApp},
		{name: "invalid", raw: "bogus", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePRAuthorMode(tt.raw)
			if tt.expectErr {
				require.Error(t, err, "parsePRAuthorMode should reject invalid values")
				return
			}
			require.NoError(t, err, "parsePRAuthorMode should accept valid values")
			require.Equal(t, tt.expected, got, "parsePRAuthorMode should normalize the expected mode")
		})
	}
}

func TestShouldPromptForPRAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     prAuthorMode
		policy   models.PRAuthorship
		expected bool
	}{
		{name: "app mode never prompts", mode: prAuthorModeApp, policy: models.PRAuthorshipUserRequired, expected: false},
		{name: "app only policy never prompts", mode: prAuthorModeAuto, policy: models.PRAuthorshipAppOnly, expected: false},
		{name: "explicit user prompts when org allows user auth", mode: prAuthorModeUser, policy: models.PRAuthorshipUserRequired, expected: true},
		{name: "auto prompts for preferred", mode: prAuthorModeAuto, policy: models.PRAuthorshipUserPreferred, expected: true},
		{name: "auto prompts for required", mode: prAuthorModeAuto, policy: models.PRAuthorshipUserRequired, expected: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, shouldPromptForPRAuth(tt.mode, tt.policy), "shouldPromptForPRAuth should respect author mode and org policy")
		})
	}
}

func TestPRAuthResumeTokenRoundTripAndFailures(t *testing.T) {
	t.Parallel()

	now := time.Now()
	claims := prAuthResumeClaims{
		SessionID:  uuid.New(),
		UserID:     uuid.New(),
		OrgID:      uuid.New(),
		AuthorMode: string(prAuthorModeUser),
		ExpiresAt:  now.Add(time.Minute).Unix(),
	}

	token, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), claims)
	require.NoError(t, err, "signPRAuthResumeToken should sign valid claims")

	parsed, err := parsePRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), token, now)
	require.NoError(t, err, "parsePRAuthResumeToken should accept a valid token")
	require.Equal(t, claims.SessionID, parsed.SessionID, "parsed token should preserve the session id")

	_, err = signPRAuthResumeToken(nil, claims)
	require.Error(t, err, "signPRAuthResumeToken should reject missing signing keys")

	_, err = parsePRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), "invalid", now)
	require.Error(t, err, "parsePRAuthResumeToken should reject malformed tokens")

	_, err = parsePRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), token+"x", now)
	require.Error(t, err, "parsePRAuthResumeToken should reject bad signatures")

	expiredToken, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), prAuthResumeClaims{
		SessionID:  uuid.New(),
		UserID:     uuid.New(),
		OrgID:      uuid.New(),
		AuthorMode: string(prAuthorModeUser),
		ExpiresAt:  now.Add(-time.Hour).Unix(),
	})
	require.NoError(t, err, "signPRAuthResumeToken should sign expired claims for parse-time validation")

	_, err = parsePRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), expiredToken, now)
	require.Error(t, err, "parsePRAuthResumeToken should reject expired tokens")
}

func TestClearCookieAndIsSecureRequest(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "https://app.143.dev/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	clearCookie(rr, req, "github_pr_resume_state")

	result := rr.Result()
	require.True(t, isSecureRequest(req), "isSecureRequest should treat https and forwarded https requests as secure")
	require.Len(t, result.Cookies(), 1, "clearCookie should emit one clearing cookie")
	require.Equal(t, -1, result.Cookies()[0].MaxAge, "clearCookie should expire the cookie immediately")
	require.True(t, result.Cookies()[0].Secure, "clearCookie should preserve secure semantics for secure requests")

	require.False(t, isSecureRequest(nil), "isSecureRequest should treat nil requests as insecure")
}
