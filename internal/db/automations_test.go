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

	// SoftDelete wraps both the automation update and the pending-run cancel in
	// one tx so a deleted automation never leaves pending runs behind.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE automation_runs SET status = 'skipped'`).
		WithArgs(anyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	require.NoError(t, store.SoftDelete(context.Background(), orgID, id))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_SoftDelete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)

	// No pending-run cancel or commit expected: the automation update returns
	// 0 rows, so SoftDelete aborts and the rollback fires.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

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
	affected, err := store.BulkUpdateEnabled(context.Background(), uuid.New(), nil, false, nil)
	require.NoError(t, err)
	require.Empty(t, affected)
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

	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]).AddRow(ids[1]))

	affected, err := store.BulkUpdateEnabled(context.Background(), uuid.New(), ids, false, &userID)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Resume(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	ids := []uuid.UUID{uuid.New()}

	// Assert the resume path emits the CASE expression that recomputes
	// next_run_at from interval_value/interval_unit — without this regex
	// a silent regression to `NULL` would pass the looser UPDATE check.
	mock.ExpectQuery(`interval_value::text \|\| ' ' \|\| interval_unit\)::interval`).
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]))

	affected, err := store.BulkUpdateEnabled(context.Background(), uuid.New(), ids, true, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkSoftDelete_EmptyIDsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	affected, err := store.BulkSoftDelete(context.Background(), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkSoftDelete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	ids := []uuid.UUID{uuid.New(), uuid.New()}

	// Bulk delete runs in a tx so affected automations' pending runs are
	// cancelled atomically alongside the soft delete.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET deleted_at").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]).AddRow(ids[1]))
	mock.ExpectExec(`UPDATE automation_runs SET status = 'skipped'`).
		WithArgs(anyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	affected, err := store.BulkSoftDelete(context.Background(), uuid.New(), ids)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
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

func TestAutomationStore_CountInFlightRuns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM automation_runs WHERE automation_id = .+ AND org_id = .+ AND status IN \('pending', 'running'\)`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err)

	got, err := store.CountInFlightRuns(ctx, tx, uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, got)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_LockByIDForUpdate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	now := time.Now()
	automation := models.Automation{
		ID: uuid.New(), OrgID: uuid.New(), Name: "a", Goal: "g",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		ScheduleType: "interval", Timezone: "UTC", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = .+ FOR UPDATE`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(addAutomationRow(pgxmock.NewRows(automationColumnSlice()), automation))
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err)

	got, err := store.LockByIDForUpdate(ctx, tx, automation.OrgID, automation.ID)
	require.NoError(t, err)
	require.Equal(t, automation.ID, got.ID)
	require.Equal(t, automation.OrgID, got.OrgID)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_CreateRunInTx_Inserts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	now := time.Now()
	runID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(anyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "triggered_at", "created_at", "updated_at"}).
				AddRow(runID, now, now, now),
		)
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err)

	r := &models.AutomationRun{
		AutomationID: uuid.New(),
		OrgID:        uuid.New(),
		TriggeredBy:  models.AutomationTriggeredByManual,
		GoalSnapshot: "goal",
		Status:       models.AutomationRunStatusPending,
	}
	created, err := store.CreateRunInTx(ctx, tx, r)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, runID, r.ID)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_CreateRunInTx_DuplicateReturnsFalse(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO automation_runs").
		WithArgs(anyArgs(8)...).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	ctx := context.Background()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err)

	r := &models.AutomationRun{
		AutomationID: uuid.New(),
		OrgID:        uuid.New(),
		TriggeredBy:  models.AutomationTriggeredBySchedule,
		GoalSnapshot: "goal",
		Status:       models.AutomationRunStatusPending,
	}
	created, err := store.CreateRunInTx(ctx, tx, r)
	require.NoError(t, err)
	require.False(t, created)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_CompletePendingNoopIfAutomationActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rowsAffected int64
		expected     bool
	}{
		{name: "updates pending run for active automation", rowsAffected: 1, expected: true},
		{name: "leaves skipped or deleted automation run unchanged", rowsAffected: 0, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewAutomationRunStore(mock)
			now := time.Now()
			summary := "noop"

			mock.ExpectExec(`UPDATE automation_runs AS r\s+SET status = @status.*FROM automations AS a.*r.status = 'pending'.*a.deleted_at IS NULL`).
				WithArgs(anyArgs(6)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))

			updated, err := store.CompletePendingNoopIfAutomationActive(context.Background(), uuid.New(), uuid.New(), uuid.New(), &now, &summary)
			require.NoError(t, err)
			require.Equal(t, tt.expected, updated)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAutomationRunStore_ReapStuckRuns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	orgID := uuid.New()

	// The reaper MUST filter by org_id, status IN ('pending', 'running'), and
	// triggered_at < cutoff. Regressions on the org_id filter would sweep
	// across tenants; regressions on status/triggered_at would either reap
	// healthy runs or fail to free saturated max_concurrent slots.
	mock.ExpectExec(`UPDATE automation_runs\s+SET status = 'failed'.*org_id = @org_id.*status IN \('pending', 'running'\).*triggered_at < @cutoff`).
		WithArgs(anyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 4))

	n, err := store.ReapStuckRuns(context.Background(), orgID, time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 4, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_ReapStuckRuns_Error(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)

	mock.ExpectExec("UPDATE automation_runs").
		WithArgs(anyArgs(3)...).
		WillReturnError(errors.New("boom"))

	_, err = store.ReapStuckRuns(context.Background(), uuid.New(), time.Hour)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_ListOrgsWithStuckRuns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	orgA := uuid.New()
	orgB := uuid.New()

	mock.ExpectQuery(`SELECT DISTINCT org_id FROM automation_runs\s+WHERE status IN \('pending', 'running'\) AND triggered_at < @cutoff`).
		WithArgs(anyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"org_id"}).AddRow(orgA).AddRow(orgB))

	orgs, err := store.ListOrgsWithStuckRuns(context.Background(), time.Hour)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{orgA, orgB}, orgs)
	require.NoError(t, mock.ExpectationsWereMet())
}
