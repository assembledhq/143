package sandboxauth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/telemetry"
)

func TestSandboxCoverageReports(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason string
		accepted     bool
	}{
		{name: "excluded command", reason: "unsupported_command", accepted: true},
		{name: "explicit credentials", reason: "explicit_credentials", accepted: true},
		{name: "custom host", reason: "custom_host", accepted: true},
		{name: "missing client binding", reason: "missing_binding", accepted: true},
		{name: "older host control unavailable", reason: "control_unavailable", accepted: true},
		{name: "admission exchange failed", reason: "control_check_failed", accepted: true},
		{name: "observation report failed", reason: "observation_report_failed", accepted: true},
		{name: "observation queue full", reason: "observation_queue_full", accepted: true},
		{name: "observation drain timed out", reason: "observation_timeout", accepted: true},
		{name: "forged capability declaration", reason: "control_capability_declared"},
		{name: "forged previous capability name", reason: "supported_control"},
		{name: "forged current observation classification", reason: "current_observation_only"},
		{name: "forged legacy observation classification", reason: "legacy_observation_only"},
		{name: "forged current credential classification", reason: "current_unobserved_api"},
		{name: "forged legacy credential classification", reason: "legacy_unobserved_api"},
		{name: "forged host binding validation", reason: "binding_unavailable"},
		{name: "forged host request validation", reason: "invalid_control_request"},
		{name: "forged host observation validation", reason: "observation_rejected"},
		{name: "principal declaration requires bound admission", reason: "unknown_principal"},
		{name: "arbitrary private payload", reason: "fixture-private-report-payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, scope, resolver, logs := newCoverageFixture(t)
			response := exchangeCoverageFixture(t, server, scope, map[string]any{
				"op": OpCoverage, "observer": "sandbox_gh", "coverage_reason": tt.reason,
				"github_coverage_source": "host", "github_coverage_trusted": true, "installation_id": 99,
			})
			expected := []map[string]any{}
			if tt.accepted {
				require.Empty(t, response.Error, "bounded client reports should remain available")
				metadata := scope.metadata
				metadata.Caller = "sandbox_gh"
				expected = append(expected, expectedCoverageEvent(metadata, tt.reason, "sandbox", false))
			} else {
				require.Equal(t, "unsupported GitHub coverage reason", response.Error, "clients must not submit host-derived or arbitrary coverage reasons")
			}
			require.Equal(t, expected, coverageEvents(t, logs.String()), "accepted reports must have host-bound context and untrusted sandbox provenance")
			require.Empty(t, response.Token, "coverage acknowledgements must never return credentials")
			require.Zero(t, resolver.calls, "coverage reports must not mint credentials or make provider calls")
			require.NotContains(t, logs.String(), "github api request", "coverage reports must not count as outbound API requests")
			require.NotContains(t, logs.String(), "fixture-private-report-payload", "invalid report contents must not be reflected into logs")
		})
	}
}

func TestSandboxCredentialCoverageClassifications(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason string
		version      int
		observed     bool
		control      bool
		exclusion    bool
	}{
		{name: "legacy unobserved credential", reason: "legacy_unobserved_api"},
		{name: "current auth-token credential", version: currentHelperVersion, reason: "current_unobserved_api"},
		{name: "current excluded gh command", version: currentHelperVersion, reason: "current_unobserved_api", exclusion: true},
		{name: "legacy observed helper", observed: true, reason: "legacy_observation_only"},
		{name: "current observation-only helper", version: currentHelperVersion, observed: true, reason: "current_observation_only"},
		{name: "declared request control", version: currentHelperVersion, observed: true, control: true, reason: "control_capability_declared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, scope, resolver, logs := newCoverageFixture(t)
			expected := []map[string]any{}
			if tt.exclusion {
				response := exchangeCoverageFixture(t, server, scope, Request{Op: OpCoverage, Observer: "sandbox_gh", CoverageReason: "unsupported_command"})
				require.Empty(t, response.Error, "current wrapper exclusion should report independently of credential issuance")
				metadata := scope.metadata
				metadata.Caller = "sandbox_gh"
				expected = append(expected, expectedCoverageEvent(metadata, "unsupported_command", "sandbox", false))
			}
			response := exchangeCoverageFixture(t, server, scope, Request{Op: OpGet, Action: ActionAPI, HelperVersion: tt.version, WantObservation: tt.observed, WantRequestControl: tt.control})
			require.Empty(t, response.Error, "legacy and current credential requests should remain compatible")
			require.Equal(t, "fixture-token", response.Token, "coverage classification must preserve credential issuance")
			metadata := scope.metadata
			metadata.AuthType, metadata.InstallationID = telemetry.AuthTypeAppInstallation, 42
			if tt.observed {
				metadata.Caller = "sandbox_gh"
			}
			expected = append(expected, expectedCoverageEvent(metadata, tt.reason, "host", true))
			require.Equal(t, expected, coverageEvents(t, logs.String()), "host coverage must distinguish current unobserved use and legacy helpers without treating declarations as remote requests")
			require.Equal(t, 1, resolver.calls, "credential coverage should not add provider lookups")
			require.NotContains(t, logs.String(), "github api request", "credential issuance and command exclusions must not count hidden API calls")
			require.NotContains(t, logs.String(), "fixture-token", "coverage events must not contain credentials")
		})
	}
}

func TestSandboxInvalidAdmissionCoverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, method, route string
	}{
		{name: "unsupported method", method: http.MethodTrace, route: "/graphql"},
		{name: "oversized route", method: http.MethodGet, route: strings.Repeat("fixture-private-route", 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, scope, resolver, logs := newCoverageFixture(t)
			binding := scope.bind(resolver.resolution)
			response := exchangeCoverageFixture(t, server, scope, Request{Op: OpCheck, ObservationID: binding, Method: tt.method, Route: tt.route})
			require.Equal(t, "invalid GitHub request admission", response.Error, "invalid control metadata must fail before admission or dispatch")
			require.Empty(t, response.Token, "invalid admission must never return credentials")
			metadata := scope.metadata
			metadata.Caller, metadata.AuthType, metadata.InstallationID = "sandbox_gh", telemetry.AuthTypeAppInstallation, 42
			require.Equal(t, []map[string]any{expectedCoverageEvent(metadata, "invalid_control_request", "host", true)}, coverageEvents(t, logs.String()), "invalid requests should produce bounded host validation evidence")
			require.Zero(t, resolver.calls, "invalid control requests must not resolve credentials or contact GitHub")
			require.NotContains(t, logs.String(), "github api request", "rejected admission must not emit outbound request telemetry")
			require.NotContains(t, logs.String(), "fixture-private-route", "invalid raw routes must not enter logs")
		})
	}
}

func TestSandboxCurrentClientCredentialMarker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		action   Action
		observed bool
	}{
		{name: "unobserved api and auth-token", action: ActionAPI},
		{name: "git credential", action: ActionPush},
		{name: "observed api", action: ActionAPI, observed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requests := make(chan Request, 1)
			socket := startSocketServer(t, func(conn net.Conn) {
				var request Request
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					t.Errorf("decode current helper request: %v", err)
					return
				}
				requests <- request
				if err := json.NewEncoder(conn).Encode(&Response{Token: "fixture-token"}); err != nil {
					t.Errorf("encode current helper fixture: %v", err)
				}
			})
			client := NewClient(socket)
			var response *Response
			var err error
			if tt.observed {
				response, err = client.GetObservedAPIToken(context.Background())
			} else {
				response, err = client.Get(context.Background(), tt.action)
			}
			require.NoError(t, err, "additive helper declarations must preserve credential protocol compatibility")
			require.Equal(t, "fixture-token", response.Token, "marker negotiation must not change credential resolution")
			expected := Request{Op: OpGet, Action: tt.action, HelperVersion: currentHelperVersion, WantObservation: tt.observed, WantRequestControl: tt.observed}
			require.Equal(t, expected, <-requests, "all current credential paths must declare helper capability without changing their observation requirements")
		})
	}
}

func TestSandboxUnknownPrincipalCoverageProvenance(t *testing.T) {
	t.Parallel()
	server, scope, resolver, logs := newCoverageFixture(t)
	binding := scope.bind(resolver.resolution)
	response := exchangeCoverageFixture(t, server, scope, Request{Op: OpCheck, ObservationID: binding, Method: http.MethodGet, Route: "/graphql", UnknownPrincipal: true})
	require.Empty(t, response.Error, "an alternate credential should remain outside installation control")
	require.Empty(t, response.Token, "alternate-principal admission must not return a credential")
	metadata := scope.metadata
	metadata.Caller, metadata.AuthType, metadata.PrincipalUnresolved = "sandbox_gh", telemetry.AuthTypeUnknown, true
	require.Equal(t, []map[string]any{expectedCoverageEvent(metadata, "unknown_principal", "sandbox", false)}, coverageEvents(t, logs.String()), "a helper's unknown-principal declaration must not inherit the bound installation or trusted provenance")
	require.Zero(t, resolver.calls, "alternate principal reporting must not add identity lookups")
}

func newCoverageFixture(t *testing.T) (*Server, *observationScope, *stubResolver, *ghLogBuffer) {
	t.Helper()
	logs := &ghLogBuffer{}
	resolver := &stubResolver{resolution: &identity.Resolution{Token: "fixture-token", Source: identity.SourceApp, InstallationID: 42}}
	server := NewServer(resolver, shortSocketDir(t), zerolog.New(logs))
	scope := newObservationScope(uuid.New(), &models.Session{OrgID: uuid.New()}, &models.Repository{FullName: "example/fixture"})
	return server, scope, resolver, logs
}

func exchangeCoverageFixture(t *testing.T, server *Server, scope *observationScope, request any) Response {
	t.Helper()
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleConn(context.Background(), host, &models.Session{}, &models.Repository{}, zerolog.Nop(), scope)
	}()
	require.NoError(t, json.NewEncoder(client).Encode(request), "coverage fixture should send one bounded protocol request")
	var response Response
	require.NoError(t, json.NewDecoder(client).Decode(&response), "coverage fixture should receive a structured protocol response")
	require.NoError(t, client.Close(), "coverage fixture should close its connection")
	<-done
	return response
}

func coverageEvents(t *testing.T, logs string) []map[string]any {
	t.Helper()
	events := []map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(logs))
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if err == io.EOF {
			return events
		}
		require.NoError(t, err, "coverage logs should remain valid structured JSON")
		if event["message"] == "github sandbox coverage" {
			events = append(events, event)
		}
	}
}

func expectedCoverageEvent(metadata telemetry.RequestMetadata, reason, source string, trusted bool) map[string]any {
	event := map[string]any{
		"level": "info", "message": "github sandbox coverage", "github_caller": metadata.Caller,
		"github_coverage_reason": reason, "github_coverage_source": source, "github_coverage_trusted": trusted,
	}
	if metadata.AuthType != "" {
		event["github_auth_type"] = metadata.AuthType
	}
	if metadata.InstallationID > 0 {
		event["github_installation_id"] = float64(metadata.InstallationID)
	}
	if metadata.PrincipalUnresolved {
		event["github_principal_unresolved"] = true
	}
	if metadata.SessionID != "" {
		event["session_id"] = metadata.SessionID
	}
	if metadata.OrgID != "" {
		event["org_id"] = metadata.OrgID
	}
	return event
}
