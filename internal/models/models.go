package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID        uuid.UUID       `db:"id" json:"id"`
	Name      string          `db:"name" json:"name"`
	Settings  json.RawMessage `db:"settings" json:"settings"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt time.Time       `db:"updated_at" json:"updated_at"`
}

type User struct {
	ID                     uuid.UUID `db:"id" json:"id"`
	OrgID                  uuid.UUID `db:"org_id" json:"org_id"`
	Email                  string    `db:"email" json:"email"`
	Name                   string    `db:"name" json:"name"`
	Role                   Role      `db:"role" json:"role"`
	GitHubID               *int64    `db:"github_id" json:"github_id,omitempty"`
	GitHubLogin            *string   `db:"github_login" json:"github_login,omitempty"`
	CapturedGitHubOrgLogin *string   `db:"-" json:"captured_github_org_login,omitempty"`
	// GitHubNoreplyEmail is the address used to attribute git commits so they
	// link back to the user's GitHub profile. Stored separately from Email
	// (the human-facing contact address) because GitHub only links commits
	// authored by either the user's verified primary email or one of their
	// noreply addresses (`{id}+{login}@users.noreply.github.com`). Populated
	// from `/user/emails` during OAuth, with a deterministic fallback.
	GitHubNoreplyEmail *string   `db:"github_noreply_email" json:"-"`
	AvatarURL          *string   `db:"avatar_url" json:"avatar_url,omitempty"`
	PasswordHash       *string   `db:"password_hash" json:"-"`
	GoogleID           *string   `db:"google_id" json:"google_id,omitempty"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
}

type UserWithSettings struct {
	ID          uuid.UUID `db:"id" json:"id"`
	OrgID       uuid.UUID `db:"org_id" json:"org_id"`
	Email       string    `db:"email" json:"email"`
	Name        string    `db:"name" json:"name"`
	Role        Role      `db:"role" json:"role"`
	GitHubID    *int64    `db:"github_id" json:"github_id,omitempty"`
	GitHubLogin *string   `db:"github_login" json:"github_login,omitempty"`
	AvatarURL   *string   `db:"avatar_url" json:"avatar_url,omitempty"`
	GoogleID    *string   `db:"google_id" json:"google_id,omitempty"`
	// EmailVerified reports whether the account's current email is attested
	// (OAuth provider claim, verification link, or emailed-invite claim).
	// Drives the frontend's "verify your email" prompt and email-domain
	// auto-join eligibility.
	EmailVerified bool         `json:"email_verified"`
	Settings      UserSettings `json:"settings"`
	CreatedAt     time.Time    `db:"created_at" json:"created_at"`
}

type AuthSession struct {
	ID     uuid.UUID `db:"id" json:"id"`
	UserID uuid.UUID `db:"user_id" json:"user_id"`
	OrgID  uuid.UUID `db:"org_id" json:"org_id"`
	// LastOrgID is the server-side hint for which membership to activate when a
	// client reconnects without echoing back an X-Active-Org-ID header (new tab,
	// cold reload). Nullable so that zero-membership users can still sign in and
	// so that deletion of the selected org via ON DELETE SET NULL does not
	// invalidate the session.
	LastOrgID *uuid.UUID `db:"last_org_id" json:"last_org_id,omitempty"`
	Token     string     `db:"token" json:"-"` // never expose token in JSON
	ExpiresAt time.Time  `db:"expires_at" json:"expires_at"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
}

type Integration struct {
	ID                          uuid.UUID           `db:"id" json:"id"`
	OrgID                       uuid.UUID           `db:"org_id" json:"org_id"`
	Provider                    IntegrationProvider `db:"provider" json:"provider"`
	Config                      json.RawMessage     `db:"config" json:"-"` // never expose config in JSON (contains secrets)
	GitHubAppInstalled          *bool               `db:"-" json:"github_app_installed,omitempty"`
	GitHubInstallationID        *int64              `db:"-" json:"github_installation_id,omitempty"`
	GitHubAccountLogin          *string             `db:"-" json:"github_account_login,omitempty"`
	GitHubRepoSelectionRequired *bool               `db:"-" json:"github_repo_selection_required,omitempty"`
	GitHubActiveRepoCount       *int                `db:"-" json:"github_active_repo_count,omitempty"`
	NotionWorkspaceID           *string             `db:"-" json:"notion_workspace_id,omitempty"`
	NotionWorkspaceName         *string             `db:"-" json:"notion_workspace_name,omitempty"`
	CircleCIProjectSlug         *string             `db:"-" json:"circleci_project_slug,omitempty"`
	MezmoDataset                *string             `db:"-" json:"mezmo_dataset,omitempty"`
	MezmoBaseURL                *string             `db:"-" json:"mezmo_base_url,omitempty"`
	// AuthError is a derived view of the auth-failure markers stamped into
	// config jsonb when a provider rejects our access token (currently only
	// Linear). Populated by ListIntegrations / Get* paths via deriveIntegrationStatus
	// so the integrations settings UI can render a "Reconnect" CTA without
	// leaking the rest of config (which contains secrets — see json:"-" above).
	AuthError    *IntegrationAuthError `db:"-" json:"auth_error,omitempty"`
	Status       IntegrationStatus     `db:"status" json:"status"`
	LastSyncedAt *time.Time            `db:"last_synced_at" json:"last_synced_at,omitempty"`
	CreatedAt    time.Time             `db:"created_at" json:"created_at"`
}

type GitHubInstallation struct {
	InstallationID      int64      `db:"installation_id" json:"installation_id"`
	AccountID           int64      `db:"account_id" json:"account_id"`
	AccountLogin        string     `db:"account_login" json:"account_login"`
	AccountType         *string    `db:"account_type" json:"account_type,omitempty"`
	RepositorySelection *string    `db:"repository_selection" json:"repository_selection,omitempty"`
	Status              string     `db:"status" json:"status"`
	RosterSyncedAt      *time.Time `db:"roster_synced_at" json:"roster_synced_at,omitempty"`
	CreatedAt           time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at" json:"updated_at"`
}

type GitHubInstallationOrgLink struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	OrgID           uuid.UUID  `db:"org_id" json:"org_id"`
	IntegrationID   *uuid.UUID `db:"integration_id" json:"integration_id,omitempty"`
	InstallationID  int64      `db:"installation_id" json:"installation_id"`
	AccountLogin    string     `db:"account_login" json:"account_login"`
	LinkedByUserID  *uuid.UUID `db:"linked_by_user_id" json:"linked_by_user_id,omitempty"`
	Status          string     `db:"status" json:"status"`
	AutoJoinEnabled bool       `db:"auto_join_enabled" json:"auto_join_enabled"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}

type GitHubOrgMember struct {
	InstallationID int64     `db:"installation_id" json:"installation_id"`
	GitHubUserID   int64     `db:"github_user_id" json:"github_user_id"`
	GitHubLogin    string    `db:"github_login" json:"github_login"`
	SyncedAt       time.Time `db:"synced_at" json:"synced_at"`
}

type GitHubOrgAutoJoinCandidate struct {
	OrgID          uuid.UUID `db:"org_id" json:"org_id"`
	OrgName        string    `db:"org_name" json:"org_name"`
	InstallationID int64     `db:"installation_id" json:"installation_id"`
	AccountLogin   string    `db:"account_login" json:"account_login"`
	AccountType    *string   `db:"account_type" json:"account_type,omitempty"`
	EnabledAt      time.Time `db:"enabled_at" json:"enabled_at"`
}

type GitHubOrgAutoJoinSummary struct {
	InstallationID     int64      `db:"installation_id" json:"installation_id"`
	AccountLogin       string     `db:"account_login" json:"account_login"`
	AccountType        *string    `db:"account_type" json:"account_type,omitempty"`
	AutoJoinEnabled    bool       `db:"auto_join_enabled" json:"auto_join_enabled"`
	RosterSyncedAt     *time.Time `db:"roster_synced_at" json:"roster_synced_at,omitempty"`
	CapturedByOtherOrg bool       `db:"captured_by_other_org" json:"captured_by_other_org"`
}

type GitHubRepositoryClaimCandidate struct {
	GitHubID       int64                       `json:"github_id"`
	FullName       string                      `json:"full_name"`
	DefaultBranch  string                      `json:"default_branch"`
	Private        bool                        `json:"private"`
	CloneURL       string                      `json:"clone_url"`
	InstallationID int64                       `json:"installation_id"`
	Status         GitHubRepositoryClaimStatus `json:"status"`
	RepositoryID   *uuid.UUID                  `json:"repository_id,omitempty"`
	OwnerOrgID     *uuid.UUID                  `json:"owner_org_id,omitempty"`
	OwnerOrgName   *string                     `json:"owner_org_name,omitempty"`
	CanTransfer    bool                        `json:"can_transfer"`
}

// IntegrationAuthError is the user-facing shape of an auth failure: a
// short reason string and the timestamp the failure was last observed.
// SECURITY: must not include raw provider responses or tokens — the
// reason field is set to a controlled string at the call site.
type IntegrationAuthError struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// IntegrationConfigAuthErrorKey / IntegrationConfigAuthErrorAtKey are the
// jsonb keys used to stamp an auth-error reason and timestamp into
// integrations.config. Defined here so the writer (linear service) and
// readers (api/handlers) share a single source of truth without forcing
// a dependency edge between those packages.
const (
	IntegrationConfigAuthErrorKey   = "last_auth_error"
	IntegrationConfigAuthErrorAtKey = "last_auth_error_at"
)

type Repository struct {
	ID             uuid.UUID        `db:"id" json:"id"`
	OrgID          uuid.UUID        `db:"org_id" json:"org_id"`
	IntegrationID  uuid.UUID        `db:"integration_id" json:"integration_id"`
	GitHubID       int64            `db:"github_id" json:"github_id"`
	FullName       string           `db:"full_name" json:"full_name"`
	DefaultBranch  string           `db:"default_branch" json:"default_branch"`
	Private        bool             `db:"private" json:"private"`
	Language       *string          `db:"language" json:"language,omitempty"`
	Description    *string          `db:"description" json:"description,omitempty"`
	CloneURL       string           `db:"clone_url" json:"clone_url"`
	InstallationID int64            `db:"installation_id" json:"installation_id"`
	Status         RepositoryStatus `db:"status" json:"status"`
	LastSyncedAt   *time.Time       `db:"last_synced_at" json:"last_synced_at,omitempty"`
	ContextQuality *float64         `db:"context_quality" json:"context_quality,omitempty"`
	Settings       json.RawMessage  `db:"settings" json:"settings"`
	CreatedAt      time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at" json:"updated_at"`
}

