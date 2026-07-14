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

type ChangesetRestackDeltaKind string

const (
	ChangesetRestackDeltaCleanReplay       ChangesetRestackDeltaKind = "clean_replay"
	ChangesetRestackDeltaMechanicalFallout ChangesetRestackDeltaKind = "mechanical_fallout"
	ChangesetRestackDeltaSemanticChange    ChangesetRestackDeltaKind = "semantic_change"
)

func (k ChangesetRestackDeltaKind) Validate() error {
	switch k {
	case ChangesetRestackDeltaCleanReplay, ChangesetRestackDeltaMechanicalFallout, ChangesetRestackDeltaSemanticChange:
		return nil
	default:
		return fmt.Errorf("invalid ChangesetRestackDeltaKind: %q", k)
	}
}

type SessionChangeset struct {
	ID                          uuid.UUID                  `db:"id" json:"id"`
	OrgID                       uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID                   uuid.UUID                  `db:"session_id" json:"session_id"`
	IsPrimary                   bool                       `db:"is_primary" json:"is_primary"`
	OrderIndex                  int                        `db:"order_index" json:"order_index"`
	Title                       string                     `db:"title" json:"title"`
	Summary                     string                     `db:"summary" json:"summary"`
	Status                      ChangesetStatus            `db:"status" json:"status"`
	TargetBranch                string                     `db:"target_branch" json:"target_branch"`
	BaseBranch                  string                     `db:"base_branch" json:"base_branch"`
	WorkingBranch               *string                    `db:"working_branch" json:"working_branch,omitempty"`
	StackedOnChangesetID        *uuid.UUID                 `db:"stacked_on_changeset_id" json:"stacked_on_changeset_id,omitempty"`
	HeadSHA                     *string                    `db:"head_sha" json:"head_sha,omitempty"`
	ExpectedRemoteHeadSHA       *string                    `db:"expected_remote_head_sha" json:"expected_remote_head_sha,omitempty"`
	BaseHeadSHA                 *string                    `db:"base_head_sha" json:"base_head_sha,omitempty"`
	WorktreePath                *string                    `db:"worktree_path" json:"worktree_path,omitempty"`
	MaterializationError        *string                    `db:"materialization_error" json:"materialization_error,omitempty"`
	MaterializedDiff            *string                    `db:"materialized_diff" json:"-"`
	RestackDeltaKind            *ChangesetRestackDeltaKind `db:"restack_delta_kind" json:"restack_delta_kind,omitempty"`
	RestackDeltaSummary         *string                    `db:"restack_delta_summary" json:"restack_delta_summary,omitempty"`
	RestackConfirmationRequired bool                       `db:"restack_confirmation_required" json:"restack_confirmation_required"`
	PRCreationState             PRCreationState            `db:"pr_creation_state" json:"pr_creation_state"`
	PRCreationError             *string                    `db:"pr_creation_error" json:"pr_creation_error,omitempty"`
	CreatedAt                   time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt                   time.Time                  `db:"updated_at" json:"updated_at"`
}

// ChangesetSummary is the stable session-detail and list representation. It
// deliberately omits internal push coordination and PR creation error fields.
type ChangesetSummary struct {
	ID                          uuid.UUID                  `db:"id" json:"id"`
	IsPrimary                   bool                       `db:"is_primary" json:"is_primary"`
	OrderIndex                  int                        `db:"order_index" json:"order_index"`
	Title                       string                     `db:"title" json:"title"`
	Summary                     string                     `db:"summary" json:"summary"`
	Status                      ChangesetStatus            `db:"status" json:"status"`
	TargetBranch                string                     `db:"target_branch" json:"target_branch"`
	BaseBranch                  string                     `db:"base_branch" json:"base_branch"`
	WorkingBranch               *string                    `db:"working_branch" json:"working_branch,omitempty"`
	StackedOnChangesetID        *uuid.UUID                 `db:"stacked_on_changeset_id" json:"stacked_on_changeset_id,omitempty"`
	HeadSHA                     *string                    `db:"head_sha" json:"head_sha,omitempty"`
	WorktreePath                *string                    `db:"worktree_path" json:"worktree_path,omitempty"`
	MaterializationError        *string                    `db:"materialization_error" json:"materialization_error,omitempty"`
	HasUnpushedChanges          bool                       `db:"has_unpushed_changes" json:"has_unpushed_changes"`
	RestackDeltaKind            *ChangesetRestackDeltaKind `db:"restack_delta_kind" json:"restack_delta_kind,omitempty"`
	RestackDeltaSummary         *string                    `db:"restack_delta_summary" json:"restack_delta_summary,omitempty"`
	RestackConfirmationRequired bool                       `db:"restack_confirmation_required" json:"restack_confirmation_required"`
	PullRequest                 *PullRequest               `json:"pull_request,omitempty"`
	CreatedAt                   time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt                   time.Time                  `db:"updated_at" json:"updated_at"`
}

