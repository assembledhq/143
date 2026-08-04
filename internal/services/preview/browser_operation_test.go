package preview

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBrowserOperationBudgetFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation BrowserOperation
		want      BrowserOperationBudget
	}{
		{name: "screenshot", operation: BrowserOperationScreenshot, want: BrowserOperationBudget{Operation: 30 * time.Second, WorkerResponse: 35 * time.Second, WorkerRequest: 40 * time.Second, APIResponse: 45 * time.Second}},
		{name: "observe", operation: BrowserOperationObserve, want: BrowserOperationBudget{Operation: 45 * time.Second, WorkerResponse: 50 * time.Second, WorkerRequest: 55 * time.Second, APIResponse: 60 * time.Second}},
		{name: "interaction", operation: BrowserOperationInteract, want: BrowserOperationBudget{Operation: 60 * time.Second, WorkerResponse: 65 * time.Second, WorkerRequest: 70 * time.Second, APIResponse: 75 * time.Second}},
		{name: "inspect", operation: BrowserOperationInspect, want: BrowserOperationBudget{Operation: 30 * time.Second, WorkerResponse: 35 * time.Second, WorkerRequest: 40 * time.Second, APIResponse: 45 * time.Second}},
		{name: "multi viewport", operation: BrowserOperationMultiViewport, want: BrowserOperationBudget{Operation: 150 * time.Second, WorkerResponse: 155 * time.Second, WorkerRequest: 160 * time.Second, APIResponse: 165 * time.Second}},
		{name: "visual diff", operation: BrowserOperationVisualDiff, want: BrowserOperationBudget{Operation: 30 * time.Second, WorkerResponse: 35 * time.Second, WorkerRequest: 40 * time.Second, APIResponse: 45 * time.Second}},
		{name: "assertions", operation: BrowserOperationAssertions, want: BrowserOperationBudget{Operation: 60 * time.Second, WorkerResponse: 65 * time.Second, WorkerRequest: 70 * time.Second, APIResponse: 75 * time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, BrowserOperationBudgetFor(tt.operation), "timeout stack should preserve bounded headroom at every hop")
		})
	}
}

func TestBrowserAccessErrorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stage BrowserAccessStage
		code  string
	}{
		{name: "token mint", stage: BrowserAccessStageTokenMint, code: "PREVIEW_BROWSER_TOKEN_MINT_FAILED"},
		{name: "bootstrap navigation", stage: BrowserAccessStageBootstrapNavigation, code: "PREVIEW_BROWSER_BOOTSTRAP_NAVIGATION_FAILED"},
		{name: "bootstrap exchange", stage: BrowserAccessStageBootstrapExchange, code: "PREVIEW_BROWSER_BOOTSTRAP_EXCHANGE_FAILED"},
		{name: "authenticated open", stage: BrowserAccessStageAuthenticatedOpen, code: "PREVIEW_BROWSER_AUTHENTICATED_OPEN_FAILED"},
		{name: "state restore", stage: BrowserAccessStageStateRestore, code: "PREVIEW_BROWSER_STATE_RESTORE_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("safe test cause")
			accessErr := &BrowserAccessError{Stage: tt.stage, Err: cause}
			actual, ok := AsBrowserAccessError(accessErr)
			require.True(t, ok, "staged errors should remain discoverable through wrapping")
			require.Equal(t, tt.code, actual.Code(), "stage should map to its stable public error code")
			require.ErrorIs(t, actual, cause, "staged error should preserve its internal cause")
		})
	}
}
