package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func exportProof(records int64) models.CodeReviewExportArchiveProof {
	return models.CodeReviewExportArchiveProof{ManifestSHA256: strings.Repeat("a", 64), ArchivedRecords: &records, FinalPosition: &records, Verified: true}
}
func exportSourceSnapshot(t *testing.T, f exportFixture) string {
	t.Helper()
	var raw string
	err := f.conn.QueryRow(f.ctx, `SELECT jsonb_build_array(
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM code_review_revision_assessments t WHERE org_id=$1),
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM code_review_session_metadata t WHERE org_id=$1),
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM code_review_policies t WHERE org_id=$1),
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM pull_requests t WHERE org_id=$1),
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM code_review_agent_results t WHERE org_id=$1),
      (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM webhook_deliveries t WHERE org_id=$1))::text`, f.org).Scan(&raw)
	require.NoError(t, err, "capture all source data independently of export cache")
	return raw
}
func exportCacheCounts(t *testing.T, f exportFixture, id uuid.UUID) []int64 {
	t.Helper()
	var headers, rows, slots int64
	err := f.conn.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM code_review_exports WHERE org_id=$1 AND id=$2),
    (SELECT count(*) FROM code_review_export_rows WHERE org_id=$1 AND export_id=$2),
    (SELECT count(*) FROM code_review_exports WHERE org_id=$1 AND id=$2 AND slot IS NOT NULL)`, f.org, id).Scan(&headers, &rows, &slots)
	require.NoError(t, err, "inspect exact acquisition metadata, payload and slot")
	return []int64{headers, rows, slots}
}

func TestCodeReviewExportPostgresReleaseRetryAndPreservation(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.assessment(t, uuid.New(), "2026-08-01", "2026-08-02", "one", "old-head", 101, 1, "full")
	f.legacy(t, uuid.New(), "2026-08-03", "2026-08-04", "two", 102, nil)
	before := exportSourceSnapshot(t, f)
	store := NewCodeReviewExportStore(f.conn)
	filters := f.filters(models.ReviewExportCreatedOrCompleted)
	a, err := store.Acquire(f.ctx, f.org, filters)
	require.NoError(t, err, "acquire original synthetic snapshot")
	retried, err := store.Acquire(f.ctx, f.org, filters)
	require.NoError(t, err, "retry lost initial response")
	require.Equal(t, a, retried, "identical active filters preserve exact acquisition identity")
	// Occupy the remaining slots and retain a live payload for another filter.
	b, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "second independent snapshot")
	_, err = store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCompleted))
	require.NoError(t, err, "third independent snapshot")
	_, err = store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportPublished))
	require.ErrorIs(t, err, ErrReviewExportSlots, "live acquisitions are never implicitly evicted")
	_, err = store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
	require.ErrorIs(t, err, ErrReviewExportArchive, "partial archive cannot attest complete acquisition")
	require.Equal(t, []int64{1, 2, 1}, exportCacheCounts(t, f, a.ID), "failed release changes nothing")
	released, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(2))
	require.NoError(t, err, "explicit release accepts verified complete archive attestation")
	require.Equal(t, a, released.Acquisition, "identity schema filters boundary and completeness survive release")
	require.Equal(t, models.ReviewExportReleased, released.State, "receipt reports released state")
	require.Equal(t, int64(2), released.ReleaseReceipt.PayloadRowsDeleted, "only exact cached acquisition payloads are deleted")
	require.Equal(t, []int64{1, 0, 0}, exportCacheCounts(t, f, a.ID), "durable header survives payload and slot release")
	require.Equal(t, []int64{1, 2, 1}, exportCacheCounts(t, f, b.ID), "other live snapshot payloads and slot survive")
	status, err := store.Status(f.ctx, f.org, f.repo, a.ID)
	require.NoError(t, err, "durable receipt can be retrieved without reading evidence")
	require.Equal(t, released, status, "status returns the exact persisted receipt")
	retry, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(2))
	require.NoError(t, err, "lost release response can be retried")
	require.Equal(t, released, retry, "idempotent retry returns original timestamp and receipt unchanged")
	conflict := exportProof(2)
	conflict.ManifestSHA256 = strings.Repeat("b", 64)
	_, err = store.Release(f.ctx, f.org, f.repo, a.ID, conflict)
	require.ErrorIs(t, err, ErrReviewExportReleaseConflict, "different archive cannot rewrite a durable release receipt")
	_, _, err = store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID, Position: 2}, 1)
	require.ErrorIs(t, err, ErrReviewExportReleased, "even a former EOF cursor clearly fails after release")
	fresh, err := store.Acquire(f.ctx, f.org, filters)
	require.NoError(t, err, "released slot permits sequential acquisition with the same filters")
	require.NotEqual(t, a.ID, fresh.ID, "new acquisition is always a separate snapshot identity")
	require.Equal(t, before, exportSourceSnapshot(t, f), "release retry and reacquisition never modify any source data")
}

func TestCodeReviewExportPostgresReleaseTenantAndRepositoryScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                    string
		foreignOrg, foreignRepo bool
	}{{"other tenant", true, false}, {"other repository", false, true}, {"missing organization", false, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			f.assessment(t, uuid.New(), "2026-08-01", "2026-08-01", "one", "head", 101, 1, "full")
			store := NewCodeReviewExportStore(f.conn)
			a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
			require.NoError(t, err, "create tenant scoped cache")
			org, repo := f.org, f.repo
			if tt.foreignOrg {
				org = uuid.New()
				f.exec(t, `INSERT INTO organizations VALUES($1)`, org)
			} else if !tt.foreignRepo {
				org = uuid.Nil
			}
			if tt.foreignRepo {
				repo = uuid.New()
				f.exec(t, `INSERT INTO repositories(id,org_id) VALUES($1,$2)`, repo, org)
			}
			_, err = store.Release(f.ctx, org, repo, a.ID, exportProof(1))
			require.ErrorIs(t, err, ErrReviewExportExpired, "unauthorized release reveals no receipt or metadata")
			_, err = store.Status(f.ctx, org, repo, a.ID)
			require.ErrorIs(t, err, ErrReviewExportExpired, "unauthorized metadata lookup reveals no acquisition")
			require.Equal(t, []int64{1, 1, 1}, exportCacheCounts(t, f, a.ID), "unauthorized operations cannot free payloads or slots")
		})
	}
}

func TestCodeReviewExportPostgresExpiryKeepsIdentityAndReleaseReceipt(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.legacy(t, uuid.New(), "2026-08-01", "2026-08-02", "old", 101, nil)
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "acquire before synthetic expiry")
	f.exec(t, `UPDATE code_review_exports SET expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, a.ID)
	expired, err := store.Status(f.ctx, f.org, f.repo, a.ID)
	require.NoError(t, err, "expired metadata stays addressable")
	require.Equal(t, models.ReviewExportExpired, expired.State, "expiry is distinct from explicit release")
	_, _, err = store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 1)
	require.ErrorIs(t, err, ErrReviewExportExpired, "expired cursor cannot return an apparent empty success")
	_, err = store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCompleted))
	require.NoError(t, err, "new acquisition lazily reclaims only expired payloads and slots")
	require.Equal(t, []int64{1, 0, 0}, exportCacheCounts(t, f, a.ID), "expiry cleanup retains immutable metadata")
	released, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
	require.NoError(t, err, "verified archive can be receipted even after expiry cleanup")
	require.Equal(t, expired.Acquisition, released.Acquisition, "release retains original expired acquisition metadata")
	require.Equal(t, int64(0), released.ReleaseReceipt.PayloadRowsDeleted, "receipt distinguishes already reclaimed cache payloads")
	retry, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
	require.NoError(t, err, "release remains idempotent after expiry")
	require.Equal(t, released, retry, "receipt cannot be rewritten by expiry or retries")
}

