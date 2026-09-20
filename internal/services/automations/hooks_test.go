package automations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeAutomationRunStore struct {
	calls        []fakeTransitionCall
	err          error
	getErr       error
	transitioned bool // what TransitionStatusIf returns for the bool result
	run          models.AutomationRun
}

type fakeTransitionCall struct {
	orgID         uuid.UUID
	runID         uuid.UUID
	fromStatus    models.AutomationRunStatus
	toStatus      models.AutomationRunStatus
	completedAt   *time.Time
	resultSummary *string
}

func (f *fakeAutomationRunStore) TransitionStatusIf(_ context.Context, orgID, runID uuid.UUID, fromStatus, toStatus models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) (bool, error) {
	f.calls = append(f.calls, fakeTransitionCall{
		orgID:         orgID,
		runID:         runID,
		fromStatus:    fromStatus,
		toStatus:      toStatus,
		completedAt:   completedAt,
		resultSummary: resultSummary,
	})
	return f.transitioned, f.err
}

func (f *fakeAutomationRunStore) GetByRunID(_ context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error) {
	if f.getErr != nil {
		return models.AutomationRun{}, f.getErr
	}
	if f.run.OrgID != orgID || f.run.ID != runID {
		return models.AutomationRun{}, errors.New("unexpected automation run lookup")
	}
	return f.run, nil
}

func TestAutomationHooks_AutomaticPublishPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snapshot []byte
		expected models.AutomationPublishPolicy
	}{
		{name: "pull request", snapshot: []byte(`{"publish_policy":"pull_request"}`), expected: models.AutomationPublishPolicyPullRequest},
		{name: "none", snapshot: []byte(`{"publish_policy":"none"}`), expected: models.AutomationPublishPolicyNone},
		{name: "legacy snapshot", snapshot: []byte(`{}`), expected: models.AutomationPublishPolicyPullRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runID := uuid.New()
			orgID := uuid.New()
			store := &fakeAutomationRunStore{run: models.AutomationRun{ID: runID, OrgID: orgID, ConfigSnapshot: tt.snapshot}}
			h := NewAutomationHooks(store, zerolog.Nop())

			actual, err := h.AutomaticPublishPolicy(context.Background(), orgID, runID)
			require.NoError(t, err, "publish policy should resolve from the automation run snapshot")
			require.Equal(t, tt.expected, actual, "resolved publish policy should match the captured snapshot")
		})
	}
}

func TestAutomationHooks_AutomaticPublishPolicy_LookupError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("db down")
	h := NewAutomationHooks(&fakeAutomationRunStore{getErr: sentinel}, zerolog.Nop())

	_, err := h.AutomaticPublishPolicy(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, sentinel, "automation run lookup errors should be preserved")
}

func TestAutomationHooks_OnSessionComplete_NoAutomationRunID_NoOp(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: true}
	h := NewAutomationHooks(store, zerolog.Nop())

	session := &models.Session{OrgID: uuid.New()}
	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusCompleted)
	require.NoError(t, err)
	require.Empty(t, store.calls, "no update should fire when session has no automation_run_id")
}

func TestAutomationHooks_OnSessionComplete_CompletedMaps(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: true}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	orgID := uuid.New()
	summary := "wrote tests, opened PR"
	diff := "--- a/service.go\n+++ b/service.go\n@@ -1 +1 @@\n-old\n+new"
	session := &models.Session{
		OrgID:           orgID,
		AutomationRunID: &runID,
		ResultSummary:   &summary,
		Diff:            &diff,
	}

	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusCompleted)
	require.NoError(t, err)
	require.Len(t, store.calls, 1)
	call := store.calls[0]
	require.Equal(t, orgID, call.orgID)
	require.Equal(t, runID, call.runID)
	require.Equal(t, models.AutomationRunStatusRunning, call.fromStatus,
		"hook must gate on current status=running so a terminal row is never overwritten")
	require.Equal(t, models.AutomationRunStatusCompleted, call.toStatus)
	require.NotNil(t, call.completedAt)
	require.NotNil(t, call.resultSummary)
	require.Equal(t, summary, *call.resultSummary)
}

func TestAutomationHooks_OnSessionComplete_CompletedWithoutChangesMapsToNoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		diff *string
	}{
		{name: "missing diff", diff: nil},
		{name: "empty diff", diff: stringPointer("")},
		{name: "whitespace diff", diff: stringPointer(" \n\t")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &fakeAutomationRunStore{transitioned: true}
			h := NewAutomationHooks(store, zerolog.Nop())
			runID := uuid.New()
			session := &models.Session{
				OrgID:           uuid.New(),
				AutomationRunID: &runID,
				Diff:            tt.diff,
			}

			err := h.OnSessionComplete(context.Background(), session, models.SessionStatusCompleted)

			require.NoError(t, err, "completed automation session should transition cleanly")
			require.Len(t, store.calls, 1, "completed automation session should write one terminal transition")
			require.Equal(t, models.AutomationRunStatusCompletedNoop, store.calls[0].toStatus, "zero-diff automation session should be recorded as a no-op")
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestAutomationHooks_OnSessionComplete_WritebackAfterTransition(t *testing.T) {
	t.Parallel()

	runID := uuid.New()
	orgID := uuid.New()
	provider := models.AutomationEventProviderPagerDuty
	summary := "fixed checkout"
	store := &fakeAutomationRunStore{
		transitioned: true,
		run: models.AutomationRun{
			ID:       runID,
			OrgID:    orgID,
			Provider: &provider,
		},
	}
	writebacks := &fakePagerDutyAutomationWritebacker{}
	h := NewAutomationHooks(store, zerolog.Nop())
	h.SetPagerDutyWritebacker(writebacks)
	session := &models.Session{
		ID:              uuid.New(),
		OrgID:           orgID,
		AutomationRunID: &runID,
		ResultSummary:   &summary,
	}

	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusCompleted)

	require.NoError(t, err, "OnSessionComplete should not fail when writeback succeeds")
	require.Equal(t, session.ID, writebacks.sessionID, "writeback should receive the completed session")
	require.Equal(t, runID, writebacks.automationRun.ID, "writeback should receive the automation run context")
	require.Equal(t, models.SessionStatusCompleted, writebacks.status, "writeback should receive terminal status")
	require.Equal(t, summary, writebacks.summary, "writeback should receive result summary")
}

func TestAutomationHooks_OnSessionComplete_FailedPrefersErrorWhenNoSummary(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: true}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	errMsg := "agent hit rate limit"
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
		Error:           &errMsg,
	}

	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusFailed)
	require.NoError(t, err)
	require.Len(t, store.calls, 1)
	require.Equal(t, models.AutomationRunStatusRunning, store.calls[0].fromStatus)
	require.Equal(t, models.AutomationRunStatusFailed, store.calls[0].toStatus)
	require.Equal(t, errMsg, *store.calls[0].resultSummary)
}

