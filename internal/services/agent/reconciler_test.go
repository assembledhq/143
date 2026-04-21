package agent_test

import (
	"context"
	"errors"
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
}

func (f *fakeOrphanStore) ListOrphanedContainers(ctx context.Context) ([]models.Session, error) {
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

func TestReconcileOrphanedContainers_DestroyFailureStillClears(t *testing.T) {
	t.Parallel()
	store := &fakeOrphanStore{
		batches: [][]models.Session{{newOrphanSession("c1")}},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.DestroyFn = func(ctx context.Context, sb *agent.Sandbox) error {
		return errors.New("already gone")
	}
	err := agent.ReconcileOrphanedContainers(context.Background(), store, provider, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, int32(1), store.cleared.Load())
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

func (e *errOrphanStore) ListOrphanedContainers(ctx context.Context) ([]models.Session, error) {
	return nil, e.err
}
func (e *errOrphanStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID) error {
	return nil
}
