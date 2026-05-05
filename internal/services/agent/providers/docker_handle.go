package providers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/assembledhq/143/internal/services/agent"
)

// runtimePIDFilePrefix is the in-container filename prefix for the internal
// signal-delivery shim. Each handle gets its own random suffix so multi-turn
// sessions never read a stale PID from a previous turn whose Close() failed
// to clean up; PIDs are reused by the kernel and signaling the wrong process
// would be a footgun. The shim is *not* exposed to adapters — adapters
// interact with the handle, not the filesystem.
const runtimePIDFilePrefix = ".143-runtime-"

// pidFileWriteTimeout bounds how long Interrupt waits for the in-container
// shim to populate the pidfile before declaring delivery a failure. The shim
// writes it synchronously immediately after fork, so the wait should normally
// be a few milliseconds at most.
const pidFileWriteTimeout = 5 * time.Second

// pidFilePollInterval is the cadence at which Interrupt re-reads the pidfile
// while waiting for the shim to record the child PID.
const pidFilePollInterval = 50 * time.Millisecond

// closeCleanupTimeout caps how long Close() will wait on best-effort
// in-container cleanup commands (pidfile rm). Keeps Close from hanging when
// the daemon is unresponsive at session teardown.
const closeCleanupTimeout = 2 * time.Second

// dockerInteractiveHandle is the Docker-backed InteractiveCommandHandle. It
// owns the hijacked exec connection, the demux goroutine, optional stdin
// access, and any internal scaffolding needed to deliver the requested
// graceful-stop semantics through the available transport.
type dockerInteractiveHandle struct {
	provider *DockerProvider
	sandbox  *agent.Sandbox
	execID   string
	spec     agent.InteractiveCommandSpec
	pidFile  string // empty unless the internal SIGINT shim is active
	hasShim  bool   // command was wrapped with the pidfile shim

	// Output streams. stdoutPipe is always populated; stderrPipe is only
	// populated when SplitOutput was honored (non-TTY).
	stdoutPipe io.ReadCloser
	stderrPipe io.ReadCloser

	// Stdin. When OpenStdin is requested the hijacked connection's writer
	// half is exposed via WriteInput / CloseInput.
	stdinW    io.WriteCloser
	stdinOnce sync.Once

	// Lifecycle.
	conn      io.Closer // hijacked connection (close releases stdin/stdout)
	connClose sync.Once
	waitDone  chan struct{}
	waitErr   error

	exitMu      sync.Mutex
	exitCode    int
	exitChecked bool
}

