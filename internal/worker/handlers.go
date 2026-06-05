package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/feedback"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/pm"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/assembledhq/143/internal/services/prioritization"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	slackbotsvc "github.com/assembledhq/143/internal/services/slackbot"
	"github.com/assembledhq/143/internal/version"
)

const sandboxCapacityRetryDelay = 10 * time.Second
const previewCapacityRetryDelay = 5 * time.Second
const prePRReviewRetryDelay = 5 * time.Second
const failureCategoryStaleSandbox = "stale_sandbox"

var sandboxCapacityDetailPattern = regexp.MustCompile(`sandbox capacity reached:\s*(\d+)/(\d+)\s+sandboxes active or reserved`)

const sandboxCapacityBaseExplanation = "Sandbox capacity was unavailable before the retry window expired. This can happen during deploys or when other sessions are using available capacity."

func sandboxCapacityFailureExplanation(ctx context.Context, stores *Stores, logger zerolog.Logger, deadLetterErr error) string {
	var samples []db.WorkerLoadSample
	if stores != nil && stores.Jobs != nil {
		loaded, err := stores.Jobs.WorkerLoadSamples(ctx)
		if err != nil {
			logger.Warn().Err(err).Msg("failed to load worker capacity details for sandbox-capacity failure")
		} else {
			if loaded == nil {
				loaded = []db.WorkerLoadSample{}
			}
			samples = loaded
		}
	}
	logSandboxCapacityFailureDetails(logger, deadLetterErr, samples)
	return formatSandboxCapacityFailureExplanation(deadLetterErr, samples)
}

func formatSandboxCapacityFailureExplanation(deadLetterErr error, samples []db.WorkerLoadSample) string {
	return sandboxCapacityBaseExplanation
}

func logSandboxCapacityFailureDetails(logger zerolog.Logger, deadLetterErr error, samples []db.WorkerLoadSample) {
	event := logger.Warn().Err(deadLetterErr)
	if current, max, ok := finalSandboxCapacityCheck(deadLetterErr); ok {
		event = event.Int("final_active_or_reserved_sandboxes", current).Int("final_max_active_sandboxes", max)
	} else if errors.Is(deadLetterErr, agent.ErrSandboxCapacity) {
		event = event.Bool("final_sandbox_capacity_full", true)
	}
	if samples != nil {
		runningSessions, sandboxContainers, activePreviews, previewHeldContainers, runningSessionJobs := summarizeWorkerLoadForSandboxCapacity(samples)
		event = event.
			Int64("worker_load_running_sessions", runningSessions).
			Int64("worker_load_session_sandbox_containers", sandboxContainers).
			Int64("worker_load_active_previews", activePreviews).
			Int64("worker_load_preview_held_containers", previewHeldContainers).
			Int64("worker_load_running_session_preview_jobs", runningSessionJobs).
			Int("worker_load_sample_count", len(samples))
	}
	event.Msg("sandbox capacity retry window exhausted")
}

func finalSandboxCapacityCheck(err error) (int, int, bool) {
	if err == nil {
		return 0, 0, false
	}
	matches := sandboxCapacityDetailPattern.FindStringSubmatch(err.Error())
	if len(matches) == 3 {
		current, currentErr := strconv.Atoi(matches[1])
		maxActive, maxErr := strconv.Atoi(matches[2])
		if currentErr == nil && maxErr == nil {
			return current, maxActive, true
		}
	}
	return 0, 0, false
}

func summarizeWorkerLoadForSandboxCapacity(samples []db.WorkerLoadSample) (int64, int64, int64, int64, int64) {
	var runningSessions int64
	var sandboxContainers int64
	var activePreviews int64
	var previewHeldContainers int64
	var runningSessionJobs int64
	for _, sample := range samples {
		runningSessions += sample.RunningSessions
		sandboxContainers += sample.SandboxContainers
		activePreviews += sample.ActivePreviews
		previewHeldContainers += sample.PreviewHeldContainers
		runningSessionJobs += sample.RunningSessionJobs
	}
	return runningSessions, sandboxContainers, activePreviews, previewHeldContainers, runningSessionJobs
}

func sandboxCapacityRetryTarget(ctx context.Context, stores *Stores, logger zerolog.Logger) (*string, bool) {
	if stores == nil || stores.Jobs == nil {
		return nil, false
	}
	excludeNodeID, _ := jobctx.WorkerNodeIDFromContext(ctx)
	targetNodeID, err := stores.Jobs.SelectWorkerWithSandboxCapacity(ctx, excludeNodeID)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to select worker with available sandbox capacity")
		return nil, false
	}
	if targetNodeID != nil {
		logger.Info().
			Str("target_node_id", *targetNodeID).
			Str("excluded_node_id", excludeNodeID).
			Msg("routing sandbox capacity retry to worker with available capacity")
		return targetNodeID, false
	}
	logger.Info().
		Str("excluded_node_id", excludeNodeID).
		Msg("no alternate worker advertises sandbox capacity; clearing retry target pin")
	return nil, true
}

func previewBusyRetryTarget(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) *string {
	if stores == nil || stores.Sessions == nil {
		return nil
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to load session worker target for sandbox-busy preview retry")
		return nil
	}
	return models.SessionWorkerTarget(&session)
}

func registerStaleSandboxDeadLetter(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session, threadID *uuid.UUID, jobType string) {
	if stores == nil || stores.Sessions == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()

		errMsg := "Session stopped after cleaning up a stale sandbox but the retry could not be scheduled."
		explanation := "The worker found a stale sandbox container reference from an earlier interrupted attempt and cleared it, but the job dead-lettered before a fresh attempt could start."
		nextSteps := []string{
			"Retry the session to start with a clean sandbox",
			"Check worker and sandbox logs if this repeats across sessions",
		}
		if deadLetterErr != nil {
			logger.Warn().
				Err(deadLetterErr).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("session job dead-lettered after stale sandbox cleanup")
		}

		result := &models.SessionResult{Error: &errMsg}
		if err := stores.Sessions.UpdateResult(writeCtx, session.OrgID, session.ID, models.SessionStatusFailed, result); err != nil {
			logger.Error().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("failed to mark session failed after stale sandbox cleanup dead-letter")
			return
		}
		if err := stores.Sessions.UpdateFailure(writeCtx, session.OrgID, session.ID, explanation, failureCategoryStaleSandbox, nextSteps, true); err != nil {
			logger.Error().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("failed to persist stale sandbox failure details")
		}
		if threadID != nil && *threadID != uuid.Nil && stores.SessionThreads != nil {
			threadCategory := failureCategoryStaleSandbox
			threadResult := &models.SessionResult{
				Error:           &errMsg,
				FailureCategory: &threadCategory,
			}
			if err := stores.SessionThreads.UpdateResult(writeCtx, session.OrgID, *threadID, models.ThreadStatusFailed, threadResult); err != nil {
				logger.Error().
					Err(err).
					Str("session_id", session.ID.String()).
					Str("thread_id", threadID.String()).
					Str("job_type", jobType).
					Msg("failed to mark session thread failed after stale sandbox cleanup dead-letter")
			}
		}
		if stores.Jobs != nil {
			linear.EnqueueMilestone(writeCtx, stores.Jobs, logger, session.OrgID, session.ID, "failed", 0)
		}
		enqueueSlackRunUpdateIfLinked(writeCtx, stores, logger, session.OrgID, session.ID, "failed", "143 session failed", errMsg, true)
		enqueueSlackSessionNotifications(writeCtx, stores, logger, session.OrgID, session.ID, session.AutomationRunID, "session.failed", "143 session failed", errMsg)
	})
}

func registerSandboxCapacityDeadLetter(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session, threadID *uuid.UUID, jobType string) {
	if stores == nil || stores.Sessions == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()

		errMsg := "Session stopped because sandbox capacity stayed full until the retry window expired."
		nextSteps := []string{
			"Retry the session when sandbox capacity is available",
			"Cancel sessions that are no longer needed to free up capacity",
		}
		failedSession := session
		failureCategory := agent.FailureCategorySandboxCapacity
		failedSession.Status = models.SessionStatusFailed
		failedSession.Error = &errMsg
		failedSession.FailureCategory = &failureCategory
		failedSession.FailureNextSteps = nextSteps
		failedSession.FailureRetryAdvised = true
		if deadLetterErr != nil {
			logger.Warn().
				Err(deadLetterErr).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("session job dead-lettered after sandbox capacity retries")
		}

		result := &models.SessionResult{Error: &errMsg}
		if err := stores.Sessions.UpdateResult(writeCtx, session.OrgID, session.ID, models.SessionStatusFailed, result); err != nil {
			logger.Error().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("failed to mark session failed after sandbox capacity dead-letter")
			return
		}
		explanation := sandboxCapacityFailureExplanation(writeCtx, stores, logger, deadLetterErr)
		failedSession.FailureExplanation = &explanation
		if err := stores.Sessions.UpdateFailure(writeCtx, session.OrgID, session.ID, explanation, agent.FailureCategorySandboxCapacity, nextSteps, true); err != nil {
			logger.Error().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("failed to persist sandbox capacity failure details")
		}
		if threadID != nil && *threadID != uuid.Nil && stores.SessionThreads != nil {
			threadCategory := agent.FailureCategorySandboxCapacity
			threadResult := &models.SessionResult{
				Error:           &errMsg,
				FailureCategory: &threadCategory,
			}
			if err := stores.SessionThreads.UpdateResult(writeCtx, session.OrgID, *threadID, models.ThreadStatusFailed, threadResult); err != nil {
				logger.Error().
					Err(err).
					Str("session_id", session.ID.String()).
					Str("thread_id", threadID.String()).
					Str("job_type", jobType).
					Msg("failed to mark session thread failed after sandbox capacity dead-letter")
			}
		}
		if services != nil && services.ProjectTasks != nil && failedSession.ProjectTaskID != nil {
			if err := services.ProjectTasks.OnSessionComplete(writeCtx, &failedSession, models.SessionStatusFailed); err != nil {
				logger.Warn().
					Err(err).
					Str("session_id", failedSession.ID.String()).
					Str("job_type", jobType).
					Msg("failed to update project task after sandbox capacity dead-letter")
			}
		}
		if services != nil && services.AutomationRuns != nil && failedSession.AutomationRunID != nil {
			if err := services.AutomationRuns.OnSessionComplete(writeCtx, &failedSession, models.SessionStatusFailed); err != nil {
				logger.Warn().
					Err(err).
					Str("session_id", failedSession.ID.String()).
					Str("job_type", jobType).
					Msg("failed to update automation run after sandbox capacity dead-letter")
			}
		}
		if stores.Jobs != nil {
			linear.EnqueueMilestone(writeCtx, stores.Jobs, logger, failedSession.OrgID, failedSession.ID, "failed", 0)
		}
		enqueueSlackRunUpdateIfLinked(writeCtx, stores, logger, failedSession.OrgID, failedSession.ID, "failed", "143 session failed", errMsg, true)
		enqueueSlackSessionNotifications(writeCtx, stores, logger, failedSession.OrgID, failedSession.ID, failedSession.AutomationRunID, "session.failed", "143 session failed", errMsg)
	})
}

// DataRetentionConfig holds retention periods for the data cleanup handler.
type DataRetentionConfig struct {
	WebhookDays int
	LogsDays    int
	JobsDays    int
}

// RegisterHandlers registers all job handlers on the worker.
func RegisterHandlers(w *Worker, stores *Stores, services *Services, retentionCfg DataRetentionConfig, logger zerolog.Logger) {
	w.Register("ingest_webhook", newIngestWebhookHandler(stores, logger))
	w.Register("sync_sentry", newSyncSentryHandler(stores, logger))
	w.Register("sync_slack", newSyncSlackHandler(stores, services, logger))
	w.Register("slack_start_or_continue_session", newSlackStartOrContinueSessionHandler(stores, services, logger))
	w.Register("slack_sync_app_home", newSlackSyncAppHomeHandler(stores, services, logger))
	w.Register("slack_post_run_update", newSlackPostRunUpdateHandler(stores, services, logger))
	w.Register("slack_post_final_response", newSlackPostFinalResponseHandler(stores, services, logger))
	w.Register("slack_deliver_human_input", newSlackDeliverHumanInputHandler(stores, services, logger))
	w.Register("slack_send_notification", newSlackSendNotificationHandler(stores, services, logger))
	w.Register("slack_handle_interaction", newSlackHandleInteractionHandler(stores, services, logger))
	if services != nil && services.Prioritization != nil {
		w.Register("prioritize", newPrioritizeHandler(stores, services, logger))
	}
	if services != nil && services.PM != nil {
		w.Register(models.JobTypePMAnalyze, newPMAnalyzeHandler(stores, services, logger))
		w.Register(models.JobTypeProjectCycle, newProjectCycleHandler(services, logger))
		w.Register(models.JobTypePMBootstrap, newOrgIDJobHandler("pm_bootstrap", services.PM.RunBootstrap, logger))
		w.Register(models.JobTypePMContextRefresh, newOrgIDJobHandler("pm_context_refresh", services.PM.RunRefresh, logger))
	}
	if stores.Automations != nil && stores.AutomationRuns != nil {
		w.Register(models.JobTypeAutomationRun, newAutomationRunHandler(stores, services, logger))
	}
	if services != nil && services.PreviewStarter != nil {
		w.Register(models.JobTypeStartPreview, newStartPreviewHandler(stores, services, logger))
		w.Register(models.JobTypeStartBranchPreview, newStartBranchPreviewHandler(stores, services, logger))
	}
	if hasServiceHandlersDependencies(services) {
		w.Register("run_agent", newRunAgentHandler(stores, services, logger))
		w.Register("continue_session", newContinueSessionHandler(stores, services, logger))
		w.Register("cancel_session", newCancelSessionHandler(stores, services, logger))
		w.Register("cancel_thread", newCancelThreadHandler(stores, services, logger))
		w.Register("deliver_thread_inbox", newDeliverThreadInboxHandler(services, logger))
		w.Register("open_pr", newOpenPRHandler(stores, services, logger))
		w.Register("create_branch", newCreateBranchHandler(stores, services, logger))
		w.Register("push_pr_changes", newPushPRChangesHandler(stores, services, logger))
		w.Register("sync_pull_request_state", newSyncPullRequestStateHandler(services, logger))
		w.Register("reconcile_pull_request_state", newReconcilePullRequestStateHandler(services, logger))
		w.Register("enrich_pull_request_health", newEnrichPullRequestHealthHandler(services, logger))
		w.Register("merge_pull_request_when_ready", newMergePullRequestWhenReadyHandler(services, logger))
		w.Register("analyze_failure", newAnalyzeFailureHandler(stores, services, logger))
		w.Register("fork_session_thread", newForkSessionThreadHandler(stores, services, logger))
		w.Register("revert_session_thread", newRevertSessionThreadHandler(stores, services, logger))
	}
	if services != nil && services.Feedback != nil {
		w.Register("process_review_comment", newProcessReviewCommentHandler(services, logger))
		w.Register("update_memories", newUpdateMemoriesHandler(services, logger))
	}
	if services != nil && services.Memory != nil {
		w.Register("reinforce_memories", newReinforceMemoriesHandler(services, logger))
	}
	if stores.AuditLogs != nil && stores.Organizations != nil {
		w.Register("audit_retention_cleanup", newAuditRetentionCleanupHandler(stores, logger))
	}
	w.Register("data_retention_cleanup", newDataRetentionCleanupHandler(stores, retentionCfg, logger))
	if services != nil && services.Linear != nil {
		w.Register("prepare_linear_primary", newPrepareLinearPrimaryHandler(services.Linear, logger))
		w.Register("link_linear_issue", newLinkLinearIssueHandler(services.Linear, logger))
		w.Register("link_linear_issue_mid_session", newLinkLinearIssueMidSessionHandler(services.Linear, logger))
		w.Register("refresh_linear_team_keys", newRefreshLinearTeamKeysHandler(services.Linear, logger))
		w.Register("linear_milestone", newLinearMilestoneHandler(stores, services.Linear, logger))
		// linear_agent_event handler — wires the inbound agent path
		// (assign / @-mention triggers a 143 session). Returns nil when
		// the agent stores aren't wired or required services are missing,
		// in which case the registration is a silent no-op (the
		// dispatcher won't even produce these jobs without the same
		// stores being wired upstream).
		if services.LinearAgentDeps != nil {
			if h := newLinearAgentEventHandler(*services.LinearAgentDeps); h != nil {
				w.Register("linear_agent_event", h)
			}
		}
	}
	if stores.EvalRuns != nil && stores.EvalTasks != nil {
		w.Register("run_eval", newRunEvalHandler(stores, services, logger))
	}
	if stores.EvalBootstraps != nil && services != nil && services.SandboxProvider != nil {
		w.Register("run_eval_bootstrap", newRunEvalBootstrapHandler(stores, services, logger))
	}
}

func hasServiceHandlersDependencies(services *Services) bool {
	if services == nil {
		return false
	}
	return services.Orchestrator != nil &&
		services.PR != nil &&
		services.Failure != nil &&
		services.SandboxProvider != nil
}

// Stores holds all the database stores needed by job handlers.
type Stores struct {
	Issues              *db.IssueStore
	Sessions            *db.SessionStore
	Jobs                *db.JobStore
	Integrations        *db.IntegrationStore
	Users               *db.UserStore
	Memberships         *db.OrganizationMembershipStore
	Webhooks            *db.WebhookDeliveryStore
	PriorityScores      *db.PriorityScoreStore
	ComplexityEstimates *db.ComplexityEstimateStore
	Projects            *db.ProjectStore        // nil-safe: projects feature disabled if nil
	ProjectTasks        *db.ProjectTaskStore    // nil-safe
	Credentials         *db.OrgCredentialStore  // nil-safe: needed for sync_slack
	AuditLogs           *db.AuditLogStore       // nil-safe: audit retention cleanup
	Organizations       *db.OrganizationStore   // nil-safe: needed for audit retention
	SessionLogs         *db.SessionLogStore     // nil-safe: data retention cleanup
	EvalTasks           *db.EvalTaskStore       // nil-safe: eval feature
	EvalRuns            *db.EvalRunStore        // nil-safe: eval feature
	EvalBatches         *db.EvalBatchStore      // nil-safe: eval feature
	EvalBootstraps      *db.EvalBootstrapStore  // nil-safe: eval bootstrap feature
	Repositories        *db.RepositoryStore     // nil-safe: needed for eval repo lookup
	SessionMessages     *db.SessionMessageStore // nil-safe: needed for title regeneration
	SessionThreads      *db.SessionThreadStore  // nil-safe: needed for thread-scoped continuation status
	HumanInputRequests  *db.SessionHumanInputRequestStore
	ThreadFileEvents    *db.SessionThreadFileEventStore // nil-safe: tab-level file write attribution
	SandboxHolders      *db.SessionSandboxHolderStore   // nil-safe: snapshot quiescence for shared sandbox thread runtimes
	IssueSnapshots      *db.SessionTurnIssueSnapshotStore
	Automations         *db.AutomationStore    // nil-safe: automations feature disabled if nil
	AutomationRuns      *db.AutomationRunStore // nil-safe: automations feature disabled if nil
	ReviewLoops         *db.SessionReviewLoopStore
	SessionIssueLinks   *db.SessionIssueLinkStore // nil-safe: needed for Linear milestones
	Previews            *db.PreviewStore
	PullRequests        *db.PullRequestStore
	SlackInstallations  *db.SlackInstallationStore
	SlackUserLinks      *db.SlackUserLinkStore
	SlackChannels       *db.SlackChannelSettingsStore
	SlackSessionLinks   *db.SlackSessionLinkStore
	SlackInboundEvents  *db.SlackInboundEventStore
	SlackOutbound       *db.SlackOutboundMessageStore
	SessionAttributions *db.SessionAttributionStore
}

func ensureSessionSnapshotQuiescent(ctx context.Context, stores *Stores, run models.Session) error {
	if run.PendingSnapshotKey != nil && *run.PendingSnapshotKey != "" {
		return &RetryableError{Err: agent.ErrSnapshotPending}
	}
	if run.Status == models.SessionStatusRunning {
		delay := 5 * time.Second
		return &RetryableError{Err: fmt.Errorf("session %s has active runtime work; snapshot is not quiescent", run.ID), RetryAfter: &delay}
	}
	if stores == nil || stores.SandboxHolders == nil {
		return nil
	}
	active, err := stores.SandboxHolders.CountActiveThreadRuntimesBySession(ctx, run.OrgID, run.ID)
	if err != nil {
		return fmt.Errorf("check active thread runtime holders: %w", err)
	}
	if active > 0 {
		delay := 5 * time.Second
		return &RetryableError{Err: fmt.Errorf("session %s has %d active thread runtime holder(s); snapshot is not quiescent", run.ID, active), RetryAfter: &delay}
	}
	return nil
}

// MemoryReinforcer retrieves and reinforces memories for a repo.
type MemoryReinforcer interface {
	GetContextMemories(ctx context.Context, req agent.MemoryContextRequest) (*agent.MemoryContextResult, error)
	ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error
}

type prCreator interface {
	CreatePR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	CreateBranch(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*ghservice.CreateBranchResult, error)
	PushChangesToPR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	SyncPullRequestState(ctx context.Context, orgID, pullRequestID uuid.UUID) error
	ReconcilePullRequestState(ctx context.Context, orgID uuid.UUID, limit int) error
	EnrichPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64) error
	ProcessMergeWhenReady(ctx context.Context, orgID, pullRequestID uuid.UUID) error
	// WaitForPostPRSnapshotUploads blocks until any in-flight post-PR
	// snapshot uploads (spawned by CreatePR) have either promoted or
	// cleared their pending_snapshot_key. Called by the server's graceful
	// shutdown path so a worker exit doesn't strand sessions with the
	// pending key set forever.
	WaitForPostPRSnapshotUploads()
}

// Services holds the service dependencies needed by job handlers.
type Services struct {
	Orchestrator    orchestratorService
	PR              prCreator
	Failure         *agent.FailureService
	SandboxProvider agent.SandboxProvider
	ProjectTasks    agent.ProjectTaskUpdater   // nil-safe: updates project tasks on terminal session fallback paths
	AutomationRuns  agent.AutomationRunUpdater // nil-safe: updates automation runs on terminal session fallback paths
	Prioritization  *prioritization.Service
	Feedback        *feedback.Service
	PM              pmService
	Memory          MemoryReinforcer              // optional — enables memory reinforcement on PR approval
	SlackSummarizer *ingestion.SlackSummarizer    // nil-safe: Slack summarization disabled if nil
	LLM             llmClient                     // nil-safe: needed for eval LLM judge grading
	GitHub          agent.GitHubTokenProvider     // nil-safe: needed for eval repo cloning
	TitleService    *services.SessionTitleService // nil-safe: session title regeneration
	Linear          *linear.Service               // nil-safe: Linear session-linking disabled if nil
	SlackbotMetrics *metrics.SlackbotMetrics      // nil-safe: Slackbot observability disabled if nil
	FrontendURL     string                        // optional base URL for Slack links
	// LinearAgentDeps wires the inbound agent feature (assign / @-mention
	// triggers a 143 session). It is intentionally independent of the
	// dispatcher kill switch so queued linear_agent_event jobs continue to
	// drain when LINEAR_AGENT_ENABLED is turned off.
	LinearAgentDeps *LinearAgentEventHandlerDeps
	ReviewLoops     interface {
		OnThreadTurnComplete(ctx context.Context, orgID, threadID uuid.UUID, assistantSummary string) error
		OnThreadTurnFailed(ctx context.Context, orgID, threadID uuid.UUID, summary string) error
		Start(ctx context.Context, orgID, sessionID uuid.UUID, req reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error)
	}
	// EvalBatchStreams publishes lightweight pub/sub signals on every batch
	// or run state transition so the eval-batch detail page can replace its
	// 5s poll with a Redis-backed SSE. nil-safe: best-effort publish, the
	// row is the source of truth and the page falls back to polling if SSE
	// cannot be established.
	EvalBatchStreams *cache.EvalBatchStreams
	// EvalBootstrapStreams is the bootstrap (PR-history scan) counterpart to
	// EvalBatchStreams; same nil-safety semantics.
	EvalBootstrapStreams *cache.EvalBootstrapStreams
	// SandboxAuthShutdown drains the per-session GitHub credential socket
	// listeners. nil when no SandboxAuthSocketDir is configured (local
	// dev). Called from cmd/server graceful shutdown after the API drains
	// so listener goroutines and on-disk socket files don't outlive the
	// process.
	SandboxAuthShutdown func()
	// SandboxAuthSweep removes per-session subdirs in the socket dir whose
	// UUID isn't in the keep set. Called once at startup, after the
	// rehydrate pass has re-Listen'd for every still-alive container, so
	// leftover dirs from prior worker generations don't accumulate. nil
	// when no SandboxAuthSocketDir is configured.
	SandboxAuthSweep func(keep map[uuid.UUID]struct{})
	// RuntimeSampler periodically polls per-container resource usage and
	// records it as OTel histograms so operators can size SANDBOX_* limits
	// against actual consumption rather than allocation. nil when sampling
	// is disabled (interval <= 0 in config) or the provider can't report
	// stats (e.g. a non-Docker provider in the future).
	RuntimeSampler *agent.RuntimeSampler
	// SandboxGC periodically reconciles provider-labeled local sandbox
	// containers against DB ownership so leaked containers cannot accumulate
	// indefinitely on worker disks. nil when disabled or unsupported.
	SandboxGC *agent.SandboxGC
	// PreviewStarter completes durable preview startup jobs. nil when this
	// node has no preview provider.
	PreviewStarter previewStarter
	// PreviewController handles control-plane actions for already-created
	// previews. nil when preview control is unavailable on this process.
	PreviewController previewController

	// SessionExecutorDispatcher moves run_agent and continue_session ownership
	// from the worker process to durable per-session executor containers when
	// configured. nil preserves inline execution only when
	// RequireSessionExecutorDispatcher is false, which is reserved for local
	// tests/dev wiring and executor-owned handler re-entry.
	SessionExecutorDispatcher sessionExecutorDispatcher
	// RequireSessionExecutorDispatcher makes a missing dispatcher a hard
	// runtime error for worker-owned run_agent/continue_session jobs. Production
	// workers set this so a startup wiring regression cannot silently reintroduce
	// deploy-sensitive inline long-running sessions.
	RequireSessionExecutorDispatcher bool
}

type previewStarter interface {
	StartReservedPreview(ctx context.Context, payload previewsvc.StartPreviewJobPayload) error
	StartReservedBranchPreview(ctx context.Context, payload previewsvc.StartBranchPreviewJobPayload) error
}

type previewController interface {
	RecyclePreview(ctx context.Context, orgID, previewID uuid.UUID) error
	StopPreview(ctx context.Context, orgID, previewID uuid.UUID) error
	SetLifetime(ctx context.Context, orgID, previewID uuid.UUID, duration time.Duration) (time.Time, error)
}

type orchestratorService interface {
	RunAgent(ctx context.Context, run *models.Session) error
	ContinueSession(ctx context.Context, session *models.Session, opts *agent.ContinueSessionOptions) error
	DeliverThreadInbox(ctx context.Context, orgID, sessionID, threadID uuid.UUID) error
	RecoverSession(ctx context.Context, session *models.Session) error
	CancelSessionByID(sessionID uuid.UUID) bool
	RequestSessionStopByID(sessionID uuid.UUID, reason agent.StopReason) bool
	CancelThreadByID(threadID uuid.UUID) bool
	RevertThread(ctx context.Context, session *models.Session, thread *models.SessionThread) error
	ResolveSessionTimeout(ctx context.Context, orgID uuid.UUID) time.Duration
	ResolveAbsoluteRuntimeCeiling(ctx context.Context, orgID uuid.UUID) time.Duration
}

// llmClient is the interface for LLM completion calls used by eval graders.
type llmClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type pmService interface {
	Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID, agentTypeOverride *models.AgentType) (*pm.Plan, error)
	AnalyzeProject(ctx context.Context, orgID, projectID uuid.UUID) error
	RunBootstrap(ctx context.Context, orgID uuid.UUID) error
	RunRefresh(ctx context.Context, orgID uuid.UUID) error
}

