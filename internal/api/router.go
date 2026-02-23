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
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/codexauth"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
)

func NewRouter(cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger, codexAuthSvc *codexauth.Service) (*chi.Mux, error) {
	// Create stores
	orgStore := db.NewOrganizationStore(pool)
	userStore := db.NewUserStore(pool)
	sessionStore := db.NewSessionStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)
	issueStore := db.NewIssueStore(pool)
	agentRunStore := db.NewAgentRunStore(pool)
	agentRunLogStore := db.NewAgentRunLogStore(pool)
	agentRunQuestionStore := db.NewAgentRunQuestionStore(pool)
	validationStore := db.NewValidationStore(pool)
	pullRequestStore := db.NewPullRequestStore(pool)
	webhookDeliveryStore := db.NewWebhookDeliveryStore(pool)
	jobStore := db.NewJobStore(pool)

	priorityScoreStore := db.NewPriorityScoreStore(pool)
	complexityEstimateStore := db.NewComplexityEstimateStore(pool)
	deployStore := db.NewDeployStore(pool)
	reviewCommentStore := db.NewReviewCommentStore(pool)
	reviewPatternStore := db.NewReviewPatternStore(pool)
	invitationStore := db.NewInvitationStore(pool)

	// Create credential store with optional encryption.
	var cryptoSvc *crypto.Service
	if cfg.EncryptionMasterKey != "" {
		var err error
		cryptoSvc, err = crypto.NewService(cfg.EncryptionMasterKey)
		if err != nil {
			return nil, err
		}
	}
	credentialStore := db.NewOrgCredentialStore(pool, cryptoSvc)

	// Create services
	ingestionSvc := ingestion.NewService(issueStore, webhookDeliveryStore, jobStore, logger)

	// Create PRService if GitHub App credentials are configured.
	var prService *ghservice.PRService
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPrivateKey != "" {
		ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
		if err != nil {
			logger.Warn().Err(err).Msg("failed to initialize GitHub App service, PR webhooks will be disabled")
		} else {
			prService = ghservice.NewPRService(
				ghSvc, pullRequestStore, agentRunStore, issueStore,
				deployStore, validationStore, repoStore, jobStore, logger,
			)
			prService.SetReviewCommentStore(reviewCommentStore)
		}
	}

	// Create handlers
	healthHandler := handlers.NewHealthHandler(pool)
	authHandler := handlers.NewAuthHandler(cfg, orgStore, userStore, sessionStore, invitationStore)
	repoHandler := handlers.NewRepositoryHandler(repoStore)
	integrationHandler := handlers.NewIntegrationHandler(integrationStore)
	webhookHandler := handlers.NewWebhookHandler(cfg, orgStore, repoStore, integrationStore, prService)
	settingsHandler := handlers.NewSettingsHandler(orgStore, cfg.SafeAgentEnv())
	issueHandler := handlers.NewIssueHandler(issueStore)
	runHandler := handlers.NewRunHandler(
		agentRunStore,
		agentRunLogStore,
		agentRunQuestionStore,
		validationStore,
		pullRequestStore,
		issueStore,
		orgStore,
		jobStore,
	)
	priorityHandler := handlers.NewPriorityHandler(priorityScoreStore, complexityEstimateStore, jobStore)
	ingestionWebhookHandler := handlers.NewIngestionWebhookHandler(webhookDeliveryStore, integrationStore, credentialStore, ingestionSvc, logger)
	credentialHandler := handlers.NewCredentialHandler(credentialStore)
	reviewPatternHandler := handlers.NewReviewPatternHandler(reviewPatternStore, reviewCommentStore)
	teamHandler := handlers.NewTeamHandler(userStore, sessionStore, invitationStore, orgStore, cfg.FrontendURL)

	codexAuthHandler := handlers.NewCodexAuthHandler(codexAuthSvc)

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
		r.Post("/sentry", ingestionWebhookHandler.HandleSentry)
		r.Post("/linear", ingestionWebhookHandler.HandleLinear)
	})

	// Public team routes (token-based, no auth)
	r.Post("/api/v1/team/invitations/accept", teamHandler.AcceptInvitation)

	// Auth routes (no auth)
	r.Get("/api/v1/auth/providers", authHandler.Providers)
	r.Get("/api/v1/auth/github/login", authHandler.Login)
	r.Get("/api/v1/auth/github/callback", authHandler.Callback)
	r.Get("/api/v1/auth/google/login", authHandler.GoogleLogin)
	r.Get("/api/v1/auth/google/callback", authHandler.GoogleCallback)
	r.Post("/api/v1/auth/register", authHandler.Register)
	r.Post("/api/v1/auth/login", authHandler.EmailLogin)

	// Protected routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(sessionStore, userStore))
		r.Use(middleware.OrgContext)
		r.Use(middleware.CSRF(cfg.CSRFSigningKey))

		r.Get("/api/v1/auth/me", authHandler.Me)
		r.Post("/api/v1/auth/logout", authHandler.Logout)

		// Read-only routes (all roles: admin, member, viewer)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member", "viewer"))

			r.Get("/api/v1/repositories", repoHandler.List)
			r.Get("/api/v1/repositories/{id}", repoHandler.Get)
			r.Get("/api/v1/integrations", integrationHandler.ListIntegrations)
			r.Get("/api/v1/issues", issueHandler.List)
			r.Get("/api/v1/issues/{id}", issueHandler.Get)
			r.Get("/api/v1/issues/{id}/priority", priorityHandler.GetPriorityScore)
			r.Get("/api/v1/issues/{id}/complexity", priorityHandler.GetComplexity)
			r.Get("/api/v1/priority-scores", priorityHandler.ListPriorityScores)
			r.Get("/api/v1/review-patterns/*", reviewPatternHandler.ListByRepo)
			r.Get("/api/v1/review-comments", reviewPatternHandler.ListComments)
			r.Get("/api/v1/runs", runHandler.List)
			r.Get("/api/v1/runs/{id}", runHandler.Get)
			r.Get("/api/v1/runs/{id}/logs", runHandler.StreamLogs)
			r.Get("/api/v1/runs/{id}/validation", runHandler.GetValidation)
			r.Get("/api/v1/runs/{id}/pr", runHandler.GetPullRequest)
			r.Get("/api/v1/runs/{id}/questions", runHandler.ListQuestions)
			r.Get("/api/v1/settings", settingsHandler.Get)
			r.Get("/api/v1/settings/agent-defaults", settingsHandler.GetAgentDefaults)
		})

		// Write routes (admin and member only)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member"))

			r.Patch("/api/v1/repositories/{id}", repoHandler.Update)
			r.Post("/api/v1/issues/{id}/fix", runHandler.TriggerFix)
			r.Post("/api/v1/runs/{id}/questions/{qid}/answer", runHandler.AnswerQuestion)
		})

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))

			r.Delete("/api/v1/repositories/{id}", repoHandler.Delete)
			r.Post("/api/v1/issues/{id}/reprioritize", priorityHandler.Reprioritize)
			r.Patch("/api/v1/settings", settingsHandler.Update)
			r.Patch("/api/v1/review-patterns/{id}", reviewPatternHandler.UpdateStatus)
			r.Put("/api/v1/review-patterns/{id}", reviewPatternHandler.UpdateRule)

			// Credential management
			r.Get("/api/v1/settings/credentials", credentialHandler.List)
			r.Put("/api/v1/settings/credentials/{provider}", credentialHandler.Update)
			r.Delete("/api/v1/settings/credentials/{provider}", credentialHandler.Delete)

			// Codex (ChatGPT) OAuth device code auth
			r.Post("/api/v1/settings/codex-auth/initiate", codexAuthHandler.Initiate)
			r.Get("/api/v1/settings/codex-auth/status", codexAuthHandler.Status)
			r.Post("/api/v1/settings/codex-auth/disconnect", codexAuthHandler.Disconnect)

      // Team management
			r.Get("/api/v1/team/members", teamHandler.ListMembers)
			r.Patch("/api/v1/team/members/{id}/role", teamHandler.ChangeRole)
			r.Delete("/api/v1/team/members/{id}", teamHandler.RemoveMember)
			r.Get("/api/v1/team/invitations", teamHandler.ListInvitations)
			r.Post("/api/v1/team/invitations", teamHandler.CreateInvitation)
			r.Delete("/api/v1/team/invitations/{id}", teamHandler.RevokeInvitation)
		})
	})

	return r, nil
}
