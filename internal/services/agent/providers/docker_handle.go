package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/assembledhq/143/internal/services/agent"
)

// dockerHandlePIDFileName is the in-container path used by the internal
// signal-delivery shim. It is *not* exposed to adapters — adapters interact
// with the handle, not the filesystem. The shim only runs when the spec
// requests Ctrl+C without a TTY (i.e. no stdin-byte path is available); for
// TTY transports we deliver control bytes through stdin directly.
const dockerHandlePIDFileName = ".143-runtime.pid"

// pidFileWriteTimeout bounds how long Interrupt waits for the in-container
// shim to populate the pidfile before declaring delivery a failure. The shim
// writes it synchronously immediately after fork, so any wait beyond a couple
// hundred milliseconds means the wrapper itself never started.
const pidFileWriteTimeout = 5 * time.Second

// pidFilePollInterval is the cadence at which Interrupt re-reads the pidfile
// while waiting for the shim to record the child PID.
const pidFilePollInterval = 50 * time.Millisecond

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
	stdoutEOF  io.WriteCloser // writer end held internally; closed when copy goroutine exits
	stderrEOF  io.WriteCloser

	// Stdin. When OpenStdin is requested the hijacked connection's writer
	// half is exposed via WriteInput / CloseInput.
	stdinW    io.WriteCloser
	stdinOnce sync.Once

	// Lifecycle.
	conn        io.Closer // hijacked connection (close releases stdin/stdout)
	connClose   sync.Once
	waitDone    chan struct{}
	waitErr     error
	exitCode    int
	exitChecked bool
	exitMu      sync.Mutex
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
	// child PID under HomeDir; Interrupt(ctrl_c) reads the pidfile and
	// exec-sends SIGINT. The wrapping is provider-internal and never seen
	// by the adapter.
	if !wantTTY && spec.CancellationSpec.Method == agent.CancellationMethodCtrlC {
		homeDir := sb.HomeDir
		if homeDir == "" {
			homeDir = "/tmp"
		}
		pidFile = fmt.Sprintf("%s/%s", homeDir, dockerHandlePIDFileName)
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
		h.stdoutEOF = stdoutW
		h.stderrPipe = io.NopCloser(strings.NewReader(""))
		go h.runTTYCopy(attachResp.Reader, stdoutW)
	} else {
		stdoutR, stdoutW := io.Pipe()
		stderrR, stderrW := io.Pipe()
		h.stdoutPipe = stdoutR
		h.stderrPipe = stderrR
		h.stdoutEOF = stdoutW
		h.stderrEOF = stderrW
		go h.runStdCopy(attachResp.Reader, stdoutW, stderrW)
	}

	if wantStdin {
		h.stdinW = attachResp.Conn
	}

	return h, nil
}

// wrapWithSignalShim wraps the user command so its PID is recorded under
// pidFile. The shim exits with the wrapped command's exit code and writes
// the pidfile synchronously before waiting on the child, so a fast-arriving
// Interrupt can always find a PID to kill.
func wrapWithSignalShim(cmd, pidFile string) string {
	// The single-quote escape only covers the pidfile path. The user
	// command is left untouched: it already passed adapter-level escaping
	// and we want it to execute with the same shell semantics it would
	// have without the shim.
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

func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection reset")
}

// WriteInput sends raw bytes to the command's stdin. Returns ErrInputNotOpen
// when the spec did not request OpenStdin.
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
		// Hijacked Docker connections expose CloseWrite via the
		// connection's underlying type when available; falling back to
		// Close terminates the entire connection which is too aggressive.
		// io.WriteCloser.Close on attachResp.Conn would tear down stdout
		// too, so we treat CloseInput as best-effort and rely on Kill /
		// Close for full teardown.
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
//   - ctrl_c on TTY+stdin: write 0x03 to stdin
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
			return h.killTrackedChild(ctx)
		}
		return agent.ErrUnsupportedInterruptMethod

	default:
		return agent.ErrUnsupportedInterruptMethod
	}
}

// killTrackedChild reads the in-container pidfile and exec-sends SIGINT to
// the recorded PID. Used only on transports without a stdin-byte path.
func (h *dockerInteractiveHandle) killTrackedChild(ctx context.Context) error {
	if h.pidFile == "" {
		return agent.ErrUnsupportedInterruptMethod
	}
	deadline, cancel := context.WithTimeout(ctx, pidFileWriteTimeout)
	defer cancel()
	cmd := fmt.Sprintf("if [ -s '%s' ]; then kill -INT \"$(cat '%s')\" 2>/dev/null || true; else exit 1; fi",
		escapeSingleQuotes(h.pidFile), escapeSingleQuotes(h.pidFile))
	for {
		exitCode, err := h.provider.Exec(deadline, h.sandbox, cmd, io.Discard, io.Discard)
		if err == nil && exitCode == 0 {
			return nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("send sigint to tracked child: %w", err)
		}
		select {
		case <-deadline.Done():
			if err != nil {
				return fmt.Errorf("send sigint to tracked child: %w", err)
			}
			return fmt.Errorf("interrupt command exited with code %d before pidfile populated", exitCode)
		case <-time.After(pidFilePollInterval):
		}
	}
}

// Kill force-stops the underlying transport. It does not attempt a graceful
// SIGINT — callers escalate to Kill only after Interrupt's grace window
// expires.
func (h *dockerInteractiveHandle) Kill(ctx context.Context) error {
	// Closing the hijacked connection unblocks the demux goroutine and
	// causes Wait to return. The container itself stays alive — only the
	// exec process is torn down.
	return h.Close()
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
// Idempotent.
func (h *dockerInteractiveHandle) Close() error {
	h.connClose.Do(func() {
		if h.conn != nil {
			_ = h.conn.Close()
		}
	})
	if h.hasShim && h.pidFile != "" {
		// Best-effort cleanup. Failure is fine — the pidfile lives under
		// the sandbox user's home and is overwritten on the next turn.
		_ = h.removePidFile(context.Background())
	}
	return nil
}

func (h *dockerInteractiveHandle) removePidFile(ctx context.Context) error {
	cmd := fmt.Sprintf("rm -f '%s'", escapeSingleQuotes(h.pidFile))
	_, err := h.provider.Exec(ctx, h.sandbox, cmd, io.Discard, io.Discard)
	return err
}

// Compile-time assertion that the handle implements the public interface.
var _ agent.InteractiveCommandHandle = (*dockerInteractiveHandle)(nil)
