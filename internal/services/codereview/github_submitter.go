package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type InstallationTokenProvider interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

type GitHubSubmitter struct {
	tokens  InstallationTokenProvider
	client  *http.Client
	baseURL string
}

type GitHubSubmitterOption func(*GitHubSubmitter)

func WithGitHubSubmitterBaseURL(baseURL string) GitHubSubmitterOption {
	return func(s *GitHubSubmitter) {
		s.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithGitHubSubmitterHTTPClient(client *http.Client) GitHubSubmitterOption {
	return func(s *GitHubSubmitter) {
		if client != nil {
			s.client = client
		}
	}
}

func NewGitHubSubmitter(tokens InstallationTokenProvider, opts ...GitHubSubmitterOption) *GitHubSubmitter {
	s := &GitHubSubmitter{
		tokens:  tokens,
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: "https://api.github.com",
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type SubmitReviewDecision string

const (
	SubmitReviewDecisionApproved    SubmitReviewDecision = "approved"
	SubmitReviewDecisionCommentOnly SubmitReviewDecision = "comment_only"
)

type SubmitReviewRequest struct {
	InstallationID int64
	Repository     string
	PullNumber     int
	HeadSHA        string
	Decision       SubmitReviewDecision
	Body           string
	Comments       []SubmitReviewComment
}

type SubmitReviewComment struct {
	Path string
	Line int
	Body string
}

type SubmitReviewResult struct {
	ID       int64
	URL      string
	Comments []SubmitReviewPostedComment
}

type SubmitReviewPostedComment struct {
	ID   int64
	Path string
	Line int
	Body string
}

type PullRequestFilesRequest struct {
	InstallationID int64
	Repository     string
	PullNumber     int
}

type PullRequestFile struct {
	Filename  string `json:"filename"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

type CommitStatusState string

const (
	CommitStatusStateError   CommitStatusState = "error"
	CommitStatusStateFailure CommitStatusState = "failure"
	CommitStatusStatePending CommitStatusState = "pending"
	CommitStatusStateSuccess CommitStatusState = "success"
)

type CommitStatusRequest struct {
	InstallationID int64
	Repository     string
	SHA            string
	State          CommitStatusState
	Context        string
	Description    string
	TargetURL      string
}

type RequestedReviewersRequest struct {
	InstallationID int64
	Repository     string
	PullNumber     int
	Reviewers      []string
	TeamReviewers  []string
}

func (s *GitHubSubmitter) SubmitReview(ctx context.Context, req SubmitReviewRequest) (SubmitReviewResult, error) {
	if s == nil || s.tokens == nil {
		return SubmitReviewResult{}, fmt.Errorf("github submitter is not configured")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return SubmitReviewResult{}, fmt.Errorf("repository must be owner/name")
	}
	if req.PullNumber <= 0 {
		return SubmitReviewResult{}, fmt.Errorf("pull number is required")
	}
	if req.InstallationID <= 0 {
		return SubmitReviewResult{}, fmt.Errorf("installation id is required")
	}
	if strings.TrimSpace(req.HeadSHA) == "" {
		return SubmitReviewResult{}, fmt.Errorf("head SHA is required")
	}
	if err := req.Decision.validate(); err != nil {
		return SubmitReviewResult{}, err
	}
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("get installation token: %w", err)
	}
	payload := map[string]any{
		"commit_id": req.HeadSHA,
		"body":      req.Body,
		"event":     githubReviewEvent(req.Decision),
	}
	if len(req.Comments) > 0 {
		comments := make([]map[string]any, 0, len(req.Comments))
		for _, comment := range req.Comments {
			if strings.TrimSpace(comment.Path) == "" || comment.Line <= 0 || strings.TrimSpace(comment.Body) == "" {
				continue
			}
			comments = append(comments, map[string]any{
				"path": comment.Path,
				"line": comment.Line,
				"side": "RIGHT",
				"body": comment.Body,
			})
		}
		if len(comments) > 0 {
			payload["comments"] = comments
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("marshal review payload: %w", err)
	}
	reviewURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), req.PullNumber)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reviewURL, bytes.NewReader(body))
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("create review request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("submit GitHub review: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SubmitReviewResult{}, fmt.Errorf("submit GitHub review returned %d: %s", resp.StatusCode, readGitHubErrorBody(resp))
	}
	var decoded struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return SubmitReviewResult{}, fmt.Errorf("decode GitHub review response: %w", err)
	}
	result := SubmitReviewResult{ID: decoded.ID, URL: decoded.HTMLURL}
	if decoded.ID != 0 && len(req.Comments) > 0 {
		if comments, commentsErr := s.listReviewComments(ctx, token, owner, repo, req.PullNumber, decoded.ID); commentsErr == nil {
			result.Comments = comments
		}
	}
	return result, nil
}

func (s *GitHubSubmitter) listReviewComments(ctx context.Context, token, owner, repo string, pullNumber int, reviewID int64) ([]SubmitReviewPostedComment, error) {
	commentsURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews/%d/comments", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), pullNumber, reviewID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, commentsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create review comments request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("list GitHub review comments: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list GitHub review comments returned %d: %s", resp.StatusCode, readGitHubErrorBody(resp))
	}
	var decoded []struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
		Line int    `json:"line"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode GitHub review comments response: %w", err)
	}
	comments := make([]SubmitReviewPostedComment, 0, len(decoded))
	for _, comment := range decoded {
		comments = append(comments, SubmitReviewPostedComment{
			ID:   comment.ID,
			Path: comment.Path,
			Line: comment.Line,
			Body: comment.Body,
		})
	}
	return comments, nil
}

func (s *GitHubSubmitter) PublishCommitStatus(ctx context.Context, req CommitStatusRequest) error {
	if s == nil || s.tokens == nil {
		return fmt.Errorf("github submitter is not configured")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return fmt.Errorf("repository must be owner/name")
	}
	if req.InstallationID <= 0 {
		return fmt.Errorf("installation id is required")
	}
	if strings.TrimSpace(req.SHA) == "" {
		return fmt.Errorf("sha is required")
	}
	if err := req.State.validate(); err != nil {
		return err
	}
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return fmt.Errorf("get installation token: %w", err)
	}
	payload := map[string]any{
		"state":       req.State,
		"context":     firstNonEmpty(req.Context, "143 Code Reviewer"),
		"description": req.Description,
	}
	if strings.TrimSpace(req.TargetURL) != "" {
		payload["target_url"] = req.TargetURL
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal commit status payload: %w", err)
	}
	statusURL := fmt.Sprintf("%s/repos/%s/%s/statuses/%s", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(req.SHA))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, statusURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create commit status request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("publish GitHub commit status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("publish GitHub commit status returned %d: %s", resp.StatusCode, readGitHubErrorBody(resp))
	}
	return nil
}

func (s *GitHubSubmitter) RemoveRequestedReviewers(ctx context.Context, req RequestedReviewersRequest) error {
	if s == nil || s.tokens == nil {
		return fmt.Errorf("github submitter is not configured")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return fmt.Errorf("repository must be owner/name")
	}
	if req.InstallationID <= 0 {
		return fmt.Errorf("installation id is required")
	}
	if req.PullNumber <= 0 {
		return fmt.Errorf("pull number is required")
	}
	reviewers := compactStrings(req.Reviewers)
	teams := compactStrings(req.TeamReviewers)
	if len(reviewers) == 0 && len(teams) == 0 {
		return nil
	}
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return fmt.Errorf("get installation token: %w", err)
	}
	payload := map[string]any{}
	if len(reviewers) > 0 {
		payload["reviewers"] = reviewers
	}
	if len(teams) > 0 {
		payload["team_reviewers"] = teams
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal requested reviewers payload: %w", err)
	}
	requestedURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/requested_reviewers", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), req.PullNumber)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, requestedURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create requested reviewers request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("remove GitHub requested reviewers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remove GitHub requested reviewers returned %d: %s", resp.StatusCode, readGitHubErrorBody(resp))
	}
	return nil
}

