package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewPriorFindingsForPrompt(t *testing.T) {
	t.Parallel()

	stringPtr := func(value string) *string { return &value }
	intPtr := func(value int) *int { return &value }
	boolPtr := func(value bool) *bool { return &value }
	longReply := strings.Repeat("é", codeReviewPriorFindingTextLimit+10)

	tests := []struct {
		name     string
		input    []models.CodeReviewPriorFinding
		expected []prompts.CodeReviewPriorFindingPromptData
	}{
		{
			name:     "returns an empty list without prior findings",
			expected: []prompts.CodeReviewPriorFindingPromptData{},
		},
		{
			name: "maps a line finding and the pull request author's reply",
			input: []models.CodeReviewPriorFinding{{
				CodeReviewFinding: models.CodeReviewFinding{
					Severity: models.CodeReviewFindingSeverity("high"), Path: stringPtr("export.go"), StartLine: intPtr(52),
					Summary: "Escaping breaks machine consumers", Body: "  Give API exports raw values.  ",
				},
				ReviewedHeadSHA:       "5eefa36f",
				ReplyBody:             stringPtr("Fixed in 3a8a5c21."),
				ReplyAuthorLogin:      stringPtr("amy-assembled"),
				ReplyAuthorIsPRAuthor: boolPtr(true),
			}},
			expected: []prompts.CodeReviewPriorFindingPromptData{{
				ReviewedHeadSHA: "5eefa36f", Severity: "high", Location: "export.go:52",
				Summary: "Escaping breaks machine consumers", Body: "Give API exports raw values.",
				ReplyAuthor: "amy-assembled", ReplyIsPRAuthor: true, ReplyBody: "Fixed in 3a8a5c21.",
			}},
		},
		{
			name: "keeps a file-level finding without a reply and truncates long text by rune",
			input: []models.CodeReviewPriorFinding{{
				CodeReviewFinding: models.CodeReviewFinding{
					Severity: models.CodeReviewFindingSeverity("medium"), Path: stringPtr("handlers.go"),
					Summary: "Timeout too short", Body: longReply,
				},
				ReviewedHeadSHA: "920c6a5e",
			}},
			expected: []prompts.CodeReviewPriorFindingPromptData{{
				ReviewedHeadSHA: "920c6a5e", Severity: "medium", Location: "handlers.go",
				Summary: "Timeout too short", Body: strings.Repeat("é", codeReviewPriorFindingTextLimit) + "…",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codeReviewPriorFindingsForPrompt(tt.input), "prior findings should map to bounded prompt data")
		})
	}
}

func TestListCodeReviewPriorFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		pullRequestID uuid.UUID
		setupMock     func(mock pgxmock.PgxPoolIface, orgID, pullRequestID, sessionID uuid.UUID, now time.Time)
		expected      []models.CodeReviewPriorFinding
		expectErr     string
	}{
		{
			name:      "skips the lookup when the review has no pull request",
			setupMock: func(pgxmock.PgxPoolIface, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) {},
		},
		{
			name:          "loads published findings from other sessions on the pull request",
			pullRequestID: uuid.New(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, pullRequestID, sessionID uuid.UUID, now time.Time) {
				mock.ExpectQuery(`(?s)FROM code_review_session_metadata m.*JOIN code_review_findings f.*code_review_decision_disputes d.*m\.session_id <> @exclude_session_id.*f\.github_comment_id IS NOT NULL`).
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "exclude_session_id": sessionID, "limit": codeReviewPriorFindingPromptLimit}).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity", "confidence", "path", "start_line", "end_line",
						"summary", "body", "selected_for_inline", "github_comment_id", "created_at",
						"reviewed_head_sha", "reply_body", "reply_author_login", "reply_author_is_pr_author",
					}).AddRow(
						uuid.Nil, orgID, uuid.Nil, nil, "raw", models.CodeReviewFindingSeverity("high"), models.CodeReviewFindingConfidence("high"), nil, nil, nil,
						"Escaping breaks machine consumers", "Give API exports raw values.", true, nil, now,
						"5eefa36f", nil, nil, nil,
					))
			},
			expected: []models.CodeReviewPriorFinding{{
				CodeReviewFinding: models.CodeReviewFinding{
					DedupeKey: "raw", Severity: models.CodeReviewFindingSeverity("high"), Confidence: models.CodeReviewFindingConfidence("high"),
					Summary: "Escaping breaks machine consumers", Body: "Give API exports raw values.", SelectedForInline: true,
				},
				ReviewedHeadSHA: "5eefa36f",
			}},
		},
		{
			name:          "returns store failures so the review retries",
			pullRequestID: uuid.New(),
			setupMock: func(mock pgxmock.PgxPoolIface, _, _, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery(`FROM code_review_session_metadata m`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("connection refused"))
			},
			expectErr: "list prior code review findings: list published code review findings for pull request: connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			orgID := uuid.New()
			sessionID := uuid.New()
			now := time.Date(2026, 10, 5, 18, 53, 52, 0, time.UTC)
			tt.setupMock(mock, orgID, tt.pullRequestID, sessionID, now)

			found, err := listCodeReviewPriorFindings(context.Background(), &Stores{CodeReviews: db.NewCodeReviewStore(mock)},
				runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}, models.CodeReviewSessionMetadata{PullRequestID: tt.pullRequestID})

			if tt.expectErr != "" {
				require.EqualError(t, err, tt.expectErr, "store failures should propagate with context")
			} else {
				require.NoError(t, err, "loading prior findings should succeed")
				for i := range tt.expected {
					tt.expected[i].OrgID = orgID
					tt.expected[i].CreatedAt = now
				}
				require.Equal(t, tt.expected, found, "should return the store's prior findings")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the prior findings lookup should run only when the review has a pull request")
		})
	}
}
