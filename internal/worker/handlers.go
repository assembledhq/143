package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"github.com/assembledhq/143/internal/version"
)

const sandboxCapacityRetryDelay = 10 * time.Second

func registerSandboxCapacityDeadLetter(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, session models.Session, threadID *uuid.UUID, jobType string) {
	if stores == nil || stores.Sessions == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()

		errMsg := "Session stopped because sandbox capacity stayed full until the retry window expired."
		explanation := "The worker could not acquire local sandbox capacity before the job retry window expired. This can happen during deploys or when other sessions are holding all sandbox slots."
		nextSteps := []string{
			"Retry the session when sandbox capacity is available",
			"Cancel sessions that are no longer needed to free up capacity",
		}
		failedSession := session
		failureCategory := agent.FailureCategorySandboxCapacity
		failedSession.Status = string(models.SessionStatusFailed)
		failedSession.Error = &errMsg
		failedSession.FailureExplanation = &explanation
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
		if err := stores.Sessions.UpdateResult(writeCtx, session.OrgID, session.ID, string(models.SessionStatusFailed), result); err != nil {
			logger.Error().
				Err(err).
				Str("session_id", session.ID.String()).
				Str("job_type", jobType).
				Msg("failed to mark session failed after sandbox capacity dead-letter")
			return
		}
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
			if err := services.ProjectTasks.OnSessionComplete(writeCtx, &failedSession, string(models.SessionStatusFailed)); err != nil {
				logger.Warn().
					Err(err).
					Str("session_id", failedSession.ID.String()).
					Str("job_type", jobType).
					Msg("failed to update project task after sandbox capacity dead-letter")
			}
		}
		if services != nil && services.AutomationRuns != nil && failedSession.AutomationRunID != nil {
			if err := services.AutomationRuns.OnSessionComplete(writeCtx, &failedSession, string(models.SessionStatusFailed)); err != nil {
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
		w.Register(models.JobTypeStartPreview, newStartPreviewHandler(services, logger))
	}
	if hasServiceHandlersDependencies(services) {
		w.Register("run_agent", newRunAgentHandler(stores, services, logger))
		w.Register("continue_session", newContinueSessionHandler(stores, services, logger))
		w.Register("cancel_session", newCancelSessionHandler(stores, services, logger))
		w.Register("open_pr", newOpenPRHandler(stores, services, logger))
		w.Register("push_pr_changes", newPushPRChangesHandler(stores, services, logger))
		w.Register("sync_pull_request_state", newSyncPullRequestStateHandler(services, logger))
		w.Register("reconcile_pull_request_state", newReconcilePullRequestStateHandler(services, logger))
		w.Register("enrich_pull_request_health", newEnrichPullRequestHealthHandler(services, logger))
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
	Webhooks            *db.WebhookDeliveryStore
	PriorityScores      *db.PriorityScoreStore
	ComplexityEstimates *db.ComplexityEstimateStore
	Projects            *db.ProjectStore                // nil-safe: projects feature disabled if nil
	ProjectTasks        *db.ProjectTaskStore            // nil-safe
	Credentials         *db.OrgCredentialStore          // nil-safe: needed for sync_slack
	AuditLogs           *db.AuditLogStore               // nil-safe: audit retention cleanup
	Organizations       *db.OrganizationStore           // nil-safe: needed for audit retention
	SessionLogs         *db.SessionLogStore             // nil-safe: data retention cleanup
	EvalTasks           *db.EvalTaskStore               // nil-safe: eval feature
	EvalRuns            *db.EvalRunStore                // nil-safe: eval feature
	EvalBatches         *db.EvalBatchStore              // nil-safe: eval feature
	EvalBootstraps      *db.EvalBootstrapStore          // nil-safe: eval bootstrap feature
	Repositories        *db.RepositoryStore             // nil-safe: needed for eval repo lookup
	SessionMessages     *db.SessionMessageStore         // nil-safe: needed for title regeneration
	SessionThreads      *db.SessionThreadStore          // nil-safe: needed for thread-scoped continuation status
	ThreadFileEvents    *db.SessionThreadFileEventStore // nil-safe: tab-level file write attribution
	IssueSnapshots      *db.SessionTurnIssueSnapshotStore
	Automations         *db.AutomationStore       // nil-safe: automations feature disabled if nil
	AutomationRuns      *db.AutomationRunStore    // nil-safe: automations feature disabled if nil
	SessionIssueLinks   *db.SessionIssueLinkStore // nil-safe: needed for Linear milestones
}

// MemoryReinforcer retrieves and reinforces memories for a repo.
type MemoryReinforcer interface {
	GetContextMemories(ctx context.Context, req agent.MemoryContextRequest) (*agent.MemoryContextResult, error)
	ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error
}

type prCreator interface {
	CreatePR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	PushChangesToPR(ctx context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*models.PullRequest, error)
	SyncPullRequestState(ctx context.Context, orgID, pullRequestID uuid.UUID) error
	ReconcilePullRequestState(ctx context.Context, orgID uuid.UUID, limit int) error
	EnrichPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64) error
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
}

