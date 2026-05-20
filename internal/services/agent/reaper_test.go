package agent

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reaperMockSessionLister implements StaleSessionLister for testing.
type reaperMockSessionLister struct {
	staleIdleSessions    []models.Session
	stalePendingSessions []models.Session
	staleRunningSessions []models.Session
	runtimeStalled       []models.Session
	expiredSnapshots     []models.Session
	listIdleErr          error
	listPendingErr       error
	listRunningErr       error
	listExpiredErr       error
	updateStatusErr      error
	updateFailureErr     error
	updateSandboxErr     error

	updatedStatuses  []statusUpdate
	updatedFailures  []failureUpdate
	updatedSandboxes []sandboxUpdate
	deadlineBefore   time.Time
	stopAfterBefore  time.Time
}

type statusUpdate struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	status    string
}

type failureUpdate struct {
	orgID       uuid.UUID
	sessionID   uuid.UUID
	explanation string
	category    string
	nextSteps   []string
}

type terminalizeUpdate struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	reason    string
}

type sandboxUpdate struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	state     string
}

func (m *reaperMockSessionLister) ListStaleIdleSessions(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.staleIdleSessions, m.listIdleErr
}

func (m *reaperMockSessionLister) ListStalePendingSessions(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.stalePendingSessions, m.listPendingErr
}

func (m *reaperMockSessionLister) ListStaleRunningSessions(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.staleRunningSessions, m.listRunningErr
}

func (m *reaperMockSessionLister) ListRuntimeControlStalledSessions(_ context.Context, deadlineBefore, stopAfterBefore time.Time) ([]models.Session, error) {
	m.deadlineBefore = deadlineBefore
	m.stopAfterBefore = stopAfterBefore
	return m.runtimeStalled, nil
}

func (m *reaperMockSessionLister) ListExpiredSnapshots(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.expiredSnapshots, m.listExpiredErr
}

func (m *reaperMockSessionLister) UpdateStatus(_ context.Context, orgID, sessionID uuid.UUID, status string) error {
	m.updatedStatuses = append(m.updatedStatuses, statusUpdate{orgID: orgID, sessionID: sessionID, status: status})
	return m.updateStatusErr
}

func (m *reaperMockSessionLister) UpdateFailure(_ context.Context, orgID, sessionID uuid.UUID, explanation, category string, nextSteps []string, _ bool) error {
	m.updatedFailures = append(m.updatedFailures, failureUpdate{orgID: orgID, sessionID: sessionID, explanation: explanation, category: category, nextSteps: nextSteps})
	return m.updateFailureErr
}

func (m *reaperMockSessionLister) UpdateSandboxState(_ context.Context, orgID, sessionID uuid.UUID, state string) error {
	m.updatedSandboxes = append(m.updatedSandboxes, sandboxUpdate{orgID: orgID, sessionID: sessionID, state: state})
	return m.updateSandboxErr
}

type reaperMockRuntimeJobTerminalizer struct {
	calls []terminalizeUpdate
	err   error
}

func (m *reaperMockRuntimeJobTerminalizer) TerminalizeRunningSessionJobs(_ context.Context, orgID, sessionID uuid.UUID, reason string) (int64, error) {
	m.calls = append(m.calls, terminalizeUpdate{orgID: orgID, sessionID: sessionID, reason: reason})
	return 1, m.err
}

// reaperMockThreadLister implements StuckThreadLister for testing.
type reaperMockThreadLister struct {
	stuckThreads []models.SessionThread
	listErr      error
	updateErr    error

	updatedThreadResults []threadResultUpdate
}

type threadResultUpdate struct {
	orgID    uuid.UUID
	threadID uuid.UUID
	status   models.ThreadStatus
	result   *models.SessionResult
}

func (m *reaperMockThreadLister) ListStuckRunningThreads(_ context.Context, _ time.Time) ([]models.SessionThread, error) {
	return m.stuckThreads, m.listErr
}

