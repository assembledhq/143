package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PMPlanStore struct {
	db DBTX
}

func NewPMPlanStore(db DBTX) *PMPlanStore {
	return &PMPlanStore{db: db}
}

type PMPlanFilters struct {
	Limit  int
	Cursor string
}

// pmPlanSelectColumns is the column list shared across all pm_plans queries.
const pmPlanSelectColumns = `id, org_id, repository_id, status, analysis, tasks, clusters, skipped_issues,
	issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed,
	recent_prs_checked, past_decisions_reviewed, commits_analyzed,
	product_context_snapshot, token_usage, triggered_by,
	created_at, completed_at`

func FormatPMPlanCursor(plan models.PMPlan) string {
	if plan.ID == uuid.Nil {
		return ""
	}
	return fmt.Sprintf("%s|%s", plan.CreatedAt.UTC().Format(time.RFC3339Nano), plan.ID.String())
}

func parsePMPlanCursor(cursor string) (time.Time, uuid.UUID, error) {
	parts := strings.Split(cursor, "|")
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid cursor format")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid cursor time: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid cursor id: %w", err)
	}
	return createdAt, id, nil
}

func (s *PMPlanStore) Create(ctx context.Context, plan *models.PMPlan) error {
	query := `
		INSERT INTO pm_plans (
			org_id, repository_id, status, analysis, tasks, clusters, skipped_issues,
			issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed,
			recent_prs_checked, past_decisions_reviewed, commits_analyzed,
			product_context_snapshot, token_usage, triggered_by
		)
		VALUES (
			@org_id, @repository_id, @status, @analysis, @tasks, @clusters, @skipped_issues,
			@issues_reviewed, @in_flight_runs_checked, @past_outcomes_reviewed,
			@recent_prs_checked, @past_decisions_reviewed, @commits_analyzed,
			@product_context_snapshot, @token_usage, @triggered_by
		)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"org_id":                   plan.OrgID,
		"repository_id":            plan.RepositoryID,
		"status":                   plan.Status,
		"analysis":                 plan.Analysis,
		"tasks":                    plan.Tasks,
		"clusters":                 plan.Clusters,
		"skipped_issues":           plan.SkippedIssues,
		"issues_reviewed":          plan.IssuesReviewed,
		"in_flight_runs_checked":   plan.InFlightRunsChecked,
		"past_outcomes_reviewed":   plan.PastOutcomesReviewed,
		"recent_prs_checked":       plan.RecentPRsChecked,
		"past_decisions_reviewed":  plan.PastDecisionsReviewed,
		"commits_analyzed":         plan.CommitsAnalyzed,
		"product_context_snapshot": plan.ProductContextSnapshot,
		"token_usage":              plan.TokenUsage,
		"triggered_by":             plan.TriggeredBy,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&plan.ID, &plan.CreatedAt)
}

func (s *PMPlanStore) Update(ctx context.Context, plan *models.PMPlan) error {
	query := `
		UPDATE pm_plans
		SET status = @status,
		    repository_id = @repository_id,
		    analysis = @analysis,
		    tasks = @tasks,
		    clusters = @clusters,
		    skipped_issues = @skipped_issues,
		    issues_reviewed = @issues_reviewed,
		    in_flight_runs_checked = @in_flight_runs_checked,
		    past_outcomes_reviewed = @past_outcomes_reviewed,
		    recent_prs_checked = @recent_prs_checked,
		    past_decisions_reviewed = @past_decisions_reviewed,
		    commits_analyzed = @commits_analyzed,
		    token_usage = @token_usage,
		    triggered_by = @triggered_by,
		    completed_at = @completed_at
		WHERE id = @id AND org_id = @org_id`

	var completedAt any
	if plan.CompletedAt != nil {
		completedAt = plan.CompletedAt
	} else if plan.Status == "completed" || plan.Status == "failed" {
		completedAt = time.Now()
	}

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                      plan.ID,
		"org_id":                  plan.OrgID,
		"repository_id":           plan.RepositoryID,
		"status":                  plan.Status,
		"analysis":                plan.Analysis,
		"tasks":                   plan.Tasks,
		"clusters":                plan.Clusters,
		"skipped_issues":          plan.SkippedIssues,
		"issues_reviewed":         plan.IssuesReviewed,
		"in_flight_runs_checked":  plan.InFlightRunsChecked,
		"past_outcomes_reviewed":  plan.PastOutcomesReviewed,
		"recent_prs_checked":      plan.RecentPRsChecked,
		"past_decisions_reviewed": plan.PastDecisionsReviewed,
		"commits_analyzed":        plan.CommitsAnalyzed,
		"token_usage":             plan.TokenUsage,
		"triggered_by":            plan.TriggeredBy,
		"completed_at":            completedAt,
	})
	return err
}

func (s *PMPlanStore) GetByID(ctx context.Context, orgID, planID uuid.UUID) (models.PMPlan, error) {
	query := fmt.Sprintf(`SELECT %s FROM pm_plans WHERE id = @id AND org_id = @org_id`, pmPlanSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     planID,
		"org_id": orgID,
	})
	if err != nil {
		return models.PMPlan{}, fmt.Errorf("query pm plan: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PMPlan])
}

func (s *PMPlanStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters PMPlanFilters) ([]models.PMPlan, error) {
	query := fmt.Sprintf(`SELECT %s FROM pm_plans WHERE org_id = @org_id`, pmPlanSelectColumns)

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Cursor != "" {
		cursorCreatedAt, cursorID, err := parsePMPlanCursor(filters.Cursor)
		if err == nil {
			query += ` AND (created_at < @cursor_created_at OR (created_at = @cursor_created_at AND id < @cursor_id))`
			args["cursor_created_at"] = cursorCreatedAt
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY created_at DESC, id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query pm plans: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PMPlan])
}

func (s *PMPlanStore) GetLatestByOrg(ctx context.Context, orgID uuid.UUID) (models.PMPlan, error) {
	query := fmt.Sprintf(`SELECT %s FROM pm_plans WHERE org_id = @org_id ORDER BY created_at DESC LIMIT 1`, pmPlanSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return models.PMPlan{}, fmt.Errorf("query latest pm plan: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PMPlan])
}
