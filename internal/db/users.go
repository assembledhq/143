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

const userSelectColumns = `id, org_id, email, name, role, github_id, github_login, avatar_url, password_hash, google_id, created_at`

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
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE id = @id AND org_id = @org_id`, userSelectColumns)

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
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE github_id = @github_id`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"github_id": githubID})
	if err != nil {
		return models.User{}, fmt.Errorf("query user by github_id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

// GetByEmail looks up a user by email address (cross-org, email is globally unique).
func (s *UserStore) GetByEmail(ctx context.Context, email string) (models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE email = @email`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"email": email})
	if err != nil {
		return models.User{}, fmt.Errorf("query user by email: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

// GetByGoogleID looks up a user by Google subject ID.
func (s *UserStore) GetByGoogleID(ctx context.Context, googleID string) (models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE google_id = @google_id`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"google_id": googleID})
	if err != nil {
		return models.User{}, fmt.Errorf("query user by google_id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

// CreateWithPassword inserts a new user with a bcrypt password hash.
func (s *UserStore) CreateWithPassword(ctx context.Context, user *models.User) error {
	query := `
		INSERT INTO users (org_id, email, name, role, password_hash)
		VALUES (@org_id, @email, @name, @role, @password_hash)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"org_id":        user.OrgID,
		"email":         user.Email,
		"name":          user.Name,
		"role":          user.Role,
		"password_hash": user.PasswordHash,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&user.ID, &user.CreatedAt)
}

// UpsertFromGoogle creates or updates a user based on their Google subject ID.
func (s *UserStore) UpsertFromGoogle(ctx context.Context, user *models.User) error {
	query := `
		INSERT INTO users (org_id, email, name, role, google_id, avatar_url)
		VALUES (@org_id, @email, @name, @role, @google_id, @avatar_url)
		ON CONFLICT (google_id) WHERE google_id IS NOT NULL DO UPDATE
		SET name = EXCLUDED.name,
		    email = EXCLUDED.email,
		    avatar_url = EXCLUDED.avatar_url
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"org_id":     user.OrgID,
		"email":      user.Email,
		"name":       user.Name,
		"role":       user.Role,
		"google_id":  user.GoogleID,
		"avatar_url": user.AvatarURL,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&user.ID, &user.CreatedAt)
}

// LinkGitHubAccount attaches a GitHub identity to an existing user.
func (s *UserStore) LinkGitHubAccount(ctx context.Context, userID, orgID uuid.UUID, githubID int64, githubLogin string, avatarURL string) error {
	query := `
		UPDATE users
		SET github_id = @github_id, github_login = @github_login, avatar_url = @avatar_url
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":           userID,
		"org_id":       orgID,
		"github_id":    githubID,
		"github_login": githubLogin,
		"avatar_url":   avatarURL,
	})
	return err
}

// LinkGoogleAccount attaches a Google identity to an existing user.
func (s *UserStore) LinkGoogleAccount(ctx context.Context, userID, orgID uuid.UUID, googleID string, avatarURL string) error {
	query := `
		UPDATE users
		SET google_id = @google_id, avatar_url = @avatar_url
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":        userID,
		"org_id":    orgID,
		"google_id": googleID,
		"avatar_url": avatarURL,
	})
	return err
}