func (m *reaperMockThreadLister) UpdateResult(_ context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) error {
	m.updatedThreadResults = append(m.updatedThreadResults, threadResultUpdate{orgID: orgID, threadID: threadID, status: status, result: result})
	return m.updateErr
}

// reaperMockSnapshotStore implements storage.SnapshotStore for testing.
type reaperMockSnapshotStore struct {
	deletedKeys []string
	deleteErr   error
}

func (m *reaperMockSnapshotStore) Save(_ context.Context, _ string, _ io.Reader) error { return nil }
func (m *reaperMockSnapshotStore) Load(_ context.Context, _ string, _ io.Writer) error { return nil }

func (m *reaperMockSnapshotStore) Delete(_ context.Context, key string) error {
	m.deletedKeys = append(m.deletedKeys, key)
	return m.deleteErr
}

func TestReapPhase0_FailsStalePendingSessions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()

	mock := &reaperMockSessionLister{
		stalePendingSessions: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusPending)},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusPending)},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 0 should mark both sessions as failed without bumping MRU result fields.
	require.Len(t, mock.updatedStatuses, 2, "Phase 0 should mark both pending sessions failed via status-only updates")
	require.Equal(t, string(models.SessionStatusFailed), mock.updatedStatuses[0].status, "first stale pending session should be marked failed")
	require.Equal(t, sessionID1, mock.updatedStatuses[0].sessionID, "first stale pending session should be updated")
	require.Equal(t, string(models.SessionStatusFailed), mock.updatedStatuses[1].status, "second stale pending session should be marked failed")
	require.Equal(t, sessionID2, mock.updatedStatuses[1].sessionID, "second stale pending session should be updated")

	// Phase 0 should also set failure details.
	require.Len(t, mock.updatedFailures, 2)
	assert.Equal(t, FailureCategoryStuckPending, mock.updatedFailures[0].category)
	assert.Equal(t, FailureCategoryStuckPending, mock.updatedFailures[1].category)
}

func TestReapPhase0_5_FailsStaleRunningSessions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	mock := &reaperMockSessionLister{
		staleRunningSessions: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 0.5 should fail both running sessions via status-only updates.
	require.Len(t, mock.updatedStatuses, 2, "Phase 0.5 should mark both running sessions failed via status-only updates")
	require.Equal(t, string(models.SessionStatusFailed), mock.updatedStatuses[0].status, "first stale running session should be marked failed")
	require.Equal(t, sessionID1, mock.updatedStatuses[0].sessionID, "first stale running session should be updated")
	require.Equal(t, string(models.SessionStatusFailed), mock.updatedStatuses[1].status, "second stale running session should be marked failed")
	require.Equal(t, sessionID2, mock.updatedStatuses[1].sessionID, "second stale running session should be updated")

	// Phase 0.5 should also set failure details with the stuck_running category.
	require.Len(t, mock.updatedFailures, 2)
	assert.Equal(t, FailureCategoryStuckRunning, mock.updatedFailures[0].category)
	assert.Equal(t, FailureCategoryStuckRunning, mock.updatedFailures[1].category)
}

