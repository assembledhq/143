package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
		Tool{Name: "code_review_history_list", Description: "List past 143 automated code reviews for this repository, newest first. Each row has the PR, decision, status, and policy version used. Use this to audit how the review policy behaved before proposing policy adjustments.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"decision":       {Type: "string", Description: "Filter by review decision", Enum: []string{"approved", "comment_only", "needs_human_review", "blocked"}},
			"status":         {Type: "string", Description: "Filter by review run status", Enum: []string{"queued", "running", "completed", "failed", "stale", "cancelled"}},
			"outcome":        {Type: "string", Description: "Filter by posted outcome", Enum: []string{"automatically_approved", "completed_not_approved"}},
			"acceptable":     {Type: "boolean", Description: "Filter by risk verdict: true for reviews judged acceptable, false for reviews flagged for humans"},
			"search":         {Type: "string", Description: "Match PR title, repo name, session title, or PR number"},
			"created_after":  {Type: "string", Description: "Only reviews created after this RFC3339 timestamp"},
			"created_before": {Type: "string", Description: "Only reviews created before this RFC3339 timestamp"},
			"cursor":         {Type: "string", Description: "Pagination cursor (id of the last row from the previous page)"},
			"limit":          {Type: "number", Description: "Max results (default 20, max 50)", Default: 20},
		}}},
		Tool{Name: "code_review_history_get", Description: "Get one past code review in full: final review body, every finding (severity, confidence, file, whether it was posted inline), and each reviewer agent's verdict. Look up by the session_id from a list row.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"session_id":         {Type: "string", Description: "Code review session ID from code_review_history_list"},
			"include_raw_output": {Type: "boolean", Description: "Include each reviewer agent's raw output (truncated)", Default: false},
			"include_prompts":    {Type: "boolean", Description: "Include the rendered reviewer prompts (truncated)", Default: false},
		}, Required: []string{"session_id"}}},
		Tool{Name: "code_review_history_policy", Description: "Get the org's code review policy: the active version by default, or a specific historical version via policy_id (from a review's policy_id field). Compare the policy that governed past reviews against their outcomes to judge whether the policy is correct.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"policy_id": {Type: "string", Description: "Policy version UUID; omit for the active policy"},
		}}},
		Tool{Name: "code_review_history_update_policy", Description: "Apply a versioned update to the org's code review policy. Supplied config keys merge onto the active policy; omitted fields keep their current values. Requires the code_review_policy_management write capability (default off; request it with `143-tools capability request`). Pass the active policy version you read — the call fails with the current version if someone changed the policy in between. Every update is a new audited version humans can inspect and roll back.", InputSchema: ToolSchema{Type: "object", Properties: map[string]SchemaProperty{
			"config":           {Type: "string", Description: "JSON object of policy fields to change, same shape as the policy tool's config output (e.g. '{\"review_instructions\":\"...\"}')"},
			"expected_version": {Type: "number", Description: "Active policy version this change is based on (0 if the org has never saved a policy)"},
			"reason":           {Type: "string", Description: "Why the policy is changing; recorded in the audit log for humans (max 2000 characters)"},
		}, Required: []string{"config", "expected_version", "reason"}}},
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
	case "code_review_history_list":
		return s.codeReviewHistoryList(ctx, args)
	case "code_review_history_get":
		var in struct {
			SessionID        string `json:"session_id"`
			IncludeRawOutput bool   `json:"include_raw_output"`
			IncludePrompts   bool   `json:"include_prompts"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &in); err != nil {
				return ErrorResult("INVALID_ARGUMENTS: invalid JSON")
			}
		}
		if strings.TrimSpace(in.SessionID) == "" {
			return ErrorResult("INVALID_ARGUMENTS: session_id is required")
		}
		q := url.Values{}
		if in.IncludeRawOutput {
			q.Set("include_raw_output", "true")
		}
		if in.IncludePrompts {
			q.Set("include_prompts", "true")
		}
		return s.do(ctx, http.MethodGet, "/api/v1/internal/code-reviews/"+url.PathEscape(in.SessionID), q, nil)
	case "code_review_history_policy":
		var in struct {
			PolicyID string `json:"policy_id"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &in); err != nil {
				return ErrorResult("INVALID_ARGUMENTS: invalid JSON")
			}
		}
		if policyID := strings.TrimSpace(in.PolicyID); policyID != "" {
			return s.do(ctx, http.MethodGet, "/api/v1/internal/code-reviews/policies/"+url.PathEscape(policyID), nil, nil)
		}
		return s.do(ctx, http.MethodGet, "/api/v1/internal/code-reviews/policy", nil, nil)
	case "code_review_history_update_policy":
		var in struct {
			Config          string `json:"config"`
			ExpectedVersion *int   `json:"expected_version"`
			Reason          string `json:"reason"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &in); err != nil {
				return ErrorResult("INVALID_ARGUMENTS: invalid JSON")
			}
		}
		configRaw := strings.TrimSpace(in.Config)
		var configProbe map[string]any
		if configRaw == "" || json.Unmarshal([]byte(configRaw), &configProbe) != nil || len(configProbe) == 0 {
			return ErrorResult("INVALID_ARGUMENTS: config must be a JSON object with at least one policy field")
		}
		if in.ExpectedVersion == nil || *in.ExpectedVersion < 0 {
			return ErrorResult("INVALID_ARGUMENTS: expected_version is required (0 if the org has never saved a policy)")
		}
		if strings.TrimSpace(in.Reason) == "" {
			return ErrorResult("INVALID_ARGUMENTS: reason is required")
		}
		body, err := json.Marshal(map[string]any{
			"config":           json.RawMessage(configRaw),
			"expected_version": *in.ExpectedVersion,
			"reason":           in.Reason,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("INVALID_ARGUMENTS: %s", err))
		}
		return s.do(ctx, http.MethodPut, "/api/v1/internal/code-reviews/policy", nil, body)
	default:
		return s.base.CallTool(ctx, name, args)
	}
}

func (s *internalMetaToolSource) codeReviewHistoryList(ctx context.Context, args json.RawMessage) *ToolCallResult {
	var in map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ErrorResult("INVALID_ARGUMENTS: invalid JSON")
		}
	}
	q := url.Values{}
	for _, key := range []string{"decision", "status", "outcome", "search", "created_after", "created_before", "cursor"} {
		if v, ok := in[key].(string); ok && strings.TrimSpace(v) != "" {
			q.Set(key, v)
		}
	}
	if v, ok := in["acceptable"]; ok {
		switch typed := v.(type) {
		case bool:
			q.Set("acceptable", strconv.FormatBool(typed))
		case string:
			if strings.TrimSpace(typed) != "" {
				q.Set("acceptable", typed)
			}
		}
	}
	// json.Unmarshal into map[string]any only ever yields float64 or string
	// for numeric-ish inputs, so those are the only limit shapes handled.
	if v, ok := in["limit"]; ok {
		switch typed := v.(type) {
		case float64:
			q.Set("limit", fmt.Sprintf("%.0f", typed))
		case string:
			q.Set("limit", typed)
		}
	}
	return s.do(ctx, http.MethodGet, "/api/v1/internal/code-reviews", q, nil)
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
