package models

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxTeamReviewers = 10

const (
	// MaxStagedPRTitleBytes matches the PR service's title cap, so a staged
	// title is never silently truncated before Linear key prefixes apply.
	MaxStagedPRTitleBytes = 120
	// MaxStagedPRBodyLength leaves room below GitHub's 65,536-character body
	// limit for the server-owned footer and publication marker.
	MaxStagedPRBodyLength = 60000
)

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

// NormalizeStagedPRTitle trims an agent-staged pull request title and rejects
// values GitHub would alter or refuse. Linear key prefixes are applied at
// publication time, not here.
func NormalizeStagedPRTitle(value string) (string, error) {
	title := strings.TrimSpace(value)
	if title == "" {
		return "", errors.New("title must not be empty")
	}
	if strings.ContainsAny(title, "\r\n") {
		return "", errors.New("title must be a single line")
	}
	if strings.ContainsRune(title, 0) {
		return "", errors.New("title must not contain NUL characters")
	}
	if len(title) > MaxStagedPRTitleBytes {
		return "", fmt.Errorf("title must be at most %d bytes", MaxStagedPRTitleBytes)
	}
	return title, nil
}

// ValidateStagedPRBody checks an agent-staged pull request description. The
// body is stored verbatim; publication trims surrounding whitespace and
// appends the server-owned footer and marker.
func ValidateStagedPRBody(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("body must not be empty")
	}
	if strings.ContainsRune(value, 0) {
		return errors.New("body must not contain NUL characters")
	}
	if utf8.RuneCountInString(value) > MaxStagedPRBodyLength {
		return fmt.Errorf("body must be at most %d characters", MaxStagedPRBodyLength)
	}
	return nil
}

// SessionPRIntent is optional automatic publication metadata. Title and Body
// are nil when the agent did not stage them, so publication generates them.
type SessionPRIntent struct {
	OrgID           uuid.UUID  `db:"org_id" json:"org_id"`
	SessionID       uuid.UUID  `db:"session_id" json:"session_id"`
	AutomationRunID uuid.UUID  `db:"automation_run_id" json:"automation_run_id"`
	ChangesetID     uuid.UUID  `db:"changeset_id" json:"changeset_id"`
	TeamReviewers   []string   `db:"team_reviewers" json:"team_reviewers"`
	Title           *string    `db:"title" json:"title,omitempty"`
	Body            *string    `db:"body" json:"body,omitempty"`
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