// ingest_webhook handler processes a webhook delivery asynchronously.
func newIngestWebhookHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			WebhookDeliveryID string `json:"webhook_delivery_id"`
			Provider          string `json:"provider"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal ingest_webhook payload: %w", err)
		}

		logger.Info().
			Str("webhook_delivery_id", input.WebhookDeliveryID).
			Str("provider", input.Provider).
			Msg("processing ingest_webhook job")

		// In a full implementation, this would re-fetch the webhook delivery,
		// parse it through the appropriate adapter, and call IngestNormalized.
		// For now, ingestion happens synchronously in the webhook handler.
		return nil
	}
}

func newStartPreviewHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.PreviewStarter == nil {
			return &FatalError{Err: fmt.Errorf("preview starter is not configured")}
		}
		var input previewsvc.StartPreviewJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal start_preview payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || input.PreviewID == uuid.Nil || input.UserID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("start_preview payload missing required ids")}
		}
		logger.Info().
			Str("preview_id", input.PreviewID.String()).
			Str("session_id", input.SessionID.String()).
			Msg("processing start_preview job")
		if err := services.PreviewStarter.StartReservedPreview(ctx, input); err != nil {
			if errors.Is(err, previewsvc.ErrPreviewCapacity) {
				retryAfter := previewCapacityRetryDelay
				targetNodeID, clearTarget := sandboxCapacityRetryTarget(ctx, stores, logger)
				logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("session_id", input.SessionID.String()).
					Dur("retry_after", retryAfter).
					Msg("preview capacity reached; retrying start_preview")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: targetNodeID, ClearTargetNodeID: clearTarget}
			}
			if errors.Is(err, agent.ErrStaleSandboxIDCleared) {
				retryAfter := 2 * time.Second
				logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("session_id", input.SessionID.String()).
					Dur("retry_after", retryAfter).
					Msg("preview cleared stale sandbox container_id; retrying start_preview")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrSandboxOnDifferentNode) {
				retryAfter := 2 * time.Second
				targetNodeID := previewBusyRetryTarget(ctx, stores, logger, input.OrgID, input.SessionID)
				logEvent := logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("session_id", input.SessionID.String()).
					Dur("retry_after", retryAfter)
				if targetNodeID != nil {
					logEvent = logEvent.Str("target_node_id", *targetNodeID)
				}
				logEvent.Msg("preview sandbox is on another worker; retrying start_preview on the recorded owner")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true, TargetNodeID: targetNodeID}
			}
			if errors.Is(err, previewsvc.ErrSandboxBusy) {
				retryAfter := 2 * time.Second
				targetNodeID := previewBusyRetryTarget(ctx, stores, logger, input.OrgID, input.SessionID)
				logEvent := logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("session_id", input.SessionID.String()).
					Dur("retry_after", retryAfter)
				if targetNodeID != nil {
					logEvent = logEvent.Str("target_node_id", *targetNodeID)
				}
				logEvent.Msg("preview sandbox is busy; retrying start_preview")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: targetNodeID}
			}
			enqueueSlackNotificationSubscribers(ctx, stores, logger, input.OrgID, slackNotificationFanoutInput{
				EventKind: "preview.failed",
				Title:     "Preview failed",
				Body:      err.Error(),
				SessionID: &input.SessionID,
				PreviewID: &input.PreviewID,
			})
			return &FatalError{Err: err}
		}
		enqueueSlackNotificationSubscribers(ctx, stores, logger, input.OrgID, slackNotificationFanoutInput{
			EventKind: "preview.ready",
			Title:     "Preview ready",
			Body:      "The preview is ready.",
			SessionID: &input.SessionID,
			PreviewID: &input.PreviewID,
		})
		return nil
	}
}

func newStartBranchPreviewHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.PreviewStarter == nil {
			return &FatalError{Err: fmt.Errorf("preview starter is not configured")}
		}
		var input previewsvc.StartBranchPreviewJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal start_branch_preview payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.UserID == uuid.Nil || input.PreviewID == uuid.Nil || input.PreviewTargetID == uuid.Nil || input.RepositoryID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("start_branch_preview payload missing required ids")}
		}
		logger.Info().
			Str("preview_id", input.PreviewID.String()).
			Str("preview_target_id", input.PreviewTargetID.String()).
			Msg("processing start_branch_preview job")
		if err := services.PreviewStarter.StartReservedBranchPreview(ctx, input); err != nil {
			if errors.Is(err, previewsvc.ErrPreviewCapacity) {
				retryAfter := previewCapacityRetryDelay
				targetNodeID, clearTarget := sandboxCapacityRetryTarget(ctx, stores, logger)
				logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("preview_target_id", input.PreviewTargetID.String()).
					Dur("retry_after", retryAfter).
					Msg("preview capacity reached; retrying start_branch_preview")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: targetNodeID, ClearTargetNodeID: clearTarget}
			}
			enqueueSlackNotificationSubscribers(ctx, stores, logger, input.OrgID, slackNotificationFanoutInput{
				EventKind: "preview.failed",
				Title:     "Preview failed",
				Body:      err.Error(),
				PreviewID: &input.PreviewID,
			})
			return &FatalError{Err: err}
		}
		enqueueSlackNotificationSubscribers(ctx, stores, logger, input.OrgID, slackNotificationFanoutInput{
			EventKind: "preview.ready",
			Title:     "Preview ready",
			Body:      "The preview is ready.",
			PreviewID: &input.PreviewID,
		})
		return nil
	}
}

// prioritize handler recomputes priority scores for an issue.
func newPrioritizeHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			IssueID string `json:"issue_id"`
			OrgID   string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal prioritize payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		issueID, err := uuid.Parse(input.IssueID)
		if err != nil {
			return fmt.Errorf("parse issue ID: %w", err)
		}
		// Compute priority score
		if _, err = services.Prioritization.ComputeScore(ctx, orgID, issueID); err != nil {
			return fmt.Errorf("compute priority score: %w", err)
		}
		// Fetch issue for complexity estimation
		issue, err := stores.Issues.GetByID(ctx, orgID, issueID)
		if err != nil {
			return fmt.Errorf("fetch issue: %w", err)
		}
		// Estimate complexity
		_, err = services.Prioritization.EstimateComplexity(ctx, orgID, issueID, &issue)
		if err != nil {
			logger.Warn().Err(err).Str("issue_id", issueID.String()).Msg("complexity estimation failed, skipping auto-trigger")
			return nil
		}
		return nil
	}
}

func newPMAnalyzeHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID     string `json:"org_id"`
			Trigger   string `json:"trigger"`
			RepoID    string `json:"repo_id,omitempty"`
			AgentType string `json:"agent_type,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal pm_analyze payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		trigger := models.PMTrigger(input.Trigger)
		if trigger == "" {
			trigger = models.PMTriggerCron
		}
		if err := trigger.Validate(); err != nil {
			return fmt.Errorf("invalid trigger: %w", err)
		}

		var repoID *uuid.UUID
		if input.RepoID != "" {
			parsed, err := uuid.Parse(input.RepoID)
			if err != nil {
				return fmt.Errorf("parse repo ID: %w", err)
			}
			repoID = &parsed
		}

		var agentTypeOverride *models.AgentType
		if input.AgentType != "" {
			at := models.AgentType(input.AgentType)
			agentTypeOverride = &at
		}

		logger.Info().Str("org_id", orgID.String()).Str("trigger", string(trigger)).Msg("running pm analyze")
		_, err = services.PM.Analyze(ctx, orgID, trigger, repoID, agentTypeOverride)
		if err != nil {
			// Mark all PM analysis errors as fatal (no retries). Analyze() creates a
			// new session record before doing any real work, so each retry would produce
			// a duplicate failed session in the UI. The scheduler will retry at the next
			// configured interval instead.
			return &FatalError{Err: err}
		}
		return nil
	}
}

// project_cycle handler runs a focused PM analysis for a single scheduled project.
func newProjectCycleHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID     string `json:"org_id"`
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal project_cycle payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		projectID, err := uuid.Parse(input.ProjectID)
		if err != nil {
			return fmt.Errorf("parse project ID: %w", err)
		}

		logger.Info().Str("org_id", orgID.String()).Str("project_id", projectID.String()).Msg("running project_cycle job")
		return services.PM.AnalyzeProject(ctx, orgID, projectID)
	}
}

// newAutomationRunHandler executes a single automation_run job by creating a
// Session owned by the run and dispatching it through the normal run_agent
// job pipeline. Completion bubbles back via AutomationRunUpdater on the
// Orchestrator (see services/automations/hooks.go).
//
// Concurrency contract: the handler relies on TransitionStatusIf to make every
// status transition out of `pending` atomic. Two workers handed a duplicate
// job (at-least-once delivery, retry-after-crash) both reach the conditional
// transition; whichever lands its UPDATE first wins, the loser sees
// transitioned=false and bails. This is what prevents two sessions from being
// created against the same automation_run row.
//
// Terminal status guarantee: by the time this handler returns, the
// automation_run row is in exactly one of {pending (lost race / unchanged),
// running (we own the session), skipped (automation deleted/paused), failed
// (session create failed)}. Leaving the row in pending after we've made
// changes would force the reaper to clean up, and the reaper's hour-long
// threshold is too slow to give the UI useful feedback.
func newAutomationRunHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID           string `json:"org_id"`
			AutomationID    string `json:"automation_id"`
			AutomationRunID string `json:"automation_run_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal automation_run payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.AutomationRunID)
		if err != nil {
			return fmt.Errorf("parse run ID: %w", err)
		}
		automationID, err := uuid.Parse(input.AutomationID)
		if err != nil {
			return fmt.Errorf("parse automation ID: %w", err)
		}

		log := logger.With().
			Str("org_id", orgID.String()).
			Str("automation_id", automationID.String()).
			Str("run_id", runID.String()).
			Logger()
		log.Info().Msg("running automation_run job")

		// Fast-path early-exit: if the row is already non-pending we can skip
		// all the downstream work (automation lookup, session build) without
		// changing correctness. The atomic transitions below are still the
		// source of truth — this is purely an optimisation for retries.
		run, err := stores.AutomationRuns.GetByID(ctx, orgID, automationID, runID)
		if err != nil {
			return fmt.Errorf("fetch automation run: %w", err)
		}
		if run.Status != models.AutomationRunStatusPending {
			log.Info().Str("status", string(run.Status)).Msg("skipping automation_run: row no longer pending")
			return nil
		}

		automation, err := stores.Automations.GetByID(ctx, orgID, automationID)
		if err != nil {
			// GetByID filters deleted_at IS NULL, so ErrNoRows covers both
			// "truly missing" and "soft-deleted" — mark skipped so the run
			// doesn't sit pending forever. Any other error is treated as
			// transient and returned so the job can be retried.
			//
			// SoftDelete cascades to cancel pending runs in the same tx
			// (see AutomationStore.SoftDelete), so a delete committed
			// before this fetch will already have flipped the run row out
			// of pending — the !pending fast-path above catches that case.
			// This branch handles the narrow window where this worker
			// observed pending *before* SoftDelete's tx committed; the
			// CAS below would also fail safely, but skipping early avoids
			// building a session for a doomed run.
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("fetch automation: %w", err)
			}
			now := time.Now()
			summary := "automation deleted before run could start"
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusSkipped, &now, &summary); updateErr != nil {
				return fmt.Errorf("mark run skipped after automation deletion: %w", updateErr)
			}
			return nil
		}

		// Paused automations: manual Run-now is refused at the API layer, so
		// we only get here when a scheduled run slipped through before a
		// pause landed. Mark skipped so the scheduler can fire cleanly on
		// the next resume without a ghost run sitting pending.
		//
		// There is a narrow TOCTOU window between this !Enabled check and
		// the pending→running transition below: if a user pauses *between*
		// those two statements, this worker still claims the row and starts
		// a session. That's acceptable — pause does not cancel in-flight
		// runs by design (see Pause handler — it clears next_run_at but
		// leaves pending/running rows alone), and the next scheduled tick
		// simply won't fire. Folding the enabled check into the CAS would
		// require coupling automations and automation_runs tables into one
		// UPDATE, which isn't worth the complexity for a race that only
		// starts *one* extra session.
		if !automation.Enabled {
			now := time.Now()
			summary := "automation paused before run could start"
			if _, err := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusSkipped, &now, &summary); err != nil {
				return fmt.Errorf("mark run skipped after pause: %w", err)
			}
			return nil
		}

		identityScope, err := automationRunIdentityScope(run, automation)
		if err != nil {
			now := time.Now()
			summary := err.Error()
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusFailed, &now, &summary); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to mark run failed after invalid automation config snapshot")
				return fmt.Errorf("mark run failed after invalid automation config snapshot: %w", updateErr)
			}
			return nil
		}

		sessionTriggeredByUserID, err := automationExecutionUserID(automation, identityScope)
		if err != nil {
			now := time.Now()
			summary := err.Error()
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusFailed, &now, &summary); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to mark run failed after invalid automation identity")
				return fmt.Errorf("mark run failed after invalid automation identity: %w", updateErr)
			}
			return nil
		}

		// Atomic claim: pending → running. Performed BEFORE session creation
		// so a duplicate worker that loses the race never reaches the Sessions
		// or Jobs stores at all. Once we own the row (transitioned=true), any
		// later failure path uses TransitionStatusIf(running → ...) so we
		// don't accidentally overwrite a status another path already wrote.
		transitioned, err := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusRunning, nil, nil)
		if err != nil {
			return fmt.Errorf("transition run to running: %w", err)
		}
		if !transitioned {
			log.Info().Msg("skipping automation_run: lost race claiming pending row")
			return nil
		}

		agentType := models.DefaultDefaultAgentType
		if automation.AgentType != nil && *automation.AgentType != "" {
			candidate := models.AgentType(*automation.AgentType)
			if err := candidate.Validate(); err == nil {
				agentType = candidate
			} else {
				log.Warn().Err(err).Msg("invalid agent_type on automation, falling back to default")
			}
		} else if stores.Organizations != nil {
			org, err := stores.Organizations.GetByID(ctx, orgID)
			if err != nil {
				log.Warn().Err(err).Msg("failed to load org settings for automation agent fallback")
			} else if settings, err := models.ParseOrgSettings(org.Settings); err != nil {
				log.Warn().Err(err).Msg("failed to parse org settings for automation agent fallback")
			} else if settings.DefaultAgentType != "" {
				agentType = settings.DefaultAgentType
			}
		}

		var targetBranch *string
		if automation.BaseBranch != "" {
			b := automation.BaseBranch
			targetBranch = &b
		}

		// Carry the run's GoalSnapshot into PMApproach so promptSeedForSession
		// surfaces it as the synthesized issue's description. Without this the
		// agent receives an empty "Session task" seed and silently ignores any
		// conditions or invariants the user wrote in the automation goal.
		var goalSeed *string
		if strings.TrimSpace(run.GoalSnapshot) != "" {
			g := run.GoalSnapshot
			goalSeed = &g
		}

		session := &models.Session{
			OrgID:             orgID,
			AgentType:         agentType,
			Status:            "pending",
			AutonomyLevel:     models.DefaultSessionAutonomy,
			TokenMode:         "low",
			ModelOverride:     automation.ModelOverride,
			ReasoningEffort:   automation.ReasoningEffort,
			TriggeredByUserID: sessionTriggeredByUserID,
			TargetBranch:      targetBranch,
			RepositoryID:      automation.RepositoryID,
			AutomationRunID:   &runID,
			PMApproach:        goalSeed,
		}
		if err := stores.Sessions.Create(ctx, session); err != nil {
			// Session creation failed after we claimed the row — flip
			// running → failed so the UI reflects the dispatch failure
			// immediately. Conditional transition guards against the (rare)
			// case that the orchestrator's completion hook somehow already
			// fired and moved the row.
			now := time.Now()
			summary := fmt.Sprintf("failed to create agent session: %s", err)
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusRunning, models.AutomationRunStatusFailed, &now, &summary); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to mark run failed after session create failure")
			}
			return fmt.Errorf("create session: %w", err)
		}

		// Dedupe key on the run_agent enqueue: if this handler is invoked
		// twice for the same automation_run (only possible if the conditional
		// transition above somehow returned true twice — defense in depth),
		// the job store rejects the second insert and the second handler
		// returns cleanly without a duplicate agent run.
		dedupeKey := db.RunAgentDedupeKey(session.ID)
		agentPayload := db.RunAgentPayload(session)
		if _, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "run_agent", agentPayload, 5, &dedupeKey); err != nil {
			return fmt.Errorf("enqueue run_agent: %w", err)
		}

		log.Info().
			Str("session_id", session.ID.String()).
			Str("agent_type", string(agentType)).
			Msg("automation session dispatched")
		return nil
	}
}

func automationRunIdentityScope(run models.AutomationRun, automation models.Automation) (models.AutomationIdentityScope, error) {
	if len(run.ConfigSnapshot) == 0 {
		return automation.IdentityScope.OrDefault(), nil
	}

	var snapshot struct {
		IdentityScope models.AutomationIdentityScope `json:"identity_scope"`
	}
	if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
		return "", fmt.Errorf("parse automation config snapshot: %w", err)
	}
	if snapshot.IdentityScope == "" {
		return automation.IdentityScope.OrDefault(), nil
	}
	if err := snapshot.IdentityScope.Validate(); err != nil {
		return "", err
	}
	return snapshot.IdentityScope.OrDefault(), nil
}

func automationExecutionUserID(automation models.Automation, identityScope models.AutomationIdentityScope) (*uuid.UUID, error) {
	switch identityScope.OrDefault() {
	case models.AutomationIdentityScopeOrg:
		return nil, nil
	case models.AutomationIdentityScopePersonal:
		if automation.CreatedBy == nil {
			return nil, fmt.Errorf("personal automation is missing created_by; cannot resolve execution identity")
		}
		return automation.CreatedBy, nil
	default:
		return nil, fmt.Errorf("automation has invalid identity_scope %q", identityScope)
	}
}

// newOrgIDJobHandler creates a handler that unmarshals an org_id payload and
// calls the given function. Used for simple jobs like pm_bootstrap and pm_context_refresh.
func newOrgIDJobHandler(jobName string, fn func(ctx context.Context, orgID uuid.UUID) error, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal %s payload: %w", jobName, err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}

		logger.Info().Str("org_id", orgID.String()).Msgf("running %s job", jobName)
		return fn(ctx, orgID)
	}
}

// sync_sentry handler polls the Sentry API for new issues and ingests them.
func newSyncSentryHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	sentryClient := ingestion.NewSentryAPIClient(&http.Client{Timeout: 30 * time.Second}, logger)
	ingestService := ingestion.NewService(stores.Issues, stores.Webhooks, stores.Jobs, logger)

	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal sync_sentry payload: %w", err)
		}

		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}

		integrations, err := stores.Integrations.ListByOrgAndProvider(ctx, orgID, models.IntegrationProviderSentry)
		if err != nil {
			return fmt.Errorf("list sentry integrations: %w", err)
		}

		if len(integrations) == 0 {
			logger.Debug().Str("org_id", orgID.String()).Msg("no active sentry integrations found")
			return nil
		}

		for _, integ := range integrations {
			if err := syncSentryIntegration(ctx, sentryClient, ingestService, stores, integ, orgID, logger); err != nil {
				logger.Error().Err(err).
					Str("integration_id", integ.ID.String()).
					Str("org_id", orgID.String()).
					Msg("failed to sync sentry integration")
				continue
			}
		}

		return nil
	}
}

func syncSentryIntegration(
	ctx context.Context,
	client *ingestion.SentryAPIClient,
	ingestService *ingestion.Service,
	stores *Stores,
	integ models.Integration,
	orgID uuid.UUID,
	logger zerolog.Logger,
) error {
	var config struct {
		BaseURL     string `json:"base_url"`
		AuthToken   string `json:"auth_token"` // #nosec G117 -- JSON config field
		ProjectSlug string `json:"project_slug"`
	}
	if err := json.Unmarshal(integ.Config, &config); err != nil {
		return fmt.Errorf("parse integration config: %w", err)
	}

	if config.BaseURL == "" {
		config.BaseURL = "https://sentry.io"
	}

	since := time.Time{}
	if integ.LastSyncedAt != nil {
		since = *integ.LastSyncedAt
	}

	syncStart := time.Now()

	logger.Info().
		Str("integration_id", integ.ID.String()).
		Str("project_slug", config.ProjectSlug).
		Time("since", since).
		Msg("starting sentry sync")

	issues, err := client.FetchIssues(ctx, integ.ID, config.BaseURL, config.AuthToken, config.ProjectSlug, since)
	if err != nil {
		return fmt.Errorf("fetch sentry issues: %w", err)
	}

	ingestedCount := 0
	for _, ni := range issues {
		if _, err := ingestService.IngestNormalized(ctx, orgID, ni); err != nil {
			logger.Error().Err(err).
				Str("external_id", ni.ExternalID).
				Msg("failed to ingest sentry issue")
			continue
		}
		ingestedCount++
	}

	if err := stores.Integrations.UpdateLastSyncedAt(ctx, orgID, integ.ID, syncStart); err != nil {
		return fmt.Errorf("update last_synced_at: %w", err)
	}

	logger.Info().
		Str("integration_id", integ.ID.String()).
		Int("fetched", len(issues)).
		Int("ingested", ingestedCount).
		Msg("sentry sync complete")

	return nil
}

// sync_slack handler fetches recent messages from Slack channels, summarizes them,
// and stores the results in the integration config for PM context.
func newSyncSlackHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)

	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal sync_slack payload: %w", err)
		}

		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}

		integrations, err := stores.Integrations.ListByOrgAndProvider(ctx, orgID, models.IntegrationProviderSlack)
		if err != nil {
			return fmt.Errorf("list slack integrations: %w", err)
		}
		if len(integrations) == 0 {
			logger.Debug().Str("org_id", orgID.String()).Msg("no active slack integrations found")
			return nil
		}

		if stores.Credentials == nil {
			logger.Debug().Msg("credential store not available for sync_slack")
			return nil
		}

		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}

		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		if len(slackCfg.ChannelIDs) == 0 {
			logger.Debug().Str("org_id", orgID.String()).Msg("no slack channels configured")
			return nil
		}

		integ := integrations[0]
		since := time.Time{}
		if integ.LastSyncedAt != nil {
			since = *integ.LastSyncedAt
		}

		syncStart := time.Now()
		var allThreads []ingestion.SlackThreadSummary

		for _, channelID := range slackCfg.ChannelIDs {
			channelInfo, err := slackClient.FetchChannelInfo(ctx, slackCfg.AccessToken, channelID)
			if err != nil {
				logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to fetch slack channel info")
				continue
			}

			messages, err := slackClient.FetchChannelMessages(ctx, slackCfg.AccessToken, channelID, since)
			if err != nil {
				logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to fetch slack messages")
				continue
			}

			threadMap := ingestion.GroupIntoThreads(messages)

			// Build SlackThreadSummary for each thread, fetching replies as needed.
			for threadTS, threadMsgs := range threadMap {
				// Check if root message has replies we should fetch.
				if len(threadMsgs) > 0 && threadMsgs[0].ReplyCount > 0 {
					replies, err := slackClient.FetchThreadReplies(ctx, slackCfg.AccessToken, channelID, threadTS)
					if err != nil {
						logger.Warn().Err(err).Str("thread_ts", threadTS).Msg("failed to fetch thread replies")
					} else {
						threadMsgs = replies
					}
				}

				// Collect participants and find last activity time.
				seen := map[string]bool{}
				var participants []string
				var lastActivity time.Time
				for _, m := range threadMsgs {
					if !seen[m.User] {
						seen[m.User] = true
						participants = append(participants, m.User)
					}
					if ts := parseSlackTimestamp(m.Timestamp); ts.After(lastActivity) {
						lastActivity = ts
					}
				}

				msgsJSON, err := json.Marshal(threadMsgs)
				if err != nil {
					logger.Warn().Err(err).Str("thread_ts", threadTS).Msg("failed to marshal thread messages")
					continue
				}

				allThreads = append(allThreads, ingestion.SlackThreadSummary{
					ChannelID:    channelID,
					ChannelName:  channelInfo.Name,
					ThreadTS:     threadTS,
					MessageCount: len(threadMsgs),
					Participants: participants,
					LastActivity: lastActivity,
					Messages:     msgsJSON,
				})
			}
		}

		// Summarize threads with cheap LLM.
		if services != nil && services.SlackSummarizer != nil && len(allThreads) > 0 {
			allThreads, err = services.SlackSummarizer.SummarizeThreads(ctx, allThreads)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to summarize slack threads")
			}
		}

		// Store results in integration config.
		configData, err := json.Marshal(map[string]any{
			"recent_threads": allThreads,
		})
		if err != nil {
			return fmt.Errorf("marshal slack sync results: %w", err)
		}

		if err := stores.Integrations.UpdateConfig(ctx, orgID, integ.ID, configData); err != nil {
			return fmt.Errorf("update slack integration config: %w", err)
		}

		if err := stores.Integrations.UpdateLastSyncedAt(ctx, orgID, integ.ID, syncStart); err != nil {
			return fmt.Errorf("update last_synced_at: %w", err)
		}

		logger.Info().
			Str("integration_id", integ.ID.String()).
			Int("threads", len(allThreads)).
			Msg("slack sync complete")

		return nil
	}
}

func newSlackStartOrContinueSessionHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, jobPayload json.RawMessage) error {
		if stores == nil || stores.Sessions == nil || stores.SessionMessages == nil || stores.Jobs == nil ||
			stores.Credentials == nil || stores.SlackSessionLinks == nil || stores.SlackInboundEvents == nil {
			return fmt.Errorf("slack session handler dependencies are not configured")
		}
		var payload models.SlackStartSessionJobPayload
		if err := json.Unmarshal(jobPayload, &payload); err != nil {
			return fmt.Errorf("unmarshal slack start session payload: %w", err)
		}
		orgID, err := uuid.Parse(payload.OrgID)
		if err != nil {
			return fmt.Errorf("parse org_id: %w", err)
		}
		installationID, err := uuid.Parse(payload.SlackInstallationID)
		if err != nil {
			return fmt.Errorf("parse slack installation id: %w", err)
		}
		inboundID, err := uuid.Parse(payload.SlackInboundEventID)
		if err != nil {
			return fmt.Errorf("parse slack inbound event id: %w", err)
		}
		threadTS := slackStartSessionReplyThreadTS(payload)
		linkThreadTS := slackStartSessionLinkThreadTS(payload, threadTS)

		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		if payload.ChannelID == "" && payload.SlackUserID != "" {
			dmChannelID, openErr := slackClient.OpenDM(ctx, slackCfg.AccessToken, payload.SlackUserID)
			if openErr != nil {
				return fmt.Errorf("open Slack DM for app-home session: %w", openErr)
			}
			payload.ChannelID = dmChannelID
		}
		permalink := payload.Permalink
		if permalink == "" && payload.ChannelID != "" && payload.MessageTS != "" && slackSourceHasMessagePermalink(payload.Source) {
			resolved, permalinkErr := slackClient.GetPermalink(ctx, slackCfg.AccessToken, payload.ChannelID, payload.MessageTS)
			if permalinkErr != nil {
				logger.Warn().Err(permalinkErr).Str("channel_id", payload.ChannelID).Str("message_ts", payload.MessageTS).Msg("failed to resolve Slack permalink")
			} else {
				permalink = resolved
			}
		}
		threadMessages := fetchSlackThreadContext(ctx, slackClient, slackCfg.AccessToken, payload.ChannelID, threadTS, logger)
		contextFiles := fetchSlackContextFiles(ctx, slackClient, slackCfg.AccessToken, payload.FileIDs, logger)
		contextRefs := detectSlackContextReferences(payload.Text, threadMessages)

		var mappedUserID *uuid.UUID
		if stores.SlackUserLinks != nil && payload.SlackUserID != "" {
			link, linkErr := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, payload.TeamID, payload.SlackUserID)
			if linkErr == nil && link.UserID != nil {
				mappedUserID = link.UserID
			} else if linkErr != nil && !errors.Is(linkErr, pgx.ErrNoRows) {
				logger.Warn().Err(linkErr).Str("slack_user_id", payload.SlackUserID).Msg("failed to resolve Slack user mapping")
			} else if errors.Is(linkErr, pgx.ErrNoRows) && stores.Users != nil {
				if matchedID := resolveSlackUserByEmail(ctx, stores, slackClient, slackCfg.AccessToken, orgID, installationID, payload.TeamID, payload.SlackUserID, logger); matchedID != nil {
					mappedUserID = matchedID
				}
			}
		}

		existingLink, linkErr := stores.SlackSessionLinks.GetByThread(ctx, orgID, payload.TeamID, payload.ChannelID, linkThreadTS)
		if linkErr == nil && existingLink.SessionID != uuid.Nil {
			session, getErr := stores.Sessions.GetByID(ctx, orgID, existingLink.SessionID)
			if getErr != nil {
				return fmt.Errorf("get linked slack session: %w", getErr)
			}
			if slackShouldContinueLinkedSession(session.Status) {
				msg := &models.SessionMessage{
					SessionID:  session.ID,
					OrgID:      orgID,
					ThreadID:   session.PrimaryThreadID,
					UserID:     mappedUserID,
					TurnNumber: session.CurrentTurn,
					Role:       models.MessageRoleUser,
					Content:    renderSlackPrompt(payload.Text, permalink, threadMessages, contextRefs, contextFiles),
				}
				if err := stores.SessionMessages.Create(ctx, msg); err != nil {
					return fmt.Errorf("create slack follow-up message: %w", err)
				}
				scopeID := session.ID
				if session.PrimaryThreadID != nil {
					scopeID = *session.PrimaryThreadID
				}
				dedupeKey := db.ContinueSessionDedupeKey(scopeID)
				continuePayload := map[string]string{
					"org_id":     orgID.String(),
					"session_id": session.ID.String(),
				}
				if session.PrimaryThreadID != nil {
					continuePayload["thread_id"] = session.PrimaryThreadID.String()
				}
				if _, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", continuePayload, 5, &dedupeKey); err != nil {
					return fmt.Errorf("enqueue slack session continuation: %w", err)
				}
				ackText := slackSessionAckText(services, session.ID, "Continuing")
				if teamLine := slackTeamSessionLine(existingLink); teamLine != "" {
					ackText = strings.TrimSpace(ackText) + "\n\n" + teamLine
				}
				ackChannelID, ackThreadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, existingLink, threadTS)
				if posted, err := slackClient.PostMessage(ctx, slackCfg.AccessToken, ackChannelID, ackThreadTS, ackText); err != nil {
					logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to post Slack continuation acknowledgement")
				} else {
					if updateErr := stores.SlackSessionLinks.SetLatestStatusMessageTS(ctx, orgID, session.ID, posted.Timestamp); updateErr != nil {
						logger.Warn().Err(updateErr).Str("session_id", session.ID.String()).Msg("failed to save Slack continuation status timestamp")
					}
					recordSlackOutboundInChannel(ctx, stores, services, logger, existingLink, ackChannelID, posted.Timestamp, models.SlackOutboundMessageKindAck, "sent", ackText)
				}
				return stores.SlackInboundEvents.MarkProcessed(ctx, orgID, inboundID)
			}
			logger.Info().
				Str("session_id", session.ID.String()).
				Str("status", string(session.Status)).
				Msg("linked Slack session is not resumable; starting a new session for thread")
		}
		if linkErr != nil && !errors.Is(linkErr, pgx.ErrNoRows) {
			return fmt.Errorf("get slack session link: %w", linkErr)
		}

		title := slackSessionTitle(payload.Text)
		session := &models.Session{
			OrgID:             orgID,
			Origin:            models.SessionOriginSlack,
			InteractionMode:   models.SessionInteractionModeInteractive,
			ValidationPolicy:  models.SessionValidationPolicyOnSessionEnd,
			AgentType:         models.DefaultDefaultAgentType,
			Status:            models.SessionStatusPending,
			AutonomyLevel:     models.DefaultSessionAutonomy,
			TokenMode:         models.SessionTokenModeLow,
			TriggeredByUserID: mappedUserID,
			Title:             &title,
			PMApproach:        &title,
		}
		if stores.SlackChannels != nil {
			settings, settingsErr := stores.SlackChannels.GetByChannel(ctx, orgID, payload.TeamID, payload.ChannelID)
			if settingsErr == nil {
				if !stringSliceContains(settings.AllowedActions, "session") {
					if markErr := stores.SlackInboundEvents.MarkFailed(ctx, orgID, inboundID, "Slack-started sessions are not allowed in this channel"); markErr != nil {
						logger.Warn().Err(markErr).Str("channel_id", payload.ChannelID).Msg("failed to mark disallowed Slack session event failed")
					}
					logger.Warn().Str("channel_id", payload.ChannelID).Msg("Slack-started session blocked by channel settings")
					return nil
				}
				session.RepositoryID = settings.DefaultRepositoryID
				session.TargetBranch = settings.DefaultBranch
			} else if !errors.Is(settingsErr, pgx.ErrNoRows) {
				logger.Warn().Err(settingsErr).Str("channel_id", payload.ChannelID).Msg("failed to load Slack channel settings")
			}
		}
		if err := stores.Sessions.Create(ctx, session); err != nil {
			return fmt.Errorf("create slack session: %w", err)
		}
		initial := &models.SessionMessage{
			SessionID:  session.ID,
			OrgID:      orgID,
			ThreadID:   session.PrimaryThreadID,
			UserID:     mappedUserID,
			TurnNumber: 0,
			Role:       models.MessageRoleUser,
			Content:    renderSlackPrompt(payload.Text, permalink, threadMessages, contextRefs, contextFiles),
		}
		if err := stores.SessionMessages.Create(ctx, initial); err != nil {
			return fmt.Errorf("create initial slack session message: %w", err)
		}
		link := &models.SlackSessionLink{
			OrgID:                 orgID,
			SessionID:             session.ID,
			SlackInstallationID:   installationID,
			SlackTeamID:           payload.TeamID,
			SlackChannelID:        payload.ChannelID,
			SlackThreadTS:         linkThreadTS,
			SlackRootTS:           linkThreadTS,
			SlackMessagePermalink: permalink,
			SlackUserID:           payload.SlackUserID,
			MappedUserID:          mappedUserID,
			TeamSession:           mappedUserID == nil,
		}
		if err := stores.SlackSessionLinks.Upsert(ctx, link); err != nil {
			return fmt.Errorf("persist slack session link: %w", err)
		}
		if stores.SessionAttributions != nil {
			attribution := &models.SessionAttribution{
				OrgID:          orgID,
				SessionID:      session.ID,
				Source:         models.SessionAttributionSourceSlack,
				SourceMetadata: slackSessionAttributionMetadata(*link),
			}
			if err := stores.SessionAttributions.Create(ctx, attribution); err != nil {
				logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to persist Slack session attribution")
			}
		}
		dedupeKey := db.RunAgentDedupeKey(session.ID)
		if _, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "run_agent", db.RunAgentPayload(session), 5, &dedupeKey); err != nil {
			return fmt.Errorf("enqueue slack-started session: %w", err)
		}
		ackText := slackSessionAckText(services, session.ID, "Starting")
		if teamLine := slackTeamSessionLine(*link); teamLine != "" {
			ackText = strings.TrimSpace(ackText) + "\n\n" + teamLine
		}
		ackBlocks := slackSessionAckBlocks(ctx, stores, logger, orgID, installationID, payload.TeamID, payload.ChannelID, session, ackText)
		var posted ingestion.SlackPostedMessage
		var postErr error
		ackChannelID, ackThreadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, *link, threadTS)
		if len(ackBlocks) > 0 {
			posted, postErr = slackClient.PostMessageWithBlocks(ctx, slackCfg.AccessToken, ackChannelID, ackThreadTS, ackText, ackBlocks)
		} else {
			posted, postErr = slackClient.PostMessage(ctx, slackCfg.AccessToken, ackChannelID, ackThreadTS, ackText)
		}
		if postErr != nil {
			logger.Warn().Err(postErr).Str("session_id", session.ID.String()).Msg("failed to post Slack session acknowledgement")
		} else {
			if updateErr := stores.SlackSessionLinks.SetLatestStatusMessageTS(ctx, orgID, session.ID, posted.Timestamp); updateErr != nil {
				logger.Warn().Err(updateErr).Str("session_id", session.ID.String()).Msg("failed to save Slack acknowledgement status timestamp")
			}
			recordSlackOutboundInChannel(ctx, stores, services, logger, *link, ackChannelID, posted.Timestamp, models.SlackOutboundMessageKindAck, "sent", ackText)
		}
		return stores.SlackInboundEvents.MarkProcessed(ctx, orgID, inboundID)
	}
}

func slackSessionAttributionMetadata(link models.SlackSessionLink) json.RawMessage {
	metadata := map[string]any{
		"slack_team_id":           link.SlackTeamID,
		"slack_channel_id":        link.SlackChannelID,
		"slack_thread_ts":         link.SlackThreadTS,
		"slack_root_ts":           link.SlackRootTS,
		"slack_message_permalink": link.SlackMessagePermalink,
		"slack_user_id":           link.SlackUserID,
		"team_session":            link.TeamSession,
	}
	if link.MappedUserID != nil {
		metadata["mapped_user_id"] = link.MappedUserID.String()
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func newSlackSyncAppHomeHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil {
			return fmt.Errorf("slack app home dependencies are not configured")
		}
		var input struct {
			OrgID       string `json:"org_id"`
			TeamID      string `json:"team_id"`
			SlackUserID string `json:"slack_user_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack app home payload: %w", err)
		}
		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org_id: %w", err)
		}
		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		view := slackHomeView(ctx, stores, services, logger, orgID, input.TeamID, input.SlackUserID)
		if err := slackClient.PublishHome(ctx, slackCfg.AccessToken, input.SlackUserID, view); err != nil {
			return fmt.Errorf("publish slack app home: %w", err)
		}
		return nil
	}
}

