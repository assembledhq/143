package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRecheckHydratesImmutableBaselineOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		change    func(*models.CodeReviewAgentResult, *[]models.CodeReviewPromptRecord)
		wantError bool
	}{
		{name: "complete out of line output"},
		{name: "legacy output reference", change: func(r *models.CodeReviewAgentResult, _ *[]models.CodeReviewPromptRecord) {
			r.StructuredResult = json.RawMessage(`{"raw_artifact_key":"output"}`)
		}},
		{name: "missing linked record", change: func(_ *models.CodeReviewAgentResult, p *[]models.CodeReviewPromptRecord) { *p = nil }, wantError: true},
		{name: "another tenant", change: func(_ *models.CodeReviewAgentResult, p *[]models.CodeReviewPromptRecord) { (*p)[0].OrgID = uuid.New() }, wantError: true},
		{name: "another source session", change: func(_ *models.CodeReviewAgentResult, p *[]models.CodeReviewPromptRecord) {
			(*p)[0].SessionID = uuid.New()
		}, wantError: true},
		{name: "another result", change: func(_ *models.CodeReviewAgentResult, p *[]models.CodeReviewPromptRecord) {
			(*p)[0].Metadata = json.RawMessage(`{}`)
		}, wantError: true},
		{name: "truncated without record", change: func(r *models.CodeReviewAgentResult, _ *[]models.CodeReviewPromptRecord) {
			r.StructuredResult = nil
			s := "[truncated: prompt record store unavailable]"
			r.RawOutput = &s
		}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			baseline := models.CodeReviewAssessment{OrgID: uuid.New(), SessionID: uuid.New()}
			stub := "stored in prompt record"
			result := models.CodeReviewAgentResult{ID: uuid.New(), OrgID: baseline.OrgID, SessionID: baseline.SessionID, Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", RawOutput: &stub, StructuredResult: json.RawMessage(`{"raw_record_key":"output"}`)}
			metadata, err := json.Marshal(map[string]any{"result_id": result.ID})
			require.NoError(t, err, "fixture metadata should encode")
			complete := strings.Repeat("original review evidence\n", 2000)
			records := []models.CodeReviewPromptRecord{{OrgID: baseline.OrgID, SessionID: baseline.SessionID, RecordKey: "output", Role: "reviewer_output", AgentProvider: "codex", Content: complete, Metadata: metadata}}
			if tt.change != nil {
				tt.change(&result, &records)
			}
			got, err := recheckHydrateBaselineResults(baseline, []models.CodeReviewAgentResult{result}, records)
			if tt.wantError {
				require.Error(t, err, "incomplete or mismatched baseline must require full review")
				return
			}
			require.NoError(t, err, "linked complete baseline should reconstruct")
			expected := result
			expected.RawOutput = &complete
			require.Equal(t, []models.CodeReviewAgentResult{expected}, got, "the entire immutable reviewer output must reach context budgeting")
			require.Equal(t, stub, *result.RawOutput, "hydration must not rewrite historical source results")
		})
	}
}
