package config

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
	t.Setenv("DATABASE_MAX_CONNS", "")
	t.Setenv("DATABASE_MAX_CONN_IDLE_TIME", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("BASE_URL", "")
	t.Setenv("FRONTEND_URL", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("MODE", "")
	t.Setenv("SANDBOX_HEALTH_CHECK_IMAGE", "")
	t.Setenv("PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR", "")
	// Prevent .env files from interfering with defaults
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("OPENAI_API_TYPE", "")
	t.Setenv("OPENROUTER_APP_NAME", "")

	cfg := Load()

	require.Equal(t, 8080, cfg.Port, "Load should default to port 8080")
	require.Equal(t, "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", cfg.DatabaseURL, "Load should default the database URL")
	require.Equal(t, int32(0), cfg.DatabaseMaxConns, "Load should default database max connections to pgxpool defaults")
	require.Equal(t, time.Duration(0), cfg.DatabaseMaxConnIdleTime, "Load should default database idle timeout to pgxpool defaults")
	require.Equal(t, "info", cfg.LogLevel, "Load should default log level to info")
	require.Equal(t, "http://localhost:8080", cfg.BaseURL, "Load should default base URL")
	require.Equal(t, "http://localhost:8080", cfg.FrontendURL, "FrontendURL should default to BaseURL")
	require.Equal(t, []string{"http://localhost:8080"}, cfg.CORSAllowedOrigins, "CORS origins should default to FrontendURL")
	require.Equal(t, int64(0), cfg.GitHubAppID, "Load should default GitHub app ID to zero")
	require.Equal(t, "all", cfg.Mode, "Load should default mode to all")
	require.Equal(t, 2, cfg.WorkerProcessCount, "Load should default worker process count to 2")
	require.Equal(t, 0, cfg.WorkerMaxActiveSandboxes, "Load should default worker max active sandboxes to derived mode")
	require.Equal(t, 2*time.Hour, cfg.WorkerPreviewDrainTimeout, "Load should default worker preview drain timeout to two hours")
	require.Equal(t, "chat", cfg.OpenAIAPIType, "Load should default OpenAI API type to chat")
	require.Equal(t, "143", cfg.OpenRouterAppName, "Load should default OpenRouter app name to 143")
	require.Equal(t, "busybox:1.36.1", cfg.SandboxHealthCheckImage, "Load should default the sandbox health-check image to a pinned busybox tag")
	require.Equal(t, "/var/cache/143/preview-dependency-cache", cfg.PreviewDependencyCacheLocalDir, "Load should default dependency cache local L1 storage to the production worker host cache path")
}

func TestResolvePreviewDependencyCacheLocalDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "keeps configured path",
			value: "/mnt/fast-cache/preview-dependency-cache",
			want:  "/mnt/fast-cache/preview-dependency-cache",
		},
		{
			name:  "trims configured path",
			value: "  /mnt/fast-cache/preview-dependency-cache  ",
			want:  "/mnt/fast-cache/preview-dependency-cache",
		},
		{
			name:  "off disables local cache",
			value: "off",
			want:  "",
		},
		{
			name:  "disabled disables local cache",
			value: "DISABLED",
			want:  "",
		},
		{
			name:  "none disables local cache",
			value: " none ",
			want:  "",
		},
		{
			name:  "blank disables local cache after explicit normalization",
			value: "   ",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ResolvePreviewDependencyCacheLocalDir(tt.value)

			require.Equal(t, tt.want, got, "local dependency cache path should normalize opt-out sentinels and configured paths")
		})
	}
}

