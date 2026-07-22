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
