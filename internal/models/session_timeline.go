package models

import "time"

const (
	SessionTimelineKindMessage         = "message"
	SessionTimelineKindAssistantOutput = "assistant_output"
	SessionTimelineKindToolGroup       = "tool_group"
	SessionTimelineKindError           = "error"
	SessionTimelineKindLog             = "log"
	SessionTimelineKindPlanOutput      = "plan_output"
	SessionTimelineKindPlanMessage     = "plan_message"
	SessionTimelineKindHumanInput      = "human_input"
)

// SessionTimelineEntry is the server-owned session timeline shape returned by
// the session detail API.
type SessionTimelineEntry struct {
	Kind              string             `json:"kind"`
	CreatedAt         time.Time          `json:"created_at"`
	Message           *SessionMessage    `json:"message,omitempty"`
	Log               *SessionLog        `json:"log,omitempty"`
	ToolUse           *SessionLog        `json:"tool_use,omitempty"`
	ToolResult        *SessionLog        `json:"tool_result,omitempty"`
	HumanInputRequest *HumanInputRequest `json:"human_input_request,omitempty"`
	TurnNumber        *int               `json:"turn_number,omitempty"`
}
