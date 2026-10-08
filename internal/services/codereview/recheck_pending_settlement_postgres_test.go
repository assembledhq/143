package codereview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Each case runs in the migrated lifecycle suite with its own org and PR.
func testRecheckPendingSettlement(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, mode string) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	current, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read the existing evidence assessment")
	completed := mode == "reuse" || mode == "replay_reuse" || strings.HasPrefix(mode, "closed")
	if completed {
		_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',result_origin='executed',coverage_complete=true,decision='blocked',acceptable=false,structured_outcome='{"coverage_complete":true}',publication_state='not_required',completed_at=now() WHERE org_id=$1 AND id=$2`, org, current.ID)
		require.NoError(t, err, "complete the reusable evidence assessment")
		require.NoError(t, schedules.SettleAssessment(ctx, org, current.ID), "release completed assessment ownership")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND status IN ('pending','running')`, org)
		require.NoError(t, err, "finish jobs for completed fixture work")
	}
	manifest, err := decodeAssessmentManifest(current.InputManifest)
	require.NoError(t, err, "decode the identical capture input")
	policy, err := store.ResolvePolicy(ctx, org)
	require.NoError(t, err, "read the continuation policy")
	require.NotNil(t, policy.Policy, "the capture needs its saved policy identity")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	service.SetAssessmentContinuation(recheckCaptureFunc(func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		return AssessmentInputCaptureResult{}, errors.New("temporary capture outage")
	}), true)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	queued, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "transient capture failure should durably queue the request")
	require.Equal(t, models.CodeReviewRequestQueued, queued.Disposition, "capture recovery must start with queued intent")
	require.NotNil(t, queued.Schedule.PendingRequestID, "queued capture must own the pending request")
	require.NotNil(t, queued.Schedule.FirstPendingAt, "queued capture must have a bounded retry window")
	require.NotNil(t, queued.Schedule.RetryAt, "queued capture must have a retry wake")
	if strings.HasPrefix(mode, "replay_") {
		status := "joined"
		if completed {
			status = "satisfied"
		}
		_, err = pool.Exec(ctx, `UPDATE code_review_requests SET status=$3,assessment_id=$4,session_id=$5 WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID, status, current.ID, current.SessionID)
		require.NoError(t, err, "simulate a settled request whose old pending intent survived")
	}
	fixture := assessmentAdmissionFixture{pool: pool, manifest: manifest, policy: *policy.Policy, session: current.SessionID, snapshot: snapshot.snapshot}
	var newer ScheduleRequestResult
	captures := 0
	service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, input AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		captures++
		if strings.HasSuffix(mode, "newer") {
			newRequest := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
			hash, err := reviewRequestHash(pr, newRequest.Mode, nil, false)
			require.NoError(t, err, "hash the concurrently admitted request")
			newer, err = service.queuePendingAssessmentCapture(ctx, newRequest, repo, nil, hash, "github")
			require.NoError(t, err, "admit a newer request while the old capture is outside the PR lock")
		}
		if strings.HasPrefix(mode, "closed") {
			closed := snapshot.snapshot
			closed.State = "closed"
			return AssessmentInputCaptureResult{Snapshot: closed}, ErrReviewIneligible
		}
		return fixture.CaptureAssessmentInputs(ctx, input)
	}), true)
	if strings.HasSuffix(mode, "newer") {
		_, err = service.requestAssessmentReview(ctx, request, true)
		require.NoError(t, err, "settling the old request should preserve the concurrent request")
		state, err := schedules.Get(ctx, org, pr)
		require.NoError(t, err, "read the newer pending state")
		state.UpdatedAt = newer.Schedule.UpdatedAt
		require.Equal(t, newer.Schedule, state, "old capture settlement must preserve every field of newer pending work and active ownership")
		record, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
		require.NoError(t, err, "read the superseded request")
		require.Equal(t, "superseded", record.Status, "old capture must not revive an already superseded request")
		return
	}
	var wakeID uuid.UUID
	lease := uuid.New()
	err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&wakeID)
	require.NoError(t, err, "claim the queued capture wake")
	require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "recovered capture should finish its scheduler wake")
	var wakeStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, org, wakeID).Scan(&wakeStatus), "read the settled wake")
	require.Equal(t, "succeeded", wakeStatus, "settled capture must finish rather than retry every fifteen seconds")
	state, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read settled pending state")
	expected := queued.Schedule
	expected.PendingInput, expected.PendingRequestID = nil, nil
	expected.FirstPendingAt, expected.RetryAt, expected.EligibleAt = nil, nil, nil
	expected.WaitReason = models.CodeReviewWaitNone
	expected.State = models.CodeReviewScheduleRunning
	status := "joined"
	if completed {
		expected.State, status = models.CodeReviewScheduleCovered, "satisfied"
	}
	if mode == "closed" {
		expected.State, status = models.CodeReviewScheduleClosed, "cancelled"
	}
	expected.UpdatedAt = state.UpdatedAt
	require.Equal(t, expected, state, "settlement should clear only pending intent and restore the correct state without replacing active ownership")
	record, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
	require.NoError(t, err, "read the settled request ledger")
	require.Equal(t, status, record.Status, "the request ledger should record the recovery outcome")
	if mode != "closed" {
		require.Equal(t, &current.ID, record.AssessmentID, "settled admission should retain the existing assessment")
		require.Equal(t, &current.SessionID, record.SessionID, "settled admission should retain the existing session")
	}
	_, err = service.requestAssessmentReview(ctx, request, true)
	require.NoError(t, err, "repeating recovery should be idempotent")
	replayed, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read state after repeated recovery")
	replayed.UpdatedAt = state.UpdatedAt
	require.Equal(t, state, replayed, "repeated recovery must not resurrect pending intent or change ownership")
	require.Equal(t, 2, captures, "each explicit recovery should capture once")
	require.NoError(t, schedules.RepairMissingWakes(ctx), "the repair sweep should accept settled state")
	var pendingWakes int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type=$2 AND status IN ('pending','running')`, org, models.JobTypeReconcileCodeReviewSchedule).Scan(&pendingWakes), "count scheduler wakes after repair")
	require.Equal(t, 0, pendingWakes, "the repair sweep must not resurrect a settled capture wake")
}
