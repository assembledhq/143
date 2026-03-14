package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinearTaskManager_Name(t *testing.T) {
	t.Parallel()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
	})
	require.Equal(t, "linear", tm.Name(), "Name should return the Linear provider name")
}

func TestLinearTaskManager_DefaultAPIURL(t *testing.T) {
	t.Parallel()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
	})
	require.Equal(t, "https://api.linear.app/graphql", tm.apiURL, "NewLinearTaskManager should default to the Linear GraphQL API URL")
}

func TestLinearTaskManager_CustomAPIURL(t *testing.T) {
	t.Parallel()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    "https://linear.example.com/graphql",
	})
	require.Equal(t, "https://linear.example.com/graphql", tm.apiURL, "NewLinearTaskManager should preserve a custom API URL")
}

func TestMapLinearPriorityToString(t *testing.T) {
	t.Parallel()

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
		tt := tt
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			got := mapLinearPriorityToString(tt.priority)
			require.Equal(t, tt.expected, got, "mapLinearPriorityToString should map Linear priorities to normalized strings")
		})
	}
}

func TestMapPriorityToLinear(t *testing.T) {
	t.Parallel()

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
		tt := tt
		t.Run(tt.priority, func(t *testing.T) {
			t.Parallel()

			got := mapPriorityToLinear(tt.priority)
			require.Equal(t, tt.expected, got, "mapPriorityToLinear should map normalized priorities to Linear codes")
		})
	}
}

func TestLinearNodeToSummary(t *testing.T) {
	t.Parallel()

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

	require.Equal(t, "uuid-123", summary.ID, "linearNodeToSummary should preserve the internal issue ID")
	require.Equal(t, "ENG-456", summary.Identifier, "linearNodeToSummary should preserve the human-readable identifier")
	require.Equal(t, "Fix login bug", summary.Title, "linearNodeToSummary should preserve the issue title")
	require.Equal(t, "In Progress", summary.State, "linearNodeToSummary should preserve the state name")
	require.Equal(t, "started", summary.StateType, "linearNodeToSummary should preserve the state type")
	require.Equal(t, "high", summary.Priority, "linearNodeToSummary should normalize the priority code")
	require.Equal(t, "Engineering", summary.Team, "linearNodeToSummary should prefer the team name")
	require.Equal(t, "Alice", summary.Assignee, "linearNodeToSummary should preserve the assignee name")
	require.Equal(t, []string{"bug", "auth"}, summary.Labels, "linearNodeToSummary should flatten label names")
}

func TestLinearNodeToSummary_TeamKeyFallback(t *testing.T) {
	t.Parallel()

	node := linearIssueNode{
		ID:    "uuid-789",
		Title: "Test issue",
		Team: struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		}{Key: "ENG", Name: ""},
	}

	summary := linearNodeToSummary(node)
	require.Equal(t, "ENG", summary.Team, "linearNodeToSummary should fall back to the team key when the team name is empty")
}

