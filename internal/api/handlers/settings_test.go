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
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orgColumns() []string {
	return []string{"id", "name", "slug", "settings", "created_at", "updated_at"}
}

func TestSettingsHandler_Get_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store)

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID, "Test Org", "test-org", json.RawMessage(`{"theme":"dark"}`), now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.Organization]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Test Org", resp.Data.Name)
	assert.Equal(t, "test-org", resp.Data.Slug)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSettingsHandler_Get_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store)

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "NOT_FOUND")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSettingsHandler_Update_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store)

	// GetByID
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID, "Test Org", "test-org", json.RawMessage(`{}`), now, now,
			),
		)

	// Update (uses QueryRow -> ExpectQuery, 4 named args: id, name, slug, settings)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
		)

	body := `{"name":"Updated Org"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.Organization]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Updated Org", resp.Data.Name)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSettingsHandler_Update_InvalidJSON(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`not json`))
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}
