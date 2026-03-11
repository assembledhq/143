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
	logger       zerolog.Logger
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
		lock:         lock,
		jobs:         jobs,
		orgs:         orgs,
		integrations: integrations,
		plans:        plans,
		repos:        repos,
		logger:       logger,
	}
}

// SetProjectStore injects the project store for scheduled project cycles.
func (s *Scheduler) SetProjectStore(projects schedulerProjectStore) {
	s.projects = projects
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
	for _, orgID := range orgIDs {
		org, err := s.orgs.GetByID(ctx, orgID)
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("scheduler failed to fetch org settings")
			continue
		}
		settings := models.ParseOrgSettings(org.Settings)
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
			repoSettings := models.ParseRepoSettings(repo.Settings)
			if repoSettings.PM != nil {
				hasCustomRepos = true
				dedupeKey := fmt.Sprintf("pm_analyze:%s:%s", orgID.String(), repo.ID.String())
				payload := map[string]string{
					"org_id":  orgID.String(),
					"repo_id": repo.ID.String(),
					"trigger": string(models.PMTriggerCron),
				}
				if _, err := s.jobs.Enqueue(ctx, orgID, "default", "pm_analyze", payload, 5, &dedupeKey); err != nil {
					s.logger.Warn().Err(err).Str("org_id", orgID.String()).Str("repo_id", repo.ID.String()).Msg("failed to enqueue repo pm_analyze job")
				}
			}
		}

		// Enqueue an org-level job (no repo_id) for repos without custom settings,
		// or as the default when no repos have custom PM config.
		if !hasCustomRepos {
			dedupeKey := fmt.Sprintf("pm_analyze:%s", orgID.String())
			payload := map[string]string{
				"org_id":  orgID.String(),
				"trigger": string(models.PMTriggerCron),
			}
			if _, err := s.jobs.Enqueue(ctx, orgID, "default", "pm_analyze", payload, 5, &dedupeKey); err != nil {
				s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue pm_analyze job")
			}
		}
	}

	// Second pass: enqueue project_cycle jobs for scheduled projects that are due.
	s.scheduleProjectCycles(ctx, now)
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
		if _, err := s.jobs.Enqueue(ctx, project.OrgID, "default", "project_cycle", payload, 5, &dedupeKey); err != nil {
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

func (s *Scheduler) shouldRunPM(ctx context.Context, orgID uuid.UUID, now time.Time, interval time.Duration) (bool, error) {
	plan, err := s.plans.GetLatestByOrg(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	return plan.CreatedAt.Add(interval).Before(now), nil
}
