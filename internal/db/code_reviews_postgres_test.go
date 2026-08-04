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
			status text NOT NULL,
			head_sha text NOT NULL,
			stale boolean NOT NULL DEFAULT false,
			superseded_by_session_id uuid,
			acceptable boolean,
			decision text,
			github_review_id bigint,
			additions integer,
			deletions integer,
			risk_reason_details jsonb NOT NULL DEFAULT '[]',
			completed_at timestamptz,
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
	approvedSessionID := uuid.New()
	needsHumanSessionID := uuid.New()
	failedSessionID := uuid.New()
	oldSessionID := uuid.New()
	legacySessionID := uuid.New()
	otherOrgSessionID := uuid.New()
	recentAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	createdAfter := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	_, err = conn.Exec(ctx, `
		INSERT INTO sessions (id, org_id, revision_context)
		VALUES
			($1, $2, '{"pull_request_author":"anya","github_delivery_id":"delivery-1","request_context":{"source":"issue_comment","author_login":"anya"}}'),
			($3, $2, '{"pull_request_author":"sam","github_delivery_id":"delivery-2","request_context":{"source":"issue_comment","author_login":"anya"}}'),
			($4, $2, '{"pull_request_author":"anya","github_delivery_id":"","request_context":{"source":"issue_comment","author_login":"anya"}}'),
			($5, $2, '{"pull_request_author":"old","github_delivery_id":"delivery-old","request_context":{"source":"issue_comment","author_login":"old"}}'),
			($6, $2, '{"pull_request_author":"anya"}'),
			($7, $8, '{"pull_request_author":"other","github_delivery_id":"delivery-other","request_context":{"source":"issue_comment","author_login":"intruder"}}')`,
		approvedSessionID, orgID, needsHumanSessionID, failedSessionID, oldSessionID,
		legacySessionID, otherOrgSessionID, otherOrgID,
	)
	require.NoError(t, err, "test should insert analytics session attribution")

	var insertedPullRequestNumbers []int
	insertReview := func(
		id, reviewOrgID, sessionID, reviewRepositoryID uuid.UUID,
		status string,
		decision *models.CodeReviewDecision,
		githubReviewID *int64,
		additions, deletions *int,
		riskReasons string,
		createdAt time.Time,
	) {
		t.Helper()
		// The analytics report joins pull_requests to scope the cohort to the
		// organization, so every fact needs a pull request of its own.
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
				id, org_id, session_id, repository_id, pull_request_id, status, decision,
				head_sha, github_review_id, additions, deletions,
				risk_reason_details, completed_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb,
				CASE WHEN $6 = 'completed' THEN $13::timestamptz ELSE NULL END, $13::timestamptz)`,
			id, reviewOrgID, sessionID, reviewRepositoryID, pullRequestID, status, decision,
			fmt.Sprintf("sha-%d", pullRequestNumber), githubReviewID, additions, deletions,
			riskReasons, createdAt,
		)
		require.NoError(t, insertErr, "test should insert a review analytics fact")
	}

	approvedDecision := models.CodeReviewDecisionApproved
	needsHumanDecision := models.CodeReviewDecisionNeedsHumanReview
	reviewID := int64(143)
	smallAdditions, smallDeletions := 30, 10
	largeAdditions, largeDeletions := 190, 60
	insertReview(
		uuid.New(), orgID, approvedSessionID, repositoryID,
		"completed", &approvedDecision, &reviewID,
		&smallAdditions, &smallDeletions, "[]", recentAt,
	)
	insertReview(
		uuid.New(), orgID, needsHumanSessionID, repositoryID,
		"completed", &needsHumanDecision, nil,
		&largeAdditions, &largeDeletions,
		`[{"code":"lines_limit_exceeded"},{"code":"files_limit_exceeded"}]`, recentAt,
	)
	insertReview(
		uuid.New(), orgID, failedSessionID, repositoryID,
		"failed", nil, nil, nil, nil, "[]", recentAt,
	)
	insertReview(
		uuid.New(), orgID, oldSessionID, repositoryID,
		"completed", &approvedDecision, &reviewID,
		&smallAdditions, &smallDeletions, "[]",
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	)
	insertReview(
		uuid.New(), orgID, legacySessionID, repositoryID,
		"completed", &approvedDecision, &reviewID,
		nil, nil, "[]", recentAt,
	)
	insertReview(
		uuid.New(), otherOrgID, otherOrgSessionID, otherRepositoryID,
		"completed", &approvedDecision, &reviewID,
		&smallAdditions, &smallDeletions, "[]", recentAt,
	)

	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_findings (org_id, session_id, severity)
		VALUES ($1, $2, 'low'), ($1, $2, 'high'), ($3, $4, 'critical')`,
		orgID, approvedSessionID, otherOrgID, otherOrgSessionID,
	)
	require.NoError(t, err, "test should insert in-scope and cross-org findings")

	medianAdditions := 110.0
	medianDeletions := 35.0
	approvedMedianAdditions := 30.0
	approvedMedianDeletions := 10.0
	needsHumanMedianAdditions := 190.0
	needsHumanMedianDeletions := 60.0
	analytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
		RepositoryID: &repositoryID,
		CreatedAfter: &createdAfter,
	})

	require.NoError(t, err, "single-statement analytics should execute against PostgreSQL")
	require.Equal(t, models.CodeReviewAnalytics{
		Summary: models.CodeReviewAnalyticsSummary{
			PRsReviewed:             4,
			PRsWithCompletedRound:   3,
			ApprovedBy143:           2,
			NotApproved:             1,
			ApprovedFirstRound:      2,
			MedianRoundsToApproval:  func() *float64 { value := 1.0; return &value }(),
			NeedsHumanReview:        1,
			PRsWithChangeBreakdown:  2,
			MedianAdditions:         &medianAdditions,
			MedianDeletions:         &medianDeletions,
			PRsWithFindings:         1,
			PRsWithBlockingFindings: 1,
			TotalFindings:           2,
			PRsWithFailedAttempt:    1,
		},
		ApprovalRounds: []models.CodeReviewApprovalRoundAnalytics{
			{Bucket: models.CodeReviewApprovalRound1, PRs: 2},
			{Bucket: models.CodeReviewApprovalRound2, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound3, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound4Plus, PRs: 0},
			{Bucket: models.CodeReviewApprovalRoundNotYet, PRs: 2},
		},
		Authors: []models.CodeReviewAuthorAnalytics{
			{
				Author:                 "anya",
				PRsReviewed:            3,
				ApprovedBy143:          2,
				ApprovedFirstRound:     2,
				MedianRoundsToApproval: func() *float64 { value := 1.0; return &value }(),
				MedianAdditions:        &approvedMedianAdditions,
				MedianDeletions:        &approvedMedianDeletions,
			},
			{
				Author:          "sam",
				PRsReviewed:     1,
				NotApproved:     1,
				MedianAdditions: &needsHumanMedianAdditions,
				MedianDeletions: &needsHumanMedianDeletions,
			},
		},
		NonApprovalReasons: []models.CodeReviewNonApprovalReasonAnalytics{
			{Code: models.CodeReviewRiskReasonFilesLimitExceeded, PRs: 1},
			{Code: models.CodeReviewRiskReasonLinesLimitExceeded, PRs: 1},
		},
		CommentRequestsTotal: 2,
		CommentRequestsByUser: []models.CodeReviewCommentRequestUserAnalytics{
			{GitHubLogin: "anya", Requests: 2},
		},
	}, analytics, "analytics should preserve outcome, author, size, reason, finding, time, and tenancy semantics")

	// Author ordering is interpolated from a strict allowlist. Execute every
	// supported expression against PostgreSQL because pgxmock query matching
	// cannot catch an unresolved alias or malformed ORDER BY.
	store := NewCodeReviewStore(conn)
	for _, sortBy := range []string{
		"", "author", "reviews", "approved", "not_approved", "approval_rate",
		"first_round", "median_rounds", "median_additions", "median_deletions",
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

	otherAdditions := float64(smallAdditions)
	otherDeletions := float64(smallDeletions)
	otherOrgAnalytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, otherOrgID, CodeReviewAnalyticsFilters{
		RepositoryID: &otherRepositoryID,
	})

	require.NoError(t, err, "analytics should return the selected organization report")
	require.Equal(t, models.CodeReviewAnalytics{
		Summary: models.CodeReviewAnalyticsSummary{
			PRsReviewed:             1,
			PRsWithCompletedRound:   1,
			ApprovedBy143:           1,
			ApprovedFirstRound:      1,
			MedianRoundsToApproval:  func() *float64 { value := 1.0; return &value }(),
			PRsWithChangeBreakdown:  1,
			MedianAdditions:         &otherAdditions,
			MedianDeletions:         &otherDeletions,
			PRsWithFindings:         1,
			PRsWithBlockingFindings: 1,
			TotalFindings:           1,
		},
		ApprovalRounds: []models.CodeReviewApprovalRoundAnalytics{
			{Bucket: models.CodeReviewApprovalRound1, PRs: 1},
			{Bucket: models.CodeReviewApprovalRound2, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound3, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound4Plus, PRs: 0},
			{Bucket: models.CodeReviewApprovalRoundNotYet, PRs: 0},
		},
		Authors: []models.CodeReviewAuthorAnalytics{
			{
				Author:                 "other",
				PRsReviewed:            1,
				ApprovedBy143:          1,
				ApprovedFirstRound:     1,
				MedianRoundsToApproval: func() *float64 { value := 1.0; return &value }(),
				MedianAdditions:        &otherAdditions,
				MedianDeletions:        &otherDeletions,
			},
		},
		NonApprovalReasons:   []models.CodeReviewNonApprovalReasonAnalytics{},
		CommentRequestsTotal: 1,
		CommentRequestsByUser: []models.CodeReviewCommentRequestUserAnalytics{
			{GitHubLogin: "intruder", Requests: 1},
		},
	}, otherOrgAnalytics, "analytics should remain isolated to the selected organization")

	emptyAnalytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, uuid.New(), CodeReviewAnalyticsFilters{})

	require.NoError(t, err, "single-statement analytics should return an empty PostgreSQL report")
	require.Equal(t, models.CodeReviewAnalytics{
		ApprovalRounds: []models.CodeReviewApprovalRoundAnalytics{
			{Bucket: models.CodeReviewApprovalRound1, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound2, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound3, PRs: 0},
			{Bucket: models.CodeReviewApprovalRound4Plus, PRs: 0},
			{Bucket: models.CodeReviewApprovalRoundNotYet, PRs: 0},
		},
		Authors:               []models.CodeReviewAuthorAnalytics{},
		NonApprovalReasons:    []models.CodeReviewNonApprovalReasonAnalytics{},
		CommentRequestsByUser: []models.CodeReviewCommentRequestUserAnalytics{},
	}, emptyAnalytics, "an organization without reviews should receive zero summary values and empty breakdowns")
}

