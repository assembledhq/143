package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/llm"
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
)

type Config struct {
	// Environment
	Env string `env:"ENV" envDefault:"development"`

	// Core
	DatabaseURL        string   `env:"DATABASE_URL"          envDefault:"postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable"`
	Port               int      `env:"PORT"                  envDefault:"8080"`
	LogLevel           string   `env:"LOG_LEVEL"             envDefault:"info"`
	SessionSecret      string   `env:"SESSION_SECRET"` // #nosec G117 -- env config field
	BaseURL            string   `env:"BASE_URL"              envDefault:"http://localhost:8080"`
	FrontendURL        string   `env:"FRONTEND_URL"`
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"  envSeparator:","`
	Mode               string   `env:"MODE"                  envDefault:"all"`

	// GitHub OAuth
	GitHubOAuthClientID     string `env:"GITHUB_OAUTH_CLIENT_ID"`
	GitHubOAuthClientSecret string `env:"GITHUB_OAUTH_CLIENT_SECRET"`
	GitHubOAuthRedirectURI  string `env:"GITHUB_OAUTH_REDIRECT_URI"`

	// Google OAuth
	GoogleOAuthClientID     string `env:"GOOGLE_OAUTH_CLIENT_ID"`
	GoogleOAuthClientSecret string `env:"GOOGLE_OAUTH_CLIENT_SECRET"`

	// Linear OAuth
	LinearOAuthClientID     string `env:"LINEAR_OAUTH_CLIENT_ID"`
	LinearOAuthClientSecret string `env:"LINEAR_OAUTH_CLIENT_SECRET"`

	// Sentry OAuth
	SentryOAuthClientID     string `env:"SENTRY_OAUTH_CLIENT_ID"`
	SentryOAuthClientSecret string `env:"SENTRY_OAUTH_CLIENT_SECRET"`

	// Slack OAuth
	SlackOAuthClientID     string `env:"SLACK_OAUTH_CLIENT_ID"`
	SlackOAuthClientSecret string `env:"SLACK_OAUTH_CLIENT_SECRET"`
	SlackSummaryModel      string `env:"SLACK_SUMMARY_MODEL" envDefault:"gpt-5-nano"`

	// GitHub App
	GitHubAppID         int64  `env:"GITHUB_APP_ID"`
	GitHubAppPrivateKey string `env:"GITHUB_APP_PRIVATE_KEY"`
	GitHubWebhookSecret string `env:"GITHUB_WEBHOOK_SECRET"`
	GitHubAppSlug       string `env:"GITHUB_APP_SLUG"`

	// CSRF
	CSRFSigningKey string `env:"CSRF_SIGNING_KEY"`

	// Encryption
	EncryptionMasterKey string `env:"ENCRYPTION_MASTER_KEY"`

	// LLM
	LLMModel           string `env:"LLM_MODEL"`
	LLMReasoningEffort string `env:"LLM_REASONING_EFFORT"`
	AnthropicAPIKey    string `env:"ANTHROPIC_API_KEY"`
	AnthropicBaseURL  string `env:"ANTHROPIC_BASE_URL"`
	AnthropicModel    string `env:"ANTHROPIC_MODEL"`
	OpenAIAPIKey      string `env:"OPENAI_API_KEY"`
	OpenAIBaseURL     string `env:"OPENAI_BASE_URL"`
	OpenAIAPIType     string `env:"OPENAI_API_TYPE"       envDefault:"chat"`
	OpenAIModel       string `env:"OPENAI_MODEL"`
	OpenRouterAPIKey  string `env:"OPENROUTER_API_KEY"`
	OpenRouterBaseURL string `env:"OPENROUTER_BASE_URL"`
	OpenRouterAppName string `env:"OPENROUTER_APP_NAME"   envDefault:"143"`
	OpenRouterSiteURL string `env:"OPENROUTER_SITE_URL"`

	// Gemini CLI
	GeminiAPIKey string `env:"GEMINI_API_KEY"`
	GeminiModel  string `env:"GEMINI_MODEL"`

	// Sandbox
	SandboxRuntime     string `env:"SANDBOX_RUNTIME" envDefault:"runc"`
	SandboxRequireGVisor bool   `env:"SANDBOX_REQUIRE_GVISOR" envDefault:"false"`
	// Data retention
	DataRetentionWebhookDays int `env:"DATA_RETENTION_WEBHOOK_DAYS" envDefault:"30"`
	DataRetentionLogsDays    int `env:"DATA_RETENTION_LOGS_DAYS"    envDefault:"90"`
	DataRetentionJobsDays    int `env:"DATA_RETENTION_JOBS_DAYS"    envDefault:"30"`

	// Upload storage (images/files attached to session messages)
	UploadStorageDir      string `env:"UPLOAD_STORAGE_DIR"      envDefault:".data/uploads"`
	UploadS3Bucket        string `env:"UPLOAD_S3_BUCKET"`
	UploadS3Prefix        string `env:"UPLOAD_S3_PREFIX"        envDefault:"uploads"`
	UploadS3Endpoint      string `env:"UPLOAD_S3_ENDPOINT"`      // e.g. https://mybucket.s3.amazonaws.com
	UploadS3Region        string `env:"UPLOAD_S3_REGION"        envDefault:"us-east-1"`
	UploadMaxAge          time.Duration `env:"UPLOAD_MAX_AGE"    envDefault:"2160h"` // 90 days

	// Interactive session snapshots
	SnapshotStorageDir    string        `env:"SNAPSHOT_STORAGE_DIR"    envDefault:".data/snapshots"`
	SessionMaxIdleAge     time.Duration `env:"SESSION_MAX_IDLE_AGE"    envDefault:"2h"`
	SessionReaperInterval time.Duration `env:"SESSION_REAPER_INTERVAL" envDefault:"5m"`
	SessionMaxSnapshotAge time.Duration `env:"SESSION_MAX_SNAPSHOT_AGE" envDefault:"720h"` // 30 days
}

