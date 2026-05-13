package linear

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// fakeRepoLookup implements AgentRepoLookup. label-override tests set
// repos to drive the resolver's first-tier check; misses return a sentinel
// error so we can verify the resolver falls through to the team mapping.
type fakeRepoLookup struct {
	byFullName map[string]models.Repository
}

func (f *fakeRepoLookup) GetByFullName(_ context.Context, _ uuid.UUID, fullName string) (models.Repository, error) {
	if repo, ok := f.byFullName[fullName]; ok {
		return repo, nil
	}
	return models.Repository{}, errors.New("repo not found")
}

// fakeSettingsLoader implements AgentSettingsLoader for the org-default
// fallback tier. err lets a test simulate a misconfigured org_settings
// JSONB blob without standing up the OrgStore.
type fakeSettingsLoader struct {
	settings models.LinearAgentSettings
	err      error
}

func (f *fakeSettingsLoader) LoadAgentSettings(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
	if f.err != nil {
		return models.LinearAgentSettings{}, f.err
	}
	return f.settings, nil
}

type resolverRig struct {
	mock     pgxmock.PgxPoolIface
	resolver *AgentRepoResolver
	repos    *fakeRepoLookup
	settings *fakeSettingsLoader
	orgID    uuid.UUID
}

func newResolverRig(t *testing.T) *resolverRig {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	repos := &fakeRepoLookup{byFullName: map[string]models.Repository{}}
	settings := &fakeSettingsLoader{}
	resolver := NewAgentRepoResolver(db.NewLinearTeamRepoMappingStore(mock), settings, repos)
	return &resolverRig{
		mock:     mock,
		resolver: resolver,
		repos:    repos,
		settings: settings,
		orgID:    uuid.New(),
	}
}

// expectMappingHit configures the mock to return a row from
// LinearTeamRepoMappingStore.Resolve with the requested project scope. A
// nil projectID models the team-default row (linear_project_id IS NULL).
func expectMappingHit(t *testing.T, mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID, projectID *string, branch string) {
	t.Helper()
	now := time.Now().UTC()
	mock.ExpectQuery("(?s)SELECT.*FROM linear_team_repo_mappings.*ORDER BY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "linear_team_id", "linear_project_id",
			"repository_id", "default_branch", "priority",
			"created_at", "updated_at",
		}).AddRow(
			uuid.New(), orgID, "team_X", projectID,
			repoID, branch, 0, now, now,
		))
}

// expectMappingMiss configures the mock to return pgx.ErrNoRows so the
// resolver falls through to the org-default tier.
func expectMappingMiss(t *testing.T, mock pgxmock.PgxPoolIface) {
	t.Helper()
	mock.ExpectQuery("(?s)SELECT.*FROM linear_team_repo_mappings.*ORDER BY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "linear_team_id", "linear_project_id",
			"repository_id", "default_branch", "priority",
			"created_at", "updated_at",
		}))
}

// TestAgentRepoResolver_PriorityFallthrough walks the documented
// priority order tier by tier, asserting both the chosen repository and
// the Source label that the operator debug surface relies on. Each
// scenario disables the higher tiers so a regression that scrambles the
// order shows up immediately.
func TestAgentRepoResolver_PriorityFallthrough(t *testing.T) {
	t.Parallel()

	t.Run("label override wins over everything else", func(t *testing.T) {
		t.Parallel()
		rig := newResolverRig(t)
		labelRepo := models.Repository{ID: uuid.New(), FullName: "org/labelled"}
		rig.repos.byFullName["org/labelled"] = labelRepo

		// No mock expectation: the label-override branch must short-circuit
		// before any DB query so we never hit the mappings store.
		got, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: "proj_Y",
			Labels:          []string{"repo:org/labelled"},
		})
		require.NoError(t, err)
		require.Equal(t, labelRepo.ID, got.RepositoryID)
		require.Equal(t, "label_override", got.Source)
		require.NoError(t, rig.mock.ExpectationsWereMet(),
			"label override must not hit the mappings store")
	})

	t.Run("falls through to team+project mapping when label is absent", func(t *testing.T) {
		t.Parallel()
		rig := newResolverRig(t)
		project := "proj_Y"
		repoID := uuid.New()
		expectMappingHit(t, rig.mock, rig.orgID, repoID, &project, "develop")

		got, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: project,
		})
		require.NoError(t, err)
		require.Equal(t, repoID, got.RepositoryID)
		require.Equal(t, "team_project_mapping", got.Source)
		require.Equal(t, "develop", got.DefaultBranch)
		require.NoError(t, rig.mock.ExpectationsWereMet())
	})

	t.Run("falls through to team default when no project mapping exists", func(t *testing.T) {
		t.Parallel()
		rig := newResolverRig(t)
		repoID := uuid.New()
		// projectID nil on the row => linear_project_id IS NULL =>
		// team-default fallback per the resolver's source classifier.
		expectMappingHit(t, rig.mock, rig.orgID, repoID, nil, "")

		got, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: "proj_Y",
		})
		require.NoError(t, err)
		require.Equal(t, repoID, got.RepositoryID)
		require.Equal(t, "team_default_mapping", got.Source)
		require.NoError(t, rig.mock.ExpectationsWereMet())
	})

	t.Run("falls through to org default when no mapping rows match", func(t *testing.T) {
		t.Parallel()
		rig := newResolverRig(t)
		expectMappingMiss(t, rig.mock)
		orgRepo := uuid.New()
		rig.settings.settings = models.LinearAgentSettings{DefaultRepoID: &orgRepo}

		got, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: "proj_Y",
		})
		require.NoError(t, err)
		require.Equal(t, orgRepo, got.RepositoryID)
		require.Equal(t, "org_default", got.Source)
		require.NoError(t, rig.mock.ExpectationsWereMet())
	})

	t.Run("returns ErrAgentRepoUnmapped when every tier misses", func(t *testing.T) {
		t.Parallel()
		rig := newResolverRig(t)
		expectMappingMiss(t, rig.mock)
		// settings.DefaultRepoID stays nil -> org default also misses.

		_, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: "proj_Y",
			Labels:          []string{"repo:org/missing"},
		})
		require.ErrorIs(t, err, ErrAgentRepoUnmapped)
		require.NoError(t, rig.mock.ExpectationsWereMet())
	})

	t.Run("unknown label falls through instead of erroring", func(t *testing.T) {
		t.Parallel()
		// Regression guard for the documented "label that points to a
		// repo the org doesn't have is a user error, not a system error"
		// branch in resolveLabelOverride.
		rig := newResolverRig(t)
		project := "proj_Y"
		repoID := uuid.New()
		expectMappingHit(t, rig.mock, rig.orgID, repoID, &project, "")

		got, err := rig.resolver.Resolve(context.Background(), AgentRepoResolveInput{
			OrgID:           rig.orgID,
			LinearTeamID:    "team_X",
			LinearProjectID: project,
			Labels:          []string{"repo:org/never-existed"},
		})
		require.NoError(t, err)
		require.Equal(t, repoID, got.RepositoryID,
			"unknown repo label must not abort the resolver; it should fall to the team+project mapping")
		require.Equal(t, "team_project_mapping", got.Source)
		require.NoError(t, rig.mock.ExpectationsWereMet())
	})
}
