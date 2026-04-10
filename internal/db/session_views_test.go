package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestNewSessionViewStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)
	require.NotNil(t, store, "should create a non-nil store")
}

func TestSessionViewStore_Upsert_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)

	mock.ExpectExec("INSERT INTO session_views").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = store.Upsert(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err, "should upsert session view without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionViewStore_Upsert_Error(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)

	mock.ExpectExec("INSERT INTO session_views").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db error"))

	err = store.Upsert(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error on database failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionViewStore_BatchGetLastViewed_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)

	userID := uuid.New()
	sid1 := uuid.New()
	sid2 := uuid.New()
	now := time.Now()
	earlier := now.Add(-time.Hour)

	mock.ExpectQuery("SELECT session_id, last_viewed_at FROM session_views").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "last_viewed_at"}).
				AddRow(sid1, now).
				AddRow(sid2, earlier),
		)

	result, err := store.BatchGetLastViewed(context.Background(), userID, []uuid.UUID{sid1, sid2})
	require.NoError(t, err, "should retrieve last viewed times without error")
	require.Len(t, result, 2, "should return entries for both sessions")
	require.Equal(t, now, result[sid1], "should return correct time for first session")
	require.Equal(t, earlier, result[sid2], "should return correct time for second session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionViewStore_BatchGetLastViewed_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)

	result, err := store.BatchGetLastViewed(context.Background(), uuid.New(), []uuid.UUID{})
	require.NoError(t, err, "should return nil without error for empty input")
	require.Nil(t, result, "should return nil map for empty session IDs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionViewStore_BatchGetLastViewed_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionViewStore(mock)

	mock.ExpectQuery("SELECT session_id, last_viewed_at FROM session_views").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db error"))

	result, err := store.BatchGetLastViewed(context.Background(), uuid.New(), []uuid.UUID{uuid.New()})
	require.Error(t, err, "should return an error on query failure")
	require.Nil(t, result, "should return nil map on error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
