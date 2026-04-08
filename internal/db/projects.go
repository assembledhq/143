package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ProjectStore struct {
	db TxStarter
}

func NewProjectStore(db TxStarter) *ProjectStore {
	return &ProjectStore{db: db}
}

type ProjectFilters struct {
	Status       string
	Limit        int
	Cursor       string
	RepositoryID uuid.UUID
}

// projectColumns is the column list shared across all project queries.
const projectColumns = `id, org_id, repository_id, title, goal, scope, completion_criteria,
	status, priority, execution_mode, max_concurrent, auto_merge, base_branch,
	current_phase, lessons_learned, approach_history,
	total_tasks, completed_tasks, failed_tasks,
	proposed_by_pm, source_issue_ids, proposal_reasoning, similar_projects,
	agent_type, model_override,
	schedule_enabled, schedule_interval, schedule_unit, next_run_at,
	created_by, deleted_at, created_at, updated_at, completed_at`

func scanProject(row pgx.Row) (models.Project, error) {
	var p models.Project
	var lessonsRaw, approachRaw []byte
	var sourceIssueIDs []uuid.UUID

	err := row.Scan(
		&p.ID, &p.OrgID, &p.RepositoryID, &p.Title, &p.Goal, &p.Scope, &p.CompletionCriteria,
		&p.Status, &p.Priority, &p.ExecutionMode, &p.MaxConcurrent, &p.AutoMerge, &p.BaseBranch,
		&p.CurrentPhase, &lessonsRaw, &approachRaw,
		&p.TotalTasks, &p.CompletedTasks, &p.FailedTasks,
		&p.ProposedByPM, &sourceIssueIDs, &p.ProposalReasoning, &p.SimilarProjects,
		&p.AgentType, &p.ModelOverride,
		&p.ScheduleEnabled, &p.ScheduleInterval, &p.ScheduleUnit, &p.NextRunAt,
		&p.CreatedBy, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
	)
	if err != nil {
		return models.Project{}, err
	}

	if len(lessonsRaw) > 0 {
		if err := json.Unmarshal(lessonsRaw, &p.LessonsLearned); err != nil {
			return models.Project{}, fmt.Errorf("unmarshal lessons_learned: %w", err)
		}
	}
	if len(approachRaw) > 0 {
		if err := json.Unmarshal(approachRaw, &p.ApproachHistory); err != nil {
			return models.Project{}, fmt.Errorf("unmarshal approach_history: %w", err)
		}
	}
	p.SourceIssueIDs = sourceIssueIDs

	return p, nil
}

