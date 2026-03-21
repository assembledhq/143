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
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/services/codexauth"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	threadservice "github.com/assembledhq/143/internal/services/thread"
)

func NewRouter(cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger, codexAuthSvc *codexauth.Service, llmClient llm.Client) (*chi.Mux, error) {
	// Create stores
	orgStore := db.NewOrganizationStore(pool)
	userStore := db.NewUserStore(pool)
	authSessionStore := db.NewAuthSessionStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)
	issueStore := db.NewIssueStore(pool)
	sessionStore := db.NewSessionStore(pool)
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionQuestionStore := db.NewSessionQuestionStore(pool)
	validationStore := db.NewValidationStore(pool)
	pullRequestStore := db.NewPullRequestStore(pool)
	webhookDeliveryStore := db.NewWebhookDeliveryStore(pool)
	jobStore := db.NewJobStore(pool)
	pmPlanStore := db.NewPMPlanStore(pool)
	pmDecisionLogStore := db.NewPMDecisionLogStore(pool)

	priorityScoreStore := db.NewPriorityScoreStore(pool)
	complexityEstimateStore := db.NewComplexityEstimateStore(pool)
	deployStore := db.NewDeployStore(pool)
	reviewCommentStore := db.NewReviewCommentStore(pool)
	memoryStore := db.NewMemoryStore(pool)
	invitationStore := db.NewInvitationStore(pool)
	projectStore := db.NewProjectStore(pool)
	projectTaskStore := db.NewProjectTaskStore(pool)
	projectCycleStore := db.NewProjectCycleStore(pool)
	projectAttachmentStore := db.NewProjectAttachmentStore(pool)
	projectSpecStore := db.NewProjectSpecStore(pool)
	pmDocumentStore := db.NewPMDocumentStore(pool)
	auditLogStore := db.NewAuditLogStore(pool)
	auditEmitter := db.NewAuditEmitter(auditLogStore, logger)

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
	userCredentialStore := db.NewUserCredentialStore(pool, cryptoSvc)

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
				ghSvc, pullRequestStore, sessionStore, issueStore,
				deployStore, validationStore, repoStore, jobStore, logger,
			)
			prService.SetReviewCommentStore(reviewCommentStore)
		}
	}

	// Create handlers
	healthHandler := handlers.NewHealthHandler(pool)
	authHandler := handlers.NewAuthHandler(cfg, orgStore, userStore, authSessionStore, invitationStore)
	repoHandler := handlers.NewRepositoryHandler(repoStore)
	integrationOpts := []handlers.IntegrationHandlerOption{
		handlers.WithSentryOAuth(cfg.SentryOAuthClientID, cfg.SentryOAuthClientSecret),
		handlers.WithGitHubIntegrationOAuth(cfg.GitHubOAuthClientID, cfg.GitHubOAuthClientSecret),
		handlers.WithGitHubAppSlug(cfg.GitHubAppSlug),
		handlers.WithSlackOAuth(cfg.SlackOAuthClientID, cfg.SlackOAuthClientSecret),
	}
	// If the GitHub App service is available, let the integration handler
	// fetch repos directly from the API during the install redirect.
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPrivateKey != "" {
		ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
		if err == nil {
			integrationOpts = append(integrationOpts, handlers.WithGitHubApp(ghSvc, repoStore))
		}
	}
	integrationHandler := handlers.NewIntegrationHandler(
		integrationStore,
		credentialStore,
		cfg.LinearOAuthClientID,
		cfg.LinearOAuthClientSecret,
		cfg.BaseURL,
		cfg.FrontendURL,
		integrationOpts...,
	)
	webhookHandler := handlers.NewWebhookHandler(cfg, orgStore, userStore, repoStore, integrationStore, prService)
	settingsHandler := handlers.NewSettingsHandler(orgStore, cfg.SafeAgentEnv(), cfg.SafeLLMEnv())
	issueHandler := handlers.NewIssueHandler(issueStore)
	sessionMessageStore := db.NewSessionMessageStore(pool)
	sessionThreadStore := db.NewSessionThreadStore(pool)
	sessionHandler := handlers.NewSessionHandler(
		sessionStore,
		sessionLogStore,
		sessionQuestionStore,
		validationStore,
		pullRequestStore,
		issueStore,
		repoStore,
		orgStore,
		jobStore,
		sessionMessageStore,
		sessionThreadStore,
		llmClient,
		logger,
	)
	threadSvc := threadservice.NewService(
		sessionThreadStore,
		sessionStore,
		sessionMessageStore,
		sessionLogStore,
		jobStore,
		logger,
	)
	sessionThreadHandler := handlers.NewSessionThreadHandler(threadSvc)
	pmHandler := handlers.NewPMHandler(pmPlanStore, pmDecisionLogStore, jobStore, orgStore)
	priorityHandler := handlers.NewPriorityHandler(priorityScoreStore, complexityEstimateStore, jobStore)
	ingestionWebhookHandler := handlers.NewIngestionWebhookHandler(webhookDeliveryStore, integrationStore, credentialStore, ingestionSvc, logger)
	credentialHandler := handlers.NewCredentialHandler(credentialStore)
	memoryHandler := handlers.NewMemoryHandler(memoryStore, reviewCommentStore)
	userCredentialHandler := handlers.NewUserCredentialHandler(userCredentialStore, credentialStore, userStore)
	teamHandler := handlers.NewTeamHandler(userStore, authSessionStore, invitationStore, orgStore, cfg.FrontendURL)

	projectHandler := handlers.NewProjectHandler(projectStore, projectTaskStore, projectCycleStore, projectAttachmentStore, projectSpecStore)
	projectHandler.SetJobStore(jobStore)

	// Wire audit emitter into all handlers that perform state changes.
	authHandler.SetAuditEmitter(auditEmitter)
	sessionHandler.SetAuditEmitter(auditEmitter)
	teamHandler.SetAuditEmitter(auditEmitter)
	settingsHandler.SetAuditEmitter(auditEmitter)
	credentialHandler.SetAuditEmitter(auditEmitter)
	projectHandler.SetAuditEmitter(auditEmitter)
	pmHandler.SetAuditEmitter(auditEmitter)
	projectAttachmentHandler := handlers.NewProjectAttachmentHandler(projectAttachmentStore, projectStore)
	projectSpecHandler := handlers.NewProjectSpecHandler(projectSpecStore, projectStore)
	projectAnalysisHandler := handlers.NewProjectAnalysisHandler(projectStore, projectSpecStore, projectAttachmentStore, projectTaskStore)
	projectGenerateHandler := handlers.NewProjectGenerateHandler(llmClient)
	codexAuthHandler := handlers.NewCodexAuthHandler(codexAuthSvc, logger)
	pmDocumentHandler := handlers.NewPMDocumentHandler(pmDocumentStore, credentialStore)
	auditLogHandler := handlers.NewAuditLogHandler(auditLogStore)

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
		r.Use(middleware.Auth(authSessionStore, userStore))
		r.Use(middleware.OrgContext)
		r.Use(middleware.LogContext(logger))
		r.Use(middleware.CSRF(cfg.CSRFSigningKey, logger))

		r.Get("/api/v1/auth/me", authHandler.Me)
		r.Post("/api/v1/auth/logout", authHandler.Logout)

		// Read-only routes (all roles: admin, member, viewer)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member", "viewer"))

			// Personal and resolved credential views
			r.Get("/api/v1/settings/credentials/personal", userCredentialHandler.ListPersonal)
			r.Get("/api/v1/settings/credentials/resolved", userCredentialHandler.ListResolved)
			r.Get("/api/v1/settings/credentials/team", userCredentialHandler.ListTeamDefaults)

			r.Get("/api/v1/repositories", repoHandler.List)
			r.Get("/api/v1/repositories/summary", repoHandler.Summary)
			r.Get("/api/v1/repositories/{id}", repoHandler.Get)
			r.Get("/api/v1/integrations", integrationHandler.ListIntegrations)
			r.Get("/api/v1/issues", issueHandler.List)
			r.Get("/api/v1/issues/{id}", issueHandler.Get)
			r.Get("/api/v1/issues/{id}/priority", priorityHandler.GetPriorityScore)
			r.Get("/api/v1/issues/{id}/complexity", priorityHandler.GetComplexity)
			r.Get("/api/v1/priority-scores", priorityHandler.ListPriorityScores)
			r.Get("/api/v1/memories/*", memoryHandler.ListByRepo)
			r.Get("/api/v1/review-comments", memoryHandler.ListComments)
			r.Get("/api/v1/sessions", sessionHandler.List)
			r.Get("/api/v1/sessions/{id}", sessionHandler.Get)
			r.Get("/api/v1/sessions/{id}/logs", sessionHandler.GetLogs)
			r.Get("/api/v1/sessions/{id}/logs/stream", sessionHandler.StreamLogs)
			r.Get("/api/v1/sessions/{id}/validation", sessionHandler.GetValidation)
			r.Get("/api/v1/sessions/{id}/pr", sessionHandler.GetPullRequest)
			r.Get("/api/v1/sessions/{id}/questions", sessionHandler.ListQuestions)
			r.Get("/api/v1/sessions/{id}/messages", sessionHandler.ListMessages)
			r.Get("/api/v1/sessions/{id}/threads", sessionThreadHandler.ListThreads)
			r.Get("/api/v1/sessions/{id}/threads/{tid}", sessionThreadHandler.GetThread)
			r.Get("/api/v1/sessions/{id}/threads/{tid}/messages", sessionThreadHandler.GetThreadMessages)
			r.Get("/api/v1/sessions/{id}/threads/{tid}/logs", sessionThreadHandler.GetThreadLogs)
			r.Get("/api/v1/settings", settingsHandler.Get)
			r.Get("/api/v1/settings/agent-defaults", settingsHandler.GetAgentDefaults)
			r.Get("/api/v1/settings/llm-defaults", settingsHandler.GetLLMDefaults)
		r.Get("/api/v1/settings/llm-models", settingsHandler.GetLLMModels)
			r.Get("/api/v1/pm/plans", pmHandler.List)
			r.Get("/api/v1/pm/plans/{id}", pmHandler.Get)
			r.Get("/api/v1/pm/plans/latest", pmHandler.Latest)
			r.Get("/api/v1/pm/decisions", pmHandler.Decisions)
			r.Get("/api/v1/pm/status", pmHandler.Status)
			r.Get("/api/v1/projects", projectHandler.List)
			r.Get("/api/v1/projects/{id}", projectHandler.Get)
			r.Get("/api/v1/projects/{id}/cycles", projectHandler.ListCycles)
			r.Get("/api/v1/projects/{id}/cycles/{cycleId}", projectHandler.GetCycle)
			r.Get("/api/v1/projects/{id}/attachments", projectAttachmentHandler.List)
			r.Get("/api/v1/projects/{id}/specs", projectSpecHandler.List)
			r.Get("/api/v1/projects/{id}/specs/{specId}", projectSpecHandler.Get)
			r.Get("/api/v1/pm/documents", pmDocumentHandler.List)
			r.Get("/api/v1/pm/documents/{docId}", pmDocumentHandler.Get)
		})

		// Write routes (admin and member only)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "member"))

			r.Patch("/api/v1/repositories/{id}", repoHandler.Update)
			r.Get("/api/v1/integrations/linear/login", integrationHandler.StartLinearOAuth)
			r.Get("/api/v1/integrations/linear/callback", integrationHandler.HandleLinearOAuthCallback)
			r.Post("/api/v1/integrations/linear/connect", integrationHandler.ConnectLinear)
			r.Get("/api/v1/integrations/sentry/login", integrationHandler.StartSentryOAuth)
			r.Get("/api/v1/integrations/sentry/callback", integrationHandler.HandleSentryOAuthCallback)
			r.Post("/api/v1/integrations/sentry/connect", integrationHandler.ConnectSentry)
			r.Get("/api/v1/integrations/github/login", integrationHandler.StartGitHubOAuth)
			r.Get("/api/v1/integrations/github/callback", integrationHandler.HandleGitHubOAuthCallback)
			r.Get("/api/v1/integrations/github/installed", integrationHandler.HandleGitHubAppInstalled)
			r.Post("/api/v1/integrations/github/connect", integrationHandler.ConnectGitHub)
			r.Post("/api/v1/integrations/github/sync", integrationHandler.SyncGitHubRepos)
			r.Get("/api/v1/integrations/slack/login", integrationHandler.StartSlackOAuth)
			r.Get("/api/v1/integrations/slack/callback", integrationHandler.HandleSlackOAuthCallback)
			r.Post("/api/v1/integrations/slack/connect", integrationHandler.ConnectSlack)
			r.Get("/api/v1/integrations/slack/channels", integrationHandler.ListSlackChannels)
			r.Patch("/api/v1/integrations/slack/channels", integrationHandler.UpdateSlackChannels)
			r.Delete("/api/v1/integrations/github/disconnect", integrationHandler.DisconnectIntegration)
			r.Delete("/api/v1/integrations/sentry/disconnect", integrationHandler.DisconnectIntegration)
			r.Delete("/api/v1/integrations/linear/disconnect", integrationHandler.DisconnectIntegration)
			r.Delete("/api/v1/integrations/slack/disconnect", integrationHandler.DisconnectIntegration)
			// Personal credential management
			r.Put("/api/v1/settings/credentials/personal/{provider}", userCredentialHandler.UpsertPersonal)
			r.Delete("/api/v1/settings/credentials/personal/{provider}", userCredentialHandler.DeletePersonal)

			r.Post("/api/v1/issues/{id}/fix", sessionHandler.TriggerFix)
			r.Post("/api/v1/sessions/manual", sessionHandler.CreateManual)
			r.Post("/api/v1/sessions/{id}/questions/{qid}/answer", sessionHandler.AnswerQuestion)
			r.Post("/api/v1/sessions/{id}/messages", sessionHandler.SendMessage)
			r.Post("/api/v1/sessions/{id}/end", sessionHandler.EndSession)
			r.Post("/api/v1/sessions/{id}/threads", sessionThreadHandler.CreateThread)
			r.Post("/api/v1/sessions/{id}/threads/{tid}/messages", sessionThreadHandler.SendThreadMessage)
			r.Post("/api/v1/sessions/{id}/threads/{tid}/end", sessionThreadHandler.EndThread)
			r.Post("/api/v1/projects", projectHandler.Create)
			r.Patch("/api/v1/projects/{id}", projectHandler.Update)
			r.Delete("/api/v1/projects/{id}", projectHandler.Delete)
			r.Post("/api/v1/projects/{id}/start", projectHandler.Start)
			r.Post("/api/v1/projects/{id}/pause", projectHandler.Pause)
			r.Post("/api/v1/projects/{id}/resume", projectHandler.Resume)
			r.Post("/api/v1/projects/{id}/approve", projectHandler.Approve)
			r.Post("/api/v1/projects/{id}/dismiss", projectHandler.Dismiss)
			r.Post("/api/v1/projects/{id}/run", projectHandler.RunNow)
			r.Post("/api/v1/projects/{id}/tasks", projectHandler.CreateTask)
			r.Patch("/api/v1/projects/{id}/tasks/{taskId}", projectHandler.UpdateTask)
			r.Delete("/api/v1/projects/{id}/tasks/{taskId}", projectHandler.DeleteTask)
			r.Post("/api/v1/projects/{id}/tasks/{taskId}/retry", projectHandler.RetryTask)
			r.Post("/api/v1/projects/{id}/attachments", projectAttachmentHandler.Create)
			r.Patch("/api/v1/projects/{id}/attachments/{attachmentId}", projectAttachmentHandler.Update)
			r.Delete("/api/v1/projects/{id}/attachments/{attachmentId}", projectAttachmentHandler.Delete)
			r.Post("/api/v1/projects/{id}/specs", projectSpecHandler.Create)
			r.Patch("/api/v1/projects/{id}/specs/{specId}", projectSpecHandler.Update)
			r.Delete("/api/v1/projects/{id}/specs/{specId}", projectSpecHandler.Delete)
			r.Post("/api/v1/projects/ai/generate", projectGenerateHandler.Generate)
			r.Post("/api/v1/projects/{id}/ai/improve", projectAnalysisHandler.Improve)
			r.Post("/api/v1/pm/documents", pmDocumentHandler.Create)
			r.Patch("/api/v1/pm/documents/{docId}", pmDocumentHandler.Update)
			r.Delete("/api/v1/pm/documents/{docId}", pmDocumentHandler.Delete)
			r.Post("/api/v1/pm/documents/{docId}/sync", pmDocumentHandler.SyncFromNotion)
			r.Post("/api/v1/pm/documents/discover/notion", pmDocumentHandler.DiscoverNotion)
		})

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))

			r.Delete("/api/v1/repositories/{id}", repoHandler.Delete)
			r.Post("/api/v1/issues/{id}/reprioritize", priorityHandler.Reprioritize)
			r.Post("/api/v1/pm/analyze", pmHandler.Analyze)
			r.Patch("/api/v1/settings", settingsHandler.Update)
			r.Post("/api/v1/memories", memoryHandler.Create)
			r.Patch("/api/v1/memories/{id}", memoryHandler.UpdateStatus)
			r.Put("/api/v1/memories/{id}", memoryHandler.UpdateRule)

			// Org credential management
			r.Get("/api/v1/settings/credentials", credentialHandler.List)
			r.Put("/api/v1/settings/credentials/{provider}", credentialHandler.Update)
			r.Delete("/api/v1/settings/credentials/{provider}", credentialHandler.Delete)

			// Team default credential management
			r.Put("/api/v1/settings/credentials/team/{provider}", userCredentialHandler.SetTeamDefault)
			r.Delete("/api/v1/settings/credentials/team/{provider}", userCredentialHandler.DeleteTeamDefault)

			// Codex (ChatGPT) OAuth device code auth
			r.Post("/api/v1/settings/codex-auth/initiate", codexAuthHandler.Initiate)
			r.Get("/api/v1/settings/codex-auth/status", codexAuthHandler.Status)
			r.Post("/api/v1/settings/codex-auth/disconnect", codexAuthHandler.Disconnect)

			// Audit logs
			r.Get("/api/v1/audit-logs", auditLogHandler.List)
			r.Get("/api/v1/audit-logs/{id}", auditLogHandler.Get)

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
