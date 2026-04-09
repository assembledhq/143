// Package prompts centralizes all LLM prompt templates used across the application.
// Templates are stored in the templates/ directory and rendered using text/template.
package prompts

import (
	"bytes"
	"embed"
	"text/template"
)

//go:embed templates/*.template
var templateFS embed.FS

var templates *template.Template

func init() {
	templates = template.Must(template.ParseFS(templateFS, "templates/*.template"))
}

// render executes a named template with the given data and returns the result.
func render(name string, data any) string {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		panic("prompts: failed to render " + name + ": " + err.Error())
	}
	return buf.String()
}

// ─── PM ──────────────────────────────────────────────────────────────────────

// PMSystemPromptData holds the dynamic values for the PM system prompt.
type PMSystemPromptData struct {
	AvailableSlots     int
	MaxConcurrent      int
	ActiveProjectCount int
}

// PMSystemPrompt renders the PM planning session system prompt.
func PMSystemPrompt(data PMSystemPromptData) string {
	return render("pm_system_prompt.template", data)
}

// PMBootstrapPromptData holds the dynamic values for the PM bootstrap prompt.
type PMBootstrapPromptData struct {
	SkillsDoc string // CLI skills doc from GenerateSkillsDoc
	HasNotion bool
	HasLinear bool
	HasSentry bool
	HasGitHub bool
}

// PMBootstrapPrompt renders the system prompt for the PM context bootstrap agent.
func PMBootstrapPrompt(data PMBootstrapPromptData) string {
	return render("pm_bootstrap.template", data)
}

// PMRefreshPromptData holds the dynamic values for the PM context refresh prompt.
type PMRefreshPromptData struct {
	SkillsDoc     string
	HasNotion     bool
	HasLinear     bool
	HasSentry     bool
	HasGitHub     bool
	LastSyncedAt  string // RFC3339 timestamp of last refresh
}

// PMRefreshPrompt renders the system prompt for the PM context refresh agent.
func PMRefreshPrompt(data PMRefreshPromptData) string {
	return render("pm_context_refresh.template", data)
}

// ─── Validation ──────────────────────────────────────────────────────────────

// DirectionCheckPrompt returns the system prompt for validating diff alignment.
func DirectionCheckPrompt() string {
	return render("direction_check_prompt.template", nil)
}

// CorrectnessCheckPrompt returns the system prompt for validating diff correctness.
func CorrectnessCheckPrompt() string {
	return render("correctness_check_prompt.template", nil)
}

// RegressionCheckPrompt returns the system prompt for checking regression tests.
func RegressionCheckPrompt() string {
	return render("regression_check_prompt.template", nil)
}

// ─── Prioritization ─────────────────────────────────────────────────────────

// DirectionAlignmentPrompt returns the system prompt for product alignment scoring.
func DirectionAlignmentPrompt() string {
	return render("direction_alignment_prompt.template", nil)
}

// ComplexityEstimatePrompt returns the system prompt for complexity estimation.
func ComplexityEstimatePrompt() string {
	return render("complexity_estimate_prompt.template", nil)
}

// ─── Feedback ────────────────────────────────────────────────────────────────

// ReviewCommentPrompt returns the system prompt for review comment classification.
func ReviewCommentPrompt() string {
	return render("review_comment_prompt.template", nil)
}

// ─── Agent ───────────────────────────────────────────────────────────────────

// CodingTaskPreamble returns the preamble injected into coding agent system prompts
// when a PM agent assigns a task to a coding agent.
func CodingTaskPreamble() string {
	return render("coding_task_preamble.template", nil)
}

// ─── Slack ────────────────────────────────────────────────────────────────────

// SlackSummarizerPrompt returns the system prompt for Slack thread analysis.
func SlackSummarizerPrompt() string {
	return render("slack_summarizer_prompt.template", nil)
}

// ─── Project ─────────────────────────────────────────────────────────────────

// ProjectGeneratePrompt returns the system prompt for AI project generation.
func ProjectGeneratePrompt() string {
	return render("project_generate_prompt.template", nil)
}

// ProjectCycleSystemPromptData holds the dynamic values for the project cycle system prompt.
type ProjectCycleSystemPromptData struct {
	Title string
	Goal  string
	ID    string
}

