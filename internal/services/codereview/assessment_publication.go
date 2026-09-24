package codereview

import (
	"context"
	"errors"
	"strings"
)

// ReconcileAssessmentPublication is read-only. It is used when an uncertain
// write can no longer be retried because the PR inputs changed. Both a summary
// update and the separate formal-approval marker must be found before approval
// is recorded as confirmed.
func (s *GitHubSubmitter) ReconcileAssessmentPublication(ctx context.Context, req SubmitReviewRequest) (SubmitReviewResult, bool, error) {
	if s == nil || s.tokens == nil || req.InstallationID <= 0 || req.PullNumber <= 0 || req.OutputKey == "" || req.HeadSHA == "" {
		return SubmitReviewResult{}, false, errors.New("incomplete assessment publication identity")
	}
	owner, repo, ok := strings.Cut(req.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return SubmitReviewResult{}, false, errors.New("invalid repository")
	}
	ctx = withGitHubInstallationTelemetry(ctx, req.InstallationID)
	token, err := s.tokens.GetInstallationToken(ctx, req.InstallationID)
	if err != nil {
		return SubmitReviewResult{}, false, err
	}
	result, found, err := s.findExistingReview(ctx, token, owner, repo, req.PullNumber, req.OutputKey)
	if err != nil || !found {
		return result, found, err
	}
	if req.Decision == SubmitReviewDecisionApproved {
		if req.ExistingReviewID == 0 {
			if result.SubmittedCommitSHA != req.HeadSHA || result.ReviewState != "APPROVED" {
				return result, false, nil
			}
			result.FormalApprovalID = &result.ID
			result.FormalApprovalURL = &result.URL
		} else {
			approval, found, err := s.findExistingReview(ctx, token, owner, repo, req.PullNumber, req.OutputKey+":formal-approval")
			if err != nil || !found {
				return result, false, err
			}
			if approval.SubmittedCommitSHA != req.HeadSHA || approval.ReviewState != "APPROVED" {
				return result, false, nil
			}
			result.FormalApprovalID = &approval.ID
			result.FormalApprovalURL = &approval.URL
		}
	}
	result.Body = req.Body
	return result, true, nil
}
