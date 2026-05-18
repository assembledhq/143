// Package integration defines category-specific interfaces for external data
// sources (error trackers, task managers, document stores, message sources).
//
// These interfaces are consumed by:
//   - The PM agent's context gatherer for enriched issue analysis
//   - MCP servers that expose tools to coding agents at runtime
//   - Static context writers that pre-populate sandbox files
//
// Each interface is designed around the queries that are most useful to a PM
// agent reasoning about what to work on, how to prioritize, and how to
// coordinate across tools.
package integration

import (
	"context"
	"fmt"
	"time"
)

// --------------------------------------------------------------------------
// ErrorTracker — Sentry, Datadog, Bugsnag, etc.
// --------------------------------------------------------------------------

// ErrorTracker provides access to error monitoring systems. The PM agent uses
// this to understand error impact, identify root causes across multiple errors,
// and assess whether errors are trending up or stabilizing.
type ErrorTracker interface {
	// Name returns the provider identifier (e.g. "sentry").
	Name() string

	// ListErrors returns unresolved errors matching the given filter.
	// The PM agent uses this to discover new errors and re-assess known ones.
	ListErrors(ctx context.Context, filter ErrorFilter) ([]ErrorSummary, error)

	// GetError returns full details for a single error, including the stack
	// trace and occurrence timeline. The PM agent uses this to understand
	// root cause and assess customer impact.
	GetError(ctx context.Context, errorID string) (*ErrorDetail, error)

	// GetTrend returns occurrence counts over time for an error. The PM agent
	// uses this to decide urgency — a flat trend is less urgent than a spike.
	GetTrend(ctx context.Context, errorID string, period time.Duration) (*ErrorTrend, error)

	// FindRelated returns errors that likely share a root cause with the given
	// error (e.g. same stack trace prefix, same culprit module). The PM agent
	// uses this to cluster errors and create unified fix tasks.
	FindRelated(ctx context.Context, errorID string) ([]ErrorSummary, error)
}

// ErrorFilter constrains which errors to return from ListErrors.
type ErrorFilter struct {
	ProjectSlug  string    // empty = all projects
	Severity     string    // "critical", "high", "medium", "low"; empty = all
	Since        time.Time // only errors seen after this time
	IsUnresolved bool      // true = only unresolved errors
	Limit        int       // max results; 0 = provider default
}

// ErrorSummary is a compact representation of an error for list views and
// PM-level reasoning. It contains enough to prioritize without fetching details.
type ErrorSummary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Culprit       string    `json:"culprit,omitempty"` // file/function that caused it
	Severity      string    `json:"severity"`          // normalized: critical/high/medium/low
	Occurrences   int       `json:"occurrences"`       // total event count
	AffectedUsers int       `json:"affected_users"`    // unique user count
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	Project       string    `json:"project,omitempty"`       // project slug
	IsRegression  bool      `json:"is_regression,omitempty"` // re-opened after resolve
}

// ErrorDetail is the full representation of an error, including the stack trace
// and rich metadata the PM agent needs for root cause analysis.
type ErrorDetail struct {
	ErrorSummary

	// StackTrace is the parsed, structured stack trace with app frames
	// separated from vendor frames.
	StackTrace *StackTrace `json:"stack_trace,omitempty"`

	// Tags are provider-specific labels (e.g. browser, OS, environment).
	Tags map[string]string `json:"tags,omitempty"`

	// ErrorType is the exception class (e.g. "TypeError", "NullPointerException").
	ErrorType string `json:"error_type,omitempty"`

	// ErrorValue is the exception message.
	ErrorValue string `json:"error_value,omitempty"`

	// WebURL is a link to the error in the provider's UI.
	WebURL string `json:"web_url,omitempty"`
}

// StackTrace is a parsed, structured stack trace.
type StackTrace struct {
	// AppFrames are the frames from the application code (not vendor/stdlib).
	// Ordered most-recent-first.
	AppFrames []StackFrame `json:"app_frames"`

	// Summary is a human-readable condensed form suitable for PM-level analysis
	// (e.g. "TypeError at handlers/auth.go:42 → middleware/session.go:18").
	Summary string `json:"summary"`
}

