package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PRFeedbackMonitoring string

const (
	PRFeedbackMonitoringInherit  PRFeedbackMonitoring = "inherit"
	PRFeedbackMonitoringEnabled  PRFeedbackMonitoring = "enabled"
	PRFeedbackMonitoringDisabled PRFeedbackMonitoring = "disabled"
)

func (v PRFeedbackMonitoring) Validate() error {
	switch v {
	case "", PRFeedbackMonitoringInherit, PRFeedbackMonitoringEnabled, PRFeedbackMonitoringDisabled:
		return nil
	}
	return fmt.Errorf("invalid PR feedback monitoring: %q", v)
}

type PRFeedbackHumanMode string

const (
	PRFeedbackHumanModeAllTrusted PRFeedbackHumanMode = "all_trusted_humans"
	PRFeedbackHumanModeMentions   PRFeedbackHumanMode = "mentions"
	PRFeedbackHumanModeOff        PRFeedbackHumanMode = "off"
)

func (v PRFeedbackHumanMode) Validate() error {
	switch v {
	case "", PRFeedbackHumanModeAllTrusted, PRFeedbackHumanModeMentions, PRFeedbackHumanModeOff:
		return nil
	}
	return fmt.Errorf("invalid PR feedback human mode: %q", v)
}
func (v PRFeedbackHumanMode) Effective() PRFeedbackHumanMode {
	if v == "" {
		return PRFeedbackHumanModeAllTrusted
	}
	return v
}

type PRFeedbackBotMode string

const (
	PRFeedbackBotModeAll       PRFeedbackBotMode = "all"
	PRFeedbackBotModeAllowlist PRFeedbackBotMode = "allowlist"
	PRFeedbackBotModeNone      PRFeedbackBotMode = "none"
)

func (v PRFeedbackBotMode) Validate() error {
	switch v {
	case "", PRFeedbackBotModeAll, PRFeedbackBotModeAllowlist, PRFeedbackBotModeNone:
		return nil
	}
	return fmt.Errorf("invalid PR feedback bot mode: %q", v)
}
func (v PRFeedbackBotMode) Effective() PRFeedbackBotMode {
	if v == "" {
		return PRFeedbackBotModeAll
	}
	return v
}

// NullableCycleLimit preserves all four setting states: absent, explicit null,
// zero (disabled), and a positive finite limit.
type NullableCycleLimit struct {
	Set   bool
	Value *int
}

func (v *NullableCycleLimit) UnmarshalJSON(data []byte) error {
	v.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		v.Value = nil
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("PR feedback bot cycle limit: %w", err)
	}
	v.Value = &n
	return v.Validate()
}
func (v NullableCycleLimit) MarshalJSON() ([]byte, error) {
	if v.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*v.Value)
}
func (v NullableCycleLimit) IsZero() bool { return !v.Set }
func (v NullableCycleLimit) Validate() error {
	if !v.Set || v.Value == nil {
		return nil
	}
	if *v.Value < 0 || *v.Value > 100 {
		return fmt.Errorf("PR feedback bot cycle limit must be between 0 and 100")
	}
	return nil
}
func (v NullableCycleLimit) Effective() *int {
	if v.Set {
		return v.Value
	}
	n := 3
	return &n
}

type PRFeedbackSurface string

const (
	PRFeedbackSurfaceIssueComment  PRFeedbackSurface = "issue_comment"
	PRFeedbackSurfaceReviewBody    PRFeedbackSurface = "review_body"
	PRFeedbackSurfaceReviewComment PRFeedbackSurface = "review_comment"
)

func (v PRFeedbackSurface) Validate() error {
	switch v {
	case PRFeedbackSurfaceIssueComment, PRFeedbackSurfaceReviewBody, PRFeedbackSurfaceReviewComment:
		return nil
	}
	return fmt.Errorf("invalid PR feedback surface: %q", v)
}

type PRFeedbackIntent string

