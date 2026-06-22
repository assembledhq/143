package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
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

type mockJobs struct {
	failedJob *models.LatestJobError
}

func (m *mockJobs) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (m *mockJobs) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (m *mockJobs) Notify(ctx context.Context, id uuid.UUID) {}

func (m *mockJobs) GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error) {
	return m.failedJob, nil
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

func (m *mockRepos) ListByOrg(ctx context.Context, orgID uuid.UUID, _ db.RepositoryFilters) ([]models.Repository, error) {
	return m.repos, nil
}

type trackingJobs struct {
	enqueued   []string // jobType values
	enqueuedTx []string // jobType values inserted in the scheduler tx
	queues     []string
	payloads   []any
	dedupeKeys []string
	enqueueErr error
}

func (m *trackingJobs) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.enqueued = append(m.enqueued, jobType)
	m.queues = append(m.queues, queue)
	m.payloads = append(m.payloads, payload)
	if dedupeKey != nil {
		m.dedupeKeys = append(m.dedupeKeys, *dedupeKey)
	}
	return uuid.New(), nil
}

func (m *trackingJobs) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if m.enqueueErr != nil {
		return uuid.Nil, m.enqueueErr
	}
	m.enqueuedTx = append(m.enqueuedTx, jobType)
	return uuid.New(), nil
}

func (m *trackingJobs) Notify(ctx context.Context, id uuid.UUID) {}

func (m *trackingJobs) GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error) {
	return nil, nil
}

type mockPMDocs struct {
	docs map[uuid.UUID]models.PMDocument
	errs map[uuid.UUID]error
}

func (m *mockPMDocs) GetByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) (models.PMDocument, error) {
	if err, ok := m.errs[orgID]; ok {
		return models.PMDocument{}, err
	}
	if doc, ok := m.docs[orgID]; ok {
		return doc, nil
	}
	return models.PMDocument{}, pgx.ErrNoRows
}

type mockSchedulerGitHubOrgStore struct {
	due          []models.GitHubOrgAutoJoinCandidate
	syncedBefore time.Time
	limit        int
	err          error
}

func (m *mockSchedulerGitHubOrgStore) ListEnabledAutoJoinLinksDueForRosterSync(ctx context.Context, syncedBefore time.Time, limit int) ([]models.GitHubOrgAutoJoinCandidate, error) {
	m.syncedBefore = syncedBefore
	m.limit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.due, nil
}

func TestScheduler_SetPMDocStore(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	require.Nil(t, s.pmDocs, "pmDocs should be nil before SetPMDocStore")

	ps := &mockPMDocs{}
	s.SetPMDocStore(ps)
	require.NotNil(t, s.pmDocs, "pmDocs should be set after SetPMDocStore")
}

func TestScheduler_SetSessionStore(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	require.Nil(t, s.sessions, "sessions should be nil before SetSessionStore (stranded-pending reaper disabled)")

	store := &mockSchedulerSessionStore{}
	s.SetSessionStore(store)
	require.NotNil(t, s.sessions, "sessions should be set after SetSessionStore so the reaper pass runs")
}

func TestScheduler_ScheduleContextRefreshes_NilStore(t *testing.T) {
	t.Parallel()

	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:   jobs,
		logger: zerolog.Nop(),
	}

	// Should not panic when pmDocs is nil.
	s.scheduleContextRefreshes(context.Background(), []uuid.UUID{uuid.New()}, map[uuid.UUID]models.OrgSettings{}, time.Now())
	require.Empty(t, jobs.enqueued, "should not enqueue any jobs when pmDocs is nil")
}

