package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// stubRepoLookup satisfies automationRepoLookup for Create/Update tests that
// supply a repository_id.
type stubRepoLookup struct {
	err error
}

func (s *stubRepoLookup) GetByID(_ context.Context, _, repoID uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return models.Repository{ID: repoID}, nil
}

func automationTestColumns() []string {
	return []string{
		"id", "org_id", "repository_id", "name", "goal", "scope",
		"agent_type", "model_override", "execution_mode", "max_concurrent", "base_branch",
		"schedule_type", "interval_value", "interval_unit", "cron_expression", "timezone",
		"next_run_at", "last_run_at", "enabled", "created_by", "paused_by", "paused_at",
		"priority", "created_at", "updated_at", "deleted_at",
	}
}

func automationRunTestColumns() []string {
	return []string{
		"id", "automation_id", "org_id", "triggered_at", "triggered_by",
		"triggered_by_user_id", "scheduled_time", "goal_snapshot", "config_snapshot",
		"status", "completed_at", "result_summary", "created_at", "updated_at",
	}
}

func testAnyArgs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}

func newAutomationRow(mock pgxmock.PgxPoolIface, a models.Automation) *pgxmock.Rows {
	return pgxmock.NewRows(automationTestColumns()).AddRow(
		a.ID, a.OrgID, a.RepositoryID, a.Name, a.Goal, a.Scope,
		a.AgentType, a.ModelOverride, a.ExecutionMode, a.MaxConcurrent, a.BaseBranch,
		a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.CronExpression, a.Timezone,
		a.NextRunAt, a.LastRunAt, a.Enabled, a.CreatedBy, a.PausedBy, a.PausedAt,
		a.Priority, a.CreatedAt, a.UpdatedAt, a.DeletedAt,
	)
}

func newAutomationRequest(t *testing.T, method, path string, body any, orgID uuid.UUID, userID uuid.UUID, urlParams map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	if len(urlParams) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range urlParams {
			rctx.URLParams.Add(k, v)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

// --- List ---

func TestAutomationHandler_List_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: uuid.New(), OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE org_id").
		WithArgs(testAnyArgs(1)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations", nil, orgID, uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ListResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_List_StoreError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM automations WHERE org_id").
		WithArgs(testAnyArgs(1)...).
		WillReturnError(errors.New("boom"))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations", nil, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- Get ---

func TestAutomationHandler_Get_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String(), nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAutomationHandler_Get_InvalidID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/bad", nil, uuid.New(), uuid.New(), map[string]string{"id": "not-a-uuid"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Get_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String(), nil, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// --- Create ---

func TestAutomationHandler_Create_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body any
		code int
	}{
		{name: "missing name", body: map[string]any{"goal": "do a thing"}, code: http.StatusBadRequest},
		{name: "missing goal", body: map[string]any{"name": "n"}, code: http.StatusBadRequest},
		{name: "cron rejected", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron"}, code: http.StatusBadRequest},
		{name: "invalid schedule type", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "bogus"}, code: http.StatusBadRequest},
		{name: "interval out of range", body: map[string]any{"name": "n", "goal": "g", "interval_value": 999}, code: http.StatusBadRequest},
		{name: "invalid interval unit", body: map[string]any{"name": "n", "goal": "g", "interval_unit": "fortnights"}, code: http.StatusBadRequest},
		{name: "invalid exec mode", body: map[string]any{"name": "n", "goal": "g", "execution_mode": "mayhem"}, code: http.StatusBadRequest},
		{name: "max_concurrent too high", body: map[string]any{"name": "n", "goal": "g", "max_concurrent": 9999}, code: http.StatusBadRequest},
		{name: "priority out of range", body: map[string]any{"name": "n", "goal": "g", "priority": 999}, code: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewAutomationHandler(nil, nil)
			req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", tt.body, uuid.New(), uuid.New(), nil)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			require.Equal(t, tt.code, rr.Code)
		})
	}
}

