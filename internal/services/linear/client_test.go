package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/stretchr/testify/require"
)

type linearGraphQLRequest struct {
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables"`
}

func newGraphQLClientForTest(t *testing.T, handler func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter)) Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method, "client should use POST for GraphQL requests")
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "client should send bearer token")

		var req linearGraphQLRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "request body should decode as GraphQL JSON")
		handler(t, req, w)
	}))
	t.Cleanup(server.Close)

	return NewClientWithEndpoint("test-token", server.URL)
}

func writeGraphQLResponse(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(payload))
	require.NoError(t, err, "test server should write GraphQL response")
}

func TestNewClientConstructors(t *testing.T) {
	t.Parallel()

	defaultClient, ok := NewClient("token-1").(*graphQLClient)
	require.True(t, ok, "NewClient should return a GraphQL client")
	require.Equal(t, "https://api.linear.app/graphql", defaultClient.apiURL, "NewClient should target the production Linear GraphQL endpoint")
	require.Equal(t, "token-1", defaultClient.token, "NewClient should preserve the access token")
	require.NotNil(t, defaultClient.httpClient, "NewClient should configure an HTTP client")

	endpointClient, ok := NewClientWithEndpoint("token-2", "https://linear.test/graphql").(*graphQLClient)
	require.True(t, ok, "NewClientWithEndpoint should return a GraphQL client")
	require.Equal(t, "https://linear.test/graphql", endpointClient.apiURL, "NewClientWithEndpoint should use the supplied endpoint")
	require.Equal(t, "token-2", endpointClient.token, "NewClientWithEndpoint should preserve the access token")
	require.NotNil(t, endpointClient.httpClient, "NewClientWithEndpoint should configure an HTTP client")
}

func TestGraphQLClientFetchIssue(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "issue(id: $identifier)", "FetchIssue should query by identifier")
		require.Contains(t, string(req.Variables), `"identifier":"ACS-123"`, "FetchIssue should pass the identifier variable")
		writeGraphQLResponse(t, w, `{
			"data": {
				"issue": {
					"id": "issue-1",
					"identifier": "ACS-123",
					"title": "Fix session linking",
					"description": "Full issue body",
					"url": "https://linear.app/acme/issue/ACS-123",
					"state": {"id": "state-1", "name": "In Progress", "type": "started"},
					"priority": 2,
					"assignee": {"name": "Ada"},
					"creator": {"id": "lin-user-1", "name": "Creator User", "email": "creator@example.com"},
					"team": {"id": "team-1", "key": "ACS", "name": "Core", "organization": {"urlKey": "acme"}},
					"comments": {"nodes": [
						{"body": "first", "user": {"name": "Grace"}, "createdAt": "2026-04-27T10:11:12Z"},
						{"body": "bad time is tolerated", "user": {"name": "Linus"}, "createdAt": "not-a-time"}
					]},
					"attachments": {"nodes": [
						{"title": "Spec", "url": "https://example.test/spec", "sourceType": "document", "metadata": {"ignored": true}}
					]}
				}
			}
		}`)
	})

	issue, err := client.FetchIssue(context.Background(), "ACS-123")
	require.NoError(t, err, "FetchIssue should return the parsed issue")
	require.Equal(t, &FetchedIssue{
		ID:            "issue-1",
		Identifier:    "ACS-123",
		Title:         "Fix session linking",
		Description:   "Full issue body",
		URL:           "https://linear.app/acme/issue/ACS-123",
		StateID:       "state-1",
		StateName:     "In Progress",
		StateType:     "started",
		Priority:      "high",
		AssigneeName:  "Ada",
		CreatorID:     "lin-user-1",
		CreatorName:   "Creator User",
		CreatorEmail:  "creator@example.com",
		TeamID:        "team-1",
		TeamKey:       "ACS",
		TeamName:      "Core",
		WorkspaceSlug: "acme",
		Comments: []FetchedComment{
			{Author: "Grace", Body: "first", CreatedAt: time.Date(2026, 4, 27, 10, 11, 12, 0, time.UTC)},
			{Author: "Linus", Body: "bad time is tolerated"},
		},
		Attachments: []FetchedAttachment{{Title: "Spec", URL: "https://example.test/spec", Source: "document"}},
	}, issue, "FetchIssue should normalize Linear's GraphQL shape")
}

