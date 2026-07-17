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

// InternalIssueCreator creates issues via the 143 internal API.
// It runs inside the PM sandbox and communicates with the server
// using a short-lived scoped token.
type InternalIssueCreator struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewInternalIssueCreator creates an IssueCreator that calls the internal API.
func NewInternalIssueCreator(token, baseURL string) *InternalIssueCreator {
	return &InternalIssueCreator{
		token:   token,
		baseURL: internalAPIBaseURL(baseURL),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *InternalIssueCreator) Name() string { return "issue" }

func (c *InternalIssueCreator) CreateIssue(ctx context.Context, params CreateIssueParams) (*CreateIssueResult, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := c.baseURL + "/issues"
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

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max response
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("issue creation failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result CreateIssueResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