// StackFrame is a single frame in a stack trace.
type StackFrame struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Context  string `json:"context,omitempty"` // source code snippet if available
}

// ErrorTrend represents how an error's occurrence rate is changing over time.
type ErrorTrend struct {
	ErrorID    string           `json:"error_id"`
	Period     time.Duration    `json:"period"`
	DataPoints []TrendDataPoint `json:"data_points"`

	// Direction is a PM-friendly summary: "increasing", "decreasing", "stable", "spike".
	Direction string `json:"direction"`
}

// TrendDataPoint is a single time-bucketed count.
type TrendDataPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int       `json:"count"`
}

// --------------------------------------------------------------------------
// TaskManager — Linear, Jira, GitHub Issues, etc.
// --------------------------------------------------------------------------

// TaskManager provides access to issue/task tracking systems. The PM agent uses
// this to understand current workload, identify stale tasks, cross-reference
// errors with existing tickets, and manage task lifecycle.
type TaskManager interface {
	// Name returns the provider identifier (e.g. "linear").
	Name() string

	// ListTasks returns tasks matching the given filter.
	// The PM agent uses this to understand the current backlog and workload.
	ListTasks(ctx context.Context, filter TaskFilter) ([]TaskSummary, error)

	// GetTask returns full details for a single task, including comments
	// and linked items. The PM agent uses this to understand context and
	// decide whether to assign work or re-prioritize.
	GetTask(ctx context.Context, taskID string) (*TaskDetail, error)

	// FindRelated returns tasks that are related to the given task (linked
	// issues, sub-issues, or tasks in the same project/epic). The PM agent
	// uses this to identify duplicate work and coordinate fixes.
	FindRelated(ctx context.Context, taskID string) ([]TaskSummary, error)

	// UpdateTask applies a change to a task (re-prioritize, re-label,
	// add comment, change state). The PM agent uses this to keep Linear
	// in sync with its decisions.
	UpdateTask(ctx context.Context, taskID string, update TaskUpdate) error

	// CreateTask creates a new task. The PM agent uses this when it
	// identifies work that doesn't have a corresponding ticket.
	CreateTask(ctx context.Context, spec TaskCreateSpec) (*TaskSummary, error)
}

// TaskFilter constrains which tasks to return from ListTasks.
type TaskFilter struct {
	TeamKey  string    // filter by team (e.g. "ENG")
	States   []string  // e.g. ["triage", "backlog", "in_progress"]
	Priority string    // "urgent", "high", "medium", "low"; empty = all
	Labels   []string  // filter by label names
	Since    time.Time // only tasks updated after this time
	Limit    int       // max results; 0 = provider default
}

