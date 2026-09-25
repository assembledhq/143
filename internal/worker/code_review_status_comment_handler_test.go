package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSyncCodeReviewStatusCommentHandlerRendersCurrentDurableState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                      string
		schedulingEnabled         bool
		initialStatus             models.CodeReviewSessionStatus
		lockedStatus              models.CodeReviewSessionStatus
		lockedFinalBody           *string
		lockedReviewID            *int64
		previousFinalBody         *string
		previousHeadSHA           string
		storedCommentID           any
		expectedExistingCommentID *int64
		expectedBody              string
		expectedAdditionalBody    string
		expectReassessmentHistory bool
		expectedCalls             []string
		hideErr                   error
		expectErr                 bool
	}{
		{
			name:              "announces running review with session link",
			schedulingEnabled: true,
			initialStatus:     models.CodeReviewSessionStatusRunning,
			lockedStatus:      models.CodeReviewSessionStatusRunning,
			expectedBody:      "143 Code Reviewer has started reviewing this pull request.",
			expectedCalls:     []string{"upsert"},
		},
		{
			name:            "publishes durable provisional blockers while review continues",
			initialStatus:   models.CodeReviewSessionStatusRunning,
			lockedStatus:    models.CodeReviewSessionStatusRunning,
			lockedFinalBody: statusCommentStringPtr(models.CodeReviewProvisionalReviewHeading + "\n\nSubstantive review is still running."),
			expectedBody:    models.CodeReviewProvisionalReviewHeading + "\n\nSubstantive review is still running.",
			expectedCalls:   []string{"upsert"},
		},
		{
			name:            "does not expose a terminal body before the decision commits",
			initialStatus:   models.CodeReviewSessionStatusRunning,
			lockedStatus:    models.CodeReviewSessionStatusRunning,
			lockedFinalBody: statusCommentStringPtr("❌ 143 Code Reviewer needs human review."),
			expectedBody:    "143 Code Reviewer has started reviewing this pull request.",
			expectedCalls:   []string{"upsert"},
		},
		{
			name:                      "keeps the previous verdict visible during reassessment",
			initialStatus:             models.CodeReviewSessionStatusRunning,
			lockedStatus:              models.CodeReviewSessionStatusRunning,
			previousFinalBody:         statusCommentStringPtr("❌ **143 Code Reviewer needs human review**\n\n**Why:** Sensitive workflow changes require a human decision."),
			previousHeadSHA:           "previous-head-sha",
			expectedBody:              "❌ **143 Code Reviewer needs human review**\n\n**Why:** Sensitive workflow changes require a human decision.",
			expectedAdditionalBody:    "<summary>Review history</summary>",
			expectReassessmentHistory: true,
			expectedCalls:             []string{"upsert"},
		},
		{
			name:                      "adds provisional blockers without hiding the previous verdict",
			initialStatus:             models.CodeReviewSessionStatusRunning,
			lockedStatus:              models.CodeReviewSessionStatusRunning,
			lockedFinalBody:           statusCommentStringPtr(models.CodeReviewProvisionalReviewHeading + "\n\nSubstantive review is still running."),
			previousFinalBody:         statusCommentStringPtr("❌ **143 Code Reviewer needs human review**\n\n**Why:** Sensitive workflow changes require a human decision."),
			previousHeadSHA:           "previous-head-sha",
			expectedBody:              models.CodeReviewProvisionalReviewHeading + "\n\nSubstantive review is still running.",
			expectedAdditionalBody:    "❌ **143 Code Reviewer needs human review**",
			expectReassessmentHistory: true,
			expectedCalls:             []string{"upsert"},
		},
		{
			name:                      "refreshes state under lock before publishing completed result",
			initialStatus:             models.CodeReviewSessionStatusRunning,
			lockedStatus:              models.CodeReviewSessionStatusCompleted,
			lockedFinalBody:           statusCommentStringPtr("143 Code Reviewer approved this PR.\n\nWhy: The change met policy."),
			lockedReviewID:            statusCommentInt64Ptr(143),
			storedCommentID:           int64(7331),
			expectedExistingCommentID: statusCommentInt64Ptr(7331),
			expectedBody:              "143 Code Reviewer approved this PR.",
			expectedCalls:             []string{"upsert", "hide"},
		},
		{
			name:                      "retries when hiding fallback summary fails after publishing completed result",
			initialStatus:             models.CodeReviewSessionStatusRunning,
			lockedStatus:              models.CodeReviewSessionStatusCompleted,
			lockedFinalBody:           statusCommentStringPtr("143 Code Reviewer did not approve this PR."),
			lockedReviewID:            statusCommentInt64Ptr(143),
			storedCommentID:           int64(7331),
			expectedExistingCommentID: statusCommentInt64Ptr(7331),
			expectedBody:              "143 Code Reviewer did not approve this PR.",
			expectedCalls:             []string{"upsert", "hide"},
			hideErr:                   errors.New("github unavailable"),
			expectErr:                 true,
		},
		{
			name:                      "hides a published fallback when the review later fails",
			initialStatus:             models.CodeReviewSessionStatusRunning,
			lockedStatus:              models.CodeReviewSessionStatusFailed,
			lockedFinalBody:           statusCommentStringPtr("Stale visible recommendation."),
			lockedReviewID:            statusCommentInt64Ptr(143),
			storedCommentID:           int64(7331),
			expectedExistingCommentID: statusCommentInt64Ptr(7331),
			expectedBody:              "143 Code Reviewer could not complete this review.",
			expectedCalls:             []string{"upsert", "hide"},
		},
		{
			name:          "explains that a changed commit receives a fresh approvable assessment",
			initialStatus: models.CodeReviewSessionStatusStale,
			lockedStatus:  models.CodeReviewSessionStatusStale,
			expectedBody:  "A fresh assessment of the latest commit is queued automatically and can still approve the PR.",
			expectedCalls: []string{"upsert"},
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
			repositoryID := uuid.New()
			pullRequestID := uuid.New()
			policyID := uuid.New()
			metadataID := uuid.New()
			now := time.Date(2026, time.September, 25, 12, 23, 16, 0, time.UTC)
			metadataRows := func(status models.CodeReviewSessionStatus, finalBody *string, reviewID *int64) *pgxmock.Rows {
				var completedAt *time.Time
				if status == models.CodeReviewSessionStatusCompleted {
					completedAt = &now
				}
				return newCodeReviewMetadataRows().AddRow(
					metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
					"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
					status, nil, nil, nil, nil, nil, false, nil, nil, false, nil,
					"output-key", nil, reviewID, nil, finalBody, nil, completedAt, now,
				)
			}
			mock.ExpectQuery("(?s)SELECT .*FROM code_review_session_metadata").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
				WillReturnRows(metadataRows(tt.initialStatus, nil, nil))
			mock.ExpectQuery("(?s)FROM code_review_session_metadata.*pull_request_id = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
				WillReturnRows(metadataRows(tt.initialStatus, nil, nil))
			repository := models.Repository{
				ID: repositoryID, OrgID: orgID, IntegrationID: uuid.New(), FullName: "acme/repo",
				InstallationID: 99, Status: models.RepositoryStatusActive, Settings: json.RawMessage(`{}`),
				CreatedAt: now, UpdatedAt: now,
			}
			mock.ExpectQuery("(?s)FROM repositories.*WHERE id = @id AND org_id = @org_id").
				WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).
				WillReturnRows(workerRepositoryRows(repository))
			mock.ExpectQuery("(?s)FROM pull_requests.*WHERE id = @id AND org_id = @org_id").
				WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID}).
				WillReturnRows(pgxmock.NewRows(workerPullRequestColumns).
					AddRow(workerPullRequestRow(pullRequestID, sessionID, orgID, "acme/repo", "feature/review", now)...))
			mock.ExpectBegin()
			mock.ExpectExec("SET LOCAL lock_timeout").
				WillReturnResult(pgxmock.NewResult("SET", 0))
			mock.ExpectExec("SET LOCAL statement_timeout").
				WillReturnResult(pgxmock.NewResult("SET", 0))
			mock.ExpectExec("SET LOCAL idle_in_transaction_session_timeout").
				WillReturnResult(pgxmock.NewResult("SET", 0))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").
				WithArgs(pgx.NamedArgs{"lock_key": "code_review_status_comment:" + orgID.String() + ":" + pullRequestID.String()}).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("(?s)FROM code_review_session_metadata.*pull_request_id = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
				WillReturnRows(metadataRows(tt.lockedStatus, tt.lockedFinalBody, tt.lockedReviewID))
			if !codeReviewMetadataTerminal(tt.lockedStatus) {
				previousRows := newCodeReviewMetadataRows()
				if tt.previousFinalBody != nil {
					previousDecision := models.CodeReviewDecisionNeedsHumanReview
					previousRows.AddRow(
						uuid.New(), orgID, uuid.New(), repositoryID, pullRequestID, policyID,
						"base", tt.previousHeadSHA, false, models.CodeReviewTriggerSourceTeamReviewer,
						models.CodeReviewSessionStatusCompleted, nil, nil, nil, nil, nil, false, &previousDecision, nil, false, nil,
						"previous-output-key", nil, nil, nil, tt.previousFinalBody, nil, &now, now.Add(-time.Minute),
					)
				}
				mock.ExpectQuery("(?s)FROM code_review_session_metadata.*status = 'completed'.*decision IS NOT NULL").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
					WillReturnRows(previousRows)
			}
			mock.ExpectQuery("SELECT code_review_status_comment_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "id": pullRequestID}).
				WillReturnRows(pgxmock.NewRows([]string{"code_review_status_comment_id"}).AddRow(tt.storedCommentID))
			mock.ExpectQuery("(?s)UPDATE pull_requests.*code_review_status_comment_id = @comment_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "id": pullRequestID, "comment_id": int64(7331)}).
				WillReturnRows(pgxmock.NewRows([]string{"code_review_status_comment_id"}).AddRow(int64(7331)))
			if tt.expectErr {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}

			submitter := &statusCommentSubmitterStub{commentID: 7331, hideErr: tt.hideErr}
			payload, err := json.Marshal(codereviewsvc.SyncReviewStatusCommentJobPayload{
				OrgID: orgID, SessionID: sessionID, RepositoryID: repositoryID, PullRequestID: pullRequestID,
			})
			require.NoError(t, err, "status comment payload should marshal")

			err = newSyncCodeReviewStatusCommentHandler(&Stores{
				CodeReviews:  db.NewCodeReviewStore(mock),
				Repositories: db.NewRepositoryStore(mock),
				PullRequests: db.NewPullRequestStore(mock),
			}, &Services{
				CodeReviews:         submitter,
				CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: tt.schedulingEnabled},
				FrontendURL:         "https://143.test",
			}, zerolog.Nop())(context.Background(), models.JobTypeSyncCodeReviewStatusComment, payload)

			if tt.expectErr {
				require.Error(t, err, "status comment handler should retry when fallback summary hiding fails")
			} else {
				require.NoError(t, err, "status comment handler should synchronize the current review state")
			}
			require.Equal(t, int64(99), submitter.request.InstallationID, "status comment should use the repository installation")
			require.Equal(t, "acme/repo", submitter.request.Repository, "status comment should target the pull request repository")
			require.Equal(t, 42, submitter.request.PullNumber, "status comment should target the pull request number")
			require.Equal(t, tt.expectedExistingCommentID, submitter.request.ExistingCommentID, "status comment should use the durable GitHub comment id when available")
			require.Contains(t, submitter.request.Body, tt.expectedBody, "status comment should render the current durable outcome")
			if tt.expectedAdditionalBody != "" {
				require.Contains(t, submitter.request.Body, tt.expectedAdditionalBody, "status comment should retain the complete previous verdict during reassessment")
			}
			if tt.expectReassessmentHistory {
				expectedEntry := "- <relative-time datetime=\"2026-09-25T12:23:16Z\">Sep 25, 2026 at 8:23 AM EDT" +
					"</relative-time> — **Reassessment started** for `head` — [Follow the review session](https://143.test/sessions/" + sessionID.String() + ")"
				require.Contains(t, submitter.request.Body, expectedEntry, "reassessment history should identify when the active assessment started and link to its session")
				require.NotContains(t, submitter.request.Body, "143 Code Reviewer is reassessing this pull request", "reassessment status should appear in history instead of a standalone paragraph")
				require.NotContains(t, submitter.request.Body, "remains visible until the new review finishes", "reassessment history should replace the redundant visibility explanation")
			}
			require.Contains(t, submitter.request.Body, "https://143.test/sessions/"+sessionID.String(), "status comment should link to the review session")
			if tt.schedulingEnabled && codeReviewMetadataTerminal(tt.lockedStatus) && tt.lockedStatus != models.CodeReviewSessionStatusStale {
				require.Contains(t, submitter.request.Body, "[Request review](https://143.test/code-reviews?review_now="+sessionID.String()+")", "enabled worker publishes a usable review action for a terminal unsuccessful review")
			} else {
				require.NotContains(t, submitter.request.Body, "[Request review]", "unavailable scheduling or active review omits the action")
			}
			require.Equal(t, tt.expectedCalls, submitter.calls, "fallback summary should only be hidden after the rolling comment is published")
			if tt.lockedReviewID != nil {
				require.Equal(t, codereviewsvc.HideReviewSummaryRequest{
					InstallationID: 99,
					Repository:     "acme/repo",
					PullNumber:     42,
					ReviewID:       *tt.lockedReviewID,
					OutputKey:      "output-key",
				}, submitter.hideRequest, "completed review should hide the persisted fallback summary")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "status comment handler should use org-scoped review, repository, and pull request reads")
		})
	}
}

