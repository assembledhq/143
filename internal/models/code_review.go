package models

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

type CodeReviewDescriptionRequirement struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	Prompt        string `json:"prompt"`
	Required      bool   `json:"required"`
	Applicability string `json:"applicability,omitempty"`
}

type CodeReviewDescriptionPolicy struct {
	Requirements []CodeReviewDescriptionRequirement `json:"requirements"`
}

type CodeReviewRiskPolicy struct {
	MaxFilesChanged       int      `json:"max_files_changed"`
	MaxLinesChanged       int      `json:"max_lines_changed"`
	RequirePassingChecks  bool     `json:"require_passing_checks"`
	ExcludeSensitivePaths bool     `json:"exclude_sensitive_paths"`
	SensitivePaths        []string `json:"sensitive_paths,omitempty"`
	ExcludeCategories     []string `json:"exclude_categories,omitempty"`
	RequireMergeable      bool     `json:"require_mergeable"`
	RequireUpToDate       bool     `json:"require_up_to_date"`
	AllowForks            bool     `json:"allow_forks"`
	AllowPolicyChanges    bool     `json:"allow_policy_changes"`
	EligibleAuthors       []string `json:"eligible_authors,omitempty"`
	RequiredChecks        []string `json:"required_checks,omitempty"`
}

type CodeReviewAgentRoster struct {
	Reviewers             []AgentType `json:"reviewers"`
	Orchestrator          AgentType   `json:"orchestrator"`
	ReviewDepth           string      `json:"review_depth"`
	DisagreementBlocks    bool        `json:"disagreement_blocks"`
	RequireReviewerQuorum int         `json:"require_reviewer_quorum"`
	TimeoutSeconds        int         `json:"timeout_seconds"`
	MaxCostCents          int         `json:"max_cost_cents"`
}

type CodeReviewPolicyConfig struct {
	Enabled             bool                        `json:"enabled"`
	ApprovalMode        CodeReviewApprovalMode      `json:"approval_mode"`
	DescriptionPolicy   CodeReviewDescriptionPolicy `json:"description_policy"`
	RiskPolicy          CodeReviewRiskPolicy        `json:"risk_policy"`
	AgentRoster         CodeReviewAgentRoster       `json:"agent_roster"`
	InlineCommentLimit  int                         `json:"inline_comment_limit"`
	FinalReviewTemplate string                      `json:"final_review_template,omitempty"`
}

func DefaultCodeReviewPolicyConfig() CodeReviewPolicyConfig {
	return CodeReviewPolicyConfig{
		Enabled:      true,
		ApprovalMode: CodeReviewApprovalModeCommentOnly,
		DescriptionPolicy: CodeReviewDescriptionPolicy{Requirements: []CodeReviewDescriptionRequirement{
			{Key: "description", Title: "Understandable description", Required: true, Prompt: "Explain what is changing and why clearly enough for a reviewer to understand the intent."},
			{Key: "testing", Title: "Testing evidence", Required: true, Applicability: "nontrivial", Prompt: "Describe the testing or validation evidence for nontrivial changes."},
			{Key: "ui_evidence", Title: "Screenshots or preview link", Required: true, Applicability: "frontend_or_ui_visible", Prompt: "Include screenshots or a preview link for frontend or UI-visible changes."},
		}},
		RiskPolicy: CodeReviewRiskPolicy{
			MaxFilesChanged:       5,
			MaxLinesChanged:       300,
			RequirePassingChecks:  true,
			ExcludeSensitivePaths: true,
			SensitivePaths:        defaultPRReadinessSensitivePaths(),
			ExcludeCategories:     []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"},
			RequireMergeable:      true,
			RequireUpToDate:       false,
			AllowForks:            false,
			AllowPolicyChanges:    false,
		},
		AgentRoster: CodeReviewAgentRoster{
			Reviewers:             []AgentType{AgentTypeCodex, AgentTypeClaudeCode},
			Orchestrator:          AgentTypeClaudeCode,
			ReviewDepth:           "standard",
			DisagreementBlocks:    true,
			RequireReviewerQuorum: 2,
			TimeoutSeconds:        1800,
			MaxCostCents:          500,
		},
		InlineCommentLimit: 4,
	}
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
	if len(config.RiskPolicy.ExcludeCategories) > 0 {
		defaults.RiskPolicy.ExcludeCategories = config.RiskPolicy.ExcludeCategories
	}
	defaults.RiskPolicy.RequireMergeable = config.RiskPolicy.RequireMergeable
	defaults.RiskPolicy.RequireUpToDate = config.RiskPolicy.RequireUpToDate
	defaults.RiskPolicy.AllowForks = config.RiskPolicy.AllowForks
	defaults.RiskPolicy.AllowPolicyChanges = config.RiskPolicy.AllowPolicyChanges
	if len(config.RiskPolicy.EligibleAuthors) > 0 {
		defaults.RiskPolicy.EligibleAuthors = config.RiskPolicy.EligibleAuthors
	}
	if len(config.RiskPolicy.RequiredChecks) > 0 {
		defaults.RiskPolicy.RequiredChecks = config.RiskPolicy.RequiredChecks
	}
	if len(config.AgentRoster.Reviewers) > 0 {
		defaults.AgentRoster = config.AgentRoster
	}
	if config.InlineCommentLimit != 0 {
		defaults.InlineCommentLimit = config.InlineCommentLimit
	}
	if config.FinalReviewTemplate != "" {
		defaults.FinalReviewTemplate = config.FinalReviewTemplate
	}
	return defaults
}

