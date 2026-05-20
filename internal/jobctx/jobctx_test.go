package jobctx_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
)

func TestRegisterDeadLetterHook_NoRegistryIsNoop(t *testing.T) {
	t.Parallel()

	// Registering on a bare context should silently drop the hook; running
	// hooks on a bare context is also a no-op. Direct callers own their
	// own error handling.
	called := false
	jobctx.RegisterDeadLetterHook(context.Background(), func(context.Context, error) {
		called = true
	})
	jobctx.RunDeadLetterHooks(context.Background(), errors.New("boom"))
	require.False(t, called, "hook must not fire when there is no registry")
}

func TestRegisterDeadLetterHook_NilHookIsSkipped(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithDeadLetterHooks(context.Background())
	require.NotPanics(t, func() {
		jobctx.RegisterDeadLetterHook(ctx, nil)
		jobctx.RunDeadLetterHooks(ctx, errors.New("boom"))
	})
}

func TestRunDeadLetterHooks_InvokesInOrderWithError(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithDeadLetterHooks(context.Background())

	var order []string
	var gotErr error
	jobctx.RegisterDeadLetterHook(ctx, func(_ context.Context, err error) {
		order = append(order, "first")
		gotErr = err
	})
	jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) {
		order = append(order, "second")
	})

	want := errors.New("dead letter")
	jobctx.RunDeadLetterHooks(ctx, want)

	require.Equal(t, []string{"first", "second"}, order)
	require.ErrorIs(t, gotErr, want)
}

func TestRunDeadLetterHooks_FiresAtMostOnce(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithDeadLetterHooks(context.Background())

	var calls int
	jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) {
		calls++
	})

	jobctx.RunDeadLetterHooks(ctx, errors.New("first"))
	jobctx.RunDeadLetterHooks(ctx, errors.New("second"))

	require.Equal(t, 1, calls, "repeated RunDeadLetterHooks calls on the same registry must be idempotent")
}

func TestWithDeadLetterHooks_ProducesFreshRegistryPerCall(t *testing.T) {
	t.Parallel()

	parent := jobctx.WithDeadLetterHooks(context.Background())
	child := jobctx.WithDeadLetterHooks(parent)

	var parentCalls, childCalls int
	jobctx.RegisterDeadLetterHook(parent, func(context.Context, error) { parentCalls++ })
	jobctx.RegisterDeadLetterHook(child, func(context.Context, error) { childCalls++ })

	jobctx.RunDeadLetterHooks(child, nil)
	require.Equal(t, 0, parentCalls, "child registry must not leak hooks to parent")
	require.Equal(t, 1, childCalls)

	jobctx.RunDeadLetterHooks(parent, nil)
	require.Equal(t, 1, parentCalls)
}

func TestRegisterDeadLetterHook_ConcurrentRegistrationIsSafe(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithDeadLetterHooks(context.Background())

	var wg sync.WaitGroup
	var mu sync.Mutex
	calls := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) {
				mu.Lock()
				calls++
				mu.Unlock()
			})
		}()
	}
	wg.Wait()

	jobctx.RunDeadLetterHooks(ctx, nil)
	require.Equal(t, 32, calls)
}

func TestWithLockToken_RoundTrip(t *testing.T) {
	t.Parallel()

	want := uuid.New()
	ctx := jobctx.WithLockToken(context.Background(), want)

	got, ok := jobctx.LockTokenFromContext(ctx)
	require.True(t, ok, "WithLockToken should store the lock token in context")
	require.Equal(t, want, got, "LockTokenFromContext should return the stored token")
}

func TestWithJobID_RoundTrip(t *testing.T) {
	t.Parallel()

	want := uuid.New()
	ctx := jobctx.WithJobID(context.Background(), want)

	got, ok := jobctx.JobIDFromContext(ctx)
	require.True(t, ok, "WithJobID should store the job id in context")
	require.Equal(t, want, got, "JobIDFromContext should return the stored job id")
}

func TestWithOwnerKind_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithOwnerKind(context.Background(), "session_executor")

	got, ok := jobctx.OwnerKindFromContext(ctx)
	require.True(t, ok, "WithOwnerKind should store the owner kind in context")
	require.Equal(t, "session_executor", got, "OwnerKindFromContext should return the stored owner kind")
}

func TestWithDeadTargetNode_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := jobctx.WithDeadTargetNode(context.Background(), "worker-dead")

	got, ok := jobctx.DeadTargetNodeFromContext(ctx)
	require.True(t, ok, "WithDeadTargetNode should store the dead target node in context")
	require.Equal(t, "worker-dead", got, "DeadTargetNodeFromContext should return the stored node id")
}
