package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	automationservice "github.com/assembledhq/143/internal/services/automations"
)

type fakeAutomationTurnCompleter struct {
	complete    automationservice.CompletionResult
	completeErr error
	completes   []completeCall
	recover     models.AutomationRunOutcomeReason
	recoverErr  error
	recovers    []uuid.UUID
	wake        automationservice.WakeOutcome
	wakeErr     error
	wakes       []uuid.UUID
}

type completeCall struct {
	runID, jobID, lockToken uuid.UUID
}

func (f *fakeAutomationTurnCompleter) Complete(_ context.Context, _, runID, jobID, lockToken uuid.UUID) (automationservice.CompletionResult, error) {
	f.completes = append(f.completes, completeCall{runID: runID, jobID: jobID, lockToken: lockToken})
	return f.complete, f.completeErr
}

func (f *fakeAutomationTurnCompleter) RecoverTerminalJob(_ context.Context, _, runID, _ uuid.UUID) (models.AutomationRunOutcomeReason, error) {
	f.recovers = append(f.recovers, runID)
	return f.recover, f.recoverErr
}

func (f *fakeAutomationTurnCompleter) Wake(_ context.Context, _, targetID uuid.UUID) (automationservice.WakeOutcome, error) {
	f.wakes = append(f.wakes, targetID)
	return f.wake, f.wakeErr
}

func TestAutomationTargetWakeHandler(t *testing.T) {
	t.Parallel()
	nudged := uuid.New()
	tests := []struct {
		name       string
		completer  *fakeAutomationTurnCompleter
		wantErr    bool
		wantRetry  bool
		wantWakes  int
		badPayload bool
	}{
		{name: "a nudged waiter finishes the job", completer: &fakeAutomationTurnCompleter{wake: automationservice.WakeOutcome{Nudged: &nudged}}, wantWakes: 1},
		{name: "nothing waiting finishes the job", completer: &fakeAutomationTurnCompleter{}, wantWakes: 1},
		{name: "a newer request re-runs the wake", completer: &fakeAutomationTurnCompleter{wake: automationservice.WakeOutcome{Requeue: true}}, wantWakes: 1, wantRetry: true},
		{name: "a missing target finishes the job", completer: &fakeAutomationTurnCompleter{wakeErr: db.ErrAutomationTargetNotFound}, wantWakes: 1},
		{name: "a wake error surfaces for the job's retry", completer: &fakeAutomationTurnCompleter{wakeErr: errors.New("boom")}, wantWakes: 1, wantErr: true},
		{name: "no completer is a no-op", completer: nil},
		{name: "a malformed payload fails", completer: &fakeAutomationTurnCompleter{}, badPayload: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			targetID := uuid.New()
			payload, err := json.Marshal(automationservice.AutomationTargetWakePayload{OrgID: orgID.String(), TargetID: targetID.String()})
			require.NoError(t, err, "marshal payload")
			if tt.badPayload {
				payload = []byte(`{"org_id":"nope"}`)
			}
			services := &Services{}
			if tt.completer != nil {
				services.AutomationTurns = tt.completer
			}
			handler := newAutomationTargetWakeHandler(services, zerolog.Nop())
			err = handler(context.Background(), models.JobTypeAutomationTargetWake, payload)
			var retryable *RetryableError
			switch {
			case tt.wantRetry:
				require.ErrorAs(t, err, &retryable, "a newer request is a retry")
				require.NotNil(t, retryable.RetryAfter, "the retry is prompt")
				require.Equal(t, time.Second, *retryable.RetryAfter, "one second")
			case tt.wantErr:
				require.Error(t, err, "expected an error")
				require.False(t, errors.As(err, &retryable), "a plain error, not a scheduled retry")
			default:
				require.NoError(t, err, "the job finishes")
			}
			if tt.completer != nil {
				require.Len(t, tt.completer.wakes, tt.wantWakes, "wake calls")
				if tt.wantWakes > 0 {
					require.Equal(t, targetID, tt.completer.wakes[0], "the payload's target is woken")
				}
			}
		})
	}
}

