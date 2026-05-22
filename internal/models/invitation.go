package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type InvitationAcceptanceMethod string

const (
	InvitationAcceptanceMethodEither InvitationAcceptanceMethod = "either"
	InvitationAcceptanceMethodEmail  InvitationAcceptanceMethod = "email"
	InvitationAcceptanceMethodGitHub InvitationAcceptanceMethod = "github"
)

func (m InvitationAcceptanceMethod) Validate() error {
	switch m {
	case "", InvitationAcceptanceMethodEither, InvitationAcceptanceMethodEmail, InvitationAcceptanceMethodGitHub:
		return nil
	default:
		return fmt.Errorf("invalid invitation acceptance method: %s", m)
	}
}

type InvitationStatus string

const (
	InvitationStatusPending  InvitationStatus = "pending"
	InvitationStatusAccepted InvitationStatus = "accepted"
	InvitationStatusRevoked  InvitationStatus = "revoked"
	InvitationStatusExpired  InvitationStatus = "expired"
)

func (s InvitationStatus) Validate() error {
	switch s {
	case InvitationStatusPending, InvitationStatusAccepted, InvitationStatusRevoked, InvitationStatusExpired:
		return nil
	default:
		return fmt.Errorf("invalid invitation status: %s", s)
	}
}

// Invitation is the DB row representation of an invitation to join an org.
// Either Email or GitHubUsername must be set; both may be set when the inviter
// provides both identifiers.
type Invitation struct {
	ID               uuid.UUID                  `db:"id"                json:"id"`
	OrgID            uuid.UUID                  `db:"org_id"            json:"org_id"`
	Email            *string                    `db:"email"             json:"email,omitempty"`
	GitHubUsername   *string                    `db:"github_username"   json:"github_username,omitempty"`
	AcceptanceMethod InvitationAcceptanceMethod `db:"acceptance_method" json:"acceptance_method"`
	Role             Role                       `db:"role"              json:"role"`
	InvitedBy        uuid.UUID                  `db:"invited_by"        json:"-"`
	Token            string                     `db:"token"             json:"-"`
	Status           InvitationStatus           `db:"status"            json:"status"`
	ExpiresAt        time.Time                  `db:"expires_at"        json:"expires_at"`
	CreatedAt        time.Time                  `db:"created_at"        json:"created_at"`
	AcceptedAt       *time.Time                 `db:"accepted_at"       json:"accepted_at,omitempty"`
}

// InvitationResponse is the API response type with the inviter expanded.
type InvitationResponse struct {
	ID               uuid.UUID                  `json:"id"`
	Email            *string                    `json:"email,omitempty"`
	GitHubUsername   *string                    `json:"github_username,omitempty"`
	AcceptanceMethod InvitationAcceptanceMethod `json:"acceptance_method"`
	Role             Role                       `json:"role"`
	Status           InvitationStatus           `json:"status"`
	InvitedBy        UserBrief                  `json:"invited_by"`
	ExpiresAt        time.Time                  `json:"expires_at"`
	CreatedAt        time.Time                  `json:"created_at"`
}

// InvitationWithInviter is the result of joining invitations with users.
type InvitationWithInviter struct {
	Invitation
	InviterName string `db:"inviter_name" json:"-"`
}

// PendingInvitationForUserRow is the DB row shape for ListPendingForUser:
// invitations joined with the target organization (for org_name) and the
// inviting user (for inviter name). Kept separate from the response type
// because the JSON shape and DB shape differ: the response embeds the
// inviter as a UserBrief, the row carries the two scalar columns.
type PendingInvitationForUserRow struct {
	ID          uuid.UUID `db:"id"`
	OrgID       uuid.UUID `db:"org_id"`
	OrgName     string    `db:"org_name"`
	Role        Role      `db:"role"`
	InvitedBy   uuid.UUID `db:"invited_by"`
	InviterName string    `db:"inviter_name"`
	ExpiresAt   time.Time `db:"expires_at"`
	CreatedAt   time.Time `db:"created_at"`
}

// PendingInvitationForUser is the API response shape for an invitation
// surfaced to its potential claimer (the user the invitation matches by
// email or github_username). Unlike InvitationResponse, this is rendered
// for the *invitee* — so it carries the target org's name (the invitee
// has no other way to know which org they're being invited into) and
// deliberately omits the recipient identifier fields (the user is the
// recipient; we don't echo their own email back at them) and the token
// (accept/decline are id-routed and re-validate against the session, so
// the token never needs to leave server-side state).
type PendingInvitationForUser struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	OrgName   string    `json:"org_name"`
	Role      Role      `json:"role"`
	InvitedBy UserBrief `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// UserBrief is a minimal user representation for embedding in responses.
type UserBrief struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
