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
	SessionSecret      string   `env:"SESSION_SECRET"` // #nosec G117 -- env config field
	BaseURL            string   `env:"BASE_URL"              envDefault:"http://localhost:8080"`
	FrontendURL        string   `env:"FRONTEND_URL"`
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"  envSeparator:","`
	Mode               string   `env:"MODE"                  envDefault:"all"`

	// GitHub OAuth
	GitHubOAuthClientID     string `env:"GITHUB_OAUTH_CLIENT_ID"`
	GitHubOAuthClientSecret string `env:"GITHUB_OAUTH_CLIENT_SECRET"`

	// Google OAuth
	GoogleOAuthClientID     string `env:"GOOGLE_OAUTH_CLIENT_ID"`
	GoogleOAuthClientSecret string `env:"GOOGLE_OAUTH_CLIENT_SECRET"`

	// GitHub App
	GitHubAppID         int64  `env:"GITHUB_APP_ID"`
	GitHubAppPrivateKey  string `env:"GITHUB_APP_PRIVATE_KEY"`
	GitHubWebhookSecret string `env:"GITHUB_WEBHOOK_SECRET"`

	// Encryption
	EncryptionMasterKey string `env:"ENCRYPTION_MASTER_KEY"`

	// LLM
	LLMModel          string `env:"LLM_MODEL"`
	AnthropicAPIKey   string `env:"ANTHROPIC_API_KEY"`
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

// AgentEnv returns a map from agent type to the environment variables that
// should be injected into sandbox containers for that agent. Only includes
// entries for agents whose required credentials are configured.
func (c *Config) AgentEnv() map[string]map[string]string {
	result := make(map[string]map[string]string)

	// Claude Code needs ANTHROPIC_API_KEY.
	// ANTHROPIC_MODEL selects the model (e.g. "opus", "sonnet", "claude-opus-4-6").
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
	// but we pass it so the adapter can use it in the --model flag.
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
	// GEMINI_MODEL selects the model (e.g. "gemini-2.5-pro", "gemini-2.5-flash").
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
