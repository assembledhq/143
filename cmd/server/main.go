package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	dockerclient "github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api"
	"github.com/assembledhq/143/internal/api/handlers"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/assembledhq/143/internal/services/automations"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/assembledhq/143/internal/services/domains"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/github/identity"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/ownerloss"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/preview"
	previewproviders "github.com/assembledhq/143/internal/services/preview/providers"
	"github.com/assembledhq/143/internal/services/prioritization"
	reviewloopservice "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/services/storage"
	threadservice "github.com/assembledhq/143/internal/services/thread"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/assembledhq/143/internal/telemetry"
	"github.com/assembledhq/143/internal/version"
	"github.com/assembledhq/143/internal/worker"
)

const (
	nodeDrainMarkTimeout      = 5 * time.Second
	httpDrainPropagationDelay = 7 * time.Second
	httpShutdownTimeout       = 100 * time.Second
)

func previewDependencyCacheEnabled(cfg config.Config) bool {
	return strings.TrimSpace(cfg.PreviewDependencyCacheBucket) != ""
}

func main() {
	if isSessionExecutorInvocation(os.Args) {
		runSessionExecutorMain()
		return
	}

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
	if err := validateSessionExecutorStartupConfig(cfg); err != nil {
		logger.Fatal().Err(err).Msg("worker session executor configuration is invalid")
	}

	sentryReporter, err := observability.NewSentryReporter(observability.SentryConfig{
		DSN:         cfg.SentryDSN,
		Environment: cfg.SentryEnvironmentOrDefault(),
		Release:     version.BuildSHA,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize sentry")
	}
	defer func() {
		if sentryReporter != nil && !sentryReporter.Flush(5*time.Second) {
			logger.Warn().Msg("timed out flushing sentry events during shutdown")
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hostname, _ := os.Hostname()
	if cfg.NodeID == "" {
		cfg.NodeID = hostname
	}

	pool, err := db.NewPoolWithOptions(ctx, cfg.DatabaseURL, db.PoolOptions{
		MaxConns:        cfg.DatabaseMaxConns,
		MaxConnIdleTime: cfg.DatabaseMaxConnIdleTime,
	})
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

	redisMetrics, err := cache.NewMetrics()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize Redis metrics")
	}
	redisClient := cache.New(cache.Config{
		Topology:   cfg.RedisTopology,
		URL:        cfg.RedisURL,
		Addrs:      cache.ParseAddrs(cfg.RedisAddrs),
		MasterName: cfg.RedisMasterName,
		Password:   cfg.RedisPassword,
		PoolSize:   cfg.RedisPoolSize,
	}, logger, redisMetrics)
	if redisClient != nil {
		defer func() {
			if err := redisClient.Close(); err != nil {
				logger.Warn().Err(err).Msg("failed to close Redis client")
			}
		}()
	}
	sessionStreams := cache.NewSessionStreams(redisClient, logger, redisMetrics)
	jobNotifier := cache.NewJobNotifier(redisClient, logger)
	// Eval batch + bootstrap pub/sub fanout. Constructed once and shared
	// between the API (for SSE handlers) and the worker (for publishing on
	// state transitions) so a single connection pool drives both. nil-safe
	// when redisClient is nil — the SSE returns 503 and the worker's
	// publishEvalBatchSignal helper skips publish.
	evalBatchStreams := cache.NewEvalBatchStreams(redisClient, logger)
	evalBootstrapStreams := cache.NewEvalBootstrapStreams(redisClient, logger)
	// Org-scoped code review pub/sub fanout. The worker publishes lifecycle
	// changes here as it runs reviews; the API subscribes for the live code
	// reviews list. nil-safe when redisClient is nil (publish becomes a no-op).
	codeReviewStreams := cache.NewCodeReviewStreams(redisClient, logger)
	codeReviewStore := db.NewCodeReviewStore(pool)
	codeReviewStore.SetStreams(codeReviewStreams)
	codeReviewStore.SetLogger(logger)

	// Create codex auth service (shared between router and orchestrator).
	var cryptoSvc *crypto.Service
	if cfg.EncryptionMasterKey != "" {
		cryptoSvc, err = crypto.NewService(cfg.EncryptionMasterKey)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to initialize crypto service")
		}
	}
	// Refuse to serve traffic until the unified-coding-credentials post-step
	// (Anthropic API-key/subscription split) has run. Fresh installs that have
	// no anthropic rows pass the gate automatically.
	if err := db.EnsureAnthropicSplitSentinel(ctx, pool); err != nil {
		logger.Fatal().Err(err).Msg("coding-credentials migration gate failed; server refusing to start")
	}

	credentialStore := db.NewOrgCredentialStore(pool, cryptoSvc)
	userCredentialStore := db.NewUserCredentialStore(pool, cryptoSvc)
	codingCredentialStore := db.NewCodingCredentialStore(pool, cryptoSvc)
	// Both OAuth services depend on a scope-aware credential surface backed
	// by the unified store — see internal/db/scoped_credential_store.go.
	scopedCredentialStore := db.NewScopedCredentialStore(codingCredentialStore)
	codexAuthSvc := codexauth.NewService(scopedCredentialStore, logger)
	claudeCodeAuthSvc := claudecodeauth.NewService(scopedCredentialStore, logger)

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
	fileReader := sandbox.FileReader(sandbox.NoOpFileReader{})
	var pvProvider preview.PreviewCapableProvider
	var dependencyCache preview.DependencyCache
	var snapshotExec preview.SnapshotExecutor
	var apiSandboxProvider agent.SandboxProvider
	var sandboxCapacity *agent.SandboxCapacityGate
	if cfg.Mode != "api" {
		apiDockerCli, dockerErr := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if dockerErr == nil {
			defer apiDockerCli.Close()
			fileReader = sandbox.NewDockerFileReader(apiDockerCli)

			// Build sandbox+preview provider so worker-capable modes can start,
			// stop, and hydrate previews locally.
			sandboxExec := providers.NewDockerProvider(
				apiDockerCli,
				logger,
				providers.WithResolvConf(cfg.SandboxResolvConf),
				providers.WithHealthCheckImage(cfg.SandboxHealthCheckImage),
				providers.WithRequireDiskQuota(cfg.SandboxRequireDiskQuota),
			)
			if previewDependencyCacheEnabled(*cfg) {
				dependencyS3Region := cfg.PreviewDependencyCacheS3Region
				if dependencyS3Region == "" {
					dependencyS3Region = cfg.SnapshotS3Region
				}
				dependencyS3Endpoint := cfg.PreviewDependencyCacheS3Endpoint
				if dependencyS3Endpoint == "" {
					dependencyS3Endpoint = cfg.SnapshotS3Endpoint
				}
				dependencyS3UsePathStyle := cfg.PreviewDependencyCacheS3UsePathStyle || cfg.SnapshotS3UsePathStyle
				dependencyBlobStore, _, cacheStoreErr := storage.BuildSnapshotStore(ctx, storage.SnapshotStoreConfig{
					S3Bucket:       cfg.PreviewDependencyCacheBucket,
					S3Prefix:       cfg.PreviewDependencyCachePrefix,
					S3Region:       dependencyS3Region,
					S3Endpoint:     dependencyS3Endpoint,
					S3UsePathStyle: dependencyS3UsePathStyle,
				})
				if cacheStoreErr != nil {
					logger.Warn().Err(cacheStoreErr).Msg("failed to initialize preview dependency cache blob store — dependency caching disabled")
				} else {
					cache, cacheErr := preview.NewDependencyCache(preview.DependencyCacheConfig{
						Store:         db.NewPreviewStore(pool),
						Executor:      sandboxExec,
						BlobStore:     dependencyBlobStore,
						Logger:        logger,
						WorkerNodeID:  cfg.NodeID,
						Prefix:        cfg.PreviewDependencyCachePrefix,
						LocalDir:      config.ResolvePreviewDependencyCacheLocalDir(cfg.PreviewDependencyCacheLocalDir),
						LocalMaxBytes: cfg.PreviewDependencyCacheLocalMaxBytes,
					})
					if cacheErr != nil {
						logger.Warn().Err(cacheErr).Msg("failed to initialize preview dependency cache — dependency caching disabled")
					} else {
						dependencyCache = cache
						cleaner := preview.NewDependencyCacheCleaner(preview.DependencyCacheCleanerConfig{
							Store:             db.NewPreviewStore(pool),
							BlobStore:         dependencyBlobStore,
							Logger:            logger,
							Retention:         time.Duration(cfg.PreviewDependencyCacheRetentionDays) * 24 * time.Hour,
							Interval:          cfg.PreviewDependencyCacheCleanupInterval,
							KeepNewestPerRepo: cfg.PreviewDependencyCacheKeepNewestPerRepo,
						})
						go cleaner.Run(ctx)
					}
				}
			}
			dockerPreviewProvider := previewproviders.NewDockerPreviewProvider(
				apiDockerCli,
				sandboxExec,
				logger,
				previewproviders.WithDependencyCache(dependencyCache),
				previewproviders.WithPackageManagerCacheEnabled(cfg.PreviewPackageManagerCacheEnabled),
			)
			pvProvider = dockerPreviewProvider
			snapshotExec = sandboxExec
			apiSandboxProvider = sandboxExec
			maxActiveSandboxes := resolveWorkerMaxActiveSandboxes(cfg.WorkerProcessCount, cfg.WorkerMaxActiveSandboxes)
			sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
				Counter:   sandboxExec,
				MaxActive: maxActiveSandboxes,
				NodeID:    cfg.NodeID,
				Logger:    logger,
			})
			logger.Info().
				Int("max_active_sandboxes", maxActiveSandboxes).
				Int("worker_process_count", cfg.WorkerProcessCount).
				Msg("sandbox capacity gate enabled")
		} else {
			logger.Warn().Err(dockerErr).Msg("Docker not available — file browsing and preview provider disabled")
		}
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
	threadCancelRegistry := agent.NewThreadCancelRegistry(logger)
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
	router, gwSrv, recycleWorker, inspectorCloser, previewManager, err := api.NewRouter(cfg, pool, logger, sentryReporter, codexAuthSvc, claudeCodeAuthSvc, llmClient, fileReader, cancelRegistry, threadCancelRegistry, pvProvider, snapshotExec, apiSandboxProvider, sandboxCapacity, apiSnapshotStore, orgSettingsCache, shutdownCh, redisClient, sessionStreams, codingCredentialStore)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize API router")
	}

	nodeManager := cluster.NewNodeManager(pool, logger, cfg.NodeID, cfg.Mode)
	previewCapable := (cfg.Mode == "worker" || cfg.Mode == "all") && pvProvider != nil
	var previewRoutingReady atomic.Bool
	nodeManager.SetMetadataProvider(func() map[string]any {
		return buildBaseMetadata(previewCapable && previewRoutingReady.Load(), cfg.PreviewInternalBaseURL, cfg.NodeRegion)
	})
	if err := nodeManager.Register(ctx, hostname); err != nil {
		logger.Fatal().Err(err).Msg("failed to register cluster node")
	}
	go nodeManager.StartHeartbeat(ctx)
	if cfg.Mode != "worker" {
		go worker.RunControlPlaneHealthAlerts(ctx, db.NewJobStore(pool), db.NewNodeStore(pool), logger, time.Minute)
	}

	// Start worker if mode includes worker capability.
	// sandboxAuthShutdown is hoisted to function scope so the graceful
	// shutdown goroutine (declared later) can drain the per-session
	// credential socket listeners that buildServices spun up. Stays nil
	// in api-only mode where buildServices never runs.
	var sandboxAuthShutdown func()
	var processWorkers []*worker.Worker
	var jobStore *db.JobStore
	var evalBootstrapStore *db.EvalBootstrapStore
	var workerPreviewStore *db.PreviewStore
	// Hoisted so the shutdown goroutine below (declared at main scope) can
	// reach the PR service for draining post-PR snapshot uploads. Stays nil
	// in api-only mode where buildServices never runs.
	var workerServices *worker.Services
	if cfg.Mode == "all" || cfg.Mode == "worker" {
		workerCount := cfg.WorkerProcessCount
		if workerCount <= 0 {
			workerCount = 2
		}

		issueStore := db.NewIssueStore(pool)
		sessionStore := db.NewSessionStore(pool)
		jobStore = db.NewJobStore(pool)
		orgStore := db.NewOrganizationStore(pool)
		repoStore := db.NewRepositoryStore(pool)
		integrationStore := db.NewIntegrationStore(pool)
		pullRequestStore := db.NewPullRequestStore(pool)
		deployStore := db.NewDeployStore(pool)
		sessionMessageStore := db.NewSessionMessageStore(pool)
		sessionThreadStore := db.NewSessionThreadStore(pool)
		sessionHumanInputStore := db.NewSessionHumanInputRequestStore(pool)
		evalBootstrapStore = db.NewEvalBootstrapStore(pool)
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
		previewStore := db.NewPreviewStore(pool)
		workerPreviewStore = previewStore
		// Reuse the snapshot store built for the API so both paths agree on
		// SnapshotStorageDir without duplicating configuration.
		snapshotStore := apiSnapshotStore

		auditLogStore := db.NewAuditLogStore(pool)
		sessionLogStore := db.NewSessionLogStore(pool)
		sessionStore.SetLogger(logger)
		sessionLogStore.SetLogger(logger)
		sessionThreadStore.SetLogger(logger)
		jobStore.SetLogger(logger)
		if sessionStreams != nil {
			sessionStore.SetStreams(sessionStreams)
			sessionLogStore.SetStreams(sessionStreams)
			sessionThreadStore.SetStreams(sessionStreams)
			sessionStreams.StartCleanup(ctx, sessionStore)
		}
		if jobNotifier != nil {
			jobStore.SetNotifier(jobNotifier)
		}

		workerPullRequestFeedbackStore := db.NewPullRequestFeedbackStore(pool)
		workerPullRequestFeedbackStore.SetJobStore(jobStore)
		stores := &worker.Stores{
			Issues:              issueStore,
			Users:               db.NewUserStore(pool),
			Sessions:            sessionStore,
			SessionChangesets:   db.NewSessionChangesetStore(pool),
			SessionPublications: db.NewSessionPublicationStore(pool),
			Jobs:                jobStore,
			Integrations:        integrationStore,
			Memberships:         db.NewOrganizationMembershipStore(pool),
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
			EvalBootstraps:      evalBootstrapStore,
			EvalReleaseGates:    db.NewEvalReleaseGateStore(pool),
			Repositories:        repoStore,
			GitHubInstallations: db.NewGitHubInstallationStore(pool),
			SessionMessages:     sessionMessageStore,
			SessionThreads:      sessionThreadStore,
			HumanInputRequests:  sessionHumanInputStore,
			ThreadFileEvents:    db.NewSessionThreadFileEventStore(pool),
			SandboxHolders:      db.NewSessionSandboxHolderStore(pool),
			Automations:         automationStore,
			AutomationRuns:      automationRunStore,
			ReviewLoops:         db.NewSessionReviewLoopStore(pool),
			PRReadiness:         db.NewPRReadinessStore(pool),
			CodeReviews:         codeReviewStore,
			SessionIssueLinks:   db.NewSessionIssueLinkStore(pool),
			Previews:            previewStore,
			PullRequests:        pullRequestStore,
			PullRequestFeedback: workerPullRequestFeedbackStore,
			SlackInstallations:  db.NewSlackInstallationStore(pool),
			SlackOrgSelections:  db.NewSlackOrgSelectionStore(pool),
			SlackBotSettings:    db.NewSlackBotSettingsStore(pool),
			SlackUserLinks:      db.NewSlackUserLinkStore(pool),
			LinearUserLinks:     db.NewLinearUserLinkStore(pool),
			ExternalUserLinks:   db.NewExternalUserLinkStore(pool),
			ExternalSuggestions: db.NewExternalUserLinkSuggestionStore(pool),
			SlackChannels:       db.NewSlackChannelSettingsStore(pool),
			SlackSessionLinks:   db.NewSlackSessionLinkStore(pool),
			SlackInboundEvents:  db.NewSlackInboundEventStore(pool),
			SlackOutbound:       db.NewSlackOutboundMessageStore(pool),
			SessionAttributions: db.NewSessionAttributionStore(pool),
		}

		// Build Phase 3+ services if runtime dependencies are available.
		// Assigns to the hoisted workerServices var so the shutdown
		// goroutine can drive post-PR snapshot upload draining via
		// workerServices.PR.WaitForPostPRSnapshotUploads().
		var services *worker.Services
		if canBuildServices(cfg, logger) {
			services = buildServices(cfg, pool, logger, codexAuthSvc, claudeCodeAuthSvc, credentialStore, userCredentialStore, codingCredentialStore, issueStore, sessionStore,
				jobStore, orgStore, repoStore, pullRequestStore,
				deployStore, priorityScoreStore, complexityEstimateStore, pmPlanStore, pmDecisionLogStore,
				projectStore, projectTaskStore, projectCycleStore, pmDocumentStore, integrationStore,
				sessionMessageStore, automationRunStore, evalBootstrapStore, snapshotStore, billingMetrics, cancelRegistry, threadCancelRegistry, orgSettingsCache, sandboxCapacity, redisClient, sessionStreams, fileReader)
			if services != nil {
				sandboxAuthShutdown = services.SandboxAuthShutdown
				registerInternalSandboxAuthRoutes(router, services.SandboxAuthBroker, cfg, logger)
				if previewManager != nil && pvProvider != nil {
					if concreteOrchestrator, ok := services.Orchestrator.(*agent.Orchestrator); ok {
						var sessionBrowserInspector preview.SessionBrowserInspector
						if inspector, ok := previewManager.Inspector().(preview.SessionBrowserInspector); ok {
							sessionBrowserInspector = inspector
						}
						concreteOrchestrator.SetSuccessfulTurnVerifier(preview.NewSuccessfulTurnVerifier(
							previewManager,
							previewStore,
							preview.NewBrowserSessionService(db.NewPreviewBrowserSessionStore(pool), sessionBrowserInspector),
							db.NewPreviewVerificationRunStore(pool),
						))
					}
					var prewarmDependencyCache preview.PreviewPathCache
					if pathCache, ok := dependencyCache.(preview.PreviewPathCache); ok {
						prewarmDependencyCache = pathCache
					}
					services.PreviewController = previewManager
					services.PreviewStarter = preview.NewStartRunner(preview.StartRunnerConfig{
						Manager:         previewManager,
						Previews:        previewStore,
						Sessions:        sessionStore,
						Repositories:    repoStore,
						Orgs:            orgStore,
						FileReader:      fileReader,
						SandboxProvider: apiSandboxProvider,
						SandboxCapacity: sandboxCapacity,
						StaticEgress:    agent.ResolveStaticEgressRuntimeConfig(cfg.StaticEgressPublicIP),
						Snapshots:       snapshotStore,
						GitHub:          services.GitHub,
						NodeID:          cfg.NodeID,
						DependencyCache: prewarmDependencyCache,
						PrewarmEnabled:  cfg.PreviewCachePrewarmEnabled,
						PrewarmTimeout:  cfg.PreviewCachePrewarmTimeout,
						Logger:          logger,
					})
					previewRPCKeyring, keyringErr := auth.NewPreviewTokenKeyring(cfg.PreviewRPCSecrets)
					if keyringErr != nil {
						logger.Warn().Err(keyringErr).Msg("failed to initialize preview RPC keyring for Slack preview control; worker RPC auth disabled")
						previewRPCKeyring = auth.PreviewTokenKeyring{}
					}
					var slackBranchPreviewHandler *handlers.BranchPreviewHandler
					if prSvc, ok := services.PR.(*ghservice.PRService); ok {
						autoPreviewNodeStore := db.NewNodeStore(pool)
						autoPreviewSelector := preview.NewWorkerSelectorWithOptions(autoPreviewNodeStore, previewStore, preview.WorkerSelectorOptions{
							MaxPreviewsPerWorker: cfg.PreviewMaxPerWorker,
							PreferredRegion:      cfg.NodeRegion,
						})
						previewStopper := preview.NewWorkerStopper(previewStore, autoPreviewSelector, preview.NewWorkerPreviewClientWithKeyring(previewRPCKeyring), cfg.NodeID, previewManager)
						prSvc.SetPreviewTeardown(previewStore, previewStopper)
						prSvc.SetPreviewOriginTemplate(cfg.PreviewOriginTemplate)
						branchPreviewHandler := handlers.NewBranchPreviewHandler(previewStore, repoStore, prSvc, previewManager, cfg.FrontendURL, cfg.PreviewOriginTemplate)
						branchPreviewHandler.SetWorkerRuntime(jobStore, autoPreviewSelector)
						services.AutoPreviewStarter = branchPreviewHandler
						slackBranchPreviewHandler = branchPreviewHandler
					}
					slackPreviewNodeStore := db.NewNodeStore(pool)
					slackPreviewSelector := preview.NewWorkerSelectorWithOptions(slackPreviewNodeStore, previewStore, preview.WorkerSelectorOptions{
						MaxPreviewsPerWorker: cfg.PreviewMaxPerWorker,
						PreferredRegion:      cfg.NodeRegion,
					})
					slackPreviewHandler := handlers.NewPreviewHandler(previewManager, previewStore, sessionStore, repoStore, fileReader, apiSandboxProvider, snapshotStore, logger)
					slackPreviewHandler.SetJobStore(jobStore)
					slackPreviewHandler.SetWorkerRuntime(slackPreviewSelector, preview.NewWorkerPreviewClientWithKeyring(previewRPCKeyring), cfg.NodeID)
					slackPreviewHandler.SetStaticEgressRuntime(orgStore, agent.ResolveStaticEgressRuntimeConfig(cfg.StaticEgressPublicIP))
					slackPreviewHandler.SetSandboxCapacityGate(sandboxCapacity)
					services.SlackPreviewControl = handlers.NewSlackPreviewControl(slackPreviewHandler, slackBranchPreviewHandler, pullRequestStore, repoStore, cfg.FrontendURL)
					services.PreviewCachePrewarmEnabled = cfg.PreviewCachePrewarmEnabled
					services.PreviewCachePrewarmPriority = cfg.PreviewCachePrewarmPriority
					services.PreviewCachePrewarmTimeout = cfg.PreviewCachePrewarmTimeout
				}
				// Wire eval pub/sub publishers so worker handlers can wake
				// the API SSE subscribers on every state transition without
				// the API having to poll Postgres.
				services.EvalBatchStreams = evalBatchStreams
				services.EvalBootstrapStreams = evalBootstrapStreams
				workerServices = services
			}
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
			WebhookDays:              cfg.DataRetentionWebhookDays,
			LogsDays:                 cfg.DataRetentionLogsDays,
			JobsDays:                 cfg.DataRetentionJobsDays,
			SlackInboundPayloadDays:  cfg.DataRetentionSlackInboundPayloadDays,
			SlackInboundPayloadBatch: cfg.DataRetentionSlackInboundPayloadBatch,
		}

		if sandboxCapacity != nil && services.SandboxGC != nil {
			sandboxCapacity.SetPressureCleaner(services.SandboxGC)
		}

		// Run a Docker-first startup cleanup before workers accept jobs. Any
		// DB-unreferenced sandbox already present on this host cannot belong to
		// an in-flight turn in this process, so it should not consume local
		// admission capacity through the normal GC grace window.
		if services.SandboxGC != nil {
			startupGCCtx, startupGCCancel := context.WithTimeout(ctx, 2*time.Minute)
			if gcErr := services.SandboxGC.ReapStartup(startupGCCtx, time.Now()); gcErr != nil {
				logger.Warn().Err(gcErr).Msg("startup: Docker-first sandbox cleanup failed; pressure cleanup and periodic GC will retry")
			}
			startupGCCancel()
		}

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

		// Keep the per-session GitHub credential socket open for the lifetime
		// of every sandbox container alive on this host, and re-bind them after
		// a restart. The worker owns the socket (executors only hold short-lived
		// per-turn leases over the remote broker), so without a container-scoped
		// pin the socket would close at every turn boundary and on every worker
		// restart — and a held-alive sandbox's `git push` would dial a dead
		// socket (ECONNREFUSED) until the next turn re-acquired. The reconciler
		// is grounded in local Docker enumeration rather than worker_node_id, so
		// it re-pins the same host containers across a rolling deploy that gives
		// the new worker generation a different node id. It runs an immediate
		// synchronous pass here (before workers accept jobs, after the orphan
		// reconciler has cleared dead rows) and then continues on an interval.
		//
		// services is guaranteed non-nil here (the Fatal above exits on nil)
		// but staticcheck's flow analysis can't follow logger.Fatal — gate this
		// inside an explicit non-nil check to keep lint clean.
		if services != nil && apiSandboxProvider != nil {
			// SandboxAuthBroker is an interface field, so a worker with sandbox
			// auth disabled (empty SANDBOX_AUTH_SOCKET_DIR) stores a typed-nil
			// *Broker — the assertion would succeed with a nil pointer. Guard on
			// broker != nil so we don't spin a reconciler that can never pin.
			broker, brokerOK := services.SandboxAuthBroker.(*sandboxauth.Broker)
			brokerOK = brokerOK && broker != nil
			lister, listerOK := any(apiSandboxProvider).(agent.ManagedSandboxLister)
			switch {
			case brokerOK && listerOK:
				orgSettingsLoader := func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error) {
					org, err := orgStore.GetByID(ctx, orgID)
					if err != nil {
						return models.OrgSettings{}, err
					}
					return models.ParseOrgSettings(org.Settings)
				}
				socketReconciler := agent.NewSandboxAuthSocketReconciler(
					broker, lister, sessionStore, repoStore, orgSettingsLoader, 0, logger,
				)
				reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 2*time.Minute)
				if err := socketReconciler.ReconcileOnce(reconcileCtx); err != nil {
					logger.Warn().Err(err).Msg("startup: initial sandbox auth socket reconcile failed; periodic loop will retry")
				}
				reconcileCancel()
				go socketReconciler.Run(ctx)
			case brokerOK && !listerOK:
				logger.Warn().Msg("startup: sandbox provider does not support managed-sandbox enumeration; credential sockets will rely on per-turn leases only")
			}
		}

		// Plumb stores into LinearAgentDeps now that the Stores struct is
		// fully constructed. buildServices runs before stores is built, so
		// the deps struct it produces leaves Stores nil; setting it here
		// closes the loop without forcing buildServices to take stores as
		// an argument (which would entangle two otherwise-independent
		// build phases).
		if services.LinearAgentDeps != nil {
			services.LinearAgentDeps.Stores = stores
		}

		processWorkers = startProcessWorkers(
			ctx,
			pool,
			logger,
			cfg.NodeID,
			workerCount,
			stores,
			services,
			retentionCfg,
			jobNotifier,
			nodeManager,
			previewCapable,
			cfg.PreviewInternalBaseURL,
			cfg.NodeRegion,
			previewRoutingReady.Load,
			sandboxCapacity,
			agent.ResolveStaticEgressRuntimeConfig(cfg.StaticEgressPublicIP),
		)
		if workerPreviewStore != nil && cfg.NodeID != "" {
			go runPreviewRuntimeHeartbeat(ctx, workerPreviewStore, cfg.NodeID, logger, 30*time.Second, 90*time.Second)
		}
		if cfg.NodeID != "" {
			go worker.RunNodeDrainWatcher(ctx, db.NewNodeStore(pool), processWorkers, cfg.NodeID, logger, 5*time.Second)
		}

		recoveryLoop := cluster.NewRecoveryLoop(nodeManager, jobStore, logger, 90*time.Second, 100)
		recoveryLoop.SetSessionExecutors(db.NewSessionExecutorStore(pool))
		recoveryLoop.SetPreviewRuntimes(workerPreviewStore)
		go recoveryLoop.Start(ctx, 30*time.Second)
		go worker.RunQueueHealthSampler(ctx, jobStore, logger, time.Minute)
		go worker.RunWorkerLoadSampler(ctx, jobStore, logger, time.Minute)
		go worker.RunRunningJobSampler(ctx, jobStore, logger, time.Minute)
		go worker.RunPreviewHealthSampler(ctx, db.NewPreviewStore(pool), logger, time.Minute)
		go worker.RunHostResourceSampler(ctx, logger, cfg.NodeID, time.Minute)

		usageRollupStore := db.NewUsageRollupStore(pool)
		reaperOpts := []agent.SessionReaperOption{
			agent.WithOrphanCloser(db.NewContainerUsageStore(pool)),
			agent.WithUsageRoller(usageRollupStore),
			agent.WithMaxRunningAge(cfg.SessionMaxRunningAge),
			agent.WithRuntimeJobTerminalizer(jobStore),
			agent.WithThreadRuntimeLeaseReclaimer(db.NewThreadRuntimeStore(pool)),
			// Phase 0.5b safety net: fails session_threads stuck in 'running'
			// past maxRunningAge. Catches orphans the orchestrator/handler
			// thread.status reset paths couldn't unwind themselves.
			agent.WithStuckThreadLister(sessionThreadStore),
		}
		if previewManager != nil {
			previewStore := db.NewPreviewStore(pool)
			nodeStore := db.NewNodeStore(pool)
			selector := preview.NewWorkerSelectorWithOptions(nodeStore, previewStore, preview.WorkerSelectorOptions{
				MaxPreviewsPerWorker: cfg.PreviewMaxPerWorker,
				PreferredRegion:      cfg.NodeRegion,
			})
			previewRPCKeyring, keyringErr := auth.NewPreviewTokenKeyring(cfg.PreviewRPCSecrets)
			if keyringErr != nil {
				logger.Warn().Err(keyringErr).Msg("preview RPC keyring is not configured; preview worker RPC will be unavailable")
				previewRPCKeyring = auth.PreviewTokenKeyring{}
			}
			client := preview.NewWorkerPreviewClientWithKeyring(previewRPCKeyring)
			reaperOpts = append(reaperOpts, agent.WithPreviewStopper(preview.NewWorkerStopper(previewStore, selector, client, cfg.NodeID, previewManager)))
		}
		reaper := agent.NewSessionReaper(sessionStore, snapshotStore, cfg.SessionMaxIdleAge, cfg.SessionMaxSnapshotAge, cfg.SessionReaperInterval, logger, reaperOpts...)
		go reaper.Run(ctx)

		// Runtime resource sampler — emits live memory/CPU histograms per
		// running sandbox so operators can size SANDBOX_* limits against
		// actual usage. nil when sampling is disabled (interval <= 0) or
		// the provider doesn't expose stats.
		if workerServices != nil && workerServices.RuntimeSampler != nil {
			go workerServices.RuntimeSampler.Run(ctx)
		}
		if workerServices != nil && workerServices.SandboxGC != nil {
			go workerServices.SandboxGC.Run(ctx)
		}

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
		scheduler.SetCapabilityResolver(agentcapabilities.NewService(db.NewAgentCapabilityPolicyStore(pool)))
		scheduler.SetSessionStore(sessionStore)
		scheduler.SetDomainRecheck(
			db.NewOrganizationDomainStore(pool),
			domains.NewVerifier(),
			db.NewAuditEmitter(db.NewAuditLogStore(pool), logger),
		)
		scheduler.SetGitHubOrgRosterReconciliation(db.NewGitHubInstallationStore(pool))
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
	// Two independent budgets:
	//   - Worker job drain:    cfg.WorkerDrainTimeout (default 45m). Long
	//     enough to let in-flight coding turns finish; cancelling them
	//     mid-execution produces orphaned thread rows when partial DB state
	//     lands during the orchestrator's cleanup defers. The matching outer
	//     bound is docker-compose.worker.yml stop_grace_period.
	//   - HTTP API drain:      100s. Bounded by docker-compose.app.yml
	//     stop_grace_period=120s with room for node drain + Caddy health
	//     propagation before Docker's SIGKILL deadline. Only load-bearing
	//     on api/all modes.
	//   - Preview gateway:     60s, in parallel with HTTP drain.
	// On worker-only nodes the HTTP drain is a no-op (no traffic) so the
	// long worker budget is the only thing the deploy actually waits on.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info().Msg("shutting down server...")

		for _, w := range processWorkers {
			w.RequestDrain()
		}
		nodeDrainCtx, nodeDrainCancel := context.WithTimeout(context.Background(), nodeDrainMarkTimeout)
		if err := nodeManager.RequestDrain(nodeDrainCtx, time.Now()); err != nil {
			logger.Warn().Err(err).Msg("failed to mark node draining")
		}
		if workerPreviewStore != nil && cfg.NodeID != "" {
			if _, err := workerPreviewStore.MarkPreviewRuntimesDrainingByWorker(nodeDrainCtx, cfg.NodeID); err != nil {
				logger.Warn().Err(err).Str("worker_node_id", cfg.NodeID).Msg("failed to mark preview runtimes draining")
			}
		}
		nodeDrainCancel()

		// Mark /healthz unhealthy before closing the listener. Caddy probes
		// every 2s with a 2s timeout and refreshes dynamic upstream DNS every
		// 2s, so this propagation window covers a full missed-probe cycle
		// plus scheduling slack before Docker stops the old container.
		close(shutdownCh)
		time.Sleep(httpDrainPropagationDelay)

		drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)
		workerDrainTimedOut := false
		for {
			activeJobs := 0
			for _, w := range processWorkers {
				activeJobs += w.ActiveJobCount()
			}
			if activeJobs == 0 {
				break
			}
			select {
			case <-drainCtx.Done():
				logger.Warn().Int("active_jobs", activeJobs).Msg("worker drain timed out; continuing shutdown")
				workerDrainTimedOut = true
				goto workerJobsDrained
			case <-time.After(500 * time.Millisecond):
			}
		}
	workerJobsDrained:
		// After all jobs have completed, wait for any post-PR snapshot
		// uploads spawned by CreatePR to finish. These run in detached
		// goroutines (the worker job has returned) and own a temp file +
		// pending_snapshot_key state; if we exit before they land the
		// session is stuck with pending_snapshot_key set with no in-flight
		// uploader to clear it.
		//
		// This drain is best-effort by design: drainCtx may have been
		// largely consumed by the worker drain above. cluster.Scheduler's
		// reapStrandedPendingSnapshots pass clears any rows the upload
		// goroutines didn't get to (within strandedPendingSnapshotThreshold
		// = 15m), so the worst-case outcome is a delayed resume rather than
		// a permanently stuck row.
		if !workerDrainTimedOut {
			drainPostPRUploads(drainCtx, resolvePostPRSnapshotDrainer(workerServices), logger)
			if jobStore != nil && cfg.NodeID != "" {
				waitForDBOwnedJobsToDrain(drainCtx, jobStore, cfg.NodeID, logger)
			}
		}
		drainCancel()
		if !workerDrainTimedOut && workerPreviewStore != nil && cfg.NodeID != "" {
			if drained := waitForActivePreviewsToDrain(context.Background(), workerPreviewStore, cfg.NodeID, logger, cfg.WorkerPreviewDrainTimeout, 5*time.Second); !drained {
				if _, err := workerPreviewStore.MarkActivePreviewRuntimesLostByWorkerWithReason(context.Background(), cfg.NodeID, "worker preview drain timeout", models.PreviewUnavailableReasonDeployDrainTimeout); err != nil {
					logger.Warn().Err(err).Str("worker_node_id", cfg.NodeID).Msg("failed to mark preview runtimes lost after drain timeout")
				}
			}
		}

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

		// Drain the main API server. The timeout leaves headroom before
		// Docker SIGKILLs the container at stop_grace_period=120s.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("server shutdown failed")
		}
		<-gwDone
		// Drain per-session GitHub credential socket listeners after the
		// API and worker have stopped accepting new turns. Doing this last
		// avoids racing an in-flight turn against socket teardown — by the
		// time we get here, no orchestrator path can call Listen anymore.
		if sandboxAuthShutdown != nil {
			sandboxAuthShutdown()
		}
	}()

	logger.Info().Int("port", cfg.Port).Str("mode", cfg.Mode).Msg("starting server")
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		logger.Fatal().Err(err).Msg("server failed to bind listener")
	}
	previewRoutingReady.Store(true)
	if err := nodeManager.HeartbeatOnce(ctx); err != nil {
		logger.Warn().Err(err).Msg("failed to publish preview routing readiness; next heartbeat will retry")
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server failed")
	}
}

