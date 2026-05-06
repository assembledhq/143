package agent

import "errors"

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
//
// Deprecated: use RuntimeProfileProvider instead — it carries the same
// information alongside transport requirements (TTY, open stdin) so
// cancellation is expressed once per adapter. Both are honored for now —
// RuntimeProfileProvider wins when both are implemented — but no production
// adapter still implements this interface, and it will be removed once the
// cancellation_test.go fixtures are migrated.
type CancellationSpecProvider interface {
	CancellationSpec() CancellationSpec
}

// ResolveCancellationSpec returns the adapter's preferred cancellation
// method, defaulting to Ctrl+C when the adapter does not declare one.
// Honors RuntimeProfileProvider first so adapters can express cancellation
// once via their full runtime profile.
func ResolveCancellationSpec(adapter AgentAdapter) CancellationSpec {
	if p, ok := adapter.(RuntimeProfileProvider); ok {
		spec := p.RuntimeProfile().Cancellation
		if spec.Method != "" {
			return spec
		}
	}
	if provider, ok := adapter.(CancellationSpecProvider); ok {
		spec := provider.CancellationSpec()
		if spec.Method != "" {
			return spec
		}
	}
	return DefaultCancellationSpec
}

// ErrUnsupportedInterruptMethod indicates the handle/provider cannot deliver
// the requested graceful interrupt method through its current transport.
var ErrUnsupportedInterruptMethod = errors.New("unsupported interrupt method")