func TestScheduler_SchedulePullRequestReconciliation(t *testing.T) {
	t.Parallel()

	orgIDs := []uuid.UUID{uuid.New(), uuid.New()}
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:   jobs,
		logger: zerolog.Nop(),
	}

	s.schedulePullRequestReconciliation(context.Background(), orgIDs, time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC))

	require.Equal(t, []string{"reconcile_pull_request_state", "reconcile_pull_request_state"}, jobs.enqueued, "should enqueue one reconciliation job per org")
	require.Len(t, jobs.payloads, 2, "should record a payload for each reconciliation job")
	firstPayload, ok := jobs.payloads[0].(map[string]any)
	require.True(t, ok, "scheduler should enqueue reconciliation payloads as maps")
	require.Equal(t, orgIDs[0].String(), firstPayload["org_id"], "scheduler should include the target org ID in the reconciliation payload")
	require.Equal(t, pullRequestReconcileBatch, firstPayload["limit"], "scheduler should include the configured reconciliation batch size")
	require.Len(t, jobs.dedupeKeys, 2, "should compute one dedupe key per reconciliation job")
	require.Contains(t, jobs.dedupeKeys[0], "reconcile_pull_request_state:"+orgIDs[0].String()+":20260423220", "dedupe key should include the org and UTC ten-minute bucket")
}

func TestScheduler_SchedulePagerDutySync(t *testing.T) {
	t.Parallel()

	orgIDs := []uuid.UUID{uuid.New(), uuid.New()}
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:   jobs,
		logger: zerolog.Nop(),
	}

	s.schedulePagerDutySync(context.Background(), orgIDs, time.Date(2026, 6, 19, 18, 27, 0, 0, time.UTC))

	require.Equal(t, []string{models.JobTypePagerDutySync, models.JobTypePagerDutySync}, jobs.enqueued, "should enqueue one PagerDuty sync job per org")
	require.Equal(t, []string{"default", "default"}, jobs.queues, "PagerDuty sync should use the default integration worker queue")
	require.Len(t, jobs.payloads, 2, "should record a payload for each PagerDuty sync job")
	firstPayload, ok := jobs.payloads[0].(map[string]any)
	require.True(t, ok, "scheduler should enqueue PagerDuty sync payloads as maps")
	require.Equal(t, orgIDs[0].String(), firstPayload["org_id"], "scheduler should include the target org ID")
	require.Len(t, jobs.dedupeKeys, 2, "should compute one dedupe key per PagerDuty sync job")
	require.Contains(t, jobs.dedupeKeys[0], "pagerduty_sync:"+orgIDs[0].String()+":20260619182", "dedupe key should include org and UTC ten-minute bucket")
}

func TestScheduler_ScheduleGitHubOrgRosterSyncs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	orgID := uuid.New()
	store := &mockSchedulerGitHubOrgStore{
		due: []models.GitHubOrgAutoJoinCandidate{
			{
				OrgID:          orgID,
				InstallationID: 12345,
				AccountLogin:   "acme",
			},
		},
	}
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:       jobs,
		githubOrgs: store,
		logger:     zerolog.Nop(),
	}

	s.scheduleGitHubOrgRosterSyncs(context.Background(), now)

	require.Equal(t, githubOrgRosterSyncBatchSize, store.limit, "scheduler should bound the GitHub org roster reconciliation batch")
	require.Equal(t, now.Add(-githubOrgRosterSyncInterval), store.syncedBefore, "scheduler should request rosters stale by the configured interval")
	require.Equal(t, []string{models.JobTypeSyncGitHubOrgRoster}, jobs.enqueued, "scheduler should enqueue a roster sync job for each due capture")
	require.Equal(t, []string{"github"}, jobs.queues, "scheduler should send roster sync work to the GitHub queue")
	require.Equal(t, []string{"sync_github_org_roster:12345"}, jobs.dedupeKeys, "scheduler should dedupe by installation")
	payload, ok := jobs.payloads[0].(map[string]any)
	require.True(t, ok, "scheduler should enqueue GitHub roster sync payloads as maps")
	require.Equal(t, orgID.String(), payload["org_id"], "scheduler should include the owning org ID")
	require.Equal(t, int64(12345), payload["installation_id"], "scheduler should include the installation ID")
	require.Equal(t, "acme", payload["account_login"], "scheduler should include the GitHub account login")
}

