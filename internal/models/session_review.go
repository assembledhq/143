package models

import (
	"fmt"

	"github.com/google/uuid"
)

// SessionReviewMode selects which native review surface the agent should run.
// "default" is the agent's standard review; "security" targets vulnerabilities.
// Adapters declare which modes they support via ReviewCapableAdapter.
type SessionReviewMode string

const (
	SessionReviewModeDefault  SessionReviewMode = "default"
	SessionReviewModeSecurity SessionReviewMode = "security"
)

func (m SessionReviewMode) Validate() error {
	switch m {
	case SessionReviewModeDefault, SessionReviewModeSecurity:
		return nil
	default:
		return fmt.Errorf("invalid SessionReviewMode: %q", m)
	}
}

// SessionReviewResponse is returned when a review turn is enqueued.
type SessionReviewResponse struct {
	SessionID uuid.UUID         `json:"session_id"`
	Mode      SessionReviewMode `json:"mode"`
}

// SessionReviewCapabilities describes whether the session can run a review
// turn right now and which modes the agent's adapter supports.
type SessionReviewCapabilities struct {
	CanReview bool                `json:"can_review"`
	Reason    string              `json:"reason,omitempty"`
	Modes     []SessionReviewMode `json:"modes"`
}
