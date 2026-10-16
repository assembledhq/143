package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCapturedObjectCleanupAllowsDelayedReaping(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	identity, err := codeReviewInputIdentityFromSession(reviewInputsSession(t, f.base, f.head))
	require.NoError(t, err, "cleanup fixture must retain the captured identity")
	encoded, err := json.Marshal(identity)
	require.NoError(t, err, "cleanup fixture must encode the captured identity")
	prefix, _, found := strings.Cut(prepareCodeReviewInputsScript, "\ndef digest(file):")
	require.True(t, found, "delayed reaping must exercise the production object verifier")
	script := prefix + `
import signal
real_popen = subprocess.Popen
created = []
def launch(args, **kwargs):
    first = not created
    body = "import signal,sys,time; signal.signal(signal.SIGUSR1, lambda *_: (time.sleep(1.5), sys.exit(0))); print(" + repr(identity["head_sha"] if first else "invalid-header") + ",flush=True); signal.pause()"
    process = real_popen(["/usr/bin/python3", "-I", "-S", "-c", body], **kwargs)
    if first:
        process.kill = lambda: os.kill(process.pid, signal.SIGUSR1)
    created.append(process)
    return process
subprocess.Popen = launch
started = time.monotonic()
try:
    try:
        verify_captured_objects(identity["base_sha"])
        raise AssertionError("the invalid object header was accepted")
    except ValueError:
        assert len(created) == 2 and all(process.poll() is not None for process in created), "delayed object children were not reaped"
        elapsed = time.monotonic() - started
        assert 1.5 <= elapsed < 3.5, "delayed cleanup escaped its finite reap allowance"
        print("delayed_reaping_confirmed_without_cleanup_uncertainty")
finally:
    for process in created:
        if process.poll() is None:
            os.kill(process.pid, signal.SIGKILL)
        process.wait(timeout=3)
`
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", script, string(encoded), ".git/unused", "", strconv.FormatFloat(float64(deadline.UnixNano())/float64(time.Second), 'f', 6, 64), "true", "true", "true")
	command.Dir = f.workDir
	output, err := command.CombinedOutput()
	require.NoError(t, err, "a confirmed child reap beyond one second must preserve the original source error: %s", output)
	require.Contains(t, string(output), "delayed_reaping_confirmed_without_cleanup_uncertainty", "confirmed delayed reaping must avoid terminal quarantine")
}

type sharedInputCleanupJoinProvider struct {
	SandboxProvider
	entered  chan struct{}
	finished chan struct{}
	delay    time.Duration
}

func (p *sharedInputCleanupJoinProvider) Exec(ctx context.Context, _ *Sandbox, _ string, _, _ io.Writer) (int, error) {
	defer close(p.finished)
	close(p.entered)
	<-ctx.Done()
	time.Sleep(p.delay)
	return -1, ctx.Err()
}

func TestSharedReviewInputCleanupAcknowledgementHasBoundedJoinAllowance(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		delay     time.Duration
		uncertain bool
	}{
		{name: "confirmation after former outer deadline", delay: 5250 * time.Millisecond},
		{name: "confirmation beyond absolute cleanup deadline", delay: 11 * time.Second, uncertain: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				provider := &sharedInputCleanupJoinProvider{entered: make(chan struct{}), finished: make(chan struct{}), delay: scenario.delay}
				orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, logger: zerolog.Nop()}
				session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				completed := make(chan error, 1)
				go func() {
					completed <- orchestrator.ensureCodeReviewInputs(ctx, session, &Sandbox{ID: "shared", WorkDir: "/workspace"})
				}()
				<-provider.entered
				started := time.Now()
				cancel()
				err := <-completed
				require.ErrorIs(t, err, context.Canceled, "cleanup joining must preserve the owner cancellation")
				if scenario.uncertain {
					require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "missing confirmation at the cleanup deadline must stay terminal")
					require.Equal(t, 10*time.Second, time.Since(started), "the cleanup allowance must expire without per-stage renewal")
				} else {
					require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "complete confirmation must not lose a race to the old five-second deadline")
					require.Equal(t, scenario.delay, time.Since(started), "cleanup must finish as soon as confirmation returns")
				}
				<-provider.finished
			})
		})
	}
}

type reapedReviewInputCallbackDelayProvider struct {
	SandboxProvider
	entered chan int
	reaped  atomic.Bool
}

func (p *reapedReviewInputCallbackDelayProvider) Exec(ctx context.Context, sandbox *Sandbox, _ string, _, _ io.Writer) (int, error) {
	command := exec.Command("/usr/bin/python3", "-I", "-S", "-c", "import signal; signal.pause()")
	command.Dir = sandbox.WorkDir
	if err := command.Start(); err != nil {
		return -1, err
	}
	p.entered <- command.Process.Pid
	<-ctx.Done()
	killErr := command.Process.Kill()
	waitErr := command.Wait()
	var exitErr *exec.ExitError
	if killErr != nil || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
		return 77, errors.Join(killErr, waitErr)
	}
	p.reaped.Store(true)
	time.Sleep(5250 * time.Millisecond)
	return -1, ctx.Err()
}

func TestSharedReviewInputCleanupJoinsConfirmedReapingAfterDelayedCallback(t *testing.T) {
	t.Parallel()
	provider := &reapedReviewInputCallbackDelayProvider{entered: make(chan int, 1)}
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, github: tokens, logger: zerolog.Nop()}
	session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	var finished atomic.Bool
	sandbox := &Sandbox{ID: "shared", WorkDir: t.TempDir()}
	go func() {
		completed <- orchestrator.ensureCodeReviewInputs(ctx, session, sandbox)
		finished.Store(true)
	}()
	t.Cleanup(func() {
		cancel()
		require.Eventually(t, finished.Load, 12*time.Second, 10*time.Millisecond, "the delayed provider callback must finish before fixture cleanup")
	})
	var pid int
	select {
	case pid = <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the cleanup fixture did not start its actual child")
	}
	t.Cleanup(func() {
		if provider.reaped.Load() {
			return
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("failed to stop the owned callback fixture child: %v", err)
		}
	})
	started := time.Now()
	cancel()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.Canceled, "confirmed cleanup must preserve the caller cancellation")
		require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "a callback delayed beyond five seconds must retain its confirmed reap")
	case <-time.After(12 * time.Second):
		t.Fatal("the delayed callback did not complete within the finite join allowance")
	}
	require.Greater(t, time.Since(started), 5*time.Second, "the actual callback must exercise the former deadline race")
	require.Less(t, time.Since(started), 10*time.Second, "the callback must remain within the finite join allowance")
	require.True(t, provider.reaped.Load(), "the owned child must be reaped before its completion callback")
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the reaped child must not survive the provider acknowledgment")
	require.Zero(t, tokens.calls.Load(), "cancellation cleanup must never acquire authentication")
}