func TestScheduler_ScheduleLinearTeamKeyRefresh_OncePerUTCDay(t *testing.T) {
	t.Parallel()

	orgIDs := []uuid.UUID{uuid.New(), uuid.New()}
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:   jobs,
		logger: zerolog.Nop(),
	}

	firstTick := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	secondTick := firstTick.Add(10 * time.Minute)
	nextDay := firstTick.Add(24 * time.Hour)

	s.scheduleLinearTeamKeyRefresh(context.Background(), orgIDs, firstTick)
	s.scheduleLinearTeamKeyRefresh(context.Background(), orgIDs, secondTick)
	s.scheduleLinearTeamKeyRefresh(context.Background(), orgIDs, nextDay)

	require.Equal(t,
		[]string{"refresh_linear_team_keys", "refresh_linear_team_keys", "refresh_linear_team_keys", "refresh_linear_team_keys"},
		jobs.enqueued,
		"linear team-key refresh should enqueue once per org per UTC day, not once per scheduler tick",
	)
	require.Len(t, jobs.dedupeKeys, 4, "should record one dedupe key per enqueued refresh job")
	require.Contains(t, jobs.dedupeKeys[0], "refresh_linear_team_keys:"+orgIDs[0].String()+":2026-05-08", "first day dedupe key should include org and date")
	require.Contains(t, jobs.dedupeKeys[2], "refresh_linear_team_keys:"+orgIDs[0].String()+":2026-05-09", "next day should schedule again with the new date")
}

func TestScheduler_ScheduleContextRefreshes_NoDoc(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs:   jobs,
		pmDocs: &mockPMDocs{},
		logger: zerolog.Nop(),
	}

	orgSettings := map[uuid.UUID]models.OrgSettings{
		orgID: {ContextRefreshIntervalDays: models.DefaultContextRefreshIntervalDays},
	}

	s.scheduleContextRefreshes(context.Background(), []uuid.UUID{orgID}, orgSettings, time.Now())
	require.Empty(t, jobs.enqueued, "should not enqueue jobs when no autogenerated doc exists")
}

func TestScheduler_ScheduleContextRefreshes_FreshDoc(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now()
	syncedAt := now.Add(-3 * 24 * time.Hour) // 3 days ago

	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs: jobs,
		pmDocs: &mockPMDocs{
			docs: map[uuid.UUID]models.PMDocument{
				orgID: {LastSyncedAt: &syncedAt},
			},
		},
		logger: zerolog.Nop(),
	}

	orgSettings := map[uuid.UUID]models.OrgSettings{
		orgID: {ContextRefreshIntervalDays: models.DefaultContextRefreshIntervalDays},
	}

	s.scheduleContextRefreshes(context.Background(), []uuid.UUID{orgID}, orgSettings, now)
	require.Empty(t, jobs.enqueued, "should not enqueue refresh for fresh doc")
}

func TestScheduler_ScheduleContextRefreshes_StaleDoc(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now()
	syncedAt := now.Add(-15 * 24 * time.Hour) // 15 days ago, past 14-day default

	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs: jobs,
		pmDocs: &mockPMDocs{
			docs: map[uuid.UUID]models.PMDocument{
				orgID: {LastSyncedAt: &syncedAt},
			},
		},
		logger: zerolog.Nop(),
	}

	orgSettings := map[uuid.UUID]models.OrgSettings{
		orgID: {ContextRefreshIntervalDays: models.DefaultContextRefreshIntervalDays},
	}

	s.scheduleContextRefreshes(context.Background(), []uuid.UUID{orgID}, orgSettings, now)
	require.Len(t, jobs.enqueued, 1, "should enqueue one refresh job")
	require.Equal(t, models.JobTypePMContextRefresh, jobs.enqueued[0])
}