// RepositoryStatus is a typed string for the `repositories.status` column.
// Defined as a distinct type (not a plain string alias) so that boundary-layer
// APIs like RepositoryStore.SetStatus reject arbitrary callers passing raw
// strings and require one of the known constants below.
type RepositoryStatus string

const (
	RepositoryStatusActive       RepositoryStatus = "active"
	RepositoryStatusPaused       RepositoryStatus = "paused"
	RepositoryStatusDisconnected RepositoryStatus = "disconnected"
)

func (s RepositoryStatus) Validate() error {
	switch s {
	case RepositoryStatusActive, RepositoryStatusPaused, RepositoryStatusDisconnected:
		return nil
	default:
		return fmt.Errorf("invalid RepositoryStatus: %q", s)
	}
}

// IsActive reports whether the repo is currently usable for new work. Disconnected
// repos remain readable (existing sessions still load) but must be rejected from
// any code path that creates new sessions, runs, projects, or automations.
func (r Repository) IsActive() bool {
	return r.Status == RepositoryStatusActive
}

// RepoSummary is the API model for repository summary data in the context switcher.
type RepoSummary struct {
	RepositoryID        uuid.UUID `json:"repository_id"`
	FullName            string    `json:"full_name"`
	ActiveSessionCount  int       `json:"active_session_count"`
	LatestSessionStatus *string   `json:"latest_session_status"`
	ActiveProjectCount  int       `json:"active_project_count"`
}

// Issue is the unified, normalized issue from any source.
type Issue struct {
	ID                    uuid.UUID       `db:"id" json:"id"`
	OrgID                 uuid.UUID       `db:"org_id" json:"org_id"`
	ExternalID            string          `db:"external_id" json:"external_id"`
	Source                IssueSource     `db:"source" json:"source"`
	SourceIntegrationID   *uuid.UUID      `db:"source_integration_id" json:"source_integration_id,omitempty"`
	RepositoryID          *uuid.UUID      `db:"repository_id" json:"repository_id,omitempty"`
	Title                 string          `db:"title" json:"title"`
	Description           *string         `db:"description" json:"description,omitempty"`
	RawData               json.RawMessage `db:"raw_data" json:"-"`
	Status                IssueStatus     `db:"status" json:"status"`
	FirstSeenAt           time.Time       `db:"first_seen_at" json:"first_seen_at"`
	LastSeenAt            time.Time       `db:"last_seen_at" json:"last_seen_at"`
	OccurrenceCount       int             `db:"occurrence_count" json:"occurrence_count"`
	AffectedCustomerCount int             `db:"affected_customer_count" json:"affected_customer_count"`
	Severity              IssueSeverity   `db:"severity" json:"severity"`
	Tags                  []string        `db:"tags" json:"tags"`
	Fingerprint           string          `db:"fingerprint" json:"fingerprint"`
	DeletedAt             *time.Time      `db:"deleted_at" json:"-"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at" json:"updated_at"`
}

