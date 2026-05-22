package sessiontimeline

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func makeMessage(t *testing.T, overrides func(*models.SessionMessage), createdAt string) models.SessionMessage {
	t.Helper()
	msg := models.SessionMessage{
		ID:         1,
		SessionID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		OrgID:      uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		TurnNumber: 1,
		Role:       models.MessageRoleAssistant,
		Content:    "assistant reply",
		CreatedAt:  mustTime(t, createdAt),
	}
	if overrides != nil {
		overrides(&msg)
	}
	return msg
}

func makeLog(t *testing.T, overrides func(*models.SessionLog), createdAt string, level, message string) models.SessionLog {
	t.Helper()
	log := models.SessionLog{
		ID:         1,
		SessionID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		OrgID:      uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		TurnNumber: 1,
		Level:      models.SessionLogLevel(level),
		Message:    message,
		Timestamp:  mustTime(t, createdAt),
	}
	if overrides != nil {
		overrides(&log)
	}
	return log
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err, "test timestamp should parse")
	return parsed
}

func TestComposeTimeline_SuppressesExactDuplicateAssistantOutput(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, nil, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, nil, "2026-01-01T00:00:01Z", "output", "progress update"),
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 2
			log.Metadata = []byte(`{"type":"assistant_final","duplicate_of_transcript":true}`)
		}, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 2, "duplicate final output log should be suppressed in favor of the assistant message")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "non-duplicate progress output should remain visible")
	require.Equal(t, models.SessionTimelineKindMessage, result[1].Kind, "assistant transcript message should remain visible")
}

func TestComposeTimeline_KeepsDifferentAssistantOutputAndMessage(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, nil, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, nil, "2026-01-01T00:00:01Z", "output", "different visible output"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 2, "non-matching output logs should still be rendered alongside the assistant transcript")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "visible output should remain visible")
	require.Equal(t, models.SessionTimelineKindMessage, result[1].Kind, "assistant message should still be returned")
}

func TestComposeTimeline_HiddenVisibilityMetadataKeepsErrorAsLog(t *testing.T) {
	t.Parallel()

	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.Metadata = []byte(`{"visibility":"hidden","diagnostic_class":"benign_runtime_diagnostic"}`)
		}, "2026-01-01T00:00:01Z", "error", "benign diagnostic"),
	}

	result := Compose(nil, logs)
	require.Len(t, result, 1, "hidden diagnostic should remain in the timeline as a hidden log entry")
	require.Equal(t, models.SessionTimelineKindLog, result[0].Kind, "hidden diagnostic should not be returned as a user-visible error")
}

func TestComposeTimeline_DedupesLegacyRowsWithoutMetadata(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, nil, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, nil, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 1, "exact duplicate output logs without metadata should still be suppressed")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "assistant message should win for transcript rendering")
}

// Pins the legacy turn-0 fallback in duplicateTranscriptLogIDs: pre-fix
// initial-run logs were tagged with turn 0 while the matching assistant
// transcript message uses turn 1. The shim still dedupes those historical
// rows. Removing the fallback should fail this test.
func TestComposeTimeline_DedupesLegacyTurnZeroInitialRunLogAgainstTurnOneTranscript(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.TurnNumber = 1
		}, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.TurnNumber = 0
		}, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 1, "legacy turn-0 initial-run log should dedupe against the turn-1 assistant transcript")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "assistant transcript should remain visible after legacy log dedupe")
}

func TestComposeTimeline_SuppressesDuplicateAssistantOutputWithWhitespaceDifferences(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.Content = "assistant reply\n"
		}, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, nil, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 1, "trivial trailing whitespace differences should not duplicate the final assistant output")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "assistant transcript should remain visible after whitespace-normalized dedupe")
}

func TestComposeTimeline_KeepsAssistantOutputWhenDifferenceIsLeadingIndentation(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.Content = "  assistant reply"
		}, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, nil, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 2, "leading indentation should remain part of transcript identity")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "output log should remain visible when indentation differs")
	require.Equal(t, models.SessionTimelineKindMessage, result[1].Kind, "assistant transcript should remain visible when indentation differs")
}

func TestComposeTimeline_PrefersMarkedDuplicateEvenWithMultipleOutputs(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, nil, "2026-01-01T00:00:04Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
		}, "2026-01-01T00:00:01Z", "output", "first progress update"),
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 2
		}, "2026-01-01T00:00:02Z", "output", "second progress update"),
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 3
			log.Metadata = []byte(`{"type":"assistant_final","duplicate_of_transcript":true}`)
		}, "2026-01-01T00:00:03Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 3, "only the metadata-marked duplicate should be removed")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "first progress update should remain")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[1].Kind, "second progress update should remain")
	require.Equal(t, models.SessionTimelineKindMessage, result[2].Kind, "assistant transcript should remain")
}

func TestComposeTimeline_PairsToolUseAndToolResult(t *testing.T) {
	t.Parallel()

	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
			log.Metadata = []byte(`{"tool":"Read"}`)
		}, "2026-01-01T00:00:01Z", "tool_use", "using tool: Read"),
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 2
			log.Metadata = []byte(`{"type":"tool_result"}`)
		}, "2026-01-01T00:00:02Z", "output", "file contents"),
	}

	result := Compose(nil, logs)
	require.Len(t, result, 1, "tool use and tool result should compose into a single group entry")
	require.Equal(t, models.SessionTimelineKindToolGroup, result[0].Kind, "paired tool logs should produce a tool_group entry")
	require.NotNil(t, result[0].ToolUse, "tool_group should include the tool_use log")
	require.NotNil(t, result[0].ToolResult, "tool_group should include the tool_result log")
}

