//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/automations"
)

// turnHarness extends the dispatch harness with the orchestrator turn
// path's store adapter and a reserved, attempt-claimed run.
type turnHarness struct {
	*dispatchHarness
	store     *automations.TurnStore
	results   *db.AutomationRunResultStore
	messages  *db.SessionMessageStore
	run       models.AutomationRun
	outcome   automations.DispatchOutcome
	lockToken uuid.UUID
	jobID     uuid.UUID
}

// newTurnHarness reserves a fresh first turn and claims its attempt under
// a running job lease, the state the orchestrator sees when a turn starts.
func newTurnHarness(t *testing.T) *turnHarness {
	t.Helper()
	h := newDispatchHarness(t)
	ctx := context.Background()
	messages := db.NewSessionMessageStore(h.pool)
	results := db.NewAutomationRunResultStore(h.pool)
	th := &turnHarness{
		dispatchHarness: h,
		store:           automations.NewTurnStore(h.pool, h.sessions, h.runs, h.targets, results, messages),
		results:         results,
		messages:        messages,
	}
	run := h.push(t, "1111111111111111111111111111111111111111", recentDeliveryTime())
	th.outcome = h.dispatch(t, run, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, th.outcome.Kind, "the first turn is reserved")
	th.lockToken = uuid.New()
	th.jobID = th.outcome.JobID
	_, err := h.pool.Exec(ctx, `UPDATE jobs SET status = 'running', lock_token = $2, lease_expires_at = now() + interval '5 minutes' WHERE id = $1`, th.jobID, th.lockToken)
	require.NoError(t, err, "lease the job")
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin claim")
	_, owned, err := h.runs.ClaimAttempt(ctx, tx, h.orgID, run.ID, th.jobID, th.lockToken)
	require.NoError(t, err, "claim attempt")
	require.NoError(t, tx.Commit(ctx), "commit claim")
	require.True(t, owned, "the lease holder owns the attempt")
	th.run = h.reload(t, run.ID)
	return th
}

func (h *turnHarness) session(t *testing.T) models.Session {
	t.Helper()
	session, err := h.sessions.GetByID(context.Background(), h.orgID, h.outcome.SessionID)
	require.NoError(t, err, "reload session")
	return session
}

func (h *turnHarness) generation(t *testing.T) models.AutomationTargetSession {
	t.Helper()
	return h.activeGeneration(t, *h.run.TargetID)
}

