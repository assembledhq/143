package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/internalapi"
	"github.com/assembledhq/143/internal/version"
)

// Client is the minimal authenticated HTTP client for the 143 server. Every
// request carries the bearer token, a `143-tools/<version>` User-Agent (the
// server's CLIVersionGate keys on it), and the active-org header when set.
type Client struct {
	baseURL string
	token   string
	orgID   string
	http    *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		baseURL: internalapi.NormalizeBaseURL(cfg.ServerURL),
		token:   cfg.Token,
		orgID:   cfg.OrgID,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// APIError is a structured error response from the server.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("server returned HTTP %d", e.Status)
}

// Do issues a JSON request and decodes the response into out (which may be
// nil). Non-2xx responses are returned as *APIError with the server's error
// code/message when present.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "143-tools/"+version.BuildSHA)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.orgID != "" {
		req.Header.Set("X-Active-Org-ID", c.orgID)
	}

	resp, err := c.http.Do(req) // #nosec G704 -- baseURL comes from the user's own config
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal(data, &envelope); jsonErr == nil {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
		}
		return apiErr
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
