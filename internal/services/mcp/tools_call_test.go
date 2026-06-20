package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/integration"
)

// --------------------------------------------------------------------------
// Mock: CodeReviewSource
// --------------------------------------------------------------------------

type mockCodeReviewSource struct {
	name string
}

func (m *mockCodeReviewSource) Name() string { return m.name }

func (m *mockCodeReviewSource) ListRecentPRs(_ context.Context, filter integration.PRFilter) ([]integration.PRSummary, error) {
	return []integration.PRSummary{
		{Number: 42, Title: "Fix auth flow", State: filter.State, Author: "alice"},
	}, nil
}

func (m *mockCodeReviewSource) GetPRReviews(_ context.Context, prNumber int) ([]integration.PRReview, error) {
	return []integration.PRReview{
		{Author: "bob", State: "APPROVED", Body: fmt.Sprintf("Looks good on PR #%d", prNumber)},
	}, nil
}

// --------------------------------------------------------------------------
// Mock: DocumentStore
// --------------------------------------------------------------------------

type mockDocumentStore struct {
	name string
}

func (m *mockDocumentStore) Name() string { return m.name }

func (m *mockDocumentStore) SearchDocuments(_ context.Context, query string, filter integration.DocFilter) ([]integration.DocSummary, error) {
	return []integration.DocSummary{
		{ID: "doc1", Title: "Architecture Guide", Snippet: "matched: " + query},
	}, nil
}

func (m *mockDocumentStore) GetDocument(_ context.Context, docID string) (*integration.Document, error) {
	return &integration.Document{
		DocSummary: integration.DocSummary{ID: docID, Title: "Full Doc"},
		Content:    "# Full document content",
	}, nil
}

// --------------------------------------------------------------------------
// Mock: MessageSource
// --------------------------------------------------------------------------

type mockMessageSource struct {
	name string
}

func (m *mockMessageSource) Name() string { return m.name }

func (m *mockMessageSource) SearchMessages(_ context.Context, query string, filter integration.MessageFilter) ([]integration.MessageSummary, error) {
	return []integration.MessageSummary{
		{ID: "msg1", Channel: filter.Channel, Author: "charlie", Text: "found: " + query},
	}, nil
}

func (m *mockMessageSource) GetThread(_ context.Context, messageID string) (*integration.Thread, error) {
	return &integration.Thread{
		Channel: "general",
		Messages: []integration.MessageSummary{
			{ID: messageID, Author: "charlie", Text: "root message"},
			{ID: "reply1", Author: "dana", Text: "reply"},
		},
	}, nil
}

// --------------------------------------------------------------------------
// Mock: IncidentProvider
// --------------------------------------------------------------------------

type mockIncidentProvider struct {
	name string
}

func (m *mockIncidentProvider) Name() string { return m.name }

func (m *mockIncidentProvider) ListIncidents(_ context.Context, filter integration.IncidentFilter) ([]integration.IncidentSummary, error) {
	return []integration.IncidentSummary{{ID: "PINCIDENT", Status: "triggered", ServiceID: filter.Service, Title: "checkout degraded"}}, nil
}

func (m *mockIncidentProvider) GetIncident(_ context.Context, incidentID string) (*integration.IncidentDetail, error) {
	return &integration.IncidentDetail{IncidentSummary: integration.IncidentSummary{ID: incidentID, Title: "checkout degraded"}}, nil
}

func (m *mockIncidentProvider) AddIncidentNote(_ context.Context, incidentID, note string) (string, error) {
	if incidentID == "" || note == "" {
		return "", fmt.Errorf("incident_id and note are required")
	}
	return "note-1", nil
}

func (m *mockIncidentProvider) ListIncidentNotes(_ context.Context, incidentID string, limit int) ([]integration.IncidentNote, error) {
	return []integration.IncidentNote{{ID: "PNOTE", IncidentID: incidentID, Content: "Investigating", UserName: "Alice"}}, nil
}

func (m *mockIncidentProvider) ListIncidentLogEntries(_ context.Context, incidentID string, limit int) ([]integration.IncidentLogEntry, error) {
	return []integration.IncidentLogEntry{{ID: "PLOG", IncidentID: incidentID, Type: "trigger", Summary: "Triggered"}}, nil
}

