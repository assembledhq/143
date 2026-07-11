package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ChangesetStatus string

const (
	ChangesetStatusPlanned                ChangesetStatus = "planned"
	ChangesetStatusMaterializing          ChangesetStatus = "materializing"
	ChangesetStatusPublishedBranch        ChangesetStatus = "published_branch"
	ChangesetStatusPROpen                 ChangesetStatus = "pr_open"
	ChangesetStatusNeedsRestack           ChangesetStatus = "needs_restack"
	ChangesetStatusRestacking             ChangesetStatus = "restacking"
	ChangesetStatusRestackConflict        ChangesetStatus = "restack_conflict"
	ChangesetStatusExternalUpdateDetected ChangesetStatus = "external_update_detected"
	ChangesetStatusReady                  ChangesetStatus = "ready"
	ChangesetStatusMerged                 ChangesetStatus = "merged"
	ChangesetStatusAbandoned              ChangesetStatus = "abandoned"
)

func (s ChangesetStatus) Validate() error {
	switch s {
	case ChangesetStatusPlanned, ChangesetStatusMaterializing, ChangesetStatusPublishedBranch,
		ChangesetStatusPROpen, ChangesetStatusNeedsRestack, ChangesetStatusRestacking,
		ChangesetStatusRestackConflict, ChangesetStatusExternalUpdateDetected,
		ChangesetStatusReady, ChangesetStatusMerged, ChangesetStatusAbandoned:
		return nil
	default:
		return fmt.Errorf("invalid ChangesetStatus: %q", s)
	}
}

type SessionChangeset struct {
	ID                    uuid.UUID       `db:"id" json:"id"`
	OrgID                 uuid.UUID       `db:"org_id" json:"org_id"`
	SessionID             uuid.UUID       `db:"session_id" json:"session_id"`
	IsPrimary             bool            `db:"is_primary" json:"is_primary"`
	OrderIndex            int             `db:"order_index" json:"order_index"`
	Title                 string          `db:"title" json:"title"`
	Summary               string          `db:"summary" json:"summary"`
	Status                ChangesetStatus `db:"status" json:"status"`
	TargetBranch          string          `db:"target_branch" json:"target_branch"`
	BaseBranch            string          `db:"base_branch" json:"base_branch"`
	WorkingBranch         *string         `db:"working_branch" json:"working_branch,omitempty"`
	StackedOnChangesetID  *uuid.UUID      `db:"stacked_on_changeset_id" json:"stacked_on_changeset_id,omitempty"`
	HeadSHA               *string         `db:"head_sha" json:"head_sha,omitempty"`
	ExpectedRemoteHeadSHA *string         `db:"expected_remote_head_sha" json:"expected_remote_head_sha,omitempty"`
	BaseHeadSHA           *string         `db:"base_head_sha" json:"base_head_sha,omitempty"`
	PRCreationState       PRCreationState `db:"pr_creation_state" json:"pr_creation_state"`
	PRCreationError       *string         `db:"pr_creation_error" json:"pr_creation_error,omitempty"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at" json:"updated_at"`
}
