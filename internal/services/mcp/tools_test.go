package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/stretchr/testify/require"
)

// mockErrorTracker is a minimal ErrorTracker for testing tool dispatch.
type mockErrorTracker struct {
	name string
}

func (m *mockErrorTracker) Name() string { return m.name }
func (m *mockErrorTracker) ListErrors(_ context.Context, filter integration.ErrorFilter) ([]integration.ErrorSummary, error) {
	return []integration.ErrorSummary{
		{ID: "1", Title: "test error", Severity: filter.Severity, Occurrences: 42},
	}, nil
}
func (m *mockErrorTracker) GetError(_ context.Context, errorID string) (*integration.ErrorDetail, error) {
	return &integration.ErrorDetail{
		ErrorSummary: integration.ErrorSummary{ID: errorID, Title: "test error detail"},
		ErrorType:    "TypeError",
	}, nil
}
func (m *mockErrorTracker) GetTrend(_ context.Context, errorID string, period time.Duration) (*integration.ErrorTrend, error) {
	return &integration.ErrorTrend{
		ErrorID:   errorID,
		Period:    period,
		Direction: "stable",
	}, nil
}
func (m *mockErrorTracker) FindRelated(_ context.Context, _ string) ([]integration.ErrorSummary, error) {
	return []integration.ErrorSummary{{ID: "2", Title: "related error"}}, nil
}

// mockTaskManager is a minimal TaskManager for testing tool dispatch.
type mockTaskManager struct {
	name string
}

func (m *mockTaskManager) Name() string { return m.name }
func (m *mockTaskManager) ListTasks(_ context.Context, _ integration.TaskFilter) ([]integration.TaskSummary, error) {
	return []integration.TaskSummary{{ID: "t1", Title: "test task", Identifier: "ENG-1"}}, nil
}
func (m *mockTaskManager) GetTask(_ context.Context, taskID string) (*integration.TaskDetail, error) {
	return &integration.TaskDetail{
		TaskSummary: integration.TaskSummary{ID: taskID, Title: "test task detail"},
	}, nil
}
func (m *mockTaskManager) FindRelated(_ context.Context, _ string) ([]integration.TaskSummary, error) {
	return nil, nil
}
func (m *mockTaskManager) UpdateTask(_ context.Context, _ string, _ integration.TaskUpdate) error {
	return nil
}
func (m *mockTaskManager) CreateTask(_ context.Context, spec integration.TaskCreateSpec) (*integration.TaskSummary, error) {
	return &integration.TaskSummary{ID: "t-new", Title: spec.Title, Identifier: "ENG-99"}, nil
}

func buildTestRegistry() *integration.Registry {
	reg := integration.NewRegistry()
	reg.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	reg.RegisterTaskManager(&mockTaskManager{name: "linear"})
	return reg
}

func TestListToolsWithIntegrations(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	tools := tr.ListTools()

	// Should have 4 error tracker tools + 5 task manager tools = 9.
	if len(tools) != 9 {
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 9 tools, got %d: %v", len(tools), names)
	}

	// Verify error tracker tools are prefixed correctly.
	expected := map[string]bool{
		"sentry_list_errors":         false,
		"sentry_get_error":           false,
		"sentry_get_error_trend":     false,
		"sentry_find_related_errors": false,
		"linear_list_tasks":          false,
		"linear_get_task":            false,
		"linear_find_related_tasks":  false,
		"linear_update_task":         false,
		"linear_create_task":         false,
	}
	for _, tool := range tools {
		if _, ok := expected[tool.Name]; !ok {
			t.Errorf("unexpected tool: %s", tool.Name)
		}
		expected[tool.Name] = true
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

func TestListToolsEmptyRegistry(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(integration.NewRegistry())
	tools := tr.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestListToolsCodeReviewDescriptionsUseCapitalizedPullRequest(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	tools := tr.ListTools()

	descriptions := make(map[string]string, len(tools))
	for _, tool := range tools {
		descriptions[tool.Name] = tool.Description
	}

	require.Contains(t, descriptions["github_list_recent_prs"], "Pull Requests", "github_list_recent_prs should describe Pull Requests with capitalized output")
	require.NotContains(t, descriptions["github_list_recent_prs"], "pull requests", "github_list_recent_prs should not describe pull requests in lowercase")
	require.Contains(t, descriptions["github_get_pr_reviews"], "Pull Request", "github_get_pr_reviews should describe Pull Request with capitalized output")
	require.NotContains(t, descriptions["github_get_pr_reviews"], "pull request", "github_get_pr_reviews should not describe pull request in lowercase")
}

func TestListToolsIncludesSessionTabTools(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	tools := tr.ListTools()
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}

	for _, name := range []string{
		"session_tabs_list",
		"session_tabs_get",
		"session_tabs_create",
		"session_tabs_send",
		"session_tabs_messages",
	} {
		require.True(t, names[name], "ListTools should expose %s when session tab manager is registered", name)
	}
}

func TestCallToolErrorTrackerListErrors(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"severity":"high","limit":10}`
	result := tr.CallTool(context.Background(), "sentry_list_errors", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	// Parse the JSON result.
	var errors []integration.ErrorSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &errors); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if errors[0].Severity != "high" {
		t.Errorf("severity = %q, want %q", errors[0].Severity, "high")
	}
}

func TestCallToolErrorTrackerGetError(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"error_id":"123"}`
	result := tr.CallTool(context.Background(), "sentry_get_error", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var detail integration.ErrorDetail
	if err := json.Unmarshal([]byte(result.Content[0].Text), &detail); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if detail.ID != "123" {
		t.Errorf("id = %q, want %q", detail.ID, "123")
	}
	if detail.ErrorType != "TypeError" {
		t.Errorf("error_type = %q, want %q", detail.ErrorType, "TypeError")
	}
}

func TestCallToolTaskManagerCreateTask(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"title":"Fix auth bug","team_key":"ENG","priority":"high"}`
	result := tr.CallTool(context.Background(), "linear_create_task", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var task integration.TaskSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &task); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if task.Title != "Fix auth bug" {
		t.Errorf("title = %q, want %q", task.Title, "Fix auth bug")
	}
}

func TestCallToolUpdateTask(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"task_id":"t1","comment":"Updated by PM agent"}`
	result := tr.CallTool(context.Background(), "linear_update_task", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "updated successfully") {
		t.Errorf("expected success message, got: %s", result.Content[0].Text)
	}
}

func TestCallToolUnknown(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "unknown_tool", json.RawMessage("{}"))

	if !result.IsError {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(result.Content[0].Text, "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %s", result.Content[0].Text)
	}
}