type ChangesetLeaseType string

const (
	ChangesetLeaseTypeAgentTurn   ChangesetLeaseType = "agent_turn"
	ChangesetLeaseTypeMaterialize ChangesetLeaseType = "materialize"
	ChangesetLeaseTypePublish     ChangesetLeaseType = "publish"
	ChangesetLeaseTypeRestack     ChangesetLeaseType = "restack"
	ChangesetLeaseTypeReadiness   ChangesetLeaseType = "readiness"
	ChangesetLeaseTypePreview     ChangesetLeaseType = "preview"
)

func (t ChangesetLeaseType) Validate() error {
	switch t {
	case ChangesetLeaseTypeAgentTurn, ChangesetLeaseTypeMaterialize, ChangesetLeaseTypePublish,
		ChangesetLeaseTypeRestack, ChangesetLeaseTypeReadiness, ChangesetLeaseTypePreview:
		return nil
	default:
		return fmt.Errorf("invalid ChangesetLeaseType: %q", t)
	}
}

type SessionChangesetLease struct {
	ChangesetID uuid.UUID          `db:"changeset_id" json:"changeset_id"`
	OrgID       uuid.UUID          `db:"org_id" json:"org_id"`
	SessionID   uuid.UUID          `db:"session_id" json:"session_id"`
	HolderID    uuid.UUID          `db:"holder_id" json:"holder_id"`
	HolderType  ChangesetLeaseType `db:"holder_type" json:"holder_type"`
	HolderLabel string             `db:"holder_label" json:"holder_label"`
	AcquiredAt  time.Time          `db:"acquired_at" json:"acquired_at"`
	HeartbeatAt time.Time          `db:"heartbeat_at" json:"heartbeat_at"`
	ExpiresAt   time.Time          `db:"expires_at" json:"expires_at"`
}

type ChangesetStackState string

const (
	ChangesetStackStateOnePR                  ChangesetStackState = "one-pr"
	ChangesetStackStateDraft                  ChangesetStackState = "draft-stack"
	ChangesetStackStatePublished              ChangesetStackState = "published"
	ChangesetStackStateCoherent               ChangesetStackState = "coherent"
	ChangesetStackStateNeedsRestack           ChangesetStackState = "needs-restack"
	ChangesetStackStateRestacking             ChangesetStackState = "restacking"
	ChangesetStackStateBlocked                ChangesetStackState = "blocked"
	ChangesetStackStateExternalUpdateDetected ChangesetStackState = "external-update-detected"
	ChangesetStackStatePartiallyMerged        ChangesetStackState = "partially-merged"
	ChangesetStackStateMerged                 ChangesetStackState = "merged"
)

