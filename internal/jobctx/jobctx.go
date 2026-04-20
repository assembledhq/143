// Package jobctx lets a job handler register hooks that run if — and only
// if — the worker decides to dead-letter the job. It lives in its own
// package so the worker (which owns the dead-letter decision) and
// downstream services like the agent orchestrator (which know what
// user-visible side effects to emit on terminal failure) can share it
// without introducing an import cycle.
//
// The worker installs a fresh registry on the handler context per attempt
// via WithDeadLetterHooks, then calls RunDeadLetterHooks on every
// dead-letter code path (FatalError, retryable timeout, retries
// exhausted, no handler registered). Handlers register side effects via
// RegisterDeadLetterHook; direct callers without a registry drop hooks
// silently and are expected to handle the returned error themselves.
package jobctx

import (
	"context"
	"sync"
)

type ctxKey int

const hooksKey ctxKey = iota

// DeadLetterHook is invoked once if the job is dead-lettered. It receives
// the context the handler ran under and the final error the worker is
// recording on the job. Hooks should be idempotent and fast — they run
// synchronously on the worker's poll goroutine.
type DeadLetterHook func(ctx context.Context, err error)

type hookRegistry struct {
	mu    sync.Mutex
	hooks []DeadLetterHook
	fired bool
}

// WithDeadLetterHooks returns a context carrying a fresh, empty hook
// registry. The worker calls this once per attempt before invoking the
// handler, so hooks registered on attempt N do not leak into attempt N+1.
func WithDeadLetterHooks(ctx context.Context) context.Context {
	return context.WithValue(ctx, hooksKey, &hookRegistry{})
}

// RegisterDeadLetterHook queues a hook to run if the current job attempt
// ends up being dead-lettered. When the context has no registry (e.g. a
// direct caller outside the worker), the hook is dropped — the caller is
// expected to act on the returned error directly. Safe to call from
// multiple goroutines.
func RegisterDeadLetterHook(ctx context.Context, hook DeadLetterHook) {
	reg, _ := ctx.Value(hooksKey).(*hookRegistry)
	if reg == nil || hook == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.hooks = append(reg.hooks, hook)
}

// RunDeadLetterHooks invokes every registered hook in registration order,
// passing the final error being recorded on the job. Hooks run at most
// once per registry even if RunDeadLetterHooks is called multiple times,
// so callers at independent dead-letter sites don't need to coordinate.
// No-op when the context has no registry.
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
