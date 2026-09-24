package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewDisputeAssessmentReadsAreTenantScoped(t *testing.T) {
	t.Parallel()
	orgID, assessmentID := uuid.New(), uuid.New()
	tests := []struct {
		name       string
		read       func(*CodeReviewStore) error
		query      string
		wantNoRows bool
	}{
		{name: "assessment", query: `SELECT \* FROM code_review_revision_assessments WHERE org_id=\$1 AND id=\$2`, read: func(store *CodeReviewStore) error {
			_, err := store.GetCompletedAssessmentByID(context.Background(), orgID, assessmentID)
			return err
		}, wantNoRows: true},
		{name: "source findings", query: `FROM code_review_findings WHERE org_id=\$1 AND assessment_id=\$2`, read: func(store *CodeReviewStore) error {
			_, err := store.ListAssessmentFindings(context.Background(), orgID, assessmentID)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mock database should initialize")
			defer mock.Close()
			mock.ExpectQuery(tt.query).WithArgs(orgID, assessmentID).WillReturnRows(pgxmock.NewRows([]string{}))
			err = tt.read(NewCodeReviewStore(mock))
			if tt.wantNoRows {
				require.ErrorIs(t, err, pgx.ErrNoRows, "unknown assessment should remain tenant scoped")
			} else {
				require.NoError(t, err, "empty assessment findings should remain tenant scoped")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "tenant-scoped query should include both exact IDs")
		})
	}
}