// Session represents an attempt to fix an issue via a coding agent.
//
// PrimaryIssueID is derived from the session_issue_links join table (role =
// 'primary') via the sessionPrimaryIssueIDColumn SELECT expression. It is the
// canonical way to identify a session's primary issue — there is no persisted
// primary_issue_id column on sessions. nil means the session has no primary
// issue linked (zero-issue sessions are a first-class execution path).
type Session struct {
	ID               uuid.UUID               `db:"id" json:"id"`
	PrimaryThreadID  *uuid.UUID              `db:"-" json:"-"`
	PrimaryIssueID   *uuid.UUID              `db:"primary_issue_id" json:"primary_issue_id"`
	OrgID            uuid.UUID               `db:"org_id" json:"org_id"`
	Origin           SessionOrigin           `db:"origin" json:"origin"`
	InteractionMode  SessionInteractionMode  `db:"interaction_mode" json:"interaction_mode"`
	ValidationPolicy SessionValidationPolicy `db:"validation_policy" json:"validation_policy"`
	LinkedIssues     []SessionIssueLink      `db:"-" json:"linked_issues,omitempty"`
	AgentType        AgentType               `db:"agent_type" json:"agent_type"`
	Status           SessionStatus           `db:"status" json:"status"`
	AutonomyLevel    SessionAutonomy         `db:"autonomy_level" json:"autonomy_level"`
	TokenMode        SessionTokenMode        `db:"token_mode" json:"token_mode"`
	ComplexityTier   *int                    `db:"complexity_tier" json:"complexity_tier,omitempty"`
	// ContainerID is the Docker container hosting the session's sandbox when
	// one is live. Non-null only while at least one holder
	// (TurnHoldingContainer or an active preview) is keeping it alive — see
	// SessionStore.AcquireTurnHold / ReleaseTurnHold for the state machine.
	ContainerID *string `db:"container_id" json:"container_id,omitempty"`
	// WorkerNodeID is the worker node currently owning the live sandbox
	// container referenced by ContainerID. Nil when the session has no live
	// container or when the row predates worker ownership tracking.
	WorkerNodeID *string `db:"worker_node_id" json:"worker_node_id,omitempty"`
	// TurnHoldingContainer marks the agent turn as a holder of the session's
	// sandbox container. Combined with PreviewInstance.PreviewHoldingContainer,
	// it is the durable refcount that decides when the container is destroyed.
	TurnHoldingContainer bool            `db:"turn_holding_container" json:"turn_holding_container"`
	StartedAt            *time.Time      `db:"started_at" json:"started_at,omitempty"`
	CompletedAt          *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
	TokenUsage           json.RawMessage `db:"token_usage" json:"token_usage,omitempty"`
	FailureExplanation   *string         `db:"failure_explanation" json:"failure_explanation,omitempty"`
	FailureCategory      *string         `db:"failure_category" json:"failure_category,omitempty"`
	FailureNextSteps     []string        `db:"failure_next_steps" json:"failure_next_steps,omitempty"`
	// FailureRetryAdvised uses plain bool (not *bool) because false is the
	// meaningful default — a session that hasn't failed never advises retry.
	// The DB column is NOT NULL DEFAULT false, so pgx scans cleanly into bool.
	FailureRetryAdvised bool             `db:"failure_retry_advised" json:"failure_retry_advised"`
	ParentSessionID     *uuid.UUID       `db:"parent_session_id" json:"parent_session_id,omitempty"`
	RevisionContext     json.RawMessage  `db:"revision_context" json:"revision_context,omitempty"`
	Error               *string          `db:"error" json:"error,omitempty"`
	ResultSummary       *string          `db:"result_summary" json:"result_summary,omitempty"`
	Diff                *string          `db:"diff" json:"diff,omitempty"`
	PMPlanID            *uuid.UUID       `db:"pm_plan_id" json:"pm_plan_id,omitempty"`
	Title               *string          `db:"title" json:"title,omitempty"`
	PMApproach          *string          `db:"pm_approach" json:"pm_approach,omitempty"`
	PMReasoning         *string          `db:"pm_reasoning" json:"pm_reasoning,omitempty"`
	ProjectTaskID       *uuid.UUID       `db:"project_task_id" json:"project_task_id,omitempty"`
	ModelOverride       *string          `db:"model_override" json:"model_override,omitempty"`
	ReasoningEffort     *ReasoningEffort `db:"reasoning_effort" json:"reasoning_effort,omitempty"`
	TriggeredByUserID   *uuid.UUID       `db:"triggered_by_user_id" json:"triggered_by_user_id,omitempty"`
	AgentSessionID      *string          `db:"agent_session_id" json:"agent_session_id,omitempty"`
	CurrentTurn         int              `db:"current_turn" json:"current_turn"`
	// LastActivityAt is the timestamp of the last write to this session — used
	// as the MRU sort key in ListByOrg. NOT NULL since migration 000077;
	// previously it could be NULL for first-turn sessions.
	LastActivityAt      time.Time    `db:"last_activity_at" json:"last_activity_at"`
	SandboxState        SandboxState `db:"sandbox_state" json:"sandbox_state"`
	WorkspaceGeneration int64        `db:"workspace_generation" json:"workspace_generation"`
	SnapshotKey         *string      `db:"snapshot_key" json:"snapshot_key,omitempty"`
	// PendingSnapshotKey, when non-nil, is the storage key for a post-PR
	// snapshot whose upload is still in flight. Hydration paths
	// (continue_session / Fix tests) must wait until this is NULL before
	// resuming — otherwise they restore the stale pre-PR snapshot.
	// Promoted into SnapshotKey (and cleared) by PromotePendingSnapshot
	// once the upload completes; cleared by ClearPendingSnapshot on
	// upload failure.
	//
	// Hidden from JSON: this is internal orchestrator state, and the raw
	// object-storage key has no meaning to API consumers. The frontend
	// surfaces "finalizing" via session status, not by inspecting this
	// column.
	PendingSnapshotKey *string `db:"pending_snapshot_key" json:"-"`
	// PendingSnapshotSetAt records when PendingSnapshotKey was set, so the
	// stranded-pending reaper can identify uploads whose owning goroutine
	// died (worker crash, drain timeout) and clear the key. Set to NOW() by
	// SetPendingSnapshotKey, cleared in lockstep by Promote/Clear. Hidden
	// from JSON for the same reason as PendingSnapshotKey.
	PendingSnapshotSetAt        *time.Time              `db:"pending_snapshot_set_at" json:"-"`
	RuntimeSoftDeadlineAt       *time.Time              `db:"runtime_soft_deadline_at" json:"runtime_soft_deadline_at,omitempty"`
	RuntimeHardDeadlineAt       *time.Time              `db:"runtime_hard_deadline_at" json:"runtime_hard_deadline_at,omitempty"`
	RuntimeLastProgressAt       *time.Time              `db:"runtime_last_progress_at" json:"runtime_last_progress_at,omitempty"`
	RuntimeLastProgressType     RuntimeProgressType     `db:"runtime_last_progress_type" json:"runtime_last_progress_type,omitempty"`
	RuntimeLastProgressStrength RuntimeProgressStrength `db:"runtime_last_progress_strength" json:"runtime_last_progress_strength,omitempty"`
	RuntimeExtensionCount       int                     `db:"runtime_extension_count" json:"runtime_extension_count"`
	RuntimeExtensionSeconds     int                     `db:"runtime_extension_seconds" json:"runtime_extension_seconds"`
	RuntimeStopReason           RuntimeStopReason       `db:"runtime_stop_reason" json:"runtime_stop_reason,omitempty"`
	RuntimeGracefulStopAt       *time.Time              `db:"runtime_graceful_stop_at" json:"runtime_graceful_stop_at,omitempty"`
	CheckpointedAt              *time.Time              `db:"checkpointed_at" json:"checkpointed_at,omitempty"`
	CheckpointKind              CheckpointKind          `db:"checkpoint_kind" json:"checkpoint_kind,omitempty"`
	CheckpointCapability        CheckpointCapability    `db:"checkpoint_capability" json:"checkpoint_capability,omitempty"`
	CheckpointSizeBytes         int64                   `db:"checkpoint_size_bytes" json:"checkpoint_size_bytes"`
	CheckpointError             *string                 `db:"checkpoint_error" json:"checkpoint_error,omitempty"`
	RecoveryState               RecoveryState           `db:"recovery_state" json:"recovery_state,omitempty"`
	RecoveryQueuedAt            *time.Time              `db:"recovery_queued_at" json:"recovery_queued_at,omitempty"`
	RecoveryStartedAt           *time.Time              `db:"recovery_started_at" json:"recovery_started_at,omitempty"`
	RecoveryAttemptCount        int                     `db:"recovery_attempt_count" json:"recovery_attempt_count"`
	TargetBranch                *string                 `db:"target_branch" json:"target_branch,omitempty"`
	WorkingBranch               *string                 `db:"working_branch" json:"working_branch,omitempty"`
	BaseCommitSHA               *string                 `db:"base_commit_sha" json:"base_commit_sha,omitempty"`
	RepositoryID                *uuid.UUID              `db:"repository_id" json:"repository_id,omitempty"`
	DiffStats                   json.RawMessage         `db:"diff_stats" json:"diff_stats,omitempty"` // nil for list queries (excluded to reduce payload size)
	// DiffHistory is only populated on single-session fetches (GetByID, ClaimIdle, etc.).
	// List queries return NULL to avoid multi-megabyte payloads — do not rely on this
	// field being non-nil unless the session was fetched individually.
	DiffHistory      json.RawMessage `db:"diff_history" json:"diff_history,omitempty"`
	InputManifest    json.RawMessage `db:"input_manifest" json:"input_manifest,omitempty"`
	ArchivedAt       *time.Time      `db:"archived_at" json:"archived_at,omitempty"`
	ArchivedByUserID *uuid.UUID      `db:"archived_by_user_id" json:"archived_by_user_id,omitempty"`
	AutomationRunID  *uuid.UUID      `db:"automation_run_id" json:"automation_run_id,omitempty"`
	// PRCreationState drives the Create PR button's state machine. It is
	// orthogonal to Status — a session can be `completed` with pr_creation_state
	// `idle` (ready for user to click Create PR), `pushing` (in flight), etc.
	PRCreationState PRCreationState `db:"pr_creation_state" json:"pr_creation_state"`
	PRCreationError *string         `db:"pr_creation_error" json:"pr_creation_error,omitempty"`
	// PRPushState drives the "Push changes" button's state machine for sessions
	// that already have an open PR. Independent from PRCreationState so a single
	// session can show "PR opened" while a follow-up push is mid-flight.
	PRPushState                PRPushState         `db:"pr_push_state" json:"pr_push_state"`
	PRPushError                *string             `db:"pr_push_error" json:"pr_push_error,omitempty"`
	PRPushErrorCode            PRPushErrorCode     `db:"pr_push_error_code" json:"pr_push_error_code,omitempty"`
	BranchCreationState        BranchCreationState `db:"branch_creation_state" json:"branch_creation_state"`
	BranchCreationError        *string             `db:"branch_creation_error" json:"branch_creation_error,omitempty"`
	BranchURL                  *string             `db:"branch_url" json:"branch_url,omitempty"`
	DiffCollectedAt            *time.Time          `db:"diff_collected_at" json:"diff_collected_at,omitempty"`
	LatestDiffSnapshotID       *uuid.UUID          `db:"latest_diff_snapshot_id" json:"latest_diff_snapshot_id,omitempty"`
	WorkspaceRevision          int64               `db:"workspace_revision" json:"workspace_revision"`
	WorkspaceRevisionUpdatedAt time.Time           `db:"workspace_revision_updated_at" json:"workspace_revision_updated_at"`
	// HasUnpushedChanges is a derived read-model field: true when the most
	// recent persisted session HEAD differs from the open PR's tracked head
	// commit, which means "Push changes" is actionable.
	HasUnpushedChanges bool `db:"has_unpushed_changes" json:"has_unpushed_changes"`
	// LinearPrivate suppresses every Linear write for this session. The agent
	// still receives Linear context locally; nothing leaves 143. Frozen at
	// session create — see design 62 §"Composer controls must express distinct
	// semantics" for why post-hoc backfill would be confusing.
	LinearPrivate bool `db:"linear_private" json:"linear_private"`
	// LinearStateSyncDisabled keeps the attachment+rolling comment trail intact
	// but blocks workflow-state automation. Distinct from LinearPrivate.
	LinearStateSyncDisabled bool `db:"linear_state_sync_disabled" json:"linear_state_sync_disabled"`
	// LinearIdentifierHint is the primary Linear key (e.g. "ACS-1234") captured
	// when detection resolved. Read by the branch-naming logic so the working
	// branch slug includes the identifier — Linear's GitHub integration matches
	// branches with the key prefix independently of the PR title prefix.
	LinearIdentifierHint *string `db:"linear_identifier_hint" json:"linear_identifier_hint,omitempty"`
	// LinearPrepareState reflects the pre-start preparation step. "ready"
	// gates turn 1 from starting until the primary Linear context snapshot
	// has been captured.
	LinearPrepareState LinearPrepareState            `db:"linear_prepare_state" json:"linear_prepare_state"`
	DeletedAt          *time.Time                    `db:"deleted_at" json:"-"`
	CapabilitySnapshot []AgentCapabilitySnapshotItem `db:"capability_snapshot" json:"capability_snapshot,omitempty"`
	// GitIdentitySource records which token authority the agent used for
	// git pushes ("user" — the triggering user's GitHub OAuth token; "app"
	// — the GitHub App installation token). Stamped at session-start by
	// the orchestrator after the identity resolver picks the right token.
	// nil for sessions that ran before this audit was added or that never
	// got a credential helper wired (preview sandboxes, PM bootstrap, etc.).
	GitIdentitySource *GitIdentitySource `db:"git_identity_source" json:"git_identity_source,omitempty"`
	GitIdentityUserID *uuid.UUID         `db:"git_identity_user_id" json:"git_identity_user_id,omitempty"`
	CreatedAt         time.Time          `db:"created_at" json:"created_at"`
}

