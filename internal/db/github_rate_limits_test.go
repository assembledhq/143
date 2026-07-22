package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestValidateGitHubRateLimitObservation(t *testing.T) {
	t.Parallel()

	limit := 5000
	remaining := 4500
	reset := time.Now().UTC().Add(time.Hour)
	blocked := time.Now().UTC().Add(time.Minute)
	tests := []struct {
		name        string
		observation models.GitHubRateLimitObservation
		expectErr   bool
	}{
		{name: "complete quota", observation: models.GitHubRateLimitObservation{Limit: &limit, Remaining: &remaining, ResetAt: &reset}},
		{name: "secondary block only", observation: models.GitHubRateLimitObservation{BlockedUntil: &blocked}},
		{name: "missing remaining", observation: models.GitHubRateLimitObservation{Limit: &limit, ResetAt: &reset}, expectErr: true},
		{name: "remaining exceeds limit", observation: models.GitHubRateLimitObservation{Limit: &remaining, Remaining: &limit, ResetAt: &reset}, expectErr: true},
		{name: "empty observation", observation: models.GitHubRateLimitObservation{}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateGitHubRateLimitObservation(tt.observation)
			if tt.expectErr {
				require.Error(t, err, "invalid quota observations should be rejected before persistence")
				return
			}
			require.NoError(t, err, "complete quota or secondary block observations should validate")
		})
	}
}

func TestGitHubRateLimitStoreObserveUsesMonotonicUpsert(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	limit := 5000
	remaining := 4321
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("github_rate_limit:143").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO github_installations").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "account_login": "installation-143"}).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectExec("INSERT INTO github_installation_rate_limits").
		WithArgs(pgx.NamedArgs{
			"installation_id": int64(143),
			"resource":        models.GitHubRateLimitResourceCore,
			"limit_count":     &limit,
			"remaining_count": &remaining,
			"reset_at":        &reset,
			"blocked_until":   (*time.Time)(nil),
			"observed_at":     now,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = NewGitHubRateLimitStore(mock).Observe(context.Background(), models.GitHubRateLimitObservation{
		InstallationID: 143,
		Resource:       models.GitHubRateLimitResourceCore,
		Limit:          &limit,
		Remaining:      &remaining,
		ResetAt:        &reset,
		ObservedAt:     now,
	})

	require.NoError(t, err, "valid GitHub quota headers should persist")
	require.NoError(t, mock.ExpectationsWereMet(), "observation should use the installation-global upsert")
}

func TestDecideGitHubCodeReviewAdmission(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	blocked := now.Add(2 * time.Minute)
	expired := now.Add(-time.Second)
	staleObservation := now.Add(-githubRateObservationMaxAge - time.Second)
	activeBootstrap := now.Add(-time.Minute)
	limit := 5000
	highRemaining := 1500
	lowRemaining := 1099
	tests := []struct {
		name           string
		state          githubRateLimitState
		activeReserved int
		existing       bool
		expected       models.GitHubRateLimitDecision
	}{
		{
			name:           "admits known capacity above recovery floor",
			state:          githubRateLimitState{Limit: &limit, Remaining: &highRemaining, ResetAt: &reset, ObservedAt: &now},
			activeReserved: 800,
			expected:       models.GitHubRateLimitDecision{Allowed: true, Known: true, Limit: 5000, Remaining: 1500, ActiveReserved: 800, RecoveryReserve: 500, ResetAt: reset},
		},
		{
			name:           "denies capacity that would cross recovery floor",
			state:          githubRateLimitState{Limit: &limit, Remaining: &lowRemaining, ResetAt: &reset, ObservedAt: &now},
			activeReserved: 500,
			expected:       models.GitHubRateLimitDecision{Known: true, Limit: 5000, Remaining: 1099, ActiveReserved: 500, RecoveryReserve: 500, ResetAt: reset, RetryAfter: time.Hour},
		},
		{
			name:     "requires a quota refresh when state is unknown",
			state:    githubRateLimitState{},
			expected: models.GitHubRateLimitDecision{Bootstrap: true, RefreshRequired: true},
		},
		{
			name:           "old reservations do not prevent a new quota bootstrap",
			state:          githubRateLimitState{Limit: &limit, Remaining: &highRemaining, ResetAt: &expired, ObservedAt: &staleObservation},
			activeReserved: 100,
			expected:       models.GitHubRateLimitDecision{Bootstrap: true, RefreshRequired: true, ActiveReserved: 100},
		},
		{
			name:     "denies a second bootstrap while the lease is active",
			state:    githubRateLimitState{BootstrapAt: &activeBootstrap},
			expected: models.GitHubRateLimitDecision{Bootstrap: true, RetryAfter: githubRateBootstrapRetry},
		},
		{
			name:     "treats a stale observation as bootstrap-only",
			state:    githubRateLimitState{Limit: &limit, Remaining: &highRemaining, ResetAt: &reset, ObservedAt: &staleObservation},
			expected: models.GitHubRateLimitDecision{Bootstrap: true, RefreshRequired: true},
		},
		{
			name:     "lets an existing reservation resume against stale quota",
			state:    githubRateLimitState{Limit: &limit, Remaining: &highRemaining, ResetAt: &reset, ObservedAt: &staleObservation},
			existing: true,
			expected: models.GitHubRateLimitDecision{Allowed: true, ExistingReservation: true},
		},
		{
			name:     "honors secondary block",
			state:    githubRateLimitState{Limit: &limit, Remaining: &highRemaining, ResetAt: &reset, BlockedUntil: &blocked, ObservedAt: &now},
			expected: models.GitHubRateLimitDecision{Known: true, Limit: 5000, Remaining: 1500, RecoveryReserve: 500, ResetAt: reset, BlockedUntil: blocked, RetryAfter: 2 * time.Minute},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metadataID := uuid.New()
			tt.expected.MetadataID = metadataID
			actual := decideGitHubCodeReviewAdmission(tt.state, tt.activeReserved, tt.existing, metadataID, now)
			require.Equal(t, tt.expected, actual, "admission should preserve capacity for existing review recovery")
		})
	}
}

func TestGitHubRateLimitStoreReserveCodeReviewCreatesAtomicReservation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("github_rate_limit:143").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO github_installations").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "account_login": "installation-143"}).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectExec("UPDATE github_installation_rate_reservations").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "released_at": now}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("SELECT reserved_units").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "installation_id": int64(143), "metadata_id": metadataID, "resource": models.GitHubRateLimitResourceCore}).
		WillReturnRows(pgxmock.NewRows([]string{"reserved_units"}))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "resource": models.GitHubRateLimitResourceCore}).
		WillReturnRows(pgxmock.NewRows([]string{"active_reserved"}).AddRow(300))
	mock.ExpectQuery("SELECT resource, limit_count, remaining_count").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143)}).
		WillReturnRows(pgxmock.NewRows([]string{"resource", "limit_count", "remaining_count", "reset_at", "blocked_until", "observed_at", "bootstrap_reserved_at"}).
			AddRow(models.GitHubRateLimitResourceCore, 5000, 1200, reset, nil, now, nil))
	mock.ExpectExec("INSERT INTO github_installation_rate_reservations").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "installation_id": int64(143), "metadata_id": metadataID,
			"resource": models.GitHubRateLimitResourceCore, "reserved_units": 100,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	decision, err := NewGitHubRateLimitStore(mock).ReserveCodeReview(context.Background(), orgID, 143, metadataID, now)

	require.NoError(t, err, "capacity above the recovery floor should create a reservation")
	require.Equal(t, models.GitHubRateLimitDecision{
		Allowed: true, Known: true, Limit: 5000, Remaining: 1200, ActiveReserved: 300,
		RecoveryReserve: 500, ResetAt: reset, MetadataID: metadataID,
	}, decision, "reservation should return the locked installation-wide budget decision")
	require.NoError(t, mock.ExpectationsWereMet(), "admission should lock, calculate, insert, and commit atomically")
}

