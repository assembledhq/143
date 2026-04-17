package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AutomationStore struct {
	db TxStarter
}

func NewAutomationStore(db TxStarter) *AutomationStore {
	return &AutomationStore{db: db}
}

const automationColumns = `id, org_id, repository_id, name, goal, scope,
	agent_type, model_override, execution_mode, max_concurrent, base_branch,
	schedule_type, interval_value, interval_unit, cron_expression, timezone,
	next_run_at, last_run_at, enabled, created_by, paused_by, paused_at,
	priority, created_at, updated_at, deleted_at`

func scanAutomation(row pgx.Row) (models.Automation, error) {
	var a models.Automation
	err := row.Scan(
		&a.ID, &a.OrgID, &a.RepositoryID, &a.Name, &a.Goal, &a.Scope,
		&a.AgentType, &a.ModelOverride, &a.ExecutionMode, &a.MaxConcurrent, &a.BaseBranch,
		&a.ScheduleType, &a.IntervalValue, &a.IntervalUnit, &a.CronExpression, &a.Timezone,
		&a.NextRunAt, &a.LastRunAt, &a.Enabled, &a.CreatedBy, &a.PausedBy, &a.PausedAt,
		&a.Priority, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt,
	)
	return a, err
}

func scanAutomations(rows pgx.Rows) ([]models.Automation, error) {
	var automations []models.Automation
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		automations = append(automations, a)
	}
	return automations, rows.Err()
}

func (s *AutomationStore) Create(ctx context.Context, a *models.Automation) error {
	query := `
		INSERT INTO automations (
			org_id, repository_id, name, goal, scope,
			agent_type, model_override, execution_mode, max_concurrent, base_branch,
			schedule_type, interval_value, interval_unit, cron_expression, timezone,
			next_run_at, enabled, created_by, priority
		) VALUES (
			@org_id, @repository_id, @name, @goal, @scope,
			@agent_type, @model_override, @execution_mode, @max_concurrent, @base_branch,
			@schedule_type, @interval_value, @interval_unit, @cron_expression, @timezone,
			@next_run_at, @enabled, @created_by, @priority
		) RETURNING id, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":          a.OrgID,
		"repository_id":   a.RepositoryID,
		"name":            a.Name,
		"goal":            a.Goal,
		"scope":           a.Scope,
		"agent_type":      a.AgentType,
		"model_override":  a.ModelOverride,
		"execution_mode":  a.ExecutionMode,
		"max_concurrent":  a.MaxConcurrent,
		"base_branch":     a.BaseBranch,
		"schedule_type":   a.ScheduleType,
		"interval_value":  a.IntervalValue,
		"interval_unit":   a.IntervalUnit,
		"cron_expression": a.CronExpression,
		"timezone":        a.Timezone,
		"next_run_at":     a.NextRunAt,
		"enabled":         a.Enabled,
		"created_by":      a.CreatedBy,
		"priority":        a.Priority,
	})
	return row.Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

func (s *AutomationStore) GetByID(ctx context.Context, orgID, automationID uuid.UUID) (models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`, automationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     automationID,
		"org_id": orgID,
	})
	return scanAutomation(row)
}

type AutomationFilters struct {
	Enabled *bool
	Limit   int
	Cursor  string
	Search  string
}

func (s *AutomationStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters AutomationFilters) ([]models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations WHERE org_id = @org_id AND deleted_at IS NULL`, automationColumns)
	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Enabled != nil {
		query += ` AND enabled = @enabled`
		args["enabled"] = *filters.Enabled
	}
	if filters.Search != "" {
		query += ` AND (name ILIKE @search OR goal ILIKE @search)`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filters.Search)
		args["search"] = "%" + escaped + "%"
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND (created_at < (SELECT created_at FROM automations WHERE id = @cursor_id) OR (created_at = (SELECT created_at FROM automations WHERE id = @cursor_id) AND id < @cursor_id))`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY created_at DESC, id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query automations: %w", err)
	}
	defer rows.Close()
	return scanAutomations(rows)
}