func TestScheduler_ScheduleContextRefreshes_NilLastSyncedAt(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	jobs := &trackingJobs{}
	s := &Scheduler{
		jobs: jobs,
		pmDocs: &mockPMDocs{
			docs: map[uuid.UUID]models.PMDocument{
				orgID: {LastSyncedAt: nil},
			},
		},
		logger: zerolog.Nop(),
	}

	orgSettings := map[uuid.UUID]models.OrgSettings{
		orgID: {ContextRefreshIntervalDays: models.DefaultContextRefreshIntervalDays},
	}

	s.scheduleContextRefreshes(context.Background(), []uuid.UUID{orgID}, orgSettings, time.Now())
	require.Empty(t, jobs.enqueued, "should not enqueue refresh when LastSyncedAt is nil")
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

func TestScheduler_ShouldRunPM_RecentFailure(t *testing.T) {
	t.Parallel()

	now := time.Now()
	failedAt := now.Add(-30 * time.Minute) // failed 30 min ago, within 4h failure backoff
	s := &Scheduler{
		lock: &mockSchedulerLock{},
		jobs: &mockJobs{failedJob: &models.LatestJobError{
			JobID:     uuid.New(),
			LastError: "sandbox error",
			UpdatedAt: failedAt,
		}},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{err: pgx.ErrNoRows}, // no successful plan
		repos:        &mockRepos{},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), now, 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.False(t, run, "should not run when a recent failure exists within the failure backoff")
}

func TestScheduler_ShouldRunPM_OldFailure(t *testing.T) {
	t.Parallel()

	now := time.Now()
	failedAt := now.Add(-5 * time.Hour) // failed 5 hours ago, beyond 4h failure backoff
	s := &Scheduler{
		lock: &mockSchedulerLock{},
		jobs: &mockJobs{failedJob: &models.LatestJobError{
			JobID:     uuid.New(),
			LastError: "sandbox error",
			UpdatedAt: failedAt,
		}},
		orgs:         &mockOrgs{},
		integrations: &mockIntegrations{},
		plans:        &mockPlanStore{err: pgx.ErrNoRows}, // no successful plan
		repos:        &mockRepos{},
		logger:       zerolog.Nop(),
	}

	run, err := s.shouldRunPM(context.Background(), uuid.New(), now, 4*time.Hour)
	require.NoError(t, err, "shouldRunPM should not error")
	require.True(t, run, "should run when the failure is older than the failure backoff")
}

// --- automation scheduling ---

type mockAutomations struct {
	due           []models.Automation
	listErr       error
	inFlight      map[uuid.UUID]int
	inFlightErr   error
	advanceErr    error
	advancedIDs   []uuid.UUID
	advancedNexts []time.Time
}

func (m *mockAutomations) ListDueForSchedule(ctx context.Context, tx pgx.Tx, now time.Time) ([]models.Automation, error) {
	return m.due, m.listErr
}

func (m *mockAutomations) AdvanceNextRunAt(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID, now time.Time, nextRunAt time.Time) error {
	m.advancedIDs = append(m.advancedIDs, automationID)
	m.advancedNexts = append(m.advancedNexts, nextRunAt)
	return m.advanceErr
}

func (m *mockAutomations) CountInFlightRuns(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (int, error) {
	if m.inFlightErr != nil {
		return 0, m.inFlightErr
	}
	return m.inFlight[automationID], nil
}

type mockAutomationRuns struct {
	created     []models.AutomationRun
	createFlag  bool // value returned by CreateRunInTx
	createErr   error
	reapCount   int64
	reapErr     error
	reapCalls   int
	reapOrgIDs  []uuid.UUID
	lastThresh  time.Duration
	listOrgs    []uuid.UUID
	listOrgsErr error
}

func (m *mockAutomationRuns) CreateRunInTx(ctx context.Context, tx pgx.Tx, r *models.AutomationRun) (bool, error) {
	if m.createErr != nil {
		return false, m.createErr
	}
	if m.createFlag {
		r.ID = uuid.New()
	}
	m.created = append(m.created, *r)
	return m.createFlag, nil
}

func (m *mockAutomationRuns) ListOrgsWithStuckRuns(ctx context.Context, threshold time.Duration) ([]uuid.UUID, error) {
	return m.listOrgs, m.listOrgsErr
}

func (m *mockAutomationRuns) ReapStuckRuns(ctx context.Context, orgID uuid.UUID, threshold time.Duration) (int64, error) {
	m.reapCalls++
	m.reapOrgIDs = append(m.reapOrgIDs, orgID)
	m.lastThresh = threshold
	return m.reapCount, m.reapErr
}

// newAutomationFixture builds an interval-scheduled automation with sensible
// defaults for scheduler tests.
func newAutomationFixture() models.Automation {
	orgID := uuid.New()
	autoID := uuid.New()
	interval := 1
	unit := models.ScheduleUnitDays
	nextRun := time.Now().Add(-time.Minute)
	return models.Automation{
		ID:            autoID,
		OrgID:         orgID,
		Name:          "test",
		Goal:          "goal",
		ExecutionMode: "sequential",
		MaxConcurrent: 1,
		BaseBranch:    "main",
		ScheduleType:  models.AutomationScheduleInterval,
		IntervalValue: &interval,
		IntervalUnit:  &unit,
		Timezone:      "UTC",
		NextRunAt:     &nextRun,
		Enabled:       true,
	}
}

func TestScheduler_ScheduleAutomationRuns_NilStores(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	// Must not panic when automation stores are not wired.
	s.scheduleAutomationRuns(context.Background(), time.Now())
}

func TestScheduler_ScheduleAutomationRuns_HappyPath(t *testing.T) {
	t.Parallel()

	mockPool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockPool.Close()

	mockPool.ExpectBegin()
	mockPool.ExpectCommit()

	a := newAutomationFixture()
	automations := &mockAutomations{due: []models.Automation{a}}
	runs := &mockAutomationRuns{createFlag: true}
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:           jobs,
		automations:    automations,
		automationRuns: runs,
		pool:           mockPool,
		logger:         zerolog.Nop(),
	}

	s.scheduleAutomationRuns(context.Background(), time.Now())

	require.NoError(t, mockPool.ExpectationsWereMet(), "all pgxmock expectations should be met")
	require.Len(t, runs.created, 1, "should create one run")
	require.Equal(t, models.AutomationTriggeredBySchedule, runs.created[0].TriggeredBy)
	require.Len(t, automations.advancedIDs, 1, "should advance next_run_at once")
	require.Equal(t, a.ID, automations.advancedIDs[0])
	require.Empty(t, jobs.enqueued, "scheduled automation jobs should not be enqueued after commit")
	require.Len(t, jobs.enqueuedTx, 1, "should enqueue the job inside the scheduler transaction")
	require.Equal(t, models.JobTypeAutomationRun, jobs.enqueuedTx[0])
}

func TestScheduler_ScheduleAutomationRuns_MaxConcurrentSaturated(t *testing.T) {
	t.Parallel()

	mockPool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockPool.Close()

	mockPool.ExpectBegin()
	mockPool.ExpectCommit()

	a := newAutomationFixture()
	a.MaxConcurrent = 1
	automations := &mockAutomations{
		due:      []models.Automation{a},
		inFlight: map[uuid.UUID]int{a.ID: 1}, // saturated
	}
	runs := &mockAutomationRuns{}
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:           jobs,
		automations:    automations,
		automationRuns: runs,
		pool:           mockPool,
		logger:         zerolog.Nop(),
	}

	s.scheduleAutomationRuns(context.Background(), time.Now())

	require.NoError(t, mockPool.ExpectationsWereMet())
	require.Empty(t, runs.created, "saturated automation must not create a run row")
	require.Empty(t, jobs.enqueued, "saturated automation must not enqueue a job")
	require.Len(t, automations.advancedIDs, 1, "next_run_at should still advance so we don't busy-loop")
}

