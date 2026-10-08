package models

import (
	"time"

	"github.com/google/uuid"
)

// SessionStreamCleanupCursor identifies a terminal session and its position in
// the cleanup sweep. Cleanup must never load the session's diff or other payloads.
type SessionStreamCleanupCursor struct {
	ID          uuid.UUID `db:"id"`
	CompletedAt time.Time `db:"completed_at"`
}