func scanProjects(rows pgx.Rows) ([]models.Project, error) {
	var projects []models.Project
	for rows.Next() {
		var p models.Project
		var lessonsRaw, approachRaw []byte
		var sourceIssueIDs []uuid.UUID

		err := rows.Scan(
			&p.ID, &p.OrgID, &p.RepositoryID, &p.Title, &p.Goal, &p.Scope, &p.CompletionCriteria,
			&p.Status, &p.Priority, &p.ExecutionMode, &p.MaxConcurrent, &p.AutoMerge, &p.BaseBranch,
			&p.CurrentPhase, &lessonsRaw, &approachRaw,
			&p.TotalTasks, &p.CompletedTasks, &p.FailedTasks,
			&p.ProposedByPM, &sourceIssueIDs, &p.ProposalReasoning, &p.SimilarProjects,
			&p.AgentType, &p.ModelOverride,
			&p.ScheduleEnabled, &p.ScheduleInterval, &p.ScheduleUnit, &p.NextRunAt,
			&p.CreatedBy, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
		)
		if err != nil {
			return nil, err
		}

		if len(lessonsRaw) > 0 {
			if err := json.Unmarshal(lessonsRaw, &p.LessonsLearned); err != nil {
				return nil, fmt.Errorf("unmarshal lessons_learned: %w", err)
			}
		}
		if len(approachRaw) > 0 {
			if err := json.Unmarshal(approachRaw, &p.ApproachHistory); err != nil {
				return nil, fmt.Errorf("unmarshal approach_history: %w", err)
			}
		}
		p.SourceIssueIDs = sourceIssueIDs

		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projects, nil
}

func (s *ProjectStore) Create(ctx context.Context, p *models.Project) error {
	lessonsJSON, err := json.Marshal(p.LessonsLearned)
	if err != nil || p.LessonsLearned == nil {
		lessonsJSON = []byte("[]")
	}
	approachJSON, err := json.Marshal(p.ApproachHistory)
	if err != nil || p.ApproachHistory == nil {
		approachJSON = []byte("[]")
	}
	similarJSON := p.SimilarProjects
	if len(similarJSON) == 0 {
		similarJSON = []byte("[]")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		INSERT INTO projects (
			org_id, repository_id, title, goal, scope, completion_criteria,
			status, priority, execution_mode, max_concurrent, auto_merge, base_branch,
			current_phase, lessons_learned, approach_history,
			proposed_by_pm, source_issue_ids, proposal_reasoning, similar_projects, created_by,
			agent_type, model_override,
			schedule_enabled, schedule_interval, schedule_unit, next_run_at
		)
		VALUES (
			@org_id, @repository_id, @title, @goal, @scope, @completion_criteria,
			@status, @priority, @execution_mode, @max_concurrent, @auto_merge, @base_branch,
			@current_phase, @lessons_learned, @approach_history,
			@proposed_by_pm, @source_issue_ids, @proposal_reasoning, @similar_projects, @created_by,
			@agent_type, @model_override,
			@schedule_enabled, @schedule_interval, @schedule_unit, @next_run_at
		)
		RETURNING id, created_at, updated_at`

	row := tx.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              p.OrgID,
		"repository_id":       p.RepositoryID,
		"title":               p.Title,
		"goal":                p.Goal,
		"scope":               p.Scope,
		"completion_criteria": p.CompletionCriteria,
		"status":              p.Status,
		"priority":            p.Priority,
		"execution_mode":      p.ExecutionMode,
		"max_concurrent":      p.MaxConcurrent,
		"auto_merge":          p.AutoMerge,
		"base_branch":         p.BaseBranch,
		"current_phase":       p.CurrentPhase,
		"lessons_learned":     lessonsJSON,
		"approach_history":    approachJSON,
		"proposed_by_pm":      p.ProposedByPM,
		"source_issue_ids":    p.SourceIssueIDs,
		"proposal_reasoning":  p.ProposalReasoning,
		"similar_projects":    similarJSON,
		"created_by":          p.CreatedBy,
		"agent_type":          p.AgentType,
		"model_override":      p.ModelOverride,
		"schedule_enabled":    p.ScheduleEnabled,
		"schedule_interval":   p.ScheduleInterval,
		"schedule_unit":       p.ScheduleUnit,
		"next_run_at":         p.NextRunAt,
	})
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return err
	}

	// Dual-write: populate the join table for source issue references.
	for _, issueID := range p.SourceIssueIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO project_source_issues (project_id, issue_id) VALUES (@project_id, @issue_id) ON CONFLICT DO NOTHING`,
			pgx.NamedArgs{"project_id": p.ID, "issue_id": issueID}); err != nil {
			return fmt.Errorf("sync source issue: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *ProjectStore) GetByID(ctx context.Context, orgID, projectID uuid.UUID) (models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`, projectColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
	})
	return scanProject(row)
}

func (s *ProjectStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters ProjectFilters) ([]models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE org_id = @org_id AND deleted_at IS NULL`, projectColumns)
	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}
	if filters.RepositoryID != uuid.Nil {
		query += ` AND repository_id = @repository_id`
		args["repository_id"] = &filters.RepositoryID
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += `
				AND (
					priority > (SELECT priority FROM projects WHERE id = @cursor_id AND org_id = @org_id)
					OR (
						priority = (SELECT priority FROM projects WHERE id = @cursor_id AND org_id = @org_id)
						AND created_at < (SELECT created_at FROM projects WHERE id = @cursor_id AND org_id = @org_id)
					)
					OR (
						priority = (SELECT priority FROM projects WHERE id = @cursor_id AND org_id = @org_id)
						AND created_at = (SELECT created_at FROM projects WHERE id = @cursor_id AND org_id = @org_id)
						AND id < @cursor_id
					)
				)`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY priority ASC, created_at DESC, id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	return scanProjects(rows)
}

func (s *ProjectStore) Update(ctx context.Context, p *models.Project) error {
	lessonsJSON, err := json.Marshal(p.LessonsLearned)
	if err != nil || p.LessonsLearned == nil {
		lessonsJSON = []byte("[]")
	}
	approachJSON, err := json.Marshal(p.ApproachHistory)
	if err != nil || p.ApproachHistory == nil {
		approachJSON = []byte("[]")
	}
	similarJSON := p.SimilarProjects
	if len(similarJSON) == 0 {
		similarJSON = []byte("[]")
	}

	query := `
		UPDATE projects SET
			title = @title, goal = @goal, scope = @scope, completion_criteria = @completion_criteria,
			status = @status, priority = @priority, execution_mode = @execution_mode,
			max_concurrent = @max_concurrent, auto_merge = @auto_merge, base_branch = @base_branch,
			current_phase = @current_phase, lessons_learned = @lessons_learned,
			approach_history = @approach_history,
			similar_projects = @similar_projects,
			total_tasks = @total_tasks, completed_tasks = @completed_tasks, failed_tasks = @failed_tasks,
			schedule_enabled = @schedule_enabled, schedule_interval = @schedule_interval,
			schedule_unit = @schedule_unit, next_run_at = @next_run_at,
			completed_at = @completed_at, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	_, err = s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                  p.ID,
		"org_id":              p.OrgID,
		"title":               p.Title,
		"goal":                p.Goal,
		"scope":               p.Scope,
		"completion_criteria": p.CompletionCriteria,
		"status":              p.Status,
		"priority":            p.Priority,
		"execution_mode":      p.ExecutionMode,
		"max_concurrent":      p.MaxConcurrent,
		"auto_merge":          p.AutoMerge,
		"base_branch":         p.BaseBranch,
		"current_phase":       p.CurrentPhase,
		"lessons_learned":     lessonsJSON,
		"approach_history":    approachJSON,
		"similar_projects":    similarJSON,
		"total_tasks":         p.TotalTasks,
		"completed_tasks":     p.CompletedTasks,
		"failed_tasks":        p.FailedTasks,
		"schedule_enabled":    p.ScheduleEnabled,
		"schedule_interval":   p.ScheduleInterval,
		"schedule_unit":       p.ScheduleUnit,
		"next_run_at":         p.NextRunAt,
		"completed_at":        p.CompletedAt,
	})
	return err
}

