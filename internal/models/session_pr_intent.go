package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaxTeamReviewers = 10

var (
	githubTeamOwnerPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`)
	githubTeamSlugPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,98}[a-z0-9])?$`)
)

// NormalizeTeamReviewers binds every team to the repository owner. The stored
// form always includes the owner so a later publication cannot reinterpret a
// bare slug in a different organization.
func NormalizeTeamReviewers(repositoryFullName string, values []string) ([]string, error) {
	repository := strings.Split(strings.ToLower(strings.TrimSpace(repositoryFullName)), "/")
	if len(repository) != 2 || !githubTeamOwnerPattern.MatchString(repository[0]) || strings.TrimSpace(repository[1]) == "" {
		return nil, fmt.Errorf("repository must contain a valid owner and name")
	}
	if len(values) > MaxTeamReviewers {
		return nil, fmt.Errorf("at most %d team reviewers may be requested", MaxTeamReviewers)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		team := strings.ToLower(strings.TrimSpace(value))
		if strings.HasPrefix(team, "@") {
			team = strings.TrimPrefix(team, "@")
			if !strings.Contains(team, "/") {
				return nil, fmt.Errorf("team mentions must include their organization")
			}
		}
		parts := strings.Split(team, "/")
		if len(parts) == 1 {
			parts = []string{repository[0], parts[0]}
		}
		if len(parts) != 2 || parts[0] != repository[0] || !githubTeamSlugPattern.MatchString(parts[1]) {
			return nil, fmt.Errorf("team reviewer must be a valid team in the repository organization")
		}
		canonical := parts[0] + "/" + parts[1]
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

type SessionPRIntent struct {
	OrgID           uuid.UUID  `db:"org_id" json:"org_id"`
	SessionID       uuid.UUID  `db:"session_id" json:"session_id"`
	AutomationRunID uuid.UUID  `db:"automation_run_id" json:"automation_run_id"`
	ChangesetID     uuid.UUID  `db:"changeset_id" json:"changeset_id"`
	TeamReviewers   []string   `db:"team_reviewers" json:"team_reviewers"`
	FrozenAt        *time.Time `db:"frozen_at" json:"frozen_at,omitempty"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}

type TeamReviewRequestState string

const (
	TeamReviewRequestSending   TeamReviewRequestState = "sending"
	TeamReviewRequestRequested TeamReviewRequestState = "requested"
	TeamReviewRequestRetryable TeamReviewRequestState = "retryable"
)

func (s TeamReviewRequestState) Validate() error {
	switch s {
	case TeamReviewRequestSending, TeamReviewRequestRequested, TeamReviewRequestRetryable:
		return nil
	default:
		return fmt.Errorf("invalid team review request state: %q", s)
	}
}

// TeamReviewRequestReceipt survives the disappearance of a pending GitHub team
// review request after the team has submitted its review.
type TeamReviewRequestReceipt struct {
	OrgID         uuid.UUID              `db:"org_id" json:"org_id"`
	PublicationID uuid.UUID              `db:"publication_id" json:"publication_id"`
	Team          string                 `db:"team" json:"team"`
	State         TeamReviewRequestState `db:"state" json:"state"`
	CreatedAt     time.Time              `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time              `db:"updated_at" json:"updated_at"`
}
