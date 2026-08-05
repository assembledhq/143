package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	reason := models.PRFeedbackIgnoreReasonCodeReviewDispute
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
	expectPRFeedbackItemUpsert(mock).
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

// TestPullRequestFeedbackStore_IngestOrdinaryIgnoreStillSchedulesCollection
// pins the boundary of the record-only shortcut. Only a code-review dispute
// capture claims a comment; the eligibility ignores predate disputes and have
// always extended the collection window and enqueued the collector.
func TestPullRequestFeedbackStore_IngestOrdinaryIgnoreStillSchedulesCollection(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	deliveryID := "github-delivery-2"
	reason := "human_mode_off"
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	item := models.PullRequestFeedbackItem{
		OrgID: orgID, PullRequestID: pullRequestID,
		Surface: models.PRFeedbackSurfaceIssueComment, ProviderObjectID: 992,
		GitHubDeliveryID: &deliveryID, AuthorLogin: "octocat",
		AuthorType: models.PRFeedbackAuthorTypeUser, AuthorAssociation: "MEMBER",
		Body: "please rename this helper", BodyHash: "body-hash",
		Intent: models.PRFeedbackIntentUnknown, Status: models.PRFeedbackItemStatusIgnored,
		IgnoreReason: &reason,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	expectPRFeedbackItemUpsert(mock).
		WillReturnRows(pgxmock.NewRows(prFeedbackItemColumnNames()).AddRow(
			uuid.New(), orgID, pullRequestID, nil, item.Surface, item.ProviderObjectID,
			item.GitHubDeliveryID, nil, nil, nil, nil, nil, item.AuthorLogin, item.AuthorType,
			item.AuthorAssociation, item.BotEligibilitySource, item.Body, item.BodyHash, nil, nil, nil, 0,
			nil, nil, nil, nil, nil, "head-sha", item.Intent, item.Status, item.IgnoreReason,
			nil, nil, nil, nil, nil, now, nil, now,
		))
	mock.ExpectExec("UPDATE pull_request_feedback_batches").
		WithArgs(orgID, pullRequestID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectCommit()

	store := NewPullRequestFeedbackStore(mock)
	store.SetJobStore(NewJobStore(mock))
	recorded, err := store.Ingest(context.Background(), &models.WebhookDelivery{
		OrgID: orgID, IntegrationID: uuid.New(), Provider: "github",
		DeliveryID: &deliveryID, EventType: "issue_comment", Status: "processed",
	}, &item)

	require.NoError(t, err, "an eligibility ignore should ingest normally")
	require.True(t, recorded, "the delivery should still be recorded")
	require.NoError(t, mock.ExpectationsWereMet(),
		"only a dispute capture may skip the collection window and collector job")
}

func TestPullRequestFeedbackStore_ReleaseCodeReviewDisputeItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rowsAffected int64
		expectJob    bool
	}{
		{name: "released inline comment schedules collection", rowsAffected: 1, expectJob: true},
		{name: "missing or already released comment is idempotent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()

			orgID := uuid.New()
			pullRequestID := uuid.New()
			providerObjectID := int64(5196810672)
			mock.ExpectBegin()
			mock.ExpectExec("UPDATE pull_request_feedback_items[\\s\\S]+ignore_reason = @ignore_reason").
				WithArgs(pgx.NamedArgs{
					"org_id": orgID, "pull_request_id": pullRequestID,
					"surface":            models.PRFeedbackSurfaceReviewComment,
					"provider_object_id": providerObjectID,
					"ignore_reason":      models.PRFeedbackIgnoreReasonCodeReviewDispute,
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))
			if tt.expectJob {
				mock.ExpectExec("UPDATE pull_request_feedback_batches").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			}
			mock.ExpectCommit()

			store := NewPullRequestFeedbackStore(mock)
			store.SetJobStore(NewJobStore(mock))
			err = store.ReleaseCodeReviewDisputeItem(
				context.Background(), orgID, pullRequestID,
				models.PRFeedbackSurfaceReviewComment, providerObjectID,
			)

			require.NoError(t, err, "releasing a non-dispute comment should be idempotent")
			require.NoError(t, mock.ExpectationsWereMet(), "release and collector scheduling should share one transaction")
		})
	}
}

func expectPRFeedbackItemUpsert(mock pgxmock.PgxPoolIface) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery("INSERT INTO pull_request_feedback_items[\\s\\S]+EXCLUDED.ignore_reason IS NOT DISTINCT FROM @code_review_dispute_reason").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		)
}

func prFeedbackItemColumnNames() []string {
	parts := strings.Split(strings.ReplaceAll(prFeedbackItemColumns, "\n", ""), ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}