type previewStarter interface {
	StartReservedPreview(ctx context.Context, payload previewsvc.StartPreviewJobPayload) error
}

type orchestratorService interface {
	RunAgent(ctx context.Context, run *models.Session) error
	ContinueSession(ctx context.Context, session *models.Session, opts *agent.ContinueSessionOptions) error
	RecoverSession(ctx context.Context, session *models.Session) error
	CancelSessionByID(sessionID uuid.UUID) bool
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

func newStartPreviewHandler(services *Services, logger zerolog.Logger) JobHandler {
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
			return &FatalError{Err: err}
		}
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
			log.Info().Str("status", run.Status).Msg("skipping automation_run: row no longer pending")
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
			AutonomyLevel:     string(models.DefaultSessionAutonomy),
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

		integrations, err := stores.Integrations.ListByOrgAndProvider(ctx, orgID, "sentry")
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

		integrations, err := stores.Integrations.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderSlack))
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
			SessionID string `json:"session_id"`
			OrgID     string `json:"org_id"`
			ThreadID  string `json:"thread_id"`
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
			_ = stores.Sessions.UpdateResult(ctx, orgID, runID, "failed", &models.SessionResult{Error: &errMsg})
			return &FatalError{Err: fmt.Errorf("linear pre-start preparation failed")}
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
				registerSandboxCapacityDeadLetter(ctx, stores, services, logger, run, run.PrimaryThreadID, "run_agent")
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("local sandbox capacity reached; retrying run_agent")
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
			}
			if errors.Is(err, agent.ErrRecoveryAttemptsExhausted) {
				logger.Warn().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent recovery exhausted; dead-lettering without another restart")
				return &FatalError{Err: err}
			}
			if errors.Is(err, agent.ErrStaleSandboxIDCleared) {
				// The orchestrator detected a stale orphan container_id from
				// a crashed prior worker, CAS-cleared it, and signaled retry.
				// Requeue without consuming an attempt — the next attempt
				// sees a clean row and creates a fresh sandbox. A short
				// backoff lets any in-flight cleanup settle.
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", runID.String()).
					Err(err).
					Msg("run_agent cleared stale orphan container_id; retrying against the clean row")
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
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
					failErr := stores.Sessions.UpdateResult(ctx, orgID, runID, "failed", &models.SessionResult{
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
					return &FatalError{Err: fmt.Errorf("session timed out waiting for concurrency slot: %w", err)}
				}
				return &RetryableError{Err: err}
			}
			return err
		}
		return nil
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

