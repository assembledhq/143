//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This test uses the suite's shared database and resetDB, so it cannot run in
// parallel with other integration tests.
func TestCodeReviewHolder_ConcurrentHandoffAndTerminalCleanup(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "review/holder")
	session := seedSession(t, pool, orgID, sessionOpts{Origin: "code_review", RepositoryID: &repoID})
	const nodeID = "review-holder-node"
	seedWorkerNode(t, pool, nodeID)
	const containerID = "review-holder-container"
	const headSHA = "abcdef123456"
	_, err := pool.Exec(ctx, `
		UPDATE sessions SET container_id = $1, worker_node_id = $2,
			revision_context = jsonb_build_object('head_sha', $3::text)
		WHERE id = $4 AND org_id = $5`, containerID, nodeID, headSHA, session.ID, orgID)
	require.NoError(t, err, "review session should own the test container and revision")

	var pullRequestID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO pull_requests (org_id, session_id, github_pr_number, github_pr_url, github_repo, title)
		VALUES ($1, $2, 42, 'https://github.com/review/holder/pull/42', 'review/holder', 'Holder test')
		RETURNING id`, orgID, session.ID).Scan(&pullRequestID)
	require.NoError(t, err, "review pull request should be seeded")
	var policyID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO code_review_policies (org_id, version, approval_mode, description_policy,
			risk_policy, agent_roster, review_instructions, automated_approval_policy)
		VALUES ($1, 1, 'comment_only', '{}', '{}', '{}', '', '') RETURNING id`, orgID).Scan(&policyID)
	require.NoError(t, err, "review policy should be seeded")
	var reviewID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO code_review_session_metadata (org_id, session_id, repository_id,
			pull_request_id, policy_id, base_sha, head_sha, trigger_source, status, review_output_key)
		VALUES ($1, $2, $3, $4, $5, 'base123', $6, 'app_reviewer', 'running', $7)
		RETURNING id`, orgID, session.ID, repoID, pullRequestID, policyID, headSHA,
		"holder-"+uuid.NewString()).Scan(&reviewID)
	require.NoError(t, err, "active review metadata should be seeded")
	threadID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO code_review_agent_results (org_id, session_id, agent_provider, role, status, structured_result)
		VALUES ($1, $2, 'claude', 'reviewer', 'running', jsonb_build_object('thread_id', $3::text))`,
		orgID, session.ID, threadID.String())
	require.NoError(t, err, "reviewer result should identify its thread")

	holders := db.NewSessionSandboxHolderStore(pool)
	type acquisition struct {
		token uuid.UUID
		ok    bool
		err   error
	}
	results := make(chan acquisition, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := uuid.New()
			holder, ok, acquireErr := holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
				SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
				OwnerNodeID: nodeID, LeaseToken: token, LeaseDuration: time.Minute,
			})
			if acquireErr == nil && ok && holder.HolderID != reviewID {
				acquireErr = fmt.Errorf("holder references review %s, want %s", holder.HolderID, reviewID)
			}
			results <- acquisition{token: holder.LeaseToken, ok: ok, err: acquireErr}
		}()
	}
	wg.Wait()
	close(results)
	var retainedToken uuid.UUID
	for result := range results {
		require.NoError(t, result.err, "concurrent acquisition should execute without a SQL or locking error")
		require.True(t, result.ok, "both reviewers should retain the same active review holder")
		if retainedToken == uuid.Nil {
			retainedToken = result.token
		}
		require.Equal(t, retainedToken, result.token, "concurrent acquisition must preserve the original fencing token")
	}
	_, err = pool.Exec(ctx, `UPDATE session_sandbox_holders
		SET created_at = now() - interval '110 seconds', expires_at = now() + interval '10 seconds'
		WHERE org_id = $1 AND session_id = $2 AND holder_kind = 'code_review' AND status = 'active'`, orgID, session.ID)
	require.NoError(t, err, "test holder should simulate a staggered second reviewer")
	lateHolder, acquired, err := holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "later reviewer should extend a nearly expired handoff holder")
	require.True(t, acquired, "last reviewer completion should begin a fresh idle retention interval")
	require.Equal(t, retainedToken, lateHolder.LeaseToken, "later reviewer must preserve the fencing token")
	require.WithinDuration(t, time.Now(), lateHolder.CreatedAt, 5*time.Second, "idle retention should start at the latest completed reviewer turn")
	_, err = pool.Exec(ctx, `UPDATE session_sandbox_holders
		SET created_at = now() - interval '130 seconds', expires_at = now() - interval '1 second'
		WHERE org_id = $1 AND session_id = $2 AND holder_kind = 'code_review' AND status = 'active'`, orgID, session.ID)
	require.NoError(t, err, "test holder should simulate an expired row not yet swept by GC")
	lateHolder, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "active reviewer should rearm an expired but unswept holder")
	require.True(t, acquired, "an unswept expired row must not prevent a current turn from retaining its workspace")
	require.Equal(t, retainedToken, lateHolder.LeaseToken, "rearming the same workspace should not transfer the fencing token")
	_, err = pool.Exec(ctx, `UPDATE session_sandbox_holders SET heartbeat_at = now() - interval '21 seconds'
		WHERE org_id = $1 AND session_id = $2 AND holder_kind = 'code_review' AND status = 'active'`, orgID, session.ID)
	require.NoError(t, err, "test holder heartbeat should be old enough to renew")
	renewed, err := holders.RenewCodeReview(ctx, orgID, session.ID, time.Minute)
	require.NoError(t, err, "review holder should renew with a matching head and owner")
	require.True(t, renewed, "eligible holder should renew after the minimum interval")
	renewed, err = holders.RenewCodeReview(ctx, orgID, session.ID, time.Minute)
	require.NoError(t, err, "early renewal should be a harmless no-op")
	require.False(t, renewed, "renewal should be rate-limited to once per 20 seconds")

	sessions := db.NewSessionStore(pool)
	allContainerIDs, reviewContainerIDs, err := sessions.ListContainerReferences(ctx)
	require.NoError(t, err, "GC should list container references in one scan")
	require.Equal(t, []string{containerID}, allContainerIDs, "inventory should include the retained workspace")
	require.Equal(t, []string{containerID}, reviewContainerIDs, "review-only inventory should include the retained workspace")
	finalized, err := sessions.FinalizeIdleCodeReviewContainer(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "finalizer should check active review holders")
	require.False(t, finalized, "active review holder must protect the workspace")
	_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context = jsonb_build_object('head_sha', 'different')
		WHERE id = $1 AND org_id = $2`, session.ID, orgID)
	require.NoError(t, err, "review session should advance to a different head")
	_, err = pool.Exec(ctx, `UPDATE session_sandbox_holders SET heartbeat_at = now() - interval '21 seconds'
		WHERE org_id = $1 AND session_id = $2 AND holder_kind = 'code_review' AND status = 'active'`, orgID, session.ID)
	require.NoError(t, err, "test holder heartbeat should permit another renewal attempt")
	renewed, err = holders.RenewCodeReview(ctx, orgID, session.ID, time.Minute)
	require.NoError(t, err, "mismatched review head should be rejected without a SQL error")
	require.False(t, renewed, "a holder for a superseded head must not renew")
	expired, err := holders.ExpireCodeReviewHolders(ctx, 100)
	require.NoError(t, err, "stale-head holder cleanup should execute")
	require.Equal(t, int64(1), expired, "stale-head holder should expire once")
	_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context = jsonb_build_object('head_sha', $1::text)
		WHERE id = $2 AND org_id = $3`, headSHA, session.ID, orgID)
	require.NoError(t, err, "test session should restore its expected head")
	_, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "recovery should create a new holder after expiry")
	require.True(t, acquired, "a fresh eligible turn should acquire a new fenced holder")
	setNodeStatus(t, pool, nodeID, "draining")
	drained, err := holders.ReleaseCodeReviewHoldersByOwner(ctx, nodeID)
	require.NoError(t, err, "owner drain should release review-only retention")
	require.Equal(t, int64(1), drained, "owner drain should release its active review holder")
	_, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "draining node should refuse holder acquisition without a SQL error")
	require.False(t, acquired, "in-flight turn must not rearm retention after owner drain")
	setNodeStatus(t, pool, nodeID, "active")
	_, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "healthy owner should acquire a new holder after drain release")
	require.True(t, acquired, "fresh eligible turn should regain holder ownership")
	synthesisThreadID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO code_review_agent_results (org_id, session_id, agent_provider, role, status, structured_result)
		VALUES ($1, $2, 'claude', 'orchestrator', 'running', jsonb_build_object('thread_id', $3::text))`,
		orgID, session.ID, synthesisThreadID.String())
	require.NoError(t, err, "synthesis result should identify its persisted thread")
	released, err := holders.ReleaseAfterSuccessfulSynthesis(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID, OwnerNodeID: nodeID,
	})
	require.NoError(t, err, "reviewer thread should not cause a synthesis release error")
	require.False(t, released, "reviewer thread must not release the active review holder")
	released, err = holders.ReleaseAfterSuccessfulSynthesis(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: synthesisThreadID, ContainerID: "wrong-container", OwnerNodeID: nodeID,
	})
	require.NoError(t, err, "stale container should not cause a synthesis release error")
	require.False(t, released, "stale synthesis must not release a successor's holder")
	var ambiguousResultID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO code_review_agent_results (org_id, session_id, agent_provider, role, status, structured_result)
		VALUES ($1, $2, 'claude', 'reviewer', 'running', jsonb_build_object('thread_id', $3::text))
		RETURNING id`, orgID, session.ID, synthesisThreadID.String()).Scan(&ambiguousResultID)
	require.NoError(t, err, "ambiguous role fixture should be inserted")
	released, err = holders.ReleaseAfterSuccessfulSynthesis(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: synthesisThreadID, ContainerID: containerID, OwnerNodeID: nodeID,
	})
	require.NoError(t, err, "ambiguous role should fail closed without an SQL error")
	require.False(t, released, "ambiguous reviewer and synthesis roles must not release the holder")
	_, err = pool.Exec(ctx, `DELETE FROM code_review_agent_results WHERE id = $1 AND org_id = $2`, ambiguousResultID, orgID)
	require.NoError(t, err, "ambiguous fixture should be removed before the matching synthesis")
	released, err = holders.ReleaseAfterSuccessfulSynthesis(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: synthesisThreadID, ContainerID: containerID, OwnerNodeID: nodeID,
	})
	require.NoError(t, err, "matching synthesis should release its workspace holder")
	require.True(t, released, "completed synthesis should permit immediate container cleanup")
	_, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: synthesisThreadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "synthesis should not fail holder acquisition")
	require.False(t, acquired, "synthesis must not rearm a released review holder")
	_, acquired, err = holders.AcquireCodeReview(ctx, orgID, db.AcquireCodeReviewSandboxHolderParams{
		SessionID: session.ID, ThreadID: threadID, ContainerID: containerID,
		OwnerNodeID: nodeID, LeaseToken: uuid.New(), LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "reviewer should be allowed to restore its holder for terminal cleanup coverage")
	require.True(t, acquired, "reviewer should regain durable retention while the review is active")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status = 'completed' WHERE id = $1 AND org_id = $2`, reviewID, orgID)
	require.NoError(t, err, "review should become terminal")
	released, err = holders.ReleaseTerminalCodeReview(ctx, orgID, session.ID)
	require.NoError(t, err, "terminal review holder release should execute")
	require.True(t, released, "terminal review should release its holder")
	var pendingJobID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO jobs (org_id, queue, job_type, payload)
		VALUES ($1, 'agents', 'continue_session', jsonb_build_object('session_id', $2::text))
		RETURNING id`, orgID, session.ID.String()).Scan(&pendingJobID)
	require.NoError(t, err, "pending executor job should be seeded")
	finalized, err = sessions.FinalizeIdleCodeReviewContainer(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "pending executor job should be checked before cleanup")
	require.False(t, finalized, "a queued turn must protect its review workspace")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded' WHERE id = $1 AND org_id = $2`, pendingJobID, orgID)
	require.NoError(t, err, "test executor job should become terminal")
	finalized, err = sessions.FinalizeIdleCodeReviewContainer(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "unheld review container should finalize")
	require.True(t, finalized, "terminal review should allow the idle container to be reclaimed")
	acquired, err = sessions.AcquireExistingTurnHold(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "delayed turn acquisition should be fenced without an SQL error")
	require.False(t, acquired, "a delayed turn must not republish a GC-cleared container")
	finalized, err = sessions.FinalizeIdleCodeReviewContainer(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "repeated finalization should be idempotent")
	require.False(t, finalized, "stale cleanup must not claim the same container twice")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status = 'running', container_id = 'successor-container',
		turn_holding_container = FALSE WHERE id = $1 AND org_id = $2`, session.ID, orgID)
	require.NoError(t, err, "test successor should replace the reclaimed workspace")
	reset, err := sessions.ResetAfterLostReuse(ctx, orgID, session.ID, containerID)
	require.NoError(t, err, "stale lane reset should check current container ownership")
	require.False(t, reset, "stale lane must not reset a successor's running session")
	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1 AND org_id = $2`, session.ID, orgID).Scan(&status)
	require.NoError(t, err, "successor session status should remain readable")
	require.Equal(t, "running", status, "successor turn should retain its running status")
}
