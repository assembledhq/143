package codereview

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func testSchedulingTeamRerequest(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, status models.CodeReviewSessionStatus, decision models.CodeReviewDecision) {
	t.Helper()
	ctx := context.Background()
	service, claim, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	service.cfg.TeamSlugs = []string{"143-code-reviewer"}
	store := db.NewCodeReviewStore(pool)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read source generation before the GitHub request")
	if status == models.CodeReviewSessionStatusCompleted {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision=$3,completed_at=now() WHERE org_id=$1 AND session_id=$2`, org, sourceID, decision)
		require.NoError(t, err, "complete the unchanged source review")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
		require.NoError(t, err, "release the completed source controller")
		_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status) VALUES($1,$2,'codex','reviewer','completed')`, org, sourceID)
		require.NoError(t, err, "represent a substantive review rather than a deterministic early stop")
	} else {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status=$3 WHERE org_id=$1 AND session_id=$2`, org, sourceID, status)
		require.NoError(t, err, "set the existing active review phase")
	}
	input := ReviewRequestedInput{
		OrgID: org, RepositoryID: repo, PullRequestID: pr,
		GitHubRepo: "test/repo", GitHubPRNumber: snapshot.snapshot.Number,
		HeadSHA: snapshot.snapshot.HeadSHA, BaseSHA: snapshot.snapshot.BaseSHA,
		RequestedTeam: "143-code-reviewer", DeliveryID: uuid.NewString(),
	}
	result, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "accept a genuinely new GitHub team review request")
	if status != models.CodeReviewSessionStatusCompleted {
		expected := ReviewRequestedResult{Processed: true, Reused: true, Deferred: true, SessionID: sourceID}
		require.Equal(t, expected, result, "equivalent queued or running work should be joined without cancellation")
		replay, err := service.HandleReviewRequested(ctx, input)
		require.NoError(t, err, "redeliver the joined GitHub request")
		require.Equal(t, expected, replay, "redelivery should keep the same active review")
		current, err := store.GetBySessionID(ctx, org, sourceID)
		require.NoError(t, err, "read joined source review")
		require.Equal(t, status, current.Status, "joining must not retire active work")
		joined, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "read joined schedule generation")
		require.Equal(t, before.Generation, joined.Generation, "joining active work must preserve its generation")
		allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sourceID)
		require.NoError(t, err, "validate the joined full review")
		require.True(t, allowed, "joining must preserve the original review's execution authority")
		completeSchedulingTeamReview(t, pool, org, sourceID)
		replay, err = service.HandleReviewRequested(ctx, input)
		require.NoError(t, err, "redeliver the joined request after its review completes")
		expected = ReviewRequestedResult{Processed: true, Reused: true, SessionID: sourceID}
		require.Equal(t, expected, replay, "a completed join must settle against its original session")
		newInput := input
		newInput.DeliveryID = uuid.NewString()
		_, err = service.HandleReviewRequested(ctx, newInput)
		require.NoError(t, err, "request a distinct full review after the joined review completes")
		require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the later team review")
		latest, err := store.GetLatestByPullRequest(ctx, org, pr)
		require.NoError(t, err, "read the later review session")
		require.NotEqual(t, sourceID, latest.SessionID, "a distinct request must dispatch its own full review")
		replay, err = service.HandleReviewRequested(ctx, input)
		require.NoError(t, err, "redeliver the original joined request after a later review starts")
		require.Equal(t, expected, replay, "a later review must not rewrite the completed join's session linkage")
		return
	}
	require.Equal(t, ReviewRequestedResult{Processed: true, Deferred: true}, result, "a new team request must queue fresh work instead of satisfying itself with the completed review")
	pending, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read the durable fresh-review intent")
	require.Equal(t, before.Generation+1, pending.Generation, "an unchanged completed revision should receive a new review generation")
	replay, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "redeliver the pending request before dispatch")
	require.Equal(t, ReviewRequestedResult{Processed: true, Reused: true, Deferred: true}, replay, "redelivery should reuse pending intent without creating another generation")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the fresh team-request review")
	replacement, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the newly dispatched review")
	require.NotEqual(t, sourceID, replacement.SessionID, "the replacement must run in a distinct review session")
	require.Equal(t, snapshot.snapshot.HeadSHA, replacement.HeadSHA, "fresh work should review the unchanged current head")
	require.Equal(t, models.CodeReviewTriggerSourceTeamReviewer, replacement.TriggerSource, "fresh work should retain the GitHub team request provenance")
	session, err := db.NewSessionStore(pool).GetByID(ctx, org, replacement.SessionID)
	require.NoError(t, err, "read replacement reviewer cleanup provenance")
	var revision map[string]any
	require.NoError(t, json.Unmarshal(session.RevisionContext, &revision), "decode replacement revision context")
	require.Equal(t, "143-code-reviewer", revision["requested_team_slug"], "scheduler refresh must preserve the requested team for GitHub cleanup")
	require.Equal(t, explicitReviewRequestChangeReason, revision["change_reason"], "scheduler refresh must preserve the original request reason")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision=$3,completed_at=now() WHERE org_id=$1 AND session_id=$2`, org, replacement.SessionID, decision)
	require.NoError(t, err, "complete the replacement before replaying its request")
	replay, err = service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "redeliver the already satisfied request")
	require.Equal(t, ReviewRequestedResult{Processed: true, Reused: true, SessionID: replacement.SessionID}, replay, "a completed request redelivery must retain its original replacement")
	var sessionIDs []uuid.UUID
	err = pool.QueryRow(ctx, `SELECT array_agg(session_id ORDER BY created_at) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&sessionIDs)
	require.NoError(t, err, "list review sessions for this tenant and PR")
	require.Equal(t, []uuid.UUID{sourceID, replacement.SessionID}, sessionIDs, "pending and completed redeliveries must not fan out extra reviewers")
	original, err := store.GetBySessionID(ctx, org, sourceID)
	require.NoError(t, err, "read the preserved historical outcome")
	require.Equal(t, &decision, original.Decision, "a new request must preserve the original review decision")
}

func completeSchedulingTeamReview(t *testing.T, pool *pgxpool.Pool, org, sessionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision='needs_human_review',completed_at=now() WHERE org_id=$1 AND session_id=$2`, org, sessionID)
	require.NoError(t, err, "complete the substantive full review")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "release the completed full review controller")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status) VALUES($1,$2,'codex','reviewer','completed')`, org, sessionID)
	require.NoError(t, err, "record substantive reviewer output for the completed session")
}

func testSchedulingOrdinaryCompletedReuse(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, mention bool) {
	t.Helper()
	ctx := context.Background()
	service, claim, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	completeSchedulingTeamReview(t, pool, org, sourceID)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read completed review generation")
	if mention {
		service.cfg.TeamSlugs = []string{"143-code-reviewer"}
		context := ReviewRequestContext{Source: "issue_comment", AuthorLogin: "reviewer", Body: "@test/143-code-reviewer", URL: "https://github.com/test/repo/pull/17#issuecomment-123"}
		raw, err := json.Marshal(context)
		require.NoError(t, err, "encode the unchanged substantive mention context")
		_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(revision_context,'{request_context}',$3::jsonb) WHERE org_id=$1 AND id=$2`, org, sourceID, raw)
		require.NoError(t, err, "represent a prior review of the same mention context")
		_, err = service.HandleReviewMentioned(ctx, ReviewMentionedInput{
			ReviewRequestedInput: ReviewRequestedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, HeadSHA: snapshot.snapshot.HeadSHA, BaseSHA: snapshot.snapshot.BaseSHA, DeliveryID: uuid.NewString()},
			CommentID:            123, CommentAuthor: context.AuthorLogin, CommentBody: context.Body, CommentURL: context.URL,
		})
		require.NoError(t, err, "request the unchanged mention with rechecks disabled")
	} else {
		_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
		require.NoError(t, err, "request ordinary UI Review now on an unchanged completed review")
	}
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "settle the ordinary request by reuse")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the ordinary request's reused review")
	require.Equal(t, sourceID, latest.SessionID, "the team reviewer-picker exception must not change other reuse paths")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read settled ordinary request")
	require.Equal(t, before.Generation, after.Generation, "ordinary equivalent reuse must not advance the generation")
	require.Equal(t, models.CodeReviewScheduleCovered, after.State, "the existing review should cover the ordinary request")
}