func TestCallToolGetTrend(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"error_id":"456","period":"7d"}`
	result := tr.CallTool(context.Background(), "sentry_get_error_trend", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var trend integration.ErrorTrend
	if err := json.Unmarshal([]byte(result.Content[0].Text), &trend); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if trend.ErrorID != "456" {
		t.Errorf("error_id = %q, want %q", trend.ErrorID, "456")
	}
	if trend.Direction != "stable" {
		t.Errorf("direction = %q, want %q", trend.Direction, "stable")
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"14d", 14 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"", 14 * 24 * time.Hour},        // default
		{"invalid", 14 * 24 * time.Hour}, // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := parseDuration(tt.input, 14*24*time.Hour)
			if got != tt.expected {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSplitCommaSeparated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"empty string", "", nil},
		{"single value", "abc", []string{"abc"}},
		{"multiple values", "a,b,c", []string{"a", "b", "c"}},
		{"with spaces", " a , b , c ", []string{"a", "b", "c"}},
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"only commas", ",,", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitCommaSeparated(tt.input)
			if len(got) == 0 && len(tt.expected) == 0 {
				return
			}
			if len(got) != len(tt.expected) {
				t.Fatalf("got %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

// --------------------------------------------------------------------------
// Additional error tracker dispatch tests
// --------------------------------------------------------------------------

func TestCallToolErrorTrackerListErrors_WithSince(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"severity":"high","since":"2024-01-01T00:00:00Z","limit":10}`
	result := tr.CallTool(context.Background(), "sentry_list_errors", json.RawMessage(args))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestCallToolErrorTrackerListErrors_EmptyArgs(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_list_errors", json.RawMessage("{}"))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestCallToolErrorTrackerFindRelated(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"error_id":"789"}`
	result := tr.CallTool(context.Background(), "sentry_find_related_errors", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var related []integration.ErrorSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &related); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(related) != 1 {
		t.Fatalf("expected 1 related error, got %d", len(related))
	}
}

func TestCallToolErrorTrackerFindRelated_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_find_related_errors", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolErrorTrackerGetError_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_get_error", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolErrorTrackerGetTrend_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_get_error_trend", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolErrorTrackerListErrors_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_list_errors", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolErrorTrackerUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "sentry_unknown_method", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown error tracker method") {
		t.Errorf("expected 'unknown error tracker method', got: %s", result.Content[0].Text)
	}
}

// --------------------------------------------------------------------------
// Additional task manager dispatch tests
// --------------------------------------------------------------------------

