package thread

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// --- Mock file event store and canceller ---

type mockFileEventStore struct {
	listBySessionFn func(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error)
}

func (m *mockFileEventStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error) {
	if m.listBySessionFn != nil {
		return m.listBySessionFn(ctx, orgID, sessionID, since)
	}
	return nil, nil
}

type mockCanceller struct {
	calls  []uuid.UUID
	reject bool
}

func (m *mockCanceller) CancelThread(threadID uuid.UUID) bool {
	m.calls = append(m.calls, threadID)
	return !m.reject
}

// markCancelTracker captures whether MarkCancelRequested was called and which
// thread received the timestamp. Used to verify CancelThread persists intent
// regardless of whether a live registry entry exists.
type markCancelTracker struct {
	*mockThreadStore
	markedThreads []uuid.UUID
}

func (m *markCancelTracker) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	m.markedThreads = append(m.markedThreads, threadID)
	return nil
}

// ----------------------------------------------------------------------------
// CancelThread
// ----------------------------------------------------------------------------

func TestService_CancelThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setup     func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller)
		expectErr error
		// expectMarked is true when MarkCancelRequested should have been
		// called against the target thread.
		expectMarked bool
		// expectSIGINT is true when the canceller's CancelThread should
		// have been invoked.
		expectSIGINT bool
	}{
		{
			name: "cancels a running thread",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectMarked: true,
			expectSIGINT: true,
		},
		{
			name: "cancels a pending thread",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusPending}, nil
				}
			},
			expectMarked: true,
			expectSIGINT: true,
		},
		{
			name: "rejects an idle thread as not cancellable",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusIdle}, nil
				}
			},
			expectErr: ErrThreadNotCancellable,
		},
		{
			name: "rejects a completed thread",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted}, nil
				}
			},
			expectErr: ErrThreadNotCancellable,
		},
		{
			name: "rejects when thread is on another session",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "wraps store lookup failures as not-found",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, errors.New("db down")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "rejects an archived thread",
			setup: func(deps *testDeps, tracker *markCancelTracker, canceller *mockCanceller) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					now := time.Now()
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning, ArchivedAt: &now}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tracker := &markCancelTracker{mockThreadStore: deps.threadStore}
			// Replace the threadStore with a wrapper that also tracks
			// MarkCancelRequested calls. We rebuild the service so the
			// service holds the wrapped reference.
			svc = NewService(tracker, deps.sessionStore, deps.messageStore, deps.logStore, deps.jobStore, svc.logger)
			canceller := &mockCanceller{}
			svc.SetCanceller(canceller)
			tt.setup(deps, tracker, canceller)

			updated, err := svc.CancelThread(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected sentinel")
				require.Equal(t, models.SessionThread{}, updated, "should not return a thread on error")
				require.Empty(t, canceller.calls, "should not SIGINT on error")
				return
			}
			require.NoError(t, err, "happy path should not error")
			require.Equal(t, tt.expectMarked, len(tracker.markedThreads) > 0, "MarkCancelRequested call expectation")
			require.Equal(t, tt.expectSIGINT, len(canceller.calls) > 0, "canceller call expectation")
			if tt.expectSIGINT {
				require.Equal(t, threadID, canceller.calls[0], "should SIGINT the right thread")
			}
		})
	}
}

func TestService_CancelThread_NoCanceller(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
	}
	// No canceller set — verifies that the durable cancel intent still
	// lands so a worker bounce can pick it up.
	_, err := svc.CancelThread(context.Background(), orgID, sessionID, threadID)
	require.NoError(t, err, "CancelThread should succeed even without a registry — the timestamp is the durable signal")
}

