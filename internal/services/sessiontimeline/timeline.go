package sessiontimeline

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const (
	planModePrefix = "[PLAN_MODE]\n"
	timeSortLayout = "2006-01-02T15:04:05.999999999Z07:00"
)

type logMetadata struct {
	Type                  string `json:"type"`
	DuplicateOfTranscript bool   `json:"duplicate_of_transcript"`
	Visibility            string `json:"visibility"`
}

type transcriptIdentity struct {
	threadID   string
	turnNumber int
	content    string
}

func normalizeTranscriptContent(content string) string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// Compose merges session messages, logs, and durable human-input requests into
// a server-owned timeline.
func Compose(messages []models.SessionMessage, logs []models.SessionLog, humanInputs ...[]models.HumanInputRequest) []models.SessionTimelineEntry {
	messages = dedupeAssistantTranscriptMessages(messages)
	duplicateLogIDs := duplicateTranscriptLogIDs(messages, logs)

	planModeTurns := make(map[int]struct{})
	for _, msg := range messages {
		if msg.Role == models.MessageRoleUser && strings.HasPrefix(msg.Content, planModePrefix) {
			planModeTurns[msg.TurnNumber] = struct{}{}
		}
	}

	type tagged struct {
		source string
		ts     string
		msg    *models.SessionMessage
		log    *models.SessionLog
		input  *models.HumanInputRequest
	}

	humanInputCount := 0
	for _, requests := range humanInputs {
		humanInputCount += len(requests)
	}

	items := make([]tagged, 0, len(messages)+len(logs)+humanInputCount)
	for i := range messages {
		msg := messages[i]
		items = append(items, tagged{source: "message", ts: msg.CreatedAt.UTC().Format(timeSortLayout), msg: &msg})
	}
	for i := range logs {
		log := logs[i]
		if _, suppressed := duplicateLogIDs[log.ID]; suppressed {
			continue
		}
		items = append(items, tagged{source: "log", ts: log.Timestamp.UTC().Format(timeSortLayout), log: &log})
	}
	for _, requests := range humanInputs {
		for i := range requests {
			request := requests[i]
			items = append(items, tagged{source: "human_input", ts: request.CreatedAt.UTC().Format(timeSortLayout), input: &request})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ts < items[j].ts
	})

	entries := make([]models.SessionTimelineEntry, 0, len(items))
	for i := 0; i < len(items); {
		item := items[i]
		if item.source == "message" {
			entry := models.SessionTimelineEntry{
				Kind:      models.SessionTimelineKindMessage,
				CreatedAt: item.msg.CreatedAt,
				Message:   item.msg,
			}
			if item.msg.Role == models.MessageRoleAssistant {
				if _, ok := planModeTurns[item.msg.TurnNumber]; ok {
					entry.Kind = models.SessionTimelineKindPlanMessage
					turn := item.msg.TurnNumber
					entry.TurnNumber = &turn
				}
			}
			entries = append(entries, entry)
			i++
			continue
		}
		if item.source == "human_input" {
			entries = append(entries, models.SessionTimelineEntry{
				Kind:              models.SessionTimelineKindHumanInput,
				CreatedAt:         item.input.CreatedAt,
				HumanInputRequest: item.input,
			})
			i++
			continue
		}

		log := item.log
		if log.Level == "tool_use" {
			entry := models.SessionTimelineEntry{
				Kind:      models.SessionTimelineKindToolGroup,
				CreatedAt: log.Timestamp,
				ToolUse:   log,
			}
			if i+1 < len(items) && items[i+1].source == "log" {
				next := items[i+1].log
				if metadataType(next) == "tool_result" {
					entry.ToolResult = next
					entries = append(entries, entry)
					i += 2
					continue
				}
			}
			entries = append(entries, entry)
			i++
			continue
		}

		switch {
		case log.Level == "error" && !isHiddenLog(log):
			entries = append(entries, models.SessionTimelineEntry{
				Kind:      models.SessionTimelineKindError,
				CreatedAt: log.Timestamp,
				Log:       log,
			})
		case isVisibleAssistantOutput(*log):
			entry := models.SessionTimelineEntry{
				Kind:      models.SessionTimelineKindAssistantOutput,
				CreatedAt: log.Timestamp,
				Log:       log,
			}
			if _, ok := planModeTurns[log.TurnNumber]; ok {
				entry.Kind = models.SessionTimelineKindPlanOutput
				turn := log.TurnNumber
				entry.TurnNumber = &turn
			}
			entries = append(entries, entry)
		default:
			entries = append(entries, models.SessionTimelineEntry{
				Kind:      models.SessionTimelineKindLog,
				CreatedAt: log.Timestamp,
				Log:       log,
			})
		}
		i++
	}

	return entries
}

