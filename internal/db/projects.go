package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ProjectStore struct {
	db DBTX
}

func NewProjectStore(db DBTX) *ProjectStore {
	return &ProjectStore{db: db}
}

type ProjectFilters struct {
	Status string
	Limit  int
	Cursor string
}

// projectColumns is the column list shared across all project queries.
const projectColumns = `id, org_id, repository_id, title, goal, scope, completion_criteria,
	status, priority, execution_mode, max_concurrent, auto_merge, base_branch,
	current_phase, lessons_learned, approach_history,
	total_tasks, completed_tasks, failed_tasks,
	proposed_by_pm, source_issue_ids, proposal_reasoning,
	agent_type, model_override,
	created_by, created_at, updated_at, completed_at`

func scanProject(row pgx.Row) (models.Project, error) {
	var p models.Project
	var lessonsRaw, approachRaw []byte
	var sourceIssueIDs []uuid.UUID

	err := row.Scan(
		&p.ID, &p.OrgID, &p.RepositoryID, &p.Title, &p.Goal, &p.Scope, &p.CompletionCriteria,
		&p.Status, &p.Priority, &p.ExecutionMode, &p.MaxConcurrent, &p.AutoMerge, &p.BaseBranch,
		&p.CurrentPhase, &lessonsRaw, &approachRaw,
		&p.TotalTasks, &p.CompletedTasks, &p.FailedTasks,
		&p.ProposedByPM, &sourceIssueIDs, &p.ProposalReasoning,
		&p.AgentType, &p.ModelOverride,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
	)
	if err != nil {
		return models.Project{}, err
	}

	if len(lessonsRaw) > 0 {
		_ = json.Unmarshal(lessonsRaw, &p.LessonsLearned)
	}
	if len(approachRaw) > 0 {
		_ = json.Unmarshal(approachRaw, &p.ApproachHistory)
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
			&p.ProposedByPM, &sourceIssueIDs, &p.ProposalReasoning,
			&p.AgentType, &p.ModelOverride,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
		)
		if err != nil {
			return nil, err
		}

		if len(lessonsRaw) > 0 {
			_ = json.Unmarshal(lessonsRaw, &p.LessonsLearned)
		}
		if len(approachRaw) > 0 {
			_ = json.Unmarshal(approachRaw, &p.ApproachHistory)
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

	query := `
		INSERT INTO projects (
			org_id, repository_id, title, goal, scope, completion_criteria,
			status, priority, execution_mode, max_concurrent, auto_merge, base_branch,
			current_phase, lessons_learned, approach_history,
			proposed_by_pm, source_issue_ids, proposal_reasoning, created_by,
			agent_type, model_override
		)
		VALUES (
			@org_id, @repository_id, @title, @goal, @scope, @completion_criteria,
			@status, @priority, @execution_mode, @max_concurrent, @auto_merge, @base_branch,
			@current_phase, @lessons_learned, @approach_history,
			@proposed_by_pm, @source_issue_ids, @proposal_reasoning, @created_by,
			@agent_type, @model_override
		)
		RETURNING id, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
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
		"created_by":          p.CreatedBy,
		"agent_type":          p.AgentType,
		"model_override":      p.ModelOverride,
	})
	return row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

func (s *ProjectStore) GetByID(ctx context.Context, orgID, projectID uuid.UUID) (models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE id = @id AND org_id = @org_id`, projectColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
	})
	return scanProject(row)
}

func (s *ProjectStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters ProjectFilters) ([]models.Project, error) {
	query := fmt.Sprintf(`SELECT %s FROM projects WHERE org_id = @org_id`, projectColumns)
	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
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

	query := `
		UPDATE projects SET
			title = @title, goal = @goal, scope = @scope, completion_criteria = @completion_criteria,
			status = @status, priority = @priority, execution_mode = @execution_mode,
			max_concurrent = @max_concurrent, auto_merge = @auto_merge, base_branch = @base_branch,
			current_phase = @current_phase, lessons_learned = @lessons_learned,
			approach_history = @approach_history,
			total_tasks = @total_tasks, completed_tasks = @completed_tasks, failed_tasks = @failed_tasks,
			completed_at = @completed_at, updated_at = now()
		WHERE id = @id AND org_id = @org_id`

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

func (s *ProjectStore) UpdateStatus(ctx context.Context, orgID, projectID uuid.UUID, status string) error {
	query := `UPDATE projects SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	if status == "completed" || status == "cancelled" {
		query = `UPDATE projects SET status = @status, completed_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`
	}

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     projectID,
		"org_id": orgID,
		"status": status,
	})
	return err
}
