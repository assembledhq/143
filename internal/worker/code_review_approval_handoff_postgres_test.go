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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type approvalRecoveryCanceller struct {
	pool *pgxpool.Pool
	hook func()
}

func (c *approvalRecoveryCanceller) CancelActiveThreads(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (int, error) {
	if c.hook != nil {
		c.hook()
		c.hook = nil
	}
	tag, err := c.pool.Exec(ctx, `UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND session_id=ANY($2) AND status IN ('pending','running','awaiting_input')`, orgID, sessionIDs)
	return int(tag.RowsAffected()), err
}

func TestCaptureIdentityApprovalSettlesRequestPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for approval-stopped request proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name  string
		phase string
	}{
		{name: "approval before recovery admission", phase: "admission"},
		{name: "approval before wake snapshot refresh", phase: "refresh"},
		{name: "approval between wake refresh and dispatch", phase: "dispatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			f.job.TriggeringDisputeID = nil
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "remove assessment before immutable source capture")
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='auto_policy' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain automatic source authorization")
			_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context='{"pull_request_title":"Saved review"}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain valid immutable source input context")
			roster, err := json.Marshal(models.DefaultCodeReviewPolicyConfig().AgentRoster)
			require.NoError(t, err, "encode dispatchable source policy")
			_, err = pool.Exec(ctx, `UPDATE code_review_policies SET agent_roster=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.PolicyID, roster)
			require.NoError(t, err, "allow later explicit review dispatch")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read isolated source PR")
			snapshot := &queuedIdentitySnapshot{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, AuthorLogin: "author", State: "open", HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}}
			lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, f.stores.Jobs, zerolog.Nop(), codereviewsvc.Config{})
			lifecycle.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
			canceller := &approvalRecoveryCanceller{pool: pool}
			lifecycle.SetThreadCanceller(canceller)
			input := codereviewsvc.ReviewChangedInput{OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, PriorSessionID: f.job.SessionID, TriggerSource: models.CodeReviewTriggerSourceAutoPolicy}
			identity := "capture_identity:" + f.job.SessionID.String()
			unrelated := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation) VALUES($1,$2,$3,$4,'github',$5,'review_now','independent-human-request',100)`, unrelated, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, "unrelated-human:"+unrelated.String())
			require.NoError(t, err, "preserve an independent pending human request")
			other := seedStrandedReview(t, pool, "idle")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_requests(org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation) VALUES($1,$2,$3,'github',$4,'review_now','other-tenant-request',100)`, other.job.OrgID, other.job.RepositoryID, other.job.PullRequestID, identity)
			require.NoError(t, err, "retain an identical delivery identity in another tenant")
			seedApproval := func() {
				prior := uuid.New()
				_, seedErr := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','completed')`, prior, f.job.OrgID)
				require.NoError(t, seedErr, "persist completed approval parent")
				_, seedErr = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,decision,acceptable,github_review_id,created_at) VALUES($1,$2,$3,$4,$5,'base','head','auto_policy','completed',$6,'approved',true,77,now()-interval '1 minute')`, f.job.OrgID, prior, f.job.RepositoryID, f.job.PullRequestID, f.job.PolicyID, "approval:"+prior.String())
				require.NoError(t, seedErr, "record a confirmed historical approval")
			}
			if tt.phase == "admission" {
				seedApproval()
			}
			_, err = lifecycle.QueueCapturedIdentityReplacement(ctx, input)
			require.NoError(t, err, "admit one idempotent capture-identity recovery request")
			if tt.phase != "admission" {
				if tt.phase == "refresh" {
					seedApproval()
				} else {
					// Cancellation happens after refresh commits, allowing approval
					// to arrive before the separately locked dispatch phase.
					_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'claude_code','Code review: draining source','running')`, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "retain a draining source thread across snapshot refresh")
					canceller.hook = seedApproval
				}
				approvalRecoveryWake(t, ctx, pool, lifecycle, f)
				require.Nil(t, canceller.hook, "dispatch-phase approval must have arrived after refresh committed")
			}
			state, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read permanently stopped automatic recovery")
			require.Equal(t, models.CodeReviewWaitApproved, state.WaitReason, "historical approval must stop automatic recovery")
			require.Nil(t, state.PendingInput, "approval must clear automatic recovery intent")
			require.Nil(t, state.PendingRequestID, "approval must clear the settled recovery pointer")
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "cancelled", nil)
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, "unrelated-human:"+unrelated.String(), "pending", nil)
			assertApprovalRecoveryRequest(t, ctx, pool, other.job.OrgID, identity, "pending", nil)
			_, err = lifecycle.QueueCapturedIdentityReplacement(ctx, input)
			require.NoError(t, err, "replay of the retired source must not revive stopped automatic intent")
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_requests WHERE org_id=$1 AND source_identity=$2),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review')]`, f.job.OrgID, identity).Scan(&counts), "read request and dispatch counts after recovery replay")
			require.Equal(t, []int{1, 1}, counts, "stopped recovery replay must retain one terminal request and no replacement controller")
			manualID := uuid.New()
			_, err = lifecycle.RequestScheduledReview(ctx, codereviewsvc.ScheduleRequestInput{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID, RequestID: manualID, Mode: models.CodeReviewReviewNow, Reason: "Recheck intentionally after approval"})
			require.NoError(t, err, "human request should remain authorized after automatic recovery stops")
			approvalRecoveryWake(t, ctx, pool, lifecycle, f)
			latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read later human review session")
			require.Equal(t, models.CodeReviewSessionStatusQueued, latest.Status, "later explicit request should dispatch a full review")
			require.NotEqual(t, f.job.SessionID, latest.SessionID, "human request must own a distinct immutable review")
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, manualID.String(), "satisfied", &latest.SessionID)
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, "unrelated-human:"+unrelated.String(), "satisfied", &latest.SessionID)
			assertApprovalRecoveryRequest(t, ctx, pool, f.job.OrgID, identity, "cancelled", nil)
			assertApprovalRecoveryRequest(t, ctx, pool, other.job.OrgID, identity, "pending", nil)
		})
	}
}

func approvalRecoveryWake(t *testing.T, ctx context.Context, pool *pgxpool.Pool, lifecycle *codereviewsvc.Service, f strandedReviewFixture) {
	t.Helper()
	var wakeID uuid.UUID
	lease := uuid.New()
	err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$2,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND job_type='reconcile_code_review_schedule' AND status IN ('pending','running') RETURNING id`, f.job.OrgID, lease).Scan(&wakeID)
	require.NoError(t, err, "claim only this isolated recovery wake")
	require.NoError(t, lifecycle.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID}), "reconcile the durable recovery or manual intent")
}

func assertApprovalRecoveryRequest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, identity, expectedStatus string, expectedSessionID *uuid.UUID) {
	t.Helper()
	var actual struct {
		status    string
		sessionID *uuid.UUID
	}
	err := pool.QueryRow(ctx, `SELECT status,session_id FROM code_review_requests WHERE org_id=$1 AND source_identity=$2`, orgID, identity).Scan(&actual.status, &actual.sessionID)
	require.NoError(t, err, "read exact delivery bookkeeping")
	require.Equal(t, expectedStatus, actual.status, "only the intended request should reach the expected status")
	require.Equal(t, expectedSessionID, actual.sessionID, "stopped automatic requests must never attach to a future human review")
}
