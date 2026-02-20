package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "should set JSON content type")
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
			require.Equal(t, tt.expected, isTerminalStatus(tt.status), "isTerminalStatus(%q) should return %v", tt.status, tt.expected)
		})
	}
}
