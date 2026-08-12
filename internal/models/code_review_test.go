package models

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
		{name: "phase waiting for GitHub", validate: CodeReviewPhaseWaitingGitHub.Validate},
		{name: "phase invalid", validate: CodeReviewPhase("bogus").Validate, expectErr: true},
		{name: "status code GitHub rate limited", validate: CodeReviewStatusCodeGitHubRateLimited.Validate},
		{name: "status code invalid", validate: CodeReviewStatusCode("bogus").Validate, expectErr: true},
		{name: "decision approved", validate: CodeReviewDecisionApproved.Validate},
		{name: "decision invalid", validate: CodeReviewDecision("bogus").Validate, expectErr: true},
		{name: "list outcome automatically approved", validate: CodeReviewListOutcomeAutomaticallyApproved.Validate},
		{name: "list outcome completed not approved", validate: CodeReviewListOutcomeCompletedNotApproved.Validate},
		{name: "list outcome invalid", validate: CodeReviewListOutcome("bogus").Validate, expectErr: true},
		{name: "approval round one", validate: CodeReviewApprovalRound1.Validate},
		{name: "approval round two", validate: CodeReviewApprovalRound2.Validate},
		{name: "approval round three", validate: CodeReviewApprovalRound3.Validate},
		{name: "approval round four plus", validate: CodeReviewApprovalRound4Plus.Validate},
		{name: "approval round not yet", validate: CodeReviewApprovalRoundNotYet.Validate},
		{name: "approval round invalid", validate: CodeReviewApprovalRoundBucket("bogus").Validate, expectErr: true},
		{name: "activity status current", validate: CodeReviewActivityStatusCurrent.Validate},
		{name: "activity status completed", validate: CodeReviewActivityStatusCompleted.Validate},
		{name: "activity status in progress", validate: CodeReviewActivityStatusInProgress.Validate},
		{name: "activity status failed", validate: CodeReviewActivityStatusFailed.Validate},
		{name: "activity status superseded", validate: CodeReviewActivityStatusSuperseded.Validate},
		{name: "activity status all", validate: CodeReviewActivityStatusAll.Validate},
		{name: "activity status invalid", validate: CodeReviewActivityStatus("bogus").Validate, expectErr: true},
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
		{name: "visual evidence surface description", validate: CodeReviewEvidenceSurfaceDescription.Validate},
		{name: "visual evidence surface invalid", validate: CodeReviewEvidenceSurface("bogus").Validate, expectErr: true},
		{name: "visual evidence author user", validate: CodeReviewEvidenceAuthorTypeUser.Validate},
		{name: "visual evidence author invalid", validate: CodeReviewEvidenceAuthorType("bogus").Validate, expectErr: true},
		{name: "visual evidence fetch available", validate: CodeReviewVisualEvidenceFetchStatusAvailable.Validate},
		{name: "visual evidence fetch invalid", validate: CodeReviewVisualEvidenceFetchStatus("bogus").Validate, expectErr: true},
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

func TestCodeReviewEvidenceAuthorTypeIsHuman(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authorType CodeReviewEvidenceAuthorType
		expected   bool
	}{
		{name: "GitHub user", authorType: CodeReviewEvidenceAuthorTypeUser, expected: true},
		{name: "GitHub mannequin", authorType: CodeReviewEvidenceAuthorTypeMannequin, expected: true},
		{name: "GitHub bot", authorType: CodeReviewEvidenceAuthorTypeBot, expected: false},
		{name: "GitHub app", authorType: CodeReviewEvidenceAuthorTypeApp, expected: false},
		{name: "GitHub organization", authorType: CodeReviewEvidenceAuthorTypeOrganization, expected: false},
		{name: "deleted or unknown actor", authorType: CodeReviewEvidenceAuthorTypeUnknown, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, tt.authorType.IsHuman(), "author classification should admit only human GitHub actor types")
		})
	}
}

