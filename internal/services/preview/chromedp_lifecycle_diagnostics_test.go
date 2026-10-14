package preview

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

const lifecycleBrowserOutputLimit = 8 * 1024

var lifecycleBrowserOutputURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s<>]+`)

type lifecycleBrowserOutput struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	limit      int
	truncated  bool
	started    time.Time
	firstWrite time.Duration
}

func (w *lifecycleBrowserOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 && len(data) > 0 {
		w.firstWrite = time.Since(w.started)
	}
	remaining := w.limit - w.buffer.Len()
	if len(data) > remaining {
		w.truncated = true
	}
	_, err := w.buffer.Write(data[:min(len(data), remaining)])
	return len(data), err
}

func (w *lifecycleBrowserOutput) snapshot() (string, bool, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return lifecycleBrowserOutputURL.ReplaceAllString(w.buffer.String(), "[redacted URL]"), w.truncated, w.firstWrite
}

func lifecycleBrowserExecutable() (string, error) {
	// Match chromedp's supported local lookup order, then bind this probe to the
	// resolved executable. No process arguments or inherited environment are logged.
	candidates := []string{"headless_shell", "headless-shell", "chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "google-chrome-beta", "google-chrome-unstable", "/usr/bin/google-chrome", "/usr/local/bin/chrome", "/snap/bin/chromium", "chrome"}
	if runtime.GOOS == "darwin" {
		candidates = []string{"/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	}
	for _, candidate := range candidates {
		if binary, err := exec.LookPath(candidate); err == nil {
			return binary, nil
		}
	}
	return "", errors.New("no supported local Chrome executable found")
}

func verifyLifecycleBrowserReadiness(t *testing.T) {
	t.Helper()
	binary, err := lifecycleBrowserExecutable()
	require.NoError(t, err, "resolve the actual local browser before readiness verification")
	versionCtx, cancelVersion := context.WithTimeout(context.Background(), 2*time.Second)
	versionOutput := &lifecycleBrowserOutput{limit: 512, started: time.Now()}
	versionCommand := exec.CommandContext(versionCtx, binary, "--version")
	versionCommand.Stdout, versionCommand.Stderr = versionOutput, versionOutput
	versionCommand.WaitDelay = time.Second
	versionErr := versionCommand.Run()
	cancelVersion()
	version, _, _ := versionOutput.snapshot()
	if versionErr != nil {
		t.Logf("browser readiness: stage=version binary=%q version=unavailable bounded_probe_error=%q", binary, versionErr.Error())
	} else {
		t.Logf("browser readiness: stage=version binary=%q version=%q", binary, strings.TrimSpace(version))
	}

	started := time.Now()
	output := &lifecycleBrowserOutput{limit: lifecycleBrowserOutputLimit, started: started}
	opts := append([]chromedp.ExecAllocatorOption(nil), chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.ExecPath(binary),
		chromedp.CombinedOutput(output),
	)
	allocCtx, cancelAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	err = chromedp.Run(browserCtx)
	cancelBrowser()
	cancelAllocator()
	captured, truncated, firstOutput := output.snapshot()
	t.Logf("browser readiness: stage=startup binary=%q elapsed=%s first_output=%s headless=true disable_gpu=true no_sandbox=true disable_dev_shm_usage=true websocket_read_timeout=20s", binary, time.Since(started), firstOutput)
	if err != nil {
		t.Logf("browser readiness: bounded stdout/stderr=%q truncated=%t", captured, truncated)
		message := err.Error()
		if len(message) > lifecycleBrowserOutputLimit {
			message = message[:lifecycleBrowserOutputLimit] + " [truncated]"
		}
		err = errors.New(lifecycleBrowserOutputURL.ReplaceAllString(message, "[redacted URL]"))
	}
	// Keep the default 20-second allocator threshold, fail on the first error,
	// and close this probe before the untouched inspector starts its own browser.
	// Each real fixture therefore pays one additional explicit readiness startup.
	require.NoError(t, err, "browser readiness must succeed on its first startup before inspector construction")
}

func TestLifecycleBrowserDiagnosticsAreBoundedAndRedactURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		limit     int
		expected  string
		truncated bool
	}{
		{name: "ordinary output", input: "Chrome startup failed\n", limit: 64, expected: "Chrome startup failed\n"},
		{name: "URL credentials and query", input: "connect https://fixture:synthetic-secret@example.test/path?token=synthetic-token failed", limit: 256, expected: "connect [redacted URL] failed"},
		{name: "bounded output", input: "Chrome startup failed", limit: 6, expected: "Chrome", truncated: true},
		{name: "truncated URL", input: "connect ws://example.test/path?token=synthetic-token", limit: 40, expected: "connect [redacted URL]", truncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			output := &lifecycleBrowserOutput{limit: tt.limit, started: time.Now()}
			written, err := output.Write([]byte(tt.input))
			require.NoError(t, err, "diagnostic capture must not disrupt the browser output stream")
			require.Equal(t, len(tt.input), written, "capture must consume all browser output while retaining only its bounded prefix")
			actual, truncated, _ := output.snapshot()
			require.Equal(t, tt.expected, actual, "diagnostics must preserve useful output while removing complete URL contents")
			require.Equal(t, tt.truncated, truncated, "diagnostics must distinguish truncated output from a complete capture")
		})
	}
}
