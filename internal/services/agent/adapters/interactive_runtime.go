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

	// Bind the handle to the session-scoped cancel entry. The orchestrator
	// installs the attacher before adapter.Execute so RequestStop can deliver
	// graceful interrupts through the live handle. No-op when an adapter is
	// driven outside the orchestrator (most unit tests).
	if attacher := agent.InteractiveHandleAttacherFromContext(ctx); attacher != nil {
		attacher.Attach(handle)
		defer attacher.Detach()
	}
	defer handle.Close()

	var stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		streamLines(handle.Stdout(), spec.OnStdout)
	}()

	if !spec.Profile.RequiresTTY {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spec.OnStderr != nil {
				streamLines(handle.Stderr(), spec.OnStderr)
				return
			}
			_, _ = io.Copy(&stderrBuf, handle.Stderr())
		}()
	}

	exitCode, waitErr := handle.Wait(ctx)
	// Reading from the pipes is what unblocks Wait when the connection
	// drops; the goroutines above are still draining trailing bytes after
	// Wait returns. Closing the handle will close the pipes.
	wg.Wait()

	if waitErr != nil {
		return InteractiveRunResult{ExitCode: exitCode, Stderr: stderrBuf.Bytes()}, waitErr
	}
	return InteractiveRunResult{ExitCode: exitCode, Stderr: stderrBuf.Bytes()}, nil
}

// streamLines reads from r and invokes handler for each newline-delimited
// line. Empty lines are skipped. The trailing partial line (no newline) is
// flushed once the reader hits EOF.
//
// Buffer policy: bufio.Scanner's default 64 KiB ceiling truncates legitimate
// agent output (stream-JSON events with large tool inputs/outputs routinely
// exceed that). 1 MiB matches the previous lineSplitter ceiling used by the
// docker provider and is small enough to bound runaway memory.
func streamLines(r io.Reader, handler LineHandler) {
	if r == nil || handler == nil {
		// Drain so the writer end can EOF.
		if r != nil {
			_, _ = io.Copy(io.Discard, r)
		}
		return
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
}
