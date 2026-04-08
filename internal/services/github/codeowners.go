package github

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// CodeownersRule is a single CODEOWNERS entry: a file pattern and its owners.
type CodeownersRule struct {
	Pattern string   // e.g. "*.go", "/docs/", "internal/services/"
	Owners  []string // GitHub usernames or team slugs (without @)
}

// ParseCodeowners parses a CODEOWNERS file into rules.
// Rules are returned in order; later rules take precedence (matching GitHub behavior).
func ParseCodeowners(content string) []CodeownersRule {
	var rules []CodeownersRule
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		pattern := parts[0]
		owners := make([]string, 0, len(parts)-1)
		for _, o := range parts[1:] {
			// Strip leading @
			o = strings.TrimPrefix(o, "@")
			if o != "" {
				owners = append(owners, o)
			}
		}
		if len(owners) > 0 {
			rules = append(rules, CodeownersRule{Pattern: pattern, Owners: owners})
		}
	}
	return rules
}

// MatchOwners returns the owners for a given file path by evaluating CODEOWNERS rules.
// Last matching rule wins (GitHub semantics).
func MatchOwners(rules []CodeownersRule, filePath string) []string {
	var matched []string
	for _, rule := range rules {
		if matchPattern(rule.Pattern, filePath) {
			matched = rule.Owners
		}
	}
	return matched
}

// ResolveReviewers determines the best reviewer(s) for a set of changed file paths
// using CODEOWNERS rules. Returns deduplicated owner handles, prioritized by
// how many files they own (most relevant first).
func ResolveReviewers(rules []CodeownersRule, changedFiles []string) []string {
	if len(rules) == 0 || len(changedFiles) == 0 {
		return nil
	}

	counts := make(map[string]int)
	for _, f := range changedFiles {
		owners := MatchOwners(rules, f)
		for _, o := range owners {
			counts[o]++
		}
	}

	if len(counts) == 0 {
		return nil
	}

	// Sort by count descending, then alphabetically.
	type ownerCount struct {
		owner string
		count int
	}
	sorted := make([]ownerCount, 0, len(counts))
	for o, c := range counts {
		sorted = append(sorted, ownerCount{o, c})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].owner < sorted[j].owner
	})

	result := make([]string, len(sorted))
	for i, oc := range sorted {
		result[i] = oc.owner
	}
	return result
}

// FetchCodeowners fetches the CODEOWNERS file from a GitHub repo.
// Checks standard locations: .github/CODEOWNERS, CODEOWNERS, docs/CODEOWNERS.
func FetchCodeowners(ctx context.Context, token, owner, repo string, httpClient *http.Client, baseURL string) (string, error) {
	paths := []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
	for _, path := range paths {
		content, err := fetchFileContent(ctx, token, owner, repo, path, httpClient, baseURL)
		if err == nil && content != "" {
			return content, nil
		}
	}
	return "", fmt.Errorf("no CODEOWNERS file found")
}

func fetchFileContent(ctx context.Context, token, owner, repo, path string, httpClient *http.Client, baseURL string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3.raw")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// matchPattern matches a CODEOWNERS pattern against a file path.
// Supports: exact match, directory patterns ending in /, glob patterns,
// and ** (doublestar) for matching across directories.
func matchPattern(pattern, filePath string) bool {
	// Directory pattern: "dir/" matches all files under dir.
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		dir = strings.TrimPrefix(dir, "/")
		return strings.HasPrefix(filePath, dir+"/") || filePath == dir
	}

	// Pattern with leading / means anchored to repo root.
	anchored := strings.HasPrefix(pattern, "/")
	cleanPattern := strings.TrimPrefix(pattern, "/")

	// Handle ** (doublestar) patterns: match zero or more directories.
	if strings.Contains(cleanPattern, "**") {
		return matchDoublestar(cleanPattern, filePath)
	}

	if anchored {
		matched, err := filepath.Match(cleanPattern, filePath)
		if err != nil {
			return false // malformed pattern
		}
		return matched
	}

	// Unanchored pattern: match against basename or full path.
	if matched, err := filepath.Match(cleanPattern, filePath); err == nil && matched {
		return true
	}
	if matched, err := filepath.Match(cleanPattern, filepath.Base(filePath)); err == nil && matched {
		return true
	}

	// Try matching as a prefix (e.g. "internal/services/" without trailing slash).
	if strings.HasPrefix(filePath, cleanPattern+"/") {
		return true
	}

	return false
}

// matchDoublestar handles patterns containing ** which match zero or more path segments.
// For example, "docs/**/*.md" matches "docs/foo.md" and "docs/a/b/c.md".
// Supports multiple ** segments like "src/**/test/**/*.go".
func matchDoublestar(pattern, filePath string) bool {
	// Split pattern on the first "**" occurrence.
	parts := strings.SplitN(pattern, "**", 2)
	prefix := parts[0]
	suffix := strings.TrimPrefix(parts[1], "/")

	// Prefix must match the start of the path.
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(filePath, prefix+"/") && filePath != prefix {
			return false
		}
	}

	// If no suffix after **, it matches everything under prefix.
	if suffix == "" {
		return true
	}

	// Strip the matched prefix from the file path.
	remaining := filePath
	if prefix != "" {
		remaining = strings.TrimPrefix(filePath, prefix+"/")
	}

	// If the suffix itself contains **, recurse.
	if strings.Contains(suffix, "**") {
		// Try matching the suffix (which has its own **) against every possible subpath.
		for {
			if matchDoublestar(suffix, remaining) {
				return true
			}
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
		}
		return false
	}

	// Simple suffix (no more **): try matching against every possible subpath.
	for {
		if matched, err := filepath.Match(suffix, remaining); err == nil && matched {
			return true
		}
		idx := strings.Index(remaining, "/")
		if idx < 0 {
			break
		}
		remaining = remaining[idx+1:]
	}
	return false
}

// RequestReviewers calls the GitHub API to request reviewers on a PR.
func (s *PRService) RequestReviewers(ctx context.Context, token, owner, repo string, prNumber int, reviewers []string) error {
	// Separate individual reviewers from team reviewers (org/team-slug format).
	var individuals []string
	var teams []string
	for _, r := range reviewers {
		if strings.Contains(r, "/") {
			// "org/team-name" → just "team-name"
			parts := strings.SplitN(r, "/", 2)
			teams = append(teams, parts[1])
		} else {
			individuals = append(individuals, r)
		}
	}

	if len(individuals) == 0 && len(teams) == 0 {
		return nil
	}

	body := map[string]interface{}{}
	if len(individuals) > 0 {
		body["reviewers"] = individuals
	}
	if len(teams) > 0 {
		body["team_reviewers"] = teams
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal reviewers request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/requested_reviewers", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request reviewers API call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("request reviewers failed with status %d", resp.StatusCode)
	}
	return nil
}
