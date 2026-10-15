package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type outcomeDailyAdmissionCall struct {
	OrgID uuid.UUID
	Opts  db.EnqueueOpts
}

// The retained rows model the store contract, including completed admissions.
// The store's PostgreSQL tests verify that contract under concurrent callers.
type outcomeDailySchedulerJobs struct {
	mockJobs
	ordinaryCalls []outcomeDailyAdmissionCall
	durableCalls  []outcomeDailyAdmissionCall
	inserted      []outcomeDailyAdmissionCall
	retained      map[string]string
	failures      map[uuid.UUID]int
}

func (m *outcomeDailySchedulerJobs) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.ordinaryCalls = append(m.ordinaryCalls, outcomeDailyAdmissionCall{orgID, db.EnqueueOpts{
		Queue: queue, JobType: jobType, Payload: payload, Priority: priority, DedupeKey: dedupeKey,
	}})
	return uuid.New(), nil
}

func (m *outcomeDailySchedulerJobs) EnqueueCodeReviewOutcomeReconciliationOnce(_ context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	call := outcomeDailyAdmissionCall{orgID, opts}
	m.durableCalls = append(m.durableCalls, call)
	if m.failures[orgID] > 0 {
		m.failures[orgID]--
		return uuid.Nil, errors.New("admission unavailable")
	}
	if opts.DedupeKey == nil {
		return uuid.Nil, errors.New("daily admission key missing")
	}
	if m.retained[*opts.DedupeKey] != "" {
		return uuid.Nil, nil
	}
	if m.retained == nil {
		m.retained = make(map[string]string)
	}
	m.retained[*opts.DedupeKey] = "pending"
	m.inserted = append(m.inserted, call)
	return uuid.New(), nil
}

func expectedOutcomeDailyCall(orgID uuid.UUID, queue, jobType, date string, priority int) outcomeDailyAdmissionCall {
	key := jobType + ":" + orgID.String() + ":" + date
	return outcomeDailyAdmissionCall{orgID, db.EnqueueOpts{
		Queue: queue, JobType: jobType, Payload: map[string]string{"org_id": orgID.String()},
		Priority: priority, DedupeKey: &key,
	}}
}

func TestScheduler_DailyOutcomeAdmissionRoutesOnlyFeedbackRepair(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		queue   string
		jobType string
		durable bool
	}{
		{name: "provider outcome repair", queue: "feedback", jobType: models.JobTypeReconcileCodeReviewOutcomes, durable: true},
		{name: "other queue retains ordinary enqueue", queue: "default", jobType: models.JobTypeReconcileCodeReviewOutcomes},
		{name: "other feedback job retains ordinary enqueue", queue: "feedback", jobType: "other_daily_feedback"},
		{name: "other daily maintenance retains ordinary enqueue", queue: "default", jobType: "audit_retention_cleanup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			jobs := &outcomeDailySchedulerJobs{}
			scheduler := &Scheduler{jobs: jobs, logger: zerolog.Nop()}
			now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)

			scheduler.scheduleDailyJobWithPriority(context.Background(), tt.queue, tt.jobType, []uuid.UUID{orgID}, now, 7)

			var durable, ordinary []outcomeDailyAdmissionCall
			expected := []outcomeDailyAdmissionCall{expectedOutcomeDailyCall(orgID, tt.queue, tt.jobType, "2026-08-04", 7)}
			if tt.durable {
				durable = expected
			} else {
				ordinary = expected
			}
			require.Equal(t, durable, jobs.durableCalls, "only feedback outcome repair should use all-status durable admission with unchanged scheduling options")
			require.Equal(t, ordinary, jobs.ordinaryCalls, "all other daily jobs should keep ordinary enqueue with unchanged scheduling options")
			require.Equal(t, map[string]string{tt.jobType: "2026-08-04"}, scheduler.lastDailyJobDates, "a successful admission pass should retain the local daily fast path")
		})
	}
}