// TestAutomationHooks_OnSessionComplete_NeedsHumanGuidanceMapsToFailed
// pins the mapping that matches pm.ProjectHooks: needs_human_guidance is
// terminal from the orchestrator's perspective, so the automation_run row
// must flip to failed (with a descriptive summary) rather than stay stuck
// in "running" until the 1-hour reaper sweeps it.
func TestAutomationHooks_OnSessionComplete_NeedsHumanGuidanceMapsToFailed(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: true}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
	}

	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusNeedsHumanGuidance)
	require.NoError(t, err)
	require.Len(t, store.calls, 1)
	require.Equal(t, models.AutomationRunStatusRunning, store.calls[0].fromStatus)
	require.Equal(t, models.AutomationRunStatusFailed, store.calls[0].toStatus)
	require.NotNil(t, store.calls[0].resultSummary)
	require.Equal(t, "Agent run needs human guidance.", *store.calls[0].resultSummary)
}

func TestAutomationHooks_OnSessionComplete_IgnoresNonTerminal(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: true}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
	}

	for _, status := range []models.SessionStatus{
		models.SessionStatusRunning,
		models.SessionStatusAwaitingInput,
		models.SessionStatusCancelled,
		models.SessionStatusPending,
	} {
		err := h.OnSessionComplete(context.Background(), session, status)
		require.NoError(t, err, "status %q should no-op", status)
	}
	require.Empty(t, store.calls, "non-terminal statuses must not touch the run row")
}

// TestAutomationHooks_OnSessionComplete_AlreadyTerminalIsSafeNoOp verifies
// that if the hook fires twice (e.g. both RunAgent step 14 and failRun dispatch
// a completion update), the second call sees transitioned=false because the
// row is no longer "running" — and the hook must return cleanly instead of
// overwriting the first writer's terminal status.
func TestAutomationHooks_OnSessionComplete_AlreadyTerminalIsSafeNoOp(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{transitioned: false}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
	}

	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusFailed)
	require.NoError(t, err, "a lost-race transition must not surface as an error")
	require.Len(t, store.calls, 1, "the hook still attempts the conditional transition")
	require.Equal(t, models.AutomationRunStatusRunning, store.calls[0].fromStatus)
}

type fakePagerDutyAutomationWritebacker struct {
	sessionID     uuid.UUID
	automationRun models.AutomationRun
	status        models.SessionStatus
	summary       string
}

func (f *fakePagerDutyAutomationWritebacker) OnAutomationSessionComplete(_ context.Context, session models.Session, automationRun models.AutomationRun, status models.SessionStatus, summary string) error {
	f.sessionID = session.ID
	f.automationRun = automationRun
	f.status = status
	f.summary = summary
	return nil
}

func TestAutomationHooks_OnSessionComplete_TransitionErrorWraps(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("db down")
	store := &fakeAutomationRunStore{err: sentinel}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	session := &models.Session{OrgID: uuid.New(), AutomationRunID: &runID}
	err := h.OnSessionComplete(context.Background(), session, models.SessionStatusCompleted)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel, "store errors must propagate so the worker can retry/log")
}

// TestDeriveSummary_FallbackLabels covers the static label arms used when the
// session has neither a ResultSummary nor (for failures) an Error string. The
// audit log relies on these defaults so a row never lands with an empty
// result_summary.
func TestDeriveSummary_FallbackLabels(t *testing.T) {
	t.Parallel()

	t.Run("completed without summary", func(t *testing.T) {
		t.Parallel()
		got := deriveSummary(&models.Session{}, "completed")
		require.NotNil(t, got)
		require.Equal(t, "Agent session completed.", *got)
	})

	t.Run("failed without summary or error", func(t *testing.T) {
		t.Parallel()
		got := deriveSummary(&models.Session{}, "failed")
		require.NotNil(t, got)
		require.Equal(t, "Agent session failed.", *got)
	})

	t.Run("needs_human_guidance falls back to descriptive label", func(t *testing.T) {
		t.Parallel()
		got := deriveSummary(&models.Session{}, "needs_human_guidance")
		require.NotNil(t, got)
		require.Equal(t, "Agent run needs human guidance.", *got)
	})

	t.Run("unexpected status falls through to generic label", func(t *testing.T) {
		t.Parallel()
		// Defensive default: if a future caller drops in a new terminal
		// status without updating the switch, we still produce a non-empty
		// summary that names the status.
		got := deriveSummary(&models.Session{}, "weird")
		require.NotNil(t, got)
		require.Contains(t, *got, "weird")
	})
}

