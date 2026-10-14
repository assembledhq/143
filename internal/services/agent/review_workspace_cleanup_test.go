package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type cleanupSignalFailureProvider struct {
	disconnectedReviewEvidenceProvider
}

func (p cleanupSignalFailureProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if stdin != nil {
		if strings.Count(reviewWorkspacePreparationPython, "def stop_child():") != 1 {
			return -1, errors.New("cleanup regression could not instrument the trusted wrapper")
		}
		observed := strings.Replace(reviewWorkspacePreparationPython, "def stop_child():", cleanupSignalFailurePython+"\ndef stop_child():", 1)
		command = strings.Replace(command, shellEscapeSingleQuote(reviewWorkspacePreparationPython), shellEscapeSingleQuote(observed), 1)
	}
	return p.disconnectedReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, stdin, stdout, stderr)
}

// Only synthetic signal failures and acknowledgement bytes are recorded. The
// wrapper and its real child receive no credentials, argv logs, or env dumps.
const cleanupSignalFailurePython = `
import errno
cleanup_test_killpg = os.killpg
cleanup_test_open = open
cleanup_test_os_open = os.open
cleanup_test_fdopen = os.fdopen
cleanup_test_ack_descriptors = set()
cleanup_test_term_count = 0

def cleanup_test_signal(group, sig):
    global cleanup_test_term_count
    mode = os.environ["REVIEW_TEST_CLEANUP_MODE"]
    if sig == signal.SIGTERM:
        cleanup_test_term_count += 1
    denied = (sig == signal.SIGTERM and mode in ("term_always", "both_always")) or (sig == signal.SIGTERM and mode == "term_once" and cleanup_test_term_count == 1) or (sig == signal.SIGKILL and mode == "both_always")
    lookup = sig == signal.SIGTERM and mode == "lookup_once" and cleanup_test_term_count == 1
    with cleanup_test_open(os.environ["REVIEW_TEST_CLEANUP_SIGNALS"], "a") as saved:
        saved.write(json.dumps({"signal": "TERM" if sig == signal.SIGTERM else "KILL", "denied": denied, "lookup": lookup}) + "\n")
    if denied:
        raise PermissionError(errno.EPERM, "synthetic cleanup signal denial")
    if lookup:
        raise ProcessLookupError(errno.ESRCH, "synthetic cleanup lookup race")
    return cleanup_test_killpg(group, sig)

class CleanupTestAckWriter:
    def __init__(self, saved):
        self.saved = saved
        self.parts = []
    def __enter__(self):
        self.saved.__enter__()
        return self
    def write(self, value):
        self.parts.append(value)
        return self.saved.write(value)
    def __getattr__(self, name):
        return getattr(self.saved, name)
    def __exit__(self, kind, value, traceback):
        try:
            return self.saved.__exit__(kind, value, traceback)
        finally:
            with cleanup_test_open(os.environ["REVIEW_TEST_CLEANUP_ACK"], "w") as recorded:
                recorded.write("".join(self.parts))

def cleanup_test_record_open(name, mode="r", *args, **kwargs):
    saved = cleanup_test_open(name, mode, *args, **kwargs)
    if str(name) == cancel_path + ".done" and "w" in mode:
        return CleanupTestAckWriter(saved)
    return saved

def cleanup_test_record_descriptor(name, *args, **kwargs):
    descriptor = cleanup_test_os_open(name, *args, **kwargs)
    if str(name) == cancel_path + ".done":
        cleanup_test_ack_descriptors.add(descriptor)
    return descriptor

def cleanup_test_record_fdopen(descriptor, mode="r", *args, **kwargs):
    saved = cleanup_test_fdopen(descriptor, mode, *args, **kwargs)
    if descriptor in cleanup_test_ack_descriptors and "w" in mode:
        cleanup_test_ack_descriptors.remove(descriptor)
        return CleanupTestAckWriter(saved)
    return saved

os.killpg = cleanup_test_signal
os.open = cleanup_test_record_descriptor
os.fdopen = cleanup_test_record_fdopen
open = cleanup_test_record_open
`

type cleanupSignalObservation struct {
	Signal string `json:"signal"`
	Denied bool   `json:"denied"`
	Lookup bool   `json:"lookup"`
}

func newCleanupSlowGitObject(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	initialize := exec.Command("/usr/bin/git", "init", "--quiet", "--template=/dev/null", root)
	out, err := initialize.CombinedOutput()
	require.NoError(t, err, "cleanup fixture should initialize an isolated Git database: %s", out)
	create := exec.Command("/usr/bin/git", "-C", root, "hash-object", "-w", "--stdin")
	create.Stdin = io.LimitReader(reviewZeroSourceReader{}, 1024*1024*1024)
	out, err = create.CombinedOutput()
	require.NoError(t, err, "Git must create the legitimate slow-hash object itself: %s", out)
	oid := strings.TrimSpace(string(out))
	require.Regexp(t, `^[0-9a-f]{40}$`, oid, "ordinary SHA1 fixture should identify its complete native object")
	relative := filepath.Join("objects", oid[:2], oid[2:])
	compressed, err := os.ReadFile(filepath.Join(root, ".git", relative))
	require.NoError(t, err, "shared immutable compressed fixture should be readable without allocating a GiB")
	return relative, compressed
}

