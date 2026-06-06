package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type APIClientStatus string

const (
	APIClientStatusEnabled  APIClientStatus = "enabled"
	APIClientStatusDisabled APIClientStatus = "disabled"
)

func (s APIClientStatus) Validate() error {
	switch s {
	case APIClientStatusEnabled, APIClientStatusDisabled:
		return nil
	default:
		return fmt.Errorf("invalid APIClientStatus: %q", s)
	}
}

type APITokenScope string

const (
	APITokenScopeSessionsRead      APITokenScope = "sessions:read"
	APITokenScopeSessionsCreate    APITokenScope = "sessions:create"
	APITokenScopeSessionsWrite     APITokenScope = "sessions:write"
	APITokenScopeSessionsCancel    APITokenScope = "sessions:cancel"
	APITokenScopeSessionsPublish   APITokenScope = "sessions:publish"
	APITokenScopeAutomationsRead   APITokenScope = "automations:read"
	APITokenScopeAutomationsCreate APITokenScope = "automations:create" // #nosec G101 -- API permission scope, not a credential
	APITokenScopeAutomationsWrite  APITokenScope = "automations:write"  // #nosec G101 -- API permission scope, not a credential
	APITokenScopeAutomationsRun    APITokenScope = "automations:run"    // #nosec G101 -- API permission scope, not a credential
	APITokenScopePreviewsRead      APITokenScope = "previews:read"
	APITokenScopePreviewsCreate    APITokenScope = "previews:create"
	APITokenScopePreviewsStop      APITokenScope = "previews:stop"
)

func (s APITokenScope) Validate() error {
	switch s {
	case APITokenScopeSessionsRead,
		APITokenScopeSessionsCreate,
		APITokenScopeSessionsWrite,
		APITokenScopeSessionsCancel,
		APITokenScopeSessionsPublish,
		APITokenScopeAutomationsRead,
		APITokenScopeAutomationsCreate,
		APITokenScopeAutomationsWrite,
		APITokenScopeAutomationsRun,
		APITokenScopePreviewsRead,
		APITokenScopePreviewsCreate,
		APITokenScopePreviewsStop:
		return nil
	default:
		return fmt.Errorf("invalid APITokenScope: %q", s)
	}
}

func ValidateAPITokenScopes(scopes []string) error {
	if len(scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	for _, scope := range scopes {
		if err := APITokenScope(scope).Validate(); err != nil {
			return err
		}
	}
	return nil
}

type APIClient struct {
	ID               uuid.UUID       `db:"id" json:"id"`
	OrgID            uuid.UUID       `db:"org_id" json:"org_id"`
	Name             string          `db:"name" json:"name"`
	Description      *string         `db:"description" json:"description,omitempty"`
	Status           APIClientStatus `db:"status" json:"status"`
	CreatedByUserID  *uuid.UUID      `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	DisabledByUserID *uuid.UUID      `db:"disabled_by_user_id" json:"disabled_by_user_id,omitempty"`
	DisabledAt       *time.Time      `db:"disabled_at" json:"disabled_at,omitempty"`
	CreatedAt        time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time       `db:"updated_at" json:"updated_at"`
}

type APIToken struct {
	ID                uuid.UUID   `db:"id" json:"id"`
	OrgID             uuid.UUID   `db:"org_id" json:"org_id"`
	APIClientID       uuid.UUID   `db:"api_client_id" json:"api_client_id"`
	Name              string      `db:"name" json:"name"`
	TokenHash         string      `db:"token_hash" json:"-"`
	TokenPrefix       string      `db:"token_prefix" json:"token_prefix"`
	Scopes            []string    `db:"scopes" json:"scopes"`
	RepositoryIDs     []uuid.UUID `db:"repository_ids" json:"repository_ids"`
	ExpiresAt         *time.Time  `db:"expires_at" json:"expires_at,omitempty"`
	LastUsedAt        *time.Time  `db:"last_used_at" json:"last_used_at,omitempty"`
	LastUsedIP        *string     `db:"last_used_ip" json:"last_used_ip,omitempty"`
	LastUsedUserAgent *string     `db:"last_used_user_agent" json:"last_used_user_agent,omitempty"`
	RevokedByUserID   *uuid.UUID  `db:"revoked_by_user_id" json:"revoked_by_user_id,omitempty"`
	RevokedAt         *time.Time  `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedByUserID   *uuid.UUID  `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt         time.Time   `db:"created_at" json:"created_at"`
}

type AuthenticatedAPIToken struct {
	Client APIClient
	Token  APIToken
}
