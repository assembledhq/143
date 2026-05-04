// Package db — scoped_credential_store_test.go
//
// Unit coverage for the type/provider translation helpers and the
// constructor's nil-guard. The adapter's routing behavior at the SQL layer
// is exercised end-to-end through the auth-service tests in
// internal/services/{codexauth,claudecodeauth}, which run against a
// scope-aware in-memory mock that mirrors the real partitioning.
package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestScopedCredentialStore_TranslateProviderForUnified(t *testing.T) {
	t.Parallel()

	cases := []struct {
		legacy  models.ProviderName
		unified models.ProviderName
	}{
		{models.ProviderOpenAIChatGPT, models.ProviderOpenAISubscription},
		{models.ProviderAnthropic, models.ProviderAnthropicSubscription},
		{models.ProviderGemini, models.ProviderGemini}, // pass-through
		{models.ProviderAmp, models.ProviderAmp},
		{models.ProviderPi, models.ProviderPi},
	}
	for _, c := range cases {
		require.Equal(t, c.unified, translateProviderForUnified(c.legacy), "%s should map to %s", c.legacy, c.unified)
	}
}

func TestScopedCredentialStore_TranslateProviderRoundtrip(t *testing.T) {
	// translateProviderForUnified and its inverse must form a roundtrip for
	// the OAuth provider names; otherwise a personal-scope read would
	// surface a unified-typed cred to the auth service, which only knows
	// about the legacy types.
	t.Parallel()

	for _, legacy := range []models.ProviderName{
		models.ProviderOpenAIChatGPT,
		models.ProviderAnthropic,
	} {
		round := translateProviderFromUnified(translateProviderForUnified(legacy))
		require.Equal(t, legacy, round, "roundtrip should restore the legacy provider name for %s", legacy)
	}
}

func TestScopedCredentialStore_TranslateConfigForUnifiedWrite_OpenAI(t *testing.T) {
	// OpenAIChatGPTConfig and OpenAISubscriptionConfig have identical layouts
	// — the conversion must preserve every field so a refresh round-trip
	// after writing doesn't drop tokens or expiry.
	t.Parallel()

	expiry := time.Now().Add(time.Hour).UTC()
	src := models.OpenAIChatGPTConfig{
		AccessToken:     "access-123",
		RefreshToken:    "refresh-456",
		IDToken:         "id-789",
		ExpiresAt:       expiry,
		AccountType:     "plus",
		DeviceAuthID:    "dev-abc",
		UserCode:        "USER-CODE",
		VerificationURI: "https://auth.openai.com/verify",
		PollInterval:    7,
	}
	out, err := translateConfigForUnifiedWrite(src)
	require.NoError(t, err)
	got, ok := out.(models.OpenAISubscriptionConfig)
	require.True(t, ok, "OpenAIChatGPTConfig must translate to OpenAISubscriptionConfig, got %T", out)
	require.Equal(t, src.AccessToken, got.AccessToken)
	require.Equal(t, src.RefreshToken, got.RefreshToken)
	require.Equal(t, src.IDToken, got.IDToken)
	require.True(t, src.ExpiresAt.Equal(got.ExpiresAt))
	require.Equal(t, src.AccountType, got.AccountType)
	require.Equal(t, src.DeviceAuthID, got.DeviceAuthID)
	require.Equal(t, src.UserCode, got.UserCode)
	require.Equal(t, src.VerificationURI, got.VerificationURI)
	require.Equal(t, src.PollInterval, got.PollInterval)
}

func TestScopedCredentialStore_TranslateConfigForUnifiedWrite_AnthropicSubscription(t *testing.T) {
	// AnthropicConfig with a non-nil Subscription must collapse to
	// AnthropicSubscriptionConfig (the unified table's flattened shape).
	t.Parallel()

	expiry := time.Now().Add(time.Hour).UTC()
	src := models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken:   "access-claude",
			RefreshToken:  "refresh-claude",
			ExpiresAt:     expiry,
			AccountType:   "claude_max",
			RateLimitTier: "default_claude_max_20x",
			Scopes:        []string{"user:profile", "user:inference"},
			State:         "csrf-state",
			CodeVerifier:  "pkce-verifier",
			AuthorizeURL:  "https://claude.ai/authorize?...",
		},
	}
	out, err := translateConfigForUnifiedWrite(src)
	require.NoError(t, err)
	got, ok := out.(models.AnthropicSubscriptionConfig)
	require.True(t, ok, "AnthropicConfig{Subscription} must translate to AnthropicSubscriptionConfig, got %T", out)
	require.Equal(t, src.Subscription.AccessToken, got.AccessToken)
	require.Equal(t, src.Subscription.RefreshToken, got.RefreshToken)
	require.True(t, src.Subscription.ExpiresAt.Equal(got.ExpiresAt))
	require.Equal(t, src.Subscription.AccountType, got.AccountType)
	require.Equal(t, src.Subscription.RateLimitTier, got.RateLimitTier)
	require.Equal(t, src.Subscription.Scopes, got.Scopes)
	require.Equal(t, src.Subscription.State, got.State)
	require.Equal(t, src.Subscription.CodeVerifier, got.CodeVerifier)
	require.Equal(t, src.Subscription.AuthorizeURL, got.AuthorizeURL)
}

