package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/feedback"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/validation"
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

		return services.Orchestrator.RunAgent(ctx, &run)
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
