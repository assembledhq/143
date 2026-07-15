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

type InternalAutomationOutcomeReporter struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewInternalAutomationOutcomeReporter(token, baseURL string) *InternalAutomationOutcomeReporter {
	return &InternalAutomationOutcomeReporter{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *InternalAutomationOutcomeReporter) Name() string {
	return "automation_run"
}

func (r *InternalAutomationOutcomeReporter) ReportAutomationOutcome(ctx context.Context, params ReportAutomationOutcomeParams) (*ReportAutomationOutcomeResult, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal automation outcome request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/automation-runs/current/outcome", bytes.NewReader(body)) // #nosec G107 -- baseURL is trusted server configuration.
	if err != nil {
		return nil, fmt.Errorf("create automation outcome request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send automation outcome request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read automation outcome response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := string(respBody)
		if len(message) > 512 {
			message = message[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("automation outcome report failed (status %d): %s", resp.StatusCode, message)
	}
	var wrapped struct {
		Data ReportAutomationOutcomeResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &wrapped); err != nil {
		return nil, fmt.Errorf("decode automation outcome response: %w", err)
	}
	return &wrapped.Data, nil
}
