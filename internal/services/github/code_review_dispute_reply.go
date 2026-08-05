package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type CodeReviewDisputeReplyRequest struct {
	OrgID               uuid.UUID
	InstallationID      int64
	Repository          string
	PullRequestNumber   int
	ThreadRootCommentID *int64
	KnownReplyCommentID *int64
	Body                string
	// SearchExistingReply scans the pull request's comments for this dispute's
	// marker before creating one. It recovers a reply that was published but
	// never recorded, and costs one GitHub request per 100 comments, so callers
	// enable it only when a previous attempt could have left one behind.
	SearchExistingReply bool
}

func (s *PRService) PublishCodeReviewDisputeReply(ctx context.Context, req CodeReviewDisputeReplyRequest) (int64, error) {
	if req.OrgID == uuid.Nil || req.InstallationID == 0 || req.PullRequestNumber <= 0 {
		return 0, fmt.Errorf("org_id, installation_id, and pull request number are required")
	}
	owner, repo, found := strings.Cut(strings.TrimSpace(req.Repository), "/")
	if !found || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" || strings.Contains(repo, "/") {
		return 0, fmt.Errorf("repository must use owner/name format")
	}
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	body := strings.TrimSpace(req.Body)
	if body == "" || !strings.Contains(body, prFeedbackHiddenMarker) {
		return 0, fmt.Errorf("dispute reply must contain the loop-prevention marker")
	}
	token, err := s.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return 0, fmt.Errorf("get installation token for dispute reply: %w", err)
	}
	if req.KnownReplyCommentID != nil {
		if req.ThreadRootCommentID != nil {
			err = s.updatePullRequestReviewComment(ctx, token, owner, repo, *req.KnownReplyCommentID, body)
		} else {
			err = s.updateIssueComment(ctx, token, owner, repo, *req.KnownReplyCommentID, body)
		}
		if err == nil {
			return *req.KnownReplyCommentID, nil
		}
		var apiErr *GitHubAPIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			return 0, err
		}
	}
	var markerID *int64
	if req.SearchExistingReply {
		markerID, err = s.findCodeReviewDisputeReply(ctx, token, owner, repo, req.PullRequestNumber, body, req.ThreadRootCommentID != nil)
		if err != nil {
			return 0, err
		}
	}
	if markerID != nil {
		if req.ThreadRootCommentID != nil {
			err = s.updatePullRequestReviewComment(ctx, token, owner, repo, *markerID, body)
		} else {
			err = s.updateIssueComment(ctx, token, owner, repo, *markerID, body)
		}
		return *markerID, err
	}
	if req.ThreadRootCommentID != nil {
		return s.createPullRequestReviewReply(ctx, token, owner, repo, req.PullRequestNumber, *req.ThreadRootCommentID, body)
	}
	return s.createIssueComment(ctx, token, owner, repo, req.PullRequestNumber, body)
}

func (s *PRService) createPullRequestReviewReply(ctx context.Context, token, owner, repo string, pullNumber int, rootCommentID int64, body string) (int64, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, pullNumber, rootCommentID)
	response, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]string{"body": body})
	if err != nil {
		return 0, err
	}
	var comment githubIssueComment
	if err := json.Unmarshal(response, &comment); err != nil {
		return 0, fmt.Errorf("decode created pull request review reply: %w", err)
	}
	return comment.ID, nil
}

func (s *PRService) updatePullRequestReviewComment(ctx context.Context, token, owner, repo string, commentID int64, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d", owner, repo, commentID)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPatch, path, map[string]string{"body": body})
	return err
}

func (s *PRService) findCodeReviewDisputeReply(ctx context.Context, token, owner, repo string, pullNumber int, desiredBody string, inline bool) (*int64, error) {
	markerStart := strings.Index(desiredBody, prFeedbackHiddenMarker)
	if markerStart < 0 {
		return nil, nil
	}
	marker := strings.TrimSpace(desiredBody[markerStart:])
	for page := 1; page <= 50; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repo, pullNumber, page)
		if inline {
			path = fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100&page=%d", owner, repo, pullNumber, page)
		}
		response, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var comments []githubIssueComment
		if err := json.Unmarshal(response, &comments); err != nil {
			return nil, fmt.Errorf("decode code review dispute reply search: %w", err)
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) {
				id := comment.ID
				return &id, nil
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	return nil, nil
}
