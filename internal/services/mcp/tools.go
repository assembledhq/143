package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
)

// taskManagerError formats a TaskManager error into an ErrorResult with a
// dedicated branch for unauthorized integrations. The unauthorized variant
// uses a stable, greppable prefix ("linear unauthorized:") so:
//
//  1. The agent gets a clear "stop trying Linear; ask the user to reconnect"
//     signal instead of a generic "linear API returned 4xx".
//  2. Sandbox stdout — captured into session logs and shipped to VictoriaLogs
//     by the worker — is searchable for orgs whose Linear OAuth has fallen
//     over, without a structured logger needing org_id (the sandbox doesn't
//     have it).
//
// providerName is the integration name ("linear", future "jira", etc.) so the
// signal is provider-tagged in the output.
func taskManagerError(action, providerName string, err error) *ToolCallResult {
	if errors.Is(err, integration.ErrLinearUnauthorized) {
		detail := taskManagerUnauthorizedDetail(providerName, err)
		return ErrorResult(fmt.Sprintf(
			"%s unauthorized: %s. Ask the user to reconnect %s in the integrations settings before retrying %s.",
			providerName, detail, providerName, action,
		))
	}
	return ErrorResult(fmt.Sprintf("%s failed: %s", action, err))
}

func taskManagerUnauthorizedDetail(providerName string, err error) string {
	fallback := fmt.Sprintf("%s access token has expired or been revoked", providerName)
	text := strings.TrimSpace(err.Error())
	sentinel := integration.ErrLinearUnauthorized.Error()
	idx := strings.Index(text, sentinel)
	if idx == -1 {
		return fallback
	}
	detail := strings.TrimSpace(strings.TrimPrefix(text[idx+len(sentinel):], ":"))
	if detail == "" {
		return fallback
	}
	return detail
}

// ToolSource is the dispatch surface shared by the MCP server and the CLI:
// a listing of available tools plus a call entry point. Implemented by
// ToolRegistry (direct execution with local credentials, the sandbox model)
// and by the CLI's server-proxied source (laptop model — credentials stay
// server-side and every call is audited per-user).
type ToolSource interface {
	ListTools() []Tool
	CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult
}

// ToolRegistry builds MCP tool definitions from an integration registry and
// dispatches tool calls to the appropriate integration method.
type ToolRegistry struct {
	integrations *integration.Registry
	cursorSigner *integration.LogCursorSigner
}

// NewToolRegistry creates a ToolRegistry backed by the given integration registry.
// It initializes a log cursor signer using LOG_CURSOR_SIGNING_KEY if set, or a
// random ephemeral key otherwise (cursors won't survive restarts without the env var).
func NewToolRegistry(reg *integration.Registry) *ToolRegistry {
	key := []byte(os.Getenv("LOG_CURSOR_SIGNING_KEY"))
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic(fmt.Sprintf("failed to generate log cursor signing key: %s", err))
		}
	}
	return &ToolRegistry{
		integrations: reg,
		cursorSigner: integration.NewLogCursorSigner(key),
	}
}

