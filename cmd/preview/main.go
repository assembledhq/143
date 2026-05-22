package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

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
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "create" {
		fmt.Fprintln(os.Stderr, "usage: 143 preview create --repo owner/name --branch BRANCH [--commit-sha SHA] [--config NAME]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	repoFullName := fs.String("repo", "", "repository full name, for example owner/name")
	repositoryID := fs.String("repository-id", "", "143 repository UUID")
	branch := fs.String("branch", "", "repository branch")
	commitSHA := fs.String("commit-sha", "", "optional commit SHA")
	configName := fs.String("config", "", "optional preview config name")
	ttlSeconds := fs.Int64("ttl-seconds", 0, "optional preview lifetime")
	apiURL := fs.String("api-url", os.Getenv("143_API_URL"), "143 API base URL")
	token := fs.String("token", os.Getenv("143_API_TOKEN"), "143 session or preview API token")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if (*repositoryID == "" && *repoFullName == "") || *branch == "" || *apiURL == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "--repo or --repository-id, --branch, --api-url, and --token are required")
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
	resp, err := http.DefaultClient.Do(req)
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

func resolveRepositoryID(apiURL, token, fullName string) (string, error) {
	endpoint := strings.TrimRight(apiURL, "/") + "/api/v1/repositories"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
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
	return "", fmt.Errorf("repository %q not found", fullName)
}
