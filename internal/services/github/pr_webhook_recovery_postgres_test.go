package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
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
	t.Cleanup(func() {
		require.NoError(t, admin.Close(ctx), "close fixture administrator")
	})
	name := "webhook_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE DATABASE `+name)
	require.NoError(t, err, "create isolated webhook database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err, "drop only this test database")
	})
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
	poolConfig, err := pgxpool.ParseConfig(u.String())
	require.NoError(t, err, "parse migrated webhook database pool configuration")
	poolConfig.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err, "connect migrated webhook pool")
	t.Cleanup(pool.Close)
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
			_, err = service.pullRequests.UpsertHealthSummary(ctx, org, pr, "head-one", "base-one", models.PullRequestHealthSummary{
				MergeState:       models.PullRequestMergeStateClean,
				ChecksConfirmed:  true,
				CheckSetComplete: boolPtr(true),
			}, nil, 0)
			require.NoError(t, err, "seed an authoritative baseline before the new CI job is reported")
			_, err = pool.Exec(ctx, `UPDATE pull_requests SET merge_when_ready_state='queued' WHERE org_id=$1 AND id=$2`, org, pr)
			require.NoError(t, err, "arm merge when ready so projection can prove it waits for verified checks")
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
			if tt.failedJob == prHealthRebuildJobType {
				require.ErrorContains(t, handle(), "synthetic enqueue failure", "webhook must report durable projection enqueue failure")
			} else {
				require.NoError(t, handle(), "a webhook reporting a new CI job should enqueue only DB projection")
				changed, err := service.RebuildPullRequestHealthFromCheckStates(ctx, org, pr)
				require.ErrorContains(t, err, "synthetic enqueue failure", "projection worker must report durable verification enqueue failure")
				require.True(t, changed, "projection must commit even when verification enqueue fails")
			}
			var version int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&version), "projection must survive the later enqueue failure")
			_, err = pool.Exec(ctx, `UPDATE enqueue_fault SET enabled=false`)
			require.NoError(t, err, "recover queue independently of projection history")
			require.NoError(t, handle(), "exact provider redelivery must repair missing follow-up work")
			var after int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&after), "read projection after exact redelivery")
			require.Equal(t, version, after, "redelivery must repair work without manufacturing a new check observation")
			dueAfter := time.Now().UTC().Add(prHealthCheckBatchDelay)
			changed, err := service.RebuildPullRequestHealthFromCheckStates(ctx, org, pr)
			require.NoError(t, err, "projection retry must queue verification even if no check updates remain")
			require.Equal(t, tt.failedJob == prHealthRebuildJobType, changed, "verification retry must leave the stored projection untouched")
			current, err := service.pullRequests.GetHealthCurrent(ctx, org, pr)
			require.NoError(t, err, "read same-head projected health")
			var summary models.PullRequestHealthSummary
			require.NoError(t, json.Unmarshal(current.SummaryJSON, &summary), "decode the stored health summary")
			require.Equal(t, boolPtr(false), summary.CheckSetComplete, "a newly projected check must invalidate completeness")
			require.False(t, summary.ChecksConfirmed, "projected checks must not imply authoritative merge readiness")
			require.Len(t, summary.Checks, 1, "the new check must be visible before authoritative verification")
			var due time.Time
			var priority int
			require.NoError(t, pool.QueryRow(ctx, `SELECT run_at,priority FROM jobs WHERE org_id=$1 AND job_type=$2 AND status='pending'`, org, prHealthSyncJobType).Scan(&due, &priority), "read the batched verification job")
			require.False(t, due.Before(dueAfter), "verify the check inventory only after a batch delay")
			require.Equal(t, 3, priority, "verification must run below projection and urgent work")
			_, err = service.RebuildPullRequestHealthFromCheckStates(ctx, org, pr)
			require.NoError(t, err, "later projection runs may reuse the delayed verification")
			var rebuilds, syncs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE job_type=$2),COUNT(*) FILTER(WHERE job_type=$3) FROM jobs WHERE org_id=$1 AND status='pending'`, org, prHealthRebuildJobType, prHealthSyncJobType).Scan(&rebuilds, &syncs), "count recovered projection and verification jobs")
			require.Equal(t, []int{1, 1}, []int{rebuilds, syncs}, "failure, redelivery, and projection retries must converge on one job in each lane")
			var merges int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND job_type=$2`, org, prMergeWhenReadyJobType).Scan(&merges), "check whether projection started a separate merge safety read")
			require.Zero(t, merges, "merge processing must wait for authoritative check coverage")
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