// SessionTitleSource identifies who owns the current session title. Manual
// titles are sticky and must not be replaced by background title generation.
type SessionTitleSource string

const (
	SessionTitleSourceLegacy    SessionTitleSource = "legacy"
	SessionTitleSourceGenerated SessionTitleSource = "generated"
	SessionTitleSourceIssue     SessionTitleSource = "issue"
	SessionTitleSourceManual    SessionTitleSource = "manual"
)

func (s SessionTitleSource) Validate() error {
	switch s {
	case SessionTitleSourceLegacy, SessionTitleSourceGenerated, SessionTitleSourceIssue, SessionTitleSourceManual:
		return nil
	default:
		return fmt.Errorf("invalid SessionTitleSource: %q", s)
	}
}

// SessionTitleState is the minimal state needed by background title updates.
type SessionTitleState struct {
	Title              *string            `db:"title"`
	TitleSource        SessionTitleSource `db:"title_source"`
	TitleIntent        *string            `db:"title_intent"`
	TitlePivotedAtTurn *int               `db:"title_pivoted_at_turn"`
	TitleGeneratedAt   *time.Time         `db:"title_generated_at"`
	CurrentTurn        int                `db:"current_turn"`
}

// SessionDetail is the API response for a single session, enriched with threads.
type SessionDetail struct {
	Session
	RepositoryFullName  *string             `json:"repository_full_name,omitempty"`
	Threads             []SessionThread     `json:"threads"`
	Changesets          []ChangesetSummary  `json:"changesets"`
	ChangesetStackState ChangesetStackState `json:"changeset_stack_state"`
}

// SessionDiff is the large, lazily-loaded diff payload for a session. It is
// intentionally separate from SessionDetail so opening a session does not
// hydrate or marshal multi-megabyte diff blobs.
type SessionDiff struct {
	SessionID            uuid.UUID       `json:"session_id"`
	Diff                 *string         `json:"diff,omitempty"`
	DiffStats            json.RawMessage `json:"diff_stats,omitempty"`
	DiffHistory          json.RawMessage `json:"diff_history,omitempty"`
	DiffTruncated        bool            `json:"diff_truncated"`
	DiffHistoryTruncated bool            `json:"diff_history_truncated"`
	DiffChars            int64           `json:"diff_chars,omitempty"`
	DiffHistoryBytes     int64           `json:"diff_history_bytes,omitempty"`
	DiffMaxChars         int64           `json:"diff_max_chars,omitempty"`
	DiffHistoryMaxBytes  int64           `json:"diff_history_max_bytes,omitempty"`
}

// SessionIssueLink is the live join model between a session and an issue.
type SessionIssueLink struct {
	ID            uuid.UUID            `db:"id" json:"id"`
	OrgID         uuid.UUID            `db:"org_id" json:"org_id"`
	SessionID     uuid.UUID            `db:"session_id" json:"session_id"`
	IssueID       uuid.UUID            `db:"issue_id" json:"issue_id"`
	Role          SessionIssueLinkRole `db:"role" json:"role"`
	Position      int                  `db:"position" json:"position"`
	AddedByUserID *uuid.UUID           `db:"added_by_user_id" json:"added_by_user_id,omitempty"`
	CreatedAt     time.Time            `db:"created_at" json:"created_at"`
	IssueTitle    *string              `db:"issue_title" json:"issue_title,omitempty"`
	IssueSource   *IssueSource         `db:"issue_source" json:"issue_source,omitempty"`
	ExternalID    *string              `db:"external_id" json:"external_id,omitempty"`
	Description   *string              `db:"description" json:"description,omitempty"`
	RepositoryID  *uuid.UUID           `db:"repository_id" json:"repository_id,omitempty"`
	IssueStatus   *IssueStatus         `db:"issue_status" json:"issue_status,omitempty"`
	// IssueWorkspaceSlug is left-joined off Linear's provider_state. The
	// frontend uses it to render `linear.app/<slug>/issue/<KEY>` deep
	// links instead of the universal redirect path. Nil for non-Linear
	// links and Linear links written before workspace caching landed.
	IssueWorkspaceSlug *string `db:"issue_workspace_slug" json:"issue_workspace_slug,omitempty"`
	// LinearLastSkippedReason is the latest state-sync skip reason from
	// provider_state. Exposed so the session detail view can explain why a
	// linked Linear issue did not move workflow state.
	LinearLastSkippedReason *string `db:"linear_last_skipped_reason" json:"linear_last_skipped_reason,omitempty"`
	// RawLinearPrimarySnapshot is the JSONB primary_snapshot cached in
	// session_issue_link_provider_state at link time. It is internal-only:
	// the orchestrator decodes it into SessionIssueSnapshotEntry fields when
	// creating the immutable per-turn issue snapshot.
	RawLinearPrimarySnapshot json.RawMessage `db:"linear_primary_snapshot" json:"-"`
	// PagerDuty provider-state fields let the session detail header render a
	// meaningful incident chip without loading a separate incident page.
	PagerDutyIncidentID     *string `db:"pagerduty_incident_id" json:"pagerduty_incident_id,omitempty"`
	PagerDutyIncidentNumber *string `db:"pagerduty_incident_number" json:"pagerduty_incident_number,omitempty"`
	PagerDutyIncidentURL    *string `db:"pagerduty_incident_url" json:"pagerduty_incident_url,omitempty"`
	PagerDutyServiceID      *string `db:"pagerduty_service_id" json:"pagerduty_service_id,omitempty"`
	PagerDutyServiceName    *string `db:"pagerduty_service_name" json:"pagerduty_service_name,omitempty"`
}

type SessionIssueSnapshotAttachment struct {
	Title  string `json:"title,omitempty"`
	URL    string `json:"url"`
	Source string `json:"source,omitempty"`
}