func TestSyncCodeReviewStatusCommentHandlerSkipsSupersededSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	oldSessionID := uuid.New()
	newSessionID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	now := time.Now().UTC()
	metadataRows := func(sessionID uuid.UUID, createdAt time.Time) *pgxmock.Rows {
		return newCodeReviewMetadataRows().AddRow(
			uuid.New(), orgID, sessionID, repositoryID, pullRequestID, policyID,
			"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
			models.CodeReviewSessionStatusRunning, nil, nil, nil, nil, nil, false, nil, nil, false, nil,
			"output-"+sessionID.String(), nil, nil, nil, nil, nil, nil, createdAt,
		)
	}
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": oldSessionID}).
		WillReturnRows(metadataRows(oldSessionID, now.Add(-time.Minute)))
	mock.ExpectQuery("(?s)FROM code_review_session_metadata.*pull_request_id = @pull_request_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(metadataRows(newSessionID, now))

	submitter := &statusCommentSubmitterStub{commentID: 7331}
	payload, err := json.Marshal(codereviewsvc.SyncReviewStatusCommentJobPayload{
		OrgID: orgID, SessionID: oldSessionID, RepositoryID: repositoryID, PullRequestID: pullRequestID,
	})
	require.NoError(t, err, "status comment payload should marshal")

	err = newSyncCodeReviewStatusCommentHandler(&Stores{
		CodeReviews: db.NewCodeReviewStore(mock),
	}, &Services{CodeReviews: submitter, FrontendURL: "https://143.test"}, zerolog.Nop())(
		context.Background(), models.JobTypeSyncCodeReviewStatusComment, payload,
	)

	require.NoError(t, err, "a delayed status sync should harmlessly skip a superseded review")
	require.Equal(t, codereviewsvc.UpsertReviewStatusCommentRequest{}, submitter.request, "an older review must not overwrite the current rolling comment")
	require.NoError(t, mock.ExpectationsWereMet(), "supersession check should stop before repository or GitHub work")
}

