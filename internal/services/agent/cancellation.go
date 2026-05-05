package agent

import (
	"context"
	"errors"
	"fmt"
)

// CancellationMethod identifies how a coding agent expects a graceful stop.
type CancellationMethod string

const (
	CancellationMethodCtrlC  CancellationMethod = "ctrl_c"
	CancellationMethodEscape CancellationMethod = "escape"
)

// CancellationSpec describes the preferred graceful-stop method for an agent.
type CancellationSpec struct {
	Method CancellationMethod
}

// DefaultCancellationSpec is used when an adapter does not specify a custom
// cancellation method.
var DefaultCancellationSpec = CancellationSpec{Method: CancellationMethodCtrlC}

// CancellationSpecProvider is an optional extension on top of AgentAdapter.
// Adapters can implement it to override the default Ctrl+C cancellation path.
type CancellationSpecProvider interface {
	CancellationSpec() CancellationSpec
}

// ResolveCancellationSpec returns the adapter's preferred cancellation method,
// defaulting to Ctrl+C when the adapter does not declare one.
func ResolveCancellationSpec(adapter AgentAdapter) CancellationSpec {
	if provider, ok := adapter.(CancellationSpecProvider); ok {
		spec := provider.CancellationSpec()
		if spec.Method != "" {
			return spec
		}
	}
	return DefaultCancellationSpec
}

// InterruptRequest describes a provider-level graceful interrupt request.
type InterruptRequest struct {
	Method      CancellationMethod
	PIDFilePath string
	TTYFilePath string
}

// ErrUnsupportedInterruptMethod indicates the provider cannot deliver the
// requested graceful interrupt method.
var ErrUnsupportedInterruptMethod = errors.New("unsupported interrupt method")

// SandboxInterruptor is an optional provider capability for agent-aware
// graceful interrupt delivery. Providers that do not implement it will fall
// back to the legacy Ctrl+C shell command path when possible.
type SandboxInterruptor interface {
	Interrupt(ctx context.Context, sb *Sandbox, req InterruptRequest) error
}

// BuildCtrlCInterruptCommand returns the shell command used to deliver a
// Ctrl+C/SIGINT interrupt to the tracked agent pid.
func BuildCtrlCInterruptCommand(pidFilePath string) string {
	return fmt.Sprintf(
		"if [ -s '%s' ]; then kill -INT \"$(cat '%s')\" 2>/dev/null || true; else exit 1; fi",
		pidFilePath,
		pidFilePath,
	)
}

// BuildEscapeInterruptCommand returns the shell command used to write an ESC
// keystroke into the tracked TTY for agents that expect keyboard interrupts.
func BuildEscapeInterruptCommand(ttyFilePath string) string {
	return fmt.Sprintf(
		"if [ -s '%s' ]; then tty_path=$(cat '%s'); if [ -n \"$tty_path\" ]; then printf '\\033' > \"$tty_path\"; else exit 1; fi; else exit 1; fi",
		ttyFilePath,
		ttyFilePath,
	)
}
