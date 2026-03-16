package models

import (
	"testing"
	"time"

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
		{"gemini is valid", ProviderGemini, true},
		{"openrouter is valid", ProviderOpenRouter, true},
		{"github_app is valid", ProviderGitHubApp, true},
		{"github_oauth is valid", ProviderGitHubOAuth, true},
		{"sentry is valid", ProviderSentry, true},
		{"linear is valid", ProviderLinear, true},
		{"slack is valid", ProviderSlack, true},
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

func TestParseProviderConfig_Gemini(t *testing.T) {
	t.Parallel()

	input := `{"api_key":"gm-test-key","model":"gemini-2.5-pro"}`
	cfg, err := ParseProviderConfig(ProviderGemini, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	gc, ok := cfg.(GeminiConfig)
	require.True(t, ok, "config should be GeminiConfig")
	require.Equal(t, "gm-test-key", gc.APIKey, "should parse api_key")
	require.Equal(t, "gemini-2.5-pro", gc.Model, "should parse model")
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
		{"GeminiConfig", GeminiConfig{}, ProviderGemini},
		{"OpenRouterConfig", OpenRouterConfig{}, ProviderOpenRouter},
		{"GitHubAppConfig", GitHubAppConfig{}, ProviderGitHubApp},
		{"GitHubOAuthConfig", GitHubOAuthConfig{}, ProviderGitHubOAuth},
		{"SentryConfig", SentryConfig{}, ProviderSentry},
		{"LinearConfig", LinearConfig{}, ProviderLinear},
		{"SlackConfig", SlackConfig{}, ProviderSlack},
		{"OpenAIChatGPTConfig", OpenAIChatGPTConfig{}, ProviderOpenAIChatGPT},
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
		{"gemini valid", GeminiConfig{APIKey: "gm-test-key", Model: "gemini-2.5-pro"}, false},
		{"gemini empty key", GeminiConfig{APIKey: ""}, true},
		{"github_app valid", GitHubAppConfig{AppID: 123, PrivateKey: "key"}, false},
		{"github_app missing app_id", GitHubAppConfig{AppID: 0, PrivateKey: "key"}, true},
		{"github_app missing private_key", GitHubAppConfig{AppID: 123, PrivateKey: ""}, true},
		{"github_oauth valid client creds", GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret"}, false},
		{"github_oauth valid access token", GitHubOAuthConfig{AccessToken: "gh-token"}, false},
		{"github_oauth missing client_id", GitHubOAuthConfig{ClientID: "", ClientSecret: "secret"}, true},
		{"github_oauth missing client_secret", GitHubOAuthConfig{ClientID: "id", ClientSecret: ""}, true},
		{"github_oauth empty", GitHubOAuthConfig{}, true},
		{"sentry webhook valid", SentryConfig{WebhookSecret: "secret"}, false},
		{"sentry oauth valid", SentryConfig{AccessToken: "sentry-token"}, false},
		{"sentry empty", SentryConfig{}, true},
		{"linear valid", LinearConfig{WebhookSecret: "secret"}, false},
		{"linear oauth valid", LinearConfig{AccessToken: "lin-token"}, false},
		{"linear empty", LinearConfig{WebhookSecret: ""}, true},
		{"slack valid", SlackConfig{AccessToken: "xoxb-test-token"}, false},
		{"slack missing access_token", SlackConfig{AccessToken: ""}, true},
		{"openai_chatgpt valid", OpenAIChatGPTConfig{AccessToken: "cha_tok", RefreshToken: "chr_tok"}, false},
		{"openai_chatgpt missing access_token", OpenAIChatGPTConfig{AccessToken: "", RefreshToken: "chr_tok"}, true},
		{"openai_chatgpt missing refresh_token", OpenAIChatGPTConfig{AccessToken: "cha_tok", RefreshToken: ""}, true},
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

func TestMaskedSummary_Gemini(t *testing.T) {
	t.Parallel()

	cfg := GeminiConfig{APIKey: "gm-test-key-12345"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderGemini, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Equal(t, "gm-tes...2345", summary.MaskedKey, "summary should mask api key")
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

func TestMaskedSummary_OpenAIChatGPT(t *testing.T) {
	t.Parallel()

	cfg := OpenAIChatGPTConfig{AccessToken: "cha_test_access_token_12345", AccountType: "plus"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderOpenAIChatGPT, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Equal(t, "cha_te...2345", summary.MaskedKey, "summary should mask access token")
	require.Equal(t, "plus", summary.AccountType, "summary should include account type")
}

func TestOpenAIChatGPTConfig_IsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      OpenAIChatGPTConfig
		expected bool
	}{
		{"expired token", OpenAIChatGPTConfig{ExpiresAt: time.Now().Add(-1 * time.Hour)}, true},
		{"valid token", OpenAIChatGPTConfig{ExpiresAt: time.Now().Add(1 * time.Hour)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.cfg.IsExpired())
		})
	}
}

func TestOpenAIChatGPTConfig_NeedsRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      OpenAIChatGPTConfig
		window   time.Duration
		expected bool
	}{
		{"expires within window", OpenAIChatGPTConfig{ExpiresAt: time.Now().Add(2 * time.Minute)}, 5 * time.Minute, true},
		{"expires outside window", OpenAIChatGPTConfig{ExpiresAt: time.Now().Add(1 * time.Hour)}, 5 * time.Minute, false},
		{"already expired", OpenAIChatGPTConfig{ExpiresAt: time.Now().Add(-1 * time.Minute)}, 5 * time.Minute, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.cfg.NeedsRefresh(tt.window))
		})
	}
}