func TestScheduler_ScheduleAutomationRuns_DuplicateIdempotencySkip(t *testing.T) {
	t.Parallel()

	mockPool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockPool.Close()

	mockPool.ExpectBegin()
	mockPool.ExpectCommit()

	a := newAutomationFixture()
	automations := &mockAutomations{due: []models.Automation{a}}
	runs := &mockAutomationRuns{createFlag: false} // idempotency hit → no insert
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:           jobs,
		automations:    automations,
		automationRuns: runs,
		pool:           mockPool,
		logger:         zerolog.Nop(),
	}

	s.scheduleAutomationRuns(context.Background(), time.Now())

	require.NoError(t, mockPool.ExpectationsWereMet())
	// On duplicate we do NOT advance next_run_at — whoever inserted the row
	// already owns the advance, and re-advancing risks overwriting their value.
	require.Empty(t, automations.advancedIDs, "duplicate skip must not advance next_run_at")
	require.Empty(t, jobs.enqueued, "duplicate runs must not enqueue a job")
	require.Empty(t, jobs.enqueuedTx, "duplicate runs must not enqueue a job in tx")
}

func TestScheduler_ScheduleAutomationRuns_EnqueueErrorAbortsTick(t *testing.T) {
	t.Parallel()

	mockPool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockPool.Close()

	mockPool.ExpectBegin()
	mockPool.ExpectRollback()

	a := newAutomationFixture()
	automations := &mockAutomations{due: []models.Automation{a}}
	runs := &mockAutomationRuns{createFlag: true}
	jobs := &trackingJobs{enqueueErr: errors.New("enqueue boom")}

	s := &Scheduler{
		jobs:           jobs,
		automations:    automations,
		automationRuns: runs,
		pool:           mockPool,
		logger:         zerolog.Nop(),
	}

	s.scheduleAutomationRuns(context.Background(), time.Now())

	require.NoError(t, mockPool.ExpectationsWereMet(), "tx should roll back, not commit")
	require.Len(t, runs.created, 1, "should create the run before enqueue")
	require.Len(t, automations.advancedIDs, 1, "should advance next_run_at before enqueue")
	require.Empty(t, jobs.enqueued, "failed tx enqueue must not be retried after rollback")
}

