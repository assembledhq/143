package preview

import (
	"errors"
	"fmt"
	"time"
)

// BrowserOperation identifies a browser RPC whose timeout budget must remain
// consistent across the public API, worker client, and worker handler.
type BrowserOperation string

const (
	browserDefaultOperationTimeout    = 30 * time.Second
	browserObserveOperationTimeout    = 45 * time.Second
	browserAssertionsOperationTimeout = 60 * time.Second

	// browserInteractionStepsTimeout is the design doc's cap on the interaction
	// step loop itself. An interaction is not just that loop: Act runs the steps
	// and then a full observation of the resulting page, so the operation budget
	// must cover both. Sizing them equally would leave the trailing observation
	// no time whenever the steps ran long, discarding step results that already
	// succeeded. Both halves are enforced -- the step loop by
	// maxInteractionTimeout, the observation by ChromeDPInspector.Observe -- so
	// the sum is a real ceiling rather than an estimate. Browser-session
	// lifecycle work is budgeted separately, by BrowserOperationSessionAct.
	browserInteractionStepsTimeout     = 60 * time.Second
	browserInteractionOperationTimeout = browserInteractionStepsTimeout + browserObserveOperationTimeout

	// Session lifecycle calls include work outside a raw browser operation. The
	// access phase covers the session row plus the worst successful recovery
	// path: bootstrap, failed state restore, and a second bootstrap. Persistence
	// covers both browser export and the state-store write. Control acquisition
	// and release split a final default-operation allowance.
	browserSessionAccessTimeout         = 4 * browserDefaultOperationTimeout
	browserSessionInitializationTimeout = browserObserveOperationTimeout
	browserSessionPersistTimeout        = 2 * browserDefaultOperationTimeout
	browserSessionControlAcquireTimeout = browserDefaultOperationTimeout / 2
	browserSessionControlReleaseTimeout = browserDefaultOperationTimeout / 2
	browserSessionCoordinationTimeout   = browserSessionControlAcquireTimeout + browserSessionControlReleaseTimeout

	browserSessionObserveOperationTimeout = browserSessionAccessTimeout +
		browserObserveOperationTimeout + browserSessionPersistTimeout + browserSessionCoordinationTimeout
	browserSessionInteractionOperationTimeout = browserSessionAccessTimeout + browserSessionInitializationTimeout +
		browserInteractionOperationTimeout + browserSessionPersistTimeout + browserSessionCoordinationTimeout

	browserMultiViewportOperationTimeout = 150 * time.Second
)

const (
	BrowserOperationScreenshot     BrowserOperation = "screenshot"
	BrowserOperationObserve        BrowserOperation = "observe"
	BrowserOperationSessionObserve BrowserOperation = "session_observe"
	BrowserOperationInteract       BrowserOperation = "interact"
	BrowserOperationSessionAct     BrowserOperation = "session_act"
	BrowserOperationInspect        BrowserOperation = "inspect"
	BrowserOperationMultiViewport  BrowserOperation = "multi_viewport"
	BrowserOperationVisualDiff     BrowserOperation = "visual_diff"
	BrowserOperationAssertions     BrowserOperation = "assertions"
)

// BrowserOperationBudget leaves bounded headroom at each network hop so the
// inner operation can return a structured error before an outer connection is
// closed by its response deadline.
type BrowserOperationBudget struct {
	Operation      time.Duration
	WorkerResponse time.Duration
	WorkerRequest  time.Duration
	APIResponse    time.Duration
}

// BrowserOperationBudgetFor returns the canonical timeout stack for an
// operation. Each operation's budget covers everything that runs inside it:
// multi-viewport captures may perform five sequential screenshots, and an
// interaction is a step loop followed by an observation.
func BrowserOperationBudgetFor(operation BrowserOperation) BrowserOperationBudget {
	operationTimeout := browserDefaultOperationTimeout
	switch operation {
	case BrowserOperationObserve:
		operationTimeout = browserObserveOperationTimeout
	case BrowserOperationSessionObserve:
		operationTimeout = browserSessionObserveOperationTimeout
	case BrowserOperationInteract:
		operationTimeout = browserInteractionOperationTimeout
	case BrowserOperationSessionAct:
		operationTimeout = browserSessionInteractionOperationTimeout
	case BrowserOperationAssertions:
		operationTimeout = browserAssertionsOperationTimeout
	case BrowserOperationMultiViewport:
		operationTimeout = browserMultiViewportOperationTimeout
	}
	return BrowserOperationBudget{
		Operation:      operationTimeout,
		WorkerResponse: operationTimeout + 5*time.Second,
		WorkerRequest:  operationTimeout + 10*time.Second,
		APIResponse:    operationTimeout + 15*time.Second,
	}
}

// CoordinatedBrowserOperationAPIResponseTimeoutFor adds the session identity
// and control fencing that compatibility browser tools perform in the public
// API before and after their raw inspector/worker operation. The worker itself
// still executes the raw operation, so only the outer API response deadline
// needs this additional allowance.
func CoordinatedBrowserOperationAPIResponseTimeoutFor(operation BrowserOperation) time.Duration {
	return BrowserOperationBudgetFor(operation).APIResponse + browserDefaultOperationTimeout + browserSessionCoordinationTimeout
}

// BrowserAccessStage identifies a safe, non-secret preview authentication
// stage that can be returned to callers and attached to logs.
type BrowserAccessStage string

const (
	BrowserAccessStageTokenMint           BrowserAccessStage = "token_mint"
	BrowserAccessStageBootstrapNavigation BrowserAccessStage = "bootstrap_navigation"
	BrowserAccessStageBootstrapExchange   BrowserAccessStage = "bootstrap_exchange"
	BrowserAccessStageAuthenticatedOpen   BrowserAccessStage = "authenticated_open"
	BrowserAccessStageStateRestore        BrowserAccessStage = "state_restore"
)

// BrowserAccessError preserves the failed authentication stage while keeping
// tokens, cookies, and underlying implementation details out of API details.
type BrowserAccessError struct {
	Stage BrowserAccessStage
	// PrecedingStage records the stage that failed first and triggered a
	// recovery attempt, when that recovery then failed at Stage. Only one stage
	// survives errors.As on a joined error, so without this the failure that
	// started the cascade never reaches logs or error details.
	PrecedingStage BrowserAccessStage
	Err            error
}

func (e *BrowserAccessError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("preview browser access failed at %s", e.Stage)
	}
	return fmt.Sprintf("preview browser access failed at %s: %v", e.Stage, e.Err)
}

func (e *BrowserAccessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsBrowserAccessError unwraps a staged browser access error.
func AsBrowserAccessError(err error) (*BrowserAccessError, bool) {
	var target *BrowserAccessError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// Code returns the stable public error code for the failed stage.
func (e *BrowserAccessError) Code() string {
	switch e.Stage {
	case BrowserAccessStageTokenMint:
		return "PREVIEW_BROWSER_TOKEN_MINT_FAILED"
	case BrowserAccessStageBootstrapNavigation:
		return "PREVIEW_BROWSER_BOOTSTRAP_NAVIGATION_FAILED"
	case BrowserAccessStageBootstrapExchange:
		return "PREVIEW_BROWSER_BOOTSTRAP_EXCHANGE_FAILED"
	case BrowserAccessStageAuthenticatedOpen:
		return "PREVIEW_BROWSER_AUTHENTICATED_OPEN_FAILED"
	case BrowserAccessStageStateRestore:
		return "PREVIEW_BROWSER_STATE_RESTORE_FAILED"
	default:
		return "PREVIEW_BROWSER_ACCESS_FAILED"
	}
}
