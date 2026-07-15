package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type InternalSessionTabManager struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewInternalSessionTabManager(token, baseURL string) *InternalSessionTabManager {
	return &InternalSessionTabManager{
		token:   token,
		baseURL: internalAPIBaseURL(baseURL),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (m *InternalSessionTabManager) Name() string { return "session_tabs" }

func (m *InternalSessionTabManager) ListTabs(ctx context.Context, params ListSessionTabsParams) (json.RawMessage, error) {
	q := url.Values{}
	if params.IncludeArchived {
		q.Set("include_archived", "true")
	}
	return m.do(ctx, http.MethodGet, "/session-tabs", q, nil)
}

func (m *InternalSessionTabManager) GetTab(ctx context.Context, params GetSessionTabParams) (json.RawMessage, error) {
	if strings.TrimSpace(params.TabID) == "" {
		return nil, fmt.Errorf("tab_id is required")
	}
	return m.do(ctx, http.MethodGet, "/session-tabs/"+url.PathEscape(params.TabID), nil, nil)
}

func (m *InternalSessionTabManager) CreateTab(ctx context.Context, params CreateSessionTabParams) (json.RawMessage, error) {
	body := map[string]any{
		"label":        params.Label,
		"agent_type":   params.Agent,
		"model":        params.Model,
		"instructions": params.Instructions,
	}
	return m.do(ctx, http.MethodPost, "/session-tabs", nil, body)
}

func (m *InternalSessionTabManager) SendTabMessage(ctx context.Context, params SendSessionTabMessageParams) (json.RawMessage, error) {
	if strings.TrimSpace(params.TabID) == "" {
		return nil, fmt.Errorf("tab_id is required")
	}
	message := params.Message
	if message == "" && params.MessageFile != "" {
		data, err := os.ReadFile(params.MessageFile)
		if err != nil {
			return nil, fmt.Errorf("read message_file: %w", err)
		}
		message = string(data)
	}
	if strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("message is required unless message_file is supplied")
	}
	body := map[string]any{
		"message":           message,
		"client_message_id": sessionTabClientMessageID(params.ClientMessageID),
	}
	return m.do(ctx, http.MethodPost, "/session-tabs/"+url.PathEscape(params.TabID)+"/messages", nil, body)
}

func sessionTabClientMessageID(existing string) string {
	if strings.TrimSpace(existing) != "" {
		return existing
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("agent-tool-%d", time.Now().UnixNano())
	}
	return "agent-tool-" + hex.EncodeToString(raw[:])
}

func (m *InternalSessionTabManager) ListTabMessages(ctx context.Context, params ListSessionTabMessagesParams) (json.RawMessage, error) {
	if strings.TrimSpace(params.TabID) == "" {
		return nil, fmt.Errorf("tab_id is required")
	}
	q := url.Values{}
	if params.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", params.Limit))
	}
	if params.Before != "" {
		q.Set("before", params.Before)
	}
	if params.IncludeToolEvents {
		q.Set("include_tool_events", "true")
	}
	return m.do(ctx, http.MethodGet, "/session-tabs/"+url.PathEscape(params.TabID)+"/messages", q, nil)
}

func (m *InternalSessionTabManager) do(ctx context.Context, method, path string, q url.Values, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	reqURL := m.baseURL + path
	if len(q) > 0 {
		reqURL += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader) // #nosec G107 -- baseURL is trusted server config.
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("session tab tool failed (status %d): %s", resp.StatusCode, bodyStr)
	}
	return json.RawMessage(respBody), nil
}
