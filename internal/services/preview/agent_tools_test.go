package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Unconfigured methods panic through the embedded interface, so an unexpected
// browser operation cannot accidentally satisfy a tool-routing expectation.
type toolInspectorFixture struct {
	PreviewInspector
	calls         []string
	args          []any
	err           error
	screenshotErr error
}

func (f *toolInspectorFixture) record(method string, args any) {
	f.calls = append(f.calls, method)
	f.args = append(f.args, args)
}
func (f *toolInspectorFixture) CaptureScreenshot(_ context.Context, id string, opts models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	f.record("screenshot", struct {
		ID   string
		Opts models.ScreenshotOpts
	}{id, opts})
	err := f.err
	if f.screenshotErr != nil {
		err = f.screenshotErr
	}
	return &models.ScreenshotResult{PNG: []byte("png"), PageTitle: "Example", URL: "https://example.test/page", CapturedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, err
}

func TestPreviewAccessibilityToolLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, params, failAt string
		expectedViolations   []a11yViolation
	}{
		{name: "default checks", expectedViolations: []a11yViolation{{Check: "missing_alt", Severity: "error", Element: `<img src="hero.png"`, Message: "Image is missing alt attribute. Screen readers cannot describe this image to users.", Fix: `Add an alt attribute with a meaningful description, or alt="" if the image is decorative.`}}},
		{name: "selected checks exclude alt", params: `{"path":"/settings","checks":["color_contrast","missing_aria"]}`},
		{name: "DOM failure stops before screenshot", failAt: "dom"},
		{name: "screenshot failure returns no partial report", failAt: "screenshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, id := uuid.New(), uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated accessibility tool fixture")
			defer mock.Close()
			mock.ExpectQuery(`SELECT .* FROM preview_instances WHERE org_id = @org_id AND session_id = @session_id`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session}).WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(id, session, org, uuid.New(), models.PreviewStatusReady, "container", time.Now())...))
			inspector := &toolInspectorFixture{}
			cause := errors.New("browser unavailable")
			if tt.failAt == "dom" {
				inspector.err = cause
			}
			if tt.failAt == "screenshot" {
				inspector.screenshotErr = cause
			}
			p := NewPreviewToolProvider(&Manager{inspector: inspector}, db.NewPreviewStore(mock), zerolog.Nop())
			before := time.Now()
			result, err := p.Execute(context.Background(), session, org, "preview_accessibility", json.RawMessage(tt.params))
			if tt.failAt != "" {
				require.ErrorIs(t, err, cause, "accessibility failures must preserve their retry cause")
				require.Nil(t, result, "failed captures must not produce a misleading accessibility report")
			} else {
				require.NoError(t, err, "ready previews must produce accessibility reports")
				var report accessibilityResponse
				require.NoError(t, json.Unmarshal(result, &report), "report must decode into its complete response contract")
				require.False(t, report.CheckedAt.Before(before), "report timestamp must describe this execution")
				require.False(t, report.CheckedAt.After(time.Now()), "report timestamp must not be in the future")
				report.CheckedAt = time.Time{}
				require.Equal(t, accessibilityResponse{Violations: tt.expectedViolations, Errors: len(tt.expectedViolations), URL: "https://example.test/page"}, report, "selected checks must produce exact findings and counts")
			}
			path := "/"
			if tt.params != "" {
				path = "/settings"
			}
			calls := []string{"dom"}
			args := []any{struct {
				ID   string
				Opts DOMCaptureOpts
			}{id.String(), DOMCaptureOpts{Path: path, IncludeStyles: true}}}
			if tt.failAt != "dom" {
				calls = append(calls, "screenshot")
				args = append(args, struct {
					ID   string
					Opts models.ScreenshotOpts
				}{id.String(), models.ScreenshotOpts{Path: path, ViewportW: 1280, ViewportH: 720, Delay: 500 * time.Millisecond}})
			}
			require.Equal(t, calls, inspector.calls, "both captures must use the same preview and stop at the first error")
			require.Equal(t, args, inspector.args, "accessibility inspection must preserve path and bounded capture options")
			require.NoError(t, mock.ExpectationsWereMet(), "accessibility lookup must bind authenticated tenant and session")
		})
	}
}
func (f *toolInspectorFixture) CaptureDOM(_ context.Context, id string, opts DOMCaptureOpts) (*DOMSnapshot, error) {
	f.record("dom", struct {
		ID   string
		Opts DOMCaptureOpts
	}{id, opts})
	return &DOMSnapshot{HTML: `<img src="hero.png">`, Styles: map[string]string{"color": "rgb(0,0,0)"}, ComponentTree: []ComponentNode{{Name: "Page", File: "page.tsx", Line: 7, Props: map[string]any{"title": "Example"}}, {Name: "App"}}}, f.err
}
func (f *toolInspectorFixture) ReadConsole(_ context.Context, id string) ([]ConsoleMessage, error) {
	f.record("console", id)
	return []ConsoleMessage{{Level: "error", Text: "broken"}, {Level: "info", Text: "ready"}}, f.err
}
func (f *toolInspectorFixture) StartScreencast(_ context.Context, id string, fps int) (string, error) {
	f.record("start", struct {
		ID  string
		FPS int
	}{id, fps})
	return "cast", f.err
}
func (f *toolInspectorFixture) StopScreencast(_ context.Context, id string) (*models.ScreencastResult, error) {
	f.record("stop", id)
	return &models.ScreencastResult{Format: "gif", Data: []byte("gif"), Duration: 2 * time.Second, FrameCount: 4}, f.err
}
func (f *toolInspectorFixture) ExecuteInteraction(_ context.Context, id string, steps []models.InteractionStep) (*models.InteractionResult, error) {
	f.record("interact", struct {
		ID    string
		Steps []models.InteractionStep
	}{id, steps})
	return &models.InteractionResult{Steps: []models.StepResult{{StepIndex: 0, Action: "click", Success: true, Duration: 20 * time.Millisecond, URL: "/done", Screenshot: &models.ScreenshotResult{PNG: []byte("png")}}, {StepIndex: 1, Action: "wait", Error: "not found", Duration: time.Second}}, TotalTime: 1020 * time.Millisecond, FinalURL: "/done"}, f.err
}
func (f *toolInspectorFixture) CaptureMultiViewport(_ context.Context, id string, opts models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	f.record("multi", struct {
		ID   string
		Opts models.MultiViewportOpts
	}{id, opts})
	return &models.MultiViewportResult{Captures: []models.ViewportCapture{{Viewport: models.ViewportSpec{Name: "mobile", Width: 375, Height: 812}, Screenshot: models.ScreenshotResult{PNG: []byte("png"), PageTitle: "Mobile", URL: "/"}}}}, f.err
}
func (f *toolInspectorFixture) ComputeVisualDiff(_ context.Context, id, before, after string) (*models.VisualDiff, error) {
	f.record("diff", []string{id, before, after})
	return &models.VisualDiff{BeforeSnapshotID: before, AfterSnapshotID: after, PixelDiffPercent: 12.5, OverlayPNG: []byte("png"), Summary: "header changed"}, f.err
}
func (f *toolInspectorFixture) RunAssertions(_ context.Context, id string, assertions []Assertion) (*AssertionResult, error) {
	f.record("assert", struct {
		ID         string
		Assertions []Assertion
	}{id, assertions})
	return &AssertionResult{Passed: 1, Total: 1, Results: []AssertionCheck{{Assertion: assertions[0], Passed: true, Message: "found"}}}, f.err
}

func TestPreviewAgentToolExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, params, method string
		expectedArgs         func(string) any
		expectedJSON         string
	}{
		{name: "preview_screenshot", params: `{"path":"/page","viewport_w":9000,"viewport_h":9000,"delay_ms":90000}`, method: "screenshot", expectedArgs: func(id string) any {
			return struct {
				ID   string
				Opts models.ScreenshotOpts
			}{id, models.ScreenshotOpts{Path: "/page", ViewportW: 3840, ViewportH: 2160, Delay: 30 * time.Second}}
		}, expectedJSON: `{"image_base64":"cG5n","page_title":"Example","url":"https://example.test/page","captured_at":"2026-10-01T00:00:00Z"}`},
		{name: "preview_screenshot_full", method: "screenshot", expectedArgs: func(id string) any {
			opts := models.DefaultScreenshotOpts()
			opts.FullPage = true
			return struct {
				ID   string
				Opts models.ScreenshotOpts
			}{id, opts}
		}, expectedJSON: `{"image_base64":"cG5n","page_title":"Example","url":"https://example.test/page","captured_at":"2026-10-01T00:00:00Z"}`},
		{name: "preview_console", params: `{"level":"error"}`, method: "console", expectedArgs: func(id string) any { return id }, expectedJSON: `{"messages":[{"level":"error","text":"broken","timestamp":"0001-01-01T00:00:00Z"}],"count":1}`},
		{name: "preview_element", params: `{"selector":"#page"}`, method: "dom", expectedArgs: func(id string) any {
			return struct {
				ID   string
				Opts DOMCaptureOpts
			}{id, DOMCaptureOpts{Selector: "#page", IncludeStyles: true}}
		}, expectedJSON: `{"tag_name":"","bounding_box":{"x":0,"y":0,"width":0,"height":0},"computed_styles":{"color":"rgb(0,0,0)"},"dom_path":"#page","component_name":"Page","component_file":"page.tsx","component_line":7,"props":{"title":"Example"},"component_tree":["Page","App"]}`},
		{name: "preview_screencast_start", params: `{"fps":100}`, method: "start", expectedArgs: func(id string) any {
			return struct {
				ID  string
				FPS int
			}{id, 4}
		}, expectedJSON: `{"screencast_id":"cast","fps":4,"message":"Screencast started at 4 FPS. Use preview_screencast_stop with screencast_id=\"cast\" to stop and retrieve the recording."}`},
		{name: "preview_screencast_stop", params: `{"screencast_id":"cast"}`, method: "stop", expectedArgs: func(string) any { return "cast" }, expectedJSON: `{"format":"gif","data_base64":"Z2lm","duration":2000000000,"frame_count":4}`},
		{name: "preview_interact", params: `{"steps":[{"action":"click","selector":"#submit","timeout_ms":999999,"screenshot":true},{"action":"wait","wait_for":"#done"}]}`, method: "interact", expectedArgs: func(id string) any {
			return struct {
				ID    string
				Steps []models.InteractionStep
			}{id, []models.InteractionStep{{Action: "click", Selector: "#submit", Timeout: time.Minute, Screenshot: true}, {Action: "wait", WaitFor: "#done", Timeout: 10 * time.Second}}}
		}, expectedJSON: `{"steps":[{"step_index":0,"action":"click","success":true,"screenshot_base64":"cG5n","duration_ms":20,"url":"/done"},{"step_index":1,"action":"wait","success":false,"error":"not found","duration_ms":1000,"url":""}],"total_time_ms":1020,"final_url":"/done"}`},
		{name: "preview_multi_viewport", params: `{"path":"/page","delay_ms":999999}`, method: "multi", expectedArgs: func(id string) any {
			return struct {
				ID   string
				Opts models.MultiViewportOpts
			}{id, models.MultiViewportOpts{Path: "/page", Viewports: models.DefaultViewports(), Delay: 30 * time.Second}}
		}, expectedJSON: `{"captures":[{"viewport_name":"mobile","width":375,"height":812,"image_base64":"cG5n","page_title":"Mobile","url":"/"}]}`},
		{name: "preview_visual_diff", params: `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, method: "diff", expectedArgs: func(id string) any { return []string{id, "before", "after"} }, expectedJSON: `{"before_snapshot_id":"before","after_snapshot_id":"after","pixel_diff_percent":12.5,"overlay_base64":"cG5n","summary":"header changed"}`},
		{name: "preview_assert", params: `{"assertions":[{"type":"element_exists","selector":"#submit","description":"submit control"}]}`, method: "assert", expectedArgs: func(id string) any {
			return struct {
				ID         string
				Assertions []Assertion
			}{id, []Assertion{{Type: "element_exists", Selector: "#submit", Description: "submit control"}}}
		}, expectedJSON: `{"passed":1,"failed":0,"total":1,"results":[{"assertion":{"type":"element_exists","selector":"#submit","description":"submit control"},"passed":true,"message":"found"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcomes := []struct {
				name  string
				cause error
			}{{name: "success"}, {name: "browser failure", cause: errors.New("browser unavailable")}}
			for _, outcome := range outcomes {
				t.Run(outcome.name, func(t *testing.T) {
					t.Parallel()
					org, session, previewID := uuid.New(), uuid.New(), uuid.New()
					mock, err := pgxmock.NewPool()
					require.NoError(t, err, "create tenant-scoped preview fixture")
					defer mock.Close()
					mock.ExpectQuery(`SELECT .* FROM preview_instances WHERE org_id = @org_id AND session_id = @session_id`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session}).WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, session, org, uuid.New(), models.PreviewStatusReady, "container", time.Now())...))
					inspector := &toolInspectorFixture{err: outcome.cause}
					p := NewPreviewToolProvider(&Manager{inspector: inspector}, db.NewPreviewStore(mock), zerolog.Nop())
					result, err := p.Execute(context.Background(), session, org, tt.name, json.RawMessage(tt.params))
					if outcome.cause != nil {
						require.ErrorIs(t, err, outcome.cause, "tool must retain inspector failure for retry decisions")
						require.Nil(t, result, "failed tools must not expose partial results")
					} else {
						require.NoError(t, err, "ready previews should execute tool")
						require.JSONEq(t, tt.expectedJSON, string(result), "tool should serialize exact inspector result")
					}
					require.Equal(t, []string{tt.method}, inspector.calls, "tool should route to its intended browser operation")
					require.Equal(t, []any{tt.expectedArgs(previewID.String())}, inspector.args, "tool should preserve parameters and enforce operation bounds")
					require.NoError(t, mock.ExpectationsWereMet(), "tool lookup must bind authenticated tenant and session")
				})
			}
		})
	}
}