func TestComposeTimeline_EmitsPlanEntries(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.ID = 1
			msg.Role = models.MessageRoleUser
			msg.Content = "[PLAN_MODE]\nPlease plan this"
			msg.CreatedAt = mustTime(t, "2026-01-01T00:00:01Z")
		}, "2026-01-01T00:00:01Z"),
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.ID = 2
			msg.Content = "Plan message"
			msg.CreatedAt = mustTime(t, "2026-01-01T00:00:03Z")
		}, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
			log.Message = "Plan output"
		}, "2026-01-01T00:00:02Z", "output", "Plan output"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 3, "plan mode turn should include the user prompt plus plan output and final plan message")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "user message should remain a normal message entry")
	require.Equal(t, models.SessionTimelineKindPlanOutput, result[1].Kind, "plan output log should be marked for plan rendering")
	require.Equal(t, models.SessionTimelineKindPlanMessage, result[2].Kind, "assistant transcript should be marked as a plan message")
}

func TestComposeTimeline_EmitsStandaloneToolUseGroup(t *testing.T) {
	t.Parallel()

	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
			log.Metadata = []byte(`{"tool":"Read"}`)
		}, "2026-01-01T00:00:01Z", "tool_use", "using tool: Read"),
	}

	result := Compose(nil, logs)
	require.Len(t, result, 1, "standalone tool_use logs should still produce a timeline entry")
	require.Equal(t, models.SessionTimelineKindToolGroup, result[0].Kind, "tool_use logs should render as tool groups")
	require.NotNil(t, result[0].ToolUse, "tool group should include the tool_use payload")
	require.Nil(t, result[0].ToolResult, "tool group should not invent a tool result")
}

func TestComposeTimeline_EmitsErrorAndGenericLogEntries(t *testing.T) {
	t.Parallel()

	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
		}, "2026-01-01T00:00:01Z", "error", "something failed"),
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 2
			log.Level = "debug"
		}, "2026-01-01T00:00:02Z", "debug", "hidden detail"),
	}

	result := Compose(nil, logs)
	require.Len(t, result, 2, "error and non-visible logs should both be returned")
	require.Equal(t, models.SessionTimelineKindError, result[0].Kind, "error logs should render as error entries")
	require.Equal(t, models.SessionTimelineKindLog, result[1].Kind, "non-visible logs should render as generic log entries")
}

func TestComposeTimeline_IncludesHumanInputRequests(t *testing.T) {
	t.Parallel()

	requestID := uuid.New()
	request := models.HumanInputRequest{
		ID:        requestID,
		SessionID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		OrgID:     uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Kind:      models.HumanInputRequestKindActionChoice,
		Status:    models.HumanInputRequestStatusAnswered,
		Title:     "Choose next action",
		Body:      "What should the agent do next?",
		CreatedAt: mustTime(t, "2026-01-01T00:00:02Z"),
	}
	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.ID = 10
			msg.CreatedAt = mustTime(t, "2026-01-01T00:00:01Z")
		}, "2026-01-01T00:00:01Z"),
	}

	result := Compose(messages, nil, []models.HumanInputRequest{request})

	require.Len(t, result, 2, "human input requests should compose into timeline entries")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "message should remain sorted before human input")
	require.Equal(t, models.SessionTimelineKindHumanInput, result[1].Kind, "human input should get a durable timeline entry")
	require.NotNil(t, result[1].HumanInputRequest, "human input timeline entry should carry the request")
	require.Equal(t, requestID, result[1].HumanInputRequest.ID, "human input timeline entry should preserve request id")
}

func TestComposeTimeline_IgnoresAssistantMessagesWithoutMatchingTurnLogs(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, nil, "2026-01-01T00:00:03Z"),
	}
	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 2
			log.TurnNumber = 2
		}, "2026-01-01T00:00:02Z", "output", "assistant reply"),
	}

	result := Compose(messages, logs)
	require.Len(t, result, 2, "logs from other turns should not be suppressed")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "other-turn output should remain visible")
	require.Equal(t, models.SessionTimelineKindMessage, result[1].Kind, "assistant message should remain visible")
}

func TestComposeTimeline_SuppressesDuplicateAssistantMessagesForSameTurn(t *testing.T) {
	t.Parallel()

	messages := []models.SessionMessage{
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.ID = 1
		}, "2026-01-01T00:00:02Z"),
		makeMessage(t, func(msg *models.SessionMessage) {
			msg.ID = 2
			msg.CreatedAt = mustTime(t, "2026-01-01T00:00:03Z")
		}, "2026-01-01T00:00:03Z"),
	}

	result := Compose(messages, nil)
	require.Len(t, result, 1, "duplicate assistant transcript rows for the same turn should collapse to a single message")
	require.Equal(t, models.SessionTimelineKindMessage, result[0].Kind, "deduped assistant transcript should still render as a message")
	require.NotNil(t, result[0].Message, "message entry should include the assistant transcript payload")
	require.Equal(t, int64(1), result[0].Message.ID, "the earliest duplicate assistant transcript should be preserved")
}

func TestComposeTimeline_TreatsInvalidMetadataAsVisibleOutput(t *testing.T) {
	t.Parallel()

	logs := []models.SessionLog{
		makeLog(t, func(log *models.SessionLog) {
			log.ID = 1
			log.Metadata = []byte(`{"type":`)
		}, "2026-01-01T00:00:01Z", "output", "assistant reply"),
	}

	result := Compose(nil, logs)
	require.Len(t, result, 1, "invalid metadata should not drop the log")
	require.Equal(t, models.SessionTimelineKindAssistantOutput, result[0].Kind, "invalid metadata should fall back to visible assistant output")
}
