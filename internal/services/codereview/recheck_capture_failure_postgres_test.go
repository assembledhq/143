package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type recheckCaptureFunc func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error)

func (f recheckCaptureFunc) CaptureAssessmentInputs(ctx context.Context, in AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
	return f(ctx, in)
}

// These helpers run inside the migrated lifecycle suite, with an isolated org
// and PR for each parallel table case.
func testRecheckCaptureFailure(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, mode string) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	previous, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "load fixture evidence assessment")
	if mode != "active" && mode != "refresh" {
		require.NoError(t, assessments.Fail(ctx, org, previous.ID, previous.Generation, previous.InputDigest, "fixture finished"), "finish fixture evidence attempt")
		require.NoError(t, schedules.SettleAssessment(ctx, org, previous.ID), "release fixture assessment ownership")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND status IN ('pending','running')`, org)
		require.NoError(t, err, "finish fixture jobs")
	}
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	now := time.Now().UTC()
	service.scheduling.now = func() time.Time { return now }
	captureErr := ErrAssessmentReuseUnavailable
	captures := 0
	service.SetAssessmentContinuation(recheckCaptureFunc(func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		captures++
		return AssessmentInputCaptureResult{}, captureErr
	}), true)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	var automatic models.CodeReviewPRState
	if mode == "automatic" {
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = "new-code-head" })
		_, err := service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr}, models.CodeReviewEnsureCurrent, false, nil)
		require.NoError(t, err, "a push must schedule review of the changed code")
		automatic, err = service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "read automatic review before the failed recheck")
		require.NotNil(t, automatic.PendingInput, "automatic review must retain pending intent")
		require.Nil(t, automatic.PendingRequestID, "automatic intent has no explicit request identity")
	}
	if mode == "mention" {
		service.SetGitHubTriggerStore(&triggerStub{setting: models.CodeReviewGitHubTriggerSetting{OrgID: org, RepositoryID: repo, TeamSlug: "reviewers"}})
		input := ReviewMentionedInput{ReviewRequestedInput: ReviewRequestedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", HeadSHA: snapshot.snapshot.HeadSHA, DeliveryID: request.RequestID.String()}, CommentID: 123, CommentAuthor: "author", CommentBody: "@test/reviewers", CommentURL: "https://github.com/test/repo/pull/17#issuecomment-123"}
		for i := 0; i < 2; i++ {
			result, err := service.HandleReviewMentioned(ctx, input)
			require.NoError(t, err, "recorded failure must acknowledge the webhook rather than retry it")
			require.Equal(t, ReviewRequestedResult{Processed: true, TriggerSource: models.CodeReviewTriggerSourceTeamReviewer, IgnoredReason: "recheck_evidence_unavailable"}, result, "mention must report the terminal capture disposition")
		}
		request.RequestID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("code-review-mention:"+request.RequestID.String()))
	} else if mode == "refresh" {
		require.NoError(t, assessments.MarkRunning(ctx, org, previous.ID, previous.Generation, previous.InputDigest), "start the evidence assessment")
		require.NoError(t, assessments.StageOutcome(ctx, org, previous.ID, previous.Generation, previous.InputDigest, models.CodeReviewAssessmentCompletion{
			ResultOrigin: models.CodeReviewResultExecuted, CoverageComplete: true, Decision: models.CodeReviewDecisionBlocked,
			StructuredOutcome: json.RawMessage(`{"reason":"missing evidence"}`), RenderedBody: "Evidence is still missing.",
		}), "stage the evidence outcome before reserving publication")
		require.NoError(t, assessments.ReservePublication(ctx, org, previous.ID, previous.Generation, previous.InputDigest, previous.HeadSHA), "reserve a publication whose send has not started")
		require.NoError(t, service.RefreshUnsentEvidenceAssessment(ctx, org, previous.ID, "inputs unavailable"), "failed replacement admission should settle durably")
		require.NoError(t, service.RefreshUnsentEvidenceAssessment(ctx, org, previous.ID, "inputs unavailable"), "failed refresh should replay without another capture")
		retired, err := assessments.GetByID(ctx, org, previous.ID)
		require.NoError(t, err, "load retired publication")
		require.Equal(t, models.CodeReviewAssessmentSuperseded, retired.Status, "obsolete publication must stay superseded")
		require.Equal(t, "evidence_recheck_failed:Evidence could not be captured. Try again or request a full review.", *retired.FailureDetail, "failure must remain visible without a repair-loop prefix")
		request.RequestID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("code-review-evidence-refresh:"+previous.ID.String()))
	} else if mode == "permanent wake" || mode == "deadline" || mode == "paused" {
		if mode == "paused" {
			_, err := pool.Exec(ctx, `UPDATE code_review_pr_state SET automatic_paused=true WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
			require.NoError(t, err, "pause automatic reviews while retaining explicit recheck support")
		}
		captureErr = errors.New("temporary provider outage")
		queued, err := service.RequestScheduledReview(ctx, request)
		require.NoError(t, err, "transient failure should retain bounded pending work")
		require.Equal(t, models.CodeReviewRequestQueued, queued.Disposition, "initial capture failure should wait")
		if mode == "permanent wake" || mode == "paused" {
			captureErr = ErrAssessmentReuseUnavailable
		} else {
			now = now.Add(16 * time.Minute)
		}
		var wakeID uuid.UUID
		lease := uuid.New()
		err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&wakeID)
		require.NoError(t, err, "claim the capture recovery wake")
		require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "terminal capture failure must finish the wake successfully")
		var jobStatus string
		require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, org, wakeID).Scan(&jobStatus), "read terminal wake")
		require.Equal(t, "succeeded", jobStatus, "failed admission must not dead-letter the scheduler")
	} else {
		result, err := service.RequestScheduledReview(ctx, request)
		require.ErrorIs(t, err, ErrRecheckUnavailable, "deterministic capture failure should be an actionable recorded rejection")
		require.Equal(t, models.CodeReviewRequestCancelled, result.Disposition, "failed capture must not be reported as queued")
	}
	record, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
	require.NoError(t, err, "read failed request ledger")
	require.Equal(t, "failed", record.Status, "terminal capture failure must be persisted")
	require.Nil(t, record.AssessmentID, "capture failure cannot allocate an assessment")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read terminal scheduling state")
	if mode == "automatic" {
		// WithLockedPR updates the observation timestamp even when the
		// request ledger is the only changed state.
		state.UpdatedAt = automatic.UpdatedAt
		require.Equal(t, automatic, state, "failed capture must preserve the entire unrelated automatic schedule")
		var wakeID uuid.UUID
		lease := uuid.New()
		err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&wakeID)
		require.NoError(t, err, "automatic review wake must survive failed recheck")
		require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "preserved automatic review must still dispatch")
		latest, err := store.GetLatestByPullRequest(ctx, org, pr)
		require.NoError(t, err, "read automatic full review after wake")
		require.Equal(t, "new-code-head", latest.HeadSHA, "changed code must receive its queued full review")
		require.NotEqual(t, previous.SessionID, latest.SessionID, "changed code must use a new full-review session")
		return
	}
	require.Nil(t, state.PendingInput, "terminal capture failure must release pending intent")
	require.Nil(t, state.PendingRequestID, "terminal capture failure must release the pending request")
	require.Nil(t, state.FirstPendingAt, "terminal capture failure must clear the retry window")
	require.Nil(t, state.RetryAt, "terminal capture failure must stop the retry timer")
	if mode == "active" {
		require.Equal(t, &previous.ID, state.ActiveAssessmentID, "capture failure cannot release an active assessment")
		require.Equal(t, models.CodeReviewScheduleRunning, state.State, "active execution must retain its scheduling state")
		return
	}
	require.Equal(t, models.CodeReviewSchedulePaused, state.State, "unavailable evidence should leave a visible stopped schedule")
	require.Equal(t, models.CodeReviewWaitContext, state.WaitReason, "schedule must explain the capture failure")
	if mode == "paused" {
		require.True(t, state.AutomaticPaused, "terminal explicit recheck must preserve the user's automatic pause")
	}
	if mode != "mention" && mode != "refresh" {
		beforeReplay := captures
		_, err := service.RequestScheduledReview(ctx, request)
		require.ErrorIs(t, err, ErrRecheckUnavailable, "redelivery must return the same terminal rejection")
		require.Equal(t, beforeReplay, captures, "redelivery must not repeat failed capture")
		request.RequestID = uuid.New()
		captureErr = errors.New("transient new attempt")
		retry, err := service.RequestScheduledReview(ctx, request)
		require.NoError(t, err, "a new request may retry evidence capture")
		require.Equal(t, models.CodeReviewRequestQueued, retry.Disposition, "a new request must not inherit the previous failure")
		// Settle the new request as well before testing that repair stops.
		captureErr = ErrAssessmentReuseUnavailable
		_, err = service.requestAssessmentReview(ctx, request, true)
		require.ErrorIs(t, err, ErrRecheckUnavailable, "settle the independent retry")
	}
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='failed' WHERE org_id=$1 AND status IN ('pending','running')`, org)
	require.NoError(t, err, "represent exhausted scheduler and supervisor jobs")
	require.NoError(t, schedules.RepairMissingWakes(ctx), "repair sweep should finish normally")
	var pendingJobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND status IN ('pending','running')`, org).Scan(&pendingJobs), "count repaired work")
	require.Equal(t, 0, pendingJobs, "terminal capture failures must not be resurrected by the repair sweep")
}

func testRecheckFailurePreservesNewerRequest(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	newer := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	var queued ScheduleRequestResult
	service.SetAssessmentContinuation(recheckCaptureFunc(func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		hash, err := reviewRequestHash(pr, newer.Mode, nil, false)
		require.NoError(t, err, "hash newer request")
		queued, err = service.queuePendingAssessmentCapture(ctx, newer, repo, nil, hash, "github")
		require.NoError(t, err, "admit newer pending intent during capture")
		return AssessmentInputCaptureResult{}, ErrAssessmentReuseUnavailable
	}), true)
	_, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck})
	require.ErrorIs(t, err, ErrRecheckUnavailable, "the failed capture should settle only its own request")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read surviving pending work")
	require.Equal(t, queued.Schedule.PendingRequestID, state.PendingRequestID, "failure must preserve a concurrently admitted request")
	require.Equal(t, queued.Schedule.PendingInput, state.PendingInput, "failure must preserve the exact newer intent")
	require.Equal(t, queued.Schedule.RetryAt, state.RetryAt, "failure must preserve the newer wake")
}
