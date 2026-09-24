package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreLockAndRejectIfCodeReviewOwned(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		owner   bool
		missing bool
	}{
		{name: "unowned session can enqueue"},
		{name: "owned session is fenced", owner: true},
		{name: "cross-org or missing session is absent", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, prID := uuid.New(), uuid.New(), uuid.New()
			query := mock.ExpectQuery(`SELECT code_review_owner_pr_id FROM sessions WHERE org_id=\$1 AND id=\$2 FOR UPDATE`).WithArgs(orgID, sessionID)
			if tt.missing {
				query.WillReturnRows(pgxmock.NewRows([]string{"code_review_owner_pr_id"}))
			} else if tt.owner {
				query.WillReturnRows(pgxmock.NewRows([]string{"code_review_owner_pr_id"}).AddRow(prID.String()))
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"code_review_owner_pr_id"}).AddRow(nil))
			}
			err = NewSessionStore(mock).LockAndRejectIfCodeReviewOwned(context.Background(), orgID, sessionID)
			switch {
			case tt.owner:
				var owned *models.SessionCodeReviewOwnedError
				require.ErrorAs(t, err, &owned, "owned conversation must reject publication")
				require.Equal(t, prID, owned.PullRequestID, "conflict should identify owner PR")
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing or cross-org session must not enqueue")
			default:
				require.NoError(t, err, "unowned session should permit publication")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "query must lock only the requested tenant session")
		})
	}
}
