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

// TestModelUnavailableForRetry pins the narrowing that keeps an infrastructure
// blip from burning an automation's whole fallback chain.
//
// Every case asserts BOTH classifiers. ModelUnavailableForRetry is the rule for
// callers that answer a positive by spending another agent run; ModelUnavailable
// is the older, broader rule the code reviewer still uses, and asserting it
// alongside proves this change left that caller's behavior untouched rather
// than merely claiming so.
func TestModelUnavailableForRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// message is the failure text as it reaches the classifier: either a
		// session's raw Error or its generated FailureExplanation.
		message string
		// wantForRetry is whether a DIFFERENT model could plausibly survive
		// this failure, i.e. whether spending another agent run is justified.
		wantForRetry bool
		// wantModelUnavailable is the broader classifier's verdict. It is
		// stated per case so any drift in ModelUnavailable — which the code
		// reviewer's fallback depends on — fails here.
		wantModelUnavailable bool
		why                  string
	}{
		{
			name:                 "model is at capacity",
			message:              "API error: the model is at capacity, please try again later",
			wantForRetry:         true,
			wantModelUnavailable: true,
			why:                  "a capacity marker names the model itself, so another rank is a different outcome",
		},
		{
			name:                 "model is overloaded",
			message:              "Error: model is overloaded",
			wantForRetry:         true,
			wantModelUnavailable: true,
			why:                  "a capacity marker names the model itself, so another rank is a different outcome",
		},
		{
			name:                 "overloaded_error payload",
			message:              `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantForRetry:         true,
			wantModelUnavailable: true,
			why:                  "the provider's structured overload code is the same signal in machine form",
		},
		{
			name:                 "server is overloaded",
			message:              "API error: server is overloaded, retry shortly",
			wantForRetry:         true,
			wantModelUnavailable: true,
			why:                  "a capacity marker names the model itself, so another rank is a different outcome",
		},
		{
			name:                 "rate limited",
			message:              "API error: rate limit exceeded for this credential",
			wantForRetry:         true,
			wantModelUnavailable: true,
			why:                  "a rate limit is per-credential and per-model, so a different rank can still serve the work",
		},
		{
			// THE FIX. "service unavailable" is what any dependency's HTTP 503
			// says — GitHub, the sandbox host, an internal service — so treating
			// it as model capacity gave every rank in a chain its own session to
			// fail identically in.
			name:                 "bare service unavailable from some dependency",
			message:              "request failed: 503 service unavailable",
			wantForRetry:         false,
			wantModelUnavailable: true,
			why:                  "a bare 503 says nothing about which model was asked, so another rank would hit the same wall",
		},
		{
			// The pre-agent setup steps run BEFORE any model is invoked, so the
			// capacity marker riding along in the message cannot be about a
			// model that never got a turn.
			name:                 "github token failure carrying a 503",
			message:              "get installation token: github api returned 503 service unavailable",
			wantForRetry:         false,
			wantModelUnavailable: true,
			why:                  "the run died resolving a GitHub token; no model was reached, so no model can do better",
		},
		{
			name:                 "github token failure carrying a rate limit",
			message:              "get installation token: 429 too many requests from api.github.com",
			wantForRetry:         false,
			wantModelUnavailable: true,
			why:                  "GitHub's own rate limit is not the model's, and every rank would re-hit it",
		},
		{
			name:                 "clone failure carrying a capacity marker",
			message:              "clone repo: fatal: could not read from remote repository: server is overloaded",
			wantForRetry:         false,
			wantModelUnavailable: true,
			why:                  "cloning happens before the agent starts, so the marker cannot be a verdict on the model",
		},
		{
			// The orchestrator formats these with %s off a wrapped error, so
			// surrounding whitespace and casing are not guaranteed. The prefix
			// match has to survive both or the guard silently stops applying.
			name:                 "clone failure with surrounding whitespace and mixed case",
			message:              "  Clone Repo: Fatal: Unable To Access Repository: Service Unavailable\n",
			wantForRetry:         false,
			wantModelUnavailable: true,
			why:                  "the prefix guard must not be defeated by a trailing newline or the message's casing",
		},
		{
			name:                 "ordinary task failure",
			message:              "the unit tests failed: 3 assertions did not hold",
			wantForRetry:         false,
			wantModelUnavailable: false,
			why:                  "a verdict on the work reaches the same place on every model",
		},
		{
			name:                 "empty message",
			message:              "",
			wantForRetry:         false,
			wantModelUnavailable: false,
			why:                  "an unclassifiable failure must never justify spending another agent run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantForRetry, ModelUnavailableForRetry(tt.message),
				"ModelUnavailableForRetry decides whether to spend another agent run: %s", tt.why)
			require.Equal(t, tt.wantModelUnavailable, ModelUnavailable(tt.message),
				"ModelUnavailable is unchanged by the retry narrowing; the code-review fallback still reads it")
		})
	}
}

// TestModelUnavailableForRetry_IsStrictlyNarrowerThanModelUnavailable states
// the relationship between the two classifiers as an invariant rather than
// leaving it to the case table: anything worth another agent run must also be
// something the broader rule recognizes. A retry-worthy failure the broader
// rule rejected would mean the two have drifted apart, and the automation chain
// would be promoting on a signal nothing else in the codebase treats as
// model-related.
func TestModelUnavailableForRetry_IsStrictlyNarrowerThanModelUnavailable(t *testing.T) {
	t.Parallel()

	messages := []string{
		"API error: the model is at capacity",
		"Error: model is overloaded",
		`{"error":{"type":"overloaded_error"}}`,
		"API error: server is overloaded",
		"API error: rate limit exceeded",
		"request failed: 503 service unavailable",
		"get installation token: github api returned 503 service unavailable",
		"clone repo: fatal: could not read from remote repository: server is overloaded",
		"the unit tests failed: 3 assertions did not hold",
	}

	for _, message := range messages {
		if ModelUnavailableForRetry(message) {
			require.True(t, ModelUnavailable(message),
				"ModelUnavailableForRetry must stay a subset of ModelUnavailable: %q", message)
		}
	}
}