func TestCodeReviewVisualEvidenceSnapshotCanonicalHash(t *testing.T) {
	t.Parallel()

	repositoryID := uuid.New()
	base := CodeReviewVisualEvidenceSnapshot{
		Version: 1, RepositoryID: repositoryID, Repository: "acme/web", PullRequestNumber: 42,
		HeadSHA: strings.Repeat("a", 40), CapturedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC), Complete: true,
		Evidence: []CodeReviewVisualEvidence{{
			EvidenceID: "ve_1", Source: CodeReviewVisualEvidenceSource{
				SourceID: "ves_1", Surface: CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "99", ImageIndex: 1,
				ImageURL: "https://github.com/user-attachments/assets/original", AltText: "untrusted caption", Untrusted: true,
			},
			OriginalURL: "https://github.com/user-attachments/assets/original", StorageKey: "org/code-review-evidence/session/hash.png",
			StoredURL: "/api/v1/uploads/files/old", ContentSHA256: strings.Repeat("b", 64), ContentType: "image/png",
			ByteSize: 100, Width: 10, Height: 10, Status: CodeReviewVisualEvidenceFetchStatusAvailable,
		}},
	}
	mutableCopy := base
	mutableCopy.CapturedAt = base.CapturedAt.Add(time.Hour)
	mutableCopy.Evidence = append([]CodeReviewVisualEvidence(nil), base.Evidence...)
	mutableCopy.Evidence[0].StoredURL = "/api/v1/uploads/files/new"
	contentCopy := mutableCopy
	contentCopy.Evidence = append([]CodeReviewVisualEvidence(nil), mutableCopy.Evidence...)
	contentCopy.Evidence[0].ContentSHA256 = strings.Repeat("c", 64)
	untrustedCopy := mutableCopy
	untrustedCopy.Evidence = append([]CodeReviewVisualEvidence(nil), mutableCopy.Evidence...)
	untrustedCopy.Evidence[0].Source.AltText = "edited captured caption"

	require.NotEmpty(t, base.CanonicalHash(), "canonical visual-evidence hash should be available for a valid snapshot")
	require.Equal(t, base.CanonicalHash(), mutableCopy.CanonicalHash(), "canonical visual-evidence hash should exclude mutable first-party URLs and capture timing")
	require.NotEqual(t, base.CanonicalHash(), contentCopy.CanonicalHash(), "canonical visual-evidence hash should change when captured image content changes")
	require.NotEqual(t, base.CanonicalHash(), untrustedCopy.CanonicalHash(), "canonical visual-evidence hash should change when captured untrusted context changes")
}

func TestCodeReviewDescriptionEvidenceBasisValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		basis     CodeReviewDescriptionEvidenceBasis
		expectErr bool
	}{
		{name: "image", basis: CodeReviewDescriptionEvidenceBasisImage},
		{name: "preview link", basis: CodeReviewDescriptionEvidenceBasisPreviewLink},
		{name: "repository", basis: CodeReviewDescriptionEvidenceBasisRepository},
		{name: "pull request description", basis: CodeReviewDescriptionEvidenceBasisPullRequestDescription},
		{name: "diff", basis: CodeReviewDescriptionEvidenceBasisDiff},
		{name: "not applicable", basis: CodeReviewDescriptionEvidenceBasisNotApplicable},
		{name: "missing", basis: CodeReviewDescriptionEvidenceBasisMissing},
		{name: "empty", basis: "", expectErr: true},
		{name: "unknown", basis: "other", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.basis.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid description evidence basis should be rejected")
				return
			}
			require.NoError(t, err, "supported description evidence basis should validate")
		})
	}
}

func TestCodeReviewFindingSeverityIsBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		severity CodeReviewFindingSeverity
		expected bool
	}{
		{name: "critical is P0 blocking", severity: CodeReviewFindingSeverityCritical, expected: true},
		{name: "high is P1 blocking", severity: CodeReviewFindingSeverityHigh, expected: true},
		{name: "medium is P2 non-blocking", severity: CodeReviewFindingSeverityMedium, expected: false},
		{name: "low is P3 non-blocking", severity: CodeReviewFindingSeverityLow, expected: false},
		{name: "info is non-blocking", severity: CodeReviewFindingSeverityInfo, expected: false},
		{name: "unknown is non-blocking", severity: CodeReviewFindingSeverity("bogus"), expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, tt.severity.IsBlocking(), "severity should match the P0 and P1 blocking threshold")
		})
	}
}

func TestCodeReviewHumanReviewReasonCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		code             CodeReviewHumanReviewReasonCode
		expectedRiskCode CodeReviewRiskReasonCode
		expectErr        bool
	}{
		{name: "architecture", code: CodeReviewHumanReviewReasonArchitecture, expectedRiskCode: CodeReviewRiskReasonArchitecture},
		{name: "ownership", code: CodeReviewHumanReviewReasonOwnership, expectedRiskCode: CodeReviewRiskReasonOwnership},
		{name: "operational risk", code: CodeReviewHumanReviewReasonOperationalRisk, expectedRiskCode: CodeReviewRiskReasonOperationalRisk},
		{name: "sensitive change", code: CodeReviewHumanReviewReasonSensitiveChange, expectedRiskCode: CodeReviewRiskReasonSensitiveChange},
		{name: "policy requirement", code: CodeReviewHumanReviewReasonPolicyRequirement, expectedRiskCode: CodeReviewRiskReasonPolicyRequirement},
		{name: "invalid", code: CodeReviewHumanReviewReasonCode("unknown"), expectedRiskCode: CodeReviewRiskReasonOrchestratorSynthesisInvalid, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.code.Validate()
			if tt.expectErr {
				require.Error(t, err, "unknown human-review reason codes should be rejected")
			} else {
				require.NoError(t, err, "known human-review reason codes should be accepted")
			}
			require.Equal(t, tt.expectedRiskCode, tt.code.RiskReasonCode(), "human-review reasons should map to a typed backend risk reason")
		})
	}
}

