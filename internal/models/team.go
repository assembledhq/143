package models

import (
	"time"

	"github.com/google/uuid"
)

const (
	TeamRoleMember = "member"
	TeamRoleLead   = "lead"
)

// Team is a group of users within an organization. Teams can be synced from
// GitHub Teams or created manually. Sessions and projects can be scoped to a
// team for filtering.
type Team struct {
	ID             uuid.UUID `db:"id" json:"id"`
	OrgID          uuid.UUID `db:"org_id" json:"org_id"`
	Name           string    `db:"name" json:"name"`
	Slug           string    `db:"slug" json:"slug"`
	Description    *string   `db:"description" json:"description,omitempty"`
	GitHubTeamID   *int64    `db:"github_team_id" json:"github_team_id,omitempty"`
	GitHubTeamSlug *string   `db:"github_team_slug" json:"github_team_slug,omitempty"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`

	// MemberCount is populated by list queries via a subquery; not stored in DB.
	MemberCount int `db:"member_count" json:"member_count"`
}

// TeamMembership links a user to a team.
type TeamMembership struct {
	ID        uuid.UUID `db:"id" json:"id"`
	TeamID    uuid.UUID `db:"team_id" json:"team_id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	Role      string    `db:"role" json:"role"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// TeamWithMembers is the API response for a single team including its members.
type TeamWithMembers struct {
	Team
	Members []User `json:"members"`
}