func TestRacedCheckProjectionSchedulesFollowUpPostgres(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		remoteStatus string
		wantJob      string
		wantComplete bool
	}{
		{name: "settled known check wakes merge processing", remoteStatus: "ci/build", wantJob: prMergeWhenReadyJobType, wantComplete: true},
		{name: "new check schedules verification before merge processing", remoteStatus: "ci/other", wantJob: prHealthSyncJobType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newWebhookRecoveryPostgres(t)
			ctx := t.Context()
			_, err := pool.Exec(ctx, `UPDATE pull_requests SET merge_when_ready_state='queued' WHERE org_id=$1 AND id=$2`, orgID, prID)
			require.NoError(t, err, "seed a pull request waiting for merge processing")

			repoMock := newMockPool(t)
			expectReconciliationRepository(repoMock, orgID, uuid.New(), "acme/api", 42, false)
			store := db.NewPullRequestStore(pool)
			service := &PRService{
				baseURL: "https://example.test", pullRequests: store, repos: db.NewRepositoryStore(repoMock), jobs: db.NewJobStore(pool),
				cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()), logger: zerolog.Nop(),
				httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					var body string
					switch r.URL.Path {
					case "/repos/acme/api/pulls/17":
						body = `{"number":17,"state":"open","mergeable":true,"mergeable_state":"clean","head":{"sha":"head-one"},"base":{"sha":"base-one","ref":"main"}}`
					case "/repos/acme/api/commits/head-one/check-runs":
						// Commit a webhook update after the full read reserved its
						// watermark, before the webhook's normal rebuild is queued.
						applied, err := store.UpsertCheckState(r.Context(), orgID, models.PullRequestCheckState{
							OrgID: orgID, PullRequestID: prID, HeadSHA: "head-one", Source: models.PullRequestCheckSourceCommitStatus,
							ExternalKey: "ci/build", Name: "ci/build", Category: models.PullRequestCheckCategoryBuild,
							Status: models.PullRequestCheckStatusPassed, Provider: "github_status", ProviderEventID: "synthetic-delivery",
							ProviderSequence: 2, ProviderUpdatedAt: time.Now().UTC(),
						})
						require.NoError(t, err, "record a check webhook concurrent with the full refresh")
						require.True(t, applied, "the concurrent webhook must advance the projection watermark")
						body = `{"total_count":0,"check_runs":[]}`
					case "/repos/acme/api/commits/head-one/status":
						body = `{"total_count":1,"statuses":[{"id":1,"state":"pending","context":"` + tt.remoteStatus + `"}]}`
					default:
						t.Fatalf("unexpected GitHub request %s", r.URL.Path)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
				})},
			}

			err = service.SyncPullRequestState(WithPullRequestSyncReason(ctx, PullRequestSyncReasonCodeReview), orgID, prID)
			require.NoError(t, err, "the full refresh should apply and reconcile the racing webhook")
			require.NoError(t, repoMock.ExpectationsWereMet(), "GitHub credentials should come from the scoped repository")
			current, err := store.GetHealthCurrent(ctx, orgID, prID)
			require.NoError(t, err, "load the post-race snapshot")
			var summary models.PullRequestHealthSummary
			require.NoError(t, json.Unmarshal(current.SummaryJSON, &summary), "decode the post-race snapshot")
			require.Equal(t, boolPtr(tt.wantComplete), summary.CheckSetComplete, "the snapshot should distinguish known check updates from new coverage")
			require.Equal(t, tt.wantComplete, summary.ChecksConfirmed, "only complete terminal coverage should wake merge processing")

			rows, err := pool.Query(ctx, `SELECT job_type FROM jobs WHERE org_id=$1 ORDER BY job_type`, orgID)
			require.NoError(t, err, "load durable follow-up jobs for the race overlay")
			jobs, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "read all follow-up jobs")
			require.Equal(t, []string{tt.wantJob}, jobs, "the overlay should queue the correct follow-up without depending on a later webhook")
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