type SessionIssueSnapshotComment struct {
	Author    string    `json:"author,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// SessionIssueSnapshotEntry is the immutable execution-time issue context
// captured for a specific session turn.
type SessionIssueSnapshotEntry struct {
	IssueID      uuid.UUID                        `json:"issue_id"`
	Role         SessionIssueLinkRole             `json:"role"`
	Position     int                              `json:"position"`
	Title        string                           `json:"title"`
	ExternalID   string                           `json:"external_id,omitempty"`
	Source       IssueSource                      `json:"source"`
	Description  string                           `json:"description,omitempty"`
	RepositoryID *uuid.UUID                       `json:"repository_id,omitempty"`
	Status       IssueStatus                      `json:"status,omitempty"`
	StateName    string                           `json:"state_name,omitempty"`
	StateType    string                           `json:"state_type,omitempty"`
	Priority     string                           `json:"priority,omitempty"`
	AssigneeName string                           `json:"assignee_name,omitempty"`
	TeamKey      string                           `json:"team_key,omitempty"`
	TeamName     string                           `json:"team_name,omitempty"`
	URL          string                           `json:"url,omitempty"`
	Attachments  []SessionIssueSnapshotAttachment `json:"attachments,omitempty"`
	Comments     []SessionIssueSnapshotComment    `json:"comments,omitempty"`
}

// SessionTurnIssueSnapshot is the authoritative resolved issue context used by
// a given execution turn.
type SessionTurnIssueSnapshot struct {
	ID              uuid.UUID                   `db:"id" json:"id"`
	OrgID           uuid.UUID                   `db:"org_id" json:"org_id"`
	SessionID       uuid.UUID                   `db:"session_id" json:"session_id"`
	TurnNumber      int                         `db:"turn_number" json:"turn_number"`
	LinkedIssues    []SessionIssueSnapshotEntry `db:"-" json:"linked_issues"`
	RawLinkedIssues json.RawMessage             `db:"linked_issues" json:"-"`
	CreatedAt       time.Time                   `db:"created_at" json:"created_at"`
}

func (s Session) HasPrimaryIssue() bool {
	return s.PrimaryIssueID != nil
}

func (s Session) IsInteractive() bool {
	return s.InteractionMode == SessionInteractionModeInteractive
}

func (s Session) ShouldValidateOnTurnComplete() bool {
	return s.ValidationPolicy == SessionValidationPolicyOnTurnComplete
}

func (s Session) ShouldValidateOnSessionEnd() bool {
	return s.ValidationPolicy == SessionValidationPolicyOnSessionEnd
}

// UnmarshalJSON accepts both the canonical Phase 2 wire format (primary_issue_id)
// and the legacy Phase 1 format (issue_id). Old workers in a rolling deploy may
// still write the legacy field into Redis status streams; reading both keeps
// PrimaryIssueID populated across the deploy boundary. Safe to remove once all
// workers have rolled past Phase 2.
func (s *Session) UnmarshalJSON(data []byte) error {
	type alias Session
	aux := struct {
		*alias
		LegacyIssueID *uuid.UUID `json:"issue_id"`
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if s.PrimaryIssueID == nil && aux.LegacyIssueID != nil && *aux.LegacyIssueID != uuid.Nil {
		s.PrimaryIssueID = aux.LegacyIssueID
	}
	return nil
}

func (s Session) marshalJSONMap() (map[string]any, error) {
	sessionJSON, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if err := json.Unmarshal(sessionJSON, &base); err != nil {
		return nil, err
	}
	return base, nil
}

func (s SessionDetail) MarshalJSON() ([]byte, error) {
	base, err := s.Session.marshalJSONMap()
	if err != nil {
		return nil, err
	}
	if s.RepositoryFullName != nil {
		base["repository_full_name"] = *s.RepositoryFullName
	}
	base["threads"] = s.Threads
	return json.Marshal(base)
}

func (s SessionListItem) MarshalJSON() ([]byte, error) {
	base, err := s.Session.marshalJSONMap()
	if err != nil {
		return nil, err
	}
	if s.LastViewedAt != nil {
		base["last_viewed_at"] = s.LastViewedAt
	}
	if s.PRSummary != nil {
		base["pr_summary"] = s.PRSummary
	}
	return json.Marshal(base)
}

// SessionResult holds the result fields to update on an agent run.
type SessionResult struct {
	TokenUsage                      json.RawMessage   `json:"token_usage,omitempty"`
	ModelUsed                       *string           `json:"model_used,omitempty"`
	ResultSummary                   *string           `json:"result_summary,omitempty"`
	Diff                            *string           `json:"diff,omitempty"`
	Error                           *string           `json:"error,omitempty"`
	FailureCategory                 *string           `json:"failure_category,omitempty"`
	DiffBaseCommitSHA               *string           `json:"-"`
	DiffHeadCommitSHA               *string           `json:"-"`
	DiffWorkspaceDirty              bool              `json:"-"`
	DiffCollectedAt                 *time.Time        `json:"-"`
	DiffSource                      SessionDiffSource `json:"-"`
	ReviewArtifactKey               *string           `json:"-"`
	ReviewArtifactVersion           *int              `json:"-"`
	ReviewArtifactCompressedBytes   int64             `json:"-"`
	ReviewArtifactUncompressedBytes int64             `json:"-"`
	ReviewArtifactFileCount         int               `json:"-"`
	ReviewArtifactSkippedCount      int               `json:"-"`
	ReviewArtifactTruncated         bool              `json:"-"`
}

type SessionDiffSnapshot struct {
	ID                              uuid.UUID         `db:"id" json:"id"`
	SessionID                       uuid.UUID         `db:"session_id" json:"session_id"`
	OrgID                           uuid.UUID         `db:"org_id" json:"org_id"`
	TurnNumber                      int               `db:"turn_number" json:"turn_number"`
	SequenceNumber                  int               `db:"sequence_number" json:"sequence_number"`
	Source                          SessionDiffSource `db:"source" json:"source"`
	BaseCommitSHA                   string            `db:"base_commit_sha" json:"base_commit_sha"`
	HeadCommitSHA                   *string           `db:"head_commit_sha" json:"head_commit_sha,omitempty"`
	WorkspaceDirty                  bool              `db:"workspace_dirty" json:"workspace_dirty"`
	WorkingBranch                   *string           `db:"working_branch" json:"working_branch,omitempty"`
	TargetBranch                    *string           `db:"target_branch" json:"target_branch,omitempty"`
	Diff                            string            `db:"diff" json:"diff"`
	FilesChanged                    int               `db:"files_changed" json:"files_changed"`
	LinesAdded                      int               `db:"lines_added" json:"lines_added"`
	LinesRemoved                    int               `db:"lines_removed" json:"lines_removed"`
	CapturedAt                      time.Time         `db:"captured_at" json:"captured_at"`
	ReviewArtifactKey               *string           `db:"review_artifact_key" json:"review_artifact_key,omitempty"`
	ReviewArtifactVersion           *int              `db:"review_artifact_version" json:"review_artifact_version,omitempty"`
	ReviewArtifactCompressedBytes   int64             `db:"review_artifact_compressed_bytes" json:"review_artifact_compressed_bytes"`
	ReviewArtifactUncompressedBytes int64             `db:"review_artifact_uncompressed_bytes" json:"review_artifact_uncompressed_bytes"`
	ReviewArtifactFileCount         int               `db:"review_artifact_file_count" json:"review_artifact_file_count"`
	ReviewArtifactSkippedCount      int               `db:"review_artifact_skipped_count" json:"review_artifact_skipped_count"`
	ReviewArtifactTruncated         bool              `db:"review_artifact_truncated" json:"review_artifact_truncated"`
}

type SessionReviewArtifactRef struct {
	Key     *string `db:"review_artifact_key" json:"review_artifact_key,omitempty"`
	Version *int    `db:"review_artifact_version" json:"review_artifact_version,omitempty"`
}

// PullRequestStatus represents the lifecycle state of a pull request.
type PullRequestStatus string

const (
	PullRequestStatusOpen   PullRequestStatus = "open"
	PullRequestStatusClosed PullRequestStatus = "closed"
	PullRequestStatusMerged PullRequestStatus = "merged"
)

func (s PullRequestStatus) Validate() error {
	switch s {
	case PullRequestStatusOpen, PullRequestStatusClosed, PullRequestStatusMerged:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestStatus: %q", s)
	}
}

type PullRequestReviewStatus string

const (
	PullRequestReviewStatusPending          PullRequestReviewStatus = "pending"
	PullRequestReviewStatusApproved         PullRequestReviewStatus = "approved"
	PullRequestReviewStatusChangesRequested PullRequestReviewStatus = "changes_requested"
)

func (s PullRequestReviewStatus) Validate() error {
	switch s {
	case PullRequestReviewStatusPending, PullRequestReviewStatusApproved, PullRequestReviewStatusChangesRequested:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestReviewStatus: %q", s)
	}
}

type PullRequestCIStatus string

const (
	PullRequestCIStatusSuccess PullRequestCIStatus = "success"
	PullRequestCIStatusFailure PullRequestCIStatus = "failure"
	PullRequestCIStatusPending PullRequestCIStatus = "pending"
)

func (s PullRequestCIStatus) Validate() error {
	switch s {
	case "", PullRequestCIStatusSuccess, PullRequestCIStatusFailure, PullRequestCIStatusPending:
		return nil
	default:
		return fmt.Errorf("invalid PullRequestCIStatus: %q", s)
	}
}

// PullRequest represents a GitHub PR created by an agent run.
// NOTE: SessionID is nullable (*uuid.UUID) because PRs can be created manually
// without an associated session. API consumers should handle null session_id.
type PullRequest struct {
	ID             uuid.UUID               `db:"id" json:"id"`
	SessionID      *uuid.UUID              `db:"session_id" json:"session_id,omitempty"`
	ChangesetID    *uuid.UUID              `db:"changeset_id" json:"changeset_id,omitempty"`
	OrgID          uuid.UUID               `db:"org_id" json:"org_id"`
	GitHubPRNumber int                     `db:"github_pr_number" json:"github_pr_number"`
	GitHubPRURL    string                  `db:"github_pr_url" json:"github_pr_url"`
	GitHubRepo     string                  `db:"github_repo" json:"github_repo"`
	Title          string                  `db:"title" json:"title"`
	Body           *string                 `db:"body" json:"body,omitempty"`
	Status         PullRequestStatus       `db:"status" json:"status"`
	ReviewStatus   PullRequestReviewStatus `db:"review_status" json:"review_status"`
	AuthoredBy     GitIdentitySource       `db:"authored_by" json:"authored_by"`
	CIStatus       PullRequestCIStatus     `db:"ci_status" json:"ci_status"`
	HeadSHA        *string                 `db:"head_sha" json:"head_sha,omitempty"`
	// HeadRef is the branch name on GitHub the PR tracks. Captured at
	// PR-creation time so the "Push changes" follow-up always targets the
	// same ref even if the session's title or Linear identifier (which feed
	// into formatBranchName) change later. Nullable for PRs created before
	// migration 107 added the column — the push code falls back to
	// recomputing in that case.
	HeadRef                     *string                        `db:"head_ref" json:"head_ref,omitempty"`
	BaseSHA                     *string                        `db:"base_sha" json:"base_sha,omitempty"`
	MergeState                  PullRequestMergeState          `db:"merge_state" json:"merge_state"`
	HasConflicts                bool                           `db:"has_conflicts" json:"has_conflicts"`
	FailingTestCount            int                            `db:"failing_test_count" json:"failing_test_count"`
	NeedsAgentAction            bool                           `db:"needs_agent_action" json:"needs_agent_action"`
	GitHubStateSyncedAt         *time.Time                     `db:"github_state_synced_at" json:"github_state_synced_at,omitempty"`
	HealthVersion               int64                          `db:"health_version" json:"health_version"`
	MergeWhenReadyState         PullRequestMergeWhenReadyState `db:"merge_when_ready_state" json:"merge_when_ready_state"`
	MergeWhenReadyRequestedBy   *uuid.UUID                     `db:"merge_when_ready_requested_by" json:"merge_when_ready_requested_by,omitempty"`
	MergeWhenReadyRequestedAt   *time.Time                     `db:"merge_when_ready_requested_at" json:"merge_when_ready_requested_at,omitempty"`
	MergeWhenReadyHeadSHA       string                         `db:"merge_when_ready_head_sha" json:"merge_when_ready_head_sha,omitempty"`
	MergeWhenReadyHealthVersion *int64                         `db:"merge_when_ready_health_version" json:"merge_when_ready_health_version,omitempty"`
	MergeWhenReadyError         string                         `db:"merge_when_ready_error" json:"merge_when_ready_error,omitempty"`
	MergeWhenReadyUpdatedAt     *time.Time                     `db:"merge_when_ready_updated_at" json:"merge_when_ready_updated_at,omitempty"`
	FeedbackMonitoring          PRFeedbackMonitoring           `db:"feedback_monitoring" json:"feedback_monitoring"`
	FeedbackBotEpoch            int64                          `db:"feedback_bot_epoch" json:"feedback_bot_epoch"`
	FeedbackBotCyclesInEpoch    int                            `db:"feedback_bot_cycles_in_epoch" json:"feedback_bot_cycles_in_epoch"`
	MergedAt                    *time.Time                     `db:"merged_at" json:"merged_at,omitempty"`
	CreatedAt                   time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt                   time.Time                      `db:"updated_at" json:"updated_at"`
}

// PRSummary is a lightweight view of a PR for inclusion in session list responses.
type PRSummary struct {
	Status   PullRequestStatus   `json:"status"`
	CIStatus PullRequestCIStatus `json:"ci_status"`
	Number   int                 `json:"number"`
	URL      string              `json:"url"`
}

// SessionListItem wraps a Session with enrichment data for list views.
type SessionListItem struct {
	Session
	LastViewedAt *time.Time `json:"last_viewed_at,omitempty"`
	PRSummary    *PRSummary `json:"pr_summary,omitempty"`
}

// SessionCounts summarizes session totals for tab badges on the sessions UI.
// Each field is capped at Cap for cheap bounded counting on large orgs;
// clients should render ">= Cap" as e.g. "99+".
type SessionCounts struct {
	All      int `json:"all"`
	Active   int `json:"active"`
	Archived int `json:"archived"`
	Cap      int `json:"cap"`
}

// SessionLog represents a log line emitted during an agent run.
type SessionLog struct {
	ID         int64           `db:"id" json:"id"`
	SessionID  uuid.UUID       `db:"session_id" json:"session_id"`
	OrgID      uuid.UUID       `db:"org_id" json:"org_id"`
	ThreadID   *uuid.UUID      `db:"thread_id" json:"thread_id,omitempty"`
	Timestamp  time.Time       `db:"timestamp" json:"created_at"`
	Level      SessionLogLevel `db:"level" json:"level"`
	Message    string          `db:"message" json:"message"`
	Metadata   json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	TurnNumber int             `db:"turn_number" json:"turn_number"`

	MessageBytes     int  `db:"-" json:"message_bytes,omitempty"`
	MessageChars     int  `db:"-" json:"message_chars,omitempty"`
	MessageTruncated bool `db:"-" json:"message_truncated,omitempty"`
}

// SessionMessageSource identifies the originator of a session message.
type SessionMessageSource string

const (
	SessionMessageSourceAgentTool        SessionMessageSource = "agent_tool"
	SessionMessageSourceSystemAutoRepair SessionMessageSource = "system_auto_repair"
	SessionMessageSourceGitHubPRFeedback SessionMessageSource = "github_pr_feedback"
)

func (s SessionMessageSource) Validate() error {
	switch s {
	case "", SessionMessageSourceAgentTool, SessionMessageSourceSystemAutoRepair, SessionMessageSourceGitHubPRFeedback:
		return nil
	default:
		return fmt.Errorf("invalid SessionMessageSource: %q", s)
	}
}

type SessionMessage struct {
	ID          int64                  `db:"id" json:"id"`
	SessionID   uuid.UUID              `db:"session_id" json:"session_id"`
	OrgID       uuid.UUID              `db:"org_id" json:"org_id"`
	ThreadID    *uuid.UUID             `db:"thread_id" json:"thread_id,omitempty"`
	UserID      *uuid.UUID             `db:"user_id" json:"user_id,omitempty"`
	TurnNumber  int                    `db:"turn_number" json:"turn_number"`
	Role        MessageRole            `db:"role" json:"role"`
	Content     string                 `db:"content" json:"content"`
	Attachments []string               `db:"attachments" json:"attachments,omitempty"`
	References  SessionInputReferences `db:"references" json:"references,omitempty"`
	Commands    SessionInputCommands   `db:"commands" json:"commands,omitempty"`
	TokenUsage  json.RawMessage        `db:"token_usage" json:"token_usage,omitempty"`
	Source      SessionMessageSource   `db:"source" json:"source,omitempty"`
	CreatedAt   time.Time              `db:"created_at" json:"created_at"`
}

// ThreadCreatedBySource identifies what or who created a session thread.
type ThreadCreatedBySource string

const (
	ThreadCreatedBySourceUser      ThreadCreatedBySource = "user"
	ThreadCreatedBySourceAgentTool ThreadCreatedBySource = "agent_tool"
	ThreadCreatedBySourceSystem    ThreadCreatedBySource = "system"
)

func (s ThreadCreatedBySource) Validate() error {
	switch s {
	case "", ThreadCreatedBySourceUser, ThreadCreatedBySourceAgentTool, ThreadCreatedBySourceSystem:
		return nil
	default:
		return fmt.Errorf("invalid ThreadCreatedBySource: %q", s)
	}
}

type ThreadExecutionMode string

const (
	ThreadExecutionModeWork   ThreadExecutionMode = "work"
	ThreadExecutionModeReview ThreadExecutionMode = "review"
)

func (m ThreadExecutionMode) Validate() error {
	switch m {
	case "", ThreadExecutionModeWork, ThreadExecutionModeReview:
		return nil
	default:
		return fmt.Errorf("invalid ThreadExecutionMode: %q", m)
	}
}

type ThreadFilesystemMode string

const (
	ThreadFilesystemModeReadWrite ThreadFilesystemMode = "read_write"
	ThreadFilesystemModeReadOnly  ThreadFilesystemMode = "read_only"
)

func (m ThreadFilesystemMode) Validate() error {
	switch m {
	case "", ThreadFilesystemModeReadWrite, ThreadFilesystemModeReadOnly:
		return nil
	default:
		return fmt.Errorf("invalid ThreadFilesystemMode: %q", m)
	}
}

// SessionThread is one agent doing one piece of work. All threads in a session
// share the same container and filesystem.
type SessionThread struct {
	ID                    uuid.UUID                   `db:"id" json:"id"`
	SessionID             uuid.UUID                   `db:"session_id" json:"session_id"`
	OrgID                 uuid.UUID                   `db:"org_id" json:"org_id"`
	AgentType             AgentType                   `db:"agent_type" json:"agent_type"`
	ModelOverride         *string                     `db:"model_override" json:"model_override,omitempty"`
	Label                 string                      `db:"label" json:"label"`
	Instructions          *string                     `db:"instructions" json:"instructions,omitempty"`
	FileScope             []string                    `db:"file_scope" json:"file_scope,omitempty"`
	Status                ThreadStatus                `db:"status" json:"status"`
	AgentSessionID        *string                     `db:"agent_session_id" json:"agent_session_id,omitempty"`
	CurrentTurn           int                         `db:"current_turn" json:"current_turn"`
	LastActivityAt        *time.Time                  `db:"last_activity_at" json:"last_activity_at,omitempty"`
	ResultSummary         *string                     `db:"result_summary" json:"result_summary,omitempty"`
	Diff                  *string                     `db:"diff" json:"diff,omitempty"`
	FailureExplanation    *string                     `db:"failure_explanation" json:"failure_explanation,omitempty"`
	FailureCategory       *string                     `db:"failure_category" json:"failure_category,omitempty"`
	StartedAt             *time.Time                  `db:"started_at" json:"started_at,omitempty"`
	CompletedAt           *time.Time                  `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt             time.Time                   `db:"created_at" json:"created_at"`
	CreatedBySource       ThreadCreatedBySource       `db:"created_by_source" json:"created_by_source,omitempty"`
	CreatedByThreadID     *uuid.UUID                  `db:"created_by_thread_id" json:"created_by_thread_id,omitempty"`
	ArchivedAt            *time.Time                  `db:"archived_at" json:"archived_at,omitempty"`
	BaseSnapshotKey       *string                     `db:"base_snapshot_key" json:"base_snapshot_key,omitempty"`
	CostCents             float64                     `db:"cost_cents" json:"cost_cents"`
	PendingMessageCount   int                         `db:"pending_message_count" json:"pending_message_count"`
	CancelRequestedAt     *time.Time                  `db:"cancel_requested_at" json:"cancel_requested_at,omitempty"`
	RuntimeStopReason     RuntimeStopReason           `db:"runtime_stop_reason" json:"runtime_stop_reason,omitempty"`
	RuntimeGracefulStopAt *time.Time                  `db:"runtime_graceful_stop_at" json:"runtime_graceful_stop_at,omitempty"`
	RecoveryState         RecoveryState               `db:"recovery_state" json:"recovery_state,omitempty"`
	RecoveryReason        string                      `db:"recovery_reason" json:"recovery_reason,omitempty"`
	RecoveryEventHistory  json.RawMessage             `db:"recovery_event_history" json:"recovery_event_history,omitempty"`
	ExecutionMode         ThreadExecutionMode         `db:"execution_mode" json:"execution_mode,omitempty"`
	FilesystemMode        ThreadFilesystemMode        `db:"filesystem_mode" json:"filesystem_mode,omitempty"`
	InboxDelivery         *ThreadInboxDeliverySummary `db:"-" json:"inbox_delivery,omitempty"`
}

