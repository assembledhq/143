package codereview

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func testSchedulingRestartLoop(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, newHead bool) {
	ctx := context.Background()
	service, claim, _ := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	store := db.NewCodeReviewStore(pool)
	assessments := db.NewCodeReviewAssessmentStore(pool)
	var previous *uuid.UUID
	var last models.CodeReviewAssessment
	for generation := int64(1); generation <= 3; generation++ {
		metadata, err := store.GetLatestByPullRequest(ctx, org, pr)
		require.NoError(t, err, "read the dispatched full review")
		a, _, err := assessments.Create(ctx, models.CodeReviewAssessmentCapture{
			OrgID: org, RepositoryID: repo, PullRequestID: pr, PolicyID: metadata.PolicyID, SessionID: metadata.SessionID,
			Generation: generation, PreviousAssessmentID: previous, BaseSHA: snapshot.snapshot.BaseSHA, BaseRef: snapshot.snapshot.BaseRef, HeadSHA: snapshot.snapshot.HeadSHA,
			InputVersion: 1, CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: uuid.NewString(), RequestDigest: "request", GateDigest: "gate", InputDigest: uuid.NewString(), InputManifest: json.RawMessage(`{}`),
			ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: metadata.ReviewOutputKey,
		})
		require.NoError(t, err, "capture assessment with unstable visual identity")
		require.NoError(t, assessments.Supersede(ctx, org, a.ID, a.Generation, a.InputDigest, "full_review:inputs changed before publication"), "retire unsent full review")
		_, err = store.MarkStale(ctx, org, a.SessionID, "inputs changed before publication")
		require.NoError(t, err, "mark legacy attempt stale before fallback")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
		require.NoError(t, err, "finish old controller lease")
		require.NoError(t, service.FallbackAssessmentToFull(ctx, org, a.ID, "inputs changed before publication"), "fallback should queue or stop durably")
		state, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "read scheduler after fallback")
		if generation < 3 {
			require.NotEmpty(t, state.PendingInput, "first two failures should admit one replacement")
			require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch replacement through normal scheduling")
		} else {
			require.Nil(t, state.PendingInput, "third failed automatic attempt must not queue a fourth")
			require.Nil(t, state.ActiveSessionID, "cancelled loop must release current session")
		}
		previous = &a.ID
		last = a
	}
	stopped, err := store.GetBySessionID(ctx, org, last.SessionID)
	require.NoError(t, err, "read stopped attempt")
	code := models.CodeReviewStatusCodeLoopDetected
	require.Equal(t, models.CodeReviewSessionStatusCancelled, stopped.Status, "loop must appear cancelled")
	require.False(t, stopped.Stale, "loop cancellation must appear as a current terminal outcome")
	require.Equal(t, &code, stopped.StatusCode, "loop must have an explicit machine-readable reason")
	require.False(t, stopped.RetryableFailure, "generic retry polling must not restart the loop")
	require.NoError(t, service.FallbackAssessmentToFull(ctx, org, last.ID, "inputs changed before publication"), "crash replay must preserve cancellation")
	_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr}, models.CodeReviewEnsureCurrent, false, nil)
	require.NoError(t, err, "automatic observation of unchanged PR should be accepted without work")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read unchanged revision after automatic observation")
	require.Nil(t, state.PendingInput, "unchanged automatic event must not reopen the loop")
	if newHead {
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = "new-head" })
		_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr}, models.CodeReviewEnsureCurrent, false, nil)
	} else {
		_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
	}
	require.NoError(t, err, "new revision or explicit retry must reopen admission")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch deliberately resumed review")
	resumed, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read resumed full review")
	require.NotEqual(t, last.SessionID, resumed.SessionID, "resuming must create a new review attempt")
	require.Equal(t, snapshot.snapshot.HeadSHA, resumed.HeadSHA, "resumed review must use the current head")
}
