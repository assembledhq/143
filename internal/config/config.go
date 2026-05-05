package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/llm"
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
)

// Default demo credentials. Must stay in sync with the envDefault tags on
// Config.DemoEmail / Config.DemoPassword and with the seeded admin row in
// .143/seed.sql (where the password is stored as a bcrypt hash). Overriding
// DemoPassword via env without regenerating the seed hash results in a
// login-page banner that advertises credentials that won't actually sign in
// — LogStatus warns about this at boot.
const (
	defaultDemoEmail    = "dogfood@143.dev"
	defaultDemoPassword = "preview-dogfood"
)

type Config struct {
	// Environment
	Env string `env:"ENV" envDefault:"development"`

	// Core
	DatabaseURL        string   `env:"DATABASE_URL"          envDefault:"postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable"`
	Port               int      `env:"PORT"                  envDefault:"8080"`
	LogLevel           string   `env:"LOG_LEVEL"             envDefault:"info"`
	SessionSecret      string   `env:"SESSION_SECRET"` // #nosec G117 -- env config field
	NodeID             string   `env:"NODE_ID"`
	BaseURL            string   `env:"BASE_URL"              envDefault:"http://localhost:8080"`
	FrontendURL        string   `env:"FRONTEND_URL"`
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"  envSeparator:","`
	Mode               string   `env:"MODE"                  envDefault:"all"`
	// DemoMode tells the server it is running a public demo/dogfood preview
	// with seeded data and no real GitHub App. Enables a credential banner
	// on the login page and short-circuits GitHub client construction.
	DemoMode bool `env:"DEMO_MODE" envDefault:"false"`
	// DemoEmail / DemoPassword are the public credentials rendered in the
	// login-page banner when DemoMode is on. Defaults must match the seeded
	// admin in .143/seed.sql and the constants below — override via env
	// only if you also regenerate the bcrypt hash in the seed.
	DemoEmail    string `env:"DEMO_EMAIL"    envDefault:"dogfood@143.dev"`
	DemoPassword string `env:"DEMO_PASSWORD" envDefault:"preview-dogfood"`
	// WorkerProcessCount controls how many worker loops run inside a single
	// server process when MODE is "worker" or "all". Increase this on larger
	// hosts to process more jobs/sandboxes in parallel.
	WorkerProcessCount int `env:"WORKER_PROCESS_COUNT" envDefault:"2"`
	// WorkerDrainTimeout is how long graceful shutdown waits for in-flight
	// worker jobs to finish before cancelling the worker context. Coding
	// turns routinely run 5–15 minutes (per-org cap is even higher), so a
	// short window cuts them off mid-execution and produces orphaned thread
	// rows when partial DB state lands.
	//
	// Outer caps (must be ≥ this value):
	//   - docker-compose.worker.yml stop_grace_period (50m) — binding
	//     SIGKILL deadline once Docker issues `docker stop`; this is the
	//     real ceiling on a deploy.
	//   - deploy/scripts/deploy.sh drain_worker_service polls for up to
	//     WORKER_DRAIN_TIMEOUT seconds (default 7200s) waiting for the
	//     SIGTERM'd container to exit, but `--force-recreate` afterwards
	//     hits stop_grace_period anyway, so the polling ceiling is only a
	//     safety upper bound, not the effective drain duration.
	WorkerDrainTimeout time.Duration `env:"WORKER_DRAIN_TIMEOUT" envDefault:"45m"`

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
	SentryDSN               string `env:"SENTRY_DSN"`
	SentryEnvironment       string `env:"SENTRY_ENVIRONMENT"`

	// Slack OAuth
	SlackOAuthClientID     string `env:"SLACK_OAUTH_CLIENT_ID"`
	SlackOAuthClientSecret string `env:"SLACK_OAUTH_CLIENT_SECRET"`
	SlackSummaryModel      string `env:"SLACK_SUMMARY_MODEL" envDefault:"gpt-5.4-nano"`

	// GitHub App
	GitHubAppID           int64  `env:"GITHUB_APP_ID"`
	GitHubAppClientID     string `env:"GITHUB_APP_CLIENT_ID"`
	GitHubAppClientSecret string `env:"GITHUB_APP_CLIENT_SECRET"`
	GitHubAppPrivateKey   string `env:"GITHUB_APP_PRIVATE_KEY"`
	GitHubWebhookSecret   string `env:"GITHUB_WEBHOOK_SECRET"`
	GitHubAppSlug         string `env:"GITHUB_APP_SLUG"`

	// CSRF
	CSRFSigningKey string `env:"CSRF_SIGNING_KEY"`

	// Encryption
	EncryptionMasterKey string `env:"ENCRYPTION_MASTER_KEY"`

	// LLM
	LLMModel           string `env:"LLM_MODEL"`
	LLMReasoningEffort string `env:"LLM_REASONING_EFFORT"`
	PlatformLLMModel   string `env:"PLATFORM_LLM_MODEL"    envDefault:"gpt-5.4-nano"`
	AnthropicAPIKey    string `env:"ANTHROPIC_API_KEY"`
	AnthropicBaseURL   string `env:"ANTHROPIC_BASE_URL"`
	OpenAIAPIKey       string `env:"OPENAI_API_KEY"`
	OpenAIBaseURL      string `env:"OPENAI_BASE_URL"`
	OpenAIAPIType      string `env:"OPENAI_API_TYPE"       envDefault:"chat"`
	OpenRouterAPIKey   string `env:"OPENROUTER_API_KEY"`
	OpenRouterBaseURL  string `env:"OPENROUTER_BASE_URL"`
	OpenRouterAppName  string `env:"OPENROUTER_APP_NAME"   envDefault:"143"`
	OpenRouterSiteURL  string `env:"OPENROUTER_SITE_URL"`
	GeminiAPIKey       string `env:"GEMINI_API_KEY"`
	GeminiBaseURL      string `env:"GEMINI_BASE_URL"`

	// SMTP (optional — invitation emails are logged to console when not configured)
	SMTPHost     string `env:"SMTP_HOST"`
	SMTPPort     string `env:"SMTP_PORT"     envDefault:"587"`
	SMTPUsername string `env:"SMTP_USERNAME"`
	SMTPPassword string `env:"SMTP_PASSWORD"`
	SMTPFrom     string `env:"SMTP_FROM"`

	// Sandbox
	SandboxRuntime       string `env:"SANDBOX_RUNTIME" envDefault:"runc"`
	SandboxRequireGVisor bool   `env:"SANDBOX_REQUIRE_GVISOR" envDefault:"false"`
	// SandboxResolvConf, when set, is bind-mounted read-only at /etc/resolv.conf
	// inside every sandbox container. Required under runsc on user-defined
	// networks because gVisor's netstack can't reach Docker's embedded DNS at
	// 127.0.0.11 and the HostConfig.DNS field doesn't replace it on user
	// networks. The file lives on the worker host (e.g. /etc/143/sandbox-resolv.conf)
	// and typically contains "nameserver 1.1.1.1\nnameserver 8.8.8.8". Leaving
	// this empty falls back to whatever resolv.conf Docker injects.
	SandboxResolvConf string `env:"SANDBOX_RESOLV_CONF"`
	// SandboxAuthSocketDir is the on-host directory under which per-session
	// GitHub credential sockets are created (one Unix-domain socket per
	// session, bind-mounted into the container). The orchestrator must be
	// able to mkdir / chmod this path; the directory is provisioned out of
	// band by deploy/scripts/provision.sh next to the resolv.conf file. If
	// empty, the credential-helper path is disabled and sessions fall back
	// to the legacy GITHUB_TOKEN env-var injection.
	SandboxAuthSocketDir string `env:"SANDBOX_AUTH_SOCKET_DIR" envDefault:"/var/run/143/sandbox-auth"`
	// Data retention
	DataRetentionWebhookDays int `env:"DATA_RETENTION_WEBHOOK_DAYS" envDefault:"30"`
	DataRetentionLogsDays    int `env:"DATA_RETENTION_LOGS_DAYS"    envDefault:"90"`
	DataRetentionJobsDays    int `env:"DATA_RETENTION_JOBS_DAYS"    envDefault:"30"`

	// Upload storage (images/files attached to session messages)
	UploadStorageDir string        `env:"UPLOAD_STORAGE_DIR"      envDefault:".data/uploads"`
	UploadS3Bucket   string        `env:"UPLOAD_S3_BUCKET"`
	UploadS3Prefix   string        `env:"UPLOAD_S3_PREFIX"        envDefault:"uploads"`
	UploadS3Endpoint string        `env:"UPLOAD_S3_ENDPOINT"` // e.g. https://mybucket.s3.amazonaws.com
	UploadS3Region   string        `env:"UPLOAD_S3_REGION"        envDefault:"us-east-1"`
	UploadMaxAge     time.Duration `env:"UPLOAD_MAX_AGE"    envDefault:"2160h"` // 90 days

	// Interactive session snapshots
	SnapshotStorageDir     string        `env:"SNAPSHOT_STORAGE_DIR"         envDefault:".data/snapshots"`
	SnapshotS3Bucket       string        `env:"SNAPSHOT_S3_BUCKET"`
	SnapshotS3Prefix       string        `env:"SNAPSHOT_S3_PREFIX"           envDefault:"snapshots"`
	SnapshotS3Region       string        `env:"SNAPSHOT_S3_REGION"           envDefault:"us-east-1"`
	SnapshotS3Endpoint     string        `env:"SNAPSHOT_S3_ENDPOINT"`
	SnapshotS3UsePathStyle bool          `env:"SNAPSHOT_S3_USE_PATH_STYLE"   envDefault:"false"`
	SessionMaxIdleAge      time.Duration `env:"SESSION_MAX_IDLE_AGE"         envDefault:"2h"`
	SessionReaperInterval  time.Duration `env:"SESSION_REAPER_INTERVAL"      envDefault:"5m"`
	SessionMaxSnapshotAge  time.Duration `env:"SESSION_MAX_SNAPSHOT_AGE" envDefault:"720h"` // 30 days
	// SessionMaxRunningAge is the safety-net cutoff after which the reaper
	// fails sessions stuck in "running". Must be at or above
	// reaper.minRunningAgeFloor (max per-org timeout + handler cleanup
	// buffer + orchestrator bookkeeping margin); the reaper raises any
	// lower value and logs a warning.
	SessionMaxRunningAge time.Duration `env:"SESSION_MAX_RUNNING_AGE" envDefault:"150m"`

	// SessionFilesCacheDir is where the file-context API stages extracted
	// session workspace snapshots when a session's sandbox container has
	// already been torn down. The first read for a given snapshot pays a
	// download + extract; subsequent reads serve straight off this dir.
	// Empty disables the snapshot fallback (file-context returns NO_SANDBOX
	// the moment the container is gone, which is the pre-Phase-6 behavior).
	SessionFilesCacheDir string `env:"SESSION_FILES_CACHE_DIR" envDefault:".data/session-files-cache"`

	// SessionFilesCacheMaxBytes is the soft cap for the on-disk LRU. When
	// total extracted bytes exceed this, the oldest entries are evicted.
	// 5 GiB by default; raise on hosts that review many sessions in
	// parallel and have spare disk.
	SessionFilesCacheMaxBytes int64 `env:"SESSION_FILES_CACHE_MAX_BYTES" envDefault:"5368709120"`

	// Preview system
	ChromeWSURL             string `env:"CHROME_WS_URL"`                                                            // e.g. "ws://chrome:9222"
	PreviewOriginTemplate   string `env:"PREVIEW_ORIGIN_TEMPLATE"  envDefault:"http://{id}.preview.localhost:9090"` // {id} replaced with preview ID
	PreviewGatewayPort      int    `env:"PREVIEW_GATEWAY_PORT"     envDefault:"9090"`
	PreviewInternalBaseURL  string `env:"PREVIEW_INTERNAL_BASE_URL"`
	PreviewSnapshotCacheDir string `env:"PREVIEW_SNAPSHOT_CACHE_DIR" envDefault:".data/preview-snapshots"`
	PreviewHMRBlobDir       string `env:"PREVIEW_HMR_BLOB_DIR"     envDefault:".data/preview-hmr"`

	// Concurrency caps for the preview subsystem. Each StartPreview checks
	// these before hydrating a sandbox, so an overloaded worker returns a
	// clear "capacity reached" 503 rather than thrashing. Defaults are
	// tuned for an 8GB single-worker deployment (most-restrictive of user
	// and org, then a per-worker safety net).
	//
	// Semantics of 0: fall back to the compile-time default in
	// internal/services/preview/manager.go (currently 2 per user, 5 per
	// org, 3 per worker). 0 does NOT mean "unlimited" — if you genuinely
	// want to disable a cap, raise it to a large sentinel like 1_000_000.
	// Any value > 0 is used verbatim.
	PreviewMaxPerUser   int `env:"PREVIEW_MAX_PER_USER"   envDefault:"0"`
	PreviewMaxPerOrg    int `env:"PREVIEW_MAX_PER_ORG"    envDefault:"0"`
	PreviewMaxPerWorker int `env:"PREVIEW_MAX_PER_WORKER" envDefault:"0"`

	// Telemetry (OpenTelemetry)
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"` // e.g. "otel-collector:4318" or "https://otlp.grafana.net"
	OTLPInsecure bool   `env:"OTEL_EXPORTER_OTLP_INSECURE" envDefault:"false"`
	// RuntimeStatsInterval controls how often the worker samples per-container
	// memory/CPU usage and emits container.{memory,cpu}.{used,utilization}
	// histograms. Operators use these to size SANDBOX_* limits against actual
	// consumption rather than allocation. Set to 0 to disable sampling.
	RuntimeStatsInterval time.Duration `env:"RUNTIME_STATS_INTERVAL" envDefault:"30s"`

	// Redis (optional)
	RedisTopology   string `env:"REDIS_TOPOLOGY" envDefault:"standalone"`
	RedisURL        string `env:"REDIS_URL"`
	RedisPrivateIP  string `env:"REDIS_PRIVATE_IP"`
	RedisAddrs      string `env:"REDIS_ADDRS"`
	RedisMasterName string `env:"REDIS_MASTER_NAME"`
	RedisPassword   string `env:"REDIS_PASSWORD"`
	RedisPoolSize   int    `env:"REDIS_POOL_SIZE" envDefault:"0"`
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
	if cfg.WorkerProcessCount <= 0 {
		cfg.WorkerProcessCount = 2
	}

	// Fall back to SessionSecret for CSRF signing if not explicitly set.
	if cfg.CSRFSigningKey == "" {
		cfg.CSRFSigningKey = cfg.SessionSecret
	}

	if cfg.RedisURL == "" && cfg.RedisTopology == "standalone" && cfg.RedisPrivateIP != "" {
		cfg.RedisURL = "redis://:" + cfg.RedisPassword + "@" + cfg.RedisPrivateIP + ":6379/0"
	}

	return cfg
}

