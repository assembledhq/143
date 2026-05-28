package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

// fakeHandle is a minimal in-memory InteractiveCommandHandle used to drive
// the cancel registry from the public attach/detach API.
type fakeHandle struct {
	mu                sync.Mutex
	interrupts        []agent.CancellationSpec
	interruptFn       func(spec agent.CancellationSpec) error
	killed            bool
	killDeadlineDelta time.Duration
	closed            chan struct{}
}

func newFakeHandle() *fakeHandle {
	return &fakeHandle{closed: make(chan struct{})}
}

func (h *fakeHandle) ID() string                               { return "fake-handle" }
func (h *fakeHandle) Stdout() io.Reader                        { return bytes.NewReader(nil) }
func (h *fakeHandle) Stderr() io.Reader                        { return bytes.NewReader(nil) }
func (h *fakeHandle) WriteInput(context.Context, []byte) error { return nil }
func (h *fakeHandle) CloseInput(context.Context) error         { return nil }

func (h *fakeHandle) Interrupt(_ context.Context, spec agent.CancellationSpec) error {
	h.mu.Lock()
	h.interrupts = append(h.interrupts, spec)
	fn := h.interruptFn
	h.mu.Unlock()
	if fn != nil {
		return fn(spec)
	}
	return nil
}

func (h *fakeHandle) Kill(ctx context.Context) error {
	h.mu.Lock()
	h.killed = true
	if deadline, ok := ctx.Deadline(); ok {
		h.killDeadlineDelta = time.Until(deadline)
	}
	h.mu.Unlock()
	return nil
}

func (h *fakeHandle) Wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-h.closed:
		return 0, nil
	}
}

func (h *fakeHandle) Close() error {
	h.mu.Lock()
	select {
	case <-h.closed:
	default:
		close(h.closed)
	}
	h.mu.Unlock()
	return nil
}

func (h *fakeHandle) Interrupts() []agent.CancellationSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]agent.CancellationSpec, len(h.interrupts))
	copy(out, h.interrupts)
	return out
}

func (h *fakeHandle) Killed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killed
}

func (h *fakeHandle) KillDeadlineDelta() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killDeadlineDelta
}

func TestCancelRegistry_RegisterDeregister(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	require.False(t, reg.WasCancelled(id), "should not be cancelled before registration")

	reg.Register(id, func() {}, agent.DefaultCancellationSpec)
	reg.AttachHandle(id, newFakeHandle())

	require.True(t, reg.CancelSession(id))
	require.True(t, reg.WasCancelled(id))

	reg.Deregister(id)
	require.False(t, reg.WasCancelled(id))
}

func TestCancelRegistry_CancelSession_NotFound(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	require.False(t, reg.CancelSession(uuid.New()))
}

func TestCancelRegistry_CancelSession_DeliversInterruptThroughHandle(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	reg.Register(id, func() {}, agent.DefaultCancellationSpec)
	handle := newFakeHandle()
	reg.AttachHandle(id, handle)

	require.True(t, reg.CancelSession(id))
	require.Eventually(t, func() bool {
		return len(handle.Interrupts()) > 0
	}, 2*time.Second, 10*time.Millisecond, "cancel should deliver an interrupt through the live handle")
	require.Equal(t, agent.CancellationMethodCtrlC, handle.Interrupts()[0].Method)
	require.True(t, reg.WasCancelled(id))
}

func TestCancelRegistry_CancelSession_UnsupportedMethodFallsBackToCtrlC(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	reg.Register(id, func() {}, agent.CancellationSpec{Method: agent.CancellationMethodEscape})
	handle := newFakeHandle()
	handle.interruptFn = func(spec agent.CancellationSpec) error {
		if spec.Method == agent.CancellationMethodEscape {
			return agent.ErrUnsupportedInterruptMethod
		}
		return nil
	}
	reg.AttachHandle(id, handle)

	require.True(t, reg.CancelSession(id))
	require.Eventually(t, func() bool {
		return len(handle.Interrupts()) == 2
	}, 2*time.Second, 10*time.Millisecond, "unsupported interrupts should fall back to Ctrl+C")
	require.Equal(t, agent.CancellationMethodEscape, handle.Interrupts()[0].Method)
	require.Equal(t, agent.CancellationMethodCtrlC, handle.Interrupts()[1].Method)
}

func TestCancelRegistry_CancelSession_OnlyOnce(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var count atomic.Int32
	reg.Register(id, func() {}, agent.DefaultCancellationSpec)
	handle := newFakeHandle()
	handle.interruptFn = func(agent.CancellationSpec) error {
		count.Add(1)
		return nil
	}
	reg.AttachHandle(id, handle)

	require.True(t, reg.CancelSession(id))
	require.True(t, reg.CancelSession(id))
	require.True(t, reg.CancelSession(id))

	require.Eventually(t, func() bool {
		return count.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int32(1), count.Load(), "interrupt should only be delivered once")
}

func TestCancelRegistry_CancelSession_FallsBackToCtxCancelOnInterruptError(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var ctxCancelled atomic.Bool
	reg.Register(id, func() { ctxCancelled.Store(true) }, agent.DefaultCancellationSpec)
	handle := newFakeHandle()
	handle.interruptFn = func(agent.CancellationSpec) error {
		return errors.New("interrupt failed")
	}
	reg.AttachHandle(id, handle)

	require.True(t, reg.CancelSession(id))
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond, "interrupt failure should fall back to context cancel")
}

