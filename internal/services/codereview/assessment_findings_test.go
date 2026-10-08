package codereview

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEffectiveAssessmentFindings(t *testing.T) {
	t.Parallel()
	orgID, sessionID, prID := uuid.New(), uuid.New(), uuid.New()
	sourceID, currentID := uuid.New(), uuid.New()
	retainedID, resolvedID := uuid.New(), uuid.New()
	source := models.CodeReviewAssessment{ID: sourceID, OrgID: orgID, SessionID: sessionID, PullRequestID: prID, Status: models.CodeReviewAssessmentCompleted, ReviewScope: models.CodeReviewScopeFull}
	findings := []models.CodeReviewFinding{{ID: retainedID, OrgID: orgID, SessionID: sessionID, Summary: "retained"}, {ID: resolvedID, OrgID: orgID, SessionID: sessionID, Summary: "resolved"}}
	outcome := json.RawMessage(`{"finding_reassessments":[{"finding_id":"` + retainedID.String() + `","status":"retained","reason":"Evidence does not address it","evidence_citations":[]},{"finding_id":"` + resolvedID.String() + `","status":"resolved","reason":"Current evidence proves fix","evidence_citations":[{"evidence_id":"text-1","quote":"passed"}]}]}`)
	current := models.CodeReviewAssessment{ID: currentID, OrgID: orgID, SessionID: sessionID, PullRequestID: prID, Status: models.CodeReviewAssessmentCompleted, ReviewScope: models.CodeReviewScopeEvidenceOnly, SourceAssessmentID: &sourceID, StructuredOutcome: outcome}
	tests := []struct {
		name     string
		current  models.CodeReviewAssessment
		source   models.CodeReviewAssessment
		findings []models.CodeReviewFinding
		want     []models.CodeReviewFinding
		wantErr  bool
	}{
		{name: "resolved finding omitted from current projection", current: current, source: source, findings: findings, want: findings[:1]},
		{name: "full assessment retains source findings", current: source, source: source, findings: findings, want: findings},
		{name: "missing disposition fails closed", current: func() models.CodeReviewAssessment {
			a := current
			a.StructuredOutcome = json.RawMessage(`{"finding_reassessments":[]}`)
			return a
		}(), source: source, findings: findings, wantErr: true},
		{name: "wrong source fails closed", current: current, source: func() models.CodeReviewAssessment { a := source; a.ID = uuid.New(); return a }(), findings: findings, wantErr: true},
		{name: "cross tenant finding fails closed", current: current, source: source, findings: []models.CodeReviewFinding{{ID: retainedID, OrgID: uuid.New(), SessionID: sessionID}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := EffectiveAssessmentFindings(tt.current, tt.source, tt.findings)
			if tt.wantErr {
				require.Error(t, err, "invalid assessment or finding provenance must fail closed")
				return
			}
			require.NoError(t, err, "valid assessment finding projection should succeed")
			require.Equal(t, tt.want, got, "current findings should match exact reassessment statuses")
		})
	}
}
