package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
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

func jobDedupeKeyPtr(s string) *string {
	return &s
}
