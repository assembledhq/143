package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type mockPlanStore struct {
	plan models.PMPlan
	err  error
}

func (m *mockPlanStore) GetLatestByOrg(ctx context.Context, orgID uuid.UUID) (models.PMPlan, error) {
	return m.plan, m.err
}

type mockSchedulerLock struct{}

func (m *mockSchedulerLock) TryAcquire(ctx context.Context) (bool, error) { return true, nil }
func (m *mockSchedulerLock) Release(ctx context.Context) error            { return nil }

type mockJobs struct{}

func (m *mockJobs) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

type mockOrgs struct {
	org models.Organization
}

func (m *mockOrgs) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	return m.org, nil
}

type mockIntegrations struct {
	orgs []uuid.UUID
}

func (m *mockIntegrations) ListOrgsWithActiveIntegrations(ctx context.Context) ([]uuid.UUID, error) {
	return m.orgs, nil
}

func TestScheduler_ShouldRunPM_NoPlans(t *testing.T) {
	t.Parallel()

	s := &Scheduler{
		lock:         &mockSchedulerLock{},
		jobs:         &mockJobs{},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{err: pgx.ErrNoRows},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), time.Now(), 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.True(t, run, "should run when no plans exist")
}

func TestScheduler_ShouldRunPM_TooSoon(t *testing.T) {
	t.Parallel()

	now := time.Now()
	s := &Scheduler{
		lock:         &mockSchedulerLock{},
		jobs:         &mockJobs{},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{plan: models.PMPlan{CreatedAt: now}},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), now, 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.False(t, run, "should not run before schedule interval")
}

func TestScheduler_ShouldRunPM_Overdue(t *testing.T) {
	t.Parallel()

	now := time.Now()
	s := &Scheduler{
		lock:         &mockSchedulerLock{},
		jobs:         &mockJobs{},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{plan: models.PMPlan{CreatedAt: now.Add(-5 * time.Hour)}},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), now, 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.True(t, run, "should run when schedule interval elapsed")
}