func TestGraphQLClientFetchIssueNotFound(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "issue(id: $identifier)", "FetchIssue should query by identifier")
		writeGraphQLResponse(t, w, `{"data":{"issue":null}}`)
	})

	issue, err := client.FetchIssue(context.Background(), "ACS-404")
	require.Error(t, err, "FetchIssue should fail when Linear returns no issue")
	require.Nil(t, issue, "FetchIssue should not return a partial issue on miss")
	require.Contains(t, err.Error(), "ACS-404", "FetchIssue error should include the missing identifier")
}

func TestGraphQLClientListTeamKeys(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "teams { nodes", "ListTeamKeys should request team keys")
		writeGraphQLResponse(t, w, `{
			"data": {
				"viewer": {"organization": {"id": "workspace-1", "urlKey": "acme"}},
				"teams": {"nodes": [
					{"id": "team-1", "key": "ACS", "name": "Core"},
					{"id": "team-2", "key": "ENG", "name": "Engineering"}
				]}
			}
		}`)
	})

	keys, err := client.ListTeamKeys(context.Background())
	require.NoError(t, err, "ListTeamKeys should parse team rows")
	require.Equal(t, []TeamKeyInfo{
		{TeamID: "team-1", Key: "ACS", Name: "Core", WorkspaceID: "workspace-1"},
		{TeamID: "team-2", Key: "ENG", Name: "Engineering", WorkspaceID: "workspace-1"},
	}, keys, "ListTeamKeys should attach the workspace id to every team")
}

func TestGraphQLClientCreateOrUpdateAttachment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		priorID     string
		wantQuery   string
		wantIssueID bool
		wantResult  AttachmentResult
	}{
		{
			name:        "creates attachment when no prior id exists",
			wantQuery:   "attachmentCreate",
			wantIssueID: true,
			wantResult:  AttachmentResult{ID: "attachment-created", URL: "https://linear.app/attachment/created"},
		},
		{
			name:       "updates attachment when prior id exists",
			priorID:    "attachment-existing",
			wantQuery:  "attachmentUpdate",
			wantResult: AttachmentResult{ID: "attachment-updated", URL: "https://linear.app/attachment/updated"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
				require.Contains(t, req.Query, tt.wantQuery, "CreateOrUpdateAttachment should choose the expected mutation")
				require.Contains(t, string(req.Variables), `"title":"143 session"`, "attachment input should include title")
				require.Contains(t, string(req.Variables), `"service":"143"`, "attachment metadata should be encoded into variables")
				if tt.wantIssueID {
					require.Contains(t, string(req.Variables), `"issueId":"issue-1"`, "create should include the target issue")
				} else {
					require.Contains(t, string(req.Variables), `"id":"attachment-existing"`, "update should include the prior attachment id")
				}
				if strings.Contains(req.Query, "attachmentUpdate") {
					writeGraphQLResponse(t, w, `{"data":{"attachmentUpdate":{"success":true,"attachment":{"id":"attachment-updated","url":"https://linear.app/attachment/updated"}}}}`)
					return
				}
				writeGraphQLResponse(t, w, `{"data":{"attachmentCreate":{"success":true,"attachment":{"id":"attachment-created","url":"https://linear.app/attachment/created"}}}}`)
			})

			got, err := client.CreateOrUpdateAttachment(context.Background(), AttachmentWriteInput{
				IssueID:  "issue-1",
				PriorID:  tt.priorID,
				Title:    "143 session",
				Subtitle: "Running",
				URL:      "https://app.test/sessions/session-1",
				IconURL:  "https://app.test/icon.png",
				Metadata: db.LinearAttachmentMetadata{Service: "143", SessionID: "session-1", Primary: true, Outcome: "running"},
			})
			require.NoError(t, err, "CreateOrUpdateAttachment should return the API result")
			require.Equal(t, tt.wantResult, got, "CreateOrUpdateAttachment should map the attachment result")
		})
	}
}

func TestGraphQLClientMutationFailureResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		call     func(client Client) error
	}{
		{
			name:     "attachment create success false is an error",
			response: `{"data":{"attachmentCreate":{"success":false,"attachment":null}}}`,
			call: func(client Client) error {
				_, err := client.CreateOrUpdateAttachment(context.Background(), AttachmentWriteInput{
					IssueID:  "issue-1",
					Title:    "143 session",
					Subtitle: "Running",
					URL:      "https://app.test/sessions/session-1",
					Metadata: db.LinearAttachmentMetadata{Service: "143", SessionID: "session-1", Primary: true, Outcome: "running"},
				})
				return err
			},
		},
		{
			name:     "attachment update missing id is an error",
			response: `{"data":{"attachmentUpdate":{"success":true,"attachment":{"id":"","url":"https://linear.app/attachment/missing"}}}}`,
			call: func(client Client) error {
				_, err := client.CreateOrUpdateAttachment(context.Background(), AttachmentWriteInput{
					IssueID:  "issue-1",
					PriorID:  "attachment-existing",
					Title:    "143 session",
					Subtitle: "Running",
					URL:      "https://app.test/sessions/session-1",
					Metadata: db.LinearAttachmentMetadata{Service: "143", SessionID: "session-1", Primary: true, Outcome: "running"},
				})
				return err
			},
		},
		{
			name:     "comment create success false is an error",
			response: `{"data":{"commentCreate":{"success":false,"comment":null}}}`,
			call: func(client Client) error {
				_, err := client.CreateComment(context.Background(), "issue-1", "body")
				return err
			},
		},
		{
			name:     "comment update success false is an error",
			response: `{"data":{"commentUpdate":{"success":false}}}`,
			call: func(client Client) error {
				return client.UpdateComment(context.Background(), "comment-1", "body")
			},
		},
		{
			name:     "issue update success false is an error",
			response: `{"data":{"issueUpdate":{"success":false}}}`,
			call: func(client Client) error {
				return client.UpdateIssueState(context.Background(), "issue-1", "state-1")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
				writeGraphQLResponse(t, w, tt.response)
			})

			err := tt.call(client)
			require.Error(t, err, "Linear mutation helpers should reject unsuccessful mutation payloads")
		})
	}
}

func TestGraphQLClientCommentAndStateMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(t *testing.T, client Client)
	}{
		{
			name: "creates comment",
			call: func(t *testing.T, client Client) {
				got, err := client.CreateComment(context.Background(), "issue-1", "body")
				require.NoError(t, err, "CreateComment should succeed")
				require.Equal(t, "comment-1", got, "CreateComment should return the comment id")
			},
		},
		{
			name: "updates comment",
			call: func(t *testing.T, client Client) {
				require.NoError(t, client.UpdateComment(context.Background(), "comment-1", "body"), "UpdateComment should succeed")
			},
		},
		{
			name: "updates issue state",
			call: func(t *testing.T, client Client) {
				require.NoError(t, client.UpdateIssueState(context.Background(), "issue-1", "state-1"), "UpdateIssueState should succeed")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
				switch {
				case strings.Contains(req.Query, "commentCreate"):
					require.Contains(t, string(req.Variables), `"issueId":"issue-1"`, "CreateComment should include issue id")
					writeGraphQLResponse(t, w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1"}}}}`)
				case strings.Contains(req.Query, "commentUpdate"):
					require.Contains(t, string(req.Variables), `"id":"comment-1"`, "UpdateComment should include comment id")
					writeGraphQLResponse(t, w, `{"data":{"commentUpdate":{"success":true}}}`)
				case strings.Contains(req.Query, "issueUpdate"):
					require.Contains(t, string(req.Variables), `"stateId":"state-1"`, "UpdateIssueState should include state id")
					writeGraphQLResponse(t, w, `{"data":{"issueUpdate":{"success":true}}}`)
				default:
					t.Fatalf("unexpected mutation query: %s", req.Query)
				}
			})

			tt.call(t, client)
		})
	}
}

func TestGraphQLClientAgentSessionUpdateUsesLinearInteractionShape(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "agentSessionUpdate(id: $agentSessionId, input: $input)", "AgentSessionUpdate should pass the session id separately from the input")
		require.NotContains(t, string(req.Variables), `"id":"as_1"`, "AgentSessionUpdateInput should not contain id")
		require.Contains(t, string(req.Variables), `"agentSessionId":"as_1"`, "AgentSessionUpdate should pass the id variable")
		require.Contains(t, string(req.Variables), `"label":"143 session"`, "external URLs should use Linear's label field")
		require.Contains(t, string(req.Variables), `"url":"https://app.test/sessions/sess_1"`, "external URLs should include the target URL")
		require.Contains(t, string(req.Variables), `"state":"active"`, "AgentSessionUpdate should use Linear's active state vocabulary")
		writeGraphQLResponse(t, w, `{"data":{"agentSessionUpdate":{"success":true}}}`)
	})

	err := client.AgentSessionUpdate(context.Background(), AgentSessionUpdateInput{
		AgentSessionID: "as_1",
		ExternalURLs: []AgentSessionExternalURL{{
			Title: "143 session",
			URL:   "https://app.test/sessions/sess_1",
		}},
		State: "active",
	})
	require.NoError(t, err, "AgentSessionUpdate should accept Linear's successful response")
}

