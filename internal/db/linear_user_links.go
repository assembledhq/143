package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LinearUserLinkStore struct {
	db DBTX
}

func NewLinearUserLinkStore(db DBTX) *LinearUserLinkStore {
	return &LinearUserLinkStore{db: db}
}

func (s *LinearUserLinkStore) GetByLinearUser(ctx context.Context, orgID uuid.UUID, workspaceID, linearUserID string) (models.LinearUserLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, integration_id, user_id, linear_workspace_id, linear_user_id,
			linear_email, linear_display_name, source, linked_at, created_at, updated_at
		FROM linear_user_links
		WHERE org_id = @org_id
		  AND linear_workspace_id = @linear_workspace_id
		  AND linear_user_id = @linear_user_id`,
		pgx.NamedArgs{"org_id": orgID, "linear_workspace_id": workspaceID, "linear_user_id": linearUserID})
	if err != nil {
		return models.LinearUserLink{}, fmt.Errorf("query linear user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.LinearUserLink])
}

func (s *LinearUserLinkStore) UpsertEmailMatch(ctx context.Context, link *models.LinearUserLink) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO linear_user_links (
			org_id, integration_id, user_id, linear_workspace_id, linear_user_id,
			linear_email, linear_display_name, source, linked_at
		)
		VALUES (
			@org_id, @integration_id, @user_id, @linear_workspace_id, @linear_user_id,
			@linear_email, @linear_display_name, 'email_match', now()
		)
		ON CONFLICT (org_id, linear_workspace_id, linear_user_id)
		DO UPDATE SET
			integration_id = EXCLUDED.integration_id,
			user_id = COALESCE(linear_user_links.user_id, EXCLUDED.user_id),
			linear_email = EXCLUDED.linear_email,
			linear_display_name = EXCLUDED.linear_display_name,
			source = CASE
				WHEN linear_user_links.source IN ('self_linked', 'admin_linked') THEN linear_user_links.source
				ELSE 'email_match'
			END,
			linked_at = COALESCE(linear_user_links.linked_at, now()),
			updated_at = now()
		RETURNING id, org_id, integration_id, user_id, linear_workspace_id, linear_user_id,
			linear_email, linear_display_name, source, linked_at, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":              link.OrgID,
			"integration_id":      link.IntegrationID,
			"user_id":             link.UserID,
			"linear_workspace_id": link.LinearWorkspaceID,
			"linear_user_id":      link.LinearUserID,
			"linear_email":        link.LinearEmail,
			"linear_display_name": link.LinearDisplayName,
		})
	if err != nil {
		return fmt.Errorf("upsert linear email match: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.LinearUserLink])
	if err != nil {
		return fmt.Errorf("scan linear email match: %w", err)
	}
	*link = updated
	return nil
}
