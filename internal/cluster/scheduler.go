package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type schedulerJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	Notify(ctx context.Context, id uuid.UUID)
	GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error)
}

type schedulerOrgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

type schedulerIntegrationStore interface {
	ListOrgsWithActiveIntegrations(ctx context.Context) ([]uuid.UUID, error)
}

type schedulerPlanStore interface {
	GetLatestByOrg(ctx context.Context, orgID uuid.UUID) (models.PMPlan, error)
}

type schedulerRepoStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.RepositoryFilters) ([]models.Repository, error)
}

type schedulerAutomationStore interface {
	ListDueForSchedule(ctx context.Context, tx pgx.Tx, now time.Time) ([]models.Automation, error)
	AdvanceNextRunAt(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID, now time.Time, nextRunAt time.Time) error
	CountInFlightRuns(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (int, error)
}

type schedulerAutomationRunStore interface {
	CreateRunInTx(ctx context.Context, tx pgx.Tx, r *models.AutomationRun) (bool, error)
	ListOrgsWithStuckRuns(ctx context.Context, threshold time.Duration) ([]uuid.UUID, error)
	ReapStuckRuns(ctx context.Context, orgID uuid.UUID, threshold time.Duration) (int64, error)
}

// schedulerSessionStore is the narrow surface the scheduler needs for the
// stranded-pending-snapshot reaper. Kept as an interface so tests can inject
// a mock without depending on db.SessionStore directly.
type schedulerSessionStore interface {
	ReapStrandedPendingSnapshots(ctx context.Context, olderThan time.Time) (int64, error)
}

// stuckAutomationRunThreshold is how long a pending/running automation_run
// can sit before the reaper marks it failed. A crashed worker would otherwise
// leave the row forever and saturate max_concurrent for the parent automation.
//
// Tuned conservatively: real automation executions are expected to complete
// in minutes; anything past an hour is almost certainly a crash, not a long
// legitimate run. If legitimate long runs start to exist, raise this bound
// (or make it per-automation) rather than lower it — false-positive reaping
// is worse than a delayed retry.
const stuckAutomationRunThreshold = 1 * time.Hour

const pullRequestReconcileBatch = 50

// strandedPendingSnapshotThreshold is the wall-clock age past which a row's
// pending_snapshot_key is presumed stranded. Must comfortably exceed the
// PRService's per-upload timeout (currently 6 minutes) so a legitimately
// slow upload is never reaped out from under itself. Tune up if real uploads
// ever push past this; tune down only after the upload timeout drops too.
const strandedPendingSnapshotThreshold = 15 * time.Minute

// schedulerTxBeginner is the narrow transaction-starter surface the scheduler
// needs from a pgx pool. Declared as an interface so tests can inject a mock
// without depending on pgxpool.Pool directly.
type schedulerTxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type schedulerPMDocStore interface {
	GetByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) (models.PMDocument, error)
}

// Scheduler enqueues periodic jobs like PM analysis.
type schedulerLock interface {
	TryAcquire(ctx context.Context) (bool, error)
	Release(ctx context.Context) error
}

type Scheduler struct {
	lock           schedulerLock
	jobs           schedulerJobStore
	orgs           schedulerOrgStore
	integrations   schedulerIntegrationStore
	plans          schedulerPlanStore
	repos          schedulerRepoStore
	pmDocs         schedulerPMDocStore         // nil-safe: context refresh scheduling disabled if nil
	automations    schedulerAutomationStore    // nil-safe: automation scheduling disabled if nil
	automationRuns schedulerAutomationRunStore // nil-safe: automation scheduling disabled if nil
	sessions       schedulerSessionStore       // nil-safe: stranded-pending reaper disabled if nil
	pool           schedulerTxBeginner         // needed for automation scheduling transactions
	logger         zerolog.Logger

	lastDailyJobDates map[string]string // tracks UTC date of last daily scheduling per job type
}

