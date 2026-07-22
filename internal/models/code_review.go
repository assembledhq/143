package models

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type CodeReviewApprovalMode string

const (
	CodeReviewApprovalModeCommentOnly       CodeReviewApprovalMode = "comment_only"
	CodeReviewApprovalModeApproveAcceptable CodeReviewApprovalMode = "approve_acceptable"
)

func (m CodeReviewApprovalMode) Validate() error {
	switch m {
	case CodeReviewApprovalModeCommentOnly, CodeReviewApprovalModeApproveAcceptable:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewApprovalMode: %q", m)
	}
}

type CodeReviewPolicyEditSource string

const (
	CodeReviewPolicyEditSourceManual  CodeReviewPolicyEditSource = "manual"
	CodeReviewPolicyEditSourceExample CodeReviewPolicyEditSource = "example"
	CodeReviewPolicyEditSourceReset   CodeReviewPolicyEditSource = "reset"
)

func (s CodeReviewPolicyEditSource) Validate() error {
	switch s {
	case CodeReviewPolicyEditSourceManual, CodeReviewPolicyEditSourceExample, CodeReviewPolicyEditSourceReset:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewPolicyEditSource: %q", s)
	}
}

type CodeReviewSessionStatus string

const (
	CodeReviewSessionStatusQueued    CodeReviewSessionStatus = "queued"
	CodeReviewSessionStatusRunning   CodeReviewSessionStatus = "running"
	CodeReviewSessionStatusCompleted CodeReviewSessionStatus = "completed"
	CodeReviewSessionStatusFailed    CodeReviewSessionStatus = "failed"
	CodeReviewSessionStatusStale     CodeReviewSessionStatus = "stale"
	CodeReviewSessionStatusCancelled CodeReviewSessionStatus = "cancelled"
)

func (s CodeReviewSessionStatus) Validate() error {
	switch s {
	case CodeReviewSessionStatusQueued, CodeReviewSessionStatusRunning, CodeReviewSessionStatusCompleted,
		CodeReviewSessionStatusFailed, CodeReviewSessionStatusStale, CodeReviewSessionStatusCancelled:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewSessionStatus: %q", s)
	}
}

type CodeReviewDecision string

const (
	CodeReviewDecisionApproved         CodeReviewDecision = "approved"
	CodeReviewDecisionCommentOnly      CodeReviewDecision = "comment_only"
	CodeReviewDecisionNeedsHumanReview CodeReviewDecision = "needs_human_review"
	CodeReviewDecisionBlocked          CodeReviewDecision = "blocked"
)

func (d CodeReviewDecision) Validate() error {
	switch d {
	case CodeReviewDecisionApproved, CodeReviewDecisionCommentOnly, CodeReviewDecisionNeedsHumanReview, CodeReviewDecisionBlocked:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDecision: %q", d)
	}
}

// CodeReviewListOutcome groups review decisions into the two completion
// outcomes operators most often need to distinguish on the Code reviews page.
// It is a list-filter contract rather than persisted review state.
type CodeReviewListOutcome string

const (
	CodeReviewListOutcomeAutomaticallyApproved CodeReviewListOutcome = "automatically_approved"
	CodeReviewListOutcomeCompletedNotApproved  CodeReviewListOutcome = "completed_not_approved"
)

func (o CodeReviewListOutcome) Validate() error {
	switch o {
	case CodeReviewListOutcomeAutomaticallyApproved, CodeReviewListOutcomeCompletedNotApproved:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewListOutcome: %q", o)
	}
}

// CodeReviewUpdatedEvent is fanned out over the org-scoped code review SSE
// stream whenever a review row is created or its status/decision changes. The
// frontend treats it as a "the list moved, refetch" signal rather than reading
// individual fields off it (Redis pub/sub is at-most-once and unordered, so the
// canonical record is whatever the list endpoint returns on invalidation).
type CodeReviewUpdatedEvent struct {
	OrgID uuid.UUID `json:"org_id"`
	// SessionID is nil for batch transitions that touch many rows at once
	// (e.g. marking a PR's prior reviews stale on a new head), which have no
	// single session. A pointer is required for omitempty to actually fire —
	// uuid.UUID is a fixed-size array and never counts as "empty" to encoding/json.
	SessionID *uuid.UUID              `json:"session_id,omitempty"`
	Status    CodeReviewSessionStatus `json:"status,omitempty"`
	Decision  *CodeReviewDecision     `json:"decision,omitempty"`
	UpdatedAt time.Time               `json:"updated_at"`
}

type CodeReviewTriggerSource string

const (
	CodeReviewTriggerSourceAppReviewer   CodeReviewTriggerSource = "app_reviewer"
	CodeReviewTriggerSourceAliasReviewer CodeReviewTriggerSource = "alias_reviewer"
	CodeReviewTriggerSourceTeamReviewer  CodeReviewTriggerSource = "team_reviewer"
	CodeReviewTriggerSourceSlashCommand  CodeReviewTriggerSource = "slash_command"
	CodeReviewTriggerSourceAutoPolicy    CodeReviewTriggerSource = "auto_policy"
)

func (s CodeReviewTriggerSource) Validate() error {
	switch s {
	case CodeReviewTriggerSourceAppReviewer, CodeReviewTriggerSourceAliasReviewer, CodeReviewTriggerSourceTeamReviewer,
		CodeReviewTriggerSourceSlashCommand, CodeReviewTriggerSourceAutoPolicy:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewTriggerSource: %q", s)
	}
}

const (
	DefaultCodeReviewGitHubTriggerTeamName = "143 Code Reviewer"
	DefaultCodeReviewGitHubTriggerTeamSlug = "143-code-reviewer"
	DefaultCodeReviewGitHubTriggerRepoPerm = CodeReviewGitHubTriggerRepoPermissionPull
)

type CodeReviewGitHubTriggerRepoPermission string

const (
	CodeReviewGitHubTriggerRepoPermissionPull CodeReviewGitHubTriggerRepoPermission = "pull"
)

func (p CodeReviewGitHubTriggerRepoPermission) Validate() error {
	switch p {
	case CodeReviewGitHubTriggerRepoPermissionPull:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewGitHubTriggerRepoPermission: %q", p)
	}
}

type CodeReviewGitHubTriggerStatus string

const (
	CodeReviewGitHubTriggerStatusUnconfigured       CodeReviewGitHubTriggerStatus = "unconfigured"
	CodeReviewGitHubTriggerStatusReady              CodeReviewGitHubTriggerStatus = "ready"
	CodeReviewGitHubTriggerStatusAuthRequired       CodeReviewGitHubTriggerStatus = "auth_required"
	CodeReviewGitHubTriggerStatusPermissionRequired CodeReviewGitHubTriggerStatus = "permission_required"
	CodeReviewGitHubTriggerStatusError              CodeReviewGitHubTriggerStatus = "error"
)

func (s CodeReviewGitHubTriggerStatus) Validate() error {
	switch s {
	case CodeReviewGitHubTriggerStatusUnconfigured, CodeReviewGitHubTriggerStatusReady,
		CodeReviewGitHubTriggerStatusAuthRequired, CodeReviewGitHubTriggerStatusPermissionRequired,
		CodeReviewGitHubTriggerStatusError:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewGitHubTriggerStatus: %q", s)
	}
}

