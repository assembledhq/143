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
	references         []string
	reviewReferences   []string
	activePreparations []string
	listErr            error
	finalize           map[string]bool
	finalErr           error
	reviewFinalize     map[string]bool

	mu              sync.Mutex
	finalized       []string
	finalOrgID      []uuid.UUID
	reviewFinalized []string
}

func (f *fakeSandboxGCStore) FinalizeIdleCodeReviewContainer(_ context.Context, _ uuid.UUID, _ uuid.UUID, expectedContainerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewFinalized = append(f.reviewFinalized, expectedContainerID)
	return f.reviewFinalize[expectedContainerID], nil
}

func (f *fakeSandboxGCStore) ListContainerReferences(context.Context) ([]string, []string, error) {
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	all := append([]string(nil), f.references...)
	review := append([]string(nil), f.reviewReferences...)
	return all, review, nil
}

func (f *fakeSandboxGCStore) ListActiveCodeReviewPreparations(context.Context) ([]string, error) {
	return append([]string(nil), f.activePreparations...), nil
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

type fakeCodeReviewHolderExpirer struct {
	calls int
	limit int
	err   error
}

func (f *fakeCodeReviewHolderExpirer) ExpireCodeReviewHolders(_ context.Context, limit int) (int64, error) {
	f.calls++
	f.limit = limit
	if f.err != nil {
		return 0, f.err
	}
	return 2, nil
}

func TestSandboxGC_HolderExpiryErrorDoesNotBlockOrphanCleanup(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{ID: "orphan", CreatedAt: now.Add(-time.Hour)}}}
	gc := agent.NewSandboxGC(provider, &fakeSandboxGCStore{}, nil, agent.SandboxGCConfig{UnreferencedGracePeriod: time.Minute}, zerolog.Nop())
	gc.SetCodeReviewHolderExpirer(&fakeCodeReviewHolderExpirer{err: errors.New("holder query unavailable")})
	require.NoError(t, gc.ReapOnce(context.Background(), now), "holder expiry failure should not stop unrelated sandbox GC")
	require.Equal(t, []string{"orphan"}, provider.destroyedIDs(), "unreferenced container should still be reclaimed")
}

func TestSandboxGC_ExpiresCodeReviewHoldersBeforeReferenceSweep(t *testing.T) {
	t.Parallel()

	provider := &fakeSandboxGCProvider{}
	store := &fakeSandboxGCStore{}
	expirer := &fakeCodeReviewHolderExpirer{}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{}, zerolog.Nop())
	gc.SetCodeReviewHolderExpirer(expirer)
	err := gc.ReapOnce(context.Background(), time.Now())
	require.NoError(t, err, "GC should expire stale review leases before it reads live container references")
	require.Equal(t, 1, expirer.calls, "each GC pass should perform one bounded review-holder expiration batch")
	require.Equal(t, 100, expirer.limit, "review-holder expiration should be bounded per pass")
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

func TestSandboxGC_ReapStartupDestroysUnreferencedContainersImmediately(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "fresh-orphan", CreatedAt: now, Purpose: "agent_run"},
			{ID: "referenced", CreatedAt: now, Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{references: []string{"referenced"}}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapStartup(context.Background(), now)
	require.NoError(t, err, "startup sandbox GC should complete when immediate orphan cleanup succeeds")
	require.Equal(t, []string{"fresh-orphan"}, provider.destroyedIDs(), "startup sandbox GC should immediately destroy unreferenced containers present before workers accept jobs")
	require.Equal(t, []string{"fresh-orphan"}, usage.closedIDs(), "startup sandbox GC should close usage rows for immediately destroyed orphans")
}

func TestSandboxGC_ReapStartupSkipsContainersCreatedAfterGCStarted(t *testing.T) {
	t.Parallel()

	now := time.Now().Add(time.Hour).UTC()
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "post-startup", CreatedAt: now, Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapStartup(context.Background(), now.Add(time.Minute))
	require.NoError(t, err, "startup sandbox GC should complete when it sees a post-startup unreferenced container")
	require.Empty(t, provider.destroyedIDs(), "startup sandbox GC should leave containers created after this worker initialized to normal/pressure GC")
}

func TestSandboxGC_ReapForCapacityUsesPressureGrace(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "too-young", CreatedAt: now.Add(-30 * time.Second), Purpose: "agent_run"},
			{ID: "pressure-old", CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		UnreferencedGracePeriod: 30 * time.Minute,
		PressureGracePeriod:     2 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapForCapacity(context.Background(), now)
	require.NoError(t, err, "capacity-pressure sandbox GC should complete when orphan cleanup succeeds")
	require.Equal(t, []string{"pressure-old"}, provider.destroyedIDs(), "capacity-pressure sandbox GC should use the shorter pressure grace for unreferenced containers")
	require.Equal(t, []string{"pressure-old"}, usage.closedIDs(), "capacity-pressure sandbox GC should close usage rows for pressure-cleaned containers")
}