func TestScheduler_ScheduleAutomationRuns_AdvanceErrorAbortsTick(t *testing.T) {
	t.Parallel()

	// A DB error inside the loop leaves pgx's tx aborted, so subsequent
	// queries silently no-op. The fix: on Advance failure we return without
	// commit so the tx rolls back and the next tick retries cleanly.
	mockPool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockPool.Close()

	mockPool.ExpectBegin()
	mockPool.ExpectRollback() // from deferred tx.Rollback; no Commit expected

	a := newAutomationFixture()
	b := newAutomationFixture() // a second automation that must NOT be enqueued
	automations := &mockAutomations{
		due:        []models.Automation{a, b},
		advanceErr: errors.New("advance boom"),
	}
	runs := &mockAutomationRuns{createFlag: true}
	jobs := &trackingJobs{}

	s := &Scheduler{
		jobs:           jobs,
		automations:    automations,
		automationRuns: runs,
		pool:           mockPool,
		logger:         zerolog.Nop(),
	}

	s.scheduleAutomationRuns(context.Background(), time.Now())

	require.NoError(t, mockPool.ExpectationsWereMet(), "tx should roll back, not commit")
	require.Empty(t, jobs.enqueued, "no jobs must be enqueued when the tick aborts")
}

func TestScheduler_ReapStuckAutomationRuns_CalledPerOrg(t *testing.T) {
	t.Parallel()

	// Two orgs with stuck runs — each must get its own org-scoped UPDATE so
	// the reaper can never sweep across tenants.
	orgA := uuid.New()
	orgB := uuid.New()
	runs := &mockAutomationRuns{
		reapCount: 3,
		listOrgs:  []uuid.UUID{orgA, orgB},
	}
	s := &Scheduler{
		automationRuns: runs,
		logger:         zerolog.Nop(),
	}

	s.reapStuckAutomationRuns(context.Background())

	require.Equal(t, 2, runs.reapCalls, "reaper should fire once per org")
	require.ElementsMatch(t, []uuid.UUID{orgA, orgB}, runs.reapOrgIDs, "every reap call must carry its org_id")
	require.Equal(t, stuckAutomationRunThreshold, runs.lastThresh, "reaper should pass the tuned threshold")
}

