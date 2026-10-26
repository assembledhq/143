package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestRuntimeController_RequestStopThreadScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		threadScope    bool
		isolateParent  bool
		expectedStops  []models.RuntimeStopReason
		siblingStopped bool
	}{
		{name: "isolated review thread", threadScope: true, isolateParent: true},
		{name: "shared lifecycle thread", threadScope: true, expectedStops: []models.RuntimeStopReason{models.RuntimeStopReasonNoProgress}},
		{name: "unthreaded session", expectedStops: []models.RuntimeStopReason{models.RuntimeStopReasonNoProgress}, siblingStopped: true},
		{name: "nil thread preserves parent lifecycle", isolateParent: true, expectedStops: []models.RuntimeStopReason{models.RuntimeStopReasonNoProgress}, siblingStopped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionID, threadID, siblingID := uuid.New(), uuid.New(), uuid.New()
			cancels := NewCancelRegistry(zerolog.Nop())
			runCtx, cancelRun := context.WithCancelCause(context.Background())
			siblingCtx, cancelSibling := context.WithCancelCause(context.Background())
			t.Cleanup(func() {
				cancelRun(context.Canceled)
				cancelSibling(context.Canceled)
				cancels.Deregister(sessionID)
			})
			cancels.RegisterScopedCause(sessionID, threadID, cancelRun, DefaultCancellationSpec)
			cancels.RegisterScopedCause(sessionID, siblingID, cancelSibling, DefaultCancellationSpec)
			sessionStore := &runtimeTestSessionStore{}
			controller := newRuntimeController(runtimeConfig{GracefulShutdownWindow: time.Second}, sessionStore, &runtimeTestJobStore{}, cancels, zerolog.Nop(), uuid.New(), sessionID, 0, nil, newRuntimeProgressTracker(time.Now()))
			if tt.threadScope {
				controller.SetThreadScope(threadID, tt.isolateParent)
			} else if tt.isolateParent {
				controller.SetThreadScope(uuid.Nil, tt.isolateParent)
			}

			controller.RequestStop(StopReasonNoProgress)

			select {
			case <-runCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("runtime watchdog should stop its process")
			}
			expectedSiblingReason := StopReasonNone
			if tt.siblingStopped {
				expectedSiblingReason = StopReasonNoProgress
				select {
				case <-siblingCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("unthreaded session stop should preserve session cancellation fanout")
				}
			} else {
				require.NoError(t, siblingCtx.Err(), "thread watchdog should leave sibling process running")
			}
			require.Equal(t, []StopReason{StopReasonNoProgress, expectedSiblingReason}, []StopReason{cancels.StopReasonScoped(sessionID, threadID), cancels.StopReasonScoped(sessionID, siblingID)}, "thread-scoped cancellation should preserve sibling stop reason")
			require.Equal(t, tt.expectedStops, sessionStore.stopRequests, "only shared lifecycle stops should mark the parent session for runtime reaping")
		})
	}
}

func TestRuntimeController_RequestStopMissingThreadScopeUsesFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		isolateParent bool
		expectedStops []models.RuntimeStopReason
	}{
		{name: "isolated review thread", isolateParent: true},
		{name: "shared lifecycle thread", expectedStops: []models.RuntimeStopReason{models.RuntimeStopReasonNoProgress}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionID, siblingID := uuid.New(), uuid.New()
			cancels := NewCancelRegistry(zerolog.Nop())
			runCtx, cancelRun := context.WithCancelCause(context.Background())
			siblingCtx, cancelSibling := context.WithCancelCause(context.Background())
			t.Cleanup(func() {
				cancelRun(context.Canceled)
				cancelSibling(context.Canceled)
				cancels.Deregister(sessionID)
			})
			cancels.RegisterScopedCause(sessionID, siblingID, cancelSibling, DefaultCancellationSpec)
			sessionStore := &runtimeTestSessionStore{}
			controller := newRuntimeController(runtimeConfig{GracefulShutdownWindow: time.Second}, sessionStore, &runtimeTestJobStore{}, cancels, zerolog.Nop(), uuid.New(), sessionID, 0, nil, newRuntimeProgressTracker(time.Now()))
			controller.SetThreadScope(uuid.New(), tt.isolateParent)
			controller.SetStopFallback(cancelRun)

			controller.RequestStop(StopReasonNoProgress)

			require.ErrorIs(t, runCtx.Err(), context.Canceled, "missing scope should cancel the exact runtime fallback context")
			require.ErrorIs(t, context.Cause(runCtx), context.Canceled, "missing scope should preserve the watchdog cancellation cause")
			require.NoError(t, siblingCtx.Err(), "missing scope should not broadcast the policy stop to a sibling")
			require.Equal(t, StopReasonNone, cancels.StopReasonScoped(sessionID, siblingID), "missing scope should leave the sibling stop reason empty")
			require.Equal(t, tt.expectedStops, sessionStore.stopRequests, "fallback stop should preserve the configured parent isolation")
		})
	}
}
