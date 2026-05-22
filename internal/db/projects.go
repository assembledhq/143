package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Status          string
	Limit           int
	Cursor          string
	RepositoryID    uuid.UUID
	CreatedBy       uuid.UUID
	CreatedByIDs    []uuid.UUID
	Search          string // When non-empty, filter projects by title or goal (case-insensitive substring match).
	ProposedByPM    *bool  // When non-nil, filter by proposed_by_pm flag.
	IncludeArchived bool
	OnlyArchived    bool
}

// projectColumns is the column list shared across all project queries.
const projectColumns = `id, org_id, repository_id, title, goal, scope, completion_criteria,
	status, priority, execution_mode, max_concurrent, auto_merge, base_branch,
	current_phase, lessons_learned, approach_history,
	total_tasks, completed_tasks, failed_tasks,
	proposed_by_pm, source_issue_ids, proposal_reasoning, similar_projects,
	agent_type, model_override,
	created_by, deleted_at, created_at, updated_at, completed_at, archived_at`

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
		&p.CreatedBy, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt, &p.ArchivedAt,
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
			&p.CreatedBy, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt, &p.ArchivedAt,
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
			agent_type, model_override
		)
		VALUES (
			@org_id, @repository_id, @title, @goal, @scope, @completion_criteria,
			@status, @priority, @execution_mode, @max_concurrent, @auto_merge, @base_branch,
			@current_phase, @lessons_learned, @approach_history,
			@proposed_by_pm, @source_issue_ids, @proposal_reasoning, @similar_projects, @created_by,
			@agent_type, @model_override
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

// applyProjectFilters appends WHERE clauses for the common filter fields and
// populates the corresponding named args. It returns the extended query string.
func applyProjectFilters(query string, args pgx.NamedArgs, filters ProjectFilters) string {
	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}
	if filters.RepositoryID != uuid.Nil {
		query += ` AND repository_id = @repository_id`
		args["repository_id"] = &filters.RepositoryID
	}
	if len(filters.CreatedByIDs) > 0 {
		query += ` AND created_by = ANY(@created_by_ids)`
		args["created_by_ids"] = filters.CreatedByIDs
	} else if filters.CreatedBy != uuid.Nil {
		query += ` AND created_by = @created_by`
		args["created_by"] = filters.CreatedBy
	}
	if filters.ProposedByPM != nil {
		query += ` AND proposed_by_pm = @proposed_by_pm`
		args["proposed_by_pm"] = *filters.ProposedByPM
	}
	if filters.Search != "" {
		query += ` AND (title ILIKE @search OR goal ILIKE @search)`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filters.Search)
		args["search"] = "%" + escaped + "%"
	}
	return query
}

func (s *ProjectStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters ProjectFilters) ([]models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE org_id = @org_id AND deleted_at IS NULL`, projectColumns)
	args := pgx.NamedArgs{"org_id": orgID}
	if filters.OnlyArchived {
		query += ` AND archived_at IS NOT NULL`
	} else if !filters.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}

	query = applyProjectFilters(query, args, filters)
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

func (s *ProjectStore) UpdateStatus(ctx context.Context, orgID, projectID uuid.UUID, status models.ProjectStatus) error {
	query := `UPDATE projects SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	if status == models.ProjectStatusCompleted {
		query = `UPDATE projects SET status = @status, completed_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	}

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
		"status": string(status),
	})
	return err
}

// Count returns the number of projects matching the given org and filters.
func (s *ProjectStore) Count(ctx context.Context, orgID uuid.UUID, filters ProjectFilters) (int, error) {
	query := `SELECT count(*) FROM projects WHERE org_id = @org_id AND deleted_at IS NULL`
	args := pgx.NamedArgs{"org_id": orgID}
	if filters.OnlyArchived {
		query += ` AND archived_at IS NOT NULL`
	} else if !filters.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}
	query = applyProjectFilters(query, args, filters)

	var count int
	err := s.db.QueryRow(ctx, query, args).Scan(&count)
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

func (s *ProjectStore) Archive(ctx context.Context, orgID, projectID uuid.UUID) error {
	query := `UPDATE projects SET archived_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found or already archived")
	}
	return nil
}

func (s *ProjectStore) Unarchive(ctx context.Context, orgID, projectID uuid.UUID) error {
	query := `UPDATE projects SET archived_at = NULL, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found or not archived")
	}
	return nil
}
