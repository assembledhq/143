package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// LinearTaskManager implements TaskManager for the Linear issue tracker.
// It uses Linear's GraphQL API for all operations.
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

	if err := l.doGraphQL(ctx, query, map[string]interface{}{"id": taskID}, &result); err != nil {
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
			CreatedAt: parseTimeBestEffort(c.CreatedAt),
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
			"issueId": taskID,
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
		// State updates need the state ID, but we accept the state name.
		// For simplicity, we set it via the stateId field — the caller
		// should resolve the state name to an ID before calling UpdateTask,
		// or we could add a state resolution step here. For now, we pass
		// the name as Linear also accepts it in some contexts.
		input["stateId"] = *update.State
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
			"id":    taskID,
			"input": input,
		}, &updateResult); err != nil {
			return fmt.Errorf("update linear task: %w", err)
		}
	}

	// Handle label changes.
	if len(update.Labels.Add) > 0 || len(update.Labels.Remove) > 0 {
		// Linear's label API requires label IDs, not names. For now,
		// we note this as a TODO — the MCP server will need to resolve
		// label names to IDs via a separate query.
		// This is intentionally a no-op until label resolution is added.
	}

	return nil
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
			Body      string `json:"body"`
			User      struct {
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
	req.Header.Set("Authorization", l.authToken)

	resp, err := l.httpClient.Do(req) // #nosec G107 -- URL is from internal config
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear API returned %d", resp.StatusCode)
	}

	// Check for GraphQL errors.
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
		return fmt.Errorf("linear graphql error: %s", errCheck.Errors[0].Message)
	}

	return json.Unmarshal(raw, target)
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
		CreatedAt:  parseTimeBestEffort(node.CreatedAt),
		UpdatedAt:  parseTimeBestEffort(node.UpdatedAt),
	}
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