// continue_session handler continues a multi-turn session with a follow-up message.
func newContinueSessionHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID string `json:"session_id"`
			OrgID     string `json:"org_id"`
			ThreadID  string `json:"thread_id"`
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
			Int("current_turn", session.CurrentTurn).
			Dur("session_timeout", sessionTimeout).
			Dur("runtime_ceiling", runtimeCeiling).
			Msg("starting continue_session job")

		var threadID uuid.UUID
		var threadTurnBefore int
		var hasThread bool
		var continueOpts *agent.ContinueSessionOptions
		var resultAgentSessionID string
		// Captured by the OnTurnComplete callback so the post-success block
		// below can persist per-thread result metadata (diff, summary,
		// confidence) onto the thread row. Stays nil when the orchestrator
		// short-circuited before completing a turn (cancel, policy stop) so
		// we fall back to the status-only completion path.
		var lastTurnResult *agent.AgentResult
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
				continueOpts = &agent.ContinueSessionOptions{
					AgentType:            thread.AgentType,
					ModelOverride:        thread.ModelOverride,
					ThreadAgentSessionID: thread.AgentSessionID,
					ResultAgentSessionID: &resultAgentSessionID,
					ThreadID:             &threadIDLocal,
					OnTurnComplete: func(result *agent.AgentResult) {
						lastTurnResult = result
						if result == nil {
							return
						}
						emitThreadAttribution(ctx, stores, orgID, sessionID, threadIDLocal, threadTurnBefore+1, result.Diff, result.TokenUsage.TotalCostUSD, logger)
					},
				}
			}
		}

		if err := services.Orchestrator.ContinueSession(jobCtx, &session, continueOpts); err != nil {
			if errors.Is(err, agent.ErrSandboxCapacity) {
				retryAfter := sandboxCapacityRetryDelay
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
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
			}
			// A pending post-PR snapshot upload is a transient state — wrap
			// in RetryableError so the job is requeued without consuming an
			// attempt. The session row is unchanged at this point.
			if errors.Is(err, agent.ErrSnapshotPending) {
				return &RetryableError{Err: err}
			}
			if errors.Is(err, agent.ErrStaleSandboxIDCleared) {
				// Stale orphan container_id cleared; retry against the clean
				// row. See newRunAgentHandler for full rationale.
				retryAfter := 2 * time.Second
				logger.Info().
					Str("session_id", sessionID.String()).
					Err(err).
					Msg("continue_session cleared stale orphan container_id; retrying against the clean row")
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
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
				return &RetryableError{Err: err, RetryAfter: &retryAfter}
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
			if hasThread {
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
			// so persist diff/summary/confidence onto the thread row via
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
					ConfidenceScore: &lastTurnResult.ConfidenceScore,
					ResultSummary:   summaryPtr,
					Diff:            diffPtr,
				}
				if err := stores.SessionThreads.UpdateTurnComplete(ctx, orgID, threadID, threadTurnBefore+1, threadResult, resultAgentSessionID); err != nil {
					logger.Warn().Err(err).
						Str("session_id", sessionID.String()).
						Str("thread_id", threadID.String()).
						Msg("failed to persist session thread turn result")
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
		return services.PR.SyncPullRequestState(ctx, orgID, pullRequestID)
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

		if stores.SessionIssueLinks != nil {
			links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, runID)
			if err != nil {
				return fmt.Errorf("hydrate linked issues for open_pr: %w", err)
			}
			run.LinkedIssues = links
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
			// "no changes to push" is a terminal-but-non-failure outcome:
			// the session ran to completion but produced nothing worth
			// shipping. Tell the Linear linker so the attachment subtitle
			// stops saying "Running" forever and the audit log records the
			// terminal state. Other PR creation errors are not fired as
			// `failed` here because failRun in the orchestrator is the
			// canonical entry point for those.
			if errors.Is(createErr, ghservice.ErrNoChanges) {
				linear.EnqueueMilestone(ctx, stores.Jobs, logger, orgID, runID, "ended_no_pr", 0)
			}
			if shouldDeadLetterPRError(createErr) {
				return &FatalError{Err: createErr}
			}
			return createErr
		}

		if stateErr := stores.Sessions.UpdatePRCreationState(ctx, orgID, runID, models.PRCreationStateSucceeded, ""); stateErr != nil {
			logger.Error().Err(stateErr).Msg("failed to mark PR creation as succeeded")
		}
		return nil
	}
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
		Level:      level,
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
			Status:        "running",
			AutonomyLevel: string(models.SessionAutonomyFull),
			TokenMode:     "low",
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
				_ = stores.Sessions.UpdateStatus(ctx, orgID, session.ID, "failed")
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
			_ = stores.Sessions.UpdateStatus(ctx, orgID, session.ID, "completed")
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
			OrgID       string           `json:"org_id"`
			SessionID   string           `json:"session_id"`
			Identifiers []string         `json:"identifiers"`
			Refs        []linear.LinkRef `json:"refs,omitempty"`
			UserID      string           `json:"user_id,omitempty"`
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
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, _ error) {
			if err := svc.MarkLinearPrepareFailed(hookCtx, orgID, sessionID); err != nil {
				logger.Warn().Err(err).
					Str("session_id", sessionID.String()).
					Msg("prepare_linear_primary dead-letter hook failed to mark prepare state failed")
			}
		})
		if err := svc.PrepareLinearPrimaryRefs(ctx, orgID, sessionID, refs, userID); err != nil {
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