func (f exportFixture) connection(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(f.ctx, f.url)
	require.NoError(t, err, "open independent concurrent connection")
	_, err = conn.Exec(f.ctx, `SET search_path TO `+f.schema+`,public`)
	require.NoError(t, err, "isolate concurrent connection")
	t.Cleanup(func() { require.NoError(t, conn.Close(f.ctx), "close independent connection") })
	return conn
}

type exportPageBarrierDB struct {
	TxStarter
	ready, resume chan struct{}
}
type exportPageBarrierTx struct {
	pgx.Tx
	ready, resume chan struct{}
}

func (b exportPageBarrierDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.TxStarter.Begin(ctx)
	return exportPageBarrierTx{tx, b.ready, b.resume}, err
}
func (b exportPageBarrierTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "SELECT r.payload") {
		close(b.ready)
		select {
		case <-b.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.Tx.Query(ctx, sql, args...)
}
func TestCodeReviewExportPostgresConcurrentPageAndRelease(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.assessment(t, uuid.New(), "2026-08-01", "2026-08-01", "one", "head", 101, 1, "full")
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "acquire page to race release")
	pageConn, releaseConn, retryConn, observer := f.connection(t), f.connection(t), f.connection(t), f.connection(t)
	ready, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	pageStore := NewCodeReviewExportStore(exportPageBarrierDB{pageConn, ready, resume})
	type pageResult struct {
		rows []models.CodeReviewExportSource
		err  error
	}
	pageCh := make(chan pageResult, 1)
	go func() {
		_, rows, err := pageStore.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 1)
		pageCh <- pageResult{rows, err}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("page did not hold header lock")
	}
	type releaseResult struct {
		status models.CodeReviewExportStatus
		err    error
	}
	releaseCh := make(chan releaseResult, 1)
	go func() {
		status, err := NewCodeReviewExportStore(releaseConn).Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
		releaseCh <- releaseResult{status, err}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := observer.QueryRow(f.ctx, `SELECT COALESCE((SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=$1),false)`, releaseConn.PgConn().PID()).Scan(&waiting)
		return err == nil && waiting
	}, 10*time.Second, 10*time.Millisecond, "release waits for in-flight page rather than deleting its rows")

	retryCh := make(chan releaseResult, 1)
	go func() {
		status, err := NewCodeReviewExportStore(retryConn).Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
		retryCh <- releaseResult{status, err}
	}()
	close(resume)
	page := <-pageCh
	require.NoError(t, page.err, "in-flight page completes losslessly")
	require.Equal(t, int64(1), int64(len(page.rows)), "concurrent release never returns a truncated page")
	released := <-releaseCh
	require.NoError(t, released.err, "release proceeds after page commits")
	require.Equal(t, models.ReviewExportReleased, released.status.State, "serialized release succeeds")
	retried := <-retryCh
	require.NoError(t, retried.err, "concurrent identical release retry succeeds")
	require.Equal(t, released.status, retried.status, "concurrent releases share one durable receipt and deletion count")
	_, _, err = store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 1)
	require.ErrorIs(t, err, ErrReviewExportReleased, "subsequent page observes explicit release")
}