// SessionThreadFileEvent is operational write attribution: which thread
// touched which path, with optional git blob hashes before/after. Used to
// power overlap badges in the tab strip. Not security attribution.
type SessionThreadFileEvent struct {
	ID         int64                      `db:"id" json:"id"`
	OrgID      uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID  uuid.UUID                  `db:"session_id" json:"session_id"`
	ThreadID   *uuid.UUID                 `db:"thread_id" json:"thread_id,omitempty"`
	Turn       int                        `db:"turn" json:"turn"`
	Path       string                     `db:"path" json:"path"`
	EventType  SessionThreadFileEventType `db:"event_type" json:"event_type"`
	BeforeHash *string                    `db:"before_hash" json:"before_hash,omitempty"`
	AfterHash  *string                    `db:"after_hash" json:"after_hash,omitempty"`
	ObservedAt time.Time                  `db:"observed_at" json:"observed_at"`
}

// SessionQuestion represents a question the agent asks a human during a run.
type SessionQuestion struct {
	ID           uuid.UUID             `db:"id" json:"id"`
	SessionID    uuid.UUID             `db:"session_id" json:"session_id"`
	OrgID        uuid.UUID             `db:"org_id" json:"org_id"`
	QuestionText string                `db:"question_text" json:"question_text"`
	Options      []string              `db:"options" json:"options,omitempty"`
	Context      *string               `db:"context" json:"context,omitempty"`
	BlocksPhase  *string               `db:"blocks_phase" json:"blocks_phase,omitempty"`
	AnswerText   *string               `db:"answer_text" json:"answer_text,omitempty"`
	AnsweredBy   *uuid.UUID            `db:"answered_by" json:"answered_by,omitempty"`
	AnsweredAt   *time.Time            `db:"answered_at" json:"answered_at,omitempty"`
	Status       SessionQuestionStatus `db:"status" json:"status"`
	CreatedAt    time.Time             `db:"created_at" json:"created_at"`
}

