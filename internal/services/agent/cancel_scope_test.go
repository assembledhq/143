package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestCancelRegistry_RequestStopScopedIsolatesPolicyStops(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason agent.StopReason
	}{
		{name: "no progress", reason: agent.StopReasonNoProgress},
		{name: "soft budget", reason: agent.StopReasonSoftBudget},
		{name: "absolute ceiling", reason: agent.StopReasonAbsoluteCeiling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := agent.NewCancelRegistry(zerolog.Nop())
			sessionID, firstID, secondID := uuid.New(), uuid.New(), uuid.New()
			firstCtx, firstCancel := context.WithCancelCause(context.Background())
			secondCtx, secondCancel := context.WithCancelCause(context.Background())
			t.Cleanup(func() {
				firstCancel(context.Canceled)
				secondCancel(context.Canceled)
				reg.Deregister(sessionID)
			})
			reg.RegisterScopedCause(sessionID, firstID, firstCancel, agent.DefaultCancellationSpec)
			reg.RegisterScopedCause(sessionID, secondID, secondCancel, agent.DefaultCancellationSpec)

			require.True(t, reg.RequestStopScoped(sessionID, firstID, tt.reason, time.Second), "policy stop should find its own running scope")
			select {
			case <-firstCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("policy stop should cancel its own process when no handle is attached")
			}
			require.Equal(t, []error{context.Canceled, nil}, []error{firstCtx.Err(), secondCtx.Err()}, "policy stop should leave the sibling process running")
			require.Equal(t, []agent.StopReason{tt.reason, agent.StopReasonNone}, []agent.StopReason{reg.StopReasonScoped(sessionID, firstID), reg.StopReasonScoped(sessionID, secondID)}, "policy stop should leave the sibling stop reason empty")

			require.True(t, reg.CancelSession(sessionID), "explicit session cancellation should find all registered scopes")
			select {
			case <-secondCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("explicit session cancellation should cancel the sibling process")
			}
			require.Equal(t, []error{context.Canceled, context.Canceled}, []error{firstCtx.Err(), secondCtx.Err()}, "explicit cancellation should stop both processes")
			require.Equal(t, []agent.StopReason{agent.StopReasonUserCancel, agent.StopReasonUserCancel}, []agent.StopReason{reg.StopReasonScoped(sessionID, firstID), reg.StopReasonScoped(sessionID, secondID)}, "explicit cancellation should upgrade the earlier policy reason on every scope")
			require.ErrorIs(t, context.Cause(secondCtx), agent.ErrUserCancelCause, "explicit cancellation should retain its user cancellation cause")
			require.True(t, reg.RequestStopScoped(sessionID, firstID, tt.reason, time.Second), "a repeated scoped stop should still find the registered scope")
			require.Equal(t, agent.StopReasonUserCancel, reg.StopReasonScoped(sessionID, firstID), "later policy stops should preserve the explicit cancellation reason")
		})
	}
}

func TestCancelRegistry_RequestStopScopedLookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		register       bool
		defaultScope   bool
		expectedFound  bool
		expectedReason agent.StopReason
	}{
		{name: "missing session"},
		{name: "missing scope", register: true},
		{name: "nil scope uses session scope", register: true, defaultScope: true, expectedFound: true, expectedReason: agent.StopReasonNoProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := agent.NewCancelRegistry(zerolog.Nop())
			sessionID := uuid.New()
			registeredCtx, registeredCancel := context.WithCancelCause(context.Background())
			t.Cleanup(func() {
				registeredCancel(context.Canceled)
				reg.Deregister(sessionID)
			})
			if tt.register {
				reg.RegisterCause(sessionID, registeredCancel, agent.DefaultCancellationSpec)
			}
			scopeID := uuid.New()
			if tt.defaultScope {
				scopeID = uuid.Nil
			}

			require.Equal(t, tt.expectedFound, reg.RequestStopScoped(sessionID, scopeID, agent.StopReasonNoProgress, time.Second), "scoped lookup should find only the requested registration")
			require.Equal(t, tt.expectedReason, reg.StopReasonScoped(sessionID, scopeID), "scoped reason should match only the requested registration")
			require.Equal(t, tt.expectedReason, reg.StopReason(sessionID), "missing scope should not mark another registration as stopped")
			if tt.expectedFound {
				select {
				case <-registeredCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("default scope should cancel the session registration")
				}
			} else {
				require.NoError(t, registeredCtx.Err(), "a missing scope should leave the registered process running")
			}
		})
	}
}