func slackReplyThreadTS(threadTS string) string {
	if strings.HasPrefix(threadTS, "slash:") || strings.HasPrefix(threadTS, "app_home:") {
		return ""
	}
	return threadTS
}

func slackChannelResponseVisibility(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, teamID, channelID string) string {
	if stores == nil || stores.SlackChannels == nil || channelID == "" {
		return "thread"
	}
	settings, err := stores.SlackChannels.GetByChannel(ctx, orgID, teamID, channelID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to load Slack response visibility")
		}
		return "thread"
	}
	return slackNormalizeResponseVisibility(settings.ResponseVisibility)
}

func slackNormalizeResponseVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "dm":
		return "dm"
	default:
		return "thread"
	}
}

func slackDeliveryTargetFromVisibility(link models.SlackSessionLink, replyThreadTS, responseVisibility, dmChannelID string) (string, string, bool) {
	if slackNormalizeResponseVisibility(responseVisibility) == "dm" && link.SlackUserID != "" && dmChannelID != "" {
		return dmChannelID, "", true
	}
	return link.SlackChannelID, replyThreadTS, false
}

func slackDeliveryTarget(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, accessToken string, logger zerolog.Logger, link models.SlackSessionLink, replyThreadTS string) (string, string) {
	visibility := slackChannelResponseVisibility(ctx, stores, logger, link.OrgID, link.SlackTeamID, link.SlackChannelID)
	if visibility != "dm" || link.SlackUserID == "" {
		channelID, threadTS, _ := slackDeliveryTargetFromVisibility(link, replyThreadTS, visibility, "")
		return channelID, threadTS
	}
	dmChannelID, err := slackClient.OpenDM(ctx, accessToken, link.SlackUserID)
	if err != nil {
		logger.Warn().Err(err).Str("slack_user_id", link.SlackUserID).Msg("failed to open Slack DM; falling back to thread reply")
		channelID, threadTS, _ := slackDeliveryTargetFromVisibility(link, replyThreadTS, "thread", "")
		return channelID, threadTS
	}
	channelID, threadTS, _ := slackDeliveryTargetFromVisibility(link, replyThreadTS, visibility, dmChannelID)
	return channelID, threadTS
}

func slackStartSessionReplyThreadTS(payload models.SlackStartSessionJobPayload) string {
	if payload.ThreadTS != "" {
		return payload.ThreadTS
	}
	if slackSourceHasMessagePermalink(payload.Source) {
		return payload.MessageTS
	}
	return ""
}

func slackStartSessionLinkThreadTS(payload models.SlackStartSessionJobPayload, replyThreadTS string) string {
	if replyThreadTS != "" {
		return replyThreadTS
	}
	switch payload.Source {
	case "app_home":
		return "app_home:" + payload.MessageTS
	case "slash_command":
		return "slash:" + payload.MessageTS
	default:
		return payload.MessageTS
	}
}

func slackShouldContinueLinkedSession(status models.SessionStatus) bool {
	return status.CanAddThread()
}

func slackSourceHasMessagePermalink(source string) bool {
	return source != "slash_command" && source != "app_home"
}

func slackHomeView(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID uuid.UUID, teamID, slackUserID string) ingestion.SlackHomeView {
	pending := []db.SlackHomeHumanInputSummary{}
	recent := []db.SlackHomeSessionSummary{}
	previews := []db.SlackHomePreviewSummary{}
	automationRuns := []db.SlackHomeAutomationRunSummary{}
	memberships := []models.MembershipSummary{}
	if stores != nil && stores.SlackSessionLinks != nil {
		var err error
		pending, err = stores.SlackSessionLinks.ListPendingHumanInputsForSlackUser(ctx, orgID, teamID, slackUserID, 5)
		if err != nil {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home pending inputs")
		}
		recent, err = stores.SlackSessionLinks.ListRecentSessionsForSlackUser(ctx, orgID, teamID, slackUserID, 5)
		if err != nil {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home recent sessions")
		}
		previews, err = stores.SlackSessionLinks.ListActivePreviewsForSlackUser(ctx, orgID, teamID, slackUserID, 5)
		if err != nil {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home active previews")
		}
		automationRuns, err = stores.SlackSessionLinks.ListRecentAutomationRunsForSlackUser(ctx, orgID, teamID, slackUserID, 5)
		if err != nil {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home automation runs")
		}
	}
	if stores != nil && stores.SlackUserLinks != nil && stores.Memberships != nil {
		if link, err := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, teamID, slackUserID); err == nil && link.UserID != nil {
			if userMemberships, membershipErr := stores.Memberships.ListByUser(ctx, *link.UserID); membershipErr == nil {
				memberships = userMemberships
			} else {
				logger.Warn().Err(membershipErr).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home org memberships")
			}
		}
	}
	blocks := []ingestion.SlackBlock{
		{
			Type: "header",
			Text: &ingestion.SlackTextObject{Type: "plain_text", Text: "143"},
		},
		{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Start from Slack*\nMention <@143> in a channel or DM this app to start or continue a 143 session."},
		},
		{
			Type: "actions",
			Elements: []map[string]any{
				{"type": "button", "action_id": "slack_start_from_home", "text": map[string]string{"type": "plain_text", "text": "Start session"}, "value": "{}"},
				{"type": "button", "action_id": "slack_link_account", "text": map[string]string{"type": "plain_text", "text": "Link account"}, "value": "{}"},
			},
		},
	}
	blocks = append(blocks, slackHomePendingBlocks(services, pending)...)
	blocks = append(blocks, slackHomeRecentWorkBlock(services, recent))
	blocks = append(blocks, slackHomeActivePreviewBlocks(services, orgID, previews)...)
	blocks = append(blocks, slackHomeAutomationRunBlock(services, automationRuns), slackHomeOrgSelectorBlock(memberships, orgID),
		ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: fmt.Sprintf("*Slack connection*\nWorkspace `%s`, Slack user `%s`.", teamID, slackUserID)},
		},
	)
	return ingestion.SlackHomeView{
		Type:   "home",
		Blocks: blocks,
	}
}

func slackHomeOrgSelectorBlock(memberships []models.MembershipSummary, activeOrgID uuid.UUID) ingestion.SlackBlock {
	if len(memberships) <= 1 {
		return ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Organization*\nUsing the connected 143 organization."},
		}
	}
	var b strings.Builder
	b.WriteString("*Organization*")
	for _, membership := range memberships {
		b.WriteString("\n- ")
		if membership.OrgID == activeOrgID {
			b.WriteString("*")
			b.WriteString(membership.OrgName)
			b.WriteString("*")
		} else {
			b.WriteString(membership.OrgName)
		}
		b.WriteString(" (`")
		b.WriteString(string(membership.Role))
		b.WriteString("`)")
	}
	return ingestion.SlackBlock{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: b.String()},
	}
}

func slackHomePendingBlocks(services *Services, items []db.SlackHomeHumanInputSummary) []ingestion.SlackBlock {
	if len(items) == 0 {
		return []ingestion.SlackBlock{{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Needs your response*\nNo pending agent questions."},
		}}
	}
	var b strings.Builder
	b.WriteString("*Needs your response*")
	for _, item := range items {
		body := strings.TrimSpace(item.Body)
		if len(body) > 100 {
			body = body[:100] + "..."
		}
		b.WriteString("\n- ")
		b.WriteString(item.Title)
		if body != "" {
			b.WriteString(": ")
			b.WriteString(body)
		}
		b.WriteString(" (<")
		b.WriteString(slackSessionURL(services, item.SessionID))
		b.WriteString("|open>)")
	}
	blocks := []ingestion.SlackBlock{{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: b.String()},
		Accessory: map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": "Open oldest"},
			"url":  slackSessionURL(services, items[0].SessionID),
		},
	}}
	elements := make([]map[string]any, 0, min(len(items), 5))
	for i, item := range items {
		if i >= 5 {
			break
		}
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_answer_human_input_freeform",
			"text":      map[string]string{"type": "plain_text", "text": truncateSlackButtonText("Reply: " + item.Title)},
			"value": slackActionValue(map[string]string{
				"session_id": item.SessionID.String(),
				"request_id": item.RequestID.String(),
			}),
		})
	}
	if len(elements) > 0 {
		blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	}
	return blocks
}

func slackHomeRecentWorkBlock(services *Services, items []db.SlackHomeSessionSummary) ingestion.SlackBlock {
	if len(items) == 0 {
		return ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Active work*\nNo recent Slack-started sessions."},
		}
	}
	var b strings.Builder
	b.WriteString("*Active work*")
	for _, item := range items {
		title := "Slack session"
		if item.Title != nil && strings.TrimSpace(*item.Title) != "" {
			title = strings.TrimSpace(*item.Title)
		}
		b.WriteString("\n- ")
		b.WriteString(title)
		b.WriteString(" - ")
		b.WriteString(item.Status)
		b.WriteString(" (<")
		b.WriteString(slackSessionURL(services, item.SessionID))
		b.WriteString("|open>)")
	}
	return ingestion.SlackBlock{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: b.String()},
		Accessory: map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": "Open latest"},
			"url":  slackSessionURL(services, items[0].SessionID),
		},
	}
}

func slackHomeActivePreviewBlocks(services *Services, orgID uuid.UUID, items []db.SlackHomePreviewSummary) []ingestion.SlackBlock {
	if len(items) == 0 {
		return []ingestion.SlackBlock{{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Active previews*\nNo active previews."},
		}}
	}
	blocks := []ingestion.SlackBlock{{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Active previews*"},
	}}
	for _, item := range items {
		title := strings.TrimSpace(item.Name)
		if title == "" {
			title = "Preview"
		}
		expires := "no expiry"
		if item.ExpiresAt != nil {
			expires = item.ExpiresAt.UTC().Format(time.RFC3339)
		}
		value := slackActionValue(map[string]string{
			"org_id":     orgID.String(),
			"preview_id": item.PreviewID.String(),
		})
		blocks = append(blocks, ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: fmt.Sprintf("*%s* - %s\nExpires: `%s`\n<%s|Open in 143>", title, item.Status, expires, slackPreviewURL(services, item.PreviewID))},
			Accessory: map[string]any{
				"type":      "button",
				"action_id": "slack_open_preview",
				"text":      map[string]string{"type": "plain_text", "text": "Open preview"},
				"value":     value,
			},
		}, ingestion.SlackBlock{
			Type: "actions",
			Elements: []map[string]any{
				{"type": "button", "action_id": "slack_refresh_preview", "text": map[string]string{"type": "plain_text", "text": "Refresh"}, "value": value},
				{"type": "button", "action_id": "slack_restart_preview", "text": map[string]string{"type": "plain_text", "text": "Restart"}, "value": value},
				{"type": "button", "action_id": "slack_extend_preview", "text": map[string]string{"type": "plain_text", "text": "Extend"}, "value": value},
				{"type": "button", "action_id": "slack_stop_preview", "text": map[string]string{"type": "plain_text", "text": "Stop"}, "style": "danger", "value": value},
			},
		})
	}
	return blocks
}

func slackHomeAutomationRunBlock(services *Services, items []db.SlackHomeAutomationRunSummary) ingestion.SlackBlock {
	if len(items) == 0 {
		return ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Recent automation runs*\nNo recent automation runs."},
		}
	}
	var b strings.Builder
	b.WriteString("*Recent automation runs*")
	for _, item := range items {
		goal := strings.TrimSpace(item.GoalSnapshot)
		if len(goal) > 90 {
			goal = strings.TrimSpace(goal[:90]) + "..."
		}
		if goal == "" {
			goal = "Automation run"
		}
		b.WriteString("\n- ")
		b.WriteString(goal)
		b.WriteString(" - ")
		b.WriteString(item.Status)
		if item.SessionID != nil {
			b.WriteString(" (<")
			b.WriteString(slackSessionURL(services, *item.SessionID))
			b.WriteString("|session>)")
		}
	}
	return ingestion.SlackBlock{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: b.String()},
	}
}

func recordSlackOutboundInChannel(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, link models.SlackSessionLink, channelID, ts string, kind models.SlackOutboundMessageKind, status, text string) {
	if services != nil && services.SlackbotMetrics != nil {
		services.SlackbotMetrics.RecordOutboundMessage(ctx, string(kind), status)
	}
	if stores == nil || stores.SlackOutbound == nil || ts == "" {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	msg := &models.SlackOutboundMessage{
		OrgID:              link.OrgID,
		SlackSessionLinkID: &link.ID,
		SlackTeamID:        link.SlackTeamID,
		SlackChannelID:     channelID,
		SlackMessageTS:     ts,
		MessageKind:        kind,
		Status:             status,
		LastPayloadHash:    hash,
	}
	if err := stores.SlackOutbound.Upsert(ctx, msg); err != nil {
		logger.Warn().Err(err).Str("slack_message_ts", ts).Msg("failed to record Slack outbound message")
	}
}

func newSlackPostRunUpdateHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil || stores.SlackSessionLinks == nil {
			return fmt.Errorf("slack run update dependencies are not configured")
		}
		var input models.SlackPostRunUpdateJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack run update payload: %w", err)
		}
		orgID, sessionID, err := parseSlackSessionJobIDs(input.OrgID, input.SessionID)
		if err != nil {
			return err
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("get slack session link: %w", err)
		}
		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		text := strings.TrimSpace(input.Title)
		if input.Summary != "" {
			text += "\n" + strings.TrimSpace(input.Summary)
		}
		if input.Terminal {
			text += "\n\nSession: " + slackSessionURL(services, sessionID)
		}
		if text == "" {
			text = "143 session update"
		}
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, link, slackReplyThreadTS(link.SlackThreadTS))
		if link.LatestStatusMessageTS != nil && *link.LatestStatusMessageTS != "" {
			updateStarted := time.Now()
			if err := slackClient.UpdateMessage(ctx, slackCfg.AccessToken, channelID, *link.LatestStatusMessageTS, text); err == nil {
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "sent", time.Since(updateStarted))
				recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, *link.LatestStatusMessageTS, models.SlackOutboundMessageKindProgress, "sent", text)
				return nil
			} else {
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "failed", time.Since(updateStarted))
				logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to update Slack progress message; posting a new update")
				recordSlackAPIFailure(ctx, services, "chat.update")
			}
		}
		posted, err := slackClient.PostMessage(ctx, slackCfg.AccessToken, channelID, threadTS, text)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
		if stores.SlackSessionLinks != nil {
			if updateErr := stores.SlackSessionLinks.SetLatestStatusMessageTS(ctx, orgID, sessionID, posted.Timestamp); updateErr != nil {
				logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).Msg("failed to save Slack status message timestamp")
			}
		}
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, posted.Timestamp, models.SlackOutboundMessageKindProgress, "sent", text)
		return nil
	}
}

func newSlackPostFinalResponseHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil || stores.SlackSessionLinks == nil || stores.SessionMessages == nil {
			return fmt.Errorf("slack final response dependencies are not configured")
		}
		var input models.SlackPostFinalResponseJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack final response payload: %w", err)
		}
		orgID, sessionID, err := parseSlackSessionJobIDs(input.OrgID, input.SessionID)
		if err != nil {
			return err
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("get slack session link: %w", err)
		}
		msg, err := stores.SessionMessages.GetByID(ctx, orgID, input.FinalMessageID)
		if err != nil {
			return fmt.Errorf("get final session message: %w", err)
		}
		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		details := slackSessionOutcomeDetails{}
		if stores.Sessions != nil {
			if session, sessionErr := stores.Sessions.GetByID(ctx, orgID, sessionID); sessionErr == nil {
				details = loadSlackSessionOutcomeDetails(ctx, stores, services, logger, session)
			} else {
				logger.Warn().Err(sessionErr).Str("session_id", sessionID.String()).Msg("failed to load session outcome for Slack final response")
			}
		}
		text, blocks := renderSlackFinalBlocks(services, msg.Content, orgID, sessionID, details)
		if teamLine := slackTeamSessionLine(link); teamLine != "" {
			text = strings.TrimSpace(text) + "\n\n" + teamLine
			if len(blocks) > 0 && blocks[0].Text != nil {
				blocks[0].Text.Text = strings.TrimSpace(blocks[0].Text.Text) + "\n\n" + teamLine
			}
		}
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, link, slackReplyThreadTS(link.SlackThreadTS))
		if link.LatestStatusMessageTS != nil && *link.LatestStatusMessageTS != "" {
			terminal := "Completed\nSession: " + slackSessionURL(services, sessionID)
			updateStarted := time.Now()
			if err := slackClient.UpdateMessage(ctx, slackCfg.AccessToken, channelID, *link.LatestStatusMessageTS, terminal); err != nil {
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "failed", time.Since(updateStarted))
				logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to update Slack progress message to terminal state")
			} else {
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "sent", time.Since(updateStarted))
				recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, *link.LatestStatusMessageTS, models.SlackOutboundMessageKindProgress, "sent", terminal)
			}
		}
		posted, err := slackClient.PostMessageWithBlocks(ctx, slackCfg.AccessToken, channelID, threadTS, text, blocks)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
		if stores.SlackSessionLinks != nil {
			if updateErr := stores.SlackSessionLinks.SetFinalMessageTS(ctx, orgID, sessionID, posted.Timestamp); updateErr != nil {
				logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).Msg("failed to save Slack final message timestamp")
			}
		}
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, posted.Timestamp, models.SlackOutboundMessageKindFinal, "sent", text)
		return nil
	}
}

func newSlackDeliverHumanInputHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil || stores.SlackSessionLinks == nil || stores.HumanInputRequests == nil {
			return fmt.Errorf("slack human-input delivery dependencies are not configured")
		}
		var input models.SlackDeliverHumanInputJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack human-input delivery payload: %w", err)
		}
		orgID, sessionID, err := parseSlackSessionJobIDs(input.OrgID, input.SessionID)
		if err != nil {
			return err
		}
		requestID, err := uuid.Parse(input.RequestID)
		if err != nil {
			return fmt.Errorf("parse human input request id: %w", err)
		}
		req, err := stores.HumanInputRequests.GetByID(ctx, orgID, sessionID, requestID)
		if err != nil {
			return fmt.Errorf("get human input request: %w", err)
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("get slack session link: %w", err)
		}
		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		text, blocks := renderSlackHumanInput(services, req, sessionID)
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, link, slackReplyThreadTS(link.SlackThreadTS))
		posted, err := slackClient.PostMessageWithBlocks(ctx, slackCfg.AccessToken, channelID, threadTS, text, blocks)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, posted.Timestamp, models.SlackOutboundMessageKindHumanInput, "sent", text)
		return nil
	}
}

func newSlackSendNotificationHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil {
			return fmt.Errorf("slack notification dependencies are not configured")
		}
		var input models.SlackSendNotificationJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack notification payload: %w", err)
		}
		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org_id: %w", err)
		}
		cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		channelID := input.ChannelID
		if channelID == "" && input.SlackUserID != "" {
			channelID, err = slackClient.OpenDM(ctx, slackCfg.AccessToken, input.SlackUserID)
			if err != nil {
				return fmt.Errorf("open slack notification dm: %w", err)
			}
		}
		if channelID == "" {
			return fmt.Errorf("slack notification missing channel or user destination")
		}
		text, blocks := renderSlackNotification(services, input)
		posted, err := slackClient.PostMessageWithBlocks(ctx, slackCfg.AccessToken, channelID, input.ThreadTS, text, blocks)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
		if services != nil && services.SlackbotMetrics != nil {
			services.SlackbotMetrics.RecordOutboundMessage(ctx, string(models.SlackOutboundMessageKindNotification), "sent")
		}
		if stores.SlackOutbound != nil {
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
			var notificationID *uuid.UUID
			if input.NotificationID != "" {
				parsed, parseErr := uuid.Parse(input.NotificationID)
				if parseErr == nil {
					notificationID = &parsed
				}
			}
			msg := &models.SlackOutboundMessage{
				OrgID:           orgID,
				NotificationID:  notificationID,
				SlackTeamID:     input.TeamID,
				SlackChannelID:  channelID,
				SlackMessageTS:  posted.Timestamp,
				MessageKind:     models.SlackOutboundMessageKindNotification,
				Status:          "sent",
				LastPayloadHash: hash,
			}
			if err := stores.SlackOutbound.Upsert(ctx, msg); err != nil {
				logger.Warn().Err(err).Str("slack_message_ts", posted.Timestamp).Msg("failed to record Slack notification")
			}
		}
		return nil
	}
}

func renderSlackNotification(services *Services, input models.SlackSendNotificationJobPayload) (string, []ingestion.SlackBlock) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "143 notification"
	}
	body := strings.TrimSpace(input.Body)
	var text strings.Builder
	text.WriteString("*")
	text.WriteString(title)
	text.WriteString("*")
	if body != "" {
		text.WriteString("\n")
		text.WriteString(body)
	}
	if input.SessionID != "" {
		if sessionID, err := uuid.Parse(input.SessionID); err == nil {
			text.WriteString("\nSession: ")
			text.WriteString(slackSessionURL(services, sessionID))
		}
	}
	blocks := []ingestion.SlackBlock{{Type: "section", Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text.String()}}}
	elements := []map[string]any{}
	if input.SessionID != "" {
		sessionURL := ""
		if sessionID, err := uuid.Parse(input.SessionID); err == nil {
			sessionURL = slackSessionURL(services, sessionID)
		}
		elements = append(elements, map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": "Open session"},
			"url":  sessionURL,
		})
	}
	if input.PreviewID != "" {
		valueFields := map[string]string{"org_id": input.OrgID, "preview_id": input.PreviewID}
		if input.SessionID != "" {
			valueFields["session_id"] = input.SessionID
		}
		value, _ := json.Marshal(valueFields)
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_open_preview",
			"text":      map[string]string{"type": "plain_text", "text": "Open preview"},
			"value":     string(value),
		})
		for _, action := range []struct {
			id    string
			label string
		}{
			{"slack_refresh_preview", "Refresh"},
			{"slack_restart_preview", "Restart"},
			{"slack_stop_preview", "Stop"},
			{"slack_extend_preview", "Extend"},
		} {
			elements = append(elements, map[string]any{
				"type":      "button",
				"action_id": action.id,
				"text":      map[string]string{"type": "plain_text", "text": action.label},
				"value":     string(value),
			})
		}
	}
	if len(elements) > 0 {
		blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	}
	return text.String(), blocks
}

type slackNotificationSubscriptionConfig struct {
	Events       []string `json:"events"`
	Automations  []string `json:"automations"`
	SlackUserIDs []string `json:"slack_user_ids"`
	DMUserIDs    []string `json:"dm_user_ids"`
}

func slackNotificationSubscriptionMatches(raw json.RawMessage, eventKind string, automationID *uuid.UUID) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	cfg := parseSlackNotificationSubscriptionConfig(raw)
	if len(cfg.Events) == 0 {
		return false
	}
	if !slackNotificationEventMatches(cfg.Events, eventKind) {
		return false
	}
	if automationID != nil && len(cfg.Automations) > 0 {
		return stringSliceContains(cfg.Automations, automationID.String())
	}
	return true
}

func slackNotificationEventMatches(events []string, eventKind string) bool {
	for _, event := range events {
		event = strings.TrimSpace(event)
		if event == "*" || event == eventKind {
			return true
		}
		if strings.HasSuffix(event, ".*") {
			prefix := strings.TrimSuffix(event, "*")
			if strings.HasPrefix(eventKind, prefix) {
				return true
			}
		}
	}
	return false
}

type slackNotificationFanoutInput struct {
	EventKind    string
	Title        string
	Body         string
	SessionID    *uuid.UUID
	PreviewID    *uuid.UUID
	AutomationID *uuid.UUID
}

func enqueueSlackNotificationSubscribers(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, input slackNotificationFanoutInput) {
	if stores == nil || stores.SlackChannels == nil || stores.Jobs == nil || input.EventKind == "" {
		return
	}
	settings, err := stores.SlackChannels.ListNotificationSubscriptions(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Str("event_kind", input.EventKind).Msg("failed to list Slack notification subscriptions")
		return
	}
	for _, setting := range settings {
		if !slackNotificationSubscriptionMatches(setting.NotificationSubscriptions, input.EventKind, input.AutomationID) {
			continue
		}
		subs := parseSlackNotificationSubscriptionConfig(setting.NotificationSubscriptions)
		payload := models.SlackSendNotificationJobPayload{
			OrgID:     orgID.String(),
			Kind:      input.EventKind,
			TeamID:    setting.SlackTeamID,
			ChannelID: setting.SlackChannelID,
			Title:     input.Title,
			Body:      input.Body,
		}
		if input.SessionID != nil {
			payload.SessionID = input.SessionID.String()
		}
		if input.PreviewID != nil {
			payload.PreviewID = input.PreviewID.String()
		}
		dedupeKeyParts := []string{"slack_notification", input.EventKind, setting.SlackChannelID}
		if input.SessionID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.SessionID.String())
		}
		if input.PreviewID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.PreviewID.String())
		}
		if input.AutomationID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.AutomationID.String())
		}
		dedupeKey := strings.Join(dedupeKeyParts, ":")
		if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_send_notification", payload, 4, &dedupeKey); err != nil {
			logger.Warn().Err(err).Str("event_kind", input.EventKind).Str("slack_channel_id", setting.SlackChannelID).Msg("failed to enqueue Slack notification")
		}
		for _, slackUserID := range append(subs.SlackUserIDs, subs.DMUserIDs...) {
			slackUserID = strings.TrimSpace(slackUserID)
			if slackUserID == "" {
				continue
			}
			dmPayload := payload
			dmPayload.ChannelID = ""
			dmPayload.SlackUserID = slackUserID
			dmDedupeKey := dedupeKey + ":dm:" + slackUserID
			if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_send_notification", dmPayload, 4, &dmDedupeKey); err != nil {
				logger.Warn().Err(err).Str("event_kind", input.EventKind).Str("slack_user_id", slackUserID).Msg("failed to enqueue Slack DM notification")
			}
		}
	}
}