// GitHubAppEnabled reports whether the GitHub App integration should be
// wired up. Requires both app credentials and a non-demo environment: in
// DemoMode we skip GitHub App construction so the dogfood preview can boot
// with placeholder credentials without 500-ing on integration calls.
func (c *Config) GitHubAppEnabled() bool {
	return !c.DemoMode && c.GitHubAppID != 0 && c.GitHubAppPrivateKey != ""
}

// previewOriginHostIsLocal reports whether the template's host resolves to
// a loopback address in the way a browser would — i.e. the literal
// "localhost", any "*.localhost" subdomain (RFC 6761), or a loopback IP.
// Used by LogStatus to warn when PREVIEW_ORIGIN_TEMPLATE is still
// unconfigured in production. A simple substring search would false-
// positive on legitimate hostnames like "localhost.example.com".
func previewOriginHostIsLocal(template string) bool {
	// Replace the placeholder with something parseable. We only care about
	// the host, so the value of the replacement doesn't matter as long as
	// it's a valid DNS label.
	candidate := strings.ReplaceAll(template, "{id}", "x")
	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	host = strings.ToLower(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
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
		GeminiAPIKey:      c.GeminiAPIKey,
		GeminiBaseURL:     c.GeminiBaseURL,
	}
}

// PlatformLLMConfig returns the llm.Config for platform-internal features
// (titles, PR descriptions, project generation, validation, prioritization).
// Uses the cheap PLATFORM_LLM_MODEL (default: gpt-5.4-nano) regardless of what
// LLM_MODEL is set to, keeping internal feature costs low.
func (c *Config) PlatformLLMConfig() llm.Config {
	return llm.Config{
		Model:             llm.ModelName(c.PlatformLLMModel),
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
		GeminiAPIKey:      c.GeminiAPIKey,
		GeminiBaseURL:     c.GeminiBaseURL,
	}
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
	if c.GeminiAPIKey != "" {
		result["gemini"] = maskSecret(c.GeminiAPIKey)
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
		{"Backend Sentry", c.SentryDSN != "", "automatic API 5xx + panic reporting"},
		{"GitHub App", c.GitHubAppEnabled(), "webhooks, PRs"},
		{"GitHub App User Auth", c.GitHubAppClientID != "" && c.GitHubAppClientSecret != "", "user-authored PRs"},
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
	if c.GeminiAPIKey != "" {
		providers = append(providers, "gemini")
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

	if c.Env == "production" && previewOriginHostIsLocal(c.PreviewOriginTemplate) {
		logger.Warn().Str("preview_origin_template", c.PreviewOriginTemplate).Msg("PREVIEW_ORIGIN_TEMPLATE points at localhost in production — preview URLs in PR comments and the gateway will not resolve. Set PREVIEW_ORIGIN_TEMPLATE to e.g. https://{id}.preview.example.com.")
	}

	if c.DemoMode {
		logger.Warn().Msg("DEMO_MODE is enabled — GitHub integrations are stubbed, seeded credentials are public. Do not use this configuration for production data.")
		// The seeded admin in .143/seed.sql stores a bcrypt hash of
		// defaultDemoPassword. Overriding DEMO_PASSWORD without regenerating
		// the hash leaves the login-page banner pointing at credentials that
		// do not log in — a subtle footgun, so warn loudly.
		if c.DemoPassword != defaultDemoPassword || c.DemoEmail != defaultDemoEmail {
			logger.Warn().
				Msg("DEMO_EMAIL or DEMO_PASSWORD overridden but the seeded admin in .143/seed.sql still uses the defaults — the login banner will advertise credentials that don't sign in. Regenerate the bcrypt hash in the seed, or unset the override.")
		}
		// Operators enabling DemoMode on top of real GitHub App credentials
		// will see integrations silently no-op. Call that out so the cause
		// is easy to find in logs.
		if c.GitHubAppID != 0 && c.GitHubAppPrivateKey != "" {
			logger.Warn().Msg("DEMO_MODE suppresses the configured GitHub App — integrations will be skipped. Unset DEMO_MODE to re-enable.")
		}
	}
}

func (c *Config) SentryEnvironmentOrDefault() string {
	if c.SentryEnvironment != "" {
		return c.SentryEnvironment
	}
	return c.Env
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
