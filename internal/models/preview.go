package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// =============================================================================
// Database-mapped models
// =============================================================================

// PreviewInstance is the core preview lifecycle record.
type PreviewInstance struct {
	ID                                uuid.UUID                    `db:"id" json:"id"`
	SessionID                         uuid.UUID                    `db:"session_id" json:"session_id"`
	PreviewTargetID                   *uuid.UUID                   `db:"preview_target_id" json:"preview_target_id,omitempty"`
	OrgID                             uuid.UUID                    `db:"org_id" json:"org_id"`
	UserID                            uuid.UUID                    `db:"user_id" json:"user_id"`
	ProfileName                       string                       `db:"profile_name" json:"profile_name"`
	Name                              string                       `db:"name" json:"name"`
	Status                            PreviewStatus                `db:"status" json:"status"`
	Provider                          string                       `db:"provider" json:"provider"`
	WorkerNodeID                      string                       `db:"worker_node_id" json:"worker_node_id"`
	PreviewHandle                     string                       `db:"preview_handle" json:"preview_handle"`
	PrimaryService                    string                       `db:"primary_service" json:"primary_service"`
	Port                              int                          `db:"port" json:"port"`
	ConfigDigest                      string                       `db:"config_digest" json:"config_digest"`
	BaseCommitSHA                     string                       `db:"base_commit_sha" json:"base_commit_sha"`
	LastAccessedAt                    time.Time                    `db:"last_accessed_at" json:"last_accessed_at"`
	ExpiresAt                         time.Time                    `db:"expires_at" json:"expires_at"`
	StoppedAt                         *time.Time                   `db:"stopped_at" json:"stopped_at,omitempty"`
	LastPath                          string                       `db:"last_path" json:"last_path"`
	MemoryLimitMB                     int                          `db:"memory_limit_mb" json:"memory_limit_mb"`
	CPULimitMillis                    int                          `db:"cpu_limit_millis" json:"cpu_limit_millis"`
	DiskLimitMB                       int                          `db:"disk_limit_mb" json:"disk_limit_mb"`
	RecycleConfig                     json.RawMessage              `db:"recycle_config" json:"-"`
	RecycleSandbox                    json.RawMessage              `db:"recycle_sandbox" json:"-"`
	CurrentPhase                      string                       `db:"current_phase" json:"current_phase,omitempty"`
	RequestID                         *string                      `db:"request_id" json:"request_id,omitempty"`
	Error                             string                       `db:"error" json:"error,omitempty"`
	CreatedAt                         time.Time                    `db:"created_at" json:"created_at"`
	UpdatedAt                         time.Time                    `db:"updated_at" json:"updated_at"`
	RecycledAt                        time.Time                    `db:"recycled_at" json:"recycled_at"`
	RecycleScheduledAt                *time.Time                   `db:"recycle_scheduled_at" json:"recycle_scheduled_at,omitempty"`
	SourceWorkspaceRevision           *int64                       `db:"source_workspace_revision" json:"source_workspace_revision,omitempty"`
	SourceWorkspaceRevisionUpdatedAt  *time.Time                   `db:"source_workspace_revision_updated_at" json:"source_workspace_revision_updated_at,omitempty"`
	RuntimeWorkspaceRevision          *int64                       `db:"runtime_workspace_revision" json:"runtime_workspace_revision,omitempty"`
	RuntimeWorkspaceRevisionUpdatedAt *time.Time                   `db:"runtime_workspace_revision_updated_at" json:"runtime_workspace_revision_updated_at,omitempty"`
	RuntimeWorkspaceRevisionSource    PreviewRuntimeRevisionSource `db:"runtime_workspace_revision_source" json:"runtime_workspace_revision_source,omitempty"`
	UnavailableReason                 PreviewUnavailableReason     `db:"unavailable_reason" json:"unavailable_reason,omitempty"`
	// PreviewHoldingContainer marks this preview as a holder of the session's
	// sandbox container. It pairs with Session.TurnHoldingContainer as the
	// durable refcount that keeps the container alive between turns.
	PreviewHoldingContainer bool `db:"preview_holding_container" json:"preview_holding_container"`
}

// PreviewRuntime is the live worker attachment for a preview instance. Preview
// instances are durable user-facing records; runtimes are leased worker-owned
// serving attachments and are authoritative for preview proxy routing.
type PreviewRuntime struct {
	ID                uuid.UUID                `db:"id" json:"id"`
	OrgID             uuid.UUID                `db:"org_id" json:"org_id"`
	PreviewInstanceID uuid.UUID                `db:"preview_instance_id" json:"preview_instance_id"`
	RuntimeEpoch      int                      `db:"runtime_epoch" json:"runtime_epoch"`
	WorkerNodeID      string                   `db:"worker_node_id" json:"worker_node_id"`
	EndpointURL       string                   `db:"endpoint_url" json:"endpoint_url"`
	PreviewHandle     string                   `db:"preview_handle" json:"preview_handle"`
	PrimaryPort       int                      `db:"primary_port" json:"primary_port"`
	Status            PreviewRuntimeStatus     `db:"status" json:"status"`
	LeaseExpiresAt    time.Time                `db:"lease_expires_at" json:"lease_expires_at"`
	LastHeartbeatAt   time.Time                `db:"last_heartbeat_at" json:"last_heartbeat_at"`
	DrainRequestedAt  *time.Time               `db:"drain_requested_at" json:"drain_requested_at,omitempty"`
	StoppedAt         *time.Time               `db:"stopped_at" json:"stopped_at,omitempty"`
	Error             string                   `db:"error" json:"error,omitempty"`
	UnavailableReason PreviewUnavailableReason `db:"unavailable_reason" json:"unavailable_reason,omitempty"`
	CreatedAt         time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                `db:"updated_at" json:"updated_at"`
}

type PreviewUnavailableReason string

const (
	PreviewUnavailableReasonNone                PreviewUnavailableReason = ""
	PreviewUnavailableReasonOwnerLost           PreviewUnavailableReason = "owner_lost"
	PreviewUnavailableReasonDeployDrainTimeout  PreviewUnavailableReason = "deploy_drain_timeout"
	PreviewUnavailableReasonHostMaintenance     PreviewUnavailableReason = "host_maintenance"
	PreviewUnavailableReasonEmergencyForce      PreviewUnavailableReason = "emergency_force"
	PreviewUnavailableReasonLeaseExpired        PreviewUnavailableReason = "lease_expired"
	PreviewUnavailableReasonEndpointUnreachable PreviewUnavailableReason = "endpoint_unreachable"
)