func NewScheduler(
	lock schedulerLock,
	jobs schedulerJobStore,
	orgs schedulerOrgStore,
	integrations schedulerIntegrationStore,
	plans schedulerPlanStore,
	repos schedulerRepoStore,
	logger zerolog.Logger,
) *Scheduler {
	return &Scheduler{
		lock:              lock,
		jobs:              jobs,
		orgs:              orgs,
		integrations:      integrations,
		plans:             plans,
		repos:             repos,
		logger:            logger,
		lastDailyJobDates: make(map[string]string),
	}
}

// SetPMDocStore injects the PM document store for context refresh scheduling.
func (s *Scheduler) SetPMDocStore(pmDocs schedulerPMDocStore) {
	s.pmDocs = pmDocs
}

// SetAutomationStores injects the automation stores and connection pool for
// automation scheduling via the claim-and-fire loop.
func (s *Scheduler) SetAutomationStores(automations schedulerAutomationStore, runs schedulerAutomationRunStore, pool schedulerTxBeginner) {
	s.automations = automations
	s.automationRuns = runs
	s.pool = pool
}

// SetSessionStore injects the session store used by the stranded-pending
// snapshot reaper. If unset, the reaper pass is a no-op (e.g. in tests that
// don't exercise it).
func (s *Scheduler) SetSessionStore(sessions schedulerSessionStore) {
	s.sessions = sessions
}

func (s *Scheduler) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context) {
	if s.lock == nil {
		return
	}
	acquired, err := s.lock.TryAcquire(ctx)
	if err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to acquire lock")
		return
	}
	if !acquired {
		return
	}
	defer func() {
		if err := s.lock.Release(ctx); err != nil {
			s.logger.Error().Err(err).Msg("scheduler failed to release lock")
		}
	}()

	orgIDs, err := s.integrations.ListOrgsWithActiveIntegrations(ctx)
	if err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to list orgs with active integrations")
		return
	}

	now := time.Now()
	orgSettings := make(map[uuid.UUID]models.OrgSettings, len(orgIDs))
	for _, orgID := range orgIDs {
		org, err := s.orgs.GetByID(ctx, orgID)
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("scheduler failed to fetch org settings")
			continue
		}
		settings, parseErr := models.ParseOrgSettings(org.Settings)
		if parseErr != nil {
			s.logger.Warn().Err(parseErr).Str("org_id", orgID.String()).Msg("scheduler failed to parse org settings, using defaults")
		}
		orgSettings[orgID] = settings
		orgScheduleHours := settings.PMScheduleHours
		if orgScheduleHours <= 0 {
			orgScheduleHours = models.DefaultPMScheduleHours
		}

		shouldRun, err := s.shouldRunPM(ctx, orgID, now, time.Duration(orgScheduleHours)*time.Hour)
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("scheduler failed to check PM schedule")
			continue
		}
		if !shouldRun {
			continue
		}

		// Check if any repos have custom PM settings; if so, enqueue per-repo jobs.
		repos, err := s.repos.ListByOrg(ctx, orgID, db.RepositoryFilters{})
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("scheduler failed to list repos")
			repos = nil
		}

		hasCustomRepos := false
		for _, repo := range repos {
			if repo.Status != "active" {
				continue
			}
			repoSettings, repoParseErr := models.ParseRepoSettings(repo.Settings)
			if repoParseErr != nil {
				s.logger.Warn().Err(repoParseErr).Str("org_id", orgID.String()).Str("repo_id", repo.ID.String()).Msg("scheduler failed to parse repo settings, skipping")
				continue
			}
			if repoSettings.PM != nil {
				hasCustomRepos = true
				dedupeKey := fmt.Sprintf("pm_analyze:%s:%s", orgID.String(), repo.ID.String())
				payload := map[string]string{
					"org_id":  orgID.String(),
					"repo_id": repo.ID.String(),
					"trigger": string(models.PMTriggerCron),
				}
				if _, err := s.jobs.Enqueue(ctx, orgID, "default", models.JobTypePMAnalyze, payload, 5, &dedupeKey); err != nil {
					s.logger.Warn().Err(err).Str("org_id", orgID.String()).Str("repo_id", repo.ID.String()).Msg("failed to enqueue repo pm_analyze job")
				}
			}
		}

		// Enqueue sync_slack for orgs with active Slack integrations.
		slackDedupeKey := fmt.Sprintf("sync_slack:%s", orgID.String())
		slackPayload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", "sync_slack", slackPayload, 3, &slackDedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue sync_slack job")
		}

		// Enqueue an org-level job (no repo_id) for repos without custom settings,
		// or as the default when no repos have custom PM config.
		if !hasCustomRepos {
			dedupeKey := fmt.Sprintf("pm_analyze:%s", orgID.String())
			payload := map[string]string{
				"org_id":  orgID.String(),
				"trigger": string(models.PMTriggerCron),
			}
			if _, err := s.jobs.Enqueue(ctx, orgID, "default", models.JobTypePMAnalyze, payload, 5, &dedupeKey); err != nil {
				s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue pm_analyze job")
			}
		}
	}

	// Enqueue daily audit retention cleanup for each org (deduplicated per day).
	s.scheduleAuditRetentionCleanup(ctx, orgIDs, now)
	s.scheduleDataRetentionCleanup(ctx, orgIDs, now)

	// Second pass: reap stuck automation runs so a crashed worker does not
	// saturate max_concurrent forever. Runs before scheduleAutomationRuns so a
	// just-reaped automation can fire a fresh run on this same tick.
	s.reapStuckAutomationRuns(ctx)

	// Third pass: enqueue automation_run jobs for automations that are due.
	s.scheduleAutomationRuns(ctx, now)

	// Fourth pass: enqueue pm_context_refresh for orgs with stale autogenerated context.
	s.scheduleContextRefreshes(ctx, orgIDs, orgSettings, now)

	// Fifth pass: periodically reconcile stale PR health for open pull requests.
	s.schedulePullRequestReconciliation(ctx, orgIDs, now)

	// Sixth pass: clear pending_snapshot_key on sessions whose owning upload
	// goroutine died (worker OOM, drain timeout, etc). Without this, the
	// orchestrator's gate would block continue_session forever for that
	// session. Idempotent across schedulers — concurrent reapers update the
	// same rows with the same NULL value.
	s.reapStrandedPendingSnapshots(ctx, now)

	// Seventh pass: refresh per-org Linear team-key allowlist once per UTC
	// day so bare-identifier detection (e.g. "ACS-1234") picks up new teams
	// created post-install. The OAuth callback enqueues an immediate refresh,
	// so this cron is the long-term safety net for teams added later. The
	// integration's settings UI does not surface a manual refresh, so this is
	// the only path that reconciles the cache against Linear's source of
	// truth.
	s.scheduleLinearTeamKeyRefresh(ctx, orgIDs, now)
}

