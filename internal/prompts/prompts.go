// Package prompts centralizes all LLM prompt templates used across the application.
// Templates are stored in the templates/ directory and rendered using html/template.
package prompts

import (
	"bytes"
	"embed"
	"html/template"
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

// AgentSystemPromptBase returns the base system prompt for coding agents.
func AgentSystemPromptBase() string {
	return render("agent_system_prompt_base.template", nil)
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
