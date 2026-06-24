package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// InternalSlackMessageSender sends Slack messages through the 143 internal API
// so Slack bot tokens remain server-side.
type InternalSlackMessageSender struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewInternalSlackMessageSender(token, baseURL string) *InternalSlackMessageSender {
	return &InternalSlackMessageSender{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *InternalSlackMessageSender) Name() string { return "slack" }

func (s *InternalSlackMessageSender) SendMessage(ctx context.Context, params SendMessageParams) (*SendMessageResult, error) {
	if strings.TrimSpace(params.ChannelID) == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	if strings.TrimSpace(params.Text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/slack/messages", bytes.NewReader(body)) // #nosec G107 -- baseURL is trusted server config.
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
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
		return nil, fmt.Errorf("slack message send failed (status %d): %s", resp.StatusCode, bodyStr)
	}
	var result SendMessageResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
