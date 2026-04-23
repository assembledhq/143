package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestUserNotificationPreferenceStore_GetByUser(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  bool
		expectErr bool
	}{
		{
			name: "returns stored preference",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				rows := pgxmock.NewRows([]string{"org_id", "user_id", "session_completion_browser_enabled", "created_at", "updated_at"}).
					AddRow(orgID, userID, true, now, now)
				mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnRows(rows)
			},
			expected: true,
		},
		{
			name: "defaults to disabled when no row exists",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnRows(
					pgxmock.NewRows([]string{"org_id", "user_id", "session_completion_browser_enabled", "created_at", "updated_at"}),
				)
			},
			expected: false,
		},
		{
			name: "returns error when query fails",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnError(context.DeadlineExceeded)
			},
			expectErr: true,
		},
		{
			name: "returns error when row scan fails",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				rows := pgxmock.NewRows([]string{"org_id", "user_id", "session_completion_browser_enabled", "created_at", "updated_at"}).
					AddRow("not-a-uuid", userID, true, now, now)
				mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnRows(rows)
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			defer mock.Close()

			store := NewUserNotificationPreferenceStore(mock)
			tt.setupMock(mock)

			pref, getErr := store.GetByUser(context.Background(), orgID, userID)
			if tt.expectErr {
				require.Error(t, getErr, "GetByUser should return an error")
				return
			}

			require.NoError(t, getErr, "GetByUser should not return an error")
			require.Equal(t, tt.expected, pref.SessionCompletionBrowserEnabled, "GetByUser should return expected enabled state")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserNotificationPreferenceStore_Upsert(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := NewUserNotificationPreferenceStore(mock)
	mock.ExpectExec("INSERT INTO user_notification_preferences").WithArgs(orgID, userID, true).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	upsertErr := store.Upsert(context.Background(), orgID, userID, true)
	require.NoError(t, upsertErr, "Upsert should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserNotificationPreferenceStore_Upsert_Error(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := NewUserNotificationPreferenceStore(mock)
	mock.ExpectExec("INSERT INTO user_notification_preferences").WithArgs(orgID, userID, false).WillReturnError(context.Canceled)

	upsertErr := store.Upsert(context.Background(), orgID, userID, false)
	require.Error(t, upsertErr, "Upsert should return an error when exec fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
