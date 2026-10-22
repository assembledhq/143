package automations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var ErrCheckRunTrigger = errors.New("opted-in check run trigger failed")

func (s *GitHubEventTriggerService) SetCheckRunPullRequestResolver(resolver PullRequestHeadResolver) {
	s.checkRunPullRequests = resolver
}

func (s *GitHubEventTriggerService) withCurrentCheckRunPullRequest(ctx context.Context, req GitHubEventTriggerRequest) (GitHubEventTriggerRequest, error) {
	if s.checkRunPullRequests == nil {
		return req, fmt.Errorf("%w: pull request resolver is not configured", ErrCheckRunTrigger)
	}
	if req.OrgID == uuid.Nil || req.RepositoryID == uuid.Nil || req.PullRequestNumber <= 0 || req.HeadSHA == "" || req.CheckRunID <= 0 {
		return req, nil
	}
	head, err := s.checkRunPullRequests.ResolvePullRequestHead(ctx, req.OrgID, req.RepositoryID, req.PullRequestNumber)
	if err != nil {
		return req, fmt.Errorf("%w: resolve current pull request: %w", ErrCheckRunTrigger, err)
	}
	if head.Number != req.PullRequestNumber || head.SHA != req.HeadSHA || head.State != "open" || head.Draft {
		return req, nil
	}
	req.Labels = normalizeGitHubLabels(head.Labels)
	req.LabelsKnown = true
	req.BaseBranch = head.BaseBranch
	req.PullRequestUpdatedAt = head.UpdatedAt
	req.firstCheckRunForHead = true
	return req, nil
}

func githubCheckRunHeadKey(req GitHubEventTriggerRequest) string {
	return fmt.Sprintf("check-run-head:%s:%d:%s", req.RepositoryID, req.PullRequestNumber, req.HeadSHA)
}