func TestParseProviderConfig_OpenAIChatGPT(t *testing.T) {
	t.Parallel()

	input := `{"access_token":"cha_tok","refresh_token":"chr_tok","account_type":"plus"}`
	cfg, err := ParseProviderConfig(ProviderOpenAIChatGPT, []byte(input))
	require.NoError(t, err)

	chatCfg, ok := cfg.(OpenAIChatGPTConfig)
	require.True(t, ok, "config should be OpenAIChatGPTConfig")
	require.Equal(t, "cha_tok", chatCfg.AccessToken)
	require.Equal(t, "chr_tok", chatCfg.RefreshToken)
	require.Equal(t, "plus", chatCfg.AccountType)
}

func TestParseProviderConfig_OpenAIChatGPT_Invalid(t *testing.T) {
	t.Parallel()

	_, err := ParseProviderConfig(ProviderOpenAIChatGPT, []byte(`{bad json`))
	require.Error(t, err)
}

func TestMaskedSummary_Slack(t *testing.T) {
	t.Parallel()

	cfg := SlackConfig{AccessToken: "xoxb-test-token"}
	summary := cfg.MaskedSummary()

	require.Equal(t, ProviderSlack, summary.Provider, "summary should have correct provider")
	require.True(t, summary.Configured, "summary should be configured")
	require.Empty(t, summary.MaskedKey, "slack summary should not include masked key")
}

func TestParseProviderConfig_Slack(t *testing.T) {
	t.Parallel()

	input := `{"access_token":"xoxb-test-token","team_id":"T123","team_name":"Test Team","scope":"channels:read","channel_ids":["C1","C2"]}`
	cfg, err := ParseProviderConfig(ProviderSlack, []byte(input))
	require.NoError(t, err, "ParseProviderConfig should not return an error")

	sc, ok := cfg.(SlackConfig)
	require.True(t, ok, "config should be SlackConfig")
	require.Equal(t, "xoxb-test-token", sc.AccessToken, "should parse access_token")
	require.Equal(t, "T123", sc.TeamID, "should parse team_id")
	require.Equal(t, "Test Team", sc.TeamName, "should parse team_name")
	require.Equal(t, "channels:read", sc.Scope, "should parse scope")
	require.Equal(t, []string{"C1", "C2"}, sc.ChannelIDs, "should parse channel_ids")
}

func TestParseProviderConfig_Slack_Invalid(t *testing.T) {
	t.Parallel()

	_, err := ParseProviderConfig(ProviderSlack, []byte(`{bad json`))
	require.Error(t, err)
}

func TestIsCodingAgentProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider ProviderName
		expected bool
	}{
		{"anthropic is coding agent", ProviderAnthropic, true},
		{"openai is coding agent", ProviderOpenAI, true},
		{"gemini is coding agent", ProviderGemini, true},
		{"openrouter is coding agent", ProviderOpenRouter, true},
		{"github_app is not coding agent", ProviderGitHubApp, false},
		{"github_oauth is not coding agent", ProviderGitHubOAuth, false},
		{"sentry is not coding agent", ProviderSentry, false},
		{"linear is not coding agent", ProviderLinear, false},
		{"slack is not coding agent", ProviderSlack, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.provider.IsCodingAgentProvider(), "IsCodingAgentProvider should return expected result")
		})
	}
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
		{"gemini is not LLM", ProviderGemini, false},
		{"github_app is not LLM", ProviderGitHubApp, false},
		{"github_oauth is not LLM", ProviderGitHubOAuth, false},
		{"sentry is not LLM", ProviderSentry, false},
		{"linear is not LLM", ProviderLinear, false},
		{"slack is not LLM", ProviderSlack, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.provider.IsLLMProvider(), "IsLLMProvider should return expected result")
		})
	}
}
