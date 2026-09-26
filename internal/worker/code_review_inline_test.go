package worker

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewInlineCommentsAnchorsOverlappingRange(t *testing.T) {
	t.Parallel()

	path := "assets/src/components/forecasts/ForecastEvaluation/ForecastEvaluation.tsx"
	files := []codereview.PullRequestFile{{Filename: path, Patch: `@@ -358,5 +348,7 @@
                     end={end.toSeconds()}
                     onDateRangeSelected={(update) => {
                       if (update.range) {
-                        setStart(start);
-                        setEnd(end);
+                        onDateRangeChange(
+                          start,
+                          end
+                        );`}}
	findings := []models.CodeReviewFinding{{
		Path: &path, StartLine: intPtr(347), EndLine: intPtr(354),
		Severity: models.CodeReviewFindingSeverityHigh, Body: "DatePicker mount resets the interval",
		DedupeKey: "mount-resets-interval",
	}}

	selected := codeReviewFindingsOnChangedLines(findings, files)
	require.Equal(t, findings, selected, "a finding whose range overlaps changed lines should remain eligible")
	require.Equal(t, []codereview.SubmitReviewComment{{
		Path: path, Line: 351, Body: "[P1] DatePicker mount resets the interval", DedupeKey: "mount-resets-interval",
	}}, codeReviewInlineComments(selected, files), "publication should anchor to the first changed line within the finding range")
}

func TestCodeReviewInlineCommentAnchors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patch    string
		start    int
		end      int
		expected int
	}{
		{name: "start already changed", patch: "@@ -10 +10 @@\n-old\n+new", start: 10, end: 12, expected: 10},
		{name: "range starts in context", patch: "@@ -10,2 +10,2 @@\n context\n-old\n+new", start: 10, end: 11, expected: 11},
		{name: "range spans hunks", patch: "@@ -10 +10 @@\n-old\n+new\n@@ -30 +30 @@\n-old\n+new", start: 11, end: 31, expected: 30},
		{name: "earliest changed line wins", patch: "@@ -10,2 +10,2 @@\n-old\n-old\n+new\n+new", start: 9, end: 12, expected: 10},
		{name: "context only stays in summary", patch: "@@ -10,2 +10,2 @@\n context\n-old\n+new", start: 10, end: 10},
		{name: "outside hunks stays in summary", patch: "@@ -10 +10 @@\n-old\n+new", start: 1, end: 9},
		{name: "missing patch stays in summary", start: 10, end: 12},
		{name: "removed file stays in summary", patch: "@@ -1,2 +0,0 @@\n-old\n-old", start: 1, end: 2},
		{name: "header is not an addition", patch: "--- a/file.go\n+++ b/file.go", start: 1, end: 2},
		{name: "plus-prefixed code is an addition", patch: "@@ -10,2 +10,2 @@\n---old\n+one\n---old\n+++two", start: 11, end: 11, expected: 11},
		{name: "no newline marker does not advance line", patch: "@@ -10,2 +10,2 @@\n-old\n+one\n\\ No newline at end of file\n-old\n+two", start: 11, end: 11, expected: 11},
		{name: "does not read beyond hunk length", patch: "@@ -10 +10 @@\n-old\n+new\n+not in hunk", start: 11, end: 11},
		{name: "malformed hunk does not reuse earlier line", patch: "@@ -10,2 +10,2 @@\n-old\n+new\n@@ malformed @@\n+not a hunk", start: 11, end: 11},
		{name: "invalid hunk body stops counting", patch: "@@ -10,2 +10,2 @@\ninvalid\n+not a hunk", start: 10, end: 11},
		{name: "zero length right side has no anchor", patch: "@@ -10 +9,0 @@\n-old", start: 9, end: 10},
		{name: "huge model range is bounded by diff", patch: "@@ -10 +10 @@\n-old\n+new", start: 1, end: math.MaxInt, expected: 10},
		{name: "missing end uses start", patch: "@@ -10 +10 @@\n-old\n+new", start: 10, expected: 10},
		{name: "reversed end uses start", patch: "@@ -10 +10 @@\n-old\n+new", start: 10, end: 9, expected: 10},
		{name: "invalid start cannot be anchored", patch: "@@ -10 +10 @@\n-old\n+new", end: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := "file.go"
			finding := models.CodeReviewFinding{
				Path: &path, StartLine: intPtr(tt.start), EndLine: intPtr(tt.end),
				Severity: models.CodeReviewFindingSeverityHigh, Summary: "Unresolved blocker", Body: "Fix this defect",
				DedupeKey: "stable-finding-key", SelectedForInline: true,
			}
			files := []codereview.PullRequestFile{{Filename: path, Patch: tt.patch}}
			findings := []models.CodeReviewFinding{finding}
			expected := []codereview.SubmitReviewComment{}
			if tt.expected > 0 {
				expected = append(expected, codereview.SubmitReviewComment{Path: path, Line: tt.expected, Body: "[P1] Fix this defect", DedupeKey: "stable-finding-key"})
			}
			require.Equal(t, expected, codeReviewInlineComments(findings, files), "only an actual changed line within the finding range may be sent to GitHub")
			require.Equal(t, tt.start, *findings[0].StartLine, "anchor mapping must not change the stored start line")
			require.Equal(t, tt.end, *findings[0].EndLine, "anchor mapping must not change the stored end line")
			require.Equal(t, "stable-finding-key", findings[0].DedupeKey, "anchor mapping must not change the finding identity")
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
				Decision: models.CodeReviewDecisionNeedsHumanReview, Findings: findings,
			})
			require.Contains(t, body, "Unresolved blocker", "blocking findings must remain in the summary even without a valid inline anchor")
		})
	}
}

