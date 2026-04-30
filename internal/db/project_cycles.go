package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const projectCycleColumns = `id, project_id, org_id, pm_plan_id, cycle_number, analysis, decisions, progress_pct,
	tasks_completed_this_cycle, tasks_failed_this_cycle, tasks_created_this_cycle, created_at`

type ProjectCycleStore struct {
	db DBTX
}

func NewProjectCycleStore(db DBTX) *ProjectCycleStore {
	return &ProjectCycleStore{db: db}
}

func (s *ProjectCycleStore) Create(ctx context.Context, c *models.ProjectCycle) error {
	query := `
		INSERT INTO project_cycles (
			project_id, org_id, pm_plan_id, cycle_number, analysis, decisions, progress_pct,
			tasks_completed_this_cycle, tasks_failed_this_cycle, tasks_created_this_cycle
		)
		VALUES (
			@project_id, @org_id, @pm_plan_id, @cycle_number, @analysis, @decisions, @progress_pct,
			@tasks_completed_this_cycle, @tasks_failed_this_cycle, @tasks_created_this_cycle
		)
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"project_id":                 c.ProjectID,
		"org_id":                     c.OrgID,
		"pm_plan_id":                 c.PMPlanID,
		"cycle_number":               c.CycleNumber,
		"analysis":                   c.Analysis,
		"decisions":                  c.Decisions,
		"progress_pct":               c.ProgressPct,
		"tasks_completed_this_cycle": c.TasksCompletedThisCycle,
		"tasks_failed_this_cycle":    c.TasksFailedThisCycle,
		"tasks_created_this_cycle":   c.TasksCreatedThisCycle,
	})
	return row.Scan(&c.ID, &c.CreatedAt)
}

func (s *ProjectCycleStore) ListByProject(ctx context.Context, orgID, projectID uuid.UUID, limit int) ([]models.ProjectCycle, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	query := fmt.Sprintf(`SELECT %s FROM project_cycles
		WHERE project_id = @project_id AND org_id = @org_id
		ORDER BY cycle_number DESC
		LIMIT %d`, projectCycleColumns, limit)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"project_id": projectID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query project cycles: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ProjectCycle])
}

func (s *ProjectCycleStore) GetByID(ctx context.Context, orgID, cycleID uuid.UUID) (models.ProjectCycle, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_cycles
		WHERE id = @id AND org_id = @org_id`, projectCycleColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     cycleID,
		"org_id": orgID,
	})
	if err != nil {
		return models.ProjectCycle{}, fmt.Errorf("query project cycle: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ProjectCycle])
}
