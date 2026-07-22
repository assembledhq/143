package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/internalapi"
	"github.com/assembledhq/143/internal/models"
)

type internalMetaToolSource struct {
	base   ToolSource
	token  string
	apiURL string
	client *http.Client
}

func NewInternalMetaToolSource(base ToolSource, token, apiURL string) ToolSource {
	return &internalMetaToolSource{
		base:   base,
		token:  token,
		apiURL: internalapi.NormalizeBaseURL(apiURL),
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *internalMetaToolSource) ListTools() []Tool {
	tools := append([]Tool{}, s.base.ListTools()...)
	if s.token == "" || s.apiURL == "" {
		return tools
	}
	tools = append(tools,
		Tool{Name: "capability_list", Description: "List the current agent run capability snapshot and requestable capabilities.", InputSchema: ToolSchema{Type: "object"}},
		Tool{Name: "capability_request", Description: "Request an additional capability for the current session through human approval.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"capability_id": {Type: "string", Description: "Capability ID to request"},
			"access_level":  {Type: "string", Description: "Minimum access level", Enum: []string{"read", "write", "publish"}, Default: "read"},
			"reason":        {Type: "string", Description: "Why this capability is needed"},
		}, Required: []string{"capability_id", "access_level", "reason"}}},
		Tool{Name: "session_history_search", Description: "Search prior 143 sessions for this org and repository.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"q":              {Type: "string", Description: "Search text"},
			"status":         {Type: "string", Description: "Session status filter"},
			"created_after":  {Type: "string", Description: "Only sessions created after this RFC3339 timestamp"},
			"created_before": {Type: "string", Description: "Only sessions created before this RFC3339 timestamp"},
			"limit":          {Type: "number", Description: "Max results", Default: 10},
			"cursor":         {Type: "string", Description: "Pagination cursor"},
		}}},
		Tool{Name: "session_history_get", Description: "Get a prior 143 session summary by ID.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"session_id": {Type: "string", Description: "Session ID"},
		}, Required: []string{"session_id"}}},
		Tool{Name: "session_history_messages", Description: "List raw messages for a selected prior session thread.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"session_id": {Type: "string", Description: "Session ID"},
			"thread_id":  {Type: "string", Description: "Thread ID"},
		}, Required: []string{"session_id", "thread_id"}}},
	)
	return tools
}

func (s *internalMetaToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	switch name {
	case "capability_list":
		return s.do(ctx, http.MethodGet, "/api/v1/internal/agent-capabilities/effective", nil, nil)
	case "capability_request":
		return s.do(ctx, http.MethodPost, "/api/v1/internal/agent-capabilities/requests", nil, args)
	case "session_history_search":
		return s.sessionHistorySearch(ctx, args)
	case "session_history_get":
		var in struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.SessionID) == "" {
			return ErrorResult("INVALID_ARGUMENTS: session_id is required")
		}
		return s.do(ctx, http.MethodGet, "/api/v1/internal/session-history/"+url.PathEscape(in.SessionID), nil, nil)
	case "session_history_messages":
		var in struct {
			SessionID string `json:"session_id"`
			ThreadID  string `json:"thread_id"`
		}
		if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.ThreadID) == "" {
			return ErrorResult("INVALID_ARGUMENTS: session_id and thread_id are required")
		}
		path := "/api/v1/internal/session-history/" + url.PathEscape(in.SessionID) + "/threads/" + url.PathEscape(in.ThreadID) + "/messages"
		return s.do(ctx, http.MethodGet, path, nil, nil)
	default:
		return s.base.CallTool(ctx, name, args)
	}
}

func (s *internalMetaToolSource) sessionHistorySearch(ctx context.Context, args json.RawMessage) *ToolCallResult {
	var in map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ErrorResult("INVALID_ARGUMENTS: invalid JSON")
		}
	}
	q := url.Values{}
	for _, key := range []string{"q", "status", "created_after", "created_before", "cursor"} {
		if v, ok := in[key].(string); ok && strings.TrimSpace(v) != "" {
			q.Set(key, v)
		}
	}
	if v, ok := in["limit"]; ok {
		switch typed := v.(type) {
		case float64:
			q.Set("limit", fmt.Sprintf("%.0f", typed))
		case json.Number:
			q.Set("limit", typed.String())
		case string:
			q.Set("limit", typed)
		}
	}
	return s.do(ctx, http.MethodGet, "/api/v1/internal/session-history/search", q, nil)
}

func (s *internalMetaToolSource) do(ctx context.Context, method, path string, q url.Values, body json.RawMessage) *ToolCallResult {
	if s.token == "" || s.apiURL == "" {
		return ErrorResult("INTERNAL_API_UNCONFIGURED: INTERNAL_API_TOKEN and INTERNAL_API_URL are required")
	}
	u := s.apiURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return ErrorResult(fmt.Sprintf("INTERNAL_API_ERROR: %s", err))
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("INTERNAL_API_ERROR: %s", err))
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ErrorResult(fmt.Sprintf("INTERNAL_API_ERROR: %s", err))
	}
	if resp.StatusCode >= 400 {
		return ErrorResult(string(out))
	}
	return TextResult(string(out))
}

func FetchCapabilitySnapshot(ctx context.Context, token, apiURL string) ([]models.AgentCapabilitySnapshotItem, error) {
	source := &internalMetaToolSource{token: token, apiURL: internalapi.NormalizeBaseURL(apiURL), client: &http.Client{Timeout: 30 * time.Second}}
	result := source.do(ctx, http.MethodGet, "/api/v1/internal/agent-capabilities/effective", nil, nil)
	if result == nil || len(result.Content) == 0 {
		return nil, fmt.Errorf("capability list failed: empty response")
	}
	if result.IsError {
		return nil, fmt.Errorf("capability list failed: %s", result.Content[0].Text)
	}
	var resp struct {
		Data struct {
			Snapshot []models.AgentCapabilitySnapshotItem `json:"snapshot"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &resp); err != nil {
		return nil, err
	}
	return resp.Data.Snapshot, nil
}
