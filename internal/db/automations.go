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
	icon_type, icon_value,
	agent_type, model_override, reasoning_effort, execution_mode, max_concurrent, base_branch,
	identity_scope, pre_pr_review_loops, schedule_type, interval_value, interval_unit, interval_run_at, cron_expression, timezone,
	github_event_triggers, github_event_filters,
	next_run_at, last_run_at, enabled, created_by, paused_by, paused_at,
	priority, external_metadata, created_at, updated_at, deleted_at`

// maxDueAutomationsPerTick caps how many due automations the scheduler claims
// in one tick. Combined with the 10-minute tick cadence, this gives a global
// ceiling of ~600 fires/hour. Excess due rows aren't dropped — they're just
// claimed on the next tick (FOR UPDATE SKIP LOCKED leaves them visible to the
// next claimer). Raise this only after confirming downstream worker capacity
// can absorb the larger batch.
const maxDueAutomationsPerTick = 100

func scanAutomation(row pgx.Row) (models.Automation, error) {
	var a models.Automation
	var githubEventTriggers []string
	err := row.Scan(
		&a.ID, &a.OrgID, &a.RepositoryID, &a.Name, &a.Goal, &a.Scope,
		&a.IconType, &a.IconValue,
		&a.AgentType, &a.ModelOverride, &a.ReasoningEffort, &a.ExecutionMode, &a.MaxConcurrent, &a.BaseBranch,
		&a.IdentityScope, &a.PrePRReviewLoops, &a.ScheduleType, &a.IntervalValue, &a.IntervalUnit, &a.IntervalRunAt, &a.CronExpression, &a.Timezone,
		&githubEventTriggers, &a.GitHubEventFilters,
		&a.NextRunAt, &a.LastRunAt, &a.Enabled, &a.CreatedBy, &a.PausedBy, &a.PausedAt,
		&a.Priority, &a.ExternalMetadata, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt,
	)
	if err == nil {
		a.GitHubEventTriggers = automationGitHubEventsFromStrings(githubEventTriggers)
	}
	return a, err
}

func automationGitHubEventsFromStrings(events []string) []models.AutomationGitHubEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]models.AutomationGitHubEvent, 0, len(events))
	for _, event := range events {
		out = append(out, models.AutomationGitHubEvent(event))
	}
	return out
}

func automationGitHubEventsToStrings(events []models.AutomationGitHubEvent) []string {
	if len(events) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event))
	}
	return out
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
			icon_type, icon_value,
			agent_type, model_override, reasoning_effort, execution_mode, max_concurrent, base_branch,
			identity_scope, pre_pr_review_loops, schedule_type, interval_value, interval_unit, interval_run_at, cron_expression, timezone,
			github_event_triggers, github_event_filters,
			next_run_at, enabled, created_by, priority, external_metadata
		) VALUES (
			@org_id, @repository_id, @name, @goal, @scope,
			@icon_type, @icon_value,
			@agent_type, @model_override, @reasoning_effort, @execution_mode, @max_concurrent, @base_branch,
			@identity_scope, @pre_pr_review_loops, @schedule_type, @interval_value, @interval_unit, @interval_run_at, @cron_expression, @timezone,
			@github_event_triggers, @github_event_filters,
			@next_run_at, @enabled, @created_by, @priority, @external_metadata
		) RETURNING id, created_at, updated_at`
	metadata := a.ExternalMetadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	githubEventFilters := a.GitHubEventFilters
	if len(githubEventFilters) == 0 {
		githubEventFilters = json.RawMessage(`{}`)
	}

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                a.OrgID,
		"repository_id":         a.RepositoryID,
		"name":                  a.Name,
		"goal":                  a.Goal,
		"scope":                 a.Scope,
		"icon_type":             a.IconType.OrDefault(),
		"icon_value":            models.AutomationIconValueOrDefault(a.IconValue),
		"agent_type":            a.AgentType,
		"model_override":        a.ModelOverride,
		"reasoning_effort":      a.ReasoningEffort,
		"execution_mode":        a.ExecutionMode,
		"max_concurrent":        a.MaxConcurrent,
		"base_branch":           a.BaseBranch,
		"identity_scope":        a.IdentityScope.OrDefault(),
		"pre_pr_review_loops":   a.PrePRReviewLoops,
		"schedule_type":         a.ScheduleType,
		"interval_value":        a.IntervalValue,
		"interval_unit":         a.IntervalUnit,
		"interval_run_at":       a.IntervalRunAt,
		"cron_expression":       a.CronExpression,
		"timezone":              a.Timezone,
		"github_event_triggers": automationGitHubEventsToStrings(a.GitHubEventTriggers),
		"github_event_filters":  githubEventFilters,
		"next_run_at":           a.NextRunAt,
		"enabled":               a.Enabled,
		"created_by":            a.CreatedBy,
		"priority":              a.Priority,
		"external_metadata":     metadata,
	})
	return row.Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

func (s *AutomationStore) ListEnabledByGitHubEvent(ctx context.Context, orgID, repositoryID uuid.UUID, event models.AutomationGitHubEvent) ([]models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations WHERE org_id = @org_id
		AND repository_id = @repository_id
		AND enabled = true
		AND deleted_at IS NULL
		AND @event = ANY(github_event_triggers)
		ORDER BY priority ASC, created_at ASC`, automationColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
		"event":         event,
	})
	if err != nil {
		return nil, fmt.Errorf("query github event automations: %w", err)
	}
	defer rows.Close()
	return scanAutomations(rows)
}

func (s *AutomationStore) GetByID(ctx context.Context, orgID, automationID uuid.UUID) (models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`, automationColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     automationID,
		"org_id": orgID,
	})
	return scanAutomation(row)
}

