package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anyArgs returns a slice of N pgxmock.AnyArg() matchers for use with WithArgs.
func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestIngestNormalized_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	issueStore := db.NewIssueStore(mock)
	webhookStore := db.NewWebhookDeliveryStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := NewService(issueStore, webhookStore, jobStore, zerolog.Nop())

	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	integrationID := uuid.New()

	ni := NormalizedIssue{
		ExternalID:            "EXT-123",
		Source:                "sentry",
		SourceIntegrationID:   integrationID,
		Title:                 "Test Issue",
		Description:           "A test issue description",
		Severity:              "error",
		OccurrenceCount:       5,
		AffectedCustomerCount: 2,
		Tags:                  []string{"backend", "api"},
		FirstSeenAt:           now.Add(-1 * time.Hour),
		LastSeenAt:            now,
		RawData:               json.RawMessage(`{"key":"value"}`),
	}

	// Upsert uses QueryRow with 16 named args
	upsertRows := pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
		AddRow(issueID, now, now)
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(anyArgs(16)...).
		WillReturnRows(upsertRows)

	// Enqueue uses QueryRow with 6 named args
	jobID := uuid.New()
	enqueueRows := pgxmock.NewRows([]string{"id"}).
		AddRow(jobID)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnRows(enqueueRows)

	issue, err := svc.IngestNormalized(context.Background(), orgID, ni)

	require.NoError(t, err)
	assert.NotNil(t, issue)
	assert.Equal(t, issueID, issue.ID)
	assert.Equal(t, orgID, issue.OrgID)
	assert.Equal(t, "sentry", issue.Source)
	assert.Equal(t, "EXT-123", issue.ExternalID)
	assert.Equal(t, "Test Issue", issue.Title)
	assert.Equal(t, "high", issue.Severity) // "error" normalizes to "high"
	assert.Equal(t, "open", issue.Status)
	assert.Equal(t, 5, issue.OccurrenceCount)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestNormalized_UpsertFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	issueStore := db.NewIssueStore(mock)
	webhookStore := db.NewWebhookDeliveryStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := NewService(issueStore, webhookStore, jobStore, zerolog.Nop())

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	ni := NormalizedIssue{
		ExternalID:          "EXT-456",
		Source:              "pagerduty",
		SourceIntegrationID: integrationID,
		Title:               "Failing Issue",
		Severity:            "critical",
		FirstSeenAt:         now,
		LastSeenAt:          now,
	}

	// Upsert fails - 16 named args
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(anyArgs(16)...).
		WillReturnError(fmt.Errorf("database connection lost"))

	issue, err := svc.IngestNormalized(context.Background(), orgID, ni)

	assert.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "upsert issue")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestNormalized_EnqueueIgnored(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	issueStore := db.NewIssueStore(mock)
	webhookStore := db.NewWebhookDeliveryStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := NewService(issueStore, webhookStore, jobStore, zerolog.Nop())

	orgID := uuid.New()
	issueID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	ni := NormalizedIssue{
		ExternalID:          "EXT-789",
		Source:              "github",
		SourceIntegrationID: integrationID,
		Title:               "Another Issue",
		Severity:            "warning",
		FirstSeenAt:         now,
		LastSeenAt:          now,
	}

	// Upsert succeeds - 16 named args
	upsertRows := pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
		AddRow(issueID, now, now)
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(anyArgs(16)...).
		WillReturnRows(upsertRows)

	// Enqueue fails - 6 named args - but the error is ignored by the service (_, _ = ...)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnError(fmt.Errorf("job queue full"))

	issue, err := svc.IngestNormalized(context.Background(), orgID, ni)

	// Should still succeed because enqueue errors are silently ignored
	require.NoError(t, err)
	assert.NotNil(t, issue)
	assert.Equal(t, issueID, issue.ID)
	assert.Equal(t, "medium", issue.Severity) // "warning" normalizes to "medium"
	assert.NoError(t, mock.ExpectationsWereMet())
}
