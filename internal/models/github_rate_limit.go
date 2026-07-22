package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type GitHubRateLimitResource string

const (
	GitHubRateLimitResourceCore    GitHubRateLimitResource = "core"
	GitHubRateLimitResourceGraphQL GitHubRateLimitResource = "graphql"
	GitHubRateLimitResourceSearch  GitHubRateLimitResource = "search"
	GitHubRateLimitResourceUnknown GitHubRateLimitResource = "unknown"
)

func (r GitHubRateLimitResource) Validate() error {
	switch r {
	case GitHubRateLimitResourceCore, GitHubRateLimitResourceGraphQL, GitHubRateLimitResourceSearch, GitHubRateLimitResourceUnknown:
		return nil
	default:
		return fmt.Errorf("invalid GitHub rate-limit resource %q", r)
	}
}

type GitHubRateLimitObservation struct {
	InstallationID int64
	Resource       GitHubRateLimitResource
	Limit          *int
	Remaining      *int
	ResetAt        *time.Time
	BlockedUntil   *time.Time
	ObservedAt     time.Time
}

type GitHubRateLimitDecision struct {
	Allowed             bool
	Known               bool
	ExistingReservation bool
	Bootstrap           bool
	RefreshRequired     bool
	Limit               int
	Remaining           int
	ActiveReserved      int
	RecoveryReserve     int
	ResetAt             time.Time
	BlockedUntil        time.Time
	RetryAfter          time.Duration
	MetadataID          uuid.UUID
}