func TestService_CancelActiveThreads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		remote           bool
		activeThreads    []models.SessionThread
		expectedCount    int
		expectedSignals  []uuid.UUID
		expectedJobCount int
	}{
		{name: "signals all active local threads", activeThreads: makeActiveReviewThreads(), expectedCount: 2, expectedSignals: reviewThreadIDs(), expectedJobCount: 0},
		{name: "queues one batch for remote threads", remote: true, activeThreads: makeActiveReviewThreads(), expectedCount: 2, expectedSignals: reviewThreadIDs(), expectedJobCount: 1},
		{name: "does nothing when no threads are active", activeThreads: []models.SessionThread{}, expectedCount: 0, expectedSignals: []uuid.UUID{}, expectedJobCount: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.MustParse("00000000-0000-0000-0000-000000000143")
			sessionID := uuid.MustParse("00000000-0000-0000-0000-000000000144")
			workerNodeID := "worker-1"
			containerID := "container-1"
			svc, deps := newTestService(t)
			deps.sessionStore.listByIDsFn = func(_ context.Context, gotOrgID uuid.UUID, gotSessionIDs []uuid.UUID) ([]models.Session, error) {
				require.Equal(t, orgID, gotOrgID, "session lookup should use the review organization")
				require.Equal(t, []uuid.UUID{sessionID}, gotSessionIDs, "session lookup should use the stale review session")
				return []models.Session{{ID: sessionID, OrgID: orgID, WorkerNodeID: &workerNodeID, ContainerID: &containerID}}, nil
			}
			deps.threadStore.markSessionCancelFn = func(_ context.Context, gotOrgID uuid.UUID, gotSessionIDs []uuid.UUID) ([]models.SessionThread, error) {
				require.Equal(t, orgID, gotOrgID, "batch cancellation should use the review organization")
				require.Equal(t, []uuid.UUID{sessionID}, gotSessionIDs, "batch cancellation should use the stale review session")
				threads := append([]models.SessionThread(nil), tt.activeThreads...)
				for i := range threads {
					threads[i].OrgID = orgID
					threads[i].SessionID = sessionID
				}
				return threads, nil
			}
			var jobs []db.EnqueueOpts
			deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
				require.Equal(t, orgID, gotOrgID, "remote cancellation should use the review organization")
				jobs = append(jobs, opts)
				return uuid.New(), nil
			}
			canceller := &mockCanceller{calls: []uuid.UUID{}, reject: tt.remote}
			svc.SetCanceller(canceller)

			count, err := svc.CancelActiveThreads(context.Background(), orgID, []uuid.UUID{sessionID})

			require.NoError(t, err, "active review thread cancellation should succeed")
			require.Equal(t, tt.expectedCount, count, "cancellation should report each active thread")
			require.Equal(t, tt.expectedSignals, canceller.calls, "cancellation should signal each active thread once")
			require.Len(t, jobs, tt.expectedJobCount, "remote threads should use one batch cancellation job")
			if tt.expectedJobCount == 0 {
				return
			}
			require.Equal(t, cancelThreadJobType, jobs[0].JobType, "remote cancellation should use the existing worker job")
			require.Equal(t, 9, jobs[0].Priority, "remote cancellation should have high priority")
			require.Equal(t, &workerNodeID, jobs[0].TargetNodeID, "remote cancellation should target the session worker")
			require.Equal(t, map[string]any{
				"session_id":  sessionID.String(),
				"session_ids": []string{sessionID.String()},
				"thread_ids":  []string{reviewThreadIDs()[0].String(), reviewThreadIDs()[1].String()},
				"org_id":      orgID.String(),
			}, jobs[0].Payload, "remote cancellation should include all active thread IDs")
		})
	}
}

