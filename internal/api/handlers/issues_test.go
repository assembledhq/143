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
	"github.com/stretchr/testify/require"
)

func TestIssueHandler_List(t *testing.T) {
	t.Parallel()

	issueColumns := []string{
		"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
		"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
		"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
		"created_at", "updated_at", "deleted_at",
	}

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns issues for org successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).AddRow(
							issueID, orgID, "ext-1", "sentry", nil, nil,
							"Test Issue", nil, json.RawMessage(`{}`), "open", now, now,
							5, 2, "high", []string{"bug"}, "fp123",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name: "returns empty list when no issues exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(issueColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewIssueStore(mock)
			handler := NewIssueHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.Issue]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of issues")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIssueHandler_Get(t *testing.T) {
	t.Parallel()

	issueColumns := []string{
		"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
		"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
		"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
		"created_at", "updated_at", "deleted_at",
	}

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "returns issue by ID successfully",
			idParam: "", // will be set to a valid UUID in the subtest
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).AddRow(
							issueID, orgID, "ext-1", "sentry", nil, nil,
							"Found Issue", nil, json.RawMessage(`{}`), "open", now, now,
							3, 1, "medium", []string{}, "fp456",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "Found Issue",
		},
		{
			name:         "returns bad request for invalid UUID",
			idParam:      "not-a-uuid",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewIssueStore(mock)
			handler := NewIssueHandler(store)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