func TestReapPhase0_4_FailsRuntimeControlStalledSessions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	mock := &reaperMockSessionLister{
		runtimeStalled: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning)},
		},
	}
	snapStore := &reaperMockSnapshotStore{}
	terminalizer := &reaperMockRuntimeJobTerminalizer{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithRuntimeJobTerminalizer(terminalizer),
	)
	before := time.Now()
	reaper.reap(context.Background())
	after := time.Now()

	require.Len(t, mock.updatedStatuses, 1, "runtime-control stalled session should be marked failed")
	require.Equal(t, string(models.SessionStatusFailed), mock.updatedStatuses[0].status, "runtime-control stalled session should fail")
	require.Equal(t, sessionID, mock.updatedStatuses[0].sessionID, "runtime-control stalled session should be updated")
	require.Len(t, mock.updatedFailures, 1, "runtime-control stalled session should get failure details")
	require.Equal(t, FailureCategoryRuntimeControlStalled, mock.updatedFailures[0].category, "failure should be attributable to runtime control")
	require.Len(t, terminalizer.calls, 1, "runtime-control reaping should terminalize the session runner job")
	require.Equal(t, orgID, terminalizer.calls[0].orgID, "terminalize call should be scoped to the stalled session org")
	require.Equal(t, sessionID, terminalizer.calls[0].sessionID, "terminalize call should target the stalled session")
	require.Contains(t, terminalizer.calls[0].reason, FailureCategoryRuntimeControlStalled, "terminalize reason should preserve watchdog attribution")
	require.True(t, !mock.stopAfterBefore.Before(before) && !mock.stopAfterBefore.After(after), "stop-after cutoff should use now so per-run persisted grace deadlines are honored")
	require.True(t, mock.deadlineBefore.Before(mock.stopAfterBefore), "soft-deadline cutoff should retain watchdog slack for sessions with no persisted stop request")
}

func TestReapPhase0_5_ContinuesOnUpdateStatusError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	mock := &reaperMockSessionLister{
		staleRunningSessions: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
		},
		updateStatusErr: errors.New("db error"),
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// UpdateStatus attempted for both, but since it errored, UpdateFailure
	// should have been skipped via the `continue` branch.
	require.Len(t, mock.updatedStatuses, 2, "UpdateStatus should be attempted for all stale running sessions")
	require.Empty(t, mock.updatedFailures, "UpdateFailure should be skipped when UpdateStatus errors")
}

func TestReapPhase0_5_ContinuesOnUpdateFailureError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	mock := &reaperMockSessionLister{
		staleRunningSessions: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: &startedAt},
		},
		updateFailureErr: errors.New("db error"),
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// UpdateStatus should have succeeded for both, UpdateFailure should have
	// been attempted but logged and continued (not breaking the loop).
	require.Len(t, mock.updatedStatuses, 2, "UpdateStatus should be attempted for all stale running sessions")
	require.Len(t, mock.updatedFailures, 2, "UpdateFailure should still be called for all sessions even if it errors")
}

func TestReapPhase0_5_UsesStartedAtForElapsedLog(t *testing.T) {
	t.Parallel()

	// Covers the zero-value StartedAt branch in Phase 0.5.
	orgID := uuid.New()
	sessionID := uuid.New()

	mock := &reaperMockSessionLister{
		staleRunningSessions: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning), StartedAt: nil},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	require.Len(t, mock.updatedStatuses, 1, "UpdateStatus should be attempted for the stale running session")
	require.Len(t, mock.updatedFailures, 1)
}

func TestReapPhase0_5_ContinuesOnListError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock := &reaperMockSessionLister{
		listRunningErr: errors.New("db error"),
		staleIdleSessions: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusIdle)},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 0.5 list failed, but Phase 1 should still run.
	require.Len(t, mock.updatedStatuses, 1)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[0].status)
}

func TestReapPhase0Error_StillRunsPhase1(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock := &reaperMockSessionLister{
		listPendingErr: errors.New("db error"),
		staleIdleSessions: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusIdle)},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 0 failed, but phase 1 should still run.
	require.Len(t, mock.updatedStatuses, 1)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[0].status)
}

func TestReapPhase1_TransitionsIdleSessionsToCompleted(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	snapshotKey := "snap-key-1"

	mock := &reaperMockSessionLister{
		staleIdleSessions: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusIdle), SnapshotKey: &snapshotKey},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusIdle)},
		},
		expiredSnapshots: nil, // No expired snapshots in this test.
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 1 should update status to completed for both sessions.
	require.Len(t, mock.updatedStatuses, 2)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[0].status)
	assert.Equal(t, sessionID1, mock.updatedStatuses[0].sessionID)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[1].status)
	assert.Equal(t, sessionID2, mock.updatedStatuses[1].sessionID)

	// Phase 1 should NOT delete any snapshots.
	assert.Empty(t, snapStore.deletedKeys, "phase 1 should not delete snapshots")

	// Phase 1 should NOT update sandbox state.
	assert.Empty(t, mock.updatedSandboxes, "phase 1 should not update sandbox state")
}

