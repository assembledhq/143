package db

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPullRequestStoreFilterOpenStaleHealthSyncIDs(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		count    int
		queryErr error
	}{
		{name: "empty selection"},
		{name: "one bounded query retains saved order", count: 3},
		{name: "maximum selection remains bounded", count: 200},
		{name: "oversized selection fails before query", count: 201},
		{name: "filter error remains visible", count: 3, queryErr: errors.New("selected PR filter unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated bounded-filter fixture")
			t.Cleanup(mock.Close)
			orgID := uuid.New()
			before := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
			ids := make([]uuid.UUID, tt.count)
			for i := range ids {
				ids[i] = uuid.New()
			}
			expected := []uuid.UUID{}
			if tt.count > 0 && tt.count <= 200 {
				query := mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM pull_requests
		WHERE org_id = @org_id
		  AND id = ANY(@pull_request_ids::uuid[])
		  AND status = 'open'
		  AND (github_state_synced_at IS NULL OR github_state_synced_at < @before)
		ORDER BY array_position(@pull_request_ids::uuid[], id)`)).WithArgs(pgx.NamedArgs{
					"org_id": orgID, "pull_request_ids": ids, "before": before,
				})
				if tt.queryErr != nil {
					query.WillReturnError(tt.queryErr)
				} else {
					expected = []uuid.UUID{ids[0], ids[len(ids)-1]}
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(expected[0]).AddRow(expected[1]))
				}
			}
			actual, err := NewPullRequestStore(mock).FilterOpenStaleHealthSyncIDs(context.Background(), orgID, ids, before)
			switch {
			case tt.count > 200:
				require.ErrorContains(t, err, "exceeds its bound", "oversized selections must fail rather than expand or silently truncate work")
				require.Nil(t, actual, "invalid selections must expose no provider candidates")
			case tt.queryErr != nil:
				require.ErrorIs(t, err, tt.queryErr, "filter errors must retain the database cause")
				require.Nil(t, actual, "failed selection filtering must expose no provider candidates")
			default:
				require.NoError(t, err, "bounded saved selections should filter successfully")
				require.Equal(t, expected, actual, "filter must return only matching saved IDs in their supplied order")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "filter must issue at most one query with tenant, saved-ID, open and strict-staleness predicates")
		})
	}
}

func TestPullRequestStoreFilterOpenStaleHealthSyncIDsPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, _, _ := newSchedulingPostgres(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `ALTER TABLE pull_requests ADD COLUMN status text NOT NULL DEFAULT 'closed', ADD COLUMN github_state_synced_at timestamptz`)
	require.NoError(t, err, "add the exact open-state and authoritative-sync fields to the isolated schema")
	before := time.Date(2026, 1, 2, 10, 0, 0, 123456000, time.UTC)
	foreignOrg := uuid.New()
	staleFirst, staleLast, neverSynced, fresh, equal, closed, foreign, missing, unselected := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		id, org uuid.UUID
		status  string
		synced  *time.Time
	}{
		{staleFirst, orgID, "open", new(before.Add(-time.Hour))},
		{staleLast, orgID, "open", new(before.Add(-time.Microsecond))},
		{neverSynced, orgID, "open", nil},
		{fresh, orgID, "open", new(before.Add(time.Microsecond))},
		{equal, orgID, "open", &before},
		{closed, orgID, "closed", new(before.Add(-time.Hour))},
		{foreign, foreignOrg, "open", nil},
		{unselected, orgID, "open", nil},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,status,github_state_synced_at) VALUES($1,$2,$3,$4)`, row.id, row.org, row.status, row.synced)
		require.NoError(t, err, "seed the freshness, lifecycle and tenant boundaries")
	}
	// This order differs from sync timestamp order and includes an identity
	// that disappeared. An unselected stale PR must never enter the result.
	ids := []uuid.UUID{fresh, staleFirst, closed, foreign, neverSynced, equal, staleLast, missing}
	actual, err := NewPullRequestStore(pool).FilterOpenStaleHealthSyncIDs(ctx, orgID, ids, before)
	require.NoError(t, err, "execute the bounded UUID-array filter in real PostgreSQL")
	require.Equal(t, []uuid.UUID{staleFirst, neverSynced, staleLast}, actual, "filter must retain supplied order, include null and strictly older timestamps, and exclude fresh, equal, closed, missing, foreign and unselected rows")
	actual, err = NewPullRequestStore(pool).FilterOpenStaleHealthSyncIDs(ctx, foreignOrg, ids, before)
	require.NoError(t, err, "the same supplied IDs should remain tenant-scoped for a second organization")
	require.Equal(t, []uuid.UUID{foreign}, actual, "the tenant predicate must exclude every saved identity owned by another organization")
}
