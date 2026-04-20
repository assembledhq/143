package models

import (
	"time"

	"github.com/google/uuid"
)

// Role constants for organization_memberships.role. These mirror the legacy
// users.role values so behavior is identical during the compatibility window.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// ValidRoles lists every legal membership role, in order of decreasing privilege.
var ValidRoles = []string{RoleAdmin, RoleMember, RoleViewer}

// IsValidRole reports whether r is one of the known membership roles.
func IsValidRole(r string) bool {
	switch r {
	case RoleAdmin, RoleMember, RoleViewer:
		return true
	}
	return false
}

// OrganizationMembership is the join row between a user identity and an org.
//
// A user may have many memberships; accepting an invitation to a second org
// adds a row here rather than mutating `users.org_id`. The middleware picks
// which membership is "active" for a given request via the X-Active-Org-ID
// header (or falls back to the session's last_org_id).
type OrganizationMembership struct {
	UserID    uuid.UUID `db:"user_id"    json:"user_id"`
	OrgID     uuid.UUID `db:"org_id"     json:"org_id"`
	Role      string    `db:"role"       json:"role"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// MembershipSummary is the API representation of one entry in the list of
// orgs a user belongs to. It includes the org name so the switcher UI can
// render without a second round-trip.
type MembershipSummary struct {
	OrgID   uuid.UUID `db:"org_id"   json:"org_id"`
	OrgName string    `db:"org_name" json:"org_name"`
	Role    string    `db:"role"     json:"role"`
}

// AuthMeResponse is the shape returned by GET /api/v1/auth/me.
//
// `User` retains the legacy `org_id` and `role` fields so existing single-org
// UI keeps working during the compatibility window. `ActiveOrgID` and
// `ActiveRole` are the authoritative values for the current request and will
// equal `User.OrgID` / `User.Role` for users with a single membership.
type AuthMeResponse struct {
	User        User                `json:"user"`
	OrgID       uuid.UUID           `json:"org_id"`
	ActiveOrgID uuid.UUID           `json:"active_org_id"`
	ActiveRole  string              `json:"active_role"`
	Memberships []MembershipSummary `json:"memberships"`
}
