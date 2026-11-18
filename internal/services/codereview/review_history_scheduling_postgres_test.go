package codereview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func testSchedulingReviewHistory(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	service, claim, originalID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	completeSchedulingTeamReview(t, pool, org, originalID)
	store := db.NewCodeReviewStore(pool)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow, UseReviewHistory: true}
	requested, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "request a full review with prior-round retrieval")
	require.Equal(t, models.CodeReviewRequestQueued, requested.Disposition, "a completed ordinary review cannot satisfy a history request")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the first history review")
	first, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the first opted-in review")
	require.NotEqual(t, originalID, first.SessionID, "history access should start a fresh session")
	requireScheduledReviewHistoryContext(t, pool, org, first.SessionID)

	joinRequest := request
	joinRequest.RequestID = uuid.New()
	joined, err := service.RequestScheduledReview(ctx, joinRequest)
	require.NoError(t, err, "an equivalent history request can join active work")
	require.Equal(t, models.CodeReviewRequestJoined, joined.Disposition, "matching active history reviews should not fan out another panel")
	require.Equal(t, &first.SessionID, joined.SessionID, "the join should refer to the active history review")

	completeSchedulingTeamReview(t, pool, org, first.SessionID)
	secondRequest := request
	secondRequest.RequestID = uuid.New()
	requested, err = service.RequestScheduledReview(ctx, secondRequest)
	require.NoError(t, err, "request history after an opted-in round completes on the same head")
	require.Equal(t, models.CodeReviewRequestQueued, requested.Disposition, "a new opt-in must be able to retrieve the newest completed round")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the next history review")
	second, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the second opted-in review")
	require.NotEqual(t, first.SessionID, second.SessionID, "the completed opted-in session must not be reused")
	requireScheduledReviewHistoryContext(t, pool, org, second.SessionID)

	replayed, err := service.RequestScheduledReview(ctx, joinRequest)
	require.NoError(t, err, "redeliver the request that joined the first history round")
	require.Equal(t, models.CodeReviewRequestReused, replayed.Disposition, "redelivery should preserve its original completed result")
	require.Equal(t, &first.SessionID, replayed.SessionID, "a later round must not rewrite the original joined result")
	var sessionIDs []uuid.UUID
	err = pool.QueryRow(ctx, `SELECT array_agg(session_id ORDER BY created_at) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&sessionIDs)
	require.NoError(t, err, "list the review sessions created for this tenant and PR")
	require.Equal(t, []uuid.UUID{originalID, first.SessionID, second.SessionID}, sessionIDs, "joins and redeliveries should not create extra sessions")
}

func testSchedulingReviewHistoryNewHead(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	service, claim, originalID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	completeSchedulingTeamReview(t, pool, org, originalID)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow, UseReviewHistory: true}
	_, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "record pending history retrieval intent")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = strings.Repeat("c", 40) })
	_, err = service.HandleReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ChangeKey: "new-history-head"})
	require.NoError(t, err, "move pending history review to new code")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the history review at the new head")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the retargeted history session")
	require.Equal(t, strings.Repeat("c", 40), latest.HeadSHA, "the new review should use the current code")
	requireScheduledReviewHistoryContext(t, pool, org, latest.SessionID)
}

func requireScheduledReviewHistoryContext(t *testing.T, pool *pgxpool.Pool, org, sessionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	session, err := db.NewSessionStore(pool).GetByID(ctx, org, sessionID)
	require.NoError(t, err, "load the history session's revision context")
	expected := &ReviewRequestContext{Source: "ui", UseReviewHistory: true}
	require.Equal(t, expected, codeReviewRevisionRequestContext(session.RevisionContext), "history access should be durable session intent")
	var raw json.RawMessage
	err = pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type=$2 AND payload->>'session_id'=$3`, org, models.JobTypeRunCodeReview, sessionID.String()).Scan(&raw)
	require.NoError(t, err, "read the dispatched worker job")
	var payload RunCodeReviewJobPayload
	require.NoError(t, json.Unmarshal(raw, &payload), "decode the worker payload")
	require.Equal(t, expected, payload.RequestContext, "the worker must receive the same history access intent")
}
