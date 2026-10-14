package codereview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func testSchedulingSandboxCapacity(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	zero := 0
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
	_, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "keep intake enabled with immediate scheduling policy")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	service.SetSandboxAdmission()
	worker := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO nodes(id,host,mode,status,metadata) VALUES($1,$1,'worker','active','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":1,"live_sandbox_count":1,"reserved_sandbox_count":0}')`, worker)
	require.NoError(t, err, "seed verified saturated synthetic worker")
	_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, models.CodeReviewReviewNow, false, nil)
	require.NoError(t, err, "accept intake before capacity opens")
	claim := func() context.Context {
		token := uuid.New()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, token).Scan(&id)
		require.NoError(t, err, "claim a bounded scheduler wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), token)
	}
	wake := models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "saturated fleet should durably defer startup")
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read capacity wait")
	require.Equal(t, models.CodeReviewWaitCapacity, before.WaitReason, "queue should explain the capacity wait")
	require.NotNil(t, before.PendingInput, "capacity cannot discard the request")
	var count, attempts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, org).Scan(&count), "read attempt clocks during wait")
	require.Equal(t, 0, count, "waiting must not create an attempt clock")
	snapshot.Lock()
	calls := snapshot.calls
	snapshot.err = fmt.Errorf("provider must not be called during capacity wait")
	snapshot.Unlock()
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "repeated capacity wait should avoid provider amplification")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read repeated durable wait")
	require.Equal(t, before.PendingInput, after.PendingInput, "repeat must preserve exact pending provenance")
	require.Equal(t, before.FirstPendingAt, after.FirstPendingAt, "repeat must preserve request age")
	require.Equal(t, before.Generation, after.Generation, "capacity does not invent revision generations")
	require.NoError(t, pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeReconcileCodeReviewSchedule).Scan(&attempts), "read wake attempt budget")
	require.Equal(t, 0, attempts, "capacity deferrals must undo each claim-time attempt increment")
	snapshot.Lock()
	require.Equal(t, calls, snapshot.calls, "capacity polling must not repeat GitHub hydration")
	snapshot.err = nil
	snapshot.snapshot.HeadSHA = "fresh-head-after-capacity"
	snapshot.Unlock()
	_, err = pool.Exec(ctx, `UPDATE nodes SET metadata='{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":10000,"live_sandbox_count":0,"reserved_sandbox_count":0}',last_heartbeat_at=now() WHERE id=$1`, worker)
	require.NoError(t, err, "open synthetic headroom independently of other parallel fixture tenants")
	clockFloor := time.Now()
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "new headroom should admit only after fresh provider hydration")
	metadata, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the newly admitted attempt")
	require.Equal(t, "fresh-head-after-capacity", metadata.HeadSHA, "capacity opening must not bypass head freshness")
	require.True(t, metadata.CreatedAt.After(clockFloor), "review deadline must begin after capacity opens")
}

// This executes the real scheduler and startReview transaction, rather than
// inserting surrogate commitments after a standalone budget query.
func testSchedulingSandboxBurst(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	zero := 0
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
	policy, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "enable new intake for the real startup burst")
	workers := []string{uuid.NewString(), uuid.NewString()}
	for i, id := range workers {
		_, err = pool.Exec(ctx, `INSERT INTO nodes(id,host,mode,status,started_at,metadata) VALUES($1,$2,'worker','active',clock_timestamp()+$3::interval,'{"sandbox_host_id":"192.0.2.20","max_active_sandboxes":4,"live_sandbox_count":0,"reserved_sandbox_count":0}')`, id, fmt.Sprintf("generation-container-%d", i), fmt.Sprintf("%d seconds", i))
		require.NoError(t, err, "seed two container generations of one physical worker")
	}
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `UPDATE nodes SET status='draining',drain_intent='planned_rollout' WHERE id=ANY($1)`, workers)
		require.NoError(t, err, "retire only this burst's advertised workers")
	})
	for i := range 128 {
		session := uuid.New()
		key := fmt.Sprintf("parked-output-%d", i)
		_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,title,status,origin) VALUES($1,$2,'Parked review','idle','code_review')`, session, org)
		require.NoError(t, err, "seed a deliberately parked review session")
		_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,'parked-base',$6,'slash_command','queued',$7)`, org, session, repo, pr, policy.ID, fmt.Sprintf("parked-head-%d", i), key)
		require.NoError(t, err, "seed durable parked metadata above the fleet budget")
		payload, err := json.Marshal(map[string]string{"session_id": session.String(), "review_output_key": key})
		require.NoError(t, err, "encode parked controller identity")
		_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,run_at) VALUES($1,'default','run_code_review',$2,'infinity')`, org, payload)
		require.NoError(t, err, "park the controller without reclaiming or deleting it")
	}
	const burst = 124
	type target struct {
		id      uuid.UUID
		service *Service
	}
	targets := make([]target, burst)
	for i := range burst {
		id := uuid.New()
		number := i + 100
		_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,$3,$4,'test/repo','Burst review')`, id, org, number, fmt.Sprintf("https://github.com/test/repo/pull/%d", number))
		require.NoError(t, err, "seed an independent synthetic burst PR")
		provider := &schedulingSnapshotFixture{snapshot: snapshot.snapshot}
		provider.snapshot.Number = number
		provider.snapshot.HTMLURL = fmt.Sprintf("https://github.com/test/repo/pull/%d", number)
		service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
		service.SetScheduling(db.NewCodeReviewScheduleStore(pool), provider)
		service.SetSandboxAdmission()
		_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: id, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, models.CodeReviewReviewNow, false, nil)
		require.NoError(t, err, "persist every incoming review request")
		targets[i] = target{id, service}
	}
	errors := make(chan error, burst)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token := uuid.New()
			var job uuid.UUID
			err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=clock_timestamp()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+target.id.String(), token).Scan(&job)
			if err == nil {
				err = target.service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, job), token), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: target.id})
			}
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err, "each concurrent real startup must admit or durably defer")
	}
	var admitted, waiting, parked int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id<>$2`, org, pr).Scan(&admitted), "read actual allocated review sessions")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_pr_state WHERE org_id=$1 AND pending_input IS NOT NULL AND wait_reason='sandbox_capacity'`, org).Scan(&waiting), "read intact pending request owners")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND run_at='infinity'`, org).Scan(&parked), "read untouched parked controllers")
	require.Equal(t, []int{4, 120, 128}, []int{admitted, waiting, parked}, "one physical budget must bound real starts while parked work and new intake remain durable")
}