func TestLinearTaskManager_GetTaskResolvesIdentifier(t *testing.T) {
	t.Parallel()

	type graphqlRequest struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}

	requests := make([]graphqlRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err, "Linear test server should decode GraphQL requests")
		requests = append(requests, req)

		switch len(requests) {
		case 1:
			require.Contains(t, req.Query, "issues(filter:", "GetTask should resolve a task identifier before loading the issue")
			require.Equal(t, "ENG-123", req.Variables["identifier"], "GetTask should look up the issue by the provided identifier")
			_, err = w.Write([]byte(`{
				"data": {
					"issues": {
						"nodes": [
							{"id": "linear-issue-id"}
						]
					}
				}
			}`))
			require.NoError(t, err, "Linear test server should write the identifier lookup response")
		case 2:
			require.Contains(t, req.Query, "issue(id: $id)", "GetTask should fetch the full issue by internal ID after resolving the identifier")
			require.Equal(t, "linear-issue-id", req.Variables["id"], "GetTask should fetch the resolved internal issue ID")
			_, err = w.Write([]byte(`{
				"data": {
					"issue": {
						"id": "linear-issue-id",
						"identifier": "ENG-123",
						"title": "Fix login bug",
						"description": "Investigate auth failures",
						"state": {"name": "In Progress", "type": "started"},
						"priority": 2,
						"team": {"key": "ENG", "name": "Engineering"},
						"labels": {"nodes": [{"name": "bug"}]},
						"assignee": {"name": "Alice"},
						"createdAt": "2024-01-15T10:30:00Z",
						"updatedAt": "2024-01-16T14:00:00Z",
						"url": "https://linear.app/issue/ENG-123",
						"parent": {"id": "parent-1", "identifier": "ENG-100"},
						"relations": {"nodes": [{"relatedIssue": {"id": "rel-1", "identifier": "ENG-200", "title": "Related"}}]},
						"comments": {"nodes": [{"body": "Looking into it", "user": {"name": "Alice"}, "createdAt": "2024-01-16T12:00:00Z"}]}
					}
				}
			}`))
			require.NoError(t, err, "Linear test server should write the issue details response")
		default:
			t.Fatalf("GetTask should only make two GraphQL calls, got %d", len(requests))
		}
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	detail, err := tm.GetTask(context.Background(), "ENG-123")
	require.NoError(t, err, "GetTask should resolve task identifiers and return the task details")
	require.Len(t, requests, 2, "GetTask should resolve the task identifier before fetching details")
	require.Equal(t, "linear-issue-id", detail.ID, "GetTask should preserve the resolved internal ID in the returned task")
	require.Equal(t, "ENG-123", detail.Identifier, "GetTask should preserve the human-readable identifier in the returned task")
}

func TestLinearTaskManager_UpdateTaskResolvesIdentifierAndState(t *testing.T) {
	t.Parallel()

	type graphqlRequest struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}

	requests := make([]graphqlRequest, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err, "Linear test server should decode GraphQL requests")
		requests = append(requests, req)

		switch len(requests) {
		case 1:
			require.Contains(t, req.Query, "issues(filter:", "UpdateTask should resolve a human-readable task identifier before mutating the issue")
			require.Equal(t, "ENG-123", req.Variables["identifier"], "UpdateTask should look up the task by identifier")
			_, err = w.Write([]byte(`{
				"data": {
					"issues": {
						"nodes": [
							{"id": "linear-issue-id", "team": {"id": "team-1"}}
						]
					}
				}
			}`))
			require.NoError(t, err, "Linear test server should write the issue lookup response")
		case 2:
			require.Contains(t, req.Query, "workflowStates", "UpdateTask should resolve the requested state name to a workflow state ID")
			require.Equal(t, "team-1", req.Variables["teamID"], "UpdateTask should look up workflow states for the issue team")
			require.Equal(t, "Done", req.Variables["stateName"], "UpdateTask should look up the requested workflow state by name")
			_, err = w.Write([]byte(`{
				"data": {
					"workflowStates": {
						"nodes": [
							{"id": "state-done"}
						]
					}
				}
			}`))
			require.NoError(t, err, "Linear test server should write the workflow state lookup response")
		case 3:
			require.Contains(t, req.Query, "issueUpdate", "UpdateTask should submit an issue update after resolving identifiers")
			require.Equal(t, "linear-issue-id", req.Variables["id"], "UpdateTask should mutate the resolved internal issue ID")
			input, ok := req.Variables["input"].(map[string]interface{})
			require.True(t, ok, "UpdateTask should send an input object to issueUpdate")
			require.Equal(t, "state-done", input["stateId"], "UpdateTask should send the resolved workflow state ID instead of the state name")
			_, err = w.Write([]byte(`{
				"data": {
					"issueUpdate": {
						"success": true
					}
				}
			}`))
			require.NoError(t, err, "Linear test server should write the issue update response")
		default:
			t.Fatalf("UpdateTask should only make three GraphQL calls, got %d", len(requests))
		}
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})
	state := "Done"

	err := tm.UpdateTask(context.Background(), "ENG-123", TaskUpdate{State: &state})
	require.NoError(t, err, "UpdateTask should resolve both task identifiers and workflow state names before mutating Linear")
	require.Len(t, requests, 3, "UpdateTask should resolve the task and state before issuing the update mutation")
}
