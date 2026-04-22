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
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/assembledhq/143/internal/services/automations"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/preview"
	previewproviders "github.com/assembledhq/143/internal/services/preview/providers"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/services/validation"
	"github.com/assembledhq/143/internal/telemetry"
	"github.com/assembledhq/143/internal/version"
	"github.com/assembledhq/143/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := logging.NewLogger(cfg.LogLevel, cfg.Env)
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

	hostname, _ := os.Hostname()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	// Initialize OpenTelemetry meter provider.
	// Enables Prometheus /metrics (always) + OTLP push (when OTEL_EXPORTER_OTLP_ENDPOINT is set).
	_, otelShutdown, err := telemetry.InitMeterProvider(ctx, telemetry.Config{
		ServiceName:       "143",
		OTLPEndpoint:      cfg.OTLPEndpoint,
		OTLPInsecure:      cfg.OTLPInsecure,
		PrometheusEnabled: true,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize telemetry")
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("failed to shutdown telemetry")
		}
	}()

	containerUsageStore := db.NewContainerUsageStore(pool)
	billingMetrics, err := metrics.NewBillingMetrics(containerUsageStore.CountActive)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize billing metrics")
	}

	httpMetrics, err := metrics.NewHTTPMetrics()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize HTTP metrics")
	}
	middleware.SetHTTPMetrics(httpMetrics)

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
	claudeCodeAuthSvc := claudecodeauth.NewService(credentialStore, logger)

	// Platform LLM client for internal features (titles, PR descriptions, project
	// generation, validation, prioritization). Uses the cheap PLATFORM_LLM_MODEL.
	llmClient, err := llm.NewClient(cfg.PlatformLLMConfig(), logger)
	if err != nil {
		logger.Warn().Err(err).Msg("Platform LLM client initialization failed — LLM-dependent features will be unavailable")
	} else {
		logger.Info().Str("model", cfg.PlatformLLMModel).Msg("Platform LLM client initialized for internal features")
	}

	// Create Docker client for file browsing and preview provider (optional —
	// gracefully degrades when Docker is not available).
	var fileReader sandbox.FileReader
	var pvProvider preview.PreviewCapableProvider
	var snapshotExec preview.SnapshotExecutor
	var apiSandboxProvider agent.SandboxProvider
	apiDockerCli, dockerErr := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if dockerErr == nil {
		defer apiDockerCli.Close()
		fileReader = sandbox.NewDockerFileReader(apiDockerCli)

		// Build sandbox+preview provider so the preview subsystem can start,
		// stop, and also hydrate sandboxes from snapshot when a user clicks
		// Start Preview on a session whose container is already torn down.
		sandboxExec := providers.NewDockerProvider(apiDockerCli, logger, providers.WithResolvConf(cfg.SandboxResolvConf))
		pvProvider = previewproviders.NewDockerPreviewProvider(apiDockerCli, sandboxExec, logger)
		snapshotExec = sandboxExec
		apiSandboxProvider = sandboxExec
	} else {
		logger.Warn().Err(dockerErr).Msg("Docker not available — file browsing and preview provider disabled")
		fileReader = sandbox.NoOpFileReader{}
	}

	// Snapshot store is shared across API (preview hydrate) and worker
	// (agent orchestrator). Constructed once so both paths agree on the
	// configured backend and object key layout.
	apiSnapshotStore, snapshotStoreInfo, err := storage.BuildSnapshotStore(ctx, storage.SnapshotStoreConfig{
		StorageDir:     cfg.SnapshotStorageDir,
		S3Bucket:       cfg.SnapshotS3Bucket,
		S3Prefix:       cfg.SnapshotS3Prefix,
		S3Region:       cfg.SnapshotS3Region,
		S3Endpoint:     cfg.SnapshotS3Endpoint,
		S3UsePathStyle: cfg.SnapshotS3UsePathStyle,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize snapshot store")
	}
	snapshotLog := logger.Info().
		Str("backend", snapshotStoreInfo.Backend)
	if snapshotStoreInfo.StorageDir != "" {
		snapshotLog = snapshotLog.Str("storage_dir", snapshotStoreInfo.StorageDir)
	}
	if snapshotStoreInfo.Bucket != "" {
		snapshotLog = snapshotLog.Str("bucket", snapshotStoreInfo.Bucket)
	}
	if snapshotStoreInfo.Prefix != "" {
		snapshotLog = snapshotLog.Str("prefix", snapshotStoreInfo.Prefix)
	}
	if snapshotStoreInfo.EndpointHost != "" {
		snapshotLog = snapshotLog.Str("endpoint_host", snapshotStoreInfo.EndpointHost)
	}
	if snapshotStoreInfo.Backend == "s3" {
		snapshotLog = snapshotLog.Bool("use_path_style", snapshotStoreInfo.UsePathStyle)
	}
	snapshotLog.Msg("snapshot store configured")

	cancelRegistry := agent.NewCancelRegistry(logger)
	// Shared org-settings cache: the settings handler invalidates it on write,
	// the orchestrator reads it when resolving Amp/Pi agent_config. In single-
	// process deployments (MODE=all), the router and worker share this instance
	// so a settings write is observed immediately. In split deployments
	// (MODE=api + separate MODE=worker), the worker process holds its own
	// cache that the API process can't reach — settings updates take effect on
	// that worker only after the TTL (DefaultOrgSettingsCacheTTL) expires.
	// That's the safety net; if you need cross-process invalidation later,
	// wire LISTEN/NOTIFY through OrgSettingsCache.InvalidateOrg.
	//
	// Even in single-process mode the invalidation is *soft*: a reader racing
	// the post-write InvalidateOrg can re-populate the cache with the
	// pre-write value (cache miss → DB read → Set with stale row), leaving
	// the entry stale until the next write or TTL expiry. Last-writer-wins,
	// so no corruption — but don't rely on a strict happens-before between
	// settings commit and next-read.
	orgSettingsCache := agent.NewOrgSettingsCache(agent.DefaultOrgSettingsCacheTTL)
	// Closed when the process receives SIGTERM so long-lived handlers (SSE
	// streams, etc.) can end their loops cleanly during graceful shutdown.
	shutdownCh := make(chan struct{})
	router, gwSrv, recycleWorker, inspectorCloser, previewManager, err := api.NewRouter(cfg, pool, logger, codexAuthSvc, claudeCodeAuthSvc, llmClient, fileReader, cancelRegistry, pvProvider, snapshotExec, apiSandboxProvider, apiSnapshotStore, orgSettingsCache, shutdownCh)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize API router")
	}

	nodeManager := cluster.NewNodeManager(pool, logger, hostname, cfg.Mode)
	nodeManager.SetMetadataProvider(func() map[string]any {
		return map[string]any{
			"build_sha": version.BuildSHA,
		}
	})
	if err := nodeManager.Register(ctx, hostname); err != nil {
		logger.Fatal().Err(err).Msg("failed to register cluster node")
	}
	go nodeManager.StartHeartbeat(ctx)

	// Start worker if mode includes worker capability
	var processWorker *worker.Worker
	if cfg.Mode == "all" || cfg.Mode == "worker" {
		w := worker.New(pool, logger, hostname)
		processWorker = w

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
		automationStore := db.NewAutomationStore(pool)
		automationRunStore := db.NewAutomationRunStore(pool)
		// Reuse the snapshot store built for the API so both paths agree on
		// SnapshotStorageDir without duplicating configuration.
		snapshotStore := apiSnapshotStore

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
			Automations:         automationStore,
			AutomationRuns:      automationRunStore,
		}

		// Build Phase 3+ services if runtime dependencies are available.
		var services *worker.Services
		if canBuildServices(cfg, logger) {
			services = buildServices(cfg, pool, logger, codexAuthSvc, claudeCodeAuthSvc, credentialStore, userCredentialStore, issueStore, sessionStore,
				jobStore, orgStore, repoStore, validationStore, pullRequestStore,
				deployStore, priorityScoreStore, complexityEstimateStore, pmPlanStore, pmDecisionLogStore,
				projectStore, projectTaskStore, projectCycleStore, pmDocumentStore, integrationStore,
				sessionMessageStore, automationRunStore, snapshotStore, billingMetrics, cancelRegistry, orgSettingsCache)
		}
		// Refuse to start an anemic worker. Without agent services (GitHub App,
		// Docker, sandbox health), run_agent won't register, but the worker will
		// still dequeue run_agent jobs and dead-letter them as "no handler" —
		// poisoning session starts on peer nodes that would have served them.
		// Operators that don't want a worker should set MODE=api.
		if services == nil {
			logger.Fatal().
				Str("mode", cfg.Mode).
				Msg("worker mode requires agent services (GitHub App + Docker + sandbox health check). " +
					"Fix the missing dependencies, or set MODE=api to disable the in-process worker.")
		}
		retentionCfg := worker.DataRetentionConfig{
			WebhookDays: cfg.DataRetentionWebhookDays,
			LogsDays:    cfg.DataRetentionLogsDays,
			JobsDays:    cfg.DataRetentionJobsDays,
		}
		worker.RegisterHandlers(w, stores, services, retentionCfg, logger)
		nodeManager.SetMetadataProvider(func() map[string]any {
			return map[string]any{
				"build_sha":              version.BuildSHA,
				"active_job_count":       w.ActiveJobCount(),
				"active_run_agent_count": w.ActiveRunAgentCount(),
			}
		})
		go w.Start(ctx)
		logger.Info().Msg("worker started with registered handlers")

		recoveryLoop := cluster.NewRecoveryLoop(nodeManager, jobStore, logger, 90*time.Second, 100)
		go recoveryLoop.Start(ctx, 30*time.Second)

		// Reconcile containers that leaked when the last server exited mid-turn
		// or mid-Stop. Runs before the reaper starts so the reaper's Phase 2
		// sees clean state. Best-effort: errors are logged, not fatal.
		if apiSandboxProvider != nil {
			reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 2*time.Minute)
			if reconcileErr := agent.ReconcileOrphanedContainers(reconcileCtx, sessionStore, apiSandboxProvider, logger); reconcileErr != nil {
				logger.Warn().Err(reconcileErr).Msg("startup: reconciling orphaned containers failed; leftover rows will be retried on next start")
			}
			reconcileCancel()
		}

		usageRollupStore := db.NewUsageRollupStore(pool)
		reaperOpts := []agent.SessionReaperOption{
			agent.WithOrphanCloser(db.NewContainerUsageStore(pool)),
			agent.WithUsageRoller(usageRollupStore),
			agent.WithMaxRunningAge(cfg.SessionMaxRunningAge),
		}
		if previewManager != nil {
			reaperOpts = append(reaperOpts, agent.WithPreviewStopper(previewManager))
		}
		reaper := agent.NewSessionReaper(sessionStore, snapshotStore, cfg.SessionMaxIdleAge, cfg.SessionMaxSnapshotAge, cfg.SessionReaperInterval, logger, reaperOpts...)
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
		scheduler.SetPMDocStore(pmDocumentStore)
		scheduler.SetAutomationStores(automationStore, automationRunStore, pool)
		go scheduler.Start(ctx, 10*time.Minute)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown.
	//
	// Ordering matters here. docker-compose.app.yml sets stop_grace_period to
	// 120s; we must finish shutting down before Docker SIGKILLs us. The total
	// budget spent below is:
	//   - SSE drain + http.Server.Shutdown:  up to 110s
	//   - preview gateway.Shutdown:          up to 60s (parallel with above)
	// leaving a safety margin under the 120s SIGKILL deadline.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info().Msg("shutting down server...")

		if processWorker != nil {
			processWorker.RequestDrain()
		}
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 110*time.Second)
		if err := nodeManager.RequestDrain(drainCtx, time.Now()); err != nil {
			logger.Warn().Err(err).Msg("failed to mark node draining")
		}
		if processWorker != nil {
			for processWorker.ActiveJobCount() > 0 {
				select {
				case <-drainCtx.Done():
					logger.Warn().Int("active_jobs", processWorker.ActiveJobCount()).Msg("worker drain timed out; continuing shutdown")
					goto drained
				case <-time.After(500 * time.Millisecond):
				}
			}
		}
	drained:
		drainCancel()

		// Signal long-lived SSE handlers to return before calling Shutdown.
		// Without this, Shutdown blocks on each open SSE connection until its
		// deadline expires; the client then sees an abrupt reset rather than
		// a clean EOF (which EventSource retries from on its own).
		close(shutdownCh)

		cancel() // stop worker
		if recycleWorker != nil {
			recycleWorker.Stop()
		}
		if inspectorCloser != nil {
			if err := inspectorCloser.Close(); err != nil {
				logger.Error().Err(err).Msg("preview inspector shutdown failed")
			}
		}
		// Gateway carries long-lived WebSocket (HMR) proxies; give it a
		// generous drain window so in-flight preview sessions close cleanly
		// instead of being severed mid-frame. Runs in parallel with the
		// main server's Shutdown below.
		gwDone := make(chan struct{})
		go func() {
			defer close(gwDone)
			if gwSrv == nil {
				return
			}
			gwShutdownCtx, gwShutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer gwShutdownCancel()
			if err := gwSrv.Shutdown(gwShutdownCtx); err != nil {
				logger.Error().Err(err).Msg("preview gateway shutdown failed")
			}
		}()

		// Drain the main API server. 110s leaves ~10s of headroom before
		// Docker SIGKILLs the container at stop_grace_period=120s.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 110*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("server shutdown failed")
		}
		<-gwDone
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
	claudeCodeAuthSvc *claudecodeauth.Service,
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
	automationRunStore *db.AutomationRunStore,
	snapshotStore storage.SnapshotStore,
	billingMetrics *metrics.BillingMetrics,
	cancelRegistry *agent.CancelRegistry,
	orgSettingsCache *agent.OrgSettingsCache,
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
	sandboxProvider := providers.NewDockerProvider(dockerCli, logger, providers.WithRuntime(cfg.SandboxRuntime), providers.WithResolvConf(cfg.SandboxResolvConf))

	// Startup health check: verify Docker daemon connectivity and, for gVisor,
	// that the runsc runtime is functional. Retry a few times because Docker and
	// gVisor can fail transiently during startup.
	{
		const maxRetries = 3
		var healthErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
			healthErr = sandboxProvider.HealthCheck(healthCtx)
			healthCancel()
			if healthErr == nil {
				break
			}
			logger.Warn().Err(healthErr).Int("attempt", attempt).Int("max", maxRetries).Msg("sandbox health check failed, retrying")
			if attempt < maxRetries {
				time.Sleep(2 * time.Second)
			}
		}
		if healthErr != nil {
			if cfg.SandboxRuntime == "runsc" && !cfg.SandboxRequireGVisor {
				logger.Warn().Err(healthErr).Msg("gVisor not available, falling back to runc — NOT RECOMMENDED FOR PRODUCTION")
				sandboxProvider = providers.NewDockerProvider(dockerCli, logger, providers.WithRuntime("runc"), providers.WithResolvConf(cfg.SandboxResolvConf))
			} else {
				logger.Error().Err(healthErr).Msg("sandbox health check failed — Phase 3+ services disabled")
				return nil
			}
		}
	}

	// Platform LLM client for worker internal features (validation, prioritization).
	llmClient, err := llm.NewClient(cfg.PlatformLLMConfig(), logger)
	if err != nil {
		logger.Warn().Err(err).Msg("Platform LLM client initialization failed — LLM-dependent checks will be skipped")
	}

	// Agent adapters.
	agentAdapters := map[models.AgentType]agent.AgentAdapter{
		models.AgentTypeClaudeCode: adapters.NewClaudeCodeAdapter(logger),
		models.AgentTypeGeminiCLI:  adapters.NewGeminiCLIAdapter(logger),
		models.AgentTypeCodex:      adapters.NewCodexAdapter(logger),
		models.AgentTypeAmp:        adapters.NewAmpAdapter(logger),
		models.AgentTypePi:         adapters.NewPiAdapter(logger),
	}

	// Shared agent env/auth helper — consumed by both the session Orchestrator
	// and the PM service so both paths resolve provider credentials, Codex
	// auth.json, and agent_config overrides through a single code path.
	agentEnv := agent.NewAgentEnv(agent.AgentEnvDeps{
		Credentials:      credentialStore,
		UserCredentials:  userCredentialStore,
		Orgs:             orgStore,
		OrgSettingsCache: orgSettingsCache,
		CodexAuth:        codexAuthSvc,
		Provider:         sandboxProvider,
		Logger:           logger,
	})

	// Orchestrator.
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionQuestionStore := db.NewSessionQuestionStore(pool)
	projectTaskUpdater := pm.NewProjectHooks(projectTaskStore, projectStore, logger)
	automationRunUpdater := automations.NewAutomationHooks(automationRunStore, logger)
	containerUsageStore := db.NewContainerUsageStore(pool)
	usageTracker := agent.NewUsageTracker(containerUsageStore, billingMetrics, logger)
	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         sandboxProvider,
		Adapters:         agentAdapters,
		Env:              agentEnv,
		Sessions:         sessionStore,
		SessionLogs:      sessionLogStore,
		SessionQuestions: sessionQuestionStore,
		SessionMessages:  sessionMessageStore,
		DecisionLog:      pmDecisionLogStore,
		ProjectTasks:     projectTaskUpdater,
		AutomationRuns:   automationRunUpdater,
		Issues:           issueStore,
		Repositories:     repoStore,
		Orgs:             orgStore,
		Jobs:             jobStore,
		GitHub:           ghSvc,
		CodexAuth:        codexAuthSvc,
		ClaudeCodeAuth:   claudeCodeAuthSvc,
		Credentials:      credentialStore,
		UserCredentials:  userCredentialStore,
		Snapshots:        snapshotStore,
		UsageTracker:     usageTracker,
		Cancels:          cancelRegistry,
		OrgSettingsCache: orgSettingsCache,
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
		agentAdapters,
		agentEnv,
		ghSvc,
		logger,
	)
	pmSvc.SetUsageTracker(usageTracker)
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
