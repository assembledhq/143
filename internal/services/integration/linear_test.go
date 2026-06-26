package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			require.Contains(t, req.Query, "issue(id: $id)", "GetTask should resolve a task identifier through Linear's direct issue lookup")
			require.NotContains(t, req.Query, "identifier:", "GetTask should not use IssueFilter.identifier because Linear does not expose that filter")
			require.Equal(t, "ENG-123", req.Variables["id"], "GetTask should look up the issue by the provided identifier")
			_, err = w.Write([]byte(`{
				"data": {
					"issue": {
						"id": "linear-issue-id",
						"team": {"id": "team-1"}
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
			require.Contains(t, req.Query, "issue(id: $id)", "UpdateTask should resolve a human-readable task identifier through Linear's direct issue lookup")
			require.NotContains(t, req.Query, "identifier:", "UpdateTask should not use IssueFilter.identifier because Linear does not expose that filter")
			require.Equal(t, "ENG-123", req.Variables["id"], "UpdateTask should look up the task by identifier")
			_, err = w.Write([]byte(`{
				"data": {
					"issue": {
						"id": "linear-issue-id",
						"team": {"id": "team-1"}
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

func TestLinearTaskManager_DoGraphQL_BearerAuth(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Bearer auth header.
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"),
			"doGraphQL should send a Bearer auth header")
		require.Equal(t, "application/json", r.Header.Get("Content-Type"),
			"doGraphQL should set Content-Type to application/json")

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"teams": {
					"nodes": [{"id": "team-1", "key": "ENG", "name": "Engineering"}]
				}
			}
		}`))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	var result struct {
		Data struct {
			Teams struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"data"`
	}

	err := tm.doGraphQL(context.Background(), `query { teams { nodes { id } } }`, nil, &result)
	require.NoError(t, err, "doGraphQL should succeed with Bearer auth")
	require.Len(t, result.Data.Teams.Nodes, 1, "doGraphQL should decode the response")
}

func TestLinearTaskManager_CreateTask_EndToEnd(t *testing.T) {
	t.Parallel()

	type graphqlRequest struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		defer r.Body.Close()

		var req graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")

		switch callCount {
		case 1:
			// Team resolution.
			require.Contains(t, req.Query, "teams(filter:")
			w.Write([]byte(`{
				"data": {
					"teams": {
						"nodes": [{"id": "team-eng"}]
					}
				}
			}`))
		case 2:
			// Issue creation.
			require.Contains(t, req.Query, "issueCreate")
			input := req.Variables["input"].(map[string]interface{})
			require.Equal(t, "Test Task", input["title"])
			require.Equal(t, "team-eng", input["teamId"])
			w.Write([]byte(`{
				"data": {
					"issueCreate": {
						"success": true,
						"issue": {
							"id": "new-id",
							"identifier": "ENG-999",
							"title": "Test Task",
							"state": {"name": "Backlog", "type": "backlog"},
							"priority": 3,
							"team": {"key": "ENG", "name": "Engineering"},
							"labels": {"nodes": []},
							"assignee": {"name": ""},
							"createdAt": "2024-01-15T10:00:00Z",
							"updatedAt": "2024-01-15T10:00:00Z"
						}
					}
				}
			}`))
		}
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	summary, err := tm.CreateTask(context.Background(), TaskCreateSpec{
		Title:   "Test Task",
		TeamKey: "ENG",
	})
	require.NoError(t, err, "CreateTask should succeed with mock server")
	require.Equal(t, "new-id", summary.ID)
	require.Equal(t, "ENG-999", summary.Identifier)
}

func TestLinearTaskManager_UpdateTask_LabelResolution(t *testing.T) {
	t.Parallel()

	type graphqlRequest struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		defer r.Body.Close()

		var req graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err, "Linear test server should decode GraphQL requests")

		w.Header().Set("Content-Type", "application/json")

		switch callCount {
		case 1:
			// Resolve identifier.
			require.Contains(t, req.Query, "issue(id: $id)", "UpdateTask should resolve label changes through Linear's direct issue lookup")
			require.NotContains(t, req.Query, "identifier:", "UpdateTask should not use IssueFilter.identifier because Linear does not expose that filter")
			require.Equal(t, "ENG-100", req.Variables["id"], "UpdateTask should resolve the label update target by issue identifier")
			w.Write([]byte(`{
				"data": {
					"issue": {
						"id": "issue-1",
						"team": {"id": "team-1"}
					}
				}
			}`))
		case 2:
			// Get current labels.
			require.Contains(t, req.Query, "labels")
			w.Write([]byte(`{
				"data": {
					"issue": {
						"labels": {
							"nodes": [
								{"id": "label-bug-id", "name": "bug"},
								{"id": "label-auth-id", "name": "auth"}
							]
						}
					}
				}
			}`))
		case 3:
			// Get team labels to resolve "feature" name to ID.
			require.Contains(t, req.Query, "team(id:")
			w.Write([]byte(`{
				"data": {
					"team": {
						"labels": {
							"nodes": [
								{"id": "label-bug-id", "name": "bug"},
								{"id": "label-feature-id", "name": "feature"},
								{"id": "label-auth-id", "name": "auth"}
							]
						}
					}
				}
			}`))
		case 4:
			// Issue update with label IDs.
			require.Contains(t, req.Query, "issueUpdate")
			input := req.Variables["input"].(map[string]interface{})
			labelIDs := input["labelIds"].([]interface{})
			// Should have: bug (kept) + feature (added) - auth (removed) = 2 labels.
			require.Len(t, labelIDs, 2,
				"label update should compute final set: current + add - remove")

			// Verify the IDs are correct (order may vary).
			ids := make(map[string]bool)
			for _, id := range labelIDs {
				ids[id.(string)] = true
			}
			require.True(t, ids["label-bug-id"], "bug label should be kept")
			require.True(t, ids["label-feature-id"], "feature label should be added")
			require.False(t, ids["label-auth-id"], "auth label should be removed")

			w.Write([]byte(`{
				"data": {
					"issueUpdate": {"success": true}
				}
			}`))
		}
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	update := TaskUpdate{
		Labels: struct {
			Add    []string `json:"add,omitempty"`
			Remove []string `json:"remove,omitempty"`
		}{
			Add:    []string{"feature"},
			Remove: []string{"auth"},
		},
	}
	err := tm.UpdateTask(context.Background(), "ENG-100", update)
	require.NoError(t, err, "UpdateTask with label changes should resolve names to IDs")
	require.Equal(t, 4, callCount, "UpdateTask with labels should make 4 GraphQL calls")
}

