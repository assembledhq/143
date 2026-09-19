//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/automations"
)

// dispatchHarness wires the real trigger service and dispatcher over the
// integration database so arrival and the ownership transaction run the
// production code paths end to end.
type dispatchHarness struct {
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	repoID     uuid.UUID
	automation models.Automation
	runs       *db.AutomationRunStore
	targets    *db.AutomationTargetStore
	sessions   *db.SessionStore
	jobs       *db.JobStore
	trigger    *automations.GitHubEventTriggerService
	dispatcher *automations.TargetDispatcher
}

func newDispatchHarness(t *testing.T) *dispatchHarness {
	t.Helper()
	pool := setup(t)
	orgID := seedOrg(t, pool)
	repoID := seedRepository(t, pool, orgID, "acme/web")
	automation := seedAutomation(t, pool, orgID, repoID)
	automationStore := db.NewAutomationStore(pool)
	runs := db.NewAutomationRunStore(pool)
	targets := db.NewAutomationTargetStore(pool)
	sessions := db.NewSessionStore(pool)
	threads := db.NewSessionThreadStore(pool)
	jobs := db.NewJobStore(pool)
	trigger := automations.NewGitHubEventTriggerService(automationStore, runs, jobs, pool, zerolog.Nop())
	trigger.SetTargetStores(targets, runs)
	dispatcher := automations.NewTargetDispatcher(pool, automationStore, targets, runs, sessions, threads, jobs, zerolog.Nop())
	return &dispatchHarness{
		pool: pool, orgID: orgID, repoID: repoID, automation: automation,
		runs: runs, targets: targets, sessions: sessions, jobs: jobs, trigger: trigger, dispatcher: dispatcher,
	}
}

// push delivers a synchronize event for PR 42 at head with the given
// updated_at and returns the created run.
func (h *dispatchHarness) push(t *testing.T, head string, updatedAt time.Time) models.AutomationRun {
	t.Helper()
	return h.deliver(t, automations.GitHubEventTriggerRequest{
		Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
		HeadSHA: head, PullRequestUpdatedAt: &updatedAt, BaseBranch: "main",
	})
}

func (h *dispatchHarness) deliver(t *testing.T, req automations.GitHubEventTriggerRequest) models.AutomationRun {
	t.Helper()
	req.OrgID = h.orgID
	req.RepositoryID = h.repoID
	req.Repository = "acme/web"
	req.PullRequestNumber = 42
	req.ProviderEventID = "delivery-" + uuid.NewString()
	before := h.runIDs(t)
	require.NoError(t, h.trigger.TriggerGitHubEvent(context.Background(), req), "trigger should succeed")
	after := h.runIDs(t)
	require.Len(t, after, len(before)+1, "delivery creates exactly one run")
	run, err := h.runs.GetByRunID(context.Background(), h.orgID, after[len(after)-1])
	require.NoError(t, err, "load created run")
	return run
}

func (h *dispatchHarness) runIDs(t *testing.T) []uuid.UUID {
	t.Helper()
	rows, err := h.pool.Query(context.Background(), `
		SELECT id FROM automation_runs WHERE org_id = $1 AND automation_id = $2 ORDER BY created_at, id`, h.orgID, h.automation.ID)
	require.NoError(t, err, "list runs")
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id), "scan run id")
		ids = append(ids, id)
	}
	return ids
}

func (h *dispatchHarness) reload(t *testing.T, runID uuid.UUID) models.AutomationRun {
	t.Helper()
	run, err := h.runs.GetByRunID(context.Background(), h.orgID, runID)
	require.NoError(t, err, "reload run")
	return run
}

func (h *dispatchHarness) template(agent models.AgentType) *models.Session {
	brief := "review the pull request"
	repoID := h.repoID
	return &models.Session{
		AgentType: agent, Status: models.SessionStatusPending, AutonomyLevel: models.DefaultSessionAutonomy,
		TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginAutomation,
		RepositoryID: &repoID, ExecutionBrief: &brief,
	}
}

