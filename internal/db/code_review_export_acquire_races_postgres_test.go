package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type exportAcquireRaceDB struct {
	TxStarter
	stage         string
	ready, resume chan struct{}
}
type exportAcquireRaceTx struct {
	pgx.Tx
	stage         string
	ready, resume chan struct{}
}

func (b exportAcquireRaceDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.TxStarter.Begin(ctx)
	return exportAcquireRaceTx{tx, b.stage, b.ready, b.resume}, err
}
func (b exportAcquireRaceTx) wait(ctx context.Context) error {
	close(b.ready)
	select {
	case <-b.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b exportAcquireRaceTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if b.stage == "cleanup" && strings.Contains(sql, "WITH expired AS (") {
		if err := b.wait(ctx); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	return b.Tx.Exec(ctx, sql, args...)
}
func (b exportAcquireRaceTx) Commit(ctx context.Context) error {
	if b.stage == "reuse" {
		if err := b.wait(ctx); err != nil {
			return err
		}
	}
	return b.Tx.Commit(ctx)
}

func TestCodeReviewExportPostgresAcquireReleaseRaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stage      string
		expired, release bool
	}{
		{"repeatable read cleanup after concurrent release", "cleanup", true, true},
		{"live reuse after concurrent release", "reuse", false, true},
		{"live reuse after concurrent expiry", "reuse", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			source := uuid.New()
			f.assessment(t, source, "2026-08-01", "2026-08-01", "one", "head", 101, 1, "full")
			before := exportSourceSnapshot(t, f)
			plain := NewCodeReviewExportStore(f.conn)
			filters := f.filters(models.ReviewExportCreated)
			a, err := plain.Acquire(f.ctx, f.org, filters)
			require.NoError(t, err, "create acquisition before the precisely controlled race")
			if tt.expired {
				f.exec(t, `UPDATE code_review_exports SET expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, a.ID)
				filters.TimeBasis = models.ReviewExportCompleted
			}
			ready, resume := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}()
			acquirer := NewCodeReviewExportStore(exportAcquireRaceDB{f.connection(t), tt.stage, ready, resume})
			result := make(chan error, 1)
			go func() { _, err := acquirer.Acquire(f.ctx, f.org, filters); result <- err }()
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				t.Fatal("acquisition did not reach its race barrier")
			}
			if tt.release {
				_, err = plain.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
				require.NoError(t, err, "commit release after acquisition captured its MVCC snapshot")
			} else {
				f.exec(t, `UPDATE code_review_exports SET expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, a.ID)
			}
			close(resume)
			require.ErrorIs(t, <-result, ErrReviewExportBusy, "cursorless acquisition race is retryable busy rather than 500 or gone")
			next, err := plain.Acquire(f.ctx, f.org, filters)
			require.NoError(t, err, "retry acquires a distinct complete snapshot")
			require.NotEqual(t, a.ID, next.ID, "retry never revives the released or expired snapshot identity")
			_, rows, err := plain.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: next.ID}, 100)
			require.NoError(t, err, "retried acquisition remains losslessly pageable")
			actual := make([]uuid.UUID, 0, len(rows))
			for _, row := range rows {
				actual = append(actual, row.ID)
			}
			require.Equal(t, []uuid.UUID{source}, actual, "concurrent lifecycle work never drops source identity")
			require.Equal(t, before, exportSourceSnapshot(t, f), "all race and retry paths preserve every source table")
		})
	}
}