func TestCodeReviewStore_GetReviewAnalyticsPRJourneys(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the PostgreSQL PR-journey analytics test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer func() {
		require.NoError(t, conn.Close(context.Background()), "test should close the PostgreSQL connection")
	}()

	schema := "test_code_review_pr_journeys_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated PR-journey schema")
	defer func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated PR-journey schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate PR-journey objects")

	_, err = conn.Exec(ctx, `
		CREATE TABLE sessions (
			id uuid PRIMARY KEY, org_id uuid NOT NULL, revision_context jsonb
		);
		CREATE TABLE pull_requests (
			id uuid PRIMARY KEY, org_id uuid NOT NULL, title text NOT NULL,
			github_repo text NOT NULL, github_pr_number int NOT NULL
		);
		CREATE TABLE code_review_session_metadata (
			id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid NOT NULL,
			repository_id uuid NOT NULL, pull_request_id uuid NOT NULL,
			status text NOT NULL, head_sha text NOT NULL, decision text, github_review_id bigint,
			additions integer, deletions integer,
			risk_reason_details jsonb NOT NULL DEFAULT '[]', completed_at timestamptz,
			created_at timestamptz NOT NULL
		);
		CREATE TABLE code_review_findings (
			org_id uuid NOT NULL, session_id uuid NOT NULL, severity text NOT NULL
		);
	`)
	require.NoError(t, err, "test should create minimal PR-journey tables")

	orgID, otherOrgID := uuid.New(), uuid.New()
	repositoryID, otherRepositoryID := uuid.New(), uuid.New()
	immediatePRID, iteratedPRID, internalApprovalPRID, operationalPRID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	otherPRID := uuid.New()
	cohortStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	cohortEnd := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	baseTime := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	_, err = conn.Exec(ctx, `
		INSERT INTO pull_requests (id, org_id, title, github_repo, github_pr_number) VALUES
			($1, $2, 'Immediate approval', 'acme/api', 1),
			($3, $2, 'Iterated approval', 'acme/api', 2),
			($4, $2, 'Internal approval', 'acme/api', 3),
			($5, $2, 'Operational only', 'acme/api', 4),
			($6, $7, 'Other tenant', 'other/api', 1)`,
		immediatePRID, orgID, iteratedPRID, internalApprovalPRID, operationalPRID,
		otherPRID, otherOrgID,
	)
	require.NoError(t, err, "test should insert tenant-scoped PR-journey parents")

	type reviewFact struct {
		orgID, sessionID, repositoryID, pullRequestID uuid.UUID
		author, status, headSHA, decision             string
		githubReviewID                                *int64
		additions, deletions                          *int
		reasons                                       string
		createdAt, completedAt                        time.Time
	}
	postedReviewID := int64(143)
	value := func(number int) *int { return &number }
	facts := []reviewFact{
		// Same-head reruns collapse to the earliest posted approval.
		{orgID, uuid.New(), repositoryID, immediatePRID, "alice", "completed", "a1", "needs_human_review", nil, value(20), value(10), `[{"code":"blocking_findings"}]`, baseTime, baseTime.Add(time.Minute)},
		{orgID, uuid.New(), repositoryID, immediatePRID, "alice", "completed", "a1", "approved", &postedReviewID, value(30), value(10), `[]`, baseTime.Add(time.Minute), baseTime.Add(2 * time.Minute)},
		{orgID, uuid.New(), repositoryID, immediatePRID, "alice", "completed", "a1", "approved", &postedReviewID, value(70), value(20), `[]`, baseTime.Add(2 * time.Minute), baseTime.Add(3 * time.Minute)},
		// The first attempt has no author. Later attribution must supply octocat.
		{orgID, uuid.New(), repositoryID, iteratedPRID, "", "completed", "b1", "needs_human_review", nil, value(80), value(20), `[{"code":"blocking_findings"}]`, baseTime, baseTime.Add(4 * time.Minute)},
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "completed", "b1", "blocked", nil, value(90), value(30), `[{"code":"blocking_findings"},{"code":"lines_limit_exceeded"}]`, baseTime.Add(time.Minute), baseTime.Add(5 * time.Minute)},
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "failed", "b2", "", nil, nil, nil, `[]`, baseTime.Add(2 * time.Minute), time.Time{}},
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "stale", "b2", "", nil, nil, nil, `[]`, baseTime.Add(3 * time.Minute), time.Time{}},
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "completed", "b2", "comment_only", nil, value(140), value(40), `[{"code":"blocking_findings"}]`, baseTime.Add(4 * time.Minute), baseTime.Add(6 * time.Minute)},
		// Approval completes after the cohort window but stays in the cohort.
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "completed", "b3", "approved", &postedReviewID, value(240), value(60), `[]`, baseTime.Add(5 * time.Minute), cohortEnd.Add(time.Hour)},
		// A later head must not affect the representative assessment or reasons.
		{orgID, uuid.New(), repositoryID, iteratedPRID, "octocat", "completed", "b4", "blocked", nil, value(400), value(100), `[{"code":"checks_failing"}]`, cohortEnd.Add(2 * time.Hour), cohortEnd.Add(3 * time.Hour)},
		{orgID, uuid.New(), repositoryID, internalApprovalPRID, "carol", "completed", "c1", "approved", nil, value(35), value(15), `[{"code":"checks_failing"}]`, baseTime, baseTime.Add(7 * time.Minute)},
		{orgID, uuid.New(), repositoryID, operationalPRID, "", "failed", "d1", "", nil, nil, nil, `[]`, baseTime, time.Time{}},
		{orgID, uuid.New(), repositoryID, operationalPRID, "", "stale", "d2", "", nil, nil, nil, `[]`, baseTime.Add(time.Minute), time.Time{}},
		{otherOrgID, uuid.New(), otherRepositoryID, otherPRID, "intruder", "completed", "x1", "approved", &postedReviewID, value(8), value(2), `[]`, baseTime, baseTime.Add(time.Minute)},
	}

	for _, fact := range facts {
		_, insertErr := conn.Exec(ctx, `
			INSERT INTO sessions (id, org_id, revision_context)
			VALUES ($1, $2, jsonb_build_object('pull_request_author', $3::text))`,
			fact.sessionID, fact.orgID, fact.author,
		)
		require.NoError(t, insertErr, "test should insert each PR-journey session")

		var completedAt *time.Time
		if !fact.completedAt.IsZero() {
			completedAt = &fact.completedAt
		}
		_, insertErr = conn.Exec(ctx, `
			INSERT INTO code_review_session_metadata (
				id, org_id, session_id, repository_id, pull_request_id,
				status, head_sha, decision, github_review_id, additions,
				deletions, risk_reason_details, completed_at, created_at
			) VALUES (
				gen_random_uuid(), $2, $1, $3, $4, $5, $6, NULLIF($7::text, ''),
				$8, $9, $10, $11::jsonb, $12::timestamptz, $13::timestamptz
			)`,
			fact.sessionID, fact.orgID, fact.repositoryID, fact.pullRequestID,
			fact.status, fact.headSHA, fact.decision, fact.githubReviewID,
			fact.additions, fact.deletions, fact.reasons,
			completedAt, fact.createdAt,
		)
		require.NoError(t, insertErr, "test should insert each PR-journey review fact")
	}

	_, err = conn.Exec(ctx, `
		UPDATE sessions
		SET revision_context = revision_context || jsonb_build_object(
			'github_delivery_id', CASE id
				WHEN $1 THEN 'delivery-alice'
				WHEN $2 THEN 'delivery-alice'
				WHEN $3 THEN 'delivery-reviewer-bob-1'
				WHEN $4 THEN 'delivery-reviewer-bob-2'
				WHEN $5 THEN 'delivery-other-org'
			END,
			'request_context', jsonb_build_object(
				'source', 'issue_comment',
				'author_login', CASE id
					WHEN $1 THEN 'Alice'
					WHEN $2 THEN 'alice'
					WHEN $3 THEN 'reviewer-bob'
					WHEN $4 THEN 'reviewer-bob'
					WHEN $5 THEN 'intruder'
				END
			)
		)
		WHERE id = ANY($6::uuid[])`,
		facts[0].sessionID, facts[1].sessionID, facts[4].sessionID,
		facts[8].sessionID, facts[13].sessionID,
		[]uuid.UUID{facts[0].sessionID, facts[1].sessionID, facts[4].sessionID, facts[8].sessionID, facts[13].sessionID},
	)
	require.NoError(t, err, "test should mark direct comment requests and a redelivery in session audit context")

	iteratedApprovalSessionID := facts[8].sessionID
	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_findings (org_id, session_id, severity) VALUES
			($1, $2, 'low'), ($1, $3, 'high'), ($4, $5, 'critical')`,
		orgID, facts[1].sessionID, iteratedApprovalSessionID, otherOrgID, facts[13].sessionID,
	)
	require.NoError(t, err, "test should insert representative and cross-tenant findings")

	analytics, err := NewCodeReviewStore(conn).GetReviewAnalytics(ctx, orgID, CodeReviewAnalyticsFilters{
		RepositoryID:  &repositoryID,
		CreatedAfter:  &cohortStart,
		CreatedBefore: &cohortEnd,
	})
	require.NoError(t, err, "PR-journey analytics should execute against PostgreSQL")

	two := 2.0
	thirtyFive, fifteen := 35.0, 15.0
	require.Equal(t, models.CodeReviewAnalyticsSummary{
		PRsReviewed: 4, PRsWithCompletedRound: 3, ApprovedBy143: 2, NotApproved: 1,
		ApprovedFirstRound: 1, MedianRoundsToApproval: &two,
		PRsWithFailedAttempt: 2, PRsWithStaleAttempt: 2,
		PRsWithChangeBreakdown: 3,
		MedianAdditions:        &thirtyFive,
		MedianDeletions:        &fifteen,
		PRsWithFindings:        2, PRsWithBlockingFindings: 1, TotalFindings: 2,
		ApprovalNotPosted: 1,
	}, analytics.Summary, "summary should derive unique PR outcomes from representative rounds")
	require.Equal(t, []models.CodeReviewApprovalRoundAnalytics{
		{Bucket: models.CodeReviewApprovalRound1, PRs: 1},
		{Bucket: models.CodeReviewApprovalRound2, PRs: 0},
		{Bucket: models.CodeReviewApprovalRound3, PRs: 1},
		{Bucket: models.CodeReviewApprovalRound4Plus, PRs: 0},
		{Bucket: models.CodeReviewApprovalRoundNotYet, PRs: 2},
	}, analytics.ApprovalRounds, "approval distribution should ignore duplicate heads and post-approval rounds")
	require.Equal(t, []models.CodeReviewNonApprovalReasonAnalytics{
		{Code: models.CodeReviewRiskReasonBlockingFindings, PRs: 1},
		{Code: models.CodeReviewRiskReasonChecksFailing, PRs: 1},
		{Code: models.CodeReviewRiskReasonLinesLimitExceeded, PRs: 1},
	}, analytics.NonApprovalReasons, "reasons should deduplicate per PR and exclude post-approval rounds")
	require.Equal(t, map[string]int64{"Unknown": 1, "alice": 1, "carol": 1, "octocat": 1}, func() map[string]int64 {
		authors := make(map[string]int64, len(analytics.Authors))
		for _, author := range analytics.Authors {
			authors[author.Author] = author.PRsReviewed
		}
		return authors
	}(), "author aggregation should use the first captured author and preserve Unknown exactly once")
	require.Equal(t, int64(3), analytics.CommentRequestsTotal, "comment request total should deduplicate GitHub redeliveries within a PR")
	require.Equal(t, []models.CodeReviewCommentRequestUserAnalytics{
		{GitHubLogin: "reviewer-bob", Requests: 2},
		{GitHubLogin: "alice", Requests: 1},
	}, analytics.CommentRequestsByUser, "comment requests should group case-insensitive GitHub logins and exclude other tenants")
}

// The rank a page cursor anchors on is computed in Go while the rank the
// ORDER BY applies is computed in SQL. TestCodeReviewSortRankSQLCoversTheSame
// Branches only proves both sides use the same set of rank values; this proves
// they assign the same rank to the same row. Without it, transposing two
// branches on one side alone would quietly anchor cursors in the wrong label
// group and drop rows from the sorted list.
//
//nolint:paralleltest // sort subtests share one PostgreSQL connection and isolated schema, so they must run serially
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
