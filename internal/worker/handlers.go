package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// RegisterHandlers registers all job handlers on the worker.
func RegisterHandlers(w *Worker, stores *Stores, services *Services, logger zerolog.Logger) {
	w.Register("ingest_webhook", newIngestWebhookHandler(stores, logger))
	w.Register("sync_sentry", newSyncSentryHandler(stores, logger))
	if services != nil && services.Prioritization != nil {
		w.Register("prioritize", newPrioritizeHandler(stores, services, logger))
	}
	if services != nil && services.PM != nil {
		w.Register("pm_analyze", newPMAnalyzeHandler(stores, services, logger))
	}
	if hasServiceHandlersDependencies(services) {
		w.Register("run_agent", newRunAgentHandler(stores, services, logger))
		w.Register("validate", newValidateHandler(stores, services, logger))
		w.Register("open_pr", newOpenPRHandler(stores, services, logger))
		w.Register("analyze_failure", newAnalyzeFailureHandler(stores, services, logger))
	}
	if services != nil && services.Feedback != nil {
		w.Register("process_review_comment", newProcessReviewCommentHandler(services, logger))
		w.Register("update_review_patterns", newUpdateReviewPatternsHandler(services, logger))
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
	AgentRuns           *db.AgentRunStore
	Jobs                *db.JobStore
	Integrations        *db.IntegrationStore
	Webhooks            *db.WebhookDeliveryStore
	PriorityScores      *db.PriorityScoreStore
	ComplexityEstimates *db.ComplexityEstimateStore
	Projects            *db.ProjectStore      // nil-safe: projects feature disabled if nil
	ProjectTasks        *db.ProjectTaskStore  // nil-safe
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
}

type pmService interface {
	Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*pm.Plan, error)
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

// run_agent handler executes an agent run end-to-end via the orchestrator.
func newRunAgentHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			AgentRunID string `json:"agent_run_id"`
			OrgID      string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal run_agent payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.AgentRunID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.AgentRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}

		logger.Info().
			Str("agent_run_id", runID.String()).
			Str("org_id", orgID.String()).
			Msg("starting run_agent job")

		return services.Orchestrator.RunAgent(ctx, &run)
	}
}

// validate handler runs validation checks on a completed agent run.
func newValidateHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			AgentRunID string `json:"agent_run_id"`
			OrgID      string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal validate payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.AgentRunID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.AgentRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}

		logger.Info().
			Str("agent_run_id", runID.String()).
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
			AgentRunID string `json:"agent_run_id"`
			OrgID      string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal open_pr payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.AgentRunID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.AgentRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}

		logger.Info().
			Str("agent_run_id", runID.String()).
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
			AgentRunID string `json:"agent_run_id"`
			OrgID      string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal analyze_failure payload: %w", err)
		}

		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return fmt.Errorf("parse org ID: %w", err)
		}
		runID, err := uuid.Parse(input.AgentRunID)
		if err != nil {
			return fmt.Errorf("parse agent run ID: %w", err)
		}

		run, err := stores.AgentRuns.GetByID(ctx, orgID, runID)
		if err != nil {
			return fmt.Errorf("fetch agent run: %w", err)
		}

		logger.Info().
			Str("agent_run_id", runID.String()).
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

		shouldUpdatePatterns := false
		if input.Repo != "" {
			// Only update learned patterns when this job is processing a pending comment.
			// This prevents duplicate occurrence increments on retries/redeliveries.
			currentComment, err := services.Feedback.GetProcessedComment(ctx, commentID, orgID)
			if err != nil {
				return fmt.Errorf("get review comment before processing: %w", err)
			}
			shouldUpdatePatterns = currentComment.FilterStatus == "pending"
		}

		if err := services.Feedback.ProcessComment(ctx, commentID, orgID); err != nil {
			return fmt.Errorf("process review comment: %w", err)
		}

		// After processing, check if the comment is generalizable and update patterns.
		// The repo is passed from the webhook handler.
		if shouldUpdatePatterns {
			comment, err := services.Feedback.GetProcessedComment(ctx, commentID, orgID)
			if err == nil && comment.Generalizable && comment.GeneralizedRule != nil {
				category := "nit"
				if comment.Category != nil {
					category = *comment.Category
				}
				if err := services.Feedback.UpdatePatterns(ctx, orgID, commentID, input.Repo, *comment.GeneralizedRule, category); err != nil {
					logger.Warn().Err(err).Str("comment_id", commentID.String()).Msg("failed to update review patterns")
				}
			}
		}

		return nil
	}
}

// update_review_patterns handler updates patterns for a classified comment.
func newUpdateReviewPatternsHandler(services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			CommentID string `json:"comment_id"`
			OrgID     string `json:"org_id"`
			Repo      string `json:"repo"`
			Rule      string `json:"rule"`
			Category  string `json:"category"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal update_review_patterns payload: %w", err)
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
			Msg("updating review patterns")

		return services.Feedback.UpdatePatterns(ctx, orgID, commentID, input.Repo, input.Rule, input.Category)
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