// scheduleLinearTeamKeyRefresh enqueues a per-org refresh_linear_team_keys
// job once per UTC day. The job's handler is idempotent (it replaces the
// linear_team_keys rows for the org's integration); the in-process date
// guard keeps the scheduler's 10-minute tick from queueing 144 redundant
// jobs/day after the first enqueue pass.
//
// Org-scoping piggybacks on the upstream ListOrgsWithActiveIntegrations call
// — orgs without a Linear integration won't appear here, so the worker never
// sees a no-op dispatch. The job itself rechecks the integration row before
// hitting Linear so a torn-down integration after enqueue still results in a
// graceful skip.
func (s *Scheduler) scheduleLinearTeamKeyRefresh(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	dateKey := now.UTC().Format("2006-01-02")
	const jobType = "refresh_linear_team_keys"
	if s.lastDailyJobDates == nil {
		s.lastDailyJobDates = make(map[string]string)
	}
	if s.lastDailyJobDates[jobType] == dateKey {
		return
	}

	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("refresh_linear_team_keys:%s:%s", orgID.String(), dateKey)
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "linear", jobType, payload, 5, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Msg("failed to enqueue refresh_linear_team_keys cron job")
		}
	}

	s.lastDailyJobDates[jobType] = dateKey
}

func (s *Scheduler) scheduleAuditRetentionCleanup(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	s.scheduleDailyCleanupJob(ctx, "audit_retention_cleanup", orgIDs, now)
}