func startProcessWorkers(
	ctx context.Context,
	pool db.DBTX,
	logger zerolog.Logger,
	nodeID string,
	workerCount int,
	stores *worker.Stores,
	services *worker.Services,
	retentionCfg worker.DataRetentionConfig,
	jobNotifier *cache.JobNotifier,
	nodeManager *cluster.NodeManager,
	previewCapable bool,
	previewInternalBaseURL string,
	nodeRegion string,
	previewRoutingReady func() bool,
	sandboxCapacity *agent.SandboxCapacityGate,
	staticEgress agent.StaticEgressRuntimeConfig,
) []*worker.Worker {
	workers := make([]*worker.Worker, 0, workerCount)
	for i := 0; i < workerCount; i++ {
		w := worker.New(pool, logger, nodeID)
		worker.RegisterHandlers(w, stores, services, retentionCfg, logger)
		workers = append(workers, w)
	}

	if jobNotifier != nil {
		jobNotifier.Start(ctx, func() {
			for _, w := range workers {
				w.Wake()
			}
		})
	}

	nodeManager.SetMetadataProvider(buildWorkerMetadataProvider(workers, previewCapable, previewInternalBaseURL, nodeRegion, previewRoutingReady, sandboxCapacity, staticEgress))

	for i, w := range workers {
		go w.Start(ctx)
		logger.Info().Int("worker_index", i).Msg("worker started with registered handlers")
	}
	return workers
}