type statusCommentSubmitterStub struct {
	request     codereviewsvc.UpsertReviewStatusCommentRequest
	hideRequest codereviewsvc.HideReviewSummaryRequest
	commentID   int64
	err         error
	hideErr     error
	calls       []string
}

func (s *statusCommentSubmitterStub) SubmitReview(context.Context, codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error) {
	return codereviewsvc.SubmitReviewResult{}, nil
}

func (s *statusCommentSubmitterStub) UpsertReviewStatusComment(_ context.Context, request codereviewsvc.UpsertReviewStatusCommentRequest) (int64, error) {
	s.request = request
	s.calls = append(s.calls, "upsert")
	return s.commentID, s.err
}

func (s *statusCommentSubmitterStub) HideReviewSummary(_ context.Context, request codereviewsvc.HideReviewSummaryRequest) error {
	s.hideRequest = request
	s.calls = append(s.calls, "hide")
	return s.hideErr
}

func statusCommentStringPtr(value string) *string {
	return &value
}

func statusCommentInt64Ptr(value int64) *int64 {
	return &value
}

func TestCodeReviewStatusCommentReviewNowLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  models.CodeReviewSessionStatus
		enabled bool
		present bool
	}{
		{"running", models.CodeReviewSessionStatusRunning, true, false},
		{"queued", models.CodeReviewSessionStatusQueued, true, false},
		{"completed", models.CodeReviewSessionStatusCompleted, true, true},
		{"failed", models.CodeReviewSessionStatusFailed, true, true},
		{"cancelled", models.CodeReviewSessionStatusCancelled, true, true},
		{"superseded", models.CodeReviewSessionStatusStale, true, false},
		{"scheduling service unavailable", models.CodeReviewSessionStatusCompleted, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sessionID := uuid.MustParse("90d8a47d-d87e-4780-90af-040f5144685a")
			link := ""
			if tt.enabled {
				link = codeReviewNowURL("https://143.test/", sessionID)
			}
			body := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{SessionID: sessionID, Status: tt.status}, nil, "https://143.test/sessions/"+sessionID.String(), link)
			expected := "[Request review](https://143.test/code-reviews?review_now=" + sessionID.String() + ")"
			if tt.present {
				require.Contains(t, body, expected, "rolling comment links to the authenticated confirmation for its source session")
			} else {
				require.NotContains(t, body, "[Request review]", "unsupported or superseded comment must not advertise action")
			}
			require.NotContains(t, body, "Open 143 to request", "confirmation explanation belongs in the destination dialog")
			require.NotContains(t, body, "/api/", "comment link never directly invokes a mutation endpoint")
		})
	}
}

