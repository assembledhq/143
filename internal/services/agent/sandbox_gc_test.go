package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

type fakeSandboxGCProvider struct {
	containers []agent.ManagedSandboxContainer
	listErr    error
	destroyErr error

	mu        sync.Mutex
	destroyed []string
}

func (f *fakeSandboxGCProvider) ListManagedSandboxes(context.Context) ([]agent.ManagedSandboxContainer, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeSandboxGCProvider) Destroy(_ context.Context, sb *agent.Sandbox) error {
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, sb.ID)
	return nil
}

func (f *fakeSandboxGCProvider) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.destroyed))
	copy(out, f.destroyed)
	return out
}

type fakeSandboxGCStore struct {
	references []string
	listErr    error
	finalize   map[string]bool
	finalErr   error

	mu         sync.Mutex
	finalized  []string
	finalOrgID []uuid.UUID
}

func (f *fakeSandboxGCStore) ListReferencedContainerIDs(context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]string, len(f.references))
	copy(out, f.references)
	return out, nil
}

func (f *fakeSandboxGCStore) FinalizeContainerDestroy(_ context.Context, orgID, _ uuid.UUID, expectedContainerID string) (bool, error) {
	if f.finalErr != nil {
		return false, f.finalErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalized = append(f.finalized, expectedContainerID)
	f.finalOrgID = append(f.finalOrgID, orgID)
	return f.finalize[expectedContainerID], nil
}

func (f *fakeSandboxGCStore) finalizedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.finalized))
	copy(out, f.finalized)
	return out
}

type fakeSandboxGCUsageCloser struct {
	err error

	mu     sync.Mutex
	closed []string
}

func (f *fakeSandboxGCUsageCloser) CloseOpenByContainerID(_ context.Context, containerID string, _ time.Time, _ string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, containerID)
	return 1, nil
}

func (f *fakeSandboxGCUsageCloser) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.closed))
	copy(out, f.closed)
	return out
}

func TestSandboxGC_ReapOnceDestroysUnreferencedContainersAfterGrace(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "young", CreatedAt: now.Add(-5 * time.Minute), Purpose: "agent_run"},
			{ID: "old", CreatedAt: now.Add(-45 * time.Minute), Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapOnce(context.Background(), now)
	require.NoError(t, err, "sandbox GC should complete when destroy and usage close succeed")
	require.Equal(t, []string{"old"}, provider.destroyedIDs(), "sandbox GC should destroy only unreferenced containers older than the grace period")
	require.Equal(t, []string{"old"}, usage.closedIDs(), "sandbox GC should close usage rows for containers it destroys")
}

func TestSandboxGC_ReapOnceKeepsReferencedContainerUntilHardMaxAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "referenced", CreatedAt: now.Add(-2 * time.Hour), Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{references: []string{"referenced"}}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapOnce(context.Background(), now)
	require.NoError(t, err, "sandbox GC should complete when referenced containers are below the hard max age")
	require.Empty(t, provider.destroyedIDs(), "sandbox GC should not destroy referenced containers below the hard max age")
	require.Empty(t, store.finalizedIDs(), "sandbox GC should not touch DB ownership for referenced containers below the hard max age")
}

func TestSandboxGC_ReapOnceExpiresReferencedContainerOnlyAfterFinalizeCAS(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	sessionID := uuid.New()
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{
				ID:        "expired",
				SessionID: sessionID.String(),
				OrgID:     orgID.String(),
				CreatedAt: now.Add(-25 * time.Hour),
				Purpose:   "agent_run",
			},
		},
	}
	store := &fakeSandboxGCStore{
		references: []string{"expired"},
		finalize:   map[string]bool{"expired": true},
	}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapOnce(context.Background(), now)
	require.NoError(t, err, "sandbox GC should complete when a hard-expired referenced container is finalized")
	require.Equal(t, []string{"expired"}, store.finalizedIDs(), "sandbox GC should CAS-finalize DB ownership before destroying a referenced hard-expired container")
	require.Equal(t, []uuid.UUID{orgID}, store.finalOrgID, "sandbox GC should parse the org label and pass it to the finalize CAS")
	require.Equal(t, []string{"expired"}, provider.destroyedIDs(), "sandbox GC should destroy a hard-expired referenced container only after finalization succeeds")
	require.Equal(t, []string{"expired"}, usage.closedIDs(), "sandbox GC should close usage rows for hard-expired containers it destroys")
}

func TestSandboxGC_ReapOnceSkipsReferencedContainerWhenFinalizeCASLoses(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	sessionID := uuid.New()
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{
				ID:        "held",
				SessionID: sessionID.String(),
				OrgID:     orgID.String(),
				CreatedAt: now.Add(-25 * time.Hour),
				Purpose:   "agent_run",
			},
		},
	}
	store := &fakeSandboxGCStore{
		references: []string{"held"},
		finalize:   map[string]bool{"held": false},
	}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapOnce(context.Background(), now)
	require.NoError(t, err, "sandbox GC should continue when finalization loses to an active holder")
	require.Equal(t, []string{"held"}, store.finalizedIDs(), "sandbox GC should attempt finalization for hard-expired referenced containers")
	require.Empty(t, provider.destroyedIDs(), "sandbox GC should not destroy a referenced container when the finalize CAS returns false")
	require.Empty(t, usage.closedIDs(), "sandbox GC should not close usage rows for containers it leaves alive")
}

func TestSandboxGC_ReapOnceReturnsListErrors(t *testing.T) {
	t.Parallel()

	provider := &fakeSandboxGCProvider{listErr: errors.New("docker unavailable")}
	store := &fakeSandboxGCStore{}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapOnce(context.Background(), time.Now())
	require.Error(t, err, "sandbox GC should surface provider list failures")
	require.Contains(t, err.Error(), "list managed sandbox containers", "sandbox GC should wrap provider list failures with context")
}
