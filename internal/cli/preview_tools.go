package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/services/mcp"
)

// previewToolExecutor implements the platform preview tools
// (preview_create/status/list/stop) as thin client-side wrappers over the
// existing /api/v1/previews* REST endpoints, which already work under
// bearer auth. preview_create accepts a repository *name* (agents don't
// know UUIDs) and resolves it against the org's repositories, and returns
// immediately — agents poll preview_status rather than holding a long-lived
// tool call.
type previewToolExecutor struct {
	client *Client
}

func (e *previewToolExecutor) handles(name string) bool {
	switch name {
	case "preview_create", "preview_status", "preview_list", "preview_stop":
		return true
	}
	return false
}

func (e *previewToolExecutor) tools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "preview_create",
			Description: "Create (or reuse) a live preview environment for a branch of one of the org's repositories. " +
				"Returns {preview_id, preview_url, status} immediately — poll preview_status until status is \"running\". " +
				"The branch must already be pushed to the remote (previews build from the pushed repo, not the local working tree).",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"repository": {Type: "string", Description: `Repository name, e.g. "acme/webapp" or just "webapp"`},
					"branch":     {Type: "string", Description: "Branch name (must exist on the remote)"},
				},
				Required: []string{"repository", "branch"},
			},
		},
		{
			Name:        "preview_status",
			Description: "Get the current status of a preview environment. Returns status, preview_url when live, and any error.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"preview_id": {Type: "string", Description: "The preview ID returned by preview_create"},
				},
				Required: []string{"preview_id"},
			},
		},
		{
			Name:        "preview_list",
			Description: "List the org's branch preview environments with their statuses and URLs.",
			InputSchema: mcp.ToolSchema{Type: "object"},
		},
		{
			Name:        "preview_stop",
			Description: "Stop a running preview environment.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"preview_id": {Type: "string", Description: "The preview ID to stop"},
				},
				Required: []string{"preview_id"},
			},
		},
	}
}

func (e *previewToolExecutor) call(ctx context.Context, name string, args json.RawMessage) *mcp.ToolCallResult {
	switch name {
	case "preview_create":
		return e.create(ctx, args)
	case "preview_status":
		return e.status(ctx, args)
	case "preview_list":
		return e.list(ctx)
	case "preview_stop":
		return e.stop(ctx, args)
	}
	return mcp.ErrorResult(fmt.Sprintf("unknown preview tool %q", name))
}

// previewView is the slice of the server's branch-preview response surfaced
// to agents: enough to act on, nothing to parse around.
type previewView struct {
	PreviewID    string  `json:"preview_id"`
	Repository   string  `json:"repository,omitempty"`
	Branch       string  `json:"branch,omitempty"`
	Status       string  `json:"status"`
	PreviewURL   *string `json:"preview_url"`
	CurrentPhase string  `json:"current_phase,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// branchPreviewWire mirrors the fields of the server's branch-preview
// response this executor consumes.
type branchPreviewWire struct {
	TargetID           string  `json:"target_id"`
	PreviewID          *string `json:"preview_id"`
	RepositoryFullName string  `json:"repository_full_name"`
	Branch             string  `json:"branch"`
	Status             string  `json:"status"`
	PreviewURL         *string `json:"preview_url"`
	CurrentPhase       string  `json:"current_phase"`
	Error              string  `json:"error"`
}

func (w branchPreviewWire) view() previewView {
	id := w.TargetID
	if w.PreviewID != nil && *w.PreviewID != "" {
		id = *w.PreviewID
	}
	return previewView{
		PreviewID:    id,
		Repository:   w.RepositoryFullName,
		Branch:       w.Branch,
		Status:       w.Status,
		PreviewURL:   w.PreviewURL,
		CurrentPhase: w.CurrentPhase,
		Error:        w.Error,
	}
}

func (e *previewToolExecutor) create(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		Repository string `json:"repository"`
		Branch     string `json:"branch"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.Repository == "" || params.Branch == "" {
		return mcp.ErrorResult("repository and branch are required")
	}

	repoID, repoFullName, err := e.resolveRepository(ctx, params.Repository)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}

	// Branch-existence preflight: the #1 failure mode for local use is a
	// branch that only exists in the local working tree. A distinct error
	// naming the fix lets agents recover without guessing.
	if exists, checkErr := e.branchExists(ctx, repoID, params.Branch); checkErr == nil && !exists {
		return mcp.ErrorResult(fmt.Sprintf(
			"BRANCH_NOT_PUSHED: branch %q does not exist on the remote for %s — run `git push -u origin %s` first, then retry",
			params.Branch, repoFullName, params.Branch))
	}

	var resp struct {
		Data branchPreviewWire `json:"data"`
	}
	err = e.client.Do(ctx, http.MethodPost, "/api/v1/previews",
		map[string]string{"repository_id": repoID, "branch": params.Branch}, &resp)
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview create failed: %s", err))
	}
	return jsonResult(resp.Data.view())
}