// --- on-failure model promotion -------------------------------------------
//
// These cover promoteToNextModelRank through its only caller,
// OnSessionComplete, because what matters is the *combination*: a promoted run
// must leave the hook without the terminal running->failed write that every
// other failure path performs.

// twoRankModelChainSnapshot is a run snapshot whose chain is the primary plus
// one fallback, so len(ranks) == 2: one spent attempt still leaves a rank to
// promote to, and two spent attempts exhaust the chain.
var twoRankModelChainSnapshot = []byte(`{"agent_type":"claude_code","model_override":"claude-sonnet-4-5","fallback_models":{"models":["gpt-5-codex"]}}`)

// threeRankModelChainSnapshot exists for the cases that need two spent
// attempts *and* a rank still remaining, so that exhaustion cannot be what
// declines the promotion and the guard under test is the only explanation.
var threeRankModelChainSnapshot = []byte(`{"agent_type":"claude_code","model_override":"claude-sonnet-4-5","fallback_models":{"models":["gpt-5-codex","claude-opus-4-1"]}}`)

// preFallbackModelChainSnapshot is the shape every run written before this
// feature carries: primary model fields, no fallback_models key at all.
var preFallbackModelChainSnapshot = []byte(`{"agent_type":"claude_code","model_override":"claude-sonnet-4-5"}`)

// capacityFailure is an upstream overload marker (agent.ModelUnavailable
// matches it). It says nothing about the work, which is the whole premise of
// promoting to the next rank.
const capacityFailure = "API error: overloaded_error: server is overloaded"

// workFailure is a verdict on the task itself. A different model would reach
// the same place, so it must never trigger a promotion.
const workFailure = "the unit tests failed: 3 assertions did not hold"

// fakeFallbackRunStore serves both interfaces the promotion path needs: the
// required automationRunStore (transitions + lookup) and the optional
// automationFallbackRunStore (attempt history). Kept separate from
// fakeAutomationRunStore because promotion writes *two* transitions with
// different outcomes (running->pending wins, pending->failed compensates), so
// a single canned bool is not enough to express the cases.
type fakeFallbackRunStore struct {
	run models.AutomationRun
	// transition, when set, decides each conditional transition by its
	// from/to pair. Unset means every transition wins.
	transition func(from, to models.AutomationRunStatus) (bool, error)
	calls      []fakeTransitionCall
	// attempts is the run's session history in the order the store hands it
	// over: NEWEST FIRST. The promotion path depends on that order, so the
	// fake must not quietly serve it the other way round.
	attempts     []models.AutomationRunAttempt
	attemptsErr  error
	attemptCalls int
	getErr       error
}

func (f *fakeFallbackRunStore) TransitionStatusIf(_ context.Context, orgID, runID uuid.UUID, fromStatus, toStatus models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) (bool, error) {
	f.calls = append(f.calls, fakeTransitionCall{
		orgID:         orgID,
		runID:         runID,
		fromStatus:    fromStatus,
		toStatus:      toStatus,
		completedAt:   completedAt,
		resultSummary: resultSummary,
	})
	if f.transition == nil {
		return true, nil
	}
	return f.transition(fromStatus, toStatus)
}

func (f *fakeFallbackRunStore) GetByRunID(_ context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error) {
	if f.getErr != nil {
		return models.AutomationRun{}, f.getErr
	}
	if f.run.OrgID != orgID || f.run.ID != runID {
		return models.AutomationRun{}, errors.New("unexpected automation run lookup")
	}
	return f.run, nil
}

func (f *fakeFallbackRunStore) ListSessionAttempts(_ context.Context, orgID, runID uuid.UUID) ([]models.AutomationRunAttempt, error) {
	f.attemptCalls++
	if f.attemptsErr != nil {
		return nil, f.attemptsErr
	}
	if f.run.OrgID != orgID || f.run.ID != runID {
		return nil, errors.New("unexpected automation run attempt lookup")
	}
	return f.attempts, nil
}

type fakeEnqueueCall struct {
	orgID     uuid.UUID
	queue     string
	jobType   string
	payload   any
	priority  int
	dedupeKey *string
}

type fakeFallbackJobStore struct {
	jobID    uuid.UUID
	err      error
	calls    []fakeEnqueueCall
	notified []uuid.UUID
}

func (f *fakeFallbackJobStore) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	f.calls = append(f.calls, fakeEnqueueCall{
		orgID:     orgID,
		queue:     queue,
		jobType:   jobType,
		payload:   payload,
		priority:  priority,
		dedupeKey: dedupeKey,
	})
	if f.err != nil {
		return uuid.Nil, f.err
	}
	return f.jobID, nil
}

func (f *fakeFallbackJobStore) Notify(_ context.Context, jobID uuid.UUID) {
	f.notified = append(f.notified, jobID)
}