const (
	PRFeedbackIntentUnknown         PRFeedbackIntent = "unknown"
	PRFeedbackIntentChangeRequest   PRFeedbackIntent = "change_request"
	PRFeedbackIntentQuestion        PRFeedbackIntent = "question"
	PRFeedbackIntentMixed           PRFeedbackIntent = "mixed"
	PRFeedbackIntentAcknowledgement PRFeedbackIntent = "acknowledgement"
	PRFeedbackIntentUnsafe          PRFeedbackIntent = "unsafe_or_unsupported"
)

func (v PRFeedbackIntent) Validate() error {
	switch v {
	case PRFeedbackIntentUnknown, PRFeedbackIntentChangeRequest, PRFeedbackIntentQuestion, PRFeedbackIntentMixed, PRFeedbackIntentAcknowledgement, PRFeedbackIntentUnsafe:
		return nil
	}
	return fmt.Errorf("invalid PR feedback intent: %q", v)
}

type PRFeedbackItemStatus string

const (
	PRFeedbackItemStatusPending        PRFeedbackItemStatus = "pending"
	PRFeedbackItemStatusIgnored        PRFeedbackItemStatus = "ignored"
	PRFeedbackItemStatusClaimed        PRFeedbackItemStatus = "claimed"
	PRFeedbackItemStatusRunning        PRFeedbackItemStatus = "running"
	PRFeedbackItemStatusResponded      PRFeedbackItemStatus = "responded"
	PRFeedbackItemStatusNeedsAttention PRFeedbackItemStatus = "needs_attention"
	PRFeedbackItemStatusCancelled      PRFeedbackItemStatus = "cancelled"
)

func (v PRFeedbackItemStatus) Validate() error {
	switch v {
	case PRFeedbackItemStatusPending, PRFeedbackItemStatusIgnored, PRFeedbackItemStatusClaimed, PRFeedbackItemStatusRunning, PRFeedbackItemStatusResponded, PRFeedbackItemStatusNeedsAttention, PRFeedbackItemStatusCancelled:
		return nil
	}
	return fmt.Errorf("invalid PR feedback item status: %q", v)
}

type PRFeedbackBatchStatus string

const (
	PRFeedbackBatchStatusCollecting     PRFeedbackBatchStatus = "collecting"
	PRFeedbackBatchStatusQueued         PRFeedbackBatchStatus = "queued"
	PRFeedbackBatchStatusRunning        PRFeedbackBatchStatus = "running"
	PRFeedbackBatchStatusPushing        PRFeedbackBatchStatus = "pushing"
	PRFeedbackBatchStatusResponding     PRFeedbackBatchStatus = "responding"
	PRFeedbackBatchStatusCompleted      PRFeedbackBatchStatus = "completed"
	PRFeedbackBatchStatusNeedsAttention PRFeedbackBatchStatus = "needs_attention"
	PRFeedbackBatchStatusCancelled      PRFeedbackBatchStatus = "cancelled"
)

func (v PRFeedbackBatchStatus) Validate() error {
	switch v {
	case PRFeedbackBatchStatusCollecting, PRFeedbackBatchStatusQueued, PRFeedbackBatchStatusRunning, PRFeedbackBatchStatusPushing, PRFeedbackBatchStatusResponding, PRFeedbackBatchStatusCompleted, PRFeedbackBatchStatusNeedsAttention, PRFeedbackBatchStatusCancelled:
		return nil
	}
	return fmt.Errorf("invalid PR feedback batch status: %q", v)
}

type PRFeedbackBatchSourceKind string

const (
	PRFeedbackBatchSourceHumanOrMixed PRFeedbackBatchSourceKind = "human_or_mixed"
	PRFeedbackBatchSourceBotOnly      PRFeedbackBatchSourceKind = "bot_only"
)

func (v PRFeedbackBatchSourceKind) Validate() error {
	switch v {
	case PRFeedbackBatchSourceHumanOrMixed, PRFeedbackBatchSourceBotOnly:
		return nil
	}
	return fmt.Errorf("invalid PR feedback batch source kind: %q", v)
}

