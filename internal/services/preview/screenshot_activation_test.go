package preview

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/stretchr/testify/require"
)

type screenshotExecutor struct {
	commands []string
	failAt   string
	err      error
	params   *page.CaptureScreenshotParams
}

func (e *screenshotExecutor) Execute(_ context.Context, method string, params, result any) error {
	e.commands = append(e.commands, method)
	if method == e.failAt {
		return e.err
	}
	if method == page.CommandCaptureScreenshot {
		e.params = params.(*page.CaptureScreenshotParams)
		result.(*page.CaptureScreenshotReturns).Data = base64.StdEncoding.EncodeToString([]byte("fixture pixels"))
	}
	return nil
}

func TestCapturePreviewScreenshot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failAt, errorStage string
		fullPage                 bool
		commands                 []string
		pixels                   []byte
	}{
		{name: "viewport capture", commands: []string{page.CommandBringToFront, page.CommandCaptureScreenshot}, pixels: []byte("fixture pixels")},
		{name: "full-page capture", fullPage: true, commands: []string{page.CommandBringToFront, page.CommandCaptureScreenshot}, pixels: []byte("fixture pixels")},
		{name: "activation failure stops capture", failAt: page.CommandBringToFront, errorStage: "activate screenshot target", commands: []string{page.CommandBringToFront}},
		{name: "capture failure retains cause", failAt: page.CommandCaptureScreenshot, errorStage: "capture target pixels", commands: []string{page.CommandBringToFront, page.CommandCaptureScreenshot}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("fixture protocol failure")
			executor := &screenshotExecutor{failAt: tt.failAt, err: cause}
			var pixels []byte
			err := capturePreviewScreenshot(&pixels, tt.fullPage).Do(cdp.WithExecutor(context.Background(), executor))
			if tt.errorStage != "" {
				require.ErrorIs(t, err, cause, "protocol failures must retain their original cause")
				require.ErrorContains(t, err, tt.errorStage, "failure must identify activation or pixel capture stage")
			} else {
				require.NoError(t, err, "activated screenshot must return protocol pixels")
				require.Equal(t, tt.fullPage, executor.params.CaptureBeyondViewport, "only full-page captures may extend beyond the viewport")
				require.True(t, executor.params.FromSurface, "both capture modes must use rendered surface pixels")
			}
			require.Equal(t, tt.commands, executor.commands, "activation must precede capture and its failure must stop further commands")
			require.Equal(t, tt.pixels, pixels, "failed capture must not return fabricated successful pixels")
		})
	}
}
