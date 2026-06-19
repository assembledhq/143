package preview

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDependencyCacheCleaner_RunOnceDeletesExpiredLocationHints(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}))
	mock.ExpectExec("DELETE FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	cleaner := NewDependencyCacheCleaner(DependencyCacheCleanerConfig{
		Store:     db.NewPreviewStore(mock),
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		Retention: time.Hour,
	})

	require.NoError(t, cleaner.RunOnce(context.Background()), "RunOnce should clean stale dependency cache location hints")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
