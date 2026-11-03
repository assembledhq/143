package worker

import (
	"context"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFullAssessmentCompletionWithRetainedWorkspacePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for retained workspace completion proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, blocker, parent string
		noPublication         bool
	}{
		{name: "failed confirmed parent with retained workspace completes"},
		{name: "running confirmed parent with retained workspace completes", parent: "running"},
		{name: "idle confirmed parent with retained workspace completes", parent: "idle"},
		{name: "running parent without required publication completes", parent: "running", noPublication: true},
		{name: "active turn remains protected", blocker: "turn"},
		{name: "running thread remains protected", blocker: "thread"},
		{name: "expired live runtime remains protected", blocker: "runtime"},
		{name: "expired running executor remains protected", blocker: "executor"},
		{name: "preview remains protected", blocker: "preview"},
		{name: "unrelated sandbox holder remains protected", blocker: "holder"},
		{name: "unrelated review holder remains protected", blocker: "review_holder"},
		{name: "queued workspace preparation remains protected", blocker: "prepare"},
		{name: "completed parent queued workspace preparation remains protected", blocker: "prepare", parent: "completed"},
		{name: "idle parent active recheck dispatch remains protected", blocker: "dispatch", parent: "idle"},
	}
	for _, route := range []string{"complete", "reconcile"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					parentStatus := tt.parent
					if parentStatus == "" {
						parentStatus = "failed"
					}
					f, _ := seedConfirmedDrainReview(t, pool, parentStatus)
					ctx := f.ctx()
					exec := func(query string, args ...any) {
						t.Helper()
						_, err := pool.Exec(ctx, query, args...)
						require.NoError(t, err, "seed isolated retained workspace completion state")
					}
					threadID, executionJobID, nodeID := seedConfirmedDrainThread(t, pool, f)
					containerID, snapshotKey := "retained-workspace", "saved-review-snapshot"
					exec(`UPDATE session_threads SET status='completed',completed_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, threadID)
					exec(`UPDATE sessions SET container_id=$3,worker_node_id=$4,snapshot_key=$5,sandbox_state='running',workspace_generation=7,agent_session_id='saved-native-context' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, containerID, nodeID, snapshotKey)
					exec(`INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at) VALUES($1,$2,$3,'code_review',$4,$5,gen_random_uuid(),'active',now()+interval '1 hour')`, f.job.OrgID, f.job.SessionID, containerID, f.job.MetadataID, nodeID)
					if parentStatus != "failed" {
						exec(`UPDATE code_review_session_metadata SET status='running',failure_reason=NULL WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
					}
					if tt.noPublication {
						exec(`UPDATE code_review_revision_assessments SET status='running',publication_state='not_started',publication_receipt=NULL,github_review_id=NULL,github_review_url=NULL,submitted_commit_sha=NULL WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
						exec(`UPDATE code_review_session_metadata SET github_review_id=NULL,github_review_url=NULL WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
					}
					if route == "reconcile" {
						exec(`UPDATE code_review_session_metadata SET status='completed',decision='blocked',acceptable=false,final_review_body='immutable saved review',failure_reason=NULL WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
					}
					switch tt.blocker {
					case "turn":
						exec(`UPDATE sessions SET turn_holding_container=true WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
					case "thread":
						exec(`UPDATE session_threads SET status='running',completed_at=NULL WHERE org_id=$1 AND id=$2`, f.job.OrgID, threadID)
					case "runtime":
						exec(`INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,status,owner_node_id,lease_token,lease_expires_at) VALUES($1,$2,$3,'claude_code','live',$4,gen_random_uuid(),now()-interval '1 hour')`, f.job.OrgID, f.job.SessionID, threadID, nodeID)
					case "executor":
						exec(`UPDATE session_executors SET status='running',lease_expires_at=now()-interval '1 hour' WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, executionJobID)
					case "preview":
						userID := uuid.New()
						exec(`INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Workspace preview owner')`, userID, f.job.OrgID, userID.String()+"@example.invalid")
						exec(`INSERT INTO preview_instances(org_id,session_id,user_id,preview_holding_container) VALUES($1,$2,$3,true)`, f.job.OrgID, f.job.SessionID, userID)
					case "holder", "review_holder":
						kind := "snapshot"
						if tt.blocker == "review_holder" {
							kind = "code_review"
						}
						exec(`INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at) VALUES($1,$2,$3,$4,gen_random_uuid(),$5,gen_random_uuid(),'draining',now()+interval '1 hour')`, f.job.OrgID, f.job.SessionID, containerID, kind, nodeID)
					case "prepare":
						exec(`INSERT INTO jobs(org_id,queue,job_type,payload,status) VALUES($1,'agent','prepare_code_review_workspace',jsonb_build_object('session_id',$2::text),'pending')`, f.job.OrgID, f.job.SessionID)
					case "dispatch":
						exec(`INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status) VALUES($1,$2,$3,$4,$5,$6,1,'retained-workspace-dispatch',1,$7,'pending')`, f.assessment, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, threadID, executionJobID)
					}
					snapshot := func() string {
						t.Helper()
						var state string
						err := pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(s),to_jsonb(m),to_jsonb(a),to_jsonb(p),(SELECT jsonb_agg(to_jsonb(h) ORDER BY h.id) FROM session_sandbox_holders h WHERE h.org_id=s.org_id AND h.session_id=s.id))::text FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id JOIN code_review_revision_assessments a ON a.org_id=m.org_id AND a.metadata_id=m.id JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id WHERE s.org_id=$1 AND s.id=$2`, f.job.OrgID, f.job.SessionID).Scan(&state)
						require.NoError(t, err, "snapshot completion, scheduler and exact holder transaction state")
						return state
					}
					before := snapshot()
					staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
					require.NoError(t, err, "read immutable staged assessment before completion")
					metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "read exact legacy completion provenance")
					cleared, err := f.stores.Sessions.FinalizeIdleCodeReviewContainer(ctx, f.job.OrgID, f.job.SessionID, containerID)
					require.NoError(t, err, "cleanup evaluates active publication and controller without SQL errors")
					require.False(t, cleared, "active assessment and running completion job retain workspace reference")
					if route == "reconcile" {
						err = reconcileCompletedFullAssessment(ctx, f.stores, metadata)
					} else {
						err = completeFullAssessment(ctx, pool, staged, db.CompleteCodeReviewParams{
							SessionID: f.job.SessionID, Decision: *staged.Decision, Acceptable: *staged.Acceptable,
							GitHubReviewID: staged.GitHubReviewID, GitHubReviewURL: staged.GitHubReviewURL,
							FinalReviewBody: *staged.RenderedBody, RiskReasonDetails: []models.CodeReviewRiskReason{},
						}, staged.StructuredOutcome, true, *staged.RenderedBody)
					}
					if tt.blocker != "" {
						require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "active workspace ownership must prevent full assessment completion")
						require.Equal(t, before, snapshot(), "blocked completion must roll back legacy metadata, parent ownership, assessment, scheduler and warm holder release")
						return
					}
					require.NoError(t, err, "terminal review must complete while its idle workspace reference remains retained")
					parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "read parent and retained workspace after assessment completion")
					expectedParentStatus := models.SessionStatus(parentStatus)
					if expectedParentStatus == models.SessionStatusFailed {
						expectedParentStatus = models.SessionStatusCompleted
					}
					require.Equal(t, expectedParentStatus, parent.Status, "assessment completion repairs failed parents and preserves ordinary parent status")
					require.Equal(t, &containerID, parent.ContainerID, "completion must preserve retained physical workspace identity")
					require.Equal(t, &nodeID, parent.WorkerNodeID, "completion must preserve physical workspace owner")
					require.Equal(t, &snapshotKey, parent.SnapshotKey, "completion must preserve saved workspace snapshot")
					require.Equal(t, int64(7), parent.WorkspaceGeneration, "completion must preserve workspace generation")
					require.Equal(t, new("saved-native-context"), parent.AgentSessionID, "completion must preserve native continuation identity")
					var owner *uuid.UUID
					require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&owner), "read durable conversation owner")
					require.Equal(t, &f.job.PullRequestID, owner, "completion claims the exact PR without clearing its workspace")
					var holderStatus string
					var holderReleased bool
					require.NoError(t, pool.QueryRow(ctx, `SELECT status,released_at IS NOT NULL FROM session_sandbox_holders WHERE org_id=$1 AND session_id=$2 AND holder_kind='code_review' AND holder_id=$3`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&holderStatus, &holderReleased), "read exact terminal review warm holder")
					require.Equal(t, "released", holderStatus, "completion releases only its terminal review warm holder")
					require.True(t, holderReleased, "released warm holder has a durable release timestamp")
					completed, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
					require.NoError(t, err, "read committed assessment without controller reconciliation")
					require.Equal(t, models.CodeReviewAssessmentCompleted, completed.Status, "assessment commits before physical workspace cleanup")
					expectedPublicationState := models.CodeReviewPublicationConfirmed
					if tt.noPublication {
						expectedPublicationState = models.CodeReviewPublicationNotRequired
					}
					require.Equal(t, expectedPublicationState, completed.PublicationState, "completion records whether its staged result required external publication")
					require.Equal(t, staged.PublicationReceipt, completed.PublicationReceipt, "completion preserves immutable publication receipt")
					require.Equal(t, staged.StructuredOutcome, completed.StructuredOutcome, "completion preserves immutable structured outcome")
					require.Equal(t, staged.RenderedBody, completed.RenderedBody, "completion preserves immutable saved review body")
					schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
					require.NoError(t, err, "read settled scheduler before cleanup")
					require.Equal(t, &f.assessment, schedule.CurrentAssessmentID, "completed assessment becomes scheduler's current outcome")
					require.Nil(t, schedule.ActiveAssessmentID, "completion settles active assessment reservation")
					require.Nil(t, schedule.ActiveSessionID, "completion settles active controller session")
					require.Equal(t, models.CodeReviewScheduleCovered, schedule.State, "completed exact-head assessment covers scheduler inputs")
					cleared, err = f.stores.Sessions.FinalizeIdleCodeReviewContainer(ctx, f.job.OrgID, f.job.SessionID, containerID)
					require.NoError(t, err, "cleanup evaluates completed assessment while controller still owns its job")
					require.False(t, cleared, "running completion controller still blocks workspace reclamation")
					exec(`UPDATE jobs SET status='succeeded',completed_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
					cleared, err = f.stores.Sessions.FinalizeIdleCodeReviewContainer(ctx, f.job.OrgID, f.job.SessionID, containerID)
					require.NoError(t, err, "cleanup reclaims workspace after its completion controller succeeds")
					require.True(t, cleared, "completion unblocks later physical workspace reference reclamation")
					parent, err = f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "read parent after physical workspace reference reclamation")
					require.Nil(t, parent.ContainerID, "cleanup clears the retained physical workspace reference")
					require.Nil(t, parent.WorkerNodeID, "cleanup clears physical workspace owner")
					require.Equal(t, &snapshotKey, parent.SnapshotKey, "cleanup retains saved snapshot for future native continuation")
					require.Equal(t, models.SandboxStateSnapshotted, parent.SandboxState, "cleaned workspace retains its saved snapshot state")
				})
			}
		})
	}
}
