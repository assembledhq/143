package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // Full migration setup touches the shared pgcrypto extension; isolated tenant cases run in parallel.
func TestWorkspaceWaitDeadLetterPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for fenced workspace retry-exhaustion proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name       string
		change     string
		wantFailed bool
	}{
		{name: "exhausted waiting thread is harvested by controller", wantFailed: true},
		{name: "different turn", change: "turn"},
		{name: "pending replacement job", change: "replacement"},
		{name: "another executor owns thread", change: "executor"},
		{name: "thread runtime still active", change: "runtime"},
		{name: "durable cancellation", change: "cancel"},
		{name: "pending parent cancellation", change: "parent_cancel"},
		{name: "superseded assessment", change: "superseded"},
		{name: "publishing assessment", change: "publishing"},
		{name: "uncertain publication", change: "uncertain"},
		{name: "confirmed publication receipt", change: "receipt"},
		{name: "staged result", change: "staged"},
		{name: "scheduler no longer owns session", change: "owner"},
		{name: "job not dead lettered", change: "retry"},
		{name: "terminal job still has ownership", change: "owned"},
		{name: "job belongs to another thread", change: "payload"},
		{name: "different terminal reason", change: "error"},
		{name: "different tenant", change: "tenant"},
		{name: "completed parent", change: "parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "running")
			threadID, waitJobID, token := uuid.New(), uuid.New(), uuid.New()
			execSQL := func(query string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, query, args...)
				require.NoError(t, err, "seed constrained workspace exhaustion state")
			}
			execSQL(`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,current_turn,started_at,execution_mode,filesystem_mode) VALUES($1,$2,$3,'claude_code','reviewer','running',0,now(),'review','read_only')`, threadID, f.job.OrgID, f.job.SessionID)
			payload, err := json.Marshal(map[string]string{"org_id": f.job.OrgID.String(), "session_id": f.job.SessionID.String(), "thread_id": threadID.String()})
			require.NoError(t, err, "encode exact waiting thread job identity")
			execSQL(`INSERT INTO jobs(id,org_id,queue,job_type,payload,status,completed_at) VALUES($1,$2,'agent','continue_session',$3,'dead_letter',now())`, waitJobID, f.job.OrgID, payload)
			node := "workspace-test-" + waitJobID.String()
			execSQL(`INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			// Executor finalization follows the job's dead-letter hook. Its own
			// row is still running here, but the exact job has released ownership.
			execSQL(`INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status) VALUES($1,$2,$3,$4,'continue_session',$5,'test',$6,'running')`, f.job.OrgID, f.job.SessionID, threadID, waitJobID, node, token)
			session := models.Session{ID: f.job.SessionID, OrgID: f.job.OrgID, Origin: models.SessionOriginCodeReview}
			switch tt.change {
			case "turn":
				execSQL(`UPDATE session_threads SET current_turn=1 WHERE org_id=$1 AND id=$2`, f.job.OrgID, threadID)
			case "replacement", "executor":
				otherJob := uuid.New()
				status := "pending"
				if tt.change == "executor" {
					status = "succeeded"
				}
				execSQL(`INSERT INTO jobs(id,org_id,queue,job_type,payload,status) VALUES($1,$2,'agent','continue_session',$3,$4)`, otherJob, f.job.OrgID, payload, status)
				if tt.change == "executor" {
					execSQL(`UPDATE session_executors SET status='failed',completed_at=now() WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, waitJobID)
					execSQL(`INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status) VALUES($1,$2,$3,$4,'continue_session',$5,'test',$6,'running')`, f.job.OrgID, f.job.SessionID, threadID, otherJob, node, uuid.New())
				}
			case "runtime":
				execSQL(`INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,owner_node_id,lease_token,status) VALUES($1,$2,$3,'claude_code',$4,$5,'live')`, f.job.OrgID, f.job.SessionID, threadID, node, uuid.New())
			case "cancel":
				execSQL(`UPDATE session_threads SET cancel_requested_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, threadID)
			case "parent_cancel":
				execSQL(`INSERT INTO session_cancel_requests(org_id,session_id) VALUES($1,$2)`, f.job.OrgID, f.job.SessionID)
			case "superseded":
				execSQL(`UPDATE code_review_revision_assessments SET status='superseded',completed_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			case "publishing", "uncertain":
				publication := "reserved"
				if tt.change == "uncertain" {
					publication = "uncertain"
				}
				execSQL(`UPDATE code_review_revision_assessments SET status='publishing',publication_state=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, publication)
			case "receipt":
				execSQL(`UPDATE code_review_revision_assessments SET publication_state='confirmed',github_review_id=123,publication_receipt='{}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			case "staged":
				execSQL(`UPDATE code_review_revision_assessments SET result_origin='executed' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			case "owner":
				execSQL(`UPDATE code_review_pr_state SET active_session_id=NULL WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID)
			case "retry":
				execSQL(`UPDATE jobs SET status='pending',completed_at=NULL WHERE org_id=$1 AND id=$2`, f.job.OrgID, waitJobID)
			case "owned":
				execSQL(`UPDATE jobs SET lock_token=$3,lease_expires_at=now()+interval '1 minute' WHERE org_id=$1 AND id=$2`, f.job.OrgID, waitJobID, token)
			case "payload":
				execSQL(`UPDATE jobs SET payload=jsonb_set(payload,'{thread_id}',to_jsonb($3::text)) WHERE org_id=$1 AND id=$2`, f.job.OrgID, waitJobID, uuid.NewString())
			case "tenant":
				session.OrgID = uuid.New()
			case "parent":
				execSQL(`UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			}
			snapshot := func() string {
				t.Helper()
				var state string
				err := pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(s),to_jsonb(m),to_jsonb(a),to_jsonb(p))::text FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id JOIN code_review_revision_assessments a ON a.org_id=m.org_id AND a.metadata_id=m.id JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id WHERE s.org_id=$1 AND s.id=$2`, f.job.OrgID, f.job.SessionID).Scan(&state)
				require.NoError(t, err, "capture immutable parent, metadata, assessment and scheduler state")
				return state
			}
			before := snapshot()
			hookCtx := jobctx.WithJobID(jobctx.WithDeadLetterHooks(ctx), waitJobID)
			registerWorkspaceWaitDeadLetter(hookCtx, f.stores, zerolog.Nop(), session, threadID, 0)
			terminalErr := fmt.Errorf("retryable job timed out after 8m0s: %w", agent.ErrSandboxWorkspaceNotReady)
			if tt.change == "error" {
				terminalErr = fmt.Errorf("%w", agent.ErrThreadCancelledBeforeWorkspaceReady)
			}
			jobctx.RunDeadLetterHooks(hookCtx, terminalErr)
			require.Equal(t, before, snapshot(), "waiting-thread failure must leave controller-owned parent, assessment, publication and scheduler state unchanged")
			thread, err := f.stores.SessionThreads.GetByID(ctx, f.job.OrgID, threadID)
			require.NoError(t, err, "read durable thread after dead-letter hook")
			if !tt.wantFailed {
				require.Equal(t, models.ThreadStatusRunning, thread.Status, "a protected or replacement turn must retain its state")
				require.Nil(t, thread.FailureCategory, "an ineligible thread must not acquire a failure result")
				return
			}
			require.Equal(t, models.ThreadStatusFailed, thread.Status, "exhausted exact waiting turn must stop showing running")
			require.Equal(t, "sandbox_workspace_not_ready", *thread.FailureCategory, "failure should expose its workspace category")
			require.Equal(t, "The review could not finish preparing its workspace before automatic retries expired.", *thread.FailureExplanation, "failure should explain bounded retry exhaustion")
			require.NotNil(t, thread.CompletedAt, "the waiting turn should have a durable terminal time")
			structured := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: "reviewer:0", ThreadID: threadID.String(), ReadOnly: true})
			execSQL(`INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status,structured_result) VALUES($1,$2,'claude_code','reviewer','running',$3)`, f.job.OrgID, f.job.SessionID, structured)
			policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
			require.NoError(t, err, "read captured policy for normal controller harvest")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read running metadata for normal controller harvest")
			require.NoError(t, harvestCodeReviewReviewerResults(ctx, f.stores, nil, zerolog.Nop(), f.job, policy, metadata, nil), "controller should harvest terminal workspace failure through its normal path")
			results, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read harvested reviewer outcome")
			require.Equal(t, 1, len(results), "the exact reviewer outcome should be preserved")
			require.Equal(t, models.CodeReviewAgentResultStatusFailed, results[0].Status, "controller should observe failure instead of waiting forever for a running thread")
			// Once the exact executor's normal finalization lands, the controller
			// may safely settle its own parent and assessment without publication.
			execSQL(`UPDATE session_executors SET status='failed',completed_at=now() WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, waitJobID)
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "load controller's captured pull request")
			require.NoError(t, failCodeReviewWithoutReviewerOutput(ctx, f.stores, nil, zerolog.Nop(), f.job, pr, results), "controller's normal terminal path should settle the exhausted review")
			parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read controller-settled parent")
			require.Equal(t, models.SessionStatusFailed, parent.Status, "controller settlement should remove stale-running parent state")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read controller-settled full assessment")
			require.Equal(t, models.CodeReviewAssessmentFailed, assessment.Status, "controller settlement should terminalize the exact unpublished assessment")
			require.Equal(t, models.CodeReviewPublicationNotStarted, assessment.PublicationState, "workspace exhaustion must not cause a publication attempt")
		})
	}
}
