package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func seedEndedReviewRetainedWorkspace(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture) (uuid.UUID, uuid.UUID, string) {
	t.Helper()
	thread, jobID, node := seedConfirmedDrainThread(t, pool, f)
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE session_threads SET status='idle' WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, thread}},
		{`UPDATE sessions SET container_id='ended-review-workspace',worker_node_id=$3,snapshot_key='ended-review-snapshot',sandbox_state='running',workspace_generation=7 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.SessionID, node}},
		{`INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at) VALUES($1,$2,'ended-review-workspace','code_review',$3,$4,gen_random_uuid(),'active',now()-interval '1 minute')`, []any{f.job.OrgID, f.job.SessionID, f.job.MetadataID, node}},
	} {
		_, err := pool.Exec(f.ctx(), row.query, row.args...)
		require.NoError(t, err, "seed ended parent after its bounded warm workspace holder expires")
	}
	return thread, jobID, node
}

func TestEndedFullReviewWithRetainedWorkspacePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for ended review retained workspace recovery proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, parent, blocker string
		saved, wantResume     bool
	}{
		{name: "completed parent resumes saved work", parent: "completed", saved: true, wantResume: true},
		{name: "failed parent settles while preserving saved work", parent: "failed", saved: true},
		{name: "completed parent without saved work settles", parent: "completed"},
		{name: "failed parent without saved work settles", parent: "failed"},
		{name: "active turn remains protected", parent: "completed", saved: true, blocker: "turn"},
		{name: "running thread remains protected", parent: "completed", saved: true, blocker: "thread"},
		{name: "expired live runtime remains protected", parent: "completed", saved: true, blocker: "runtime"},
		{name: "expired running executor remains protected", parent: "completed", saved: true, blocker: "executor"},
		{name: "live warm review holder remains protected", parent: "completed", saved: true, blocker: "holder"},
		{name: "preview remains protected", parent: "completed", saved: true, blocker: "preview"},
		{name: "queued preparation remains protected", parent: "completed", saved: true, blocker: "prepare"},
		{name: "active recheck dispatch remains protected", parent: "completed", saved: true, blocker: "dispatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, tt.parent)
			ctx := f.ctx()
			thread, executionJob, node := seedEndedReviewRetainedWorkspace(t, pool, f)
			if tt.saved {
				seedSavedReviewResults(t, pool, f)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, query, args...)
				require.NoError(t, err, "seed retained ended workspace ownership blocker")
			}
			switch tt.blocker {
			case "turn":
				exec(`UPDATE sessions SET turn_holding_container=true WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			case "thread":
				exec(`UPDATE session_threads SET status='running' WHERE org_id=$1 AND id=$2`, f.job.OrgID, thread)
			case "runtime":
				exec(`INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,status,owner_node_id,lease_token,lease_expires_at) VALUES($1,$2,$3,'claude_code','live',$4,gen_random_uuid(),now()-interval '1 hour')`, f.job.OrgID, f.job.SessionID, thread, node)
			case "executor":
				exec(`UPDATE session_executors SET status='running',lease_expires_at=now()-interval '1 hour' WHERE org_id=$1 AND job_id=$2`, f.job.OrgID, executionJob)
			case "holder":
				exec(`UPDATE session_sandbox_holders SET expires_at=now()+interval '1 hour' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
			case "preview":
				user := uuid.New()
				exec(`INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Synthetic preview owner')`, user, f.job.OrgID, user.String()+"@example.invalid")
				exec(`INSERT INTO preview_instances(org_id,session_id,user_id,preview_holding_container) VALUES($1,$2,$3,true)`, f.job.OrgID, f.job.SessionID, user)
			case "prepare":
				exec(`INSERT INTO jobs(org_id,queue,job_type,payload,status) VALUES($1,'agent','prepare_code_review_workspace',jsonb_build_object('session_id',$2::text),'pending')`, f.job.OrgID, f.job.SessionID)
			case "dispatch":
				exec(`INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status) VALUES($1,$2,$3,$4,$5,$6,1,'ended-workspace-dispatch',1,$7,'pending')`, f.assessment, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, thread, executionJob)
			}
			cleared, err := f.stores.Sessions.FinalizeIdleCodeReviewContainer(ctx, f.job.OrgID, f.job.SessionID, "ended-review-workspace")
			require.NoError(t, err, "evaluate GC with the original full review controller still active")
			require.False(t, cleared, "running controller and unstaged full assessment retain the physical workspace reference")
			before, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "snapshot saved evidence before ended-parent reconciliation")
			policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
			require.NoError(t, err, "load original captured review policy")
			resumed, settled, err := recoverEndedCodeReviewParent(ctx, f.stores, f.job, policy.Config())
			if tt.blocker != "" {
				require.Error(t, err, "live workspace ownership must still fence ended-parent recovery")
			} else {
				require.NoError(t, err, "idle retained workspace must not block ended-parent recovery")
			}
			require.Equal(t, tt.wantResume, resumed, "only completed parents with validated saved work may resume")
			wantSettled := tt.blocker == "" && !tt.wantResume
			require.Equal(t, wantSettled, settled, "ended parents without resumable saved work must settle durably")
			after, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read preserved saved evidence after recovery")
			require.Equal(t, before, after, "recovery must preserve every saved reviewer result")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read recovered review metadata")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read recovered assessment")
			schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read original scheduler reservation")
			if wantSettled {
				require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "non-resumable ended metadata must retire")
				require.Equal(t, models.CodeReviewAssessmentFailed, assessment.Status, "non-resumable ended assessment must retire")
				require.Nil(t, schedule.ActiveSessionID, "settlement releases the exact original scheduler reservation")
			} else {
				require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "resumable or fenced metadata must stay active")
				require.Equal(t, models.CodeReviewAssessmentRunning, assessment.Status, "resumable or fenced assessment must stay active")
				require.Equal(t, &f.job.SessionID, schedule.ActiveSessionID, "resumable or fenced review retains the original scheduler reservation")
			}
			parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read retained workspace after logical recovery")
			require.Equal(t, new("ended-review-workspace"), parent.ContainerID, "logical recovery preserves physical workspace ownership for GC")
			require.Equal(t, new("ended-review-snapshot"), parent.SnapshotKey, "logical recovery preserves the saved native snapshot")
			var dispatches int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','fork_session_thread','prepare_code_review_workspace') AND status IN ('pending','running')`, f.job.OrgID).Scan(&dispatches)
			require.NoError(t, err, "count newly executable agent work")
			wantDispatches := 0
			if tt.blocker == "prepare" {
				wantDispatches = 1
			}
			require.Equal(t, wantDispatches, dispatches, "ended recovery must never dispatch a new agent turn")
		})
	}
}

