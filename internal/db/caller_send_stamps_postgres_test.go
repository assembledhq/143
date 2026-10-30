package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// One root owns one disposable, fully migrated database. Subcases intentionally
// run sequentially: the control singleton is database-wide, and the lock proof
// uses all three bounded pool checkouts. These tests exercise correlation only;
// they do not model a complete caller send, ambiguous commit, or admission.
//
//nolint:paralleltest // Subcases share the control singleton and all three bounded pool checkouts.
func TestCallerSendStampStoragePostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
	require.Equal(t, int32(3), pool.Config().MaxConns, "the full-schema fixture must retain a strict three-connection budget")
	var version int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version), "read the actual disposable PostgreSQL version")
	require.GreaterOrEqual(t, version, 150000, "the full-schema storage proof requires PostgreSQL 15 or newer")
	var ready, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "the installed highest migration must expose its real control singleton")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "passive schema installation must not enable caller-send intents")
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "enable retained writes only in this isolated synthetic test database")
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgxpool.Pool, CallerSendStampPurpose)
	}{
		{name: "exact linkage and conflict idempotence", run: callerSendStampPGExact},
		{name: "missing and forged typed linkage", run: callerSendStampPGInvalidLinkage},
		{name: "retained stamps cannot hide removed linkage", run: callerSendStampPGRetainedMissingLinkage},
		{name: "wrong digest preserves trigger error", run: callerSendStampPGWrongDigest},
		{name: "independently scoped observations", run: callerSendStampPGReadScopes},
		{name: "caller owns whole rollback", run: callerSendStampPGRollback},
		{name: "read-only observation does not lock owners", run: callerSendStampPGReadOnly},
		{name: "disabled control retains read-only observation", run: callerSendStampPGDisabledRead},
	}
	for _, purpose := range []CallerSendStampPurpose{CallerSendStampCodeReviewDispatch, CallerSendStampReviewLoopStartup} {
		t.Run(string(purpose), func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					caseCtx, caseCancel := context.WithTimeout(ctx, 30*time.Second)
					defer caseCancel()
					tt.run(t, caseCtx, pool, purpose)
				})
			}
		})
	}
}

func callerSendStampPGBegin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, readOnly bool) pgx.Tx {
	t.Helper()
	options := pgx.TxOptions{}
	if readOnly {
		options.AccessMode = pgx.ReadOnly
	}
	tx, err := pool.BeginTx(ctx, options)
	require.NoError(t, err, "the test caller must own its explicit outer transaction")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := tx.Rollback(cleanupCtx)
		require.True(t, err == nil || errors.Is(err, pgx.ErrTxClosed), "release only this case's outer transaction without deleting retained rows")
	})
	return tx
}

func callerSendStampPGSession(t *testing.T, ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	if orgID == uuid.Nil {
		orgID = uuid.New()
		_, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic stamp storage tenant')`, orgID)
		require.NoError(t, err, "seed a synthetic tenant with no deployment or customer data")
	}
	session := models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType,
		Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy,
		TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual,
		InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(tx).CreateInTx(ctx, tx, &session), "use the real session writer and its primary-thread creation path")
	require.NotNil(t, session.PrimaryThreadID, "the actual session writer must retain an explicit primary-thread identity")
	return orgID, session.ID, *session.PrimaryThreadID
}

