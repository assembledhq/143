package codereview

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func testSchedulingCompletedRecheckDrain(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	schedules := db.NewCodeReviewScheduleStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	evidence, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "load current evidence assessment")
	require.NoError(t, assessments.MarkRunning(ctx, org, evidence.ID, evidence.Generation, evidence.InputDigest), "start evidence analysis")
	threadID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,$3,'codex','Evidence turn','idle','review','read_only')`, threadID, org, evidence.SessionID)
	require.NoError(t, err, "seed the exact evidence conversation thread")
	dispatch, _, err := db.NewCodeReviewRecheckStore(pool).Dispatch(ctx, models.CodeReviewRecheckDispatchInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, AssessmentID: evidence.ID, SessionID: evidence.SessionID, ThreadID: threadID, ExpectedTurn: 1, Prompt: "Verify evidence"})
	require.NoError(t, err, "dispatch the owned evidence turn")
	_, err = pool.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='completed',completed_at=now() WHERE org_id=$1 AND assessment_id=$2`, org, evidence.ID)
	require.NoError(t, err, "represent completed agent output before assessment publication")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=1 WHERE org_id=$1 AND id=$2`, org, threadID)
	require.NoError(t, err, "agent has completed the expected evidence turn")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, org, dispatch.JobID)
	require.NoError(t, err, "completed agent job no longer supplies an admission blocker")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='idle',container_id='completed-turn-still-draining' WHERE org_id=$1 AND id=$2`, org, evidence.SessionID)
	require.NoError(t, err, "completed evidence output still has a live runtime owner")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=now(),result_origin='evidence_only',decision='blocked',acceptable=false,structured_outcome='{}' WHERE org_id=$1 AND id=$2`, org, evidence.ID)
	require.NoError(t, err, "represent a naturally completed historical evidence assessment")
	require.NoError(t, schedules.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, _ *models.CodeReviewPRState) error {
		active, err := db.HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
		if err != nil {
			return err
		}
		require.False(t, active, "a historical completed turn without force cancellation cannot block fresh admission merely for a warm runtime")
		return nil
	}), "check unchanged historical completed admission semantics")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='running',completed_at=NULL WHERE org_id=$1 AND id=$2`, org, evidence.ID)
	require.NoError(t, err, "restore the in-progress assessment whose agent turn already completed")
	store := db.NewCodeReviewStore(pool)
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	service.SetAssessmentContinuation(assessmentAdmissionNoCapture{}, true)
	_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewForceFresh, Reason: "Start a fresh full panel"})
	require.NoError(t, err, "force fresh may cancel the unsent assessment after its agent output completes")
	cancelled, err := assessments.GetByID(ctx, org, evidence.ID)
	require.NoError(t, err, "read cancelled evidence assessment")
	require.Equal(t, models.CodeReviewAssessmentCancelled, cancelled.Status, "completed agent output does not prevent cancelling obsolete assessment")
	turn, err := db.NewCodeReviewRecheckStore(pool).Get(ctx, org, evidence.ID)
	require.NoError(t, err, "read completed dispatch")
	require.Equal(t, models.CodeReviewRecheckDispatchCompleted, turn.Status, "force fresh preserves the completed turn's result provenance")
	claim := func() context.Context {
		var id uuid.UUID
		lease := uuid.New()
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&id)
		require.NoError(t, err, "claim force-fresh reconciliation wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), lease)
	}
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "fresh full panel must wait for actual runtime drain")
	waiting, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read pending replacement")
	require.Equal(t, models.CodeReviewWaitActive, waiting.WaitReason, "completed dispatch with live runtime remains an admission blocker")
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&count), "count admitted full panels")
	require.Equal(t, 1, count, "runtime drain must finish before allocating replacement full session")
	_, err = pool.Exec(ctx, `UPDATE sessions SET container_id=NULL WHERE org_id=$1 AND id=$2`, org, evidence.SessionID)
	require.NoError(t, err, "finish draining completed evidence turn")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "drained completed turn releases replacement admission")
	started, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read fresh full panel")
	require.NotNil(t, started.ActiveSessionID, "replacement starts after drain")
	require.NotEqual(t, evidence.SessionID, *started.ActiveSessionID, "force fresh allocates a new full panel")
	require.Nil(t, started.PendingInput, "admitted full panel consumes pending intent")
}
