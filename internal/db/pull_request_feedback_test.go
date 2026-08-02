package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPullRequestFeedbackStore_IngestRecordOnlyDoesNotEnqueueCollection(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	integrationID := uuid.New()
	deliveryID := "github-delivery-1"
	reason := "handled_as_code_review_dispute"
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	item := models.PullRequestFeedbackItem{
		OrgID: orgID, PullRequestID: pullRequestID,
		Surface: models.PRFeedbackSurfaceIssueComment, ProviderObjectID: 991,
		GitHubDeliveryID: &deliveryID, AuthorLogin: "octocat",
		AuthorType: models.PRFeedbackAuthorTypeUser, AuthorAssociation: "MEMBER",
		Body: "@acme/143-code-reviewer this block is incorrect", BodyHash: "body-hash",
		Intent: models.PRFeedbackIntentUnknown, Status: models.PRFeedbackItemStatusIgnored,
		IgnoreReason: &reason,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("INSERT INTO pull_request_feedback_items[\\s\\S]+WHEN EXCLUDED.status='ignored' THEN 'ignored'").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(prFeedbackItemColumnNames()).AddRow(
			uuid.New(), orgID, pullRequestID, nil, item.Surface, item.ProviderObjectID,
			item.GitHubDeliveryID, nil, nil, nil, nil, nil, item.AuthorLogin, item.AuthorType,
			item.AuthorAssociation, item.BotEligibilitySource, item.Body, item.BodyHash, nil, nil, nil, 0,
			nil, nil, nil, nil, nil, "head-sha", item.Intent, item.Status, item.IgnoreReason,
			nil, nil, nil, nil, nil, now, nil, now,
		))
	mock.ExpectCommit()

	store := NewPullRequestFeedbackStore(mock)
	store.SetJobStore(NewJobStore(mock))
	recorded, err := store.Ingest(context.Background(), &models.WebhookDelivery{
		OrgID: orgID, IntegrationID: integrationID, Provider: "github",
		DeliveryID: &deliveryID, EventType: "issue_comment", Status: "processed",
	}, &item)

	require.NoError(t, err, "record-only feedback should preserve the webhook ledger without scheduling work")
	require.True(t, recorded, "record-only feedback should report that the delivery was recorded")
	require.Equal(t, models.PRFeedbackItemStatusIgnored, item.Status, "captured dispute should remain ignored by generic feedback follow-through")
	require.Equal(t, &reason, item.IgnoreReason, "captured dispute should retain the dedicated ignore reason")
	require.NoError(t, mock.ExpectationsWereMet(), "record-only ingestion should commit without creating a collection job")
}

func prFeedbackItemColumnNames() []string {
	parts := strings.Split(strings.ReplaceAll(prFeedbackItemColumns, "\n", ""), ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}