func TestCodeReviewPolicyPromptValidationIdentifiesField(t *testing.T) {
	t.Parallel()
	config := DefaultCodeReviewPolicyConfig()
	config.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
	config.AutomatedApprovalPolicy = ""

	err := config.ValidatePromptFields()

	var validationErr *CodeReviewPolicyValidationError
	require.ErrorAs(t, err, &validationErr, "prompt validation should return a typed field error")
	require.Equal(t, "automated_approval_policy", validationErr.Field, "prompt validation should identify the failing field")
}

func TestDefaultCodeReviewPolicyConfig(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()
	require.Empty(t, config.ReviewInstructions, "default review instructions should preserve native review behavior")
	require.Equal(t, DefaultCodeReviewAutomatedApprovalPolicy, config.AutomatedApprovalPolicy, "default approval policy should be conservative")
	require.Contains(t, config.AutomatedApprovalPolicy, "Disregard GitHub checks, CI results, build statuses", "default approval policy should base approval on code rather than external check status")
	require.Contains(t, config.AutomatedApprovalPolicy, "Unresolved human review threads must not count against approval.", "default approval policy should require an independent decision")

	require.Equal(t, CodeReviewApprovalModeCommentOnly, config.ApprovalMode, "code reviewer should default to comment-only mode")
	require.True(t, config.Enabled, "code reviewer should default enabled so explicit reviewer requests are honored")
	require.Equal(t, 4, config.InlineCommentLimit, "default inline comment limit should match product design")
	require.Equal(t, 5, config.RiskPolicy.MaxFilesChanged, "default acceptable-risk file threshold should be conservative")
	require.Equal(t, 300, config.RiskPolicy.MaxLinesChanged, "default acceptable-risk line threshold should be conservative")
	require.False(t, config.RiskPolicy.RequirePassingChecks, "default approval policy should evaluate code without requiring GitHub checks")
	require.False(t, config.RiskPolicy.ExcludeSensitivePaths, "default approval policy should not block on unconfigured sensitive paths")
	require.Empty(t, config.RiskPolicy.SensitivePaths, "default approval policy should not assume repository-specific sensitive paths")
	require.Equal(t, []AgentType{AgentTypeCodex, AgentTypeClaudeCode}, config.AgentRoster.Reviewers, "default roster should run two reviewers")
	require.Equal(t, []string{DefaultCodexModel, DefaultClaudeCodeModel}, config.AgentRoster.ReviewerModels, "default roster should pin reviewer models")
	require.Equal(t, []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortHigh}, config.AgentRoster.ReviewerReasoningEfforts, "each default reviewer should use high reasoning")
	require.Equal(t, OpenCodeModelGPT55, *config.AgentRoster.OrchestratorModel, "default roster should pin the orchestrator model")
	require.Equal(t, ReasoningEffortHigh, config.AgentRoster.ReasoningEffort, "code review orchestrator should default to high reasoning")
	require.NoError(t, config.Validate(), "default code review policy should be valid")
}

func TestResolveCodeReviewPolicyConfigDefaultsLegacyRosterReasoning(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()
	config.AgentRoster.ReviewerReasoningEfforts = nil
	config.AgentRoster.ReasoningEffort = ""

	resolved := ResolveCodeReviewPolicyConfig(&config)

	require.Equal(t, ReasoningEffortHigh, resolved.AgentRoster.ReasoningEffort, "legacy code review policies should inherit high reasoning")
	require.Equal(t, []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortHigh}, resolved.AgentRoster.ReviewerReasoningEfforts, "legacy reviewers should inherit the roster reasoning level")
}