func dedupeAssistantTranscriptMessages(messages []models.SessionMessage) []models.SessionMessage {
	if len(messages) == 0 {
		return messages
	}

	deduped := make([]models.SessionMessage, 0, len(messages))
	seen := make(map[transcriptIdentity]struct{}, len(messages))

	for _, msg := range messages {
		key, ok := assistantTranscriptIdentity(msg)
		if !ok {
			deduped = append(deduped, msg)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, msg)
	}

	return deduped
}

func duplicateTranscriptLogIDs(messages []models.SessionMessage, logs []models.SessionLog) map[int64]struct{} {
	visibleByTranscript := make(map[transcriptIdentity][]models.SessionLog)
	for _, log := range logs {
		key, ok := visibleAssistantTranscriptIdentity(log)
		if !ok {
			continue
		}
		visibleByTranscript[key] = append(visibleByTranscript[key], log)
	}

	duplicateLogIDs := make(map[int64]struct{})
	for _, msg := range messages {
		key, ok := assistantTranscriptIdentity(msg)
		if !ok {
			continue
		}
		candidates := append([]models.SessionLog(nil), visibleByTranscript[key]...)
		if key.turnNumber == 1 {
			legacyInitialKey := key
			legacyInitialKey.turnNumber = 0
			candidates = append(candidates, visibleByTranscript[legacyInitialKey]...)
		}
		if len(candidates) == 0 {
			continue
		}
		markedAny := false
		for _, candidate := range candidates {
			meta := parseMetadata(candidate.Metadata)
			if meta.Type == "assistant_final" && meta.DuplicateOfTranscript {
				duplicateLogIDs[candidate.ID] = struct{}{}
				markedAny = true
			}
		}
		if markedAny {
			continue
		}
		for _, candidate := range candidates {
			duplicateLogIDs[candidate.ID] = struct{}{}
		}
	}

	return duplicateLogIDs
}

func isVisibleAssistantOutput(log models.SessionLog) bool {
	if log.Level != "output" {
		return false
	}
	meta := parseMetadata(log.Metadata)
	return meta.Type == "" || meta.Type == "assistant_final"
}

func metadataType(log *models.SessionLog) string {
	return parseMetadata(log.Metadata).Type
}

func isHiddenLog(log *models.SessionLog) bool {
	return parseMetadata(log.Metadata).Visibility == "hidden" || isRecoverableCodexRouterDiagnostic(log.Message)
}

func isRecoverableCodexRouterDiagnostic(message string) bool {
	return strings.Contains(message, "codex_core::tools::router:") &&
		(strings.Contains(message, "write_stdin failed: stdin is closed for this session") ||
			strings.Contains(message, "apply_patch verification failed: Failed to find expected lines"))
}

func parseMetadata(raw json.RawMessage) logMetadata {
	if len(raw) == 0 {
		return logMetadata{}
	}
	var meta logMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return logMetadata{}
	}
	return meta
}

func assistantTranscriptIdentity(msg models.SessionMessage) (transcriptIdentity, bool) {
	if msg.Role != models.MessageRoleAssistant {
		return transcriptIdentity{}, false
	}
	return transcriptIdentity{
		threadID:   optionalThreadID(msg.ThreadID),
		turnNumber: msg.TurnNumber,
		content:    normalizeTranscriptContent(msg.Content),
	}, true
}

func visibleAssistantTranscriptIdentity(log models.SessionLog) (transcriptIdentity, bool) {
	if !isVisibleAssistantOutput(log) {
		return transcriptIdentity{}, false
	}
	return transcriptIdentity{
		threadID:   optionalThreadID(log.ThreadID),
		turnNumber: log.TurnNumber,
		content:    normalizeTranscriptContent(log.Message),
	}, true
}

func optionalThreadID(threadID *uuid.UUID) string {
	if threadID == nil {
		return ""
	}
	return threadID.String()
}
