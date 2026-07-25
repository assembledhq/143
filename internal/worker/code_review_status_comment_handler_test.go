package worker

import (
	"context"
	"encoding/json"
	"errors"
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
		initialStatus             models.CodeReviewSessionStatus
		lockedStatus              models.CodeReviewSessionStatus
		lockedFinalBody           *string
		lockedReviewID            *int64
		storedCommentID           any
		expectedExistingCommentID *int64
		expectedBody              string
		expectedCalls             []string
		hideErr                   error
		expectErr                 bool
	}{
		{
			name:          "announces running review with session link",
			initialStatus: models.CodeReviewSessionStatusRunning,
			lockedStatus:  models.CodeReviewSessionStatusRunning,
			expectedBody:  "143 Code Reviewer has started reviewing this pull request.",
			expectedCalls: []string{"upsert"},
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
			now := time.Now().UTC()
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
			mock.ExpectExec("SELECT pg_advisory_xact_lock").
				WithArgs(pgx.NamedArgs{"lock_key": "code_review_status_comment:" + orgID.String() + ":" + pullRequestID.String()}).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("(?s)FROM code_review_session_metadata.*pull_request_id = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
				WillReturnRows(metadataRows(tt.lockedStatus, tt.lockedFinalBody, tt.lockedReviewID))
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
				CodeReviews: submitter,
				FrontendURL: "https://143.test",
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
			require.Contains(t, submitter.request.Body, "https://143.test/sessions/"+sessionID.String(), "status comment should link to the review session")
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
