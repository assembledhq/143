package agent

import (
	"context"
	"errors"
	"io"
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
	staleIdleSessions []models.Session
	expiredSnapshots  []models.Session
	listIdleErr       error
	listExpiredErr    error
	updateStatusErr   error
	updateSandboxErr  error

	updatedStatuses  []statusUpdate
	updatedSandboxes []sandboxUpdate
}

type statusUpdate struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	status    string
}

type sandboxUpdate struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	state     string
}

func (m *reaperMockSessionLister) ListStaleIdleSessions(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.staleIdleSessions, m.listIdleErr
}

func (m *reaperMockSessionLister) ListExpiredSnapshots(_ context.Context, _ time.Time) ([]models.Session, error) {
	return m.expiredSnapshots, m.listExpiredErr
}

func (m *reaperMockSessionLister) UpdateStatus(_ context.Context, orgID, sessionID uuid.UUID, status string) error {
	m.updatedStatuses = append(m.updatedStatuses, statusUpdate{orgID: orgID, sessionID: sessionID, status: status})
	return m.updateStatusErr
}

func (m *reaperMockSessionLister) UpdateSandboxState(_ context.Context, orgID, sessionID uuid.UUID, state string) error {
	m.updatedSandboxes = append(m.updatedSandboxes, sandboxUpdate{orgID: orgID, sessionID: sessionID, state: state})
	return m.updateSandboxErr
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
	rolledHours     []time.Time
	rollupErrByHour map[time.Time]error
	deletedCutoffs  []time.Time
	deleteErr       error
}

func (m *mockUsageRoller) RollupAllOrgs(_ context.Context, hour time.Time) error {
	hour = hour.UTC()
	m.rolledHours = append(m.rolledHours, hour)
	if m.rollupErrByHour != nil {
		if err := m.rollupErrByHour[hour]; err != nil {
			return err
		}
	}
	return nil
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

	reaper.reapUsageRollups(context.Background(), time.Date(2026, 4, 10, 10, 35, 0, 0, time.UTC))

	require.Equal(t, []time.Time{
		time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
	}, usageRoller.rolledHours, "reaper should catch up every missed hour through the current hour")
	require.Equal(t, time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC), reaper.lastRollupHour, "reaper should advance the watermark after successful catch-up")
}
