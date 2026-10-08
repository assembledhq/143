package models

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewFindingReassessmentStatusValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status CodeReviewFindingReassessmentStatus
		valid  bool
	}{
		{"retained", CodeReviewFindingRetained, true},
		{"resolved", CodeReviewFindingResolved, true},
		{"unknown", "ignored", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.valid {
				require.NoError(t, err, "known finding status should validate")
			} else {
				require.Error(t, err, "unknown finding status should be rejected")
			}
		})
	}
}

func TestCodeReviewFindingReassessmentValidate(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	tests := []struct {
		name   string
		change func(*CodeReviewFindingReassessment)
		valid  bool
	}{
		{"valid retained", func(*CodeReviewFindingReassessment) {}, true},
		{"valid resolved", func(r *CodeReviewFindingReassessment) { r.Status = CodeReviewFindingResolved }, true},
		{"missing finding", func(r *CodeReviewFindingReassessment) { r.FindingID = uuid.Nil }, false},
		{"missing reason", func(r *CodeReviewFindingReassessment) { r.Reason = " " }, false},
		{"missing evidence ID", func(r *CodeReviewFindingReassessment) { r.EvidenceCitations[0].EvidenceID = "" }, false},
		{"multiple quotes from one source", func(r *CodeReviewFindingReassessment) {
			r.EvidenceCitations = append(r.EvidenceCitations, CodeReviewEvidenceCitation{EvidenceID: "text-1", Quote: "another excerpt"})
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value := CodeReviewFindingReassessment{FindingID: id, Status: CodeReviewFindingRetained, Reason: "Still present", EvidenceCitations: []CodeReviewEvidenceCitation{{EvidenceID: "text-1", Quote: "still present"}}}
			tt.change(&value)
			err := value.Validate()
			if tt.valid {
				require.NoError(t, err, "complete reassessment should validate")
			} else {
				require.Error(t, err, "incomplete reassessment should be rejected")
			}
		})
	}
}

func TestCodeReviewRequirementReassessmentValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*CodeReviewRequirementReassessment)
		valid  bool
	}{
		{"satisfied", func(*CodeReviewRequirementReassessment) {}, true},
		{"missing", func(r *CodeReviewRequirementReassessment) { r.Status = CodeReviewRequirementMissing }, true},
		{"unknown status", func(r *CodeReviewRequirementReassessment) { r.Status = "partial" }, false},
		{"missing key", func(r *CodeReviewRequirementReassessment) { r.Key = "" }, false},
		{"missing reason", func(r *CodeReviewRequirementReassessment) { r.Reason = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value := CodeReviewRequirementReassessment{Key: "testing_evidence", Status: CodeReviewRequirementSatisfied, Reason: "Captured test output", EvidenceCitations: []CodeReviewEvidenceCitation{{EvidenceID: "text-1", Quote: "passed"}}}
			tt.change(&value)
			err := value.Validate()
			if tt.valid {
				require.NoError(t, err, "complete requirement reassessment should validate")
			} else {
				require.Error(t, err, "invalid requirement reassessment should be rejected")
			}
		})
	}
}
