package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newTestFailureService(t *testing.T) (*FailureService, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	store := db.NewSessionStore(mock)
	svc := NewFailureService(store, zerolog.Nop())
	return svc, mock
}

func TestClassifyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		run              models.Session
		wantCategory     string
		wantSubType      string
		wantRetryAdvised bool
	}{
		{
			name: "timeout error",
			run: models.Session{
				Error: strPtr("operation timeout after 5m"),
			},
			wantCategory:     "tooling",
			wantSubType:      "timeout",
			wantRetryAdvised: true,
		},
		{
			name: "deadline exceeded",
			run: models.Session{
				Error: strPtr("context deadline exceeded"),
			},
			wantCategory:     "tooling",
			wantSubType:      "timeout",
			wantRetryAdvised: true,
		},
		{
			name: "OOM crash",
			run: models.Session{
				Error: strPtr("process killed: OOM"),
			},
			wantCategory:     "tooling",
			wantSubType:      "sandbox_crash",
			wantRetryAdvised: true,
		},
		{
			name: "signal crash",
			run: models.Session{
				Error: strPtr("process received signal: SIGKILL"),
			},
			wantCategory:     "tooling",
			wantSubType:      "sandbox_crash",
			wantRetryAdvised: true,
		},
		{
			name: "out of memory",
			run: models.Session{
				Error: strPtr("container out of memory"),
			},
			wantCategory:     "tooling",
			wantSubType:      "sandbox_crash",
			wantRetryAdvised: true,
		},
		{
			name: "rate limit API error",
			run: models.Session{
				Error: strPtr("API error: rate limit exceeded"),
			},
			wantCategory:     "tooling",
			wantSubType:      "api_error",
			wantRetryAdvised: true,
		},
		{
			name: "429 status code",
			run: models.Session{
				Error: strPtr("HTTP 429 Too Many Requests"),
			},
			wantCategory:     "tooling",
			wantSubType:      "api_error",
			wantRetryAdvised: true,
		},
		{
			name: "503 service unavailable",
			run: models.Session{
				Error: strPtr("upstream returned 503"),
			},
			wantCategory:     "tooling",
			wantSubType:      "api_error",
			wantRetryAdvised: true,
		},
		{
			name: "codex responses stream disconnected",
			run: models.Session{
				Error: strPtr(`{"type":"turn.failed","error":{"message":"stream disconnected before completion: error sending request for url (https://chatgpt.com/backend-api/codex/responses)"}}`),
			},
			wantCategory:     "tooling",
			wantSubType:      "upstream_transport",
			wantRetryAdvised: true,
		},
		{
			name: "codex rmcp transport channel closed on resume",
			run: models.Session{
				Error: strPtr(`restored-workspace fallback after stale agent resume failed: codex CLI exited with code 1: 2026-06-15T09:17:28Z ERROR rmcp::transport::worker: worker quit with fatal: Transport channel closed, when Client(HttpRequest(HttpRequest("http/request failed: error sending request for url (https://chatgpt.com/backend-api/wham/apps)")))`),
			},
			wantCategory:     "tooling",
			wantSubType:      "upstream_transport",
			wantRetryAdvised: true,
		},
		{
			name: "build failure",
			run: models.Session{
				Error: strPtr("build failed: exit code 1"),
			},
			wantCategory:     "tooling",
			wantSubType:      "build_failure",
			wantRetryAdvised: true,
		},
		{
			name: "compilation error",
			run: models.Session{
				Error: strPtr("compilation error in main.go:42"),
			},
			wantCategory:     "tooling",
			wantSubType:      "build_failure",
			wantRetryAdvised: true,
		},
		{
			name: "syntax error",
			run: models.Session{
				Error: strPtr("syntax error: unexpected token"),
			},
			wantCategory:     "tooling",
			wantSubType:      "build_failure",
			wantRetryAdvised: true,
		},
		{
			name: "empty diff no error",
			run:  models.Session{
				// No error, no diff
			},
			wantCategory:     "context",
			wantSubType:      "missing_context",
			wantRetryAdvised: false,
		},
		{
			name: "test regression in error",
			run: models.Session{
				Error: strPtr("test failed: TestFoo"),
				Diff:  strPtr("some diff content"),
			},
			wantCategory:     "validation",
			wantSubType:      "test_regression",
			wantRetryAdvised: true,
		},
		{
			name: "test regression in result summary",
			run: models.Session{
				ResultSummary: strPtr("Fix applied but tests failed"),
				Diff:          strPtr("some diff content"),
			},
			wantCategory:     "validation",
			wantSubType:      "test_regression",
			wantRetryAdvised: true,
		},
		{
			name: "security violation in error",
			run: models.Session{
				Error: strPtr("security violation detected in generated code"),
				Diff:  strPtr("some diff content"),
			},
			wantCategory:     "validation",
			wantSubType:      "security_violation",
			wantRetryAdvised: false,
		},
		{
			name: "security violation in result summary",
			run: models.Session{
				ResultSummary: strPtr("Security scan flagged vulnerability"),
				Diff:          strPtr("some diff content"),
			},
			wantCategory:     "validation",
			wantSubType:      "security_violation",
			wantRetryAdvised: false,
		},
		{
			name: "large diff over 500 lines",
			run: models.Session{
				Diff: strPtr(strings.Repeat("line\n", 501)),
			},
			wantCategory:     "complexity",
			wantSubType:      "multi_file_scope",
			wantRetryAdvised: false,
		},
		{
			name: "default classification with error and small diff",
			run: models.Session{
				Error: strPtr("something unknown happened"),
				Diff:  strPtr("a small change"),
			},
			wantCategory:     "context",
			wantSubType:      "missing_context",
			wantRetryAdvised: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, mock := newTestFailureService(t)
			defer mock.Close()

			summary, err := svc.AnalyzeFailure(context.Background(), &tt.run)
			require.NoError(t, err, "AnalyzeFailure should not return an error")
			require.NotNil(t, summary, "AnalyzeFailure should return a non-nil summary")

			require.Equal(t, tt.wantCategory, summary.Category, "failure category should match expected value")
			require.Equal(t, tt.wantSubType, summary.SubType, "failure sub-type should match expected value")
			require.Equal(t, tt.wantRetryAdvised, summary.RetryAdvised, "retry advised flag should match expected value")
			require.NotEmpty(t, summary.Explanation, "explanation should not be empty")
			require.GreaterOrEqual(t, len(summary.NextSteps), 2, "should have at least 2 next steps")
			require.LessOrEqual(t, len(summary.NextSteps), 3, "should have at most 3 next steps")
		})
	}
}