func (s *AutomationStore) Update(ctx context.Context, a *models.Automation) error {
	query := `
		UPDATE automations SET
			name = @name, goal = @goal, scope = @scope, repository_id = @repository_id,
			agent_type = @agent_type, model_override = @model_override,
			execution_mode = @execution_mode, max_concurrent = @max_concurrent,
			base_branch = @base_branch,
			schedule_type = @schedule_type, interval_value = @interval_value,
			interval_unit = @interval_unit, cron_expression = @cron_expression,
			timezone = @timezone, next_run_at = @next_run_at,
			enabled = @enabled, paused_by = @paused_by, paused_at = @paused_at,
			priority = @priority, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":              a.ID,
		"org_id":          a.OrgID,
		"name":            a.Name,
		"goal":            a.Goal,
		"scope":           a.Scope,
		"repository_id":   a.RepositoryID,
		"agent_type":      a.AgentType,
		"model_override":  a.ModelOverride,
		"execution_mode":  a.ExecutionMode,
		"max_concurrent":  a.MaxConcurrent,
		"base_branch":     a.BaseBranch,
		"schedule_type":   a.ScheduleType,
		"interval_value":  a.IntervalValue,
		"interval_unit":   a.IntervalUnit,
		"cron_expression": a.CronExpression,
		"timezone":        a.Timezone,
		"next_run_at":     a.NextRunAt,
		"enabled":         a.Enabled,
		"paused_by":       a.PausedBy,
		"paused_at":       a.PausedAt,
		"priority":        a.Priority,
	})
	return err
}

func (s *AutomationStore) SoftDelete(ctx context.Context, orgID, automationID uuid.UUID) error {
	query := `UPDATE automations SET deleted_at = now(), enabled = false, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     automationID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("automation not found or already deleted")
	}
	return nil
}

