package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestScopeLabel(t *testing.T) {
	t.Parallel()
	user := uuid.New()
	cases := []struct {
		name  string
		scope Scope
		want  CodingCredentialScope
	}{
		{"org", Scope{OrgID: uuid.New()}, CodingCredentialScopeOrg},
		{"personal", Scope{OrgID: uuid.New(), UserID: &user}, CodingCredentialScopePersonal},
	}
	for _, tc := range cases {
		if got := tc.scope.Label(); got != tc.want {
			t.Errorf("%s: Label()=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestScopeHelpers(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	orgID := uuid.New()
	personal := Scope{OrgID: orgID, UserID: &userID}
	org := Scope{OrgID: orgID}
	cred := DecryptedCodingCredential{OrgID: orgID, UserID: &userID}

	require.True(t, personal.IsPersonal(), "personal scope should report IsPersonal")
	require.False(t, personal.IsOrg(), "personal scope should not report IsOrg")
	require.True(t, org.IsOrg(), "org scope should report IsOrg")
	require.False(t, org.IsPersonal(), "org scope should not report IsPersonal")
	require.Equal(t, personal, cred.Scope(), "credential Scope should preserve org and user ids")
}

func TestAnthropicSubscriptionConfigValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  AnthropicSubscriptionConfig
		ok   bool
	}{
		{
			"active tokens",
			AnthropicSubscriptionConfig{AccessToken: "a", RefreshToken: "b"},
			true,
		},
		{
			"pending pkce",
			AnthropicSubscriptionConfig{State: "s", CodeVerifier: "v"},
			true,
		},
		{
			"empty",
			AnthropicSubscriptionConfig{},
			false,
		},
		{
			"partial tokens",
			AnthropicSubscriptionConfig{AccessToken: "a"},
			false,
		},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: expected ok, got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestAnthropicSubscriptionConfigProvider(t *testing.T) {
	t.Parallel()
	if got := (AnthropicSubscriptionConfig{}).Provider(); got != ProviderAnthropicSubscription {
		t.Fatalf("Provider()=%q want %q", got, ProviderAnthropicSubscription)
	}
}

func TestOpenAISubscriptionConfigProvider(t *testing.T) {
	t.Parallel()
	if got := (OpenAISubscriptionConfig{}).Provider(); got != ProviderOpenAISubscription {
		t.Fatalf("Provider()=%q want %q", got, ProviderOpenAISubscription)
	}
}

func TestOpenAISubscriptionConfigValidateAndMetadata(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	valid := OpenAISubscriptionConfig{
		AccessToken:  "access-token-123456",
		RefreshToken: "refresh-token",
		ExpiresAt:    future,
		AccountType:  "pro",
	}

	require.NoError(t, valid.Validate(), "complete OpenAI subscription config should validate")
	require.False(t, valid.IsExpired(), "future OpenAI subscription token should not be expired")
	require.True(t, valid.NeedsRefresh(2*time.Hour), "OpenAI subscription token inside refresh window should need refresh")
	require.Equal(t, ProviderOpenAISubscription, valid.MaskedSummary().Provider, "OpenAI subscription summary should use subscription provider")
	require.Equal(t, "pro", valid.MaskedSummary().AccountType, "OpenAI subscription summary should preserve account type")
	require.NotEmpty(t, valid.MaskedSummary().MaskedKey, "OpenAI subscription summary should mask the access token")

	expired := valid
	expired.ExpiresAt = past
	require.True(t, expired.IsExpired(), "past OpenAI subscription token should be expired")

	require.Error(t, (OpenAISubscriptionConfig{RefreshToken: "refresh"}).Validate(), "missing OpenAI access token should fail validation")
	require.Error(t, (OpenAISubscriptionConfig{AccessToken: "access"}).Validate(), "missing OpenAI refresh token should fail validation")
}

func TestAnthropicSubscriptionConfigMetadata(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	valid := AnthropicSubscriptionConfig{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    future,
		AccountType:  "claude_max",
	}

	require.False(t, valid.IsExpired(), "future Anthropic subscription token should not be expired")
	require.True(t, valid.NeedsRefresh(2*time.Hour), "Anthropic subscription token inside refresh window should need refresh")
	require.Equal(t, ProviderAnthropicSubscription, valid.MaskedSummary().Provider, "Anthropic subscription summary should use subscription provider")
	require.Equal(t, "claude_max", valid.MaskedSummary().AccountType, "Anthropic subscription summary should preserve account type")

	expired := valid
	expired.ExpiresAt = past
	require.True(t, expired.IsExpired(), "past Anthropic subscription token should be expired")
}

func TestParseCodingProviderConfig(t *testing.T) {
	t.Parallel()
	t.Run("opencode", func(t *testing.T) {
		t.Parallel()

		original := OpenCodeConfig{
			APIKey:          "oc-key",
			BackingProvider: ProviderOpenAI,
			Model:           OpenCodeModelGPT54Mini,
		}
		data, err := json.Marshal(original)
		require.NoError(t, err, "test should marshal OpenCode config")

		got, err := ParseCodingProviderConfig(ProviderOpenCode, data)
		require.NoError(t, err, "ParseCodingProviderConfig should accept OpenCode coding credentials")
		cfg, ok := got.(OpenCodeConfig)
		require.True(t, ok, "parsed config should be OpenCodeConfig")
		require.Equal(t, original, cfg, "parsed OpenCode config should match expected")
	})

	t.Run("openai_subscription", func(t *testing.T) {
		t.Parallel()

		original := OpenAISubscriptionConfig{
			AccessToken:  "tok",
			RefreshToken: "rt",
			ExpiresAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			AccountType:  "pro",
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseCodingProviderConfig(ProviderOpenAISubscription, data)
		if err != nil {
			t.Fatal(err)
		}
		cfg, ok := got.(OpenAISubscriptionConfig)
		if !ok {
			t.Fatalf("expected OpenAISubscriptionConfig, got %T", got)
		}
		if cfg.AccessToken != "tok" || cfg.AccountType != "pro" {
			t.Fatalf("unexpected config: %+v", cfg)
		}
	})

	t.Run("anthropic_subscription", func(t *testing.T) {
		t.Parallel()

		original := AnthropicSubscriptionConfig{
			AccessToken:  "tok",
			RefreshToken: "rt",
			ExpiresAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			AccountType:  "claude_max",
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseCodingProviderConfig(ProviderAnthropicSubscription, data)
		if err != nil {
			t.Fatal(err)
		}
		cfg, ok := got.(AnthropicSubscriptionConfig)
		if !ok {
			t.Fatalf("expected AnthropicSubscriptionConfig, got %T", got)
		}
		if cfg.AccountType != "claude_max" {
			t.Fatalf("unexpected config: %+v", cfg)
		}
	})

	t.Run("anthropic api key still parses through coding allowlist", func(t *testing.T) {
		t.Parallel()

		// ProviderAnthropic is a coding provider (carries either an API key or
		// the embedded Subscription), so the strict path still accepts it.
		original := AnthropicConfig{APIKey: "sk-ant-1234567890"}
		data, _ := json.Marshal(original)
		got, err := ParseCodingProviderConfig(ProviderAnthropic, data)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(AnthropicConfig); !ok {
			t.Fatalf("expected AnthropicConfig, got %T", got)
		}
	})

	t.Run("rejects non-coding providers", func(t *testing.T) {
		t.Parallel()

		// coding_credentials must never carry GitHub/Sentry/Linear/etc rows —
		// those live in org_credentials. The unified table's CHECK is on
		// status only, so this allowlist is the only thing that catches a
		// stray non-coding INSERT at read time.
		nonCoding := []ProviderName{
			ProviderGitHubApp,
			ProviderGitHubAppUser,
			ProviderGitHubOAuth,
			ProviderSentry,
			ProviderLinear,
			ProviderSlack,
			ProviderNotion,
			// ProviderOpenAIChatGPT is renamed to ProviderOpenAISubscription
			// by the SQL data-copy migration, so it must never appear in a
			// coding_credentials row either.
			ProviderOpenAIChatGPT,
		}
		for _, p := range nonCoding {
			_, err := ParseCodingProviderConfig(p, []byte("{}"))
			if err == nil {
				t.Fatalf("ParseCodingProviderConfig should reject non-coding provider %q", p)
			}
		}
	})
}

func TestFromAnthropicSubscription(t *testing.T) {
	t.Parallel()
	src := AnthropicSubscription{
		AccessToken:   "a",
		RefreshToken:  "r",
		ExpiresAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AccountType:   "claude_max",
		RateLimitTier: "default_claude_max_20x",
		Scopes:        []string{"a", "b"},
		State:         "st",
		CodeVerifier:  "cv",
		AuthorizeURL:  "https://example",
	}
	got := FromAnthropicSubscription(src)
	if got.AccessToken != src.AccessToken ||
		got.RefreshToken != src.RefreshToken ||
		!got.ExpiresAt.Equal(src.ExpiresAt) ||
		got.AccountType != src.AccountType ||
		got.RateLimitTier != src.RateLimitTier ||
		len(got.Scopes) != 2 ||
		got.State != src.State ||
		got.CodeVerifier != src.CodeVerifier ||
		got.AuthorizeURL != src.AuthorizeURL {
		t.Fatalf("FromAnthropicSubscription mismatch: got %+v want %+v", got, src)
	}
}

func TestOpenAISubscriptionConfigRoundTrip(t *testing.T) {
	t.Parallel()
	src := OpenAIChatGPTConfig{
		AccessToken:  "a",
		RefreshToken: "r",
		IDToken:      "id",
		ExpiresAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AccountType:  "pro",
	}
	round := FromOpenAIChatGPTConfig(src).AsOpenAIChatGPTConfig()
	if round != src {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", round, src)
	}
}

func TestCreateCodingCredentialInputValidate(t *testing.T) {
	t.Parallel()
	validDefaults := map[string]string{"AMP_MODE": "deep"}
	cases := []struct {
		name string
		in   CreateCodingCredentialInput
		ok   bool
	}{
		{
			"ok personal api key",
			CreateCodingCredentialInput{
				Scope: CodingCredentialScopePersonal, Agent: AgentTypeCodex,
				AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1",
			},
			true,
		},
		{
			"ok org api key",
			CreateCodingCredentialInput{
				Scope: CodingCredentialScopeOrg, Agent: AgentTypeCodex,
				AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1",
			},
			true,
		},
		{
			"missing scope",
			CreateCodingCredentialInput{Agent: AgentTypeCodex, AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1"},
			false,
		},
		{
			"invalid scope",
			CreateCodingCredentialInput{Scope: "team", Agent: AgentTypeCodex, AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1"},
			false,
		},
		{
			"missing api key",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeCodex, AuthType: CodingAuthTypeAPIKey},
			false,
		},
		{
			"subscription rejected",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeCodex, AuthType: CodingAuthTypeSubscription},
			false,
		},
		{
			"invalid agent",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: "unknown", AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1"},
			false,
		},
		{
			"invalid auth type",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeCodex, AuthType: "magic", APIKey: "sk-1"},
			false,
		},
		{
			"valid amp defaults",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeAmp, AuthType: CodingAuthTypeAPIKey, APIKey: "amp", AgentDefaults: validDefaults},
			true,
		},
		{
			"valid opencode defaults",
			CreateCodingCredentialInput{
				Scope:    CodingCredentialScopeOrg,
				Agent:    AgentTypeOpenCode,
				AuthType: CodingAuthTypeAPIKey,
				APIKey:   "oc-key",
				AgentDefaults: map[string]string{
					"OPENCODE_MODEL":        "not-in-curated-list",
					"OPENCODE_MODEL_CUSTOM": "xai/grok-code-fast",
				},
			},
			true,
		},
		{
			"defaults reject unsupported agent",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeCodex, AuthType: CodingAuthTypeAPIKey, APIKey: "sk-1", AgentDefaults: validDefaults},
			false,
		},
		{
			"defaults reject invalid model",
			CreateCodingCredentialInput{Scope: CodingCredentialScopeOrg, Agent: AgentTypeAmp, AuthType: CodingAuthTypeAPIKey, APIKey: "amp", AgentDefaults: map[string]string{"AMP_MODE": "turbo"}},
			false,
		},
	}
	for _, tc := range cases {
		err := tc.in.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: expected ok, got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestMoveCodingCredentialInputValidateModel(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	tests := []struct {
		name string
		in   MoveCodingCredentialInput
		ok   bool
	}{
		{name: "before", in: MoveCodingCredentialInput{BeforeID: &id}, ok: true},
		{name: "after", in: MoveCodingCredentialInput{AfterID: &id}, ok: true},
		{name: "top", in: MoveCodingCredentialInput{ToTop: true}, ok: true},
		{name: "bottom", in: MoveCodingCredentialInput{ToBottom: true}, ok: true},
		{name: "none", in: MoveCodingCredentialInput{}, ok: false},
		{name: "multiple", in: MoveCodingCredentialInput{BeforeID: &id, ToBottom: true}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.in.Validate()
			if tt.ok {
				require.NoError(t, err, "valid move input should pass validation")
			} else {
				require.Error(t, err, "invalid move input should fail validation")
			}
		})
	}
}

func TestParseSubscriptionProviderConfigLegacyEntrypoint(t *testing.T) {
	t.Parallel()

	openAI, err := ParseProviderConfig(ProviderOpenAISubscription, mustMarshal(t, OpenAISubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh"}))
	require.NoError(t, err, "ParseProviderConfig should parse OpenAI subscription configs")
	require.IsType(t, OpenAISubscriptionConfig{}, openAI, "ParseProviderConfig should return OpenAISubscriptionConfig")

	anthropic, err := ParseProviderConfig(ProviderAnthropicSubscription, mustMarshal(t, AnthropicSubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh"}))
	require.NoError(t, err, "ParseProviderConfig should parse Anthropic subscription configs")
	require.IsType(t, AnthropicSubscriptionConfig{}, anthropic, "ParseProviderConfig should return AnthropicSubscriptionConfig")

	_, err = ParseProviderConfig(ProviderOpenAISubscription, []byte("{"))
	require.Error(t, err, "ParseProviderConfig should reject invalid OpenAI subscription JSON")
	_, err = ParseProviderConfig(ProviderAnthropicSubscription, []byte("{"))
	require.Error(t, err, "ParseProviderConfig should reject invalid Anthropic subscription JSON")
	_, err = ParseCodingProviderConfig(ProviderOpenAISubscription, []byte("{"))
	require.Error(t, err, "ParseCodingProviderConfig should reject invalid OpenAI subscription JSON")
	_, err = ParseCodingProviderConfig(ProviderAnthropicSubscription, []byte("{"))
	require.Error(t, err, "ParseCodingProviderConfig should reject invalid Anthropic subscription JSON")
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err, "test value should marshal")
	return data
}