func TestGraphQLClientAgentActivityCreateUsesLinearActionShape(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "agentActivityCreate(input: $input)", "AgentActivityCreate should use Linear's agent activity mutation")
		require.Contains(t, string(req.Variables), `"agentSessionId":"as_1"`, "AgentActivityCreate should pass the target session id")
		require.Contains(t, string(req.Variables), `"type":"action"`, "action activities should declare the action content type")
		require.Contains(t, string(req.Variables), `"action":"Searched"`, "action activities should include Linear's required action field")
		require.Contains(t, string(req.Variables), `"parameter":"San Francisco Weather"`, "action activities should include Linear's required parameter field")
		require.Contains(t, string(req.Variables), `"result":"12C, mostly clear"`, "action activities should include optional result when present")
		require.NotContains(t, string(req.Variables), `"body":"this field is invalid for actions"`, "action activities must not send a body field")
		writeGraphQLResponse(t, w, `{"data":{"agentActivityCreate":{"success":true,"agentActivity":{"id":"act_1"}}}}`)
	})

	got, err := client.AgentActivityCreate(context.Background(), AgentActivityInput{
		AgentSessionID: "as_1",
		Type:           "action",
		Action:         "Searched",
		Parameter:      "San Francisco Weather",
		Result:         "12C, mostly clear",
		Body:           "this field is invalid for actions",
	})
	require.NoError(t, err, "AgentActivityCreate should accept Linear's successful response")
	require.Equal(t, AgentActivityResult{ActivityID: "act_1"}, got, "AgentActivityCreate should return the Linear activity id")
}

func TestGraphQLClientWorkflowStateForType(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "workflowStates", "WorkflowStateForType should query team workflow states")
		require.Contains(t, req.Query, "$teamID: ID!", "WorkflowStateForType should declare teamID with Linear's ID scalar")
		require.Contains(t, string(req.Variables), `"teamID":"team-1"`, "WorkflowStateForType should pass the team id")
		writeGraphQLResponse(t, w, `{
			"data": {"workflowStates": {"nodes": [
				{"id": "backlog-1", "name": "Backlog", "type": "backlog", "position": 1},
				{"id": "started-1", "name": "In Progress", "type": "started", "position": 2},
				{"id": "started-2", "name": "In Review", "type": "started", "position": 3}
			]}}
		}`)
	})

	preferred, err := client.WorkflowStateForType(context.Background(), "team-1", []string{"in review"}, "started")
	require.NoError(t, err, "WorkflowStateForType should find a preferred state")
	require.Equal(t, &WorkflowState{ID: "started-2", Name: "In Review", Type: "started"}, preferred, "WorkflowStateForType should prefer configured names case-insensitively")

	fallback, err := client.WorkflowStateForType(context.Background(), "team-1", []string{"does not exist"}, "started")
	require.NoError(t, err, "WorkflowStateForType should fall back by type")
	require.Equal(t, &WorkflowState{ID: "started-1", Name: "In Progress", Type: "started"}, fallback, "WorkflowStateForType should return the first matching type without a preference hit")

	missing, err := client.WorkflowStateForType(context.Background(), "team-1", nil, "completed")
	require.NoError(t, err, "WorkflowStateForType should not fail when a type is absent")
	require.Nil(t, missing, "WorkflowStateForType should return nil when no state of the type exists")

	_, err = client.WorkflowStateForType(context.Background(), "", nil, "started")
	require.Error(t, err, "WorkflowStateForType should validate required inputs")
}

