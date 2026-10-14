package github

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newWebhookRecoveryPostgres(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for webhook redelivery and urgent successor proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL server")
	name := "webhook_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE DATABASE `+name)
	require.NoError(t, err, "create isolated webhook database")
	u, err := url.Parse(dsn)
	require.NoError(t, err, "parse database URL")
	u.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve actual migrations")
	m, err := migrate.New("file://"+source, u.String())
	require.NoError(t, err, "initialize real schema migration")
	require.NoError(t, m.Up(), "migrate full webhook schema")
	se, de := m.Close()
	require.NoError(t, se, "close migration source")
	require.NoError(t, de, "close migration connection")
	pool, err := pgxpool.New(ctx, u.String())
	require.NoError(t, err, "connect migrated webhook pool")
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err, "drop only this test database")
		require.NoError(t, admin.Close(ctx), "close fixture administrator")
	})
	org, pr := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Webhook recovery fixture')`, org)
	require.NoError(t, err, "seed synthetic organization")
	_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,status) VALUES($1,$2,17,'https://github.com/acme/api/pull/17','acme/api','Webhook recovery','head-one','open')`, pr, org)
	require.NoError(t, err, "seed synthetic current revision")
	return pool, org, pr
}

func TestCheckWebhookEnqueueFailureRedeliveryPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		status    bool
		failedJob string
	}{
		{"check_run rebuild failure", false, prHealthRebuildJobType},
		{"check_run authoritative enqueue failure", false, prHealthSyncJobType},
		{"status rebuild failure", true, prHealthRebuildJobType},
		{"status authoritative enqueue failure", true, prHealthSyncJobType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, pr := newWebhookRecoveryPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `CREATE TABLE enqueue_fault(enabled boolean,job_type text); CREATE FUNCTION reject_fixture_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM enqueue_fault WHERE enabled AND job_type=NEW.job_type) THEN RAISE EXCEPTION 'synthetic enqueue failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fixture_enqueue_fault BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION reject_fixture_enqueue();`)
			require.NoError(t, err, "create deterministic queue failure after projection commit")
			_, err = pool.Exec(ctx, `INSERT INTO enqueue_fault VALUES(true,$1)`, tt.failedJob)
			require.NoError(t, err, "arm exact enqueue failure stage")
			service := &PRService{pullRequests: db.NewPullRequestStore(pool), jobs: db.NewJobStore(pool), logger: zerolog.Nop()}
			now := time.Now().UTC().Truncate(time.Microsecond)
			check := CheckRunEvent{Action: "created", OwnerOrgID: &org, DeliveryID: "synthetic-check-delivery"}
			check.Repository.FullName = "acme/api"
			check.CheckRun.ID = 101
			check.CheckRun.Name = "CI"
			check.CheckRun.HeadSHA = "head-one"
			check.CheckRun.Status = "in_progress"
			check.CheckRun.UpdatedAt = &now
			check.CheckRun.PullRequests = append(check.CheckRun.PullRequests, struct {
				Number int `json:"number"`
				Base   struct {
					Ref string `json:"ref"`
				} `json:"base"`
			}{Number: 17})
			status := StatusEvent{ID: 202, State: "success", SHA: "head-one", Context: "ci/test", UpdatedAt: &now, OwnerOrgID: &org, DeliveryID: "synthetic-status-delivery"}
			status.Repository.FullName = "acme/api"
			handle := func() error {
				if tt.status {
					return service.HandleStatusEvent(ctx, status)
				}
				return service.HandleCheckRunEvent(ctx, check)
			}
			require.ErrorContains(t, handle(), "synthetic enqueue failure", "webhook must report durable follow-up enqueue failure")
			var version int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&version), "projection must survive the later enqueue failure")
			_, err = pool.Exec(ctx, `UPDATE enqueue_fault SET enabled=false`)
			require.NoError(t, err, "recover queue independently of projection history")
			require.NoError(t, handle(), "exact provider redelivery must repair missing follow-up work")
			var after int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&after), "read projection after exact redelivery")
			require.Equal(t, version, after, "redelivery must repair work without manufacturing a new check observation")
			var rebuilds, syncs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE job_type=$2),COUNT(*) FILTER(WHERE job_type=$3) FROM jobs WHERE org_id=$1 AND status='pending'`, org, prHealthRebuildJobType, prHealthSyncJobType).Scan(&rebuilds, &syncs), "count recovered projection and inventory jobs")
			require.Equal(t, []int{1, 1}, []int{rebuilds, syncs}, "failure and redelivery must converge on one job in each lane")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1`, org)
			require.NoError(t, err, "finish recovered work before testing obsolete events")
			older := now.Add(-time.Second)
			if tt.status {
				status.ID--
				status.UpdatedAt = &older
				status.State = "failure"
			} else {
				check.CheckRun.UpdatedAt = &older
				check.CheckRun.Status = "queued"
			}
			require.NoError(t, handle(), "obsolete provider state should be acknowledged without projection or follow-up")
			var pending int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND status IN ('pending','running')`, org).Scan(&pending), "read obsolete-event queue footprint")
			require.Equal(t, 0, pending, "obsolete payloads must not receive exact-redelivery acceptance")
			require.NoError(t, pool.QueryRow(ctx, `SELECT projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&after), "read immutable current projection")
			require.Equal(t, version, after, "obsolete events must leave current check state unchanged")
		})
	}
}

func TestUrgentHeadChangeRetainsSuccessorPostgres(t *testing.T) {
	t.Parallel()
	pool, org, prID := newWebhookRecoveryPostgres(t)
	ctx := context.Background()
	service := &PRService{pullRequests: db.NewPullRequestStore(pool), jobs: db.NewJobStore(pool), logger: zerolog.Nop()}
	// A publication-owned mirror follows synchronize into the urgent health lane.
	sessionID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,title,status) VALUES($1,$2,'Synthetic publication','idle')`, sessionID, org)
	require.NoError(t, err, "seed the synthetic publication session")
	_, err = pool.Exec(ctx, `UPDATE pull_requests SET session_id=$1 WHERE org_id=$2 AND id=$3`, sessionID, org, prID)
	require.NoError(t, err, "associate the synthetic publication session")
	pr := models.PullRequest{ID: prID, OrgID: org}
	require.NoError(t, service.queuePullRequestStateSync(ctx, pr, PullRequestSyncReasonInitial), "queue initial urgent read")
	var predecessor uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='running' WHERE org_id=$1 RETURNING id`, org).Scan(&predecessor), "start initial read before the push")
	event := PullRequestEvent{Action: "synchronize", Number: 17, OwnerOrgID: &org}
	event.Repository.FullName = "acme/api"
	event.PR.Head.SHA = "head-two"
	for range 32 {
		require.NoError(t, service.HandlePullRequestEvent(ctx, event), "head changes during a running urgent read must retain fresh work")
	}
	var running, pending int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='running'),COUNT(*) FILTER(WHERE status='pending') FROM jobs WHERE org_id=$1`, org).Scan(&running, &pending), "read complete urgent lane")
	require.Equal(t, []int{1, 1}, []int{running, pending}, "urgent burst must retain exactly one successor")
	var reason, after string
	var due time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload->>'sync_reason',payload->>'after_job_id',run_at FROM jobs WHERE org_id=$1 AND status='pending'`, org).Scan(&reason, &after, &due), "read durable urgent successor")
	require.Equal(t, []string{string(PullRequestSyncReasonHeadChanged), predecessor.String()}, []string{reason, after}, "successor must preserve head-change intent and exact predecessor")
	require.False(t, due.After(time.Now()), "urgent successor must not inherit the check debounce")
}
