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

func TestAssessmentFileReadsRespectSharedGitHubCooldown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		installationID int64
		elapsed        time.Duration
		wantOutbound   int
	}{
		{name: "another workload's cooldown blocks assessment files", installationID: 42},
		{name: "another installation stays available", installationID: 43, wantOutbound: 1},
		{name: "files resume after the shared deadline", installationID: 42, elapsed: 3 * time.Minute, wantOutbound: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.January, 2, 10, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1,
				InstallationAllowlist: []int64{42, 43}, Logger: zerolog.Nop(),
				Now: func() time.Time { return now },
			})
			require.NoError(t, err, "initialize an isolated shared cooldown controller")
			permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42, Caller: "pr_health"})
			require.NoError(t, err, "a different workload should establish the installation's cooldown")
			throttle := controller.Observe(context.Background(), permit, ratelimit.Observation{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}},
			})
			require.NotNil(t, throttle.Deferral, "the other workload's throttle should retain a shared deadline")
			now = now.Add(tt.elapsed)
			outbound := 0
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "code_review", roundTripFunc(func(req *http.Request) (*http.Response, error) {
				outbound++
				require.Equal(t, "/repos/acme/repo/pulls/42/files", req.URL.Path, "assessment capture should use the pull request files endpoint")
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("[]"))}, nil
			}))
			submitter := NewGitHubSubmitter(&tokenStub{token: "synthetic-token"}, WithGitHubSubmitterHTTPClient(client))
			files, err := submitter.ListPullRequestFiles(context.Background(), PullRequestFilesRequest{
				InstallationID: tt.installationID, Repository: "acme/repo", PullNumber: 42,
			})
			if tt.wantOutbound == 0 {
				var deferral *ratelimit.Deferral
				require.ErrorAs(t, err, &deferral, "assessment capture must preserve the installation's shared cooldown")
				require.Equal(t, throttle.Deferral, deferral, "file capture must retain the exact deadline established by another workload")
				delay := throttle.Deferral.RetryAt.Sub(now)
				require.Equal(t, ghservice.RetryClassification{
					Retryable: true, RateLimited: true, RetryAfter: &delay, RetryAt: &throttle.Deferral.RetryAt,
					RateLimitKind: throttle.Deferral.Kind, ControllerManaged: true,
				}, ghservice.ClassifyRetry(err, now), "file capture must preserve the controller's exact retry policy")
				require.Nil(t, files, "a deferred capture cannot expose partial file inputs")
			} else {
				require.NoError(t, err, "available installations should capture files normally")
				require.Equal(t, []PullRequestFile{}, files, "available captures should retain the complete empty response")
			}
			require.Equal(t, tt.wantOutbound, outbound, "a shared cooldown must suppress outbound file requests only for its installation")
		})
	}
}
