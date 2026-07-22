//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitHubRateLimitObservationRejectsOutOfOrderSnapshots(t *testing.T) {
	// Integration tests share and truncate one database, so they cannot run in parallel.
	pool := setup(t)
	seedGitHubInstallation(t, pool, 143)
	store := db.NewGitHubRateLimitStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Hour)

	require.NoError(t, store.Observe(context.Background(), githubQuotaObservation(143, 4000, reset, now)), "fresh quota should persist")
	require.NoError(t, store.Observe(context.Background(), githubQuotaObservation(143, 4500, reset, now.Add(-time.Minute))), "out-of-order same-window quota should be accepted conservatively")
	require.NoError(t, store.Observe(context.Background(), githubQuotaObservation(143, 100, reset.Add(-time.Hour), now.Add(time.Minute))), "an older reset window should not replace the current window")

	var limit, remaining int
	var actualReset, observedAt time.Time
	err := pool.QueryRow(context.Background(), `
		SELECT limit_count, remaining_count, reset_at, observed_at
		FROM github_installation_rate_limits
		WHERE installation_id = $1 AND resource = 'core'
	`, int64(143)).Scan(&limit, &remaining, &actualReset, &observedAt)
	require.NoError(t, err, "persisted quota should be queryable")
	require.Equal(t, 5000, limit, "the quota limit should remain exact")
	require.Equal(t, 4000, remaining, "same-window observations must only decrease remaining quota")
	require.True(t, reset.Equal(actualReset), "an older reset window must not replace the current window")
	require.True(t, now.Equal(observedAt), "an ignored older window must not make the accepted quota snapshot appear fresh")
}

func TestIntegrationGitHubRateLimitConcurrentReservationsSerialize(t *testing.T) {
	// Integration tests share and truncate one database, so they cannot run in parallel.
	pool := setup(t)
	orgID := seedOrg(t, pool)
	seedGitHubInstallation(t, pool, 143)
	metadataIDs := seedGitHubRateLimitMetadata(t, pool, orgID, 2)
	store := db.NewGitHubRateLimitStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.Observe(context.Background(), githubQuotaObservation(143, 600, now.Add(time.Hour), now)), "initial quota should persist")

	decisions, errs := reserveConcurrently(store, orgID, 143, metadataIDs, now)
	require.NoError(t, errs[0], "first concurrent admission should complete")
	require.NoError(t, errs[1], "second concurrent admission should complete")
	allowed := 0
	for _, decision := range decisions {
		if decision.Allowed {
			allowed++
		}
	}
	require.Equal(t, 1, allowed, "the installation lock should admit only one review above the recovery floor")

	var activeReserved int
	err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(reserved_units), 0)
		FROM github_installation_rate_reservations
		WHERE installation_id = $1 AND released_at IS NULL
	`, int64(143)).Scan(&activeReserved)
	require.NoError(t, err, "active reservations should be queryable")
	require.Equal(t, 100, activeReserved, "only the admitted review should reserve installation quota")
}

func TestIntegrationGitHubRateLimitConcurrentBootstrapUsesSingleLease(t *testing.T) {
	// Integration tests share and truncate one database, so they cannot run in parallel.
	pool := setup(t)
	orgID := seedOrg(t, pool)
	metadataIDs := seedGitHubRateLimitMetadata(t, pool, orgID, 2)
	store := db.NewGitHubRateLimitStore(pool)
	now := time.Now().UTC().Truncate(time.Second)

	decisions, errs := reserveConcurrently(store, orgID, 1, metadataIDs, now)
	require.NoError(t, errs[0], "first bootstrap admission should complete")
	require.NoError(t, errs[1], "second bootstrap admission should complete")
	refreshRequired := 0
	bootstrap := 0
	for _, decision := range decisions {
		if decision.RefreshRequired {
			refreshRequired++
		}
		if decision.Bootstrap {
			bootstrap++
		}
	}
	require.Equal(t, 1, refreshRequired, "only one review should own the bootstrap refresh lease")
	require.Equal(t, 2, bootstrap, "both decisions should identify the unknown quota state")
	var installationCount int
	err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM github_installations WHERE installation_id = 1`).Scan(&installationCount)
	require.NoError(t, err, "legacy repository installation should be queryable")
	require.Equal(t, 1, installationCount, "admission should materialize a missing global installation for a legacy repository")
}

