package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
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
