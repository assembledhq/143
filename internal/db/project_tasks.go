package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ProjectTaskStore struct {
	db TxStarter
}

func NewProjectTaskStore(db TxStarter) *ProjectTaskStore {
	return &ProjectTaskStore{db: db}
}

type ProjectTaskFilters struct {
	Status string
}

const projectTaskColumns = `id, project_id, org_id, title, description, approach, reasoning,
	sort_order, depends_on, batch_number, status, complexity, confidence,
	session_id, issue_id, branch_name, pr_url, outcome_notes,
	retry_count, max_retries, created_at, updated_at, completed_at`

func scanProjectTask(row pgx.Row) (models.ProjectTask, error) {
	var t models.ProjectTask
	var dependsOn []uuid.UUID

	err := row.Scan(
		&t.ID, &t.ProjectID, &t.OrgID, &t.Title, &t.Description, &t.Approach, &t.Reasoning,
		&t.SortOrder, &dependsOn, &t.BatchNumber, &t.Status, &t.Complexity, &t.Confidence,
		&t.SessionID, &t.IssueID, &t.BranchName, &t.PRURL, &t.OutcomeNotes,
		&t.RetryCount, &t.MaxRetries, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
	)
	if err != nil {
		return models.ProjectTask{}, err
	}
	t.DependsOn = dependsOn
	return t, nil
}

func scanProjectTasks(rows pgx.Rows) ([]models.ProjectTask, error) {
	var tasks []models.ProjectTask
	for rows.Next() {
		var t models.ProjectTask
		var dependsOn []uuid.UUID

		err := rows.Scan(
			&t.ID, &t.ProjectID, &t.OrgID, &t.Title, &t.Description, &t.Approach, &t.Reasoning,
			&t.SortOrder, &dependsOn, &t.BatchNumber, &t.Status, &t.Complexity, &t.Confidence,
			&t.SessionID, &t.IssueID, &t.BranchName, &t.PRURL, &t.OutcomeNotes,
			&t.RetryCount, &t.MaxRetries, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		t.DependsOn = dependsOn
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *ProjectTaskStore) Create(ctx context.Context, t *models.ProjectTask) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		INSERT INTO project_tasks (
			project_id, org_id, title, description, approach, reasoning,
			sort_order, depends_on, batch_number, status, complexity, confidence,
			session_id, issue_id, branch_name, pr_url
		)
		VALUES (
			@project_id, @org_id, @title, @description, @approach, @reasoning,
			@sort_order, @depends_on, @batch_number, @status, @complexity, @confidence,
			@session_id, @issue_id, @branch_name, @pr_url
		)
		RETURNING id, created_at, updated_at`

	row := tx.QueryRow(ctx, query, pgx.NamedArgs{
		"project_id":   t.ProjectID,
		"org_id":       t.OrgID,
		"title":        t.Title,
		"description":  t.Description,
		"approach":     t.Approach,
		"reasoning":    t.Reasoning,
		"sort_order":   t.SortOrder,
		"depends_on":   t.DependsOn,
		"batch_number": t.BatchNumber,
		"status":       t.Status,
		"complexity":   t.Complexity,
		"confidence":   t.Confidence,
		"session_id":   t.SessionID,
		"issue_id":     t.IssueID,
		"branch_name":  t.BranchName,
		"pr_url":       t.PRURL,
	})
	if err := row.Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}

	// Dual-write: also populate the join table for referential integrity.
	if err := syncTaskDependencies(ctx, tx, t.ID, t.DependsOn); err != nil {
		return fmt.Errorf("sync task dependencies: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *ProjectTaskStore) GetByID(ctx context.Context, orgID, taskID uuid.UUID) (models.ProjectTask, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_tasks WHERE id = @id AND org_id = @org_id`, projectTaskColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     taskID,
		"org_id": orgID,
	})
	return scanProjectTask(row)
}

