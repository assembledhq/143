package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type reviewCheckoutExecStub struct {
	SandboxProvider
	exec func(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error)
}

func (p reviewCheckoutExecStub) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	return p.exec(ctx, sandbox, command, stdout, stderr)
}

func TestReviewCheckoutBatch(t *testing.T) {
	t.Parallel()
	const (
		head         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		otherHead    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		token        = "synthetic/checkout+token"
		escapedToken = "synthetic%2Fcheckout%2Btoken"
	)
	type step struct {
		command        string
		exit           int
		stdout, stderr string
		err            error
		cancel         bool
	}
	present := step{command: "probe"}
	missing := step{command: "probe", exit: 1}
	exact := step{command: "exact"}
	fallback := step{command: "exact", exit: 1}
	verify := step{command: "checkout", stdout: head + "\n"}
	tests := []struct {
		name    string
		steps   []step
		message string
		cause   error
	}{
		{name: "local head", steps: []step{present, verify}},
		{name: "exact fetch", steps: []step{missing, exact, verify}},
		{name: "pull ref fallback", steps: []step{missing, fallback, {command: "pull"}, verify}},
		{name: "pull ref fallback leaves exact head absent", steps: []step{missing, fallback, {command: "pull"}, {command: "checkout", exit: reviewCheckoutFailed}}, message: "checkout captured review head"},
		{name: "fetch failures", steps: []step{missing, fallback, {command: "pull", exit: 2, stderr: escapedToken}}, message: "captured review head unavailable"},
		{name: "stale head", steps: []step{present, {command: "checkout", stdout: otherHead}}, message: otherHead, cause: ErrStalePullRequestHead},
		{name: "checkout failure", steps: []step{present, {command: "checkout", exit: reviewCheckoutFailed, stderr: "tracked edit " + token}}, message: "tracked edit"},
		{name: "verify failure", steps: []step{present, {command: "checkout", exit: reviewCheckoutVerifyFailed}}, message: "verify captured review head"},
		{name: "transport error with missing exit", steps: []step{{command: "probe", exit: 1, err: ErrCodeReviewPreparationTransport}}, message: "inspect captured review head", cause: ErrCodeReviewPreparationTransport},
		{name: "fetch transport error", steps: []step{missing, {command: "exact", err: fmt.Errorf("%s: %w", escapedToken, ErrCodeReviewPreparationTransport)}}, message: "fetch captured review head", cause: ErrCodeReviewPreparationTransport},
		{name: "fetch cancellation prevents fallback", steps: []step{missing, {command: "exact", exit: 1, cancel: true}}, message: "fetch captured review head", cause: context.Canceled},
		{name: "checkout deadline error", steps: []step{present, {command: "checkout", err: context.DeadlineExceeded}}, message: "checkout captured review head", cause: context.DeadlineExceeded},
		{name: "cancellation during successful probe prevents checkout", steps: []step{{command: "probe", cancel: true}}, message: "inspect captured review head", cause: context.Canceled},
		{name: "unexpected shell failure", steps: []step{{command: "probe", exit: 127, err: errors.New("unexpected shell error")}}, message: "unexpected shell error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deadline time.Time
			var calls int
			sandbox := &Sandbox{Env: map[string]string{"EXISTING_ENV": "present"}}
			provider := reviewCheckoutExecStub{exec: func(ctx context.Context, sb *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				require.Less(t, calls, len(tt.steps), "executor should only see the expected calls")
				step := tt.steps[calls]
				execDeadline, ok := ctx.Deadline()
				require.True(t, ok, "every call should inherit the checkout budget")
				if calls == 0 {
					deadline = execDeadline
					require.LessOrEqual(t, time.Until(deadline), 2*time.Minute, "checkout budget should be bounded")
				}
				require.Equal(t, deadline, execDeadline, "fetch and checkout should share the same deadline")
				calls++
				require.NotContains(t, command, token, "credentials must stay out of commands")
				switch step.command {
				case "probe", "checkout":
					require.Same(t, sandbox, sb, "checkout must run in the original sandbox")
					require.NotContains(t, sb.Env, "GH143_REVIEW_FETCH_TOKEN", "worktree subprocesses must not receive fetch credentials")
					if step.command == "probe" {
						require.Equal(t, "git -c maintenance.auto=false -c gc.auto=0 cat-file -e '"+head+"^{commit}'", command, "probe should run on its own before mutation")
					} else {
						require.Contains(t, command, "git -c maintenance.auto=false -c gc.auto=0 checkout -B 'review' '"+head+"'", "checkout must use the pinned head")
						require.Contains(t, command, "git rev-parse HEAD", "checkout must verify HEAD in the same executor call")
						require.NotContains(t, command, "cat-file -e", "checkout must follow a separate probe")
					}
				case "exact", "pull":
					require.NotSame(t, sandbox, sb, "authentication must use a sandbox copy")
					require.Equal(t, map[string]string{"EXISTING_ENV": "present", "GH143_REVIEW_FETCH_TOKEN": token}, sb.Env, "fetch sandbox should inherit environment and receive temporary credentials")
					ref := head
					if step.command == "pull" {
						ref = "pull/42/head"
					}
					require.Equal(t, codeReviewFetchCommand("https://example.invalid/repo", ref), command, "fallback must use the expected remote ref")
				}
				_, err := io.WriteString(stdout, step.stdout)
				require.NoError(t, err, "stub stdout should be recorded")
				_, err = io.WriteString(stderr, step.stderr)
				require.NoError(t, err, "stub stderr should be recorded")
				if step.cancel {
					cancel()
				}
				return step.exit, step.err
			}}
			o := &Orchestrator{provider: provider}
			err := o.checkoutCodeReviewHead(ctx, sandbox, "https://example.invalid/repo", token, 42, "review", head)
			require.Equal(t, len(tt.steps), calls, "the expected number of executor calls should run")
			require.Equal(t, map[string]string{"EXISTING_ENV": "present"}, sandbox.Env, "fetch must leave the source environment intact")
			if tt.message == "" {
				require.NoError(t, err, "checkout should succeed at the captured head")
				return
			}
			require.ErrorContains(t, err, tt.message, "failure should retain the relevant stage or diagnostics")
			require.NotContains(t, err.Error(), token, "failure should redact the raw credential")
			require.NotContains(t, err.Error(), escapedToken, "failure should redact the encoded credential")
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "failure should retain its classification")
			}
		})
	}
}

