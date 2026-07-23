package codereview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	ghservice "github.com/assembledhq/143/internal/services/github"
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
	SubmitReviewDecisionApproved         SubmitReviewDecision = "approved"
	SubmitReviewDecisionCommentOnly      SubmitReviewDecision = "comment_only"
	SubmitReviewDecisionNeedsHumanReview SubmitReviewDecision = "needs_human_review"
	SubmitReviewDecisionBlocked          SubmitReviewDecision = "blocked"
)

type SubmitReviewRequest struct {
	InstallationID    int64
	Repository        string
	PullNumber        int
	HeadSHA           string
	OutputKey         string
	PreviousOutputKey string
	ExistingReviewID  int64
	ExistingReviewURL string
	Decision          SubmitReviewDecision
	PreviousDecision  SubmitReviewDecision
	PreviousDecidedAt time.Time
	PreviousBody      string
	Body              string
	Comments          []SubmitReviewComment
}

type SubmitReviewComment struct {
	Path      string
	Line      int
	Body      string
	DedupeKey string
}

type SubmitReviewResult struct {
	ID       int64
	URL      string
	Body     string
	Comments []SubmitReviewPostedComment
}

type SubmitReviewPostedComment struct {
	ID        int64
	Path      string
	Line      int
	Body      string
	DedupeKey string
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
	Patch     string `json:"patch"`
}

type ReviewContextRequest struct {
	InstallationID int64
	Repository     string
	PullNumber     int
	BotLogins      []string
}