func TestCodeReviewPolicyRecordConfigDefaultsLegacyRosterReasoning(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()
	config.AgentRoster.ReviewerReasoningEfforts = nil
	config.AgentRoster.ReasoningEffort = ""
	record := CodeReviewPolicyRecord{
		Enabled:                 config.Enabled,
		ApprovalMode:            config.ApprovalMode,
		ReviewInstructions:      config.ReviewInstructions,
		AutomatedApprovalPolicy: config.AutomatedApprovalPolicy,
		DescriptionPolicy:       config.DescriptionPolicy,
		RiskPolicy:              config.RiskPolicy,
		AgentRoster:             config.AgentRoster,
		InlineCommentLimit:      config.InlineCommentLimit,
	}

	resolved := record.Config()

	require.Equal(t, ReasoningEffortHigh, resolved.AgentRoster.ReasoningEffort, "stored legacy code review policies should run with high reasoning")
	require.Equal(t, []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortHigh}, resolved.AgentRoster.ReviewerReasoningEfforts, "stored legacy reviewers should inherit high reasoning")
}

func TestResolveCodeReviewPolicyConfigPreservesDeterministicEarlyStop(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()
	config.RiskPolicy.StopAfterDeterministicFailure = true

	resolved := ResolveCodeReviewPolicyConfig(&config)

	require.True(t, resolved.RiskPolicy.StopAfterDeterministicFailure, "resolved policy should preserve the explicit deterministic early-stop setting")
}

func TestResolveCodeReviewPolicyConfigDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	config := DefaultCodeReviewPolicyConfig()
	config.DescriptionPolicy.Requirements[1].AppliesWhen = CodeReviewDescriptionApplicability{}

	resolved := ResolveCodeReviewPolicyConfig(&config)

	require.True(t, config.DescriptionPolicy.Requirements[1].AppliesWhen.Empty(), "resolving legacy applicability should leave the input policy unchanged")
	require.Equal(t, CodeReviewDescriptionApplicabilityNontrivial, resolved.DescriptionPolicy.Requirements[1].AppliesWhen.Kind, "resolving legacy applicability should populate the typed rule")
	resolved.DescriptionPolicy.Requirements[1].Title = "changed"
	require.Equal(t, "Testing evidence", config.DescriptionPolicy.Requirements[1].Title, "the resolved requirements should not share mutable slice storage with the input policy")
}

func TestResolveCodeReviewPolicyConfigRemovesLegacyFilenameClassifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		applicability CodeReviewDescriptionApplicability
		expected      CodeReviewDescriptionApplicability
	}{
		{
			name:          "frontend classifier becomes explicit paths",
			applicability: CodeReviewDescriptionApplicability{Kind: "frontend_or_ui_visible", PathPatterns: []string{"web/**"}},
			expected:      CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"web/**"}},
		},
		{
			name:          "category classifier becomes universal",
			applicability: CodeReviewDescriptionApplicability{Kind: "categories"},
			expected:      CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityAll},
		},
		{
			name:          "test classifier becomes universal",
			applicability: CodeReviewDescriptionApplicability{Kind: "tests_changed"},
			expected:      CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityAll},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := DefaultCodeReviewPolicyConfig()
			config.DescriptionPolicy.Requirements[0].AppliesWhen = tt.applicability

			resolved := ResolveCodeReviewPolicyConfig(&config)

			require.Equal(t, tt.expected, resolved.DescriptionPolicy.Requirements[0].AppliesWhen, "legacy applicability should resolve without filename inference")
			require.NoError(t, resolved.Validate(), "resolved legacy policy should remain valid")
		})
	}
}

func TestCodeReviewPolicyConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*CodeReviewPolicyConfig)
		expectErr bool
	}{
		{name: "valid default"},
		{name: "accepts empty review instructions", mutate: func(c *CodeReviewPolicyConfig) { c.ReviewInstructions = "" }},
		{name: "rejects blank approval policy in approve mode", mutate: func(c *CodeReviewPolicyConfig) {
			c.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
			c.AutomatedApprovalPolicy = "  "
		}, expectErr: true},
		{name: "rejects oversized review instructions", mutate: func(c *CodeReviewPolicyConfig) {
			c.ReviewInstructions = strings.Repeat("界", CodeReviewPromptMaxRunes+1)
		}, expectErr: true},
		{name: "rejects oversized automated approval policy", mutate: func(c *CodeReviewPolicyConfig) {
			c.AutomatedApprovalPolicy = strings.Repeat("界", CodeReviewPromptMaxRunes+1)
		}, expectErr: true},
		{name: "accepts maximum rune count", mutate: func(c *CodeReviewPolicyConfig) {
			c.ReviewInstructions = strings.Repeat("界", CodeReviewPromptMaxRunes)
		}},
		{name: "rejects invalid UTF-8", mutate: func(c *CodeReviewPolicyConfig) { c.ReviewInstructions = string([]byte{0xff}) }, expectErr: true},
		{name: "rejects invalid UTF-8 approval policy", mutate: func(c *CodeReviewPolicyConfig) { c.AutomatedApprovalPolicy = string([]byte{0xff}) }, expectErr: true},
		{name: "rejects zero inline comments", mutate: func(c *CodeReviewPolicyConfig) { c.InlineCommentLimit = 0 }, expectErr: true},
		{name: "rejects too many inline comments", mutate: func(c *CodeReviewPolicyConfig) { c.InlineCommentLimit = 11 }, expectErr: true},
		{name: "rejects too short semantic cooldown", mutate: func(c *CodeReviewPolicyConfig) { c.RiskPolicy.SemanticDedupeCooldownSeconds = 59 }, expectErr: true},
		{name: "rejects too long semantic cooldown", mutate: func(c *CodeReviewPolicyConfig) { c.RiskPolicy.SemanticDedupeCooldownSeconds = 86401 }, expectErr: true},
		{name: "accepts qualified eligible author team", mutate: func(c *CodeReviewPolicyConfig) {
			c.RiskPolicy.EligibleAuthorTeams = []string{"acme/platform-reviewers"}
		}},
		{name: "rejects unqualified eligible author team", mutate: func(c *CodeReviewPolicyConfig) { c.RiskPolicy.EligibleAuthorTeams = []string{"platform-reviewers"} }, expectErr: true},
		{name: "rejects malformed eligible author team", mutate: func(c *CodeReviewPolicyConfig) {
			c.RiskPolicy.EligibleAuthorTeams = []string{"acme/platform/reviewers"}
		}, expectErr: true},
		{name: "rejects no reviewers", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.Reviewers = nil }, expectErr: true},
		{name: "rejects unsupported reviewer", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.Reviewers = []AgentType{AgentTypePMAgent} }, expectErr: true},
		{name: "rejects reviewer model count mismatch", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.ReviewerModels = []string{DefaultCodexModel} }, expectErr: true},
		{name: "rejects reviewer reasoning count mismatch", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerReasoningEfforts = []ReasoningEffort{ReasoningEffortHigh}
		}, expectErr: true},
		{name: "rejects invalid reviewer model", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerModels = []string{DefaultCodexModel, DefaultCodexModel}
		}, expectErr: true},
		{name: "rejects invalid orchestrator model", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.OrchestratorModel = strPtr(DefaultCodexModel) }, expectErr: true},
		{name: "accepts independent reviewer reasoning", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerReasoningEfforts = []ReasoningEffort{ReasoningEffortXHigh, ReasoningEffortMax}
		}},
		{name: "rejects invalid reviewer reasoning effort", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerReasoningEfforts[1] = ReasoningEffort("turbo")
		}, expectErr: true},
		{name: "rejects empty reviewer reasoning effort", mutate: func(c *CodeReviewPolicyConfig) {
			c.AgentRoster.ReviewerReasoningEfforts[1] = ""
		}, expectErr: true},
		{name: "rejects invalid reasoning effort", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.ReasoningEffort = ReasoningEffort("turbo") }, expectErr: true},
		{name: "rejects reasoning effort unsupported by reviewer", mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.ReviewerReasoningEfforts[0] = ReasoningEffortMax }, expectErr: true},
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

func TestCodeReviewGitHubTriggerStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  CodeReviewGitHubTriggerStatus
		wantErr bool
	}{
		{name: "unconfigured", status: CodeReviewGitHubTriggerStatusUnconfigured},
		{name: "ready", status: CodeReviewGitHubTriggerStatusReady},
		{name: "auth required", status: CodeReviewGitHubTriggerStatusAuthRequired},
		{name: "permission required", status: CodeReviewGitHubTriggerStatusPermissionRequired},
		{name: "disconnected", status: CodeReviewGitHubTriggerStatusDisconnected},
		{name: "error", status: CodeReviewGitHubTriggerStatusError},
		{name: "unknown", status: CodeReviewGitHubTriggerStatus("unknown"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.wantErr {
				require.Error(t, err, "unknown GitHub trigger status should be rejected")
				return
			}
			require.NoError(t, err, "known GitHub trigger status should be accepted")
		})
	}
}

