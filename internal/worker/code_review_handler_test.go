package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewInlineComments(t *testing.T) {
	t.Parallel()

	path := "internal/api/router.go"
	emptyPath := ""
	line := 42
	zeroLine := 0

	tests := []struct {
		name     string
		findings []models.CodeReviewFinding
		expected []codereview.SubmitReviewComment
	}{
		{
			name: "returns selected file-backed findings",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
					Body:      "body",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "body"},
			},
		},
		{
			name: "falls back to summary when body is empty",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "summary"},
			},
		},
		{
			name: "skips findings without GitHub comment coordinates",
			findings: []models.CodeReviewFinding{
				{Path: nil, StartLine: &line, Summary: "summary"},
				{Path: &emptyPath, StartLine: &line, Summary: "summary"},
				{Path: &path, StartLine: nil, Summary: "summary"},
				{Path: &path, StartLine: &zeroLine, Summary: "summary"},
				{Path: &path, StartLine: &line},
			},
			expected: []codereview.SubmitReviewComment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewInlineComments(tt.findings)
			require.Equal(t, tt.expected, actual, "codeReviewInlineComments should return deterministic GitHub comments")
		})
	}
}

func TestParseCodeReviewFindings(t *testing.T) {
	t.Parallel()

	output := `Looks mostly good.
::code-comment{title="[P1] Missing org filter" body="This subquery can read rows from another org when IDs collide." file="/workspace/internal/db/users.go" start=42 end=43 priority=1}
::code-comment{title="[P3] Broad note" body="No line means this should be ignored." file="internal/db/users.go"}`

	findings := parseCodeReviewFindings(output, []string{"internal/db/users.go"})

	require.Equal(t, []models.CodeReviewFinding{{
		DedupeKey:  "internal/db/users.go:42:43:missing org filter",
		Severity:   models.CodeReviewFindingSeverityHigh,
		Confidence: models.CodeReviewFindingConfidenceHigh,
		Path:       stringPtr("internal/db/users.go"),
		StartLine:  intPtr(42),
		EndLine:    intPtr(43),
		Summary:    "Missing org filter",
		Body:       "This subquery can read rows from another org when IDs collide.",
	}}, findings, "parser should persist concrete directive-backed findings with repo-relative paths")
}

func TestCodeReviewFindingsOnChangedLines(t *testing.T) {
	t.Parallel()

	path := "internal/db/users.go"
	changedLine := 11
	contextLine := 10
	otherPath := "internal/db/projects.go"
	findings := []models.CodeReviewFinding{
		{ID: uuid.New(), Path: &path, StartLine: &changedLine, Summary: "changed line"},
		{ID: uuid.New(), Path: &path, StartLine: &contextLine, Summary: "context line"},
		{ID: uuid.New(), Path: &otherPath, StartLine: &changedLine, Summary: "other file"},
	}
	files := []codereview.PullRequestFile{
		{
			Filename: path,
			Patch: `@@ -8,5 +8,6 @@ func load() {
 context
 context
 context
+added guard
 context
}`,
		},
	}

	filtered := codeReviewFindingsOnChangedLines(findings, files)

	require.Equal(t, []models.CodeReviewFinding{findings[0]}, filtered, "inline selection should keep only findings attached to added diff lines")
}

func TestCodeReviewDescriptionPassed(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	body := "Fix invoice rounding.\n\nTesting: go test ./...\n\nPreview: https://preview.example.com"

	tests := []struct {
		name    string
		body    *string
		files   []codereview.PullRequestFile
		passed  bool
		message string
	}{
		{
			name:   "passes applicable built-ins",
			body:   &body,
			files:  []codereview.PullRequestFile{{Filename: "frontend/src/App.tsx", Additions: 40, Deletions: 2}},
			passed: true,
		},
		{
			name:   "skips nontrivial and UI requirements for tiny backend change",
			body:   stringPtr("Fix typo in log message."),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 1}},
			passed: true,
		},
		{
			name:   "requires testing evidence for nontrivial change",
			body:   stringPtr("Fix invoice rounding with backend changes."),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 40}},
			passed: false,
		},
		{
			name:   "rejects explicit missing testing evidence",
			body:   stringPtr("Fix invoice rounding with backend changes.\n\nTesting: not run"),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 40}},
			passed: false,
		},
		{
			name:   "requires UI evidence for frontend change",
			body:   stringPtr("Fix chart tooltip.\n\nTesting: npm test"),
			files:  []codereview.PullRequestFile{{Filename: "frontend/src/Chart.tsx", Additions: 8}},
			passed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pr := models.PullRequest{Body: tt.body}
			require.Equal(t, tt.passed, codeReviewDescriptionPassed(policy, pr, tt.files), "description policy should respect applicability and built-in evidence checks")
		})
	}
}

