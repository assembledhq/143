package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type checkpointPostgresFixture struct {
	turnAttemptPostgresFixture
	generationID uuid.UUID
	at           time.Time
}

type checkpointPostgresStarter struct {
	*pgxpool.Pool
	tx      *checkpointPostgresTx
	begins  int
	observe func(pgx.Tx)
}

func (s *checkpointPostgresStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &checkpointPostgresTx{Tx: tx, owner: s}
	return s.tx, nil
}

type checkpointPostgresTx struct {
	pgx.Tx
	owner           *checkpointPostgresStarter
	nested, commits int
	commitFailed    bool
}

func (tx *checkpointPostgresTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nested++
	return nil, errors.New("checkpoint must never begin a savepoint")
}

func (tx *checkpointPostgresTx) Commit(ctx context.Context) error {
	tx.commits++
	tx.owner.observe(tx.Tx)
	err := tx.Tx.Commit(ctx)
	tx.commitFailed = err != nil
	return err
}

func (tx *checkpointPostgresTx) Rollback(ctx context.Context) error {
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

func TestCheckpointPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newTurnAttemptPostgresPool(t)
	tests := []struct{ name, variant, mode string }{
		{name: "actual owner publishes exact committed session and generation"},
		{name: "bootstrap is not a completed review", variant: "bootstrap"},
		{name: "graceful checkpoint preserves an empty agent identifier", variant: "graceful"},
		{name: "still owned retired generation retains historical eligibility", variant: "retired"},
		{name: "foreign tenant cannot publish", variant: "foreign"},
		{name: "revoked job token cannot publish", variant: "job token"},
		{name: "revoked attempt token cannot publish", variant: "attempt token"},
		{name: "wrong owning generation cannot publish", variant: "generation"},
		{name: "missing provenance false rolls staged session update back", variant: "missing"},
		{name: "late real provenance SQL error rolls the session back", variant: "sql"},
		{name: "real deferred commit failure discards captured event", variant: "commit"},
		{name: "optional notification failure keeps committed checkpoint", variant: "notification"},
		{name: "caller transaction retains commit and event ownership", mode: "transaction"},
		{name: "caller SQL rejects revoked job token without owner lock", mode: "transaction", variant: "job token"},
		{name: "caller rollback discards genuine checkpoint and provenance", mode: "transaction", variant: "rollback"},
		{name: "caller missing provenance false requires whole rollback", mode: "transaction", variant: "missing"},
		{name: "standalone DBTX retains immediate notification", mode: "standalone"},
		{name: "standalone missing provenance retains legacy false with mutation", mode: "standalone", variant: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			f := seedCheckpointPostgres(t, ctx, pool)
			kind, agentID := models.CheckpointKindTurnComplete, "synthetic-agent"
			generation, requestedOrg := f.generationID, f.orgID
			switch tt.variant {
			case "bootstrap":
				kind = models.CheckpointKindBootstrap
			case "graceful":
				kind, agentID = models.CheckpointKindGracefulStop, ""
			case "retired":
				_, err := pool.Exec(ctx, "UPDATE automation_target_sessions SET status='retired',retired_reason='pr_closed',retired_at=$3,ownership_release_pending=true WHERE org_id=$1 AND id=$2", f.orgID, f.generationID, f.at.Add(-time.Hour))
				require.NoError(t, err, "retain a matching historically retired generation while its executing turn still owns the session")
			case "foreign":
				requestedOrg = uuid.New()
			case "job token":
				_, err := pool.Exec(ctx, "UPDATE jobs SET lock_token=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.jobID, uuid.New())
				require.NoError(t, err, "revoke only this synthetic job's existing token")
			case "attempt token":
				_, err := pool.Exec(ctx, "UPDATE automation_runs SET attempt_lock_token=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.runID, uuid.New())
				require.NoError(t, err, "revoke only this synthetic attempt's existing token")
			case "generation":
				generation = uuid.New()
			case "missing":
				generation = uuid.New()
				_, err := pool.Exec(ctx, "UPDATE sessions SET automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID, generation)
				require.NoError(t, err, "characterize the legacy owner-marker-without-provenance-row edge, not fabricate a matching generation")
			}
			if tt.variant == "sql" || tt.variant == "commit" {
				installCheckpointPostgresFailure(t, ctx, pool, f, tt.variant)
			}
			before := checkpointPostgresRows(t, ctx, pool, f)
			observer, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			require.True(t, err == nil, "preopen a dedicated observer from only the copied generated fixture configuration without disclosing connection data")
			t.Cleanup(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, observer.Close(closeCtx), "close the independent checkpoint observer with a separate bounded context")
			})
			config := pool.Config()
			config.MaxConns, config.MinConns = 1, 0
			ownerPool, err := pgxpool.NewWithConfig(ctx, config)
			require.True(t, err == nil, "open the generated fixture with one physical owner connection without exposing connection data")
			t.Cleanup(ownerPool.Close)
			starter := &checkpointPostgresStarter{Pool: ownerPool}
			sessions, redis := turnAttemptStreams(t, starter)
			var staged atomic.Value
			starter.observe = func(tx pgx.Tx) {
				staged.Store(checkpointPostgresRows(t, ctx, tx, f))
				require.Empty(t, redis.Keys(), "neither checkpoint nor provenance persistence may notify before physical owner settlement")
				require.NotEqual(t, tx.Conn().PgConn().PID(), observer.PgConn().PID(), "checkpoint visibility must be read through a separate physical connection")
				require.Equal(t, before, checkpointPostgresRows(t, ctx, observer, f), "independent readers must see the exact original session, generation, run, target, thread and job before commit")
			}
			commands := turnAttemptCaptureCommands(redis, func() error {
				observed, err := readCheckpointPostgresRows(ctx, observer, f)
				if err != nil {
					return fmt.Errorf("observe accepted checkpoint graph: %w", err)
				}
				if tt.mode == "standalone" {
					if bytes.Equal(before, observed) {
						return errors.New("standalone event preceded checkpoint persistence")
					}
					staged.Store(observed)
				} else {
					expected, ok := staged.Load().(json.RawMessage)
					if !ok || !bytes.Equal(expected, observed) {
						return errors.New("checkpoint event preceded the exact complete physical owner commit")
					}
				}
				return nil
			})
			if tt.variant == "notification" {
				redis.SetError("ERR synthetic checkpoint notification failure")
			}
			fingerprint := "synthetic-dependencies"
			provenance := models.CheckpointProvenance{GenerationID: generation, HeadSHA: " synthetic-head ", DependencyFingerprint: &fingerprint, ReviewComplete: true}
			var published bool
			var publishErr error
			switch tt.mode {
			case "transaction":
				tx, err := starter.Begin(ctx)
				require.NoError(t, err, "only the explicit outer caller begins its actual checkpoint transaction")
				defer func() { _ = tx.Rollback(context.Background()) }()
				var effects db.SessionPersistenceEffects
				published, effects, publishErr = sessions.PublishCheckpointWithProvenanceInTx(ctx, f.orgID, tx, f.sessionID, f.token, agentID, "synthetic-checkpoint", kind, models.CheckpointCapabilityFullResume, 1234, f.at, models.RuntimeStopReasonSoftBudget, provenance)
				require.NoError(t, publishErr, "the supplied transaction must remain usable after success or an original SQL fence rejection")
				starter.observe(tx)
				if tt.variant == "job token" {
					require.False(t, published, "the seam's real job-token SQL predicate must reject even without the owner's earlier LockAttempt check")
					require.Equal(t, db.SessionPersistenceEffects{}, effects, "a direct job-token rejection cannot return a publishable workspace effect")
					captured, ok := staged.Load().(json.RawMessage)
					require.True(t, ok, "direct SQL token rejection must capture the real still-usable caller transaction")
					require.NotNil(t, captured, "a missing capture cannot prove that the direct SQL fence preserved all rows")
					require.Equal(t, before, captured, "the direct revoked-token SQL branch must stage no session, generation, lease or target mutation")
				} else {
					require.NotEqual(t, before, staged.Load(), "success and the legacy missing-provenance false edge must both contain actual staged session writes")
				}
				if tt.variant == "missing" {
					require.False(t, published, "missing provenance must preserve the legacy joined-result false return")
					require.Equal(t, db.SessionPersistenceEffects{}, effects, "a false checkpoint must not return a publishable effect even after staged session mutation")
				}
				var later int
				require.NoError(t, tx.QueryRow(ctx, "SELECT 42").Scan(&later), "the seam must leave its caller's concrete transaction usable for later work")
				require.Equal(t, 42, later, "later caller work must run on the same occupied owner connection")
				if tt.variant == "rollback" || tt.variant == "missing" || tt.variant == "job token" {
					require.NoError(t, tx.Rollback(ctx), "the caller alone rolls back the entire checkpoint and generation unit")
					if tt.variant == "job token" {
						require.Zero(t, starter.tx.commits, "a direct false result must be explicitly rolled back rather than committed or retried")
					}
				} else {
					require.NoError(t, tx.Commit(ctx), "the caller alone physically commits checkpoint and provenance")
					require.Empty(t, redis.Keys(), "physical commit itself must not implicitly publish the captured checkpoint effect")
					sessions.PublishCommittedSessionPersistenceEffects(ctx, f.orgID, effects)
				}
			case "standalone":
				published, publishErr = sessions.PublishCheckpointWithProvenance(ctx, f.orgID, f.sessionID, f.token, agentID, "synthetic-checkpoint", kind, models.CheckpointCapabilityFullResume, 1234, f.at, models.RuntimeStopReasonSoftBudget, provenance)
			default:
				store := NewTurnStore(starter, sessions, db.NewAutomationRunStore(starter), nil, nil, nil)
				published, publishErr = store.PublishCheckpointWithProvenance(ctx, requestedOrg, f.sessionID, f.runID, f.token, agentID, "synthetic-checkpoint", kind, models.CheckpointCapabilityFullResume, 1234, f.at, models.RuntimeStopReasonSoftBudget, provenance)
			}
			after := checkpointPostgresRows(t, ctx, observer, f)
			expectedBegin := 1
			if tt.mode == "standalone" {
				expectedBegin = 0
			}
			require.Equal(t, expectedBegin, starter.begins, "the actual owner begins once, the direct seam never begins, and the standalone DBTX wrapper does not gain a transaction")
			if starter.tx != nil {
				require.Zero(t, starter.tx.nested, "none of the actual checkpoint paths may create a savepoint or nested transaction")
			}
			if tt.variant == "sql" || tt.variant == "commit" {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, publishErr, &pgErr, "late checkpoint rejection must come from genuine PostgreSQL SQL or physical deferred Commit")
				require.Equal(t, "synthetic checkpoint "+tt.variant+" failure", pgErr.Message, "fixture SQL must first verify genuine preceding session and provenance data before its selected late failure")
				require.False(t, published, "a real SQL or physical commit failure must not report acceptance")
				if tt.variant == "commit" {
					require.Equal(t, 1, starter.tx.commits, "the deferred PostgreSQL failure must occur at the actual owner's one physical Commit")
					require.NotEqual(t, before, staged.Load(), "deferred commit failure must follow real staged checkpoint and provenance writes")
				}
			} else {
				require.NoError(t, publishErr, "legacy fence rejection, success and optional notification failures retain their exact error-free result")
			}
			rejected := tt.variant == "foreign" || tt.variant == "job token" || tt.variant == "attempt token" || tt.variant == "generation" || tt.variant == "missing" || tt.variant == "sql" || tt.variant == "commit"
			require.Equal(t, !rejected, published, "only the existing lease, ownership, joined provenance and commit contract determines checkpoint acceptance")
			rolledBack := rejected || tt.variant == "rollback"
			if tt.mode == "standalone" && tt.variant == "missing" {
				rolledBack = false
				require.NotEqual(t, before, after, "characterize the unchanged standalone false-with-session-mutation legacy quirk rather than silently repairing SQL")
				requireCheckpointPostgresValues(t, ctx, observer, f, kind, agentID, false)
			} else if rolledBack {
				require.Equal(t, before, after, "the real outer owner must restore every session, generation, target, run, thread and job column on false, late failure or caller rollback")
			} else {
				require.Equal(t, staged.Load(), after, "every accepted event must correspond to exactly the entire owner-staged row graph")
				requireCheckpointPostgresValues(t, ctx, observer, f, kind, agentID, true)
			}
			if tt.variant == "missing" && tt.mode != "standalone" {
				require.NotEqual(t, before, staged.Load(), "missing provenance false must be tested after the real CTE stages a session mutation, before outer rollback")
			}
			actualCommands, hookErrors := commands()
			require.Empty(t, hookErrors, "each accepted Redis command must independently observe the exact already committed checkpoint graph")
			if !rolledBack && published && tt.variant != "notification" {
				require.Equal(t, []string{"XADD:143:stream:{ses:" + f.sessionID.String() + "}:events"}, actualCommands, "accepted checkpoint persistence emits exactly its original single workspace event and no status, expiry or readiness event")
				requireAutomationCheckpointEvent(t, redis, f.orgID, f.sessionID, 5, f.at.UTC())
			} else {
				require.Empty(t, redis.Keys(), "all rollbacks, rejected provenance and optional notification failures must leave every stream empty")
			}
		})
	}
}

func seedCheckpointPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) checkpointPostgresFixture {
	t.Helper()
	f := checkpointPostgresFixture{turnAttemptPostgresFixture: seedTurnAttemptPostgres(t, ctx, pool), at: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.FixedZone("synthetic", 3600))}
	require.NoError(t, pool.QueryRow(ctx, "SELECT automation_owner_generation_id FROM sessions WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID).Scan(&f.generationID), "read the genuine generation installed by the production ownership constructor")
	_, err := pool.Exec(ctx, "UPDATE jobs SET payload=jsonb_build_object('session_id',$3::text) WHERE org_id=$1 AND id=$2", f.orgID, f.jobID, f.sessionID.String())
	require.NoError(t, err, "preserve the exact session binding required by the legacy checkpoint job predicate")
	_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic-prior-checkpoint',agent_session_id='synthetic-prior-agent',checkpoint_kind='bootstrap',checkpoint_capability='full_resume',checkpoint_size_bytes=11,checkpointed_at=$3,checkpoint_error='synthetic prior error',runtime_graceful_stop_at=$3,workspace_revision=4,workspace_revision_updated_at=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID, f.at.Add(-time.Hour))
	require.NoError(t, err, "seed every legacy checkpoint field so exact rollback cannot pass by comparing empty defaults")
	_, err = pool.Exec(ctx, `UPDATE automation_target_sessions SET checkpoint_snapshot_key='synthetic-prior-checkpoint',checkpoint_head_sha='synthetic-prior-head',checkpoint_dependency_fingerprint='synthetic-prior-dependencies',checkpoint_review_complete=false WHERE org_id=$1 AND id=$2`, f.orgID, f.generationID)
	require.NoError(t, err, "seed existing generation provenance whose historical values must be preserved on every rejection")
	return f
}

func readCheckpointPostgresRows(ctx context.Context, database db.DBTX, f checkpointPostgresFixture) (json.RawMessage, error) {
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
	'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
	'thread',(SELECT to_jsonb(t) FROM session_threads t WHERE org_id=$1 AND id=$3),
	'run',(SELECT to_jsonb(r) FROM automation_runs r WHERE org_id=$1 AND id=$4),
	'job',(SELECT to_jsonb(j) FROM jobs j WHERE org_id=$1 AND id=$5),
	'generations',(SELECT jsonb_agg(to_jsonb(g) ORDER BY id) FROM automation_target_sessions g WHERE org_id=$1 AND session_id=$2),
	'targets',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM automation_targets t WHERE org_id=$1 AND id IN (SELECT target_id FROM automation_target_sessions WHERE org_id=$1 AND session_id=$2)))`, f.orgID, f.sessionID, f.threadID, f.runID, f.jobID).Scan(&rows)
	return rows, err
}

func checkpointPostgresRows(t *testing.T, ctx context.Context, database db.DBTX, f checkpointPostgresFixture) json.RawMessage {
	t.Helper()
	rows, err := readCheckpointPostgresRows(ctx, database, f)
	require.NoError(t, err, "capture every column of the exact tenant-scoped checkpoint, generation and lease graph")
	return rows
}

func requireCheckpointPostgresValues(t *testing.T, ctx context.Context, database db.DBTX, f checkpointPostgresFixture, kind models.CheckpointKind, agentID string, generationPublished bool) {
	t.Helper()
	var actual struct {
		Agent, Key, Sandbox, Kind, Capability, Reason string
		Size, Revision                                int64
		At, RevisionAt, StopAt                        time.Time
		Error                                         *string
	}
	err := database.QueryRow(ctx, `SELECT agent_session_id,snapshot_key,sandbox_state,checkpoint_kind,checkpoint_capability,runtime_stop_reason,checkpoint_size_bytes,workspace_revision,checkpointed_at,workspace_revision_updated_at,runtime_graceful_stop_at,checkpoint_error FROM sessions WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID).Scan(&actual.Agent, &actual.Key, &actual.Sandbox, &actual.Kind, &actual.Capability, &actual.Reason, &actual.Size, &actual.Revision, &actual.At, &actual.RevisionAt, &actual.StopAt, &actual.Error)
	require.NoError(t, err, "inspect exact independently committed session checkpoint values")
	actual.At, actual.RevisionAt, actual.StopAt = actual.At.UTC(), actual.RevisionAt.UTC(), actual.StopAt.UTC()
	if agentID == "" {
		agentID = "synthetic-prior-agent"
	}
	expected := actual
	expected.Agent, expected.Key, expected.Sandbox, expected.Kind, expected.Capability, expected.Reason = agentID, "synthetic-checkpoint", "snapshotted", string(kind), "full_resume", "soft_budget"
	expected.Size, expected.Revision, expected.At, expected.RevisionAt, expected.StopAt, expected.Error = 1234, 5, f.at.UTC(), f.at.UTC(), f.at.UTC(), nil
	require.Equal(t, expected, actual, "preserve every checkpoint field, UTC timestamp, agent fallback, revision increment and stop bookkeeping exactly")
	var key, head, fingerprint string
	var review bool
	require.NoError(t, database.QueryRow(ctx, "SELECT checkpoint_snapshot_key,checkpoint_head_sha,checkpoint_dependency_fingerprint,checkpoint_review_complete FROM automation_target_sessions WHERE org_id=$1 AND id=$2", f.orgID, f.generationID).Scan(&key, &head, &fingerprint, &review), "read the actual persisted generation provenance without a current-state event reread")
	expectedValues := []any{"synthetic-checkpoint", "synthetic-head", "synthetic-dependencies", kind == models.CheckpointKindTurnComplete}
	if !generationPublished {
		expectedValues = []any{"synthetic-prior-checkpoint", "synthetic-prior-head", "synthetic-prior-dependencies", false}
	}
	require.Equal(t, expectedValues, []any{key, head, fingerprint, review}, "checkpoint provenance and completed-review gating must retain their exact existing historical semantics")
}

func installCheckpointPostgresFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f checkpointPostgresFixture, stage string) {
	t.Helper()
	name := pgx.Identifier{"checkpoint_failure_" + strings.ReplaceAll(f.orgID.String(), "-", "")}.Sanitize()
	body := fmt.Sprintf(`IF (SELECT snapshot_key FROM sessions WHERE org_id='%s' AND id='%s') IS DISTINCT FROM 'synthetic-checkpoint' OR (SELECT workspace_revision FROM sessions WHERE org_id='%s' AND id='%s')<>5 OR NEW.checkpoint_snapshot_key IS DISTINCT FROM 'synthetic-checkpoint' OR NEW.checkpoint_head_sha IS DISTINCT FROM 'synthetic-head' OR NEW.checkpoint_dependency_fingerprint IS DISTINCT FROM 'synthetic-dependencies' OR NEW.checkpoint_review_complete IS DISTINCT FROM true THEN RAISE EXCEPTION 'incomplete earlier checkpoint writes'; END IF; RAISE EXCEPTION 'synthetic checkpoint %s failure';`, f.orgID, f.sessionID, f.orgID, f.sessionID, stage)
	ddl := "CREATE FUNCTION " + name + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN " + body + " END $$; "
	if stage == "commit" {
		ddl += "CREATE CONSTRAINT TRIGGER " + name + " AFTER UPDATE ON automation_target_sessions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	} else {
		ddl += "CREATE TRIGGER " + name + " BEFORE UPDATE ON automation_target_sessions FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	}
	_, err := pool.Exec(ctx, ddl)
	require.NoError(t, err, "install only a tenant-scoped late SQL or deferred-commit failure in the generated disposable fixture")
}
