package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/agent"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/assembledhq/143/internal/services/externalidentity"
	"github.com/assembledhq/143/internal/services/feedback"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/linear"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/assembledhq/143/internal/services/pm"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/assembledhq/143/internal/services/prioritization"
	readinesssvc "github.com/assembledhq/143/internal/services/readiness"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	slackbotsvc "github.com/assembledhq/143/internal/services/slackbot"
	"github.com/assembledhq/143/internal/services/storage"
)

const sandboxCapacityRetryDelay = 10 * time.Second
const previewCapacityRetryDelay = 5 * time.Second
const previewStartupInterruptedRetryDelay = 2 * time.Second
const prePRReviewRetryDelay = 5 * time.Second

// prePRReviewMaxWait bounds how long a readiness run will requeue itself waiting
// for the agent review loop to finish. The wait uses BypassMaxRetryDuration +
// non-consuming retries, so without this deadline a review loop that never
// reaches a clean snapshot would requeue every prePRReviewRetryDelay forever
// (re-running custom-check LLM calls each time). Past the deadline the run is
// marked failed instead.
const prePRReviewMaxWait = 30 * time.Minute
const defaultSessionPrewarmQueuedTimeout = 15 * time.Minute
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
		enqueueSlackSessionNotifications(writeCtx, stores, logger, session.OrgID, session.ID, session.AutomationRunID, string(models.SlackNotificationSessionFailed), "143 session failed", errMsg)
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
		enqueueSlackSessionNotifications(writeCtx, stores, logger, failedSession.OrgID, failedSession.ID, failedSession.AutomationRunID, string(models.SlackNotificationSessionFailed), "143 session failed", errMsg)
	})
}

// DataRetentionConfig holds retention periods for the data cleanup handler.
type DataRetentionConfig struct {
	WebhookDays              int
	LogsDays                 int
	JobsDays                 int
	SlackInboundPayloadDays  int
	SlackInboundPayloadBatch int
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
	w.Register("slack_add_session_reaction", newSlackAddSessionReactionHandler(stores, logger))
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
	if stores.CodeReviews != nil {
		w.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(stores, services, logger))
	}
	if services != nil && services.PagerDuty != nil {
		w.Register(models.JobTypePagerDutyIngestEvent, newPagerDutyIngestEventHandler(services.PagerDuty, logger))
	}
	if services != nil && services.PagerDutySync != nil {
		w.Register(models.JobTypePagerDutySync, newPagerDutySyncHandler(services.PagerDutySync, logger))
	}
	if stores.GitHubInstallations != nil && services != nil && services.GitHubOrgRoster != nil {
		w.Register(models.JobTypeSyncGitHubOrgRoster, newSyncGitHubOrgRosterHandler(stores, services, logger))
	}
	if services != nil && services.PreviewStarter != nil {
		w.Register(models.JobTypeStartPreview, newStartPreviewHandler(stores, services, logger))
		w.Register(models.JobTypeStartBranchPreview, newStartBranchPreviewHandler(stores, services, logger))
		if services.AutoPreviewStarter != nil {
			w.Register(models.JobTypeAutoPreviewDeferred, newAutoPreviewDeferredHandler(stores, services, logger))
		}
		w.Register(models.JobTypePreviewCachePrewarm, newPreviewCachePrewarmHandler(services, logger))
		w.Register(models.JobTypeSessionPreviewWarmBuild, newSessionPreviewWarmBuildHandler(services, logger))
	}
	if stores != nil && stores.Sessions != nil && stores.Repositories != nil && stores.Previews != nil && stores.Jobs != nil && services != nil && (services.SessionPrewarmClassifier != nil || services.LLM != nil) {
		w.Register(models.JobTypeSessionPreviewPrewarmClassify, newSessionPreviewPrewarmClassifyHandler(stores, services, logger))
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
		w.Register("run_pr_readiness", newRunPRReadinessHandler(stores, services, logger))
		w.Register("sync_pull_request_state", newSyncPullRequestStateHandler(services, logger))
		w.Register("reconcile_pull_request_state", newReconcilePullRequestStateHandler(services, logger))
		w.Register("enrich_pull_request_health", newEnrichPullRequestHealthHandler(services, logger))
		w.Register("merge_pull_request_when_ready", newMergePullRequestWhenReadyHandler(services, logger))
		w.Register(models.JobTypeSyncPRPreviewSurfaces, newSyncPRPreviewSurfacesHandler(services, logger))
		w.Register("analyze_failure", newAnalyzeFailureHandler(stores, services, logger))
		w.Register("fork_session_thread", newForkSessionThreadHandler(stores, services, logger))
		w.Register("revert_session_thread", newRevertSessionThreadHandler(stores, services, logger))
	}
	if stores != nil && stores.EvalRuns != nil && stores.EvalTasks != nil {
		w.Register("run_eval_grader", newEvalGraderHandler(stores, services, logger))
		w.Register("run_eval", newLegacyRunEvalCompatHandler(stores, logger))
	}
	if stores != nil && stores.EvalBootstraps != nil {
		w.Register("run_eval_bootstrap", newLegacyRunEvalBootstrapCompatHandler(stores, logger))
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
	if stores.Previews != nil {
		w.Register(models.JobTypeBackfillPreviewGroups, newBackfillPreviewGroupsHandler(stores, logger))
	}
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
	Projects            *db.ProjectStore         // nil-safe: projects feature disabled if nil
	ProjectTasks        *db.ProjectTaskStore     // nil-safe
	Credentials         *db.OrgCredentialStore   // nil-safe: needed for sync_slack
	AuditLogs           *db.AuditLogStore        // nil-safe: audit retention cleanup
	Organizations       *db.OrganizationStore    // nil-safe: needed for audit retention
	SessionLogs         *db.SessionLogStore      // nil-safe: data retention cleanup
	EvalTasks           *db.EvalTaskStore        // nil-safe: eval feature
	EvalRuns            *db.EvalRunStore         // nil-safe: eval feature
	EvalBatches         *db.EvalBatchStore       // nil-safe: eval feature
	EvalBootstraps      *db.EvalBootstrapStore   // nil-safe: eval bootstrap feature
	EvalReleaseGates    *db.EvalReleaseGateStore // nil-safe: eval release gates
	Repositories        *db.RepositoryStore      // nil-safe: needed for eval repo lookup
	GitHubInstallations *db.GitHubInstallationStore
	SessionMessages     *db.SessionMessageStore // nil-safe: needed for title regeneration
	SessionThreads      *db.SessionThreadStore  // nil-safe: needed for thread-scoped continuation status
	HumanInputRequests  *db.SessionHumanInputRequestStore
	ThreadFileEvents    *db.SessionThreadFileEventStore // nil-safe: tab-level file write attribution
	SandboxHolders      *db.SessionSandboxHolderStore   // nil-safe: snapshot quiescence for shared sandbox thread runtimes
	IssueSnapshots      *db.SessionTurnIssueSnapshotStore
	Automations         *db.AutomationStore    // nil-safe: automations feature disabled if nil
	AutomationRuns      *db.AutomationRunStore // nil-safe: automations feature disabled if nil
	ReviewLoops         *db.SessionReviewLoopStore
	PRReadiness         *db.PRReadinessStore
	CodeReviews         *db.CodeReviewStore
	SessionIssueLinks   *db.SessionIssueLinkStore // nil-safe: needed for Linear milestones
	Previews            *db.PreviewStore
	PullRequests        *db.PullRequestStore
	SlackInstallations  *db.SlackInstallationStore
	SlackOrgSelections  *db.SlackOrgSelectionStore
	SlackBotSettings    *db.SlackBotSettingsStore
	SlackUserLinks      *db.SlackUserLinkStore
	LinearUserLinks     *db.LinearUserLinkStore
	ExternalUserLinks   *db.ExternalUserLinkStore
	ExternalSuggestions *db.ExternalUserLinkSuggestionStore
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

func enqueueRunAgentForSession(ctx context.Context, stores *Stores, session models.Session) error {
	if stores == nil || stores.Jobs == nil {
		return errors.New("run_agent enqueue store unavailable")
	}
	if session.ID == uuid.Nil {
		return errors.New("session id is required to enqueue run_agent")
	}
	if session.OrgID == uuid.Nil {
		return errors.New("org id is required to enqueue run_agent")
	}
	dedupeKey := db.RunAgentDedupeKey(session.ID)
	if _, err := stores.Jobs.Enqueue(ctx, session.OrgID, "agent", "run_agent", db.RunAgentPayload(&session), 5, &dedupeKey); err != nil {
		return fmt.Errorf("enqueue run_agent: %w", err)
	}
	return nil
}

// MemoryReinforcer retrieves and reinforces memories for a repo.
type MemoryReinforcer interface {
	GetContextMemories(ctx context.Context, req agent.MemoryContextRequest) (*agent.MemoryContextResult, error)
	ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error
}

type SandboxAuthBroker interface {
	Acquire(ctx context.Context, orgID, sessionID, holderID uuid.UUID) (string, error)
	Release(ctx context.Context, orgID, sessionID, holderID uuid.UUID) error
}

type pagerDutyEventProcessor interface {
	ProcessInboundEvent(ctx context.Context, orgID, eventID uuid.UUID) error
}

type pagerDutySyncer interface {
	SyncOrg(ctx context.Context, orgID uuid.UUID) (pagerdutysvc.SyncResult, error)
}

type pagerDutyPRWritebacker interface {
	OnPROpened(ctx context.Context, session models.Session, pr models.PullRequest) error
}

type prCreator interface {
	CreatePR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	CreateBranch(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*ghservice.CreateBranchResult, error)
	PushChangesToPR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	SyncPullRequestState(ctx context.Context, orgID, pullRequestID uuid.UUID) error
	ReconcilePullRequestState(ctx context.Context, orgID uuid.UUID, limit int) error
	EnrichPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64) error
	CompletePullRequestRepairRun(ctx context.Context, orgID, pullRequestID, repairRunID uuid.UUID) error
	MaybeStartAutoRepair(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, reason string) (*ghservice.AutoRepairDecision, error)
	QueueMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error)
	ProcessMergeWhenReady(ctx context.Context, orgID, pullRequestID uuid.UUID) error
	SyncPRPreviewSurfaces(ctx context.Context, payload ghservice.SyncPRPreviewSurfacesPayload) error
	// WaitForPostPRSnapshotUploads blocks until any in-flight post-PR
	// snapshot uploads (spawned by CreatePR) have either promoted or
	// cleared their pending_snapshot_key. Called by the server's graceful
	// shutdown path so a worker exit doesn't strand sessions with the
	// pending key set forever.
	WaitForPostPRSnapshotUploads()
}

type codeReviewSubmitter interface {
	SubmitReview(ctx context.Context, req codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error)
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
	GitHubOrgRoster githubOrgRosterService        // nil-safe: needed for GitHub org auto-join roster sync
	Snapshots       storage.SnapshotStore         // nil-safe: needed for eval code_check grading
	TitleService    *services.SessionTitleService // nil-safe: session title regeneration
	Linear          *linear.Service               // nil-safe: Linear session-linking disabled if nil
	PagerDuty       pagerDutyEventProcessor       // nil-safe: PagerDuty incident ingestion disabled if nil
	PagerDutySync   pagerDutySyncer               // nil-safe: PagerDuty reconciliation disabled if nil
	PagerDutyWrites pagerDutyPRWritebacker        // nil-safe: PagerDuty writeback disabled if nil
	CodeReviews     codeReviewSubmitter           // nil-safe: GitHub review submission disabled if nil
	SlackbotMetrics *metrics.SlackbotMetrics      // nil-safe: Slackbot observability disabled if nil
	// Redis is optional and used for non-authoritative shared caches such as
	// Slack user display names. Losing it should only increase provider lookups.
	Redis       *cache.Client
	FrontendURL string // optional base URL for Slack links
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
	// SandboxAuthBroker is the worker-owned lease manager exposed through
	// signed internal RPCs for detached session executors.
	SandboxAuthBroker SandboxAuthBroker
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
	// AutoPreviewStarter handles deferred auto-preview starts (E6 queue-at-cap).
	// nil-safe: deferred auto_preview_deferred jobs are no-ops if not configured.
	AutoPreviewStarter ghservice.AutoPreviewStarter
	// PreviewCachePrewarmEnabled gates opportunistic low-priority prewarm
	// enqueues from successful session turns.
	PreviewCachePrewarmEnabled bool
	// PreviewCachePrewarmPriority is the job priority for opportunistic prewarm
	// work; production defaults it below foreground preview starts.
	PreviewCachePrewarmPriority int
	// PreviewCachePrewarmTimeout bounds speculative prewarm reservations before
	// they are swept as failed.
	PreviewCachePrewarmTimeout time.Duration
	// SessionPrewarmClassifier optionally overrides the default platform-LLM
	// classifier used by smart session preview prewarm jobs.
	SessionPrewarmClassifier sessionPrewarmClassifier
	// PreviewController handles control-plane actions for already-created
	// previews. nil when preview control is unavailable on this process.
	PreviewController previewController
	// SlackPreviewControl creates and opens previews from Slack actions using
	// the same product control plane as the web preview surfaces.
	SlackPreviewControl SlackPreviewControl

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

type githubOrgRosterService interface {
	ListOrgMembers(ctx context.Context, installationID int64, orgLogin string) ([]ghservice.OrgMember, error)
}

type previewStarter interface {
	StartReservedPreview(ctx context.Context, payload previewsvc.StartPreviewJobPayload) error
	StartReservedBranchPreview(ctx context.Context, payload previewsvc.StartBranchPreviewJobPayload) error
	PrewarmPreviewCaches(ctx context.Context, payload previewsvc.PreviewCachePrewarmJobPayload) error
	WarmSessionPreview(ctx context.Context, payload previewsvc.SessionPreviewWarmBuildJobPayload) error
}

type previewController interface {
	RecyclePreview(ctx context.Context, orgID, previewID uuid.UUID) error
	StopPreview(ctx context.Context, orgID, previewID uuid.UUID) error
	SetLifetime(ctx context.Context, orgID, previewID uuid.UUID, duration time.Duration) (time.Time, error)
}

type SlackPreviewControl interface {
	CreatePreviewForSlack(ctx context.Context, orgID uuid.UUID, target models.SlackPreviewTarget, actor models.SlackActor) (models.PreviewInstance, error)
	OpenPreviewURL(ctx context.Context, orgID, previewID uuid.UUID, actor models.SlackActor) (string, error)
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

type sessionPrewarmClassifier interface {
	Classify(ctx context.Context, input previewsvc.SessionPrewarmClassifierInput) previewsvc.SessionPrewarmClassifierResult
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
			return &FatalError{Err: err}
		}
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
			if errors.Is(err, previewsvc.ErrPreviewStartupInterrupted) {
				retryAfter := previewStartupInterruptedRetryDelay
				logger.Info().
					Err(err).
					Str("preview_id", input.PreviewID.String()).
					Str("preview_target_id", input.PreviewTargetID.String()).
					Dur("retry_after", retryAfter).
					Msg("preview startup interrupted; retrying start_branch_preview on a fresh worker selection")
				return &RetryableError{
					Err:                    err,
					ConsumeAttempt:         true,
					BypassMaxRetryDuration: true,
					RetryAfter:             &retryAfter,
					ClearTargetNodeID:      true,
				}
			}
			return &FatalError{Err: err}
		}
		return nil
	}
}

// newAutoPreviewDeferredHandler retries an auto-preview start that was
// deferred because the auto-pool was full at webhook time. It re-checks
// capacity at dequeue time and backs off with a RetryableError when the
// pool is still saturated, so auto-preview starts queue naturally rather
// than being silently dropped.
func newAutoPreviewDeferredHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	retryDelay := 2 * time.Minute
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.AutoPreviewStarter == nil {
			return &FatalError{Err: fmt.Errorf("auto_preview_deferred: starter not configured")}
		}
		var input previewsvc.AutoPreviewDeferredPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal auto_preview_deferred payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("auto_preview_deferred payload missing required ids")}
		}

		// Load repo so we can call StartAutoPullRequestPreview.
		if stores == nil || stores.Repositories == nil {
			return &FatalError{Err: fmt.Errorf("auto_preview_deferred: repository store not configured")}
		}
		repo, err := stores.Repositories.GetByID(ctx, input.OrgID, input.RepositoryID)
		if err != nil {
			return &FatalError{Err: fmt.Errorf("auto_preview_deferred: load repository: %w", err)}
		}

		// Check pool capacity. If still full, back off and let the retry
		// mechanism reschedule rather than immediately calling the starter
		// (which would enqueue another deferred job, creating a chain).
		if stores.Previews != nil && stores.Organizations != nil {
			maxActive := models.DefaultPreviewAutoPoolMaxActive
			if org, orgErr := stores.Organizations.GetByID(ctx, input.OrgID); orgErr == nil {
				if settings, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil && settings.PreviewAutoPoolMaxActive > 0 {
					maxActive = settings.PreviewAutoPoolMaxActive
				}
			}
			count, countErr := stores.Previews.CountActiveAutoPreviews(ctx, input.OrgID)
			if countErr == nil && count >= maxActive {
				logger.Debug().
					Str("org_id", input.OrgID.String()).
					Int("active", count).
					Int("max", maxActive).
					Dur("retry_after", retryDelay).
					Msg("auto_preview_deferred: pool still full, backing off")
				return &RetryableError{Err: fmt.Errorf("auto-preview pool full (%d/%d)", count, maxActive), RetryAfter: &retryDelay}
			}
		}

		if err := services.AutoPreviewStarter.StartAutoPullRequestPreview(ctx, input.OrgID, input.UserID, repo, input.PRNumber, input.HeadRef, input.HeadSHA, input.HTMLURL, input.Mode, input.PreviewConfigName); err != nil {
			return fmt.Errorf("auto_preview_deferred: start preview: %w", err)
		}
		return nil
	}
}

func newPreviewCachePrewarmHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.PreviewStarter == nil {
			return &FatalError{Err: fmt.Errorf("preview starter is not configured")}
		}
		var input previewsvc.PreviewCachePrewarmJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal preview_cache_prewarm payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("preview_cache_prewarm payload missing required ids")}
		}
		switch input.Source {
		case previewsvc.PreviewCachePrewarmSourceSession:
			if input.SessionID == uuid.Nil {
				return &FatalError{Err: fmt.Errorf("preview_cache_prewarm session payload missing session_id")}
			}
		case previewsvc.PreviewCachePrewarmSourceBranch:
			if input.PreviewTargetID == uuid.Nil || input.CommitSHA == "" {
				return &FatalError{Err: fmt.Errorf("preview_cache_prewarm branch payload missing target or commit")}
			}
		default:
			return &FatalError{Err: fmt.Errorf("preview_cache_prewarm payload has invalid source %q", input.Source)}
		}
		logger.Info().
			Str("repository_id", input.RepositoryID.String()).
			Str("source", string(input.Source)).
			Msg("processing preview_cache_prewarm job")
		if err := services.PreviewStarter.PrewarmPreviewCaches(ctx, input); err != nil {
			if errors.Is(err, previewsvc.ErrPreviewCachePrewarmCapacitySkipped) || errors.Is(err, previewsvc.ErrPreviewCapacity) {
				logger.Info().Err(err).Msg("preview cache prewarm skipped due to capacity")
				return nil
			}
			return &FatalError{Err: err}

		}
		return nil
	}
}

func newSessionPreviewWarmBuildHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if services == nil || services.PreviewStarter == nil {
			return &FatalError{Err: fmt.Errorf("preview starter is not configured")}
		}
		var input previewsvc.SessionPreviewWarmBuildJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal session_preview_warm_build payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil || input.SessionID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("session_preview_warm_build payload missing required ids")}
		}
		logger.Info().
			Str("org_id", input.OrgID.String()).
			Str("repository_id", input.RepositoryID.String()).
			Str("session_id", input.SessionID.String()).
			Int64("workspace_revision", input.WorkspaceRevision).
			Msg("processing session preview warm build job")
		if err := services.PreviewStarter.WarmSessionPreview(ctx, input); err != nil {
			if errors.Is(err, previewsvc.ErrPreviewCapacity) {
				logger.Info().Err(err).Msg("session preview warm build skipped due to capacity")
				return nil
			}
			return &FatalError{Err: err}
		}
		return nil
	}
}

func newSessionPreviewPrewarmClassifyHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Sessions == nil || stores.Repositories == nil || stores.Previews == nil || stores.Jobs == nil {
			return &FatalError{Err: fmt.Errorf("session preview prewarm classifier dependencies are not configured")}
		}
		var input previewsvc.SessionPreviewPrewarmClassifyJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("unmarshal session preview prewarm classifier payload: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || input.RepositoryID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("session preview prewarm classifier payload missing required ids")}
		}
		session, err := stores.Sessions.GetByID(ctx, input.OrgID, input.SessionID)
		if err != nil {
			return fmt.Errorf("load session for prewarm classifier: %w", err)
		}
		if session.RepositoryID == nil || *session.RepositoryID != input.RepositoryID || session.WorkspaceRevision != input.WorkspaceRevision {
			status := "skipped_superseded"
			if session.RepositoryID == nil || *session.RepositoryID != input.RepositoryID {
				status = "failed"
			}
			return recordSessionPreviewPrewarmDecision(ctx, stores, session, input.JobID, input.RepositoryID, input.ConfigDigest, models.PreviewSessionPrewarmModeSmart, models.PreviewSpeculativeDecisionNone, 0, "superseded", "Session moved before classifier completed.", status)
		}
		repo, err := stores.Repositories.GetByID(ctx, input.OrgID, input.RepositoryID)
		if err != nil {
			return fmt.Errorf("load repository for prewarm classifier: %w", err)
		}
		classifier := sessionPrewarmClassifierForServices(services)
		if classifier == nil {
			return recordSessionPreviewPrewarmDecision(ctx, stores, session, input.JobID, input.RepositoryID, input.ConfigDigest, models.PreviewSessionPrewarmModeSmart, models.PreviewSpeculativeDecisionNone, 0, "classifier_error", "Classifier is not configured.", "decided")
		}
		classifierStarted := time.Now()
		configDigest := sessionPreviewKnownConfigDigest(ctx, stores, logger, session.OrgID, input.RepositoryID)
		historicalOpenCount := sessionPrewarmHistoricalOpenCount(ctx, stores, logger, session)
		result := classifier.Classify(ctx, previewsvc.SessionPrewarmClassifierInput{
			RepositoryFullName:         repo.FullName,
			RepositoryLanguage:         stringPtrValue(repo.Language),
			PreviewConfigDigest:        configDigest,
			SessionSource:              sessionPrewarmSource(session),
			UserPrompt:                 sessionPrewarmPromptSeed(ctx, stores, logger, session),
			IssueLabels:                sessionPrewarmIssueLabels(ctx, stores, logger, session),
			IssueType:                  sessionPrewarmIssueType(ctx, stores, logger, session),
			PreviewHistory:             sessionPrewarmPreviewHistory(ctx, stores, logger, session),
			HistoricalPreviewOpenCount: historicalOpenCount,
			CapacitySummary:            sessionPrewarmCapacitySummary(ctx, stores, logger, session.OrgID),
			ChangedFileKinds:           sessionPrewarmChangedFileKinds(session),
			Phase:                      input.Phase,
		})
		metrics.RecordSessionPreviewClassifierLatency(ctx, input.OrgID.String(), input.Phase, time.Since(classifierStarted))
		if result.Status == "" {
			result.Status = "decided"
		}
		input.ConfigDigest = configDigest
		if err := recordSessionPreviewPrewarmDecision(ctx, stores, session, input.JobID, input.RepositoryID, input.ConfigDigest, models.PreviewSessionPrewarmModeSmart, result.Decision, result.Confidence, result.Reason, result.Explanation, result.Status); err != nil {
			return err
		}
		if result.Decision == models.PreviewSpeculativeDecisionCache {
			enqueueSessionPreviewCachePrewarmForDecision(ctx, stores, services, logger, session, models.PreviewSessionPrewarmModeSmart, result.Decision, result.Confidence, result.Reason, result.Explanation, nil)
		} else if result.Decision == models.PreviewSpeculativeDecisionWarmCandidate && strings.EqualFold(input.Phase, "post_turn") {
			enqueueSessionPreviewWarmBuildIfCandidate(ctx, stores, services, logger, input.OrgID, input.SessionID, "post_turn_classifier")
		}
		return nil
	}
}

func sessionPrewarmClassifierForServices(services *Services) sessionPrewarmClassifier {
	if services == nil {
		return nil
	}
	if services.SessionPrewarmClassifier != nil {
		return services.SessionPrewarmClassifier
	}
	if services.LLM == nil {
		return nil
	}
	return previewsvc.NewSessionPrewarmClassifier(services.LLM, 5*time.Second)
}

func recordSessionPreviewPrewarmDecision(ctx context.Context, stores *Stores, session models.Session, jobID, repositoryID uuid.UUID, configDigest string, mode models.PreviewSessionPrewarmMode, decision models.PreviewSpeculativeDecision, confidence float64, reason, explanation, status string) error {
	if stores == nil || stores.Previews == nil {
		return nil
	}
	var jobIDPtr *uuid.UUID
	if jobID != uuid.Nil {
		jobIDCopy := jobID
		jobIDPtr = &jobIDCopy
	}
	_, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             session.OrgID,
		RepositoryID:      repositoryID,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      configDigest,
		Mode:              mode,
		Decision:          decision,
		Confidence:        confidence,
		Reason:            reason,
		Explanation:       explanation,
		Status:            status,
		JobID:             jobIDPtr,
		CapacitySnapshot:  json.RawMessage(`{}`),
	})
	if err != nil {
		return fmt.Errorf("record session preview prewarm classifier decision: %w", err)
	}
	metrics.RecordSessionPreviewPrewarmDecision(ctx, session.OrgID.String(), string(mode), string(decision), sessionPrewarmSource(session), reason)
	return nil
}

func sessionPrewarmSource(session models.Session) string {
	if session.AutomationRunID != nil {
		return "automation"
	}
	if session.ProjectTaskID != nil {
		return "project"
	}
	if session.LinearIdentifierHint != nil && strings.TrimSpace(*session.LinearIdentifierHint) != "" {
		return "linear"
	}
	if session.Origin != "" {
		return string(session.Origin)
	}
	return "manual"
}

func sessionPrewarmQueuedTimeout(services *Services) time.Duration {
	if services != nil && services.PreviewCachePrewarmTimeout > 0 {
		return services.PreviewCachePrewarmTimeout
	}
	return defaultSessionPrewarmQueuedTimeout
}

func expireStaleSessionPreviewPrewarmRuns(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID uuid.UUID) {
	if stores == nil || stores.Previews == nil || orgID == uuid.Nil {
		return
	}
	timeout := sessionPrewarmQueuedTimeout(services)
	rows, err := stores.Previews.ExpireStaleQueuedSessionPreviewPrewarmRuns(ctx, orgID, time.Now().Add(-timeout))
	if err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to expire stale queued session preview prewarm runs")
		return
	}
	if rows > 0 {
		logger.Info().Int64("count", rows).Str("org_id", orgID.String()).Msg("expired stale queued session preview prewarm runs")
	}
}

func sessionPreviewKnownConfigDigest(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, repoID uuid.UUID) string {
	if stores == nil || stores.Previews == nil || orgID == uuid.Nil || repoID == uuid.Nil {
		return ""
	}
	digest, err := stores.Previews.GetLatestRepositoryPreviewConfigDigest(ctx, orgID, repoID)
	if err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).Str("repository_id", repoID.String()).Msg("failed to load latest preview config digest for session prewarm")
		return ""
	}
	return strings.TrimSpace(digest)
}

func sessionPreviewPrewarmEligible(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) bool {
	if session.RepositoryID == nil || *session.RepositoryID == uuid.Nil {
		return false
	}
	if session.SnapshotKey != nil && strings.TrimSpace(*session.SnapshotKey) != "" {
		return true
	}
	if stores == nil || stores.Previews == nil {
		return false
	}
	eligible, err := stores.Previews.HasRepositoryPreviewCacheEligibility(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to check repository preview cache eligibility")
		return false
	}
	return eligible
}

func sessionPrewarmCapacitySummary(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID) string {
	if stores == nil || stores.Previews == nil || orgID == uuid.Nil {
		return "unknown"
	}
	active, err := stores.Previews.CountActiveSessionPreviewPrewarmRuns(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to count active session preview prewarm runs for classifier")
		return "unknown"
	}
	maxActive := 0
	if stores.Organizations != nil {
		org, orgErr := stores.Organizations.GetByID(ctx, orgID)
		if orgErr != nil {
			logger.Warn().Err(orgErr).Str("org_id", orgID.String()).Msg("failed to load org settings for classifier capacity summary")
		} else if settings, parseErr := models.ParseOrgSettings(org.Settings); parseErr != nil {
			logger.Warn().Err(parseErr).Str("org_id", orgID.String()).Msg("failed to parse org settings for classifier capacity summary")
		} else {
			maxActive = settings.PreviewSessionPrewarmMaxActive
		}
	}
	cacheSkips := 0
	if stores.Previews != nil {
		if count, skipErr := stores.Previews.CountRecentSessionPreviewCacheSkips(ctx, orgID, time.Now().Add(-15*time.Minute)); skipErr != nil {
			logger.Warn().Err(skipErr).Str("org_id", orgID.String()).Msg("failed to count recent session preview cache skips for classifier")
		} else {
			cacheSkips = count
		}
	}
	if maxActive <= 0 {
		return fmt.Sprintf("active=%d max=unknown recent_cache_skips=%d", active, cacheSkips)
	}
	workerSaturation := "unknown"
	if stores.Jobs != nil {
		capacity, capErr := stores.Jobs.SandboxCapacitySummary(ctx)
		if capErr != nil {
			logger.Warn().Err(capErr).Str("org_id", orgID.String()).Msg("failed to load sandbox capacity summary for classifier")
		} else if capacity.MaxSandboxes > 0 {
			used := capacity.LiveSandboxes + capacity.ReservedSandboxes
			workerSaturation = fmt.Sprintf("workers=%d workers_with_headroom=%d sandboxes=%d/%d", capacity.FreshWorkers, capacity.WorkersWithSlots, used, capacity.MaxSandboxes)
		} else {
			workerSaturation = fmt.Sprintf("workers=%d workers_with_headroom=%d sandboxes=unknown", capacity.FreshWorkers, capacity.WorkersWithSlots)
		}
	}
	return fmt.Sprintf("active=%d max=%d remaining=%d worker_saturation=%s recent_cache_skips=%d", active, maxActive, max(0, maxActive-active), workerSaturation, cacheSkips)
}

func sessionPreviewHasSpeculativeWorkerHeadroom(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID) bool {
	if stores == nil || stores.Jobs == nil {
		return true
	}
	capacity, err := stores.Jobs.SandboxCapacitySummary(ctx)
	if err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to load sandbox capacity for session preview prewarm")
		return false
	}
	return capacity.WorkersWithSlots > 0
}

func sessionPrewarmHistoricalOpenCount(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) int {
	if stores == nil || stores.Previews == nil || session.OrgID == uuid.Nil || session.RepositoryID == nil {
		return 0
	}
	count, err := stores.Previews.CountSessionsWithPanelOpenedBySource(ctx, session.OrgID, *session.RepositoryID, sessionPrewarmSource(session))
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to count sessions with panel opened for classifier")
		return 0
	}
	return count
}

func sessionPrewarmPreviewHistory(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) string {
	if stores == nil || stores.Previews == nil || session.OrgID == uuid.Nil || session.ID == uuid.Nil {
		return "unknown"
	}
	parts := make([]string, 0, 4)
	if session.RepositoryID != nil {
		successes, failures, err := stores.Previews.CountRecentRepositoryPreviewOutcomes(ctx, session.OrgID, *session.RepositoryID, time.Now().Add(-14*24*time.Hour))
		if err != nil {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load repository preview outcome history")
		} else {
			parts = append(parts, fmt.Sprintf("repo_recent_success=%d repo_recent_failure=%d", successes, failures))
		}
		if digest := sessionPreviewKnownConfigDigest(ctx, stores, logger, session.OrgID, *session.RepositoryID); digest != "" {
			parts = append(parts, "preview_config=known")
		} else {
			parts = append(parts, "preview_config=unknown")
		}
	}
	for _, decision := range []models.PreviewSpeculativeDecision{
		models.PreviewSpeculativeDecisionCache,
		models.PreviewSpeculativeDecisionWarmCandidate,
	} {
		run, err := stores.Previews.GetLatestSessionPreviewPrewarmRun(ctx, session.OrgID, session.ID, decision)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Warn().Err(err).Str("session_id", session.ID.String()).Str("decision", string(decision)).Msg("failed to load session preview prewarm history")
			}
			continue
		}
		staleness := "current"
		if run.WorkspaceRevision != session.WorkspaceRevision {
			staleness = "stale"
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%s", decision, run.Status, staleness))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func sessionPrewarmPromptSeed(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) string {
	if session.PMApproach != nil && strings.TrimSpace(*session.PMApproach) != "" {
		return *session.PMApproach
	}
	if stores == nil || stores.SessionMessages == nil {
		return ""
	}
	messages, err := stores.SessionMessages.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load messages for session preview prewarm classifier")
		return ""
	}
	for _, msg := range messages {
		if msg.Role == models.MessageRoleUser && strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}
	}
	return ""
}