type CodeReviewGitHubTriggerSetting struct {
	ID              uuid.UUID                             `db:"id" json:"id"`
	OrgID           uuid.UUID                             `db:"org_id" json:"org_id"`
	RepositoryID    uuid.UUID                             `db:"repository_id" json:"repository_id"`
	InstallationID  int64                                 `db:"installation_id" json:"installation_id"`
	Active          bool                                  `db:"active" json:"active"`
	Version         int                                   `db:"version" json:"version"`
	TeamSlug        string                                `db:"team_slug" json:"team_slug"`
	TeamName        string                                `db:"team_name" json:"team_name"`
	TeamID          int64                                 `db:"team_id" json:"team_id"`
	RepoPermission  CodeReviewGitHubTriggerRepoPermission `db:"repo_permission" json:"repo_permission"`
	CreatedByUserID *uuid.UUID                            `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time                             `db:"created_at" json:"created_at"`
}

type CodeReviewGitHubTriggerResponse struct {
	Status             CodeReviewGitHubTriggerStatus         `json:"status"`
	RepositoryID       uuid.UUID                             `json:"repository_id"`
	RepositoryFullName string                                `json:"repository_full_name,omitempty"`
	GitHubOrg          string                                `json:"github_org,omitempty"`
	TeamSlug           string                                `json:"team_slug"`
	TeamName           string                                `json:"team_name"`
	TeamReviewer       string                                `json:"team_reviewer,omitempty"`
	RepoPermission     CodeReviewGitHubTriggerRepoPermission `json:"repo_permission"`
	Trigger            *CodeReviewGitHubTriggerSetting       `json:"trigger,omitempty"`
	Message            string                                `json:"message,omitempty"`
}

type CodeReviewAgentRole string

const (
	CodeReviewAgentRoleReviewer     CodeReviewAgentRole = "reviewer"
	CodeReviewAgentRoleOrchestrator CodeReviewAgentRole = "orchestrator"
)

func (r CodeReviewAgentRole) Validate() error {
	switch r {
	case CodeReviewAgentRoleReviewer, CodeReviewAgentRoleOrchestrator:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewAgentRole: %q", r)
	}
}

type CodeReviewAgentResultStatus string

const (
	CodeReviewAgentResultStatusQueued    CodeReviewAgentResultStatus = "queued"
	CodeReviewAgentResultStatusRunning   CodeReviewAgentResultStatus = "running"
	CodeReviewAgentResultStatusCompleted CodeReviewAgentResultStatus = "completed"
	CodeReviewAgentResultStatusFailed    CodeReviewAgentResultStatus = "failed"
	CodeReviewAgentResultStatusTimedOut  CodeReviewAgentResultStatus = "timed_out"
)

func (s CodeReviewAgentResultStatus) Validate() error {
	switch s {
	case CodeReviewAgentResultStatusQueued, CodeReviewAgentResultStatusRunning, CodeReviewAgentResultStatusCompleted,
		CodeReviewAgentResultStatusFailed, CodeReviewAgentResultStatusTimedOut:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewAgentResultStatus: %q", s)
	}
}

type CodeReviewFindingSeverity string

const (
	CodeReviewFindingSeverityInfo     CodeReviewFindingSeverity = "info"
	CodeReviewFindingSeverityLow      CodeReviewFindingSeverity = "low"
	CodeReviewFindingSeverityMedium   CodeReviewFindingSeverity = "medium"
	CodeReviewFindingSeverityHigh     CodeReviewFindingSeverity = "high"
	CodeReviewFindingSeverityCritical CodeReviewFindingSeverity = "critical"
)

func (s CodeReviewFindingSeverity) Validate() error {
	switch s {
	case CodeReviewFindingSeverityInfo, CodeReviewFindingSeverityLow, CodeReviewFindingSeverityMedium,
		CodeReviewFindingSeverityHigh, CodeReviewFindingSeverityCritical:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewFindingSeverity: %q", s)
	}
}

type CodeReviewFindingConfidence string

const (
	CodeReviewFindingConfidenceLow    CodeReviewFindingConfidence = "low"
	CodeReviewFindingConfidenceMedium CodeReviewFindingConfidence = "medium"
	CodeReviewFindingConfidenceHigh   CodeReviewFindingConfidence = "high"
)

func (c CodeReviewFindingConfidence) Validate() error {
	switch c {
	case CodeReviewFindingConfidenceLow, CodeReviewFindingConfidenceMedium, CodeReviewFindingConfidenceHigh:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewFindingConfidence: %q", c)
	}
}

type CodeReviewDescriptionApplicabilityKind string

const (
	CodeReviewDescriptionApplicabilityAll        CodeReviewDescriptionApplicabilityKind = "all"
	CodeReviewDescriptionApplicabilityNontrivial CodeReviewDescriptionApplicabilityKind = "nontrivial"
	CodeReviewDescriptionApplicabilityFrontend   CodeReviewDescriptionApplicabilityKind = "frontend_or_ui_visible"
	CodeReviewDescriptionApplicabilityPaths      CodeReviewDescriptionApplicabilityKind = "paths"
	CodeReviewDescriptionApplicabilityCategories CodeReviewDescriptionApplicabilityKind = "categories"
	CodeReviewDescriptionApplicabilityTests      CodeReviewDescriptionApplicabilityKind = "tests_changed"
)

func (k CodeReviewDescriptionApplicabilityKind) Validate() error {
	switch k {
	case "", CodeReviewDescriptionApplicabilityAll, CodeReviewDescriptionApplicabilityNontrivial,
		CodeReviewDescriptionApplicabilityFrontend, CodeReviewDescriptionApplicabilityPaths,
		CodeReviewDescriptionApplicabilityCategories, CodeReviewDescriptionApplicabilityTests:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDescriptionApplicabilityKind: %q", k)
	}
}

type CodeReviewDescriptionApplicability struct {
	Kind                    CodeReviewDescriptionApplicabilityKind `json:"kind,omitempty"`
	MinFilesChanged         int                                    `json:"min_files_changed,omitempty"`
	MinLinesChanged         int                                    `json:"min_lines_changed,omitempty"`
	PathPatterns            []string                               `json:"path_patterns,omitempty"`
	Categories              []string                               `json:"categories,omitempty"`
	RequireTestFilesChanged bool                                   `json:"require_test_files_changed,omitempty"`
}

func (a CodeReviewDescriptionApplicability) Empty() bool {
	return a.Kind == "" &&
		a.MinFilesChanged == 0 &&
		a.MinLinesChanged == 0 &&
		len(a.PathPatterns) == 0 &&
		len(a.Categories) == 0 &&
		!a.RequireTestFilesChanged
}

func (a CodeReviewDescriptionApplicability) Validate() error {
	if err := a.Kind.Validate(); err != nil {
		return err
	}
	if a.MinFilesChanged < 0 || a.MinLinesChanged < 0 {
		return fmt.Errorf("description applicability thresholds must not be negative")
	}
	return nil
}

type CodeReviewDescriptionRequirement struct {
	Key           string                             `json:"key"`
	Title         string                             `json:"title"`
	Prompt        string                             `json:"prompt"`
	Required      bool                               `json:"required"`
	Applicability string                             `json:"applicability,omitempty"`
	AppliesWhen   CodeReviewDescriptionApplicability `json:"applies_when,omitempty"`
}

type CodeReviewDescriptionPolicy struct {
	Requirements []CodeReviewDescriptionRequirement `json:"requirements"`
}

type CodeReviewRiskPolicy struct {
	MaxFilesChanged       int                   `json:"max_files_changed"`
	MaxLinesChanged       int                   `json:"max_lines_changed"`
	RequirePassingChecks  bool                  `json:"require_passing_checks"`
	ExcludeSensitivePaths bool                  `json:"exclude_sensitive_paths"`
	SensitivePaths        []string              `json:"sensitive_paths,omitempty"`
	AllowedPathPatterns   []string              `json:"allowed_path_patterns,omitempty"`
	BlockedPathPatterns   []string              `json:"blocked_path_patterns,omitempty"`
	ExcludeCategories     []string              `json:"exclude_categories,omitempty"`
	RequireUpToDate       bool                  `json:"require_up_to_date"`
	AllowForks            bool                  `json:"allow_forks"`
	AllowPolicyChanges    bool                  `json:"allow_policy_changes"`
	EligibleAuthors       []string              `json:"eligible_authors,omitempty"`
	RequiredChecks        []string              `json:"required_checks,omitempty"`
	LowRiskLane           CodeReviewLowRiskLane `json:"low_risk_lane,omitempty"`
}

// CodeReviewLowRiskLane relaxes a subset of approval prerequisites for changes
// whose risk categories all fall within a low-risk allowlist (e.g. docs-only
// changes). It never bypasses the substantive gates (sensitive/blocked paths,
// description policy, passing checks, mergeability, prompt injection, blocking
// findings); it only raises the churn ceiling and, optionally, waives the
// reviewer-quorum requirement so a clean low-risk change can approve on the
// heuristic gates even when the review agents time out.
type CodeReviewLowRiskLane struct {
	Enabled             bool     `json:"enabled"`
	Categories          []string `json:"categories,omitempty"`
	MaxLinesChanged     int      `json:"max_lines_changed,omitempty"`
	WaiveReviewerQuorum bool     `json:"waive_reviewer_quorum,omitempty"`
}

// CodeReviewLowRiskLaneApplies reports whether the lane is enabled and every
// risk category present in the change is contained in the lane's allowlist. An
// empty category set never qualifies — we only relax changes we can positively
// classify as low risk.
func CodeReviewLowRiskLaneApplies(lane CodeReviewLowRiskLane, categories []string) bool {
	if !lane.Enabled || len(lane.Categories) == 0 || len(categories) == 0 {
		return false
	}
	for _, category := range categories {
		if !stringInSlice(category, lane.Categories) {
			return false
		}
	}
	return true
}

type CodeReviewAgentRoster struct {
	Reviewers                []AgentType       `json:"reviewers"`
	Orchestrator             AgentType         `json:"orchestrator"`
	ReviewerModels           []string          `json:"reviewer_models,omitempty"`
	ReviewerReasoningEfforts []ReasoningEffort `json:"reviewer_reasoning_efforts,omitempty"`
	OrchestratorModel        *string           `json:"orchestrator_model,omitempty"`
	ReasoningEffort          ReasoningEffort   `json:"reasoning_effort,omitempty"`
	DisagreementBlocks       bool              `json:"disagreement_blocks"`
	RequireReviewerQuorum    int               `json:"require_reviewer_quorum"`
	TimeoutSeconds           int               `json:"timeout_seconds"`
}

// ReviewerReasoningEffort returns the explicit effort for one reviewer. The
// legacy roster-wide value remains the fallback for policies saved before
// reviewer_reasoning_efforts was introduced.
func (r CodeReviewAgentRoster) ReviewerReasoningEffort(index int) ReasoningEffort {
	if index >= 0 && index < len(r.ReviewerReasoningEfforts) && r.ReviewerReasoningEfforts[index] != "" {
		return r.ReviewerReasoningEfforts[index]
	}
	if r.ReasoningEffort != "" {
		return r.ReasoningEffort
	}
	return ReasoningEffortHigh
}

type CodeReviewPolicyConfig struct {
	Enabled                 bool                        `json:"enabled"`
	ApprovalMode            CodeReviewApprovalMode      `json:"approval_mode"`
	ReviewInstructions      string                      `json:"review_instructions"`
	AutomatedApprovalPolicy string                      `json:"automated_approval_policy"`
	DescriptionPolicy       CodeReviewDescriptionPolicy `json:"description_policy"`
	RiskPolicy              CodeReviewRiskPolicy        `json:"risk_policy"`
	AgentRoster             CodeReviewAgentRoster       `json:"agent_roster"`
	InlineCommentLimit      int                         `json:"inline_comment_limit"`
}

const CodeReviewPromptMaxRunes = 8000

type CodeReviewPolicyValidationError struct {
	Field   string
	Message string
}

func (e *CodeReviewPolicyValidationError) Error() string { return e.Message }

func codeReviewPolicyFieldError(field, message string) error {
	return &CodeReviewPolicyValidationError{Field: field, Message: message}
}

const codeReviewIndependentApprovalPolicy = `

Evaluate the pull request independently based on the code itself. Disregard GitHub checks, CI results, build statuses, and other external validation signals, whether passing, failing, or pending; they must not count for or against approval. Also disregard existing human review comments, review decisions, and review threads, whether open or resolved. Unresolved human review threads must not count against approval.`

const DefaultCodeReviewAutomatedApprovalPolicy = `Automatically approve routine changes when:
- the intent is clear and the change has a small, understandable scope
- there are no blocking findings
- the implementation follows established repository patterns
- the test coverage visible in the code is appropriate for the change

Require human review when:
- the change affects authentication, billing, permissions, infrastructure, or production data
- the change introduces a new architectural pattern or crosses unclear ownership boundaries
- the intended behavior cannot be determined from the pull request and repository context` + codeReviewIndependentApprovalPolicy

func DefaultCodeReviewPolicyConfig() CodeReviewPolicyConfig {
	return CodeReviewPolicyConfig{
		Enabled:                 true,
		ApprovalMode:            CodeReviewApprovalModeCommentOnly,
		ReviewInstructions:      "",
		AutomatedApprovalPolicy: DefaultCodeReviewAutomatedApprovalPolicy,
		DescriptionPolicy: CodeReviewDescriptionPolicy{Requirements: []CodeReviewDescriptionRequirement{
			{Key: "description", Title: "Understandable description", Required: true, Prompt: "Explain what is changing and why clearly enough for a reviewer to understand the intent."},
			{
				Key:           "testing",
				Title:         "Testing evidence",
				Required:      true,
				Applicability: "nontrivial",
				AppliesWhen: CodeReviewDescriptionApplicability{
					Kind:            CodeReviewDescriptionApplicabilityNontrivial,
					MinFilesChanged: 2,
					MinLinesChanged: 31,
				},
				Prompt: "Describe the testing or validation evidence for nontrivial changes.",
			},
			{
				Key:           "ui_evidence",
				Title:         "Screenshots or preview link",
				Required:      true,
				Applicability: "frontend_or_ui_visible",
				AppliesWhen: CodeReviewDescriptionApplicability{
					Kind: CodeReviewDescriptionApplicabilityFrontend,
					PathPatterns: []string{
						"frontend/**",
						"apps/web/**",
						"**/app/**",
						"**/components/**",
						"**/pages/**",
						"**/*.tsx",
						"**/*.jsx",
						"**/*.css",
					},
				},
				Prompt: "Include screenshots or a preview link for frontend or UI-visible changes.",
			},
		}},
		RiskPolicy: CodeReviewRiskPolicy{
			MaxFilesChanged:       5,
			MaxLinesChanged:       300,
			RequirePassingChecks:  false,
			ExcludeSensitivePaths: true,
			SensitivePaths:        defaultPRReadinessSensitivePaths(),
			ExcludeCategories:     []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"},
			RequireUpToDate:       false,
			AllowForks:            false,
			AllowPolicyChanges:    false,
			LowRiskLane: CodeReviewLowRiskLane{
				Enabled:             true,
				Categories:          []string{"docs"},
				MaxLinesChanged:     1000,
				WaiveReviewerQuorum: true,
			},
		},
		AgentRoster: CodeReviewAgentRoster{
			Reviewers:                []AgentType{AgentTypeCodex, AgentTypeClaudeCode},
			Orchestrator:             AgentTypeOpenCode,
			ReviewerModels:           []string{DefaultCodexModel, DefaultClaudeCodeModel},
			ReviewerReasoningEfforts: []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortHigh},
			OrchestratorModel:        strPtr(OpenCodeModelGPT55),
			ReasoningEffort:          ReasoningEffortHigh,
			DisagreementBlocks:       true,
			RequireReviewerQuorum:    2,
			TimeoutSeconds:           1800,
		},
		InlineCommentLimit: 4,
	}
}

func strPtr(value string) *string {
	return &value
}

func ResolveCodeReviewPolicyConfig(config *CodeReviewPolicyConfig) CodeReviewPolicyConfig {
	defaults := DefaultCodeReviewPolicyConfig()
	if config == nil {
		return defaults
	}
	if config.ApprovalMode != "" {
		defaults.ApprovalMode = config.ApprovalMode
	}
	defaults.Enabled = config.Enabled
	defaults.ReviewInstructions = strings.TrimSpace(config.ReviewInstructions)
	if config.AutomatedApprovalPolicy != "" {
		defaults.AutomatedApprovalPolicy = strings.TrimSpace(config.AutomatedApprovalPolicy)
	}
	if len(config.DescriptionPolicy.Requirements) > 0 {
		defaults.DescriptionPolicy = config.DescriptionPolicy
	}
	if config.RiskPolicy.MaxFilesChanged != 0 {
		defaults.RiskPolicy.MaxFilesChanged = config.RiskPolicy.MaxFilesChanged
	}
	if config.RiskPolicy.MaxLinesChanged != 0 {
		defaults.RiskPolicy.MaxLinesChanged = config.RiskPolicy.MaxLinesChanged
	}
	defaults.RiskPolicy.RequirePassingChecks = config.RiskPolicy.RequirePassingChecks
	defaults.RiskPolicy.ExcludeSensitivePaths = config.RiskPolicy.ExcludeSensitivePaths
	if len(config.RiskPolicy.SensitivePaths) > 0 {
		defaults.RiskPolicy.SensitivePaths = config.RiskPolicy.SensitivePaths
	}
	if len(config.RiskPolicy.AllowedPathPatterns) > 0 {
		defaults.RiskPolicy.AllowedPathPatterns = config.RiskPolicy.AllowedPathPatterns
	}
	if len(config.RiskPolicy.BlockedPathPatterns) > 0 {
		defaults.RiskPolicy.BlockedPathPatterns = config.RiskPolicy.BlockedPathPatterns
	}
	if len(config.RiskPolicy.ExcludeCategories) > 0 {
		defaults.RiskPolicy.ExcludeCategories = config.RiskPolicy.ExcludeCategories
	}
	defaults.RiskPolicy.RequireUpToDate = config.RiskPolicy.RequireUpToDate
	defaults.RiskPolicy.AllowForks = config.RiskPolicy.AllowForks
	defaults.RiskPolicy.AllowPolicyChanges = config.RiskPolicy.AllowPolicyChanges
	if len(config.RiskPolicy.EligibleAuthors) > 0 {
		defaults.RiskPolicy.EligibleAuthors = config.RiskPolicy.EligibleAuthors
	}
	if len(config.RiskPolicy.RequiredChecks) > 0 {
		defaults.RiskPolicy.RequiredChecks = config.RiskPolicy.RequiredChecks
	}
	// Only override the low-risk lane when the stored policy specifies one;
	// otherwise inherit the default docs lane so existing policies pick up the
	// relaxed handling without needing to be re-saved.
	if config.RiskPolicy.LowRiskLane.Enabled ||
		len(config.RiskPolicy.LowRiskLane.Categories) > 0 ||
		config.RiskPolicy.LowRiskLane.MaxLinesChanged != 0 {
		defaults.RiskPolicy.LowRiskLane = config.RiskPolicy.LowRiskLane
	}
	if len(config.AgentRoster.Reviewers) > 0 {
		defaults.AgentRoster = config.AgentRoster
		if defaults.AgentRoster.ReasoningEffort == "" {
			defaults.AgentRoster.ReasoningEffort = ReasoningEffortHigh
		}
		if len(defaults.AgentRoster.ReviewerReasoningEfforts) == 0 {
			defaults.AgentRoster.ReviewerReasoningEfforts = make([]ReasoningEffort, len(defaults.AgentRoster.Reviewers))
			for i := range defaults.AgentRoster.ReviewerReasoningEfforts {
				defaults.AgentRoster.ReviewerReasoningEfforts[i] = defaults.AgentRoster.ReasoningEffort
			}
		}
	}
	if config.InlineCommentLimit != 0 {
		defaults.InlineCommentLimit = config.InlineCommentLimit
	}
	defaults.DescriptionPolicy = normalizeCodeReviewDescriptionPolicy(defaults.DescriptionPolicy)
	return defaults
}

func normalizeCodeReviewDescriptionPolicy(policy CodeReviewDescriptionPolicy) CodeReviewDescriptionPolicy {
	// Policy configs are frequently copied by value, so clone the slice before
	// normalizing its elements to avoid mutating a caller's shared backing array.
	policy.Requirements = append([]CodeReviewDescriptionRequirement(nil), policy.Requirements...)
	for i := range policy.Requirements {
		if !policy.Requirements[i].AppliesWhen.Empty() {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(policy.Requirements[i].Applicability)) {
		case "nontrivial":
			policy.Requirements[i].AppliesWhen = CodeReviewDescriptionApplicability{
				Kind:            CodeReviewDescriptionApplicabilityNontrivial,
				MinFilesChanged: 2,
				MinLinesChanged: 31,
			}
		case "frontend_or_ui_visible", "frontend", "ui":
			policy.Requirements[i].AppliesWhen = CodeReviewDescriptionApplicability{
				Kind: CodeReviewDescriptionApplicabilityFrontend,
				PathPatterns: []string{
					"frontend/**",
					"apps/web/**",
					"**/app/**",
					"**/components/**",
					"**/pages/**",
					"**/*.tsx",
					"**/*.jsx",
					"**/*.css",
				},
			}
		case "", "all", "always":
			policy.Requirements[i].AppliesWhen = CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityAll}
		}
	}
	return policy
}

func (c CodeReviewPolicyConfig) Validate() error {
	if err := c.ApprovalMode.Validate(); err != nil {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldApprovalMode, err.Error())
	}
	if err := c.ValidatePromptFields(); err != nil {
		return err
	}
	if c.InlineCommentLimit < 1 || c.InlineCommentLimit > 10 {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldInlineCommentLimit, "inline_comment_limit must be between 1 and 10")
	}
	if c.RiskPolicy.MaxFilesChanged < 1 {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldRiskPolicy, "max_files_changed must be positive")
	}
	if c.RiskPolicy.MaxLinesChanged < 1 {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldRiskPolicy, "max_lines_changed must be positive")
	}
	for _, requirement := range c.DescriptionPolicy.Requirements {
		if err := requirement.AppliesWhen.Validate(); err != nil {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldDescriptionPolicy, err.Error())
		}
	}
	if len(c.AgentRoster.Reviewers) == 0 {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, "at least one reviewer agent is required")
	}
	if len(c.AgentRoster.ReviewerReasoningEfforts) > 0 && len(c.AgentRoster.ReviewerReasoningEfforts) != len(c.AgentRoster.Reviewers) {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, "reviewer_reasoning_efforts must match reviewer count")
	}
	for idx, agentType := range c.AgentRoster.Reviewers {
		if err := agentType.Validate(); err != nil {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, err.Error())
		}
		if !AgentSupportsNativeReview(agentType) {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("agent %q does not support native review", agentType))
		}
		reasoningEffort := c.AgentRoster.ReviewerReasoningEffort(idx)
		if len(c.AgentRoster.ReviewerReasoningEfforts) > 0 && c.AgentRoster.ReviewerReasoningEfforts[idx] == "" {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("reviewer reasoning effort %d must be non-empty", idx+1))
		}
		if err := reasoningEffort.Validate(); err != nil {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("invalid reviewer reasoning effort %d: %v", idx+1, err))
		}
		if agentType.SupportsReasoningEffort() && !agentType.SupportsReasoningEffortLevel(reasoningEffort) {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("reasoning effort %q is not supported by reviewer %q", reasoningEffort, agentType))
		}
	}
	if len(c.AgentRoster.ReviewerModels) > 0 && len(c.AgentRoster.ReviewerModels) != len(c.AgentRoster.Reviewers) {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, "reviewer_models must match reviewer count")
	}
	for idx, model := range c.AgentRoster.ReviewerModels {
		model = strings.TrimSpace(model)
		if model == "" {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("reviewer model %d must be non-empty", idx+1))
		}
		if err := ValidateModelForAgentType(c.AgentRoster.Reviewers[idx], model); err != nil {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("invalid reviewer model %d: %v", idx+1, err))
		}
	}
	if err := c.AgentRoster.Orchestrator.Validate(); err != nil {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, err.Error())
	}
	if !AgentSupportsNativeReview(c.AgentRoster.Orchestrator) {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("orchestrator %q does not support native review", c.AgentRoster.Orchestrator))
	}
	if c.AgentRoster.OrchestratorModel != nil && strings.TrimSpace(*c.AgentRoster.OrchestratorModel) != "" {
		if err := ValidateModelForAgentType(c.AgentRoster.Orchestrator, strings.TrimSpace(*c.AgentRoster.OrchestratorModel)); err != nil {
			return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("invalid orchestrator model: %v", err))
		}
	}
	if err := c.AgentRoster.ReasoningEffort.Validate(); err != nil {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, err.Error())
	}
	if c.AgentRoster.Orchestrator.SupportsReasoningEffort() && !c.AgentRoster.Orchestrator.SupportsReasoningEffortLevel(c.AgentRoster.ReasoningEffort) {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, fmt.Sprintf("reasoning effort %q is not supported by orchestrator %q", c.AgentRoster.ReasoningEffort, c.AgentRoster.Orchestrator))
	}
	if c.AgentRoster.RequireReviewerQuorum < 1 || c.AgentRoster.RequireReviewerQuorum > len(c.AgentRoster.Reviewers) {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, "require_reviewer_quorum must be between 1 and reviewer count")
	}
	if c.AgentRoster.TimeoutSeconds < 60 {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldAgentRoster, "timeout_seconds must be at least 60")
	}
	return nil
}

func (c CodeReviewPolicyConfig) ValidatePromptFields() error {
	if err := c.ApprovalMode.Validate(); err != nil {
		return err
	}
	for field, value := range map[string]string{"review_instructions": c.ReviewInstructions, "automated_approval_policy": c.AutomatedApprovalPolicy} {
		if !utf8.ValidString(value) {
			return codeReviewPolicyFieldError(field, fmt.Sprintf("%s must be valid UTF-8", field))
		}
		if utf8.RuneCountInString(value) > CodeReviewPromptMaxRunes {
			return codeReviewPolicyFieldError(field, fmt.Sprintf("%s must be at most %d characters", field, CodeReviewPromptMaxRunes))
		}
	}
	if c.ApprovalMode == CodeReviewApprovalModeApproveAcceptable && strings.TrimSpace(c.AutomatedApprovalPolicy) == "" {
		return codeReviewPolicyFieldError("automated_approval_policy", "automated_approval_policy must be non-empty when automatic approval is enabled")
	}
	return nil
}

type CodeReviewPolicyRecord struct {
	ID                      uuid.UUID                   `db:"id" json:"id"`
	OrgID                   uuid.UUID                   `db:"org_id" json:"org_id"`
	RepositoryID            *uuid.UUID                  `db:"repository_id" json:"repository_id,omitempty"`
	Active                  bool                        `db:"active" json:"active"`
	Version                 int                         `db:"version" json:"version"`
	Enabled                 bool                        `db:"enabled" json:"enabled"`
	ApprovalMode            CodeReviewApprovalMode      `db:"approval_mode" json:"approval_mode"`
	ReviewInstructions      string                      `db:"review_instructions" json:"review_instructions"`
	AutomatedApprovalPolicy string                      `db:"automated_approval_policy" json:"automated_approval_policy"`
	DescriptionPolicy       CodeReviewDescriptionPolicy `db:"-" json:"description_policy"`
	RiskPolicy              CodeReviewRiskPolicy        `db:"-" json:"risk_policy"`
	AgentRoster             CodeReviewAgentRoster       `db:"-" json:"agent_roster"`
	InlineCommentLimit      int                         `db:"inline_comment_limit" json:"inline_comment_limit"`
	CreatedByUserID         *uuid.UUID                  `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt               time.Time                   `db:"created_at" json:"created_at"`
}

