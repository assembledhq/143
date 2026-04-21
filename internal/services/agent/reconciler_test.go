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
	batches   [][]models.Session
	callCount atomic.Int32
	clearErr  error
	cleared   atomic.Int32
	mu        sync.Mutex
	cursors   []uuid.UUID
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

func (f *fakeOrphanStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared.Add(1)
	return nil
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

func TestReconcileOrphanedContainers_DestroyFailureLeavesRow(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.DestroyFn = func(ctx context.Context, sb *agent.Sandbox) error {
		return errors.New("daemon flaked")
	}
	// Default IsAlive returns (true, nil) so destroy runs and fails. We must
	// NOT clear in that case — the container may still be alive and a
	// subsequent startup should retry destroy rather than forget about it.
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, int32(0), store.cleared.Load())
}

func TestReconcileOrphanedContainers_IsAliveErrorSkipsRow(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, errors.New("inspect failed")
	}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	// Transient liveness failure → no destroy, no clear. Row waits for next
	// startup, which might see a healthier daemon.
	require.Equal(t, 0, provider.GetDestroyCalls())
	require.Equal(t, int32(0), store.cleared.Load())
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
func (e *errOrphanStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID) error {
	return nil
}
