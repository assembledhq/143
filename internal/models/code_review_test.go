package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "approval mode comment only", validate: CodeReviewApprovalModeCommentOnly.Validate},
		{name: "approval mode invalid", validate: CodeReviewApprovalMode("bogus").Validate, expectErr: true},
		{name: "session status queued", validate: CodeReviewSessionStatusQueued.Validate},
		{name: "session status invalid", validate: CodeReviewSessionStatus("bogus").Validate, expectErr: true},
		{name: "decision approved", validate: CodeReviewDecisionApproved.Validate},
		{name: "decision invalid", validate: CodeReviewDecision("bogus").Validate, expectErr: true},
		{name: "trigger source app reviewer", validate: CodeReviewTriggerSourceAppReviewer.Validate},
		{name: "trigger source invalid", validate: CodeReviewTriggerSource("bogus").Validate, expectErr: true},
		{name: "agent role reviewer", validate: CodeReviewAgentRoleReviewer.Validate},
		{name: "agent role invalid", validate: CodeReviewAgentRole("bogus").Validate, expectErr: true},
		{name: "agent result timed out", validate: CodeReviewAgentResultStatusTimedOut.Validate},
		{name: "agent result invalid", validate: CodeReviewAgentResultStatus("bogus").Validate, expectErr: true},
		{name: "finding severity high", validate: CodeReviewFindingSeverityHigh.Validate},
		{name: "finding severity invalid", validate: CodeReviewFindingSeverity("bogus").Validate, expectErr: true},
		{name: "finding confidence high", validate: CodeReviewFindingConfidenceHigh.Validate},
		{name: "finding confidence invalid", validate: CodeReviewFindingConfidence("bogus").Validate, expectErr: true},
		{name: "description applicability nontrivial", validate: CodeReviewDescriptionApplicabilityNontrivial.Validate},
		{name: "description applicability invalid", validate: CodeReviewDescriptionApplicabilityKind("bogus").Validate, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid code review enum values should be rejected")
				return
			}
			require.NoError(t, err, "valid code review enum values should be accepted")
		})
	}
}

func TestDefaultCodeReviewPolicyConfig(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()

	require.Equal(t, CodeReviewApprovalModeCommentOnly, config.ApprovalMode, "code reviewer should default to comment-only mode")
	require.True(t, config.Enabled, "code reviewer should default enabled so explicit reviewer requests are honored")
	require.Equal(t, 4, config.InlineCommentLimit, "default inline comment limit should match product design")
	require.Equal(t, 5, config.RiskPolicy.MaxFilesChanged, "default acceptable-risk file threshold should be conservative")
	require.Equal(t, 300, config.RiskPolicy.MaxLinesChanged, "default acceptable-risk line threshold should be conservative")
	require.Equal(t, []AgentType{AgentTypeCodex, AgentTypeClaudeCode}, config.AgentRoster.Reviewers, "default roster should run two reviewers")
	require.Equal(t, []string{DefaultCodexModel, DefaultClaudeCodeModel}, config.AgentRoster.ReviewerModels, "default roster should pin reviewer models")
	require.Equal(t, OpenCodeModelGPT55, *config.AgentRoster.OrchestratorModel, "default roster should pin the orchestrator model")
	require.NoError(t, config.Validate(), "default code review policy should be valid")
}

func TestCodeReviewPolicyConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*CodeReviewPolicyConfig)
		expectErr bool
	}{
		{name: "valid default"},
		{name: "rejects zero inline comments", mutate: func(c *CodeReviewPolicyConfig) { c.InlineCommentLimit = 0 }, expectErr: true},
		{name: "rejects too many inline comments", mutate: func(c *CodeReviewPolicyConfig) { c.InlineCommentLimit = 11 }, expectErr: true},
		{name: "rejects no reviewers", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.Reviewers = nil }, expectErr: true},
		{name: "rejects unsupported reviewer", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.Reviewers = []AgentType{AgentTypePMAgent} }, expectErr: true},
		{name: "rejects reviewer model count mismatch", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.ReviewerModels = []string{DefaultCodexModel} }, expectErr: true},
		{name: "rejects invalid reviewer model", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerModels = []string{DefaultCodexModel, DefaultCodexModel}
		}, expectErr: true},
		{name: "rejects invalid orchestrator model", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.OrchestratorModel = strPtr(DefaultCodexModel) }, expectErr: true},
		{name: "rejects oversized quorum", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.RequireReviewerQuorum = 3 }, expectErr: true},
		{name: "rejects too short timeout", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.TimeoutSeconds = 30 }, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := DefaultCodeReviewPolicyConfig()
			if tt.mutate != nil {
				tt.mutate(&config)
			}
			err := config.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid code review policy should be rejected")
				return
			}
			require.NoError(t, err, "valid code review policy should be accepted")
		})
	}
}