// StartInteractiveCommand spins up a long-lived command inside the sandbox
// and returns a handle that owns its lifecycle. Adapters should call this for
// agent turns; one-shot utilities (git, tar, prompt-file writes) continue to
// use Exec / ExecStream.
func (d *DockerProvider) StartInteractiveCommand(ctx context.Context, sb *agent.Sandbox, spec agent.InteractiveCommandSpec) (agent.InteractiveCommandHandle, error) {
	if spec.Cmd == "" {
		return nil, errors.New("interactive command spec missing Cmd")
	}

	// Resolve transport requirements. Cancellation method drives a few
	// transport defaults: Esc requires TTY+stdin (the agent only honors a
	// raw 0x1b under raw-mode); Ctrl+C without a TTY needs the in-container
	// SIGINT shim because Docker exec has no first-class
	// "send signal to exec process" API.
	wantTTY := spec.TTY
	wantStdin := spec.OpenStdin
	if spec.CancellationSpec.Method == agent.CancellationMethodEscape {
		wantTTY = true
		wantStdin = true
	}
	if spec.CancellationSpec.Method == "" {
		spec.CancellationSpec = agent.DefaultCancellationSpec
	}

	cmd := spec.Cmd
	pidFile := ""
	hasShim := false
	// Without a TTY there is no stdin-byte path to deliver SIGINT (writing
	// 0x03 to a non-TTY stdin reaches the shell as a literal byte, not a
	// signal). Wrap the command in a tiny shell shim that records the
	// child PID under HomeDir so Interrupt(ctrl_c) can exec-send SIGINT
	// and Kill() can exec-send SIGKILL. The wrapping is provider-internal
	// and never seen by the adapter.
	//
	// We do NOT wrap on TTY transports because backgrounding the agent
	// inside a subshell (`(cmd) & wait`) detaches it from the TTY's
	// foreground process group, which breaks Pi's raw-byte input model.
	// TTY handles get force-stopped via stdin-byte delivery + connection
	// close instead; see Kill().
	if !wantTTY && spec.CancellationSpec.Method == agent.CancellationMethodCtrlC {
		homeDir := sb.HomeDir
		if homeDir == "" {
			homeDir = "/tmp"
		}
		suffix, err := newPIDFileSuffix()
		if err != nil {
			return nil, fmt.Errorf("generate runtime pidfile name: %w", err)
		}
		pidFile = fmt.Sprintf("%s/%s%s.pid", homeDir, runtimePIDFilePrefix, suffix)
		cmd = wrapWithSignalShim(cmd, pidFile)
		hasShim = true
	}

	workingDir := spec.WorkingDir
	if workingDir == "" {
		workingDir = sb.WorkDir
	}

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  wantStdin,
		Tty:          wantTTY,
		WorkingDir:   workingDir,
	}

	execResp, err := d.client.ContainerExecCreate(ctx, sb.ID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("create interactive exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: wantTTY})
	if err != nil {
		// The exec is created but not attached. Docker auto-reaps unattached
		// execs eventually; we don't have a public API to tear it down from
		// here. Surface the error and let the caller retry or destroy the
		// sandbox.
		return nil, fmt.Errorf("attach interactive exec: %w", err)
	}

	h := &dockerInteractiveHandle{
		provider: d,
		sandbox:  sb,
		execID:   execResp.ID,
		spec:     spec,
		pidFile:  pidFile,
		hasShim:  hasShim,
		conn:     attachResp.Conn,
		waitDone: make(chan struct{}),
	}

	// Wire up output. On TTY transports stdout and stderr share the same
	// stream by definition; SplitOutput is ignored.
	if wantTTY {
		stdoutR, stdoutW := io.Pipe()
		h.stdoutPipe = stdoutR
		h.stderrPipe = io.NopCloser(strings.NewReader(""))
		go h.runTTYCopy(attachResp.Reader, stdoutW)
	} else {
		stdoutR, stdoutW := io.Pipe()
		stderrR, stderrW := io.Pipe()
		h.stdoutPipe = stdoutR
		h.stderrPipe = stderrR
		go h.runStdCopy(attachResp.Reader, stdoutW, stderrW)
	}

	if wantStdin {
		h.stdinW = attachResp.Conn
	}

	return h, nil
}

// newPIDFileSuffix returns a short hex suffix unique enough that two handles
// in the same sandbox never collide. crypto/rand is used so the suffix is
// also resistant to a hostile process inside the sandbox guessing the path.
func newPIDFileSuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// wrapWithSignalShim wraps the user command so its PID is recorded under
// pidFile. The shim writes the pidfile synchronously before waiting on the
// child, so a fast-arriving Interrupt always finds a PID to signal. It exits
// with the child's exit code so the wrapping is invisible to callers.
func wrapWithSignalShim(cmd, pidFile string) string {
	// Only the pidfile path is escaped — the user command is intentionally
	// passed through verbatim because it already went through adapter-level
	// argv assembly and we want it to execute with identical shell semantics
	// to running it bare.
	return fmt.Sprintf("(%s) & __143_pid=$!; printf '%%s\\n' \"$__143_pid\" > '%s'; wait \"$__143_pid\"",
		cmd, escapeSingleQuotes(pidFile))
}

func escapeSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

func (h *dockerInteractiveHandle) ID() string { return h.execID }

func (h *dockerInteractiveHandle) Stdout() io.Reader { return h.stdoutPipe }
func (h *dockerInteractiveHandle) Stderr() io.Reader { return h.stderrPipe }

// runStdCopy demuxes the docker stdcopy stream into separate stdout/stderr
// pipes and signals exit completion. Closing the writer ends of the pipes
// is what unblocks readers when the command exits or the connection drops.
func (h *dockerInteractiveHandle) runStdCopy(src io.Reader, stdoutW, stderrW io.WriteCloser) {
	defer close(h.waitDone)
	defer stdoutW.Close()
	defer stderrW.Close()
	if _, err := stdcopy.StdCopy(stdoutW, stderrW, src); err != nil && !isClosedConnErr(err) {
		h.waitErr = fmt.Errorf("interactive exec stream: %w", err)
	}
}

// runTTYCopy bridges the raw TTY stream to the stdout pipe. Docker does not
// multiplex over a TTY connection, so stdcopy would corrupt the stream.
func (h *dockerInteractiveHandle) runTTYCopy(src io.Reader, stdoutW io.WriteCloser) {
	defer close(h.waitDone)
	defer stdoutW.Close()
	if _, err := io.Copy(stdoutW, src); err != nil && !isClosedConnErr(err) {
		h.waitErr = fmt.Errorf("interactive tty stream: %w", err)
	}
}

// isClosedConnErr reports whether err signals "the transport is gone, this
// is the expected end-of-stream" rather than a true I/O failure.
func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed)
}

