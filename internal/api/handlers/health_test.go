package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	h := &HealthHandler{}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.Healthz(w, req)

	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for health check")
	require.Contains(t, w.Body.String(), `"status":"ok"`, "response should contain ok status")
}