func (h *dispatchHarness) dispatch(t *testing.T, run models.AutomationRun, agent models.AgentType) automations.DispatchOutcome {
	t.Helper()
	outcome, err := h.dispatcher.Dispatch(context.Background(), automations.DispatchInput{
		Run: run, Automation: h.automation, SessionTemplate: h.template(agent),
	})
	require.NoError(t, err, "dispatch should not error")
	return outcome
}

// finishTurn simulates the orchestrator and completer for an executing run:
// the run is done, the session is idle with a coherent checkpoint, and the
// primary thread is idle.
func (h *dispatchHarness) finishTurn(t *testing.T, run models.AutomationRun, head string) {
	t.Helper()
	ctx := context.Background()
	run = h.reload(t, run.ID)
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET dispatch_state = 'done', status = 'completed', outcome_reason = 'turn_completed', completed_at = now() WHERE id = $1`, run.ID)
	require.NoError(t, err, "finish run")
	_, err = h.pool.Exec(ctx, `
		UPDATE sessions SET status = 'idle', snapshot_key = $2, sandbox_state = 'snapshotted', checkpoint_kind = 'turn_complete',
			checkpointed_at = now(), current_turn = current_turn + 1, agent_session_id = 'agent-session'
		WHERE id = $1`, *run.SessionID, "snapshots/"+run.ID.String())
	require.NoError(t, err, "idle the session with a checkpoint")
	_, err = h.pool.Exec(ctx, `UPDATE session_threads SET status = 'idle' WHERE session_id = $1`, *run.SessionID)
	require.NoError(t, err, "idle the thread")
	_, err = h.pool.Exec(ctx, `
		UPDATE automation_target_sessions SET turn_count = turn_count + 1, last_reviewed_head_sha = $2, last_reviewed_epoch = COALESCE($3, last_reviewed_epoch),
			checkpoint_snapshot_key = $4, checkpoint_head_sha = $2, checkpoint_review_complete = true, last_run_id = $5, last_turn_at = now()
		WHERE id = (SELECT id FROM automation_target_sessions WHERE org_id = $1 AND session_id = $6 AND status = 'active')`,
		h.orgID, head, run.HeadEpoch, "snapshots/"+run.ID.String(), run.ID, *run.SessionID)
	require.NoError(t, err, "record the completed review on the generation")
}

func (h *dispatchHarness) activeGeneration(t *testing.T, targetID uuid.UUID) models.AutomationTargetSession {
	t.Helper()
	generation, err := h.targets.GetActiveGeneration(context.Background(), nil, h.orgID, targetID)
	require.NoError(t, err, "active generation")
	return generation
}

func (h *dispatchHarness) jobPayload(t *testing.T, jobID uuid.UUID) map[string]string {
	t.Helper()
	var raw json.RawMessage
	var jobType, dedupeKey string
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT job_type, COALESCE(dedupe_key, ''), payload FROM jobs WHERE id = $1`, jobID).Scan(&jobType, &dedupeKey, &raw), "load job")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "decode payload")
	payload := map[string]string{"__job_type": jobType, "__dedupe_key": dedupeKey}
	for key, value := range decoded {
		payload[key] = fmt.Sprint(value)
	}
	return payload
}

