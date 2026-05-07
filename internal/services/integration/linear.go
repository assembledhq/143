package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/services/ingestion"
)

// LinearTaskManager implements TaskManager for the Linear issue tracker.
// It uses Linear's GraphQL API for all operations.
//
// The ingestion layer (ingestion.LinearAdapter) only handles webhook parsing
// and normalizes incoming Linear events into NormalizedIssue. This is the
// first Linear API client in the codebase — it provides the read/write
// operations needed by the PM agent and MCP servers.
type LinearTaskManager struct {
	httpClient *http.Client
	apiURL     string
	authToken  string
}

// LinearManagerConfig holds the connection details for a Linear TaskManager.
type LinearManagerConfig struct {
	AuthToken string
	APIURL    string // defaults to "https://api.linear.app/graphql"
}

// NewLinearTaskManager creates a Linear TaskManager from the given config.
func NewLinearTaskManager(cfg LinearManagerConfig) *LinearTaskManager {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = "https://api.linear.app/graphql"
	}
	return &LinearTaskManager{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiURL:     apiURL,
		authToken:  cfg.AuthToken,
	}
}

func (l *LinearTaskManager) Name() string { return "linear" }

// ListTasks queries Linear for issues matching the filter.
func (l *LinearTaskManager) ListTasks(ctx context.Context, filter TaskFilter) ([]TaskSummary, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	// Build the GraphQL filter object.
	filterParts := make(map[string]interface{})
	if filter.TeamKey != "" {
		filterParts["team"] = map[string]interface{}{"key": map[string]string{"eq": filter.TeamKey}}
	}
	if len(filter.States) > 0 {
		filterParts["state"] = map[string]interface{}{"type": map[string]interface{}{"in": filter.States}}
	}
	if filter.Priority != "" {
		filterParts["priority"] = map[string]interface{}{"lte": mapPriorityToLinear(filter.Priority)}
	}
	if len(filter.Labels) > 0 {
		filterParts["labels"] = map[string]interface{}{"name": map[string]interface{}{"in": filter.Labels}}
	}
	if !filter.Since.IsZero() {
		filterParts["updatedAt"] = map[string]interface{}{"gte": filter.Since.Format(time.RFC3339)}
	}

	query := `query($filter: IssueFilter, $first: Int) {
		issues(filter: $filter, first: $first, orderBy: updatedAt) {
			nodes {
				id
				identifier
				title
				state { name type }
				priority
				team { key name }
				labels { nodes { name } }
				assignee { name }
				createdAt
				updatedAt
			}
		}
	}`

	variables := map[string]interface{}{
		"first": limit,
	}
	if len(filterParts) > 0 {
		variables["filter"] = filterParts
	}

	var result struct {
		Data struct {
			Issues struct {
				Nodes []linearIssueNode `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("list linear tasks: %w", err)
	}

	summaries := make([]TaskSummary, 0, len(result.Data.Issues.Nodes))
	for _, node := range result.Data.Issues.Nodes {
		summaries = append(summaries, linearNodeToSummary(node))
	}
	return summaries, nil
}

// GetTask fetches full details for a single Linear issue, including comments.
func (l *LinearTaskManager) GetTask(ctx context.Context, taskID string) (*TaskDetail, error) {
	issueRef, err := l.resolveIssueReference(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("resolve linear task reference: %w", err)
	}

	query := `query($id: String!) {
		issue(id: $id) {
			id
			identifier
			title
			description
			state { name type }
			priority
			team { key name }
			labels { nodes { name } }
			assignee { name }
			createdAt
			updatedAt
			url
			parent { id identifier }
			relations { nodes { relatedIssue { id identifier title } } }
			comments(first: 20) {
				nodes {
					body
					user { name }
					createdAt
				}
			}
		}
	}`

	var result struct {
		Data struct {
			Issue linearIssueDetailNode `json:"issue"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, map[string]interface{}{"id": issueRef.ID}, &result); err != nil {
		return nil, fmt.Errorf("get linear task: %w", err)
	}

	node := result.Data.Issue
	detail := &TaskDetail{
		TaskSummary: linearNodeToSummary(node.linearIssueNode),
		Description: node.Description,
		WebURL:      node.URL,
	}

	if node.Parent != nil {
		detail.ParentID = node.Parent.ID
	}

	for _, rel := range node.Relations.Nodes {
		detail.LinkedIssues = append(detail.LinkedIssues, rel.RelatedIssue.ID)
	}

	for _, c := range node.Comments.Nodes {
		detail.Comments = append(detail.Comments, TaskComment{
			Author:    c.User.Name,
			Body:      c.Body,
			CreatedAt: ingestion.ParseTimeSafe(c.CreatedAt),
		})
	}

	return detail, nil
}

// FindRelated returns issues related to the given task via Linear's relations
// and sub-issue hierarchy.
func (l *LinearTaskManager) FindRelated(ctx context.Context, taskID string) ([]TaskSummary, error) {
	detail, err := l.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// Collect related IDs from links and parent.
	relatedIDs := make([]string, 0, len(detail.LinkedIssues)+1)
	relatedIDs = append(relatedIDs, detail.LinkedIssues...)
	if detail.ParentID != "" {
		relatedIDs = append(relatedIDs, detail.ParentID)
	}

	if len(relatedIDs) == 0 {
		return nil, nil
	}

	// Fetch summaries for related issues.
	query := `query($filter: IssueFilter) {
		issues(filter: $filter, first: 20) {
			nodes {
				id
				identifier
				title
				state { name type }
				priority
				team { key name }
				labels { nodes { name } }
				assignee { name }
				createdAt
				updatedAt
			}
		}
	}`

	variables := map[string]interface{}{
		"filter": map[string]interface{}{
			"id": map[string]interface{}{"in": relatedIDs},
		},
	}

	var result struct {
		Data struct {
			Issues struct {
				Nodes []linearIssueNode `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("find related linear tasks: %w", err)
	}

	summaries := make([]TaskSummary, 0, len(result.Data.Issues.Nodes))
	for _, node := range result.Data.Issues.Nodes {
		summaries = append(summaries, linearNodeToSummary(node))
	}
	return summaries, nil
}

// UpdateTask applies a change to a Linear issue.
func (l *LinearTaskManager) UpdateTask(ctx context.Context, taskID string, update TaskUpdate) error {
	issueRef, err := l.resolveIssueReference(ctx, taskID)
	if err != nil {
		return fmt.Errorf("resolve linear task reference: %w", err)
	}

	// Add comment first if provided, since it's a separate mutation.
	if update.Comment != nil && *update.Comment != "" {
		commentQuery := `mutation($issueId: String!, $body: String!) {
			commentCreate(input: { issueId: $issueId, body: $body }) {
				success
			}
		}`
		var commentResult struct {
			Data struct {
				CommentCreate struct {
					Success bool `json:"success"`
				} `json:"commentCreate"`
			} `json:"data"`
		}
		if err := l.doGraphQL(ctx, commentQuery, map[string]interface{}{
			"issueId": issueRef.ID,
			"body":    *update.Comment,
		}, &commentResult); err != nil {
			return fmt.Errorf("add comment to linear task: %w", err)
		}
	}

	// Build the issue update input.
	input := make(map[string]interface{})
	if update.Priority != nil {
		input["priority"] = mapPriorityToLinear(*update.Priority)
	}
	if update.State != nil {
		stateID, err := l.resolveWorkflowStateID(ctx, issueRef, *update.State)
		if err != nil {
			return fmt.Errorf("resolve linear workflow state: %w", err)
		}
		input["stateId"] = stateID
	}

	if len(input) > 0 {
		updateQuery := `mutation($id: String!, $input: IssueUpdateInput!) {
			issueUpdate(id: $id, input: $input) {
				success
			}
		}`
		var updateResult struct {
			Data struct {
				IssueUpdate struct {
					Success bool `json:"success"`
				} `json:"issueUpdate"`
			} `json:"data"`
		}
		if err := l.doGraphQL(ctx, updateQuery, map[string]interface{}{
			"id":    issueRef.ID,
			"input": input,
		}, &updateResult); err != nil {
			return fmt.Errorf("update linear task: %w", err)
		}
	}

	// Handle label changes.
	if len(update.Labels.Add) > 0 || len(update.Labels.Remove) > 0 {
		labelIDs, err := l.resolveLabelIDs(ctx, issueRef, update.Labels.Add, update.Labels.Remove)
		if err != nil {
			return fmt.Errorf("resolve linear labels: %w", err)
		}
		labelQuery := `mutation($id: String!, $input: IssueUpdateInput!) {
			issueUpdate(id: $id, input: $input) {
				success
			}
		}`
		var labelResult struct {
			Data struct {
				IssueUpdate struct {
					Success bool `json:"success"`
				} `json:"issueUpdate"`
			} `json:"data"`
		}
		if err := l.doGraphQL(ctx, labelQuery, map[string]interface{}{
			"id":    issueRef.ID,
			"input": map[string]interface{}{"labelIds": labelIDs},
		}, &labelResult); err != nil {
			return fmt.Errorf("update linear task labels: %w", err)
		}
	}

	return nil
}

type linearIssueReference struct {
	ID     string
	TeamID string
}

// CreateTask creates a new Linear issue.
func (l *LinearTaskManager) CreateTask(ctx context.Context, spec TaskCreateSpec) (*TaskSummary, error) {
	// First, resolve the team key to a team ID.
	teamQuery := `query($key: String!) {
		teams(filter: { key: { eq: $key } }) {
			nodes { id }
		}
	}`
	var teamResult struct {
		Data struct {
			Teams struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"data"`
	}
	if err := l.doGraphQL(ctx, teamQuery, map[string]interface{}{
		"key": spec.TeamKey,
	}, &teamResult); err != nil {
		return nil, fmt.Errorf("resolve linear team: %w", err)
	}
	if len(teamResult.Data.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("linear team %q not found", spec.TeamKey)
	}
	teamID := teamResult.Data.Teams.Nodes[0].ID

	// Create the issue.
	input := map[string]interface{}{
		"title":  spec.Title,
		"teamId": teamID,
	}
	if spec.Description != "" {
		input["description"] = spec.Description
	}
	if spec.Priority != "" {
		input["priority"] = mapPriorityToLinear(spec.Priority)
	}
	if spec.ParentID != "" {
		input["parentId"] = spec.ParentID
	}

	createQuery := `mutation($input: IssueCreateInput!) {
		issueCreate(input: $input) {
			success
			issue {
				id
				identifier
				title
				state { name type }
				priority
				team { key name }
				labels { nodes { name } }
				assignee { name }
				createdAt
				updatedAt
			}
		}
	}`

	var createResult struct {
		Data struct {
			IssueCreate struct {
				Success bool            `json:"success"`
				Issue   linearIssueNode `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
	}
	if err := l.doGraphQL(ctx, createQuery, map[string]interface{}{
		"input": input,
	}, &createResult); err != nil {
		return nil, fmt.Errorf("create linear task: %w", err)
	}

	if !createResult.Data.IssueCreate.Success {
		return nil, fmt.Errorf("linear issue creation failed")
	}

	summary := linearNodeToSummary(createResult.Data.IssueCreate.Issue)
	return &summary, nil
}

// --- internal types ---

type linearIssueNode struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	State      struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Priority int `json:"priority"`
	Team     struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"team"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignee struct {
		Name string `json:"name"`
	} `json:"assignee"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type linearIssueDetailNode struct {
	linearIssueNode
	Description string `json:"description"`
	URL         string `json:"url"`
	Parent      *struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	} `json:"parent"`
	Relations struct {
		Nodes []struct {
			RelatedIssue struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
			} `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
	Comments struct {
		Nodes []struct {
			Body string `json:"body"`
			User struct {
				Name string `json:"name"`
			} `json:"user"`
			CreatedAt string `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
}

// --- helpers ---

func (l *LinearTaskManager) doGraphQL(ctx context.Context, query string, variables map[string]interface{}, target interface{}) error {
	body, err := json.Marshal(map[string]interface{}{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.authToken)

	resp, err := l.httpClient.Do(req) // #nosec G107 -- URL is from internal config
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return &LinearRateLimitError{RetryAfter: resp.Header.Get("Retry-After")}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxLinearErrorBodyBytes))
		if msg := linearErrorBodyMessage(bodyBytes); msg != "" {
			return fmt.Errorf("%w: %s", ErrLinearUnauthorized, truncateLinearErrorMessage(msg))
		}
		return ErrLinearUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		// Capture and surface what Linear actually said. The 4xx body is
		// where the diagnosable detail lives — masking it as bare "linear API
		// returned 400" forces ops into log spelunking that won't yield more
		// than what the body already carries.
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxLinearErrorBodyBytes))
		// If the body is a GraphQL error envelope, prefer the structured
		// message — it's far more readable than the raw JSON.
		if msg := linearErrorBodyMessage(bodyBytes); msg != "" {
			return fmt.Errorf("linear API returned %d: %s", resp.StatusCode, truncateLinearErrorMessage(msg))
		}
		return fmt.Errorf("linear API returned %d", resp.StatusCode)
	}

	// Check for GraphQL errors in a 200 response body.
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("decode linear response: %w", err)
	}

	if msg := parseGraphQLErrorMessage(raw); msg != "" {
		return fmt.Errorf("linear graphql error: %s", truncateLinearErrorMessage(msg))
	}

	return json.Unmarshal(raw, target)
}

// ErrLinearUnauthorized is returned when Linear rejects the access token with
// a 401. Callers (and the sandbox CLI wrapper) match on this with errors.Is to
// surface a "reconnect Linear" signal to the user instead of an opaque
// "linear API returned 401" string.
var ErrLinearUnauthorized = errors.New("linear unauthorized")

// LinearRateLimitError carries the Retry-After hint from a 429 response so
// retry orchestration upstream can space its attempts; matches the shape the
// services/linear client uses so the two surfaces stay legible together.
type LinearRateLimitError struct {
	RetryAfter string
}

func (e *LinearRateLimitError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("linear rate limit exceeded (retry-after=%s)", e.RetryAfter)
	}
	return "linear rate limit exceeded"
}

// maxLinearErrorBodyBytes caps how much of a non-2xx response body we read
// before composing an error. Linear can return small JSON envelopes or the
// occasional HTML error page from its edge — 4 KiB is enough for the actual
// message and keeps a wedged proxy from turning every retry into a megabyte
// of error text in worker logs.
const maxLinearErrorBodyBytes = 4 * 1024

// maxLinearErrorMessageLen is the per-message cap applied after parsing.
// Linear validation traces can run multi-KB; this keeps logs small while
// retaining the operator-actionable head of the message.
const maxLinearErrorMessageLen = 512

func truncateLinearErrorMessage(s string) string {
	if len(s) <= maxLinearErrorMessageLen {
		return s
	}
	cut := maxLinearErrorMessageLen
	// Step back to a valid UTF-8 rune boundary so the cap doesn't split a
	// multi-byte rune and corrupt the error string.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// parseGraphQLErrorMessage returns the first non-empty errors[].message from a
// GraphQL response envelope, or "" if the body isn't an envelope or has no
// errors. Used both for 200 responses (where Linear may report query errors
// in the body) and for 4xx responses (where it sometimes returns the same
// envelope with auth/validation errors).
func parseGraphQLErrorMessage(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	for _, e := range envelope.Errors {
		if e.Message != "" {
			return e.Message
		}
	}
	return ""
}

func linearErrorBodyMessage(body []byte) string {
	if msg := parseGraphQLErrorMessage(body); msg != "" {
		return msg
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 {
		return string(trimmed)
	}
	return ""
}

func linearNodeToSummary(node linearIssueNode) TaskSummary {
	labels := make([]string, 0, len(node.Labels.Nodes))
	for _, l := range node.Labels.Nodes {
		labels = append(labels, l.Name)
	}

	team := node.Team.Name
	if team == "" {
		team = node.Team.Key
	}

	return TaskSummary{
		ID:         node.ID,
		Identifier: node.Identifier,
		Title:      node.Title,
		State:      node.State.Name,
		StateType:  node.State.Type,
		Priority:   mapLinearPriorityToString(node.Priority),
		Team:       team,
		Labels:     labels,
		Assignee:   node.Assignee.Name,
		CreatedAt:  ingestion.ParseTimeSafe(node.CreatedAt),
		UpdatedAt:  ingestion.ParseTimeSafe(node.UpdatedAt),
	}
}

var linearIdentifierPattern = regexp.MustCompile(`^[A-Z0-9]+-\d+$`)

func (l *LinearTaskManager) resolveIssueReference(ctx context.Context, taskID string) (linearIssueReference, error) {
	if !linearIdentifierPattern.MatchString(taskID) {
		return linearIssueReference{ID: taskID}, nil
	}

	query := `query($identifier: String!) {
		issues(filter: { identifier: { eq: $identifier } }, first: 1) {
			nodes {
				id
				team { id }
			}
		}
	}`

	var result struct {
		Data struct {
			Issues struct {
				Nodes []struct {
					ID   string `json:"id"`
					Team struct {
						ID string `json:"id"`
					} `json:"team"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, map[string]interface{}{"identifier": taskID}, &result); err != nil {
		return linearIssueReference{}, err
	}
	if len(result.Data.Issues.Nodes) == 0 {
		return linearIssueReference{}, fmt.Errorf("linear issue identifier %q not found", taskID)
	}

	return linearIssueReference{
		ID:     result.Data.Issues.Nodes[0].ID,
		TeamID: result.Data.Issues.Nodes[0].Team.ID,
	}, nil
}

func (l *LinearTaskManager) resolveWorkflowStateID(ctx context.Context, issueRef linearIssueReference, stateName string) (string, error) {
	teamID := issueRef.TeamID
	if teamID == "" {
		var err error
		teamID, err = l.getIssueTeamID(ctx, issueRef.ID)
		if err != nil {
			return "", err
		}
	}

	query := `query($teamID: String!, $stateName: String!) {
		workflowStates(
			filter: {
				team: { id: { eq: $teamID } }
				name: { eq: $stateName }
			}
			first: 1
		) {
			nodes { id }
		}
	}`

	var result struct {
		Data struct {
			WorkflowStates struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"workflowStates"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, map[string]interface{}{
		"teamID":    teamID,
		"stateName": stateName,
	}, &result); err != nil {
		return "", err
	}
	if len(result.Data.WorkflowStates.Nodes) == 0 {
		return "", fmt.Errorf("linear workflow state %q not found", stateName)
	}

	return result.Data.WorkflowStates.Nodes[0].ID, nil
}

func (l *LinearTaskManager) getIssueTeamID(ctx context.Context, issueID string) (string, error) {
	query := `query($id: String!) {
		issue(id: $id) {
			team { id }
		}
	}`

	var result struct {
		Data struct {
			Issue *struct {
				Team struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"issue"`
		} `json:"data"`
	}

	if err := l.doGraphQL(ctx, query, map[string]interface{}{"id": issueID}, &result); err != nil {
		return "", err
	}
	if result.Data.Issue == nil || result.Data.Issue.Team.ID == "" {
		return "", fmt.Errorf("linear issue %q does not have a team", issueID)
	}

	return result.Data.Issue.Team.ID, nil
}

// resolveLabelIDs computes the final set of label IDs for an issue by fetching
// current labels, adding new ones by name, and removing specified ones by name.
func (l *LinearTaskManager) resolveLabelIDs(ctx context.Context, issueRef linearIssueReference, addNames, removeNames []string) ([]string, error) {
	// 1. Get the issue's current labels.
	currentQuery := `query($id: String!) {
		issue(id: $id) {
			labels { nodes { id name } }
		}
	}`
	var currentResult struct {
		Data struct {
			Issue struct {
				Labels struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := l.doGraphQL(ctx, currentQuery, map[string]interface{}{"id": issueRef.ID}, &currentResult); err != nil {
		return nil, fmt.Errorf("get current labels: %w", err)
	}

	// Build a map of current label name→ID and track the set of IDs.
	currentIDs := make(map[string]bool)
	nameToID := make(map[string]string)
	for _, label := range currentResult.Data.Issue.Labels.Nodes {
		currentIDs[label.ID] = true
		nameToID[label.Name] = label.ID
	}

	// 2. Resolve add label names to IDs via the team's labels.
	if len(addNames) > 0 {
		teamID := issueRef.TeamID
		if teamID == "" {
			var err error
			teamID, err = l.getIssueTeamID(ctx, issueRef.ID)
			if err != nil {
				return nil, err
			}
		}

		teamLabelsQuery := `query($teamID: ID!) {
			team(id: $teamID) {
				labels { nodes { id name } }
			}
		}`
		var teamLabelsResult struct {
			Data struct {
				Team struct {
					Labels struct {
						Nodes []struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"labels"`
				} `json:"team"`
			} `json:"data"`
		}
		if err := l.doGraphQL(ctx, teamLabelsQuery, map[string]interface{}{"teamID": teamID}, &teamLabelsResult); err != nil {
			return nil, fmt.Errorf("get team labels: %w", err)
		}

		teamNameToID := make(map[string]string)
		for _, label := range teamLabelsResult.Data.Team.Labels.Nodes {
			teamNameToID[label.Name] = label.ID
		}

		for _, name := range addNames {
			id, ok := teamNameToID[name]
			if !ok {
				return nil, fmt.Errorf("label %q not found on team", name)
			}
			currentIDs[id] = true
		}
	}

	// 3. Remove labels by name.
	for _, name := range removeNames {
		if id, ok := nameToID[name]; ok {
			delete(currentIDs, id)
		}
	}

	// 4. Collect final label IDs.
	finalIDs := make([]string, 0, len(currentIDs))
	for id := range currentIDs {
		finalIDs = append(finalIDs, id)
	}
	return finalIDs, nil
}

func mapLinearPriorityToString(priority int) string {
	switch priority {
	case 0:
		return "none"
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	default:
		return "none"
	}
}

func mapPriorityToLinear(priority string) int {
	switch priority {
	case "urgent":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	case "low":
		return 4
	default:
		return 0
	}
}