type ReviewContext struct {
	UnresolvedHumanThreads int
	BlockingHumanReviews   int
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
	visibleReviewBody := strings.TrimSpace(req.Body)
	if req.ExistingReviewID > 0 {
		visibleReviewBody = withCodeReviewHistory(visibleReviewBody, req.PreviousBody, req.PreviousDecision, req.PreviousDecidedAt)
		reviewBody := withCodeReviewOutputMarker(visibleReviewBody, req.OutputKey)
		result, err := s.updateExistingReview(ctx, token, owner, repo, req, reviewBody)
		result.Body = visibleReviewBody
		return result, err
	}
	reviewBody := withCodeReviewOutputMarker(visibleReviewBody, req.OutputKey)
	if strings.TrimSpace(req.OutputKey) != "" {
		existing, found, err := s.findExistingReview(ctx, token, owner, repo, req.PullNumber, req.OutputKey)
		if err != nil {
			return SubmitReviewResult{}, err
		}
		if found {
			existing.Body = visibleReviewBody
			return existing, nil
		}
	}
	payload := map[string]any{
		"commit_id": req.HeadSHA,
		"body":      reviewBody,
		"event":     githubReviewEvent(req.Decision),
	}
	knownPostedComments := make([]SubmitReviewPostedComment, 0)
	dedupeKeysByMarker := make(map[string]string, len(req.Comments))
	if len(req.Comments) > 0 {
		existingByMarker := make(map[string]githubReviewCommentItem)
		if submitReviewHasCommentDedupeKeys(req.Comments) {
			existingComments, err := s.listPullRequestReviewComments(ctx, token, owner, repo, req.PullNumber)
			if err != nil {
				return SubmitReviewResult{}, err
			}
			existingByMarker = codeReviewCommentsByMarker(existingComments)
		}
		comments := make([]map[string]any, 0, len(req.Comments))
		for _, comment := range req.Comments {
			if strings.TrimSpace(comment.Path) == "" || comment.Line <= 0 || strings.TrimSpace(comment.Body) == "" {
				continue
			}
			markerDigest := ""
			if strings.TrimSpace(comment.DedupeKey) != "" {
				markerKey := codeReviewFindingMarkerKey(req.OutputKey, comment.DedupeKey)
				markerDigest = codeReviewMarkerDigest(markerKey)
				dedupeKeysByMarker[markerDigest] = comment.DedupeKey
			}
			body := withCodeReviewFindingMarker(comment.Body, codeReviewFindingMarkerKey(req.OutputKey, comment.DedupeKey))
			if markerDigest != "" {
				if existing, ok := existingByMarker[markerDigest]; ok {
					if strings.TrimSpace(existing.Body) != strings.TrimSpace(body) {
						if err := s.updateReviewComment(ctx, token, owner, repo, existing.ID, body); err != nil {
							return SubmitReviewResult{}, err
						}
						existing.Body = body
					}
					knownPostedComments = append(knownPostedComments, SubmitReviewPostedComment{
						ID:        existing.ID,
						Path:      existing.Path,
						Line:      existing.Line,
						Body:      body,
						DedupeKey: comment.DedupeKey,
					})
					continue
				}
			}
			comments = append(comments, map[string]any{
				"path": comment.Path,
				"line": comment.Line,
				"side": "RIGHT",
				"body": body,
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
		return SubmitReviewResult{}, fmt.Errorf("submit GitHub review: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	var decoded struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return SubmitReviewResult{}, fmt.Errorf("decode GitHub review response: %w", err)
	}
	result := SubmitReviewResult{ID: decoded.ID, URL: decoded.HTMLURL, Body: visibleReviewBody}
	result.Comments = append(result.Comments, knownPostedComments...)
	if decoded.ID != 0 && len(req.Comments) > 0 {
		if comments, commentsErr := s.listReviewComments(ctx, token, owner, repo, req.PullNumber, decoded.ID); commentsErr == nil {
			annotatePostedCommentsWithDedupeKeys(comments, dedupeKeysByMarker)
			result.Comments = append(result.Comments, comments...)
		} else {
			return SubmitReviewResult{}, commentsErr
		}
	}
	return result, nil
}

func (s *GitHubSubmitter) updateExistingReview(ctx context.Context, token, owner, repo string, req SubmitReviewRequest, reviewBody string) (SubmitReviewResult, error) {
	comments, err := s.listPullRequestReviewComments(ctx, token, owner, repo, req.PullNumber)
	if err != nil {
		return SubmitReviewResult{}, err
	}
	existingByMarker := codeReviewCommentsByMarker(comments)
	posted := make([]SubmitReviewPostedComment, 0, len(req.Comments))
	for _, comment := range req.Comments {
		if strings.TrimSpace(comment.Path) == "" || comment.Line <= 0 || strings.TrimSpace(comment.Body) == "" {
			continue
		}
		// Once a PR has a persistent review summary, inline findings use a
		// review-independent marker. This lets a later assessment revive and
		// update the same thread even if an intermediate assessment did not
		// select that finding.
		markerKey := codeReviewFindingMarkerKey("", comment.DedupeKey)
		body := withCodeReviewFindingMarker(comment.Body, markerKey)
		var existing githubReviewCommentItem
		found := false
		for _, outputKey := range []string{req.OutputKey, req.PreviousOutputKey, ""} {
			candidateKey := codeReviewFindingMarkerKey(outputKey, comment.DedupeKey)
			if strings.TrimSpace(candidateKey) == "" {
				continue
			}
			candidate, ok := existingByMarker[codeReviewMarkerDigest(candidateKey)]
			if ok {
				existing = candidate
				found = true
				break
			}
		}
		if found {
			if strings.TrimSpace(existing.Body) != strings.TrimSpace(body) {
				if err := s.updateReviewComment(ctx, token, owner, repo, existing.ID, body); err != nil {
					return SubmitReviewResult{}, err
				}
			}
			posted = append(posted, SubmitReviewPostedComment{
				ID: existing.ID, Path: existing.Path, Line: existing.Line, Body: body, DedupeKey: comment.DedupeKey,
			})
			continue
		}
		created, err := s.createReviewComment(ctx, token, owner, repo, req.PullNumber, req.HeadSHA, comment, body)
		if err != nil {
			return SubmitReviewResult{}, err
		}
		posted = append(posted, created)
	}

	result, err := s.updateReviewSummary(ctx, token, owner, repo, req.PullNumber, req.ExistingReviewID, reviewBody)
	if err != nil {
		return SubmitReviewResult{}, err
	}
	if req.Decision == SubmitReviewDecisionApproved {
		if err := s.ensureFormalApproval(ctx, token, owner, repo, req); err != nil {
			return SubmitReviewResult{}, err
		}
	}
	if strings.TrimSpace(result.URL) == "" {
		result.URL = strings.TrimSpace(req.ExistingReviewURL)
	}
	result.Comments = posted
	return result, nil
}

func (s *GitHubSubmitter) ensureFormalApproval(ctx context.Context, token, owner, repo string, req SubmitReviewRequest) error {
	approvalOutputKey := strings.TrimSpace(req.OutputKey) + ":formal-approval"
	if strings.TrimSpace(req.OutputKey) != "" {
		_, found, err := s.findExistingReview(ctx, token, owner, repo, req.PullNumber, approvalOutputKey)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
	}
	payload, err := json.Marshal(map[string]any{
		"commit_id": req.HeadSHA,
		"body":      withCodeReviewOutputMarker("", approvalOutputKey),
		"event":     githubReviewEvent(SubmitReviewDecisionApproved),
	})
	if err != nil {
		return fmt.Errorf("marshal formal approval payload: %w", err)
	}
	reviewURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), req.PullNumber)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reviewURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create formal approval request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("submit formal GitHub approval: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("submit formal GitHub approval: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	return nil
}

func (s *GitHubSubmitter) updateReviewSummary(ctx context.Context, token, owner, repo string, pullNumber int, reviewID int64, body string) (SubmitReviewResult, error) {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("marshal review summary update: %w", err)
	}
	reviewURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews/%d", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), pullNumber, reviewID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reviewURL, bytes.NewReader(payload))
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("create review summary update request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return SubmitReviewResult{}, fmt.Errorf("update GitHub review summary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SubmitReviewResult{}, fmt.Errorf("update GitHub review summary: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	var decoded struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return SubmitReviewResult{}, fmt.Errorf("decode GitHub review summary update: %w", err)
	}
	if decoded.ID == 0 {
		decoded.ID = reviewID
	}
	return SubmitReviewResult{ID: decoded.ID, URL: decoded.HTMLURL}, nil
}

func (s *GitHubSubmitter) createReviewComment(ctx context.Context, token, owner, repo string, pullNumber int, headSHA string, comment SubmitReviewComment, body string) (SubmitReviewPostedComment, error) {
	payload, err := json.Marshal(map[string]any{
		"body": body, "commit_id": headSHA, "path": comment.Path, "line": comment.Line, "side": "RIGHT",
	})
	if err != nil {
		return SubmitReviewPostedComment{}, fmt.Errorf("marshal review comment create: %w", err)
	}
	commentURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), pullNumber)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, commentURL, bytes.NewReader(payload))
	if err != nil {
		return SubmitReviewPostedComment{}, fmt.Errorf("create review comment request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return SubmitReviewPostedComment{}, fmt.Errorf("create GitHub review comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SubmitReviewPostedComment{}, fmt.Errorf("create GitHub review comment: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	var decoded githubReviewCommentItem
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return SubmitReviewPostedComment{}, fmt.Errorf("decode GitHub review comment response: %w", err)
	}
	return SubmitReviewPostedComment{
		ID: decoded.ID, Path: decoded.Path, Line: decoded.Line, Body: body, DedupeKey: comment.DedupeKey,
	}, nil
}

func annotatePostedCommentsWithDedupeKeys(comments []SubmitReviewPostedComment, dedupeKeysByMarker map[string]string) {
	if len(comments) == 0 || len(dedupeKeysByMarker) == 0 {
		return
	}
	for idx := range comments {
		marker := extractCodeReviewFindingMarker(comments[idx].Body)
		if marker == "" {
			continue
		}
		if key := dedupeKeysByMarker[marker]; key != "" {
			comments[idx].DedupeKey = key
		}
	}
}

func submitReviewHasCommentDedupeKeys(comments []SubmitReviewComment) bool {
	for _, comment := range comments {
		if strings.TrimSpace(comment.DedupeKey) != "" {
			return true
		}
	}
	return false
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
		return nil, fmt.Errorf("list GitHub review comments: %w", readGitHubAPIResponseError(httpReq, resp))
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

type githubReviewListItem struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

type githubReviewCommentItem struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

func (s *GitHubSubmitter) findExistingReview(ctx context.Context, token, owner, repo string, pullNumber int, outputKey string) (SubmitReviewResult, bool, error) {
	marker := codeReviewOutputMarker(outputKey)
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", url.PathEscape(owner), url.PathEscape(repo), pullNumber)
	for path != "" {
		var reviews []githubReviewListItem
		nextPath, err := s.getGitHubJSONPage(ctx, token, path, &reviews)
		if err != nil {
			return SubmitReviewResult{}, false, fmt.Errorf("list GitHub pull request reviews: %w", err)
		}
		for _, review := range reviews {
			if review.ID == 0 || !strings.Contains(review.Body, marker) {
				continue
			}
			result := SubmitReviewResult{ID: review.ID, URL: review.HTMLURL}
			comments, err := s.listReviewComments(ctx, token, owner, repo, pullNumber, review.ID)
			if err != nil {
				return SubmitReviewResult{}, false, err
			}
			result.Comments = comments
			return result, true, nil
		}
		path = nextPath
	}
	return SubmitReviewResult{}, false, nil
}

func (s *GitHubSubmitter) listPullRequestReviewComments(ctx context.Context, token, owner, repo string, pullNumber int) ([]githubReviewCommentItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", url.PathEscape(owner), url.PathEscape(repo), pullNumber)
	comments := make([]githubReviewCommentItem, 0)
	for path != "" {
		var page []githubReviewCommentItem
		nextPath, err := s.getGitHubJSONPage(ctx, token, path, &page)
		if err != nil {
			return nil, fmt.Errorf("list GitHub pull request review comments: %w", err)
		}
		comments = append(comments, page...)
		path = nextPath
	}
	return comments, nil
}

func (s *GitHubSubmitter) updateReviewComment(ctx context.Context, token, owner, repo string, commentID int64, body string) error {
	payload, err := json.Marshal(map[string]any{"body": body})
	if err != nil {
		return fmt.Errorf("marshal review comment update: %w", err)
	}
	commentURL := fmt.Sprintf("%s/repos/%s/%s/pulls/comments/%d", s.baseURL, url.PathEscape(owner), url.PathEscape(repo), commentID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, commentURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create review comment update request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("update GitHub review comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("update GitHub review comment: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	return nil
}

func (s *GitHubSubmitter) getGitHubJSONPage(ctx context.Context, token, path string, target any) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub request failed: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return "", fmt.Errorf("decode GitHub response: %w", err)
	}
	return parseNextGitHubPath(resp.Header.Get("Link")), nil
}

func (s *GitHubSubmitter) doGitHubGraphQL(ctx context.Context, token, query string, variables map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, fmt.Errorf("marshal GitHub GraphQL request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create GitHub GraphQL request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("GitHub GraphQL request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read GitHub GraphQL response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub GraphQL request failed: %w", newGitHubAPIResponseError(httpReq, resp, body))
	}
	return body, nil
}

type reviewContextGraphQLPage struct {
	Context             ReviewContext
	ReviewThreadsCursor string
	ReviewThreadsMore   bool
	ReviewsCursor       string
	ReviewsMore         bool
}

func parseReviewContextGraphQLPage(body []byte, botLogins []string) (reviewContextGraphQLPage, error) {
	var decoded struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									Author *struct {
										Login string `json:"login"`
									} `json:"author"`
									PullRequestReview *struct {
										State  string `json:"state"`
										Author *struct {
											Login string `json:"login"`
										} `json:"author"`
									} `json:"pullRequestReview"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
					Reviews struct {
						Nodes []struct {
							State  string `json:"state"`
							Author *struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return reviewContextGraphQLPage{}, fmt.Errorf("decode GitHub review context: %w", err)
	}
	if len(decoded.Errors) > 0 {
		messages := make([]string, 0, len(decoded.Errors))
		for _, item := range decoded.Errors {
			messages = append(messages, strings.TrimSpace(item.Message))
		}
		return reviewContextGraphQLPage{}, fmt.Errorf("GitHub review context GraphQL errors: %s", strings.Join(compactStrings(messages), "; "))
	}
	bots := normalizedLoginSet(botLogins)
	var ctx ReviewContext
	for _, review := range decoded.Data.Repository.PullRequest.Reviews.Nodes {
		login := ""
		if review.Author != nil {
			login = review.Author.Login
		}
		if strings.EqualFold(review.State, "CHANGES_REQUESTED") && !loginInSet(login, bots) {
			ctx.BlockingHumanReviews++
		}
	}
	for _, thread := range decoded.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if thread.IsResolved {
			continue
		}
		humanOwned := len(thread.Comments.Nodes) == 0
		for _, comment := range thread.Comments.Nodes {
			login := ""
			if comment.Author != nil {
				login = comment.Author.Login
			}
			if login == "" && comment.PullRequestReview != nil && comment.PullRequestReview.Author != nil {
				login = comment.PullRequestReview.Author.Login
			}
			if !loginInSet(login, bots) {
				humanOwned = true
				break
			}
		}
		if humanOwned {
			ctx.UnresolvedHumanThreads++
		}
	}
	return reviewContextGraphQLPage{
		Context:             ctx,
		ReviewThreadsCursor: decoded.Data.Repository.PullRequest.ReviewThreads.PageInfo.EndCursor,
		ReviewThreadsMore:   decoded.Data.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage,
		ReviewsCursor:       decoded.Data.Repository.PullRequest.Reviews.PageInfo.EndCursor,
		ReviewsMore:         decoded.Data.Repository.PullRequest.Reviews.PageInfo.HasNextPage,
	}, nil
}

func normalizedLoginSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	return out
}

func loginInSet(login string, set map[string]struct{}) bool {
	_, ok := set[strings.ToLower(strings.TrimSpace(login))]
	return ok
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
	payload := map[string]any{
		"reviewers": reviewers,
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
		return fmt.Errorf("remove GitHub requested reviewers: %w", readGitHubAPIResponseError(httpReq, resp))
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

func (s *GitHubSubmitter) ListReviewContext(ctx context.Context, req ReviewContextRequest) (ReviewContext, error) {
	if s == nil || s.tokens == nil {
		return ReviewContext{}, fmt.Errorf("github submitter is not configured")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return ReviewContext{}, fmt.Errorf("repository must be owner/name")
	}
	if req.PullNumber <= 0 {
		return ReviewContext{}, fmt.Errorf("pull number is required")
	}
	if req.InstallationID <= 0 {
		return ReviewContext{}, fmt.Errorf("installation id is required")
	}
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return ReviewContext{}, fmt.Errorf("get installation token: %w", err)
	}
	query := `
query($owner: String!, $repo: String!, $number: Int!, $threadCursor: String, $reviewCursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $threadCursor) {
        nodes {
          isResolved
          comments(first: 20) {
            nodes {
              author { login }
              pullRequestReview {
                state
                author { login }
              }
            }
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
      reviews(first: 100, after: $reviewCursor) {
        nodes {
          state
          author { login }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
		  }
		}
	}`
	var out ReviewContext
	var threadCursor *string
	var reviewCursor *string
	for {
		body, err := s.doGitHubGraphQL(ctx, token, query, map[string]any{
			"owner":        owner,
			"repo":         repo,
			"number":       req.PullNumber,
			"threadCursor": threadCursor,
			"reviewCursor": reviewCursor,
		})
		if err != nil {
			return ReviewContext{}, err
		}
		page, err := parseReviewContextGraphQLPage(body, req.BotLogins)
		if err != nil {
			return ReviewContext{}, err
		}
		out.UnresolvedHumanThreads += page.Context.UnresolvedHumanThreads
		out.BlockingHumanReviews += page.Context.BlockingHumanReviews
		if !page.ReviewThreadsMore && !page.ReviewsMore {
			return out, nil
		}
		if page.ReviewThreadsMore && strings.TrimSpace(page.ReviewThreadsCursor) == "" {
			return ReviewContext{}, fmt.Errorf("GitHub review context pagination missing reviewThreads cursor")
		}
		if page.ReviewsMore && strings.TrimSpace(page.ReviewsCursor) == "" {
			return ReviewContext{}, fmt.Errorf("GitHub review context pagination missing reviews cursor")
		}
		if cursor := strings.TrimSpace(page.ReviewThreadsCursor); cursor != "" {
			threadCursor = &cursor
		}
		if cursor := strings.TrimSpace(page.ReviewsCursor); cursor != "" {
			reviewCursor = &cursor
		}
	}
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
		return nil, "", fmt.Errorf("list GitHub pull request files: %w", readGitHubAPIResponseError(httpReq, resp))
	}
	var files []PullRequestFile
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, "", fmt.Errorf("decode GitHub pull request files: %w", err)
	}
	return files, parseNextGitHubPath(resp.Header.Get("Link")), nil
}

func readGitHubAPIResponseError(req *http.Request, resp *http.Response) error {
	errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	apiErr := newGitHubAPIResponseError(req, resp, errorBody)
	if readErr != nil {
		return errors.Join(apiErr, fmt.Errorf("read GitHub error response: %w", readErr))
	}
	return apiErr
}

func newGitHubAPIResponseError(req *http.Request, resp *http.Response, body []byte) *ghservice.GitHubAPIError {
	method := ""
	path := ""
	if req != nil {
		method = req.Method
		if req.URL != nil {
			path = req.URL.RequestURI()
		}
	}
	return &ghservice.GitHubAPIError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       body,
		Header:     resp.Header.Clone(),
	}
}

func withCodeReviewOutputMarker(body, outputKey string) string {
	outputKey = strings.TrimSpace(outputKey)
	if outputKey == "" {
		return strings.TrimSpace(body)
	}
	marker := codeReviewOutputMarker(outputKey)
	body = strings.TrimSpace(body)
	if strings.Contains(body, marker) {
		return body
	}
	if body == "" {
		return marker
	}
	return body + "\n\n" + marker
}

func withCodeReviewFindingMarker(body, markerKey string) string {
	markerKey = strings.TrimSpace(markerKey)
	body = strings.TrimSpace(body)
	if markerKey == "" {
		return body
	}
	marker := codeReviewFindingMarker(markerKey)
	if strings.Contains(body, marker) {
		return body
	}
	if body == "" {
		return marker
	}
	return body + "\n\n" + marker
}

const (
	codeReviewHistoryStartMarker = "<!-- 143-code-review-history:start -->"
	codeReviewHistoryEndMarker   = "<!-- 143-code-review-history:end -->"
)

func withCodeReviewHistory(body, previousBody string, decision SubmitReviewDecision, decidedAt time.Time) string {
	body = stripCodeReviewHistory(body)
	entries := codeReviewHistoryEntries(previousBody)
	if entry := codeReviewHistoryEntry(decision, decidedAt); entry != "" && !containsString(entries, entry) {
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return body
	}
	history := codeReviewHistoryStartMarker + "\nPrevious 143 code reviews:\n" + strings.Join(entries, "\n") + "\n" + codeReviewHistoryEndMarker
	if body == "" {
		return history
	}
	return body + "\n\n" + history
}

func codeReviewHistoryEntries(body string) []string {
	start := strings.Index(body, codeReviewHistoryStartMarker)
	if start < 0 {
		return nil
	}
	rest := body[start+len(codeReviewHistoryStartMarker):]
	end := strings.Index(rest, codeReviewHistoryEndMarker)
	if end < 0 {
		return nil
	}
	lines := strings.Split(rest[:end], "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			entries = append(entries, line)
		}
	}
	return entries
}

func stripCodeReviewHistory(body string) string {
	start := strings.Index(body, codeReviewHistoryStartMarker)
	if start < 0 {
		return strings.TrimSpace(body)
	}
	rest := body[start+len(codeReviewHistoryStartMarker):]
	end := strings.Index(rest, codeReviewHistoryEndMarker)
	if end < 0 {
		return strings.TrimSpace(body)
	}
	before := strings.TrimSpace(body[:start])
	after := strings.TrimSpace(rest[end+len(codeReviewHistoryEndMarker):])
	if before == "" {
		return after
	}
	if after == "" {
		return before
	}
	return before + "\n\n" + after
}

func codeReviewHistoryEntry(decision SubmitReviewDecision, decidedAt time.Time) string {
	if decidedAt.IsZero() || decision.validate() != nil {
		return ""
	}
	return fmt.Sprintf("- `%s` — **%s**", decidedAt.UTC().Format(time.RFC3339), codeReviewDecisionLabel(decision))
}

func codeReviewDecisionLabel(decision SubmitReviewDecision) string {
	switch decision {
	case SubmitReviewDecisionApproved:
		return "Approved"
	case SubmitReviewDecisionCommentOnly:
		return "Not approved"
	case SubmitReviewDecisionNeedsHumanReview:
		return "Not approved — needs human review"
	case SubmitReviewDecisionBlocked:
		return "Not approved — blocked"
	default:
		return string(decision)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func codeReviewOutputMarker(outputKey string) string {
	return "<!-- 143-code-review-output:" + codeReviewMarkerDigest(outputKey) + " -->"
}

func codeReviewFindingMarkerKey(outputKey, dedupeKey string) string {
	dedupeKey = strings.TrimSpace(dedupeKey)
	if dedupeKey == "" {
		return ""
	}
	outputKey = strings.TrimSpace(outputKey)
	if outputKey == "" {
		return dedupeKey
	}
	return outputKey + "\n" + dedupeKey
}

func codeReviewFindingMarker(markerKey string) string {
	return "<!-- 143-code-review-finding:" + codeReviewMarkerDigest(markerKey) + " -->"
}

func codeReviewMarkerDigest(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return fmt.Sprintf("%x", sum[:])
}

func codeReviewCommentsByMarker(comments []githubReviewCommentItem) map[string]githubReviewCommentItem {
	out := make(map[string]githubReviewCommentItem)
	for _, comment := range comments {
		marker := extractCodeReviewFindingMarker(comment.Body)
		if marker == "" {
			continue
		}
		if _, exists := out[marker]; !exists {
			out[marker] = comment
		}
	}
	return out
}

func extractCodeReviewFindingMarker(body string) string {
	const prefix = "<!-- 143-code-review-finding:"
	start := strings.Index(body, prefix)
	if start < 0 {
		return ""
	}
	rest := body[start+len(prefix):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// IsCodeReviewAuthoredBody identifies hidden markers written by this
// submitter. Webhook handlers use it to avoid reassessment loops from 143's own
// review and inline-comment writes.
func IsCodeReviewAuthoredBody(body string) bool {
	return strings.Contains(body, "<!-- 143-code-review-output:") ||
		strings.Contains(body, "<!-- 143-code-review-finding:")
}

func githubReviewEvent(decision SubmitReviewDecision) string {
	if decision == SubmitReviewDecisionApproved {
		return "APPROVE"
	}
	return "COMMENT"
}

func (d SubmitReviewDecision) validate() error {
	switch d {
	case SubmitReviewDecisionApproved, SubmitReviewDecisionCommentOnly,
		SubmitReviewDecisionNeedsHumanReview, SubmitReviewDecisionBlocked:
		return nil
	default:
		return fmt.Errorf("invalid submit review decision: %q", d)
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
