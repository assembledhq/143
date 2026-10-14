package sandboxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

type ghRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ghRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGHProxyRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, host, method, path, auth string
		status                         int
		transportErr                   bool
		expected                       []HTTPObservation
	}{
		{name: "REST", host: "api.github.com", method: "GET", path: "/repos/example/fixture/pulls/42?secret=private", auth: "token fixture", status: 200, expected: []HTTPObservation{{Method: "GET", Route: "/repos/:owner/:repo/pulls/:id", Status: 200, Header: map[string]string{"X-RateLimit-Resource": "core", "X-RateLimit-Remaining": "12"}}}},
		{name: "GraphQL", host: "api.github.com", method: "POST", path: "/graphql", auth: "Bearer fixture", status: 200, expected: []HTTPObservation{{Method: "POST", Route: "/graphql", Status: 200, Header: map[string]string{"X-RateLimit-Resource": "core", "X-RateLimit-Remaining": "12"}}}},
		{name: "foreign host", host: "127.0.0.1", method: "GET", path: "/", auth: "token fixture", status: 403},
		{name: "CONNECT", host: "api.github.com", method: "CONNECT", path: "/", auth: "token fixture", status: 403},
		{name: "credential override", host: "api.github.com", method: "GET", path: "/graphql", auth: "token other", status: 200, expected: []HTTPObservation{{UnknownPrincipal: true, Method: "GET", Route: "/graphql", Status: 200, Header: map[string]string{"X-RateLimit-Resource": "core", "X-RateLimit-Remaining": "12"}}}},
		{name: "transport failure", host: "api.github.com", method: "GET", path: "/graphql", auth: "token fixture", status: 502, transportErr: true, expected: []HTTPObservation{{Method: "GET", Route: "/graphql"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var actual []HTTPObservation
			secretBody := strings.Repeat("private-body", 100000)
			handler := ghProxy("fixture", ghRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "https", r.URL.Scheme, "forwarded requests should use TLS")
				require.Equal(t, tt.host, r.URL.Host, "only the verified GitHub host should be forwarded")
				if tt.transportErr {
					return nil, errors.New("private upstream error")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Ratelimit-Remaining": []string{"12"}, "X-Ratelimit-Resource": []string{"core"}, "Authorization": []string{"private-header"}}, Body: io.NopCloser(strings.NewReader(secretBody))}, nil
			}), func(event HTTPObservation) { event.DurationMS = 0; actual = append(actual, event) })
			request := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, strings.NewReader(secretBody))
			request.Header.Set("Authorization", tt.auth)
			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, request)
			require.Equal(t, tt.status, writer.Code, "CLI should retain the upstream status or a safe proxy error")
			require.Equal(t, tt.expected, actual, "only actual forwarded requests should produce bounded safe metadata")
			if tt.status == 200 {
				require.Equal(t, secretBody, writer.Body.String(), "response bodies should stream unchanged without entering telemetry")
			}
		})
	}
}

func TestObservationScopeIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		foreign, unknown, oversized bool
	}{{name: "bound identity"}, {name: "unknown principal", unknown: true}, {name: "foreign binding", foreign: true}, {name: "oversized batch", oversized: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			session, org := uuid.New(), uuid.New()
			scope := newObservationScope(session, &models.Session{OrgID: org}, &models.Repository{FullName: "example/fixture"})
			binding := scope.bind(&identity.Resolution{Source: identity.SourceApp, InstallationID: 42})
			var logs bytes.Buffer
			event := HTTPObservation{UnknownPrincipal: tt.unknown, Method: "GET", Route: "/repos/example/fixture/pulls/9", Status: 200, Header: map[string]string{"X-RateLimit-Remaining": "7", "Authorization": "private"}}
			req := Request{ObservationID: binding, Events: []HTTPObservation{event}, Finish: true}
			if tt.foreign {
				req.ObservationID = newObservationScope(uuid.New(), nil, nil).bind(&identity.Resolution{Source: identity.SourceApp, InstallationID: 99})
			}
			if tt.oversized {
				req.Events = make([]HTTPObservation, 33)
			}
			err := scope.record(context.Background(), zerolog.New(&logs), req)
			if tt.foreign || tt.oversized {
				require.Error(t, err, "foreign or oversized reports must not be accepted")
				require.Empty(t, logs.String(), "rejected reports should emit no GitHub request")
				return
			}
			require.NoError(t, err, "a host-issued binding should accept its own events")
			var actual map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "one forwarded request should produce one JSON event")
			require.Equal(t, false, actual["github_observation_trusted"], "sandbox quota reports must not be treated as authoritative")
			require.Equal(t, "sandbox", actual["github_observation_source"], "external report provenance must be explicit")
			require.Equal(t, session.String(), actual["session_id"], "session attribution should come from the listener")
			require.Equal(t, org.String(), actual["org_id"], "org attribution should come from the listener")
			require.Equal(t, "example/fixture", actual["github_repository"], "repository attribution should come from the listener")
			if tt.unknown {
				require.NotContains(t, actual, "github_installation_id", "credential overrides must not inherit installation attribution")
			} else {
				require.Equal(t, float64(42), actual["github_installation_id"], "installation should come from credential resolution")
			}
			require.NotContains(t, logs.String(), "private", "unapproved headers must never enter telemetry")
			require.Error(t, scope.record(context.Background(), zerolog.Nop(), req), "completed bindings must be retired")
		})
	}
}

func TestGHConfigPreservation(t *testing.T) {
	t.Parallel()
	source, dest := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "config.yml"), []byte("editor: example-editor\naliases:\n  inspect: pr view\n"), 0o600), "fixture should define ordinary CLI settings")
	require.NoError(t, os.WriteFile(filepath.Join(source, "hosts.yml"), []byte("existing-private-config"), 0o600), "fixture should include preexisting host settings")
	require.NoError(t, prepareGHConfig(dest, "/tmp/fixture.sock", []string{"GH_CONFIG_DIR=" + source}), "transport config should preserve existing settings")
	body, err := os.ReadFile(filepath.Join(dest, "config.yml"))
	require.NoError(t, err, "temporary config should be readable")
	var config map[string]any
	require.NoError(t, yaml.Unmarshal(body, &config), "temporary config should be valid YAML")
	require.Equal(t, map[string]any{"editor": "example-editor", "aliases": map[string]any{"inspect": "pr view"}, "http_unix_socket": "/tmp/fixture.sock"}, config, "only transport configuration should be added")
	target, err := os.Readlink(filepath.Join(dest, "hosts.yml"))
	require.NoError(t, err, "existing credentials should be referenced without copying")
	require.Equal(t, filepath.Join(source, "hosts.yml"), target, "host credentials should stay in their original file")
	require.NotContains(t, string(body), "existing-private-config", "credentials must not be persisted in the temporary config")
}

func TestExecuteGHStreamsAndPreservesExit(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := executeGH(context.Background(), "/bin/sh", []string{"-c", "cat; printf 'ordinary error' >&2; exit 7"}, os.Environ(), strings.NewReader("ordinary output"), &stdout, &stderr)
	require.Equal(t, 7, code, "wrapped CLI exit status should be preserved")
	require.Equal(t, "ordinary output", stdout.String(), "stdout and stdin should stream unchanged")
	require.Equal(t, "ordinary error", stderr.String(), "ordinary stderr should stream unchanged")
}

