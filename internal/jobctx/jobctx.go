// Package jobctx carries per-invocation job metadata (attempt counters) on
// the context. It lives in its own package so the worker (which sets the
// values) and downstream services like the agent orchestrator (which read
// them) can share it without introducing an import cycle.
package jobctx

import "context"

type ctxKey int

const attemptKey ctxKey = iota

type attemptInfo struct {
	current int
	max     int
}

// WithAttempt annotates ctx with the current attempt number (1-indexed) and
// the configured attempt ceiling for the running job.
func WithAttempt(ctx context.Context, current, max int) context.Context {
	return context.WithValue(ctx, attemptKey, attemptInfo{current: current, max: max})
}

// IsFinalAttempt reports whether the job is on its last allowed attempt,
// i.e. the next failure will dead-letter the job. Returns false when no
// attempt metadata is present (e.g. direct callers outside the worker), so
// callers default to treating unknown contexts as non-final.
func IsFinalAttempt(ctx context.Context) bool {
	info, ok := ctx.Value(attemptKey).(attemptInfo)
	if !ok {
		return false
	}
	return info.current >= info.max
}
