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
		Level:      level,
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
