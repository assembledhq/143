package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type forceEvidenceCanceller struct {
	pool         *pgxpool.Pool
	prID         uuid.UUID
	assessmentID uuid.UUID
	threadID     uuid.UUID
	calls        int
	fail         bool
}

func testForceFreshBaselineRedispatch(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	testForceFreshEvidenceCancellation(t, pool, org, repo, pr, snapshot, "running")
	ctx := context.Background()
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	rechecks := db.NewCodeReviewRecheckStore(pool)
	threads := db.NewSessionThreadStore(pool)
	store := db.NewCodeReviewStore(pool)
	old, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read the cancelled evidence assessment")
	oldDispatch, err := rechecks.Get(ctx, org, old.ID)
	require.NoError(t, err, "read the drained old evidence turn")
	oldThread, err := threads.GetByID(ctx, org, oldDispatch.ThreadID)
	require.NoError(t, err, "read baseline thread after cancellation drain")
	require.NotNil(t, oldThread.CancelRequestedAt, "the cancelled turn should retain its interrupt timestamp until new ownership")
	require.Equal(t, models.ThreadStatusIdle, oldThread.Status, "old runtime drain should return the baseline thread to idle")
	state, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read the replacement that is about to fail")
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE code_review_session_metadata SET status='failed',completed_at=now() WHERE org_id=$1 AND session_id=$2`, []any{org, *state.ActiveSessionID}},
		{`UPDATE sessions SET status='failed' WHERE org_id=$1 AND id=$2`, []any{org, *state.ActiveSessionID}},
		{`UPDATE session_threads SET status='failed' WHERE org_id=$1 AND session_id=$2`, []any{org, *state.ActiveSessionID}},
		{`UPDATE jobs SET status='dead_letter',lock_token=NULL,lease_expires_at=NULL WHERE org_id=$1 AND payload->>'session_id'=$2`, []any{org, state.ActiveSessionID.String()}},
		{`UPDATE jobs SET status='succeeded',lock_token=NULL,lease_expires_at=NULL WHERE org_id=$1 AND id=$2`, []any{org, oldDispatch.JobID}},
	} {
		_, err := pool.Exec(ctx, seed.sql, seed.args...)
		require.NoError(t, err, "finish failed replacement and old continuation worker")
	}
	require.NoError(t, schedules.ReconcileTerminalReviews(ctx, org, repo, pr), "repair failed replacement admission state")
	var manifest ReviewInputManifest
	require.NoError(t, json.Unmarshal(old.InputManifest, &manifest), "reuse evidence inputs against the surviving completed baseline")
	policy, err := store.GetPolicyByID(ctx, org, old.PolicyID)
	require.NoError(t, err, "read baseline continuation policy")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	service.SetAssessmentContinuation(assessmentAdmissionFixture{pool: pool, manifest: manifest, policy: policy, session: old.SessionID, snapshot: snapshot.snapshot}, true)
	result, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck})
	require.NoError(t, err, "a failed replacement must leave the completed baseline eligible for a later recheck")
	require.NotNil(t, result.AssessmentID, "later recheck should receive a new evidence assessment")
	next, err := assessments.GetByID(ctx, org, *result.AssessmentID)
	require.NoError(t, err, "read newly admitted evidence assessment")
	require.Equal(t, old.SessionID, next.SessionID, "later evidence must reuse the completed baseline conversation")
	require.NoError(t, assessments.MarkRunning(ctx, org, next.ID, next.Generation, next.InputDigest), "start the new evidence assessment")
	in := models.CodeReviewRecheckDispatchInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, AssessmentID: next.ID, SessionID: next.SessionID, ThreadID: oldDispatch.ThreadID, ExpectedTurn: oldDispatch.ExpectedTurn + 1, Prompt: "Verify the evidence again"}
	dispatch, reused, err := rechecks.Dispatch(ctx, in)
	require.NoError(t, err, "dispatch the next turn on the surviving baseline")
	require.False(t, reused, "later assessment must receive a new dispatch")
	thread, err := threads.GetByID(ctx, org, in.ThreadID)
	require.NoError(t, err, "read the exact new continuation ownership")
	require.Nil(t, thread.CancelRequestedAt, "new dispatch must clear the old timestamp that continue_session would otherwise reject")
	marked, err := threads.MarkCancelRequestedForTurn(ctx, org, in.SessionID, in.ThreadID, oldDispatch.ExpectedTurn)
	require.NoError(t, err, "deliver a stale exact-turn interrupt after redispatch")
	require.False(t, marked, "an old interrupt must not mark the newly owned turn")
	lease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND id=$2`, org, dispatch.JobID, lease)
	require.NoError(t, err, "lease the new evidence continuation")
	claimed, err := rechecks.Claim(ctx, org, next.ID, dispatch.JobID, lease, in.SessionID, in.ThreadID, in.ExpectedTurn, dispatch.MessageID)
	require.NoError(t, err, "claim the exact later evidence turn")
	require.True(t, claimed, "later evidence turn should execute after the old cancellation drains")
	marked, err = threads.MarkCancelRequestedForTurn(ctx, org, in.SessionID, in.ThreadID, in.ExpectedTurn)
	require.NoError(t, err, "request cancellation for the current turn")
	require.True(t, marked, "current-turn cancellation must still take effect")
	thread, err = threads.GetByID(ctx, org, in.ThreadID)
	require.NoError(t, err, "read a fresh current-turn cancellation")
	require.NotNil(t, thread.CancelRequestedAt, "current cancellation must remain visible to continuation execution")
	_, reused, err = rechecks.Dispatch(ctx, in)
	require.NoError(t, err, "replay the same dispatch after current-turn cancellation")
	require.True(t, reused, "dispatch replay must keep original turn ownership")
	replayed, err := threads.GetByID(ctx, org, in.ThreadID)
	require.NoError(t, err, "read the replayed current turn")
	require.Equal(t, thread.CancelRequestedAt, replayed.CancelRequestedAt, "dispatch replay must never erase a fresh cancellation")
	activeSession, err := db.NewSessionStore(pool).GetByID(ctx, org, in.SessionID)
	require.NoError(t, err, "read the later turn's active session")
	activeDispatch, err := rechecks.Get(ctx, org, next.ID)
	require.NoError(t, err, "read the later turn's claimed dispatch")
	released, err := rechecks.ReconcileDrainedTerminalTurn(ctx, org, old.ID)
	require.NoError(t, err, "reconciling the old dispatch must succeed while a later turn is running")
	require.False(t, released, "the historical dispatch must not release a later turn")
	for sweep := 0; sweep < 2; sweep++ {
		require.NoError(t, schedules.RepairMissingWakes(ctx), "repeat repair while the cancelled baseline has a newer running turn")
	}
	repairedThread, err := threads.GetByID(ctx, org, in.ThreadID)
	require.NoError(t, err, "read later thread after historical cancellation repair")
	require.Equal(t, replayed, repairedThread, "old cancellation repair must preserve the later running thread and its fresh cancellation")
	repairedSession, err := db.NewSessionStore(pool).GetByID(ctx, org, in.SessionID)
	require.NoError(t, err, "read later session after historical cancellation repair")
	require.Equal(t, activeSession, repairedSession, "historical dispatch repair must not return a later running session to idle")
	repairedDispatch, err := rechecks.Get(ctx, org, next.ID)
	require.NoError(t, err, "read later dispatch after historical cancellation repair")
	require.Equal(t, activeDispatch, repairedDispatch, "old cancellation repair must preserve later turn ownership")
}

