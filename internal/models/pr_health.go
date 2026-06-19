package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PullRequestMergeState string

const (
	PullRequestMergeStateUnknown             PullRequestMergeState = "unknown"
	PullRequestMergeStateMergeabilityPending PullRequestMergeState = "mergeability_pending"
	PullRequestMergeStateClean               PullRequestMergeState = "clean"
	PullRequestMergeStateConflicted          PullRequestMergeState = "conflicted"
	PullRequestMergeStateBehind              PullRequestMergeState = "behind"
	PullRequestMergeStateBlocked             PullRequestMergeState = "blocked"
)

func (s PullRequestMergeState) Validate() error {
	switch s {
	case PullRequestMergeStateUnknown,
		PullRequestMergeStateMergeabilityPending,
		PullRequestMergeStateClean,
		PullRequestMergeStateConflicted,
		PullRequestMergeStateBehind,
		PullRequestMergeStateBlocked:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestMergeState: %q", s)
	}
}

type PullRequestCheckCategory string

const (
	PullRequestCheckCategoryTest    PullRequestCheckCategory = "test"
	PullRequestCheckCategoryLint    PullRequestCheckCategory = "lint"
	PullRequestCheckCategoryBuild   PullRequestCheckCategory = "build"
	PullRequestCheckCategoryDeploy  PullRequestCheckCategory = "deploy"
	PullRequestCheckCategoryUnknown PullRequestCheckCategory = "unknown"
)

func (c PullRequestCheckCategory) Validate() error {
	switch c {
	case PullRequestCheckCategoryTest,
		PullRequestCheckCategoryLint,
		PullRequestCheckCategoryBuild,
		PullRequestCheckCategoryDeploy,
		PullRequestCheckCategoryUnknown:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestCheckCategory: %q", c)
	}
}

type PullRequestCheckStatus string

const (
	PullRequestCheckStatusPassed  PullRequestCheckStatus = "passed"
	PullRequestCheckStatusFailed  PullRequestCheckStatus = "failed"
	PullRequestCheckStatusPending PullRequestCheckStatus = "pending"
)

func (s PullRequestCheckStatus) Validate() error {
	switch s {
	case PullRequestCheckStatusPassed,
		PullRequestCheckStatusFailed,
		PullRequestCheckStatusPending:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestCheckStatus: %q", s)
	}
}

type PullRequestHealthEnrichmentStatus string

const (
	PullRequestHealthEnrichmentStatusNotRequested PullRequestHealthEnrichmentStatus = "not_requested"
	PullRequestHealthEnrichmentStatusPending      PullRequestHealthEnrichmentStatus = "pending"
	PullRequestHealthEnrichmentStatusReady        PullRequestHealthEnrichmentStatus = "ready"
	PullRequestHealthEnrichmentStatusFailed       PullRequestHealthEnrichmentStatus = "failed"
	PullRequestHealthEnrichmentStatusStale        PullRequestHealthEnrichmentStatus = "stale"
)

func (s PullRequestHealthEnrichmentStatus) Validate() error {
	switch s {
	case PullRequestHealthEnrichmentStatusNotRequested,
		PullRequestHealthEnrichmentStatusPending,
		PullRequestHealthEnrichmentStatusReady,
		PullRequestHealthEnrichmentStatusFailed,
		PullRequestHealthEnrichmentStatusStale:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestHealthEnrichmentStatus: %q", s)
	}
}

type PullRequestMergeWhenReadyState string

const (
	PullRequestMergeWhenReadyStateOff       PullRequestMergeWhenReadyState = "off"
	PullRequestMergeWhenReadyStateQueued    PullRequestMergeWhenReadyState = "queued"
	PullRequestMergeWhenReadyStateMerging   PullRequestMergeWhenReadyState = "merging"
	PullRequestMergeWhenReadyStateSucceeded PullRequestMergeWhenReadyState = "succeeded"
	PullRequestMergeWhenReadyStateFailed    PullRequestMergeWhenReadyState = "failed"
	PullRequestMergeWhenReadyStateCancelled PullRequestMergeWhenReadyState = "cancelled"
)

func (s PullRequestMergeWhenReadyState) Validate() error {
	switch s {
	case PullRequestMergeWhenReadyStateOff,
		PullRequestMergeWhenReadyStateQueued,
		PullRequestMergeWhenReadyStateMerging,
		PullRequestMergeWhenReadyStateSucceeded,
		PullRequestMergeWhenReadyStateFailed,
		PullRequestMergeWhenReadyStateCancelled:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestMergeWhenReadyState: %q", s)
	}
}

type PullRequestRepairActionType string

const (
	PullRequestRepairActionTypeFixTests         PullRequestRepairActionType = "fix_tests"
	PullRequestRepairActionTypeResolveConflicts PullRequestRepairActionType = "resolve_conflicts"
)