// PriorityScore holds the computed priority score for an issue.
type PriorityScore struct {
	ID                  uuid.UUID       `db:"id" json:"id"`
	IssueID             uuid.UUID       `db:"issue_id" json:"issue_id"`
	OrgID               uuid.UUID       `db:"org_id" json:"org_id"`
	Score               float64         `db:"score" json:"score"`
	CustomerImpactScore float64         `db:"customer_impact_score" json:"customer_impact_score"`
	SeverityScore       float64         `db:"severity_score" json:"severity_score"`
	RecencyScore        float64         `db:"recency_score" json:"recency_score"`
	RevenueRiskScore    float64         `db:"revenue_risk_score" json:"revenue_risk_score"`
	DirectionAlignment  float64         `db:"direction_alignment" json:"direction_alignment"`
	Factors             json.RawMessage `db:"factors" json:"factors,omitempty"`
	EligibleForAgent    bool            `db:"eligible_for_agent" json:"eligible_for_agent"`
	ComputedAt          time.Time       `db:"computed_at" json:"computed_at"`
}

// ComplexityEstimate holds the estimated complexity for an issue.
type ComplexityEstimate struct {
	ID              uuid.UUID `db:"id" json:"id"`
	IssueID         uuid.UUID `db:"issue_id" json:"issue_id"`
	OrgID           uuid.UUID `db:"org_id" json:"org_id"`
	Tier            int       `db:"tier" json:"tier"`
	Label           string    `db:"label" json:"label"`
	Confidence      float64   `db:"confidence" json:"confidence"`
	IssueType       *string   `db:"issue_type" json:"issue_type,omitempty"`
	Reasoning       *string   `db:"reasoning" json:"reasoning,omitempty"`
	EstimatedFiles  []string  `db:"estimated_files" json:"estimated_files,omitempty"`
	EstimatedTokens *int      `db:"estimated_tokens" json:"estimated_tokens,omitempty"`
	ModelUsed       *string   `db:"model_used" json:"model_used,omitempty"`
	ComputedAt      time.Time `db:"computed_at" json:"computed_at"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
}

// Deploy represents a deployment of a pull request.
type Deploy struct {
	ID            uuid.UUID `db:"id" json:"id"`
	PullRequestID uuid.UUID `db:"pull_request_id" json:"pull_request_id"`
	OrgID         uuid.UUID `db:"org_id" json:"org_id"`
	Environment   string    `db:"environment" json:"environment"`
	DeployedAt    time.Time `db:"deployed_at" json:"deployed_at"`
	CommitSHA     *string   `db:"commit_sha" json:"commit_sha,omitempty"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// WebhookDelivery represents an inbound webhook.
type WebhookDelivery struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	OrgID          uuid.UUID       `db:"org_id" json:"org_id"`
	IntegrationID  uuid.UUID       `db:"integration_id" json:"integration_id"`
	Provider       string          `db:"provider" json:"provider"`
	DeliveryID     *string         `db:"delivery_id" json:"delivery_id,omitempty"`
	EventType      string          `db:"event_type" json:"event_type"`
	SignatureValid *bool           `db:"signature_valid" json:"signature_valid,omitempty"`
	ReceivedAt     time.Time       `db:"received_at" json:"received_at"`
	ProcessedAt    *time.Time      `db:"processed_at" json:"processed_at,omitempty"`
	Status         string          `db:"status" json:"status"`
	Attempts       int             `db:"attempts" json:"attempts"`
	Error          *string         `db:"error" json:"error,omitempty"`
	Payload        json.RawMessage `db:"payload" json:"-"`
	Headers        json.RawMessage `db:"headers" json:"-"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
}

// LatestJobError holds the error and timestamp from the most recent failed job.
type LatestJobError struct {
	JobID     uuid.UUID
	LastError string
	UpdatedAt time.Time
}

// Job type constants for async work queue items.
const (
	JobTypePMAnalyze                     = "pm_analyze"
	JobTypePMBootstrap                   = "pm_bootstrap"
	JobTypePMContextRefresh              = "pm_context_refresh"
	JobTypeProjectCycle                  = "project_cycle"
	JobTypeAutomationRun                 = "automation_run"
	JobTypeStartPreview                  = "start_preview"
	JobTypeStartBranchPreview            = "start_branch_preview"
	JobTypeAutoPreviewDeferred           = "auto_preview_deferred"
	JobTypeSyncGitHubOrgRoster           = "sync_github_org_roster"
	JobTypePreviewCachePrewarm           = "preview_cache_prewarm"
	JobTypeSessionPreviewPrewarmClassify = "session_preview_prewarm_classify"
	JobTypeSessionPreviewWarmBuild       = "session_preview_warm_build"
	JobTypeBackfillPreviewGroups         = "backfill_preview_groups"
	JobTypeSyncPRPreviewSurfaces         = "sync_pr_preview_surfaces"
	JobTypePagerDutyIngestEvent          = "pagerduty_ingest_event"
	JobTypePagerDutySync                 = "pagerduty_sync"
	JobTypeRunCodeReview                 = "run_code_review"
	JobTypeMaterializeChangeset          = "materialize_changeset"
	JobTypeVerifyChangesetSplit          = "verify_changeset_split"
	JobTypeRestackChangesets             = "restack_changesets"
	JobTypePublishChangesetStack         = "publish_changeset_stack"
)

// Job represents an async work queue item.
type Job struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	OrgID          uuid.UUID       `db:"org_id" json:"org_id"`
	Queue          string          `db:"queue" json:"queue"`
	JobType        string          `db:"job_type" json:"job_type"`
	Payload        json.RawMessage `db:"payload" json:"payload"`
	Priority       int             `db:"priority" json:"priority"`
	Status         JobStatus       `db:"status" json:"status"`
	Attempts       int             `db:"attempts" json:"attempts"`
	MaxAttempts    int             `db:"max_attempts" json:"max_attempts"`
	RunAt          time.Time       `db:"run_at" json:"run_at"`
	LockedByNodeID *string         `db:"locked_by_node_id" json:"locked_by_node_id,omitempty"`
	LockedAt       *time.Time      `db:"locked_at" json:"locked_at,omitempty"`
	LeaseExpiresAt *time.Time      `db:"lease_expires_at" json:"lease_expires_at,omitempty"`
	LockToken      *uuid.UUID      `db:"lock_token" json:"lock_token,omitempty"`
	RunOwnerID     *string         `db:"run_owner_id" json:"run_owner_id,omitempty"`
	OwnerKind      JobOwnerKind    `db:"owner_kind" json:"owner_kind"`
	LastError      *string         `db:"last_error" json:"last_error,omitempty"`
	DedupeKey      *string         `db:"dedupe_key" json:"dedupe_key,omitempty"`
	// TargetNodeID, when set, restricts the job to be claimed by this
	// specific worker node. Used for sandbox-bound jobs (continue_session,
	// open_pr, run_agent for resume) where the work must execute on the same
	// docker daemon as the session's recorded container_id. NULL means any
	// worker can claim. A pinned job becomes claimable by any worker if its
	// target node is marked dead in the `nodes` table (starvation safety).
	TargetNodeID *string    `db:"target_node_id" json:"target_node_id,omitempty"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
	CompletedAt  *time.Time `db:"completed_at" json:"completed_at,omitempty"`
}

