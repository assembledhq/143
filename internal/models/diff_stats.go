package models

import (
	"encoding/json"
	"strings"
)

// ComputeDiffStats parses a standard unified diff string and returns JSON-encoded stats.
// It assumes "diff --git" format and counts lines starting with +/- (excluding +++/---
// markers). Binary diffs and combined diff formats are not handled — binary files will
// report 0 added/removed but will be counted as a changed file.
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
