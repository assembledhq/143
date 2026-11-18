package worker

import (
	"context"
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
)

// TestRegisterConcurrencyLimitDeadLetter proves the continue_session quota
// dead-letter hook settles the claimed session and its thread with the
// concurrency-timeout failure, and stands down for a per-target automation
// turn, whose completer settles the run instead.
func TestRegisterConcurrencyLimitDeadLetter(t *testing.T) {
	t.Parallel()

	t.Run("marks the session and thread failed with the concurrency timeout category", func(t *testing.T) {
		t.Parallel()
		stores, mock := newTestStores(t)
		t.Cleanup(mock.Close)
		// No milestone or Slack fan-out stores: only the session and thread settle.
		stores.Jobs = nil
		stores.SessionThreads = db.NewSessionThreadStore(mock)

		orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
		now := time.Now()
		session := models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}
		errMsg := concurrencyLimitErrorMessage
		category := agent.FailureCategoryConcurrencyTimeout
		nextSteps := []string{
			"Wait for other running sessions to complete, then retry",
			"Cancel sessions that are no longer needed to free up capacity",
			"Raise the concurrent agent runs limit in the runtime settings if the organization needs more capacity",
		}

		failedRow := newWorkerSessionRow(sessionID, orgID, now, nil)
		setWorkerSessionColumnValue(failedRow, "status", models.SessionStatusFailed)
		// UpdateResult: status, token_usage, model_used, result_summary, diff,
		// error, failure_explanation, failure_category, failure_next_steps,
		// failure_retry_advised, base_commit_sha, diff_collected_at,
		// diff_stats, id, org_id.
		mock.ExpectQuery(`UPDATE sessions[\s\S]+SET status = @status`).
			WithArgs(string(models.SessionStatusFailed), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				&errMsg, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), sessionID, orgID).
			WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(failedRow...))
		// UpdateFailure: failure_explanation, failure_category,
		// failure_next_steps, failure_retry_advised, id, org_id.
		mock.ExpectExec(`UPDATE sessions[\s\S]+SET failure_explanation = @failure_explanation`).
			WithArgs(concurrencyLimitFailureExplanation, category, nextSteps, true, sessionID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		// Thread UpdateResult: status, result_summary, diff,
		// failure_explanation, failure_category, id, org_id.
		mock.ExpectExec(`UPDATE session_threads[\s\S]+SET status = @status`).
			WithArgs(models.ThreadStatusFailed, (*string)(nil), (*string)(nil), &errMsg, &category, threadID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		ctx := jobctx.WithDeadLetterHooks(context.Background())
		registerConcurrencyLimitDeadLetter(ctx, stores, &Services{}, zerolog.Nop(), session, &threadID, "continue_session")

		jobctx.RunDeadLetterHooks(ctx, errors.New("concurrency limit reached: 1/1 runs active"))
		require.NoError(t, mock.ExpectationsWereMet(), "the hook should fail the session and its thread with the concurrency timeout category")
	})

	t.Run("stands down for a per-target automation turn", func(t *testing.T) {
		t.Parallel()
		stores, mock := newTestStores(t)
		t.Cleanup(mock.Close)
		stores.SessionThreads = db.NewSessionThreadStore(mock)
		threadID := uuid.New()
		turn := &agent.AutomationTurnContinueOptions{RunID: uuid.New()}
		ctx := agent.WithAutomationTurn(jobctx.WithDeadLetterHooks(context.Background()), turn)
		session := models.Session{ID: uuid.New(), OrgID: uuid.New(), Status: models.SessionStatusRunning}

		registerConcurrencyLimitDeadLetter(ctx, stores, &Services{}, zerolog.Nop(), session, &threadID, "continue_session")
		jobctx.RunDeadLetterHooks(ctx, errors.New("exhausted"))

		require.NoError(t, mock.ExpectationsWereMet(), "an automation-owned session is settled by its completer, not marked failed")
	})
}