func sessionPrewarmIssueLabels(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) []string {
	if stores == nil || stores.SessionIssueLinks == nil || stores.Issues == nil {
		return nil
	}
	links, err := stores.SessionIssueLinks.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load issue links for session preview prewarm classifier")
		return nil
	}
	if len(links) == 0 {
		return nil
	}
	issueIDs := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		if link.IssueID != uuid.Nil {
			issueIDs = append(issueIDs, link.IssueID)
		}
	}
	issues, err := stores.Issues.ListByIDs(ctx, session.OrgID, issueIDs)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load issue labels for session preview prewarm classifier")
		return nil
	}
	seen := map[string]struct{}{}
	labels := make([]string, 0)
	for _, issue := range issues {
		for _, tag := range issue.Tags {
			label := strings.TrimSpace(tag)
			if label == "" {
				continue
			}
			key := strings.ToLower(label)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			labels = append(labels, label)
		}
	}
	return labels
}

func sessionPrewarmIssueType(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session) string {
	if stores == nil || stores.ComplexityEstimates == nil || session.PrimaryIssueID == nil || *session.PrimaryIssueID == uuid.Nil {
		return ""
	}
	estimate, err := stores.ComplexityEstimates.GetByIssueID(ctx, session.OrgID, *session.PrimaryIssueID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load issue type for session preview prewarm classifier")
		}
		return ""
	}
	if estimate.IssueType == nil {
		return ""
	}
	return strings.TrimSpace(*estimate.IssueType)
}

func sessionPrewarmChangedFileKinds(session models.Session) []string {
	if session.Diff == nil || strings.TrimSpace(*session.Diff) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	for _, event := range parseDiffFileEvents(*session.Diff) {
		kind := sessionPrewarmFileKind(event.Path)
		if kind != "" {
			seen[kind] = struct{}{}
		}
	}
	order := []string{"frontend", "backend", "config", "test", "docs"}
	kinds := make([]string, 0, len(seen))
	for _, kind := range order {
		if _, ok := seen[kind]; ok {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func sessionPrewarmFileKind(path string) string {
	p := strings.ToLower(strings.TrimSpace(path))
	if p == "" {
		return ""
	}
	base := p
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	switch {
	case strings.Contains(p, "/test/") || strings.Contains(p, "/tests/") || strings.Contains(p, "__tests__/") ||
		strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.tsx") ||
		strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.tsx"):
		return "test"
	case strings.HasPrefix(p, "docs/") || strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".mdx"):
		return "docs"
	case strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".jsx") || strings.HasSuffix(base, ".css") ||
		strings.HasSuffix(base, ".scss") || strings.HasSuffix(base, ".vue") || strings.HasSuffix(base, ".svelte") ||
		strings.Contains(p, "frontend/") || strings.Contains(p, "src/app/") || strings.Contains(p, "src/components/"):
		return "frontend"
	case strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml") ||
		strings.HasSuffix(base, ".toml") || strings.HasSuffix(base, ".lock") || strings.HasPrefix(base, "dockerfile") ||
		base == "package.json" || base == "go.mod":
		return "config"
	case strings.HasSuffix(base, ".go") || strings.HasSuffix(base, ".rs") || strings.HasSuffix(base, ".py") ||
		strings.HasSuffix(base, ".rb") || strings.HasSuffix(base, ".java") || strings.HasSuffix(base, ".kt") ||
		strings.HasSuffix(base, ".php") || strings.Contains(p, "backend/") || strings.Contains(p, "internal/"):
		return "backend"
	default:
		return ""
	}
}

func sessionPreviewPrewarmUntrustedFork(session models.Session) bool {
	if len(session.InputManifest) == 0 {
		return false
	}
	var manifest map[string]any
	if err := json.Unmarshal(session.InputManifest, &manifest); err != nil {
		return false
	}
	return boolAtPath(manifest, "pull_request", "head", "repo", "fork") ||
		boolAtPath(manifest, "pull_request", "head_repo", "fork") ||
		boolAtPath(manifest, "pull_request", "head_repo_fork") ||
		boolAtPath(manifest, "github", "pull_request", "head", "repo", "fork") ||
		boolAtPath(manifest, "github", "pull_request", "head_repo_fork") ||
		boolAtPath(manifest, "untrusted_fork")
}

func sessionPreviewPrewarmBlockedByUntrustedFork(session models.Session, policy *models.RepositoryPreviewPolicy) bool {
	if !sessionPreviewPrewarmUntrustedFork(session) {
		return false
	}
	return policy == nil || !policy.SessionPrewarmUntrustedFork
}

func boolAtPath(root map[string]any, path ...string) bool {
	var current any = root
	for _, part := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = obj[part]
		if !ok {
			return false
		}
	}
	value, ok := current.(bool)
	return ok && value
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

func newPagerDutyIngestEventHandler(processor pagerDutyEventProcessor, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if processor == nil {
			return nil
		}
		var input struct {
			OrgID   string `json:"org_id"`
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal pagerduty_ingest_event payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		eventID, err := uuid.Parse(input.EventID)
		if err != nil {
			return fmt.Errorf("parse PagerDuty inbound event ID: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Str("event_id", eventID.String()).
			Msg("processing PagerDuty inbound event")
		if err := processor.ProcessInboundEvent(ctx, orgID, eventID); err != nil {
			return fmt.Errorf("process PagerDuty inbound event: %w", err)
		}
		return nil
	}
}

func newPagerDutySyncHandler(syncer pagerDutySyncer, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if syncer == nil {
			return nil
		}
		var input struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal pagerduty_sync payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Msg("running PagerDuty reconciliation")
		result, err := syncer.SyncOrg(ctx, orgID)
		if err != nil {
			return fmt.Errorf("sync PagerDuty incidents: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Int("integrations", result.IntegrationCount).
			Int("incidents", result.IncidentCount).
			Msg("PagerDuty reconciliation complete")
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
		repositoryID, err := automationRunRepositoryID(run, automation)
		if err != nil {
			now := time.Now()
			summary := err.Error()
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusFailed, &now, &summary); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to mark run failed after invalid automation trigger context")
				return fmt.Errorf("mark run failed after invalid automation trigger context: %w", updateErr)
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
		// surfaces it as the synthesized issue's description. Append run
		// metadata here because automation goals often scope themselves relative
		// to the previous execution, and the agent cannot derive that timestamp
		// reliably from the repo alone.
		goalSeed, err := automationRunPromptSeed(run)
		if err != nil {
			now := time.Now()
			summary := err.Error()
			if _, updateErr := stores.AutomationRuns.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusRunning, models.AutomationRunStatusFailed, &now, &summary); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to mark run failed after automation prompt seed error")
				return fmt.Errorf("mark run failed after automation prompt seed error: %w", updateErr)
			}
			return nil
		}

		session := &models.Session{
			OrgID:              orgID,
			AgentType:          agentType,
			Status:             "pending",
			AutonomyLevel:      models.DefaultSessionAutonomy,
			TokenMode:          "low",
			ModelOverride:      automation.ModelOverride,
			ReasoningEffort:    automation.ReasoningEffort,
			TriggeredByUserID:  sessionTriggeredByUserID,
			TargetBranch:       targetBranch,
			RepositoryID:       repositoryID,
			AutomationRunID:    &runID,
			PMApproach:         &goalSeed,
			CapabilitySnapshot: run.CapabilitySnapshot,
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

func automationRunRepositoryID(run models.AutomationRun, automation models.Automation) (*uuid.UUID, error) {
	repositoryID := automation.RepositoryID
	if len(run.TriggerContext) == 0 {
		return repositoryID, nil
	}
	var context struct {
		RepositoryID string `json:"repository_id"`
	}
	if err := json.Unmarshal(run.TriggerContext, &context); err != nil {
		return nil, fmt.Errorf("parse automation trigger context: %w", err)
	}
	raw := strings.TrimSpace(context.RepositoryID)
	if raw == "" {
		return repositoryID, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid repository_id in automation trigger context: %w", err)
	}
	return &parsed, nil
}

func automationRunPromptSeed(run models.AutomationRun) (string, error) {
	var snapshot struct {
		PreviousRunAt *string `json:"previous_run_at"`
	}
	if len(run.ConfigSnapshot) > 0 {
		if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
			return "", fmt.Errorf("parse automation config snapshot for prompt context: %w", err)
		}
	}

	previousRunAt := "none"
	if snapshot.PreviousRunAt != nil && strings.TrimSpace(*snapshot.PreviousRunAt) != "" {
		previousRunAt = strings.TrimSpace(*snapshot.PreviousRunAt)
	}

	context := fmt.Sprintf("Automation run context\n- Current automation run triggered at: %s\n- Previous automation run: %s",
		run.TriggeredAt.UTC().Format(time.RFC3339), previousRunAt)
	goal := strings.TrimSpace(run.GoalSnapshot)
	if goal == "" {
		return context, nil
	}
	return goal + "\n\n" + context, nil
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
		resolvedContext := slackbotsvc.SlackContextResolveResult{RoutingMode: slackbotsvc.SlackRoutingModeAuto}
		userResolver := newSlackCachedUserDisplayResolver(slackClient, slackRedisClient(services), slackCfg.AccessToken, payload.TeamID, logger)

		var mappedUserID *uuid.UUID
		if payload.SlackUserID != "" {
			if resolved, resolveErr := resolveExternalSlackUser(ctx, stores, orgID, payload.TeamID, payload.SlackUserID, nil, nil, logger); resolveErr != nil {
				logger.Warn().Err(resolveErr).Str("slack_user_id", payload.SlackUserID).Msg("failed to resolve unified Slack user mapping")
			} else if resolved != nil {
				mappedUserID = resolved
			}
		}
		if stores.SlackUserLinks != nil && payload.SlackUserID != "" {
			link, linkErr := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, payload.TeamID, payload.SlackUserID)
			if linkErr == nil && link.UserID != nil {
				if mappedUserID == nil {
					mappedUserID = link.UserID
				}
			} else if linkErr != nil && !errors.Is(linkErr, pgx.ErrNoRows) {
				logger.Warn().Err(linkErr).Str("slack_user_id", payload.SlackUserID).Msg("failed to resolve Slack user mapping")
			} else if mappedUserID == nil && errors.Is(linkErr, pgx.ErrNoRows) && stores.Users != nil {
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
				var llm llmClient
				if services != nil {
					llm = services.LLM
				}
				refreshedSession, routingMode, routeErr := refreshSlackLinkedSessionRouting(ctx, stores, llm, logger, orgID, payload.TeamID, payload.ChannelID, payload.Text, session)
				if routeErr != nil {
					return fmt.Errorf("refresh linked Slack session routing: %w", routeErr)
				}
				session = refreshedSession
				if err := enqueueSlackSessionContinuationMessage(
					ctx,
					stores,
					orgID,
					session,
					mappedUserID,
					renderSlackPromptWithUserResolver(ctx, payload.Text, permalink, threadMessages, contextRefs, contextFiles, userResolver),
					contextRefs,
				); err != nil {
					return err
				}
				ackText := slackSessionAckText(services, session.ID, "Continuing")
				if teamLine := slackTeamSessionLine(existingLink); teamLine != "" {
					ackText = strings.TrimSpace(ackText) + "\n\n" + teamLine
				}
				ackChannelID, ackThreadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, existingLink, threadTS)
				ackBlocks := slackSessionAckBlocks(ctx, stores, services, logger, orgID, installationID, payload.TeamID, payload.ChannelID, &session, ackText, slackbotsvc.SlackSessionContextSummary{}, routingMode)
				posted, postErr := postSlackMessageWithFallback(ctx, slackClient, stores, services, logger, existingLink, slackCfg.AccessToken, ackChannelID, ackThreadTS, ackText, ackBlocks, models.SlackOutboundMessageKindAck)
				if postErr != nil {
					logger.Warn().Err(postErr).Str("session_id", session.ID.String()).Msg("failed to post Slack continuation acknowledgement")
				} else {
					if updateErr := stores.SlackSessionLinks.SetLatestStatusMessageTS(ctx, orgID, session.ID, posted.Timestamp); updateErr != nil {
						logger.Warn().Err(updateErr).Str("session_id", session.ID.String()).Msg("failed to save Slack continuation status timestamp")
					}
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
			settings, settingsErr := stores.SlackChannels.GetEffectiveByChannel(ctx, orgID, payload.TeamID, payload.ChannelID)
			if settingsErr == nil {
				if !stringSliceContains(settings.AllowedActions, string(slackbotsvc.CapabilitySession)) {
					if markErr := stores.SlackInboundEvents.MarkFailed(ctx, orgID, inboundID, "Slack-started sessions are not allowed in this channel"); markErr != nil {
						logger.Warn().Err(markErr).Str("channel_id", payload.ChannelID).Msg("failed to mark disallowed Slack session event failed")
					}
					logger.Warn().Str("channel_id", payload.ChannelID).Msg("Slack-started session blocked by effective Slack channel settings")
					return nil
				}
				if mappedUserID != nil && stores.Memberships != nil {
					membership, membershipErr := stores.Memberships.Get(ctx, *mappedUserID, orgID)
					if membershipErr != nil || !roleCanStartSlackSession(membership.Role) {
						if markErr := stores.SlackInboundEvents.MarkFailed(ctx, orgID, inboundID, "Slack-started sessions require builder access"); markErr != nil {
							logger.Warn().Err(markErr).Str("channel_id", payload.ChannelID).Msg("failed to mark unauthorized Slack session event failed")
						}
						logger.Warn().Err(membershipErr).Str("channel_id", payload.ChannelID).Msg("Slack-started session blocked by mapped user role")
						return nil
					}
				}
				contextSettings := settings
				contextSettings.DefaultRepositoryID = nil
				resolvedContext = slackbotsvc.ResolveSlackContext(slackbotsvc.SlackContextResolveInput{
					Settings:              contextSettings,
					Text:                  payload.Text,
					References:            slackContextReferencesForResolver(ctx, stores, logger, orgID, contextRefs),
					RepositoryDefaults:    slackRepositoryDefaultsForContext(ctx, stores, logger, orgID, installationID, payload.TeamID, payload.ChannelID),
					TriggeringSlackUserID: payload.SlackUserID,
				})
				session.RepositoryID = resolvedContext.RepositoryID
				if strings.TrimSpace(resolvedContext.Branch) != "" {
					branch := strings.TrimSpace(resolvedContext.Branch)
					session.TargetBranch = &branch
				}
			} else if !errors.Is(settingsErr, pgx.ErrNoRows) {
				logger.Warn().Err(settingsErr).Str("channel_id", payload.ChannelID).Msg("failed to load Slack channel settings")
			}
		}
		var llm llmClient
		if services != nil {
			llm = services.LLM
		}
		resolvedContext = resolveSlackAutoRouting(ctx, llm, logger, payload.Text, resolvedContext)
		session.InputManifest = slackRoutingInputManifest(session.InputManifest, resolvedContext.RoutingMode, resolvedContext.RoutingReason)
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
			Content:    renderSlackPromptWithUserResolver(ctx, payload.Text, permalink, threadMessages, contextRefs, contextFiles, userResolver),
			References: slackContextReferencesForSessionInput(contextRefs),
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
		if shouldEnqueueSlackStartedRun(session, resolvedContext) {
			if err := enqueueRunAgentForSession(ctx, stores, *session); err != nil {
				return fmt.Errorf("enqueue slack-started session: %w", err)
			}
		}
		ackText := slackSessionAckText(services, session.ID, "Starting")
		if teamLine := slackTeamSessionLine(*link); teamLine != "" {
			ackText = strings.TrimSpace(ackText) + "\n\n" + teamLine
		}
		ackBlocks := slackSessionAckBlocks(ctx, stores, services, logger, orgID, installationID, payload.TeamID, payload.ChannelID, session, ackText, resolvedContext.ContextSummary, resolvedContext.RoutingMode)
		ackChannelID, ackThreadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, *link, threadTS)
		posted, postErr := postSlackMessageWithFallback(ctx, slackClient, stores, services, logger, *link, slackCfg.AccessToken, ackChannelID, ackThreadTS, ackText, ackBlocks, models.SlackOutboundMessageKindAck)
		if postErr != nil {
			logger.Warn().Err(postErr).Str("session_id", session.ID.String()).Msg("failed to post Slack session acknowledgement")
		} else {
			if updateErr := stores.SlackSessionLinks.SetLatestStatusMessageTS(ctx, orgID, session.ID, posted.Timestamp); updateErr != nil {
				logger.Warn().Err(updateErr).Str("session_id", session.ID.String()).Msg("failed to save Slack acknowledgement status timestamp")
			}
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
	settings, err := stores.SlackChannels.GetEffectiveByChannel(ctx, orgID, teamID, channelID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to load Slack response visibility")
		}
		return "thread"
	}
	return slackNormalizeResponseVisibility(string(settings.ResponseVisibility))
}

func slackNormalizeResponseVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "dm":
		return "dm"
	default:
		return "thread"
	}
}

func slackNormalizeOptionalResponseVisibility(value *models.SlackResponseVisibility) string {
	if value == nil {
		return "thread"
	}
	return slackNormalizeResponseVisibility(string(*value))
}

func slackResponseVisibilityPtr(value models.SlackResponseVisibility) *models.SlackResponseVisibility {
	return &value
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

func slackRepositoryDefaultsForContext(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, installationID uuid.UUID, teamID, channelID string) []slackbotsvc.SlackRepositoryDefault {
	if stores == nil {
		return nil
	}
	defaults := []slackbotsvc.SlackRepositoryDefault{}
	if stores.SlackChannels != nil && channelID != "" {
		channelSettings, err := stores.SlackChannels.GetByChannel(ctx, orgID, teamID, channelID)
		if err == nil && channelSettings.DefaultRepositoryID != nil {
			defaults = appendSlackRepositoryDefault(ctx, stores, logger, defaults, orgID, *channelSettings.DefaultRepositoryID, channelSettings.DefaultBranch, slackbotsvc.SlackRepositoryResolutionSourceChannelDefault)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to load Slack channel repository default")
		}
	}
	if stores.SlackBotSettings != nil && installationID != uuid.Nil {
		botSettings, err := stores.SlackBotSettings.GetByInstallation(ctx, orgID, installationID)
		if err == nil && botSettings.DefaultRepositoryID != nil {
			defaults = appendSlackRepositoryDefault(ctx, stores, logger, defaults, orgID, *botSettings.DefaultRepositoryID, botSettings.DefaultBranch, slackbotsvc.SlackRepositoryResolutionSourceInstallDefault)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Msg("failed to load Slack install repository default")
		}
	}
	if stores.Organizations != nil {
		org, err := stores.Organizations.GetByID(ctx, orgID)
		if err == nil {
			settings, parseErr := models.ParseOrgSettings(org.Settings)
			if parseErr != nil {
				logger.Warn().Err(parseErr).Str("org_id", orgID.String()).Msg("failed to parse org settings for Slack repository default")
			} else if settings.DefaultWorkRepositoryID != nil {
				defaults = appendSlackRepositoryDefault(ctx, stores, logger, defaults, orgID, *settings.DefaultWorkRepositoryID, nil, slackbotsvc.SlackRepositoryResolutionSourceOrgDefault)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to load org settings for Slack repository default")
		}
	}
	if stores.Repositories != nil {
		// Last-resort fallback: when no channel/install/org default is configured,
		// attach the org's first connected repository so a Slack session still has
		// code context instead of dropping to research-only mode. ListByOrg returns
		// active repos ordered by full_name, so repos[0] is deterministic.
		repos, err := stores.Repositories.ListByOrg(ctx, orgID, db.RepositoryFilters{})
		if err == nil && len(repos) > 0 {
			repo := repos[0]
			source := slackbotsvc.SlackRepositoryResolutionSourceFirstRepo
			if len(repos) == 1 {
				source = slackbotsvc.SlackRepositoryResolutionSourceSingleRepo
			}
			defaults = append(defaults, slackbotsvc.SlackRepositoryDefault{
				RepositoryID:   repo.ID,
				RepositoryName: repo.FullName,
				Branch:         repo.DefaultBranch,
				Source:         source,
			})
		} else if err != nil {
			logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to list repositories for Slack repository fallback")
		}
	}
	return defaults
}

func appendSlackRepositoryDefault(ctx context.Context, stores *Stores, logger zerolog.Logger, defaults []slackbotsvc.SlackRepositoryDefault, orgID, repoID uuid.UUID, branch *string, source slackbotsvc.SlackRepositoryResolutionSource) []slackbotsvc.SlackRepositoryDefault {
	candidate := slackbotsvc.SlackRepositoryDefault{
		RepositoryID: repoID,
		Source:       source,
	}
	if branch != nil {
		candidate.Branch = strings.TrimSpace(*branch)
	}
	if stores != nil && stores.Repositories != nil {
		repo, err := stores.Repositories.GetByID(ctx, orgID, repoID)
		if err == nil {
			candidate.RepositoryName = repo.FullName
			if candidate.Branch == "" {
				candidate.Branch = repo.DefaultBranch
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("repository_id", repoID.String()).Msg("failed to load repository default details")
		}
	}
	return append(defaults, candidate)
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
	var botSettings *models.SlackBotSettings
	var defaultRepo *models.Repository
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
	if stores != nil && stores.Memberships != nil {
		var mappedUserID *uuid.UUID
		if stores.ExternalUserLinks != nil {
			if externalUserID, externalErr := lookupExternalSlackMappedUserID(ctx, stores.ExternalUserLinks, orgID, teamID, slackUserID); externalErr == nil && externalUserID != nil {
				mappedUserID = externalUserID
			} else if externalErr != nil {
				logger.Warn().Err(externalErr).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home external org memberships")
			}
		}
		if stores.SlackUserLinks != nil {
			if link, err := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, teamID, slackUserID); err == nil && link.UserID != nil {
				if mappedUserID == nil {
					mappedUserID = link.UserID
				}
			}
		}
		if mappedUserID != nil {
			if userMemberships, membershipErr := stores.Memberships.ListByUser(ctx, *mappedUserID); membershipErr == nil {
				memberships = userMemberships
			} else {
				logger.Warn().Err(membershipErr).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home org memberships")
			}
		}
	}
	if stores != nil && stores.SlackBotSettings != nil {
		if settings, err := stores.SlackBotSettings.GetByOrg(ctx, orgID); err == nil {
			botSettings = &settings
			if settings.DefaultRepositoryID != nil && stores.Repositories != nil {
				if repo, repoErr := stores.Repositories.GetByID(ctx, orgID, *settings.DefaultRepositoryID); repoErr == nil {
					defaultRepo = &repo
				} else if !errors.Is(repoErr, pgx.ErrNoRows) {
					logger.Warn().Err(repoErr).Str("repository_id", settings.DefaultRepositoryID.String()).Msg("failed to load Slack App Home default repository")
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to load Slack App Home defaults")
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
	blocks = append(blocks, slackHomePersonalDefaultsBlock(botSettings, defaultRepo))
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

func slackHomePersonalDefaultsBlock(settings *models.SlackBotSettings, repo *models.Repository) ingestion.SlackBlock {
	if settings == nil {
		return ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Personal defaults*\nUsing the connected organization's Slack defaults."},
		}
	}
	repoLabel := "Not set"
	if repo != nil && strings.TrimSpace(repo.FullName) != "" {
		repoLabel = strings.TrimSpace(repo.FullName)
	} else if settings.DefaultRepositoryID != nil {
		repoLabel = settings.DefaultRepositoryID.String()
	}
	branchLabel := "Not set"
	if settings.DefaultBranch != nil && strings.TrimSpace(*settings.DefaultBranch) != "" {
		branchLabel = "`" + strings.TrimSpace(*settings.DefaultBranch) + "`"
	}
	text := fmt.Sprintf(
		"*Personal defaults*\nRepository: %s\nBranch: %s\nRouting: %s\nReplies: %s\nNotifications: %s",
		repoLabel,
		branchLabel,
		slackRoutingModeLabel(models.SlackRoutingMode(settings.RoutingMode)),
		slackResponseVisibilityLabel(settings.ResponseVisibility),
		slackNotificationPresetLabel(settings.NotificationPreset),
	)
	return ingestion.SlackBlock{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
	}
}

func slackRoutingModeLabel(mode models.SlackRoutingMode) string {
	switch mode {
	case models.SlackRoutingModeAnswerOnly:
		return "Answer only"
	case models.SlackRoutingModeStartWork:
		return "Start work"
	default:
		return "Auto"
	}
}

func slackResponseVisibilityLabel(visibility models.SlackResponseVisibility) string {
	switch visibility {
	case models.SlackResponseVisibilityDM:
		return "DM"
	default:
		return "Thread"
	}
}

func slackNotificationPresetLabel(preset models.SlackNotificationPreset) string {
	switch preset {
	case models.SlackNotificationPresetQuiet:
		return "Quiet"
	case models.SlackNotificationPresetVerbose:
		return "Verbose"
	case models.SlackNotificationPresetCustom:
		return "Custom"
	default:
		return "Balanced"
	}
}

func slackHomeOrgSelectorBlock(memberships []models.MembershipSummary, activeOrgID uuid.UUID) ingestion.SlackBlock {
	if len(memberships) <= 1 {
		return ingestion.SlackBlock{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "*Organization*\nUsing the connected 143 organization."},
		}
	}
	options := make([]map[string]any, 0, len(memberships))
	var initial map[string]any
	for _, membership := range memberships {
		label := strings.TrimSpace(membership.OrgName)
		if label == "" {
			label = membership.OrgID.String()
		}
		option := map[string]any{
			"text": map[string]string{
				"type": "plain_text",
				"text": truncateSlackButtonText(label + " (" + string(membership.Role) + ")"),
			},
			"value": membership.OrgID.String(),
		}
		options = append(options, option)
		if membership.OrgID == activeOrgID {
			initial = option
		}
	}
	element := map[string]any{
		"type":        "static_select",
		"action_id":   "slack_select_org",
		"placeholder": map[string]string{"type": "plain_text", "text": "Select organization"},
		"options":     options,
	}
	if initial != nil {
		element["initial_option"] = initial
	}
	return ingestion.SlackBlock{
		Type:     "actions",
		Elements: []map[string]any{element},
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

type slackMessagePoster interface {
	PostMessage(ctx context.Context, accessToken, channelID, threadTS, text string) (ingestion.SlackPostedMessage, error)
	PostMessageWithBlocks(ctx context.Context, accessToken, channelID, threadTS, text string, blocks []ingestion.SlackBlock) (ingestion.SlackPostedMessage, error)
}

type slackMessageUpdater interface {
	UpdateMessage(ctx context.Context, accessToken, channelID, messageTS, text string) error
	UpdateMessageWithBlocks(ctx context.Context, accessToken, channelID, messageTS, text string, blocks []ingestion.SlackBlock) error
}

type slackReactionAdder interface {
	AddReaction(ctx context.Context, accessToken, channelID, messageTS, name string) error
}

const slackCompletionReactionName = models.SlackReactionCompletedResponse

func addSlackCompletionReaction(ctx context.Context, adder slackReactionAdder, logger zerolog.Logger, accessToken string, link models.SlackSessionLink) {
	addSlackSessionReaction(ctx, adder, logger, accessToken, link, slackCompletionReactionName)
}

func addSlackSessionReaction(ctx context.Context, adder slackReactionAdder, logger zerolog.Logger, accessToken string, link models.SlackSessionLink, reactionName string) {
	if adder == nil {
		return
	}
	channelID := strings.TrimSpace(link.SlackChannelID)
	messageTS := strings.TrimSpace(link.SlackRootTS)
	if messageTS == "" {
		messageTS = strings.TrimSpace(link.SlackThreadTS)
	}
	reactionName = strings.TrimSpace(reactionName)
	if strings.TrimSpace(accessToken) == "" || channelID == "" || messageTS == "" || reactionName == "" {
		return
	}
	if err := adder.AddReaction(ctx, accessToken, channelID, messageTS, reactionName); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already_reacted") {
			return
		}
		logger.Warn().Err(err).
			Str("slack_channel_id", channelID).
			Str("slack_message_ts", messageTS).
			Str("slack_reaction_name", reactionName).
			Msg("failed to add Slack session reaction")
	}
}

func postSlackMessageWithFallback(ctx context.Context, poster slackMessagePoster, stores *Stores, services *Services, logger zerolog.Logger, link models.SlackSessionLink, accessToken, channelID, threadTS, text string, blocks []ingestion.SlackBlock, kind models.SlackOutboundMessageKind) (ingestion.SlackPostedMessage, error) {
	if poster == nil {
		return ingestion.SlackPostedMessage{}, fmt.Errorf("slack message poster is not configured")
	}
	if len(blocks) == 0 {
		posted, err := poster.PostMessage(ctx, accessToken, channelID, threadTS, text)
		if err != nil {
			recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed"), kind, "failed", text)
			return ingestion.SlackPostedMessage{}, err
		}
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, posted.Timestamp, kind, "sent", text)
		return posted, nil
	}
	posted, err := poster.PostMessageWithBlocks(ctx, accessToken, channelID, threadTS, text, blocks)
	if err == nil {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, posted.Timestamp, kind, "sent", text)
		return posted, nil
	}
	if !slackIsInvalidBlocksError(err) {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed"), kind, "failed", text)
		return ingestion.SlackPostedMessage{}, err
	}
	recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_invalid_blocks"), kind, "failed_invalid_blocks", text)
	fallbackPosted, fallbackErr := poster.PostMessage(ctx, accessToken, channelID, threadTS, text)
	if fallbackErr != nil {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_fallback"), kind, "failed_fallback", text)
		return ingestion.SlackPostedMessage{}, fallbackErr
	}
	recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, fallbackPosted.Timestamp, kind, "sent_fallback", text)
	return fallbackPosted, nil
}

func updateSlackMessageWithPostFallback(ctx context.Context, poster slackMessagePoster, updater slackMessageUpdater, stores *Stores, services *Services, logger zerolog.Logger, link models.SlackSessionLink, accessToken, channelID, threadTS, messageTS, text string, blocks []ingestion.SlackBlock, kind models.SlackOutboundMessageKind) (ingestion.SlackPostedMessage, error) {
	if updater == nil || strings.TrimSpace(messageTS) == "" {
		return postSlackMessageWithFallback(ctx, poster, stores, services, logger, link, accessToken, channelID, threadTS, text, blocks, kind)
	}
	if len(blocks) == 0 {
		if err := updater.UpdateMessage(ctx, accessToken, channelID, messageTS, text); err != nil {
			recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_update"), kind, "failed_update", text)
			logger.Warn().Err(err).Str("session_id", link.SessionID.String()).Msg("failed to update Slack message; posting a new message")
			return postSlackMessageWithFallback(ctx, poster, stores, services, logger, link, accessToken, channelID, threadTS, text, blocks, kind)
		}
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, messageTS, kind, "sent", text)
		return ingestion.SlackPostedMessage{Channel: channelID, Timestamp: messageTS}, nil
	}
	if err := updater.UpdateMessageWithBlocks(ctx, accessToken, channelID, messageTS, text, blocks); err == nil {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, messageTS, kind, "sent", text)
		return ingestion.SlackPostedMessage{Channel: channelID, Timestamp: messageTS}, nil
	} else if !slackIsInvalidBlocksError(err) {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_update"), kind, "failed_update", text)
		logger.Warn().Err(err).Str("session_id", link.SessionID.String()).Msg("failed to update Slack message; posting a new message")
		return postSlackMessageWithFallback(ctx, poster, stores, services, logger, link, accessToken, channelID, threadTS, text, blocks, kind)
	}
	recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_update_invalid_blocks"), kind, "failed_update_invalid_blocks", text)
	if err := updater.UpdateMessage(ctx, accessToken, channelID, messageTS, text); err != nil {
		recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, slackSyntheticMessageTS(kind, "failed_update_fallback"), kind, "failed_update_fallback", text)
		logger.Warn().Err(err).Str("session_id", link.SessionID.String()).Msg("failed to update Slack message without blocks; posting a new message")
		return postSlackMessageWithFallback(ctx, poster, stores, services, logger, link, accessToken, channelID, threadTS, text, nil, kind)
	}
	recordSlackOutboundInChannel(ctx, stores, services, logger, link, channelID, messageTS, kind, "sent_update_fallback", text)
	return ingestion.SlackPostedMessage{Channel: channelID, Timestamp: messageTS}, nil
}

