package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCaptureRecoveryIntentRetirementSettlesDisputePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for retired capture recovery dispute proof")
	}
	tests := []struct {
		name    string
		request string
		protect string
	}{
		{name: "PR closes before replacement dispatch", request: "closed"},
		{name: "PR merges before replacement dispatch", request: "merged"},
		{name: "UI request replaces capacity held recovery", request: "ui"},
		{name: "team request commits replacement before provider outage", request: "team"},
		{name: "comment mention commits replacement before provider outage", request: "mention"},
		{name: "completed dispute survives old recovery retirement", request: "ui", protect: "completed"},
		{name: "replacement owned dispute survives old recovery retirement", request: "ui", protect: "bound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			// Each capacity scenario owns its entire advertised synthetic fleet.
			pool := fullRecoveryPostgresPool(t, ctx)
			f := seedStrandedReview(t, pool, "idle")
			disputeID, reviewed := uuid.New(), uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "begin before reviewer or assessment allocation")
			_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','completed')`, reviewed, f.job.OrgID)
			require.NoError(t, err, "seed the original disputed review")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id) VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider this change.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$7)`, disputeID, f.job.OrgID, reviewed, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID, f.job.SessionID)
			require.NoError(t, err, "retain the dispute with its original running reassessment owner")
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='dispute_reassessment' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "preserve original dispute trigger")
			_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_build_object('pull_request_title','Saved review','triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, disputeID.String())
			require.NoError(t, err, "retain immutable source dispute provenance")
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode matching original controller dispute provenance")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload)
			require.NoError(t, err, "keep source controller and session dispute identity consistent")
			config := models.DefaultCodeReviewPolicyConfig()
			zero := 0
			config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
			policy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, config, nil)
			require.NoError(t, err, "save a dispatchable new policy after source admission")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read authoritative synthetic PR identity")
			snapshot := &queuedIdentitySnapshot{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, AuthorLogin: "author", State: "open", HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}}
			lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, f.stores.Jobs, zerolog.Nop(), codereviewsvc.Config{TeamSlugs: []string{"reviewers"}})
			lifecycle.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			services := &Services{CodeReviewLifecycle: lifecycle, CodeReviewDisputes: disputes}
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "load queued disputed source identity")
			require.NoError(t, supersedeCodeReviewForCapturedIdentity(ctx, f.stores, services, zerolog.Nop(), f.job, metadata, codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: snapshot.snapshot}, &fullAssessmentQueuedIdentityChanged{Fields: []string{"policy_id", "policy_version"}}), "capture supersession must durably preserve the dispute pending replacement")
			identity := "capture_identity:" + f.job.SessionID.String()
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "pending", nil)
			node := "retired-capture-" + uuid.NewString()
			closing := tt.request == "closed" || tt.request == "merged"
			if !closing {
				lifecycle.SetSandboxAdmission()
				_, err = pool.Exec(ctx, `INSERT INTO nodes(id,host,mode,status,last_heartbeat_at,metadata) VALUES($1,'synthetic-review-worker','worker','active',clock_timestamp(),'{"sandbox_host_id":"192.0.2.43","max_active_sandboxes":1,"live_sandbox_count":1,"reserved_sandbox_count":0}')`, node)
				require.NoError(t, err, "hold recovery in verified saturated synthetic capacity")
				approvalRecoveryWake(t, ctx, pool, lifecycle, f)
				state, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "read held capture replacement")
				require.Equal(t, models.CodeReviewWaitCapacity, state.WaitReason, "capture recovery should wait durably for fleet capacity")
			}
			if tt.protect != "" {
				if tt.protect == "completed" {
					_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_status='completed',reassessment_decision='approved',reassessment_flipped=true,status_detail='Preserved completed result.' WHERE org_id=$1 AND id=$2`, f.job.OrgID, disputeID)
				} else {
					owner := uuid.New()
					_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status,revision_context) VALUES($1,$2,'code_review','completed','{}')`, owner, f.job.OrgID)
					require.NoError(t, err, "seed an independently established replacement owner")
					_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,decision,acceptable) VALUES($1,$2,$3,$4,$5,'base','head','dispute_reassessment','completed',$6,'approved',true)`, f.job.OrgID, owner, f.job.RepositoryID, f.job.PullRequestID, policy.ID, "independent-owner:"+owner.String())
					require.NoError(t, err, "retain actual newer owner decision awaiting authoritative dispute reconciliation")
					_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_session_id=$3,status_detail='Preserved replacement owner.' WHERE org_id=$1 AND id=$2`, f.job.OrgID, disputeID, owner)
				}
				require.NoError(t, err, "establish durable result or newer ownership before old intent retirement")
			}
			before, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read exact dispute before intent retirement")
			requestID := uuid.New()
			if tt.request == "ui" {
				snapshot.err = errors.New("synthetic snapshot failure before accepting the human request")
				_, requestErr := lifecycle.RequestScheduledReview(ctx, codereviewsvc.ScheduleRequestInput{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID, RequestID: requestID, Mode: models.CodeReviewReviewNow, Reason: "An unrelated intentional review"})
				require.Error(t, requestErr, "failed UI admission must roll back its request and old-intent retirement")
				assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "pending", nil)
				preserved, preserveErr := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
				require.NoError(t, preserveErr, "read original dispute after rejected human admission")
				require.Equal(t, before, preserved, "rejected human intent must preserve the exact old reassessment")
				snapshot.err = nil
			}
			request := codereviewsvc.ReviewRequestedInput{OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, GitHubRepo: pr.GitHubRepo, GitHubPRNumber: pr.GitHubPRNumber, GitHubPRURL: pr.GitHubPRURL, PullRequestTitle: pr.Title, PullRequestAuthor: "author", HeadSHA: "head", BaseSHA: "base", RequestedTeam: "reviewers", DeliveryID: requestID.String()}
			admitHuman := func() {
				if tt.request == "team" {
					_, requestErr := lifecycle.HandleReviewRequested(ctx, request)
					require.NoError(t, requestErr, "team intake should remain committed despite later provider outage")
				} else if tt.request == "mention" {
					_, requestErr := lifecycle.HandleReviewMentioned(ctx, codereviewsvc.ReviewMentionedInput{ReviewRequestedInput: request, CommentID: 1, CommentAuthor: "reviewer", CommentBody: "@test/reviewers Please run another review.", CommentURL: "https://example.invalid/comment/1"})
					require.NoError(t, requestErr, "comment intake should remain committed despite later provider outage")
				} else {
					_, requestErr := lifecycle.RequestScheduledReview(ctx, codereviewsvc.ScheduleRequestInput{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID, RequestID: requestID, Mode: models.CodeReviewReviewNow, Reason: "An unrelated intentional review"})
					require.NoError(t, requestErr, "admit an independent UI review intention")
				}
			}
			if closing {
				snapshot.snapshot.State = "closed"
				_, err = pool.Exec(ctx, `UPDATE pull_requests SET status=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.PullRequestID, tt.request)
				require.NoError(t, err, "observe actual closed or merged provider state")
				approvalRecoveryWake(t, ctx, pool, lifecycle, f)
			} else {
				if tt.request == "team" || tt.request == "mention" {
					snapshot.err = errors.New("synthetic provider unavailable after early intake")
				}
				admitHuman()
			}
			expectedRequestStatus := "superseded"
			if closing {
				expectedRequestStatus = "cancelled"
			}
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, expectedRequestStatus, nil)
			after, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read dispute after exact old intent retirement")
			var replies int
			retirementKey := "reply_code_review_dispute:" + disputeID.String() + ":capture_identity_retired:" + f.job.SessionID.String()
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, f.job.OrgID, retirementKey).Scan(&replies), "count durably committed atomic retirement replies")
			if tt.protect == "" {
				require.Equal(t, models.CodeReviewDisputeReassessmentFailed, after.ReassessmentStatus, "retiring unbound recovery must terminalize the original reassessment")
				require.Equal(t, &f.job.SessionID, after.ReassessmentSessionID, "terminalization must retain original ownership rather than attach to unrelated human intent")
				require.Equal(t, 1, replies, "terminalization and a single durable reply must commit together")
			} else {
				require.Equal(t, before, after, "retiring old intent must preserve every completed or replacement-owned dispute field")
				require.Equal(t, 0, replies, "old intent must not enqueue a failure reply for another owner or completed decision")
			}
			_, authoritativeReply, replyErr := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, replyErr, "build the authoritative terminal or newer owner result")
			after, err = f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "retain authoritative dispute state after reconciling its actual owner")
			if closing {
				state, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "read closed review schedule")
				require.Nil(t, state.PendingInput, "closed PR must not retain replacement dispatch intent")
				snapshot.snapshot.State = "open"
				_, err = pool.Exec(ctx, `UPDATE pull_requests SET status='open' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "reopen only synthetic fixture for later independent human request")
				admitHuman()
			} else {
				snapshot.err = nil
				admitHuman()
				_, err = pool.Exec(ctx, `UPDATE nodes SET metadata='{"sandbox_host_id":"192.0.2.43","max_active_sandboxes":1,"live_sandbox_count":0,"reserved_sandbox_count":0}',last_heartbeat_at=clock_timestamp() WHERE id=$1`, node)
				require.NoError(t, err, "open synthetic capacity for the independent request")
			}
			approvalRecoveryWake(t, ctx, pool, lifecycle, f)
			latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read independently dispatched human review")
			require.NotEqual(t, f.job.SessionID, latest.SessionID, "independent human review must have a new immutable session")
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, requestID.String(), "satisfied", &latest.SessionID)
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, expectedRequestStatus, nil)
			parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, latest.SessionID)
			require.NoError(t, err, "read independent human request provenance")
			var provenance struct {
				DisputeID *uuid.UUID `json:"triggering_dispute_id"`
			}
			require.NoError(t, json.Unmarshal(parent.RevisionContext, &provenance), "decode independent human review dispute provenance")
			require.Nil(t, provenance.DisputeID, "new human intent must never inherit the retired dispute")
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
			final, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read original dispute after old source replay")
			require.Equal(t, after, final, "dispatch and old-source replay must preserve exact terminal or newer-owned dispute state")
			_, repairReply, repairErr := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, repairErr, "generic reply repair should render authoritative current dispute state")
			require.Equal(t, authoritativeReply, repairReply, "old callback repair must render the same terminal or replacement-owned reply")
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, f.job.OrgID, retirementKey).Scan(&replies), "count atomic retirement replies after manual replay and old-source repair")
			expectedReplies := 1
			if tt.protect != "" {
				expectedReplies = 0
			}
			require.Equal(t, expectedReplies, replies, "replay must not duplicate or invent an atomic retirement-stage reply")
		})
	}
}