func TestAutomationHandler_Create_BadJSON(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automations", bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Create_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(19)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":           "my automation",
		"goal":           "poke at things",
		"interval_value": 2,
		"interval_unit":  "days",
		"execution_mode": "sequential",
		"max_concurrent": 1,
		"priority":       25,
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Create_InvalidRepoID_BadUUID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetRepositoryStore(&stubRepoLookup{})

	body := map[string]any{
		"name":          "n",
		"goal":          "g",
		"repository_id": "not-a-uuid",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Create_RepoIDNotFoundInOrg(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetRepositoryStore(&stubRepoLookup{err: errors.New("not found")})

	body := map[string]any{
		"name":          "n",
		"goal":          "g",
		"repository_id": uuid.New().String(),
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Create_RepoIDFailsClosedWhenNoStore(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil) // No SetRepositoryStore

	body := map[string]any{
		"name":          "n",
		"goal":          "g",
		"repository_id": uuid.New().String(),
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// --- Update ---

func TestAutomationHandler_Update_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body any
		code int
	}{
		{name: "blank name", body: map[string]any{"name": "   "}, code: http.StatusBadRequest},
		{name: "blank goal", body: map[string]any{"goal": "   "}, code: http.StatusBadRequest},
		{name: "invalid exec mode", body: map[string]any{"execution_mode": "x"}, code: http.StatusBadRequest},
		{name: "invalid priority", body: map[string]any{"priority": 999}, code: http.StatusBadRequest},
		{name: "cron rejected", body: map[string]any{"schedule_type": "cron"}, code: http.StatusBadRequest},
		{name: "blank base branch", body: map[string]any{"base_branch": "  "}, code: http.StatusBadRequest},
		{name: "invalid interval value", body: map[string]any{"interval_value": -1}, code: http.StatusBadRequest},
		{name: "invalid interval unit", body: map[string]any{"interval_unit": "bogus"}, code: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			id := uuid.New()
			now := time.Now()
			a := models.Automation{
				ID: id, OrgID: orgID, Name: "a", Goal: "g",
				ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
				Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now,
			}
			mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
				WithArgs(testAnyArgs(2)...).
				WillReturnRows(newAutomationRow(mock, a))

			h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
			req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), tt.body, orgID, uuid.New(), map[string]string{"id": id.String()})
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			require.Equal(t, tt.code, rr.Code)
		})
	}
}

