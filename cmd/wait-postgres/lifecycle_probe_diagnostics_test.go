package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const lifecycleWaiterProbeOutputLimit = 64 * 1024

// Child errors and output may include a fixture DSN, database name, or local
// path. Retain a bounded prefix only for classification; never publish it.
type lifecycleWaiterProbeOutput struct {
	data      []byte
	truncated bool
}

func (output *lifecycleWaiterProbeOutput) Write(data []byte) (int, error) {
	remaining := lifecycleWaiterProbeOutputLimit - len(output.data)
	retained := min(len(data), remaining)
	output.data = append(output.data, data[:retained]...)
	output.truncated = output.truncated || retained < len(data)
	return len(data), nil
}

func lifecycleWaiterProbeSummary(runErr, contextErr error, output *lifecycleWaiterProbeOutput) string {
	outcome, exitCode := "success", 0
	deadline := errors.Is(contextErr, context.DeadlineExceeded)
	if runErr != nil {
		outcome, exitCode = "run_failure", -1
		var exitErr *exec.ExitError
		var lookupErr *exec.Error
		var pathErr *os.PathError
		switch {
		case errors.As(runErr, &exitErr):
			outcome, exitCode = "nonzero_exit", exitErr.ExitCode()
		case errors.As(runErr, &lookupErr), errors.As(runErr, &pathErr):
			outcome = "start_failure"
		}
		if deadline {
			outcome = "deadline"
		}
	}
	failedTest, panicked, race, testTimeout := false, false, false, false
	for line := range strings.SplitSeq(string(output.data), "\n") {
		line = strings.TrimSpace(line)
		failedTest = failedTest || strings.HasPrefix(line, "--- FAIL: TestLifecycleWaiterConnectionPostgres (")
		panicked = panicked || strings.HasPrefix(line, "panic:")
		race = race || line == "WARNING: DATA RACE" || strings.Contains(line, "race detected during execution of test")
		testTimeout = testTimeout || strings.HasPrefix(line, "panic: test timed out after ")
	}
	// Every published value is a fixed enum, boolean, or process exit code. In
	// particular, do not format runErr, contextErr, or any captured child text.
	return fmt.Sprintf("child_outcome=%s exit_code=%d deadline=%t failed_waiter_test=%t panic=%t race=%t test_timeout=%t output_truncated=%t", outcome, exitCode, deadline, failedTest, panicked, race, testTimeout, output.truncated)
}

func TestLifecycleWaiterProbeSummary(t *testing.T) {
	t.Parallel()

	const privateText = "postgres://synthetic-user:synthetic-secret@example.invalid/generated-database /private/synthetic-fixture/test-binary"
	tests := []struct {
		name       string
		runErr     error
		contextErr error
		output     string
		expected   string
	}{
		{
			name:     "success does not publish captured text",
			output:   privateText,
			expected: "child_outcome=success exit_code=0 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:     "nonzero exit without process state",
			runErr:   &exec.ExitError{},
			output:   privateText,
			expected: "child_outcome=nonzero_exit exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:       "deadline without exposing underlying error",
			runErr:     errors.New(privateText),
			contextErr: context.DeadlineExceeded,
			output:     privateText,
			expected:   "child_outcome=deadline exit_code=-1 deadline=true failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:     "lookup failure does not publish executable name",
			runErr:   &exec.Error{Name: privateText, Err: exec.ErrNotFound},
			output:   privateText,
			expected: "child_outcome=start_failure exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:     "path failure does not publish executable path",
			runErr:   &os.PathError{Op: "fork/exec", Path: privateText, Err: os.ErrPermission},
			output:   privateText,
			expected: "child_outcome=start_failure exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:     "unknown run failure does not publish error",
			runErr:   errors.New(privateText),
			output:   privateText,
			expected: "child_outcome=run_failure exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:       "completed process remains successful if context later expires",
			contextErr: context.DeadlineExceeded,
			output:     privateText,
			expected:   "child_outcome=success exit_code=0 deadline=true failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
		{
			name:     "only allowlisted failure classifications escape child output",
			runErr:   errors.New(privateText),
			output:   "--- FAIL: TestLifecycleWaiterConnectionPostgres (0.01s)\n" + privateText + "\npanic: test timed out after 15s\nWARNING: DATA RACE\n",
			expected: "child_outcome=run_failure exit_code=-1 deadline=false failed_waiter_test=true panic=true race=true test_timeout=true output_truncated=false",
		},
		{
			name:     "other test names are never echoed",
			runErr:   errors.New(privateText),
			output:   "--- FAIL: Test" + privateText + " (0.01s)\n",
			expected: "child_outcome=run_failure exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=false",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output lifecycleWaiterProbeOutput
			written, err := output.Write([]byte(tt.output))
			require.NoError(t, err, "private capture should accept child output without forwarding it")
			require.Equal(t, len(tt.output), written, "capture should consume the entire child write")
			actual := lifecycleWaiterProbeSummary(tt.runErr, tt.contextErr, &output)
			require.Equal(t, tt.expected, actual, "summary should contain only the exact allowlisted process classification")
			require.NotContains(t, actual, "synthetic-secret", "child summary must not publish credentials")
			require.NotContains(t, actual, "/private/", "child summary must not publish local paths")
			require.NotContains(t, actual, "generated-database", "child summary must not publish fixture database identifiers")
		})
	}
}

func TestLifecycleWaiterProbeOutputBound(t *testing.T) {
	t.Parallel()
	var output lifecycleWaiterProbeOutput
	input := strings.Repeat("x", lifecycleWaiterProbeOutputLimit+1)
	written, err := output.Write([]byte(input))
	require.NoError(t, err, "bounded capture should discard excess child output without interrupting the child")
	require.Equal(t, len(input), written, "bounded capture must consume writes beyond its retention limit")
	require.Equal(t, strings.Repeat("x", lifecycleWaiterProbeOutputLimit), string(output.data), "capture must retain no more than its fixed output limit")
	written, err = output.Write([]byte("more private child output"))
	require.NoError(t, err, "capture should continue discarding output after reaching its limit")
	require.Equal(t, len("more private child output"), written, "capture must continue consuming child writes")
	require.Equal(t, lifecycleWaiterProbeOutputLimit, len(output.data), "later writes must not grow retained output")
	require.Equal(t, "child_outcome=nonzero_exit exit_code=-1 deadline=false failed_waiter_test=false panic=false race=false test_timeout=false output_truncated=true", lifecycleWaiterProbeSummary(&exec.ExitError{}, nil, &output), "summary should explicitly mark incomplete output classification")
}
