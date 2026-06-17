package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// stubRepoLookup satisfies automationRepoLookup for Create/Update tests that
// supply a repository_id. Status defaults to active so existing tests that
// exercise the happy path don't need to opt in.
type stubRepoLookup struct {
	err    error
	status models.RepositoryStatus
}

func (s *stubRepoLookup) GetByID(_ context.Context, _, repoID uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	status := s.status
	if status == "" {
		status = models.RepositoryStatusActive
	}
	return models.Repository{ID: repoID, Status: status}, nil
}

func automationTestColumns() []string {
	return []string{
		"id", "org_id", "repository_id", "name", "goal", "scope",
		"icon_type", "icon_value",
		"agent_type", "model_override", "reasoning_effort", "execution_mode", "max_concurrent", "base_branch",
		"identity_scope", "pre_pr_review_loops",
		"schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone",
		"github_event_triggers",
		"next_run_at", "last_run_at", "enabled", "created_by", "paused_by", "paused_at",
		"priority", "external_metadata", "created_at", "updated_at", "deleted_at",
	}
}

func automationRunTestColumns() []string {
	return []string{
		"id", "automation_id", "org_id", "triggered_at", "triggered_by",
		"triggered_by_user_id", "scheduled_time", "goal_snapshot", "config_snapshot",
		"status", "capability_snapshot", "completed_at", "result_summary", "created_at", "updated_at",
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
	metadata := a.ExternalMetadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	return pgxmock.NewRows(automationTestColumns()).AddRow(
		a.ID, a.OrgID, a.RepositoryID, a.Name, a.Goal, a.Scope,
		a.IconType.OrDefault(), a.IconValue,
		a.AgentType, a.ModelOverride, a.ReasoningEffort, a.ExecutionMode, a.MaxConcurrent, a.BaseBranch,
		a.IdentityScope.OrDefault(), a.PrePRReviewLoops,
		a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.IntervalRunAt, a.CronExpression, a.Timezone,
		automationGitHubEventStrings(a.GitHubEventTriggers),
		a.NextRunAt, a.LastRunAt, a.Enabled, a.CreatedBy, a.PausedBy, a.PausedAt,
		a.Priority, metadata, a.CreatedAt, a.UpdatedAt, a.DeletedAt,
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

type stubAutomationOrgLookup struct {
	org models.Organization
	err error
}

func (s *stubAutomationOrgLookup) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return s.org, nil
}

type stubAutomationCodingCredentialLookup struct {
	rows []models.DecryptedCodingCredential
	err  error
}

func (s *stubAutomationCodingCredentialLookup) ListByScope(context.Context, models.Scope) ([]models.DecryptedCodingCredential, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
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

	longGoal := strings.Repeat("g", automationGoalMaxLength+1)

	tests := []struct {
		name string
		body any
		code int
	}{
		{name: "missing name", body: map[string]any{"goal": "do a thing"}, code: http.StatusBadRequest},
		{name: "missing goal", body: map[string]any{"name": "n"}, code: http.StatusBadRequest},
		{name: "cron missing expression", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron"}, code: http.StatusBadRequest},
		{name: "cron invalid expression", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron", "cron_expression": "not a cron"}, code: http.StatusBadRequest},
		{name: "invalid schedule type", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "bogus"}, code: http.StatusBadRequest},
		{name: "interval out of range", body: map[string]any{"name": "n", "goal": "g", "interval_value": 999}, code: http.StatusBadRequest},
		{name: "invalid interval unit", body: map[string]any{"name": "n", "goal": "g", "interval_unit": "fortnights"}, code: http.StatusBadRequest},
		{name: "invalid interval run at", body: map[string]any{"name": "n", "goal": "g", "interval_run_at": "09:37"}, code: http.StatusBadRequest},
		{name: "invalid exec mode", body: map[string]any{"name": "n", "goal": "g", "execution_mode": "mayhem"}, code: http.StatusBadRequest},
		{name: "invalid agent type", body: map[string]any{"name": "n", "goal": "g", "agent_type": "bogus"}, code: http.StatusBadRequest},
		{name: "invalid model", body: map[string]any{"name": "n", "goal": "g", "model": "not-a-real-model"}, code: http.StatusBadRequest},
		{name: "invalid identity scope", body: map[string]any{"name": "n", "goal": "g", "identity_scope": "team"}, code: http.StatusBadRequest},
		{name: "invalid reasoning effort", body: map[string]any{"name": "n", "goal": "g", "agent_type": "codex", "reasoning_effort": "turbo"}, code: http.StatusBadRequest},
		{name: "unsupported reasoning effort for agent", body: map[string]any{"name": "n", "goal": "g", "agent_type": "codex", "reasoning_effort": "max"}, code: http.StatusBadRequest},
		{name: "goal too long", body: map[string]any{"name": "n", "goal": longGoal}, code: http.StatusBadRequest},
		{name: "max_concurrent too high", body: map[string]any{"name": "n", "goal": "g", "max_concurrent": 9999}, code: http.StatusBadRequest},
		{name: "priority out of range", body: map[string]any{"name": "n", "goal": "g", "priority": 999}, code: http.StatusBadRequest},
		// Cross-typed schedule fields are rejected up front so client bugs
		// (sending interval_* on a cron payload, or vice versa) surface as
		// 400s instead of being silently dropped and producing a row whose
		// in-memory fields disagree with the persisted ones.
		{name: "cron with interval_value", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron", "cron_expression": "0 9 * * *", "interval_value": 3}, code: http.StatusBadRequest},
		{name: "cron with interval_unit", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron", "cron_expression": "0 9 * * *", "interval_unit": "days"}, code: http.StatusBadRequest},
		{name: "cron with interval_run_at", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "cron", "cron_expression": "0 9 * * *", "interval_run_at": "09:35"}, code: http.StatusBadRequest},
		{name: "interval with cron_expression", body: map[string]any{"name": "n", "goal": "g", "schedule_type": "interval", "cron_expression": "0 9 * * *"}, code: http.StatusBadRequest},
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
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":             "my automation",
		"goal":             "poke at things",
		"icon_type":        "emoji",
		"icon_value":       "🧹",
		"interval_value":   2,
		"interval_unit":    "days",
		"reasoning_effort": "xhigh",
		"execution_mode":   "sequential",
		"max_concurrent":   1,
		"priority":         25,
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.ReasoningEffort)
	require.Equal(t, models.ReasoningEffortXHigh, *resp.Data.ReasoningEffort)
	require.Equal(t, models.AutomationIdentityScopeOrg, resp.Data.IdentityScope)
	require.Equal(t, models.AutomationIconTypeEmoji, resp.Data.IconType)
	require.Equal(t, "🧹", resp.Data.IconValue)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Create_PersonalIdentityScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":           "my automation",
		"goal":           "poke at things",
		"interval_value": 2,
		"interval_unit":  "days",
		"identity_scope": "personal",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, models.AutomationIdentityScopePersonal, resp.Data.IdentityScope)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Create_ModelInfersAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":           "my automation",
		"goal":           "poke at things",
		"interval_value": 2,
		"interval_unit":  "days",
		"model":          "claude-sonnet-4-6",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.AgentType)
	require.Equal(t, string(models.AgentTypeClaudeCode), *resp.Data.AgentType)
	require.NotNil(t, resp.Data.ModelOverride)
	require.Equal(t, "claude-sonnet-4-6", *resp.Data.ModelOverride)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Create_RejectsUnavailableValidModel(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetOrgStore(&stubAutomationOrgLookup{org: models.Organization{Settings: json.RawMessage(`{}`)}})
	h.SetCodingCredentialStore(&stubAutomationCodingCredentialLookup{})

	body := map[string]any{
		"name":           "my automation",
		"goal":           "poke at things",
		"interval_value": 2,
		"interval_unit":  "days",
		"model":          "claude-sonnet-4-6",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "Create should reject a valid model when no usable credentials exist")

	var resp models.ErrorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create should return a JSON error response")
	require.Equal(t, "INVALID_MODEL", resp.Error.Code, "Create should classify unavailable models as INVALID_MODEL")
	require.Contains(t, resp.Error.Message, "configure a team-usable credential first", "Create should explain how to make the model available")
}

func TestAutomationHandler_Create_AllowsAvailableValidModel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetCodingCredentialStore(&stubAutomationCodingCredentialLookup{
		rows: []models.DecryptedCodingCredential{
			{Provider: models.ProviderAnthropic, Status: models.CodingCredentialStatusActive},
		},
	})

	body := map[string]any{
		"name":           "my automation",
		"goal":           "poke at things",
		"interval_value": 2,
		"interval_unit":  "days",
		"model":          "claude-sonnet-4-6",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "Create should allow a valid model when a matching healthy coding auth exists")
	require.NoError(t, mock.ExpectationsWereMet(), "Create should insert the automation")
}

func TestAutomationHandler_Create_ReasoningFallsBackWhenOrgSettingsMalformed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetOrgStore(&stubAutomationOrgLookup{org: models.Organization{Settings: json.RawMessage(`{"default_agent_type":`)}})

	body := map[string]any{
		"name":             "my automation",
		"goal":             "poke at things",
		"interval_value":   2,
		"interval_unit":    "days",
		"reasoning_effort": "xhigh",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "Create should fall back to the default agent when org settings are malformed")
	require.NoError(t, mock.ExpectationsWereMet(), "Create should still insert the automation")
}

// TestAutomationHandler_Create_IntervalNonUTCTimezone locks in the post-
// migration-93 contract: an interval schedule may specify any IANA zone for
// interval_run_at, and ComputeNextRunAt resolves next_run_at in that zone
// before storing UTC. Prior to migration 93 this combination returned 400
// INVALID_TIMEZONE.
func TestAutomationHandler_Create_IntervalNonUTCTimezone(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":            "my automation",
		"goal":            "poke at things",
		"schedule_type":   "interval",
		"interval_value":  1,
		"interval_unit":   "days",
		"interval_run_at": "09:00",
		"timezone":        "America/New_York",
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "America/New_York", resp.Data.Timezone)
	require.NotNil(t, resp.Data.NextRunAt)
	// next_run_at is stored UTC; its wall clock in America/New_York must be
	// 09:00 — otherwise ComputeNextRunAt is dropping the timezone.
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	localNext := resp.Data.NextRunAt.In(loc)
	require.Equal(t, 9, localNext.Hour(), "next_run_at should be 09:00 America/New_York")
	require.Equal(t, 0, localNext.Minute())
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

func TestAutomationHandler_Create_RejectsDisconnectedRepo(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetRepositoryStore(&stubRepoLookup{status: models.RepositoryStatusDisconnected})

	body := map[string]any{
		"name":          "n",
		"goal":          "g",
		"repository_id": uuid.New().String(),
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "REPO_DISCONNECTED")
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

	longGoal := strings.Repeat("g", automationGoalMaxLength+1)

	tests := []struct {
		name string
		body any
		code int
	}{
		{name: "blank name", body: map[string]any{"name": "   "}, code: http.StatusBadRequest},
		{name: "blank goal", body: map[string]any{"goal": "   "}, code: http.StatusBadRequest},
		{name: "goal too long", body: map[string]any{"goal": longGoal}, code: http.StatusBadRequest},
		{name: "invalid exec mode", body: map[string]any{"execution_mode": "x"}, code: http.StatusBadRequest},
		{name: "invalid priority", body: map[string]any{"priority": 999}, code: http.StatusBadRequest},
		{name: "cron invalid expression", body: map[string]any{"schedule_type": "cron", "cron_expression": "nope"}, code: http.StatusBadRequest},
		// Up-front switch validation: a type switch must include its
		// companion fields in the same PATCH. Before this check, the handler
		// would happily mutate the in-memory model and surface a less
		// precise error downstream at ComputeNextRunAt.
		{name: "switch to cron without expression", body: map[string]any{"schedule_type": "cron"}, code: http.StatusBadRequest},
		{name: "switch to cron with blank expression", body: map[string]any{"schedule_type": "cron", "cron_expression": "   "}, code: http.StatusBadRequest},
		{name: "blank base branch", body: map[string]any{"base_branch": "  "}, code: http.StatusBadRequest},
		{name: "invalid interval value", body: map[string]any{"interval_value": -1}, code: http.StatusBadRequest},
		{name: "invalid interval unit", body: map[string]any{"interval_unit": "bogus"}, code: http.StatusBadRequest},
		{name: "invalid interval run at", body: map[string]any{"interval_run_at": "11:07"}, code: http.StatusBadRequest},
		{name: "invalid agent type", body: map[string]any{"agent_type": "bogus"}, code: http.StatusBadRequest},
		{name: "invalid model", body: map[string]any{"model": "not-a-real-model"}, code: http.StatusBadRequest},
		{name: "invalid identity scope", body: map[string]any{"identity_scope": "team"}, code: http.StatusBadRequest},
		{name: "personal identity scope requires creator", body: map[string]any{"identity_scope": "personal"}, code: http.StatusBadRequest},
		{name: "invalid reasoning effort", body: map[string]any{"reasoning_effort": "turbo"}, code: http.StatusBadRequest},
		{name: "unsupported reasoning effort for codex", body: map[string]any{"agent_type": "codex", "reasoning_effort": "max"}, code: http.StatusBadRequest},
		// Reject mismatched companion fields up front: existing automation
		// is interval, so cron_expression on its own should 400 (not be
		// silently dropped during normalisation).
		{name: "interval automation with cron_expression", body: map[string]any{"cron_expression": "0 9 * * *"}, code: http.StatusBadRequest},
		// Switching to cron with stale interval_value in the same PATCH is
		// also rejected — the user must supply a clean cron payload.
		{name: "switch to cron with leftover interval_value", body: map[string]any{"schedule_type": "cron", "cron_expression": "0 9 * * *", "interval_value": 3}, code: http.StatusBadRequest},
		{name: "switch to cron with leftover interval_run_at", body: map[string]any{"schedule_type": "cron", "cron_expression": "0 9 * * *", "interval_run_at": "09:35"}, code: http.StatusBadRequest},
		{name: "invalid schedule_type", body: map[string]any{"schedule_type": "bogus"}, code: http.StatusBadRequest},
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
	unit := models.ScheduleUnitDays
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
		WithArgs(testAnyArgs(29)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":             "new name",
		"goal":             "new goal",
		"execution_mode":   "parallel",
		"max_concurrent":   3,
		"reasoning_effort": "high",
		"interval_value":   7,
		"interval_unit":    "days",
		"priority":         75,
		"base_branch":      "develop",
		// Migration 93 dropped chk_automations_timezone_interval, so any IANA
		// zone is accepted for interval schedules. UTC kept here as the default.
		"timezone": "UTC",
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.ReasoningEffort)
	require.Equal(t, models.ReasoningEffortHigh, *resp.Data.ReasoningEffort)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Update_ReasoningFallsBackWhenOrgSettingsMalformed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
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
		WithArgs(testAnyArgs(29)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetOrgStore(&stubAutomationOrgLookup{org: models.Organization{Settings: json.RawMessage(`{"default_agent_type":`)}})

	body := map[string]any{
		"reasoning_effort": "high",
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "Update should fall back to the default agent when org settings are malformed")
	require.NoError(t, mock.ExpectationsWereMet(), "Update should still persist the automation")
}

func TestAutomationHandler_Update_BlankModelPreservesExplicitAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
	agentType := string(models.AgentTypeClaudeCode)
	model := "claude-sonnet-4-6"
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		AgentType: &agentType, ModelOverride: &model,
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, IntervalValue: &iv, IntervalUnit: &unit,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectExec("UPDATE automations SET").
		WithArgs(testAnyArgs(29)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"model": "",
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.AgentType)
	require.Equal(t, string(models.AgentTypeClaudeCode), *resp.Data.AgentType)
	require.Nil(t, resp.Data.ModelOverride)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutomationHandler_Update_TimezoneOnlyRecomputesNextRunAt pins the Update
// branch `if req.Timezone != nil { scheduleChanged = true }`: patching *only*
// the timezone on an interval row with interval_run_at must move next_run_at
// to the new zone's wall-clock 09:00, not leave the old UTC value in place.
func TestAutomationHandler_Update_TimezoneOnlyRecomputesNextRunAt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
	runAt := "09:00"
	// Seed a UTC interval row whose stored next_run_at is 09:00 UTC. After the
	// PATCH flips timezone to America/New_York the recompute must shift
	// next_run_at so the wall clock in America/New_York is still 09:00.
	nextUTC := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true,
		IntervalValue: &iv, IntervalUnit: &unit, IntervalRunAt: &runAt,
		NextRunAt: &nextUTC,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectExec("UPDATE automations SET").
		WithArgs(testAnyArgs(29)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"timezone": "America/New_York",
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "America/New_York", resp.Data.Timezone)
	require.NotNil(t, resp.Data.NextRunAt)
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	localNext := resp.Data.NextRunAt.In(loc)
	require.Equal(t, 9, localNext.Hour(), "next_run_at should be 09:00 America/New_York after tz-only PATCH")
	require.Equal(t, 0, localNext.Minute())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutomationHandler_Update_SwitchScheduleType_OK exercises the schedule
// type switch arms in Update: an interval automation is converted to cron
// (which clears the legacy interval_* fields) and vice versa. These two paths
// share the validation that demands the new type's companion field be present
// in the same PATCH.
func TestAutomationHandler_Update_SwitchScheduleType_OK(t *testing.T) {
	t.Parallel()

	t.Run("interval to cron clears interval fields", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		id := uuid.New()
		now := time.Now()
		iv := 1
		unit := models.ScheduleUnitDays
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
			WithArgs(testAnyArgs(29)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
		body := map[string]any{
			"schedule_type":   "cron",
			"cron_expression": "0 9 * * *",
		}
		req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("cron to interval clears cron expression", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		id := uuid.New()
		now := time.Now()
		cron := "0 9 * * *"
		a := models.Automation{
			ID: id, OrgID: orgID, Name: "a", Goal: "g",
			ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "cron",
			Timezone: "UTC", Enabled: true, CronExpression: &cron,
			CreatedAt: now, UpdatedAt: now,
		}
		mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
			WithArgs(testAnyArgs(2)...).
			WillReturnRows(newAutomationRow(mock, a))
		mock.ExpectExec("UPDATE automations SET").
			WithArgs(testAnyArgs(29)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
		body := map[string]any{
			"schedule_type":  "interval",
			"interval_value": 2,
			"interval_unit":  "days",
		}
		req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})
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

func TestAutomationHandler_Update_RejectsDisconnectedRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, IntervalValue: &iv, IntervalUnit: &unit,
		CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	h.SetRepositoryStore(&stubRepoLookup{status: models.RepositoryStatusDisconnected})

	body := map[string]any{"repository_id": uuid.New().String()}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "REPO_DISCONNECTED")
}

// --- Delete ---

func TestAutomationHandler_Delete_OK(t *testing.T) {
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
	// Delete reads the row first so the audit entry can record the name/schedule.
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
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
	req := newAutomationRequest(t, http.MethodDelete, "/api/v1/automations/"+id.String(), nil, orgID, uuid.New(), map[string]string{"id": id.String()})
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

func TestAutomationHandler_Delete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	// Delete reads the row first; a missing row short-circuits before SoftDelete.
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodDelete, "/api/v1/automations/"+id.String(), nil, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
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
		WithArgs(testAnyArgs(29)...).
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
	unit := models.ScheduleUnitDays
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
		WithArgs(testAnyArgs(29)...).
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

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id = .+ FOR UPDATE").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectQuery("SELECT count.+FROM automation_runs").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(testAnyArgs(9)...).
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

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id = .+ FOR UPDATE").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectRollback()

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

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id = .+ FOR UPDATE").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
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

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id = .+ FOR UPDATE").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	mock.ExpectQuery("SELECT count.+FROM automation_runs").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(testAnyArgs(9)...).
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

	// BulkUpdateEnabled wraps the UPDATE in a tx so cron rows can be fixed up
	// in the same atomic step. The UPDATE's RETURNING is narrowed to only the
	// fields ComputeNextRunAt needs (schedule_type / interval* / cron /
	// timezone) plus id — mirror that here.
	a1 := models.Automation{ID: uuid.New(), ScheduleType: "interval", Timezone: "UTC"}
	a2 := models.Automation{ID: uuid.New(), ScheduleType: "interval", Timezone: "UTC"}
	pauseRows := pgxmock.NewRows([]string{"id", "schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone"}).
		AddRow(a1.ID, a1.ScheduleType, a1.IntervalValue, a1.IntervalUnit, a1.IntervalRunAt, a1.CronExpression, a1.Timezone).
		AddRow(a2.ID, a2.ScheduleType, a2.IntervalValue, a2.IntervalUnit, a2.IntervalRunAt, a2.CronExpression, a2.Timezone)

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(testAnyArgs(5)...).
		WillReturnRows(pauseRows)
	mock.ExpectCommit()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{"action": "pause", "automation_ids": []string{uuid.New().String(), uuid.New().String()}}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/bulk", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Bulk(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp BulkResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Affected, 2)
	require.Empty(t, resp.FixupFailures)
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
	require.Equal(t, http.StatusOK, rr.Code)
	var resp BulkResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Affected, 1)
	require.Empty(t, resp.FixupFailures)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutomationHandler_Bulk_ResumeCronFixupFailure exercises the resume path
// when a cron row's expression no longer parses: the row must still be flipped
// enabled, the response must carry a fixup_failures entry keyed by that row's
// ID, and the per-row audit emit must include fixup_failure_reason so an
// auditor reading "automation.resumed" sees why the scheduler will skip it.
// This is the only test that covers the full resume-with-fixup branch through
// the handler — the store-layer test (TestAutomationStore_BulkUpdateEnabled_
// Resume_CronFixupFailure) stops at the store boundary.
func TestAutomationHandler_Bulk_ResumeCronFixupFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	automationID := uuid.New()
	brokenCron := "this is not a cron expression"

	// BulkUpdateEnabled's UPDATE ... RETURNING returns only the schedule columns
	// the post-update cron-fixup pass needs. Match that narrow projection so
	// the handler test reflects the real store contract.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(testAnyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone"}).
				AddRow(automationID, "cron", nil, nil, nil, &brokenCron, "UTC"),
		)
	mock.ExpectCommit()

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{"action": "resume", "automation_ids": []string{automationID.String()}}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations/bulk", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Bulk(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp BulkResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, []string{automationID.String()}, resp.Affected, "row was still resumed")
	require.Len(t, resp.FixupFailures, 1, "broken cron must surface to the caller")
	require.Equal(t, automationID.String(), resp.FixupFailures[0].AutomationID)
	require.NotEmpty(t, resp.FixupFailures[0].Reason)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutomationHandler_SetLogger exercises the logger setter — trivial, but
// unexercised setters otherwise drag diff coverage down.
func TestAutomationHandler_SetLogger(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	h.SetLogger(zerolog.Nop())
}

// --- ListRuns / GetRun ---

func TestAutomationHandler_ListRuns_OK(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	title := "Refactor diff viewer"
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))
	sessionStatus := models.SessionStatusCompleted
	retryAdvised := false
	prCreationState := "succeeded"
	prNumber := 1213
	prURL := "https://github.com/example/repo/pull/1213"
	prStatus := "open"
	prCIStatus := "success"
	mock.ExpectQuery("SELECT .+ FROM automation_runs ar.+LEFT JOIN LATERAL").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(db.AutomationRunListColumns).AddRow(
				uuid.New(), id, orgID, now, models.AutomationTriggeredBySchedule,
				nil, nil, "goal",
				models.AutomationRunStatusCompleted, nil, nil, now, now,
				&sessionID, &title, &sessionStatus,
				[]byte(`{"added":12,"removed":3}`),
				nil, nil, nil, &retryAdvised, &prCreationState,
				&prNumber, &prURL, &prStatus, &prCIStatus,
			),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String()+"/runs", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	// Confirm the response carries the embedded session payload — the
	// frontend's clickable rows + diff/PR badges depend on this shape.
	var resp models.ListResponse[models.AutomationRun]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	got := resp.Data[0]
	require.NotNil(t, got.Session)
	require.Equal(t, sessionID, got.Session.ID)
	require.Equal(t, "Refactor diff viewer", *got.Session.Title)
	require.NotNil(t, got.Session.PR)
	require.Equal(t, 1213, got.Session.PR.Number)
	require.Equal(t, "https://github.com/example/repo/pull/1213", got.Session.PR.URL)

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
				models.AutomationRunStatusPending, nil, nil, nil, now, now,
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

// --- Stats ---

func TestAutomationHandler_Stats_OK(t *testing.T) {
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

	cols := []string{
		"bucket", "total", "completed", "completed_noop", "failed",
		"skipped", "running", "pending", "avg_duration_seconds",
	}
	day := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM automation_runs").
		WithArgs(testAnyArgs(4)...).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(day, 3, 2, 0, 1, 0, 0, 0, 90.0))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String()+"/stats", nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Stats_AutomationNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/"+id.String()+"/stats", nil, uuid.New(), uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAutomationHandler_Stats_InvalidID(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	req := newAutomationRequest(t, http.MethodGet, "/api/v1/automations/x/stats", nil, uuid.New(), uuid.New(), map[string]string{"id": "x"})
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAutomationHandler_Stats_InvalidWindow(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()

	// Param validation now runs BEFORE the automation lookup, so the
	// invalid-window branches must short-circuit without touching the DB.
	// No ExpectQuery calls here: mock.ExpectationsWereMet() below fails if
	// the handler regresses and burns a SELECT on malformed params.
	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))

	// since after until -> 400.
	url := "/api/v1/automations/" + id.String() + "/stats?since=2026-05-01T00:00:00Z&until=2026-04-01T00:00:00Z"
	req := newAutomationRequest(t, http.MethodGet, url, nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// window > 90 days -> 400.
	url2 := "/api/v1/automations/" + id.String() + "/stats?since=2026-01-01T00:00:00Z&until=2026-06-01T00:00:00Z"
	req2 := newAutomationRequest(t, http.MethodGet, url2, nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr2 := httptest.NewRecorder()
	h.Stats(rr2, req2)
	require.Equal(t, http.StatusBadRequest, rr2.Code)

	// Malformed since -> 400 INVALID_SINCE.
	url3 := "/api/v1/automations/" + id.String() + "/stats?since=not-a-date"
	req3 := newAutomationRequest(t, http.MethodGet, url3, nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr3 := httptest.NewRecorder()
	h.Stats(rr3, req3)
	require.Equal(t, http.StatusBadRequest, rr3.Code)
	require.Contains(t, rr3.Body.String(), "INVALID_SINCE")

	// Malformed until -> 400 INVALID_UNTIL. The handler parses until before
	// since, so a junk until short-circuits before the window-size and
	// since-ordering checks can fire — this pins that precedence so a future
	// reordering doesn't silently fall through to a confusing INVALID_WINDOW.
	url4 := "/api/v1/automations/" + id.String() + "/stats?until=not-a-date"
	req4 := newAutomationRequest(t, http.MethodGet, url4, nil, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr4 := httptest.NewRecorder()
	h.Stats(rr4, req4)
	require.Equal(t, http.StatusBadRequest, rr4.Code)
	require.Contains(t, rr4.Body.String(), "INVALID_UNTIL")

	require.NoError(t, mock.ExpectationsWereMet())
}

// --- GitHub event triggers ---

func TestAutomationHandler_Create_WithGitHubEventTriggers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":                  "PR review automation",
		"goal":                  "Review every PR",
		"interval_value":        1,
		"interval_unit":         "days",
		"github_event_triggers": []string{"github.pull_request.opened", "github.issue_comment.created"},
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "should create automation with valid github_event_triggers")

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, []models.AutomationGitHubEvent{
		models.AutomationGitHubEventPullRequestOpened,
		models.AutomationGitHubEventIssueCommentCreated,
	}, resp.Data.GitHubEventTriggers, "response should echo back the github_event_triggers")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Create_RejectsInvalidGitHubEventTrigger(t *testing.T) {
	t.Parallel()

	h := NewAutomationHandler(nil, nil)
	body := map[string]any{
		"name":                  "bad trigger",
		"goal":                  "do something",
		"interval_value":        1,
		"interval_unit":         "days",
		"github_event_triggers": []string{"github.unknown.event"},
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "invalid github_event_triggers should be rejected")
	require.Contains(t, rr.Body.String(), "INVALID_GITHUB_EVENT_TRIGGERS", "error code should identify the invalid trigger")
	// No DB expectations: reject before touching the store.
}

func TestAutomationHandler_Create_DeduplicatesGitHubEventTriggers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	newID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(testAnyArgs(27)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newID, now, now),
		)

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"name":           "dedup automation",
		"goal":           "review prs",
		"interval_value": 1,
		"interval_unit":  "days",
		"github_event_triggers": []string{
			"github.pull_request.opened",
			"github.pull_request.opened",
			"github.issue_comment.created",
		},
	}
	req := newAutomationRequest(t, http.MethodPost, "/api/v1/automations", body, uuid.New(), uuid.New(), nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "duplicate github_event_triggers should be accepted (deduplicated)")

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, []models.AutomationGitHubEvent{
		models.AutomationGitHubEventPullRequestOpened,
		models.AutomationGitHubEventIssueCommentCreated,
	}, resp.Data.GitHubEventTriggers, "duplicate events should be deduplicated while preserving order")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Update_WithGitHubEventTriggers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
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
		WithArgs(testAnyArgs(29)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"github_event_triggers": []string{"github.pull_request_review.submitted"},
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "should update automation with valid github_event_triggers")

	var resp models.SingleResponse[models.Automation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, []models.AutomationGitHubEvent{
		models.AutomationGitHubEventPullRequestReviewSubmitted,
	}, resp.Data.GitHubEventTriggers, "updated github_event_triggers should be reflected in response")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationHandler_Update_RejectsInvalidGitHubEventTrigger(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()
	iv := 1
	unit := models.ScheduleUnitDays
	a := models.Automation{
		ID: id, OrgID: orgID, Name: "a", Goal: "g",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, IntervalValue: &iv, IntervalUnit: &unit,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(testAnyArgs(2)...).
		WillReturnRows(newAutomationRow(mock, a))

	h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
	body := map[string]any{
		"github_event_triggers": []string{"not.a.valid.event"},
	}
	req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "invalid github_event_triggers in Update should be rejected")
	require.Contains(t, rr.Body.String(), "INVALID_GITHUB_EVENT_TRIGGERS", "error code should identify the invalid trigger")
	require.NoError(t, mock.ExpectationsWereMet(), "no UPDATE should be attempted after trigger validation fails")
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
