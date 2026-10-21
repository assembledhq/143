package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestScheduledSessionCreationFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newScheduledSessionPostgresPool(t)
	_, err := pool.Exec(context.Background(), `CREATE TABLE synthetic_scheduled_create_rejections(pull_request_id uuid PRIMARY KEY);
CREATE FUNCTION synthetic_scheduled_create_reject() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE created_session uuid;
BEGIN
 IF NEW.active_session_id IS NULL OR NOT EXISTS(SELECT 1 FROM synthetic_scheduled_create_rejections WHERE pull_request_id=NEW.pull_request_id) THEN RETURN NEW; END IF;
 created_session:=NEW.active_session_id;
 IF NOT EXISTS(SELECT 1 FROM sessions WHERE org_id=NEW.org_id AND id=created_session AND origin='code_review')
 OR NOT EXISTS(SELECT 1 FROM session_threads WHERE org_id=NEW.org_id AND session_id=created_session AND execution_mode='review' AND filesystem_mode='read_only')
 OR NOT EXISTS(SELECT 1 FROM session_changesets WHERE org_id=NEW.org_id AND session_id=created_session AND is_primary)
 OR NOT EXISTS(SELECT 1 FROM code_review_session_metadata WHERE org_id=NEW.org_id AND session_id=created_session AND pull_request_id=NEW.pull_request_id AND status='queued')
 OR NOT EXISTS(SELECT 1 FROM jobs WHERE org_id=NEW.org_id AND job_type='run_code_review' AND payload->>'session_id'=created_session::text)
 OR NOT EXISTS(SELECT 1 FROM code_review_requests WHERE org_id=NEW.org_id AND pull_request_id=NEW.pull_request_id AND session_id=created_session AND status='satisfied')
 THEN RAISE EXCEPTION 'synthetic scheduler probe did not observe all earlier admission rows'; END IF;
 RAISE EXCEPTION 'synthetic late scheduled creation failure';
END $$;
CREATE TRIGGER synthetic_scheduled_create_reject BEFORE UPDATE ON code_review_pr_state FOR EACH ROW EXECUTE FUNCTION synthetic_scheduled_create_reject();`)
	require.NoError(t, err, "install only a generated-database final admission-write failure that validates all earlier rows")
	for _, name := range []string{"confirmed commit", "late admission failure", "commit failure"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			org, integration, repository, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			for _, row := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO organizations(id,name) VALUES($1,'Synthetic scheduled creation')`, []any{org}},
				{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
				{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'test/scheduled-create','https://example.invalid/repository.git',1)`, []any{repository, org, integration}},
				{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,17,'https://example.invalid/pull/17','test/scheduled-create','Synthetic scheduled creation')`, []any{pr, org}},
			} {
				_, err := pool.Exec(ctx, row.query, row.args...)
				require.NoError(t, err, "seed only synthetic tenant, repository and PR relationships in the generated database")
			}
			metadata := db.NewCodeReviewStore(pool)
			policy := models.DefaultCodeReviewPolicyConfig()
			zero := 0
			policy.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
			_, err := metadata.SavePolicy(ctx, org, policy, nil)
			require.NoError(t, err, "enable immediate review scheduling without introducing a new admission policy")
			snapshot := &schedulingSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: 17, State: "open", Title: "Synthetic scheduled creation", HTMLURL: "https://example.invalid/pull/17", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseRef: "main"}}
			service := NewService(metadata, metadata, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
			service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
			_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repository, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, models.CodeReviewReviewNow, false, nil)
			require.NoError(t, err, "accept the actual durable review request before claiming its scheduler wake")
			jobID, token := uuid.Nil, uuid.New()
			require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, token).Scan(&jobID), "claim the actual scheduler job with its exact lease identity")
			if name == "late admission failure" {
				_, err := pool.Exec(ctx, "INSERT INTO synthetic_scheduled_create_rejections VALUES($1)", pr)
				require.NoError(t, err, "scope the final real-admission SQL failure to this exact synthetic PR")
			}
			// Pre-open the physical observer before any owner starts. It never
			// borrows the bounded setup/DDL pool from a commit/rollback hook.
			observer, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			require.True(t, err == nil, "open a dedicated observer on only the generated database without publishing its DSN")
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				require.NoError(t, observer.Close(cleanup), "close the dedicated observer independently of the operation context")
			})
			configuration := pool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			callerPool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open a one-connection scheduler-owner pool on only the generated fixture")
			t.Cleanup(callerPool.Close)
			starter := &scheduledCreateOwner{Pool: callerPool, observer: observer, orgID: org, failCommit: name == "commit failure"}
			service.scheduling.store = db.NewCodeReviewScheduleStore(starter)
			wakeCtx := jobctx.WithLockToken(jobctx.WithJobID(ctx, jobID), token)
			err = service.ReconcileSchedule(wakeCtx, models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr})
			require.NoError(t, starter.observationErr, "all transaction observations must use the pre-opened physical connection without deadlocking the setup pool")
			require.Zero(t, starter.nestedBegins, "the actual ReconcileSchedule/startReview path must never begin a nested creation savepoint")
			after, readErr := scheduledCreateGraph(ctx, observer, org)
			require.NoError(t, readErr, "reload every created row and scheduler/queue column from the independent observer")
			if name != "confirmed commit" {
				if name == "late admission failure" {
					require.ErrorContains(t, err, "synthetic late scheduled creation failure", "the real final state write must fail only after the trigger proves session, primary thread, changeset, metadata, job and request writes")
				} else {
					require.ErrorIs(t, err, errScheduledCreateCommit, "the real outer owner must return its injected commit error without replay")
					require.NotEmpty(t, starter.staged, "commit failure must follow a real staged admission, not an early validation rejection")
				}
				require.Equal(t, starter.beforeOwner, after, "the failed actual admission owner must preserve its exact original graph after all staged creation/queue/schedule rows roll back")
				return
			}
			require.NoError(t, err, "the real scheduler must admit through existing CreateInTx with its pool's only connection occupied")
			require.NotEmpty(t, starter.staged, "the success proof must observe the complete real admission before commit")
			require.Equal(t, starter.staged, after, "outer commit must expose exactly the complete graph staged by the actual callback")
			var graph struct {
				Sessions   []models.Session                   `json:"sessions"`
				Threads    []models.SessionThread             `json:"threads"`
				Changesets []models.SessionChangeset          `json:"changesets"`
				Metadata   []models.CodeReviewSessionMetadata `json:"metadata"`
				Schedule   []models.CodeReviewPRState         `json:"schedule"`
				Jobs       []models.Job                       `json:"jobs"`
			}
			require.NoError(t, json.Unmarshal(after, &graph), "decode the real committed admission shape for exact relationship checks")
			require.Len(t, graph.Sessions, 1, "the real admitted PR must create exactly one session")
			require.Len(t, graph.Threads, 1, "the real create path must produce exactly one primary thread")
			require.Len(t, graph.Changesets, 1, "the unchanged schema must create exactly one primary changeset")
			require.Len(t, graph.Metadata, 1, "the session must have exactly one existing review metadata row")
			require.Equal(t, graph.Sessions[0].ID, graph.Threads[0].SessionID, "the primary thread must belong to the exact admitted session")
			require.Equal(t, models.ThreadExecutionModeReview, graph.Threads[0].ExecutionMode, "scheduled creation retains read-only review execution mode")
			require.Equal(t, models.ThreadFilesystemModeReadOnly, graph.Threads[0].FilesystemMode, "scheduled creation must not inherit writable ordinary-thread defaults")
			require.Equal(t, graph.Sessions[0].ID, graph.Changesets[0].SessionID, "the primary changeset must remain attached to the admitted session")
			require.True(t, graph.Changesets[0].IsPrimary, "the unchanged session trigger must retain primary changeset identity")
			require.Equal(t, graph.Sessions[0].ID, graph.Metadata[0].SessionID, "metadata must bind the same newly admitted session")
			require.Len(t, graph.Schedule, 1, "the existing scheduler keeps one PR state row")
			require.Equal(t, &graph.Sessions[0].ID, graph.Schedule[0].ActiveSessionID, "the final scheduler write must bind exactly that admitted session")
			require.Equal(t, models.CodeReviewScheduleRunning, graph.Schedule[0].State, "the unchanged scheduler state must become running")
		})
	}
}

var errScheduledCreateCommit = errors.New("synthetic scheduled create commit failure")

type scheduledCreateOwner struct {
	*pgxpool.Pool
	observer            *pgx.Conn
	orgID               uuid.UUID
	failCommit          bool
	nestedBegins        int
	beforeOwner, staged json.RawMessage
	observationErr      error
}

func (s *scheduledCreateOwner) Begin(ctx context.Context) (pgx.Tx, error) {
	before, err := scheduledCreateGraph(ctx, s.observer, s.orgID)
	if err != nil {
		s.observationErr = err
		return nil, err
	}
	s.beforeOwner = before
	owner, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var observerPID, ownerPID uint32
	observerPID, ownerPID = s.observer.PgConn().PID(), owner.Conn().PgConn().PID()
	if observerPID == ownerPID {
		err := errors.New("scheduled creation observer must be a separate physical connection")
		s.observationErr = err
		if rollbackErr := owner.Rollback(ctx); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	return &scheduledCreateTx{Tx: owner, owner: s}, nil
}

type scheduledCreateTx struct {
	pgx.Tx
	owner *scheduledCreateOwner
}

func (tx *scheduledCreateTx) Begin(context.Context) (pgx.Tx, error) {
	tx.owner.nestedBegins++
	return nil, errors.New("nested scheduler creation transaction is forbidden")
}

func (tx *scheduledCreateTx) Commit(ctx context.Context) error {
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE org_id=$1", tx.owner.orgID).Scan(&count); err != nil {
		tx.owner.observationErr = err
		return err
	}
	if count > 0 {
		staged, err := scheduledCreateGraph(ctx, tx, tx.owner.orgID)
		if err != nil {
			tx.owner.observationErr = err
			return err
		}
		independent, err := scheduledCreateGraph(ctx, tx.owner.observer, tx.owner.orgID)
		if err != nil {
			tx.owner.observationErr = err
			return err
		}
		if string(independent) != string(tx.owner.beforeOwner) {
			err := errors.New("scheduled admission became visible before its outer commit")
			tx.owner.observationErr = err
			return err
		}
		tx.owner.staged = staged
		if tx.owner.failCommit {
			return errScheduledCreateCommit
		}
	}
	return tx.Tx.Commit(ctx)
}

func scheduledCreateGraph(ctx context.Context, conn db.DBTX, org uuid.UUID) (json.RawMessage, error) {
	var graph json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM sessions s WHERE org_id=$1),
 'threads',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM session_threads s WHERE org_id=$1),
 'changesets',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM session_changesets s WHERE org_id=$1),
 'context',(SELECT jsonb_agg(to_jsonb(s) ORDER BY session_id) FROM session_execution_context s WHERE org_id=$1),
 'execution_metadata',(SELECT jsonb_agg(to_jsonb(s) ORDER BY session_id) FROM session_execution_metadata s WHERE org_id=$1),
 'automation_links',(SELECT jsonb_agg(to_jsonb(s) ORDER BY session_id) FROM session_automation_links s WHERE org_id=$1),
 'linear_context',(SELECT jsonb_agg(to_jsonb(s) ORDER BY session_id) FROM session_linear_context s WHERE org_id=$1),
 'publish',(SELECT jsonb_agg(to_jsonb(s) ORDER BY session_id) FROM session_publish_state s WHERE org_id=$1),
 'metadata',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_session_metadata s WHERE org_id=$1),
 'jobs',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM jobs s WHERE org_id=$1),
 'schedule',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_pr_state s WHERE org_id=$1),
 'requests',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_requests s WHERE org_id=$1))`, org).Scan(&graph)
	return graph, err
}

func newScheduledSessionPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for actual scheduled creation composition proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the supplied disposable server without printing its DSN")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, admin.Close(cleanup), "close this generated fixture's administrator connection")
	})
	var version int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version), "verify the actual database supports the complete unchanged schema")
	require.GreaterOrEqual(t, version, 150000, "the full-schema proof requires PostgreSQL 15 or newer")
	name := "scheduled_creation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this independently generated synthetic fixture database")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.Exec(cleanup, "DROP DATABASE "+identifier)
		require.NoError(t, err, "drop only this generated database after owned connections close, without forced termination")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the fixture target without publishing connection details")
	query := target.Query()
	query.Del("dbname")
	query.Del("database")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	configuration, err := pgxpool.ParseConfig(target.String())
	require.True(t, err == nil && configuration.ConnConfig.Database == name, "both test drivers must target only the generated database")
	configuration.MaxConns, configuration.MinConns = 2, 0
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve the full unchanged migration chain")
	migration, err := migrate.New("file://"+source, target.String())
	require.True(t, err == nil, "open migration drivers only for this generated fixture")
	migrationErr := migration.Up()
	sourceErr, databaseErr := migration.Close()
	require.NoError(t, migrationErr, "apply every real repository migration before the actual scheduler proof")
	require.NoError(t, sourceErr, "close the migration source before parallel test owners start")
	require.NoError(t, databaseErr, "close the migration database connection before parallel test owners start")
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	require.True(t, err == nil, "open only the generated fixture's bounded setup pool")
	t.Cleanup(pool.Close)
	return pool
}
