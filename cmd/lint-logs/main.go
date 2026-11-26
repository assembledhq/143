// Command lint-logs keeps personal data out of new log lines. It scans
// non-test Go files for zerolog calls and fails when a field key or a logged
// value names personal data: email addresses, people's names, usernames and
// logins, IP addresses, user agents, phone numbers, postal addresses, and
// government, financial, or birth-date identifiers.
//
// Log the stable ID instead (user_id, slack_user_id): an ID lets an operator
// join a log line back to the database without copying the data itself into
// the log pipeline, where it is retained, shipped, and searchable.
//
// A log statement that must carry one of these values opts out with a comment
// on the line above it (the reason clause is required):
//
//	// lint:allow-log-pii reason="abuse investigation needs the client IP"
//	logger.Warn().Str("ip", ip).Msg("rate limit exceeded")
//
// Findings that predate this lint are listed in cmd/lint-logs/baseline.txt.
// The lint fails on any finding missing from the baseline and on any baseline
// entry that no longer matches the code. After fixing a pre-existing finding,
// drop its entry with `go run ./cmd/lint-logs -prune-baseline`. Pruning never
// adds entries, so the list can only shrink.
//
// Run via `make lint-logs` or directly: `go run ./cmd/lint-logs`.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultBaselinePath = "cmd/lint-logs/baseline.txt"
	baselineHeader      = `# Log statements that wrote personal data before cmd/lint-logs existed.
# Do not add entries: fix the log line, or annotate it with
# // lint:allow-log-pii reason="..." when the value is genuinely required.
# Drop entries for fixed findings with: go run ./cmd/lint-logs -prune-baseline
`
)

var skippedDirs = map[string]bool{
	".git":         true,
	"bin":          true,
	"frontend":     true,
	"node_modules": true,
	"testdata":     true,
	"vendor":       true,
}

func main() {
	root := flag.String("root", ".", "repository root to scan")
	baselinePath := flag.String("baseline", defaultBaselinePath, "baseline file, relative to -root")
	pruneBaseline := flag.Bool("prune-baseline", false, "remove baseline entries that no longer match the code")
	flag.Parse()

	os.Exit(run(*root, *baselinePath, *pruneBaseline, os.Stdout, os.Stderr))
}

func run(root, baselinePath string, pruneBaseline bool, stdout, stderr io.Writer) int {
	files, err := goSourceFiles(root)
	if err != nil {
		fmt.Fprintf(stderr, "lint-logs: %v\n", err)
		return 2
	}

	var findings []finding
	var markerErrors []string
	for _, rel := range files {
		// #nosec G304 -- rel comes from walking the repository tree, not user input.
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintf(stderr, "lint-logs: read %s: %v\n", rel, err)
			return 2
		}
		result, err := checkSource(rel, src)
		if err != nil {
			fmt.Fprintf(stderr, "lint-logs: %v\n", err)
			return 2
		}
		findings = append(findings, result.findings...)
		markerErrors = append(markerErrors, result.markerErrors...)
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})

	baselineFile := filepath.Join(root, baselinePath)
	baseline, err := readBaseline(baselineFile)
	if err != nil {
		fmt.Fprintf(stderr, "lint-logs: %v\n", err)
		return 2
	}
	matched, newFindings, stale := compareToBaseline(findings, baseline)

	if pruneBaseline && len(stale) > 0 {
		if err := os.WriteFile(baselineFile, []byte(formatBaseline(matched)), 0o600); err != nil {
			fmt.Fprintf(stderr, "lint-logs: write baseline: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "lint-logs: removed %d stale baseline entr%s from %s\n", len(stale), plural(len(stale), "y", "ies"), baselinePath)
		stale = nil
	}

	if len(newFindings) == 0 && len(stale) == 0 && len(markerErrors) == 0 {
		fmt.Fprintf(stdout, "lint-logs: OK — scanned %d file(s); %d pre-existing finding(s) remain in %s\n", len(files), len(matched), baselinePath)
		return 0
	}

	if len(newFindings) > 0 {
		fmt.Fprintln(stderr, "lint-logs: log statements that write personal data")
		for _, f := range newFindings {
			fmt.Fprintln(stderr, "  "+f.String())
		}
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Logs are retained, shipped off-host, and searchable, so they must not carry")
		fmt.Fprintln(stderr, "personal data. Log the stable ID instead (user_id, slack_user_id), or drop the")
		fmt.Fprintln(stderr, "field. If the value is genuinely required, add a comment above the statement")
		fmt.Fprintln(stderr, "(the reason clause is required):")
		fmt.Fprintln(stderr, `  // lint:allow-log-pii reason="..."`)
	}
	if len(markerErrors) > 0 {
		fmt.Fprintln(stderr, "lint-logs: malformed opt-out comments")
		for _, m := range markerErrors {
			fmt.Fprintln(stderr, "  "+m)
		}
		fmt.Fprintln(stderr, `An opt-out must read: // lint:allow-log-pii reason="<why this value is required>"`)
	}
	if len(stale) > 0 {
		fmt.Fprintf(stderr, "lint-logs: %s lists findings that no longer exist\n", baselinePath)
		for _, s := range stale {
			fmt.Fprintln(stderr, "  "+s)
		}
		fmt.Fprintln(stderr, "Remove them with: go run ./cmd/lint-logs -prune-baseline")
	}
	return 1
}

func goSourceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skippedDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