func (r PreviewUnavailableReason) Validate() error {
	switch r {
	case PreviewUnavailableReasonNone,
		PreviewUnavailableReasonOwnerLost,
		PreviewUnavailableReasonDeployDrainTimeout,
		PreviewUnavailableReasonHostMaintenance,
		PreviewUnavailableReasonEmergencyForce,
		PreviewUnavailableReasonLeaseExpired,
		PreviewUnavailableReasonEndpointUnreachable:
		return nil
	default:
		return fmt.Errorf("invalid PreviewUnavailableReason: %q", r)
	}
}

// PreviewTarget is the branch/commit/config tuple a preview runtime attempts
// to render. Runtime attempts live in preview_instances.
type PreviewTarget struct {
	ID                   uuid.UUID         `db:"id" json:"id"`
	OrgID                uuid.UUID         `db:"org_id" json:"org_id"`
	RepositoryID         uuid.UUID         `db:"repository_id" json:"repository_id"`
	Branch               string            `db:"branch" json:"branch"`
	CommitSHA            string            `db:"commit_sha" json:"commit_sha"`
	PreviewConfigName    string            `db:"preview_config_name" json:"preview_config_name,omitempty"`
	ResolvedConfigDigest string            `db:"resolved_config_digest" json:"resolved_config_digest,omitempty"`
	SourceType           PreviewSourceType `db:"source_type" json:"source_type"`
	SourceID             string            `db:"source_id" json:"source_id,omitempty"`
	SourceURL            string            `db:"source_url" json:"source_url,omitempty"`
	CreatedByUserID      uuid.UUID         `db:"created_by_user_id" json:"created_by_user_id"`
	RequestID            *string           `db:"request_id" json:"request_id,omitempty"`
	PreviewGroupID       *uuid.UUID        `db:"preview_group_id" json:"preview_group_id,omitempty"`
	CreatedAt            time.Time         `db:"created_at" json:"created_at"`
}

// PreviewGroup is the current user-facing preview surface for a branch, PR,
// source, session, or explicitly pinned commit.
type PreviewGroup struct {
	ID                uuid.UUID         `db:"id" json:"preview_group_id"`
	OrgID             uuid.UUID         `db:"org_id" json:"org_id"`
	RepositoryID      uuid.UUID         `db:"repository_id" json:"repository_id"`
	GroupKind         PreviewGroupKind  `db:"group_kind" json:"group_kind"`
	Branch            string            `db:"branch" json:"branch"`
	PreviewConfigName string            `db:"preview_config_name" json:"preview_config_name,omitempty"`
	PullRequestNumber *int              `db:"pull_request_number" json:"pull_request_number,omitempty"`
	SourceType        PreviewSourceType `db:"source_type" json:"source_type"`
	SourceID          string            `db:"source_id" json:"source_id,omitempty"`
	SourceURL         string            `db:"source_url" json:"source_url,omitempty"`
	CurrentTargetID   *uuid.UUID        `db:"current_target_id" json:"current_target_id,omitempty"`
	LatestCommitSHA   string            `db:"latest_commit_sha" json:"latest_commit_sha,omitempty"`
	CurrentStatus     string            `db:"current_status" json:"current_status"`
	Pinned            bool              `db:"pinned" json:"pinned"`
	CreatedByUserID   *uuid.UUID        `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt         time.Time         `db:"created_at" json:"created_at"`
	LastActivityAt    time.Time         `db:"last_activity_at" json:"last_activity_at"`
}

// PreviewLaunchRecommendation is a compact product recommendation for grouped
// preview rows. It keeps the frontend from re-encoding backend lifecycle rules.
type PreviewLaunchRecommendation struct {
	Action         PreviewLaunchAction `json:"action"`
	PrimaryLabel   string              `json:"primary_label"`
	SecondaryLabel string              `json:"secondary_label,omitempty"`
	Message        string              `json:"message,omitempty"`
}

// PreviewCurrentSummary is the API/list shape for the grouped current preview index.
type PreviewCurrentSummary struct {
	PreviewGroup
	RepositoryFullName    string                      `db:"repository_full_name" json:"repository_full_name"`
	Status                PreviewStatus               `db:"status" json:"status"`
	Freshness             PreviewCurrentFreshness     `db:"freshness" json:"freshness"`
	RunningCommitSHA      string                      `db:"running_commit_sha" json:"running_commit_sha,omitempty"`
	CurrentPreviewID      *uuid.UUID                  `db:"current_preview_id" json:"current_preview_id,omitempty"`
	PreviewURL            *string                     `db:"-" json:"preview_url,omitempty"`
	StableURL             string                      `db:"-" json:"stable_url"`
	ExpiresAt             *time.Time                  `db:"expires_at" json:"expires_at,omitempty"`
	StoppedAt             *time.Time                  `db:"stopped_at" json:"stopped_at,omitempty"`
	StoppedReason         PreviewStoppedReason        `db:"stopped_reason" json:"stopped_reason,omitempty"`
	Error                 string                      `db:"error" json:"error,omitempty"`
	CurrentPhase          string                      `db:"current_phase" json:"current_phase,omitempty"`
	AttemptCount          int                         `db:"attempt_count" json:"attempt_count"`
	TargetCount           int                         `db:"target_count" json:"target_count"`
	Resumable             bool                        `db:"resumable" json:"resumable"`
	ResumeEstimateSeconds *int                        `db:"resume_estimate_seconds" json:"resume_estimate_seconds,omitempty"`
	Launch                PreviewLaunchRecommendation `db:"-" json:"launch"`
}

type PreviewTargetHistory struct {
	TargetID          uuid.UUID         `db:"target_id" json:"target_id"`
	CommitSHA         string            `db:"commit_sha" json:"commit_sha"`
	PreviewConfigName string            `db:"preview_config_name" json:"preview_config_name,omitempty"`
	SourceType        PreviewSourceType `db:"source_type" json:"source_type"`
	SourceID          string            `db:"source_id" json:"source_id,omitempty"`
	CreatedAt         time.Time         `db:"created_at" json:"created_at"`
	IsLatestHead      bool              `db:"is_latest_head" json:"is_latest_head"`
}

// RepositoryPreviewPolicy stores the per-repository auto-preview mode.
type RepositoryPreviewPolicy struct {
	ID                          uuid.UUID                 `db:"id" json:"id"`
	OrgID                       uuid.UUID                 `db:"org_id" json:"org_id"`
	RepositoryID                uuid.UUID                 `db:"repository_id" json:"repository_id"`
	AutoMode                    PreviewAutoMode           `db:"auto_mode" json:"auto_mode"`
	SessionPrewarmMode          PreviewSessionPrewarmMode `db:"session_prewarm_mode" json:"session_prewarm_mode"`
	SessionPrewarmUntrustedFork bool                      `db:"session_prewarm_untrusted_fork" json:"session_prewarm_untrusted_fork"`
	PRPreviewSurfacesEnabled    bool                      `db:"pr_preview_surfaces_enabled" json:"pr_preview_surfaces_enabled"`
	GitHubPRCommentEnabled      bool                      `db:"github_pr_comment_enabled" json:"github_pr_comment_enabled"`
	GitHubCommitStatusEnabled   bool                      `db:"github_commit_status_enabled" json:"github_commit_status_enabled"`
	UpdatedByUserID             uuid.UUID                 `db:"updated_by_user_id" json:"updated_by_user_id"`
	CreatedAt                   time.Time                 `db:"created_at" json:"created_at"`
	UpdatedAt                   time.Time                 `db:"updated_at" json:"updated_at"`
}

// RepositoryPreviewPolicySummary is the settings-page view of repository
// policy with repository identity and PR volume.
type RepositoryPreviewPolicySummary struct {
	RepositoryID                   uuid.UUID                 `db:"repository_id" json:"repository_id"`
	RepositoryFullName             string                    `db:"repository_full_name" json:"repository_full_name"`
	AutoMode                       PreviewAutoMode           `db:"auto_mode" json:"auto_mode"`
	SessionPrewarmMode             PreviewSessionPrewarmMode `db:"session_prewarm_mode" json:"session_prewarm_mode"`
	SessionPrewarmUntrustedFork    bool                      `db:"session_prewarm_untrusted_fork" json:"session_prewarm_untrusted_fork"`
	PRPreviewSurfacesEnabled       bool                      `db:"pr_preview_surfaces_enabled" json:"pr_preview_surfaces_enabled"`
	GitHubPRCommentEnabled         bool                      `db:"github_pr_comment_enabled" json:"github_pr_comment_enabled"`
	GitHubCommitStatusEnabled      bool                      `db:"github_commit_status_enabled" json:"github_commit_status_enabled"`
	PreviewConfigured              bool                      `db:"preview_configured" json:"preview_configured"`
	PreviewSuccessRecorded         bool                      `db:"preview_success_recorded" json:"preview_success_recorded"`
	PreviewConfigNames             []string                  `db:"-" json:"preview_config_names,omitempty"`
	PreviewConfigDefaultName       string                    `db:"-" json:"preview_config_default_name,omitempty"`
	PreviewConfigRequiresSelection bool                      `db:"-" json:"preview_config_requires_selection,omitempty"`
	PreviewReady                   bool                      `db:"preview_ready" json:"preview_ready"`
	PreviewReadinessMissingReason  string                    `db:"preview_readiness_missing_reason" json:"preview_readiness_missing_reason,omitempty"`
	GitHubPRCommentPermissionOK    bool                      `db:"github_pr_comment_permission_ok" json:"github_pr_comment_permission_ok"`
	GitHubCommitStatusPermissionOK bool                      `db:"github_commit_status_permission_ok" json:"github_commit_status_permission_ok"`
	LastSurfaceSyncSHA             string                    `db:"last_surface_sync_sha" json:"last_surface_sync_sha,omitempty"`
	LastSurfaceSyncAt              *time.Time                `db:"last_surface_sync_at" json:"last_surface_sync_at,omitempty"`
	LastSurfaceSyncError           string                    `db:"last_surface_sync_error" json:"last_surface_sync_error,omitempty"`
	OpenPRCount                    int                       `db:"open_pr_count" json:"open_pr_count"`
	UpdatedAt                      *time.Time                `db:"updated_at" json:"updated_at,omitempty"`
}

// PreviewLink is a stable app-owned URL mapping to a branch preview target.
type PreviewLink struct {
	ID              uuid.UUID       `db:"id" json:"id"`
	OrgID           uuid.UUID       `db:"org_id" json:"org_id"`
	PreviewTargetID uuid.UUID       `db:"preview_target_id" json:"preview_target_id"`
	LinkType        PreviewLinkType `db:"link_type" json:"link_type"`
	Slug            string          `db:"slug" json:"slug"`
	RepositoryID    *uuid.UUID      `db:"repository_id" json:"repository_id,omitempty"`
	PRNumber        *int            `db:"pr_number" json:"pr_number,omitempty"`
	CreatedAt       time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at" json:"updated_at"`
}