// LockByIDForUpdate returns an automation row locked for the caller's
// transaction. Use this before making decisions that must serialize against
// other writers for the same automation, such as max_concurrent checks.
func (s *AutomationStore) LockByIDForUpdate(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		FOR UPDATE`, automationColumns)
	row := tx.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     automationID,
		"org_id": orgID,
	})
	return scanAutomation(row)
}

type AutomationFilters struct {
	Enabled       *bool
	Limit         int
	Cursor        string
	Search        string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
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
	if filters.CreatedAfter != nil {
		query += ` AND created_at >= @created_after`
		args["created_after"] = *filters.CreatedAfter
	}
	if filters.CreatedBefore != nil {
		query += ` AND created_at <= @created_before`
		args["created_before"] = *filters.CreatedBefore
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			// Filter deleted_at IS NULL inside the subquery: a cursor pointing
			// at a row that was soft-deleted between pages would otherwise
			// resolve to NULL and the `<` comparison would silently return no
			// rows, stalling pagination for the rest of the list.
			query += ` AND (created_at < (SELECT created_at FROM automations WHERE id = @cursor_id AND deleted_at IS NULL) OR (created_at = (SELECT created_at FROM automations WHERE id = @cursor_id AND deleted_at IS NULL) AND id < @cursor_id))`
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
			icon_type = @icon_type, icon_value = @icon_value,
			agent_type = @agent_type, model_override = @model_override, reasoning_effort = @reasoning_effort,
			execution_mode = @execution_mode, max_concurrent = @max_concurrent,
			base_branch = @base_branch, identity_scope = @identity_scope,
			pre_pr_review_loops = @pre_pr_review_loops,
			schedule_type = @schedule_type, interval_value = @interval_value,
			interval_unit = @interval_unit, interval_run_at = @interval_run_at, cron_expression = @cron_expression,
			timezone = @timezone, github_event_triggers = @github_event_triggers,
			github_event_filters = @github_event_filters, next_run_at = @next_run_at,
			enabled = @enabled, paused_by = @paused_by, paused_at = @paused_at,
			priority = @priority, external_metadata = @external_metadata, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	metadata := a.ExternalMetadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	githubEventFilters := a.GitHubEventFilters
	if len(githubEventFilters) == 0 {
		githubEventFilters = json.RawMessage(`{}`)
	}

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                    a.ID,
		"org_id":                a.OrgID,
		"name":                  a.Name,
		"goal":                  a.Goal,
		"scope":                 a.Scope,
		"repository_id":         a.RepositoryID,
		"icon_type":             a.IconType.OrDefault(),
		"icon_value":            models.AutomationIconValueOrDefault(a.IconValue),
		"agent_type":            a.AgentType,
		"model_override":        a.ModelOverride,
		"reasoning_effort":      a.ReasoningEffort,
		"execution_mode":        a.ExecutionMode,
		"max_concurrent":        a.MaxConcurrent,
		"base_branch":           a.BaseBranch,
		"identity_scope":        a.IdentityScope.OrDefault(),
		"pre_pr_review_loops":   a.PrePRReviewLoops,
		"schedule_type":         a.ScheduleType,
		"interval_value":        a.IntervalValue,
		"interval_unit":         a.IntervalUnit,
		"interval_run_at":       a.IntervalRunAt,
		"cron_expression":       a.CronExpression,
		"timezone":              a.Timezone,
		"github_event_triggers": automationGitHubEventsToStrings(a.GitHubEventTriggers),
		"github_event_filters":  githubEventFilters,
		"next_run_at":           a.NextRunAt,
		"enabled":               a.Enabled,
		"paused_by":             a.PausedBy,
		"paused_at":             a.PausedAt,
		"priority":              a.Priority,
		"external_metadata":     metadata,
	})
	return err
}

// SoftDelete marks an automation deleted and cancels any pending runs in the
// same transaction. Running rows are left alone — the worker is expected to
// observe the deleted_at on pickup/finish. Skipping only pending rows keeps
// the semantics tight: we don't race-cancel a run that's actively executing.
func (s *AutomationStore) SoftDelete(ctx context.Context, orgID, automationID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin soft-delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE automations SET deleted_at = now(), enabled = false, updated_at = now()
			WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{"id": automationID, "org_id": orgID},
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("automation not found or already deleted")
	}

	if _, err := tx.Exec(ctx,
		`UPDATE automation_runs SET status = 'skipped',
			completed_at = now(),
			result_summary = 'automation deleted before run could start',
			updated_at = now()
			WHERE automation_id = @id AND status = 'pending'`,
		pgx.NamedArgs{"id": automationID},
	); err != nil {
		return fmt.Errorf("cancel pending runs: %w", err)
	}

	return tx.Commit(ctx)
}

// ListDueForSchedule returns enabled automations that are due to run.
// Uses FOR UPDATE SKIP LOCKED for safe concurrent claiming across replicas.
//
// priority is ordered ASC because the UI exposes lower numbers as higher
// importance (Critical=0, High=25, Medium=50, Low=75). Sorting DESC would
// silently invert the user's intent and run Low before Critical.
// lint:allow-no-orgid reason="scheduler tick claims due automations across all orgs"
func (s *AutomationStore) ListDueForSchedule(ctx context.Context, tx pgx.Tx, now time.Time) ([]models.Automation, error) {
	query := fmt.Sprintf(`SELECT %s FROM automations
		WHERE enabled = true AND deleted_at IS NULL
			AND next_run_at IS NOT NULL AND next_run_at <= @now
		ORDER BY priority ASC, next_run_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT %d`, automationColumns, maxDueAutomationsPerTick)

	rows, err := tx.Query(ctx, query, pgx.NamedArgs{"now": now})
	if err != nil {
		return nil, fmt.Errorf("query due automations: %w", err)
	}
	defer rows.Close()
	return scanAutomations(rows)
}

// AdvanceNextRunAt updates last_run_at and next_run_at after claiming.
// org_id is required (not just automation_id) so a caller that confuses IDs
// across tenants can't mutate another org's row. The scheduler already claims
// under FOR UPDATE SKIP LOCKED, so today the check is redundant — it's kept
// as defense-in-depth against future callers.
func (s *AutomationStore) AdvanceNextRunAt(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID, now time.Time, nextRunAt time.Time) error {
	query := `UPDATE automations SET last_run_at = @now, next_run_at = @next_run_at, updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	_, err := tx.Exec(ctx, query, pgx.NamedArgs{
		"id":          automationID,
		"org_id":      orgID,
		"now":         now,
		"next_run_at": nextRunAt,
	})
	return err
}