func (m *mockIncidentProvider) GetService(_ context.Context, serviceID string) (*integration.IncidentService, error) {
	return &integration.IncidentService{ID: serviceID, Name: "Checkout", EscalationPolicy: "Primary"}, nil
}

func (m *mockIncidentProvider) ListOnCalls(_ context.Context, filter integration.OnCallFilter) ([]integration.OnCall, error) {
	return []integration.OnCall{{UserID: "PUSER", UserName: "Alice", ScheduleID: filter.ScheduleID}}, nil
}

func (m *mockIncidentProvider) FindRelatedIncidents(_ context.Context, incidentID string, days int) ([]integration.IncidentSummary, error) {
	return []integration.IncidentSummary{{ID: "PRELATED", Title: "similar to " + incidentID}}, nil
}

func (m *mockIncidentProvider) CreateIncidentStatusUpdate(_ context.Context, incidentID, body string) error {
	if incidentID == "" || body == "" {
		return fmt.Errorf("incident_id and body are required")
	}
	return nil
}

// --------------------------------------------------------------------------
// Mock: IssueCreator
// --------------------------------------------------------------------------

type mockIssueCreator struct {
	name string
}

func (m *mockIssueCreator) Name() string { return m.name }

func (m *mockIssueCreator) CreateIssue(_ context.Context, params integration.CreateIssueParams) (*integration.CreateIssueResult, error) {
	sid := "session-mock-123"
	return &integration.CreateIssueResult{
		ID:        "issue-mock-456",
		Title:     params.Title,
		SessionID: &sid,
	}, nil
}

// --------------------------------------------------------------------------
// Mock: PullRequestCreator
// --------------------------------------------------------------------------

type mockPullRequestCreator struct {
	name string
}

func (m *mockPullRequestCreator) Name() string { return m.name }

func (m *mockPullRequestCreator) CreatePullRequest(_ context.Context, params integration.CreatePullRequestParams) (*integration.CreatePullRequestResult, error) {
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = "session-from-env"
	}
	return &integration.CreatePullRequestResult{
		Status:    "queued",
		SessionID: sessionID,
	}, nil
}

type mockSessionTabManager struct {
	name string
}

func (m *mockSessionTabManager) Name() string { return m.name }
func (m *mockSessionTabManager) ListTabs(_ context.Context, _ integration.ListSessionTabsParams) (json.RawMessage, error) {
	return json.RawMessage(`{"data":[{"id":"tab-1","label":"Codex"}],"meta":{}}`), nil
}
func (m *mockSessionTabManager) GetTab(_ context.Context, params integration.GetSessionTabParams) (json.RawMessage, error) {
	return json.RawMessage(fmt.Sprintf(`{"data":{"thread":{"id":%q},"recent_files":[]}}`, params.TabID)), nil
}
func (m *mockSessionTabManager) CreateTab(_ context.Context, params integration.CreateSessionTabParams) (json.RawMessage, error) {
	return json.RawMessage(fmt.Sprintf(`{"data":{"id":"tab-new","label":%q}}`, params.Label)), nil
}
func (m *mockSessionTabManager) SendTabMessage(_ context.Context, params integration.SendSessionTabMessageParams) (json.RawMessage, error) {
	return json.RawMessage(fmt.Sprintf(`{"data":{"message":{"id":1,"content":%q},"delivery_state":"pending"}}`, params.Message)), nil
}
func (m *mockSessionTabManager) ListTabMessages(_ context.Context, _ integration.ListSessionTabMessagesParams) (json.RawMessage, error) {
	return json.RawMessage(`{"data":[{"id":1,"role":"assistant","content":"done"}],"meta":{"next_cursor":"0"}}`), nil
}

type mockAutomationGoalImprovementCompleter struct {
	name string
}

func (m *mockAutomationGoalImprovementCompleter) Name() string {
	return m.name
}

func (m *mockAutomationGoalImprovementCompleter) CompleteGoalImprovement(_ context.Context, params integration.CompleteAutomationGoalImprovementParams) (*integration.CompleteAutomationGoalImprovementResult, error) {
	return &integration.CompleteAutomationGoalImprovementResult{
		ImprovementID: params.ImprovementID,
		Status:        "completed",
	}, nil
}

// --------------------------------------------------------------------------
// Mock: CITestInsights
// --------------------------------------------------------------------------

