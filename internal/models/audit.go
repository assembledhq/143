package models

import (
	"encoding/json"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// AuditLog represents an immutable audit trail entry.
//
// IPAddress uses netip.Prefix because PostgreSQL's inet type includes a netmask
// and pgx v5 natively scans it into netip.Prefix. Use .Addr() to extract the
// address when needed.
type AuditLog struct {
	ID           int64             `db:"id" json:"id"`
	OrgID        uuid.UUID         `db:"org_id" json:"org_id"`
	ActorType    AuditActorType    `db:"actor_type" json:"actor_type"`
	ActorID      string            `db:"actor_id" json:"actor_id"`
	UserID       *uuid.UUID        `db:"user_id" json:"user_id,omitempty"`
	Action       AuditAction       `db:"action" json:"action"`
	ResourceType AuditResourceType `db:"resource_type" json:"resource_type"`
	ResourceID   *string           `db:"resource_id" json:"resource_id,omitempty"`
	Details      json.RawMessage   `db:"details" json:"details,omitempty"`
	RequestID    *string           `db:"request_id" json:"request_id,omitempty"`
	IPAddress    *netip.Prefix     `db:"ip_address" json:"ip_address,omitempty"`
	UserAgent    *string           `db:"user_agent" json:"user_agent,omitempty"`
	SessionID    *uuid.UUID        `db:"session_id" json:"session_id,omitempty"`
	ProjectID    *uuid.UUID        `db:"project_id" json:"project_id,omitempty"`
	CreatedAt    time.Time         `db:"created_at" json:"created_at"`
}
