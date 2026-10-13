package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Real browser tests use the TestBrowserChromeDP prefix so CI can run them
// separately from the CPU-heavy backend suite. Ordinary package tests remain
// usable without Chromium; the dedicated CI step requires a browser.
func requirePreviewTestBrowser(t *testing.T) {
	t.Helper()
	for _, candidate := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return
		}
	}
	if os.Getenv("TEST_BROWSER_REQUIRED") == "1" {
		t.Fatal("headless preview tests require Google Chrome or Chromium")
	}
	t.Skip("install Google Chrome or Chromium to exercise browser-backed preview tests")
}

// Browser processes are isolated, but compete for the same runner's CPU and
// memory. Bound only real browser fixtures; ordinary package tests stay parallel.
var lifecycleBrowserSlot = make(chan struct{}, 1)

func acquireLifecycleBrowser(t *testing.T) {
	t.Helper()
	requirePreviewTestBrowser(t)
	select {
	case lifecycleBrowserSlot <- struct{}{}:
		t.Cleanup(func() { <-lifecycleBrowserSlot })
	case <-time.After(90 * time.Second):
		t.Fatal("timed out waiting for the browser fixture slot")
	}
}

func newLifecycleInspector(t *testing.T) (*ChromeDPInspector, string) {
	t.Helper()
	acquireLifecycleBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, err := io.WriteString(w, `<!doctype html><html><head><title>Preview fixture</title><style>body{margin:0;color:rgb(0,0,0);background:white}#label{color:rgb(255,0,0)}#hidden{display:none}</style></head><body><h1 id="label">Preview fixture</h1><label>Name<input id="name" value="initial"></label><label><input id="check" type="checkbox">Accept</label><select id="choice"><option value="a">A</option><option value="b">B</option></select><button id="save" onclick="document.querySelector('#result').textContent=document.querySelector('#name').value">Save</button><p id="result">empty</p><span id="hidden">Hidden</span><ul><li>first</li><li>second</li></ul><script>window.fixtureInitialVisibility=document.visibilityState;console.log('fixture loaded');localStorage.setItem('fixture','value');document.cookie='fixture-cookie=kept;path=/';</script></body></html>`)
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	inspector := NewChromeDPInspector(ChromeDPInspectorConfig{PreviewURLTemplate: server.URL + "{{.Path}}"}, zerolog.Nop())
	t.Cleanup(func() { require.NoError(t, inspector.Close(), "close only the browser owned by this test") })
	return inspector, server.URL
}

func TestBrowserChromeDPFailedContextInitializationIsNotCached(t *testing.T) {
	t.Parallel()
	inspector, _ := newLifecycleInspector(t)
	first, err := inspector.getOrCreatePreviewCtx("first")
	require.NoError(t, err, "initialize browser on its persistent context")
	require.NotNil(t, first, "successful initialization must return an owned preview context")
	inspector.browserCancel()
	pc, err := inspector.getOrCreatePreviewCtx("failed")
	require.ErrorContains(t, err, "initialize preview browser context", "failed target initialization must retain its browser stage")
	require.Nil(t, pc, "failed initialization must not expose a partially initialized target")
	inspector.mu.Lock()
	_, cached := inspector.previews["failed"]
	inspector.mu.Unlock()
	require.False(t, cached, "a failed context must not poison later operations with a cached unusable target")
}