// TestAutomationTurn_ProvenanceWriterAndGuards proves an owned session's
// snapshot key moves only through PublishCheckpointWithProvenance, together
// with the generation's provenance, and that every other key writer leaves
// an owned session's key alone.
func TestAutomationTurn_ProvenanceWriterAndGuards(t *testing.T) {
	h := newTurnHarness(t)
	ctx := context.Background()
	sessionID := h.outcome.SessionID
	generation := h.generation(t)
	require.Equal(t, &generation.ID, sessionOwnerMarker(t, h.pool, h.orgID, sessionID), "the session is automation-owned")
	fp := "v1:abc"
	head := "1111111111111111111111111111111111111111"

	published, err := h.sessions.PublishCheckpoint(ctx, h.orgID, sessionID, h.lockToken, "agent-1", "snapshots/plain", models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 10, time.Now(), nil, models.RuntimeStopReasonNone)
	require.NoError(t, err, "plain publish should not error")
	require.False(t, published, "PublishCheckpoint never installs a key on an owned session")

	_, err = h.sessions.PublishCheckpointWithProvenance(ctx, h.orgID, sessionID, h.lockToken, "agent-1", "", models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 10, time.Now(), models.RuntimeStopReasonNone, models.CheckpointProvenance{GenerationID: generation.ID})
	require.Error(t, err, "provenance requires a key")

	published, err = h.sessions.PublishCheckpointWithProvenance(ctx, h.orgID, sessionID, h.lockToken, "agent-1", "snapshots/other-gen", models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 10, time.Now(), models.RuntimeStopReasonNone, models.CheckpointProvenance{GenerationID: uuid.New(), HeadSHA: head})
	require.NoError(t, err, "wrong generation should not error")
	require.False(t, published, "a generation that does not own the session cannot publish")

	published, err = h.sessions.PublishCheckpointWithProvenance(ctx, h.orgID, sessionID, uuid.New(), "agent-1", "snapshots/other-lease", models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 10, time.Now(), models.RuntimeStopReasonNone, models.CheckpointProvenance{GenerationID: generation.ID, HeadSHA: head})
	require.NoError(t, err, "wrong lease should not error")
	require.False(t, published, "the job fence rejects another lease")

	published, err = h.sessions.PublishCheckpointWithProvenance(ctx, h.orgID, sessionID, h.lockToken, "agent-1", "snapshots/turn1", models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 10, time.Now(), models.RuntimeStopReasonNone, models.CheckpointProvenance{GenerationID: generation.ID, HeadSHA: head, DependencyFingerprint: &fp, ReviewComplete: true})
	require.NoError(t, err, "publish with provenance")
	require.True(t, published, "the owning generation publishes under its lease")
	session := h.session(t)
	require.Equal(t, "snapshots/turn1", *session.SnapshotKey, "the key is installed on the session")
	require.Equal(t, models.CheckpointKindTurnComplete, session.CheckpointKind, "checkpoint kind is recorded")
	require.Equal(t, "agent-1", *session.AgentSessionID, "the native agent session id is recorded")
	generation = h.generation(t)
	require.Equal(t, "snapshots/turn1", *generation.CheckpointSnapshotKey, "provenance names the installed key")
	require.Equal(t, head, *generation.CheckpointHeadSHA, "provenance records the head")
	require.Equal(t, fp, *generation.CheckpointDependencyFingerprint, "provenance records the fingerprint")
	require.True(t, *generation.CheckpointReviewComplete, "a completed turn's checkpoint is review-complete")

	published, err = h.sessions.PublishCheckpointWithProvenance(ctx, h.orgID, sessionID, h.lockToken, "agent-1", "snapshots/stop", models.CheckpointKindGracefulStop, models.CheckpointCapabilityFullResume, 10, time.Now(), models.RuntimeStopReasonUserCancel, models.CheckpointProvenance{GenerationID: generation.ID, HeadSHA: head, ReviewComplete: true})
	require.NoError(t, err, "graceful stop publish")
	require.True(t, published, "graceful stop publishes")
	generation = h.generation(t)
	require.False(t, *generation.CheckpointReviewComplete, "an interrupted checkpoint is never review-complete, whatever the caller says")
	require.Equal(t, "snapshots/stop", *generation.CheckpointSnapshotKey, "provenance follows the checkpoint")

	// Every other key writer leaves the owned session's key alone.
	require.NoError(t, h.sessions.UpdateTurnComplete(ctx, h.orgID, sessionID, 1, nil, "agent-1", "snapshots/bypass"), "turn complete")
	require.Equal(t, "snapshots/stop", *h.session(t).SnapshotKey, "UpdateTurnComplete does not replace an owned session's key")
	require.NoError(t, h.sessions.UpdateWorkspaceSnapshot(ctx, h.orgID, sessionID, "snapshots/bypass", nil), "workspace snapshot")
	require.Equal(t, "snapshots/stop", *h.session(t).SnapshotKey, "UpdateWorkspaceSnapshot does not replace an owned session's key")
	require.NoError(t, h.sessions.UpdateSnapshotInfo(ctx, h.orgID, sessionID, "agent-2", "snapshots/bypass"), "snapshot info")
	require.Equal(t, "snapshots/stop", *h.session(t).SnapshotKey, "UpdateSnapshotInfo does not replace an owned session's key")

	// A keyless publication (a recorded snapshot failure) still lands.
	errText := "upload failed"
	published, err = h.sessions.PublishCheckpoint(ctx, h.orgID, sessionID, h.lockToken, "", "", models.CheckpointKindGracefulStop, models.CheckpointCapabilityFullResume, 0, time.Now(), &errText, models.RuntimeStopReasonNone)
	require.NoError(t, err, "keyless publish")
	require.True(t, published, "an owned session records a checkpoint error without a key")
	session = h.session(t)
	require.Equal(t, "snapshots/stop", *session.SnapshotKey, "the key is untouched")
	require.Equal(t, errText, *session.CheckpointError, "the error is recorded")

	// Promotion of a pending key installs it and clears the provenance.
	require.NoError(t, h.sessions.SetPendingSnapshotKey(ctx, h.orgID, sessionID, "snapshots/pending"), "set pending")
	require.NoError(t, h.sessions.PromotePendingSnapshot(ctx, h.orgID, sessionID, "snapshots/pending"), "promote")
	require.Equal(t, "snapshots/pending", *h.session(t).SnapshotKey, "promotion installs the pending key")
	generation = h.generation(t)
	require.Nil(t, generation.CheckpointSnapshotKey, "promotion without captured provenance clears the generation's provenance")
	require.Nil(t, generation.CheckpointReviewComplete, "including the review flag")
}