// TestGraphQLClientWorkflowStateForType_PositionOrderingDeterministic pins
// the no-preferences fallback to "lowest position wins" regardless of the
// order Linear returns the nodes. Without the client-side sort, a session
// start could silently land on "In Review" instead of "In Progress" when
// Linear's API reorders responses (the workflowStates query has no orderBy
// argument, so server-side ordering is implementation-dependent).
func TestGraphQLClientWorkflowStateForType_PositionOrderingDeterministic(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		// Out-of-order on purpose: position-3 first, then position-2.
		writeGraphQLResponse(t, w, `{
			"data": {"workflowStates": {"nodes": [
				{"id": "started-2", "name": "In Review", "type": "started", "position": 3},
				{"id": "started-1", "name": "In Progress", "type": "started", "position": 2},
				{"id": "backlog-1", "name": "Backlog", "type": "backlog", "position": 1}
			]}}
		}`)
	})

	got, err := client.WorkflowStateForType(context.Background(), "team-1", nil, "started")
	require.NoError(t, err, "WorkflowStateForType should not error on unordered nodes")
	require.Equal(t, &WorkflowState{ID: "started-1", Name: "In Progress", Type: "started"}, got, "no-preferences fallback must pick the lowest-position state of the requested type, regardless of API order")
}

func TestGraphQLClientIssueRecentHumanEdits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "history(first: 10)", "IssueRecentHumanEdits should query issue history")
		require.Contains(t, req.Query, "viewer { id }", "IssueRecentHumanEdits should fetch viewer id for self-attribution filtering")
		writeGraphQLResponse(t, w, `{
			"data": {
				"viewer": {"id": "viewer-self"},
				"issue": {"history": {"nodes": [
					{"createdAt": "2026-04-27T11:45:00Z", "actor": null, "botActor": {"name": "GitHub"}, "fromState": {"id": "a"}, "toState": {"id": "b"}},
					{"createdAt": "2026-04-27T11:50:00Z", "actor": {"id": "actor-ada", "name": "Ada", "displayName": "Ada"}, "botActor": null, "fromState": {"id": "b"}, "toState": {"id": "c"}},
					{"createdAt": "2026-04-27T11:55:00Z", "actor": {"id": "actor-ignored", "name": "Ignored"}, "botActor": null, "fromState": null, "toState": {"id": "d"}}
				]}}
			}
		}`)
	})

	edited, err := client.IssueRecentHumanEdits(context.Background(), "issue-1", now.Add(-20*time.Minute))
	require.NoError(t, err, "IssueRecentHumanEdits should parse history")
	require.True(t, edited, "IssueRecentHumanEdits should detect recent human state transitions")
}

func TestGraphQLClientIssueRecentHumanEditsIgnoresSelfActor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "viewer { id }", "IssueRecentHumanEdits should fetch viewer id for self-attribution filtering")
		// Linear attributes OAuth-driven moves to the authorizing user with
		// botActor=null. The only state transition in the window is one
		// 143 itself made — IssueRecentHumanEdits must NOT flag this as a
		// human edit, otherwise the next milestone (e.g. PR-open after
		// session-start) gets suppressed within the 10-min cooldown.
		writeGraphQLResponse(t, w, `{
			"data": {
				"viewer": {"id": "viewer-self"},
				"issue": {"history": {"nodes": [
					{"createdAt": "2026-04-27T11:55:00Z", "actor": {"id": "viewer-self", "name": "John Doe", "displayName": "John"}, "botActor": null, "fromState": {"id": "a"}, "toState": {"id": "b"}}
				]}}
			}
		}`)
	})

	edited, err := client.IssueRecentHumanEdits(context.Background(), "issue-1", now.Add(-20*time.Minute))
	require.NoError(t, err, "IssueRecentHumanEdits should parse history")
	require.False(t, edited, "IssueRecentHumanEdits should ignore transitions performed by our own OAuth actor")
}

func TestGraphQLClientIssueRecentHumanEditsNoIssue(t *testing.T) {
	t.Parallel()

	client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
		require.Contains(t, req.Query, "history(first: 10)", "IssueRecentHumanEdits should query issue history")
		writeGraphQLResponse(t, w, `{"data":{"viewer":{"id":"viewer-self"},"issue":null}}`)
	})

	edited, err := client.IssueRecentHumanEdits(context.Background(), "issue-1", time.Now())
	require.NoError(t, err, "IssueRecentHumanEdits should tolerate missing issues")
	require.False(t, edited, "IssueRecentHumanEdits should return false for missing issues")
}