func resolveWorkerMaxActiveSandboxes(workerProcessCount, configured int) int {
	if configured > 0 {
		return configured
	}
	if workerProcessCount > 0 {
		return workerProcessCount
	}
	return 2
}

// buildBaseMetadata returns the node metadata fields that must appear on every
// heartbeat. SetMetadataProvider replaces the previous provider entirely, so
// any provider installed later (e.g. by startProcessWorkers) must continue to
// emit these fields or the next heartbeat will wipe preview_capable from the
// node row and break preview routing.
func buildBaseMetadata(previewCapable bool, previewInternalBaseURL string, nodeRegion string) map[string]any {
	metadata := map[string]any{
		"build_sha": version.BuildSHA,
	}
	if nodeRegion != "" {
		metadata["region"] = nodeRegion
	}
	if previewCapable {
		metadata["preview_capable"] = true
		metadata["preview_rpc_auth_check"] = true
	}
	if previewInternalBaseURL != "" {
		metadata["preview_internal_base_url"] = previewInternalBaseURL
	}
	return metadata
}

func buildStaticEgressMetadata(runtime agent.StaticEgressRuntimeConfig) map[string]any {
	metadata := map[string]any{}
	if runtime.Enabled && runtime.Capable && runtime.PublicIP != "" {
		metadata["static_egress_capable"] = true
		metadata["static_egress_public_ip"] = runtime.PublicIP
	}
	return metadata
}