func (c CodeReviewPolicyConfig) Validate() error {
	if err := c.ApprovalMode.Validate(); err != nil {
		return err
	}
	if c.InlineCommentLimit < 1 || c.InlineCommentLimit > 10 {
		return fmt.Errorf("inline_comment_limit must be between 1 and 10")
	}
	if c.RiskPolicy.MaxFilesChanged < 1 {
		return fmt.Errorf("max_files_changed must be positive")
	}
	if c.RiskPolicy.MaxLinesChanged < 1 {
		return fmt.Errorf("max_lines_changed must be positive")
	}
	if len(c.AgentRoster.Reviewers) == 0 {
		return fmt.Errorf("at least one reviewer agent is required")
	}
	for _, agentType := range c.AgentRoster.Reviewers {
		if err := agentType.Validate(); err != nil {
			return err
		}
		if !AgentSupportsNativeReview(agentType) {
			return fmt.Errorf("agent %q does not support native review", agentType)
		}
	}
	if err := c.AgentRoster.Orchestrator.Validate(); err != nil {
		return err
	}
	if !AgentSupportsNativeReview(c.AgentRoster.Orchestrator) {
		return fmt.Errorf("orchestrator %q does not support native review", c.AgentRoster.Orchestrator)
	}
	if c.AgentRoster.RequireReviewerQuorum < 1 || c.AgentRoster.RequireReviewerQuorum > len(c.AgentRoster.Reviewers) {
		return fmt.Errorf("require_reviewer_quorum must be between 1 and reviewer count")
	}
	if c.AgentRoster.TimeoutSeconds < 60 {
		return fmt.Errorf("timeout_seconds must be at least 60")
	}
	if c.AgentRoster.MaxCostCents < 0 {
		return fmt.Errorf("max_cost_cents must not be negative")
	}
	return nil
}

