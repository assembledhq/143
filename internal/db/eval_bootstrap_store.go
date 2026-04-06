package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// EvalBootstrapStore provides CRUD operations for eval_bootstrap_runs.
type EvalBootstrapStore struct {
	db DBTX
}

// NewEvalBootstrapStore returns a new EvalBootstrapStore backed by the given db handle.
func NewEvalBootstrapStore(db DBTX) *EvalBootstrapStore {
	return &EvalBootstrapStore{db: db}
}

// Create inserts a new bootstrap run.
func (s *EvalBootstrapStore) Create(ctx context.Context, run *models.EvalBootstrapRun) error {
	return s.db.QueryRow(ctx,
		`INSERT INTO eval_bootstrap_runs (org_id, repo_id, status, created_by)
		 VALUES (@org_id, @repo_id, @status, @created_by)
		 RETURNING id, created_at`,
		pgx.NamedArgs{
			"org_id":     run.OrgID,
			"repo_id":    run.RepoID,
			"status":     run.Status,
			"created_by": run.CreatedBy,
		},
	).Scan(&run.ID, &run.CreatedAt)
}

// GetByID retrieves a bootstrap run by ID, scoped to org.
func (s *EvalBootstrapStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.EvalBootstrapRun, error) {
	var r models.EvalBootstrapRun
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, repo_id, status, candidates, session_id, created_by, created_at, completed_at, error_message
		 FROM eval_bootstrap_runs WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Status, &r.Candidates, &r.SessionID,
		&r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ErrorMessage)
	if err != nil {
		return r, fmt.Errorf("get eval bootstrap run: %w", err)
	}
	return r, nil
}

// GetLatestByOrg returns the most recent bootstrap run for an org+repo.
func (s *EvalBootstrapStore) GetLatestByOrg(ctx context.Context, orgID, repoID uuid.UUID) (models.EvalBootstrapRun, error) {
	var r models.EvalBootstrapRun
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, repo_id, status, candidates, session_id, created_by, created_at, completed_at, error_message
		 FROM eval_bootstrap_runs WHERE org_id = @org_id AND repo_id = @repo_id
		 ORDER BY created_at DESC LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "repo_id": repoID},
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Status, &r.Candidates, &r.SessionID,
		&r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ErrorMessage)
	if err != nil {
		return r, fmt.Errorf("get latest eval bootstrap run: %w", err)
	}
	return r, nil
}

// UpdateStatus updates the status and optionally sets the session_id.
func (s *EvalBootstrapStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.EvalBootstrapStatus, sessionID *uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_runs SET status = @status, session_id = COALESCE(@session_id, session_id)
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "status": status, "session_id": sessionID},
	)
	if err != nil {
		return fmt.Errorf("update eval bootstrap run status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateResult stores the candidates and marks the run as completed or failed.
func (s *EvalBootstrapStore) UpdateResult(ctx context.Context, orgID, id uuid.UUID, status models.EvalBootstrapStatus, candidates []byte, errMsg *string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_runs
		 SET status = @status, candidates = @candidates, error_message = @error_message, completed_at = now()
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":            id,
			"org_id":        orgID,
			"status":        status,
			"candidates":    candidates,
			"error_message": errMsg,
		},
	)
	if err != nil {
		return fmt.Errorf("update eval bootstrap run result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