// attemptsForRanks builds the history a run would have after the first `count`
// ranks of `snapshot` each spawned a session that left no work behind.
//
// The attempts are derived from the snapshot's own chain rather than written
// out by hand so that each one really does Matches() the rank it stands for —
// a hand-typed agent/model pair that drifted from the chain would silently turn
// an "already spent" case into a "still remaining" one and the test would pass
// for the wrong reason. The slice is newest-first, like ListSessionAttempts.
func attemptsForRanks(t *testing.T, snapshot []byte, count int) []models.AutomationRunAttempt {
	t.Helper()

	ranks, ok, err := models.AutomationModelRanksFromConfigSnapshot(snapshot)
	require.NoError(t, err, "the test snapshot must parse as a model chain")
	require.True(t, ok, "the test snapshot must carry a fallback chain")
	require.LessOrEqual(t, count, len(ranks), "a run cannot have spent more attempts than its chain has ranks")

	attempts := make([]models.AutomationRunAttempt, 0, count)
	for i := count - 1; i >= 0; i-- {
		attempts = append(attempts, models.AutomationRunAttempt{
			SessionID: uuid.New(),
			AgentType: ranks[i].AgentType,
			Model:     ranks[i].Model,
		})
	}
	return attempts
}

// promotionFixture wires a hooks value with a fallback promoter over one
// automation run, so each case only has to state what makes it different.
type promotionFixture struct {
	hooks   *AutomationHooks
	runs    *fakeFallbackRunStore
	jobs    *fakeFallbackJobStore
	orgID   uuid.UUID
	runID   uuid.UUID
	autoID  uuid.UUID
	session *models.Session
}

// newPromotionFixture stands up a run whose attempt history is `attempts`
// (newest first). The hook is invoked for attempts[0]'s session, i.e. the run's
// newest attempt — the only one allowed to promote — so a case that wants the
// superseded-session path has to say so by overriding session.ID.
func newPromotionFixture(t *testing.T, snapshot []byte, attempts []models.AutomationRunAttempt) *promotionFixture {
	t.Helper()

	orgID := uuid.New()
	runID := uuid.New()
	autoID := uuid.New()
	runs := &fakeFallbackRunStore{
		run: models.AutomationRun{
			ID:             runID,
			OrgID:          orgID,
			AutomationID:   autoID,
			ConfigSnapshot: snapshot,
		},
		attempts: attempts,
	}
	jobs := &fakeFallbackJobStore{jobID: uuid.New()}
	h := NewAutomationHooks(runs, zerolog.Nop())
	h.SetFallbackPromoter(runs, jobs)

	sessionID := uuid.New()
	if len(attempts) > 0 {
		sessionID = attempts[0].SessionID
	}

	errMsg := capacityFailure
	return &promotionFixture{
		hooks:  h,
		runs:   runs,
		jobs:   jobs,
		orgID:  orgID,
		runID:  runID,
		autoID: autoID,
		session: &models.Session{
			ID:              sessionID,
			OrgID:           orgID,
			AutomationRunID: &runID,
			Error:           &errMsg,
		},
	}
}

// requireFailedExactlyOnce asserts the run landed on the pre-existing terminal
// path: one conditional running->failed write, and nothing enqueued.
func requireFailedExactlyOnce(t *testing.T, f *promotionFixture, why string) {
	t.Helper()

	require.Len(t, f.runs.calls, 1, "%s: the run must take the ordinary single terminal write", why)
	require.Equal(t, models.AutomationRunStatusRunning, f.runs.calls[0].fromStatus,
		"%s: the terminal write must still be gated on running so a terminal row is never overwritten", why)
	require.Equal(t, models.AutomationRunStatusFailed, f.runs.calls[0].toStatus,
		"%s: the run must land failed rather than be re-dispatched", why)
	require.Empty(t, f.jobs.calls, "%s: nothing may be enqueued when the run is not promoted", why)
	require.Empty(t, f.jobs.notified, "%s: no worker may be woken when the run is not promoted", why)
}

// TestAutomationHooks_OnSessionComplete_PromotesOnModelUnavailable is the
// happy path: a run whose session died of upstream capacity goes back to
// pending and is re-dispatched, instead of being reported to the user as a
// failure that has nothing to do with their task.
func TestAutomationHooks_OnSessionComplete_PromotesOnModelUnavailable(t *testing.T) {
	t.Parallel()

	// One attempt has been spent (the rank-0 session that just failed), so the
	// chain's second and last rank is still unspent.
	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "a successful promotion is not an error")
	require.Len(t, f.runs.calls, 1,
		"promotion must write exactly one transition; a second terminal write would undo the re-dispatch")
	promote := f.runs.calls[0]
	require.Equal(t, models.AutomationRunStatusRunning, promote.fromStatus,
		"promotion must be a CAS off running so only one promoter can win")
	require.Equal(t, models.AutomationRunStatusPending, promote.toStatus,
		"pending is what the worker's unchanged run.Status guard requires to accept the re-dispatch")
	require.Nil(t, promote.completedAt,
		"the run is still in flight, so stamping completed_at would make it look finished")
	require.Nil(t, promote.resultSummary,
		"a run that is about to retry must not carry the failed attempt's summary")

	require.Len(t, f.jobs.calls, 1, "promotion must enqueue exactly one re-dispatch job")
	enqueued := f.jobs.calls[0]
	require.Equal(t, f.orgID, enqueued.orgID, "the job must be scoped to the run's org")
	require.Equal(t, models.JobTypeAutomationRun, enqueued.jobType,
		"the retry must go through the ordinary automation_run handler, which re-selects the model rank")
	require.Equal(t, map[string]string{
		"org_id":            f.orgID.String(),
		"automation_id":     f.autoID.String(),
		"automation_run_id": f.runID.String(),
	}, enqueued.payload, "the payload must identify the same run so the handler resumes it rather than starting a new one")
	require.NotNil(t, enqueued.dedupeKey, "the re-dispatch must be deduped; two hook invocations can both reach here")
	require.Equal(t, "automation_run_fallback:"+f.runID.String()+":1", *enqueued.dedupeKey,
		"the dedupe key is keyed on the attempt count so duplicate hooks collapse but a later rank still gets its own job")

	require.Equal(t, []uuid.UUID{f.jobs.jobID}, f.jobs.notified,
		"the enqueued job must be notified, otherwise the run waits for the next poll tick")
}