func testForceFreshHistoricalCancellation(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	testForceFreshEvidenceCancellation(t, pool, org, repo, pr, snapshot, "running")
	ctx := context.Background()
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	rechecks := db.NewCodeReviewRecheckStore(pool)
	old, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read historical cancelled evidence assessment")
	dispatch, err := rechecks.Get(ctx, org, old.ID)
	require.NoError(t, err, "read historical cancelled dispatch")
	thread, err := db.NewSessionThreadStore(pool).GetByID(ctx, org, dispatch.ThreadID)
	require.NoError(t, err, "read the historical drained thread")
	canceller := &forceEvidenceCanceller{pool: pool, prID: pr, assessmentID: old.ID, threadID: dispatch.ThreadID}
	store := db.NewCodeReviewStore(pool)
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	service.SetAssessmentContinuation(assessmentAdmissionNoCapture{}, true)
	service.SetThreadCanceller(canceller)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewForceFresh, Reason: "Replace the active full review"})
		require.NoError(t, err, "later force requests should target current full review work")
		after, err := assessments.GetByID(ctx, org, old.ID)
		require.NoError(t, err, "read historical assessment after a later force request")
		require.Equal(t, old, after, "later force requests must not rewrite historical assessment cancellation provenance")
		turn, err := rechecks.Get(ctx, org, old.ID)
		require.NoError(t, err, "read historical dispatch after a later force request")
		require.Equal(t, dispatch, turn, "later force requests must not rewrite historical dispatch timestamps or request identity")
		afterThread, err := db.NewSessionThreadStore(pool).GetByID(ctx, org, dispatch.ThreadID)
		require.NoError(t, err, "read historical thread after a later force request")
		require.Equal(t, thread, afterThread, "later force requests must leave the drained historical turn untouched")
	}
	require.Equal(t, 0, canceller.calls, "drained historical evidence must not receive more interrupts")
}

