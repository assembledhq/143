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
	logger       zerolog.Logger
}

func NewScheduler(
	lock schedulerLock,
	jobs schedulerJobStore,
	orgs schedulerOrgStore,
	integrations schedulerIntegrationStore,
	plans schedulerPlanStore,
	logger zerolog.Logger,
) *Scheduler {
	return &Scheduler{
		lock:         lock,
		jobs:         jobs,
		orgs:         orgs,
		integrations: integrations,
		plans:        plans,
		logger:       logger,
	}
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
		scheduleHours := settings.PMScheduleHours
		if scheduleHours <= 0 {
			scheduleHours = models.DefaultPMScheduleHours
		}

		shouldRun, err := s.shouldRunPM(ctx, orgID, now, time.Duration(scheduleHours)*time.Hour)
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("scheduler failed to check PM schedule")
			continue
		}
		if !shouldRun {
			continue
		}

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