func TestScheduler_ReapStuckAutomationRuns_NilStoreNoop(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	// Must not panic when automationRuns is not wired.
	s.reapStuckAutomationRuns(context.Background())
}

func TestScheduler_ReapStuckAutomationRuns_NoStuckOrgsNoop(t *testing.T) {
	t.Parallel()

	runs := &mockAutomationRuns{} // listOrgs empty
	s := &Scheduler{
		automationRuns: runs,
		logger:         zerolog.Nop(),
	}

	s.reapStuckAutomationRuns(context.Background())
	require.Equal(t, 0, runs.reapCalls, "reaper must not run when no orgs have stuck runs")
}

func TestScheduler_ReapStuckAutomationRuns_ListOrgsError(t *testing.T) {
	t.Parallel()

	runs := &mockAutomationRuns{listOrgsErr: pgx.ErrTxClosed}
	s := &Scheduler{
		automationRuns: runs,
		logger:         zerolog.Nop(),
	}

	// Must swallow errors and never call ReapStuckRuns if listing orgs failed —
	// firing an UPDATE with a zero UUID would be a cross-tenant bug.
	s.reapStuckAutomationRuns(context.Background())
	require.Equal(t, 0, runs.reapCalls)
}

func TestScheduler_ReapStuckAutomationRuns_PerOrgReapErrorContinues(t *testing.T) {
	t.Parallel()

	orgA := uuid.New()
	orgB := uuid.New()
	runs := &mockAutomationRuns{
		reapErr:  pgx.ErrTxClosed,
		listOrgs: []uuid.UUID{orgA, orgB},
	}
	s := &Scheduler{
		automationRuns: runs,
		logger:         zerolog.Nop(),
	}

	// Must swallow per-org errors — a failed reap on one org should not skip
	// the others (crashed worker in org A shouldn't block cleanup in org B).
	s.reapStuckAutomationRuns(context.Background())
	require.Equal(t, 2, runs.reapCalls)
}

// mockSchedulerSessionStore captures the cutoff passed by the reaper so tests
// can assert that strandedPendingSnapshotThreshold was applied as a relative
// offset from `now`, not as an absolute that drifts as wall-clock advances.
type mockSchedulerSessionStore struct {
	reapCalls int
	lastAsOf  time.Time
	clearN    int64
	reapErr   error
}

func (m *mockSchedulerSessionStore) ReapStrandedPendingSnapshots(_ context.Context, olderThan time.Time) (int64, error) {
	m.reapCalls++
	m.lastAsOf = olderThan
	return m.clearN, m.reapErr
}

func TestScheduler_ReapStrandedPendingSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sessions := &mockSchedulerSessionStore{clearN: 2}
	s := &Scheduler{
		sessions: sessions,
		logger:   zerolog.Nop(),
	}

	s.reapStrandedPendingSnapshots(context.Background(), now)
	require.Equal(t, 1, sessions.reapCalls, "reaper should fire once per tick")
	require.WithinDuration(t,
		now.Add(-strandedPendingSnapshotThreshold),
		sessions.lastAsOf,
		time.Millisecond,
		"reaper must pass now - threshold so a clock skew between the scheduler and DB doesn't bias the cutoff",
	)
}

func TestScheduler_ReapStrandedPendingSnapshots_NilStoreNoop(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	// Must not panic when sessions is not wired (mode without scheduler-eligible session store).
	s.reapStrandedPendingSnapshots(context.Background(), time.Now())
}

