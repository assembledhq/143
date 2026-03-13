package integration

import (
	"testing"
)

func TestLinearTaskManager_Name(t *testing.T) {
	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
	})
	if tm.Name() != "linear" {
		t.Errorf("Name() = %q, want %q", tm.Name(), "linear")
	}
}

func TestLinearTaskManager_DefaultAPIURL(t *testing.T) {
	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
	})
	if tm.apiURL != "https://api.linear.app/graphql" {
		t.Errorf("apiURL = %q, want %q", tm.apiURL, "https://api.linear.app/graphql")
	}
}

func TestLinearTaskManager_CustomAPIURL(t *testing.T) {
	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    "https://linear.example.com/graphql",
	})
	if tm.apiURL != "https://linear.example.com/graphql" {
		t.Errorf("apiURL = %q, want %q", tm.apiURL, "https://linear.example.com/graphql")
	}
}

func TestMapLinearPriorityToString(t *testing.T) {
	tests := []struct {
		priority int
		expected string
	}{
		{0, "none"},
		{1, "urgent"},
		{2, "high"},
		{3, "medium"},
		{4, "low"},
		{5, "none"},
		{-1, "none"},
	}

	for _, tt := range tests {
		got := mapLinearPriorityToString(tt.priority)
		if got != tt.expected {
			t.Errorf("mapLinearPriorityToString(%d) = %q, want %q", tt.priority, got, tt.expected)
		}
	}
}

func TestMapPriorityToLinear(t *testing.T) {
	tests := []struct {
		priority string
		expected int
	}{
		{"urgent", 1},
		{"high", 2},
		{"medium", 3},
		{"low", 4},
		{"none", 0},
		{"", 0},
		{"unknown", 0},
	}

	for _, tt := range tests {
		got := mapPriorityToLinear(tt.priority)
		if got != tt.expected {
			t.Errorf("mapPriorityToLinear(%q) = %d, want %d", tt.priority, got, tt.expected)
		}
	}
}

func TestLinearNodeToSummary(t *testing.T) {
	node := linearIssueNode{
		ID:         "uuid-123",
		Identifier: "ENG-456",
		Title:      "Fix login bug",
		State: struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}{Name: "In Progress", Type: "started"},
		Priority: 2,
		Team: struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		}{Key: "ENG", Name: "Engineering"},
		Assignee: struct {
			Name string `json:"name"`
		}{Name: "Alice"},
		CreatedAt: "2024-01-15T10:30:00Z",
		UpdatedAt: "2024-01-16T14:00:00Z",
	}
	node.Labels.Nodes = []struct {
		Name string `json:"name"`
	}{{Name: "bug"}, {Name: "auth"}}

	summary := linearNodeToSummary(node)

	if summary.ID != "uuid-123" {
		t.Errorf("ID = %q, want %q", summary.ID, "uuid-123")
	}
	if summary.Identifier != "ENG-456" {
		t.Errorf("Identifier = %q, want %q", summary.Identifier, "ENG-456")
	}
	if summary.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", summary.Title, "Fix login bug")
	}
	if summary.State != "In Progress" {
		t.Errorf("State = %q, want %q", summary.State, "In Progress")
	}
	if summary.StateType != "started" {
		t.Errorf("StateType = %q, want %q", summary.StateType, "started")
	}
	if summary.Priority != "high" {
		t.Errorf("Priority = %q, want %q", summary.Priority, "high")
	}
	if summary.Team != "Engineering" {
		t.Errorf("Team = %q, want %q", summary.Team, "Engineering")
	}
	if summary.Assignee != "Alice" {
		t.Errorf("Assignee = %q, want %q", summary.Assignee, "Alice")
	}
	if len(summary.Labels) != 2 {
		t.Fatalf("Labels len = %d, want 2", len(summary.Labels))
	}
	if summary.Labels[0] != "bug" || summary.Labels[1] != "auth" {
		t.Errorf("Labels = %v, want [bug auth]", summary.Labels)
	}
}

func TestLinearNodeToSummary_TeamKeyFallback(t *testing.T) {
	node := linearIssueNode{
		ID:    "uuid-789",
		Title: "Test issue",
		Team: struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		}{Key: "ENG", Name: ""},
	}

	summary := linearNodeToSummary(node)
	if summary.Team != "ENG" {
		t.Errorf("Team = %q, want %q (should fall back to Key)", summary.Team, "ENG")
	}
}