func (s *Scheduler) scheduleDataRetentionCleanup(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	s.scheduleDailyCleanupJob(ctx, "data_retention_cleanup", orgIDs, now)
}

// scheduleDailyCleanupJob enqueues one job per org, deduplicated per UTC day.
// It avoids N redundant Enqueue calls on every scheduler tick after the first
// tick of the day.
func (s *Scheduler) scheduleDailyCleanupJob(ctx context.Context, jobType string, orgIDs []uuid.UUID, now time.Time) {
	dateKey := now.UTC().Format("2006-01-02")
	if s.lastDailyJobDates == nil {
		s.lastDailyJobDates = make(map[string]string)
	}
	if s.lastDailyJobDates[jobType] == dateKey {
		return
	}

	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("%s:%s:%s", jobType, orgID.String(), dateKey)
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", jobType, payload, 1, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msgf("failed to enqueue %s job", jobType)
		}
	}

	s.lastDailyJobDates[jobType] = dateKey
}

// scheduleContextRefreshes checks each org for a stale autogenerated context doc
// and enqueues a pm_context_refresh job if the doc is older than the configured interval.
func (s *Scheduler) scheduleContextRefreshes(ctx context.Context, orgIDs []uuid.UUID, orgSettings map[uuid.UUID]models.OrgSettings, now time.Time) {
	if s.pmDocs == nil {
		return
	}

	for _, orgID := range orgIDs {
		doc, err := s.pmDocs.GetByOrgAndSourceType(ctx, orgID, models.PMDocSourceAutogenerated)
		if err != nil {
			// No autogenerated doc — nothing to refresh.
			continue
		}
		if doc.LastSyncedAt == nil {
			continue
		}

		settings := orgSettings[orgID]
		intervalDays := settings.ContextRefreshIntervalDays
		if intervalDays <= 0 {
			intervalDays = models.DefaultContextRefreshIntervalDays
		}
		refreshInterval := time.Duration(intervalDays) * 24 * time.Hour

		if doc.LastSyncedAt.Add(refreshInterval).After(now) {
			continue // not stale yet
		}

		dedupeKey := fmt.Sprintf("pm_context_refresh:%s", orgID.String())
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", models.JobTypePMContextRefresh, payload, 3, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue pm_context_refresh job")
		}
	}
}

// reapStrandedPendingSnapshots clears pending_snapshot_key on sessions whose
// owning upload goroutine cannot finish — most often a worker hard-crash
// (OOM, container kill) that left the row with no live uploader, or a
// graceful drain that timed out. Without this pass, the orchestrator's gate
// in ContinueSession would refuse to resume those sessions forever.
//
// The reaper uses the strandedPendingSnapshotThreshold age guard, which is
// chosen to comfortably exceed PRService.postPRSnapshotUploadTimeout: a
// legitimately slow upload that's still in flight will not match.
func (s *Scheduler) reapStrandedPendingSnapshots(ctx context.Context, now time.Time) {
	if s.sessions == nil {
		return
	}
	cutoff := now.Add(-strandedPendingSnapshotThreshold)
	cleared, err := s.sessions.ReapStrandedPendingSnapshots(ctx, cutoff)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to reap stranded pending_snapshot_key rows")
		return
	}
	if cleared > 0 {
		s.logger.Warn().
			Int64("cleared", cleared).
			Dur("threshold", strandedPendingSnapshotThreshold).
			Msg("cleared stranded pending_snapshot_key rows; sessions can resume")
	}
}

func (s *Scheduler) schedulePullRequestReconciliation(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	hourBucket := now.UTC().Format("2006010215")
	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("reconcile_pull_request_state:%s:%s", orgID.String(), hourBucket)
		payload := map[string]any{
			"org_id": orgID.String(),
			"limit":  pullRequestReconcileBatch,
		}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", "reconcile_pull_request_state", payload, 2, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue reconcile_pull_request_state job")
		}
	}
}

