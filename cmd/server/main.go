package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api"
	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/codexauth"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/services/validation"
	"github.com/assembledhq/143/internal/version"
	"github.com/assembledhq/143/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := logging.NewLogger(cfg.LogLevel)
	cfg.LogStatus(logger)

	if version.IsDev() {
		logger.Warn().Msg("BuildSHA is \"dev\" — ldflags not injected; input manifests will not be reproducible")
	} else {
		logger.Info().Str("build_sha", version.BuildSHA).Msg("server build version")
	}

	if err := cfg.ValidateSecrets(); err != nil {
		logger.Fatal().Err(err).Msg("security configuration check failed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	// Create codex auth service (shared between router and orchestrator).
	var cryptoSvc *crypto.Service
	if cfg.EncryptionMasterKey != "" {
		cryptoSvc, err = crypto.NewService(cfg.EncryptionMasterKey)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to initialize crypto service")
		}
	}
	credentialStore := db.NewOrgCredentialStore(pool, cryptoSvc)
	userCredentialStore := db.NewUserCredentialStore(pool, cryptoSvc)
	codexAuthSvc := codexauth.NewService(credentialStore, logger)

	// LLM client (shared between router and worker services).
	llmClient, err := llm.NewClient(cfg.LLMConfig(), logger)
	if err != nil {
		logger.Warn().Err(err).Msg("LLM client initialization failed — LLM-dependent features will be unavailable")
	}

	// Create file reader for sandbox file browsing (optional — gracefully degrades
	// to a no-op reader if Docker is not available).
	var fileReader sandbox.FileReader
	if apiDockerCli, dockerErr := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()); dockerErr == nil {
		defer apiDockerCli.Close()
		fileReader = sandbox.NewDockerFileReader(apiDockerCli)
	} else {
		logger.Warn().Err(dockerErr).Msg("Docker not available for API file browsing — repo explorer will be disabled")
		fileReader = sandbox.NoOpFileReader{}
	}

	cancelRegistry := agent.NewCancelRegistry(logger)
	router, err := api.NewRouter(cfg, pool, logger, codexAuthSvc, llmClient, fileReader, cancelRegistry)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize API router")
	}

	// Start worker if mode includes worker capability
	if cfg.Mode == "all" || cfg.Mode == "worker" {
		hostname, _ := os.Hostname()
		w := worker.New(pool, logger, hostname)

		issueStore := db.NewIssueStore(pool)
		sessionStore := db.NewSessionStore(pool)
		jobStore := db.NewJobStore(pool)
		orgStore := db.NewOrganizationStore(pool)
		repoStore := db.NewRepositoryStore(pool)
		integrationStore := db.NewIntegrationStore(pool)
		validationStore := db.NewValidationStore(pool)
		pullRequestStore := db.NewPullRequestStore(pool)
		deployStore := db.NewDeployStore(pool)
		sessionMessageStore := db.NewSessionMessageStore(pool)
		priorityScoreStore := db.NewPriorityScoreStore(pool)
		complexityEstimateStore := db.NewComplexityEstimateStore(pool)
		pmPlanStore := db.NewPMPlanStore(pool)
		pmDecisionLogStore := db.NewPMDecisionLogStore(pool)
		projectStore := db.NewProjectStore(pool)
		projectTaskStore := db.NewProjectTaskStore(pool)
		projectCycleStore := db.NewProjectCycleStore(pool)
		pmDocumentStore := db.NewPMDocumentStore(pool)
		snapshotStore := storage.NewFileSnapshotStore(cfg.SnapshotStorageDir)

		auditLogStore := db.NewAuditLogStore(pool)
		sessionLogStore := db.NewSessionLogStore(pool)

		stores := &worker.Stores{
			Issues:              issueStore,
			Sessions:            sessionStore,
			Jobs:                jobStore,
			Integrations:        integrationStore,
			Webhooks:            db.NewWebhookDeliveryStore(pool),
			PriorityScores:      priorityScoreStore,
			ComplexityEstimates: complexityEstimateStore,
			Projects:            projectStore,
			ProjectTasks:        projectTaskStore,
			Credentials:         credentialStore,
			AuditLogs:           auditLogStore,
			Organizations:       orgStore,
			SessionLogs:         sessionLogStore,
			EvalTasks:           db.NewEvalTaskStore(pool),
			EvalRuns:            db.NewEvalRunStore(pool),
			EvalBatches:         db.NewEvalBatchStore(pool),
			EvalBootstraps:      db.NewEvalBootstrapStore(pool),
			Repositories:        repoStore,
			SessionMessages:     sessionMessageStore,
		}

		// Build Phase 3+ services if runtime dependencies are available.
		var services *worker.Services
		if canBuildServices(cfg, logger) {
			services = buildServices(cfg, pool, logger, codexAuthSvc, credentialStore, userCredentialStore, issueStore, sessionStore,
				jobStore, orgStore, repoStore, validationStore, pullRequestStore,
				deployStore, priorityScoreStore, complexityEstimateStore, pmPlanStore, pmDecisionLogStore,
				projectStore, projectTaskStore, projectCycleStore, pmDocumentStore, integrationStore,
				sessionMessageStore, snapshotStore, cancelRegistry)
		}
		retentionCfg := worker.DataRetentionConfig{
			WebhookDays: cfg.DataRetentionWebhookDays,
			LogsDays:    cfg.DataRetentionLogsDays,
			JobsDays:    cfg.DataRetentionJobsDays,
		}
		worker.RegisterHandlers(w, stores, services, retentionCfg, logger)
		go w.Start(ctx)
		logger.Info().Msg("worker started with registered handlers")

		reaper := agent.NewSessionReaper(sessionStore, snapshotStore, cfg.SessionMaxIdleAge, cfg.SessionMaxSnapshotAge, cfg.SessionReaperInterval, logger)
		go reaper.Run(ctx)

		// Upload reaper: clean up old uploaded files (local mode only; use S3 lifecycle rules for S3).
		uploadStore := storage.NewFileUploadStore(cfg.UploadStorageDir, "")
		uploadReaper := storage.NewUploadReaper(uploadStore, cfg.UploadMaxAge, cfg.SessionReaperInterval, logger)
		go uploadReaper.Run(ctx)

		scheduler := cluster.NewScheduler(
			cluster.NewSchedulerLock(pool),
			jobStore,
			orgStore,
			integrationStore,
			pmPlanStore,
			repoStore,
			logger,
		)
		scheduler.SetProjectStore(projectStore)
		scheduler.SetPMDocStore(pmDocumentStore)
		go scheduler.Start(ctx, 10*time.Minute)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info().Msg("shutting down server...")
		cancel() // stop worker
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("server shutdown failed")
		}
	}()

	logger.Info().Int("port", cfg.Port).Str("mode", cfg.Mode).Msg("starting server")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server failed")
	}
}

