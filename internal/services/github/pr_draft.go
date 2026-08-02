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

// ErrDraftHeadMoved reports that GitHub's draft is no longer at the head that
// was reviewed. Callers must resolve this as a review decision — retrying can
// never make an already-moved head match again.
var ErrDraftHeadMoved = errors.New("draft pull request head moved after review")

// MarkPullRequestReady transitions a 143-owned draft to ready-for-review.
// The REST read makes replays idempotent; GraphQL is only called while the PR
// is still a draft.
func (s *PRService) MarkPullRequestReady(ctx context.Context, orgID, pullRequestID uuid.UUID, expectedHeadSHA string) error {
	if s.pullRequests == nil || s.repos == nil || s.tokenProvider == nil {
		return errors.New("draft readiness dependencies are unavailable")
	}
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return fmt.Errorf("load draft pull request: %w", err)
	}
	repo, err := s.repos.GetByFullNameAnyStatus(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return fmt.Errorf("load draft repository: %w", err)
	}
	token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return fmt.Errorf("get draft repository token: %w", err)
	}
	owner, repoName := splitRepo(repo.FullName)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repoName, pr.GitHubPRNumber), nil)
	if err != nil {
		return fmt.Errorf("fetch draft pull request: %w", err)
	}
	var current struct {
		NodeID string `json:"node_id"`
		Draft  bool   `json:"draft"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &current); err != nil {
		return fmt.Errorf("decode draft pull request: %w", err)
	}
	if expected := strings.TrimSpace(expectedHeadSHA); expected != "" && current.Head.SHA != expected {
		return fmt.Errorf("%w: expected %s, got %s", ErrDraftHeadMoved, expected, current.Head.SHA)
	}
	if !current.Draft {
		return nil
	}
	if strings.TrimSpace(current.NodeID) == "" {
		return errors.New("draft pull request has no GraphQL node ID")
	}
	response, err := s.doGitHubGraphQL(ctx, token, `mutation MarkReady($pullRequestId: ID!) {
		markPullRequestReadyForReview(input: {pullRequestId: $pullRequestId}) {
			pullRequest { id isDraft }
		}
	}`, map[string]any{"pullRequestId": current.NodeID})
	if err != nil {
		return fmt.Errorf("mark draft ready for review: %w", err)
	}
	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Mark struct {
				PullRequest struct {
					IsDraft bool `json:"isDraft"`
				} `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return fmt.Errorf("decode mark-ready response: %w", err)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("mark draft ready for review: %s", result.Errors[0].Message)
	}
	if result.Data.Mark.PullRequest.IsDraft {
		return errors.New("GitHub left pull request in draft state")
	}
	return nil
}
