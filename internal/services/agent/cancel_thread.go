package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// threadCancelEntry holds the run-context cancel func that unwinds the
// in-flight turn for one tab.
type threadCancelEntry struct {
	ctxCancel context.CancelFunc
	cancel    CancellationSpec

	mu     sync.Mutex
	handle InteractiveCommandHandle
	once   sync.Once
}

// ThreadCancelRegistry maps thread IDs to their cancellable agent run.
// It mirrors CancelRegistry but is keyed by thread instead of session so that
// cancelling one tab leaves siblings running. The orchestrator registers a
// thread when it starts an agent run with thread-scoped options and
// deregisters when the run unwinds.
//
// Thread-scoped cancellation and input delivery are keyed by thread, never by
// process name. The adapter runtime helper attaches the exact provider handle
// for this thread's command, so a stop or input write targets only the matching
// tab even when sibling tabs run the same CLI binary in the shared sandbox.
type ThreadCancelRegistry struct {
	mu     sync.Map // thread ID (uuid.UUID) → *threadCancelEntry
	logger zerolog.Logger
}

var ErrThreadHandleUnavailable = errors.New("thread runtime handle unavailable")

// NewThreadCancelRegistry creates a new ThreadCancelRegistry.
func NewThreadCancelRegistry(logger zerolog.Logger) *ThreadCancelRegistry {
	return &ThreadCancelRegistry{logger: logger}
}

// Register stores the run-context cancel func for a thread. Sandbox and
// process-name routing is intentionally not used; the live provider handle is
// attached later by HandleAttacher once the adapter starts the command.
func (r *ThreadCancelRegistry) Register(threadID uuid.UUID, ctxCancel context.CancelFunc) {
	r.RegisterWithSpec(threadID, ctxCancel, DefaultCancellationSpec)
}

func (r *ThreadCancelRegistry) RegisterWithSpec(threadID uuid.UUID, ctxCancel context.CancelFunc, cancelSpec CancellationSpec) {
	if threadID == uuid.Nil {
		return
	}
	if cancelSpec.Method == "" {
		cancelSpec = DefaultCancellationSpec
	}
	r.mu.Store(threadID, &threadCancelEntry{ctxCancel: ctxCancel, cancel: cancelSpec})
}

// Deregister removes the entry. Call from a defer at the end of the agent
// run path so a crashed run does not leave a stale handle.
func (r *ThreadCancelRegistry) Deregister(threadID uuid.UUID) {
	r.mu.Delete(threadID)
}

func (r *ThreadCancelRegistry) AttachHandle(threadID uuid.UUID, handle InteractiveCommandHandle) {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return
	}
	entry := val.(*threadCancelEntry)
	entry.mu.Lock()
	entry.handle = handle
	entry.mu.Unlock()
}

func (r *ThreadCancelRegistry) DetachHandle(threadID uuid.UUID) {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return
	}
	entry := val.(*threadCancelEntry)
	entry.mu.Lock()
	entry.handle = nil
	entry.mu.Unlock()
}

func (r *ThreadCancelRegistry) HandleAttacher(threadID uuid.UUID) InteractiveHandleAttacher {
	return &threadRegistryHandleAttacher{registry: r, threadID: threadID}
}

type threadRegistryHandleAttacher struct {
	registry *ThreadCancelRegistry
	threadID uuid.UUID
}

func (a *threadRegistryHandleAttacher) Attach(handle InteractiveCommandHandle) {
	a.registry.AttachHandle(a.threadID, handle)
}

func (a *threadRegistryHandleAttacher) Detach() {
	a.registry.DetachHandle(a.threadID)
}

func (r *ThreadCancelRegistry) DeliverInput(ctx context.Context, threadID uuid.UUID, data []byte) error {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return ErrThreadHandleUnavailable
	}
	entry := val.(*threadCancelEntry)
	entry.mu.Lock()
	handle := entry.handle
	entry.mu.Unlock()
	if handle == nil {
		return ErrThreadHandleUnavailable
	}
	return handle.WriteInput(ctx, data)
}

// CancelThread interrupts the live provider handle associated with this
// thread, falling back to context cancellation when no handle is attached or
// interrupt delivery fails.
// Returns true when the request was accepted, false when no entry exists for
// the thread (e.g. the run already finished). Safe to call multiple times;
// sync.Once guarantees the cancel goroutine fires at most once per entry.
func (r *ThreadCancelRegistry) CancelThread(threadID uuid.UUID) bool {
	return r.requestStop(threadID, 30*time.Second)
}

func (r *ThreadCancelRegistry) requestStop(threadID uuid.UUID, graceWindow time.Duration) bool {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return false
	}
	entry := val.(*threadCancelEntry)
	entry.once.Do(func() {
		go r.doCancel(threadID, entry, graceWindow)
	})
	return true
}

func (r *ThreadCancelRegistry) doCancel(threadID uuid.UUID, entry *threadCancelEntry, graceWindow time.Duration) {
	entry.mu.Lock()
	handle := entry.handle
	spec := entry.cancel
	entry.mu.Unlock()
	if handle == nil {
		r.logger.Info().
			Str("thread_id", threadID.String()).
			Msg("no live thread handle attached, falling back to context cancel")
		entry.ctxCancel()
		return
	}

	interruptCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := handle.Interrupt(interruptCtx, spec); err != nil {
		if errors.Is(err, ErrUnsupportedInterruptMethod) && spec.Method != CancellationMethodCtrlC {
			r.logger.Warn().Err(err).
				Str("thread_id", threadID.String()).
				Str("requested_method", string(spec.Method)).
				Msg("thread handle does not support requested interrupt method, falling back to Ctrl+C")
			err = handle.Interrupt(interruptCtx, DefaultCancellationSpec)
		}
		if err != nil {
			r.logger.Warn().Err(err).
				Str("thread_id", threadID.String()).
				Msg("failed to interrupt live thread handle, falling back to context cancel")
			entry.ctxCancel()
			return
		}
	}
	r.logger.Info().
		Str("thread_id", threadID.String()).
		Msg("delivered graceful interrupt to running thread")

	if graceWindow <= 0 {
		graceWindow = 30 * time.Second
	}
	timer := time.NewTimer(graceWindow)
	defer timer.Stop()

	<-timer.C
	if !r.threadStillRegistered(threadID) {
		return
	}
	r.logger.Warn().
		Str("thread_id", threadID.String()).
		Msg("thread did not exit after graceful interrupt, force-stopping handle and cancelling context")
	killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer killCancel()
	if err := handle.Kill(killCtx); err != nil {
		r.logger.Warn().Err(err).
			Str("thread_id", threadID.String()).
			Msg("failed to force-stop thread handle")
	}
	entry.ctxCancel()
}

func (r *ThreadCancelRegistry) threadStillRegistered(threadID uuid.UUID) bool {
	_, ok := r.mu.Load(threadID)
	return ok
}
