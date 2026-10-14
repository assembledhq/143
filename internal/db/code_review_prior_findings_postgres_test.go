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

func TestCodeReviewStore_ListPublishedFindingsForPullRequestPostgres(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the PostgreSQL prior findings test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer func() {
		require.NoError(t, conn.Close(context.Background()), "test should close the PostgreSQL connection")
	}()

	schema := "test_code_review_prior_findings_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated schema")
	defer func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate prior findings objects")

	_, err = conn.Exec(ctx, `
		CREATE TABLE code_review_session_metadata (
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			pull_request_id uuid NOT NULL,
			head_sha text NOT NULL
		);
		CREATE TABLE code_review_findings (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			agent_result_id uuid,
			dedupe_key text NOT NULL,
			severity text NOT NULL,
			confidence text NOT NULL,
			path text,
			start_line integer,
			end_line integer,
			summary text NOT NULL,
			body text NOT NULL,
			selected_for_inline boolean NOT NULL,
			github_comment_id bigint,
			created_at timestamptz NOT NULL
		);
		CREATE TABLE code_review_decision_disputes (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			github_thread_root_comment_id bigint,
			body text NOT NULL,
			filed_by_login text NOT NULL,
			author_is_pr_author boolean NOT NULL,
			created_at timestamptz NOT NULL
		);
	`)
	require.NoError(t, err, "test should create the prior findings schema")

	orgID := uuid.New()
	otherOrgID := uuid.New()
	pullRequestID := uuid.New()
	otherPullRequestID := uuid.New()
	firstSessionID := uuid.New()
	secondSessionID := uuid.New()
	currentSessionID := uuid.New()
	otherPullRequestSessionID := uuid.New()
	otherOrgSessionID := uuid.New()
	base := time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)

	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_session_metadata (org_id, session_id, pull_request_id, head_sha) VALUES
			($1, $3, $6, 'first-head'),
			($1, $4, $6, 'second-head'),
			($1, $5, $6, 'current-head'),
			($1, $7, $8, 'other-pr-head'),
			($2, $9, $6, 'other-org-head')`,
		orgID, otherOrgID, firstSessionID, secondSessionID, currentSessionID, pullRequestID, otherPullRequestSessionID, otherPullRequestID, otherOrgSessionID)
	require.NoError(t, err, "test should insert review sessions")

	firstFindingID := uuid.New()
	secondFindingID := uuid.New()
	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_findings
			(id, org_id, session_id, dedupe_key, severity, confidence, path, start_line, end_line, summary, body, selected_for_inline, github_comment_id, created_at)
		VALUES
			($1, $3, $5, 'raw', 'high', 'high', 'export.go', 52, 52, 'Escaping breaks machine consumers', 'Give API exports raw values.', true, 101, $10),
			($2, $3, $6, 'escape', 'high', 'high', 'handlers.go', 12, 12, 'Raw exports allow formulas', 'Escape API exports.', true, 201, $11),
			($12, $3, $6, 'advisory', 'low', 'medium', 'handlers.go', 30, 30, 'Unposted advisory', 'Never published inline.', false, NULL, $11),
			($13, $3, $7, 'current', 'high', 'high', 'handlers.go', 40, 40, 'Current session finding', 'Excluded session.', true, 301, $11),
			($14, $3, $8, 'other-pr', 'high', 'high', 'other.go', 1, 1, 'Other pull request finding', 'Different PR.', true, 401, $11),
			($15, $4, $9, 'other-org', 'high', 'high', 'export.go', 1, 1, 'Other org finding', 'Different org.', true, 501, $11)`,
		firstFindingID, secondFindingID, orgID, otherOrgID, firstSessionID, secondSessionID, currentSessionID, otherPullRequestSessionID, otherOrgSessionID,
		base, base.Add(time.Hour), uuid.New(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err, "test should insert findings")

	_, err = conn.Exec(ctx, `
		INSERT INTO code_review_decision_disputes
			(id, org_id, session_id, github_thread_root_comment_id, body, filed_by_login, author_is_pr_author, created_at)
		VALUES
			($1, $4, $6, 101, 'First reply.', 'amy', true, $7),
			($2, $4, $6, 101, 'Fixed: API exports now return raw values.', 'amy', true, $8),
			($3, $5, $6, 101, 'Other org reply.', 'mallory', false, $9)`,
		uuid.New(), uuid.New(), uuid.New(), orgID, otherOrgID, firstSessionID, base.Add(10*time.Minute), base.Add(20*time.Minute), base.Add(30*time.Minute))
	require.NoError(t, err, "test should insert thread replies")

	store := NewCodeReviewStore(conn)
	found, err := store.ListPublishedFindingsForPullRequest(ctx, orgID, pullRequestID, currentSessionID, 10)
	require.NoError(t, err, "listing prior published findings should succeed")

	stringPtr := func(value string) *string { return &value }
	intPtr := func(value int) *int { return &value }
	int64Ptr := func(value int64) *int64 { return &value }
	boolPtr := func(value bool) *bool { return &value }
	expected := []models.CodeReviewPriorFinding{
		{
			CodeReviewFinding: models.CodeReviewFinding{
				ID: secondFindingID, OrgID: orgID, SessionID: secondSessionID, DedupeKey: "escape",
				Severity: models.CodeReviewFindingSeverity("high"), Confidence: models.CodeReviewFindingConfidence("high"),
				Path: stringPtr("handlers.go"), StartLine: intPtr(12), EndLine: intPtr(12),
				Summary: "Raw exports allow formulas", Body: "Escape API exports.", SelectedForInline: true,
				GitHubCommentID: int64Ptr(201), CreatedAt: base.Add(time.Hour),
			},
			ReviewedHeadSHA: "second-head",
		},
		{
			CodeReviewFinding: models.CodeReviewFinding{
				ID: firstFindingID, OrgID: orgID, SessionID: firstSessionID, DedupeKey: "raw",
				Severity: models.CodeReviewFindingSeverity("high"), Confidence: models.CodeReviewFindingConfidence("high"),
				Path: stringPtr("export.go"), StartLine: intPtr(52), EndLine: intPtr(52),
				Summary: "Escaping breaks machine consumers", Body: "Give API exports raw values.", SelectedForInline: true,
				GitHubCommentID: int64Ptr(101), CreatedAt: base,
			},
			ReviewedHeadSHA:       "first-head",
			ReplyBody:             stringPtr("Fixed: API exports now return raw values."),
			ReplyAuthorLogin:      stringPtr("amy"),
			ReplyAuthorIsPRAuthor: boolPtr(true),
		},
	}
	for i := range found {
		found[i].CreatedAt = found[i].CreatedAt.UTC()
	}
	require.Equal(t, expected, found, "should return only this org and PR's published findings from other sessions, newest first, with the latest same-org thread reply")

	limited, err := store.ListPublishedFindingsForPullRequest(ctx, orgID, pullRequestID, currentSessionID, 1)
	require.NoError(t, err, "listing with a limit should succeed")
	limitedIDs := make([]uuid.UUID, 0, len(limited))
	for _, finding := range limited {
		limitedIDs = append(limitedIDs, finding.ID)
	}
	require.Equal(t, []uuid.UUID{secondFindingID}, limitedIDs, "limit should keep only the newest published finding")
}

func TestCodeReviewStore_ListPublishedFindingsForPullRequestSkipsQueryWithoutLimit(t *testing.T) {
	t.Parallel()

	found, err := NewCodeReviewStore(nil).ListPublishedFindingsForPullRequest(context.Background(), uuid.New(), uuid.New(), uuid.New(), 0)

	require.NoError(t, err, "a non-positive limit should not error")
	require.Equal(t, []models.CodeReviewPriorFinding{}, found, "a non-positive limit should return no prior findings without querying")
}
