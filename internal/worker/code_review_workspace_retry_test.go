package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewWorkspacePreparationFailureClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stage string
		cause       error
		expired     bool
		cancelled   bool
		ctxDeadline bool
		retry       bool
		deadline    bool
	}{
		{name: "clone transient DNS", stage: agent.CodeReviewPreparationStageClone, cause: &net.DNSError{Err: "temporary failure", IsTemporary: true}, retry: true},
		{name: "hydrate transient transport", stage: agent.CodeReviewPreparationStageHydration, cause: io.ErrUnexpectedEOF, retry: true},
		{name: "checkout temporary HTTP failure", stage: agent.CodeReviewPreparationStageCheckout, cause: &ghservice.GitHubAPIError{StatusCode: 503}, retry: true},
		{name: "Git subprocess network failure", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("prepare evidence: exit=128 stderr=fatal: unable to access URL: Could not resolve host: github.com"), retry: true},
		{name: "Git HTTP 500", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("the requested URL returned error: 500"), retry: true},
		{name: "Git curl request timeout", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("curl 28 Operation timed out after 60000 milliseconds with 0 bytes received"), retry: true},
		{name: "Git HTTP 404 is deterministic", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("the requested URL returned error: 404")},
		{name: "Git HTTP 502", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("the requested URL returned error: 502"), retry: true},
		{name: "invalid pinned revision is deterministic", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("review workspace is missing or has an invalid pinned revision")},
		{name: "source integrity failure is deterministic", stage: agent.CodeReviewPreparationStageHydration, cause: agent.ErrCodeReviewSourceIntegrity},
		{name: "source integrity failure never retries joined transport", stage: agent.CodeReviewPreparationStageHydration, cause: errors.Join(agent.ErrCodeReviewSourceIntegrity, io.ErrUnexpectedEOF)},
		{name: "source integrity failure never retries transport diagnostics", stage: agent.CodeReviewPreparationStageHydration, cause: errors.Join(agent.ErrCodeReviewSourceIntegrity, errors.New("connection reset by peer"))},
		{name: "source integrity failure after original deadline stays budget exhaustion", stage: agent.CodeReviewPreparationStageHydration, cause: agent.ErrCodeReviewSourceIntegrity, expired: true, deadline: true},
		{name: "deadline context owns source integrity failure", stage: agent.CodeReviewPreparationStageHydration, cause: agent.ErrCodeReviewSourceIntegrity, ctxDeadline: true, deadline: true},
		{name: "cancelled context owns source integrity failure", stage: agent.CodeReviewPreparationStageHydration, cause: agent.ErrCodeReviewSourceIntegrity, cancelled: true},
		{name: "missing Git binary is deterministic", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("git: command not found")},
		{name: "auth denied is deterministic", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("Authentication failed")},
		{name: "failed bootstrap never retries even transport", stage: agent.CodeReviewPreparationStageDependencySetup, cause: &net.DNSError{Err: "temporary failure", IsTemporary: true}},
		{name: "readiness failure is not source evidence", stage: agent.CodeReviewPreparationStageReadiness, cause: errors.New("missing python3")},
		{name: "deadline remains terminal", stage: agent.CodeReviewPreparationStageHydration, cause: context.DeadlineExceeded, deadline: true},
		{name: "cancellation remains terminal", stage: agent.CodeReviewPreparationStageCheckout, cause: context.Canceled},
		{name: "generic error after original deadline is budget exhaustion", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("connection reset by peer"), expired: true, deadline: true},
		{name: "deadline context owns generic returned failure", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("connection reset by peer"), ctxDeadline: true, deadline: true},
		{name: "cancelled context owns generic returned failure", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("connection reset by peer"), cancelled: true},
		{name: "moved head is terminal supersession signal", stage: agent.CodeReviewPreparationStageCheckout, cause: agent.ErrStalePullRequestHead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			started := time.Now().UTC().Add(-time.Minute)
			if tt.expired {
				started = started.Add(-codeReviewWorkspaceWaitLimit)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.ctxDeadline {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			if tt.cancelled {
				cancel()
			}
			actual := classifyCodeReviewWorkspacePreparationFailure(ctx, agent.NewCodeReviewWorkspacePreparationError(tt.stage, tt.cause), started)
			var retry *RetryableError
			if tt.retry {
				require.ErrorAs(t, actual, &retry, "only concrete transient source failures may retry")
				require.Equal(t, &started, retry.RetryWindowStartedAt, "source retries must retain the original durable checkpoint")
				require.Equal(t, durationPtr(codeReviewWorkspaceWaitLimit), retry.MaxRetryDuration, "source retries must share the original total budget")
				require.False(t, retry.BypassMaxRetryDuration, "source retries may not escape the preparation budget")
			} else {
				var fatal *FatalError
				require.ErrorAs(t, actual, &fatal, "deterministic, moved-head, cancelled and exhausted preparation must not repeat")
			}
			require.Equal(t, tt.deadline, errors.Is(actual, context.DeadlineExceeded), "deadline authority must survive generic preparer errors")
			if errors.Is(tt.cause, agent.ErrCodeReviewSourceIntegrity) {
				require.Equal(t, !tt.expired && !tt.ctxDeadline && !tt.cancelled, errors.Is(actual, agent.ErrCodeReviewSourceIntegrity), "source validation classification must preserve its terminal cause unless cancellation or the durable budget owns the outcome")
			}
			if tt.cancelled {
				require.ErrorIs(t, actual, context.Canceled, "caller cancellation must prevent another cold attempt")
			}
		})
	}
}

