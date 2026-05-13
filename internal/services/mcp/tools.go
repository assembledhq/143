package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/integration"
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

// ToolRegistry builds MCP tool definitions from an integration registry and
// dispatches tool calls to the appropriate integration method.
type ToolRegistry struct {
	integrations *integration.Registry
}

// NewToolRegistry creates a ToolRegistry backed by the given integration registry.
func NewToolRegistry(reg *integration.Registry) *ToolRegistry {
	return &ToolRegistry{integrations: reg}
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

	return tools
}

// CallTool dispatches a tool call to the appropriate integration method.
func (tr *ToolRegistry) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
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
			return ErrorResult(fmt.Sprintf("list flaky tests failed: %s", err))
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
			return ErrorResult(fmt.Sprintf("get job test results failed: %s", err))
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
			return ErrorResult(fmt.Sprintf("get recent test failures failed: %s", err))
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
