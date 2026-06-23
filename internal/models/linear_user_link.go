package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type LinearUserLinkSource string

const (
	LinearUserLinkSourceObserved    LinearUserLinkSource = "observed"
	LinearUserLinkSourceEmailMatch  LinearUserLinkSource = "email_match"
	LinearUserLinkSourceSelfLinked  LinearUserLinkSource = "self_linked"
	LinearUserLinkSourceAdminLinked LinearUserLinkSource = "admin_linked"
)

func (s LinearUserLinkSource) Validate() error {
	switch s {
	case LinearUserLinkSourceObserved, LinearUserLinkSourceEmailMatch, LinearUserLinkSourceSelfLinked, LinearUserLinkSourceAdminLinked:
		return nil
	default:
		return fmt.Errorf("invalid LinearUserLinkSource: %q", s)
	}
}

type LinearUserLink struct {
	ID                uuid.UUID            `db:"id" json:"id"`
	OrgID             uuid.UUID            `db:"org_id" json:"org_id"`
	IntegrationID     uuid.UUID            `db:"integration_id" json:"integration_id"`
	UserID            *uuid.UUID           `db:"user_id" json:"user_id,omitempty"`
	LinearWorkspaceID string               `db:"linear_workspace_id" json:"linear_workspace_id"`
	LinearUserID      string               `db:"linear_user_id" json:"linear_user_id"`
	LinearEmail       *string              `db:"linear_email" json:"linear_email,omitempty"`
	LinearDisplayName string               `db:"linear_display_name" json:"linear_display_name"`
	Source            LinearUserLinkSource `db:"source" json:"source"`
	LinkedAt          *time.Time           `db:"linked_at" json:"linked_at,omitempty"`
	CreatedAt         time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time            `db:"updated_at" json:"updated_at"`
}
