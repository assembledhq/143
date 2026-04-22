package pm

import (
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParsePlan(t *testing.T) {
	t.Parallel()

	issueID := uuid.New()
	issueID2 := uuid.New()

	output := `<pm-plan>
{
  "analysis": "Issues cluster around billing timeouts.",
  "tasks": [
    {
      "rank": 1,
      "issue_ids": ["` + issueID.String() + `"],
      "title": "Fix billing timeout",
      "reasoning": "High impact",
      "approach": "Check handlers/billing.go",
      "risk": "Test coverage is thin",
      "complexity": "moderate",
      "confidence": "medium"
    }
  ],
  "clusters": [
    {
      "issue_ids": ["` + issueID.String() + `", "` + issueID2.String() + `"],
      "root_cause": "Missing retry",
      "strategy": "Fix retry logic"
    }
  ],
  "skip": [
    {
      "issue_id": "` + issueID2.String() + `",
      "reason": "in_avoid_area",
      "detail": "Legacy auth"
    }
  ]
}
</pm-plan>`

	plan, err := parsePlan(output)
	require.NoError(t, err, "parsePlan should succeed")
	require.Equal(t, "Issues cluster around billing timeouts.", plan.Analysis, "analysis should parse")
	require.Len(t, plan.Tasks, 1, "should parse tasks")
	require.Equal(t, issueID, plan.Tasks[0].IssueIDs[0], "should parse task issue IDs")
	require.Len(t, plan.Clusters, 1, "should parse clusters")
	require.Len(t, plan.SkippedIssues, 1, "should parse skip list")
	require.Equal(t, issueID2, plan.SkippedIssues[0].IssueID, "should parse skipped issue ID")
}

func TestPlanToDecisionLog(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	orgID := uuid.New()
	issueID := uuid.New()
	issueID2 := uuid.New()

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		Tasks: []Task{
			{
				IssueIDs:  []uuid.UUID{issueID},
				Reasoning: "High impact",
			},
		},
		Clusters: []Cluster{
			{
				IssueIDs:  []uuid.UUID{issueID, issueID2},
				RootCause: "Root cause",
				Strategy:  "Fix core",
			},
		},
		SkippedIssues: []SkipEntry{
			{
				IssueID: issueID2,
				Detail:  "Too risky",
			},
		},
	}

	entries := planToDecisionLog(plan)
	require.Len(t, entries, 4, "should create decision entries for tasks, skips, clusters")
	require.Equal(t, models.PMDecisionTypeDelegate, entries[0].Decision, "should mark task as delegate")
	require.Equal(t, models.PMDecisionTypeSkip, entries[1].Decision, "should mark skip")
	require.Equal(t, models.PMDecisionTypeCluster, entries[2].Decision, "should mark cluster")
}

func TestParsePlan_WithLinearActions(t *testing.T) {
	t.Parallel()

	issueID3 := uuid.New()

	output := `<pm-plan>
{
  "analysis": "Found auth issues that need Linear updates.",
  "tasks": [],
  "clusters": [],
  "skip": [],
  "linear_actions": [
    {
      "issue_id": "` + issueID3.String() + `",
      "external_id": "ENG-456",
      "action": "re_prioritize",
      "detail": "Change priority from Low to High",
      "reasoning": "This issue is causing customer-facing errors"
    }
  ]
}
</pm-plan>`

	plan, err := parsePlan(output)
	require.NoError(t, err, "parsePlan should succeed with linear_actions")
	require.Len(t, plan.LinearActions, 1, "should parse linear_actions")
	require.Equal(t, issueID3, plan.LinearActions[0].IssueID, "should parse linear action issue ID")
	require.Equal(t, "ENG-456", plan.LinearActions[0].ExternalID, "should parse linear action external ID")
	require.Equal(t, "re_prioritize", plan.LinearActions[0].Action, "should parse linear action type")
}

