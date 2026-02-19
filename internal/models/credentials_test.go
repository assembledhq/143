package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderName_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider ProviderName
		expected bool
	}{
		{"anthropic is valid", ProviderAnthropic, true},
		{"openai is valid", ProviderOpenAI, true},
		{"openrouter is valid", ProviderOpenRouter, true},
		{"github_app is valid", ProviderGitHubApp, true},
		{"github_oauth is valid", ProviderGitHubOAuth, true},
		{"sentry is valid", ProviderSentry, true},
		{"linear is valid", ProviderLinear, true},
		{"unknown is invalid", ProviderName("unknown"), false},
		{"empty is invalid", ProviderName(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.provider.Valid(), "Valid() should return expected result for %q", tt.provider)
		})
	}
}

func TestParseProviderConfig_Anthropic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  AnthropicConfig
		expectErr bool
	}{
		{
			name:  "full config",
			input: `{"api_key":"sk-ant-test","base_url":"https://custom.api.com"}`,
			expected: AnthropicConfig{
				APIKey:  "sk-ant-test",
				BaseURL: "https://custom.api.com",
			},
		},
		{
			name:  "key only",
			input: `{"api_key":"sk-ant-test"}`,
			expected: AnthropicConfig{
				APIKey: "sk-ant-test",
			},
		},
		{
			name:      "invalid json",
			input:     `{bad json`,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := ParseProviderConfig(ProviderAnthropic, []byte(tt.input))
			if tt.expectErr {
				require.Error(t, err, "ParseProviderConfig should return an error")
				return
			}
			require.NoError(t, err, "ParseProviderConfig should not return an error")
			ac, ok := cfg.(AnthropicConfig)
			require.True(t, ok, "config should be AnthropicConfig")
			require.Equal(t, tt.expected, ac, "parsed config should match expected")
		})
	}
}

func TestParseProviderConfig_OpenAI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected OpenAIConfig
	}{
		{
			name:  "chat type",
			input: `{"api_key":"sk-test","api_type":"chat"}`,
			expected: OpenAIConfig{
				APIKey:  "sk-test",
				APIType: "chat",
			},
		},
		{
			name:  "responses type",
			input: `{"api_key":"sk-test","api_type":"responses"}`,
			expected: OpenAIConfig{
				APIKey:  "sk-test",
				APIType: "responses",
			},
		},
		{
			name:  "defaults to chat when empty",
			input: `{"api_key":"sk-test"}`,
			expected: OpenAIConfig{
				APIKey:  "sk-test",
				APIType: "chat",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := ParseProviderConfig(ProviderOpenAI, []byte(tt.input))
			require.NoError(t, err, "ParseProviderConfig should not return an error")
			oc, ok := cfg.(OpenAIConfig)
			require.True(t, ok, "config should be OpenAIConfig")
			require.Equal(t, tt.expected, oc, "parsed config should match expected")
		})
	}
}