func testSchedulingFullAssessmentJoinSettlement(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	service, _, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	service.cfg.TeamSlugs = []string{"143-code-reviewer"}
	input := ReviewRequestedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", HeadSHA: snapshot.snapshot.HeadSHA, RequestedTeam: "143-code-reviewer", DeliveryID: uuid.NewString()}
	_, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "join the full review before its immutable assessment is captured")
	assessment := captureSchedulingJoinedFullAssessment(t, pool, org, repo, pr, sourceID, snapshot)
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=now(),result_origin='executed',decision='needs_human_review',acceptable=false,structured_outcome='{}' WHERE org_id=$1 AND id=$2`, org, assessment.ID)
	require.NoError(t, err, "complete the captured full assessment")
	var pendingID uuid.UUID
	require.NoError(t, service.scheduling.store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		var err error
		pendingID, _, err = db.RecordCodeReviewRequest(ctx, tx, org, repo, pr, "github", uuid.NewString(), models.CodeReviewReviewNow, "newer-request", state.Generation+1, nil)
		return err
	}), "record a newer pending request that completion must leave alone")
	require.NoError(t, service.scheduling.store.SettleAssessment(ctx, org, assessment.ID), "completion should settle the earlier full-review join")
	require.NoError(t, service.scheduling.store.SettleAssessment(ctx, org, assessment.ID), "full-review join settlement should be idempotent")
	var joinedStatus, pendingStatus string
	var joinedSession uuid.UUID
	err = pool.QueryRow(ctx, `SELECT status,session_id FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2 AND source_identity=$3`, org, pr, input.DeliveryID).Scan(&joinedStatus, &joinedSession)
	require.NoError(t, err, "read the completed full-review join")
	require.Equal(t, "satisfied", joinedStatus, "full completion must settle joins that have no assessment ID")
	require.Equal(t, sourceID, joinedSession, "completion must preserve the joined session identity")
	err = pool.QueryRow(ctx, `SELECT status FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2 AND id=$3`, org, pr, pendingID).Scan(&pendingStatus)
	require.NoError(t, err, "read the newer pending request")
	require.Equal(t, "pending", pendingStatus, "settling a historical full review must preserve newer pending intent")
}

func captureSchedulingJoinedFullAssessment(t *testing.T, pool *pgxpool.Pool, org, repo, pr, sourceID uuid.UUID, snapshot *schedulingSnapshotFixture) models.CodeReviewAssessment {
	t.Helper()
	ctx := context.Background()
	metadata, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sourceID)
	require.NoError(t, err, "read source policy for immutable capture")
	assessment, _, err := db.NewCodeReviewAssessmentStore(pool).Create(ctx, models.CodeReviewAssessmentCapture{
		OrgID: org, RepositoryID: repo, PullRequestID: pr, PolicyID: metadata.PolicyID, SessionID: sourceID,
		Generation: 1, BaseSHA: snapshot.snapshot.BaseSHA, BaseRef: snapshot.snapshot.BaseRef, HeadSHA: snapshot.snapshot.HeadSHA,
		InputVersion: 1, CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gate", InputDigest: "input", InputManifest: json.RawMessage(`{}`),
		ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: "join-settlement:" + pr.String(),
	})
	require.NoError(t, err, "capture the full assessment after its request joined")
	return assessment
}

func testSchedulingSupersededFullReviewJoin(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	service, claim, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	service.cfg.TeamSlugs = []string{"143-code-reviewer"}
	input := ReviewRequestedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", HeadSHA: snapshot.snapshot.HeadSHA, RequestedTeam: "143-code-reviewer", DeliveryID: uuid.NewString()}
	_, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "join the source full review before assessment capture")
	assessment := captureSchedulingJoinedFullAssessment(t, pool, org, repo, pr, sourceID, snapshot)
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = "replacement-head" })
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "queue the worker-detected changed head before retiring its old assessment")
	require.NoError(t, service.scheduling.store.SettleAssessment(ctx, org, assessment.ID), "settle the superseded assessment after replacement intent is durable")
	var joinedStatus string
	err = pool.QueryRow(ctx, `SELECT status FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2 AND source_identity=$3`, org, pr, input.DeliveryID).Scan(&joinedStatus)
	require.NoError(t, err, "read the join while replacement is pending")
	require.Equal(t, "joined", joinedStatus, "supersession must preserve a legacy join that the replacement still serves")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "release the superseded controller")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch the replacement that serves the joined request")
	replacement, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read the changed-head replacement")
	require.NotEqual(t, sourceID, replacement.SessionID, "changed code must receive a new full-review session")
	replay, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "redeliver the joined request after the replacement starts")
	require.Equal(t, ReviewRequestedResult{Processed: true, Reused: true, SessionID: replacement.SessionID}, replay, "the replacement must satisfy the retargeted join rather than leave it failed on the stale session")
}