func TestReapPhase2_DeletesExpiredSnapshots(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	snapshotKey1 := "snap-key-1"
	snapshotKey2 := "snap-key-2"

	mock := &reaperMockSessionLister{
		staleIdleSessions: nil, // No idle sessions in this test.
		expiredSnapshots: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey1},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey2},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 2 should delete both snapshots.
	require.Len(t, snapStore.deletedKeys, 2)
	assert.Equal(t, "snap-key-1", snapStore.deletedKeys[0])
	assert.Equal(t, "snap-key-2", snapStore.deletedKeys[1])

	// Phase 2 should update sandbox state to destroyed.
	require.Len(t, mock.updatedSandboxes, 2)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[0].state)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[1].state)

	// Phase 2 should NOT update status (sessions are already completed).
	assert.Empty(t, mock.updatedStatuses, "phase 2 should not update session status")
}

func TestReapPhase2_SkipsSessionsWithNilSnapshotKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock := &reaperMockSessionLister{
		expiredSnapshots: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: nil},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// No snapshot to delete.
	assert.Empty(t, snapStore.deletedKeys)
	// But sandbox state should still be updated.
	require.Len(t, mock.updatedSandboxes, 1)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[0].state)
}

func TestReapPhase2_SkipsSessionsWithEmptySnapshotKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	emptyKey := ""

	mock := &reaperMockSessionLister{
		expiredSnapshots: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &emptyKey},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	assert.Empty(t, snapStore.deletedKeys)
	require.Len(t, mock.updatedSandboxes, 1)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[0].state)
}

func TestReapBothPhases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	idleSessionID := uuid.New()
	expiredSessionID := uuid.New()
	snapshotKey := "expired-snap"

	mock := &reaperMockSessionLister{
		staleIdleSessions: []models.Session{
			{ID: idleSessionID, OrgID: orgID, Status: string(models.SessionStatusIdle)},
		},
		expiredSnapshots: []models.Session{
			{ID: expiredSessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 1: idle session transitioned to completed.
	require.Len(t, mock.updatedStatuses, 1)
	assert.Equal(t, idleSessionID, mock.updatedStatuses[0].sessionID)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[0].status)

	// Phase 2: expired snapshot deleted and sandbox state updated.
	require.Len(t, snapStore.deletedKeys, 1)
	assert.Equal(t, "expired-snap", snapStore.deletedKeys[0])
	require.Len(t, mock.updatedSandboxes, 1)
	assert.Equal(t, expiredSessionID, mock.updatedSandboxes[0].sessionID)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[0].state)
}

func TestReapPhase1Error_StillRunsPhase2(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotKey := "snap-key"

	mock := &reaperMockSessionLister{
		listIdleErr: errors.New("db error"),
		expiredSnapshots: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey},
		},
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 1 failed, but phase 2 should still run.
	require.Len(t, snapStore.deletedKeys, 1)
	assert.Equal(t, "snap-key", snapStore.deletedKeys[0])
	require.Len(t, mock.updatedSandboxes, 1)
}

func TestReapPhase2Error_ListExpiredSnapshots(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock := &reaperMockSessionLister{
		staleIdleSessions: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusIdle)},
		},
		listExpiredErr: errors.New("db error"),
	}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Phase 1 should still work.
	require.Len(t, mock.updatedStatuses, 1)
	assert.Equal(t, string(models.SessionStatusCompleted), mock.updatedStatuses[0].status)

	// Phase 2 had an error listing, so no snapshots deleted.
	assert.Empty(t, snapStore.deletedKeys)
	assert.Empty(t, mock.updatedSandboxes)
}

