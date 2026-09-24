package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
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