// TestAutomationHooks_OnSessionComplete_PromotesHydratedFailedSession pins the
// contract the orchestrator's failure path now honors: it copies the result's
// error onto the in-memory session before invoking the hooks. Classification
// reads that field, so before the hydration fix the hook saw the session's
// load-time Error (nil) and no automation run ever promoted — the whole feature
// was inert on the path it exists for.
func TestAutomationHooks_OnSessionComplete_PromotesHydratedFailedSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// sessionError is what the orchestrator left on the session before
		// calling the hook.
		sessionError *string
		wantPromoted bool
	}{
		{
			name:         "hydrated with the capacity error",
			sessionError: stringPointer(capacityFailure),
			wantPromoted: true,
		},
		{
			// The pre-fix shape: the failure never reached the struct, so
			// there is nothing to classify and the run must simply fail.
			name:         "no error on the session",
			sessionError: nil,
			wantPromoted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
			f.session.Error = tt.sessionError

			err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

			require.NoError(t, err, "neither outcome is an error")
			if !tt.wantPromoted {
				requireFailedExactlyOnce(t, f, "an unclassifiable failure is not a capacity failure")
				return
			}
			require.Len(t, f.runs.calls, 1, "a promoted run takes only the running->pending write")
			require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
				"a capacity error carried on the session must send the run back to pending")
			require.Len(t, f.jobs.calls, 1, "the promotion must reach the enqueue end to end")
			require.Equal(t, []uuid.UUID{f.jobs.jobID}, f.jobs.notified,
				"the re-dispatch must be woken, not left for the next poll tick")
		})
	}
}

// TestAutomationHooks_OnSessionComplete_DoesNotPromoteOrdinaryFailure pins the
// deliberate narrowness of the rule: only a capacity error earns another
// model. Burning the chain on a deterministic task failure costs real money
// and side effects to reach the same verdict.
func TestAutomationHooks_OnSessionComplete_DoesNotPromoteOrdinaryFailure(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
	failure := workFailure
	f.session.Error = &failure

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "an ordinary failure still lands cleanly")
	requireFailedExactlyOnce(t, f, "a failing test suite is a verdict on the work")
	require.Equal(t, workFailure, *f.runs.calls[0].resultSummary,
		"the user must still see why the run failed")
}

// TestAutomationHooks_OnSessionComplete_DoesNotPromoteExhaustedChain: once
// every rank in the chain has been attempted there is nothing left to promote
// to, and the run must land failed rather than loop.
//
// Exhaustion is decided by which (agent, model) pairs the attempts already
// spent, not by how many sessions ran: pre-flight availability can skip a rank
// without spawning a session, so counting would resume in the wrong place.
func TestAutomationHooks_OnSessionComplete_DoesNotPromoteExhaustedChain(t *testing.T) {
	t.Parallel()

	// Both ranks of the two-rank chain have been attempted.
	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 2))

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "an exhausted chain is a normal failure, not an error")
	requireFailedExactlyOnce(t, f, "every rank in the chain has already been attempted")
}

// TestAutomationHooks_OnSessionComplete_ExhaustionCountsSpentRanksNotSessions
// is why the attempt history replaced a bare session count. Sessions and ranks
// are not one-to-one: pre-flight availability skips a rank without spawning a
// session, and a re-dispatch can land on the same rank twice. A count would
// call this two-rank chain exhausted after two sessions and report "every model
// was already attempted" while a model the user configured had never run.
func TestAutomationHooks_OnSessionComplete_ExhaustionCountsSpentRanksNotSessions(t *testing.T) {
	t.Parallel()

	// Two sessions, both on rank 0: the fallback rank is still untouched.
	newest := attemptsForRanks(t, twoRankModelChainSnapshot, 1)[0]
	older := newest
	older.SessionID = uuid.New()
	f := newPromotionFixture(t, twoRankModelChainSnapshot, []models.AutomationRunAttempt{newest, older})

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "promotion should succeed")
	require.Len(t, f.runs.calls, 1,
		"two sessions on one rank must not read as an exhausted chain; the run still has a model to try")
	require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
		"the unspent fallback rank must still send the run back to pending")
	require.Len(t, f.jobs.calls, 1, "the untried rank must be dispatched")
	require.Equal(t, "automation_run_fallback:"+f.runID.String()+":2", *f.jobs.calls[0].dedupeKey,
		"the dedupe key counts attempts, so this re-dispatch does not collide with the previous attempt's job")
}

// TestAutomationHooks_OnSessionComplete_OnlyNewestSessionPromotes is the guard
// against duplicate hook deliveries for a session the run has already moved
// past. The status CAS cannot catch this on its own: the row is legitimately
// "running" again under its successor, so a stale session would win the CAS,
// yank a running attempt back to pending and enqueue a second dispatch.
func TestAutomationHooks_OnSessionComplete_OnlyNewestSessionPromotes(t *testing.T) {
	t.Parallel()

	// Two of three ranks spent, so a rank does remain: the only thing that can
	// decline this promotion is the superseded-session guard itself.
	attempts := attemptsForRanks(t, threeRankModelChainSnapshot, 2)
	f := newPromotionFixture(t, threeRankModelChainSnapshot, attempts)
	// Re-deliver the hook for the older, already-superseded session.
	f.session.ID = attempts[1].SessionID

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "a duplicate delivery must land cleanly, not error")
	require.Equal(t, 1, f.runs.attemptCalls,
		"the guard must consult the attempt history; it cannot be decided from the session alone")
	requireFailedExactlyOnce(t, f,
		"a duplicate delivery for a superseded session would otherwise yank the legitimately-running attempt back to pending and start a second concurrent agent session on the same run")
}

