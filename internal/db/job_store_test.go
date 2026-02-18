package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobStore_Enqueue_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()

	payload := map[string]string{"issue_id": "abc-123"}

	// 6 named args: org_id, queue, job_type, payload, priority, dedupe_key
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(generatedID),
		)

	id, err := store.Enqueue(context.Background(), orgID, "default", "process_issue", payload, 1, nil)
	require.NoError(t, err)
	assert.Equal(t, generatedID, id)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestJobStore_Enqueue_WithDedupeKey(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()

	payload := map[string]string{"repo_id": "repo-456"}
	dedupeKey := "sync-repo-456"

	// 6 named args: org_id, queue, job_type, payload, priority, dedupe_key
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(generatedID),
		)

	id, err := store.Enqueue(context.Background(), orgID, "sync", "sync_repo", payload, 5, &dedupeKey)
	require.NoError(t, err)
	assert.Equal(t, generatedID, id)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestJobStore_Enqueue_MarshalError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()

	// Pass a channel which cannot be marshaled to JSON
	payload := make(chan int)

	id, err := store.Enqueue(context.Background(), orgID, "default", "bad_job", payload, 1, nil)
	assert.Error(t, err)
	assert.Equal(t, uuid.Nil, id)
	assert.NoError(t, mock.ExpectationsWereMet())
}
