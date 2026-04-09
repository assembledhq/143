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
	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace"}
	reg.Register(id, sb, provider, func() {})

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

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace"}
	reg.Register(id, sb, provider, func() {})

	require.True(t, reg.CancelSession(id))

	// Give the goroutine a moment to execute.
	require.Eventually(t, func() bool {
		v, _ := execCmd.Load().(string)
		return v != ""
	}, 2*time.Second, 10*time.Millisecond)

	require.Contains(t, execCmd.Load().(string), "pkill -INT")
	require.True(t, reg.WasCancelled(id))
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

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace"}
	reg.Register(id, sb, provider, func() {})

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

	sb := &agent.Sandbox{ID: "sb-1", Provider: "mock", WorkDir: "/workspace"}
	reg.Register(id, sb, provider, cancelFn)

	require.True(t, reg.CancelSession(id))

	// Should fall back to context cancel when exec fails.
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond)
}