func readCleanupSignalObservations(t *testing.T, path string) []cleanupSignalObservation {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "actual wrapper should record its controlled signal attempts")
	var observations []cleanupSignalObservation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var observation cleanupSignalObservation
		require.NoError(t, json.Unmarshal([]byte(line), &observation), "controlled signal attempt should contain only finite fields")
		observations = append(observations, observation)
	}
	return observations
}

func TestReviewPreparationCleanupSignalErrors(t *testing.T) {
	t.Parallel()
	// The compressed native object is immutable. Every parallel case gets a
	// fresh repository and object file; only expensive fixture creation is shared.
	relative, compressed := newCleanupSlowGitObject(t)
	tests := []struct {
		name, mode      string
		cancel          bool
		cleanupFailed   bool
		childReaped     bool
		expectedSignals []cleanupSignalObservation
	}{
		{name: "transient TERM deadline", mode: "term_once", cleanupFailed: true, childReaped: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Denied: true}, {Signal: "KILL"}}},
		{name: "transient TERM cancellation", mode: "term_once", cancel: true, cleanupFailed: true, childReaped: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Denied: true}, {Signal: "KILL"}}},
		{name: "persistent TERM deadline", mode: "term_always", cleanupFailed: true, childReaped: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Denied: true}, {Signal: "KILL"}}},
		{name: "persistent TERM cancellation", mode: "term_always", cancel: true, cleanupFailed: true, childReaped: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Denied: true}, {Signal: "KILL"}}},
		{name: "TERM and KILL denied", mode: "both_always", cancel: true, cleanupFailed: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Denied: true}, {Signal: "KILL", Denied: true}}},
		{name: "lookup race remains benign", mode: "lookup_once", childReaped: true, expectedSignals: []cleanupSignalObservation{{Signal: "TERM", Lookup: true}, {Signal: "KILL"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			object := filepath.Join(f.workDir, ".git", relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(object), 0o700), "case-owned object directory should exist")
			require.NoError(t, os.WriteFile(object, compressed, 0o600), "case should own a valid duplicate of the immutable native object")
			root := t.TempDir()
			pidPath, signalsPath, ackPath := filepath.Join(root, "git.pid"), filepath.Join(root, "signals.jsonl"), filepath.Join(root, "ack")
			sandbox := f.sandbox()
			sandbox.Env = map[string]string{
				"REVIEW_TEST_CLEANUP_MODE": tt.mode, "REVIEW_TEST_CLEANUP_SIGNALS": signalsPath, "REVIEW_TEST_CLEANUP_ACK": ackPath,
			}
			o := &Orchestrator{provider: cleanupSignalFailureProvider{}, logger: zerolog.Nop()}
			// Exec makes the native fsck the wrapper's direct child, so its own
			// wait reaps the exact observed PID without relying on host PID1.
			script := strings.Replace(reviewGitObjectIntegrityScript(f.workDir), "if ! ", "printf '%s' \"$$\" > '"+shellEscapeSingleQuote(pidPath)+"'\nexec ", 1)
			script = strings.TrimSuffix(script, "; then exit 125; fi\n") + "\n"
			deadline := time.Now().Add(2 * time.Second)
			ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline)
			cancel := func() {}
			if tt.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			started := time.Now()
			completed := make(chan error, 1)
			go func() { completed <- o.runBoundedReviewCommand(ctx, sandbox, script, "") }()
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(pidPath)
				if err != nil {
					return false
				}
				_, err = strconv.Atoi(string(data))
				return err == nil
			}, time.Second, time.Millisecond, "fixture must expose its real child within the original preparation budget")
			data, err := os.ReadFile(pidPath)
			require.NoError(t, err, "owned native Git should publish its PID")
			pid, err := strconv.Atoi(string(data))
			require.NoError(t, err, "published native PID should be an integer")
			require.Greater(t, pid, 1, "cleanup fixture must identify an individual owned process")
			group, err := syscall.Getpgid(pid)
			require.NoError(t, err, "native fixture group should be inspectable before cancellation")
			require.Equal(t, pid, group, "exec'd native Git must own the wrapper's captured process group")
			needsCleanup := true
			t.Cleanup(func() {
				if !needsCleanup {
					return
				}
				if err := syscall.Kill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					t.Errorf("could not terminate the owned cleanup regression group: %v", err)
				}
				require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 2*time.Second, time.Millisecond, "red or deliberately denied cleanup cases must leave no owned live process after fixture teardown")
			})
			process, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").CombinedOutput()
			require.NoError(t, err, "owned native process identity should be inspected without argv or environment: %s", process)
			require.Equal(t, "git", filepath.Base(strings.TrimSpace(string(process))), "cleanup signal regression must pause real Git")
			require.NoError(t, syscall.Kill(pid, syscall.SIGSTOP), "the identified real Git process must still exist when paused")
			if tt.cancel {
				cancel()
			}
			select {
			case err = <-completed:
			case <-time.After(7 * time.Second):
				t.Fatal("cleanup signal failure must return within the original deadline plus bounded cleanup")
			}
			terminationErr := syscall.Kill(pid, 0)
			needsCleanup = !errors.Is(terminationErr, syscall.ESRCH)
			observations := readCleanupSignalObservations(t, signalsPath)
			t.Logf("controlled cleanup signal attempts: %+v", observations)
			if tt.cancel {
				require.ErrorIs(t, err, context.Canceled, "cleanup signal failure must retain caller cancellation identity")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded, "cleanup signal failure must retain the original absolute deadline identity")
			}
			require.Less(t, time.Since(started), 7*time.Second, "cleanup signal errors must not renew preparation or cleanup budgets")
			if tt.cleanupFailed {
				require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "unexpected signal failure must remain a terminal cleanup cause even when KILL recovers")
			} else {
				require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "a benign ProcessLookupError followed by successful reap must not become a cleanup failure")
			}
			require.Equal(t, tt.expectedSignals, observations, "cleanup must attempt KILL after TERM failure without restarting or erasing the same cleanup pass")
			if tt.childReaped {
				require.ErrorIs(t, terminationErr, syscall.ESRCH, "the paused real Git must be reaped before preparation returns when KILL is allowed")
			} else {
				require.NoError(t, terminationErr, "denied TERM and KILL must leave an honestly reported owned process for fixture teardown")
			}
			require.Eventually(t, func() bool { _, err := os.Stat(ackPath); return err == nil }, time.Second, time.Millisecond, "wrapper must acknowledge cleanup failure without losing its completion marker")
			ack, err := os.ReadFile(ackPath)
			require.NoError(t, err, "explicit wrapper acknowledgement should be readable")
			wantedAck := "cleaned"
			if tt.cleanupFailed {
				wantedAck = "cleanup_failed"
			}
			require.Equal(t, wantedAck, strings.TrimSpace(string(ack)), "wrapper acknowledgement must distinguish failed cleanup from benign successful reap")
			require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "failed process cleanup must never leave a ready evidence receipt")
		})
	}
}