func parseSlackNotificationSubscriptionConfig(raw json.RawMessage) slackNotificationSubscriptionConfig {
	var cfg slackNotificationSubscriptionConfig
	if len(raw) == 0 || string(raw) == "null" {
		return cfg
	}
	_ = json.Unmarshal(raw, &cfg)
	return cfg
}

func recordSlackAPIFailure(ctx context.Context, services *Services, method string) {
	if services == nil || services.SlackbotMetrics == nil {
		return
	}
	services.SlackbotMetrics.RecordAPIFailure(ctx, method)
}

func recordSlackMessageUpdateLatency(ctx context.Context, services *Services, method, outcome string, duration time.Duration) {
	if services == nil || services.SlackbotMetrics == nil {
		return
	}
	services.SlackbotMetrics.RecordMessageUpdateLatency(ctx, method, outcome, float64(duration.Milliseconds()))
}

func enqueueSlackSessionNotifications(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, automationRunID *uuid.UUID, eventKind, title, body string) {
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind: eventKind,
		Title:     title,
		Body:      body,
		SessionID: &sessionID,
	})
	if automationRunID == nil || stores == nil || stores.AutomationRuns == nil {
		return
	}
	run, err := stores.AutomationRuns.GetByRunID(ctx, orgID, *automationRunID)
	if err != nil {
		logger.Warn().Err(err).Str("automation_run_id", automationRunID.String()).Msg("failed to load automation run for Slack notification")
		return
	}
	automationKind := "automation.run.completed"
	if eventKind == "session.failed" {
		automationKind = "automation.run.failed"
	}
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind:    automationKind,
		Title:        title,
		Body:         body,
		SessionID:    &sessionID,
		AutomationID: &run.AutomationID,
	})
	if automationKind == "automation.run.failed" {
		streak, streakErr := stores.AutomationRuns.CountConsecutiveFailures(ctx, orgID, run.AutomationID)
		if streakErr != nil {
			logger.Warn().Err(streakErr).Str("automation_id", run.AutomationID.String()).Msg("failed to count automation failure streak for Slack notification")
			return
		}
		if streak >= 3 {
			enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
				EventKind:    "automation.run.failure_streak",
				Title:        "Automation failure streak",
				Body:         fmt.Sprintf("%d consecutive automation runs failed.", streak),
				SessionID:    &sessionID,
				AutomationID: &run.AutomationID,
			})
		}
	}
}

func enqueueSlackPreviewStaleIfNeeded(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.Sessions == nil || stores.Previews == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for Slack preview stale notification")
		return
	}
	preview, err := stores.Previews.GetActivePreviewForSession(ctx, orgID, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load active preview for Slack stale notification")
		}
		return
	}
	if !slackPreviewIsStaleForSession(session, *preview) {
		return
	}
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind: "preview.stale",
		Title:     "Preview stale",
		Body:      "The session has newer workspace changes than the active preview. Refresh the preview to pick up the latest changes.",
		SessionID: &sessionID,
		PreviewID: &preview.ID,
	})
}

func slackPreviewIsStaleForSession(session models.Session, preview models.PreviewInstance) bool {
	if preview.SourceWorkspaceRevision == nil {
		return false
	}
	if session.WorkspaceRevision <= *preview.SourceWorkspaceRevision {
		return false
	}
	return preview.Status == models.PreviewStatusReady ||
		preview.Status == models.PreviewStatusPartiallyReady ||
		preview.Status == models.PreviewStatusUnhealthy
}

func renderSlackHumanInput(services *Services, req models.HumanInputRequest, sessionID uuid.UUID) (string, []ingestion.SlackBlock) {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(req.Title)
	b.WriteString("*\n")
	b.WriteString(strings.TrimSpace(req.Body))
	if len(req.Choices) > 0 {
		b.WriteString("\n\nChoices:")
		for _, choice := range req.Choices {
			b.WriteString("\n- ")
			b.WriteString(choice.ID)
			b.WriteString(": ")
			b.WriteString(choice.Label)
		}
	}
	b.WriteString("\n\nAnswer in 143 or use a Slack action.\nSession: ")
	b.WriteString(slackSessionURL(services, sessionID))
	text := b.String()
	blocks := []ingestion.SlackBlock{
		{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
		},
	}
	if len(req.Choices) > 0 {
		elements := make([]map[string]any, 0, min(len(req.Choices), 5))
		for i, choice := range req.Choices {
			if i >= 5 {
				break
			}
			value, err := json.Marshal(map[string]string{
				"session_id": sessionID.String(),
				"request_id": req.ID.String(),
				"answer":     choice.ID,
			})
			if err != nil {
				continue
			}
			label := strings.TrimSpace(choice.Label)
			if label == "" {
				label = choice.ID
			}
			elements = append(elements, map[string]any{
				"type":      "button",
				"action_id": "slack_answer_human_input",
				"text": map[string]string{
					"type": "plain_text",
					"text": truncateSlackButtonText(label),
				},
				"value": string(value),
			})
		}
		if len(elements) > 0 {
			blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
		}
	}
	blocks = append(blocks, ingestion.SlackBlock{
		Type: "actions",
		Elements: []map[string]any{
			{
				"type":      "button",
				"action_id": "slack_answer_human_input_freeform",
				"text":      map[string]string{"type": "plain_text", "text": "Reply in Slack"},
				"value": slackActionValue(map[string]string{
					"session_id": sessionID.String(),
					"request_id": req.ID.String(),
				}),
			},
			{
				"type": "button",
				"text": map[string]string{"type": "plain_text", "text": "Answer in 143"},
				"url":  slackSessionURL(services, sessionID),
			},
		},
	})
	return text, blocks
}

func truncateSlackButtonText(text string) string {
	const maxButtonText = 75
	if len(text) <= maxButtonText {
		return text
	}
	return strings.TrimSpace(text[:maxButtonText-3]) + "..."
}

func enqueueSlackHumanInputsIfPending(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.HumanInputRequests == nil || stores.SlackSessionLinks == nil || stores.Jobs == nil {
		return
	}
	if _, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID); err != nil {
		return
	}
	status := models.HumanInputRequestStatusPending
	requests, err := stores.HumanInputRequests.ListBySession(ctx, orgID, sessionID, db.HumanInputRequestFilters{Status: status})
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to list pending human-input requests for Slack delivery")
		return
	}
	for _, req := range requests {
		payload := models.SlackDeliverHumanInputJobPayload{
			OrgID:     orgID.String(),
			SessionID: sessionID.String(),
			RequestID: req.ID.String(),
		}
		dedupeKey := "slack_human_input:" + req.ID.String()
		if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_deliver_human_input", payload, 4, &dedupeKey); err != nil {
			logger.Warn().Err(err).Str("human_input_request_id", req.ID.String()).Msg("failed to enqueue Slack human-input delivery")
		}
		enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
			EventKind: "human_input.requested",
			Title:     "143 needs your response",
			Body:      strings.TrimSpace(req.Title),
			SessionID: &sessionID,
		})
	}
}

func newSlackHandleInteractionHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input models.SlackInteractionJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack interaction payload: %w", err)
		}
		if services != nil && services.SlackbotMetrics != nil {
			services.SlackbotMetrics.RecordInteractionAction(ctx, input.ActionID, "handling")
		}
		switch input.ActionID {
		case "":
			if input.CallbackID == "slack_start_session_modal" {
				return handleSlackStartSessionModal(ctx, stores, input)
			}
			if input.CallbackID == "slack_human_input_freeform_modal" {
				return handleSlackHumanInputFreeformModal(ctx, stores, services, slackClient, input)
			}
			if input.CallbackID == "slack_configure_channel_modal" {
				return handleSlackConfigureChannelModal(ctx, stores, input)
			}
			return nil
		case "slack_open_session":
			return nil
		case "slack_start_from_home":
			return handleSlackStartFromHome(ctx, stores, slackClient, input)
		case "slack_link_account":
			return handleSlackLinkAccount(ctx, stores, services, slackClient, input)
		case "slack_select_repository":
			return handleSlackSelectRepository(ctx, stores, input)
		case "slack_configure_channel":
			return handleSlackConfigureChannel(ctx, stores, slackClient, input)
		case "slack_create_preview":
			return handleSlackCreatePreview(ctx, stores, slackClient, input)
		case "slack_open_preview":
			return handleSlackOpenPreview(ctx, stores, services, slackClient, input)
		case "slack_answer_human_input":
			return handleSlackHumanInputAnswer(ctx, stores, services, slackClient, input)
		case "slack_answer_human_input_freeform":
			return handleSlackHumanInputFreeformPrompt(ctx, stores, slackClient, input)
		case "slack_member_joined_channel":
			return handleSlackMemberJoinedChannel(ctx, stores, slackClient, input)
		case "slack_refresh_preview", "slack_restart_preview", "slack_stop_preview", "slack_extend_preview":
			return handleSlackPreviewAction(ctx, stores, services, input)
		default:
			logger.Debug().Str("action_id", input.ActionID).Msg("unhandled Slack interaction action")
			return nil
		}
	}
}

func handleSlackStartFromHome(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack app-home composer dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	if input.TriggerID == "" {
		return nil
	}
	return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackStartSessionModal())
}

func slackStartSessionModal() ingestion.SlackHomeView {
	return ingestion.SlackHomeView{
		Type:       "modal",
		CallbackID: "slack_start_session_modal",
		Title:      &ingestion.SlackTextObject{Type: "plain_text", Text: "Start 143"},
		Submit:     &ingestion.SlackTextObject{Type: "plain_text", Text: "Start"},
		Close:      &ingestion.SlackTextObject{Type: "plain_text", Text: "Cancel"},
		Blocks: []ingestion.SlackBlock{{
			Type:    "input",
			BlockID: "start_prompt",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "What should 143 do?"},
			Element: map[string]any{
				"type":      "plain_text_input",
				"action_id": "prompt",
				"multiline": true,
			},
		}},
	}
}

func handleSlackStartSessionModal(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Jobs == nil {
		return fmt.Errorf("slack app-home modal dependencies are not configured")
	}
	prompt := slackModalStringValue(input.RawPayload, "start_prompt", "prompt")
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	payload := models.SlackStartSessionJobPayload{
		OrgID:               input.OrgID,
		SlackInboundEventID: input.SlackInboundEventID,
		SlackInstallationID: input.SlackInstallationID,
		TeamID:              input.TeamID,
		ChannelID:           input.ChannelID,
		ThreadTS:            "",
		MessageTS:           input.ViewID,
		SlackUserID:         input.UserID,
		Text:                prompt,
		Source:              "app_home",
	}
	dedupeKey := "slack_app_home_start:" + input.TeamID + ":" + input.UserID + ":" + input.ViewID
	_, err = stores.Jobs.Enqueue(ctx, orgID, "agent", "slack_start_or_continue_session", payload, 5, &dedupeKey)
	return err
}

func slackModalStringValue(raw json.RawMessage, blockID, actionID string) string {
	var payload struct {
		View struct {
			State struct {
				Values map[string]map[string]struct {
					Value string `json:"value"`
				} `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	actions := payload.View.State.Values[blockID]
	if actions == nil {
		return ""
	}
	return strings.TrimSpace(actions[actionID].Value)
}

func slackModalSelectedValue(raw json.RawMessage, blockID, actionID string) string {
	var payload struct {
		View struct {
			State struct {
				Values map[string]map[string]struct {
					SelectedOption struct {
						Value string `json:"value"`
					} `json:"selected_option"`
				} `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	actions := payload.View.State.Values[blockID]
	if actions == nil {
		return ""
	}
	return strings.TrimSpace(actions[actionID].SelectedOption.Value)
}

func slackModalSelectedValues(raw json.RawMessage, blockID, actionID string) []string {
	var payload struct {
		View struct {
			State struct {
				Values map[string]map[string]struct {
					SelectedOptions []struct {
						Value string `json:"value"`
					} `json:"selected_options"`
				} `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	actions := payload.View.State.Values[blockID]
	if actions == nil {
		return nil
	}
	selected := actions[actionID].SelectedOptions
	values := make([]string, 0, len(selected))
	for _, option := range selected {
		value := strings.TrimSpace(option.Value)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func handleSlackLinkAccount(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack account-link dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	integrationsURL := slackFrontendURL(services, "/integrations?slack_user_id="+url.QueryEscape(input.UserID))
	text := "Link your Slack account from 143 to enable personal approvals, DMs, and user-specific defaults.\n\nOpen 143: " + integrationsURL
	if input.TriggerID != "" {
		return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackLinkAccountModal(integrationsURL))
	}
	if input.ChannelID != "" && input.UserID != "" {
		return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, text)
	}
	return nil
}

func slackLinkAccountModal(integrationsURL string) ingestion.SlackHomeView {
	return ingestion.SlackHomeView{
		Type: "modal",
		Blocks: []ingestion.SlackBlock{
			{
				Type: "section",
				Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Link Slack to 143*\nOpen 143 integrations and link this Slack user to your 143 account."},
			},
			{
				Type: "actions",
				Elements: []map[string]any{{
					"type": "button",
					"text": map[string]string{"type": "plain_text", "text": "Open integrations"},
					"url":  integrationsURL,
				}},
			},
		},
	}
}

func handleSlackSelectRepository(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.SlackChannels == nil {
		return fmt.Errorf("slack repository selection dependencies are not configured")
	}
	var value struct {
		InstallationID string `json:"installation_id"`
		TeamID         string `json:"team_id"`
		ChannelID      string `json:"channel_id"`
		RepositoryID   string `json:"repository_id"`
		DefaultBranch  string `json:"default_branch"`
	}
	if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
		return fmt.Errorf("parse slack repository selection value: %w", err)
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	rawInstallationID := input.SlackInstallationID
	if rawInstallationID == "" {
		rawInstallationID = value.InstallationID
	}
	installationID, err := uuid.Parse(rawInstallationID)
	if err != nil {
		return fmt.Errorf("parse slack installation id: %w", err)
	}
	repoID, err := uuid.Parse(value.RepositoryID)
	if err != nil {
		return fmt.Errorf("parse repository_id: %w", err)
	}
	defaultBranch := strings.TrimSpace(value.DefaultBranch)
	var defaultBranchPtr *string
	if defaultBranch != "" {
		defaultBranchPtr = &defaultBranch
	}
	teamID := input.TeamID
	if teamID == "" {
		teamID = value.TeamID
	}
	channelID := input.ChannelID
	if channelID == "" {
		channelID = value.ChannelID
	}
	settings := &models.SlackChannelSettings{
		OrgID:                     orgID,
		SlackInstallationID:       installationID,
		SlackTeamID:               teamID,
		SlackChannelID:            channelID,
		DefaultRepositoryID:       &repoID,
		DefaultBranch:             defaultBranchPtr,
		ResponseVisibility:        "thread",
		AllowedActions:            []string{"session", "preview"},
		NotificationSubscriptions: json.RawMessage(`{}`),
		Active:                    true,
	}
	return stores.SlackChannels.Upsert(ctx, settings)
}

func handleSlackConfigureChannel(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack configure-channel dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	if input.TriggerID != "" && stores.Repositories != nil {
		repos, err := stores.Repositories.ListByOrg(ctx, orgID, db.RepositoryFilters{})
		if err != nil {
			return fmt.Errorf("list repositories for Slack channel config: %w", err)
		}
		if len(repos) == 0 {
			if input.ChannelID != "" && input.UserID != "" {
				return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, "No active repositories are connected to this 143 organization yet.")
			}
			return nil
		}
		return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackConfigureChannelModal(input, repos))
	}
	if input.ChannelID == "" || input.UserID == "" {
		return nil
	}
	return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, "Configure Slack channel defaults and notification subscriptions in 143: /integrations")
}

func slackConfigureChannelModal(input models.SlackInteractionJobPayload, repos []models.Repository) ingestion.SlackHomeView {
	if len(repos) > 100 {
		repos = repos[:100]
	}
	options := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		options = append(options, map[string]any{
			"text":  map[string]string{"type": "plain_text", "text": truncateSlackButtonText(repo.FullName)},
			"value": repo.ID.String(),
		})
	}
	return ingestion.SlackHomeView{
		Type:            "modal",
		CallbackID:      "slack_configure_channel_modal",
		PrivateMetadata: slackActionValue(map[string]string{"channel_id": input.ChannelID}),
		Title:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Configure channel"},
		Submit:          &ingestion.SlackTextObject{Type: "plain_text", Text: "Save"},
		Close:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Cancel"},
		Blocks: []ingestion.SlackBlock{{
			Type:    "input",
			BlockID: "repository",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "Default repository"},
			Element: map[string]any{
				"type":      "static_select",
				"action_id": "selected",
				"options":   options,
			},
		}, {
			Type:    "input",
			BlockID: "response_visibility",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "Response visibility"},
			Element: map[string]any{
				"type":      "static_select",
				"action_id": "selected",
				"initial_option": map[string]any{
					"text":  map[string]string{"type": "plain_text", "text": "Thread replies"},
					"value": "thread",
				},
				"options": []map[string]any{
					{"text": map[string]string{"type": "plain_text", "text": "Thread replies"}, "value": "thread"},
					{"text": map[string]string{"type": "plain_text", "text": "DM requester"}, "value": "dm"},
				},
			},
		}, {
			Type:    "input",
			BlockID: "allowed_actions",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "Allowed actions"},
			Element: map[string]any{
				"type":      "checkboxes",
				"action_id": "selected",
				"initial_options": []map[string]any{
					{"text": map[string]string{"type": "plain_text", "text": "Start sessions"}, "value": "session"},
					{"text": map[string]string{"type": "plain_text", "text": "Preview controls"}, "value": "preview"},
				},
				"options": []map[string]any{
					{"text": map[string]string{"type": "plain_text", "text": "Start sessions"}, "value": "session"},
					{"text": map[string]string{"type": "plain_text", "text": "Preview controls"}, "value": "preview"},
					{"text": map[string]string{"type": "plain_text", "text": "PR requests"}, "value": "pr_request"},
					{"text": map[string]string{"type": "plain_text", "text": "Human input"}, "value": "human_input"},
				},
			},
		}, {
			Type:     "input",
			BlockID:  "notification_events",
			Optional: true,
			Label:    &ingestion.SlackTextObject{Type: "plain_text", Text: "Notifications"},
			Element: map[string]any{
				"type":      "checkboxes",
				"action_id": "selected",
				"options": []map[string]any{
					{"text": map[string]string{"type": "plain_text", "text": "Session completed"}, "value": "session.completed"},
					{"text": map[string]string{"type": "plain_text", "text": "Session failed"}, "value": "session.failed"},
					{"text": map[string]string{"type": "plain_text", "text": "Automation completed"}, "value": "automation.run.completed"},
					{"text": map[string]string{"type": "plain_text", "text": "Automation failed"}, "value": "automation.run.failed"},
					{"text": map[string]string{"type": "plain_text", "text": "Automation failure streak"}, "value": "automation.run.failure_streak"},
					{"text": map[string]string{"type": "plain_text", "text": "All preview events (ready, failed, stale)"}, "value": "preview.*"},
				},
			},
		}},
	}
}

func handleSlackConfigureChannelModal(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.SlackChannels == nil || stores.Repositories == nil {
		return fmt.Errorf("slack configure-channel modal dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	var payload struct {
		View struct {
			PrivateMetadata string `json:"private_metadata"`
		} `json:"view"`
	}
	if err := json.Unmarshal(input.RawPayload, &payload); err != nil {
		return fmt.Errorf("parse Slack channel config modal payload: %w", err)
	}
	var metadata struct {
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &metadata); err != nil {
		return fmt.Errorf("parse Slack channel config metadata: %w", err)
	}
	repoID, err := uuid.Parse(slackModalSelectedValue(input.RawPayload, "repository", "selected"))
	if err != nil {
		return fmt.Errorf("parse selected repository: %w", err)
	}
	repo, err := stores.Repositories.GetByID(ctx, orgID, repoID)
	if err != nil {
		return fmt.Errorf("get selected repository: %w", err)
	}
	installationID, err := uuid.Parse(input.SlackInstallationID)
	if err != nil {
		return fmt.Errorf("parse slack installation id: %w", err)
	}
	defaultBranch := repo.DefaultBranch
	responseVisibility := slackModalSelectedValue(input.RawPayload, "response_visibility", "selected")
	if responseVisibility == "" {
		responseVisibility = "thread"
	}
	allowedActions := slackModalSelectedValues(input.RawPayload, "allowed_actions", "selected")
	if len(allowedActions) == 0 {
		allowedActions = []string{"session", "preview"}
	}
	notificationSubscriptions := slackNotificationSubscriptionsFromModal(input.RawPayload)
	settings := &models.SlackChannelSettings{
		OrgID:                     orgID,
		SlackInstallationID:       installationID,
		SlackTeamID:               input.TeamID,
		SlackChannelID:            metadata.ChannelID,
		DefaultRepositoryID:       &repo.ID,
		DefaultBranch:             &defaultBranch,
		ResponseVisibility:        responseVisibility,
		AllowedActions:            allowedActions,
		NotificationSubscriptions: notificationSubscriptions,
		Active:                    true,
	}
	return stores.SlackChannels.Upsert(ctx, settings)
}

func slackNotificationSubscriptionsFromModal(raw json.RawMessage) json.RawMessage {
	selected := slackModalSelectedValues(raw, "notification_events", "selected")
	if len(selected) == 0 {
		return json.RawMessage(`{}`)
	}
	events := make([]string, 0, len(selected))
	events = append(events, selected...)
	encoded, err := json.Marshal(slackNotificationSubscriptionConfig{Events: events})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func handleSlackCreatePreview(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack preview prompt dependencies are not configured")
	}
	var actionValue struct {
		SessionID string `json:"session_id"`
	}
	if input.Value != "" {
		_ = json.Unmarshal([]byte(input.Value), &actionValue)
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	if actionValue.SessionID != "" {
		if stores.SessionMessages == nil || stores.Jobs == nil || stores.Sessions == nil {
			return fmt.Errorf("slack preview creation session dependencies are not configured")
		}
		sessionID, err := uuid.Parse(actionValue.SessionID)
		if err != nil {
			return fmt.Errorf("parse session_id: %w", err)
		}
		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("get session for Slack preview creation: %w", err)
		}
		msg := &models.SessionMessage{
			SessionID:  session.ID,
			OrgID:      orgID,
			ThreadID:   session.PrimaryThreadID,
			UserID:     session.TriggeredByUserID,
			TurnNumber: session.CurrentTurn,
			Role:       models.MessageRoleUser,
			Content:    "Create or refresh a preview for this session and report the preview URL/status.",
		}
		if err := stores.SessionMessages.Create(ctx, msg); err != nil {
			return fmt.Errorf("create Slack preview request message: %w", err)
		}
		scopeID := session.ID
		if session.PrimaryThreadID != nil {
			scopeID = *session.PrimaryThreadID
		}
		dedupeKey := db.ContinueSessionDedupeKey(scopeID)
		payload := map[string]string{"org_id": orgID.String(), "session_id": session.ID.String()}
		if session.PrimaryThreadID != nil {
			payload["thread_id"] = session.PrimaryThreadID.String()
		}
		_, err = stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", payload, 5, &dedupeKey)
		return err
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	if input.TriggerID != "" {
		return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackPreviewContextModal())
	}
	if input.ChannelID != "" && input.UserID != "" {
		return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, "I need a session, PR, branch, or repository before I can create a preview.")
	}
	return nil
}

func handleSlackOpenPreview(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack open-preview dependencies are not configured")
	}
	var value struct {
		OrgID     string `json:"org_id"`
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
		return fmt.Errorf("parse slack open-preview value: %w", err)
	}
	rawOrgID := input.OrgID
	if rawOrgID == "" {
		rawOrgID = value.OrgID
	}
	orgID, err := uuid.Parse(rawOrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	previewID, err := uuid.Parse(value.PreviewID)
	if err != nil {
		return fmt.Errorf("parse preview_id: %w", err)
	}
	if err := authorizeSlackPreviewAction(ctx, stores, input, orgID); err != nil {
		return err
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	text := "Open preview: " + slackPreviewURL(services, previewID)
	if input.TriggerID != "" {
		return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackPreviewOpenModal(text))
	}
	if input.ChannelID != "" && input.UserID != "" {
		return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, text)
	}
	return nil
}

func slackPreviewOpenModal(text string) ingestion.SlackHomeView {
	return ingestion.SlackHomeView{
		Type: "modal",
		Blocks: []ingestion.SlackBlock{{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
		}},
	}
}

func slackPreviewContextModal() ingestion.SlackHomeView {
	return ingestion.SlackHomeView{
		Type: "modal",
		Blocks: []ingestion.SlackBlock{
			{
				Type: "section",
				Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Create preview*\nSend a branch, PR, session, or repository in the Slack thread so 143 can resolve the preview target."},
			},
		},
	}
}

func handleSlackPreviewAction(ctx context.Context, stores *Stores, services *Services, input models.SlackInteractionJobPayload) error {
	if services == nil || services.PreviewController == nil {
		return fmt.Errorf("slack preview control dependencies are not configured")
	}
	var value struct {
		OrgID           string `json:"org_id"`
		PreviewID       string `json:"preview_id"`
		ExtendSeconds   int64  `json:"extend_seconds"`
		LifetimeSeconds int64  `json:"lifetime_seconds"`
	}
	if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
		return fmt.Errorf("parse slack preview action value: %w", err)
	}
	rawOrgID := input.OrgID
	if rawOrgID == "" {
		rawOrgID = value.OrgID
	}
	orgID, err := uuid.Parse(rawOrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	previewID, err := uuid.Parse(value.PreviewID)
	if err != nil {
		return fmt.Errorf("parse preview_id: %w", err)
	}
	if err := authorizeSlackPreviewAction(ctx, stores, input, orgID); err != nil {
		return err
	}
	switch input.ActionID {
	case "slack_refresh_preview", "slack_restart_preview":
		return services.PreviewController.RecyclePreview(ctx, orgID, previewID)
	case "slack_stop_preview":
		return services.PreviewController.StopPreview(ctx, orgID, previewID)
	case "slack_extend_preview":
		seconds := value.ExtendSeconds
		if seconds == 0 {
			seconds = value.LifetimeSeconds
		}
		if seconds <= 0 {
			seconds = int64((2 * time.Hour).Seconds())
		}
		_, err := services.PreviewController.SetLifetime(ctx, orgID, previewID, time.Duration(seconds)*time.Second)
		return err
	default:
		return nil
	}
}

func authorizeSlackPreviewAction(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload, orgID uuid.UUID) error {
	if stores == nil {
		return fmt.Errorf("slack preview action dependencies are not configured")
	}
	var value struct {
		SessionID string `json:"session_id"`
	}
	if input.Value != "" {
		_ = json.Unmarshal([]byte(input.Value), &value)
	}
	isOriginatingTeamSession := false
	if value.SessionID != "" && stores.SlackSessionLinks != nil {
		sessionID, err := uuid.Parse(value.SessionID)
		if err != nil {
			return fmt.Errorf("parse slack preview action session_id: %w", err)
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load slack session link for preview action: %w", err)
		} else if err == nil {
			isOriginatingTeamSession = link.TeamSession &&
				link.SlackTeamID == input.TeamID &&
				link.SlackChannelID == input.ChannelID
		}
	}
	authorizer := slackbotsvc.NewAuthorizer(stores.SlackUserLinks, stores.Memberships, stores.SlackChannels)
	_, err := authorizer.Authorize(ctx, slackbotsvc.ActionRequest{
		OrgID:                    orgID,
		TeamID:                   input.TeamID,
		ChannelID:                input.ChannelID,
		SlackUserID:              input.UserID,
		Capability:               slackbotsvc.CapabilityPreview,
		AllowedRoles:             []models.Role{models.RoleAdmin, models.RoleMember, models.RoleBuilder},
		RequireMapped:            !isOriginatingTeamSession,
		AllowUnmappedTeamSession: true,
		IsOriginatingTeamSession: isOriginatingTeamSession,
	})
	return err
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func handleSlackMemberJoinedChannel(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack channel setup dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	text := "143 is available in this channel.\n\nMention me to ask coding questions, start work, or create previews."
	blocks := []ingestion.SlackBlock{
		{Type: "section", Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text}},
		{
			Type: "actions",
			Elements: []map[string]any{
				{"type": "button", "action_id": "slack_configure_channel", "text": map[string]string{"type": "plain_text", "text": "Configure channel"}, "value": "{}"},
				{"type": "button", "action_id": "slack_start_from_home", "text": map[string]string{"type": "plain_text", "text": "Open App Home"}, "value": "{}"},
			},
		},
	}
	_, err = slackClient.PostMessageWithBlocks(ctx, slackCfg.AccessToken, input.ChannelID, "", text, blocks)
	return err
}

func handleSlackHumanInputFreeformPrompt(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack human-input freeform dependencies are not configured")
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	cred, err := stores.Credentials.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("get slack credentials: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("unexpected slack credential type")
	}
	if input.TriggerID == "" {
		return nil
	}
	return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackHumanInputFreeformModal(input.Value))
}

func slackHumanInputFreeformModal(privateMetadata string) ingestion.SlackHomeView {
	return ingestion.SlackHomeView{
		Type:            "modal",
		CallbackID:      "slack_human_input_freeform_modal",
		PrivateMetadata: privateMetadata,
		Title:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Reply to 143"},
		Submit:          &ingestion.SlackTextObject{Type: "plain_text", Text: "Send"},
		Close:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Cancel"},
		Blocks: []ingestion.SlackBlock{{
			Type:    "input",
			BlockID: "answer",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "Answer"},
			Element: map[string]any{
				"type":      "plain_text_input",
				"action_id": "value",
				"multiline": true,
			},
		}},
	}
}

func handleSlackHumanInputFreeformModal(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	var payload struct {
		View struct {
			PrivateMetadata string `json:"private_metadata"`
		} `json:"view"`
	}
	if err := json.Unmarshal(input.RawPayload, &payload); err != nil {
		return fmt.Errorf("parse Slack human-input modal payload: %w", err)
	}
	var value struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &value); err != nil {
		return fmt.Errorf("parse Slack human-input modal metadata: %w", err)
	}
	answer := slackModalStringValue(input.RawPayload, "answer", "value")
	input.Value = slackActionValue(map[string]string{
		"session_id": value.SessionID,
		"request_id": value.RequestID,
		"answer":     answer,
	})
	return handleSlackHumanInputAnswer(ctx, stores, services, slackClient, input)
}

func handleSlackHumanInputAnswer(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.HumanInputRequests == nil || stores.SlackUserLinks == nil || stores.Jobs == nil {
		return fmt.Errorf("slack human-input dependencies are not configured")
	}
	var value struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
		return fmt.Errorf("parse slack human-input action value: %w", err)
	}
	orgID, sessionID, err := parseSlackSessionJobIDs(input.OrgID, value.SessionID)
	if err != nil {
		return err
	}
	requestID, err := uuid.Parse(value.RequestID)
	if err != nil {
		return fmt.Errorf("parse human input request id: %w", err)
	}
	link, err := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, input.TeamID, input.UserID)
	if err != nil {
		return fmt.Errorf("resolve slack user link for human input: %w", err)
	}
	if link.UserID == nil {
		return fmt.Errorf("slack user is not linked to a 143 user")
	}
	answer := strings.TrimSpace(value.Answer)
	req, err := stores.HumanInputRequests.AnswerPending(ctx, orgID, sessionID, requestID, &answer, nil, *link.UserID)
	if err != nil {
		return fmt.Errorf("answer human input request from Slack: %w", err)
	}
	if stores.Credentials != nil && input.ChannelID != "" && input.MessageTS != "" {
		if cred, credErr := stores.Credentials.Get(ctx, orgID, models.ProviderSlack); credErr == nil {
			if slackCfg, ok := cred.Config.(models.SlackConfig); ok {
				updateText := fmt.Sprintf("Answered by <@%s>: %s", input.UserID, answer)
				updateStarted := time.Now()
				if err := slackClient.UpdateMessage(ctx, slackCfg.AccessToken, input.ChannelID, input.MessageTS, updateText); err != nil {
					recordSlackMessageUpdateLatency(ctx, services, "chat.update", "failed", time.Since(updateStarted))
					return fmt.Errorf("update Slack human-input message: %w", err)
				}
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "sent", time.Since(updateStarted))
			}
		} else {
			return fmt.Errorf("get slack credentials: %w", credErr)
		}
	}
	scopeID := sessionID
	if req.ThreadID != nil {
		scopeID = *req.ThreadID
	}
	dedupeKey := db.ContinueSessionDedupeKey(scopeID)
	payload := map[string]string{
		"org_id":                 orgID.String(),
		"session_id":             sessionID.String(),
		"human_input_request_id": requestID.String(),
	}
	if req.ThreadID != nil {
		payload["thread_id"] = req.ThreadID.String()
	}
	_, err = stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", payload, 5, &dedupeKey)
	return err
}

func parseSlackSessionJobIDs(orgIDRaw, sessionIDRaw string) (uuid.UUID, uuid.UUID, error) {
	orgID, err := uuid.Parse(orgIDRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse org_id: %w", err)
	}
	sessionID, err := uuid.Parse(sessionIDRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse session_id: %w", err)
	}
	return orgID, sessionID, nil
}

func renderSlackFinal(services *Services, content string, sessionID uuid.UUID) string {
	trimmed := strings.TrimSpace(content)
	const maxSlackFinal = 2400
	if len(trimmed) > maxSlackFinal {
		trimmed = strings.TrimSpace(trimmed[:maxSlackFinal]) + "\n\n[Truncated in Slack]"
	}
	if trimmed == "" {
		trimmed = "143 session completed."
	}
	return trimmed + "\n\nSession: " + slackSessionURL(services, sessionID)
}

func renderSlackFinalBlocks(services *Services, content string, orgID, sessionID uuid.UUID, details slackSessionOutcomeDetails) (string, []ingestion.SlackBlock) {
	text := appendSlackSessionOutcomeDetails(renderSlackFinal(services, content, sessionID), details)
	blocks := []ingestion.SlackBlock{{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
	}}
	elements := []map[string]any{{
		"type": "button",
		"text": map[string]string{"type": "plain_text", "text": "Open session"},
		"url":  slackSessionURL(services, sessionID),
	}}
	if details.Preview != nil {
		value := slackActionValue(map[string]string{
			"org_id":     orgID.String(),
			"session_id": sessionID.String(),
			"preview_id": details.Preview.ID.String(),
		})
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_open_preview",
			"text":      map[string]string{"type": "plain_text", "text": "Open preview"},
			"value":     value,
		})
	} else {
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_create_preview",
			"text":      map[string]string{"type": "plain_text", "text": "Create preview"},
			"value": slackActionValue(map[string]string{
				"org_id":     orgID.String(),
				"session_id": sessionID.String(),
			}),
		})
	}
	if details.PullRequest != nil && strings.TrimSpace(details.PullRequest.GitHubPRURL) != "" {
		elements = append(elements, map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": "Open PR"},
			"url":  strings.TrimSpace(details.PullRequest.GitHubPRURL),
		})
	}
	blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	return text, blocks
}

type slackSessionOutcomeDetails struct {
	Session     models.Session
	PullRequest *models.PullRequest
	Preview     *models.PreviewInstance
	PreviewURL  string
}

func loadSlackSessionOutcomeDetails(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session) slackSessionOutcomeDetails {
	details := slackSessionOutcomeDetails{Session: session}
	if stores == nil {
		return details
	}
	if stores.PullRequests != nil {
		pr, err := stores.PullRequests.GetBySessionID(ctx, session.OrgID, session.ID)
		if err == nil {
			details.PullRequest = &pr
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load pull request for Slack final response")
		}
	}
	if stores.Previews != nil {
		preview, err := stores.Previews.GetActivePreviewForSession(ctx, session.OrgID, session.ID)
		if err != nil && errors.Is(err, pgx.ErrNoRows) {
			preview, err = stores.Previews.GetLatestFailedPreviewForSession(ctx, session.OrgID, session.ID)
		}
		if err != nil && errors.Is(err, pgx.ErrNoRows) {
			preview, err = stores.Previews.GetLatestTerminalPreviewForSession(ctx, session.OrgID, session.ID)
		}
		if err == nil && preview != nil {
			details.Preview = preview
			details.PreviewURL = slackPreviewURL(services, preview.ID)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load preview for Slack final response")
		}
	}
	return details
}

func appendSlackSessionOutcomeDetails(text string, details slackSessionOutcomeDetails) string {
	session := details.Session
	lines := []string{}
	if session.BranchURL != nil && strings.TrimSpace(*session.BranchURL) != "" {
		lines = append(lines, "Branch: "+strings.TrimSpace(*session.BranchURL))
	}
	if details.PullRequest != nil && strings.TrimSpace(details.PullRequest.GitHubPRURL) != "" {
		lines = append(lines, slackPullRequestOutcomeLine(*details.PullRequest))
	} else if session.PRCreationState == models.PRCreationStateSucceeded {
		lines = append(lines, "PR: opened")
	} else if session.PRCreationState == models.PRCreationStateFailed && session.PRCreationError != nil && strings.TrimSpace(*session.PRCreationError) != "" {
		lines = append(lines, "PR: failed - "+strings.TrimSpace(*session.PRCreationError))
	}
	if details.Preview != nil && strings.TrimSpace(details.PreviewURL) != "" {
		lines = append(lines, fmt.Sprintf("Preview: %s - %s", details.Preview.Status, details.PreviewURL))
	}
	if line := slackDiffStatsOutcomeLine(session.DiffStats); line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return text
	}
	return strings.TrimSpace(text) + "\n\n" + strings.Join(lines, "\n")
}

func slackTeamSessionLine(link models.SlackSessionLink) string {
	if !link.TeamSession {
		return ""
	}
	return "_This is a team session started from Slack without a linked 143 user._"
}

func slackPullRequestOutcomeLine(pr models.PullRequest) string {
	line := "PR: " + strings.TrimSpace(pr.GitHubPRURL)
	metadata := []string{}
	if pr.Status != "" {
		metadata = append(metadata, string(pr.Status))
	}
	if pr.CIStatus != "" {
		metadata = append(metadata, "CI "+string(pr.CIStatus))
	}
	if len(metadata) > 0 {
		line += " (" + strings.Join(metadata, ", ") + ")"
	}
	return line
}

func slackDiffStatsOutcomeLine(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var stats struct {
		FilesChanged int `json:"files_changed"`
		Added        int `json:"added"`
		Removed      int `json:"removed"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return "Changes: diff available in 143"
	}
	if stats.FilesChanged == 0 && stats.Added == 0 && stats.Removed == 0 {
		return ""
	}
	fileLabel := "files"
	if stats.FilesChanged == 1 {
		fileLabel = "file"
	}
	return fmt.Sprintf("Changes: %d %s, +%d/-%d", stats.FilesChanged, fileLabel, stats.Added, stats.Removed)
}

func fetchSlackThreadContext(ctx context.Context, client *ingestion.SlackAPIClient, accessToken, channelID, threadTS string, logger zerolog.Logger) []ingestion.SlackMessage {
	if channelID == "" || threadTS == "" {
		return nil
	}
	messages, err := client.FetchThreadReplies(ctx, accessToken, channelID, threadTS)
	if err != nil {
		logger.Warn().Err(err).Str("channel_id", channelID).Str("thread_ts", threadTS).Msg("failed to fetch Slack thread context")
		return nil
	}
	if len(messages) > 20 {
		messages = messages[len(messages)-20:]
	}
	return messages
}

type slackContextFile struct {
	Name      string
	Title     string
	Mimetype  string
	Permalink string
}

func renderSlackPrompt(text, permalink string, threadMessages []ingestion.SlackMessage, references []slackContextReference, files []slackContextFile) string {
	cleaned := strings.TrimSpace(text)
	var b strings.Builder
	b.WriteString(cleaned)
	if permalink != "" {
		b.WriteString("\n\nSlack thread: ")
		b.WriteString(permalink)
	}
	if len(references) > 0 {
		b.WriteString("\n\nDetected references:")
		for _, ref := range references {
			b.WriteString("\n- ")
			b.WriteString(string(ref.Kind))
			b.WriteString(": ")
			b.WriteString(ref.Value)
		}
	}
	if len(files) > 0 {
		b.WriteString("\n\nAttached files:")
		for _, file := range files {
			label := strings.TrimSpace(file.Title)
			if label == "" {
				label = strings.TrimSpace(file.Name)
			}
			if label == "" {
				label = "Slack file"
			}
			b.WriteString("\n- ")
			b.WriteString(label)
			if file.Name != "" && file.Name != label {
				b.WriteString(" [")
				b.WriteString(file.Name)
				b.WriteString("]")
			}
			if file.Mimetype != "" {
				b.WriteString(" (")
				b.WriteString(file.Mimetype)
				b.WriteString(")")
			}
			if file.Permalink != "" {
				b.WriteString(": ")
				b.WriteString(file.Permalink)
			}
		}
	}
	if len(threadMessages) > 0 {
		b.WriteString("\n\nRecent Slack thread context:\n")
		for _, msg := range threadMessages {
			line := strings.TrimSpace(msg.Text)
			if line == "" {
				continue
			}
			if len(line) > 500 {
				line = line[:500] + "..."
			}
			b.WriteString("- ")
			if msg.User != "" {
				b.WriteString("<@")
				b.WriteString(msg.User)
				b.WriteString(">: ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func fetchSlackContextFiles(ctx context.Context, client *ingestion.SlackAPIClient, accessToken string, fileIDs []string, logger zerolog.Logger) []slackContextFile {
	if len(fileIDs) == 0 {
		return nil
	}
	if len(fileIDs) > 10 {
		fileIDs = fileIDs[:10]
	}
	files := make([]slackContextFile, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		file, err := client.FetchFileInfo(ctx, accessToken, fileID)
		if err != nil {
			logger.Warn().Err(err).Str("slack_file_id", fileID).Msg("failed to fetch Slack file metadata")
			continue
		}
		files = append(files, slackContextFile{
			Name:      file.Name,
			Title:     file.Title,
			Mimetype:  file.Mimetype,
			Permalink: file.Permalink,
		})
	}
	return files
}

type slackReferenceKind string

const (
	slackReferenceKindURL         slackReferenceKind = "url"
	slackReferenceKindRepository  slackReferenceKind = "repository"
	slackReferenceKindPullRequest slackReferenceKind = "pull_request"
	slackReferenceKindIssue       slackReferenceKind = "issue"
	slackReferenceKindSentry      slackReferenceKind = "sentry"
	slackReferenceKindPreview     slackReferenceKind = "preview"
	slackReferenceKindBranch      slackReferenceKind = "branch"
	slackReferenceKindFilePath    slackReferenceKind = "file_path"
)

type slackContextReference struct {
	Kind  slackReferenceKind
	Value string
}

var (
	slackURLReferencePattern      = regexp.MustCompile(`https?://[^\s<>()]+`)
	slackFilePathReferencePattern = regexp.MustCompile(`(?:^|[\s(])((?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.[A-Za-z0-9]+(?::[0-9]+)?)`)
	slackRepoIssuePattern         = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_.-]+#[0-9]+\b`)
	slackLinearIssuePattern       = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)
	slackBranchPattern            = regexp.MustCompile(`(?i)\bbranch\s+([A-Za-z0-9._/-]+)`)
)

func detectSlackContextReferences(text string, threadMessages []ingestion.SlackMessage) []slackContextReference {
	seen := map[string]bool{}
	refs := []slackContextReference{}
	addRef := func(kind slackReferenceKind, value string) {
		value = strings.TrimRight(strings.TrimSpace(value), ".,)")
		if value == "" || seen[string(kind)+":"+value] {
			return
		}
		seen[string(kind)+":"+value] = true
		refs = append(refs, slackContextReference{Kind: kind, Value: value})
	}
	addRefs := func(input string) {
		for _, rawURL := range slackURLReferencePattern.FindAllString(input, -1) {
			urlRef := strings.TrimRight(rawURL, ".,)")
			kind := classifySlackURLReference(urlRef)
			addRef(kind, urlRef)
			if len(refs) >= 20 {
				return
			}
		}
		for _, ref := range slackLinearIssuePattern.FindAllString(input, -1) {
			addRef(slackReferenceKindIssue, ref)
			if len(refs) >= 20 {
				return
			}
		}
		for _, ref := range slackRepoIssuePattern.FindAllString(input, -1) {
			addRef(slackReferenceKindIssue, ref)
			if len(refs) >= 20 {
				return
			}
		}
		for _, match := range slackBranchPattern.FindAllStringSubmatch(input, -1) {
			if len(match) > 1 {
				addRef(slackReferenceKindBranch, match[1])
			}
			if len(refs) >= 20 {
				return
			}
		}
		for _, match := range slackFilePathReferencePattern.FindAllStringSubmatch(input, -1) {
			if len(match) > 1 {
				addRef(slackReferenceKindFilePath, match[1])
			}
			if len(refs) >= 20 {
				return
			}
		}
	}
	addRefs(text)
	for _, msg := range threadMessages {
		if len(refs) >= 20 {
			break
		}
		addRefs(msg.Text)
	}
	return refs
}

func classifySlackURLReference(value string) slackReferenceKind {
	lowered := strings.ToLower(value)
	switch {
	case strings.Contains(lowered, "sentry.io/"):
		return slackReferenceKindSentry
	case strings.Contains(lowered, "/pull/"):
		return slackReferenceKindPullRequest
	case strings.Contains(lowered, "/issues/"):
		return slackReferenceKindIssue
	case strings.Contains(lowered, "/previews/") || strings.Contains(lowered, "preview."):
		return slackReferenceKindPreview
	case strings.Contains(lowered, "github.com/"):
		return slackReferenceKindRepository
	default:
		return slackReferenceKindURL
	}
}

func resolveSlackUserByEmail(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, accessToken string, orgID, installationID uuid.UUID, teamID, slackUserID string, logger zerolog.Logger) *uuid.UUID {
	slackUser, err := slackClient.FetchUserInfo(ctx, accessToken, slackUserID)
	if err != nil {
		logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to fetch Slack user info for email mapping")
		return nil
	}
	email := strings.TrimSpace(slackUser.Profile.Email)
	if email == "" {
		return nil
	}
	user, err := stores.Users.GetByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to resolve Slack email to 143 user")
		}
		return nil
	}
	if user.OrgID != orgID {
		return nil
	}
	displayName := strings.TrimSpace(slackUser.Profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(slackUser.RealName)
	}
	link := &models.SlackUserLink{
		OrgID:               orgID,
		SlackInstallationID: installationID,
		UserID:              &user.ID,
		SlackTeamID:         teamID,
		SlackUserID:         slackUserID,
		SlackEmail:          &email,
		SlackDisplayName:    displayName,
	}
	if err := stores.SlackUserLinks.UpsertEmailMatch(ctx, link); err != nil {
		logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to persist Slack email match")
	}
	return &user.ID
}

func slackSessionTitle(text string) string {
	cleaned := strings.TrimSpace(text)
	cleaned = regexp.MustCompile(`<@[^>]+>`).ReplaceAllString(cleaned, "")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if cleaned == "" {
		return "Slack Session"
	}
	if len(cleaned) > 80 {
		return strings.TrimSpace(cleaned[:80])
	}
	return cleaned
}

func slackSessionAckText(services *Services, sessionID uuid.UUID, verb string) string {
	return fmt.Sprintf("%s a 143 session for this Slack thread...\n\nSession: %s", verb, slackSessionURL(services, sessionID))
}

func slackSessionAckBlocks(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, installationID uuid.UUID, teamID, channelID string, session *models.Session, text string) []ingestion.SlackBlock {
	blocks := []ingestion.SlackBlock{{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
	}}
	if session == nil || session.RepositoryID != nil || stores == nil || stores.Repositories == nil {
		return blocks
	}
	repos, err := stores.Repositories.ListByOrg(ctx, orgID, db.RepositoryFilters{})
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to list repositories for Slack repository selector")
		return blocks
	}
	if len(repos) == 0 {
		return blocks
	}
	if len(repos) > 5 {
		repos = repos[:5]
	}
	elements := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_select_repository",
			"text":      map[string]string{"type": "plain_text", "text": truncateSlackButtonText(repo.FullName)},
			"value": slackActionValue(map[string]string{
				"installation_id": installationID.String(),
				"team_id":         teamID,
				"channel_id":      channelID,
				"repository_id":   repo.ID.String(),
				"default_branch":  repo.DefaultBranch,
				"session_id":      session.ID.String(),
			}),
		})
	}
	blocks = append(blocks, ingestion.SlackBlock{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "I can keep going, but selecting a default repository will make this Slack thread more precise."},
	}, ingestion.SlackBlock{Type: "actions", Elements: elements})
	return blocks
}

