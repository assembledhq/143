package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// previewCreateTimeout is generous because the API call triggers GitHub SHA
// resolution, config validation, worker selection, and DB reservation before
// returning. 30s was too tight when worker selection is slow.
const previewCreateTimeout = 120 * time.Second

// repoLookupTimeout is for a lightweight paginated read; keep it short.
const repoLookupTimeout = 30 * time.Second

var httpClient = &http.Client{Timeout: previewCreateTimeout}
var repoClient = &http.Client{Timeout: repoLookupTimeout}

type createPreviewRequest struct {
	RepositoryID      string `json:"repository_id"`
	Branch            string `json:"branch"`
	CommitSHA         string `json:"commit_sha,omitempty"`
	PreviewConfigName string `json:"preview_config_name,omitempty"`
	Source            struct {
		Type       string `json:"type"`
		ExternalID string `json:"external_id,omitempty"`
		URL        string `json:"url,omitempty"`
	} `json:"source"`
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

type repositoryListResponse struct {
	Data []struct {
		ID       string `json:"id"`
		FullName string `json:"full_name"`
	} `json:"data"`
	Meta struct {
		NextCursor string `json:"next_cursor"`
	} `json:"meta"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "create" {
		fmt.Fprintln(os.Stderr, "usage: 143 preview create --repo owner/name --branch BRANCH [--commit-sha SHA] [--config NAME] [--ttl-seconds N]")
		fmt.Fprintln(os.Stderr, "       use --repository-id UUID instead of --repo to skip the repository lookup")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	repoFullName := fs.String("repo", "", "repository full name in owner/name format, e.g. acme/app")
	repositoryID := fs.String("repository-id", "", "143 repository UUID (alternative to --repo)")
	branch := fs.String("branch", "", "repository branch to preview")
	commitSHA := fs.String("commit-sha", "", "optional commit SHA")
	configName := fs.String("config", "", "optional preview config name")
	ttlSeconds := fs.Int64("ttl-seconds", 0, "optional preview lifetime in seconds (must be positive)")
	apiURL := fs.String("api-url", os.Getenv("143_API_URL"), "143 API base URL")
	token := fs.String("token", os.Getenv("143_API_TOKEN"), "143 session or preview API token")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "--token (or 143_API_TOKEN env var) is required")
		os.Exit(2)
	}
	if *apiURL == "" {
		fmt.Fprintln(os.Stderr, "--api-url (or 143_API_URL env var) is required")
		os.Exit(2)
	}
	if *branch == "" {
		fmt.Fprintln(os.Stderr, "--branch is required")
		os.Exit(2)
	}
	if *repositoryID == "" && *repoFullName == "" {
		fmt.Fprintln(os.Stderr, "--repo (owner/name format) or --repository-id is required")
		os.Exit(2)
	}
	if *repoFullName != "" {
		parts := strings.SplitN(*repoFullName, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintf(os.Stderr, "--repo must be in owner/name format (got %q)\n", *repoFullName)
			os.Exit(2)
		}
	}
	if *ttlSeconds < 0 {
		fmt.Fprintln(os.Stderr, "--ttl-seconds must be a positive number")
		os.Exit(2)
	}
	resolvedRepoID := *repositoryID
	if resolvedRepoID == "" {
		var err error
		resolvedRepoID, err = resolveRepositoryID(*apiURL, *token, *repoFullName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	reqBody := createPreviewRequest{
		RepositoryID:      resolvedRepoID,
		Branch:            *branch,
		CommitSHA:         *commitSHA,
		PreviewConfigName: *configName,
		TTLSeconds:        *ttlSeconds,
	}
	reqBody.Source.Type = "api"
	body, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	endpoint := strings.TrimRight(*apiURL, "/") + "/api/v1/previews"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "preview create failed: %s\n%s\n", resp.Status, string(respBody))
		os.Exit(1)
	}
	fmt.Println(string(respBody))
}

// resolveRepositoryID pages through /api/v1/repositories until it finds the
// repository with the given full_name. Pagination prevents missing repos that
// appear on pages beyond the first.
func resolveRepositoryID(apiURL, token, fullName string) (string, error) {
	base := strings.TrimRight(apiURL, "/") + "/api/v1/repositories"
	cursor := ""
	for {
		endpoint := base
		if cursor != "" {
			endpoint = base + "?cursor=" + url.QueryEscape(cursor)
		}
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := repoClient.Do(req)
		if err != nil {
			return "", err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("repository lookup failed: %s\n%s", resp.Status, string(body))
		}
		var parsed repositoryListResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("decode repository list: %w", err)
		}
		for _, repo := range parsed.Data {
			if repo.FullName == fullName {
				return repo.ID, nil
			}
		}
		if parsed.Meta.NextCursor == "" {
			break
		}
		cursor = parsed.Meta.NextCursor
	}
	return "", fmt.Errorf("repository %q not found; verify the --repo flag uses owner/name format", fullName)
}