func TestCodeReviewStatusCommentFooterRoutes(t *testing.T) {
	t.Parallel()
	const detailURL = "https://143.test/sessions/current"
	const assessmentURL = "https://143.test/code-reviews?assessment=current"
	const reviewURL = "https://143.test/code-reviews?review_now=current"
	const evidenceURL = "https://143.test/code-reviews?recheck=baseline"
	const start = "<!-- 143-code-review-footer:start -->"
	const end = "<!-- 143-code-review-footer:end -->"
	const oldMarkedBody = "Preserved blocker details.\n\n" + start + "\n[Re-check evidence](" + evidenceURL + ") · [View full review](https://143.test/sessions/old)\n" + end
	const cancellationReason = "Review cancelled after three consecutive attempts could not publish on unchanged analysis inputs. Push a new revision or explicitly request a fresh review to retry."
	tests := []struct {
		name            string
		status          models.CodeReviewSessionStatus
		body            string
		statusMessage   string
		decision        models.CodeReviewDecision
		acceptable      bool
		assessment      bool
		noURLs          bool
		expectedContent string
		expectedLinks   string
	}{
		{name: "approved removes saved action", status: models.CodeReviewSessionStatusCompleted, body: oldMarkedBody, decision: models.CodeReviewDecisionApproved, expectedContent: "Preserved blocker details.", expectedLinks: "[View full review](" + detailURL + ")"},
		{name: "acceptable comment is view only", status: models.CodeReviewSessionStatusCompleted, body: oldMarkedBody, decision: models.CodeReviewDecisionNeedsHumanReview, acceptable: true, expectedContent: "Preserved blocker details.", expectedLinks: "[View full review](" + detailURL + ")"},
		{name: "completed evidence action takes priority", status: models.CodeReviewSessionStatusCompleted, body: oldMarkedBody, expectedContent: "Preserved blocker details.", expectedLinks: "[Re-check evidence](" + evidenceURL + ") · [View full review](" + detailURL + ")"},
		{name: "completed recheck uses assessment detail", status: models.CodeReviewSessionStatusCompleted, body: oldMarkedBody, assessment: true, expectedContent: "Preserved blocker details.", expectedLinks: "[Re-check evidence](" + evidenceURL + ") · [View assessment](" + assessmentURL + ")"},
		{name: "completed legacy body receives action footer", status: models.CodeReviewSessionStatusCompleted, body: "❌ **143 Code Reviewer needs human review**\n\nPreserved blocker details.\n\n[View the full review](https://143.test/sessions/old)", expectedContent: "❌ **143 Code Reviewer needs human review**\n\nPreserved blocker details.", expectedLinks: "[Request review](" + reviewURL + ") · [View full review](" + detailURL + ")"},
		{name: "missing completed body", status: models.CodeReviewSessionStatusCompleted, expectedContent: "143 Code Reviewer completed its review.", expectedLinks: "[Request review](" + reviewURL + ") · [View full review](" + detailURL + ")"},
		{name: "missing approved body", status: models.CodeReviewSessionStatusCompleted, decision: models.CodeReviewDecisionApproved, expectedContent: "143 Code Reviewer approved this PR.", expectedLinks: "[View full review](" + detailURL + ")"},
		{name: "failed discards staged result", status: models.CodeReviewSessionStatusFailed, body: oldMarkedBody, expectedContent: "143 Code Reviewer could not complete this review.", expectedLinks: "[Request review](" + reviewURL + ") · [View full review](" + detailURL + ")"},
		{name: "failed recheck has details only", status: models.CodeReviewSessionStatusFailed, body: oldMarkedBody, assessment: true, expectedContent: "143 Code Reviewer could not complete this review.", expectedLinks: "[View assessment](" + assessmentURL + ")"},
		{name: "cancelled discards saved evidence action", status: models.CodeReviewSessionStatusCancelled, body: oldMarkedBody, expectedContent: "This 143 code review was cancelled.", expectedLinks: "[Request review](" + reviewURL + ") · [View full review](" + detailURL + ")"},
		{name: "loop cancellation preserves reason before retry footer", status: models.CodeReviewSessionStatusCancelled, body: oldMarkedBody, statusMessage: " " + cancellationReason + " ", expectedContent: "This 143 code review was cancelled.\n\n" + cancellationReason, expectedLinks: "[Request review](" + reviewURL + ") · [View full review](" + detailURL + ")"},
		{name: "superseded is view only", status: models.CodeReviewSessionStatusStale, body: oldMarkedBody, expectedContent: "143 Code Reviewer superseded this assessment because the pull request code changed before publication. A fresh assessment of the latest commit is queued automatically and can still approve the PR.", expectedLinks: "[View full review](" + detailURL + ")"},
		{name: "active staged result remains hidden", status: models.CodeReviewSessionStatusRunning, body: oldMarkedBody, expectedContent: "143 Code Reviewer has started reviewing this pull request.", expectedLinks: "[Follow review](" + detailURL + ")"},
		{name: "queued uses follow", status: models.CodeReviewSessionStatusQueued, expectedContent: "143 Code Reviewer has started reviewing this pull request.", expectedLinks: "[Follow review](" + detailURL + ")"},
		{name: "active provisional strips actions", status: models.CodeReviewSessionStatusRunning, body: models.CodeReviewProvisionalReviewHeading + "\n\n" + oldMarkedBody, expectedContent: models.CodeReviewProvisionalReviewHeading + "\n\nPreserved blocker details.", expectedLinks: "[Follow review](" + detailURL + ")"},
		{name: "empty URLs omit footer", status: models.CodeReviewSessionStatusCompleted, body: "Preserved blocker details.", noURLs: true, expectedContent: "Preserved blocker details."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stored := tt.body
			metadata := models.CodeReviewSessionMetadata{Status: tt.status, StatusMessage: &tt.statusMessage, FinalReviewBody: &stored, Acceptable: &tt.acceptable}
			if tt.decision != "" {
				metadata.Decision = &tt.decision
			}
			detail, request := detailURL, reviewURL
			if tt.assessment {
				detail, request = assessmentURL, ""
			}
			if tt.noURLs {
				detail, request = "", ""
			}
			expected := tt.expectedContent
			if tt.expectedLinks != "" {
				expected += "\n\n" + start + "\n" + tt.expectedLinks + "\n" + end
			}
			actual := codeReviewStatusCommentBody(metadata, nil, detail, request)
			require.Equal(t, expected, actual, "status should preserve its relevant result and expose only the applicable grouped footer")
			require.Equal(t, tt.body, stored, "rolling comment formatting must not rewrite the stored publication body")
		})
	}
}