func (r CodeReviewPolicyRecord) Config() CodeReviewPolicyConfig {
	config := CodeReviewPolicyConfig{
		ApprovalMode:            r.ApprovalMode,
		Enabled:                 r.Enabled,
		ReviewInstructions:      r.ReviewInstructions,
		AutomatedApprovalPolicy: r.AutomatedApprovalPolicy,
		DescriptionPolicy:       r.DescriptionPolicy,
		RiskPolicy:              r.RiskPolicy,
		AgentRoster:             r.AgentRoster,
		InlineCommentLimit:      r.InlineCommentLimit,
	}
	return ResolveCodeReviewPolicyConfig(&config)
}

type CodeReviewResolvedPolicy struct {
	Config CodeReviewPolicyConfig  `json:"config"`
	Source string                  `json:"source"`
	Policy *CodeReviewPolicyRecord `json:"policy,omitempty"`
}

const (
	CodeReviewPolicyFieldEnabled                 = "enabled"
	CodeReviewPolicyFieldApprovalMode            = "approval_mode"
	CodeReviewPolicyFieldReviewInstructions      = "review_instructions"
	CodeReviewPolicyFieldAutomatedApprovalPolicy = "automated_approval_policy"
	CodeReviewPolicyFieldDescriptionPolicy       = "description_policy"
	CodeReviewPolicyFieldRiskPolicy              = "risk_policy"
	CodeReviewPolicyFieldAgentRoster             = "agent_roster"
	CodeReviewPolicyFieldInlineCommentLimit      = "inline_comment_limit"
)

