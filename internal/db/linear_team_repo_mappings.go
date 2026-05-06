package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearTeamRepoMapping is one row of the (Linear team, optional Linear
// project) → 143 repo lookup table. The repo resolver consults this when
// an inbound AgentSession arrives so the coding session knows which repo
// to clone. A row with linear_project_id=NULL is the "team default" and
// matches issues with no project.
type LinearTeamRepoMapping struct {
	ID              uuid.UUID `db:"id" json:"id"`
	OrgID           uuid.UUID `db:"org_id" json:"org_id"`
	LinearTeamID    string    `db:"linear_team_id" json:"linear_team_id"`
	LinearProjectID *string   `db:"linear_project_id" json:"linear_project_id,omitempty"`
	RepositoryID    uuid.UUID `db:"repository_id" json:"repository_id"`
	DefaultBranch   string    `db:"default_branch" json:"default_branch,omitempty"`
	Priority        int       `db:"priority" json:"priority"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time `db:"updated_at" json:"updated_at"`
}

// LinearTeamRepoMappingStore reads and writes the repo-mapping table. Hot
// path: the repo resolver calls Resolve on every inbound AgentSession, so
// the implementation prefers a single round-trip query that returns both
// the team-default and per-project rows for a team.
type LinearTeamRepoMappingStore struct {
	db DBTX
}

func NewLinearTeamRepoMappingStore(db DBTX) *LinearTeamRepoMappingStore {
	return &LinearTeamRepoMappingStore{db: db}
}

// ResolveInput is the resolver's question to the store. ProjectID is
// optional — issues without a project pass empty.
type ResolveInput struct {
	OrgID           uuid.UUID
	LinearTeamID    string
	LinearProjectID string
}

// Resolve returns the best mapping for an issue, applying the documented
// priority order:
//  1. exact (team, project) match
//  2. team default (project IS NULL)
//
// Returns ErrLinearTeamRepoMappingNotFound when neither row exists. The
// org-default fallback (org_settings.linear_agent.default_repo_id) is the
// resolver's responsibility, not this store's — keeps the store simple and
// doesn't pin the resolver's policy choices into the data layer.
//
// One query, server-side ORDER BY: the planner uses the
// idx_linear_team_repo_mappings_org_team index to fetch both candidate rows
// at once, and ORDER BY ranks "exact match" above "team default". Cheaper
// than two round-trips and atomic w.r.t. concurrent admin edits.
//
// orgID is taken explicitly so the lint-stores tooling can verify the
// org scope without recursing through ResolveInput.OrgID; if the caller
// also populates in.OrgID the two are cross-checked for consistency.
func (s *LinearTeamRepoMappingStore) Resolve(ctx context.Context, orgID uuid.UUID, in ResolveInput) (*LinearTeamRepoMapping, error) {
	if in.OrgID == uuid.Nil {
		in.OrgID = orgID
	}
	if in.OrgID != orgID {
		return nil, errors.New("org_id mismatch between argument and input struct")
	}
	var row LinearTeamRepoMapping
	err := s.db.QueryRow(ctx, `
		SELECT id, org_id, linear_team_id, linear_project_id,
		       repository_id, default_branch, priority,
		       created_at, updated_at
		FROM linear_team_repo_mappings
		WHERE org_id = @org_id
		  AND linear_team_id = @linear_team_id
		  AND (
		      linear_project_id = @linear_project_id
		      OR (linear_project_id IS NULL AND @linear_project_id IS NOT DISTINCT FROM '')
		      OR linear_project_id IS NULL
		  )
		ORDER BY
		    -- Exact (team, project) match wins, then team-default fallback.
		    -- COALESCE keeps NULLs at the bottom of the ordering.
		    CASE
		        WHEN linear_project_id = @linear_project_id THEN 0
		        WHEN linear_project_id IS NULL                 THEN 1
		        ELSE                                                2
		    END ASC,
		    priority ASC,
		    updated_at DESC
		LIMIT 1`,
		pgx.NamedArgs{
			"org_id":            in.OrgID,
			"linear_team_id":    in.LinearTeamID,
			"linear_project_id": in.LinearProjectID,
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.LinearTeamID,
		&row.LinearProjectID,
		&row.RepositoryID,
		&row.DefaultBranch,
		&row.Priority,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLinearTeamRepoMappingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query linear_team_repo_mappings: %w", err)
	}
	return &row, nil
}

// ListByOrg returns all mappings for an org, ordered for a stable settings
// UI render. Sized for a settings page, not a hot path; an org with
// thousands of teams is unrealistic.
func (s *LinearTeamRepoMappingStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]LinearTeamRepoMapping, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, linear_team_id, linear_project_id,
		       repository_id, default_branch, priority,
		       created_at, updated_at
		FROM linear_team_repo_mappings
		WHERE org_id = @org_id
		ORDER BY linear_team_id ASC,
		         (linear_project_id IS NULL) DESC,
		         linear_project_id NULLS LAST,
		         priority ASC`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list linear_team_repo_mappings: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearTeamRepoMapping])
}