func TestAnalyzeFailure_NilRun(t *testing.T) {
	t.Parallel()

	svc, mock := newTestFailureService(t)
	defer mock.Close()

	summary, err := svc.AnalyzeFailure(context.Background(), nil)
	require.Error(t, err, "AnalyzeFailure should return an error for nil run")
	require.Nil(t, summary, "summary should be nil when run is nil")
	require.Contains(t, err.Error(), "nil", "error message should mention nil")
}

func TestUpdateRunWithFailure(t *testing.T) {
	t.Parallel()

	svc, mock := newTestFailureService(t)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	summary := &FailureSummary{
		Explanation:  "The agent timed out.",
		Category:     "tooling",
		SubType:      "timeout",
		NextSteps:    []string{"Retry", "Break into smaller tasks"},
		RetryAdvised: true,
	}

	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := svc.UpdateRunWithFailure(context.Background(), orgID, runID, summary)
	require.NoError(t, err, "UpdateRunWithFailure should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRetryAdvised_ToolingCategoriesAreRetryable(t *testing.T) {
	t.Parallel()

	toolingErrors := []string{
		"timeout occurred",
		"process killed by OOM",
		"rate limit exceeded",
		"build failed with errors",
	}

	svc, mock := newTestFailureService(t)
	defer mock.Close()

	for _, errMsg := range toolingErrors {
		run := &models.Session{Error: strPtr(errMsg)}
		summary, err := svc.AnalyzeFailure(context.Background(), run)
		require.NoError(t, err, "AnalyzeFailure should not return an error for: %s", errMsg)
		require.Equal(t, "tooling", summary.Category, "error: %s", errMsg)
		require.True(t, summary.RetryAdvised, "tooling error should advise retry: %s", errMsg)
	}
}

func TestRetryAdvised_SecurityNeverRetryable(t *testing.T) {
	t.Parallel()

	svc, mock := newTestFailureService(t)
	defer mock.Close()

	run := &models.Session{
		Error: strPtr("security violation: SQL injection detected"),
		Diff:  strPtr("some changes"),
	}

	summary, err := svc.AnalyzeFailure(context.Background(), run)
	require.NoError(t, err, "AnalyzeFailure should not return an error")
	require.Equal(t, "validation", summary.Category, "security violation should be classified as validation")
	require.Equal(t, "security_violation", summary.SubType, "security violation should have correct sub-type")
	require.False(t, summary.RetryAdvised, "security violations should never advise retry")
}

func TestCountLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"one line", 1},
		{"line1\nline2", 2},
		{"line1\nline2\nline3\n", 4},
		{strings.Repeat("x\n", 500), 501},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, countLines(tt.input), "countLines should return correct line count for input")
		})
	}
}

func TestContainsAny(t *testing.T) {
	t.Parallel()

	require.True(t, containsAny("hello world", "world"), "containsAny should find 'world' in 'hello world'")
	require.True(t, containsAny("hello world", "foo", "world"), "containsAny should find 'world' among multiple substrings")
	require.False(t, containsAny("hello world", "foo", "bar"), "containsAny should return false when no substrings match")
	require.False(t, containsAny("", "foo"), "containsAny should return false for empty string")
}
