package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewScheduleStoreGetRepositoryIDForPR(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		found bool
	}{
		{"owned PR resolves repository", true},
		{"missing or foreign PR stays inaccessible", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should initialize")
			defer mock.Close()
			orgID, prID, repoID := uuid.New(), uuid.New(), uuid.New()
			expectation := mock.ExpectQuery(`SELECT r\.id FROM pull_requests p JOIN repositories r ON r\.org_id=p\.org_id AND r\.full_name=p\.github_repo WHERE p\.org_id=\$1 AND p\.id=\$2`).WithArgs(orgID, prID)
			if tt.found {
				expectation.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(repoID))
			} else {
				expectation.WillReturnError(pgx.ErrNoRows)
			}
			got, err := NewCodeReviewScheduleStore(mock).GetRepositoryIDForPR(context.Background(), orgID, prID)
			if tt.found {
				require.NoError(t, err, "owned PR should resolve repository")
				require.Equal(t, repoID, got, "resolved repository should match org-scoped join")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing or foreign PR should not reveal a repository")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "query must filter org and PR identity")
		})
	}
}
