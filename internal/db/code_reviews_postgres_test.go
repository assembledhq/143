package db

import (
	"context"
	"fmt"
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
			title text,
			revision_context jsonb
		);
		CREATE TABLE code_review_policies (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			risk_policy jsonb NOT NULL
		);
		CREATE TABLE pull_requests (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			title text NOT NULL,
			github_repo text NOT NULL,
			github_pr_number int NOT NULL
		);
		CREATE TABLE code_review_session_metadata (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			repository_id uuid NOT NULL,
			pull_request_id uuid NOT NULL,
			policy_id uuid NOT NULL,
			status text NOT NULL,
			stale boolean NOT NULL DEFAULT false,
			superseded_by_session_id uuid,
			acceptable boolean,
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

	var insertedPullRequestNumbers []int
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
		// The analytics report joins pull_requests so it can share the reviews
		// list filter builder (title/repo/number search), so every fact needs a
		// pull request of its own.
		pullRequestID := uuid.New()
		pullRequestNumber := len(insertedPullRequestNumbers) + 1
		insertedPullRequestNumbers = append(insertedPullRequestNumbers, pullRequestNumber)
		_, prErr := conn.Exec(ctx, `
			INSERT INTO pull_requests (id, org_id, title, github_repo, github_pr_number)
			VALUES ($1, $2, $3, 'acme/api', $4)`,
			pullRequestID, reviewOrgID, fmt.Sprintf("Ship feature %d", pullRequestNumber), pullRequestNumber,
		)
		require.NoError(t, prErr, "test should insert the pull request a review is attached to")
		_, insertErr := conn.Exec(ctx, `
			INSERT INTO code_review_session_metadata (
				id, org_id, session_id, repository_id, pull_request_id, policy_id, status, decision,
				github_review_id, lines_changed, additions, deletions, files_changed,
				risk_reason_details, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15)`,
			id, reviewOrgID, sessionID, reviewRepositoryID, pullRequestID, reviewPolicyID, status, decision,
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
	needsHumanLines, needsHumanMedianLines := 250.0, 250.0
	needsHumanAdditions, needsHumanMedianAdditions := 190.0, 190.0
	needsHumanDeletions, needsHumanMedianDeletions := 60.0, 60.0
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

	// The report now reuses the reviews list WHERE builder, so its SQL reaches
	// for pull_requests and sessions aliases that only exist because of the
	// joins in the filtered_reviews CTE, and its author ordering is an
	// interpolated expression. Execute every one of those against PostgreSQL:
	// the pgxmock unit tests match on query text and cannot catch an unresolved
	// alias or a malformed ORDER BY.
	store := NewCodeReviewStore(conn)
	currentActivity := models.CodeReviewActivityStatusCurrent
	approvedOutcome := models.CodeReviewListOutcomeAutomaticallyApproved
	for _, sortBy := range []string{
		"", "author", "reviews", "approved", "not_approved", "approval_rate",
		"split_sample", "average_additions", "median_additions", "average_deletions", "median_deletions",
	} {
		for _, sortOrder := range []string{"asc", "desc"} {
			_, sortErr := store.GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
				RepositoryID:    &repositoryID,
				CreatedAfter:    &createdAfter,
				AuthorSortBy:    sortBy,
				AuthorSortOrder: sortOrder,
			})
			require.NoErrorf(t, sortErr, "author sort %q %s should execute against PostgreSQL", sortBy, sortOrder)
		}
	}

	searched, err := store.GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
		RepositoryID:   &repositoryID,
		CreatedAfter:   &createdAfter,
		Search:         "Ship feature",
		ActivityStatus: &currentActivity,
		Outcome:        &approvedOutcome,
	})
	require.NoError(t, err, "the shared list filters should resolve their joined aliases in the analytics CTE")
	require.Equal(t, int64(2), searched.Summary.AutomaticallyApproved, "the outcome filter should narrow the report to posted approvals")
	require.Equal(t, int64(2), searched.Summary.ReviewsRequested, "search, activity, and outcome filters should all apply")

	unmatched, err := store.GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
		RepositoryID: &repositoryID,
		CreatedAfter: &createdAfter,
		Search:       "no pull request has this title",
	})
	require.NoError(t, err, "an unmatched search should still execute")
	require.Equal(t, int64(0), unmatched.Summary.ReviewsRequested, "search should filter on the joined pull request columns")

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

