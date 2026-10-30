// Package codereviewreceipt compares the retained full-review publication
// receipt without depending on storage, workers, or provider clients.
package codereviewreceipt

import "encoding/json"

// Matches retains encoding/json's tagged-struct behavior, including folded
// field names, member order, sticky type errors, and pointer-versus-string null
// handling. Callers supply their separate staged, state, and owner conditions.
func Matches(raw []byte, assessmentID, inputDigest, headSHA string, githubReviewID int64) bool {
	var receipt struct {
		AssessmentID       string `json:"assessment_id"`
		InputDigest        string `json:"input_digest"`
		SubmittedCommitSHA string `json:"submitted_commit_sha"`
		GitHubReviewID     *int64 `json:"github_review_id"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return false
	}
	return receipt.AssessmentID == assessmentID && receipt.InputDigest == inputDigest &&
		receipt.SubmittedCommitSHA == headSHA && receipt.GitHubReviewID != nil && *receipt.GitHubReviewID == githubReviewID
}