// TestAutomationDispatch_FreshThenContinue walks one pull request through
// its first turn, a push that waits behind it, and the continued turn that
// follows, checking the reservation, the session ownership, the enqueued
// jobs, the visible message, the attempt claim, and the fences.
func TestAutomationDispatch_FreshThenContinue(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	h1 := "1111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222"

	run1 := h.push(t, h1, t0)
	require.NotNil(t, run1.TargetID, "arrival attaches the run to its target")
	require.Equal(t, models.AutomationRunHeadAuthoritative, *run1.HeadResolution, "first push is authoritative")
	require.Equal(t, 1, *run1.HeadEpoch, "first push opens epoch 1")

	first := h.dispatch(t, run1, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, first.Kind, "first run is reserved")
	require.Equal(t, models.AutomationRunContinuationFresh, first.ContinuationMode, "first turn is fresh")
	require.Equal(t, models.AutomationRunContinuationReasonNoGeneration, *first.ContinuationReason, "fresh because no generation existed")

	run1 = h.reload(t, run1.ID)
	require.Equal(t, models.AutomationRunStatusRunning, run1.Status, "reserved run is running")
	require.Equal(t, models.AutomationRunDispatchExecuting, *run1.DispatchState, "reserved run is executing")
	require.Equal(t, first.SessionID, *run1.SessionID, "run records its session")
	require.Equal(t, first.JobID, *run1.JobID, "run records its job")
	require.Equal(t, 1, *run1.TargetGeneration, "run executes on generation 1")
	require.True(t, run1.HeadLookupDegraded, "without a head resolver the push reviews its delivered head in degraded mode")
	generation := h.activeGeneration(t, *run1.TargetID)
	require.Equal(t, first.SessionID, generation.SessionID, "generation 1 owns the new session")
	require.Equal(t, &generation.ID, sessionOwnerMarker(t, h.pool, h.orgID, first.SessionID), "session is automation-owned")
	payload := h.jobPayload(t, first.JobID)
	require.Equal(t, "run_agent", payload["__job_type"], "fresh turn runs run_agent")
	require.Equal(t, automations.AutomationTurnDedupeKey(run1.ID), payload["__dedupe_key"], "job is run-scoped")
	require.Equal(t, first.SessionID.String(), payload["session_id"], "job targets the new session")

	again := h.dispatch(t, run1, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchAlreadyReserved, again.Kind, "a retry recognizes its own reservation")

	run2 := h.push(t, h2, t0.Add(time.Second))
	require.Equal(t, 2, *run2.HeadEpoch, "second push opens epoch 2")
	waiting := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchWaiting, waiting.Kind, "a push behind an executing turn waits")
	run2 = h.reload(t, run2.ID)
	require.Equal(t, models.AutomationRunDispatchWaiting, *run2.DispatchState, "waiting run records its state")
	require.Equal(t, models.AutomationRunWaitTargetBusy, *run2.WaitReason, "waiting run records why")
	require.NotNil(t, run2.WaitStartedAt, "waiting run records when")

	// The executing run cannot be terminalized by the unstarted path.
	terminalized, err := h.runs.TerminalizeUnstarted(ctx, nil, h.orgID, run1.ID, models.AutomationRunOutcomeWaitTimeout, nil, "")
	require.NoError(t, err, "terminalize should not error")
	require.False(t, terminalized, "TerminalizeUnstarted never touches an executing run")

	// Attempt claim against the real job row.
	lockToken := uuid.New()
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'running', lock_token = $2, lease_expires_at = now() + interval '5 minutes' WHERE id = $1`, first.JobID, lockToken)
	require.NoError(t, err, "lease the job")
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin claim")
	attempt, owned, err := h.runs.ClaimAttempt(ctx, tx, h.orgID, run1.ID, first.JobID, lockToken)
	require.NoError(t, err, "claim attempt")
	require.NoError(t, tx.Commit(ctx), "commit claim")
	require.True(t, owned, "the lease holder claims the attempt")
	require.Equal(t, 1, attempt, "first attempt")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'pending', lock_token = NULL WHERE id = $1`, first.JobID)
	require.NoError(t, err, "reclaim the job")
	tx, err = h.pool.Begin(ctx)
	require.NoError(t, err, "begin stale claim")
	_, owned, err = h.runs.ClaimAttempt(ctx, tx, h.orgID, run1.ID, first.JobID, lockToken)
	require.NoError(t, err, "stale claim should not error")
	require.NoError(t, tx.Rollback(ctx), "rollback stale claim")
	require.False(t, owned, "a reclaimed job's token cannot claim an attempt")

	// The turn finishes; the waiting push continues the same session.
	h.finishTurn(t, run1, h1)
	continued := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, continued.Kind, "the waiting push is reserved once the target frees")
	require.Equal(t, models.AutomationRunContinuationContinued, continued.ContinuationMode, "a coherent checkpoint continues")
	require.Equal(t, first.SessionID, continued.SessionID, "the same session continues")
	run2 = h.reload(t, run2.ID)
	require.Equal(t, h1, *run2.PreviousHeadSHA, "the delta baseline is the checkpoint head")
	require.Equal(t, 1, *run2.TargetGeneration, "the continued turn runs on generation 1")
	require.Nil(t, run2.WaitReason, "reservation clears the wait reason")
	payload = h.jobPayload(t, continued.JobID)
	require.Equal(t, "continue_session", payload["__job_type"], "continued turn runs continue_session")
	require.Equal(t, run2.ID.String(), payload["automation_run_id"], "payload carries the run")
	require.Equal(t, "1", payload["target_generation"], "payload carries the generation")
	require.Equal(t, h2, payload["head_sha"], "payload carries the head to review")
	require.NotEmpty(t, payload["structured_prompt"], "payload carries the turn prompt")
	var messageCount int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM session_messages WHERE org_id = $1 AND session_id = $2 AND role = 'user' AND source = 'automation_turn'`, h.orgID, first.SessionID).Scan(&messageCount), "count messages")
	require.Equal(t, 2, messageCount, "each turn inserts its visible user message on the session")
	session, err := h.sessions.GetByID(ctx, h.orgID, first.SessionID)
	require.NoError(t, err, "load session")
	require.Equal(t, models.SessionStatusRunning, session.Status, "the continued session is claimed running")
}

// TestAutomationDispatch_ConcurrentReservation proves the target lock
// serializes dispatch: two workers dispatching the same run produce one
// reservation and one recognition, and two different runs produce one
// reservation and one waiter.
func TestAutomationDispatch_ConcurrentReservation(t *testing.T) {
	h := newDispatchHarness(t)
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	head := "3333333333333333333333333333333333333333"
	run := h.push(t, head, t0)
	// The seeded automation subscribes to pull_request.updated only, so the
	// second run is a non-push edit at the same head.
	edited := t0.Add(time.Minute)
	comment := h.deliver(t, automations.GitHubEventTriggerRequest{
		Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "edited",
		HeadSHA: head, PullRequestUpdatedAt: &edited, Body: "description updated",
	})

	dispatchBoth := func(a, b models.AutomationRun) (automations.DispatchOutcome, automations.DispatchOutcome) {
		var wg sync.WaitGroup
		var first, second automations.DispatchOutcome
		wg.Add(2)
		go func() { defer wg.Done(); first = h.dispatch(t, a, models.AgentTypeCodex) }()
		go func() { defer wg.Done(); second = h.dispatch(t, b, models.AgentTypeCodex) }()
		wg.Wait()
		return first, second
	}

	first, second := dispatchBoth(run, run)
	kinds := map[automations.DispatchKind]int{first.Kind: 1}
	kinds[second.Kind]++
	require.Equal(t, 1, kinds[automations.DispatchReserved], "exactly one worker reserves the run")
	require.Equal(t, 1, kinds[automations.DispatchAlreadyReserved], "the other worker recognizes the reservation")

	var executing int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM automation_runs WHERE org_id = $1 AND dispatch_state = 'executing'`, h.orgID).Scan(&executing), "count executing")
	require.Equal(t, 1, executing, "one executing run")

	third := h.dispatch(t, comment, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchWaiting, third.Kind, "a second run waits behind the executing one")
	var sessions int
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE org_id = $1`, h.orgID).Scan(&sessions), "count sessions")
	require.Equal(t, 1, sessions, "only one session exists for the target")
}

// TestAutomationDispatch_ConflictLookup proves the run-scoped dedupe key:
// a job holding the key for a different run fails the transaction and
// leaves nothing behind, while a job holding it for this run is reused.
func TestAutomationDispatch_ConflictLookup(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	run := h.push(t, "4444444444444444444444444444444444444444", t0)
	dedupeKey := automations.AutomationTurnDedupeKey(run.ID)

	foreign, err := h.jobs.Enqueue(ctx, h.orgID, automations.AutomationTurnJobQueue, "continue_session", map[string]string{"automation_run_id": uuid.NewString()}, 5, &dedupeKey)
	require.NoError(t, err, "enqueue a foreign job on the key")
	require.NotEqual(t, uuid.Nil, foreign, "foreign job is created")
	_, err = h.dispatcher.Dispatch(ctx, automations.DispatchInput{Run: run, Automation: h.automation, SessionTemplate: h.template(models.AgentTypeCodex)})
	require.Error(t, err, "a key held for a different run fails the ownership transaction")
	run = h.reload(t, run.ID)
	require.Equal(t, models.AutomationRunStatusPending, run.Status, "the run stays pending after rollback")
	require.Nil(t, run.DispatchState, "nothing was reserved")
	var sessions int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE org_id = $1`, h.orgID).Scan(&sessions), "count sessions")
	require.Equal(t, 0, sessions, "the rolled-back transaction created no session")

	_, err = h.pool.Exec(ctx, `UPDATE jobs SET payload = $2 WHERE id = $1`, foreign, map[string]string{"automation_run_id": run.ID.String()})
	require.NoError(t, err, "make the job carry this run")
	outcome := h.dispatch(t, run, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, outcome.Kind, "a key held for this run is accepted")
	require.Equal(t, foreign, outcome.JobID, "the existing job is reused")
}