// DeriveChangesetStackState keeps stack health a pure function of durable
// changeset rows. Callers must pass the complete ordered session changeset list.
func DeriveChangesetStackState(changesets []ChangesetSummary) ChangesetStackState {
	active := make([]ChangesetSummary, 0, len(changesets))
	for _, changeset := range changesets {
		if changeset.Status != ChangesetStatusAbandoned {
			active = append(active, changeset)
		}
	}
	if len(active) <= 1 {
		return ChangesetStackStateOnePR
	}
	counts := make(map[ChangesetStatus]int)
	for _, changeset := range active {
		if changeset.RestackConfirmationRequired {
			return ChangesetStackStateBlocked
		}
		counts[changeset.Status]++
	}
	if counts[ChangesetStatusExternalUpdateDetected] > 0 {
		return ChangesetStackStateExternalUpdateDetected
	}
	if counts[ChangesetStatusRestackConflict] > 0 {
		return ChangesetStackStateBlocked
	}
	if counts[ChangesetStatusRestacking] > 0 {
		return ChangesetStackStateRestacking
	}
	if counts[ChangesetStatusNeedsRestack] > 0 {
		return ChangesetStackStateNeedsRestack
	}
	if counts[ChangesetStatusMerged] == len(active) {
		return ChangesetStackStateMerged
	}
	if counts[ChangesetStatusMerged] > 0 {
		return ChangesetStackStatePartiallyMerged
	}
	if counts[ChangesetStatusPlanned]+counts[ChangesetStatusMaterializing] > 0 {
		return ChangesetStackStateDraft
	}
	if counts[ChangesetStatusPublishedBranch]+counts[ChangesetStatusPROpen] == len(active) {
		return ChangesetStackStatePublished
	}
	return ChangesetStackStateCoherent
}

type ChangesetSplitPathAssignment struct {
	ChangesetID uuid.UUID `json:"changeset_id"`
	Paths       []string  `json:"paths"`
}

type ChangesetSplitDuplicate struct {
	Path         string      `json:"path"`
	ChangesetIDs []uuid.UUID `json:"changeset_ids"`
}

type ChangesetSplitConflict struct {
	Path        string    `json:"path"`
	ChangesetID uuid.UUID `json:"changeset_id"`
	Reason      string    `json:"reason"`
}

type ChangesetSplitOmission struct {
	Path              string    `json:"path"`
	Reason            string    `json:"reason"`
	ConfirmedByUserID uuid.UUID `json:"confirmed_by_user_id"`
	CreatedAt         time.Time `json:"created_at"`
}

type ChangesetSplitVerification string

const (
	ChangesetSplitVerificationPlanned  ChangesetSplitVerification = "planned"
	ChangesetSplitVerificationVerified ChangesetSplitVerification = "verified"
)

// ChangesetSplitStatus is derived from an immutable session diff snapshot and
// the current draft path assignments. It is progress state, not durable diff
// ownership after branches are materialized.
type ChangesetSplitStatus struct {
	Status               string                         `json:"status"`
	SourceDiffSnapshotID uuid.UUID                      `json:"source_diff_snapshot_id"`
	SourcePaths          []string                       `json:"source_paths"`
	Assignments          []ChangesetSplitPathAssignment `json:"assignments"`
	UnassignedPaths      []string                       `json:"unassigned_paths"`
	Duplicates           []ChangesetSplitDuplicate      `json:"duplicates"`
	Conflicts            []ChangesetSplitConflict       `json:"conflicts"`
	Omissions            []ChangesetSplitOmission       `json:"omissions"`
	UnexpectedPaths      []string                       `json:"unexpected_paths"`
	Verification         ChangesetSplitVerification     `json:"verification"`
	Complete             bool                           `json:"complete"`
}

func (c SessionChangeset) SummaryView() ChangesetSummary {
	return ChangesetSummary{
		ID: c.ID, IsPrimary: c.IsPrimary, OrderIndex: c.OrderIndex, Title: c.Title, Summary: c.Summary,
		Status: c.Status, TargetBranch: c.TargetBranch, BaseBranch: c.BaseBranch, WorkingBranch: c.WorkingBranch,
		StackedOnChangesetID: c.StackedOnChangesetID, HeadSHA: c.HeadSHA, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		WorktreePath: c.WorktreePath, MaterializationError: c.MaterializationError,
		RestackDeltaKind: c.RestackDeltaKind, RestackDeltaSummary: c.RestackDeltaSummary, RestackConfirmationRequired: c.RestackConfirmationRequired,
		HasUnpushedChanges: c.HeadSHA != nil && (c.ExpectedRemoteHeadSHA == nil || *c.HeadSHA != *c.ExpectedRemoteHeadSHA),
	}
}
