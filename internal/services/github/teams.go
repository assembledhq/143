package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GitHubTeam represents a team from the GitHub API.
type GitHubTeam struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

// GitHubTeamMember represents a member from the GitHub Teams API.
type GitHubTeamMember struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// ListOrgTeams fetches all teams for the given GitHub organization.
// Uses pagination to handle orgs with many teams.
func (s *Service) ListOrgTeams(ctx context.Context, installationToken, org string) ([]GitHubTeam, error) {
	var allTeams []GitHubTeam
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/teams?per_page=100&page=%d", org, page)
		teams, hasMore, err := fetchPage[GitHubTeam](ctx, s.httpClient, url, installationToken)
		if err != nil {
			return nil, fmt.Errorf("list org teams (page %d): %w", page, err)
		}
		allTeams = append(allTeams, teams...)
		if !hasMore {
			break
		}
		page++
	}

	return allTeams, nil
}

// ListTeamMembers fetches all members of a team in the given GitHub organization.
func (s *Service) ListTeamMembers(ctx context.Context, installationToken, org, teamSlug string) ([]GitHubTeamMember, error) {
	var allMembers []GitHubTeamMember
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/teams/%s/members?per_page=100&page=%d", org, teamSlug, page)
		members, hasMore, err := fetchPage[GitHubTeamMember](ctx, s.httpClient, url, installationToken)
		if err != nil {
			return nil, fmt.Errorf("list team members (page %d): %w", page, err)
		}
		allMembers = append(allMembers, members...)
		if !hasMore {
			break
		}
		page++
	}

	return allMembers, nil
}

// fetchPage fetches a single page from the GitHub API, closing the response body
// immediately. Returns the decoded items and whether there are more pages (via
// the Link header or a full page of results).
func fetchPage[T any](ctx context.Context, client *http.Client, url, token string) ([]T, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, false, fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, body)
	}

	var items []T
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, false, fmt.Errorf("decode response: %w", err)
	}

	// Check the Link header for a "next" relation; only fall back to
	// page-size heuristic when no Link header is present.
	linkHeader := resp.Header.Get("Link")
	hasMore := strings.Contains(linkHeader, `rel="next"`)
	if linkHeader == "" && len(items) >= 100 {
		hasMore = true
	}

	return items, hasMore, nil
}
