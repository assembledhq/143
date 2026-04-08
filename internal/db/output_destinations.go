package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type OutputDestinationStore struct {
	db TxStarter
}

func NewOutputDestinationStore(db TxStarter) *OutputDestinationStore {
	return &OutputDestinationStore{db: db}
}

const outputDestColumns = `id, project_id, org_id, destination_type, label, config, enabled, created_at, updated_at`

func scanOutputDest(row pgx.Row) (models.OutputDestination, error) {
	var d models.OutputDestination
	err := row.Scan(&d.ID, &d.ProjectID, &d.OrgID, &d.DestinationType, &d.Label, &d.Config, &d.Enabled, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (s *OutputDestinationStore) Create(ctx context.Context, d *models.OutputDestination) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	query := fmt.Sprintf(`INSERT INTO project_output_destinations (%s) VALUES ($1,$2,$3,$4,$5,$6,$7,now(),now()) RETURNING %s`, outputDestColumns, outputDestColumns)
	row := s.db.QueryRow(ctx, query,
		d.ID, d.ProjectID, d.OrgID, d.DestinationType, d.Label, d.Config, d.Enabled,
	)
	result, err := scanOutputDest(row)
	if err != nil {
		return fmt.Errorf("create output destination: %w", err)
	}
	*d = result
	return nil
}

func (s *OutputDestinationStore) ListByProject(ctx context.Context, orgID, projectID uuid.UUID) ([]models.OutputDestination, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_output_destinations WHERE org_id = $1 AND project_id = $2 ORDER BY created_at ASC`, outputDestColumns)
	rows, err := s.db.Query(ctx, query, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list output destinations: %w", err)
	}
	defer rows.Close()

	var dests []models.OutputDestination
	for rows.Next() {
		d, err := scanOutputDest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan output destination: %w", err)
		}
		dests = append(dests, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate output destinations: %w", err)
	}
	return dests, nil
}

func (s *OutputDestinationStore) ListEnabledByProject(ctx context.Context, orgID, projectID uuid.UUID) ([]models.OutputDestination, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_output_destinations WHERE org_id = $1 AND project_id = $2 AND enabled = true ORDER BY created_at ASC`, outputDestColumns)
	rows, err := s.db.Query(ctx, query, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list enabled output destinations: %w", err)
	}
	defer rows.Close()

	var dests []models.OutputDestination
	for rows.Next() {
		d, err := scanOutputDest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan output destination: %w", err)
		}
		dests = append(dests, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled output destinations: %w", err)
	}
	return dests, nil
}

func (s *OutputDestinationStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.OutputDestination, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_output_destinations WHERE org_id = $1 AND id = $2`, outputDestColumns)
	return scanOutputDest(s.db.QueryRow(ctx, query, orgID, id))
}

func (s *OutputDestinationStore) Update(ctx context.Context, orgID, projectID, id uuid.UUID, destType models.OutputDestinationType, label string, config []byte, enabled bool) (models.OutputDestination, error) {
	query := fmt.Sprintf(`UPDATE project_output_destinations SET destination_type = $4, label = $5, config = $6, enabled = $7, updated_at = now() WHERE org_id = $1 AND project_id = $2 AND id = $3 RETURNING %s`, outputDestColumns)
	return scanOutputDest(s.db.QueryRow(ctx, query, orgID, projectID, id, destType, label, config, enabled))
}

func (s *OutputDestinationStore) Delete(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM project_output_destinations WHERE org_id = $1 AND project_id = $2 AND id = $3`, orgID, projectID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