// SessionWorkerTarget returns the worker_node_id to pin sandbox-bound jobs
// (continue_session, open_pr, run_agent for resume) to, or nil if the
// session has no live sandbox to be pinned to.
//
// Pinning is correct only when the session currently owns a sandbox (its
// container_id and worker_node_id are both populated). After a snapshot —
// either by reaper recycling, post-PR snapshot capture, or explicit user
// stop — container_id is cleared and any worker can hydrate fresh, so the
// caller wants nil. Both fields are checked because container_id without
// worker_node_id is a transient state during the AcquireTurnHold → Set
// WorkerNodeID handshake; pinning during that window would race the
// owner-publishing CAS.
//
// The defensive empty-string check guards against a malformed row where the
// pointer is non-nil but the value is the zero string — recording an empty
// target would make every claim attempt mismatch and starve the job.
func SessionWorkerTarget(s *Session) *string {
	if s == nil {
		return nil
	}
	if s.ContainerID == nil || *s.ContainerID == "" {
		return nil
	}
	if s.WorkerNodeID == nil || *s.WorkerNodeID == "" {
		return nil
	}
	owner := *s.WorkerNodeID
	return &owner
}

// SessionReviewComment represents an inline review comment on a session diff.
type SessionReviewComment struct {
	ID             uuid.UUID  `db:"id" json:"id"`
	SessionID      uuid.UUID  `db:"session_id" json:"session_id"`
	OrgID          uuid.UUID  `db:"org_id" json:"org_id"`
	UserID         uuid.UUID  `db:"user_id" json:"user_id"`
	FilePath       string     `db:"file_path" json:"file_path"`
	LineNumber     int        `db:"line_number" json:"line_number"`
	DiffSide       string     `db:"diff_side" json:"diff_side"`
	Body           string     `db:"body" json:"body"`
	Resolved       bool       `db:"resolved" json:"resolved"`
	ResolvedAt     *time.Time `db:"resolved_at" json:"resolved_at,omitempty"`
	ResolvedByPass *int       `db:"resolved_by_pass" json:"resolved_by_pass,omitempty"`
	PassNumber     int        `db:"pass_number" json:"pass_number"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
}

// ReviewComment represents a captured review comment on a 143-generated PR.
type ReviewComment struct {
	ID              uuid.UUID `db:"id" json:"id"`
	PullRequestID   uuid.UUID `db:"pull_request_id" json:"pull_request_id"`
	OrgID           uuid.UUID `db:"org_id" json:"org_id"`
	GitHubCommentID int64     `db:"github_comment_id" json:"github_comment_id"`
	Reviewer        string    `db:"reviewer" json:"reviewer"`
	Body            string    `db:"body" json:"body"`
	DiffPath        *string   `db:"diff_path" json:"diff_path,omitempty"`
	DiffPosition    *int      `db:"diff_position" json:"diff_position,omitempty"`
	FilterStatus    string    `db:"filter_status" json:"filter_status"`
	Category        *string   `db:"category" json:"category,omitempty"`
	Actionable      bool      `db:"actionable" json:"actionable"`
	Generalizable   bool      `db:"generalizable" json:"generalizable"`
	GeneralizedRule *string   `db:"generalized_rule" json:"generalized_rule,omitempty"`
	Summary         *string   `db:"summary" json:"summary,omitempty"`
	Applied         bool      `db:"applied" json:"applied"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
}

// Memory represents a learned convention or rule for a repo or org.
type Memory struct {
	ID               uuid.UUID   `db:"id" json:"id"`
	OrgID            uuid.UUID   `db:"org_id" json:"org_id"`
	Repo             string      `db:"repo" json:"repo"`
	Rule             string      `db:"rule" json:"rule"`
	Category         string      `db:"category" json:"category"`
	SourceCommentIDs []uuid.UUID `db:"source_comment_ids" json:"source_comment_ids"`
	OccurrenceCount  int         `db:"occurrence_count" json:"occurrence_count"`
	Status           string      `db:"status" json:"status"`
	ManuallyCurated  bool        `db:"manually_curated" json:"manually_curated"`
	Active           bool        `db:"active" json:"active"`
	Scope            string      `db:"scope" json:"scope"`
	Source           string      `db:"source" json:"source"`
	LastUsedAt       *time.Time  `db:"last_used_at" json:"last_used_at,omitempty"`
	TimesReinforced  int         `db:"times_reinforced" json:"times_reinforced"`
	FilePatterns     []string    `db:"file_patterns" json:"file_patterns,omitempty"`
	CreatedAt        time.Time   `db:"created_at" json:"created_at"`
}
