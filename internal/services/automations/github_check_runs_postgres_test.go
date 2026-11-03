package automations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type checkRunPostgresStore struct{ automation models.Automation }

func (f checkRunPostgresStore) ListEnabledByGitHubEvent(context.Context, uuid.UUID, uuid.UUID, models.AutomationGitHubEvent) ([]models.Automation, error) {
	return []models.Automation{f.automation}, nil
}

type checkRunPostgresHead struct{ head PullRequestHeadInfo }

func (f checkRunPostgresHead) ResolvePullRequestHead(context.Context, uuid.UUID, uuid.UUID, int) (PullRequestHeadInfo, error) {
	return f.head, nil
}

type checkRunPostgresJobs struct{ fail bool }

func (f checkRunPostgresJobs) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, _, _ string, payload any, _ int, _ *string) (uuid.UUID, error) {
	if f.fail {
		_, err := tx.Exec(ctx, "SELECT 1/0")
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, "INSERT INTO check_run_jobs(org_id,run_id) VALUES($1,$2) RETURNING id", orgID, payload.(map[string]string)["automation_run_id"]).Scan(&id)
	return id, err
}
func (checkRunPostgresJobs) Notify(context.Context, uuid.UUID) {}

func checkRunPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to verify atomic first-head queue transactions")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "connect the test database")
	schema := "check_run_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create an isolated transaction fixture schema")
	config, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse the test database URL")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "connect the isolated fixture pool")
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err, "drop only the isolated fixture schema")
		admin.Close()
	})
	_, err = pool.Exec(ctx, `CREATE TABLE automation_runs (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(), automation_id uuid, org_id uuid,
		triggered_by text, triggered_by_user_id uuid, scheduled_time timestamptz,
		trigger_id uuid, provider text, provider_event_id text, trigger_context jsonb,
		goal_snapshot text, config_snapshot jsonb, status text, capability_snapshot jsonb,
		triggered_at timestamptz DEFAULT now(), created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now()
	);
	CREATE UNIQUE INDEX idx_automation_runs_provider_event ON automation_runs(automation_id,provider,provider_event_id) WHERE provider_event_id IS NOT NULL;
	CREATE TABLE check_run_jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid, run_id uuid REFERENCES automation_runs(id));`)
	require.NoError(t, err, "create the real provider identity index and transactional queue fixture")
	return pool
}

func postgresCheckRunService(pool *pgxpool.Pool, automation models.Automation, req GitHubEventTriggerRequest, fail bool) *GitHubEventTriggerService {
	s := NewGitHubEventTriggerService(checkRunPostgresStore{automation}, db.NewAutomationRunStore(pool), checkRunPostgresJobs{fail}, pool, zerolog.Nop())
	s.SetCheckRunPullRequestResolver(checkRunPostgresHead{PullRequestHeadInfo{Number: req.PullRequestNumber, SHA: req.HeadSHA, State: "open", Labels: []string{"review-ready"}}})
	return s
}

func TestGitHubCheckRunPostgresConcurrentDeliveryAndRollback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rollback bool
	}{{name: "concurrent duplicates"}, {name: "rollback retry", rollback: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := checkRunPostgresPool(t)
			fixture, _, _, _, req := checkRunFixture(t)
			automation := fixture.automations.(*fakeGitHubAutomationStore).automations[0]
			ctx := context.Background()
			if tt.rollback {
				failed := postgresCheckRunService(pool, automation, req, true)
				require.ErrorContains(t, failed.TriggerGitHubEvent(ctx, req), "enqueue", "a queue error must roll back the durable head claim")
				var count int
				require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM automation_runs WHERE org_id=$1", req.OrgID).Scan(&count), "count rolled-back claims")
				require.Zero(t, count, "failed enqueue must leave no claimed run")
			}
			service := postgresCheckRunService(pool, automation, req, false)
			var wait sync.WaitGroup
			errors := make(chan error, 4)
			for i := range 4 {
				wait.Go(func() {
					delivery := req
					delivery.ProviderEventID = fmt.Sprintf("delivery-%d", i)
					errors <- service.TriggerGitHubEvent(ctx, delivery)
				})
			}
			wait.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err, "concurrent matching completions should queue at most one run")
			}
			var runs, jobs int
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM automation_runs WHERE org_id=$1", req.OrgID).Scan(&runs), "count durable runs")
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM check_run_jobs WHERE org_id=$1", req.OrgID).Scan(&jobs), "count queued jobs")
			require.Equal(t, 1, runs, "database uniqueness must preserve one first assessment")
			require.Equal(t, 1, jobs, "only the transaction owning that run may queue its job")
			_, err := pool.Exec(ctx, "UPDATE automation_runs SET status='completed' WHERE org_id=$1", req.OrgID)
			require.NoError(t, err, "complete the first assessment fixture")
			req.ProviderEventID = "later-owner-republish"
			require.NoError(t, service.TriggerGitHubEvent(ctx, req), "completion must not release the first-head identity")
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM check_run_jobs WHERE org_id=$1", req.OrgID).Scan(&jobs), "count jobs after completion")
			require.Equal(t, 1, jobs, "completed runs must keep their non-expiring per-head claim")
		})
	}
}
