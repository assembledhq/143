package agent

import (
	"context"
	"errors"
	"io"
)

// InteractiveCommandSpec describes how the provider should launch a long-lived
// interactive command on behalf of an adapter. Adapters fill this from their
// AgentRuntimeProfile; they do not encode transport mechanics in the command
// string itself.
type InteractiveCommandSpec struct {
	// Cmd is the shell-level command line to execute inside the sandbox.
	// Adapters must NOT embed pidfile/ttyfile/PTY scaffolding here — that is
	// the provider's job when it honors RequiresTTY/RequiresOpenStdin.
	Cmd string

	// WorkingDir overrides the sandbox WorkDir for this command. Empty means
	// "use sandbox.WorkDir".
	WorkingDir string

	// TTY indicates the command needs a TTY-backed transport. Adapters that
	// rely on keyboard interrupts (e.g. Esc) must request this; otherwise a
	// TTY just adds noise and merges stderr into stdout.
	TTY bool

	// OpenStdin keeps stdin writable after start so the handle can deliver
	// runtime input (Ctrl+C bytes, Esc, future custom input).
	OpenStdin bool

	// SplitOutput requests that stdout and stderr stay logically separated
	// when the transport allows it. Ignored on TTY transports — a real PTY
	// merges them by definition.
	SplitOutput bool

	// CancellationSpec is the adapter's preferred graceful-stop method. The
	// handle's Interrupt(...) decides how to deliver it given the available
	// transport (writing a control byte to stdin, exec'ing a SIGINT helper,
	// etc.). Empty defaults to Ctrl+C.
	CancellationSpec CancellationSpec
}

// InteractiveCommandHandle represents a single running command inside the
// sandbox. It owns the live transport (hijacked Docker connection, future
// remote channel, etc.) and is the only correct place to model cancellation
// and runtime input for that command.
//
// Lifecycle:
//
//  1. Start  → InteractiveSandboxProvider.StartInteractiveCommand returns it
//  2. Stream → callers read Stdout()/Stderr()
//  3. Stop   → Interrupt(ctx, spec) for graceful, Kill(ctx) for force
//  4. Wait   → Wait(ctx) returns the exit code
//  5. Close  → Close() releases transport resources (idempotent)
//
// Concurrency: Wait/Interrupt/Kill/Close may be called from goroutines other
// than the one reading Stdout/Stderr.
type InteractiveCommandHandle interface {
	// ID is a stable provider-level identifier (e.g. Docker exec ID) used
	// for log lines and debugging. It is not a session or sandbox ID.
	ID() string

	// Stdout returns a reader that yields the command's stdout. On TTY
	// transports stderr is merged into the same stream.
	Stdout() io.Reader

	// Stderr returns a reader that yields the command's stderr when
	// SplitOutput was requested and the transport supports it. May return
	// an empty reader otherwise.
	Stderr() io.Reader

	// WriteInput writes raw bytes to the command's stdin. Returns
	// ErrInputNotOpen if the spec did not request OpenStdin.
	WriteInput(ctx context.Context, data []byte) error

	// CloseInput signals EOF on stdin. No-op when stdin was not opened.
	CloseInput(ctx context.Context) error

	// Interrupt delivers the requested graceful-stop semantic. The handle
	// chooses the concrete mechanism (write a control byte to stdin, send
	// SIGINT to a tracked child, etc.) given the transport it allocated at
	// start. Returns ErrUnsupportedInterruptMethod when the requested
	// method cannot be delivered through this handle's transport.
	Interrupt(ctx context.Context, spec CancellationSpec) error

	// Kill force-terminates the underlying transport. Use after Interrupt's
	// grace window expires.
	Kill(ctx context.Context) error

	// Wait blocks until the command exits and returns the exit code. Safe
	// to call multiple times; subsequent calls return the cached exit code.
	Wait(ctx context.Context) (int, error)

	// Close releases provider-side resources held by the handle (hijacked
	// connections, internal goroutines, scratch files). Idempotent.
	Close() error
}

// InteractiveSandboxProvider is the optional capability a SandboxProvider
// implements when it can host long-lived interactive agent commands. Only
// agents whose runtime needs first-class lifecycle control (cancellation,
// stdin, TTY) go through this path; one-shot utilities continue to use
// SandboxProvider.Exec / ExecStream.
type InteractiveSandboxProvider interface {
	StartInteractiveCommand(ctx context.Context, sb *Sandbox, spec InteractiveCommandSpec) (InteractiveCommandHandle, error)
}

// AgentRuntimeProfile describes what kind of runtime an adapter needs from
// the provider. Adapters declare it once; the runtime helper translates it
// into an InteractiveCommandSpec at start time.
type AgentRuntimeProfile struct {
	// Cancellation is the adapter's preferred graceful-stop method.
	Cancellation CancellationSpec

	// RequiresTTY signals that the CLI honors its documented cancel
	// behavior only under a real TTY (e.g. Pi only treats Esc as cancel
	// when raw-mode is engaged).
	RequiresTTY bool

	// RequiresOpenStdin keeps stdin writable while the command runs so the
	// handle can deliver runtime input. Implied by TTY-backed cancellation.
	RequiresOpenStdin bool

	// PreferSplitOutput asks the provider to keep stderr separate when the
	// transport supports it; advisory, not a contract.
	PreferSplitOutput bool
}

// RuntimeProfileProvider is an optional extension on top of AgentAdapter.
// Adapters implement it to declare their interactive runtime requirements.
type RuntimeProfileProvider interface {
	RuntimeProfile() AgentRuntimeProfile
}

// ResolveRuntimeProfile returns the adapter's declared runtime profile,
// falling back to a Ctrl+C, no-TTY, stderr-split default for adapters that
// do not implement RuntimeProfileProvider.
func ResolveRuntimeProfile(adapter AgentAdapter) AgentRuntimeProfile {
	if p, ok := adapter.(RuntimeProfileProvider); ok {
		profile := p.RuntimeProfile()
		if profile.Cancellation.Method == "" {
			profile.Cancellation = DefaultCancellationSpec
		}
		return profile
	}
	return AgentRuntimeProfile{
		Cancellation:      DefaultCancellationSpec,
		PreferSplitOutput: true,
	}
}

// ErrInputNotOpen indicates a WriteInput/CloseInput call was made on a
// handle whose spec did not request OpenStdin.
var ErrInputNotOpen = errors.New("interactive handle: stdin not open")

// interactiveHandleAttacherKey is the context key for the handle attacher
// the orchestrator installs before invoking adapter.Execute.
type interactiveHandleAttacherKey struct{}

// InteractiveHandleAttacher binds an interactive command handle to an
// out-of-band cancellation owner (typically the CancelRegistry entry for
// the running session). The runtime helper calls Attach as soon as the
// handle starts, and Detach when the handle closes; no-op when the
// orchestrator did not install an attacher.
type InteractiveHandleAttacher interface {
	Attach(handle InteractiveCommandHandle)
	Detach()
}

// WithInteractiveHandleAttacher installs an attacher into the context.
func WithInteractiveHandleAttacher(ctx context.Context, attacher InteractiveHandleAttacher) context.Context {
	if attacher == nil {
		return ctx
	}
	return context.WithValue(ctx, interactiveHandleAttacherKey{}, attacher)
}

// InteractiveHandleAttacherFromContext retrieves the attacher, returning
// nil when none was installed (e.g. unit tests that don't exercise cancel).
func InteractiveHandleAttacherFromContext(ctx context.Context) InteractiveHandleAttacher {
	a, _ := ctx.Value(interactiveHandleAttacherKey{}).(InteractiveHandleAttacher)
	return a
}