func TestCancelRegistry_CancelSession_NoHandle_FallsBackToCtxCancel(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var ctxCancelled atomic.Bool
	reg.Register(id, func() { ctxCancelled.Store(true) }, agent.DefaultCancellationSpec)

	require.True(t, reg.CancelSession(id))
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond, "without a live handle, cancel should fall straight back to context cancel")
}

func TestCancelRegistry_HandleAttacher_AttachAndDetach(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()
	reg.Register(id, func() {}, agent.DefaultCancellationSpec)

	attacher := reg.HandleAttacher(id)
	handle := newFakeHandle()
	attacher.Attach(handle)

	// Without explicit detach, cancel should deliver to the attached handle.
	require.True(t, reg.RequestStop(id, agent.StopReasonSoftBudget, time.Hour))
	require.Eventually(t, func() bool {
		return len(handle.Interrupts()) == 1
	}, 2*time.Second, 10*time.Millisecond)

	// After detach, the registry no longer has a handle to interrupt.
	attacher.Detach()
	id2 := uuid.New()
	var ctxCancelled atomic.Bool
	reg.Register(id2, func() { ctxCancelled.Store(true) }, agent.DefaultCancellationSpec)
	reg.HandleAttacher(id2).Detach() // safe no-op
	require.True(t, reg.RequestStop(id2, agent.StopReasonSoftBudget, time.Hour))
	require.Eventually(t, func() bool {
		return ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond)
}

func TestNewMultiInteractiveHandleAttacher_AttachesAndDetachesAll(t *testing.T) {
	t.Parallel()

	first := &recordingHandleAttacher{}
	second := &recordingHandleAttacher{}
	handle := newFakeHandle()

	attacher := agent.NewMultiInteractiveHandleAttacher(first, nil, second)
	require.NotNil(t, attacher, "multi attacher should be created when at least one delegate is present")

	attacher.Attach(handle)
	attacher.Detach()

	require.Equal(t, int32(1), first.attached.Load(), "first delegate should receive Attach")
	require.Equal(t, int32(1), second.attached.Load(), "second delegate should receive Attach")
	require.Equal(t, int32(1), first.detached.Load(), "first delegate should receive Detach")
	require.Equal(t, int32(1), second.detached.Load(), "second delegate should receive Detach")
}

type recordingHandleAttacher struct {
	attached atomic.Int32
	detached atomic.Int32
}

func (a *recordingHandleAttacher) Attach(agent.InteractiveCommandHandle) {
	a.attached.Add(1)
}

func (a *recordingHandleAttacher) Detach() {
	a.detached.Add(1)
}

// TestCancelRegistry_GraceWindowExpiry_EscalatesToKill exercises the cancel-
// mid-stream path: Interrupt is delivered to a handle whose underlying agent
// keeps streaming output and never exits. After the grace window the registry
// must escalate to handle.Kill and ctxCancel, even though the entry was never
// Deregistered.
func TestCancelRegistry_GraceWindowExpiry_EscalatesToKill(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	var ctxCancelled atomic.Bool
	reg.Register(id, func() { ctxCancelled.Store(true) }, agent.DefaultCancellationSpec)
	handle := newFakeHandle()
	// Simulate a real agent that accepts the interrupt but keeps running:
	// Interrupt returns nil, but the handle's Wait/closed never fires.
	reg.AttachHandle(id, handle)

	// Use RequestStop directly so we can pass a tiny grace window.
	require.True(t, reg.RequestStop(id, agent.StopReasonUserCancel, 50*time.Millisecond))

	require.Eventually(t, func() bool {
		return handle.Killed() && ctxCancelled.Load()
	}, 2*time.Second, 10*time.Millisecond, "grace expiry should escalate to Kill + ctxCancel when the agent ignores Interrupt")
	require.Len(t, handle.Interrupts(), 1, "Interrupt should be delivered exactly once before Kill escalation")
}

func TestCancelRegistry_GraceWindowExpiry_UsesFreshKillContext(t *testing.T) {
	t.Parallel()
	reg := agent.NewCancelRegistry(zerolog.Nop())
	id := uuid.New()

	reg.Register(id, func() {}, agent.DefaultCancellationSpec)
	handle := newFakeHandle()
	reg.AttachHandle(id, handle)

	require.True(t, reg.RequestStop(id, agent.StopReasonUserCancel, 2*time.Second))

	require.Eventually(t, func() bool {
		return handle.Killed()
	}, 4*time.Second, 10*time.Millisecond, "grace expiry should call Kill")
	require.Greater(t, handle.KillDeadlineDelta(), 29*time.Second, "Kill should receive a fresh force-stop timeout after the grace window expires")
}

// Compile-time check that fakeHandle satisfies the public handle contract.
var _ agent.InteractiveCommandHandle = (*fakeHandle)(nil)