func TestCodeReviewExportPostgresAdjacentWindowsOverlapAndUnknownPublished(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	id := uuid.New()
	f.assessment(t, id, "2026-08-01", "2026-09-01", "one", "superseded", 101, 1, "full")
	store := NewCodeReviewExportStore(f.conn)
	tests := []struct {
		name        string
		basis       models.ReviewExportTimeBasis
		from, until string
	}{
		{"created in earlier window", models.ReviewExportCreatedOrCompleted, "2026-07-15", "2026-08-16"},
		{"completed in adjacent window", models.ReviewExportCreatedOrCompleted, "2026-08-16", "2026-10-19"},
		{"broad publication window retains unresolved", models.ReviewExportPublished, "2026-07-15", "2026-10-19"},
		{"publication lower bound cannot drop unresolved", models.ReviewExportPublished, "2026-10-01", "2026-10-19"},
	}
	for _, tt := range tests {
		filters := f.filters(tt.basis)
		var err error
		filters.From, err = time.Parse("2006-01-02", tt.from)
		require.NoError(t, err, "parse inclusive synthetic boundary")
		filters.Until, err = time.Parse("2006-01-02", tt.until)
		require.NoError(t, err, "parse exclusive synthetic boundary")
		a, err := store.Acquire(f.ctx, f.org, filters)
		require.NoError(t, err, "acquire separate synthetic window")
		_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 100)
		require.NoError(t, err, "page complete window")
		actual := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			actual = append(actual, row.ID)
		}
		require.Equal(t, []uuid.UUID{id}, actual, tt.name)
		_, err = store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
		require.NoError(t, err, "release each verified synthetic acquisition before the next independent snapshot")
	}
}

