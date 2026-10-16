package sandboxauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	"github.com/assembledhq/143/internal/services/github/telemetry"
)

// SetGitHubRateLimitController is called during construction, before Listen.
func (s *Server) SetGitHubRateLimitController(controller *ratelimit.Controller) {
	s.githubController = controller
}

func (s *Server) handleGitHubControl(ctx context.Context, conn net.Conn, scope *observationScope, req Request, logger zerolog.Logger) {
	if scope == nil {
		s.writeError(conn, "GitHub request binding unavailable")
		return
	}
	if req.Op == OpCoverage {
		metadata := scope.metadata
		switch req.Observer {
		case "sandbox_gh", "sandbox_tools":
			metadata.Caller = req.Observer
		default:
			s.writeError(conn, "unsupported GitHub coverage observer")
			return
		}
		if !validCoverageReason(req.CoverageReason) {
			s.writeError(conn, "unsupported GitHub coverage reason")
			return
		}
		s.logCoverage(metadata, req.CoverageReason)
	} else {
		scope.mu.Lock()
		binding, ok := scope.bindings[req.ObservationID]
		scope.mu.Unlock()
		if !ok || !binding.expires.After(time.Now()) {
			s.logCoverage(scope.metadata, "binding_unavailable")
			s.writeError(conn, "GitHub request binding expired or unavailable")
			return
		}
		if !validGitHubRequestMethod(req.Method) || len(req.Route) > 512 {
			s.logCoverage(binding.metadata, "invalid_control_request")
			s.writeError(conn, "invalid GitHub request admission")
			return
		}
		metadata := binding.metadata
		if req.UnknownPrincipal {
			s.logCoverage(metadata, "unknown_principal")
		} else if metadata.AuthType == telemetry.AuthTypeAppInstallation {
			route := telemetry.NormalizeExternalRoute(req.Route)
			if err := s.githubController.BeforeExternal(ctx, ratelimit.Scope{InstallationID: metadata.InstallationID, Caller: metadata.Caller, Route: route}); err != nil {
				deferral, ok := ratelimit.AsDeferral(err)
				if !ok {
					s.writeError(conn, "GitHub request admission unavailable")
					return
				}
				metrics.RecordGitHubActivity(ctx, metadata.Caller, "", "deferred", "deferral")
				response := Response{RequestControl: true, RetryAt: &deferral.RetryAt, LimitKind: string(deferral.Kind), InstallationID: deferral.InstallationID}
				if encodeErr := json.NewEncoder(conn).Encode(&response); encodeErr != nil {
					logger.Warn().Err(encodeErr).Msg("sandboxauth: write GitHub deferral response")
				}
				return
			}
		}
	}
	if err := json.NewEncoder(conn).Encode(&Response{RequestControl: true}); err != nil {
		logger.Warn().Err(err).Msg("sandboxauth: write GitHub control response")
	}
}

func validGitHubRequestMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func validCoverageReason(reason string) bool {
	switch reason {
	case "supported_control", "legacy_observation_only", "legacy_unobserved_api", "unsupported_command", "explicit_credentials", "custom_host", "missing_binding", "binding_unavailable", "invalid_control_request", "control_unavailable", "control_check_failed", "unknown_principal", "observation_rejected", "observation_report_failed", "observation_queue_full", "observation_timeout":
		return true
	default:
		return false
	}
}

func (s *Server) logCoverage(metadata telemetry.RequestMetadata, reason string) {
	event := s.logger.Info().Str("github_caller", metadata.Caller).Str("github_coverage_reason", reason)
	if metadata.InstallationID > 0 {
		event = event.Int64("github_installation_id", metadata.InstallationID)
	}
	if metadata.AuthType != "" {
		event = event.Str("github_auth_type", metadata.AuthType)
	}
	if metadata.SessionID != "" {
		event = event.Str("session_id", metadata.SessionID)
	}
	if metadata.OrgID != "" {
		event = event.Str("org_id", metadata.OrgID)
	}
	event.Msg("github sandbox coverage")
}

func (c *Client) reportCoverage(ctx context.Context, caller, reason string, stderr io.Writer) {
	if c.SocketPath == "" || !validCoverageReason(reason) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// This reports an existing coverage failure; a reporting error cannot
	// replace the original command result. No token or command text is sent.
	response, err := c.exchange(ctx, Request{Op: OpCoverage, Observer: caller, CoverageReason: reason})
	if err != nil || response.Error != "" {
		fmt.Fprintln(stderr, "143-tools: GitHub coverage reporting unavailable")
	}
}

// githubControlError identifies local admission failures, which must never be
// counted as actual requests in the observation transport.
type githubControlError struct{ cause error }

func (e *githubControlError) Error() string { return e.cause.Error() }
func (e *githubControlError) Unwrap() error { return e.cause }

type githubAdmissionTransport struct {
	base    http.RoundTripper
	client  *Client
	binding *Response
	caller  string
	stderr  io.Writer
}

func (t githubAdmissionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.binding.RequestControl && (req.URL.Host == "api.github.com" || req.URL.Host == "uploads.github.com") {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		unknown, _ := req.Context().Value(unknownGHPrincipal{}).(bool)
		response, err := t.client.exchange(ctx, Request{Op: OpCheck, ObservationID: t.binding.ObservationID, Method: req.Method, Route: telemetry.NormalizeExternalRoute(req.URL.Path), UnknownPrincipal: unknown})
		if err == nil && response.Error != "" {
			err = errors.New("GitHub request admission unavailable")
		}
		if err == nil && !response.RequestControl {
			err = errors.New("GitHub request admission response incomplete")
		}
		if err == nil && response.RetryAt != nil {
			kind := ratelimit.Kind(response.LimitKind)
			if kind.Validate() != nil {
				kind = ratelimit.KindUnknown
			}
			err = &ratelimit.Deferral{InstallationID: response.InstallationID, Kind: kind, RetryAt: *response.RetryAt}
		}
		if err != nil {
			if _, deferred := ratelimit.AsDeferral(err); !deferred {
				stderr := t.stderr
				if stderr == nil {
					stderr = io.Discard
				}
				t.client.reportCoverage(context.WithoutCancel(req.Context()), t.caller, "control_check_failed", stderr)
			}
			if req.Body != nil {
				if closeErr := req.Body.Close(); closeErr != nil {
					err = errors.Join(err, fmt.Errorf("close locally deferred request body: %w", closeErr))
				}
			}
			return nil, &githubControlError{cause: err}
		}
	}
	return t.base.RoundTrip(req)
}
