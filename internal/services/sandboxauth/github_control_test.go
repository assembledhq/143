package sandboxauth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
)

func TestSandboxRequestAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mode         ratelimit.Mode
		installation int64
		unknown      bool
		deferred     bool
		forgedReport bool
	}{
		{name: "enforced known cooldown", mode: ratelimit.ModeEnforce, installation: 42, deferred: true},
		{name: "sandbox success report cannot clear cooldown", mode: ratelimit.ModeEnforce, installation: 42, deferred: true, forgedReport: true},
		{name: "observe does not suppress", mode: ratelimit.ModeObserve, installation: 42},
		{name: "other installation unaffected", mode: ratelimit.ModeEnforce, installation: 99},
		{name: "different credential is not relabeled", mode: ratelimit.ModeEnforce, installation: 42, unknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs ghLogBuffer
			now := time.Now().UTC().Truncate(time.Second)
			deadline := now.Add(time.Hour)
			controller, err := ratelimit.NewController(ratelimit.Config{Mode: tt.mode, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42}, Now: func() time.Time { return now }, Logger: zerolog.New(&logs)})
			require.NoError(t, err, "fixture should create a shared controller")
			controller.Observe(context.Background(), ratelimit.Permit{Scope: ratelimit.Scope{InstallationID: 42}}, ratelimit.Observation{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(deadline.Unix(), 10)}}})
			resolver := &stubResolver{resolution: &identity.Resolution{Token: "fixture-token", Source: identity.SourceApp, InstallationID: tt.installation}}
			server := NewServer(resolver, shortSocketDir(t), zerolog.New(&logs))
			server.SetGitHubRateLimitController(controller)
			session := uuid.New()
			socket, err := server.Listen(context.Background(), session, &models.Session{ID: session, OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"}, models.OrgSettings{})
			require.NoError(t, err, "fixture should bind a session auth socket")
			defer server.Close(session)
			observer := NewGitHubHTTPObserver(socket, io.Discard)
			_, err = observer.GetAPIToken(context.Background())
			require.NoError(t, err, "binding should resolve once before requests")
			if tt.forgedReport {
				err = observer.client.Observe(context.Background(), observer.response.ObservationID, []HTTPObservation{{Method: http.MethodGet, Route: "/graphql", Status: 200, Header: map[string]string{"X-RateLimit-Remaining": "7350", "X-RateLimit-Resource": "core"}}}, false)
				require.NoError(t, err, "untrusted reports should remain available for attribution only")
			}
			previousRemoteEvents := strings.Count(logs.String(), "github api request")
			calls := 0
			observer.base = ghRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("[]"))}, nil
			})
			request, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/example/fixture/pulls", nil)
			require.NoError(t, err, "fixture request should be valid")
			auth := "Bearer fixture-token"
			if tt.unknown {
				auth = "Bearer other-token"
			}
			request.Header.Set("Authorization", auth)
			response, err := observer.HTTPClient().Do(request)
			if tt.deferred {
				deferral, ok := ratelimit.AsDeferral(err)
				require.True(t, ok, "the caller should receive a typed local deferral")
				require.Equal(t, deadline, deferral.RetryAt, "sandbox should retain the trusted provider deadline")
				require.Zero(t, calls, "known cooldown must stop the request before the outbound transport")
				require.Equal(t, previousRemoteEvents, strings.Count(logs.String(), "github api request"), "a local deferral must not inflate outbound request counts")
			} else {
				require.NoError(t, err, "uncovered or observation-only requests should proceed")
				require.NoError(t, response.Body.Close(), "the caller should retain its response body")
				require.Equal(t, 1, calls, "an admitted request should reach the underlying transport exactly once")
				if tt.unknown {
					require.Contains(t, logs.String(), `"github_principal_unresolved":true`, "different credentials must be recorded as unresolved")
				}
			}
			require.Equal(t, 1, resolver.calls, "admission must use the bound principal without minting tokens per request")
			require.NotContains(t, logs.String(), "fixture-token", "credentials must never enter telemetry")
		})
	}
}

func TestSandboxControlCapabilityCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		advertised    bool
		expectedCalls int
	}{
		{name: "older server retains observed forwarding", expectedCalls: 1},
		{name: "advertised control cannot silently downgrade", advertised: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var observations, checks atomic.Int64
			socket := startSocketServer(t, func(conn net.Conn) {
				var request Request
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					t.Errorf("decode compatibility fixture: %v", err)
					return
				}
				response := Response{}
				switch request.Op {
				case OpGet:
					response = Response{Token: "fixture", ObservationID: "fixture-binding", RequestControl: tt.advertised}
				case OpObserve:
					observations.Add(1)
				case OpCheck:
					checks.Add(1)
					response.Error = "unsupported op"
				case OpCoverage:
					response.Error = "unsupported op"
				default:
					response.Error = "unexpected op"
				}
				if err := json.NewEncoder(conn).Encode(&response); err != nil {
					t.Errorf("encode compatibility fixture: %v", err)
				}
			})
			observer := NewGitHubHTTPObserver(socket, io.Discard)
			_, err := observer.GetAPIToken(context.Background())
			require.NoError(t, err, "capability negotiation must preserve old token responses")
			calls := 0
			observer.base = ghRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
			})
			request, err := http.NewRequest(http.MethodGet, "https://api.github.com/graphql", nil)
			require.NoError(t, err, "fixture request should construct")
			request.Header.Set("Authorization", "Bearer fixture")
			response, err := observer.HTTPClient().Do(request)
			if tt.advertised {
				var local *githubControlError
				require.ErrorAs(t, err, &local, "a negotiated admission failure must not fall back to direct HTTP")
				require.Equal(t, int64(1), checks.Load(), "each covered request should ask the advertised gate")
			} else {
				require.NoError(t, err, "older servers should preserve the preexisting observation-only behavior")
				require.NoError(t, response.Body.Close(), "caller should own the admitted response")
				require.Equal(t, int64(1), observations.Load(), "older servers should continue receiving request observations")
				require.Zero(t, checks.Load(), "helpers must not call a gate the older server never advertised")
			}
			require.Equal(t, tt.expectedCalls, calls, "negotiated control determines whether forwarding is permitted")
		})
	}
}

func TestSandboxBindingCannotBeUsedAcrossSessions(t *testing.T) {
	t.Parallel()
	server := NewServer(&stubResolver{resolution: &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}}, shortSocketDir(t), zerolog.Nop())
	first, second := uuid.New(), uuid.New()
	firstSocket, err := server.Listen(context.Background(), first, &models.Session{ID: first, OrgID: uuid.New()}, nil, models.OrgSettings{})
	require.NoError(t, err, "first tenant socket should bind")
	defer server.Close(first)
	secondSocket, err := server.Listen(context.Background(), second, &models.Session{ID: second, OrgID: uuid.New()}, nil, models.OrgSettings{})
	require.NoError(t, err, "second tenant socket should bind")
	defer server.Close(second)
	binding, err := NewClient(firstSocket).GetObservedAPIToken(context.Background())
	require.NoError(t, err, "first tenant should receive its own binding")
	response, err := NewClient(secondSocket).exchange(context.Background(), Request{Op: OpCheck, ObservationID: binding.ObservationID, Method: http.MethodGet, Route: "/graphql"})
	require.NoError(t, err, "the broker should return a structured rejection")
	require.Equal(t, "GitHub request binding expired or unavailable", response.Error, "foreign session bindings must not confer admission")
	require.Empty(t, response.Token, "admission replies must never include credentials")
}

func TestGHProxyReturnsLocalDeferralWithoutRemoteObservation(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(time.Minute)
	observations := 0
	proxy := ghProxy("fixture", ghRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &githubControlError{cause: &ratelimit.Deferral{InstallationID: 42, Kind: ratelimit.KindPrimary, RetryAt: deadline}}
	}), func(HTTPObservation) { observations++ })
	request := httptest.NewRequest(http.MethodGet, "http://api.github.com/graphql", nil)
	request.Header.Set("Authorization", "Bearer fixture")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code, "CLI should receive an actionable local cooldown response")
	require.NotEmpty(t, response.Header().Get("Retry-After"), "CLI response should retain a bounded wait")
	require.Zero(t, observations, "suppressed requests must never be reported as provider traffic")
}
