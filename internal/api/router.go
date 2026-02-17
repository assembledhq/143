package api

import (
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/handlers"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
)

func NewRouter(cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger) *chi.Mux {
	// Create stores
	orgStore := db.NewOrganizationStore(pool)
	userStore := db.NewUserStore(pool)
	sessionStore := db.NewSessionStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)

	// Create handlers
	healthHandler := handlers.NewHealthHandler(pool)
	authHandler := handlers.NewAuthHandler(cfg, orgStore, userStore, sessionStore)
	repoHandler := handlers.NewRepositoryHandler(repoStore)
	webhookHandler := handlers.NewWebhookHandler(cfg, orgStore, repoStore, integrationStore)
	settingsHandler := handlers.NewSettingsHandler(orgStore)

	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RequestID)
	r.Use(middleware.Logging(logger))
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.CORS(cfg.CORSAllowedOrigins))

	// Public routes
	r.Get("/healthz", healthHandler.Healthz)
	r.Get("/readyz", healthHandler.Readyz)

	// Webhook routes (no auth)
	r.Post("/api/v1/webhooks/github", webhookHandler.HandleGitHub)

	// Auth routes (no auth)
	r.Get("/api/v1/auth/github/login", authHandler.Login)
	r.Get("/api/v1/auth/github/callback", authHandler.Callback)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(sessionStore, userStore))
		r.Use(middleware.OrgContext)

		r.Post("/api/v1/auth/logout", authHandler.Logout)

		// Repositories
		r.Get("/api/v1/repositories", repoHandler.List)
		r.Get("/api/v1/repositories/{id}", repoHandler.Get)
		r.Patch("/api/v1/repositories/{id}", repoHandler.Update)
		r.Delete("/api/v1/repositories/{id}", repoHandler.Delete)

		// Settings
		r.Get("/api/v1/settings", settingsHandler.Get)
		r.Patch("/api/v1/settings", settingsHandler.Update)
	})

	return r
}
