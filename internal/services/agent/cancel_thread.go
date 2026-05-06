package agent

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// threadCancelEntry holds the run-context cancel func that unwinds the
// in-flight turn for one tab.
type threadCancelEntry struct {
	ctxCancel context.CancelFunc
	once      sync.Once
}

// ThreadCancelRegistry maps thread IDs to their cancellable agent run.
// It mirrors CancelRegistry but is keyed by thread instead of session so that
// cancelling one tab leaves siblings running. The orchestrator registers a
// thread when it starts an agent run with thread-scoped options and
// deregisters when the run unwinds.
//
// Thread-scoped cancellation does not signal the in-container agent process
// directly: two sibling tabs commonly run the same binary and `pkill -x` would
// interrupt both. Until per-tab PID tracking lands (Phase 4.5), cancellation
// is intentionally context-only — the orchestrator's run context goroutines
// observe the cancel and unwind the turn cooperatively.
type ThreadCancelRegistry struct {
	mu     sync.Map // thread ID (uuid.UUID) → *threadCancelEntry
	logger zerolog.Logger
}

// NewThreadCancelRegistry creates a new ThreadCancelRegistry.
func NewThreadCancelRegistry(logger zerolog.Logger) *ThreadCancelRegistry {
	return &ThreadCancelRegistry{logger: logger}
}

// Register stores the run-context cancel func for a thread. Sandbox and
// process-name plumbing was removed when SIGINT-by-binary-name was rejected
// for the same-binary multi-tab case (see type doc); add it back alongside
// per-tab PID tracking when Phase 4.5 lands.
func (r *ThreadCancelRegistry) Register(threadID uuid.UUID, ctxCancel context.CancelFunc) {
	if threadID == uuid.Nil {
		return
	}
	r.mu.Store(threadID, &threadCancelEntry{ctxCancel: ctxCancel})
}

// Deregister removes the entry. Call from a defer at the end of the agent
// run path so a crashed run does not leave a stale handle.
func (r *ThreadCancelRegistry) Deregister(threadID uuid.UUID) {
	r.mu.Delete(threadID)
}

// CancelThread cancels the run context associated with this thread.
// Returns true when the request was accepted, false when no entry exists for
// the thread (e.g. the run already finished). Safe to call multiple times;
// sync.Once guarantees the cancel goroutine fires at most once per entry.
func (r *ThreadCancelRegistry) CancelThread(threadID uuid.UUID) bool {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return false
	}
	entry := val.(*threadCancelEntry)
	entry.once.Do(func() {
		r.logger.Info().
			Str("thread_id", threadID.String()).
			Msg("cancelling thread via run context")
		entry.ctxCancel()
	})
	return true
}