// TestAutomationHooks_OnSessionComplete_DoesNotPromoteLegacySnapshot covers
// runs already in flight when this shipped. Their snapshot has no
// fallback_models key, so there is no chain to walk and the old behavior must
// hold — notably without falling back to the live automation row, which may
// have gained a chain after this run was dispatched.
func TestAutomationHooks_OnSessionComplete_DoesNotPromoteLegacySnapshot(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, preFallbackModelChainSnapshot, []models.AutomationRunAttempt{{SessionID: uuid.New()}})

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "a pre-feature run must fail the way it always did")
	requireFailedExactlyOnce(t, f, "the run's snapshot predates fallback models")
	require.Zero(t, f.runs.attemptCalls,
		"a run with no chain must not even be costed an attempt-history query")
}

// TestAutomationHooks_OnSessionComplete_DoesNotPromoteWhenWorkWasProduced is
// the guard that keeps promotion safe. A capacity error hit *after* the agent
// pushed a branch or opened a PR would, if retried on a fresh session, produce
// a second PR for the same run — so the run must fail instead.
//
// The evidence is read off the persisted attempt rows, which is the only place
// it is trustworthy: the in-memory session carries whatever publish state it
// had when the worker loaded it, so the old session-based check was inert.
func TestAutomationHooks_OnSessionComplete_DoesNotPromoteWhenWorkWasProduced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// mutate marks work on the run's attempt history (newest first).
		mutate   func(attempts []models.AutomationRunAttempt)
		evidence string
	}{
		{
			name: "newest attempt produced a diff",
			mutate: func(attempts []models.AutomationRunAttempt) {
				attempts[0].ProducedDiff = true
			},
			evidence: "the agent already produced changes a retry would redo",
		},
		{
			name: "newest attempt opened a pull request",
			mutate: func(attempts []models.AutomationRunAttempt) {
				attempts[0].ProducedPullRequest = true
			},
			evidence: "a PR for this run already exists on the remote",
		},
		{
			// An earlier rank's side effects outlive the session that made
			// them, so the check has to span the whole history rather than
			// only the attempt that just failed.
			name: "an earlier attempt opened a pull request",
			mutate: func(attempts []models.AutomationRunAttempt) {
				attempts[len(attempts)-1].ProducedPullRequest = true
			},
			evidence: "an earlier rank already opened a PR for this run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Two of three ranks spent so a rank remains: the work guard is
			// the only thing that can decline these promotions.
			attempts := attemptsForRanks(t, threeRankModelChainSnapshot, 2)
			tt.mutate(attempts)
			f := newPromotionFixture(t, threeRankModelChainSnapshot, attempts)

			err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

			require.NoError(t, err, "declining to promote is not an error")
			requireFailedExactlyOnce(t, f,
				"promoting would duplicate work that already exists: "+tt.evidence)
		})
	}
}

// TestAutomationHooks_OnSessionComplete_AttemptsWithoutWorkDoNotBlockPromotion
// is the other half of the work guard. An attempt that produced neither a diff
// nor a PR is the ordinary shape of a session that died on capacity before it
// did anything, so it must not be mistaken for evidence and suppress every
// promotion the feature exists to perform.
func TestAutomationHooks_OnSessionComplete_AttemptsWithoutWorkDoNotBlockPromotion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(session *models.Session)
		whyNotWork string
	}{
		{
			name:       "attempt left nothing behind",
			mutate:     func(*models.Session) {},
			whyNotWork: "both produced_* columns are false, so no side effect exists to duplicate",
		},
		{
			// Pins that the in-memory session is no longer consulted: its
			// publish fields are load-time values that say nothing about what
			// the run went on to do, and reading them suppressed promotions
			// that were perfectly safe.
			name: "stale in-memory publish state on the session",
			mutate: func(session *models.Session) {
				session.PRCreationState = models.PRCreationStateSucceeded
				session.PRPushState = models.PRPushStatePushing
			},
			whyNotWork: "the persisted attempt rows, not the session struct, are authoritative about side effects",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
			tt.mutate(f.session)

			err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

			require.NoError(t, err, "promotion should succeed")
			require.Len(t, f.jobs.calls, 1, "%s, so the run is still promotable", tt.whyNotWork)
			require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
				"%s, so the run must go back to pending", tt.whyNotWork)
		})
	}
}

