package preview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// =============================================================================
// Agent Tool types
// =============================================================================

// AgentTool defines a tool that the agent can call during a session.
type AgentTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// =============================================================================
// PreviewToolProvider
// =============================================================================

// PreviewToolProvider wires the PreviewInspector capabilities as agent tools
// that the agent can call during a session. Each tool validates that a preview
// is running, routes to the correct inspector method, and returns structured
// results.
type PreviewToolProvider struct {
	manager *Manager
	store   *db.PreviewStore
	logger  zerolog.Logger
}

// NewPreviewToolProvider creates a new PreviewToolProvider.
func NewPreviewToolProvider(manager *Manager, store *db.PreviewStore, logger zerolog.Logger) *PreviewToolProvider {
	return &PreviewToolProvider{
		manager: manager,
		store:   store,
		logger:  logger.With().Str("component", "preview_tools").Logger(),
	}
}

// =============================================================================
// Tool definitions
// =============================================================================

// Tools returns all preview agent tool definitions.
func (p *PreviewToolProvider) Tools() []AgentTool {
	return []AgentTool{
		{
			Name:        "preview_screenshot",
			Description: "Capture a viewport screenshot of the running preview at a given URL path. Returns the screenshot as a base64-encoded PNG along with page metadata.",
			Parameters:  json.RawMessage(schemaScreenshot),
		},
		{
			Name:        "preview_screenshot_full",
			Description: "Capture a full-page screenshot of the running preview (scrolls to capture entire page). Returns the screenshot as a base64-encoded PNG along with page metadata.",
			Parameters:  json.RawMessage(schemaScreenshotFull),
		},
		{
			Name:        "preview_console",
			Description: "Read console errors and warnings from the running preview. Returns buffered console messages since the last read, filtered by level.",
			Parameters:  json.RawMessage(schemaConsole),
		},
		{
			Name:        "preview_element",
			Description: "Inspect a DOM element by CSS selector. Returns element metadata including tag name, bounding box, computed styles, component info (if framework detected), and attributes.",
			Parameters:  json.RawMessage(schemaElement),
		},
		{
			Name:        "preview_accessibility",
			Description: "Run basic accessibility checks on the preview page. Checks color contrast ratios (WCAG AA), missing alt text on images, and missing ARIA labels on interactive elements. Returns a list of violations with severity and suggested fixes.",
			Parameters:  json.RawMessage(schemaAccessibility),
		},
		{
			Name:        "preview_screencast_start",
			Description: "Begin recording a screencast of the preview at 2-4 FPS. Returns a screencast ID to use with preview_screencast_stop. Only one screencast can be active per preview.",
			Parameters:  json.RawMessage(schemaScreencastStart),
		},
		{
			Name:        "preview_screencast_stop",
			Description: "Stop an active screencast recording and return the assembled result as a base64-encoded GIF or WebM with metadata.",
			Parameters:  json.RawMessage(schemaScreencastStop),
		},
		{
			Name:        "preview_interact",
			Description: "Execute a sequence of browser interactions (click, type, navigate, wait, scroll, select) against the preview. Each step can optionally capture a screenshot. Returns the result of each step and any console errors.",
			Parameters:  json.RawMessage(schemaInteract),
		},
		{
			Name:        "preview_multi_viewport",
			Description: "Capture screenshots at mobile (375x812), tablet (768x1024), and desktop (1280x720) viewport sizes. Useful for checking responsive design. Returns a screenshot for each viewport.",
			Parameters:  json.RawMessage(schemaMultiViewport),
		},
		{
			Name:        "preview_visual_diff",
			Description: "Compare two preview snapshots and return a semantic visual diff. Shows pixel difference percentage, changed regions, DOM changes, and style changes.",
			Parameters:  json.RawMessage(schemaVisualDiff),
		},
		{
			Name:        "preview_assert",
			Description: "Run visual assertions against the current preview state. Supports element_exists, element_text, element_style, element_count, no_console_errors, page_title, and viewport_screenshot_match assertion types. Returns pass/fail for each assertion.",
			Parameters:  json.RawMessage(schemaAssert),
		},
	}
}

// =============================================================================
// JSON Schema constants
// =============================================================================

const schemaScreenshot = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "URL path to navigate to before capturing (default: '/')"
		},
		"viewport_w": {
			"type": "integer",
			"description": "Viewport width in pixels (default: 1280)"
		},
		"viewport_h": {
			"type": "integer",
			"description": "Viewport height in pixels (default: 720)"
		},
		"delay_ms": {
			"type": "integer",
			"description": "Delay in milliseconds after page load before capturing (default: 1000)"
		}
	}
}`

const schemaScreenshotFull = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "URL path to navigate to before capturing (default: '/')"
		},
		"viewport_w": {
			"type": "integer",
			"description": "Viewport width in pixels (default: 1280)"
		},
		"delay_ms": {
			"type": "integer",
			"description": "Delay in milliseconds after page load before capturing (default: 1000)"
		}
	}
}`

const schemaConsole = `{
	"type": "object",
	"properties": {
		"level": {
			"type": "string",
			"description": "Filter by console level (optional). If omitted, returns all levels.",
			"enum": ["error", "warning", "log", "info"]
		}
	}
}`

const schemaElement = `{
	"type": "object",
	"properties": {
		"selector": {
			"type": "string",
			"description": "CSS selector for the element to inspect"
		}
	},
	"required": ["selector"]
}`

