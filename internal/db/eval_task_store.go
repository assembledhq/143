package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type EvalTaskStore struct {
	db DBTX
}

func NewEvalTaskStore(db DBTX) *EvalTaskStore {
	return &EvalTaskStore{db: db}
}

const evalTaskColumns = `id, org_id, repo_id, name, description,
	base_commit_sha, solution_commit_sha, solution_diff,
	issue_description, issue_context,
	server_deploy_sha, pm_document_set_pin_id, org_settings_version_id,
	memory_snapshot, sandbox_image_digest, context_overrides,
	scoring_criteria, pass_threshold,
	source, source_pr_number, complexity, tags,
	snapshot_broken,
	created_by, created_at, updated_at, archived_at`

func scanEvalTask(row pgx.Row) (models.EvalTask, error) {
	var t models.EvalTask
	err := row.Scan(
		&t.ID, &t.OrgID, &t.RepoID, &t.Name, &t.Description,
		&t.BaseCommitSHA, &t.SolutionCommitSHA, &t.SolutionDiff,
		&t.IssueDescription, &t.IssueContext,
		&t.ServerDeploySHA, &t.PMDocumentSetPinID, &t.OrgSettingsVersionID,
		&t.MemorySnapshot, &t.SandboxImageDigest, &t.ContextOverrides,
		&t.ScoringCriteria, &t.PassThreshold,
		&t.Source, &t.SourcePRNumber, &t.Complexity, &t.Tags,
		&t.SnapshotBroken,
		&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt,
	)
	return t, err
}

func scanEvalTasks(rows pgx.Rows) ([]models.EvalTask, error) {
	var tasks []models.EvalTask
	for rows.Next() {
		var t models.EvalTask
		err := rows.Scan(
			&t.ID, &t.OrgID, &t.RepoID, &t.Name, &t.Description,
			&t.BaseCommitSHA, &t.SolutionCommitSHA, &t.SolutionDiff,
			&t.IssueDescription, &t.IssueContext,
			&t.ServerDeploySHA, &t.PMDocumentSetPinID, &t.OrgSettingsVersionID,
			&t.MemorySnapshot, &t.SandboxImageDigest, &t.ContextOverrides,
			&t.ScoringCriteria, &t.PassThreshold,
			&t.Source, &t.SourcePRNumber, &t.Complexity, &t.Tags,
			&t.SnapshotBroken,
			&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan eval task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *EvalTaskStore) Create(ctx context.Context, task *models.EvalTask) error {
	query := fmt.Sprintf(`INSERT INTO eval_tasks (
		org_id, repo_id, name, description,
		base_commit_sha, solution_commit_sha, solution_diff,
		issue_description, issue_context,
		server_deploy_sha, pm_document_set_pin_id, org_settings_version_id,
		memory_snapshot, sandbox_image_digest, context_overrides,
		scoring_criteria, pass_threshold,
		source, source_pr_number, complexity, tags, created_by
	) VALUES (
		@org_id, @repo_id, @name, @description,
		@base_commit_sha, @solution_commit_sha, @solution_diff,
		@issue_description, @issue_context,
		@server_deploy_sha, @pm_document_set_pin_id, @org_settings_version_id,
		@memory_snapshot, @sandbox_image_digest, @context_overrides,
		@scoring_criteria, @pass_threshold,
		@source, @source_pr_number, @complexity, @tags, @created_by
	) RETURNING %s`, evalTaskColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                  task.OrgID,
		"repo_id":                 task.RepoID,
		"name":                    task.Name,
		"description":             task.Description,
		"base_commit_sha":         task.BaseCommitSHA,
		"solution_commit_sha":     task.SolutionCommitSHA,
		"solution_diff":           task.SolutionDiff,
		"issue_description":       task.IssueDescription,
		"issue_context":           task.IssueContext,
		"server_deploy_sha":       task.ServerDeploySHA,
		"pm_document_set_pin_id":  task.PMDocumentSetPinID,
		"org_settings_version_id": task.OrgSettingsVersionID,
		"memory_snapshot":         task.MemorySnapshot,
		"sandbox_image_digest":    task.SandboxImageDigest,
		"context_overrides":       task.ContextOverrides,
		"scoring_criteria":        task.ScoringCriteria,
		"pass_threshold":          task.PassThreshold,
		"source":                  task.Source,
		"source_pr_number":        task.SourcePRNumber,
		"complexity":              task.Complexity,
		"tags":                    task.Tags,
		"created_by":              task.CreatedBy,
	})

	scanned, err := scanEvalTask(row)
	if err != nil {
		return fmt.Errorf("create eval task: %w", err)
	}
	*task = scanned
	return nil
}

func (s *EvalTaskStore) GetByID(ctx context.Context, orgID, taskID uuid.UUID) (models.EvalTask, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_tasks WHERE id = @id AND org_id = @org_id`, evalTaskColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     taskID,
		"org_id": orgID,
	})
	return scanEvalTask(row)
}

func (s *EvalTaskStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters models.EvalTaskListFilters) ([]models.EvalTask, error) {
	var extraConditions []string
	args := pgx.NamedArgs{
		"org_id": orgID,
	}

	// Default to non-archived unless explicitly requesting archived
	if filters.Archived != nil && *filters.Archived {
		extraConditions = append(extraConditions, "AND archived_at IS NOT NULL")
	} else {
		extraConditions = append(extraConditions, "AND archived_at IS NULL")
	}

	if filters.Source != nil {
		extraConditions = append(extraConditions, "AND source = @source")
		args["source"] = *filters.Source
	}

	if filters.Complexity != nil {
		extraConditions = append(extraConditions, "AND complexity = @complexity")
		args["complexity"] = *filters.Complexity
	}

	if len(filters.Tags) > 0 {
		extraConditions = append(extraConditions, "AND tags && @tags")
		args["tags"] = filters.Tags
	}

	if filters.Cursor != nil {
		extraConditions = append(extraConditions, "AND created_at < @cursor")
		args["cursor"] = *filters.Cursor
	}

	limit := 50
	if filters.Limit > 0 && filters.Limit <= 100 {
		limit = filters.Limit
	}
	args["limit"] = limit

	query := fmt.Sprintf(`SELECT %s FROM eval_tasks WHERE org_id = @org_id %s ORDER BY created_at DESC LIMIT @limit`,
		evalTaskColumns, strings.Join(extraConditions, " "))

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("list eval tasks: %w", err)
	}
	defer rows.Close()
	return scanEvalTasks(rows)
}

func (s *EvalTaskStore) Update(ctx context.Context, task *models.EvalTask) error {
	query := fmt.Sprintf(`UPDATE eval_tasks SET
		name = @name, description = @description,
		issue_description = @issue_description, issue_context = @issue_context,
		scoring_criteria = @scoring_criteria, pass_threshold = @pass_threshold,
		complexity = @complexity, tags = @tags,
		context_overrides = @context_overrides,
		updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING %s`, evalTaskColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":                task.ID,
		"org_id":            task.OrgID,
		"name":              task.Name,
		"description":       task.Description,
		"issue_description": task.IssueDescription,
		"issue_context":     task.IssueContext,
		"scoring_criteria":  task.ScoringCriteria,
		"pass_threshold":    task.PassThreshold,
		"complexity":        task.Complexity,
		"tags":              task.Tags,
		"context_overrides": task.ContextOverrides,
	})

	scanned, err := scanEvalTask(row)
	if err != nil {
		return fmt.Errorf("update eval task: %w", err)
	}
	*task = scanned
	return nil
}

func (s *EvalTaskStore) Archive(ctx context.Context, orgID, taskID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_tasks SET archived_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND archived_at IS NULL`,
		pgx.NamedArgs{"id": taskID, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("archive eval task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// MarkSnapshotBroken sets the snapshot_broken flag on a task (e.g., when the
// base_commit_sha is no longer reachable after a force-push).
func (s *EvalTaskStore) MarkSnapshotBroken(ctx context.Context, orgID, taskID uuid.UUID, broken bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE eval_tasks SET snapshot_broken = @broken, updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": taskID, "org_id": orgID, "broken": broken},
	)
	if err != nil {
		return fmt.Errorf("mark snapshot broken: %w", err)
	}
	return nil
}

// CountByIDs returns how many of the given task IDs exist for this org (non-archived).
// Used to validate batch task IDs in a single query instead of N+1 GetByID calls.
func (s *EvalTaskStore) CountByIDs(ctx context.Context, orgID uuid.UUID, taskIDs []uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM eval_tasks WHERE org_id = @org_id AND id = ANY(@task_ids) AND archived_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "task_ids": taskIDs},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count eval tasks by ids: %w", err)
	}
	return count, nil
}

func (s *EvalTaskStore) CountByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM eval_tasks WHERE org_id = @org_id AND archived_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count eval tasks: %w", err)
	}
	return count, nil
}

