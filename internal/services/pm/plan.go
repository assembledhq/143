package pm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// apiKeyPattern matches common API key shapes (Anthropic `sk-ant-…`, OpenAI
// `sk-…`, and similar) so we can redact them before embedding CLI output in
// error messages that land in logs.
var apiKeyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`)

func parsePlan(output string) (*Plan, error) {
	start := strings.Index(output, "<pm-plan>")
	end := strings.Index(output, "</pm-plan>")
	if start == -1 || end == -1 || end <= start {
		// Surface common upstream failures (e.g. Claude Code CLI auth errors)
		// so they don't get buried behind a generic "tags not found".
		if strings.TrimSpace(output) == "" {
			return nil, fmt.Errorf("pm plan tags not found: agent produced no output")
		}
		lower := strings.ToLower(output)
		if strings.Contains(lower, "not logged in") ||
			strings.Contains(lower, "please run /login") ||
			strings.Contains(lower, "authentication_failed") ||
			strings.Contains(lower, "invalid api key") ||
			strings.Contains(lower, "invalid_api_key") {
			return nil, fmt.Errorf("pm agent not authenticated — configure an Anthropic API key for this org: %s", excerpt(output, 200))
		}
		return nil, fmt.Errorf("pm plan tags not found in agent output: %s", excerpt(output, 500))
	}

	content := strings.TrimSpace(output[start+len("<pm-plan>") : end])
	if content == "" {
		return nil, fmt.Errorf("pm plan content is empty")
	}

	var payload struct {
		Analysis       string          `json:"analysis"`
		Tasks          []Task          `json:"tasks"`
		Clusters       []Cluster       `json:"clusters"`
		Skip           []SkipEntry     `json:"skip"`
		ProjectPlans   []ProjectPlan   `json:"project_plans,omitempty"`
		LinearActions  []LinearAction  `json:"linear_actions,omitempty"`
		SlotAllocation *SlotAllocation `json:"slot_allocation,omitempty"`
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
		Analysis:       payload.Analysis,
		Tasks:          payload.Tasks,
		Clusters:       payload.Clusters,
		SkippedIssues:  payload.Skip,
		ProjectPlans:   payload.ProjectPlans,
		LinearActions:  payload.LinearActions,
		SlotAllocation: payload.SlotAllocation,
	}, nil
}

// excerpt returns a trimmed, single-line preview of s capped at max runes so
// error messages don't dump megabytes of CLI output into logs. API-key-shaped
// tokens are redacted before truncation so a CLI that echoes its config can't
// leak credentials into log streams.
func excerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = apiKeyPattern.ReplaceAllString(s, "sk-***REDACTED***")
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}
