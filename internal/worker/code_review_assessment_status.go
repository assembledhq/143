package worker

import "github.com/assembledhq/143/internal/models"

// Project a recheck only for current publication. Historical session readers
// continue to return the original full review and its evidence.
func codeReviewAssessmentStatusMetadata(previous models.CodeReviewSessionMetadata, a models.CodeReviewAssessment) models.CodeReviewSessionMetadata {
	previous.PolicyID = a.PolicyID
	previous.HeadSHA, previous.BaseSHA = a.HeadSHA, a.BaseSHA
	previous.Decision, previous.Acceptable = a.Decision, a.Acceptable
	previous.ReviewOutputKey = a.PublicationKey
	previous.FinalReviewBody = a.RenderedBody
	previous.GitHubReviewID, previous.GitHubReviewURL = a.GitHubReviewID, a.GitHubReviewURL
	previous.CreatedAt, previous.CompletedAt = a.CreatedAt, a.CompletedAt
	previous.Phase, previous.StatusCode, previous.StatusMessage = nil, nil, nil
	previous.RetryAt, previous.LastErrorAt = nil, nil
	previous.FailureReason, previous.RetryableFailure = a.FailureDetail, false
	switch a.Status {
	case models.CodeReviewAssessmentCompleted:
		previous.Status = models.CodeReviewSessionStatusCompleted
	case models.CodeReviewAssessmentFailed:
		previous.Status = models.CodeReviewSessionStatusFailed
		message := "Evidence recheck failed. Open this assessment to retry or request a full review."
		previous.StatusMessage = &message
	case models.CodeReviewAssessmentCancelled, models.CodeReviewAssessmentSuperseded:
		previous.Status = models.CodeReviewSessionStatusCancelled
	default:
		previous.Status = models.CodeReviewSessionStatusRunning
	}
	return previous
}
