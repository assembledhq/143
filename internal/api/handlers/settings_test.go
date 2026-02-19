package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func orgColumns() []string {
	return []string{"id", "name", "settings", "created_at", "updated_at"}
}

func TestSettingsHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "returns organization settings successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{"theme":"dark"}`), now, now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "Test Org",
		},
		{
			name: "returns not found when organization does not exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(orgColumns()))
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewOrganizationStore(mock)
			handler := NewSettingsHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSettingsHandler_Update(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "updates organization settings successfully",
			body: `{"name":"Updated Org"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				// GetByID
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{}`), now, now,
						),
					)
				// Update
				mock.ExpectQuery("UPDATE organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "Updated Org",
		},
		{
			name: "returns bad request for invalid JSON body",
			body: `not json`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewOrganizationStore(mock)
			handler := NewSettingsHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Update(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
