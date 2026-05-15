package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// Well-known failure categories.
const (
	FailureCategoryTooling        = "tooling"
	FailureCategoryContext        = "context"
	FailureCategoryValidation     = "validation"
	FailureCategoryComplexity     = "complexity"
	FailureCategoryCodexAuth      = "codex_auth_expired"
	FailureCategoryClaudeCodeAuth = "claude_code_auth_expired"
	// FailureCategoryCodexAuthInject is set when codex auth.json injection
	// into the sandbox failed for non-auth reasons (Docker container missing,
	// exec/write transport error, etc). Distinct from codex_auth_expired so
	// the UI does not push the user to re-authenticate when the credential
	// is fine.
	FailureCategoryCodexAuthInject = "codex_auth_inject_failed"
	// FailureCategoryTimeout marks a session that hit its configured
	// wall-clock limit. Set explicitly by the orchestrator timeout path so
	// classification does not depend on error-text matching in classifyFailure.
	FailureCategoryTimeout = "session_timeout"
	// FailureCategoryRecovery marks a session that could not be safely recovered
	// after repeated worker losses before any durable checkpoint existed.
	FailureCategoryRecovery = "recovery_exhausted"
	// FailureCategorySandboxCapacity marks a session whose job exhausted its
	// retry budget while waiting for a sandbox slot.
	FailureCategorySandboxCapacity = "sandbox_capacity"
)

// FailureSummary holds a human-readable explanation of why an agent run failed,
// along with classification metadata and actionable next steps.
type FailureSummary struct {
	Explanation  string   // 1-3 sentence human-readable explanation
	Category     string   // context, complexity, tooling, validation
	SubType      string   // detailed sub-type for system learning
	NextSteps    []string // 2-3 actionable suggestions
	RetryAdvised bool     // should the user try again?
}

// FailureRunUpdater defines the subset of agent run store operations needed by FailureService.
type FailureRunUpdater interface {
	UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
}

// FailureService classifies agent run failures and generates user-facing explanations.
type FailureService struct {
	sessions FailureRunUpdater
	logger   zerolog.Logger
}

// NewFailureService creates a new FailureService.
func NewFailureService(sessions FailureRunUpdater, logger zerolog.Logger) *FailureService {
	return &FailureService{
		sessions: sessions,
		logger:   logger,
	}
}

// AnalyzeFailure classifies the failure for a completed agent run and returns
// a FailureSummary with a human-readable explanation, category, and next steps.
func (s *FailureService) AnalyzeFailure(ctx context.Context, run *models.Session) (*FailureSummary, error) {
	if run == nil {
		return nil, fmt.Errorf("agent run is nil")
	}

	summary := s.classifyFailure(run)

	s.logger.Info().
		Str("session_id", run.ID.String()).
		Str("category", summary.Category).
		Str("sub_type", summary.SubType).
		Bool("retry_advised", summary.RetryAdvised).
		Msg("classified agent run failure")

	return summary, nil
}

