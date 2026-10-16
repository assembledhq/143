package sandboxauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/telemetry"
)

func TestObservationScopeCredentialGeneration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		unknown bool
	}{
		{name: "host-bound credential"},
		{name: "credential override clears correlation", unknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := newObservationScope(uuid.New(), &models.Session{OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"})
			scope.credentialRegistry = telemetry.NewCredentialRegistry()
			token := "fixture-" + uuid.NewString()
			credential, known := scope.credentialRegistry.Register(token, telemetry.CredentialRegistration{
				Profile: telemetry.CredentialProfileSandboxAPI, AppID: 7, InstallationID: 42,
				Repository: "example/fixture", IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			})
			require.True(t, known, "fixture credential should be registered only on the host")
			binding := scope.bind(&identity.Resolution{Token: token, Source: identity.SourceApp, InstallationID: 42}, "sandbox_tools")
			var request Request
			require.NoError(t, json.Unmarshal([]byte(`{"observation_id":"`+binding+`","github_token_generation_id":"forged","credential_identity":{"generation_id":"forged"},"events":[{"method":"GET","route":"/repos/example/fixture/pulls/9","status":200,"github_token_generation_id":"forged"}]}`), &request), "unknown sandbox fields must not become authoritative correlation")
			request.Events[0].UnknownPrincipal = tt.unknown
			request.Events[0].Header = map[string]string{
				"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "4999", "X-RateLimit-Used": "1",
				"X-RateLimit-Reset": "1900000000", "X-RateLimit-Resource": "core", "Retry-After": "1",
				"Date": "Mon, 02 Jan 2006 15:04:05 GMT", "X-GitHub-Request-Id": "ABCD:1234:5678",
			}
			var logs bytes.Buffer
			require.NoError(t, scope.record(context.Background(), zerolog.New(&logs), request), "all eight approved observation headers should be accepted")
			var actual map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "one sandbox observation should produce one request event")
			require.Equal(t, false, actual["github_observation_trusted"], "credential binding must not promote sandbox headers to trusted observations")
			require.Equal(t, "sandbox_reported_duration", actual["github_timing_source"], "sandbox timings must retain reported provenance")
			require.Equal(t, "2006-01-02T15:04:05Z", actual["github_provider_date"], "valid provider date should survive the bounded observation protocol")
			require.Equal(t, "ABCD:1234:5678", actual["github_request_id"], "valid provider request identity should survive the bounded observation protocol")
			require.NotContains(t, actual, "github_response_headers_at", "sandbox reports must not fabricate direct host response-arrival times")
			if tt.unknown {
				require.Equal(t, "unknown", actual["github_token_correlation_status"], "credential overrides must clear host-bound generation")
				require.NotContains(t, actual, "github_token_generation_id", "unknown principals must not inherit a credential generation")
				require.NotContains(t, actual, "github_token_installation_id", "unknown principals must not inherit issuing installation identity")
			} else {
				require.Equal(t, credential.GenerationID, actual["github_token_generation_id"], "only the host-issued generation may be logged")
				require.Equal(t, "known", actual["github_token_correlation_status"], "matching scope should retain its diagnostic correlation")
			}
			digest := sha256.Sum256([]byte(token))
			require.NotContains(t, logs.String(), "forged", "sandbox-provided generation fields must never enter telemetry")
			require.NotContains(t, logs.String(), token, "sandbox telemetry must never contain its bound token")
			require.NotContains(t, logs.String(), hex.EncodeToString(digest[:]), "sandbox telemetry must not expose an internal token fingerprint")
		})
	}
}

func TestGHObservationForwardsProviderCorrelationHeaders(t *testing.T) {
	t.Parallel()
	var events []HTTPObservation
	transport := ghObservationTransport{
		base: ghRoundTripFunc(func(*http.Request) (*http.Response, error) {
			header := make(http.Header)
			header.Set("Date", "Mon, 02 Jan 2006 15:04:05 GMT")
			header.Set("X-GitHub-Request-Id", "ABCD:1234:5678")
			header.Set("Authorization", "Bearer private")
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("fixture-body"))}, nil
		}),
		observe: func(event HTTPObservation) { event.DurationMS = 0; events = append(events, event) },
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/repos/example/fixture/pulls/9", nil)
	require.NoError(t, err, "fixture request should be valid")
	response, err := transport.RoundTrip(request)
	require.NoError(t, err, "correlation headers must not change the upstream request result")
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err, "response content should remain readable")
	require.NoError(t, response.Body.Close(), "response body should close normally")
	require.Equal(t, "fixture-body", string(body), "observation must not consume or alter upstream content")
	require.Equal(t, []HTTPObservation{{Method: http.MethodGet, Route: "/repos/:owner/:repo/pulls/:id", Status: http.StatusOK, Header: map[string]string{"Date": "Mon, 02 Jan 2006 15:04:05 GMT", "X-GitHub-Request-Id": "ABCD:1234:5678"}}}, events, "helper should forward only the newly allowlisted correlation headers")
}

func TestObservationProviderHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    map[string]string
		expected http.Header
	}{
		{name: "valid provider metadata", input: map[string]string{"Date": "Mon, 02 Jan 2006 15:04:05 GMT", "X-GitHub-Request-Id": "A0:12-ab_cd"}, expected: http.Header{"Date": {"Mon, 02 Jan 2006 15:04:05 GMT"}, "X-Github-Request-Id": {"A0:12-ab_cd"}}},
		{name: "malformed provider date", input: map[string]string{"Date": "private fixture"}, expected: http.Header{}},
		{name: "oversized request identity", input: map[string]string{"X-GitHub-Request-Id": strings.Repeat("A", 129)}, expected: http.Header{}},
		{name: "newline request identity", input: map[string]string{"X-GitHub-Request-Id": "ABC\nprivate"}, expected: http.Header{}},
		{name: "unapproved credential header", input: map[string]string{"Authorization": "Bearer private"}, expected: http.Header{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, observationHeaders(tt.input), "only bounded provider metadata should pass the host allowlist")
		})
	}
}
