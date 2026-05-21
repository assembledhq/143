package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
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

func TestIngestNormalized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ni          func(integrationID uuid.UUID, now time.Time) NormalizedIssue
		setupMock   func(mock pgxmock.PgxPoolIface, issueID uuid.UUID, now time.Time)
		expectErr   bool
		errSubstr   string
		checkResult func(t *testing.T, issue *models.Issue, issueID uuid.UUID)
	}{
		{
			name: "successful ingestion upserts issue and enqueues prioritize job",
			ni: func(integrationID uuid.UUID, now time.Time) NormalizedIssue {
				return NormalizedIssue{
					ExternalID:            "EXT-123",
					Source:                models.IssueSourceSentry,
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
			},
			setupMock: func(mock pgxmock.PgxPoolIface, issueID uuid.UUID, now time.Time) {
				upsertRows := pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
					AddRow(issueID, now, now)
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(anyArgs(16)...).
					WillReturnRows(upsertRows)

				jobID := uuid.New()
				enqueueRows := pgxmock.NewRows([]string{"id"}).
					AddRow(jobID)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(anyArgs(6)...).
					WillReturnRows(enqueueRows)
			},
			expectErr: false,
			checkResult: func(t *testing.T, issue *models.Issue, issueID uuid.UUID) {
				t.Helper()
				require.NotNil(t, issue, "ingested issue should not be nil")
				require.Equal(t, issueID, issue.ID, "issue ID should match upserted value")
				require.Equal(t, models.IssueSourceSentry, issue.Source, "source should be sentry")
				require.Equal(t, "EXT-123", issue.ExternalID, "external ID should match input")
				require.Equal(t, "Test Issue", issue.Title, "title should match input")
				require.Equal(t, models.IssueSeverityHigh, issue.Severity, "error severity should normalize to high")
				require.Equal(t, models.IssueStatusOpen, issue.Status, "status should default to open")
				require.Equal(t, 5, issue.OccurrenceCount, "occurrence count should match input")
			},
		},
		{
			name: "upsert failure returns error",
			ni: func(integrationID uuid.UUID, now time.Time) NormalizedIssue {
				return NormalizedIssue{
					ExternalID:          "EXT-456",
					Source:              models.IssueSource("pagerduty"),
					SourceIntegrationID: integrationID,
					Title:               "Failing Issue",
					Severity:            "critical",
					FirstSeenAt:         now,
					LastSeenAt:          now,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(anyArgs(16)...).
					WillReturnError(fmt.Errorf("database connection lost"))
			},
			expectErr: true,
			errSubstr: "upsert issue",
			checkResult: func(t *testing.T, issue *models.Issue, issueID uuid.UUID) {
				t.Helper()
				require.Nil(t, issue, "result should be nil on upsert failure")
			},
		},
		{
			name: "enqueue failure is ignored and ingestion still succeeds",
			ni: func(integrationID uuid.UUID, now time.Time) NormalizedIssue {
				return NormalizedIssue{
					ExternalID:          "EXT-789",
					Source:              models.IssueSource("github"),
					SourceIntegrationID: integrationID,
					Title:               "Another Issue",
					Severity:            "warning",
					FirstSeenAt:         now,
					LastSeenAt:          now,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, issueID uuid.UUID, now time.Time) {
				upsertRows := pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
					AddRow(issueID, now, now)
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(anyArgs(16)...).
					WillReturnRows(upsertRows)

				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(anyArgs(6)...).
					WillReturnError(fmt.Errorf("job queue full"))
			},
			expectErr: false,
			checkResult: func(t *testing.T, issue *models.Issue, issueID uuid.UUID) {
				t.Helper()
				require.NotNil(t, issue, "ingested issue should not be nil even when enqueue fails")
				require.Equal(t, issueID, issue.ID, "issue ID should match upserted value")
				require.Equal(t, models.IssueSeverityMedium, issue.Severity, "warning severity should normalize to medium")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			issueStore := db.NewIssueStore(mock)
			webhookStore := db.NewWebhookDeliveryStore(mock)
			jobStore := db.NewJobStore(mock)
			svc := NewService(issueStore, webhookStore, jobStore, zerolog.Nop())

			orgID := uuid.New()
			issueID := uuid.New()
			integrationID := uuid.New()
			now := time.Now()

			ni := tt.ni(integrationID, now)
			tt.setupMock(mock, issueID, now)

			issue, err := svc.IngestNormalized(context.Background(), orgID, ni)

			if tt.expectErr {
				require.Error(t, err, "IngestNormalized should return an error")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "IngestNormalized should not return an error")
			}

			tt.checkResult(t, issue, issueID)
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