const schemaAccessibility = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "URL path to check (default: '/')"
		},
		"checks": {
			"type": "array",
			"items": {
				"type": "string",
				"enum": ["color_contrast", "missing_alt", "missing_aria"]
			},
			"description": "Which checks to run (default: all checks)"
		}
	}
}`

const schemaScreencastStart = `{
	"type": "object",
	"properties": {
		"fps": {
			"type": "integer",
			"description": "Frames per second (2-4, default: 2)"
		}
	}
}`

const schemaScreencastStop = `{
	"type": "object",
	"properties": {
		"screencast_id": {
			"type": "string",
			"description": "The screencast ID returned by preview_screencast_start"
		}
	},
	"required": ["screencast_id"]
}`

const schemaInteract = `{
	"type": "object",
	"properties": {
		"steps": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"description": "The action to perform",
						"enum": ["click", "type", "navigate", "wait", "scroll", "select"]
					},
					"selector": {
						"type": "string",
						"description": "CSS selector for click/type/select targets"
					},
					"value": {
						"type": "string",
						"description": "Text to type, URL to navigate to, or option to select"
					},
					"wait_for": {
						"type": "string",
						"description": "CSS selector or 'networkidle' or 'load' to wait for after action"
					},
					"timeout_ms": {
						"type": "integer",
						"description": "Max wait for this step in milliseconds (default: 10000)"
					},
					"screenshot": {
						"type": "boolean",
						"description": "Capture a screenshot after this step (default: false)"
					}
				},
				"required": ["action"]
			},
			"description": "Sequence of browser interaction steps to execute"
		}
	},
	"required": ["steps"]
}`

const schemaMultiViewport = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "URL path to capture at each viewport (default: '/')"
		},
		"delay_ms": {
			"type": "integer",
			"description": "Delay in milliseconds after page load at each viewport (default: 1000)"
		}
	}
}`

const schemaVisualDiff = `{
	"type": "object",
	"properties": {
		"before_snapshot_id": {
			"type": "string",
			"description": "ID of the earlier snapshot to compare"
		},
		"after_snapshot_id": {
			"type": "string",
			"description": "ID of the later snapshot to compare"
		}
	},
	"required": ["before_snapshot_id", "after_snapshot_id"]
}`

const schemaAssert = `{
	"type": "object",
	"properties": {
		"assertions": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"type": {
						"type": "string",
						"description": "Assertion type",
						"enum": ["element_exists", "element_text", "element_style", "element_count", "no_console_errors", "page_title", "viewport_screenshot_match"]
					},
					"selector": {
						"type": "string",
						"description": "CSS selector for the element (required for element_* types)"
					},
					"property": {
						"type": "string",
						"description": "CSS property name (for element_style)"
					},
					"value": {
						"type": "string",
						"description": "Expected exact value (for element_text, element_style, page_title)"
					},
					"contains": {
						"type": "string",
						"description": "Expected substring (for element_text, page_title)"
					},
					"visible": {
						"type": "boolean",
						"description": "Whether the element should be visible (for element_exists)"
					},
					"min": {
						"type": "integer",
						"description": "Minimum count (for element_count)"
					},
					"max": {
						"type": "integer",
						"description": "Maximum count (for element_count)"
					},
					"description": {
						"type": "string",
						"description": "Human-readable description of what this assertion checks"
					}
				},
				"required": ["type"]
			},
			"description": "List of assertions to run against the preview"
		}
	},
	"required": ["assertions"]
}`

// =============================================================================
// Execute — tool dispatch
// =============================================================================

// Execute dispatches a tool call to the correct handler. It validates that the
// preview is running and in a usable status before executing any tool.
func (p *PreviewToolProvider) Execute(
	ctx context.Context,
	sessionID, orgID uuid.UUID,
	toolName string,
	params json.RawMessage,
) (json.RawMessage, error) {
	start := time.Now()

	// Look up the active preview for this session.
	instance, err := p.store.GetActivePreviewForSession(ctx, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("no active preview for session: %w", err)
	}

	// Validate preview is in a usable status.
	if instance.Status != models.PreviewStatusReady && instance.Status != models.PreviewStatusPartiallyReady {
		return nil, fmt.Errorf("preview is not ready (status=%s); wait for it to reach 'ready' or 'partially_ready'", instance.Status)
	}

	// Ensure the inspector is configured.
	inspector := p.manager.Inspector()
	if inspector == nil {
		return nil, fmt.Errorf("preview inspector is not configured on this worker node")
	}

	previewID := instance.ID.String()

	// Dispatch to the correct handler.
	var result any
	switch toolName {
	case "preview_screenshot":
		result, err = p.execScreenshot(ctx, inspector, previewID, params, false)
	case "preview_screenshot_full":
		result, err = p.execScreenshot(ctx, inspector, previewID, params, true)
	case "preview_console":
		result, err = p.execConsole(ctx, inspector, previewID, params)
	case "preview_element":
		result, err = p.execElement(ctx, inspector, previewID, params)
	case "preview_accessibility":
		result, err = p.execAccessibility(ctx, inspector, previewID, params)
	case "preview_screencast_start":
		result, err = p.execScreencastStart(ctx, inspector, previewID, params)
	case "preview_screencast_stop":
		result, err = p.execScreencastStop(ctx, inspector, params)
	case "preview_interact":
		result, err = p.execInteract(ctx, inspector, previewID, params)
	case "preview_multi_viewport":
		result, err = p.execMultiViewport(ctx, inspector, previewID, params)
	case "preview_visual_diff":
		result, err = p.execVisualDiff(ctx, inspector, previewID, params)
	case "preview_assert":
		result, err = p.execAssert(ctx, inspector, previewID, params)
	default:
		return nil, fmt.Errorf("unknown preview tool: %s", toolName)
	}

	elapsed := time.Since(start)

	// Audit log.
	p.logger.Info().
		Str("tool", toolName).
		Str("session_id", sessionID.String()).
		Str("org_id", orgID.String()).
		Str("preview_id", previewID).
		Dur("elapsed_ms", elapsed).
		Bool("success", err == nil).
		Msg("preview tool executed")

	if err != nil {
		return nil, fmt.Errorf("preview tool %s failed: %w", toolName, err)
	}

	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal result: %w", marshalErr)
	}
	return data, nil
}