func TestBrowserChromeDPPreviewLifecycle(t *testing.T) {
	t.Parallel()
	inspector, base := newLifecycleInspector(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	screenshot, err := inspector.CaptureScreenshot(ctx, "preview", models.ScreenshotOpts{ViewportW: 640, ViewportH: 480})
	require.NoError(t, err, "navigate actual fixture and capture screenshot")
	require.Equal(t, "Preview fixture", screenshot.PageTitle, "capture must retain actual browser title")
	require.Equal(t, base+"/", screenshot.URL, "capture must stay on exact authorized preview origin")
	image, err := png.Decode(bytes.NewReader(screenshot.PNG))
	require.NoError(t, err, "capture must be decodable PNG")
	require.Equal(t, []int{640, 480}, []int{image.Bounds().Dx(), image.Bounds().Dy()}, "PNG dimensions must match requested viewport")
	dom, err := inspector.CaptureDOM(ctx, "preview", DOMCaptureOpts{Selector: "#label", IncludeStyles: true})
	require.NoError(t, err, "capture scoped DOM and computed styles")
	require.Equal(t, `<h1 id="label">Preview fixture</h1>`, dom.HTML, "DOM selector must not leak unrelated form content")
	require.Equal(t, "rgb(255, 0, 0)", dom.Styles["color"], "computed styles must come from rendered browser element")
	steps := []models.InteractionStep{{Action: "fill", Selector: "#name", Value: "tested"}, {Action: "select", Selector: "#choice", Value: "b"}, {Action: "check", Selector: "#check"}, {Action: "uncheck", Selector: "#check"}, {Action: "hover", Selector: "#save"}, {Action: "press", Selector: "#name", Value: "Tab"}, {Action: "click", Role: "button", Name: "Save", Screenshot: true}, {Action: "wait", Text: "tested"}, {Action: "viewport", Value: "800x600"}, {Action: "scroll", Value: "10"}, {Action: "wait", URL: "/"}, {Action: "wait", WaitFor: "load"}}
	interaction, err := inspector.ExecuteInteraction(ctx, "preview", steps)
	require.NoError(t, err, "supported interactions must run against actual DOM")
	require.Len(t, interaction.Steps, len(steps), "every supported step must be returned")
	for i, step := range interaction.Steps {
		require.Equal(t, models.StepResult{StepIndex: i, Action: steps[i].Action, Success: true, URL: base + "/"}, models.StepResult{StepIndex: step.StepIndex, Action: step.Action, Success: step.Success, Error: step.Error, ErrorCode: step.ErrorCode, URL: step.URL, MatchCount: step.MatchCount}, "step must succeed on expected target without hidden errors")
	}
	require.NotNil(t, interaction.Steps[6].Screenshot, "requested click evidence must include real browser screenshot")
	visible := true
	count := 2
	assertions := []Assertion{{Type: "element_exists", Selector: "#result", Visible: &visible}, {Type: "element_text", Selector: "#result", Value: "tested"}, {Type: "element_style", Selector: "#label", Property: "color", Value: "rgb(255, 0, 0)"}, {Type: "element_count", Selector: "li", Min: &count, Max: &count}, {Type: "page_title", Value: "Preview fixture"}, {Type: "no_console_errors"}}
	actual, err := inspector.RunAssertions(ctx, "preview", assertions)
	require.NoError(t, err, "browser assertions should inspect resulting state")
	expected := &AssertionResult{Passed: 6, Total: 6, Results: []AssertionCheck{{Assertion: assertions[0], Passed: true, Message: `element "#result" exists`, Actual: "found"}, {Assertion: assertions[1], Passed: true, Message: "text matches", Actual: "tested"}, {Assertion: assertions[2], Passed: true, Message: "color matches", Actual: "rgb(255, 0, 0)"}, {Assertion: assertions[3], Passed: true, Message: "element count 2 within bounds", Actual: "2"}, {Assertion: assertions[4], Passed: true, Message: "page title matches", Actual: "Preview fixture"}, {Assertion: assertions[5], Passed: true, Message: "no console errors"}}}
	require.Equal(t, expected, actual, "all browser assertions must return exact expected observations")
	element, err := inspector.InspectElementBySelector(ctx, "preview", "#label")
	require.NoError(t, err, "selector inspection must resolve actual element")
	require.Equal(t, "h1", strings.ToLower(element.TagName), "inspected element must match selector")
	target := models.BrowserTarget{PreviewID: "preview", ContextKey: "preview"}
	observed, err := inspector.Observe(ctx, target, models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{CurrentPage: true}, IncludeDOM: true})
	require.NoError(t, err, "observation must combine screenshot, semantic tree and redacted DOM")
	require.Equal(t, models.PreviewBrowserContextStatus{ContextKey: "preview", Reused: true, Restoration: models.PreviewBrowserRestorationPreserved}, observed.Context, "observation must reuse exact existing browser context")
	require.Contains(t, observed.SemanticState, "tested", "semantic state must represent current result")
	require.NotContains(t, observed.DOMExcerpt, `value="initial"`, "observation must strip input values from DOM evidence")
	messages, err := inspector.ReadConsole(ctx, "preview")
	require.NoError(t, err, "console messages must be readable")
	require.NotEmpty(t, messages, "real navigation should deliver fixture console messages")
	viewports := []models.ViewportSpec{{Name: "small", Width: 320, Height: 240}, {Name: "large", Width: 800, Height: 600}}
	multi, err := inspector.CaptureMultiViewport(ctx, "preview", models.MultiViewportOpts{Viewports: viewports})
	require.NoError(t, err, "multi-viewport capture must render each requested size")
	require.Len(t, multi.Captures, 2, "both captures must be returned")
	for i, capture := range multi.Captures {
		require.Equal(t, viewports[i], capture.Viewport, "capture metadata must retain exact named viewport")
	}
}

