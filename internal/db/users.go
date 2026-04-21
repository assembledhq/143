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

// GetByIDGlobal looks up a user by their primary key alone, without scoping to
// a single org. Used by the auth middleware: in the multi-org world the
// session identifies a user, not a (user, org) pair — the active org is
// resolved separately from memberships.
//
// lint:allow-no-orgid reason="auth middleware loads user identity before active-org resolution; org membership is enforced by OrganizationMembershipStore.Get"
func (s *UserStore) GetByIDGlobal(ctx context.Context, userID uuid.UUID) (models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE id = @id`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": userID})
	if err != nil {
		return models.User{}, fmt.Errorf("query user: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

// GetByGitHubID looks up a user by their GitHub user id (cross-org).
// lint:allow-no-orgid reason="pre-auth login lookup; GitHub id is globally unique"
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

// GetByEmail looks up a user by email address (cross-org, email is globally
// unique). Matches case-insensitively: `users.email` is case-preserving in
// the schema, but OAuth callbacks have historically written mixed-case emails
// (`John@Foo.com`) and the handler layer increasingly normalizes to lower
// (invite lookups, signup dedup). Looking up with `LOWER(email) = LOWER(@email)`
// makes the Go layer agnostic to the stored casing so invitation dedup and
// "email already exists" paths stay consistent with invitation claim's own
// case-insensitive match (strings.EqualFold in validateInvitation).
// lint:allow-no-orgid reason="pre-auth login lookup; email is globally unique"
func (s *UserStore) GetByEmail(ctx context.Context, email string) (models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE LOWER(email) = LOWER(@email)`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"email": email})
	if err != nil {
		return models.User{}, fmt.Errorf("query user by email: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.User])
}

// GetByGoogleID looks up a user by Google subject ID.
// lint:allow-no-orgid reason="pre-auth login lookup; Google subject id is globally unique"
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
//
// TODO(2026-04-25): drop the orgID parameter and the `AND org_id = @org_id`
// filter once users.org_id is removed. The org scope is a legacy
// single-org-per-user safety belt; with multi-org, the (id) predicate alone
// is sufficient because user IDs are globally unique.
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

// ListByOrg returns all users in the given organization, ordered by creation time.
//
// This reads the legacy users.org_id column and so only returns users whose
// primary org is the one queried. New code should prefer ListByOrgViaMemberships,
// which joins through organization_memberships and therefore also returns users
// who hold a non-primary membership (e.g. someone who joined a second org via
// ClaimInvitation). Kept for the sunset window while remaining callers migrate.
func (s *UserStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users
		WHERE org_id = @org_id
		ORDER BY created_at ASC`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.User])
}

// ListByOrgViaMemberships returns every user who currently holds a membership
// in the given org, joined through organization_memberships. Each row's Role
// is populated from the membership (not users.role) so a user who is admin in
// their primary org but member here is reported as `member`.
//
// Ordered by membership created_at so the admin/member directory stays stable
// and matches the order members joined this specific org.
func (s *UserStore) ListByOrgViaMemberships(ctx context.Context, orgID uuid.UUID) ([]models.User, error) {
	query := `
		SELECT u.id, u.org_id, u.email, u.name, m.role,
		       u.github_id, u.github_login, u.avatar_url,
		       u.password_hash, u.google_id, u.created_at
		FROM users u
		JOIN organization_memberships m ON m.user_id = u.id
		WHERE m.org_id = @org_id
		ORDER BY m.created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query users via memberships: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.User])
}

// IsGitHubLoginMemberOfOrg reports whether any user whose github_login
// matches (case-insensitive) is currently a member of the given org via
// the organization_memberships join. Used by invitation creation to dedup
// against current members regardless of which org's row was their primary
// at user-creation time. The legacy users.org_id column does not reflect
// non-primary memberships, so a plain ListByOrg dedup misses members whose
// only membership in this org is non-primary.
//
// Callers must pass a non-empty, already-validated login. The handler layer
// rejects empty / malformed input via isValidGitHubUsername before calling,
// so a silent short-circuit here would only mask an upstream bug.
func (s *UserStore) IsGitHubLoginMemberOfOrg(ctx context.Context, githubLogin string, orgID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM users u
			JOIN organization_memberships m ON m.user_id = u.id
			WHERE m.org_id = @org_id
			  AND u.github_login IS NOT NULL
			  AND lower(u.github_login) = lower(@github_login)
		)`
	var exists bool
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":       orgID,
		"github_login": githubLogin,
	}).Scan(&exists); err != nil {
		return false, fmt.Errorf("check github login membership: %w", err)
	}
	return exists, nil
}

// UpdateRole changes a user's role within their org.
func (s *UserStore) UpdateRole(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	query := `UPDATE users SET role = @role WHERE id = @id AND org_id = @org_id`
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"role":   role,
		"id":     userID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Delete removes a user from their org.
//
// The DELETE FROM users cascades through organization_memberships, so if the
// user is the last admin of *any* org (not just orgID — a multi-membership
// user could be sole admin elsewhere) the enforce_last_admin trigger rejects
// the cascade at commit. We map that into ErrLastAdmin so the operator-facing
// caller gets the same sentinel as the membership store's guarded paths
// rather than a raw pgconn.PgError.
func (s *UserStore) Delete(ctx context.Context, orgID, userID uuid.UUID) error {
	// Clear known user references before deletion to avoid FK failures for active users.
	query := `
		WITH cleared_agent_answers AS (
			UPDATE session_questions
			SET answered_by = NULL
			WHERE org_id = @org_id AND answered_by = @id
		),
		deleted_invitations AS (
			DELETE FROM invitations
			WHERE org_id = @org_id AND invited_by = @id
		),
		deleted_user AS (
			DELETE FROM users
			WHERE id = @id AND org_id = @org_id
			RETURNING id
		)
		SELECT EXISTS (SELECT 1 FROM deleted_user)`

	var deleted bool
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     userID,
		"org_id": orgID,
	}).Scan(&deleted)
	if err != nil {
		return mapLastAdminViolation(err)
	}
	if !deleted {
		return pgx.ErrNoRows
	}
	return nil
}

// CountAdmins returns the number of admin users in the given org.
func (s *UserStore) CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM users WHERE org_id = @org_id AND role = 'admin'`
	var count int
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}

// LinkGoogleAccount attaches a Google identity to an existing user.
//
// TODO(2026-04-25): drop the orgID parameter and the `AND org_id = @org_id`
// filter once users.org_id is removed — see LinkGitHubAccount for rationale.
func (s *UserStore) LinkGoogleAccount(ctx context.Context, userID, orgID uuid.UUID, googleID string, avatarURL string) error {
	query := `
		UPDATE users
		SET google_id = @google_id, avatar_url = @avatar_url
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":         userID,
		"org_id":     orgID,
		"google_id":  googleID,
		"avatar_url": avatarURL,
	})
	return err
}