func buildWorkerMetadataProvider(workers []*worker.Worker, previewCapable bool, previewInternalBaseURL string, nodeRegion string, previewRoutingReady func() bool, sandboxCapacity *agent.SandboxCapacityGate, staticEgress agent.StaticEgressRuntimeConfig) func() map[string]any {
	return func() map[string]any {
		advertisePreview := previewCapable
		if previewRoutingReady != nil {
			advertisePreview = advertisePreview && previewRoutingReady()
		}
		metadata := buildBaseMetadata(advertisePreview, previewInternalBaseURL, nodeRegion)
		for k, v := range buildStaticEgressMetadata(staticEgress) {
			metadata[k] = v
		}
		metadata["active_job_count"] = totalActiveJobs(workers)
		metadata["active_run_agent_count"] = totalActiveRunAgentJobs(workers)
		if sandboxCapacity != nil {
			snapshotCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			snapshot := sandboxCapacity.Snapshot(snapshotCtx)
			cancel()
			metadata["live_sandbox_count"] = snapshot.Live
			metadata["reserved_sandbox_count"] = snapshot.Reserved
			metadata["max_active_sandboxes"] = snapshot.MaxActive
			if snapshot.CountError != "" {
				metadata["live_sandbox_count_error"] = snapshot.CountError
			}
		}
		return metadata
	}
}

