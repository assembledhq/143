package models

import (
	"encoding/json"
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
	ID           uuid.UUID `db:"id" json:"id"`
	OrgID        uuid.UUID `db:"org_id" json:"org_id"`
	Email        string    `db:"email" json:"email"`
	Name         string    `db:"name" json:"name"`
	Role         string    `db:"role" json:"role"`
	GitHubID     *int64    `db:"github_id" json:"github_id,omitempty"`
	GitHubLogin  *string   `db:"github_login" json:"github_login,omitempty"`
	AvatarURL    *string   `db:"avatar_url" json:"avatar_url,omitempty"`
	PasswordHash *string   `db:"password_hash" json:"-"`
	GoogleID     *string   `db:"google_id" json:"google_id,omitempty"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
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
	ID                 uuid.UUID           `db:"id" json:"id"`
	OrgID              uuid.UUID           `db:"org_id" json:"org_id"`
	Provider           IntegrationProvider `db:"provider" json:"provider"`
	Config             json.RawMessage     `db:"config" json:"-"` // never expose config in JSON (contains secrets)
	GitHubAppInstalled *bool               `db:"-" json:"github_app_installed,omitempty"`
	Status             IntegrationStatus   `db:"status" json:"status"`
	LastSyncedAt       *time.Time          `db:"last_synced_at" json:"last_synced_at,omitempty"`
	CreatedAt          time.Time           `db:"created_at" json:"created_at"`
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

// RepositoryStatus is a typed string for the `repositories.status` column.
// Defined as a distinct type (not a plain string alias) so that boundary-layer
// APIs like RepositoryStore.SetStatus reject arbitrary callers passing raw
// strings and require one of the known constants below.
type RepositoryStatus string

const (
	RepositoryStatusActive       RepositoryStatus = "active"
	RepositoryStatusDisconnected RepositoryStatus = "disconnected"
)

// IsActive reports whether the repo is currently usable for new work. Disconnected
// repos remain readable (existing sessions still load) but must be rejected from
// any code path that creates new sessions, runs, projects, or automations.
func (r Repository) IsActive() bool {
	return RepositoryStatus(r.Status) == RepositoryStatusActive
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
	Status                string          `db:"status" json:"status"`
	FirstSeenAt           time.Time       `db:"first_seen_at" json:"first_seen_at"`
	LastSeenAt            time.Time       `db:"last_seen_at" json:"last_seen_at"`
	OccurrenceCount       int             `db:"occurrence_count" json:"occurrence_count"`
	AffectedCustomerCount int             `db:"affected_customer_count" json:"affected_customer_count"`
	Severity              string          `db:"severity" json:"severity"`
	Tags                  []string        `db:"tags" json:"tags"`
	Fingerprint           string          `db:"fingerprint" json:"fingerprint"`
	DeletedAt             *time.Time      `db:"deleted_at" json:"-"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at" json:"updated_at"`
}