func TestIntegrationGitHubRateLimitReleasesTerminalReservation(t *testing.T) {
	// Integration tests share and truncate one database, so they cannot run in parallel.
	pool := setup(t)
	orgID := seedOrg(t, pool)
	seedGitHubInstallation(t, pool, 143)
	metadataIDs := seedGitHubRateLimitMetadata(t, pool, orgID, 2)
	store := db.NewGitHubRateLimitStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.Observe(context.Background(), githubQuotaObservation(143, 600, now.Add(time.Hour), now)), "initial quota should persist")

	first, err := store.ReserveCodeReview(context.Background(), orgID, 143, metadataIDs[0], now)
	require.NoError(t, err, "first review admission should complete")
	require.True(t, first.Allowed, "first review should fit above the recovery floor")
	_, err = pool.Exec(context.Background(), `
		UPDATE code_review_session_metadata
		SET status = 'completed', completed_at = $1
		WHERE org_id = $2 AND id = $3
	`, now, orgID, metadataIDs[0])
	require.NoError(t, err, "first review metadata should become terminal")

	second, err := store.ReserveCodeReview(context.Background(), orgID, 143, metadataIDs[1], now.Add(time.Second))
	require.NoError(t, err, "second review admission should complete")
	require.True(t, second.Allowed, "terminal review units should be reusable by the next review")

	var activeUnits, releasedRows int
	err = pool.QueryRow(context.Background(), `
		SELECT
			COALESCE(SUM(reserved_units) FILTER (WHERE released_at IS NULL), 0),
			COUNT(*) FILTER (WHERE released_at IS NOT NULL)
		FROM github_installation_rate_reservations
		WHERE installation_id = $1
	`, int64(143)).Scan(&activeUnits, &releasedRows)
	require.NoError(t, err, "reservation lifecycle should be queryable")
	require.Equal(t, 100, activeUnits, "only the second review should retain active reserved units")
	require.Equal(t, 1, releasedRows, "terminal review reservation should be marked released")
}

func githubQuotaObservation(installationID int64, remaining int, resetAt, observedAt time.Time) models.GitHubRateLimitObservation {
	limit := 5000
	return models.GitHubRateLimitObservation{
		InstallationID: installationID,
		Resource:       models.GitHubRateLimitResourceCore,
		Limit:          &limit,
		Remaining:      &remaining,
		ResetAt:        &resetAt,
		ObservedAt:     observedAt,
	}
}

func reserveConcurrently(store *db.GitHubRateLimitStore, orgID uuid.UUID, installationID int64, metadataIDs []uuid.UUID, now time.Time) ([]models.GitHubRateLimitDecision, []error) {
	decisions := make([]models.GitHubRateLimitDecision, len(metadataIDs))
	errs := make([]error, len(metadataIDs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, metadataID := range metadataIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			decisions[index], errs[index] = store.ReserveCodeReview(context.Background(), orgID, installationID, metadataID, now)
		}()
	}
	close(start)
	wg.Wait()
	return decisions, errs
}

func seedGitHubInstallation(t *testing.T, pool *pgxpool.Pool, installationID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO github_installations (installation_id, account_id, account_login)
		VALUES ($1, $2, $3)
	`, installationID, installationID, fmt.Sprintf("integration-%d", installationID))
	require.NoError(t, err, "seed GitHub installation")
}

func seedGitHubRateLimitMetadata(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, count int) []uuid.UUID {
	t.Helper()
	repoID := seedRepository(t, pool, orgID, "integration/rate-budget")
	var policyID uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO code_review_policies (
			org_id, version, approval_mode, description_policy, risk_policy, agent_roster,
			inline_comment_limit, review_instructions, automated_approval_policy
		) VALUES ($1, 1, 'comment_only', '{}', '{}', '{}', 4, 'Review the change.', 'Approve routine changes.')
		RETURNING id
	`, orgID).Scan(&policyID)
	require.NoError(t, err, "seed code review policy")

	metadataIDs := make([]uuid.UUID, 0, count)
	for index := 0; index < count; index++ {
		session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending, Origin: models.SessionOriginCodeReview, RepositoryID: &repoID})
		var pullRequestID uuid.UUID
		err := pool.QueryRow(context.Background(), `
			INSERT INTO pull_requests (org_id, github_pr_number, github_pr_url, github_repo, title)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id
		`, orgID, index+1, fmt.Sprintf("https://github.com/integration/rate-budget/pull/%d", index+1), "integration/rate-budget", fmt.Sprintf("PR %d", index+1)).Scan(&pullRequestID)
		require.NoError(t, err, "seed pull request")

		var metadataID uuid.UUID
		err = pool.QueryRow(context.Background(), `
			INSERT INTO code_review_session_metadata (
				org_id, session_id, repository_id, pull_request_id, policy_id,
				base_sha, head_sha, trigger_source, status, review_output_key
			) VALUES ($1, $2, $3, $4, $5, 'base', $6, 'team_reviewer', 'queued', $7)
			RETURNING id
		`, orgID, session.ID, repoID, pullRequestID, policyID, fmt.Sprintf("head-%d", index), fmt.Sprintf("output-%d-%s", index, uuid.NewString())).Scan(&metadataID)
		require.NoError(t, err, "seed code review metadata")
		metadataIDs = append(metadataIDs, metadataID)
	}
	return metadataIDs
}