type mockCITestInsights struct {
	name string
}

func (m *mockCITestInsights) Name() string { return m.name }

func (m *mockCITestInsights) ListFlakyTests(_ context.Context, filter integration.FlakyTestFilter) ([]integration.FlakyTest, error) {
	return []integration.FlakyTest{
		{
			TestName:     "TestFlaky",
			Classname:    "pkg/foo",
			JobName:      "build",
			WorkflowName: filter.WorkflowName,
			TimesFlaked:  3,
			LastJob:      &integration.JobRef{JobNumber: 42},
		},
	}, nil
}

func (m *mockCITestInsights) GetTestResults(_ context.Context, ref integration.JobRef) ([]integration.TestResult, error) {
	return []integration.TestResult{
		{
			TestName:  "TestFlaky",
			Classname: "pkg/foo",
			Result:    "failure",
			Message:   "expected true, got false",
			JobNumber: ref.JobNumber,
		},
	}, nil
}

func (m *mockCITestInsights) GetRecentFailures(_ context.Context, _, testName string, _ int) ([]integration.TestResult, error) {
	return []integration.TestResult{
		{TestName: testName, Result: "failure", Message: "failure A"},
		{TestName: testName, Result: "failure", Message: "failure B"},
	}, nil
}

// --------------------------------------------------------------------------
// Helper: build a registry with all integration types
// --------------------------------------------------------------------------

func buildFullTestRegistry() *integration.Registry {
	reg := integration.NewRegistry()
	reg.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	reg.RegisterTaskManager(&mockTaskManager{name: "linear"})
	reg.RegisterIncidentProvider(&mockIncidentProvider{name: "pagerduty"})
	reg.RegisterCodeReviewSource(&mockCodeReviewSource{name: "github"})
	reg.RegisterDocumentStore(&mockDocumentStore{name: "notion"})
	reg.RegisterMessageSource(&mockMessageSource{name: "slack"})
	reg.RegisterIssueCreator(&mockIssueCreator{name: "issue"})
	reg.RegisterPullRequestCreator(&mockPullRequestCreator{name: "session"})
	reg.RegisterSessionTabManager(&mockSessionTabManager{name: "session_tabs"})
	reg.RegisterProjectProposer(&mockProjectProposer{name: "project"})
	reg.RegisterAutomationGoalImprovementCompleter(&mockAutomationGoalImprovementCompleter{name: "automation_goal_improvement"})
	reg.RegisterCITestInsights(&mockCITestInsights{name: "circleci"})
	return reg
}

