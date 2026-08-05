package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const internalPullRequestUpdateTimeout = 60 * time.Second

// InternalPullRequestCreator requests PR creation via the 143 internal API.
// It runs inside an agent sandbox with a short-lived scoped token.
type InternalPullRequestCreator struct {
	token        string
	baseURL      string
	client       *http.Client
	updateClient *http.Client
}

// PullRequestCreationError carries the API's structured error code alongside
// the HTTP status. No caller branches on the fields yet — the MCP layer
// formats the error through Error(), which is what puts the code in front of
// the agent — but keeping them typed means a caller that needs to tell a
// policy rejection from a retryable server failure can do so without parsing
// prose.
type PullRequestCreationError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *PullRequestCreationError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("pull request creation failed (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("pull request creation failed (%s, status %d): %s", e.Code, e.StatusCode, e.Message)
}

// PullRequestUpdateError carries a structured internal API failure for a PR
// metadata update.
type PullRequestUpdateError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *PullRequestUpdateError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("pull request update failed (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("pull request update failed (%s, status %d): %s", e.Code, e.StatusCode, e.Message)
}

// NewInternalPullRequestCreator creates a PullRequestCreator that calls the internal API.
func NewInternalPullRequestCreator(token, baseURL string) *InternalPullRequestCreator {
	return &InternalPullRequestCreator{
		token:        token,
		baseURL:      internalAPIBaseURL(baseURL),
		client:       &http.Client{Timeout: 10 * time.Second},
		updateClient: &http.Client{Timeout: internalPullRequestUpdateTimeout},
	}
}

func (c *InternalPullRequestCreator) Name() string { return "session" }

func (c *InternalPullRequestCreator) CreatePullRequest(ctx context.Context, params CreatePullRequestParams) (*CreatePullRequestResult, error) {
	sessionID := strings.TrimSpace(params.SessionID)
	params.SessionID = sessionID

	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// The current-session endpoint derives the authoritative session ID from
	// the bearer token. This keeps PR creation working even when an agent
	// runtime filters or drops optional environment variables.
	reqURL := c.baseURL + "/session/pr"
	if sessionID != "" {
		reqURL = c.baseURL + "/sessions/" + sessionID + "/pr"
	}
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
		var apiError struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiError) == nil && (apiError.Error.Code != "" || apiError.Error.Message != "") {
			return nil, &PullRequestCreationError{
				StatusCode: resp.StatusCode,
				Code:       apiError.Error.Code,
				Message:    apiError.Error.Message,
			}
		}
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, &PullRequestCreationError{StatusCode: resp.StatusCode, Message: bodyStr}
	}

	var result CreatePullRequestResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// UpdatePullRequest updates the current session's existing primary PR through
// the internal API. Large descriptions can be supplied via BodyFile so agents
// do not need to squeeze markdown through shell quoting.
func (c *InternalPullRequestCreator) UpdatePullRequest(ctx context.Context, params UpdatePullRequestParams) (*UpdatePullRequestResult, error) {
	sessionID := strings.TrimSpace(params.SessionID)
	params.SessionID = sessionID
	params.BodyFile = strings.TrimSpace(params.BodyFile)
	if params.Body != nil && params.BodyFile != "" {
		return nil, fmt.Errorf("body and body_file are mutually exclusive")
	}
	if params.BodyFile != "" {
		data, err := os.ReadFile(params.BodyFile)
		if err != nil {
			return nil, fmt.Errorf("read body_file: %w", err)
		}
		body := string(data)
		params.Body = &body
	}
	if params.Title == nil && params.Body == nil {
		return nil, fmt.Errorf("title or body is required")
	}

	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	reqURL := c.baseURL + "/session/pr"
	if sessionID != "" {
		reqURL = c.baseURL + "/sessions/" + sessionID + "/pr"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body)) // #nosec G107 -- URL from trusted server config
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.updateClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var apiError struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiError) == nil && (apiError.Error.Code != "" || apiError.Error.Message != "") {
			return nil, &PullRequestUpdateError{
				StatusCode: resp.StatusCode,
				Code:       apiError.Error.Code,
				Message:    apiError.Error.Message,
			}
		}
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, &PullRequestUpdateError{StatusCode: resp.StatusCode, Message: bodyStr}
	}

	var result UpdatePullRequestResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