// TestClaimAutomationTurnAttempt_MarkerCompletesOnRetry proves a retry that
// finds a result marker for the run's current attempt completes the run
// under the live lease and does not claim a new attempt.
func TestClaimAutomationTurnAttempt_MarkerCompletesOnRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		completer   *fakeAutomationTurnCompleter
		wantOwned   bool
		wantErr     bool
		wantRetry   bool
		wantComplet int
	}{
		{name: "the marker completes the run and skips the turn", completer: &fakeAutomationTurnCompleter{complete: automationservice.CompletionResult{Applied: true, Outcome: models.AutomationRunOutcomeTurnCompleted}}, wantComplet: 1},
		{name: "an already completed marker still skips the turn", completer: &fakeAutomationTurnCompleter{}, wantComplet: 1},
		{name: "a completion failure retries the job", completer: &fakeAutomationTurnCompleter{completeErr: errors.New("db down")}, wantComplet: 1, wantErr: true, wantRetry: true},
		{name: "no completer is a configuration error", completer: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.AutomationRuns = db.NewAutomationRunStore(mock)
			stores.ThreadSendTx = mock
			orgID := uuid.New()
			runID := uuid.New()
			jobID := uuid.New()
			lockToken := uuid.New()
			targetID := uuid.New()
			now := time.Now()
			executing := string(models.AutomationRunDispatchExecuting)
			mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id AND org_id = @org_id`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(automationRunRowsWith(t, map[string]any{
					"id": runID, "automation_id": uuid.New(), "org_id": orgID, "triggered_at": now, "triggered_by": models.AutomationTriggeredByGitHub,
					"config_snapshot": []byte("{}"), "goal_snapshot": "goal", "status": models.AutomationRunStatusRunning, "created_at": now, "updated_at": now,
					"target_id": &targetID, "dispatch_state": &executing, "job_id": &jobID, "attempt": 1,
				}))
			mock.ExpectQuery(`SELECT EXISTS \(\s+SELECT 1 FROM automation_runs r\s+JOIN automation_run_results m`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), lockToken)
			services := &Services{}
			if tt.completer != nil {
				services.AutomationTurns = tt.completer
			}
			owned, err := claimAutomationTurnAttempt(ctx, stores, services, zerolog.Nop(), orgID, runID)
			require.Equal(t, tt.wantOwned, owned, "ownership")
			var retryable *RetryableError
			switch {
			case tt.wantRetry:
				require.ErrorAs(t, err, &retryable, "completion failures retry")
				require.True(t, retryable.BypassMaxRetryDuration, "the retry bypasses the age window: the marker is durable")
			case tt.wantErr:
				require.Error(t, err, "expected an error")
			default:
				require.NoError(t, err, "no error")
			}
			if tt.completer != nil {
				require.Len(t, tt.completer.completes, tt.wantComplet, "completion calls")
				if tt.wantComplet > 0 {
					require.Equal(t, completeCall{runID: runID, jobID: jobID, lockToken: lockToken}, tt.completer.completes[0], "completed under this job's lease")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "no attempt claim transaction was opened")
		})
	}
}

// TestCompleteAutomationTurn proves the post-orchestrator completion call:
// applied completions report done, failures become prompt retries, and a
// missing lease or completer is a no-op.
func TestCompleteAutomationTurn(t *testing.T) {
	t.Parallel()
	jobID := uuid.New()
	lockToken := uuid.New()
	leased := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), lockToken)
	tests := []struct {
		name      string
		ctx       context.Context
		completer *fakeAutomationTurnCompleter
		wantDone  bool
		wantRetry bool
		wantCalls int
	}{
		{name: "an applied completion is done", ctx: leased, completer: &fakeAutomationTurnCompleter{complete: automationservice.CompletionResult{Applied: true}}, wantDone: true, wantCalls: 1},
		{name: "no marker is not done", ctx: leased, completer: &fakeAutomationTurnCompleter{}, wantCalls: 1},
		{name: "a completion failure retries promptly", ctx: leased, completer: &fakeAutomationTurnCompleter{completeErr: errors.New("boom")}, wantRetry: true, wantCalls: 1},
		{name: "no lease on the context is a no-op", ctx: context.Background(), completer: &fakeAutomationTurnCompleter{}},
		{name: "no completer is a no-op", ctx: leased},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			services := &Services{}
			if tt.completer != nil {
				services.AutomationTurns = tt.completer
			}
			done, err := completeAutomationTurn(tt.ctx, services, zerolog.Nop(), uuid.New(), uuid.New())
			require.Equal(t, tt.wantDone, done, "done")
			if tt.wantRetry {
				var retryable *RetryableError
				require.ErrorAs(t, err, &retryable, "retryable")
				require.True(t, retryable.BypassMaxRetryDuration, "bypasses the age window")
			} else {
				require.NoError(t, err, "no error")
			}
			if tt.completer != nil {
				require.Len(t, tt.completer.completes, tt.wantCalls, "completion calls")
				if tt.wantCalls > 0 {
					require.Equal(t, jobID, tt.completer.completes[0].jobID, "the context's job")
					require.Equal(t, lockToken, tt.completer.completes[0].lockToken, "the context's lease")
				}
			}
		})
	}
}

// TestAutomationTurnDeadLetterHooks proves the per-target dead-letter hook
// settles the run and that the session-failure hooks stand down for a
// per-target turn.
func TestAutomationTurnDeadLetterHooks(t *testing.T) {
	t.Parallel()
	t.Run("the turn hook recovers the run", func(t *testing.T) {
		t.Parallel()
		completer := &fakeAutomationTurnCompleter{recover: models.AutomationRunOutcomeRetriesExhausted}
		runID := uuid.New()
		ctx := jobctx.WithDeadLetterHooks(jobctx.WithJobID(context.Background(), uuid.New()))
		registerAutomationTurnDeadLetter(ctx, &Services{AutomationTurns: completer}, zerolog.Nop(), uuid.New(), runID)
		jobctx.RunDeadLetterHooks(ctx, errors.New("exhausted"))
		require.Equal(t, []uuid.UUID{runID}, completer.recovers, "the run is settled on dead-letter")
	})
	t.Run("session failure hooks stand down for a per-target turn", func(t *testing.T) {
		t.Parallel()
		stores, mock := newTestStores(t)
		defer mock.Close()
		turn := &agent.AutomationTurnContinueOptions{RunID: uuid.New()}
		ctx := agent.WithAutomationTurn(jobctx.WithDeadLetterHooks(context.Background()), turn)
		session := models.Session{ID: uuid.New(), OrgID: uuid.New()}
		registerSystemInterruptDeadLetter(ctx, stores, &Services{}, zerolog.Nop(), session, nil, "run_agent")
		registerSandboxCapacityDeadLetter(ctx, stores, &Services{}, zerolog.Nop(), session, nil, "run_agent")
		registerStaleSandboxDeadLetter(ctx, stores, zerolog.Nop(), session, nil, "run_agent")
		jobctx.RunDeadLetterHooks(ctx, errors.New("exhausted"))
		require.NoError(t, mock.ExpectationsWereMet(), "no session write happens for an owned session")
	})
}

// automationRunRowsWith builds one automation_runs row from named columns,
// leaving every other column null.
func automationRunRowsWith(t *testing.T, values map[string]any) *pgxmock.Rows {
	t.Helper()
	cols := automationRunRowColumns()
	row := make([]any, len(cols))
	seen := make(map[string]bool, len(values))
	for i, col := range cols {
		if v, ok := values[col]; ok {
			row[i] = v
			seen[col] = true
		}
	}
	for col := range values {
		require.True(t, seen[col], "column %q is not an automation_runs column", col)
	}
	return pgxmock.NewRows(cols).AddRow(row...)
}