func slackSessionURL(services *Services, sessionID uuid.UUID) string {
	return slackFrontendURL(services, "/sessions/"+sessionID.String())
}

func slackPreviewURL(services *Services, previewID uuid.UUID) string {
	return slackFrontendURL(services, "/previews/"+previewID.String())
}

func slackFrontendURL(services *Services, path string) string {
	base := ""
	if services != nil {
		base = strings.TrimRight(strings.TrimSpace(services.FrontendURL), "/")
	}
	if base == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func slackActionValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// parseSlackTimestamp parses a Slack message timestamp (e.g. "1678901234.567890")
// into a time.Time. Returns zero time on parse failure.
func parseSlackTimestamp(ts string) time.Time {
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// run_agent handler executes an agent run end-to-end via the orchestrator.
func newRunAgentHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID           string `json:"session_id"`
			OrgID               string `json:"org_id"`
			ThreadID            string `json:"thread_id"`
			HumanInputRequestID string `json:"human_input_request_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal run_agent payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.Sessions.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}
		if input.ThreadID != "" {
			threadID, parseErr := uuid.Parse(input.ThreadID)
			if parseErr != nil {
				logger.Warn().Err(parseErr).Str("thread_id", input.ThreadID).Msg("invalid thread_id in run_agent payload")
			} else {
				run.PrimaryThreadID = &threadID
			}
		} else if stores.SessionThreads != nil {
			threads, listErr := stores.SessionThreads.ListBySession(ctx, orgID, runID)
			if listErr != nil {
				logger.Warn().Err(listErr).Str("session_id", runID.String()).Msg("failed to load primary thread for run_agent")
			} else if len(threads) > 0 {
				threadID := threads[0].ID
				run.PrimaryThreadID = &threadID
			}
		}

		// Gate turn 1 on Linear pre-start preparation. If a session linked a
		// Linear primary issue but its context isn't ready, hold the run in
		// pending and retry — design 62 §"Pre-start preparation step" makes
		// this strictly stronger than "link it later if we can": the first
		// agent turn either has the linked issue's details or doesn't start.
		switch run.LinearPrepareState {
		case models.LinearPrepareStatePending:
			// Try later; the prepare worker will mark this ready or failed.
			// The fixed 5s wait avoids busy-spinning the queue against
			// exponential backoff — design 62 promises a short-lived
			// preparing state, not minutes-long waits.
			d := 5 * time.Second
			return &RetryableError{
				Err:        fmt.Errorf("waiting for linear pre-start preparation"),
				RetryAfter: &d,
			}
		case models.LinearPrepareStateFailed:
			// Don't start blind. Surface to the user via the recoverable
			// failure path so they can retry.
			errMsg := "Linear context could not be loaded. Retry the session to fetch it again."
			if run.Error != nil && strings.TrimSpace(*run.Error) != "" {
				errMsg = *run.Error
			}
			_ = stores.Sessions.UpdateResult(ctx, orgID, runID, models.SessionStatusFailed, &models.SessionResult{Error: &errMsg})
			return &FatalError{Err: fmt.Errorf("linear pre-start preparation failed")}
		}

		if err := maybeDispatchSessionExecutor(ctx, services, jobType, run, run.PrimaryThreadID); err != nil {
			return err
		}

		// Apply the per-session wall-clock timeout at the handler boundary so
		// the orchestrator exits cleanly when a container is killed or
		// ExecStream hangs. HandlerCleanupBuffer gives the orchestrator slack
		// to stop the container, snapshot, and persist the failed status
		// after the session timeout expires.
		sessionTimeout := services.Orchestrator.ResolveSessionTimeout(ctx, orgID)
		runtimeCeiling := services.Orchestrator.ResolveAbsoluteRuntimeCeiling(ctx, orgID)
		jobCtx, cancel := context.WithTimeout(ctx, runtimeCeiling+agent.HandlerCleanupBuffer)
		defer cancel()

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Dur("session_timeout", sessionTimeout).
			Dur("runtime_ceiling", runtimeCeiling).
			Msg("starting run_agent job")
		enqueueSlackRunUpdateIfLinked(ctx, stores, logger, orgID, runID, "running", "143 is working on this", "I will post the result back in this thread.", false)

		var runErr error
		switch models.SessionStatus(run.Status) {
		case models.SessionStatusRunning:
			runErr = services.Orchestrator.RecoverSession(jobCtx, &run)
		default:
			runErr = services.Orchestrator.RunAgent(jobCtx, &run)
		}
		if err := runErr; err != nil {
			if errors.Is(err, agent.ErrSandboxCapacity) {
				retryAfter := sandboxCapacityRetryDelay
				targetNodeID, clearTargetNodeID := sandboxCapacityRetryTarget(ctx, stores, logger)
				registerSandboxCapacityDeadLetter(ctx, stores, services, logger, run, run.PrimaryThreadID, "run_agent")
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("local sandbox capacity reached; retrying run_agent")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: targetNodeID, ClearTargetNodeID: clearTargetNodeID}
			}
			if errors.Is(err, agent.ErrRecoveryAttemptsExhausted) {
				logger.Warn().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent recovery exhausted; dead-lettering without another restart")
				return &FatalError{Err: err}
			}
			if errors.Is(err, agent.ErrSessionInterrupted) {
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent interrupted by system stop; retrying turn")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrThreadRuntimeAlreadyActive) {
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("thread runtime already active; retrying after lease recovery")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrSandboxOnDifferentNode) {
				retryAfter := 5 * time.Second
				targetNodeID := models.SessionWorkerTarget(&run)
				logEvent := logger.Info().
					Str("session_id", runID.String()).
					Err(err)
				if targetNodeID != nil {
					logEvent = logEvent.Str("target_node_id", *targetNodeID)
				}
				logEvent.Msg("run_agent recovery claimed on the wrong node; releasing for the recorded sandbox worker")
				return &RetryableError{
					Err:                    err,
					RetryAfter:             &retryAfter,
					BypassMaxRetryDuration: true,
					TargetNodeID:           targetNodeID,
				}
			}
			if errors.Is(err, agent.ErrStaleSandboxIDCleared) {
				// The orchestrator detected a stale orphan container_id from
				// a crashed prior worker, CAS-cleared it, and signaled retry.
				// Requeue without consuming an attempt — the next attempt
				// sees a clean row and creates a fresh sandbox. A short
				// backoff lets any in-flight cleanup settle.
				retryAfter := 2 * time.Second
				registerStaleSandboxDeadLetter(ctx, stores, logger, run, run.PrimaryThreadID, "run_agent")
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent cleared stale orphan container_id; retrying against the clean row")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrSandboxPreviewRace) {
				// A preview hydrate published the live container first. There is
				// no winning agent job to publish a terminal result, so retry
				// after the orchestrator reverted the session back to pending.
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent lost sandbox publish race to preview; retrying against session state")
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
			}
			if errors.Is(err, agent.ErrSandboxRaceLoser) {
				// A duplicate run_agent job lost the AcquireTurnHold race to
				// the winner that owns the session row. The winner will
				// publish the authoritative result; this duplicate must
				// dead-letter immediately (every retry would lose the same
				// race) without surfacing a user-visible failure.
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("duplicate run_agent job lost sandbox-hold race; dead-lettering silently — winner retains the session row")
				return &FatalError{Err: err}
			}
			if errors.Is(err, agent.ErrConcurrencyLimit) {
				// If the session has been pending for too long, fail it
				// instead of retrying indefinitely.
				if time.Since(run.CreatedAt) > 8*time.Minute {
					logger.Warn().
						Str("session_id", runID.String()).
						Dur("age", time.Since(run.CreatedAt)).
						Msg("concurrency limit: session pending too long, failing")
					errMsg := "Session could not start: all agent slots are in use. Please try again when capacity is available."
					failErr := stores.Sessions.UpdateResult(ctx, orgID, runID, models.SessionStatusFailed, &models.SessionResult{
						Error: &errMsg,
					})
					if failErr != nil {
						logger.Error().Err(failErr).Str("session_id", runID.String()).Msg("failed to mark timed-out session as failed")
					}
					_ = stores.Sessions.UpdateFailure(ctx, orgID, runID,
						"This session was unable to start because all available agent slots were in use for an extended period.",
						"concurrency_timeout",
						[]string{
							"Wait for other running sessions to complete, then retry",
							"Cancel sessions that are no longer needed to free up capacity",
						},
						true,
					)
					enqueueSlackRunUpdateIfLinked(ctx, stores, logger, orgID, runID, "failed", "143 could not start this session", errMsg, true)
					enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, "session.failed", "143 session failed", errMsg)
					return &FatalError{Err: fmt.Errorf("session timed out waiting for concurrency slot: %w", err)}
				}
				return &RetryableError{Err: err}
			}
			enqueueSlackRunUpdateIfLinked(ctx, stores, logger, orgID, runID, "failed", "143 session failed", err.Error(), true)
			enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, "session.failed", "143 session failed", err.Error())
			return err
		}
		enqueueSlackHumanInputsIfPending(ctx, stores, logger, orgID, runID)
		enqueueSlackFinalIfLinked(ctx, stores, logger, orgID, runID)
		enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, "session.completed", "143 session completed", "The session finished successfully.")
		enqueueSlackPreviewStaleIfNeeded(ctx, stores, logger, orgID, runID)
		return nil
	}
}

func enqueueSlackRunUpdateIfLinked(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, updateKind, title, summary string, terminal bool) {
	if stores == nil || stores.SlackSessionLinks == nil || stores.Jobs == nil {
		return
	}
	link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
	if err != nil {
		return
	}
	payload := models.SlackPostRunUpdateJobPayload{
		OrgID:              orgID.String(),
		SessionID:          sessionID.String(),
		SlackSessionLinkID: link.ID.String(),
		UpdateKind:         updateKind,
		Title:              title,
		Summary:            summary,
		Terminal:           terminal,
	}
	dedupeKey := "slack_run_update:" + sessionID.String() + ":" + updateKind
	if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_post_run_update", payload, 4, &dedupeKey); err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to enqueue Slack run update")
	}
}

func enqueueSlackFinalIfLinked(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.SlackSessionLinks == nil || stores.SessionMessages == nil || stores.Jobs == nil {
		return
	}
	link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load Slack session link for final response")
		}
		return
	}
	messages, err := stores.SessionMessages.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load messages for Slack final response")
		return
	}
	var finalMessageID int64
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == models.MessageRoleAssistant {
			finalMessageID = messages[i].ID
			break
		}
	}
	if finalMessageID == 0 {
		return
	}
	payload := models.SlackPostFinalResponseJobPayload{
		OrgID:              orgID.String(),
		SessionID:          sessionID.String(),
		SlackSessionLinkID: link.ID.String(),
		FinalMessageID:     finalMessageID,
	}
	dedupeKey := "slack_final:" + sessionID.String() + ":" + strconv.FormatInt(finalMessageID, 10)
	if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_post_final_response", payload, 3, &dedupeKey); err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to enqueue Slack final response")
	}
}

func newCancelSessionHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.Orchestrator == nil {
			return &FatalError{Err: fmt.Errorf("orchestrator is not configured")}
		}
		var input struct {
			SessionID string `json:"session_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal cancel_session payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		accepted := services.Orchestrator.CancelSessionByID(sessionID)
		if accepted && stores != nil && stores.Sessions != nil {
			consumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, err := stores.Sessions.ConsumeCancelRequest(consumeCtx, orgID, sessionID); err != nil {
				logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to consume delivered session cancel request")
			}
		}
		if !accepted {
			logger.Warn().
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Msg("cancel_session job found no live local cancel registry entry")
		}
		return nil
	}
}

func newCancelThreadHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.Orchestrator == nil {
			return &FatalError{Err: fmt.Errorf("orchestrator is not configured")}
		}
		var input struct {
			SessionID string `json:"session_id"`
			ThreadID  string `json:"thread_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal cancel_thread payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		threadID, err := uuid.Parse(input.ThreadID)
		if err != nil {
			return fmt.Errorf("parse thread ID: %w", err)
		}
		accepted := services.Orchestrator.CancelThreadByID(threadID)
		if !accepted {
			logger.Warn().
				Str("session_id", sessionID.String()).
				Str("thread_id", threadID.String()).
				Str("org_id", orgID.String()).
				Msg("cancel_thread job found no live local cancel registry entry")
		}
		return nil
	}
}

func newDeliverThreadInboxHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.Orchestrator == nil {
			return &FatalError{Err: fmt.Errorf("orchestrator is not configured")}
		}
		var input struct {
			SessionID string `json:"session_id"`
			ThreadID  string `json:"thread_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal deliver_thread_inbox payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		threadID, err := uuid.Parse(input.ThreadID)
		if err != nil {
			return fmt.Errorf("parse thread ID: %w", err)
		}
		if err := services.Orchestrator.DeliverThreadInbox(ctx, orgID, sessionID, threadID); err != nil {
			var ownerErr *agent.ThreadRuntimeOwnedElsewhereError
			if errors.As(err, &ownerErr) && ownerErr.OwnerNodeID != "" {
				logger.Info().
					Str("session_id", sessionID.String()).
					Str("thread_id", threadID.String()).
					Str("target_node_id", ownerErr.OwnerNodeID).
					Msg("thread inbox delivery belongs to another worker; retargeting retry")
				return &RetryableError{
					Err:          err,
					TargetNodeID: &ownerErr.OwnerNodeID,
				}
			}
			if errors.Is(err, agent.ErrThreadRuntimeLeaseLost) {
				delay := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Str("thread_id", threadID.String()).
					Dur("retry_after", delay).
					Msg("thread inbox delivery lost runtime lease; retrying after recovery")
				return &RetryableError{
					Err:        err,
					RetryAfter: &delay,
				}
			}
			return err
		}
		return nil
	}
}

func answerQueuedHumanInputForContinue(ctx context.Context, stores *Stores, orgID, sessionID, threadID uuid.UUID, hasThread bool, queuedMessageID string, logger zerolog.Logger) (*uuid.UUID, error) {
	if stores == nil || stores.HumanInputRequests == nil || stores.SessionMessages == nil {
		return nil, nil
	}
	messageID, err := strconv.ParseInt(queuedMessageID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse queued message ID: %w", err)
	}
	queued, err := stores.SessionMessages.GetByID(ctx, orgID, messageID)
	if err != nil {
		return nil, fmt.Errorf("fetch queued message for human input answer: %w", err)
	}
	if queued.SessionID != sessionID {
		return nil, fmt.Errorf("queued message %d belongs to session %s, not %s", messageID, queued.SessionID, sessionID)
	}
	if hasThread {
		if queued.ThreadID == nil || *queued.ThreadID != threadID {
			return nil, fmt.Errorf("queued message %d does not belong to thread %s", messageID, threadID)
		}
	}
	if queued.UserID == nil {
		return nil, nil
	}
	answerText := strings.TrimPrefix(queued.Content, "[PLAN_MODE]\n")
	var request models.HumanInputRequest
	if hasThread {
		request, err = stores.HumanInputRequests.AnswerLatestPendingFreeTextByThread(ctx, orgID, sessionID, threadID, answerText, *queued.UserID)
	} else {
		request, err = stores.HumanInputRequests.AnswerLatestPendingFreeTextBySession(ctx, orgID, sessionID, answerText, *queued.UserID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("thread_id", threadID.String()).
			Int64("queued_message_id", messageID).
			Msg("failed to answer pending human-input request from queued message")
		return nil, nil
	}
	return &request.ID, nil
}

// continue_session handler continues a multi-turn session with a follow-up message.
func newContinueSessionHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID           string `json:"session_id"`
			OrgID               string `json:"org_id"`
			ThreadID            string `json:"thread_id"`
			PullRequestID       string `json:"pull_request_id"`
			RepairRunID         string `json:"repair_run_id"`
			CommandType         string `json:"command_type"`
			HealthVersion       int64  `json:"health_version"`
			HeadSHA             string `json:"head_sha"`
			WorkspaceMode       string `json:"workspace_mode"`
			PullRequestNumber   int    `json:"pull_request_number"`
			HumanInputRequestID string `json:"human_input_request_id"`
			QueuedMessageID     string `json:"queued_message_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal continue_session payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}

		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("fetch session: %w", err)
		}

		// Apply the per-session wall-clock timeout (see newRunAgentHandler for
		// rationale). HandlerCleanupBuffer lets the orchestrator clean up
		// after the timeout fires without racing the handler context.
		sessionTimeout := services.Orchestrator.ResolveSessionTimeout(ctx, orgID)
		runtimeCeiling := services.Orchestrator.ResolveAbsoluteRuntimeCeiling(ctx, orgID)
		jobCtx, cancel := context.WithTimeout(ctx, runtimeCeiling+agent.HandlerCleanupBuffer)
		defer cancel()

		logger.Info().
			Str("session_id", sessionID.String()).
			Str("org_id", orgID.String()).
			Str("thread_id", input.ThreadID).
			Str("human_input_request_id", input.HumanInputRequestID).
			Int("current_turn", session.CurrentTurn).
			Dur("session_timeout", sessionTimeout).
			Dur("runtime_ceiling", runtimeCeiling).
			Msg("starting continue_session job")

		var threadID uuid.UUID
		var threadTurnBefore int
		var hasThread bool
		var continueOpts *agent.ContinueSessionOptions
		var resultAgentSessionID string
		var humanInputRequestID *uuid.UUID
		if input.HumanInputRequestID != "" {
			parsedHumanInputRequestID, parseErr := uuid.Parse(input.HumanInputRequestID)
			if parseErr != nil {
				return fmt.Errorf("parse human input request ID: %w", parseErr)
			}
			humanInputRequestID = &parsedHumanInputRequestID
		}
		// Captured by the OnTurnComplete callback so the post-success block
		// below can persist per-thread result metadata (diff, summary,
		// etc.) onto the thread row. Stays nil when the orchestrator
		// short-circuited before completing a turn (cancel, policy stop) so
		// we fall back to the status-only completion path.
		var lastTurnResult *agent.AgentResult
		if input.CommandType != "" {
			prID, parseErr := uuid.Parse(input.PullRequestID)
			if parseErr != nil {
				return fmt.Errorf("parse pull request ID: %w", parseErr)
			}
			var repairRunID uuid.UUID
			if input.RepairRunID != "" {
				repairRunID, parseErr = uuid.Parse(input.RepairRunID)
				if parseErr != nil {
					return fmt.Errorf("parse repair run ID: %w", parseErr)
				}
			}
			mode := models.PullRequestRepairWorkspaceMode(input.WorkspaceMode)
			if mode == "" {
				mode = models.PullRequestRepairWorkspaceModeSnapshotContinuation
			}
			if err := mode.Validate(); err != nil {
				return err
			}
			action := models.PullRequestRepairActionType(input.CommandType)
			if err := action.Validate(); err != nil {
				return err
			}
			continueOpts = &agent.ContinueSessionOptions{
				PRRepair: &agent.PRRepairContinueOptions{
					PullRequestID:     prID,
					RepairRunID:       repairRunID,
					PullRequestNumber: input.PullRequestNumber,
					CommandType:       action,
					HealthVersion:     input.HealthVersion,
					HeadSHA:           input.HeadSHA,
					WorkspaceMode:     mode,
				},
			}
		}
		if input.ThreadID != "" && stores.SessionThreads != nil {
			parsedThreadID, parseErr := uuid.Parse(input.ThreadID)
			if parseErr != nil {
				logger.Warn().Err(parseErr).Str("thread_id", input.ThreadID).Msg("invalid thread_id in continue_session payload")
			} else {
				threadID = parsedThreadID
				hasThread = true
				thread, threadErr := stores.SessionThreads.GetByID(ctx, orgID, threadID)
				if threadErr != nil {
					return fmt.Errorf("fetch session thread: %w", threadErr)
				}
				if thread.SessionID != sessionID {
					return fmt.Errorf("session thread %s does not belong to session %s", threadID, sessionID)
				}
				threadTurnBefore = thread.CurrentTurn

				// Phase 2: stamp the thread-start checkpoint before the first
				// agent turn. The session's current SnapshotKey is the
				// pre-thread-edit state from the user's perspective; using
				// it as the thread's recovery baseline lets a "revert this
				// tab" action restore the workspace to that point even if
				// later sibling-tab turns overwrite session.SnapshotKey.
				if thread.CurrentTurn == 0 && thread.BaseSnapshotKey == nil && session.SnapshotKey != nil && *session.SnapshotKey != "" {
					if err := stores.SessionThreads.SetBaseSnapshot(ctx, orgID, threadID, *session.SnapshotKey); err != nil {
						logger.Warn().Err(err).
							Str("thread_id", threadID.String()).
							Msg("failed to stamp thread-start checkpoint")
					}
				}

				threadIDLocal := threadID
				threadOpts := &agent.ContinueSessionOptions{
					AgentType:            thread.AgentType,
					ModelOverride:        thread.ModelOverride,
					ThreadAgentSessionID: thread.AgentSessionID,
					ResultAgentSessionID: &resultAgentSessionID,
					HumanInputRequestID:  humanInputRequestID,
					ThreadID:             &threadIDLocal,
					OnTurnComplete: func(result *agent.AgentResult) {
						lastTurnResult = result
						if result == nil {
							return
						}
						emitThreadAttribution(ctx, stores, orgID, sessionID, threadIDLocal, threadTurnBefore+1, result.Diff, result.TokenUsage.TotalCostUSD, logger)
					},
				}
				if continueOpts != nil {
					threadOpts.PRRepair = continueOpts.PRRepair
				}
				continueOpts = threadOpts
			}
		}
		if humanInputRequestID == nil && input.QueuedMessageID != "" {
			answeredID, answerErr := answerQueuedHumanInputForContinue(ctx, stores, orgID, sessionID, threadID, hasThread, input.QueuedMessageID, logger)
			if answerErr != nil {
				return answerErr
			}
			humanInputRequestID = answeredID
		}
		if continueOpts == nil && humanInputRequestID != nil {
			continueOpts = &agent.ContinueSessionOptions{HumanInputRequestID: humanInputRequestID}
		} else if continueOpts != nil && humanInputRequestID != nil {
			continueOpts.HumanInputRequestID = humanInputRequestID
		}

		var dispatchThreadID *uuid.UUID
		if hasThread {
			threadIDLocal := threadID
			dispatchThreadID = &threadIDLocal
		}
		if err := maybeDispatchSessionExecutor(ctx, services, jobType, session, dispatchThreadID); err != nil {
			return err
		}

		if err := services.Orchestrator.ContinueSession(jobCtx, &session, continueOpts); err != nil {
			if errors.Is(err, agent.ErrSandboxCapacity) {
				retryAfter := sandboxCapacityRetryDelay
				targetNodeID, clearTargetNodeID := sandboxCapacityRetryTarget(ctx, stores, logger)
				var capacityThreadID *uuid.UUID
				if hasThread {
					threadIDLocal := threadID
					capacityThreadID = &threadIDLocal
				}
				registerSandboxCapacityDeadLetter(ctx, stores, services, logger, session, capacityThreadID, "continue_session")
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("local sandbox capacity reached; retrying continue_session")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: targetNodeID, ClearTargetNodeID: clearTargetNodeID}
			}
			// A pending post-PR snapshot upload is a transient state — wrap
			// in RetryableError so the job is requeued without consuming an
			// attempt. The session row is unchanged at this point.
			if errors.Is(err, agent.ErrSnapshotPending) {
				return &RetryableError{Err: err}
			}
			if errors.Is(err, agent.ErrSessionInterrupted) {
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("continue_session interrupted by system stop; retrying turn")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrThreadRuntimeAlreadyActive) {
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Str("thread_id", input.ThreadID).
					Err(err).
					Msg("thread runtime already active; retrying after lease recovery")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrStalePullRequestHead) {
				if input.PullRequestID != "" && services.PR != nil {
					if syncErr := services.PR.SyncPullRequestState(ctx, orgID, uuid.MustParse(input.PullRequestID)); syncErr != nil {
						logger.Warn().Err(syncErr).Str("pull_request_id", input.PullRequestID).Msg("failed to sync pull request state after stale repair head")
					}
				}
				return &FatalError{Err: err}
			}
			if errors.Is(err, agent.ErrStaleSandboxIDCleared) {
				// Stale orphan container_id cleared; retry against the clean
				// row. See newRunAgentHandler for full rationale.
				retryAfter := 2 * time.Second
				var staleThreadID *uuid.UUID
				if hasThread {
					threadIDLocal := threadID
					staleThreadID = &threadIDLocal
				}
				registerStaleSandboxDeadLetter(ctx, stores, logger, session, staleThreadID, "continue_session")
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("continue_session cleared stale orphan container_id; retrying against the clean row")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, BypassMaxRetryDuration: true}
			}
			if errors.Is(err, agent.ErrSandboxOnDifferentNode) {
				// We claimed a job whose session sandbox lives on a sibling
				// worker. Release it so the correct node can pick it up. A
				// 5s delay (longer than the stale-orphan path) avoids tight
				// loops if the wrong-node worker keeps polling first while
				// the right one is briefly busy. With node-affinity routing
				// (target_node_id on jobs) in place this branch is rare —
				// it only fires for jobs enqueued before the affinity rolled
				// out, or as a defense-in-depth catch for bugs that bypass
				// the pinning.
				retryAfter := 5 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("continue_session claimed on the wrong node; releasing for the correct worker")
				return &RetryableError{
					Err:                    err,
					RetryAfter:             &retryAfter,
					BypassMaxRetryDuration: true,
					TargetNodeID:           models.SessionWorkerTarget(&session),
				}
			}
			if errors.Is(err, agent.ErrSandboxPreviewRace) {
				// A preview hydrate published the live container first. Retry
				// so the next attempt fetches the updated session row and
				// attaches to that preview-held container via the reuse path.
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("continue_session lost sandbox publish race to preview; retrying against the preview container")
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
			}
			if errors.Is(err, agent.ErrSandboxSiblingRace) {
				// Another sibling tab published the shared sandbox first. This
				// job is not a duplicate of the winner's work: it owns a
				// different thread turn, so retry and attach to the recorded
				// shared sandbox on the next attempt.
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Str("thread_id", input.ThreadID).
					Err(err).
					Msg("continue_session lost sandbox publish race to sibling thread; retrying against the shared sandbox")
				return &RetryableError{Err: err, RetryAfter: &retryAfter, TargetNodeID: models.SessionWorkerTarget(&session)}
			}
			if errors.Is(err, agent.ErrSandboxRaceLoser) {
				// A duplicate continue_session job lost the AcquireTurnHold
				// race to the winner. Dead-letter immediately without
				// retries (every retry would lose the same race) and
				// without touching the session row — the winner owns it.
				// The thread is left as the orchestrator left it; the
				// winner's success/failure path will release it.
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("duplicate continue_session job lost sandbox-hold race; dead-lettering silently — winner retains the session row")
				return &FatalError{Err: err}
			}
			if hasThread && !errors.Is(err, agent.ErrSessionCancelled) {
				// Detached context: this cleanup must land even when ctx was
				// cancelled by worker drain mid-shutdown. Otherwise the
				// thread is stuck in 'running' and the UI shows an orphaned
				// "Agent is working..." indefinitely (until Phase 0.5b of
				// the reaper picks it up — defense-in-depth, not the primary
				// path). 10s is plenty for a single UPDATE.
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if statusErr := stores.SessionThreads.UpdateStatus(cleanupCtx, orgID, threadID, models.ThreadStatusIdle); statusErr != nil {
					logger.Warn().Err(statusErr).
						Str("session_id", sessionID.String()).
						Str("thread_id", threadID.String()).
						Msg("failed to release session thread after continue_session failure")
				}
				cleanupCancel()
				if services.ReviewLoops != nil {
					reviewCleanupCtx, reviewCleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					if reviewErr := services.ReviewLoops.OnThreadTurnFailed(reviewCleanupCtx, orgID, threadID, err.Error()); reviewErr != nil && !errors.Is(reviewErr, reviewloopsvc.ErrNoRunningReviewLoop) {
						logger.Warn().Err(reviewErr).
							Str("session_id", sessionID.String()).
							Str("thread_id", threadID.String()).
							Msg("failed to mark review loop failed after thread turn failure")
					}
					reviewCleanupCancel()
				}
			}
			return err
		}

		if hasThread {
			// The user message was inserted at thread.CurrentTurn + 1, so the
			// thread's new current_turn is the same value once the assistant
			// turn completes. The session's CurrentTurn is independent — it
			// tracks the shared sandbox's total turns across all threads.
			//
			// When OnTurnComplete fired we have the agent's result in hand,
			// so persist diff/summary onto the thread row via
			// UpdateTurnComplete — this is the data the revert action and
			// the per-tab summary panel read. When the turn short-circuited
			// before producing a result (cancel, policy stop) we fall back
			// to CompleteTurn which only flips status.
			if lastTurnResult != nil {
				var summaryPtr, diffPtr *string
				if lastTurnResult.Summary != "" {
					summaryPtr = &lastTurnResult.Summary
				}
				if lastTurnResult.Diff != "" {
					diffPtr = &lastTurnResult.Diff
				}
				threadResult := &models.SessionResult{
					ResultSummary: summaryPtr,
					Diff:          diffPtr,
				}
				if err := stores.SessionThreads.UpdateTurnComplete(ctx, orgID, threadID, threadTurnBefore+1, threadResult, resultAgentSessionID); err != nil {
					logger.Warn().Err(err).
						Str("session_id", sessionID.String()).
						Str("thread_id", threadID.String()).
						Msg("failed to persist session thread turn result")
				}
				if services.ReviewLoops != nil {
					if err := services.ReviewLoops.OnThreadTurnComplete(ctx, orgID, threadID, lastTurnResult.Summary); err != nil && !errors.Is(err, reviewloopsvc.ErrNoRunningReviewLoop) {
						logger.Warn().Err(err).
							Str("session_id", sessionID.String()).
							Str("thread_id", threadID.String()).
							Msg("failed to advance review loop after thread turn")
					}
				}
			} else {
				if err := stores.SessionThreads.CompleteTurn(ctx, orgID, threadID, threadTurnBefore+1, resultAgentSessionID); err != nil {
					logger.Warn().Err(err).
						Str("session_id", sessionID.String()).
						Str("thread_id", threadID.String()).
						Msg("failed to mark session thread turn complete")
				}
			}
		}

		// Regenerate title if due (every 3 turns). Non-fatal — log and continue.
		if services.TitleService != nil {
			if titleErr := services.TitleService.MaybeRegenerateTitle(ctx, orgID, sessionID); titleErr != nil {
				logger.Warn().Err(titleErr).Str("session_id", sessionID.String()).Msg("failed to regenerate session title")
			}
		}
		return nil
	}
}

func primaryIssueIDFromSnapshot(snapshot *models.SessionTurnIssueSnapshot) *uuid.UUID {
	if snapshot == nil {
		return nil
	}
	for _, linked := range snapshot.LinkedIssues {
		if linked.Role == models.SessionIssueLinkRolePrimary {
			id := linked.IssueID
			return &id
		}
	}
	return nil
}

func newSyncPullRequestStateHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID         string `json:"org_id"`
			PullRequestID string `json:"pull_request_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal sync_pull_request_state payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		pullRequestID, err := uuid.Parse(input.PullRequestID)
		if err != nil {
			return fmt.Errorf("parse pull request ID: %w", err)
		}
		logger.Info().Str("org_id", orgID.String()).Str("pull_request_id", pullRequestID.String()).Msg("starting sync_pull_request_state job")
		if err := services.PR.SyncPullRequestState(ctx, orgID, pullRequestID); err != nil {
			if errors.Is(err, ghservice.ErrPullRequestMergeabilityPending) {
				return &RetryableError{Err: err, ConsumeAttempt: true}
			}
			return err
		}
		return nil
	}
}

func newReconcilePullRequestStateHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal reconcile_pull_request_state payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		if input.Limit <= 0 {
			input.Limit = 50
		}
		logger.Info().Str("org_id", orgID.String()).Int("limit", input.Limit).Msg("starting reconcile_pull_request_state job")
		return services.PR.ReconcilePullRequestState(ctx, orgID, input.Limit)
	}
}

func newEnrichPullRequestHealthHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID         string `json:"org_id"`
			PullRequestID string `json:"pull_request_id"`
			Version       int64  `json:"version,string"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal enrich_pull_request_health payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		pullRequestID, err := uuid.Parse(input.PullRequestID)
		if err != nil {
			return fmt.Errorf("parse pull request ID: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Str("pull_request_id", pullRequestID.String()).
			Int64("version", input.Version).
			Msg("starting enrich_pull_request_health job")
		return services.PR.EnrichPullRequestHealth(ctx, orgID, pullRequestID, input.Version)
	}
}

func newMergePullRequestWhenReadyHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID         string `json:"org_id"`
			PullRequestID string `json:"pull_request_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal merge_pull_request_when_ready payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		pullRequestID, err := uuid.Parse(input.PullRequestID)
		if err != nil {
			return fmt.Errorf("parse pull request ID: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Str("pull_request_id", pullRequestID.String()).
			Msg("starting merge_pull_request_when_ready job")
		return services.PR.ProcessMergeWhenReady(ctx, orgID, pullRequestID)
	}
}

// open_pr handler creates a GitHub PR from a completed agent run by pushing
// the restored sandbox snapshot to GitHub. Drives the session's
// pr_creation_state through pushing -> succeeded/failed so the UI can reflect
// progress without needing to poll PR rows.
func newOpenPRHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID       string `json:"session_id"`
			OrgID           string `json:"org_id"`
			IssueSnapshotID string `json:"issue_snapshot_id,omitempty"`
			Draft           *bool  `json:"draft,omitempty"`
			AuthorMode      string `json:"author_mode,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal open_pr payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.Sessions.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}
		if run.SnapshotKey == nil || *run.SnapshotKey == "" {
			if run.Status == models.SessionStatusRunning {
				logger.Info().
					Str("session_id", runID.String()).
					Msg("open_pr waiting for running session snapshot")
				return &RetryableError{Err: agent.ErrSnapshotPending}
			}
			return fmt.Errorf("session %s has no snapshot (status: %s)", runID, run.Status)
		}
		if err := ensureSessionSnapshotQuiescent(ctx, stores, run); err != nil {
			logger.Info().
				Err(err).
				Str("session_id", runID.String()).
				Msg("open_pr waiting for shared sandbox quiescence")
			return err
		}

		if stores.SessionIssueLinks != nil {
			links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, runID)
			if err != nil {
				return fmt.Errorf("hydrate linked issues for open_pr: %w", err)
			}
			run.LinkedIssues = links
		}

		ready, err := ensureAutomationPrePRReview(ctx, stores, services, logger, run)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting open_pr job")

		if stores.IssueSnapshots != nil {
			var snapshot *models.SessionTurnIssueSnapshot
			if input.IssueSnapshotID != "" {
				snapshotID, err := uuid.Parse(input.IssueSnapshotID)
				if err != nil {
					return fmt.Errorf("parse issue snapshot id: %w", err)
				}
				resolved, err := stores.IssueSnapshots.GetByID(ctx, orgID, snapshotID)
				if err != nil {
					return fmt.Errorf("fetch issue snapshot for open_pr: %w", err)
				}
				snapshot = &resolved
			} else if run.CurrentTurn > 0 {
				if resolved, err := stores.IssueSnapshots.GetByTurn(ctx, orgID, run.ID, run.CurrentTurn); err == nil {
					snapshot = &resolved
				}
			}
			if issueID := primaryIssueIDFromSnapshot(snapshot); issueID != nil {
				run.PrimaryIssueID = issueID
			}
		}

		if err := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStatePushing, ""); err != nil {
			logger.Error().Err(err).Msg("failed to mark PR creation as pushing")
		}

		var params []ghservice.CreatePRParams
		if input.Draft != nil {
			params = append(params, ghservice.CreatePRParams{Draft: input.Draft})
		}
		if input.AuthorMode != "" {
			params = append(params, ghservice.CreatePRParams{AuthorMode: input.AuthorMode})
		}

		_, createErr := services.PR.CreatePR(ctx, &run, params...)
		if createErr != nil {
			// ErrNoChanges is a benign terminal outcome (session ran fine
			// but produced no diff), so log at info to keep `open_pr failed`
			// a real-error signal that dashboards/alerts can key off without
			// false positives.
			if errors.Is(createErr, ghservice.ErrNoChanges) {
				logger.Info().
					Str("session_id", runID.String()).
					Msg("open_pr: no changes to push")
			} else {
				// Always log the raw error — only a curated subset is shown in
				// the UI, so the worker log is the source of truth for ops.
				logger.Error().Err(createErr).
					Str("session_id", runID.String()).
					Msg("open_pr failed")
			}
			msg := userFacingPRError(createErr)
			if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateFailed, msg); stateErr != nil {
				logger.Error().Err(stateErr).Msg("failed to mark PR creation as failed")
			}
			// PR creation failures happen after the agent run has already
			// completed, so failRun will not fire for them. Tell the Linear
			// linker about terminal outcomes here so agent-triggered sessions
			// do not stay in-progress forever after a dead-lettered open_pr.
			if errors.Is(createErr, ghservice.ErrNoChanges) {
				linear.EnqueueMilestone(ctx, stores.Jobs, logger, orgID, runID, "ended_no_pr", 0)
			}
			if shouldDeadLetterPRError(createErr) {
				if !errors.Is(createErr, ghservice.ErrNoChanges) {
					linear.EnqueueMilestone(ctx, stores.Jobs, logger, orgID, runID, "failed", 0)
				}
				return &FatalError{Err: createErr}
			}
			registerOpenPRDeadLetterMilestone(ctx, stores, logger, orgID, runID)
			return createErr
		}

		if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateSucceeded, ""); stateErr != nil {
			logger.Error().Err(stateErr).Msg("failed to mark PR creation as succeeded")
		}
		enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
			EventKind: "pr.opened",
			Title:     "Pull request opened",
			Body:      "A pull request was opened for the session.",
			SessionID: &runID,
		})
		return nil
	}
}

func registerOpenPRDeadLetterMilestone(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.Jobs == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, _ error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()
		linear.EnqueueMilestone(writeCtx, stores.Jobs, logger, orgID, sessionID, "failed", 0)
	})
}

// create_branch pushes a completed session snapshot to GitHub without opening
// a pull request, so a human can fetch and test the branch locally.
func newCreateBranchHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID  string `json:"session_id"`
			OrgID      string `json:"org_id"`
			AuthorMode string `json:"author_mode,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal create_branch payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}

		run, err := stores.Sessions.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch session: %w", err)
		}
		if err := ensureSessionSnapshotQuiescent(ctx, stores, run); err != nil {
			logger.Info().
				Err(err).
				Str("session_id", runID.String()).
				Msg("create_branch waiting for shared sandbox quiescence")
			return err
		}
		if stores.SessionIssueLinks != nil {
			links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, runID)
			if err != nil {
				return fmt.Errorf("hydrate linked issues for create_branch: %w", err)
			}
			run.LinkedIssues = links
		}

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting create_branch job")

		if err := stores.Sessions.UpdateBranchCreationState(ctx, orgID, runID, models.BranchCreationStatePushing, "", nil); err != nil {
			logger.Error().Err(err).Msg("failed to mark branch creation as pushing")
		}

		var params []ghservice.CreatePRParams
		if input.AuthorMode != "" {
			params = append(params, ghservice.CreatePRParams{AuthorMode: input.AuthorMode})
		}

		branch, branchErr := services.PR.CreateBranch(ctx, &run, params...)
		if branchErr != nil {
			if errors.Is(branchErr, ghservice.ErrNoChanges) {
				logger.Info().Str("session_id", runID.String()).Msg("create_branch: no changes to push")
			} else {
				logger.Error().Err(branchErr).Str("session_id", runID.String()).Msg("create_branch failed")
			}
			msg := userFacingPRError(branchErr)
			if stateErr := stores.Sessions.UpdateBranchCreationState(ctx, orgID, runID, models.BranchCreationStateFailed, msg, nil); stateErr != nil {
				logger.Error().Err(stateErr).Msg("failed to mark branch creation as failed")
			}
			if shouldDeadLetterPRError(branchErr) {
				return &FatalError{Err: branchErr}
			}
			return branchErr
		}

		if stateErr := stores.Sessions.UpdateBranchCreationState(ctx, orgID, runID, models.BranchCreationStateSucceeded, "", &branch.URL); stateErr != nil {
			logger.Error().Err(stateErr).Msg("failed to mark branch creation as succeeded")
		}
		return nil
	}
}

func ensureAutomationPrePRReview(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, run models.Session) (bool, error) {
	if run.AutomationRunID == nil {
		return true, nil
	}
	if stores == nil || stores.AutomationRuns == nil {
		return true, nil
	}
	automationRun, err := stores.AutomationRuns.GetByRunID(ctx, run.OrgID, *run.AutomationRunID)
	if err != nil {
		return false, fmt.Errorf("fetch automation run for pre-pr review: %w", err)
	}
	passCount, err := automationPrePRReviewPasses(automationRun.ConfigSnapshot)
	if err != nil {
		return false, err
	}
	if passCount == 0 {
		return true, nil
	}
	if stores.ReviewLoops == nil || services == nil || services.ReviewLoops == nil {
		return false, fmt.Errorf("pre-pr review is enabled but review loop service is unavailable")
	}
	loop, err := stores.ReviewLoops.GetLatestLoopByAutomationRun(ctx, run.OrgID, *run.AutomationRunID)
	if err == nil {
		switch loop.Status {
		case models.ReviewLoopStatusClean:
			return true, nil
		case models.ReviewLoopStatusRunning:
			logger.Info().
				Str("session_id", run.ID.String()).
				Str("review_loop_id", loop.ID.String()).
				Msg("open_pr waiting for pre-pr review loop")
			retryAfter := prePRReviewRetryDelay
			return false, &RetryableError{
				Err:        fmt.Errorf("pre-pr review loop is still running"),
				RetryAfter: &retryAfter,
			}
		case models.ReviewLoopStatusNeedsHumanDecision:
			if stateErr := stores.Sessions.UpdatePRCreationState(ctx, run.OrgID, run.ID, models.PRCreationStateFailed, "Pre-PR review needs human decision."); stateErr != nil {
				logger.Error().Err(stateErr).Str("session_id", run.ID.String()).Msg("failed to mark PR creation blocked by review loop")
			}
			return false, nil
		default:
			if stateErr := stores.Sessions.UpdatePRCreationState(ctx, run.OrgID, run.ID, models.PRCreationStateFailed, "Pre-PR review did not complete cleanly."); stateErr != nil {
				logger.Error().Err(stateErr).Str("session_id", run.ID.String()).Msg("failed to mark PR creation blocked by review loop")
			}
			return false, nil
		}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("fetch pre-pr review loop: %w", err)
	}

	_, err = services.ReviewLoops.Start(ctx, run.OrgID, run.ID, reviewloopsvc.StartReviewLoopRequest{
		AgentType:       run.AgentType,
		Model:           stringValue(run.ModelOverride),
		MaxPasses:       passCount,
		Source:          models.ReviewLoopSourceAutomation,
		AutomationRunID: run.AutomationRunID,
		StartedByUserID: run.TriggeredByUserID,
		ReviewRequired:  true,
	})
	if err != nil {
		return false, fmt.Errorf("start pre-pr review loop: %w", err)
	}
	logger.Info().
		Str("session_id", run.ID.String()).
		Str("automation_run_id", run.AutomationRunID.String()).
		Int("max_passes", passCount).
		Msg("started pre-pr review loop")
	return false, nil
}