func (s *ProjectStore) UpdateProgress(ctx context.Context, orgID, projectID uuid.UUID) error {
	query := `
		UPDATE projects SET
			total_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id),
			completed_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id AND status = 'completed'),
			failed_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = @project_id AND org_id = @org_id AND status = 'failed'),
			updated_at = now()
		WHERE id = @project_id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"project_id": projectID,
		"org_id":     orgID,
	})
	return err
}

// ListDueForSchedule returns active projects with scheduling enabled that are due to run.
func (s *ProjectStore) ListDueForSchedule(ctx context.Context, now time.Time) ([]models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects
		WHERE schedule_enabled = true AND status = 'active' AND deleted_at IS NULL AND next_run_at IS NOT NULL AND next_run_at <= @now
		ORDER BY next_run_at ASC
		LIMIT 100`, projectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"now": now})
	if err != nil {
		return nil, fmt.Errorf("query due projects: %w", err)
	}
	defer rows.Close()

	return scanProjects(rows)
}

// UpdateNextRunAt sets the next scheduled run time for a project.
func (s *ProjectStore) UpdateNextRunAt(ctx context.Context, orgID, projectID uuid.UUID, nextRunAt time.Time) error {
	query := `UPDATE projects SET next_run_at = @next_run_at, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":          projectID,
		"org_id":      orgID,
		"next_run_at": nextRunAt,
	})
	return err
}

func (s *ProjectStore) UpdateStatus(ctx context.Context, orgID, projectID uuid.UUID, status string) error {
	query := `UPDATE projects SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	if status == "completed" || status == "cancelled" {
		query = `UPDATE projects SET status = @status, completed_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	}

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

// CountByOrgStatus counts projects matching the given org and statuses (across all repos).
func (s *ProjectStore) CountByOrgStatus(ctx context.Context, orgID uuid.UUID, statuses []string) (int, error) {
	query := `SELECT count(*) FROM projects WHERE org_id = @org_id AND status = ANY(@statuses) AND deleted_at IS NULL`
	var count int
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"statuses": statuses,
	}).Scan(&count)
	return count, err
}

// CountByOrgRepoStatus counts projects matching the given org, repo, and statuses.
func (s *ProjectStore) CountByOrgRepoStatus(ctx context.Context, orgID, repoID uuid.UUID, statuses []string) (int, error) {
	query := `SELECT count(*) FROM projects WHERE org_id = @org_id AND repository_id = @repo_id AND status = ANY(@statuses) AND deleted_at IS NULL`
	var count int
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"repo_id":  repoID,
		"statuses": statuses,
	}).Scan(&count)
	return count, err
}

// ListByOrgRepoStatuses returns projects matching the given org, repo, and statuses.
func (s *ProjectStore) ListByOrgRepoStatuses(ctx context.Context, orgID, repoID uuid.UUID, statuses []string) ([]models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE org_id = @org_id AND repository_id = @repo_id AND status = ANY(@statuses) AND deleted_at IS NULL ORDER BY priority ASC, created_at DESC`, projectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"repo_id":  repoID,
		"statuses": statuses,
	})
	if err != nil {
		return nil, fmt.Errorf("query projects by repo/statuses: %w", err)
	}
	defer rows.Close()
	return scanProjects(rows)
}

// SoftDelete marks a project as deleted without removing the row.
// Uses db.Exec directly (not a transaction) since this is a single atomic UPDATE.
func (s *ProjectStore) SoftDelete(ctx context.Context, orgID, projectID uuid.UUID) error {
	query := `UPDATE projects SET deleted_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found or already deleted")
	}
	return nil
}
