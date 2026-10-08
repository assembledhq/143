package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewPublicationLockBoundsServerTransaction(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL publication timeout proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(pool.Close)
	err = NewCodeReviewStore(pool).RunWithGitHubPublicationLock(ctx, uuid.New(), uuid.New(), func(lockCtx context.Context, tx DBTX) error {
		for _, tt := range []struct{ setting, expected string }{
			{setting: "lock_timeout", expected: "15s"},
			{setting: "statement_timeout", expected: "2min"},
			{setting: "idle_in_transaction_session_timeout", expected: "6min"},
		} {
			var actual string
			if err := tx.QueryRow(lockCtx, `SELECT current_setting($1)`, tt.setting).Scan(&actual); err != nil {
				return err
			}
			require.Equal(t, tt.expected, actual, "publication transaction should enforce the expected server-side timeout")
		}
		deadline, ok := lockCtx.Deadline()
		require.True(t, ok, "publication work should have a deadline after lock acquisition")
		require.LessOrEqual(t, time.Until(deadline), codeReviewPublicationLockTimeout, "publication work should fit within its deadline")
		return nil
	})
	require.NoError(t, err, "bounded publication transaction should commit")
}

func TestCodeReviewPublicationKeepsPoolHeadroomForLeaseRenewals(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL publication pool proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "connect to disposable database")
	defer admin.Close()
	schema := "publication_pool_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create isolated publication schema")
	defer func() {
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err, "remove publication schema")
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse local pool configuration")
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create production-sized pool")
	defer pool.Close()
	defer cancel() // Unblock callbacks and renewals before closing their pool.
	_, err = pool.Exec(ctx, `CREATE TABLE sessions(id uuid, org_id uuid, status text);
		CREATE TABLE jobs(id uuid PRIMARY KEY, org_id uuid, lock_token uuid, status text, job_type text, payload jsonb, lease_expires_at timestamptz, updated_at timestamptz);
		CREATE TABLE publication_markers(org_id uuid, job_id uuid PRIMARY KEY)`)
	require.NoError(t, err, "create dependencies of production publication and lease methods")
	orgID := uuid.New()
	type jobIdentity struct{ id, token, pr uuid.UUID }
	jobs := []jobIdentity{{uuid.New(), uuid.New(), uuid.New()}, {uuid.New(), uuid.New(), uuid.New()}}
	for _, job := range jobs {
		_, err := pool.Exec(ctx, `INSERT INTO jobs VALUES($1,$2,$3,'running','run_code_review','{}',now()+interval '1 minute',now())`, job.id, orgID, job.token)
		require.NoError(t, err, "seed a running review job")
	}
	store := NewCodeReviewStore(pool)
	ready := make(chan struct{}, 2)
	startReads := make(chan struct{})
	release := make(chan struct{}, 2)
	defer close(release)
	probes := make(chan error, 2)
	publicationDone := make(chan error, 2)
	renewDone := make(chan error, 2)
	await := func(results <-chan error) error {
		select {
		case err := <-results:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for i, job := range jobs {
		go func() {
			publicationDone <- store.RunWithGitHubPublicationLock(ctx, orgID, job.pr, func(lockCtx context.Context, tx DBTX) error {
				if err := NewCodeReviewStore(tx).LockAssessmentPublicationJob(lockCtx, orgID, job.id, job.token); err != nil {
					return err
				}
				ready <- struct{}{}
				select {
				case <-startReads:
				case <-ctx.Done():
					return ctx.Err()
				}
				// Freshness capture and the pre-send marker need the pool even
				// while the publisher holds its transaction and job-row fence.
				probe := func() error {
					probeCtx, cancelProbe := context.WithTimeout(lockCtx, time.Second)
					defer cancelProbe()
					var one int
					if err := pool.QueryRow(probeCtx, "SELECT 1").Scan(&one); err != nil {
						return err
					}
					if _, err := pool.Exec(probeCtx, "INSERT INTO publication_markers VALUES($1,$2)", orgID, job.id); err != nil {
						return err
					}
					var marked bool
					if err := tx.QueryRow(probeCtx, "SELECT EXISTS(SELECT 1 FROM publication_markers WHERE org_id=$1 AND job_id=$2)", orgID, job.id).Scan(&marked); err != nil {
						return err
					}
					if one != 1 || !marked {
						return fmt.Errorf("freshness read or independently committed marker missing")
					}
					return nil
				}()
				probes <- probe
				if probe != nil {
					return probe
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		if i == 0 {
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("first publisher did not acquire its job-row fence")
			}
		}
	}
	for _, job := range jobs {
		go func() {
			_, ok, err := NewJobStore(pool).RenewLease(ctx, job.id, job.token, time.Minute)
			if err == nil && !ok {
				err = fmt.Errorf("lease ownership unexpectedly lost")
			}
			renewDone <- err
		}()
	}
	require.Eventually(t, func() bool {
		var blocked int
		err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, schema).Scan(&blocked)
		return err == nil && blocked >= 1
	}, time.Second, time.Millisecond, "the active publisher's lease renewal should wait on its job-row fence")
	close(startReads)
	require.NoError(t, await(probes), "freshness reads and an independent marker commit must progress while publication and lease renewal hold connections")
	require.NoError(t, await(renewDone), "the queued publisher's lease should renew while it waits outside the pool")
	var marked int
	require.NoError(t, admin.QueryRow(ctx, "SELECT count(*) FROM "+schema+".publication_markers WHERE org_id=$1", orgID).Scan(&marked), "read committed marker before releasing publication")
	require.Equal(t, 1, marked, "only the admitted publisher should have committed its marker")
	release <- struct{}{}
	release <- struct{}{}
	for range jobs {
		require.NoError(t, await(publicationDone), "both publications should finish without a pool acquisition timeout")
	}
	require.NoError(t, await(probes), "the queued publication should retain headroom after admission")
	require.NoError(t, await(renewDone), "the fenced lease renewal should resume after publication commits")
	require.Equal(t, int64(0), pool.Stat().CanceledAcquireCount(), "publication and lease renewals must not exhaust pool acquisitions")
}