// shouldRunPM checks whether PM analysis should be enqueued for the given org.
// It respects the configured interval for both successful plans and recent failures,
// preventing a retry storm when PM Analysis is persistently failing.
//
// NOTE: this is an org-level check. When repos have custom PM settings, a failure
// from any single repo's job will delay retries for the entire org. If per-repo
// backoff is needed in the future, failure checks should move into the per-repo loop.
func (s *Scheduler) shouldRunPM(ctx context.Context, orgID uuid.UUID, now time.Time, interval time.Duration) (bool, error) {
	plan, err := s.plans.GetLatestByOrg(ctx, orgID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	// If the latest successful plan is recent enough, skip.
	if err == nil && plan.CreatedAt.Add(interval).After(now) {
		return false, nil
	}

	// Also check the latest failed job — if it failed recently, don't retry
	// until the interval has elapsed. This prevents a storm of retries when
	// PM Analysis is persistently failing (e.g. sandbox issues).
	failedJob, err := s.jobs.GetLatestFailedByType(ctx, orgID, models.JobTypePMAnalyze)
	if err != nil {
		return false, err
	}
	// Use the full interval for failure backoff. Persistent failures (e.g. Docker
	// daemon down) won't resolve in minutes, so retrying sooner just creates noise.
	failureBackoff := interval
	if failedJob != nil && failedJob.UpdatedAt.Add(failureBackoff).After(now) {
		s.logger.Debug().
			Str("org_id", orgID.String()).
			Time("failed_at", failedJob.UpdatedAt).
			Str("error", failedJob.LastError).
			Msg("skipping PM analysis: recent failure within interval")
		return false, nil
	}

	return true, nil
}

// reapStuckAutomationRuns marks runs in pending/running past the stuck
// threshold as failed. A crashed worker would otherwise hold max_concurrent
// slots forever (CountInFlightRuns counts pending+running), blocking all
// future runs for the automation.
//
// Fans out one UPDATE per org: the reaper first lists orgs with any stuck
// runs, then issues a per-org, org-scoped UPDATE. This keeps every mutating
// query tenant-isolated at the SQL layer (defense-in-depth even though the
// scheduler is leader-elected and takes no external input) and produces
// per-org reap counts that are useful for audit/metrics.
func (s *Scheduler) reapStuckAutomationRuns(ctx context.Context) {
	if s.automationRuns == nil {
		return
	}
	orgIDs, err := s.automationRuns.ListOrgsWithStuckRuns(ctx, stuckAutomationRunThreshold)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list orgs with stuck automation runs")
		return
	}
	for _, orgID := range orgIDs {
		reaped, err := s.automationRuns.ReapStuckRuns(ctx, orgID, stuckAutomationRunThreshold)
		if err != nil {
			s.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Msg("failed to reap stuck automation runs for org")
			continue
		}
		if reaped > 0 {
			s.logger.Info().
				Str("org_id", orgID.String()).
				Int64("reaped", reaped).
				Dur("threshold", stuckAutomationRunThreshold).
				Msg("reaped stuck automation runs")
		}
	}
}

