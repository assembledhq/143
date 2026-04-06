package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestJobStore_Enqueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		queue     string
		jobType   string
		payload   any
		priority  int
		dedupeKey *string
		setupMock func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID)
		expectErr bool
	}{
		{
			name:      "enqueues job without dedupe key",
			queue:     "default",
			jobType:   "process_issue",
			payload:   map[string]string{"issue_id": "abc-123"},
			priority:  1,
			dedupeKey: nil,
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id"}).
							AddRow(generatedID),
					)
			},
		},
		{
			name:      "enqueues job with dedupe key",
			queue:     "sync",
			jobType:   "sync_repo",
			payload:   map[string]string{"repo_id": "repo-456"},
			priority:  5,
			dedupeKey: jobDedupeKeyPtr("sync-repo-456"),
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id"}).
							AddRow(generatedID),
					)
			},
		},
		{
			name:      "returns error when payload cannot be marshaled",
			queue:     "default",
			jobType:   "bad_job",
			payload:   make(chan int),
			priority:  1,
			dedupeKey: nil,
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				// No DB interaction expected since marshaling fails first
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewJobStore(mock)
			orgID := uuid.New()
			generatedID := uuid.New()
			tt.setupMock(mock, generatedID)

			id, err := store.Enqueue(context.Background(), orgID, tt.queue, tt.jobType, tt.payload, tt.priority, tt.dedupeKey)
			if tt.expectErr {
				require.Error(t, err, "Enqueue should return an error")
				require.Equal(t, uuid.Nil, id, "should return nil UUID on error")
				return
			}
			require.NoError(t, err, "Enqueue should not return an error")
			require.Equal(t, generatedID, id, "should return the generated job ID")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_GetLatestFailedByType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func(mock pgxmock.PgxPoolIface)
		wantResult bool
		expectErr  bool
	}{
		{
			name: "returns latest failed job",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				jobID := uuid.New()
				now := time.Now()
				mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "last_error", "updated_at"}).
							AddRow(jobID, "connection timeout", now),
					)
			},
			wantResult: true,
		},
		{
			name: "returns nil when no failed jobs exist",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewJobStore(mock)
			tt.setupMock(mock)

			result, err := store.GetLatestFailedByType(context.Background(), uuid.New(), "sync_repo")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantResult {
				require.NotNil(t, result)
				require.Equal(t, "connection timeout", result.LastError)
			} else {
				require.Nil(t, result)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestJobStore_DeleteExpiredCompleted(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)

	mock.ExpectQuery("SELECT delete_expired_completed_jobs").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_completed_jobs"}).AddRow(int64(42)))

	deleted, err := store.DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err)
	require.Equal(t, int64(42), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func jobDedupeKeyPtr(s string) *string {
	return &s
}