func TestReviewPreparationCleanupWaitFailuresRemainBounded(t *testing.T) {
	t.Parallel()
	start := strings.Index(reviewWorkspacePreparationPython, "def stop_child():")
	end := strings.Index(reviewWorkspacePreparationPython, "def terminated(")
	require.GreaterOrEqual(t, start, 0, "cleanup helper must be taken from the actual trusted wrapper")
	require.Greater(t, end, start, "cleanup helper extraction must include its terminal failure check")
	helper := reviewWorkspacePreparationPython[start:end]
	tests := []struct {
		name, mode string
		failed     bool
		waits      []float64
	}{
		{name: "first wait OS error still reaches KILL", mode: "wait_error", failed: true, waits: []float64{2, 2.5}},
		{name: "final wait timeout cannot renew cleanup", mode: "wait_timeout", failed: true, waits: []float64{2, 1}},
		{name: "KILL error after leader exit stays terminal", mode: "kill_error", failed: true, waits: []float64{2, 3}},
		{name: "benign lookup followed by reap", mode: "lookup", waits: []float64{3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := `import errno, io, json, os, signal, subprocess, sys
clock = [0.0]
class Clock:
    def monotonic(self):
        return clock[0]
time = Clock()
mode = sys.argv[1]
attempts, waits = [], []
class OwnedChild:
    pid = 4242
    def wait(self, timeout):
        waits.append(timeout)
        if mode == "wait_error" and len(waits) == 1:
            clock[0] = 0.5
            raise OSError(errno.EIO, "synthetic wait failure")
        if mode == "wait_timeout":
            clock[0] += timeout
            raise subprocess.TimeoutExpired("synthetic-owned-child", timeout)
        return 0
active = OwnedChild()
stopping_child = False
cleanup_failed = False
def signal_owned(group, sig):
    if group != 4242:
        raise AssertionError("cleanup changed the owned group identity")
    attempts.append("TERM" if sig == signal.SIGTERM else "KILL")
    if mode == "kill_error" and sig == signal.SIGKILL:
        raise PermissionError(errno.EPERM, "synthetic final signal denial")
    if mode == "lookup" and sig == signal.SIGTERM:
        raise ProcessLookupError(errno.ESRCH, "synthetic lookup race")
os.killpg = signal_owned
` + helper + `
stop_child()
first = {"attempts": list(attempts), "waits": list(waits), "failed": cleanup_failed}
stop_child()
if first != {"attempts": attempts, "waits": waits, "failed": cleanup_failed}:
    raise AssertionError("repeated cleanup renewed or erased its first result")
rejected = False
try:
    require_process_cleanup()
except RuntimeError:
    rejected = True
print(json.dumps({"attempts": attempts, "waits": waits, "failed": cleanup_failed, "rejected": rejected, "active_cleared": active is None, "stopping_cleared": not stopping_child}))
`
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var stderr strings.Builder
			cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", script, tt.mode)
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			require.NoError(t, err, "actual cleanup helper must finish despite controlled wait/signal failures: %s", stderr.String())
			var actual struct {
				Attempts        []string  `json:"attempts"`
				Waits           []float64 `json:"waits"`
				Failed          bool      `json:"failed"`
				Rejected        bool      `json:"rejected"`
				ActiveCleared   bool      `json:"active_cleared"`
				StoppingCleared bool      `json:"stopping_cleared"`
			}
			require.NoError(t, json.Unmarshal(out, &actual), "bounded cleanup results should contain only controlled contract fields")
			require.Equal(t, []string{"TERM", "KILL"}, actual.Attempts, "KILL must follow the first attempt even when TERM wait fails or leader exit is observed")
			require.Equal(t, tt.waits, actual.Waits, "all waits must share the single three-second cleanup allowance")
			require.Equal(t, tt.failed, actual.Failed, "unexpected failures must remain latched across repeated cleanup")
			require.Equal(t, tt.failed, actual.Rejected, "uncertain cleanup must prevent successful preparation")
			require.True(t, actual.ActiveCleared && actual.StoppingCleared, "owned cleanup must finish once without leaving a reentrant cleanup state")
		})
	}
}

