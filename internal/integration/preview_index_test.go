//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// seedRepository inserts the integrations → repositories FK chain that
// preview targets hang off. Raw SQL keeps the test independent of the GitHub
// sync path that normally creates these rows.
func seedRepository(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, fullName string) uuid.UUID {
	t.Helper()
	var integrationID uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO integrations (org_id, provider)
		VALUES ($1, 'github')
		RETURNING id
	`, orgID).Scan(&integrationID)
	require.NoError(t, err, "seed integration")

	var repoID uuid.UUID
	err = pool.QueryRow(context.Background(), `
		INSERT INTO repositories (org_id, integration_id, github_id, full_name, clone_url, installation_id)
		VALUES ($1, $2, $3, $4, $5, 1)
		RETURNING id
	`, orgID, integrationID, time.Now().UnixNano(), fullName,
		fmt.Sprintf("https://github.com/%s.git", fullName)).Scan(&repoID)
	require.NoError(t, err, "seed repository")
	return repoID
}

// seedPreviewTargetWithInstance creates a preview target plus one runtime
// attempt in the given status, mirroring what the branch preview launch path
// persists.
func seedPreviewTargetWithInstance(t *testing.T, store *db.PreviewStore, orgID, repoID, userID uuid.UUID, branch string, sourceType models.PreviewSourceType, sourceID string, status models.PreviewStatus) models.PreviewTarget {
	t.Helper()
	target := models.PreviewTarget{
		OrgID:           orgID,
		RepositoryID:    repoID,
		Branch:          branch,
		CommitSHA:       "abc123" + branch,
		SourceType:      sourceType,
		SourceID:        sourceID,
		CreatedByUserID: userID,
	}
	require.NoError(t, store.CreatePreviewTarget(context.Background(), &target), "seed preview target")

	instance := models.PreviewInstance{
		PreviewTargetID: &target.ID,
		OrgID:           orgID,
		UserID:          userID,
		ProfileName:     "bootstrap",
		Status:          status,
		Provider:        "docker",
		ExpiresAt:       time.Now().Add(30 * time.Minute),
		LastPath:        "/",
		MemoryLimitMB:   512,
		CPULimitMillis:  500,
		DiskLimitMB:     10240,
	}
	require.NoError(t, store.CreateBranchPreviewInstance(context.Background(), &instance), "seed preview instance")
	return target
}

// TestIntegration_PreviewIndex_ListAndCountScopes executes the previews index
// list + scope-count SQL against real Postgres. The previews page polls both
// queries every few seconds, so a SQL-level regression here takes the whole
// /previews page down (it renders the 500 as an empty/loading flicker).
// pgxmock unit tests cannot catch errors Postgres raises at plan time — this
// suite exists precisely for bugs like the ambiguous "status" column reference
// (SQLSTATE 42702) that shipped in the original count query.
func TestIntegration_PreviewIndex_ListAndCountScopes(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	repoID := seedRepository(t, pool, orgID, "acme/app")
	store := db.NewPreviewStore(pool)

	running := seedPreviewTargetWithInstance(t, store, orgID, repoID, user.ID,
		"feature-running", models.PreviewSourceTypePullRequest, "acme/app#42@abc123", models.PreviewStatusReady)
	stopped := seedPreviewTargetWithInstance(t, store, orgID, repoID, user.ID,
		"feature-stopped", models.PreviewSourceTypeManual, "", models.PreviewStatusStopped)

	scopeWant := map[string][]uuid.UUID{
		"":          {running.ID, stopped.ID},
		"running":   {running.ID},
		"resumable": {}, // stopped target has no warm snapshot cache entry
		"recent":    {stopped.ID},
	}
	for scope, want := range scopeWant {
		summaries, err := store.ListBranchPreviewIndex(context.Background(), orgID, db.BranchPreviewIndexFilters{
			Scope: scope,
			Limit: 20,
		})
		require.NoError(t, err, "ListBranchPreviewIndex scope=%q", scope)
		got := make([]uuid.UUID, 0, len(summaries))
		for _, summary := range summaries {
			got = append(got, summary.TargetID)
		}
		require.ElementsMatch(t, want, got, "scope=%q should return exactly the seeded targets in it", scope)
	}

	// The q filter has its own SQL surface (ILIKE branches plus the PR-number
	// regexp on source_id) — execute it for real too.
	byPRNumber, err := store.ListBranchPreviewIndex(context.Background(), orgID, db.BranchPreviewIndexFilters{
		Query: "#42",
		Limit: 20,
	})
	require.NoError(t, err, "ListBranchPreviewIndex with PR-number query")
	require.Len(t, byPRNumber, 1, "q=#42 should match only the PR-sourced target")
	require.Equal(t, running.ID, byPRNumber[0].TargetID)

	counts, err := store.CountBranchPreviewIndexScopes(context.Background(), orgID, db.BranchPreviewIndexFilters{})
	require.NoError(t, err, "CountBranchPreviewIndexScopes")
	require.Equal(t, map[string]int{"running": 1, "resumable": 0, "recent": 1}, counts)
}