// TestAutomationDispatch_AgentConfigChangeGoesFresh proves an incompatible
// generation is retired inside the ownership transaction and the run starts
// a fresh generation with the matching continuation reason.
func TestAutomationDispatch_AgentConfigChangeGoesFresh(t *testing.T) {
	h := newDispatchHarness(t)
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	h1 := "5555555555555555555555555555555555555555"
	h2 := "6666666666666666666666666666666666666666"
	run1 := h.push(t, h1, t0)
	first := h.dispatch(t, run1, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, first.Kind, "first run is reserved")
	h.finishTurn(t, run1, h1)

	run2 := h.push(t, h2, t0.Add(time.Second))
	second := h.dispatch(t, run2, models.AgentTypeClaudeCode)
	require.Equal(t, automations.DispatchReserved, second.Kind, "second run is reserved")
	require.Equal(t, models.AutomationRunContinuationFresh, second.ContinuationMode, "agent change starts fresh")
	require.Equal(t, models.AutomationRunContinuationReasonAgentConfigChanged, *second.ContinuationReason, "the run records why")
	require.NotEqual(t, first.SessionID, second.SessionID, "a new session is created")

	generation := h.activeGeneration(t, *run2.TargetID)
	require.Equal(t, 2, generation.Generation, "generation 2 is active")
	require.Equal(t, second.SessionID, generation.SessionID, "generation 2 owns the new session")
	old, err := h.targets.GetGenerationByID(context.Background(), h.orgID, h.activeGenerationIDBefore(t, *run2.TargetID))
	require.NoError(t, err, "load generation 1")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, old.Status, "generation 1 is retired")
	require.Equal(t, models.AutomationTargetRetiredAgentConfigChanged, *old.RetiredReason, "generation 1 records the reason")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, first.SessionID), "the idle old session is released")
	require.Equal(t, &generation.ID, sessionOwnerMarker(t, h.pool, h.orgID, second.SessionID), "the new session is owned")
}