type cleanupAcknowledgementProvider struct {
	localReviewEvidenceProvider
	marker, kind, contents string
}

func (p cleanupAcknowledgementProvider) ExecWithStdin(_ context.Context, _ *Sandbox, _ string, stdin io.Reader, _, _ io.Writer) (int, error) {
	if _, err := io.Copy(io.Discard, stdin); err != nil {
		return -1, err
	}
	switch p.kind {
	case "missing":
		return 0, nil
	case "fifo":
		return 0, syscall.Mkfifo(p.marker, 0o600)
	case "symlink":
		if err := os.WriteFile(p.marker+".target", []byte("cleaned\n"), 0o600); err != nil {
			return -1, err
		}
		return 0, os.Symlink(p.marker+".target", p.marker)
	default:
		return 0, os.WriteFile(p.marker, []byte(p.contents), 0o600)
	}
}

func TestReviewPreparationCleanupAcknowledgement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind, contents string
		failed               bool
	}{
		{name: "exact success", contents: "cleaned\n"},
		{name: "explicit failure", contents: "cleanup_failed\n", failed: true},
		{name: "empty", failed: true},
		{name: "unterminated status", contents: "cleaned", failed: true},
		{name: "long forged success prefix", contents: "cleaned\n" + strings.Repeat("\n", 64) + "untrusted-tail", failed: true},
		{name: "missing", kind: "missing", failed: true},
		{name: "FIFO", kind: "fifo", failed: true},
		{name: "symlink", kind: "symlink", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cancelPath := filepath.Join(root, "cancel")
			provider := cleanupAcknowledgementProvider{marker: cancelPath + ".done", kind: tt.kind, contents: tt.contents}
			o := &Orchestrator{provider: provider, logger: zerolog.Nop()}
			started := time.Now()
			err := o.execReviewPreparationInput(context.Background(), &Sandbox{WorkDir: root}, "synthetic completed execution", cancelPath, "", strings.NewReader("{}"), zerolog.Nop())
			if tt.failed {
				require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "even a nominally successful exec must reject an absent, failed, malformed or unsafe cleanup acknowledgement")
			} else {
				require.NoError(t, err, "only an exact finite regular-file success may confirm cleanup")
			}
			require.Less(t, time.Since(started), 2*time.Second, "unsafe completion paths must be rejected without blocking the cleanup allowance")
			if tt.kind == "symlink" {
				data, readErr := os.ReadFile(provider.marker + ".target")
				require.NoError(t, readErr, "symlink rejection must preserve the external target")
				require.Equal(t, "cleaned\n", string(data), "cleanup must not read through or modify the planted completion target")
			}
		})
	}
}
