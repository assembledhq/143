package codereview

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This canceller uses a separate connection, proving replacement intent and
// revocation committed before cancellation becomes externally visible.
type schedulingInputCanceller struct {
	pool      *pgxpool.Pool
	prID      uuid.UUID
	cancelled []uuid.UUID
}

func (c *schedulingInputCanceller) CancelActiveThreads(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (int, error) {
	var durable bool
	if err := c.pool.QueryRow(ctx, `SELECT pending_input IS NOT NULL AND EXISTS(SELECT 1 FROM jobs WHERE org_id=$1 AND job_type=$3 AND payload->>'pull_request_id'=$2::uuid::text AND status IN ('pending','running')) FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, orgID, c.prID, models.JobTypeReconcileCodeReviewSchedule).Scan(&durable); err != nil {
		return 0, err
	}
	if !durable {
		return 0, fmt.Errorf("cancellation preceded committed replacement intent and wake")
	}
	delegate := &schedulingThreadCanceller{pool: c.pool}
	count, err := delegate.CancelActiveThreads(ctx, orgID, sessionIDs)
	c.cancelled = append(c.cancelled, delegate.cancelled...)
	return count, err
}

func testSchedulingInputDrift(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, field, route string) {
	ctx := context.Background()
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.Body = "Original description" })
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read original admission")
	store := db.NewCodeReviewStore(pool)
	metadata, err := store.GetBySessionID(ctx, org, sessionID)
	require.NoError(t, err, "read original review")
	manifest, err := json.Marshal(ReviewInputManifest{Title: snapshot.snapshot.Title, Description: snapshot.snapshot.Body})
	require.NoError(t, err, "encode captured title and description")
	assessments := db.NewCodeReviewAssessmentStore(pool)
	original, _, err := assessments.Create(ctx, models.CodeReviewAssessmentCapture{
		OrgID: org, RepositoryID: repo, PullRequestID: pr, PolicyID: metadata.PolicyID, SessionID: sessionID,
		Generation: 1, BaseSHA: snapshot.snapshot.BaseSHA, BaseRef: snapshot.snapshot.BaseRef, HeadSHA: snapshot.snapshot.HeadSHA,
		InputVersion: ReviewInputManifestVersion, CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gate", InputDigest: "input", InputManifest: manifest,
		ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: "input-drift:" + pr.String(),
	})
	require.NoError(t, err, "capture full assessment baseline")
	require.NoError(t, assessments.MarkRunning(ctx, org, original.ID, 1, "input"), "start captured assessment")
	_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, org, pr, original.ID)
	require.NoError(t, err, "bind active full assessment")
	if route == "legacy" {
		_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=revision_context-'schedule_inputs' WHERE org_id=$1 AND id=$2`, org, sessionID)
		require.NoError(t, err, "pre-upgrade sessions rely on immutable assessment capture")
	}
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: input drift','running')`, org, sessionID)
	require.NoError(t, err, "start original reviewer")
	canceller := &schedulingInputCanceller{pool: pool, prID: pr}
	service.SetThreadCanceller(canceller)
	config := models.DefaultCodeReviewPolicyConfig()
	quiet, interval := 300, 300
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &quiet, MinimumIntervalSeconds: &interval}
	_, err = store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "enable normal quiet and minimum interval delays")
	now := before.LastMaterialChangeAt.UTC().Add(time.Minute)
	service.scheduling.now = func() time.Time { return now }
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) {
		if field == "title" {
			s.Title = "Updated review intent"
		} else {
			s.Body = "Updated body with new intent"
		}
	})
	if route == "captured" {
		raw, err := json.Marshal(reviewSnapshotInputs(snapshot.snapshot))
		require.NoError(t, err, "encode a later admission observation")
		_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(revision_context,'{schedule_inputs}',$3::jsonb) WHERE org_id=$1 AND id=$2`, org, sessionID, raw)
		require.NoError(t, err, "captured analysis must win over session observation")
	}
	if route == "missed" {
		allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sessionID)
		require.NoError(t, err, "missed webhook refresh should durably queue replacement")
		require.False(t, allowed, "obsolete title or body cannot fan out or publish")
	} else {
		_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
		require.NoError(t, err, "edited provider input should queue a replacement")
	}
	require.Equal(t, []uuid.UUID{sessionID}, canceller.cancelled, "obsolete panel cancels immediately while replacement is debounced")
	stale, err := store.GetBySessionID(ctx, org, sessionID)
	require.NoError(t, err, "read original review after event")
	require.Equal(t, models.CodeReviewSessionStatusStale, stale.Status, "obsolete metadata must be durably stale before worker returns")
	pending, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read delayed replacement")
	require.Equal(t, before.Generation+1, pending.Generation, "same-head input drift advances admission generation once")
	require.Equal(t, now, pending.LastMaterialChangeAt.UTC(), "quiet period starts at the title or body observation")
	require.Equal(t, now.Add(5*time.Minute), pending.EligibleAt.UTC(), "replacement waits full quiet interval")
	var intent scheduledReviewIntent
	require.NoError(t, json.Unmarshal(pending.PendingInput, &intent), "decode replacement intent")
	require.True(t, intent.Force, "same-head input changes must not reuse old review")
	require.Equal(t, reviewSnapshotInputs(snapshot.snapshot), intent.Inputs, "replacement binds current title and body")
	now = now.Add(time.Minute)
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "duplicate edited event should coalesce")
	repeated, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read coalesced intent")
	require.Equal(t, pending.Generation, repeated.Generation, "duplicate does not revoke another generation")
	require.Equal(t, pending.LastMaterialChangeAt, repeated.LastMaterialChangeAt, "duplicate does not restart quiet period")
	require.Equal(t, pending.EligibleAt, repeated.EligibleAt, "duplicate does not postpone replacement")
	now = pending.EligibleAt.Add(time.Second)
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "retire obsolete full reservation and admit replacement")
	retired, err := assessments.GetByID(ctx, org, original.ID)
	require.NoError(t, err, "read retired assessment")
	require.Equal(t, models.CodeReviewAssessmentSuperseded, retired.Status, "obsolete assessment releases admission")
	latest, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read replacement review")
	require.NotEqual(t, sessionID, latest.SessionID, "replacement receives distinct session despite same code hashes")
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, latest.SessionID)
	require.NoError(t, err, "validate freshly admitted title and body")
	require.True(t, allowed, "replacement retains execution authority")
}

func testSchedulingEquivalentAutomaticJoin(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	service, _, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read active schedule")
	result, err := service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "unchanged provider event should join active review")
	require.True(t, result.Reused, "automatic redelivery reuses active work")
	require.Equal(t, sessionID, result.SessionID, "automatic redelivery identifies original review")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read joined schedule")
	require.Nil(t, after.PendingInput, "equivalent automatic event must not display spurious queue entry")
	require.Equal(t, before.Generation, after.Generation, "join preserves execution generation")
	require.Equal(t, before.LastMaterialChangeAt, after.LastMaterialChangeAt, "join does not reset cadence")
	require.Equal(t, models.CodeReviewScheduleRunning, after.State, "active review stays running")
}

func testSchedulingStrandedJoin(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, explicit bool) {
	ctx := context.Background()
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	_, err := pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "represent a starter that exited without running its review")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET created_at=now()-interval '10 minutes' WHERE org_id=$1 AND session_id=$2`, org, sessionID)
	require.NoError(t, err, "stranded metadata has outlived enqueue grace")
	input := ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: explicit}
	if explicit {
		input.GitHubDeliveryID = uuid.NewString()
	}
	result, err := service.scheduleReview(ctx, input, models.CodeReviewEnsureCurrent, false, nil)
	require.NoError(t, err, "equivalent observation must schedule stranded-work recovery")
	require.False(t, result.Reused, "metadata without actual execution is not an active review to join")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read recovery intent")
	require.NotNil(t, state.PendingInput, "stranded metadata must keep a recovery wake")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "recovery wake should admit a fresh controller")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read recovered review")
	require.NotEqual(t, sessionID, latest.SessionID, "stranded controller must be replaced")
}