func TestCodeReviewActiveCommentPreservesHistoricalDetailWithoutActions(t *testing.T) {
	t.Parallel()
	const previousURL = "https://143.test/sessions/previous"
	const currentURL = "https://143.test/sessions/current"
	tests := []struct{ name, previousBody string }{
		{name: "legacy links", previousBody: "❌ **143 Code Reviewer needs human review**\n\nPrevious blockers.\n\n[View the full review](" + previousURL + ")\n\n[Request Full Re-Review Now](https://143.test/code-reviews?review_now=previous) · Open 143 to request a review of your latest pushed changes. If a running or completed review already covers those changes, 143 may use it instead of starting another."},
		{name: "marked evidence footer", previousBody: "❌ **143 Code Reviewer needs human review**\n\nPrevious blockers.\n\n<!-- 143-code-review-footer:start -->\n[Re-check evidence](https://143.test/code-reviews?recheck=previous) · [View full review](" + previousURL + ")\n<!-- 143-code-review-footer:end -->"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stored := tt.previousBody
			message := "Waiting for GitHub checks."
			provisional := models.CodeReviewProvisionalReviewHeading + "\n\nCurrent policy blocker."
			body := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatusRunning, StatusMessage: &message, FinalReviewBody: &provisional}, &models.CodeReviewSessionMetadata{FinalReviewBody: &stored}, currentURL, "https://143.test/code-reviews?review_now=current")
			require.Contains(t, body, provisional+"\n\n"+message, "current blockers and operational progress should precede the prior verdict")
			require.Contains(t, body, "<summary>Previous review result</summary>\n\n❌ **143 Code Reviewer needs human review**\n\nPrevious blockers.\n\n[View previous review]("+previousURL+")", "prior result and detail navigation should remain explicitly historical")
			require.NotContains(t, body, "?recheck=", "historical evidence action must not compete with the active review")
			require.NotContains(t, body, "?review_now=", "active comment must not expose a new review request")
			require.Equal(t, 1, strings.Count(body, "<!-- 143-code-review-footer:start -->"), "active comment should have exactly one primary footer")
			require.True(t, strings.HasSuffix(body, "[Follow review]("+currentURL+")\n<!-- 143-code-review-footer:end -->"), "active detail belongs in the final footer")
			require.Equal(t, tt.previousBody, stored, "history composition must leave the saved previous body unchanged")
		})
	}
}