// PreviewAPIToken is an org-scoped bearer credential for external preview API
// callers. The plaintext token is only returned at creation time; the DB stores
// a SHA-256 hash.
type PreviewAPIToken struct {
	ID              uuid.UUID   `db:"id" json:"id"`
	OrgID           uuid.UUID   `db:"org_id" json:"org_id"`
	Name            string      `db:"name" json:"name"`
	TokenHash       string      `db:"token_hash" json:"-"`
	Scopes          []string    `db:"scopes" json:"scopes"`
	RepositoryIDs   []uuid.UUID `db:"repository_ids" json:"repository_ids"`
	CreatedByUserID uuid.UUID   `db:"created_by_user_id" json:"created_by_user_id"`
	LastUsedAt      *time.Time  `db:"last_used_at" json:"last_used_at,omitempty"`
	RevokedAt       *time.Time  `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt       time.Time   `db:"created_at" json:"created_at"`
}

// BranchPreviewSummary is the list/get shape for stable branch-preview targets
// plus their latest active runtime, when one exists.
type BranchPreviewSummary struct {
	TargetID              uuid.UUID            `db:"target_id" json:"target_id"`
	PreviewID             *uuid.UUID           `db:"preview_id" json:"preview_id,omitempty"`
	RepositoryID          uuid.UUID            `db:"repository_id" json:"repository_id"`
	RepositoryFullName    string               `db:"repository_full_name" json:"repository_full_name,omitempty"`
	Branch                string               `db:"branch" json:"branch"`
	CommitSHA             string               `db:"commit_sha" json:"commit_sha"`
	PreviewConfigName     string               `db:"preview_config_name" json:"preview_config_name,omitempty"`
	SourceType            PreviewSourceType    `db:"source_type" json:"source_type"`
	SourceID              string               `db:"source_id" json:"source_id,omitempty"`
	SourceURL             string               `db:"source_url" json:"source_url,omitempty"`
	Status                string               `db:"status" json:"status"`
	CreatedAt             time.Time            `db:"created_at" json:"created_at"`
	SortCreatedAt         time.Time            `db:"sort_created_at" json:"-"`
	ExpiresAt             *time.Time           `db:"expires_at" json:"expires_at,omitempty"`
	StoppedAt             *time.Time           `db:"stopped_at" json:"stopped_at,omitempty"`
	StoppedReason         PreviewStoppedReason `db:"stopped_reason" json:"stopped_reason,omitempty"`
	CurrentPhase          string               `db:"current_phase" json:"current_phase,omitempty"`
	Error                 string               `db:"error" json:"error,omitempty"`
	Resumable             bool                 `db:"resumable" json:"resumable"`
	ResumeEstimateSeconds *int                 `db:"resume_estimate_seconds" json:"resume_estimate_seconds,omitempty"`
}

// PreviewService tracks the state of a single service within a multi-service preview.
type PreviewService struct {
	ID                uuid.UUID            `db:"id" json:"id"`
	PreviewInstanceID uuid.UUID            `db:"preview_instance_id" json:"preview_instance_id"`
	ServiceName       string               `db:"service_name" json:"service_name"`
	Role              PreviewServiceRole   `db:"role" json:"role"`
	Status            PreviewServiceStatus `db:"status" json:"status"`
	Command           []string             `db:"command" json:"command"`
	Cwd               string               `db:"cwd" json:"cwd"`
	Port              int                  `db:"port" json:"port"`
	PID               *int                 `db:"pid" json:"pid,omitempty"`
	Error             string               `db:"error" json:"error,omitempty"`
	CreatedAt         time.Time            `db:"created_at" json:"created_at"`
}

// PreviewInfrastructure tracks platform infrastructure containers (PostgreSQL, Redis, etc.).
type PreviewInfrastructure struct {
	ID                uuid.UUID          `db:"id" json:"id"`
	PreviewInstanceID uuid.UUID          `db:"preview_instance_id" json:"preview_instance_id"`
	InfraName         string             `db:"infra_name" json:"infra_name"`
	Template          string             `db:"template" json:"template"`
	ContainerID       string             `db:"container_id" json:"container_id"`
	Status            PreviewInfraStatus `db:"status" json:"status"`
	Host              string             `db:"host" json:"host"`
	Port              int                `db:"port" json:"port"`
	CredentialsHash   string             `db:"credentials_hash" json:"-"` // never expose in JSON
	Error             string             `db:"error" json:"error,omitempty"`
	CreatedAt         time.Time          `db:"created_at" json:"created_at"`
}

// PreviewSnapshot is a screenshot captured during a preview session.
type PreviewSnapshot struct {
	ID                uuid.UUID              `db:"id" json:"id"`
	PreviewInstanceID uuid.UUID              `db:"preview_instance_id" json:"preview_instance_id"`
	Trigger           PreviewSnapshotTrigger `db:"trigger" json:"trigger"`
	URLPath           string                 `db:"url_path" json:"url_path"`
	BlobRef           string                 `db:"blob_ref" json:"blob_ref"`
	ViewportWidth     int                    `db:"viewport_width" json:"viewport_width"`
	ViewportHeight    int                    `db:"viewport_height" json:"viewport_height"`
	ConsoleErrors     json.RawMessage        `db:"console_errors" json:"console_errors"`
	FileChanges       json.RawMessage        `db:"file_changes" json:"file_changes,omitempty"`
	CreatedAt         time.Time              `db:"created_at" json:"created_at"`
}

// PreviewLog is a lifecycle or diagnostic log entry for a preview.
type PreviewLog struct {
	ID                uuid.UUID       `db:"id" json:"id"`
	PreviewInstanceID uuid.UUID       `db:"preview_instance_id" json:"preview_instance_id"`
	OrgID             uuid.UUID       `db:"org_id" json:"org_id"`
	Level             string          `db:"level" json:"level"`
	Step              PreviewLogStep  `db:"step" json:"step"`
	Message           string          `db:"message" json:"message"`
	Metadata          json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	CreatedAt         time.Time       `db:"created_at" json:"created_at"`
}

// PreviewAccessSession tracks a bootstrap-token-derived preview access session.
type PreviewAccessSession struct {
	ID                uuid.UUID  `db:"id" json:"id"`
	OrgID             uuid.UUID  `db:"org_id" json:"org_id"`
	UserID            uuid.UUID  `db:"user_id" json:"user_id"`
	PreviewInstanceID uuid.UUID  `db:"preview_instance_id" json:"preview_instance_id"`
	SessionTokenHash  string     `db:"session_token_hash" json:"-"` // never expose
	IssuedAt          time.Time  `db:"issued_at" json:"issued_at"`
	ExpiresAt         time.Time  `db:"expires_at" json:"expires_at"`
	RevokedAt         *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	LastAccessedAt    time.Time  `db:"last_accessed_at" json:"last_accessed_at"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
}