// classifyFailure applies rule-based classification to determine the failure
// category, sub-type, explanation, and next steps.
func (s *FailureService) classifyFailure(run *models.Session) *FailureSummary {
	errorMsg := ""
	if run.Error != nil {
		errorMsg = strings.ToLower(*run.Error)
	}

	resultSummary := ""
	if run.ResultSummary != nil {
		resultSummary = strings.ToLower(*run.ResultSummary)
	}

	diff := ""
	if run.Diff != nil {
		diff = *run.Diff
	}

	// 1. Timeout detection. The orchestrator's session-timeout path now
	// classifies explicitly via failRunWithCategory(FailureCategoryTimeout),
	// so this branch only fires for async-classified failures whose error
	// text happens to contain a timeout shape — including network/IO
	// timeouts ("dial tcp: i/o timed out"), upstream API deadline errors,
	// and anything else bubbled up as a plain string. The explanation
	// below is deliberately generic because we can't distinguish a session
	// deadline from a transient socket timeout at this point.
	//
	// Category is Tooling (not Timeout) on purpose: Timeout means "the
	// session's own wall-clock budget was exceeded" — a signal to raise
	// max_session_duration_seconds. A network/IO timeout bubbling up here
	// is an infra/tooling issue and shouldn't push admins to raise the
	// session budget. Keep these two categories semantically distinct.
	if containsAny(errorMsg, "timeout", "deadline exceeded", "timed out") {
		return &FailureSummary{
			Explanation:  "The agent ran out of time before completing the fix. This usually means the issue requires more analysis than the allocated time window allows.",
			Category:     FailureCategoryTooling,
			SubType:      "timeout",
			NextSteps:    []string{"Retry with a higher complexity tier to allow more time", "Break the issue into smaller, more focused sub-tasks", "Provide additional context about the root cause to speed up analysis"},
			RetryAdvised: true,
		}
	}

	// 2. Sandbox crash (OOM, killed, signal, crash)
	if containsAny(errorMsg, "oom", "killed", "signal", "crash", "out of memory") {
		return &FailureSummary{
			Explanation:  "The agent's sandbox environment crashed, likely due to running out of memory or being terminated by the system. This is an infrastructure issue, not a problem with the fix approach.",
			Category:     FailureCategoryTooling,
			SubType:      "sandbox_crash",
			NextSteps:    []string{"Retry the run — sandbox crashes are usually transient", "If this keeps happening, the repository may need more resources to build", "Contact support if crashes persist across multiple retries"},
			RetryAdvised: true,
		}
	}

	// 3. API error (rate limit, 429, 503, API error)
	if containsAny(errorMsg, "rate limit", "429", "503", "api error") {
		return &FailureSummary{
			Explanation:  "The agent encountered an API error, likely due to rate limiting or a temporary service outage. This is not related to the quality of the fix.",
			Category:     FailureCategoryTooling,
			SubType:      "api_error",
			NextSteps:    []string{"Wait a few minutes and retry", "Check the status page for any ongoing service incidents", "If rate limiting persists, consider reducing concurrent agent runs"},
			RetryAdvised: true,
		}
	}

	// 4. Build failure
	if containsAny(errorMsg, "build failed", "compilation error", "syntax error") {
		return &FailureSummary{
			Explanation:  "The agent's changes caused a build failure. The generated code had compilation or syntax errors that prevented a successful build.",
			Category:     FailureCategoryTooling,
			SubType:      "build_failure",
			NextSteps:    []string{"Review the build logs for specific errors", "Ensure the repository's build toolchain is properly configured", "Retry — the agent may produce a different, valid fix on a second attempt"},
			RetryAdvised: true,
		}
	}

	// 5. Empty diff with no error — agent couldn't figure out what to change
	if diff == "" && errorMsg == "" {
		return &FailureSummary{
			Explanation:  "The agent was unable to produce a code change. It could not identify the right files to modify or determine a clear fix for this issue.",
			Category:     FailureCategoryContext,
			SubType:      "missing_context",
			NextSteps:    []string{"Add more detail to the issue description, especially which files or functions are involved", "Link related issues or stack traces to provide more context", "Consider manually pointing the agent to the relevant code area"},
			RetryAdvised: false,
		}
	}

	// 6. Test regression (check result_summary and error for test failure signals)
	if containsAny(errorMsg, "test failed", "test regression", "tests failed") ||
		containsAny(resultSummary, "test failed", "test regression", "tests failed") {
		return &FailureSummary{
			Explanation:  "The agent produced a fix, but it caused existing tests to fail. The change was rejected to protect against regressions.",
			Category:     FailureCategoryValidation,
			SubType:      "test_regression",
			NextSteps:    []string{"Review which tests failed to understand the regression", "The fix may be partially correct — consider refining the approach manually", "Retry with more context about the expected test behavior"},
			RetryAdvised: true,
		}
	}

	// 7. Security violation
	if containsAny(errorMsg, "security violation", "security scan", "vulnerability") ||
		containsAny(resultSummary, "security violation", "security scan", "vulnerability") {
		return &FailureSummary{
			Explanation:  "The agent's fix was rejected because it introduced a security concern. The security scan flagged potential vulnerabilities in the generated code.",
			Category:     FailureCategoryValidation,
			SubType:      "security_violation",
			NextSteps:    []string{"Review the security scan results to understand the specific concern", "A human developer should address this issue with security best practices in mind", "Consider adding security-related context or constraints to the issue description"},
			RetryAdvised: false,
		}
	}

	// 8. Large diff — likely too complex
	if diff != "" && countLines(diff) > 500 {
		return &FailureSummary{
			Explanation:  "The agent produced a very large change spanning many lines. Changes of this size are often unreliable and may indicate the issue requires a more targeted approach.",
			Category:     FailureCategoryComplexity,
			SubType:      "multi_file_scope",
			NextSteps:    []string{"Break the issue into smaller, more focused sub-tasks", "Identify the core change needed and create a narrower issue", "Consider having a human developer handle this architectural change"},
			RetryAdvised: false,
		}
	}

	// 9. Default — not enough signal to classify specifically
	return &FailureSummary{
		Explanation:  "The agent was unable to produce a successful fix. There wasn't enough context to determine a clear solution for this issue.",
		Category:     "context",
		SubType:      "missing_context",
		NextSteps:    []string{"Add more detail to the issue description", "Provide specific file paths or function names related to the bug", "Include stack traces or reproduction steps if available"},
		RetryAdvised: false,
	}
}

// UpdateRunWithFailure persists the failure analysis results to the session record.
func (s *FailureService) UpdateRunWithFailure(ctx context.Context, orgID, runID uuid.UUID, summary *FailureSummary) error {
	if err := s.sessions.UpdateFailure(ctx, orgID, runID, summary.Explanation, summary.Category, summary.NextSteps, summary.RetryAdvised); err != nil {
		return fmt.Errorf("update agent run failure: %w", err)
	}

	s.logger.Info().
		Str("session_id", runID.String()).
		Str("category", summary.Category).
		Msg("updated agent run with failure analysis")

	return nil
}

// containsAny checks if s contains any of the given substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// countLines returns the number of newline-separated lines in s.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