//nolint:paralleltest // uses t.Setenv
func TestLoad_UsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DATABASE_URL", "postgres://custom")
	t.Setenv("DATABASE_MAX_CONNS", "12")
	t.Setenv("DATABASE_MAX_CONN_IDLE_TIME", "5m")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("BASE_URL", "https://api.example.com")
	t.Setenv("FRONTEND_URL", "https://app.example.com")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://one.example.com,https://two.example.com")
	t.Setenv("MODE", "worker")
	t.Setenv("WORKER_PROCESS_COUNT", "4")
	t.Setenv("WORKER_MAX_ACTIVE_SANDBOXES", "7")
	t.Setenv("WORKER_PREVIEW_DRAIN_TIMEOUT", "90m")
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("SANDBOX_HEALTH_CHECK_IMAGE", "registry.example.com/health/busybox:1.36.1")

	cfg := Load()

	require.Equal(t, 9090, cfg.Port, "Load should read PORT from the environment")
	require.Equal(t, "postgres://custom", cfg.DatabaseURL, "Load should read DATABASE_URL from the environment")
	require.Equal(t, int32(12), cfg.DatabaseMaxConns, "Load should parse DATABASE_MAX_CONNS from the environment")
	require.Equal(t, 5*time.Minute, cfg.DatabaseMaxConnIdleTime, "Load should parse DATABASE_MAX_CONN_IDLE_TIME from the environment")
	require.Equal(t, "debug", cfg.LogLevel, "Load should read LOG_LEVEL from the environment")
	require.Equal(t, "https://api.example.com", cfg.BaseURL, "Load should read BASE_URL from the environment")
	require.Equal(t, "https://app.example.com", cfg.FrontendURL, "Load should read FRONTEND_URL from the environment")
	require.Equal(t, []string{"https://one.example.com", "https://two.example.com"}, cfg.CORSAllowedOrigins, "Load should split CORS origins from the environment")
	require.Equal(t, int64(12345), cfg.GitHubAppID, "Load should parse GITHUB_APP_ID from the environment")
	require.Equal(t, "worker", cfg.Mode, "Load should read MODE from the environment")
	require.Equal(t, 4, cfg.WorkerProcessCount, "Load should parse WORKER_PROCESS_COUNT from the environment")
	require.Equal(t, 7, cfg.WorkerMaxActiveSandboxes, "Load should parse WORKER_MAX_ACTIVE_SANDBOXES from the environment")
	require.Equal(t, 90*time.Minute, cfg.WorkerPreviewDrainTimeout, "Load should parse WORKER_PREVIEW_DRAIN_TIMEOUT from the environment")
	require.Equal(t, "registry.example.com/health/busybox:1.36.1", cfg.SandboxHealthCheckImage, "Load should read SANDBOX_HEALTH_CHECK_IMAGE from the environment")
}

//nolint:paralleltest // uses t.Setenv
func TestLoad_PreviewSecretBundleKEKVersion(t *testing.T) {
	t.Setenv("PREVIEW_SECRET_BUNDLE_KEK_VERSION", "preview-secrets-2026-05")

	cfg := Load()

	require.Equal(t, "preview-secrets-2026-05", cfg.PreviewSecretBundleKEKVersion, "Load should read PREVIEW_SECRET_BUNDLE_KEK_VERSION from the environment")
}

