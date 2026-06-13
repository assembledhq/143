package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/assembledhq/143/internal/services/mcp"
)

// RemoteToolSource implements mcp.ToolSource against the 143 server's local
// agent gateway. Integration tools are listed from GET /api/v1/cli/tools at
// construction (so availability mirrors the org's connected integrations)
// and executed via POST /api/v1/cli/tools/invoke with the user's bearer
// token — org credentials never exist on this machine. Platform preview
// tools are layered on top and execute client-side against the public
// /api/v1/previews* REST endpoints, which already work under bearer auth.
type RemoteToolSource struct {
	client  *Client
	tools   []mcp.Tool
	preview *previewToolExecutor
}

// NewRemoteToolSource fetches the org's tool list and assembles the source.
func NewRemoteToolSource(ctx context.Context, cfg Config) (*RemoteToolSource, error) {
	client := NewClient(cfg)
	var resp struct {
		Data struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/cli/tools", nil, &resp); err != nil {
		return nil, fmt.Errorf("fetch tool list: %w", err)
	}

	preview := &previewToolExecutor{client: client}
	return &RemoteToolSource{
		client:  client,
		tools:   append(resp.Data.Tools, preview.tools()...),
		preview: preview,
	}, nil
}

func (s *RemoteToolSource) ListTools() []mcp.Tool {
	return s.tools
}

func (s *RemoteToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *mcp.ToolCallResult {
	if s.preview.handles(name) {
		return s.preview.call(ctx, name, args)
	}

	var resp struct {
		Data mcp.ToolCallResult `json:"data"`
	}
	err := s.client.Do(ctx, http.MethodPost, "/api/v1/cli/tools/invoke",
		map[string]any{"tool": name, "args": args}, &resp)
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("tool invocation failed: %s", err))
	}
	return &resp.Data
}