// Session represents an attempt to fix an issue via a coding agent.
type Session struct {
	ID                  uuid.UUID `db:"id" json:"id"`
	IssueID             uuid.UUID `db:"issue_id" json:"issue_id"`
	OrgID               uuid.UUID `db:"org_id" json:"org_id"`
	AgentType           AgentType `db:"agent_type" json:"agent_type"`
	Status              string    `db:"status" json:"status"`
	AutonomyLevel       string    `db:"autonomy_level" json:"autonomy_level"`
	TokenMode           string    `db:"token_mode" json:"token_mode"`
	ComplexityTier      *int      `db:"complexity_tier" json:"complexity_tier,omitempty"`
	ConfidenceScore     *float64  `db:"confidence_score" json:"confidence_score,omitempty"`
	ConfidenceReasoning *string   `db:"confidence_reasoning" json:"confidence_reasoning,omitempty"`
	RiskFactors         []string  `db:"risk_factors" json:"risk_factors,omitempty"`
	// ContainerID is the Docker container hosting the session's sandbox when
	// one is live. Non-null only while at least one holder
	// (TurnHoldingContainer or an active preview) is keeping it alive — see
	// SessionStore.AcquireTurnHold / ReleaseTurnHold for the state machine.
	ContainerID *string `db:"container_id" json:"container_id,omitempty"`
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
	FailureRetryAdvised bool            `db:"failure_retry_advised" json:"failure_retry_advised"`
	ParentSessionID     *uuid.UUID      `db:"parent_session_id" json:"parent_session_id,omitempty"`
	RevisionContext     json.RawMessage `db:"revision_context" json:"revision_context,omitempty"`
	Error               *string         `db:"error" json:"error,omitempty"`
	ResultSummary       *string         `db:"result_summary" json:"result_summary,omitempty"`
	Diff                *string         `db:"diff" json:"diff,omitempty"`
	PMPlanID            *uuid.UUID      `db:"pm_plan_id" json:"pm_plan_id,omitempty"`
	Title               *string         `db:"title" json:"title,omitempty"`
	PMApproach          *string         `db:"pm_approach" json:"pm_approach,omitempty"`
	PMReasoning         *string         `db:"pm_reasoning" json:"pm_reasoning,omitempty"`
	ProjectTaskID       *uuid.UUID      `db:"project_task_id" json:"project_task_id,omitempty"`
	ModelOverride       *string         `db:"model_override" json:"model_override,omitempty"`
	TriggeredByUserID   *uuid.UUID      `db:"triggered_by_user_id" json:"triggered_by_user_id,omitempty"`
	AgentSessionID      *string         `db:"agent_session_id" json:"agent_session_id,omitempty"`
	CurrentTurn         int             `db:"current_turn" json:"current_turn"`
	// LastActivityAt is the timestamp of the last write to this session — used
	// as the MRU sort key in ListByOrg. NOT NULL since migration 000077;
	// previously it could be NULL for first-turn sessions.
	LastActivityAt time.Time       `db:"last_activity_at" json:"last_activity_at"`
	SandboxState   string          `db:"sandbox_state" json:"sandbox_state"`
	SnapshotKey    *string         `db:"snapshot_key" json:"snapshot_key,omitempty"`
	TargetBranch   *string         `db:"target_branch" json:"target_branch,omitempty"`
	WorkingBranch  *string         `db:"working_branch" json:"working_branch,omitempty"`
	BaseCommitSHA  *string         `db:"base_commit_sha" json:"base_commit_sha,omitempty"`
	RepositoryID   *uuid.UUID      `db:"repository_id" json:"repository_id,omitempty"`
	DiffStats      json.RawMessage `db:"diff_stats" json:"diff_stats,omitempty"` // nil for list queries (excluded to reduce payload size)
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
	PRCreationState      PRCreationState `db:"pr_creation_state" json:"pr_creation_state"`
	PRCreationError      *string         `db:"pr_creation_error" json:"pr_creation_error,omitempty"`
	DiffCollectedAt      *time.Time      `db:"diff_collected_at" json:"diff_collected_at,omitempty"`
	LatestDiffSnapshotID *uuid.UUID      `db:"latest_diff_snapshot_id" json:"latest_diff_snapshot_id,omitempty"`
	DeletedAt            *time.Time      `db:"deleted_at" json:"-"`
	CreatedAt            time.Time       `db:"created_at" json:"created_at"`
}

// SessionDetail is the API response for a single session, enriched with threads.
type SessionDetail struct {
	Session
	Threads []SessionThread `json:"threads"`
}

// SessionResult holds the result fields to update on an agent run.
type SessionResult struct {
	ConfidenceScore     *float64        `json:"confidence_score,omitempty"`
	ConfidenceReasoning *string         `json:"confidence_reasoning,omitempty"`
	RiskFactors         []string        `json:"risk_factors,omitempty"`
	TokenUsage          json.RawMessage `json:"token_usage,omitempty"`
	ResultSummary       *string         `json:"result_summary,omitempty"`
	Diff                *string         `json:"diff,omitempty"`
	Error               *string         `json:"error,omitempty"`
	FailureCategory     *string         `json:"failure_category,omitempty"`
	DiffBaseCommitSHA   *string         `json:"-"`
	DiffHeadCommitSHA   *string         `json:"-"`
	DiffCollectedAt     *time.Time      `json:"-"`
	DiffSource          string          `json:"-"`
}