func TestCallToolCITestInsights_ListFlakyTests(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"workflow_name":"ci","limit":5}`
	result := tr.CallTool(context.Background(), "circleci_list_flaky_tests", json.RawMessage(args))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	var got []integration.FlakyTest
	if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].TestName != "TestFlaky" {
		t.Errorf("unexpected list_flaky_tests result: %+v", got)
	}
}

func TestCallToolPagerDutyIncidentProviderExtendedTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		args     string
		expected string
	}{
		{name: "list notes", tool: "pagerduty_list_notes", args: `{"incident_id":"PINCIDENT","limit":10}`, expected: "Investigating"},
		{name: "list log entries", tool: "pagerduty_list_log_entries", args: `{"incident_id":"PINCIDENT","limit":10}`, expected: "Triggered"},
		{name: "get service", tool: "pagerduty_get_service", args: `{"service_id":"PSVC"}`, expected: "Checkout"},
		{name: "list oncalls", tool: "pagerduty_list_oncalls", args: `{"schedule_id":"PSCHED","limit":10}`, expected: "Alice"},
		{name: "find related", tool: "pagerduty_find_related_incidents", args: `{"incident_id":"PINCIDENT","days":90}`, expected: "PRELATED"},
		{name: "create status update", tool: "pagerduty_create_status_update", args: `{"incident_id":"PINCIDENT","body":"Fix is rolling out"}`, expected: "incident status update created successfully"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewToolRegistry(buildFullTestRegistry())
			result := tr.CallTool(context.Background(), tt.tool, json.RawMessage(tt.args))

			require.False(t, result.IsError, "PagerDuty extended tool should dispatch without error")
			require.Contains(t, result.Content[0].Text, tt.expected, "PagerDuty extended tool should return provider data")
		})
	}
}

func TestCallToolSessionTabsList(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "session_tabs_list", json.RawMessage(`{}`))
	require.False(t, result.IsError, "session_tabs_list should dispatch without error")
	require.JSONEq(t, `[{"id":"tab-1","label":"Codex"}]`, result.Content[0].Text, "session_tabs_list should unwrap the API envelope for CLI output")
}

func TestCallToolSessionTabsCreateUnwrapsCreatedTab(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "session_tabs_create", json.RawMessage(`{"label":"Review"}`))
	require.False(t, result.IsError, "session_tabs_create should dispatch without error")
	require.JSONEq(t, `{"id":"tab-new","label":"Review"}`, result.Content[0].Text, "session_tabs_create should return the created tab JSON without the API envelope")
}

func TestCallToolSessionTabsSendRequiresMessageOrFile(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "session_tabs_send", json.RawMessage(`{"tab_id":"tab-1"}`))
	require.True(t, result.IsError, "session_tabs_send should reject calls without message or message_file")
	require.Contains(t, result.Content[0].Text, "message is required", "session_tabs_send should explain the missing message")
}

func TestCallToolSessionTabsSendUnwrapsResponse(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "session_tabs_send", json.RawMessage(`{"tab_id":"tab-1","message":"run tests"}`))
	require.False(t, result.IsError, "session_tabs_send should dispatch without error")
	require.JSONEq(t, `{"message":{"id":1,"content":"run tests"},"delivery_state":"pending"}`, result.Content[0].Text,
		"session_tabs_send should unwrap the API data envelope so agents access delivery_state directly")
}

func TestCallToolCITestInsights_GetJobTestResults(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "circleci_get_job_test_results", json.RawMessage(`{"job_number":42}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	var got []integration.TestResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Message == "" {
		t.Errorf("expected one result with a failure message: %+v", got)
	}
}

func TestCallToolCITestInsights_GetRecentTestFailures_RequiresName(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "circleci_get_recent_test_failures", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatalf("expected error when test_name is missing")
	}
}

// --------------------------------------------------------------------------
// Tests: CodeReviewSource dispatch (callCodeReviewSource)
// --------------------------------------------------------------------------

func TestCallToolCodeReviewListRecentPRs(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"state":"merged","limit":5}`
	result := tr.CallTool(context.Background(), "github_list_recent_prs", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var prs []integration.PRSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &prs); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].Number != 42 {
		t.Errorf("number = %d, want 42", prs[0].Number)
	}
	if prs[0].State != "merged" {
		t.Errorf("state = %q, want %q", prs[0].State, "merged")
	}
}

func TestCallToolCodeReviewListRecentPRs_EmptyArgs(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "github_list_recent_prs", json.RawMessage("{}"))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestCallToolCodeReviewGetPRReviews(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"pr_number":99}`
	result := tr.CallTool(context.Background(), "github_get_pr_reviews", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var reviews []integration.PRReview
	if err := json.Unmarshal([]byte(result.Content[0].Text), &reviews); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(reviews))
	}
	if reviews[0].State != "APPROVED" {
		t.Errorf("state = %q, want %q", reviews[0].State, "APPROVED")
	}
	if !strings.Contains(reviews[0].Body, "#99") {
		t.Errorf("expected body to reference PR #99, got: %s", reviews[0].Body)
	}
}

func TestCallToolCodeReviewGetPRReviews_InvalidPRNumber(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"pr_number":0}`
	result := tr.CallTool(context.Background(), "github_get_pr_reviews", json.RawMessage(args))

	if !result.IsError {
		t.Fatal("expected error for pr_number=0")
	}
	if !strings.Contains(result.Content[0].Text, "pr_number is required") {
		t.Errorf("expected pr_number validation error, got: %s", result.Content[0].Text)
	}
}

func TestCallToolCodeReviewGetPRReviews_NegativePRNumber(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"pr_number":-1}`
	result := tr.CallTool(context.Background(), "github_get_pr_reviews", json.RawMessage(args))

	if !result.IsError {
		t.Fatal("expected error for negative pr_number")
	}
}

func TestCallToolCodeReviewGetPRReviews_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "github_get_pr_reviews", json.RawMessage("not json"))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(result.Content[0].Text, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in error, got: %s", result.Content[0].Text)
	}
}

func TestCallToolCodeReviewUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "github_unknown_method", json.RawMessage("{}"))

	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown code review source method") {
		t.Errorf("expected 'unknown code review source method' in error, got: %s", result.Content[0].Text)
	}
}

// --------------------------------------------------------------------------
// Tests: DocumentStore dispatch (callDocumentStore)
// --------------------------------------------------------------------------

func TestCallToolDocumentStoreSearchDocuments(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"query":"architecture","workspace":"eng","limit":5}`
	result := tr.CallTool(context.Background(), "notion_search_documents", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var docs []integration.DocSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &docs); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].ID != "doc1" {
		t.Errorf("id = %q, want %q", docs[0].ID, "doc1")
	}
}

func TestCallToolDocumentStoreSearchDocuments_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "notion_search_documents", json.RawMessage("bad"))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolDocumentStoreGetDocument(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"doc_id":"doc-abc"}`
	result := tr.CallTool(context.Background(), "notion_get_document", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var doc integration.Document
	if err := json.Unmarshal([]byte(result.Content[0].Text), &doc); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if doc.ID != "doc-abc" {
		t.Errorf("id = %q, want %q", doc.ID, "doc-abc")
	}
	if doc.Content != "# Full document content" {
		t.Errorf("content = %q, want %q", doc.Content, "# Full document content")
	}
}

func TestCallToolDocumentStoreGetDocument_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "notion_get_document", json.RawMessage("bad"))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolDocumentStoreUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "notion_unknown_method", json.RawMessage("{}"))

	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown document store method") {
		t.Errorf("expected 'unknown document store method' in error, got: %s", result.Content[0].Text)
	}
}

// --------------------------------------------------------------------------
// Tests: MessageSource dispatch (callMessageSource)
// --------------------------------------------------------------------------

func TestCallToolMessageSourceSearchMessages(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"query":"deploy issue","channel":"incidents","limit":5}`
	result := tr.CallTool(context.Background(), "slack_search_messages", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var msgs []integration.MessageSummary
	if err := json.Unmarshal([]byte(result.Content[0].Text), &msgs); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Channel != "incidents" {
		t.Errorf("channel = %q, want %q", msgs[0].Channel, "incidents")
	}
}

func TestCallToolMessageSourceSearchMessages_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "slack_search_messages", json.RawMessage("bad"))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolMessageSourceGetThread(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"message_id":"msg-root"}`
	result := tr.CallTool(context.Background(), "slack_get_thread", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var thread integration.Thread
	if err := json.Unmarshal([]byte(result.Content[0].Text), &thread); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(thread.Messages) != 2 {
		t.Fatalf("expected 2 messages in thread, got %d", len(thread.Messages))
	}
	if thread.Messages[0].ID != "msg-root" {
		t.Errorf("root message id = %q, want %q", thread.Messages[0].ID, "msg-root")
	}
}

func TestCallToolMessageSourceGetThread_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "slack_get_thread", json.RawMessage("bad"))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolMessageSourceUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "slack_unknown_method", json.RawMessage("{}"))

	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown message source method") {
		t.Errorf("expected 'unknown message source method' in error, got: %s", result.Content[0].Text)
	}
}

// --------------------------------------------------------------------------
// Tests: ListTools includes all integration types
// --------------------------------------------------------------------------

