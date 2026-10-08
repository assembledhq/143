package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type codeReviewOwnershipGuardTest struct{ err error }

func (g codeReviewOwnershipGuardTest) RejectIfAutomationOwned(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (g codeReviewOwnershipGuardTest) RejectIfCodeReviewOwned(_ context.Context, _, _ uuid.UUID) error {
	return g.err
}

func TestRejectCodeReviewOwnedSession(t *testing.T) {
	t.Parallel()
	prID := uuid.New()
	tests := []struct {
		name    string
		err     error
		blocked bool
		status  int
		code    string
	}{
		{name: "unowned session", status: http.StatusOK},
		{name: "owned session", err: &models.SessionCodeReviewOwnedError{PullRequestID: prID}, blocked: true, status: http.StatusConflict, code: "SESSION_CODE_REVIEW_OWNED"},
		{name: "unknown or cross-org session", err: pgx.ErrNoRows, blocked: true, status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "database failure", err: errors.New("database unavailable"), blocked: true, status: http.StatusInternalServerError, code: "SESSION_LOOKUP_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/guarded", nil)
			blocked := rejectCodeReviewOwnedSession(recorder, request, codeReviewOwnershipGuardTest{err: tt.err}, uuid.New(), uuid.New())
			require.Equal(t, tt.blocked, blocked, "ownership check should block only errors")
			if tt.blocked {
				require.Equal(t, tt.status, recorder.Code, "ownership error should map to the expected HTTP status")
				require.Contains(t, recorder.Body.String(), `"code":"`+tt.code+`"`, "ownership error should expose the expected API code")
			}
		})
	}
}

func TestSetResponseWriteDeadline_ExtendsShortServerDeadline(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setResponseWriteDeadline(w, r, 500*time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}))
	server.Config.WriteTimeout = 5 * time.Millisecond
	server.Start()
	defer server.Close()

	resp, err := server.Client().Get(server.URL)
	require.NoError(t, err, "operation-specific response deadline should prevent an EOF from the shorter server timeout")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "delayed handler should complete within its operation-specific budget")
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body), "delayed handler should return a complete JSON response")
	require.Equal(t, map[string]string{"status": "ready"}, body, "delayed handler should preserve its response body")
}

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
