package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func automationColumnSlice() []string {
	return []string{
		"id", "org_id", "repository_id", "name", "goal", "scope",
		"agent_type", "model_override", "execution_mode", "max_concurrent", "base_branch",
		"schedule_type", "interval_value", "interval_unit", "cron_expression", "timezone",
		"next_run_at", "last_run_at", "enabled", "created_by", "paused_by", "paused_at",
		"priority", "created_at", "updated_at", "deleted_at",
	}
}

func addAutomationRow(rows *pgxmock.Rows, a models.Automation) *pgxmock.Rows {
	return rows.AddRow(
		a.ID, a.OrgID, a.RepositoryID, a.Name, a.Goal, a.Scope,
		a.AgentType, a.ModelOverride, a.ExecutionMode, a.MaxConcurrent, a.BaseBranch,
		a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.CronExpression, a.Timezone,
		a.NextRunAt, a.LastRunAt, a.Enabled, a.CreatedBy, a.PausedBy, a.PausedAt,
		a.Priority, a.CreatedAt, a.UpdatedAt, a.DeletedAt,
	)
}

func TestAutomationStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	newID := uuid.New()
	now := time.Now()

	a := &models.Automation{
		OrgID:         orgID,
		Name:          "Test Automation",
		Goal:          "Clean up stale branches",
		ExecutionMode: "sequential",
		MaxConcurrent: 1,
		BaseBranch:    "main",
		ScheduleType:  models.AutomationScheduleInterval,
		Timezone:      "UTC",
		Enabled:       true,
		Priority:      50,
	}

	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(anyArgs(19)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(newID, now, now),
		)

	require.NoError(t, store.Create(context.Background(), a))
	require.Equal(t, newID, a.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_GetByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	want := models.Automation{
		ID: id, OrgID: orgID, Name: "A", Goal: "G",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: models.AutomationScheduleInterval, Timezone: "UTC",
		Enabled: true, Priority: 50, CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(anyArgs(2)...).
		WillReturnRows(addAutomationRow(pgxmock.NewRows(automationColumnSlice()), want))

	got, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err)
	require.Equal(t, want.ID, got.ID)
	require.Equal(t, want.Name, got.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
		WithArgs(anyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_ListByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	now := time.Now()

	a1 := models.Automation{ID: uuid.New(), OrgID: orgID, Name: "a1", Goal: "g", ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval", Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}
	a2 := models.Automation{ID: uuid.New(), OrgID: orgID, Name: "a2", Goal: "g", ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval", Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}

	rows := addAutomationRow(addAutomationRow(pgxmock.NewRows(automationColumnSlice()), a1), a2)
	// Args: org_id + enabled + search (3)
	mock.ExpectQuery("SELECT .+ FROM automations WHERE org_id").
		WithArgs(anyArgs(3)...).
		WillReturnRows(rows)

	enabled := true
	got, err := store.ListByOrg(context.Background(), orgID, AutomationFilters{
		Enabled: &enabled,
		Limit:   25,
		Search:  "foo",
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_ListByOrg_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	// No filters: only org_id (1 arg).
	mock.ExpectQuery("SELECT .+ FROM automations WHERE org_id").
		WithArgs(anyArgs(1)...).
		WillReturnError(errors.New("boom"))

	_, err = store.ListByOrg(context.Background(), uuid.New(), AutomationFilters{})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	a := &models.Automation{
		ID: uuid.New(), OrgID: uuid.New(), Name: "updated",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, Priority: 50,
	}

	mock.ExpectExec("UPDATE automations SET").
		WithArgs(anyArgs(21)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, store.Update(context.Background(), a))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_SoftDelete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, store.SoftDelete(context.Background(), orgID, id))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_SoftDelete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)

	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.SoftDelete(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_EmptyIDsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	err = store.BulkUpdateEnabled(context.Background(), uuid.New(), nil, false, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Pause(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	userID := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New()}

	mock.ExpectExec("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	err = store.BulkUpdateEnabled(context.Background(), uuid.New(), ids, false, &userID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Resume(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	ids := []uuid.UUID{uuid.New()}

	mock.ExpectExec("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.BulkUpdateEnabled(context.Background(), uuid.New(), ids, true, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkSoftDelete_EmptyIDsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	require.NoError(t, store.BulkSoftDelete(context.Background(), uuid.New(), nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkSoftDelete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	ids := []uuid.UUID{uuid.New(), uuid.New()}

	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	require.NoError(t, store.BulkSoftDelete(context.Background(), uuid.New(), ids))
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- AutomationRunStore ---

func automationRunColumnSlice() []string {
	return []string{
		"id", "automation_id", "org_id", "triggered_at", "triggered_by",
		"triggered_by_user_id", "scheduled_time", "goal_snapshot", "config_snapshot",
		"status", "completed_at", "result_summary", "created_at", "updated_at",
	}
}

func TestAutomationRunStore_CreateRun_Inserts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	now := time.Now()
	runID := uuid.New()

	r := &models.AutomationRun{
		AutomationID: uuid.New(),
		OrgID:        uuid.New(),
		TriggeredBy:  models.AutomationTriggeredByManual,
		GoalSnapshot: "goal",
		Status:       models.AutomationRunStatusPending,
	}

	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(anyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "triggered_at", "created_at", "updated_at"}).
				AddRow(runID, now, now, now),
		)

	created, err := store.CreateRun(context.Background(), r)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, runID, r.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_CreateRun_DuplicateReturnsFalse(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)

	r := &models.AutomationRun{
		AutomationID: uuid.New(),
		OrgID:        uuid.New(),
		TriggeredBy:  models.AutomationTriggeredBySchedule,
		GoalSnapshot: "goal",
		Status:       models.AutomationRunStatusPending,
	}

	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(anyArgs(8)...).
		WillReturnError(pgx.ErrNoRows)

	created, err := store.CreateRun(context.Background(), r)
	require.NoError(t, err)
	require.False(t, created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_GetByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	now := time.Now()
	runID := uuid.New()
	automationID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM automation_runs WHERE id =").
		WithArgs(anyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows(automationRunColumnSlice()).AddRow(
				runID, automationID, orgID, now, models.AutomationTriggeredByManual,
				nil, nil, "goal", []byte(`{}`),
				models.AutomationRunStatusPending, nil, nil, now, now,
			),
		)

	got, err := store.GetByID(context.Background(), orgID, automationID, runID)
	require.NoError(t, err)
	require.Equal(t, runID, got.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_ListByAutomation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM automation_runs WHERE automation_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(automationRunColumnSlice()).AddRow(
				uuid.New(), uuid.New(), uuid.New(), now, models.AutomationTriggeredBySchedule,
				nil, nil, "goal", []byte(`{}`),
				models.AutomationRunStatusCompleted, nil, nil, now, now,
			),
		)

	runs, err := store.ListByAutomation(context.Background(), uuid.New(), uuid.New(), AutomationRunFilters{Limit: 25})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	now := time.Now()
	summary := "ok"

	mock.ExpectExec("UPDATE automation_runs SET status").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), uuid.New(), uuid.New(), models.AutomationRunStatusCompleted, &now, &summary)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
