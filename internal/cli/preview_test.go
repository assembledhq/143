package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func branchStatusBody(status string) string {
	return `{"data":{"target_id":"target-1","preview_id":"preview-1","repository_full_name":"acme/app",` +
		`"branch":"feature","status":"` + status + `","preview_url":"https://preview.test"}}`
}

// branchPreviewServer serves the resolve/create flow behind `preview create
// --wait`, delegating the status endpoint to statusFor so each test can script
// how readiness (or failure) arrives across successive polls.
func branchPreviewServer(t *testing.T, statusFor func(call int64) (int, string)) *httptest.Server {
	t.Helper()

	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(body string) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(body))
			require.NoError(t, err, "preview response should write")
		}
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /api/v1/repositories":
			write(`{"data":[{"id":"repo-1","full_name":"acme/app"}]}`)
		case http.MethodGet + " /api/v1/repositories/repo-1/branches":
			write(`{"data":[{"name":"feature"}]}`)
		case http.MethodPost + " /api/v1/previews":
			write(`{"data":{"target_id":"target-1","preview_id":"preview-1","status":"starting"}}`)
		case http.MethodGet + " /api/v1/previews/preview-1":
			code, body := statusFor(statusCalls.Add(1))
			if code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			write(body)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func runCreateWait(t *testing.T, server *httptest.Server) (int, string, string) {
	t.Helper()

	executor := &previewToolExecutor{
		client:       NewClient(Config{ServerURL: server.URL, Token: "token"}),
		pollInterval: time.Millisecond,
	}
	// A bounded context keeps a loop that fails to recognise a terminal status
	// from hanging the suite; it surfaces as a failed assertion instead.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := runPreviewCreate(ctx, executor, []string{"--repo", "acme/app", "--branch", "feature", "--wait"}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// The CLI wait loop and waitBranchReady must agree on which statuses end the
// wait. When they drifted, a preview that reached "ready" kept being polled
// until the overall deadline and was reported as a timeout.
func TestRunPreviewCreateWaitTreatsLiveStatusesAsTerminal(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"running", "ready", "partially_ready"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			server := branchPreviewServer(t, func(int64) (int, string) {
				return http.StatusOK, branchStatusBody(status)
			})
			code, stdout, stderr := runCreateWait(t, server)

			require.Equal(t, 0, code, "a preview reporting %q should end the wait successfully: %s", status, stderr)
			require.Contains(t, stdout, "Preview is live: https://preview.test", "wait should report the live preview URL")
		})
	}
}

func TestRunPreviewCreateWaitTreatsDeadStatusesAsTerminal(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"failed", "stopped", "expired", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			server := branchPreviewServer(t, func(int64) (int, string) {
				return http.StatusOK, branchStatusBody(status)
			})
			code, _, stderr := runCreateWait(t, server)

			require.Equal(t, 1, code, "a preview reporting %q should fail the wait", status)
			require.Contains(t, stderr, "preview "+status, "wait should name the terminal status it gave up on")
		})
	}
}

func TestRunPreviewCreateWaitRetriesTransientStatusError(t *testing.T) {
	t.Parallel()

	var statusCalls atomic.Int64
	server := branchPreviewServer(t, func(call int64) (int, string) {
		statusCalls.Store(call)
		if call == 1 {
			return http.StatusServiceUnavailable, ""
		}
		return http.StatusOK, branchStatusBody("running")
	})
	code, stdout, stderr := runCreateWait(t, server)

	require.Equal(t, 0, code, "wait should recover from a transient status response: %s", stderr)
	require.EqualValues(t, 2, statusCalls.Load(), "wait should poll again after a transient response")
	require.Contains(t, stdout, "temporarily unavailable", "wait should explain that a transient status error is being retried")
}

func TestRunPreviewCreateWaitFailsFastOnPermanentStatusError(t *testing.T) {
	t.Parallel()

	var statusCalls atomic.Int64
	server := branchPreviewServer(t, func(call int64) (int, string) {
		statusCalls.Store(call)
		return http.StatusUnauthorized, ""
	})
	code, _, stderr := runCreateWait(t, server)

	require.Equal(t, 1, code, "wait should surface a permanent status failure")
	require.EqualValues(t, 1, statusCalls.Load(), "wait should not retry a permanent status failure")
	require.Contains(t, stderr, "preview status failed", "wait should report why it stopped polling")
}
