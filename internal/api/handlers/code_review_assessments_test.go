package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAssessmentEndpointsScopeLookupToAuthenticatedOrganization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, id    string
		unavailable bool
		expected    int
	}{
		{name: "inaccessible detail", id: uuid.NewString(), expected: http.StatusNotFound},
		{name: "inaccessible evidence", id: uuid.NewString(), expected: http.StatusNotFound},
		{name: "invalid identity", id: "bad", expected: http.StatusBadRequest},
		{name: "reader unavailable", id: uuid.NewString(), unavailable: true, expected: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated database mock")
			defer mock.Close()
			h := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			if !tt.unavailable {
				h.SetAssessments(db.NewCodeReviewAssessmentStore(mock), nil, nil)
			}
			if tt.expected == http.StatusNotFound {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2")).WithArgs(orgID, uuid.MustParse(tt.id)).WillReturnError(pgx.ErrNoRows)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/code-review-assessments/"+tt.id, nil)
			ctx := chi.NewRouteContext()
			ctx.URLParams.Add("id", tt.id)
			r = r.WithContext(middleware.WithOrgID(r.Context(), orgID))
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
			w := httptest.NewRecorder()
			if tt.name == "inaccessible evidence" {
				h.AssessmentEvidence(w, r)
			} else {
				h.GetAssessment(w, r)
			}
			require.Equal(t, tt.expected, w.Code, "assessment reader must reject malformed, unavailable, or cross-tenant identity")
			require.NoError(t, mock.ExpectationsWereMet(), "lookup must bind authenticated org and exact assessment ID")
		})
	}
}

func TestCodeReviewAssessmentAuditFields(t *testing.T) {
	t.Parallel()
	findingID := uuid.New()
	tests := []struct {
		name       string
		assessment models.CodeReviewAssessment
		wantStatus models.CodeReviewFindingReassessmentStatus
		wantText   json.RawMessage
		wantError  bool
	}{
		{
			name: "current evidence audit",
			assessment: models.CodeReviewAssessment{
				StructuredOutcome: json.RawMessage(`{"finding_reassessments":[{"finding_id":"` + findingID.String() + `","status":"resolved","reason":"Tests now pass","evidence_citations":[{"evidence_id":"text-1","quote":"passed"}]}],"requirement_reassessments":[{"key":"testing","status":"satisfied","reason":"Test output captured","evidence_citations":[{"evidence_id":"text-1","quote":"passed"}]}]}`),
				InputManifest:     json.RawMessage(`{"text_evidence":{"items":[{"evidence_id":"text-1","content":"passed"}],"complete":true}}`),
			},
			wantStatus: models.CodeReviewFindingResolved,
			wantText:   json.RawMessage(`{"items":[{"evidence_id":"text-1","content":"passed"}],"complete":true}`),
		},
		{name: "older assessment", assessment: models.CodeReviewAssessment{}, wantText: nil},
		{name: "malformed outcome", assessment: models.CodeReviewAssessment{StructuredOutcome: json.RawMessage(`{`)}, wantError: true},
		{name: "malformed manifest", assessment: models.CodeReviewAssessment{InputManifest: json.RawMessage(`{`)}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome, textEvidence, err := codeReviewAssessmentAuditFields(tt.assessment)
			if tt.wantError {
				require.Error(t, err, "malformed immutable assessment evidence should fail closed")
				return
			}
			require.NoError(t, err, "valid assessment evidence should decode")
			require.Equal(t, tt.wantText, textEvidence, "text evidence should come from the immutable manifest")
			if tt.wantStatus != "" {
				require.Equal(t, []models.CodeReviewFindingReassessment{{FindingID: findingID, Status: tt.wantStatus, Reason: "Tests now pass", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "text-1", Quote: "passed"}}}}, outcome.FindingReassessments, "finding audit should preserve status, reason, and exact citation")
				require.Equal(t, []models.CodeReviewRequirementReassessment{{Key: "testing", Status: models.CodeReviewRequirementSatisfied, Reason: "Test output captured", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "text-1", Quote: "passed"}}}}, outcome.RequirementReassessments, "requirement audit should preserve the current assessment result")
			} else {
				require.Empty(t, outcome.FindingReassessments, "older assessments should have no invented finding reassessments")
			}
		})
	}
}