// TaskSummary is a compact representation of a task for list views.
type TaskSummary struct {
	ID         string    `json:"id"`         // provider's internal ID
	Identifier string    `json:"identifier"` // human-readable key (e.g. "ENG-123")
	Title      string    `json:"title"`
	State      string    `json:"state"`      // normalized state name
	StateType  string    `json:"state_type"` // triage/backlog/unstarted/started/completed/cancelled
	Priority   string    `json:"priority"`   // normalized: urgent/high/medium/low/none
	Team       string    `json:"team"`       // team name or key
	Labels     []string  `json:"labels,omitempty"`
	Assignee   string    `json:"assignee,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TaskDetail is the full representation of a task with comments and relations.
type TaskDetail struct {
	TaskSummary

	Description  string        `json:"description,omitempty"`
	Comments     []TaskComment `json:"comments,omitempty"`
	LinkedIssues []string      `json:"linked_issues,omitempty"` // IDs of related tasks
	ParentID     string        `json:"parent_id,omitempty"`
	WebURL       string        `json:"web_url,omitempty"`
}

// TaskComment is a single comment on a task.
type TaskComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// TaskUpdate describes a change to apply to an existing task.
type TaskUpdate struct {
	// Only non-nil fields are applied.
	Priority *string `json:"priority,omitempty"` // normalized priority level
	State    *string `json:"state,omitempty"`    // target state name
	Comment  *string `json:"comment,omitempty"`  // comment to add
	Labels   struct {
		Add    []string `json:"add,omitempty"`
		Remove []string `json:"remove,omitempty"`
	} `json:"labels,omitempty"`
}

// TaskCreateSpec describes a new task to create.
type TaskCreateSpec struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	TeamKey     string   `json:"team_key"`
	Priority    string   `json:"priority,omitempty"` // "urgent", "high", "medium", "low"
	Labels      []string `json:"labels,omitempty"`
	ParentID    string   `json:"parent_id,omitempty"`
}

// --------------------------------------------------------------------------
// DocumentStore — Notion, Confluence, Google Docs, etc.
// --------------------------------------------------------------------------

// DocumentStore provides access to documentation and knowledge bases. The PM
// agent uses this to find relevant context docs, architecture decisions, and
// runbooks that inform how issues should be fixed.
type DocumentStore interface {
	// Name returns the provider identifier (e.g. "notion").
	Name() string

	// SearchDocuments finds documents matching a text query.
	SearchDocuments(ctx context.Context, query string, filter DocFilter) ([]DocSummary, error)

	// GetDocument returns the full content of a document.
	GetDocument(ctx context.Context, docID string) (*Document, error)
}

// DocFilter constrains document search results.
type DocFilter struct {
	Workspace string // filter by workspace or space
	Limit     int    // max results; 0 = provider default
}

// DocSummary is a compact representation of a document for search results.
type DocSummary struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Snippet    string    `json:"snippet,omitempty"` // matched text excerpt
	LastEdited time.Time `json:"last_edited"`
	Author     string    `json:"author,omitempty"`
	WebURL     string    `json:"web_url,omitempty"`
}

// Document is the full representation of a document.
type Document struct {
	DocSummary
	Content    string            `json:"content"`              // markdown or plain text
	Properties map[string]string `json:"properties,omitempty"` // custom fields
}

// --------------------------------------------------------------------------
// CodeReviewSource — GitHub, GitLab, Bitbucket, etc.
// --------------------------------------------------------------------------

// CodeReviewSource provides access to code review data from pull/merge requests.
// The PM agent uses this to identify recurring review themes, quality patterns,
// and areas of the codebase that consistently generate review friction.
type CodeReviewSource interface {
	// Name returns the provider identifier (e.g. "github").
	Name() string

	// ListRecentPRs returns recently merged pull requests matching the filter.
	// The PM agent uses this to understand what's shipping and identify review patterns.
	ListRecentPRs(ctx context.Context, filter PRFilter) ([]PRSummary, error)

	// GetPRReviews returns all review comments and review decisions for a PR.
	// The PM agent uses this to extract quality signals and recurring feedback themes.
	GetPRReviews(ctx context.Context, prNumber int) ([]PRReview, error)
}

// PRFilter constrains which PRs to return from ListRecentPRs.
type PRFilter struct {
	State string // "merged", "open", "closed"; empty = "merged"
	Limit int    // max results; 0 = provider default (20)
}

// PRSummary is a compact representation of a pull request for list views.
type PRSummary struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	State        string    `json:"state"`         // "merged", "open", "closed"
	ReviewStatus string    `json:"review_status"` // "has_reviews", "pending" (list endpoint can't distinguish approved/changes_requested)
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changed_files"`
	CreatedAt    time.Time `json:"created_at"`
	MergedAt     time.Time `json:"merged_at,omitempty"`
	WebURL       string    `json:"web_url,omitempty"`
}

// PRReview is a single review or review comment on a pull request.
type PRReview struct {
	Author    string    `json:"author"`
	State     string    `json:"state"` // "APPROVED", "CHANGES_REQUESTED", "COMMENTED"
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Comments are inline review comments attached to specific lines.
	Comments []PRReviewComment `json:"comments,omitempty"`
}

