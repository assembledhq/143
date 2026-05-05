package adapters

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

// recordingAttacher captures the Attach/Detach calls runInteractiveCommand
// makes against the InteractiveHandleAttacher installed in the context.
type recordingAttacher struct {
	attached atomic.Int32
	detached atomic.Int32
	last     atomic.Pointer[agent.InteractiveCommandHandle]
}

func (a *recordingAttacher) Attach(h agent.InteractiveCommandHandle) {
	a.attached.Add(1)
	a.last.Store(&h)
}

func (a *recordingAttacher) Detach() {
	a.detached.Add(1)
}

// TestRunInteractiveCommand_AttachesAndDetachesHandle verifies the wiring
// the orchestrator depends on: when WithInteractiveHandleAttacher installed
// an attacher in the exec context, the runtime helper Attach()es the live
// handle on start and Detach()es on exit. Regressions here would silently
// break Stop / RequestStop in production because the cancel registry would
// hold no handle to interrupt.
func TestRunInteractiveCommand_AttachesAndDetachesHandle(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	rec := &recordingAttacher{}
	ctx := agent.WithSandboxProvider(context.Background(), provider)
	ctx = agent.WithInteractiveHandleAttacher(ctx, rec)

	res, err := runInteractiveCommand(ctx, &agent.Sandbox{ID: "t", WorkDir: "/w", HomeDir: "/h"}, InteractiveRunSpec{
		Cmd: "agent",
		Profile: agent.AgentRuntimeProfile{
			Cancellation:      agent.DefaultCancellationSpec,
			PreferSplitOutput: true,
		},
		OnStdout: func([]byte) {},
	})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	require.Equal(t, int32(1), rec.attached.Load(),
		"runInteractiveCommand must Attach the live handle so RequestStop can deliver interrupts")
	require.Equal(t, int32(1), rec.detached.Load(),
		"runInteractiveCommand must Detach on exit so the registry never holds a stale handle")
	require.NotNil(t, rec.last.Load(), "attached handle pointer should be observable")
}

// TestRunInteractiveCommand_NoAttacher_NoOp ensures unit tests that drive an
// adapter without a CancelRegistry-attached context still work — the helper
// must tolerate a missing attacher silently.
func TestRunInteractiveCommand_NoAttacher_NoOp(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	ctx := agent.WithSandboxProvider(context.Background(), provider)

	res, err := runInteractiveCommand(ctx, &agent.Sandbox{ID: "t", WorkDir: "/w", HomeDir: "/h"}, InteractiveRunSpec{
		Cmd: "agent",
		Profile: agent.AgentRuntimeProfile{
			Cancellation: agent.DefaultCancellationSpec,
		},
		OnStdout: func([]byte) {},
	})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
}

// TestRunInteractiveCommand_RequiresInteractiveProvider rejects providers
// that don't implement the optional capability instead of falling back to
// ExecStream — silent fallback would mean adapters running through a
// degraded transport without the cancel registry knowing.
func TestRunInteractiveCommand_RequiresInteractiveProvider(t *testing.T) {
	t.Parallel()

	provider := &nonInteractiveProvider{}
	ctx := agent.WithSandboxProvider(context.Background(), provider)

	_, err := runInteractiveCommand(ctx, &agent.Sandbox{ID: "t", WorkDir: "/w"}, InteractiveRunSpec{
		Cmd: "agent",
		Profile: agent.AgentRuntimeProfile{
			Cancellation: agent.DefaultCancellationSpec,
		},
		OnStdout: func([]byte) {},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support interactive commands")
}

// nonInteractiveProvider is a stand-alone SandboxProvider implementation
// that intentionally does NOT also implement InteractiveSandboxProvider.
// It returns trivial values for every required method.
type nonInteractiveProvider struct{}

func (p *nonInteractiveProvider) Name() string { return "non-interactive" }
func (p *nonInteractiveProvider) Create(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{}, nil
}
func (p *nonInteractiveProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	return nil
}
func (p *nonInteractiveProvider) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}
func (p *nonInteractiveProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, nil
}
func (p *nonInteractiveProvider) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}
func (p *nonInteractiveProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	return nil
}
func (p *nonInteractiveProvider) Destroy(context.Context, *agent.Sandbox) error { return nil }
func (p *nonInteractiveProvider) IsAlive(context.Context, *agent.Sandbox) (bool, error) {
	return false, nil
}
func (p *nonInteractiveProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}
func (p *nonInteractiveProvider) Snapshot(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (p *nonInteractiveProvider) Restore(context.Context, *agent.Sandbox, io.Reader) error {
	return nil
}

var _ agent.SandboxProvider = (*nonInteractiveProvider)(nil)