// LatestRunScores returns the most recent run's final_score for each task,
// keyed by task ID. Used for the eval task list view summary.
func (s *EvalTaskStore) LatestRunScores(ctx context.Context, orgID uuid.UUID, taskIDs []uuid.UUID) (map[uuid.UUID]*float64, error) {
	if len(taskIDs) == 0 {
		return map[uuid.UUID]*float64{}, nil
	}

	query := `SELECT DISTINCT ON (task_id) task_id, final_score
		FROM eval_runs
		WHERE org_id = @org_id AND task_id = ANY(@task_ids) AND status = 'completed'
		ORDER BY task_id, created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"task_ids": taskIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("latest run scores: %w", err)
	}
	defer rows.Close()

	scores := make(map[uuid.UUID]*float64, len(taskIDs))
	for rows.Next() {
		var taskID uuid.UUID
		var score *float64
		if err := rows.Scan(&taskID, &score); err != nil {
			return nil, err
		}
		scores[taskID] = score
	}
	return scores, rows.Err()
}

// RunCountByTask returns the number of completed runs per task, keyed by task ID.
func (s *EvalTaskStore) RunCountByTask(ctx context.Context, orgID uuid.UUID, taskIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	if len(taskIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}

	query := `SELECT task_id, COUNT(*)
		FROM eval_runs
		WHERE org_id = @org_id AND task_id = ANY(@task_ids)
		GROUP BY task_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"task_ids": taskIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("run count by task: %w", err)
	}
	defer rows.Close()

	counts := make(map[uuid.UUID]int, len(taskIDs))
	for rows.Next() {
		var taskID uuid.UUID
		var count int
		if err := rows.Scan(&taskID, &count); err != nil {
			return nil, err
		}
		counts[taskID] = count
	}
	return counts, rows.Err()
}
