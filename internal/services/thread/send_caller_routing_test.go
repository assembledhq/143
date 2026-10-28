package thread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func callerRoutingInput(t *testing.T, purpose CallerSendPurpose, source models.SessionMessageSource) SendMessageInput {
	t.Helper()
	command, err := NewCallerSendCommand(purpose, uuid.New(), SendMessageInput{
		OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "synthetic retained startup command", MessageSource: source,
	})
	require.NoError(t, err, "freeze a complete valid caller command before invoking the real service router")
	input := command.SendInput()
	input.CallerCommand, err = command.Proof()
	require.NoError(t, err, "attach the exact opaque proof without rebuilding its semantic input")
	return input
}

func TestSendMessageCallerProofActiveRefusalPrecedesReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		source  models.SessionMessageSource
	}{
		{name: "ordinary-looking reviewer command", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "review-source reviewer command", purpose: CallerSendCodeReviewReviewerDispatch, source: models.SessionMessageSourceCodeReview},
		{name: "ordinary-looking initial loop command", purpose: CallerSendReviewLoopInitialSend},
		{name: "review-source initial loop command", purpose: CallerSendReviewLoopInitialSend, source: models.SessionMessageSourceCodeReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerRoutingInput(t, tt.purpose, tt.source)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated strict SQL trace for the active refusal boundary")
			defer mock.Close()
			mock.ExpectBegin()
			expectActiveSendMode(mock, input)
			mock.ExpectRollback()
			service, effects := newActiveSendService(t, mock)
			units := 0
			runner := service.send.Transactions
			service.send.Transactions = sendTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				units++
				return runner.Run(ctx, work)
			})
			result, err := service.SendMessage(context.Background(), input)
			require.EqualError(t, err, "caller command requires legacy thread send", "a stamped active call must have one fixed known refusal before replay, preparation errors or fallback")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "a known successfully rolled-back active refusal must not pretend that a send was attempted")
			require.Nil(t, result, "unsupported stamped active mode cannot expose an accepted or replayed receipt")
			require.Equal(t, 1, units, "the sole router must stop after its first observation without rerouting or opening a mutation unit")
			require.Empty(t, effects.calls, "refused correlation transport cannot publish, enqueue or notify")
			require.NoError(t, mock.ExpectationsWereMet(), "only mode observation and rollback may execute; even a replay lookup is forbidden")
		})
	}
}

func TestSendMessageCallerProofMismatchRefusesBeforeDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SendMessageInput)
	}{
		{name: "zero proof", mutate: func(input *SendMessageInput) { input.CallerCommand = &CallerSendCommandProof{} }},
		{name: "different transport version", mutate: func(input *SendMessageInput) { input.CallerCommand.version = 2 }},
		{name: "different purpose", mutate: func(input *SendMessageInput) { input.CallerCommand.purpose = CallerSendReviewLoopInitialSend }},
		{name: "different owner", mutate: func(input *SendMessageInput) { input.CallerCommand.ownerID = uuid.New() }},
		{name: "different digest", mutate: func(input *SendMessageInput) { input.CallerCommand.digest = strings.Repeat("f", 64) }},
		{name: "missing actual key", mutate: func(input *SendMessageInput) { input.ClientMessageID = "" }},
		{name: "changed actual message", mutate: func(input *SendMessageInput) { input.Message += " changed" }},
		{name: "changed actual scope", mutate: func(input *SendMessageInput) { input.OrgID = uuid.New() }},
		{name: "unsupported actual target policy", mutate: func(input *SendMessageInput) { input.LegacyChangesetPolicy = LegacyChangesetRequireMaterialized }},
		{name: "malformed actual text", mutate: func(input *SendMessageInput) { input.Message = "\x00" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerRoutingInput(t, CallerSendCodeReviewReviewerDispatch, "")
			tt.mutate(&input)
			service, _ := newTestService(t)
			units := 0
			service.send.Transactions = sendTransactionsFunc(func(context.Context, func(context.Context, pgx.Tx) error) error {
				units++
				return errors.New("unexpected send transaction for invalid caller proof")
			})
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, ErrCallerCommandMismatch, "the real ingress must validate the supplied proof before dropping, reconstructing or routing it")
			require.EqualError(t, err, ErrCallerCommandMismatch.Error(), "the refusal must not expose the command, scope, key or digest")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "pure prewrite validation cannot imply an uncertain send attempt")
			require.Nil(t, result, "invalid transport cannot return an accepted message")
			require.Zero(t, units, "invalid proof must stop before every observation, replay and mutation transaction")
		})
	}
}