// TestAutomationHooks_OnSessionComplete_WithoutPromoterKeepsLegacyBehavior
// protects every caller that never opts in (tests, tools, any wiring that
// constructs hooks without the optional stores): their failure path must be
// exactly what it was before this feature existed.
func TestAutomationHooks_OnSessionComplete_WithoutPromoterKeepsLegacyBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// wire is how (or whether) the promoter is configured.
		wire func(h *AutomationHooks, runs automationFallbackRunStore, jobs automationFallbackJobStore)
	}{
		{name: "never wired", wire: func(*AutomationHooks, automationFallbackRunStore, automationFallbackJobStore) {}},
		{
			// A partially-wired promoter cannot promote, so the setter must
			// refuse it outright rather than leave a half-built promoter that
			// nil-panics inside the hook.
			name: "wired with a nil job store",
			wire: func(h *AutomationHooks, runs automationFallbackRunStore, _ automationFallbackJobStore) {
				h.SetFallbackPromoter(runs, nil)
			},
		},
		{
			name: "wired with a nil run store",
			wire: func(h *AutomationHooks, _ automationFallbackRunStore, jobs automationFallbackJobStore) {
				h.SetFallbackPromoter(nil, jobs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			runID := uuid.New()
			runs := &fakeFallbackRunStore{
				run:      models.AutomationRun{ID: runID, OrgID: orgID, ConfigSnapshot: twoRankModelChainSnapshot},
				attempts: attemptsForRanks(t, twoRankModelChainSnapshot, 1),
			}
			jobs := &fakeFallbackJobStore{jobID: uuid.New()}
			h := NewAutomationHooks(runs, zerolog.Nop())
			tt.wire(h, runs, jobs)

			errMsg := capacityFailure
			session := &models.Session{ID: runs.attempts[0].SessionID, OrgID: orgID, AutomationRunID: &runID, Error: &errMsg}

			err := h.OnSessionComplete(context.Background(), session, models.SessionStatusFailed)

			require.NoError(t, err, "the unpromoted failure path must stay clean")
			require.Len(t, runs.calls, 1, "without a promoter the hook writes only its own terminal transition")
			require.Equal(t, models.AutomationRunStatusRunning, runs.calls[0].fromStatus,
				"the pre-existing CAS on running must be untouched")
			require.Equal(t, models.AutomationRunStatusFailed, runs.calls[0].toStatus,
				"a capacity error with no promoter must still fail the run")
			require.Equal(t, capacityFailure, *runs.calls[0].resultSummary,
				"the summary must still come from the session's error")
			require.Zero(t, runs.attemptCalls, "an unwired promoter must not query anything")
			require.Empty(t, jobs.calls, "an unwired promoter must not enqueue anything")
		})
	}
}

// TestAutomationHooks_OnSessionComplete_FallbackReadErrorsStillWriteTerminal:
// every read the promotion path makes is best-effort. The run is sitting at
// "running" when the hook is entered, so returning a transient error instead of
// falling through would strand the row there until the 1-hour reaper swept it —
// trading a clean, immediate failure for a silent hang.
func TestAutomationHooks_OnSessionComplete_FallbackReadErrorsStillWriteTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// breakStore injects one failure into the promotion path.
		breakStore func(runs *fakeFallbackRunStore)
		// wantCalls is how many transitions the hook attempts: one for a
		// failure raised before the CAS, two when the CAS itself is what failed.
		wantCalls int
		why       string
	}{
		{
			name:       "run lookup fails",
			breakStore: func(runs *fakeFallbackRunStore) { runs.getErr = errors.New("runs table unavailable") },
			wantCalls:  1,
			why:        "a failed snapshot read must not strand the run as running",
		},
		{
			name:       "attempt history read fails",
			breakStore: func(runs *fakeFallbackRunStore) { runs.attemptsErr = errors.New("sessions join timed out") },
			wantCalls:  1,
			why:        "a failed attempt-history read must not strand the run as running",
		},
		{
			name: "promotion CAS fails",
			breakStore: func(runs *fakeFallbackRunStore) {
				runs.transition = func(_, to models.AutomationRunStatus) (bool, error) {
					if to == models.AutomationRunStatusPending {
						return false, errors.New("deadlock detected")
					}
					return true, nil
				}
			},
			wantCalls: 2,
			why:       "a failed promotion CAS must fall through to the terminal write, not abort the hook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
			tt.breakStore(f.runs)

			err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

			require.NoError(t, err, "%s, so the hook must not surface the read error", tt.why)
			require.Len(t, f.runs.calls, tt.wantCalls, "%s", tt.why)
			terminal := f.runs.calls[len(f.runs.calls)-1]
			require.Equal(t, models.AutomationRunStatusRunning, terminal.fromStatus,
				"%s: the fall-through write keeps the ordinary CAS off running", tt.why)
			require.Equal(t, models.AutomationRunStatusFailed, terminal.toStatus,
				"%s: the run must reach a terminal status immediately", tt.why)
			require.Equal(t, capacityFailure, *terminal.resultSummary,
				"%s: the user must still see the session's own failure, not an internal read error", tt.why)
			require.Empty(t, f.jobs.calls, "%s: a promotion that could not be decided must not be dispatched", tt.why)
			require.Empty(t, f.jobs.notified, "%s: no worker may be woken for a promotion that did not happen", tt.why)
		})
	}
}