func TestService_CancelActiveThreadsBatchesSupersededSessionsByWorker(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000150")
	firstSessionID := uuid.MustParse("00000000-0000-0000-0000-000000000151")
	secondSessionID := uuid.MustParse("00000000-0000-0000-0000-000000000152")
	firstThreadID := uuid.MustParse("00000000-0000-0000-0000-000000000153")
	secondThreadID := uuid.MustParse("00000000-0000-0000-0000-000000000154")
	workerNodeID := "worker-1"
	containerID := "container-1"
	svc, deps := newTestService(t)
	deps.sessionStore.listByIDsFn = func(_ context.Context, gotOrgID uuid.UUID, gotSessionIDs []uuid.UUID) ([]models.Session, error) {
		require.Equal(t, orgID, gotOrgID, "session batch lookup should remain organization scoped")
		require.Equal(t, []uuid.UUID{firstSessionID, secondSessionID}, gotSessionIDs, "session batch lookup should include every superseded review")
		return []models.Session{
			{ID: firstSessionID, OrgID: orgID, WorkerNodeID: &workerNodeID, ContainerID: &containerID},
			{ID: secondSessionID, OrgID: orgID, WorkerNodeID: &workerNodeID, ContainerID: &containerID},
		}, nil
	}
	deps.threadStore.markSessionCancelFn = func(_ context.Context, gotOrgID uuid.UUID, gotSessionIDs []uuid.UUID) ([]models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "thread batch update should remain organization scoped")
		require.Equal(t, []uuid.UUID{firstSessionID, secondSessionID}, gotSessionIDs, "thread batch update should include every superseded review")
		return []models.SessionThread{
			{ID: firstThreadID, OrgID: orgID, SessionID: firstSessionID, Status: models.ThreadStatusRunning},
			{ID: secondThreadID, OrgID: orgID, SessionID: secondSessionID, Status: models.ThreadStatusPending},
		}, nil
	}
	var jobs []db.EnqueueOpts
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
		require.Equal(t, orgID, gotOrgID, "worker cancellation job should remain organization scoped")
		jobs = append(jobs, opts)
		return uuid.New(), nil
	}
	svc.SetCanceller(&mockCanceller{reject: true})

	count, err := svc.CancelActiveThreads(context.Background(), orgID, []uuid.UUID{secondSessionID, firstSessionID})

	require.NoError(t, err, "multi-review cancellation should succeed")
	require.Equal(t, 2, count, "multi-review cancellation should report every active thread")
	require.Len(t, jobs, 1, "sessions on the same worker should use one cancellation job")
	require.Equal(t, &workerNodeID, jobs[0].TargetNodeID, "cancellation job should target the owning worker")
	require.Equal(t, map[string]any{
		"session_ids": []string{firstSessionID.String(), secondSessionID.String()},
		"thread_ids":  []string{firstThreadID.String(), secondThreadID.String()},
		"org_id":      orgID.String(),
	}, jobs[0].Payload, "worker cancellation job should contain every superseded session and thread")
}

func reviewThreadIDs() []uuid.UUID {
	return []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000145"),
		uuid.MustParse("00000000-0000-0000-0000-000000000146"),
	}
}

func makeActiveReviewThreads() []models.SessionThread {
	ids := reviewThreadIDs()
	return []models.SessionThread{
		{ID: ids[0], Status: models.ThreadStatusRunning},
		{ID: ids[1], Status: models.ThreadStatusPending},
	}
}

// ----------------------------------------------------------------------------
// ListFileEvents
// ----------------------------------------------------------------------------

func TestService_ListFileEvents_PassesSinceThrough(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	since := time.Now().Add(-1 * time.Hour).UTC()

	svc, deps := newTestService(t)
	fileEvents := &mockFileEventStore{}
	svc.SetFileEventStore(fileEvents)

	var capturedSince *time.Time
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}
	fileEvents.listBySessionFn = func(_ context.Context, _, _ uuid.UUID, s *time.Time) ([]models.SessionThreadFileEvent, error) {
		capturedSince = s
		return []models.SessionThreadFileEvent{}, nil
	}

	_, err := svc.ListFileEvents(context.Background(), orgID, sessionID, &since)
	require.NoError(t, err)
	require.NotNil(t, capturedSince, "since should be forwarded to the store")
	require.True(t, capturedSince.Equal(since), "since timestamp must be preserved exactly")
}

func TestService_ListFileEvents_NoFileEventStore(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	events, err := svc.ListFileEvents(context.Background(), uuid.New(), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, events)
}