func TestEvaluateCodeReviewDescriptionPolicyUsesCachedArtifact(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	job := runCodeReviewPayload{
		OrgID:         orgID,
		SessionID:     sessionID,
		PolicyVersion: 3,
		HeadSHA:       "head-sha",
	}
	rootKey := "code-review-prompts/" + sessionID.String() + "/head-sha"
	artifactKey := rootKey + "/description-01-custom-requirement"
	passed := false
	artifactMetadata, err := json.Marshal(map[string]any{
		"requirement_key": "custom_requirement",
		"passed":          passed,
		"reason":          "cached failure",
		"policy_version":  3,
		"head_sha":        "head-sha",
	})
	require.NoError(t, err, "cached artifact metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM code_review_prompt_artifacts").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "artifact_key", "role", "agent_provider",
			"content", "metadata", "created_at",
		}).AddRow(uuid.New(), orgID, sessionID, artifactKey, "description_policy", "platform_llm", "cached prompt", artifactMetadata, now))

	policyConfig := models.DefaultCodeReviewPolicyConfig()
	policyConfig.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{{
		Key:      "custom_requirement",
		Title:    "Custom requirement",
		Prompt:   "Require a custom statement.",
		Required: true,
		AppliesWhen: models.CodeReviewDescriptionApplicability{
			Kind: models.CodeReviewDescriptionApplicabilityAll,
		},
	}}
	policy := models.CodeReviewPolicyRecord{
		Version:            3,
		Enabled:            policyConfig.Enabled,
		ApprovalMode:       policyConfig.ApprovalMode,
		DescriptionPolicy:  policyConfig.DescriptionPolicy,
		RiskPolicy:         policyConfig.RiskPolicy,
		AgentRoster:        policyConfig.AgentRoster,
		InlineCommentLimit: policyConfig.InlineCommentLimit,
	}
	llm := &codeReviewDescriptionLLMStub{response: `{"passed":true,"reason":"fresh call"}`}

	evaluation, err := evaluateCodeReviewDescriptionPolicy(context.Background(), &Stores{
		CodeReviews: db.NewCodeReviewStore(mock),
	}, &Services{LLM: llm}, zerolog.Nop(), job, models.PullRequest{Title: "Fix invoices", Body: stringPtr("Body")}, policy, models.CodeReviewSessionMetadata{}, nil)

	require.NoError(t, err, "description evaluation should reuse cached prompt artifact")
	require.False(t, evaluation.Passed, "cached failed requirement should drive the evaluation result")
	require.Equal(t, 1, evaluation.FailedRequirementCount, "cached failed requirement should be counted once")
	require.Equal(t, []string{"Custom requirement: failed (cached failure)"}, evaluation.RequirementSummaries, "cached artifact should produce the same summary as a fresh evaluation")
	require.Equal(t, 0, llm.calls, "cached description artifact should avoid another LLM call")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDescriptionRequirementAppliesTypedRules(t *testing.T) {
	t.Parallel()

	files := []codereview.PullRequestFile{
		{Filename: "frontend/src/Chart.tsx", Additions: 12, Deletions: 1},
		{Filename: "internal/db/users_test.go", Additions: 4},
	}
	tests := []struct {
		name        string
		appliesWhen models.CodeReviewDescriptionApplicability
		expected    bool
	}{
		{
			name:        "matches frontend kind",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityFrontend},
			expected:    true,
		},
		{
			name:        "matches path patterns",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"frontend/**"}},
			expected:    true,
		},
		{
			name:        "matches risk categories",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityCategories, Categories: []string{"frontend"}},
			expected:    true,
		},
		{
			name:        "matches changed test files",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityTests, RequireTestFilesChanged: true},
			expected:    true,
		},
		{
			name:        "does not match unrelated path patterns",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"docs/**"}},
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			requirement := models.CodeReviewDescriptionRequirement{Key: "custom", Required: true, AppliesWhen: tt.appliesWhen}
			require.Equal(t, tt.expected, codeReviewDescriptionRequirementApplies(requirement, files), "typed applicability should be evaluated from changed files")
		})
	}
}