func TestScopedCredentialStore_TranslateConfigForUnifiedWrite_AnthropicAPIKey(t *testing.T) {
	// API-key-only AnthropicConfig (no Subscription) is left untouched —
	// the unified table stores personal anthropic API keys under
	// provider=anthropic, not anthropic_subscription.
	t.Parallel()

	src := models.AnthropicConfig{APIKey: "sk-ant-test"}
	out, err := translateConfigForUnifiedWrite(src)
	require.NoError(t, err)
	got, ok := out.(models.AnthropicConfig)
	require.True(t, ok, "API-key AnthropicConfig must pass through unchanged, got %T", out)
	require.Equal(t, src.APIKey, got.APIKey)
	require.Nil(t, got.Subscription)
}

func TestScopedCredentialStore_TranslateConfigFromUnifiedRead_Roundtrip(t *testing.T) {
	// A write-then-read roundtrip must restore the legacy types exactly.
	// The auth services pattern-match on AnthropicConfig / OpenAIChatGPTConfig
	// so any drift would surface as "credential is not OpenAIChatGPTConfig"
	// at the next refresh.
	t.Parallel()

	chatGPT := models.OpenAIChatGPTConfig{
		AccessToken:  "access",
		RefreshToken: "refresh",
		IDToken:      "id",
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
		AccountType:  "plus",
	}
	written, err := translateConfigForUnifiedWrite(chatGPT)
	require.NoError(t, err)
	read := translateConfigFromUnifiedRead(written)
	got, ok := read.(models.OpenAIChatGPTConfig)
	require.True(t, ok, "roundtrip should restore OpenAIChatGPTConfig, got %T", read)
	require.Equal(t, chatGPT, got)

	anthropic := models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken:  "claude-access",
			RefreshToken: "claude-refresh",
			ExpiresAt:    time.Now().Add(time.Hour).UTC(),
			Scopes:       []string{"user:profile"},
		},
	}
	writtenA, err := translateConfigForUnifiedWrite(anthropic)
	require.NoError(t, err)
	readA := translateConfigFromUnifiedRead(writtenA)
	gotA, ok := readA.(models.AnthropicConfig)
	require.True(t, ok, "roundtrip should restore AnthropicConfig, got %T", readA)
	require.NotNil(t, gotA.Subscription)
	require.Equal(t, anthropic.Subscription.AccessToken, gotA.Subscription.AccessToken)
	require.Equal(t, anthropic.Subscription.RefreshToken, gotA.Subscription.RefreshToken)
	require.True(t, anthropic.Subscription.ExpiresAt.Equal(gotA.Subscription.ExpiresAt))
	require.Equal(t, anthropic.Subscription.Scopes, gotA.Subscription.Scopes)
}

func TestScopedCredentialStore_DecryptedCredentialFromCoding_TranslatesProvider(t *testing.T) {
	// The unified row's provider name (anthropic_subscription) must surface
	// to the auth service as the legacy name (anthropic), since that's what
	// the service's Provider checks compare against.
	t.Parallel()

	expiry := time.Now().Add(time.Hour).UTC()
	created := time.Now().UTC()
	row := &models.DecryptedCodingCredential{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		UserID:    uuidPtr(uuid.New()),
		Provider:  models.ProviderAnthropicSubscription,
		Label:     "personal-claude",
		Status:    models.CodingCredentialStatusActive,
		Priority:  1,
		CreatedAt: created,
		UpdatedAt: created,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ExpiresAt:    expiry,
		},
	}
	legacy, err := decryptedCredentialFromCoding(row)
	require.NoError(t, err)
	require.Equal(t, models.ProviderAnthropic, legacy.Provider, "provider must translate back to anthropic for legacy callers")
	require.Equal(t, row.ID, legacy.ID)
	require.Equal(t, row.OrgID, legacy.OrgID)
	require.Equal(t, row.Label, legacy.Label)

	// The config must come back as AnthropicConfig with a populated
	// Subscription — that's the shape the auth services pattern-match on.
	cfg, ok := legacy.Config.(models.AnthropicConfig)
	require.True(t, ok, "config must translate back to AnthropicConfig, got %T", legacy.Config)
	require.NotNil(t, cfg.Subscription)
	require.Equal(t, "access", cfg.Subscription.AccessToken)
	require.Equal(t, "refresh", cfg.Subscription.RefreshToken)
	require.True(t, expiry.Equal(cfg.Subscription.ExpiresAt))
}

func TestScopedCredentialStore_NewPanicsOnNilOrgStore(t *testing.T) {
	// Construction-time guard: missing wiring fails fast at boot rather
	// than surfacing as cryptic per-request errors.
	t.Parallel()

	require.PanicsWithValue(t,
		"db: NewScopedCredentialStore requires a non-nil OrgCredentialStore",
		func() { NewScopedCredentialStore(nil, &CodingCredentialStore{}) },
	)
}

func TestScopedCredentialStore_NewPanicsOnNilCodingStore(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t,
		"db: NewScopedCredentialStore requires a non-nil CodingCredentialStore",
		func() { NewScopedCredentialStore(&OrgCredentialStore{}, nil) },
	)
}

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
