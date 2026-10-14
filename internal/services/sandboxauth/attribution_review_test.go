package sandboxauth

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/telemetry"
)

func TestToolBindingsReuseAcrossInvocations(t *testing.T) {
	t.Parallel()
	scope := newObservationScope(uuid.New(), &models.Session{OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"})
	resolution := &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}
	first := scope.bind(resolution, "sandbox_tools")
	for range 300 {
		require.Equal(t, first, scope.bind(resolution, "sandbox_tools"), "short-lived tool processes should reuse the same trusted credential binding")
	}
	gh := scope.bind(resolution)
	require.NotEmpty(t, gh, "tool invocations must leave capacity for wrapped gh commands")
	require.NotEqual(t, first, gh, "gh completion must not retire the shared tools binding")
	require.Equal(t, 2, len(scope.bindings), "repeated tools should consume one binding rather than one per process")
	rotated := scope.bind(&identity.Resolution{Token: "rotated", Source: identity.SourceApp, InstallationID: 43}, "sandbox_tools")
	require.NotEqual(t, first, rotated, "changed credentials or installation must never reuse stale identity")
}

type recordingAuthResolver struct {
	mu       sync.Mutex
	metadata []telemetry.RequestMetadata
}

func (r *recordingAuthResolver) ResolveSandbox(ctx context.Context, _ *models.Session, _ *models.Repository, _ string) (*identity.Resolution, error) {
	metadata, _ := telemetry.RequestMetadataFromContext(ctx)
	r.mu.Lock()
	r.metadata = append(r.metadata, metadata)
	r.mu.Unlock()
	return &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}, nil
}

func TestMintCallerAndRollingCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		request        string
		expectedCaller string
		rejected       bool
	}{
		{name: "gh", request: `{"op":"get","action":"api","want_observation":true}`, expectedCaller: "sandbox_gh"},
		{name: "tools", request: `{"op":"get","action":"api","want_observation":true,"observer":"sandbox_tools"}`, expectedCaller: "sandbox_tools"},
		{name: "push future field", request: `{"op":"get","action":"push","future_client_field":"fixture"}`},
		{name: "invalid caller", request: `{"op":"get","action":"api","want_observation":true,"observer":"arbitrary-session"}`, rejected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := &recordingAuthResolver{}
			server := NewServer(resolver, shortSocketDir(t), zerolog.Nop())
			scope := newObservationScope(uuid.New(), &models.Session{OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"})
			host, client := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				server.handleConn(context.Background(), host, &models.Session{}, &models.Repository{}, zerolog.Nop(), scope)
			}()
			require.NoError(t, json.NewEncoder(client).Encode(json.RawMessage(tt.request)), "future credential fields should be accepted without expanding identity authority")
			var response Response
			require.NoError(t, json.NewDecoder(client).Decode(&response), "credential protocol should return a bounded response")
			require.NoError(t, client.Close(), "fixture connection should close")
			<-done
			if tt.rejected {
				require.NotEmpty(t, response.Error, "unknown caller labels must be rejected")
				require.Empty(t, resolver.metadata, "rejected observer labels must not resolve credentials")
				return
			}
			require.Empty(t, response.Error, "rolling client fields should not break credentials")
			require.Equal(t, "sandbox_token", resolver.metadata[0].Caller, "all socket token minting must use the token caller, including pushes")
			require.Equal(t, scope.metadata.SessionID, resolver.metadata[0].SessionID, "mint calls must retain session attribution")
			if tt.expectedCaller != "" {
				require.Equal(t, tt.expectedCaller, scope.bindings[response.ObservationID].metadata.Caller, "API observations must retain the independent bounded caller")
			}
		})
	}
}

func TestObservationCapacityWarning(t *testing.T) {
	t.Parallel()
	var logs ghLogBuffer
	resolver := &stubResolver{resolution: &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}}
	server := NewServer(resolver, shortSocketDir(t), zerolog.New(&logs))
	session := uuid.New()
	sock, err := server.Listen(context.Background(), session, &models.Session{OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"}, models.OrgSettings{})
	require.NoError(t, err, "capacity fixture should bind")
	defer server.Close(session)
	client := NewClient(sock)
	for range 257 {
		response, err := client.GetObservedAPIToken(context.Background())
		require.NoError(t, err, "capacity pressure must not fail credential issuance")
		require.Equal(t, "fixture", response.Token, "credential issuance must remain available at the cap")
	}
	require.Contains(t, logs.String(), "GitHub observation capacity reached", "capacity rejection must be visible at warning level")
}

func TestWrapperCredentialAndEndpointSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		env, args            []string
		credentials, observe bool
	}{
		{name: "standard", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "view", "https://github.com/example/fixture/pull/42"}, credentials: true, observe: true},
		{name: "enterprise PR identifier", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "view", "https://enterprise.example/owner/repo/pull/42"}, credentials: true},
		{name: "URL title", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "create", "--title", "https://enterprise.example"}, credentials: true, observe: true},
		{name: "explicit token", env: []string{SocketEnvVar + "=fixture", "GH_TOKEN=provided"}, credentials: false},
		{name: "legacy token", env: []string{SocketEnvVar + "=fixture", "GITHUB_TOKEN=provided"}, credentials: false},
		{name: "host override", env: []string{SocketEnvVar + "=fixture", "GH_HOST=enterprise.example"}, credentials: false},
		{name: "missing socket", credentials: false},
		{name: "method before enterprise endpoint", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "-X", "GET", "https://enterprise.example/api/v3/user"}, credentials: true},
		{name: "preview before enterprise endpoint", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "--preview", "fixture", "https://enterprise.example/api/v3/user"}, credentials: true},
		{name: "short preview before enterprise endpoint", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "-p", "fixture", "https://enterprise.example/api/v3/user"}, credentials: true},
		{name: "method before standard endpoint", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "-X", "GET", "https://api.github.com/user"}, credentials: true, observe: true},
		{name: "explicit repository host", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "view", "-R", "enterprise.example/owner/repo"}, credentials: true},
		{name: "compact repository host", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "view", "-Renterprise.example/owner/repo"}, credentials: true},
		{name: "standard repository host", env: []string{SocketEnvVar + "=fixture"}, args: []string{"pr", "view", "--repo=github.com/owner/repo"}, credentials: true, observe: true},
		{name: "URL valued field", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "-f", "link=https://enterprise.example", "repos/example/fixture/pulls"}, credentials: true, observe: true},
		{name: "archive redirect", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "-X", "GET", "repos/example/fixture/zipball/main"}, credentials: true},
		{name: "artifact redirect", env: []string{SocketEnvVar + "=fixture"}, args: []string{"api", "repos/example/fixture/actions/artifacts/42/zip"}, credentials: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.credentials, usesHostGitHubCredential(tt.env), "explicit credentials and hosts must preserve their existing credential selection")
			if tt.credentials {
				require.Equal(t, tt.observe, shouldObserveGH(tt.args, append(tt.env, "GH_CONFIG_DIR="+t.TempDir())), "only supported default-host API traffic should select observation")
			}
		})
	}
}