func TestGraphQLClientHasGitHubIntegrationAttachment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "detects native GitHub attachment",
			body: `{"data":{"issue":{"attachments":{"nodes":[
				{"sourceType":"github","metadata":{"service":"github"}},
				{"sourceType":"manual","metadata":{"service":"143"}}
			]}}}}`,
			want: true,
		},
		{
			name: "ignores our own attachment",
			body: `{"data":{"issue":{"attachments":{"nodes":[
				{"sourceType":"github","metadata":{"service":"143"}}
			]}}}}`,
			want: false,
		},
		{
			name: "missing issue is false",
			body: `{"data":{"issue":null}}`,
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newGraphQLClientForTest(t, func(t *testing.T, req linearGraphQLRequest, w http.ResponseWriter) {
				require.Contains(t, req.Query, "attachments(first: 20)", "HasGitHubIntegrationAttachment should query attachments")
				writeGraphQLResponse(t, w, tt.body)
			})

			got, err := client.HasGitHubIntegrationAttachment(context.Background(), "issue-1")
			require.NoError(t, err, "HasGitHubIntegrationAttachment should parse attachment rows")
			require.Equal(t, tt.want, got, "HasGitHubIntegrationAttachment should return the expected coexistence signal")
		})
	}
}

func TestGraphQLClientTransportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		assertErr  func(t *testing.T, err error)
	}{
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			assertErr: func(t *testing.T, err error) {
				var rate *RateLimitError
				require.ErrorAs(t, err, &rate, "429 responses should map to RateLimitError")
			},
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrUnauthorized, "401 responses should map to ErrUnauthorized")
			},
		},
		{
			name:       "non ok",
			statusCode: http.StatusBadGateway,
			assertErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "502", "non-OK errors should include the HTTP status")
			},
		},
		{
			name:       "invalid json",
			statusCode: http.StatusOK,
			body:       `{bad json`,
			assertErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "decode linear response", "invalid JSON should be wrapped")
			},
		},
		{
			name:       "graphql error",
			statusCode: http.StatusOK,
			body:       `{"errors":[{"message":"bad query"}]}`,
			assertErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "bad query", "GraphQL errors should surface their message")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.statusCode == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "45")
				}
				w.WriteHeader(tt.statusCode)
				if tt.body != "" {
					_, err := w.Write([]byte(tt.body))
					require.NoError(t, err, "test server should write error response")
				}
			}))
			t.Cleanup(server.Close)

			client := NewClientWithEndpoint("test-token", server.URL)
			_, err := client.ListTeamKeys(context.Background())
			require.Error(t, err, "transport error case should fail")
			tt.assertErr(t, err)
		})
	}
}

func TestRateLimitErrorAndPriorityMapping(t *testing.T) {
	t.Parallel()

	require.Equal(t, "linear rate limit exceeded (retry-after=15)", (&RateLimitError{RetryAfter: "15"}).Error(), "RateLimitError should include retry hint")
	require.Equal(t, "linear rate limit exceeded", (&RateLimitError{}).Error(), "RateLimitError should omit empty retry hint")

	tests := []struct {
		priority int
		expected string
	}{
		{priority: 1, expected: "urgent"},
		{priority: 2, expected: "high"},
		{priority: 3, expected: "medium"},
		{priority: 4, expected: "low"},
		{priority: 0, expected: "none"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.expected, mapLinearPriorityName(tt.priority), "mapLinearPriorityName should map Linear's numeric priority")
	}
}

func TestGraphQLClientBuildRequestError(t *testing.T) {
	t.Parallel()

	client := &graphQLClient{httpClient: http.DefaultClient, apiURL: "://bad-url", token: "test-token"}
	err := client.do(context.Background(), "query { viewer { id } }", nil, &struct{}{})
	require.Error(t, err, "do should return URL construction errors")
}

func TestGraphQLClientNetworkError(t *testing.T) {
	t.Parallel()

	client := &graphQLClient{httpClient: &http.Client{Timeout: time.Nanosecond}, apiURL: "http://127.0.0.1:1/graphql", token: "test-token"}
	err := client.do(context.Background(), "query { viewer { id } }", nil, &struct{}{})
	require.Error(t, err, "do should return HTTP client errors")
	require.False(t, errors.Is(err, ErrUnauthorized), "network errors should not be confused with auth errors")
}
