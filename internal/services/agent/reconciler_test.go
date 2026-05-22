package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

type fakeOrphanStore struct {
	batches       [][]models.Session
	callCount     atomic.Int32
	clearErr      error
	clearRaceFor  map[string]bool // keyed by expected container_id → CAS returns cleared=false
	cleared       atomic.Int32
	clearAttempts atomic.Int32
	mu            sync.Mutex
	cursors       []uuid.UUID
	clearedIDs    []string
}

func (f *fakeOrphanStore) ListOrphanedContainers(ctx context.Context, afterID uuid.UUID) ([]models.Session, error) {
	f.mu.Lock()
	f.cursors = append(f.cursors, afterID)
	f.mu.Unlock()
	idx := int(f.callCount.Add(1)) - 1
	if idx >= len(f.batches) {
		return nil, nil
	}
	return f.batches[idx], nil
}

func (f *fakeOrphanStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, error) {
	f.clearAttempts.Add(1)
	if f.clearErr != nil {
		return false, f.clearErr
	}
	if f.clearRaceFor[expectedContainerID] {
		return false, nil
	}
	f.mu.Lock()
	f.clearedIDs = append(f.clearedIDs, expectedContainerID)
	f.mu.Unlock()
	f.cleared.Add(1)
	return true, nil
}

func newOrphanSession(containerID string) models.Session {
	cid := containerID
	return models.Session{
		ID:          uuid.New(),
		OrgID:       uuid.New(),
		ContainerID: &cid,
	}
}

func TestReconcileOrphanedContainers_NoProvider(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, nil, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, int32(0), store.callCount.Load())
}

func TestReconcileOrphanedContainers_Empty(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{batches: nil}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
}

func TestReconcileOrphanedContainers_DestroysAndClears(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{
			{newOrphanSession("c1"), newOrphanSession("c2")},
		},
	}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 2, provider.GetDestroyCalls())
	require.Equal(t, int32(2), store.cleared.Load())
}

func TestReconcileOrphanedContainers_SkipsEmptyContainerID(t *testing.T) {
	t.Parallel()
	empty := ""
	store := &fakeOrphanStore{
		batches: [][]models.Session{
			{{ID: uuid.New(), OrgID: uuid.New(), ContainerID: &empty}},
		},
	}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
}

func TestReconcileOrphanedContainers_PreservesNonTerminalAndRecoveringSessions(t *testing.T) {
	t.Parallel()

	running := newOrphanSession("running-c1")
	running.Status = models.SessionStatusRunning
	recovering := newOrphanSession("recovering-c1")
	recovering.Status = models.SessionStatusFailed
	recovering.RecoveryState = models.RecoveryStateRecovering
	store := &fakeOrphanStore{
		batches: [][]models.Session{{running, recovering}},
	}
	provider := testutil.NewMockSandboxProvider()

	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err, "reconciler should skip protected rows without failing startup")
	require.Equal(t, 0, provider.GetDestroyCalls(), "startup GC must not destroy containers for running or recovering sessions")
	require.Equal(t, int32(0), store.clearAttempts.Load(), "startup GC must not clear container ownership for running or recovering sessions")
}

func TestReconcileOrphanedContainers_DestroyFailureAfterClearLogsButContinues(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.DestroyFn = func(ctx context.Context, sb *agent.Sandbox) error {
		return errors.New("daemon flaked")
	}
	// The CAS-clear happens before destroy, so even if destroy fails the
	// DB row IS cleared. This is a deliberate trade-off: with CAS-first
	// ordering we can never destroy a container another holder has
	// acquired, but a persistently failing destroy orphans the container
	// on the host. The reconciler logs the leak so ops can prune manually.
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, int32(1), store.cleared.Load())
	require.Equal(t, 1, provider.GetDestroyCalls())
}

func TestReconcileOrphanedContainers_IsAliveErrorSkipsRow(t *testing.T) {
	t.Parallel()
	// Drop the retry backoff so this test doesn't pay ~1 second waiting
	// between IsAlive attempts. Production value unchanged.
	agent.SetIsAliveBackoffForTesting(0)
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	var aliveCalls atomic.Int32
	provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		aliveCalls.Add(1)
		return false, errors.New("inspect failed")
	}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	// Transient liveness failure → no destroy, no clear. Row waits for next
	// startup, which might see a healthier daemon.
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
	require.Equal(t, int32(0), store.clearAttempts.Load())
	// The probe must retry (reconcileIsAliveAttempts=3) before giving up —
	// a single-attempt skip was the bug we fixed.
	require.Equal(t, int32(3), aliveCalls.Load(), "reconciler must retry IsAlive before skipping the row")
}

func TestReconcileOrphanedContainers_IsAliveRetryRecovers(t *testing.T) {
	t.Parallel()
	agent.SetIsAliveBackoffForTesting(0)
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	var aliveCalls atomic.Int32
	provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		// First attempt fails transiently; second says "container gone".
		// Reconciler should proceed to CAS-clear without destroy.
		if aliveCalls.Add(1) == 1 {
			return false, errors.New("transient inspect failure")
		}
		return false, nil
	}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(1), store.cleared.Load(), "retry success must clear the row")
	require.Equal(t, int32(2), aliveCalls.Load(), "retry must succeed on the second attempt")
}

func TestReconcileOrphanedContainers_ContainerGoneClearsWithoutDestroy(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, nil
	}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(1), store.cleared.Load())
}

func TestReconcileOrphanedContainers_CASLostToNewHolderSkipsDestroy(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
		clearRaceFor: map[string]bool{
			"c1": true, // simulate: a turn or preview acquired the hold between list and clear
		},
	}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	// The CAS-clear returned cleared=false → must NOT destroy the container,
	// because the row is in active use by whatever holder slipped in.
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
	require.Equal(t, int32(1), store.clearAttempts.Load())
}

func TestReconcileOrphanedContainers_ClearErrorLeavesRowForNextStartup(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches:  [][]models.Session{{newOrphanSession("c1")}},
		clearErr: errors.New("db down"),
	}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
	require.Equal(t, int32(1), store.clearAttempts.Load())
}

func TestReconcileOrphanedContainers_CursorAdvancesPastStuckRow(t *testing.T) {
	t.Parallel()
	stuck := newOrphanSession("stuck")
	next := newOrphanSession("next")
	store := &fakeOrphanStore{
		batches: [][]models.Session{{stuck}, {next}},
	}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	// The cursor sequence must be: uuid.Nil (first page), stuck.ID (second
	// page — so we skip past the first row's id even if it had stayed
	// stuck), next.ID (third page, which returns empty and ends the loop).
	// Asserting stuck.ID appears as a cursor is the key correctness
	// property: the reconciler advances past stuck rows rather than
	// re-reading them forever.
	require.Contains(t, store.cursors, stuck.ID, "cursor must advance past the first batch's last id")
	require.Equal(t, uuid.Nil, store.cursors[0], "first list call starts at uuid.Nil")
	require.Equal(t, 2, provider.GetDestroyCalls())
}

func TestReconcileOrphanedContainers_ListError(t *testing.T) {
	t.Parallel()
	store := &errOrphanStore{err: errors.New("db down")}
	provider := testutil.NewMockSandboxProvider()
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "list orphaned containers")
}

type errOrphanStore struct{ err error }

func (e *errOrphanStore) ListOrphanedContainers(ctx context.Context, afterID uuid.UUID) ([]models.Session, error) {
	return nil, e.err
}
func (e *errOrphanStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, error) {
	return false, nil
}
