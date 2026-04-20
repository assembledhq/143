package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/llm"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // uses t.Setenv
func TestLoad_UsesDefaults(t *testing.T) {
	// Unset all vars that have defaults so env.Parse falls back to envDefault tags.
	// t.Setenv("FOO", "") sets the var to empty string. caarlos0/env treats empty
	// string the same as unset when the field has an envDefault, so defaults apply.
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("BASE_URL", "")
	t.Setenv("FRONTEND_URL", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("MODE", "")
	// Prevent .env files from interfering with defaults
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("OPENAI_API_TYPE", "")
	t.Setenv("OPENROUTER_APP_NAME", "")

	cfg := Load()

	require.Equal(t, 8080, cfg.Port, "Load should default to port 8080")
	require.Equal(t, "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", cfg.DatabaseURL, "Load should default the database URL")
	require.Equal(t, "info", cfg.LogLevel, "Load should default log level to info")
	require.Equal(t, "http://localhost:8080", cfg.BaseURL, "Load should default base URL")
	require.Equal(t, "http://localhost:8080", cfg.FrontendURL, "FrontendURL should default to BaseURL")
	require.Equal(t, []string{"http://localhost:8080"}, cfg.CORSAllowedOrigins, "CORS origins should default to FrontendURL")
	require.Equal(t, int64(0), cfg.GitHubAppID, "Load should default GitHub app ID to zero")
	require.Equal(t, "all", cfg.Mode, "Load should default mode to all")
	require.Equal(t, "chat", cfg.OpenAIAPIType, "Load should default OpenAI API type to chat")
	require.Equal(t, "143", cfg.OpenRouterAppName, "Load should default OpenRouter app name to 143")
}

//nolint:paralleltest // uses t.Setenv
func TestLoad_UsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DATABASE_URL", "postgres://custom")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("BASE_URL", "https://api.example.com")
	t.Setenv("FRONTEND_URL", "https://app.example.com")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://one.example.com,https://two.example.com")
	t.Setenv("MODE", "worker")
	t.Setenv("GITHUB_APP_ID", "12345")

	cfg := Load()

	require.Equal(t, 9090, cfg.Port, "Load should read PORT from the environment")
	require.Equal(t, "postgres://custom", cfg.DatabaseURL, "Load should read DATABASE_URL from the environment")
	require.Equal(t, "debug", cfg.LogLevel, "Load should read LOG_LEVEL from the environment")
	require.Equal(t, "https://api.example.com", cfg.BaseURL, "Load should read BASE_URL from the environment")
	require.Equal(t, "https://app.example.com", cfg.FrontendURL, "Load should read FRONTEND_URL from the environment")
	require.Equal(t, []string{"https://one.example.com", "https://two.example.com"}, cfg.CORSAllowedOrigins, "Load should split CORS origins from the environment")
	require.Equal(t, int64(12345), cfg.GitHubAppID, "Load should parse GITHUB_APP_ID from the environment")
	require.Equal(t, "worker", cfg.Mode, "Load should read MODE from the environment")
}

//nolint:paralleltest // uses t.Setenv
func TestLoad_LLMConfig(t *testing.T) {
	t.Setenv("LLM_MODEL", "claude-sonnet-4-5")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cfg := Load()
	llmCfg := cfg.LLMConfig()

	require.Equal(t, llm.ModelName("claude-sonnet-4-5"), llmCfg.Model)
	require.Equal(t, "sk-ant-test", llmCfg.AnthropicAPIKey)
	require.Equal(t, "sk-or-test", llmCfg.OpenRouterAPIKey)
	require.Equal(t, "chat", llmCfg.OpenAIAPIType, "OpenAI API type should default to chat")
}

//nolint:paralleltest // uses t.Setenv
func TestLogStatus_AllConfigured(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "gh-id")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "gh-secret")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "go-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "go-secret")
	t.Setenv("GITHUB_APP_ID", "123")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "key")
	t.Setenv("ENCRYPTION_MASTER_KEY", "master-key")
	t.Setenv("LLM_MODEL", "claude-sonnet-4-5")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("SESSION_SECRET", "secret")

	cfg := Load()
	// LogStatus should not panic.
	require.NotPanics(t, func() {
		cfg.LogStatus(zerolog.Nop())
	})
}

//nolint:paralleltest // uses t.Setenv
func TestLogStatus_NothingConfigured(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("GITHUB_APP_ID", "0")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("ENCRYPTION_MASTER_KEY", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("SESSION_SECRET", "")

	cfg := Load()
	require.NotPanics(t, func() {
		cfg.LogStatus(zerolog.Nop())
	})
}

//nolint:paralleltest // uses t.Setenv
func TestLogStatus_LLMModelWithoutProviders(t *testing.T) {
	t.Setenv("LLM_MODEL", "claude-sonnet-4-5")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("SESSION_SECRET", "test-secret")

	cfg := Load()
	require.NotPanics(t, func() {
		cfg.LogStatus(zerolog.Nop())
	})
}

//nolint:paralleltest // uses t.Setenv
func TestSafeLLMEnv_MasksAPIKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api3-abcdef1234567890")
	t.Setenv("OPENAI_API_KEY", "sk-proj-abcdefgh1234")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-abcdefgh5678")

	cfg := Load()
	safe := cfg.SafeLLMEnv()

	require.Len(t, safe, 3, "should include all three providers")
	require.Equal(t, "sk-a••••7890", safe["anthropic"])
	require.Equal(t, "sk-p••••1234", safe["openai"])
	require.Equal(t, "sk-o••••5678", safe["openrouter"])
}

