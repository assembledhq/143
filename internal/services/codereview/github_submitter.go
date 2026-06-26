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
	ID  int64
	URL string
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
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return SubmitReviewResult{}, fmt.Errorf("submit GitHub review returned %d: %s", resp.StatusCode, strings.TrimSpace(string(errorBody)))
	}
	var decoded struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return SubmitReviewResult{}, fmt.Errorf("decode GitHub review response: %w", err)
	}
	return SubmitReviewResult{ID: decoded.ID, URL: decoded.HTMLURL}, nil
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
