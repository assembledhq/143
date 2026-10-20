package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type turnAttemptPostgresFixture struct{ orgID, sessionID, threadID, runID, jobID, token uuid.UUID }

type turnAttemptPostgresStarter struct {
	*pgxpool.Pool
	tx      *turnAttemptPostgresTx
	begins  int
	observe func(pgx.Tx)
}

func (s *turnAttemptPostgresStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &turnAttemptPostgresTx{Tx: tx, owner: s}
	return s.tx, nil
}

type turnAttemptPostgresTx struct {
	pgx.Tx
	owner                   *turnAttemptPostgresStarter
	nested, commits, aborts int
	commitFailed            bool
}

func (tx *turnAttemptPostgresTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nested++
	return nil, errors.New("attempt must not create nested transaction")
}
func (tx *turnAttemptPostgresTx) Commit(ctx context.Context) error {
	tx.commits++
	tx.owner.observe(tx.Tx)
	err := tx.Tx.Commit(ctx)
	tx.commitFailed = err != nil
	return err
}
func (tx *turnAttemptPostgresTx) Rollback(ctx context.Context) error {
	tx.aborts++
	settled := false
	defer func() {
		if !settled {
			_ = tx.Tx.Rollback(context.Background())
		}
	}()
	if !tx.commitFailed && tx.Conn().PgConn().TxStatus() == 'T' {
		tx.owner.observe(tx.Tx)
	}
	err := tx.Tx.Rollback(ctx)
	settled = true
	return err
}

func TestTurnAttemptPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newTurnAttemptPostgresPool(t)
	tests := []struct{ name, kind, failure string }{
		{name: "actual failed-result status waits for outer commit", kind: "result"},
		{name: "actual awaiting-input status and row-only snapshot wait for commit", kind: "pause"},
		{name: "actual successful diff-free turn adds no event", kind: "turn"},
		{name: "latent full-diff turn defers workspace wake", kind: "full diff"},
		{name: "latent authoritative empty-diff turn defers workspace wake", kind: "empty diff"},
		{name: "latent head-only turn retains its head without a savepoint", kind: "head only"},
		{name: "ordered result workspace and later status effects", kind: "ordered"},
		{name: "late marker rejection rolls back captured workspace", kind: "full diff", failure: "marker"},
		{name: "late callback error rolls back captured status", kind: "result", failure: "callback"},
		{name: "real late duration failure rolls back captured workspace", kind: "full diff", failure: "duration"},
		{name: "real commit failure rolls back captured status and workspace", kind: "ordered", failure: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			f := seedTurnAttemptPostgres(t, ctx, pool)
			if tt.failure == "duration" || tt.failure == "commit" {
				installTurnAttemptFailure(t, ctx, pool, f, tt.failure)
			}
			before := turnAttemptPostgresRows(t, ctx, pool, f)
			// A parallel case's fixture DDL may occupy the setup pool while
			// waiting on this owner's table locks. Own the observer connection
			// before starting the transaction so that wait cannot form a
			// pool-acquisition cycle invisible to PostgreSQL.
			observer, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			require.True(t, err == nil, "open an independent generated-fixture observer without exposing connection data")
			t.Cleanup(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, observer.Close(closeCtx), "close the dedicated attempt observer independently of the owner")
			})
			config := pool.Config()
			config.MaxConns, config.MinConns = 1, 0
			ownerPool, err := pgxpool.NewWithConfig(ctx, config)
			require.True(t, err == nil, "open the generated fixture's single physical owner connection without disclosing connection data")
			t.Cleanup(ownerPool.Close)
			starter := &turnAttemptPostgresStarter{Pool: ownerPool}
			sessions, redis := turnAttemptStreams(t, starter)
			redis.SetTime(time.Now())
			var staged atomic.Value
			starter.observe = func(tx pgx.Tx) {
				staged.Store(turnAttemptPostgresRows(t, ctx, tx, f))
				require.Empty(t, redis.Keys(), "the original enabled publisher must remain silent until actual outer settlement")
				require.NotEqual(t, tx.Conn().PgConn().PID(), observer.PgConn().PID(), "the observer must read through a different physical PostgreSQL connection")
				require.Equal(t, before, turnAttemptPostgresRows(t, ctx, observer, f), "independent observers must see exactly the original session, marker, duration, head and snapshot rows before outer commit")
			}
			commands := turnAttemptCaptureCommands(redis, func() error {
				expected, ok := staged.Load().(json.RawMessage)
				if !ok {
					return errors.New("notification preceded the actual owner commit boundary")
				}
				observed, err := readTurnAttemptPostgresRows(ctx, observer, f)
				if err != nil {
					return fmt.Errorf("read independent accepted-effect observer: %w", err)
				}
				if !bytes.Equal(expected, observed) {
					return errors.New("accepted notification did not observe the exact fully committed owner rows")
				}
				return nil
			})
			store := NewTurnStore(starter, sessions, db.NewAutomationRunStore(starter), nil, db.NewAutomationRunResultStore(starter), nil)
			failure := errors.New("synthetic late callback failure")
			var expectedStatuses []models.Session
			err = store.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, bound agent.SessionStore) error {
				marker := &models.AutomationRunResult{RunID: f.runID, OrgID: f.orgID, Attempt: 1, AttemptLockToken: f.token, ThreadID: f.threadID, TurnNumber: 1, Outcome: models.AutomationRunResultTurnCompleted, ReviewComplete: true}
				written, err := store.WriteResult(ctx, tx, f.orgID, f.jobID, marker)
				if err != nil {
					return err
				}
				if !written {
					return agent.ErrAutomationAttemptLost
				}
				// Match the real owner: marker/run/job locks first, parent
				// persistence second, and duration last. Latent diff cases test
				// the adapter without changing real automation diff policy.
				result := &models.SessionResult{}
				if tt.kind == "full diff" || tt.kind == "empty diff" || tt.kind == "ordered" || tt.kind == "head only" {
					diff, base, head := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -0,0 +1 @@\n+synthetic\n", "synthetic-base", "synthetic-head"
					at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
					result = &models.SessionResult{Diff: &diff, DiffBaseCommitSHA: &base, DiffHeadCommitSHA: &head, DiffCollectedAt: &at}
					if tt.kind == "empty diff" {
						result.Diff = new(string)
					}
					if tt.kind == "head only" {
						result.Diff, result.DiffBaseCommitSHA = nil, nil
					}
				}
				switch tt.kind {
				case "result":
					err = bound.UpdateResult(ctx, f.orgID, f.sessionID, models.SessionStatusFailed, nil)
				case "pause":
					if err = bound.UpdateSnapshotInfo(ctx, f.orgID, f.sessionID, "synthetic-agent", "synthetic-checkpoint"); err == nil {
						err = bound.UpdateStatus(ctx, f.orgID, f.sessionID, models.SessionStatusAwaitingInput)
					}
				case "ordered":
					if err = bound.UpdateResult(ctx, f.orgID, f.sessionID, models.SessionStatusFailed, result); err == nil {
						err = bound.UpdateStatus(ctx, f.orgID, f.sessionID, models.SessionStatusAwaitingInput)
					}
				default:
					if tt.kind == "turn" {
						result = nil
					}
					err = bound.UpdateTurnComplete(ctx, f.orgID, f.sessionID, 1, result, "synthetic-agent", "synthetic-checkpoint")
				}
				if err != nil {
					return err
				}
				for _, effect := range bound.(*turnAttemptSessions).effects {
					if effect.status != nil {
						expectedStatuses = append(expectedStatuses, *effect.status)
					}
				}
				require.Empty(t, redis.Keys(), "every real callback path must keep both status and workspace notifications deferred")
				if tt.kind == "ordered" {
					require.Equal(t, int64(0), expectedStatuses[0].WorkspaceRevision, "result status must preserve UPDATE RETURNING's pre-snapshot row rather than a refreshed projection")
					require.Nil(t, expectedStatuses[0].LatestDiffSnapshotID, "result status must retain the original returned snapshot identity")
					require.Equal(t, int64(1), expectedStatuses[1].WorkspaceRevision, "the later status call must retain its own independently returned post-snapshot row")
				}
				if tt.failure == "callback" {
					return failure
				}
				if tt.failure == "marker" {
					// A second explicit fence probe tests rejection after real
					// prior writes without moving the production first marker.
					marker.AttemptLockToken = uuid.New()
					written, err := store.WriteResult(ctx, tx, f.orgID, f.jobID, marker)
					if err != nil {
						return err
					}
					if !written {
						return agent.ErrAutomationAttemptLost
					}
				}
				_, err = store.RecordTurnDuration(ctx, tx, f.orgID, f.runID, f.token, 1234)
				return err
			})
			require.Equal(t, 1, starter.begins, "the actual pool-backed owner must begin exactly one physical transaction")
			require.Zero(t, starter.tx.nested, "head, empty/full diff and status paths must never create savepoints or inner transactions")
			after := turnAttemptPostgresRows(t, ctx, pool, f)
			if tt.failure != "" {
				require.Error(t, err, "late marker, callback, duration and physical commit failures must reach the real owner")
				if tt.failure == "marker" {
					require.ErrorIs(t, err, agent.ErrAutomationAttemptLost, "late marker rejection must preserve the existing attempt fence error")
				}
				if tt.failure == "callback" {
					require.ErrorIs(t, err, failure, "callback rejection must preserve its raw cause")
				}
				if tt.failure == "duration" || tt.failure == "commit" {
					var pgErr *pgconn.PgError
					require.ErrorAs(t, err, &pgErr, "late database failures must come from genuine duration SQL or physical deferred commit")
					require.Equal(t, "synthetic attempt "+tt.failure+" failure", pgErr.Message, "fixture SQL must first verify genuinely staged owner writes before the chosen failure")
				}
				require.Equal(t, before, after, "all session, publish-state, changeset, snapshot, marker and duration rows must return exactly to baseline")
				require.Empty(t, redis.Keys(), "failed or uncertain owner settlement must discard every collected status and workspace effect")
				if tt.failure != "duration" {
					require.NotEqual(t, before, staged.Load(), "the outer rollback proof must contain genuine earlier staged writes rather than no-op success")
				}
				return
			}
			require.NoError(t, err, "the real callback must commit through the unchanged pool-backed owner")
			require.Equal(t, staged.Load(), after, "exactly the staged entire row set must be durable before any accepted wake")
			requireTurnAttemptStatusEvents(t, redis, f.sessionID, expectedStatuses)
			actualCommands, hookErrors := commands()
			require.Empty(t, hookErrors, "every accepted Redis event must independently observe the exact fully committed database rows")
			prefix := "143:stream:{ses:" + f.sessionID.String() + "}:"
			var expectedCommands []string
			for _, status := range expectedStatuses {
				expectedCommands = append(expectedCommands, "XADD:"+prefix+"status")
				if status.Status == models.SessionStatusFailed {
					expectedCommands = append(expectedCommands, "EXPIREAT:"+prefix+"logs", "EXPIREAT:"+prefix+"status", "EXPIREAT:"+prefix+"events")
				}
				if tt.kind == "ordered" && status.Status == models.SessionStatusFailed {
					expectedCommands = append(expectedCommands, "XADD:"+prefix+"events")
				}
			}
			if tt.kind == "full diff" || tt.kind == "empty diff" {
				expectedCommands = append(expectedCommands, "XADD:"+prefix+"events")
			}
			require.Equal(t, expectedCommands, actualCommands, "the true owner must preserve status, terminal expiry and workspace ordering without inventing a successful diff-free turn event")
			var workspaceEvents []string
			if redis.Exists(prefix + "events") {
				entries, err := redis.Stream(prefix + "events")
				require.NoError(t, err, "read the existing workspace stream after confirmed outer commit")
				for _, entry := range entries {
					workspaceEvents = append(workspaceEvents, entry.Values[1])
				}
			}
			var expectedWorkspace []string
			if tt.kind == "full diff" || tt.kind == "empty diff" || tt.kind == "ordered" {
				at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				event := models.SessionStreamEvent{Type: models.SessionStreamEventWorkspaceGenerationChanged, SessionID: f.sessionID, OrgID: f.orgID, Data: models.SessionWorkspaceGenerationChangedEvent{SessionID: f.sessionID, OrgID: f.orgID, WorkspaceRevision: 1, WorkspaceRevisionUpdatedAt: at, Reason: "diff_snapshot"}}
				encoded, err := json.Marshal(event)
				require.NoError(t, err, "encode the exact original opaque-effect workspace event contract")
				expectedWorkspace = []string{string(encoded)}
			}
			require.Equal(t, expectedWorkspace, workspaceEvents, "post-commit dispatch must preserve every original workspace effect field exactly once")
			var status models.SessionStatus
			var duration, snapshots int
			var head string
			require.NoError(t, pool.QueryRow(ctx, `SELECT s.status,r.turn_duration_ms,c.head_sha,(SELECT count(*) FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2) FROM sessions s JOIN automation_runs r ON r.org_id=s.org_id AND r.id=$3 JOIN session_changesets c ON c.org_id=s.org_id AND c.session_id=s.id AND c.is_primary WHERE s.org_id=$1 AND s.id=$2`, f.orgID, f.sessionID, f.runID).Scan(&status, &duration, &head, &snapshots), "inspect unchanged concrete completion projections after accepted effects")
			expectedState, expectedHead, expectedSnapshots := models.SessionStatusIdle, "synthetic-prior-head", 0
			if tt.kind == "result" {
				expectedState = models.SessionStatusFailed
			}
			if tt.kind == "pause" || tt.kind == "ordered" {
				expectedState = models.SessionStatusAwaitingInput
			}
			if tt.kind == "head only" || tt.kind == "full diff" || tt.kind == "empty diff" || tt.kind == "ordered" {
				expectedHead = "synthetic-head"
			}
			if tt.kind == "full diff" || tt.kind == "empty diff" || tt.kind == "ordered" {
				expectedSnapshots = 1
			}
			require.Equal(t, expectedState, status, "deferring notifications cannot change the existing caller's end policy")
			require.Equal(t, 1234, duration, "duration must remain the final mutation in the same committed owner unit")
			require.Equal(t, expectedHead, head, "explicit persistence must retain head-only and authoritative changeset behavior")
			require.Equal(t, expectedSnapshots, snapshots, "authoritative empty diffs must retain their real snapshot without adding one for actual diff-free automation")
		})
	}
}

func seedTurnAttemptPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) turnAttemptPostgresFixture {
	t.Helper()
	f := turnAttemptPostgresFixture{orgID: uuid.New(), runID: uuid.New(), jobID: uuid.New(), token: uuid.New()}
	integrationID, repoID, automationID := uuid.New(), uuid.New(), uuid.New()
	queries := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO organizations(id,name) VALUES($1,'Synthetic turn attempt')", []any{f.orgID}},
		{"INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')", []any{integrationID, f.orgID}},
		{"INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/turn','https://example.invalid/turn.git',1)", []any{repoID, f.orgID, integrationID}},
		{"INSERT INTO automations(id,org_id,repository_id,name,goal,session_continuity) VALUES($1,$2,$3,'Synthetic','Test committed effects','per_target')", []any{automationID, f.orgID, repoID}},
		{"INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,triggered_at) VALUES($1,$2,$3,'Synthetic','running',now())", []any{f.runID, f.orgID, automationID}},
	}
	for _, query := range queries {
		_, err := pool.Exec(ctx, query.sql, query.args...)
		require.NoError(t, err, "seed only the full-schema synthetic automation attempt graph")
	}
	session := &models.Session{OrgID: f.orgID, RepositoryID: &repoID, AutomationRunID: &f.runID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, Origin: models.SessionOriginAutomation, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow}
	require.NoError(t, db.NewSessionStore(pool).Create(ctx, session), "create the real automation-linked session and its primary thread and changeset")
	f.sessionID, f.threadID = session.ID, *session.PrimaryThreadID
	_, err := pool.Exec(ctx, "UPDATE session_changesets SET head_sha='synthetic-prior-head' WHERE org_id=$1 AND session_id=$2 AND is_primary", f.orgID, f.sessionID)
	require.NoError(t, err, "seed the previous primary changeset head")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "prepare target ownership outside the attempt-under-test boundary")
	defer func() { _ = tx.Rollback(ctx) }()
	targets := db.NewAutomationTargetStore(pool)
	target, err := targets.LockOrCreate(ctx, tx, f.orgID, automationID, repoID, models.AutomationTargetKindGitHubPullRequest, "9")
	require.NoError(t, err, "create the synthetic per-target domain")
	generation, err := targets.InsertGeneration(ctx, tx, f.orgID, target.ID, f.sessionID)
	require.NoError(t, err, "install the genuine owning generation before attempt completion")
	require.NoError(t, tx.Commit(ctx), "finish fixture-only ownership preparation")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,status,lock_token,lease_expires_at,payload) VALUES($1,$2,'agent','continue_session','running',$3,now()+interval '1 hour','{}')`, f.jobID, f.orgID, f.token)
	require.NoError(t, err, "seed the running leased job used by the real marker fence")
	_, err = pool.Exec(ctx, `UPDATE automation_runs SET dispatch_state='executing',target_id=$3,target_generation=$4,session_id=$5,thread_id=$6,turn_number=1,job_id=$7,attempt=1,attempt_lock_token=$8,execution_started_at=now() WHERE org_id=$1 AND id=$2`, f.orgID, f.runID, target.ID, generation.Generation, f.sessionID, f.threadID, f.jobID, f.token)
	require.NoError(t, err, "reserve the exact session/thread/generation and running lease under the real schema")
	return f
}

func readTurnAttemptPostgresRows(ctx context.Context, database db.DBTX, f turnAttemptPostgresFixture) (json.RawMessage, error) {
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
	'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
	'thread',(SELECT to_jsonb(t) FROM session_threads t WHERE org_id=$1 AND id=$3),
	'run',(SELECT to_jsonb(r) FROM automation_runs r WHERE org_id=$1 AND id=$4),
	'marker',(SELECT to_jsonb(m) FROM automation_run_results m WHERE org_id=$1 AND run_id=$4),
	'job',(SELECT to_jsonb(j) FROM jobs j WHERE org_id=$1 AND id=$5),
	'publish',(SELECT jsonb_agg(to_jsonb(p) ORDER BY session_id) FROM session_publish_state p WHERE org_id=$1 AND session_id=$2),
	'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM session_changesets c WHERE org_id=$1 AND session_id=$2),
	'snapshots',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM session_diff_snapshots d WHERE org_id=$1 AND session_id=$2))`, f.orgID, f.sessionID, f.threadID, f.runID, f.jobID).Scan(&rows)
	return rows, err
}
func turnAttemptPostgresRows(t *testing.T, ctx context.Context, database db.DBTX, f turnAttemptPostgresFixture) json.RawMessage {
	t.Helper()
	rows, err := readTurnAttemptPostgresRows(ctx, database, f)
	require.NoError(t, err, "capture every column of every row in the concrete tenant-scoped completion unit")
	return rows
}

func installTurnAttemptFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f turnAttemptPostgresFixture, stage string) {
	t.Helper()
	name := pgx.Identifier{"reject_attempt_" + strings.ReplaceAll(f.orgID.String(), "-", "")}.Sanitize()
	body := fmt.Sprintf(`IF NOT EXISTS(SELECT 1 FROM automation_run_results WHERE org_id='%s' AND run_id='%s') OR (SELECT count(*) FROM session_diff_snapshots WHERE org_id='%s' AND session_id='%s')<>1 OR (SELECT head_sha FROM session_changesets WHERE org_id='%s' AND session_id='%s' AND is_primary)<>'synthetic-head' THEN RAISE EXCEPTION 'incomplete earlier attempt writes'; END IF; RAISE EXCEPTION 'synthetic attempt %s failure';`, f.orgID, f.runID, f.orgID, f.sessionID, f.orgID, f.sessionID, stage)
	ddl := "CREATE FUNCTION " + name + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN " + body + " END $$; "
	if stage == "duration" {
		ddl += "CREATE TRIGGER " + name + " BEFORE UPDATE OF turn_duration_ms ON automation_runs FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid AND NEW.turn_duration_ms=1234) EXECUTE FUNCTION " + name + "()"
	} else {
		ddl += "CREATE CONSTRAINT TRIGGER " + name + " AFTER INSERT ON automation_run_results DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	}
	_, err := pool.Exec(ctx, ddl)
	require.NoError(t, err, "install only a fixture-local tenant-scoped late SQL failure")
}

func newTurnAttemptPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for owner-local attempt effect proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the disposable server without exposing connection data")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(ctx), "close fixture administration")
	})
	var version int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version), "read the unchanged full-schema prerequisite")
	require.GreaterOrEqual(t, version, 150000, "the complete schema requires PostgreSQL 15 or newer")
	name := "turn_attempt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create the independently generated test database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err, "drop only the exact generated database after its connections close, without force")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the fixture URI without exposing private details")
	query := target.Query()
	query.Del("dbname")
	query.Del("database")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	isolated := target.String()
	config, err := pgxpool.ParseConfig(isolated)
	require.True(t, err == nil && config.ConnConfig.Database == name, "all fixture drivers must target only the generated database")
	config.MaxConns, config.MinConns = 2, 0
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve the entire unchanged migration chain")
	migration, err := migrate.New("file://"+source, isolated)
	require.True(t, err == nil, "open only the generated fixture migration driver")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close migration sources")
		require.NoError(t, databaseErr, "close the migration driver before cleanup")
	})
	require.NoError(t, migration.Up(), "apply the complete real schema before actual owner tests")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.True(t, err == nil, "open the bounded fully migrated fixture pool")
	t.Cleanup(pool.Close)
	return pool
}
