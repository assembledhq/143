package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PRReadinessStore struct {
	db DBTX
}

func NewPRReadinessStore(db DBTX) *PRReadinessStore {
	return &PRReadinessStore{db: db}
}

const prReadinessRunColumns = `id, org_id, session_id, repository_id, status,
	evaluated_workspace_revision, evaluated_snapshot_key, summary, review_packet,
	triggered_by_user_id, started_at, completed_at, created_at, updated_at`

const prReadinessCheckColumns = `id, org_id, run_id, session_id, check_type, status,
	enforcement, title, summary, details, action, created_at`

func (s *PRReadinessStore) CreateRun(ctx context.Context, run *models.PRReadinessRun) error {
	if err := run.Status.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO pr_readiness_runs (
			org_id, session_id, repository_id, status, evaluated_workspace_revision,
			evaluated_snapshot_key, summary, review_packet, triggered_by_user_id
		) VALUES (
			@org_id, @session_id, @repository_id, @status, @evaluated_workspace_revision,
			@evaluated_snapshot_key, @summary, @review_packet, @triggered_by_user_id
		)
		RETURNING id, started_at, created_at, updated_at`
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                       run.OrgID,
		"session_id":                   run.SessionID,
		"repository_id":                run.RepositoryID,
		"status":                       run.Status,
		"evaluated_workspace_revision": run.EvaluatedWorkspaceRevision,
		"evaluated_snapshot_key":       run.EvaluatedSnapshotKey,
		"summary":                      run.Summary,
		"review_packet":                run.ReviewPacket,
		"triggered_by_user_id":         run.TriggeredByUserID,
	}).Scan(&run.ID, &run.StartedAt, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create PR readiness run: %w", err)
	}
	return nil
}

func (s *PRReadinessStore) MarkRunning(ctx context.Context, orgID, runID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE pr_readiness_runs
		SET status = 'running', updated_at = now()
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
		"org_id": orgID,
		"id":     runID,
	})
	if err != nil {
		return fmt.Errorf("mark PR readiness running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PRReadinessStore) CompleteRunWithChecks(ctx context.Context, orgID uuid.UUID, runID uuid.UUID, result models.PRReadinessRun, checks []models.PRReadinessCheck) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("complete PR readiness requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PR readiness completion tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE pr_readiness_runs
		SET status = @status,
		    summary = @summary,
		    review_packet = @review_packet,
		    completed_at = now(),
		    updated_at = now()
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
		"org_id":        orgID,
		"id":            runID,
		"status":        result.Status,
		"summary":       result.Summary,
		"review_packet": result.ReviewPacket,
	})
	if err != nil {
		return fmt.Errorf("update PR readiness run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pr_readiness_checks WHERE org_id = @org_id AND run_id = @run_id`, pgx.NamedArgs{"org_id": orgID, "run_id": runID}); err != nil {
		return fmt.Errorf("replace PR readiness checks: %w", err)
	}
	for _, check := range checks {
		if err := check.CheckType.Validate(); err != nil {
			return err
		}
		if err := check.Status.Validate(); err != nil {
			return err
		}
		if err := check.Enforcement.Validate(); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO pr_readiness_checks (
				org_id, run_id, session_id, check_type, status, enforcement,
				title, summary, details, action
			) VALUES (
				@org_id, @run_id, @session_id, @check_type, @status, @enforcement,
				@title, @summary, @details, @action
			)`, pgx.NamedArgs{
			"org_id":      orgID,
			"run_id":      runID,
			"session_id":  check.SessionID,
			"check_type":  check.CheckType,
			"status":      check.Status,
			"enforcement": check.Enforcement,
			"title":       check.Title,
			"summary":     check.Summary,
			"details":     check.Details,
			"action":      check.Action,
		})
		if err != nil {
			return fmt.Errorf("insert PR readiness check: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PR readiness completion tx: %w", err)
	}
	return nil
}

func (s *PRReadinessStore) GetLatestBySession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PRReadinessRun, error) {
	query := `
		SELECT ` + prReadinessRunColumns + `
		FROM pr_readiness_runs
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY created_at DESC, id DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("query latest PR readiness run: %w", err)
	}
	run, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PRReadinessRun])
	if err != nil {
		return nil, err
	}
	checks, err := s.ListChecksByRun(ctx, orgID, run.ID)
	if err != nil {
		return nil, err
	}
	run.Checks = checks
	return &run, nil
}

func (s *PRReadinessStore) GetRunByID(ctx context.Context, orgID, runID uuid.UUID) (models.PRReadinessRun, error) {
	query := `
		SELECT ` + prReadinessRunColumns + `
		FROM pr_readiness_runs
		WHERE org_id = @org_id AND id = @id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": runID})
	if err != nil {
		return models.PRReadinessRun{}, fmt.Errorf("query PR readiness run: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PRReadinessRun])
}

func (s *PRReadinessStore) ListChecksByRun(ctx context.Context, orgID, runID uuid.UUID) ([]models.PRReadinessCheck, error) {
	query := `
		SELECT ` + prReadinessCheckColumns + `
		FROM pr_readiness_checks
		WHERE org_id = @org_id AND run_id = @run_id
		ORDER BY created_at ASC, id ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("query PR readiness checks: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PRReadinessCheck])
}
