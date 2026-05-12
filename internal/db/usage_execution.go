package db

import "strings"

// UsageExecutionFilters narrows execution-backed usage analytics queries.
type UsageExecutionFilters struct {
	Agent     *string
	Model     *string
	Reasoning *string
}

func (f UsageExecutionFilters) HasAny() bool {
	return f.Agent != nil || f.Model != nil || f.Reasoning != nil
}

func NormalizeUsageExecutionFilterValue(raw string) *string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	return &value
}