func TestResolveCodeReviewPolicyConfigNormalizesPromptFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           CodeReviewPolicyConfig
		expectedReview   string
		expectedApproval string
	}{
		{name: "fills omitted approval policy", config: CodeReviewPolicyConfig{}, expectedReview: "", expectedApproval: DefaultCodeReviewAutomatedApprovalPolicy},
		{name: "trims supplied prompts", config: CodeReviewPolicyConfig{ReviewInstructions: "  review  ", AutomatedApprovalPolicy: "  approve  "}, expectedReview: "review", expectedApproval: "approve"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved := ResolveCodeReviewPolicyConfig(&tt.config)
			require.Equal(t, tt.expectedReview, resolved.ReviewInstructions, "review instructions should resolve predictably")
			require.Equal(t, tt.expectedApproval, resolved.AutomatedApprovalPolicy, "approval policy should resolve predictably")
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
				UpToDate:          true,
				Author:            "devin",
			},
			expected: codeReviewRiskEvaluationForTest(),
		},
		{
			name: "blocks oversized sensitive fork with agent concerns",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.RequirePassingChecks = true
				c.RiskPolicy.ExcludeSensitivePaths = true
				c.RiskPolicy.SensitivePaths = []string{"internal/auth/**"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:         6,
				LinesChanged:         350,
				ChangedPaths:         []string{"internal/auth/session.go"},
				ChecksPassing:        false,
				DescriptionPassed:    false,
				FromFork:             true,
				BlockingFindings:     1,
				ReviewerDisagreement: true,
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 6, Limit: 5},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: 350, Limit: 300},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonChecksFailing},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonDescriptionFailed},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonForkIneligible},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockingFindings},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerDisagreement},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonSensitivePath, Subject: "internal/auth/session.go"},
			),
		},
		{
			name: "default ignores failing GitHub checks",
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChecksPassing:     false,
				DescriptionPassed: true,
				Author:            "devin",
			},
			expected: codeReviewRiskEvaluationForTest(),
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
				Author:                "sam",
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "ci/test"},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonAuthorIneligible},
			),
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
				Author:            "sam",
				AuthorClass:       "human",
			},
			expected: codeReviewRiskEvaluationForTest(),
		},
		{
			name: "allows active member of configured GitHub team",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.EligibleAuthorTeams = []string{"acme/platform"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChecksPassing:     true,
				DescriptionPassed: true,
				Author:            "sam",
				AuthorClass:       "human",
				AuthorTeams:       []string{"ACME/PLATFORM"},
			},
			expected: codeReviewRiskEvaluationForTest(),
		},
		{
			name: "blocks author outside configured GitHub teams",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.EligibleAuthorTeams = []string{"acme/platform"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChecksPassing:     true,
				DescriptionPassed: true,
				Author:            "sam",
				AuthorClass:       "human",
				AuthorTeams:       []string{"acme/security"},
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonAuthorIneligible},
			),
		},
		{
			name: "blocks synthesized reviewer risk signals",
			input: CodeReviewRiskInput{
				FilesChanged:          1,
				LinesChanged:          20,
				ChecksPassing:         true,
				DescriptionPassed:     true,
				Author:                "devin",
				ScopeMismatch:         true,
				UnresolvedUncertainty: true,
				PromptInjectionFound:  true,
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonScopeMismatch},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonUnresolvedUncertainty},
				CodeReviewRiskReason{Code: CodeReviewRiskReasonPromptInjection},
			),
		},
		{
			name: "blocks paths outside allowed scope",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.AllowedPathPatterns = []string{"docs/**", "**/*.md"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChangedPaths:      []string{"internal/api/router.go"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Author:            "devin",
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonPathOutsideScope, Subject: "internal/api/router.go"},
			),
		},
		{
			name: "blocks explicit blocked path patterns",
			mutate: func(c *CodeReviewPolicyConfig) {
				c.RiskPolicy.BlockedPathPatterns = []string{"**/schema/**"}
			},
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      20,
				ChangedPaths:      []string{"internal/db/schema/users.go"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Author:            "devin",
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockedPath, Subject: "internal/db/schema/users.go"},
			),
		},
		{
			name: "filename does not alter the configured churn ceiling",
			input: CodeReviewRiskInput{
				FilesChanged:      1,
				LinesChanged:      607,
				ChangedPaths:      []string{"docs/design/future/111-session-changesets-and-stacks.md"},
				ChecksPassing:     true,
				DescriptionPassed: true,
				Author:            "devin",
			},
			expected: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: 607, Limit: 300},
			),
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

func codeReviewRiskEvaluationForTest(reasons ...CodeReviewRiskReason) CodeReviewRiskEvaluation {
	if len(reasons) == 0 {
		return CodeReviewRiskEvaluation{Acceptable: true}
	}
	return CodeReviewRiskEvaluation{
		Acceptable:    false,
		Reasons:       CodeReviewRiskReasonMessages(reasons),
		ReasonDetails: reasons,
	}
}