func (a PullRequestRepairActionType) Validate() error {
	switch a {
	case PullRequestRepairActionTypeFixTests,
		PullRequestRepairActionTypeResolveConflicts:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestRepairActionType: %q", a)
	}
}

type PullRequestCheckSummary struct {
	Name       string                   `json:"name"`
	Category   PullRequestCheckCategory `json:"category"`
	Status     PullRequestCheckStatus   `json:"status"`
	Provider   string                   `json:"provider,omitempty"`
	DetailsURL string                   `json:"details_url,omitempty"`
	Summary    string                   `json:"summary,omitempty"`
}

type PullRequestHealthSummary struct {
	MergeState       PullRequestMergeState     `json:"merge_state"`
	HasConflicts     bool                      `json:"has_conflicts"`
	FailingTestCount int                       `json:"failing_test_count"`
	NeedsAgentAction bool                      `json:"needs_agent_action"`
	ChecksConfirmed  bool                      `json:"checks_confirmed,omitempty"`
	Checks           []PullRequestCheckSummary `json:"checks,omitempty"`
}

type PullRequestHealthSnapshot struct {
	PullRequestID       uuid.UUID                         `db:"pull_request_id" json:"pull_request_id"`
	OrgID               uuid.UUID                         `db:"org_id" json:"org_id"`
	Version             int64                             `db:"version" json:"version"`
	HeadSHA             string                            `db:"head_sha" json:"head_sha"`
	BaseSHA             string                            `db:"base_sha" json:"base_sha"`
	SummaryJSON         json.RawMessage                   `db:"summary_json" json:"summary_json"`
	ConflictPayload     json.RawMessage                   `db:"conflict_payload" json:"conflict_payload,omitempty"`
	FailingTestsPayload json.RawMessage                   `db:"failing_tests_payload" json:"failing_tests_payload,omitempty"`
	PayloadSizeBytes    int                               `db:"payload_size_bytes" json:"payload_size_bytes"`
	EnrichmentStatus    PullRequestHealthEnrichmentStatus `db:"enrichment_status" json:"enrichment_status"`
	EnrichedAt          *time.Time                        `db:"enriched_at" json:"enriched_at,omitempty"`
	CreatedAt           time.Time                         `db:"created_at" json:"created_at"`
}