// ListDueForSchedule returns enabled automations that are due to run.
// Uses FOR UPDATE SKIP LOCKED for safe concurrent claiming across replicas.
//
// priority is ordered ASC because the UI exposes lower numbers as higher
// importance (Critical=0, High=25, Medium=50, Low=75). Sorting DESC would
// silently invert the user's intent and run Low before Critical.
func (s *AutomationStore) ListDueForSchedule(ctx context.Context, tx pgx.Tx, now time.Time) ([]models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations
		WHERE enabled = true AND deleted_at IS NULL
			AND next_run_at IS NOT NULL AND next_run_at <= @now
		ORDER BY priority ASC, next_run_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 100`, automationColumns)

	rows, err := tx.Query(ctx, query, pgx.NamedArgs{"now": now})
	if err != nil {
		return nil, fmt.Errorf("query due automations: %w", err)
	}
	defer rows.Close()
	return scanAutomations(rows)
}

// AdvanceNextRunAt updates last_run_at and next_run_at after claiming.
func (s *AutomationStore) AdvanceNextRunAt(ctx context.Context, tx pgx.Tx, automationID uuid.UUID, now time.Time, nextRunAt time.Time) error {
	query := `UPDATE automations SET last_run_at = @now, next_run_at = @next_run_at, updated_at = now()
		WHERE id = @id`
	_, err := tx.Exec(ctx, query, pgx.NamedArgs{
		"id":          automationID,
		"now":         now,
		"next_run_at": nextRunAt,
	})
	return err
}

// CountInFlightRuns returns the number of runs for an automation that are
// pending or running, used for max_concurrent enforcement.
// Accepts a transaction so the count is consistent with the scheduling transaction.
func (s *AutomationStore) CountInFlightRuns(ctx context.Context, tx pgx.Tx, automationID uuid.UUID) (int, error) {
	var count int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM automation_runs WHERE automation_id = @id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": automationID},
	).Scan(&count)
	return count, err
}

// BulkUpdateEnabled sets enabled state for multiple automations. When pausing,
// next_run_at is cleared so the scheduler skips paused rows. When resuming, an
// interval automation's next_run_at is recomputed from now() so it doesn't sit
// dead with a NULL next_run_at (which the scheduler's idx_automations_due
// excludes).
//
// Fails closed on empty automationIDs so a caller who forgot to populate the
// list can't silently pause or resume every automation in the org.
func (s *AutomationStore) BulkUpdateEnabled(ctx context.Context, orgID uuid.UUID, automationIDs []uuid.UUID, enabled bool, userID *uuid.UUID) error {
	if len(automationIDs) == 0 {
		return nil
	}

	var pausedBy *uuid.UUID
	var pausedAt *time.Time
	if !enabled {
		pausedBy = userID
		now := time.Now()
		pausedAt = &now
	}

	// On resume, compute next_run_at = now() + interval. Postgres' interval
	// arithmetic handles 'hours'/'days'/'weeks' literals natively. On pause,
	// clear next_run_at so the scheduler doesn't fire a paused automation.
	var nextRunAtExpr string
	if enabled {
		nextRunAtExpr = `CASE
			WHEN schedule_type = 'interval' AND interval_value IS NOT NULL AND interval_unit IS NOT NULL
			THEN now() + (interval_value::text || ' ' || interval_unit)::interval
			ELSE next_run_at
		END`
	} else {
		nextRunAtExpr = `NULL`
	}

	query := fmt.Sprintf(`UPDATE automations SET
			enabled = @enabled,
			paused_by = @paused_by,
			paused_at = @paused_at,
			next_run_at = %s,
			updated_at = now()
		WHERE org_id = @org_id AND deleted_at IS NULL AND id = ANY(@ids)`, nextRunAtExpr)

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"enabled":   enabled,
		"paused_by": pausedBy,
		"paused_at": pausedAt,
		"ids":       automationIDs,
	})
	return err
}

// BulkSoftDelete soft-deletes multiple automations. Fails closed on empty
// automationIDs to avoid silently wiping an entire org's automations.
func (s *AutomationStore) BulkSoftDelete(ctx context.Context, orgID uuid.UUID, automationIDs []uuid.UUID) error {
	if len(automationIDs) == 0 {
		return nil
	}

	query := `UPDATE automations SET deleted_at = now(), enabled = false, updated_at = now()
		WHERE org_id = @org_id AND deleted_at IS NULL AND id = ANY(@ids)`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"ids":    automationIDs,
	})
	return err
}

// --- AutomationRunStore ---

type AutomationRunStore struct {
	db TxStarter
}

func NewAutomationRunStore(db TxStarter) *AutomationRunStore {
	return &AutomationRunStore{db: db}
}

const automationRunColumns = `id, automation_id, org_id, triggered_at, triggered_by,
	triggered_by_user_id, scheduled_time, goal_snapshot, config_snapshot,
	status, completed_at, result_summary, created_at, updated_at`

func scanAutomationRun(row pgx.Row) (models.AutomationRun, error) {
	var r models.AutomationRun
	err := row.Scan(
		&r.ID, &r.AutomationID, &r.OrgID, &r.TriggeredAt, &r.TriggeredBy,
		&r.TriggeredByUserID, &r.ScheduledTime, &r.GoalSnapshot, &r.ConfigSnapshot,
		&r.Status, &r.CompletedAt, &r.ResultSummary, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

func scanAutomationRuns(rows pgx.Rows) ([]models.AutomationRun, error) {
	var runs []models.AutomationRun
	for rows.Next() {
		r, err := scanAutomationRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// CreateRun inserts a new automation run. If scheduled_time is set and a
// duplicate exists (idempotency index), the insert is skipped and false is returned.
func (s *AutomationRunStore) CreateRun(ctx context.Context, r *models.AutomationRun) (bool, error) {
	configJSON, err := json.Marshal(r.ConfigSnapshot)
	if err != nil {
		configJSON = nil
	}

	query := `
		INSERT INTO automation_runs (
			automation_id, org_id, triggered_by, triggered_by_user_id, scheduled_time,
			goal_snapshot, config_snapshot, status
		) VALUES (
			@automation_id, @org_id, @triggered_by, @triggered_by_user_id, @scheduled_time,
			@goal_snapshot, @config_snapshot, @status
		)
		ON CONFLICT (automation_id, scheduled_time) WHERE scheduled_time IS NOT NULL
		DO NOTHING
		RETURNING id, triggered_at, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"automation_id":        r.AutomationID,
		"org_id":               r.OrgID,
		"triggered_by":         r.TriggeredBy,
		"triggered_by_user_id": r.TriggeredByUserID,
		"scheduled_time":       r.ScheduledTime,
		"goal_snapshot":        r.GoalSnapshot,
		"config_snapshot":      configJSON,
		"status":               r.Status,
	})
	err = row.Scan(&r.ID, &r.TriggeredAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // duplicate — idempotency
	}
	return err == nil, err
}

