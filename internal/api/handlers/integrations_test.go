package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestNewIntegrationHandler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store)
	require.NotNil(t, handler, "handler should not be nil")
}

func TestIntegrationHandler_ListIntegrations_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store)

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")
	require.Contains(t, w.Body.String(), `"data":[]`, "should return empty array for no integrations")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ListIntegrations_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store)

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 on DB error")
	require.Contains(t, w.Body.String(), "LIST_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}
