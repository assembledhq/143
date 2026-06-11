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

type InternalEvalCandidateReporter struct {
	token          string
	baseURL        string
	bootstrapRunID string
	client         *http.Client
}

func NewInternalEvalCandidateReporter(token, baseURL string, bootstrapRunID ...string) *InternalEvalCandidateReporter {
	runID := ""
	if len(bootstrapRunID) > 0 {
		runID = strings.TrimSpace(bootstrapRunID[0])
	}
	return &InternalEvalCandidateReporter{
		token:          token,
		baseURL:        strings.TrimRight(baseURL, "/"),
		bootstrapRunID: runID,
		client:         &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *InternalEvalCandidateReporter) Name() string { return "eval" }

func (r *InternalEvalCandidateReporter) AddCandidate(ctx context.Context, params AddEvalCandidateParams) (*AddEvalCandidateResult, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	path := "/eval/candidates"
	if r.bootstrapRunID != "" {
		path = "/evals/bootstrap/" + r.bootstrapRunID + "/candidates"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(body)) // #nosec G107 -- baseURL is trusted server config.
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
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
		return nil, fmt.Errorf("eval candidate add failed (status %d): %s", resp.StatusCode, bodyStr)
	}

	var wrapped struct {
		Data AddEvalCandidateResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &wrapped); err == nil && wrapped.Data.CandidateID != "" {
		return &wrapped.Data, nil
	}
	var result AddEvalCandidateResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
