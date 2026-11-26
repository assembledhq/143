package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

func formatBaseline(findings []finding) string {
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, f.baselineKey())
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(baselineHeader)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("\n")
	}
	return b.String()
}

func readBaseline(path string) (map[string]int, error) {
	// #nosec G304 -- path is the repository's baseline file, not user input.
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open baseline: %w", err)
	}
	defer func() { _ = f.Close() }()

	entries := map[string]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries[line]++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	return entries, nil
}

// compareToBaseline matches findings to baseline entries by file, function,
// and logged subject rather than line number, so unrelated edits that shift
// lines do not churn the baseline. Entries are counted, so a function that
// already logged an email once cannot add a second email log.
func compareToBaseline(findings []finding, baseline map[string]int) (matched, newFindings []finding, stale []string) {
	remaining := make(map[string]int, len(baseline))
	for k, v := range baseline {
		remaining[k] = v
	}
	for _, f := range findings {
		key := f.baselineKey()
		if remaining[key] > 0 {
			remaining[key]--
			matched = append(matched, f)
			continue
		}
		newFindings = append(newFindings, f)
	}
	for k, v := range remaining {
		for i := 0; i < v; i++ {
			stale = append(stale, strings.ReplaceAll(k, "\t", "  "))
		}
	}
	sort.Strings(stale)
	return matched, newFindings, stale
}