func callerSendStampPGOwner(t *testing.T, ctx context.Context, tx pgx.Tx, purpose CallerSendStampPurpose) (uuid.UUID, CallerSendStampInput) {
	t.Helper()
	orgID, sessionID, threadID := callerSendStampPGSession(t, ctx, tx, uuid.Nil)
	ownerID, passID := uuid.New(), uuid.New()
	key, err := CallerSendStampKey(purpose, ownerID)
	require.NoError(t, err, "the typed owner must retain its canonical purpose-specific original key")
	input := CallerSendStampInput{Scope: CallerSendStampScope{SessionID: sessionID, ThreadID: threadID,
		ClientMessageID: key, Purpose: purpose, OwnerID: ownerID}, Version: CallerSendStampVersion, Digest: strings.Repeat("a", 64)}
	// This minimal JSON satisfies the actual SQL scope-shape contract. It is
	// deliberately not a full semantic codec envelope or startup API proof.
	command, err := json.Marshal(map[string]any{"version": 1, "purpose": purpose,
		"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "client_message_id": key})
	require.NoError(t, err, "encode only reusable synthetic owner scope for the schema fixture")
	if purpose == CallerSendStampCodeReviewDispatch {
		wake, marshalErr := json.Marshal(map[string]any{"job_type": "run_code_review", "queue": "agent",
			"dedupe_key": "synthetic-stamp:" + ownerID.String(), "payload": map[string]any{"org_id": orgID, "session_id": sessionID}})
		require.NoError(t, marshalErr, "encode a finite synthetic wake shape without invoking a scheduler")
		_, err = tx.Exec(ctx, `INSERT INTO code_review_agent_results (
 id,org_id,session_id,agent_provider,role,status,dispatch_version,dispatch_key,dispatch_input,
 dispatch_digest,dispatch_thread_id,dispatch_state,dispatch_attempt_seq,dispatch_due_at,
 dispatch_writer_seq,dispatch_updated_at,dispatch_wake
) VALUES($1,$2,$3,$4,'reviewer','queued',1,$5,$6::jsonb,$7,$8,'pending',0,statement_timestamp(),0,statement_timestamp(),$9::jsonb)`,
			ownerID, orgID, sessionID, string(models.DefaultDefaultAgentType), key, string(command), input.Digest, threadID, string(wake))
		require.NoError(t, err, "seed a real typed reviewer-dispatch owner in the exact retained scope")
		return orgID, input
	}
	require.Equal(t, CallerSendStampReviewLoopStartup, purpose, "the fixture must not infer an arbitrary owner namespace")
	_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`)
	require.NoError(t, err, "the fixture caller explicitly owns the deferred startup loop/pass creation cycle")
	_, err = tx.Exec(ctx, `INSERT INTO review_loop_start_intents (
 id,org_id,session_id,caller_kind,envelope,envelope_digest,pass_id,thread_id,startup_input,
 startup_digest,state,due_at,created_at,updated_at
) VALUES($1,$2,$3,'manual','{}'::jsonb,$4,$5,$6,$7::jsonb,$4,'prepared',statement_timestamp(),statement_timestamp(),statement_timestamp())`,
		ownerID, orgID, sessionID, input.Digest, passID, threadID, string(command))
	require.NoError(t, err, "a startup owner must originate from an exact retained manual creation record")
	_, err = tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3 AND state='prepared'`, orgID, sessionID, ownerID)
	require.NoError(t, err, "prepare only the fixture's known-created startup owner without a service call")
	_, err = tx.Exec(ctx, `INSERT INTO session_review_loops (
 id,org_id,session_id,thread_id,status,source,agent_type,max_passes,startup_send_version,
 startup_send_key,startup_send_input,startup_send_digest,startup_send_thread_id,startup_send_state,
 startup_send_attempt_seq,startup_send_due_at,startup_send_writer_seq,startup_send_updated_at,startup_send_pass_id
) VALUES($1,$2,$3,$4,'running','manual',$5,1,1,$6,$7::jsonb,$8,$4,'pending',0,statement_timestamp(),0,statement_timestamp(),$9)`,
		ownerID, orgID, sessionID, threadID, string(models.DefaultDefaultAgentType), key, string(command), input.Digest, passID)
	require.NoError(t, err, "seed a real startup owner with matching retained first-pass and thread identities")
	_, err = tx.Exec(ctx, `INSERT INTO session_review_loop_passes(id,org_id,session_id,loop_id,pass_index,status) VALUES($1,$2,$3,$4,1,'reviewing')`, passID, orgID, sessionID, ownerID)
	require.NoError(t, err, "seed the exact reverse-scoped first pass rather than a fabricated pointer")
	_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	require.NoError(t, err, "prove both sides of the actual deferred scope constraints before stamp work")
	return orgID, input
}

func callerSendStampPGMessage(t *testing.T, ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) int64 {
	t.Helper()
	var messageID int64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO session_messages(org_id,session_id,thread_id,turn_number,role,content)
 VALUES($1,$2,$3,1,'user','Synthetic immutable caller command') RETURNING id`, orgID, sessionID, threadID).Scan(&messageID), "create an actual partitioned message with explicit tenant/session/thread scope")
	return messageID
}

func callerSendStampPGInbox(t *testing.T, ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, key string, typedMessageID any, payloadMessageID int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"message_id": payloadMessageID})
	require.NoError(t, err, "encode a synthetic payload whose contents cannot replace typed linkage")
	_, err = tx.Exec(ctx, `INSERT INTO thread_inbox_entries(org_id,session_id,thread_id,sequence_no,message_id,client_message_id,entry_type,payload)
 VALUES($1,$2,$3,1,$4,$5,'user_message',$6::jsonb)`, orgID, sessionID, threadID, typedMessageID, key, string(payload))
	require.NoError(t, err, "seed an actual typed inbox row without routing, enqueueing or granting admission")
}

func callerSendStampPGLink(t *testing.T, ctx context.Context, tx pgx.Tx, orgID uuid.UUID, input *CallerSendStampInput) {
	t.Helper()
	input.MessageID = callerSendStampPGMessage(t, ctx, tx, orgID, input.Scope.SessionID, input.Scope.ThreadID)
	callerSendStampPGInbox(t, ctx, tx, orgID, input.Scope.SessionID, input.Scope.ThreadID, input.Scope.ClientMessageID, input.MessageID, input.MessageID)
}

func callerSendStampPGExact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
	callerSendStampPGLink(t, ctx, tx, orgID, &input)
	var expectedTime time.Time
	var transactionTime, beforeInsertTime time.Time
	require.NoError(t, tx.QueryRow(ctx, `SELECT transaction_timestamp(),statement_timestamp()`).Scan(&transactionTime, &beforeInsertTime), "retain independent database clock bounds before the insertion statement")
	// A caller cannot choose CreatedAt; the returned value must be the actual
	// insertion statement's PostgreSQL timestamp, not the transaction start.
	actual, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "real typed message/inbox linkage should allow one exact immutable stamp")
	require.True(t, actual.CreatedAt.After(transactionTime), "the stamp must use its later insertion statement rather than the outer transaction's start timestamp")
	require.False(t, actual.CreatedAt.Before(beforeInsertTime), "the inserted timestamp must not precede the independently observed pre-insertion statement")
	require.NoError(t, tx.QueryRow(ctx, `SELECT created_at FROM caller_send_command_stamps WHERE org_id=$1 AND thread_id=$2 AND client_message_id=$3`, orgID, input.Scope.ThreadID, input.Scope.ClientMessageID).Scan(&expectedTime), "read back the database-owned insertion timestamp")
	expected := CallerSendStamp{OrgID: orgID, CallerSendStampInput: input, CreatedAt: expectedTime}
	require.Equal(t, expected, actual, "Record must preserve all exact typed fields and its database timestamp")
	duplicate, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "the same exact conflict should be idempotent without changing the immutable stamp")
	require.Equal(t, expected, duplicate, "conflict recovery must return the original timestamp rather than a replacement")
	observed, found, err := ReadCallerSendStampInTx(ctx, orgID, tx, input.Scope)
	require.NoError(t, err, "the supplied outer transaction should observe its own exact stamp")
	require.True(t, found, "positive exact correlation must remain distinguishable from absence")
	require.Equal(t, expected, observed, "the reader must retain the complete exact database record")
	var count int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1`, orgID).Scan(&count), "read the synthetic tenant's actual retained stamp count")
	require.Equal(t, 1, count, "idempotent conflict handling must not create another message binding")
}

