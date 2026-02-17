package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type UserStore struct {
	db DBTX
}

func NewUserStore(db DBTX) *UserStore {
	return &UserStore{db: db}
}

// UpsertFromGitHub creates or updates a user based on their GitHub ID.
// On conflict, it updates the user's name, login, avatar, and email.
func (s *UserStore) UpsertFromGitHub(ctx context.Context, user *models.User) error {
	query := `
		INSERT INTO users (org_id, email, name, role, github_id, github_login, avatar_url)
		VALUES (@org_id, @email, @name, @role, @github_id, @github_login, @avatar_url)
		ON CONFLICT (github_id) WHERE github_id IS NOT NULL DO UPDATE
		SET name = EXCLUDED.name,
		    email = EXCLUDED.email,
		    github_login = EXCLUDED.github_login,
		    avatar_url = EXCLUDED.avatar_url
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"org_id":       user.OrgID,
		"email":        user.Email,
		"name":         user.Name,
		"role":         user.Role,
		"github_id":    user.GitHubID,
		"github_login": user.GitHubLogin,
		"avatar_url":   user.AvatarURL,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&user.ID, &user.CreatedAt)
}

func (s *UserStore) GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error) {
	query := `
		SELECT id, org_id, email, name, role, github_id, github_login, avatar_url, created_at
		FROM users
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     userID,
		"org_id": orgID,
	})
	if err != nil {
		return models.User{}, fmt.Errorf("query user: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

func (s *UserStore) GetByGitHubID(ctx context.Context, githubID int64) (models.User, error) {
	query := `
		SELECT id, org_id, email, name, role, github_id, github_login, avatar_url, created_at
		FROM users
		WHERE github_id = @github_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"github_id": githubID})
	if err != nil {
		return models.User{}, fmt.Errorf("query user by github_id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}