func totalActiveJobs(workers []*worker.Worker) int {
	total := 0
	for _, w := range workers {
		total += w.ActiveJobCount()
	}
	return total
}

func totalActiveRunAgentJobs(workers []*worker.Worker) int {
	total := 0
	for _, w := range workers {
		total += w.ActiveRunAgentCount()
	}
	return total
}

func waitForDBOwnedJobsToDrain(ctx context.Context, jobStore *db.JobStore, nodeID string, logger zerolog.Logger) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, err := jobStore.CountRunningOwnedByNode(ctx, nodeID)
		if err == nil && running == 0 {
			return
		}
		if err != nil {
			logger.Warn().Err(err).Str("node_id", nodeID).Msg("failed to verify DB-owned running jobs during drain")
		} else {
			logger.Info().Str("node_id", nodeID).Int("db_running_jobs", running).Msg("waiting for DB-owned running jobs to drain")
		}
		select {
		case <-ctx.Done():
			logger.Warn().Str("node_id", nodeID).Msg("DB-owned running jobs did not drain before shutdown timeout")
			return
		case <-ticker.C:
		}
	}
}

// postPRSnapshotDrainer is the narrow surface drainPostPRUploads needs from
// the PR service. Declared here (rather than reusing worker.prCreator, which
// is unexported) so the drain function can be unit-tested with a tiny stub
// instead of a full PR service mock.
type postPRSnapshotDrainer interface {
	WaitForPostPRSnapshotUploads()
}

// resolvePostPRSnapshotDrainer extracts the drainer from workerServices,
// returning nil in api-only mode (where workerServices is unset). Kept as a
// helper so the call site doesn't have to introduce a local variable that
// would conflict with the surrounding for/select+goto control flow.
func resolvePostPRSnapshotDrainer(workerServices *worker.Services) postPRSnapshotDrainer {
	if workerServices == nil || workerServices.PR == nil {
		return nil
	}
	return workerServices.PR
}

// drainPostPRUploads blocks until the PR service has finished all in-flight
// post-PR snapshot uploads, or until drainCtx expires. A timeout here is
// non-fatal: cluster.Scheduler.reapStrandedPendingSnapshots will recover any
// pending_snapshot_key rows whose owning upload was killed when this drain
// timed out, so worst-case is a delayed (not permanent) resume for affected
// sessions.
func drainPostPRUploads(drainCtx context.Context, drainer postPRSnapshotDrainer, logger zerolog.Logger) {
	if drainer == nil {
		return
	}
	uploadsDone := make(chan struct{})
	go func() {
		defer close(uploadsDone)
		drainer.WaitForPostPRSnapshotUploads()
	}()
	select {
	case <-uploadsDone:
	case <-drainCtx.Done():
		logger.Warn().Msg("post-PR snapshot upload drain timed out; cluster.Scheduler reaper will clear stranded pending_snapshot_key rows")
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

type internalSandboxAuthRouter interface {
	Post(pattern string, h http.HandlerFunc)
}

func registerInternalSandboxAuthRoutes(router internalSandboxAuthRouter, broker handlers.InternalSandboxAuthBroker, cfg *config.Config, logger zerolog.Logger) {
	if router == nil || broker == nil || cfg == nil {
		return
	}
	keyring, err := auth.NewPreviewTokenKeyring(cfg.PreviewRPCSecrets)
	if err != nil {
		logger.Warn().Err(err).Msg("sandbox auth: preview RPC keyring is not configured; internal sandbox auth RPC unavailable")
		return
	}
	handler := handlers.NewInternalSandboxAuthHandler(broker, cfg.NodeID, keyring, logger)
	router.Post("/internal/sandbox-auth/acquire", handler.Acquire)
	router.Post("/internal/sandbox-auth/release", handler.Release)
}

func validateSessionExecutorStartupConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if cfg.Env == "production" && (cfg.Mode == "worker" || cfg.Mode == "all") && cfg.SessionExecutorImage == "" {
		return fmt.Errorf("SESSION_EXECUTOR_IMAGE is required in production %s mode", cfg.Mode)
	}
	return nil
}

func configureSessionExecutorDispatch(
	svc *worker.Services,
	cfg *config.Config,
	pool *pgxpool.Pool,
	dockerCli *dockerclient.Client,
	jobStore *db.JobStore,
	logger zerolog.Logger,
) {
	if svc == nil || cfg == nil {
		return
	}
	if cfg.SessionExecutorImage == "" {
		logger.Warn().Msg("SESSION_EXECUTOR_IMAGE is empty; using inline run_agent/continue_session execution outside production")
		return
	}

	svc.RequireSessionExecutorDispatcher = true
	executorLauncher := worker.NewDockerExecutorLauncher(dockerCli, worker.DockerExecutorLauncherConfig{
		Image:       cfg.SessionExecutorImage,
		NetworkMode: cfg.SessionExecutorDockerNetwork,
		Binds:       sessionExecutorBinds(cfg.SessionExecutorExtraBinds),
		GroupAdd:    sessionExecutorGroupAddFromEnv(),
		Env:         os.Environ(),
		StopTimeout: cfg.SessionExecutorStopTimeout,
	})
	executorLauncher.SetLogger(logger)
	svc.SessionExecutorDispatcher = &worker.DurableSessionExecutorDispatcher{
		Executors:             db.NewSessionExecutorStore(pool),
		Jobs:                  jobStore,
		Launcher:              executorLauncher,
		NodeID:                cfg.NodeID,
		Image:                 cfg.SessionExecutorImage,
		BuildSHA:              version.BuildSHA,
		ResolveRuntimeCeiling: svc.Orchestrator.ResolveAbsoluteRuntimeCeiling,
		Logger:                logger,
	}
}

func sessionExecutorBinds(extraBinds []string) []string {
	binds := []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		"/var/run/143/sandbox-auth:/var/run/143/sandbox-auth",
		"/etc/143:/etc/143:ro",
	}
	for _, bind := range extraBinds {
		bind = strings.TrimSpace(bind)
		if bind == "" {
			continue
		}
		binds = append(binds, bind)
	}
	return binds
}