func TestScheduler_ReapStrandedPendingSnapshots_StoreErrorSwallowed(t *testing.T) {
	t.Parallel()

	sessions := &mockSchedulerSessionStore{reapErr: pgx.ErrTxClosed}
	s := &Scheduler{
		sessions: sessions,
		logger:   zerolog.Nop(),
	}

	// A reaper failure must never propagate up the tick — other passes must
	// still run on the same tick.
	s.reapStrandedPendingSnapshots(context.Background(), time.Now())
	require.Equal(t, 1, sessions.reapCalls)
}

// --- verified-domain recheck sweep ---

type mockDomainStore struct {
	due            []models.OrganizationDomain
	listErr        error
	successIDs     []uuid.UUID
	failureIDs     []uuid.UUID
	failureReturns []struct {
		count    int
		disabled bool
	}
}

func (m *mockDomainStore) ListVerifiedDueForRecheck(ctx context.Context, checkedBefore time.Time, limit int) ([]models.OrganizationDomain, error) {
	if len(m.due) > limit {
		return m.due[:limit], m.listErr
	}
	return m.due, m.listErr
}

func (m *mockDomainStore) RecordRecheckSuccess(ctx context.Context, id uuid.UUID) error {
	m.successIDs = append(m.successIDs, id)
	return nil
}

func (m *mockDomainStore) RecordRecheckFailure(ctx context.Context, id uuid.UUID, maxFailures int) (int, bool, error) {
	m.failureIDs = append(m.failureIDs, id)
	r := m.failureReturns[len(m.failureIDs)-1]
	return r.count, r.disabled, nil
}

type mockDomainVerifier struct {
	results map[string]bool
	errs    map[string]error
}

func (m *mockDomainVerifier) Verify(ctx context.Context, domain, token string) (bool, error) {
	if err, ok := m.errs[domain]; ok {
		return false, err
	}
	return m.results[domain], nil
}

func TestRecheckVerifiedDomains(t *testing.T) {
	t.Parallel()

	okID, missingID, brokenID := uuid.New(), uuid.New(), uuid.New()
	store := &mockDomainStore{
		due: []models.OrganizationDomain{
			{ID: okID, OrgID: uuid.New(), Domain: "healthy.example", VerificationToken: "t1", Status: models.OrgDomainStatusVerified},
			{ID: missingID, OrgID: uuid.New(), Domain: "lapsed.example", VerificationToken: "t2", Status: models.OrgDomainStatusVerified},
			{ID: brokenID, OrgID: uuid.New(), Domain: "resolverdown.example", VerificationToken: "t3", Status: models.OrgDomainStatusVerified},
		},
		failureReturns: []struct {
			count    int
			disabled bool
		}{{count: 3, disabled: true}},
	}
	verifier := &mockDomainVerifier{
		results: map[string]bool{"healthy.example": true, "lapsed.example": false},
		errs:    map[string]error{"resolverdown.example": errors.New("server misbehaving")},
	}

	s := &Scheduler{logger: zerolog.Nop()}
	s.SetDomainRecheck(store, verifier, nil)
	s.recheckVerifiedDomains(context.Background(), time.Now())

	require.Equal(t, []uuid.UUID{okID}, store.successIDs, "a present TXT record resets the failure streak")
	require.Equal(t, []uuid.UUID{missingID}, store.failureIDs, "a missing record increments the streak (and can disable auto-join)")
	// brokenID appears in neither list: resolver trouble is no information,
	// so the row is left untouched for the next tick to retry.
}

func TestRecheckVerifiedDomains_NoopWhenUnwired(t *testing.T) {
	t.Parallel()

	s := &Scheduler{logger: zerolog.Nop()}
	// Must not panic with nil store/verifier (e.g. worker-only deployments).
	s.recheckVerifiedDomains(context.Background(), time.Now())
}