func TestParseProviderConfig_OpenRouter(t *testing.T) {
	t.Parallel()

	input := `{"api_key":"sk-or-test","app_name":"myapp","site_url":"https://example.com"}`
	cfg, err := ParseProviderConfig(ProviderOpenRouter, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	orc, ok := cfg.(OpenRouterConfig)
	require.True(t, ok, "config should be OpenRouterConfig")
	require.Equal(t, "sk-or-test", orc.APIKey, "should parse api_key")
	require.Equal(t, "myapp", orc.AppName, "should parse app_name")
	require.Equal(t, "https://example.com", orc.SiteURL, "should parse site_url")
}

func TestParseProviderConfig_GitHubApp(t *testing.T) {
	t.Parallel()

	input := `{"app_id":12345,"private_key":"-----BEGIN RSA-----","webhook_secret":"whsec_test"}`
	cfg, err := ParseProviderConfig(ProviderGitHubApp, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	ghc, ok := cfg.(GitHubAppConfig)
	require.True(t, ok, "config should be GitHubAppConfig")
	require.Equal(t, int64(12345), ghc.AppID, "should parse app_id")
	require.Equal(t, "-----BEGIN RSA-----", ghc.PrivateKey, "should parse private_key")
	require.Equal(t, "whsec_test", ghc.WebhookSecret, "should parse webhook_secret")
}

func TestParseProviderConfig_GitHubOAuth(t *testing.T) {
	t.Parallel()

	input := `{"client_id":"Iv1_test","client_secret":"secret_test"}`
	cfg, err := ParseProviderConfig(ProviderGitHubOAuth, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	ghoc, ok := cfg.(GitHubOAuthConfig)
	require.True(t, ok, "config should be GitHubOAuthConfig")
	require.Equal(t, "Iv1_test", ghoc.ClientID, "should parse client_id")
	require.Equal(t, "secret_test", ghoc.ClientSecret, "should parse client_secret")
}

func TestParseProviderConfig_Sentry(t *testing.T) {
	t.Parallel()

	input := `{"webhook_secret":"sentry_secret"}`
	cfg, err := ParseProviderConfig(ProviderSentry, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	sc, ok := cfg.(SentryConfig)
	require.True(t, ok, "config should be SentryConfig")
	require.Equal(t, "sentry_secret", sc.WebhookSecret, "should parse webhook_secret")
}

func TestParseProviderConfig_Linear(t *testing.T) {
	t.Parallel()

	input := `{"webhook_secret":"linear_secret"}`
	cfg, err := ParseProviderConfig(ProviderLinear, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	lc, ok := cfg.(LinearConfig)
	require.True(t, ok, "config should be LinearConfig")
	require.Equal(t, "linear_secret", lc.WebhookSecret, "should parse webhook_secret")
}

func TestParseProviderConfig_UnknownProvider(t *testing.T) {
	t.Parallel()

	_, err := ParseProviderConfig(ProviderName("unknown"), []byte(`{}`))
	require.Error(t, err, "ParseProviderConfig should return an error for unknown provider")
	require.Contains(t, err.Error(), "unknown provider", "error should mention unknown provider")
}

func TestProviderConfig_Provider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      ProviderConfig
		expected ProviderName
	}{
		{"AnthropicConfig", AnthropicConfig{}, ProviderAnthropic},
		{"OpenAIConfig", OpenAIConfig{}, ProviderOpenAI},
		{"OpenRouterConfig", OpenRouterConfig{}, ProviderOpenRouter},
		{"GitHubAppConfig", GitHubAppConfig{}, ProviderGitHubApp},
		{"GitHubOAuthConfig", GitHubOAuthConfig{}, ProviderGitHubOAuth},
		{"SentryConfig", SentryConfig{}, ProviderSentry},
		{"LinearConfig", LinearConfig{}, ProviderLinear},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.cfg.Provider(), "Provider() should return the correct ProviderName")
		})
	}
}

func TestProviderConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       ProviderConfig
		expectErr bool
	}{
		{"anthropic valid", AnthropicConfig{APIKey: "sk-ant-test"}, false},
		{"anthropic empty key", AnthropicConfig{APIKey: ""}, true},
		{"openai valid", OpenAIConfig{APIKey: "sk-test", APIType: "chat"}, false},
		{"openai empty key", OpenAIConfig{APIKey: ""}, true},
		{"openrouter valid", OpenRouterConfig{APIKey: "sk-or-test"}, false},
		{"openrouter empty key", OpenRouterConfig{APIKey: ""}, true},
		{"github_app valid", GitHubAppConfig{AppID: 123, PrivateKey: "key"}, false},
		{"github_app missing app_id", GitHubAppConfig{AppID: 0, PrivateKey: "key"}, true},
		{"github_app missing private_key", GitHubAppConfig{AppID: 123, PrivateKey: ""}, true},
		{"github_oauth valid", GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret"}, false},
		{"github_oauth missing client_id", GitHubOAuthConfig{ClientID: "", ClientSecret: "secret"}, true},
		{"github_oauth missing client_secret", GitHubOAuthConfig{ClientID: "id", ClientSecret: ""}, true},
		{"sentry valid", SentryConfig{WebhookSecret: "secret"}, false},
		{"sentry empty", SentryConfig{WebhookSecret: ""}, true},
		{"linear valid", LinearConfig{WebhookSecret: "secret"}, false},
		{"linear empty", LinearConfig{WebhookSecret: ""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should return an error")
			} else {
				require.NoError(t, err, "Validate should not return an error")
			}
		})
	}
}

func TestMaskKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"long key", "sk-ant-api03-abcdefghij", "sk-ant...ghij"},
		{"short key", "abc", "****"},
		{"exactly 12 chars", "123456789012", "****"},
		{"13 chars", "1234567890123", "123456...0123"},
		{"empty key", "", "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, MaskKey(tt.input), "MaskKey should produce expected output")
		})
	}
}

func TestMaskedSummary_Anthropic(t *testing.T) {
	t.Parallel()

	cfg := AnthropicConfig{APIKey: "sk-ant-api03-testkey123"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderAnthropic, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Equal(t, "sk-ant...y123", summary.MaskedKey, "summary should have masked key")
}

func TestMaskedSummary_OpenAI(t *testing.T) {
	t.Parallel()

	cfg := OpenAIConfig{APIKey: "sk-proj-testkey456", APIType: "responses"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderOpenAI, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Equal(t, "responses", summary.APIType, "summary should include api_type")
}

func TestMaskedSummary_OpenRouter(t *testing.T) {
	t.Parallel()

	cfg := OpenRouterConfig{APIKey: "sk-or-v1-testkey789", AppName: "143"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderOpenRouter, summary.Provider, "summary should have correct provider")
	require.Equal(t, "143", summary.AppName, "summary should include app_name")
}

func TestMaskedSummary_GitHubApp(t *testing.T) {
	t.Parallel()

	cfg := GitHubAppConfig{AppID: 12345, PrivateKey: "key", WebhookSecret: "secret"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderGitHubApp, summary.Provider, "summary should have correct provider")
	require.Equal(t, int64(12345), summary.AppID, "summary should include app_id")
	require.Empty(t, summary.MaskedKey, "summary should not include masked key for github app")
}

func TestMaskedSummary_GitHubOAuth(t *testing.T) {
	t.Parallel()

	cfg := GitHubOAuthConfig{ClientID: "Iv1_abcdefghij", ClientSecret: "secret"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderGitHubOAuth, summary.Provider, "summary should have correct provider")
	require.Equal(t, "Iv1_ab...ghij", summary.MaskedKey, "summary should mask client_id")
}

func TestMaskedSummary_Sentry(t *testing.T) {
	t.Parallel()

	cfg := SentryConfig{WebhookSecret: "secret"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderSentry, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Empty(t, summary.MaskedKey, "sentry summary should not include masked key")
}

func TestMaskedSummary_Linear(t *testing.T) {
	t.Parallel()

	cfg := LinearConfig{WebhookSecret: "secret"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderLinear, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Empty(t, summary.MaskedKey, "linear summary should not include masked key")
}

func TestIsLLMProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider ProviderName
		expected bool
	}{
		{"anthropic is LLM", ProviderAnthropic, true},
		{"openai is LLM", ProviderOpenAI, true},
		{"openrouter is LLM", ProviderOpenRouter, true},
		{"github_app is not LLM", ProviderGitHubApp, false},
		{"github_oauth is not LLM", ProviderGitHubOAuth, false},
		{"sentry is not LLM", ProviderSentry, false},
		{"linear is not LLM", ProviderLinear, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.provider.IsLLMProvider(), "IsLLMProvider should return expected result")
		})
	}
}