// ListTools returns all available MCP tools based on which integrations are
// registered. Only tools for configured integrations are included.
func (tr *ToolRegistry) ListTools() []Tool {
	var tools []Tool

	for _, et := range tr.integrations.ErrorTrackers() {
		prefix := et.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_list_errors",
				Description: fmt.Sprintf("List unresolved errors from %s. Returns error summaries with severity, occurrence counts, and affected users.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"project":  {Type: "string", Description: "Project slug to filter by (optional)"},
						"severity": {Type: "string", Description: "Filter by severity level", Enum: []string{"critical", "high", "medium", "low"}},
						"since":    {Type: "string", Description: "Only errors seen after this ISO 8601 timestamp (optional)"},
						"limit":    {Type: "number", Description: "Max results to return (default: 25)", Default: 25},
					},
				},
			},
			Tool{
				Name:        prefix + "_get_error",
				Description: fmt.Sprintf("Get full details for a single error from %s, including stack trace, tags, and error type.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"error_id": {Type: "string", Description: "The error/issue ID"}},
					Required:   []string{"error_id"},
				},
			},
			Tool{
				Name:        prefix + "_get_error_trend",
				Description: fmt.Sprintf("Get occurrence trend for an error from %s over a time period. Returns data points and a direction (increasing/decreasing/stable/spike).", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"error_id": {Type: "string", Description: "The error/issue ID"},
						"period":   {Type: "string", Description: "Time period (e.g. '24h', '7d', '14d')", Default: "14d"},
					},
					Required: []string{"error_id"},
				},
			},
			Tool{
				Name:        prefix + "_find_related_errors",
				Description: fmt.Sprintf("Find errors from %s that likely share a root cause with the given error (same stack trace prefix, same culprit).", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"error_id": {Type: "string", Description: "The error/issue ID"}},
					Required:   []string{"error_id"},
				},
			},
		)
	}

	for _, ip := range tr.integrations.IncidentProviders() {
		prefix := ip.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_list_incidents",
				Description: fmt.Sprintf("List active incidents from %s. Returns incident summaries with status, urgency, priority, service, and PagerDuty URL.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"statuses": {Type: "array", Description: "Incident statuses, e.g. triggered,acknowledged,resolved", Items: &SchemaProperty{Type: "string"}},
						"urgency":  {Type: "string", Description: "Filter by urgency", Enum: []string{"high", "low"}},
						"service":  {Type: "string", Description: "PagerDuty service ID to filter by"},
						"since":    {Type: "string", Description: "Only incidents updated after this ISO 8601 timestamp"},
						"limit":    {Type: "number", Description: "Max results to return (default: 25)", Default: 25},
					},
				},
			},
			Tool{
				Name:        prefix + "_get_incident",
				Description: fmt.Sprintf("Get details for one incident from %s, including assignees, escalation policy, teams, and service.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"incident_id": {Type: "string", Description: "The PagerDuty incident ID"}},
					Required:   []string{"incident_id"},
				},
			},
			Tool{
				Name:        prefix + "_list_notes",
				Description: fmt.Sprintf("List notes on a %s incident.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"incident_id": {Type: "string", Description: "The PagerDuty incident ID"},
						"limit":       {Type: "number", Description: "Max notes to return (default: 50)", Default: 50},
					},
					Required: []string{"incident_id"},
				},
			},
			Tool{
				Name:        prefix + "_list_log_entries",
				Description: fmt.Sprintf("List log entries for a %s incident.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"incident_id": {Type: "string", Description: "The PagerDuty incident ID"},
						"limit":       {Type: "number", Description: "Max log entries to return (default: 100)", Default: 100},
					},
					Required: []string{"incident_id"},
				},
			},
			Tool{
				Name:        prefix + "_get_service",
				Description: fmt.Sprintf("Get details for a %s service.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"service_id": {Type: "string", Description: "The PagerDuty service ID"}},
					Required:   []string{"service_id"},
				},
			},
			Tool{
				Name:        prefix + "_list_oncalls",
				Description: fmt.Sprintf("List current on-calls from %s.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"schedule_id": {Type: "string", Description: "PagerDuty schedule ID to filter by"},
						"limit":       {Type: "number", Description: "Max on-calls to return (default: 20)", Default: 20},
					},
				},
			},
			Tool{
				Name:        prefix + "_find_related_incidents",
				Description: fmt.Sprintf("Find recent %s incidents related to an incident.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"incident_id": {Type: "string", Description: "The PagerDuty incident ID"},
						"days":        {Type: "number", Description: "Lookback window in days (default: 90)", Default: 90},
					},
					Required: []string{"incident_id"},
				},
			},
			Tool{
				Name:        prefix + "_add_note",
				Description: fmt.Sprintf("Add a note to a %s incident.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"incident_id": {Type: "string", Description: "The PagerDuty incident ID"},
						"note":        {Type: "string", Description: "Note body to append to the incident"},
					},
					Required: []string{"incident_id", "note"},
				},
			},
			Tool{
				Name:        prefix + "_create_status_update",
				Description: fmt.Sprintf("Create a stakeholder status update on a %s incident.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"incident_id": {Type: "string", Description: "The PagerDuty incident ID"},
						"body":        {Type: "string", Description: "Status update body"},
					},
					Required: []string{"incident_id", "body"},
				},
			},
		)
		if !incidentProviderWritebackEnabled(ip) {
			tools = tools[:len(tools)-2]
		}
	}

	for _, tm := range tr.integrations.TaskManagers() {
		prefix := tm.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_list_tasks",
				Description: fmt.Sprintf("List tasks from %s matching filters. Returns task summaries with state, priority, and assignee.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"team":     {Type: "string", Description: "Team key to filter by (e.g. 'ENG')"},
						"states":   {Type: "array", Description: "Filter by states (e.g. ['triage','backlog','in_progress'])", Items: &SchemaProperty{Type: "string"}},
						"priority": {Type: "string", Description: "Filter by priority", Enum: []string{"urgent", "high", "medium", "low"}},
						"limit":    {Type: "number", Description: "Max results to return (default: 25)", Default: 25},
					},
				},
			},
			Tool{
				Name:        prefix + "_get_task",
				Description: fmt.Sprintf("Get full details for a task from %s, including description, comments, and linked issues.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"task_id": {Type: "string", Description: "The task ID or identifier (e.g. 'ENG-123')"}},
					Required:   []string{"task_id"},
				},
			},
			Tool{
				Name:        prefix + "_find_related_tasks",
				Description: fmt.Sprintf("Find tasks from %s related to the given task (linked issues, sub-issues, same project).", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"task_id": {Type: "string", Description: "The task ID"}},
					Required:   []string{"task_id"},
				},
			},
			Tool{
				Name:        prefix + "_update_task",
				Description: fmt.Sprintf("Update a task in %s. Can change priority, state, add comments, or modify labels.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"task_id":  {Type: "string", Description: "The task ID"},
						"priority": {Type: "string", Description: "New priority level", Enum: []string{"urgent", "high", "medium", "low"}},
						"state":    {Type: "string", Description: "Target state name"},
						"comment":  {Type: "string", Description: "Comment to add"},
					},
					Required: []string{"task_id"},
				},
			},
			Tool{
				Name:        prefix + "_create_task",
				Description: fmt.Sprintf("Create a new task in %s.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"title":       {Type: "string", Description: "Task title"},
						"description": {Type: "string", Description: "Task description (markdown)"},
						"team_key":    {Type: "string", Description: "Team key (e.g. 'ENG')"},
						"priority":    {Type: "string", Description: "Priority level", Enum: []string{"urgent", "high", "medium", "low"}},
						"labels":      {Type: "array", Description: "Labels to apply", Items: &SchemaProperty{Type: "string"}},
					},
					Required: []string{"title", "team_key"},
				},
			},
		)
	}

	for _, ds := range tr.integrations.DocumentStores() {
		prefix := ds.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_search_documents",
				Description: fmt.Sprintf("Search documents in %s by text query. Returns document summaries with titles and snippets.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"query":     {Type: "string", Description: "Search query text"},
						"workspace": {Type: "string", Description: "Workspace or space to search within (optional)"},
						"limit":     {Type: "number", Description: "Max results (default: 10)", Default: 10},
					},
					Required: []string{"query"},
				},
			},
			Tool{
				Name:        prefix + "_get_document",
				Description: fmt.Sprintf("Get the full content of a document from %s.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"doc_id": {Type: "string", Description: "The document ID"}},
					Required:   []string{"doc_id"},
				},
			},
		)
	}

	for _, cr := range tr.integrations.CodeReviewSources() {
		prefix := cr.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_list_recent_prs",
				Description: fmt.Sprintf("List recently merged Pull Requests from %s. Returns PR summaries with titles, authors, review status, and change size.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"state": {Type: "string", Description: "PR state filter", Enum: []string{"merged", "open", "closed"}, Default: "merged"},
						"limit": {Type: "number", Description: "Max results to return (default: 20)", Default: 20},
					},
				},
			},
			Tool{
				Name:        prefix + "_get_pr_reviews",
				Description: fmt.Sprintf("Get all reviews and inline review comments for a specific Pull Request from %s. Returns review decisions, comments, and code-level feedback.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"pr_number": {Type: "number", Description: "The Pull Request number"},
					},
					Required: []string{"pr_number"},
				},
			},
		)
	}

	for _, ic := range tr.integrations.IssueCreators() {
		prefix := ic.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_create",
				Description: "Create a new issue for the engineering team to work on. Returns the created issue's UUID.",
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"title":       {Type: "string", Description: "Issue title (concise, descriptive)"},
						"description": {Type: "string", Description: "Detailed description of the issue, including context and evidence"},
						"severity":    {Type: "string", Description: "Issue severity level", Enum: []string{"info", "warning", "error", "critical"}, Default: "info"},
						"tags":        {Type: "array", Description: "Tags to categorize the issue", Items: &SchemaProperty{Type: "string"}},
					},
					Required: []string{"title", "description"},
				},
			},
		)
	}

	if len(tr.integrations.PullRequestCreators()) > 0 {
		tools = append(tools,
			Tool{
				Name:        "create_pr",
				Description: "Queue first-class 143 pull request creation for a session. Uses the same PR workflow as the app, including repo templates and session links.",
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"session_id":  {Type: "string", Description: "Session UUID. Omit inside a session sandbox; the server derives it from the signed internal token."},
						"draft":       {Type: "boolean", Description: "Whether to create a draft PR. Omit to use the repo default."},
						"author_mode": {Type: "string", Description: "PR author mode. Omit to use the default (auto).", Enum: []string{"auto", "app", "user"}},
					},
				},
			},
		)
	}

	if len(tr.integrations.SessionTabManagers()) > 0 {
		tools = append(tools, sessionTabToolDefinitions()...)
	}

	if len(tr.integrations.AutomationManagers()) > 0 {
		tools = append(tools, automationToolDefinitions()...)
	}

	if len(tr.integrations.EvalCandidateReporters()) > 0 {
		tools = append(tools, Tool{
			Name:        "eval_add",
			Description: "Add a candidate eval task from the current eval bootstrap session. Only available to sessions launched from eval settings.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"pr_number":           {Type: "number", Description: "Source pull request number"},
					"pr_title":            {Type: "string", Description: "Source pull request title"},
					"base_commit_sha":     {Type: "string", Description: "Commit SHA before the fix"},
					"solution_commit_sha": {Type: "string", Description: "Commit SHA containing the fix"},
					"solution_diff":       {Type: "string", Description: "Diff that solved the issue"},
					"issue_description":   {Type: "string", Description: "Reproducible task prompt for the eval"},
					"scoring_criteria":    {Type: "string", Description: "JSON array of scoring criteria"},
					"complexity":          {Type: "string", Description: "Task complexity", Enum: []string{"trivial", "simple", "moderate", "complex"}, Default: "moderate"},
					"fitness_score":       {Type: "number", Description: "Candidate quality score from 0 to 1"},
					"fitness_reasoning":   {Type: "string", Description: "Why this candidate is useful for regression protection"},
					"evidence":            {Type: "string", Description: "Optional JSON evidence gathered while selecting the candidate"},
					"warnings":            {Type: "array", Description: "Optional warnings reviewers should consider", Items: &SchemaProperty{Type: "string"}},
				},
				Required: []string{"pr_number", "pr_title", "base_commit_sha", "solution_commit_sha", "solution_diff", "issue_description", "scoring_criteria", "complexity", "fitness_score", "fitness_reasoning"},
			},
		})
	}

	if len(tr.integrations.AutomationGoalImprovementCompleters()) > 0 {
		tools = append(tools, Tool{
			Name:        "automation_goal_improvement_complete",
			Description: "Complete the current deep automation-goal improvement session with one structured proposal for human review.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"improvement_id": {Type: "string", Description: "Automation goal improvement UUID"},
					"proposed_goal":  {Type: "string", Description: "Complete improved automation goal"},
					"rationale":      {Type: "string", Description: "Short rationale for the proposal"},
					"changes":        {Type: "array", Description: "Important changes", Items: &SchemaProperty{Type: "string"}},
					"evidence":       {Type: "array", Description: "Evidence used", Items: &SchemaProperty{Type: "string"}},
					"risks":          {Type: "array", Description: "Risks or tradeoffs", Items: &SchemaProperty{Type: "string"}},
					"confidence":     {Type: "string", Description: "Confidence level", Enum: []string{"low", "medium", "high"}, Default: "medium"},
					"warnings":       {Type: "array", Description: "Reviewer warnings", Items: &SchemaProperty{Type: "string"}},
				},
				Required: []string{"improvement_id", "proposed_goal", "rationale", "confidence"},
			},
		})
	}

	if logProviders := tr.integrations.LogProviders(); len(logProviders) > 0 {
		tools = append(tools, logToolDefinitions(logProviders)...)
	}

	for _, pp := range tr.integrations.ProjectProposers() {
		prefix := pp.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_propose",
				Description: "Propose a new repo-scoped project for human review. Creates a project with status 'proposed' that requires human approval before execution begins.",
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"repository_id":       {Type: "string", Description: "Target repository UUID (required)"},
						"title":               {Type: "string", Description: "Project title (required)"},
						"goal":                {Type: "string", Description: "What success looks like (required)"},
						"scope":               {Type: "string", Description: "What is in and out of bounds (optional)"},
						"completion_criteria": {Type: "string", Description: "How to know when done (optional)"},
						"reasoning":           {Type: "string", Description: "Why this project should exist (required)"},
						"source_issue_ids":    {Type: "string", Description: "Comma-separated motivating issue UUIDs (optional)"},
						"priority":            {Type: "number", Description: "Priority 0-100, default 50 (optional)", Default: 50},
						"tasks":               {Type: "string", Description: "JSON array of seed task specs [{title, description, approach, complexity, confidence}] (optional)"},
						"similar_project_ids": {Type: "string", Description: "Comma-separated existing same-repo project UUIDs considered and judged non-duplicate (optional)"},
					},
					Required: []string{"repository_id", "title", "goal", "reasoning"},
				},
			},
		)
	}

	for _, ci := range tr.integrations.CITestInsightsProviders() {
		prefix := ci.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_list_flaky_tests",
				Description: fmt.Sprintf("List flaky tests detected by %s. Returns each flaky test's name, file, classname, owning job, and times-flaked count. Use this to find tests to investigate and fix.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"branch":        {Type: "string", Description: "Restrict to flakes seen on this branch (optional)"},
						"workflow_name": {Type: "string", Description: "Restrict to a specific workflow (optional)"},
						"limit":         {Type: "number", Description: "Max results (default: provider's full list)", Default: 25},
					},
				},
			},
			Tool{
				Name:        prefix + "_get_job_test_results",
				Description: fmt.Sprintf("Fetch individual test results for a single %s job, including failure messages. Use this to read the actual failure output for a flaky test occurrence.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"job_number": {Type: "number", Description: "The CI job number"},
					},
					Required: []string{"job_number"},
				},
			},
			Tool{
				Name:        prefix + "_get_recent_test_failures",
				Description: fmt.Sprintf("Get recent failure occurrences of a single flaky test in %s, with the failure message from each occurrence. Use this to compare multiple failures and identify a root cause before fixing.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"test_name": {Type: "string", Description: "The test function/case name"},
						"classname": {Type: "string", Description: "Class or file grouping (optional, recommended to disambiguate)"},
						"limit":     {Type: "number", Description: "Max failure occurrences to return (default: 5)", Default: 5},
					},
					Required: []string{"test_name"},
				},
			},
		)
	}

	for _, ms := range tr.integrations.MessageSources() {
		prefix := ms.Name()
		tools = append(tools,
			Tool{
				Name:        prefix + "_search_messages",
				Description: fmt.Sprintf("Search messages in %s by text query. Returns message summaries.", prefix),
				InputSchema: ToolSchema{
					Type: "object",
					Properties: map[string]SchemaProperty{
						"query":   {Type: "string", Description: "Search query text"},
						"channel": {Type: "string", Description: "Channel name or ID to search within (optional)"},
						"limit":   {Type: "number", Description: "Max results (default: 10)", Default: 10},
					},
					Required: []string{"query"},
				},
			},
			Tool{
				Name:        prefix + "_get_thread",
				Description: fmt.Sprintf("Get a full conversation thread from %s.", prefix),
				InputSchema: ToolSchema{
					Type:       "object",
					Properties: map[string]SchemaProperty{"message_id": {Type: "string", Description: "The message ID of the thread root"}},
					Required:   []string{"message_id"},
				},
			},
		)
	}

	for _, ms := range tr.integrations.MessageSenders() {
		prefix := ms.Name()
		tools = append(tools, Tool{
			Name:        prefix + "_send",
			Description: fmt.Sprintf("Send a message in %s and return delivery status. Use near automation completion to notify a configured channel or thread about the result.", prefix),
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"channel_id": {Type: "string", Description: "Destination channel ID"},
					"text":       {Type: "string", Description: "Plain-text message body"},
					"thread_ts":  {Type: "string", Description: "Optional Slack thread timestamp to reply in"},
				},
				Required: []string{"channel_id", "text"},
			},
		})
	}

	return tools
}

