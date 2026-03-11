package pm

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseProjectPlan_ValidJSON(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	summary := `{
		"cycle_analysis": "Good progress on tests",
		"progress_pct": 42,
		"current_phase": "implementation",
		"status_recommendation": "",
		"lessons_learned": ["test first"],
		"new_tasks": [
			{
				"title": "Add caching",
				"description": "Implement Redis caching",
				"approach": "Use go-redis",
				"reasoning": "Performance",
				"complexity": "moderate",
				"confidence": "high"
			}
		],
		"skipped_tasks": [
			{"description": "Refactor auth", "reason": "Not in scope"}
		]
	}`

	pp, err := parseProjectPlan(summary, projectID)
	require.NoError(t, err, "parseProjectPlan should succeed for valid JSON")
	require.Equal(t, projectID, pp.ProjectID, "should set project ID")
	require.Equal(t, "Good progress on tests", pp.CycleAnalysis)
	require.Equal(t, 42, pp.ProgressPct)
	require.Equal(t, "implementation", pp.CurrentPhase)
	require.Len(t, pp.NewTasks, 1)
	require.Equal(t, "Add caching", pp.NewTasks[0].Title)
	require.Len(t, pp.SkippedTasks, 1)
	require.Len(t, pp.LessonsLearned, 1)
}

func TestParseProjectPlan_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := parseProjectPlan("{bad json", uuid.New())
	require.Error(t, err, "parseProjectPlan should fail for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal")
}

func TestParseProjectPlan_EmptyTasks(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	summary := `{"cycle_analysis": "All done", "progress_pct": 100, "new_tasks": [], "lessons_learned": []}`

	pp, err := parseProjectPlan(summary, projectID)
	require.NoError(t, err)
	require.Equal(t, projectID, pp.ProjectID)
	require.Equal(t, 100, pp.ProgressPct)
	require.Empty(t, pp.NewTasks)
}

func TestBuildProjectCycleSystemPrompt(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	project := &ProjectSummary{
		ID:    projectID.String(),
		Title: "Test Project",
		Goal:  "Build something great",
	}

	prompt := buildProjectCycleSystemPrompt(project)
	require.Contains(t, prompt, "Test Project", "prompt should contain project title")
	require.Contains(t, prompt, "Build something great", "prompt should contain project goal")
	require.Contains(t, prompt, projectID.String(), "prompt should contain project ID")
	require.Contains(t, prompt, "cycle_analysis", "prompt should describe expected JSON output")
	require.Contains(t, prompt, "new_tasks", "prompt should mention new_tasks in schema")
}