func testSchedulingProtectedPublication(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, publication models.CodeReviewPublicationState, concurrentSend bool) {
	ctx := context.Background()
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	metadata, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sessionID)
	require.NoError(t, err, "read publishing review")
	manifest, err := json.Marshal(ReviewInputManifest{Title: snapshot.snapshot.Title, Description: snapshot.snapshot.Body})
	require.NoError(t, err, "encode analysis inputs")
	assessments := db.NewCodeReviewAssessmentStore(pool)
	assessment, _, err := assessments.Create(ctx, models.CodeReviewAssessmentCapture{
		OrgID: org, RepositoryID: repo, PullRequestID: pr, PolicyID: metadata.PolicyID, SessionID: sessionID,
		Generation: 1, BaseSHA: snapshot.snapshot.BaseSHA, BaseRef: snapshot.snapshot.BaseRef, HeadSHA: snapshot.snapshot.HeadSHA,
		InputVersion: ReviewInputManifestVersion, CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gate", InputDigest: "input", InputManifest: manifest,
		ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: "protected:" + pr.String(),
	})
	require.NoError(t, err, "create publication baseline")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state=$3,result_origin='executed',submitted_commit_sha=head_sha WHERE org_id=$1 AND id=$2`, org, assessment.ID, publication)
	require.NoError(t, err, "represent publication state")
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: publication','running')`, org, sessionID)
	require.NoError(t, err, "represent publishing controller thread")
	canceller := &schedulingInputCanceller{pool: pool, prID: pr}
	service.SetThreadCanceller(canceller)
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.Title = "Changed during publication" })
	queue := func() error {
		_, err := service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
		return err
	}
	if concurrentSend {
		publicationTx, err := pool.Begin(ctx)
		require.NoError(t, err, "begin external publication transition")
		defer func() { _ = publicationTx.Rollback(ctx) }()
		var publisherPID int
		require.NoError(t, publicationTx.QueryRow(ctx, `SELECT pg_backend_pid() FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, assessment.ID).Scan(&publisherPID), "hold assessment row while external send begins")
		queued := make(chan error, 1)
		go func() { queued <- queue() }()
		var lockErr error
		require.Eventually(t, func() bool {
			var waiting bool
			lockErr = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, publisherPID).Scan(&waiting)
			return lockErr == nil && waiting
		}, 10*time.Second, 10*time.Millisecond, "scheduler must wait for the in-progress publication transition")
		require.NoError(t, lockErr, "observe assessment publication lock")
		_, err = publicationTx.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_state='uncertain' WHERE org_id=$1 AND id=$2`, org, assessment.ID)
		require.NoError(t, err, "publication becomes uncertain before cancellation can lock it")
		require.NoError(t, publicationTx.Commit(ctx), "commit send attempt before scheduler decides revocation")
		require.NoError(t, <-queued, "scheduler should preserve a concurrent uncertain send")
		publication = models.CodeReviewPublicationUncertain
	} else {
		require.NoError(t, queue(), "new intent must persist while publication is in progress")
	}
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read replacement intent")
	require.NotNil(t, state.PendingInput, "publication protection cannot lose the replacement")
	protected := publication == models.CodeReviewPublicationUncertain || publication == models.CodeReviewPublicationConfirmed
	if protected {
		require.Equal(t, []uuid.UUID(nil), canceller.cancelled, "unknown or recorded send must remain available for reconciliation")
		allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sessionID)
		require.ErrorIs(t, err, db.ErrCodeReviewPublicationPending, "worker retries protected publication into staged recovery")
		require.False(t, allowed, "protected publication never permits new obsolete analysis")
		current, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sessionID)
		require.NoError(t, err, "read protected metadata")
		require.Equal(t, metadata.Status, current.Status, "publication protection preserves original metadata")
	} else {
		require.Equal(t, []uuid.UUID{sessionID}, canceller.cancelled, "reserved unsent work can be cancelled")
		require.ErrorIs(t, assessments.MarkPublicationAttemptUncertain(ctx, org, assessment.ID, assessment.Generation, assessment.InputDigest), db.ErrCodeReviewAssessmentState, "revocation winning the race must fence a later external send")
	}
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "reconcile must respect publication reservation")
	current, err := assessments.GetByID(ctx, org, assessment.ID)
	require.NoError(t, err, "read publication assessment after recovery")
	if protected {
		require.Equal(t, models.CodeReviewAssessmentPublishing, current.Status, "uncertain and confirmed publication cannot be superseded")
		require.Equal(t, publication, current.PublicationState, "publication evidence must remain intact")
		waiting, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "read admission protection")
		require.Equal(t, models.CodeReviewWaitActive, waiting.WaitReason, "replacement waits until publication reconciliation settles")
	} else {
		require.Equal(t, models.CodeReviewAssessmentSuperseded, current.Status, "unsent reservation releases before replacement admission")
	}
}