// The rank a page cursor anchors on is computed in Go while the rank the
// ORDER BY applies is computed in SQL. TestCodeReviewSortRankSQLCoversTheSame
// Branches only proves both sides use the same set of rank values; this proves
// they assign the same rank to the same row. Without it, transposing two
// branches on one side alone would quietly anchor cursors in the wrong label
// group and drop rows from the sorted list.
func TestCodeReviewSortRankMatchesPostgresForEveryRow(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the PostgreSQL sort rank equivalence test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer func() {
		require.NoError(t, conn.Close(context.Background()), "test should close the PostgreSQL connection")
	}()

	schema := "test_code_review_sort_rank_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated sort rank schema")
	defer func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated sort rank schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate sort rank objects")

	_, err = conn.Exec(ctx, `
		CREATE TABLE code_review_session_metadata (
			id uuid PRIMARY KEY,
			status text NOT NULL,
			stale boolean NOT NULL,
			superseded_by_session_id uuid,
			decision text,
			acceptable boolean,
			github_review_id bigint
		)`)
	require.NoError(t, err, "test should create the columns the rank expressions read")

	statuses := []models.CodeReviewSessionStatus{
		models.CodeReviewSessionStatusQueued, models.CodeReviewSessionStatusRunning,
		models.CodeReviewSessionStatusCompleted, models.CodeReviewSessionStatusFailed,
		models.CodeReviewSessionStatusStale, models.CodeReviewSessionStatusCancelled,
	}
	decisions := []*models.CodeReviewDecision{nil}
	for _, decision := range []models.CodeReviewDecision{
		models.CodeReviewDecisionApproved, models.CodeReviewDecisionNeedsHumanReview,
		models.CodeReviewDecisionBlocked, models.CodeReviewDecisionCommentOnly,
	} {
		decisions = append(decisions, &decision)
	}
	reviewID := int64(1)
	acceptable, notAcceptable := true, false
	supersededSession := uuid.New()

	expected := map[uuid.UUID]models.CodeReviewListItem{}
	for _, status := range statuses {
		for _, decision := range decisions {
			for _, githubReviewID := range []*int64{nil, &reviewID} {
				for _, riskValue := range []*bool{nil, &acceptable, &notAcceptable} {
					for _, superseded := range []*uuid.UUID{nil, &supersededSession} {
						for _, stale := range []bool{false, true} {
							id := uuid.New()
							expected[id] = models.CodeReviewListItem{
								CodeReviewSessionMetadata: models.CodeReviewSessionMetadata{
									ID: id, Status: status, Decision: decision, GitHubReviewID: githubReviewID,
									Acceptable: riskValue, SupersededBySessionID: superseded, Stale: stale,
								},
							}
							_, insertErr := conn.Exec(ctx, `
								INSERT INTO code_review_session_metadata (
									id, status, stale, superseded_by_session_id, decision, acceptable, github_review_id
								) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
								id, status, stale, superseded, decision, riskValue, githubReviewID,
							)
							require.NoError(t, insertErr, "test should insert a rank combination")
						}
					}
				}
			}
		}
	}

	for _, sortBy := range []string{"outcome", "risk", "run_status"} {
		t.Run(sortBy, func(t *testing.T) {
			sort, sortErr := codeReviewListSortFor(sortBy)
			require.NoError(t, sortErr, "the sort should be allowlisted")

			rows, queryErr := conn.Query(ctx, `SELECT m.id, `+sort.expression+` FROM code_review_session_metadata m`)
			require.NoError(t, queryErr, "the rank expression should execute against PostgreSQL")
			defer rows.Close()

			seen := 0
			for rows.Next() {
				var id uuid.UUID
				var sqlRank int
				require.NoError(t, rows.Scan(&id, &sqlRank), "the rank expression should return an integer")
				goRank, ok := CodeReviewSortRankForItem(sortBy, expected[id])
				require.True(t, ok, "the Go rank should be defined for a label-derived sort")
				require.Equalf(t, sqlRank, goRank,
					"row %s (status=%s stale=%t superseded=%t decision=%v approval_posted=%t acceptable=%v) should rank identically in Go and SQL",
					id, expected[id].Status, expected[id].Stale, expected[id].SupersededBySessionID != nil,
					expected[id].Decision, expected[id].GitHubReviewID != nil, expected[id].Acceptable,
				)
				seen++
			}
			require.NoError(t, rows.Err(), "the rank query should complete")
			require.Equal(t, len(expected), seen, "every seeded combination should be compared")
		})
	}
}