//nolint:paralleltest // uses t.Setenv
func TestLoad_DerivesStandaloneRedisURL(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("REDIS_TOPOLOGY", "standalone")
	t.Setenv("REDIS_PRIVATE_IP", "10.0.0.50")
	t.Setenv("REDIS_PASSWORD", "secret")

	cfg := Load()

	require.Equal(t, "redis://:secret@10.0.0.50:6379/0", cfg.RedisURL, "Load should derive the standalone Redis URL from private IP and password")
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
func TestLoad_SnapshotS3Config(t *testing.T) {
	t.Setenv("SNAPSHOT_STORAGE_DIR", "/var/lib/143/snapshots")
	t.Setenv("SNAPSHOT_S3_BUCKET", "session-snapshots")
	t.Setenv("SNAPSHOT_S3_PREFIX", "sessions")
	t.Setenv("SNAPSHOT_S3_REGION", "us-west-2")
	t.Setenv("SNAPSHOT_S3_ENDPOINT", "https://r2.example.com")
	t.Setenv("SNAPSHOT_S3_USE_PATH_STYLE", "true")

	cfg := Load()

	require.Equal(t, "/var/lib/143/snapshots", cfg.SnapshotStorageDir, "Load should read SNAPSHOT_STORAGE_DIR from the environment")
	require.Equal(t, "session-snapshots", cfg.SnapshotS3Bucket, "Load should read SNAPSHOT_S3_BUCKET from the environment")
	require.Equal(t, "sessions", cfg.SnapshotS3Prefix, "Load should read SNAPSHOT_S3_PREFIX from the environment")
	require.Equal(t, "us-west-2", cfg.SnapshotS3Region, "Load should read SNAPSHOT_S3_REGION from the environment")
	require.Equal(t, "https://r2.example.com", cfg.SnapshotS3Endpoint, "Load should read SNAPSHOT_S3_ENDPOINT from the environment")
	require.True(t, cfg.SnapshotS3UsePathStyle, "Load should parse SNAPSHOT_S3_USE_PATH_STYLE from the environment")
}

//nolint:paralleltest // uses t.Setenv
func TestPlatformLLMConfig_PropagatesGeminiFields(t *testing.T) {
	t.Setenv("PLATFORM_LLM_MODEL", "gpt-5.4-nano")
	t.Setenv("GEMINI_API_KEY", "AIza-test-key")
	t.Setenv("GEMINI_BASE_URL", "https://gemini.example.com")

	cfg := Load()
	platform := cfg.PlatformLLMConfig()

	require.Equal(t, llm.ModelName("gpt-5.4-nano"), platform.Model, "platform model should come from PLATFORM_LLM_MODEL")
	require.Equal(t, "AIza-test-key", platform.GeminiAPIKey, "platform config must propagate GEMINI_API_KEY")
	require.Equal(t, "https://gemini.example.com", platform.GeminiBaseURL, "platform config must propagate GEMINI_BASE_URL")
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
	t.Setenv("GEMINI_API_KEY", "AIza-test")
	t.Setenv("SESSION_SECRET", "secret")

	cfg := Load()
	// LogStatus should not panic.
	require.NotPanics(t, func() {
		cfg.LogStatus(zerolog.Nop())
	})
}

//nolint:paralleltest // uses t.Setenv
func TestLogStatus_IncludesGeminiProvider(t *testing.T) {
	t.Setenv("LLM_MODEL", "gemini-2.5-pro")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "AIza-test-key")
	t.Setenv("SESSION_SECRET", "test-secret")

	cfg := Load()
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	cfg.LogStatus(logger)

	require.Contains(t, buf.String(), "gemini", "LogStatus should mention the gemini provider")
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
	t.Setenv("GEMINI_API_KEY", "AIzaSy-abcdefgh1234")

	cfg := Load()
	safe := cfg.SafeLLMEnv()

	require.Len(t, safe, 4, "should include all four providers")
	require.Equal(t, "sk-a••••7890", safe["anthropic"])
	require.Equal(t, "sk-p••••1234", safe["openai"])
	require.Equal(t, "sk-o••••5678", safe["openrouter"])
	require.Equal(t, "AIza••••1234", safe["gemini"])
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

func TestSentryEnvironmentOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		expected string
	}{
		{
			name:     "returns explicit sentry environment when configured",
			cfg:      Config{Env: "production", SentryEnvironment: "staging"},
			expected: "staging",
		},
		{
			name:     "falls back to app environment when sentry environment is empty",
			cfg:      Config{Env: "production"},
			expected: "production",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.cfg.SentryEnvironmentOrDefault(), "SentryEnvironmentOrDefault should return the expected environment")
		})
	}
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

