package agent_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

func TestCancelRegistry_RegisterDeregister(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	require.False(t, reg.WasCancelled(id), "should not be cancelled before registration")

	provider := testutil.NewMockSandboxProvider()
	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, func() {}, agent.DefaultCancellationSpec)

	// CancelSession should find it.
	require.True(t, reg.CancelSession(id))
	require.True(t, reg.WasCancelled(id))

	// After deregister, WasCancelled should be cleared.
	reg.Deregister(id)
	require.False(t, reg.WasCancelled(id))
}

func TestCancelRegistry_CancelSession_NotFound(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	require.False(t, reg.CancelSession(uuid.New()))
}

func TestCancelRegistry_CancelSession_SendsSIGINT(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	provider := testutil.NewMockSandboxProvider()
	var execCmd atomic.Value
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		execCmd.Store(cmd)
		return 0, nil
	}

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, func() {}, agent.DefaultCancellationSpec)

	require.True(t, reg.CancelSession(id))

	// Give the goroutine a moment to execute.
	require.Eventually(t, func() bool {
		v, _ := execCmd.Load().(string)
		return v != ""
	}, 2*time.Second, 10*time.Millisecond)

	require.Contains(t, execCmd.Load().(string), "kill -INT", "cancel should send SIGINT to the tracked agent pid first")
	require.Contains(t, execCmd.Load().(string), ".143-agent.pid", "cancel should target the tracked agent pid file")
	require.NotContains(t, execCmd.Load().(string), "pkill -INT", "cancel should not depend on a hard-coded process-name fallback")
	require.True(t, reg.WasCancelled(id))
}

type interruptingProvider struct {
	*testutil.MockSandboxProvider
	interruptFn func(ctx context.Context, sb *agent.Sandbox, req agent.InterruptRequest) error
}

func (p *interruptingProvider) Interrupt(ctx context.Context, sb *agent.Sandbox, req agent.InterruptRequest) error {
	if p.interruptFn != nil {
		return p.interruptFn(ctx, sb, req)
	}
	return nil
}

func TestCancelRegistry_CancelSession_UsesProviderInterruptor(t *testing.T) {
	t.Parallel()

	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var method atomic.Value
	provider := &interruptingProvider{
		MockSandboxProvider: testutil.NewMockSandboxProvider(),
		interruptFn: func(ctx context.Context, sb *agent.Sandbox, req agent.InterruptRequest) error {
			method.Store(string(req.Method))
			return nil
		},
	}

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, func() {}, agent.CancellationSpec{Method: agent.CancellationMethodCtrlC})

	require.True(t, reg.CancelSession(id), "cancel should find registered session")
	require.Eventually(t, func() bool {
		v, _ := method.Load().(string)
		return v != ""
	}, 2*time.Second, 10*time.Millisecond, "provider interruptor should receive an interrupt request")
	require.Equal(t, string(agent.CancellationMethodCtrlC), method.Load(), "provider interruptor should receive the resolved cancellation method")
}

func TestCancelRegistry_CancelSession_UnsupportedMethodFallsBackToCtrlC(t *testing.T) {
	t.Parallel()

	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	requests := make(chan agent.CancellationMethod, 2)
	provider := &interruptingProvider{
		MockSandboxProvider: testutil.NewMockSandboxProvider(),
		interruptFn: func(ctx context.Context, sb *agent.Sandbox, req agent.InterruptRequest) error {
			requests <- req.Method
			if req.Method == agent.CancellationMethodEscape {
				return agent.ErrUnsupportedInterruptMethod
			}
			return nil
		},
	}

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, func() {}, agent.CancellationSpec{Method: agent.CancellationMethodEscape})

	require.True(t, reg.CancelSession(id), "cancel should find registered session")
	require.Eventually(t, func() bool {
		return len(requests) == 2
	}, 2*time.Second, 10*time.Millisecond, "unsupported methods should fall back to the default Ctrl+C interrupt")
	require.Equal(t, agent.CancellationMethodEscape, <-requests, "cancel should try the adapter-specific interrupt first")
	require.Equal(t, agent.CancellationMethodCtrlC, <-requests, "cancel should fall back to Ctrl+C when the provider cannot deliver the requested method")
}

func TestCancelRegistry_CancelSession_OnlyOnce(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var execCount atomic.Int32
	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		execCount.Add(1)
		return 0, nil
	}

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, func() {}, agent.DefaultCancellationSpec)

	// Call cancel multiple times rapidly.
	require.True(t, reg.CancelSession(id))
	require.True(t, reg.CancelSession(id))
	require.True(t, reg.CancelSession(id))

	// Wait for goroutine to run.
	require.Eventually(t, func() bool {
		return execCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)

	// Should only have exec'd once despite 3 cancel calls.
	time.Sleep(100 * time.Millisecond) // brief pause to ensure no extra goroutines fire
	require.Equal(t, int32(1), execCount.Load(), "pkill should only be sent once")
}

func TestCancelRegistry_CancelSession_FallsBackToCtxCancel(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, context.DeadlineExceeded // simulate exec failure
	}

	var ctxCancelled atomic.Bool
	cancelFn := func() { ctxCancelled.Store(true) }

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, cancelFn, agent.DefaultCancellationSpec)

	require.True(t, reg.CancelSession(id))

	// Should fall back to context cancel when exec fails.
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond)
}

func TestCancelRegistry_RequestStop_NonZeroInterruptExitFallsBackToCtxCancel(t *testing.T) {
	t.Parallel()

	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 1, nil
	}

	var ctxCancelled atomic.Bool
	cancelFn := func() { ctxCancelled.Store(true) }

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	reg.Register(id, sb, provider, cancelFn, agent.DefaultCancellationSpec)

	require.True(t, reg.RequestStop(id, agent.StopReasonUserCancel, time.Hour), "stop should find registered session")
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 200*time.Millisecond, 10*time.Millisecond,
		"non-zero interrupt command exits should fall back to context cancel immediately")
}
