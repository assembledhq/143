package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
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

type schedulerCapabilityResolver interface {
	ResolveForSession(ctx context.Context, in agentcapabilities.ResolveInput) ([]models.AgentCapabilitySnapshotItem, error)
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

// Scheduler enqueues periodic maintenance and integration jobs.
type schedulerLock interface {
	TryAcquire(ctx context.Context) (bool, error)
	Release(ctx context.Context) error
}

type Scheduler struct {
	lock           schedulerLock
	jobs           schedulerJobStore
	orgs           schedulerOrgStore
	integrations   schedulerIntegrationStore
	repos          schedulerRepoStore
	automations    schedulerAutomationStore    // nil-safe: automation scheduling disabled if nil
	automationRuns schedulerAutomationRunStore // nil-safe: automation scheduling disabled if nil
	capabilities   schedulerCapabilityResolver // nil-safe: scheduled automation runs use empty snapshots if nil
	sessions       schedulerSessionStore       // nil-safe: stranded-pending reaper disabled if nil
	pool           schedulerTxBeginner         // needed for automation scheduling transactions
	logger         zerolog.Logger

	domainStore    schedulerDomainStore    // nil-safe: verified-domain recheck disabled if nil
	domainVerifier schedulerDomainVerifier // nil-safe: verified-domain recheck disabled if nil
	audit          *db.AuditEmitter        // nil-safe: recheck disable events unlogged if nil
	githubOrgs     schedulerGitHubOrgStore // nil-safe: GitHub org roster reconciliation disabled if nil

	lastDailyJobDates map[string]string // tracks UTC date of last daily scheduling per job type
}

// schedulerDomainStore is the verified-domain surface for the daily DNS
// re-check sweep (expired/transferred-domain hygiene for auto-join).
type schedulerDomainStore interface {
	ListVerifiedDueForRecheck(ctx context.Context, checkedBefore time.Time, limit int) ([]models.OrganizationDomain, error)
	RecordRecheckSuccess(ctx context.Context, id uuid.UUID) error
	RecordRecheckFailure(ctx context.Context, id uuid.UUID, maxFailures int) (int, bool, error)
}

// schedulerDomainVerifier checks a domain's verification TXT record.
type schedulerDomainVerifier interface {
	Verify(ctx context.Context, domain, token string) (bool, error)
}

type schedulerGitHubOrgStore interface {
	ListEnabledAutoJoinLinksDueForRosterSync(ctx context.Context, syncedBefore time.Time, limit int) ([]models.GitHubOrgAutoJoinCandidate, error)
}

// domainRecheckInterval is how stale a verified domain's last check may be
// before the sweep re-verifies it. The data is the gate (last_checked_at),
// so the 10-minute scheduler tick re-checks each domain about once a day
// without extra bookkeeping.
const domainRecheckInterval = 24 * time.Hour

// domainRecheckBatchSize bounds DNS work per scheduler tick: each domain
// costs up to two lookups with 5s timeouts, and runOnce is sequential, so
// an unbounded sweep on a bad DNS day could starve every other scheduler
// pass. 25/tick clears 3600 domains/day — far above any realistic count —
// while capping a worst-case tick at ~4 minutes of DNS.
const domainRecheckBatchSize = 25

const githubOrgRosterSyncInterval = 24 * time.Hour
const githubOrgRosterSyncBatchSize = 25

func NewScheduler(
	lock schedulerLock,
	jobs schedulerJobStore,
	orgs schedulerOrgStore,
	integrations schedulerIntegrationStore,
	repos schedulerRepoStore,
	logger zerolog.Logger,
) *Scheduler {
	return &Scheduler{
		lock:              lock,
		jobs:              jobs,
		orgs:              orgs,
		integrations:      integrations,
		repos:             repos,
		logger:            logger,
		lastDailyJobDates: make(map[string]string),
	}
}

// SetAutomationStores injects the automation stores and connection pool for
// automation scheduling via the claim-and-fire loop.
func (s *Scheduler) SetAutomationStores(automations schedulerAutomationStore, runs schedulerAutomationRunStore, pool schedulerTxBeginner) {
	s.automations = automations
	s.automationRuns = runs
	s.pool = pool
}

func (s *Scheduler) SetCapabilityResolver(resolver schedulerCapabilityResolver) {
	s.capabilities = resolver
}

// SetSessionStore injects the session store used by the stranded-pending
// snapshot reaper. If unset, the reaper pass is a no-op (e.g. in tests that
// don't exercise it).
func (s *Scheduler) SetSessionStore(sessions schedulerSessionStore) {
	s.sessions = sessions
}

// SetDomainRecheck injects the stores for the daily verified-domain DNS
// re-check sweep. If unset, the sweep is a no-op.
func (s *Scheduler) SetDomainRecheck(store schedulerDomainStore, verifier schedulerDomainVerifier, audit *db.AuditEmitter) {
	s.domainStore = store
	s.domainVerifier = verifier
	s.audit = audit
}

func (s *Scheduler) SetGitHubOrgRosterReconciliation(store schedulerGitHubOrgStore) {
	s.githubOrgs = store
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
	for _, orgID := range orgIDs {
		// Slack synchronization remains independent of PM scheduling.
		slackDedupeKey := fmt.Sprintf("sync_slack:%s", orgID.String())
		slackPayload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", "sync_slack", slackPayload, 3, &slackDedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue sync_slack job")
		}

	}

	// Enqueue daily audit retention cleanup for each org (deduplicated per day).
	s.scheduleAuditRetentionCleanup(ctx, orgIDs, now)
	s.scheduleDataRetentionCleanup(ctx, orgIDs, now)
	s.scheduleDailyJob(ctx, "feedback", models.JobTypeReconcileCodeReviewOutcomes, orgIDs, now)

	// Second pass: reap stuck automation runs so a crashed worker does not
	// saturate max_concurrent forever. Runs before scheduleAutomationRuns so a
	// just-reaped automation can fire a fresh run on this same tick.
	s.reapStuckAutomationRuns(ctx)

	// Third pass: enqueue automation_run jobs for automations that are due.
	s.scheduleAutomationRuns(ctx, now)

	// Fourth pass: periodically reconcile stale PR health for open pull requests.
	s.schedulePullRequestReconciliation(ctx, orgIDs, now)

	// Sixth pass: reconcile PagerDuty incident mirrors from the provider API.
	s.schedulePagerDutySync(ctx, orgIDs, now)

	// Seventh pass: clear pending_snapshot_key on sessions whose owning upload
	// goroutine died (worker OOM, drain timeout, etc). Without this, the
	// orchestrator's gate would block continue_session forever for that
	// session. Idempotent across schedulers — concurrent reapers update the
	// same rows with the same NULL value.
	s.reapStrandedPendingSnapshots(ctx, now)

	// Eighth pass: refresh per-org Linear team-key allowlist once per UTC
	// day so bare-identifier detection (e.g. "ACS-1234") picks up new teams
	// created post-install. The OAuth callback enqueues an immediate refresh,
	// so this cron is the long-term safety net for teams added later. The
	// integration's settings UI does not surface a manual refresh, so this is
	// the only path that reconciles the cache against Linear's source of
	// truth.
	s.scheduleLinearTeamKeyRefresh(ctx, orgIDs, now)

	// Ninth pass: re-verify auto-join domains' DNS TXT records roughly
	// daily. A domain that expires or transfers must not keep admitting new
	// members forever — after MaxDomainRecheckFailures consecutive missing
	// records, auto-join is disabled (the verified claim is kept so nobody
	// else can grab the domain; re-enabling is an explicit admin action).
	s.recheckVerifiedDomains(ctx, now)

	// Tenth pass: reconcile GitHub org auto-join rosters roughly daily.
	// Login-time grants still live-confirm membership, so stale rosters only
	// affect discovery latency; this sweep heals missed organization webhooks.
	s.scheduleGitHubOrgRosterSyncs(ctx, now)
}