func TestCodeReviewRiskReasonCodeValidate(t *testing.T) {
	t.Parallel()

	valid := []CodeReviewRiskReasonCode{
		CodeReviewRiskReasonReviewerDisabled,
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
		CodeReviewRiskReasonOrchestratorSynthesisInvalid,
		CodeReviewRiskReasonOrchestratorEscalation,
		CodeReviewRiskReasonOrchestratorContextStale,
		CodeReviewRiskReasonArchitecture,
		CodeReviewRiskReasonOwnership,
		CodeReviewRiskReasonOperationalRisk,
		CodeReviewRiskReasonSensitiveChange,
		CodeReviewRiskReasonPolicyRequirement,
	}
	tests := make([]struct {
		name      string
		code      CodeReviewRiskReasonCode
		expectErr bool
	}, 0, len(valid)+1)
	for _, code := range valid {
		tests = append(tests, struct {
			name      string
			code      CodeReviewRiskReasonCode
			expectErr bool
		}{name: string(code), code: code})
	}
	tests = append(tests, struct {
		name      string
		code      CodeReviewRiskReasonCode
		expectErr bool
	}{name: "invalid", code: "unknown_reason", expectErr: true})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.code.Validate()
			if tt.expectErr {
				require.Error(t, err, "unknown risk reason codes should fail validation")
				return
			}
			require.NoError(t, err, "known risk reason codes should validate")
		})
	}
}

func TestCodeReviewRiskReasonMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reason   CodeReviewRiskReason
		expected string
	}{
		{name: "reviewer disabled", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerDisabled}, expected: "code reviewer is disabled by policy"},
		{name: "context unavailable", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonContextUnavailable}, expected: "required PR context could not be fetched"},
		{name: "head changed", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonHeadChanged}, expected: "PR head changed after review started"},
		{name: "files limit", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 34, Limit: 20}, expected: "changed files 34 exceeds policy limit 20"},
		{name: "lines limit", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: 1842, Limit: 1000}, expected: "changed lines 1842 exceeds policy limit 1000"},
		{name: "checks failing", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonChecksFailing}, expected: "required GitHub checks are not passing"},
		{name: "required check", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "ci/test"}, expected: "required check is not passing: ci/test"},
		{name: "description", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonDescriptionFailed}, expected: "PR description policy did not pass"},
		{name: "branch", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonBranchOutOfDate}, expected: "PR branch is not up to date"},
		{name: "fork", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonForkIneligible}, expected: "fork PRs are not eligible for approval"},
		{name: "author", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonAuthorIneligible}, expected: "PR author is not eligible for automated approval"},
		{name: "human review", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonUnresolvedHumanReview}, expected: "unresolved human review threads are present"},
		{name: "findings", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockingFindings}, expected: "review agents reported blocking findings"},
		{name: "disagreement", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerDisagreement}, expected: "reviewer agents disagreed on material risk"},
		{name: "scope", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonScopeMismatch}, expected: "orchestrator reported the change may not match the stated intent"},
		{name: "uncertainty", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonUnresolvedUncertainty}, expected: "orchestrator reported unresolved uncertainty"},
		{name: "prompt injection", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonPromptInjection}, expected: "possible prompt-injection attempt found in PR content"},
		{name: "sensitive path", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonSensitivePath, Subject: "internal/auth/session.go"}, expected: "sensitive path changed: internal/auth/session.go"},
		{name: "outside scope", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonPathOutsideScope, Subject: "internal/api/router.go"}, expected: "path is outside allowed policy scope: internal/api/router.go"},
		{name: "blocked path", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockedPath, Subject: "internal/db/schema/users.go"}, expected: "blocked path changed: internal/db/schema/users.go"},
		{name: "policy path", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonPolicyPathChanged, Subject: "internal/models/code_review.go"}, expected: "code review policy/config path changed: internal/models/code_review.go"},
		{name: "category", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonExcludedCategory, Subject: "auth"}, expected: "excluded risk category changed: auth"},
		{name: "quorum", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonReviewerQuorum, Actual: 1, Limit: 2}, expected: "reviewer quorum 1 is below policy requirement 2"},
		{name: "invalid orchestrator synthesis", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid}, expected: "orchestrator did not produce a valid structured synthesis"},
		{name: "orchestrator escalation", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonOrchestratorEscalation}, expected: "coding-agent orchestrator recommends human review"},
		{name: "stale orchestrator context", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonOrchestratorContextStale}, expected: "PR title or description changed after the coding-agent assessment"},
		{name: "architecture", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonArchitecture, Subject: "introduces a new cross-service protocol"}, expected: "human review is required for architectural judgment: introduces a new cross-service protocol"},
		{name: "ownership", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonOwnership, Subject: "touches an unowned boundary"}, expected: "human review is required for ownership judgment: touches an unowned boundary"},
		{name: "operational risk", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonOperationalRisk, Subject: "requires a coordinated rollout"}, expected: "human review is required for operational-risk judgment: requires a coordinated rollout"},
		{name: "sensitive change", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonSensitiveChange, Subject: "changes production data access"}, expected: "human review is required for sensitive-change judgment: changes production data access"},
		{name: "policy requirement", reason: CodeReviewRiskReason{Code: CodeReviewRiskReasonPolicyRequirement, Subject: "requires domain-owner signoff"}, expected: "human review is required for an automated approval policy requirement: requires domain-owner signoff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, tt.reason.Message(), "typed risk reasons should preserve the compatibility message")
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
			risk: codeReviewRiskEvaluationForTest(
				CodeReviewRiskReason{Code: CodeReviewRiskReasonChecksFailing},
			),
			expected: CodeReviewDecisionEvaluation{
				Decision:          CodeReviewDecisionNeedsHumanReview,
				Acceptable:        false,
				RiskReasons:       []string{"required GitHub checks are not passing"},
				RiskReasonDetails: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonChecksFailing}},
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