func TestCodeReviewReviewerMessageUsesNativeReviewCommand(t *testing.T) {
	t.Parallel()

	prompt := "Review PR #42 against policy version 3."

	require.Equal(t, "/review "+prompt, codeReviewReviewerMessage(models.AgentTypeCodex, prompt), "Codex reviewer messages should invoke native /review")
	require.Len(t, codeReviewNativeReviewCommands(models.AgentTypeCodex, prompt), 1, "native reviewer command metadata should be persisted")
	require.Equal(t, prompt, codeReviewNativeReviewCommands(models.AgentTypeCodex, prompt)[0].Arguments, "native reviewer command should carry the review prompt as arguments")
	require.Equal(t, prompt, codeReviewReviewerMessage(models.AgentTypeOpenCode, prompt), "agents without a native /review command should receive the plain prompt")
	require.Empty(t, codeReviewNativeReviewCommands(models.AgentTypeOpenCode, prompt), "agents without a native /review command should not persist command metadata")
}

type codeReviewDescriptionLLMStub struct {
	calls    int
	response string
}

func (s *codeReviewDescriptionLLMStub) Complete(context.Context, string, string) (string, error) {
	s.calls++
	return s.response, nil
}

func TestBuildUnavailableCodeReviewOutcome(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	job := runCodeReviewPayload{PolicyVersion: 7, HeadSHA: "abc123"}

	decision, body := buildUnavailableCodeReviewOutcome(policy, job)

	require.Equal(t, models.CodeReviewDecisionNeedsHumanReview, decision.Decision, "unavailable live reviewer evidence should require human review")
	require.False(t, decision.Acceptable, "unavailable live reviewer evidence should not be acceptable risk")
	require.Contains(t, decision.RiskReasons, "Automated reviewer agents are not configured for this worker.", "decision should explain missing live reviewers")
	require.Contains(t, body, "Policy version: 7", "final body should include captured policy version")
	require.Contains(t, body, "Reviewed head: abc123", "final body should include reviewed head")
}

func TestCodeReviewReviewerAgentModel(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode}
	cfg.AgentRoster.ReviewerModels = []string{models.DefaultCodexModel, "  "}

	require.Equal(t, models.DefaultCodexModel, *codeReviewReviewerAgentModel(cfg, 0, models.AgentTypeCodex),
		"non-empty configured model should win")
	require.Equal(t, models.DefaultClaudeCodeModel, *codeReviewReviewerAgentModel(cfg, 1, models.AgentTypeClaudeCode),
		"whitespace-only configured model should fall back to the per-agent default")
	require.Equal(t, models.DefaultClaudeCodeModel, *codeReviewReviewerAgentModel(cfg, 5, models.AgentTypeClaudeCode),
		"out-of-range index should fall back to the per-agent default")

	empty := models.DefaultCodeReviewPolicyConfig()
	empty.AgentRoster.ReviewerModels = nil
	require.Equal(t, models.DefaultCodexModel, *codeReviewReviewerAgentModel(empty, 0, models.AgentTypeCodex),
		"missing reviewer_models should fall back to the per-agent default")
}