type CodeReviewSessionMetadata struct {
	ID                    uuid.UUID               `db:"id" json:"id"`
	OrgID                 uuid.UUID               `db:"org_id" json:"org_id"`
	SessionID             uuid.UUID               `db:"session_id" json:"session_id"`
	RepositoryID          uuid.UUID               `db:"repository_id" json:"repository_id"`
	PullRequestID         uuid.UUID               `db:"pull_request_id" json:"pull_request_id"`
	PolicyID              uuid.UUID               `db:"policy_id" json:"policy_id"`
	BaseSHA               string                  `db:"base_sha" json:"base_sha"`
	HeadSHA               string                  `db:"head_sha" json:"head_sha"`
	FromFork              bool                    `db:"from_fork" json:"from_fork"`
	TriggerSource         CodeReviewTriggerSource `db:"trigger_source" json:"trigger_source"`
	Status                CodeReviewSessionStatus `db:"status" json:"status"`
	Decision              *CodeReviewDecision     `db:"decision" json:"decision,omitempty"`
	Acceptable            *bool                   `db:"acceptable" json:"acceptable,omitempty"`
	Stale                 bool                    `db:"stale" json:"stale"`
	SupersededBySessionID *uuid.UUID              `db:"superseded_by_session_id" json:"superseded_by_session_id,omitempty"`
	ReviewOutputKey       string                  `db:"review_output_key" json:"review_output_key"`
	PromptArtifactKey     *string                 `db:"prompt_artifact_key" json:"prompt_artifact_key,omitempty"`
	GitHubReviewID        *int64                  `db:"github_review_id" json:"github_review_id,omitempty"`
	GitHubReviewURL       *string                 `db:"github_review_url" json:"github_review_url,omitempty"`
	FinalReviewBody       *string                 `db:"final_review_body" json:"final_review_body,omitempty"`
	FailureReason         *string                 `db:"failure_reason" json:"failure_reason,omitempty"`
	CompletedAt           *time.Time              `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt             time.Time               `db:"created_at" json:"created_at"`
}

type CodeReviewAgentResult struct {
	ID               uuid.UUID                   `db:"id" json:"id"`
	OrgID            uuid.UUID                   `db:"org_id" json:"org_id"`
	SessionID        uuid.UUID                   `db:"session_id" json:"session_id"`
	AgentProvider    string                      `db:"agent_provider" json:"agent_provider"`
	AgentModel       *string                     `db:"agent_model" json:"agent_model,omitempty"`
	Role             CodeReviewAgentRole         `db:"role" json:"role"`
	Status           CodeReviewAgentResultStatus `db:"status" json:"status"`
	RawOutput        *string                     `db:"raw_output" json:"raw_output,omitempty"`
	StructuredResult json.RawMessage             `db:"structured_result" json:"structured_result,omitempty"`
	CreatedAt        time.Time                   `db:"created_at" json:"created_at"`
}

type CodeReviewPromptArtifact struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	OrgID         uuid.UUID       `db:"org_id" json:"org_id"`
	SessionID     uuid.UUID       `db:"session_id" json:"session_id"`
	ArtifactKey   string          `db:"artifact_key" json:"artifact_key"`
	Role          string          `db:"role" json:"role"`
	AgentProvider string          `db:"agent_provider" json:"agent_provider,omitempty"`
	Content       string          `db:"content" json:"content"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
}