func testEvidenceCapacityLoss(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	current, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read the previous evidence assessment")
	baseline, err := schedules.GetLatestFullBaseline(ctx, org, pr)
	require.NoError(t, err, "read the exact full baseline used by admission")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',result_origin='executed',coverage_complete=true,decision='blocked',acceptable=false,structured_outcome=$3,publication_state='not_required',completed_at=now() WHERE org_id=$1 AND id=$2`, org, current.ID, baseline.StructuredOutcome)
	require.NoError(t, err, "retire the previous evidence assessment")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1`, org)
	require.NoError(t, err, "settle prior controller work before testing capacity")
	_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET active_assessment_id=NULL,current_assessment_id=$3,state='covered' WHERE org_id=$1 AND pull_request_id=$2`, org, pr, current.ID)
	require.NoError(t, err, "retain the current executed evidence outcome")
	manifest, err := decodeAssessmentManifest(current.InputManifest)
	require.NoError(t, err, "decode prior evidence input")
	input := ReviewInputCapture{Code: manifest.Code, Contract: manifest.Contract, Title: manifest.Title, Description: manifest.Description, Visual: manifest.Visual, TextEvidence: manifest.TextEvidence, Request: manifest.Request, Gates: manifest.Gates}
	input.Visual.Images = []ReviewVisualImage{{SourceID: "capacity-loss-image", SourceURL: "https://example.test/capacity.png", ContentDigest: strings.Repeat("e", 64)}}
	changed, err := BuildReviewInputManifest(input)
	require.NoError(t, err, "build distinct evidence for another narrow turn")
	resolved, err := store.ResolvePolicy(ctx, org)
	require.NoError(t, err, "read saved policy identity")
	// The lifecycle suite is serial in its own database. Retire previous test
	// advertisements so this scenario owns its complete synthetic fleet.
	_, err = pool.Exec(ctx, `UPDATE nodes SET status='draining',drain_intent='planned_rollout'`)
	require.NoError(t, err, "isolate this capacity advertisement")
	worker := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO nodes(id,host,mode,status,metadata) VALUES($1,'generation-only-hostname','worker','active','{"sandbox_host_id":"192.0.2.30","max_active_sandboxes":2,"live_sandbox_count":2,"reserved_sandbox_count":0}')`, worker)
	require.NoError(t, err, "advertise no cold slots")
	unrelated := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,title,status,container_id,worker_node_id) VALUES($1,$2,'Other warm session','idle','other-warm-container',$3)`, unrelated, org, worker)
	require.NoError(t, err, "seed a different warm session that must not satisfy baseline preflight")
	_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET active_session_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, org, pr, unrelated)
	require.NoError(t, err, "make schedule active-session differ from evidence baseline")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	service.SetSandboxAdmission()
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.scheduling.now = func() time.Time { return now }
	captures := 0
	fixture := assessmentAdmissionFixture{pool: pool, manifest: changed, policy: *resolved.Policy, session: baseline.SessionID, snapshot: snapshot.snapshot}
	service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, req AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		captures++
		require.Equal(t, baseline.SessionID, req.SessionID, "capture must target the same full baseline as both capacity gates")
		if captures == 2 {
			_, err := pool.Exec(ctx, `UPDATE sessions SET container_id=NULL,worker_node_id=NULL WHERE org_id=$1 AND id=$2`, org, baseline.SessionID)
			require.NoError(t, err, "lose baseline workspace after a successful warm preflight")
		}
		if captures == 3 {
			return AssessmentInputCaptureResult{}, fmt.Errorf("first transient capture failure after capacity wait")
		}
		return fixture.CaptureAssessmentInputs(ctx, req)
	}), true)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	queued, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "queue evidence when cold headroom is unavailable")
	require.Equal(t, models.CodeReviewWaitCapacity, queued.Schedule.WaitReason, "first attempt must preserve a capacity wait")
	first := queued.Schedule.FirstPendingAt
	require.NotNil(t, first, "first request time must be durable")
	var tracked bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID).Scan(&tracked), "read clock tracking default for a new request")
	require.True(t, tracked, "new requests must distinguish failure episodes from request age")
	_, err = pool.Exec(ctx, `UPDATE code_review_requests SET capture_failure_clock_tracked=false,capture_failure_started_at=$3 WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID, first)
	require.NoError(t, err, "represent a legacy request's old failure episode before actual successful recapture")
	_, err = pool.Exec(ctx, `UPDATE sessions SET container_id='baseline-warm-container',worker_node_id=$3 WHERE org_id=$1 AND id=$2`, org, baseline.SessionID, worker)
	require.NoError(t, err, "warm the exact baseline for preflight")
	claim := func() context.Context {
		token := uuid.New()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=clock_timestamp()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), token).Scan(&id)
		require.NoError(t, err, "claim capacity wake with an exact lease")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), token)
	}
	now = now.Add(30 * time.Second)
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "capacity lost after capture must return to durable wait")
	after, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read recapture wait")
	require.Equal(t, first, after.FirstPendingAt, "same request's capacity retry must preserve its original age")
	require.Equal(t, queued.Schedule.PendingRequestID, after.PendingRequestID, "retry must preserve the same request owner")
	require.Equal(t, 2, captures, "warm preflight must admit one capture before final gate detects loss")
	var started *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_clock_tracked,capture_failure_started_at FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID).Scan(&tracked, &started), "read successful legacy capture before the final capacity wait")
	require.True(t, tracked, "actual successful capture must promote legacy timing to tracked state")
	require.Nil(t, started, "successful capture must durably end a prior episode even when final capacity disappears")
	now = now.Add(20 * time.Minute)
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "cold baseline must defer despite another warm active-session pointer")
	require.Equal(t, 2, captures, "capacity polling must match final baseline and skip provider capture")
	var assessments int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_revision_assessments WHERE org_id=$1`, org).Scan(&assessments), "read allocated assessment clocks")
	require.Equal(t, 2, assessments, "waiting must allocate neither a fresh assessment nor deadline")
	_, err = pool.Exec(ctx, `UPDATE sessions SET container_id='baseline-warm-container',worker_node_id=$3 WHERE org_id=$1 AND id=$2`, org, baseline.SessionID, worker)
	require.NoError(t, err, "restore the exact baseline after the long capacity wait")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "first actual transient failure must retain old intake after capacity reopens")
	afterFailure, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read first actual capture failure")
	require.Equal(t, first, afterFailure.FirstPendingAt, "capture failure must preserve original request age")
	require.Equal(t, queued.Schedule.PendingRequestID, afterFailure.PendingRequestID, "first failure must not discard the durable request")
	require.Equal(t, models.CodeReviewWaitContext, afterFailure.WaitReason, "actual provider failure should own a distinct context retry")
	require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, afterFailure.PendingRequestID).Scan(&started), "read durable capture episode clock")
	require.NotNil(t, started, "actual failure must start a durable episode")
	require.Equal(t, now, started.UTC(), "episode must start at the actual first failure, not intake or capacity wait")
	now = now.Add(15 * time.Second)
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "successful recapture must complete durable admission")
	require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID).Scan(&started), "read ended capture episode")
	require.Nil(t, started, "only actual successful capture should end the episode")
	require.Equal(t, 4, captures, "long capacity wait should skip provider until it opens, then permit one failure and success")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_revision_assessments WHERE org_id=$1`, org).Scan(&assessments), "read newly admitted evidence attempt")
	require.Equal(t, 3, assessments, "recovered capture must allocate exactly one new assessment")
}
