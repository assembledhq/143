package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type captureRetirementFaultPool struct {
	*pgxpool.Pool
	transactions db.CodeReviewMetadataTransactions
	fail         atomic.Bool
	calls        atomic.Int32
}

func (p *captureRetirementFaultPool) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	// Keep the real owned-pool transaction's acquisition, budget and settlement;
	// inject only the same retirement SQL fault inside its supplied transaction.
	return p.transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		return work(unitCtx, &captureRetirementFaultTx{Tx: tx, owner: p})
	})
}

type captureRetirementFaultTx struct {
	pgx.Tx
	owner *captureRetirementFaultPool
}

func (tx *captureRetirementFaultTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx.owner.fail.Load() && strings.Contains(sql, "UPDATE code_review_session_metadata") && strings.Contains(sql, "SET status = 'stale'") {
		tx.owner.calls.Add(1)
		return nil, errors.New("synthetic source retirement write unavailable")
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func TestCaptureIdentityRetirementRetryDisputeHandoffPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for retirement failure after durable capture handoff proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name                 string
		handoff, starterFail bool
	}{
		{name: "retirement exhaustion preserves admitted replacement decision", handoff: true},
		{name: "actual replacement starter failure still settles reassessment", handoff: true, starterFail: true},
		{name: "ordinary failed source without a handoff still settles reassessment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			disputeID, reviewedSessionID := uuid.New(), uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "start capture before assessment creation or reviewer fanout")
			_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','completed')`, reviewedSessionID, f.job.OrgID)
			require.NoError(t, err, "retain the originally reviewed session referenced by the objection")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id)
			VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider this change.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$7)`, disputeID, f.job.OrgID, reviewedSessionID, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID, f.job.SessionID)
			require.NoError(t, err, "seed a triaged reassessment owned by the queued source controller")
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='dispute_reassessment' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain dispute authorization on source metadata")
			_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_build_object('pull_request_title','Saved review','triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, disputeID.String())
			require.NoError(t, err, "retain valid immutable revision context and triggering dispute")
			fault := &captureRetirementFaultPool{Pool: pool, transactions: f.stores.ThreadSendDependencies.Transactions}
			f.stores.CodeReviews, err = db.NewCodeReviewStoreWithTerminalTransactions(fault, fault)
			require.NoError(t, err, "retain actual terminal transactions while faulting only the source retirement write")
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			services := &Services{CodeReviewDisputes: disputes, CodeReviews: &statusCommentSubmitterStub{commentID: 9001}}
			if !tt.handoff {
				_, err = f.stores.CodeReviews.FailReview(ctx, f.job.OrgID, f.job.SessionID, "synthetic ordinary controller failure")
				require.NoError(t, err, "fail an ordinary source without durable replacement admission")
				enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
				failed, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
				require.NoError(t, err, "ordinary failure must still reconcile through the actual dispute reply service")
				require.Equal(t, models.CodeReviewDisputeReassessmentFailed, failed.ReassessmentStatus, "handoff protection must not preserve ordinary source failure")
				require.Equal(t, &f.job.SessionID, failed.ReassessmentSessionID, "ordinary terminal reconciliation must retain its matching owner")
				require.Nil(t, failed.ReassessmentDecision, "an ordinary failure must not invent a completed decision")
				return
			}
			policy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, models.DefaultCodeReviewPolicyConfig(), nil)
			require.NoError(t, err, "move active policy after the source controller was queued")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "load the authoritative source pull request")
			snapshot := ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, State: "open", HeadSHA: f.job.HeadSHA, BaseSHA: "base", BaseRef: "main"}
			services.PR = &stubPRService{getCodeReviewPRSnapshotFn: func(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
				return snapshot, nil
			}}
			services.CodeReviewLifecycle = codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, f.stores.Jobs, zerolog.Nop(), codereviewsvc.Config{})
			services.CodeReviewAssessmentsEnabled = true
			services.CodeReviewInputCapture = &queuedIdentityCapture{result: codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: snapshot}}
			nodeID := "capture-retirement-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, nodeID)
			require.NoError(t, err, "register the isolated actual controller worker")
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode the immutable queued source job")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3,status='pending',lock_token=NULL,attempts=7,max_attempts=8,target_node_id=$4,priority=100000,run_at=now(),retry_window_started_at=now()-interval '4 hours' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, nodeID)
			require.NoError(t, err, "reproduce an eighth claim after an unrelated generic retry window expired")
			fault.fail.Store(true)
			worker := f.newWorker(t, pool, nodeID)
			worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			var status models.JobStatus
			var attempts, starterCount int
			var phaseStart time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,(payload->>'_code_review_capture_identity_retry_started_at')::timestamptz FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts, &phaseStart), "read the actual worker's phase-specific retirement retry")
			require.Equal(t, models.JobStatusPending, status, "failed retirement after durable admission must defer the source controller")
			require.Equal(t, 7, attempts, "retirement dependency failure must preserve ordinary controller attempts")
			require.WithinDuration(t, time.Now(), phaseStart, 5*time.Second, "retirement retries must share the newly established capture handoff phase")
			require.Equal(t, int32(1), fault.calls.Load(), "the regression must fault only the actual source retirement write")
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='start_code_review_reassessment'`, f.job.OrgID).Scan(&starterCount), "count durable legacy handoffs admitted before source retirement")
			require.Equal(t, 1, starterCount, "retirement failure must retain exactly one durable replacement starter")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read recoverable source after its first failed retirement")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "source metadata must remain active until retirement succeeds or its phase expires")
			_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now(),payload=jsonb_set(payload,'{_code_review_capture_identity_retry_started_at}',to_jsonb(now()-interval '121 minutes')) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "expire only the bounded capture handoff phase without sleeping")
			worker.poll(ctx)
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts), "read controller settlement after the handoff phase deadline")
			require.Equal(t, models.JobStatusDeadLetter, status, "retirement outages must retain a finite two-hour phase deadline")
			require.Equal(t, 8, attempts, "phase exhaustion must settle the actual eighth controller claim")
			require.Equal(t, int32(2), fault.calls.Load(), "the second actual retirement attempt must remain faulted")
			metadata, err = f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read terminal obsolete source metadata")
			require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "phase exhaustion may fail the source while admitted replacement work survives")
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='start_code_review_reassessment'`, f.job.OrgID).Scan(&starterCount), "count replacement admission after source retry exhaustion")
			require.Equal(t, 1, starterCount, "source replay must join its durable starter rather than duplicate it")
			pending, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read the dispute before replacement ownership is bound")
			require.Equal(t, models.CodeReviewDisputeReassessmentRunning, pending.ReassessmentStatus, "source dead-letter reconciliation must preserve admitted replacement work")
			require.Equal(t, &f.job.SessionID, pending.ReassessmentSessionID, "the source remains the dispute owner only until starter dispatch binds replacement")
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), f.job, "terminal")
			beforeBind, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "actual reply construction must suppress obsolete source failure during admitted handoff")
			require.Equal(t, models.CodeReviewDisputeReassessmentRunning, beforeBind.ReassessmentStatus, "BuildReply must leave the unbound replacement reassessment pending")
			afterSync, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read durable dispute after both source terminal reconciliation paths")
			require.Equal(t, pending, afterSync, "source terminal sync and BuildReply must preserve every dispute field until replacement binding")
			if tt.starterFail {
				services.PR = &stubPRService{getCodeReviewPRSnapshotFn: func(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
					return ghservice.CodeReviewPullRequestSnapshot{}, &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Body: []byte(`{"message":"Resource not accessible by integration"}`)}
				}}
				var failedStarterID uuid.UUID
				require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='pending',attempts=7,max_attempts=8,target_node_id=$2,priority=100000,run_at=now() WHERE org_id=$1 AND job_type='start_code_review_reassessment' RETURNING id`, f.job.OrgID, nodeID).Scan(&failedStarterID), "reproduce the replacement starter's eighth actual worker claim")
				worker.Register(models.JobTypeStartCodeReviewReassessment, newStartCodeReviewReassessmentHandler(f.stores, services, zerolog.Nop()))
				worker.poll(ctx)
				require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, failedStarterID).Scan(&status, &attempts), "read actual replacement starter dead-letter settlement")
				require.Equal(t, models.JobStatusDeadLetter, status, "definitive snapshot permission failure must dead-letter the actual replacement starter")
				require.Equal(t, 8, attempts, "replacement starter must settle its actual eighth claim")
				detail := "The reassessment could not start after repeated attempts. The objection remains recorded for a policy owner."
				failed, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
				require.NoError(t, err, "build the actual replacement starter failure reply")
				require.Equal(t, models.CodeReviewDisputeReassessmentFailed, failed.ReassessmentStatus, "durable handoff protection must not suppress genuine replacement starter failure")
				require.Equal(t, &detail, failed.StatusDetail, "replacement starter failure must retain its exact reason")
				require.Equal(t, &f.job.SessionID, failed.ReassessmentSessionID, "unbound starter failure must retain the last reassessment owner")
				require.Nil(t, failed.ReassessmentDecision, "starter failure must not report a fabricated review decision")
				return
			}
			fault.fail.Store(false)
			var starterID uuid.UUID
			var starterPayload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,payload FROM jobs WHERE org_id=$1 AND job_type='start_code_review_reassessment'`, f.job.OrgID).Scan(&starterID, &starterPayload), "load the single durable replacement starter")
			require.NoError(t, newStartCodeReviewReassessmentHandler(f.stores, services, zerolog.Nop())(ctx, starterID.String(), starterPayload), "actual legacy starter must dispatch and bind replacement despite failed source retirement")
			latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read the dispatched current-policy replacement")
			require.NotEqual(t, f.job.SessionID, latest.SessionID, "replacement dispatch must create a new immutable reassessment owner")
			require.Equal(t, policy.ID, latest.PolicyID, "replacement must resolve the active policy after source capture drift")
			bound, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read replacement-bound dispute before completion")
			require.Equal(t, &latest.SessionID, bound.ReassessmentSessionID, "actual starter dispatch must bind the replacement as reassessment owner")
			decision := models.CodeReviewDecisionApproved
			_, err = f.stores.CodeReviews.CompleteReview(ctx, f.job.OrgID, db.CompleteCodeReviewParams{SessionID: latest.SessionID, Decision: decision, Acceptable: true, FinalReviewBody: "Replacement decision."})
			require.NoError(t, err, "complete the actual replacement metadata with its approved decision")
			replacementJob := f.job
			replacementJob.SessionID = latest.SessionID
			enqueueCodeReviewStatusCommentSync(ctx, f.stores, services, zerolog.Nop(), replacementJob, "terminal")
			completed, _, err := disputes.BuildReply(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "build the replacement's final dispute reply through the actual service")
			require.Equal(t, models.CodeReviewDisputeReassessmentCompleted, completed.ReassessmentStatus, "replacement completion must settle reassessment after source retirement exhaustion")
			require.Equal(t, &latest.SessionID, completed.ReassessmentSessionID, "final dispute decision must retain replacement ownership")
			require.Equal(t, &decision, completed.ReassessmentDecision, "final dispute decision must exactly match the replacement's approval")
			require.Equal(t, new(true), completed.ReassessmentFlipped, "replacement approval must record that the original blocked decision changed")
		})
	}
}
