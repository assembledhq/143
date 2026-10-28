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

func TestDisputeTerminalSyncPreservesActiveReviewPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for delayed terminal callback proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name   string
		status models.CodeReviewSessionStatus
	}{
		{name: "queued metadata", status: models.CodeReviewSessionStatusQueued},
		{name: "running metadata", status: models.CodeReviewSessionStatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			disputeID := uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status=$3, trigger_source='dispute_reassessment' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, tt.status)
			require.NoError(t, err, "leave the owning review active when an earlier terminal notification arrives")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id)
			VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$3)`, disputeID, f.job.OrgID, f.job.SessionID, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID)
			require.NoError(t, err, "seed the accepted reassessment without a replacement handoff")
			before, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read every field before the delayed callback")
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, &Services{CodeReviewDisputes: disputes}, zerolog.Nop(), f.job, "terminal")
			after, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read the dispute after the delayed callback")
			require.Equal(t, before, after, "a terminal stage label must not override active owning review metadata")
			var replyJobs int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute' AND payload->>'dispute_id'=$2`, f.job.OrgID, disputeID.String()).Scan(&replyJobs)
			require.NoError(t, err, "inspect durable replies for the unchanged dispute")
			require.Zero(t, replyJobs, "a nonterminal owner must not enqueue a terminal dispute reply")
		})
	}
}