type PullRequestHealthCurrent struct {
	PullRequestID      uuid.UUID                         `db:"pull_request_id" json:"pull_request_id"`
	OrgID              uuid.UUID                         `db:"org_id" json:"org_id"`
	Version            int64                             `db:"version" json:"version"`
	HeadSHA            string                            `db:"head_sha" json:"head_sha"`
	BaseSHA            string                            `db:"base_sha" json:"base_sha"`
	SummaryJSON        json.RawMessage                   `db:"summary_json" json:"summary_json"`
	SummaryPreviewJSON json.RawMessage                   `db:"summary_preview_json" json:"summary_preview_json,omitempty"`
	EnrichmentStatus   PullRequestHealthEnrichmentStatus `db:"enrichment_status" json:"enrichment_status"`
	EnrichedAt         *time.Time                        `db:"enriched_at" json:"enriched_at,omitempty"`
	CreatedAt          time.Time                         `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time                         `db:"updated_at" json:"updated_at"`
}

type PullRequestHealthResponse struct {
	PullRequestID                uuid.UUID                         `json:"pull_request_id"`
	PullRequestNumber            int                               `json:"pull_request_number"`
	Repository                   string                            `json:"repository"`
	URL                          string                            `json:"url"`
	Status                       PullRequestStatus                 `json:"status"`
	HeadSHA                      string                            `json:"head_sha"`
	BaseSHA                      string                            `json:"base_sha"`
	HealthVersion                int64                             `json:"health_version"`
	MergeState                   PullRequestMergeState             `json:"merge_state"`
	HasConflicts                 bool                              `json:"has_conflicts"`
	FailingTestCount             int                               `json:"failing_test_count"`
	NeedsAgentAction             bool                              `json:"needs_agent_action"`
	GitHubStateSyncedAt          *time.Time                        `json:"github_state_synced_at,omitempty"`
	Summary                      string                            `json:"summary"`
	Checks                       []PullRequestCheckSummary         `json:"checks,omitempty"`
	ChecksConfirmed              bool                              `json:"checks_confirmed"`
	CanResolveConflicts          bool                              `json:"can_resolve_conflicts"`
	CanFixTests                  bool                              `json:"can_fix_tests"`
	CanMerge                     bool                              `json:"can_merge"`
	ActiveRepairs                []PullRequestActiveRepair         `json:"active_repairs,omitempty"`
	EnrichmentStatus             PullRequestHealthEnrichmentStatus `json:"enrichment_status"`
	EnrichmentRequested          bool                              `json:"enrichment_requested"`
	EnrichmentReady              bool                              `json:"enrichment_ready"`
	ConflictDetailAvailable      bool                              `json:"conflict_detail_available"`
	FailingTestDetailAvailable   bool                              `json:"failing_test_detail_available"`
	ObsoleteActiveRepairSessions bool                              `json:"obsolete_active_repair_sessions,omitempty"`
	MergeWhenReady               PullRequestMergeWhenReadyStatus   `json:"merge_when_ready"`
}

type PullRequestMergeWhenReadyStatus struct {
	State                  PullRequestMergeWhenReadyState `json:"state"`
	RequestedByUserID      *uuid.UUID                     `json:"requested_by_user_id,omitempty"`
	RequestedAt            *time.Time                     `json:"requested_at,omitempty"`
	RequestedHeadSHA       string                         `json:"requested_head_sha,omitempty"`
	RequestedHealthVersion *int64                         `json:"requested_health_version,omitempty"`
	LastError              string                         `json:"last_error,omitempty"`
}

type PullRequestActiveRepair struct {
	ActionType    PullRequestRepairActionType `json:"action_type"`
	SessionID     uuid.UUID                   `json:"session_id"`
	ThreadID      *uuid.UUID                  `json:"thread_id,omitempty"`
	SessionStatus SessionStatus               `json:"session_status"`
	HealthVersion int64                       `json:"health_version"`
}

type PullRequestRepairResponse struct {
	SessionID        uuid.UUID                   `json:"session_id"`
	ThreadID         *uuid.UUID                  `json:"thread_id,omitempty"`
	Mode             string                      `json:"mode"`
	ReusedInFlight   bool                        `json:"reused_in_flight"`
	HeadSHA          string                      `json:"head_sha"`
	BaseSHA          string                      `json:"base_sha"`
	HealthVersion    int64                       `json:"health_version"`
	RepairActionType PullRequestRepairActionType `json:"repair_action_type"`
}

type PullRequestRepairWorkspaceMode string

const (
	PullRequestRepairWorkspaceModeSnapshotContinuation PullRequestRepairWorkspaceMode = "snapshot_continuation"
	PullRequestRepairWorkspaceModePRHeadReconstruction PullRequestRepairWorkspaceMode = "pr_head_reconstruction"
)

func (m PullRequestRepairWorkspaceMode) Validate() error {
	switch m {
	case PullRequestRepairWorkspaceModeSnapshotContinuation,
		PullRequestRepairWorkspaceModePRHeadReconstruction:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestRepairWorkspaceMode: %q", m)
	}
}

type PullRequestMergeMethod string

const (
	PullRequestMergeMethodMerge  PullRequestMergeMethod = "merge"
	PullRequestMergeMethodSquash PullRequestMergeMethod = "squash"
	PullRequestMergeMethodRebase PullRequestMergeMethod = "rebase"
)

type PullRequestMergeResponse struct {
	Merged      bool                   `json:"merged"`
	SHA         string                 `json:"sha"`
	Message     string                 `json:"message"`
	MergeMethod PullRequestMergeMethod `json:"merge_method"`
}

type PullRequestRepairRun struct {
	ID                 uuid.UUID                      `db:"id" json:"id"`
	OrgID              uuid.UUID                      `db:"org_id" json:"org_id"`
	PullRequestID      uuid.UUID                      `db:"pull_request_id" json:"pull_request_id"`
	SessionID          uuid.UUID                      `db:"session_id" json:"session_id"`
	ThreadID           *uuid.UUID                     `db:"thread_id" json:"thread_id,omitempty"`
	ActionType         PullRequestRepairActionType    `db:"action_type" json:"action_type"`
	HealthVersion      int64                          `db:"health_version" json:"health_version"`
	HeadSHA            string                         `db:"head_sha" json:"head_sha,omitempty"`
	BaseSHA            string                         `db:"base_sha" json:"base_sha,omitempty"`
	WorkspaceMode      PullRequestRepairWorkspaceMode `db:"workspace_mode" json:"workspace_mode"`
	Active             bool                           `db:"active" json:"active"`
	ObsoletedByVersion *int64                         `db:"obsoleted_by_version" json:"obsoleted_by_version,omitempty"`
	CreatedAt          time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time                      `db:"updated_at" json:"updated_at"`
}

type PullRequestUpdatedEvent struct {
	PullRequestID uuid.UUID `json:"pull_request_id"`
	Version       int64     `json:"version"`
	HeadSHA       string    `json:"head_sha"`
	BaseSHA       string    `json:"base_sha"`
	SyncedAt      time.Time `json:"synced_at"`
}