func TestReapPhase2_SnapshotDeleteError_SkipsSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID1 := uuid.New()
	sessionID2 := uuid.New()
	key1 := "key-1"
	key2 := "key-2"

	mock := &reaperMockSessionLister{
		expiredSnapshots: []models.Session{
			{ID: sessionID1, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &key1},
			{ID: sessionID2, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &key2},
		},
	}
	snapStore := &reaperMockSnapshotStore{deleteErr: errors.New("s3 error")}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())

	// Both keys attempted.
	require.Len(t, snapStore.deletedKeys, 2)
	// Sandbox state should NOT be updated because delete failed.
	assert.Empty(t, mock.updatedSandboxes)
}

// mockOrphanCloser implements OrphanCloser for testing.
type mockOrphanCloser struct {
	closed   int64
	closeErr error
	calledAt time.Time
}

func (m *mockOrphanCloser) CloseOrphans(_ context.Context, startedBefore time.Time) (int64, error) {
	m.calledAt = startedBefore
	return m.closed, m.closeErr
}

type mockUsageRoller struct {
	mu              sync.Mutex
	rolledHours     []time.Time
	rollupErrByHour map[time.Time]error
	deletedCutoffs  []time.Time
	deleteErr       error
	latestHour      time.Time
	latestHourErr   error
}

func (m *mockUsageRoller) RollupAllOrgs(_ context.Context, hour time.Time) error {
	hour = hour.UTC()
	m.mu.Lock()
	m.rolledHours = append(m.rolledHours, hour)
	m.mu.Unlock()
	if m.rollupErrByHour != nil {
		if err := m.rollupErrByHour[hour]; err != nil {
			return err
		}
	}
	return nil
}

// sortedRolledHours returns a sorted copy of rolledHours for deterministic assertions.
func (m *mockUsageRoller) sortedRolledHours() []time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	sorted := make([]time.Time, len(m.rolledHours))
	copy(sorted, m.rolledHours)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	return sorted
}

func (m *mockUsageRoller) GetLatestRollupHour(_ context.Context) (time.Time, error) {
	return m.latestHour, m.latestHourErr
}

func (m *mockUsageRoller) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.deletedCutoffs = append(m.deletedCutoffs, cutoff.UTC())
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	return 0, nil
}

func TestReapPhase3_ClosesOrphanedUsageEvents(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{
		staleIdleSessions: nil,
		expiredSnapshots:  nil,
	}
	snapStore := &reaperMockSnapshotStore{}
	orphanCloser := &mockOrphanCloser{closed: 5}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithOrphanCloser(orphanCloser),
	)
	reaper.reap(context.Background())

	// Phase 3 should have called CloseOrphans with the idle cutoff.
	assert.False(t, orphanCloser.calledAt.IsZero(), "orphan closer should have been called")
	assert.Equal(t, int64(5), orphanCloser.closed)
}

func TestReapPhase3_SkippedWhenOrphanCloserNil(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{
		staleIdleSessions: nil,
		expiredSnapshots:  nil,
	}
	snapStore := &reaperMockSnapshotStore{}

	// No orphan closer — should not panic.
	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())
}

func TestReapPhase4_CatchesUpMissedHoursFromWatermark(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}
	usageRoller := &mockUsageRoller{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithUsageRoller(usageRoller),
	)
	reaper.lastRollupHour = time.Date(2026, 4, 10, 7, 0, 0, 0, time.UTC)

	// now is 10:35 → lastCompletedHour is 09:00 (truncate to 10:00 then subtract 1h)
	reaper.reapUsageRollups(context.Background(), time.Date(2026, 4, 10, 10, 35, 0, 0, time.UTC))

	sorted := usageRoller.sortedRolledHours()
	require.Equal(t, []time.Time{
		time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC), // current hour (best-effort)
	}, sorted, "reaper should catch up every missed hour plus roll the current hour")
	require.Equal(t, time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC), reaper.lastRollupHour, "watermark should advance to lastCompletedHour, not the current hour")
}

