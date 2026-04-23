package models

import (
	"time"

	"github.com/google/uuid"
)

// UserNotificationPreference stores per-user notification toggles scoped to an org.
type UserNotificationPreference struct {
	OrgID                           uuid.UUID `db:"org_id" json:"org_id"`
	UserID                          uuid.UUID `db:"user_id" json:"user_id"`
	SessionCompletionBrowserEnabled bool      `db:"session_completion_browser_enabled" json:"session_completion_browser_enabled"`
	CreatedAt                       time.Time `db:"created_at" json:"created_at"`
	UpdatedAt                       time.Time `db:"updated_at" json:"updated_at"`
}
