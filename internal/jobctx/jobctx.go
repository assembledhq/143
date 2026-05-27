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

type hookRegistry struct {
	mu    sync.Mutex
	hooks []DeadLetterHook
	fired bool
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
	reg.hooks = append(reg.hooks, hook)
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
	if reg.fired {
		reg.mu.Unlock()
		return
	}
	reg.fired = true
	hooks := make([]DeadLetterHook, len(reg.hooks))
	copy(hooks, reg.hooks)
	reg.mu.Unlock()
	for _, hook := range hooks {
		hook(ctx, err)
	}
}
