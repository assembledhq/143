// Package adapters runtime helper. interactive_runtime.go owns the shared
// flow that turns an adapter's runtime profile into a live command handle,
// streams its output through line-oriented parser callbacks, registers the
// handle for cancellation, and tears everything down on exit.
//
// Adapters keep ownership of:
//
//   - prompt construction
//   - CLI argv assembly
//   - per-line parsing
//   - agent-specific result shaping (Diff, Summary, ConfidenceScore, ...)
//
// Everything below — transport selection, cancellation registration, stream
// fanout, exit waiting, resource cleanup — is in this file so adapters never
// hand-roll wrapper scripts or pidfile mechanics.
package adapters

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/assembledhq/143/internal/services/agent"
)

// LineHandler processes a single non-empty line of output from the agent CLI.
// onStdout fires for stdout lines; onStderr fires for stderr lines on
// transports that keep them separated.
type LineHandler func(line []byte)

// InteractiveRunSpec describes the inputs to runInteractiveCommand. It is
// the adapter-facing layer over agent.InteractiveCommandSpec — it carries
// the parser callbacks and a few convenience knobs that keep adapters out
// of the transport business.
type InteractiveRunSpec struct {
	// Cmd is the fully-formed shell command line for the agent CLI. Must
	// not include any pidfile / PTY / interrupt scaffolding — that is the
	// runtime's job.
	Cmd string

	// Profile declares the runtime requirements (TTY, open stdin,
	// cancellation method).
	Profile agent.AgentRuntimeProfile

	// WorkingDir overrides the sandbox WorkDir. Empty means "sandbox default".
	WorkingDir string

	// OnStdout is invoked once per non-empty stdout line.
	OnStdout LineHandler

	// OnStderr is invoked once per non-empty stderr line on split-output
	// transports. Optional — when nil, stderr bytes are buffered and
	// returned via InteractiveRunResult.Stderr so adapters can surface
	// them in error messages.
	OnStderr LineHandler
}

// InteractiveRunResult is the shared outcome of an interactive agent run.
type InteractiveRunResult struct {
	ExitCode int
	// Stderr is the buffered stderr stream when the caller did not pass
	// OnStderr. Bounded only by what the CLI emits — adapters should treat
	// it as best-effort diagnostic context, not raw user input.
	Stderr []byte
}

// runInteractiveCommand starts the agent CLI inside the sandbox via the
// provider's interactive capability, drives stdout/stderr through the
// supplied line handlers, registers the handle with the cancel registry
// (via the InteractiveHandleAttacher in ctx), waits for exit, and tears
// everything down. Adapters get back the exit code and any buffered stderr.
//
// The caller must do NO transport work itself — no pidfile pathing, no
// shell wrapping, no PTY allocation. If the command needs a TTY (e.g. Pi),
// declare it via Profile.RequiresTTY and the runtime will arrange it.
func runInteractiveCommand(ctx context.Context, sandbox *agent.Sandbox, spec InteractiveRunSpec) (InteractiveRunResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return InteractiveRunResult{}, fmt.Errorf("sandbox provider not found in context")
	}
	interactive, ok := provider.(agent.InteractiveSandboxProvider)
	if !ok {
		return InteractiveRunResult{}, fmt.Errorf("sandbox provider %q does not support interactive commands", provider.Name())
	}

	cancelSpec := spec.Profile.Cancellation
	if cancelSpec.Method == "" {
		cancelSpec = agent.DefaultCancellationSpec
	}

	cmdSpec := agent.InteractiveCommandSpec{
		Cmd:              spec.Cmd,
		WorkingDir:       spec.WorkingDir,
		TTY:              spec.Profile.RequiresTTY,
		OpenStdin:        spec.Profile.RequiresOpenStdin,
		SplitOutput:      spec.Profile.PreferSplitOutput,
		CancellationSpec: cancelSpec,
	}

	handle, err := interactive.StartInteractiveCommand(ctx, sandbox, cmdSpec)
	if err != nil {
		return InteractiveRunResult{}, fmt.Errorf("start interactive command: %w", err)
	}

	// Defer order matters here. Defers run LIFO, so on return:
	//
	//   1. handle.Close() — releases the hijacked connection first so the
	//      demux goroutine exits and any in-flight Interrupt/Kill on the
	//      handle becomes a no-op.
	//   2. attacher.Detach() — only after the handle is dead, so the cancel
	//      registry never sees a "detached but still attempting work"
	//      handle.
	//
	// Reordering would let RequestStop call Interrupt on a closed handle
	// briefly, which is safe but noisy in logs.
	defer handle.Close()
	if attacher := agent.InteractiveHandleAttacherFromContext(ctx); attacher != nil {
		attacher.Attach(handle)
		defer attacher.Detach()
	}

	var (
		stderrBuf     bytes.Buffer
		stdoutScanErr error
		stderrScanErr error
		stdoutScanMu  sync.Mutex
		stderrScanMu  sync.Mutex
		wg            sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := streamLines(handle.Stdout(), spec.OnStdout); err != nil {
			stdoutScanMu.Lock()
			stdoutScanErr = err
			stdoutScanMu.Unlock()
		}
	}()

	if !spec.Profile.RequiresTTY {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spec.OnStderr != nil {
				if err := streamLines(handle.Stderr(), spec.OnStderr); err != nil {
					stderrScanMu.Lock()
					stderrScanErr = err
					stderrScanMu.Unlock()
				}
				return
			}
			_, _ = io.Copy(&stderrBuf, handle.Stderr())
		}()
	}

	exitCode, waitErr := handle.Wait(ctx)
	// Wait returns when the connection drops; the streaming goroutines
	// finish draining trailing bytes shortly after. We must wait on them
	// before returning so callers see the full stderr buffer and any
	// scanner error.
	wg.Wait()

	result := InteractiveRunResult{ExitCode: exitCode, Stderr: stderrBuf.Bytes()}
	switch {
	case waitErr != nil:
		return result, waitErr
	case stdoutScanErr != nil:
		return result, fmt.Errorf("read agent stdout: %w", stdoutScanErr)
	case stderrScanErr != nil:
		return result, fmt.Errorf("read agent stderr: %w", stderrScanErr)
	}
	return result, nil
}

// streamLines reads from r and invokes handler for each newline-delimited
// line. Empty lines are skipped. The trailing partial line (no newline) is
// flushed once the reader hits EOF. Returns any scanner error so callers
// can distinguish a clean EOF from a truncated stream (e.g. the 1 MiB line
// ceiling being exceeded by a runaway tool output).
//
// Buffer policy: bufio.Scanner's default 64 KiB ceiling truncates legitimate
// agent output (stream-JSON events with large tool inputs/outputs routinely
// exceed that). 1 MiB matches the previous lineSplitter ceiling used by the
// docker provider and is small enough to bound runaway memory.
func streamLines(r io.Reader, handler LineHandler) error {
	if r == nil {
		return nil
	}
	if handler == nil {
		// Drain so the writer end can EOF.
		_, err := io.Copy(io.Discard, r)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			return err
		}
		return nil
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// scanner reuses its internal buffer; copy before handing off so
		// handlers can retain the slice without aliasing.
		buf := make([]byte, len(line))
		copy(buf, line)
		handler(buf)
	}
	return scanner.Err()
}
