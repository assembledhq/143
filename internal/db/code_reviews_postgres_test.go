package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStore_GetReviewAnalyticsPostgresBehavior(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the PostgreSQL analytics behavior test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer func() {
		require.NoError(t, conn.Close(context.Background()), "test should close the PostgreSQL connection")
	}()

	schema := "test_code_review_analytics_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated analytics schema")
	defer func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated analytics schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate analytics objects")

	_, err = conn.Exec(ctx, `
		CREATE TABLE sessions (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			revision_context jsonb
		);
		CREATE TABLE code_review_policies (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			risk_policy jsonb NOT NULL
		);
		CREATE TABLE code_review_session_metadata (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			repository_id uuid NOT NULL,
			policy_id uuid NOT NULL,
			status text NOT NULL,
			decision text,
			github_review_id bigint,
			lines_changed integer,
			additions integer,
			deletions integer,
			files_changed integer,
			risk_reason_details jsonb NOT NULL DEFAULT '[]',
			created_at timestamptz NOT NULL
		);
		CREATE TABLE code_review_findings (
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			severity text NOT NULL
		);
	`)
	require.NoError(t, err, "test should create the minimal analytics schema")

	orgID := uuid.New()
	otherOrgID := uuid.New()
	repositoryID := uuid.New()
	otherRepositoryID := uuid.New()
	policyID := uuid.New()
	otherPolicyID := uuid.New()
	approvedSessionID := uuid.New()
	needsHumanSessionID := uuid.New()
	failedSessionID := uuid.New()
	oldSessionID := uuid.New()
	legacySessionID := uuid.New()
	otherOrgSessionID := uuid.New()
	recentAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	createdAfter := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_policies (id, org_id, risk_policy)
		VALUES
			($1, $2, '{"max_lines_changed":200,"max_files_changed":5}'),
			($3, $4, '{"max_lines_changed":3000000000,"max_files_changed":3000000000}')`,
		policyID, orgID, otherPolicyID, otherOrgID,
	)
	require.NoError(t, err, "test should insert captured analytics policies")

	_, err = conn.Exec(ctx, `
		INSERT INTO sessions (id, org_id, revision_context)
		VALUES
			($1, $2, '{"pull_request_author":"anya"}'),
			($3, $2, '{"pull_request_author":"sam"}'),
			($4, $2, '{"pull_request_author":"anya"}'),
			($5, $2, '{"pull_request_author":"old"}'),
			($6, $2, '{"pull_request_author":"anya"}'),
			($7, $8, '{"pull_request_author":"other"}')`,
		approvedSessionID, orgID, needsHumanSessionID, failedSessionID, oldSessionID,
		legacySessionID, otherOrgSessionID, otherOrgID,
	)
	require.NoError(t, err, "test should insert analytics session attribution")

	insertReview := func(
		id, reviewOrgID, sessionID, reviewRepositoryID, reviewPolicyID uuid.UUID,
		status string,
		decision *models.CodeReviewDecision,
		githubReviewID *int64,
		linesChanged, additions, deletions, filesChanged *int,
		riskReasons string,
		createdAt time.Time,
	) {
		t.Helper()
		_, insertErr := conn.Exec(ctx, `
			INSERT INTO code_review_session_metadata (
				id, org_id, session_id, repository_id, policy_id, status, decision,
				github_review_id, lines_changed, additions, deletions, files_changed,
				risk_reason_details, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14)`,
			id, reviewOrgID, sessionID, reviewRepositoryID, reviewPolicyID, status, decision,
			githubReviewID, linesChanged, additions, deletions, filesChanged, riskReasons, createdAt,
		)
		require.NoError(t, insertErr, "test should insert a review analytics fact")
	}

	approvedDecision := models.CodeReviewDecisionApproved
	needsHumanDecision := models.CodeReviewDecisionNeedsHumanReview
	reviewID := int64(143)
	smallLines, smallFiles := 40, 2
	smallAdditions, smallDeletions := 30, 10
	largeLines, largeFiles := 250, 8
	largeAdditions, largeDeletions := 190, 60
	legacyLines, legacyFiles := 100, 4
	insertReview(
		uuid.New(), orgID, approvedSessionID, repositoryID, policyID,
		"completed", &approvedDecision, &reviewID,
		&smallLines, &smallAdditions, &smallDeletions, &smallFiles, "[]", recentAt,
	)
	insertReview(
		uuid.New(), orgID, needsHumanSessionID, repositoryID, policyID,
		"completed", &needsHumanDecision, nil,
		&largeLines, &largeAdditions, &largeDeletions, &largeFiles,
		`[{"code":"lines_limit_exceeded"},{"code":"files_limit_exceeded"}]`, recentAt,
	)
	insertReview(
		uuid.New(), orgID, failedSessionID, repositoryID, policyID,
		"failed", nil, nil, nil, nil, nil, nil, "[]", recentAt,
	)
	insertReview(
		uuid.New(), orgID, oldSessionID, repositoryID, policyID,
		"completed", &approvedDecision, &reviewID,
		&smallLines, &smallAdditions, &smallDeletions, &smallFiles, "[]",
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	)
	insertReview(
		uuid.New(), orgID, legacySessionID, repositoryID, policyID,
		"completed", &approvedDecision, &reviewID,
		&legacyLines, nil, nil, &legacyFiles, "[]", recentAt,
	)
	insertReview(
		uuid.New(), otherOrgID, otherOrgSessionID, otherRepositoryID, otherPolicyID,
		"completed", &approvedDecision, &reviewID,
		&smallLines, &smallAdditions, &smallDeletions, &smallFiles, "[]", recentAt,
	)

	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_findings (org_id, session_id, severity)
		VALUES ($1, $2, 'low'), ($1, $2, 'high'), ($3, $4, 'critical')`,
		orgID, approvedSessionID, otherOrgID, otherOrgSessionID,
	)
	require.NoError(t, err, "test should insert in-scope and cross-org findings")

	averageLines, medianLines := 130.0, 100.0
	averageAdditions, medianAdditions := 110.0, 110.0
	averageDeletions, medianDeletions := 35.0, 35.0
	averageFiles, medianFiles := 14.0/3.0, 4.0
	approvedLines, approvedMedianLines := 70.0, 70.0
	approvedAdditions, approvedMedianAdditions := 30.0, 30.0
	approvedDeletions, approvedMedianDeletions := 10.0, 10.0
	approvedMedianFiles := 3.0
	needsHumanLines, needsHumanMedianLines := 250.0, 250.0
	needsHumanAdditions, needsHumanMedianAdditions := 190.0, 190.0
	needsHumanDeletions, needsHumanMedianDeletions := 60.0, 60.0
	needsHumanMedianFiles := 8.0
	analytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
		RepositoryID: &repositoryID,
		CreatedAfter: &createdAfter,
	})

	require.NoError(t, err, "single-statement analytics should execute against PostgreSQL")
	require.Equal(t, models.CodeReviewAnalytics{
		Summary: models.CodeReviewAnalyticsSummary{
			ReviewsRequested:            4,
			ReviewsCompleted:            3,
			AutomaticallyApproved:       2,
			NotApproved:                 1,
			NeedsHumanReview:            1,
			ReviewsWithSizeData:         3,
			ReviewsWithChangeBreakdown:  2,
			AverageLinesChanged:         &averageLines,
			MedianLinesChanged:          &medianLines,
			AverageAdditions:            &averageAdditions,
			MedianAdditions:             &medianAdditions,
			AverageDeletions:            &averageDeletions,
			MedianDeletions:             &medianDeletions,
			AverageFilesChanged:         &averageFiles,
			MedianFilesChanged:          &medianFiles,
			ReviewsAboveSizeLimit:       1,
			ReviewsWithFindings:         1,
			ReviewsWithBlockingFindings: 1,
			TotalFindings:               2,
			FailedReviews:               1,
		},
		Authors: []models.CodeReviewAuthorAnalytics{
			{
				Author:                     "anya",
				ReviewsCompleted:           2,
				AutomaticallyApproved:      2,
				ReviewsWithSizeData:        2,
				ReviewsWithChangeBreakdown: 1,
				AverageLinesChanged:        &approvedLines,
				MedianLinesChanged:         &approvedMedianLines,
				AverageAdditions:           &approvedAdditions,
				MedianAdditions:            &approvedMedianAdditions,
				AverageDeletions:           &approvedDeletions,
				MedianDeletions:            &approvedMedianDeletions,
				MedianFilesChanged:         &approvedMedianFiles,
			},
			{
				Author:                     "sam",
				ReviewsCompleted:           1,
				NotApproved:                1,
				ReviewsWithSizeData:        1,
				ReviewsWithChangeBreakdown: 1,
				AverageLinesChanged:        &needsHumanLines,
				MedianLinesChanged:         &needsHumanMedianLines,
				AverageAdditions:           &needsHumanAdditions,
				MedianAdditions:            &needsHumanMedianAdditions,
				AverageDeletions:           &needsHumanDeletions,
				MedianDeletions:            &needsHumanMedianDeletions,
				MedianFilesChanged:         &needsHumanMedianFiles,
			},
		},
		SizeBuckets: []models.CodeReviewSizeBucketAnalytics{
			{Bucket: models.CodeReviewSizeBucketSmall, ReviewsCompleted: 1, AutomaticallyApproved: 1},
			{Bucket: models.CodeReviewSizeBucketMedium, ReviewsCompleted: 1, AutomaticallyApproved: 1},
			{Bucket: models.CodeReviewSizeBucketLarge, ReviewsCompleted: 1},
		},
		NonApprovalReasons: []models.CodeReviewNonApprovalReasonAnalytics{
			{Code: models.CodeReviewRiskReasonFilesLimitExceeded, Reviews: 1},
			{Code: models.CodeReviewRiskReasonLinesLimitExceeded, Reviews: 1},
		},
	}, analytics, "analytics should preserve outcome, author, size, reason, finding, time, and tenancy semantics")

	otherLines := float64(smallLines)
	otherAdditions := float64(smallAdditions)
	otherDeletions := float64(smallDeletions)
	otherFiles := float64(smallFiles)
	otherOrgAnalytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, otherOrgID, CodeReviewAnalyticsFilters{
		RepositoryID: &otherRepositoryID,
	})

	require.NoError(t, err, "analytics should accept captured policy limits larger than a PostgreSQL integer")
	require.Equal(t, models.CodeReviewAnalytics{
		Summary: models.CodeReviewAnalyticsSummary{
			ReviewsRequested:            1,
			ReviewsCompleted:            1,
			AutomaticallyApproved:       1,
			ReviewsWithSizeData:         1,
			ReviewsWithChangeBreakdown:  1,
			AverageLinesChanged:         &otherLines,
			MedianLinesChanged:          &otherLines,
			AverageAdditions:            &otherAdditions,
			MedianAdditions:             &otherAdditions,
			AverageDeletions:            &otherDeletions,
			MedianDeletions:             &otherDeletions,
			AverageFilesChanged:         &otherFiles,
			MedianFilesChanged:          &otherFiles,
			ReviewsWithFindings:         1,
			ReviewsWithBlockingFindings: 1,
			TotalFindings:               1,
		},
		Authors: []models.CodeReviewAuthorAnalytics{
			{
				Author:                     "other",
				ReviewsCompleted:           1,
				AutomaticallyApproved:      1,
				ReviewsWithSizeData:        1,
				ReviewsWithChangeBreakdown: 1,
				AverageLinesChanged:        &otherLines,
				MedianLinesChanged:         &otherLines,
				AverageAdditions:           &otherAdditions,
				MedianAdditions:            &otherAdditions,
				AverageDeletions:           &otherDeletions,
				MedianDeletions:            &otherDeletions,
				MedianFilesChanged:         &otherFiles,
			},
		},
		SizeBuckets: []models.CodeReviewSizeBucketAnalytics{
			{Bucket: models.CodeReviewSizeBucketSmall, ReviewsCompleted: 1, AutomaticallyApproved: 1},
		},
		NonApprovalReasons: []models.CodeReviewNonApprovalReasonAnalytics{},
	}, otherOrgAnalytics, "large captured limits should remain usable in every analytics section")

	emptyAnalytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, uuid.New(), CodeReviewAnalyticsFilters{})

	require.NoError(t, err, "single-statement analytics should return an empty PostgreSQL report")
	require.Equal(t, models.CodeReviewAnalytics{
		Authors:            []models.CodeReviewAuthorAnalytics{},
		SizeBuckets:        []models.CodeReviewSizeBucketAnalytics{},
		NonApprovalReasons: []models.CodeReviewNonApprovalReasonAnalytics{},
	}, emptyAnalytics, "an organization without reviews should receive zero summary values and empty breakdowns")
}