type PRFeedbackBotEligibilitySource string

const (
	PRFeedbackBotEligibilityNone             PRFeedbackBotEligibilitySource = ""
	PRFeedbackBotEligibilityPrivateAll       PRFeedbackBotEligibilitySource = "private_repository_all"
	PRFeedbackBotEligibilityGitHubFirstParty PRFeedbackBotEligibilitySource = "github_first_party"
	PRFeedbackBotEligibilityInstalledApp     PRFeedbackBotEligibilitySource = "repository_installed_app"
	PRFeedbackBotEligibilityAllowlist        PRFeedbackBotEligibilitySource = "explicit_allowlist"
)

func (v PRFeedbackBotEligibilitySource) Validate() error {
	switch v {
	case PRFeedbackBotEligibilityNone, PRFeedbackBotEligibilityPrivateAll, PRFeedbackBotEligibilityGitHubFirstParty, PRFeedbackBotEligibilityInstalledApp, PRFeedbackBotEligibilityAllowlist:
		return nil
	}
	return fmt.Errorf("invalid PR feedback bot eligibility source: %q", v)
}

type PRFeedbackAuthorType string

const (
	PRFeedbackAuthorTypeUser         PRFeedbackAuthorType = "User"
	PRFeedbackAuthorTypeBot          PRFeedbackAuthorType = "Bot"
	PRFeedbackAuthorTypeMannequin    PRFeedbackAuthorType = "Mannequin"
	PRFeedbackAuthorTypeOrganization PRFeedbackAuthorType = "Organization"
	PRFeedbackAuthorTypeUnknown      PRFeedbackAuthorType = "Unknown"
)

func (v PRFeedbackAuthorType) Validate() error {
	switch v {
	case PRFeedbackAuthorTypeUser, PRFeedbackAuthorTypeBot, PRFeedbackAuthorTypeMannequin,
		PRFeedbackAuthorTypeOrganization, PRFeedbackAuthorTypeUnknown:
		return nil
	}
	return fmt.Errorf("invalid PR feedback author type: %q", v)
}

// PRFeedbackWorkspaceMode records how the canonical PR workspace was prepared
// for a feedback batch. Its values intentionally match PR repair workspace
// modes, but the type remains feedback-specific so the two workflows can
// evolve independently.
type PRFeedbackWorkspaceMode string

const (
	PRFeedbackWorkspaceModeSnapshotContinuation PRFeedbackWorkspaceMode = "snapshot_continuation"
	PRFeedbackWorkspaceModePRHeadReconstruction PRFeedbackWorkspaceMode = "pr_head_reconstruction"
)

func (v PRFeedbackWorkspaceMode) Validate() error {
	switch v {
	case "", PRFeedbackWorkspaceModeSnapshotContinuation, PRFeedbackWorkspaceModePRHeadReconstruction:
		return nil
	}
	return fmt.Errorf("invalid PR feedback workspace mode: %q", v)
}