type CodeReviewPolicyRecord struct {
	ID                  uuid.UUID                   `db:"id" json:"id"`
	OrgID               uuid.UUID                   `db:"org_id" json:"org_id"`
	RepositoryID        *uuid.UUID                  `db:"repository_id" json:"repository_id,omitempty"`
	Active              bool                        `db:"active" json:"active"`
	Version             int                         `db:"version" json:"version"`
	Enabled             bool                        `db:"enabled" json:"enabled"`
	ApprovalMode        CodeReviewApprovalMode      `db:"approval_mode" json:"approval_mode"`
	DescriptionPolicy   CodeReviewDescriptionPolicy `db:"-" json:"description_policy"`
	RiskPolicy          CodeReviewRiskPolicy        `db:"-" json:"risk_policy"`
	AgentRoster         CodeReviewAgentRoster       `db:"-" json:"agent_roster"`
	InlineCommentLimit  int                         `db:"inline_comment_limit" json:"inline_comment_limit"`
	FinalReviewTemplate string                      `db:"final_review_template" json:"final_review_template,omitempty"`
	CreatedByUserID     *uuid.UUID                  `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt           time.Time                   `db:"created_at" json:"created_at"`
}

func (r CodeReviewPolicyRecord) Config() CodeReviewPolicyConfig {
	return CodeReviewPolicyConfig{
		ApprovalMode:        r.ApprovalMode,
		Enabled:             r.Enabled,
		DescriptionPolicy:   r.DescriptionPolicy,
		RiskPolicy:          r.RiskPolicy,
		AgentRoster:         r.AgentRoster,
		InlineCommentLimit:  r.InlineCommentLimit,
		FinalReviewTemplate: r.FinalReviewTemplate,
	}
}

type CodeReviewResolvedPolicy struct {
	Config CodeReviewPolicyConfig  `json:"config"`
	Source string                  `json:"source"`
	Policy *CodeReviewPolicyRecord `json:"policy,omitempty"`
}

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
	AgentResults []CodeReviewAgentResult `json:"agent_results"`
	Findings     []CodeReviewFinding     `json:"findings"`
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

func CodeReviewPolicyTemplates() []CodeReviewTemplateOption {
	base := DefaultCodeReviewPolicyConfig()
	return []CodeReviewTemplateOption{
		{Key: CodeReviewTemplateDocsOnly, Title: "Docs and comments only", Description: "Approve only documentation/comment-only changes with passing checks.", Config: templatePolicy(base, 8, 400, []string{"dependencies", "auth", "billing", "permissions", "crypto", "infra", "migrations"})},
		{Key: CodeReviewTemplateTestsOnly, Title: "Tests only", Description: "Approve isolated test and fixture changes with conservative churn limits.", Config: templatePolicy(base, 10, 500, []string{"dependencies", "auth", "billing", "permissions", "crypto", "infra", "migrations", "generated"})},
		{Key: CodeReviewTemplateSmallFrontend, Title: "Small frontend change", Description: "Approve small UI changes with screenshot or preview evidence.", Config: templatePolicy(base, 5, 250, []string{"auth", "billing", "permissions", "crypto", "infra", "dependencies"})},
		{Key: CodeReviewTemplateSmallBackend, Title: "Small backend change", Description: "Approve small backend changes outside sensitive packages with test evidence.", Config: templatePolicy(base, 4, 200, []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"})},
		{Key: CodeReviewTemplateSmallCombined, Title: "Small combined feature", Description: "Approve tightly scoped frontend/backend changes with evidence and passing checks.", Config: templatePolicy(base, 6, 250, []string{"migrations", "dependencies", "auth", "billing", "permissions", "crypto", "infra"})},
	}
}

func templatePolicy(base CodeReviewPolicyConfig, maxFiles, maxLines int, excluded []string) CodeReviewPolicyConfig {
	cfg := base
	cfg.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
	cfg.RiskPolicy.MaxFilesChanged = maxFiles
	cfg.RiskPolicy.MaxLinesChanged = maxLines
	cfg.RiskPolicy.ExcludeCategories = excluded
	return cfg
}

type CodeReviewRiskInput struct {
	FilesChanged           int
	LinesChanged           int
	ChangedPaths           []string
	Categories             []string
	ChecksPassing          bool
	RequiredChecksPassing  map[string]bool
	DescriptionPassed      bool
	Mergeable              bool
	UpToDate               bool
	Author                 string
	FromFork               bool
	UnresolvedHumanThreads int
	BlockingFindings       int
	ReviewerDisagreement   bool
	ContextFetchFailed     bool
	HeadSHAChanged         bool
}

type CodeReviewRiskEvaluation struct {
	Acceptable bool     `json:"acceptable"`
	Reasons    []string `json:"reasons"`
}

type CodeReviewDecisionEvaluation struct {
	Decision    CodeReviewDecision `json:"decision"`
	Acceptable  bool               `json:"acceptable"`
	RiskReasons []string           `json:"risk_reasons,omitempty"`
}

func EvaluateCodeReviewDecision(policy CodeReviewPolicyConfig, risk CodeReviewRiskEvaluation) CodeReviewDecisionEvaluation {
	if !risk.Acceptable {
		return CodeReviewDecisionEvaluation{
			Decision:    CodeReviewDecisionNeedsHumanReview,
			Acceptable:  false,
			RiskReasons: append([]string(nil), risk.Reasons...),
		}
	}
	if policy.ApprovalMode == CodeReviewApprovalModeApproveAcceptable {
		return CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionApproved, Acceptable: true}
	}
	return CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionCommentOnly, Acceptable: true}
}

func EvaluateCodeReviewRisk(policy CodeReviewPolicyConfig, input CodeReviewRiskInput) CodeReviewRiskEvaluation {
	policy = ResolveCodeReviewPolicyConfig(&policy)
	reasons := make([]string, 0)
	if !policy.Enabled {
		reasons = append(reasons, "code reviewer is disabled by policy")
	}
	if input.ContextFetchFailed {
		reasons = append(reasons, "required PR context could not be fetched")
	}
	if input.HeadSHAChanged {
		reasons = append(reasons, "PR head changed after review started")
	}
	if input.FilesChanged > policy.RiskPolicy.MaxFilesChanged {
		reasons = append(reasons, fmt.Sprintf("changed files %d exceeds policy limit %d", input.FilesChanged, policy.RiskPolicy.MaxFilesChanged))
	}
	if input.LinesChanged > policy.RiskPolicy.MaxLinesChanged {
		reasons = append(reasons, fmt.Sprintf("changed lines %d exceeds policy limit %d", input.LinesChanged, policy.RiskPolicy.MaxLinesChanged))
	}
	if policy.RiskPolicy.RequirePassingChecks && !input.ChecksPassing {
		reasons = append(reasons, "required GitHub checks are not passing")
	}
	for _, check := range policy.RiskPolicy.RequiredChecks {
		if !input.RequiredChecksPassing[check] {
			reasons = append(reasons, "required check is not passing: "+check)
		}
	}
	if !input.DescriptionPassed {
		reasons = append(reasons, "PR description policy did not pass")
	}
	if policy.RiskPolicy.RequireMergeable && !input.Mergeable {
		reasons = append(reasons, "PR is not mergeable")
	}
	if policy.RiskPolicy.RequireUpToDate && !input.UpToDate {
		reasons = append(reasons, "PR branch is not up to date")
	}
	if input.FromFork && !policy.RiskPolicy.AllowForks {
		reasons = append(reasons, "fork PRs are not eligible for approval")
	}
	if len(policy.RiskPolicy.EligibleAuthors) > 0 && !stringInSlice(input.Author, policy.RiskPolicy.EligibleAuthors) {
		reasons = append(reasons, "PR author is not eligible for automated approval")
	}
	if input.UnresolvedHumanThreads > 0 {
		reasons = append(reasons, "unresolved human review threads are present")
	}
	if input.BlockingFindings > 0 {
		reasons = append(reasons, "review agents reported blocking findings")
	}
	if input.ReviewerDisagreement && policy.AgentRoster.DisagreementBlocks {
		reasons = append(reasons, "reviewer agents disagreed on material risk")
	}
	if policy.RiskPolicy.ExcludeSensitivePaths {
		for _, path := range input.ChangedPaths {
			if matchesAnyCodeReviewPath(path, policy.RiskPolicy.SensitivePaths) {
				reasons = append(reasons, "sensitive path changed: "+path)
			}
			if !policy.RiskPolicy.AllowPolicyChanges && isCodeReviewPolicyPath(path) {
				reasons = append(reasons, "code review policy/config path changed: "+path)
			}
		}
	}
	for _, category := range input.Categories {
		if stringInSlice(category, policy.RiskPolicy.ExcludeCategories) {
			reasons = append(reasons, "excluded risk category changed: "+category)
		}
	}
	if len(reasons) == 0 {
		return CodeReviewRiskEvaluation{Acceptable: true}
	}
	return CodeReviewRiskEvaluation{Acceptable: false, Reasons: reasons}
}

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(needle)) {
			return true
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
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		normalizedPattern := strings.TrimSuffix(pattern, "/**")
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
		if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") && strings.Contains(path, strings.Trim(pattern, "*")) {
			return true
		}
		if normalizedPattern == path || strings.HasPrefix(path, normalizedPattern+"/") || strings.HasPrefix(path, pattern) {
			return true
		}
	}
	return false
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