func TestAutomationHandler_Update_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := "days"
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, IntervalValue: &iv, IntervalUnit: &unit,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectExec("UPDATE automations SET").
		WithArgs(testAnyArgs(21)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":           "new name",
		"goal":           "new goal",
		"execution_mode": "parallel",
		"max_concurrent": 3,
		"interval_value": 7,
		"interval_unit":  "days",
		"priority":       75,
		"base_branch":    "develop",
		// Interval schedules must be UTC — matches the chk_automations_timezone_interval DB constraint.
		"timezone": "UTC",
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Update_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{"name": "x"}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAutomationHandler_Update_InvalidID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/bogus", map[string]any{"name": "x"}, uuid.New(), uuid.New(), map[string]string{"id": "bogus"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// --- Delete ---

func TestAutomationHandler_Delete_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	// SoftDelete wraps the automation update and pending-run cancel in one tx.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(testAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE automation_runs SET status = 'skipped'`).
		WithArgs(testAnyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodDelete, "/api/v1/automations/"+id.String(), nil, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Delete_InvalidID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := newAutomationRequest(t, http.MethodDelete, "/api/v1/automations/x", nil, uuid.New(), uuid.New(), map[string]string{"id": "x"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// --- Pause / Resume ---

func TestAutomationHandler_Pause_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectExec("UPDATE automations SET").
		WithArgs(testAnyArgs(21)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/pause", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Pause(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAutomationHandler_Pause_AlreadyPaused(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Enabled: false,
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/pause", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Pause(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Resume_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := "days"
	a := models.Automation{
		ID: id, OrgID: orgID, Enabled: false,
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", IntervalValue: &iv, IntervalUnit: &unit,
		CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectExec("UPDATE automations SET").
		WithArgs(testAnyArgs(21)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/resume", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAutomationHandler_Resume_AlreadyEnabled(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Enabled: true,
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/resume", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// --- RunNow ---

func TestAutomationHandler_RunNow_NotConfigured(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil) // no job store / pool
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/x/run", nil, uuid.New(), uuid.New(), map[string]string{"id": uuid.New().String()})
	rr := httptest.NewRecorder()
	h.RunNow(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestAutomationHandler_RunNow_HappyPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	runID := uuid.New()
	jobID := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT count.+FROM automation_runs").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(testAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "triggered_at", "created_at", "updated_at"}).
				AddRow(runID, now, now, now),
		)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(testAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()
	mock.ExpectRollback() // deferred rollback (no-op after commit)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetJobStore(db.NewJobStore(mock))
	h.SetPool(mock)

	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/run", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.RunNow(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_RunNow_Paused(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: false,
		CreatedAt: now, UpdatedAt: now,
	}

	// Only the GetByID fires — no Begin/Query/Commit since the handler
	// short-circuits on !Enabled before opening a tx.
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetJobStore(db.NewJobStore(mock))
	h.SetPool(mock)

	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/run", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.RunNow(rr, req)
	require.Equal(t, http.StatusConflict, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "AUTOMATION_PAUSED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_RunNow_Throttled(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	mock.ExpectBegin()
	// A run is already in flight — CountInFlightRuns returns MaxConcurrent.
	mock.ExpectQuery("SELECT count.+FROM automation_runs").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetJobStore(db.NewJobStore(mock))
	h.SetPool(mock)

	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/run", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.RunNow(rr, req)
	require.Equal(t, http.StatusConflict, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "DUPLICATE_RUN")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_RunNow_EnqueueFailureRollsBack(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	runID := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT count.+FROM automation_runs").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(testAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "triggered_at", "created_at", "updated_at"}).
				AddRow(runID, now, now, now),
		)
	// Enqueue fails — the run row we just inserted must roll back alongside it.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(testAnyArgs(6)...).
		WillReturnError(errors.New("enqueue failed"))
	mock.ExpectRollback()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetJobStore(db.NewJobStore(mock))
	h.SetPool(mock)

	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/"+id.String()+"/run", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.RunNow(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "ENQUEUE_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- Bulk ---

func TestAutomationHandler_Bulk_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body any
		code int
	}{
		{name: "empty ids", body: map[string]any{"action": "pause", "automation_ids": []string{}}, code: http.StatusBadRequest},
		{name: "invalid action", body: map[string]any{"action": "explode", "automation_ids": []string{uuid.New().String()}}, code: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := NewAutomationHandler(nil, nil)
			req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/bulk", tt.body, uuid.New(), uuid.New(), nil)
			rr := httptest.NewRecorder()
			h.Bulk(rr, req)
			require.Equal(t, tt.code, rr.Code)
		})
	}
}

func TestAutomationHandler_Bulk_BadJSON(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automations/bulk", bytes.NewBufferString("{nope"))
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.Bulk(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Bulk_PauseOK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(testAnyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()).AddRow(uuid.New()))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{"action": "pause", "automation_ids": []string{uuid.New().String(), uuid.New().String()}}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/bulk", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Bulk(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Bulk_DeleteOK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// BulkSoftDelete runs inside a tx so affected automations' pending runs
	// are cancelled atomically alongside the soft delete.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET deleted_at").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec(`UPDATE automation_runs SET status = 'skipped'`).
		WithArgs(testAnyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{"action": "delete", "automation_ids": []string{uuid.New().String()}}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/bulk", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Bulk(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- ListRuns / GetRun ---

func TestAutomationHandler_ListRuns_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectQuery("SELECT .+ FROM automation_runs WHERE automation_id").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(automationRunTestColumns()).AddRow(
				uuid.New(), id, orgID, now, models.AutomationTriggeredBySchedule,
				nil, nil, "goal", []byte(`{}`),
				models.AutomationRunStatusCompleted, nil, nil, now, now,
			),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String()+"/runs", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_ListRuns_AutomationNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String()+"/runs", nil, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAutomationHandler_ListRuns_InvalidID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/x/runs", nil, uuid.New(), uuid.New(), map[string]string{"id": "x"})
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_GetRun_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	automationID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM automation_runs WHERE id =").
		WithArgs(testAnyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows(automationRunTestColumns()).AddRow(
				runID, automationID, orgID, now, models.AutomationTriggeredByManual,
				nil, nil, "goal", []byte(`{}`),
				models.AutomationRunStatusPending, nil, nil, now, now,
			),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+automationID.String()+"/runs/"+runID.String(), nil, orgID, uuid.New(), map[string]string{"id": automationID.String(), "rid": runID.String()})
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAutomationHandler_GetRun_InvalidIDs(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req1 := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/x/runs/y", nil, uuid.New(), uuid.New(), map[string]string{"id": "x", "rid": uuid.New().String()})
	rr1 := httptest.NewRecorder()
	h.GetRun(rr1, req1)
	require.Equal(t, http.StatusBadRequest, rr1.Code)

	req2 := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/x/runs/y", nil, uuid.New(), uuid.New(), map[string]string{"id": uuid.New().String(), "rid": "y"})
	rr2 := httptest.NewRecorder()
	h.GetRun(rr2, req2)
	require.Equal(t, http.StatusBadRequest, rr2.Code)
}

// --- Setters ---

func TestAutomationHandler_Setters(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetJobStore(nil)
	h.SetAuditEmitter(nil)
	h.SetRepositoryStore(&stubRepoLookup{})
	h.SetPool(nil)
	// No assertions: exercising the setters to bump coverage; they're trivial stores.
	require.NotNil(t, h)
}