//nolint:paralleltest // uses t.Setenv
func TestSafeLLMEnv_Empty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	cfg := Load()
	safe := cfg.SafeLLMEnv()

	require.Empty(t, safe, "should return empty map when no keys configured")
}

//nolint:paralleltest // uses t.Setenv
func TestSafeLLMEnv_PartialKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api3-abcdef1234567890")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	cfg := Load()
	safe := cfg.SafeLLMEnv()

	require.Len(t, safe, 1, "should only include configured providers")
	require.Contains(t, safe, "anthropic")
	require.NotContains(t, safe, "openai")
	require.NotContains(t, safe, "openrouter")
}

func TestValidateSecrets_DevelopmentAllowsMissing(t *testing.T) {
	t.Parallel()

	cfg := &Config{Env: "development"}
	require.NoError(t, cfg.ValidateSecrets(), "development env should allow missing secrets")
}

func TestValidateSecrets_ProductionMissingSessionSecret(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       "",
		EncryptionMasterKey: strings.Repeat("k", 32),
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "missing SessionSecret in production should error")
	require.Contains(t, err.Error(), "SESSION_SECRET")
}

func TestValidateSecrets_ProductionWeakSessionSecret(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       "changeme",
		EncryptionMasterKey: strings.Repeat("k", 32),
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "weak SessionSecret in production should error")
	require.Contains(t, err.Error(), "SESSION_SECRET")
}

func TestValidateSecrets_ProductionShortSessionSecret(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       "short",
		EncryptionMasterKey: strings.Repeat("k", 32),
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "short SessionSecret in production should error")
	require.Contains(t, err.Error(), "SESSION_SECRET")
}

func TestValidateSecrets_ProductionMissingEncryptionKey(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       strings.Repeat("s", 32),
		EncryptionMasterKey: "",
		CSRFSigningKey:      strings.Repeat("c", 32),
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "missing EncryptionMasterKey in production should error")
	require.Contains(t, err.Error(), "ENCRYPTION_MASTER_KEY")
}

func TestValidateSecrets_ProductionShortEncryptionKey(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       strings.Repeat("s", 32),
		EncryptionMasterKey: "short",
		CSRFSigningKey:      strings.Repeat("c", 32),
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "short EncryptionMasterKey in production should error")
	require.Contains(t, err.Error(), "ENCRYPTION_MASTER_KEY")
}

func TestValidateSecrets_ProductionAllValid(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       strings.Repeat("s", 32),
		EncryptionMasterKey: strings.Repeat("k", 32),
		CSRFSigningKey:      strings.Repeat("c", 32),
	}
	require.NoError(t, cfg.ValidateSecrets(), "valid production config should not error")
}

func TestValidateSecrets_NegativeRetentionDays(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                      "development",
		DataRetentionWebhookDays: -1,
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "negative retention days should error")
	require.Contains(t, err.Error(), "DATA_RETENTION")
}

func TestValidateSecrets_ProductionMissingCSRFKey(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env:                 "production",
		SessionSecret:       strings.Repeat("s", 32),
		EncryptionMasterKey: strings.Repeat("k", 32),
		CSRFSigningKey:      "",
	}
	err := cfg.ValidateSecrets()
	require.Error(t, err, "missing CSRF_SIGNING_KEY in production should error")
	require.Contains(t, err.Error(), "CSRF_SIGNING_KEY")
}

func TestGitHubAppEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "fully configured",
			cfg:  Config{GitHubAppID: 123, GitHubAppPrivateKey: "key"},
			want: true,
		},
		{
			name: "missing app id",
			cfg:  Config{GitHubAppPrivateKey: "key"},
			want: false,
		},
		{
			name: "missing private key",
			cfg:  Config{GitHubAppID: 123},
			want: false,
		},
		{
			name: "demo mode disables even with credentials",
			cfg:  Config{GitHubAppID: 123, GitHubAppPrivateKey: "key", DemoMode: true},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.cfg.GitHubAppEnabled())
		})
	}
}

func TestLogStatus_DemoModeWarns(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	cfg := &Config{
		SessionSecret:  "s",
		CSRFSigningKey: "c",
		DemoMode:       true,
	}
	cfg.LogStatus(logger)

	require.Contains(t, buf.String(), "DEMO_MODE is enabled", "LogStatus should warn when DemoMode is set")
}

func TestLogStatus_PreviewOriginTemplateLocalhostInProduction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		wantWarn bool
	}{
		{
			name: "production + localhost template warns",
			cfg: Config{
				Env:                   "production",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "http://{id}.preview.localhost:9090",
			},
			wantWarn: true,
		},
		{
			name: "production + custom localhost variant warns",
			cfg: Config{
				Env:                   "production",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "http://localhost:8080/{id}",
			},
			wantWarn: true,
		},
		{
			name: "production + real host does not warn",
			cfg: Config{
				Env:                   "production",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "https://{id}.preview.example.com",
			},
			wantWarn: false,
		},
		{
			name: "development + localhost does not warn",
			cfg: Config{
				Env:                   "development",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "http://{id}.preview.localhost:9090",
			},
			wantWarn: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			tc.cfg.LogStatus(zerolog.New(&buf))

			if tc.wantWarn {
				require.Contains(t, buf.String(), "PREVIEW_ORIGIN_TEMPLATE points at localhost")
			} else {
				require.NotContains(t, buf.String(), "PREVIEW_ORIGIN_TEMPLATE points at localhost")
			}
		})
	}
}
