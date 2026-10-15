package telemetry

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

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTransportCorrelatesActualAuthorizationWithoutChangingPrincipal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 123, time.UTC)
	tests := []struct {
		name          string
		authorization http.Header
		metadata      RequestMetadata
		status        string
		known         bool
	}{
		{name: "case insensitive bearer", authorization: http.Header{"authorization": {"bEaReR synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42}, status: "known", known: true},
		{name: "case insensitive token", authorization: http.Header{"Authorization": {"ToKeN synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42}, status: "known", known: true},
		{name: "prelabeled wrong installation", authorization: http.Header{"Authorization": {"Bearer synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 99}, status: "installation_mismatch", known: true},
		{name: "prelabeled wrong authentication", authorization: http.Header{"Authorization": {"Bearer synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeUser}, status: "auth_type_mismatch", known: true},
		{name: "unresolved context still identifies issued token", authorization: http.Header{"Authorization": {"Bearer synthetic-secret"}}, status: "known", known: true},
		{name: "wrong repository", authorization: http.Header{"Authorization": {"Bearer synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42, Repository: "octo/other"}, status: "repository_mismatch", known: true},
		{name: "unknown credential ignores context identity", authorization: http.Header{"Authorization": {"Bearer different-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42, CredentialIdentity: CredentialIdentity{GenerationID: "forged-context-generation"}}, status: "unknown"},
		{name: "ambiguous credentials", authorization: http.Header{"Authorization": {"Bearer synthetic-secret", "Token synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42}, status: "unknown"},
		{name: "unsupported scheme", authorization: http.Header{"Authorization": {"Basic synthetic-secret"}}, metadata: RequestMetadata{AuthType: AuthTypeAppInstallation, InstallationID: 42}, status: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry := NewCredentialRegistry()
			registry.now = func() time.Time { return now }
			registration := credentialFixture(now)
			registration.Profile = CredentialProfileSandboxAPI
			registration.Repository = "octo/demo"
			identity, ok := registry.Register("synthetic-secret", registration)
			require.True(t, ok, "test issuer should register the exact outgoing token")
			var logs bytes.Buffer
			clock := 0
			times := []time.Time{now, now.Add(5 * time.Millisecond), now.Add(15 * time.Millisecond)}
			transport := &transport{
				credentials: registry, logger: zerolog.New(&logs), caller: "unit",
				now: func() time.Time { value := times[clock]; clock++; return value },
				base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					metadata, exists := RequestMetadataFromContext(request.Context())
					require.True(t, exists, "transport should pass bounded metadata to its base transport")
					require.Equal(t, tt.metadata.InstallationID, metadata.InstallationID, "diagnostic token provenance must not change the admission principal")
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{"Date": {now.Format(http.TimeFormat)}, "X-Github-Request-Id": {strings.Repeat("r", 160)}}}, nil
				}),
			}
			request, err := http.NewRequestWithContext(WithRequestMetadata(context.Background(), tt.metadata), http.MethodGet, "https://api.github.com/repos/octo/demo/pulls/3", nil)
			require.NoError(t, err, "test outgoing request should be valid")
			request.Header = tt.authorization
			response, err := transport.RoundTrip(request)
			require.NoError(t, err, "correlation should not change transport success")
			_, readErr := io.ReadAll(response.Body)
			require.NoError(t, readErr, "correlation should preserve response consumption")
			require.NoError(t, response.Body.Close(), "correlation should preserve response close")
			expected := map[string]any{
				"level": "info", "message": "github api request", "github_method": "GET",
				"github_route": "/repos/:owner/:repo/pulls/:id", "github_repository": "octo/demo",
				"github_auth_type": tt.metadata.AuthType, "github_caller": "unit", "github_status_code": float64(200),
				"github_status_class": "2xx", "github_result": "success", "github_rate_limited": false, "github_duration_ms": float64(15),
				"github_request_started_at": times[0].Format(time.RFC3339Nano), "github_response_headers_at": times[1].Format(time.RFC3339Nano),
				"github_request_completed_at": times[2].Format(time.RFC3339Nano), "github_timing_source": "host_transport",
				"github_provider_date": now.Truncate(time.Second).Format(time.RFC3339Nano), "github_request_id": strings.Repeat("r", 128),
				"github_token_correlation_status":          tt.status,
				"github_transport_write_attempts_observed": float64(0), "github_transport_write_errors_observed": float64(0),
				"github_transport_write_observation_status": "unknown", "github_transport_write_observation_cutoff": "request_summary",
				"github_target_host_class": "api_github_com",
			}
			if tt.metadata.AuthType == "" {
				expected["github_auth_type"] = AuthTypeUnknown
				expected["github_principal_unresolved"] = true
			}
			if tt.metadata.InstallationID > 0 {
				expected["github_installation_id"] = float64(tt.metadata.InstallationID)
			}
			if tt.metadata.Repository != "" {
				expected["github_repository"] = tt.metadata.Repository
			}
			if tt.known {
				expected["github_token_generation_id"] = identity.GenerationID
				expected["github_token_profile"] = string(identity.Profile)
				expected["github_token_app_id"] = float64(identity.AppID)
				expected["github_token_installation_id"] = float64(identity.InstallationID)
				expected["github_token_repository"] = identity.Repository
				expected["github_token_issued_at"] = identity.IssuedAt.Format(time.RFC3339Nano)
				expected["github_token_expires_at"] = identity.ExpiresAt.Format(time.RFC3339Nano)
			}
			var actual map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "request telemetry should remain structured JSON")
			require.Equal(t, expected, actual, "telemetry should report the actual header identity separately from the unchanged context principal")
			digest := sha256.Sum256([]byte("synthetic-secret"))
			require.NotContains(t, logs.String(), "synthetic-secret", "token bytes must never enter logs")
			require.NotContains(t, logs.String(), hex.EncodeToString(digest[:]), "internal token digest must never enter logs")
			require.NotContains(t, logs.String(), "forged-context-generation", "native requests must ignore context-supplied token identity")
		})
	}
}

func TestTransportPreservesHeaderOrderAcrossOutOfOrderCompletion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 123, time.UTC)
	registry := NewCredentialRegistry()
	registry.now = func() time.Time { return now }
	identity, ok := registry.Register("synthetic-token", credentialFixture(now))
	require.True(t, ok, "test issuer should register its shared generation")
	var logs bytes.Buffer
	clock := 0
	times := []time.Time{now, now.Add(time.Millisecond), now.Add(2 * time.Millisecond), now.Add(3 * time.Millisecond), now.Add(4 * time.Millisecond), now.Add(5 * time.Millisecond)}
	requestCount := 0
	transport := &transport{credentials: registry, logger: zerolog.New(&logs), now: func() time.Time { value := times[clock]; clock++; return value }, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		used := "10"
		if requestCount == 2 {
			used = "11"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Ratelimit-Used": {used}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	newRequest := func() *http.Request {
		request, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
		require.NoError(t, err, "test request should be valid")
		request.Header.Set("Authorization", "Bearer synthetic-token")
		return request
	}
	first, firstErr := transport.RoundTrip(newRequest())
	require.NoError(t, firstErr, "first request should return headers before its body completes")
	second, secondErr := transport.RoundTrip(newRequest())
	require.NoError(t, secondErr, "second request should proceed while the earlier body remains open")
	_, readSecond := io.ReadAll(second.Body)
	require.NoError(t, readSecond, "second body should finish first")
	require.NoError(t, second.Body.Close(), "second response should close")
	_, readFirst := io.ReadAll(first.Body)
	require.NoError(t, readFirst, "earlier body should finish last")
	require.NoError(t, first.Body.Close(), "first response should close")
	decoder := json.NewDecoder(&logs)
	var events []map[string]any
	for range 2 {
		var event map[string]any
		require.NoError(t, decoder.Decode(&event), "each response should emit exactly one completed summary")
		events = append(events, map[string]any{
			"generation": event["github_token_generation_id"], "used": event["github_rate_limit_used"],
			"started": event["github_request_started_at"], "headers": event["github_response_headers_at"], "completed": event["github_request_completed_at"],
		})
	}
	require.Equal(t, []map[string]any{
		{"generation": identity.GenerationID, "used": float64(11), "started": times[2].Format(time.RFC3339Nano), "headers": times[3].Format(time.RFC3339Nano), "completed": times[4].Format(time.RFC3339Nano)},
		{"generation": identity.GenerationID, "used": float64(10), "started": times[0].Format(time.RFC3339Nano), "headers": times[1].Format(time.RFC3339Nano), "completed": times[5].Format(time.RFC3339Nano)},
	}, events, "provider counters must retain transport chronology when body completion reverses log order")
	var extra map[string]any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF, "closing already observed responses must not duplicate summaries")
}

func TestExternalRequestUsesHostBoundIdentityWithoutInventingHeaderArrival(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 123, time.UTC)
	registry := NewCredentialRegistry()
	registry.now = func() time.Time { return now }
	registration := credentialFixture(now)
	registration.Profile = CredentialProfileSandboxAPI
	registration.Repository = "octo/demo"
	identity, ok := registry.Register("host-bound-synthetic-token", registration)
	require.True(t, ok, "server-owned sandbox credential should have issuer provenance")
	var logs bytes.Buffer
	RecordExternalRequest(context.Background(), zerolog.New(&logs), RequestMetadata{
		AuthType: AuthTypeAppInstallation, InstallationID: 42, Repository: "octo/demo",
		Caller: "sandbox_api", CredentialIdentity: identity,
	}, http.MethodGet, "/repos/octo/demo/pulls/3", http.StatusOK,
		http.Header{"Date": {now.Format(http.TimeFormat)}, "X-Github-Request-Id": {"sandbox-request"}}, 2*time.Second)
	var actual map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "sandbox summary should remain structured JSON")
	completed, err := time.Parse(time.RFC3339Nano, actual["github_request_completed_at"].(string))
	require.NoError(t, err, "external completion should be the host receipt time")
	expected := map[string]any{
		"level": "info", "message": "github api request", "github_method": "GET",
		"github_route": "/repos/:owner/:repo/pulls/:id", "github_repository": "octo/demo",
		"github_auth_type": AuthTypeAppInstallation, "github_installation_id": float64(42), "github_caller": "sandbox_api",
		"github_status_code": float64(200), "github_status_class": "2xx", "github_result": "success",
		"github_duration_ms": float64(2000), "github_rate_limited": false,
		"github_request_started_at":   completed.Add(-2 * time.Second).Format(time.RFC3339Nano),
		"github_request_completed_at": completed.Format(time.RFC3339Nano),
		"github_timing_source":        "sandbox_reported_duration", "github_observation_source": "sandbox", "github_observation_trusted": false,
		"github_provider_date": now.Truncate(time.Second).Format(time.RFC3339Nano), "github_request_id": "sandbox-request",
		"github_token_generation_id": identity.GenerationID, "github_token_correlation_status": "known",
		"github_token_profile": string(identity.Profile), "github_token_app_id": float64(identity.AppID),
		"github_token_installation_id": float64(identity.InstallationID), "github_token_repository": identity.Repository,
		"github_token_issued_at": identity.IssuedAt.Format(time.RFC3339Nano), "github_token_expires_at": identity.ExpiresAt.Format(time.RFC3339Nano),
	}
	require.Equal(t, expected, actual, "sandbox reports should keep host-bound identity separate from untrusted reported timing and headers")
}
