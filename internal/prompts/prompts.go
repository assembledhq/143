// Package prompts centralizes all LLM prompt templates used across the application.
// Templates are stored in the templates/ directory and rendered using text/template.
package prompts

import (
	"bytes"
	"embed"
	"strings"
	"text/template"
)

//go:embed templates/*.template
var templateFS embed.FS

var templates *template.Template

func init() {
	templates = template.Must(template.New("").Funcs(template.FuncMap{
		"join": strings.Join,
	}).ParseFS(templateFS, "templates/*.template"))
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
	SkillsDoc    string
	HasNotion    bool
	HasLinear    bool
	HasSentry    bool
	HasGitHub    bool
	LastSyncedAt string // RFC3339 timestamp of last refresh
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

func PRReadinessCustomCheckPrompt() string {
	return render("pr_readiness_custom_check.template", nil)
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

// ─── Preview ───────────────────────────────────────────────────────────────

// SessionPreviewPrewarmClassifierPrompt returns the system prompt for deciding
// whether speculative session preview work is useful.
func SessionPreviewPrewarmClassifierPrompt() string {
	return render("session_preview_prewarm_classifier.template", nil)
}

type SessionPreviewPrewarmClassifierUserPromptData struct {
	RepositoryFullName string
	RepositoryLanguage string
	SessionSource      string
	UserPrompt         string
	IssueLabels        []string
	IssueType          string
	PreviewHistory     string
	CapacitySummary    string
	Phase              string
	ChangedFileKinds   []string
}

func SessionPreviewPrewarmClassifierUserPrompt(data SessionPreviewPrewarmClassifierUserPromptData) string {
	return render("session_preview_prewarm_classifier_user.template", data)
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

// AnswerOnlyPreamble returns the preamble injected into Slack answer-only agent
// prompts. These runs should answer questions without mutating the repository.
func AnswerOnlyPreamble() string {
	return render("answer_only_preamble.template", nil)
}

// SlackRoutingClassifierPrompt returns the system prompt for classifying
// Slack mentions into answer-only or start-work sessions.
func SlackRoutingClassifierPrompt() string {
	return render("slack_routing_classifier_prompt.template", nil)
}

// LinkedIssueContextData renders the canonical XML issue-context block used by
// coding agents when a session is linked to issues.
type LinkedIssueContextData struct {
	LinkedIssues []LinkedIssueContextEntry
}

type LinkedIssueContextEntry struct {
	Role        string
	Source      string
	Title       string
	ExternalID  string
	Description string
	StateName   string
	StateType   string
	Priority    string
	Assignee    string
	TeamKey     string
	TeamName    string
	URL         string
	Attachments []LinkedIssueAttachment
	Comments    []LinkedIssueComment
}

type LinkedIssueAttachment struct {
	Title  string
	URL    string
	Source string
}

type LinkedIssueComment struct {
	Author string
	Body   string
}

// untrustedXMLEscaper escapes the four characters that can break out of the
// surrounding XML-shaped trust fence: `<` and `>` would let Linear-supplied
// text close `</linked_issues>` or `</trust_warning>` and inject pseudo-
// instructions; `&` is escaped for round-trippability; `"` is escaped because
// several fields (StateName, TeamKey, attachment Source, etc.) interpolate
// into XML attributes where a stray quote would close the attribute and
// allow attribute injection. Applied to every Linear-derived field in
// LinkedIssuesContext below; the template itself uses text/template (no
// auto-escape) so the escape must happen at the data layer.
var untrustedXMLEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
)

func sanitizeUntrustedXML(s string) string {
	return untrustedXMLEscaper.Replace(s)
}

// LinkedIssuesContext renders the trust-fenced linked-issue block. Every
// string field on each entry is treated as untrusted external content
// (Linear titles, descriptions, comment bodies, attachment metadata, etc.)
// and is HTML-entity-escaped before reaching the template so an attacker
// can't smuggle a literal `</linked_issues>` or `</trust_warning>` past
// the fence and inject text the agent would treat as instructions.
func LinkedIssuesContext(data LinkedIssueContextData) string {
	sanitized := LinkedIssueContextData{
		LinkedIssues: make([]LinkedIssueContextEntry, len(data.LinkedIssues)),
	}
	for i, entry := range data.LinkedIssues {
		safe := LinkedIssueContextEntry{
			Role:        sanitizeUntrustedXML(entry.Role),
			Source:      sanitizeUntrustedXML(entry.Source),
			Title:       sanitizeUntrustedXML(entry.Title),
			ExternalID:  sanitizeUntrustedXML(entry.ExternalID),
			Description: sanitizeUntrustedXML(entry.Description),
			StateName:   sanitizeUntrustedXML(entry.StateName),
			StateType:   sanitizeUntrustedXML(entry.StateType),
			Priority:    sanitizeUntrustedXML(entry.Priority),
			Assignee:    sanitizeUntrustedXML(entry.Assignee),
			TeamKey:     sanitizeUntrustedXML(entry.TeamKey),
			TeamName:    sanitizeUntrustedXML(entry.TeamName),
			URL:         sanitizeUntrustedXML(entry.URL),
		}
		if len(entry.Attachments) > 0 {
			safe.Attachments = make([]LinkedIssueAttachment, 0, len(entry.Attachments))
			for _, a := range entry.Attachments {
				safe.Attachments = append(safe.Attachments, LinkedIssueAttachment{
					Title:  sanitizeUntrustedXML(a.Title),
					URL:    sanitizeUntrustedXML(a.URL),
					Source: sanitizeUntrustedXML(a.Source),
				})
			}
		}
		if len(entry.Comments) > 0 {
			safe.Comments = make([]LinkedIssueComment, 0, len(entry.Comments))
			for _, c := range entry.Comments {
				safe.Comments = append(safe.Comments, LinkedIssueComment{
					Author: sanitizeUntrustedXML(c.Author),
					Body:   sanitizeUntrustedXML(c.Body),
				})
			}
		}
		sanitized.LinkedIssues[i] = safe
	}
	return render("linked_issues_context.template", sanitized)
}

// ─── Review Loops ────────────────────────────────────────────────────────────

type ReviewLoopReviewPromptData struct {
	AgentType any
	FixMode   any
}

func ReviewLoopReviewPrompt(data ReviewLoopReviewPromptData) string {
	return render("review_loop_review.template", data)
}

func ReviewLoopDecisionPrompt() string {
	return render("review_loop_decision.template", nil)
}

type ReviewLoopFixPromptData struct {
	FixMode any
}

func ReviewLoopFixPrompt(data ReviewLoopFixPromptData) string {
	return render("review_loop_fix.template", data)
}

// ─── Automations ────────────────────────────────────────────────────────────

type AutomationGoalFastImprovementPromptData struct {
	MaxGoalChars int
}

func AutomationGoalFastImprovementPrompt(data AutomationGoalFastImprovementPromptData) string {
	return render("automation_goal_fast_improvement.template", data)
}

type AutomationGoalProposalJudgePromptData struct {
	MaxGoalChars int
}

func AutomationGoalProposalJudgePrompt(data AutomationGoalProposalJudgePromptData) string {
	return render("automation_goal_proposal_judge.template", data)
}

type AutomationGoalDeepImprovementPromptData struct {
	MaxGoalChars  int
	ImprovementID string
	AutomationID  string
	RepositoryID  string
	Name          string
	Scope         string
	CurrentGoal   string
	ConfigJSON    string
	EvidenceJSON  string
}

func AutomationGoalDeepImprovementPrompt(data AutomationGoalDeepImprovementPromptData) string {
	return render("automation_goal_deep_improvement.template", data)
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

// ─── PR Content ──────────────────────────────────────────────────────────────

// PRContentPromptData holds the dynamic values for the PR content system prompt.
type PRContentPromptData struct {
	HasTemplate bool // whether a repo PR template is available
}

// PRContentPrompt renders the system prompt for PR title and body generation.
func PRContentPrompt(data PRContentPromptData) string {
	return render("pr_content_prompt.template", data)
}

// PRContentUserPromptData holds the dynamic values for the PR content user prompt.
type PRContentUserPromptData struct {
	RepoTemplate     string   // the repo's PR template (if any)
	ResultSummary    string   // what the agent did
	ThreadContext    string   // summaries from all visible session threads
	SessionTitle     string   // session title (for manual sessions)
	IssueTitle       string   // issue title
	IssueSource      string   // issue source (e.g. "linear", "sentry")
	IssueSeverity    string   // issue severity
	ValidationChecks []string // e.g. ["Regression tests passed", "CI/CD passed"]
	FileSummary      string   // file-level diff summary
	Diff             string   // truncated raw diff
}

// PRContentUserPrompt renders the user prompt for PR title and body generation.
func PRContentUserPrompt(data PRContentUserPromptData) string {
	return render("pr_content_user_prompt.template", data)
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

// ─── Session Title ────────────────────────────────────────────────────────────

// SessionTitlePromptData holds the dynamic values for the session title prompt.
type SessionTitlePromptData struct {
	CurrentTitle string // empty on initial generation
}

// SessionTitlePrompt renders the system prompt for session title generation.
func SessionTitlePrompt(data SessionTitlePromptData) string {
	return render("session_title_prompt.template", data)
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