// TestAutomationTurn_EndAttemptMarker proves the attempt end commits the
// session status write and the result marker together, and that a lost
// lease rolls both back.
func TestAutomationTurn_EndAttemptMarker(t *testing.T) {
	h := newTurnHarness(t)
	ctx := context.Background()
	sessionID := h.outcome.SessionID
	require.NotEqual(t, models.SessionStatusIdle, h.session(t).Status, "a fresh generation's session is pending until the agent starts")

	marker := &models.AutomationRunResult{
		RunID: h.run.ID, OrgID: h.orgID, Attempt: h.run.Attempt, AttemptLockToken: h.lockToken,
		ThreadID: *h.run.ThreadID, TurnNumber: *h.run.TurnNumber, Outcome: models.AutomationRunResultTurnCompleted, ReviewComplete: true, NativeContext: true,
	}
	err := h.store.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions agent.SessionStore) error {
		if err := sessions.UpdateTurnComplete(ctx, h.orgID, sessionID, 1, nil, "agent-1", ""); err != nil {
			return err
		}
		written, err := h.store.WriteResult(ctx, tx, h.orgID, h.jobID, marker)
		if err != nil {
			return err
		}
		require.True(t, written, "the lease holder writes the marker")
		_, err = h.store.RecordTurnDuration(ctx, tx, h.orgID, h.run.ID, h.lockToken, 1234)
		return err
	})
	require.NoError(t, err, "attempt end commits")
	session := h.session(t)
	require.Equal(t, models.SessionStatusIdle, session.Status, "the session is idle")
	require.Equal(t, 1, session.CurrentTurn, "the turn counted")
	stored, err := h.results.GetByRun(ctx, h.orgID, h.run.ID)
	require.NoError(t, err, "marker exists")
	require.Equal(t, models.AutomationRunResultTurnCompleted, stored.Outcome, "marker outcome")
	require.True(t, stored.ReviewComplete, "marker review flag")
	run := h.reload(t, h.run.ID)
	require.Equal(t, 1234, *run.TurnDurationMS, "turn duration recorded in the same transaction")

	// A second attempt end with a lost lease rolls everything back.
	_, err = h.pool.Exec(ctx, `UPDATE sessions SET status = 'running' WHERE id = $1`, sessionID)
	require.NoError(t, err, "put the session back to running")
	stale := *marker
	stale.Attempt = h.run.Attempt + 1
	stale.AttemptLockToken = uuid.New()
	stale.Outcome = models.AutomationRunResultAgentFailed
	err = h.store.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions agent.SessionStore) error {
		if err := sessions.UpdateResult(ctx, h.orgID, sessionID, models.SessionStatusFailed, &models.SessionResult{}); err != nil {
			return err
		}
		written, err := h.store.WriteResult(ctx, tx, h.orgID, h.jobID, &stale)
		if err != nil {
			return err
		}
		if !written {
			return agent.ErrAutomationAttemptLost
		}
		return nil
	})
	require.ErrorIs(t, err, agent.ErrAutomationAttemptLost, "a rejected marker fails the attempt end")
	require.Equal(t, models.SessionStatusRunning, h.session(t).Status, "the status write rolled back with the marker")
	stored, err = h.results.GetByRun(ctx, h.orgID, h.run.ID)
	require.NoError(t, err, "marker still exists")
	require.Equal(t, models.AutomationRunResultTurnCompleted, stored.Outcome, "the earlier marker stands")
}