type PullRequestFeedbackBatch struct {
	ID               uuid.UUID                 `db:"id" json:"id"`
	OrgID            uuid.UUID                 `db:"org_id" json:"org_id"`
	PullRequestID    uuid.UUID                 `db:"pull_request_id" json:"pull_request_id"`
	SessionID        uuid.UUID                 `db:"session_id" json:"session_id"`
	ThreadID         *uuid.UUID                `db:"thread_id" json:"thread_id,omitempty"`
	Status           PRFeedbackBatchStatus     `db:"status" json:"status"`
	SourceKind       PRFeedbackBatchSourceKind `db:"source_kind" json:"source_kind"`
	BotFeedbackEpoch *int64                    `db:"bot_feedback_epoch" json:"bot_feedback_epoch,omitempty"`
	ExpectedHeadSHA  string                    `db:"expected_head_sha" json:"expected_head_sha"`
	ResultHeadSHA    *string                   `db:"result_head_sha" json:"result_head_sha,omitempty"`
	WorkspaceMode    *PRFeedbackWorkspaceMode  `db:"workspace_mode" json:"workspace_mode,omitempty"`
	FeedbackSnapshot json.RawMessage           `db:"feedback_snapshot" json:"feedback_snapshot"`
	DebounceUntil    time.Time                 `db:"debounce_until" json:"debounce_until"`
	MaxCollectUntil  time.Time                 `db:"max_collect_until" json:"max_collect_until"`
	AttemptCount     int                       `db:"attempt_count" json:"attempt_count"`
	ResultSummary    *string                   `db:"result_summary" json:"result_summary,omitempty"`
	ErrorCode        *string                   `db:"error_code" json:"error_code,omitempty"`
	ErrorDetail      *string                   `db:"error_detail" json:"error_detail,omitempty"`
	StartedAt        *time.Time                `db:"started_at" json:"started_at,omitempty"`
	CompletedAt      *time.Time                `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt        time.Time                 `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time                 `db:"updated_at" json:"updated_at"`
}

type PullRequestFeedbackItem struct {
	ID                        uuid.UUID                      `db:"id" json:"id"`
	OrgID                     uuid.UUID                      `db:"org_id" json:"org_id"`
	PullRequestID             uuid.UUID                      `db:"pull_request_id" json:"pull_request_id"`
	BatchID                   *uuid.UUID                     `db:"batch_id" json:"batch_id,omitempty"`
	Surface                   PRFeedbackSurface              `db:"surface" json:"surface"`
	ProviderObjectID          int64                          `db:"provider_object_id" json:"provider_object_id"`
	GitHubDeliveryID          *string                        `db:"github_delivery_id" json:"github_delivery_id,omitempty"`
	GitHubReviewID            *int64                         `db:"github_review_id" json:"github_review_id,omitempty"`
	GitHubThreadRootCommentID *int64                         `db:"github_thread_root_comment_id" json:"github_thread_root_comment_id,omitempty"`
	InReplyToCommentID        *int64                         `db:"in_reply_to_comment_id" json:"in_reply_to_comment_id,omitempty"`
	GitHubAppID               *int64                         `db:"github_app_id" json:"github_app_id,omitempty"`
	GitHubAppSlug             *string                        `db:"github_app_slug" json:"github_app_slug,omitempty"`
	AuthorLogin               string                         `db:"author_login" json:"author_login"`
	AuthorType                PRFeedbackAuthorType           `db:"author_type" json:"author_type"`
	AuthorAssociation         string                         `db:"author_association" json:"author_association"`
	BotEligibilitySource      PRFeedbackBotEligibilitySource `db:"bot_eligibility_source" json:"bot_eligibility_source"`
	Body                      string                         `db:"body" json:"body"`
	BodyHash                  string                         `db:"body_hash" json:"body_hash"`
	ProcessedBodyHash         *string                        `db:"processed_body_hash" json:"processed_body_hash,omitempty"`
	ProviderFindingKey        *string                        `db:"provider_finding_key" json:"provider_finding_key,omitempty"`
	FindingFingerprint        *string                        `db:"finding_fingerprint" json:"finding_fingerprint,omitempty"`
	AutomaticAttemptCount     int                            `db:"automatic_attempt_count" json:"automatic_attempt_count"`
	Path                      *string                        `db:"path" json:"path,omitempty"`
	Line                      *int                           `db:"line" json:"line,omitempty"`
	Side                      *string                        `db:"side" json:"side,omitempty"`
	DiffHunk                  *string                        `db:"diff_hunk" json:"diff_hunk,omitempty"`
	CommentCommitSHA          *string                        `db:"comment_commit_sha" json:"comment_commit_sha,omitempty"`
	ObservedHeadSHA           string                         `db:"observed_head_sha" json:"observed_head_sha"`
	Intent                    PRFeedbackIntent               `db:"intent" json:"intent"`
	Status                    PRFeedbackItemStatus           `db:"status" json:"status"`
	IgnoreReason              *string                        `db:"ignore_reason" json:"ignore_reason,omitempty"`
	GitHubResponseCommentID   *int64                         `db:"github_response_comment_id" json:"github_response_comment_id,omitempty"`
	ResponseBody              *string                        `db:"response_body" json:"response_body,omitempty"`
	ResponseCommitSHA         *string                        `db:"response_commit_sha" json:"response_commit_sha,omitempty"`
	ProviderCreatedAt         *time.Time                     `db:"provider_created_at" json:"provider_created_at,omitempty"`
	ProviderUpdatedAt         *time.Time                     `db:"provider_updated_at" json:"provider_updated_at,omitempty"`
	ReceivedAt                time.Time                      `db:"received_at" json:"received_at"`
	ProcessedAt               *time.Time                     `db:"processed_at" json:"processed_at,omitempty"`
	UpdatedAt                 time.Time                      `db:"updated_at" json:"updated_at"`
}

