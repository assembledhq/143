package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestScopeLabel(t *testing.T) {
	t.Parallel()
	user := uuid.New()
	cases := []struct {
		name  string
		scope Scope
		want  string
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

func TestParseCodingProviderConfig(t *testing.T) {
	t.Parallel()
	t.Run("openai_subscription", func(t *testing.T) {
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

	t.Run("falls back to ParseProviderConfig for non-coding", func(t *testing.T) {
		// Anthropic API key path through the legacy struct still works via fallback.
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