func TestScheduler_DailyOutcomeAdmissionSurvivesSchedulerReplacementAndUTCRollover(t *testing.T) {
	t.Parallel()
	orgIDs := []uuid.UUID{uuid.New(), uuid.New()}
	jobs := &outcomeDailySchedulerJobs{}
	first := &Scheduler{jobs: jobs, logger: zerolog.Nop()}
	replacement := &Scheduler{jobs: jobs, logger: zerolog.Nop()}
	// The local calendar day differs from the UTC admission day.
	now := time.Date(2026, 10, 14, 20, 30, 0, 0, time.FixedZone("local", -4*60*60))

	first.scheduleDailyJob(context.Background(), "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, now)
	for key := range jobs.retained {
		jobs.retained[key] = "succeeded"
	}
	// Sequential leadership changes share retained jobs but no process cache.
	steps := []struct {
		scheduler *Scheduler
		at        time.Time
	}{
		{first, now.Add(10 * time.Minute)},
		{replacement, now.Add(time.Hour)},
		{replacement, now.Add(2 * time.Hour)},
		{replacement, now.Add(24 * time.Hour)},
	}
	for _, step := range steps {
		step.scheduler.scheduleDailyJob(context.Background(), "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, step.at)
	}

	dayOne := []outcomeDailyAdmissionCall{
		expectedOutcomeDailyCall(orgIDs[0], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-10-15", 1),
		expectedOutcomeDailyCall(orgIDs[1], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-10-15", 1),
	}
	dayTwo := []outcomeDailyAdmissionCall{
		expectedOutcomeDailyCall(orgIDs[0], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-10-16", 1),
		expectedOutcomeDailyCall(orgIDs[1], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-10-16", 1),
	}
	expectedCalls := append(append(append([]outcomeDailyAdmissionCall{}, dayOne...), dayOne...), dayTwo...)
	expectedInserted := append(append([]outcomeDailyAdmissionCall{}, dayOne...), dayTwo...)
	require.Equal(t, expectedCalls, jobs.durableCalls, "replacement leaders should consult durable admission while repeated ticks use their local cache")
	require.Equal(t, expectedInserted, jobs.inserted, "completed org-day repairs must stay admitted while each org can enter the next UTC day")
	require.Empty(t, jobs.ordinaryCalls, "daily provider repair must never fall back to active-only enqueue")
	require.Equal(t, map[string]string{models.JobTypeReconcileCodeReviewOutcomes: "2026-10-16"}, replacement.lastDailyJobDates, "the local fast path should advance only after a successful new UTC day")
}

func TestScheduler_DailyOutcomeAdmissionRetriesPartialFailureWithoutReadmittingSuccess(t *testing.T) {
	t.Parallel()
	orgIDs := []uuid.UUID{uuid.New(), uuid.New()}
	jobs := &outcomeDailySchedulerJobs{failures: map[uuid.UUID]int{orgIDs[1]: 1}}
	scheduler := &Scheduler{jobs: jobs, logger: zerolog.Nop()}
	now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)

	scheduler.scheduleDailyJob(context.Background(), "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, now)
	require.Empty(t, scheduler.lastDailyJobDates, "one failed org must leave the daily pass eligible for retry")
	scheduler.scheduleDailyJob(context.Background(), "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, now.Add(10*time.Minute))
	scheduler.scheduleDailyJob(context.Background(), "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, now.Add(20*time.Minute))

	expected := []outcomeDailyAdmissionCall{
		expectedOutcomeDailyCall(orgIDs[0], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-08-04", 1),
		expectedOutcomeDailyCall(orgIDs[1], "feedback", models.JobTypeReconcileCodeReviewOutcomes, "2026-08-04", 1),
	}
	require.Equal(t, append(append([]outcomeDailyAdmissionCall{}, expected...), expected...), jobs.durableCalls, "an unfinished pass should retry both orgs through durable admission exactly once")
	require.Equal(t, expected, jobs.inserted, "retrying a partial pass should admit only the previously failed org")
	require.Empty(t, jobs.ordinaryCalls, "a retry must not bypass durable admission")
	require.Equal(t, map[string]string{models.JobTypeReconcileCodeReviewOutcomes: "2026-08-04"}, scheduler.lastDailyJobDates, "only the fully successful retry should finish the local daily pass")
}
