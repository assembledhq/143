package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestQueryInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      string
		key        string
		defaultVal int
		expected   int
	}{
		{
			name:       "returns default when key is missing",
			query:      "",
			key:        "limit",
			defaultVal: 50,
			expected:   50,
		},
		{
			name:       "returns parsed integer value",
			query:      "limit=10",
			key:        "limit",
			defaultVal: 50,
			expected:   10,
		},
		{
			name:       "returns default for non-integer value",
			query:      "limit=abc",
			key:        "limit",
			defaultVal: 50,
			expected:   50,
		},
		{
			name:       "returns default for negative value",
			query:      "limit=-5",
			key:        "limit",
			defaultVal: 50,
			expected:   50,
		},
		{
			name:       "returns zero when value is zero",
			query:      "limit=0",
			key:        "limit",
			defaultVal: 50,
			expected:   0,
		},
		{
			name:       "returns large value",
			query:      "limit=999",
			key:        "limit",
			defaultVal: 50,
			expected:   999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := "/test"
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			result := queryInt(req, tt.key, tt.defaultVal)
			require.Equal(t, tt.expected, result, "queryInt should return expected value")
		})
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	data := map[string]string{"status": "ok"}
	writeJSON(w, http.StatusOK, data)

	require.Equal(t, http.StatusOK, w.Code, "should return expected status code")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "should set content type")
	require.Contains(t, w.Body.String(), `"status":"ok"`, "should contain JSON data")
}

func TestWriteError(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "something went wrong")

	require.Equal(t, http.StatusBadRequest, w.Code, "should return expected status code")
	require.Contains(t, w.Body.String(), "BAD_REQUEST", "should contain error code")
	require.Contains(t, w.Body.String(), "something went wrong", "should contain error message")
}

func TestWriteErrorLogsWrappedRootError(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := zerolog.New(&logBuffer)
	rootErr := errors.New("insert slack inbound event: constraint failed")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/slack/events", nil)
	r = r.WithContext(logger.WithContext(r.Context()))

	writeError(w, r, http.StatusInternalServerError, "SLACK_EVENT_PERSIST_FAILED", "failed to persist Slack event", rootErr)

	require.Equal(t, http.StatusInternalServerError, w.Code, "writeError should return expected status code")
	var logEvent map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &logEvent), "writeError should emit a structured log event")
	require.Equal(t, "error", logEvent["level"], "5xx writeError should log at error level")
	require.Equal(t, "SLACK_EVENT_PERSIST_FAILED", logEvent["code"], "writeError should include stable API error code")
	require.Contains(t, logEvent["error"], "constraint failed", "writeError should include the wrapped root error")
}