// CountInFlightRuns returns the number of runs for an automation that are
// pending or running, used for max_concurrent enforcement. org_id is required
// so a leaked automation UUID from another tenant cannot be counted against
// this org's quota.
// Accepts a transaction so the count is consistent with the scheduling transaction.
func (s *AutomationStore) CountInFlightRuns(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) (int, error) {
	var count int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM automation_runs WHERE automation_id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": automationID, "org_id": orgID},
	).Scan(&count)
	return count, err
}

// CronFixupFailure describes a cron-scheduled automation whose next_run_at
// could not be recomputed during a bulk resume. The row is still flipped to
// enabled=true by the bulk UPDATE and that change is committed alongside the
// rest of the transaction, but its next_run_at stays NULL so the scheduler
// will not fire it. Callers should surface these so the user knows why a
// "resumed" automation isn't running.
type CronFixupFailure struct {
	AutomationID uuid.UUID
	Reason       string
}

// BulkUpdateEnabled sets enabled state for multiple automations. When pausing,
// next_run_at is cleared so the scheduler skips paused rows. When resuming,
// next_run_at is recomputed so the row doesn't sit dead with a NULL next_run_at
// (which the scheduler's idx_automations_due excludes).
//
// Interval schedules are advanced in-DB via Postgres' interval arithmetic.
// Cron schedules can't be evaluated in SQL, so the resume path falls through to
// a Go-side pass inside the same transaction: we scan the returning rows, pick
// the cron ones, and update each with a locally-computed next_run_at. Keeping
// both branches in a single tx avoids a half-resumed state visible to the
// scheduler between statements.
//
// Returns:
//   - affectedIDs: rows actually mutated (scoped to org_id and not-yet-deleted)
//     so callers can emit audit events only for rows that really changed.
//   - cronFixupFailures: cron rows whose next_run_at could not be computed.
//     The row is still resumed but its next_run_at is NULL, so the scheduler
//     will skip it. Callers should log/surface these — silently dropping them
//     would leave a "resumed" automation that never fires.
//
// Fails closed on empty automationIDs so a caller who forgot to populate the
// list can't silently pause or resume every automation in the org.
func (s *AutomationStore) BulkUpdateEnabled(ctx context.Context, orgID uuid.UUID, automationIDs []uuid.UUID, enabled bool, userID *uuid.UUID) (affectedIDs []uuid.UUID, cronFixupFailures []CronFixupFailure, err error) {
	if len(automationIDs) == 0 {
		return nil, nil, nil
	}

	var pausedBy *uuid.UUID
	var pausedAt *time.Time
	if !enabled {
		pausedBy = userID
		now := time.Now()
		pausedAt = &now
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin bulk-update-enabled tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Interval without interval_run_at: compute next_run_at directly in SQL.
	// Cron and interval rows with interval_run_at: leave next_run_at NULL here
	// and fix up below in Go.
	// Pause: clear next_run_at so the scheduler skips the row.
	//
	// NOTE: the ELSE NULL branch is load-bearing for cron — the Go-side
	// loop below fixes those up before the tx commits. Any *new* schedule
	// kind added to AutomationScheduleType must either extend this CASE or
	// add its own post-update pass, otherwise resuming it here will silently
	// leave next_run_at NULL and the scheduler will skip it forever.
	var nextRunAtExpr string
	if enabled {
		nextRunAtExpr = `CASE
			WHEN schedule_type = 'interval' AND interval_value IS NOT NULL AND interval_unit IS NOT NULL AND interval_run_at IS NULL
			THEN now() + (interval_value::text || ' ' || interval_unit)::interval
			ELSE NULL
		END`
	} else {
		nextRunAtExpr = `NULL`
	}

	// RETURNING only the fields the Go-side fixup pass needs
	// (ComputeNextRunAt reads schedule_type, interval fields, cron fields, timezone). Scanning
	// the full row here would pull 20+ columns back over the wire on every bulk
	// pause/resume for no reason.
	query := fmt.Sprintf(`UPDATE automations SET
			enabled = @enabled,
			paused_by = @paused_by,
			paused_at = @paused_at,
			next_run_at = %s,
			updated_at = now()
		WHERE org_id = @org_id AND deleted_at IS NULL AND id = ANY(@ids)
		RETURNING id, schedule_type, interval_value, interval_unit, interval_run_at, cron_expression, timezone`, nextRunAtExpr)

	rows, err := tx.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"enabled":   enabled,
		"paused_by": pausedBy,
		"paused_at": pausedAt,
		"ids":       automationIDs,
	})
	if err != nil {
		return nil, nil, err
	}
	type affectedRow struct {
		id             uuid.UUID
		scheduleType   models.AutomationScheduleType
		intervalValue  *int
		intervalUnit   *models.ScheduleUnit
		intervalRunAt  *string
		cronExpression *string
		timezone       string
	}
	var affected []affectedRow
	for rows.Next() {
		var r affectedRow
		if err := rows.Scan(&r.id, &r.scheduleType, &r.intervalValue, &r.intervalUnit, &r.intervalRunAt, &r.cronExpression, &r.timezone); err != nil {
			rows.Close()
			return nil, nil, err
		}
		affected = append(affected, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	affectedIDs = make([]uuid.UUID, 0, len(affected))
	now := time.Now()
	for _, r := range affected {
		affectedIDs = append(affectedIDs, r.id)

		// Resume-side fixup for cron rows and interval rows with interval_run_at.
		// On pause we leave next_run_at NULL.
		if !enabled {
			continue
		}
		needsFixup := r.scheduleType == models.AutomationScheduleCron ||
			(r.scheduleType == models.AutomationScheduleInterval && r.intervalRunAt != nil && *r.intervalRunAt != "")
		if !needsFixup {
			continue
		}

		partial := &models.Automation{
			ID:             r.id,
			ScheduleType:   r.scheduleType,
			IntervalValue:  r.intervalValue,
			IntervalUnit:   r.intervalUnit,
			IntervalRunAt:  r.intervalRunAt,
			CronExpression: r.cronExpression,
			Timezone:       r.timezone,
		}
		next, computeErr := partial.ComputeNextRunAt(now)
		if computeErr != nil {
			// Record the failure so the caller can surface it. Don't abort
			// the bulk: a single malformed cron shouldn't block resuming
			// every other automation in the request.
			cronFixupFailures = append(cronFixupFailures, CronFixupFailure{
				AutomationID: r.id,
				Reason:       computeErr.Error(),
			})
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE automations SET next_run_at = @next_run_at, updated_at = now()
				WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
			pgx.NamedArgs{"id": r.id, "org_id": orgID, "next_run_at": next},
		); err != nil {
			return nil, nil, fmt.Errorf("update cron next_run_at: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return affectedIDs, cronFixupFailures, nil
}

// BulkSoftDelete soft-deletes multiple automations and cancels their pending
// runs in the same transaction. Returns the IDs actually affected for audit-
// logging purposes. Fails closed on empty automationIDs to avoid silently
// wiping an entire org's automations.
//
// Running rows are left alone; see SoftDelete for the rationale.
func (s *AutomationStore) BulkSoftDelete(ctx context.Context, orgID uuid.UUID, automationIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(automationIDs) == 0 {
		return nil, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin bulk-soft-delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`UPDATE automations SET deleted_at = now(), enabled = false, updated_at = now()
			WHERE org_id = @org_id AND deleted_at IS NULL AND id = ANY(@ids)
			RETURNING id`,
		pgx.NamedArgs{"org_id": orgID, "ids": automationIDs},
	)
	if err != nil {
		return nil, err
	}
	affected, err := collectIDs(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}

	if len(affected) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE automation_runs SET status = 'skipped',
				completed_at = now(),
				result_summary = 'automation deleted before run could start',
				updated_at = now()
				WHERE automation_id = ANY(@ids) AND status = 'pending'`,
			pgx.NamedArgs{"ids": affected},
		); err != nil {
			return nil, fmt.Errorf("cancel pending runs: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return affected, nil
}

// collectIDs scans a single-column UUID result set.
func collectIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- AutomationRunStore ---

type AutomationRunStore struct {
	db TxStarter
}

func NewAutomationRunStore(db TxStarter) *AutomationRunStore {
	return &AutomationRunStore{db: db}
}

const automationRunColumns = `id, automation_id, org_id, triggered_at, triggered_by,
	triggered_by_user_id, scheduled_time, trigger_id, provider, provider_event_id, trigger_context,
	goal_snapshot, config_snapshot,
	status, capability_snapshot, completed_at, result_summary, created_at, updated_at`

func scanAutomationRun(row pgx.Row) (models.AutomationRun, error) {
	var r models.AutomationRun
	var provider *string
	err := row.Scan(
		&r.ID, &r.AutomationID, &r.OrgID, &r.TriggeredAt, &r.TriggeredBy,
		&r.TriggeredByUserID, &r.ScheduledTime, &r.TriggerID, &provider, &r.ProviderEventID, &r.TriggerContext,
		&r.GoalSnapshot, &r.ConfigSnapshot,
		&r.Status, &r.CapabilitySnapshot, &r.CompletedAt, &r.ResultSummary, &r.CreatedAt, &r.UpdatedAt,
	)
	if provider != nil {
		p := models.AutomationEventProvider(*provider)
		r.Provider = &p
	}
	return r, err
}

// runInserter is the minimal QueryRow surface shared by pgxpool.Pool, pgx.Tx,
// and pgxmock — all used here to insert automation runs. Unifying the pool and
// tx paths through this interface lets CreateRun and CreateRunInTx delegate to
// a single insertRun helper without duplicating the SQL.
type runInserter interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const createAutomationRunSQL = `
	INSERT INTO automation_runs (
		automation_id, org_id, triggered_by, triggered_by_user_id, scheduled_time,
		trigger_id, provider, provider_event_id, trigger_context,
		goal_snapshot, config_snapshot, status, capability_snapshot
	) VALUES (
		@automation_id, @org_id, @triggered_by, @triggered_by_user_id, @scheduled_time,
		@trigger_id, @provider, @provider_event_id, @trigger_context,
		@goal_snapshot, @config_snapshot, @status, @capability_snapshot
	)
	ON CONFLICT DO NOTHING
	RETURNING id, triggered_at, created_at, updated_at`

// insertRun runs the shared INSERT for automation_runs against either a pool
// or a transaction. Returns (false, nil) on conflict — the partial unique
// index only fires when scheduled_time IS NOT NULL (i.e. scheduler-triggered
// runs), so manual runs always insert successfully.
func insertRun(ctx context.Context, q runInserter, r *models.AutomationRun) (bool, error) {
	if r.CapabilitySnapshot == nil {
		r.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{}
	}
	configJSON, err := json.Marshal(r.ConfigSnapshot)
	if err != nil {
		return false, fmt.Errorf("marshal config snapshot: %w", err)
	}
	triggerContext := r.TriggerContext
	if len(triggerContext) == 0 {
		triggerContext = json.RawMessage(`{}`)
	}
	var provider *string
	if r.Provider != nil {
		p := string(*r.Provider)
		provider = &p
	}

	row := q.QueryRow(ctx, createAutomationRunSQL, pgx.NamedArgs{
		"automation_id":        r.AutomationID,
		"org_id":               r.OrgID,
		"triggered_by":         r.TriggeredBy,
		"triggered_by_user_id": r.TriggeredByUserID,
		"scheduled_time":       r.ScheduledTime,
		"trigger_id":           r.TriggerID,
		"provider":             provider,
		"provider_event_id":    r.ProviderEventID,
		"trigger_context":      triggerContext,
		"goal_snapshot":        r.GoalSnapshot,
		"config_snapshot":      configJSON,
		"status":               r.Status,
		"capability_snapshot":  r.CapabilitySnapshot,
	})
	err = row.Scan(&r.ID, &r.TriggeredAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // duplicate — idempotency
	}
	return err == nil, err
}

// CreateRun inserts a new automation run. If scheduled_time is set and a
// duplicate exists (idempotency index), the insert is skipped and false is returned.
func (s *AutomationRunStore) CreateRun(ctx context.Context, r *models.AutomationRun) (bool, error) {
	return insertRun(ctx, s.db, r)
}

// CreateRunInTx inserts a new automation run inside an existing transaction.
func (s *AutomationRunStore) CreateRunInTx(ctx context.Context, tx pgx.Tx, r *models.AutomationRun) (bool, error) {
	return insertRun(ctx, tx, r)
}

func (s *AutomationRunStore) CountRecentProviderTriggerRuns(ctx context.Context, tx pgx.Tx, orgID, automationID, triggerID uuid.UUID, provider models.AutomationEventProvider, since time.Time) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM automation_runs
		WHERE org_id = @org_id
			AND automation_id = @automation_id
			AND trigger_id = @trigger_id
			AND provider = @provider
			AND triggered_at >= @since
			AND status IN ('pending', 'running', 'completed', 'completed_noop', 'failed')`,
		pgx.NamedArgs{
			"org_id":        orgID,
			"automation_id": automationID,
			"trigger_id":    triggerID,
			"provider":      provider,
			"since":         since,
		},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count recent provider trigger runs: %w", err)
	}
	return count, nil
}

func (s *AutomationRunStore) ClaimTriggerDedupe(ctx context.Context, orgID, automationID uuid.UUID, dedupeKey string, expiresAt time.Time) (bool, error) {
	row := s.db.QueryRow(ctx, `
		WITH cleanup AS (
			DELETE FROM automation_trigger_dedupes
			WHERE expires_at <= now()
		)
		INSERT INTO automation_trigger_dedupes (org_id, automation_id, dedupe_key, expires_at)
		VALUES (@org_id, @automation_id, @dedupe_key, @expires_at)
		ON CONFLICT DO NOTHING
		RETURNING dedupe_key`,
		pgx.NamedArgs{
			"org_id":        orgID,
			"automation_id": automationID,
			"dedupe_key":    dedupeKey,
			"expires_at":    expiresAt,
		},
	)
	var inserted string
	err := row.Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim automation trigger dedupe: %w", err)
	}
	return true, nil
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

func (s *AutomationRunStore) GetByRunID(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_runs
		WHERE id = @id AND org_id = @org_id`, automationRunColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
	})
	return scanAutomationRun(row)
}

func (s *AutomationRunStore) CountConsecutiveFailures(ctx context.Context, orgID, automationID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		WITH ordered AS (
			SELECT status,
			       row_number() OVER (ORDER BY triggered_at DESC, id DESC) AS rn
			FROM automation_runs
			WHERE org_id = @org_id
			  AND automation_id = @automation_id
			  AND status IN ('completed', 'completed_noop', 'failed', 'skipped')
		),
		boundary AS (
			SELECT COALESCE(MIN(rn), 2147483647) AS first_non_failed
			FROM ordered
			WHERE status <> 'failed'
		)
		SELECT count(*)
		FROM ordered, boundary
		WHERE status = 'failed'
		  AND rn < boundary.first_non_failed`,
		pgx.NamedArgs{"org_id": orgID, "automation_id": automationID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count consecutive automation failures: %w", err)
	}
	return count, nil
}

type AutomationRunFilters struct {
	Limit  int
	Cursor string
}

// AutomationRunListColumns is the post-projection column list returned by
// ListByAutomation: the lean run columns (config_snapshot intentionally
// dropped to keep the 10s polling row small — the row UI doesn't read it)
// plus a small projection of the spawned session and its newest PR.
//
// Exported so tests in this package and downstream handler tests can build
// pgxmock rows without redefining the list and silently drifting from the
// SQL. Any change to listByAutomationSelectColumns must update this slice
// in lockstep.
var AutomationRunListColumns = []string{
	"id", "automation_id", "org_id", "triggered_at", "triggered_by",
	"triggered_by_user_id", "scheduled_time", "trigger_id", "provider",
	"provider_event_id", "trigger_context", "goal_snapshot",
	"status", "completed_at", "result_summary", "created_at", "updated_at",
	"session_id", "session_title", "session_status",
	"session_diff_stats",
	"session_failure_explanation",
	"session_failure_category",
	"session_failure_next_steps",
	"session_failure_retry_advised",
	"session_pr_creation_state",
	"pr_number", "pr_url", "pr_status", "pr_ci_status",
}

// listByAutomationSelectColumns is the SQL projection matching
// AutomationRunListColumns. Lateral joins keep one row per run (preserving
// keyset pagination on triggered_at, id) while avoiding an N+1 from the
// frontend's polling loop.
//
// Heavy session fields (diff_history, input_manifest) and the run's
// config_snapshot blob are intentionally excluded — this query runs on a
// 10s poll for the open automation page, so the row stays small.
const listByAutomationSelectColumns = `ar.id, ar.automation_id, ar.org_id, ar.triggered_at, ar.triggered_by,
	ar.triggered_by_user_id, ar.scheduled_time, ar.trigger_id, ar.provider,
	ar.provider_event_id, ar.trigger_context, ar.goal_snapshot,
	ar.status, ar.completed_at, ar.result_summary, ar.created_at, ar.updated_at,
	s.id AS session_id, s.title AS session_title, s.status AS session_status,
	s.diff_stats AS session_diff_stats,
	s.failure_explanation AS session_failure_explanation,
	s.failure_category AS session_failure_category,
	s.failure_next_steps AS session_failure_next_steps,
	s.failure_retry_advised AS session_failure_retry_advised,
	s.pr_creation_state AS session_pr_creation_state,
	pr.github_pr_number AS pr_number, pr.github_pr_url AS pr_url,
	pr.status AS pr_status, pr.ci_status AS pr_ci_status`

// listByAutomationFromClause is the FROM + LATERAL JOINs used by
// ListByAutomation. Pulled out as a const so the cursor variants can share
// the same join shape and the query plan stays predictable.
//
// Multiplicity note: session_automation_links has no UNIQUE constraint on
// automation_run_id (the design doc explicitly leaves room for multi-session
// fan-out per run), so we collapse to the most recent session per run via the
// LATERAL + LIMIT 1. The next PR per session likewise picks the most recently
// created — sufficient for the row-level "Review PR →" CTA.
const listByAutomationFromClause = `FROM automation_runs ar
	LEFT JOIN LATERAL (
		SELECT sessions.id, sessions.title, sessions.status, sessions.diff_stats,
			sessions.failure_explanation, sessions.failure_category, sessions.failure_next_steps,
			sessions.failure_retry_advised, sessions.pr_creation_state
		FROM session_automation_links sal
		JOIN sessions
		  ON sessions.org_id = sal.org_id
		 AND sessions.id = sal.session_id
		 AND sessions.deleted_at IS NULL
		WHERE sal.automation_run_id = ar.id AND sal.org_id = ar.org_id
		ORDER BY sessions.created_at DESC
		LIMIT 1
	) s ON true
	LEFT JOIN LATERAL (
		SELECT github_pr_number, github_pr_url, status, ci_status
		FROM pull_requests
		WHERE pull_requests.session_id = s.id AND pull_requests.org_id = ar.org_id
		-- Prefer an open PR over a stale closed/merged one, then break
		-- ties by recency. The ranked status case keeps the row's
		-- "Review PR" CTA pointing at the actionable PR when a session
		-- has been through revisions.
		ORDER BY CASE pull_requests.status WHEN 'open' THEN 0 ELSE 1 END, pull_requests.created_at DESC
		LIMIT 1
	) pr ON true`

func (s *AutomationRunStore) ListByAutomation(ctx context.Context, orgID, automationID uuid.UUID, filters AutomationRunFilters) ([]models.AutomationRun, error) {
	query := fmt.Sprintf(`SELECT %s %s
		WHERE ar.automation_id = @automation_id AND ar.org_id = @org_id`,
		listByAutomationSelectColumns, listByAutomationFromClause)
	args := pgx.NamedArgs{
		"automation_id": automationID,
		"org_id":        orgID,
	}

	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND (ar.triggered_at < (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id) OR (ar.triggered_at = (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id) AND ar.id < @cursor_id))`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY ar.triggered_at DESC, ar.id DESC`

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
	return scanAutomationRunsWithSession(rows)
}

// scanAutomationRunsWithSession decodes rows from the enriched
// ListByAutomation query. Each row is one automation_run plus an optional
// embedded session (and its optional embedded PR), all collapsed by
// LATERAL JOINs upstream so we never have to dedupe runs here.
func scanAutomationRunsWithSession(rows pgx.Rows) ([]models.AutomationRun, error) {
	var runs []models.AutomationRun
	for rows.Next() {
		r, err := scanAutomationRunWithSession(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func scanAutomationRunWithSession(row pgx.Row) (models.AutomationRun, error) {
	var (
		r        models.AutomationRun
		provider *string

		// Session columns. All nullable because the LEFT JOIN may produce
		// NULL for runs that haven't spawned a session yet (pending,
		// skipped, in-flight before the worker creates the session).
		sessionID                  *uuid.UUID
		sessionTitle               *string
		sessionStatus              *models.SessionStatus
		sessionDiffStats           []byte
		sessionFailureExplanation  *string
		sessionFailureCategory     *string
		sessionFailureNextSteps    []string
		sessionFailureRetryAdvised *bool
		sessionPRCreationState     *string

		// PR columns. NULL when the session has no PullRequest row yet —
		// the inner LATERAL is also a LEFT JOIN.
		prNumber   *int
		prURL      *string
		prStatus   *models.PullRequestStatus
		prCIStatus *models.PullRequestCIStatus
	)

	err := row.Scan(
		&r.ID, &r.AutomationID, &r.OrgID, &r.TriggeredAt, &r.TriggeredBy,
		&r.TriggeredByUserID, &r.ScheduledTime, &r.TriggerID, &provider, &r.ProviderEventID, &r.TriggerContext, &r.GoalSnapshot,
		&r.Status, &r.CompletedAt, &r.ResultSummary, &r.CreatedAt, &r.UpdatedAt,
		&sessionID, &sessionTitle, &sessionStatus,
		&sessionDiffStats,
		&sessionFailureExplanation,
		&sessionFailureCategory,
		&sessionFailureNextSteps,
		&sessionFailureRetryAdvised,
		&sessionPRCreationState,
		&prNumber, &prURL, &prStatus, &prCIStatus,
	)
	if err != nil {
		return r, err
	}
	if provider != nil {
		p := models.AutomationEventProvider(*provider)
		r.Provider = &p
	}

	if sessionID != nil {
		// status, failure_retry_advised, and pr_creation_state are NOT
		// NULL on sessions today, so a non-nil sessionID normally implies
		// non-nil scans here. Fall back to zero values defensively so a
		// future migration that relaxes one of those constraints can't
		// turn this scanner into a nil-deref panic in the polling loop.
		s := &models.AutomationRunSession{
			ID:                 *sessionID,
			Title:              sessionTitle,
			DiffStats:          sessionDiffStats,
			FailureExplanation: sessionFailureExplanation,
			FailureCategory:    sessionFailureCategory,
			FailureNextSteps:   sessionFailureNextSteps,
		}
		if sessionStatus != nil {
			s.Status = *sessionStatus
		}
		if sessionFailureRetryAdvised != nil {
			s.FailureRetryAdvised = *sessionFailureRetryAdvised
		}
		if sessionPRCreationState != nil {
			s.PRCreationState = models.PRCreationState(*sessionPRCreationState)
		}
		if prNumber != nil && prURL != nil {
			pr := &models.PRSummary{
				Number: *prNumber,
				URL:    *prURL,
			}
			if prStatus != nil {
				pr.Status = *prStatus
			}
			if prCIStatus != nil {
				pr.CIStatus = *prCIStatus
			}
			s.PR = pr
		}
		r.Session = s
	}

	return r, nil
}

// UpdateStatus updates a run's status. Scoped by org_id so a leaked run UUID
// from another tenant cannot be mutated.
func (s *AutomationRunStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) error {
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

// TransitionStatusIf updates the run's status only if its current status equals
// fromStatus. Returns true when the row was actually updated. Use this to make
// the worker handler's pending → running transition safe under at-least-once
// job delivery: two workers each holding a duplicate job will both pass the
// "is pending?" check, so the transition itself must be the source of truth.
//
// completedAt and resultSummary are preserved via COALESCE when the caller
// passes nil: intermediate transitions (e.g. pending → running) can pass nil
// for both without risk, and a later terminal transition that accidentally
// drops one of them still won't clobber a value written by an earlier writer.
// Pass non-nil values for terminal transitions.
func (s *AutomationRunStore) TransitionStatusIf(ctx context.Context, orgID, runID uuid.UUID, fromStatus, toStatus models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) (bool, error) {
	query := `UPDATE automation_runs
		SET status = @to_status,
			completed_at = COALESCE(@completed_at, completed_at),
			result_summary = COALESCE(@result_summary, result_summary),
			updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status = @from_status`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             runID,
		"org_id":         orgID,
		"from_status":    fromStatus,
		"to_status":      toStatus,
		"completed_at":   completedAt,
		"result_summary": resultSummary,
	})
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListOrgsWithStuckRuns returns the distinct org_ids that have at least one
// pending/running run older than threshold. The scheduler uses this to fan the
// reaper out to one UPDATE per org, so every reap query carries an explicit
// org_id filter — defense-in-depth against a bug elsewhere causing cross-org
// state mutation, and it lets the reaper log per-org counts for audit clarity.
// lint:allow-no-orgid reason="scheduler reaper enumerates orgs with stuck runs across all tenants"
func (s *AutomationRunStore) ListOrgsWithStuckRuns(ctx context.Context, threshold time.Duration) ([]uuid.UUID, error) {
	cutoff := time.Now().Add(-threshold)
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT org_id FROM automation_runs
			WHERE status IN ('pending', 'running') AND triggered_at < @cutoff`,
		pgx.NamedArgs{"cutoff": cutoff},
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs with stuck runs: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// GetStats returns per-day aggregates and window totals for an automation's
// runs. The bucketing uses date_trunc('day', triggered_at) in UTC so every
// client sees the same bucket boundaries regardless of their locale. Days
// with zero runs are omitted from the result set — the UI fills gaps with
// zeros so it stays O(days returned) instead of O(window size).
//
// since/until form a half-open interval [since, until); until=now() if zero.
// Both bounds are required to be UTC times.
func (s *AutomationRunStore) GetStats(ctx context.Context, orgID, automationID uuid.UUID, since, until time.Time) (models.AutomationRunStats, error) {
	if until.IsZero() {
		until = time.Now().UTC()
	}
	if since.IsZero() {
		since = until.Add(-30 * 24 * time.Hour)
	}
	stats := models.AutomationRunStats{Since: since, Until: until}

	// FILTER clauses keep the pivot readable. avg_duration_seconds filters
	// on the same status set the Go-side weighted re-aggregation uses
	// (completed/completed_noop/failed) — skipped rows also get completed_at
	// set by the worker handler, so "WHERE completed_at IS NOT NULL" would
	// pull them into the bucket mean while the Go weight excludes them, and
	// the window-wide avg would drift whenever an automation accumulates
	// skipped runs.
	const bucketQuery = `
		SELECT
			date_trunc('day', triggered_at AT TIME ZONE 'UTC') AS bucket,
			count(*) AS total,
			count(*) FILTER (WHERE status = 'completed') AS completed,
			count(*) FILTER (WHERE status = 'completed_noop') AS completed_noop,
			count(*) FILTER (WHERE status = 'failed') AS failed,
			count(*) FILTER (WHERE status = 'skipped') AS skipped,
			count(*) FILTER (WHERE status = 'running') AS running,
			count(*) FILTER (WHERE status = 'pending') AS pending,
			coalesce(
				avg(GREATEST(EXTRACT(EPOCH FROM (completed_at - triggered_at)), 0))
					FILTER (WHERE status IN ('completed', 'completed_noop', 'failed')
						AND completed_at IS NOT NULL),
				0
			) AS avg_duration_seconds
		FROM automation_runs
		WHERE org_id = @org_id
		  AND automation_id = @automation_id
		  AND triggered_at >= @since
		  AND triggered_at < @until
		GROUP BY bucket
		ORDER BY bucket ASC`

	rows, err := s.db.Query(ctx, bucketQuery, pgx.NamedArgs{
		"org_id":        orgID,
		"automation_id": automationID,
		"since":         since,
		"until":         until,
	})
	if err != nil {
		return stats, fmt.Errorf("query automation run stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var b models.AutomationRunStatsBucket
		if err := rows.Scan(
			&b.Bucket,
			&b.Total,
			&b.Completed,
			&b.CompletedNoop,
			&b.Failed,
			&b.Skipped,
			&b.Running,
			&b.Pending,
			&b.AvgDurationSeconds,
		); err != nil {
			return stats, fmt.Errorf("scan automation run stats: %w", err)
		}
		stats.Buckets = append(stats.Buckets, b)
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}

	// Totals: summing the buckets is cheaper than a second query and keeps
	// bucketed and window-wide numbers consistent by construction.
	t := &stats.Totals
	var durWeightedSum float64
	var durCompletedCount int
	for _, b := range stats.Buckets {
		t.Total += b.Total
		t.Completed += b.Completed
		t.CompletedNoop += b.CompletedNoop
		t.Failed += b.Failed
		t.Skipped += b.Skipped
		t.Running += b.Running
		t.Pending += b.Pending
		// Re-aggregate duration as a weighted mean over completed counts.
		// avg_duration_seconds is over rows with completed_at set, which is
		// (completed + completed_noop + failed) for that bucket. Using
		// completed+noop+failed as the weight matches what the bucket mean
		// was computed over.
		completedLike := b.Completed + b.CompletedNoop + b.Failed
		if completedLike > 0 {
			durWeightedSum += b.AvgDurationSeconds * float64(completedLike)
			durCompletedCount += completedLike
		}
	}
	if denom := t.Completed + t.CompletedNoop + t.Failed; denom > 0 {
		t.SuccessRate = float64(t.Completed+t.CompletedNoop) / float64(denom)
	}
	if durCompletedCount > 0 {
		t.AvgDurationSeconds = durWeightedSum / float64(durCompletedCount)
	}

	return stats, nil
}

// ReapStuckRuns marks pending/running runs older than threshold as failed for
// a single org. Without this, a worker that crashes mid-run would leave a row
// stuck and permanently saturate max_concurrent for that automation —
// CountInFlightRuns counts pending+running, so the scheduler would never fire
// a new run.
//
// Scoped by org_id: the scheduler's reaper pass iterates ListOrgsWithStuckRuns
// and calls this once per org, so no query ever sweeps across tenants. Returns
// the number of runs reaped for this org.
func (s *AutomationRunStore) ReapStuckRuns(ctx context.Context, orgID uuid.UUID, threshold time.Duration) (int64, error) {
	cutoff := time.Now().Add(-threshold)
	summary := "run exceeded execution timeout; marked failed by reaper"
	query := `UPDATE automation_runs
		SET status = 'failed',
		    completed_at = now(),
		    result_summary = @summary,
		    updated_at = now()
		WHERE org_id = @org_id
		  AND status IN ('pending', 'running')
		  AND triggered_at < @cutoff`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"summary": summary,
		"cutoff":  cutoff,
	})
	if err != nil {
		return 0, fmt.Errorf("reap stuck automation runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