// PRReviewComment is an inline comment attached to a specific file/line in a review.
type PRReviewComment struct {
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Body     string `json:"body"`
	Author   string `json:"author"`
	DiffHunk string `json:"diff_hunk,omitempty"`
}

// StubCodeReviewSource is a no-op CodeReviewSource used only for skills doc
// generation. It registers the tool names without making any HTTP requests.
type StubCodeReviewSource struct {
	ProviderName string
}

func (s *StubCodeReviewSource) Name() string { return s.ProviderName }
func (s *StubCodeReviewSource) ListRecentPRs(_ context.Context, _ PRFilter) ([]PRSummary, error) {
	return nil, fmt.Errorf("stub: use sandbox CLI tools (143-tools %s_list_recent_prs) instead of direct API calls", s.ProviderName)
}
func (s *StubCodeReviewSource) GetPRReviews(_ context.Context, _ int) ([]PRReview, error) {
	return nil, fmt.Errorf("stub: use sandbox CLI tools (143-tools %s_get_pr_reviews) instead of direct API calls", s.ProviderName)
}

// --------------------------------------------------------------------------
// MessageSource — Slack, Discord, Teams, etc.
// --------------------------------------------------------------------------

// MessageSource provides access to team communication channels. The PM agent
// uses this to find discussions about bugs, understand user-reported issues,
// and identify context that didn't make it into formal tickets.
type MessageSource interface {
	// Name returns the provider identifier (e.g. "slack").
	Name() string

	// SearchMessages finds messages matching a text query.
	SearchMessages(ctx context.Context, query string, filter MessageFilter) ([]MessageSummary, error)

	// GetThread returns a full conversation thread given a message ID.
	// The PM agent uses this to understand the full discussion around an issue.
	GetThread(ctx context.Context, messageID string) (*Thread, error)
}

// MessageFilter constrains message search results.
type MessageFilter struct {
	Channel string    // filter by channel name or ID
	Since   time.Time // only messages after this time
	Limit   int       // max results; 0 = provider default
}

// MessageSummary is a compact representation of a message for search results.
type MessageSummary struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	HasThread bool      `json:"has_thread"`
}

// Thread is a full conversation thread.
type Thread struct {
	Messages []MessageSummary `json:"messages"`
	Channel  string           `json:"channel"`
}

// --------------------------------------------------------------------------
// IssueCreator — internal 143 issue creation
// --------------------------------------------------------------------------

// IssueCreator allows agents to create first-class issues in the 143 database.
// This is used by the PM agent when it identifies new work that should be tracked.
type IssueCreator interface {
	// Name returns the provider identifier (e.g. "143").
	Name() string

	// CreateIssue creates a new issue and returns the created issue's ID and title.
	CreateIssue(ctx context.Context, params CreateIssueParams) (*CreateIssueResult, error)
}

// CreateIssueParams describes a new issue to create.
type CreateIssueParams struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    string   `json:"severity,omitempty"` // info, warning, error, critical
	Tags        []string `json:"tags,omitempty"`
}

// CreateIssueResult is returned after successfully creating an issue.
type CreateIssueResult struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	SessionID *string `json:"session_id,omitempty"`
}

// --------------------------------------------------------------------------
// ProjectProposer — internal 143 project proposal creation
// --------------------------------------------------------------------------

// ProjectProposer allows the PM agent to propose new projects.
type ProjectProposer interface {
	// Name returns the provider identifier.
	Name() string

	// ProposeProject creates a new project proposal and returns the result.
	ProposeProject(ctx context.Context, params ProposeProjectParams) (*ProposeProjectResult, error)
}

// ProposeProjectParams describes a new project proposal.
type ProposeProjectParams struct {
	RepositoryID       string               `json:"repository_id"`
	Title              string               `json:"title"`
	Goal               string               `json:"goal"`
	Scope              *string              `json:"scope,omitempty"`
	CompletionCriteria *string              `json:"completion_criteria,omitempty"`
	Reasoning          string               `json:"reasoning"`
	SourceIssueIDs     []string             `json:"source_issue_ids,omitempty"`
	Priority           int                  `json:"priority"`
	Tasks              []ProposeProjectTask `json:"tasks,omitempty"`
	SimilarProjectIDs  []string             `json:"similar_project_ids,omitempty"`
}

