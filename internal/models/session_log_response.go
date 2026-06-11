package models

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const SessionLogPreviewBytes = 8 * 1024

type SessionLogResponse struct {
	ID               int64           `json:"id"`
	SessionID        uuid.UUID       `json:"session_id"`
	ThreadID         *uuid.UUID      `json:"thread_id,omitempty"`
	Level            SessionLogLevel `json:"level"`
	Message          string          `json:"message"`
	Metadata         json.RawMessage `json:"metadata"`
	TurnNumber       int             `json:"turn_number"`
	CreatedAt        time.Time       `json:"created_at"`
	MessageBytes     int             `json:"message_bytes"`
	MessageChars     int             `json:"message_chars"`
	MessageTruncated bool            `json:"message_truncated"`
}

type SessionLogDetailResponse struct {
	ID           int64           `json:"id"`
	SessionID    uuid.UUID       `json:"session_id"`
	ThreadID     *uuid.UUID      `json:"thread_id,omitempty"`
	Level        SessionLogLevel `json:"level"`
	Message      string          `json:"message"`
	Metadata     json.RawMessage `json:"metadata"`
	TurnNumber   int             `json:"turn_number"`
	CreatedAt    time.Time       `json:"created_at"`
	MessageBytes int             `json:"message_bytes"`
	MessageChars int             `json:"message_chars"`
}

type SessionTimelineResponseEntry struct {
	Kind              SessionTimelineEntryKind `json:"kind"`
	CreatedAt         time.Time                `json:"created_at"`
	Message           *SessionMessage          `json:"message,omitempty"`
	Log               *SessionLogResponse      `json:"log,omitempty"`
	ToolUse           *SessionLogResponse      `json:"tool_use,omitempty"`
	ToolResult        *SessionLogResponse      `json:"tool_result,omitempty"`
	HumanInputRequest *HumanInputRequest       `json:"human_input_request,omitempty"`
	TurnNumber        int                      `json:"turn_number,omitempty"`
}

func NewSessionLogResponse(log SessionLog) SessionLogResponse {
	message, truncated := previewSessionLogMessage(log.Message)
	return SessionLogResponse{
		ID:               log.ID,
		SessionID:        log.SessionID,
		ThreadID:         log.ThreadID,
		Level:            log.Level,
		Message:          message,
		Metadata:         log.Metadata,
		TurnNumber:       log.TurnNumber,
		CreatedAt:        log.Timestamp,
		MessageBytes:     len([]byte(log.Message)),
		MessageChars:     utf8.RuneCountInString(log.Message),
		MessageTruncated: truncated,
	}
}

func NewSessionLogDetailResponse(log SessionLog) SessionLogDetailResponse {
	return SessionLogDetailResponse{
		ID:           log.ID,
		SessionID:    log.SessionID,
		ThreadID:     log.ThreadID,
		Level:        log.Level,
		Message:      log.Message,
		Metadata:     log.Metadata,
		TurnNumber:   log.TurnNumber,
		CreatedAt:    log.Timestamp,
		MessageBytes: len([]byte(log.Message)),
		MessageChars: utf8.RuneCountInString(log.Message),
	}
}

func NewSessionTimelineResponseEntry(entry SessionTimelineEntry) SessionTimelineResponseEntry {
	out := SessionTimelineResponseEntry{
		Kind:              entry.Kind,
		CreatedAt:         entry.CreatedAt,
		Message:           entry.Message,
		HumanInputRequest: entry.HumanInputRequest,
	}
	if entry.TurnNumber != nil {
		out.TurnNumber = *entry.TurnNumber
	}
	if entry.Log != nil {
		log := NewSessionLogResponse(*entry.Log)
		out.Log = &log
	}
	if entry.ToolUse != nil {
		toolUse := NewSessionLogResponse(*entry.ToolUse)
		out.ToolUse = &toolUse
	}
	if entry.ToolResult != nil {
		toolResult := NewSessionLogResponse(*entry.ToolResult)
		out.ToolResult = &toolResult
	}
	return out
}

func NewSessionLogResponses(logs []SessionLog) []SessionLogResponse {
	if logs == nil {
		return nil
	}
	out := make([]SessionLogResponse, 0, len(logs))
	for _, log := range logs {
		out = append(out, NewSessionLogResponse(log))
	}
	return out
}

func NewSessionTimelineResponseEntries(entries []SessionTimelineEntry) []SessionTimelineResponseEntry {
	if entries == nil {
		return nil
	}
	out := make([]SessionTimelineResponseEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, NewSessionTimelineResponseEntry(entry))
	}
	return out
}

func previewSessionLogMessage(message string) (string, bool) {
	if len([]byte(message)) <= SessionLogPreviewBytes {
		return message, false
	}
	preview := message[:SessionLogPreviewBytes]
	for !utf8.ValidString(preview) && len(preview) > 0 {
		preview = preview[:len(preview)-1]
	}
	return preview, true
}
