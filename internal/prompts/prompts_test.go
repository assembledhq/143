package prompts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── System Prompts ──────────────────────────────────────────────────────────

func TestPMSystemPrompt(t *testing.T) {
	t.Parallel()

	result := PMSystemPrompt(PMSystemPromptData{
		AvailableSlots:     5,
		MaxConcurrent:      3,
		ActiveProjectCount: 2,
	})
	assert.Contains(t, result, "<constraints>")
	assert.Contains(t, result, "</constraints>")
	assert.Contains(t, result, "5")
	assert.Contains(t, result, "3")
	assert.NotEmpty(t, result)
}

func TestDirectionCheckPrompt(t *testing.T) {
	t.Parallel()

	result := DirectionCheckPrompt()
	assert.Contains(t, result, "code review validation agent")
	assert.Contains(t, result, "<evaluation_criteria>")
	assert.Contains(t, result, "PASS the diff if ALL")
	assert.Contains(t, result, "FAIL the diff if ANY")
	assert.Contains(t, result, "</evaluation_criteria>")
	assert.Contains(t, result, "<guidelines>")
	assert.Contains(t, result, "</guidelines>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
}

func TestCorrectnessCheckPrompt(t *testing.T) {
	t.Parallel()

	result := CorrectnessCheckPrompt()
	assert.Contains(t, result, "correctly fixes")
	assert.Contains(t, result, "<evaluation_criteria>")
	assert.Contains(t, result, "root cause")
	assert.Contains(t, result, "</evaluation_criteria>")
	assert.Contains(t, result, "<guidelines>")
	assert.Contains(t, result, "</guidelines>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
}

func TestRegressionCheckPrompt(t *testing.T) {
	t.Parallel()

	result := RegressionCheckPrompt()
	assert.Contains(t, result, "regression tests")
	assert.Contains(t, result, "<evaluation_criteria>")
	assert.Contains(t, result, "</evaluation_criteria>")
	assert.Contains(t, result, "<guidelines>")
	assert.Contains(t, result, "</guidelines>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
}

func TestDirectionAlignmentPrompt(t *testing.T) {
	t.Parallel()

	result := DirectionAlignmentPrompt()
	assert.Contains(t, result, "product alignment")
	assert.Contains(t, result, "<scoring_guide>")
	assert.Contains(t, result, "High alignment")
	assert.Contains(t, result, "Negative alignment")
	assert.Contains(t, result, "</scoring_guide>")
	assert.Contains(t, result, "<guidelines>")
	assert.Contains(t, result, "</guidelines>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
}

func TestComplexityEstimatePrompt(t *testing.T) {
	t.Parallel()

	result := ComplexityEstimatePrompt()
	assert.Contains(t, result, "complexity estimator")
	assert.Contains(t, result, "<tier_definitions>")
	assert.Contains(t, result, "</tier_definitions>")
	assert.Contains(t, result, "<estimation_signals>")
	assert.Contains(t, result, "</estimation_signals>")
	assert.Contains(t, result, "<confidence_calibration>")
	assert.Contains(t, result, "</confidence_calibration>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
}

func TestReviewCommentPrompt(t *testing.T) {
	t.Parallel()

	result := ReviewCommentPrompt()
	assert.Contains(t, result, "review comment")
	assert.Contains(t, result, "<instructions>")
	assert.Contains(t, result, "</instructions>")
	assert.Contains(t, result, "<classification_criteria>")
	assert.Contains(t, result, "</classification_criteria>")
	assert.Contains(t, result, "<category_definitions>")
	assert.Contains(t, result, "</category_definitions>")
	assert.Contains(t, result, "<generalizability_criteria>")
	assert.Contains(t, result, "</generalizability_criteria>")
}

func TestCodingTaskPreamble(t *testing.T) {
	t.Parallel()

	result := CodingTaskPreamble()
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "untrusted external content")
	assert.Contains(t, result, "confidence_score")
	assert.Contains(t, result, "confidence_reasoning")
	assert.Contains(t, result, "risk_factors")
}

func TestProjectGeneratePrompt(t *testing.T) {
	t.Parallel()

	result := ProjectGeneratePrompt()
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "<guidelines>")
	assert.Contains(t, result, "</guidelines>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
	assert.Contains(t, result, "execution_mode")
}

// ─── Project Cycle System Prompt ─────────────────────────────────────────────

func TestProjectCycleSystemPrompt(t *testing.T) {
	t.Parallel()

	result := ProjectCycleSystemPrompt(ProjectCycleSystemPromptData{
		Title: "Auth Refactor",
		Goal:  "Modernize the auth stack",
		ID:    "proj-123",
	})
	assert.Contains(t, result, "<project_title>")
	assert.Contains(t, result, "Auth Refactor")
	assert.Contains(t, result, "</project_title>")
	assert.Contains(t, result, "<goal>")
	assert.Contains(t, result, "Modernize the auth stack")
	assert.Contains(t, result, "</goal>")
	assert.Contains(t, result, "proj-123")
	assert.Contains(t, result, "<analysis_instructions>")
	assert.Contains(t, result, "</analysis_instructions>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "</response_format>")
	assert.Contains(t, result, "cycle_analysis")
	assert.Contains(t, result, "new_tasks")
}

// ─── User Prompts ────────────────────────────────────────────────────────────

func TestDirectionCheckUserPrompt(t *testing.T) {
	t.Parallel()

	result := DirectionCheckUserPrompt(DirectionCheckUserPromptData{
		IssueContext: "Issue Title: Fix login bug\n",
		OrgContext:   "Organization Settings: {}\n",
		Diff:         "+fixed",
	})
	assert.Contains(t, result, "Check if this code change aligns")
	assert.Contains(t, result, "<issue_context>")
	assert.Contains(t, result, "Fix login bug")
	assert.Contains(t, result, "</issue_context>")
	assert.Contains(t, result, "<org_context>")
	assert.Contains(t, result, "Organization Settings")
	assert.Contains(t, result, "</org_context>")
	assert.Contains(t, result, "<code_diff>")
	assert.Contains(t, result, "+fixed")
	assert.Contains(t, result, "</code_diff>")
}

func TestDirectionCheckUserPrompt_NoHTMLEscaping(t *testing.T) {
	t.Parallel()

	result := DirectionCheckUserPrompt(DirectionCheckUserPromptData{
		IssueContext: "test <b>bold</b>\n",
		Diff:         "+line with <angle> brackets",
	})
	require.Contains(t, result, "<b>bold</b>", "template must not HTML-escape angle brackets in data")
	require.Contains(t, result, "<angle>", "template must not HTML-escape angle brackets in data")
	require.NotContains(t, result, "&lt;", "template must use text/template, not html/template")
}

func TestCorrectnessCheckUserPrompt(t *testing.T) {
	t.Parallel()

	result := CorrectnessCheckUserPrompt(CorrectnessCheckUserPromptData{
		IssueContext: "Issue Title: NPE in handler\nSeverity: high\n",
		Diff:         "-old\n+new",
	})
	assert.Contains(t, result, "correctly fixes")
	assert.Contains(t, result, "<issue_context>")
	assert.Contains(t, result, "NPE in handler")
	assert.Contains(t, result, "</issue_context>")
	assert.Contains(t, result, "<code_diff>")
	assert.Contains(t, result, "</code_diff>")
}

func TestRegressionCheckUserPrompt(t *testing.T) {
	t.Parallel()

	result := RegressionCheckUserPrompt(RegressionCheckUserPromptData{
		IssueContext: "Issue Title: Race condition\n",
		Diff:         "+test",
	})
	assert.Contains(t, result, "regression tests")
	assert.Contains(t, result, "<issue_context>")
	assert.Contains(t, result, "Race condition")
	assert.Contains(t, result, "</issue_context>")
	assert.Contains(t, result, "<code_diff>")
	assert.Contains(t, result, "</code_diff>")
}

func TestReviewCommentUserPrompt(t *testing.T) {
	t.Parallel()

	result := ReviewCommentUserPrompt(ReviewCommentUserPromptData{
		DiffContext: "File: main.go, position: 42",
		CommentBody: "This should use a mutex",
	})
	assert.Contains(t, result, "<diff_context>")
	assert.Contains(t, result, "File: main.go, position: 42")
	assert.Contains(t, result, "</diff_context>")
	assert.Contains(t, result, "<review_comment>")
	assert.Contains(t, result, "This should use a mutex")
	assert.Contains(t, result, "</review_comment>")
	assert.Contains(t, result, "<response_format>")
	assert.Contains(t, result, "actionable")
	assert.Contains(t, result, "</response_format>")
}

func TestDirectionAlignmentUserPrompt(t *testing.T) {
	t.Parallel()

	result := DirectionAlignmentUserPrompt(DirectionAlignmentUserPromptData{
		ProductDirection: "Focus on API reliability",
		Title:            "Fix timeout in /api/v2",
		Description:      "Users seeing 504s",
		Severity:         "high",
		OccurrenceCount:  150,
	})
	assert.Contains(t, result, "<product_direction>")
	assert.Contains(t, result, "Focus on API reliability")
	assert.Contains(t, result, "</product_direction>")
	assert.Contains(t, result, "<issue>")
	assert.Contains(t, result, "Fix timeout in /api/v2")
	assert.Contains(t, result, "Users seeing 504s")
	assert.Contains(t, result, "</issue>")
	assert.Contains(t, result, "150")
}

func TestComplexityEstimateUserPrompt(t *testing.T) {
	t.Parallel()

	result := ComplexityEstimateUserPrompt(ComplexityEstimateUserPromptData{
		Title:                 "Refactor auth middleware",
		Description:           "Needs new token format",
		Severity:              "medium",
		OccurrenceCount:       10,
		AffectedCustomerCount: 5,
	})
	assert.Contains(t, result, "<issue>")
	assert.Contains(t, result, "Refactor auth middleware")
	assert.Contains(t, result, "Needs new token format")
	assert.Contains(t, result, "</issue>")
	assert.Contains(t, result, "medium")
	assert.Contains(t, result, "10")
	assert.Contains(t, result, "5")
}

// ─── Eval Prompts ────────────────────────────────────────────────────────────

func TestEvalJudgePrompt(t *testing.T) {
	t.Parallel()

	t.Run("pass_fail mode", func(t *testing.T) {
		result := EvalJudgePrompt(EvalJudgePromptData{OutputMode: "pass_fail"})
		assert.Contains(t, result, "expert code review judge")
		assert.Contains(t, result, "1.0 if pass else 0.0")
		assert.NotEmpty(t, result)
	})

	t.Run("score mode", func(t *testing.T) {
		result := EvalJudgePrompt(EvalJudgePromptData{OutputMode: "score"})
		assert.Contains(t, result, "float 0.0-1.0")
	})

	t.Run("default mode", func(t *testing.T) {
		result := EvalJudgePrompt(EvalJudgePromptData{})
		assert.Contains(t, result, "1.0 if pass else 0.0")
	})
}

func TestEvalJudgeUserPrompt(t *testing.T) {
	t.Parallel()

	t.Run("with solution diff", func(t *testing.T) {
		result := EvalJudgeUserPrompt(EvalJudgeUserPromptData{
			IssueDescription: "Fix the auth bug",
			AgentDiff:        "+fixed auth",
			CriterionName:    "tests_pass",
			CriterionNotes:   "All tests should pass",
			SolutionDiff:     "+correct fix",
		})
		assert.Contains(t, result, "Fix the auth bug")
		assert.Contains(t, result, "+fixed auth")
		assert.Contains(t, result, "tests_pass")
		assert.Contains(t, result, "All tests should pass")
		assert.Contains(t, result, "+correct fix")
		assert.Contains(t, result, "Known-Good Solution")
	})

	t.Run("without solution diff", func(t *testing.T) {
		result := EvalJudgeUserPrompt(EvalJudgeUserPromptData{
			IssueDescription: "Fix the auth bug",
			AgentDiff:        "+fixed",
			CriterionName:    "quality",
			CriterionNotes:   "Good code",
		})
		assert.NotContains(t, result, "Known-Good Solution")
	})

	t.Run("empty agent diff", func(t *testing.T) {
		result := EvalJudgeUserPrompt(EvalJudgeUserPromptData{
			IssueDescription: "Do something",
			CriterionName:    "test",
			CriterionNotes:   "notes",
		})
		assert.Contains(t, result, "No changes produced")
	})
}

func TestEvalBootstrapPrompt(t *testing.T) {
	t.Parallel()

	result := EvalBootstrapPrompt(EvalBootstrapPromptData{
		RepoFullName: "org/repo",
	})
	assert.Contains(t, result, "org/repo")
	assert.Contains(t, result, "eval task discovery")
	assert.Contains(t, result, "git log")
	assert.NotEmpty(t, result)
}

func TestUserPrompts_EmptyFields(t *testing.T) {
	t.Parallel()

	t.Run("direction check with empty context", func(t *testing.T) {
		t.Parallel()
		result := DirectionCheckUserPrompt(DirectionCheckUserPromptData{
			IssueContext: "No issue context available.\n",
			Diff:         "",
		})
		assert.Contains(t, result, "No issue context available.")
		assert.Contains(t, result, "<issue_context>")
	})

	t.Run("review comment with empty diff context", func(t *testing.T) {
		t.Parallel()
		result := ReviewCommentUserPrompt(ReviewCommentUserPromptData{
			DiffContext: "",
			CommentBody: "some feedback",
		})
		assert.Contains(t, result, "some feedback")
	})

	t.Run("complexity with zero counts", func(t *testing.T) {
		t.Parallel()
		result := ComplexityEstimateUserPrompt(ComplexityEstimateUserPromptData{
			Title:                 "Minor fix",
			Severity:              "low",
			OccurrenceCount:       0,
			AffectedCustomerCount: 0,
		})
		assert.Contains(t, result, "Minor fix")
	})
}