func TestCallToolTaskManagerListTasks(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"team":"ENG","states":["backlog"],"limit":5}`
	result := tr.CallTool(context.Background(), "linear_list_tasks", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var tasks []integration.TaskSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &tasks); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
}

func TestCallToolTaskManagerListTasks_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_list_tasks", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolTaskManagerGetTask(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"task_id":"t1"}`
	result := tr.CallTool(context.Background(), "linear_get_task", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var task integration.TaskDetail
	if err := json.Unmarshal([]byte(result.Content[0].Text), &task); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if task.ID != "t1" {
		t.Errorf("id = %q, want %q", task.ID, "t1")
	}
}

func TestCallToolTaskManagerGetTask_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_get_task", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolTaskManagerFindRelated(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	args := `{"task_id":"t1"}`
	result := tr.CallTool(context.Background(), "linear_find_related_tasks", json.RawMessage(args))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestCallToolTaskManagerFindRelated_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_find_related_tasks", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolTaskManagerUpdateTask_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_update_task", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolTaskManagerCreateTask_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_create_task", json.RawMessage(`bad`))
	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolTaskManagerUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	result := tr.CallTool(context.Background(), "linear_unknown_method", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown task manager method") {
		t.Errorf("expected 'unknown task manager method', got: %s", result.Content[0].Text)
	}
}

// unauthorizedTaskManager always returns ErrLinearUnauthorized to exercise the
// dedicated reconnect-Linear path in taskManagerError. We use a fresh mock
// rather than threading an error knob through mockTaskManager — the existing
// mock's "always succeed" contract is what most tests rely on, and the
// auth-error case is narrow enough that a separate mock keeps both readable.
type unauthorizedTaskManager struct{ name string }

func (u *unauthorizedTaskManager) Name() string { return u.name }
func (u *unauthorizedTaskManager) ListTasks(_ context.Context, _ integration.TaskFilter) ([]integration.TaskSummary, error) {
	return nil, fmt.Errorf("%w: Authentication required, not authenticated", integration.ErrLinearUnauthorized)
}
func (u *unauthorizedTaskManager) GetTask(_ context.Context, _ string) (*integration.TaskDetail, error) {
	return nil, fmt.Errorf("%w: Authentication required, not authenticated", integration.ErrLinearUnauthorized)
}
func (u *unauthorizedTaskManager) FindRelated(_ context.Context, _ string) ([]integration.TaskSummary, error) {
	return nil, fmt.Errorf("%w: Authentication required, not authenticated", integration.ErrLinearUnauthorized)
}
func (u *unauthorizedTaskManager) UpdateTask(_ context.Context, _ string, _ integration.TaskUpdate) error {
	return fmt.Errorf("%w: Authentication required, not authenticated", integration.ErrLinearUnauthorized)
}
func (u *unauthorizedTaskManager) CreateTask(_ context.Context, _ integration.TaskCreateSpec) (*integration.TaskSummary, error) {
	return nil, fmt.Errorf("%w: Authentication required, not authenticated", integration.ErrLinearUnauthorized)
}

func TestCallToolTaskManager_UnauthorizedSurfacesReconnectMessage(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterTaskManager(&unauthorizedTaskManager{name: "linear"})
	tr := NewToolRegistry(reg)

	cases := []struct {
		tool string
		args string
	}{
		{"linear_get_task", `{"task_id":"VIR-35"}`},
		{"linear_list_tasks", `{}`},
		{"linear_find_related_tasks", `{"task_id":"VIR-35"}`},
		{"linear_update_task", `{"task_id":"VIR-35"}`},
		{"linear_create_task", `{"title":"x"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			result := tr.CallTool(context.Background(), tc.tool, json.RawMessage(tc.args))
			require.True(t, result.IsError, "%s should return IsError when Linear is unauthorized", tc.tool)
			require.Len(t, result.Content, 1)
			text := result.Content[0].Text
			// Stable, greppable prefix — workers tail sandbox stdout into
			// VictoriaLogs, so this string is what ops will search for to
			// find sessions blocked on a dead Linear token.
			require.Contains(t, text, "linear unauthorized:",
				"unauthorized errors should use the reserved 'linear unauthorized:' prefix so log queries can pick them out")
			require.Contains(t, text, "reconnect linear",
				"unauthorized error should explicitly tell the agent to ask the user to reconnect Linear")
			require.Contains(t, text, "Authentication required, not authenticated",
				"unauthorized error should preserve Linear's auth detail for sandbox CLI output")
		})
	}
}

func TestToolSchemaHasRequiredFields(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	tools := tr.ListTools()

	for _, tool := range tools {
		if tool.InputSchema.Type != "object" {
			t.Errorf("tool %q schema type = %q, want %q", tool.Name, tool.InputSchema.Type, "object")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
	}

	// Check that get_error requires error_id.
	for _, tool := range tools {
		if tool.Name == "sentry_get_error" {
			if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "error_id" {
				t.Errorf("sentry_get_error required = %v, want [error_id]", tool.InputSchema.Required)
			}
		}
	}
}
