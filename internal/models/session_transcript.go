package models

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TranscriptWindowPosition describes which slice of the conversation window to return.
type TranscriptWindowPosition string

const (
	TranscriptWindowPositionLatest TranscriptWindowPosition = "latest"
	TranscriptWindowPositionOlder  TranscriptWindowPosition = "older"
	TranscriptWindowPositionNewer  TranscriptWindowPosition = "newer"
	TranscriptWindowPositionAround TranscriptWindowPosition = "around"
)

func (p TranscriptWindowPosition) Validate() error {
	switch p {
	case TranscriptWindowPositionLatest,
		TranscriptWindowPositionOlder,
		TranscriptWindowPositionNewer,
		TranscriptWindowPositionAround:
		return nil
	default:
		return fmt.Errorf("invalid TranscriptWindowPosition: %q", p)
	}
}

// TranscriptEntryKind classifies a single renderable item within a turn.
type TranscriptEntryKind string

const (
	TranscriptEntryKindMessage    TranscriptEntryKind = "message"
	TranscriptEntryKindToolUse    TranscriptEntryKind = "tool_use"
	TranscriptEntryKindToolResult TranscriptEntryKind = "tool_result"
	TranscriptEntryKindLog        TranscriptEntryKind = "log"
	TranscriptEntryKindHumanInput TranscriptEntryKind = "human_input"
	TranscriptEntryKindMilestone  TranscriptEntryKind = "milestone"
	TranscriptEntryKindCheckpoint TranscriptEntryKind = "checkpoint"
)

func (k TranscriptEntryKind) Validate() error {
	switch k {
	case TranscriptEntryKindMessage,
		TranscriptEntryKindToolUse,
		TranscriptEntryKindToolResult,
		TranscriptEntryKindLog,
		TranscriptEntryKindHumanInput,
		TranscriptEntryKindMilestone,
		TranscriptEntryKindCheckpoint:
		return nil
	default:
		return fmt.Errorf("invalid TranscriptEntryKind: %q", k)
	}
}

// TranscriptCursor is a base64url-encoded JSON cursor used for turn-based
// pagination of the transcript window. Version must equal 1.
type TranscriptCursor struct {
	Version    int       `json:"v"`
	OrgID      uuid.UUID `json:"org_id"`
	ThreadID   uuid.UUID `json:"thread_id"`
	TurnNumber int       `json:"turn_number"`
	EntryID    string    `json:"entry_id"`
}

// Encode serialises the cursor as a base64url (no padding) JSON blob.
func (c TranscriptCursor) Encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode transcript cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeTranscriptCursor parses a raw cursor string and validates that it
// belongs to the given org and thread.
func DecodeTranscriptCursor(raw string, orgID, threadID uuid.UUID) (TranscriptCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return TranscriptCursor{}, fmt.Errorf("decode transcript cursor: %w", err)
	}
	var c TranscriptCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return TranscriptCursor{}, fmt.Errorf("unmarshal transcript cursor: %w", err)
	}
	if c.Version != 1 {
		return TranscriptCursor{}, fmt.Errorf("unsupported transcript cursor version: %d", c.Version)
	}
	if c.OrgID != orgID {
		return TranscriptCursor{}, fmt.Errorf("transcript cursor org mismatch")
	}
	if c.ThreadID != threadID {
		return TranscriptCursor{}, fmt.Errorf("transcript cursor thread mismatch")
	}
	return c, nil
}

// SessionTranscriptEntry is one renderable item within a turn.
type SessionTranscriptEntry struct {
	ID        string              `json:"id"`
	Kind      TranscriptEntryKind `json:"kind"`
	CreatedAt time.Time           `json:"created_at"`

	MessageID *int64     `json:"message_id,omitempty"`
	LogID     *int64     `json:"log_id,omitempty"`
	RequestID *uuid.UUID `json:"request_id,omitempty"`

	Role  MessageRole     `json:"role,omitempty"`
	Level SessionLogLevel `json:"level,omitempty"`

	Content          string `json:"content,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	ContentBytes     int    `json:"content_bytes,omitempty"`
	ContentChars     int    `json:"content_chars,omitempty"`

	Summary   string `json:"summary,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`

	Message    *SessionMessage     `json:"message,omitempty"`
	Log        *SessionLogResponse `json:"log,omitempty"`
	HumanInput *HumanInputRequest  `json:"human_input,omitempty"`
}

// SessionTranscriptTurn groups all entries belonging to one agent turn.
type SessionTranscriptTurn struct {
	TurnNumber int                      `json:"turn_number"`
	StartedAt  time.Time                `json:"started_at"`
	EndedAt    *time.Time               `json:"ended_at,omitempty"`
	Entries    []SessionTranscriptEntry `json:"entries"`
}

// SessionTranscriptWindowMeta carries pagination and live-edge metadata.
type SessionTranscriptWindowMeta struct {
	Position        TranscriptWindowPosition `json:"position"`
	HasOlder        bool                     `json:"has_older"`
	NextOlderCursor string                   `json:"next_older_cursor,omitempty"`
	HasNewer        bool                     `json:"has_newer"`
	NextNewerCursor string                   `json:"next_newer_cursor,omitempty"`

	AnchorEntryID string `json:"anchor_entry_id,omitempty"`
	AnchorFound   bool   `json:"anchor_found,omitempty"`

	LatestAssistantEntryID   string `json:"latest_assistant_entry_id,omitempty"`
	LatestAssistantMessageID int64  `json:"latest_assistant_message_id,omitempty"`
	LiveEdgeEntryID          string `json:"live_edge_entry_id,omitempty"`
	LiveEdgeMessageID        int64  `json:"live_edge_message_id,omitempty"`

	ThreadStatus ThreadStatus `json:"thread_status"`
}

// SessionTranscriptWindowResponse is the top-level API response for the
// transcript window endpoint.
type SessionTranscriptWindowResponse struct {
	Data []SessionTranscriptTurn     `json:"data"`
	Meta SessionTranscriptWindowMeta `json:"meta"`
}

type SessionTranscriptSearchResponse struct {
	Data []SessionTranscriptSearchMatch `json:"data"`
	Meta SessionTranscriptSearchMeta    `json:"meta"`
}

type SessionTranscriptSearchMeta struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type SessionTranscriptSearchMatch struct {
	EntryID    string              `json:"entry_id"`
	Kind       TranscriptEntryKind `json:"kind"`
	TurnNumber int                 `json:"turn_number"`
	CreatedAt  time.Time           `json:"created_at"`
	Snippet    string              `json:"snippet"`
	MessageID  int64               `json:"message_id,omitempty"`
	LogID      int64               `json:"log_id,omitempty"`
	RequestID  *uuid.UUID          `json:"request_id,omitempty"`
	Role       MessageRole         `json:"role,omitempty"`
	Level      SessionLogLevel     `json:"level,omitempty"`
	ToolName   string              `json:"tool_name,omitempty"`
}