func callerSendStampPGInvalidLinkage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tests := []struct{ name string }{
		{"missing message"}, {"missing inbox"}, {"missing typed message pointer"}, {"wrong typed message pointer"},
		{"wrong inbox key"}, {"message in another lane"}, {"message in another session"}, {"message in another tenant"}, {"inbox in another session"}, {"inbox in another tenant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential cases own rollback-isolated fixtures and at most one
			// pool checkout; they must not contend with the global control case.
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
			messageOrg, messageSession, messageThread := orgID, input.Scope.SessionID, input.Scope.ThreadID
			if tt.name == "message in another lane" {
				lane := models.SessionThread{OrgID: orgID, SessionID: input.Scope.SessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic stamp sibling", Status: models.ThreadStatusIdle}
				require.NoError(t, NewSessionThreadStore(tx).Create(ctx, &lane, 10), "seed an actual same-session sibling instead of an invalid thread fixture")
				messageThread = lane.ID
			}
			if tt.name == "message in another session" {
				messageOrg, messageSession, messageThread = callerSendStampPGSession(t, ctx, tx, orgID)
			}
			if tt.name == "message in another tenant" {
				messageOrg, messageSession, messageThread = callerSendStampPGSession(t, ctx, tx, uuid.Nil)
			}
			input.MessageID = 9223372036854775807
			if tt.name != "missing message" {
				input.MessageID = callerSendStampPGMessage(t, ctx, tx, messageOrg, messageSession, messageThread)
			}
			inboxOrg, inboxSession, inboxThread := orgID, input.Scope.SessionID, input.Scope.ThreadID
			if tt.name == "inbox in another session" {
				inboxOrg, inboxSession, inboxThread = callerSendStampPGSession(t, ctx, tx, orgID)
			}
			if tt.name == "inbox in another tenant" {
				inboxOrg, inboxSession, inboxThread = callerSendStampPGSession(t, ctx, tx, uuid.Nil)
			}
			var typedMessageID any = input.MessageID
			key := input.Scope.ClientMessageID
			switch tt.name {
			case "missing typed message pointer":
				typedMessageID = nil
			case "wrong typed message pointer":
				typedMessageID = callerSendStampPGMessage(t, ctx, tx, orgID, input.Scope.SessionID, input.Scope.ThreadID)
			case "wrong inbox key":
				key = "synthetic-inbox-key-mismatch"
			}
			if tt.name != "missing inbox" {
				callerSendStampPGInbox(t, ctx, tx, inboxOrg, inboxSession, inboxThread, key, typedMessageID, input.MessageID)
			}
			actual, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
			require.ErrorIs(t, err, ErrCallerSendStampMismatch, "a missing or independently foreign typed link cannot be replaced by matching payload JSON")
			require.Equal(t, CallerSendStamp{}, actual, "failed linkage must yield no usable stamp or authority")
			var count int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1`, orgID).Scan(&count), "a known helper mismatch must leave settlement to the still-open outer transaction")
			require.Equal(t, 0, count, "invalid typed linkage must never insert an immutable stamp")
		})
	}
}

func callerSendStampPGRetainedMissingLinkage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tests := []struct{ name string }{{"message removed"}, {"inbox removed"}, {"typed pointer replaced"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The retained stamp is deliberately not deleted or rewritten. Only
			// exact synthetic linkage changes inside this rollback-owned case.
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
			callerSendStampPGLink(t, ctx, tx, orgID, &input)
			stamp, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
			require.NoError(t, err, "retain the original exact stamp before changing only its synthetic typed linkage")
			switch tt.name {
			case "message removed":
				_, err = tx.Exec(ctx, `DELETE FROM session_messages WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, orgID, input.Scope.SessionID, input.Scope.ThreadID, input.MessageID)
			case "inbox removed":
				_, err = tx.Exec(ctx, `DELETE FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4`, orgID, input.Scope.SessionID, input.Scope.ThreadID, input.Scope.ClientMessageID)
			case "typed pointer replaced":
				otherMessageID := callerSendStampPGMessage(t, ctx, tx, orgID, input.Scope.SessionID, input.Scope.ThreadID)
				_, err = tx.Exec(ctx, `UPDATE thread_inbox_entries SET message_id=$5 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4`, orgID, input.Scope.SessionID, input.Scope.ThreadID, input.Scope.ClientMessageID, otherMessageID)
			}
			require.NoError(t, err, "change only exact rollback-owned synthetic linkage without touching retained immutable evidence")
			actual, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
			require.ErrorIs(t, err, ErrCallerSendStampMismatch, "an existing matching stamp cannot mask absent or different required typed linkage")
			require.Equal(t, CallerSendStamp{}, actual, "failed conflict correlation must not return acceptance or permission to proceed")
			observed, found, err := ReadCallerSendStampInTx(ctx, orgID, tx, input.Scope)
			require.NoError(t, err, "plain observation must still retain the original immutable correlation without interpreting it as acceptance")
			require.True(t, found, "negative linkage evidence must not erase the original stamp")
			require.Equal(t, stamp, observed, "the retained original record must remain byte-for-field identical")
		})
	}
}

