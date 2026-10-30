package worker

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func terminalConfirmedPointer[T any](value T) *T { return &value }

// This pins the existing recovery predicate and its caller's confirmed-state
// precondition as the reference for the bounded DB retirement proof. It does
// not execute SQL or claim that a mock proves PostgreSQL JSON/type semantics.
func TestCodeReviewTerminalConfirmedReceiptReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*models.CodeReviewAssessment, *models.CodeReviewSessionMetadata)
		receipt func(map[string]any)
		raw     json.RawMessage
		want    bool
	}{
		{name: "exact confirmed staged full receipt", want: true},
		{name: "matching metadata provider identity", want: true, mutate: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			m.GitHubReviewID = terminalConfirmedPointer(*a.GitHubReviewID)
		}},
		{name: "incomplete coverage still uses the same receipt predicate", want: true, mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.CoverageComplete = false }},
		{name: "uncertain state does not inherit caller authority", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.PublicationState = models.CodeReviewPublicationUncertain
		}},
		{name: "reserved state does not inherit caller authority", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.PublicationState = models.CodeReviewPublicationReserved
		}},
		{name: "running assessment is not confirmed completion recovery", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.Status = models.CodeReviewAssessmentRunning
		}},
		{name: "completed assessment does not acquire a new pending owner", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.Status = models.CodeReviewAssessmentCompleted
		}},
		{name: "cancelled assessment is not resurrected", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.Status = models.CodeReviewAssessmentCancelled
		}},
		{name: "evidence recheck remains excluded", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.ReviewScope = models.CodeReviewScopeEvidenceOnly
		}},
		{name: "missing staged origin", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.ResultOrigin = nil }},
		{name: "missing staged decision", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.Decision = nil }},
		{name: "missing staged acceptability", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.Acceptable = nil }},
		{name: "missing staged body", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.RenderedBody = nil }},
		{name: "missing staged structured outcome", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.StructuredOutcome = nil }},
		{name: "missing provider review identity", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.GitHubReviewID = nil }},
		{name: "missing submitted head", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.SubmittedCommitSHA = nil }},
		{name: "submitted head differs from original A", mutate: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.SubmittedCommitSHA = terminalConfirmedPointer("different-head")
		}},
		{name: "metadata provider review differs", mutate: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			m.GitHubReviewID = terminalConfirmedPointer(int64(999))
		}},
		{name: "receipt assessment differs", receipt: func(r map[string]any) { r["assessment_id"] = uuid.NewString() }},
		{name: "receipt assessment is not string", receipt: func(r map[string]any) { r["assessment_id"] = 17 }},
		{name: "receipt digest differs", receipt: func(r map[string]any) { r["input_digest"] = "different-digest" }},
		{name: "receipt digest is not string", receipt: func(r map[string]any) { r["input_digest"] = false }},
		{name: "receipt submitted head differs", receipt: func(r map[string]any) { r["submitted_commit_sha"] = "different-head" }},
		{name: "receipt submitted head is not string", receipt: func(r map[string]any) { r["submitted_commit_sha"] = 17 }},
		{name: "receipt review identity differs", receipt: func(r map[string]any) { r["github_review_id"] = int64(999) }},
		{name: "receipt review identity missing", receipt: func(r map[string]any) { delete(r, "github_review_id") }},
		{name: "receipt review identity null", receipt: func(r map[string]any) { r["github_review_id"] = nil }},
		{name: "receipt review identity quoted", receipt: func(r map[string]any) { r["github_review_id"] = "731" }},
		{name: "receipt review identity fractional", receipt: func(r map[string]any) { r["github_review_id"] = json.RawMessage("731.0") }},
		{name: "receipt review identity out of int64 range", receipt: func(r map[string]any) { r["github_review_id"] = json.RawMessage("9223372036854775808") }},
		{name: "receipt wrong JSON shape", raw: json.RawMessage(`[]`)},
		{name: "receipt malformed JSON", raw: json.RawMessage(`{"assessment_id":`)},
		{name: "receipt JSON null", raw: json.RawMessage(`null`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := models.CodeReviewAssessment{
				ID: uuid.New(), ReviewScope: models.CodeReviewScopeFull, Status: models.CodeReviewAssessmentPublishing,
				PublicationState: models.CodeReviewPublicationConfirmed, InputDigest: "synthetic-digest", HeadSHA: "synthetic-head",
				ResultOrigin: terminalConfirmedPointer(models.CodeReviewResultExecuted), Decision: terminalConfirmedPointer(models.CodeReviewDecisionApproved),
				Acceptable: terminalConfirmedPointer(true), RenderedBody: terminalConfirmedPointer("Synthetic retained review"),
				StructuredOutcome: json.RawMessage(`{"decision":"approved"}`), CoverageComplete: true,
				GitHubReviewID: terminalConfirmedPointer(int64(731)), SubmittedCommitSHA: terminalConfirmedPointer("synthetic-head"),
			}
			metadata := models.CodeReviewSessionMetadata{}
			receipt := map[string]any{
				"assessment_id": a.ID.String(), "input_digest": a.InputDigest,
				"submitted_commit_sha": a.HeadSHA, "github_review_id": *a.GitHubReviewID,
			}
			if tt.receipt != nil {
				tt.receipt(receipt)
			}
			raw, err := json.Marshal(receipt)
			require.NoError(t, err, "each reference case must construct its own finite synthetic receipt before mutation")
			a.PublicationReceipt = raw
			if tt.raw != nil {
				a.PublicationReceipt = tt.raw
			}
			if tt.mutate != nil {
				tt.mutate(&a, &metadata)
			}
			actual := a.PublicationState == models.CodeReviewPublicationConfirmed && confirmedFullAssessmentReceiptMatches(a, metadata)
			require.Equal(t, tt.want, actual, "the exact saved original full receipt and staged outcome, with confirmed caller precondition, define recovery authority rather than a legacy metadata review ID")
		})
	}
}