func TestReapPhase4_BackfillsStartupWindowWhenWatermarkMissing(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}
	usageRoller := &mockUsageRoller{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithUsageRoller(usageRoller),
	)

	// now is 10:35 → lastCompletedHour is 09:00
	reaper.reapUsageRollups(context.Background(), time.Date(2026, 4, 10, 10, 35, 0, 0, time.UTC))

	sorted := usageRoller.sortedRolledHours()
	// 25 completed hours + 1 current hour (best-effort)
	require.Len(t, sorted, 26, "fresh reaper should backfill a bounded startup window plus the current hour")
	require.Equal(t, time.Date(2026, 4, 9, 9, 0, 0, 0, time.UTC), sorted[0], "startup catch-up should begin 24 hours before the last completed hour")
	require.Equal(t, time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC), sorted[len(sorted)-2], "startup catch-up should end at the last completed hour")
	require.Equal(t, time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC), sorted[len(sorted)-1], "current hour should be rolled as best-effort")
}

// TestNewSessionReaper_RaisesMaxRunningAgeBelowFloor guards the invariant
// that the reaper's stuck-running cutoff is always longer than the maximum
// possible per-org session timeout plus handler buffer. An admin who
// configures SESSION_MAX_RUNNING_AGE below that would otherwise have
// legitimate long-running sessions killed by the reaper.
func TestNewSessionReaper_RaisesMaxRunningAgeBelowFloor(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithMaxRunningAge(10*time.Minute),
	)

	// The configured 10 min is well below the floor (~2h17m), so the
	// constructor should raise it.
	assert.GreaterOrEqual(t, reaper.maxRunningAge, minRunningAgeFloor)
	// And the floor itself must exceed the max per-org timeout + buffer so
	// legitimate long runs survive.
	maxPerOrgTimeout := time.Duration(models.MaxMaxSessionDurationSeconds) * time.Second
	assert.Greater(t, reaper.maxRunningAge, maxPerOrgTimeout+HandlerCleanupBuffer,
		"reaper cutoff must exceed max-per-org-timeout + handler buffer")
}

func TestNewSessionReaper_KeepsMaxRunningAgeAboveFloor(t *testing.T) {
	t.Parallel()

	mock := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	configured := 4 * time.Hour // well above the floor
	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithMaxRunningAge(configured),
	)

	assert.Equal(t, configured, reaper.maxRunningAge,
		"configured value above the floor should be preserved verbatim")
}

// reaperMockPreviewStopper records StopActivePreviewForSession calls so tests
// can verify the reaper drives preview teardown before expiring snapshots.
type reaperMockPreviewStopper struct {
	mu      sync.Mutex
	calls   []uuid.UUID
	stopped bool
	err     error
}

func (m *reaperMockPreviewStopper) StopActivePreviewForSession(_ context.Context, _ uuid.UUID, sessionID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, sessionID)
	return m.stopped, m.err
}

func TestReapPhase2_StopsActivePreviewBeforeDeletingSnapshot(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotKey := "snap-with-preview"

	mock := &reaperMockSessionLister{
		expiredSnapshots: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey},
		},
	}
	snapStore := &reaperMockSnapshotStore{}
	stopper := &reaperMockPreviewStopper{stopped: true}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithPreviewStopper(stopper),
	)
	reaper.reap(context.Background())

	require.Len(t, stopper.calls, 1, "reaper must ask the preview stopper about the expired-snapshot session")
	assert.Equal(t, sessionID, stopper.calls[0])
	require.Len(t, snapStore.deletedKeys, 1)
	assert.Equal(t, snapshotKey, snapStore.deletedKeys[0])
	require.Len(t, mock.updatedSandboxes, 1)
	assert.Equal(t, string(models.SandboxStateDestroyed), mock.updatedSandboxes[0].state)
}

