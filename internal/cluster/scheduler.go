package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

type schedulerJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
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
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error)
}

type schedulerProjectStore interface {
	ListDueForSchedule(ctx context.Context, now time.Time) ([]models.Project, error)
	UpdateNextRunAt(ctx context.Context, orgID, projectID uuid.UUID, nextRunAt time.Time) error
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
	lock         schedulerLock
	jobs         schedulerJobStore
	orgs         schedulerOrgStore
	integrations schedulerIntegrationStore
	plans        schedulerPlanStore
	repos        schedulerRepoStore
	projects     schedulerProjectStore // nil-safe: project scheduling disabled if nil
	pmDocs       schedulerPMDocStore   // nil-safe: context refresh scheduling disabled if nil
	logger       zerolog.Logger

	lastCleanupDates map[string]string // tracks UTC date of last cleanup scheduling per job type
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
		lock:             lock,
		jobs:             jobs,
		orgs:             orgs,
		integrations:     integrations,
		plans:            plans,
		repos:            repos,
		logger:           logger,
		lastCleanupDates: make(map[string]string),
	}
}

// SetProjectStore injects the project store for scheduled project cycles.
func (s *Scheduler) SetProjectStore(projects schedulerProjectStore) {
	s.projects = projects
}

// SetPMDocStore injects the PM document store for context refresh scheduling.
func (s *Scheduler) SetPMDocStore(pmDocs schedulerPMDocStore) {
	s.pmDocs = pmDocs
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
		repos, err := s.repos.ListByOrg(ctx, orgID)
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

	// Second pass: enqueue project_cycle jobs for scheduled projects that are due.
	s.scheduleProjectCycles(ctx, now)

	// Third pass: enqueue pm_context_refresh for orgs with stale autogenerated context.
	s.scheduleContextRefreshes(ctx, orgIDs, orgSettings, now)
}

func (s *Scheduler) scheduleAuditRetentionCleanup(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	s.scheduleDailyCleanupJob(ctx, "audit_retention_cleanup", orgIDs, now)
}

func (s *Scheduler) scheduleDataRetentionCleanup(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	s.scheduleDailyCleanupJob(ctx, "data_retention_cleanup", orgIDs, now)
}

// scheduleDailyCleanupJob enqueues one job per org, deduplicated per UTC day.
// It avoids N redundant Enqueue calls on every scheduler tick after the first
// tick of the day — matching the selective-query pattern used by scheduleProjectCycles.
func (s *Scheduler) scheduleDailyCleanupJob(ctx context.Context, jobType string, orgIDs []uuid.UUID, now time.Time) {
	dateKey := now.UTC().Format("2006-01-02")
	if s.lastCleanupDates == nil {
		s.lastCleanupDates = make(map[string]string)
	}
	if s.lastCleanupDates[jobType] == dateKey {
		return
	}

	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("%s:%s:%s", jobType, orgID.String(), dateKey)
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", jobType, payload, 1, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msgf("failed to enqueue %s job", jobType)
		}
	}

	s.lastCleanupDates[jobType] = dateKey
}

func (s *Scheduler) scheduleProjectCycles(ctx context.Context, now time.Time) {
	if s.projects == nil {
		return
	}

	dueProjects, err := s.projects.ListDueForSchedule(ctx, now)
	if err != nil {
		s.logger.Error().Err(err).Msg("scheduler failed to list due projects")
		return
	}

	for _, project := range dueProjects {
		dedupeKey := fmt.Sprintf("project_cycle:%s", project.ID.String())
		payload := map[string]string{
			"org_id":     project.OrgID.String(),
			"project_id": project.ID.String(),
		}
		if _, err := s.jobs.Enqueue(ctx, project.OrgID, "default", models.JobTypeProjectCycle, payload, 5, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).
				Str("project_id", project.ID.String()).
				Str("org_id", project.OrgID.String()).
				Msg("failed to enqueue project_cycle job")
			continue
		}

		// Advance next_run_at so we don't re-enqueue on the next tick.
		nextRun := models.NextRunTime(now, project.ScheduleInterval, project.ScheduleUnit)
		if err := s.projects.UpdateNextRunAt(ctx, project.OrgID, project.ID, nextRun); err != nil {
			s.logger.Warn().Err(err).
				Str("project_id", project.ID.String()).
				Msg("failed to advance project next_run_at")
		}
	}
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
	if failedJob != nil && failedJob.UpdatedAt.Add(interval).After(now) {
		s.logger.Debug().
			Str("org_id", orgID.String()).
			Time("failed_at", failedJob.UpdatedAt).
			Str("error", failedJob.LastError).
			Msg("skipping PM analysis: recent failure within interval")
		return false, nil
	}

	return true, nil
}
