package models

import (
	"encoding/json"
	"strings"
)

// ComputeDiffStats parses a unified diff string and returns JSON-encoded stats.
// Returns nil when the diff is empty.
func ComputeDiffStats(diff string) json.RawMessage {
	if diff == "" {
		return nil
	}

	var added, removed, filesChanged int
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git") {
			filesChanged++
		} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}

	stats := map[string]int{
		"added":         added,
		"removed":       removed,
		"files_changed": filesChanged,
	}
	b, _ := json.Marshal(stats)
	return json.RawMessage(b)
}
