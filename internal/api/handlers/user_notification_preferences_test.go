package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestUserNotificationPreferenceHandler_Get(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := db.NewUserNotificationPreferenceStore(mock)
	handler := NewUserNotificationPreferenceHandler(store)

	rows := pgxmock.NewRows([]string{"org_id", "user_id", "session_completion_browser_enabled", "created_at", "updated_at"}).
		AddRow(orgID, userID, true, now, now)
	mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/notification-preferences", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Get should return 200")
	var resp struct {
		Data models.UserNotificationPreference `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.True(t, resp.Data.SessionCompletionBrowserEnabled, "Get should return stored preference")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserNotificationPreferenceHandler_Update(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := db.NewUserNotificationPreferenceStore(mock)
	handler := NewUserNotificationPreferenceHandler(store)

	mock.ExpectExec("INSERT INTO user_notification_preferences").WithArgs(orgID, userID, true).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	rows := pgxmock.NewRows([]string{"org_id", "user_id", "session_completion_browser_enabled", "created_at", "updated_at"}).
		AddRow(orgID, userID, true, now, now)
	mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnRows(rows)

	body := bytes.NewBufferString(`{"session_completion_browser_enabled":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/account/notification-preferences", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Update should return 200")
	var resp struct {
		Data models.UserNotificationPreference `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.True(t, resp.Data.SessionCompletionBrowserEnabled, "Update should persist enabled preference")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserNotificationPreferenceHandler_Get_Unauthorized(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	handler := NewUserNotificationPreferenceHandler(db.NewUserNotificationPreferenceStore(mock))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/notification-preferences", nil)
	req = req.WithContext(context.Background())
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "Get should return 401 without authenticated user")
}

func TestUserNotificationPreferenceHandler_Get_StoreError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	handler := NewUserNotificationPreferenceHandler(db.NewUserNotificationPreferenceStore(mock))
	mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnError(context.Canceled)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/notification-preferences", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "Get should return 500 when the store read fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserNotificationPreferenceHandler_Update_Unauthorized(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	handler := NewUserNotificationPreferenceHandler(db.NewUserNotificationPreferenceStore(mock))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/account/notification-preferences", bytes.NewBufferString(`{"session_completion_browser_enabled":true}`))
	req = req.WithContext(context.Background())
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "Update should return 401 without authenticated user")
}

func TestUserNotificationPreferenceHandler_Update_InvalidJSON(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	handler := NewUserNotificationPreferenceHandler(db.NewUserNotificationPreferenceStore(mock))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/account/notification-preferences", bytes.NewBufferString("{"))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Update should return 400 for invalid JSON")
}

func TestUserNotificationPreferenceHandler_Update_StoreErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name            string
		setupMock       func(mock pgxmock.PgxPoolIface)
		expectedStatus  int
		expectedMessage string
	}{
		{
			name: "returns 500 when upsert fails",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO user_notification_preferences").WithArgs(orgID, userID, true).WillReturnError(context.DeadlineExceeded)
			},
			expectedStatus:  http.StatusInternalServerError,
			expectedMessage: "Update should return 500 when the store upsert fails",
		},
		{
			name: "returns 500 when follow-up read fails",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO user_notification_preferences").WithArgs(orgID, userID, true).WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectQuery("SELECT").WithArgs(orgID, userID).WillReturnError(context.DeadlineExceeded)
			},
			expectedStatus:  http.StatusInternalServerError,
			expectedMessage: "Update should return 500 when follow-up preference lookup fails",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			defer mock.Close()

			handler := NewUserNotificationPreferenceHandler(db.NewUserNotificationPreferenceStore(mock))
			tt.setupMock(mock)

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/account/notification-preferences", bytes.NewBufferString(`{"session_completion_browser_enabled":true}`))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.Update(rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, tt.expectedMessage)
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