func TestPreviewAgentToolRejectsInvalidParameters(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, params, expected string }{
		{"preview_element", `{}`, "selector is required"}, {"preview_screencast_stop", `{}`, "screencast_id is required"}, {"preview_interact", `{"steps":[]}`, "at least one step is required"}, {"preview_visual_diff", `{"before_snapshot_id":"before"}`, "both before_snapshot_id and after_snapshot_id are required"}, {"preview_assert", `{"assertions":[]}`, "at least one assertion is required"},
	}
	for _, tool := range []string{"preview_screenshot", "preview_screenshot_full", "preview_console", "preview_element", "preview_accessibility", "preview_screencast_start", "preview_screencast_stop", "preview_interact", "preview_multi_viewport", "preview_visual_diff", "preview_assert"} {
		tests = append(tests, struct{ name, params, expected string }{tool, "{", "invalid parameters"})
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s-%d", tt.name, i), func(t *testing.T) {
			t.Parallel()
			org, session, id := uuid.New(), uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create parameter validation fixture")
			defer mock.Close()
			mock.ExpectQuery(`SELECT .* FROM preview_instances WHERE org_id = @org_id AND session_id = @session_id`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session}).WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(id, session, org, uuid.New(), models.PreviewStatusPartiallyReady, "container", time.Now())...))
			inspector := &toolInspectorFixture{}
			p := NewPreviewToolProvider(&Manager{inspector: inspector}, db.NewPreviewStore(mock), zerolog.Nop())
			result, err := p.Execute(context.Background(), session, org, tt.name, json.RawMessage(tt.params))
			require.ErrorContains(t, err, tt.expected, "invalid parameters must fail before browser operations")
			require.Nil(t, result, "invalid input should return no result")
			require.Empty(t, inspector.calls, "invalid parameters must not reach browser")
			require.NoError(t, mock.ExpectationsWereMet(), "validation still must use tenant-scoped preview lookup")
		})
	}
}
