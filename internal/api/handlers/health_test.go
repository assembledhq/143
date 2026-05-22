package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestNewHealthHandler(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(nil)
	require.NotNil(t, handler, "handler should not be nil")
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	h := &HealthHandler{}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.Healthz(w, req)

	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for health check")
	require.Contains(t, w.Body.String(), `"status":"ok"`, "response should contain ok status")
	require.Contains(t, w.Body.String(), `"redis":"unavailable"`, "response should include Redis health status")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "should set JSON content type")
}

func TestHealthz_WithRedisHealthy(t *testing.T) {
	t.Parallel()

	h := &HealthHandler{}
	h.SetRedisHealthCheck(func(context.Context) bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.Healthz(w, req)

	require.Equal(t, http.StatusOK, w.Code, "health check should still return 200")
	require.Contains(t, w.Body.String(), `"redis":"ok"`, "health response should report healthy Redis")
}

func TestHealthz_WhenDraining(t *testing.T) {
	t.Parallel()

	draining := make(chan struct{})
	h := &HealthHandler{}
	h.SetDrainingSignal(draining)
	close(draining)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.Healthz(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "health check should fail while the process is draining")
	require.Contains(t, w.Body.String(), `"status":"draining"`, "health response should report draining status")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "draining health response should set JSON content type")
}

func TestVersion(t *testing.T) {
	t.Parallel()

	h := &HealthHandler{}
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()

	h.Version(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "deploy_sha")
}

func TestParseClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		remoteAddr string
		wantNil    bool
	}{
		{"from X-Forwarded-For", "1.2.3.4", "5.6.7.8:1234", false},
		{"from X-Forwarded-For with chain", "1.2.3.4, 10.0.0.1", "", false},
		{"from RemoteAddr with port", "", "192.168.1.1:8080", false},
		{"from RemoteAddr without port", "", "192.168.1.1", false},
		{"invalid address", "", "not-an-ip", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			result := parseClientIP(req)
			if tt.wantNil {
				require.Nil(t, result)
			} else {
				require.NotNil(t, result)
			}
		})
	}
}

func TestEmitUserAudit_NilEmitter(t *testing.T) {
	t.Parallel()

	// Nil emitter should be a no-op (no panic).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	emitUserAudit(nil, req, "", "", nil, nil)
}

func TestEmitUserAuditWithSession_NilEmitter(t *testing.T) {
	t.Parallel()

	// Nil emitter should be a no-op.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	emitUserAuditWithSession(nil, req, "", "", nil, nil, nil, nil)
}

func TestIsTerminalStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   string
		expected bool
	}{
		{"completed", true},
		{"failed", true},
		{"cancelled", true},
		{"running", false},
		{"pending", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, isTerminalStatus(models.SessionStatus(tt.status)), "isTerminalStatus(%q) should return %v", tt.status, tt.expected)
		})
	}
}
