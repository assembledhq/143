package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type MembershipRole string

// Role constants for organization_memberships.role. These mirror the legacy
// users.role values so behavior is identical during the compatibility window.
const (
	RoleAdmin   MembershipRole = "admin"
	RoleMember  MembershipRole = "member"
	RoleBuilder MembershipRole = "builder"
	RoleViewer  MembershipRole = "viewer"
)

// ValidRoles lists every legal membership role, in order of decreasing privilege.
var ValidRoles = []MembershipRole{RoleAdmin, RoleMember, RoleBuilder, RoleViewer}

func (r MembershipRole) Validate() error {
	switch r {
	case RoleAdmin, RoleMember, RoleBuilder, RoleViewer:
		return nil
	default:
		return fmt.Errorf("invalid MembershipRole: %q", r)
	}
}

// IsValidRole reports whether r is one of the known membership roles.
func IsValidRole[T ~string](r T) bool {
	return MembershipRole(r).Validate() == nil
}

func ParseMembershipRole(raw string) (MembershipRole, error) {
	role := MembershipRole(raw)
	if err := role.Validate(); err != nil {
		return "", err
	}
	return role, nil
}

// OrganizationMembership is the join row between a user identity and an org.
//
// A user may have many memberships; accepting an invitation to a second org
// adds a row here rather than mutating `users.org_id`. The middleware picks
// which membership is "active" for a given request via the X-Active-Org-ID
// header (or falls back to the session's last_org_id).
type OrganizationMembership struct {
	UserID    uuid.UUID      `db:"user_id"    json:"user_id"`
	OrgID     uuid.UUID      `db:"org_id"     json:"org_id"`
	Role      MembershipRole `db:"role"       json:"role"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
}

// MembershipSummary is the API representation of one entry in the list of
// orgs a user belongs to. It includes the org name so the switcher UI can
// render without a second round-trip.
type MembershipSummary struct {
	OrgID   uuid.UUID      `db:"org_id"   json:"org_id"`
	OrgName string         `db:"org_name" json:"org_name"`
	Role    MembershipRole `db:"role"     json:"role"`
}

// MembershipsResponse is the body of GET /api/v1/auth/memberships. It carries
// the active-org resolution that /auth/me cannot emit during the compat
// window (changing the /auth/me shape is the sunset step, not this PR) plus
// the full membership set so the org switcher renders in one round-trip.
//
// ActiveOrgID is uuid.Nil when the user has zero memberships — the frontend
// renders the empty state rather than forcing the user to pick a nonexistent
// org. ActiveRole is empty in the same case.
type MembershipsResponse struct {
	ActiveOrgID uuid.UUID           `json:"active_org_id"`
	ActiveRole  MembershipRole      `json:"active_role"`
	Memberships []MembershipSummary `json:"memberships"`
}
