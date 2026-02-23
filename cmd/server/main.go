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
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/assembledhq/143/internal/services/codexauth"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/validation"
	"github.com/assembledhq/143/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := logging.NewLogger(cfg.LogLevel)
	cfg.LogStatus(logger)

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
	codexAuthSvc := codexauth.NewService(credentialStore, logger)

	router, err := api.NewRouter(cfg, pool, logger, codexAuthSvc)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize API router")
	}

	// Start worker if mode includes worker capability
	if cfg.Mode == "all" || cfg.Mode == "worker" {
		hostname, _ := os.Hostname()
		w := worker.New(pool, logger, hostname)

		issueStore := db.NewIssueStore(pool)
		agentRunStore := db.NewAgentRunStore(pool)
		jobStore := db.NewJobStore(pool)
		orgStore := db.NewOrganizationStore(pool)
		repoStore := db.NewRepositoryStore(pool)
		validationStore := db.NewValidationStore(pool)
		pullRequestStore := db.NewPullRequestStore(pool)
		deployStore := db.NewDeployStore(pool)
		priorityScoreStore := db.NewPriorityScoreStore(pool)
		complexityEstimateStore := db.NewComplexityEstimateStore(pool)

		stores := &worker.Stores{
			Issues:              issueStore,
			AgentRuns:           agentRunStore,
			Jobs:                jobStore,
			Integrations:        db.NewIntegrationStore(pool),
			Webhooks:            db.NewWebhookDeliveryStore(pool),
			PriorityScores:      priorityScoreStore,
			ComplexityEstimates: complexityEstimateStore,
		}

		// Build Phase 3+ services if runtime dependencies are available.
		var services *worker.Services
		if canBuildServices(cfg, logger) {
			services = buildServices(cfg, pool, logger, codexAuthSvc, issueStore, agentRunStore,
				jobStore, orgStore, repoStore, validationStore, pullRequestStore,
				deployStore, priorityScoreStore, complexityEstimateStore)
		}
		worker.RegisterHandlers(w, stores, services, logger)
		go w.Start(ctx)
		logger.Info().Msg("worker started with registered handlers")
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
	issueStore *db.IssueStore,
	agentRunStore *db.AgentRunStore,
	jobStore *db.JobStore,
	orgStore *db.OrganizationStore,
	repoStore *db.RepositoryStore,
	validationStore *db.ValidationStore,
	pullRequestStore *db.PullRequestStore,
	deployStore *db.DeployStore,
	priorityScoreStore *db.PriorityScoreStore,
	complexityEstimateStore *db.ComplexityEstimateStore,
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
	sandboxProvider := providers.NewDockerProvider(dockerCli, logger)

	// LLM client (optional — validation/prioritization degrade gracefully without it).
	llmClient, err := llm.NewClient(cfg.LLMConfig(), logger)
	if err != nil {
		logger.Warn().Err(err).Msg("LLM client initialization failed — LLM-dependent checks will be skipped")
	}

	// Agent adapters.
	agentAdapters := map[string]agent.AgentAdapter{
		"claude_code": adapters.NewClaudeCodeAdapter(logger),
		"gemini_cli":  adapters.NewGeminiCLIAdapter(logger),
		"codex":       adapters.NewCodexAdapter(logger),
	}

	// Orchestrator.
	agentRunLogStore := db.NewAgentRunLogStore(pool)
	agentRunQuestionStore := db.NewAgentRunQuestionStore(pool)
	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:          sandboxProvider,
		Adapters:          agentAdapters,
		AgentRuns:         agentRunStore,
		AgentRunLogs:      agentRunLogStore,
		AgentRunQuestions: agentRunQuestionStore,
		Issues:            issueStore,
		Repositories:      repoStore,
		Orgs:              orgStore,
		Jobs:              jobStore,
		GitHub:            ghSvc,
		CodexAuth:         codexAuthSvc,
		Logger:            logger,
		AgentEnv:          cfg.AgentEnv(),
	})

	// Validation service.
	validationSvc := validation.NewService(
		validationStore, issueStore, orgStore, jobStore, llmClient, sandboxProvider, logger,
	)

	// PR service.
	prService := ghservice.NewPRService(
		ghSvc, pullRequestStore, agentRunStore, issueStore,
		deployStore, validationStore, repoStore, jobStore, logger,
	)

	// Failure analysis service.
	failureSvc := agent.NewFailureService(agentRunStore, logger)

	// Prioritization service.
	prioritizationSvc := prioritization.NewService(
		issueStore, priorityScoreStore, complexityEstimateStore,
		agentRunStore, orgStore, jobStore, llmClient, logger,
	)

	logger.Info().
		Int("adapters", len(agentAdapters)).
		Bool("llm_configured", llmClient != nil).
		Msg("Phase 3+ services initialized")

	return &worker.Services{
		Orchestrator:    orchestrator,
		Validation:      validationSvc,
		PR:              prService,
		Failure:         failureSvc,
		SandboxProvider: sandboxProvider,
		Prioritization:  prioritizationSvc,
	}
}