// ----------------------------------------------------------------------------
// ForkThread
// ----------------------------------------------------------------------------

func TestService_ForkThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Label: "Codex"}, nil
	}
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}

	var capturedJobType, capturedQueue string
	var capturedPayload any
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, _ *string) (uuid.UUID, error) {
		capturedQueue = queue
		capturedJobType = jobType
		capturedPayload = payload
		return uuid.New(), nil
	}

	result, err := svc.ForkThread(context.Background(), ForkInput{
		SourceSessionID: sessionID,
		SourceThreadID:  threadID,
		OrgID:           orgID,
		UserID:          &userID,
		Label:           "Risky branch",
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, result.JobID, "fork should return a job ID")
	require.Equal(t, "agent", capturedQueue)
	require.Equal(t, "fork_session_thread", capturedJobType)

	payload, ok := capturedPayload.(map[string]any)
	require.True(t, ok, "payload should be a string-keyed map")
	require.Equal(t, sessionID.String(), payload["source_session_id"])
	require.Equal(t, threadID.String(), payload["source_thread_id"])
	require.Equal(t, orgID.String(), payload["org_id"])
	require.Equal(t, userID.String(), payload["user_id"])
	require.Equal(t, "Risky branch", payload["label"])
}

func TestService_ForkThread_ThreadNotFound(t *testing.T) {
	t.Parallel()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, errors.New("missing")
	}
	_, err := svc.ForkThread(context.Background(), ForkInput{
		SourceSessionID: uuid.New(),
		SourceThreadID:  uuid.New(),
		OrgID:           uuid.New(),
	})
	require.ErrorIs(t, err, ErrThreadNotFound)
}

func TestService_ForkThread_ThreadOnDifferentSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	otherSession := uuid.New()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: otherSession, OrgID: orgID}, nil
	}
	_, err := svc.ForkThread(context.Background(), ForkInput{
		SourceSessionID: sessionID,
		SourceThreadID:  threadID,
		OrgID:           orgID,
	})
	require.ErrorIs(t, err, ErrThreadNotFound, "thread on a different session must be treated as not found for this caller")
}

func TestService_ForkThread_EnqueueFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
	}
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
		return uuid.Nil, fmt.Errorf("queue down")
	}
	_, err := svc.ForkThread(context.Background(), ForkInput{
		SourceSessionID: sessionID,
		SourceThreadID:  threadID,
		OrgID:           orgID,
	})
	require.ErrorIs(t, err, ErrEnqueueFailed)
}

// ----------------------------------------------------------------------------
// RevertThread
// ----------------------------------------------------------------------------

func TestService_RevertThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	diff := "diff --git a/foo b/foo\n--- a/foo\n+++ b/foo\n"

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Diff: &diff}, nil
	}
	var capturedJobType string
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, jobType string, _ any, _ int, _ *string) (uuid.UUID, error) {
		capturedJobType = jobType
		return uuid.New(), nil
	}
	result, err := svc.RevertThread(context.Background(), orgID, sessionID, threadID, nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, result.JobID)
	require.Equal(t, "revert_session_thread", capturedJobType)
}

func TestService_RevertThread_NoDiff(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	empty := "  "

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Diff: &empty}, nil
	}
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
		t.Fatal("RevertThread must not enqueue when there is no diff to revert")
		return uuid.Nil, nil
	}
	_, err := svc.RevertThread(context.Background(), orgID, sessionID, threadID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no diff", "error should call out the empty-diff case")
}

func TestService_RevertThread_ThreadOnDifferentSession(t *testing.T) {
	t.Parallel()

	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		// SessionID intentionally differs from the caller's argument.
		return models.SessionThread{ID: uuid.New(), SessionID: uuid.New(), OrgID: uuid.New()}, nil
	}
	_, err := svc.RevertThread(context.Background(), uuid.New(), uuid.New(), uuid.New(), nil)
	require.ErrorIs(t, err, ErrThreadNotFound)
}