func (h *dispatchHarness) activeGenerationIDBefore(t *testing.T, targetID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT id FROM automation_target_sessions WHERE org_id = $1 AND target_id = $2 AND generation = 1`, h.orgID, targetID).Scan(&id), "load generation 1 id")
	return id
}

// fakeHeadResolver answers the dispatch-time lookup from a script.
type fakeHeadResolver struct {
	info  automations.PullRequestHeadInfo
	err   error
	calls int
}

func (f *fakeHeadResolver) ResolvePullRequestHead(_ context.Context, _, _ uuid.UUID, _ int) (automations.PullRequestHeadInfo, error) {
	f.calls++
	return f.info, f.err
}

// TestAutomationDispatch_HeadLookup proves the dispatch-time head rules
// against real rows: a missed webhook is adopted and reviewed, a closed
// pull request skips, ambiguous candidates resolve to the one at the
// current head, and when none holds it the dispatching candidate reviews it.
func TestAutomationDispatch_HeadLookup(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	h1 := "1111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222"
	h3 := "3333333333333333333333333333333333333333"
	t3 := t0.Add(10 * time.Second)

	// A delivered push at H1 while GitHub already reports H3: the run
	// reviews H3 with a new epoch and records the resolved base branch.
	resolver := &fakeHeadResolver{info: automations.PullRequestHeadInfo{SHA: h3, UpdatedAt: &t3, State: "open", BaseBranch: "release"}}
	h.dispatcher.SetHeadResolver(resolver)
	run1 := h.push(t, h1, t0)
	first := h.dispatch(t, run1, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, first.Kind, "missed-webhook run is reserved")
	require.Equal(t, 1, resolver.calls, "the lookup runs once per dispatch")
	run1 = h.reload(t, run1.ID)
	require.Equal(t, 2, *run1.HeadEpoch, "the missed head opens a new epoch")
	require.False(t, run1.HeadLookupDegraded, "a successful lookup is not degraded")
	payload := h.jobPayload(t, first.JobID)
	require.Equal(t, h3, payload["head_sha"], "the turn reviews the current head, not the delivered one")
	target, err := h.targets.GetByID(ctx, h.orgID, *run1.TargetID)
	require.NoError(t, err, "reload target")
	require.Equal(t, h3, *target.ObservedHeadSHA, "the target observed the current head")
	generation := h.activeGeneration(t, target.ID)
	require.Equal(t, h3, *generation.LastAttemptedHeadSHA, "the generation records the head being reviewed")
	require.Equal(t, "release", *generation.LastBaseRef, "the generation records the resolved base branch")
	h.finishTurn(t, run1, h3)

	// Two pushes with equal timestamps at different heads: the first is
	// strictly newer than the watermark and authoritative, the second ties
	// it and is ambiguous. The lookup resolves the tie to the candidate at
	// the current head.
	tie := t3.Add(time.Minute)
	runA := h.push(t, h1, tie)
	runB := h.push(t, h2, tie)
	require.Equal(t, models.AutomationRunHeadAuthoritative, *runA.HeadResolution, "first tie candidate is authoritative")
	require.Equal(t, 3, *runA.HeadEpoch, "first tie candidate opened epoch 3")
	require.Equal(t, models.AutomationRunHeadAmbiguous, *runB.HeadResolution, "second tie candidate is ambiguous")
	require.Nil(t, runB.HeadEpoch, "an ambiguous candidate has no epoch")
	require.Equal(t, models.AutomationRunDispatchWaiting, *runB.DispatchState, "ambiguous candidates wait visibly")
	resolver.info = automations.PullRequestHeadInfo{SHA: h2, UpdatedAt: &tie, State: "open", BaseBranch: "release"}
	lost := h.dispatch(t, runA, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchTerminalized, lost.Kind, "the candidate without the current head is superseded")
	require.Equal(t, models.AutomationRunOutcomeSuperseded, lost.OutcomeReason, "superseded outcome is recorded")
	runB = h.reload(t, runB.ID)
	require.Equal(t, models.AutomationRunHeadAuthoritative, *runB.HeadResolution, "the candidate at the current head is stamped authoritative")
	require.Equal(t, 4, *runB.HeadEpoch, "the resolved head opened epoch 4")
	target, err = h.targets.GetByID(ctx, h.orgID, target.ID)
	require.NoError(t, err, "reload target")
	require.False(t, target.HeadResolutionPending, "resolution clears the pending flag")
	require.Nil(t, target.HeadResolutionDeadlineAt, "resolution clears the deadline")
	runA = h.reload(t, runA.ID)
	require.Equal(t, runB.ID, *runA.SupersededByRunID, "the loser points at the survivor")
	won := h.dispatch(t, runB, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, won.Kind, "the survivor is reserved")
	h.finishTurn(t, runB, h2)

	// A tie where no candidate holds the current head (a missed webhook
	// for H6): the dispatching run becomes the survivor and reviews H6, and
	// the ambiguous candidate is superseded.
	tie2 := tie.Add(time.Minute)
	h4 := "4444444444444444444444444444444444444444"
	h5 := "5555555555555555555555555555555555555555"
	h6 := "6666666666666666666666666666666666666666"
	runC := h.push(t, h4, tie2)
	runD := h.push(t, h5, tie2)
	require.Equal(t, models.AutomationRunHeadAmbiguous, *runD.HeadResolution, "the tying candidate is ambiguous")
	t6 := tie2.Add(time.Second)
	resolver.info = automations.PullRequestHeadInfo{SHA: h6, UpdatedAt: &t6, State: "open", BaseBranch: "release"}
	survivor := h.dispatch(t, runC, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, survivor.Kind, "the dispatching run survives and reviews the current head")
	require.Equal(t, h6, h.jobPayload(t, survivor.JobID)["head_sha"], "the survivor reviews the missed head")
	runD = h.reload(t, runD.ID)
	require.Equal(t, models.AutomationRunStatusSkipped, runD.Status, "the other candidate is superseded")
	require.Equal(t, runC.ID, *runD.SupersededByRunID, "the other candidate points at the survivor")
	h.finishTurn(t, runC, h6)

	// A closed pull request skips at dispatch.
	runE := h.push(t, h4, t6.Add(time.Minute))
	resolver.info = automations.PullRequestHeadInfo{SHA: h4, State: "closed"}
	closed := h.dispatch(t, runE, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchTerminalized, closed.Kind, "a closed pull request is skipped")
	require.Equal(t, models.AutomationRunOutcomePRClosed, closed.OutcomeReason, "pr_closed is recorded")
}

// TestAutomationDispatch_LookupFailureWithAmbiguity proves that a failed
// lookup holds ambiguous candidates as waiting with backoff instead of
// guessing, and that a failed lookup without ambiguity degrades to the
// delivered head.
func TestAutomationDispatch_LookupFailureWithAmbiguity(t *testing.T) {
	h := newDispatchHarness(t)
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	h1 := "1111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222"
	resolver := &fakeHeadResolver{err: context.DeadlineExceeded}
	h.dispatcher.SetHeadResolver(resolver)

	runA := h.push(t, h1, t0)
	runB := h.push(t, h2, t0)
	held := h.dispatch(t, runA, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchRetry, held.Kind, "an ambiguous candidate is held while the lookup fails")
	require.Equal(t, 30*time.Second, held.RetryAfter, "the first retry is 30 seconds out")
	require.Greater(t, held.MaxWait, 30*time.Minute, "the hold is bounded beyond the ambiguity window")
	runA = h.reload(t, runA.ID)
	require.Equal(t, models.AutomationRunDispatchWaiting, *runA.DispatchState, "the held candidate stays waiting")
	require.Equal(t, models.AutomationRunStatusPending, runA.Status, "the held candidate stays pending")
	_ = runB

	// A later authoritative push on a fresh target degrades when the
	// lookup fails and nothing is ambiguous.
	h.finishAmbiguity(t, *runA.TargetID)
	runC := h.push(t, "3333333333333333333333333333333333333333", t0.Add(time.Minute))
	degraded := h.dispatch(t, runC, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, degraded.Kind, "an authoritative push dispatches on lookup failure")
	runC = h.reload(t, runC.ID)
	require.True(t, runC.HeadLookupDegraded, "the run records the degraded lookup")
}

// finishAmbiguity clears a target's ambiguity state and skips its ambiguous
// candidates, standing in for the deadline sweep that lands with completion.
func (h *dispatchHarness) finishAmbiguity(t *testing.T, targetID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', dispatch_state = 'done', outcome_reason = 'superseded', completed_at = now() WHERE org_id = $1 AND target_id = $2 AND head_resolution = 'ambiguous'`, h.orgID, targetID)
	require.NoError(t, err, "skip ambiguous candidates")
	require.NoError(t, h.targets.ClearHeadResolutionPending(ctx, nil, h.orgID, targetID), "clear ambiguity")
}

