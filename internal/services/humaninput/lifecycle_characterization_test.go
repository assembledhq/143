package humaninput

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The current readiness check is session-wide. These tests describe that gap;
// they do not certify a checkpoint for the request's turn or generation.
func TestHumanInputLifecycleCharacterization_CheckpointScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		cancel          bool
		checkpoint      bool
		expectedStatus  models.HumanInputRequestStatus
		expectedText    string
		expectedPayload json.RawMessage
	}{
		{name: "answer rejects missing session checkpoint"},
		{name: "cancel rejects missing session checkpoint", cancel: true},
		{name: "answer accepts session checkpoint despite request turn mismatch", checkpoint: true, expectedStatus: models.HumanInputRequestStatusAnswered, expectedText: "Use transactions.", expectedPayload: json.RawMessage(`{"answer_text":"Use transactions."}`)},
		{name: "cancel accepts session checkpoint despite request turn mismatch", checkpoint: true, cancel: true, expectedStatus: models.HumanInputRequestStatusCancelled, expectedText: "Cancelled human input request.", expectedPayload: json.RawMessage(`{"cancelled":true,"decision":"deny"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newRecoveryRepository(t)
			repo.request.TurnNumber = 100
			if !tt.checkpoint {
				repo.session.SnapshotKey = nil
			}
			originalRequest := repo.request
			userID := uuid.New()
			service := New(repo)
			var actual Result
			var err error
			operation := "answer"
			if tt.cancel {
				operation = "cancel"
				actual, err = service.Cancel(context.Background(), CancelInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: userID})
			} else {
				text := "Use transactions."
				actual, err = service.Answer(context.Background(), AnswerInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: userID, Answer: models.HumanInputAnswerInput{AnswerText: &text}})
			}
			if !tt.checkpoint {
				require.ErrorIs(t, err, ErrCheckpointPending, "both structured answer and cancellation should wait for the session checkpoint")
				require.Equal(t, Result{}, actual, "checkpoint rejection should expose no accepted result")
				require.Equal(t, originalRequest, repo.request, "checkpoint rejection should leave the pending input unchanged")
				require.Equal(t, []string{"begin", "session", "request"}, repo.calls, "checkpoint rejection should stop before answer, claim, message, job, or commit")
				require.Equal(t, uuid.Nil, repo.notifiedJobID, "checkpoint rejection must not wake an execution")
				return
			}
			require.NoError(t, err, "the baseline accepts an available session checkpoint without request-turn proof")
			require.NotNil(t, actual.Message, "accepted provider input should persist a user message")
			expectedRequest := originalRequest
			expectedRequest.Status = tt.expectedStatus
			expectedRequest.AnswerText = &tt.expectedText
			expectedRequest.AnswerPayload = tt.expectedPayload
			expectedRequest.AnsweredBy = &userID
			// The shared fixture supplies wall-clock timestamps. Preserve
			// those values while comparing every other returned field exactly.
			expectedRequest.AnsweredAt = actual.Request.AnsweredAt
			expected := Result{Request: expectedRequest, Message: &models.SessionMessage{ID: 42, SessionID: repo.session.ID, OrgID: repo.session.OrgID, ThreadID: originalRequest.ThreadID, UserID: &userID, TurnNumber: 9, Role: models.MessageRoleUser, Content: tt.expectedText, CreatedAt: actual.Message.CreatedAt}, JobID: repo.jobID}
			require.Equal(t, expected, actual, "the baseline should bind the exact request but select the resumed thread's next turn")
			require.Equal(t, []string{"begin", "session", "request", operation, "compatibility", "claim session", "claim thread", "message", "enqueue", "commit", "notify"}, repo.calls, "accepted provider input should notify only after the owned transaction commits")
			require.Equal(t, originalRequest.ID, repo.enqueuedHumanInputRequestID, "the provider continuation should retain the answered or denied request identity")
			require.Equal(t, repo.jobID, repo.notifiedJobID, "only the committed provider continuation should be notified")
		})
	}
}
