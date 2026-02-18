package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueHandler_List_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	store := db.NewIssueStore(mock)
	handler := NewIssueHandler(store)

	mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at",
			}).AddRow(
				issueID, orgID, "ext-1", "sentry", nil, nil,
				"Test Issue", nil, json.RawMessage(`{}`), "open", now, now,
				5, 2, "high", []string{"bug"}, "fp123",
				now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.Issue]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, "Test Issue", resp.Data[0].Title)
	assert.Equal(t, "high", resp.Data[0].Severity)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueHandler_List_Empty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewIssueStore(mock)
	handler := NewIssueHandler(store)

	mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at",
			}),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.Issue]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueHandler_Get_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	store := db.NewIssueStore(mock)
	handler := NewIssueHandler(store)

	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at",
			}).AddRow(
				issueID, orgID, "ext-1", "sentry", nil, nil,
				"Found Issue", nil, json.RawMessage(`{}`), "open", now, now,
				3, 1, "medium", []string{}, "fp456",
				now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+issueID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.Issue]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Found Issue", resp.Data.Title)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueHandler_Get_InvalidID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIssueStore(mock)
	handler := NewIssueHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/not-a-uuid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ID")
}