type CodeReviewFinding struct {
	ID                uuid.UUID                   `db:"id" json:"id"`
	OrgID             uuid.UUID                   `db:"org_id" json:"org_id"`
	SessionID         uuid.UUID                   `db:"session_id" json:"session_id"`
	AgentResultID     *uuid.UUID                  `db:"agent_result_id" json:"agent_result_id,omitempty"`
	DedupeKey         string                      `db:"dedupe_key" json:"dedupe_key"`
	Severity          CodeReviewFindingSeverity   `db:"severity" json:"severity"`
	Confidence        CodeReviewFindingConfidence `db:"confidence" json:"confidence"`
	Path              *string                     `db:"path" json:"path,omitempty"`
	StartLine         *int                        `db:"start_line" json:"start_line,omitempty"`
	EndLine           *int                        `db:"end_line" json:"end_line,omitempty"`
	Summary           string                      `db:"summary" json:"summary"`
	Body              string                      `db:"body" json:"body"`
	SelectedForInline bool                        `db:"selected_for_inline" json:"selected_for_inline"`
	GitHubCommentID   *int64                      `db:"github_comment_id" json:"github_comment_id,omitempty"`
	CreatedAt         time.Time                   `db:"created_at" json:"created_at"`
}

type CodeReviewListItem struct {
	CodeReviewSessionMetadata
	SessionTitle      *string `db:"session_title" json:"session_title,omitempty"`
	RepositoryName    *string `db:"repository_name" json:"repository_name,omitempty"`
	GitHubRepo        string  `db:"github_repo" json:"github_repo"`
	GitHubPRNumber    int     `db:"github_pr_number" json:"github_pr_number"`
	GitHubPRURL       string  `db:"github_pr_url" json:"github_pr_url"`
	PullRequestTitle  string  `db:"pull_request_title" json:"pull_request_title"`
	PullRequestAuthor string  `db:"pull_request_author" json:"pull_request_author"`
}

type CodeReviewEvidence struct {
	AgentResults    []CodeReviewAgentResult    `json:"agent_results"`
	Findings        []CodeReviewFinding        `json:"findings"`
	PromptArtifacts []CodeReviewPromptArtifact `json:"prompt_artifacts,omitempty"`
}

type CodeReviewTemplate string

const (
	CodeReviewTemplateDocsOnly      CodeReviewTemplate = "docs_and_comments_only"
	CodeReviewTemplateTestsOnly     CodeReviewTemplate = "tests_only"
	CodeReviewTemplateSmallFrontend CodeReviewTemplate = "small_frontend_change"
	CodeReviewTemplateSmallBackend  CodeReviewTemplate = "small_backend_change"
	CodeReviewTemplateSmallCombined CodeReviewTemplate = "small_combined_feature"
)