type SessionDiffSnapshot struct {
	ID             uuid.UUID `db:"id" json:"id"`
	SessionID      uuid.UUID `db:"session_id" json:"session_id"`
	OrgID          uuid.UUID `db:"org_id" json:"org_id"`
	TurnNumber     int       `db:"turn_number" json:"turn_number"`
	SequenceNumber int       `db:"sequence_number" json:"sequence_number"`
	Source         string    `db:"source" json:"source"`
	BaseCommitSHA  string    `db:"base_commit_sha" json:"base_commit_sha"`
	HeadCommitSHA  *string   `db:"head_commit_sha" json:"head_commit_sha,omitempty"`
	WorkingBranch  *string   `db:"working_branch" json:"working_branch,omitempty"`
	TargetBranch   *string   `db:"target_branch" json:"target_branch,omitempty"`
	Diff           string    `db:"diff" json:"diff"`
	FilesChanged   int       `db:"files_changed" json:"files_changed"`
	LinesAdded     int       `db:"lines_added" json:"lines_added"`
	LinesRemoved   int       `db:"lines_removed" json:"lines_removed"`
	CapturedAt     time.Time `db:"captured_at" json:"captured_at"`
}

// Validation represents validation results for an agent run.
type Validation struct {
	ID                  uuid.UUID       `db:"id" json:"id"`
	SessionID           uuid.UUID       `db:"session_id" json:"session_id"`
	OrgID               uuid.UUID       `db:"org_id" json:"org_id"`
	Status              string          `db:"status" json:"status"`
	DirectionCheck      string          `db:"direction_check" json:"direction_check"`
	CorrectnessCheck    string          `db:"correctness_check" json:"correctness_check"`
	QualityCheck        string          `db:"quality_check" json:"quality_check"`
	SecurityScan        string          `db:"security_scan" json:"security_scan"`
	RegressionTestCheck string          `db:"regression_test_check" json:"regression_test_check"`
	CoverageDelta       json.RawMessage `db:"coverage_delta" json:"coverage_delta,omitempty"`
	CICheck             string          `db:"ci_check" json:"ci_check"`
	Details             json.RawMessage `db:"details" json:"details,omitempty"`
	StartedAt           *time.Time      `db:"started_at" json:"started_at,omitempty"`
	CompletedAt         *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt           time.Time       `db:"created_at" json:"created_at"`
}

// PullRequest represents a GitHub PR created by an agent run.
// NOTE: SessionID is nullable (*uuid.UUID) because PRs can be created manually
// without an associated session. API consumers should handle null session_id.
type PullRequest struct {
	ID             uuid.UUID  `db:"id" json:"id"`
	SessionID      *uuid.UUID `db:"session_id" json:"session_id,omitempty"`
	OrgID          uuid.UUID  `db:"org_id" json:"org_id"`
	GitHubPRNumber int        `db:"github_pr_number" json:"github_pr_number"`
	GitHubPRURL    string     `db:"github_pr_url" json:"github_pr_url"`
	GitHubRepo     string     `db:"github_repo" json:"github_repo"`
	Title          string     `db:"title" json:"title"`
	Body           *string    `db:"body" json:"body,omitempty"`
	Status         string     `db:"status" json:"status"`
	ReviewStatus   string     `db:"review_status" json:"review_status"`
	AuthoredBy     string     `db:"authored_by" json:"authored_by"`
	CIStatus       string     `db:"ci_status" json:"ci_status"`
	MergedAt       *time.Time `db:"merged_at" json:"merged_at,omitempty"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
}

// PRSummary is a lightweight view of a PR for inclusion in session list responses.
type PRSummary struct {
	Status   string `json:"status"`
	CIStatus string `json:"ci_status"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
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
	Level      string          `db:"level" json:"level"`
	Message    string          `db:"message" json:"message"`
	Metadata   json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	TurnNumber int             `db:"turn_number" json:"turn_number"`
}

