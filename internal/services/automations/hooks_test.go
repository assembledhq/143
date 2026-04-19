package automations

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeAutomationRunStore struct {
	calls []fakeUpdateCall
	err   error
}

type fakeUpdateCall struct {
	orgID         uuid.UUID
	runID         uuid.UUID
	status        string
	completedAt   *time.Time
	resultSummary *string
}

func (f *fakeAutomationRunStore) UpdateStatus(_ context.Context, orgID, runID uuid.UUID, status string, completedAt *time.Time, resultSummary *string) error {
	f.calls = append(f.calls, fakeUpdateCall{
		orgID:         orgID,
		runID:         runID,
		status:        status,
		completedAt:   completedAt,
		resultSummary: resultSummary,
	})
	return f.err
}

func TestAutomationHooks_OnSessionComplete_NoAutomationRunID_NoOp(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{}
	h := NewAutomationHooks(store, zerolog.Nop())

	session := &models.Session{OrgID: uuid.New()}
	err := h.OnSessionComplete(context.Background(), session, "completed")
	require.NoError(t, err)
	require.Empty(t, store.calls, "no update should fire when session has no automation_run_id")
}

func TestAutomationHooks_OnSessionComplete_CompletedMaps(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	orgID := uuid.New()
	summary := "wrote tests, opened PR"
	session := &models.Session{
		OrgID:           orgID,
		AutomationRunID: &runID,
		ResultSummary:   &summary,
	}

	err := h.OnSessionComplete(context.Background(), session, "completed")
	require.NoError(t, err)
	require.Len(t, store.calls, 1)
	call := store.calls[0]
	require.Equal(t, orgID, call.orgID)
	require.Equal(t, runID, call.runID)
	require.Equal(t, models.AutomationRunStatusCompleted, call.status)
	require.NotNil(t, call.completedAt)
	require.NotNil(t, call.resultSummary)
	require.Equal(t, summary, *call.resultSummary)
}

func TestAutomationHooks_OnSessionComplete_FailedPrefersErrorWhenNoSummary(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	errMsg := "agent hit rate limit"
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
		Error:           &errMsg,
	}

	err := h.OnSessionComplete(context.Background(), session, "failed")
	require.NoError(t, err)
	require.Len(t, store.calls, 1)
	require.Equal(t, models.AutomationRunStatusFailed, store.calls[0].status)
	require.Equal(t, errMsg, *store.calls[0].resultSummary)
}

func TestAutomationHooks_OnSessionComplete_IgnoresNonTerminal(t *testing.T) {
	t.Parallel()

	store := &fakeAutomationRunStore{}
	h := NewAutomationHooks(store, zerolog.Nop())

	runID := uuid.New()
	session := &models.Session{
		OrgID:           uuid.New(),
		AutomationRunID: &runID,
	}

	for _, status := range []string{"running", "awaiting_input", "cancelled", "pending"} {
		err := h.OnSessionComplete(context.Background(), session, status)
		require.NoError(t, err, "status %q should no-op", status)
	}
	require.Empty(t, store.calls, "non-terminal statuses must not touch the run row")
}