type CodeReviewTemplateOption struct {
	Key         CodeReviewTemplate     `json:"key"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Config      CodeReviewPolicyConfig `json:"config"`
}

type CodeReviewPromptExample string

const (
	CodeReviewPromptExampleBalanced        CodeReviewPromptExample = "balanced"
	CodeReviewPromptExampleSecurityFocused CodeReviewPromptExample = "security_focused"
	CodeReviewPromptExampleMinimal         CodeReviewPromptExample = "minimal"
)

type CodeReviewAutomatedApprovalExample string

const (
	CodeReviewAutomatedApprovalExampleConservative  CodeReviewAutomatedApprovalExample = "conservative_low_risk"
	CodeReviewAutomatedApprovalExampleDocumentation CodeReviewAutomatedApprovalExample = "documentation_only"
	CodeReviewAutomatedApprovalExampleSmallRoutine  CodeReviewAutomatedApprovalExample = "small_routine_changes"
)

type CodeReviewPromptExampleOption struct {
	Key          CodeReviewPromptExample `json:"key"`
	Title        string                  `json:"title"`
	Description  string                  `json:"description"`
	Instructions string                  `json:"instructions"`
}

type CodeReviewAutomatedApprovalExampleOption struct {
	Key         CodeReviewAutomatedApprovalExample `json:"key"`
	Title       string                             `json:"title"`
	Description string                             `json:"description"`
	Policy      string                             `json:"policy"`
}

type CodeReviewPromptExamplesResponse struct {
	ReviewInstructions        []CodeReviewPromptExampleOption            `json:"review_instructions"`
	AutomatedApprovalPolicies []CodeReviewAutomatedApprovalExampleOption `json:"automated_approval_policies"`
}

func CodeReviewPromptExamples() []CodeReviewPromptExampleOption {
	return []CodeReviewPromptExampleOption{
		{Key: CodeReviewPromptExampleBalanced, Title: "Balanced review", Description: "Correctness, security, tests, and maintainability.", Instructions: "Prioritize correctness, security, appropriate test coverage, and maintainability. Report actionable findings with concise reasoning and avoid low-value style comments."},
		{Key: CodeReviewPromptExampleSecurityFocused, Title: "Security-focused", Description: "Trust boundaries, authorization, data exposure, secrets, and abuse cases.", Instructions: "Focus on trust boundaries, authentication and authorization, tenant isolation, data exposure, secret handling, input validation, and realistic abuse cases. Explain exploitability and impact for each security finding."},
		{Key: CodeReviewPromptExampleMinimal, Title: "Minimal", Description: "Concise correctness-only review with low comment noise.", Instructions: "Report only concrete correctness defects that could change behavior or cause failures. Keep comments concise and omit style, naming, and speculative suggestions."},
	}
}

func CodeReviewAutomatedApprovalExamples() []CodeReviewAutomatedApprovalExampleOption {
	return []CodeReviewAutomatedApprovalExampleOption{
		{Key: CodeReviewAutomatedApprovalExampleConservative, Title: "Conservative low-risk approval", Description: "Approve routine changes and escalate uncertainty.", Policy: DefaultCodeReviewAutomatedApprovalPolicy},
		{Key: CodeReviewAutomatedApprovalExampleDocumentation, Title: "Documentation-only approval", Description: "Approve clear documentation changes while escalating executable or generated changes.", Policy: "Automatically approve clear, accurate documentation-only changes when they match the implementation and contain no executable, configuration, generated, or security-sensitive changes.\n\nRequire human review whenever the change affects runtime behavior, configuration, generated files, permissions, secrets, or the intended documentation behavior is ambiguous." + codeReviewIndependentApprovalPolicy},
		{Key: CodeReviewAutomatedApprovalExampleSmallRoutine, Title: "Small routine changes", Description: "Approve narrow changes that follow established patterns with proportionate tests.", Policy: "Automatically approve small, narrowly scoped changes that follow established repository patterns, have no blocking findings, and include test evidence proportionate to their risk.\n\nRequire human review for architectural changes, sensitive areas, unclear intent, reviewer disagreement, weak evidence, or any change whose impact cannot be evaluated confidently." + codeReviewIndependentApprovalPolicy},
	}
}

func CodeReviewPolicyTemplates() []CodeReviewTemplateOption {
	base := DefaultCodeReviewPolicyConfig()
	return []CodeReviewTemplateOption{
		{
			Key:         CodeReviewTemplateDocsOnly,
			Title:       "Docs and comments only",
			Description: "Approve only documentation/comment-only changes with passing checks.",
			Config: templatePolicy(base, templatePolicyOptions{
				maxFiles: 8,
				maxLines: 400,
				excludedCategories: []string{
					"dependencies", "auth", "billing", "permissions", "crypto", "infra", "migrations", "generated",
				},
				allowedPaths: []string{
					"docs/**", "**/*.md", "**/*.mdx", "**/*.txt", "**/*.rst", "**/*.adoc", "**/README*", "**/CHANGELOG*",
				},
				blockedPaths: []string{".github/**", "deploy/**", "infra/**", "**/.env*", "**/secrets/**"},
			}),
		},
		{
			Key:         CodeReviewTemplateTestsOnly,
			Title:       "Tests only",
			Description: "Approve isolated test and fixture changes with conservative churn limits.",
			Config: templatePolicy(base, templatePolicyOptions{
				maxFiles: 10,
				maxLines: 500,
				excludedCategories: []string{
					"dependencies", "auth", "billing", "permissions", "crypto", "infra", "migrations", "generated",
				},
				allowedPaths: []string{
					"tests/**", "test/**", "**/__tests__/**", "**/*_test.go", "**/*.test.ts", "**/*.test.tsx",
					"**/*.spec.ts", "**/*.spec.tsx", "fixtures/**", "**/fixtures/**", "testdata/**", "**/testdata/**",
				},
				blockedPaths: []string{"**/__snapshots__/**", "**/*.snap", "**/*.golden", "**/golden/**"},
			}),
		},
		{
			Key:         CodeReviewTemplateSmallFrontend,
			Title:       "Small frontend change",
			Description: "Approve small UI changes with screenshot or preview evidence.",
			Config: templatePolicy(base, templatePolicyOptions{
				maxFiles:           5,
				maxLines:           250,
				excludedCategories: []string{"auth", "billing", "permissions", "crypto", "infra", "dependencies"},
				blockedPaths: []string{
					"**/auth/**", "**/*auth*", "**/billing/**", "**/*billing*", "**/api/**", "**/queries/**",
					"**/services/**", "**/data/**", "migrations/**",
				},
			}),
		},
		{
			Key:         CodeReviewTemplateSmallBackend,
			Title:       "Small backend change",
			Description: "Approve small backend changes outside sensitive packages with test evidence.",
			Config: templatePolicy(base, templatePolicyOptions{
				maxFiles:           4,
				maxLines:           200,
				excludedCategories: []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"},
				blockedPaths:       []string{"migrations/**", "**/schema/**", "**/auth/**", "**/billing/**", ".github/**"},
			}),
		},
		{
			Key:         CodeReviewTemplateSmallCombined,
			Title:       "Small combined feature",
			Description: "Approve tightly scoped frontend/backend changes with evidence and passing checks.",
			Config: templatePolicy(base, templatePolicyOptions{
				maxFiles:           6,
				maxLines:           250,
				excludedCategories: []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"},
				blockedPaths:       []string{"migrations/**", "**/schema/**", "**/auth/**", "**/billing/**", ".github/**", "deploy/**"},
			}),
		},
	}
}

type templatePolicyOptions struct {
	maxFiles           int
	maxLines           int
	excludedCategories []string
	allowedPaths       []string
	blockedPaths       []string
}

func templatePolicy(base CodeReviewPolicyConfig, opts templatePolicyOptions) CodeReviewPolicyConfig {
	cfg := base
	cfg.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
	cfg.RiskPolicy.MaxFilesChanged = opts.maxFiles
	cfg.RiskPolicy.MaxLinesChanged = opts.maxLines
	cfg.RiskPolicy.ExcludeCategories = opts.excludedCategories
	cfg.RiskPolicy.AllowedPathPatterns = opts.allowedPaths
	cfg.RiskPolicy.BlockedPathPatterns = opts.blockedPaths
	return cfg
}

type CodeReviewRiskInput struct {
	FilesChanged          int
	LinesChanged          int
	ChangedPaths          []string
	Categories            []string
	ChecksPassing         bool
	RequiredChecksPassing map[string]bool
	DescriptionPassed     bool
	UpToDate              bool
	Author                string
	AuthorClass           string
	FromFork              bool
	BlockingFindings      int
	ReviewerDisagreement  bool
	ScopeMismatch         bool
	UnresolvedUncertainty bool
	PromptInjectionFound  bool
	ContextFetchFailed    bool
	HeadSHAChanged        bool
}

type CodeReviewRiskReasonCode string

const (
	CodeReviewRiskReasonReviewerDisabled     CodeReviewRiskReasonCode = "reviewer_disabled"
	CodeReviewRiskReasonContextUnavailable   CodeReviewRiskReasonCode = "context_unavailable"
	CodeReviewRiskReasonHeadChanged          CodeReviewRiskReasonCode = "head_changed"
	CodeReviewRiskReasonFilesLimitExceeded   CodeReviewRiskReasonCode = "files_limit_exceeded"
	CodeReviewRiskReasonLinesLimitExceeded   CodeReviewRiskReasonCode = "lines_limit_exceeded"
	CodeReviewRiskReasonChecksFailing        CodeReviewRiskReasonCode = "checks_failing"
	CodeReviewRiskReasonRequiredCheckFailing CodeReviewRiskReasonCode = "required_check_failing"
	CodeReviewRiskReasonDescriptionFailed    CodeReviewRiskReasonCode = "description_failed"
	CodeReviewRiskReasonBranchOutOfDate      CodeReviewRiskReasonCode = "branch_out_of_date"
	CodeReviewRiskReasonForkIneligible       CodeReviewRiskReasonCode = "fork_ineligible"
	CodeReviewRiskReasonAuthorIneligible     CodeReviewRiskReasonCode = "author_ineligible"
	// CodeReviewRiskReasonUnresolvedHumanReview is retained so historical decisions remain renderable.
	// New risk evaluations deliberately do not emit it.
	CodeReviewRiskReasonUnresolvedHumanReview        CodeReviewRiskReasonCode = "unresolved_human_review"
	CodeReviewRiskReasonBlockingFindings             CodeReviewRiskReasonCode = "blocking_findings"
	CodeReviewRiskReasonReviewerDisagreement         CodeReviewRiskReasonCode = "reviewer_disagreement"
	CodeReviewRiskReasonScopeMismatch                CodeReviewRiskReasonCode = "scope_mismatch"
	CodeReviewRiskReasonUnresolvedUncertainty        CodeReviewRiskReasonCode = "unresolved_uncertainty"
	CodeReviewRiskReasonPromptInjection              CodeReviewRiskReasonCode = "prompt_injection"
	CodeReviewRiskReasonSensitivePath                CodeReviewRiskReasonCode = "sensitive_path"
	CodeReviewRiskReasonPathOutsideScope             CodeReviewRiskReasonCode = "path_outside_scope"
	CodeReviewRiskReasonBlockedPath                  CodeReviewRiskReasonCode = "blocked_path"
	CodeReviewRiskReasonPolicyPathChanged            CodeReviewRiskReasonCode = "policy_path_changed"
	CodeReviewRiskReasonExcludedCategory             CodeReviewRiskReasonCode = "excluded_category"
	CodeReviewRiskReasonReviewerQuorum               CodeReviewRiskReasonCode = "reviewer_quorum"
	CodeReviewRiskReasonOrchestratorSynthesisInvalid CodeReviewRiskReasonCode = "orchestrator_synthesis_invalid"
)

func (c CodeReviewRiskReasonCode) Validate() error {
	switch c {
	case CodeReviewRiskReasonReviewerDisabled,
		CodeReviewRiskReasonContextUnavailable,
		CodeReviewRiskReasonHeadChanged,
		CodeReviewRiskReasonFilesLimitExceeded,
		CodeReviewRiskReasonLinesLimitExceeded,
		CodeReviewRiskReasonChecksFailing,
		CodeReviewRiskReasonRequiredCheckFailing,
		CodeReviewRiskReasonDescriptionFailed,
		CodeReviewRiskReasonBranchOutOfDate,
		CodeReviewRiskReasonForkIneligible,
		CodeReviewRiskReasonAuthorIneligible,
		CodeReviewRiskReasonUnresolvedHumanReview,
		CodeReviewRiskReasonBlockingFindings,
		CodeReviewRiskReasonReviewerDisagreement,
		CodeReviewRiskReasonScopeMismatch,
		CodeReviewRiskReasonUnresolvedUncertainty,
		CodeReviewRiskReasonPromptInjection,
		CodeReviewRiskReasonSensitivePath,
		CodeReviewRiskReasonPathOutsideScope,
		CodeReviewRiskReasonBlockedPath,
		CodeReviewRiskReasonPolicyPathChanged,
		CodeReviewRiskReasonExcludedCategory,
		CodeReviewRiskReasonReviewerQuorum,
		CodeReviewRiskReasonOrchestratorSynthesisInvalid:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewRiskReasonCode: %q", c)
	}
}

type CodeReviewRiskReason struct {
	Code    CodeReviewRiskReasonCode `json:"code"`
	Actual  int                      `json:"actual,omitempty"`
	Limit   int                      `json:"limit,omitempty"`
	Subject string                   `json:"subject,omitempty"`
}

func (r CodeReviewRiskReason) Message() string {
	switch r.Code {
	case CodeReviewRiskReasonReviewerDisabled:
		return "code reviewer is disabled by policy"
	case CodeReviewRiskReasonContextUnavailable:
		return "required PR context could not be fetched"
	case CodeReviewRiskReasonHeadChanged:
		return "PR head changed after review started"
	case CodeReviewRiskReasonFilesLimitExceeded:
		return fmt.Sprintf("changed files %d exceeds policy limit %d", r.Actual, r.Limit)
	case CodeReviewRiskReasonLinesLimitExceeded:
		return fmt.Sprintf("changed lines %d exceeds policy limit %d", r.Actual, r.Limit)
	case CodeReviewRiskReasonChecksFailing:
		return "required GitHub checks are not passing"
	case CodeReviewRiskReasonRequiredCheckFailing:
		return "required check is not passing: " + r.Subject
	case CodeReviewRiskReasonDescriptionFailed:
		return "PR description policy did not pass"
	case CodeReviewRiskReasonBranchOutOfDate:
		return "PR branch is not up to date"
	case CodeReviewRiskReasonForkIneligible:
		return "fork PRs are not eligible for approval"
	case CodeReviewRiskReasonAuthorIneligible:
		return "PR author is not eligible for automated approval"
	case CodeReviewRiskReasonUnresolvedHumanReview:
		return "unresolved human review threads are present"
	case CodeReviewRiskReasonBlockingFindings:
		return "review agents reported blocking findings"
	case CodeReviewRiskReasonReviewerDisagreement:
		return "reviewer agents disagreed on material risk"
	case CodeReviewRiskReasonScopeMismatch:
		return "orchestrator reported the change may not match the stated intent"
	case CodeReviewRiskReasonUnresolvedUncertainty:
		return "orchestrator reported unresolved uncertainty"
	case CodeReviewRiskReasonPromptInjection:
		return "possible prompt-injection attempt found in PR content"
	case CodeReviewRiskReasonSensitivePath:
		return "sensitive path changed: " + r.Subject
	case CodeReviewRiskReasonPathOutsideScope:
		return "path is outside allowed policy scope: " + r.Subject
	case CodeReviewRiskReasonBlockedPath:
		return "blocked path changed: " + r.Subject
	case CodeReviewRiskReasonPolicyPathChanged:
		return "code review policy/config path changed: " + r.Subject
	case CodeReviewRiskReasonExcludedCategory:
		return "excluded risk category changed: " + r.Subject
	case CodeReviewRiskReasonReviewerQuorum:
		return fmt.Sprintf("reviewer quorum %d is below policy requirement %d", r.Actual, r.Limit)
	case CodeReviewRiskReasonOrchestratorSynthesisInvalid:
		return "orchestrator did not produce a valid structured synthesis"
	default:
		return string(r.Code)
	}
}

func CodeReviewRiskReasonMessages(reasons []CodeReviewRiskReason) []string {
	messages := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if message := strings.TrimSpace(reason.Message()); message != "" {
			messages = append(messages, message)
		}
	}
	return messages
}

type CodeReviewRiskEvaluation struct {
	Acceptable    bool                   `json:"acceptable"`
	Reasons       []string               `json:"reasons"`
	ReasonDetails []CodeReviewRiskReason `json:"reason_details,omitempty"`
}

func (e *CodeReviewRiskEvaluation) AddReason(reason CodeReviewRiskReason) {
	e.Acceptable = false
	e.Reasons = append(e.Reasons, reason.Message())
	e.ReasonDetails = append(e.ReasonDetails, reason)
}

type CodeReviewDecisionEvaluation struct {
	Decision          CodeReviewDecision     `json:"decision"`
	Acceptable        bool                   `json:"acceptable"`
	RiskReasons       []string               `json:"risk_reasons,omitempty"`
	RiskReasonDetails []CodeReviewRiskReason `json:"risk_reason_details,omitempty"`
}

func EvaluateCodeReviewDecision(policy CodeReviewPolicyConfig, risk CodeReviewRiskEvaluation) CodeReviewDecisionEvaluation {
	if !risk.Acceptable {
		return CodeReviewDecisionEvaluation{
			Decision:          CodeReviewDecisionNeedsHumanReview,
			Acceptable:        false,
			RiskReasons:       append([]string(nil), risk.Reasons...),
			RiskReasonDetails: append([]CodeReviewRiskReason(nil), risk.ReasonDetails...),
		}
	}
	if policy.ApprovalMode == CodeReviewApprovalModeApproveAcceptable {
		return CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionApproved, Acceptable: true}
	}
	return CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionCommentOnly, Acceptable: true}
}

func EvaluateCodeReviewRisk(policy CodeReviewPolicyConfig, input CodeReviewRiskInput) CodeReviewRiskEvaluation {
	policy = ResolveCodeReviewPolicyConfig(&policy)
	risk := CodeReviewRiskEvaluation{Acceptable: true}
	if !policy.Enabled {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerDisabled})
	}
	if input.ContextFetchFailed {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonContextUnavailable})
	}
	if input.HeadSHAChanged {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonHeadChanged})
	}
	lowRisk := CodeReviewLowRiskLaneApplies(policy.RiskPolicy.LowRiskLane, input.Categories)
	if input.FilesChanged > policy.RiskPolicy.MaxFilesChanged {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: input.FilesChanged, Limit: policy.RiskPolicy.MaxFilesChanged})
	}
	maxLinesChanged := policy.RiskPolicy.MaxLinesChanged
	if lowRisk && policy.RiskPolicy.LowRiskLane.MaxLinesChanged > maxLinesChanged {
		maxLinesChanged = policy.RiskPolicy.LowRiskLane.MaxLinesChanged
	}
	if input.LinesChanged > maxLinesChanged {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: input.LinesChanged, Limit: maxLinesChanged})
	}
	if policy.RiskPolicy.RequirePassingChecks && !input.ChecksPassing {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonChecksFailing})
	}
	for _, check := range policy.RiskPolicy.RequiredChecks {
		if !input.RequiredChecksPassing[check] {
			risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: check})
		}
	}
	if !input.DescriptionPassed {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonDescriptionFailed})
	}
	if policy.RiskPolicy.RequireUpToDate && !input.UpToDate {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonBranchOutOfDate})
	}
	if input.FromFork && !policy.RiskPolicy.AllowForks {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonForkIneligible})
	}
	if len(policy.RiskPolicy.EligibleAuthors) > 0 && !codeReviewAuthorAllowed(input.Author, input.AuthorClass, policy.RiskPolicy.EligibleAuthors) {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonAuthorIneligible})
	}
	if input.BlockingFindings > 0 {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockingFindings})
	}
	if input.ReviewerDisagreement && policy.AgentRoster.DisagreementBlocks {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerDisagreement})
	}
	if input.ScopeMismatch {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonScopeMismatch})
	}
	if input.UnresolvedUncertainty {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonUnresolvedUncertainty})
	}
	if input.PromptInjectionFound {
		risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonPromptInjection})
	}
	if policy.RiskPolicy.ExcludeSensitivePaths {
		for _, path := range input.ChangedPaths {
			if matchesAnyCodeReviewPath(path, policy.RiskPolicy.SensitivePaths) {
				risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonSensitivePath, Subject: path})
			}
		}
	}
	if len(policy.RiskPolicy.AllowedPathPatterns) > 0 {
		for _, path := range input.ChangedPaths {
			if !matchesAnyCodeReviewPath(path, policy.RiskPolicy.AllowedPathPatterns) {
				risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonPathOutsideScope, Subject: path})
			}
		}
	}
	for _, path := range input.ChangedPaths {
		if matchesAnyCodeReviewPath(path, policy.RiskPolicy.BlockedPathPatterns) {
			risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockedPath, Subject: path})
		}
	}
	if !policy.RiskPolicy.AllowPolicyChanges {
		for _, path := range input.ChangedPaths {
			if isCodeReviewPolicyPath(path) {
				risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonPolicyPathChanged, Subject: path})
			}
		}
	}
	for _, category := range input.Categories {
		if stringInSlice(category, policy.RiskPolicy.ExcludeCategories) {
			risk.AddReason(CodeReviewRiskReason{Code: CodeReviewRiskReasonExcludedCategory, Subject: category})
		}
	}
	return risk
}

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func codeReviewAuthorAllowed(author, authorClass string, allowed []string) bool {
	author = strings.TrimSpace(author)
	authorClass = strings.ToLower(strings.TrimSpace(authorClass))
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "", "none":
			continue
		case "all", "*":
			return true
		case "human", "humans":
			if authorClass == "human" {
				return true
			}
		case "143", "143-authored", "app", "bot", "agent":
			if authorClass == "143" || authorClass == "app" || authorClass == "agent" {
				return true
			}
		default:
			if strings.EqualFold(author, item) {
				return true
			}
			if strings.HasPrefix(item, "login:") && strings.EqualFold(author, strings.TrimPrefix(item, "login:")) {
				return true
			}
		}
	}
	return false
}

func CodeReviewFindingSeverityRank(severity CodeReviewFindingSeverity) int {
	switch severity {
	case CodeReviewFindingSeverityCritical:
		return 5
	case CodeReviewFindingSeverityHigh:
		return 4
	case CodeReviewFindingSeverityMedium:
		return 3
	case CodeReviewFindingSeverityLow:
		return 2
	case CodeReviewFindingSeverityInfo:
		return 1
	default:
		return 0
	}
}

func CodeReviewFindingConfidenceRank(confidence CodeReviewFindingConfidence) int {
	switch confidence {
	case CodeReviewFindingConfidenceHigh:
		return 3
	case CodeReviewFindingConfidenceMedium:
		return 2
	case CodeReviewFindingConfidenceLow:
		return 1
	default:
		return 0
	}
}

func SortCodeReviewFindingsForInline(findings []CodeReviewFinding) []CodeReviewFinding {
	sorted := append([]CodeReviewFinding(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		leftSeverity := CodeReviewFindingSeverityRank(sorted[i].Severity)
		rightSeverity := CodeReviewFindingSeverityRank(sorted[j].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity > rightSeverity
		}
		leftConfidence := CodeReviewFindingConfidenceRank(sorted[i].Confidence)
		rightConfidence := CodeReviewFindingConfidenceRank(sorted[j].Confidence)
		if leftConfidence != rightConfidence {
			return leftConfidence > rightConfidence
		}
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	return sorted
}

func matchesAnyCodeReviewPath(path string, patterns []string) bool {
	path = normalizeCodeReviewPath(path)
	for _, pattern := range patterns {
		pattern = normalizeCodeReviewPath(pattern)
		if pattern == "" {
			continue
		}
		if codeReviewPathPatternMatches(pattern, path) {
			return true
		}
	}
	return false
}

func normalizeCodeReviewPath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	path = strings.TrimPrefix(path, "./")
	return strings.ToLower(path)
}

func codeReviewPathPatternMatches(pattern, path string) bool {
	if pattern == "*" || pattern == "**" {
		return true
	}
	trimmedTreePattern := strings.TrimSuffix(pattern, "/**")
	if trimmedTreePattern != pattern && (trimmedTreePattern == path || strings.HasPrefix(path, trimmedTreePattern+"/")) {
		return true
	}
	if ok, err := filepath.Match(pattern, path); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "**") {
		regexPattern := regexp.QuoteMeta(pattern)
		regexPattern = strings.ReplaceAll(regexPattern, `\*\*`, `.*`)
		regexPattern = strings.ReplaceAll(regexPattern, `\*`, `[^/]*`)
		if ok, err := regexp.MatchString("^"+regexPattern+"$", path); err == nil && ok {
			return true
		}
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if codeReviewPathPatternMatches(suffix, path) || strings.HasSuffix(path, "/"+suffix) {
			return true
		}
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") && strings.Contains(path, strings.Trim(pattern, "*")) {
		return true
	}
	return pattern == path || strings.HasPrefix(path, pattern+"/") || strings.HasPrefix(path, pattern)
}

func isCodeReviewPolicyPath(path string) bool {
	return matchesAnyCodeReviewPath(path, []string{
		"docs/design/future/112-code-reviewer-bot-auto-approval.md",
		"internal/models/code_review",
		"internal/db/code_reviews",
		"internal/api/handlers/code_reviews",
		"migrations/",
	})
}