// UpsertInput is the settings-UI write surface. Empty LinearProjectID is
// canonicalized to NULL by the SQL layer (so empty string and NULL share a
// row), matching the UNIQUE constraint's COALESCE shape.
type UpsertInput struct {
	OrgID           uuid.UUID
	LinearTeamID    string
	LinearProjectID string
	RepositoryID    uuid.UUID
	DefaultBranch   string
	Priority        int
}

// Upsert creates or replaces a mapping. The (org, team, project) UNIQUE
// constraint means callers don't need to delete-then-insert when changing
// the target repo — ON CONFLICT DO UPDATE handles it.
func (s *LinearTeamRepoMappingStore) Upsert(ctx context.Context, orgID uuid.UUID, in UpsertInput) (*LinearTeamRepoMapping, error) {
	if in.OrgID == uuid.Nil {
		in.OrgID = orgID
	}
	if in.OrgID != orgID {
		return nil, errors.New("org_id mismatch between argument and input struct")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}

	var row LinearTeamRepoMapping
	err := s.db.QueryRow(ctx, `
		INSERT INTO linear_team_repo_mappings
			(org_id, linear_team_id, linear_project_id, repository_id, default_branch, priority)
		VALUES
			(@org_id, @linear_team_id, @linear_project_id, @repository_id, @default_branch, @priority)
		ON CONFLICT (org_id, linear_team_id, COALESCE(linear_project_id, '')) DO UPDATE
		SET repository_id  = EXCLUDED.repository_id,
		    default_branch = EXCLUDED.default_branch,
		    priority       = EXCLUDED.priority,
		    updated_at     = now()
		RETURNING id, org_id, linear_team_id, linear_project_id,
		          repository_id, default_branch, priority,
		          created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":            in.OrgID,
			"linear_team_id":    in.LinearTeamID,
			"linear_project_id": nullableString(in.LinearProjectID),
			"repository_id":     in.RepositoryID,
			"default_branch":    in.DefaultBranch,
			"priority":          in.Priority,
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.LinearTeamID,
		&row.LinearProjectID,
		&row.RepositoryID,
		&row.DefaultBranch,
		&row.Priority,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert linear_team_repo_mappings: %w", err)
	}
	return &row, nil
}

// Delete removes a mapping by id. Returns ErrLinearTeamRepoMappingNotFound
// when the id doesn't exist or belongs to another org.
func (s *LinearTeamRepoMappingStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM linear_team_repo_mappings
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("delete linear_team_repo_mappings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinearTeamRepoMappingNotFound
	}
	return nil
}

func (in UpsertInput) validate() error {
	if in.OrgID == uuid.Nil {
		return errors.New("org_id is required")
	}
	if in.LinearTeamID == "" {
		return errors.New("linear_team_id is required")
	}
	if in.RepositoryID == uuid.Nil {
		return errors.New("repository_id is required")
	}
	return nil
}

// ErrLinearTeamRepoMappingNotFound is returned when Resolve or Delete cannot
// find a row. Sentinel so the resolver can fall back to the org-default
// without misclassifying a system error.
var ErrLinearTeamRepoMappingNotFound = errors.New("linear team repo mapping not found")