// Load reads configuration from env files and environment variables.
//
// Precedence (highest wins):
//  1. Real env vars (CI, Docker, secret manager, SOPS decrypt)
//  2. .env.local (personal overrides, gitignored)
//  3. .env (shared defaults, gitignored)
func Load() *Config {
	// godotenv does NOT overwrite already-set variables, so real env vars
	// always win. .env.local is listed first so it takes precedence over .env.
	_ = godotenv.Load(".env.local", ".env")

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		// Fall back to zero-value config rather than crashing — LogStatus
		// will surface missing values at startup.
		return cfg
	}

	// Default FRONTEND_URL and CORS_ALLOWED_ORIGINS to BASE_URL when not
	// explicitly set. In production the frontend proxies API calls so all
	// three share the same origin.
	if cfg.FrontendURL == "" {
		cfg.FrontendURL = cfg.BaseURL
	}
	if len(cfg.CORSAllowedOrigins) == 0 {
		cfg.CORSAllowedOrigins = []string{cfg.FrontendURL}
	}

	// Default GitHub OAuth redirect URI to BASE_URL + callback path.
	if cfg.GitHubOAuthRedirectURI == "" {
		cfg.GitHubOAuthRedirectURI = cfg.BaseURL + "/api/v1/auth/github/callback"
	}

	// Fall back to SessionSecret for CSRF signing if not explicitly set.
	if cfg.CSRFSigningKey == "" {
		cfg.CSRFSigningKey = cfg.SessionSecret
	}

	return cfg
}

// LLMConfig returns the llm.Config derived from this Config.
func (c *Config) LLMConfig() llm.Config {
	return llm.Config{
		Model:             llm.ModelName(c.LLMModel),
		ReasoningEffort:   llm.ReasoningEffort(c.LLMReasoningEffort),
		AnthropicAPIKey:   c.AnthropicAPIKey,
		AnthropicBaseURL:  c.AnthropicBaseURL,
		OpenAIAPIKey:      c.OpenAIAPIKey,
		OpenAIBaseURL:     c.OpenAIBaseURL,
		OpenAIAPIType:     c.OpenAIAPIType,
		OpenRouterAPIKey:  c.OpenRouterAPIKey,
		OpenRouterBaseURL: c.OpenRouterBaseURL,
		OpenRouterAppName: c.OpenRouterAppName,
		OpenRouterSiteURL: c.OpenRouterSiteURL,
	}
}

