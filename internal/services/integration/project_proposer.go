package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// InternalProjectProposer creates project proposals via the 143 internal API.
// It runs inside the PM sandbox and communicates with the server
// using a short-lived scoped token.
type InternalProjectProposer struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewInternalProjectProposer creates a ProjectProposer that calls the internal API.
func NewInternalProjectProposer(token, baseURL string) *InternalProjectProposer {
	return &InternalProjectProposer{
		token:   token,
		baseURL: internalAPIBaseURL(baseURL),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *InternalProjectProposer) Name() string { return "project" }

func (c *InternalProjectProposer) ProposeProject(ctx context.Context, params ProposeProjectParams) (*ProposeProjectResult, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := c.baseURL + "/projects/propose"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body)) // #nosec G107 -- URL from trusted server config
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		// Truncate response body to avoid leaking large/sensitive payloads into logs.
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("project proposal failed (status %d): %s", resp.StatusCode, bodyStr)
	}

	var result ProposeProjectResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
