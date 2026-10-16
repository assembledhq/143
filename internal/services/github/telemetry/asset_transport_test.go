package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGitHubAssetTransportSelectsAuthenticatedAttachmentHops(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, target, method, authorization string
		observed                            bool
		duplicate                           bool
	}{
		{name: "authenticated attachment", target: "https://github.com/user-attachments/assets/private-id/image.png?signature=private", authorization: "Bearer synthetic", observed: true},
		{name: "case insensitive host", target: "https://GitHub.com/user-attachments/assets/private-id", authorization: "bEaReR synthetic", observed: true},
		{name: "unauthenticated attachment", target: "https://github.com/user-attachments/assets/private-id"},
		{name: "ambiguous authorization remains unselected", target: "https://github.com/user-attachments/assets/private-id", authorization: "Bearer synthetic", duplicate: true},
		{name: "arbitrary external image", target: "https://images.example.invalid/user-attachments/assets/private-id", authorization: "Bearer synthetic"},
		{name: "signed CDN", target: "https://github-production-user-asset.example.invalid/image.png?signature=private"},
		{name: "other GitHub surface", target: "https://github.com/login/oauth/access_token", authorization: "Bearer synthetic"},
		{name: "lookalike host", target: "https://github.com.example.invalid/user-attachments/assets/private-id", authorization: "Bearer synthetic"},
		{name: "non HTTPS", target: "http://github.com/user-attachments/assets/private-id", authorization: "Bearer synthetic"},
		{name: "embedded credentials", target: "https://user:private@github.com/user-attachments/assets/private-id", authorization: "Bearer synthetic"},
		{name: "other method", target: "https://github.com/user-attachments/assets/private-id", method: http.MethodPost, authorization: "Bearer synthetic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			ctx := WithRequestMetadata(context.Background(), RequestMetadata{Caller: "another_caller", AuthType: AuthTypeUser, InstallationID: 42})
			request, err := http.NewRequestWithContext(ctx, method, tt.target, nil)
			require.NoError(t, err, "synthetic request should parse without contacting any provider")
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			if tt.duplicate {
				request.Header["authorization"] = []string{"Bearer another-synthetic-token"}
			}
			calls := 0
			base := roundTripFunc(func(actual *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, request.URL, actual.URL, "observation must not rewrite attachment or signed redirect targets")
				require.Equal(t, request.Header, actual.Header, "observation must not alter any outgoing credentials or request headers")
				metadata, ok := RequestMetadataFromContext(actual.Context())
				require.True(t, ok, "the original request context must survive observation")
				if tt.observed {
					require.Equal(t, CallerVisualAsset, metadata.Caller, "asset observations must override an inherited unrelated caller")
					require.Equal(t, RequestKindAPI, metadata.Kind, "asset exchanges must use the native HTTP collector stream rather than OAuth")
					require.Equal(t, AuthTypeAppInstallation, metadata.AuthType, "selected asset requests use the installation token supplied by the downloader")
				} else {
					require.Same(t, request, actual, "unselected external and OAuth requests must go directly to the original transport")
					require.Equal(t, "another_caller", metadata.Caller, "unselected requests must retain their original attribution")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
			})
			response, err := NewGitHubAssetTransport(base, zerolog.New(&logs)).RoundTrip(request)
			require.NoError(t, err, "observing an eligible hop must not change transport success")
			require.NoError(t, response.Body.Close(), "observation must preserve body closure")
			require.Equal(t, 1, calls, "selection must never add or replay a transport exchange")
			if tt.observed {
				events := decodeAssetEvents(t, &logs)
				require.Equal(t, 1, len(events), "an eligible hop must produce exactly one completed observation")
				require.Equal(t, "/user-attachments/assets/:asset", events[0]["github_route"], "private asset identifiers must collapse before logging")
			} else {
				require.Equal(t, "", logs.String(), "arbitrary external and unauthenticated requests must not produce GitHub asset events")
			}
		})
	}
}

