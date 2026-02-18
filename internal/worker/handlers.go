package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
)

// RegisterHandlers registers all job handlers on the worker.
func RegisterHandlers(w *Worker, stores *Stores, logger zerolog.Logger) {
	w.Register("ingest_webhook", newIngestWebhookHandler(stores, logger))
	w.Register("prioritize", newPrioritizeHandler(stores, logger))
}

// Stores holds all the database stores needed by job handlers.
type Stores struct {
	Issues    *db.IssueStore
	AgentRuns *db.AgentRunStore
	Jobs      *db.JobStore
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
func newPrioritizeHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			IssueID string `json:"issue_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal prioritize payload: %w", err)
		}

		issueID, err := uuid.Parse(input.IssueID)
		if err != nil {
			return fmt.Errorf("parse issue ID: %w", err)
		}

		logger.Info().
			Str("issue_id", issueID.String()).
			Msg("computing priority score")

		// In a full implementation, this would:
		// 1. Fetch the issue
		// 2. Compute customer_impact, severity, recency, revenue_risk sub-scores
		// 3. Compute direction_alignment via LLM
		// 4. Aggregate into composite score
		// 5. Upsert into priority_scores table
		// 6. Check eligibility and auto-trigger agent run if applicable

		// For now, log that prioritization was requested.
		return nil
	}
}
