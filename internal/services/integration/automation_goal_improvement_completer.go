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

	"github.com/google/uuid"
)

type InternalAutomationGoalImprovementCompleter struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewInternalAutomationGoalImprovementCompleter(token, baseURL string) *InternalAutomationGoalImprovementCompleter {
	return &InternalAutomationGoalImprovementCompleter{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *InternalAutomationGoalImprovementCompleter) Name() string {
	return "automation_goal_improvement"
}

func (c *InternalAutomationGoalImprovementCompleter) CompleteGoalImprovement(ctx context.Context, params CompleteAutomationGoalImprovementParams) (*CompleteAutomationGoalImprovementResult, error) {
	if _, err := uuid.Parse(params.ImprovementID); err != nil {
		return nil, fmt.Errorf("improvement_id must be a valid UUID: %w", err)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/automation-goal-improvements/"+params.ImprovementID+"/complete", bytes.NewReader(body)) // #nosec G107 -- baseURL is trusted server config; ImprovementID is validated as UUID above.
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
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
		return nil, fmt.Errorf("automation goal improvement complete failed (status %d): %s", resp.StatusCode, bodyStr)
	}

	var wrapped struct {
		Data CompleteAutomationGoalImprovementResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &wrapped); err == nil && wrapped.Data.ImprovementID != "" {
		return &wrapped.Data, nil
	}
	var result CompleteAutomationGoalImprovementResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