func sessionExecutorIDFromEnv() (uuid.UUID, bool, error) {
	raw := strings.TrimSpace(os.Getenv("SESSION_EXECUTOR_ID"))
	if raw == "" {
		return uuid.Nil, false, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, true, fmt.Errorf("parse SESSION_EXECUTOR_ID: %w", err)
	}
	return id, true, nil
}

func sessionExecutorGroupAddFromEnv() []string {
	return sessionExecutorGroupAdd(os.Getenv("DOCKER_GID"))
}

func sessionExecutorGroupAdd(dockerGID string) []string {
	if dockerGID == "" {
		return nil
	}
	return []string{dockerGID}
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
	codingCredentialStore *db.CodingCredentialStore,
	issueStore *db.IssueStore,
	sessionStore *db.SessionStore,
	jobStore *db.JobStore,
	orgStore *db.OrganizationStore,
	repoStore *db.RepositoryStore,
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
	evalBootstrapStore *db.EvalBootstrapStore,
	snapshotStore storage.SnapshotStore,
	billingMetrics *metrics.BillingMetrics,
	cancelRegistry *agent.CancelRegistry,
	threadCancelRegistry *agent.ThreadCancelRegistry,
	orgSettingsCache *agent.OrgSettingsCache,
	sandboxCapacity *agent.SandboxCapacityGate,
	redisClient *cache.Client,
	sessionStreams *cache.SessionStreams,
	fileReader sandbox.FileReader,
) *worker.Services {
	// GitHub App service (for installation tokens, PR creation).
	ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey, logger)
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
	sandboxProvider := providers.NewDockerProvider(
		dockerCli,
		logger,
		providers.WithRuntime(cfg.SandboxRuntime),
		providers.WithResolvConf(cfg.SandboxResolvConf),
		providers.WithHealthCheckImage(cfg.SandboxHealthCheckImage),
		providers.WithRequireDiskQuota(cfg.SandboxRequireDiskQuota),
		providers.WithAuthSocketPreflightDir(cfg.SandboxAuthSocketDir),
	)
	mentionIndexCache := workspace.NewMentionIndexCache(workspace.MentionIndexCacheConfig{
		Redis:  redisClient,
		Logger: logger,
	})

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
			if errors.Is(healthErr, providers.ErrDiskQuotaUnsupported) {
				logger.Error().Err(healthErr).Msg("sandbox disk quota is required but Docker storage cannot enforce it — all Phase 3+ services disabled")
				return nil
			}
			if cfg.SandboxRuntime == "runsc" && !cfg.SandboxRequireGVisor {
				logger.Warn().Err(healthErr).Msg("gVisor not available, falling back to runc — NOT RECOMMENDED FOR PRODUCTION")
				sandboxProvider = providers.NewDockerProvider(
					dockerCli,
					logger,
					providers.WithRuntime("runc"),
					providers.WithResolvConf(cfg.SandboxResolvConf),
					providers.WithHealthCheckImage(cfg.SandboxHealthCheckImage),
					providers.WithRequireDiskQuota(cfg.SandboxRequireDiskQuota),
					providers.WithAuthSocketPreflightDir(cfg.SandboxAuthSocketDir),
				)
				healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
				fallbackErr := sandboxProvider.HealthCheck(healthCtx)
				healthCancel()
				if fallbackErr != nil {
					logger.Error().Err(fallbackErr).Msg("fallback runc sandbox health check failed — Phase 3+ services disabled")
					return nil
				}
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

	var appUserAuthSvc *ghservice.AppUserAuthService
	if cfg.GitHubAppClientID != "" && cfg.GitHubAppClientSecret != "" {
		appUserAuthSvc = ghservice.NewAppUserAuthService(userCredentialStore, cfg.GitHubAppClientID, cfg.GitHubAppClientSecret, cfg.BaseURL, logger)
	}

	// Agent adapters. Shared factory with the router; see adapters.DefaultMap.
	agentAdapters := adapters.DefaultMap(logger)

	// Shared agent env/auth helper — consumed by both the session Orchestrator
	// and the PM service so both paths resolve provider credentials, Codex
	// auth.json, and agent_config overrides through a single code path.
	agentEnv := agent.NewAgentEnv(agent.AgentEnvDeps{
		Credentials:           credentialStore,
		CodingCredentials:     codingCredentialStore,
		Orgs:                  orgStore,
		OrgSettingsCache:      orgSettingsCache,
		CodexAuth:             codexAuthSvc,
		PagerDutyIntegrations: db.NewPagerDutyIntegrationStore(pool),
		Provider:              sandboxProvider,
		Logger:                logger,
	})

	// Orchestrator.
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionQuestionStore := db.NewSessionQuestionStore(pool)
	sessionHumanInputStore := db.NewSessionHumanInputRequestStore(pool)
	sessionThreadStore := db.NewSessionThreadStore(pool)
	sessionThreadStore.SetLogger(logger)
	if sessionStreams != nil {
		sessionThreadStore.SetStreams(sessionStreams)
	}
	threadInboxStore := db.NewThreadInboxStore(pool)
	threadRuntimeStore := db.NewThreadRuntimeStore(pool)
	sessionSandboxHolderStore := db.NewSessionSandboxHolderStore(pool)
	reviewLoopStore := db.NewSessionReviewLoopStore(pool)
	projectTaskUpdater := pm.NewProjectHooks(projectTaskStore, projectStore, logger)
	automationStore := db.NewAutomationStore(pool)
	automationRunUpdater := automations.NewAutomationHooks(automationRunStore, logger)
	automationGoalImprovementStore := db.NewAutomationGoalImprovementStore(pool)
	automationGoalImprovementUpdater := automations.NewGoalImprovementService(automationGoalImprovementStore, automationStore, automationRunStore, sessionStore, jobStore, pool, llmClient)
	auditEmitter := db.NewAuditEmitter(db.NewAuditLogStore(pool), logger)
	automationGoalImprovementUpdater.SetAuditEmitter(auditEmitter)
	containerUsageStore := db.NewContainerUsageStore(pool)
	usageTracker := agent.NewUsageTracker(containerUsageStore, billingMetrics, logger)

	// Identity resolver + per-session credential socket server. Wired
	// together so an agent's `git push` / read-only `gh pr view` can reach a fresh
	// GitHub token without the host ever planting a long-lived secret in the
	// container's env. Local development may omit SandboxAuthSocketDir and use
	// the legacy fallback; production worker/all configuration validation
	// requires the socket path and startup preflight below must succeed.
	userStore := db.NewUserStore(pool)
	identityResolver := identity.NewResolver(ghSvc, logger)
	if appUserAuthSvc != nil {
		identityResolver.SetAppUserAuth(appUserAuthSvc)
	}
	identityResolver.SetUsers(userStore)
	identityResolver.SetIntegrations(integrationStore)
	var (
		sandboxAuthBroker       *sandboxauth.Broker
		orchestratorSandboxAuth agent.SandboxAuthServer
		prSandboxAuth           agent.SandboxAuthServer
	)
	if cfg.SandboxAuthSocketDir != "" {
		if executorID, ok, err := sessionExecutorIDFromEnv(); err != nil {
			logger.Error().Err(err).Msg("sandbox auth: invalid session executor id — worker services disabled")
			return nil
		} else if ok {
			if strings.TrimSpace(cfg.PreviewInternalBaseURL) == "" {
				logger.Error().Msg("sandbox auth: PREVIEW_INTERNAL_BASE_URL is required for session executor remote broker access")
				return nil
			}
			previewRPCKeyring, keyringErr := auth.NewPreviewTokenKeyring(cfg.PreviewRPCSecrets)
			if keyringErr != nil {
				logger.Error().Err(keyringErr).Msg("sandbox auth: preview RPC keyring is required for session executor remote broker access")
				return nil
			}
			orchestratorSandboxAuth = sandboxauth.NewRemoteBrokerClient(sandboxauth.RemoteBrokerClientConfig{
				BaseURL:  cfg.PreviewInternalBaseURL,
				NodeID:   cfg.NodeID,
				HolderID: executorID,
				Keyring:  previewRPCKeyring,
				Logger:   logger,
			})
			prSandboxAuth = orchestratorSandboxAuth
			logger.Info().
				Str("worker_base_url", cfg.PreviewInternalBaseURL).
				Str("executor_id", executorID.String()).
				Msg("sandbox auth: session executor will use worker-owned remote broker")
		} else {
			if cfg.Env == "production" {
				if err := sandboxauth.ValidateSocketDirForStartup(cfg.SandboxAuthSocketDir); err != nil {
					logger.Error().
						Err(err).
						Str("socket_dir", cfg.SandboxAuthSocketDir).
						Msg("sandbox auth: socket directory preflight failed — worker services disabled")
					return nil
				}
			}
			sandboxAuthServer := sandboxauth.NewServer(identityResolver, cfg.SandboxAuthSocketDir, logger)
			sandboxAuthBroker = sandboxauth.NewBroker(sandboxAuthServer, sessionStore, repoStore, orgStore, logger)
			orchestratorSandboxAuth = sandboxauth.NewLeaseClient(sandboxAuthBroker, "worker-orchestrator", logger)
			prSandboxAuth = sandboxauth.NewLeaseClient(sandboxAuthBroker, "worker-pr", logger)
			logger.Info().
				Str("socket_dir", cfg.SandboxAuthSocketDir).
				Msg("sandbox auth: worker-owned credential socket broker enabled")
		}
	} else {
		logger.Warn().
			Msg("sandbox auth: SANDBOX_AUTH_SOCKET_DIR is empty; per-session credential socket disabled — sandbox `git push` will require GITHUB_TOKEN env fallback")
	}

	uploadStore := buildUploadStore(context.Background(), cfg, logger)

	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:                   sandboxProvider,
		Adapters:                   agentAdapters,
		Env:                        agentEnv,
		Sessions:                   sessionStore,
		SessionLogs:                sessionLogStore,
		SessionQuestions:           sessionQuestionStore,
		HumanInputRequests:         sessionHumanInputStore,
		SessionMessages:            sessionMessageStore,
		SessionThreads:             sessionThreadStore,
		SessionIssueLinks:          db.NewSessionIssueLinkStore(pool),
		IssueSnapshots:             db.NewSessionTurnIssueSnapshotStore(pool),
		DecisionLog:                pmDecisionLogStore,
		ProjectTasks:               projectTaskUpdater,
		AutomationRuns:             automationRunUpdater,
		AutomationGoalImprovements: automationGoalImprovementUpdater,
		Issues:                     issueStore,
		Repositories:               repoStore,
		PRReadiness:                db.NewPRReadinessStore(pool),
		Orgs:                       orgStore,
		Jobs:                       jobStore,
		GitHub:                     ghSvc,
		CodexAuth:                  codexAuthSvc,
		ClaudeCodeAuth:             claudeCodeAuthSvc,
		Credentials:                credentialStore,
		CodingCredentials:          codingCredentialStore,
		Snapshots:                  snapshotStore,
		Uploads:                    uploadStore,
		FileReader:                 fileReader,
		MentionIndexes:             mentionIndexCache,
		UsageTracker:               usageTracker,
		SandboxCapacity:            sandboxCapacity,
		StaticEgress:               agent.ResolveStaticEgressRuntimeConfig(cfg.StaticEgressPublicIP),
		ThreadRuntimes:             threadRuntimeStore,
		ThreadInbox:                threadInboxStore,
		SandboxHolders:             sessionSandboxHolderStore,
		Cancels:                    cancelRegistry,
		ThreadCancels:              threadCancelRegistry,
		OrgSettingsCache:           orgSettingsCache,
		IdentityResolver:           identityResolver,
		SandboxAuth:                orchestratorSandboxAuth,
		Users:                      userStore,
		EvalBootstraps:             evalBootstrapStore,
		InternalAPIURL:             cfg.BaseURL,
		InternalAPISecret:          cfg.SessionSecret,
		NodeID:                     cfg.NodeID,
		Logger:                     logger,
	})

	// PR service.
	prTemplateStore := db.NewPRTemplateStore(pool)
	prService := ghservice.NewPRService(
		ghSvc, pullRequestStore, sessionStore, issueStore,
		deployStore, repoStore, jobStore, logger,
	)
	workerFeedbackStore := db.NewPullRequestFeedbackStore(pool)
	workerFeedbackStore.SetJobStore(jobStore)
	prService.SetPullRequestFeedbackStore(workerFeedbackStore)
	prService.SetChangesetStore(db.NewSessionChangesetStore(pool))
	prService.SetPublicationStore(db.NewSessionPublicationStore(pool))
	prService.SetPRPreviewSurfacesEnabled(cfg.PRPreviewSurfacesEnabled)
	wireWorkerPRService(
		prService,
		sandboxProvider,
		snapshotStore,
		prSandboxAuth,
		integrationStore,
		userCredentialStore,
		appUserAuthSvc,
		userStore,
		orgStore,
		llmClient,
		prTemplateStore,
		db.NewPRReadinessStore(pool),
		redisClient,
		logger,
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
	pmSvc.SetSessionMessageStore(sessionMessageStore)
	pmSvc.SetInternalAPI(cfg.BaseURL, cfg.SessionSecret)
	pmSvc.SetSkillsBuilder(orchestrator)
	threadSvc := threadservice.NewService(
		sessionThreadStore,
		sessionStore,
		sessionMessageStore,
		sessionLogStore,
		jobStore,
		logger,
	)
	threadSvc.SetOwnerLossOrchestrator(ownerloss.NewService(
		sessionStore,
		db.NewSessionExecutorStore(pool),
		jobStore,
		jobStore,
		logger,
	))
	threadSvc.SetThreadInboxStore(threadInboxStore, pool)
	threadSvc.SetThreadRuntimeStore(threadRuntimeStore)
	reviewLoopSvc := reviewloopservice.NewService(
		reviewLoopStore,
		reviewloopservice.RuntimeAdapter{
			Sessions: sessionStore,
			Threads:  threadSvc,
		},
		reviewloopservice.WithAutoReadinessDependencies(orgStore, userStore, pool, jobStore),
	)

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
		titleService = services.NewSessionTitleService(llmClient, sessionStore, sessionMessageStore, sessionThreadStore)
	}

	// Linear session-linking service. Drives prepare_linear_primary,
	// link_linear_issue, refresh_linear_team_keys workers and handles the
	// post-link milestones (PR open / PR merged / etc.). Constructed via
	// the shared Build helper so the API server (router.go) and the worker
	// (here) wire the service identically.
	//
	// Inbound-agent metrics shared between Service.HandleAgentMilestone
	// and (in MODE=all) the API router's dispatcher. Failure to register
	// the OTel instruments is non-fatal — nil-safe RecordX helpers
	// degrade to no-ops.
	workerLinearAgentMetrics, mErr := metrics.NewLinearAgentMetrics()
	if mErr != nil {
		logger.Warn().Err(mErr).Msg("failed to register linear_agent metrics in worker; milestone emits will not record")
	}
	workerSlackbotMetrics, slackMetricsErr := metrics.NewSlackbotMetrics()
	if slackMetricsErr != nil {
		logger.Warn().Err(slackMetricsErr).Msg("failed to register slackbot metrics in worker; Slackbot emits will not record")
	}
	workerPagerDutyMetrics, pagerDutyMetricsErr := metrics.NewPagerDutyMetrics()
	if pagerDutyMetricsErr != nil {
		logger.Warn().Err(pagerDutyMetricsErr).Msg("failed to register PagerDuty metrics in worker; PagerDuty paths will not record")
	}
	linearService := linear.Build(linear.BuildDeps{
		Pool:               pool,
		Logger:             logger,
		Integrations:       integrationStore,
		IntegrationsWriter: integrationStore,
		Credentials:        credentialStore,
		CredentialsWriter:  credentialStore,
		Issues:             issueStore,
		Sessions:           sessionStore,
		IssueLinks:         db.NewSessionIssueLinkStore(pool),
		Orgs:               orgStore,
		Jobs:               jobStore,
		OAuthClient: linear.OAuthClientCreds{
			ClientID:     cfg.LinearOAuthClientID,
			ClientSecret: cfg.LinearOAuthClientSecret,
		},
		AppBaseURL:   cfg.FrontendURL,
		AgentMetrics: workerLinearAgentMetrics,
	})
	prService.SetLinearMilestoneEnqueuer(linear.MilestoneEnqueuerFor(jobStore, logger))
	// Wire the linear service into the agent env so sandbox-bound
	// LINEAR_ACCESS_TOKEN is resolved through the refresh path. Without
	// this, sessions that start within the refresh window of a token's
	// expiry would inject a soon-to-be-stale token and the agent's
	// 143-tools would 401 mid-turn. SetLinearTokens is a no-op when called
	// before linearService is built, but the orchestrator construction
	// happens on the same goroutine after linear.Build returns, so this
	// ordering is deterministic.
	agentEnv.SetLinearTokens(linearService)

	// PAGERDUTY_ACCESS_TOKEN is resolved through a refresh-aware service so a
	// scoped OAuth token nearing expiry is rotated (and persisted) before the
	// sandbox starts, rather than 401-ing mid-session ~24h after connect.
	agentEnv.SetPagerDutyTokens(pagerdutysvc.NewTokenService(
		credentialStore,
		credentialStore,
		pagerdutysvc.PagerDutyOAuthClientCreds{
			ClientID:     cfg.PagerDutyOAuthClientID,
			ClientSecret: cfg.PagerDutyOAuthClientSecret,
		},
		logger,
	))

	// Runtime resource sampler. Optional capability — only providers that
	// implement RuntimeStatsProvider produce samples. Disabled when the
	// interval is non-positive (operators can switch this off if the OTel
	// pipeline isn't wired up yet). The runtime type assertion goes
	// through an explicit any() conversion because Go only allows type
	// assertions on interface static types, and sandboxProvider's static
	// type is the concrete *DockerProvider here.
	var runtimeSampler *agent.RuntimeSampler
	if cfg.RuntimeStatsInterval > 0 {
		if statsProvider, ok := any(sandboxProvider).(agent.RuntimeStatsProvider); ok {
			runtimeSampler = agent.NewRuntimeSampler(usageTracker, statsProvider, billingMetrics, cfg.RuntimeStatsInterval, logger, cfg.NodeID)
		} else {
			logger.Info().Msg("sandbox provider does not implement RuntimeStatsProvider; runtime sampler disabled")
		}
	}

	var sandboxGC *agent.SandboxGC
	if cfg.SandboxGCInterval > 0 {
		if gcProvider, ok := any(sandboxProvider).(agent.SandboxGCProvider); ok {
			sandboxGC = agent.NewSandboxGC(gcProvider, sessionStore, containerUsageStore, agent.SandboxGCConfig{
				Interval:                cfg.SandboxGCInterval,
				UnreferencedGracePeriod: cfg.SandboxGCGrace,
				HardMaxAge:              cfg.SandboxGCHardMax,
			}, logger)
		} else {
			logger.Info().Msg("sandbox provider does not support managed-container listing; sandbox GC disabled")
		}
	}

	codeReviewLifecycleStore := db.NewCodeReviewStore(pool)
	codeReviewLifecycleStore.SetStreams(cache.NewCodeReviewStreams(redisClient, logger))
	codeReviewLifecycleStore.SetLogger(logger)
	codeReviewLifecycle := codereviewsvc.NewService(
		codeReviewLifecycleStore,
		codeReviewLifecycleStore,
		sessionStore,
		jobStore,
		logger,
		codereviewsvc.Config{
			AppReviewerLogins: cfg.CodeReviewAppReviewerLogins,
			AliasLogins:       cfg.CodeReviewAliasLogins,
			TeamSlugs:         cfg.CodeReviewTeamSlugs,
		},
	)
	svc := &worker.Services{
		Orchestrator:    orchestrator,
		PR:              prService,
		Failure:         failureSvc,
		SandboxProvider: sandboxProvider,
		ProjectTasks:    projectTaskUpdater,
		AutomationRuns:  automationRunUpdater,
		Prioritization:  prioritizationSvc,
		PM:              pmSvc,
		SlackSummarizer: slackSummarizer,
		LLM:             llmClient,
		GitHub:          ghSvc,
		CodeReviews: codereviewsvc.NewGitHubSubmitter(
			ghSvc,
			codereviewsvc.WithGitHubSubmitterHTTPClient(githubtelemetry.NewHTTPClient(15*time.Second, logger)),
		),
		CodeReviewLifecycle: codeReviewLifecycle,
		CodingAgents:        agentEnv,
		GitHubOrgRoster:     ghSvc,
		Snapshots:           snapshotStore,
		TitleService:        titleService,
		Linear:              linearService,
		SlackbotMetrics:     workerSlackbotMetrics,
		Redis:               redisClient,
		FrontendURL:         cfg.FrontendURL,
		ReviewLoops:         reviewLoopSvc,
		RuntimeSampler:      runtimeSampler,
		SandboxGC:           sandboxGC,
		SandboxAuthBroker:   sandboxAuthBroker,
	}
	pagerDutyTriggerer := automations.NewPagerDutyEventTriggerService(
		db.NewAutomationEventTriggerStore(pool),
		db.NewAutomationStore(pool),
		automationRunStore,
		jobStore,
		pool,
		logger,
	)
	pagerDutyIntegrationStore := db.NewPagerDutyIntegrationStore(pool)
	pagerDutyIncidentStore := db.NewPagerDutyIncidentStore(pool)
	defaultWorkRepositoryView := db.LinearAgentSettingsView{Orgs: orgStore, Repos: repoStore}
	pagerDutyIngester := ingestion.NewService(issueStore, db.NewWebhookDeliveryStore(pool), jobStore, logger)
	pagerDutyWritebacks := pagerdutysvc.NewWritebackService(pagerdutysvc.WritebackDeps{
		Integrations:  pagerDutyIntegrationStore,
		Credentials:   credentialStore,
		Incidents:     pagerDutyIncidentStore,
		ProviderState: db.NewPagerDutyProviderStateStore(pool),
		FrontendURL:   cfg.FrontendURL,
		Audit:         auditEmitter,
		Metrics:       workerPagerDutyMetrics,
		Logger:        logger,
	})
	orchestrator.SetPagerDutyWritebacker(pagerDutyWritebacks)
	automationRunUpdater.SetPagerDutyWritebacker(pagerDutyWritebacks)
	pagerDutyTriggerer.SetRepositoryResolver(db.NewPagerDutyServiceRepoMappingStore(pool), pagerDutyIntegrationStore)
	pagerDutyTriggerer.SetDefaultRepositoryResolver(defaultWorkRepositoryView)
	pagerDutyTriggerer.SetCapabilityResolver(agentcapabilities.NewService(db.NewAgentCapabilityPolicyStore(pool)))
	pagerDutyTriggerer.SetMetrics(workerPagerDutyMetrics)
	pagerDutyTriggerer.SetAuditEmitter(auditEmitter)
	svc.PagerDuty = pagerdutysvc.NewProcessor(pagerdutysvc.ProcessorDeps{
		Events:       db.NewPagerDutyInboundEventStore(pool),
		Integrations: pagerDutyIntegrationStore,
		Ingester:     pagerDutyIngester,
		Issues:       issueStore,
		Incidents:    pagerDutyIncidentStore,
		Triggers:     pagerDutyTriggerer,
		Metrics:      workerPagerDutyMetrics,
	})
	svc.PagerDutySync = pagerdutysvc.NewSyncer(pagerdutysvc.SyncerDeps{
		Integrations: pagerDutyIntegrationStore,
		Credentials:  credentialStore,
		Ingester:     pagerDutyIngester,
		Issues:       issueStore,
		Incidents:    pagerDutyIncidentStore,
		Metrics:      workerPagerDutyMetrics,
		Logger:       logger,
	})
	svc.PagerDutyWrites = pagerDutyWritebacks
	configureSessionExecutorDispatch(svc, cfg, pool, dockerCli, jobStore, logger)

	// Linear inbound-agent worker wiring. The process-wide
	// LINEAR_AGENT_ENABLED flag gates the webhook dispatcher, not the
	// worker handler: disabling the flag must stop new inbound events while
	// still allowing already-enqueued linear_agent_event jobs to drain.
	if linearService != nil {
		linearAgentSettingsView := db.LinearAgentSettingsView{Orgs: orgStore, Repos: repoStore}
		repoResolver := linear.NewAgentRepoResolver(
			db.NewLinearTeamRepoMappingStore(pool),
			linearAgentSettingsView,
			repoStore,
		)
		svc.LinearAgentDeps = &worker.LinearAgentEventHandlerDeps{
			Stores:         nil, // populated below in BuildStores
			Linear:         linearService,
			RepoResolver:   repoResolver,
			ProviderState:  db.NewLinearProviderStateStore(pool),
			SettingsLoader: linearAgentSettingsView.LoadAgentSettings,
			OrgSettingsLoader: func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error) {
				org, err := orgStore.GetByID(ctx, orgID)
				if err != nil {
					return models.OrgSettings{}, err
				}
				return models.ParseOrgSettings(org.Settings)
			},
			ClientForOrg: func(ctx context.Context, orgID uuid.UUID) (linear.Client, error) {
				return linearService.ClientForOrg(ctx, orgID)
			},
			Metrics: workerLinearAgentMetrics,
			Logger:  logger,
		}
	}
	if sandboxAuthBroker != nil {
		// Capture by value: the closure outlives buildServices, but the
		// *Broker pointer is stable for the process lifetime.
		b := sandboxAuthBroker
		svc.SandboxAuthShutdown = b.Shutdown
	}
	return svc
}