// TestAutomationTurn_CompletePreflight proves a reserved run that could not
// start ends without a marker: terminal status, dispatch done, session and
// thread released without counting a turn, reservation message deleted,
// wake requested, pending ownership release applied; and that the fence
// rejects another lease.
func TestAutomationTurn_CompletePreflight(t *testing.T) {
	h := newTurnHarness(t)
	ctx := context.Background()
	sessionID := h.outcome.SessionID
	var messages int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM session_messages WHERE org_id = $1 AND automation_run_id = $2`, h.orgID, h.run.ID).Scan(&messages), "count messages")
	require.Equal(t, 1, messages, "the reservation inserted the turn's user message")

	done, err := h.store.CompletePreflight(ctx, h.orgID, h.run.ID, uuid.New(), models.AutomationRunOutcomeStaleHead, "stale")
	require.NoError(t, err, "wrong lease should not error")
	require.False(t, done, "the fence rejects another lease")
	require.Equal(t, models.AutomationRunStatusRunning, h.reload(t, h.run.ID).Status, "the run is untouched")

	_, err = h.pool.Exec(ctx, `UPDATE automation_target_sessions SET ownership_release_pending = true WHERE id = $1`, h.generation(t).ID)
	require.NoError(t, err, "mark the release pending, as a retirement during execution would")
	done, err = h.store.CompletePreflight(ctx, h.orgID, h.run.ID, h.lockToken, models.AutomationRunOutcomeStaleHead, "the pull request head is no longer reachable")
	require.NoError(t, err, "preflight completes")
	require.True(t, done, "the lease holder completes the preflight")
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunStatusSkipped, run.Status, "stale_head skips the run")
	require.Equal(t, models.AutomationRunDispatchDone, *run.DispatchState, "dispatch is done")
	require.Equal(t, models.AutomationRunOutcomeStaleHead, *run.OutcomeReason, "outcome recorded")
	session := h.session(t)
	require.Equal(t, models.SessionStatusIdle, session.Status, "the session is released")
	require.Equal(t, 0, session.CurrentTurn, "no turn was counted")
	var threadStatus string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT status FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&threadStatus), "thread status")
	require.Equal(t, "idle", threadStatus, "the primary thread is released")
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM session_messages WHERE org_id = $1 AND automation_run_id = $2`, h.orgID, h.run.ID).Scan(&messages), "count messages")
	require.Equal(t, 0, messages, "the reservation message is deleted because no assistant turn happened")
	target, err := h.targets.GetByID(ctx, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "reload target")
	require.NotNil(t, target.WakeRequestedAt, "a wake is requested for the next waiter")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, sessionID), "the pending ownership release cleared the marker")
	_, err = h.results.GetByRun(ctx, h.orgID, h.run.ID)
	require.Error(t, err, "no marker is written for a preflight outcome")
}

