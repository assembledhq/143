package models

import "time"

type SessionTimelineEntryKind string

const (
	SessionTimelineKindMessage         SessionTimelineEntryKind = "message"
	SessionTimelineKindAssistantOutput SessionTimelineEntryKind = "assistant_output"
	SessionTimelineKindToolGroup       SessionTimelineEntryKind = "tool_group"
	SessionTimelineKindError           SessionTimelineEntryKind = "error"
	SessionTimelineKindLog             SessionTimelineEntryKind = "log"
	SessionTimelineKindPlanOutput      SessionTimelineEntryKind = "plan_output"
	SessionTimelineKindPlanMessage     SessionTimelineEntryKind = "plan_message"
	SessionTimelineKindHumanInput      SessionTimelineEntryKind = "human_input"
)

// SessionTimelineEntry is the server-owned session timeline shape returned by
// the session detail API.
type SessionTimelineEntry struct {
	Kind              SessionTimelineEntryKind `json:"kind"`
	CreatedAt         time.Time                `json:"created_at"`
	Message           *SessionMessage          `json:"message,omitempty"`
	Log               *SessionLog              `json:"log,omitempty"`
	ToolUse           *SessionLog              `json:"tool_use,omitempty"`
	ToolResult        *SessionLog              `json:"tool_result,omitempty"`
	HumanInputRequest *HumanInputRequest       `json:"human_input_request,omitempty"`
	TurnNumber        *int                     `json:"turn_number,omitempty"`
}