// AgentEnv returns a map from agent type to the environment variables that
// should be injected into sandbox containers for that agent. Only includes
// entries for agents whose required credentials are configured.
func (c *Config) AgentEnv() map[string]map[string]string {
	result := make(map[string]map[string]string)

	// Claude Code needs ANTHROPIC_API_KEY.
	// ANTHROPIC_MODEL selects the model (e.g. "opus", "sonnet", "claude-opus-4-6", "claude-sonnet-4-5").
	if c.AnthropicAPIKey != "" {
		env := map[string]string{"ANTHROPIC_API_KEY": c.AnthropicAPIKey}
		if c.AnthropicBaseURL != "" {
			env["ANTHROPIC_BASE_URL"] = c.AnthropicBaseURL
		}
		if c.AnthropicModel != "" {
			env["ANTHROPIC_MODEL"] = c.AnthropicModel
		}
		result["claude_code"] = env
	}

	// Codex needs OPENAI_API_KEY.
	// OPENAI_MODEL is not natively supported by Codex CLI (it uses config.toml),
	// but we pass it so the adapter can use it in the --model flag
	// (e.g. "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5-codex").
	if c.OpenAIAPIKey != "" {
		env := map[string]string{"OPENAI_API_KEY": c.OpenAIAPIKey}
		if c.OpenAIBaseURL != "" {
			env["OPENAI_BASE_URL"] = c.OpenAIBaseURL
		}
		if c.OpenAIModel != "" {
			env["OPENAI_MODEL"] = c.OpenAIModel
		}
		result["codex"] = env
	}

	// Gemini CLI needs GEMINI_API_KEY.
	// GEMINI_MODEL selects the model (e.g. "gemini-3-pro-preview", "gemini-3-flash-preview", "gemini-2.5-pro").
	if c.GeminiAPIKey != "" {
		env := map[string]string{"GEMINI_API_KEY": c.GeminiAPIKey}
		if c.GeminiModel != "" {
			env["GEMINI_MODEL"] = c.GeminiModel
		}
		result["gemini_cli"] = env
	}

	return result
}

// SafeAgentEnv returns the same structure as AgentEnv but with API key values
// masked (e.g. "sk-ant-...prod"). Suitable for exposing to the frontend so
// operators can see what server defaults are configured without leaking secrets.
func (c *Config) SafeAgentEnv() map[string]map[string]string {
	raw := c.AgentEnv()
	safe := make(map[string]map[string]string, len(raw))
	for agent, vars := range raw {
		safeVars := make(map[string]string, len(vars))
		for k, v := range vars {
			if isSecretKey(k) {
				safeVars[k] = maskSecret(v)
			} else {
				safeVars[k] = v
			}
		}
		safe[agent] = safeVars
	}
	return safe
}

// isSecretKey returns true for env var names that contain secrets.
func isSecretKey(key string) bool {
	switch key {
	case "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY":
		return true
	}
	return false
}

// maskSecret masks a secret string, showing the first 4 and last 4 characters.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "••••••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// SafeLLMEnv returns a map of LLM provider names to masked API keys for
// providers that have server-level keys configured. This lets the frontend
// show whether platform fallback is available without leaking secrets.
func (c *Config) SafeLLMEnv() map[string]string {
	result := make(map[string]string)
	if c.AnthropicAPIKey != "" {
		result["anthropic"] = maskSecret(c.AnthropicAPIKey)
	}
	if c.OpenAIAPIKey != "" {
		result["openai"] = maskSecret(c.OpenAIAPIKey)
	}
	if c.OpenRouterAPIKey != "" {
		result["openrouter"] = maskSecret(c.OpenRouterAPIKey)
	}
	return result
}