// scheduleAutomationRuns claims due automations using FOR UPDATE SKIP LOCKED,
// creates automation_run rows (with idempotency), and enqueues jobs.
//
// Ordering matters: we check max_concurrent BEFORE creating the run row so
// throttled automations don't leave behind orphan pending rows that no one
// will ever execute. Job enqueue is deferred until AFTER commit so a rolled-
// back tx can't leave dangling jobs pointing at runs that don't exist.
func (s *Scheduler) scheduleAutomationRuns(ctx context.Context, now time.Time) {
	if s.automations == nil || s.automationRuns == nil || s.pool == nil {
		return
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to begin automation tx")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	dueAutomations, err := s.automations.ListDueForSchedule(ctx, tx, now)
	if err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to list due automations")
		return
	}

	jobIDs := make([]uuid.UUID, 0, len(dueAutomations))
	for _, a := range dueAutomations {
		// Any DB error inside this loop leaves pgx's tx in an aborted state,
		// so subsequent queries on the same tx will fail. On such errors we
		// abort the whole tick (return without commit). The rolled-back row
		// lock releases, and the next tick will retry.
		inFlight, err := s.automations.CountInFlightRuns(ctx, tx, a.OrgID, a.ID)
		if err != nil {
			s.logger.Error().Err(err).
				Str("automation_id", a.ID.String()).
				Msg("failed to count in-flight runs; aborting tick")
			return
		}

		// ComputeNextRunAt branches on schedule_type so the scheduler does not
		// have to know about each schedule kind. A returned error indicates a
		// corrupt row (Create/Update validation and the DB CHECKs already
		// enforce the interval/cron XOR); skip it loudly rather than silently
		// advancing with a wrong fire time.
		nextRunAt, err := a.ComputeNextRunAt(now)
		if err != nil {
			s.logger.Error().Err(err).
				Str("automation_id", a.ID.String()).
				Str("schedule_type", a.ScheduleType).
				Msg("skipping automation: could not compute next_run_at; expected Create/Update validation to prevent this state")
			continue
		}

		if inFlight >= a.MaxConcurrent {
			s.logger.Info().
				Str("automation_id", a.ID.String()).
				Int("in_flight", inFlight).
				Int("max_concurrent", a.MaxConcurrent).
				Msg("skipping automation: max_concurrent saturated, deferring to next tick")
			if err := s.automations.AdvanceNextRunAt(ctx, tx, a.OrgID, a.ID, now, nextRunAt); err != nil {
				s.logger.Error().Err(err).
					Str("automation_id", a.ID.String()).
					Msg("failed to advance automation next_run_at; aborting tick")
				return
			}
			continue
		}

		scheduledTime := a.NextRunAt

		// BuildConfigSnapshot doesn't touch the DB, so on marshal failure we
		// can safely skip this one row without poisoning the tx.
		configSnapshot, err := a.BuildConfigSnapshot()
		if err != nil {
			s.logger.Warn().Err(err).
				Str("automation_id", a.ID.String()).
				Msg("failed to build config snapshot; skipping")
			continue
		}

		// Create run row (with idempotency via scheduled_time). We insert
		// BEFORE advancing next_run_at so that on duplicate/no-op the parent
		// row's next_run_at is left untouched — any out-of-band writer that
		// already advanced it wins.
		run := models.AutomationRun{
			AutomationID:   a.ID,
			OrgID:          a.OrgID,
			TriggeredBy:    models.AutomationTriggeredBySchedule,
			ScheduledTime:  scheduledTime,
			GoalSnapshot:   a.Goal,
			ConfigSnapshot: configSnapshot,
			Status:         models.AutomationRunStatusPending,
		}

		created, err := s.automationRuns.CreateRunInTx(ctx, tx, &run)
		if err != nil {
			s.logger.Error().Err(err).
				Str("automation_id", a.ID.String()).
				Msg("failed to create automation run; aborting tick")
			return
		}
		if !created {
			// Duplicate — idempotency check. Skip advancing too: whoever
			// inserted the row already advanced the parent.
			s.logger.Debug().
				Str("automation_id", a.ID.String()).
				Msg("skipping duplicate automation run")
			continue
		}

		if err := s.automations.AdvanceNextRunAt(ctx, tx, a.OrgID, a.ID, now, nextRunAt); err != nil {
			s.logger.Error().Err(err).
				Str("automation_id", a.ID.String()).
				Msg("failed to advance automation next_run_at; aborting tick")
			return
		}

		dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
		payload := map[string]string{
			"org_id":            a.OrgID.String(),
			"automation_id":     a.ID.String(),
			"automation_run_id": run.ID.String(),
		}
		jobID, err := s.jobs.EnqueueInTx(ctx, tx, a.OrgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
		if err != nil {
			s.logger.Error().Err(err).
				Str("automation_id", a.ID.String()).
				Str("automation_run_id", run.ID.String()).
				Msg("failed to enqueue automation_run job; aborting tick")
			return
		}
		if jobID != uuid.Nil {
			jobIDs = append(jobIDs, jobID)
		}
		s.logger.Info().
			Str("automation_id", a.ID.String()).
			Str("run_id", run.ID.String()).
			Msg("enqueued automation run")
	}

	if err := tx.Commit(ctx); err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to commit automation tx")
		return
	}
	for _, jobID := range jobIDs {
		s.jobs.Notify(ctx, jobID)
	}
}
