package db

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPullRequestStoreCountOpenForHealthSync(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                     string
		cap, effectiveCap, count int
		err                      error
	}{
		{name: "bounded population", cap: 1000, effectiveCap: 1000, count: 681},
		{name: "smaller bound", cap: 3, effectiveCap: 3, count: 3},
		{name: "invalid zero defaults", effectiveCap: 1000},
		{name: "oversized bound defaults", cap: 1001, effectiveCap: 1000},
		{name: "query failure", cap: 1000, effectiveCap: 1000, err: errors.New("population unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "population query mock should initialize")
			t.Cleanup(mock.Close)
			orgID := uuid.New()
			expected := mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM (
			SELECT 1 FROM pull_requests
			WHERE org_id = @org_id AND status = 'open'
			LIMIT @population_cap
		) AS open_pull_requests`)).WithArgs(pgx.NamedArgs{"org_id": orgID, "population_cap": tt.effectiveCap})
			if tt.err != nil {
				expected.WillReturnError(tt.err)
			} else {
				expected.WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.count))
			}
			count, err := NewPullRequestStore(mock).CountOpenForHealthSync(context.Background(), orgID, tt.cap)
			require.Equal(t, tt.count, count, "bounded open population should be returned exactly")
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err, "count failure should retain the database cause")
			} else {
				require.NoError(t, err, "bounded population query should succeed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "population SQL should filter tenant and open state before limiting")
		})
	}
}

func TestPullRequestStoreCountOpenForHealthSyncPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                  string
		population, cap, want int
	}{
		{name: "empty tenant", cap: 1000},
		{name: "exact population", population: 251, cap: 1000, want: 251},
		{name: "population beyond cap", population: 1005, cap: 1000, want: 1000},
		{name: "smaller cap", population: 10, cap: 3, want: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _, _ := newSchedulingPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `ALTER TABLE pull_requests ADD COLUMN status text NOT NULL DEFAULT 'closed';
				CREATE INDEX ON pull_requests(org_id,status)`)
			require.NoError(t, err, "fixture should support the production open-state predicate")
			_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,status)
				SELECT gen_random_uuid(),$1,'open' FROM generate_series(1,$2::integer)`, orgID, tt.population)
			require.NoError(t, err, "fixture should contain the requested open population")
			_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,status)
				SELECT gen_random_uuid(),$1,'open' FROM generate_series(1,1005)`, uuid.New())
			require.NoError(t, err, "foreign open population should not affect the tenant count")
			count, err := NewPullRequestStore(pool).CountOpenForHealthSync(ctx, orgID, tt.cap)
			require.NoError(t, err, "real PostgreSQL should execute the bounded subquery")
			require.Equal(t, tt.want, count, "count should exclude closed and foreign rows and stop at the supplied cap")
		})
	}
}