func TestGitHubAssetTransportCorrelatesActualCredentialAndHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, resource string
		declaredID     int64
	}{
		{name: "missing resource stays unknown"},
		{name: "actual core header", resource: "core"},
		{name: "other actual resource", resource: "integration_manifest"},
		{name: "declared mismatch retains actual issuer identity", resource: "core", declaredID: 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
			registry := NewCredentialRegistry()
			registry.now = func() time.Time { return now }
			token := "synthetic-asset-" + uuid.NewString()
			identity, ok := registry.Register(token, CredentialRegistration{Profile: CredentialProfileInstallation, AppID: 11, InstallationID: 42, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
			require.True(t, ok, "the isolated issuer fixture should register its own token generation")
			orgID, sessionID, assessmentID, prID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			declaredID, correlationStatus := int64(42), "known"
			if tt.declaredID != 0 {
				declaredID, correlationStatus = tt.declaredID, "installation_mismatch"
			}
			ctx := WithRequestMetadata(jobctx.WithJobID(context.Background(), jobID), RequestMetadata{Kind: RequestKindOAuth, AuthType: AuthTypeUser, InstallationID: declaredID, Caller: "parent_caller", OrgID: orgID.String(), SessionID: sessionID.String(), AssessmentID: assessmentID, PullRequestID: prID, Repository: "acme/demo", CredentialIdentity: CredentialIdentity{GenerationID: "forged-generation"}})
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/user-attachments/assets/00000000-1111-2222-3333-444444444444/private.png?signature=private-query", nil)
			require.NoError(t, err, "the synthetic authenticated asset URL should be valid")
			request.Header.Set("Authorization", "Bearer "+token)
			var logs bytes.Buffer
			clock := 0
			times := []time.Time{now, now.Add(time.Millisecond), now.Add(5 * time.Millisecond)}
			observer := NewGitHubAssetTransport(roundTripFunc(func(actual *http.Request) (*http.Response, error) {
				require.Equal(t, "Bearer "+token, actual.Header.Get("Authorization"), "correlation must observe the actual outgoing credential without changing it")
				httptrace.ContextClientTrace(actual.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				header := http.Header{"Date": {now.Format(http.TimeFormat)}, "X-Github-Request-Id": {"asset-request"}, "X-Ratelimit-Limit": {"5000"}, "X-Ratelimit-Remaining": {"4950"}, "X-Ratelimit-Used": {"50"}, "X-Ratelimit-Reset": {"1792069200"}, "Location": {"https://cdn.example.invalid/private-target?signature=private-location"}}
				if tt.resource != "" {
					header.Set("X-RateLimit-Resource", tt.resource)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("private-body"))}, nil
			}), zerolog.New(&logs)).(*assetTransport)
			observer.observer.credentials = registry
			observer.observer.now = func() time.Time { value := times[clock]; clock++; return value }
			response, err := observer.RoundTrip(request)
			require.NoError(t, err, "diagnostic correlation must not reject the asset")
			require.Equal(t, "", logs.String(), "header arrival must not be reported as body completion")
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err, "asset body bytes must pass through unchanged")
			require.Equal(t, "private-body", string(body), "the observer must not consume or rewrite image bytes")
			require.NoError(t, response.Body.Close(), "closing a completed body must preserve the base result")
			events := decodeAssetEvents(t, &logs)
			require.Equal(t, 1, len(events), "EOF and Close must produce one observation in total")
			expected := map[string]any{
				"level": "info", "message": "github api request", "github_method": "GET", "github_route": "/user-attachments/assets/:asset", "github_auth_type": AuthTypeAppInstallation,
				"github_status_code": float64(200), "github_status_class": "2xx", "github_result": "success", "github_duration_ms": float64(5), "github_request_started_at": times[0].Format(time.RFC3339Nano), "github_response_headers_at": times[1].Format(time.RFC3339Nano), "github_request_completed_at": times[2].Format(time.RFC3339Nano), "github_timing_source": "host_transport", "github_token_correlation_status": correlationStatus, "github_rate_limited": false,
				"github_transport_write_attempts_observed": float64(1), "github_transport_write_errors_observed": float64(0), "github_transport_write_observation_status": "callbacks_observed", "github_transport_write_observation_cutoff": "request_summary", "github_target_host_class": "github_com", "github_provider_date": now.Format(time.RFC3339Nano), "github_request_id": "asset-request",
				"github_token_generation_id": identity.GenerationID, "github_token_profile": string(identity.Profile), "github_token_app_id": float64(11), "github_token_installation_id": float64(42), "github_token_issued_at": identity.IssuedAt.Format(time.RFC3339Nano), "github_token_expires_at": identity.ExpiresAt.Format(time.RFC3339Nano),
				"org_id": orgID.String(), "session_id": sessionID.String(), "assessment_id": assessmentID.String(), "pull_request_id": prID.String(), "job_id": jobID.String(), "github_repository": "acme/demo", "github_installation_id": float64(declaredID), "github_caller": CallerVisualAsset,
				"github_rate_limit_limit": float64(5000), "github_rate_limit_remaining": float64(4950), "github_rate_limit_used": float64(50), "github_rate_limit_remaining_pct": float64(99), "github_rate_limit_reset_unix": float64(1792069200), "github_rate_limit_reset_at": time.Unix(1792069200, 0).UTC().Format(time.RFC3339), "github_rate_limit_reset_seconds": time.Unix(1792069200, 0).Sub(times[2]).Seconds(),
			}
			if tt.resource != "" {
				expected["github_rate_limit_resource"] = tt.resource
			}
			require.Equal(t, expected, events[0], "asset attribution must preserve actual issuer identity, headers and request scope without inventing a quota resource")
			for _, private := range []string{token, "00000000-1111-2222-3333-444444444444", "private.png", "private-query", "private-location", "private-body", "forged-generation"} {
				require.NotContains(t, logs.String(), private, "asset telemetry must not retain URL, credential, response-body or untrusted context secrets")
			}
		})
	}
}

