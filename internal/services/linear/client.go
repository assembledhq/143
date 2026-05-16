package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// graphQLClient is a small Linear GraphQL client that owns the API surface
// the linker service needs. We deliberately keep this separate from
// integration.LinearTaskManager because the linker has different needs:
// attachmentCreate/Update, single-comment lifecycle, workflow state lookup
// by type with name preferences, recent-edits introspection, and coexistence
// detection.
type graphQLClient struct {
	httpClient *http.Client
	apiURL     string
	token      string
}

// NewClient builds a Client backed by the production Linear GraphQL API.
// Suitable for ClientFactory wiring.
func NewClient(token string) Client {
	return &graphQLClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiURL:     "https://api.linear.app/graphql",
		token:      token,
	}
}

// NewClientWithEndpoint is for tests that want to point at a fake server.
func NewClientWithEndpoint(token, endpoint string) Client {
	return &graphQLClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiURL:     endpoint,
		token:      token,
	}
}

// FetchIssue resolves an identifier (e.g. "ACS-1234") to the full Linear
// issue payload needed by the linker. Returns workspace slug, comments,
// attachments — everything the prompt builder downstream wants.
func (c *graphQLClient) FetchIssue(ctx context.Context, identifier string) (*FetchedIssue, error) {
	query := `query($identifier: String!) {
		issue(id: $identifier) {
			id identifier title description url
			state { id name type }
			priority
			assignee { name }
			team { id key name organization { urlKey } }
			project { id }
			labels(first: 50) {
				nodes { name }
			}
			comments(first: 25) {
				nodes { body user { name } createdAt }
			}
			attachments(first: 10) {
				nodes { title url metadata sourceType }
			}
		}
	}`
	var result struct {
		Data struct {
			Issue *struct {
				ID          string `json:"id"`
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				Description string `json:"description"`
				URL         string `json:"url"`
				State       struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"state"`
				Priority int `json:"priority"`
				Assignee struct {
					Name string `json:"name"`
				} `json:"assignee"`
				Team struct {
					ID           string `json:"id"`
					Key          string `json:"key"`
					Name         string `json:"name"`
					Organization struct {
						URLKey string `json:"urlKey"`
					} `json:"organization"`
				} `json:"team"`
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
				Labels struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
				Comments struct {
					Nodes []struct {
						Body string `json:"body"`
						User struct {
							Name string `json:"name"`
						} `json:"user"`
						CreatedAt string `json:"createdAt"`
					} `json:"nodes"`
				} `json:"comments"`
				Attachments struct {
					Nodes []struct {
						Title      string         `json:"title"`
						URL        string         `json:"url"`
						SourceType string         `json:"sourceType"`
						Metadata   map[string]any `json:"metadata"`
					} `json:"nodes"`
				} `json:"attachments"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"identifier": identifier}, &result); err != nil {
		return nil, err
	}
	if result.Data.Issue == nil {
		return nil, fmt.Errorf("linear issue %q not found", identifier)
	}
	issue := result.Data.Issue

	comments := make([]FetchedComment, 0, len(issue.Comments.Nodes))
	for _, c := range issue.Comments.Nodes {
		ts, _ := time.Parse(time.RFC3339, c.CreatedAt)
		comments = append(comments, FetchedComment{
			Author:    c.User.Name,
			Body:      c.Body,
			CreatedAt: ts,
		})
	}
	attachments := make([]FetchedAttachment, 0, len(issue.Attachments.Nodes))
	for _, a := range issue.Attachments.Nodes {
		attachments = append(attachments, FetchedAttachment{
			Title:  a.Title,
			URL:    a.URL,
			Source: a.SourceType,
		})
	}
	// Stay nil when no labels are present so existing test expectations
	// and JSON consumers see the same `omitempty` behavior the rest of
	// FetchedIssue follows.
	var labels []string
	if len(issue.Labels.Nodes) > 0 {
		labels = make([]string, 0, len(issue.Labels.Nodes))
		for _, l := range issue.Labels.Nodes {
			if l.Name != "" {
				labels = append(labels, l.Name)
			}
		}
	}
	projectID := ""
	if issue.Project != nil {
		projectID = issue.Project.ID
	}

	return &FetchedIssue{
		ID:            issue.ID,
		Identifier:    issue.Identifier,
		Title:         issue.Title,
		Description:   issue.Description,
		URL:           issue.URL,
		StateID:       issue.State.ID,
		StateName:     issue.State.Name,
		StateType:     issue.State.Type,
		Priority:      mapLinearPriorityName(issue.Priority),
		AssigneeName:  issue.Assignee.Name,
		TeamID:        issue.Team.ID,
		TeamKey:       issue.Team.Key,
		TeamName:      issue.Team.Name,
		WorkspaceSlug: issue.Team.Organization.URLKey,
		ProjectID:     projectID,
		Labels:        labels,
		Comments:      comments,
		Attachments:   attachments,
	}, nil
}

func (c *graphQLClient) ListTeamKeys(ctx context.Context) ([]TeamKeyInfo, error) {
	query := `query {
		viewer { organization { id urlKey } }
		teams { nodes { id key name } }
	}`
	var result struct {
		Data struct {
			Viewer struct {
				Organization struct {
					ID     string `json:"id"`
					URLKey string `json:"urlKey"`
				} `json:"organization"`
			} `json:"viewer"`
			Teams struct {
				Nodes []struct {
					ID   string `json:"id"`
					Key  string `json:"key"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, nil, &result); err != nil {
		return nil, err
	}
	out := make([]TeamKeyInfo, 0, len(result.Data.Teams.Nodes))
	for _, t := range result.Data.Teams.Nodes {
		out = append(out, TeamKeyInfo{
			TeamID:      t.ID,
			Key:         t.Key,
			Name:        t.Name,
			WorkspaceID: result.Data.Viewer.Organization.ID,
		})
	}
	return out, nil
}

// CreateOrUpdateAttachment idempotently writes the durable attachment for a
// session-issue link. PriorID drives the create-vs-update decision.
func (c *graphQLClient) CreateOrUpdateAttachment(ctx context.Context, in AttachmentWriteInput) (AttachmentResult, error) {
	metaJSON, err := json.Marshal(in.Metadata)
	if err != nil {
		return AttachmentResult{}, fmt.Errorf("encode attachment metadata: %w", err)
	}

	if in.PriorID != "" {
		query := `mutation($id: String!, $input: AttachmentUpdateInput!) {
			attachmentUpdate(id: $id, input: $input) {
				success
				attachment { id url }
			}
		}`
		var result struct {
			Data struct {
				AttachmentUpdate struct {
					Success    bool `json:"success"`
					Attachment struct {
						ID  string `json:"id"`
						URL string `json:"url"`
					} `json:"attachment"`
				} `json:"attachmentUpdate"`
			} `json:"data"`
		}
		input := map[string]any{
			"title":    in.Title,
			"subtitle": in.Subtitle,
			"metadata": json.RawMessage(metaJSON),
		}
		if err := c.do(ctx, query, map[string]any{"id": in.PriorID, "input": input}, &result); err != nil {
			return AttachmentResult{}, err
		}
		if !result.Data.AttachmentUpdate.Success {
			return AttachmentResult{}, fmt.Errorf("linear attachmentUpdate returned success=false")
		}
		if result.Data.AttachmentUpdate.Attachment.ID == "" {
			return AttachmentResult{}, fmt.Errorf("linear attachmentUpdate returned no attachment id")
		}
		return AttachmentResult{
			ID:  result.Data.AttachmentUpdate.Attachment.ID,
			URL: result.Data.AttachmentUpdate.Attachment.URL,
		}, nil
	}

	query := `mutation($input: AttachmentCreateInput!) {
		attachmentCreate(input: $input) {
			success
			attachment { id url }
		}
	}`
	var result struct {
		Data struct {
			AttachmentCreate struct {
				Success    bool `json:"success"`
				Attachment struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"attachment"`
			} `json:"attachmentCreate"`
		} `json:"data"`
	}
	input := map[string]any{
		"issueId":  in.IssueID,
		"title":    in.Title,
		"subtitle": in.Subtitle,
		"url":      in.URL,
		"metadata": json.RawMessage(metaJSON),
	}
	if in.IconURL != "" {
		input["iconUrl"] = in.IconURL
	}
	if err := c.do(ctx, query, map[string]any{"input": input}, &result); err != nil {
		return AttachmentResult{}, err
	}
	if !result.Data.AttachmentCreate.Success {
		return AttachmentResult{}, fmt.Errorf("linear attachmentCreate returned success=false")
	}
	if result.Data.AttachmentCreate.Attachment.ID == "" {
		return AttachmentResult{}, fmt.Errorf("linear attachmentCreate returned no attachment id")
	}
	return AttachmentResult{
		ID:  result.Data.AttachmentCreate.Attachment.ID,
		URL: result.Data.AttachmentCreate.Attachment.URL,
	}, nil
}

func (c *graphQLClient) CreateComment(ctx context.Context, issueID, body string) (string, error) {
	query := `mutation($input: CommentCreateInput!) {
		commentCreate(input: $input) {
			success
			comment { id }
		}
	}`
	var result struct {
		Data struct {
			CommentCreate struct {
				Success bool `json:"success"`
				Comment struct {
					ID string `json:"id"`
				} `json:"comment"`
			} `json:"commentCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{
		"input": map[string]any{"issueId": issueID, "body": body},
	}, &result); err != nil {
		return "", err
	}
	if !result.Data.CommentCreate.Success {
		return "", fmt.Errorf("linear commentCreate returned success=false")
	}
	if result.Data.CommentCreate.Comment.ID == "" {
		return "", fmt.Errorf("linear commentCreate returned no comment id")
	}
	return result.Data.CommentCreate.Comment.ID, nil
}

func (c *graphQLClient) UpdateComment(ctx context.Context, commentID, body string) error {
	query := `mutation($id: String!, $input: CommentUpdateInput!) {
		commentUpdate(id: $id, input: $input) {
			success
		}
	}`
	var result struct {
		Data struct {
			CommentUpdate struct {
				Success bool `json:"success"`
			} `json:"commentUpdate"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{
		"id":    commentID,
		"input": map[string]any{"body": body},
	}, &result); err != nil {
		return err
	}
	if !result.Data.CommentUpdate.Success {
		return fmt.Errorf("linear commentUpdate returned success=false")
	}
	return nil
}

// WorkflowStateForType picks the best workflow state for the given type,
// preferring user-named matches first. The team's own state set is the
// universe; if none of the preferences match we fall back to any state of
// the desired type.
func (c *graphQLClient) WorkflowStateForType(ctx context.Context, teamID string, prefer []string, stateType string) (*WorkflowState, error) {
	if teamID == "" || stateType == "" {
		return nil, errors.New("teamID and stateType are required")
	}
	query := `query($teamID: ID!) {
		workflowStates(filter: { team: { id: { eq: $teamID } } }, first: 50) {
			nodes { id name type position }
		}
	}`
	var result struct {
		Data struct {
			WorkflowStates struct {
				Nodes []struct {
					ID       string  `json:"id"`
					Name     string  `json:"name"`
					Type     string  `json:"type"`
					Position float64 `json:"position"`
				} `json:"nodes"`
			} `json:"workflowStates"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"teamID": teamID}, &result); err != nil {
		return nil, err
	}
	// Sort by position ascending so the no-preferences fallback is
	// deterministic ("leftmost typed state in the team's UI"). Linear's
	// workflowStates query has no orderBy argument, so default ordering is
	// API-implementation-dependent — without this sort, two `started`-type
	// states like "In Progress" and "In Review" could swap in API responses
	// and silently land a session-start move on "In Review."
	nodes := result.Data.WorkflowStates.Nodes
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].Position < nodes[j].Position
	})
	preferred := make(map[string]bool, len(prefer))
	for _, p := range prefer {
		preferred[strings.ToLower(p)] = true
	}
	var fallback *WorkflowState
	for _, n := range nodes {
		if n.Type != stateType {
			continue
		}
		if preferred[strings.ToLower(n.Name)] {
			return &WorkflowState{ID: n.ID, Name: n.Name, Type: n.Type}, nil
		}
		if fallback == nil {
			fallback = &WorkflowState{ID: n.ID, Name: n.Name, Type: n.Type}
		}
	}
	return fallback, nil
}

func (c *graphQLClient) UpdateIssueState(ctx context.Context, issueID, stateID string) error {
	query := `mutation($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) { success }
	}`
	var result struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{
		"id":    issueID,
		"input": map[string]any{"stateId": stateID},
	}, &result); err != nil {
		return err
	}
	if !result.Data.IssueUpdate.Success {
		return fmt.Errorf("linear issueUpdate returned success=false")
	}
	return nil
}

// IssueRecentHumanEdits returns true if a human moved the issue's workflow
// state within the given window. Reads from Linear's issue history; we only
// count entries that look like state transitions performed by users (not
// integrations or webhooks).
//
// Self-attribution guard: history entries whose actor matches the OAuth
// token's own viewer are filtered out. Linear attributes API-driven moves
// to the user who authorized the OAuth integration (with botActor=null), so
// without this filter our own previous transition would read as a "human
// edit" on the next milestone for the same session and skip every follow-up
// move (e.g. session-start → "In Progress" suppresses the subsequent PR-open
// → "In Review" within the 10-min window). The viewer lookup is folded into
// the same GraphQL request to avoid an extra round trip.
func (c *graphQLClient) IssueRecentHumanEdits(ctx context.Context, issueID string, since time.Time) (bool, error) {
	query := `query($id: String!) {
		viewer { id }
		issue(id: $id) {
			history(first: 10) {
				nodes {
					createdAt
					actor { id name displayName }
					botActor { name }
					fromState { id }
					toState { id }
				}
			}
		}
	}`
	var result struct {
		Data struct {
			Viewer *struct {
				ID string `json:"id"`
			} `json:"viewer"`
			Issue *struct {
				History struct {
					Nodes []struct {
						CreatedAt string `json:"createdAt"`
						Actor     *struct {
							ID          string `json:"id"`
							Name        string `json:"name"`
							DisplayName string `json:"displayName"`
						} `json:"actor"`
						BotActor *struct {
							Name string `json:"name"`
						} `json:"botActor"`
						FromState *struct {
							ID string `json:"id"`
						} `json:"fromState"`
						ToState *struct {
							ID string `json:"id"`
						} `json:"toState"`
					} `json:"nodes"`
				} `json:"history"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &result); err != nil {
		return false, err
	}
	if result.Data.Issue == nil {
		return false, nil
	}
	selfActorID := ""
	if result.Data.Viewer != nil {
		selfActorID = result.Data.Viewer.ID
	}
	for _, h := range result.Data.Issue.History.Nodes {
		if h.FromState == nil || h.ToState == nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, h.CreatedAt)
		if ts.Before(since) {
			continue
		}
		// Skip transitions performed by bots/integrations.
		if h.BotActor != nil && h.BotActor.Name != "" {
			continue
		}
		// Skip transitions attributed to our own OAuth actor — these are
		// 143's prior writes, not human edits worth deferring to.
		if h.Actor != nil && selfActorID != "" && h.Actor.ID == selfActorID {
			continue
		}
		if h.Actor != nil && h.Actor.Name != "" {
			return true, nil
		}
	}
	return false, nil
}

// FindRecentBotCommentByURL scans the issue's recent comments and returns
// the ID of the most recent comment whose body contains the given session
// URL. Used by HandleMilestone's recovery path to recover an orphaned
// comment when our commentCreate response was lost in flight (so we have
// no local CommentID to drive the update branch on retry).
//
// Linear has no client-supplied idempotency key on commentCreate, so we
// embed the deterministic per-session URL in the body and search for it
// here. A scan is restricted to ~50 recent comments — enough for our
// retry window (a stuck job retries within minutes) and avoids paginating
// long-lived issues.
//
// Pagination shape: Linear's `orderBy: createdAt` is ascending and
// `first: N` would return the OLDEST N comments, so on any issue with
// more than 50 comments the orphan we just posted (which is the most
// recent) would never appear in the result. We use `last: N` which —
// per the Relay-style pagination — returns the LAST N entries in the
// ordering, i.e. the newest 50.
//
// Returns "" with no error when no matching comment is found.
func (c *graphQLClient) FindRecentBotCommentByURL(ctx context.Context, issueID, sessionURL string) (string, error) {
	if issueID == "" || sessionURL == "" {
		return "", nil
	}
	query := `query($id: String!) {
		issue(id: $id) {
			comments(last: 50, orderBy: createdAt) {
				nodes { id body }
			}
		}
	}`
	var result struct {
		Data struct {
			Issue *struct {
				Comments struct {
					Nodes []struct {
						ID   string `json:"id"`
						Body string `json:"body"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &result); err != nil {
		return "", err
	}
	if result.Data.Issue == nil {
		return "", nil
	}
	// `last: N` with an ascending orderBy returns the newest N entries in
	// ascending order, so we still walk last-wins to return the most
	// recent match. Match on the session URL only — the bot prefix is
	// convention but a future format change shouldn't invalidate the
	// recovery.
	var foundID string
	for _, c := range result.Data.Issue.Comments.Nodes {
		if strings.Contains(c.Body, sessionURL) {
			foundID = c.ID
		}
	}
	return foundID, nil
}

// HasGitHubIntegrationAttachment checks whether Linear's native GitHub
// integration has already posted attachments to this issue. When true, we
// suppress our merge-time writes to avoid double cycle/sprint membership.
//
// We explicitly skip attachments whose metadata.service == "143" — those
// are *our* attachments, possibly with sourceType set by Linear's
// classifier to something we'd otherwise count. Self-detection would lock
// us out of writes after the first one.
func (c *graphQLClient) HasGitHubIntegrationAttachment(ctx context.Context, issueID string) (bool, error) {
	query := `query($id: String!) {
		issue(id: $id) {
			attachments(first: 20) { nodes { sourceType metadata } }
		}
	}`
	var result struct {
		Data struct {
			Issue *struct {
				Attachments struct {
					Nodes []struct {
						SourceType string         `json:"sourceType"`
						Metadata   map[string]any `json:"metadata"`
					} `json:"nodes"`
				} `json:"attachments"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &result); err != nil {
		return false, err
	}
	if result.Data.Issue == nil {
		return false, nil
	}
	for _, a := range result.Data.Issue.Attachments.Nodes {
		// Skip our own attachments first — see metadata schema in
		// db.LinearAttachmentMetadata.
		if svc, ok := a.Metadata["service"].(string); ok && svc == "143" {
			continue
		}
		// Linear's GitHub integration uses sourceType "github" for PR
		// attachments.
		if strings.EqualFold(a.SourceType, "github") {
			return true, nil
		}
	}
	return false, nil
}

// ----------------------------------------------------------------------------
// Linear Agent Interaction API
// ----------------------------------------------------------------------------
// The agent surface is a separate set of mutations Linear exposes for
// AgentSession-aware integrations:
//   * agentActivityCreate — emits one activity (thought/action/elicitation/
//     response/error) into a tracked AgentSession. Activity stream is what
//     Linear's UI renders as the live "thinking → running → final" view.
//   * agentSessionUpdate — sets externalUrls (deep links to the 143 session
//     and resulting PR) and optionally pins state.
//   * agentSessionGet     — fetches the AgentSession by id; used by the
//     dispatcher to confirm presence and recover the issue context after
//     a worker restart.
//
// All three reuse the same `do` transport so 401/429 and GraphQL-error
// shaping stay consistent with the rest of the client.

// AgentActivityInput is the data the writer hands the client for one
// AgentActivity emit. Fields map directly to Linear's
// AgentActivityCreateInput.content.
type AgentActivityInput struct {
	// AgentSessionID is the Linear AgentSession id this activity belongs to.
	// Required; the dispatcher persists it in linear_agent_sessions on the
	// `created` event and threads it through every subsequent emit.
	AgentSessionID string
	// Type is the activity kind. Use the typed constants in
	// internal/models/linear_agent_enums.go to avoid typos.
	Type string
	// Body is the human-visible message. For thought/response/error this
	// is the rendered text; for elicitation it is the question. Action
	// activities use Parameter/Result instead.
	Body string
	// Action is the human-readable name of the tool the agent invoked.
	// Only set on type=action; the GraphQL schema rejects it otherwise.
	Action string
	// Parameter is the action's input parameter, free-text. Required when
	// Type is action.
	Parameter string
	// Result is the action's output. Optional, only meaningful on
	// type=action. Limit ~4KB — Linear truncates large bodies and the value
	// is for human display, not machine consumption.
	Result string
	// Ephemeral marks the activity as transient (it scrolls out of the
	// activity feed quickly). Linear only honors this for thought/action.
	// The writer enforces this so we don't get a runtime GraphQL rejection.
	Ephemeral bool
}

// AgentActivityResult captures the post-emit metadata callers persist.
type AgentActivityResult struct {
	// ActivityID is Linear's id for the emitted activity. Persisted in
	// linear_agent_activity_log so a future replay can dedupe at the
	// (agent_session, idem_key) granularity even if our row was lost.
	ActivityID string
}

// AgentActivityCreate emits one activity into a Linear AgentSession.
//
// Per Linear's contract, only thought and action accept the ephemeral flag;
// the writer normalizes Ephemeral to false for other types before reaching
// here as a defense-in-depth (the upstream caller already enforces this via
// LinearAgentActivityType.CanBeEphemeral).
func (c *graphQLClient) AgentActivityCreate(ctx context.Context, in AgentActivityInput) (AgentActivityResult, error) {
	if in.AgentSessionID == "" {
		return AgentActivityResult{}, errors.New("agent_session_id is required")
	}
	if in.Type == "" {
		return AgentActivityResult{}, errors.New("activity type is required")
	}
	// Linear's AgentActivityCreateInput models content as a discriminated
	// union; we send a single content object whose shape varies by type.
	// Building the variables explicitly (rather than via reflection) keeps
	// the wire format obvious to a reader and lets us validate forbidden
	// combinations (e.g. ephemeral on a non-thought activity) loudly.
	content := map[string]any{
		"type": in.Type,
	}
	switch in.Type {
	case "thought", "response":
		content["body"] = in.Body
		if in.Type == "thought" && in.Ephemeral {
			content["ephemeral"] = true
		}
	case "action":
		if in.Action == "" {
			return AgentActivityResult{}, errors.New("action.action is required")
		}
		if in.Parameter == "" {
			return AgentActivityResult{}, errors.New("action.parameter is required")
		}
		content["action"] = in.Action
		content["parameter"] = in.Parameter
		if in.Result != "" {
			content["result"] = in.Result
		}
		if in.Ephemeral {
			content["ephemeral"] = true
		}
	case "elicitation":
		content["body"] = in.Body
	case "error":
		content["body"] = in.Body
	default:
		return AgentActivityResult{}, fmt.Errorf("unsupported agent activity type: %q", in.Type)
	}

	const query = `mutation AgentActivityCreate($input: AgentActivityCreateInput!) {
		agentActivityCreate(input: $input) {
			success
			agentActivity { id }
		}
	}`
	variables := map[string]any{
		"input": map[string]any{
			"agentSessionId": in.AgentSessionID,
			"content":        content,
		},
	}
	var result struct {
		Data struct {
			AgentActivityCreate struct {
				Success       bool `json:"success"`
				AgentActivity struct {
					ID string `json:"id"`
				} `json:"agentActivity"`
			} `json:"agentActivityCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, variables, &result); err != nil {
		return AgentActivityResult{}, err
	}
	if !result.Data.AgentActivityCreate.Success {
		return AgentActivityResult{}, errors.New("agent activity create returned success=false")
	}
	return AgentActivityResult{ActivityID: result.Data.AgentActivityCreate.AgentActivity.ID}, nil
}

// AgentSessionExternalURL is one entry in the externalUrls list Linear
// surfaces under the AgentSession header. Two slots are conventional:
// the 143 session URL and (after PR open) the GitHub PR URL.
type AgentSessionExternalURL struct {
	URL   string
	Title string
}

// AgentSessionUpdateInput captures the fields the writer can set on an
// existing AgentSession. State is optional — Linear computes it from the
// activity stream by default; only set this when the writer wants to pin
// the session into awaitingInput, complete, or error explicitly.
type AgentSessionUpdateInput struct {
	AgentSessionID string
	ExternalURLs   []AgentSessionExternalURL
	// State is one of "pending", "active", "awaitingInput", "complete",
	// "error" — Linear's vocabulary, not 143's. Empty string means "leave
	// the existing state alone".
	State string
}

// AgentSessionUpdate applies pinned fields to an existing AgentSession.
// Idempotent on Linear's side: two writes with the same content collapse.
func (c *graphQLClient) AgentSessionUpdate(ctx context.Context, in AgentSessionUpdateInput) error {
	if in.AgentSessionID == "" {
		return errors.New("agent_session_id is required")
	}
	if len(in.ExternalURLs) == 0 && in.State == "" {
		// Nothing to write — Linear would reject the empty patch. Treat
		// this as a no-op so callers can pass empty inputs without special
		// casing.
		return nil
	}
	input := map[string]any{}
	if len(in.ExternalURLs) > 0 {
		urls := make([]map[string]string, 0, len(in.ExternalURLs))
		for _, u := range in.ExternalURLs {
			urls = append(urls, map[string]string{
				"url":   u.URL,
				"label": u.Title,
			})
		}
		input["externalUrls"] = urls
	}
	if in.State != "" {
		input["state"] = in.State
	}
	const query = `mutation AgentSessionUpdate($agentSessionId: String!, $input: AgentSessionUpdateInput!) {
		agentSessionUpdate(id: $agentSessionId, input: $input) {
			success
		}
	}`
	var result struct {
		Data struct {
			AgentSessionUpdate struct {
				Success bool `json:"success"`
			} `json:"agentSessionUpdate"`
		} `json:"data"`
	}
	variables := map[string]any{
		"agentSessionId": in.AgentSessionID,
		"input":          input,
	}
	if err := c.do(ctx, query, variables, &result); err != nil {
		return err
	}
	if !result.Data.AgentSessionUpdate.Success {
		return errors.New("agent session update returned success=false")
	}
	return nil
}

// FetchedAgentSession is the recovery snapshot used by the dispatcher when
// a `prompted` event arrives but the local row was lost (e.g. DB restored
// from backup). The fields mirror what we store in linear_agent_sessions
// plus the most recent comment id (used as the parent for response
// activities).
type FetchedAgentSession struct {
	ID        string
	IssueID   string
	IssueKey  string
	State     string
	CreatorID string
	CommentID string
	UpdatedAt time.Time
}

// FetchComment resolves a single comment by id. Used by the prompted
// worker handler to read the user's follow-up message verbatim.
//
// Returns ErrCommentNotFound when Linear has no record of the id (rare
// — Linear preserves comments on issue lifecycle changes — but possible
// if the comment was deleted between dispatch and worker execution).
func (c *graphQLClient) FetchComment(ctx context.Context, commentID string) (*FetchedComment, error) {
	if commentID == "" {
		return nil, errors.New("comment_id is required")
	}
	const query = `query CommentGet($id: String!) {
		comment(id: $id) {
			id body createdAt
			user { name }
			issue { id }
		}
	}`
	var result struct {
		Data struct {
			Comment *struct {
				ID        string    `json:"id"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"createdAt"`
				User      struct {
					Name string `json:"name"`
				} `json:"user"`
				Issue struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"comment"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"id": commentID}, &result); err != nil {
		return nil, err
	}
	if result.Data.Comment == nil {
		return nil, ErrCommentNotFound
	}
	return &FetchedComment{
		ID:        result.Data.Comment.ID,
		Body:      result.Data.Comment.Body,
		Author:    result.Data.Comment.User.Name,
		IssueID:   result.Data.Comment.Issue.ID,
		CreatedAt: result.Data.Comment.CreatedAt,
	}, nil
}

// ErrCommentNotFound is returned by FetchComment when Linear cannot
// resolve the requested comment id.
var ErrCommentNotFound = errors.New("linear comment not found")

// AgentSessionGet is the recovery hook. Returns ErrAgentSessionNotFound when
// Linear no longer has the session (rare but possible if the workspace's
// retention has elapsed).
func (c *graphQLClient) AgentSessionGet(ctx context.Context, agentSessionID string) (*FetchedAgentSession, error) {
	if agentSessionID == "" {
		return nil, errors.New("agent_session_id is required")
	}
	const query = `query AgentSessionGet($id: String!) {
		agentSession(id: $id) {
			id state updatedAt
			issue { id identifier }
			comment { id }
			creator { id }
		}
	}`
	var result struct {
		Data struct {
			AgentSession *struct {
				ID        string    `json:"id"`
				State     string    `json:"state"`
				UpdatedAt time.Time `json:"updatedAt"`
				Issue     struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
				} `json:"issue"`
				Comment *struct {
					ID string `json:"id"`
				} `json:"comment"`
				Creator *struct {
					ID string `json:"id"`
				} `json:"creator"`
			} `json:"agentSession"`
		} `json:"data"`
	}
	if err := c.do(ctx, query, map[string]any{"id": agentSessionID}, &result); err != nil {
		return nil, err
	}
	if result.Data.AgentSession == nil {
		return nil, ErrAgentSessionNotFound
	}
	out := &FetchedAgentSession{
		ID:        result.Data.AgentSession.ID,
		IssueID:   result.Data.AgentSession.Issue.ID,
		IssueKey:  result.Data.AgentSession.Issue.Identifier,
		State:     result.Data.AgentSession.State,
		UpdatedAt: result.Data.AgentSession.UpdatedAt,
	}
	if result.Data.AgentSession.Comment != nil {
		out.CommentID = result.Data.AgentSession.Comment.ID
	}
	if result.Data.AgentSession.Creator != nil {
		out.CreatorID = result.Data.AgentSession.Creator.ID
	}
	return out, nil
}

// ErrAgentSessionNotFound is returned by AgentSessionGet when Linear has no
// record of the requested AgentSession. Callers treat this as terminal —
// the matching 143 session can be soft-completed because there is no Linear
// surface left to stream into.
var ErrAgentSessionNotFound = errors.New("linear agent session not found")

// do is the GraphQL transport. Handles 200/non-200 + GraphQL errors. Note:
// retries and rate-limit handling live in the worker layer (RetryableError),
// not here, so the Client stays a thin transport.
func (c *graphQLClient) do(ctx context.Context, query string, variables map[string]any, target any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return &RateLimitError{RetryAfter: retryAfter}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear API returned %d", resp.StatusCode)
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("decode linear response: %w", err)
	}
	var errCheck struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &errCheck); err == nil && len(errCheck.Errors) > 0 {
		return fmt.Errorf("linear graphql error: %s", joinGraphQLErrorMessages(errCheck.Errors))
	}
	return json.Unmarshal(raw, target)
}

// joinGraphQLErrorMessages flattens Linear's `errors[]` array into a
// single line. We deliberately surface every distinct error message (up
// to a count cap) rather than just errors[0] — Linear can report related
// validation failures across the array (e.g. "team not found" + "issue
// not found") and showing only the first hides the operator-relevant
// detail. The joined string is bounded by truncateErrorMessage, so a
// pathological array of multi-KB messages still can't blow up logs.
func joinGraphQLErrorMessages(errs []struct {
	Message string `json:"message"`
}) string {
	const maxMessages = 5
	parts := make([]string, 0, len(errs))
	for i, e := range errs {
		if i >= maxMessages {
			parts = append(parts, fmt.Sprintf("…(%d more)", len(errs)-maxMessages))
			break
		}
		parts = append(parts, e.Message)
	}
	return truncateErrorMessage(strings.Join(parts, "; "))
}

// maxLinearErrorMessageLen caps how much of a GraphQL error message we
// surface in returned errors. Linear can occasionally return verbose
// payloads (multi-KB validation traces or stack-trace-shaped messages)
// that, when bubbled up through error wrapping into structured logs,
// blow up log lines and any downstream cost / size limits. 512 bytes
// preserves the operator-actionable head of the message without the
// tail that's rarely useful.
const maxLinearErrorMessageLen = 512

func truncateErrorMessage(s string) string {
	if len(s) <= maxLinearErrorMessageLen {
		return s
	}
	// Trim back to the previous valid UTF-8 boundary so the cap doesn't
	// split a multi-byte rune and produce invalid UTF-8 in error logs (some
	// JSON encoders reject or replace it, masking the actual error message).
	cut := maxLinearErrorMessageLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// ErrUnauthorized is returned by the client when Linear rejects the access
// token. The integration health check (every 6h) flags it; doGraphQL
// callers can choose to refresh and retry once.
var ErrUnauthorized = errors.New("linear unauthorized")

// RateLimitError indicates a 429 with an optional Retry-After hint. Worker
// handlers wrap this in their own RetryableError to schedule the retry.
type RateLimitError struct {
	RetryAfter string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("linear rate limit exceeded (retry-after=%s)", e.RetryAfter)
	}
	return "linear rate limit exceeded"
}

func mapLinearPriorityName(p int) string {
	switch p {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	}
	return "none"
}
