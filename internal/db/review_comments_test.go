package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestReviewCommentStore_IsDuplicate_NotDuplicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReviewCommentStore(mock)

	c := &models.ReviewComment{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	mock.ExpectQuery("SELECT filter_status FROM review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"filter_status"}).AddRow("pending"),
		)

	result := store.IsDuplicate(context.Background(), c)
	require.False(t, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReviewCommentStore_IsDuplicate_Duplicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReviewCommentStore(mock)

	c := &models.ReviewComment{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	mock.ExpectQuery("SELECT filter_status FROM review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"filter_status"}).AddRow("processed"),
		)

	result := store.IsDuplicate(context.Background(), c)
	require.True(t, result)
	require.NoError(t, mock.ExpectationsWereMet())
}
