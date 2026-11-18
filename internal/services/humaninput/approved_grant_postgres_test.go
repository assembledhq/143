package humaninput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type answerGrantFixture struct {
	orgID, userID, sessionID, threadID, requestID, runID uuid.UUID
}

type answerGrantStarter struct {
	*pgxpool.Pool
	tx                        *answerGrantGuardTx
	begins                    int
	beforeCommit, beforeAbort func(pgx.Tx)
}

func (s *answerGrantStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &answerGrantGuardTx{Tx: tx, owner: s}
	return s.tx, nil
}

type answerGrantGuardTx struct {
	pgx.Tx
	owner                            *answerGrantStarter
	nestedBegins, commits, rollbacks int
	commitFailed                     bool
}

func (tx *answerGrantGuardTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nestedBegins++
	return nil, errors.New("nested answer grant transaction is forbidden")
}

func (tx *answerGrantGuardTx) Commit(ctx context.Context) error {
	tx.commits++
	if tx.owner.beforeCommit != nil {
		tx.owner.beforeCommit(tx.Tx)
	}
	err := tx.Tx.Commit(ctx)
	tx.commitFailed = err != nil
	return err
}

func (tx *answerGrantGuardTx) Rollback(ctx context.Context) error {
	tx.rollbacks++
	// A failed assertion in the observation hook must not strand the actual
	// transaction and make this test's one-connection pool cleanup hang.
	settled := false
	defer func() {
		if !settled {
			_ = tx.Tx.Rollback(context.Background())
		}
	}()
	if tx.owner.beforeAbort != nil && !tx.commitFailed && tx.Conn().PgConn().TxStatus() == 'T' {
		tx.owner.beforeAbort(tx.Tx)
	}
	err := tx.Tx.Rollback(ctx)
	settled = true
	return err
}

func TestHumanInputApprovedGrantFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newAnswerGrantPostgresPool(t)
	tests := []struct{ name, variant, failure string }{
		{name: "linked approval commits one atomic unit"},
		{name: "standalone approval commits one atomic unit", variant: "standalone"},
		{name: "duplicate grant preserves original snapshots and resumes", variant: "duplicate"},
		{name: "denial resumes without granting", variant: "deny"},
		{name: "nil capability service retains existing resume", variant: "nil approver"},
		{name: "foreign tenant cannot answer or grant", variant: "foreign"},
		{name: "request from another session cannot bind a grant", variant: "wrong request"},
		{name: "invalid capability rolls the answer back", variant: "invalid"},
		{name: "late session claim rejects the whole unit", failure: "claim"},
		{name: "late message failure rejects the whole unit", failure: "message"},
		{name: "late enqueue failure rejects the whole unit", failure: "enqueue"},
		{name: "real deferred PostgreSQL commit failure rejects the whole unit", failure: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			fixture := seedAnswerGrantPostgres(t, ctx, pool)
			if tt.variant == "standalone" {
				_, err := pool.Exec(ctx, "DELETE FROM session_automation_links WHERE org_id=$1 AND session_id=$2", fixture.orgID, fixture.sessionID)
				require.NoError(t, err, "exercise a standalone session with no linked automation mirror")
			}
			if tt.variant == "invalid" {
				_, err := pool.Exec(ctx, `UPDATE session_human_input_requests SET provider_payload='{"type":"agent_capability_request","capability_id":"unknown","access_level":"read"}' WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.requestID)
				require.NoError(t, err, "persist synthetic invalid catalog metadata without changing answer validation rules")
			}
			var priorSnapshot []models.AgentCapabilitySnapshotItem
			if tt.variant == "duplicate" {
				builder := &answerGrantBuilder{}
				item, err := builder.BuildApprovedGrant(ctx, fixture.orgID, fixture.sessionID, fixture.requestID, models.AgentCapabilitySessionHistory, models.AgentCapabilityAccessRead)
				require.NoError(t, err, "construct the preexisting valid grant for the legacy retry case")
				item.GrantedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				priorSnapshot, err = db.NewAgentCapabilityPolicyStore(pool).AppendApprovedSessionGrant(ctx, fixture.orgID, fixture.sessionID, item)
				require.NoError(t, err, "persist a preexisting grant before the answer transaction begins")
			}
			if tt.failure != "" {
				installAnswerGrantFailure(t, ctx, pool, fixture, tt.failure)
			}
			before := answerGrantPostgresRows(t, ctx, pool, fixture)
			config := pool.Config()
			config.MaxConns, config.MinConns = 1, 0
			ownerPool, err := pgxpool.NewWithConfig(ctx, config)
			require.True(t, err == nil, "open only the generated fixture with a single owner connection without printing private connection data")
			t.Cleanup(ownerPool.Close)
			// DDL in another parallel case may occupy every setup-pool
			// connection while waiting for Answer's table locks. The observer
			// must already own an independent connection before Answer starts,
			// rather than wait for that pool outside PostgreSQL's lock graph.
			observer, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			require.True(t, err == nil, "open a dedicated observer on the copied generated fixture configuration without exposing connection data")
			t.Cleanup(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, observer.Close(closeCtx), "close the dedicated observer with an independent bounded cleanup context")
			})
			starter := &answerGrantStarter{Pool: ownerPool}
			var staged json.RawMessage
			observe := func(tx pgx.Tx) {
				staged = answerGrantPostgresRows(t, ctx, tx, fixture)
				require.NotEqual(t, tx.Conn().PgConn().PID(), observer.PgConn().PID(), "the visibility observer must own a different physical connection from the answer transaction")
				require.Equal(t, before, answerGrantPostgresRows(t, ctx, observer, fixture), "the dedicated observer must see no answer, grant, automation snapshot, claim, message or job before outer commit without borrowing the setup or DDL pool")
			}
			starter.beforeCommit, starter.beforeAbort = observe, observe
			svc := NewDBService(db.NewSessionStore(starter), db.NewSessionHumanInputRequestStore(starter), db.NewSessionQuestionStore(starter), db.NewSessionMessageStore(starter), db.NewSessionThreadStore(starter), db.NewJobStore(starter))
			builder := &answerGrantBuilder{}
			if tt.variant != "nil approver" {
				svc.SetCapabilityApprover(builder)
			}
			input := AnswerInput{OrgID: fixture.orgID, SessionID: fixture.sessionID, RequestID: fixture.requestID, UserID: fixture.userID, Answer: models.HumanInputAnswerInput{SelectedChoiceIDs: []string{"approve"}}}
			var otherRequest *models.HumanInputRequest
			if tt.variant == "deny" {
				input.Answer.SelectedChoiceIDs = []string{"deny"}
			} else if tt.variant == "foreign" {
				input.OrgID = uuid.New()
			} else if tt.variant == "wrong request" {
				otherSession := &models.Session{OrgID: fixture.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
				require.NoError(t, db.NewSessionStore(pool).Create(ctx, otherSession), "seed another real session in the same tenant so org-only filtering cannot satisfy request binding")
				otherRequest = &models.HumanInputRequest{OrgID: fixture.orgID, SessionID: otherSession.ID, ThreadID: otherSession.PrimaryThreadID, AgentType: otherSession.AgentType, Kind: models.HumanInputRequestKindToolApproval, Title: "Other session approval", Body: "Synthetic other request", Choices: []models.HumanInputChoice{{ID: "approve", Label: "Approve"}}}
				require.NoError(t, db.NewSessionHumanInputRequestStore(pool).Create(ctx, otherRequest), "persist a known request belonging to another session under the same tenant")
				input.RequestID = otherRequest.ID
			}
			result, answerErr := svc.Answer(ctx, input)
			require.Equal(t, 1, starter.begins, "actual Answer must own exactly one physical transaction even with the pool's only connection occupied")
			require.Zero(t, starter.tx.nestedBegins, "grant composition must never create a savepoint or independent nested transaction")
			after := answerGrantPostgresRows(t, ctx, pool, fixture)
			rejected := tt.failure != "" || tt.variant == "foreign" || tt.variant == "wrong request" || tt.variant == "invalid"
			if rejected {
				require.Error(t, answerErr, "every rejected answer boundary must report its existing validation or persistence failure")
				require.Equal(t, Result{}, result, "rejected answer transactions must not leak accepted result data")
				require.Equal(t, before, after, "answer, grant, mirrored automation snapshot, claims, messages and jobs must all roll back to the exact original rows")
				require.Equal(t, 1, starter.tx.rollbacks, "the actual repository must perform its outer failure cleanup exactly once")
				if otherRequest != nil {
					preserved, err := db.NewSessionHumanInputRequestStore(pool).GetByID(ctx, otherRequest.OrgID, otherRequest.SessionID, otherRequest.ID)
					require.NoError(t, err, "inspect the same-tenant request that must not be rebound to another session")
					require.Equal(t, *otherRequest, preserved, "mismatched session/request identity must preserve the entire other pending request")
				}
				if tt.failure == "claim" || tt.failure == "message" {
					require.NotEqual(t, before, staged, "application-level late rejection must occur after genuine answer and grant writes inside the usable owner transaction")
					requireAnswerGrantStaged(t, staged, builder.item, tt.failure == "message", false)
				} else if tt.failure == "enqueue" || tt.failure == "commit" {
					var pgErr *pgconn.PgError
					require.ErrorAs(t, answerErr, &pgErr, "the fixture must exercise a genuine PostgreSQL enqueue or deferred commit failure")
					require.Equal(t, "synthetic answer grant "+tt.failure+" failure", pgErr.Message, "fixture SQL must first validate the genuine preceding writes before raising the selected late failure")
					if tt.failure == "commit" {
						requireAnswerGrantStaged(t, staged, builder.item, true, true)
						require.Equal(t, 1, starter.tx.commits, "the real deferred failure must occur in the repository's actual outer Commit")
					}
				}
				return
			}
			require.NoError(t, answerErr, "the actual answer path must commit without any independent grant appender")
			require.Equal(t, staged, after, "the physical outer commit must make exactly the complete staged row set durable")
			require.Equal(t, 1, starter.tx.commits, "successful Answer must commit the one complete unit exactly once")
			require.Zero(t, starter.tx.rollbacks, "successful Answer must not attempt an additional abort")
			var sessionSnapshot, runSnapshot []models.AgentCapabilitySnapshotItem
			require.NoError(t, pool.QueryRow(ctx, "SELECT COALESCE((SELECT capability_snapshot FROM session_execution_metadata WHERE org_id=$1 AND session_id=$2),'[]'::jsonb)", fixture.orgID, fixture.sessionID).Scan(&sessionSnapshot), "read the exact committed session capability snapshot, preserving legitimate absent metadata on no-grant paths")
			require.NoError(t, pool.QueryRow(ctx, "SELECT capability_snapshot FROM automation_runs WHERE org_id=$1 AND id=$2", fixture.orgID, fixture.runID).Scan(&runSnapshot), "read the exact committed linked automation snapshot")
			expectedSnapshot := []models.AgentCapabilitySnapshotItem{}
			if tt.variant == "duplicate" {
				expectedSnapshot = priorSnapshot
			} else if tt.variant != "deny" && tt.variant != "nil approver" {
				expectedSnapshot = []models.AgentCapabilitySnapshotItem{builder.item}
			}
			require.Equal(t, expectedSnapshot, sessionSnapshot, "approval must persist the complete validated item exactly once, retaining duplicate timestamps and request identity")
			if tt.variant == "deny" || tt.variant == "nil approver" {
				require.Zero(t, builder.calls, "denial and nil capability service must not even construct a grant")
				var metadataCount int
				require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM session_execution_metadata WHERE org_id=$1 AND session_id=$2", fixture.orgID, fixture.sessionID).Scan(&metadataCount), "inspect exact metadata absence on the no-grant path")
				require.Zero(t, metadataCount, "no-grant paths must not fabricate even an empty capability metadata row")
			} else {
				require.Equal(t, 1, builder.calls, "an explicit approval must construct exactly one grant")
				require.Equal(t, []uuid.UUID{fixture.orgID, fixture.sessionID, fixture.requestID}, builder.identity, "construction must retain the exact owned request, session and tenant binding")
			}
			if tt.variant == "standalone" {
				require.Equal(t, []models.AgentCapabilitySnapshotItem{}, runSnapshot, "standalone session approval must not touch an unrelated automation snapshot")
			} else {
				require.Equal(t, expectedSnapshot, runSnapshot, "linked session and automation capability snapshots must commit atomically with identical grant data")
			}
			require.NotNil(t, result.Message, "a committed answer must return its real durable user message")
			require.NotEqual(t, uuid.Nil, result.JobID, "a committed answer must return its real durable continuation job")
			content := "Approve"
			if tt.variant == "deny" {
				content = "Deny"
			}
			messages, err := db.NewSessionMessageStore(pool).ListBySession(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "read every field of the actual committed structured-answer message")
			expectedMessage := models.SessionMessage{ID: result.Message.ID, OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: &fixture.threadID, UserID: &fixture.userID, TurnNumber: 8, Role: models.MessageRoleUser, Content: content, CreatedAt: result.Message.CreatedAt, References: models.SessionInputReferences{}, Commands: models.SessionInputCommands{}}
			require.Equal(t, []models.SessionMessage{expectedMessage}, messages, "grant composition must preserve the exact approval/denial message, user attribution and originating thread turn")
			var payload map[string]string
			require.NoError(t, pool.QueryRow(ctx, "SELECT payload FROM jobs WHERE org_id=$1 AND id=$2", fixture.orgID, result.JobID).Scan(&payload), "read the actual committed continuation identity")
			require.Equal(t, map[string]string{"org_id": fixture.orgID.String(), "session_id": fixture.sessionID.String(), "thread_id": fixture.threadID.String(), "human_input_request_id": fixture.requestID.String(), "human_input_request_status": "ready"}, payload, "continuation must preserve the exact answered request, session, tenant and originating thread")
			starter.beforeCommit, starter.beforeAbort = nil, nil
			_, repeatErr := svc.Answer(ctx, input)
			require.ErrorIs(t, repeatErr, ErrNotPending, "repeated answer delivery must retain the existing no-second-resume behavior")
			require.Equal(t, after, answerGrantPostgresRows(t, ctx, pool, fixture), "duplicate answer delivery cannot mutate the grant, its timestamp, messages or job")
		})
	}
}

func seedAnswerGrantPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) answerGrantFixture {
	t.Helper()
	f := answerGrantFixture{orgID: uuid.New(), userID: uuid.New(), runID: uuid.New()}
	_, err := pool.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,'synthetic answer grant')", f.orgID)
	require.NoError(t, err, "seed the isolated synthetic tenant")
	_, err = pool.Exec(ctx, "INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$1::uuid::text||'@example.invalid','Synthetic answer user','member')", f.userID, f.orgID)
	require.NoError(t, err, "seed the authenticated synthetic answering user")
	automationID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic','Test atomic answer grants')", automationID, f.orgID)
	require.NoError(t, err, "seed the linked synthetic automation")
	_, err = pool.Exec(ctx, "INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,triggered_at) VALUES($1,$2,$3,'Synthetic','running',now())", f.runID, f.orgID, automationID)
	require.NoError(t, err, "seed the automation run whose snapshot must share settlement")
	session := &models.Session{OrgID: f.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginAutomation, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, AutomationRunID: &f.runID}
	require.NoError(t, db.NewSessionStore(pool).Create(ctx, session), "create the real session, execution metadata, automation link and primary thread")
	f.sessionID, f.threadID = session.ID, *session.PrimaryThreadID
	_, err = pool.Exec(ctx, "UPDATE sessions SET status='awaiting_input',sandbox_state='running',snapshot_key='synthetic-checkpoint',current_turn=2 WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
	require.NoError(t, err, "seed the existing durable ready checkpoint without new admission semantics")
	_, err = pool.Exec(ctx, "UPDATE session_threads SET status='awaiting_input',current_turn=7 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID)
	require.NoError(t, err, "seed the exact awaiting originating thread")
	request := &models.HumanInputRequest{OrgID: f.orgID, SessionID: f.sessionID, ThreadID: &f.threadID, TurnNumber: 7, AgentType: session.AgentType, Kind: models.HumanInputRequestKindToolApproval, Title: "Synthetic approval", Body: "Approve session history?", Choices: []models.HumanInputChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}, ProviderPayload: json.RawMessage(`{"type":"agent_capability_request","capability_id":"session_history","access_level":"read"}`)}
	require.NoError(t, db.NewSessionHumanInputRequestStore(pool).Create(ctx, request), "persist the actual pending structured approval request")
	f.requestID = request.ID
	return f
}

func answerGrantPostgresRows(t *testing.T, ctx context.Context, database db.DBTX, f answerGrantFixture) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
		'thread',(SELECT to_jsonb(t) FROM session_threads t WHERE org_id=$1 AND id=$3),
		'request',(SELECT to_jsonb(r) FROM session_human_input_requests r WHERE org_id=$1 AND id=$4),
		'metadata',(SELECT to_jsonb(m) FROM session_execution_metadata m WHERE org_id=$1 AND session_id=$2),
		'automation',(SELECT to_jsonb(a) FROM automation_runs a WHERE org_id=$1 AND id=$5),
		'messages',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM session_messages m WHERE org_id=$1 AND session_id=$2),'[]'),
		'jobs',COALESCE((SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j WHERE org_id=$1),'[]'))`, f.orgID, f.sessionID, f.threadID, f.requestID, f.runID).Scan(&rows)
	require.NoError(t, err, "capture every column of the entire tenant-scoped answer and grant unit")
	return rows
}

func requireAnswerGrantStaged(t *testing.T, rows json.RawMessage, item models.AgentCapabilitySnapshotItem, claimed, queued bool) {
	t.Helper()
	var staged struct {
		Session  struct{ Status string }
		Request  struct{ Status string }
		Metadata struct {
			Snapshot []models.AgentCapabilitySnapshotItem `json:"capability_snapshot"`
		}
		Automation struct {
			Snapshot []models.AgentCapabilitySnapshotItem `json:"capability_snapshot"`
		}
		Messages []json.RawMessage
		Jobs     []json.RawMessage
	}
	require.NoError(t, json.Unmarshal(rows, &staged), "inspect genuine staged data without replacing full-row rollback comparisons")
	require.Equal(t, "answered", staged.Request.Status, "the answer must already be staged at the selected late failure")
	require.Equal(t, []models.AgentCapabilitySnapshotItem{item}, staged.Metadata.Snapshot, "the exact constructed grant must already be staged in session metadata")
	require.Equal(t, staged.Metadata.Snapshot, staged.Automation.Snapshot, "the identical automation snapshot must already share the uncommitted unit")
	expectedStatus := "awaiting_input"
	if claimed {
		expectedStatus = "running"
	}
	require.Equal(t, expectedStatus, staged.Session.Status, "the staged session claim must match the selected failure boundary")
	expectedCount := 0
	if queued {
		expectedCount = 1
	}
	require.Len(t, staged.Messages, expectedCount, "only the pre-commit case must already contain its actual resume message")
	require.Len(t, staged.Jobs, expectedCount, "only the pre-commit case must already contain its actual continuation job")
}

func installAnswerGrantFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f answerGrantFixture, stage string) {
	t.Helper()
	name := pgx.Identifier{"answer_grant_failure_" + strings.ReplaceAll(f.orgID.String(), "-", "")}.Sanitize()
	checks := fmt.Sprintf(`IF (SELECT status FROM session_human_input_requests WHERE org_id='%s' AND id='%s') <> 'answered'
		OR NOT EXISTS(SELECT 1 FROM session_execution_metadata m,jsonb_array_elements(m.capability_snapshot) i WHERE m.org_id='%s' AND m.session_id='%s' AND i->>'id'='session_history' AND i->>'human_input_request_id'='%s')
		OR (SELECT capability_snapshot FROM session_execution_metadata WHERE org_id='%s' AND session_id='%s') IS DISTINCT FROM (SELECT capability_snapshot FROM automation_runs WHERE org_id='%s' AND id='%s')
		THEN RAISE EXCEPTION 'incomplete earlier answer grant writes'; END IF;`, f.orgID, f.requestID, f.orgID, f.sessionID, f.requestID, f.orgID, f.sessionID, f.orgID, f.runID)
	if stage != "claim" {
		checks += fmt.Sprintf(`IF (SELECT status FROM sessions WHERE org_id='%s' AND id='%s') <> 'running' OR (SELECT status FROM session_threads WHERE org_id='%s' AND id='%s') <> 'running' THEN RAISE EXCEPTION 'incomplete earlier claims'; END IF;`, f.orgID, f.sessionID, f.orgID, f.threadID)
	}
	if stage == "enqueue" || stage == "commit" {
		checks += fmt.Sprintf(`IF (SELECT count(*) FROM session_messages WHERE org_id='%s' AND session_id='%s') <> 1 THEN RAISE EXCEPTION 'missing earlier answer message'; END IF;`, f.orgID, f.sessionID)
	}
	ending := "RETURN NULL;"
	if stage == "enqueue" || stage == "commit" {
		ending = "RAISE EXCEPTION 'synthetic answer grant " + stage + " failure';"
	}
	ddl := "CREATE FUNCTION " + name + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN " + checks + ending + " END $$; "
	switch stage {
	case "claim":
		ddl += "CREATE TRIGGER " + name + " BEFORE UPDATE OF status ON sessions FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid AND NEW.status='running') EXECUTE FUNCTION " + name + "()"
	case "message":
		ddl += "CREATE TRIGGER " + name + " BEFORE INSERT ON session_messages FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	case "enqueue":
		ddl += "CREATE TRIGGER " + name + " BEFORE INSERT ON jobs FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	case "commit":
		ddl += "CREATE CONSTRAINT TRIGGER " + name + " AFTER INSERT ON jobs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.org_id='" + f.orgID.String() + "'::uuid) EXECUTE FUNCTION " + name + "()"
	}
	_, err := pool.Exec(ctx, ddl)
	require.NoError(t, err, "install only a tenant-scoped synthetic failure inside the owned disposable full-schema database")
}

func newAnswerGrantPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema structured-answer grant atomicity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the supplied disposable database server without exposing connection data")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(ctx), "close this fixture's administrator connection")
	})
	var version int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version), "read the full-schema PostgreSQL version prerequisite")
	require.GreaterOrEqual(t, version, 150000, "the unchanged complete migration chain requires PostgreSQL 15 or newer")
	name := "human_input_grant_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this fixture's generated database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err, "drop the exact generated fixture after owned connections close, without force")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the fixture URI without exposing its private details")
	query := target.Query()
	query.Del("database")
	query.Del("dbname")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	isolated := target.String()
	config, err := pgxpool.ParseConfig(isolated)
	require.True(t, err == nil && config.ConnConfig.Database == name, "all real test connections must target only the generated fixture")
	config.MaxConns, config.MinConns = 2, 0
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve the complete unmodified schema migration chain")
	migration, err := migrate.New("file://"+source, isolated)
	require.True(t, err == nil, "open the real migration driver on only this fixture")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close the migration source")
		require.NoError(t, databaseErr, "close the migration driver before database cleanup")
	})
	require.NoError(t, migration.Up(), "apply the full unchanged production schema before actual Answer tests")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.True(t, err == nil, "open the bounded fully migrated fixture pool without exposing private details")
	t.Cleanup(pool.Close)
	return pool
}