func TestReviewCheckoutBatchWithRealGit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		hook          string
		wantStaleHead bool
	}{
		{
			name: "fetched head checkout hook receives no fetch credentials",
			hook: "if [ -n \"$GH143_REVIEW_FETCH_TOKEN\" ]; then exit 1; fi",
		},
		{
			name:          "verification detects a head moved by checkout hook",
			hook:          "git update-ref HEAD HEAD^",
			wantStaleHead: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source, _, head, mergeBase := newReviewInputsGitFixture(t)
			directory := t.TempDir()
			reviewInputsGit(t, directory, "init", "-qb", "main")
			hook := "#!/bin/sh\nset -e\n" + tt.hook + "\nprintf ran > checkout-hook-ran\n"
			hooks := filepath.Join(directory, "hooks")
			require.NoError(t, os.Mkdir(hooks, 0o700), "isolated hook directory should exist")
			reviewInputsGit(t, directory, "config", "core.hooksPath", hooks)
			require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(hook), 0o700), "synthetic post-checkout hook should exist")
			sandbox := &Sandbox{WorkDir: directory}
			o := &Orchestrator{provider: localReviewEvidenceProvider{}}
			err := o.checkoutCodeReviewHead(t.Context(), sandbox, source, "synthetic-token", 42, "review", head)
			expectedHead := head
			if tt.wantStaleHead {
				require.ErrorIs(t, err, ErrStalePullRequestHead, "checkout must reject a HEAD changed by its hook")
				expectedHead = mergeBase
			} else {
				require.NoError(t, err, "checkout should fetch the missing head and run its hook without credentials")
			}
			require.Equal(t, expectedHead, reviewInputsGit(t, directory, "rev-parse", "HEAD"), "worktree should end at the expected head")
			observed, err := os.ReadFile(filepath.Join(directory, "checkout-hook-ran"))
			require.NoError(t, err, "post-checkout hook must run")
			require.Equal(t, "ran", string(observed), "post-checkout hook should complete")
		})
	}
}

func TestReviewCheckoutBatchPartialClone(t *testing.T) {
	t.Parallel()
	source, _, _, _ := newReviewInputsGitFixture(t)
	reviewInputsGit(t, source, "config", "uploadpack.allowFilter", "true")
	reviewInputsGit(t, source, "config", "uploadpack.allowAnySHA1InWant", "true")
	root := t.TempDir()
	directory := filepath.Join(root, "checkout")
	reviewInputsGit(t, root, "-c", "maintenance.auto=false", "-c", "gc.auto=0", "clone", "-q", "--no-checkout", "--filter=blob:none", "file://"+source, directory)

	// The promisor origin can provide this commit and its new blob, neither of
	// which existed when the checkout was cloned.
	const content = "captured update after clone\n"
	require.NoError(t, os.WriteFile(filepath.Join(source, "change.txt"), []byte(content), 0o600), "new captured source should be written")
	reviewInputsGit(t, source, "commit", "-qam", "advance captured review")
	head := reviewInputsGit(t, source, "rev-parse", "HEAD")
	reviewInputsGit(t, directory, "config", "maintenance.commit-graph.enabled", "true")
	reviewInputsGit(t, directory, "config", "maintenance.commit-graph.auto", "1")

	trace := filepath.Join(root, "git-trace.json")
	sandbox := &Sandbox{WorkDir: directory, Env: map[string]string{"GIT_TRACE2_EVENT": trace}}
	o := &Orchestrator{provider: localReviewEvidenceProvider{}}
	require.NoError(t, o.checkoutCodeReviewHead(t.Context(), sandbox, filepath.Join(root, "unavailable-explicit-remote"), "", 42, "review", head), "Git should lazily fetch the captured commit and blob through its promisor origin")
	require.Equal(t, head, reviewInputsGit(t, directory, "rev-parse", "HEAD"), "partial clone should end at the exact captured head")
	actual, err := os.ReadFile(filepath.Join(directory, "change.txt"))
	require.NoError(t, err, "lazy fetch should materialize the new blob")
	require.Equal(t, content, string(actual), "checkout should materialize the captured source")
	traced, err := os.ReadFile(trace)
	require.NoError(t, err, "Git should emit a trace of its child processes")
	require.Contains(t, string(traced), "--filter=blob:none", "Git should attempt a promisor fetch")
	require.NotContains(t, string(traced), `"maintenance","run","--auto"`, "promisor fetches should suppress automatic maintenance")
	require.NoDirExists(t, filepath.Join(directory, ".git", "objects", "info", "commit-graphs"), "checkout should preserve the integrity guard's prohibition on commit graphs")
}
