package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestOrganizationsHandler_Create_Unauthenticated(t *testing.T) {
	t.Parallel()

	handler := NewOrganizationsHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", bytes.NewBufferString(`{"name":"Acme"}`))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestOrganizationsHandler_Create_PoolNotConfigured(t *testing.T) {
	t.Parallel()

	handler := NewOrganizationsHandler(nil)
	req := authedRequest(t, `{"name":"Acme"}`)
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestOrganizationsHandler_Create_InvalidBody(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewOrganizationsHandler(mock)
	req := authedRequest(t, "not json")
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_BODY")
}

func TestOrganizationsHandler_Create_MissingName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"empty", `{"name":""}`},
		{"whitespace only", `{"name":"   "}`},
		{"no field", `{}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			handler := NewOrganizationsHandler(mock)
			req := authedRequest(t, tc.body)
			w := httptest.NewRecorder()

			handler.Create(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "MISSING_NAME")
		})
	}
}

func TestOrganizationsHandler_Create_NameTooLong(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewOrganizationsHandler(mock)
	body, err := json.Marshal(map[string]string{"name": strings.Repeat("a", maxOrgNameLen+1)})
	require.NoError(t, err)

	req := authedRequest(t, string(body))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "NAME_TOO_LONG")
}

func TestOrganizationsHandler_Create_HappyPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	orgID := uuid.New()
	createdAt := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(orgID, createdAt, createdAt))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	handler := NewOrganizationsHandler(mock)

	req := authedRequestAs(t, userID, `{"name":"  Acme  "}`)
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
			Role string    `json:"role"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, orgID, resp.Data.ID)
	require.Equal(t, "Acme", resp.Data.Name, "whitespace should be trimmed before persistence")
	require.Equal(t, string(models.RoleAdmin), resp.Data.Role)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Happy path with an audit emitter attached — the handler must write an
// organization.created row keyed on the NEW org (not the caller's previous
// active org, since the route runs outside OrgContext).
func TestOrganizationsHandler_Create_EmitsAudit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	orgID := uuid.New()
	createdAt := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(orgID, createdAt, createdAt))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	// Audit row fires post-commit via the emitter. AuditLogStore.Create binds
	// 13 named args which pgxmock sees as 13 positional args.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewOrganizationsHandler(mock)
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))

	req := authedRequestAs(t, userID, `{"name":"Acme"}`)
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationsHandler_Create_OrgInsertFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	handler := NewOrganizationsHandler(mock)
	req := authedRequest(t, `{"name":"Acme"}`)
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "ORG_CREATE_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationsHandler_Create_MembershipInsertFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	createdAt := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(orgID, createdAt, createdAt))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	handler := NewOrganizationsHandler(mock)
	req := authedRequest(t, `{"name":"Acme"}`)
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "ORG_CREATE_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// authedRequest builds a POST request with a fresh authenticated user on the
// context. Use authedRequestAs when the test needs a known user ID.
func authedRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return authedRequestAs(t, uuid.New(), body)
}

func authedRequestAs(t *testing.T, userID uuid.UUID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", bytes.NewBufferString(body))
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "u@example.com"})
	return req.WithContext(ctx)
}