func (s *Scheduler) scheduleGitHubOrgRosterSyncs(ctx context.Context, now time.Time) {
	if s.githubOrgs == nil || s.jobs == nil {
		return
	}
	due, err := s.githubOrgs.ListEnabledAutoJoinLinksDueForRosterSync(ctx, now.Add(-githubOrgRosterSyncInterval), githubOrgRosterSyncBatchSize)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list github org rosters due for sync")
		return
	}
	for _, link := range due {
		dedupe := fmt.Sprintf("sync_github_org_roster:%d", link.InstallationID)
		if _, err := s.jobs.Enqueue(ctx, link.OrgID, "github", models.JobTypeSyncGitHubOrgRoster, map[string]any{
			"org_id":          link.OrgID.String(),
			"installation_id": link.InstallationID,
			"account_login":   link.AccountLogin,
		}, 5, &dedupe); err != nil {
			s.logger.Warn().Err(err).
				Str("org_id", link.OrgID.String()).
				Int64("installation_id", link.InstallationID).
				Msg("failed to enqueue github org roster sync")
		}
	}
}

// recheckVerifiedDomains is the expired/transferred-domain hygiene sweep.
// Resolver errors are skipped without a write — "DNS is down" carries no
// information about the record, and the next tick retries; only an
// affirmative present/absent answer moves last_checked_at or the failure
// streak.
func (s *Scheduler) recheckVerifiedDomains(ctx context.Context, now time.Time) {
	if s.domainStore == nil || s.domainVerifier == nil {
		return
	}

	due, err := s.domainStore.ListVerifiedDueForRecheck(ctx, now.Add(-domainRecheckInterval), domainRecheckBatchSize)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list verified domains due for recheck")
		return
	}

	for _, d := range due {
		ok, verr := s.domainVerifier.Verify(ctx, d.Domain, d.VerificationToken)
		if verr != nil {
			s.logger.Warn().Err(verr).Str("domain", d.Domain).Msg("domain recheck lookup failed; will retry next tick")
			continue
		}
		if ok {
			if err := s.domainStore.RecordRecheckSuccess(ctx, d.ID); err != nil {
				s.logger.Warn().Err(err).Str("domain", d.Domain).Msg("failed to record domain recheck success")
			}
			continue
		}

		failedChecks, disabled, err := s.domainStore.RecordRecheckFailure(ctx, d.ID, models.MaxDomainRecheckFailures)
		if err != nil {
			s.logger.Warn().Err(err).Str("domain", d.Domain).Msg("failed to record domain recheck failure")
			continue
		}
		s.logger.Warn().
			Str("domain", d.Domain).
			Str("org_id", d.OrgID.String()).
			Int("failed_checks", failedChecks).
			Bool("auto_join_disabled", disabled).
			Msg("verified domain TXT record missing on recheck")

		if disabled && s.audit != nil {
			idStr := d.ID.String()
			details, _ := json.Marshal(map[string]any{
				"domain":        d.Domain,
				"reason":        "dns_recheck_failed",
				"failed_checks": failedChecks,
			})
			s.audit.EmitSystemAction(ctx, db.SystemActionParams{
				OrgID:        d.OrgID,
				ActorID:      "domain-recheck",
				Action:       models.AuditActionTeamDomainUpdated,
				ResourceType: models.AuditResourceOrgDomain,
				ResourceID:   &idStr,
				Details:      details,
			})
		}
	}
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
	s.scheduleDailyJob(ctx, "default", "audit_retention_cleanup", orgIDs, now)
}

