package db

import (
	"context"
	"encoding/json"
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
		"icon_type", "icon_value",
		"agent_type", "model_override", "reasoning_effort", "execution_mode", "max_concurrent", "base_branch",
		"identity_scope", "pre_pr_review_loops",
		"schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone",
		"github_event_triggers", "github_event_filters",
		"next_run_at", "last_run_at", "enabled", "created_by", "paused_by", "paused_at",
		"priority", "external_metadata", "created_at", "updated_at", "deleted_at",
	}
}

func addAutomationRow(rows *pgxmock.Rows, a models.Automation) *pgxmock.Rows {
	metadata := a.ExternalMetadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	githubEventFilters := a.GitHubEventFilters
	if len(githubEventFilters) == 0 {
		githubEventFilters = []byte(`{}`)
	}
	return rows.AddRow(
		a.ID, a.OrgID, a.RepositoryID, a.Name, a.Goal, a.Scope,
		a.IconType.OrDefault(), a.IconValue,
		a.AgentType, a.ModelOverride, a.ReasoningEffort, a.ExecutionMode, a.MaxConcurrent, a.BaseBranch,
		a.IdentityScope.OrDefault(), a.PrePRReviewLoops,
		a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.IntervalRunAt, a.CronExpression, a.Timezone,
		automationGitHubEventsToStrings(a.GitHubEventTriggers), githubEventFilters,
		a.NextRunAt, a.LastRunAt, a.Enabled, a.CreatedBy, a.PausedBy, a.PausedAt,
		a.Priority, metadata, a.CreatedAt, a.UpdatedAt, a.DeletedAt,
	)
}

// bulkUpdateEnabledColumns mirrors the narrow RETURNING used by
// BulkUpdateEnabled — only the fields ComputeNextRunAt needs for cron fixup.
func bulkUpdateEnabledColumns() []string {
	return []string{"id", "schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone"}
}

func addBulkUpdateEnabledRow(rows *pgxmock.Rows, a models.Automation) *pgxmock.Rows {
	return rows.AddRow(a.ID, a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.IntervalRunAt, a.CronExpression, a.Timezone)
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
		IconType:      models.AutomationIconTypeEmoji,
		IconValue:     "🧹",
		ExecutionMode: "sequential",
		MaxConcurrent: 1,
		BaseBranch:    "main",
		ScheduleType:  models.AutomationScheduleInterval,
		Timezone:      "UTC",
		Enabled:       true,
		Priority:      50,
	}

	mock.ExpectQuery("INSERT INTO automations").
		WithArgs(anyArgs(28)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(newID, now, now),
		)

	require.NoError(t, store.Create(context.Background(), a))
	require.Equal(t, newID, a.ID)
	require.Equal(t, models.AutomationIconTypeEmoji, a.IconType)
	require.Equal(t, "🧹", a.IconValue)
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
		IconType: models.AutomationIconTypeEmoji, IconValue: "🔁",
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
	require.Equal(t, want.IconType, got.IconType)
	require.Equal(t, want.IconValue, got.IconValue)
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

