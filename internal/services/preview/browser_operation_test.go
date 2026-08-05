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
		{name: "session observe", operation: BrowserOperationSessionObserve, want: BrowserOperationBudget{Operation: 255 * time.Second, WorkerResponse: 260 * time.Second, WorkerRequest: 265 * time.Second, APIResponse: 270 * time.Second}},
		{name: "interaction", operation: BrowserOperationInteract, want: BrowserOperationBudget{Operation: 105 * time.Second, WorkerResponse: 110 * time.Second, WorkerRequest: 115 * time.Second, APIResponse: 120 * time.Second}},
		{name: "session action", operation: BrowserOperationSessionAct, want: BrowserOperationBudget{Operation: 360 * time.Second, WorkerResponse: 365 * time.Second, WorkerRequest: 370 * time.Second, APIResponse: 375 * time.Second}},
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

func TestBrowserInteractionBudgetCoversStepsAndTrailingObservation(t *testing.T) {
	t.Parallel()

	interact := BrowserOperationBudgetFor(BrowserOperationInteract).Operation
	observe := BrowserOperationBudgetFor(BrowserOperationObserve).Operation

	require.Equal(t, maxInteractionTimeout, browserInteractionStepsTimeout, "the inspector step loop should be capped by the documented step budget")
	require.GreaterOrEqual(t, interact, browserInteractionStepsTimeout+observe,
		"Act runs the step loop and then a full observation, so a step loop that uses its whole budget must still leave room to observe the resulting page")
	require.Equal(t, browserObserveOperationTimeout, observe,
		"ChromeDPInspector.Observe bounds itself by browserObserveOperationTimeout, so the observe half of the interaction budget must be that same value")
}

func TestBrowserSessionActionBudgetCoversLifecycleWork(t *testing.T) {
	t.Parallel()

	sessionAct := BrowserOperationBudgetFor(BrowserOperationSessionAct).Operation
	required := browserSessionAccessTimeout + browserSessionInitializationTimeout + browserInteractionStepsTimeout + browserObserveOperationTimeout + browserSessionPersistTimeout + browserSessionCoordinationTimeout
	require.Equal(t, required, sessionAct,
		"session actions should budget setup, initialization, steps, trailing observation, persistence, and control coordination")
}

func TestBrowserSessionObserveBudgetCoversLifecycleWork(t *testing.T) {
	t.Parallel()

	sessionObserve := BrowserOperationBudgetFor(BrowserOperationSessionObserve).Operation
	required := browserSessionAccessTimeout + browserObserveOperationTimeout + browserSessionPersistTimeout + browserSessionCoordinationTimeout
	require.Equal(t, required, sessionObserve,
		"session observations should budget access, page observation, persistence, and control coordination")
}

func TestCoordinatedBrowserOperationAPIResponseTimeoutCoversIdentityAndControlFencing(t *testing.T) {
	t.Parallel()

	base := BrowserOperationBudgetFor(BrowserOperationScreenshot)
	overhead := browserDefaultOperationTimeout + browserSessionCoordinationTimeout

	require.Equal(t, base.APIResponse+overhead, CoordinatedBrowserOperationAPIResponseTimeoutFor(BrowserOperationScreenshot),
		"public API response should retain raw operation headroom after identity setup and both control phases")
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
