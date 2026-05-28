package agent

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	testWaitTimeout  = 2 * time.Second
	testPollInterval = 10 * time.Millisecond
)

func TestThreadCancelRegistry_CancelThreadFallsBackToContextCancel(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	cancelled := false
	registry := NewThreadCancelRegistry(zerolog.Nop())

	registry.Register(threadID, func() {
		cancelled = true
	})

	accepted := registry.CancelThread(threadID)
	require.True(t, accepted, "CancelThread should accept registered threads")

	require.Eventually(t, func() bool {
		return cancelled
	}, testWaitTimeout, testPollInterval, "CancelThread should unwind the run context")
}

func TestThreadCancelRegistry_CancelThreadDeliversInterruptThroughHandle(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	cancelled := false

	registry.RegisterWithSpec(threadID, func() {
		cancelled = true
	}, CancellationSpec{Method: CancellationMethodEscape})
	registry.AttachHandle(threadID, handle)

	accepted := registry.CancelThread(threadID)
	require.True(t, accepted, "CancelThread should accept registered threads")

	require.Eventually(t, func() bool {
		return len(handle.Interrupts()) == 1
	}, testWaitTimeout, testPollInterval, "CancelThread should deliver the configured interrupt to the live thread handle")
	require.Equal(t, CancellationMethodEscape, handle.Interrupts()[0].Method, "CancelThread should use the thread-specific cancellation method")
	require.False(t, cancelled, "CancelThread should not cancel the context when the handle interrupt succeeds")
}

func TestThreadCancelRegistry_DeliverInputWritesToAttachedHandle(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	handle := newThreadCancelTestHandle()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})
	registry.AttachHandle(threadID, handle)

	err := registry.DeliverInput(context.Background(), threadID, []byte("hello\n"))
	require.NoError(t, err, "DeliverInput should write to the live thread handle")
	require.Equal(t, []byte("hello\n"), handle.StdinBuffer(), "DeliverInput should preserve the input payload bytes")
}

func TestThreadCancelRegistry_DeregisterRemovesEntry(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})

	registry.Deregister(threadID)
	require.False(t, registry.CancelThread(threadID), "deregistered threads should not be cancellable")
}

func TestThreadCancelRegistry_CancelThreadIsIdempotent(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	calls := 0
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() { calls++ })

	require.True(t, registry.CancelThread(threadID))
	require.True(t, registry.CancelThread(threadID), "re-cancel of a still-registered thread is accepted")

	require.Eventually(t, func() bool { return calls == 1 }, testWaitTimeout, testPollInterval,
		"sync.Once must guarantee the cancel func fires exactly once")
}

func TestThreadCancelRegistry_RequestStopForceKillsAfterGrace(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	handle := newThreadCancelTestHandle()
	cancelled := false
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {
		cancelled = true
	})
	registry.AttachHandle(threadID, handle)

	require.True(t, registry.requestStop(threadID, 10*time.Millisecond), "requestStop should accept a registered thread")

	require.Eventually(t, func() bool {
		return handle.KillCount() == 1 && cancelled
	}, testWaitTimeout, testPollInterval, "requestStop should force-kill and cancel context if the thread remains registered after grace")
}

type threadCancelTestHandle struct {
	mu         sync.Mutex
	interrupts []CancellationSpec
	stdin      bytes.Buffer
	writeErr   error
	kills      int
}

func newThreadCancelTestHandle() *threadCancelTestHandle {
	return &threadCancelTestHandle{}
}

func (h *threadCancelTestHandle) ID() string                       { return "thread-test-handle" }
func (h *threadCancelTestHandle) Stdout() io.Reader                { return bytes.NewReader(nil) }
func (h *threadCancelTestHandle) Stderr() io.Reader                { return bytes.NewReader(nil) }
func (h *threadCancelTestHandle) CloseInput(context.Context) error { return nil }
func (h *threadCancelTestHandle) Kill(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kills++
	return nil
}
func (h *threadCancelTestHandle) Close() error                      { return nil }
func (h *threadCancelTestHandle) Wait(context.Context) (int, error) { return 0, nil }
func (h *threadCancelTestHandle) WriteInput(_ context.Context, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writeErr != nil {
		return h.writeErr
	}
	_, err := h.stdin.Write(data)
	return err
}

func (h *threadCancelTestHandle) Interrupt(_ context.Context, spec CancellationSpec) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.interrupts = append(h.interrupts, spec)
	return nil
}

func (h *threadCancelTestHandle) Interrupts() []CancellationSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]CancellationSpec, len(h.interrupts))
	copy(out, h.interrupts)
	return out
}

func (h *threadCancelTestHandle) KillCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kills
}

func (h *threadCancelTestHandle) StdinBuffer() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]byte, h.stdin.Len())
	copy(out, h.stdin.Bytes())
	return out
}