// PreviewStartupCache tracks filesystem snapshot metadata for fast startup.
// SnapshotKey is the exact (lockfiles, commit, config) hash; BaseKey drops the
// commit so a start at a new commit can find a base snapshot to restore and
// patch with a git diff. CommitSHA records which commit the blob was built at.
type PreviewStartupCache struct {
	ID           uuid.UUID `db:"id" json:"id"`
	OrgID        uuid.UUID `db:"org_id" json:"org_id"`
	RepoID       uuid.UUID `db:"repo_id" json:"repo_id"`
	SnapshotKey  string    `db:"snapshot_key" json:"snapshot_key"`
	BaseKey      string    `db:"base_key" json:"base_key"`
	CommitSHA    string    `db:"commit_sha" json:"commit_sha"`
	BlobPath     string    `db:"blob_path" json:"blob_path"`
	SizeBytes    int64     `db:"size_bytes" json:"size_bytes"`
	WorkerNodeID string    `db:"worker_node_id" json:"worker_node_id"`
	LastUsedAt   time.Time `db:"last_used_at" json:"last_used_at"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// PreviewSecretBundle is one insert-only version of a repo-scoped preview
// secret bundle. Encrypted configs are never exposed through JSON responses.
type PreviewSecretBundle struct {
	ID                     uuid.UUID       `db:"id" json:"id"`
	OrgID                  uuid.UUID       `db:"org_id" json:"org_id"`
	RepositoryID           uuid.UUID       `db:"repository_id" json:"repository_id"`
	Name                   string          `db:"name" json:"name"`
	Active                 bool            `db:"active" json:"active"`
	SourceType             string          `db:"source_type" json:"source_type"`
	SourceConfigEncrypted  json.RawMessage `db:"source_config_encrypted" json:"-"`
	OutputsConfigEncrypted json.RawMessage `db:"outputs_config_encrypted" json:"-"`
	ExposurePolicy         string          `db:"exposure_policy" json:"exposure_policy"`
	CreatedByUserID        uuid.UUID       `db:"created_by_user_id" json:"created_by_user_id"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
}

type PreviewSecretBundleSource struct {
	Type   string            `json:"type"`
	Values map[string]string `json:"values,omitempty"`
}

type PreviewSecretBundleOutput struct {
	Type    string            `json:"type"`
	Values  map[string]string `json:"values,omitempty"`
	Path    string            `json:"path,omitempty"`
	Format  string            `json:"format,omitempty"`
	Mode    string            `json:"mode,omitempty"`
	Content json.RawMessage   `json:"content,omitempty"`
	Value   string            `json:"value,omitempty"`
}

