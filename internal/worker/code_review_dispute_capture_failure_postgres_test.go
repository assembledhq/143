package worker

import (
	"context"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDisputeTerminalSyncPreservesCaptureReplacementFailurePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for capture replacement failure projection proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 4)
	tests := []struct {
		name, requestStatus string
	}{
		{name: "failed replacement request", requestStatus: "failed"},
		{name: "joined replacement request without a bound owner", requestStatus: "joined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			disputeID := uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			_, err := pool.Exec(ctx, `INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id)
VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$3)`, disputeID, f.job.OrgID, f.job.SessionID, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID)
			require.NoError(t, err, "retain the original source owner before replacement dispatch")
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='stale',stale=true,trigger_source='dispute_reassessment',triggering_dispute_id=$3,failure_reason='review scheduling target superseded',status_message=NULL WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, disputeID)
			require.NoError(t, err, "retire the source after capture replacement admission")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,status) VALUES($1,$2,$3,$4,'github',$5,'review_now','synthetic-input',1,$6)`, uuid.New(), f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, "capture_identity:"+f.job.SessionID.String(), tt.requestStatus)
			require.NoError(t, err, "persist the replacement request outcome that no longer satisfies the live handoff fence")
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, &Services{CodeReviewDisputes: disputes}, zerolog.Nop(), f.job, "terminal")
			after, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read the terminal dispute after the stale-owner callback")
			require.Equal(t, models.CodeReviewDisputeReassessmentFailed, after.ReassessmentStatus, "a stopped capture replacement must not become ordinary supersession")
			detail := "The reassessment was stopped before a replacement review could start. The objection remains recorded."
			require.Equal(t, &detail, after.StatusDetail, "terminal sync must retain the safe replacement-start failure explanation")
			require.Equal(t, &f.job.SessionID, after.ReassessmentSessionID, "failure must remain attached to its exact old source owner")
			require.Nil(t, after.ReassessmentDecision, "the abandoned replacement must not produce a verdict")
			require.Nil(t, after.ReassessmentFlipped, "the abandoned replacement must not claim a changed decision")
			readback, reply, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "build the actual dispute reply after terminal projection")
			expectedReply := after
			expectedReply.CurrentAuthorizationReason = "external contributor"
			require.Equal(t, expectedReply, readback, "reply construction must preserve the failed replacement outcome while deriving current authorization")
			persisted, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read the durable outcome after reply construction")
			require.Equal(t, after, persisted, "reply construction must not mutate the settled capture failure")
			require.Contains(t, reply, "The reassessment could not complete.", "the reply must continue to report a genuine reassessment failure")
			require.NotContains(t, reply, "was superseded", "the reply must not conceal a failed capture replacement as routine scheduling")
		})
	}
}