func TestEndedSavedReviewWithRetainedWorkspaceHandlerPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for retained workspace saved-result consumption proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	f := seedStrandedReview(t, pool, "completed")
	seedEndedReviewRetainedWorkspace(t, pool, f)
	seedSavedReviewResults(t, pool, f)
	policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
	require.NoError(t, err, "load original saved-result policy")
	capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: policy}}
	publisher := &inlineRecoveryPublisher{}
	services := &Services{CodeReviewVisualEvidence: &codeReviewVisualEvidenceProviderStub{snapshot: models.CodeReviewVisualEvidenceSnapshot{Version: 1, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, HeadSHA: "head", Complete: true}}, CodeReviewWorkspacePreparationEnabled: true, CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviews: publisher}
	before, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "snapshot the immutable saved reviewer and synthesis output")
	payload, err := json.Marshal(f.job)
	require.NoError(t, err, "encode original controller identity")
	err = newRunCodeReviewHandler(f.stores, services, zerolog.Nop())(f.ctx(), "run_code_review", payload)
	require.NoError(t, err, "saved completed work must finish while the idle workspace remains referenced")
	assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read completed saved full assessment")
	require.Equal(t, models.CodeReviewAssessmentCompleted, assessment.Status, "normal freshness and publication must finish the existing assessment")
	after, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read saved evidence after handler consumption")
	require.Equal(t, before, after, "resumption consumes original results without rerunning reviewers")
	require.Greater(t, capture.calls, 0, "saved work must pass normal fresh input validation")
	var dispatches int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','fork_session_thread','prepare_code_review_workspace') AND status IN ('pending','running')`, f.job.OrgID).Scan(&dispatches)
	require.NoError(t, err, "count saved-work agent dispatches")
	require.Equal(t, 0, dispatches, "saved-work consumption must dispatch no new workspace or agent execution")
}

func TestEndedRetainedWorkspaceControllerSweepPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for retained unstaged controller sweep proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, parent string
		liveHolder   bool
	}{
		{name: "completed parent unstaged saved work", parent: "completed"},
		{name: "failed parent unstaged work", parent: "failed"},
		{name: "active workspace holder remains protected", parent: "completed", liveHolder: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, tt.parent)
			ctx := f.ctx()
			seedEndedReviewRetainedWorkspace(t, pool, f)
			seedSavedReviewResults(t, pool, f)
			if tt.liveHolder {
				_, err := pool.Exec(ctx, `UPDATE session_sandbox_holders SET expires_at=now()+interval '1 hour' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "keep live bounded workspace holder fenced")
			}
			_, err := pool.Exec(ctx, `UPDATE jobs SET status='dead_letter' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "exhaust the original controller while deliberately preserving its active assessment")
			sweep := db.NewCodeReviewScheduleStore(pool)
			require.NoError(t, sweep.RepairMissingWakes(ctx), "restore the exact abandoned controller despite its idle retained workspace")
			require.NoError(t, sweep.RepairMissingWakes(ctx), "repeated sweep must not duplicate the restored controller")
			var payloads []json.RawMessage
			rows, err := pool.Query(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending'`, f.job.OrgID)
			require.NoError(t, err, "query repaired original controllers")
			for rows.Next() {
				var payload json.RawMessage
				require.NoError(t, rows.Scan(&payload), "decode restored controller provenance")
				payloads = append(payloads, payload)
			}
			require.NoError(t, rows.Err(), "read every restored controller")
			rows.Close()
			if tt.liveHolder {
				require.Empty(t, payloads, "sweeper must not wake ended work while a sandbox owner remains live")
				return
			}
			require.Len(t, payloads, 1, "sweeper must restore one bounded original controller")
			expected, err := json.Marshal(f.job)
			require.NoError(t, err, "encode expected fork, dispute, policy and publication identity")
			require.JSONEq(t, string(expected), string(payloads[0]), "recovery must preserve the exact original controller payload")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read unstaged assessment after sweep")
			require.Equal(t, models.CodeReviewAssessmentRunning, assessment.Status, "sweep must wake existing work without settling or replacing its assessment")
			require.Nil(t, assessment.ResultOrigin, "restored saved work remains unstaged until the original controller validates it")
		})
	}
}
