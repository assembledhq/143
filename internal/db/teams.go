package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// TeamStore handles CRUD for teams and team memberships.
type TeamStore struct {
	db TxStarter
}

// NewTeamStore creates a new TeamStore.
func NewTeamStore(db TxStarter) *TeamStore {
	return &TeamStore{db: db}
}

const teamColumns = `id, org_id, name, slug, description, github_team_id, github_team_slug, created_at, updated_at`

// Create inserts a new team.
func (s *TeamStore) Create(ctx context.Context, team *models.Team) error {
	query := `
		INSERT INTO teams (org_id, name, slug, description, github_team_id, github_team_slug)
		VALUES (@org_id, @name, @slug, @description, @github_team_id, @github_team_slug)
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"org_id":           team.OrgID,
		"name":             team.Name,
		"slug":             team.Slug,
		"description":      team.Description,
		"github_team_id":   team.GitHubTeamID,
		"github_team_slug": team.GitHubTeamSlug,
	}

	return s.db.QueryRow(ctx, query, args).Scan(&team.ID, &team.CreatedAt, &team.UpdatedAt)
}

// Update modifies an existing team.
func (s *TeamStore) Update(ctx context.Context, orgID, teamID uuid.UUID, name, slug string, description *string) error {
	query := `
		UPDATE teams
		SET name = @name, slug = @slug, description = @description, updated_at = now()
		WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":          teamID,
		"org_id":      orgID,
		"name":        name,
		"slug":        slug,
		"description": description,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Delete removes a team. Memberships cascade-delete.
func (s *TeamStore) Delete(ctx context.Context, orgID, teamID uuid.UUID) error {
	// Clear team_id references on sessions and projects before deleting the team.
	query := `
		WITH clear_sessions AS (
			UPDATE sessions SET team_id = NULL WHERE team_id = @id AND org_id = @org_id
		),
		clear_projects AS (
			UPDATE projects SET team_id = NULL WHERE team_id = @id AND org_id = @org_id
		)
		DELETE FROM teams WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     teamID,
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

// GetByID returns a single team.
func (s *TeamStore) GetByID(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error) {
	query := fmt.Sprintf(`
		SELECT %s,
			(SELECT COUNT(*) FROM team_memberships WHERE team_id = t.id) AS member_count
		FROM teams t
		WHERE t.id = @id AND t.org_id = @org_id`, teamColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     teamID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Team{}, fmt.Errorf("query team: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Team])
}

// ListByOrg returns all teams in the org with member counts.
func (s *TeamStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Team, error) {
	query := fmt.Sprintf(`
		SELECT %s,
			COALESCE(mc.cnt, 0) AS member_count
		FROM teams t
		LEFT JOIN (
			SELECT team_id, COUNT(*) AS cnt FROM team_memberships GROUP BY team_id
		) mc ON mc.team_id = t.id
		WHERE t.org_id = @org_id
		ORDER BY t.name ASC`, teamColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query teams: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Team])
}

// ListByUser returns teams the given user belongs to.
func (s *TeamStore) ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.Team, error) {
	query := fmt.Sprintf(`
		SELECT %s,
			COALESCE(mc.cnt, 0) AS member_count
		FROM teams t
		JOIN team_memberships tm ON tm.team_id = t.id AND tm.user_id = @user_id
		LEFT JOIN (
			SELECT team_id, COUNT(*) AS cnt FROM team_memberships GROUP BY team_id
		) mc ON mc.team_id = t.id
		WHERE t.org_id = @org_id
		ORDER BY t.name ASC`, teamColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"user_id": userID,
	})
	if err != nil {
		return nil, fmt.Errorf("query user teams: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Team])
}

// AddMember adds a user to a team. The team and user must belong to orgID.
func (s *TeamStore) AddMember(ctx context.Context, orgID, teamID, userID uuid.UUID, role string) error {
	query := `
		INSERT INTO team_memberships (org_id, team_id, user_id, role)
		SELECT @org_id, @team_id, @user_id, @role
		FROM teams t
		WHERE t.id = @team_id AND t.org_id = @org_id
		  AND EXISTS (SELECT 1 FROM users u WHERE u.id = @user_id AND u.org_id = @org_id)
		ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"team_id": teamID,
		"user_id": userID,
		"role":    role,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// RemoveMember removes a user from a team scoped to orgID.
func (s *TeamStore) RemoveMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	query := `
		DELETE FROM team_memberships
		WHERE team_id = @team_id AND user_id = @user_id AND org_id = @org_id`
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"team_id": teamID,
		"user_id": userID,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListMembers returns all users in a team scoped to orgID.
func (s *TeamStore) ListMembers(ctx context.Context, orgID, teamID uuid.UUID) ([]models.User, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM users u
		JOIN team_memberships tm ON tm.user_id = u.id AND tm.org_id = @org_id
		WHERE tm.team_id = @team_id AND u.org_id = @org_id
		ORDER BY u.name ASC`, userSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"team_id": teamID,
	})
	if err != nil {
		return nil, fmt.Errorf("query team members: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.User])
}

// GitHubTeamSync is the input for bulk-syncing teams from GitHub.
type GitHubTeamSync struct {
	GitHubTeamID   int64
	GitHubTeamSlug string
	Name           string
	Description    *string
	// MemberGitHubIDs are the GitHub user IDs that belong to this team.
	MemberGitHubIDs []int64
}

// SyncFromGitHub upserts teams and memberships from GitHub in a single transaction.
// It creates/updates teams, adds new memberships, and removes stale memberships.
func (s *TeamStore) SyncFromGitHub(ctx context.Context, orgID uuid.UUID, ghTeams []GitHubTeamSync) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, ght := range ghTeams {
		teamID, skip, err := upsertGitHubTeam(ctx, tx, orgID, ght)
		if err != nil {
			return err
		}
		if skip {
			continue
		}

		if len(ght.MemberGitHubIDs) == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM team_memberships WHERE team_id = @team_id AND org_id = @org_id`,
				pgx.NamedArgs{"team_id": teamID, "org_id": orgID}); err != nil {
				return fmt.Errorf("clear memberships for team %s: %w", ght.Name, err)
			}
			continue
		}

		rows, err := tx.Query(ctx, `
			SELECT id FROM users
			WHERE org_id = @org_id AND github_id = ANY(@github_ids)`,
			pgx.NamedArgs{
				"org_id":     orgID,
				"github_ids": ght.MemberGitHubIDs,
			},
		)
		if err != nil {
			return fmt.Errorf("resolve github users for team %s: %w", ght.Name, err)
		}

		var localUserIDs []uuid.UUID
		for rows.Next() {
			var uid uuid.UUID
			if err := rows.Scan(&uid); err != nil {
				rows.Close()
				return fmt.Errorf("scan user: %w", err)
			}
			localUserIDs = append(localUserIDs, uid)
		}
		rows.Close()
		// Guard against partial iteration: a stream error would leave localUserIDs
		// incomplete and cause the prune step below to delete valid memberships.
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate github users for team %s: %w", ght.Name, err)
		}

		if len(localUserIDs) > 0 {
			batch := &pgx.Batch{}
			for _, uid := range localUserIDs {
				batch.Queue(`
					INSERT INTO team_memberships (org_id, team_id, user_id, role)
					VALUES (@org_id, @team_id, @user_id, @role)
					ON CONFLICT (team_id, user_id) DO NOTHING`,
					pgx.NamedArgs{
						"org_id":  orgID,
						"team_id": teamID,
						"user_id": uid,
						"role":    models.TeamRoleMember,
					},
				)
			}
			br := tx.SendBatch(ctx, batch)
			for range localUserIDs {
				if _, err := br.Exec(); err != nil {
					br.Close()
					return fmt.Errorf("upsert membership: %w", err)
				}
			}
			br.Close()
		}

		// Remove memberships for users no longer in the GitHub team.
		if len(localUserIDs) > 0 {
			_, err = tx.Exec(ctx, `
				DELETE FROM team_memberships
				WHERE team_id = @team_id AND org_id = @org_id AND user_id != ALL(@user_ids)`,
				pgx.NamedArgs{
					"team_id":  teamID,
					"org_id":   orgID,
					"user_ids": localUserIDs,
				},
			)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM team_memberships WHERE team_id = @team_id AND org_id = @org_id`,
				pgx.NamedArgs{"team_id": teamID, "org_id": orgID})
		}
		if err != nil {
			return fmt.Errorf("prune memberships for team %s: %w", ght.Name, err)
		}
	}

	return tx.Commit(ctx)
}

// upsertGitHubTeam inserts or updates a single team row, handling both
// (org_id, github_team_id) and (org_id, slug) unique constraints without
// aborting the caller's transaction. If the GitHub team's slug collides
// with another github-linked team, the sync skips this team (skip=true).
func upsertGitHubTeam(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, ght GitHubTeamSync) (teamID uuid.UUID, skip bool, err error) {
	// 1. Look for an existing row linked by github_team_id.
	err = tx.QueryRow(ctx, `
		SELECT id FROM teams
		WHERE org_id = @org_id AND github_team_id = @github_team_id`,
		pgx.NamedArgs{"org_id": orgID, "github_team_id": ght.GitHubTeamID},
	).Scan(&teamID)
	if err == nil {
		// Update by id. If the desired slug collides with another team, keep the existing slug.
		if updErr := updateGitHubTeamByID(ctx, tx, teamID, ght); updErr != nil {
			return uuid.Nil, false, updErr
		}
		return teamID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("lookup team %s: %w", ght.Name, err)
	}

	// 2. Not linked yet — attempt insert, skipping if the slug is taken.
	err = tx.QueryRow(ctx, `
		INSERT INTO teams (org_id, name, slug, description, github_team_id, github_team_slug)
		VALUES (@org_id, @name, @slug, @description, @github_team_id, @github_team_slug)
		ON CONFLICT (org_id, slug) DO NOTHING
		RETURNING id`,
		pgx.NamedArgs{
			"org_id":           orgID,
			"name":             ght.Name,
			"slug":             ght.GitHubTeamSlug,
			"description":      ght.Description,
			"github_team_id":   ght.GitHubTeamID,
			"github_team_slug": ght.GitHubTeamSlug,
		},
	).Scan(&teamID)
	if err == nil {
		return teamID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("insert team %s: %w", ght.Name, err)
	}

	// 3. Slug is taken — adopt the existing row if it isn't already linked to
	// a different GitHub team.
	err = tx.QueryRow(ctx, `
		UPDATE teams
		SET name = @name, description = @description,
			github_team_id = @github_team_id, github_team_slug = @github_team_slug,
			updated_at = now()
		WHERE org_id = @org_id AND slug = @slug AND github_team_id IS NULL
		RETURNING id`,
		pgx.NamedArgs{
			"org_id":           orgID,
			"slug":             ght.GitHubTeamSlug,
			"name":             ght.Name,
			"description":      ght.Description,
			"github_team_id":   ght.GitHubTeamID,
			"github_team_slug": ght.GitHubTeamSlug,
		},
	).Scan(&teamID)
	if err == nil {
		return teamID, false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		zerolog.Ctx(ctx).Warn().
			Str("team", ght.Name).
			Str("slug", ght.GitHubTeamSlug).
			Int64("github_team_id", ght.GitHubTeamID).
			Msg("slug already owned by a different GitHub team; skipping sync for this team")
		return uuid.Nil, true, nil
	}
	return uuid.Nil, false, fmt.Errorf("adopt team %s by slug: %w", ght.Name, err)
}

// updateGitHubTeamByID applies the desired GitHub metadata to an existing team row.
// The slug change is only applied if it doesn't conflict with another team — any
// unique violation here would otherwise abort the caller's transaction.
func updateGitHubTeamByID(ctx context.Context, tx pgx.Tx, id uuid.UUID, ght GitHubTeamSync) error {
	_, err := tx.Exec(ctx, `
		UPDATE teams AS t
		SET name = @name,
			slug = CASE
				WHEN NOT EXISTS (
					SELECT 1 FROM teams t2
					WHERE t2.org_id = t.org_id AND t2.slug = @slug AND t2.id <> t.id
				) THEN @slug
				ELSE t.slug
			END,
			description = @description,
			github_team_slug = @github_team_slug,
			updated_at = now()
		WHERE t.id = @id`,
		pgx.NamedArgs{
			"id":               id,
			"name":             ght.Name,
			"slug":             ght.GitHubTeamSlug,
			"description":      ght.Description,
			"github_team_slug": ght.GitHubTeamSlug,
		},
	)
	if err != nil {
		return fmt.Errorf("update team %s: %w", ght.Name, err)
	}
	return nil
}
