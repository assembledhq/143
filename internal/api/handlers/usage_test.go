package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestUsageHandler_GetSummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(store)

	orgID := uuid.New()

	// Totals
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_sessions"}).AddRow(42.5, 5))

	// Capacity breakdown
	mock.ExpectQuery("SELECT cpu_limit, memory_limit_mb").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"cpu_limit", "memory_limit_mb", "minutes", "sessions"}).
			AddRow(2.0, 4096, 42.5, 5))

	// Peak concurrent
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(concurrent\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"peak"}).AddRow(2))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=2026-04-01T00:00:00Z&end=2026-05-01T00:00:00Z", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetSummary(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.SingleResponse[models.UsageSummary]
	err = json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, 42.5, resp.Data.TotalContainerMinutes)
	require.Equal(t, 5, resp.Data.TotalSessions)
	require.Equal(t, 3, resp.Data.PeakConcurrent) // 2 peers + 1
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageHandler_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(store)

	orgID := uuid.New()
	sessionID := uuid.New()
	eventID := uuid.New()
	now := time.Now()
	dur := int64(60000)
	mins := 1.0
	reason := "completed"

	cols := []string{
		"id", "org_id", "session_id", "container_id", "provider",
		"cpu_limit", "memory_limit_mb", "image",
		"started_at", "stopped_at", "duration_ms", "container_minutes",
		"exit_reason", "created_at",
	}
	mock.ExpectQuery("SELECT .+ FROM container_usage_events WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).AddRow(
				eventID, orgID, sessionID, "ctr-1", "docker",
				2.0, 4096, "143-sandbox:latest",
				now, &now, &dur, &mins, &reason, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/usage", nil)
	// Set up chi route context with the "id" parameter.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ListBySession(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ListResponse[models.ContainerUsageEvent]
	err = json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "ctr-1", resp.Data[0].ContainerID)
	require.NoError(t, mock.ExpectationsWereMet())
}
