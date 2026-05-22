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

const maxStreamLineBytes = 16 * 1024 * 1024

type streamLineTooLongError struct {
	LineBytes int
	MaxBytes  int
}

func (e *streamLineTooLongError) Error() string {
	return fmt.Sprintf("agent output line exceeded retained limit: line_bytes=%d max_bytes=%d", e.LineBytes, e.MaxBytes)
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

	var closeHandle sync.Once
	closeHandleFunc := func() {
		closeHandle.Do(func() {
			_ = handle.Close()
		})
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
	if attacher := agent.InteractiveHandleAttacherFromContext(ctx); attacher != nil {
		attacher.Attach(handle)
		defer attacher.Detach()
	}
	defer closeHandleFunc()

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
	if waitErr != nil {
		closeHandleFunc()
	}
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
// flushed once the reader hits EOF.
//
// Some agent CLIs emit one JSONL event per tool completion, with the entire
// command output embedded in that one line. Those lines can be many megabytes
// long, so this must not use bufio.Scanner: Scanner has a fixed token ceiling
// and stops reading once a line exceeds it, which can backpressure the Docker
// exec stream and wedge the agent. ReadSlice lets us keep draining fixed-size
// chunks while only assembling the current logical line. Lines above
// maxStreamLineBytes are drained but not retained, and a clear error is
// returned after the stream reaches EOF.
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

	reader := bufio.NewReaderSize(r, 64*1024)
	var line bytes.Buffer
	lineBytes := 0
	discardingLine := false
	var retainedErr error
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			content := fragment
			if err == nil {
				content = trimLineEnding(content)
			}
			lineBytes += len(content)
			if !discardingLine {
				if line.Len()+len(content) <= maxStreamLineBytes {
					if _, writeErr := line.Write(content); writeErr != nil {
						return writeErr
					}
				} else {
					discardingLine = true
					line = bytes.Buffer{}
				}
			}
		}
		switch {
		case err == nil:
			if lineErr := finishStreamLine(&line, handler, lineBytes, discardingLine); lineErr != nil && retainedErr == nil {
				retainedErr = lineErr
			}
			lineBytes = 0
			discardingLine = false
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if lineBytes > 0 || line.Len() > 0 {
				if lineErr := finishStreamLine(&line, handler, lineBytes, discardingLine); lineErr != nil && retainedErr == nil {
					retainedErr = lineErr
				}
			}
			return retainedErr
		default:
			return err
		}
	}
}

func finishStreamLine(line *bytes.Buffer, handler LineHandler, lineBytes int, discarding bool) error {
	if discarding {
		*line = bytes.Buffer{}
		return &streamLineTooLongError{LineBytes: lineBytes, MaxBytes: maxStreamLineBytes}
	}
	raw := line.Bytes()
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	defer func() { *line = bytes.Buffer{} }()
	if len(raw) == 0 {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	buf := make([]byte, len(raw))
	copy(buf, raw)
	handler(buf)
	return nil
}

func trimLineEnding(fragment []byte) []byte {
	fragment = bytes.TrimSuffix(fragment, []byte("\n"))
	fragment = bytes.TrimSuffix(fragment, []byte("\r"))
	return fragment
}