func TestBrowserChromeDPStorageRestoration(t *testing.T) {
	t.Parallel()
	inspector, base := newLifecycleInspector(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target := models.BrowserTarget{PreviewID: "before", SessionID: "session", ContextKey: "session:session"}
	_, err := inspector.CaptureScreenshot(ctx, target.PreviewID, models.ScreenshotOpts{})
	require.NoError(t, err, "initialize browser storage fixture")
	raw, err := inspector.ExportStorage(ctx, target)
	require.NoError(t, err, "export storage from bound session context")
	var exported browserStorageState
	require.NoError(t, json.Unmarshal(raw, &exported), "exported storage must be valid JSON")
	require.Equal(t, map[string]string{"fixture": "value"}, exported.LocalStorage, "export must retain exact app storage")
	require.Equal(t, base+"/", exported.URL, "export must retain compatible origin")
	inspector.dropBrowserContext(target)
	target.PreviewID = "after"
	require.NoError(t, inspector.RestoreStorage(ctx, target, raw), "replacement preview must restore compatible session storage")
	restored, err := inspector.ExportStorage(ctx, target)
	require.NoError(t, err, "export restored storage")
	var actual browserStorageState
	require.NoError(t, json.Unmarshal(restored, &actual), "restored storage must be serializable")
	require.Equal(t, exported, actual, "restoration must retain complete origin, local storage and cookies")
}

//nolint:paralleltest // Subtests operate on the same browser tab and must preserve sequential page state.
func TestBrowserChromeDPInteractionAndAssertionFailures(t *testing.T) {
	t.Parallel()
	inspector, base := newLifecycleInspector(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := inspector.CaptureScreenshot(ctx, "preview", models.ScreenshotOpts{})
	require.NoError(t, err, "initialize authorized fixture")
	// Each case resets the DOM; cases serialize because they intentionally share
	// one browser context while the parent owns an isolated browser process.
	tests := []struct {
		name string
		step models.InteractionStep
		code string
	}{{"external navigation rejected", models.InteractionStep{Action: "navigate", Value: "https://example.invalid/"}, "NAVIGATION_NOT_ALLOWED"}, {"invalid selector rejected", models.InteractionStep{Action: "click", Selector: "[", Timeout: 100 * time.Millisecond}, "TARGET_NOT_FOUND"}, {"invalid scroll rejected", models.InteractionStep{Action: "scroll", Value: "bad"}, "BROWSER_ERROR"}, {"invalid viewport rejected", models.InteractionStep{Action: "viewport", Value: "bad"}, "INVALID_STEP"}, {"unknown action rejected", models.InteractionStep{Action: "unknown"}, "INVALID_STEP"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := inspector.ExecuteInteraction(ctx, "preview", []models.InteractionStep{tt.step, {Action: "navigate", Value: "/must-not-run"}})
			require.NoError(t, err, "failed steps must return structured evidence")
			require.Len(t, result.Steps, 1, "execution must stop after first invalid step")
			require.False(t, result.Steps[0].Success, "invalid step must never be reported successful")
			require.NotEmpty(t, result.Steps[0].Error, "failed step must retain actionable detail")
			require.Equal(t, tt.code, result.Steps[0].ErrorCode, "failed browser step must retain exact recovery classification")
			require.Equal(t, base+"/", result.FinalURL, "rejected navigation must retain authorized origin")
		})
	}
	visible := true
	min, max := 3, 1
	assertions := []Assertion{{Type: "element_exists", Selector: "#missing"}, {Type: "element_exists", Selector: "#hidden", Visible: &visible}, {Type: "element_text", Selector: "#label", Value: "wrong"}, {Type: "element_text", Selector: "#label", Contains: "missing"}, {Type: "element_style", Selector: "#label", Property: "color", Value: "blue"}, {Type: "element_style", Selector: "#label", Property: "color", Contains: "blue"}, {Type: "element_count", Selector: "li", Min: &min}, {Type: "element_count", Selector: "li", Max: &max}, {Type: "page_title", Value: "wrong"}, {Type: "page_title", Contains: "missing"}, {Type: "unknown"}}
	actual, err := inspector.RunAssertions(ctx, "preview", assertions)
	require.NoError(t, err, "negative assertions must return structured failures")
	require.Equal(t, []int{0, 11, 11}, []int{actual.Passed, actual.Failed, actual.Total}, "every deliberately invalid assertion must fail")
	// A caller cancellation must mark every remaining assertion rather than
	// continuing to query a live browser after its deadline.
	cancelled, stop := context.WithCancel(ctx)
	stop()
	actual, err = inspector.RunAssertions(cancelled, "preview", assertions[:2])
	require.NoError(t, err, "cancelled assertions should return diagnostic checks")
	require.Equal(t, 2, actual.Failed, "cancelled operation must mark every unexecuted check")
	pc, err := inspector.getOrCreatePreviewCtx("preview")
	require.NoError(t, err, "load exact context for controlled console error")
	merged, done := mergeContexts(pc.ctx, ctx)
	defer done()
	require.NoError(t, chromedp.Run(merged, chromedp.Evaluate(`console.error('fixture error')`, nil)), "inject only local fixture console error")
	require.Eventually(t, func() bool {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		for _, m := range pc.messages {
			if m.Level == "error" && m.Text == "fixture error" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond, "browser console event must reach inspector before assertion")
	actual, err = inspector.RunAssertions(ctx, "preview", []Assertion{{Type: "no_console_errors"}})
	require.NoError(t, err, "console assertion should inspect delivered event")
	require.Equal(t, "fixture error", actual.Results[0].Actual, "console failure must retain exact error text")
}

func TestBrowserChromeDPPreviewAccessBootstrap(t *testing.T) {
	t.Parallel()
	requirePreviewTestBrowser(t)
	tests := []struct {
		name      string
		status    int
		wantStage BrowserAccessStage
	}{{name: "successful bootstrap", status: 200}, {name: "expired access token", status: 401, wantStage: BrowserAccessStageBootstrapExchange}, {name: "gateway unavailable", status: 503, wantStage: BrowserAccessStageBootstrapExchange}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			acquireLifecycleBrowser(t)
			var mu sync.Mutex
			var exchanges []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/bootstrap/exchange" {
					var input map[string]string
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						http.Error(w, "bad exchange", 400)
						return
					}
					mu.Lock()
					exchanges = append(exchanges, input["token"])
					mu.Unlock()
					http.SetCookie(w, &http.Cookie{Name: "preview_session", Value: "gateway-access", Path: "/"})
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<!doctype html><title>Authenticated preview</title><body><button id="save">Save</button></body>`)
			}))
			defer server.Close()
			inspector := NewChromeDPInspector(ChromeDPInspectorConfig{PreviewURLTemplate: server.URL + "{{.Path}}"}, zerolog.Nop())
			defer func() { require.NoError(t, inspector.Close(), "close isolated access-test browser") }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			target := models.BrowserTarget{PreviewID: "preview", SessionID: "session", ContextKey: "session:session"}
			err := inspector.BootstrapPreviewAccess(ctx, target, "single-use-access")
			mu.Lock()
			actualExchanges := append([]string(nil), exchanges...)
			mu.Unlock()
			require.Equal(t, []string{"single-use-access"}, actualExchanges, "browser bootstrap must redeem exact token once")
			if tt.wantStage != "" {
				require.Error(t, err, "failed bootstrap must return staged error")
				failure, ok := AsBrowserAccessError(err)
				require.True(t, ok, "failed exchange must retain access stage")
				require.Equal(t, tt.wantStage, failure.Stage, "exchange failure must not be mislabeled as navigation")
				require.False(t, inspector.HasContext(target), "failed access exchange must discard unauthorized browser context")
				require.False(t, inspector.HasPreviewAccess(target), "failed exchange must never claim browser is authorized")
				return
			}
			require.NoError(t, err, "successful exchange must open authenticated preview")
			require.True(t, inspector.HasPreviewAccess(target), "authorization must be bound to exact preview identity")
			raw, err := inspector.ExportStorage(ctx, target)
			require.NoError(t, err, "authenticated application state must be exportable")
			require.NotContains(t, string(raw), "gateway-access", "gateway authorization cookies must never be persisted for later replay")
			result, err := inspector.Act(ctx, target, []models.InteractionStep{{Action: "click", Selector: "#save"}}, models.PreviewObservationOpts{IncludeDOM: true})
			require.NoError(t, err, "authorized act must include post-action observation")
			require.True(t, result.Interaction.Steps[0].Success, "authorized click must execute")
			require.Equal(t, "Authenticated preview", result.Observation.Title, "post-action observation must be actual authenticated page")
		})
	}
}

func TestBrowserChromeDPVisualDiffSnapshots(t *testing.T) {
	t.Parallel()
	inspector, _ := newLifecycleInspector(t)
	// Warm the persistent target so this fixture times snapshot work against
	// the production 30s budget instead of 20s. Production also counts cold
	// browser startup against that budget.
	_, err := inspector.getOrCreatePreviewCtx("preview")
	require.NoError(t, err, "initialize the persistent target before timing snapshot comparison")
	ctx, cancel := context.WithTimeout(context.Background(), BrowserOperationBudgetFor(BrowserOperationVisualDiff).Operation)
	defer cancel()
	actual, err := inspector.ComputeVisualDiff(ctx, "preview", "abcd", "dcba")
	require.NoError(t, err, "actual rendered snapshots must compare successfully")
	require.Equal(t, "abcd", actual.BeforeSnapshotID, "diff must retain exact before identity")
	require.Equal(t, "dcba", actual.AfterSnapshotID, "diff must retain exact after identity")
	require.Equal(t, float64(0), actual.PixelDiffPercent, "identical rendered fixtures must have no changed pixels")
	require.Equal(t, "No significant visual changes detected.", actual.Summary, "unchanged snapshots must not be described as a regression")
	_, err = png.Decode(bytes.NewReader(actual.OverlayPNG))
	require.NoError(t, err, "diff overlay must contain valid PNG evidence")
}

func TestBrowserChromeDPScreenshotReactivatesBackgroundTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fullPage bool
	}{
		{name: "viewport"},
		{name: "full page", fullPage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inspector, base := newLifecycleInspector(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			preview, err := inspector.getOrCreatePreviewCtx("preview")
			require.NoError(t, err, "create persistent preview target")
			background, err := inspector.getOrCreatePreviewCtx("other")
			require.NoError(t, err, "create competing browser tab")
			for _, size := range []models.ViewportSpec{{Width: 320, Height: 240}, {Width: 800, Height: 600}} {
				require.NoError(t, chromedp.Run(background.ctx, page.BringToFront()), "background the preview before each repeated capture")
				var visibility string
				require.NoError(t, chromedp.Run(preview.ctx, chromedp.Evaluate(`document.visibilityState`, &visibility)), "read preview visibility before activation")
				require.Equal(t, "hidden", visibility, "regression fixture must actually background the preview target")
				actual, err := inspector.CaptureScreenshot(ctx, "preview", models.ScreenshotOpts{ViewportW: size.Width, ViewportH: size.Height, FullPage: tt.fullPage})
				require.NoError(t, err, "repeated capture must reactivate the target after navigation and resize")
				require.Equal(t, base+"/", actual.URL, "capture must retain the authorized preview URL")
				require.Equal(t, "Preview fixture", actual.PageTitle, "capture must inspect the preview rather than the competing tab")
				decoded, err := png.Decode(bytes.NewReader(actual.PNG))
				require.NoError(t, err, "reactivated capture must return real PNG pixels")
				require.Equal(t, []int{size.Width, size.Height}, []int{decoded.Bounds().Dx(), decoded.Bounds().Dy()}, "resized capture must retain exact requested dimensions")
				require.NoError(t, chromedp.Run(preview.ctx, chromedp.Evaluate(`document.visibilityState`, &visibility)), "read target visibility after capture")
				require.Equal(t, "visible", visibility, "screenshot target must be foregrounded so compositor frames remain available")
				require.NoError(t, chromedp.Run(preview.ctx, chromedp.Evaluate(`window.fixtureInitialVisibility`, &visibility)), "read visibility recorded by the newly loaded page")
				require.Equal(t, "visible", visibility, "target must be activated before navigation and page scripts run")
			}
		})
	}
}