// TestAutomationTurn_WorkspaceAndPromptBookkeeping proves the fenced
// workspace record, the prompt rewrite of the reservation message, the
// assistant message attribution, and the ordered summaries of completed
// turns.
func TestAutomationTurn_WorkspaceAndPromptBookkeeping(t *testing.T) {
	h := newTurnHarness(t)
	ctx := context.Background()
	sessionID := h.outcome.SessionID
	base := "0000000000000000000000000000000000000000"
	bytes := int64(4096)
	ms := 250

	recorded, err := h.store.RecordTurnWorkspace(ctx, h.orgID, h.run.ID, uuid.New(), models.AutomationTurnWorkspace{BaseSHA: base})
	require.NoError(t, err, "wrong lease should not error")
	require.False(t, recorded, "the fence rejects another lease")
	recorded, err = h.store.RecordTurnWorkspace(ctx, h.orgID, h.run.ID, h.lockToken, models.AutomationTurnWorkspace{BaseSHA: base, WorkerNodeID: "node-a", RestoreSnapshotBytes: &bytes, RestoreDurationMS: &ms})
	require.NoError(t, err, "record workspace")
	require.True(t, recorded, "the lease holder records the workspace")
	run := h.reload(t, h.run.ID)
	require.Equal(t, base, *run.BaseSHA, "base sha recorded")
	require.Equal(t, "node-a", *run.WorkerNodeID, "worker node recorded")
	require.Equal(t, bytes, *run.RestoreSnapshotBytes, "restore bytes recorded")
	require.Equal(t, ms, *run.RestoreDurationMS, "restore duration recorded")

	updated, err := h.store.UpdateTurnPrompt(ctx, h.orgID, h.run.ID, "rendered prompt")
	require.NoError(t, err, "update prompt")
	require.True(t, updated, "the reservation message is rewritten")
	var content string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT content FROM session_messages WHERE org_id = $1 AND automation_run_id = $2 AND role = 'user'`, h.orgID, h.run.ID).Scan(&content), "read prompt")
	require.Equal(t, "rendered prompt", content, "the transcript shows the rendered prompt")

	assistant := &models.SessionMessage{SessionID: sessionID, OrgID: h.orgID, ThreadID: h.run.ThreadID, TurnNumber: *h.run.TurnNumber, Role: models.MessageRoleAssistant, Content: "Looks good."}
	require.NoError(t, h.messages.Create(ctx, assistant), "create assistant message")
	require.NoError(t, h.store.TagAssistantMessage(ctx, h.orgID, sessionID, *h.run.ThreadID, *h.run.TurnNumber, h.run.ID), "tag assistant message")
	var tagged uuid.UUID
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT automation_run_id FROM session_messages WHERE id = $1`, assistant.ID).Scan(&tagged), "read tag")
	require.Equal(t, h.run.ID, tagged, "the assistant message is attributed to the run")

	// Two completed turns, then a third run: summaries come back oldest
	// first with their heads.
	h.finishTurn(t, h.run, "1111111111111111111111111111111111111111")
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET result_summary = 'first review' WHERE id = $1`, h.run.ID)
	require.NoError(t, err, "summarize the first turn")
	run2 := h.push(t, "2222222222222222222222222222222222222222", recentDeliveryTime().Add(time.Second))
	second := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, second.Kind, "second turn reserved")
	h.finishTurn(t, run2, "2222222222222222222222222222222222222222")
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET result_summary = 'second review', completed_at = now() + interval '1 second' WHERE id = $1`, run2.ID)
	require.NoError(t, err, "summarize the second turn")
	summaries, err := h.store.ListCompletedTurnSummaries(ctx, h.orgID, *h.run.TargetID, 1, 5)
	require.NoError(t, err, "list summaries")
	require.Len(t, summaries, 2, "both completed turns are listed")
	require.Equal(t, "first review", summaries[0].Summary, "oldest first")
	require.Equal(t, "1111111111111111111111111111111111111111", summaries[0].HeadSHA, "the first turn's head")
	require.Equal(t, "second review", summaries[1].Summary, "newest last")
	require.Equal(t, 2, summaries[1].TurnNumber, "turn numbers are carried")
	require.Equal(t, "2222222222222222222222222222222222222222", summaries[1].HeadSHA, "the second turn's resolved head")
}

// TestAutomationTurn_AttemptFenceHelpers proves the marker-free fences the
// drain and restore-fallback paths rely on.
func TestAutomationTurn_AttemptFenceHelpers(t *testing.T) {
	h := newTurnHarness(t)
	ctx := context.Background()

	owned, err := h.store.AttemptOwned(ctx, nil, h.orgID, h.run.ID, h.lockToken)
	require.NoError(t, err, "ownership check")
	require.True(t, owned, "the lease holder owns the attempt")
	owned, err = h.store.AttemptOwned(ctx, nil, h.orgID, h.run.ID, uuid.New())
	require.NoError(t, err, "ownership check with another token")
	require.False(t, owned, "another token does not own the attempt")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'pending', lock_token = NULL WHERE id = $1`, h.jobID)
	require.NoError(t, err, "reclaim the job")
	owned, err = h.store.AttemptOwned(ctx, nil, h.orgID, h.run.ID, h.lockToken)
	require.NoError(t, err, "ownership check after reclaim")
	require.False(t, owned, "a reclaimed job no longer owns the attempt")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'running', lock_token = $2 WHERE id = $1`, h.jobID, h.lockToken)
	require.NoError(t, err, "restore the lease")

	recorded, err := h.store.RecordContinuationFallback(ctx, h.orgID, h.run.ID, uuid.New(), models.AutomationRunContinuationReasonRestoreFailed)
	require.NoError(t, err, "fallback with another token")
	require.False(t, recorded, "the fence rejects another lease")
	recorded, err = h.store.RecordContinuationFallback(ctx, h.orgID, h.run.ID, h.lockToken, models.AutomationRunContinuationReasonRestoreFailed)
	require.NoError(t, err, "fallback")
	require.True(t, recorded, "the lease holder records the fallback")
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunContinuationReconstructed, *run.ContinuationMode, "the run is reconstructed")
	require.Equal(t, models.AutomationRunContinuationReasonRestoreFailed, *run.ContinuationReason, "with restore_failed")
}
