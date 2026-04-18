package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func newTeamRow(orgID uuid.UUID, name, slug string, ghTeamID *int64, memberCount int, now time.Time) ([]string, []any) {
	cols := []string{
		"id", "org_id", "name", "slug", "description",
		"github_team_id", "github_team_slug",
		"created_at", "updated_at", "member_count",
	}
	id := uuid.New()
	return cols, []any{
		id, orgID, name, slug, nil,
		ghTeamID, nil,
		now, now, memberCount,
	}
}

func TestTeamStore_Create(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	now := time.Now()
	generatedID := uuid.New()

	mock.ExpectQuery("INSERT INTO teams").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(generatedID, now, now))

	team := &models.Team{OrgID: orgID, Name: "Frontend", Slug: "frontend"}
	require.NoError(t, store.Create(context.Background(), team))
	require.Equal(t, generatedID, team.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamStore_Delete_PlainDelete(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	teamID := uuid.New()

	// Verifies the CTE was dropped — only a plain DELETE FROM teams remains;
	// FK ON DELETE SET NULL clears session/project team_id references.
	mock.ExpectExec(`^DELETE FROM teams WHERE id = @id AND org_id = @org_id$`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	require.NoError(t, store.Delete(context.Background(), orgID, teamID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamStore_Delete_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	mock.ExpectExec("DELETE FROM teams").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = store.Delete(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamStore_AddMember_NoMatchingTeamOrUser(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	mock.ExpectExec("INSERT INTO team_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	err = store.AddMember(context.Background(), uuid.New(), uuid.New(), uuid.New(), models.TeamRoleMember)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamStore_RemoveMember_NotInTeam(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	mock.ExpectExec("DELETE FROM team_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = store.RemoveMember(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamStore_ListByOrg(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	now := time.Now()
	cols, row := newTeamRow(orgID, "Frontend", "frontend", nil, 3, now)

	mock.ExpectQuery("SELECT .+ FROM teams t").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(row...))

	teams, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, teams, 1)
	require.Equal(t, "Frontend", teams[0].Name)
	require.Equal(t, 3, teams[0].MemberCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- SyncFromGitHub branches ---

// expectGitHubLookupHit sets up the SELECT-by-github_team_id branch returning a row.
func expectGitHubLookupHit(mock pgxmock.PgxPoolIface, existingTeamID uuid.UUID) {
	mock.ExpectQuery(`SELECT id FROM teams\s+WHERE org_id = @org_id AND github_team_id = @github_team_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(existingTeamID))
}

// expectGitHubLookupMiss sets up the SELECT-by-github_team_id branch returning no rows.
func expectGitHubLookupMiss(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`SELECT id FROM teams\s+WHERE org_id = @org_id AND github_team_id = @github_team_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
}

func TestSyncFromGitHub_LinkByGitHubTeamID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	existingID := uuid.New()

	mock.ExpectBegin()
	expectGitHubLookupHit(mock, existingID)
	// updateGitHubTeamByID
	mock.ExpectExec(`UPDATE teams AS t`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// No members → DELETE clear
	mock.ExpectExec(`DELETE FROM team_memberships WHERE team_id = @team_id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	err = store.SyncFromGitHub(context.Background(), orgID, []GitHubTeamSync{{
		GitHubTeamID:   42,
		GitHubTeamSlug: "frontend",
		Name:           "Frontend",
	}})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncFromGitHub_FreshInsert(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	newID := uuid.New()

	mock.ExpectBegin()
	expectGitHubLookupMiss(mock)
	// INSERT ... ON CONFLICT DO NOTHING RETURNING id → returns id
	mock.ExpectQuery(`INSERT INTO teams \(org_id, name, slug, description, github_team_id, github_team_slug\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(newID))
	// No members → DELETE clear
	mock.ExpectExec(`DELETE FROM team_memberships WHERE team_id = @team_id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	err = store.SyncFromGitHub(context.Background(), orgID, []GitHubTeamSync{{
		GitHubTeamID: 99, GitHubTeamSlug: "platform", Name: "Platform",
	}})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncFromGitHub_AdoptManualTeamBySlug(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()
	adoptedID := uuid.New()

	mock.ExpectBegin()
	expectGitHubLookupMiss(mock)
	// INSERT collides on slug → returns ErrNoRows
	mock.ExpectQuery(`INSERT INTO teams \(org_id, name, slug, description, github_team_id, github_team_slug\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	// UPDATE adopts manual team (github_team_id IS NULL) → returns adopted id
	mock.ExpectQuery(`UPDATE teams\s+SET name = @name`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(adoptedID))
	mock.ExpectExec(`DELETE FROM team_memberships WHERE team_id = @team_id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	err = store.SyncFromGitHub(context.Background(), orgID, []GitHubTeamSync{{
		GitHubTeamID: 7, GitHubTeamSlug: "frontend", Name: "Frontend",
	}})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncFromGitHub_SkipWhenSlugOwnedByDifferentGitHubTeam(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()

	mock.ExpectBegin()
	expectGitHubLookupMiss(mock)
	mock.ExpectQuery(`INSERT INTO teams \(org_id, name, slug, description, github_team_id, github_team_slug\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	// UPDATE adoption fails because slug is owned by another github-linked team
	// (WHERE github_team_id IS NULL filters us out).
	mock.ExpectQuery(`UPDATE teams\s+SET name = @name`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()

	err = store.SyncFromGitHub(context.Background(), orgID, []GitHubTeamSync{{
		GitHubTeamID: 13, GitHubTeamSlug: "frontend", Name: "Frontend",
	}})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncFromGitHub_RollsBackOnLookupError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewTeamStore(mock)
	orgID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM teams\s+WHERE org_id = @org_id AND github_team_id = @github_team_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	err = store.SyncFromGitHub(context.Background(), orgID, []GitHubTeamSync{{
		GitHubTeamID: 1, GitHubTeamSlug: "x", Name: "X",
	}})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