// TestLogStatus_DemoCredentialMismatch covers the boot-time warning emitted
// when DemoMode is on but DemoEmail/DemoPassword have been overridden away
// from the seeded defaults — the banner would advertise credentials that
// don't actually sign in against the seeded bcrypt hash.
func TestLogStatus_DemoCredentialMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		wantWarn bool
	}{
		{
			name: "demo on + defaults does not warn",
			cfg: Config{
				SessionSecret:  "s",
				CSRFSigningKey: "c",
				DemoMode:       true,
				DemoEmail:      defaultDemoEmail,
				DemoPassword:   defaultDemoPassword,
			},
			wantWarn: false,
		},
		{
			name: "demo on + overridden password warns",
			cfg: Config{
				SessionSecret:  "s",
				CSRFSigningKey: "c",
				DemoMode:       true,
				DemoEmail:      defaultDemoEmail,
				DemoPassword:   "something-else",
			},
			wantWarn: true,
		},
		{
			name: "demo on + overridden email warns",
			cfg: Config{
				SessionSecret:  "s",
				CSRFSigningKey: "c",
				DemoMode:       true,
				DemoEmail:      "other@example.com",
				DemoPassword:   defaultDemoPassword,
			},
			wantWarn: true,
		},
		{
			name: "demo off + overridden creds does not warn",
			cfg: Config{
				SessionSecret:  "s",
				CSRFSigningKey: "c",
				DemoMode:       false,
				DemoEmail:      "other@example.com",
				DemoPassword:   "something-else",
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
				require.Contains(t, buf.String(), "DEMO_EMAIL or DEMO_PASSWORD overridden")
			} else {
				require.NotContains(t, buf.String(), "DEMO_EMAIL or DEMO_PASSWORD overridden")
			}
		})
	}
}

// TestLogStatus_DemoSuppressesGitHubApp covers the boot-time warning emitted
// when DemoMode is on but GitHub App credentials are also configured — the
// integration is silently suppressed, so we log once at startup so the cause
// is easy to find in logs.
func TestLogStatus_DemoSuppressesGitHubApp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		wantWarn bool
	}{
		{
			name: "demo on + github app creds warns",
			cfg: Config{
				SessionSecret:       "s",
				CSRFSigningKey:      "c",
				DemoMode:            true,
				GitHubAppID:         123,
				GitHubAppPrivateKey: "key",
			},
			wantWarn: true,
		},
		{
			name: "demo on + no github app creds does not warn",
			cfg: Config{
				SessionSecret:  "s",
				CSRFSigningKey: "c",
				DemoMode:       true,
			},
			wantWarn: false,
		},
		{
			name: "demo off + github app creds does not warn",
			cfg: Config{
				SessionSecret:       "s",
				CSRFSigningKey:      "c",
				GitHubAppID:         123,
				GitHubAppPrivateKey: "key",
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
				require.Contains(t, buf.String(), "DEMO_MODE suppresses the configured GitHub App")
			} else {
				require.NotContains(t, buf.String(), "DEMO_MODE suppresses the configured GitHub App")
			}
		})
	}
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
			name: "production + host containing 'localhost' as substring does not warn",
			cfg: Config{
				Env:                   "production",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "https://{id}.preview.localhost.example.com",
			},
			wantWarn: false,
		},
		{
			name: "production + loopback IP warns",
			cfg: Config{
				Env:                   "production",
				SessionSecret:         "s",
				CSRFSigningKey:        "c",
				PreviewOriginTemplate: "http://127.0.0.1:9090/{id}",
			},
			wantWarn: true,
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

func TestPreviewOriginHostIsLocal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		want     bool
	}{
		{name: "localhost host", template: "http://localhost:9090/{id}", want: true},
		{name: "wildcard localhost subdomain", template: "http://{id}.preview.localhost:9090", want: true},
		{name: "loopback IPv4", template: "http://127.0.0.1:9090/{id}", want: true},
		{name: "loopback IPv6", template: "http://[::1]:9090/{id}", want: true},
		{name: "real host", template: "https://{id}.preview.example.com", want: false},
		{name: "substring localhost in real host", template: "https://{id}.localhost.example.com", want: false},
		// url.Parse error branch: stray percent escape
		{name: "malformed percent escape", template: "http://%zz", want: false},
		// u.Host == "" branch: no authority component
		{name: "relative path without scheme", template: "relative/path", want: false},
		{name: "scheme only", template: "http:", want: false},
		// u.Hostname() == "" branch: authority with port but empty host
		{name: "empty host with port", template: "http://:8080/{id}", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, previewOriginHostIsLocal(tc.template))
		})
	}
}
