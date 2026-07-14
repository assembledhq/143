package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ExternalIdentityProvider string

const (
	ExternalIdentityProviderSlack  ExternalIdentityProvider = "slack"
	ExternalIdentityProviderLinear ExternalIdentityProvider = "linear"
)

func (p ExternalIdentityProvider) Validate() error {
	switch p {
	case ExternalIdentityProviderSlack, ExternalIdentityProviderLinear:
		return nil
	default:
		return fmt.Errorf("invalid ExternalIdentityProvider: %q", p)
	}
}

type ExternalUserLinkSource string

const (
	ExternalUserLinkSourceSelfLinked  ExternalUserLinkSource = "self_linked"
	ExternalUserLinkSourceAdminLinked ExternalUserLinkSource = "admin_linked"
	ExternalUserLinkSourceEmailMatch  ExternalUserLinkSource = "email_match"
	ExternalUserLinkSourceDirectory   ExternalUserLinkSource = "directory"
)

func (s ExternalUserLinkSource) Validate() error {
	switch s {
	case ExternalUserLinkSourceSelfLinked, ExternalUserLinkSourceAdminLinked, ExternalUserLinkSourceEmailMatch, ExternalUserLinkSourceDirectory:
		return nil
	default:
		return fmt.Errorf("invalid ExternalUserLinkSource: %q", s)
	}
}

type ExternalUserLinkStatus string

const (
	ExternalUserLinkStatusActive  ExternalUserLinkStatus = "active"
	ExternalUserLinkStatusRevoked ExternalUserLinkStatus = "revoked"
)

func (s ExternalUserLinkStatus) Validate() error {
	switch s {
	case ExternalUserLinkStatusActive, ExternalUserLinkStatusRevoked:
		return nil
	default:
		return fmt.Errorf("invalid ExternalUserLinkStatus: %q", s)
	}
}

type ExternalUserLink struct {
	ID                  uuid.UUID                `db:"id" json:"id"`
	OrgID               uuid.UUID                `db:"org_id" json:"org_id"`
	Provider            ExternalIdentityProvider `db:"provider" json:"provider"`
	ProviderWorkspaceID string                   `db:"provider_workspace_id" json:"provider_workspace_id"`
	ProviderUserID      string                   `db:"provider_user_id" json:"provider_user_id"`
	UserID              uuid.UUID                `db:"user_id" json:"user_id"`
	Source              ExternalUserLinkSource   `db:"source" json:"source"`
	Status              ExternalUserLinkStatus   `db:"status" json:"status"`
	Confidence          int                      `db:"confidence" json:"confidence"`
	ExternalEmail       *string                  `db:"external_email" json:"external_email,omitempty"`
	ExternalHandle      *string                  `db:"external_handle" json:"external_handle,omitempty"`
	ExternalDisplayName *string                  `db:"external_display_name" json:"external_display_name,omitempty"`
	LinkedByUserID      *uuid.UUID               `db:"linked_by_user_id" json:"linked_by_user_id,omitempty"`
	CreatedAt           time.Time                `db:"created_at" json:"created_at"`
	RevokedAt           *time.Time               `db:"revoked_at" json:"revoked_at,omitempty"`
}

type ExternalUserLinkSuggestion struct {
	ID                  uuid.UUID                `db:"id" json:"id"`
	OrgID               uuid.UUID                `db:"org_id" json:"org_id"`
	Provider            ExternalIdentityProvider `db:"provider" json:"provider"`
	ProviderWorkspaceID string                   `db:"provider_workspace_id" json:"provider_workspace_id"`
	ProviderUserID      string                   `db:"provider_user_id" json:"provider_user_id"`
	SuggestedUserID     uuid.UUID                `db:"suggested_user_id" json:"suggested_user_id"`
	Reason              string                   `db:"reason" json:"reason"`
	Confidence          int                      `db:"confidence" json:"confidence"`
	ExternalEmail       *string                  `db:"external_email" json:"external_email,omitempty"`
	ExternalHandle      *string                  `db:"external_handle" json:"external_handle,omitempty"`
	ExternalDisplayName *string                  `db:"external_display_name" json:"external_display_name,omitempty"`
	LastSeenAt          time.Time                `db:"last_seen_at" json:"last_seen_at"`
	DismissedAt         *time.Time               `db:"dismissed_at" json:"dismissed_at,omitempty"`
}

type ExternalUserLinkClaim struct {
	ID                  uuid.UUID                `db:"id" json:"id"`
	OrgID               uuid.UUID                `db:"org_id" json:"org_id"`
	Provider            ExternalIdentityProvider `db:"provider" json:"provider"`
	ProviderWorkspaceID string                   `db:"provider_workspace_id" json:"provider_workspace_id"`
	ProviderUserID      string                   `db:"provider_user_id" json:"provider_user_id"`
	SourceContext       []byte                   `db:"source_context" json:"source_context"`
	ExpiresAt           time.Time                `db:"expires_at" json:"expires_at"`
	ClaimedByUserID     *uuid.UUID               `db:"claimed_by_user_id" json:"claimed_by_user_id,omitempty"`
	ClaimedAt           *time.Time               `db:"claimed_at" json:"claimed_at,omitempty"`
	CreatedAt           time.Time                `db:"created_at" json:"created_at"`
}

type ExternalUserObservation struct {
	ID                  uuid.UUID                `db:"id" json:"id"`
	OrgID               uuid.UUID                `db:"org_id" json:"org_id"`
	Provider            ExternalIdentityProvider `db:"provider" json:"provider"`
	ProviderWorkspaceID string                   `db:"provider_workspace_id" json:"provider_workspace_id"`
	ProviderUserID      string                   `db:"provider_user_id" json:"provider_user_id"`
	ExternalEmail       *string                  `db:"external_email" json:"external_email,omitempty"`
	ExternalHandle      *string                  `db:"external_handle" json:"external_handle,omitempty"`
	ExternalDisplayName *string                  `db:"external_display_name" json:"external_display_name,omitempty"`
	LastSeenAt          time.Time                `db:"last_seen_at" json:"last_seen_at"`
}
