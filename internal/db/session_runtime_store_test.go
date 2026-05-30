package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionStore_RecordRuntimeProgress(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectExec(`UPDATE sessions\s+SET runtime_last_progress_at = @runtime_last_progress_at`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.RecordRuntimeProgress(
		context.Background(),
		orgID,
		sessionID,
		models.RuntimeProgressTypeToolResult,
		models.RuntimeProgressStrengthStrong,
		time.Now().UTC(),
	)
	require.NoError(t, err, "RecordRuntimeProgress should update the runtime progress fields")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_MarkRuntimeStopRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		execErr      error
		expectErr    bool
		queryPattern string
	}{
		{
			name:         "records stop reason on running session",
			queryPattern: `UPDATE sessions\s+SET runtime_stop_reason = CASE`,
		},
		{
			name:         "wraps exec errors",
			execErr:      errors.New("write failed"),
			expectErr:    true,
			queryPattern: `runtime_stop_reason = CASE`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			expect := mock.ExpectExec(tt.queryPattern).
				WithArgs(
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
				)
			if tt.execErr != nil {
				expect.WillReturnError(tt.execErr)
			} else {
				expect.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}

			err = store.MarkRuntimeStopRequested(
				context.Background(),
				uuid.New(),
				uuid.New(),
				models.RuntimeStopReasonNoProgress,
				time.Now().UTC().Add(5*time.Minute),
			)
			if tt.expectErr {
				require.Error(t, err, "MarkRuntimeStopRequested should wrap database errors")
			} else {
				require.NoError(t, err, "MarkRuntimeStopRequested should persist the requested stop reason")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ListRuntimeControlStalledSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`runtime_graceful_stop_at < @stop_after_before`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListRuntimeControlStalledSessions(
		context.Background(),
		time.Now().UTC().Add(-2*time.Minute),
		time.Now().UTC().Add(-2*time.Minute),
	)
	require.NoError(t, err, "ListRuntimeControlStalledSessions should query stalled runtime rows")
	require.Empty(t, sessions, "empty result set should return no sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GrantRuntimeExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		lockToken     uuid.UUID
		rowsAffected  int64
		execErr       error
		expectGranted bool
		expectErr     bool
		queryPattern  string
	}{
		{
			name:          "grants extension without lock token",
			rowsAffected:  1,
			expectGranted: true,
			queryPattern:  `runtime_extension_count = runtime_extension_count \+ 1`,
		},
		{
			name:          "grants extension with lock token fencing",
			lockToken:     uuid.New(),
			rowsAffected:  1,
			expectGranted: true,
			queryPattern:  `AND EXISTS \(`,
		},
		{
			name:          "returns false when compare and swap misses",
			rowsAffected:  0,
			expectGranted: false,
			queryPattern:  `runtime_extension_seconds = runtime_extension_seconds \+ @extension_seconds`,
		},
		{
			name:         "wraps exec errors",
			execErr:      errors.New("write failed"),
			expectErr:    true,
			queryPattern: `runtime_extension_count = runtime_extension_count \+ 1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			expect := mock.ExpectExec(tt.queryPattern)
			if tt.lockToken != uuid.Nil {
				expect.WithArgs(
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
				)
			} else {
				expect.WithArgs(
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
				)
			}
			if tt.execErr != nil {
				expect.WillReturnError(tt.execErr)
			} else {
				expect.WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))
			}

			granted, err := store.GrantRuntimeExtension(
				context.Background(),
				uuid.New(),
				uuid.New(),
				tt.lockToken,
				time.Now().UTC(),
				time.Now().UTC().Add(2*time.Minute),
				time.Now().UTC().Add(10*time.Minute),
				120,
			)
			if tt.expectErr {
				require.Error(t, err, "GrantRuntimeExtension should wrap database errors")
				require.False(t, granted, "GrantRuntimeExtension should return false on error")
			} else {
				require.NoError(t, err, "GrantRuntimeExtension should not return an error")
				require.Equal(t, tt.expectGranted, granted, "GrantRuntimeExtension should report whether the write succeeded")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_PublishCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		lockToken     uuid.UUID
		rowsAffected  int64
		queryErr      error
		expectOK      bool
		expectErr     bool
		queryPattern  string
		checkpointErr *string
	}{
		{
			name:         "publishes checkpoint without lock token",
			rowsAffected: 1,
			expectOK:     true,
			queryPattern: `checkpoint_kind = @checkpoint_kind`,
		},
		{
			name:         "publishes checkpoint with lock token fencing",
			lockToken:    uuid.New(),
			rowsAffected: 1,
			expectOK:     true,
			queryPattern: `AND EXISTS \(`,
		},
		{
			name:         "returns false when publish loses the race",
			rowsAffected: 0,
			expectOK:     false,
			queryPattern: `runtime_stop_reason = @runtime_stop_reason`,
		},
		{
			name:         "wraps publish errors",
			queryErr:     errors.New("write failed"),
			expectErr:    true,
			queryPattern: `checkpointed_at = @checkpointed_at`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			expect := mock.ExpectQuery(tt.queryPattern)
			if tt.lockToken != uuid.Nil {
				expect.WithArgs(
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
				)
			} else {
				expect.WithArgs(
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
					pgxmock.AnyArg(),
				)
			}
			if tt.queryErr != nil {
				expect.WillReturnError(tt.queryErr)
			} else if tt.rowsAffected == 0 {
				expect.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}))
			} else {
				expect.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).
					AddRow(int64(2), time.Now().UTC()))
			}

			ok, err := store.PublishCheckpoint(
				context.Background(),
				uuid.New(),
				uuid.New(),
				tt.lockToken,
				"agent-session-1",
				"snapshot-key-1",
				models.CheckpointKindGracefulStop,
				models.CheckpointCapabilityFullResume,
				2048,
				time.Now().UTC(),
				tt.checkpointErr,
				models.RuntimeStopReasonSoftBudget,
			)
			if tt.expectErr {
				require.Error(t, err, "PublishCheckpoint should wrap database errors")
				require.False(t, ok, "PublishCheckpoint should return false on error")
			} else {
				require.NoError(t, err, "PublishCheckpoint should not return an error")
				require.Equal(t, tt.expectOK, ok, "PublishCheckpoint should report whether the write succeeded")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_RequestCancelAndConsumeCancelRequest(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	tests := []struct {
		name          string
		run           func(context.Context, *SessionStore) (bool, error)
		expectQuery   string
		rowsAffected  int64
		expectPending bool
	}{
		{
			name: "request upserts pending cancel intent",
			run: func(ctx context.Context, store *SessionStore) (bool, error) {
				return false, store.RequestCancel(ctx, orgID, sessionID)
			},
			expectQuery:  `INSERT INTO session_cancel_requests`,
			rowsAffected: 1,
		},
		{
			name: "consume returns true for pending cancel intent",
			run: func(ctx context.Context, store *SessionStore) (bool, error) {
				return store.ConsumeCancelRequest(ctx, orgID, sessionID)
			},
			expectQuery:   `UPDATE session_cancel_requests`,
			rowsAffected:  1,
			expectPending: true,
		},
		{
			name: "consume returns false when no pending cancel intent exists",
			run: func(ctx context.Context, store *SessionStore) (bool, error) {
				return store.ConsumeCancelRequest(ctx, orgID, sessionID)
			},
			expectQuery:   `UPDATE session_cancel_requests`,
			rowsAffected:  0,
			expectPending: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			mock.ExpectExec(tt.expectQuery).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))

			pending, err := tt.run(context.Background(), NewSessionStore(mock))

			require.NoError(t, err, "cancel request store operation should succeed")
			require.Equal(t, tt.expectPending, pending, "consume should report whether a pending request was claimed")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_UpdateRecoveryState(t *testing.T) {
	t.Parallel()

	t.Run("clears recovery timestamps", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET recovery_state = @recovery_state`).
			WithArgs(
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
			).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err = store.UpdateRecoveryState(
			context.Background(),
			uuid.New(),
			uuid.New(),
			models.RecoveryStateNone,
			nil,
			nil,
			false,
		)
		require.NoError(t, err, "UpdateRecoveryState should allow clearing queued and started timestamps")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("stores queued and started timestamps", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`recovery_attempt_count = CASE\s+WHEN @increment_attempt THEN recovery_attempt_count \+ 1`).
			WithArgs(
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
			).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		queuedAt := time.Now().UTC()
		startedAt := queuedAt.Add(time.Minute)
		err = store.UpdateRecoveryState(
			context.Background(),
			uuid.New(),
			uuid.New(),
			models.RecoveryStateRecovering,
			&queuedAt,
			&startedAt,
			true,
		)
		require.NoError(t, err, "UpdateRecoveryState should persist queued and started timestamps when provided")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}