func (e *previewToolExecutor) status(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.PreviewID == "" {
		return mcp.ErrorResult("preview_id is required")
	}
	var resp struct {
		Data branchPreviewWire `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/previews/"+params.PreviewID, nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview status failed: %s", err))
	}
	return jsonResult(resp.Data.view())
}

func (e *previewToolExecutor) list(ctx context.Context) *mcp.ToolCallResult {
	var resp struct {
		Data []branchPreviewWire `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/previews", nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview list failed: %s", err))
	}
	views := make([]previewView, 0, len(resp.Data))
	for _, p := range resp.Data {
		views = append(views, p.view())
	}
	return jsonResult(views)
}

func (e *previewToolExecutor) stop(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.PreviewID == "" {
		return mcp.ErrorResult("preview_id is required")
	}
	if err := e.client.Do(ctx, http.MethodPost, "/api/v1/previews/"+params.PreviewID+"/stop", nil, nil); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview stop failed: %s", err))
	}
	return jsonResult(map[string]string{"preview_id": params.PreviewID, "status": "stopping"})
}

// resolveRepository matches a human/agent-supplied name against the org's
// repositories: exact full_name ("acme/webapp") first, then unique
// short-name ("webapp"). Ambiguity and misses return errors that name the
// candidates so agents can self-correct.
func (e *previewToolExecutor) resolveRepository(ctx context.Context, name string) (id, fullName string, err error) {
	var resp struct {
		Data []struct {
			ID       string `json:"id"`
			FullName string `json:"full_name"`
		} `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/repositories", nil, &resp); err != nil {
		return "", "", fmt.Errorf("list repositories: %w", err)
	}

	name = strings.TrimSpace(name)
	var matches []struct{ id, fullName string }
	for _, repo := range resp.Data {
		if strings.EqualFold(repo.FullName, name) {
			return repo.ID, repo.FullName, nil
		}
		if parts := strings.SplitN(repo.FullName, "/", 2); len(parts) == 2 && strings.EqualFold(parts[1], name) {
			matches = append(matches, struct{ id, fullName string }{repo.ID, repo.FullName})
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].id, matches[0].fullName, nil
	case 0:
		available := make([]string, 0, len(resp.Data))
		for _, repo := range resp.Data {
			available = append(available, repo.FullName)
		}
		return "", "", fmt.Errorf("repository %q not found — connected repositories: %s", name, strings.Join(available, ", "))
	default:
		candidates := make([]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, m.fullName)
		}
		return "", "", fmt.Errorf("repository %q is ambiguous — use the full name: %s", name, strings.Join(candidates, ", "))
	}
}

// branchExists checks the remote for the branch. Errors are swallowed by
// the caller (the create call itself will surface a real failure) — this
// is purely the fast-path for the BRANCH_NOT_PUSHED hint.
func (e *previewToolExecutor) branchExists(ctx context.Context, repoID, branch string) (bool, error) {
	var resp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/repositories/"+repoID+"/branches", nil, &resp); err != nil {
		return false, err
	}
	for _, b := range resp.Data {
		if b.Name == branch {
			return true, nil
		}
	}
	return false, nil
}

func jsonResult(v any) *mcp.ToolCallResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("encode result: %s", err))
	}
	return mcp.TextResult(string(data))
}
