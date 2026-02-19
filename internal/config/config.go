package config

import (
	"fmt"

	"github.com/assembledhq/143/internal/llm"
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
)

type Config struct {
	// Core
	DatabaseURL        string   `env:"DATABASE_URL"          envDefault:"postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable"`
	Port               int      `env:"PORT"                  envDefault:"8080"`
	LogLevel           string   `env:"LOG_LEVEL"             envDefault:"info"`
	SessionSecret      string   `env:"SESSION_SECRET"`
	BaseURL            string   `env:"BASE_URL"              envDefault:"http://localhost:8080"`
	FrontendURL        string   `env:"FRONTEND_URL"          envDefault:"http://localhost:3000"`
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"  envDefault:"http://localhost:3000" envSeparator:","`
	Mode               string   `env:"MODE"                  envDefault:"all"`

	// GitHub OAuth
	GitHubOAuthClientID     string `env:"GITHUB_OAUTH_CLIENT_ID"`
	GitHubOAuthClientSecret string `env:"GITHUB_OAUTH_CLIENT_SECRET"`

	// GitHub App
	GitHubAppID         int64  `env:"GITHUB_APP_ID"`
	GitHubAppPrivateKey  string `env:"GITHUB_APP_PRIVATE_KEY"`
	GitHubWebhookSecret string `env:"GITHUB_WEBHOOK_SECRET"`

	// Webhook secrets
	SentryWebhookSecret string `env:"SENTRY_WEBHOOK_SECRET"`
	LinearWebhookSecret string `env:"LINEAR_WEBHOOK_SECRET"`

	// Encryption
	EncryptionMasterKey string `env:"ENCRYPTION_MASTER_KEY"`

	// LLM
	LLMModel          string `env:"LLM_MODEL"`
	AnthropicAPIKey   string `env:"ANTHROPIC_API_KEY"`
	AnthropicBaseURL  string `env:"ANTHROPIC_BASE_URL"`
	OpenAIAPIKey      string `env:"OPENAI_API_KEY"`
	OpenAIBaseURL     string `env:"OPENAI_BASE_URL"`
	OpenAIAPIType     string `env:"OPENAI_API_TYPE"       envDefault:"chat"`
	OpenRouterAPIKey  string `env:"OPENROUTER_API_KEY"`
	OpenRouterBaseURL string `env:"OPENROUTER_BASE_URL"`
	OpenRouterAppName string `env:"OPENROUTER_APP_NAME"   envDefault:"143"`
	OpenRouterSiteURL string `env:"OPENROUTER_SITE_URL"`
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
	return cfg
}

// LLMConfig returns the llm.Config derived from this Config.
func (c *Config) LLMConfig() llm.Config {
	return llm.Config{
		Model:             c.LLMModel,
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
		{"GitHub App", c.GitHubAppID != 0 && c.GitHubAppPrivateKey != "", "webhooks, PRs"},
		{"Sentry webhooks", c.SentryWebhookSecret != "", "ingestion"},
		{"Linear webhooks", c.LinearWebhookSecret != "", "ingestion"},
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

	if c.LLMModel != "" && len(providers) > 0 {
		logger.Info().
			Str("model", c.LLMModel).
			Strs("providers", providers).
			Int("chain_length", len(providers)).
			Msg("LLM configured")
	} else if c.LLMModel != "" {
		logger.Warn().
			Str("model", c.LLMModel).
			Msg("LLM model set but no provider API keys configured — LLM checks will be skipped")
	} else {
		logger.Warn().
			Msg("LLM not configured (set LLM_MODEL + at least one provider API key) — LLM checks will be skipped")
	}

	if c.SessionSecret == "" {
		logger.Warn().Msg("SESSION_SECRET is empty — sessions will not survive restarts")
	}
}
