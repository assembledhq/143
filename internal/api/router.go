package api

import (
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/handlers"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/ingestion"
)

func NewRouter(cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger) *chi.Mux {
	// Create stores
	orgStore := db.NewOrganizationStore(pool)
	userStore := db.NewUserStore(pool)
	sessionStore := db.NewSessionStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)
	issueStore := db.NewIssueStore(pool)
	agentRunStore := db.NewAgentRunStore(pool)
	webhookDeliveryStore := db.NewWebhookDeliveryStore(pool)
	jobStore := db.NewJobStore(pool)

	// Create services
	ingestionSvc := ingestion.NewService(issueStore, webhookDeliveryStore, jobStore, logger)

	// Create handlers
	healthHandler := handlers.NewHealthHandler(pool)
	authHandler := handlers.NewAuthHandler(cfg, orgStore, userStore, sessionStore)
	repoHandler := handlers.NewRepositoryHandler(repoStore)
	webhookHandler := handlers.NewWebhookHandler(cfg, orgStore, repoStore, integrationStore)
	settingsHandler := handlers.NewSettingsHandler(orgStore)
	issueHandler := handlers.NewIssueHandler(issueStore)
	runHandler := handlers.NewRunHandler(agentRunStore)
	ingestionWebhookHandler := handlers.NewIngestionWebhookHandler(webhookDeliveryStore, integrationStore, ingestionSvc, logger)

	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RequestID)
	r.Use(middleware.Logging(logger))
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.CORS(cfg.CORSAllowedOrigins))
	r.Use(middleware.MaxBodySize(1 << 20)) // 1MB request body limit
	r.Use(middleware.RateLimit(middleware.DefaultRateLimitConfig()))
	r.Use(middleware.Metrics)

	// Public routes (no auth, no rate limit beyond global)
	r.Get("/healthz", healthHandler.Healthz)
	r.Get("/readyz", healthHandler.Readyz)
	r.Handle("/metrics", promhttp.Handler())

	// Webhook routes (no auth — called by external services, signature verified per-provider)
	r.Route("/api/v1/webhooks", func(r chi.Router) {
		r.Post("/github", webhookHandler.HandleGitHub)
		r.Group(func(r chi.Router) {
			r.Use(middleware.VerifyWebhookSignature("X-Sentry-Hook-Signature", cfg.SentryWebhookSecret, ""))
			r.Post("/sentry", ingestionWebhookHandler.HandleSentry)
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.VerifyWebhookSignature("X-Linear-Signature", cfg.LinearWebhookSecret, ""))
			r.Post("/linear", ingestionWebhookHandler.HandleLinear)
		})
	})

	// Auth routes (no auth)
	r.Get("/api/v1/auth/github/login", authHandler.Login)
	r.Get("/api/v1/auth/github/callback", authHandler.Callback)

	// Protected routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(sessionStore, userStore))
		r.Use(middleware.OrgContext)

		r.Post("/api/v1/auth/logout", authHandler.Logout)

		// Read-only routes (all roles: admin, member, viewer)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member", "viewer"))

			r.Get("/api/v1/repositories", repoHandler.List)
			r.Get("/api/v1/repositories/{id}", repoHandler.Get)
			r.Get("/api/v1/issues", issueHandler.List)
			r.Get("/api/v1/issues/{id}", issueHandler.Get)
			r.Get("/api/v1/runs", runHandler.List)
			r.Get("/api/v1/runs/{id}", runHandler.Get)
			r.Get("/api/v1/settings", settingsHandler.Get)
		})

		// Write routes (admin and member only)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member"))

			r.Patch("/api/v1/repositories/{id}", repoHandler.Update)
		})

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))

			r.Delete("/api/v1/repositories/{id}", repoHandler.Delete)
			r.Patch("/api/v1/settings", settingsHandler.Update)
		})
	})

	return r
}