func TestMergeCodeReviewPolicyConfigInheritsFieldByField(t *testing.T) {
	t.Parallel()

	base := DefaultCodeReviewPolicyConfig()
	base.Enabled = true
	base.ApprovalMode = CodeReviewApprovalModeCommentOnly
	base.RiskPolicy.MaxFilesChanged = 9
	base.InlineCommentLimit = 4
	override := base
	override.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
	override.RiskPolicy.MaxFilesChanged = 2
	override.InlineCommentLimit = 8
	override.Inheritance = CodeReviewPolicyInheritance{
		InheritOrgDefaults: true,
		OverrideFields:     []string{CodeReviewPolicyFieldApprovalMode, CodeReviewPolicyFieldRiskPolicy},
	}

	merged := MergeCodeReviewPolicyConfig(base, override)

	require.True(t, merged.Enabled, "merged policy should inherit fields outside the repository override list")
	require.Equal(t, CodeReviewApprovalModeApproveAcceptable, merged.ApprovalMode, "merged policy should apply explicitly overridden approval mode")
	require.Equal(t, 2, merged.RiskPolicy.MaxFilesChanged, "merged policy should apply explicitly overridden risk policy")
	require.Equal(t, 4, merged.InlineCommentLimit, "merged policy should inherit non-overridden inline comment limit")
	require.Equal(t, override.Inheritance, merged.Inheritance, "merged policy should preserve inheritance audit metadata")
	require.Equal(t, []string{CodeReviewPolicyFieldApprovalMode, CodeReviewPolicyFieldRiskPolicy, CodeReviewPolicyFieldInlineCommentLimit}, CodeReviewPolicyOverrideFields(base, override), "override field detection should report changed policy sections")
}

func TestCodeReviewPolicyTemplates(t *testing.T) {
	t.Parallel()

	templates := CodeReviewPolicyTemplates()

	require.Len(t, templates, 5, "starter templates should cover the product design templates")
	for _, template := range templates {
		t.Run(string(template.Key), func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, template.Title, "template should have a display title")
			require.Equal(t, CodeReviewApprovalModeApproveAcceptable, template.Config.ApprovalMode, "starter templates should be editable approval policies")
			require.NoError(t, template.Config.Validate(), "template config should be valid")
		})
	}
}

func TestEvaluateCodeReviewRisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*CodeReviewPolicyConfig)
		input    CodeReviewRiskInput
		expected CodeReviewRiskEvaluation
	}{
		{
			name: "acceptable when every prerequisite passes",
			input: CodeReviewRiskInput{
				FilesChanged:      2,
				LinesChanged:      100,
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				UpToDate:          true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: true},
		},
		{
			name: "blocks oversized sensitive fork with agent concerns",
			input: CodeReviewRiskInput{
				FilesChanged:           6,
				LinesChanged:           350,
				ChangedPaths:           []string{"internal/auth/session.go"},
				Categories:             []string{"auth"},
				ChecksPassing:          false,
				DescriptionPassed:      false,
				Mergeable:              false,
				FromFork:               true,
				UnresolvedHumanThreads: 1,
				BlockingFindings:       1,
				ReviewerDisagreement:   true,
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"changed files 6 exceeds policy limit 5",
				"changed lines 350 exceeds policy limit 300",
				"required GitHub checks are not passing",
				"PR description policy did not pass",
				"PR is not mergeable",
				"fork PRs are not eligible for approval",
				"unresolved human review threads are present",
				"review agents reported blocking findings",
				"reviewer agents disagreed on material risk",
				"sensitive path changed: internal/auth/session.go",
				"excluded risk category changed: auth",
			}},
		},
		{
			name: "blocks missing required named check and ineligible author",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.RequiredChecks = []string{"ci/test"}
				c.RiskPolicy.EligibleAuthors = []string{"anya"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:          1,
				LinesChanged:          20,
				ChecksPassing:         true,
				RequiredChecksPassing: map[string]bool{"ci/lint": true},
				DescriptionPassed:     true,
				Mergeable:             true,
				Author:                "sam",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"required check is not passing: ci/test",
				"PR author is not eligible for automated approval",
			}},
		},
		{
			name: "allows configured author classes",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.EligibleAuthors = []string{"human"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "sam",
				AuthorClass:       "human",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: true},
		},
		{
			name: "blocks policy changes independently from sensitive path exclusions",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.ExcludeSensitivePaths = false
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChangedPaths:      []string{"internal/models/code_review.go"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"code review policy/config path changed: internal/models/code_review.go",
			}},
		},
		{
			name: "blocks synthesized reviewer risk signals",
			input: CodeReviewRiskInput{
				FilesChanged:          1,
				LinesChanged:          20,
				ChecksPassing:         true,
				DescriptionPassed:     true,
				Mergeable:             true,
				Author:                "devin",
				ScopeMismatch:         true,
				UnresolvedUncertainty: true,
				PromptInjectionFound:  true,
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"orchestrator reported the change may not match the stated intent",
				"orchestrator reported unresolved uncertainty",
				"possible prompt-injection attempt found in PR content",
			}},
		},
		{
			name: "blocks paths outside allowed scope",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.AllowedPathPatterns = []string{"docs/**", "**/*.md"}
				c.RiskPolicy.ExcludeCategories = nil
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChangedPaths:      []string{"internal/api/router.go"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"path is outside allowed policy scope: internal/api/router.go",
			}},
		},
		{
			name: "blocks explicit blocked path patterns",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.BlockedPathPatterns = []string{"**/schema/**"}
				c.RiskPolicy.ExcludeCategories = nil
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChangedPaths:      []string{"internal/db/schema/users.go"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"blocked path changed: internal/db/schema/users.go",
			}},
		},
		{
			name: "low-risk docs lane raises the churn ceiling",
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      607,
				ChangedPaths:      []string{"docs/design/future/111-session-changesets-and-stacks.md"},
				Categories:        []string{"docs"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: true},
		},
		{
			name: "low-risk docs lane still enforces its own ceiling",
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      1200,
				ChangedPaths:      []string{"docs/huge.md"},
				Categories:        []string{"docs"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"changed lines 1200 exceeds policy limit 1000",
			}},
		},
		{
			name: "low-risk lane does not apply to mixed docs and code changes",
			input: CodeReviewRiskInput{
				FilesChanged:      2,
				LinesChanged:      607,
				ChangedPaths:      []string{"docs/x.md", "internal/api/router.go"},
				Categories:        []string{"docs", "backend"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Mergeable:         true,
				Author:            "devin",
			},
			expected: CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{
				"changed lines 607 exceeds policy limit 300",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := DefaultCodeReviewPolicyConfig()
			if tt.mutate != nil {
				tt.mutate(&config)
			}

			actual := EvaluateCodeReviewRisk(config, tt.input)

			require.Equal(t, tt.expected, actual, "risk evaluator should enforce deterministic approval prerequisites")
		})
	}
}

func TestEvaluateCodeReviewDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   CodeReviewPolicyConfig
		risk     CodeReviewRiskEvaluation
		expected CodeReviewDecisionEvaluation
	}{
		{
			name: "approves acceptable risk when policy allows approval",
			policy: func() CodeReviewPolicyConfig {
				c := DefaultCodeReviewPolicyConfig()
				c.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
				return c
			}(),
			risk: CodeReviewRiskEvaluation{Acceptable: true},
			expected: CodeReviewDecisionEvaluation{
				Decision:   CodeReviewDecisionApproved,
				Acceptable: true,
			},
		},
		{
			name:   "comments on acceptable risk when policy is comment only",
			policy: DefaultCodeReviewPolicyConfig(),
			risk:   CodeReviewRiskEvaluation{Acceptable: true},
			expected: CodeReviewDecisionEvaluation{
				Decision:   CodeReviewDecisionCommentOnly,
				Acceptable: true,
			},
		},
		{
			name:   "requires human review when risk is not acceptable",
			policy: DefaultCodeReviewPolicyConfig(),
			risk:   CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{"required GitHub checks are not passing"}},
			expected: CodeReviewDecisionEvaluation{
				Decision:    CodeReviewDecisionNeedsHumanReview,
				Acceptable:  false,
				RiskReasons: []string{"required GitHub checks are not passing"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := EvaluateCodeReviewDecision(tt.policy, tt.risk)

			require.Equal(t, tt.expected, actual, "decision evaluator should map policy and risk to final review decision")
		})
	}
}