func (s *Scheduler) scheduleDataRetentionCleanup(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	s.scheduleDailyJob(ctx, "default", "data_retention_cleanup", orgIDs, now)
}

// scheduleDailyJob enqueues one job per org, deduplicated per UTC day.
// It avoids N redundant Enqueue calls on every scheduler tick after the first
// tick of the day.
func (s *Scheduler) scheduleDailyJob(ctx context.Context, queue, jobType string, orgIDs []uuid.UUID, now time.Time) {
	dateKey := now.UTC().Format("2006-01-02")
	if s.lastDailyJobDates == nil {
		s.lastDailyJobDates = make(map[string]string)
	}
	if s.lastDailyJobDates[jobType] == dateKey {
		return
	}

	allEnqueued := true
	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("%s:%s:%s", jobType, orgID.String(), dateKey)
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, queue, jobType, payload, 1, &dedupeKey); err != nil {
			allEnqueued = false
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msgf("failed to enqueue %s job", jobType)
		}
	}

	if allEnqueued {
		s.lastDailyJobDates[jobType] = dateKey
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
	tenMinuteBucket := now.UTC().Format("2006010215") + fmt.Sprintf("%d", now.UTC().Minute()/10)
	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("reconcile_pull_request_state:%s:%s", orgID.String(), tenMinuteBucket)
		payload := map[string]any{
			"org_id": orgID.String(),
			"limit":  pullRequestReconcileBatch,
		}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", "reconcile_pull_request_state", payload, 2, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue reconcile_pull_request_state job")
		}
	}
}

func (s *Scheduler) schedulePagerDutySync(ctx context.Context, orgIDs []uuid.UUID, now time.Time) {
	if s.jobs == nil {
		return
	}
	tenMinuteBucket := now.UTC().Format("2006010215") + fmt.Sprintf("%d", now.UTC().Minute()/10)
	for _, orgID := range orgIDs {
		dedupeKey := fmt.Sprintf("pagerduty_sync:%s:%s", orgID.String(), tenMinuteBucket)
		payload := map[string]any{"org_id": orgID.String()}
		if _, err := s.jobs.Enqueue(ctx, orgID, "default", models.JobTypePagerDutySync, payload, 2, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to enqueue pagerduty_sync job")
		}
	}
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
				Str("schedule_type", string(a.ScheduleType)).
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
		if s.capabilities != nil {
			snapshot, err := s.capabilities.ResolveForSession(ctx, agentcapabilities.ResolveInput{
				OrgID:         a.OrgID,
				RepositoryID:  a.RepositoryID,
				SessionOrigin: models.SessionOriginAutomation,
				AutomationID:  &a.ID,
			})
			if err != nil {
				s.logger.Error().Err(err).
					Str("automation_id", a.ID.String()).
					Msg("failed to resolve automation capabilities; skipping automation")
				continue
			}
			run.CapabilitySnapshot = snapshot
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
