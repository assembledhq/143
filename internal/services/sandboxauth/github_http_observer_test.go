package sandboxauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

func TestGitHubHTTPObserver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, host                string
		transportErr, closeSocket bool
		expectedEvents            int
	}{{name: "multiple actual calls", host: "api.github.com", expectedEvents: 2}, {name: "transport error", host: "api.github.com", transportErr: true, expectedEvents: 2}, {name: "custom host", host: "enterprise.example"}, {name: "socket failure", host: "api.github.com", closeSocket: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs ghLogBuffer
			resolver := &stubResolver{resolution: &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}}
			server := NewServer(resolver, shortSocketDir(t), zerolog.New(&logs))
			session, org := uuid.New(), uuid.New()
			socket, err := server.Listen(context.Background(), session, &models.Session{ID: session, OrgID: org}, &models.Repository{FullName: "example/fixture"}, models.OrgSettings{})
			require.NoError(t, err, "observer fixture should bind the credential socket")
			defer server.Close(session)
			var stderr ghLogBuffer
			observer := NewGitHubHTTPObserver(socket, &stderr)
			for range 2 {
				token, err := observer.GetAPIToken(context.Background())
				require.NoError(t, err, "token resolution should be cached per source")
				require.Equal(t, "fixture", token, "source should receive the host-issued token")
			}
			require.Equal(t, 1, resolver.calls, "observations must not resolve a credential for every request")
			calls := 0
			expectedErr := errors.New("private-upstream-error")
			observer.base = ghRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tt.transportErr {
					return nil, expectedErr
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("[]"))}, nil
			})
			if tt.closeSocket {
				server.Close(session)
			}
			for range 2 {
				request, err := http.NewRequest(http.MethodGet, "https://"+tt.host+"/repos/example/fixture/pulls", nil)
				require.NoError(t, err, "request fixture should be valid")
				request.Header.Set("Authorization", "token fixture")
				response, err := observer.HTTPClient().Do(request)
				if tt.transportErr {
					require.ErrorIs(t, err, expectedErr, "telemetry should preserve the original transport error")
				} else if tt.closeSocket {
					var local *githubControlError
					require.ErrorAs(t, err, &local, "advertised request control must fail closed when the broker disappears")
				} else {
					require.NoError(t, err, "reporting failure must not fail the source request")
					require.NoError(t, response.Body.Close(), "source should retain an owned response body")
				}
			}
			expectedCalls := 2
			if tt.closeSocket {
				expectedCalls = 0
			}
			require.Equal(t, expectedCalls, calls, "only admitted requests should reach the original transport")
			require.Equal(t, tt.expectedEvents, strings.Count(logs.String(), "github api request"), "only standard-host calls with available socket reporting should emit attributed requests")
			require.NotContains(t, logs.String(), "private-upstream-error", "raw transport errors must not enter socket reports")
			if tt.expectedEvents > 0 {
				require.Contains(t, logs.String(), `"github_caller":"sandbox_tools"`, "integration requests must have a distinct bounded caller")
			}
		})
	}
}
