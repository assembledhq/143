package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ValidationStore struct {
	db DBTX
}

func NewValidationStore(db DBTX) *ValidationStore {
	return &ValidationStore{db: db}
}

func (s *ValidationStore) Create(ctx context.Context, v *models.Validation) error {
	query := `
		INSERT INTO validations (session_id, org_id, status)
		VALUES (@session_id, @org_id, @status)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id": v.SessionID,
		"org_id":       v.OrgID,
		"status":       v.Status,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&v.ID, &v.CreatedAt)
}

func (s *ValidationStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.Validation, error) {
	query := `
		SELECT id, session_id, org_id, status,
		       direction_check, correctness_check, quality_check, security_scan,
		       regression_test_check, coverage_delta, ci_check, details,
		       started_at, completed_at, created_at
		FROM validations
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.Validation{}, fmt.Errorf("query validation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Validation])
}

func (s *ValidationStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Validation, error) {
	query := `
		SELECT id, session_id, org_id, status,
		       direction_check, correctness_check, quality_check, security_scan,
		       regression_test_check, coverage_delta, ci_check, details,
		       started_at, completed_at, created_at
		FROM validations
		WHERE session_id = @session_id AND org_id = @org_id
		ORDER BY CASE WHEN status IN ('passed', 'failed') THEN 0 ELSE 1 END,
		         created_at DESC
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":       orgID,
	})
	if err != nil {
		return models.Validation{}, fmt.Errorf("query validation by session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Validation])
}

func (s *ValidationStore) UpdateCheck(ctx context.Context, orgID, id uuid.UUID, checkName, result string, details []byte) error {
	validChecks := map[string]bool{
		"direction_check":       true,
		"correctness_check":     true,
		"quality_check":         true,
		"security_scan":         true,
		"regression_test_check": true,
		"ci_check":              true,
	}
	if !validChecks[checkName] {
		return fmt.Errorf("invalid check name: %s", checkName)
	}

	query := fmt.Sprintf(
		`UPDATE validations SET %s = @result, details = COALESCE(@details, details) WHERE id = @id AND org_id = @org_id`,
		checkName,
	)
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":      id,
		"org_id":  orgID,
		"result":  result,
		"details": details,
	})
	return err
}

func (s *ValidationStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status string) error {
	query := `UPDATE validations SET status = @status WHERE id = @id AND org_id = @org_id`
	switch status {
	case "running":
		query = `UPDATE validations SET status = @status, started_at = now() WHERE id = @id AND org_id = @org_id`
	case "passed", "failed":
		query = `UPDATE validations SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": status,
	})
	return err
}
