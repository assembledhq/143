package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

type githubJSONTestBody struct {
	io.Reader
	closed  bool
	onClose func()
}

func (b *githubJSONTestBody) Close() error {
	b.closed = true
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

type githubJSONEndReader struct {
	err    error
	onRead context.CancelFunc
}

func (r githubJSONEndReader) Read([]byte) (int, error) {
	if r.onRead != nil {
		r.onRead()
	}
	return 0, r.err
}

func TestGitHubJSONClientsCompleteProbe(t *testing.T) {
	t.Parallel()
	const document = `{"repositories":[],"items":[]}`
	clients := []string{"repositories", "team_search"}
	tests := []struct {
		name          string
		body          func(context.CancelFunc) io.Reader
		expectedErr   bool
		expectedProbe bool
	}{
		{name: "decoder returns before trailing whitespace and EOF", body: func(context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader(document), strings.NewReader(" \r\n\t"))
		}},
		{name: "malformed first value", body: func(context.CancelFunc) io.Reader { return strings.NewReader(`{"items":`) }, expectedErr: true, expectedProbe: true},
		{name: "malformed trailing content", body: func(context.CancelFunc) io.Reader { return strings.NewReader(document + " junk") }, expectedErr: true, expectedProbe: true},
		{name: "second JSON value", body: func(context.CancelFunc) io.Reader { return strings.NewReader(document + " {}") }, expectedErr: true, expectedProbe: true},
		{name: "late read failure", body: func(context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader(document), githubJSONEndReader{err: io.ErrUnexpectedEOF})
		}, expectedErr: true, expectedProbe: true},
		{name: "body timeout", body: func(context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader(document), githubJSONEndReader{err: context.DeadlineExceeded})
		}, expectedErr: true, expectedProbe: true},
		{name: "cancelled request at EOF", body: func(cancel context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader(document), githubJSONEndReader{err: io.EOF, onRead: cancel})
		}, expectedErr: true, expectedProbe: true},
		{name: "oversized response", body: func(context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader(document), strings.NewReader(strings.Repeat(" ", maxGitHubJSONResponseBytes)))
		}, expectedErr: true, expectedProbe: true},
	}
	for _, clientName := range clients {
		for _, tt := range tests {
			t.Run(clientName+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
				controller, err := ratelimit.NewController(ratelimit.Config{
					Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
					Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: t.Name(),
				})
				require.NoError(t, err, "controller should initialize")
				seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				require.NoError(t, err, "initial request should be admitted")
				observed := controller.Observe(context.Background(), seed, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}})
				now = observed.Deferral.RetryAt
				ctx, cancel := context.WithCancel(githubtelemetry.WithInstallationRequestMetadata(context.Background(), 42, clientName))
				defer cancel()
				body := &githubJSONTestBody{Reader: tt.body(cancel)}
				outbound := 0
				client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, clientName, roundTripFunc(func(req *http.Request) (*http.Response, error) {
					outbound++
					metadata, _ := githubtelemetry.RequestMetadataFromContext(req.Context())
					require.Equal(t, clientName, metadata.Caller, "owned decoder should preserve caller attribution")
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				}))
				if clientName == "repositories" {
					handler := &IntegrationHandler{githubHTTPClient: client}
					_, err = handler.listInstallationRepos(ctx, "token", 42)
				} else {
					handler := &TeamHandler{httpClient: client}
					_, err = handler.searchGitHubUsers(ctx, "token", "octocat")
				}
				require.Equal(t, tt.expectedErr, err != nil, "owned decoder should reject malformed, oversized, and failed response bodies")
				next, nextErr := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				require.NoError(t, nextErr, "completed decoder should release its probe lease")
				require.Equal(t, tt.expectedProbe, next.Probe, "only a complete valid response may admit ordinary work")
				require.True(t, body.closed, "owned client should close every response")
				require.Equal(t, 1, outbound, "JSON observation should never replay requests")
			})
		}
	}
}

func TestIntegrationHandler_ListInstallationReposProbePagination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		secondBody    string
		expectedProbe bool
	}{
		{name: "complete second page recovers", secondBody: `{"repositories":[]}`},
		{name: "malformed second page keeps cooldown", secondBody: `{"repositories":[]} garbage`, expectedProbe: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
				Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: t.Name(),
			})
			require.NoError(t, err, "controller should initialize")
			openCooldown := func() {
				seed, seedErr := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				require.NoError(t, seedErr, "new throttle should be admitted between pages")
				result := controller.Observe(context.Background(), seed, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}})
				now = result.Deferral.RetryAt
			}
			openCooldown()
			outbound := 0
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "integration_sync", roundTripFunc(func(*http.Request) (*http.Response, error) {
				outbound++
				if outbound == 1 {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Link": []string{`<https://api.github.com/installation/repositories?page=2>; rel="next"`}}, Body: &githubJSONTestBody{
						Reader: strings.NewReader(`{"repositories":[]}`), onClose: openCooldown,
					}}, nil
				}
				require.Equal(t, 2, outbound, "pagination should make exactly two requests")
				checkLease := githubJSONEndReader{err: io.EOF, onRead: func() {
					_, admissionErr := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
					var deferral *ratelimit.Deferral
					require.ErrorAs(t, admissionErr, &deferral, "first page handoff must not prove second page recovery before reading its body")
				}}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(io.MultiReader(checkLease, strings.NewReader(tt.secondBody)))}, nil
			}))
			handler := &IntegrationHandler{githubHTTPClient: client}
			_, err = handler.listInstallationRepos(context.Background(), "token", 42)
			require.Equal(t, tt.expectedProbe, err != nil, "each page must independently validate its full JSON document")
			next, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "second page should release its probe lease")
			require.Equal(t, tt.expectedProbe, next.Probe, "only the current page decoder may establish recovery")
			require.Equal(t, 2, outbound, "pagination must preserve both requests without replay")
		})
	}
}