func slackIsInvalidBlocksError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_blocks")
}

func slackSyntheticMessageTS(kind models.SlackOutboundMessageKind, status string) string {
	return "attempt:" + string(kind) + ":" + status + ":" + uuid.NewString()
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
		progress := slackbotsvc.NormalizeProgressUpdate(slackbotsvc.ProgressInput{
			UpdateKind: input.UpdateKind,
			Title:      input.Title,
			Summary:    input.Summary,
			Terminal:   input.Terminal,
			Failed:     strings.Contains(strings.ToLower(input.UpdateKind+" "+input.Title), "fail"),
			OccurredAt: time.Now(),
		})
		previous := slackbotsvc.SlackProgressPrevious{}
		if link.LatestStatusMessageTS != nil && *link.LatestStatusMessageTS != "" {
			previous.UpdatedAt = link.UpdatedAt
		}
		if link.LatestProgressKind != nil {
			previous.Kind = slackbotsvc.SlackProgressKind(*link.LatestProgressKind)
		}
		if !slackbotsvc.ShouldSendProgressUpdate(progress, previous, slackbotsvc.DefaultSlackProgressPolicy()) {
			if services != nil && services.SlackbotMetrics != nil {
				services.SlackbotMetrics.RecordDroppedUpdate(ctx, string(progress.Kind), "progress_policy")
			}
			return nil
		}
		state := slackLifecycleStateForProgress(progress)
		rendered := slackbotsvc.RenderSessionStatus(slackbotsvc.SlackSessionRenderInput{
			Session:    models.Session{ID: sessionID},
			Link:       link,
			State:      state,
			Title:      progress.Title,
			Summary:    progress.Summary,
			SessionURL: slackSessionURL(services, sessionID),
		})
		text := rendered.Text
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, link, slackReplyThreadTS(link.SlackThreadTS))
		if link.LatestStatusMessageTS != nil && *link.LatestStatusMessageTS != "" {
			updateStarted := time.Now()
			if err := slackClient.UpdateMessageWithBlocks(ctx, slackCfg.AccessToken, channelID, *link.LatestStatusMessageTS, text, rendered.Blocks); err == nil {
				recordSlackMessageUpdateLatency(ctx, services, "chat.update", "sent", time.Since(updateStarted))
				if updateErr := stores.SlackSessionLinks.SetLatestStatusProgress(ctx, orgID, sessionID, *link.LatestStatusMessageTS, string(progress.Kind)); updateErr != nil {
					logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).Msg("failed to save Slack progress kind")
				}
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
			if updateErr := stores.SlackSessionLinks.SetLatestStatusProgress(ctx, orgID, sessionID, posted.Timestamp, string(progress.Kind)); updateErr != nil {
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
		text, blocks := renderSlackFinalBlocks(services, msg.Content, orgID, sessionID, link, details)
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, link, slackReplyThreadTS(link.SlackThreadTS))
		messageTS := ""
		if link.LatestStatusMessageTS != nil && *link.LatestStatusMessageTS != "" {
			messageTS = *link.LatestStatusMessageTS
		}
		updateStarted := time.Now()
		posted, err := updateSlackMessageWithPostFallback(ctx, slackClient, slackClient, stores, services, logger, link, slackCfg.AccessToken, channelID, threadTS, messageTS, text, blocks, models.SlackOutboundMessageKindFinal)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
		if messageTS != "" && posted.Timestamp == messageTS {
			recordSlackMessageUpdateLatency(ctx, services, "chat.update", "sent", time.Since(updateStarted))
		}
		if stores.SlackSessionLinks != nil {
			if updateErr := stores.SlackSessionLinks.SetFinalMessageTS(ctx, orgID, sessionID, posted.Timestamp); updateErr != nil {
				logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).Msg("failed to save Slack final message timestamp")
			}
		}
		addSlackCompletionReaction(ctx, slackClient, logger, slackCfg.AccessToken, link)
		return nil
	}
}

func newSlackAddSessionReactionHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	slackClient := ingestion.NewSlackAPIClient(logger)
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		if stores == nil || stores.Credentials == nil || stores.SlackSessionLinks == nil {
			return fmt.Errorf("slack reaction dependencies are not configured")
		}
		var input models.SlackAddSessionReactionJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal slack reaction payload: %w", err)
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
		addSlackSessionReaction(ctx, slackClient, logger, slackCfg.AccessToken, link, input.ReactionName)
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
		channelID, threadTS, shouldPost := slackHumanInputDeliveryTarget(ctx, stores, slackClient, slackCfg.AccessToken, logger, req, link, slackReplyThreadTS(link.SlackThreadTS))
		if !shouldPost {
			logger.Info().
				Str("org_id", orgID.String()).
				Str("session_id", sessionID.String()).
				Str("human_input_request_id", requestID.String()).
				Str("sensitivity", string(req.Sensitivity)).
				Str("preferred_channel", string(req.PreferredChannel)).
				Msg("skipping Slack human-input delivery because request is not channel-deliverable")
			return nil
		}
		_, err = postSlackMessageWithFallback(ctx, slackClient, stores, services, logger, link, slackCfg.AccessToken, channelID, threadTS, text, blocks, models.SlackOutboundMessageKindHumanInput)
		if err != nil {
			recordSlackAPIFailure(ctx, services, "chat.postMessage")
			return err
		}
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
	renderInput := slackNotificationRenderInput(input)
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = slackNotificationDefaultTitle(renderInput.Kind)
	}
	body := strings.TrimSpace(input.Body)
	if body == "" {
		body = slackNotificationDefaultBody(renderInput.Kind)
	}
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
	if input.PullRequestID != "" {
		prURL := strings.TrimSpace(input.PullRequestURL)
		if prURL == "" && input.SessionID != "" {
			if sessionID, err := uuid.Parse(input.SessionID); err == nil {
				prURL = slackSessionURL(services, sessionID)
			}
		}
		if prURL != "" {
			elements = append(elements, map[string]any{
				"type": "button",
				"text": map[string]string{"type": "plain_text", "text": "Review PR"},
				"url":  prURL,
			})
		}
	}
	if len(elements) > 0 {
		blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	}
	return text.String(), blocks
}

func slackNotificationRenderInput(input models.SlackSendNotificationJobPayload) models.SlackNotificationRenderInput {
	return models.SlackNotificationRenderInput{
		Kind:            models.SlackNotificationKind(input.Kind),
		Preset:          models.SlackNotificationPreset(input.NotificationPreset),
		Title:           input.Title,
		Body:            input.Body,
		SessionID:       parseOptionalUUID(input.SessionID),
		AutomationID:    parseOptionalUUID(input.AutomationID),
		AutomationRunID: parseOptionalUUID(input.AutomationRunID),
		PullRequestID:   parseOptionalUUID(input.PullRequestID),
		PreviewID:       parseOptionalUUID(input.PreviewID),
		ActorUserID:     parseOptionalUUID(input.ActorUserID),
	}
}

func parseOptionalUUID(raw string) *uuid.UUID {
	if raw == "" {
		return nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func slackNotificationDefaultTitle(kind models.SlackNotificationKind) string {
	switch kind {
	case models.SlackNotificationSessionCompleted:
		return "Session completed"
	case models.SlackNotificationSessionFailed:
		return "Session failed"
	case models.SlackNotificationAutomationCompleted:
		return "Automation completed"
	case models.SlackNotificationAutomationFailed:
		return "Automation failed"
	case models.SlackNotificationAutomationFailureStreak:
		return "Automation failure streak"
	case models.SlackNotificationPROpened:
		return "Pull request opened"
	case models.SlackNotificationPreviewReady:
		return "Preview ready"
	case models.SlackNotificationPreviewFailed:
		return "Preview failed"
	case models.SlackNotificationPreviewStale:
		return "Preview stale"
	case models.SlackNotificationHumanInputRequested:
		return "143 needs your response"
	case models.SlackNotificationPRAutoRepairAttention:
		return "Automatic PR repair needs attention"
	case models.SlackNotificationPRReadinessAttention:
		return "PR readiness needs attention"
	default:
		return "143 notification"
	}
}

func slackNotificationDefaultBody(kind models.SlackNotificationKind) string {
	switch kind {
	case models.SlackNotificationSessionCompleted:
		return "The requested work finished. Open the session to review the result."
	case models.SlackNotificationSessionFailed:
		return "The run failed before completing. Open the session for details and recovery options."
	case models.SlackNotificationAutomationCompleted:
		return "An automation run completed."
	case models.SlackNotificationAutomationFailed:
		return "An automation run failed and may need attention."
	case models.SlackNotificationAutomationFailureStreak:
		return "This automation has failed repeatedly."
	case models.SlackNotificationPROpened:
		return "A pull request is ready for review."
	case models.SlackNotificationPreviewReady:
		return "A preview is ready to open."
	case models.SlackNotificationPreviewFailed:
		return "Preview startup failed. Open the session or preview details to inspect logs."
	case models.SlackNotificationPreviewStale:
		return "The session has newer workspace changes than the active preview."
	case models.SlackNotificationHumanInputRequested:
		return "The agent is waiting for a human response."
	case models.SlackNotificationPRAutoRepairAttention:
		return "143 could not complete automatic PR repair. Open the session or pull request to decide the next step."
	case models.SlackNotificationPRReadinessAttention:
		return "Automatic readiness checks found blockers. Open the session or pull request to decide the next step."
	default:
		return ""
	}
}

type slackNotificationSubscriptionConfig struct {
	Events       []string `json:"events"`
	Automations  []string `json:"automations"`
	SlackUserIDs []string `json:"slack_user_ids"`
	DMUserIDs    []string `json:"dm_user_ids"`
}

func slackNotificationSubscriptionMatches(raw json.RawMessage, preset *models.SlackNotificationPreset, eventKind string, automationID *uuid.UUID) bool {
	switch eventKind {
	case string(models.SlackNotificationPreviewReady), string(models.SlackNotificationPreviewFailed):
		return false
	}
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	cfg := parseSlackNotificationSubscriptionConfig(raw)
	if len(cfg.Events) == 0 {
		cfg.Events = slackNotificationPresetEvents(preset)
	}
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

func slackNotificationPresetEvents(preset *models.SlackNotificationPreset) []string {
	if preset == nil {
		return nil
	}
	switch *preset {
	case models.SlackNotificationPresetQuiet:
		return []string{
			string(models.SlackNotificationSessionFailed),
			string(models.SlackNotificationAutomationFailed),
			string(models.SlackNotificationAutomationFailureStreak),
			string(models.SlackNotificationHumanInputRequested),
			string(models.SlackNotificationPRAutoRepairAttention),
			string(models.SlackNotificationPRReadinessAttention),
		}
	case models.SlackNotificationPresetBalanced:
		return []string{
			string(models.SlackNotificationSessionCompleted),
			string(models.SlackNotificationSessionFailed),
			string(models.SlackNotificationAutomationFailed),
			string(models.SlackNotificationAutomationFailureStreak),
			string(models.SlackNotificationPROpened),
			string(models.SlackNotificationHumanInputRequested),
			string(models.SlackNotificationPRAutoRepairAttention),
			string(models.SlackNotificationPRReadinessAttention),
		}
	case models.SlackNotificationPresetVerbose:
		return []string{"*"}
	default:
		return nil
	}
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
	EventKind       string
	Title           string
	Body            string
	SessionID       *uuid.UUID
	PreviewID       *uuid.UUID
	PullRequestID   *uuid.UUID
	PullRequestURL  string
	AutomationID    *uuid.UUID
	AutomationRunID *uuid.UUID
}

type slackNotificationDestination struct {
	TeamID      string
	ChannelID   string
	SlackUserID string
	ThreadTS    string
}

func slackNotificationDestinations(setting models.SlackChannelSettings, input slackNotificationFanoutInput) []slackNotificationDestination {
	subs := parseSlackNotificationSubscriptionConfig(setting.NotificationSubscriptions)
	destinations := []slackNotificationDestination{}
	if slackNormalizeOptionalResponseVisibility(setting.ResponseVisibility) != "dm" {
		destinations = append(destinations, slackNotificationDestination{
			TeamID:    setting.SlackTeamID,
			ChannelID: setting.SlackChannelID,
		})
	}
	for _, slackUserID := range append(subs.SlackUserIDs, subs.DMUserIDs...) {
		slackUserID = strings.TrimSpace(slackUserID)
		if slackUserID == "" {
			continue
		}
		destinations = append(destinations, slackNotificationDestination{
			TeamID:      setting.SlackTeamID,
			SlackUserID: slackUserID,
		})
	}
	return destinations
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
		if !slackNotificationSubscriptionMatches(setting.NotificationSubscriptions, setting.NotificationPreset, input.EventKind, input.AutomationID) {
			continue
		}
		dedupeKeyParts := []string{"slack_notification", input.EventKind, setting.SlackChannelID}
		if input.SessionID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.SessionID.String())
		}
		if input.PreviewID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.PreviewID.String())
		}
		if input.PullRequestID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.PullRequestID.String())
		}
		if input.AutomationID != nil {
			dedupeKeyParts = append(dedupeKeyParts, input.AutomationID.String())
		}
		dedupeKey := strings.Join(dedupeKeyParts, ":")
		for _, destination := range slackNotificationDestinations(setting, input) {
			payload := models.SlackSendNotificationJobPayload{
				OrgID:       orgID.String(),
				Kind:        input.EventKind,
				TeamID:      destination.TeamID,
				ChannelID:   destination.ChannelID,
				SlackUserID: destination.SlackUserID,
				ThreadTS:    destination.ThreadTS,
				Title:       input.Title,
				Body:        input.Body,
			}
			if input.SessionID != nil {
				payload.SessionID = input.SessionID.String()
			}
			if input.PreviewID != nil {
				payload.PreviewID = input.PreviewID.String()
			}
			if input.PullRequestID != nil {
				payload.PullRequestID = input.PullRequestID.String()
				payload.PullRequestURL = strings.TrimSpace(input.PullRequestURL)
			}
			if input.AutomationID != nil {
				payload.AutomationID = input.AutomationID.String()
			}
			if input.AutomationRunID != nil {
				payload.AutomationRunID = input.AutomationRunID.String()
			}
			if setting.NotificationPreset != nil {
				payload.NotificationPreset = string(*setting.NotificationPreset)
			}
			destinationDedupeKey := dedupeKey
			if destination.SlackUserID != "" {
				destinationDedupeKey += ":dm:" + destination.SlackUserID
			}
			if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "slack_send_notification", payload, 4, &destinationDedupeKey); err != nil {
				logger.Warn().Err(err).Str("event_kind", input.EventKind).Str("slack_channel_id", destination.ChannelID).Str("slack_user_id", destination.SlackUserID).Msg("failed to enqueue Slack notification")
			}
		}
	}
}

func notifyPRAutoRepairAttentionIfAutomatic(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, rawPullRequestID, action, reason string, autoAttempt bool) {
	if !autoAttempt || rawPullRequestID == "" {
		return
	}
	pullRequestID, err := uuid.Parse(rawPullRequestID)
	if err != nil {
		logger.Warn().Err(err).Str("pull_request_id", rawPullRequestID).Msg("invalid pull request ID for automatic PR repair notification")
		return
	}
	notifyPRAutoRepairAttention(ctx, stores, logger, orgID, sessionID, &pullRequestID, action, reason)
}

func notifyPRAutoRepairAttention(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, pullRequestID *uuid.UUID, action, reason string) {
	body := "143 could not complete automatic PR repair. Open the session or pull request to decide the next step."
	reason = strings.TrimSpace(reason)
	if reason != "" {
		body = "143 could not complete automatic PR repair: " + reason
	}
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind:     string(models.SlackNotificationPRAutoRepairAttention),
		Title:         "Automatic PR repair needs attention",
		Body:          body,
		SessionID:     &sessionID,
		PullRequestID: pullRequestID,
	})
}

func notifyPRAutoReadinessAttention(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, status models.PRReadinessRunStatus, summary string) {
	var pullRequestID *uuid.UUID
	if stores != nil && stores.PullRequests != nil {
		pr, err := stores.PullRequests.GetBySessionID(ctx, orgID, sessionID)
		if err == nil {
			id := pr.ID
			pullRequestID = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load pull request for automatic PR readiness notification")
		}
	}
	body := "Automatic readiness checks need attention."
	if strings.TrimSpace(summary) != "" {
		body = summary
	} else if status == models.PRReadinessRunStatusFailed {
		body = "Automatic readiness checks failed before producing a result."
	}
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind:     string(models.SlackNotificationPRReadinessAttention),
		Title:         "PR readiness needs attention",
		Body:          body,
		SessionID:     &sessionID,
		PullRequestID: pullRequestID,
	})
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
	automationKind := string(models.SlackNotificationAutomationCompleted)
	if eventKind == string(models.SlackNotificationSessionFailed) {
		automationKind = string(models.SlackNotificationAutomationFailed)
	}
	enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
		EventKind:       automationKind,
		Title:           title,
		Body:            body,
		SessionID:       &sessionID,
		AutomationID:    &run.AutomationID,
		AutomationRunID: automationRunID,
	})
	if automationKind == string(models.SlackNotificationAutomationFailed) {
		streak, streakErr := stores.AutomationRuns.CountConsecutiveFailures(ctx, orgID, run.AutomationID)
		if streakErr != nil {
			logger.Warn().Err(streakErr).Str("automation_id", run.AutomationID.String()).Msg("failed to count automation failure streak for Slack notification")
			return
		}
		if streak >= 3 {
			enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
				EventKind:       string(models.SlackNotificationAutomationFailureStreak),
				Title:           "Automation failure streak",
				Body:            fmt.Sprintf("%d consecutive automation runs failed.", streak),
				SessionID:       &sessionID,
				AutomationID:    &run.AutomationID,
				AutomationRunID: automationRunID,
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
		EventKind: string(models.SlackNotificationPreviewStale),
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
	summary := strings.TrimSpace(req.Body)
	if len(req.Choices) > 0 {
		var choices strings.Builder
		choices.WriteString(summary)
		choices.WriteString("\n\nChoices:")
		for _, choice := range req.Choices {
			choices.WriteString("\n- ")
			choices.WriteString(choice.ID)
			choices.WriteString(": ")
			choices.WriteString(choice.Label)
		}
		summary = strings.TrimSpace(choices.String())
	}
	rendered := slackbotsvc.RenderSessionStatus(slackbotsvc.SlackSessionRenderInput{
		Session:    models.Session{ID: sessionID},
		State:      slackbotsvc.SessionLifecycleWaiting,
		Title:      strings.TrimSpace(req.Title),
		Summary:    summary + "\n\nAnswer in 143 or use a Slack action.",
		SessionURL: slackSessionURL(services, sessionID),
	})
	text := rendered.Text
	blocks := rendered.Blocks
	if len(req.Choices) > 0 {
		elements := make([]map[string]any, 0, min(len(req.Choices), 5))
		if req.Kind == models.HumanInputRequestKindMultiChoice {
			value, err := json.Marshal(map[string]string{
				"session_id": sessionID.String(),
				"request_id": req.ID.String(),
			})
			if err == nil {
				elements = append(elements, map[string]any{
					"type":      "button",
					"action_id": "slack_answer_human_input_multi",
					"text":      map[string]string{"type": "plain_text", "text": "Choose options"},
					"value":     string(value),
				})
			}
		} else {
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
				actionID := "slack_answer_human_input"
				if req.Kind == models.HumanInputRequestKindToolApproval || req.Kind == models.HumanInputRequestKindActionChoice {
					switch strings.ToLower(choice.ID) {
					case "continue":
						actionID = "slack_continue_human_input"
					case "resume":
						actionID = "slack_resume_human_input"
					case "stop":
						actionID = "slack_stop_human_input"
					case "approve", "approved", "yes", "allow":
						actionID = "slack_approve_human_input"
					case "deny", "denied", "no", "reject", "cancel":
						actionID = "slack_deny_human_input"
					}
				}
				element := map[string]any{
					"type":      "button",
					"action_id": actionID,
					"text": map[string]string{
						"type": "plain_text",
						"text": truncateSlackButtonText(label),
					},
					"value": string(value),
				}
				if choice.Destructive || actionID == "slack_deny_human_input" || actionID == "slack_stop_human_input" {
					element["style"] = "danger"
				} else if actionID == "slack_approve_human_input" || actionID == "slack_continue_human_input" || actionID == "slack_resume_human_input" {
					element["style"] = "primary"
				}
				elements = append(elements, element)
			}
		}
		if len(elements) > 0 {
			blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
		}
	}
	elements := []map[string]any{}
	if req.Kind == "" || req.Kind.AcceptsFreeText() {
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": "slack_answer_human_input_freeform",
			"text":      map[string]string{"type": "plain_text", "text": "Reply in Slack"},
			"value": slackActionValue(map[string]string{
				"session_id": sessionID.String(),
				"request_id": req.ID.String(),
			}),
		})
	}
	elements = append(elements, map[string]any{
		"type": "button",
		"text": map[string]string{"type": "plain_text", "text": "Answer in 143"},
		"url":  slackSessionURL(services, sessionID),
	})
	blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	return text, blocks
}

func slackHumanInputDeliveryTarget(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, accessToken string, logger zerolog.Logger, req models.HumanInputRequest, link models.SlackSessionLink, replyThreadTS string) (string, string, bool) {
	if req.PreferredChannel == "" {
		req.PreferredChannel = models.HumanInputPreferredChannelSlackThread
	}
	if req.Sensitivity == "" {
		req.Sensitivity = models.HumanInputSensitivityTeam
	}
	if req.PreferredChannel == models.HumanInputPreferredChannelWeb {
		return "", "", false
	}
	needsDM := req.PreferredChannel == models.HumanInputPreferredChannelSlackDM ||
		req.Sensitivity == models.HumanInputSensitivityPersonal ||
		req.Sensitivity == models.HumanInputSensitivitySensitive
	slackUserID := link.SlackUserID
	if req.AssignedUserID != nil {
		needsDM = true
		slackUserID = ""
		if stores != nil && stores.SlackUserLinks != nil {
			assignedLink, err := stores.SlackUserLinks.GetByUser(ctx, req.OrgID, *req.AssignedUserID, link.SlackTeamID)
			if err == nil {
				slackUserID = assignedLink.SlackUserID
			} else if !errors.Is(err, pgx.ErrNoRows) {
				logger.Warn().Err(err).
					Str("org_id", req.OrgID.String()).
					Str("assigned_user_id", req.AssignedUserID.String()).
					Msg("failed to resolve assigned Slack user for human-input delivery")
			}
		}
	}
	if !needsDM {
		channelID, threadTS := slackDeliveryTarget(ctx, stores, slackClient, accessToken, logger, link, replyThreadTS)
		return channelID, threadTS, true
	}
	if slackUserID == "" {
		return "", "", false
	}
	dmChannelID, err := slackClient.OpenDM(ctx, accessToken, slackUserID)
	if err != nil {
		logger.Warn().Err(err).Str("slack_user_id", slackUserID).Msg("failed to open Slack DM for human-input delivery")
		return "", "", false
	}
	return slackHumanInputDeliveryTargetFromRequest(req, link, replyThreadTS, dmChannelID)
}

func slackHumanInputDeliveryTargetFromRequest(req models.HumanInputRequest, link models.SlackSessionLink, replyThreadTS, dmChannelID string) (string, string, bool) {
	if req.PreferredChannel == models.HumanInputPreferredChannelWeb {
		return "", "", false
	}
	needsDM := req.PreferredChannel == models.HumanInputPreferredChannelSlackDM ||
		req.Sensitivity == models.HumanInputSensitivityPersonal ||
		req.Sensitivity == models.HumanInputSensitivitySensitive
	if needsDM {
		if dmChannelID == "" {
			return "", "", false
		}
		return dmChannelID, "", true
	}
	return link.SlackChannelID, replyThreadTS, true
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
		if slackHumanInputAllowsNotificationFanout(req) {
			enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
				EventKind: string(models.SlackNotificationHumanInputRequested),
				Title:     "143 needs your response",
				Body:      strings.TrimSpace(req.Title),
				SessionID: &sessionID,
			})
		}
	}
}

func slackHumanInputAllowsNotificationFanout(req models.HumanInputRequest) bool {
	if req.AssignedUserID != nil {
		return false
	}
	if req.Sensitivity == "" {
		req.Sensitivity = models.HumanInputSensitivityTeam
	}
	if req.PreferredChannel == "" {
		req.PreferredChannel = models.HumanInputPreferredChannelSlackThread
	}
	if req.Sensitivity != models.HumanInputSensitivityTeam {
		return false
	}
	return req.PreferredChannel == models.HumanInputPreferredChannelSlackThread
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
			if input.CallbackID == "slack_human_input_multi_modal" {
				return handleSlackHumanInputMultiModal(ctx, stores, services, slackClient, input)
			}
			if input.CallbackID == "slack_configure_channel_modal" {
				return handleSlackConfigureChannelModal(ctx, stores, input)
			}
			if input.CallbackID == "slack_missing_context_modal" {
				return handleSlackMissingContextModal(ctx, stores, services, slackClient, input)
			}
			return nil
		case "slack_open_session":
			return nil
		case "slack_start_from_home":
			return handleSlackStartFromHome(ctx, stores, slackClient, input)
		case "slack_link_account":
			return handleSlackLinkAccount(ctx, stores, services, slackClient, input)
		case "slack_select_org":
			return handleSlackSelectOrg(ctx, stores, services, slackClient, input)
		case "slack_select_repository":
			return handleSlackSelectRepository(ctx, stores, input)
		case "slack_configure_channel":
			return handleSlackConfigureChannel(ctx, stores, slackClient, input)
		case "slack_choose_preview_target", "slack_choose_pull_request", "slack_choose_branch":
			return handleSlackMissingContextPrompt(ctx, stores, slackClient, input)
		case "slack_create_preview":
			return handleSlackCreatePreview(ctx, stores, services, slackClient, input)
		case "slack_open_preview":
			return handleSlackOpenPreview(ctx, stores, services, slackClient, input)
		case "slack_answer_human_input", "slack_approve_human_input", "slack_deny_human_input", "slack_continue_human_input", "slack_resume_human_input", "slack_stop_human_input":
			return handleSlackHumanInputAnswer(ctx, stores, services, slackClient, input)
		case "slack_answer_human_input_freeform":
			return handleSlackHumanInputFreeformPrompt(ctx, stores, slackClient, input)
		case "slack_answer_human_input_multi":
			return handleSlackHumanInputMultiPrompt(ctx, stores, slackClient, input)
		case "slack_member_joined_channel":
			return handleSlackMemberJoinedChannel(ctx, stores, slackClient, input)
		case "slack_refresh_preview", "slack_restart_preview", "slack_stop_preview", "slack_extend_preview":
			return handleSlackPreviewAction(ctx, stores, services, input)
		case "slack_repair_pr", "slack_merge_pr", "slack_claim_team_session", "slack_start_work", "slack_create_pr":
			return handleSlackSpecializedSessionAction(ctx, stores, services, slackClient, input)
		default:
			logger.Debug().Str("action_id", input.ActionID).Msg("unhandled Slack interaction action")
			return nil
		}
	}
}

func handleSlackSelectOrg(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil || stores.Memberships == nil || stores.SlackInstallations == nil || stores.SlackOrgSelections == nil {
		return fmt.Errorf("slack org selection dependencies are not configured")
	}
	selectedOrgID, err := uuid.Parse(input.Value)
	if err != nil {
		return fmt.Errorf("parse selected org_id: %w", err)
	}
	currentOrgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	var mappedUserID *uuid.UUID
	if stores.ExternalUserLinks != nil {
		mappedUserID, err = lookupExternalSlackMappedUserID(ctx, stores.ExternalUserLinks, currentOrgID, input.TeamID, input.UserID)
		if err != nil {
			return fmt.Errorf("resolve external slack user link for org selection: %w", err)
		}
	}
	if mappedUserID == nil && stores.SlackUserLinks != nil {
		link, err := stores.SlackUserLinks.GetBySlackUser(ctx, currentOrgID, input.TeamID, input.UserID)
		if err != nil {
			return fmt.Errorf("resolve slack user link for org selection: %w", err)
		}
		mappedUserID = link.UserID
	}
	if mappedUserID == nil {
		return fmt.Errorf("slack org selection requires a linked 143 user")
	}
	if _, err := stores.Memberships.Get(ctx, *mappedUserID, selectedOrgID); err != nil {
		return fmt.Errorf("selected Slack org is not available to mapped user: %w", err)
	}
	currentInstall, err := stores.SlackInstallations.GetActiveByOrg(ctx, currentOrgID)
	if err != nil {
		return fmt.Errorf("get current Slack installation for org selection: %w", err)
	}
	selectedInstall, err := stores.SlackInstallations.GetActiveByOrgTeamApp(ctx, selectedOrgID, input.TeamID, currentInstall.APIAppID)
	if err != nil {
		return fmt.Errorf("get selected Slack installation for org selection: %w", err)
	}
	selection := &models.SlackOrgSelection{
		OrgID:               selectedOrgID,
		SlackInstallationID: selectedInstall.ID,
		SlackTeamID:         input.TeamID,
		APIAppID:            selectedInstall.APIAppID,
		SlackUserID:         input.UserID,
	}
	if err := stores.SlackOrgSelections.Upsert(ctx, selection); err != nil {
		return fmt.Errorf("persist Slack org selection: %w", err)
	}
	if input.ChannelID != "" || input.TriggerID != "" {
		cred, err := stores.Credentials.Get(ctx, selectedOrgID, models.ProviderSlack)
		if err != nil {
			return fmt.Errorf("get slack credentials: %w", err)
		}
		slackCfg, ok := cred.Config.(models.SlackConfig)
		if !ok {
			return fmt.Errorf("unexpected slack credential type")
		}
		text := "Future Slack actions from your account in this workspace will use the selected 143 organization.\n\nOpen 143: " + slackFrontendURL(services, "/settings/integrations")
		if input.TriggerID != "" {
			return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackInfoModal("Organization selection", text))
		}
		if input.UserID == "" {
			return nil
		}
		return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, text)
	}
	return nil
}

func slackInfoModal(title, text string) ingestion.SlackHomeView {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "143"
	}
	return ingestion.SlackHomeView{
		Type:  "modal",
		Title: &ingestion.SlackTextObject{Type: "plain_text", Text: truncateSlackButtonText(title)},
		Close: &ingestion.SlackTextObject{Type: "plain_text", Text: "Close"},
		Blocks: []ingestion.SlackBlock{{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
		}},
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
		SessionID      string `json:"session_id"`
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
		ResponseVisibility:        slackResponseVisibilityPtr(models.SlackResponseVisibilityThread),
		AllowedActions:            []string{"session", "preview"},
		NotificationSubscriptions: json.RawMessage(`{}`),
		Active:                    true,
	}
	if err := stores.SlackChannels.Upsert(ctx, settings); err != nil {
		return err
	}
	if strings.TrimSpace(value.SessionID) == "" {
		return nil
	}
	if stores.Sessions == nil || stores.Jobs == nil {
		return fmt.Errorf("slack repository selection session-start dependencies are not configured")
	}
	sessionID, err := uuid.Parse(value.SessionID)
	if err != nil {
		return fmt.Errorf("parse session_id: %w", err)
	}
	session, err := stores.Sessions.SetRepositoryContext(ctx, orgID, sessionID, repoID, defaultBranchPtr)
	if err != nil {
		return fmt.Errorf("set Slack session repository context: %w", err)
	}
	if !sessionStatusNeedsRunAgent(session.Status) {
		return nil
	}
	return enqueueRunAgentForSession(ctx, stores, session)
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
	responseVisibilityValue := models.SlackResponseVisibility(responseVisibility)
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
		ResponseVisibility:        &responseVisibilityValue,
		AllowedActions:            allowedActions,
		NotificationSubscriptions: notificationSubscriptions,
		Active:                    true,
	}
	return stores.SlackChannels.Upsert(ctx, settings)
}