func (*forceEvidenceCanceller) CancelActiveThreads(context.Context, uuid.UUID, []uuid.UUID) (int, error) {
	return 0, errors.New("force fresh must cancel the evidence thread without cancelling the completed baseline")
}

func (c *forceEvidenceCanceller) CancelThreadTurn(ctx context.Context, orgID, sessionID, threadID uuid.UUID, expectedTurn int) (models.SessionThread, error) {
	c.calls++
	if threadID != c.threadID || expectedTurn != 1 {
		return models.SessionThread{}, errors.New("cancellation targeted a different thread")
	}
	var durable bool
	err := c.pool.QueryRow(ctx, `SELECT pending_input->>'mode'='force_fresh' AND pending_request_id IS NOT NULL
 AND EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.id=$3 AND a.status='cancelled' AND a.failure_detail LIKE 'force_fresh:%')
 AND EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=$1 AND d.assessment_id=$3 AND d.thread_id=$4 AND d.status='cancelled')
 FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, orgID, c.prID, c.assessmentID, threadID).Scan(&durable)
	if err != nil {
		return models.SessionThread{}, err
	}
	if !durable {
		return models.SessionThread{}, errors.New("interrupt preceded durable replacement and exact cancellation")
	}
	if c.fail {
		return models.SessionThread{}, errors.New("interrupt delivery unavailable")
	}
	if _, err := c.pool.Exec(ctx, `UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND id=$2 AND session_id=$3`, orgID, threadID, sessionID); err != nil {
		return models.SessionThread{}, err
	}
	return db.NewSessionThreadStore(c.pool).GetByID(ctx, orgID, threadID)
}

// The lifecycle suite isolates each mode by organization and PR.
func testForceFreshEvidenceCancellation(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, mode string) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	evidence, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read the active evidence assessment")
	baseline, err := schedules.GetLatestFullBaseline(ctx, org, pr)
	require.NoError(t, err, "read the completed full baseline")
	metadata, err := store.GetBySessionID(ctx, org, baseline.SessionID)
	require.NoError(t, err, "read original full review metadata")
	threadID := uuid.New()
	canceller := &forceEvidenceCanceller{pool: pool, prID: pr, assessmentID: evidence.ID, threadID: threadID, fail: mode == "retry"}
	newService := func() *Service {
		service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
		service.SetScheduling(schedules, snapshot)
		service.SetAssessmentContinuation(assessmentAdmissionNoCapture{}, true)
		service.SetThreadCanceller(canceller)
		return service
	}
	service := newService()
	hasDispatch := mode == "queued" || mode == "running" || mode == "draining" || mode == "retry"
	var dispatch models.CodeReviewRecheckDispatch
	if mode != "reserved" {
		require.NoError(t, assessments.MarkRunning(ctx, org, evidence.ID, evidence.Generation, evidence.InputDigest), "start the evidence assessment")
	}
	if hasDispatch {
		_, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,$3,'codex','Evidence turn','idle','review','read_only')`, threadID, org, evidence.SessionID)
		require.NoError(t, err, "seed the exact reused orchestrator thread")
		dispatch, _, err = db.NewCodeReviewRecheckStore(pool).Dispatch(ctx, models.CodeReviewRecheckDispatchInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, AssessmentID: evidence.ID, SessionID: evidence.SessionID, ThreadID: threadID, ExpectedTurn: 1, Prompt: "Verify evidence"})
		require.NoError(t, err, "dispatch the evidence-only turn")
		if mode != "queued" {
			lease := uuid.New()
			_, err := pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND id=$2`, org, dispatch.JobID, lease)
			require.NoError(t, err, "lease the exact continuation job")
			owned, err := db.NewCodeReviewRecheckStore(pool).Claim(ctx, org, evidence.ID, dispatch.JobID, lease, evidence.SessionID, threadID, 1, dispatch.MessageID)
			require.NoError(t, err, "claim the evidence-only turn")
			require.True(t, owned, "the fixture should own its exact continuation turn")
		}
		if mode == "draining" {
			_, err := pool.Exec(ctx, `UPDATE sessions SET container_id='still-draining' WHERE org_id=$1 AND id=$2`, org, evidence.SessionID)
			require.NoError(t, err, "keep an actual runtime owner after logical cancellation")
		}
	}
	protected := mode == "uncertain" || mode == "confirmed" || mode == "receipt"
	if mode == "publication_reserved" || protected {
		publication := "reserved"
		if mode == "uncertain" || mode == "confirmed" {
			publication = mode
		}
		_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state=$3,result_origin='evidence_only' WHERE org_id=$1 AND id=$2`, org, evidence.ID, publication)
		require.NoError(t, err, "reserve the evidence publication state")
		if mode == "receipt" {
			_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_receipt='{}' WHERE org_id=$1 AND id=$2`, org, evidence.ID)
			require.NoError(t, err, "preserve an existing external receipt")
		}
	}
	before, err := assessments.GetByID(ctx, org, evidence.ID)
	require.NoError(t, err, "read evidence before the explicit force request")
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewForceFresh, Reason: "Run a new reviewer panel"}
	_, err = service.RequestScheduledReview(ctx, request)
	if mode == "retry" {
		require.ErrorContains(t, err, "interrupt delivery unavailable", "delivery failure should surface after durable cancellation")
		canceller.fail = false
		service = newService()
	} else {
		require.NoError(t, err, "force fresh should durably admit the replacement")
	}
	originalRequest, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
	require.NoError(t, err, "read the force request that originally cancelled evidence")
	originalCancellation, err := assessments.GetByID(ctx, org, evidence.ID)
	require.NoError(t, err, "read original cancellation provenance")
	var originalDispatch models.CodeReviewRecheckDispatch
	var originalThread models.SessionThread
	if hasDispatch {
		originalDispatch, err = db.NewCodeReviewRecheckStore(pool).Get(ctx, org, evidence.ID)
		require.NoError(t, err, "read original dispatch cancellation provenance")
		originalThread, err = db.NewSessionThreadStore(pool).GetByID(ctx, org, threadID)
		require.NoError(t, err, "read original exact-turn cancellation timestamp")
	}
	if mode == "draining" {
		request.RequestID = uuid.New()
		_, err := service.RequestScheduledReview(ctx, request)
		require.NoError(t, err, "a newer force request should replace pending intent while old runtime cancellation drains")
	}
	state, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read the committed force request")
	require.NotNil(t, state.PendingRequestID, "force fresh must preserve its request identity while replacing evidence")
	after, err := assessments.GetByID(ctx, org, evidence.ID)
	require.NoError(t, err, "read retired or protected evidence")
	if protected {
		require.Equal(t, before, after, "force fresh cannot alter uncertain publication or an external receipt")
		require.Equal(t, &evidence.ID, state.ActiveAssessmentID, "protected publication must retain active ownership")
	} else {
		require.Equal(t, models.CodeReviewAssessmentCancelled, after.Status, "force fresh must stop the obsolete evidence assessment")
		require.Equal(t, "force_fresh:"+originalRequest.ID.String(), *after.FailureDetail, "cancellation marker must retain the original request even when replacement admission changes owners")
		if hasDispatch {
			turn, err := db.NewCodeReviewRecheckStore(pool).Get(ctx, org, evidence.ID)
			require.NoError(t, err, "read the exact cancelled dispatch")
			require.Equal(t, models.CodeReviewRecheckDispatchCancelled, turn.Status, "old dispatch must lose completion authority before interrupt delivery")
		}
	}
	if mode == "draining" {
		require.Equal(t, originalCancellation, after, "a second force request must preserve original assessment cancellation provenance")
		turn, err := db.NewCodeReviewRecheckStore(pool).Get(ctx, org, evidence.ID)
		require.NoError(t, err, "read unchanged cancellation dispatch after second force")
		require.Equal(t, originalDispatch, turn, "a second force request must not rewrite dispatch cancellation timestamps or reason")
	}
	claim := func() context.Context {
		var wakeID uuid.UUID
		lease := uuid.New()
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&wakeID)
		require.NoError(t, err, "claim the replacement scheduler wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease)
	}
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "scheduler should recover cancellation and respect actual drain")
	if mode == "retry" {
		retriedCancellation, err := assessments.GetByID(ctx, org, evidence.ID)
		require.NoError(t, err, "read cancellation after interrupt retry")
		require.Equal(t, originalCancellation, retriedCancellation, "retry must preserve assessment cancellation provenance")
		turn, err := db.NewCodeReviewRecheckStore(pool).Get(ctx, org, evidence.ID)
		require.NoError(t, err, "read dispatch after interrupt retry")
		require.Equal(t, originalDispatch, turn, "retry must preserve dispatch timestamps and original marker")
		thread, err := db.NewSessionThreadStore(pool).GetByID(ctx, org, threadID)
		require.NoError(t, err, "read thread after interrupt retry")
		require.Equal(t, originalThread.CancelRequestedAt, thread.CancelRequestedAt, "retry must not restamp the original cancellation request")
	}
	if protected || mode == "draining" {
		var sessions int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&sessions), "count review sessions before publication reconciliation or drain")
		require.Equal(t, 1, sessions, "replacement must wait for protected publication or actual runtime drain")
		if protected {
			require.Equal(t, 0, canceller.calls, "protected publication must not be interrupted")
			return
		}
		_, err := pool.Exec(ctx, `UPDATE sessions SET container_id=NULL WHERE org_id=$1 AND id=$2`, org, evidence.SessionID)
		require.NoError(t, err, "finish draining the old evidence runtime")
		require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "replacement should start once the old runtime drains")
	}
	state, err = schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read the fresh full review admission")
	require.NotNil(t, state.ActiveSessionID, "force fresh must start a new full-review session")
	require.NotEqual(t, &evidence.SessionID, state.ActiveSessionID, "fresh full review must not reuse the evidence conversation")
	require.Nil(t, state.PendingInput, "replacement should consume its durable pending intent")
	latestRequest, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
	require.NoError(t, err, "read the latest force request after replacement admission")
	require.Equal(t, state.ActiveSessionID, latestRequest.SessionID, "the latest force request must own the single replacement")
	unchanged, err := assessments.GetByID(ctx, org, baseline.ID)
	require.NoError(t, err, "read historical baseline after cancellation")
	require.Equal(t, baseline, unchanged, "evidence cancellation must preserve the completed full baseline")
	unchangedMetadata, err := store.GetBySessionID(ctx, org, metadata.SessionID)
	require.NoError(t, err, "read historical full metadata after cancellation")
	require.Equal(t, metadata, unchangedMetadata, "force fresh cannot mark the completed full review stale")
	wantCalls := 0
	if hasDispatch {
		wantCalls = 1
	}
	if mode == "retry" {
		wantCalls = 2
	}
	require.Equal(t, wantCalls, canceller.calls, fmt.Sprintf("%s must deliver only the exact old evidence interrupt", mode))
	_, err = service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "replaying force fresh must retain the same replacement")
	require.Equal(t, wantCalls, canceller.calls, "force request replay must not cancel the newly admitted review")
}