// WriteInput sends raw bytes to the command's stdin. Returns ErrInputNotOpen
// when the spec did not request OpenStdin.
//
// Note: OpenStdin alone does not enable Ctrl+C delivery; the underlying TTY
// line discipline only converts 0x03 into SIGINT when a real TTY was
// allocated. Callers that want byte-level cancellation should set both TTY
// and OpenStdin in the spec.
func (h *dockerInteractiveHandle) WriteInput(ctx context.Context, data []byte) error {
	if h.stdinW == nil {
		return agent.ErrInputNotOpen
	}
	if len(data) == 0 {
		return nil
	}
	type writeResult struct{ err error }
	resCh := make(chan writeResult, 1)
	go func() {
		_, err := h.stdinW.Write(data)
		resCh <- writeResult{err: err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-resCh:
		if r.err != nil {
			return fmt.Errorf("write interactive stdin: %w", r.err)
		}
		return nil
	}
}

// CloseInput signals EOF on stdin. Idempotent.
func (h *dockerInteractiveHandle) CloseInput(ctx context.Context) error {
	if h.stdinW == nil {
		return nil
	}
	var err error
	h.stdinOnce.Do(func() {
		// The hijacked connection exposes CloseWrite via net.Conn-style
		// types when supported; falling back to Close would tear down
		// stdout too, which is too aggressive. CloseInput is best-effort —
		// Kill / Close handle full transport teardown.
		type closeWriter interface{ CloseWrite() error }
		if cw, ok := h.stdinW.(closeWriter); ok {
			err = cw.CloseWrite()
		}
	})
	return err
}

// Interrupt delivers the requested graceful-stop method. The handle decides
// the concrete mechanism based on the transport it allocated:
//
//   - escape on TTY+stdin: write 0x1b to stdin
//   - ctrl_c on TTY+stdin: write 0x03 to stdin (TTY line discipline converts
//     to SIGINT for the foreground process)
//   - ctrl_c without TTY:  exec-send SIGINT to the pidfile-tracked child
//   - everything else:     ErrUnsupportedInterruptMethod
//
// This is the entire surface adapters need; the wrapper / pidfile mechanics
// stay below this boundary.
func (h *dockerInteractiveHandle) Interrupt(ctx context.Context, spec agent.CancellationSpec) error {
	method := spec.Method
	if method == "" {
		method = h.spec.CancellationSpec.Method
	}
	if method == "" {
		method = agent.CancellationMethodCtrlC
	}

	switch method {
	case agent.CancellationMethodEscape:
		if h.stdinW == nil {
			return agent.ErrUnsupportedInterruptMethod
		}
		return h.WriteInput(ctx, []byte{0x1b})

	case agent.CancellationMethodCtrlC:
		if h.stdinW != nil && h.spec.TTY {
			return h.WriteInput(ctx, []byte{0x03})
		}
		if h.hasShim {
			return h.signalTrackedChild(ctx, "INT")
		}
		return agent.ErrUnsupportedInterruptMethod

	default:
		return agent.ErrUnsupportedInterruptMethod
	}
}

// signalTrackedChild waits for the in-container shim to populate the pidfile
// and then exec-sends the requested signal exactly once. Used for both
// Interrupt(ctrl_c) (signal=INT) and Kill() (signal=KILL) on shim transports.
func (h *dockerInteractiveHandle) signalTrackedChild(ctx context.Context, signal string) error {
	if h.pidFile == "" {
		return agent.ErrUnsupportedInterruptMethod
	}
	deadline, cancel := context.WithTimeout(ctx, pidFileWriteTimeout)
	defer cancel()
	if err := h.waitForPIDFile(deadline); err != nil {
		return err
	}
	return h.execSignal(deadline, signal)
}

// waitForPIDFile blocks until the in-container pidfile exists and is
// non-empty. The shim writes the pidfile immediately after fork so this
// loop is purely defensive against a fast-arriving Interrupt that races
// the shim's first few syscalls.
func (h *dockerInteractiveHandle) waitForPIDFile(ctx context.Context) error {
	check := fmt.Sprintf("test -s '%s'", escapeSingleQuotes(h.pidFile))
	for {
		exitCode, err := h.provider.Exec(ctx, h.sandbox, check, io.Discard, io.Discard)
		if err == nil && exitCode == 0 {
			return nil
		}
		// On any unexpected exec error other than ctx expiry, surface it
		// immediately — there's no point in spinning if Docker itself is
		// broken.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("poll runtime pidfile: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("runtime pidfile not populated within %s: %w", pidFileWriteTimeout, ctx.Err())
		case <-time.After(pidFilePollInterval):
		}
	}
}

