package github

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
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
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].count > sorted[i].count || (sorted[j].count == sorted[i].count && sorted[j].owner < sorted[i].owner) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

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
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", baseURL, owner, repo, path)
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

	var buf strings.Builder
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// matchPattern matches a CODEOWNERS pattern against a file path.
// Supports: exact match, directory patterns ending in /, and glob patterns.
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

	if anchored {
		matched, _ := filepath.Match(cleanPattern, filePath)
		return matched
	}

	// Unanchored pattern: match against basename or full path.
	if matched, _ := filepath.Match(cleanPattern, filePath); matched {
		return true
	}
	if matched, _ := filepath.Match(cleanPattern, filepath.Base(filePath)); matched {
		return true
	}

	// Try matching as a prefix (e.g. "internal/services/" without trailing slash).
	if strings.HasPrefix(filePath, cleanPattern+"/") {
		return true
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

	jsonBody, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/requested_reviewers", s.baseURL, owner, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(jsonBody)))
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