func buildUploadStore(ctx context.Context, cfg *config.Config, logger zerolog.Logger) storage.UploadStore {
	if cfg.UploadS3Bucket == "" {
		return storage.NewFileUploadStore(cfg.UploadStorageDir, "/api/v1/uploads/files")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.UploadS3Region))
	if err != nil {
		logger.Warn().Err(err).Msg("failed to load AWS config for upload S3 — falling back to file uploads")
		return storage.NewFileUploadStore(cfg.UploadStorageDir, "/api/v1/uploads/files")
	}
	logger.Info().Str("bucket", cfg.UploadS3Bucket).Str("prefix", cfg.UploadS3Prefix).Msg("upload S3 store configured for worker attachment reads")
	return storage.NewS3UploadStore(s3.NewFromConfig(awsCfg), cfg.UploadS3Bucket, cfg.UploadS3Prefix)
}

func wireWorkerPRService(
	prService *ghservice.PRService,
	sandboxProvider agent.SandboxProvider,
	snapshotStore storage.SnapshotStore,
	sandboxAuthServer agent.SandboxAuthServer,
	integrationStore *db.IntegrationStore,
	userCredentialStore *db.UserCredentialStore,
	appUserAuthSvc *ghservice.AppUserAuthService,
	userStore *db.UserStore,
	orgStore *db.OrganizationStore,
	llmClient llm.Client,
	prTemplateStore *db.PRTemplateStore,
	prReadinessStore *db.PRReadinessStore,
	redisClient *cache.Client,
	logger zerolog.Logger,
) {
	if prService == nil {
		return
	}
	prService.SetSandboxPushDeps(sandboxProvider, snapshotStore)
	prService.SetSandboxAuth(sandboxAuthServer)
	prService.SetIntegrationStore(integrationStore)
	prService.SetUserCredentialStore(userCredentialStore)
	prService.SetAppUserAuth(appUserAuthSvc)
	prService.SetUserStore(userStore)
	prService.SetOrgStore(orgStore)
	prService.SetLLMClient(llmClient)
	prService.SetPRTemplateStore(prTemplateStore)
	prService.SetReadinessStore(prReadinessStore)
	prService.SetRedisClient(redisClient)
	prService.SetPullRequestStreams(cache.NewPullRequestStreams(redisClient, logger))
}