func testSchedulingCompletedBaselineInputs(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, completed bool) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessment, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read evidence-only assessment")
	manifest, err := decodeAssessmentManifest(assessment.InputManifest)
	require.NoError(t, err, "read newer evidence inputs")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) {
		s.Title = manifest.Title + " with newer evidence"
		s.Body = manifest.Description + "\nUpdated evidence"
	})
	manifest.Title, manifest.Description = snapshot.snapshot.Title, snapshot.snapshot.Body
	raw, err := json.Marshal(manifest)
	require.NoError(t, err, "encode newer evidence analysis")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET input_manifest=$3 WHERE org_id=$1 AND id=$2`, org, assessment.ID, raw)
	require.NoError(t, err, "newer title and body belong to evidence assessment")
	if completed {
		_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',result_origin='evidence_only',coverage_complete=true,decision='blocked',acceptable=false,structured_outcome='{}',publication_state='not_required',completed_at=now() WHERE org_id=$1 AND id=$2`, org, assessment.ID)
		require.NoError(t, err, "represent a completed evidence-only turn")
	}
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read assessment schedule")
	_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(revision_context,'{schedule_generation}',to_jsonb($3::bigint)) WHERE org_id=$1 AND id=$2`, org, assessment.SessionID, before.Generation)
	require.NoError(t, err, "align baseline admission generation with existing evidence schedule")
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "ordinary observation should retain existing evidence routing")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read observed evidence state")
	require.Equal(t, before.Generation, after.Generation, "completed full capture cannot supersede newer evidence assessment")
	var intent scheduledReviewIntent
	require.NoError(t, json.Unmarshal(after.PendingInput, &intent), "decode automatic intent")
	require.False(t, intent.Force, "ordinary title/body observation must not force a full panel from completed baseline")
	current, err := schedules.GetAssessmentByID(ctx, org, assessment.ID)
	require.NoError(t, err, "read preserved evidence assessment")
	expectedStatus := assessment.Status
	if completed {
		expectedStatus = models.CodeReviewAssessmentCompleted
	}
	require.Equal(t, expectedStatus, current.Status, "automatic observation preserves newer evidence assessment")
}
