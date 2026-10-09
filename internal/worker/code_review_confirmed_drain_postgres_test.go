package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func seedConfirmedDrainReview(t *testing.T, pool *pgxpool.Pool, parent string) (strandedReviewFixture, json.RawMessage) {
	t.Helper()
	f := seedStrandedReview(t, pool, parent)
	reviewID := int64(9876)
	reviewURL := "https://example.invalid/review/9876"
	receipt, err := fullAssessmentPublicationReceipt(f.assessment, "all", "head", &reviewID, &reviewURL, nil, nil)
	require.NoError(t, err, "encode exact saved publication receipt")
	_, err = pool.Exec(f.ctx(), `UPDATE code_review_revision_assessments SET status='publishing',publication_state='confirmed',publication_receipt=$3,github_review_id=$4,github_review_url=$5,submitted_commit_sha='head',result_origin='executed',coverage_complete=true,decision='blocked',acceptable=false,rendered_body='immutable saved review',risk_reason_details='[]',structured_outcome='{"coverage_complete":true,"description_assessments":[]}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, receipt, reviewID, reviewURL)
	require.NoError(t, err, "stage confirmed reusable full assessment")
	_, err = pool.Exec(f.ctx(), `UPDATE code_review_session_metadata SET status='failed',failure_reason='controller retry exhausted',github_review_id=$3,github_review_url=$4 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, reviewID, reviewURL)
	require.NoError(t, err, "seed terminal controller metadata with exact receipt")
	_, err = pool.Exec(f.ctx(), `UPDATE code_review_pr_state SET active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID, f.assessment)
	require.NoError(t, err, "pin scheduler to original assessment")
	return f, receipt
}

func seedConfirmedDrainThread(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture) (uuid.UUID, uuid.UUID, string) {
	t.Helper()
	thread, jobID := uuid.New(), uuid.New()
	node := "confirmed-drain-test-" + jobID.String()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO nodes(id,mode) VALUES($1,'worker')`, []any{node}},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status) VALUES($1,$2,$3,'claude_code','Saved reviewer','running')`, []any{thread, f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO jobs(id,org_id,queue,job_type,payload,status) VALUES($1,$2,'agent','continue_session',jsonb_build_object('session_id',$3::text,'thread_id',$4::text),'dead_letter')`, []any{jobID, f.job.OrgID, f.job.SessionID, thread}},
		{`INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status) VALUES($1,$2,$3,$4,'continue_session',$5,'test',gen_random_uuid(),'failed')`, []any{f.job.OrgID, f.job.SessionID, thread, jobID, node}},
	} {
		_, err := pool.Exec(f.ctx(), row.query, row.args...)
		require.NoError(t, err, "seed exact orphan terminal executor and bound job")
	}
	return thread, jobID, node
}