func (s *ProjectTaskStore) ListByProject(ctx context.Context, orgID, projectID uuid.UUID, filters ProjectTaskFilters) ([]models.ProjectTask, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id`, projectTaskColumns)
	args := pgx.NamedArgs{
		"project_id": projectID,
		"org_id":     orgID,
	}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}

	query += ` ORDER BY batch_number ASC, sort_order ASC`

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query project tasks: %w", err)
	}
	defer rows.Close()

	return scanProjectTasks(rows)
}

func (s *ProjectTaskStore) Update(ctx context.Context, t *models.ProjectTask) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		UPDATE project_tasks SET
			title = @title, description = @description, approach = @approach, reasoning = @reasoning,
			sort_order = @sort_order, depends_on = @depends_on, status = @status,
			complexity = @complexity, confidence = @confidence,
			session_id = @session_id, issue_id = @issue_id,
			branch_name = @branch_name, pr_url = @pr_url,
			outcome_notes = @outcome_notes, retry_count = @retry_count,
			completed_at = @completed_at, updated_at = now()
		WHERE id = @id AND org_id = @org_id`

	_, err = tx.Exec(ctx, query, pgx.NamedArgs{
		"id":            t.ID,
		"org_id":        t.OrgID,
		"title":         t.Title,
		"description":   t.Description,
		"approach":      t.Approach,
		"reasoning":     t.Reasoning,
		"sort_order":    t.SortOrder,
		"depends_on":    t.DependsOn,
		"status":        t.Status,
		"complexity":    t.Complexity,
		"confidence":    t.Confidence,
		"session_id":    t.SessionID,
		"issue_id":      t.IssueID,
		"branch_name":   t.BranchName,
		"pr_url":        t.PRURL,
		"outcome_notes": t.OutcomeNotes,
		"retry_count":   t.RetryCount,
		"completed_at":  t.CompletedAt,
	})
	if err != nil {
		return err
	}

	// Dual-write: sync the join table to match the updated depends_on array.
	if err := syncTaskDependencies(ctx, tx, t.ID, t.DependsOn); err != nil {
		return fmt.Errorf("sync task dependencies: %w", err)
	}

	return tx.Commit(ctx)
}

// syncTaskDependencies replaces the join table rows for a task's dependencies.
func syncTaskDependencies(ctx context.Context, db DBTX, taskID uuid.UUID, dependsOn []uuid.UUID) error {
	// Clear existing dependencies.
	if _, err := db.Exec(ctx, `DELETE FROM project_task_dependencies WHERE task_id = @task_id`,
		pgx.NamedArgs{"task_id": taskID}); err != nil {
		return err
	}

	if len(dependsOn) == 0 {
		return nil
	}

	// Batch insert all dependencies in a single statement.
	_, err := db.Exec(ctx,
		`INSERT INTO project_task_dependencies (task_id, depends_on_id)
		 SELECT @task_id, unnest(@depends_on_ids::uuid[])`,
		pgx.NamedArgs{"task_id": taskID, "depends_on_ids": dependsOn})
	return err
}

func (s *ProjectTaskStore) Delete(ctx context.Context, orgID, taskID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM project_tasks WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": taskID, "org_id": orgID},
	)
	return err
}

func (s *ProjectTaskStore) CountByProjectAndStatus(ctx context.Context, orgID, projectID uuid.UUID, status models.ProjectTaskStatus) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id AND status = @status`,
		pgx.NamedArgs{"project_id": projectID, "org_id": orgID, "status": string(status)},
	).Scan(&count)
	return count, err
}

func (s *ProjectTaskStore) GetMaxBatchNumber(ctx context.Context, orgID, projectID uuid.UUID) (int, error) {
	var maxBatch *int
	err := s.db.QueryRow(ctx,
		`SELECT max(batch_number) FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id`,
		pgx.NamedArgs{"project_id": projectID, "org_id": orgID},
	).Scan(&maxBatch)
	if err != nil {
		return 0, err
	}
	if maxBatch == nil {
		return 0, nil
	}
	return *maxBatch, nil
}
