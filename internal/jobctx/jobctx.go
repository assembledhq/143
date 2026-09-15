// Package jobctx lets a job handler register hooks that run only if the
// job is dead-lettered. Split from the worker package to avoid an import
// cycle with services that need to register hooks.
package jobctx

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ctxKey int

const (
	hooksKey ctxKey = iota
	lockTokenKey
	deadTargetNodeKey
	jobCreatedAtKey
	jobIDKey
	ownerKindKey
	workerNodeIDKey
)

// DeadLetterHook runs synchronously on the worker's poll goroutine when
// a job is dead-lettered, receiving the final error recorded on the job.
type DeadLetterHook func(ctx context.Context, err error)

// RetryScheduledHook runs only after the lease-owned retry update succeeds.
// It receives the exact durable run_at selected by the worker.
type RetryScheduledHook func(ctx context.Context, err error, runAt time.Time)

type hookRegistry struct {
	mu                  sync.Mutex
	deadLetterHooks     []DeadLetterHook
	retryScheduledHooks []RetryScheduledHook
	deadLetterFired     bool
	retryScheduledFired bool
}

// WithDeadLetterHooks returns a context carrying a fresh, empty hook
// registry — installed once per attempt so hooks don't leak across retries.
func WithDeadLetterHooks(ctx context.Context) context.Context {
	return context.WithValue(ctx, hooksKey, &hookRegistry{})
}

func WithLockToken(ctx context.Context, token uuid.UUID) context.Context {
	return context.WithValue(ctx, lockTokenKey, token)
}

func LockTokenFromContext(ctx context.Context) (uuid.UUID, bool) {
	token, ok := ctx.Value(lockTokenKey).(uuid.UUID)
	return token, ok
}

func WithJobID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, jobIDKey, id)
}

func JobIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(jobIDKey).(uuid.UUID)
	return id, ok
}

func WithOwnerKind(ctx context.Context, ownerKind string) context.Context {
	return context.WithValue(ctx, ownerKindKey, ownerKind)
}

func OwnerKindFromContext(ctx context.Context) (string, bool) {
	ownerKind, ok := ctx.Value(ownerKindKey).(string)
	return ownerKind, ok && ownerKind != ""
}

func WithWorkerNodeID(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, workerNodeIDKey, nodeID)
}

func WorkerNodeIDFromContext(ctx context.Context) (string, bool) {
	nodeID, ok := ctx.Value(workerNodeIDKey).(string)
	return nodeID, ok && nodeID != ""
}

func WithDeadTargetNode(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, deadTargetNodeKey, nodeID)
}

func DeadTargetNodeFromContext(ctx context.Context) (string, bool) {
	nodeID, ok := ctx.Value(deadTargetNodeKey).(string)
	return nodeID, ok && nodeID != ""
}

// WithJobCreatedAt records the wall-clock time the job row was first
// enqueued. Handlers can read it via JobCreatedAtFromContext to enforce
// their own deadlines without depending on the global maxRetryableDuration
// (which is intentionally coarse). Handlers that drop a job early via
// returning nil still benefit from this so retries that consume no Attempts
// (RetryableError) don't loop indefinitely.
func WithJobCreatedAt(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, jobCreatedAtKey, t)
}

func JobCreatedAtFromContext(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(jobCreatedAtKey).(time.Time)
	return t, ok && !t.IsZero()
}

// RegisterDeadLetterHook queues a hook on the context's registry. When the
// context has no registry (direct caller outside the worker), the hook is
// dropped and the caller is expected to act on the returned error directly.
func RegisterDeadLetterHook(ctx context.Context, hook DeadLetterHook) {
	reg, _ := ctx.Value(hooksKey).(*hookRegistry)
	if reg == nil || hook == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.deadLetterHooks = append(reg.deadLetterHooks, hook)
}

// RegisterRetryScheduledHook queues a hook for successful lease-owned retry
// scheduling. Direct handler calls without a worker registry are a no-op.
func RegisterRetryScheduledHook(ctx context.Context, hook RetryScheduledHook) {
	reg, _ := ctx.Value(hooksKey).(*hookRegistry)
	if reg == nil || hook == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.retryScheduledHooks = append(reg.retryScheduledHooks, hook)
}

// RunDeadLetterHooks invokes registered hooks in registration order. Hooks
// fire at most once per registry, so independent dead-letter sites don't
// need to coordinate. No-op when the context has no registry.
func RunDeadLetterHooks(ctx context.Context, err error) {
	reg, _ := ctx.Value(hooksKey).(*hookRegistry)
	if reg == nil {
		return
	}
	reg.mu.Lock()
	if reg.deadLetterFired {
		reg.mu.Unlock()
		return
	}
	reg.deadLetterFired = true
	hooks := make([]DeadLetterHook, len(reg.deadLetterHooks))
	copy(hooks, reg.deadLetterHooks)
	reg.mu.Unlock()
	for _, hook := range hooks {
		hook(ctx, err)
	}
}

// RunRetryScheduledHooks invokes registered hooks at most once after the job
// row has durably accepted the worker's retry timestamp.
func RunRetryScheduledHooks(ctx context.Context, err error, runAt time.Time) {
	reg, _ := ctx.Value(hooksKey).(*hookRegistry)
	if reg == nil {
		return
	}
	reg.mu.Lock()
	if reg.retryScheduledFired {
		reg.mu.Unlock()
		return
	}
	reg.retryScheduledFired = true
	hooks := make([]RetryScheduledHook, len(reg.retryScheduledHooks))
	copy(hooks, reg.retryScheduledHooks)
	reg.mu.Unlock()
	for _, hook := range hooks {
		hook(ctx, err, runAt)
	}
}