type assetBodyProbe struct {
	readResult []byte
	readErr    error
	closeErr   error
	reads      int
	closes     int
}

func (b *assetBodyProbe) Read(data []byte) (int, error) {
	b.reads++
	return copy(data, b.readResult), b.readErr
}

func (b *assetBodyProbe) Close() error { b.closes++; return b.closeErr }

func TestGitHubAssetTransportPreservesFailuresAndNeverDrains(t *testing.T) {
	t.Parallel()
	privateErr := errors.New("https://github.com/user-attachments/assets/private-file.png?signature=private-error")
	tests := []struct {
		name          string
		status        int
		transportErr  error
		nilResponse   bool
		nilBody       bool
		readErr       error
		closeErr      error
		read          bool
		expectedError string
	}{
		{name: "transport error is sanitized", transportErr: privateErr, nilResponse: true, expectedError: "GitHub asset transport failed"},
		{name: "nil response is finalized once", nilResponse: true, expectedError: "GitHub asset transport failed"},
		{name: "nil body is finalized once", nilBody: true},
		{name: "403 close never drains error body", status: http.StatusForbidden},
		{name: "redirect close never drains body", status: http.StatusFound},
		{name: "read failure is sanitized", read: true, readErr: privateErr, expectedError: "GitHub asset response read failed"},
		{name: "close failure is sanitized", closeErr: privateErr, expectedError: "GitHub asset response close failed"},
		{name: "EOF then close failure keeps honest summary cutoff", read: true, readErr: io.EOF, closeErr: privateErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			body := &assetBodyProbe{readResult: []byte("private-body"), readErr: tt.readErr, closeErr: tt.closeErr}
			status := tt.status
			if status == 0 {
				status = http.StatusOK
			}
			calls := 0
			observer := NewGitHubAssetTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tt.nilResponse {
					return nil, tt.transportErr
				}
				response := &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://cdn.example.invalid/private-location?signature=private-signature"}}, Body: body}
				if tt.nilBody {
					response.Body = nil
				}
				return response, tt.transportErr
			}), zerolog.New(&logs))
			request, err := http.NewRequest(http.MethodGet, "https://github.com/user-attachments/assets/private-file.png?signature=private-query", nil)
			require.NoError(t, err, "the synthetic failure request should parse")
			request.Header.Set("Authorization", "Bearer private-token")
			response, err := observer.RoundTrip(request)
			require.ErrorIs(t, err, tt.transportErr, "observation must return the exact underlying transport error")
			if !tt.nilResponse && !tt.nilBody {
				require.Equal(t, "", logs.String(), "the observer must not finalize a streaming body at header time")
				if tt.read {
					data := make([]byte, 32)
					n, readErr := response.Body.Read(data)
					require.ErrorIs(t, readErr, tt.readErr, "observation must return the exact underlying read error")
					require.Equal(t, "private-body", string(data[:n]), "observation must preserve bytes returned with an error")
				}
				require.ErrorIs(t, response.Body.Close(), tt.closeErr, "observation must return the exact underlying close error")
				require.ErrorIs(t, response.Body.Close(), tt.closeErr, "repeated Close must retain the underlying body's behavior")
				require.Equal(t, 2, body.closes, "the observer must forward each caller Close without extra hidden closes")
			}
			wantReads := 0
			if tt.read {
				wantReads = 1
			}
			if tt.nilResponse || tt.nilBody {
				wantReads = 0
			}
			require.Equal(t, wantReads, body.reads, "error and redirect closures must never add body reads or drains")
			require.Equal(t, 1, calls, "telemetry must never retry the underlying exchange")
			events := decodeAssetEvents(t, &logs)
			require.Equal(t, 1, len(events), "transport, read, EOF and repeated closure paths must finalize exactly once")
			actualError, _ := events[0]["error"].(string)
			require.Equal(t, tt.expectedError, actualError, "error observations must use only fixed safe diagnostic categories")
			for _, private := range []string{"private-file", "private-query", "private-token", "private-body", "private-location", "private-signature", "private-error"} {
				require.NotContains(t, logs.String(), private, "freeform URL, body, redirect and error details must never reach asset logs")
			}
		})
	}
}

func decodeAssetEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var events []map[string]any
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if err == io.EOF {
			return events
		}
		require.NoError(t, err, "every completed asset observation must be structured JSON")
		events = append(events, event)
	}
}

func TestGitHubAssetTransportKeepsHonestWriteCutoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		early       bool
		writeErr    error
		attempts    float64
		writeErrors float64
		status      string
	}{
		{name: "late callback cannot revise unknown cutoff", status: "unknown"},
		{name: "observed callback records one write", early: true, attempts: 1, status: "callbacks_observed"},
		{name: "callback error is counted without raw error", early: true, writeErr: errors.New("https://github.com/private-callback?signature=private-write"), attempts: 1, writeErrors: 1, status: "callbacks_observed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			var trace *httptrace.ClientTrace
			observer := NewGitHubAssetTransport(roundTripFunc(func(request *http.Request) (*http.Response, error) {
				trace = httptrace.ContextClientTrace(request.Context())
				if tt.early {
					trace.WroteRequest(httptrace.WroteRequestInfo{Err: tt.writeErr})
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
			}), zerolog.New(&logs))
			request, err := http.NewRequest(http.MethodGet, "https://github.com/user-attachments/assets/private-id", nil)
			require.NoError(t, err, "the synthetic write observation request should parse")
			request.Header.Set("Authorization", "Bearer synthetic")
			response, err := observer.RoundTrip(request)
			require.NoError(t, err, "write observation must preserve transport success")
			require.NoError(t, response.Body.Close(), "a summary should finalize at the caller's body close")
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: tt.writeErr})
			events := decodeAssetEvents(t, &logs)
			require.Equal(t, 1, len(events), "late callbacks must not emit another request summary")
			require.Equal(t, tt.attempts, events[0]["github_transport_write_attempts_observed"], "write counts must reflect only callbacks observed before the summary cutoff")
			require.Equal(t, tt.writeErrors, events[0]["github_transport_write_errors_observed"], "write failures must be counted without retaining their error text")
			require.Equal(t, tt.status, events[0]["github_transport_write_observation_status"], "zero callbacks must remain unknown rather than proof of no provider write")
			require.Equal(t, "request_summary", events[0]["github_transport_write_observation_cutoff"], "asset telemetry must preserve the existing callback cutoff contract")
			require.NotContains(t, logs.String(), "private-write", "raw write-callback errors must never reach asset logs")
		})
	}
}

type assetIdleCloseProbe struct {
	roundTripFunc
	closes int
}

func (p *assetIdleCloseProbe) CloseIdleConnections() { p.closes++ }

func TestGitHubAssetTransportPreservesIdleConnectionCleanup(t *testing.T) {
	t.Parallel()
	base := &assetIdleCloseProbe{}
	client := &http.Client{Transport: NewGitHubAssetTransport(base, zerolog.Nop())}
	client.CloseIdleConnections()
	require.Equal(t, 1, base.closes, "asset observation must preserve the base transport's idle connection cleanup")
}

func TestGitHubAssetTransportRequiresExplicitBase(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	request, err := http.NewRequest(http.MethodGet, "https://github.com/user-attachments/assets/private-id?signature=private", nil)
	require.NoError(t, err, "the synthetic missing-base request should parse")
	request.Header.Set("Authorization", "Bearer synthetic")
	response, err := NewGitHubAssetTransport(nil, zerolog.New(&logs)).RoundTrip(request)
	require.EqualError(t, err, "GitHub asset observer requires a base transport", "a missing safe transport must never fall back to an unrestricted default")
	require.Nil(t, response, "a missing base must not produce a fabricated provider response")
	require.Equal(t, "", logs.String(), "no attempted exchange means no remote GitHub observation")
}