func (s *GitHubSubmitter) ListPullRequestFiles(ctx context.Context, req PullRequestFilesRequest) ([]PullRequestFile, error) {
	if s == nil || s.tokens == nil {
		return nil, fmt.Errorf("github submitter is not configured")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("repository must be owner/name")
	}
	if req.PullNumber <= 0 {
		return nil, fmt.Errorf("pull number is required")
	}
	if req.InstallationID <= 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", url.PathEscape(owner), url.PathEscape(repo), req.PullNumber)
	files := make([]PullRequestFile, 0)
	for path != "" {
		page, nextPath, err := s.getPullRequestFilesPage(ctx, token, path)
		if err != nil {
			return nil, err
		}
		files = append(files, page...)
		path = nextPath
	}
	return files, nil
}

func (s *GitHubSubmitter) getPullRequestFilesPage(ctx context.Context, token, path string) ([]PullRequestFile, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create pull request files request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("list GitHub pull request files: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("list GitHub pull request files returned %d: %s", resp.StatusCode, readGitHubErrorBody(resp))
	}
	var files []PullRequestFile
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, "", fmt.Errorf("decode GitHub pull request files: %w", err)
	}
	return files, parseNextGitHubPath(resp.Header.Get("Link")), nil
}

func readGitHubErrorBody(resp *http.Response) string {
	errorBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Sprintf("failed to read error body: %v", err)
	}
	return strings.TrimSpace(string(errorBody))
}

func githubReviewEvent(decision SubmitReviewDecision) string {
	if decision == SubmitReviewDecisionApproved {
		return "APPROVE"
	}
	return "COMMENT"
}

func (d SubmitReviewDecision) validate() error {
	switch d {
	case SubmitReviewDecisionApproved, SubmitReviewDecisionCommentOnly:
		return nil
	default:
		return fmt.Errorf("invalid submit review decision: %q", d)
	}
}

func (s CommitStatusState) validate() error {
	switch s {
	case CommitStatusStateError, CommitStatusStateFailure, CommitStatusStatePending, CommitStatusStateSuccess:
		return nil
	default:
		return fmt.Errorf("invalid commit status state: %q", s)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func compactStrings(values []string) []string {
	compacted := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		compacted = append(compacted, value)
	}
	return compacted
}

func parseNextGitHubPath(link string) string {
	for _, part := range strings.Split(link, ",") {
		sections := strings.Split(part, ";")
		if len(sections) < 2 || !strings.Contains(sections[1], `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(sections[0]), "<>")
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Path != "" {
			if parsed.RawQuery != "" {
				return parsed.Path + "?" + parsed.RawQuery
			}
			return parsed.Path
		}
		if strings.HasPrefix(raw, "/") {
			return raw
		}
	}
	return ""
}