func TestGitHubRateLimitStoreReserveCodeReviewExistingReservationHonorsGraphQLBlock(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	blocked := now.Add(90 * time.Second)
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("github_rate_limit:143").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO github_installations").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "account_login": "installation-143"}).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectExec("UPDATE github_installation_rate_reservations").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "released_at": now}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("SELECT reserved_units").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "installation_id": int64(143), "metadata_id": metadataID, "resource": models.GitHubRateLimitResourceCore}).
		WillReturnRows(pgxmock.NewRows([]string{"reserved_units"}).AddRow(100))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143), "resource": models.GitHubRateLimitResourceCore}).
		WillReturnRows(pgxmock.NewRows([]string{"active_reserved"}).AddRow(100))
	mock.ExpectQuery("SELECT resource, limit_count, remaining_count").
		WithArgs(pgx.NamedArgs{"installation_id": int64(143)}).
		WillReturnRows(pgxmock.NewRows([]string{"resource", "limit_count", "remaining_count", "reset_at", "blocked_until", "observed_at", "bootstrap_reserved_at"}).
			AddRow(models.GitHubRateLimitResourceGraphQL, nil, nil, nil, blocked, now, nil))
	mock.ExpectCommit()

	decision, err := NewGitHubRateLimitStore(mock).ReserveCodeReview(context.Background(), orgID, 143, metadataID, now)

	require.NoError(t, err, "a shared secondary block should produce a durable admission decision")
	require.Equal(t, models.GitHubRateLimitDecision{
		ExistingReservation: true, ActiveReserved: 100, BlockedUntil: blocked,
		RetryAfter: 90 * time.Second, MetadataID: metadataID,
	}, decision, "a retry must not bypass a GraphQL secondary block through its existing reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "existing admission should read installation-wide blocks before committing")
}
