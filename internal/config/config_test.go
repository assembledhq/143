package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("OPENAI_API_TYPE", "")
	t.Setenv("OPENROUTER_APP_NAME", "")

	cfg := Load()

	require.Equal(t, 8080, cfg.Port, "Load should default to port 8080")
	require.Equal(t, "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", cfg.DatabaseURL, "Load should default the database URL")
	require.Equal(t, "info", cfg.LogLevel, "Load should default log level to info")
	require.Equal(t, "http://localhost:8080", cfg.BaseURL, "Load should default base URL")
	require.Equal(t, "http://localhost:3000", cfg.FrontendURL, "Load should default frontend URL")
	require.Equal(t, []string{"http://localhost:3000"}, cfg.CORSAllowedOrigins, "Load should default CORS origins")
	require.Equal(t, int64(0), cfg.GitHubAppID, "Load should default GitHub app ID to zero")
	require.Equal(t, "all", cfg.Mode, "Load should default mode to all")
	require.Equal(t, "chat", cfg.OpenAIAPIType, "Load should default OpenAI API type to chat")
	require.Equal(t, "143", cfg.OpenRouterAppName, "Load should default OpenRouter app name to 143")
}

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

func TestLoad_LLMConfig(t *testing.T) {
	t.Setenv("LLM_MODEL", "claude-sonnet-4-5")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cfg := Load()
	llmCfg := cfg.LLMConfig()

	require.Equal(t, "claude-sonnet-4-5", llmCfg.Model)
	require.Equal(t, "sk-ant-test", llmCfg.AnthropicAPIKey)
	require.Equal(t, "sk-or-test", llmCfg.OpenRouterAPIKey)
	require.Equal(t, "chat", llmCfg.OpenAIAPIType, "OpenAI API type should default to chat")
}

func TestAgentEnv_ClaudeCodeAndCodex(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-prod")
	t.Setenv("ANTHROPIC_BASE_URL", "https://custom.anthropic.com")
	t.Setenv("ANTHROPIC_MODEL", "opus")
	t.Setenv("OPENAI_API_KEY", "sk-openai-prod")
	t.Setenv("OPENAI_BASE_URL", "https://custom.openai.com")
	t.Setenv("OPENAI_MODEL", "gpt-5.3-codex")

	cfg := Load()
	env := cfg.AgentEnv()

	// Claude Code
	require.Contains(t, env, "claude_code")
	require.Equal(t, "sk-ant-prod", env["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "https://custom.anthropic.com", env["claude_code"]["ANTHROPIC_BASE_URL"])
	require.Equal(t, "opus", env["claude_code"]["ANTHROPIC_MODEL"])

	// Codex
	require.Contains(t, env, "codex")
	require.Equal(t, "sk-openai-prod", env["codex"]["OPENAI_API_KEY"])
	require.Equal(t, "https://custom.openai.com", env["codex"]["OPENAI_BASE_URL"])
	require.Equal(t, "gpt-5.3-codex", env["codex"]["OPENAI_MODEL"])
}

func TestAgentEnv_NoKeysConfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	cfg := Load()
	env := cfg.AgentEnv()

	require.NotContains(t, env, "claude_code", "claude_code should not be present without ANTHROPIC_API_KEY")
	require.NotContains(t, env, "codex", "codex should not be present without OPENAI_API_KEY")
	require.NotContains(t, env, "gemini_cli", "gemini_cli should not be present without GEMINI_API_KEY")
}

func TestAgentEnv_GeminiCLI(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-pro")

	cfg := Load()
	env := cfg.AgentEnv()

	require.Contains(t, env, "gemini_cli")
	require.Equal(t, "gemini-test-key", env["gemini_cli"]["GEMINI_API_KEY"])
	require.Equal(t, "gemini-2.5-pro", env["gemini_cli"]["GEMINI_MODEL"])
	require.NotContains(t, env, "claude_code")
	require.NotContains(t, env, "codex")
}

func TestAgentEnv_ModelOmittedWhenEmpty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("GEMINI_API_KEY", "gemini-test")
	t.Setenv("GEMINI_MODEL", "")

	cfg := Load()
	env := cfg.AgentEnv()

	require.NotContains(t, env["claude_code"], "ANTHROPIC_MODEL", "model should be omitted when empty")
	require.NotContains(t, env["codex"], "OPENAI_MODEL", "model should be omitted when empty")
	require.NotContains(t, env["gemini_cli"], "GEMINI_MODEL", "model should be omitted when empty")
}

func TestSafeAgentEnv_MasksAPIKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api3-abcdef1234567890")
	t.Setenv("ANTHROPIC_MODEL", "opus")
	t.Setenv("OPENAI_API_KEY", "sk-proj-abcdefgh1234")
	t.Setenv("GEMINI_API_KEY", "short")

	cfg := Load()
	safe := cfg.SafeAgentEnv()

	// Long keys: first 4 + •••• + last 4
	require.Equal(t, "sk-a••••7890", safe["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "sk-p••••1234", safe["codex"]["OPENAI_API_KEY"])

	// Short keys (<= 8 chars): fully masked
	require.Equal(t, "••••••••", safe["gemini_cli"]["GEMINI_API_KEY"])

	// Non-secret values should be unchanged
	require.Equal(t, "opus", safe["claude_code"]["ANTHROPIC_MODEL"])
}

func TestAgentEnv_OnlyAnthropicKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-only")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "")

	cfg := Load()
	env := cfg.AgentEnv()

	require.Contains(t, env, "claude_code")
	require.Equal(t, "sk-ant-only", env["claude_code"]["ANTHROPIC_API_KEY"])
	require.NotContains(t, env["claude_code"], "ANTHROPIC_BASE_URL", "base URL should be omitted when empty")
	require.NotContains(t, env["claude_code"], "ANTHROPIC_MODEL", "model should be omitted when empty")
	require.NotContains(t, env, "codex")
}