func handleSlackMissingContextPrompt(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil {
		return fmt.Errorf("slack missing-context dependencies are not configured")
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
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Open the linked 143 session to add the missing context.")
	}
	var value struct {
		OrgID     string `json:"org_id"`
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	if input.Value != "" {
		if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
			return fmt.Errorf("parse Slack missing-context value: %w", err)
		}
	}
	kind := strings.TrimSpace(value.Kind)
	if kind == "" {
		switch input.ActionID {
		case "slack_choose_preview_target":
			kind = "preview_target"
		case "slack_choose_pull_request":
			kind = "pull_request"
		case "slack_choose_branch":
			kind = "branch"
		default:
			kind = "context"
		}
	}
	options, err := slackMissingContextOptions(ctx, stores, orgID, kind)
	if err != nil {
		return err
	}
	return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackMissingContextModal(kind, input.Value, options))
}

type slackMissingContextOption struct {
	Text  string
	Value string
}

func slackMissingContextOptions(ctx context.Context, stores *Stores, orgID uuid.UUID, kind string) ([]slackMissingContextOption, error) {
	options := []slackMissingContextOption{}
	if kind == "preview_target" || kind == "branch" {
		if stores != nil && stores.Repositories != nil {
			repos, err := stores.Repositories.ListByOrg(ctx, orgID, db.RepositoryFilters{})
			if err != nil {
				return nil, fmt.Errorf("list repositories for Slack missing-context options: %w", err)
			}
			for _, repo := range repos {
				if len(options) >= 100 {
					return options, nil
				}
				if kind == "preview_target" {
					options = append(options, slackMissingContextOption{
						Text:  repo.FullName,
						Value: slackActionValue(map[string]string{"repository_id": repo.ID.String(), "display": repo.FullName}),
					})
					if len(options) >= 100 {
						return options, nil
					}
				}
				branch := strings.TrimSpace(repo.DefaultBranch)
				if branch != "" {
					options = append(options, slackMissingContextOption{
						Text:  repo.FullName + " " + branch,
						Value: slackActionValue(map[string]string{"repository_id": repo.ID.String(), "branch": branch, "display": repo.FullName + "@" + branch}),
					})
				}
			}
		}
	}
	if kind == "preview_target" || kind == "pull_request" {
		if stores != nil && stores.PullRequests != nil && len(options) < 100 {
			prs, err := stores.PullRequests.ListByOrg(ctx, orgID, db.PullRequestFilters{Status: models.PullRequestStatusOpen, Limit: 100 - len(options)})
			if err != nil {
				return nil, fmt.Errorf("list pull requests for Slack missing-context options: %w", err)
			}
			for _, pr := range prs {
				if len(options) >= 100 {
					break
				}
				label := fmt.Sprintf("%s #%d", pr.GitHubRepo, pr.GitHubPRNumber)
				if strings.TrimSpace(pr.Title) != "" {
					label += " " + strings.TrimSpace(pr.Title)
				}
				options = append(options, slackMissingContextOption{
					Text: label,
					Value: slackActionValue(map[string]string{
						"pull_request_id":  pr.ID.String(),
						"pull_request_url": pr.GitHubPRURL,
						"display":          label,
					}),
				})
			}
		}
	}
	return options, nil
}

func slackMissingContextModal(kind string, privateMetadata string, options []slackMissingContextOption) ingestion.SlackHomeView {
	title := "Add context"
	label := "Context value"
	placeholder := "Paste a URL, branch, repository, session, or PR"
	switch kind {
	case "preview_target":
		title = "Preview target"
		label = "Branch, PR, session, or repository"
		placeholder = "main, https://github.com/acme/api/pull/42, or a session URL"
	case "pull_request":
		title = "Pull request"
		label = "Pull request URL or number"
		placeholder = "https://github.com/acme/api/pull/42"
	case "branch":
		title = "Branch"
		label = "Branch name"
		placeholder = "main"
	}
	return ingestion.SlackHomeView{
		Type:            "modal",
		CallbackID:      "slack_missing_context_modal",
		PrivateMetadata: privateMetadata,
		Title:           &ingestion.SlackTextObject{Type: "plain_text", Text: title},
		Submit:          &ingestion.SlackTextObject{Type: "plain_text", Text: "Continue"},
		Close:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Cancel"},
		Blocks: []ingestion.SlackBlock{{
			Type:    "input",
			BlockID: "context_value",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: label},
			Element: slackMissingContextInputElement(placeholder, options),
		}},
	}
}

func slackMissingContextInputElement(placeholder string, options []slackMissingContextOption) map[string]any {
	if len(options) > 0 {
		slackOptions := make([]map[string]any, 0, min(len(options), 100))
		for i, option := range options {
			if i >= 100 {
				break
			}
			slackOptions = append(slackOptions, map[string]any{
				"text":  map[string]string{"type": "plain_text", "text": truncateSlackButtonText(option.Text)},
				"value": option.Value,
			})
		}
		return map[string]any{
			"type":        "static_select",
			"action_id":   "value",
			"placeholder": map[string]string{"type": "plain_text", "text": placeholder},
			"options":     slackOptions,
		}
	}
	return map[string]any{
		"type":        "plain_text_input",
		"action_id":   "value",
		"placeholder": map[string]string{"type": "plain_text", "text": placeholder},
	}
}

func handleSlackMissingContextModal(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	var metadata struct {
		OrgID     string `json:"org_id"`
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	var payload struct {
		View struct {
			PrivateMetadata string `json:"private_metadata"`
		} `json:"view"`
	}
	if err := json.Unmarshal(input.RawPayload, &payload); err != nil {
		return fmt.Errorf("parse Slack missing-context modal payload: %w", err)
	}
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &metadata); err != nil {
		return fmt.Errorf("parse Slack missing-context metadata: %w", err)
	}
	rawOrgID := input.OrgID
	if rawOrgID == "" {
		rawOrgID = metadata.OrgID
	}
	orgID, sessionID, err := parseSlackSessionJobIDs(rawOrgID, metadata.SessionID)
	if err != nil {
		return err
	}
	value := slackModalSelectedValue(input.RawPayload, "context_value", "value")
	if value == "" {
		value = slackModalStringValue(input.RawPayload, "context_value", "value")
	}
	if value == "" {
		return nil
	}
	if metadata.Kind == "preview_target" && slackMissingContextValueHasPreviewTarget(value) {
		previewInput := input
		previewInput.OrgID = orgID.String()
		previewInput.ActionID = "slack_create_preview"
		previewInput.Value = value
		return handleSlackCreatePreview(ctx, stores, services, slackClient, previewInput)
	}
	display := slackMissingContextDisplayValue(metadata.Kind, value)
	prompt := slackMissingContextContinuationPrompt(metadata.Kind, display)
	return enqueueSlackSessionContinuationPromptWithReferences(ctx, stores, orgID, sessionID, prompt, slackMissingContextReferences(metadata.Kind, value))
}

func slackMissingContextContinuationPrompt(kind, value string) string {
	value = strings.TrimSpace(value)
	switch kind {
	case "preview_target":
		return "Continue this Slack-started session using this preview target: " + value + "\n\nCreate or update the preview and report the URL/status."
	case "pull_request":
		return "Continue this Slack-started session using this pull request: " + value + "\n\nInspect or repair the pull request and report the outcome."
	case "branch":
		return "Continue this Slack-started session using this branch: " + value + "\n\nProceed with the requested work and report the outcome."
	default:
		return "Continue this Slack-started session using this additional context: " + value
	}
}

func slackMissingContextValueHasPreviewTarget(value string) bool {
	var actionValue struct {
		SessionID     string `json:"session_id"`
		RepositoryID  string `json:"repository_id"`
		PullRequestID string `json:"pull_request_id"`
	}
	if err := json.Unmarshal([]byte(value), &actionValue); err != nil {
		return false
	}
	return actionValue.SessionID != "" || actionValue.RepositoryID != "" || actionValue.PullRequestID != ""
}

func slackMissingContextDisplayValue(kind, value string) string {
	var actionValue struct {
		Display        string `json:"display"`
		PullRequestURL string `json:"pull_request_url"`
		Branch         string `json:"branch"`
		RepositoryID   string `json:"repository_id"`
		PullRequestID  string `json:"pull_request_id"`
		SessionID      string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(value), &actionValue); err == nil {
		switch {
		case strings.TrimSpace(actionValue.Display) != "":
			return strings.TrimSpace(actionValue.Display)
		case strings.TrimSpace(actionValue.PullRequestURL) != "":
			return strings.TrimSpace(actionValue.PullRequestURL)
		case strings.TrimSpace(actionValue.Branch) != "":
			return strings.TrimSpace(actionValue.Branch)
		case strings.TrimSpace(actionValue.RepositoryID) != "":
			return "repository " + strings.TrimSpace(actionValue.RepositoryID)
		case strings.TrimSpace(actionValue.PullRequestID) != "":
			return "pull request " + strings.TrimSpace(actionValue.PullRequestID)
		case strings.TrimSpace(actionValue.SessionID) != "":
			return "session " + strings.TrimSpace(actionValue.SessionID)
		}
	}
	return strings.TrimSpace(value)
}

func slackMissingContextReferences(kind, value string) []slackContextReference {
	display := slackMissingContextDisplayValue(kind, value)
	if display == "" {
		return nil
	}
	var actionValue struct {
		Display        string `json:"display"`
		PullRequestURL string `json:"pull_request_url"`
		Branch         string `json:"branch"`
		RepositoryID   string `json:"repository_id"`
		PullRequestID  string `json:"pull_request_id"`
	}
	_ = json.Unmarshal([]byte(value), &actionValue)
	ref := slackContextReference{Value: display, Source: "slack_modal"}
	switch kind {
	case "pull_request":
		ref.Kind = slackReferenceKindPullRequest
	case "branch":
		ref.Kind = slackReferenceKindBranch
	case "preview_target":
		switch {
		case strings.TrimSpace(actionValue.PullRequestURL) != "" || strings.TrimSpace(actionValue.PullRequestID) != "":
			ref.Kind = slackReferenceKindPullRequest
		case strings.TrimSpace(actionValue.Branch) != "":
			ref.Kind = slackReferenceKindBranch
		case strings.TrimSpace(actionValue.RepositoryID) != "":
			ref.Kind = slackReferenceKindRepository
		default:
			ref.Kind = classifySlackURLReference(display)
		}
	default:
		ref.Kind = classifySlackURLReference(display)
	}
	return []slackContextReference{ref}
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

func handleSlackCreatePreview(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil {
		return fmt.Errorf("slack preview prompt dependencies are not configured")
	}
	var actionValue struct {
		SessionID     string `json:"session_id"`
		RepositoryID  string `json:"repository_id"`
		PullRequestID string `json:"pull_request_id"`
		Branch        string `json:"branch"`
		CommitSHA     string `json:"commit_sha"`
		ConfigName    string `json:"config_name"`
	}
	if input.Value != "" {
		_ = json.Unmarshal([]byte(input.Value), &actionValue)
	}
	orgID, err := uuid.Parse(input.OrgID)
	if err != nil {
		return fmt.Errorf("parse org_id: %w", err)
	}
	if actionValue.SessionID != "" {
		if services == nil || services.SlackPreviewControl == nil || stores.Sessions == nil {
			return fmt.Errorf("slack preview control dependencies are not configured")
		}
		sessionID, err := uuid.Parse(actionValue.SessionID)
		if err != nil {
			return fmt.Errorf("parse session_id: %w", err)
		}
		auth, err := authorizeSlackSessionAction(ctx, stores, input, orgID, sessionID, slackbotsvc.CapabilityPreview, true)
		if err != nil {
			return err
		}
		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("get session for Slack preview creation: %w", err)
		}
		target := models.SlackPreviewTarget{
			Kind:      models.SlackPreviewTargetSession,
			SessionID: &session.ID,
		}
		if session.RepositoryID != nil {
			target.RepositoryID = *session.RepositoryID
		}
		preview, err := services.SlackPreviewControl.CreatePreviewForSlack(ctx, orgID, target, models.SlackActor{
			UserID:      auth.ActorUserID,
			SlackTeamID: input.TeamID,
			SlackUserID: input.UserID,
		})
		if err != nil {
			return fmt.Errorf("create Slack preview: %w", err)
		}
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Preview requested: "+slackPreviewURL(services, preview.ID))
	}
	if actionValue.RepositoryID != "" || actionValue.PullRequestID != "" {
		if services == nil || services.SlackPreviewControl == nil {
			return fmt.Errorf("slack preview control dependencies are not configured")
		}
		auth, err := authorizeSlackPreviewAction(ctx, stores, input, orgID)
		if err != nil {
			return err
		}
		if auth.ActorUserID == uuid.Nil {
			return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "You need to link your Slack account to 143 before creating previews.")
		}
		target := models.SlackPreviewTarget{
			Kind:       models.SlackPreviewTargetBranch,
			Branch:     actionValue.Branch,
			CommitSHA:  actionValue.CommitSHA,
			ConfigName: actionValue.ConfigName,
		}
		if actionValue.PullRequestID != "" {
			prID, err := uuid.Parse(actionValue.PullRequestID)
			if err != nil {
				return fmt.Errorf("parse pull_request_id: %w", err)
			}
			target.Kind = models.SlackPreviewTargetPullRequest
			target.PullRequestID = &prID
		} else {
			repoID, err := uuid.Parse(actionValue.RepositoryID)
			if err != nil {
				return fmt.Errorf("parse repository_id: %w", err)
			}
			target.RepositoryID = repoID
			if strings.TrimSpace(target.CommitSHA) != "" {
				target.Kind = models.SlackPreviewTargetCommit
			} else if strings.TrimSpace(target.Branch) == "" {
				target.Kind = models.SlackPreviewTargetRepository
			}
		}
		preview, err := services.SlackPreviewControl.CreatePreviewForSlack(ctx, orgID, target, models.SlackActor{
			UserID:      auth.ActorUserID,
			SlackTeamID: input.TeamID,
			SlackUserID: input.UserID,
		})
		if err != nil {
			return fmt.Errorf("create Slack preview: %w", err)
		}
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Preview requested: "+slackPreviewURL(services, preview.ID))
	}
	if stores.Credentials == nil {
		return fmt.Errorf("slack preview prompt dependencies are not configured")
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
	if _, err := authorizeSlackPreviewAction(ctx, stores, input, orgID); err != nil {
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
	url := slackPreviewURL(services, previewID)
	if services != nil && services.SlackPreviewControl != nil {
		actor := models.SlackActor{SlackTeamID: input.TeamID, SlackUserID: input.UserID}
		if stores.ExternalUserLinks != nil {
			if externalUserID, externalErr := lookupExternalSlackMappedUserID(ctx, stores.ExternalUserLinks, orgID, input.TeamID, input.UserID); externalErr == nil && externalUserID != nil {
				actor.UserID = *externalUserID
			}
		}
		if actor.UserID == uuid.Nil && stores.SlackUserLinks != nil {
			if link, linkErr := stores.SlackUserLinks.GetBySlackUser(ctx, orgID, input.TeamID, input.UserID); linkErr == nil && link.UserID != nil {
				actor.UserID = *link.UserID
			}
		}
		if resolved, openErr := services.SlackPreviewControl.OpenPreviewURL(ctx, orgID, previewID, actor); openErr == nil && resolved != "" {
			url = resolved
		}
	}
	text := "Open preview: " + url
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
	if _, err := authorizeSlackPreviewAction(ctx, stores, input, orgID); err != nil {
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

func handleSlackSpecializedSessionAction(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	var value struct {
		OrgID     string `json:"org_id"`
		SessionID string `json:"session_id"`
	}
	if input.Value != "" {
		if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
			return fmt.Errorf("parse Slack specialized action value: %w", err)
		}
	}
	rawOrgID := input.OrgID
	if rawOrgID == "" {
		rawOrgID = value.OrgID
	}
	orgID, sessionID, err := parseSlackSessionJobIDs(rawOrgID, value.SessionID)
	if err != nil {
		return err
	}
	capability := slackbotsvc.CapabilityPRRequest
	if input.ActionID == "slack_claim_team_session" || input.ActionID == "slack_start_work" {
		capability = slackbotsvc.CapabilitySession
	}
	auth, err := authorizeSlackSessionAction(ctx, stores, input, orgID, sessionID, capability, true)
	if err != nil {
		return err
	}
	switch input.ActionID {
	case "slack_create_pr":
		if stores == nil || stores.Jobs == nil {
			return fmt.Errorf("slack PR creation dependencies are not configured")
		}
		payload := map[string]string{
			"org_id":               orgID.String(),
			"session_id":           sessionID.String(),
			"requested_by_user_id": auth.ActorUserID.String(),
		}
		dedupeKey := "slack_create_pr:" + sessionID.String()
		if _, err := stores.Jobs.Enqueue(ctx, orgID, "default", "open_pr", payload, 5, &dedupeKey); err != nil {
			return fmt.Errorf("enqueue Slack PR creation: %w", err)
		}
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Pull request creation requested for this session.")
	case "slack_repair_pr", "slack_start_work":
		prompt := "Continue this session and repair the pull request or branch publication failure. Report the outcome and any next action."
		if input.ActionID == "slack_start_work" {
			if stores == nil || stores.Sessions == nil {
				return fmt.Errorf("slack start-work dependencies are not configured")
			}
			session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
			if err != nil {
				return fmt.Errorf("load Slack session for start-work routing: %w", err)
			}
			manifest := slackRoutingInputManifest(session.InputManifest, slackbotsvc.SlackRoutingModeStartWork, "Slack Start work action")
			if _, err := stores.Sessions.UpdateInputManifest(ctx, orgID, sessionID, manifest); err != nil {
				return fmt.Errorf("update Slack session start-work routing: %w", err)
			}
			prompt = "Continue this session from Slack and start the requested work. Report progress and outcome back to Slack."
		}
		return enqueueSlackSessionContinuationPrompt(ctx, stores, orgID, sessionID, prompt)
	case "slack_merge_pr":
		if stores == nil || stores.PullRequests == nil || services == nil || services.PR == nil {
			return fmt.Errorf("slack PR merge dependencies are not configured")
		}
		pr, err := stores.PullRequests.GetBySessionID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("load pull request for Slack merge: %w", err)
		}
		if _, err := services.PR.QueueMergeWhenReady(ctx, orgID, pr.ID, auth.ActorUserID); err != nil {
			return fmt.Errorf("merge pull request from Slack: %w", err)
		}
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Merge requested for this pull request.")
	case "slack_claim_team_session":
		if stores == nil || stores.SlackSessionLinks == nil {
			return fmt.Errorf("slack team-session claim dependencies are not configured")
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("load Slack session link for claim: %w", err)
		}
		if _, err := stores.SlackSessionLinks.ClaimTeamSession(ctx, orgID, link.ID, auth.ActorUserID, input.UserID); err != nil {
			return fmt.Errorf("claim Slack team session: %w", err)
		}
		return postSlackEphemeralIfPossible(ctx, stores, slackClient, orgID, input, "Team session claimed. Future personal actions will use your linked 143 account.")
	default:
		return nil
	}
}

func enqueueSlackSessionContinuationPrompt(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID, prompt string) error {
	return enqueueSlackSessionContinuationPromptWithReferences(ctx, stores, orgID, sessionID, prompt, nil)
}

func enqueueSlackSessionContinuationPromptWithReferences(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID, prompt string, refs []slackContextReference) error {
	if stores == nil || stores.Sessions == nil || stores.SessionMessages == nil || stores.Jobs == nil {
		return fmt.Errorf("slack session continuation dependencies are not configured")
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("get session for Slack continuation: %w", err)
	}
	return enqueueSlackSessionContinuationMessage(ctx, stores, orgID, session, session.TriggeredByUserID, prompt, refs)
}

func enqueueSlackSessionContinuationMessage(ctx context.Context, stores *Stores, orgID uuid.UUID, session models.Session, userID *uuid.UUID, prompt string, refs []slackContextReference) error {
	if stores == nil || stores.SessionMessages == nil || stores.SessionThreads == nil || stores.Jobs == nil {
		return fmt.Errorf("slack session continuation dependencies are not configured")
	}
	thread, err := primaryThreadForSlackContinuation(ctx, stores, session)
	if err != nil {
		return fmt.Errorf("resolve primary thread for Slack continuation: %w", err)
	}
	threadIDLocal := thread.ID
	msg := &models.SessionMessage{
		SessionID:  session.ID,
		OrgID:      orgID,
		ThreadID:   &threadIDLocal,
		UserID:     userID,
		TurnNumber: thread.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    prompt,
		References: slackContextReferencesForSessionInput(refs),
	}
	if err := stores.SessionMessages.Create(ctx, msg); err != nil {
		return fmt.Errorf("create Slack continuation message: %w", err)
	}
	dedupeKey := db.ContinueSessionDedupeKey(thread.ID)
	payload := map[string]string{
		"org_id":     orgID.String(),
		"session_id": session.ID.String(),
		"thread_id":  thread.ID.String(),
	}
	_, err = stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", payload, 5, &dedupeKey)
	return err
}

func primaryThreadForSlackContinuation(ctx context.Context, stores *Stores, session models.Session) (models.SessionThread, error) {
	if stores == nil || stores.SessionThreads == nil {
		return models.SessionThread{}, fmt.Errorf("session thread store not configured")
	}
	if session.PrimaryThreadID != nil && *session.PrimaryThreadID != uuid.Nil {
		return stores.SessionThreads.GetByID(ctx, session.OrgID, *session.PrimaryThreadID)
	}
	threads, err := stores.SessionThreads.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("list session threads: %w", err)
	}
	if len(threads) == 0 {
		return models.SessionThread{}, fmt.Errorf("session has no threads")
	}
	return threads[0], nil
}

func postSlackEphemeralIfPossible(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, orgID uuid.UUID, input models.SlackInteractionJobPayload, text string) error {
	if stores == nil || stores.Credentials == nil || slackClient == nil || input.ChannelID == "" || input.UserID == "" {
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
	return slackClient.PostEphemeral(ctx, slackCfg.AccessToken, input.ChannelID, input.UserID, text)
}

type slackActionAuthorizationDecision struct {
	ActorUserID uuid.UUID
}

func authorizeSlackSessionAction(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload, orgID, sessionID uuid.UUID, capability slackbotsvc.Capability, requireMapped bool) (slackActionAuthorizationDecision, error) {
	if stores == nil {
		return slackActionAuthorizationDecision{}, fmt.Errorf("slack action dependencies are not configured")
	}
	isOriginatingTeamSession := false
	if stores.SlackSessionLinks != nil {
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return slackActionAuthorizationDecision{}, fmt.Errorf("load slack session link for action authorization: %w", err)
		} else if err == nil {
			isOriginatingTeamSession = link.TeamSession &&
				link.SlackTeamID == input.TeamID &&
				link.SlackChannelID == input.ChannelID
		}
	}
	authorizer := slackbotsvc.NewAuthorizer(stores.SlackUserLinks, stores.Memberships, stores.SlackChannels)
	decision, err := authorizer.Authorize(ctx, slackbotsvc.ActionRequest{
		OrgID:                    orgID,
		TeamID:                   input.TeamID,
		ChannelID:                input.ChannelID,
		SlackUserID:              input.UserID,
		Capability:               capability,
		AllowedRoles:             []models.Role{models.RoleAdmin, models.RoleMember, models.RoleBuilder},
		RequireMapped:            requireMapped,
		AllowUnmappedTeamSession: true,
		IsOriginatingTeamSession: isOriginatingTeamSession,
	})
	if err != nil {
		return slackActionAuthorizationDecision{}, err
	}
	if decision.MappedUserID == nil {
		return slackActionAuthorizationDecision{}, fmt.Errorf("slack action requires a linked 143 user")
	}
	return slackActionAuthorizationDecision{ActorUserID: *decision.MappedUserID}, nil
}

