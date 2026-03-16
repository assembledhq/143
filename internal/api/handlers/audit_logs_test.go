package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// mockAuditLogReader is a hand-written mock for the auditLogReader interface.
type mockAuditLogReader struct {
	listFn    func(ctx context.Context, orgID uuid.UUID, filters db.AuditLogFilters) ([]models.AuditLog, error)
	getByIDFn func(ctx context.Context, orgID uuid.UUID, id int64) (*models.AuditLog, error)
}

func (m *mockAuditLogReader) List(ctx context.Context, orgID uuid.UUID, filters db.AuditLogFilters) ([]models.AuditLog, error) {
	return m.listFn(ctx, orgID, filters)
}

func (m *mockAuditLogReader) GetByID(ctx context.Context, orgID uuid.UUID, id int64) (*models.AuditLog, error) {
	return m.getByIDFn(ctx, orgID, id)
}

func TestAuditLogHandler_List(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	orgID := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	userID := uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001")

	tests := []struct {
		name         string
		queryString  string
		setupMock    func() *mockAuditLogReader
		expectedCode int
		expectedBody models.ListResponse[models.AuditLog]
	}{
		{
			name:        "returns audit logs successfully",
			queryString: "",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{
					listFn: func(_ context.Context, _ uuid.UUID, _ db.AuditLogFilters) ([]models.AuditLog, error) {
						return []models.AuditLog{
							{
								ID: 1, OrgID: orgID, ActorType: models.AuditActorUser,
								ActorID: userID.String(), UserID: &userID,
								Action: models.AuditActionSessionCreated, ResourceType: models.AuditResourceSession,
								Details: json.RawMessage(`{}`), CreatedAt: now,
							},
						}, nil
					},
				}
			},
			expectedCode: http.StatusOK,
			expectedBody: models.ListResponse[models.AuditLog]{
				Data: []models.AuditLog{
					{
						ID: 1, OrgID: orgID, ActorType: models.AuditActorUser,
						ActorID: userID.String(), UserID: &userID,
						Action: models.AuditActionSessionCreated, ResourceType: models.AuditResourceSession,
						Details: json.RawMessage(`{}`), CreatedAt: now,
					},
				},
				Meta: models.PaginationMeta{},
			},
		},
		{
			name:        "returns empty list when no logs exist",
			queryString: "",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{
					listFn: func(_ context.Context, _ uuid.UUID, _ db.AuditLogFilters) ([]models.AuditLog, error) {
						return nil, nil
					},
				}
			},
			expectedCode: http.StatusOK,
			expectedBody: models.ListResponse[models.AuditLog]{
				Data: []models.AuditLog{},
				Meta: models.PaginationMeta{},
			},
		},
		{
			name:        "returns bad request for invalid user_id",
			queryString: "?user_id=not-a-uuid",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{}
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "returns bad request for invalid since",
			queryString: "?since=not-a-date",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{}
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "returns bad request for invalid cursor",
			queryString: "?cursor=not-base64!!!",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{}
			},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockStore := tt.setupMock()
			handler := NewAuditLogHandler(mockStore)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs"+tt.queryString, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")

			if tt.expectedCode == http.StatusOK {
				var resp models.ListResponse[models.AuditLog]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "should parse response body as JSON")
				require.Equal(t, tt.expectedBody, resp, "should return the expected response body")
			}
		})
	}
}

func TestAuditLogHandler_Get(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	orgID := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")

	tests := []struct {
		name         string
		idParam      string
		setupMock    func() *mockAuditLogReader
		expectedCode int
		expectedBody *models.SingleResponse[*models.AuditLog]
	}{
		{
			name:    "returns audit log by ID",
			idParam: "42",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{
					getByIDFn: func(_ context.Context, _ uuid.UUID, _ int64) (*models.AuditLog, error) {
						return &models.AuditLog{
							ID: 42, OrgID: orgID, ActorType: models.AuditActorUser,
							ActorID: "actor-1", Action: models.AuditActionAuthLogin,
							ResourceType: models.AuditResourceUser, CreatedAt: now,
						}, nil
					},
				}
			},
			expectedCode: http.StatusOK,
			expectedBody: &models.SingleResponse[*models.AuditLog]{
				Data: &models.AuditLog{
					ID: 42, OrgID: orgID, ActorType: models.AuditActorUser,
					ActorID: "actor-1", Action: models.AuditActionAuthLogin,
					ResourceType: models.AuditResourceUser, CreatedAt: now,
				},
			},
		},
		{
			name:    "returns bad request for non-integer ID",
			idParam: "not-a-number",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{}
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "returns not found when entry does not exist",
			idParam: "999",
			setupMock: func() *mockAuditLogReader {
				return &mockAuditLogReader{
					getByIDFn: func(_ context.Context, _ uuid.UUID, _ int64) (*models.AuditLog, error) {
						return nil, fmt.Errorf("get audit log 999: %w", pgx.ErrNoRows)
					},
				}
			},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockStore := tt.setupMock()
			handler := NewAuditLogHandler(mockStore)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/"+tt.idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")

			if tt.expectedBody != nil {
				var resp models.SingleResponse[*models.AuditLog]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "should parse response body as JSON")
				require.Equal(t, *tt.expectedBody, resp, "should return the expected response body")
			}
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