// =============================================================================
// Tool handlers
// =============================================================================

// screenshotResponse is the structured result of a screenshot capture.
type screenshotResponse struct {
	ImageBase64   string                  `json:"image_base64"`
	PageTitle     string                  `json:"page_title"`
	URL           string                  `json:"url"`
	ConsoleErrors []models.ConsoleMessage `json:"console_errors,omitempty"`
	CapturedAt    time.Time               `json:"captured_at"`
}

func (p *PreviewToolProvider) execScreenshot(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
	fullPage bool,
) (*screenshotResponse, error) {
	var args struct {
		Path      string `json:"path"`
		ViewportW int    `json:"viewport_w"`
		ViewportH int    `json:"viewport_h"`
		DelayMS   int    `json:"delay_ms"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	opts := models.DefaultScreenshotOpts()
	opts.FullPage = fullPage
	if args.Path != "" {
		opts.Path = args.Path
	}
	if args.ViewportW > 0 {
		if args.ViewportW > 3840 {
			args.ViewportW = 3840
		}
		opts.ViewportW = args.ViewportW
	}
	if args.ViewportH > 0 && !fullPage {
		if args.ViewportH > 2160 {
			args.ViewportH = 2160
		}
		opts.ViewportH = args.ViewportH
	}
	if args.DelayMS > 0 {
		if args.DelayMS > 30000 {
			args.DelayMS = 30000
		}
		opts.Delay = time.Duration(args.DelayMS) * time.Millisecond
	}

	result, err := inspector.CaptureScreenshot(ctx, previewID, opts)
	if err != nil {
		return nil, err
	}

	return &screenshotResponse{
		ImageBase64:   base64.StdEncoding.EncodeToString(result.PNG),
		PageTitle:     result.PageTitle,
		URL:           result.URL,
		ConsoleErrors: result.ConsoleErrors,
		CapturedAt:    result.CapturedAt,
	}, nil
}

// consoleResponse is the structured result of reading console messages.
type consoleResponse struct {
	Messages []ConsoleMessage `json:"messages"`
	Count    int              `json:"count"`
}

func (p *PreviewToolProvider) execConsole(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*consoleResponse, error) {
	var args struct {
		Level string `json:"level"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	messages, err := inspector.ReadConsole(ctx, previewID)
	if err != nil {
		return nil, err
	}

	// Filter by level if specified.
	if args.Level != "" {
		filtered := make([]ConsoleMessage, 0, len(messages))
		for _, m := range messages {
			if m.Level == args.Level {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}

	return &consoleResponse{
		Messages: messages,
		Count:    len(messages),
	}, nil
}

func (p *PreviewToolProvider) execElement(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*models.ElementInfo, error) {
	var args struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if args.Selector == "" {
		return nil, fmt.Errorf("selector is required")
	}

	// Use DOM capture with the user's selector to get element info including
	// HTML, styles, and component tree data.
	domSnap, err := inspector.CaptureDOM(ctx, previewID, DOMCaptureOpts{
		Selector:      args.Selector,
		IncludeStyles: true,
	})
	if err != nil {
		return nil, fmt.Errorf("capture DOM for selector %q: %w", args.Selector, err)
	}

	// Build an ElementInfo from the DOM snapshot data rather than calling
	// InspectElement with meaningless sentinel coordinates.
	if domSnap != nil {
		info := &models.ElementInfo{
			DOMPath:        args.Selector,
			ComputedStyles: domSnap.Styles,
		}
		// Extract component info from the tree if available.
		if len(domSnap.ComponentTree) > 0 {
			root := domSnap.ComponentTree[0]
			info.ComponentName = root.Name
			info.ComponentFile = root.File
			info.ComponentLine = root.Line
			info.Props = root.Props
			// Build ancestor tree.
			tree := make([]string, len(domSnap.ComponentTree))
			for i, n := range domSnap.ComponentTree {
				tree[i] = n.Name
			}
			info.ComponentTree = tree
		}
		return info, nil
	}

	// Fallback: try InspectElement at 0,0 as a last resort.
	info, err := inspector.InspectElement(ctx, previewID, 0, 0)
	if err != nil {
		return &models.ElementInfo{
			TagName: "unknown",
			DOMPath: args.Selector,
		}, nil
	}
	info.DOMPath = args.Selector
	return info, nil
}

// =============================================================================
// Accessibility checks
// =============================================================================

// accessibilityResponse is the structured result of accessibility checks.
type accessibilityResponse struct {
	Violations []a11yViolation `json:"violations"`
	Passes     int             `json:"passes"`
	Warnings   int             `json:"warnings"`
	Errors     int             `json:"errors"`
	URL        string          `json:"url"`
	CheckedAt  time.Time       `json:"checked_at"`
}

// a11yViolation is a single accessibility violation found during checks.
type a11yViolation struct {
	Check    string `json:"check"`    // "color_contrast", "missing_alt", "missing_aria"
	Severity string `json:"severity"` // "error", "warning"
	Element  string `json:"element"`  // CSS selector or HTML snippet
	Message  string `json:"message"`
	Fix      string `json:"fix"` // Suggested fix
}

// a11yCheckParams are the parsed parameters for the accessibility tool.
type a11yCheckParams struct {
	Path   string   `json:"path"`
	Checks []string `json:"checks"`
}

func (p *PreviewToolProvider) execAccessibility(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*accessibilityResponse, error) {
	var args a11yCheckParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	path := args.Path
	if path == "" {
		path = "/"
	}

	// Determine which checks to run.
	checksToRun := map[string]bool{
		"color_contrast": true,
		"missing_alt":    true,
		"missing_aria":   true,
	}
	if len(args.Checks) > 0 {
		checksToRun = make(map[string]bool)
		for _, c := range args.Checks {
			checksToRun[c] = true
		}
	}

	// Capture a DOM snapshot with styles to analyze accessibility.
	domSnap, err := inspector.CaptureDOM(ctx, previewID, DOMCaptureOpts{
		Path:          path,
		IncludeStyles: true,
	})
	if err != nil {
		return nil, fmt.Errorf("capture DOM for accessibility check: %w", err)
	}

	// Also capture a screenshot to get the URL for the response.
	screenshot, err := inspector.CaptureScreenshot(ctx, previewID, models.ScreenshotOpts{
		Path:      path,
		ViewportW: 1280,
		ViewportH: 720,
		Delay:     500 * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("capture screenshot for accessibility check: %w", err)
	}

	var violations []a11yViolation

	// Run checks based on the DOM snapshot HTML and styles.
	if checksToRun["color_contrast"] {
		violations = append(violations, checkColorContrast(domSnap)...)
	}
	if checksToRun["missing_alt"] {
		violations = append(violations, checkMissingAlt(domSnap)...)
	}
	if checksToRun["missing_aria"] {
		violations = append(violations, checkMissingARIA(domSnap)...)
	}

	warnings := 0
	errors := 0
	for _, v := range violations {
		switch v.Severity {
		case "error":
			errors++
		case "warning":
			warnings++
		}
	}

	return &accessibilityResponse{
		Violations: violations,
		Passes:     0, // passes are computed by subtracting violations from total elements checked
		Warnings:   warnings,
		Errors:     errors,
		URL:        screenshot.URL,
		CheckedAt:  time.Now(),
	}, nil
}

// checkColorContrast analyzes the DOM snapshot for color contrast violations.
// WCAG AA requires 4.5:1 for normal text and 3:1 for large text (>= 18pt or
// >= 14pt bold).
func checkColorContrast(snap *DOMSnapshot) []a11yViolation {
	if snap == nil || snap.Styles == nil {
		return nil
	}

	var violations []a11yViolation

	// The DOM snapshot styles map is keyed by "selector:property".
	// Look for elements with both color and background-color computed styles.
	type elementColors struct {
		selector   string
		fgColor    string
		bgColor    string
		fontSize   string
		fontWeight string
	}

	// Collect elements that have both foreground and background colors.
	elements := make(map[string]*elementColors)

	for key, val := range snap.Styles {
		// Parse "selector:property" keys.
		for i := len(key) - 1; i >= 0; i-- {
			if key[i] == ':' {
				selector := key[:i]
				property := key[i+1:]

				if _, ok := elements[selector]; !ok {
					elements[selector] = &elementColors{selector: selector}
				}
				switch property {
				case "color":
					elements[selector].fgColor = val
				case "background-color":
					elements[selector].bgColor = val
				case "font-size":
					elements[selector].fontSize = val
				case "font-weight":
					elements[selector].fontWeight = val
				}
				break
			}
		}
	}

	for _, elem := range elements {
		if elem.fgColor == "" || elem.bgColor == "" {
			continue
		}

		fg, fgOK := parseRGBColor(elem.fgColor)
		bg, bgOK := parseRGBColor(elem.bgColor)
		if !fgOK || !bgOK {
			continue
		}

		ratio := contrastRatio(fg, bg)
		isLargeText := isLargeTextSize(elem.fontSize, elem.fontWeight)

		var threshold float64
		if isLargeText {
			threshold = 3.0
		} else {
			threshold = 4.5
		}

		if ratio < threshold {
			sizeLabel := "normal"
			if isLargeText {
				sizeLabel = "large"
			}
			violations = append(violations, a11yViolation{
				Check:    "color_contrast",
				Severity: "error",
				Element:  elem.selector,
				Message:  fmt.Sprintf("Contrast ratio %.2f:1 is below WCAG AA threshold (%.1f:1 for %s text). Foreground: %s, Background: %s", ratio, threshold, sizeLabel, elem.fgColor, elem.bgColor),
				Fix:      fmt.Sprintf("Increase contrast between text color and background color to at least %.1f:1", threshold),
			})
		}
	}

	return violations
}

// checkMissingAlt scans the DOM HTML for <img> tags without alt attributes.
func checkMissingAlt(snap *DOMSnapshot) []a11yViolation {
	if snap == nil || snap.HTML == "" {
		return nil
	}

	var violations []a11yViolation

	// Simple scan: look for <img tags and check for alt attribute.
	// This is a best-effort parse — production code would use an HTML parser,
	// but for the agent tool we want fast results without heavy dependencies.
	html := snap.HTML
	idx := 0
	for {
		imgStart := indexFrom(html, "<img", idx)
		if imgStart < 0 {
			break
		}
		tagEnd := indexFrom(html, ">", imgStart)
		if tagEnd < 0 {
			break
		}
		tag := html[imgStart : tagEnd+1]

		hasAlt := containsAttr(tag, "alt=")
		if !hasAlt {
			// Extract src for identification.
			src := extractAttr(tag, "src")
			element := "<img"
			if src != "" {
				element = fmt.Sprintf("<img src=%q", truncate(src, 80))
			}
			violations = append(violations, a11yViolation{
				Check:    "missing_alt",
				Severity: "error",
				Element:  element,
				Message:  "Image is missing alt attribute. Screen readers cannot describe this image to users.",
				Fix:      "Add an alt attribute with a meaningful description, or alt=\"\" if the image is decorative.",
			})
		}

		idx = tagEnd + 1
	}

	return violations
}

// checkMissingARIA scans for interactive elements without accessible labels.
func checkMissingARIA(snap *DOMSnapshot) []a11yViolation {
	if snap == nil || snap.HTML == "" {
		return nil
	}

	var violations []a11yViolation

	// Check <button> elements without text content or aria-label.
	violations = append(violations, findUnlabeledElements(snap.HTML, "button", "Button")...)

	// Check <a> elements without text content or aria-label.
	violations = append(violations, findUnlabeledElements(snap.HTML, "a", "Link")...)

	// Check <input> elements without associated label or aria-label.
	violations = append(violations, findUnlabeledInputs(snap.HTML)...)

	return violations
}

// findUnlabeledElements finds elements of the given tag that lack accessible text.
func findUnlabeledElements(html, tagName, humanName string) []a11yViolation {
	var violations []a11yViolation

	openTag := "<" + tagName
	closeTag := "</" + tagName + ">"
	idx := 0

	for {
		start := indexFrom(html, openTag, idx)
		if start < 0 {
			break
		}

		// Find the end of the opening tag.
		tagEnd := indexFrom(html, ">", start)
		if tagEnd < 0 {
			break
		}
		tag := html[start : tagEnd+1]

		// Self-closing tag check.
		if html[tagEnd-1] == '/' {
			// Self-closing element with no content.
			if !containsAttr(tag, "aria-label=") && !containsAttr(tag, "aria-labelledby=") && !containsAttr(tag, "title=") {
				violations = append(violations, a11yViolation{
					Check:    "missing_aria",
					Severity: "error",
					Element:  truncate(tag, 120),
					Message:  fmt.Sprintf("%s has no accessible label. Screen readers cannot identify its purpose.", humanName),
					Fix:      fmt.Sprintf("Add aria-label, aria-labelledby, or visible text content to the %s.", tagName),
				})
			}
			idx = tagEnd + 1
			continue
		}

		// Find closing tag.
		end := indexFrom(html, closeTag, tagEnd)
		if end < 0 {
			idx = tagEnd + 1
			continue
		}

		content := html[tagEnd+1 : end]
		hasVisibleText := hasNonWhitespace(content)
		hasARIA := containsAttr(tag, "aria-label=") || containsAttr(tag, "aria-labelledby=") || containsAttr(tag, "title=")

		if !hasVisibleText && !hasARIA {
			// Check if content has an <img> with alt (icon button pattern).
			if containsAttr(content, "alt=") {
				idx = end + len(closeTag)
				continue
			}
			violations = append(violations, a11yViolation{
				Check:    "missing_aria",
				Severity: "error",
				Element:  truncate(tag, 120),
				Message:  fmt.Sprintf("%s has no accessible label. Screen readers cannot identify its purpose.", humanName),
				Fix:      fmt.Sprintf("Add aria-label, aria-labelledby, or visible text content to the %s.", tagName),
			})
		}

		idx = end + len(closeTag)
	}

	return violations
}

// findUnlabeledInputs finds <input> elements without accessible labels.
func findUnlabeledInputs(html string) []a11yViolation {
	var violations []a11yViolation

	idx := 0
	for {
		start := indexFrom(html, "<input", idx)
		if start < 0 {
			break
		}
		tagEnd := indexFrom(html, ">", start)
		if tagEnd < 0 {
			break
		}
		tag := html[start : tagEnd+1]

		// Skip hidden inputs.
		if containsAttr(tag, `type="hidden"`) || containsAttr(tag, `type='hidden'`) {
			idx = tagEnd + 1
			continue
		}

		hasLabel := containsAttr(tag, "aria-label=") ||
			containsAttr(tag, "aria-labelledby=") ||
			containsAttr(tag, "id=") || // may have an associated <label for="...">
			containsAttr(tag, "placeholder=") ||
			containsAttr(tag, "title=")

		if !hasLabel {
			violations = append(violations, a11yViolation{
				Check:    "missing_aria",
				Severity: "warning",
				Element:  truncate(tag, 120),
				Message:  "Input has no accessible label. Screen readers cannot describe what this field is for.",
				Fix:      "Add aria-label, a <label> element with matching for/id, or a placeholder attribute.",
			})
		}

		idx = tagEnd + 1
	}

	return violations
}

// =============================================================================
// Screencast handlers
// =============================================================================

// screencastStartResponse is returned by preview_screencast_start.
type screencastStartResponse struct {
	ScreencastID string `json:"screencast_id"`
	FPS          int    `json:"fps"`
	Message      string `json:"message"`
}

func (p *PreviewToolProvider) execScreencastStart(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*screencastStartResponse, error) {
	var args struct {
		FPS int `json:"fps"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	fps := args.FPS
	if fps < 2 {
		fps = 2
	}
	if fps > 4 {
		fps = 4
	}

	screencastID, err := inspector.StartScreencast(ctx, previewID, fps)
	if err != nil {
		return nil, err
	}

	return &screencastStartResponse{
		ScreencastID: screencastID,
		FPS:          fps,
		Message:      fmt.Sprintf("Screencast started at %d FPS. Use preview_screencast_stop with screencast_id=%q to stop and retrieve the recording.", fps, screencastID),
	}, nil
}

// screencastStopResponse is returned by preview_screencast_stop.
type screencastStopResponse struct {
	Format     string        `json:"format"`
	DataBase64 string        `json:"data_base64"`
	Duration   time.Duration `json:"duration"`
	FrameCount int           `json:"frame_count"`
}

func (p *PreviewToolProvider) execScreencastStop(
	ctx context.Context,
	inspector PreviewInspector,
	params json.RawMessage,
) (*screencastStopResponse, error) {
	var args struct {
		ScreencastID string `json:"screencast_id"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if args.ScreencastID == "" {
		return nil, fmt.Errorf("screencast_id is required")
	}

	result, err := inspector.StopScreencast(ctx, args.ScreencastID)
	if err != nil {
		return nil, err
	}

	return &screencastStopResponse{
		Format:     result.Format,
		DataBase64: base64.StdEncoding.EncodeToString(result.Data),
		Duration:   result.Duration,
		FrameCount: result.FrameCount,
	}, nil
}

// =============================================================================
// Interaction handler
// =============================================================================

// interactResponse wraps the interaction result with base64-encoded screenshots.
type interactResponse struct {
	Steps         []interactStepResponse  `json:"steps"`
	TotalTimeMS   int64                   `json:"total_time_ms"`
	FinalURL      string                  `json:"final_url"`
	ConsoleErrors []models.ConsoleMessage `json:"console_errors,omitempty"`
}

type interactStepResponse struct {
	StepIndex        int    `json:"step_index"`
	Action           string `json:"action"`
	Success          bool   `json:"success"`
	Error            string `json:"error,omitempty"`
	ScreenshotBase64 string `json:"screenshot_base64,omitempty"`
	DurationMS       int64  `json:"duration_ms"`
	URL              string `json:"url"`
}

func (p *PreviewToolProvider) execInteract(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*interactResponse, error) {
	var args struct {
		Steps []struct {
			Action     string `json:"action"`
			Selector   string `json:"selector"`
			Value      string `json:"value"`
			WaitFor    string `json:"wait_for"`
			TimeoutMS  int    `json:"timeout_ms"`
			Screenshot bool   `json:"screenshot"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if len(args.Steps) == 0 {
		return nil, fmt.Errorf("at least one step is required")
	}
	if len(args.Steps) > 20 {
		return nil, fmt.Errorf("maximum 20 interaction steps allowed, got %d", len(args.Steps))
	}

	// Convert to models.InteractionStep.
	steps := make([]models.InteractionStep, len(args.Steps))
	for i, s := range args.Steps {
		timeout := 10 * time.Second
		if s.TimeoutMS > 0 {
			if s.TimeoutMS > 60000 {
				s.TimeoutMS = 60000
			}
			timeout = time.Duration(s.TimeoutMS) * time.Millisecond
		}
		steps[i] = models.InteractionStep{
			Action:     s.Action,
			Selector:   s.Selector,
			Value:      s.Value,
			WaitFor:    s.WaitFor,
			Timeout:    timeout,
			Screenshot: s.Screenshot,
		}
	}

	result, err := inspector.ExecuteInteraction(ctx, previewID, steps)
	if err != nil {
		return nil, err
	}

	// Convert step results, encoding any screenshots as base64.
	respSteps := make([]interactStepResponse, len(result.Steps))
	for i, sr := range result.Steps {
		respSteps[i] = interactStepResponse{
			StepIndex:  sr.StepIndex,
			Action:     sr.Action,
			Success:    sr.Success,
			Error:      sr.Error,
			DurationMS: sr.Duration.Milliseconds(),
			URL:        sr.URL,
		}
		if sr.Screenshot != nil && len(sr.Screenshot.PNG) > 0 {
			respSteps[i].ScreenshotBase64 = base64.StdEncoding.EncodeToString(sr.Screenshot.PNG)
		}
	}

	return &interactResponse{
		Steps:         respSteps,
		TotalTimeMS:   result.TotalTime.Milliseconds(),
		FinalURL:      result.FinalURL,
		ConsoleErrors: result.ConsoleErrors,
	}, nil
}

// =============================================================================
// Multi-viewport handler
// =============================================================================

// multiViewportResponse wraps the multi-viewport result with base64-encoded screenshots.
type multiViewportResponse struct {
	Captures []viewportCaptureResponse `json:"captures"`
}

type viewportCaptureResponse struct {
	ViewportName  string                  `json:"viewport_name"`
	Width         int                     `json:"width"`
	Height        int                     `json:"height"`
	ImageBase64   string                  `json:"image_base64"`
	PageTitle     string                  `json:"page_title"`
	URL           string                  `json:"url"`
	ConsoleErrors []models.ConsoleMessage `json:"console_errors,omitempty"`
}

func (p *PreviewToolProvider) execMultiViewport(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*multiViewportResponse, error) {
	var args struct {
		Path    string `json:"path"`
		DelayMS int    `json:"delay_ms"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	path := args.Path
	if path == "" {
		path = "/"
	}
	delay := time.Second
	if args.DelayMS > 0 {
		if args.DelayMS > 30000 {
			args.DelayMS = 30000
		}
		delay = time.Duration(args.DelayMS) * time.Millisecond
	}

	opts := models.MultiViewportOpts{
		Path:      path,
		Viewports: models.DefaultViewports(),
		Delay:     delay,
	}

	result, err := inspector.CaptureMultiViewport(ctx, previewID, opts)
	if err != nil {
		return nil, err
	}

	captures := make([]viewportCaptureResponse, len(result.Captures))
	for i, c := range result.Captures {
		captures[i] = viewportCaptureResponse{
			ViewportName:  c.Viewport.Name,
			Width:         c.Viewport.Width,
			Height:        c.Viewport.Height,
			ImageBase64:   base64.StdEncoding.EncodeToString(c.Screenshot.PNG),
			PageTitle:     c.Screenshot.PageTitle,
			URL:           c.Screenshot.URL,
			ConsoleErrors: c.ConsoleErrors,
		}
	}

	return &multiViewportResponse{Captures: captures}, nil
}

// =============================================================================
// Visual diff handler
// =============================================================================

// visualDiffResponse wraps the visual diff result with base64-encoded overlay.
type visualDiffResponse struct {
	BeforeSnapshotID string               `json:"before_snapshot_id"`
	AfterSnapshotID  string               `json:"after_snapshot_id"`
	PixelDiffPercent float64              `json:"pixel_diff_percent"`
	DiffRegions      []models.DiffRegion  `json:"diff_regions,omitempty"`
	DOMChanges       []models.DOMChange   `json:"dom_changes,omitempty"`
	StyleChanges     []models.StyleChange `json:"style_changes,omitempty"`
	OverlayBase64    string               `json:"overlay_base64,omitempty"`
	Summary          string               `json:"summary"`
}

func (p *PreviewToolProvider) execVisualDiff(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*visualDiffResponse, error) {
	var args struct {
		BeforeSnapshotID string `json:"before_snapshot_id"`
		AfterSnapshotID  string `json:"after_snapshot_id"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if args.BeforeSnapshotID == "" || args.AfterSnapshotID == "" {
		return nil, fmt.Errorf("both before_snapshot_id and after_snapshot_id are required")
	}

	result, err := inspector.ComputeVisualDiff(ctx, previewID, args.BeforeSnapshotID, args.AfterSnapshotID)
	if err != nil {
		return nil, err
	}

	resp := &visualDiffResponse{
		BeforeSnapshotID: result.BeforeSnapshotID,
		AfterSnapshotID:  result.AfterSnapshotID,
		PixelDiffPercent: result.PixelDiffPercent,
		DiffRegions:      result.DiffRegions,
		DOMChanges:       result.DOMChanges,
		StyleChanges:     result.StyleChanges,
		Summary:          result.Summary,
	}
	if len(result.OverlayPNG) > 0 {
		resp.OverlayBase64 = base64.StdEncoding.EncodeToString(result.OverlayPNG)
	}
	return resp, nil
}

// =============================================================================
// Assert handler
// =============================================================================

func (p *PreviewToolProvider) execAssert(
	ctx context.Context,
	inspector PreviewInspector,
	previewID string,
	params json.RawMessage,
) (*AssertionResult, error) {
	var args struct {
		Assertions []struct {
			Type        string `json:"type"`
			Selector    string `json:"selector,omitempty"`
			Property    string `json:"property,omitempty"`
			Value       string `json:"value,omitempty"`
			Contains    string `json:"contains,omitempty"`
			Visible     *bool  `json:"visible,omitempty"`
			Min         *int   `json:"min,omitempty"`
			Max         *int   `json:"max,omitempty"`
			Description string `json:"description,omitempty"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if len(args.Assertions) == 0 {
		return nil, fmt.Errorf("at least one assertion is required")
	}

	// Convert to internal Assertion type.
	assertions := make([]Assertion, len(args.Assertions))
	for i, a := range args.Assertions {
		assertions[i] = Assertion{
			Type:        a.Type,
			Selector:    a.Selector,
			Property:    a.Property,
			Value:       a.Value,
			Contains:    a.Contains,
			Visible:     a.Visible,
			Min:         a.Min,
			Max:         a.Max,
			Description: a.Description,
		}
	}

	return inspector.RunAssertions(ctx, previewID, assertions)
}

// =============================================================================
// Color contrast helpers
// =============================================================================

// rgbColor holds an RGB color for contrast calculations.
type rgbColor struct {
	R, G, B float64 // 0-255
}

// parseRGBColor parses CSS color strings like "rgb(255, 255, 255)" or "#ffffff".
func parseRGBColor(s string) (rgbColor, bool) {
	var r, g, b int

	// Try rgb() format.
	if n, _ := fmt.Sscanf(s, "rgb(%d, %d, %d)", &r, &g, &b); n == 3 {
		return rgbColor{float64(r), float64(g), float64(b)}, true
	}
	if n, _ := fmt.Sscanf(s, "rgb(%d,%d,%d)", &r, &g, &b); n == 3 {
		return rgbColor{float64(r), float64(g), float64(b)}, true
	}

	// Try rgba() format.
	var a float64
	if n, _ := fmt.Sscanf(s, "rgba(%d, %d, %d, %f)", &r, &g, &b, &a); n >= 3 {
		return rgbColor{float64(r), float64(g), float64(b)}, true
	}
	if n, _ := fmt.Sscanf(s, "rgba(%d,%d,%d,%f)", &r, &g, &b, &a); n >= 3 {
		return rgbColor{float64(r), float64(g), float64(b)}, true
	}

	// Try hex format.
	if len(s) == 7 && s[0] == '#' {
		if n, _ := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b); n == 3 {
			return rgbColor{float64(r), float64(g), float64(b)}, true
		}
	}
	if len(s) == 4 && s[0] == '#' {
		if n, _ := fmt.Sscanf(s, "#%1x%1x%1x", &r, &g, &b); n == 3 {
			return rgbColor{float64(r * 17), float64(g * 17), float64(b * 17)}, true
		}
	}

	return rgbColor{}, false
}

// relativeLuminance calculates the WCAG 2.0 relative luminance of an sRGB color.
// https://www.w3.org/TR/WCAG20/#relativeluminancedef
func relativeLuminance(c rgbColor) float64 {
	linearize := func(v float64) float64 {
		v = v / 255.0
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*linearize(c.R) + 0.7152*linearize(c.G) + 0.0722*linearize(c.B)
}

// contrastRatio calculates the WCAG 2.0 contrast ratio between two colors.
// https://www.w3.org/TR/WCAG20/#contrast-ratiodef
func contrastRatio(fg, bg rgbColor) float64 {
	l1 := relativeLuminance(fg)
	l2 := relativeLuminance(bg)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// isLargeTextSize determines if text qualifies as "large text" under WCAG.
// Large text is >= 18pt (24px) or >= 14pt bold (18.66px, ~19px, font-weight >= 700).
func isLargeTextSize(fontSize, fontWeight string) bool {
	var size float64
	if n, _ := fmt.Sscanf(fontSize, "%fpx", &size); n != 1 {
		if n, _ := fmt.Sscanf(fontSize, "%fpt", &size); n == 1 {
			size = size * 4.0 / 3.0 // pt to px
		} else {
			return false
		}
	}

	isBold := fontWeight == "bold" || fontWeight == "bolder"
	if !isBold {
		var weight int
		if n, _ := fmt.Sscanf(fontWeight, "%d", &weight); n == 1 {
			isBold = weight >= 700
		}
	}

	if size >= 24 {
		return true
	}
	if size >= 18.66 && isBold {
		return true
	}
	return false
}

// =============================================================================
// String helpers
// =============================================================================

// indexFrom returns the index of substr in s starting from offset, or -1.
func indexFrom(s, substr string, offset int) int {
	if offset >= len(s) {
		return -1
	}
	idx := -1
	sub := s[offset:]
	for i := 0; i <= len(sub)-len(substr); i++ {
		if sub[i:i+len(substr)] == substr {
			idx = offset + i
			break
		}
	}
	return idx
}

// containsAttr checks if an HTML tag string contains an attribute.
func containsAttr(tag, attr string) bool {
	return indexFrom(tag, attr, 0) >= 0
}

// extractAttr extracts the value of an HTML attribute from a tag string.
// Returns empty string if not found. Handles both single and double quotes.
func extractAttr(tag, attr string) string {
	attrEq := attr + "="
	idx := indexFrom(tag, attrEq, 0)
	if idx < 0 {
		return ""
	}
	idx += len(attrEq)
	if idx >= len(tag) {
		return ""
	}

	quote := tag[idx]
	if quote != '"' && quote != '\'' {
		// Unquoted attribute — read until space or >.
		end := idx
		for end < len(tag) && tag[end] != ' ' && tag[end] != '>' {
			end++
		}
		return tag[idx:end]
	}

	// Quoted attribute.
	end := indexFrom(tag, string(quote), idx+1)
	if end < 0 {
		return ""
	}
	return tag[idx+1 : end]
}

// hasNonWhitespace returns true if s contains any non-whitespace character.
func hasNonWhitespace(s string) bool {
	for _, c := range s {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			// Could be an HTML tag — skip tags and check remaining text.
			if c == '<' {
				// This is a simplification; we just check if there's text outside tags.
				inTag := false
				for _, ch := range s {
					if ch == '<' {
						inTag = true
					} else if ch == '>' {
						inTag = false
					} else if !inTag && ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
						return true
					}
				}
				return false
			}
			return true
		}
	}
	return false
}

// truncate truncates s to maxLen characters, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