// LogStatus logs which features are configured and which are missing.
// Call this at startup so contributors immediately see what's working.
func (c *Config) LogStatus(logger zerolog.Logger) {
	features := []struct {
		name       string
		configured bool
		detail     string
	}{
		{"Database", c.DatabaseURL != "", ""},
		{"GitHub OAuth", c.GitHubOAuthClientID != "" && c.GitHubOAuthClientSecret != "", "login"},
		{"Google OAuth", c.GoogleOAuthClientID != "" && c.GoogleOAuthClientSecret != "", "login"},
		{"Linear OAuth", c.LinearOAuthClientID != "" && c.LinearOAuthClientSecret != "", "integration auth"},
		{"Sentry OAuth", c.SentryOAuthClientID != "" && c.SentryOAuthClientSecret != "", "integration auth"},
		{"GitHub App", c.GitHubAppID != 0 && c.GitHubAppPrivateKey != "", "webhooks, PRs"},
		{"Credential encryption", c.EncryptionMasterKey != "", "encrypted credential storage"},
	}

	for _, f := range features {
		evt := logger.Info()
		if !f.configured {
			evt = logger.Warn()
		}
		e := evt.Bool("configured", f.configured).Str("feature", f.name)
		if f.detail != "" {
			e = e.Str("enables", f.detail)
		}
		e.Msg("feature status")
	}

	// LLM providers
	var providers []string
	if c.AnthropicAPIKey != "" {
		providers = append(providers, "anthropic")
	}
	if c.OpenAIAPIKey != "" {
		providers = append(providers, fmt.Sprintf("openai_%s", c.OpenAIAPIType))
	}
	if c.OpenRouterAPIKey != "" {
		providers = append(providers, "openrouter")
	}

	llmModel := c.LLMModel
	if llmModel == "" {
		llmModel = "(default: gpt-5.4-mini)"
	}
	if len(providers) > 0 {
		logger.Info().
			Str("model", llmModel).
			Strs("providers", providers).
			Int("chain_length", len(providers)).
			Msg("LLM configured")
	} else if c.LLMModel != "" {
		logger.Warn().
			Str("model", c.LLMModel).
			Msg("LLM model set but no provider API keys configured — LLM checks will be skipped")
	} else {
		logger.Warn().
			Msg("LLM not configured (set at least one provider API key) — LLM checks will be skipped")
	}

	if c.SessionSecret == "" {
		logger.Warn().Msg("SESSION_SECRET is empty — sessions will not survive restarts")
	}

	if c.CSRFSigningKey == "" {
		logger.Warn().Msg("CSRF_SIGNING_KEY is empty — CSRF protection will be ineffective")
	}
}

// ValidateSecrets checks that security-sensitive configuration values meet
// minimum strength requirements when running in production.
func (c *Config) ValidateSecrets() error {
	// Retention day validation applies in all environments.
	if c.DataRetentionWebhookDays < 0 || c.DataRetentionLogsDays < 0 || c.DataRetentionJobsDays < 0 {
		return errors.New("DATA_RETENTION_*_DAYS values must not be negative")
	}

	if c.Env != "production" {
		return nil
	}

	if c.SessionSecret == "" || c.SessionSecret == "changeme" || len(c.SessionSecret) < 32 {
		return errors.New("SESSION_SECRET must be set to a strong random value in production (min 32 characters)")
	}

	if c.EncryptionMasterKey == "" {
		return errors.New("ENCRYPTION_MASTER_KEY must be set in production")
	}
	if len(c.EncryptionMasterKey) < 32 {
		return errors.New("ENCRYPTION_MASTER_KEY must be at least 32 characters")
	}

	if c.CSRFSigningKey == "" || len(c.CSRFSigningKey) < 32 {
		return errors.New("CSRF_SIGNING_KEY must be set to a strong random value in production (min 32 characters)")
	}

	return nil
}
