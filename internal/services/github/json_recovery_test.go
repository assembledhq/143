package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestOwnedRESTJSONRecovery(t *testing.T) {
	t.Parallel()
	requests := []struct {
		name, valid string
		typed       bool
		call        func(context.Context, *PRService, *Service) error
	}{
		{name: "PR buffer", valid: `{}`, call: func(ctx context.Context, p *PRService, _ *Service) error {
			_, err := p.doGitHubRequest(ctx, "token", http.MethodGet, "/repos/acme/repo", nil)
			return err
		}},
		{name: "org members", typed: true, valid: `[{"login":"octocat"}]`, call: func(ctx context.Context, _ *PRService, s *Service) error {
			_, err := s.ListOrgMembers(ctx, 42, "acme")
			return err
		}},
		{name: "org membership", typed: true, valid: `{"state":"active"}`, call: func(ctx context.Context, _ *PRService, s *Service) error {
			_, err := s.IsActiveOrgMember(ctx, 42, "acme", "octocat")
			return err
		}},
		{name: "team membership", typed: true, valid: `{"state":"active"}`, call: func(ctx context.Context, _ *PRService, s *Service) error {
			_, err := s.IsActiveTeamMember(ctx, 42, "acme", "team", "octocat")
			return err
		}},
	}
	cases := []struct {
		name, body                  string
		valid, typeMismatch, cancel bool
	}{
		{name: "complete valid document", valid: true},
		{name: "truncated document", body: `{"broken":`},
		{name: "trailing document", body: `{} {}`},
		{name: "wrong typed shape", body: `"wrong"`, typeMismatch: true},
		{name: "empty response"},
		{name: "caller canceled at EOF", valid: true, cancel: true},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					controller, _ := newJSONRecoveryController(t)
					ctx, cancel := context.WithCancel(githubtelemetry.WithInstallationRequestMetadata(context.Background(), 42, "test"))
					t.Cleanup(cancel)
					body := tt.body
					if tt.valid {
						body = request.valid
					}
					outbound, checkedEOF := 0, false
					client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "test", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
						outbound++
						reader := &jsonRecoveryReader{Reader: strings.NewReader(body), atEOF: func() {
							checkedEOF = true
							_, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
							var deferred *ratelimit.Deferral
							require.ErrorAs(t, err, &deferred, "reading EOF must not release the probe before the owned decoder finishes")
							if tt.cancel {
								cancel()
							}
						}}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
					}))
					prs := &PRService{baseURL: "https://api.github.com", httpClient: client, logger: zerolog.Nop()}
					service := &Service{httpClient: client, cache: map[int64]*cachedToken{42: {Token: "token", ExpiresAt: time.Now().Add(time.Hour)}}}
					err := request.call(ctx, prs, service)
					recovered := !tt.cancel && (tt.valid || tt.typeMismatch && !request.typed)
					require.Equal(t, tt.cancel || request.typed && !recovered, err != nil, "decoder handoff should preserve the caller's existing return semantics")
					require.True(t, checkedEOF, "fixture must exercise EOF before JSON decoding")
					permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
					require.NoError(t, err, "completed request should release its own lease")
					require.Equal(t, !recovered, permit.Probe, "only a valid uncanceled decode may restore ordinary admission")
					require.Equal(t, 1, outbound, "recovery observation must never replay the request")
				})
			}
		})
	}
}

func TestPRResponseFormatsPreserveRecovery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, accept, body string
		status             int
	}{
		{name: "raw diff", accept: "application/vnd.github.v3.diff", body: "diff --git a/a b/a", status: http.StatusOK},
		{name: "raw source", accept: "application/vnd.github.raw+json", body: "package example", status: http.StatusOK},
		{name: "no content", accept: "application/vnd.github+json", status: http.StatusNoContent},
		{name: "body throttle", accept: "application/vnd.github+json", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit."}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, now := newJSONRecoveryController(t)
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "test", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			}))
			service := &PRService{baseURL: "https://api.github.com", httpClient: client, logger: zerolog.Nop()}
			ctx := githubtelemetry.WithInstallationRequestMetadata(context.Background(), 42, "test")
			body, err := service.doGitHubRequestWithAccept(ctx, "token", http.MethodGet, "/repos/acme/repo", nil, tt.accept)
			if tt.status == http.StatusForbidden {
				var apiErr *GitHubAPIError
				require.ErrorAs(t, err, &apiErr, "provider error must retain its original structured type")
				_, deferralErr := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				var deferral *ratelimit.Deferral
				require.ErrorAs(t, deferralErr, &deferral, "body throttle must extend the cooldown")
				require.Equal(t, &deferral.RetryAt, ClassifyRetry(err, now.Add(time.Minute)).RetryAt, "cloned error headers must already contain the controller's exact deadline")
				return
			}
			require.NoError(t, err, "raw and no-content responses should retain existing success semantics")
			require.Equal(t, tt.body, string(body), "format selection must preserve response bytes")
			permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "successful response should release the probe")
			require.False(t, permit.Probe, "complete raw or no-content responses should establish recovery")
		})
	}
}

type jsonRecoveryReader struct {
	io.Reader
	atEOF func()
}

func (r *jsonRecoveryReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.atEOF()
	}
	return n, err
}

func newJSONRecoveryController(t *testing.T) (*ratelimit.Controller, *time.Time) {
	t.Helper()
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42}, Logger: zerolog.Nop(), Now: func() time.Time { return now }})
	require.NoError(t, err, "isolated controller should initialize")
	permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
	require.NoError(t, err, "seed request should be admitted")
	now = controller.Observe(context.Background(), permit, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}}).Deferral.RetryAt
	return controller, &now
}

func TestOrgMembersPaginationOwnsEachJSONObservation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, secondBody string
		recovered        bool
	}{
		{name: "valid second page", secondBody: `[{"login":"second"}]`, recovered: true},
		{name: "malformed second page", secondBody: `[{"login":`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, now := newJSONRecoveryController(t)
			outbound := 0
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "test", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				outbound++
				body, header := tt.secondBody, make(http.Header)
				onClose := func() {}
				if outbound == 1 {
					body = `[{"login":"first"}]`
					header.Set("Link", `<https://api.github.com/orgs/acme/members?page=2>; rel="next"`)
					onClose = func() {
						permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
						require.NoError(t, err, "first page must complete recovery before the next episode")
						require.False(t, permit.Probe, "valid first page should establish recovery before Close")
						*now = controller.Observe(context.Background(), permit, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}}).Deferral.RetryAt
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: &jsonPageBody{Reader: strings.NewReader(body), onClose: onClose}}, nil
			}))
			service := &Service{httpClient: client, cache: map[int64]*cachedToken{42: {Token: "token", ExpiresAt: time.Now().Add(time.Hour)}}}
			members, err := service.ListOrgMembers(context.Background(), 42, "acme")
			require.Equal(t, !tt.recovered, err != nil, "second page must use its own decoder result")
			if tt.recovered {
				require.Equal(t, []OrgMember{{Login: "first"}, {Login: "second"}}, members, "pagination should preserve every decoded member")
			}
			permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "second page should release its own probe lease")
			require.Equal(t, !tt.recovered, permit.Probe, "each page must complete a fresh observation independently")
			require.Equal(t, 2, outbound, "pagination should send exactly one request per page")
		})
	}
}

type jsonPageBody struct {
	io.Reader
	onClose func()
}

func (b *jsonPageBody) Close() error { b.onClose(); return nil }