func authorizeSlackPreviewAction(ctx context.Context, stores *Stores, input models.SlackInteractionJobPayload, orgID uuid.UUID) (slackActionAuthorizationDecision, error) {
	if stores == nil {
		return slackActionAuthorizationDecision{}, fmt.Errorf("slack preview action dependencies are not configured")
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
			return slackActionAuthorizationDecision{}, fmt.Errorf("parse slack preview action session_id: %w", err)
		}
		link, err := stores.SlackSessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return slackActionAuthorizationDecision{}, fmt.Errorf("load slack session link for preview action: %w", err)
		} else if err == nil {
			isOriginatingTeamSession = link.TeamSession &&
				link.SlackTeamID == input.TeamID &&
				link.SlackChannelID == input.ChannelID
		}
	}
	authorizer := slackbotsvc.NewAuthorizer(stores.SlackUserLinks, stores.Memberships, stores.SlackChannels)
	decision, err := authorizer.Authorize(ctx, slackbotsvc.ActionRequest{
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
	if err != nil {
		return slackActionAuthorizationDecision{}, err
	}
	if decision.MappedUserID == nil {
		return slackActionAuthorizationDecision{}, nil
	}
	return slackActionAuthorizationDecision{ActorUserID: *decision.MappedUserID}, nil
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

func handleSlackHumanInputMultiPrompt(ctx context.Context, stores *Stores, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	if stores == nil || stores.Credentials == nil || stores.HumanInputRequests == nil {
		return fmt.Errorf("slack human-input multi-choice dependencies are not configured")
	}
	var value struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(input.Value), &value); err != nil {
		return fmt.Errorf("parse Slack human-input multi-choice value: %w", err)
	}
	orgID, sessionID, err := parseSlackSessionJobIDs(input.OrgID, value.SessionID)
	if err != nil {
		return err
	}
	requestID, err := uuid.Parse(value.RequestID)
	if err != nil {
		return fmt.Errorf("parse human input request id: %w", err)
	}
	req, err := stores.HumanInputRequests.GetByID(ctx, orgID, sessionID, requestID)
	if err != nil {
		return fmt.Errorf("get human input request for Slack multi-choice: %w", err)
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
	return slackClient.OpenView(ctx, slackCfg.AccessToken, input.TriggerID, slackHumanInputMultiModal(input.Value, req))
}

func slackHumanInputMultiModal(privateMetadata string, req models.HumanInputRequest) ingestion.SlackHomeView {
	options := make([]map[string]any, 0, min(len(req.Choices), 100))
	for i, choice := range req.Choices {
		if i >= 100 {
			break
		}
		label := strings.TrimSpace(choice.Label)
		if label == "" {
			label = choice.ID
		}
		options = append(options, map[string]any{
			"text":  map[string]string{"type": "plain_text", "text": truncateSlackButtonText(label)},
			"value": choice.ID,
		})
	}
	return ingestion.SlackHomeView{
		Type:            "modal",
		CallbackID:      "slack_human_input_multi_modal",
		PrivateMetadata: privateMetadata,
		Title:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Choose options"},
		Submit:          &ingestion.SlackTextObject{Type: "plain_text", Text: "Send"},
		Close:           &ingestion.SlackTextObject{Type: "plain_text", Text: "Cancel"},
		Blocks: []ingestion.SlackBlock{{
			Type:    "input",
			BlockID: "choices",
			Label:   &ingestion.SlackTextObject{Type: "plain_text", Text: "Choices"},
			Element: map[string]any{
				"type":      "multi_static_select",
				"action_id": "selected",
				"options":   options,
			},
		}},
	}
}

func handleSlackHumanInputMultiModal(ctx context.Context, stores *Stores, services *Services, slackClient *ingestion.SlackAPIClient, input models.SlackInteractionJobPayload) error {
	var payload struct {
		View struct {
			PrivateMetadata string `json:"private_metadata"`
		} `json:"view"`
	}
	if err := json.Unmarshal(input.RawPayload, &payload); err != nil {
		return fmt.Errorf("parse Slack human-input multi-choice modal payload: %w", err)
	}
	var value struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &value); err != nil {
		return fmt.Errorf("parse Slack human-input multi-choice modal metadata: %w", err)
	}
	selected := slackModalSelectedValues(input.RawPayload, "choices", "selected")
	encoded, err := json.Marshal(map[string]any{
		"session_id":          value.SessionID,
		"request_id":          value.RequestID,
		"selected_choice_ids": selected,
	})
	if err != nil {
		return fmt.Errorf("marshal Slack human-input multi-choice answer: %w", err)
	}
	input.Value = string(encoded)
	return handleSlackHumanInputAnswer(ctx, stores, services, slackClient, input)
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
	if stores == nil || stores.HumanInputRequests == nil || stores.Jobs == nil || (stores.SlackUserLinks == nil && stores.ExternalUserLinks == nil) {
		return fmt.Errorf("slack human-input dependencies are not configured")
	}
	var value struct {
		SessionID         string   `json:"session_id"`
		RequestID         string   `json:"request_id"`
		Answer            string   `json:"answer"`
		SelectedChoiceIDs []string `json:"selected_choice_ids"`
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
	decision, err := authorizeSlackHumanInputAnswer(ctx, workerSlackHumanInputAuthStores{
		sessionLinks:  stores.SlackSessionLinks,
		userLinks:     stores.SlackUserLinks,
		externalLinks: stores.ExternalUserLinks,
		memberships:   stores.Memberships,
		requests:      stores.HumanInputRequests,
	}, orgID, sessionID, requestID, input)
	if err != nil {
		return err
	}
	answer := strings.TrimSpace(value.Answer)
	pendingReq, err := stores.HumanInputRequests.GetByID(ctx, orgID, sessionID, requestID)
	if err != nil {
		return fmt.Errorf("get human input request before Slack answer: %w", err)
	}
	answerInput := slackHumanInputAnswerInput(pendingReq, answer, value.SelectedChoiceIDs)
	if err := pendingReq.ValidateAnswer(answerInput); err != nil {
		return fmt.Errorf("validate Slack human-input answer: %w", err)
	}
	answerPayload, err := db.MarshalHumanInputAnswerPayload(answerInput)
	if err != nil {
		return fmt.Errorf("marshal Slack human-input answer: %w", err)
	}
	req, err := stores.HumanInputRequests.AnswerPending(ctx, orgID, sessionID, requestID, answerInput.AnswerText, answerPayload, decision.AnsweredByUserID)
	if err != nil {
		return fmt.Errorf("answer human input request from Slack: %w", err)
	}
	if stores.Credentials != nil && input.ChannelID != "" && input.MessageTS != "" {
		if cred, credErr := stores.Credentials.Get(ctx, orgID, models.ProviderSlack); credErr == nil {
			if slackCfg, ok := cred.Config.(models.SlackConfig); ok {
				updateText := slackHumanInputAnsweredText(input.UserID, pendingReq, answer)
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

func slackHumanInputAnswerInput(req models.HumanInputRequest, answer string, selectedChoiceIDs []string) models.HumanInputAnswerInput {
	answer = strings.TrimSpace(answer)
	switch req.Kind {
	case models.HumanInputRequestKindMultiChoice:
		choices := make([]string, 0, len(selectedChoiceIDs))
		for _, id := range selectedChoiceIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				choices = append(choices, trimmed)
			}
		}
		if len(choices) == 0 && answer != "" {
			choices = append(choices, answer)
		}
		return models.HumanInputAnswerInput{SelectedChoiceIDs: choices}
	case models.HumanInputRequestKindSingleChoice, models.HumanInputRequestKindToolApproval, models.HumanInputRequestKindActionChoice:
		if answer != "" {
			return models.HumanInputAnswerInput{SelectedChoiceIDs: []string{answer}}
		}
		return models.HumanInputAnswerInput{}
	default:
		return models.HumanInputAnswerInput{AnswerText: &answer}
	}
}

func slackHumanInputAnsweredText(slackUserID string, req models.HumanInputRequest, answer string) string {
	answerLabel := answer
	for _, choice := range req.Choices {
		if choice.ID == answer {
			answerLabel = strings.TrimSpace(choice.Label)
			if answerLabel == "" {
				answerLabel = choice.ID
			}
			break
		}
	}
	prefix := "Answered"
	if req.Kind == models.HumanInputRequestKindToolApproval || req.Kind == models.HumanInputRequestKindActionChoice {
		switch strings.ToLower(answer) {
		case "approve", "approved", "yes", "allow", "continue":
			prefix = "Approved"
		case "deny", "denied", "no", "reject", "stop", "cancel":
			prefix = "Denied"
		}
	}
	if slackUserID != "" {
		return fmt.Sprintf("%s by <@%s> at %s: %s", prefix, slackUserID, slackDateToken(time.Now()), answerLabel)
	}
	return fmt.Sprintf("%s at %s: %s", prefix, slackDateToken(time.Now()), answerLabel)
}

func slackDateToken(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	unix := t.UTC().Unix()
	return fmt.Sprintf("<!date^%d^{date_short_pretty} {time}|%s>", unix, t.UTC().Format(time.RFC3339))
}

type workerSlackHumanInputAuthStores struct {
	sessionLinks interface {
		GetBySession(ctx context.Context, orgID, sessionID uuid.UUID) (models.SlackSessionLink, error)
	}
	userLinks interface {
		GetBySlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string) (models.SlackUserLink, error)
	}
	externalLinks interface {
		GetActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) (models.ExternalUserLink, error)
	}
	memberships interface {
		Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
	}
	requests interface {
		GetByID(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (models.HumanInputRequest, error)
	}
}

type slackHumanInputAuthorizationDecision struct {
	AnsweredByUserID uuid.UUID
	TeamSessionClaim bool
}

func authorizeSlackHumanInputAnswer(ctx context.Context, stores workerSlackHumanInputAuthStores, orgID, sessionID, requestID uuid.UUID, input models.SlackInteractionJobPayload) (slackHumanInputAuthorizationDecision, error) {
	if stores.userLinks == nil && stores.externalLinks == nil {
		return slackHumanInputAuthorizationDecision{}, fmt.Errorf("slack human-input authorization requires user links")
	}
	var mappedUserID *uuid.UUID
	if stores.externalLinks != nil {
		externalUserID, err := lookupExternalSlackMappedUserID(ctx, stores.externalLinks, orgID, input.TeamID, input.UserID)
		if err != nil {
			return slackHumanInputAuthorizationDecision{}, fmt.Errorf("resolve external slack user link for human input: %w", err)
		}
		mappedUserID = externalUserID
	}
	if mappedUserID == nil && stores.userLinks != nil {
		link, err := stores.userLinks.GetBySlackUser(ctx, orgID, input.TeamID, input.UserID)
		if err != nil {
			return slackHumanInputAuthorizationDecision{}, fmt.Errorf("resolve slack user link for human input: %w", err)
		}
		mappedUserID = link.UserID
	}
	if mappedUserID == nil {
		return slackHumanInputAuthorizationDecision{}, fmt.Errorf("slack user is not linked to a 143 user")
	}
	if stores.memberships == nil {
		return slackHumanInputAuthorizationDecision{}, fmt.Errorf("slack human-input authorization requires memberships")
	}
	membership, err := stores.memberships.Get(ctx, *mappedUserID, orgID)
	if err != nil {
		return slackHumanInputAuthorizationDecision{}, fmt.Errorf("load slack human-input membership: %w", err)
	}
	if !roleCanAnswerSlackHumanInput(membership.Role) {
		return slackHumanInputAuthorizationDecision{}, fmt.Errorf("slack human-input answer requires member access")
	}
	if stores.requests != nil {
		req, err := stores.requests.GetByID(ctx, orgID, sessionID, requestID)
		if err != nil {
			return slackHumanInputAuthorizationDecision{}, fmt.Errorf("load human-input request for authorization: %w", err)
		}
		if req.AssignedUserID != nil && *req.AssignedUserID != *mappedUserID {
			return slackHumanInputAuthorizationDecision{}, fmt.Errorf("human-input request is assigned to another user")
		}
	}
	decision := slackHumanInputAuthorizationDecision{AnsweredByUserID: *mappedUserID}
	if stores.sessionLinks != nil {
		sessionLink, err := stores.sessionLinks.GetBySession(ctx, orgID, sessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return slackHumanInputAuthorizationDecision{}, fmt.Errorf("load slack session link for human input: %w", err)
		}
		if err == nil && sessionLink.TeamSession {
			if sessionLink.SlackTeamID != input.TeamID || sessionLink.SlackChannelID != input.ChannelID {
				return slackHumanInputAuthorizationDecision{}, fmt.Errorf("team-session human input must be answered from the originating Slack channel")
			}
			decision.TeamSessionClaim = true
		}
	}
	return decision, nil
}

func roleCanAnswerSlackHumanInput(role models.Role) bool {
	switch role {
	case models.RoleAdmin, models.RoleMember, models.RoleBuilder:
		return true
	default:
		return false
	}
}

func roleCanStartSlackSession(role models.Role) bool {
	switch role {
	case models.RoleAdmin, models.RoleMember, models.RoleBuilder:
		return true
	default:
		return false
	}
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

func renderSlackFinalBlocks(services *Services, content string, orgID, sessionID uuid.UUID, link models.SlackSessionLink, details slackSessionOutcomeDetails) (string, []ingestion.SlackBlock) {
	actions := []slackbotsvc.SlackAction{}
	if details.Preview != nil {
		value := slackActionValue(map[string]string{
			"org_id":     orgID.String(),
			"session_id": sessionID.String(),
			"preview_id": details.Preview.ID.String(),
		})
		actions = append(actions, slackbotsvc.SlackAction{
			Text:     "Open preview",
			ActionID: "slack_open_preview",
			Value:    value,
		})
	} else {
		actions = append(actions, slackbotsvc.SlackAction{
			Text:     "Create preview",
			ActionID: "slack_create_preview",
			Value: slackActionValue(map[string]string{
				"org_id":     orgID.String(),
				"session_id": sessionID.String(),
			}),
		})
	}
	if details.PullRequest != nil && strings.TrimSpace(details.PullRequest.GitHubPRURL) != "" {
		actions = append(actions, slackbotsvc.SlackAction{
			Text: "Review PR",
			URL:  strings.TrimSpace(details.PullRequest.GitHubPRURL),
		})
		if details.PullRequest.Status == models.PullRequestStatusOpen {
			actions = append(actions, slackbotsvc.SlackAction{
				Text:     "Merge PR",
				ActionID: "slack_merge_pr",
				Value: slackActionValue(map[string]string{
					"org_id":     orgID.String(),
					"session_id": sessionID.String(),
				}),
				Confirm: &slackbotsvc.SlackActionConfirm{
					Title:       "Merge pull request?",
					Text:        "143 will queue merge-when-ready for this pull request.",
					ConfirmText: "Queue merge",
					DenyText:    "Cancel",
				},
			})
		}
	} else if details.Session.Status == models.SessionStatusCompleted && details.Session.SnapshotKey != nil && strings.TrimSpace(*details.Session.SnapshotKey) != "" {
		actions = append(actions, slackbotsvc.SlackAction{
			Text:     "Create PR",
			ActionID: "slack_create_pr",
			Value: slackActionValue(map[string]string{
				"org_id":     orgID.String(),
				"session_id": sessionID.String(),
			}),
			Confirm: &slackbotsvc.SlackActionConfirm{
				Title:       "Create pull request?",
				Text:        "143 will publish this session's changes and open a pull request.",
				ConfirmText: "Create PR",
				DenyText:    "Cancel",
			},
		})
	}
	if details.Session.PRCreationState == models.PRCreationStateFailed || details.Session.PRPushState == models.PRPushStateFailed {
		actions = append(actions, slackbotsvc.SlackAction{
			Text:     "Repair PR",
			ActionID: "slack_repair_pr",
			Value: slackActionValue(map[string]string{
				"org_id":     orgID.String(),
				"session_id": sessionID.String(),
			}),
		})
	}
	rendered := slackbotsvc.RenderFinalResponse(content, slackbotsvc.SlackSessionRenderInput{
		Session:    models.Session{ID: sessionID},
		Link:       link,
		SessionURL: slackSessionURL(services, sessionID),
		Outcome: slackbotsvc.SlackSessionOutcome{
			BranchURL:     strings.TrimSpace(stringValue(details.Session.BranchURL)),
			PullRequest:   details.PullRequest,
			PreviewStatus: slackPreviewStatus(details.Preview),
			PreviewURL:    strings.TrimSpace(details.PreviewURL),
			DiffStats:     details.Session.DiffStats,
		},
		Actions: actions,
	})
	return rendered.Text, rendered.Blocks
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
	return slackbotsvc.TeamSessionLine()
}

func slackPreviewStatus(preview *models.PreviewInstance) models.PreviewStatus {
	if preview == nil {
		return ""
	}
	return preview.Status
}

func slackLifecycleStateForProgress(update slackbotsvc.SlackProgressUpdate) slackbotsvc.SessionLifecycleState {
	switch update.Kind {
	case slackbotsvc.SlackProgressCompleted:
		return slackbotsvc.SessionLifecycleComplete
	case slackbotsvc.SlackProgressFailed:
		return slackbotsvc.SessionLifecycleFailed
	case slackbotsvc.SlackProgressWaitingForInput:
		return slackbotsvc.SessionLifecycleWaiting
	default:
		return slackbotsvc.SessionLifecycleRunning
	}
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

const slackUserDisplayCacheTTL = 7 * 24 * time.Hour
const slackUserPromptLabelMaxRunes = 80

var slackUserMentionPattern = regexp.MustCompile(`<@([UW][A-Z0-9]+)>`)

type slackUserInfoFetcher interface {
	FetchUserInfo(ctx context.Context, accessToken, userID string) (ingestion.SlackUser, error)
}

type slackUserDisplayResolver interface {
	ResolveSlackUserDisplay(ctx context.Context, userID string) (slackUserDisplay, bool)
}

type slackUserDisplay struct {
	SlackID string
	Handle  string
}

type slackCachedUserDisplay struct {
	SlackID     string `json:"slack_id"`
	Name        string `json:"name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type slackCachedUserDisplayResolver struct {
	fetcher     slackUserInfoFetcher
	redis       *cache.Client
	accessToken string
	teamID      string
	logger      zerolog.Logger
	local       map[string]slackUserDisplay
}

func newSlackCachedUserDisplayResolver(fetcher slackUserInfoFetcher, redisClient *cache.Client, accessToken, teamID string, logger zerolog.Logger) *slackCachedUserDisplayResolver {
	return &slackCachedUserDisplayResolver{
		fetcher:     fetcher,
		redis:       redisClient,
		accessToken: accessToken,
		teamID:      strings.TrimSpace(teamID),
		logger:      logger,
		local:       make(map[string]slackUserDisplay),
	}
}

func slackRedisClient(services *Services) *cache.Client {
	if services == nil {
		return nil
	}
	return services.Redis
}

func slackUserDisplayCacheKey(teamID, userID string) string {
	return "143:slack:user:" + strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
}

func (r *slackCachedUserDisplayResolver) ResolveSlackUserDisplay(ctx context.Context, userID string) (slackUserDisplay, bool) {
	userID = strings.TrimSpace(userID)
	if r == nil || userID == "" {
		return slackUserDisplay{}, false
	}
	if display, ok := r.local[userID]; ok {
		return display, true
	}
	if display, ok := r.getShared(ctx, userID); ok {
		r.local[userID] = display
		return display, true
	}
	if r.fetcher == nil || strings.TrimSpace(r.accessToken) == "" {
		return slackUserDisplay{}, false
	}
	user, err := r.fetcher.FetchUserInfo(ctx, r.accessToken, userID)
	if err != nil {
		r.logger.Warn().Err(err).Str("slack_user_id", userID).Msg("failed to resolve Slack user display name")
		return slackUserDisplay{}, false
	}
	cached := slackCachedUserDisplay{
		SlackID:     user.ID,
		Name:        user.Name,
		RealName:    firstNonEmpty(user.Profile.RealName, user.RealName),
		DisplayName: user.Profile.DisplayName,
	}
	display := cached.toDisplay(userID)
	if strings.TrimSpace(display.Handle) == "" {
		return slackUserDisplay{}, false
	}
	r.local[userID] = display
	r.putShared(ctx, userID, cached)
	return display, true
}

func (r *slackCachedUserDisplayResolver) getShared(ctx context.Context, userID string) (slackUserDisplay, bool) {
	if r == nil || r.redis == nil || !r.redis.Available() || r.teamID == "" {
		return slackUserDisplay{}, false
	}
	key := slackUserDisplayCacheKey(r.teamID, userID)
	data, err := r.redis.GetBytes(ctx, key)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return slackUserDisplay{}, false
		}
		r.logger.Warn().Err(err).Str("key", key).Msg("failed to read Slack user display from Redis")
		return slackUserDisplay{}, false
	}
	var cached slackCachedUserDisplay
	if err := json.Unmarshal(data, &cached); err != nil {
		r.logger.Warn().Err(err).Str("key", key).Msg("failed to decode Slack user display from Redis")
		return slackUserDisplay{}, false
	}
	display := cached.toDisplay(userID)
	if strings.TrimSpace(display.Handle) == "" {
		return slackUserDisplay{}, false
	}
	return display, true
}

func (r *slackCachedUserDisplayResolver) putShared(ctx context.Context, userID string, cached slackCachedUserDisplay) {
	if r == nil || r.redis == nil || !r.redis.Available() || r.teamID == "" {
		return
	}
	key := slackUserDisplayCacheKey(r.teamID, userID)
	data, err := json.Marshal(cached)
	if err != nil {
		r.logger.Warn().Err(err).Str("slack_user_id", userID).Msg("failed to marshal Slack user display cache entry")
		return
	}
	if err := r.redis.SetBytes(ctx, key, data, slackUserDisplayCacheTTL); err != nil {
		r.logger.Warn().Err(err).Str("key", key).Msg("failed to write Slack user display to Redis")
	}
}

func (c slackCachedUserDisplay) toDisplay(fallbackID string) slackUserDisplay {
	return slackUserDisplay{
		SlackID: firstNonEmpty(c.SlackID, fallbackID),
		Handle:  sanitizeSlackUserHandle(c.Name),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sanitizeSlackUserHandle(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= slackUserPromptLabelMaxRunes {
			break
		}
	}
	return b.String()
}

func renderSlackPrompt(text, permalink string, threadMessages []ingestion.SlackMessage, references []slackContextReference, files []slackContextFile) string {
	return renderSlackPromptWithUserResolver(context.Background(), text, permalink, threadMessages, references, files, nil)
}

func renderSlackPromptWithUserResolver(ctx context.Context, text, permalink string, threadMessages []ingestion.SlackMessage, references []slackContextReference, files []slackContextFile, userResolver slackUserDisplayResolver) string {
	cleaned := strings.TrimSpace(text)
	cleaned = humanizeSlackMentions(ctx, cleaned, userResolver)
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
			line = humanizeSlackMentions(ctx, line, userResolver)
			if len(line) > 500 {
				line = line[:500] + "..."
			}
			b.WriteString("- ")
			if msg.User != "" {
				b.WriteString(slackUserAuthorLabel(ctx, msg.User, userResolver))
				b.WriteString(": ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func humanizeSlackMentions(ctx context.Context, text string, userResolver slackUserDisplayResolver) string {
	if userResolver == nil || text == "" {
		return text
	}
	return slackUserMentionPattern.ReplaceAllStringFunc(text, func(raw string) string {
		// raw is already the full pattern match "<@UXXX>"; extract the captured ID directly.
		userID := raw[2 : len(raw)-1]
		display, ok := userResolver.ResolveSlackUserDisplay(ctx, userID)
		if !ok || display.Handle == "" {
			return raw
		}
		return "@" + display.Handle
	})
}

func slackUserAuthorLabel(ctx context.Context, userID string, userResolver slackUserDisplayResolver) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "Slack user"
	}
	if userResolver == nil {
		return "<@" + userID + ">"
	}
	display, ok := userResolver.ResolveSlackUserDisplay(ctx, userID)
	if !ok || display.Handle == "" {
		return "<@" + userID + ">"
	}
	slackID := firstNonEmpty(display.SlackID, userID)
	return "@" + display.Handle + " (Slack " + slackID + ")"
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
	Kind   slackReferenceKind
	Value  string
	Source string
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
	addRef := func(kind slackReferenceKind, value, source string) {
		value = strings.TrimRight(strings.TrimSpace(value), ".,)")
		if value == "" || seen[string(kind)+":"+value] {
			return
		}
		seen[string(kind)+":"+value] = true
		refs = append(refs, slackContextReference{Kind: kind, Value: value, Source: source})
	}
	addRefs := func(input, source string) {
		for _, rawURL := range slackURLReferencePattern.FindAllString(input, -1) {
			urlRef := strings.TrimRight(rawURL, ".,)")
			kind := classifySlackURLReference(urlRef)
			addRef(kind, urlRef, source)
			if len(refs) >= 20 {
				return
			}
		}
		for _, ref := range slackLinearIssuePattern.FindAllString(input, -1) {
			addRef(slackReferenceKindIssue, ref, source)
			if len(refs) >= 20 {
				return
			}
		}
		for _, ref := range slackRepoIssuePattern.FindAllString(input, -1) {
			addRef(slackReferenceKindIssue, ref, source)
			if len(refs) >= 20 {
				return
			}
		}
		for _, match := range slackBranchPattern.FindAllStringSubmatch(input, -1) {
			if len(match) > 1 {
				addRef(slackReferenceKindBranch, match[1], source)
			}
			if len(refs) >= 20 {
				return
			}
		}
		for _, match := range slackFilePathReferencePattern.FindAllStringSubmatch(input, -1) {
			if len(match) > 1 {
				addRef(slackReferenceKindFilePath, match[1], source)
			}
			if len(refs) >= 20 {
				return
			}
		}
	}
	addRefs(text, "message")
	for _, msg := range threadMessages {
		if len(refs) >= 20 {
			break
		}
		addRefs(msg.Text, "thread")
	}
	return refs
}

func slackContextReferencesForResolver(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, refs []slackContextReference) []slackbotsvc.SlackContextReference {
	converted := make([]slackbotsvc.SlackContextReference, 0, len(refs))
	for _, ref := range refs {
		resolved := slackbotsvc.SlackContextReference{
			Kind:   slackbotsvc.SlackContextReferenceKind(ref.Kind),
			Value:  ref.Value,
			Source: ref.Source,
		}
		if ref.Kind == slackReferenceKindRepository {
			resolved.ResolvedID = resolveSlackRepositoryReferenceID(ctx, stores, logger, orgID, ref.Value)
		}
		converted = append(converted, resolved)
	}
	return converted
}

func resolveSlackRepositoryReferenceID(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, value string) *uuid.UUID {
	if stores == nil || stores.Repositories == nil || orgID == uuid.Nil {
		return nil
	}
	fullName := slackRepositoryFullNameFromReference(value)
	if fullName == "" {
		return nil
	}
	repo, err := stores.Repositories.GetByFullName(ctx, orgID, fullName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("repository", fullName).Msg("failed to resolve Slack repository reference")
		}
		return nil
	}
	return &repo.ID
}

func slackRepositoryFullNameFromReference(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return parts[0] + "/" + parts[1]
		}
		return ""
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(parts[0], " ") && !strings.Contains(parts[1], " ") {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

func slackContextReferencesForSessionInput(refs []slackContextReference) models.SessionInputReferences {
	if len(refs) == 0 {
		return nil
	}
	out := make(models.SessionInputReferences, 0, len(refs))
	for _, ref := range refs {
		value := strings.TrimSpace(ref.Value)
		if value == "" {
			continue
		}
		switch ref.Kind {
		case slackReferenceKindFilePath:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindFile,
				Token:   "@" + value,
				Path:    value,
				Display: value,
			})
		case slackReferenceKindSentry:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "sentry",
				Display: value,
			})
		case slackReferenceKindPullRequest, slackReferenceKindRepository:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "github",
				Display: value,
			})
		case slackReferenceKindIssue:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "linear",
				Display: value,
			})
		case slackReferenceKindPreview:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "preview",
				Display: value,
			})
		case slackReferenceKindBranch:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "branch",
				Display: value,
			})
		default:
			out = append(out, models.SessionInputReference{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "url",
				Display: value,
			})
		}
	}
	return out
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
	displayName := strings.TrimSpace(slackUser.Profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(slackUser.RealName)
	}
	if resolved, resolveErr := resolveExternalSlackUser(ctx, stores, orgID, teamID, slackUserID, &email, &displayName, logger); resolveErr != nil {
		logger.Warn().Err(resolveErr).Str("slack_user_id", slackUserID).Msg("failed to resolve unified Slack email mapping")
	} else if resolved != nil {
		return resolved
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

func resolveExternalSlackUser(ctx context.Context, stores *Stores, orgID uuid.UUID, teamID, slackUserID string, email, displayName *string, logger zerolog.Logger) (*uuid.UUID, error) {
	if stores == nil || stores.ExternalUserLinks == nil || stores.Users == nil {
		return nil, nil
	}
	resolver := externalidentity.NewResolver(stores.ExternalUserLinks, stores.ExternalSuggestions, nil, stores.Users, externalidentity.Options{})
	input := externalidentity.ExternalActorInput{
		Provider:            models.ExternalIdentityProviderSlack,
		ProviderWorkspaceID: strings.TrimSpace(teamID),
		ProviderUserID:      strings.TrimSpace(slackUserID),
		Email:               email,
		EmailVerified:       false,
		DisplayName:         displayName,
	}
	resolution, err := resolver.ResolveExternalActor(ctx, orgID, input)
	if err != nil {
		return nil, err
	}
	if resolution.MappedUserID == nil {
		return nil, nil
	}
	return resolution.MappedUserID, nil
}

func lookupExternalSlackMappedUserID(ctx context.Context, links interface {
	GetActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) (models.ExternalUserLink, error)
}, orgID uuid.UUID, teamID, slackUserID string) (*uuid.UUID, error) {
	if links == nil {
		return nil, nil
	}
	link, err := links.GetActiveByExternal(ctx, orgID, models.ExternalIdentityProviderSlack, strings.TrimSpace(teamID), strings.TrimSpace(slackUserID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	userID := link.UserID
	return &userID, nil
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
	state := slackbotsvc.SessionLifecycleStarting
	if strings.EqualFold(strings.TrimSpace(verb), "continuing") {
		state = slackbotsvc.SessionLifecycleRunning
	}
	return slackbotsvc.RenderSessionStatus(slackbotsvc.SlackSessionRenderInput{
		Session:    models.Session{ID: sessionID},
		State:      state,
		Title:      strings.TrimSpace(verb) + " a 143 session",
		SessionURL: slackSessionURL(services, sessionID),
	}).Text
}

func slackSessionAckBlocks(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID, installationID uuid.UUID, teamID, channelID string, session *models.Session, text string, contextSummary slackbotsvc.SlackSessionContextSummary, routingMode slackbotsvc.SlackRoutingMode) []ingestion.SlackBlock {
	var sessionID uuid.UUID
	if session != nil {
		sessionID = session.ID
	}
	loadedContext := slackSessionAckContext(ctx, stores, logger, orgID, teamID, channelID, session)
	if contextSummary.RepositoryName == "" {
		contextSummary.RepositoryName = loadedContext.RepositoryName
	}
	if contextSummary.Branch == "" {
		contextSummary.Branch = loadedContext.Branch
	}
	if len(contextSummary.Missing) == 0 {
		contextSummary.Missing = loadedContext.Missing
	}
	if routingMode == "" || routingMode == slackbotsvc.SlackRoutingModeAuto {
		routingMode = slackSessionAckRoutingMode(ctx, stores, logger, orgID, teamID, channelID)
	}
	actions := []slackbotsvc.SlackAction{{
		Text: "Join session",
		URL:  slackSessionURL(services, sessionID),
	}}
	if session != nil {
		actions = append(actions, slackbotsvc.SlackAction{
			Text:     "Change repo",
			ActionID: "slack_configure_channel",
			Value: slackActionValue(map[string]string{
				"session_id": session.ID.String(),
				"team_id":    teamID,
				"channel_id": channelID,
			}),
		})
		for _, missing := range contextSummary.Missing {
			switch missing.Kind {
			case "preview_target":
				actions = append(actions, slackbotsvc.SlackAction{
					Text:     "Choose preview target",
					ActionID: "slack_choose_preview_target",
					Value: slackActionValue(map[string]string{
						"org_id":     orgID.String(),
						"session_id": session.ID.String(),
						"kind":       "preview_target",
					}),
				})
			case "pull_request":
				actions = append(actions, slackbotsvc.SlackAction{
					Text:     "Choose PR",
					ActionID: "slack_choose_pull_request",
					Value: slackActionValue(map[string]string{
						"org_id":     orgID.String(),
						"session_id": session.ID.String(),
						"kind":       "pull_request",
					}),
				})
			case "branch":
				actions = append(actions, slackbotsvc.SlackAction{
					Text:     "Choose branch",
					ActionID: "slack_choose_branch",
					Value: slackActionValue(map[string]string{
						"org_id":     orgID.String(),
						"session_id": session.ID.String(),
						"kind":       "branch",
					}),
				})
			}
		}
	}
	blocks := slackbotsvc.RenderSessionStatus(slackbotsvc.SlackSessionRenderInput{
		Session:     models.Session{ID: sessionID},
		State:       slackbotsvc.SessionLifecycleStarting,
		Title:       strings.TrimSpace(text),
		Context:     contextSummary,
		RoutingMode: routingMode,
		SessionURL:  "",
		Actions:     actions,
	}).Blocks
	if session == nil {
		blocks = []ingestion.SlackBlock{{
			Type: "section",
			Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
		}}
	}
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
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: "Choose a repository before I start durable work for this Slack thread."},
	}, ingestion.SlackBlock{Type: "actions", Elements: elements})
	return blocks
}

func slackSessionAckContext(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, teamID, channelID string, session *models.Session) slackbotsvc.SlackSessionContextSummary {
	summary := slackbotsvc.SlackSessionContextSummary{}
	if session == nil {
		return summary
	}
	var repoID *uuid.UUID
	if session.RepositoryID != nil {
		repoID = session.RepositoryID
	}
	if session.TargetBranch != nil {
		summary.Branch = strings.TrimSpace(*session.TargetBranch)
	}
	if stores != nil && stores.SlackChannels != nil && channelID != "" {
		settings, err := stores.SlackChannels.GetEffectiveByChannel(ctx, orgID, teamID, channelID)
		if err == nil {
			if repoID == nil {
				repoID = settings.DefaultRepositoryID
			}
			if summary.Branch == "" && settings.DefaultBranch != nil {
				summary.Branch = strings.TrimSpace(*settings.DefaultBranch)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to load Slack ack effective settings")
		}
	}
	if repoID != nil && stores != nil && stores.Repositories != nil {
		repo, err := stores.Repositories.GetByID(ctx, orgID, *repoID)
		if err == nil {
			summary.RepositoryName = repo.FullName
			if summary.Branch == "" {
				summary.Branch = repo.DefaultBranch
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("repository_id", repoID.String()).Msg("failed to load Slack ack repository")
		}
	}
	if summary.RepositoryName == "" {
		summary.Missing = append(summary.Missing, slackbotsvc.MissingSlackContext{
			Kind:   "repository",
			Reason: "Select a default repository to make Slack-started sessions more precise.",
		})
	}
	return summary
}

func slackSessionAckRoutingMode(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, teamID, channelID string) slackbotsvc.SlackRoutingMode {
	if stores == nil || stores.SlackChannels == nil || channelID == "" {
		return slackbotsvc.SlackRoutingModeAuto
	}
	settings, err := stores.SlackChannels.GetEffectiveByChannel(ctx, orgID, teamID, channelID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to load Slack ack routing mode")
		}
		return slackbotsvc.SlackRoutingModeAuto
	}
	return slackbotsvc.SlackRoutingMode(settings.RoutingMode)
}

func refreshSlackLinkedSessionRouting(ctx context.Context, stores *Stores, llm llmClient, logger zerolog.Logger, orgID uuid.UUID, teamID, channelID, text string, session models.Session) (models.Session, slackbotsvc.SlackRoutingMode, error) {
	baseMode := slackSessionAckRoutingMode(ctx, stores, logger, orgID, teamID, channelID)
	resolved := resolveSlackAutoRouting(ctx, llm, logger, text, slackbotsvc.SlackContextResolveResult{RoutingMode: baseMode})
	manifest := slackRoutingInputManifest(session.InputManifest, resolved.RoutingMode, resolved.RoutingReason)
	if string(manifest) == string(session.InputManifest) {
		session.InputManifest = manifest
		return session, resolved.RoutingMode, nil
	}
	if stores == nil || stores.Sessions == nil {
		return session, resolved.RoutingMode, fmt.Errorf("slack session store is not configured")
	}
	updated, err := stores.Sessions.UpdateInputManifest(ctx, orgID, session.ID, manifest)
	if err != nil {
		return session, resolved.RoutingMode, fmt.Errorf("update Slack routing input manifest: %w", err)
	}
	return updated, resolved.RoutingMode, nil
}

func resolveSlackAutoRouting(ctx context.Context, llm llmClient, logger zerolog.Logger, text string, resolved slackbotsvc.SlackContextResolveResult) slackbotsvc.SlackContextResolveResult {
	if override := slackbotsvc.RoutingOverrideFromText(text); override != "" {
		return applySlackRoutingMode(resolved, override, "explicit Slack routing command")
	}
	if resolved.RoutingMode != "" && resolved.RoutingMode != slackbotsvc.SlackRoutingModeAuto {
		return resolved
	}
	if slackTextDeterministicallyRequestsWork(text) {
		return applySlackRoutingMode(resolved, slackbotsvc.SlackRoutingModeStartWork, "imperative Slack request to modify product behavior")
	}
	classification := classifySlackRouting(ctx, llm, logger, text)
	return applySlackRoutingMode(resolved, classification.RoutingMode, classification.Reason)
}

type slackRoutingClassification struct {
	RoutingMode slackbotsvc.SlackRoutingMode
	Confidence  float64
	Reason      string
}

func classifySlackRouting(ctx context.Context, llm llmClient, logger zerolog.Logger, text string) slackRoutingClassification {
	if llm == nil {
		return fallbackSlackRoutingClassification(text, "LLM classifier unavailable")
	}
	classifierCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := llm.Complete(classifierCtx, prompts.SlackRoutingClassifierPrompt(), strings.TrimSpace(text))
	if err != nil {
		logger.Warn().Err(err).Msg("Slack routing classifier failed, using heuristic fallback")
		return fallbackSlackRoutingClassification(text, "LLM classifier failed")
	}
	classification, err := parseSlackRoutingClassification(response)
	if err != nil {
		logger.Warn().Err(err).Msg("Slack routing classifier returned invalid response, using heuristic fallback")
		return fallbackSlackRoutingClassification(text, "LLM classifier returned invalid JSON")
	}
	if classification.Confidence < 0.65 {
		return fallbackSlackRoutingClassification(text, "LLM classifier confidence below threshold")
	}
	return classification
}

func parseSlackRoutingClassification(response string) (slackRoutingClassification, error) {
	jsonText := extractFirstJSONObject(response)
	var raw struct {
		RoutingMode slackbotsvc.SlackRoutingMode `json:"routing_mode"`
		Confidence  float64                      `json:"confidence"`
		Reason      string                       `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonText), &raw); err != nil {
		return slackRoutingClassification{}, err
	}
	switch raw.RoutingMode {
	case slackbotsvc.SlackRoutingModeAnswerOnly, slackbotsvc.SlackRoutingModeStartWork:
	default:
		return slackRoutingClassification{}, fmt.Errorf("invalid Slack routing mode %q", raw.RoutingMode)
	}
	return slackRoutingClassification{
		RoutingMode: raw.RoutingMode,
		Confidence:  raw.Confidence,
		Reason:      strings.TrimSpace(raw.Reason),
	}, nil
}

func extractFirstJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func fallbackSlackRoutingClassification(text, reason string) slackRoutingClassification {
	cleaned := normalizeSlackRoutingText(text)
	mode := slackbotsvc.SlackRoutingModeAnswerOnly
	if slackTextRequestsWork(cleaned) && (!slackTextLooksQuestionLike(cleaned) || slackTextQuestionRequestsWork(cleaned)) {
		mode = slackbotsvc.SlackRoutingModeStartWork
	} else if slackTextLooksQuestionLike(cleaned) {
		mode = slackbotsvc.SlackRoutingModeAnswerOnly
	}
	return slackRoutingClassification{
		RoutingMode: mode,
		Confidence:  0,
		Reason:      reason,
	}
}

func slackTextDeterministicallyRequestsWork(text string) bool {
	cleaned := normalizeSlackRoutingText(text)
	if cleaned == "" {
		return false
	}
	startWorkPhrases := []string{
		"start this work",
		"start the work",
		"start this task",
		"start the task",
		"kick off this work",
		"kick off the work",
	}
	for _, phrase := range startWorkPhrases {
		if strings.Contains(cleaned, phrase) {
			return true
		}
	}
	requestPrefixes := []string{
		"please ",
		"can you ",
		"could you ",
		"would you ",
		"will you ",
		"can we ",
		"could we ",
		"let's ",
		"lets ",
	}
	for _, prefix := range requestPrefixes {
		if strings.HasPrefix(cleaned, prefix) && slackDirectiveRestRequestsWork(strings.TrimSpace(strings.TrimPrefix(cleaned, prefix))) {
			return true
		}
	}
	if idx := strings.Index(cleaned, " please "); idx >= 0 {
		return slackDirectiveRestRequestsWork(strings.TrimSpace(cleaned[idx+len(" please "):]))
	}
	return false
}

func slackDirectiveRestRequestsWork(rest string) bool {
	if rest == "" {
		return false
	}
	answerOnlyPrefixes := []string{
		"show me ",
		"show us ",
		"tell me ",
		"explain ",
		"describe ",
		"list ",
		"don't ",
		"do not ",
		"not ",
	}
	for _, prefix := range answerOnlyPrefixes {
		if strings.HasPrefix(rest, prefix) {
			return false
		}
	}
	workTerms := []string{
		"fix", "implement", "change", "update", "add", "remove", "create", "build",
		"wire", "refactor", "migrate", "hide", "make", "repair", "use", "display",
		"format", "wrap", "render", "style", "rename", "reword", "label",
	}
	for _, term := range workTerms {
		if rest == term || strings.HasPrefix(rest, term+" ") {
			return true
		}
	}
	return false
}

func normalizeSlackRoutingText(text string) string {
	cleaned := strings.TrimSpace(strings.ToLower(text))
	for strings.HasPrefix(cleaned, "<@") {
		end := strings.Index(cleaned, ">")
		if end < 0 {
			break
		}
		cleaned = strings.TrimSpace(cleaned[end+1:])
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

func slackTextLooksQuestionLike(text string) bool {
	if strings.Contains(text, "?") {
		return true
	}
	questionPrefixes := []string{
		"what ", "why ", "how ", "when ", "where ", "who ", "does ", "do ", "did ",
		"is ", "are ", "can ", "could ", "would ", "should ", "has ", "have ", "will ",
	}
	for _, prefix := range questionPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func slackTextRequestsWork(text string) bool {
	workTerms := []string{
		"fix", "implement", "change", "update", "add", "remove", "create", "build",
		"wire", "refactor", "migrate", "hide", "make", "repair",
	}
	for _, term := range workTerms {
		if text == term || strings.HasPrefix(text, term+" ") || strings.Contains(text, " "+term+" ") {
			return true
		}
	}
	return false
}

func slackTextQuestionRequestsWork(text string) bool {
	workTerms := []string{
		"fix", "implement", "change", "update", "add", "remove", "create", "build",
		"wire", "refactor", "migrate", "hide", "make", "repair",
	}
	requestPrefixes := []string{
		"can you ", "could you ", "would you ", "will you ", "please ", "can we ", "could we ",
	}
	for _, prefix := range requestPrefixes {
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(text, prefix))
		for _, term := range workTerms {
			if rest == term || strings.HasPrefix(rest, term+" ") {
				return true
			}
		}
	}
	return false
}

func applySlackRoutingMode(resolved slackbotsvc.SlackContextResolveResult, mode slackbotsvc.SlackRoutingMode, reason string) slackbotsvc.SlackContextResolveResult {
	if mode == "" {
		mode = slackbotsvc.SlackRoutingModeAnswerOnly
	}
	resolved.RoutingMode = mode
	resolved.RoutingReason = strings.TrimSpace(reason)
	if mode == slackbotsvc.SlackRoutingModeAnswerOnly {
		resolved.Missing = nonRepositorySlackMissingContext(resolved.Missing)
		resolved.ContextSummary.Missing = nonRepositorySlackMissingContext(resolved.ContextSummary.Missing)
	}
	return resolved
}

func nonRepositorySlackMissingContext(missing []slackbotsvc.MissingSlackContext) []slackbotsvc.MissingSlackContext {
	if len(missing) == 0 {
		return missing
	}
	filtered := make([]slackbotsvc.MissingSlackContext, 0, len(missing))
	for _, item := range missing {
		if item.Kind == "repository" {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func slackRoutingInputManifest(raw json.RawMessage, mode slackbotsvc.SlackRoutingMode, reason string) json.RawMessage {
	manifest := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			manifest = map[string]any{}
		}
	}
	slackManifest, _ := manifest["slack"].(map[string]any)
	if slackManifest == nil {
		slackManifest = map[string]any{}
	}
	slackManifest["routing_mode"] = string(mode)
	if strings.TrimSpace(reason) != "" {
		slackManifest["routing_reason"] = strings.TrimSpace(reason)
	}
	manifest["slack"] = slackManifest
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return raw
	}
	return encoded
}

func slackRoutingModeFromInputManifest(raw json.RawMessage) (slackbotsvc.SlackRoutingMode, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var manifest struct {
		Slack struct {
			RoutingMode slackbotsvc.SlackRoutingMode `json:"routing_mode"`
		} `json:"slack"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", false
	}
	switch manifest.Slack.RoutingMode {
	case slackbotsvc.SlackRoutingModeAuto, slackbotsvc.SlackRoutingModeAnswerOnly, slackbotsvc.SlackRoutingModeStartWork:
		return manifest.Slack.RoutingMode, true
	default:
		return "", false
	}
}

func shouldEnqueueSlackStartedRun(session *models.Session, resolved slackbotsvc.SlackContextResolveResult) bool {
	if session == nil {
		return false
	}
	if resolved.RoutingMode == slackbotsvc.SlackRoutingModeAnswerOnly {
		return true
	}
	if session.RepositoryID == nil {
		return false
	}
	return !blockingSlackMissingContext(resolved.Missing)
}

func blockingSlackMissingContext(missing []slackbotsvc.MissingSlackContext) bool {
	for _, item := range missing {
		switch item.Kind {
		case "repository", "preview_target", "pull_request":
			return true
		}
	}
	return false
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
		defer finalizeSessionBackedEvalArtifacts(ctx, stores, services, logger, orgID, runID)
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
		markSessionBackedEvalRunning(ctx, stores, services, logger, run)
		enqueueSlackRunUpdateIfLinked(ctx, stores, logger, orgID, runID, "running", "143 is working on this", "I will post the result back in this thread.", false)
		enqueueSessionPreviewPrewarmOnStart(ctx, stores, services, logger, run)

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
					enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, string(models.SlackNotificationSessionFailed), "143 session failed", errMsg)
					return &FatalError{Err: fmt.Errorf("session timed out waiting for concurrency slot: %w", err)}
				}
				return &RetryableError{Err: err}
			}
			enqueueSlackRunUpdateIfLinked(ctx, stores, logger, orgID, runID, "failed", "143 session failed", err.Error(), true)
			enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, string(models.SlackNotificationSessionFailed), "143 session failed", err.Error())
			return err
		}
		enqueueSlackHumanInputsIfPending(ctx, stores, logger, orgID, runID)
		enqueueSlackFinalIfLinked(ctx, stores, logger, orgID, runID)
		enqueueSlackSessionNotifications(ctx, stores, logger, orgID, runID, run.AutomationRunID, string(models.SlackNotificationSessionCompleted), "143 session completed", "The session finished successfully.")
		enqueueSlackPreviewStaleIfNeeded(ctx, stores, logger, orgID, runID)
		supersedeStaleSessionPreviewWarmRuns(ctx, stores, logger, orgID, runID)
		enqueueSessionPreviewCachePrewarm(ctx, stores, services, logger, orgID, runID, "run_agent_completed")
		enqueueSessionPreviewPostTurnClassifier(ctx, stores, services, logger, orgID, runID)
		enqueueSessionPreviewWarmBuildIfCandidate(ctx, stores, services, logger, orgID, runID, "run_agent_completed")
		return nil
	}
}

func newLegacyRunEvalCompatHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			EvalRunID string `json:"eval_run_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal legacy run_eval payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		runID, err := uuid.Parse(input.EvalRunID)
		if err != nil {
			return fmt.Errorf("parse eval run ID: %w", err)
		}
		if stores == nil || stores.EvalRuns == nil || stores.EvalTasks == nil || stores.Sessions == nil || stores.Jobs == nil {
			return fmt.Errorf("legacy run_eval compatibility requires eval, session, and job stores")
		}
		run, err := stores.EvalRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("load legacy eval run: %w", err)
		}
		if run.SessionID != nil && *run.SessionID != uuid.Nil {
			session, err := stores.Sessions.GetByID(ctx, orgID, *run.SessionID)
			if err != nil {
				return fmt.Errorf("load converted eval session: %w", err)
			}
			if !sessionStatusNeedsRunAgent(session.Status) {
				return nil
			}
			return enqueueCompatRunAgent(ctx, stores, orgID, session)
		}
		task, err := stores.EvalTasks.GetByID(ctx, orgID, run.TaskID)
		if err != nil {
			return fmt.Errorf("load legacy eval task: %w", err)
		}
		session := legacyEvalRunSessionFromTask(orgID, task, run.Model, run.ConfigRef)
		if err := stores.Sessions.Create(ctx, session); err != nil {
			return fmt.Errorf("create session for legacy eval run: %w", err)
		}
		if err := stores.EvalRuns.AttachSession(ctx, orgID, run.ID, session.ID, session.PrimaryThreadID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				logger.Info().Str("eval_run_id", run.ID.String()).Msg("legacy eval run was already converted by another worker")
				return nil
			}
			return fmt.Errorf("attach legacy eval run session: %w", err)
		}
		return enqueueCompatRunAgent(ctx, stores, orgID, *session)
	}
}

func newLegacyRunEvalBootstrapCompatHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			BootstrapRunID string `json:"bootstrap_run_id"`
			OrgID          string `json:"org_id"`
			RepoID         string `json:"repo_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal legacy run_eval_bootstrap payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		bootstrapRunID, err := uuid.Parse(input.BootstrapRunID)
		if err != nil {
			return fmt.Errorf("parse bootstrap run ID: %w", err)
		}
		if stores == nil || stores.EvalBootstraps == nil || stores.Sessions == nil || stores.Jobs == nil {
			return fmt.Errorf("legacy run_eval_bootstrap compatibility requires bootstrap, session, and job stores")
		}
		run, err := stores.EvalBootstraps.GetByID(ctx, orgID, bootstrapRunID)
		if err != nil {
			return fmt.Errorf("load legacy eval bootstrap run: %w", err)
		}
		if run.SessionID != nil && *run.SessionID != uuid.Nil {
			session, err := stores.Sessions.GetByID(ctx, orgID, *run.SessionID)
			if err != nil {
				return fmt.Errorf("load converted eval bootstrap session: %w", err)
			}
			if !sessionStatusNeedsRunAgent(session.Status) {
				return nil
			}
			return enqueueCompatRunAgent(ctx, stores, orgID, session)
		}
		repoID := run.RepoID
		if repoID == uuid.Nil && input.RepoID != "" {
			parsed, err := uuid.Parse(input.RepoID)
			if err != nil {
				return fmt.Errorf("parse repo ID: %w", err)
			}
			repoID = parsed
		}
		session := legacyEvalBootstrapSession(orgID, repoID, run.CreatedBy)
		if err := stores.Sessions.Create(ctx, session); err != nil {
			return fmt.Errorf("create session for legacy eval bootstrap: %w", err)
		}
		if err := stores.EvalBootstraps.AttachSessionThread(ctx, orgID, run.ID, session.ID, session.PrimaryThreadID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				logger.Info().Str("bootstrap_run_id", run.ID.String()).Msg("legacy eval bootstrap was already converted by another worker")
				return nil
			}
			return fmt.Errorf("attach legacy eval bootstrap session: %w", err)
		}
		return enqueueCompatRunAgent(ctx, stores, orgID, *session)
	}
}

func enqueueCompatRunAgent(ctx context.Context, stores *Stores, orgID uuid.UUID, session models.Session) error {
	dedupeKey := db.RunAgentDedupeKey(session.ID)
	if _, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "run_agent", db.RunAgentPayload(&session), 5, &dedupeKey); err != nil {
		return fmt.Errorf("enqueue converted eval session: %w", err)
	}
	return nil
}

func sessionStatusNeedsRunAgent(status models.SessionStatus) bool {
	switch status {
	case models.SessionStatusPending, models.SessionStatusRunning:
		return true
	default:
		return false
	}
}

func legacyEvalRunSessionFromTask(orgID uuid.UUID, task models.EvalTask, model string, configRef *string) *models.Session {
	title := "Eval: " + task.Name
	agentType := legacyEvalRunAgentType(model)
	baseCommitSHA := task.BaseCommitSHA
	var modelOverride *string
	if model != "codex" {
		modelOverride = &model
	}
	configLine := "Use the repository's default runtime configuration."
	if configRef != nil && strings.TrimSpace(*configRef) != "" {
		configLine = fmt.Sprintf("Use eval config ref %s. Apply that configuration before starting the task.", strings.TrimSpace(*configRef))
	}
	inputManifestMap := map[string]any{
		"eval_task_id":    task.ID.String(),
		"base_commit_sha": task.BaseCommitSHA,
		"model":           model,
	}
	if task.SolutionCommitSHA != nil && *task.SolutionCommitSHA != "" {
		inputManifestMap["solution_commit_sha"] = *task.SolutionCommitSHA
	}
	if configRef != nil && strings.TrimSpace(*configRef) != "" {
		inputManifestMap["config_ref"] = strings.TrimSpace(*configRef)
	}
	inputManifest, err := json.Marshal(inputManifestMap)
	if err != nil {
		inputManifest = json.RawMessage(`{}`)
	}
	prompt := fmt.Sprintf(`Run this coding-agent eval exactly as specified.

Repository setup:
- Start from base commit %s before changing code.
- %s
- Do not inspect or apply the known solution diff.

Task:
%s

When finished, leave the working tree with only the changes needed to solve the task.`, task.BaseCommitSHA, configLine, task.IssueDescription)
	return &models.Session{
		OrgID:            orgID,
		Origin:           models.SessionOriginEvalRun,
		InteractionMode:  models.SessionInteractionModeSingleRun,
		ValidationPolicy: models.SessionValidationPolicyOnSessionEnd,
		AgentType:        agentType,
		Status:           models.SessionStatusPending,
		AutonomyLevel:    models.DefaultSessionAutonomy,
		TokenMode:        models.SessionTokenModeHigh,
		ModelOverride:    modelOverride,
		Title:            &title,
		PMApproach:       &prompt,
		RepositoryID:     &task.RepoID,
		BaseCommitSHA:    &baseCommitSHA,
		InputManifest:    inputManifest,
	}
}

func legacyEvalBootstrapSession(orgID, repoID uuid.UUID, createdBy *uuid.UUID) *models.Session {
	title := "Bootstrap eval tasks"
	prompt := "Analyze this repository's pull request history and add high-quality candidate eval tasks with `143-tools eval add`."
	return &models.Session{
		OrgID:             orgID,
		Origin:            models.SessionOriginEvalBootstrap,
		InteractionMode:   models.SessionInteractionModeSingleRun,
		ValidationPolicy:  models.SessionValidationPolicyOnSessionEnd,
		AgentType:         models.AgentTypeCodex,
		Status:            models.SessionStatusPending,
		AutonomyLevel:     models.DefaultSessionAutonomy,
		TokenMode:         models.SessionTokenModeLow,
		TriggeredByUserID: createdBy,
		Title:             &title,
		PMApproach:        &prompt,
		RepositoryID:      &repoID,
	}
}

func legacyEvalRunAgentType(model string) models.AgentType {
	switch model {
	case "claude-opus-4-6", "claude-sonnet-4-6":
		return models.AgentTypeClaudeCode
	case models.OpenCodeModelGPT54Mini, models.OpenCodeModelClaudeHaiku45, models.OpenCodeModelDeepSeekV4Flash:
		return models.AgentTypeOpenCode
	default:
		return models.AgentTypeCodex
	}
}

func markSessionBackedEvalRunning(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session) {
	switch session.Origin {
	case models.SessionOriginEvalRun:
		if stores == nil || stores.EvalRuns == nil {
			return
		}
		run, err := stores.EvalRuns.GetBySessionID(ctx, session.OrgID, session.ID)
		if err != nil {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load eval run linked to session")
			return
		}
		if err := stores.EvalRuns.UpdateStatus(ctx, session.OrgID, run.ID, models.EvalRunStatusRunning); err != nil {
			logger.Warn().Err(err).Str("eval_run_id", run.ID.String()).Msg("failed to mark session-backed eval run running")
			return
		}
		if run.BatchID != nil {
			publishEvalBatchSignal(ctx, services, session.OrgID, *run.BatchID, models.EvalBatchStatusRunning, logger)
		}
	case models.SessionOriginEvalBootstrap:
		if stores == nil || stores.EvalBootstraps == nil {
			return
		}
		threadID, err := primaryThreadIDForSession(ctx, stores, session)
		if err != nil {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to resolve eval bootstrap session thread")
			return
		}
		run, err := stores.EvalBootstraps.GetBySessionThread(ctx, session.OrgID, session.ID, threadID)
		if err != nil {
			logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load eval bootstrap linked to session")
			return
		}
		if err := stores.EvalBootstraps.UpdateStatus(ctx, session.OrgID, run.ID, models.EvalBootstrapStatusRunning, &session.ID); err != nil {
			logger.Warn().Err(err).Str("bootstrap_run_id", run.ID.String()).Msg("failed to mark session-backed eval bootstrap running")
		}
		publishEvalBootstrapSignal(ctx, services, session.OrgID, run.ID, models.EvalBootstrapStatusRunning, &session.ID, logger)
	}
}

func finalizeSessionBackedEvalArtifacts(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.Sessions == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to reload session for eval finalization")
		return
	}
	switch session.Origin {
	case models.SessionOriginEvalRun:
		finalizeSessionBackedEvalRun(ctx, stores, services, logger, session)
	case models.SessionOriginEvalBootstrap:
		finalizeSessionBackedEvalBootstrap(ctx, stores, services, logger, session)
	}
}

func finalizeSessionBackedEvalBootstrap(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session) {
	if stores.EvalBootstraps == nil {
		return
	}
	status, errMsg, terminal := evalBootstrapStatusForSession(session)
	if !terminal {
		return
	}
	threadID, err := primaryThreadIDForSession(ctx, stores, session)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to resolve session-backed eval bootstrap thread")
		return
	}
	run, err := stores.EvalBootstraps.GetBySessionThread(ctx, session.OrgID, session.ID, threadID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load session-backed eval bootstrap")
		return
	}
	if err := stores.EvalBootstraps.UpdateResult(ctx, session.OrgID, run.ID, status, nil, errMsg); err != nil {
		logger.Warn().Err(err).Str("bootstrap_run_id", run.ID.String()).Msg("failed to finalize session-backed eval bootstrap")
		return
	}
	publishEvalBootstrapSignal(ctx, services, session.OrgID, run.ID, status, &session.ID, logger)
}

func finalizeSessionBackedEvalRun(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session) {
	if stores.EvalRuns == nil {
		return
	}
	status, errMsg, terminal := evalRunStatusForSession(session)
	if !terminal {
		return
	}
	run, err := stores.EvalRuns.GetBySessionID(ctx, session.OrgID, session.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load session-backed eval run")
		return
	}
	threadID, threadErr := primaryThreadIDForSession(ctx, stores, session)
	if threadErr != nil {
		logger.Warn().Err(threadErr).Str("session_id", session.ID.String()).Msg("failed to resolve session-backed eval run thread")
	}
	var traceThreadID *uuid.UUID
	if threadErr == nil {
		traceThreadID = &threadID
	}
	diff, diffErr := stores.Sessions.GetDiffByID(ctx, session.OrgID, session.ID)
	if diffErr != nil {
		logger.Warn().Err(diffErr).Str("session_id", session.ID.String()).Msg("failed to load session diff for eval run")
	}
	agentTrace, _ := json.Marshal(map[string]any{
		"session_id": session.ID.String(),
		"thread_id":  stringPtrUUID(traceThreadID),
		"status":     string(session.Status),
	})
	inputManifest, manifestErr := evalRunInputManifest(run, session, traceThreadID)
	if manifestErr != nil {
		logger.Warn().Err(manifestErr).Str("eval_run_id", run.ID.String()).Msg("failed to build eval run input manifest")
		inputManifest = run.InputManifest
	}
	if status == models.EvalRunStatusGrading {
		if diffErr != nil {
			errStr := fmt.Sprintf("failed to load session diff for grading: %s", diffErr)
			result := &models.EvalRunResult{
				Status:        models.EvalRunStatusFailed,
				ErrorMessage:  &errStr,
				InputManifest: inputManifest,
			}
			if err := stores.EvalRuns.UpdateResult(ctx, session.OrgID, run.ID, result); err != nil {
				logger.Warn().Err(err).Str("eval_run_id", run.ID.String()).Msg("failed to mark eval run failed after diff load error")
			}
			return
		}
		if err := stores.EvalRuns.UpdatePostSessionArtifacts(ctx, session.OrgID, run.ID, diff.Diff, agentTrace, inputManifest); err != nil {
			logger.Warn().Err(err).Str("eval_run_id", run.ID.String()).Msg("failed to persist session-backed eval run artifacts")
			return
		}
		if stores.Jobs != nil {
			dedupeKey := "run_eval_grader:" + run.ID.String()
			if _, err := stores.Jobs.Enqueue(ctx, session.OrgID, "eval", "run_eval_grader", map[string]string{
				"eval_run_id": run.ID.String(),
				"org_id":      session.OrgID.String(),
			}, 5, &dedupeKey); err != nil {
				logger.Warn().Err(err).Str("eval_run_id", run.ID.String()).Msg("failed to enqueue eval grader")
			}
		}
		publishEvalRunBatchUpdate(ctx, stores, services, logger, session.OrgID, run)
		return
	}
	result := &models.EvalRunResult{
		Status:        status,
		AgentDiff:     diff.Diff,
		AgentTrace:    agentTrace,
		ErrorMessage:  errMsg,
		InputManifest: inputManifest,
	}
	if err := stores.EvalRuns.UpdateResult(ctx, session.OrgID, run.ID, result); err != nil {
		logger.Warn().Err(err).Str("eval_run_id", run.ID.String()).Msg("failed to finalize session-backed eval run")
		return
	}
	publishEvalRunBatchUpdate(ctx, stores, services, logger, session.OrgID, run)
}

func newEvalGraderHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			EvalRunID string `json:"eval_run_id"`
			OrgID     string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal run_eval_grader payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return err
		}
		runID, err := uuid.Parse(input.EvalRunID)
		if err != nil {
			return fmt.Errorf("parse eval run ID: %w", err)
		}
		run, err := stores.EvalRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("load eval run for grading: %w", err)
		}
		task, err := stores.EvalTasks.GetByID(ctx, orgID, run.TaskID)
		if err != nil {
			return fmt.Errorf("load eval task for grading: %w", err)
		}
		var session *models.Session
		if run.SessionID != nil && stores.Sessions != nil {
			loaded, err := stores.Sessions.GetByID(ctx, orgID, *run.SessionID)
			if err != nil {
				return fmt.Errorf("load eval run session for grading: %w", err)
			}
			session = &loaded
		}
		var provider agent.SandboxProvider
		var snapshots storage.SnapshotStore
		var llm llmClient
		if services != nil {
			provider = services.SandboxProvider
			snapshots = services.Snapshots
			llm = services.LLM
		}
		result, err := gradeEvalRunArtifacts(ctx, run, task, evalGraderDeps{
			session:   session,
			provider:  provider,
			snapshots: snapshots,
			llm:       llm,
		})
		if err != nil {
			return fmt.Errorf("grade eval run artifacts: %w", err)
		}
		if err := stores.EvalRuns.UpdateResult(ctx, orgID, run.ID, result); err != nil {
			return fmt.Errorf("update eval grader result: %w", err)
		}
		publishEvalRunBatchUpdate(ctx, stores, services, logger, orgID, run)
		return nil
	}
}

type evalGraderDeps struct {
	session   *models.Session
	provider  agent.SandboxProvider
	snapshots storage.SnapshotStore
	llm       llmClient
}

func gradeEvalRunArtifacts(ctx context.Context, run models.EvalRun, task models.EvalTask, deps evalGraderDeps) (*models.EvalRunResult, error) {
	var criteria []models.ScoringCriterion
	if len(task.ScoringCriteria) > 0 {
		if err := json.Unmarshal(task.ScoringCriteria, &criteria); err != nil {
			return nil, fmt.Errorf("parse scoring criteria: %w", err)
		}
	}
	if len(criteria) == 0 {
		return nil, errors.New("scoring criteria are required")
	}
	hasDiff := run.AgentDiff != nil && strings.TrimSpace(*run.AgentDiff) != ""
	sandbox, sandboxCleanup, sandboxErr := prepareEvalGraderSandbox(ctx, deps)
	defer sandboxCleanup()
	results := make([]models.CriterionResult, 0, len(criteria))
	totalWeight := 0.0
	weightedScore := 0.0
	requiredFailed := false
	for _, criterion := range criteria {
		weight := criterion.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
		result := models.CriterionResult{
			Name:       criterion.Name,
			GraderType: criterion.GraderType,
			Score:      0,
			Pass:       false,
			Details:    "No agent diff was produced by the session.",
			Reasoning:  criterion.Notes,
		}
		if hasDiff {
			switch criterion.GraderType {
			case models.GraderTypeCodeCheck:
				result = gradeCodeCheckCriterion(ctx, sandbox, sandboxErr, deps, criterion)
			case models.GraderTypeLLMJudge:
				result = gradeLLMJudgeCriterion(ctx, deps.llm, run, task, criterion)
			default:
				result.Details = "Unsupported grader type."
			}
		}
		if criterion.Required && !result.Pass {
			requiredFailed = true
		}
		weightedScore += result.Score * weight
		results = append(results, result)
	}
	if totalWeight == 0 {
		totalWeight = float64(len(results))
	}
	finalScore := weightedScore / totalWeight
	passed := finalScore >= task.PassThreshold && !requiredFailed
	status := models.EvalRunStatusCompleted
	errMsg := (*string)(nil)
	if !passed {
		msg := "eval run did not meet pass threshold"
		errMsg = &msg
	}
	criterionResults, err := json.Marshal(results)
	if err != nil {
		return nil, fmt.Errorf("marshal criterion results: %w", err)
	}
	return &models.EvalRunResult{
		Status:           status,
		AgentDiff:        run.AgentDiff,
		AgentTrace:       run.AgentTrace,
		CriterionResults: criterionResults,
		FinalScore:       &finalScore,
		Passed:           &passed,
		ErrorMessage:     errMsg,
		InputManifest:    run.InputManifest,
	}, nil
}

func prepareEvalGraderSandbox(ctx context.Context, deps evalGraderDeps) (*agent.Sandbox, func(), error) {
	noop := func() {}
	if deps.provider == nil || deps.snapshots == nil || deps.session == nil || deps.session.SnapshotKey == nil || strings.TrimSpace(*deps.session.SnapshotKey) == "" {
		return nil, noop, errors.New("completed session snapshot is required for code_check grading")
	}
	cfg := agent.DefaultSandboxConfig()
	cfg.SessionID = deps.session.ID.String()
	cfg.OrgID = deps.session.OrgID.String()
	cfg.Purpose = "eval_grader"
	cfg.Timeout = 10 * time.Minute
	sandbox, err := agent.HydrateSandboxFromSnapshot(ctx, deps.provider, deps.snapshots, *deps.session.SnapshotKey, cfg)
	if err != nil {
		return nil, noop, fmt.Errorf("hydrate eval grader sandbox: %w", err)
	}
	cleanup := func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = deps.provider.Destroy(destroyCtx, sandbox)
	}
	return sandbox, cleanup, nil
}

func gradeCodeCheckCriterion(ctx context.Context, sandbox *agent.Sandbox, sandboxErr error, deps evalGraderDeps, criterion models.ScoringCriterion) models.CriterionResult {
	result := models.CriterionResult{Name: criterion.Name, GraderType: criterion.GraderType, Reasoning: criterion.Notes}
	if sandboxErr != nil {
		result.Details = sandboxErr.Error()
		return result
	}
	if deps.provider == nil || sandbox == nil {
		result.Details = "Sandbox provider is not configured for code_check grading."
		return result
	}
	var cfg models.CodeCheckConfig
	if len(criterion.GraderConfig) > 0 {
		if err := json.Unmarshal(criterion.GraderConfig, &cfg); err != nil {
			result.Details = "Invalid code_check grader_config: " + err.Error()
			return result
		}
	}
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.Command == "" {
		result.Details = "code_check grader_config.command is required."
		return result
	}
	execCtx := ctx
	cancel := func() {}
	if cfg.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	var stdout, stderr bytes.Buffer
	exitCode, err := deps.provider.Exec(execCtx, sandbox, cfg.Command, &stdout, &stderr)
	result.Pass = err == nil && exitCode == 0
	if result.Pass {
		result.Score = 1
	}
	result.Details = formatEvalCommandDetails(cfg.Command, exitCode, stdout.String(), stderr.String(), err)
	return result
}

func gradeLLMJudgeCriterion(ctx context.Context, llm llmClient, run models.EvalRun, task models.EvalTask, criterion models.ScoringCriterion) models.CriterionResult {
	result := models.CriterionResult{Name: criterion.Name, GraderType: criterion.GraderType, Reasoning: criterion.Notes}
	if llm == nil {
		result.Details = "LLM client is not configured for llm_judge grading."
		return result
	}
	systemPrompt := "You are grading one coding-agent eval criterion. Return compact JSON with fields pass, score, reasoning, and details."
	diff := ""
	if run.AgentDiff != nil {
		diff = *run.AgentDiff
	}
	userPrompt := fmt.Sprintf("Criterion: %s\nNotes: %s\nIssue:\n%s\nAgent diff:\n%s", criterion.Name, criterion.Notes, task.IssueDescription, diff)
	response, err := llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		result.Details = "LLM judge failed: " + err.Error()
		return result
	}
	score, pass, reasoning, details := parseEvalLLMJudgeResponse(response)
	result.Score = score
	result.Pass = pass
	result.Reasoning = reasoning
	result.Details = details
	return result
}

func parseEvalLLMJudgeResponse(response string) (float64, bool, string, string) {
	type judgeResponse struct {
		Score     *float64 `json:"score"`
		Pass      *bool    `json:"pass"`
		Reasoning string   `json:"reasoning"`
		Details   string   `json:"details"`
	}
	var parsed judgeResponse
	trimmed := strings.TrimSpace(response)
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end >= start {
			_ = json.Unmarshal([]byte(trimmed[start:end+1]), &parsed)
		}
	}
	score := 0.0
	if parsed.Score != nil {
		score = *parsed.Score
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
	}
	pass := false
	if parsed.Pass != nil {
		pass = *parsed.Pass
	} else if parsed.Score != nil {
		pass = score >= 0.5
	} else {
		pass = strings.Contains(strings.ToLower(response), "pass")
		if pass {
			score = 1
		}
	}
	if parsed.Score == nil && parsed.Pass != nil && *parsed.Pass {
		score = 1
	}
	reasoning := strings.TrimSpace(parsed.Reasoning)
	if reasoning == "" {
		reasoning = strings.TrimSpace(response)
	}
	details := strings.TrimSpace(parsed.Details)
	if details == "" {
		details = "LLM judge completed."
	}
	return score, pass, reasoning, details
}

func formatEvalCommandDetails(command string, exitCode int, stdout, stderr string, execErr error) string {
	const maxOutput = 4000
	if len(stdout) > maxOutput {
		stdout = stdout[:maxOutput] + "\n[truncated]"
	}
	if len(stderr) > maxOutput {
		stderr = stderr[:maxOutput] + "\n[truncated]"
	}
	details := fmt.Sprintf("Command: %s\nExit code: %d", command, exitCode)
	if execErr != nil {
		details += "\nError: " + execErr.Error()
	}
	if strings.TrimSpace(stdout) != "" {
		details += "\nStdout:\n" + stdout
	}
	if strings.TrimSpace(stderr) != "" {
		details += "\nStderr:\n" + stderr
	}
	return details
}

func primaryThreadIDForSession(ctx context.Context, stores *Stores, session models.Session) (uuid.UUID, error) {
	if session.PrimaryThreadID != nil && *session.PrimaryThreadID != uuid.Nil {
		return *session.PrimaryThreadID, nil
	}
	if stores == nil || stores.SessionThreads == nil {
		return uuid.Nil, fmt.Errorf("session thread store not configured")
	}
	threads, err := stores.SessionThreads.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list session threads: %w", err)
	}
	if len(threads) == 0 {
		return uuid.Nil, fmt.Errorf("session has no threads")
	}
	return threads[0].ID, nil
}

func evalBootstrapStatusForSession(session models.Session) (models.EvalBootstrapStatus, *string, bool) {
	switch session.Status {
	case models.SessionStatusCompleted, models.SessionStatusPRCreated:
		return models.EvalBootstrapStatusCompleted, nil, true
	case models.SessionStatusSkipped:
		msg := "bootstrap session was skipped before producing candidates"
		return models.EvalBootstrapStatusFailed, &msg, true
	case models.SessionStatusFailed, models.SessionStatusCancelled:
		return models.EvalBootstrapStatusFailed, sessionTerminalError(session), true
	default:
		return "", nil, false
	}
}

func evalRunStatusForSession(session models.Session) (models.EvalRunStatus, *string, bool) {
	switch session.Status {
	case models.SessionStatusCompleted, models.SessionStatusPRCreated:
		return models.EvalRunStatusGrading, nil, true
	case models.SessionStatusSkipped:
		msg := "eval run session was skipped before producing a diff"
		return models.EvalRunStatusFailed, &msg, true
	case models.SessionStatusFailed, models.SessionStatusCancelled:
		return models.EvalRunStatusFailed, sessionTerminalError(session), true
	default:
		return "", nil, false
	}
}

func evalRunInputManifest(run models.EvalRun, session models.Session, threadID *uuid.UUID) (json.RawMessage, error) {
	manifest := map[string]any{
		"session_id": session.ID.String(),
		"status":     string(session.Status),
		"model":      run.Model,
	}
	if threadID != nil && *threadID != uuid.Nil {
		manifest["thread_id"] = threadID.String()
	}
	if session.BaseCommitSHA != nil && *session.BaseCommitSHA != "" {
		manifest["base_commit_sha"] = *session.BaseCommitSHA
	}
	if run.ConfigRef != nil && *run.ConfigRef != "" {
		manifest["config_ref"] = *run.ConfigRef
	}
	return json.Marshal(manifest)
}

func publishEvalRunBatchUpdate(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID uuid.UUID, run models.EvalRun) {
	if run.BatchID == nil || stores.EvalBatches == nil {
		return
	}
	if err := stores.EvalBatches.CompleteBatchIfDone(ctx, orgID, *run.BatchID); err != nil {
		logger.Warn().Err(err).Str("batch_id", run.BatchID.String()).Msg("failed to complete eval batch after run update")
	}
	batchStatus := models.EvalBatchStatusRunning
	if batch, err := stores.EvalBatches.GetByID(ctx, orgID, *run.BatchID); err == nil {
		batchStatus = batch.Status
		if batch.Status == models.EvalBatchStatusCompleted && stores.EvalReleaseGates != nil {
			if _, err := stores.EvalReleaseGates.EvaluateBatch(ctx, orgID, *run.BatchID); err != nil {
				logger.Warn().Err(err).Str("batch_id", run.BatchID.String()).Msg("failed to evaluate eval release gates")
			}
		}
	}
	publishEvalBatchSignal(ctx, services, orgID, *run.BatchID, batchStatus, logger)
}

func sessionTerminalError(session models.Session) *string {
	if session.Error != nil && strings.TrimSpace(*session.Error) != "" {
		return session.Error
	}
	if session.FailureExplanation != nil && strings.TrimSpace(*session.FailureExplanation) != "" {
		return session.FailureExplanation
	}
	msg := "session ended without a successful result"
	return &msg
}

func stringPtrUUID(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
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
	enqueueSlackFinalIfLinkedForThread(ctx, stores, logger, orgID, sessionID, nil, 0)
}

func enqueueSlackFinalIfLinkedForThread(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID, threadID *uuid.UUID, turnNumber int) {
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
	var messages []models.SessionMessage
	if threadID != nil && *threadID != uuid.Nil {
		messages, err = stores.SessionMessages.ListByThread(ctx, orgID, *threadID)
	} else {
		messages, err = stores.SessionMessages.ListBySession(ctx, orgID, sessionID)
	}
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load messages for Slack final response")
		return
	}
	var finalMessageID int64
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == models.MessageRoleAssistant && (turnNumber <= 0 || messages[i].TurnNumber == turnNumber) {
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

func answerQueuedHumanInputForContinue(ctx context.Context, stores *Stores, orgID, sessionID, threadID uuid.UUID, hasThread bool, queuedMessageID int64, logger zerolog.Logger) (*uuid.UUID, error) {
	if stores == nil || stores.HumanInputRequests == nil || stores.SessionMessages == nil {
		return nil, nil
	}
	queued, err := stores.SessionMessages.GetByID(ctx, orgID, queuedMessageID)
	if err != nil {
		return nil, fmt.Errorf("fetch queued message for human input answer: %w", err)
	}
	if queued.SessionID != sessionID {
		return nil, fmt.Errorf("queued message %d belongs to session %s, not %s", queuedMessageID, queued.SessionID, sessionID)
	}
	if hasThread {
		if queued.ThreadID == nil || *queued.ThreadID != threadID {
			return nil, fmt.Errorf("queued message %d does not belong to thread %s", queuedMessageID, threadID)
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
			Int64("queued_message_id", queuedMessageID).
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
			AutoAttempt         bool   `json:"auto_attempt"`
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
		var queuedMessageID *int64
		if input.HumanInputRequestID != "" {
			parsedHumanInputRequestID, parseErr := uuid.Parse(input.HumanInputRequestID)
			if parseErr != nil {
				return fmt.Errorf("parse human input request ID: %w", parseErr)
			}
			humanInputRequestID = &parsedHumanInputRequestID
		}
		if input.QueuedMessageID != "" {
			parsedQueuedMessageID, parseErr := strconv.ParseInt(input.QueuedMessageID, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("parse queued message ID: %w", parseErr)
			}
			queuedMessageID = &parsedQueuedMessageID
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
					QueuedMessageID:      queuedMessageID,
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
		isPRRepairContinuation := continueOpts != nil && continueOpts.PRRepair != nil
		if humanInputRequestID == nil && queuedMessageID != nil && !isPRRepairContinuation {
			answeredID, answerErr := answerQueuedHumanInputForContinue(ctx, stores, orgID, sessionID, threadID, hasThread, *queuedMessageID, logger)
			if answerErr != nil {
				return answerErr
			}
			humanInputRequestID = answeredID
		}
		if continueOpts == nil && (humanInputRequestID != nil || queuedMessageID != nil) {
			continueOpts = &agent.ContinueSessionOptions{
				HumanInputRequestID: humanInputRequestID,
				QueuedMessageID:     queuedMessageID,
			}
		} else if continueOpts != nil {
			if humanInputRequestID != nil {
				continueOpts.HumanInputRequestID = humanInputRequestID
			}
			continueOpts.QueuedMessageID = queuedMessageID
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
				notifyPRAutoRepairAttentionIfAutomatic(ctx, stores, logger, orgID, sessionID, input.PullRequestID, input.CommandType, "pull request head changed during automatic repair", input.AutoAttempt)
				if input.AutoAttempt {
					metrics.RecordPRAutoRepairOutcome(ctx, orgID.String(), "", input.CommandType, "stale_head")
					metrics.RecordPRAutoRepairRegret(ctx, orgID.String(), "", input.CommandType, "head_changed_during_repair")
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
			notifyPRAutoRepairAttentionIfAutomatic(ctx, stores, logger, orgID, sessionID, input.PullRequestID, input.CommandType, err.Error(), input.AutoAttempt)
			if input.AutoAttempt {
				metrics.RecordPRAutoRepairOutcome(ctx, orgID.String(), "", input.CommandType, "failed")
				// Automatic repair is best-effort: the named branches above already
				// requeue the known-transient cases, so an unclassified failure here
				// is deterministic against this snapshot continuation. Dead-letter
				// instead of retrying so the attention notification and outcome metric
				// fire once rather than once per max_attempts retry.
				return &FatalError{Err: err}
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
			var completedThreadID *uuid.UUID
			if hasThread {
				threadIDLocal := threadID
				completedThreadID = &threadIDLocal
			}
			if titleErr := services.TitleService.MaybeRegenerateTitle(ctx, orgID, sessionID, completedThreadID); titleErr != nil {
				logger.Warn().Err(titleErr).Str("session_id", sessionID.String()).Msg("failed to regenerate session title")
			}
		}
		autoRepairAfterContinue := true
		if continueOpts != nil && continueOpts.PRRepair != nil && services.PR != nil {
			if completionErr := services.PR.CompletePullRequestRepairRun(ctx, orgID, continueOpts.PRRepair.PullRequestID, continueOpts.PRRepair.RepairRunID); completionErr != nil {
				logger.Warn().
					Err(completionErr).
					Str("session_id", sessionID.String()).
					Str("pull_request_id", continueOpts.PRRepair.PullRequestID.String()).
					Str("repair_run_id", continueOpts.PRRepair.RepairRunID.String()).
					Msg("failed to complete pull request repair run after continue_session")
			}
			if input.AutoAttempt {
				metrics.RecordPRAutoRepairOutcome(ctx, orgID.String(), "", string(continueOpts.PRRepair.CommandType), "completed")
			}
			if syncErr := services.PR.SyncPullRequestState(ctx, orgID, continueOpts.PRRepair.PullRequestID); syncErr != nil {
				if errors.Is(syncErr, ghservice.ErrPullRequestMergeabilityPending) {
					// The health snapshot was refreshed to the post-repair head;
					// only GitHub's mergeability flag is still settling. The
					// blocker signals the follow-through relies on are current,
					// so proceed rather than stranding the repair chain.
					logger.Debug().
						Err(syncErr).
						Str("session_id", sessionID.String()).
						Str("pull_request_id", continueOpts.PRRepair.PullRequestID.String()).
						Msg("pull request mergeability still settling after repair; continuing automatic repair follow-through on refreshed health")
				} else {
					autoRepairAfterContinue = false
					logger.Warn().
						Err(syncErr).
						Str("session_id", sessionID.String()).
						Str("pull_request_id", continueOpts.PRRepair.PullRequestID.String()).
						Msg("skipping automatic pull request repair follow-through until PR health is fresh")
				}
			}
		}
		if services.PR != nil && autoRepairAfterContinue {
			if decision, autoRepairErr := services.PR.MaybeStartAutoRepair(ctx, orgID, sessionID, "session_idle"); autoRepairErr != nil {
				logger.Warn().
					Err(autoRepairErr).
					Str("session_id", sessionID.String()).
					Msg("failed to evaluate automatic pull request repair after continue_session")
			} else if decision != nil && decision.Status == ghservice.AutoRepairDecisionStarted {
				event := logger.Info().
					Str("session_id", sessionID.String()).
					Str("auto_repair_action", string(decision.Action)).
					Str("head_sha", decision.HeadSHA)
				if decision.PullRequestID != nil {
					event = event.Str("pull_request_id", decision.PullRequestID.String())
				}
				event.Msg("started automatic pull request repair after continue_session")
			} else if decision != nil && decision.Status == ghservice.AutoRepairDecisionBudgetExhausted && input.AutoAttempt {
				// Only surface "out of budget" right after the automatic repair turn
				// that consumed it. This block runs after every continue_session, so
				// without the AutoAttempt gate every later user turn on the same head
				// would re-notify and re-count the same exhaustion.
				notifyPRAutoRepairAttention(ctx, stores, logger, orgID, sessionID, decision.PullRequestID, string(decision.Action), decision.Reason)
				metrics.RecordPRAutoRepairOutcome(ctx, orgID.String(), "", string(decision.Action), "budget_exhausted")
			}
		}
		enqueueSlackHumanInputsIfPending(ctx, stores, logger, orgID, sessionID)
		var completedThreadID *uuid.UUID
		completedThreadTurn := 0
		if hasThread {
			threadIDLocal := threadID
			completedThreadID = &threadIDLocal
			completedThreadTurn = threadTurnBefore + 1
		}
		enqueueSlackFinalIfLinkedForThread(ctx, stores, logger, orgID, sessionID, completedThreadID, completedThreadTurn)
		supersedeStaleSessionPreviewWarmRuns(ctx, stores, logger, orgID, sessionID)
		enqueueSessionPreviewCachePrewarm(ctx, stores, services, logger, orgID, sessionID, "continue_session_completed")
		enqueueSessionPreviewPostTurnClassifier(ctx, stores, services, logger, orgID, sessionID)
		enqueueSessionPreviewWarmBuildIfCandidate(ctx, stores, services, logger, orgID, sessionID, "continue_session_completed")
		return nil
	}
}

func enqueueSessionPreviewCachePrewarm(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID, sessionID uuid.UUID, reason string) {
	if stores == nil || services == nil || !services.PreviewCachePrewarmEnabled || stores.Sessions == nil || stores.Jobs == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for preview cache prewarm enqueue")
		return
	}
	if session.RepositoryID == nil || *session.RepositoryID == uuid.Nil {
		return
	}
	if session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" {
		return
	}
	if stores.Previews != nil {
		policy, policyErr := stores.Previews.GetRepositoryPreviewPolicy(ctx, orgID, *session.RepositoryID)
		if policyErr != nil {
			logger.Warn().Err(policyErr).Str("session_id", session.ID.String()).Msg("failed to load preview policy for session cache prewarm")
			return
		}
		if sessionPreviewPrewarmBlockedByUntrustedFork(session, policy) {
			recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_untrusted_fork", "untrusted_fork", "Session came from forked PR content; speculative previews are disabled.")
			return
		}
	}
	userID := uuid.Nil
	if session.TriggeredByUserID != nil {
		userID = *session.TriggeredByUserID
	}
	payload := previewsvc.PreviewCachePrewarmJobPayload{
		OrgID:             orgID,
		RepositoryID:      *session.RepositoryID,
		UserID:            userID,
		Source:            previewsvc.PreviewCachePrewarmSourceSession,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		Reason:            reason,
	}
	dedupeKey := previewsvc.PreviewCachePrewarmScopeKey(payload)
	if dedupeKey == "" {
		return
	}
	targetNodeID := models.SessionWorkerTarget(&session)
	var jobID uuid.UUID
	if targetNodeID != nil {
		jobID, err = stores.Jobs.EnqueueWithTarget(ctx, orgID, "preview", models.JobTypePreviewCachePrewarm, payload, services.PreviewCachePrewarmPriority, &dedupeKey, targetNodeID)
	} else {
		jobID, err = stores.Jobs.Enqueue(ctx, orgID, "preview", models.JobTypePreviewCachePrewarm, payload, services.PreviewCachePrewarmPriority, &dedupeKey)
	}
	if err != nil {
		logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to enqueue session preview cache prewarm")
		return
	}
	if stores.Previews != nil {
		var jobIDPtr *uuid.UUID
		if jobID != uuid.Nil {
			jobIDCopy := jobID
			jobIDPtr = &jobIDCopy
		}
		_, runErr := stores.Previews.UpsertPreviewCachePrewarmRun(ctx, &models.PreviewCachePrewarmRun{
			OrgID:             orgID,
			RepoID:            *session.RepositoryID,
			Source:            string(previewsvc.PreviewCachePrewarmSourceSession),
			SourceID:          session.ID.String(),
			CacheScopeKey:     dedupeKey,
			JobID:             jobIDPtr,
			WorkerNodeID:      stringPtrValue(targetNodeID),
			Status:            "pending",
			WorkspaceRevision: session.WorkspaceRevision,
		})
		if runErr != nil {
			logger.Warn().Err(runErr).Str("session_id", session.ID.String()).Msg("failed to upsert session preview cache prewarm run")
		}
	}
}

func enqueueSessionPreviewWarmBuildIfCandidate(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID, sessionID uuid.UUID, reason string) {
	if stores == nil || services == nil || stores.Sessions == nil || stores.Jobs == nil || stores.Previews == nil || stores.Organizations == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for preview warm enqueue")
		return
	}
	if session.RepositoryID == nil || *session.RepositoryID == uuid.Nil || session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" {
		return
	}
	policy, err := stores.Previews.GetRepositoryPreviewPolicy(ctx, orgID, *session.RepositoryID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load repository preview policy for warm enqueue")
		return
	}
	if policy.SessionPrewarmMode != models.PreviewSessionPrewarmModeSmart {
		return
	}
	if sessionPreviewPrewarmBlockedByUntrustedFork(session, policy) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_untrusted_fork", "untrusted_fork", "Session came from forked PR content; speculative previews are disabled.")
		return
	}
	decision, err := stores.Previews.GetLatestSessionPreviewPrewarmRun(ctx, orgID, sessionID, models.PreviewSpeculativeDecisionWarmCandidate)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session preview warm candidate")
		}
		return
	}
	if decision.WorkspaceRevision != session.WorkspaceRevision || decision.Status != "decided" {
		return
	}
	org, err := stores.Organizations.GetByID(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load org settings for preview warm enqueue")
		return
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil || settings.PreviewSessionPrewarmMaxActive <= 0 {
		if err != nil {
			logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to parse org settings for preview warm enqueue")
		}
		return
	}
	expireStaleSessionPreviewPrewarmRuns(ctx, stores, services, logger, orgID)
	active, err := stores.Previews.CountActiveSessionPreviewPrewarmRuns(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to count session preview warm capacity")
		return
	}
	if active >= settings.PreviewSessionPrewarmMaxActive {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, models.PreviewSessionPrewarmModeSmart, "skipped_capacity", "capacity_tight", fmt.Sprintf("Speculative preview pool is full (%d/%d).", active, settings.PreviewSessionPrewarmMaxActive))
		return
	}
	if !sessionPreviewHasSpeculativeWorkerHeadroom(ctx, stores, logger, orgID) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, models.PreviewSessionPrewarmModeSmart, "skipped_capacity", "capacity_tight", "No preview worker has at least 2 free sandbox slots.")
		return
	}
	userID := uuid.Nil
	if session.TriggeredByUserID != nil {
		userID = *session.TriggeredByUserID
	}
	payload := previewsvc.SessionPreviewWarmBuildJobPayload{
		OrgID:             orgID,
		UserID:            userID,
		SessionID:         sessionID,
		RepositoryID:      *session.RepositoryID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      decision.ConfigDigest,
		Reason:            reason,
	}
	if _, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             orgID,
		RepositoryID:      *session.RepositoryID,
		SessionID:         sessionID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      decision.ConfigDigest,
		Mode:              models.PreviewSessionPrewarmModeSmart,
		Decision:          models.PreviewSpeculativeDecisionWarmCandidate,
		Confidence:        decision.Confidence,
		Reason:            decision.Reason,
		Explanation:       decision.Explanation,
		Status:            "queued",
		CapacitySnapshot:  json.RawMessage(`{}`),
	}); err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to reserve session preview warm build")
		return
	}
	dedupeKey := fmt.Sprintf("session_preview_warm:%s:%d:%s", sessionID, session.WorkspaceRevision, decision.ConfigDigest)
	priority := -49
	if services.PreviewCachePrewarmPriority != 0 {
		priority = services.PreviewCachePrewarmPriority + 1
	}
	targetNodeID := models.SessionWorkerTarget(&session)
	if targetNodeID == nil {
		if workerNodeID, localErr := stores.Previews.FindLatestStartupCacheWorkerForRepository(ctx, orgID, *session.RepositoryID); localErr != nil {
			logger.Warn().Err(localErr).Str("session_id", sessionID.String()).Msg("failed to find cache-local worker for session preview warm build")
		} else if strings.TrimSpace(workerNodeID) != "" {
			workerNodeID = strings.TrimSpace(workerNodeID)
			targetNodeID = &workerNodeID
		}
	}
	var jobID uuid.UUID
	if targetNodeID != nil {
		jobID, err = stores.Jobs.EnqueueWithTarget(ctx, orgID, "preview", models.JobTypeSessionPreviewWarmBuild, payload, priority, &dedupeKey, targetNodeID)
	} else {
		jobID, err = stores.Jobs.Enqueue(ctx, orgID, "preview", models.JobTypeSessionPreviewWarmBuild, payload, priority, &dedupeKey)
	}
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to enqueue session preview warm build")
		if _, updateErr := stores.Previews.UpdateSessionPreviewPrewarmRunStatus(ctx, orgID, *session.RepositoryID, sessionID, session.WorkspaceRevision, models.PreviewSpeculativeDecisionWarmCandidate, decision.ConfigDigest, "failed", err.Error(), true); updateErr != nil {
			logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).Msg("failed to mark session preview warm enqueue failure")
		}
		return
	}
	jobIDPtr := &jobID
	if _, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             orgID,
		RepositoryID:      *session.RepositoryID,
		SessionID:         sessionID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      decision.ConfigDigest,
		Mode:              models.PreviewSessionPrewarmModeSmart,
		Decision:          models.PreviewSpeculativeDecisionWarmCandidate,
		Confidence:        decision.Confidence,
		Reason:            decision.Reason,
		Explanation:       decision.Explanation,
		Status:            "queued",
		JobID:             jobIDPtr,
		CapacitySnapshot:  json.RawMessage(`{}`),
	}); err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to record session preview warm job id")
	}
}

func supersedeStaleSessionPreviewWarmRuns(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || stores.Sessions == nil || stores.Previews == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for stale preview warm cleanup")
		return
	}
	rows, err := stores.Previews.SupersedeStaleSessionPreviewWarmRuns(ctx, orgID, sessionID, session.WorkspaceRevision)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to supersede stale session preview warm runs")
		return
	}
	if rows > 0 {
		metrics.RecordSessionPrewarmSpeculativeWaste(ctx, orgID.String(), "superseded")
	}
}

func enqueueSessionPreviewPrewarmOnStart(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session) {
	if stores == nil || services == nil || !services.PreviewCachePrewarmEnabled || stores.Jobs == nil || stores.Previews == nil || stores.Organizations == nil {
		return
	}
	if session.OrgID == uuid.Nil || session.ID == uuid.Nil || session.RepositoryID == nil || *session.RepositoryID == uuid.Nil {
		return
	}
	org, err := stores.Organizations.GetByID(ctx, session.OrgID)
	if err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to load org settings for session preview prewarm")
		return
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to parse org settings for session preview prewarm")
		return
	}
	if settings.PreviewSessionPrewarmMaxActive <= 0 {
		return
	}
	expireStaleSessionPreviewPrewarmRuns(ctx, stores, services, logger, session.OrgID)
	policy, err := stores.Previews.GetRepositoryPreviewPolicy(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to load repository preview policy for session prewarm")
		return
	}
	if policy.SessionPrewarmMode == models.PreviewSessionPrewarmModeOff {
		return
	}
	if sessionPreviewPrewarmBlockedByUntrustedFork(session, policy) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_untrusted_fork", "untrusted_fork", "Session came from forked PR content; speculative previews are disabled.")
		return
	}
	if !sessionPreviewPrewarmEligible(ctx, stores, logger, session) {
		return
	}
	active, countErr := stores.Previews.CountActiveSessionPreviewPrewarmRuns(ctx, session.OrgID)
	if countErr != nil {
		logger.Warn().Err(countErr).
			Str("org_id", session.OrgID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to count active session preview prewarm runs")
		return
	}
	if active >= settings.PreviewSessionPrewarmMaxActive {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_capacity", "capacity_tight", fmt.Sprintf("Speculative preview pool is full (%d/%d).", active, settings.PreviewSessionPrewarmMaxActive))
		return
	}
	if !sessionPreviewHasSpeculativeWorkerHeadroom(ctx, stores, logger, session.OrgID) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_capacity", "capacity_tight", "No preview worker has at least 2 free sandbox slots.")
		return
	}
	cooling, cooldownErr := stores.Previews.HasRecentSessionPreviewPrewarmCooldown(ctx, session.OrgID, *session.RepositoryID, time.Now().Add(-5*time.Minute))
	if cooldownErr != nil {
		logger.Warn().Err(cooldownErr).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to check session preview prewarm cooldown")
		return
	}
	if cooling {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_cooldown", "capacity_tight", "Repository is in speculative preview cooldown.")
		return
	}
	concurrentRepo, concurrentErr := stores.Previews.HasRecentRepoSessionCachePrewarm(ctx, session.OrgID, *session.RepositoryID, session.ID, time.Now().Add(-5*time.Minute))
	if concurrentErr != nil {
		logger.Warn().Err(concurrentErr).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Msg("failed to check concurrent repo session cache prewarm")
		return
	}
	if concurrentRepo {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_cooldown", "concurrent_session", "Another session for this repository recently had a cache prewarm.")
		return
	}
	switch policy.SessionPrewarmMode {
	case models.PreviewSessionPrewarmModeCache:
		enqueueSessionPreviewCachePrewarmForDecision(ctx, stores, services, logger, session, models.PreviewSessionPrewarmModeCache, models.PreviewSpeculativeDecisionCache, 1, "policy_cache", "Repository policy is cache-only.", nil)
	case models.PreviewSessionPrewarmModeSmart:
		enqueueSessionPreviewPrewarmClassifier(ctx, stores, services, logger, session, "session_start")
	default:
		return
	}
}

func enqueueSessionPreviewPostTurnClassifier(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, orgID, sessionID uuid.UUID) {
	if stores == nil || services == nil || stores.Sessions == nil || stores.Previews == nil || stores.Jobs == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load session for post-turn preview classifier")
		return
	}
	if session.RepositoryID == nil || *session.RepositoryID == uuid.Nil || session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" {
		return
	}
	policy, err := stores.Previews.GetRepositoryPreviewPolicy(ctx, orgID, *session.RepositoryID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load preview policy for post-turn classifier")
		return
	}
	if policy.SessionPrewarmMode != models.PreviewSessionPrewarmModeSmart {
		return
	}
	if sessionPreviewPrewarmBlockedByUntrustedFork(session, policy) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_untrusted_fork", "untrusted_fork", "Session came from forked PR content; speculative previews are disabled.")
		return
	}
	if session.Status.IsTerminal() {
		return
	}
	if existing, activeErr := stores.Previews.GetActivePreviewForSession(ctx, orgID, sessionID); activeErr == nil && existing != nil {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, models.PreviewSessionPrewarmModeSmart, "skipped_user_started", "user_started", "User preview already exists.")
		return
	} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
		logger.Warn().Err(activeErr).Str("session_id", sessionID.String()).Msg("failed to check active preview for post-turn classifier")
		return
	}
	if !sessionPreviewPrewarmEligible(ctx, stores, logger, session) {
		return
	}
	if !sessionPreviewHasSpeculativeWorkerHeadroom(ctx, stores, logger, orgID) {
		recordSkippedSessionPreviewPrewarm(ctx, stores, logger, session, policy.SessionPrewarmMode, "skipped_capacity", "capacity_tight", "No preview worker has at least 2 free sandbox slots.")
		return
	}
	if pending, pendingErr := stores.Previews.HasPendingSessionPreviewPrewarmJob(ctx, orgID, sessionID, session.WorkspaceRevision); pendingErr != nil {
		logger.Warn().Err(pendingErr).Str("session_id", sessionID.String()).Msg("failed to check pending session preview prewarm jobs")
		return
	} else if pending {
		return
	}
	enqueueSessionPreviewPrewarmClassifier(ctx, stores, services, logger, session, "post_turn")
}

func recordSkippedSessionPreviewPrewarm(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session, mode models.PreviewSessionPrewarmMode, status, reason, explanation string) {
	if stores == nil || stores.Previews == nil || session.RepositoryID == nil {
		return
	}
	metrics.RecordSessionPreviewPrewarmSkipped(ctx, session.OrgID.String(), strings.TrimPrefix(status, "skipped_"))
	decision := models.PreviewSpeculativeDecisionNone
	if mode == models.PreviewSessionPrewarmModeCache {
		decision = models.PreviewSpeculativeDecisionCache
	}
	if _, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             session.OrgID,
		RepositoryID:      *session.RepositoryID,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		Mode:              mode,
		Decision:          decision,
		Reason:            reason,
		Explanation:       explanation,
		Status:            status,
		CapacitySnapshot:  json.RawMessage(`{}`),
	}); err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Str("decision", string(decision)).
			Str("reason", reason).
			Msg("failed to record skipped session preview prewarm")
	}
}

func enqueueSessionPreviewCachePrewarmForDecision(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session, mode models.PreviewSessionPrewarmMode, decision models.PreviewSpeculativeDecision, confidence float64, reason, explanation string, priorJobID *uuid.UUID) {
	if stores == nil || services == nil || stores.Jobs == nil || stores.Previews == nil || session.RepositoryID == nil || *session.RepositoryID == uuid.Nil {
		return
	}
	if !sessionPreviewPrewarmEligible(ctx, stores, logger, session) {
		return
	}
	userID := uuid.Nil
	if session.TriggeredByUserID != nil {
		userID = *session.TriggeredByUserID
	}
	payload := previewsvc.PreviewCachePrewarmJobPayload{
		OrgID:             session.OrgID,
		RepositoryID:      *session.RepositoryID,
		UserID:            userID,
		Source:            previewsvc.PreviewCachePrewarmSourceSession,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		Reason:            reason,
	}
	dedupeKey := previewsvc.PreviewCachePrewarmScopeKey(payload)
	if dedupeKey == "" {
		return
	}
	if _, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             session.OrgID,
		RepositoryID:      *session.RepositoryID,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      payload.ConfigDigest,
		Mode:              mode,
		Decision:          decision,
		Confidence:        confidence,
		Reason:            reason,
		Explanation:       explanation,
		Status:            "queued",
		CapacitySnapshot:  json.RawMessage(`{}`),
	}); err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Int64("workspace_revision", session.WorkspaceRevision).
			Str("config_digest", payload.ConfigDigest).
			Str("decision", string(decision)).
			Str("reason", reason).
			Msg("failed to reserve session preview prewarm decision")
		return
	}

	targetNodeID := models.SessionWorkerTarget(&session)
	if targetNodeID == nil {
		if workerNodeID, ok := jobctx.WorkerNodeIDFromContext(ctx); ok && strings.TrimSpace(workerNodeID) != "" {
			workerNodeID = strings.TrimSpace(workerNodeID)
			targetNodeID = &workerNodeID
		}
	}
	var jobID uuid.UUID
	var err error
	if targetNodeID != nil {
		jobID, err = stores.Jobs.EnqueueWithTarget(ctx, session.OrgID, "preview", models.JobTypePreviewCachePrewarm, payload, services.PreviewCachePrewarmPriority, &dedupeKey, targetNodeID)
	} else {
		jobID, err = stores.Jobs.Enqueue(ctx, session.OrgID, "preview", models.JobTypePreviewCachePrewarm, payload, services.PreviewCachePrewarmPriority, &dedupeKey)
	}
	if err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Int64("workspace_revision", session.WorkspaceRevision).
			Str("config_digest", payload.ConfigDigest).
			Str("decision", string(decision)).
			Str("reason", reason).
			Msg("failed to enqueue session preview cache prewarm")
		if _, updateErr := stores.Previews.UpdateSessionPreviewPrewarmRunStatus(ctx, session.OrgID, *session.RepositoryID, session.ID, session.WorkspaceRevision, decision, payload.ConfigDigest, "failed", err.Error(), true); updateErr != nil {
			logger.Warn().Err(updateErr).
				Str("org_id", session.OrgID.String()).
				Str("repository_id", session.RepositoryID.String()).
				Str("session_id", session.ID.String()).
				Str("decision", string(decision)).
				Str("reason", reason).
				Msg("failed to mark session preview prewarm enqueue failure")
		}
		return
	}

	var jobIDPtr *uuid.UUID
	if jobID != uuid.Nil {
		jobIDCopy := jobID
		jobIDPtr = &jobIDCopy
	} else if priorJobID != nil && *priorJobID != uuid.Nil {
		jobIDPtr = priorJobID
	}
	if _, err := stores.Previews.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{
		OrgID:             session.OrgID,
		RepositoryID:      *session.RepositoryID,
		SessionID:         session.ID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      payload.ConfigDigest,
		Mode:              mode,
		Decision:          decision,
		Confidence:        confidence,
		Reason:            reason,
		Explanation:       explanation,
		Status:            "queued",
		JobID:             jobIDPtr,
		CapacitySnapshot:  json.RawMessage(`{}`),
	}); err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Int64("workspace_revision", session.WorkspaceRevision).
			Str("config_digest", payload.ConfigDigest).
			Str("decision", string(decision)).
			Str("reason", reason).
			Msg("failed to record session preview prewarm decision")
	}
}

func enqueueSessionPreviewPrewarmClassifier(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session, phase string) {
	if stores == nil || stores.Jobs == nil || session.RepositoryID == nil || *session.RepositoryID == uuid.Nil {
		return
	}
	if strings.TrimSpace(phase) == "" {
		phase = "session_start"
	}
	payload := previewsvc.SessionPreviewPrewarmClassifyJobPayload{
		OrgID:             session.OrgID,
		SessionID:         session.ID,
		RepositoryID:      *session.RepositoryID,
		WorkspaceRevision: session.WorkspaceRevision,
		ConfigDigest:      sessionPreviewKnownConfigDigest(ctx, stores, logger, session.OrgID, *session.RepositoryID),
		Phase:             phase,
	}
	dedupeKey := fmt.Sprintf("session_preview_prewarm_classify:%s:%d:%s:%s", session.ID, session.WorkspaceRevision, payload.ConfigDigest, phase)
	priority := -51
	if services != nil && services.PreviewCachePrewarmPriority != 0 {
		priority = services.PreviewCachePrewarmPriority - 1
	}
	if _, err := stores.Jobs.Enqueue(ctx, session.OrgID, "preview", models.JobTypeSessionPreviewPrewarmClassify, payload, priority, &dedupeKey); err != nil {
		logger.Warn().Err(err).
			Str("org_id", session.OrgID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Str("session_id", session.ID.String()).
			Int64("workspace_revision", session.WorkspaceRevision).
			Str("decision", string(models.PreviewSpeculativeDecisionNone)).
			Str("reason", "classifier_enqueue_failed").
			Msg("failed to enqueue session preview prewarm classifier")
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
			if errors.Is(err, ghservice.ErrPullRequestRepositoryDisconnected) {
				logger.Info().Str("org_id", orgID.String()).Str("pull_request_id", pullRequestID.String()).Msg("skipping pull request state sync for disconnected repository")
				return nil
			}
			return err
		}
		return nil
	}
}

func newSyncPRPreviewSurfacesHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input ghservice.SyncPRPreviewSurfacesPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal sync_pr_preview_surfaces payload: %w", err)
		}
		if input.OrgID == uuid.Nil {
			orgID, err := parseOrgID("", ctx)
			if err != nil {
				return fmt.Errorf("parse org ID: %w", err)
			}
			input.OrgID = orgID
		}
		if input.RepositoryID == uuid.Nil {
			return fmt.Errorf("repository_id is required")
		}
		logger.Info().
			Str("org_id", input.OrgID.String()).
			Str("repository_id", input.RepositoryID.String()).
			Int("pr_number", input.PRNumber).
			Str("head_sha", input.HeadSHA).
			Msg("starting sync_pr_preview_surfaces job")
		return services.PR.SyncPRPreviewSurfaces(ctx, input)
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
		if err := services.PR.ProcessMergeWhenReady(ctx, orgID, pullRequestID); err != nil {
			if errors.Is(err, ghservice.ErrPullRequestMergeWhenReadyChecksPending) {
				// The PR looks mergeable only because GitHub has not registered
				// its check runs yet. Retry on a short fixed delay (without
				// consuming the attempt budget) so we re-evaluate once checks
				// appear or the grace window elapses, rather than thrashing the
				// queue with exponential backoff or dead-lettering.
				retryAfter := 20 * time.Second
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
			}
			return err
		}
		return nil
	}
}

// open_pr handler creates a GitHub PR from a completed agent run by pushing
// the restored sandbox snapshot to GitHub. Drives the session's
// pr_creation_state through pushing -> succeeded/failed so the UI can reflect
// progress without needing to poll PR rows.
func newOpenPRHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID         string `json:"session_id"`
			OrgID             string `json:"org_id"`
			IssueSnapshotID   string `json:"issue_snapshot_id,omitempty"`
			Draft             *bool  `json:"draft,omitempty"`
			AuthorMode        string `json:"author_mode,omitempty"`
			MergeWhenReady    bool   `json:"merge_when_ready,omitempty"`
			RequestedByUserID string `json:"requested_by_user_id,omitempty"`
			RequestedRole     string `json:"requested_role,omitempty"`
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

		if input.RequestedRole == string(models.RoleBuilder) {
			if err := ensureBuilderReadinessFresh(ctx, stores, run); err != nil {
				if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateFailed, "PR readiness blockers must pass before creating a PR."); stateErr != nil {
					logger.Error().Err(stateErr).Msg("failed to mark PR creation blocked by readiness")
				}
				return err
			}
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

		pr, createErr := services.PR.CreatePR(ctx, &run, params...)
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

		if input.MergeWhenReady {
			if pr == nil {
				if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateFailed, "Could not enable auto-merge for this pull request."); stateErr != nil {
					logger.Error().Err(stateErr).Msg("failed to mark PR creation as failed")
				}
				return fmt.Errorf("open_pr merge_when_ready requested but PRService returned nil pull request")
			}
			requestedByUserID, err := uuid.Parse(input.RequestedByUserID)
			if err != nil {
				queueErr := fmt.Errorf("parse merge_when_ready requesting user id: %w", err)
				if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateFailed, "Could not enable auto-merge for this pull request."); stateErr != nil {
					logger.Error().Err(stateErr).Msg("failed to mark PR creation as failed")
				}
				return queueErr
			}
			if _, err := services.PR.QueueMergeWhenReady(ctx, orgID, pr.ID, requestedByUserID); err != nil {
				if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateFailed, "Could not enable auto-merge for this pull request."); stateErr != nil {
					logger.Error().Err(stateErr).Msg("failed to mark PR creation as failed")
				}
				return fmt.Errorf("queue merge when ready after open_pr: %w", err)
			}
		}
		if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateSucceeded, ""); stateErr != nil {
			logger.Error().Err(stateErr).Msg("failed to mark PR creation as succeeded")
		}
		if services.PagerDutyWrites != nil && pr != nil {
			if err := services.PagerDutyWrites.OnPROpened(ctx, run, *pr); err != nil {
				logger.Warn().
					Err(err).
					Str("session_id", runID.String()).
					Msg("failed to write PagerDuty PR-open note")
			}
		}
		enqueueSlackNotificationSubscribers(ctx, stores, logger, orgID, slackNotificationFanoutInput{
			EventKind:      string(models.SlackNotificationPROpened),
			Title:          "Pull request opened",
			Body:           "A pull request was opened for the session.",
			SessionID:      &runID,
			PullRequestID:  &pr.ID,
			PullRequestURL: pr.GitHubPRURL,
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

func ensureBuilderReadinessFresh(ctx context.Context, stores *Stores, run models.Session) error {
	if stores == nil || stores.PRReadiness == nil {
		return fmt.Errorf("PR readiness policy is not configured")
	}
	resolved, err := stores.PRReadiness.ResolvePolicy(ctx, run.OrgID, run.RepositoryID)
	if err != nil {
		return fmt.Errorf("resolve PR readiness policy: %w", err)
	}
	if !resolved.Config.RequiresRoleReadiness(models.RoleBuilder) {
		return nil
	}
	readinessRun, err := stores.PRReadiness.GetLatestBySession(ctx, run.OrgID, run.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("PR readiness is required before builder PR creation")
		}
		return fmt.Errorf("load PR readiness for builder PR creation: %w", err)
	}
	if readinessRun.Status == models.PRReadinessRunStatusQueued || readinessRun.Status == models.PRReadinessRunStatusRunning {
		return fmt.Errorf("PR readiness is still running")
	}
	if readinessRun.EvaluatedWorkspaceRevision != run.WorkspaceGeneration || stringValue(readinessRun.EvaluatedSnapshotKey) != stringValue(run.SnapshotKey) {
		return fmt.Errorf("PR readiness is stale for current workspace revision")
	}
	if readinessRun.Status == models.PRReadinessRunStatusFailed {
		return fmt.Errorf("PR readiness is blocked")
	}
	if blockers := readinessRun.UnbypassedBlockingCheckKeys(models.RoleBuilder); len(blockers) > 0 {
		return fmt.Errorf("PR readiness blocking check failed: %s", strings.Join(blockers, ", "))
	}
	return nil
}

func newRunPRReadinessHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID       string `json:"org_id"`
			SessionID   string `json:"session_id"`
			ReadinessID string `json:"readiness_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal run_pr_readiness payload: %w", err)
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session ID: %w", err)
		}
		readinessID, err := uuid.Parse(input.ReadinessID)
		if err != nil {
			return fmt.Errorf("parse readiness ID: %w", err)
		}
		if stores == nil || stores.PRReadiness == nil || stores.Sessions == nil {
			return fmt.Errorf("PR readiness stores are not configured")
		}
		registerPRReadinessDeadLetter(ctx, stores, logger, orgID, readinessID)

		run, err := stores.PRReadiness.GetRunByID(ctx, orgID, readinessID)
		if err != nil {
			return fmt.Errorf("load PR readiness run: %w", err)
		}
		// Skip runs that already reached a terminal state (e.g. a dead-letter
		// MarkFailed or a prior completion). MarkRunning is guarded the same way,
		// so treat its ErrNoRows as "already terminal" rather than a job failure.
		if run.Status != models.PRReadinessRunStatusQueued && run.Status != models.PRReadinessRunStatusRunning {
			logger.Info().
				Str("session_id", sessionID.String()).
				Str("readiness_id", readinessID.String()).
				Str("status", string(run.Status)).
				Msg("PR readiness run already terminal; skipping")
			return nil
		}
		if run.Status != models.PRReadinessRunStatusRunning {
			if err := stores.PRReadiness.MarkRunning(ctx, orgID, readinessID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				return err
			}
		}
		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("load readiness session: %w", err)
		}
		if loop, err := runningReadinessReviewLoop(ctx, stores, session); err != nil {
			return err
		} else if loop != nil {
			if time.Since(run.CreatedAt) > prePRReviewMaxWait {
				return failReadinessReviewWaitTimeout(ctx, stores, logger, orgID, readinessID, sessionID)
			}
			return retryPRReadinessReviewLoop(logger, sessionID, readinessID)
		}
		if err := ensureSessionSnapshotQuiescent(ctx, stores, session); err != nil {
			return err
		}

		latestLoop, reviewReady, err := ensureReadinessReviewLoop(ctx, stores, services, session, stringValue(run.EvaluatedSnapshotKey))
		if err != nil {
			return err
		}
		if !reviewReady {
			if time.Since(run.CreatedAt) > prePRReviewMaxWait {
				return failReadinessReviewWaitTimeout(ctx, stores, logger, orgID, readinessID, sessionID)
			}
			return retryPRReadinessReviewLoop(logger, sessionID, readinessID)
		}

		logs := []models.SessionLog{}
		if stores.SessionLogs != nil {
			if loaded, err := stores.SessionLogs.ListByRunID(ctx, orgID, sessionID); err == nil {
				logs = loaded
			} else {
				return fmt.Errorf("load readiness logs: %w", err)
			}
		}
		changedFiles := []string{}
		if stores.ThreadFileEvents != nil {
			events, err := stores.ThreadFileEvents.ListBySession(ctx, orgID, sessionID, nil)
			if err != nil {
				return fmt.Errorf("load readiness changed files: %w", err)
			}
			seen := map[string]struct{}{}
			for _, event := range events {
				if _, ok := seen[event.Path]; ok {
					continue
				}
				seen[event.Path] = struct{}{}
				changedFiles = append(changedFiles, event.Path)
			}
		}
		linkedIssueCount := 0
		if stores.SessionIssueLinks != nil {
			links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, sessionID)
			if err != nil {
				return fmt.Errorf("load readiness issue links: %w", err)
			}
			linkedIssueCount = len(links)
		}
		policyConfig := models.DefaultPRReadinessPolicyConfig()
		if stores.PRReadiness != nil {
			resolved, err := stores.PRReadiness.ResolvePolicy(ctx, orgID, session.RepositoryID)
			if err != nil {
				return fmt.Errorf("resolve PR readiness policy: %w", err)
			}
			policyConfig = resolved.Config
		}
		issueLessReason := ""
		if stores.PRReadiness != nil {
			contextValue, err := stores.PRReadiness.GetContext(ctx, orgID, sessionID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("load PR readiness context: %w", err)
			}
			if err == nil {
				issueLessReason = contextValue.IssueLessReason
			}
		}
		customChecks := []models.PRReadinessCheck{}
		if stores.PRReadiness != nil {
			definedChecks, err := stores.PRReadiness.ListCustomChecks(ctx, orgID, session.RepositoryID)
			if err != nil {
				return fmt.Errorf("load PR readiness custom checks: %w", err)
			}
			customChecks = evaluateCustomReadinessChecks(ctx, services, definedChecks, session, changedFiles, logs)
		}

		result, err := readinesssvc.NewEvaluator(policyConfig.EffectivePolicy()).Evaluate(ctx, readinesssvc.EvaluationInput{
			Session:                    session,
			EvaluatedWorkspaceRevision: run.EvaluatedWorkspaceRevision,
			EvaluatedSnapshotKey:       stringValue(run.EvaluatedSnapshotKey),
			LatestReviewLoop:           latestLoop,
			Logs:                       logs,
			ChangedFiles:               changedFiles,
			LinkedIssueCount:           linkedIssueCount,
			IssueLessReason:            issueLessReason,
			PolicyConfig:               policyConfig,
			CustomChecks:               customChecks,
		})
		if err != nil {
			return fmt.Errorf("evaluate PR readiness: %w", err)
		}
		completed := models.PRReadinessRun{
			Status:       result.Status,
			Summary:      result.Summary,
			ReviewPacket: result.ReviewPacket,
		}
		for i := range result.Checks {
			result.Checks[i].OrgID = orgID
			result.Checks[i].RunID = readinessID
			result.Checks[i].SessionID = sessionID
		}
		if err := stores.PRReadiness.CompleteRunWithChecks(ctx, orgID, readinessID, completed, result.Checks); err != nil {
			return err
		}
		if run.TriggeredByUserID == nil && (result.Status == models.PRReadinessRunStatusBlocked || result.Status == models.PRReadinessRunStatusFailed) {
			notifyPRAutoReadinessAttention(ctx, stores, logger, orgID, sessionID, result.Status, result.Summary)
			metrics.RecordPRAutoRepairOutcome(ctx, orgID.String(), "", "readiness", string(result.Status))
		}
		return nil
	}
}