// execSignal sends `kill -<signal>` to the recorded PID exactly once.
// kill exiting non-zero (PID already gone) is treated as success — the
// child is dead either way.
func (h *dockerInteractiveHandle) execSignal(ctx context.Context, signal string) error {
	cmd := fmt.Sprintf("kill -%s \"$(cat '%s')\" 2>/dev/null || true",
		signal, escapeSingleQuotes(h.pidFile))
	if _, err := h.provider.Exec(ctx, h.sandbox, cmd, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("send sig%s to tracked child: %w", strings.ToLower(signal), err)
	}
	return nil
}

// Kill force-terminates the agent process. Unlike Interrupt, Kill is not
// expected to give the agent a chance to clean up — it is the escalation
// step the cancel registry uses after Interrupt's grace window expires.
//
// Mechanism by transport:
//
//   - shim handle (no TTY): exec-send SIGKILL to the pidfile-tracked child,
//     then close the connection.
//   - TTY+stdin handle:     write 0x03 to stdin so the TTY line discipline
//     delivers SIGINT, close stdin (EOF), then close the connection. We do
//     not have a tracked PID for TTY handles (wrapping breaks the foreground
//     process group), so byte-level interrupt + transport teardown is the
//     best we can do.
//   - everything else:      close the connection.
//
// Closing the hijacked connection unblocks the demux goroutine and lets
// Wait return. The container itself stays alive — only the exec process
// is torn down.
func (h *dockerInteractiveHandle) Kill(ctx context.Context) error {
	var killErr error
	switch {
	case h.hasShim:
		if err := h.signalTrackedChild(ctx, "KILL"); err != nil && !errors.Is(err, agent.ErrUnsupportedInterruptMethod) {
			killErr = err
		}
	case h.stdinW != nil && h.spec.TTY:
		// Best-effort byte-level interrupt; any error is non-fatal because
		// we are about to drop the connection anyway.
		_ = h.WriteInput(ctx, []byte{0x03})
		_ = h.CloseInput(ctx)
	}
	if err := h.Close(); err != nil && killErr == nil {
		// Close is currently always nil but keep the contract honest if a
		// future implementation adds real teardown errors.
		killErr = err
	}
	return killErr
}

// Wait blocks until the command exits and returns the exit code.
func (h *dockerInteractiveHandle) Wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-h.waitDone:
	}
	if h.waitErr != nil {
		return -1, h.waitErr
	}
	h.exitMu.Lock()
	defer h.exitMu.Unlock()
	if h.exitChecked {
		return h.exitCode, nil
	}
	inspect, err := h.provider.client.ContainerExecInspect(ctx, h.execID)
	if err != nil {
		return -1, fmt.Errorf("inspect interactive exec: %w", err)
	}
	h.exitCode = inspect.ExitCode
	h.exitChecked = true
	return h.exitCode, nil
}

// Close releases the hijacked connection and any pidfile scratch state.
// Idempotent — subsequent calls are no-ops.
func (h *dockerInteractiveHandle) Close() error {
	h.connClose.Do(func() {
		if h.conn != nil {
			_ = h.conn.Close()
		}
		// Best-effort pidfile cleanup, bounded so a hung daemon at session
		// teardown can't block Close indefinitely. Failure is fine — the
		// pidfile lives under the sandbox user's home and the next handle
		// gets a unique suffix anyway.
		if h.hasShim && h.pidFile != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), closeCleanupTimeout)
			defer cancel()
			_ = h.removePidFile(cleanupCtx)
		}
	})
	return nil
}

func (h *dockerInteractiveHandle) removePidFile(ctx context.Context) error {
	cmd := fmt.Sprintf("rm -f '%s'", escapeSingleQuotes(h.pidFile))
	_, err := h.provider.Exec(ctx, h.sandbox, cmd, io.Discard, io.Discard)
	return err
}

// Compile-time assertion that the handle implements the public interface.
var _ agent.InteractiveCommandHandle = (*dockerInteractiveHandle)(nil)