// ProjectCycleSystemPrompt renders the system prompt for project-scoped PM cycles.
func ProjectCycleSystemPrompt(data ProjectCycleSystemPromptData) string {
	return render("project_cycle_system_prompt.template", data)
}

// ─── User Prompts ────────────────────────────────────────────────────────────

// DirectionCheckUserPromptData holds the dynamic values for the direction check user prompt.
type DirectionCheckUserPromptData struct {
	IssueContext string
	OrgContext   string
	Diff         string
}

// DirectionCheckUserPrompt renders the user prompt for direction validation.
func DirectionCheckUserPrompt(data DirectionCheckUserPromptData) string {
	return render("direction_check_user_prompt.template", data)
}

// CorrectnessCheckUserPromptData holds the dynamic values for the correctness check user prompt.
type CorrectnessCheckUserPromptData struct {
	IssueContext string
	Diff         string
}

// CorrectnessCheckUserPrompt renders the user prompt for correctness validation.
func CorrectnessCheckUserPrompt(data CorrectnessCheckUserPromptData) string {
	return render("correctness_check_user_prompt.template", data)
}

// RegressionCheckUserPromptData holds the dynamic values for the regression check user prompt.
type RegressionCheckUserPromptData struct {
	IssueContext string
	Diff         string
}

// RegressionCheckUserPrompt renders the user prompt for regression test validation.
func RegressionCheckUserPrompt(data RegressionCheckUserPromptData) string {
	return render("regression_check_user_prompt.template", data)
}

// ReviewCommentUserPromptData holds the dynamic values for the review comment user prompt.
type ReviewCommentUserPromptData struct {
	DiffContext string
	CommentBody string
}

// ReviewCommentUserPrompt renders the user prompt for review comment classification.
func ReviewCommentUserPrompt(data ReviewCommentUserPromptData) string {
	return render("review_comment_user_prompt.template", data)
}

// DirectionAlignmentUserPromptData holds the dynamic values for the direction alignment user prompt.
type DirectionAlignmentUserPromptData struct {
	ProductDirection string
	Title            string
	Description      string
	Severity         string
	OccurrenceCount  int
}

// DirectionAlignmentUserPrompt renders the user prompt for direction alignment assessment.
func DirectionAlignmentUserPrompt(data DirectionAlignmentUserPromptData) string {
	return render("direction_alignment_user_prompt.template", data)
}

// ComplexityEstimateUserPromptData holds the dynamic values for the complexity estimate user prompt.
type ComplexityEstimateUserPromptData struct {
	Title                 string
	Description           string
	Severity              string
	OccurrenceCount       int
	AffectedCustomerCount int
}

// ComplexityEstimateUserPrompt renders the user prompt for complexity estimation.
func ComplexityEstimateUserPrompt(data ComplexityEstimateUserPromptData) string {
	return render("complexity_estimate_user_prompt.template", data)
}

// ─── Eval ─────────────────────────────────────────────────────────────────────

// EvalJudgePromptData holds the dynamic values for the eval judge system prompt.
type EvalJudgePromptData struct {
	OutputMode string // "pass_fail" (default) or "score"
}

// EvalJudgePrompt renders the system prompt for the LLM judge grader.
func EvalJudgePrompt(data EvalJudgePromptData) string {
	if data.OutputMode == "" {
		data.OutputMode = "pass_fail"
	}
	return render("eval_judge_prompt.template", data)
}

// EvalJudgeUserPromptData holds the dynamic values for the eval judge user prompt.
type EvalJudgeUserPromptData struct {
	IssueDescription string
	AgentDiff        string
	CriterionName    string
	CriterionNotes   string
	SolutionDiff     string // optional
}

// EvalJudgeUserPrompt renders the user prompt for the LLM judge grader.
func EvalJudgeUserPrompt(data EvalJudgeUserPromptData) string {
	return render("eval_judge_user_prompt.template", data)
}

// EvalBootstrapPromptData holds the dynamic values for the eval bootstrap prompt.
type EvalBootstrapPromptData struct {
	RepoFullName string
}

// EvalBootstrapPrompt renders the system prompt for the PR history bootstrap agent.
func EvalBootstrapPrompt(data EvalBootstrapPromptData) string {
	return render("eval_bootstrap_prompt.template", data)
}
