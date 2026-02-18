package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_UsesDefaults(t *testing.T) {
	// This test mutates process-wide env vars via t.Setenv, so it must not run in parallel.
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("BASE_URL", "")
	t.Setenv("FRONTEND_URL", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("MODE", "")
	t.Setenv("GITHUB_APP_ID", "")

	cfg := Load()

	require.Equal(t, 8080, cfg.Port, "Load should default to port 8080")
	require.Equal(t, "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", cfg.DatabaseURL, "Load should default the database URL")
	require.Equal(t, "info", cfg.LogLevel, "Load should default log level to info")
	require.Equal(t, "http://localhost:8080", cfg.BaseURL, "Load should default base URL")
	require.Equal(t, "http://localhost:3000", cfg.FrontendURL, "Load should default frontend URL")
	require.Equal(t, []string{"http://localhost:3000"}, cfg.CORSAllowedOrigins, "Load should default CORS origins")
	require.Equal(t, int64(0), cfg.GitHubAppID, "Load should default GitHub app ID to zero")
	require.Equal(t, "all", cfg.Mode, "Load should default mode to all")
}

func TestLoad_UsesEnvironmentOverrides(t *testing.T) {
	// This test mutates process-wide env vars via t.Setenv, so it must not run in parallel.
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