type customReadinessLLMResponse struct {
	Status  models.PRReadinessCheckStatus `json:"status"`
	Summary string                        `json:"summary"`
	Details map[string]any                `json:"details"`
	Action  string                        `json:"action"`
}

func evaluateCustomReadinessChecks(ctx context.Context, services *Services, checks []models.PRReadinessCustomCheck, session models.Session, changedFiles []string, logs []models.SessionLog) []models.PRReadinessCheck {
	results := make([]models.PRReadinessCheck, 0, len(checks))
	for _, check := range checks {
		if !customReadinessCheckEnabled(check) {
			continue
		}
		if !customReadinessCheckMatches(check, changedFiles) {
			results = append(results, customReadinessCheckResult(check, models.PRReadinessCheckStatusSkipped, "Custom check skipped", "No changed files matched this custom check's path filters.", nil, ""))
			continue
		}
		if services == nil || services.LLM == nil {
			results = append(results, customReadinessCheckError(check, fmt.Errorf("LLM client is not configured")))
			continue
		}
		userPrompt, err := renderCustomReadinessUserPrompt(check, session, changedFiles, logs)
		if err != nil {
			results = append(results, customReadinessCheckError(check, err))
			continue
		}
		raw, err := services.LLM.Complete(ctx, prompts.PRReadinessCustomCheckPrompt(), userPrompt)
		if err != nil {
			results = append(results, customReadinessCheckError(check, err))
			continue
		}
		parsed, err := parseCustomReadinessLLMResponse(raw)
		if err != nil {
			results = append(results, customReadinessCheckError(check, err))
			continue
		}
		summary := strings.TrimSpace(parsed.Summary)
		if summary == "" {
			summary = "Custom check completed."
		}
		results = append(results, customReadinessCheckResult(check, parsed.Status, check.Name, summary, parsed.Details, parsed.Action))
	}
	return results
}