// CallTool dispatches a tool call to the appropriate integration method.
func (tr *ToolRegistry) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	if name == "create_pr" {
		creators := tr.integrations.PullRequestCreators()
		if len(creators) == 0 {
			return ErrorResult("pull request creator not registered")
		}
		return tr.callPullRequestCreator(ctx, creators[0], "create_pr", args)
	}

	switch name {
	case "session_tabs_list", "session_tabs_get", "session_tabs_create", "session_tabs_send", "session_tabs_messages":
		managers := tr.integrations.SessionTabManagers()
		if len(managers) == 0 {
			return ErrorResult("session tab manager not registered")
		}
		return tr.callSessionTabs(ctx, managers[0], name, args)
	case "automation_create", "automation_update", "automation_run", "automation_pause", "automation_resume":
		managers := tr.integrations.AutomationManagers()
		if len(managers) == 0 {
			return ErrorResult("automation manager not registered")
		}
		return tr.callAutomationManager(ctx, managers[0], name, args)
	case "eval_add":
		reporters := tr.integrations.EvalCandidateReporters()
		if len(reporters) == 0 {
			return ErrorResult("eval candidate reporter not registered")
		}
		return tr.callEvalCandidateReporter(ctx, reporters[0], name, args)
	case "automation_goal_improvement_complete":
		completers := tr.integrations.AutomationGoalImprovementCompleters()
		if len(completers) == 0 {
			return ErrorResult("automation goal improvement completer not registered")
		}
		return tr.callAutomationGoalImprovementCompleter(ctx, completers[0], name, args)
	}

	switch name {
	case "log_query", "log_context", "log_fields", "log_stats":
		return tr.callLogTool(ctx, name, args)
	}

	// Try each integration category. The tool name is prefixed with the
	// provider name (e.g. "sentry_list_errors"), so we match by prefix.
	for _, et := range tr.integrations.ErrorTrackers() {
		prefix := et.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callErrorTracker(ctx, et, method, args)
	}

	for _, ip := range tr.integrations.IncidentProviders() {
		prefix := ip.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callIncidentProvider(ctx, ip, method, args)
	}

	for _, tm := range tr.integrations.TaskManagers() {
		prefix := tm.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callTaskManager(ctx, tm, method, args)
	}

	for _, ds := range tr.integrations.DocumentStores() {
		prefix := ds.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callDocumentStore(ctx, ds, method, args)
	}

	for _, cr := range tr.integrations.CodeReviewSources() {
		prefix := cr.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callCodeReviewSource(ctx, cr, method, args)
	}

	for _, ci := range tr.integrations.CITestInsightsProviders() {
		prefix := ci.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callCITestInsights(ctx, ci, method, args)
	}

	for _, ms := range tr.integrations.MessageSenders() {
		prefix := ms.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		if method != "send" {
			continue
		}
		return tr.callMessageSender(ctx, ms, method, args)
	}

	for _, ms := range tr.integrations.MessageSources() {
		prefix := ms.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callMessageSource(ctx, ms, method, args)
	}

	for _, ic := range tr.integrations.IssueCreators() {
		prefix := ic.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callIssueCreator(ctx, ic, method, args)
	}

	for _, pp := range tr.integrations.ProjectProposers() {
		prefix := pp.Name() + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		method := name[len(prefix):]
		return tr.callProjectProposer(ctx, pp, method, args)
	}

	return ErrorResult(fmt.Sprintf("unknown tool: %s", name))
}

