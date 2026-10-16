package worker

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestFullAssessmentCaptureGitHubRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cause   func(time.Time) error
		consume bool
		fatal   bool
		exact   bool
	}{
		{name: "primary limit", cause: func(now time.Time) error {
			return &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Method: "GET", Path: "/repos/test/repo/pulls/7", Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(now.Add(time.Hour).Unix())}}, Body: []byte(`{"message":"API rate limit exceeded for installation ID 42"}`)}
		}},
		{name: "shared controller wait", exact: true, cause: func(now time.Time) error {
			return &ratelimit.Deferral{Kind: ratelimit.KindPrimary, InstallationID: 42, RetryAt: now.Add(time.Hour), Generation: 1}
		}},
		{name: "temporary GitHub failure", consume: true, cause: func(time.Time) error {
			return &ghservice.GitHubAPIError{StatusCode: http.StatusServiceUnavailable}
		}},
		{name: "permission denial stays fatal", fatal: true, cause: func(time.Time) error {
			return &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Body: []byte(`{"message":"Resource not accessible by integration"}`)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, phase := range []string{"initial capture", "publication freshness", "visual discovery"} {
				t.Run(phase, func(t *testing.T) {
					t.Parallel()
					job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
					now := time.Now().UTC()
					cause := fmt.Errorf("load pull request for code review mention: %w", tt.cause(now))
					capture := &fixedRecheckCapture{captureErr: cause}
					services := &Services{CodeReviewInputCapture: capture, CodeReviewAssessmentsEnabled: true}
					var err error
					var calls int
					switch phase {
					case "initial capture":
						mock, mockErr := pgxmock.NewPool()
						require.NoError(t, mockErr, "create an isolated tenant-scoped capture lookup")
						defer mock.Close()
						mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").WithArgs(job.OrgID, job.SessionID).WillReturnError(pgx.ErrNoRows)
						_, _, err = prepareFullAssessment(context.Background(), &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}, services, job, models.CodeReviewSessionMetadata{})
						require.NoError(t, mock.ExpectationsWereMet(), "initial capture must look up only the owning organization's session")
						calls = capture.calls
					case "publication freshness":
						_, err = captureFreshFullAssessment(context.Background(), services, job, models.CodeReviewAssessment{})
						calls = capture.calls
					case "visual discovery":
						visual := &assessmentVisualCheckpoint{captureErr: cause}
						services.CodeReviewVisualEvidence = visual
						_, err = captureCodeReviewVisualEvidence(context.Background(), services, job, models.PullRequest{})
						calls = len(visual.requests)
					}
					require.ErrorIs(t, err, cause, "capture failure must preserve the provider error chain")
					require.Equal(t, 1, calls, "failure classification must not perform another provider read")
					if tt.fatal {
						var fatal *FatalError
						require.ErrorAs(t, err, &fatal, "nonretryable GitHub responses must fail without spending eight retries")
						return
					}
					var retry *RetryableError
					require.ErrorAs(t, err, &retry, "capture errors must use the GitHub recovery policy rather than ordinary job backoff")
					require.Equal(t, tt.consume, retry.ConsumeAttempt, "rate-limit waits must preserve the attempt budget while transient failures consume it")
					require.Equal(t, new(githubRateLimitMaxRetryDuration), retry.MaxRetryDuration, "capture recovery must retain the finite GitHub recovery window")
					if tt.exact {
						require.Equal(t, new(now.Add(time.Hour)), retry.GitHubRetryAt, "shared controller recovery must preserve its exact reset deadline")
					} else if !tt.consume {
						require.GreaterOrEqual(t, *retry.RetryAfter, 59*time.Minute, "primary-limit recovery must wait for the provider reset instead of retrying every few seconds")
					}
				})
			}
		})
	}
}
