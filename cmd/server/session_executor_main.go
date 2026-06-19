package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/worker"
)

func isSessionExecutorInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if filepath.Base(args[0]) == "session-executor" {
		return true
	}
	return len(args) > 1 && args[1] == "session-executor"
}

func runSessionExecutorMain() {
	cfg := config.Load()
	logger := logging.NewLogger(cfg.LogLevel, cfg.Env)
	if err := cfg.ValidateSecrets(); err != nil {
		logger.Fatal().Err(err).Msg("security configuration check failed")
	}

	executorID, err := parseSessionExecutorID(os.Args)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid session executor arguments")
	}
	if cfg.NodeID == "" {
		hostname, _ := os.Hostname()
		cfg.NodeID = hostname
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPoolWithOptions(ctx, cfg.DatabaseURL, db.PoolOptions{
		MaxConns:        cfg.DatabaseMaxConns,
		MaxConnIdleTime: cfg.DatabaseMaxConnIdleTime,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()
	if err := db.EnsureAnthropicSplitSentinel(ctx, pool); err != nil {
		logger.Fatal().Err(err).Msg("coding-credentials migration gate failed; session executor refusing to start")
	}

	runtime, shutdown, err := buildSessionExecutorRuntime(ctx, cfg, pool, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to build session executor runtime")
	}
	defer shutdown()

	logger.Info().Str("executor_id", executorID.String()).Str("node_id", cfg.NodeID).Msg("session executor starting")
	if err := runtime.Run(ctx, executorID); err != nil {
		logger.Fatal().Err(err).Str("executor_id", executorID.String()).Msg("session executor failed")
	}
	logger.Info().Str("executor_id", executorID.String()).Msg("session executor completed")
}

func parseSessionExecutorID(args []string) (uuid.UUID, error) {
	fs := flag.NewFlagSet("session-executor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var raw string
	fs.StringVar(&raw, "executor-id", os.Getenv("SESSION_EXECUTOR_ID"), "session executor id")
	parseArgs := args[1:]
	if len(parseArgs) > 0 && parseArgs[0] == "session-executor" {
		parseArgs = parseArgs[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return uuid.Nil, err
	}
	if raw == "" {
		return uuid.Nil, fmt.Errorf("--executor-id or SESSION_EXECUTOR_ID is required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse executor id: %w", err)
	}
	return id, nil
}

func buildSessionExecutorRuntime(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger) (*worker.SessionExecutorRuntime, func(), error) {
	var cryptoSvc *crypto.Service
	if cfg.EncryptionMasterKey != "" {
		var err error
		cryptoSvc, err = crypto.NewService(cfg.EncryptionMasterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize crypto service: %w", err)
		}
	}

	redisMetrics, err := cache.NewMetrics()
	if err != nil {
		return nil, nil, fmt.Errorf("initialize Redis metrics: %w", err)
	}
	redisClient := cache.New(cache.Config{
		Topology:   cfg.RedisTopology,
		URL:        cfg.RedisURL,
		Addrs:      cache.ParseAddrs(cfg.RedisAddrs),
		MasterName: cfg.RedisMasterName,
		Password:   cfg.RedisPassword,
		PoolSize:   cfg.RedisPoolSize,
	}, logger, redisMetrics)

	shutdown := func() {
		if redisClient != nil {
			if err := redisClient.Close(); err != nil {
				logger.Warn().Err(err).Msg("failed to close Redis client")
			}
		}
	}

	credentialStore := db.NewOrgCredentialStore(pool, cryptoSvc)
	userCredentialStore := db.NewUserCredentialStore(pool, cryptoSvc)
	codingCredentialStore := db.NewCodingCredentialStore(pool, cryptoSvc)
	scopedCredentialStore := db.NewScopedCredentialStore(codingCredentialStore)
	codexAuthSvc := codexauth.NewService(scopedCredentialStore, logger)
	claudeCodeAuthSvc := claudecodeauth.NewService(scopedCredentialStore, logger)

	sessionStore := db.NewSessionStore(pool)
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionMessageStore := db.NewSessionMessageStore(pool)
	sessionThreadStore := db.NewSessionThreadStore(pool)
	jobStore := db.NewJobStore(pool)
	jobStore.SetLogger(logger)
	if jobNotifier := cache.NewJobNotifier(redisClient, logger); jobNotifier != nil {
		jobStore.SetNotifier(jobNotifier)
	}
	sessionStreams := cache.NewSessionStreams(redisClient, logger, redisMetrics)
	sessionStore.SetLogger(logger)
	sessionLogStore.SetLogger(logger)
	sessionThreadStore.SetLogger(logger)
	if sessionStreams != nil {
		sessionStore.SetStreams(sessionStreams)
		sessionLogStore.SetStreams(sessionStreams)
		sessionThreadStore.SetStreams(sessionStreams)
	}

	containerUsageStore := db.NewContainerUsageStore(pool)
	billingMetrics, err := metrics.NewBillingMetrics(containerUsageStore.CountActive)
	if err != nil {
		return nil, shutdown, fmt.Errorf("initialize billing metrics: %w", err)
	}

	fileReader := sandbox.FileReader(sandbox.NoOpFileReader{})
	var sandboxCapacity *agent.SandboxCapacityGate
	apiDockerCli, dockerErr := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if dockerErr == nil {
		fileReader = sandbox.NewDockerFileReader(apiDockerCli)
		sandboxExec := providers.NewDockerProvider(
			apiDockerCli,
			logger,
			providers.WithResolvConf(cfg.SandboxResolvConf),
			providers.WithHealthCheckImage(cfg.SandboxHealthCheckImage),
			providers.WithRequireDiskQuota(cfg.SandboxRequireDiskQuota),
		)
		maxActiveSandboxes := resolveWorkerMaxActiveSandboxes(cfg.WorkerProcessCount, cfg.WorkerMaxActiveSandboxes)
		sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
			Counter:   sandboxExec,
			MaxActive: maxActiveSandboxes,
			NodeID:    cfg.NodeID,
			Logger:    logger,
		})
		oldShutdown := shutdown
		shutdown = func() {
			if err := apiDockerCli.Close(); err != nil {
				logger.Warn().Err(err).Msg("failed to close Docker client")
			}
			oldShutdown()
		}
	} else {
		logger.Warn().Err(dockerErr).Msg("Docker file reader and capacity gate disabled for session executor")
	}

	snapshotStore, _, err := storage.BuildSnapshotStore(ctx, storage.SnapshotStoreConfig{
		StorageDir:     cfg.SnapshotStorageDir,
		S3Bucket:       cfg.SnapshotS3Bucket,
		S3Prefix:       cfg.SnapshotS3Prefix,
		S3Region:       cfg.SnapshotS3Region,
		S3Endpoint:     cfg.SnapshotS3Endpoint,
		S3UsePathStyle: cfg.SnapshotS3UsePathStyle,
	})
	if err != nil {
		return nil, shutdown, fmt.Errorf("initialize snapshot store: %w", err)
	}

	orgStore := db.NewOrganizationStore(pool)
	issueStore := db.NewIssueStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)
	pullRequestStore := db.NewPullRequestStore(pool)
	deployStore := db.NewDeployStore(pool)
	priorityScoreStore := db.NewPriorityScoreStore(pool)
	complexityEstimateStore := db.NewComplexityEstimateStore(pool)
	pmPlanStore := db.NewPMPlanStore(pool)
	pmDecisionLogStore := db.NewPMDecisionLogStore(pool)
	projectStore := db.NewProjectStore(pool)
	projectTaskStore := db.NewProjectTaskStore(pool)
	projectCycleStore := db.NewProjectCycleStore(pool)
	pmDocumentStore := db.NewPMDocumentStore(pool)
	automationRunStore := db.NewAutomationRunStore(pool)
	evalBootstrapStore := db.NewEvalBootstrapStore(pool)
	orgSettingsCache := agent.NewOrgSettingsCache(agent.DefaultOrgSettingsCacheTTL)

	services := buildServices(
		cfg,
		pool,
		logger,
		codexAuthSvc,
		claudeCodeAuthSvc,
		credentialStore,
		userCredentialStore,
		codingCredentialStore,
		issueStore,
		sessionStore,
		jobStore,
		orgStore,
		repoStore,
		pullRequestStore,
		deployStore,
		priorityScoreStore,
		complexityEstimateStore,
		pmPlanStore,
		pmDecisionLogStore,
		projectStore,
		projectTaskStore,
		projectCycleStore,
		pmDocumentStore,
		integrationStore,
		sessionMessageStore,
		automationRunStore,
		evalBootstrapStore,
		snapshotStore,
		billingMetrics,
		agent.NewCancelRegistry(logger),
		agent.NewThreadCancelRegistry(logger),
		orgSettingsCache,
		sandboxCapacity,
		redisClient,
		sessionStreams,
		fileReader,
	)
	if services == nil {
		return nil, shutdown, fmt.Errorf("worker services unavailable")
	}

	stores := buildSessionExecutorStores(sessionExecutorStoreDeps{
		Pool:                pool,
		Issues:              issueStore,
		Sessions:            sessionStore,
		Jobs:                jobStore,
		Integrations:        integrationStore,
		PriorityScores:      priorityScoreStore,
		ComplexityEstimates: complexityEstimateStore,
		Projects:            projectStore,
		ProjectTasks:        projectTaskStore,
		Credentials:         credentialStore,
		Organizations:       orgStore,
		SessionLogs:         sessionLogStore,
		EvalBootstraps:      evalBootstrapStore,
		Repositories:        repoStore,
		SessionMessages:     sessionMessageStore,
		SessionThreads:      sessionThreadStore,
		AutomationRuns:      automationRunStore,
		PullRequests:        pullRequestStore,
	})
	if services.LinearAgentDeps != nil {
		services.LinearAgentDeps.Stores = stores
	}

	oldShutdown := shutdown
	shutdown = func() {
		if services.SandboxAuthShutdown != nil {
			services.SandboxAuthShutdown()
		}
		oldShutdown()
	}

	return &worker.SessionExecutorRuntime{
		Executors:         db.NewSessionExecutorStore(pool),
		Jobs:              jobStore,
		Stores:            stores,
		Services:          services,
		Logger:            logger,
		LeaseDuration:     60 * time.Second,
		HeartbeatInterval: 10 * time.Second,
		RenewInterval:     20 * time.Second,
	}, shutdown, nil
}

type sessionExecutorStoreDeps struct {
	Pool                *pgxpool.Pool
	Issues              *db.IssueStore
	Sessions            *db.SessionStore
	Jobs                *db.JobStore
	Integrations        *db.IntegrationStore
	PriorityScores      *db.PriorityScoreStore
	ComplexityEstimates *db.ComplexityEstimateStore
	Projects            *db.ProjectStore
	ProjectTasks        *db.ProjectTaskStore
	Credentials         *db.OrgCredentialStore
	Organizations       *db.OrganizationStore
	SessionLogs         *db.SessionLogStore
	EvalBootstraps      *db.EvalBootstrapStore
	Repositories        *db.RepositoryStore
	SessionMessages     *db.SessionMessageStore
	SessionThreads      *db.SessionThreadStore
	AutomationRuns      *db.AutomationRunStore
	PullRequests        *db.PullRequestStore
}

func buildSessionExecutorStores(deps sessionExecutorStoreDeps) *worker.Stores {
	pool := deps.Pool
	if deps.Issues == nil {
		deps.Issues = db.NewIssueStore(pool)
	}
	if deps.Sessions == nil {
		deps.Sessions = db.NewSessionStore(pool)
	}
	if deps.Jobs == nil {
		deps.Jobs = db.NewJobStore(pool)
	}
	if deps.Integrations == nil {
		deps.Integrations = db.NewIntegrationStore(pool)
	}
	if deps.PriorityScores == nil {
		deps.PriorityScores = db.NewPriorityScoreStore(pool)
	}
	if deps.ComplexityEstimates == nil {
		deps.ComplexityEstimates = db.NewComplexityEstimateStore(pool)
	}
	if deps.Projects == nil {
		deps.Projects = db.NewProjectStore(pool)
	}
	if deps.ProjectTasks == nil {
		deps.ProjectTasks = db.NewProjectTaskStore(pool)
	}
	if deps.Credentials == nil {
		deps.Credentials = db.NewOrgCredentialStore(pool, nil)
	}
	if deps.Organizations == nil {
		deps.Organizations = db.NewOrganizationStore(pool)
	}
	if deps.SessionLogs == nil {
		deps.SessionLogs = db.NewSessionLogStore(pool)
	}
	if deps.EvalBootstraps == nil {
		deps.EvalBootstraps = db.NewEvalBootstrapStore(pool)
	}
	if deps.Repositories == nil {
		deps.Repositories = db.NewRepositoryStore(pool)
	}
	if deps.SessionMessages == nil {
		deps.SessionMessages = db.NewSessionMessageStore(pool)
	}
	if deps.SessionThreads == nil {
		deps.SessionThreads = db.NewSessionThreadStore(pool)
	}
	if deps.AutomationRuns == nil {
		deps.AutomationRuns = db.NewAutomationRunStore(pool)
	}
	if deps.PullRequests == nil {
		deps.PullRequests = db.NewPullRequestStore(pool)
	}
	return &worker.Stores{
		Issues:              deps.Issues,
		Sessions:            deps.Sessions,
		Jobs:                deps.Jobs,
		Integrations:        deps.Integrations,
		Memberships:         db.NewOrganizationMembershipStore(pool),
		Webhooks:            db.NewWebhookDeliveryStore(pool),
		PriorityScores:      deps.PriorityScores,
		ComplexityEstimates: deps.ComplexityEstimates,
		Projects:            deps.Projects,
		ProjectTasks:        deps.ProjectTasks,
		Credentials:         deps.Credentials,
		AuditLogs:           db.NewAuditLogStore(pool),
		Organizations:       deps.Organizations,
		Users:               db.NewUserStore(pool),
		SessionLogs:         deps.SessionLogs,
		EvalTasks:           db.NewEvalTaskStore(pool),
		EvalRuns:            db.NewEvalRunStore(pool),
		EvalBatches:         db.NewEvalBatchStore(pool),
		EvalBootstraps:      deps.EvalBootstraps,
		EvalReleaseGates:    db.NewEvalReleaseGateStore(pool),
		Repositories:        deps.Repositories,
		GitHubInstallations: db.NewGitHubInstallationStore(pool),
		SessionMessages:     deps.SessionMessages,
		SessionThreads:      deps.SessionThreads,
		HumanInputRequests:  db.NewSessionHumanInputRequestStore(pool),
		ThreadFileEvents:    db.NewSessionThreadFileEventStore(pool),
		SandboxHolders:      db.NewSessionSandboxHolderStore(pool),
		Automations:         db.NewAutomationStore(pool),
		AutomationRuns:      deps.AutomationRuns,
		ReviewLoops:         db.NewSessionReviewLoopStore(pool),
		PRReadiness:         db.NewPRReadinessStore(pool),
		SessionIssueLinks:   db.NewSessionIssueLinkStore(pool),
		Previews:            db.NewPreviewStore(pool),
		PullRequests:        deps.PullRequests,
		SlackInstallations:  db.NewSlackInstallationStore(pool),
		SlackOrgSelections:  db.NewSlackOrgSelectionStore(pool),
		SlackBotSettings:    db.NewSlackBotSettingsStore(pool),
		SlackUserLinks:      db.NewSlackUserLinkStore(pool),
		SlackChannels:       db.NewSlackChannelSettingsStore(pool),
		SlackSessionLinks:   db.NewSlackSessionLinkStore(pool),
		SlackInboundEvents:  db.NewSlackInboundEventStore(pool),
		SlackOutbound:       db.NewSlackOutboundMessageStore(pool),
		SessionAttributions: db.NewSessionAttributionStore(pool),
	}
}