// SessionMessage represents a chat message in a multi-turn session.
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
	TokenUsage  json.RawMessage        `db:"token_usage" json:"token_usage,omitempty"`
	CreatedAt   time.Time              `db:"created_at" json:"created_at"`
}

// SessionThread represents an agent thread within a multi-agent session.
// Each thread is one agent doing one piece of work. All threads in a session
// share the same container and filesystem.
type SessionThread struct {
	ID                 uuid.UUID    `db:"id" json:"id"`
	SessionID          uuid.UUID    `db:"session_id" json:"session_id"`
	OrgID              uuid.UUID    `db:"org_id" json:"org_id"`
	AgentType          AgentType    `db:"agent_type" json:"agent_type"`
	ModelOverride      *string      `db:"model_override" json:"model_override,omitempty"`
	Label              string       `db:"label" json:"label"`
	Instructions       *string      `db:"instructions" json:"instructions,omitempty"`
	FileScope          []string     `db:"file_scope" json:"file_scope,omitempty"`
	Status             ThreadStatus `db:"status" json:"status"`
	AgentSessionID     *string      `db:"agent_session_id" json:"agent_session_id,omitempty"`
	CurrentTurn        int          `db:"current_turn" json:"current_turn"`
	LastActivityAt     *time.Time   `db:"last_activity_at" json:"last_activity_at,omitempty"`
	ConfidenceScore    *float64     `db:"confidence_score" json:"confidence_score,omitempty"`
	ResultSummary      *string      `db:"result_summary" json:"result_summary,omitempty"`
	Diff               *string      `db:"diff" json:"diff,omitempty"`
	FailureExplanation *string      `db:"failure_explanation" json:"failure_explanation,omitempty"`
	FailureCategory    *string      `db:"failure_category" json:"failure_category,omitempty"`
	StartedAt          *time.Time   `db:"started_at" json:"started_at,omitempty"`
	CompletedAt        *time.Time   `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt          time.Time    `db:"created_at" json:"created_at"`
}

// SessionQuestion represents a question the agent asks a human during a run.
type SessionQuestion struct {
	ID           uuid.UUID  `db:"id" json:"id"`
	SessionID    uuid.UUID  `db:"session_id" json:"session_id"`
	OrgID        uuid.UUID  `db:"org_id" json:"org_id"`
	QuestionText string     `db:"question_text" json:"question_text"`
	Options      []string   `db:"options" json:"options,omitempty"`
	Context      *string    `db:"context" json:"context,omitempty"`
	BlocksPhase  *string    `db:"blocks_phase" json:"blocks_phase,omitempty"`
	AnswerText   *string    `db:"answer_text" json:"answer_text,omitempty"`
	AnsweredBy   *uuid.UUID `db:"answered_by" json:"answered_by,omitempty"`
	AnsweredAt   *time.Time `db:"answered_at" json:"answered_at,omitempty"`
	Status       string     `db:"status" json:"status"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
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
	JobTypePMAnalyze        = "pm_analyze"
	JobTypePMBootstrap      = "pm_bootstrap"
	JobTypePMContextRefresh = "pm_context_refresh"
	JobTypeProjectCycle     = "project_cycle"
	JobTypeAutomationRun    = "automation_run"
)

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
	LeaseExpiresAt *time.Time      `db:"lease_expires_at" json:"lease_expires_at,omitempty"`
	LockToken      *uuid.UUID      `db:"lock_token" json:"lock_token,omitempty"`
	RunOwnerID     *string         `db:"run_owner_id" json:"run_owner_id,omitempty"`
	LastError      *string         `db:"last_error" json:"last_error,omitempty"`
	DedupeKey      *string         `db:"dedupe_key" json:"dedupe_key,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	CompletedAt    *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
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