func TestReapPhase2_ProceedsWhenPreviewStopperErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotKey := "snap-key"

	mock := &reaperMockSessionLister{
		expiredSnapshots: []models.Session{
			{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusCompleted), SnapshotKey: &snapshotKey},
		},
	}
	snapStore := &reaperMockSnapshotStore{}
	stopper := &reaperMockPreviewStopper{err: errors.New("stop failed")}

	reaper := NewSessionReaper(mock, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithPreviewStopper(stopper),
	)
	reaper.reap(context.Background())

	require.Len(t, stopper.calls, 1)
	require.Len(t, snapStore.deletedKeys, 1, "snapshot cleanup must continue even if preview stop errors")
	require.Len(t, mock.updatedSandboxes, 1)
}

func TestReapPhase0_5b_FailsStuckRunningThreads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID1 := uuid.New()
	threadID2 := uuid.New()
	startedAt := time.Now().Add(-3 * time.Hour)

	threads := &reaperMockThreadLister{
		stuckThreads: []models.SessionThread{
			{ID: threadID1, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning, StartedAt: &startedAt},
			{ID: threadID2, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning, StartedAt: &startedAt},
		},
	}
	sessions := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(sessions, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithStuckThreadLister(threads),
	)
	reaper.reap(context.Background())

	// Both threads should be marked failed with the stuck_thread category.
	require.Len(t, threads.updatedThreadResults, 2)
	for _, u := range threads.updatedThreadResults {
		assert.Equal(t, models.ThreadStatusFailed, u.status, "stuck threads should be marked failed")
		require.NotNil(t, u.result, "result should be set so failure_explanation/category land in the row")
		require.NotNil(t, u.result.FailureCategory, "failure category should be set")
		assert.Equal(t, FailureCategoryStuckThread, *u.result.FailureCategory)
		require.NotNil(t, u.result.Error, "error message should be set")
	}
	threadIDs := []uuid.UUID{threads.updatedThreadResults[0].threadID, threads.updatedThreadResults[1].threadID}
	assert.Contains(t, threadIDs, threadID1)
	assert.Contains(t, threadIDs, threadID2)
}

func TestReapPhase0_5b_SkippedWhenNoLister(t *testing.T) {
	t.Parallel()

	sessions := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	// No WithStuckThreadLister option — Phase 0.5b should be a no-op and not
	// panic. This is the production safety property: existing deployments
	// that don't wire the option must keep working.
	reaper := NewSessionReaper(sessions, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop())
	reaper.reap(context.Background())
}

func TestReapPhase0_5b_ContinuesOnUpdateError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID1 := uuid.New()
	threadID2 := uuid.New()
	startedAt := time.Now().Add(-3 * time.Hour)

	threads := &reaperMockThreadLister{
		stuckThreads: []models.SessionThread{
			{ID: threadID1, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning, StartedAt: &startedAt},
			{ID: threadID2, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning, StartedAt: &startedAt},
		},
		updateErr: errors.New("db error"),
	}
	sessions := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	reaper := NewSessionReaper(sessions, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithStuckThreadLister(threads),
	)
	reaper.reap(context.Background())

	// Both attempts must be made even when the first errors — a single
	// transient DB blip on one row shouldn't strand siblings forever.
	require.Len(t, threads.updatedThreadResults, 2)
}

func TestReapPhase0_5b_ContinuesOnListError(t *testing.T) {
	t.Parallel()

	threads := &reaperMockThreadLister{listErr: errors.New("query failed")}
	sessions := &reaperMockSessionLister{}
	snapStore := &reaperMockSnapshotStore{}

	// A list-stage error must not panic or prevent later phases from
	// running. The phase logs and falls through.
	reaper := NewSessionReaper(sessions, snapStore, 30*time.Minute, 24*time.Hour, time.Minute, zerolog.Nop(),
		WithStuckThreadLister(threads),
	)
	reaper.reap(context.Background())

	require.Empty(t, threads.updatedThreadResults, "no rows should be touched when the list query failed")
}
