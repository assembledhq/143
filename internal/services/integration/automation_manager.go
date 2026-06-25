package integration

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
)

// InternalAutomationManager manages automations via the 143 internal API.
type InternalAutomationManager struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewInternalAutomationManager(token, baseURL string) *InternalAutomationManager {
	return &InternalAutomationManager{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (m *InternalAutomationManager) Name() string { return "automation" }

func (m *InternalAutomationManager) CreateAutomation(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	return m.do(ctx, http.MethodPost, "/automations", payload, http.StatusCreated)
}

func (m *InternalAutomationManager) UpdateAutomation(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("automation_id is required")
	}
	return m.do(ctx, http.MethodPatch, "/automations/"+url.PathEscape(id), payload, http.StatusOK)
}

func (m *InternalAutomationManager) RunAutomation(ctx context.Context, id string) (json.RawMessage, error) {
	return m.postAction(ctx, id, "run", http.StatusAccepted)
}

func (m *InternalAutomationManager) PauseAutomation(ctx context.Context, id string) (json.RawMessage, error) {
	return m.postAction(ctx, id, "pause", http.StatusOK)
}

func (m *InternalAutomationManager) ResumeAutomation(ctx context.Context, id string) (json.RawMessage, error) {
	return m.postAction(ctx, id, "resume", http.StatusOK)
}

func (m *InternalAutomationManager) postAction(ctx context.Context, id, action string, expectedStatus int) (json.RawMessage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("automation_id is required")
	}
	return m.do(ctx, http.MethodPost, "/automations/"+url.PathEscape(id)+"/"+action, json.RawMessage(`{}`), expectedStatus)
}

func (m *InternalAutomationManager) do(ctx context.Context, method, path string, payload json.RawMessage, expectedStatus int) (json.RawMessage, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, bytes.NewReader(payload)) // #nosec G107 -- URL from trusted server config
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != expectedStatus {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("automation request failed (status %d): %s", resp.StatusCode, bodyStr)
	}
	return json.RawMessage(respBody), nil
}