func customReadinessCheckEnabled(check models.PRReadinessCustomCheck) bool {
	if check.Enforcement == (models.PRReadinessEnforcementByRole{}) {
		return true
	}
	return check.Enforcement.EnforcementFor(models.RoleBuilder) != models.PRReadinessEnforcementOff ||
		check.Enforcement.EnforcementFor(models.RoleMember) != models.PRReadinessEnforcementOff ||
		check.Enforcement.EnforcementFor(models.RoleAdmin) != models.PRReadinessEnforcementOff
}

func customReadinessCheckResult(check models.PRReadinessCustomCheck, status models.PRReadinessCheckStatus, title, summary string, details map[string]any, action string) models.PRReadinessCheck {
	rawDetails, _ := json.Marshal(details)
	if len(details) == 0 {
		rawDetails = nil
	}
	enforcement := check.Enforcement
	if enforcement == (models.PRReadinessEnforcementByRole{}) {
		enforcement = models.PRReadinessEnforcementByRole{
			Builder:  models.PRReadinessEnforcementAdvisory,
			Engineer: models.PRReadinessEnforcementAdvisory,
			Admin:    models.PRReadinessEnforcementAdvisory,
		}
	}
	return models.PRReadinessCheck{
		CheckKey:             check.CheckKey,
		CheckType:            models.PRReadinessCheckTypeCustomPrompt,
		Status:               status,
		Enforcement:          enforcement.Builder,
		EnforcementByRole:    enforcement,
		EnforcementBuilder:   enforcement.Builder,
		EnforcementEngineer:  enforcement.Engineer,
		EnforcementAdmin:     enforcement.Admin,
		EffectiveEnforcement: enforcement.Builder,
		Provenance:           models.PRReadinessProvenance(check.Source),
		Source:               string(check.Source),
		Title:                title,
		Summary:              summary,
		Details:              rawDetails,
		Action:               action,
	}
}

