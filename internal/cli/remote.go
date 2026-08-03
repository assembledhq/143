package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type previewAugmentedToolSource struct {
	base    mcp.ToolSource
	preview *previewToolExecutor
}

func newPreviewAugmentedToolSource(base mcp.ToolSource, client *Client) mcp.ToolSource {
	preview := &previewToolExecutor{client: client.WithRequestTimeout(previewWaitTimeout)}
	return &previewAugmentedToolSource{base: base, preview: preview}
}

// newInternalToolSource layers the internal meta tools, the platform preview
// tools, and — outermost — the org capability filter on top of base. Every
// in-sandbox entry point must build its source through here: the capability
// filter is what enforces the org's tool policy, so a caller that assembles
// previewAugmentedToolSource directly silently exempts itself from it.
//
// The preview executor is returned alongside the source so callers can
// configure it (e.g. attaching --wait progress output) without reaching back
// through the capability wrapper, which has to stay on the outside.
func newInternalToolSource(ctx context.Context, base mcp.ToolSource, token, apiURL string, stderr io.Writer) (mcp.ToolSource, *previewToolExecutor) {
	preview := &previewToolExecutor{client: NewClient(Config{ServerURL: apiURL, Token: token}).WithRequestTimeout(previewWaitTimeout), internal: true}
	var source mcp.ToolSource = &previewAugmentedToolSource{
		base:    mcp.NewInternalMetaToolSource(base, token, apiURL),
		preview: preview,
	}
	snapshot, err := mcp.FetchCapabilitySnapshot(ctx, token, apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "143-tools: capability snapshot unavailable, running without filter: %v\n", err)
	} else if len(snapshot) > 0 {
		source = mcp.NewCapabilityFilteredToolSource(source, mcp.ToolCapabilityPolicy{Capabilities: snapshot})
	}
	return source, preview
}

func (s *previewAugmentedToolSource) ListTools() []mcp.Tool {
	return append(s.base.ListTools(), s.preview.tools()...)
}

func (s *previewAugmentedToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *mcp.ToolCallResult {
	if s.preview.handles(name) {
		return s.preview.call(ctx, name, args)
	}
	return s.base.CallTool(ctx, name, args)
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

	preview := &previewToolExecutor{client: client.WithRequestTimeout(previewWaitTimeout)}
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