func automationPrePRReviewPasses(config json.RawMessage) (int, error) {
	if len(config) == 0 {
		return 0, nil
	}
	var snapshot struct {
		PrePRReviewLoops *int `json:"pre_pr_review_loops"`
	}
	if err := json.Unmarshal(config, &snapshot); err != nil {
		return 0, fmt.Errorf("parse automation config snapshot for pre-pr review: %w", err)
	}
	if snapshot.PrePRReviewLoops == nil {
		return 0, nil
	}
	if *snapshot.PrePRReviewLoops < 0 || *snapshot.PrePRReviewLoops > reviewloopsvc.MaxReviewPasses {
		return 0, fmt.Errorf("pre_pr_review_loops must be between 0 and %d", reviewloopsvc.MaxReviewPasses)
	}
	return *snapshot.PrePRReviewLoops, nil
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// userFacingPRError collapses an internal error into a short string safe for
// surfacing in the UI. Sentinel errors get curated messages; everything else
// resolves to a generic fallback so low-level details (API paths, exit codes,
// tokens in wrapped errors) can't leak to the browser. The raw error is
// logged separately by the caller.
func userFacingPRError(err error) string {
	switch {
	case errors.Is(err, ghservice.ErrSnapshotExpired):
		return ghservice.SnapshotExpiredPRMessage
	case errors.Is(err, ghservice.ErrSnapshotNotCaptured):
		return ghservice.SnapshotNotCapturedPRMessage
	case errors.Is(err, ghservice.ErrSnapshotUnavailable):
		return ghservice.SnapshotUnavailablePRMessage
	case errors.Is(err, ghservice.ErrNoChanges):
		return "No changes to push."
	case errors.Is(err, ghservice.ErrNoPullRequest):
		return "No pull request exists for this session."
	case errors.Is(err, ghservice.ErrPRClosed):
		return "This pull request is no longer open."
	case errors.Is(err, ghservice.ErrLegacyPRMissingHeadRef):
		return "This PR predates branch tracking; create a new PR to push follow-up changes."
	case errors.Is(err, ghservice.ErrPushRejected):
		return ghservice.PushRejectedPRMessage
	default:
		return "Check GitHub access or repo permissions and try again."
	}
}

// push_pr_changes handler pushes any uncommitted/unpushed sandbox changes up
// to an existing PR's branch. Mirrors newOpenPRHandler but operates on a
// session that already has a PR row — drives pr_push_state through pushing ->
// succeeded/failed without touching pr_creation_state, and skips the Linear
// "ended_no_pr" milestone (a PR already exists, so "no changes to push" is a
// benign UI-only outcome).
func newPushPRChangesHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID  string `json:"session_id"`
			OrgID      string `json:"org_id"`
			AuthorMode string `json:"author_mode,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal push_pr_changes payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}

		run, err := stores.Sessions.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch session: %w", err)
		}
		if run.PendingSnapshotKey != nil && *run.PendingSnapshotKey != "" {
			logger.Info().
				Str("session_id", runID.String()).
				Str("pending_snapshot_key", *run.PendingSnapshotKey).
				Msg("push_pr_changes waiting for post-PR snapshot upload to land")
			return &RetryableError{Err: agent.ErrSnapshotPending}
		}
		if err := ensureSessionSnapshotQuiescent(ctx, stores, run); err != nil {
			logger.Info().
				Err(err).
				Str("session_id", runID.String()).
				Msg("push_pr_changes waiting for shared sandbox quiescence")
			return err
		}

		if stores.SessionIssueLinks != nil {
			links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, runID)
			if err != nil {
				return fmt.Errorf("hydrate linked issues for push_pr_changes: %w", err)
			}
			run.LinkedIssues = links
		}

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting push_pr_changes job")

		if err := stores.Sessions.UpdatePRPushState(ctx, orgID, runID, models.PRPushStatePushing, ""); err != nil {
			logger.Error().Err(err).Msg("failed to mark PR push as pushing")
		}

		var params []ghservice.CreatePRParams
		if input.AuthorMode != "" {
			params = append(params, ghservice.CreatePRParams{AuthorMode: input.AuthorMode})
		}

		_, pushErr := services.PR.PushChangesToPR(ctx, &run, params...)
		if pushErr != nil {
			// ErrNoChanges is benign for push-changes: either the user clicked
			// with a clean sandbox + nothing ahead of upstream, or this is a
			// worker retry after a partial-success first attempt landed the
			// push. In both cases the PR's branch already reflects the
			// session's state, so mark succeeded rather than failed — pr_push_error
			// stays empty and the head_sha on the PR row is the canonical
			// source of truth. Distinguishes push from CreatePR, where
			// ErrNoChanges is a real "nothing to ship" outcome with no PR row.
			if errors.Is(pushErr, ghservice.ErrNoChanges) {
				logger.Info().
					Str("session_id", runID.String()).
					Msg("push_pr_changes: nothing to push (already up to date)")
				if stateErr := stores.Sessions.UpdatePRPushState(ctx, orgID, runID, models.PRPushStateSucceeded, ""); stateErr != nil {
					logger.Error().Err(stateErr).Msg("failed to mark PR push as succeeded")
				}
				return nil
			}
			logger.Error().Err(pushErr).
				Str("session_id", runID.String()).
				Msg("push_pr_changes failed")
			msg := userFacingPRError(pushErr)
			if stateErr := stores.Sessions.UpdatePRPushState(ctx, orgID, runID, models.PRPushStateFailed, msg); stateErr != nil {
				logger.Error().Err(stateErr).Msg("failed to mark PR push as failed")
			}
			if shouldDeadLetterPRError(pushErr) {
				return &FatalError{Err: pushErr}
			}
			return pushErr
		}

		if stateErr := stores.Sessions.UpdatePRPushState(ctx, orgID, runID, models.PRPushStateSucceeded, ""); stateErr != nil {
			logger.Error().Err(stateErr).Msg("failed to mark PR push as succeeded")
		}
		return nil
	}
}

func shouldDeadLetterPRError(err error) bool {
	switch {
	case errors.Is(err, ghservice.ErrSnapshotExpired):
		return true
	case errors.Is(err, ghservice.ErrSnapshotNotCaptured):
		return true
	case errors.Is(err, ghservice.ErrSnapshotUnavailable):
		return true
	case errors.Is(err, ghservice.ErrNoChanges):
		return true
	case errors.Is(err, ghservice.ErrNoPullRequest):
		return true
	case errors.Is(err, ghservice.ErrPRClosed):
		return true
	case errors.Is(err, ghservice.ErrLegacyPRMissingHeadRef):
		return true
	default:
		return false
	}
}

// analyze_failure handler classifies and persists failure analysis for a failed agent run.
func newAnalyzeFailureHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID string `json:"session_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal analyze_failure payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.Sessions.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting analyze_failure job")

		summary, err := services.Failure.AnalyzeFailure(ctx, &run)
		if err != nil {
			return fmt.Errorf("analyze failure: %w", err)
		}

		return services.Failure.UpdateRunWithFailure(ctx, orgID, runID, summary)
	}
}

// process_review_comment handler runs the feedback processing pipeline on a single comment.
func newProcessReviewCommentHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			CommentID string `json:"comment_id"`
			OrgID     string `json:"org_id"`
			Repo      string `json:"repo"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal process_review_comment payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		commentID, err := uuid.Parse(input.CommentID)
		if err != nil {
			return fmt.Errorf("parse comment ID: %w", err)
		}

		logger.Info().
			Str("comment_id", commentID.String()).
			Str("org_id", orgID.String()).
			Msg("processing review comment")

		shouldUpdateMemories := false
		if input.Repo != "" {
			// Only update learned memories when this job is processing a pending comment.
			// This prevents duplicate occurrence increments on retries/redeliveries.
			currentComment, err := services.Feedback.GetProcessedComment(ctx, commentID, orgID)
			if err != nil {
				return fmt.Errorf("get review comment before processing: %w", err)
			}
			shouldUpdateMemories = currentComment.FilterStatus == "pending"
		}

		if err := services.Feedback.ProcessComment(ctx, commentID, orgID); err != nil {
			return fmt.Errorf("process review comment: %w", err)
		}

		// After processing, check if the comment is generalizable and update memories.
		// The repo is passed from the webhook handler.
		if shouldUpdateMemories {
			comment, err := services.Feedback.GetProcessedComment(ctx, commentID, orgID)
			if err == nil && comment.Generalizable && comment.GeneralizedRule != nil {
				category := "nit"
				if comment.Category != nil {
					category = *comment.Category
				}
				if err := services.Feedback.UpdateMemories(ctx, orgID, commentID, input.Repo, *comment.GeneralizedRule, category); err != nil {
					logger.Warn().Err(err).Str("comment_id", commentID.String()).Msg("failed to update memories")
				}
			}
		}

		return nil
	}
}

// update_memories handler updates memories for a classified comment.
func newUpdateMemoriesHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			CommentID string `json:"comment_id"`
			OrgID     string `json:"org_id"`
			Repo      string `json:"repo"`
			Rule      string `json:"rule"`
			Category  string `json:"category"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal update_memories payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		commentID, err := uuid.Parse(input.CommentID)
		if err != nil {
			return fmt.Errorf("parse comment ID: %w", err)
		}

		logger.Info().
			Str("comment_id", commentID.String()).
			Str("org_id", orgID.String()).
			Str("repo", input.Repo).
			Msg("updating memories")

		return services.Feedback.UpdateMemories(ctx, orgID, commentID, input.Repo, input.Rule, input.Category)
	}
}

// reinforce_memories handler re-derives the active memory set for a repo and
// reinforces those memories. Enqueued when a 143-generated PR is approved.
func newReinforceMemoriesHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
			Repo  string `json:"repo"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal reinforce_memories payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}

		if input.Repo == "" {
			return fmt.Errorf("missing repo in reinforce_memories payload")
		}

		// Re-derive which memories would be selected for this repo's context.
		memResult, err := services.Memory.GetContextMemories(ctx, agent.MemoryContextRequest{
			OrgID: orgID,
			Repo:  input.Repo,
		})
		if err != nil {
			return fmt.Errorf("get context memories for reinforcement: %w", err)
		}

		if memResult == nil || len(memResult.MemoryIDs) == 0 {
			logger.Debug().Str("repo", input.Repo).Msg("no active memories to reinforce")
			return nil
		}

		logger.Info().
			Str("org_id", orgID.String()).
			Str("repo", input.Repo).
			Int("memory_count", len(memResult.MemoryIDs)).
			Msg("reinforcing memories after PR approval")

		return services.Memory.ReinforceMemories(ctx, orgID, memResult.MemoryIDs)
	}
}

// audit_retention_cleanup handler deletes audit log entries older than the
// org-configured retention period using the SECURITY DEFINER function.
func newAuditRetentionCleanupHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal audit_retention_cleanup payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}

		org, err := stores.Organizations.GetByID(ctx, orgID)
		if err != nil {
			return fmt.Errorf("fetch organization: %w", err)
		}

		settings, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to parse org settings, using default retention")
			settings.AuditRetentionDays = models.DefaultAuditRetentionDays
		}

		retentionDays := settings.AuditRetentionDays
		if retentionDays <= 0 {
			retentionDays = models.DefaultAuditRetentionDays
		}

		deleted, err := stores.AuditLogs.DeleteExpired(ctx, orgID, retentionDays)
		if err != nil {
			return fmt.Errorf("delete expired audit logs: %w", err)
		}

		logger.Info().
			Str("org_id", orgID.String()).
			Int("retention_days", retentionDays).
			Int64("deleted", deleted).
			Msg("audit retention cleanup complete")

		return nil
	}
}

// data_retention_cleanup handler deletes expired webhook deliveries, session logs,
// and completed jobs based on configurable retention periods.
func newDataRetentionCleanupHandler(stores *Stores, retentionCfg DataRetentionConfig, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var totalDeleted int64
		var errs []error

		if stores.Webhooks != nil && retentionCfg.WebhookDays > 0 {
			deleted, err := stores.Webhooks.DeleteExpired(ctx, retentionCfg.WebhookDays)
			if err != nil {
				logger.Error().Err(err).Msg("failed to delete expired webhook deliveries")
				errs = append(errs, fmt.Errorf("delete expired webhook deliveries: %w", err))
			} else {
				totalDeleted += deleted
				logger.Info().Int64("deleted", deleted).Int("retention_days", retentionCfg.WebhookDays).Msg("webhook delivery cleanup complete")
			}
		}

		if stores.SessionLogs != nil && retentionCfg.LogsDays > 0 {
			deleted, err := stores.SessionLogs.DeleteExpired(ctx, retentionCfg.LogsDays)
			if err != nil {
				logger.Error().Err(err).Msg("failed to delete expired session logs")
				errs = append(errs, fmt.Errorf("delete expired session logs: %w", err))
			} else {
				totalDeleted += deleted
				logger.Info().Int64("deleted", deleted).Int("retention_days", retentionCfg.LogsDays).Msg("session log cleanup complete")
			}
		}

		if stores.Jobs != nil && retentionCfg.JobsDays > 0 {
			deleted, err := stores.Jobs.DeleteExpiredCompleted(ctx, retentionCfg.JobsDays)
			if err != nil {
				logger.Error().Err(err).Msg("failed to delete expired completed jobs")
				errs = append(errs, fmt.Errorf("delete expired completed jobs: %w", err))
			} else {
				totalDeleted += deleted
				logger.Info().Int64("deleted", deleted).Int("retention_days", retentionCfg.JobsDays).Msg("completed job cleanup complete")
			}
		}

		logger.Info().Int64("total_deleted", totalDeleted).Msg("data retention cleanup complete")

		if len(errs) > 0 {
			return fmt.Errorf("data retention cleanup had %d error(s): %w", len(errs), errors.Join(errs...))
		}
		return nil
	}
}

func parseOrgID(orgIDFromPayload string, ctx context.Context) (uuid.UUID, error) {
	if orgIDFromPayload != "" {
		return uuid.Parse(orgIDFromPayload)
	}
	orgID, ok := jobOrgIDFromContext(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("missing org ID")
	}
	return orgID, nil
}

// publishEvalBatchSignal best-effort publishes an EvalBatchUpdatedEvent over
// Redis pub/sub so the eval-batch detail page's SSE wakes immediately. The
// event is intentionally minimal — clients re-fetch the full EvalBatchDetail
// via the existing GET handler on receipt — so this stays cheap to fan out
// even when many runs in the same batch finish in quick succession. Errors
// are logged at warn and swallowed: the database row is the source of truth
// and the page falls back to polling if SSE is unavailable. batchID may be
// the zero UUID when called from a code path with no batch context, in which
// case this is a no-op. The published channel is keyed on batchID so
// unrelated batch viewers don't fan out.
func publishEvalBatchSignal(ctx context.Context, services *Services, orgID, batchID uuid.UUID, status models.EvalBatchStatus, logger zerolog.Logger) {
	if services == nil || services.EvalBatchStreams == nil || batchID == uuid.Nil {
		return
	}
	if err := services.EvalBatchStreams.PublishUpdated(ctx, models.EvalBatchUpdatedEvent{
		BatchID:   batchID,
		OrgID:     orgID,
		Status:    status,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		logger.Warn().Err(err).Str("batch_id", batchID.String()).Msg("failed to publish eval batch update event")
	}
}

// publishEvalBootstrapSignal mirrors publishEvalBatchSignal for bootstrap runs.
func publishEvalBootstrapSignal(ctx context.Context, services *Services, orgID, bootstrapRunID uuid.UUID, status models.EvalBootstrapStatus, sessionID *uuid.UUID, logger zerolog.Logger) {
	if services == nil || services.EvalBootstrapStreams == nil || bootstrapRunID == uuid.Nil {
		return
	}
	if err := services.EvalBootstrapStreams.PublishUpdated(ctx, models.EvalBootstrapUpdatedEvent{
		BootstrapRunID: bootstrapRunID,
		OrgID:          orgID,
		Status:         status,
		SessionID:      sessionID,
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		logger.Warn().Err(err).Str("bootstrap_run_id", bootstrapRunID.String()).Msg("failed to publish eval bootstrap update event")
	}
}

// run_eval handler executes a single eval run: clones repo, runs agent, scores output.
func newRunEvalHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			EvalRunID string `json:"eval_run_id"`
			OrgID     string `json:"org_id"`
			BatchID   string `json:"batch_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal run_eval payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.EvalRunID)
		if err != nil {
			return fmt.Errorf("parse eval run ID: %w", err)
		}

		run, err := stores.EvalRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch eval run: %w", err)
		}

		task, err := stores.EvalTasks.GetByID(ctx, orgID, run.TaskID)
		if err != nil {
			return fmt.Errorf("fetch eval task: %w", err)
		}

		logger.Info().
			Str("eval_run_id", runID.String()).
			Str("eval_task_id", task.ID.String()).
			Str("model", run.Model).
			Str("org_id", orgID.String()).
			Msg("starting run_eval job")

		// Mark run as running
		if err := stores.EvalRuns.UpdateStatus(ctx, orgID, runID, models.EvalRunStatusRunning); err != nil {
			return fmt.Errorf("update eval run status to running: %w", err)
		}
		// Wake any batch detail SSE subscribers so the matrix flips this
		// run's tile to "running" without a polling round-trip. Batch
		// itself stays "running"; we surface the parent batch's status
		// rather than the run's so subscribers can ignore stale runs.
		if run.BatchID != nil {
			publishEvalBatchSignal(ctx, services, orgID, *run.BatchID, models.EvalBatchStatusRunning, logger)
		}

		startTime := time.Now()

		// Execute the eval. This is the core eval execution flow:
		// 1. Create sandbox
		// 2. Clone repo at base_commit_sha
		// 3. Optionally overlay config from config_ref
		// 4. Run the coding agent with the issue_description
		// 5. Collect the agent's diff
		// 6. Score using each criterion
		// 7. Compute final weighted score
		//
		// For now, the orchestrator integration is wired up as a placeholder.
		// The full implementation will use the sandbox and agent adapter infrastructure
		// once the eval-specific orchestrator flow is built.

		result := executeEvalRun(ctx, stores, services, &run, &task, logger)
		duration := int(time.Since(startTime).Seconds())
		result.DurationSeconds = &duration

		if err := stores.EvalRuns.UpdateResult(ctx, orgID, runID, result); err != nil {
			return fmt.Errorf("update eval run result: %w", err)
		}

		// If this run is part of a batch, atomically complete the batch if all runs are done
		if run.BatchID != nil && stores.EvalBatches != nil {
			if err := stores.EvalBatches.CompleteBatchIfDone(ctx, orgID, *run.BatchID); err != nil {
				logger.Warn().Err(err).Str("batch_id", run.BatchID.String()).Msg("failed to check/complete batch")
			}
			// Re-fetch so the published event carries the post-CompleteBatchIfDone
			// status. CompleteBatchIfDone only flips the batch when all runs are
			// terminal, so most signals here will still be `running` — that's
			// fine: the event is a "something changed" wake, the client fetches
			// the full detail to see which run completed.
			//
			// Concurrency note: Redis pub/sub is at-most-once and unordered.
			// Two runs in the same batch finishing nearly simultaneously can
			// publish their "running" / "completed" events out of order — A
			// re-reads `running`, B then flips to `completed` and publishes,
			// A's earlier `running` arrives last. Subscribers must NOT read
			// the Status field from the event; the event is a wake signal
			// and the canonical state lives in Postgres. The frontend
			// invalidate-and-refetch pattern in batch/[id]/page.tsx is what
			// makes this self-healing — every event triggers a fresh GET on
			// /evals/batch/{id}, so the worst case is one extra round-trip
			// before the UI converges.
			batchStatus := models.EvalBatchStatusRunning
			if batch, err := stores.EvalBatches.GetByID(ctx, orgID, *run.BatchID); err == nil {
				batchStatus = batch.Status
			} else {
				logger.Warn().Err(err).Str("batch_id", run.BatchID.String()).Msg("failed to re-read batch after run completion; publishing with running status")
			}
			publishEvalBatchSignal(ctx, services, orgID, *run.BatchID, batchStatus, logger)
		}

		logger.Info().
			Str("eval_run_id", runID.String()).
			Str("status", string(result.Status)).
			Msg("completed run_eval job")

		return nil
	}
}

// executeEvalRun performs the actual eval execution. Returns the result to store.
// This is separated from the handler for testability and to keep the handler focused on job lifecycle.
//
// Flow:
//  1. Resolve repository (clone URL + GitHub token)
//  2. Create sandbox, clone repo at base_commit_sha
//  3. Apply config overlay from run.ConfigRef if set
//  4. Run the coding agent with the issue description
//  5. Collect the agent's diff
//  6. Grade each criterion (code_check or llm_judge)
//  7. Compute weighted final score
func executeEvalRun(ctx context.Context, stores *Stores, services *Services, run *models.EvalRun, task *models.EvalTask, logger zerolog.Logger) *models.EvalRunResult {
	// Parse scoring criteria
	var criteria []models.ScoringCriterion
	if err := json.Unmarshal(task.ScoringCriteria, &criteria); err != nil {
		return evalFailed("failed to parse scoring criteria: %v", err)
	}

	// 1. Resolve repository
	if stores.Repositories == nil {
		return evalFailed("repository store not configured")
	}
	repo, err := stores.Repositories.GetByID(ctx, task.OrgID, task.RepoID)
	if err != nil {
		return evalFailed("fetch repository: %v", err)
	}

	if services == nil || services.SandboxProvider == nil {
		return evalFailed("sandbox provider not configured")
	}
	if services.GitHub == nil {
		return evalFailed("github token provider not configured")
	}

	ghToken, err := services.GitHub.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return evalFailed("get installation token: %v", err)
	}

	// 2. Create sandbox
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.Timeout = 10 * time.Minute // evals may take longer than default 5min
	sb, err := services.SandboxProvider.Create(ctx, sandboxCfg)
	if err != nil {
		return evalFailed("create sandbox: %v", err)
	}
	defer func() {
		if destroyErr := services.SandboxProvider.Destroy(ctx, sb); destroyErr != nil {
			logger.Warn().Err(destroyErr).Msg("failed to destroy eval sandbox")
		}
	}()

	// Clone repo and checkout base commit
	if err := services.SandboxProvider.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, ghToken); err != nil {
		return evalFailed("clone repo: %v", err)
	}

	// Validate BaseCommitSHA as defense-in-depth (API layer also validates)
	if !validGitSHA.MatchString(task.BaseCommitSHA) {
		return evalFailed("invalid base commit SHA format: %s", task.BaseCommitSHA)
	}

	var stderr bytes.Buffer
	exitCode, err := services.SandboxProvider.Exec(ctx, sb, fmt.Sprintf("git checkout %s", task.BaseCommitSHA), io.Discard, &stderr)
	if err != nil || exitCode != 0 {
		// Mark the task as snapshot_broken so the UI can surface it
		if stores.EvalTasks != nil {
			if markErr := stores.EvalTasks.MarkSnapshotBroken(ctx, task.OrgID, task.ID, true); markErr != nil {
				logger.Warn().Err(markErr).Msg("failed to mark eval task as snapshot_broken")
			}
		}
		return evalFailed("checkout base commit %s: exit=%d err=%v stderr=%s", task.BaseCommitSHA, exitCode, err, stderr.String())
	}

	// 3. Apply config overlay from run.ConfigRef if set
	if run.ConfigRef != nil && *run.ConfigRef != "" {
		applyConfigOverlay(ctx, services.SandboxProvider, sb, *run.ConfigRef, logger)
	}

	// 4. Run the coding agent
	agentDiff, agentTrace, tokenUsage := runCodingAgent(ctx, services, sb, run.Model, task.IssueDescription, logger)

	// 5. If agent produced no diff, collect it explicitly
	if agentDiff == "" {
		var diffBuf bytes.Buffer
		_, _ = services.SandboxProvider.Exec(ctx, sb, "git diff HEAD", &diffBuf, io.Discard)
		agentDiff = diffBuf.String()
	}

	// 6. Grade each criterion
	criterionResults := make([]models.CriterionResult, 0, len(criteria))
	for _, c := range criteria {
		var result models.CriterionResult
		switch c.GraderType {
		case models.GraderTypeCodeCheck:
			result = gradeCodeCheck(ctx, services.SandboxProvider, sb, c, logger)
		case models.GraderTypeLLMJudge:
			result = gradeLLMJudge(ctx, services.LLM, c, agentDiff, task, logger)
		default:
			result = models.CriterionResult{
				Name:    c.Name,
				Score:   0,
				Pass:    false,
				Details: fmt.Sprintf("unknown grader type: %s", c.GraderType),
			}
		}
		criterionResults = append(criterionResults, result)
	}

	// 7. Compute weighted final score
	finalScore, passed := computeWeightedScore(criteria, criterionResults, task.PassThreshold)

	// Build input manifest
	manifest := buildEvalManifest(task, run)
	manifestJSON, _ := json.Marshal(manifest)

	criterionJSON, _ := json.Marshal(criterionResults)
	traceJSON, _ := json.Marshal(agentTrace)
	usageJSON, _ := json.Marshal(tokenUsage)
	sandboxID := sb.ID

	return &models.EvalRunResult{
		Status:           models.EvalRunStatusCompleted,
		AgentDiff:        &agentDiff,
		AgentTrace:       traceJSON,
		TokenUsage:       usageJSON,
		CriterionResults: criterionJSON,
		FinalScore:       &finalScore,
		Passed:           &passed,
		SandboxID:        &sandboxID,
		InputManifest:    manifestJSON,
	}
}

// evalFailed returns an EvalRunResult with failed status and a formatted error message.
func evalFailed(format string, args ...any) *models.EvalRunResult {
	errMsg := fmt.Sprintf(format, args...)
	return &models.EvalRunResult{
		Status:       models.EvalRunStatusFailed,
		ErrorMessage: &errMsg,
	}
}

// validGitSHA matches short and full hex SHA hashes.
var validGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// validConfigRef matches branch names, tags, and SHAs safe for shell use.
var validConfigRef = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// applyConfigOverlay overlays repo config files from a branch/SHA onto the sandbox.
// Config paths: AGENTS.md, CLAUDE.md, .claude/, .143/
func applyConfigOverlay(ctx context.Context, provider agent.SandboxProvider, sb *agent.Sandbox, configRef string, logger zerolog.Logger) {
	// Validate configRef to prevent shell injection
	if !validConfigRef.MatchString(configRef) {
		logger.Warn().Str("config_ref", configRef).Msg("invalid config_ref, skipping overlay")
		return
	}

	// Fetch the config ref first
	var stderr bytes.Buffer
	if _, err := provider.Exec(ctx, sb, fmt.Sprintf("git fetch origin %s", configRef), io.Discard, &stderr); err != nil {
		logger.Debug().Err(err).Str("config_ref", configRef).Msg("git fetch for config overlay failed (non-fatal)")
	}

	// Overlay individual config files — failures are non-fatal (file may not exist on branch)
	configFiles := []string{"AGENTS.md", "CLAUDE.md"}
	for _, f := range configFiles {
		cmd := fmt.Sprintf("git show %s:%s > %s 2>/dev/null || true", configRef, f, f)
		if _, err := provider.Exec(ctx, sb, cmd, io.Discard, io.Discard); err != nil {
			logger.Debug().Err(err).Str("config_file", f).Msg("config file overlay failed (non-fatal)")
		}
	}

	// Overlay config directories
	configDirs := []string{".claude", ".143"}
	for _, d := range configDirs {
		// Remove existing dir and recreate from config ref
		cmd := fmt.Sprintf("rm -rf %s && git checkout %s -- %s 2>/dev/null || true", d, configRef, d)
		if _, err := provider.Exec(ctx, sb, cmd, io.Discard, io.Discard); err != nil {
			logger.Debug().Err(err).Str("config_dir", d).Msg("config dir overlay failed (non-fatal)")
		}
	}

	logger.Debug().Str("config_ref", configRef).Msg("applied config overlay")
}

// validModelName matches alphanumeric model identifiers with dashes and dots (no shell metacharacters).
var validModelName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// runCodingAgent executes the coding agent CLI in the sandbox and returns the diff, trace, and token usage.
func runCodingAgent(ctx context.Context, services *Services, sb *agent.Sandbox, model, issueDescription string, logger zerolog.Logger) (diff string, trace map[string]any, tokenUsage map[string]any) {
	// Validate model name to prevent shell injection
	if !validModelName.MatchString(model) {
		return "", map[string]any{"error": "invalid model name"}, nil
	}

	// Write issue description to a temp file to avoid shell injection via interpolation
	var writeStderr bytes.Buffer
	writeCmd := fmt.Sprintf("cat > /tmp/issue_description.txt << 'ISSUE_EOF'\n%s\nISSUE_EOF", strings.ReplaceAll(issueDescription, "'", "'\\''"))
	if _, writeErr := services.SandboxProvider.Exec(ctx, sb, writeCmd, io.Discard, &writeStderr); writeErr != nil {
		return "", map[string]any{"error": fmt.Sprintf("write issue description: %v", writeErr)}, nil
	}

	// Run Claude Code CLI with --print flag, reading issue from file
	cmd := fmt.Sprintf("claude --model %s --print \"$(cat /tmp/issue_description.txt)\" 2>&1", model)

	var stdout, stderr bytes.Buffer
	exitCode, err := services.SandboxProvider.Exec(ctx, sb, cmd, &stdout, &stderr)

	trace = map[string]any{
		"agent_stdout": truncateString(stdout.String(), 50000),
		"agent_stderr": truncateString(stderr.String(), 10000),
		"exit_code":    exitCode,
	}
	if err != nil {
		trace["exec_error"] = err.Error()
	}

	logger.Info().
		Int("exit_code", exitCode).
		Int("stdout_len", stdout.Len()).
		Msg("agent execution completed")

	// Collect the diff produced by the agent
	var diffBuf bytes.Buffer
	_, _ = services.SandboxProvider.Exec(ctx, sb, "git diff HEAD", &diffBuf, io.Discard)

	return diffBuf.String(), trace, nil
}