func TestParsePlan_AuthFailureSurface(t *testing.T) {
	t.Parallel()

	// Claude Code CLI prints this when ANTHROPIC_API_KEY is missing.
	output := `{"type":"assistant","content":[{"type":"text","text":"Not logged in · Please run /login"}],"error":"authentication_failed"}`

	_, err := parsePlan(output)
	require.Error(t, err, "missing auth should produce an error")

	var authErr *agent.AuthError
	require.ErrorAs(t, err, &authErr, "auth failure should return a typed AuthError")
	require.Contains(t, authErr.Detail, "not authenticated", "error should identify auth root cause, not tag parsing")
}

func TestParsePlan_EmptyOutput(t *testing.T) {
	t.Parallel()

	_, err := parsePlan("   \n\n   ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no output", "empty output should produce a descriptive error")
}

func TestParsePlan_MissingTagsIncludesExcerpt(t *testing.T) {
	t.Parallel()

	// Non-auth output without tags should still surface a snippet so the
	// operator can diagnose why the agent didn't emit a plan.
	output := "I thought about this for a while but decided not to emit a plan."

	_, err := parsePlan(output)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm plan tags not found")
	require.Contains(t, err.Error(), "decided not to emit", "error should embed output excerpt")
}

func TestExcerpt_TruncatesAndCollapsesNewlines(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello world", excerpt("  hello\nworld  ", 100),
		"excerpt should trim edges and collapse newlines into spaces")

	long := "abcdefghijklmnop"
	got := excerpt(long, 5)
	require.Equal(t, "abcde…", got, "excerpt should cap at max runes and append ellipsis")

	require.Equal(t, "fits", excerpt("fits", 10),
		"excerpt should leave short input unchanged")
}

func TestExcerpt_RedactsAPIKeys(t *testing.T) {
	t.Parallel()

	got := excerpt("failed with key sk-ant-api03-abcdef0123456789ABCDEF", 200)
	require.Contains(t, got, "sk-***REDACTED***", "excerpt should redact Anthropic-shaped API keys")
	require.NotContains(t, got, "api03-abcdef", "excerpt should not leak key material")

	got = excerpt("openai error: sk-proj-AbCdEf1234567890xyz failed", 200)
	require.Contains(t, got, "sk-***REDACTED***", "excerpt should redact OpenAI-shaped API keys")
	require.NotContains(t, got, "AbCdEf1234567890", "excerpt should not leak OpenAI key material")
}

func TestParsePlan_AuthFailureCaseInsensitive(t *testing.T) {
	t.Parallel()

	// Upstream CLI may format the message differently; detection should be
	// robust to capitalization.
	_, err := parsePlan("ERROR: NOT LOGGED IN to anthropic")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not authenticated", "auth detection should be case-insensitive")
}

func TestParsePlan_InvalidEnums(t *testing.T) {
	t.Parallel()

	issueID := uuid.New()
	issueID2 := uuid.New()

	buildOutput := func(complexity, confidence, skipReason string) string {
		return fmt.Sprintf(`<pm-plan>
{
  "analysis": "Test analysis",
  "tasks": [
    {
      "rank": 1,
      "issue_ids": ["%s"],
      "title": "Fix issue",
      "reasoning": "High impact",
      "approach": "Check handlers/billing.go",
      "risk": "Test coverage is thin",
      "complexity": "%s",
      "confidence": "%s"
    }
  ],
  "clusters": [
    {
      "issue_ids": ["%s", "%s"],
      "root_cause": "Missing retry",
      "strategy": "Fix retry logic"
    }
  ],
  "skip": [
    {
      "issue_id": "%s",
      "reason": "%s",
      "detail": "Legacy auth"
    }
  ]
}
</pm-plan>`, issueID, complexity, confidence, issueID, issueID2, issueID2, skipReason)
	}

	tests := []struct {
		name       string
		complexity string
		confidence string
		reason     string
	}{
		{
			name:       "invalid complexity",
			complexity: "very_complex",
			confidence: "medium",
			reason:     "duplicate",
		},
		{
			name:       "invalid confidence",
			complexity: "simple",
			confidence: "sure",
			reason:     "duplicate",
		},
		{
			name:       "invalid skip reason",
			complexity: "simple",
			confidence: "medium",
			reason:     "other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			output := buildOutput(tt.complexity, tt.confidence, tt.reason)
			_, err := parsePlan(output)
			require.Error(t, err, "parsePlan should return error for invalid enums")
		})
	}
}
