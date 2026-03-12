package pm

import (
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/models"
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

func TestParsePlan_WithNewProjectsAndLinearActions(t *testing.T) {
	t.Parallel()

	issueID := uuid.New()
	issueID2 := uuid.New()
	issueID3 := uuid.New()

	output := `<pm-plan>
{
  "analysis": "Found a cluster of auth issues that warrant a project.",
  "tasks": [],
  "clusters": [],
  "skip": [],
  "new_projects": [
    {
      "title": "Auth Hardening",
      "goal": "Fix all authentication edge cases",
      "scope": "internal/auth/ and internal/api/middleware/",
      "completion_criteria": "All auth tests pass with 90% coverage",
      "issue_ids": ["` + issueID.String() + `", "` + issueID2.String() + `"],
      "priority": 1,
      "reasoning": "3 related auth failures in the last week"
    }
  ],
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
	require.NoError(t, err, "parsePlan should succeed with new_projects and linear_actions")
	require.Len(t, plan.NewProjects, 1, "should parse new_projects")
	require.Equal(t, "Auth Hardening", plan.NewProjects[0].Title, "should parse project title")
	require.Equal(t, 2, len(plan.NewProjects[0].IssueIDs), "should parse project issue IDs")
	require.Equal(t, 1, plan.NewProjects[0].Priority, "should parse project priority")
	require.Len(t, plan.LinearActions, 1, "should parse linear_actions")
	require.Equal(t, issueID3, plan.LinearActions[0].IssueID, "should parse linear action issue ID")
	require.Equal(t, "ENG-456", plan.LinearActions[0].ExternalID, "should parse linear action external ID")
	require.Equal(t, "re_prioritize", plan.LinearActions[0].Action, "should parse linear action type")
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
