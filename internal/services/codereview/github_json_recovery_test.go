package codereview

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGitHubSubmitterRESTJSONRecovery(t *testing.T) {
	t.Parallel()
	requests := []struct {
		name     string
		mutation bool
		call     func(context.Context, *GitHubSubmitter) error
	}{
		{name: "typed read", call: func(ctx context.Context, s *GitHubSubmitter) error {
			var target struct{ ID int }
			_, err := s.getGitHubJSONPage(ctx, "token", "/repos/acme/repo", &target)
			return err
		}},
		{name: "review comment update", mutation: true, call: func(ctx context.Context, s *GitHubSubmitter) error {
			return s.updateReviewComment(ctx, "token", "acme", "repo", 1, "body")
		}},
		{name: "issue comment update", mutation: true, call: func(ctx context.Context, s *GitHubSubmitter) error {
			return s.updateIssueComment(ctx, "token", "acme", "repo", 1, "body")
		}},
		{name: "formal approval", mutation: true, call: func(ctx context.Context, s *GitHubSubmitter) error {
			return s.ensureFormalApproval(ctx, "token", "acme", "repo", SubmitReviewRequest{PullNumber: 1, HeadSHA: "head"})
		}},
	}
	cases := []struct {
		name, body string
		status     int
		recovered  bool
	}{
		{name: "complete JSON", body: `{"id":1}`, status: http.StatusOK, recovered: true},
		{name: "malformed JSON", body: `{"id":`, status: http.StatusOK},
		{name: "trailing JSON", body: `{"id":1} {}`, status: http.StatusOK},
		{name: "empty success", status: http.StatusOK},
		{name: "no content", status: http.StatusNoContent, recovered: true},
		{name: "body throttle", body: `{"message":"You have exceeded a secondary rate limit."}`, status: http.StatusForbidden},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
					controller, err := ratelimit.NewController(ratelimit.Config{Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42}, Logger: zerolog.Nop(), Now: func() time.Time { return now }})
					require.NoError(t, err, "isolated controller should initialize")
					scope := ratelimit.Scope{InstallationID: 42}
					initial, err := controller.Before(context.Background(), scope)
					require.NoError(t, err, "seed request should be admitted")
					now = controller.Observe(context.Background(), initial, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}}).Deferral.RetryAt
					outbound := 0
					client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "test", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
						outbound++
						return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
					}))
					submitter := NewGitHubSubmitter(&tokenStub{token: "token"}, WithGitHubSubmitterHTTPClient(client))
					ctx := githubtelemetry.WithInstallationRequestMetadata(context.Background(), 42, "test")
					err = request.call(ctx, submitter)
					require.Equal(t, tt.status >= 400 || !request.mutation && !tt.recovered, err != nil, "status-confirmed mutations must preserve success without replaying on invalid unused bodies")
					permit, admissionErr := controller.Before(context.Background(), scope)
					if tt.status >= 400 {
						var deferral *ratelimit.Deferral
						require.ErrorAs(t, admissionErr, &deferral, "body throttle must extend the shared cooldown")
						require.Equal(t, &deferral.RetryAt, ghservice.ClassifyRetry(err, now.Add(time.Minute)).RetryAt, "error headers must copy the controller deadline after bounded body observation")
					} else {
						require.NoError(t, admissionErr, "completed request should release its own probe lease")
						require.Equal(t, !tt.recovered, permit.Probe, "only complete valid JSON or no-content responses may establish recovery")
					}
					require.Equal(t, 1, outbound, "JSON validation must not replay any outbound request")
				})
			}
		})
	}
}