func TestLinearTaskManager_UpdateTask_LabelNotFound(t *testing.T) {
	t.Parallel()

	type graphqlRequest struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		defer r.Body.Close()

		var req graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err, "Linear test server should decode GraphQL requests")
		w.Header().Set("Content-Type", "application/json")

		switch callCount {
		case 1:
			// Resolve identifier.
			require.Contains(t, req.Query, "issue(id: $id)",
				"UpdateTask should resolve the label update target by direct issue lookup")
			require.NotContains(t, req.Query, "identifier:",
				"UpdateTask should not use IssueFilter.identifier because Linear does not expose that filter")
			w.Write([]byte(`{
				"data": {
					"issue": {
						"id": "issue-1",
						"team": {"id": "team-1"}
					}
				}
			}`))
		case 2:
			// Get current labels.
			w.Write([]byte(`{
				"data": {
					"issue": {
						"labels": {"nodes": []}
					}
				}
			}`))
		case 3:
			// Get team labels — "nonexistent" label not found.
			w.Write([]byte(`{
				"data": {
					"team": {
						"labels": {
							"nodes": [{"id": "label-bug-id", "name": "bug"}]
						}
					}
				}
			}`))
		}
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	update := TaskUpdate{
		Labels: struct {
			Add    []string `json:"add,omitempty"`
			Remove []string `json:"remove,omitempty"`
		}{
			Add: []string{"nonexistent"},
		},
	}
	err := tm.UpdateTask(context.Background(), "ENG-100", update)
	require.Error(t, err, "UpdateTask should fail when a label name can't be resolved")
	require.Contains(t, err.Error(), `label "nonexistent" not found`,
		"error should indicate which label wasn't found")
}

