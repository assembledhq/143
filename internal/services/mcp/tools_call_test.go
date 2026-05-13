package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
	reg.RegisterCodeReviewSource(&mockCodeReviewSource{name: "github"})
	reg.RegisterDocumentStore(&mockDocumentStore{name: "notion"})
	reg.RegisterMessageSource(&mockMessageSource{name: "slack"})
	reg.RegisterIssueCreator(&mockIssueCreator{name: "issue"})
	reg.RegisterProjectProposer(&mockProjectProposer{name: "project"})
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

	// 4 error tracker + 5 task manager + 2 document store + 2 code review + 2 message source + 1 issue creator + 1 project proposer + 3 ci test insights = 20
	if len(tools) != 20 {
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 20 tools, got %d: %v", len(tools), names)
	}

	expected := map[string]bool{
		"github_list_recent_prs":             false,
		"github_get_pr_reviews":              false,
		"notion_search_documents":            false,
		"notion_get_document":                false,
		"slack_search_messages":              false,
		"slack_get_thread":                   false,
		"issue_create":                       false,
		"project_propose":                    false,
		"circleci_list_flaky_tests":          false,
		"circleci_get_job_test_results":      false,
		"circleci_get_recent_test_failures":  false,
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
