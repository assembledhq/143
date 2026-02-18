package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type DeployStore struct {
	db DBTX
}

func NewDeployStore(db DBTX) *DeployStore {
	return &DeployStore{db: db}
}

func (s *DeployStore) Create(ctx context.Context, d *models.Deploy) error {
	query := `
		INSERT INTO deploys (pull_request_id, org_id, environment, commit_sha)
		VALUES (@pull_request_id, @org_id, @environment, @commit_sha)
		RETURNING id, deployed_at, created_at`

	args := pgx.NamedArgs{
		"pull_request_id": d.PullRequestID,
		"org_id":          d.OrgID,
		"environment":     d.Environment,
		"commit_sha":      d.CommitSHA,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&d.ID, &d.DeployedAt, &d.CreatedAt)
}

func (s *DeployStore) GetByPullRequestID(ctx context.Context, orgID, prID uuid.UUID) (models.Deploy, error) {
	query := `
		SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at
		FROM deploys
		WHERE pull_request_id = @pull_request_id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"pull_request_id": prID,
		"org_id":          orgID,
	})
	if err != nil {
		return models.Deploy{}, fmt.Errorf("query deploy: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Deploy])
}