// CreateRunInTx inserts a new automation run inside an existing transaction.
func (s *AutomationRunStore) CreateRunInTx(ctx context.Context, tx pgx.Tx, r *models.AutomationRun) (bool, error) {
	configJSON, err := json.Marshal(r.ConfigSnapshot)
	if err != nil {
		configJSON = nil
	}

	query := `
		INSERT INTO automation_runs (
			automation_id, org_id, triggered_by, triggered_by_user_id, scheduled_time,
			goal_snapshot, config_snapshot, status
		) VALUES (
			@automation_id, @org_id, @triggered_by, @triggered_by_user_id, @scheduled_time,
			@goal_snapshot, @config_snapshot, @status
		)
		ON CONFLICT (automation_id, scheduled_time) WHERE scheduled_time IS NOT NULL
		DO NOTHING
		RETURNING id, triggered_at, created_at, updated_at`

	row := tx.QueryRow(ctx, query, pgx.NamedArgs{
		"automation_id":        r.AutomationID,
		"org_id":               r.OrgID,
		"triggered_by":         r.TriggeredBy,
		"triggered_by_user_id": r.TriggeredByUserID,
		"scheduled_time":       r.ScheduledTime,
		"goal_snapshot":        r.GoalSnapshot,
		"config_snapshot":      configJSON,
		"status":               r.Status,
	})
	err = row.Scan(&r.ID, &r.TriggeredAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // duplicate — idempotency
	}
	return err == nil, err
}

// GetByID returns a single automation run scoped to the given org and parent
// automation. Both filters are required: a leaked run UUID must not be readable
// from another org's account.
func (s *AutomationRunStore) GetByID(ctx context.Context, orgID, automationID, runID uuid.UUID) (models.AutomationRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_runs
		WHERE id = @id AND automation_id = @automation_id AND org_id = @org_id`, automationRunColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":            runID,
		"automation_id": automationID,
		"org_id":        orgID,
	})
	return scanAutomationRun(row)
}

type AutomationRunFilters struct {
	Limit  int
	Cursor string
}

func (s *AutomationRunStore) ListByAutomation(ctx context.Context, orgID, automationID uuid.UUID, filters AutomationRunFilters) ([]models.AutomationRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_runs
		WHERE automation_id = @automation_id AND org_id = @org_id`, automationRunColumns)
	args := pgx.NamedArgs{
		"automation_id": automationID,
		"org_id":        orgID,
	}

	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND (triggered_at < (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id) OR (triggered_at = (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id) AND id < @cursor_id))`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY triggered_at DESC, id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query automation runs: %w", err)
	}
	defer rows.Close()
	return scanAutomationRuns(rows)
}

// UpdateStatus updates a run's status. Scoped by org_id so a leaked run UUID
// from another tenant cannot be mutated.
func (s *AutomationRunStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string, completedAt *time.Time, resultSummary *string) error {
	query := `UPDATE automation_runs SET status = @status, completed_at = @completed_at, result_summary = @result_summary, updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             runID,
		"org_id":         orgID,
		"status":         status,
		"completed_at":   completedAt,
		"result_summary": resultSummary,
	})
	return err
}

// ReapStuckRuns marks pending/running runs older than threshold as failed.
// Without this, a worker that crashes mid-run would leave a row stuck and
// permanently saturate max_concurrent for that automation — CountInFlightRuns
// counts pending+running, so the scheduler would never fire a new run.
//
// Returns the number of runs reaped.
func (s *AutomationRunStore) ReapStuckRuns(ctx context.Context, threshold time.Duration) (int64, error) {
	cutoff := time.Now().Add(-threshold)
	summary := "run exceeded execution timeout; marked failed by reaper"
	query := `UPDATE automation_runs
		SET status = 'failed',
		    completed_at = now(),
		    result_summary = @summary,
		    updated_at = now()
		WHERE status IN ('pending', 'running')
		  AND triggered_at < @cutoff`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"summary": summary,
		"cutoff":  cutoff,
	})
	if err != nil {
		return 0, fmt.Errorf("reap stuck automation runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
