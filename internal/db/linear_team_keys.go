package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearTeamKey is a cached Linear team prefix (e.g. "ACS") used by detection
// to gate bare-identifier matches. Without this allowlist, "JIRA-1234" or
// "AWS-001" would false-positive as Linear refs.
type LinearTeamKey struct {
	OrgID         uuid.UUID `db:"org_id" json:"org_id"`
	IntegrationID uuid.UUID `db:"integration_id" json:"integration_id"`
	WorkspaceID   string    `db:"workspace_id" json:"workspace_id"`
	TeamID        string    `db:"team_id" json:"team_id"`
	TeamKey       string    `db:"team_key" json:"team_key"`
	TeamName      string    `db:"team_name" json:"team_name"`
	RefreshedAt   time.Time `db:"refreshed_at" json:"refreshed_at"`
}

// LinearTeamKeyStore reads and writes the team-key allowlist used by
// detection's bare-identifier branch. Refreshed on OAuth install and every
// 24h via the integration sync worker.
type LinearTeamKeyStore struct {
	db DBTX
}

func NewLinearTeamKeyStore(db DBTX) *LinearTeamKeyStore {
	return &LinearTeamKeyStore{db: db}
}

// ListByOrg returns the org's team-key entries scoped to integrations that
// are currently `active`. Detection consumes this allowlist as authoritative
// for "is this bare identifier a Linear ref?" — a stale row from a paused
// or dead integration could otherwise match a key the org no longer owns,
// and resolution would route the write through the active integration's
// token to a different workspace's issue (cross-workspace token misuse).
//
// The JOIN against `integrations` is the cheapest place to enforce this:
// it keeps the cache append-only (we don't need to delete rows when an
// integration leaves active status), and the cardinality is low enough
// that the join cost is negligible compared with the remote Linear fetch
// every detection avoids.
func (s *LinearTeamKeyStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]LinearTeamKey, error) {
	rows, err := s.db.Query(ctx, `
		SELECT k.org_id, k.integration_id, k.workspace_id, k.team_id, k.team_key, k.team_name, k.refreshed_at
		FROM linear_team_keys k
		JOIN integrations i ON i.id = k.integration_id
		WHERE k.org_id = @org_id
		  AND i.status = 'active'`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query linear team keys: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearTeamKey])
}

// ReplaceForIntegration is called after a fresh team list fetch from Linear:
// it upserts the current set so detection sees the latest cache. Stale rows
// not in `teams` are removed for the same integration.
//
// Wrapped in a transaction so a concurrent session-create that runs
// detection during the refresh sees either the old set or the new set,
// never an empty cache between DELETE and the first INSERT. Falls back
// to the non-transactional path when the DB handle doesn't support it
// (pgxmock in some tests) — production always passes a *pgxpool.Pool.
func (s *LinearTeamKeyStore) ReplaceForIntegration(ctx context.Context, orgID, integrationID uuid.UUID, workspaceID string, teams []LinearTeamKey) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return s.replaceForIntegrationNoTx(ctx, s.db, orgID, integrationID, workspaceID, teams)
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin linear team keys tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.replaceForIntegrationNoTx(ctx, tx, orgID, integrationID, workspaceID, teams); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit linear team keys: %w", err)
	}
	return nil
}

func (s *LinearTeamKeyStore) replaceForIntegrationNoTx(ctx context.Context, ex DBTX, orgID, integrationID uuid.UUID, workspaceID string, teams []LinearTeamKey) error {
	if _, err := ex.Exec(ctx, `
		DELETE FROM linear_team_keys WHERE integration_id = @integration_id AND org_id = @org_id`,
		pgx.NamedArgs{
			"integration_id": integrationID,
			"org_id":         orgID,
		}); err != nil {
		return fmt.Errorf("clear linear team keys: %w", err)
	}

	for _, t := range teams {
		if _, err := ex.Exec(ctx, `
			INSERT INTO linear_team_keys (org_id, integration_id, workspace_id, team_id, team_key, team_name, refreshed_at)
			VALUES (@org_id, @integration_id, @workspace_id, @team_id, @team_key, @team_name, now())
			ON CONFLICT (integration_id, team_key) DO UPDATE
			SET team_name = EXCLUDED.team_name,
			    workspace_id = EXCLUDED.workspace_id,
			    team_id = EXCLUDED.team_id,
			    refreshed_at = now()`,
			pgx.NamedArgs{
				"org_id":         orgID,
				"integration_id": integrationID,
				"workspace_id":   workspaceID,
				"team_id":        t.TeamID,
				"team_key":       t.TeamKey,
				"team_name":      t.TeamName,
			}); err != nil {
			return fmt.Errorf("insert linear team key %q: %w", t.TeamKey, err)
		}
	}
	return nil
}