func callerSendStampPGWrongDigest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
	callerSendStampPGLink(t, ctx, tx, orgID, &input)
	_, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "retain the exact original stamp before probing a different canonical digest")
	input.Digest = strings.Repeat("b", 64)
	actual, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	// BEFORE INSERT validates the typed owner even on ON CONFLICT DO NOTHING;
	// the actual parent trigger refuses this before a conflict read can run.
	require.ErrorIs(t, err, ErrCallerSendStampStorage, "the actual pre-conflict trigger refusal must retain the helper's safe storage category")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the owning unit must retain the PostgreSQL cause rather than reinterpret the outcome")
	require.Equal(t, "23503", pgErr.Code, "a different immutable owner digest must fail the actual typed-scope trigger")
	require.EqualError(t, err, "caller send stamp storage failed", "rendered errors must not publish the database's values or query details")
	require.Equal(t, CallerSendStamp{}, actual, "a refused insert must expose no partial replacement stamp")
	require.NoError(t, tx.Rollback(ctx), "the caller must roll back the entire refused unit itself")
}

func callerSendStampPGReadScopes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
	callerSendStampPGLink(t, ctx, tx, orgID, &input)
	_, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "retain a real stamp before testing independently supplied observation scope")
	tests := []struct{ name string }{{"foreign organization"}, {"different session"}, {"different lane"}, {"different owner and key"}, {"different purpose and key"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One explicitly owned transaction is reused only sequentially.
			readOrg, scope := orgID, input.Scope
			switch tt.name {
			case "foreign organization":
				readOrg = uuid.New()
			case "different session":
				scope.SessionID = uuid.New()
			case "different lane":
				scope.ThreadID = uuid.New()
			case "different owner and key":
				scope.OwnerID = uuid.New()
				scope.ClientMessageID, err = CallerSendStampKey(scope.Purpose, scope.OwnerID)
			case "different purpose and key":
				scope.Purpose = CallerSendStampReviewLoopStartup
				if purpose == CallerSendStampReviewLoopStartup {
					scope.Purpose = CallerSendStampCodeReviewDispatch
				}
				scope.ClientMessageID, err = CallerSendStampKey(scope.Purpose, scope.OwnerID)
			}
			require.NoError(t, err, "negative read scopes must remain syntactically valid independent expectations")
			actual, found, readErr := ReadCallerSendStampInTx(ctx, readOrg, tx, scope)
			require.NoError(t, readErr, "a missing exact scoped record is only an observation miss")
			require.False(t, found, "tenant, session, lane, key, purpose and owner must all bind the read independently")
			require.Equal(t, CallerSendStamp{}, actual, "scope misses must not expose another owner's stamp or grant abort/retry permission")
		})
	}
}

