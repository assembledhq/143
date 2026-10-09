package models

import (
	"strings"

	"github.com/google/uuid"
)

// PreviewInfrastructureOwner identifies one provider handle and worker
// generation. It is an internal maintenance carrier, never an API response.
type PreviewInfrastructureOwner struct {
	OrgID        uuid.UUID
	PreviewID    uuid.UUID
	SessionID    uuid.UUID
	Handle       string
	WorkerNodeID string
}

// Valid reports whether the durable identity is complete enough for cleanup.
// A nil session is valid for standalone branch previews.
func (o PreviewInfrastructureOwner) Valid() bool {
	return o.OrgID != uuid.Nil && o.PreviewID != uuid.Nil &&
		o.Handle != "" && strings.TrimSpace(o.Handle) == o.Handle &&
		o.WorkerNodeID != "" && strings.TrimSpace(o.WorkerNodeID) == o.WorkerNodeID
}