// TestAutomationHooks_OnSessionComplete_FailsRunWhenReEnqueueFails covers the
// compensating write. The run is already pending at that point with nothing
// coming to claim it, so without this a clean failure would turn into a silent
// hour-long hang until the reaper swept the row.
func TestAutomationHooks_OnSessionComplete_FailsRunWhenReEnqueueFails(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
	f.jobs.err = errors.New("jobs table unavailable")
	summary := "agent stopped before making changes"
	f.session.ResultSummary = &summary

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err,
		"the enqueue failure is compensated for here; surfacing it would retry the hook against an already-pending row")
	require.Len(t, f.runs.calls, 2,
		"exactly two writes: the promotion, then the compensating failure")
	require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
		"the run was moved to pending before the enqueue was attempted")

	compensating := f.runs.calls[1]
	require.Equal(t, models.AutomationRunStatusPending, compensating.fromStatus,
		"the compensating write must CAS off pending — the state the failed promotion left behind")
	require.Equal(t, models.AutomationRunStatusFailed, compensating.toStatus,
		"the orphaned pending row must be failed rather than left to the reaper")
	require.NotNil(t, compensating.completedAt, "a failed run must be stamped complete")
	require.NotNil(t, compensating.resultSummary, "the compensating write must not blank the summary")
	require.Equal(t, summary, *compensating.resultSummary,
		"the user must see the original failure, not an internal enqueue error")

	for _, call := range f.runs.calls {
		// The caller's own terminal write is the running->failed pair. Seeing
		// it here would mean the run was failed twice for one session.
		require.False(t,
			call.fromStatus == models.AutomationRunStatusRunning && call.toStatus == models.AutomationRunStatusFailed,
			"OnSessionComplete must not also apply its own running->failed write after the compensating one")
	}
}

// TestAutomationHooks_OnSessionComplete_EnqueueDedupeCollisionIsAPromotion:
// Enqueue reports "an equivalent job is already pending" as (uuid.Nil, nil).
// That satisfies the intent — the run will be picked up — so it must count as a
// promotion and must not be compensated back to failed. There is simply no new
// job to wake, and notifying uuid.Nil would be a lookup for a job that does not
// exist.
func TestAutomationHooks_OnSessionComplete_EnqueueDedupeCollisionIsAPromotion(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
	f.jobs.jobID = uuid.Nil

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "a dedupe collision is a successful outcome, not an error")
	require.Len(t, f.runs.calls, 1,
		"the run stays promoted: no compensating failure and no terminal write may follow a dedupe collision")
	require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
		"the already-queued job will claim the run out of pending")
	require.Len(t, f.jobs.calls, 1, "the enqueue is still attempted; the collision is discovered inside it")
	require.Empty(t, f.jobs.notified,
		"there is no new job to notify — waking uuid.Nil would chase a job that does not exist")
}

// TestAutomationHooks_OnSessionComplete_LostPromotionCASFailsRun: the
// running->pending CAS is what makes concurrent hook invocations safe. The
// loser must fall through to the ordinary terminal path and enqueue nothing,
// so the winner's re-dispatch is the only one.
func TestAutomationHooks_OnSessionComplete_LostPromotionCASFailsRun(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
	f.runs.transition = func(_, to models.AutomationRunStatus) (bool, error) {
		// Another promoter already moved the row off running.
		return to != models.AutomationRunStatusPending, nil
	}

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

	require.NoError(t, err, "losing the promotion race must not surface as an error")
	require.Len(t, f.runs.calls, 2, "the loser attempts the promotion, then falls through to the terminal write")
	require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus, "the promotion was attempted")
	require.Equal(t, models.AutomationRunStatusRunning, f.runs.calls[1].fromStatus,
		"the fall-through write stays gated on running, so it no-ops against the winner's row")
	require.Empty(t, f.jobs.calls,
		"the loser must not enqueue: the winner already did, and a second job would run the automation twice")
	require.Empty(t, f.jobs.notified, "no worker may be woken by the loser")
}

// TestAutomationHooks_OnSessionComplete_NeedsHumanGuidanceIsNeverPromoted:
// needs_human_guidance is the agent's verdict that the *task* needs a person.
// Even if its text happens to look like a capacity error, retrying it on
// another model would reach the same place, so the run must fail.
func TestAutomationHooks_OnSessionComplete_NeedsHumanGuidanceIsNeverPromoted(t *testing.T) {
	t.Parallel()

	f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
	explanation := capacityFailure
	f.session.FailureExplanation = &explanation

	err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusNeedsHumanGuidance)

	require.NoError(t, err, "needs_human_guidance still maps cleanly onto the run")
	requireFailedExactlyOnce(t, f, "needs_human_guidance is a verdict on the task, not on the model")
	require.Zero(t, f.runs.attemptCalls,
		"the promotion path must not even be entered for a non-failed terminal status")
}

// TestAutomationHooks_OnSessionComplete_ReadsCapacityMarkerFromEitherField:
// the structured explanation is a generated human summary that frequently
// drops the upstream marker, so the raw error has to be consulted too. A
// session whose marker survives in only one of the two fields must still be
// promoted.
func TestAutomationHooks_OnSessionComplete_ReadsCapacityMarkerFromEitherField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		explanation *string
		errMsg      *string
	}{
		{
			name:        "marker only in the structured explanation",
			explanation: stringPointer(capacityFailure),
			errMsg:      nil,
		},
		{
			// The realistic shape: the generated explanation paraphrases the
			// failure and loses the marker the classifier matches on.
			name:        "marker only in the raw error",
			explanation: stringPointer("The agent stopped before it could analyze the repository."),
			errMsg:      stringPointer(capacityFailure),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newPromotionFixture(t, twoRankModelChainSnapshot, attemptsForRanks(t, twoRankModelChainSnapshot, 1))
			f.session.FailureExplanation = tt.explanation
			f.session.Error = tt.errMsg

			err := f.hooks.OnSessionComplete(context.Background(), f.session, models.SessionStatusFailed)

			require.NoError(t, err, "promotion should succeed")
			require.Len(t, f.runs.calls, 1, "a promoted run takes only the running->pending write")
			require.Equal(t, models.AutomationRunStatusPending, f.runs.calls[0].toStatus,
				"a capacity marker in either field must send the run back to pending")
			require.Len(t, f.jobs.calls, 1, "the run must be re-dispatched onto the next rank")
		})
	}
}