type PreviewSecretBundleSummary struct {
	ID              uuid.UUID                    `json:"id"`
	RepositoryID    uuid.UUID                    `json:"repository_id"`
	Name            string                       `json:"name"`
	SourceType      string                       `json:"source_type"`
	ExposurePolicy  string                       `json:"exposure_policy"`
	Outputs         []PreviewSecretOutputSummary `json:"outputs"`
	CreatedByUserID uuid.UUID                    `json:"created_by_user_id"`
	CreatedAt       time.Time                    `json:"created_at"`
}

type PreviewSecretBundleTestResult struct {
	Status string                     `json:"status"`
	Bundle PreviewSecretBundleSummary `json:"bundle"`
	Error  string                     `json:"error,omitempty"`
}

type PreviewSecretBundleRevealResult struct {
	Bundle  PreviewSecretBundleSummary  `json:"bundle"`
	Source  PreviewSecretBundleSource   `json:"source"`
	Outputs []PreviewSecretBundleOutput `json:"outputs"`
}

type PreviewSecretOutputSummary struct {
	Type   string   `json:"type"`
	Env    []string `json:"env,omitempty"`
	Path   string   `json:"path,omitempty"`
	Format string   `json:"format,omitempty"`
}

// PRPreviewState tracks the PR comment lifecycle for preview integration.
type PRPreviewState struct {
	ID                     uuid.UUID       `db:"id" json:"id"`
	OrgID                  uuid.UUID       `db:"org_id" json:"org_id"`
	RepoID                 uuid.UUID       `db:"repo_id" json:"repo_id"`
	PRNumber               int             `db:"pr_number" json:"pr_number"`
	GitHubCommentID        *int64          `db:"github_comment_id" json:"github_comment_id,omitempty"`
	LastPreviewInstanceID  *uuid.UUID      `db:"last_preview_instance_id" json:"last_preview_instance_id,omitempty"`
	LastScreenshotBlobPath string          `db:"last_screenshot_blob_path" json:"last_screenshot_blob_path,omitempty"`
	LastVisualDiffBlobPath string          `db:"last_visual_diff_blob_path" json:"last_visual_diff_blob_path,omitempty"`
	BaseSnapshotKey        string          `db:"base_snapshot_key" json:"base_snapshot_key,omitempty"`
	Status                 PRPreviewStatus `db:"status" json:"status"`
	LastSurfaceSyncSHA     string          `db:"last_surface_sync_sha" json:"last_surface_sync_sha,omitempty"`
	LastSurfaceSyncAt      *time.Time      `db:"last_surface_sync_at" json:"last_surface_sync_at,omitempty"`
	LastSurfaceSyncError   string          `db:"last_surface_sync_error" json:"last_surface_sync_error,omitempty"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt              time.Time       `db:"updated_at" json:"updated_at"`
}

// =============================================================================
// Preview configuration types (parsed from the nested preview section in
// .143/config.json)
// =============================================================================

// PreviewConfig is the parsed representation of the preview section in
// .143/config.json.
// Internally, single-service configs are normalized to multi-service format.
type PreviewConfig struct {
	Version        string                          `json:"version"`
	Name           string                          `json:"name"`
	Primary        string                          `json:"primary"`
	Install        *PreviewInstallConfig           `json:"install,omitempty"`
	Services       map[string]ServiceConfig        `json:"services"`
	Infrastructure map[string]InfrastructureConfig `json:"infrastructure,omitempty"`
	Resources      PreviewResourceRequirements     `json:"resources,omitempty"`
	Secrets        []PreviewSecretBundleRef        `json:"secrets,omitempty"`
	Credentials    CredentialConfig                `json:"credentials"`
	Network        NetworkConfig                   `json:"network"`
	Progressive    bool                            `json:"progressive,omitempty"`

	RuntimeSecretEnv   map[string]map[string]string `json:"-"`
	RuntimeSecretFiles []PreviewRuntimeSecretFile   `json:"-"`
}

// PreviewResourceRequirements follows the Kubernetes resources shape for
// preview-level resource requests and limits.
type PreviewResourceRequirements struct {
	Requests PreviewResourceList `json:"requests,omitempty"`
	Limits   PreviewResourceList `json:"limits,omitempty"`
}

// PreviewResourceList defines CPU, memory, and ephemeral storage quantities.
type PreviewResourceList struct {
	CPU              string `json:"cpu,omitempty"`
	Memory           string `json:"memory,omitempty"`
	EphemeralStorage string `json:"ephemeral-storage,omitempty"`
}

// PreviewInstallConfig defines an optional platform-managed install phase that
// runs before preview services start.
type PreviewInstallConfig struct {
	Command        []string                   `json:"command"`
	Cwd            string                     `json:"cwd,omitempty"`
	Lockfiles      []string                   `json:"lockfiles,omitempty"`
	CleanPaths     []string                   `json:"clean_paths,omitempty"`
	VerifyPaths    []string                   `json:"verify_paths,omitempty"`
	TimeoutSeconds int                        `json:"timeout_seconds,omitempty"`
	Cache          *PreviewInstallCacheConfig `json:"cache,omitempty"`
}

// PreviewInstallCacheConfig controls optional dependency artifact caching for
// preview.install. Enabled is a pointer so named config merge/defaulting can
// distinguish omitted from explicitly false.
type PreviewInstallCacheConfig struct {
	Enabled        *bool                             `json:"enabled,omitempty"`
	Paths          []string                          `json:"paths,omitempty"`
	PackageManager *PreviewPackageManagerCacheConfig `json:"package_manager,omitempty"`
	Prewarm        *PreviewInstallPrewarmCacheConfig `json:"prewarm,omitempty"`
	Build          *PreviewBuildCacheConfig          `json:"build,omitempty"`
}

type PreviewPackageManagerCacheConfig struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Include []string `json:"include,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// PreviewBuildCacheConfig controls caching of incremental build artifacts
// (e.g. Turborepo's local cache) that services populate while booting. Unlike
// the install-artifact cache, build caches are saved after services report
// ready and use latest-wins keying: the build tool is trusted to content-hash
// its own entries, so a stale blob degrades to partial hits rather than wrong
// output.
type PreviewBuildCacheConfig struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

type PreviewInstallPrewarmCacheConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type PreviewCacheKind string

const (
	PreviewCacheKindInstallArtifact PreviewCacheKind = "install_artifact"
	PreviewCacheKindPackageManager  PreviewCacheKind = "package_manager"
	PreviewCacheKindBuildArtifact   PreviewCacheKind = "build_artifact"
)

type PreviewCacheRoot string

const (
	PreviewCacheRootWorkDir PreviewCacheRoot = "workdir"
	PreviewCacheRootHomeDir PreviewCacheRoot = "homedir"
)

type PreviewDependencyCache struct {
	ID           uuid.UUID        `db:"id" json:"id"`
	OrgID        uuid.UUID        `db:"org_id" json:"org_id"`
	RepoID       uuid.UUID        `db:"repo_id" json:"repo_id"`
	CacheKind    PreviewCacheKind `db:"cache_kind" json:"cache_kind"`
	CacheKey     string           `db:"cache_key" json:"cache_key"`
	PlacementKey string           `db:"placement_key" json:"placement_key"`
	BlobKey      string           `db:"blob_key" json:"-"`
	SizeBytes    int64            `db:"size_bytes" json:"size_bytes"`
	Metadata     json.RawMessage  `db:"metadata" json:"metadata"`
	LastUsedAt   time.Time        `db:"last_used_at" json:"last_used_at"`
	CreatedAt    time.Time        `db:"created_at" json:"created_at"`
}

type PreviewDependencyCacheLocation struct {
	ID           uuid.UUID        `db:"id" json:"id"`
	OrgID        uuid.UUID        `db:"org_id" json:"org_id"`
	RepoID       uuid.UUID        `db:"repo_id" json:"repo_id"`
	CacheKind    PreviewCacheKind `db:"cache_kind" json:"cache_kind"`
	CacheKey     string           `db:"cache_key" json:"cache_key"`
	PlacementKey string           `db:"placement_key" json:"placement_key"`
	WorkerNodeID string           `db:"worker_node_id" json:"worker_node_id"`
	SizeBytes    int64            `db:"size_bytes" json:"size_bytes"`
	LastUsedAt   time.Time        `db:"last_used_at" json:"last_used_at"`
	CreatedAt    time.Time        `db:"created_at" json:"created_at"`
}

type PreviewCachePrewarmRun struct {
	ID                     uuid.UUID  `db:"id" json:"id"`
	OrgID                  uuid.UUID  `db:"org_id" json:"org_id"`
	RepoID                 uuid.UUID  `db:"repo_id" json:"repo_id"`
	Source                 string     `db:"source" json:"source"`
	SourceID               string     `db:"source_id" json:"source_id"`
	CacheScopeKey          string     `db:"cache_scope_key" json:"cache_scope_key"`
	JobID                  *uuid.UUID `db:"job_id" json:"job_id,omitempty"`
	WorkerNodeID           string     `db:"worker_node_id" json:"worker_node_id"`
	Status                 string     `db:"status" json:"status"`
	PackageManagerCacheKey string     `db:"package_manager_cache_key" json:"package_manager_cache_key"`
	DependencyCacheKey     string     `db:"dependency_cache_key" json:"dependency_cache_key"`
	ConfigDigest           string     `db:"config_digest" json:"config_digest"`
	CommitSHA              string     `db:"commit_sha" json:"commit_sha"`
	WorkspaceRevision      int64      `db:"workspace_revision" json:"workspace_revision"`
	Error                  string     `db:"error" json:"error"`
	StartedAt              *time.Time `db:"started_at" json:"started_at,omitempty"`
	CompletedAt            *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt              time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt              time.Time  `db:"updated_at" json:"updated_at"`
}

type SessionPreviewPrewarmRun struct {
	ID                uuid.UUID                  `db:"id" json:"id"`
	OrgID             uuid.UUID                  `db:"org_id" json:"org_id"`
	RepositoryID      uuid.UUID                  `db:"repository_id" json:"repository_id"`
	SessionID         uuid.UUID                  `db:"session_id" json:"session_id"`
	WorkspaceRevision int64                      `db:"workspace_revision" json:"workspace_revision"`
	ConfigDigest      string                     `db:"config_digest" json:"config_digest"`
	Mode              PreviewSessionPrewarmMode  `db:"mode" json:"mode"`
	Decision          PreviewSpeculativeDecision `db:"decision" json:"decision"`
	Confidence        float64                    `db:"confidence" json:"confidence"`
	Reason            string                     `db:"reason" json:"reason"`
	Explanation       string                     `db:"explanation" json:"explanation"`
	Status            string                     `db:"status" json:"status"`
	JobID             *uuid.UUID                 `db:"job_id" json:"job_id,omitempty"`
	PreviewID         *uuid.UUID                 `db:"preview_id" json:"preview_id,omitempty"`
	PreviewGroupID    *uuid.UUID                 `db:"preview_group_id" json:"preview_group_id,omitempty"`
	CapacitySnapshot  json.RawMessage            `db:"capacity_snapshot" json:"capacity_snapshot,omitempty"`
	Error             string                     `db:"error" json:"error"`
	CreatedAt         time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                  `db:"updated_at" json:"updated_at"`
	StartedAt         *time.Time                 `db:"started_at" json:"started_at,omitempty"`
	CompletedAt       *time.Time                 `db:"completed_at" json:"completed_at,omitempty"`
	PanelOpenedAt     *time.Time                 `db:"panel_opened_at" json:"panel_opened_at,omitempty"`
}

// ServiceConfig defines a single service within a preview.
type ServiceConfig struct {
	Command []string          `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Port    int               `json:"port"`
	Env     map[string]string `json:"env,omitempty"`
	Ready   ReadinessProbe    `json:"ready"`
}

// InfrastructureConfig defines a platform-provided infrastructure service.
type InfrastructureConfig struct {
	Template   string            `json:"template"`
	InitScript string            `json:"init_script,omitempty"`
	InjectEnv  map[string]string `json:"inject_env,omitempty"`
	InjectInto []string          `json:"inject_into,omitempty"`
}

// CredentialConfig references an admin-managed credential set.
type CredentialConfig struct {
	Mode          string   `json:"mode"`
	CredentialSet string   `json:"credential_set,omitempty"`
	Env           []string `json:"env,omitempty"`
	InjectInto    []string `json:"inject_into,omitempty"`
}

// PreviewSecretBundleRef is the non-secret repo-authored reference to an
// admin-managed preview secret bundle.
type PreviewSecretBundleRef struct {
	Bundle   string   `json:"bundle"`
	Services []string `json:"services,omitempty"`
	Env      []string `json:"env,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// PreviewRuntimeSecretFile is populated by the preview secret resolver at
// launch time. Content intentionally has no JSON encoding so plaintext cannot
// leak into persisted preview recycle config.
type PreviewRuntimeSecretFile struct {
	Services []string `json:"services,omitempty"`
	Path     string   `json:"path"`
	Mode     string   `json:"mode,omitempty"`
	Content  []byte   `json:"-"`
}

// NetworkConfig controls sandbox network access.
type NetworkConfig struct {
	Mode         string   `json:"mode"`
	Destinations []string `json:"destinations,omitempty"`
}

// ReadinessProbe defines how to check if a service is ready.
type ReadinessProbe struct {
	HTTPPath       string `json:"http_path"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// ResourceLimits defines the memory, CPU, and disk limits for a preview.
type ResourceLimits struct {
	MemoryMiB int `json:"memory_mib"`
	CPUMillis int `json:"cpu_millis"`
	DiskMiB   int `json:"disk_mib"`
}

// =============================================================================
// Preview Inspector types (headless browser interaction)
// =============================================================================

// ScreenshotOpts configures a screenshot capture.
type ScreenshotOpts struct {
	Path      string        `json:"path"`
	ViewportW int           `json:"viewport_w"`
	ViewportH int           `json:"viewport_h"`
	FullPage  bool          `json:"full_page"`
	Delay     time.Duration `json:"delay"`
}

// DefaultScreenshotOpts returns sensible defaults for screenshot capture.
func DefaultScreenshotOpts() ScreenshotOpts {
	return ScreenshotOpts{
		Path:      "/",
		ViewportW: 1280,
		ViewportH: 720,
		FullPage:  false,
		Delay:     time.Second,
	}
}

// ScreenshotResult is the output of a screenshot capture.
type ScreenshotResult struct {
	PNG           []byte           `json:"-"`
	PageTitle     string           `json:"page_title"`
	ConsoleErrors []ConsoleMessage `json:"console_errors,omitempty"`
	URL           string           `json:"url"`
	CapturedAt    time.Time        `json:"captured_at"`
}

// ConsoleMessage is a browser console message captured during inspection.
type ConsoleMessage struct {
	Level  string    `json:"level"` // "error", "warning", "log", "info"
	Text   string    `json:"text"`
	Source string    `json:"source,omitempty"`
	LineNo int       `json:"line_no,omitempty"`
	URL    string    `json:"url,omitempty"`
	Time   time.Time `json:"time"`
}

// ScreencastResult is the output of a screencast recording.
type ScreencastResult struct {
	Format     string        `json:"format"` // "gif" or "webm"
	Data       []byte        `json:"-"`
	Duration   time.Duration `json:"duration"`
	FrameCount int           `json:"frame_count"`
}

// InteractionStep defines a single browser interaction action.
type InteractionStep struct {
	Action     string        `json:"action"`     // "click", "type", "navigate", "wait", "scroll", "select"
	Selector   string        `json:"selector"`   // CSS selector for click/type/select targets
	Value      string        `json:"value"`      // text to type, URL to navigate to, option to select
	WaitFor    string        `json:"wait_for"`   // CSS selector or "networkidle" or "load"
	Timeout    time.Duration `json:"timeout"`    // max wait for this step, default 10s
	Screenshot bool          `json:"screenshot"` // capture a screenshot after this step
}

// InteractionResult is the output of an interaction replay.
type InteractionResult struct {
	Steps         []StepResult     `json:"steps"`
	TotalTime     time.Duration    `json:"total_time"`
	FinalURL      string           `json:"final_url"`
	ConsoleErrors []ConsoleMessage `json:"console_errors,omitempty"`
}

// StepResult is the outcome of a single interaction step.
type StepResult struct {
	StepIndex  int               `json:"step_index"`
	Action     string            `json:"action"`
	Success    bool              `json:"success"`
	Error      string            `json:"error,omitempty"`
	Screenshot *ScreenshotResult `json:"screenshot,omitempty"`
	Duration   time.Duration     `json:"duration"`
	URL        string            `json:"url"`
}

// MultiViewportOpts configures a multi-viewport screenshot capture.
type MultiViewportOpts struct {
	Path      string         `json:"path"`
	Viewports []ViewportSpec `json:"viewports"`
	Delay     time.Duration  `json:"delay"`
}

// ViewportSpec defines a named viewport size.
type ViewportSpec struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// DefaultViewports returns the standard mobile/tablet/desktop viewport set.
func DefaultViewports() []ViewportSpec {
	return []ViewportSpec{
		{Name: "mobile", Width: 375, Height: 812},
		{Name: "tablet", Width: 768, Height: 1024},
		{Name: "desktop", Width: 1280, Height: 720},
	}
}

// MultiViewportResult is the output of a multi-viewport capture.
type MultiViewportResult struct {
	Captures []ViewportCapture `json:"captures"`
}

// ViewportCapture is the screenshot result for a single viewport.
type ViewportCapture struct {
	Viewport      ViewportSpec     `json:"viewport"`
	Screenshot    ScreenshotResult `json:"screenshot"`
	ConsoleErrors []ConsoleMessage `json:"console_errors,omitempty"`
}

// VisualDiff is the structured result of comparing two preview snapshots.
type VisualDiff struct {
	BeforeSnapshotID string        `json:"before_snapshot_id"`
	AfterSnapshotID  string        `json:"after_snapshot_id"`
	PixelDiffPercent float64       `json:"pixel_diff_percent"`
	DiffRegions      []DiffRegion  `json:"diff_regions,omitempty"`
	DOMChanges       []DOMChange   `json:"dom_changes,omitempty"`
	StyleChanges     []StyleChange `json:"style_changes,omitempty"`
	OverlayPNG       []byte        `json:"-"`
	Summary          string        `json:"summary"`
}

// DiffRegion identifies a bounding box of visual change.
type DiffRegion struct {
	BoundingBox Rect   `json:"bounding_box"`
	Severity    string `json:"severity"` // "minor", "major", "new", "removed"
}

// Rect is a bounding box.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// DOMChange describes a structural change in the DOM tree.
type DOMChange struct {
	Selector   string `json:"selector"`
	ChangeType string `json:"change_type"` // "added", "removed", "text_changed", "attribute_changed", "moved"
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
}

// StyleChange describes a computed style change on an element.
type StyleChange struct {
	Selector string `json:"selector"`
	Property string `json:"property"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Token    string `json:"token,omitempty"` // design token name if applicable
}

// ElementInfo is the full metadata about a DOM element at a given point.
type ElementInfo struct {
	TagName        string            `json:"tag_name"`
	ComponentName  string            `json:"component_name,omitempty"`
	ComponentFile  string            `json:"component_file,omitempty"`
	ComponentLine  int               `json:"component_line,omitempty"`
	Props          map[string]any    `json:"props,omitempty"`
	ComponentTree  []string          `json:"component_tree,omitempty"`
	BoundingBox    Rect              `json:"bounding_box"`
	ComputedStyles map[string]string `json:"computed_styles,omitempty"`
	DesignTokens   map[string]string `json:"design_tokens,omitempty"`
	InnerText      string            `json:"inner_text,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
	DOMPath        string            `json:"dom_path"`
	ParentContext  string            `json:"parent_context,omitempty"`
	Framework      string            `json:"framework,omitempty"` // "react", "vue", "svelte", "angular", ""
}

// DOMCaptureOpts configures a DOM snapshot capture.
type DOMCaptureOpts struct {
	Path      string   `json:"path"`
	Selectors []string `json:"selectors,omitempty"` // specific elements to include; empty = full page
}

// DOMSnapshot is a serialized snapshot of the DOM tree.
type DOMSnapshot struct {
	HTML       string        `json:"html"`
	Elements   []ElementInfo `json:"elements,omitempty"`
	CapturedAt time.Time     `json:"captured_at"`
}

// =============================================================================
// Preview status response types (for API)
// =============================================================================

// PreviewStatusResponse is the API response for GET /sessions/{id}/preview.
type PreviewStatusResponse struct {
	Instance        *PreviewInstance        `json:"instance"`
	Services        []PreviewService        `json:"services"`
	Infrastructure  []PreviewInfrastructure `json:"infrastructure,omitempty"`
	PreviewOrigin   string                  `json:"preview_origin,omitempty"`
	Freshness       *PreviewFreshness       `json:"freshness,omitempty"`
	StartupEstimate *PreviewStartupEstimate `json:"startup_estimate,omitempty"`
	Prewarm         *PreviewPrewarmStatus   `json:"prewarm,omitempty"`
}

type PreviewPrewarmStatus struct {
	State                 string `json:"state"`
	WorkspaceRevision     int64  `json:"workspace_revision"`
	ResumeEstimateSeconds *int   `json:"resume_estimate_seconds,omitempty"`
	PreviewID             string `json:"preview_id,omitempty"`
	Error                 string `json:"error,omitempty"`
}

type PreviewFreshness struct {
	State                             PreviewFreshnessState        `json:"state"`
	CurrentWorkspaceRevision          int64                        `json:"current_workspace_revision"`
	CurrentWorkspaceRevisionUpdatedAt time.Time                    `json:"current_workspace_revision_updated_at"`
	PreviewWorkspaceRevision          *int64                       `json:"preview_workspace_revision,omitempty"`
	PreviewWorkspaceRevisionUpdatedAt *time.Time                   `json:"preview_workspace_revision_updated_at,omitempty"`
	RuntimeWorkspaceRevision          *int64                       `json:"runtime_workspace_revision,omitempty"`
	RuntimeWorkspaceRevisionUpdatedAt *time.Time                   `json:"runtime_workspace_revision_updated_at,omitempty"`
	RuntimeWorkspaceRevisionSource    PreviewRuntimeRevisionSource `json:"runtime_workspace_revision_source,omitempty"`
	RestartRequired                   bool                         `json:"restart_required"`
	RestartReasons                    []PreviewRestartReason       `json:"restart_reasons,omitempty"`
	Reason                            string                       `json:"reason,omitempty"`
}

type PreviewRestartReason struct {
	Kind   PreviewRestartReasonKind `json:"kind"`
	Path   string                   `json:"path,omitempty"`
	Detail string                   `json:"detail,omitempty"`
}

type PreviewStartupEstimate struct {
	Label       string `json:"label"`
	P50Seconds  int    `json:"p50_seconds"`
	SampleCount int    `json:"sample_count"`
	Confidence  string `json:"confidence"`
}

// PreviewDetectionResult is the API response for GET /repos/{owner}/{repo}/preview/detect.
type PreviewDetectionResult struct {
	Readiness            PreviewReadiness      `json:"readiness"`
	ConfigName           string                `json:"config_name,omitempty"`
	Services             []string              `json:"services,omitempty"`
	PrimaryService       string                `json:"primary_service,omitempty"`
	Infrastructure       []string              `json:"infrastructure,omitempty"`
	MissingCredentials   []MissingCredential   `json:"missing_credentials,omitempty"`
	MissingSecretBundles []MissingSecretBundle `json:"missing_secret_bundles,omitempty"`
	MissingDestinations  []string              `json:"missing_destinations,omitempty"`
	ValidationErrors     []string              `json:"validation_errors,omitempty"`
}

// MissingCredential describes a credential set that needs admin setup.
type MissingCredential struct {
	CredentialSet string   `json:"credential_set"`
	EnvVars       []string `json:"env_vars"`
}

// MissingSecretBundle describes a preview secret bundle that needs admin setup.
type MissingSecretBundle struct {
	Bundle   string   `json:"bundle"`
	Services []string `json:"services,omitempty"`
	Env      []string `json:"env,omitempty"`
	Files    []string `json:"files,omitempty"`
	Status   string   `json:"status"`
}

// =============================================================================
// Design Mode types
// =============================================================================

// DesignModeFeedback is the structured message sent from Design Mode to the agent.
type DesignModeFeedback struct {
	Type          string        `json:"type"` // "design_mode_feedback" or "visual_edit" or "reorder"
	Elements      []ElementInfo `json:"elements"`
	Instruction   string        `json:"instruction,omitempty"`
	Annotations   []Annotation  `json:"annotations,omitempty"`
	ScreenshotRef string        `json:"screenshot_ref,omitempty"`
	// Visual edit fields.
	StyleEdits []StyleEdit `json:"style_edits,omitempty"`
	// Reorder-specific fields.
	Direction string       `json:"direction,omitempty"` // "up", "down", "left", "right"
	Parent    *ElementInfo `json:"parent,omitempty"`
	Siblings  []string     `json:"siblings,omitempty"`
}

// Annotation is an SVG annotation drawn on the Design Mode overlay.
type Annotation struct {
	Type string `json:"type"` // "rectangle", "arrow", "freehand"
	Path string `json:"path"` // SVG path data relative to iframe viewport
}

// VisualEdit is the structured message sent from Visual Editing to the agent.
type VisualEdit struct {
	Element          ElementInfo `json:"element"`
	Changes          []StyleEdit `json:"changes"`
	BeforeScreenshot string      `json:"before_screenshot,omitempty"`
	AfterScreenshot  string      `json:"after_screenshot,omitempty"`
}

// StyleEdit is a single CSS property change from Visual Editing.
type StyleEdit struct {
	Property string `json:"property"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
	OldToken string `json:"old_token,omitempty"` // design token name if applicable
	NewToken string `json:"new_token,omitempty"`
}

// =============================================================================
// Assertion types (for agent self-verification)
// =============================================================================

// PreviewAssertion is a single visual assertion the agent runs against the preview.
type PreviewAssertion struct {
	Type        string `json:"type"` // element_exists, element_text, element_style, element_count, no_console_errors, page_title, viewport_screenshot_match
	Selector    string `json:"selector,omitempty"`
	Property    string `json:"property,omitempty"`
	Value       string `json:"value,omitempty"`
	Contains    string `json:"contains,omitempty"`
	Visible     *bool  `json:"visible,omitempty"`
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
	Region      *Rect  `json:"region,omitempty"`
	Description string `json:"description,omitempty"` // for viewport_screenshot_match
}

// AssertionResult is the outcome of a single assertion check.
type AssertionResult struct {
	Assertion PreviewAssertion `json:"assertion"`
	Passed    bool             `json:"passed"`
	Actual    string           `json:"actual,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// AssertionResults is the aggregate outcome of running assertions.
type AssertionResults struct {
	Results []AssertionResult `json:"results"`
	Passed  int               `json:"passed"`
	Failed  int               `json:"failed"`
	Total   int               `json:"total"`
}
