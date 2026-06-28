package preview

import (
	"context"
	"time"

	"github.com/assembledhq/143/internal/models"
)

// PreviewInspector abstracts the headless browser used for screenshot
// capture, DOM inspection, and interaction replay. One headless browser
// instance is shared across all active previews on the same worker node.
// Each preview gets its own browser context (isolated cookies/storage).
//
// The headless browser runs outside the sandbox on the worker node and
// connects to the preview through the same transport as the preview gateway.
type PreviewInspector interface {
	// CaptureScreenshot takes a viewport screenshot of the preview at the given URL path.
	CaptureScreenshot(ctx context.Context, previewID string, opts models.ScreenshotOpts) (*models.ScreenshotResult, error)

	// CaptureDOM returns a serialized snapshot of the DOM.
	CaptureDOM(ctx context.Context, previewID string, opts DOMCaptureOpts) (*DOMSnapshot, error)

	// ReadConsole returns buffered console messages since last read.
	ReadConsole(ctx context.Context, previewID string) ([]ConsoleMessage, error)

	// InspectElement returns metadata about the DOM element at (x, y).
	InspectElement(ctx context.Context, previewID string, x, y int) (*models.ElementInfo, error)

	// InspectElementBySelector returns metadata about the first DOM element
	// matching selector.
	InspectElementBySelector(ctx context.Context, previewID string, selector string) (*models.ElementInfo, error)

	// StartScreencast begins recording frames at the given FPS.
	StartScreencast(ctx context.Context, previewID string, fps int) (screencastID string, err error)

	// StopScreencast ends recording and returns the assembled result.
	StopScreencast(ctx context.Context, screencastID string) (*models.ScreencastResult, error)

	// ExecuteInteraction runs a sequence of browser interactions.
	ExecuteInteraction(ctx context.Context, previewID string, steps []models.InteractionStep) (*models.InteractionResult, error)

	// CaptureMultiViewport takes screenshots at multiple viewport sizes.
	CaptureMultiViewport(ctx context.Context, previewID string, opts models.MultiViewportOpts) (*models.MultiViewportResult, error)

	// ComputeVisualDiff compares two snapshots.
	ComputeVisualDiff(ctx context.Context, previewID string, beforeSnapshotID, afterSnapshotID string) (*models.VisualDiff, error)

	// RunAssertions runs visual assertions against the current preview state.
	RunAssertions(ctx context.Context, previewID string, assertions []Assertion) (*AssertionResult, error)

	// Close shuts down the headless browser and frees resources.
	Close() error
}

// =============================================================================
// Supporting types
// =============================================================================

// DOMCaptureOpts configures DOM snapshot capture.
type DOMCaptureOpts struct {
	Path          string // URL path, default "/"
	Selector      string // optional CSS selector to scope the capture
	IncludeStyles bool   // include computed styles
}

// DOMSnapshot is a serialized DOM tree with optional computed styles.
type DOMSnapshot struct {
	HTML          string            `json:"html"`
	ComponentTree []ComponentNode   `json:"component_tree,omitempty"`
	Styles        map[string]string `json:"styles,omitempty"`
}

// ComponentNode represents a component in the framework tree.
type ComponentNode struct {
	Name     string          `json:"name"`
	File     string          `json:"file,omitempty"`
	Line     int             `json:"line,omitempty"`
	Props    map[string]any  `json:"props,omitempty"`
	Children []ComponentNode `json:"children,omitempty"`
}

// ConsoleMessage represents a browser console entry.
type ConsoleMessage struct {
	Level     string    `json:"level"` // "log", "warn", "error", "info"
	Text      string    `json:"text"`
	Source    string    `json:"source,omitempty"`
	Line      int       `json:"line,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Assertion is a single visual assertion to run against the preview.
type Assertion struct {
	Type        string        `json:"type"` // element_exists, element_text, element_style, element_count, no_console_errors, page_title, viewport_screenshot_match
	Selector    string        `json:"selector,omitempty"`
	Property    string        `json:"property,omitempty"`
	Value       string        `json:"value,omitempty"`
	Contains    string        `json:"contains,omitempty"`
	Visible     *bool         `json:"visible,omitempty"`
	Min         *int          `json:"min,omitempty"`
	Max         *int          `json:"max,omitempty"`
	Region      *AssertRegion `json:"region,omitempty"`
	Description string        `json:"description,omitempty"`
}

// AssertRegion defines a viewport region for screenshot assertions.
type AssertRegion struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// AssertionResult contains the results of running assertions.
type AssertionResult struct {
	Passed  int              `json:"passed"`
	Failed  int              `json:"failed"`
	Total   int              `json:"total"`
	Results []AssertionCheck `json:"results"`
}

// AssertionCheck is the result of a single assertion.
type AssertionCheck struct {
	Assertion Assertion `json:"assertion"`
	Passed    bool      `json:"passed"`
	Message   string    `json:"message"`
	Actual    string    `json:"actual,omitempty"`
}
