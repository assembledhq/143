package models

import (
	"time"

	"github.com/google/uuid"
)

// Invitation is the DB row representation of an invitation to join an org.
// Either Email or GitHubUsername must be set; both may be set when the inviter
// provides both identifiers.
type Invitation struct {
	ID             uuid.UUID  `db:"id"              json:"id"`
	OrgID          uuid.UUID  `db:"org_id"          json:"org_id"`
	Email          *string    `db:"email"           json:"email,omitempty"`
	GitHubUsername *string    `db:"github_username" json:"github_username,omitempty"`
	Role           string     `db:"role"            json:"role"`
	InvitedBy      uuid.UUID  `db:"invited_by"      json:"-"`
	Token          string     `db:"token"           json:"-"`
	Status         string     `db:"status"          json:"status"`
	ExpiresAt      time.Time  `db:"expires_at"      json:"expires_at"`
	CreatedAt      time.Time  `db:"created_at"      json:"created_at"`
	AcceptedAt     *time.Time `db:"accepted_at"     json:"accepted_at,omitempty"`
}

// InvitationResponse is the API response type with the inviter expanded.
type InvitationResponse struct {
	ID             uuid.UUID `json:"id"`
	Email          *string   `json:"email,omitempty"`
	GitHubUsername *string   `json:"github_username,omitempty"`
	Role           string    `json:"role"`
	Status         string    `json:"status"`
	InvitedBy      UserBrief `json:"invited_by"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// InvitationWithInviter is the result of joining invitations with users.
type InvitationWithInviter struct {
	Invitation
	InviterName string `db:"inviter_name" json:"-"`
}

// UserBrief is a minimal user representation for embedding in responses.
type UserBrief struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