//nolint:paralleltest // Migrate once before parallel isolated-tenant cases to bound PostgreSQL's lock budget.
func TestConfirmedFullPublicationDrainRecoveryPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for confirmed drain recovery proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, parent, change                     string
		orphan, wantComplete, wantThreadTerminal bool
	}{
		{name: "fully drained failed parent recovers", parent: "failed", wantComplete: true},
		{name: "failed parent historical receipt survives moved PR head", parent: "failed", change: "moved_head", wantComplete: true},
		{name: "failed parent and terminal orphan recover", parent: "failed", orphan: true, wantComplete: true, wantThreadTerminal: true},
		{name: "idle parent and terminal orphan recover", parent: "idle", orphan: true, wantComplete: true, wantThreadTerminal: true},
		{name: "stale metadata cancellation repairs exact orphan", parent: "failed", orphan: true, change: "stale", wantComplete: true, wantThreadTerminal: true},
		{name: "cancelled metadata cancellation repairs exact orphan", parent: "failed", orphan: true, change: "cancelled", wantComplete: true, wantThreadTerminal: true},
		{name: "expired active executor stays protected", parent: "failed", orphan: true, change: "executor"},
		{name: "expired active runtime stays protected", parent: "failed", orphan: true, change: "runtime"},
		{name: "queued replacement stays protected", parent: "failed", orphan: true, change: "queue"},
		{name: "terminal executor with pending job stays protected", parent: "failed", orphan: true, change: "pending_job"},
		{name: "running thread without executor proof stays protected", parent: "failed", orphan: true, change: "no_executor"},
		{name: "active container stays protected", parent: "failed", change: "container"},
		{name: "active turn holder stays protected", parent: "failed", change: "turn_holder"},
		{name: "active sandbox holder stays protected", parent: "failed", change: "holder"},
		{name: "queued workspace prep stays protected", parent: "failed", change: "prepare"},
		{name: "wrong controller key cannot repair orphan", parent: "failed", orphan: true, change: "job_key"},
		{name: "wrong controller head cannot repair orphan", parent: "failed", orphan: true, change: "job_head"},
		{name: "wrong controller metadata cannot repair orphan", parent: "failed", orphan: true, change: "job_metadata"},
		{name: "wrong controller policy cannot repair orphan", parent: "failed", orphan: true, change: "job_policy"},
		{name: "wrong controller PR cannot repair orphan", parent: "failed", orphan: true, change: "job_pr"},
		{name: "wrong controller repository cannot repair orphan", parent: "failed", orphan: true, change: "job_repo"},
		{name: "missing receipt cannot repair orphan", parent: "failed", orphan: true, change: "receipt_missing"},
		{name: "empty receipt cannot repair orphan", parent: "failed", orphan: true, change: "receipt_empty"},
		{name: "wrong receipt head cannot repair orphan", parent: "failed", orphan: true, change: "receipt_head"},
		{name: "wrong receipt review cannot repair orphan", parent: "failed", orphan: true, change: "receipt_review"},
		{name: "wrong receipt assessment cannot repair orphan", parent: "failed", orphan: true, change: "receipt_assessment"},
		{name: "wrong receipt digest cannot repair orphan", parent: "failed", orphan: true, change: "receipt_digest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, receipt := seedConfirmedDrainReview(t, pool, tt.parent)
			ctx := f.ctx()
			prID := f.job.PullRequestID
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, query, args...)
				require.NoError(t, err, "seed protected recovery case")
			}
			var thread, executionJob uuid.UUID
			var node string
			if tt.orphan {
				thread, executionJob, node = seedConfirmedDrainThread(t, pool, f)
			}
			switch tt.change {
			case "moved_head":
				exec(`UPDATE pull_requests SET head_sha='new-head' WHERE org_id=$1 AND id=$2`, f.job.OrgID, prID)
				exec(`UPDATE code_review_pr_state SET head_sha='new-head' WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, prID)
			case "stale", "cancelled":
				exec(`UPDATE code_review_session_metadata SET status=$3 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, tt.change)
				exec(`UPDATE session_threads SET cancel_requested_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, thread)
			case "executor":
				exec(`UPDATE session_executors SET status='running',lease_expires_at=now()-interval '1 hour' WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, executionJob)
			case "runtime":
				exec(`INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,status,owner_node_id,lease_token,lease_expires_at) VALUES($1,$2,$3,'claude_code','live',$4,gen_random_uuid(),now()-interval '1 hour')`, f.job.OrgID, f.job.SessionID, thread, node)
			case "queue", "prepare":
				kind := "continue_session"
				if tt.change == "prepare" {
					kind = "prepare_code_review_workspace"
				}
				exec(`INSERT INTO jobs(org_id,queue,job_type,payload) VALUES($1,'agent',$3,jsonb_build_object('session_id',$2::text))`, f.job.OrgID, f.job.SessionID, kind)
			case "pending_job":
				exec(`UPDATE jobs SET status='pending' WHERE org_id=$1 AND id=$2`, f.job.OrgID, executionJob)
			case "no_executor":
				exec(`DELETE FROM session_executors WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, executionJob)
			case "container":
				exec(`UPDATE sessions SET container_id='test-container' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			case "turn_holder":
				exec(`UPDATE sessions SET turn_holding_container=true WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			case "holder":
				exec(`INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at) VALUES($1,$2,'test-container','snapshot',gen_random_uuid(),'test-node',gen_random_uuid(),'active',now()+interval '1 hour')`, f.job.OrgID, f.job.SessionID)
			case "job_key":
				f.job.OutputKey = "wrong-key"
			case "job_head":
				f.job.HeadSHA = "wrong-head"
			case "job_metadata":
				f.job.MetadataID = uuid.New()
			case "job_policy":
				f.job.PolicyID = uuid.New()
			case "job_pr":
				f.job.PullRequestID = uuid.New()
			case "job_repo":
				f.job.RepositoryID = uuid.New()
			case "receipt_missing":
				exec(`UPDATE code_review_revision_assessments SET publication_receipt=NULL WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			case "receipt_empty":
				exec(`UPDATE code_review_revision_assessments SET publication_receipt='{}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			case "receipt_head", "receipt_review", "receipt_assessment", "receipt_digest":
				field, value := "submitted_commit_sha", any("wrong-head")
				if tt.change == "receipt_review" {
					field, value = "github_review_id", int64(88)
				}
				if tt.change == "receipt_assessment" {
					field, value = "assessment_id", uuid.NewString()
				}
				if tt.change == "receipt_digest" {
					field, value = "input_digest", "wrong-digest"
				}
				var parsed map[string]any
				require.NoError(t, json.Unmarshal(receipt, &parsed), "decode immutable receipt fixture")
				parsed[field] = value
				altered, err := json.Marshal(parsed)
				require.NoError(t, err, "encode mismatched saved receipt")
				exec(`UPDATE code_review_revision_assessments SET publication_receipt=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, altered)
			}
			before, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "capture exact staged publication")
			metadataBefore, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "capture terminal metadata")
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode controller identity")
			capture, publisher := &fixedRecheckCapture{}, &fakeRecheckPublisher{}
			// Workspace warm-holder maintenance is tested elsewhere; these cases isolate
			// confirmed outcome recovery and include the real parent reconciliation store.
			f.stores.CodeReviewWorkspaces = nil
			err = newRunCodeReviewHandler(f.stores, &Services{CodeReviewInputCapture: capture, CodeReviews: publisher}, zerolog.Nop())(ctx, "run_code_review", payload)
			if tt.wantComplete {
				require.NoError(t, err, "drained confirmed assessment must finish through normal controller")
			} else {
				require.Error(t, err, "protected recovery must remain blocked")
			}
			require.Equal(t, 0, capture.calls, "confirmed historical result must not recapture inputs")
			require.Empty(t, publisher.requests, "confirmed result must never resend publication")
			require.Empty(t, publisher.reconciliations, "confirmed receipt must not require network lookup")
			after, readErr := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, readErr, "read assessment after recovery")
			metadataAfter, readErr := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, readErr, "read metadata after recovery")
			schedule, readErr := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, prID)
			require.NoError(t, readErr, "read scheduler after recovery")
			parent, readErr := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, readErr, "read parent after recovery")
			require.Equal(t, before.PublicationReceipt, after.PublicationReceipt, "saved receipt is immutable")
			require.Equal(t, before.StructuredOutcome, after.StructuredOutcome, "saved outcome is immutable")
			var owner *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&owner), "read exact durable conversation owner")
			if tt.wantComplete {
				require.Equal(t, models.CodeReviewAssessmentCompleted, after.Status, "confirmed full assessment completes")
				require.Equal(t, models.CodeReviewSessionStatusCompleted, metadataAfter.Status, "metadata completion commits with assessment")
				require.Equal(t, models.SessionStatusCompleted, parent.Status, "controller marks recovered parent completed only after proof")
				require.Equal(t, &prID, owner, "conversation retains exact full assessment owner")
				require.Equal(t, &f.assessment, schedule.CurrentAssessmentID, "confirmed result advances current assessment")
				require.Nil(t, schedule.ActiveAssessmentID, "active assessment reservation settles")
				if tt.change == "moved_head" {
					require.Equal(t, models.CodeReviewScheduleIdle, schedule.State, "historical confirmed receipt must not cover the newer PR head")
				}
			} else {
				require.Equal(t, before, after, "blocked recovery must retain exact staged assessment")
				require.Equal(t, metadataBefore, metadataAfter, "blocked recovery must retain terminal metadata")
				require.Equal(t, models.SessionStatus(tt.parent), parent.Status, "blocked recovery must not revive failed parent")
				require.Nil(t, owner, "blocked recovery must not create owner authority")
				require.Equal(t, &f.assessment, schedule.ActiveAssessmentID, "blocked recovery retains original assessment reservation")
			}
			if tt.orphan {
				got, readErr := f.stores.SessionThreads.GetByID(ctx, f.job.OrgID, thread)
				require.NoError(t, readErr, "read exact orphan thread")
				expected := models.ThreadStatusRunning
				if tt.wantThreadTerminal {
					expected = models.ThreadStatusFailed
					if tt.change == "stale" || tt.change == "cancelled" {
						expected = models.ThreadStatusCancelled
					}
				}
				require.Equal(t, expected, got.Status, "only proven terminal orphan may be repaired")
			}
		})
	}
}

