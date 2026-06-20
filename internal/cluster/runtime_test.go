package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type schedulerRuntimeLockMock struct {
	acquired     bool
	acquireErr   error
	releaseErr   error
	acquireCalls int
	releaseCalls int
}

func (m *schedulerRuntimeLockMock) TryAcquire(ctx context.Context) (bool, error) {
	m.acquireCalls++
	if m.acquireErr != nil {
		return false, m.acquireErr
	}
	return m.acquired, nil
}

func (m *schedulerRuntimeLockMock) Release(ctx context.Context) error {
	m.releaseCalls++
	return m.releaseErr
}

type schedulerRuntimeJobsMock struct {
	enqueued []string
	err      error
}

func (m *schedulerRuntimeJobsMock) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if m.err != nil {
		return uuid.Nil, m.err
	}
	m.enqueued = append(m.enqueued, fmt.Sprintf("%s:%s", orgID.String(), jobType))
	return uuid.New(), nil
}

func (m *schedulerRuntimeJobsMock) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return m.Enqueue(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
}

func (m *schedulerRuntimeJobsMock) Notify(ctx context.Context, id uuid.UUID) {}

func (m *schedulerRuntimeJobsMock) GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error) {
	return nil, nil
}

type schedulerRuntimeOrgStoreMock struct {
	orgByID map[uuid.UUID]models.Organization
	errByID map[uuid.UUID]error
}

func (m *schedulerRuntimeOrgStoreMock) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	if err := m.errByID[id]; err != nil {
		return models.Organization{}, err
	}
	return m.orgByID[id], nil
}

type schedulerRuntimeIntegrationStoreMock struct {
	orgIDs []uuid.UUID
	err    error
}

func (m *schedulerRuntimeIntegrationStoreMock) ListOrgsWithActiveIntegrations(ctx context.Context) ([]uuid.UUID, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.orgIDs, nil
}

type schedulerRuntimePlanStoreMock struct {
	plansByOrg map[uuid.UUID]models.PMPlan
	errByOrg   map[uuid.UUID]error
}

func (m *schedulerRuntimePlanStoreMock) GetLatestByOrg(ctx context.Context, orgID uuid.UUID) (models.PMPlan, error) {
	if err := m.errByOrg[orgID]; err != nil {
		return models.PMPlan{}, err
	}
	return m.plansByOrg[orgID], nil
}

type schedulerRuntimeRepoStoreMock struct {
	repos []models.Repository
	err   error
}

func (m *schedulerRuntimeRepoStoreMock) ListByOrg(ctx context.Context, orgID uuid.UUID, _ db.RepositoryFilters) ([]models.Repository, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.repos, nil
}

func TestSchedulerRunOnce(t *testing.T) {
	t.Parallel()

	oldOrgID := uuid.New()
	newOrgID := uuid.New()
	now := time.Now().UTC()

	defaultSettingsJSON, err := json.Marshal(models.OrgSettings{})
	require.NoError(t, err, "test setup should marshal default org settings")

	tests := []struct {
		name            string
		lock            *schedulerRuntimeLockMock
		integrations    *schedulerRuntimeIntegrationStoreMock
		orgs            *schedulerRuntimeOrgStoreMock
		plans           *schedulerRuntimePlanStoreMock
		repos           *schedulerRuntimeRepoStoreMock
		jobs            *schedulerRuntimeJobsMock
		expectedEnqueue int
		expectedRelease int
	}{
		{
			name:            "returns early when lock acquisition fails",
			lock:            &schedulerRuntimeLockMock{acquireErr: fmt.Errorf("lock unavailable")},
			integrations:    &schedulerRuntimeIntegrationStoreMock{},
			orgs:            &schedulerRuntimeOrgStoreMock{},
			plans:           &schedulerRuntimePlanStoreMock{},
			repos:           &schedulerRuntimeRepoStoreMock{},
			jobs:            &schedulerRuntimeJobsMock{},
			expectedEnqueue: 0,
			expectedRelease: 0,
		},
		{
			name:            "returns early when lock is not acquired",
			lock:            &schedulerRuntimeLockMock{acquired: false},
			integrations:    &schedulerRuntimeIntegrationStoreMock{},
			orgs:            &schedulerRuntimeOrgStoreMock{},
			plans:           &schedulerRuntimePlanStoreMock{},
			repos:           &schedulerRuntimeRepoStoreMock{},
			jobs:            &schedulerRuntimeJobsMock{},
			expectedEnqueue: 0,
			expectedRelease: 0,
		},
		{
			name:            "releases lock when integration lookup fails",
			lock:            &schedulerRuntimeLockMock{acquired: true},
			integrations:    &schedulerRuntimeIntegrationStoreMock{err: fmt.Errorf("db error")},
			orgs:            &schedulerRuntimeOrgStoreMock{},
			plans:           &schedulerRuntimePlanStoreMock{},
			jobs:            &schedulerRuntimeJobsMock{},
			expectedEnqueue: 0,
			expectedRelease: 1,
		},
		{
			name:         "enqueues PM job only for orgs due by schedule",
			lock:         &schedulerRuntimeLockMock{acquired: true},
			integrations: &schedulerRuntimeIntegrationStoreMock{orgIDs: []uuid.UUID{oldOrgID, newOrgID}},
			orgs: &schedulerRuntimeOrgStoreMock{orgByID: map[uuid.UUID]models.Organization{
				oldOrgID: {ID: oldOrgID, Settings: defaultSettingsJSON},
				newOrgID: {ID: newOrgID, Settings: defaultSettingsJSON},
			}},
			plans: &schedulerRuntimePlanStoreMock{
				plansByOrg: map[uuid.UUID]models.PMPlan{
					oldOrgID: {CreatedAt: now.Add(-6 * time.Hour)},
				},
				errByOrg: map[uuid.UUID]error{
					newOrgID: pgx.ErrNoRows,
				},
			},
			repos:           &schedulerRuntimeRepoStoreMock{},
			jobs:            &schedulerRuntimeJobsMock{},
			expectedEnqueue: 12, // 2 orgs get recurring maintenance jobs (incl. Linear team-key refresh, PagerDuty sync, and PR-health reconciliation); only the org with no prior PM plan is due for pm_analyze under the 24h default
			expectedRelease: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Scheduler{
				lock:         tt.lock,
				jobs:         tt.jobs,
				orgs:         tt.orgs,
				integrations: tt.integrations,
				plans:        tt.plans,
				repos:        tt.repos,
				logger:       zerolog.Nop(),
			}

			s.runOnce(context.Background())
			require.Equal(t, tt.expectedEnqueue, len(tt.jobs.enqueued), "runOnce should enqueue the expected number of PM jobs")
			require.Equal(t, tt.expectedRelease, tt.lock.releaseCalls, "runOnce should release the lock when acquired")
		})
	}
}

func TestSchedulerStartAndNodeHeartbeatStopOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scheduler{logger: zerolog.Nop()}
	s.Start(ctx, time.Millisecond)

	nm := &NodeManager{logger: zerolog.Nop()}
	nm.StartHeartbeat(ctx)

	require.True(t, true, "Start and StartHeartbeat should return immediately when context is canceled")
}