func TestAutomationStore_ListEnabledByGitHubEvent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	want := models.Automation{
		ID: orgID, OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Review every PR",
		IconType: models.AutomationIconTypeEmoji, IconValue: "🔁",
		ExecutionMode: "sequential", MaxConcurrent: 1, BaseBranch: "main",
		IdentityScope: models.AutomationIdentityScopeOrg,
		ScheduleType:  models.AutomationScheduleInterval, Timezone: "UTC",
		GitHubEventTriggers: []models.AutomationGitHubEvent{models.AutomationGitHubEventPullRequestOpened},
		GitHubEventFilters:  json.RawMessage(`{}`),
		Enabled:             true, Priority: 50, ExternalMetadata: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM automations WHERE org_id = @org_id").
		WithArgs(pgx.NamedArgs{
			"org_id":        orgID,
			"repository_id": repoID,
			"event":         models.AutomationGitHubEventPullRequestOpened,
		}).
		WillReturnRows(addAutomationRow(pgxmock.NewRows(automationColumnSlice()), want))

	got, err := store.ListEnabledByGitHubEvent(context.Background(), orgID, repoID, models.AutomationGitHubEventPullRequestOpened)
	require.NoError(t, err, "listing enabled GitHub-event automations should not fail")
	require.Equal(t, []models.Automation{want}, got, "store should return matching enabled automations for the org and repository")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
		IconType: models.AutomationIconTypeEmoji, IconValue: "🚀",
		ExecutionMode: "sequential", BaseBranch: "main", ScheduleType: "interval",
		Timezone: "UTC", Enabled: true, Priority: 50,
	}

	mock.ExpectExec("UPDATE automations SET").
		WithArgs(anyArgs(30)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, store.Update(context.Background(), a))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_ClaimTriggerDedupe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(pgxmock.PgxPoolIface)
		expected  bool
		expectErr bool
	}{
		{
			name: "claims new key",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("WITH cleanup AS").
					WithArgs(anyArgs(4)...).
					WillReturnRows(pgxmock.NewRows([]string{"dedupe_key"}).AddRow("feedback:review:1"))
			},
			expected: true,
		},
		{
			name: "returns false for existing key",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("WITH cleanup AS").
					WithArgs(anyArgs(4)...).
					WillReturnError(pgx.ErrNoRows)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should be created")
			defer mock.Close()
			tt.setupMock(mock)

			store := NewAutomationRunStore(mock)
			got, err := store.ClaimTriggerDedupe(context.Background(), uuid.New(), uuid.New(), "feedback:review:1", time.Now().Add(time.Minute))
			if tt.expectErr {
				require.Error(t, err, "dedupe claim should return error")
				return
			}
			require.NoError(t, err, "dedupe claim should not error")
			require.Equal(t, tt.expected, got, "dedupe claim should return expected inserted state")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
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
	affected, fixupFailures, err := store.BulkUpdateEnabled(context.Background(), uuid.New(), nil, false, nil)
	require.NoError(t, err)
	require.Empty(t, affected)
	require.Empty(t, fixupFailures)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Pause(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New()}

	a1 := models.Automation{ID: ids[0], OrgID: orgID, Name: "a1", ScheduleType: "interval", Timezone: "UTC"}
	a2 := models.Automation{ID: ids[1], OrgID: orgID, Name: "a2", ScheduleType: "interval", Timezone: "UTC"}

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnRows(addBulkUpdateEnabledRow(addBulkUpdateEnabledRow(pgxmock.NewRows(bulkUpdateEnabledColumns()), a1), a2))
	mock.ExpectCommit()

	affected, fixupFailures, err := store.BulkUpdateEnabled(context.Background(), orgID, ids, false, &userID)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
	require.Empty(t, fixupFailures, "pause path never fixes up cron rows")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Resume(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New()}

	iv := 1
	iu := models.ScheduleUnitHours
	a1 := models.Automation{
		ID: ids[0], OrgID: orgID, Name: "a1",
		ScheduleType: "interval", IntervalValue: &iv, IntervalUnit: &iu, Timezone: "UTC",
	}

	mock.ExpectBegin()
	// Assert the resume path emits the CASE expression that recomputes
	// next_run_at from interval_value/interval_unit — without this regex
	// a silent regression to `NULL` would pass the looser UPDATE check.
	mock.ExpectQuery(`interval_value::text \|\| ' ' \|\| interval_unit\)::interval`).
		WithArgs(anyArgs(5)...).
		WillReturnRows(addBulkUpdateEnabledRow(pgxmock.NewRows(bulkUpdateEnabledColumns()), a1))
	// No cron fixup expected for interval-only rows.
	mock.ExpectCommit()

	affected, fixupFailures, err := store.BulkUpdateEnabled(context.Background(), orgID, ids, true, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
	require.Empty(t, fixupFailures, "interval-only resume produces no cron fixups")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_Resume_CronFixup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New()}

	// Cron rows come back from the bulk UPDATE with next_run_at=NULL because
	// Postgres can't evaluate cron expressions. The Go-side fixup then issues
	// a per-row UPDATE with the computed next_run_at.
	expr := "0 9 * * *"
	a1 := models.Automation{
		ID: ids[0], OrgID: orgID, Name: "cron-a1",
		ScheduleType:   "cron",
		CronExpression: &expr,
		Timezone:       "UTC",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnRows(addBulkUpdateEnabledRow(pgxmock.NewRows(bulkUpdateEnabledColumns()), a1))
	mock.ExpectExec("UPDATE automations SET next_run_at").
		WithArgs(anyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	affected, fixupFailures, err := store.BulkUpdateEnabled(context.Background(), orgID, ids, true, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, affected)
	require.Empty(t, fixupFailures, "valid cron expression should fix up cleanly")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutomationStore_BulkUpdateEnabled_Resume_CronFixupFailure verifies that
// a malformed cron expression on a resumed row does not abort the bulk and
// produces a CronFixupFailure entry the caller can surface to the operator.
// Without this signal the row would be enabled=true with next_run_at=NULL
// and silently never fire — the worst kind of "automation bug".
func TestAutomationStore_BulkUpdateEnabled_Resume_CronFixupFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New()}

	// Cron expression that ValidateCronExpression / ComputeNextRunAt rejects.
	// We rely on the model's parser to fail rather than constructing an error
	// path manually — keeps this test honest about which inputs trip it.
	expr := "this is definitely not a cron expression"
	a1 := models.Automation{
		ID: ids[0], OrgID: orgID, Name: "broken-cron",
		ScheduleType:   "cron",
		CronExpression: &expr,
		Timezone:       "UTC",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnRows(addBulkUpdateEnabledRow(pgxmock.NewRows(bulkUpdateEnabledColumns()), a1))
	// No follow-up UPDATE expected — the per-row fixup is skipped on parse error.
	mock.ExpectCommit()

	affected, fixupFailures, err := store.BulkUpdateEnabled(context.Background(), orgID, ids, true, nil)
	require.NoError(t, err, "a single malformed cron must not abort the bulk")
	require.ElementsMatch(t, ids, affected, "row was still resumed in the SQL UPDATE")
	require.Len(t, fixupFailures, 1, "operator must be told the cron next_run_at couldn't be computed")
	require.Equal(t, ids[0], fixupFailures[0].AutomationID)
	require.NotEmpty(t, fixupFailures[0].Reason)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationStore_BulkUpdateEnabled_ReturningScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New()}

	mock.ExpectBegin()
	// Bad UUID type in the first RETURNING column forces rows.Scan to error.
	mock.ExpectQuery("UPDATE automations SET").
		WithArgs(anyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows(bulkUpdateEnabledColumns()).
				AddRow("not-a-uuid", "interval", nil, nil, nil, nil, "UTC"),
		)
	mock.ExpectRollback()

	_, _, err = store.BulkUpdateEnabled(context.Background(), orgID, ids, true, nil)
	require.Error(t, err, "BulkUpdateEnabled should return an error when RETURNING rows cannot be scanned")
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
		"status", "capability_snapshot", "completed_at", "result_summary", "created_at", "updated_at",
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
		WithArgs(anyArgs(9)...).
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
		WithArgs(anyArgs(9)...).
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
				models.AutomationRunStatusPending, nil, nil, nil, now, now,
			),
		)

	got, err := store.GetByID(context.Background(), orgID, automationID, runID)
	require.NoError(t, err)
	require.Equal(t, runID, got.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_CountConsecutiveFailures(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	automationID := uuid.New()
	store := NewAutomationRunStore(mock)

	mock.ExpectQuery(`WITH ordered AS`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountConsecutiveFailures(context.Background(), orgID, automationID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_ListByAutomation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sessionID := uuid.New()
	title := "Refactor diff viewer"
	failureExplanation := "Three tests failed in orchestrator_test.go"
	failureCategory := "tests-failing"
	failureNextSteps := []string{"Re-run with --focus orchestrator", "Inspect logs in session"}
	prURL := "https://github.com/example/repo/pull/1213"

	type prRow struct {
		number   int
		url      string
		status   models.PullRequestStatus
		ciStatus models.PullRequestCIStatus
	}
	type sessionRow struct {
		id                  uuid.UUID
		title               *string
		status              models.SessionStatus
		diffStats           []byte
		failureExplanation  *string
		failureCategory     *string
		failureNextSteps    []string
		failureRetryAdvised bool
		prCreationState     string
		pr                  *prRow
	}

	cases := []struct {
		name             string
		runStatus        models.AutomationRunStatus
		session          *sessionRow
		assertEnrichment func(t *testing.T, got models.AutomationRun)
	}{
		{
			name:      "completed run with session and PR",
			runStatus: models.AutomationRunStatusCompleted,
			session: &sessionRow{
				id:              sessionID,
				title:           &title,
				status:          models.SessionStatusCompleted,
				diffStats:       []byte(`{"added":128,"removed":23,"files_changed":4}`),
				prCreationState: "succeeded",
				pr: &prRow{
					number:   1213,
					url:      prURL,
					status:   models.PullRequestStatusOpen,
					ciStatus: models.PullRequestCIStatusSuccess,
				},
			},
			assertEnrichment: func(t *testing.T, got models.AutomationRun) {
				t.Helper()
				require.NotNil(t, got.Session)
				require.Equal(t, sessionID, got.Session.ID)
				require.Equal(t, "Refactor diff viewer", *got.Session.Title)
				require.Equal(t, models.SessionStatusCompleted, got.Session.Status)
				require.JSONEq(t, `{"added":128,"removed":23,"files_changed":4}`, string(got.Session.DiffStats))
				require.NotNil(t, got.Session.PR)
				require.Equal(t, 1213, got.Session.PR.Number)
				require.Equal(t, prURL, got.Session.PR.URL)
				require.Equal(t, models.PullRequestStatusOpen, got.Session.PR.Status)
				require.Equal(t, models.PullRequestCIStatusSuccess, got.Session.PR.CIStatus)
			},
		},
		{
			name:      "completed run with session but no PR",
			runStatus: models.AutomationRunStatusCompleted,
			session: &sessionRow{
				id:              sessionID,
				title:           &title,
				status:          models.SessionStatusCompleted,
				diffStats:       []byte(`{"added":12,"removed":3}`),
				prCreationState: "idle",
			},
			assertEnrichment: func(t *testing.T, got models.AutomationRun) {
				t.Helper()
				require.NotNil(t, got.Session)
				require.Nil(t, got.Session.PR)
				require.Equal(t, models.PRCreationState("idle"), got.Session.PRCreationState)
			},
		},
		{
			name:      "failed run carries failure metadata",
			runStatus: models.AutomationRunStatusFailed,
			session: &sessionRow{
				id:                  sessionID,
				title:               nil,
				status:              models.SessionStatusFailed,
				failureExplanation:  &failureExplanation,
				failureCategory:     &failureCategory,
				failureNextSteps:    failureNextSteps,
				failureRetryAdvised: true,
				prCreationState:     "idle",
			},
			assertEnrichment: func(t *testing.T, got models.AutomationRun) {
				t.Helper()
				require.NotNil(t, got.Session)
				require.Equal(t, failureExplanation, *got.Session.FailureExplanation)
				require.Equal(t, failureCategory, *got.Session.FailureCategory)
				require.Equal(t, failureNextSteps, got.Session.FailureNextSteps)
				require.True(t, got.Session.FailureRetryAdvised)
			},
		},
		{
			name:      "pending run without a spawned session yet",
			runStatus: models.AutomationRunStatusPending,
			session:   nil,
			assertEnrichment: func(t *testing.T, got models.AutomationRun) {
				t.Helper()
				require.Nil(t, got.Session)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewAutomationRunStore(mock)

			cols := AutomationRunListColumns
			row := []any{
				uuid.New(), uuid.New(), uuid.New(), now, models.AutomationTriggeredBySchedule,
				nil, nil, "goal",
				tc.runStatus, nil, nil, now, now,
			}
			if tc.session != nil {
				// pgxmock matches scan kinds via reflection: a *T scan
				// destination requires *T (or nil) as the AddRow value, so
				// we wrap every non-NULL nullable column in a pointer.
				// Address-takeable copies are needed because struct fields
				// are not addressable through interface{} boxing.
				sessionIDCopy := tc.session.id
				sessionStatusCopy := tc.session.status
				retryCopy := tc.session.failureRetryAdvised
				prCreationCopy := tc.session.prCreationState
				row = append(row,
					&sessionIDCopy,
					tc.session.title,
					&sessionStatusCopy,
					tc.session.diffStats,
					tc.session.failureExplanation,
					tc.session.failureCategory,
					tc.session.failureNextSteps,
					&retryCopy,
					&prCreationCopy,
				)
				if tc.session.pr != nil {
					prNumberCopy := tc.session.pr.number
					prURLCopy := tc.session.pr.url
					prStatusCopy := tc.session.pr.status
					prCICopy := tc.session.pr.ciStatus
					row = append(row, &prNumberCopy, &prURLCopy, &prStatusCopy, &prCICopy)
				} else {
					row = append(row, nil, nil, nil, nil)
				}
			} else {
				// 9 session columns + 4 PR columns = 13 NULLs.
				for i := 0; i < 13; i++ {
					row = append(row, nil)
				}
			}

			mock.ExpectQuery("SELECT .+ FROM automation_runs ar.+LEFT JOIN LATERAL").
				WithArgs(anyArgs(2)...).
				WillReturnRows(
					pgxmock.NewRows(cols).AddRow(row...),
				)

			runs, err := store.ListByAutomation(context.Background(), uuid.New(), uuid.New(), AutomationRunFilters{Limit: 25})
			require.NoError(t, err)
			require.Len(t, runs, 1)
			require.Equal(t, tc.runStatus, runs[0].Status)
			tc.assertEnrichment(t, runs[0])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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
		WithArgs(anyArgs(9)...).
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
		WithArgs(anyArgs(9)...).
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

func TestAutomationRunStore_GetStats(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)
	orgID := uuid.New()
	automationID := uuid.New()

	day1 := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"bucket", "total", "completed", "completed_noop", "failed",
		"skipped", "running", "pending", "avg_duration_seconds",
	}
	rows := pgxmock.NewRows(cols).
		AddRow(day1, 5, 3, 1, 1, 0, 0, 0, 120.0).
		AddRow(day2, 2, 2, 0, 0, 0, 0, 0, 60.0)

	mock.ExpectQuery(`FROM automation_runs\s+WHERE org_id = @org_id\s+AND automation_id = @automation_id\s+AND triggered_at >= @since\s+AND triggered_at < @until\s+GROUP BY bucket\s+ORDER BY bucket ASC`).
		WithArgs(anyArgs(4)...).
		WillReturnRows(rows)

	stats, err := store.GetStats(context.Background(), orgID, automationID, since, until)
	require.NoError(t, err)
	require.Len(t, stats.Buckets, 2)
	require.Equal(t, 5, stats.Buckets[0].Total)
	require.Equal(t, 3, stats.Buckets[0].Completed)
	require.Equal(t, 1, stats.Buckets[0].Failed)
	require.InDelta(t, 120.0, stats.Buckets[0].AvgDurationSeconds, 0.001)

	// Window totals roll up across buckets.
	require.Equal(t, 7, stats.Totals.Total)
	require.Equal(t, 5, stats.Totals.Completed)
	require.Equal(t, 1, stats.Totals.CompletedNoop)
	require.Equal(t, 1, stats.Totals.Failed)

	// Success rate: (completed + completed_noop) / (completed + completed_noop + failed)
	//             = (5 + 1) / (5 + 1 + 1) = 6/7
	require.InDelta(t, 6.0/7.0, stats.Totals.SuccessRate, 0.001)

	// Weighted duration: (5*120 + 2*60) / (5 + 2) = 720/7 ≈ 102.857
	require.InDelta(t, 720.0/7.0, stats.Totals.AvgDurationSeconds, 0.001)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutomationRunStore_GetStats_EmptyWindow(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAutomationRunStore(mock)

	cols := []string{
		"bucket", "total", "completed", "completed_noop", "failed",
		"skipped", "running", "pending", "avg_duration_seconds",
	}
	mock.ExpectQuery(`FROM automation_runs`).
		WithArgs(anyArgs(4)...).
		WillReturnRows(pgxmock.NewRows(cols))

	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	stats, err := store.GetStats(context.Background(), uuid.New(), uuid.New(), since, until)
	require.NoError(t, err)
	require.Empty(t, stats.Buckets)
	// No terminal runs → success_rate stays 0 rather than NaN.
	require.Equal(t, 0.0, stats.Totals.SuccessRate)
	require.Equal(t, 0.0, stats.Totals.AvgDurationSeconds)
	require.NoError(t, mock.ExpectationsWereMet())
}