func TestListToolsAllIntegrations(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	tools := tr.ListTools()

	// 4 error tracker + 9 incident response + 5 task manager + 2 document store + 2 code review + 2 message source + 1 issue creator + 1 PR creator + 5 session tab tools + 1 automation goal improvement completer + 1 project proposer + 3 ci test insights = 36
	if len(tools) != 36 {
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 36 tools, got %d: %v", len(tools), names)
	}

	expected := map[string]bool{
		"pagerduty_list_incidents":             false,
		"pagerduty_get_incident":               false,
		"pagerduty_list_notes":                 false,
		"pagerduty_list_log_entries":           false,
		"pagerduty_get_service":                false,
		"pagerduty_list_oncalls":               false,
		"pagerduty_find_related_incidents":     false,
		"pagerduty_add_note":                   false,
		"pagerduty_create_status_update":       false,
		"github_list_recent_prs":               false,
		"github_get_pr_reviews":                false,
		"notion_search_documents":              false,
		"notion_get_document":                  false,
		"slack_search_messages":                false,
		"slack_get_thread":                     false,
		"issue_create":                         false,
		"create_pr":                            false,
		"session_tabs_list":                    false,
		"session_tabs_get":                     false,
		"session_tabs_create":                  false,
		"session_tabs_send":                    false,
		"session_tabs_messages":                false,
		"automation_goal_improvement_complete": false,
		"project_propose":                      false,
		"circleci_list_flaky_tests":            false,
		"circleci_get_job_test_results":        false,
		"circleci_get_recent_test_failures":    false,
	}
	for _, tool := range tools {
		if _, ok := expected[tool.Name]; ok {
			expected[tool.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

// --------------------------------------------------------------------------
// Tests: IssueCreator dispatch (callIssueCreator)
// --------------------------------------------------------------------------

func TestCallToolIssueCreatorCreate(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"title":"New bug","description":"Something is broken","severity":"warning","tags":["backend"]}`
	result := tr.CallTool(context.Background(), "issue_create", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp integration.CreateIssueResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if resp.ID != "issue-mock-456" {
		t.Errorf("expected issue ID issue-mock-456, got %s", resp.ID)
	}
	if resp.Title != "New bug" {
		t.Errorf("expected title 'New bug', got %s", resp.Title)
	}
	if resp.SessionID == nil || *resp.SessionID != "session-mock-123" {
		t.Error("expected session ID session-mock-123")
	}
}

func TestCallToolIssueCreatorCreate_MissingTitle(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"title":"","description":"desc"}`
	result := tr.CallTool(context.Background(), "issue_create", json.RawMessage(args))

	if !result.IsError {
		t.Fatal("expected error for missing title")
	}
	if !strings.Contains(result.Content[0].Text, "title is required") {
		t.Errorf("expected 'title is required', got: %s", result.Content[0].Text)
	}
}

func TestCallToolIssueCreatorCreate_MissingDescription(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"title":"bug","description":""}`
	result := tr.CallTool(context.Background(), "issue_create", json.RawMessage(args))

	if !result.IsError {
		t.Fatal("expected error for missing description")
	}
	if !strings.Contains(result.Content[0].Text, "description is required") {
		t.Errorf("expected 'description is required', got: %s", result.Content[0].Text)
	}
}

func TestCallToolIssueCreatorCreate_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "issue_create", json.RawMessage(`{invalid`))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolIssueCreatorUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "issue_delete", json.RawMessage(`{}`))

	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
}

func TestCallToolPullRequestCreatorCreate(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"session_id":"session-123","draft":true,"author_mode":"app"}`
	result := tr.CallTool(context.Background(), "create_pr", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp integration.CreatePullRequestResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if resp.Status != "queued" {
		t.Errorf("expected queued status, got %s", resp.Status)
	}
	if resp.SessionID != "session-123" {
		t.Errorf("expected session id session-123, got %s", resp.SessionID)
	}
}

func TestCallToolPullRequestCreatorCreate_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "create_pr", json.RawMessage(`{bad`))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolPullRequestCreatorCreate_DraftAsString(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())

	for _, tc := range []struct {
		raw   string
		draft bool
	}{
		{`{"session_id":"s1","draft":"true"}`, true},
		{`{"session_id":"s1","draft":"false"}`, false},
	} {
		result := tr.CallTool(context.Background(), "create_pr", json.RawMessage(tc.raw))
		if result.IsError {
			t.Fatalf("draft=%q: unexpected error: %s", tc.raw, result.Content[0].Text)
		}
		var resp integration.CreatePullRequestResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
			t.Fatalf("draft=%q: failed to parse result: %v", tc.raw, err)
		}
		if resp.Status != "queued" {
			t.Errorf("draft=%q: expected queued, got %s", tc.raw, resp.Status)
		}
	}
}

func TestCallToolPullRequestCreatorCreate_InvalidDraft(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "create_pr", json.RawMessage(`{"draft":"maybe"}`))

	if !result.IsError {
		t.Fatal("expected error for invalid draft value")
	}
	if !strings.Contains(result.Content[0].Text, "draft must be true or false") {
		t.Errorf("unexpected error: %s", result.Content[0].Text)
	}
}