func callerSendStampPGRollback(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
	callerSendStampPGLink(t, ctx, tx, orgID, &input)
	_, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "record correlation within the caller-owned message/inbox transaction")
	require.NoError(t, tx.Rollback(ctx), "only the caller decides to roll back the complete synthetic unit")
	var actual [3]int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1),
 (SELECT count(*) FROM session_messages WHERE org_id=$1),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1)`, orgID).Scan(&actual[0], &actual[1], &actual[2]), "a separate checkout must observe the actual database state after whole-unit rollback")
	require.Equal(t, [3]int{0, 0, 0}, actual, "stamp helpers must not independently commit stamps, messages or inbox entries")
}

func callerSendStampPGCommitted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) CallerSendStamp {
	t.Helper()
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	orgID, input := callerSendStampPGOwner(t, ctx, tx, purpose)
	callerSendStampPGLink(t, ctx, tx, orgID, &input)
	stamp, err := RecordCallerSendStampInTx(ctx, orgID, tx, input)
	require.NoError(t, err, "retain a real stamp before independent read-only composition")
	require.NoError(t, tx.Commit(ctx), "only the fixture caller may commit its complete synthetic scope")
	return stamp
}

func callerSendStampPGReadOnly(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	stamp := callerSendStampPGCommitted(t, ctx, pool, purpose)
	holder := callerSendStampPGBegin(t, ctx, pool, false)
	table := "code_review_agent_results"
	if purpose == CallerSendStampReviewLoopStartup {
		table = "session_review_loops"
	}
	var lockedID uuid.UUID
	ownerQuery := `SELECT id FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`
	require.NoError(t, holder.QueryRow(ctx, ownerQuery, stamp.OrgID, stamp.Scope.SessionID, stamp.Scope.OwnerID).Scan(&lockedID), "hold the actual typed owner row before starting an independent observer")
	require.Equal(t, stamp.Scope.OwnerID, lockedID, "the blocker must hold this exact retained owner rather than an unrelated row")
	reader := callerSendStampPGBegin(t, ctx, pool, true)
	var readOnly string
	require.NoError(t, reader.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly), "verify PostgreSQL is enforcing the observer's read-only transaction")
	require.Equal(t, "on", readOnly, "a helper that adds row locks or writes must fail this real read-only composition")
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	actual, found, err := ReadCallerSendStampInTx(readCtx, stamp.OrgID, reader, stamp.Scope)
	require.NoError(t, err, "a plain stamp observation must succeed while its owner row remains exclusively locked")
	require.True(t, found, "an owner lock cannot hide existing immutable correlation")
	require.Equal(t, stamp, actual, "read-only observation must preserve the exact original persisted record")
	probe := callerSendStampPGBegin(t, ctx, pool, false)
	err = probe.QueryRow(ctx, ownerQuery+` NOWAIT`, stamp.OrgID, stamp.Scope.SessionID, stamp.Scope.OwnerID).Scan(&lockedID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "an independent NOWAIT probe must positively prove the original owner lock is still held")
	require.Equal(t, "55P03", pgErr.Code, "successful stamp observation must not have depended on owner-lock release")
	require.NoError(t, probe.Rollback(ctx), "release the refused probe without cancelling or settling the holder")
	require.NoError(t, reader.Rollback(ctx), "the read-only observer owns its own completion")
	require.NoError(t, holder.Rollback(ctx), "release only the exact synthetic holder after the positive proof")
}

func callerSendStampPGDisabledRead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose CallerSendStampPurpose) {
	stamp := callerSendStampPGCommitted(t, ctx, pool, purpose)
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton AND schema_ready`)
	require.NoError(t, err, "disable only this isolated database after the original correlation is committed")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, restoreErr := pool.Exec(cleanupCtx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
		require.NoError(t, restoreErr, "restore only this fixture's control flag before the next sequential synthetic case")
	})
	reader := callerSendStampPGBegin(t, ctx, pool, true)
	actual, found, err := ReadCallerSendStampInTx(ctx, stamp.OrgID, reader, stamp.Scope)
	require.NoError(t, err, "disabled insertion control must not prevent a plain retained-stamp observation")
	require.True(t, found, "the observer must retain positive immutable evidence without attempting a fresh Record")
	require.Equal(t, stamp, actual, "disabled-control observation must return the original full record unchanged")
	require.NoError(t, reader.Rollback(ctx), "finish read-only observation before probing the separate insertion gate")
	writer := callerSendStampPGBegin(t, ctx, pool, false)
	actual, err = RecordCallerSendStampInTx(ctx, stamp.OrgID, writer, stamp.CallerSendStampInput)
	require.ErrorIs(t, err, ErrCallerSendStampStorage, "even an otherwise exact conflict cannot bypass the actual BEFORE INSERT control gate")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the refused writer must retain the actual disabled-control database cause")
	require.Equal(t, "55000", pgErr.Code, "disabled control must be a real insertion refusal, not an absent stamp or retry instruction")
	require.Equal(t, CallerSendStamp{}, actual, "a refused write must not expose a new stamp or admission outcome")
	require.NoError(t, writer.Rollback(ctx), "the caller must roll back its own refused write unit")
}
