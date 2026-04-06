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
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/feedback"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/validation"
	"github.com/assembledhq/143/internal/version"
)

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
	if hasServiceHandlersDependencies(services) {
		w.Register("run_agent", newRunAgentHandler(stores, services, logger))
		w.Register("continue_session", newContinueSessionHandler(stores, services, logger))
		w.Register("validate", newValidateHandler(stores, services, logger))
		w.Register("open_pr", newOpenPRHandler(stores, services, logger))
		w.Register("analyze_failure", newAnalyzeFailureHandler(stores, services, logger))
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
		services.Validation != nil &&
		services.PR != nil &&
		services.Failure != nil &&
		services.SandboxProvider != nil
}

// Stores holds all the database stores needed by job handlers.
type Stores struct {
	Issues              *db.IssueStore
	Sessions           *db.SessionStore
	Jobs                *db.JobStore
	Integrations        *db.IntegrationStore
	Webhooks            *db.WebhookDeliveryStore
	PriorityScores      *db.PriorityScoreStore
	ComplexityEstimates *db.ComplexityEstimateStore
	Projects            *db.ProjectStore      // nil-safe: projects feature disabled if nil
	ProjectTasks        *db.ProjectTaskStore  // nil-safe
	Credentials         *db.OrgCredentialStore // nil-safe: needed for sync_slack
	AuditLogs           *db.AuditLogStore     // nil-safe: audit retention cleanup
	Organizations       *db.OrganizationStore // nil-safe: needed for audit retention
	SessionLogs         *db.SessionLogStore   // nil-safe: data retention cleanup
	EvalTasks           *db.EvalTaskStore      // nil-safe: eval feature
	EvalRuns            *db.EvalRunStore       // nil-safe: eval feature
	EvalBatches         *db.EvalBatchStore     // nil-safe: eval feature
	EvalBootstraps      *db.EvalBootstrapStore // nil-safe: eval bootstrap feature
	Repositories        *db.RepositoryStore    // nil-safe: needed for eval repo lookup
}

// MemoryReinforcer retrieves and reinforces memories for a repo.
type MemoryReinforcer interface {
	GetContextMemories(ctx context.Context, req agent.MemoryContextRequest) (*agent.MemoryContextResult, error)
	ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error
}

// Services holds the service dependencies needed by job handlers.
type Services struct {
	Orchestrator    *agent.Orchestrator
	Validation      *validation.Service
	PR              *ghservice.PRService
	Failure         *agent.FailureService
	SandboxProvider agent.SandboxProvider
	Prioritization  *prioritization.Service
	Feedback        *feedback.Service
	PM              pmService
	Memory          MemoryReinforcer // optional — enables memory reinforcement on PR approval
	SlackSummarizer *ingestion.SlackSummarizer // nil-safe: Slack summarization disabled if nil
	LLM             llmClient        // nil-safe: needed for eval LLM judge grading
	GitHub          agent.GitHubTokenProvider // nil-safe: needed for eval repo cloning
}

// llmClient is the interface for LLM completion calls used by eval graders.
type llmClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type pmService interface {
	Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*pm.Plan, error)
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
			OrgID  string `json:"org_id"`
			Trigger string `json:"trigger"`
			RepoID string `json:"repo_id,omitempty"`
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

		logger.Info().Str("org_id", orgID.String()).Str("trigger", string(trigger)).Msg("running pm analyze")
		_, err = services.PM.Analyze(ctx, orgID, trigger, repoID)
		return err
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
			OrgID      string `json:"org_id"`
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

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting run_agent job")

		if err := services.Orchestrator.RunAgent(ctx, &run); err != nil {
			if errors.Is(err, agent.ErrConcurrencyLimit) {
				return &RetryableError{Err: err}
			}
			return err
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

		logger.Info().
			Str("session_id", sessionID.String()).
			Str("org_id", orgID.String()).
			Int("current_turn", session.CurrentTurn).
			Msg("starting continue_session job")

		return services.Orchestrator.ContinueSession(ctx, &session)
	}
}