func TestCodeReviewPromptExamples(t *testing.T) {
	t.Parallel()

	review := CodeReviewPromptExamples()
	approval := CodeReviewAutomatedApprovalExamples()

	require.Equal(t, []CodeReviewPromptExample{CodeReviewPromptExampleBalanced, CodeReviewPromptExampleSecurityFocused, CodeReviewPromptExampleMinimal}, []CodeReviewPromptExample{review[0].Key, review[1].Key, review[2].Key}, "review examples should expose the stable ordered keys")
	require.Equal(t, []CodeReviewAutomatedApprovalExample{CodeReviewAutomatedApprovalExampleConservative, CodeReviewAutomatedApprovalExampleDocumentation, CodeReviewAutomatedApprovalExampleSmallRoutine}, []CodeReviewAutomatedApprovalExample{approval[0].Key, approval[1].Key, approval[2].Key}, "approval examples should expose the stable ordered keys")
	require.Equal(t, DefaultCodeReviewAutomatedApprovalPolicy, approval[0].Policy, "the conservative example should match the built-in approval policy")
	for _, example := range append([]CodeReviewPromptExampleOption(nil), review...) {
		require.NotEmpty(t, example.Instructions, "every review example should contain usable instructions")
	}
	for _, example := range append([]CodeReviewAutomatedApprovalExampleOption(nil), approval...) {
		require.Contains(t, example.Policy, "Unresolved human review threads must not count against approval.", "every approval example should require an independent decision")
	}
}

func TestCodeReviewPolicyConfig_ValidateReturnsStructuredAdvancedFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, field string
		mutate      func(*CodeReviewPolicyConfig)
	}{
		{name: "inline comment limit", field: CodeReviewPolicyFieldInlineCommentLimit, mutate: func(c *CodeReviewPolicyConfig) { c.InlineCommentLimit = 0 }},
		{name: "risk policy", field: CodeReviewPolicyFieldRiskPolicy, mutate: func(c *CodeReviewPolicyConfig) { c.RiskPolicy.MaxFilesChanged = 0 }},
		{name: "agent roster", field: CodeReviewPolicyFieldAgentRoster, mutate: func(c *CodeReviewPolicyConfig) { c.AgentRoster.Reviewers = nil }},
		{name: "description policy", field: CodeReviewPolicyFieldDescriptionPolicy, mutate: func(c *CodeReviewPolicyConfig) { c.DescriptionPolicy.Requirements[0].AppliesWhen.Kind = "invalid" }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := DefaultCodeReviewPolicyConfig()
			tt.mutate(&config)
			err := config.Validate()
			var validationErr *CodeReviewPolicyValidationError
			require.ErrorAs(t, err, &validationErr, "advanced validation should return a structured field error")
			require.Equal(t, tt.field, validationErr.Field, "structured validation should identify the relevant policy subsection")
		})
	}
}

func TestCodeReviewPolicyEditSource_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   CodeReviewPolicyEditSource
		wantErr bool
	}{{name: "manual", value: CodeReviewPolicyEditSourceManual}, {name: "example", value: CodeReviewPolicyEditSourceExample}, {name: "reset", value: CodeReviewPolicyEditSourceReset}, {name: "invalid", value: "prompt text", wantErr: true}}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "unknown edit sources should be rejected")
				return
			}
			require.NoError(t, err, "known privacy-safe edit sources should validate")
		})
	}
}
