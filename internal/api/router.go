package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/gateway"
	"github.com/assembledhq/143/internal/api/handlers"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/assembledhq/143/internal/services/email"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	threadservice "github.com/assembledhq/143/internal/services/thread"
	"github.com/assembledhq/143/internal/services/workspace"
)

func NewRouter(cfg *config.Config, pool *pgxpool.Pool, logger zerolog.Logger, sentryReporter observability.Reporter, codexAuthSvc *codexauth.Service, claudeCodeAuthSvc *claudecodeauth.Service, llmClient llm.Client, fileReader sandbox.FileReader, canceller handlers.SessionCanceller, threadCanceller *agent.ThreadCancelRegistry, previewProvider preview.PreviewCapableProvider, snapshotExecutor preview.SnapshotExecutor, sandboxProvider agent.SandboxProvider, snapshotStore storage.SnapshotStore, orgSettingsInvalidator handlers.OrgSettingsInvalidator, shutdownCh <-chan struct{}, redisClient *cache.Client, sessionStreams *cache.SessionStreams, sharedCodingCredentialStore ...*db.CodingCredentialStore) (*chi.Mux, *http.Server, *preview.RecycleWorker, io.Closer, *preview.Manager, error) {
	// Create stores
	orgStore := db.NewOrganizationStore(pool)
	userStore := db.NewUserStore(pool)
	authSessionStore := db.NewAuthSessionStore(pool)
	membershipStore := db.NewOrganizationMembershipStore(pool)
	repoStore := db.NewRepositoryStore(pool)
	integrationStore := db.NewIntegrationStore(pool)
	issueStore := db.NewIssueStore(pool)
	sessionStore := db.NewSessionStore(pool)
	sessionIssueLinkStore := db.NewSessionIssueLinkStore(pool)
	sessionIssueSnapshotStore := db.NewSessionTurnIssueSnapshotStore(pool)
	sessionLogStore := db.NewSessionLogStore(pool)
	sessionQuestionStore := db.NewSessionQuestionStore(pool)
	validationStore := db.NewValidationStore(pool)
	pullRequestStore := db.NewPullRequestStore(pool)
	webhookDeliveryStore := db.NewWebhookDeliveryStore(pool)
	jobStore := db.NewJobStore(pool)
	sessionStore.SetLogger(logger)
	sessionLogStore.SetLogger(logger)
	jobStore.SetLogger(logger)
	if sessionStreams != nil {
		sessionStore.SetStreams(sessionStreams)
		sessionLogStore.SetStreams(sessionStreams)
	}
	if redisClient != nil {
		jobStore.SetNotifier(cache.NewJobNotifier(redisClient, logger))
	}
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
	evalTaskStore := db.NewEvalTaskStore(pool)
	evalRunStore := db.NewEvalRunStore(pool)
	evalBatchStore := db.NewEvalBatchStore(pool)
	evalBootstrapStore := db.NewEvalBootstrapStore(pool)
	sessionReviewCommentStore := db.NewSessionReviewCommentStore(pool)
	previewStore := db.NewPreviewStore(pool)
	nodeStore := db.NewNodeStore(pool)
	auditLogStore := db.NewAuditLogStore(pool)
	auditEmitter := db.NewAuditEmitter(auditLogStore, logger)

	// Create credential store with optional encryption.
	var cryptoSvc *crypto.Service
	if cfg.EncryptionMasterKey != "" {
		var err error
		cryptoSvc, err = crypto.NewService(cfg.EncryptionMasterKey)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
	}
	credentialStore := db.NewOrgCredentialStore(pool, cryptoSvc)
	userCredentialStore := db.NewUserCredentialStore(pool, cryptoSvc)
	codingCredentialStore := resolveRouterCodingCredentialStore(pool, cryptoSvc, sharedCodingCredentialStore...)
	// Mirror legacy writes into the unified `coding_credentials` table during the
	// migration window. Removed in the cleanup PR. See
	// docs/design/future/65-unified-coding-credentials.md.
	credentialStore.SetCodingMirror(codingCredentialStore)
	userCredentialStore.SetCodingMirror(codingCredentialStore)
	mirrorLog := func(format string, args ...any) {
		logger.Warn().Msgf(format, args...)
	}
	credentialStore.SetMirrorLogger(mirrorLog)
	userCredentialStore.SetMirrorLogger(mirrorLog)
	codingCredentialStore.SetMirrorLogger(mirrorLog)

	// Create services
	ingestionSvc := ingestion.NewService(issueStore, webhookDeliveryStore, jobStore, logger)

	// Create PRService if GitHub App credentials are configured.
	var prService *ghservice.PRService
	var appUserAuthSvc *ghservice.AppUserAuthService
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPrivateKey != "" {
		ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
		if err != nil {
			logger.Warn().Err(err).Msg("failed to initialize GitHub App service, PR webhooks will be disabled")
		} else {
			prService = ghservice.NewPRService(
				ghSvc, pullRequestStore, sessionStore, issueStore,
				deployStore, validationStore, repoStore, jobStore, logger,
			)
			prService.SetAppBaseURL(cfg.FrontendURL)
			prService.SetReviewCommentStore(reviewCommentStore)
			prService.SetIntegrationStore(integrationStore)
			prService.SetSandboxPushDeps(sandboxProvider, snapshotStore)
			// Linear milestone enqueuer fires post-PR-event Linear writes.
			prService.SetLinearMilestoneEnqueuer(linear.MilestoneEnqueuerFor(jobStore, logger))
		}
	}
	if cfg.GitHubAppClientID != "" && cfg.GitHubAppClientSecret != "" {
		appUserAuthSvc = ghservice.NewAppUserAuthService(userCredentialStore, cfg.GitHubAppClientID, cfg.GitHubAppClientSecret, cfg.BaseURL, logger)
	}

	// Create handlers
	healthHandler := handlers.NewHealthHandler(pool)
	if redisClient != nil {
		healthHandler.SetRedisHealthCheck(redisClient.Healthy)
	}
	authHandler := handlers.NewAuthHandler(cfg, pool, userStore, authSessionStore, invitationStore, membershipStore)
	organizationsHandler := handlers.NewOrganizationsHandler(pool)
	repoHandler := handlers.NewRepositoryHandler(repoStore)
	if prService != nil {
		repoHandler.SetPRService(prService)
	}
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
	integrationOpts = append(integrationOpts, handlers.WithPMContextAutoTrigger(jobStore, pmDocumentStore, logger))
	integrationHandler := handlers.NewIntegrationHandler(
		integrationStore,
		credentialStore,
		cfg.LinearOAuthClientID,
		cfg.LinearOAuthClientSecret,
		cfg.BaseURL,
		cfg.FrontendURL,
		integrationOpts...,
	)
	integrationHandler.SetLinearJobStore(jobStore)
	webhookHandler := handlers.NewWebhookHandler(cfg, orgStore, userStore, repoStore, integrationStore, prService)
	containerUsageStore := db.NewContainerUsageStore(pool)
	usageRollupStore := db.NewUsageRollupStore(pool)
	usageHandler := handlers.NewUsageHandler(
		containerUsageStore,
		handlers.WithRollupStore(usageRollupStore),
		handlers.WithMembershipStore(membershipStore),
	)
	settingsHandler := handlers.NewSettingsHandler(orgStore, cfg.SafeLLMEnv())
	issueHandler := handlers.NewIssueHandler(issueStore)
	sessionMessageStore := db.NewSessionMessageStore(pool)
	sessionThreadStore := db.NewSessionThreadStore(pool)
	sessionThreadFileEventStore := db.NewSessionThreadFileEventStore(pool)
	sessionViewStore := db.NewSessionViewStore(pool)
	sessionComposerHandler := handlers.NewSessionComposerHandler(repoStore, prService)
	pullRequestHandler := handlers.NewPullRequestHandler(prService)
	prHealthStreams := cache.NewPullRequestStreams(redisClient, logger)
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
	sessionHandler.SetViewStore(sessionViewStore)
	sessionHandler.SetIssueLinkStore(sessionIssueLinkStore)
	sessionHandler.SetIssueSnapshotStore(sessionIssueSnapshotStore)
	sessionHandler.SetReviewCommentStore(sessionReviewCommentStore)

	// Linear session-linking: detection, primary resolution + context
	// snapshotting, attachment/comment writes, state-sync transitions —
	// see design 62. Wired here so it's available to CreateManual; the
	// worker gets its own instance via buildServices in cmd/server/main.go.
	// Both call linear.Build so wiring stays in lockstep.
	linearService := linear.Build(linear.BuildDeps{
		Pool:               pool,
		Logger:             logger,
		Integrations:       integrationStore,
		IntegrationsWriter: integrationStore,
		Credentials:        credentialStore,
		CredentialsWriter:  credentialStore,
		Issues:             issueStore,
		Sessions:           sessionStore,
		IssueLinks:         sessionIssueLinkStore,
		Orgs:               orgStore,
		Jobs:               jobStore,
		OAuthClient: linear.OAuthClientCreds{
			ClientID:     cfg.LinearOAuthClientID,
			ClientSecret: cfg.LinearOAuthClientSecret,
		},
		AppBaseURL: cfg.FrontendURL,
	})
	if sessionStreams != nil {
		// Republish session status on every link change so the detail view
		// re-fetches the enriched LinkedIssues without a manual reload.
		linearService.SetLinksChangedNotifier(func(ctx context.Context, orgID, sessionID uuid.UUID, _ string) {
			session, err := sessionStore.GetByID(ctx, orgID, sessionID)
			if err != nil {
				return
			}
			_ = sessionStreams.PublishStatus(ctx, &session)
		})
	}
	sessionHandler.SetLinearLinker(linearService)
	// Wire the inline team-key refresh hook so the Linear OAuth callback
	// can populate the allowlist synchronously before falling back to the
	// worker enqueue. See HandleLinearOAuthCallback for the two-tier
	// strategy.
	integrationHandler.SetLinearTeamKeyRefresher(linearService.RefreshTeamKeys)
	// Drop the in-process team-key cache as soon as the integration is
	// disconnected so post-disconnect session creates can't admit
	// bare-identifier matches via a stale cache.
	integrationHandler.SetLinearTeamKeyCacheInvalidator(linearService.InvalidateTeamKeyCache)
	sessionHandler.SetShutdownSignal(shutdownCh)
	sessionHandler.SetSnapshotStore(snapshotStore)
	sessionHandler.SetPRCredentialStore(userCredentialStore)
	sessionHandler.SetPRAuthCredentialChecker(appUserAuthSvc)
	sessionHandler.SetPRAuthFlow(cfg.CSRFSigningKey, cfg.FrontendURL)
	sessionHandler.SetStreams(sessionStreams)
	sessionHandler.SetMembershipStore(membershipStore)
	pullRequestHandler.SetStreams(prHealthStreams)
	pullRequestHandler.SetMembershipStore(membershipStore)
	if prService != nil {
		sessionHandler.SetPRTitleSyncer(prService)
	}
	threadSvc := threadservice.NewService(
		sessionThreadStore,
		sessionStore,
		sessionMessageStore,
		sessionLogStore,
		jobStore,
		logger,
	)
	threadSvc.SetFileEventStore(sessionThreadFileEventStore)
	if threadCanceller != nil {
		threadSvc.SetCanceller(threadCanceller)
	}
	// Wire review-comment resolution so SendThreadMessage can resolve
	// comments atomically with the message create — same invariant the
	// session-level SendMessage already enforces. Without this, the
	// "submitted comments stick around for the next turn" bug surfaces on
	// any session whose follow-up flows through a thread tab.
	threadSvc.SetReviewCommentResolver(pool, sessionReviewCommentStore)
	// Wire the question store so a follow-up sent to an awaiting_input
	// session via a thread tab flips the latest pending question to
	// 'answered' atomically with the message create — same invariant the
	// session-level handler maintains. Without this, question state
	// diverges from the resumed run on the thread surface.
	threadSvc.SetQuestionStore(sessionQuestionStore)
	sessionThreadHandler := handlers.NewSessionThreadHandler(threadSvc)
	sessionThreadHandler.SetAuditEmitter(auditEmitter)
	sessionThreadHandler.SetLogger(logger)
	// Mirror sessionHandler.SetLinearLinker so Linear refs typed into a
	// thread tab follow-up get the same fail-soft mid-session linking the
	// legacy session surface already provides.
	sessionThreadHandler.SetLinearLinker(linearService)
	pmHandler := handlers.NewPMHandler(pmPlanStore, pmDecisionLogStore, jobStore, orgStore)
	priorityHandler := handlers.NewPriorityHandler(priorityScoreStore, complexityEstimateStore, jobStore)
	ingestionWebhookHandler := handlers.NewIngestionWebhookHandler(webhookDeliveryStore, integrationStore, credentialStore, ingestionSvc, logger)
	credentialHandler := handlers.NewCredentialHandler(credentialStore)
	credentialHandler.SetSelfHeal(orgStore, cfg.SafeLLMEnv())
	memoryHandler := handlers.NewMemoryHandler(memoryStore, reviewCommentStore)
	userCredentialHandler := handlers.NewUserCredentialHandler(userCredentialStore, credentialStore, userStore)
	codingAuthHandler := handlers.NewCodingAuthHandler(credentialStore, orgStore)
	// Unified coding-credentials handler — see docs/design/future/65-unified-coding-credentials.md.
	codingCredentialHandler := handlers.NewCodingCredentialHandler(codingCredentialStore, orgStore)
	var emailSender email.Sender
	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" {
		emailSender = email.NewSMTPSender(email.SMTPConfig{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
		})
		logger.Info().Str("smtp_host", cfg.SMTPHost).Msg("SMTP email sender configured")
	}
	teamHandler := handlers.NewTeamHandler(userStore, membershipStore, authSessionStore, invitationStore, orgStore, cfg.FrontendURL, emailSender)
	teamHandler.SetRepositoryStore(repoStore)
	if cfg.GitHubAppID != 0 && cfg.GitHubAppPrivateKey != "" {
		ghSvc, err := ghservice.NewService(cfg.GitHubAppID, cfg.GitHubAppPrivateKey)
		if err == nil {
			teamHandler.SetGitHubIntegration(integrationStore, ghSvc)
		}
	}

	projectHandler := handlers.NewProjectHandler(projectStore, projectTaskStore, projectCycleStore, projectAttachmentStore, projectSpecStore)
	projectHandler.SetJobStore(jobStore)
	projectHandler.SetRepositoryStore(repoStore)

	automationStore := db.NewAutomationStore(pool)
	automationRunStore := db.NewAutomationRunStore(pool)
	automationHandler := handlers.NewAutomationHandler(automationStore, automationRunStore)
	automationHandler.SetJobStore(jobStore)
	automationHandler.SetRepositoryStore(repoStore)
	automationHandler.SetOrgStore(orgStore)
	automationHandler.SetOrgCredentialStore(credentialStore)
	automationHandler.SetUserCredentialStore(userCredentialStore)
	automationHandler.SetCodingAuthStore(credentialStore)
	automationHandler.SetCodingCredentialStore(codingCredentialStore)
	automationHandler.SetPool(pool)
	automationHandler.SetLogger(logger)

	prTemplateStore := db.NewPRTemplateStore(pool)
	githubStatusHandler := handlers.NewGitHubStatusHandler(
		userCredentialStore, orgStore,
		cfg.GitHubAppClientID, cfg.GitHubAppClientSecret,
		cfg.BaseURL, cfg.FrontendURL,
	)
	githubStatusHandler.SetPRAuthFlow(cfg.CSRFSigningKey)
	githubStatusHandler.SetAppUserAuth(appUserAuthSvc)

	// Wire user credential store and LLM client into PR service.
	if prService != nil {
		prService.SetUserCredentialStore(userCredentialStore)
		prService.SetSessionMessageStore(sessionMessageStore)
		prService.SetAppUserAuth(appUserAuthSvc)
		prService.SetUserStore(userStore)
		prService.SetOrgStore(orgStore)
		prService.SetLLMClient(llmClient)
		prService.SetPRTemplateStore(prTemplateStore)
		prService.SetAuditEmitter(auditEmitter)
		prService.SetPullRequestStreams(prHealthStreams)
	}

	// Wire user credential store into auth handler for token storage on login.
	authHandler.SetUserCredentialStore(userCredentialStore)

	// Wire audit emitter into all handlers that perform state changes.
	authHandler.SetAuditEmitter(auditEmitter)
	organizationsHandler.SetAuditEmitter(auditEmitter)
	sessionHandler.SetAuditEmitter(auditEmitter)
	if canceller != nil {
		sessionHandler.SetCanceller(canceller)
	}
	teamHandler.SetAuditEmitter(auditEmitter)
	settingsHandler.SetAuditEmitter(auditEmitter)
	settingsHandler.SetLogger(logger)
	if orgSettingsInvalidator != nil {
		settingsHandler.SetOrgSettingsInvalidator(orgSettingsInvalidator)
		codingAuthHandler.SetOrgSettingsInvalidator(orgSettingsInvalidator)
		codingCredentialHandler.SetOrgSettingsInvalidator(orgSettingsInvalidator)
	}
	credentialHandler.SetAuditEmitter(auditEmitter)
	projectHandler.SetAuditEmitter(auditEmitter)
	automationHandler.SetAuditEmitter(auditEmitter)
	pmHandler.SetAuditEmitter(auditEmitter)
	pmHandler.SetPMDocumentStore(pmDocumentStore)
	projectAttachmentHandler := handlers.NewProjectAttachmentHandler(projectAttachmentStore, projectStore)
	projectSpecHandler := handlers.NewProjectSpecHandler(projectSpecStore, projectStore)
	projectAnalysisHandler := handlers.NewProjectAnalysisHandler(projectStore, projectSpecStore, projectAttachmentStore, projectTaskStore)
	projectGenerateHandler := handlers.NewProjectGenerateHandler(llmClient)
	codexAuthHandler := handlers.NewCodexAuthHandler(codexAuthSvc, logger)
	claudeCodeAuthHandler := handlers.NewClaudeCodeAuthHandler(claudeCodeAuthSvc, logger)
	pmDocumentHandler := handlers.NewPMDocumentHandler(pmDocumentStore, credentialStore)
	pmDocumentHandler.SetAuditEmitter(auditEmitter)
	evalHandler := handlers.NewEvalHandler(evalTaskStore, evalRunStore, evalBatchStore, evalBootstrapStore, jobStore, pool)
	evalHandler.SetAuditEmitter(auditEmitter)
	// Redis-backed pub/sub for the eval batch + bootstrap detail SSEs. nil
	// when redisClient is nil — handlers will return 503 and the frontend
	// will continue to fall back to its existing polling path.
	evalBatchStreams := cache.NewEvalBatchStreams(redisClient, logger)
	evalBootstrapStreams := cache.NewEvalBootstrapStreams(redisClient, logger)
	evalHandler.SetBatchStreams(evalBatchStreams)
	evalHandler.SetBootstrapStreams(evalBootstrapStreams)
	evalHandler.SetMembershipStore(membershipStore)
	auditLogHandler := handlers.NewAuditLogHandler(auditLogStore)
	sessionReviewCommentHandler := handlers.NewSessionReviewCommentHandler(sessionReviewCommentStore, sessionStore, logger)
	sessionReviewCommentHandler.SetAuditEmitter(auditEmitter)
	sessionReviewCommentHandler.SetMessageAndJobStores(sessionMessageStore, sessionThreadStore, jobStore)
	// Initialize the session-files snapshot cache so that file-context reads
	// keep working after the live container is torn down. The cache is
	// best-effort: a build error here is logged and the handler falls back
	// to its pre-Phase-6 behavior (NO_SANDBOX once the container is gone).
	var sessionFilesSnapshotCache *workspace.SnapshotCache
	if snapshotStore != nil && cfg.SessionFilesCacheDir != "" {
		sc, scErr := workspace.NewSnapshotCache(snapshotStore, cfg.SessionFilesCacheDir, cfg.SessionFilesCacheMaxBytes, logger)
		if scErr != nil {
			logger.Warn().Err(scErr).Str("cache_dir", cfg.SessionFilesCacheDir).Msg("failed to initialize session-files snapshot cache — snapshot fallback disabled")
		} else {
			sessionFilesSnapshotCache = sc
		}
	}
	sessionFileHandler := handlers.NewSessionFileHandler(sessionStore, repoStore, fileReader, sessionFilesSnapshotCache, logger)

	// Preview system: inspector, snapshot cache, HMR watcher, manager, recycler, gateway.
	var previewInspector preview.PreviewInspector
	var inspectorCloser io.Closer // returned for graceful shutdown
	if (cfg.Mode == "worker" || cfg.Mode == "all") && cfg.ChromeWSURL != "" {
		// Convert env-friendly "{id}" placeholder to chromedp inspector's "{{.PreviewID}}" format.
		inspectorURLTemplate := strings.Replace(cfg.PreviewOriginTemplate, "{id}", "{{.PreviewID}}", 1) + "{{.Path}}"
		cdpInspector := preview.NewChromeDPInspector(preview.ChromeDPInspectorConfig{
			RemoteURL:          cfg.ChromeWSURL,
			PreviewURLTemplate: inspectorURLTemplate,
		}, logger)
		previewInspector = cdpInspector
		inspectorCloser = cdpInspector
	}

	var previewSnapshotCache *preview.SnapshotCache
	if (cfg.Mode == "worker" || cfg.Mode == "all") && cfg.PreviewSnapshotCacheDir != "" && snapshotExecutor != nil {
		var scErr error
		previewSnapshotCache, scErr = preview.NewSnapshotCache(preview.SnapshotCacheConfig{
			Store:        previewStore,
			Executor:     snapshotExecutor,
			Logger:       logger,
			WorkerNodeID: cfg.NodeID,
			CacheDir:     cfg.PreviewSnapshotCacheDir,
		})
		if scErr != nil {
			logger.Warn().Err(scErr).Msg("failed to initialize preview snapshot cache — caching disabled")
			previewSnapshotCache = nil
		}
	}

	var hmrWatcher *preview.HMRWatcher
	if previewInspector != nil && (cfg.Mode == "worker" || cfg.Mode == "all") && cfg.PreviewHMRBlobDir != "" {
		var hmrErr error
		hmrWatcher, hmrErr = preview.NewHMRWatcher(preview.HMRWatcherConfig{
			Inspector: previewInspector,
			Store:     previewStore,
			Logger:    logger,
			BlobDir:   cfg.PreviewHMRBlobDir,
		})
		if hmrErr != nil {
			logger.Warn().Err(hmrErr).Msg("failed to initialize HMR watcher — auto-screenshot disabled")
			hmrWatcher = nil
		}
	}

	previewManager := preview.NewManager(preview.ManagerConfig{
		Store:                 previewStore,
		SessionStore:          sessionStore,
		Provider:              previewProvider,
		SandboxProvider:       sandboxProvider,
		Inspector:             previewInspector,
		SnapshotCache:         previewSnapshotCache,
		HMRWatcher:            hmrWatcher,
		Logger:                logger,
		WorkerNodeID:          cfg.NodeID,
		PreviewOriginTemplate: cfg.PreviewOriginTemplate,
		MaxPerUser:            cfg.PreviewMaxPerUser,
		MaxPerOrg:             cfg.PreviewMaxPerOrg,
		MaxPerWorker:          cfg.PreviewMaxPerWorker,
	})

	recycleWorker := preview.NewRecycleWorker(preview.RecycleWorkerConfig{
		Manager: previewManager,
		Logger:  logger,
	})
	if cfg.Mode == "worker" || cfg.Mode == "all" {
		recycleWorker.Start()
		cleanupWorker := preview.NewCleanupWorker(preview.CleanupWorkerConfig{
			Manager: previewManager,
			Logger:  logger,
		})
		cleanupWorker.Start()
	} else {
		recycleWorker = nil
	}

	workerSelector := preview.NewWorkerSelector(nodeStore, previewStore)
	workerClient := preview.NewWorkerPreviewClient(cfg.SessionSecret)

	// Preview gateway (separate HTTP listener for <id>.preview.* origins).
	// gwSrv is stored so callers can shut it down gracefully.
	var gwSrv *http.Server
	if cfg.PreviewGatewayPort > 0 && cfg.Mode != "worker" {
		gw := gateway.NewGateway(gateway.GatewayConfig{
			Store:                 previewStore,
			Manager:               previewManager,
			WorkerSelector:        workerSelector,
			HMRWatcher:            hmrWatcher,
			Logger:                logger,
			AppOrigin:             cfg.FrontendURL,
			CookieSecret:          []byte(cfg.SessionSecret),
			PreviewTokenSecret:    cfg.SessionSecret,
			PreviewOriginTemplate: cfg.PreviewOriginTemplate,
		})
		addr := fmt.Sprintf(":%d", cfg.PreviewGatewayPort)
		logger.Info().Str("addr", addr).Msg("starting preview gateway")
		gwSrv = &http.Server{Addr: addr, Handler: gw, ReadHeaderTimeout: 10 * time.Second}
		// Use a listener to detect port-in-use errors synchronously before
		// returning from NewRouter, rather than silently failing in a goroutine.
		gwListener, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("preview gateway listen on %s: %w", addr, listenErr)
		}
		go func() {
			if gwErr := gwSrv.Serve(gwListener); gwErr != nil && gwErr != http.ErrServerClosed {
				logger.Error().Err(gwErr).Msg("preview gateway failed")
			}
		}()
	}

	previewHandler := handlers.NewPreviewHandler(previewManager, previewStore, sessionStore, repoStore, fileReader, sandboxProvider, snapshotStore, logger)
	previewHandler.SetAuditEmitter(auditEmitter)
	previewHandler.SetWorkerRuntime(workerSelector, workerClient, cfg.NodeID)
	internalPreviewHandler := handlers.NewInternalPreviewHandler(previewHandler, previewManager, cfg.NodeID, cfg.SessionSecret, logger)
	previewStopper := preview.NewWorkerStopper(previewStore, workerSelector, workerClient, cfg.NodeID, previewManager)
	if prService != nil {
		prService.SetPreviewTeardown(previewStore, previewStopper)
	}

	// Upload store: use S3 if configured, otherwise fall back to local filesystem.
	var uploadStore storage.UploadStore
	if cfg.UploadS3Bucket != "" {
		awsCfg, awsErr := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(cfg.UploadS3Region),
		)
		if awsErr != nil {
			logger.Warn().Err(awsErr).Msg("failed to load AWS config for upload S3 — falling back to file uploads")
			uploadStore = storage.NewFileUploadStore(cfg.UploadStorageDir, "/api/v1/uploads/files")
		} else {
			s3Client := s3.NewFromConfig(awsCfg)
			uploadStore = storage.NewS3UploadStore(s3Client, cfg.UploadS3Bucket, cfg.UploadS3Prefix)
			logger.Info().Str("bucket", cfg.UploadS3Bucket).Str("prefix", cfg.UploadS3Prefix).Msg("upload S3 store configured")
		}
	} else {
		uploadStore = storage.NewFileUploadStore(cfg.UploadStorageDir, "/api/v1/uploads/files")
	}
	uploadHandler := handlers.NewUploadHandler(uploadStore)
	uploadHandler.SetMembershipStore(membershipStore)

	r := chi.NewRouter()

	// Common middleware shared by both public/API routes and worker preview RPC.
	r.Use(chiMiddleware.RequestID)
	r.Use(middleware.Logging(logger, sentryReporter))
	r.Use(middleware.Recoverer(logger, sentryReporter))
	r.Use(middleware.CORS(cfg.CORSAllowedOrigins))
	r.Use(middleware.Metrics)

	// Public routes (no auth)
	r.Get("/healthz", healthHandler.Healthz)
	r.Get("/readyz", healthHandler.Readyz)
	r.Handle("/metrics", promhttp.Handler())
	if cfg.Mode == "worker" || cfg.Mode == "all" {
		r.Post("/internal/preview/start", internalPreviewHandler.StartPreview)
		r.Post("/internal/preview/stop-session", internalPreviewHandler.StopActivePreviewForSession)
		r.Post("/internal/preview/{previewID}/stop", internalPreviewHandler.StopPreview)
		r.Post("/internal/preview/{previewID}/recycle", internalPreviewHandler.RecyclePreview)
		r.Post("/internal/preview/{previewID}/screenshot", internalPreviewHandler.CaptureScreenshot)
		r.Post("/internal/preview/{previewID}/inspect", internalPreviewHandler.InspectElement)
		r.Get("/internal/preview/{previewID}/console", internalPreviewHandler.ReadConsole)
		r.Post("/internal/preview/{previewID}/interact", internalPreviewHandler.ExecuteInteraction)
		r.Post("/internal/preview/{previewID}/multi-viewport", internalPreviewHandler.CaptureMultiViewport)
		r.Post("/internal/preview/{previewID}/visual-diff", internalPreviewHandler.ComputeVisualDiff)
		r.Post("/internal/preview/{previewID}/assert", internalPreviewHandler.RunAssertions)
		r.HandleFunc("/internal/preview/{previewID}/proxy", internalPreviewHandler.Proxy)
		r.HandleFunc("/internal/preview/{previewID}/proxy/*", internalPreviewHandler.Proxy)
	}

	apiRoutes := chi.NewRouter()
	apiRoutes.Use(middleware.MaxBodySize(1 << 20)) // 1MB request body limit
	apiRoutes.Use(middleware.RateLimit(middleware.DefaultRateLimitConfig()))

	apiRoutes.Group(func(r chi.Router) {
		// Webhook routes (no auth — called by external services, signature verified per-provider)
		r.Route("/api/v1/webhooks", func(r chi.Router) {
			r.Post("/github", webhookHandler.HandleGitHub)
			r.Post("/sentry", ingestionWebhookHandler.HandleSentry)
			r.Post("/linear", ingestionWebhookHandler.HandleLinear)
		})

		// Internal API routes (token-based auth — called by sandbox agents)
		internalIssueHandler := handlers.NewInternalIssueHandler(issueStore, sessionStore, jobStore, orgStore, cfg.SessionSecret, logger)
		internalProjectHandler := handlers.NewInternalProjectHandler(pool, projectStore, projectTaskStore, repoStore, cfg.SessionSecret, logger)
		r.Route("/api/v1/internal", func(r chi.Router) {
			r.Post("/issues", internalIssueHandler.Create)
			r.Post("/projects/propose", internalProjectHandler.Propose)
		})

		// Public team routes (token-based, no auth). AcceptInvitation looks the
		// token up server-side, so it's the same brute-force shape as the
		// authenticated ClaimInvitation endpoint: rate-limit it on the same
		// 10/min-per-IP budget. ClaimRateLimit's user-bucket branch is skipped
		// for anonymous callers (no user in context), so it degrades to the IP
		// bucket only — exactly the guarantee this public route needs.
		r.With(middleware.ClaimRateLimit(10)).Post("/api/v1/team/invitations/accept", teamHandler.AcceptInvitation)

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
			r.Use(middleware.Auth(middleware.AuthStores{
				Sessions:    authSessionStore,
				Users:       userStore,
				Memberships: membershipStore,
			}, []byte(cfg.CSRFSigningKey), logger))
			r.Use(middleware.LogContext(logger))
			r.Use(middleware.CSRF(cfg.CSRFSigningKey, logger))

			// Zero-membership-safe endpoints: a user whose only membership was
			// just revoked still needs to load /auth/me to see the empty state,
			// Logout to drop the session, and POST /invitations/claim to redeem
			// a fresh invite. These routes intentionally do NOT go through
			// OrgContext (which 403s on uuid.Nil) or RequireRole (which 403s on
			// empty active role).
			r.Get("/api/v1/auth/me", authHandler.Me)
			r.Patch("/api/v1/auth/me/settings", authHandler.UpdateSettings)
			// Memberships is zero-membership-safe for the same reason /auth/me
			// is: a user whose only org was just revoked still needs to see the
			// empty list so the switcher can render an invite-me state rather
			// than a 403 spinner.
			r.Get("/api/v1/auth/memberships", authHandler.Memberships)
			r.Post("/api/v1/auth/active-org", authHandler.SetActiveOrg)
			r.Post("/api/v1/auth/logout", authHandler.Logout)
			// Available to any authenticated user (no RequireRole) — an invited
			// user may not yet have a role in the target org when they claim.
			// Rate-limited per-IP and per-user at 10/minute: the endpoint is a
			// natural brute-force target because each request names an opaque
			// token the server looks up, so tightening beyond the default 20 rps
			// IP limit forces any enumeration attempt into detectable territory.
			r.With(middleware.ClaimRateLimit(10)).Post("/api/v1/invitations/claim", authHandler.ClaimInvitation)

			// In-app pending-invitation surface (org switcher dot + dropdown
			// section). Sits alongside /invitations/claim outside the
			// OrgContext block because invitations span organizations and
			// matter most to users with no usable active org (e.g. a fresh
			// signup or someone removed from their last org). The mutation
			// routes share the claim endpoint's 10/min ClaimRateLimit since
			// they are equivalent brute-force surfaces — the URL names a
			// specific invitation row that the server validates against the
			// session's email/github_login.
			r.Get("/api/v1/invitations/pending", authHandler.ListPendingInvitations)
			r.With(middleware.ClaimRateLimit(10)).Post("/api/v1/invitations/{id}/accept", authHandler.AcceptInvitationByID)
			r.With(middleware.ClaimRateLimit(10)).Post("/api/v1/invitations/{id}/decline", authHandler.DeclineInvitationByID)

			// Creating a new org is zero-membership-safe for the same reason the
			// other routes in this block are: a user whose only membership was
			// just revoked must be able to create a fresh org to recover, and
			// OrgContext would 403 them before they could. Rate-limited 5/hour
			// per user and per IP to cap spam: a human creates maybe one org per
			// onboarding, so a bucket any larger is just room for scripted abuse.
			// The prune goroutine inside the limiter runs for the life of the
			// process; context.Background() is the right lifetime here.
			r.With(middleware.CreateOrgRateLimit(context.Background(), 5)).Post("/api/v1/organizations", organizationsHandler.Create)

			// Read-only routes (all roles: admin, member, viewer)
			r.Group(func(r chi.Router) {
				r.Use(middleware.OrgContext)
				r.Use(middleware.RequireRole("admin", "member", "viewer"))

				r.Get("/api/v1/version", healthHandler.Version)

				// GitHub connection status for PR authorship
				r.Get("/api/v1/users/me/github-status", githubStatusHandler.GetStatus)

				// Personal and resolved credential views
				r.Get("/api/v1/settings/credentials/personal", userCredentialHandler.ListPersonal)
				r.Get("/api/v1/settings/credentials/resolved", userCredentialHandler.ListResolved)
				r.Get("/api/v1/settings/credentials/team", userCredentialHandler.ListTeamDefaults)
				// Unified coding-credentials reads are safe for every org role:
				// personal/resolved reads are scoped to the caller, and org rows
				// are the same read-only fallback metadata already shown on
				// settings pages.
				r.Get("/api/v1/coding-credentials", codingCredentialHandler.List)

				r.Get("/api/v1/repositories", repoHandler.List)
				r.Get("/api/v1/repositories/summary", repoHandler.Summary)
				r.Get("/api/v1/repositories/{id}", repoHandler.Get)
				r.Get("/api/v1/repositories/{id}/branches", repoHandler.ListBranches)
				r.Get("/api/v1/session-composer/files", sessionComposerHandler.ListFileMentions)
				r.Get("/api/v1/session-composer/slash-commands", sessionComposerHandler.ListSlashCommands)
				r.Get("/api/v1/session-composer/slash-commands/details", sessionComposerHandler.GetSlashCommandDetail)
				r.Get("/api/v1/integrations", integrationHandler.ListIntegrations)
				r.Get("/api/v1/issues", issueHandler.List)
				r.Get("/api/v1/issues/{id}", issueHandler.Get)
				r.Get("/api/v1/issues/{id}/priority", priorityHandler.GetPriorityScore)
				r.Get("/api/v1/issues/{id}/complexity", priorityHandler.GetComplexity)
				r.Get("/api/v1/priority-scores", priorityHandler.ListPriorityScores)
				r.Get("/api/v1/memories/*", memoryHandler.ListByRepo)
				r.Get("/api/v1/review-comments", memoryHandler.ListComments)
				r.Get("/api/v1/sessions", sessionHandler.List)
				r.Get("/api/v1/sessions/counts", sessionHandler.Counts)
				r.Get("/api/v1/sessions/{id}", sessionHandler.Get)
				r.Patch("/api/v1/sessions/{id}", sessionHandler.Update)
				r.Get("/api/v1/sessions/{id}/logs", sessionHandler.GetLogs)
				r.Get("/api/v1/sessions/{id}/logs/stream", sessionHandler.StreamLogs)
				r.Get("/api/v1/sessions/{id}/validation", sessionHandler.GetValidation)
				r.Get("/api/v1/sessions/{id}/pr", sessionHandler.GetPullRequest)
				r.Get("/api/v1/sessions/{id}/questions", sessionHandler.ListQuestions)
				r.Get("/api/v1/sessions/{id}/messages", sessionHandler.ListMessages)
				r.Get("/api/v1/sessions/{id}/timeline", sessionHandler.GetTimeline)
				r.Get("/api/v1/sessions/{id}/threads", sessionThreadHandler.ListThreads)
				r.Get("/api/v1/sessions/{id}/threads/{tid}", sessionThreadHandler.GetThread)
				r.Get("/api/v1/sessions/{id}/threads/{tid}/messages", sessionThreadHandler.GetThreadMessages)
				r.Get("/api/v1/sessions/{id}/threads/{tid}/logs", sessionThreadHandler.GetThreadLogs)
				r.Get("/api/v1/sessions/{id}/thread-file-events", sessionThreadHandler.ListThreadFileEvents)
				r.Get("/api/v1/sessions/{id}/review-comments", sessionReviewCommentHandler.List)
				r.Get("/api/v1/sessions/{id}/usage", usageHandler.ListBySession)
				r.Get("/api/v1/usage", usageHandler.GetSummary)
				r.Get("/api/v1/sessions/{id}/preview", previewHandler.GetPreview)
				r.Get("/api/v1/sessions/{id}/preview/logs", previewHandler.GetLogs)
				r.Get("/api/v1/sessions/{id}/preview/services", previewHandler.GetServices)
				r.Get("/api/v1/sessions/{id}/preview/console", previewHandler.ReadConsole)
				r.Get("/api/v1/sessions/{id}/preview/snapshots", previewHandler.GetSnapshots)
				r.Get("/api/v1/pull-requests/stream", pullRequestHandler.StreamUpdates)
				r.Get("/api/v1/pull-requests/{id}/health", pullRequestHandler.GetHealth)
				r.Get("/api/v1/repos/{owner}/{repo}/preview/detect", previewHandler.DetectReadiness)
				r.Get("/api/v1/uploads/files/*", uploadHandler.ServeUpload)
				r.Get("/api/v1/sessions/{id}/files", sessionFileHandler.ListFiles)
				r.Get("/api/v1/sessions/{id}/files/content", sessionFileHandler.GetFileContent)
				r.Get("/api/v1/sessions/{id}/files/context", sessionFileHandler.GetFileContext)
				r.Get("/api/v1/settings", settingsHandler.Get)
				r.Get("/api/v1/settings/llm-defaults", settingsHandler.GetLLMDefaults)
				r.Get("/api/v1/settings/llm-models", settingsHandler.GetLLMModels)
				r.Get("/api/v1/pm/current", pmHandler.Current)
				r.Get("/api/v1/pm/plans", pmHandler.List)
				r.Get("/api/v1/pm/plans/{id}", pmHandler.Get)
				r.Get("/api/v1/pm/plans/latest", pmHandler.Latest)
				r.Get("/api/v1/pm/decisions", pmHandler.Decisions)
				r.Get("/api/v1/pm/status", pmHandler.Status)
				// Automations (read-only)
				r.Get("/api/v1/automations", automationHandler.List)
				r.Get("/api/v1/automations/{id}", automationHandler.Get)
				r.Get("/api/v1/automations/{id}/runs", automationHandler.ListRuns)
				r.Get("/api/v1/automations/{id}/runs/{rid}", automationHandler.GetRun)
				r.Get("/api/v1/automations/{id}/stats", automationHandler.Stats)

				r.Get("/api/v1/projects", projectHandler.List)
				r.Get("/api/v1/projects/proposals/summary", projectHandler.ProposalSummary)
				r.Get("/api/v1/projects/{id}", projectHandler.Get)
				r.Get("/api/v1/projects/{id}/cycles", projectHandler.ListCycles)
				r.Get("/api/v1/projects/{id}/cycles/{cycleId}", projectHandler.GetCycle)
				r.Get("/api/v1/projects/{id}/attachments", projectAttachmentHandler.List)
				r.Get("/api/v1/projects/{id}/specs", projectSpecHandler.List)
				r.Get("/api/v1/projects/{id}/specs/{specId}", projectSpecHandler.Get)
				r.Get("/api/v1/pm/documents", pmDocumentHandler.List)
				r.Get("/api/v1/pm/documents/{docId}", pmDocumentHandler.Get)
				r.Get("/api/v1/pm/documents/{docId}/versions", pmDocumentHandler.ListVersions)
				r.Get("/api/v1/pm/document-set-pins", pmDocumentHandler.ListDocumentSetPins)
				r.Get("/api/v1/pm/document-set-pins/{pinId}", pmDocumentHandler.GetDocumentSetPin)

			})

			// Write routes (admin and member only)
			r.Group(func(r chi.Router) {
				r.Use(middleware.OrgContext)
				r.Use(middleware.RequireRole("admin", "member"))

				r.Patch("/api/v1/repositories/{id}", repoHandler.Update)
				r.Post("/api/v1/repositories/{id}/disconnect", repoHandler.Disconnect)
				r.Post("/api/v1/repositories/{id}/reconnect", repoHandler.Reconnect)

				// Team roster read — sits in the admin+member group (not the
				// all-roles read group) so viewers cannot enumerate org members.
				r.Get("/api/v1/team/members", teamHandler.ListMembers)

				// Coding-agents config reads. Members can view what's configured
				// (so /settings/agent renders read-only); mutations stay admin-only.
				r.Get("/api/v1/settings/coding-auths", codingAuthHandler.List)
				r.Get("/api/v1/settings/codex-auth/subscriptions", codexAuthHandler.List)
				r.Get("/api/v1/settings/claude-code-auth/subscriptions", claudeCodeAuthHandler.List)

				// Codex / Claude OAuth subscription flows. Org-scope writes are
				// admin-gated inside each handler (see resolveOAuthScope);
				// personal-scope writes are available to any member because they
				// target the caller's own credential rows. Routing both into the
				// admin+member group lets a single endpoint serve both cases —
				// the handler decides based on the request's scope param.
				r.Post("/api/v1/settings/codex-auth/initiate", codexAuthHandler.Initiate)
				r.Get("/api/v1/settings/codex-auth/status", codexAuthHandler.Status)
				r.Post("/api/v1/settings/codex-auth/disconnect", codexAuthHandler.DisconnectAll) // legacy compat
				r.Delete("/api/v1/settings/codex-auth/subscriptions/{id}", codexAuthHandler.DisconnectByPath)

				r.Post("/api/v1/settings/claude-code-auth/initiate", claudeCodeAuthHandler.Initiate)
				r.Post("/api/v1/settings/claude-code-auth/complete", claudeCodeAuthHandler.Complete)
				r.Post("/api/v1/settings/claude-code-auth/disconnect", claudeCodeAuthHandler.DisconnectAll) // legacy compat
				r.Delete("/api/v1/settings/claude-code-auth/subscriptions/{id}", claudeCodeAuthHandler.DisconnectByPath)

				// Unified coding-credentials writes. Personal-scope mutations live in
				// this group because they target the requester's own credentials and
				// do not require admin privileges for members. The handler enforces
				// "admin only when scope=org" via resolveScopeFromBody; per-row Move
				// and bulk Reorder both rely on that gate, so both can sit here
				// without allowing members to reorder the org stack.
				// See docs/design/future/65-unified-coding-credentials.md.
				r.Post("/api/v1/coding-credentials", codingCredentialHandler.Create)
				r.Patch("/api/v1/coding-credentials/{id}", codingCredentialHandler.Update)
				r.Delete("/api/v1/coding-credentials/{id}", codingCredentialHandler.Delete)
				r.Patch("/api/v1/coding-credentials/{id}/move", codingCredentialHandler.Move)
				r.Patch("/api/v1/coding-credentials/reorder", codingCredentialHandler.Reorder)

				// Eval reads — admin+member only so viewers cannot enumerate eval
				// tasks or runs. Eval writes are gated even more tightly (admin-only)
				// further down.
				r.Get("/api/v1/evals/tasks", evalHandler.ListTasks)
				r.Get("/api/v1/evals/tasks/{id}", evalHandler.GetTask)
				r.Get("/api/v1/evals/tasks/{id}/runs", evalHandler.ListRuns)
				r.Get("/api/v1/evals/runs/{runId}", evalHandler.GetRun)
				r.Get("/api/v1/evals/batch", evalHandler.ListBatches)
				r.Get("/api/v1/evals/batch/{batchId}", evalHandler.GetBatch)
				r.Get("/api/v1/evals/batch/{batchId}/stream", evalHandler.StreamBatchUpdates)
				r.Get("/api/v1/evals/bootstrap/candidates", evalHandler.GetBootstrapCandidates)
				r.Get("/api/v1/evals/bootstrap/{runId}/stream", evalHandler.StreamBootstrapUpdates)

				// Personal credential management
				r.Put("/api/v1/settings/credentials/personal/{provider}", userCredentialHandler.UpsertPersonal)
				r.Delete("/api/v1/settings/credentials/personal/{provider}", userCredentialHandler.DeletePersonal)

				// GitHub connection for user-authored PRs
				r.Get("/api/v1/users/me/github/connect", githubStatusHandler.StartConnect)
				r.Get("/api/v1/users/me/github/callback", githubStatusHandler.HandleConnectCallback)
				r.Post("/api/v1/users/me/github/disconnect", githubStatusHandler.Disconnect)

				r.Post("/api/v1/issues/{id}/fix", sessionHandler.TriggerFix)
				// File upload (higher body-size limit for multipart uploads).
				r.With(middleware.MaxBodySize(11<<20)).Post("/api/v1/uploads", uploadHandler.Upload)

				r.Post("/api/v1/sessions/{id}/view", sessionHandler.RecordView)
				r.Post("/api/v1/sessions/manual", sessionHandler.CreateManual)
				r.Post("/api/v1/sessions/{id}/questions/{qid}/answer", sessionHandler.AnswerQuestion)
				r.Post("/api/v1/sessions/{id}/messages", sessionHandler.SendMessage)
				r.Post("/api/v1/sessions/{id}/end", sessionHandler.EndSession)
				r.Post("/api/v1/sessions/{id}/retry", sessionHandler.RetrySession)
				r.Post("/api/v1/sessions/{id}/cancel", sessionHandler.CancelSession)
				r.Post("/api/v1/sessions/{id}/archive", sessionHandler.ArchiveSession)
				r.Post("/api/v1/sessions/{id}/unarchive", sessionHandler.UnarchiveSession)
				r.Post("/api/v1/sessions/{id}/pr", sessionHandler.CreatePR)
				r.Post("/api/v1/sessions/{id}/pr/push", sessionHandler.PushChangesToPR)
				r.Post("/api/v1/pull-requests/{id}/repair/fix-tests", pullRequestHandler.FixTests)
				r.Post("/api/v1/pull-requests/{id}/repair/resolve-conflicts", pullRequestHandler.ResolveConflicts)
				r.Post("/api/v1/pull-requests/{id}/merge", pullRequestHandler.Merge)
				r.Post("/api/v1/sessions/{id}/threads", sessionThreadHandler.CreateThread)
				r.Patch("/api/v1/sessions/{id}/threads/{tid}", sessionThreadHandler.UpdateThread)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/archive", sessionThreadHandler.ArchiveThread)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/messages", sessionThreadHandler.SendThreadMessage)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/end", sessionThreadHandler.EndThread)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/cancel", sessionThreadHandler.CancelThread)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/fork", sessionThreadHandler.ForkThread)
				r.Post("/api/v1/sessions/{id}/threads/{tid}/revert", sessionThreadHandler.RevertThread)
				r.Post("/api/v1/sessions/{id}/review-comments", sessionReviewCommentHandler.Create)
				r.Post("/api/v1/sessions/{id}/preview", previewHandler.StartPreview)
				r.Delete("/api/v1/sessions/{id}/preview", previewHandler.StopPreview)
				r.Post("/api/v1/sessions/{id}/preview/restart", previewHandler.RestartPreview)
				r.Post("/api/v1/sessions/{id}/preview/bootstrap", previewHandler.MintBootstrapToken)
				r.Post("/api/v1/sessions/{id}/preview/extend", previewHandler.ExtendTTL)
				r.Post("/api/v1/sessions/{id}/preview/screenshot", previewHandler.CaptureScreenshot)
				r.Post("/api/v1/sessions/{id}/preview/inspect", previewHandler.InspectElement)
				r.Post("/api/v1/sessions/{id}/preview/design-feedback", previewHandler.SubmitDesignFeedback)
				r.Post("/api/v1/sessions/{id}/preview/interact", previewHandler.ExecuteInteraction)
				r.Post("/api/v1/sessions/{id}/preview/multi-viewport", previewHandler.CaptureMultiViewport)
				r.Post("/api/v1/sessions/{id}/preview/visual-diff", previewHandler.ComputeVisualDiff)
				r.Post("/api/v1/sessions/{id}/preview/assert", previewHandler.RunAssertions)
				r.Post("/api/v1/sessions/{id}/review-comments/send", sessionReviewCommentHandler.SendToAgent)
				r.Patch("/api/v1/sessions/{id}/review-comments/{commentId}", sessionReviewCommentHandler.Update)
				r.Delete("/api/v1/sessions/{id}/review-comments/{commentId}", sessionReviewCommentHandler.Delete)
				// Automations (write)
				r.Post("/api/v1/automations", automationHandler.Create)
				r.Patch("/api/v1/automations/{id}", automationHandler.Update)
				r.Delete("/api/v1/automations/{id}", automationHandler.Delete)
				r.Post("/api/v1/automations/{id}/run", automationHandler.RunNow)
				r.Post("/api/v1/automations/{id}/pause", automationHandler.Pause)
				r.Post("/api/v1/automations/{id}/resume", automationHandler.Resume)
				r.Post("/api/v1/automations/bulk", automationHandler.Bulk)

				r.Post("/api/v1/projects", projectHandler.Create)
				r.Patch("/api/v1/projects/{id}", projectHandler.Update)
				r.Delete("/api/v1/projects/{id}", projectHandler.Delete)
				r.Post("/api/v1/projects/{id}/start", projectHandler.Start)
				r.Post("/api/v1/projects/{id}/archive", projectHandler.Archive)
				r.Post("/api/v1/projects/{id}/unarchive", projectHandler.Unarchive)
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
				r.Post("/api/v1/pm/documents/discover/notion", pmDocumentHandler.DiscoverNotion)
				r.Patch("/api/v1/pm/documents/{docId}", pmDocumentHandler.Update)
				r.Delete("/api/v1/pm/documents/{docId}", pmDocumentHandler.Delete)
				r.Post("/api/v1/pm/documents/{docId}/sync", pmDocumentHandler.SyncFromNotion)
				r.Post("/api/v1/pm/documents/{docId}/restore", pmDocumentHandler.RestoreVersion)
				r.Post("/api/v1/pm/document-set-pins", pmDocumentHandler.CreateDocumentSetPin)

			})

			// Admin-only routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.OrgContext)
				r.Use(middleware.RequireRole("admin"))

				r.Delete("/api/v1/repositories/{id}", repoHandler.Delete)
				r.Post("/api/v1/issues/{id}/reprioritize", priorityHandler.Reprioritize)
				r.Post("/api/v1/pm/analyze", pmHandler.Analyze)
				r.Post("/api/v1/pm/bootstrap", pmHandler.Bootstrap)
				r.Post("/api/v1/pm/refresh", pmHandler.Refresh)
				r.Get("/api/v1/pm/context/pending", pmHandler.ListPendingRefreshes)
				r.Post("/api/v1/pm/context/{id}/accept", pmHandler.AcceptRefresh)
				r.Delete("/api/v1/pm/context/{id}/reject", pmHandler.RejectRefresh)
				r.Patch("/api/v1/settings", settingsHandler.Update)
				r.Post("/api/v1/memories", memoryHandler.Create)
				r.Patch("/api/v1/memories/{id}", memoryHandler.UpdateStatus)
				r.Put("/api/v1/memories/{id}", memoryHandler.UpdateRule)

				// Org credential management. Reads (List) live in the admin+member
				// group above so members can view the coding-agents settings page
				// in read-only mode; mutations stay admin-only here.
				r.Get("/api/v1/settings/credentials", credentialHandler.List)
				r.Put("/api/v1/settings/credentials/{provider}", credentialHandler.Update)
				r.Delete("/api/v1/settings/credentials/{provider}", credentialHandler.Delete)
				r.Post("/api/v1/settings/coding-auths", codingAuthHandler.Create)
				r.Patch("/api/v1/settings/coding-auths/reorder", codingAuthHandler.Reorder)
				r.Patch("/api/v1/settings/coding-auths/{id}", codingAuthHandler.Update)
				r.Delete("/api/v1/settings/coding-auths/{id}", codingAuthHandler.Delete)

				// Team default credential management
				r.Put("/api/v1/settings/credentials/team/{provider}", userCredentialHandler.SetTeamDefault)
				r.Delete("/api/v1/settings/credentials/team/{provider}", userCredentialHandler.DeleteTeamDefault)

				// Codex / Claude OAuth subscription endpoints moved to the
				// admin+member group above. The handlers' resolveOAuthScope
				// keeps the admin gate on org-scope traffic, so members
				// disconnecting their own personal subscription doesn't
				// require elevating them to admin.

				// Usage timeseries, breakdown, and export (admin-only)
				r.Get("/api/v1/usage/timeseries", usageHandler.GetTimeseries)
				r.Get("/api/v1/usage/breakdown", usageHandler.GetBreakdown)
				r.Get("/api/v1/usage/export", usageHandler.ExportCSV)

				// Audit logs
				r.Get("/api/v1/audit-logs", auditLogHandler.List)
				r.Get("/api/v1/audit-logs/{id}", auditLogHandler.Get)

				// Team management. The roster read (GET /team/members) is registered
				// in the admin+member group above; mutations and invite flows stay
				// admin-only here.
				r.Patch("/api/v1/team/members/{id}/role", teamHandler.ChangeRole)
				r.Delete("/api/v1/team/members/{id}", teamHandler.RemoveMember)
				r.Get("/api/v1/team/invitations", teamHandler.ListInvitations)
				r.Post("/api/v1/team/invitations", teamHandler.CreateInvitation)
				r.Delete("/api/v1/team/invitations/{id}", teamHandler.RevokeInvitation)
				r.Get("/api/v1/team/github/status", teamHandler.GitHubInviteStatus)
				r.Get("/api/v1/team/github/users", teamHandler.SearchGitHubUsers)

				// Integration management (OAuth flows + connect/disconnect/sync).
				// Connecting an integration is an org-wide trust decision, so members
				// shouldn't be able to wire prod Slack/GitHub/Linear/Sentry/Notion.
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
				r.Post("/api/v1/integrations/notion/connect", integrationHandler.ConnectNotion)
				r.Delete("/api/v1/integrations/notion/disconnect", integrationHandler.DisconnectIntegration)

				// Eval write routes (admin-only — creating tasks shapes org-wide eval setup).
				r.Post("/api/v1/evals/tasks", evalHandler.CreateTask)
				r.Patch("/api/v1/evals/tasks/{id}", evalHandler.UpdateTask)
				r.Delete("/api/v1/evals/tasks/{id}", evalHandler.ArchiveTask)
				r.Post("/api/v1/evals/tasks/{id}/runs", evalHandler.StartRun)
				r.Post("/api/v1/evals/batch", evalHandler.StartBatch)
				r.Post("/api/v1/evals/bootstrap", evalHandler.Bootstrap)
				r.Post("/api/v1/evals/bootstrap/accept", evalHandler.AcceptBootstrapCandidates)
			})
		})
	})

	r.Mount("/", apiRoutes)

	return r, gwSrv, recycleWorker, inspectorCloser, previewManager, nil
}

func resolveRouterCodingCredentialStore(pool *pgxpool.Pool, cryptoSvc *crypto.Service, shared ...*db.CodingCredentialStore) *db.CodingCredentialStore {
	if len(shared) > 0 && shared[0] != nil {
		return shared[0]
	}
	return db.NewCodingCredentialStore(pool, cryptoSvc)
}
