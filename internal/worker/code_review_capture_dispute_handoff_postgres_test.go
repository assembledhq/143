package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type captureDisputeDispatchLifecycle struct {
	*codereviewsvc.Service
	beforeTerminalSync func()
}

func (s captureDisputeDispatchLifecycle) QueueCapturedIdentityReplacement(ctx context.Context, input codereviewsvc.ReviewChangedInput) (codereviewsvc.ReviewRequestedResult, error) {
	result, err := s.Service.QueueCapturedIdentityReplacement(ctx, input)
	if err == nil && s.beforeTerminalSync != nil {
		s.beforeTerminalSync()
	}
	return result, err
}

func TestCapturedIdentityDisputeHandoffPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for captured-identity dispute handoff proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name                                                     string
		scheduled, dispatchFirst, cancel, nullOwner, normalStale bool
	}{
		{name: "scheduled replacement dispatches before source terminal sync", scheduled: true, dispatchFirst: true},
		{name: "scheduled source terminal sync precedes replacement dispatch", scheduled: true},
		{name: "legacy replacement dispatches before source terminal sync", dispatchFirst: true},
		{name: "legacy source terminal sync precedes replacement dispatch"},
		{name: "normal parent cancellation still fails reassessment", cancel: true},
		{name: "unbound reassessment accepts its owning completion", cancel: true, nullOwner: true},
		{name: "ordinary stale owner settles as superseded", normalStale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			disputeID, reviewedSession := uuid.New(), uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "start before the source assessment or reviewer fanout")
			_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','completed')`, reviewedSession, f.job.OrgID)
			require.NoError(t, err, "seed the original reviewed session being disputed")
			var owner *uuid.UUID
			if !tt.nullOwner {
				owner = &f.job.SessionID
			}
			_, err = pool.Exec(ctx, `INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id)
			VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider this change.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$7)`, disputeID, f.job.OrgID, reviewedSession, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID, owner)
			require.NoError(t, err, "seed the accepted dispute and its current reassessment owner")
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='dispute_reassessment' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain dispute trigger on the queued full review")
			_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_build_object('pull_request_title','Saved review','triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, disputeID.String())
			require.NoError(t, err, "retain immutable dispute authorization and queued analysis inputs")
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			submit := &statusCommentSubmitterStub{commentID: 9001}
			services := &Services{CodeReviewDisputes: disputes, CodeReviews: submit}
			if tt.normalStale {
				_, err := f.stores.CodeReviews.MarkActiveReviewStale(ctx, f.job.OrgID, f.job.SessionID, "normal stale failure")
				require.NoError(t, err, "retire a normal stale reassessment without capture replacement admission")
				enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
				dispute, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
				require.NoError(t, err, "read normal stale reassessment settlement")
				require.Equal(t, models.CodeReviewDisputeReassessmentSuperseded, dispute.ReassessmentStatus, "an ordinary stale owner should supersede its reassessment without claiming a new verdict")
				detail := "The reassessment was superseded because the review target changed. The objection remains recorded for a policy owner."
				require.Equal(t, &detail, dispute.StatusDetail, "the worker must retain a safe explanation for stale reassessment retirement")
				return
			}
			if tt.cancel {
				require.NoError(t, f.stores.Sessions.UpdateStatus(ctx, f.job.OrgID, f.job.SessionID, models.SessionStatusCancelled), "cancel the original owning parent")
				pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "load cancelled source PR")
				cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, f.stores, services, zerolog.Nop(), f.job, pr)
				require.NoError(t, err, "normal cancellation should settle through the existing controller path")
				require.True(t, cancelled, "normal cancellation should stop the owning controller")
				dispute, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
				require.NoError(t, err, "read the cancelled dispute reassessment")
				require.Equal(t, models.CodeReviewDisputeReassessmentFailed, dispute.ReassessmentStatus, "ordinary cancellation must still fail its reassessment")
				require.Equal(t, &f.job.SessionID, dispute.ReassessmentSessionID, "unbound or matching owner must retain its cancelling session")
				return
			}
			policy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, models.DefaultCodeReviewPolicyConfig(), nil)
			require.NoError(t, err, "change the active policy after the disputed review was queued")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read authoritative PR identity")
			snapshot := ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, State: "open", HeadSHA: f.job.HeadSHA, BaseSHA: "base", BaseRef: "main"}
			lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, f.stores.Jobs, zerolog.Nop(), codereviewsvc.Config{})
			if tt.scheduled {
				lifecycle.SetScheduling(db.NewCodeReviewScheduleStore(pool), &queuedIdentitySnapshot{snapshot: snapshot})
			}
			services.PR = &stubPRService{getCodeReviewPRSnapshotFn: func(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
				return snapshot, nil
			}}
			dispatch := func() {
				var replacementID uuid.UUID
				var raw json.RawMessage
				if tt.scheduled {
					lease := uuid.New()
					require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$2,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND job_type='reconcile_code_review_schedule' RETURNING id`, f.job.OrgID, lease).Scan(&replacementID), "claim the durable replacement wake")
					require.NoError(t, lifecycle.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, replacementID), lease), models.CodeReviewScheduleWake{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID}), "dispatch should bind the replacement dispute owner transactionally")
				} else {
					require.NoError(t, pool.QueryRow(ctx, `SELECT id,payload FROM jobs WHERE org_id=$1 AND job_type='start_code_review_reassessment'`, f.job.OrgID).Scan(&replacementID, &raw), "load the admitted legacy starter")
					require.NoError(t, newStartCodeReviewReassessmentHandler(f.stores, services, zerolog.Nop())(ctx, replacementID.String(), raw), "actual legacy starter should dispatch and bind the replacement owner")
				}
			}
			wrapper := captureDisputeDispatchLifecycle{Service: lifecycle}
			if tt.dispatchFirst {
				wrapper.beforeTerminalSync = dispatch
			}
			services.CodeReviewLifecycle = wrapper
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "load source review metadata")
			require.NoError(t, supersedeCodeReviewForCapturedIdentity(ctx, f.stores, services, zerolog.Nop(), f.job, metadata, codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: snapshot}, &fullAssessmentQueuedIdentityChanged{Fields: []string{"policy_id", "policy_version"}}), "actual capture recovery should preserve dispute across source retirement")
			before, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "reply reconciliation must retain a pending handoff instead of failing the stale source")
			require.Equal(t, models.CodeReviewDisputeReassessmentRunning, before.ReassessmentStatus, "terminal source sync must not fail the replacement reassessment")
			if !tt.dispatchFirst {
				require.Equal(t, &f.job.SessionID, before.ReassessmentSessionID, "source ownership should remain pending until replacement dispatch")
				dispatch()
			}
			latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read replacement full review")
			require.NotEqual(t, f.job.SessionID, latest.SessionID, "replacement must have immutable new session identity")
			bound, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read the replacement-bound dispute")
			require.Equal(t, &latest.SessionID, bound.ReassessmentSessionID, "replacement dispatch should own dispute completion")
			// Replay both status paths and an old completion callback after the
			// replacement owns the dispute. They cannot steal or fail its result.
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
			syncPayload, err := json.Marshal(codereviewsvc.SyncReviewStatusCommentJobPayload{OrgID: f.job.OrgID, SessionID: f.job.SessionID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, TriggeringDisputeID: &disputeID})
			require.NoError(t, err, "encode old source status sync replay")
			require.NoError(t, newSyncCodeReviewStatusCommentHandler(f.stores, services, zerolog.Nop())(ctx, "old-status-replay", syncPayload), "old asynchronous status reconciliation should skip dispute completion")
			lateDecision := models.CodeReviewDecisionBlocked
			transitioned, err := f.stores.CodeReviewDisputes.CompleteReassessmentOnce(ctx, f.job.OrgID, disputeID, f.job.SessionID, models.CodeReviewSessionStatusCompleted, &lateDecision, "late old callback")
			require.NoError(t, err, "late old completion callback should be fenced without failing its job")
			require.False(t, transitioned, "a prior source cannot overwrite replacement ownership")
			afterReplay, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read handoff after old replay")
			require.Equal(t, bound, afterReplay, "old source replay must preserve every durable dispute field and version")
			decision := models.CodeReviewDecisionApproved
			_, err = f.stores.CodeReviews.CompleteReview(ctx, f.job.OrgID, db.CompleteCodeReviewParams{SessionID: latest.SessionID, Decision: decision, Acceptable: true, FinalReviewBody: "Replacement decision."})
			require.NoError(t, err, "complete the replacement's actual decision")
			replacementJob := f.job
			replacementJob.SessionID = latest.SessionID
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), replacementJob, "terminal")
			completed, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "build the replacement's final dispute reply")
			require.Equal(t, models.CodeReviewDisputeReassessmentCompleted, completed.ReassessmentStatus, "replacement's own decision should complete reassessment")
			require.Equal(t, &latest.SessionID, completed.ReassessmentSessionID, "final result must retain the replacement owner")
			require.Equal(t, &decision, completed.ReassessmentDecision, "dispute should report the replacement's exact decision")
			require.Equal(t, new(true), completed.ReassessmentFlipped, "replacement approval should record the changed original blocked decision")
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
			require.NoError(t, newSyncCodeReviewStatusCommentHandler(f.stores, services, zerolog.Nop())(ctx, "old-status-after-decision", syncPayload), "delayed source sync after replacement completion must remain harmless")
			final, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read replacement decision after delayed source terminal sync")
			require.Equal(t, completed, final, "delayed old-source sync must preserve every completed replacement dispute field")
		})
	}
}