func customReadinessCheckError(check models.PRReadinessCustomCheck, err error) models.PRReadinessCheck {
	return customReadinessCheckResult(check, models.PRReadinessCheckStatusError, check.Name, "Custom readiness check failed to execute.", map[string]any{"error": err.Error()}, "Review check configuration")
}

func customReadinessCheckMatches(check models.PRReadinessCustomCheck, changedFiles []string) bool {
	if len(check.PathFilters.Include) == 0 && len(check.PathFilters.Exclude) == 0 {
		return true
	}
	for _, file := range changedFiles {
		included := len(check.PathFilters.Include) == 0
		for _, pattern := range check.PathFilters.Include {
			if readinesssvc.MatchPathPattern(pattern, file) {
				included = true
				break
			}
		}
		if !included {
			continue
		}
		excluded := false
		for _, pattern := range check.PathFilters.Exclude {
			if readinesssvc.MatchPathPattern(pattern, file) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

func renderCustomReadinessUserPrompt(check models.PRReadinessCustomCheck, session models.Session, changedFiles []string, logs []models.SessionLog) (string, error) {
	data := map[string]any{
		"CheckName":         check.Name,
		"ChangedFiles":      limitStrings(changedFiles, 100),
		"DiffStats":         string(session.DiffStats),
		"WorkspaceRevision": session.WorkspaceGeneration,
		"Logs":              boundedReadinessLogs(logs, 20, 4000),
	}
	tmpl, err := template.New("custom_readiness_prompt").Parse(check.Prompt)
	if err != nil {
		return "", fmt.Errorf("parse custom readiness prompt template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("render custom readiness prompt template: %w", err)
	}
	payload := map[string]any{
		"check_prompt":       rendered.String(),
		"changed_files":      data["ChangedFiles"],
		"diff_stats":         data["DiffStats"],
		"workspace_revision": data["WorkspaceRevision"],
		"logs":               data["Logs"],
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal custom readiness prompt context: %w", err)
	}
	return string(payloadBytes), nil
}

func parseCustomReadinessLLMResponse(raw string) (customReadinessLLMResponse, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	var parsed customReadinessLLMResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &parsed); err != nil {
		return customReadinessLLMResponse{}, fmt.Errorf("parse custom readiness check response: %w", err)
	}
	switch parsed.Status {
	case models.PRReadinessCheckStatusPassed, models.PRReadinessCheckStatusWarning, models.PRReadinessCheckStatusFailed:
		return parsed, nil
	default:
		return customReadinessLLMResponse{}, fmt.Errorf("invalid custom readiness status %q", parsed.Status)
	}
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func boundedReadinessLogs(logs []models.SessionLog, maxLogs, maxBytes int) []string {
	if len(logs) > maxLogs {
		logs = logs[len(logs)-maxLogs:]
	}
	// Walk newest-first so the most recent (most relevant) logs win the byte
	// budget, and skip — rather than stop at — an oversized line so one early
	// giant entry can't drop every newer log. Re-reverse to chronological order.
	out := make([]string, 0, len(logs))
	used := 0
	for i := len(logs) - 1; i >= 0; i-- {
		line := logs[i].Timestamp.UTC().Format(time.RFC3339) + " " + logs[i].Message
		if used+len(line) > maxBytes {
			continue
		}
		used += len(line)
		out = append(out, line)
	}
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

func registerPRReadinessDeadLetter(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, readinessID uuid.UUID) {
	if stores == nil || stores.PRReadiness == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()
		summary := "Readiness job failed before checks completed."
		if deadLetterErr != nil {
			summary = "Readiness job failed before checks completed: " + deadLetterErr.Error()
		}
		if err := stores.PRReadiness.MarkFailed(writeCtx, orgID, readinessID, summary); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			logger.Error().
				Err(err).
				Str("readiness_id", readinessID.String()).
				Msg("failed to mark PR readiness run failed after job dead-letter")
		}
	})
}

func runningReadinessReviewLoop(ctx context.Context, stores *Stores, session models.Session) (*models.SessionReviewLoop, error) {
	if stores == nil || stores.ReviewLoops == nil {
		return nil, nil
	}
	loop, err := stores.ReviewLoops.GetRunningLoopBySession(ctx, session.OrgID, session.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load running readiness review loop: %w", err)
	}
	return &loop, nil
}

func retryPRReadinessReviewLoop(logger zerolog.Logger, sessionID, readinessID uuid.UUID) error {
	logger.Info().
		Str("session_id", sessionID.String()).
		Str("readiness_id", readinessID.String()).
		Msg("PR readiness waiting for review loop")
	delay := prePRReviewRetryDelay
	return &RetryableError{
		Err:                    fmt.Errorf("PR readiness review loop is still running"),
		RetryAfter:             &delay,
		BypassMaxRetryDuration: true,
	}
}

// failReadinessReviewWaitTimeout terminates a readiness run that has waited past
// prePRReviewMaxWait for the agent review loop. It marks the run failed and
// returns nil so the job completes instead of requeuing forever.
func failReadinessReviewWaitTimeout(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID, readinessID, sessionID uuid.UUID) error {
	summary := fmt.Sprintf("Readiness timed out after %s waiting for the agent review loop to finish.", prePRReviewMaxWait)
	if err := stores.PRReadiness.MarkFailed(ctx, orgID, readinessID, summary); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("mark readiness failed after review wait timeout: %w", err)
	}
	logger.Warn().
		Str("session_id", sessionID.String()).
		Str("readiness_id", readinessID.String()).
		Dur("max_wait", prePRReviewMaxWait).
		Msg("PR readiness review wait timed out; marking run failed")
	return nil
}

func ensureReadinessReviewLoop(ctx context.Context, stores *Stores, services *Services, session models.Session, snapshotKey string) (*models.SessionReviewLoop, bool, error) {
	if stores == nil || stores.ReviewLoops == nil {
		return nil, true, nil
	}
	loops, err := stores.ReviewLoops.ListLoopsBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		return nil, false, fmt.Errorf("list readiness review loops: %w", err)
	}
	var latest *models.SessionReviewLoop
	for i := range loops {
		loop := loops[i]
		if latest == nil {
			latest = &loop
		}
		if loop.Status == models.ReviewLoopStatusClean && stringValue(loop.LatestCheckpointKey) == snapshotKey {
			return &loop, true, nil
		}
		if loop.Status == models.ReviewLoopStatusRunning {
			return &loop, false, nil
		}
		if stringValue(loop.LatestCheckpointKey) == snapshotKey || stringValue(loop.LoopStartCheckpointKey) == snapshotKey {
			return &loop, true, nil
		}
	}
	if services == nil || services.ReviewLoops == nil || snapshotKey == "" {
		return latest, true, nil
	}
	started, err := services.ReviewLoops.Start(ctx, session.OrgID, session.ID, reviewloopsvc.StartReviewLoopRequest{
		AgentType:       session.AgentType,
		Model:           stringValue(session.ModelOverride),
		MaxPasses:       1,
		Source:          models.ReviewLoopSourceManual,
		StartedByUserID: session.TriggeredByUserID,
		ReviewRequired:  true,
	})
	if err != nil {
		return nil, false, fmt.Errorf("start readiness review loop: %w", err)
	}
	return started, false, nil
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
	case errors.Is(err, ghservice.ErrPushBranchDiverged):
		return ghservice.PushBranchDivergedPRMessage
	case errors.Is(err, ghservice.ErrSandboxAuthUnavailable):
		return ghservice.SandboxAuthUnavailablePRMessage
	default:
		return "Check GitHub access or repo permissions and try again."
	}
}

func prPushErrorCode(err error) models.PRPushErrorCode {
	switch {
	case errors.Is(err, ghservice.ErrPushBranchDiverged):
		return models.PRPushErrorCodeBranchDiverged
	case errors.Is(err, ghservice.ErrPushRejected):
		return models.PRPushErrorCodePushRejected
	case errors.Is(err, ghservice.ErrSandboxAuthUnavailable):
		return models.PRPushErrorCodeSandboxAuthUnavailable
	default:
		return models.PRPushErrorCodeGeneric
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
			if stateErr := stores.Sessions.UpdatePRPushStateWithCode(ctx, orgID, runID, models.PRPushStateFailed, msg, prPushErrorCode(pushErr)); stateErr != nil {
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
	case errors.Is(err, ghservice.ErrPushBranchDiverged):
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

// backfill_preview_groups handler links existing preview_targets rows that have
// no preview_group_id to their correct preview_groups row. The job is safe to
// run multiple times; targets that are already linked are skipped.
func newBackfillPreviewGroupsHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var p struct {
			BatchSize int `json:"batch_size"`
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &p); err != nil {
				return &FatalError{Err: fmt.Errorf("unmarshal backfill_preview_groups payload: %w", err)}
			}
		}
		orgID, ok := jobOrgIDFromContext(ctx)
		if !ok {
			return &FatalError{Err: fmt.Errorf("backfill_preview_groups: missing org_id in job context")}
		}
		total, err := stores.Previews.BackfillPreviewGroups(ctx, orgID, p.BatchSize)
		if err != nil {
			return fmt.Errorf("backfill preview groups: %w", err)
		}
		logger.Info().
			Str("org_id", orgID.String()).
			Int("targets_linked", total).
			Msg("backfill_preview_groups completed")
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

		if stores.SlackInboundEvents != nil && stores.Organizations != nil && retentionCfg.SlackInboundPayloadDays > 0 {
			batchSize := retentionCfg.SlackInboundPayloadBatch
			if batchSize <= 0 {
				batchSize = 1000
			}
			cutoff := time.Now().AddDate(0, 0, -retentionCfg.SlackInboundPayloadDays)
			orgIDs, err := stores.Organizations.ListIDs(ctx)
			if err != nil {
				logger.Error().Err(err).Msg("failed to list organizations for Slack inbound payload cleanup")
				errs = append(errs, fmt.Errorf("list organizations for slack payload cleanup: %w", err))
			} else {
				var redactedTotal int64
			outerOrgLoop:
				for _, orgID := range orgIDs {
					if ctx.Err() != nil {
						break
					}
					for {
						redacted, redactErr := stores.SlackInboundEvents.RedactPayloadsOlderThan(ctx, orgID, cutoff, batchSize)
						if redactErr != nil {
							logger.Error().Err(redactErr).Str("org_id", orgID.String()).Msg("failed to redact expired Slack inbound payloads")
							errs = append(errs, fmt.Errorf("redact expired slack payloads for org %s: %w", orgID, redactErr))
							break
						}
						redactedTotal += redacted
						if redacted < int64(batchSize) {
							break
						}
						if ctx.Err() != nil {
							break outerOrgLoop
						}
					}
				}
				totalDeleted += redactedTotal
				logger.Info().Int64("redacted", redactedTotal).Int("retention_days", retentionCfg.SlackInboundPayloadDays).Msg("Slack inbound payload cleanup complete")
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

type syncGitHubOrgRosterPayload struct {
	OrgID          string `json:"org_id"`
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
}

func newSyncGitHubOrgRosterHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var p syncGitHubOrgRosterPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("unmarshal sync_github_org_roster payload: %w", err)
		}
		orgID, err := uuid.Parse(p.OrgID)
		if err != nil {
			return fmt.Errorf("parse sync_github_org_roster org_id: %w", err)
		}
		if p.InstallationID <= 0 {
			return fmt.Errorf("sync_github_org_roster installation_id is required")
		}
		if strings.TrimSpace(p.AccountLogin) == "" {
			links, err := stores.GitHubInstallations.ListEnabledAutoJoinLinksByInstallation(ctx, p.InstallationID)
			if err != nil {
				return &RetryableError{Err: fmt.Errorf("load github org auto-join link: %w", err)}
			}
			if len(links) == 0 {
				logger.Info().Int64("installation_id", p.InstallationID).Msg("github org roster sync skipped; auto-join no longer enabled")
				return nil
			}
			p.AccountLogin = links[0].AccountLogin
			orgID = links[0].OrgID
		}
		members, err := services.GitHubOrgRoster.ListOrgMembers(ctx, p.InstallationID, p.AccountLogin)
		if err != nil {
			if httpStatus(err) == http.StatusForbidden {
				return disableGitHubOrgAutoJoinAfterPermissionLoss(ctx, stores, logger, orgID, p.InstallationID, p.AccountLogin)
			}
			return &RetryableError{Err: fmt.Errorf("list github org members: %w", err)}
		}
		rows := make([]models.GitHubOrgMember, 0, len(members))
		for _, member := range members {
			rows = append(rows, models.GitHubOrgMember{
				InstallationID: p.InstallationID,
				GitHubUserID:   member.ID,
				GitHubLogin:    member.Login,
			})
		}
		if err := stores.GitHubInstallations.ReplaceRosterForInstallation(ctx, p.InstallationID, rows); err != nil {
			return &RetryableError{Err: fmt.Errorf("replace github org roster: %w", err)}
		}
		logger.Info().
			Str("job_type", jobType).
			Str("org_id", orgID.String()).
			Int64("installation_id", p.InstallationID).
			Int("member_count", len(rows)).
			Msg("github org roster synced")
		return nil
	}
}

type httpStatusError interface {
	HTTPStatus() int
}

func httpStatus(err error) int {
	var statusErr httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.HTTPStatus()
	}
	return 0
}

func disableGitHubOrgAutoJoinAfterPermissionLoss(ctx context.Context, stores *Stores, logger zerolog.Logger, orgID uuid.UUID, installationID int64, accountLogin string) error {
	link, err := stores.GitHubInstallations.DisableOrgLinkAutoJoin(ctx, orgID, installationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Link already disabled (e.g. a previous attempt succeeded up to this
			// point). Still clear the roster — if the previous attempt failed after
			// the disable but before the clear, this retry must finish the job.
			if clearErr := stores.GitHubInstallations.ClearRosterForInstallation(ctx, installationID); clearErr != nil {
				return &RetryableError{Err: fmt.Errorf("clear github org roster after permission loss (retry): %w", clearErr)}
			}
			return nil
		}
		return &RetryableError{Err: fmt.Errorf("disable github org auto-join: %w", err)}
	}
	if err := stores.GitHubInstallations.ClearRosterForInstallation(ctx, installationID); err != nil {
		return &RetryableError{Err: fmt.Errorf("clear github org roster after permission loss: %w", err)}
	}
	if accountLogin == "" {
		accountLogin = link.AccountLogin
	}
	if stores.AuditLogs != nil {
		audit := db.NewAuditEmitter(stores.AuditLogs, logger)
		resourceID := fmt.Sprintf("%d", installationID)
		details, _ := json.Marshal(map[string]any{
			"account_login":   accountLogin,
			"installation_id": installationID,
			"reason":          "members_permission_revoked",
		})
		audit.EmitSystemAction(ctx, db.SystemActionParams{
			OrgID:        orgID,
			ActorID:      "github_org_auto_join",
			Action:       models.AuditActionTeamGitHubOrgAutoJoinDisabled,
			ResourceType: models.AuditResourceIntegration,
			ResourceID:   &resourceID,
			Details:      details,
		})
	}
	logger.Warn().
		Str("org_id", orgID.String()).
		Int64("installation_id", installationID).
		Str("account_login", accountLogin).
		Msg("disabled github org auto-join after GitHub returned 403 for roster sync")
	return nil
}
