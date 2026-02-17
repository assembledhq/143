package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID        uuid.UUID       `db:"id" json:"id"`
	Name      string          `db:"name" json:"name"`
	Slug      string          `db:"slug" json:"slug"`
	Settings  json.RawMessage `db:"settings" json:"settings"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt time.Time       `db:"updated_at" json:"updated_at"`
}

type User struct {
	ID          uuid.UUID `db:"id" json:"id"`
	OrgID       uuid.UUID `db:"org_id" json:"org_id"`
	Email       string    `db:"email" json:"email"`
	Name        string    `db:"name" json:"name"`
	Role        string    `db:"role" json:"role"`
	GitHubID    *int64    `db:"github_id" json:"github_id,omitempty"`
	GitHubLogin *string   `db:"github_login" json:"github_login,omitempty"`
	AvatarURL   *string   `db:"avatar_url" json:"avatar_url,omitempty"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

type Session struct {
	ID        uuid.UUID `db:"id" json:"id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	OrgID     uuid.UUID `db:"org_id" json:"org_id"`
	Token     string    `db:"token" json:"-"` // never expose token in JSON
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Integration struct {
	ID           uuid.UUID       `db:"id" json:"id"`
	OrgID        uuid.UUID       `db:"org_id" json:"org_id"`
	Provider     string          `db:"provider" json:"provider"`
	Config       json.RawMessage `db:"config" json:"-"` // never expose config in JSON (contains secrets)
	Status       string          `db:"status" json:"status"`
	LastSyncedAt *time.Time      `db:"last_synced_at" json:"last_synced_at,omitempty"`
	CreatedAt    time.Time       `db:"created_at" json:"created_at"`
}

type Repository struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	OrgID          uuid.UUID       `db:"org_id" json:"org_id"`
	IntegrationID  uuid.UUID       `db:"integration_id" json:"integration_id"`
	GitHubID       int64           `db:"github_id" json:"github_id"`
	FullName       string          `db:"full_name" json:"full_name"`
	DefaultBranch  string          `db:"default_branch" json:"default_branch"`
	Private        bool            `db:"private" json:"private"`
	Language       *string         `db:"language" json:"language,omitempty"`
	Description    *string         `db:"description" json:"description,omitempty"`
	CloneURL       string          `db:"clone_url" json:"clone_url"`
	InstallationID int64           `db:"installation_id" json:"installation_id"`
	Status         string          `db:"status" json:"status"`
	LastSyncedAt   *time.Time      `db:"last_synced_at" json:"last_synced_at,omitempty"`
	ContextQuality *float64        `db:"context_quality" json:"context_quality,omitempty"`
	Settings       json.RawMessage `db:"settings" json:"settings"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
}

// Job represents an async work queue item.
type Job struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	OrgID          uuid.UUID       `db:"org_id" json:"org_id"`
	Queue          string          `db:"queue" json:"queue"`
	JobType        string          `db:"job_type" json:"job_type"`
	Payload        json.RawMessage `db:"payload" json:"payload"`
	Priority       int             `db:"priority" json:"priority"`
	Status         string          `db:"status" json:"status"`
	Attempts       int             `db:"attempts" json:"attempts"`
	MaxAttempts    int             `db:"max_attempts" json:"max_attempts"`
	RunAt          time.Time       `db:"run_at" json:"run_at"`
	LockedByNodeID *string         `db:"locked_by_node_id" json:"locked_by_node_id,omitempty"`
	LockedAt       *time.Time      `db:"locked_at" json:"locked_at,omitempty"`
	LastError      *string         `db:"last_error" json:"last_error,omitempty"`
	DedupeKey      *string         `db:"dedupe_key" json:"dedupe_key,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	CompletedAt    *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
}