// TestAutomationDispatch_ThreadClaimContentionWaits proves a primary thread
// that cannot be claimed leaves the session untouched: the ownership
// transaction rolls back its session claim and the run waits.
func TestAutomationDispatch_ThreadClaimContentionWaits(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	h1 := "1111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222"
	run1 := h.push(t, h1, t0)
	first := h.dispatch(t, run1, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, first.Kind, "first run is reserved")
	h.finishTurn(t, run1, h1)
	_, err := h.pool.Exec(ctx, `UPDATE session_threads SET status = 'running' WHERE session_id = $1`, first.SessionID)
	require.NoError(t, err, "make the primary thread unclaimable")

	run2 := h.push(t, h2, t0.Add(time.Second))
	outcome := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchWaiting, outcome.Kind, "an unclaimable primary thread makes the run wait")
	session, err := h.sessions.GetByID(ctx, h.orgID, first.SessionID)
	require.NoError(t, err, "reload session")
	require.Equal(t, models.SessionStatusIdle, session.Status, "the session claim was rolled back")
	run2 = h.reload(t, run2.ID)
	require.Equal(t, models.AutomationRunDispatchWaiting, *run2.DispatchState, "the run is recorded as waiting")
	var messages int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM session_messages WHERE org_id = $1 AND session_id = $2 AND source = 'automation_turn'`, h.orgID, first.SessionID).Scan(&messages), "count messages")
	require.Equal(t, 1, messages, "only the first turn's message exists; the rolled-back turn inserted none")

	_, err = h.pool.Exec(ctx, `UPDATE session_threads SET status = 'idle' WHERE session_id = $1`, first.SessionID)
	require.NoError(t, err, "free the primary thread")
	recovered := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, recovered.Kind, "the waiting run is reserved once the thread frees")
}

// TestAutomationDispatch_AutomationChangedUnderLock proves dispatch rereads
// the automation under the lock: a continuity switch that committed after
// the worker loaded the run sends the run down the per-run path, and any
// other change asks for a retry with fresh input.
func TestAutomationDispatch_AutomationChangedUnderLock(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	run := h.push(t, "1111111111111111111111111111111111111111", t0)

	stale := h.automation
	_, err := h.pool.Exec(ctx, `UPDATE automations SET goal = 'tightened goal', updated_at = now() + interval '1 second' WHERE id = $1`, h.automation.ID)
	require.NoError(t, err, "change the automation after the worker loaded it")
	outcome, err := h.dispatcher.Dispatch(ctx, automations.DispatchInput{Run: run, Automation: stale, SessionTemplate: h.template(models.AgentTypeCodex)})
	require.NoError(t, err, "dispatch should not error")
	require.Equal(t, automations.DispatchRetry, outcome.Kind, "a changed automation asks the worker to reload")

	_, err = h.pool.Exec(ctx, `UPDATE automations SET session_continuity = 'per_run', updated_at = now() + interval '2 seconds' WHERE id = $1`, h.automation.ID)
	require.NoError(t, err, "switch the automation to per_run after the worker loaded it")
	outcome, err = h.dispatcher.Dispatch(ctx, automations.DispatchInput{Run: run, Automation: stale, SessionTemplate: h.template(models.AgentTypeCodex)})
	require.NoError(t, err, "dispatch should not error")
	require.Equal(t, automations.DispatchNotApplicable, outcome.Kind, "a per_run automation sends the run down the per-run path")
	run = h.reload(t, run.ID)
	require.Equal(t, models.AutomationRunStatusPending, run.Status, "the run is left for the per-run path")
	var generations int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM automation_target_sessions WHERE org_id = $1`, h.orgID).Scan(&generations), "count generations")
	require.Equal(t, 0, generations, "no generation was created from stale input")
}
