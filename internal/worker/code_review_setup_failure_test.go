package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewPersistedSetupFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, record string
		status       models.JobStatus
		terminal     bool
	}{
		{name: "lockfile rejection", record: codeReviewSetupFailurePrefix + "EUSAGE", status: models.JobStatusDeadLetter, terminal: true},
		{name: "engine rejection", record: codeReviewSetupFailurePrefix + "EBADENGINE", status: models.JobStatusDeadLetter, terminal: true},
		{name: "lock verification rejection", record: codeReviewSetupFailurePrefix + "ELOCKVERIFY", status: models.JobStatusDeadLetter, terminal: true},
		{name: "unknown failure", record: "network unavailable", status: models.JobStatusDeadLetter},
		{name: "authentication can be transient", record: codeReviewSetupFailurePrefix + "E401", status: models.JobStatusDeadLetter},
		{name: "untrusted suffix", record: codeReviewSetupFailurePrefix + "EUSAGE\nsynthetic-private-token", status: models.JobStatusDeadLetter},
		{name: "running attempt is not terminal", record: codeReviewSetupFailurePrefix + "EUSAGE", status: models.JobStatusRunning},
		{name: "cancelled attempt is not terminal", record: codeReviewSetupFailurePrefix + "EUSAGE", status: models.JobStatusCancelled},
		{name: "unknown record version", record: "code_review_npm_setup_failure:v2:EUSAGE", status: models.JobStatusDeadLetter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := codeReviewPersistedSetupFailure(tt.status, &tt.record)
			if !tt.terminal {
				require.NoError(t, err, "only an exact terminal platform record may stop a review")
				return
			}
			var fatal *FatalError
			require.ErrorAs(t, err, &fatal, "deterministic inputs must fail without consuming automatic retries")
			require.EqualError(t, err, tt.record, "the durable record must round-trip without raw diagnostics")
			code, message, retryable := codeReviewTerminalFailureStatus(err)
			require.Equal(t, models.CodeReviewStatusCodeWorkerFailed, code, "setup errors are not GitHub errors")
			require.Equal(t, "Workspace dependency setup rejected the package manifests, lockfile, or runtime requirements. Fix the npm setup inputs, then request a new review. Review did not run.", message, "the review must explain the corrective action safely")
			require.False(t, retryable, "unchanged deterministic inputs cannot offer automatic recovery")
		})
	}
}

func TestWorkerPreparationFailureDeadLettersFirstAttempt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		joinCleanup bool
	}{
		{name: "lockfile rejection"},
		{name: "cleanup failure preserves terminal classification", joinCleanup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w, mock := newTestWorker(t)
			defer mock.Close()
			jobID, token, orgID := uuid.New(), uuid.New(), uuid.New()
			var cause error = &agent.NPMSetupError{Code: "EUSAGE", Category: "permanent"}
			if tt.joinCleanup {
				cause = errors.Join(cause, errors.New("synthetic-private-token"))
			}
			wrapped := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, cause)
			w.Register(models.JobTypePrepareCodeReviewWorkspace, func(context.Context, string, json.RawMessage) error {
				return codeReviewPreparationFailure(wrapped)
			})
			expectClaim(mock, jobID, orgID, models.JobTypePrepareCodeReviewWorkspace, json.RawMessage(`{}`), time.Now(), token)
			mock.ExpectExec("UPDATE jobs\\s+SET status = 'dead_letter'").
				WithArgs(codeReviewSetupFailurePrefix+"EUSAGE", jobID, token).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			w.poll(context.Background())
			require.NoError(t, mock.ExpectationsWereMet(), "the first failed attempt must dead-letter with a safe record and never retry")
		})
	}
}

func TestCodeReviewPreparationFailurePreservesTransientError(t *testing.T) {
	t.Parallel()
	cause := errors.New("temporary registry outage")
	require.ErrorIs(t, codeReviewPreparationFailure(cause), cause, "unknown failure must retain existing bounded retries")
}