func TestSandboxGC_ReapForCapacityLimitsDestroyAttempts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{
		containers: []agent.ManagedSandboxContainer{
			{ID: "old-1", CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run"},
			{ID: "old-2", CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run"},
			{ID: "old-3", CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run"},
		},
	}
	store := &fakeSandboxGCStore{}
	usage := &fakeSandboxGCUsageCloser{}
	gc := agent.NewSandboxGC(provider, store, usage, agent.SandboxGCConfig{
		PressureGracePeriod:     2 * time.Minute,
		PressureMaxDestroy:      2,
		UnreferencedGracePeriod: 30 * time.Minute,
		HardMaxAge:              24 * time.Hour,
	}, zerolog.Nop())

	err := gc.ReapForCapacity(context.Background(), now)
	require.NoError(t, err, "capacity-pressure sandbox GC should complete after hitting the destroy attempt cap")
	require.Equal(t, []string{"old-1", "old-2"}, provider.destroyedIDs(), "capacity-pressure sandbox GC should cap destroy attempts on the admission path")
	require.Equal(t, []string{"old-1", "old-2"}, usage.closedIDs(), "capacity-pressure sandbox GC should close usage rows only for containers destroyed before the cap")
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
	require.Empty(t, store.reviewFinalized, "ordinary referenced containers should skip the review-only finalizer")
}

func TestSandboxGC_ReapOnceCleansTerminalCodeReviewContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		finalized  bool
		wantDelete bool
	}{
		{name: "terminal unheld review", finalized: true, wantDelete: true},
		{name: "review still held"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
			provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{
				ID: "review-container", OrgID: uuid.NewString(), SessionID: uuid.NewString(),
				CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run",
			}}}
			store := &fakeSandboxGCStore{
				references:       []string{"review-container"},
				reviewReferences: []string{"review-container"},
				reviewFinalize:   map[string]bool{"review-container": tt.finalized},
			}
			gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{HardMaxAge: 24 * time.Hour}, zerolog.Nop())
			err := gc.ReapOnce(context.Background(), now)
			require.NoError(t, err, "GC should finish after checking the terminal review's holder state")
			require.Equal(t, []string{"review-container"}, store.reviewFinalized, "GC should ask the database to finalize only an unheld terminal review container")
			if tt.wantDelete {
				require.Equal(t, []string{"review-container"}, provider.destroyedIDs(), "GC should destroy the terminal review container after the database CAS succeeds")
			} else {
				require.Empty(t, provider.destroyedIDs(), "GC should preserve a container still held by an active review")
			}
		})
	}
}

func TestSandboxGC_YoungReviewContainerSkipsIdleFinalization(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{
		ID: "young-review", OrgID: uuid.NewString(), SessionID: uuid.NewString(),
		CreatedAt: now.Add(-time.Minute), Purpose: "agent_run",
	}}}
	store := &fakeSandboxGCStore{
		references: []string{"young-review"}, reviewReferences: []string{"young-review"},
		reviewFinalize: map[string]bool{"young-review": true},
	}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{HardMaxAge: 24 * time.Hour}, zerolog.Nop())
	require.NoError(t, gc.ReapOnce(context.Background(), now), "GC should leave a young review workspace alone")
	require.Empty(t, store.reviewFinalized, "review workspace younger than two minutes should not be probed for finalization")
	require.Empty(t, provider.destroyedIDs(), "young review workspace should remain available for handoff")
}

func TestSandboxGC_ContainerInventoryErrorFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{
		ID: "review-container", OrgID: uuid.NewString(), SessionID: uuid.NewString(),
		CreatedAt: now.Add(-3 * time.Minute), Purpose: "agent_run",
	}}}
	store := &fakeSandboxGCStore{listErr: errors.New("inventory unavailable")}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{HardMaxAge: 24 * time.Hour}, zerolog.Nop())
	err := gc.ReapOnce(context.Background(), now)
	require.ErrorContains(t, err, "list container references", "failed inventory must stop cleanup before ownership is known")
	require.Empty(t, store.reviewFinalized, "failed inventory must not probe a review workspace")
	require.Empty(t, provider.destroyedIDs(), "failed inventory must not destroy a potentially referenced container")
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

func TestSandboxGC_PressurePreservesActiveUnpublishedReviewPreparation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	orgID, sessionID := uuid.New().String(), uuid.New().String()
	oldLease, liveLease := uuid.New().String(), uuid.New().String()
	provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{
		{ID: "orphaned-sibling", OrgID: orgID, SessionID: sessionID, Purpose: "prepare_code_review_workspace", PreparationLeaseToken: oldLease, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "preparing", OrgID: orgID, SessionID: sessionID, Purpose: "prepare_code_review_workspace", PreparationLeaseToken: liveLease, CreatedAt: now.Add(-10 * time.Minute)},
	}}
	store := &fakeSandboxGCStore{activePreparations: []string{liveLease}}
	gc := agent.NewSandboxGC(provider, store, nil, agent.SandboxGCConfig{}, zerolog.Nop())
	require.NoError(t, gc.ReapForCapacity(context.Background(), now), "pressure GC should inspect an active preparation")
	require.Equal(t, []string{"orphaned-sibling"}, provider.destroyedIDs(), "a live preparation must protect only its own container")
	provider.containers = provider.containers[1:] // Docker no longer inventories the reclaimed sibling.
	store.activePreparations = nil
	require.NoError(t, gc.ReapForCapacity(context.Background(), now), "pressure GC should inspect an expired preparation")
	require.Equal(t, []string{"orphaned-sibling", "preparing"}, provider.destroyedIDs(), "an unreferenced preparation may be reclaimed after its lease expires")
}
