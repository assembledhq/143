package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// InternalPullRequestCreator requests PR creation via the 143 internal API.
// It runs inside an agent sandbox with a short-lived scoped token.
type InternalPullRequestCreator struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewInternalPullRequestCreator creates a PullRequestCreator that calls the internal API.
func NewInternalPullRequestCreator(token, baseURL string) *InternalPullRequestCreator {
	return &InternalPullRequestCreator{
		token:   token,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *InternalPullRequestCreator) Name() string { return "session" }

func (c *InternalPullRequestCreator) CreatePullRequest(ctx context.Context, params CreatePullRequestParams) (*CreatePullRequestResult, error) {
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = os.Getenv("143_SESSION_ID")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	params.SessionID = sessionID

	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := c.baseURL + "/sessions/" + sessionID + "/pr"
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
	if resp.StatusCode != http.StatusAccepted {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("pull request creation failed (status %d): %s", resp.StatusCode, bodyStr)
	}

	var result CreatePullRequestResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
