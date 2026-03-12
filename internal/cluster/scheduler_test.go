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

type mockRepos struct {
	repos []models.Repository
}

func (m *mockRepos) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error) {
	return m.repos, nil
}

type mockProjects struct {
	projects    []models.Project
	listErr     error
	updatedIDs  []uuid.UUID
	updatedNext []time.Time
}

func (m *mockProjects) ListDueForSchedule(ctx context.Context, now time.Time) ([]models.Project, error) {
	return m.projects, m.listErr
}

func (m *mockProjects) UpdateNextRunAt(ctx context.Context, orgID, projectID uuid.UUID, nextRunAt time.Time) error {
	m.updatedIDs = append(m.updatedIDs, projectID)
	m.updatedNext = append(m.updatedNext, nextRunAt)
	return nil
}

type trackingJobs struct {
	enqueued []string // jobType values
}

func (m *trackingJobs) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.enqueued = append(m.enqueued, jobType)
	return uuid.New(), nil
}

func TestNewScheduler(t *testing.T) {
	t.Parallel()

	s := NewScheduler(
		&mockSchedulerLock{},
		&mockJobs{},
		&mockOrgs{},
		&mockIntegrations{},
		&mockPlanStore{},
		&mockRepos{},
		zerolog.Nop(),
	)
	require.NotNil(t, s, "NewScheduler should return a non-nil scheduler")
	require.Nil(t, s.projects, "projects should be nil by default")
}

func TestScheduler_SetProjectStore(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	require.Nil(t, s.projects, "projects should be nil before SetProjectStore")

	ps := &mockProjects{}
	s.SetProjectStore(ps)
	require.NotNil(t, s.projects, "projects should be set after SetProjectStore")
}

func TestScheduler_ScheduleProjectCycles_ListError(t *testing.T) {
	t.Parallel()

	projects := &mockProjects{listErr: pgx.ErrNoRows}
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:     jobs,
		projects: projects,
		logger:   zerolog.Nop(),
	}

	// Should not panic, should log error and continue.
	s.scheduleProjectCycles(context.Background(), time.Now())
	require.Empty(t, jobs.enqueued, "should not enqueue any jobs on list error")
}

func TestScheduler_ScheduleProjectCycles_NilStore(t *testing.T) {
	t.Parallel()

	s := &Scheduler{
		logger: zerolog.Nop(),
	}
	// Should not panic when projects store is nil.
	s.scheduleProjectCycles(context.Background(), time.Now())
}

func TestScheduler_ScheduleProjectCycles_EnqueuesAndAdvances(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projID := uuid.New()
	now := time.Now()

	projects := &mockProjects{
		projects: []models.Project{
			{
				ID:               projID,
				OrgID:            orgID,
				ScheduleEnabled:  true,
				ScheduleInterval: 2,
				ScheduleUnit:     "days",
			},
		},
	}
	jobs := &trackingJobs{}

	s := &Scheduler{
		lock:         &mockSchedulerLock{},
		jobs:         jobs,
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{err: pgx.ErrNoRows},
		repos:        &mockRepos{},
		projects:     projects,
		logger:       zerolog.Nop(),
	}

	s.scheduleProjectCycles(context.Background(), now)

	require.Len(t, jobs.enqueued, 1, "should enqueue one job")
	require.Equal(t, "project_cycle", jobs.enqueued[0])
	require.Len(t, projects.updatedIDs, 1, "should advance next_run_at for one project")
	require.Equal(t, projID, projects.updatedIDs[0])
	// next_run_at should be 2 days from now.
	expected := now.AddDate(0, 0, 2)
	require.Equal(t, expected, projects.updatedNext[0])
}

func TestScheduler_ScheduleProjectCycles_NoDueProjects(t *testing.T) {
	t.Parallel()

	projects := &mockProjects{projects: nil}
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:     jobs,
		projects: projects,
		logger:   zerolog.Nop(),
	}

	s.scheduleProjectCycles(context.Background(), time.Now())

	require.Empty(t, jobs.enqueued, "should not enqueue any jobs")
	require.Empty(t, projects.updatedIDs, "should not update any projects")
}

func TestScheduler_ShouldRunPM_NoPlans(t *testing.T) {
	t.Parallel()

	s := &Scheduler{
		lock:         &mockSchedulerLock{},
		jobs:         &mockJobs{},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{err: pgx.ErrNoRows},
		repos:        &mockRepos{},
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
		repos:        &mockRepos{},
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
		repos:        &mockRepos{},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), now, 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.True(t, run, "should run when schedule interval elapsed")
}
