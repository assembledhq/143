package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type testInternalSessionLogStore struct {
	logs             []models.SessionLog
	markedThreadID   *uuid.UUID
	markedOrgID      uuid.UUID
	markedSessionID  uuid.UUID
	markedTurnNumber int
	markedMessage    string
}

func (s *testInternalSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	s.logs = append(s.logs, *log)
	return nil
}

func (s *testInternalSessionLogStore) MarkAssistantTranscriptDuplicate(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	threadID *uuid.UUID,
	turnNumber int,
	message string,
) error {
	s.markedOrgID = orgID
	s.markedSessionID = sessionID
	if threadID != nil {
		copied := *threadID
		s.markedThreadID = &copied
	}
	s.markedTurnNumber = turnNumber
	s.markedMessage = message
	return nil
}

type testInternalSessionMessageStore struct {
	messages []models.SessionMessage
}

func (s *testInternalSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	s.messages = append(s.messages, *msg)
	return nil
}

func (s *testInternalSessionMessageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error) {
	return nil, nil
}

func TestCreateAssistantMessage_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		agentRunLogs:    logs,
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, &threadID, 4, &AgentResult{
		Summary: "Final answer",
	})
	require.NoError(t, err, "createAssistantMessage should persist the assistant transcript")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.NotNil(t, messages.messages[0].ThreadID, "assistant message should preserve the thread id")
	require.Equal(t, threadID, *messages.messages[0].ThreadID, "assistant message should use the provided thread id")
	require.NotNil(t, logs.markedThreadID, "duplicate marker should preserve the thread id")
	require.Equal(t, threadID, *logs.markedThreadID, "duplicate marker should use the provided thread id")
	require.Equal(t, 4, logs.markedTurnNumber, "duplicate marker should use the provided turn number")
	require.Equal(t, "Final answer", logs.markedMessage, "duplicate marker should target the assistant summary")
}

func TestStreamLogs_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "streamed message",
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, &threadID, 2, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should persist the log entry")
	require.NotNil(t, logs.logs[0].ThreadID, "persisted log should preserve the thread id")
	require.Equal(t, threadID, *logs.logs[0].ThreadID, "persisted log should use the provided thread id")
	require.Equal(t, 2, logs.logs[0].TurnNumber, "persisted log should keep the turn number")
	require.Equal(t, "streamed message", logs.logs[0].Message, "persisted log should keep the message content")
}