//nolint:paralleltest // Full migration setup is serial; each case owns its tenant.
func TestFailedConfirmedFullAssessmentOwnerPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for failed owner recovery proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, change, parent string
		wantClaim            bool
	}{
		{name: "only pinned drained confirmed full outcome claims", wantClaim: true},
		{name: "ordinary idle owner is unchanged", parent: "idle", wantClaim: true},
		{name: "ordinary running owner is unchanged", parent: "running", wantClaim: true},
		{name: "ordinary completed owner is unchanged", parent: "completed", wantClaim: true},
		{name: "generic failed parent without confirmed stage", change: "unstaged"},
		{name: "missing coverage", change: "coverage"},
		{name: "reserved publication", change: "reserved"},
		{name: "uncertain publication", change: "uncertain"},
		{name: "unsent publication", change: "not_started"},
		{name: "publication not required", change: "not_required"},
		{name: "not publishing", change: "status"},
		{name: "missing receipt", change: "receipt"},
		{name: "invalid receipt shape", change: "receipt_shape"},
		{name: "missing GitHub ID", change: "review"},
		{name: "wrong stored commit", change: "commit"},
		{name: "wrong metadata head", change: "metadata_head"},
		{name: "wrong metadata publication key", change: "metadata_key"},
		{name: "wrong metadata review", change: "metadata_review"},
		{name: "missing metadata assessment pin", change: "metadata_assessment"},
		{name: "metadata still failed", change: "metadata_status"},
		{name: "thread still nonterminal", change: "thread"},
		{name: "active recheck dispatch", change: "dispatch"},
		{name: "active preview holder", change: "preview"},
		{name: "different tenant", change: "org"},
		{name: "different PR", change: "pr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parentStatus := tt.parent
			if parentStatus == "" {
				parentStatus = "failed"
			}
			f, _ := seedConfirmedDrainReview(t, pool, parentStatus)
			ctx := f.ctx()
			exec := func(q string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, q, args...)
				require.NoError(t, err, "seed direct failed-owner fence")
			}
			exec(`UPDATE code_review_session_metadata SET status='completed',assessment_id=$3 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, f.assessment)
			orgID, prID := f.job.OrgID, f.job.PullRequestID
			switch tt.change {
			case "unstaged":
				exec(`UPDATE code_review_revision_assessments SET result_origin=NULL WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "coverage":
				exec(`UPDATE code_review_revision_assessments SET coverage_complete=false WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "reserved", "uncertain", "not_started", "not_required":
				exec(`UPDATE code_review_revision_assessments SET publication_state=$3 WHERE org_id=$1 AND id=$2`, orgID, f.assessment, tt.change)
			case "status":
				exec(`UPDATE code_review_revision_assessments SET status='running' WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "receipt":
				exec(`UPDATE code_review_revision_assessments SET publication_receipt=NULL WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "receipt_shape":
				exec(`UPDATE code_review_revision_assessments SET publication_receipt=jsonb_set(publication_receipt,'{github_review_id}','"9876"') WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "review":
				exec(`UPDATE code_review_revision_assessments SET github_review_id=NULL WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "commit":
				exec(`UPDATE code_review_revision_assessments SET submitted_commit_sha='wrong-head' WHERE org_id=$1 AND id=$2`, orgID, f.assessment)
			case "metadata_head":
				exec(`UPDATE code_review_session_metadata SET head_sha='wrong-head' WHERE org_id=$1 AND session_id=$2`, orgID, f.job.SessionID)
			case "metadata_key":
				exec(`UPDATE code_review_session_metadata SET review_output_key='wrong-key' WHERE org_id=$1 AND session_id=$2`, orgID, f.job.SessionID)
			case "metadata_review":
				exec(`UPDATE code_review_session_metadata SET github_review_id=88 WHERE org_id=$1 AND session_id=$2`, orgID, f.job.SessionID)
			case "metadata_assessment":
				exec(`UPDATE code_review_session_metadata SET assessment_id=NULL WHERE org_id=$1 AND session_id=$2`, orgID, f.job.SessionID)
			case "metadata_status":
				exec(`UPDATE code_review_session_metadata SET status='failed' WHERE org_id=$1 AND session_id=$2`, orgID, f.job.SessionID)
			case "thread", "dispatch":
				thread, jobID, _ := seedConfirmedDrainThread(t, pool, f)
				if tt.change == "dispatch" {
					exec(`UPDATE session_threads SET status='failed',completed_at=now() WHERE org_id=$1 AND id=$2`, orgID, thread)
					exec(`INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status) VALUES($1,$2,$3,$4,$5,$6,1,'test-digest',1,$7,'pending')`, f.assessment, orgID, f.job.RepositoryID, prID, f.job.SessionID, thread, jobID)
				}
			case "preview":
				userID := uuid.New()
				exec(`INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Test preview owner')`, userID, orgID, userID.String()+"@example.invalid")
				exec(`INSERT INTO preview_instances(org_id,session_id,user_id,preview_holding_container) VALUES($1,$2,$3,true)`, orgID, f.job.SessionID, userID)
			case "org":
				orgID = uuid.New()
			case "pr":
				prID = uuid.New()
			}
			before, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "snapshot exact staged full outcome")
			parentBefore, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "capture original parent attributes before claiming")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin failed-owner claim transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			claimed, err := db.NewCodeReviewRecheckStore(pool).ClaimFullAssessmentOwner(ctx, tx, orgID, f.job.SessionID, prID)
			require.NoError(t, err, "owner eligibility must fail closed without SQL errors")
			require.Equal(t, tt.wantClaim, claimed, "only exact confirmed full outcome with complete drain gains authority")
			require.NoError(t, tx.Commit(ctx), "commit owner eligibility proof")
			after, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read preserved outcome")
			require.Equal(t, before, after, "owner claiming must not rewrite assessment publication or outcome")
			var status string
			var owner *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&status, &owner), "read parent and exact owner")
			expectedStatus := parentStatus
			if tt.wantClaim && parentStatus == "failed" {
				expectedStatus = "completed"
			}
			require.Equal(t, expectedStatus, status, "eligible failed-parent status and ownership must commit together")
			if parentStatus != "failed" {
				parentAfter, readErr := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, readErr, "read unchanged ordinary owner session attributes")
				require.Equal(t, parentBefore, parentAfter, "ordinary owner claiming must preserve every existing parent attribute")
			}
			var expectedOwner *uuid.UUID
			if tt.wantClaim {
				expectedOwner = &f.job.PullRequestID
			}
			require.Equal(t, expectedOwner, owner, "invalid recovery must not create conversation ownership")
		})
	}
}
