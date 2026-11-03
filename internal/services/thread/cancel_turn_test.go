package thread

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type turnCancelStore struct {
	*mockThreadStore
	mark func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (bool, error)
}

func (s *turnCancelStore) MarkCancelRequestedForTurn(ctx context.Context, org, session, thread uuid.UUID, turn int) (bool, error) {
	return s.mark(ctx, org, session, thread, turn)
}

type exactTurnCanceller struct {
	*mockCanceller
	turns []int
	local bool
}

func (c *exactTurnCanceller) CancelThreadTurn(_ uuid.UUID, turn int) bool {
	c.turns = append(c.turns, turn)
	return c.local
}

func TestServiceCancelThreadTurn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		alreadyDrained bool
		local          bool
	}{
		{name: "matching local turn", local: true},
		{name: "remote turn retains queue fence"},
		{name: "later turn remains untouched", alreadyDrained: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			org, session, thread := uuid.New(), uuid.New(), uuid.New()
			current := models.SessionThread{ID: thread, OrgID: org, SessionID: session, Status: models.ThreadStatusRunning, CurrentTurn: 4}
			if tt.alreadyDrained {
				current.CurrentTurn = 5
			}
			marks := 0
			store := &turnCancelStore{mockThreadStore: deps.threadStore, mark: func(_ context.Context, gotOrg, gotSession, gotThread uuid.UUID, turn int) (bool, error) {
				require.Equal(t, []uuid.UUID{org, session, thread}, []uuid.UUID{gotOrg, gotSession, gotThread}, "atomic cancellation must scope the exact tenant, session, and thread")
				require.Equal(t, 5, turn, "expected turn must use current_turn plus one")
				if current.CurrentTurn != turn-1 {
					return false, nil
				}
				marks++
				return true, nil
			}}
			deps.threadStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) { return current, nil }
			service = NewService(store, deps.sessionStore, deps.messageStore, deps.logStore, deps.jobStore, service.logger)
			canceller := &exactTurnCanceller{mockCanceller: &mockCanceller{}, local: tt.local}
			service.SetCanceller(canceller)
			var queued []db.EnqueueOpts
			deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrg uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
				require.Equal(t, org, gotOrg, "remote interrupt must retain organization scope")
				queued = append(queued, opts)
				return uuid.New(), nil
			}
			got, err := service.CancelThreadTurn(context.Background(), org, session, thread, 5)
			if tt.alreadyDrained {
				require.ErrorIs(t, err, ErrThreadNotCancellable, "delayed cancellation must reject the newly active turn")
				require.Equal(t, 0, marks, "a later turn must not inherit the old cancellation timestamp")
				require.Empty(t, canceller.turns, "a stale request must not signal any local entry")
				require.Empty(t, queued, "a stale request must not enqueue an interrupt")
				return
			}
			require.NoError(t, err, "matching turn cancellation should be accepted")
			require.Equal(t, current, got, "accepted cancellation should return the current thread")
			require.Equal(t, 1, marks, "matching turn must receive the durable cancellation mark")
			require.Equal(t, []int{5}, canceller.turns, "local cancellation must retain the original expected turn")
			require.Empty(t, canceller.calls, "exact cancellation must never fall back to generic thread cancellation")
			if tt.local {
				require.Empty(t, queued, "accepted local interrupt needs no remote delivery")
				return
			}
			require.Len(t, queued, 1, "remote interrupt must enqueue once")
			require.Equal(t, map[string]any{"org_id": org.String(), "session_id": session.String(), "thread_id": thread.String(), "expected_turn": 5}, queued[0].Payload, "remote payload must preserve the exact turn fence")
			require.Equal(t, "cancel_thread_turn", queued[0].JobType, "old workers must reject exact-turn jobs rather than ignoring their fence")
			require.Equal(t, "cancel_thread_turn:"+thread.String()+":turn:5", *queued[0].DedupeKey, "an old queued interrupt cannot deduplicate a different turn")
		})
	}
}
