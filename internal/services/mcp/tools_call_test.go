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
// Helper: build a registry with all integration types
// --------------------------------------------------------------------------

func buildFullTestRegistry() *integration.Registry {
	reg := integration.NewRegistry()
	reg.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	reg.RegisterTaskManager(&mockTaskManager{name: "linear"})
	reg.RegisterCodeReviewSource(&mockCodeReviewSource{name: "github"})
	reg.RegisterDocumentStore(&mockDocumentStore{name: "notion"})
	reg.RegisterMessageSource(&mockMessageSource{name: "slack"})
	return reg
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

	// 4 error tracker + 5 task manager + 2 document store + 2 code review + 2 message source = 15
	if len(tools) != 15 {
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 15 tools, got %d: %v", len(tools), names)
	}

	expected := map[string]bool{
		"github_list_recent_prs": false,
		"github_get_pr_reviews":  false,
		"notion_search_documents": false,
		"notion_get_document":     false,
		"slack_search_messages":   false,
		"slack_get_thread":        false,
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