func TestCodeReviewExportPostgresSingleOversizedRecordFailsAtomically(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.legacy(t, uuid.New(), "2026-08-01", "2026-08-01", "big", 101, nil)
	f.exec(t, `UPDATE code_review_session_metadata SET risk_reason_details=jsonb_build_object('synthetic',repeat('x',67108864)) WHERE org_id=$1`, f.org)
	_, err := NewCodeReviewExportStore(f.conn).Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.ErrorIs(t, err, ErrReviewExportBytes, "one oversized source fails without truncation or silent omission")
	var headers int
	err = f.conn.QueryRow(f.ctx, `SELECT count(*) FROM code_review_exports WHERE org_id=$1`, f.org).Scan(&headers)
	require.NoError(t, err, "inspect atomic byte-limit rollback")
	require.Equal(t, 0, headers, "failed byte-bound acquisition consumes no slot or header")
	var size int
	err = f.conn.QueryRow(f.ctx, `SELECT length(risk_reason_details->>'synthetic') FROM code_review_session_metadata WHERE org_id=$1`, f.org).Scan(&size)
	require.NoError(t, err, "source remains readable after refused acquisition")
	require.Equal(t, 67108864, size, "oversized source is preserved in full")
}

// JSON equality is intentionally exact for receipts, including pointer-valued
// zero counts; JSONB storage must not change the attested archive proof.
func TestCodeReviewExportPostgresEmptyArchiveRelease(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "empty archive acquisition")
	status, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(0))
	require.NoError(t, err, "verified empty acquisition can release")
	raw, err := json.Marshal(status.ReleaseReceipt.Archive)
	require.NoError(t, err, "encode empty attestation")
	require.JSONEq(t, `{"manifest_sha256":"`+strings.Repeat("a", 64)+`","archived_records":0,"final_position":0,"verified":true}`, string(raw), "zero counts are retained explicitly")
}

func TestCodeReviewExportPostgresFailedReleaseRollsBack(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.legacy(t, uuid.New(), "2026-08-01", "2026-08-01", "one", 101, nil)
	before := exportSourceSnapshot(t, f)
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "create synthetic archive before failure injection")
	f.exec(t, `CREATE FUNCTION refuse_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic release failure'; END $$;
    CREATE TRIGGER refuse_release BEFORE UPDATE ON code_review_exports FOR EACH ROW EXECUTE FUNCTION refuse_release()`)
	_, err = store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
	require.ErrorContains(t, err, "synthetic release failure", "a persistence failure aborts the whole release")
	require.Equal(t, []int64{1, 1, 1}, exportCacheCounts(t, f, a.ID), "rollback restores payloads and slot after delete but before receipt persistence")
	require.Equal(t, before, exportSourceSnapshot(t, f), "release failure cannot change source evidence")
	f.exec(t, `DROP TRIGGER refuse_release ON code_review_exports`)
	released, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(1))
	require.NoError(t, err, "operator can retry after transient storage failure")
	require.Equal(t, models.ReviewExportReleased, released.State, "only committed retry produces a receipt")
}

func TestCodeReviewExportPostgresReleaseMigrationRollbackGuard(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000321_code_review_export_release.down.sql"))
	require.NoError(t, err, "read receipt-preserving rollback migration")
	_, err = f.conn.Exec(f.ctx, string(down))
	require.NoError(t, err, "unused release schema can roll back")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000321_code_review_export_release.up.sql"))
	require.NoError(t, err, "read lifecycle schema migration")
	f.exec(t, string(up))
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "create durable acquisition")
	released, err := store.Release(f.ctx, f.org, f.repo, a.ID, exportProof(0))
	require.NoError(t, err, "retain explicit receipt")
	_, err = f.conn.Exec(f.ctx, string(down))
	require.ErrorContains(t, err, "metadata is durable", "rollback cannot silently destroy retained release records")
	status, err := store.Status(f.ctx, f.org, f.repo, a.ID)
	require.NoError(t, err, "refused migration leaves receipt retrievable")
	require.Equal(t, released, status, "schema rollback refusal preserves all metadata and the receipt")
}