func TestGHUnixTransportPaginationAndCache(t *testing.T) {
	t.Parallel()
	binary, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("GitHub CLI is not installed")
	}
	dir := t.TempDir()
	var mu sync.Mutex
	var requests []string
	var events []HTTPObservation
	transport := ghRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, r.URL.RequestURI())
		mu.Unlock()
		header := http.Header{"Content-Type": []string{"application/json"}, "X-Ratelimit-Remaining": []string{"9"}}
		body := "[2]"
		if r.URL.Query().Get("page") != "2" {
			header.Set("Link", "<https://api.github.com/repos/example/fixture/pulls?page=2>; rel=\"next\"")
			body = "[1]"
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	env := replaceGHEnv(os.Environ(), map[string]string{"GH_CONFIG_DIR": filepath.Join(dir, "original"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "GH_DEBUG": "api"})
	var expectedOutput string
	for i := range 2 {
		var output, stderr bytes.Buffer
		code := runObservedGH(context.Background(), binary, []string{"api", "repos/example/fixture/pulls?per_page=100", "--paginate", "--cache", "1h"}, env, "fixture", nil, &output, &stderr, func(event HTTPObservation) { mu.Lock(); events = append(events, event); mu.Unlock() }, transport)
		require.Equal(t, 0, code, "the real CLI should paginate through the forwarding transport: %s", stderr.String())
		require.Empty(t, stderr.String(), "HTTP debug data must be disabled before spawning the CLI")
		if i == 0 {
			expectedOutput = output.String()
		}
		require.Equal(t, expectedOutput, output.String(), "cached pagination should preserve the CLI's native output")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"/repos/example/fixture/pulls?per_page=100", "/repos/example/fixture/pulls?page=2"}, requests, "cached pagination across temporary configurations should make only the original two remote requests")
	require.Len(t, events, 2, "only the two actual requests should emit telemetry")
}

func TestGHConfigEmptyAndRoutingOverrides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body      string
		args            []string
		expectedObserve bool
	}{{name: "empty config", expectedObserve: true}, {name: "null config", body: "null", expectedObserve: true}, {name: "custom Unix transport", body: "http_unix_socket: /tmp/original.sock", expectedObserve: false}, {name: "custom hostname", args: []string{"api", "--hostname", "enterprise.example", "user"}, expectedObserve: false}, {name: "GitHub PR identifier", args: []string{"pr", "view", "https://github.com/example/fixture/pull/42"}, expectedObserve: true}, {name: "absolute URL", args: []string{"api", "https://enterprise.example/api/v3/user"}, expectedObserve: false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source, dest := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(source, "config.yml"), []byte(tt.body), 0o600), "config fixture should be created")
			env := []string{"GH_CONFIG_DIR=" + source}
			require.Equal(t, tt.expectedObserve, shouldObserveGH(tt.args, env), "explicit routing overrides must preserve their transport")
			require.NoError(t, prepareGHConfig(dest, "/tmp/fixture.sock", env), "empty and null configurations must accept the Unix transport setting")
		})
	}
}

type ghLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *ghLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *ghLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func TestObservationProtocol(t *testing.T) {
	t.Parallel()
	var logs ghLogBuffer
	session, org := uuid.New(), uuid.New()
	server := NewServer(&stubResolver{resolution: &identity.Resolution{Token: "fixture", Source: identity.SourceApp, InstallationID: 42}}, shortSocketDir(t), zerolog.New(&logs))
	sock, err := server.Listen(context.Background(), session, &models.Session{ID: session, OrgID: org}, &models.Repository{FullName: "example/fixture"}, models.OrgSettings{})
	require.NoError(t, err, "credential listener should start")
	defer server.Close(session)
	client := NewClient(sock)
	response, err := client.GetObservedAPIToken(context.Background())
	require.NoError(t, err, "observed token request should succeed")
	require.NotEmpty(t, response.ObservationID, "host must create an opaque observation binding")
	require.NoError(t, client.Observe(context.Background(), response.ObservationID, []HTTPObservation{{Method: "GET", Route: "/graphql", Status: 200}}, true), "reporting should use the existing credential socket")
	require.Error(t, client.Observe(context.Background(), response.ObservationID, nil, true), "completed bindings should reject further observations")
	require.Contains(t, logs.String(), "github api request", "an actual reported request must produce request telemetry")
}