func TestLinearTaskManager_DoGraphQL_UnauthorizedMapsToErrLinearUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required, not authenticated"}]}`))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "dead-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err, "GetTask should surface a 401 from Linear")
	require.ErrorIs(t, err, ErrLinearUnauthorized,
		"401 from Linear should map to ErrLinearUnauthorized so callers can detect dead tokens with errors.Is")
	require.Contains(t, err.Error(), "Authentication required, not authenticated",
		"401 from Linear should preserve the GraphQL auth detail for CLI and logs")
}

func TestLinearTaskManager_DoGraphQL_RateLimitReturnsTypedError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err, "GetTask should surface a 429 from Linear")
	var rl *LinearRateLimitError
	require.ErrorAs(t, err, &rl,
		"429 from Linear should map to *LinearRateLimitError so retry orchestration can read Retry-After")
	require.Equal(t, "30", rl.RetryAfter, "RateLimitError should preserve the Retry-After header")
}

func TestLinearTaskManager_DoGraphQL_NonOKBodySurfacedInError(t *testing.T) {
	t.Parallel()

	// Linear sometimes returns a 4xx with a GraphQL error envelope explaining
	// why the request was rejected (auth, schema, validation). The transport
	// must read and surface that body so the next 4xx debug doesn't require
	// reproducing the request locally to discover what Linear actually said.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required: invalid token"}]}`))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err, "GetTask should surface a 400 from Linear")
	require.Contains(t, err.Error(), "linear API returned 400",
		"error should keep the status code so log searches by code still work")
	require.Contains(t, err.Error(), "Authentication required: invalid token",
		"error should include the GraphQL message Linear returned in the 4xx body")
}

func TestLinearTaskManager_DoGraphQL_NonOKPlainBodySurfaced(t *testing.T) {
	t.Parallel()

	// Non-GraphQL bodies (HTML error pages from edge proxies, plain-text
	// nginx errors) should still be surfaced verbatim — they're often the
	// only signal that the request never reached Linear's GraphQL handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream connect error"))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err, "GetTask should surface a 502 from Linear")
	require.Contains(t, err.Error(), "linear API returned 502")
	require.Contains(t, err.Error(), "upstream connect error",
		"non-JSON 4xx/5xx bodies should be included verbatim so edge-proxy failures are diagnosable")
}

func TestLinearTaskManager_DoGraphQL_GraphQLErrorIn200Surfaced(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Field 'foo' is not defined"}]}`))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err, "GetTask should fail when Linear returns a GraphQL error envelope")
	require.Contains(t, err.Error(), "linear graphql error",
		"errors[].message in 200 responses should be surfaced via the existing prefix")
	require.Contains(t, err.Error(), "Field 'foo' is not defined")
}

func TestLinearTaskManager_DoGraphQL_LongErrorMessagesAreTruncated(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("X", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"` + long + `"}]}`))
	}))
	defer server.Close()

	tm := NewLinearTaskManager(LinearManagerConfig{
		AuthToken: "test-token",
		APIURL:    server.URL,
	})

	_, err := tm.GetTask(context.Background(), "ENG-123")
	require.Error(t, err)
	require.Contains(t, err.Error(), "[truncated]",
		"oversize Linear error messages should be truncated to bound log line size")
	require.Less(t, len(err.Error()), 1024,
		"truncated error should fit comfortably inside a single log line")
}
