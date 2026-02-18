package config

import (
	"os"
	"strconv"
	"strings"
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

	// Server mode
	Mode string // "all", "api", "worker"
}

func Load() *Config {
	port, _ := strconv.Atoi(getEnv("PORT", "8080"))
	appID, _ := strconv.ParseInt(getEnv("GITHUB_APP_ID", "0"), 10, 64)

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
		Mode:                    getEnv("MODE", "all"),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