// --------------------------------------------------------------------------
// Mock: ProjectProposer
// --------------------------------------------------------------------------

type mockProjectProposer struct {
	name string
}

func (m *mockProjectProposer) Name() string { return m.name }

func (m *mockProjectProposer) ProposeProject(_ context.Context, params integration.ProposeProjectParams) (*integration.ProposeProjectResult, error) {
	var warning *string
	if len(params.SimilarProjectIDs) > 0 {
		w := "similar projects acknowledged"
		warning = &w
	}
	return &integration.ProposeProjectResult{
		ID:               "proj-new-123",
		DuplicateWarning: warning,
	}, nil
}

// --------------------------------------------------------------------------
// Tests: ProjectProposer dispatch (callProjectProposer)
// --------------------------------------------------------------------------

func TestCallToolProjectProposerPropose(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"repository_id":"repo-1","title":"New feature","goal":"Ship it","reasoning":"Users want it"}`
	result := tr.CallTool(context.Background(), "project_propose", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp integration.ProposeProjectResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if resp.ID != "proj-new-123" {
		t.Errorf("id = %q, want %q", resp.ID, "proj-new-123")
	}
}

func TestCallToolProjectProposerPropose_WithOptionalFields(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{
		"repository_id":"repo-1",
		"title":"New feature",
		"goal":"Ship it",
		"reasoning":"Users want it",
		"source_issue_ids":"id1,id2",
		"similar_project_ids":"proj-a, proj-b",
		"priority":80,
		"tasks":"[{\"title\":\"task 1\"}]"
	}`
	result := tr.CallTool(context.Background(), "project_propose", json.RawMessage(args))

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp integration.ProposeProjectResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if resp.DuplicateWarning == nil {
		t.Error("expected duplicate warning for similar_project_ids")
	}
}

func TestCallToolProjectProposerPropose_MissingRequired(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())

	tests := []struct {
		name string
		args string
		want string
	}{
		{"missing repo", `{"title":"t","goal":"g","reasoning":"r"}`, "repository_id is required"},
		{"missing title", `{"repository_id":"r","goal":"g","reasoning":"r"}`, "title is required"},
		{"missing goal", `{"repository_id":"r","title":"t","reasoning":"r"}`, "goal is required"},
		{"missing reasoning", `{"repository_id":"r","title":"t","goal":"g"}`, "reasoning is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tr.CallTool(context.Background(), "project_propose", json.RawMessage(tt.args))
			if !result.IsError {
				t.Fatal("expected error")
			}
			if !strings.Contains(result.Content[0].Text, tt.want) {
				t.Errorf("expected %q in error, got: %s", tt.want, result.Content[0].Text)
			}
		})
	}
}

func TestCallToolProjectProposerPropose_BadJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "project_propose", json.RawMessage(`{bad`))

	if !result.IsError {
		t.Fatal("expected error for bad JSON")
	}
}

func TestCallToolProjectProposerPropose_BadTasksJSON(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	args := `{"repository_id":"r","title":"t","goal":"g","reasoning":"r","tasks":"not-json"}`
	result := tr.CallTool(context.Background(), "project_propose", json.RawMessage(args))

	if !result.IsError {
		t.Fatal("expected error for bad tasks JSON")
	}
	if !strings.Contains(result.Content[0].Text, "invalid tasks JSON") {
		t.Errorf("expected 'invalid tasks JSON' in error, got: %s", result.Content[0].Text)
	}
}

func TestCallToolProjectProposerUnknownMethod(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	result := tr.CallTool(context.Background(), "project_unknown", json.RawMessage(`{}`))

	if !result.IsError {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(result.Content[0].Text, "unknown project proposer method") {
		t.Errorf("expected 'unknown project proposer method' in error, got: %s", result.Content[0].Text)
	}
}

func TestListToolsIncludesProjectProposer(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildFullTestRegistry())
	tools := tr.ListTools()

	found := false
	for _, tool := range tools {
		if tool.Name == "project_propose" {
			found = true
			if len(tool.InputSchema.Required) < 4 {
				t.Errorf("expected at least 4 required fields, got %d", len(tool.InputSchema.Required))
			}
		}
	}
	if !found {
		t.Error("project_propose tool not found in ListTools")
	}
}
