package linear

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeMilestoneJobStore struct {
	called    bool
	queue     string
	jobType   string
	payload   any
	priority  int
	dedupeKey *string
	err       error
}

func (f *fakeMilestoneJobStore) Enqueue(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	f.called = true
	f.queue = queue
	f.jobType = jobType
	f.payload = payload
	f.priority = priority
	f.dedupeKey = dedupeKey
	if f.err != nil {
		return uuid.Nil, f.err
	}
	return uuid.New(), nil
}

func TestBuildConstructsServiceDefaults(t *testing.T) {
	t.Parallel()

	svc := Build(BuildDeps{
		Logger:        zerolog.Nop(),
		Integrations:  fakeIntegrationReader{},
		Credentials:   fakeCredentialReader{},
		ClientFactory: func(context.Context, string) (Client, error) { return newFakeLinearClient(), nil },
		AppBaseURL:    "https://app.test/",
	})

	require.NotNil(t, svc, "Build should return a service")
	require.Equal(t, "https://app.test", svc.appBaseURL, "Build should trim trailing slash from app base URL")
	require.NotNil(t, svc.teamKeys, "Build should construct the team key store")
	require.NotNil(t, svc.providerState, "Build should construct the provider state store")
	require.NotNil(t, svc.stateEvents, "Build should construct the state event store")
	client, err := svc.clientFactory(context.Background(), "tok")
	require.NoError(t, err, "Build should use the injected client factory")
	require.NotNil(t, client, "Build should preserve injected client factory result")
}

func TestEnqueueMilestone(t *testing.T) {
	t.Parallel()

	t.Run("nil job store is no-op", func(t *testing.T) {
		t.Parallel()
		EnqueueMilestone(context.Background(), nil, zerolog.Nop(), uuid.New(), uuid.New(), string(MilestoneLinked), 0)
	})

	t.Run("enqueues canonical job payload", func(t *testing.T) {
		t.Parallel()
		store := &fakeMilestoneJobStore{}
		orgID := uuid.New()
		sessionID := uuid.New()

		EnqueueMilestone(context.Background(), store, zerolog.Nop(), orgID, sessionID, string(MilestonePRMerged), 42)

		require.True(t, store.called, "EnqueueMilestone should enqueue when a store is present")
		require.Equal(t, "linear", store.queue, "EnqueueMilestone should use the linear queue")
		require.Equal(t, "linear_milestone", store.jobType, "EnqueueMilestone should use the milestone job type")
		require.Equal(t, 5, store.priority, "EnqueueMilestone should use the standard priority")
		require.NotNil(t, store.dedupeKey, "EnqueueMilestone should set a dedupe key")
		require.Contains(t, *store.dedupeKey, sessionID.String(), "dedupe key should include session id")
		require.NotNil(t, store.payload, "EnqueueMilestone should include a payload")
	})

	t.Run("logs enqueue errors without returning", func(t *testing.T) {
		t.Parallel()
		store := &fakeMilestoneJobStore{err: errors.New("enqueue failed")}
		EnqueueMilestone(context.Background(), store, zerolog.Nop(), uuid.New(), uuid.New(), string(MilestoneLinked), 0)
		require.True(t, store.called, "EnqueueMilestone should attempt enqueue even when it fails")
	})
}

func TestMilestoneEnqueuerFor(t *testing.T) {
	t.Parallel()

	closure := MilestoneEnqueuerFor((*db.JobStore)(nil), zerolog.Nop())
	require.NotNil(t, closure, "MilestoneEnqueuerFor should return a closure even for nil stores")

	// Invoking the closure with a nil store must NOT panic. This exercises
	// the typed-nil-interface gotcha: passing (*db.JobStore)(nil) into
	// EnqueueMilestone's MilestoneJobEnqueuer interface parameter wraps a
	// nil pointer in a non-nil interface, so EnqueueMilestone's own
	// `if jobs == nil` guard does NOT fire — the concrete-pointer guard in
	// MilestoneEnqueuerFor is what protects callers in api-only and test
	// configurations.
	require.NotPanics(t, func() {
		closure(context.Background(), uuid.New(), uuid.New(), string(MilestoneLinked), 0)
	}, "closure must short-circuit safely when the JobStore is a nil pointer")
}
