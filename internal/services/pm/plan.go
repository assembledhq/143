package pm

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parsePlan(output string) (*Plan, error) {
	start := strings.Index(output, "<pm-plan>")
	end := strings.Index(output, "</pm-plan>")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("pm plan tags not found")
	}

	content := strings.TrimSpace(output[start+len("<pm-plan>") : end])
	if content == "" {
		return nil, fmt.Errorf("pm plan content is empty")
	}

	var payload struct {
		Analysis string      `json:"analysis"`
		Tasks    []Task      `json:"tasks"`
		Clusters []Cluster   `json:"clusters"`
		Skip     []SkipEntry `json:"skip"`
	}

	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("parse pm plan json: %w", err)
	}

	for i, task := range payload.Tasks {
		if err := task.Complexity.Validate(); err != nil {
			return nil, fmt.Errorf("task %d complexity: %w", i, err)
		}
		if err := task.Confidence.Validate(); err != nil {
			return nil, fmt.Errorf("task %d confidence: %w", i, err)
		}
	}
	for i, skip := range payload.Skip {
		if err := skip.Reason.Validate(); err != nil {
			return nil, fmt.Errorf("skip %d reason: %w", i, err)
		}
	}

	return &Plan{
		Analysis:      payload.Analysis,
		Tasks:         payload.Tasks,
		Clusters:      payload.Clusters,
		SkippedIssues: payload.Skip,
	}, nil
}