// canBuildServices checks whether the runtime dependencies for Phase 3+
// services (agent orchestrator, validation, PR creation, failure analysis)
// are configured. Returns false with a log message if not.
func canBuildServices(cfg *config.Config, logger zerolog.Logger) bool {
	if cfg.GitHubAppID == 0 || cfg.GitHubAppPrivateKey == "" {
		logger.Warn().Msg("GitHub App not configured — agent/validation/PR services disabled")
		return false
	}
	return true
}

// buildServices constructs the full set of Phase 3+ worker services.
func buildServices(
	cfg *config.Config,
	pool *pgxpool.Pool,
	logger zerolog.Logger,
	codexAuthSvc *codexauth.Service,
	credentialStore *db.OrgCredentialStore,
	userCredentialStore *db.UserCredentialStore,
	issueStore *db.IssueStore,
	sessionStore *db.SessionStore,
	jobStore *db.JobStore,
	orgStore *db.OrganizationStore,
	repoStore *db.RepositoryStore,
	validationStore *db.ValidationStore,
	pullRequestStore *db.PullRequestStore,
	deployStore *db.DeployStore,
	priorityScoreStore *db.PriorityScoreStore,
	complexityEstimateStore *db.ComplexityEstimateStore,
	pmPlanStore *db.PMPlanStore,
	pmDecisionLogStore *db.PMDecisionLogStore,
	projectStore *db.ProjectStore,
	projectTaskStore *db.ProjectTaskStore,
	projectCycleStore *db.ProjectCycleStore,
	pmDocumentStore *db.PMDocumentStore,
	integrationStore *db.IntegrationStore,
	sessionMessageStore *db.SessionMessageStore,
	snapshotStore storage.SnapshotStore,
	cancelRegistry *agent.CancelRegistry,
) *worker.Services {
	// GitHub App service (for installation tokens, PR creation).
	ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
	if err != nil {
		logger.Error().Err(err).Msg("failed to initialize GitHub App service — all Phase 3+ services disabled")
		return nil
	}

	// Docker sandbox provider.
	dockerCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error().Err(err).Msg("Docker not available — all Phase 3+ services disabled")
		return nil
	}
	sandboxProvider := providers.NewDockerProvider(dockerCli, logger, providers.WithRuntime(cfg.SandboxRuntime))

	// Runtime health check: verify gVisor works if required.
	if cfg.SandboxRuntime == "runsc" {
		healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer healthCancel()
		if err := sandboxProvider.HealthCheck(healthCtx); err != nil {
			if cfg.SandboxRequireGVisor {
				logger.Fatal().Err(err).Msg("gVisor health check failed — set SANDBOX_REQUIRE_GVISOR=false to disable")
			} else {
				logger.Warn().Err(err).Msg("gVisor not available, falling back to runc — NOT RECOMMENDED FOR PRODUCTION")
				sandboxProvider = providers.NewDockerProvider(dockerCli, logger, providers.WithRuntime("runc"))
			}
		}
	}

	// LLM client (optional — validation/prioritization degrade gracefully without it).
	llmClient, err := llm.NewClient(cfg.LLMConfig(), logger)
	if err != nil {
		logger.Warn().Err(err).Msg("LLM client initialization failed — LLM-dependent checks will be skipped")
	}

	// Agent adapters.
	agentAdapters := map[models.AgentType]agent.AgentAdapter{
		models.AgentTypeClaudeCode: adapters.NewClaudeCodeAdapter(logger),
		models.AgentTypeGeminiCLI:  adapters.NewGeminiCLIAdapter(logger),
		models.AgentTypeCodex:      adapters.NewCodexAdapter(logger),
	}

	// Orchestrator.
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionQuestionStore := db.NewSessionQuestionStore(pool)
	projectTaskUpdater := pm.NewProjectHooks(projectTaskStore, projectStore, logger)
	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         sandboxProvider,
		Adapters:         agentAdapters,
		Sessions:         sessionStore,
		SessionLogs:      sessionLogStore,
		SessionQuestions: sessionQuestionStore,
		SessionMessages:  sessionMessageStore,
		DecisionLog:      pmDecisionLogStore,
		ProjectTasks:     projectTaskUpdater,
		Issues:           issueStore,
		Repositories:     repoStore,
		Orgs:             orgStore,
		Jobs:             jobStore,
		GitHub:           ghSvc,
		CodexAuth:        codexAuthSvc,
		Credentials:      credentialStore,
		UserCredentials:  userCredentialStore,
		Snapshots:        snapshotStore,
		Cancels:          cancelRegistry,
		Logger:           logger,
	})

	// Validation service.
	validationSvc := validation.NewService(
		validationStore, issueStore, orgStore, jobStore, llmClient, sandboxProvider, logger,
	)

	// PR service.
	prService := ghservice.NewPRService(
		ghSvc, pullRequestStore, sessionStore, issueStore,
		deployStore, validationStore, repoStore, jobStore, logger,
	)

	// Failure analysis service.
	failureSvc := agent.NewFailureService(sessionStore, logger)

	// Prioritization service.
	prioritizationSvc := prioritization.NewService(
		issueStore, priorityScoreStore, complexityEstimateStore,
		sessionStore, orgStore, jobStore, llmClient, logger,
	)

	pmAdapter := adapters.NewClaudeCodeAdapter(logger)
	pmSvc := pm.NewService(
		issueStore,
		sessionStore,
		pullRequestStore,
		orgStore,
		repoStore,
		jobStore,
		pmPlanStore,
		pmDecisionLogStore,
		sandboxProvider,
		pmAdapter,
		ghSvc,
		logger,
	)
	pmSvc.SetProjectStores(projectStore, projectTaskStore, projectCycleStore)
	pmSvc.SetPMDocumentStore(pmDocumentStore)
	pmSvc.SetSlackStores(integrationStore, credentialStore)
	pmSvc.SetSessionLogStore(sessionLogStore)
	pmSvc.SetInternalAPI(cfg.BaseURL+"/api/v1/internal", cfg.SessionSecret)
	pmSvc.SetSkillsBuilder(orchestrator)

	logger.Info().
		Int("adapters", len(agentAdapters)).
		Bool("llm_configured", llmClient != nil).
		Msg("Phase 3+ services initialized")

	// Slack summarizer (optional — only if LLM client is available).
	var slackSummarizer *ingestion.SlackSummarizer
	if llmClient != nil {
		slackSummarizer = ingestion.NewSlackSummarizer(llmClient, cfg.SlackSummaryModel, logger)
	}

	// Session title service (optional — only if LLM client is available).
	var titleService *services.SessionTitleService
	if llmClient != nil {
		titleService = services.NewSessionTitleService(llmClient, sessionStore, sessionMessageStore)
	}

	return &worker.Services{
		Orchestrator:    orchestrator,
		Validation:      validationSvc,
		PR:              prService,
		Failure:         failureSvc,
		SandboxProvider: sandboxProvider,
		Prioritization:  prioritizationSvc,
		PM:              pmSvc,
		SlackSummarizer: slackSummarizer,
		LLM:             llmClient,
		GitHub:          ghSvc,
		TitleService:    titleService,
	}
}
