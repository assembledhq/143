package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCaptureIdentityRecoveryRespectsSandboxCapacityPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for capture recovery and capacity integration proof")
	}
	ctx := context.Background()
	// Fleet admission spans tenants, so this case owns a fresh fixture schema
	// independently of the parallel capture-identity recovery cases.
	pool := fullRecoveryPostgresPool(t, ctx)
	f := seedStrandedReview(t, pool, "idle")
	f.job.TriggeringDisputeID = nil
	f.job.RequestedReviewerLogin, f.job.RequestedTeamSlug = "143-code-reviewer", "reviewers"
	f.job.RequestContext = &codereviewsvc.ReviewRequestContext{Source: "issue_comment", AuthorLogin: "reviewer", Body: "Check save behavior", URL: "https://example.invalid/comment/1"}
	_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
	require.NoError(t, err, "retain the queue-to-capture gap before any reviewer runs")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='team_reviewer' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "retain the original reviewer trigger independently of automatic sync authorization")
	revision, err := json.Marshal(map[string]any{"change_reason": "pull_request.synchronize", "request_context": f.job.RequestContext, "pull_request_title": "Saved review"})
	require.NoError(t, err, "encode immutable original human request provenance")
	_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, revision)
	require.NoError(t, err, "persist automatic follow-up authorization with human request context")
	pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "read isolated source PR")
	config := models.DefaultCodeReviewPolicyConfig()
	zero := 0
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
	config.ReviewInstructions = "Policy updated after the source was queued"
	policy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, config, nil)
	require.NoError(t, err, "create current dispatchable policy distinct from queued source identity")
	reads := 0
	snapshot := &queuedIdentitySnapshot{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, AuthorLogin: "author", State: "open", HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}, hook: func() { reads++ }}
	capture := &queuedIdentityCapture{result: codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: snapshot.snapshot}}
	lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, f.stores.Jobs, zerolog.Nop(), codereviewsvc.Config{})
	lifecycle.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	lifecycle.SetSandboxAdmission()
	services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewLifecycle: lifecycle}
	nodeID := "capture-capacity-" + uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO nodes(id,host,mode,status,last_heartbeat_at,metadata) VALUES($1,'synthetic-review-worker','worker','active',clock_timestamp(),'{"sandbox_host_id":"192.0.2.42","max_active_sandboxes":1,"live_sandbox_count":1,"reserved_sandbox_count":0}')`, nodeID)
	require.NoError(t, err, "advertise fresh saturated synthetic worker capacity")
	payload, err := json.Marshal(f.job)
	require.NoError(t, err, "encode source controller identity and request provenance")
	_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3,status='pending',lock_token=NULL,attempts=7,max_attempts=8,target_node_id=$4,priority=100000,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, nodeID)
	require.NoError(t, err, "make the obsolete controller eligible for its final ordinary claim")
	worker := New(pool, zerolog.Nop(), nodeID)
	worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
	worker.poll(ctx)
	var sourceJobStatus models.JobStatus
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&sourceJobStatus), "read settled obsolete controller")
	require.Equal(t, models.JobStatusSucceeded, sourceJobStatus, "source identity drift should settle even while replacement capacity is saturated")
	source, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read immutable source retirement")
	require.Equal(t, models.CodeReviewSessionStatusStale, source.Status, "durable current-input intent must retire the obsolete source")
	require.Equal(t, f.job.PolicyID, source.PolicyID, "source retirement must preserve its original policy identity")
	identity := "capture_identity:" + f.job.SessionID.String()
	assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "pending", nil)
	claimWake := func() context.Context {
		token := uuid.New()
		var wakeID uuid.UUID
		claimErr := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=clock_timestamp()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, f.job.OrgID, "code_review_schedule:"+f.job.PullRequestID.String(), token).Scan(&wakeID)
		require.NoError(t, claimErr, "claim the exact durable replacement wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), token)
	}
	wake := models.CodeReviewScheduleWake{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID}
	require.NoError(t, lifecycle.ReconcileSchedule(claimWake(), wake), "saturated capacity should defer the admitted recovery without starting a new review")
	held, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "read first capacity hold")
	require.Equal(t, models.CodeReviewWaitCapacity, held.WaitReason, "fleet capacity must own the waiting reason")
	require.NotNil(t, held.PendingInput, "capacity hold must retain exact replacement intent")
	var pending struct {
		Input codereviewsvc.ReviewChangedInput `json:"input"`
		Force bool                             `json:"force"`
	}
	require.NoError(t, json.Unmarshal(held.PendingInput, &pending), "decode deferred recovery authorization")
	require.True(t, pending.Force, "capture drift must retain forced fresh review intent")
	require.True(t, pending.Input.AutomaticRecovery, "automatic synchronize admission must survive inherited human trigger")
	require.Equal(t, f.job.RequestContext, pending.Input.RequestContext, "capacity hold must retain original human request context")
	readsBeforeHold := reads
	snapshot.err = errors.New("provider should remain unused during repeated capacity wait")
	require.NoError(t, lifecycle.ReconcileSchedule(claimWake(), wake), "repeated capacity wake should skip provider hydration")
	repeated, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "read repeated capacity hold")
	require.Equal(t, held.PendingInput, repeated.PendingInput, "capacity polling must preserve complete replacement provenance")
	require.Equal(t, held.PendingRequestID, repeated.PendingRequestID, "capacity polling must retain the exact recovery delivery")
	require.Equal(t, held.FirstPendingAt, repeated.FirstPendingAt, "capacity polling must retain original request age")
	require.Equal(t, held.Generation, repeated.Generation, "capacity alone must not invent revision generations")
	require.Equal(t, readsBeforeHold, reads, "capacity polling must avoid additional provider reads")
	var counts []int
	require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_session_metadata WHERE org_id=$1),(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review'),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace')),(SELECT attempts FROM jobs WHERE org_id=$1 AND dedupe_key=$2)]`, f.job.OrgID, "code_review_schedule:"+f.job.PullRequestID.String()).Scan(&counts), "read attempt clocks, controller allocations, fanout and wake budget during saturation")
	require.Equal(t, []int{1, 0, 0, 1, 0, 0}, counts, "capacity must allocate no replacement deadline, controller, assessment or fanout and preserve retry budget")
	assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "pending", nil)
	snapshot.err = nil
	snapshot.snapshot.HeadSHA, snapshot.snapshot.BaseSHA = "current-head-after-capacity", "current-base-after-capacity"
	config.ReviewInstructions = "Policy updated while replacement waited for capacity"
	currentPolicy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, config, nil)
	require.NoError(t, err, "advance policy independently while replacement remains durably queued")
	_, err = pool.Exec(ctx, `UPDATE nodes SET metadata='{"sandbox_host_id":"192.0.2.42","max_active_sandboxes":1,"live_sandbox_count":0,"reserved_sandbox_count":0}',last_heartbeat_at=clock_timestamp() WHERE id=$1`, nodeID)
	require.NoError(t, err, "open the single synthetic worker slot")
	clockFloor := time.Now()
	require.NoError(t, lifecycle.ReconcileSchedule(claimWake(), wake), "capacity reopening must refresh inputs and dispatch one current full replacement")
	latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "read current-input full replacement")
	require.NotEqual(t, source.SessionID, latest.SessionID, "replacement must own a distinct immutable session")
	require.Equal(t, models.CodeReviewSessionStatusQueued, latest.Status, "one current full replacement must be dispatched")
	require.Equal(t, currentPolicy.ID, latest.PolicyID, "replacement must resolve policy after capacity opens")
	require.Equal(t, snapshot.snapshot.HeadSHA, latest.HeadSHA, "replacement must use freshly observed provider head")
	require.Equal(t, snapshot.snapshot.BaseSHA, latest.BaseSHA, "replacement must use freshly observed provider base")
	require.True(t, latest.CreatedAt.After(clockFloor), "replacement deadline must begin after capacity opens")
	assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "satisfied", &latest.SessionID)
	parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, latest.SessionID)
	require.NoError(t, err, "read replacement authorization separate from reviewer trigger")
	var provenance struct {
		AutomaticRecovery bool `json:"capture_identity_automatic"`
	}
	require.NoError(t, json.Unmarshal(parent.RevisionContext, &provenance), "decode immutable replacement authorization")
	require.True(t, provenance.AutomaticRecovery, "replacement must preserve automatic source authorization across the capacity hold")
	var controller codereviewsvc.RunCodeReviewJobPayload
	var replacementPayload json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND payload->>'session_id'=$2`, f.job.OrgID, latest.SessionID.String()).Scan(&replacementPayload), "read exactly one replacement controller")
	require.NoError(t, json.Unmarshal(replacementPayload, &controller), "decode replacement human request provenance")
	require.Equal(t, f.job.RequestContext, controller.RequestContext, "replacement must retain original human request context")
	require.Equal(t, f.job.RequestedReviewerLogin, controller.RequestedReviewerLogin, "replacement must retain reviewer cleanup identity")
	require.Equal(t, f.job.RequestedTeamSlug, controller.RequestedTeamSlug, "replacement must retain team cleanup identity")
	require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_session_metadata WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review'),(SELECT count(*)::int FROM code_review_requests WHERE org_id=$1 AND source_identity=$2),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace'))]`, f.job.OrgID, identity).Scan(&counts), "count immutable replacements and pre-assessment fanout after admission")
	require.Equal(t, []int{2, 2, 1, 0}, counts, "reopening must create only one replacement and preserve one deterministic recovery delivery without reviewer fanout")
}