func TestCodeReviewActiveCommentMissingPreviousBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		decision   models.CodeReviewDecision
		acceptable bool
		expected   string
	}{
		{name: "approved previous review", decision: models.CodeReviewDecisionApproved, acceptable: true, expected: "143 Code Reviewer approved this PR."},
		{name: "acceptable comment only", decision: models.CodeReviewDecisionCommentOnly, acceptable: true, expected: "143 Code Reviewer completed its previous review."},
		{name: "blocked previous review", decision: models.CodeReviewDecisionNeedsHumanReview, expected: "143 Code Reviewer completed its previous review."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			previous := models.CodeReviewSessionMetadata{Decision: &tt.decision, Acceptable: &tt.acceptable}
			actual := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatusRunning}, &previous, "", "")
			expected := "143 Code Reviewer has started reviewing this pull request.\n\n<details>\n<summary>Previous review result</summary>\n\n" + tt.expected + "\n\n</details>"
			require.Equal(t, expected, actual, "missing previous body must distinguish acceptable comment-only outcomes from an actual approval")
		})
	}
}

type statusCommentSchedulingStub struct {
	*codeReviewLifecycleStub
	enabled bool
}

func (s *statusCommentSchedulingStub) SchedulingEnabled() bool { return s.enabled }
func (s *statusCommentSchedulingStub) ReconcileSchedule(context.Context, models.CodeReviewScheduleWake) error {
	panic("comment rendering must not execute review scheduling")
}

func TestCodeReviewStatusCommentCancelledReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		message  *string
		expected string
	}{
		{name: "ordinary cancellation", expected: "This 143 code review was cancelled."},
		{name: "blank cancellation message", message: statusCommentStringPtr("  "), expected: "This 143 code review was cancelled."},
		{name: "loop cancellation explains recovery", message: statusCommentStringPtr(" Review cancelled after three consecutive attempts could not publish on unchanged analysis inputs. Push a new revision or explicitly request a fresh review to retry. "), expected: "This 143 code review was cancelled.\n\nReview cancelled after three consecutive attempts could not publish on unchanged analysis inputs. Push a new revision or explicitly request a fresh review to retry."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatusCancelled, StatusMessage: tt.message}, nil, "", "")
			require.Equal(t, tt.expected, body, "cancelled status comment must preserve its actionable reason")
		})
	}
}