func TestCodeReviewWorkspaceWrappedTimeoutSafeMessage(t *testing.T) {
	t.Parallel()
	clone := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageClone, errors.New("private raw stderr"))
	integrity := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, agent.ErrCodeReviewSourceIntegrity)
	tests := []struct {
		name, raw string
		known     bool
		expected  string
	}{
		{name: "exact worker wrapper", raw: fmt.Sprintf("retryable job timed out after %s: %s", codeReviewWorkspaceWaitLimit, clone), known: true, expected: "Review workspace preparation exceeded its total eight-minute budget during repository clone. Review did not run. This is an operational review failure. Check repository access and sandbox network connectivity, then request a new review."},
		{name: "source integrity under exact worker timeout wrapper", raw: fmt.Sprintf("retryable job timed out after %s: %s", codeReviewWorkspaceWaitLimit, integrity), known: true, expected: "Review workspace preparation exceeded its total eight-minute budget during pinned source hydration. Review did not run. This is an operational review failure. Restore access to the pinned base and head Git objects, then request a new review."},
		{name: "raw suffix rejected", raw: fmt.Sprintf("retryable job timed out after %s: token=secret", codeReviewWorkspaceWaitLimit)},
		{name: "nested prefix rejected", raw: fmt.Sprintf("retryable job timed out after %s: raw: %s", codeReviewWorkspaceWaitLimit, clone)},
		{name: "different duration rejected", raw: "retryable job timed out after 30m0s: " + clone.Error()},
		{name: "appended diagnostics rejected", raw: fmt.Sprintf("retryable job timed out after %s: %s token=secret", codeReviewWorkspaceWaitLimit, clone)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, known := knownCodeReviewWorkspacePreparationError(tt.raw)
			require.Equal(t, tt.known, known, "only the exact worker timeout wrapper and finite safe suffix are trusted")
			if known {
				require.ErrorIs(t, actual, context.DeadlineExceeded, "worker timeout must render as exhausted total budget")
				require.NotErrorIs(t, actual, agent.ErrCodeReviewSourceIntegrity, "the worker's expired preparation window must own the terminal outcome")
				require.Equal(t, tt.expected, codeReviewWorkspacePreparationFailureMessage(actual), "timeout wrapper must preserve the source stage and author recovery guidance")
			}
		})
	}
}

func TestCodeReviewMovedHeadRetryCannotWidenPreparationBudget(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-codeReviewWorkspaceWaitLimit + time.Second)
	longDelay := time.Hour
	longAt := time.Now().Add(time.Hour)
	input := &RetryableError{Err: errors.New("rate limit"), RetryAfter: &longDelay, GitHubRetryAt: &longAt, GitHubRetryPolicy: GitHubRetryPolicyExact, GitHubRetryKey: "external", ResetRetryWindow: true, ConsumeAttempt: true}
	actual := codeReviewMovedHeadRetry(input, started, 2)
	var retry *RetryableError
	require.ErrorAs(t, actual, &retry, "moved-head external waits should remain retryable until the original gate deadline")
	require.Equal(t, &started, retry.RetryWindowStartedAt, "moved-head synchronization must inherit the first durable checkpoint")
	require.LessOrEqual(t, *retry.RetryAfter, time.Second, "a long external hint may not delay stale reconciliation beyond the remaining budget")
	require.GreaterOrEqual(t, *retry.RetryAfter, time.Duration(0), "next reconciliation should never use a negative delay")
	require.Equal(t, GitHubRetryPolicyNone, retry.GitHubRetryPolicy, "worker-owned GitHub scheduling must not override the clamped phase wait")
	require.Nil(t, retry.GitHubRetryAt, "external absolute reset time must not widen the preparation deadline")
	require.False(t, retry.ResetRetryWindow, "moved-head reconciliation cannot start a new timer")
	require.False(t, retry.ConsumeAttempt, "bounded synchronization must reach its own terminal gate instead of generic attempt exhaustion")
	var provenance *codeReviewWorkspaceGateFailure
	require.ErrorAs(t, retry, &provenance, "dead-letter reconciliation must retain the original gate generation")
	require.Equal(t, int64(2), provenance.Generation, "an exhausted runtime error may never adopt a newer workspace generation")
}

func TestCodeReviewMovedHeadRetryDefaultsToShortWait(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-time.Minute)
	actual := codeReviewMovedHeadRetry(&RetryableError{Err: errors.New("transport unavailable"), GitHubRetryPolicy: GitHubRetryPolicyBackoff}, started, 0)
	var retry *RetryableError
	require.ErrorAs(t, actual, &retry, "an existing GitHub retry should keep its bounded moved-head wait")
	require.Equal(t, durationPtr(codeReviewWorkspaceWait), retry.RetryAfter, "a missing external hint must default to a short sync retry rather than the entire remaining budget")
}