// validate handler runs validation checks on a completed agent run.
func newValidateHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID string `json:"session_id"`
			OrgID      string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal validate payload: %w", err)
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
			Msg("starting validate job")

		// Create a sandbox for CI checks.
		sandbox, err := services.SandboxProvider.Create(ctx, agent.DefaultSandboxConfig())
		if err != nil {
			return fmt.Errorf("create sandbox for validation: %w", err)
		}
		defer func() {
			if destroyErr := services.SandboxProvider.Destroy(ctx, sandbox); destroyErr != nil {
				logger.Error().Err(destroyErr).Msg("failed to destroy validation sandbox")
			}
		}()

		issue, err := stores.Issues.GetByID(ctx, orgID, run.IssueID)
		if err != nil {
			return fmt.Errorf("fetch issue for validation: %w", err)
		}

		return services.Validation.Validate(ctx, &run, &issue, sandbox)
	}
}

// open_pr handler creates a GitHub PR from a completed agent run.
func newOpenPRHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID string `json:"session_id"`
			OrgID      string `json:"org_id"`
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

		logger.Info().
			Str("session_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting open_pr job")

		_, err = services.PR.CreatePR(ctx, &run)
		return err
	}
}

// analyze_failure handler classifies and persists failure analysis for a failed agent run.
func newAnalyzeFailureHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			SessionID string `json:"session_id"`
			OrgID      string `json:"org_id"`
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

		// Mark as running
		if err := stores.EvalBootstraps.UpdateStatus(ctx, orgID, bootstrapRunID, models.EvalBootstrapStatusRunning, nil); err != nil {
			return fmt.Errorf("update bootstrap status to running: %w", err)
		}

		candidates, scanErr := executeBootstrapScan(ctx, stores, services, orgID, repoID, logger)

		if scanErr != nil {
			errMsg := scanErr.Error()
			if updateErr := stores.EvalBootstraps.UpdateResult(ctx, orgID, bootstrapRunID,
				models.EvalBootstrapStatusFailed, nil, &errMsg); updateErr != nil {
				logger.Warn().Err(updateErr).Msg("failed to update bootstrap run with error")
			}
			return fmt.Errorf("bootstrap scan failed: %w", scanErr)
		}

		candidatesJSON, _ := json.Marshal(candidates)
		if err := stores.EvalBootstraps.UpdateResult(ctx, orgID, bootstrapRunID,
			models.EvalBootstrapStatusCompleted, candidatesJSON, nil); err != nil {
			return fmt.Errorf("update bootstrap result: %w", err)
		}

		logger.Info().
			Int("candidates", len(candidates)).
			Msg("eval bootstrap scan completed")

		return nil
	}
}

// executeBootstrapScan runs the PR history scan using an agent in a sandbox.
func executeBootstrapScan(ctx context.Context, stores *Stores, services *Services, orgID, repoID uuid.UUID, logger zerolog.Logger) ([]models.EvalBootstrapCandidate, error) {
	if stores.Repositories == nil {
		return nil, fmt.Errorf("repository store not configured")
	}
	repo, err := stores.Repositories.GetByID(ctx, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("fetch repository: %w", err)
	}

	if services.GitHub == nil {
		return nil, fmt.Errorf("github token provider not configured")
	}
	ghToken, err := services.GitHub.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}

	// Create sandbox with longer timeout for bootstrap
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
	if err := services.SandboxProvider.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, ghToken); err != nil {
		return nil, fmt.Errorf("clone repo: %w", err)
	}

	// Run the bootstrap agent using Claude Code CLI
	bootstrapPrompt := prompts.EvalBootstrapPrompt(prompts.EvalBootstrapPromptData{
		RepoFullName: repo.FullName,
	})

	var stdout, stderr bytes.Buffer
	cmd := fmt.Sprintf("claude --print %q 2>&1", bootstrapPrompt)
	exitCode, execErr := services.SandboxProvider.Exec(ctx, sb, cmd, &stdout, &stderr)
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

