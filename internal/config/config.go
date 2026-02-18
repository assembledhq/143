package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/llm"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
)

type Config struct {
	DatabaseURL        string
	Port               int
	LogLevel           string
	SessionSecret      string
	BaseURL            string
	FrontendURL        string
	CORSAllowedOrigins []string

	// GitHub OAuth
	GitHubOAuthClientID     string
	GitHubOAuthClientSecret string

	// GitHub App
	GitHubAppID         int64
	GitHubAppPrivateKey  string
	GitHubWebhookSecret string

	// Webhook secrets for ingestion providers
	SentryWebhookSecret string
	LinearWebhookSecret string

	// LLM
	LLMModel          string // e.g. "claude-sonnet-4-5", "gpt-4o"
	AnthropicAPIKey   string
	AnthropicBaseURL  string // optional, defaults to https://api.anthropic.com
	OpenAIAPIKey      string
	OpenAIBaseURL     string // optional, defaults to https://api.openai.com
	OpenAIAPIType     string // "chat" or "responses", defaults to "chat"
	OpenRouterAPIKey  string
	OpenRouterBaseURL string // optional, defaults to https://openrouter.ai/api
	OpenRouterAppName string // optional, sent as X-Title
	OpenRouterSiteURL string // optional, sent as HTTP-Referer

	// Server mode
	Mode string // "all", "api", "worker"
}

func Load() *Config {
	// Load env files in precedence order (lowest to highest).
	// godotenv does NOT overwrite already-set variables, so we load in
	// reverse precedence: .env first, then .env.local (which won't
	// overwrite .env values — but real env vars beat both).
	//
	// File loading order:
	//   1. .env          — shared defaults, committed to repo as .env.example
	//   2. .env.local    — personal overrides, never committed
	//   3. Real env vars — always win (set by secret manager, CI, Docker, etc.)
	//
	// Errors are silently ignored (files are optional).
	_ = godotenv.Load(".env.local", ".env")

	port := 8080
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		}
	}

	var appID int64
	if v := os.Getenv("GITHUB_APP_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			appID = id
		}
	}

	return &Config{
		DatabaseURL:             getEnv("DATABASE_URL", "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable"),
		Port:                    port,
		LogLevel:                getEnv("LOG_LEVEL", "info"),
		SessionSecret:           getEnv("SESSION_SECRET", ""),
		BaseURL:                 getEnv("BASE_URL", "http://localhost:8080"),
		FrontendURL:             getEnv("FRONTEND_URL", "http://localhost:3000"),
		CORSAllowedOrigins:      strings.Split(getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:3000"), ","),
		GitHubOAuthClientID:     getEnv("GITHUB_OAUTH_CLIENT_ID", ""),
		GitHubOAuthClientSecret: getEnv("GITHUB_OAUTH_CLIENT_SECRET", ""),
		GitHubAppID:             appID,
		GitHubAppPrivateKey:     getEnv("GITHUB_APP_PRIVATE_KEY", ""),
		GitHubWebhookSecret:     getEnv("GITHUB_WEBHOOK_SECRET", ""),
		SentryWebhookSecret:     getEnv("SENTRY_WEBHOOK_SECRET", ""),
		LinearWebhookSecret:     getEnv("LINEAR_WEBHOOK_SECRET", ""),
		LLMModel:                getEnv("LLM_MODEL", ""),
		AnthropicAPIKey:         getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicBaseURL:        getEnv("ANTHROPIC_BASE_URL", ""),
		OpenAIAPIKey:            getEnv("OPENAI_API_KEY", ""),
		OpenAIBaseURL:           getEnv("OPENAI_BASE_URL", ""),
		OpenAIAPIType:           getEnv("OPENAI_API_TYPE", "chat"),
		OpenRouterAPIKey:        getEnv("OPENROUTER_API_KEY", ""),
		OpenRouterBaseURL:       getEnv("OPENROUTER_BASE_URL", ""),
		OpenRouterAppName:       getEnv("OPENROUTER_APP_NAME", "143"),
		OpenRouterSiteURL:       getEnv("OPENROUTER_SITE_URL", ""),
		Mode:                    getEnv("MODE", "all"),
	}
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
	// Core services
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

	// LLM providers — special handling to show the fallback chain
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

	// Security warnings
	if c.SessionSecret == "" {
		logger.Warn().Msg("SESSION_SECRET is empty — sessions will not survive restarts")
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