// ProposeProjectTask is a seed task included in a proposal.
type ProposeProjectTask struct {
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
	Approach    *string `json:"approach,omitempty"`
	Complexity  *string `json:"complexity,omitempty"`
	Confidence  *string `json:"confidence,omitempty"`
}

// ProposeProjectResult is returned after successfully creating a proposal.
type ProposeProjectResult struct {
	ID               string  `json:"id"`
	DuplicateWarning *string `json:"duplicate_warning,omitempty"`
}

// --------------------------------------------------------------------------
// CITestInsights — CircleCI, GitHub Actions, etc.
// --------------------------------------------------------------------------

// CITestInsights provides access to CI test analytics, especially flaky-test
// detection. A coding agent uses this to discover unreliable tests, look at
// the failure messages for a specific occurrence, and decide on a fix.
type CITestInsights interface {
	// Name returns the provider identifier (e.g. "circleci").
	Name() string

	// ListFlakyTests returns tests the provider flagged as flaky in the
	// recent window. The agent uses this to find candidates to fix.
	ListFlakyTests(ctx context.Context, filter FlakyTestFilter) ([]FlakyTest, error)

	// GetTestResults returns individual test results (passes + failures)
	// for a specific job, so the agent can read the failure message and
	// stack of a flaky test occurrence.
	GetTestResults(ctx context.Context, ref JobRef) ([]TestResult, error)

	// GetRecentFailures returns recent failed test occurrences for a single
	// flaky test (matched by classname + test name). The agent uses this
	// to look at multiple failure messages and identify the root cause.
	GetRecentFailures(ctx context.Context, classname, testName string, limit int) ([]TestResult, error)
}

// FlakyTestFilter constrains which flaky tests to return.
type FlakyTestFilter struct {
	Branch       string // only tests flaking on this branch; empty = default branch
	WorkflowName string // restrict to a specific workflow; empty = all
	Limit        int    // max results; 0 = provider default
}

// FlakyTest is a test the CI provider has identified as flaky based on
// repeated pass/fail flips on the same commit.
type FlakyTest struct {
	// TestName is the individual test function/case name.
	TestName string `json:"test_name"`
	// Classname groups related tests (file path or class name, provider-dependent).
	Classname string `json:"classname,omitempty"`
	// File is the source file containing the test, if the provider knows it.
	File string `json:"file,omitempty"`
	// JobName is the CI job where the flake was observed.
	JobName string `json:"job_name,omitempty"`
	// WorkflowName groups jobs (e.g. "build-and-test").
	WorkflowName string `json:"workflow_name,omitempty"`
	// TimesFlaked is how many times this test has flipped in the window
	// the provider reports on. 0 if not provided.
	TimesFlaked int `json:"times_flaked,omitempty"`
	// LastFailureAt is the most recent observed failure timestamp.
	LastFailureAt time.Time `json:"last_failure_at,omitempty"`
	// LastJob is a reference to the most recent failing job, suitable for
	// passing to GetTestResults.
	LastJob *JobRef `json:"last_job,omitempty"`
}

// JobRef identifies a CI job for fetching test results.
type JobRef struct {
	// JobNumber is the provider's numeric job identifier.
	JobNumber int `json:"job_number"`
	// WebURL is a link to the job in the provider UI, if available.
	WebURL string `json:"web_url,omitempty"`
}

// TestResult is a single test run outcome inside a job.
type TestResult struct {
	TestName  string    `json:"test_name"`
	Classname string    `json:"classname,omitempty"`
	File      string    `json:"file,omitempty"`
	Result    string    `json:"result"` // success, failure, error, skipped
	RunTime   float64   `json:"run_time_seconds,omitempty"`
	Message   string    `json:"message,omitempty"` // failure message / stack
	JobNumber int       `json:"job_number,omitempty"`
	RunAt     time.Time `json:"run_at,omitempty"`
}
