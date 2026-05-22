package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// NormalizedIssue is the intermediate form before upserting into the issues table.
type NormalizedIssue struct {
	ExternalID            string
	Source                models.IssueSource
	SourceIntegrationID   uuid.UUID
	Title                 string
	Description           string
	Severity              string
	OccurrenceCount       int
	AffectedCustomerCount int
	Tags                  []string
	FirstSeenAt           time.Time
	LastSeenAt            time.Time
	RawData               json.RawMessage
}

// Service handles the normalization and persistence of ingested issues.
type Service struct {
	issueStore   *db.IssueStore
	webhookStore *db.WebhookDeliveryStore
	jobStore     *db.JobStore
	logger       zerolog.Logger
}

func NewService(
	issueStore *db.IssueStore,
	webhookStore *db.WebhookDeliveryStore,
	jobStore *db.JobStore,
	logger zerolog.Logger,
) *Service {
	return &Service{
		issueStore:   issueStore,
		webhookStore: webhookStore,
		jobStore:     jobStore,
		logger:       logger,
	}
}

// IngestNormalized normalizes and upserts an issue, then enqueues a prioritize job.
func (s *Service) IngestNormalized(ctx context.Context, orgID uuid.UUID, ni NormalizedIssue) (*models.Issue, error) {
	// Compute fingerprint for dedup
	fingerprint := computeFingerprint(string(ni.Source), ni.ExternalID)

	// Normalize severity
	severity := normalizeSeverity(ni.Severity)

	issue := &models.Issue{
		OrgID:                 orgID,
		ExternalID:            ni.ExternalID,
		Source:                ni.Source,
		SourceIntegrationID:   &ni.SourceIntegrationID,
		Title:                 cleanText(ni.Title, 500),
		Description:           strPtr(cleanText(ni.Description, 5000)),
		RawData:               ni.RawData,
		Status:                models.IssueStatusOpen,
		FirstSeenAt:           ni.FirstSeenAt,
		LastSeenAt:            ni.LastSeenAt,
		OccurrenceCount:       ni.OccurrenceCount,
		AffectedCustomerCount: ni.AffectedCustomerCount,
		Severity:              models.IssueSeverity(severity),
		Tags:                  ni.Tags,
		Fingerprint:           fingerprint,
	}

	if err := s.issueStore.Upsert(ctx, issue); err != nil {
		return nil, fmt.Errorf("upsert issue: %w", err)
	}

	// Enqueue prioritize job
	dedupeKey := fmt.Sprintf("prioritize:%s", issue.ID.String())
	if _, err := s.jobStore.Enqueue(ctx, orgID, "default", "prioritize", map[string]string{
		"issue_id": issue.ID.String(),
		"org_id":   orgID.String(),
	}, 3, &dedupeKey); err != nil {
		s.logger.Warn().Err(err).Str("issue_id", issue.ID.String()).Msg("failed to enqueue prioritize job")
	}

	s.logger.Info().
		Str("issue_id", issue.ID.String()).
		Str("source", string(ni.Source)).
		Str("external_id", ni.ExternalID).
		Msg("issue ingested")

	return issue, nil
}

func computeFingerprint(source, externalID string) string {
	return models.IssueFingerprint(models.IssueSource(source), externalID)
}

func normalizeSeverity(raw string) string {
	switch strings.ToLower(raw) {
	case "fatal", "critical", "urgent", "0":
		return "critical"
	case "error", "high", "1":
		return "high"
	case "warning", "medium", "normal", "2":
		return "medium"
	case "info", "low", "3", "4":
		return "low"
	default:
		return "medium"
	}
}

func cleanText(s string, maxLen int) string {
	// Strip HTML tags (simple approach)
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
