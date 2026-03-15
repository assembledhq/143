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

var auditLogColumns = []string{
	"id", "org_id", "actor_type", "actor_id", "user_id",
	"action", "resource_type", "resource_id",
	"details", "request_id", "ip_address", "user_agent",
	"session_id", "project_id", "created_at",
}

func TestAuditLogHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		queryString  string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name:        "returns audit logs successfully",
			queryString: "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				userID := uuid.New()
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(auditLogColumns).AddRow(
							int64(1), orgID, "user", userID.String(), &userID,
							"session.created", "session", nil,
							json.RawMessage(`{}`), nil, nil, nil,
							nil, nil, now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name:        "returns empty list when no logs exist",
			queryString: "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:        "filters by actor_type",
			queryString: "?actor_type=user",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND actor_type").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:        "filters by action",
			queryString: "?action=auth.login",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND action").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:        "returns bad request for invalid user_id",
			queryString: "?user_id=not-a-uuid",
			setupMock:   func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedLen:  -1,
		},
		{
			name:        "returns bad request for invalid since",
			queryString: "?since=not-a-date",
			setupMock:   func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedLen:  -1,
		},
		{
			name:        "returns bad request for invalid cursor",
			queryString: "?cursor=not-base64!!!",
			setupMock:   func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedLen:  -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock database pool")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewAuditLogStore(mock)
			handler := NewAuditLogHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs"+tt.queryString, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")

			if tt.expectedLen >= 0 {
				var resp models.ListResponse[models.AuditLog]
				err = json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "should parse response body as JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of audit log entries")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
	}{
		{
			name:    "returns audit log by ID",
			idParam: "42",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(auditLogColumns).AddRow(
							int64(42), orgID, "user", "actor-1", nil,
							"auth.login", "user", nil,
							nil, nil, nil, nil,
							nil, nil, now,
						),
					)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:         "returns bad request for non-integer ID",
			idParam:      "not-a-number",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "returns not found when entry does not exist",
			idParam: "999",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock database pool")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewAuditLogStore(mock)
			handler := NewAuditLogHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/"+tt.idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Nanosecond)
	id := int64(12345)

	encoded := encodeAuditCursor(now, id)
	decodedTime, decodedID, err := decodeAuditCursor(encoded)
	require.NoError(t, err, "should decode a valid cursor without error")
	require.Equal(t, now.Format(time.RFC3339Nano), decodedTime.Format(time.RFC3339Nano), "decoded timestamp should match the original")
	require.Equal(t, id, decodedID, "decoded ID should match the original")
}

func TestDecodeAuditCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!invalid!!!"},
		{name: "missing comma", cursor: "dGVzdA=="},
		{name: "bad timestamp", cursor: "bm90LWEtdGltZSwxMjM="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeAuditCursor(tt.cursor)
			require.Error(t, err, "should reject invalid cursor input")
		})
	}
}