// --------------------------------------------------------------------------
// Error tracker dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callErrorTracker(ctx context.Context, et integration.ErrorTracker, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "list_errors":
		var p struct {
			Project  string `json:"project"`
			Severity string `json:"severity"`
			Since    string `json:"since"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.ErrorFilter{
			ProjectSlug:  p.Project,
			Severity:     p.Severity,
			IsUnresolved: true,
			Limit:        p.Limit,
		}
		if p.Since != "" {
			if t, err := time.Parse(time.RFC3339, p.Since); err == nil {
				filter.Since = t
			}
		}
		results, err := et.ListErrors(ctx, filter)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list errors failed: %s", err))
		}
		return jsonResult(results)

	case "get_error":
		var p struct {
			ErrorID string `json:"error_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		detail, err := et.GetError(ctx, p.ErrorID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get error failed: %s", err))
		}
		return jsonResult(detail)

	case "get_error_trend":
		var p struct {
			ErrorID string `json:"error_id"`
			Period  string `json:"period"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		period := parseDuration(p.Period, 14*24*time.Hour)
		trend, err := et.GetTrend(ctx, p.ErrorID, period)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get trend failed: %s", err))
		}
		return jsonResult(trend)

	case "find_related_errors":
		var p struct {
			ErrorID string `json:"error_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		related, err := et.FindRelated(ctx, p.ErrorID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("find related failed: %s", err))
		}
		return jsonResult(related)

	default:
		return ErrorResult(fmt.Sprintf("unknown error tracker method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Incident provider dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callIncidentProvider(ctx context.Context, ip integration.IncidentProvider, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "list_incidents":
		var p struct {
			Statuses []string `json:"statuses"`
			Urgency  string   `json:"urgency"`
			Service  string   `json:"service"`
			Since    string   `json:"since"`
			Limit    int      `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.IncidentFilter{
			Statuses: p.Statuses,
			Urgency:  p.Urgency,
			Service:  p.Service,
			Limit:    p.Limit,
		}
		if p.Since != "" {
			if t, err := time.Parse(time.RFC3339, p.Since); err == nil {
				filter.Since = t
			}
		}
		incidents, err := ip.ListIncidents(ctx, filter)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list incidents failed: %s", err))
		}
		return jsonResult(incidents)

	case "get_incident":
		var p struct {
			IncidentID string `json:"incident_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		incident, err := ip.GetIncident(ctx, p.IncidentID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get incident failed: %s", err))
		}
		return jsonResult(incident)

	case "list_notes":
		var p struct {
			IncidentID string `json:"incident_id"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		notes, err := ip.ListIncidentNotes(ctx, p.IncidentID, p.Limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list incident notes failed: %s", err))
		}
		return jsonResult(notes)

	case "list_log_entries":
		var p struct {
			IncidentID string `json:"incident_id"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		entries, err := ip.ListIncidentLogEntries(ctx, p.IncidentID, p.Limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list incident log entries failed: %s", err))
		}
		return jsonResult(entries)

	case "get_service":
		var p struct {
			ServiceID string `json:"service_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		service, err := ip.GetService(ctx, p.ServiceID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get service failed: %s", err))
		}
		return jsonResult(service)

	case "list_oncalls":
		var p struct {
			ScheduleID string `json:"schedule_id"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		onCalls, err := ip.ListOnCalls(ctx, integration.OnCallFilter{ScheduleID: p.ScheduleID, Limit: p.Limit})
		if err != nil {
			return ErrorResult(fmt.Sprintf("list on-calls failed: %s", err))
		}
		return jsonResult(onCalls)

	case "find_related_incidents":
		var p struct {
			IncidentID string `json:"incident_id"`
			Days       int    `json:"days"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		related, err := ip.FindRelatedIncidents(ctx, p.IncidentID, p.Days)
		if err != nil {
			return ErrorResult(fmt.Sprintf("find related incidents failed: %s", err))
		}
		return jsonResult(related)

	case "add_note":
		if !incidentProviderWritebackEnabled(ip) {
			return ErrorResult("incident provider writeback is disabled")
		}
		var p struct {
			IncidentID string `json:"incident_id"`
			Note       string `json:"note"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if _, err := ip.AddIncidentNote(ctx, p.IncidentID, p.Note); err != nil {
			return ErrorResult(fmt.Sprintf("add incident note failed: %s", err))
		}
		return TextResult("incident note added successfully")

	case "create_status_update":
		if !incidentProviderWritebackEnabled(ip) {
			return ErrorResult("incident provider writeback is disabled")
		}
		var p struct {
			IncidentID string `json:"incident_id"`
			Body       string `json:"body"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if err := ip.CreateIncidentStatusUpdate(ctx, p.IncidentID, p.Body); err != nil {
			return ErrorResult(fmt.Sprintf("create incident status update failed: %s", err))
		}
		return TextResult("incident status update created successfully")

	default:
		return ErrorResult(fmt.Sprintf("unknown incident provider method: %s", method))
	}
}

type incidentWritebackState interface {
	WritebackEnabled() bool
}

func incidentProviderWritebackEnabled(ip integration.IncidentProvider) bool {
	state, ok := ip.(incidentWritebackState)
	if !ok {
		return true
	}
	return state.WritebackEnabled()
}

// --------------------------------------------------------------------------
// Task manager dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callTaskManager(ctx context.Context, tm integration.TaskManager, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "list_tasks":
		var p struct {
			Team     string   `json:"team"`
			States   []string `json:"states"`
			Priority string   `json:"priority"`
			Limit    int      `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.TaskFilter{
			TeamKey:  p.Team,
			States:   p.States,
			Priority: p.Priority,
			Limit:    p.Limit,
		}
		results, err := tm.ListTasks(ctx, filter)
		if err != nil {
			return taskManagerError("list tasks", tm.Name(), err)
		}
		return jsonResult(results)

	case "get_task":
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		detail, err := tm.GetTask(ctx, p.TaskID)
		if err != nil {
			return taskManagerError("get task", tm.Name(), err)
		}
		return jsonResult(detail)

	case "find_related_tasks":
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		related, err := tm.FindRelated(ctx, p.TaskID)
		if err != nil {
			return taskManagerError("find related", tm.Name(), err)
		}
		return jsonResult(related)

	case "update_task":
		var p struct {
			TaskID   string  `json:"task_id"`
			Priority *string `json:"priority"`
			State    *string `json:"state"`
			Comment  *string `json:"comment"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		update := integration.TaskUpdate{
			Priority: p.Priority,
			State:    p.State,
			Comment:  p.Comment,
		}
		if err := tm.UpdateTask(ctx, p.TaskID, update); err != nil {
			return taskManagerError("update task", tm.Name(), err)
		}
		return TextResult("task updated successfully")

	case "create_task":
		var p struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			TeamKey     string   `json:"team_key"`
			Priority    string   `json:"priority"`
			Labels      []string `json:"labels"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		spec := integration.TaskCreateSpec{
			Title:       p.Title,
			Description: p.Description,
			TeamKey:     p.TeamKey,
			Priority:    p.Priority,
			Labels:      p.Labels,
		}
		task, err := tm.CreateTask(ctx, spec)
		if err != nil {
			return taskManagerError("create task", tm.Name(), err)
		}
		return jsonResult(task)

	default:
		return ErrorResult(fmt.Sprintf("unknown task manager method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Document store dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callDocumentStore(ctx context.Context, ds integration.DocumentStore, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "search_documents":
		var p struct {
			Query     string `json:"query"`
			Workspace string `json:"workspace"`
			Limit     int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.DocFilter{
			Workspace: p.Workspace,
			Limit:     p.Limit,
		}
		results, err := ds.SearchDocuments(ctx, p.Query, filter)
		if err != nil {
			return ErrorResult(fmt.Sprintf("search documents failed: %s", err))
		}
		return jsonResult(results)

	case "get_document":
		var p struct {
			DocID string `json:"doc_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		doc, err := ds.GetDocument(ctx, p.DocID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get document failed: %s", err))
		}
		return jsonResult(doc)

	default:
		return ErrorResult(fmt.Sprintf("unknown document store method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Code review source dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callCodeReviewSource(ctx context.Context, cr integration.CodeReviewSource, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "list_recent_prs":
		var p struct {
			State string `json:"state"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.PRFilter{
			State: p.State,
			Limit: p.Limit,
		}
		results, err := cr.ListRecentPRs(ctx, filter)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list recent PRs failed: %s", err))
		}
		return jsonResult(results)

	case "get_pr_reviews":
		var p struct {
			PRNumber int `json:"pr_number"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.PRNumber <= 0 {
			return ErrorResult("pr_number is required and must be positive")
		}
		reviews, err := cr.GetPRReviews(ctx, p.PRNumber)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get PR reviews failed: %s", err))
		}
		return jsonResult(reviews)

	default:
		return ErrorResult(fmt.Sprintf("unknown code review source method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Message source dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callMessageSource(ctx context.Context, ms integration.MessageSource, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "search_messages":
		var p struct {
			Query   string `json:"query"`
			Channel string `json:"channel"`
			Limit   int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.MessageFilter{
			Channel: p.Channel,
			Limit:   p.Limit,
		}
		results, err := ms.SearchMessages(ctx, p.Query, filter)
		if err != nil {
			return ErrorResult(fmt.Sprintf("search messages failed: %s", err))
		}
		return jsonResult(results)

	case "get_thread":
		var p struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		thread, err := ms.GetThread(ctx, p.MessageID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get thread failed: %s", err))
		}
		return jsonResult(thread)

	default:
		return ErrorResult(fmt.Sprintf("unknown message source method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Message sender dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callMessageSender(ctx context.Context, ms integration.MessageSender, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "send":
		var p integration.SendMessageParams
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if strings.TrimSpace(p.ChannelID) == "" {
			return ErrorResult("channel_id is required")
		}
		if strings.TrimSpace(p.Text) == "" {
			return ErrorResult("text is required")
		}
		result, err := ms.SendMessage(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("send message failed: %s", err))
		}
		return jsonResult(result)
	default:
		return ErrorResult(fmt.Sprintf("unknown message sender method: %s", method))
	}
}

// ciTestInsightsError surfaces auth failures with a "reconnect" prompt so
// the agent stops retrying a doomed call instead of seeing a generic 4xx.
func ciTestInsightsError(action, providerName string, err error) *ToolCallResult {
	if errors.Is(err, integration.ErrCircleCIUnauthorized) {
		detail := strings.TrimSpace(err.Error())
		return ErrorResult(fmt.Sprintf(
			"%s unauthorized: %s. Ask the user to reconnect %s in the integrations settings before retrying %s.",
			providerName, detail, providerName, action,
		))
	}
	return ErrorResult(fmt.Sprintf("%s failed: %s", action, err))
}

// --------------------------------------------------------------------------
// CI test insights dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callCITestInsights(ctx context.Context, ci integration.CITestInsights, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "list_flaky_tests":
		var p struct {
			Branch       string `json:"branch"`
			WorkflowName string `json:"workflow_name"`
			Limit        int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		filter := integration.FlakyTestFilter{
			Branch:       p.Branch,
			WorkflowName: p.WorkflowName,
			Limit:        p.Limit,
		}
		results, err := ci.ListFlakyTests(ctx, filter)
		if err != nil {
			return ciTestInsightsError("list flaky tests", ci.Name(), err)
		}
		return jsonResult(results)

	case "get_job_test_results":
		var p struct {
			JobNumber int `json:"job_number"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.JobNumber <= 0 {
			return ErrorResult("job_number is required and must be positive")
		}
		results, err := ci.GetTestResults(ctx, integration.JobRef{JobNumber: p.JobNumber})
		if err != nil {
			return ciTestInsightsError("get job test results", ci.Name(), err)
		}
		return jsonResult(results)

	case "get_recent_test_failures":
		var p struct {
			TestName  string `json:"test_name"`
			Classname string `json:"classname"`
			Limit     int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.TestName == "" {
			return ErrorResult("test_name is required")
		}
		failures, err := ci.GetRecentFailures(ctx, p.Classname, p.TestName, p.Limit)
		if err != nil {
			return ciTestInsightsError("get recent test failures", ci.Name(), err)
		}
		return jsonResult(failures)

	default:
		return ErrorResult(fmt.Sprintf("unknown ci test insights method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Issue creator dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callIssueCreator(ctx context.Context, ic integration.IssueCreator, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "create":
		var p struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Severity    string   `json:"severity"`
			Tags        []string `json:"tags"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.Title == "" {
			return ErrorResult("title is required")
		}
		if p.Description == "" {
			return ErrorResult("description is required")
		}
		params := integration.CreateIssueParams{
			Title:       p.Title,
			Description: p.Description,
			Severity:    p.Severity,
			Tags:        p.Tags,
		}
		result, err := ic.CreateIssue(ctx, params)
		if err != nil {
			return ErrorResult(fmt.Sprintf("create issue failed: %s", err))
		}
		return jsonResult(result)

	default:
		return ErrorResult(fmt.Sprintf("unknown issue creator method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Pull request creator dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callPullRequestCreator(ctx context.Context, pc integration.PullRequestCreator, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "create_pr":
		var p struct {
			SessionID  string          `json:"session_id"`
			Draft      json.RawMessage `json:"draft"`
			AuthorMode string          `json:"author_mode"`
		}
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		var draft *bool
		if len(p.Draft) > 0 && string(p.Draft) != "null" {
			var b bool
			if err := json.Unmarshal(p.Draft, &b); err != nil {
				var s string
				if strErr := json.Unmarshal(p.Draft, &s); strErr != nil {
					return ErrorResult("draft must be true or false")
				}
				switch strings.ToLower(strings.TrimSpace(s)) {
				case "true":
					b = true
				case "false":
					b = false
				default:
					return ErrorResult("draft must be true or false")
				}
			}
			draft = &b
		}
		result, err := pc.CreatePullRequest(ctx, integration.CreatePullRequestParams{
			SessionID:  p.SessionID,
			Draft:      draft,
			AuthorMode: p.AuthorMode,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("create pull request failed: %s", err))
		}
		return jsonResult(result)

	default:
		return ErrorResult(fmt.Sprintf("unknown pull request creator method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Eval candidate reporter dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callEvalCandidateReporter(ctx context.Context, reporter integration.EvalCandidateReporter, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "eval_add":
		var p struct {
			PRNumber          int             `json:"pr_number"`
			PRTitle           string          `json:"pr_title"`
			BaseCommitSHA     string          `json:"base_commit_sha"`
			SolutionCommitSHA string          `json:"solution_commit_sha"`
			SolutionDiff      string          `json:"solution_diff"`
			IssueDescription  string          `json:"issue_description"`
			ScoringCriteria   json.RawMessage `json:"scoring_criteria"`
			Complexity        string          `json:"complexity"`
			FitnessScore      float64         `json:"fitness_score"`
			FitnessReasoning  string          `json:"fitness_reasoning"`
			Evidence          json.RawMessage `json:"evidence"`
			Warnings          []string        `json:"warnings"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.PRNumber <= 0 {
			return ErrorResult("pr_number is required")
		}
		if strings.TrimSpace(p.PRTitle) == "" {
			return ErrorResult("pr_title is required")
		}
		if strings.TrimSpace(p.BaseCommitSHA) == "" {
			return ErrorResult("base_commit_sha is required")
		}
		if strings.TrimSpace(p.SolutionCommitSHA) == "" {
			return ErrorResult("solution_commit_sha is required")
		}
		if strings.TrimSpace(p.SolutionDiff) == "" {
			return ErrorResult("solution_diff is required")
		}
		if strings.TrimSpace(p.IssueDescription) == "" {
			return ErrorResult("issue_description is required")
		}
		if strings.TrimSpace(p.Complexity) == "" {
			return ErrorResult("complexity is required")
		}
		if strings.TrimSpace(p.FitnessReasoning) == "" {
			return ErrorResult("fitness_reasoning is required")
		}
		criteria := p.ScoringCriteria
		if len(criteria) == 0 {
			return ErrorResult("scoring_criteria is required")
		}
		var criteriaProbe []any
		if err := json.Unmarshal(criteria, &criteriaProbe); err != nil {
			var criteriaString string
			if strErr := json.Unmarshal(criteria, &criteriaString); strErr != nil {
				return ErrorResult("scoring_criteria must be a JSON array")
			}
			criteria = json.RawMessage(criteriaString)
			if err := json.Unmarshal(criteria, &criteriaProbe); err != nil {
				return ErrorResult("scoring_criteria must be a JSON array")
			}
		}
		evidence := p.Evidence
		if len(evidence) > 0 {
			var evidenceProbe any
			if err := json.Unmarshal(evidence, &evidenceProbe); err != nil {
				var evidenceString string
				if strErr := json.Unmarshal(evidence, &evidenceString); strErr != nil {
					return ErrorResult("evidence must be valid JSON when provided")
				}
				evidence = json.RawMessage(evidenceString)
				if err := json.Unmarshal(evidence, &evidenceProbe); err != nil {
					return ErrorResult("evidence must be valid JSON when provided")
				}
			}
		}

		result, err := reporter.AddCandidate(ctx, integration.AddEvalCandidateParams{
			PRNumber:          p.PRNumber,
			PRTitle:           p.PRTitle,
			BaseCommitSHA:     p.BaseCommitSHA,
			SolutionCommitSHA: p.SolutionCommitSHA,
			SolutionDiff:      p.SolutionDiff,
			IssueDescription:  p.IssueDescription,
			ScoringCriteria:   criteria,
			Complexity:        p.Complexity,
			FitnessScore:      p.FitnessScore,
			FitnessReasoning:  p.FitnessReasoning,
			Evidence:          evidence,
			Warnings:          p.Warnings,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("add eval candidate failed: %s", err))
		}
		return jsonResult(result)
	default:
		return ErrorResult(fmt.Sprintf("unknown eval candidate reporter method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Automation goal improvement completer dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callAutomationGoalImprovementCompleter(ctx context.Context, completer integration.AutomationGoalImprovementCompleter, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "automation_goal_improvement_complete":
		var p struct {
			ImprovementID string   `json:"improvement_id"`
			ProposedGoal  string   `json:"proposed_goal"`
			Rationale     string   `json:"rationale"`
			Changes       []string `json:"changes"`
			Evidence      []string `json:"evidence"`
			Risks         []string `json:"risks"`
			Confidence    string   `json:"confidence"`
			Warnings      []string `json:"warnings"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if strings.TrimSpace(p.ImprovementID) == "" {
			return ErrorResult("improvement_id is required")
		}
		if _, err := uuid.Parse(p.ImprovementID); err != nil {
			return ErrorResult("improvement_id must be a valid UUID")
		}
		if strings.TrimSpace(p.ProposedGoal) == "" {
			return ErrorResult("proposed_goal is required")
		}
		if strings.TrimSpace(p.Rationale) == "" {
			return ErrorResult("rationale is required")
		}
		confidence := strings.TrimSpace(p.Confidence)
		if confidence == "" {
			confidence = "medium"
		}
		result, err := completer.CompleteGoalImprovement(ctx, integration.CompleteAutomationGoalImprovementParams{
			ImprovementID: p.ImprovementID,
			ProposedGoal:  p.ProposedGoal,
			Rationale:     p.Rationale,
			Changes:       p.Changes,
			Evidence:      p.Evidence,
			Risks:         p.Risks,
			Confidence:    confidence,
			Warnings:      p.Warnings,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("complete automation goal improvement failed: %s", err))
		}
		return jsonResult(result)
	default:
		return ErrorResult(fmt.Sprintf("unknown automation goal improvement completer method: %s", method))
	}
}

func automationToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "automation_create",
			Description: "Create a repo-scoped automation from a JSON payload. The payload uses the same flat fields as the automation create API and must set repository_id to the current session repository.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"payload": {Type: "string", Description: "JSON automation create payload"},
			}, Required: []string{"payload"}},
		},
		{
			Name:        "automation_update",
			Description: "Update a repo-scoped automation from a JSON payload. The target automation must belong to the current session repository.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"automation_id": {Type: "string", Description: "Automation UUID"},
				"payload":       {Type: "string", Description: "JSON automation update payload"},
			}, Required: []string{"automation_id", "payload"}},
		},
		{
			Name:        "automation_run",
			Description: "Queue an immediate run for a repo-scoped automation in the current session repository.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"automation_id": {Type: "string", Description: "Automation UUID"},
			}, Required: []string{"automation_id"}},
		},
		{
			Name:        "automation_pause",
			Description: "Pause a repo-scoped automation in the current session repository.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"automation_id": {Type: "string", Description: "Automation UUID"},
			}, Required: []string{"automation_id"}},
		},
		{
			Name:        "automation_resume",
			Description: "Resume a repo-scoped automation in the current session repository.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"automation_id": {Type: "string", Description: "Automation UUID"},
			}, Required: []string{"automation_id"}},
		},
	}
}

func (tr *ToolRegistry) callAutomationManager(ctx context.Context, manager integration.AutomationManager, name string, args json.RawMessage) *ToolCallResult {
	var p struct {
		AutomationID string `json:"automation_id"`
		Payload      string `json:"payload"`
	}
	if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	automationID := strings.TrimSpace(p.AutomationID)
	switch name {
	case "automation_create":
		payload, err := parseAutomationToolPayload(p.Payload)
		if err != nil {
			return ErrorResult(err.Error())
		}
		raw, err := manager.CreateAutomation(ctx, payload)
		if err != nil {
			return ErrorResult(fmt.Sprintf("create automation failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "automation_update":
		if automationID == "" {
			return ErrorResult("automation_id is required")
		}
		payload, err := parseAutomationToolPayload(p.Payload)
		if err != nil {
			return ErrorResult(err.Error())
		}
		raw, err := manager.UpdateAutomation(ctx, automationID, payload)
		if err != nil {
			return ErrorResult(fmt.Sprintf("update automation failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "automation_run":
		if automationID == "" {
			return ErrorResult("automation_id is required")
		}
		raw, err := manager.RunAutomation(ctx, automationID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("run automation failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "automation_pause":
		if automationID == "" {
			return ErrorResult("automation_id is required")
		}
		raw, err := manager.PauseAutomation(ctx, automationID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("pause automation failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "automation_resume":
		if automationID == "" {
			return ErrorResult("automation_id is required")
		}
		raw, err := manager.ResumeAutomation(ctx, automationID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("resume automation failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	default:
		return ErrorResult(fmt.Sprintf("unknown automation tool: %s", name))
	}
}

func parseAutomationToolPayload(raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("payload is required")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	return json.RawMessage(raw), nil
}

func sessionTabToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "session_tabs_list",
			Description: "List tabs in the current session only. Returns sibling tab status, activity, cost, and delivery state.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"include_archived": {Type: "boolean", Description: "Include archived tabs in the current session only", Default: false},
			}},
		},
		{
			Name:        "session_tabs_get",
			Description: "Get one tab from the current session only, including recent touched files and delivery state.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"tab_id": {Type: "string", Description: "Tab/thread UUID in the current session only"},
			}, Required: []string{"tab_id"}},
		},
		{
			Name:        "session_tabs_create",
			Description: "Create a blank idle tab in the current session only. Does not start agent execution.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"agent":        {Type: "string", Description: "Agent type for the new tab", Enum: []string{"codex", "claude_code", "amp", "pi", "opencode"}},
				"instructions": {Type: "string", Description: "Optional stored instructions for the new tab"},
				"label":        {Type: "string", Description: "Optional tab label"},
				"model":        {Type: "string", Description: "Optional model override"},
			}},
		},
		{
			Name:        "session_tabs_send",
			Description: "Send a user message to a tab in the current session only and queue delivery.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"client_message_id": {Type: "string", Description: "Optional idempotency key"},
				"message":           {Type: "string", Description: "Message to send (required unless --message-file is provided)"},
				"message_file":      {Type: "string", Description: "Path to a file inside the sandbox containing the message (alternative to --message)"},
				"tab_id":            {Type: "string", Description: "Target tab/thread UUID in the current session only"},
			}, Required: []string{"tab_id"}},
		},
		{
			Name:        "session_tabs_messages",
			Description: "Read recent transcript messages from a tab in the current session only. Newest messages are returned first.",
			InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
				"before":              {Type: "string", Description: "Older-page cursor"},
				"include_tool_events": {Type: "boolean", Description: "Include sanitized tool-event summaries when available", Default: false},
				"limit":               {Type: "number", Description: "Max messages to return (default 20, max 100)", Default: 20},
				"tab_id":              {Type: "string", Description: "Tab/thread UUID in the current session only"},
			}, Required: []string{"tab_id"}},
		},
	}
}

func (tr *ToolRegistry) callSessionTabs(ctx context.Context, manager integration.SessionTabManager, name string, args json.RawMessage) *ToolCallResult {
	switch name {
	case "session_tabs_list":
		var p integration.ListSessionTabsParams
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		raw, err := manager.ListTabs(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list session tabs failed: %s", err))
		}
		return TextResult(string(unwrapListResponseData(raw)))
	case "session_tabs_get":
		var p integration.GetSessionTabParams
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		raw, err := manager.GetTab(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("get session tab failed: %s", err))
		}
		return TextResult(string(raw))
	case "session_tabs_create":
		var p integration.CreateSessionTabParams
		if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		raw, err := manager.CreateTab(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("create session tab failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "session_tabs_send":
		var p integration.SendSessionTabMessageParams
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if strings.TrimSpace(p.Message) == "" && strings.TrimSpace(p.MessageFile) == "" {
			return ErrorResult("message is required unless message_file is supplied")
		}
		raw, err := manager.SendTabMessage(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("send session tab message failed: %s", err))
		}
		return TextResult(string(unwrapResponseData(raw)))
	case "session_tabs_messages":
		var p integration.ListSessionTabMessagesParams
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		raw, err := manager.ListTabMessages(ctx, p)
		if err != nil {
			return ErrorResult(fmt.Sprintf("list session tab messages failed: %s", err))
		}
		return TextResult(string(raw))
	default:
		return ErrorResult(fmt.Sprintf("unknown session tab tool: %s", name))
	}
}

func unwrapListResponseData(raw json.RawMessage) json.RawMessage {
	return unwrapResponseData(raw)
}

func unwrapResponseData(raw json.RawMessage) json.RawMessage {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 {
		return raw
	}
	return envelope.Data
}

// --------------------------------------------------------------------------
// Project proposer dispatch
// --------------------------------------------------------------------------

func (tr *ToolRegistry) callProjectProposer(ctx context.Context, pp integration.ProjectProposer, method string, args json.RawMessage) *ToolCallResult {
	switch method {
	case "propose":
		var p struct {
			RepositoryID       string  `json:"repository_id"`
			Title              string  `json:"title"`
			Goal               string  `json:"goal"`
			Scope              *string `json:"scope"`
			CompletionCriteria *string `json:"completion_criteria"`
			Reasoning          string  `json:"reasoning"`
			SourceIssueIDs     string  `json:"source_issue_ids"`
			Priority           int     `json:"priority"`
			Tasks              string  `json:"tasks"`
			SimilarProjectIDs  string  `json:"similar_project_ids"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
		}
		if p.RepositoryID == "" {
			return ErrorResult("repository_id is required")
		}
		if p.Title == "" {
			return ErrorResult("title is required")
		}
		if p.Goal == "" {
			return ErrorResult("goal is required")
		}
		if p.Reasoning == "" {
			return ErrorResult("reasoning is required")
		}

		sourceIssueIDs := splitCommaSeparated(p.SourceIssueIDs)
		similarProjectIDs := splitCommaSeparated(p.SimilarProjectIDs)

		// Parse tasks JSON array.
		var tasks []integration.ProposeProjectTask
		if p.Tasks != "" {
			if err := json.Unmarshal([]byte(p.Tasks), &tasks); err != nil {
				return ErrorResult(fmt.Sprintf("invalid tasks JSON: %s", err))
			}
		}

		priority := p.Priority
		if priority <= 0 {
			priority = 50
		}

		params := integration.ProposeProjectParams{
			RepositoryID:       p.RepositoryID,
			Title:              p.Title,
			Goal:               p.Goal,
			Scope:              p.Scope,
			CompletionCriteria: p.CompletionCriteria,
			Reasoning:          p.Reasoning,
			SourceIssueIDs:     sourceIssueIDs,
			Priority:           priority,
			Tasks:              tasks,
			SimilarProjectIDs:  similarProjectIDs,
		}
		result, err := pp.ProposeProject(ctx, params)
		if err != nil {
			return ErrorResult(fmt.Sprintf("propose project failed: %s", err))
		}
		return jsonResult(result)

	default:
		return ErrorResult(fmt.Sprintf("unknown project proposer method: %s", method))
	}
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// jsonResult marshals v to JSON and wraps it in a ToolCallResult.
func jsonResult(v any) *ToolCallResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to marshal result: %s", err))
	}
	return TextResult(string(data))
}

// splitCommaSeparated splits a comma-separated string into trimmed, non-empty parts.
func splitCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// parseDuration parses human-friendly durations like "24h", "7d", "14d".
// Falls back to defaultDur on parse failure.
func parseDuration(s string, defaultDur time.Duration) time.Duration {
	if s == "" {
		return defaultDur
	}

	// Handle day suffixes (e.g. "7d", "14d") which time.ParseDuration doesn't support.
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}

	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return defaultDur
}