func TestMarkPostedCodeReviewFindingsWithMappedAnchor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		line          int
		dedupeKey     string
		body          string
		expectReceipt bool
	}{
		{name: "mapped anchor and stable key", line: 351, dedupeKey: "stable-key", expectReceipt: true},
		{name: "existing marked thread retains its original anchor", line: 347, dedupeKey: "stable-key", expectReceipt: true},
		{name: "unmarked receipt matches mapped anchor and body", line: 351, body: "[P1] Fix this defect", expectReceipt: true},
		{name: "unmarked original start cannot match", line: 347, body: "[P1] Fix this defect"},
		{name: "different key cannot match even with same body", line: 351, dedupeKey: "different-key", body: "[P1] Fix this defect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated receipt store")
			defer mock.Close()
			orgID, findingID, sessionID := uuid.New(), uuid.New(), uuid.New()
			path := "file.go"
			finding := models.CodeReviewFinding{
				ID: findingID, Path: &path, StartLine: intPtr(347), EndLine: intPtr(354),
				Severity: models.CodeReviewFindingSeverityHigh, Body: "Fix this defect", DedupeKey: "stable-key",
			}
			if tt.expectReceipt {
				mock.ExpectQuery("(?s)UPDATE code_review_findings.*WHERE org_id = @org_id.*id = @id").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "id": findingID, "github_comment_id": int64(123)}).
					WillReturnRows(newCodeReviewFindingRows().AddRow(
						findingID, orgID, sessionID, nil, "stable-key", "high", "high", &path, intPtr(347), intPtr(354),
						"Blocker", "Fix this defect", true, new(int64(123)), time.Now().UTC(),
					))
			}
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			markPostedCodeReviewFindings(logger.WithContext(context.Background()), db.NewCodeReviewStore(mock), orgID,
				[]models.CodeReviewFinding{finding},
				[]codereview.PullRequestFile{{Filename: path, Patch: "@@ -351 +351 @@\n-old\n+new"}},
				[]codereview.SubmitReviewPostedComment{{ID: 123, Path: path, Line: tt.line, Body: tt.body, DedupeKey: tt.dedupeKey}},
			)
			require.NoError(t, mock.ExpectationsWereMet(), "the original tenant-scoped finding should receive only its own GitHub receipt")
			require.Empty(t, logs.String(), "receipt reconciliation should not issue unexpected writes or discard persistence errors")
		})
	}
}

func TestCodeReviewDeadLetterReasonDoesNotClaimRetryExhaustion(t *testing.T) {
	t.Parallel()
	err := &ghservice.GitHubAPIError{
		Method: http.MethodPost, Path: "/repos/acme/repo/pulls/42/comments", StatusCode: http.StatusUnprocessableEntity,
		Body: []byte(`{"message":"Validation Failed","errors":[{"field":"pull_request_review_thread.line","message":"could not be resolved"}]}`),
	}
	require.Equal(t, "code review job failed: GitHub API POST /repos/acme/repo/pulls/42/comments returned 422",
		codeReviewDeadLetterReason(err), "a terminal validation failure should not imply that automatic retries occurred")
}