func TestCodeReviewOrchestratorAgentModel(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.Orchestrator = models.AgentTypeOpenCode

	pinned := cfg
	model := models.OpenCodeModelGPT54Mini
	pinned.AgentRoster.OrchestratorModel = &model
	require.Equal(t, models.OpenCodeModelGPT54Mini, *codeReviewOrchestratorAgentModel(pinned),
		"non-empty configured orchestrator model should win")

	whitespace := cfg
	blank := "   "
	whitespace.AgentRoster.OrchestratorModel = &blank
	require.Equal(t, models.OpenCodeModelGPT55, *codeReviewOrchestratorAgentModel(whitespace),
		"whitespace-only orchestrator model should fall back to the per-agent default")

	unset := cfg
	unset.AgentRoster.OrchestratorModel = nil
	require.Equal(t, models.OpenCodeModelGPT55, *codeReviewOrchestratorAgentModel(unset),
		"nil orchestrator model should fall back to the per-agent default")
}

func TestCodeReviewStatusTargetURL(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	tests := []struct {
		name        string
		frontendURL string
		expected    string
	}{
		{name: "empty frontend URL omits target", expected: ""},
		{name: "trims trailing slash", frontendURL: "https://143.dev/", expected: "https://143.dev/sessions/" + sessionID.String()},
		{name: "uses base URL", frontendURL: "https://app.143.dev", expected: "https://app.143.dev/sessions/" + sessionID.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewStatusTargetURL(tt.frontendURL, sessionID)

			require.Equal(t, tt.expected, actual, "codeReviewStatusTargetURL should build stable session links")
		})
	}
}

func TestEvaluateLiveCodeReviewOutcome(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	prBody := "Fixes invoice rounding.\n\nTesting: go test ./..."

	tests := []struct {
		name         string
		input        liveCodeReviewOutcomeInput
		expected     models.CodeReviewDecision
		reason       string
		bodyContains string
	}{
		{
			name: "approves when live reviewer quorum and PR health satisfy policy",
			input: liveCodeReviewOutcomeInput{
				Policy:     policy,
				Job:        runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				SessionURL: "https://143.dev/sessions/" + sessionID.String(),
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "Review session: https://143.dev/sessions/" + sessionID.String(),
		},
		{
			name: "uses queued GitHub author login for eligible author policy",
			input: liveCodeReviewOutcomeInput{
				Policy: func() models.CodeReviewPolicyConfig {
					config := policy
					config.RiskPolicy.EligibleAuthors = []string{"anya"}
					return config
				}(),
				Job: runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head", PullRequestAuthor: "anya"},
				PullRequest: models.PullRequest{
					OrgID:      orgID,
					Body:       &prBody,
					HeadSHA:    stringPtr("head"),
					Status:     models.PullRequestStatusOpen,
					AuthoredBy: models.GitIdentitySourceUser,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionApproved,
		},
		{
			name: "withholds approval without reviewer quorum",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "reviewer quorum 1 is below policy requirement 2",
		},
		{
			name: "withholds approval for fork pull requests when policy disallows forks",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head", FromFork: true},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "fork PRs are not eligible for approval",
		},
		{
			name: "withholds approval when prior human review requested changes",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:        orgID,
					Body:         &prBody,
					HeadSHA:      stringPtr("head"),
					Status:       models.PullRequestStatusOpen,
					ReviewStatus: models.PullRequestReviewStatusChangesRequested,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "unresolved human review threads are present",
		},
		{
			name: "withholds approval when PR head moved",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "old"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("new"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "new",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "PR head changed after review started",
		},
		{
			name: "withholds approval for blocking findings",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				Findings: []models.CodeReviewFinding{
					{Severity: models.CodeReviewFindingSeverityHigh},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "review agents reported blocking findings",
		},
		{
			name: "withholds approval for dependency file category",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "go.mod", Additions: 1, Deletions: 0},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "excluded risk category changed: dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, body := evaluateLiveCodeReviewOutcome(tt.input)

			require.Equal(t, tt.expected, decision.Decision, "live code review outcome should choose the expected decision")
			if tt.reason != "" {
				require.Contains(t, decision.RiskReasons, tt.reason, "non-approval should preserve the expected risk reason")
				require.Contains(t, body, tt.reason, "final review body should explain the non-approval reason")
			}
			if tt.bodyContains != "" {
				require.Contains(t, body, tt.bodyContains, "final review body should include expected evidence")
			}
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}