// gradeCodeCheck runs a code_check criterion command in the sandbox.
// Exit code 0 = pass (score 1.0), non-zero = fail (score 0.0).
// If stdout contains valid JSON with a "score" field, that numeric score is used instead.
func gradeCodeCheck(ctx context.Context, provider agent.SandboxProvider, sb *agent.Sandbox, criterion models.ScoringCriterion, logger zerolog.Logger) models.CriterionResult {
	var config models.CodeCheckConfig
	if err := json.Unmarshal(criterion.GraderConfig, &config); err != nil {
		return models.CriterionResult{
			Name:    criterion.Name,
			Score:   0,
			Pass:    false,
			Details: fmt.Sprintf("invalid code_check config: %v", err),
		}
	}
	if config.Command == "" {
		return models.CriterionResult{
			Name:    criterion.Name,
			Score:   0,
			Pass:    false,
			Details: "code_check config is missing required 'command' field",
		}
	}

	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	gradeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	exitCode, execErr := provider.Exec(gradeCtx, sb, config.Command, &stdout, &stderr)

	pass := exitCode == 0 && execErr == nil
	score := 0.0
	if pass {
		score = 1.0
	}

	// Try to parse optional JSON score from stdout
	var jsonResult struct {
		Score *float64 `json:"score"`
	}
	if json.Unmarshal(stdout.Bytes(), &jsonResult) == nil && jsonResult.Score != nil {
		score = *jsonResult.Score
		pass = score >= 0.5
	}

	details := fmt.Sprintf("exit_code=%d\nstdout:\n%s\nstderr:\n%s",
		exitCode, truncateString(stdout.String(), 5000), truncateString(stderr.String(), 5000))

	if execErr != nil {
		details += fmt.Sprintf("\nexec_error: %v", execErr)
	}

	logger.Debug().
		Str("criterion", criterion.Name).
		Int("exit_code", exitCode).
		Float64("score", score).
		Bool("pass", pass).
		Msg("code_check graded")

	return models.CriterionResult{
		Name:    criterion.Name,
		Score:   score,
		Pass:    pass,
		Details: details,
	}
}

// gradeLLMJudge evaluates a criterion using an LLM judge.
// The criterion's Notes field is the rubric. The judge returns pass/fail + reasoning.
func gradeLLMJudge(ctx context.Context, llm llmClient, criterion models.ScoringCriterion, agentDiff string, task *models.EvalTask, logger zerolog.Logger) models.CriterionResult {
	if llm == nil {
		return models.CriterionResult{
			Name:    criterion.Name,
			Score:   0,
			Pass:    false,
			Details: "LLM client not configured — cannot run llm_judge grader",
		}
	}

	var config models.LLMJudgeConfig
	if err := json.Unmarshal(criterion.GraderConfig, &config); err != nil {
		// Default config is fine — just use pass_fail output mode
		config.Output = "pass_fail"
	}
	if config.Output == "" {
		config.Output = "pass_fail"
	}

	systemPrompt := prompts.EvalJudgePrompt(prompts.EvalJudgePromptData{
		OutputMode: config.Output,
	})

	var solutionDiff string
	if task.SolutionDiff != nil {
		solutionDiff = *task.SolutionDiff
	}

	userPrompt := prompts.EvalJudgeUserPrompt(prompts.EvalJudgeUserPromptData{
		IssueDescription: task.IssueDescription,
		AgentDiff:        agentDiff,
		CriterionName:    criterion.Name,
		CriterionNotes:   criterion.Notes,
		SolutionDiff:     solutionDiff,
	})

	judgeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	response, err := llm.Complete(judgeCtx, systemPrompt, userPrompt)
	if err != nil {
		return models.CriterionResult{
			Name:    criterion.Name,
			Score:   0,
			Pass:    false,
			Details: fmt.Sprintf("LLM judge call failed: %v", err),
		}
	}

	// Parse structured response
	var judgment struct {
		Pass      bool    `json:"pass"`
		Score     float64 `json:"score"`
		Reasoning string  `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(response), &judgment); err != nil {
		// Try to extract JSON from markdown-fenced response
		cleaned := extractJSON(response)
		if err2 := json.Unmarshal([]byte(cleaned), &judgment); err2 != nil {
			return models.CriterionResult{
				Name:      criterion.Name,
				Score:     0,
				Pass:      false,
				Details:   fmt.Sprintf("failed to parse judge response: %v", err),
				Reasoning: truncateString(response, 5000),
			}
		}
	}

	// For pass_fail mode, score is binary
	if config.Output != "score" {
		if judgment.Pass {
			judgment.Score = 1.0
		} else {
			judgment.Score = 0.0
		}
	}

	logger.Debug().
		Str("criterion", criterion.Name).
		Float64("score", judgment.Score).
		Bool("pass", judgment.Pass).
		Msg("llm_judge graded")

	return models.CriterionResult{
		Name:      criterion.Name,
		Score:     judgment.Score,
		Pass:      judgment.Pass,
		Reasoning: judgment.Reasoning,
	}
}

// computeWeightedScore calculates the final weighted score and pass/fail status.
// If any required criterion fails, the eval fails regardless of score.
func computeWeightedScore(criteria []models.ScoringCriterion, results []models.CriterionResult, passThreshold float64) (float64, bool) {
	if len(results) == 0 {
		return 0, false
	}

	// Build a lookup from criterion name to result
	resultMap := make(map[string]models.CriterionResult, len(results))
	for _, r := range results {
		resultMap[r.Name] = r
	}

	// Check required criteria first
	for _, c := range criteria {
		if c.Required {
			if r, ok := resultMap[c.Name]; ok && !r.Pass {
				// Required criterion failed — compute score but force fail
				score := weightedAverage(criteria, resultMap)
				return score, false
			}
		}
	}

	score := weightedAverage(criteria, resultMap)
	passed := score >= passThreshold
	return score, passed
}

// weightedAverage computes a normalized weighted average score.
func weightedAverage(criteria []models.ScoringCriterion, results map[string]models.CriterionResult) float64 {
	var totalWeight, weightedSum float64
	for _, c := range criteria {
		w := c.Weight
		if w <= 0 {
			w = 1.0 // default weight
		}
		totalWeight += w
		if r, ok := results[c.Name]; ok {
			weightedSum += w * r.Score
		}
	}
	if totalWeight == 0 {
		return 0
	}
	return weightedSum / totalWeight
}

// buildEvalManifest constructs the input manifest for an eval run.
func buildEvalManifest(task *models.EvalTask, run *models.EvalRun) *models.InputManifest {
	manifest := &models.InputManifest{
		ServerDeploySHA:   version.BuildSHA,
		RepoBaseCommitSHA: task.BaseCommitSHA,
		Model:             run.Model,
	}
	if task.PMDocumentSetPinID != nil {
		manifest.PMDocumentSetPinID = task.PMDocumentSetPinID
	}
	if task.OrgSettingsVersionID != nil {
		manifest.OrgSettingsVersionID = task.OrgSettingsVersionID
	}
	if task.SandboxImageDigest != nil {
		manifest.SandboxImageDigest = *task.SandboxImageDigest
	}
	if task.MemorySnapshot != nil {
		var memSnap models.MemorySnapshot
		if json.Unmarshal(task.MemorySnapshot, &memSnap) == nil {
			manifest.MemorySnapshot = &memSnap
		}
	}
	return manifest
}

// extractJSON attempts to extract a JSON object from a string that may contain
// markdown code fences or other wrapper text.
func extractJSON(s string) string {
	// Find the first { and last }
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// truncateString shortens a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// bootstrapLogWriter is a helper that writes session log entries for a bootstrap run.
type bootstrapLogWriter struct {
	store     *db.SessionLogStore
	sessionID uuid.UUID
	orgID     uuid.UUID
}

func (w *bootstrapLogWriter) log(ctx context.Context, level, message string) {
	if w.store == nil || w.sessionID == uuid.Nil {
		return
	}
	entry := &models.SessionLog{
		SessionID:  w.sessionID,
		OrgID:      w.orgID,
		Level:      models.SessionLogLevel(level),
		Message:    message,
		TurnNumber: 0,
	}
	// Best-effort: don't fail the bootstrap if logging fails.
	_ = w.store.Create(ctx, entry)
}

// run_eval_bootstrap handler scans PR history to discover eval task candidates.
func newRunEvalBootstrapHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			BootstrapRunID string `json:"bootstrap_run_id"`
			OrgID          string `json:"org_id"`
			RepoID         string `json:"repo_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal bootstrap payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		bootstrapRunID, err := uuid.Parse(input.BootstrapRunID)
		if err != nil {
			return fmt.Errorf("parse bootstrap run ID: %w", err)
		}
		repoID, err := uuid.Parse(input.RepoID)
		if err != nil {
			return fmt.Errorf("parse repo ID: %w", err)
		}

		logger.Info().
			Str("bootstrap_run_id", bootstrapRunID.String()).
			Str("repo_id", repoID.String()).
			Msg("starting eval bootstrap scan")

		// Create a lightweight session to store bootstrap logs.
		title := "Bootstrap: scanning PR history"
		session := &models.Session{
			OrgID:         orgID,
			AgentType:     models.AgentTypeClaudeCode,
			Status:        models.SessionStatusRunning,
			AutonomyLevel: models.SessionAutonomyFull,
			TokenMode:     models.SessionTokenModeLow,
			Title:         &title,
			RepositoryID:  &repoID,
		}
		if err := stores.Sessions.Create(ctx, session); err != nil {
			logger.Warn().Err(err).Msg("failed to create bootstrap session for logging, continuing without logs")
		}

		// Mark as running and link the session.
		var sessionIDPtr *uuid.UUID
		if session.ID != uuid.Nil {
			sessionIDPtr = &session.ID
		}
		if err := stores.EvalBootstraps.UpdateStatus(ctx, orgID, bootstrapRunID, models.EvalBootstrapStatusRunning, sessionIDPtr); err != nil {
			return fmt.Errorf("update bootstrap status to running: %w", err)
		}
		publishEvalBootstrapSignal(ctx, services, orgID, bootstrapRunID, models.EvalBootstrapStatusRunning, sessionIDPtr, logger)

		logWriter := &bootstrapLogWriter{
			store:     stores.SessionLogs,
			sessionID: session.ID,
			orgID:     orgID,
		}

		candidates, scanErr := executeBootstrapScan(ctx, stores, services, orgID, repoID, logWriter, logger)

		if scanErr != nil {
			errMsg := scanErr.Error()
			logWriter.log(ctx, "error", fmt.Sprintf("Bootstrap scan failed: %s", errMsg))
			if session.ID != uuid.Nil {
				_ = stores.Sessions.UpdateStatus(ctx, orgID, session.ID, models.SessionStatusFailed)
			}
			if updateErr := stores.EvalBootstraps.UpdateResult(ctx, orgID, bootstrapRunID,
				models.EvalBootstrapStatusFailed, nil, &errMsg); updateErr != nil {
				logger.Warn().Err(updateErr).Msg("failed to update bootstrap run with error")
			}
			publishEvalBootstrapSignal(ctx, services, orgID, bootstrapRunID, models.EvalBootstrapStatusFailed, sessionIDPtr, logger)
			return fmt.Errorf("bootstrap scan failed: %w", scanErr)
		}

		candidatesJSON, _ := json.Marshal(candidates)
		if err := stores.EvalBootstraps.UpdateResult(ctx, orgID, bootstrapRunID,
			models.EvalBootstrapStatusCompleted, candidatesJSON, nil); err != nil {
			return fmt.Errorf("update bootstrap result: %w", err)
		}
		publishEvalBootstrapSignal(ctx, services, orgID, bootstrapRunID, models.EvalBootstrapStatusCompleted, sessionIDPtr, logger)

		logWriter.log(ctx, "info", fmt.Sprintf("Bootstrap scan completed successfully. Found %d candidates.", len(candidates)))
		if session.ID != uuid.Nil {
			_ = stores.Sessions.UpdateStatus(ctx, orgID, session.ID, models.SessionStatusCompleted)
		}

		logger.Info().
			Int("candidates", len(candidates)).
			Msg("eval bootstrap scan completed")

		return nil
	}
}

// executeBootstrapScan runs the PR history scan using an agent in a sandbox.
func executeBootstrapScan(ctx context.Context, stores *Stores, services *Services, orgID, repoID uuid.UUID, logWriter *bootstrapLogWriter, logger zerolog.Logger) ([]models.EvalBootstrapCandidate, error) {
	if stores.Repositories == nil {
		return nil, fmt.Errorf("repository store not configured")
	}
	logWriter.log(ctx, "info", "Fetching repository details...")
	repo, err := stores.Repositories.GetByID(ctx, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("fetch repository: %w", err)
	}
	logWriter.log(ctx, "info", fmt.Sprintf("Repository: %s", repo.FullName))

	if services.GitHub == nil {
		return nil, fmt.Errorf("github token provider not configured")
	}
	logWriter.log(ctx, "info", "Obtaining GitHub access token...")
	ghToken, err := services.GitHub.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}

	// Create sandbox with longer timeout for bootstrap
	logWriter.log(ctx, "info", "Creating sandbox environment...")
	sandboxCfg := agent.DefaultSandboxConfig()
	sandboxCfg.Timeout = 15 * time.Minute
	sb, err := services.SandboxProvider.Create(ctx, sandboxCfg)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		if destroyErr := services.SandboxProvider.Destroy(ctx, sb); destroyErr != nil {
			logger.Warn().Err(destroyErr).Msg("failed to destroy bootstrap sandbox")
		}
	}()

	// Clone repo at HEAD (need full history for git log analysis)
	logWriter.log(ctx, "info", fmt.Sprintf("Cloning repository %s (full history for git log analysis)...", repo.FullName))
	if err := services.SandboxProvider.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, ghToken); err != nil {
		return nil, fmt.Errorf("clone repo: %w", err)
	}
	logWriter.log(ctx, "info", "Repository cloned successfully.")

	// Run the bootstrap agent using Claude Code CLI
	logWriter.log(ctx, "info", "Starting bootstrap analysis — scanning merged PRs for eval candidates...")
	bootstrapPrompt := prompts.EvalBootstrapPrompt(prompts.EvalBootstrapPromptData{
		RepoFullName: repo.FullName,
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := bootstrapAgentCommand(bootstrapPrompt)
	exitCode, execErr := services.SandboxProvider.ExecStream(ctx, sb, cmd, func(line []byte) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			return
		}
		// Write each output line as a log entry so the user can follow along.
		logWriter.log(ctx, "assistant", trimmed)
		// Also accumulate the full output for JSON parsing.
		stdout.Write(line)
		stdout.WriteByte('\n')
	}, &stderr)
	if execErr != nil {
		return nil, fmt.Errorf("execute bootstrap agent: %w", execErr)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("bootstrap agent failed with exit code %d, stderr: %s", exitCode, truncateString(stderr.String(), 1000))
	}

	logger.Info().
		Int("exit_code", exitCode).
		Int("stdout_len", stdout.Len()).
		Msg("bootstrap agent execution completed")

	logWriter.log(ctx, "info", "Analysis complete. Parsing candidates...")

	// Parse the agent's structured output
	var candidates []models.EvalBootstrapCandidate
	output := stdout.String()

	// Try to extract JSON array from agent output
	jsonStr := extractJSON(output)
	// Try array first
	if err := json.Unmarshal([]byte(jsonStr), &candidates); err != nil {
		// Try to find a JSON array within the output
		start := strings.Index(output, "[")
		end := strings.LastIndex(output, "]")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(output[start:end+1]), &candidates); err2 != nil {
				return nil, fmt.Errorf("failed to parse bootstrap output as candidate array: %w (raw output: %s)", err2, truncateString(output, 1000))
			}
		} else {
			return nil, fmt.Errorf("no JSON array found in bootstrap output: %s", truncateString(output, 1000))
		}
	}

	return candidates, nil
}

// shellSingleQuote wraps s in single quotes so the shell treats it as a
// literal. Embedded single quotes are closed, escaped, and reopened
// ('\”). This is safe for strings containing backticks, $, backslashes,
// or other shell metacharacters.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bootstrapAgentCommand builds the sh command that runs Claude Code with
// the bootstrap prompt. The prompt is single-quoted so the triple-backtick
// JSON fence in the template is not interpreted as command substitution.
func bootstrapAgentCommand(prompt string) string {
	return fmt.Sprintf("claude --print %s 2>&1", shellSingleQuote(prompt))
}

// prepare_linear_primary handler resolves the primary Linear issue for a
// session that came through CreateManual when inline resolution either
// missed the latency budget or hit a transient error. Marks the session
// linear_prepare_state=ready so run_agent unblocks.
func newPrepareLinearPrimaryHandler(svc *linear.Service, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID                   string           `json:"org_id"`
			SessionID               string           `json:"session_id"`
			Identifiers             []string         `json:"identifiers"`
			Refs                    []linear.LinkRef `json:"refs,omitempty"`
			UserID                  string           `json:"user_id,omitempty"`
			AllowRepositoryMismatch bool             `json:"allow_repository_mismatch,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal prepare_linear_primary payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		var userID *uuid.UUID
		if input.UserID != "" {
			if parsed, err := uuid.Parse(input.UserID); err == nil {
				userID = &parsed
			} else {
				// Audit attribution (added_by_user_id, "linked by you" filters)
				// drops to nil here, so log loudly rather than silently
				// degrading. The job still proceeds because the link is
				// strictly more useful than a parse-error retry loop.
				logger.Warn().Err(err).
					Str("session_id", sessionID.String()).
					Str("user_id_raw", input.UserID).
					Msg("prepare_linear_primary: malformed user_id in payload; proceeding with nil attribution")
			}
		}
		refs := input.Refs
		if len(refs) == 0 {
			refs = linearRefsFromIdentifiers(input.Identifiers)
		}
		invalidLinkMessage := "Linear issue could not be linked to this session's repository. The Linear issue appears to belong to another repository; choose the matching repository or start a manual session with your intended repository selected."
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, _ error) {
			if err := svc.MarkLinearPrepareFailed(hookCtx, orgID, sessionID); err != nil {
				logger.Warn().Err(err).
					Str("session_id", sessionID.String()).
					Msg("prepare_linear_primary dead-letter hook failed to mark prepare state failed")
			}
		})
		if err := svc.PrepareLinearPrimaryRefsWithOptions(ctx, orgID, sessionID, refs, userID, linear.LinkOptions{AllowRepositoryMismatch: input.AllowRepositoryMismatch}); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("prepare_linear_primary failed")
			if errors.Is(err, linear.ErrIntegrationNotFound) {
				// Integration was disconnected/removed between enqueue and
				// pickup. Retrying for 8 minutes won't make the row appear;
				// dead-letter immediately so the prepare-state hook can
				// unblock run_agent.
				return &FatalError{Err: err}
			}
			if errors.Is(err, linear.ErrUnauthorized) {
				// Flip the integration to errored on the very first 401 so the
				// settings UI shows a Reconnect CTA without waiting for retry
				// exhaustion. Doing this inside the retry loop (vs only the
				// dead-letter hook) is intentional: retries are 5s apart and
				// exhaustion takes minutes, but the user is staring at the
				// session-detail page waiting for context to load.
				svc.MarkIntegrationUnauthorized(ctx, orgID)
				// Token won't fix itself in 8 minutes — dead-letter so we
				// don't fire the dead-letter hook minutes after the user
				// already saw "Reconnect" in settings.
				return &FatalError{Err: err}
			}
			if errors.Is(err, db.ErrInvalidSessionIssueLink) {
				if markErr := svc.MarkLinearPrepareFailedWithError(ctx, orgID, sessionID, invalidLinkMessage); markErr != nil {
					logger.Warn().Err(markErr).
						Str("session_id", sessionID.String()).
						Msg("prepare_linear_primary failed to persist invalid-link message")
				}
				return &FatalError{Err: fmt.Errorf("%s: %w", invalidLinkMessage, err)}
			}
			return &RetryableError{Err: err}
		}
		return nil
	}
}

// link_linear_issue handler is the post-create catch-up path. It runs
// detection again over the bounded inputs, links any additional refs as
// related, and is idempotent on (session_id, source_inputs_hash).
func newLinkLinearIssueHandler(svc *linear.Service, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID       string           `json:"org_id"`
			SessionID   string           `json:"session_id"`
			Identifiers []string         `json:"identifiers"`
			Refs        []linear.LinkRef `json:"refs,omitempty"`
			UserID      string           `json:"user_id,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal link_linear_issue payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		var userID *uuid.UUID
		if input.UserID != "" {
			if parsed, err := uuid.Parse(input.UserID); err == nil {
				userID = &parsed
			} else {
				// See prepare_linear_primary handler for the rationale on
				// logging instead of failing — same trade-off applies here.
				logger.Warn().Err(err).
					Str("session_id", sessionID.String()).
					Str("user_id_raw", input.UserID).
					Msg("link_linear_issue: malformed user_id in payload; proceeding with nil attribution")
			}
		}
		refs := input.Refs
		if len(refs) == 0 {
			refs = linearRefsFromIdentifiers(input.Identifiers)
		}
		if err := svc.LinkRelatedLinearRefs(ctx, orgID, sessionID, refs, userID); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("link_linear_issue failed")
			// linkRelatedRefs (the inner loop) swallows per-ref ResolvePrimary
			// errors with Warn().continue, so ErrIntegrationNotFound /
			// ErrUnauthorized never reach this caller. Any error here is
			// pre-loop wiring (e.g. missing service deps) — let the worker
			// retry under the default backoff.
			return &RetryableError{Err: err}
		}
		return nil
	}
}

// link_linear_issue_mid_session handler links Linear refs detected in a
// follow-up message body. Distinct from link_linear_issue because the
// post-create catch-up path treats refs[0] as the already-linked primary; the
// mid-session path has no such primary in the payload, so all refs are
// candidate related links.
func newLinkLinearIssueMidSessionHandler(svc *linear.Service, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID       string           `json:"org_id"`
			SessionID   string           `json:"session_id"`
			Identifiers []string         `json:"identifiers"`
			Refs        []linear.LinkRef `json:"refs,omitempty"`
			UserID      string           `json:"user_id,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal link_linear_issue_mid_session payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		var userID *uuid.UUID
		if input.UserID != "" {
			if parsed, err := uuid.Parse(input.UserID); err == nil {
				userID = &parsed
			} else {
				// See prepare_linear_primary handler for the rationale on
				// logging instead of failing — same trade-off applies here.
				logger.Warn().Err(err).
					Str("session_id", sessionID.String()).
					Str("user_id_raw", input.UserID).
					Msg("link_linear_issue_mid_session: malformed user_id in payload; proceeding with nil attribution")
			}
		}
		refs := input.Refs
		if len(refs) == 0 {
			refs = linearRefsFromIdentifiers(input.Identifiers)
		}
		if err := svc.LinkMidSessionRefs(ctx, orgID, sessionID, refs, userID); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("link_linear_issue_mid_session failed")
			// LinkMidSessionRefs runs through linkRelatedRefs which swallows
			// per-ref errors; reaching this branch means a pre-loop wiring
			// failure that's worth retrying.
			return &RetryableError{Err: err}
		}
		return nil
	}
}

func linearRefsFromIdentifiers(identifiers []string) []linear.LinkRef {
	refs := make([]linear.LinkRef, 0, len(identifiers))
	for _, ident := range identifiers {
		refs = append(refs, linear.LinkRef{Identifier: ident})
	}
	return refs
}

// linear_milestone handler fires the Linear writes (attachment + rolling
// comment + state-sync transitions under guards) for a session milestone.
// Decoupled from the synchronous PR webhook path so retries are cheap and
// rate-limit failures don't cascade to GitHub event handling.
func newLinearMilestoneHandler(stores *Stores, svc *linear.Service, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID     string `json:"org_id"`
			SessionID string `json:"session_id"`
			Event     string `json:"event"`
			PRNumber  int    `json:"pr_number,omitempty"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal linear_milestone payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("fetch session: %w", err)
		}
		// Hydrate linked issues so HandleMilestone can pick the primary
		// link. SessionStore.GetByID doesn't include them by default.
		if stores.SessionIssueLinks != nil {
			if links, listErr := stores.SessionIssueLinks.ListBySession(ctx, orgID, sessionID); listErr == nil {
				session.LinkedIssues = links
			} else {
				return fmt.Errorf("list linear session issue links: %w", listErr)
			}
		}
		var primary *models.SessionIssueLink
		for i := range session.LinkedIssues {
			if session.LinkedIssues[i].Role == models.SessionIssueLinkRolePrimary {
				primary = &session.LinkedIssues[i]
				break
			}
		}
		if primary == nil {
			// No primary link at all — log so operators chasing "why
			// didn't Linear update?" don't have to grep the worker for
			// silence. Common when the link was removed before the
			// milestone fired; benign.
			logger.Info().Str("session_id", sessionID.String()).Msg("linear_milestone: no primary link; skipping")
			return nil
		}
		// Resolve the Linear external ID + identifier from the issue.
		issue, err := stores.Issues.GetByID(ctx, orgID, primary.IssueID)
		if err != nil {
			return fmt.Errorf("fetch primary linear issue: %w", err)
		}
		if issue.Source != models.IssueSourceLinear {
			// The primary link points at a non-Linear issue (data drift —
			// usually means the issue was re-sourced). Log loudly: this
			// shouldn't happen for a job dispatched as `linear_milestone`,
			// and silent return makes operator debugging painful.
			logger.Warn().
				Str("session_id", sessionID.String()).
				Str("issue_id", primary.IssueID.String()).
				Str("issue_source", string(issue.Source)).
				Msg("linear_milestone: primary issue is not a Linear issue; skipping")
			return nil
		}
		identifier := ""
		if primary.ExternalID != nil {
			identifier = *primary.ExternalID
		}
		if identifier == "" {
			identifier = issue.ExternalID
		}

		// SessionURL is built inside the linear.Service from its configured
		// AppBaseURL — the worker doesn't have the FRONTEND_URL plumbed
		// here and would otherwise post a relative path that Linear renders
		// as plain text.
		event := linear.MilestoneEvent(input.Event)
		in := linear.MilestoneInput{
			Event:      event,
			Session:    &session,
			Link:       *primary,
			IssueID:    issue.ExternalID,
			PRNumber:   input.PRNumber,
			IssueIdent: identifier,
		}
		if err := svc.HandleMilestone(ctx, in); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("HandleMilestone failed")
			if errors.Is(err, linear.ErrUnauthorized) {
				svc.MarkIntegrationUnauthorized(ctx, orgID)
			}
			return mapLinearWriteErrorToRetry(err)
		}
		if err := svc.HandleStateTransition(ctx, in); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("HandleStateTransition failed")
			if errors.Is(err, linear.ErrUnauthorized) {
				svc.MarkIntegrationUnauthorized(ctx, orgID)
			}
			return mapLinearWriteErrorToRetry(err)
		}
		// HandleAgentMilestone is a no-op for sessions not triggered through
		// the inbound agent path. It's deliberately last + best-effort: the
		// durable handles (attachment + rolling comment) are what
		// HandleMilestone wrote above, and an agent-stream emit failure
		// must not retry-cascade the milestone job and risk re-firing the
		// idempotent-but-not-free attachment update.
		if err := svc.HandleAgentMilestone(ctx, in); err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("HandleAgentMilestone failed")
		}
		return nil
	}
}

// mapLinearWriteErrorToRetry classifies a Linear write error into the
// FatalError / RetryableError shape the worker expects. 429s carry a
// Retry-After hint; integration-not-found and unauthorized are fatal because
// retrying for the 8-minute max duration won't bring the row back or
// re-grant the token; everything else falls through to default exponential
// backoff.
//
// On ErrUnauthorized the caller is responsible for invoking
// svc.MarkIntegrationUnauthorized — done at each call site rather than here
// so this helper stays a pure error-classifier without the orgID dependency.
func mapLinearWriteErrorToRetry(err error) error {
	var rate *linear.RateLimitError
	if errors.As(err, &rate) {
		delay := 30 * time.Second
		if d, parseErr := strconv.Atoi(rate.RetryAfter); parseErr == nil && d > 0 {
			delay = time.Duration(d) * time.Second
		}
		return &RetryableError{Err: err, RetryAfter: &delay}
	}
	if errors.Is(err, linear.ErrIntegrationNotFound) || errors.Is(err, linear.ErrUnauthorized) {
		return &FatalError{Err: err}
	}
	return &RetryableError{Err: err}
}

// refresh_linear_team_keys handler refreshes the per-org team-key cache.
// Scheduled every 24h and after OAuth install. Idempotent.
//
// Doubles as the periodic Linear health probe the codebase has been
// missing: a successful refresh clears any stale "needs reauth" banner; a
// 401 flips the integration to errored so the settings UI surfaces a
// Reconnect CTA without waiting for the next session create to fail.
func newRefreshLinearTeamKeysHandler(svc *linear.Service, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal refresh_linear_team_keys payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		if err := svc.RefreshTeamKeys(ctx, orgID); err != nil {
			logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("refresh_linear_team_keys failed")
			if errors.Is(err, linear.ErrIntegrationNotFound) {
				// Org disconnected Linear after the 24h cron tick was
				// scheduled. Retrying for 8 minutes won't bring the row
				// back; dead-letter so the cron can re-arm cleanly the
				// next time an install enqueues this job.
				return &FatalError{Err: err}
			}
			if errors.Is(err, linear.ErrUnauthorized) {
				svc.MarkIntegrationUnauthorized(ctx, orgID)
				// Token won't recover in 8 minutes. The MarkIntegrationUnauthorized
				// call above already surfaced the Reconnect CTA in settings;
				// dead-letter immediately rather than burning retries.
				return &FatalError{Err: err}
			}
			return &RetryableError{Err: err}
		}
		// Refresh succeeded — token works. Clear any stale auth-error
		// banner. No-op when the row is already healthy.
		svc.ClearIntegrationUnauthorized(ctx, orgID)
		return nil
	}
}