type PRFeedbackBotScope string

const (
	PRFeedbackBotScopeAllPrivate    PRFeedbackBotScope = "all_private_repository_bots"
	PRFeedbackBotScopeTrustedPublic PRFeedbackBotScope = "installed_or_first_party_public_bots"
	PRFeedbackBotScopeSelected      PRFeedbackBotScope = "selected_bots"
	PRFeedbackBotScopeNone          PRFeedbackBotScope = "none"
)

type PullRequestFeedbackState struct {
	PullRequestID          uuid.UUID                 `json:"pull_request_id"`
	EffectiveMode          PRFeedbackHumanMode       `json:"effective_mode"`
	EffectiveBotMode       PRFeedbackBotMode         `json:"effective_bot_mode"`
	EffectiveBotCycleLimit *int                      `json:"effective_bot_cycle_limit"`
	BotScope               PRFeedbackBotScope        `json:"bot_scope"`
	Monitoring             PRFeedbackMonitoring      `json:"monitoring"`
	PausedReason           string                    `json:"paused_reason,omitempty"`
	PendingCount           int                       `json:"pending_count"`
	NeedsAttentionCount    int                       `json:"needs_attention_count"`
	ActiveBatch            *PullRequestFeedbackBatch `json:"active_batch,omitempty"`
	RecentItems            []PullRequestFeedbackItem `json:"recent_items"`
}

type UpdatePullRequestFeedbackMonitoringRequest struct {
	Monitoring PRFeedbackMonitoring `json:"monitoring" validate:"required"`
}

type PRFeedbackTriageResult struct {
	Intent             PRFeedbackIntent `json:"intent"`
	RequiresAgent      bool             `json:"requires_agent"`
	RequiresCodeChange bool             `json:"requires_code_change"`
	Reason             string           `json:"reason"`
	FindingFingerprint string           `json:"finding_fingerprint,omitempty"`
}

func (r PRFeedbackTriageResult) Validate() error {
	if err := r.Intent.Validate(); err != nil {
		return err
	}
	if r.Intent == PRFeedbackIntentUnknown {
		return fmt.Errorf("PR feedback triage intent must be resolved")
	}
	if strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("PR feedback triage reason is required")
	}
	if r.Intent == PRFeedbackIntentAcknowledgement && (r.RequiresAgent || r.RequiresCodeChange) {
		return fmt.Errorf("acknowledgement feedback cannot require agent work")
	}
	if r.RequiresCodeChange && !r.RequiresAgent {
		return fmt.Errorf("code-changing feedback must require an agent")
	}
	return nil
}

func (r UpdatePullRequestFeedbackMonitoringRequest) Validate() error {
	if r.Monitoring == "" {
		return fmt.Errorf("monitoring is required")
	}
	return r.Monitoring.Validate()
}
