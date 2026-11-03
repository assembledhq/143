package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// One root owns one fully migrated, isolated database. Its subcases deliberately
// run sequentially because control flags, migration DDL and TRUNCATE guards are
// database-wide. Each ordinary case rolls back its complete fixture; NewPool's
// cleanup drops the isolated database rather than bypassing retention guards.
func TestCallerSendIntentSchemaPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
	callerDispatchStatusPinOriginal389(t, ctx, pool)
	var ready, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "the highest migration must install a readable readiness singleton")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "schema readiness must not enable caller-send intents")
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "only this isolated fixture explicitly enables retained caller-send writes")
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgxpool.Pool)
	}{
		{name: "owner state and NULL truth tables", run: callerSendSchemaOwnerShapes},
		{name: "owner transitions and attempt fencing", run: callerSendSchemaOwnerTransitions},
		{name: "observation history cannot regress", run: callerSendSchemaObservationHistory},
		{name: "pointer conflict observation history cannot regress", run: callerSendSchemaPointerConflictObservationHistory},
		{name: "review requirement cancellation compatibility", run: callerSendSchemaCancellationCompatibility},
		{name: "legacy and ancillary writers", run: callerSendSchemaLegacyWriters},
		{name: "stamp purpose digest and scope", run: callerSendSchemaStamps},
		{name: "first pass exact scope and retention", run: callerSendSchemaPassBinding},
		{name: "retained creation and caller bindings", run: callerSendSchemaStarts},
		{name: "readiness disabled and rollback guards", run: callerSendSchemaControl},
		{name: "actual JSONB storage bounds", run: callerSendSchemaJSONBBounds},
		{name: "empty real up down up", run: callerSendSchemaMigrationRoundTrip},
	}
	for _, tt := range tests {
		if !t.Run(tt.name, func(t *testing.T) { tt.run(t, ctx, pool) }) {
			return
		}
	}
}

type callerSendSchemaOwner struct {
	name, table, prefix, keyPrefix string
	loop                           bool
}

func callerSendSchemaOwners() []callerSendSchemaOwner {
	return []callerSendSchemaOwner{
		{name: "reviewer dispatch", table: "code_review_agent_results", prefix: "dispatch_", keyPrefix: "cr-dispatch:v1:"},
		{name: "loop startup", table: "session_review_loops", prefix: "startup_send_", keyPrefix: "rl-startup:v1:", loop: true},
	}
}

func callerSendSchemaFields(owner callerSendSchemaOwner) []string {
	fields := []string{"version", "key", "input", "digest", "thread_id", "state", "attempt_id", "attempt_seq", "attempted_at", "due_at", "observe_deadline", "attention_at", "observed_at", "message_id", "resolution", "detail", "writer_seq", "updated_at"}
	if owner.loop {
		return append(fields, "pass_id")
	}
	return append(fields, "wake")
}

type callerSendSchemaFixture struct {
	org, session, thread, owner, pass uuid.UUID
	at                                time.Time
}

func callerSendSchemaBegin(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "own an isolated fixture transaction before making retained synthetic rows")
	t.Cleanup(func() {
		err := tx.Rollback(context.Background())
		require.True(t, err == nil || errors.Is(err, pgx.ErrTxClosed), "discard this case without deleting or truncating retained evidence")
	})
	return tx
}

func callerSendSchemaSeed(t *testing.T, ctx context.Context, tx pgx.Tx) callerSendSchemaFixture {
	t.Helper()
	f := callerSendSchemaFixture{org: uuid.New(), owner: uuid.New(), pass: uuid.New(), at: time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)}
	_, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic caller schema tenant')`, f.org)
	require.NoError(t, err, "create a synthetic tenant without deployment or customer data")
	session := models.Session{OrgID: f.org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(tx).CreateInTx(ctx, tx, &session), "use the real current session writer and its owned primary-thread fixture")
	require.NotNil(t, session.PrimaryThreadID, "a real session fixture must retain an exact thread identity")
	f.session, f.thread = session.ID, *session.PrimaryThreadID
	return f
}

func callerSendSchemaShape(owner callerSendSchemaOwner, f callerSendSchemaFixture, state string) map[string]any {
	row := map[string]any{
		"version": int16(1), "key": owner.keyPrefix + f.owner.String(), "input": callerSendSchemaInput(owner, f), "digest": strings.Repeat("a", 64),
		"thread_id": f.thread, "state": state, "attempt_seq": int32(1), "attempt_id": uuid.New(), "attempted_at": f.at,
		"due_at": f.at.Add(5 * time.Second), "writer_seq": int32(0), "updated_at": f.at,
	}
	if owner.loop {
		row["pass_id"] = f.pass
	} else {
		row["wake"] = fmt.Sprintf(`{"job_type":"run_code_review","queue":"agent","dedupe_key":"synthetic:%s","payload":{"org_id":"%s","session_id":"%s"}}`, f.owner, f.org, f.session)
	}
	switch state {
	case "pending":
		row["attempt_seq"], row["attempt_id"], row["attempted_at"] = int32(0), nil, nil
	case "retry-pending":
		row["state"], row["detail"] = "pending", "contention"
	case "unknown":
		row["detail"], row["observe_deadline"] = "unknown_ack", f.at.Add(15*time.Minute)
	case "attention":
		row["detail"], row["observe_deadline"], row["attention_at"] = "unknown_ack", f.at.Add(15*time.Minute), f.at.Add(15*time.Minute)
	case "accepted", "observed-auto", "observed-manual":
		row["state"], row["due_at"], row["message_id"], row["resolution"] = "accepted", nil, int64(123), "send_ack"
		if state != "accepted" {
			row["observed_at"] = f.at.Add(time.Minute)
			row["resolution"] = map[string]string{"observed-auto": "observed_auto", "observed-manual": "observed_manual"}[state]
		}
	case "refused":
		row["due_at"], row["detail"], row["resolution"] = nil, "stamped_active_unsupported", "send_refusal"
	case "cancelled", "cancelled-after-abort":
		row["state"], row["due_at"], row["resolution"] = "cancelled", nil, "owner_cancelled"
		if state == "cancelled" {
			row["attempt_seq"], row["attempt_id"], row["attempted_at"] = int32(0), nil, nil
		}
	}
	return row
}

func callerSendSchemaInput(owner callerSendSchemaOwner, f callerSendSchemaFixture) string {
	// This is a minimal SQL scope-shape fixture, not a full codec envelope or
	// proof that the codec accepts a semantically complete caller command.
	purpose := "code-review-reviewer-dispatch"
	if owner.loop {
		purpose = "review-loop-initial-send"
	}
	return fmt.Sprintf(`{"version":1,"purpose":%q,"org_id":%q,"session_id":%q,"thread_id":%q,"client_message_id":%q}`, purpose, f.org.String(), f.session.String(), f.thread.String(), owner.keyPrefix+f.owner.String())
}

func callerSendSchemaInsertOwner(ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture, row map[string]any) error {
	if owner.loop && row["version"] != nil {
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_loop_start_intents WHERE org_id=$1 AND id=$2)`, f.org, f.owner).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			start := callerSendSchemaStartRow(f, "manual", uuid.Nil)
			start["startup_input"], start["startup_digest"] = row["input"], row["digest"]
			if err := callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", start); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3 AND state='prepared'`, f.org, f.session, f.owner); err != nil {
			return err
		}
	}
	columns, args := []string{"id", "org_id", "session_id"}, []any{f.owner, f.org, f.session}
	if owner.loop {
		status := "failed"
		if row["version"] != nil {
			status = "running"
		}
		columns, args = append(columns, "thread_id", "status", "source", "agent_type", "max_passes"), append(args, f.thread, status, "manual", string(models.DefaultDefaultAgentType), 1)
		if required, exists := row["review_required"]; exists {
			columns, args = append(columns, "review_required"), append(args, required)
		}
	} else {
		columns, args = append(columns, "agent_provider", "role", "status"), append(args, string(models.DefaultDefaultAgentType), "reviewer", "queued")
	}
	if row != nil {
		for _, field := range callerSendSchemaFields(owner) {
			columns, args = append(columns, owner.prefix+field), append(args, row[field])
		}
	}
	quoted, placeholders := make([]string, len(columns)), make([]string, len(columns))
	for i, column := range columns {
		quoted[i], placeholders[i] = pgx.Identifier{column}.Sanitize(), fmt.Sprintf("$%d", i+1)
	}
	query := `INSERT INTO ` + pgx.Identifier{owner.table}.Sanitize() + ` (` + strings.Join(quoted, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return err
	}
	if owner.loop {
		if _, err := tx.Exec(ctx, `INSERT INTO session_review_loop_passes(id,org_id,session_id,loop_id,pass_index,status) VALUES($1,$2,$3,$4,1,'reviewing')`, f.pass, f.org, f.session, f.owner); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	return err
}

func callerSendSchemaProbe(t *testing.T, ctx context.Context, tx pgx.Tx, operation func(pgx.Tx) error) error {
	t.Helper()
	probe, err := tx.Begin(ctx)
	require.NoError(t, err, "each negative probe owns a savepoint so one SQL refusal cannot hide later checks")
	err = operation(probe)
	require.NoError(t, probe.Rollback(ctx), "restore the exact fixture after a successful or refused probe")
	return err
}

func callerSendSchemaSQLState(t *testing.T, err error, code, reason string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, reason)
	// These fixtures contain only synthetic rows. Include bounded schema-level
	// diagnostics, never Detail (which can include complete row/key values).
	require.Equal(t, code, pgErr.Code, "%s (PostgreSQL constraint=%q message=%.512q)", reason, pgErr.ConstraintName, pgErr.Message)
}

func callerSendSchemaOwnerShapes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, owner := range callerSendSchemaOwners() {
		t.Run(owner.name, func(t *testing.T) {
			states := []string{"pending", "retry-pending", "attempting", "unknown", "attention", "accepted", "observed-auto", "observed-manual", "refused", "cancelled", "cancelled-after-abort"}
			for _, state := range states {
				t.Run(state, func(t *testing.T) {
					tx := callerSendSchemaBegin(t, ctx, pool)
					f := callerSendSchemaSeed(t, ctx, tx)
					row := callerSendSchemaShape(owner, f, state)
					callerSendSchemaReachOwner(t, ctx, tx, owner, f, state)
					require.True(t, callerSendSchemaCheck(t, ctx, tx, owner, f, row), "every complete retained state must satisfy the actual installed NULL-safe CHECK")
					for _, field := range callerSendSchemaFields(owner) {
						if row[field] == nil {
							continue
						}
						t.Run("NULL "+field, func(t *testing.T) {
							copyRow := make(map[string]any, len(row))
							for key, value := range row {
								copyRow[key] = value
							}
							copyRow[field] = nil
							require.False(t, callerSendSchemaCheck(t, ctx, tx, owner, f, copyRow), "an individually missing required value must fail rather than pass a NULL-valued CHECK")
						})
					}
				})
			}
			t.Run("all NULL legacy and partial legacy rows", func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				require.NoError(t, callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error { return callerSendSchemaInsertOwner(ctx, probe, owner, f, nil) }), "an all-NULL legacy row remains legal without inferring a protocol command")
				full := callerSendSchemaShape(owner, f, "attention")
				for _, field := range callerSendSchemaFields(owner) {
					t.Run(field+" alone", func(t *testing.T) {
						value := full[field]
						if value == nil {
							switch field {
							case "message_id":
								value = int64(123)
							case "resolution":
								value = "owner_cancelled"
							case "observed_at":
								value = f.at
							}
						}
						require.False(t, callerSendSchemaCheck(t, ctx, tx, owner, f, map[string]any{field: value}), "every single-column partial intent must fail the all-NULL legacy branch")
					})
				}
			})
			t.Run("invalid complete shapes", func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				tests := []struct {
					name, field string
					value       any
				}{
					{name: "zero digest", field: "digest", value: strings.Repeat("0", 64)},
					{name: "uppercase digest", field: "digest", value: strings.Repeat("A", 64)},
					{name: "short digest", field: "digest", value: strings.Repeat("a", 63)},
					{name: "long digest", field: "digest", value: strings.Repeat("a", 65)},
					{name: "whitespace digest", field: "digest", value: strings.Repeat("a", 63) + " "},
					{name: "nonhex digest", field: "digest", value: strings.Repeat("g", 64)},
					{name: "bytea spelling", field: "digest", value: `\x` + strings.Repeat("a", 64)},
					{name: "non ASCII digest", field: "digest", value: strings.Repeat("ａ", 64)},
					{name: "unsupported version", field: "version", value: int16(2)},
					{name: "wrong key", field: "key", value: "synthetic-wrong-key"},
					{name: "negative attempt", field: "attempt_seq", value: int32(-1)},
					{name: "negative writer", field: "writer_seq", value: int32(-1)},
					{name: "zero thread", field: "thread_id", value: uuid.Nil},
					{name: "unknown state", field: "state", value: "synthetic_unknown"},
				}
				for _, tt := range tests {
					t.Run(tt.name, func(t *testing.T) {
						row := callerSendSchemaShape(owner, f, "pending")
						row[tt.field] = tt.value
						require.False(t, callerSendSchemaCheck(t, ctx, tx, owner, f, row), "the actual installed owner CHECK must reject malformed complete protocol shapes without relying on another trigger")
					})
				}
				for _, tt := range []struct{ name, input string }{
					{name: "nonobject input", input: `[]`},
					{name: "missing input scope", input: `{}`},
				} {
					t.Run(tt.name, func(t *testing.T) {
						row := callerSendSchemaShape(owner, f, "pending")
						row["input"] = tt.input
						err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error { return callerSendSchemaInsertOwner(ctx, probe, owner, f, row) })
						callerSendSchemaSQLState(t, err, "23514", "expensive immutable payload checks run once at actual installation rather than on unrelated later owner updates")
					})
				}
			})
		})
	}
}

// The installed CHECK is tested independently from BEFORE triggers: otherwise
// an immutability/fresh-insert refusal can mask a nullable CHECK that is wrong.
// The expression comes from the actual migrated catalog, not a test copy of SQL.
func callerSendSchemaCheck(t *testing.T, ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture, row map[string]any) bool {
	t.Helper()
	constraint := "chk_cra_dispatch_send"
	if owner.loop {
		constraint = "chk_srl_startup_send"
	}
	var expression string
	require.NoError(t, tx.QueryRow(ctx, `SELECT pg_get_expr(conbin,conrelid) FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2 AND contype='c' AND convalidated`, owner.table, constraint).Scan(&expression), "inspect the exact validated owner CHECK from the real PostgreSQL catalog")
	values := map[string]any{"id": f.owner, "org_id": f.org, "session_id": f.session}
	if owner.loop {
		values["thread_id"] = f.thread
	} else {
		values["role"] = "reviewer"
	}
	for _, field := range callerSendSchemaFields(owner) {
		values[owner.prefix+field] = row[field]
	}
	for _, field := range []string{"input", "wake"} {
		if value, ok := values[owner.prefix+field].(string); ok {
			values[owner.prefix+field] = json.RawMessage(value)
		}
	}
	return callerSendSchemaConstraintCheck(t, ctx, tx, owner.table, expression, values)
}

func callerSendSchemaConstraintCheck(t *testing.T, ctx context.Context, tx pgx.Tx, table, expression string, values map[string]any) bool {
	t.Helper()
	payload, err := json.Marshal(values)
	require.NoError(t, err, "encode the complete synthetic candidate row without changing typed NULL presence")
	var accepted bool
	query := `SELECT (` + expression + `) IS TRUE FROM jsonb_populate_record(NULL::` + pgx.Identifier{table}.Sanitize() + `,$1::jsonb) candidate`
	require.NoError(t, tx.QueryRow(ctx, query, payload).Scan(&accepted), "evaluate the installed CHECK against actual PostgreSQL record types without a trigger masking its NULL behavior")
	return accepted
}

func callerSendSchemaReachOwner(t *testing.T, ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture, state string, reviewRequired ...bool) {
	t.Helper()
	row := callerSendSchemaShape(owner, f, "pending")
	if len(reviewRequired) > 0 {
		row["review_required"] = reviewRequired[0]
	}
	require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, row), "the only new retained owner shape is fresh pending")
	if state == "pending" {
		return
	}
	writer := int32(0)
	apply := func(next map[string]any) {
		writer++
		next["writer_seq"] = writer
		fields := []string{"state", "attempt_id", "attempt_seq", "attempted_at", "due_at", "observe_deadline", "attention_at", "observed_at", "message_id", "resolution", "detail", "writer_seq", "updated_at"}
		set, args := make([]string, len(fields)), make([]any, len(fields))
		for i, field := range fields {
			set[i], args[i] = owner.prefix+field+fmt.Sprintf("=$%d", i+4), next[field]
		}
		_, err := callerSendSchemaUpdate(ctx, tx, owner, f, strings.Join(set, ","), args...)
		require.NoError(t, err, "a complete legal transition must reach the requested state without bypassing its real trigger")
	}
	if state == "cancelled" {
		apply(callerSendSchemaShape(owner, f, state))
		return
	}
	attempt := callerSendSchemaShape(owner, f, "attempting")
	apply(attempt)
	if state == "attempting" {
		return
	}
	target := callerSendSchemaShape(owner, f, state)
	for _, field := range []string{"attempt_id", "attempt_seq", "attempted_at"} {
		target[field] = attempt[field]
	}
	if state == "attention" || state == "observed-auto" || state == "observed-manual" {
		unknown := callerSendSchemaShape(owner, f, "unknown")
		unknown["attempt_id"] = attempt["attempt_id"]
		apply(unknown)
		if state == "observed-auto" || state == "observed-manual" {
			var retainedDeadline time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT `+owner.prefix+`observe_deadline FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&retainedDeadline), "restore the actual original deadline before the positive observed-acceptance transition")
			target["observe_deadline"] = retainedDeadline
		}
	}
	if state == "cancelled-after-abort" {
		pending := callerSendSchemaShape(owner, f, "retry-pending")
		pending["attempt_id"] = attempt["attempt_id"]
		apply(pending)
	}
	apply(target)
	if state == "observed-auto" || state == "observed-manual" {
		var actualDeadline time.Time
		require.NoError(t, tx.QueryRow(ctx, `SELECT `+owner.prefix+`observe_deadline FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualDeadline), "positively read the original deadline after observed acceptance")
		require.Equal(t, target["observe_deadline"].(time.Time).UTC(), actualDeadline.UTC(), "the accepted fixture must retain its existing observation deadline rather than reset history from a fresh shape")
	}
}

func callerSendSchemaSnapshot(t *testing.T, ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture) json.RawMessage {
	t.Helper()
	var row json.RawMessage
	err := tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM `+pgx.Identifier{owner.table}.Sanitize()+` r WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&row)
	require.NoError(t, err, "read every retained owner and ancillary field before comparing a refused mutation")
	return row
}

func callerSendSchemaUpdate(ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture, set string, args ...any) (pgconn.CommandTag, error) {
	return tx.Exec(ctx, `UPDATE `+pgx.Identifier{owner.table}.Sanitize()+` SET `+set+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, append([]any{f.org, f.session, f.owner}, args...)...)
}

func callerSendSchemaOwnerTransitions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, owner := range callerSendSchemaOwners() {
		t.Run(owner.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, callerSendSchemaShape(owner, f, "pending")), "insert a complete fresh pending command before exercising its transition graph")
			p := owner.prefix
			first, second := uuid.New(), uuid.New()
			_, err := callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='attempting',`+p+`attempt_seq=1,`+p+`attempt_id=$4,`+p+`attempted_at=$5,`+p+`due_at=$6,`+p+`writer_seq=1`, first, f.at, f.at.Add(time.Minute))
			require.NoError(t, err, "a first claimed attempt must advance the sequence and retain its exact identity")
			_, err = callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='pending',`+p+`detail='contention',`+p+`due_at=$4,`+p+`writer_seq=2`, f.at.Add(5*time.Second))
			require.NoError(t, err, "a positively known abort may return to pending without clearing attempt history")
			_, err = callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='attempting',`+p+`attempt_seq=2,`+p+`attempt_id=$4,`+p+`attempted_at=$5,`+p+`due_at=$6,`+p+`detail=NULL,`+p+`writer_seq=3`, second, f.at.Add(time.Second), f.at.Add(time.Minute))
			require.NoError(t, err, "a retry must install a different attempt and monotonically newer timestamp")
			before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			tag, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{owner.table}.Sanitize()+` SET `+p+`state='unknown',`+p+`detail='unknown_ack',`+p+`observe_deadline=$5,`+p+`writer_seq=`+p+`writer_seq+1 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND `+p+`attempt_id=$4 AND `+p+`writer_seq=3`, f.org, f.session, f.owner, first, f.at.Add(15*time.Minute))
			require.NoError(t, err, "stale attempt fencing must be a normal zero-row CAS rather than a replacement claim")
			require.Equal(t, int64(0), tag.RowsAffected(), "the first attempt cannot settle the newer attempt")
			require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "a stale CAS cannot change owner status or any retained command evidence")
			mutations := []struct {
				name, set string
				args      []any
			}{
				{name: "clear complete intent", set: callerSendSchemaClear(owner)},
				{name: "replace digest", set: p + `digest=repeat('b',64),` + p + `writer_seq=4`},
				{name: "replace input", set: p + `input='{"different":true}',` + p + `writer_seq=4`},
				{name: "replace key", set: p + `key='synthetic-other',` + p + `writer_seq=4`},
				{name: "clear attempt history", set: p + `state='pending',` + p + `attempt_seq=0,` + p + `attempt_id=NULL,` + p + `attempted_at=NULL,` + p + `writer_seq=4`},
				{name: "skip writer sequence", set: p + `writer_seq=5`},
				{name: "decrease writer sequence", set: p + `writer_seq=2`},
				{name: "state change without next writer sequence", set: p + `state='unknown',` + p + `detail='unknown_ack',` + p + `observe_deadline=$4`, args: []any{f.at.Add(15 * time.Minute)}},
				{name: "cancel attempted send", set: p + `state='cancelled',` + p + `due_at=NULL,` + p + `resolution='owner_cancelled',` + p + `writer_seq=4`},
			}
			if owner.loop {
				mutations = append(mutations,
					struct {
						name, set string
						args      []any
					}{name: "replace frozen max passes", set: `max_passes=2,` + p + `writer_seq=4`},
					struct {
						name, set string
						args      []any
					}{name: "replace frozen fix mode", set: `fix_mode='thorough',` + p + `writer_seq=4`},
					struct {
						name, set string
						args      []any
					}{name: "replace frozen review requirement", set: `review_required=true,` + p + `writer_seq=4`},
				)
			}
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						_, err := callerSendSchemaUpdate(ctx, probe, owner, f, mutation.set, mutation.args...)
						return err
					})
					callerSendSchemaSQLState(t, err, "55000", "immutable intent identity and the finite transition graph must reject this overwrite")
					require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "a refused update must preserve the entire current owner row")
				})
			}
			_, err = callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='unknown',`+p+`detail='unknown_ack',`+p+`observe_deadline=$4,`+p+`writer_seq=4`, f.at.Add(15*time.Minute))
			require.NoError(t, err, "an uncertain send becomes observe-only while retaining its second attempt identity")
			unknown := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := callerSendSchemaUpdate(ctx, probe, owner, f, p+`state='pending',`+p+`detail='contention',`+p+`observe_deadline=NULL,`+p+`writer_seq=5`)
				return err
			})
			callerSendSchemaSQLState(t, err, "55000", "unknown send evidence can never return to a resendable pending state")
			require.Equal(t, unknown, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "an unknown-first retry refusal must preserve every original uncertainty field")
			detail := "unknown_ack"
			if owner.loop {
				_, err = tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=456 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
				require.NoError(t, err, "a loop pointer conflict requires positive durable evidence on the exact bound initial pass")
				detail = "pass_pointer_conflict"
			}
			_, err = callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='attention',`+p+`detail=$6,`+p+`attention_at=$4,`+p+`due_at=$5,`+p+`writer_seq=5`, f.at.Add(15*time.Minute), f.at.Add(time.Hour), detail)
			require.NoError(t, err, "a positively detected pointer conflict must have the complete retained attention shape")
			_, err = callerSendSchemaUpdate(ctx, tx, owner, f, p+`state='accepted',`+p+`message_id=123,`+p+`resolution='observed_auto',`+p+`observed_at=$4,`+p+`due_at=NULL,`+p+`writer_seq=6`, f.at.Add(16*time.Minute))
			require.NoError(t, err, "positive observation may accept an uncertain attempt without re-sending or regressing ordinary status")
			var attempt uuid.UUID
			var seq, writer int32
			require.NoError(t, tx.QueryRow(ctx, `SELECT `+p+`attempt_id,`+p+`attempt_seq,`+p+`writer_seq FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&attempt, &seq, &writer), "read the final durable attempt history rather than infer it from successful statements")
			require.Equal(t, second, attempt, "observation must preserve the exact attempted command identity")
			require.Equal(t, [2]int32{2, 6}, [2]int32{seq, writer}, "retry history and writer fencing must remain distinct monotonic sequences")
		})
		for _, from := range []string{"attempting", "unknown", "attention"} {
			t.Run(owner.name+" pointer conflict from "+from, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				callerSendSchemaReachOwner(t, ctx, tx, owner, f, from)
				if owner.loop {
					_, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=456 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
					require.NoError(t, err, "positively install a conflicting pointer on the exact loop-bound first pass before reconciliation")
				}
				p := owner.prefix
				var attempt uuid.UUID
				var sequence int32
				require.NoError(t, tx.QueryRow(ctx, `SELECT `+p+`attempt_id,`+p+`attempt_seq FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&attempt, &sequence), "capture the positively retained attempt before pointer-conflict reconciliation")
				attentionAt := f.at.Add(16 * time.Minute)
				if from == "attention" {
					require.NoError(t, tx.QueryRow(ctx, `SELECT `+p+`attention_at FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&attentionAt), "restore the existing attention timestamp before changing only its pointer-conflict subtype")
					require.Equal(t, f.at.Add(15*time.Minute), attentionAt.UTC(), "the fixture must start with its actual original attention timestamp")
				}
				err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					_, err := callerSendSchemaUpdate(ctx, probe, owner, f, p+`state='attention',`+p+`detail='pass_pointer_conflict',`+p+`attention_at=$4,`+p+`observe_deadline=$5,`+p+`due_at=$6,`+p+`writer_seq=`+p+`writer_seq+1`, attentionAt, f.at.Add(15*time.Minute), f.at.Add(time.Hour))
					if err == nil {
						var actualState, actualDetail string
						var actualAttention time.Time
						require.NoError(t, probe.QueryRow(ctx, `SELECT `+p+`state,`+p+`detail,`+p+`attention_at FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualState, &actualDetail, &actualAttention), "read the successful conflict transition before its deliberately rolled-back probe finishes")
						require.Equal(t, []string{"attention", "pass_pointer_conflict"}, []string{actualState, actualDetail}, "the successful real transition must have installed the complete retained pointer-conflict state")
						require.Equal(t, attentionAt.UTC(), actualAttention.UTC(), "pointer-conflict entry must retain existing attention history or initialize it only on its first entry")
					}
					return err
				})
				if !owner.loop {
					callerSendSchemaSQLState(t, err, "55000", "a code-review dispatch has no pass binding and cannot adopt the loop-only pointer-conflict escape")
					return
				}
				require.NoError(t, err, "same-attempt positively evidenced loop pointer conflict must fit the complete retained attention shape")
				var actualAttempt uuid.UUID
				var actualSequence int32
				require.NoError(t, tx.QueryRow(ctx, `SELECT `+p+`attempt_id,`+p+`attempt_seq FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualAttempt, &actualSequence), "read back the actual retained conflict attempt")
				require.Equal(t, []any{attempt, sequence}, []any{actualAttempt, actualSequence}, "repeated conflict observation cannot mint or replace an attempted command")
			})
		}
	}
}

func callerSendSchemaClear(owner callerSendSchemaOwner) string {
	fields := callerSendSchemaFields(owner)
	set := make([]string, len(fields))
	for i, field := range fields {
		set[i] = owner.prefix + field + "=NULL"
	}
	return strings.Join(set, ",")
}

func callerSendSchemaObservationHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, owner := range callerSendSchemaOwners() {
		for _, state := range []string{"unknown", "attention"} {
			t.Run(owner.name+" "+state, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				callerSendSchemaReachOwner(t, ctx, tx, owner, f, state)
				p := owner.prefix
				_, err := callerSendSchemaUpdate(ctx, tx, owner, f, p+`observed_at=$4,`+p+`due_at=$5,`+p+`writer_seq=`+p+`writer_seq+1`, f.at.Add(16*time.Minute), f.at.Add(time.Hour))
				require.NoError(t, err, "a complete same-state observation may retain a positive observation timestamp and advance its due time")
				before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
				tests := []struct {
					name  string
					value any
				}{
					{name: "clear historical observation", value: nil},
					{name: "regress historical observation", value: f.at.Add(15 * time.Minute)},
				}
				for _, tt := range tests {
					t.Run(tt.name, func(t *testing.T) {
						err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
							_, err := callerSendSchemaUpdate(ctx, probe, owner, f, p+`observed_at=$4,`+p+`due_at=$5,`+p+`writer_seq=`+p+`writer_seq+1`, tt.value, f.at.Add(2*time.Hour))
							return err
						})
						callerSendSchemaSQLState(t, err, "55000", "SQL NULL or an older timestamp must not bypass the same-state observation bump rule")
						require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "a refused self-observation must retain every durable historical field")
					})
				}
			})
		}
	}
}

func callerSendSchemaCancellationCompatibility(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	owner := callerSendSchemaOwners()[1]
	for _, state := range []string{"pending", "attempting", "unknown", "attention", "accepted"} {
		t.Run("running cancellation preserves "+state, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			start := callerSendSchemaStartRow(f, "manual", uuid.Nil)
			start["envelope"] = `{"review_required":true,"synthetic_original_intent":"retain"}`
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", start), "retain the original required-review envelope before loop creation")
			callerSendSchemaReachOwner(t, ctx, tx, owner, f, state, true)
			var before map[string]any
			require.NoError(t, json.Unmarshal(callerSendSchemaSnapshot(t, ctx, tx, owner, f), &before), "capture every startup field before the existing bypass-shaped cancellation")
			var beforeEnvelope, beforeDigest string
			require.NoError(t, tx.QueryRow(ctx, `SELECT envelope::text,envelope_digest FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&beforeEnvelope, &beforeDigest), "capture the immutable original envelope independently from current operational loop flags")
			_, err := callerSendSchemaUpdate(ctx, tx, owner, f, `status='cancelled',review_required=false`)
			require.NoError(t, err, "the existing running-to-cancelled true-to-false bypass update must remain legal without changing original intent")
			var after map[string]any
			require.NoError(t, json.Unmarshal(callerSendSchemaSnapshot(t, ctx, tx, owner, f), &after), "read back actual cancellation rather than infer it from a successful SQL statement")
			require.Equal(t, []any{"running", true, "cancelled", false}, []any{before["status"], before["review_required"], after["status"], after["review_required"]}, "only the exact current operational cancellation flags may change")
			for _, field := range callerSendSchemaFields(owner) {
				require.Contains(t, before, owner.prefix+field, "all nineteen startup fields must participate in the exact before/after comparison")
			}
			delete(before, "status")
			delete(before, "review_required")
			delete(after, "status")
			delete(after, "review_required")
			require.Equal(t, before, after, "cancellation must not settle or rewrite any startup attempt, due time, receipt, payload, or other loop field")
			var afterEnvelope, afterDigest string
			require.NoError(t, tx.QueryRow(ctx, `SELECT envelope::text,envelope_digest FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&afterEnvelope, &afterDigest), "read the independently retained original envelope after operational cancellation")
			require.Equal(t, []string{beforeEnvelope, beforeDigest}, []string{afterEnvelope, afterDigest}, "changing current review_required during cancellation must leave the original envelope and digest immutable")
		})
	}
	tests := []struct {
		name          string
		initial       bool
		initialStatus string
		set           string
	}{
		{name: "standalone true to false", initial: true, set: `review_required=false`},
		{name: "standalone false to true", set: `review_required=true`},
		{name: "other option cannot accompany cancellation", initial: true, set: `status='cancelled',review_required=false,max_passes=2`},
		{name: "failure is not cancellation", initial: true, set: `status='failed',review_required=false`},
		{name: "old state must be running", initial: true, initialStatus: "failed", set: `status='cancelled',review_required=false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			callerSendSchemaReachOwner(t, ctx, tx, owner, f, "unknown", tt.initial)
			if tt.initialStatus != "" {
				_, err := callerSendSchemaUpdate(ctx, tx, owner, f, `status=$4,startup_send_writer_seq=startup_send_writer_seq+1`, tt.initialStatus)
				require.NoError(t, err, "protocol-aware ordinary terminality remains legal before testing the precise review-required exception")
			}
			before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := callerSendSchemaUpdate(ctx, probe, owner, f, tt.set)
				return err
			})
			callerSendSchemaSQLState(t, err, "55000", "the narrow cancellation exception must not authorize other option or review-required mutations")
			require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "a refused operational option rewrite must preserve all retained evidence")
		})
	}
}

func callerSendSchemaLegacyWriters(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, owner := range callerSendSchemaOwners() {
		for _, state := range []string{"legacy", "unknown", "accepted"} {
			t.Run(owner.name+" "+state, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				var row map[string]any
				if state != "legacy" {
					row = callerSendSchemaShape(owner, f, state)
				}
				if state == "legacy" {
					require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, row), "seed an old owner through the complete schema")
					var allNull bool
					fields := callerSendSchemaFields(owner)
					columns := make([]string, len(fields))
					for i, field := range fields {
						columns[i] = owner.prefix + field
					}
					require.NoError(t, tx.QueryRow(ctx, `SELECT num_nonnulls(`+strings.Join(columns, ",")+`)=0 FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&allNull), "inspect the actual old INSERT that omitted every new typed column")
					require.True(t, allNull, "legacy writes must leave all nineteen caller-send columns NULL rather than acquire default protocol identity")
				} else {
					callerSendSchemaReachOwner(t, ctx, tx, owner, f, state)
				}
				var set string
				if owner.loop {
					set = `latest_summary='Synthetic later progress',completed_passes=1,workspace_revision=7,desired_head_sha='1111111111111111111111111111111111111111'`
				} else {
					set = `raw_output='Synthetic later output'`
					if state != "legacy" {
						set += `,dispatch_writer_seq=dispatch_writer_seq+1`
					}
				}
				_, err := callerSendSchemaUpdate(ctx, tx, owner, f, set)
				require.NoError(t, err, "legitimate ancillary progress and output writes must not erase or be mistaken for intent transitions")
				if state == "legacy" {
					t.Run("cannot retrofit retained identity", func(t *testing.T) {
						err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
							_, err := callerSendSchemaUpdate(ctx, probe, owner, f, owner.prefix+`writer_seq=0`)
							return err
						})
						callerSendSchemaSQLState(t, err, "55000", "an old owner cannot acquire partial retained identity through an ordinary update")
					})
				}
			})
		}
	}
}

func callerSendSchemaPointerConflictObservationHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	tx := callerSendSchemaBegin(t, ctx, pool)
	f := callerSendSchemaSeed(t, ctx, tx)
	owner := callerSendSchemaOwners()[1]
	callerSendSchemaReachOwner(t, ctx, tx, owner, f, "attention")
	_, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=456 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
	require.NoError(t, err, "pointer-conflict observation requires positive message evidence on the exact bound initial pass")
	_, err = callerSendSchemaUpdate(ctx, tx, owner, f, `startup_send_observed_at=$4,startup_send_due_at=$5,startup_send_writer_seq=startup_send_writer_seq+1`, f.at.Add(16*time.Minute), f.at.Add(time.Hour))
	require.NoError(t, err, "retain an actual observation history before entering the finite pointer-conflict attention escape")
	var attempt uuid.UUID
	var attemptSequence int32
	require.NoError(t, tx.QueryRow(ctx, `SELECT startup_send_attempt_id,startup_send_attempt_seq FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&attempt, &attemptSequence), "capture the existing attempted command before changing only its positively evidenced conflict detail")
	_, err = callerSendSchemaUpdate(ctx, tx, owner, f, `startup_send_detail='pass_pointer_conflict',startup_send_writer_seq=startup_send_writer_seq+1`)
	require.NoError(t, err, "a positive bound-pass conflict may enter the complete attention shape without minting another attempt")
	var actualState, actualDetail string
	require.NoError(t, tx.QueryRow(ctx, `SELECT startup_send_state,startup_send_detail FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualState, &actualDetail), "positively verify that the regression fixture actually entered pointer-conflicted attention")
	require.Equal(t, []string{"attention", "pass_pointer_conflict"}, []string{actualState, actualDetail}, "negative observation probes must exercise the already-pointer-conflicted path rather than ordinary attention")
	before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
	tests := []struct {
		name     string
		observed any
		due      time.Time
	}{
		{name: "clear observed history", observed: nil, due: f.at.Add(2 * time.Hour)},
		{name: "regress observed history", observed: f.at.Add(15 * time.Minute), due: f.at.Add(2 * time.Hour)},
		{name: "move due time backward", observed: f.at.Add(17 * time.Minute), due: f.at.Add(30 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := callerSendSchemaUpdate(ctx, probe, owner, f, `startup_send_observed_at=$4,startup_send_due_at=$5,startup_send_writer_seq=startup_send_writer_seq+1`, tt.observed, tt.due)
				return err
			})
			callerSendSchemaSQLState(t, err, "55000", "already-pointer-conflicted attention must obey the ordinary monotone observation path, not reenter its initial escape")
			require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "a refused repeated conflict observation must retain the complete original intent, attempt, and observation history")
		})
	}
	_, err = callerSendSchemaUpdate(ctx, tx, owner, f, `startup_send_observed_at=$4,startup_send_due_at=$5,startup_send_writer_seq=startup_send_writer_seq+1`, f.at.Add(17*time.Minute), f.at.Add(2*time.Hour))
	require.NoError(t, err, "a monotone later observation remains legal in already-pointer-conflicted attention")
	var actualAttempt uuid.UUID
	var actualSequence int32
	var actualObserved, actualDue time.Time
	require.NoError(t, tx.QueryRow(ctx, `SELECT startup_send_attempt_id,startup_send_attempt_seq,startup_send_observed_at,startup_send_due_at FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualAttempt, &actualSequence, &actualObserved, &actualDue), "read the actual successful same-attempt monotone observation rather than assume a nil error is complete proof")
	require.Equal(t, []any{attempt, attemptSequence, f.at.Add(17 * time.Minute), f.at.Add(2 * time.Hour)}, []any{actualAttempt, actualSequence, actualObserved.UTC(), actualDue.UTC()}, "successful conflict reobservation must preserve attempt identity while advancing exactly the retained observation and due times")
}

func callerSendSchemaStamps(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, owner := range callerSendSchemaOwners() {
		t.Run(owner.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, callerSendSchemaShape(owner, f, "pending")), "a command stamp belongs to a real typed owner in the same scope")
			var message int64
			require.NoError(t, tx.QueryRow(ctx, `INSERT INTO session_messages(org_id,session_id,thread_id,turn_number,role,content) VALUES($1,$2,$3,1,'user','Synthetic stamped command') RETURNING id`, f.org, f.session, f.thread).Scan(&message), "use a real scoped message while retaining the partitioned-message pointer model")
			purpose := "code-review-reviewer-dispatch"
			if owner.loop {
				purpose = "review-loop-initial-send"
			}
			insert := func(q pgx.Tx, key, p, digest string, version int16, ownerID, org, session, thread uuid.UUID) error {
				_, err := q.Exec(ctx, `INSERT INTO caller_send_command_stamps(org_id,session_id,thread_id,client_message_id,purpose,owner_id,version,digest,message_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, org, session, thread, key, p, ownerID, version, digest, message, f.at)
				return err
			}
			key, digest := owner.keyPrefix+f.owner.String(), strings.Repeat("a", 64)
			require.NoError(t, insert(tx, key, purpose, digest, 1, f.owner, f.org, f.session, f.thread), "the approved descriptive purpose must map to the short owner-derived key prefix without changing the codec purpose")
			var actualPurpose, actualKey, actualDigest string
			var actualVersion int16
			var actualMessage int64
			require.NoError(t, tx.QueryRow(ctx, `SELECT purpose,client_message_id,digest,version,message_id FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND owner_id=$4`, f.org, f.session, f.thread, f.owner).Scan(&actualPurpose, &actualKey, &actualDigest, &actualVersion, &actualMessage), "read the actual stored text digest and scope rather than a bytea bridge")
			require.Equal(t, []any{purpose, key, digest, int16(1), message}, []any{actualPurpose, actualKey, actualDigest, actualVersion, actualMessage}, "stamp storage must preserve the complete approved semantic representation exactly")
			t.Run("exact duplicate owner remains unique", func(t *testing.T) {
				err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					return insert(probe, key, purpose, digest, 1, f.owner, f.org, f.session, f.thread)
				})
				callerSendSchemaSQLState(t, err, "23505", "a second stamp must not create another message binding for the same owner command")
			})
			for _, query := range []string{`DELETE FROM caller_send_command_stamps WHERE org_id=$1 AND owner_id=$2`, `TRUNCATE TABLE caller_send_command_stamps`} {
				t.Run("unresolved retention "+strings.Fields(query)[0], func(t *testing.T) {
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						args := []any{f.org, f.owner}
						if strings.HasPrefix(query, "TRUNCATE") {
							args = nil
						}
						_, err := probe.Exec(ctx, query, args...)
						return err
					})
					callerSendSchemaSQLState(t, err, "55000", "unresolved send proof cannot be deleted through direct cleanup or statement-wide truncate")
				})
			}
			for _, mutation := range []struct{ name, set string }{
				{name: "digest", set: `digest=repeat('b',64)`},
				{name: "message", set: `message_id=message_id+1`},
				{name: "purpose", set: `purpose='synthetic-unsupported'`},
			} {
				t.Run("immutable "+mutation.name, func(t *testing.T) {
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						_, err := probe.Exec(ctx, `UPDATE caller_send_command_stamps SET `+mutation.set+` WHERE org_id=$1 AND owner_id=$2`, f.org, f.owner)
						return err
					})
					callerSendSchemaSQLState(t, err, "55000", "a retained stamp is immutable evidence of the original exact send command")
				})
			}
			for _, scope := range []string{"different organization", "different session", "different thread"} {
				t.Run(scope, func(t *testing.T) {
					foreign := callerSendSchemaSeed(t, ctx, tx)
					org, session, thread := f.org, f.session, f.thread
					switch scope {
					case "different organization":
						org = foreign.org
					case "different session":
						session = callerSendSchemaAnotherSession(t, ctx, tx, f.org)
					case "different thread":
						lane := models.SessionThread{OrgID: f.org, SessionID: f.session, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic different caller lane", Status: models.ThreadStatusIdle}
						require.NoError(t, NewSessionThreadStore(tx).Create(ctx, &lane, 10), "seed a valid same-session sibling rather than rely on a nonexistent parent to reject the stamp")
						thread = lane.ID
					}
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error { return insert(probe, key, purpose, digest, 1, f.owner, org, session, thread) })
					callerSendSchemaSQLState(t, err, "23503", "the same owner key and digest must not be rebound to a different tenant session or sibling lane")
				})
			}
		})
	}
	owner := callerSendSchemaOwners()[0]
	negative := []struct {
		name, field string
		value       any
		code        string
	}{
		{name: "unsupported purpose", field: "purpose", value: "synthetic-unsupported", code: "23514"},
		{name: "wrong key prefix", field: "client_message_id", value: "wrong:v1:00000000-0000-0000-0000-000000000001", code: "23514"},
		{name: "unsupported version", field: "version", value: int16(2), code: "23514"},
		{name: "zero owner", field: "owner_id", value: uuid.Nil, code: "23514"},
		{name: "short digest", field: "digest", value: strings.Repeat("a", 63), code: "23514"},
		{name: "zero digest", field: "digest", value: strings.Repeat("0", 64), code: "23514"},
		{name: "uppercase digest", field: "digest", value: strings.Repeat("A", 64), code: "23514"},
		{name: "long digest", field: "digest", value: strings.Repeat("a", 65), code: "23514"},
		{name: "whitespace digest", field: "digest", value: strings.Repeat("a", 63) + " ", code: "23514"},
		{name: "nonhex digest", field: "digest", value: strings.Repeat("g", 64), code: "23514"},
		{name: "bytea digest spelling", field: "digest", value: `\x` + strings.Repeat("a", 64), code: "23514"},
		{name: "non ASCII digest", field: "digest", value: strings.Repeat("ａ", 64), code: "23514"},
	}
	for _, tt := range negative {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, callerSendSchemaShape(owner, f, "pending")), "seed a fresh owner so each malformed INSERT reaches real stamp validation")
			values := map[string]any{"org_id": f.org, "session_id": f.session, "thread_id": f.thread, "client_message_id": owner.keyPrefix + f.owner.String(), "purpose": "code-review-reviewer-dispatch", "owner_id": f.owner, "version": int16(1), "digest": strings.Repeat("a", 64), "message_id": int64(123), "created_at": f.at}
			values[tt.field] = tt.value
			var expression string
			require.NoError(t, tx.QueryRow(ctx, `SELECT pg_get_expr(conbin,conrelid) FROM pg_constraint WHERE conrelid='caller_send_command_stamps'::regclass AND conname='chk_caller_send_stamp' AND convalidated`).Scan(&expression), "read the actual stamp CHECK independently of its exact-owner trigger")
			require.False(t, callerSendSchemaConstraintCheck(t, ctx, tx, "caller_send_command_stamps", expression, values), "the installed stamp CHECK itself must reject every malformed representation")
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				return callerSendSchemaInsertMap(ctx, probe, "caller_send_command_stamps", values)
			})
			callerSendSchemaSQLState(t, err, "23503", "the actual stamp INSERT must refuse the different command before it can bypass exact owner scope")
		})
	}
}

func callerSendSchemaInsertMap(ctx context.Context, tx pgx.Tx, table string, values map[string]any) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	columns, args, placeholders := make([]string, len(keys)), make([]any, len(keys)), make([]string, len(keys))
	for i, key := range keys {
		columns[i], args[i], placeholders[i] = pgx.Identifier{key}.Sanitize(), values[key], fmt.Sprintf("$%d", i+1)
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+pgx.Identifier{table}.Sanitize()+` (`+strings.Join(columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func callerSendSchemaAnotherSession(t *testing.T, ctx context.Context, tx pgx.Tx, org uuid.UUID) uuid.UUID {
	t.Helper()
	session := models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(tx).CreateInTx(ctx, tx, &session), "create a second legitimate same-tenant session for exact binding negatives")
	return session.ID
}

func callerSendSchemaPassBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	owner := callerSendSchemaOwners()[1]
	for _, tt := range []struct{ name, query, code string }{
		{name: "bound pass cannot be deleted", query: `DELETE FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND id=$3`, code: "23503"},
		{name: "bound pass session cannot be replaced", query: `UPDATE session_review_loop_passes SET session_id=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, code: "23503"},
		{name: "bound pass loop cannot be replaced", query: `UPDATE session_review_loop_passes SET loop_id=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, code: "23503"},
		{name: "bound pass organization cannot be replaced", query: `UPDATE session_review_loop_passes SET org_id=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, code: "23503"},
		{name: "direct pass truncate cannot ignore reverse reference", query: `TRUNCATE TABLE session_review_loop_passes`, code: "0A000"},
		{name: "cascading pass truncate retains unresolved startup", query: `TRUNCATE TABLE session_review_loop_passes CASCADE`, code: "55000"},
		{name: "owner delete retains unresolved startup", query: `DELETE FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, code: "55000"},
		{name: "parent hard cascade retains unresolved startup", query: `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, code: "55000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			other := callerSendSchemaAnotherSession(t, ctx, tx, f.org)
			if strings.Contains(tt.name, "loop cannot") {
				otherLoop := f
				otherLoop.owner, otherLoop.pass = uuid.New(), uuid.New()
				require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, otherLoop, nil), "seed a valid same-tenant legacy loop so a reparent rejection proves the reverse binding rather than parent absence")
				_, err := tx.Exec(ctx, `DELETE FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, otherLoop.owner, otherLoop.pass)
				require.NoError(t, err, "remove the unbound legacy pass so the reparent probe cannot fail on the unrelated per-loop pass-index unique key")
				other = otherLoop.owner
			} else if strings.Contains(tt.name, "organization cannot") {
				other = uuid.New()
				_, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic different pass tenant')`, other)
				require.NoError(t, err, "use an existing foreign tenant so the bound-pass rejection cannot be credited to a missing organization")
			}
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, callerSendSchemaShape(owner, f, "pending")), "create the loop and bound first pass in the same deferred-FK transaction")
			before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				if strings.Contains(tt.name, "parent hard cascade") {
					_, err := probe.Exec(ctx, `ALTER TABLE sessions DISABLE TRIGGER trg_sessions_soft_delete`)
					if err != nil {
						return err
					}
				}
				args := []any{f.org, f.session, f.pass, other}
				if strings.HasPrefix(tt.query, "DELETE FROM session_review_loops ") {
					args[2] = f.owner
				}
				if strings.HasPrefix(tt.query, "TRUNCATE") {
					args = nil
				} else {
					if strings.Contains(tt.query, "$4") {
						args = args[:4]
					} else if strings.Contains(tt.query, "$3") {
						args = args[:3]
					} else {
						args = args[:2]
					}
				}
				_, err := probe.Exec(ctx, tt.query, args...)
				return err
			})
			callerSendSchemaSQLState(t, err, tt.code, "actual DELETE UPDATE and TRUNCATE semantics must preserve the complete unresolved initial-pass binding")
			require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "failed evidence deletion must not alter loop or startup state")
			var softDeleteEnabled bool
			require.NoError(t, tx.QueryRow(ctx, `SELECT tgenabled='O' FROM pg_trigger WHERE tgrelid='sessions'::regclass AND tgname='trg_sessions_soft_delete'`).Scan(&softDeleteEnabled), "inspect the production soft-delete trigger after the isolated savepoint probe")
			require.True(t, softDeleteEnabled, "rolling back the hard-cascade probe must restore the unchanged production soft-delete protection")
		})
	}
	t.Run("parent soft deletion retains startup", func(t *testing.T) {
		tx := callerSendSchemaBegin(t, ctx, pool)
		f := callerSendSchemaSeed(t, ctx, tx)
		require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, callerSendSchemaShape(owner, f, "pending")), "seed retained startup before the ordinary existing session soft-delete operation")
		before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, f.org, f.session)
		require.NoError(t, err, "normal session DELETE must preserve existing soft-delete behavior rather than claim a hard cascade occurred")
		var deleted bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM sessions WHERE org_id=$1 AND id=$2`, f.org, f.session).Scan(&deleted), "positively observe the actual soft-deleted parent that remains durably present")
		require.True(t, deleted, "the real session deletion trigger must mark the parent deleted without removing its evidence")
		require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "soft deletion must retain all startup intent and attempt evidence unchanged")
	})
	t.Run("later pass progress and resolved deletion", func(t *testing.T) {
		tx := callerSendSchemaBegin(t, ctx, pool)
		f := callerSendSchemaSeed(t, ctx, tx)
		callerSendSchemaReachOwner(t, ctx, tx, owner, f, "accepted")
		_, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET status='deciding',review_message_id=123,summary='Synthetic later progress' WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
		require.NoError(t, err, "the startup reverse binding must not freeze ordinary pass progress")
		_, err = tx.Exec(ctx, `DELETE FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
		require.NoError(t, err, "resolved startup evidence must not permanently block the existing loop-to-pass cascade")
		var remaining int
		require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3`, f.org, f.session, f.owner).Scan(&remaining), "read the actual cascade result inside the owner transaction")
		require.Equal(t, 0, remaining, "resolved-loop deletion must remove only its bound pass through the existing cascade")
	})
}

func callerSendSchemaStartRow(f callerSendSchemaFixture, kind string, caller uuid.UUID) map[string]any {
	row := map[string]any{
		"id": f.owner, "org_id": f.org, "session_id": f.session, "caller_kind": kind,
		"envelope": `{}`, "envelope_digest": strings.Repeat("a", 64), "pass_id": f.pass, "thread_id": f.thread,
		"startup_input": callerSendSchemaInput(callerSendSchemaOwners()[1], f), "startup_digest": strings.Repeat("a", 64), "state": "prepared", "due_at": f.at,
		"created_at": f.at, "updated_at": f.at,
	}
	switch kind {
	case "automation":
		row["automation_run_id"] = caller
	case "publication":
		row["publication_changeset_id"] = caller
	}
	return row
}

func callerSendSchemaCaller(t *testing.T, ctx context.Context, tx pgx.Tx, f callerSendSchemaFixture, kind string) (table string, id uuid.UUID, startCaller uuid.UUID) {
	t.Helper()
	id = uuid.New()
	if kind == "automation" {
		automation := uuid.New()
		_, err := tx.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic caller binding','Verify retained caller identity')`, automation, f.org)
		require.NoError(t, err, "seed an owned automation without scheduler or production effects")
		_, err = tx.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by) VALUES($1,$2,$3,'Synthetic review-start caller',$4,'manual')`, id, f.org, automation, f.session)
		require.NoError(t, err, "seed the exact automatic caller whose creation identity must be retained")
		return "automation_runs", id, id
	}
	integration, repository, changeset := uuid.New(), uuid.New(), uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, f.org)
	require.NoError(t, err, "seed a synthetic repository integration for the publication scope")
	_, err = tx.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/caller-schema','https://example.invalid/caller-schema.git',1)`, repository, f.org, integration)
	require.NoError(t, err, "use a synthetic owned repository rather than weakening publication parent references")
	_, err = tx.Exec(ctx, `INSERT INTO session_changesets(id,org_id,session_id,order_index,title,target_branch,base_branch) VALUES($1,$2,$3,99,'Synthetic review-start scope','main','main')`, changeset, f.org, f.session)
	require.NoError(t, err, "seed an exact same-session changeset for the publication caller")
	_, err = tx.Exec(ctx, `INSERT INTO session_publications(id,org_id,session_id,changeset_id,repository_id,base_branch,head_branch) VALUES($1,$2,$3,$4,$5,'main','synthetic/caller-schema')`, id, f.org, f.session, changeset, repository)
	require.NoError(t, err, "create a valid publication without remote operations")
	return "session_publications", id, changeset
}

func callerSendSchemaStarts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Run("created then deleted cannot bypass deferred binding", func(t *testing.T) {
		tx := callerSendSchemaBegin(t, ctx, pool)
		f := callerSendSchemaSeed(t, ctx, tx)
		require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, "manual", uuid.Nil)), "begin with a genuine prepared record before testing the deferred creation boundary")
		err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
			if _, err := probe.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner); err != nil {
				return err
			}
			if _, err := probe.Exec(ctx, `DELETE FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner); err != nil {
				return err
			}
			_, err := probe.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			return err
		})
		callerSendSchemaSQLState(t, err, "23503", "a same-transaction delete cannot hide a created transition that never installed its actual loop and first pass")
	})
	t.Run("prior prepared cancellation forbids created transition", func(t *testing.T) {
		tx := callerSendSchemaBegin(t, ctx, pool)
		f := callerSendSchemaSeed(t, ctx, tx)
		require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, "manual", uuid.Nil)), "retain a prepared creation before its caller cancellation arrives")
		_, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET cancel_requested_at=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner, f.at)
		require.NoError(t, err, "persist a positive cancellation before any claimable startup exists")
		err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
			_, err := probe.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
			return err
		})
		callerSendSchemaSQLState(t, err, "55000", "a prior cancellation cannot be bypassed by publishing created before a later separate CancelLoop")
		_, err = tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='refused',due_at=NULL,detail='owner_cancelled_before_creation' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
		require.NoError(t, err, "positive cancellation may resolve only as a known before-creation refusal")
		var count int
		require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND id=$2`, f.org, f.owner).Scan(&count), "read the actual loop absence after the cancellation-winning schedule")
		require.Equal(t, 0, count, "cancellation winning before creation must expose no claimable startup interval")
	})
	for _, field := range []string{"envelope_digest", "startup_digest"} {
		for _, tt := range []struct{ name, value string }{
			{name: "all zero", value: strings.Repeat("0", 64)},
			{name: "uppercase", value: strings.Repeat("A", 64)},
			{name: "short", value: strings.Repeat("a", 63)},
			{name: "long", value: strings.Repeat("a", 65)},
			{name: "whitespace", value: strings.Repeat("a", 63) + " "},
			{name: "nonhex", value: strings.Repeat("g", 64)},
			{name: "bytea spelling", value: `\x` + strings.Repeat("a", 64)},
			{name: "non ASCII", value: strings.Repeat("ａ", 64)},
		} {
			t.Run(field+" rejects "+tt.name, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				row := callerSendSchemaStartRow(f, "manual", uuid.Nil)
				row[field] = tt.value
				err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					return callerSendSchemaInsertMap(ctx, probe, "review_loop_start_intents", row)
				})
				callerSendSchemaSQLState(t, err, "23514", "each separately retained creation digest must use the same exact nonzero lower-ASCII SHA256 text representation")
			})
		}
	}
	for _, kind := range []string{"manual", "automation", "publication"} {
		t.Run(kind+" complete and immutable prepared envelope", func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			var caller uuid.UUID
			if kind != "manual" {
				_, _, caller = callerSendSchemaCaller(t, ctx, tx, f, kind)
			}
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, kind, caller)), "a prepared creation keeps the complete original envelope before its loop or first pass exists")
			_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			require.NoError(t, err, "flush the genuine prepared-row deferred events before TRUNCATE so its retained-state trigger, not pending-event protection, is exercised")
			var loopCount int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND id=$2`, f.org, f.owner).Scan(&loopCount), "prepared creation is valid without pretending that a loop already exists")
			require.Equal(t, 0, loopCount, "the retained creation row alone is not proof of successful loop creation")
			for _, tt := range []struct{ name, set string }{
				{name: "envelope", set: `envelope='{"changed":true}'`},
				{name: "digest", set: `envelope_digest=repeat('b',64)`},
				{name: "startup input", set: `startup_input='{"changed":true}'`},
				{name: "startup digest", set: `startup_digest=repeat('b',64)`},
				{name: "thread", set: `thread_id=gen_random_uuid()`},
				{name: "pass", set: `pass_id=gen_random_uuid()`},
			} {
				t.Run(tt.name, func(t *testing.T) {
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						_, err := probe.Exec(ctx, `UPDATE review_loop_start_intents SET `+tt.set+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
						return err
					})
					callerSendSchemaSQLState(t, err, "55000", "creation recovery cannot silently replace the retained original envelope or pre-minted identity")
				})
			}
			for _, tt := range []struct{ name, query string }{
				{name: "direct delete", query: `DELETE FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`},
				{name: "session cascade", query: `DELETE FROM sessions WHERE org_id=$1 AND id=$2`},
				{name: "truncate", query: `TRUNCATE TABLE review_loop_start_intents CASCADE`},
			} {
				t.Run(tt.name, func(t *testing.T) {
					before := callerSendSchemaAllRows(t, ctx, tx, f.org)
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						if tt.name == "session cascade" {
							_, err := probe.Exec(ctx, `ALTER TABLE sessions DISABLE TRIGGER trg_sessions_soft_delete`)
							if err != nil {
								return err
							}
						}
						args := []any{f.org, f.session, f.owner}
						if strings.HasPrefix(tt.query, "TRUNCATE") {
							args = nil
						} else if !strings.Contains(tt.query, "$3") {
							args = args[:2]
						}
						_, err := probe.Exec(ctx, tt.query, args...)
						return err
					})
					if kind == "automation" && tt.name == "session cascade" {
						callerSendSchemaSQLState(t, err, "23503", "the existing runtime-session NO ACTION FK prevents this cascade before the new retained-start trigger can run")
						var pgErr *pgconn.PgError
						require.ErrorAs(t, err, &pgErr, "the automation cascade must positively identify its existing FK refusal")
						require.Equal(t, "automation_runs_session_id_fkey", pgErr.ConstraintName, "credit only the exact existing runtime-session FK, not a new S1 retention-guard claim")
					} else {
						callerSendSchemaSQLState(t, err, "55000", "the actual prepared-start retention guard must preserve unresolved creation without an ordinary loop")
					}
					require.Equal(t, before, callerSendSchemaAllRows(t, ctx, tx, f.org), "every refused cleanup must retain the exact start, caller, session, and control evidence unchanged")
					var softDeleteEnabled bool
					require.NoError(t, tx.QueryRow(ctx, `SELECT tgenabled='O' FROM pg_trigger WHERE tgrelid='sessions'::regclass AND tgname='trg_sessions_soft_delete'`).Scan(&softDeleteEnabled), "read back the existing soft-delete protection after the retained-start cascade probe")
					require.True(t, softDeleteEnabled, "the savepoint rollback must restore the existing soft-delete trigger after every creation-retention probe")
				})
			}
		})
	}
	for _, kind := range []string{"automation", "publication"} {
		for _, state := range []string{"prepared", "created unresolved", "created accepted", "refused"} {
			t.Run(kind+" caller "+state, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				table, callerID, startCaller := callerSendSchemaCaller(t, ctx, tx, f, kind)
				require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, kind, startCaller)), "persist the original creation identity before binding its automatic caller")
				_, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{table}.Sanitize()+` SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, callerID, f.owner)
				require.NoError(t, err, "the exact matching caller kind and original identity must bind to the prepared start")
				if strings.HasPrefix(state, "created") {
					owner := callerSendSchemaOwners()[1]
					target := "pending"
					if state == "created accepted" {
						target = "accepted"
					}
					callerSendSchemaReachOwner(t, ctx, tx, owner, f, target)
					_, err = tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
					require.NoError(t, err, "created must bind the actual same-ID loop, first pass and frozen startup command")
					_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
					require.NoError(t, err, "deferred created-start validation must prove the complete actual loop/pass/thread scope")
				} else if state == "refused" {
					_, err = tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='refused',detail='session_unavailable',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
					require.NoError(t, err, "a positively known before-creation refusal may resolve the retained preparation")
				}
				terminalSet := `status='failed',result_summary='Synthetic terminal caller'`
				if kind == "publication" {
					terminalSet = `state='terminal_failed',last_error_code='synthetic_terminal'`
				}
				_, err = tx.Exec(ctx, `UPDATE `+pgx.Identifier{table}.Sanitize()+` SET `+terminalSet+` WHERE org_id=$1 AND id=$2`, f.org, callerID)
				require.NoError(t, err, "ordinary caller terminality must remain legal without discarding unresolved creation identity")
				err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					_, err := probe.Exec(ctx, `UPDATE `+pgx.Identifier{table}.Sanitize()+` SET review_start_id=NULL WHERE org_id=$1 AND id=$2`, f.org, callerID)
					return err
				})
				if state == "prepared" || state == "created unresolved" {
					callerSendSchemaSQLState(t, err, "55000", "terminal callers still cannot clear either prepared creation or created-but-unresolved startup")
					err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						_, err := probe.Exec(ctx, `DELETE FROM `+pgx.Identifier{table}.Sanitize()+` WHERE org_id=$1 AND id=$2`, f.org, callerID)
						return err
					})
					callerSendSchemaSQLState(t, err, "55000", "caller deletion cannot erase a retained unresolved start binding")
				} else {
					require.NoError(t, err, "only a positively refused creation or resolved startup may release the caller identity")
				}
			})
		}
	}
}

func callerSendSchemaMigration(t *testing.T, version int, direction string) string {
	t.Helper()
	names := map[int]string{
		384: "caller_send_intents", 385: "code_review_dispatch_due", 386: "review_loop_startup_due",
		387: "review_loop_startup_pass_key", 388: "caller_send_intent_validation", 389: "caller_send_intent_readiness",
	}
	name, ok := names[version]
	require.True(t, ok, "the schema proof reads only the six explicitly scoped caller-send migrations")
	require.Contains(t, []string{"up", "down"}, direction, "migration proof must not accept arbitrary path suffixes")
	raw, err := os.ReadFile(filepath.Join("../../migrations", fmt.Sprintf("%06d_%s.%s.sql", version, name, direction)))
	require.NoError(t, err, "read the actual checked-in migration rather than duplicate its readiness or rollback SQL")
	return string(raw)
}

func callerSendSchemaControl(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, control := range []string{"absent", "not ready", "disabled"} {
		for _, kind := range []string{"dispatch", "startup", "start"} {
			t.Run(control+" refuses "+kind, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				query := `UPDATE caller_send_intent_control SET schema_ready=true,intents_enabled=false WHERE singleton`
				if control == "absent" {
					query = `DELETE FROM caller_send_intent_control WHERE singleton`
				} else if control == "not ready" {
					query = `UPDATE caller_send_intent_control SET schema_ready=false,intents_enabled=false WHERE singleton`
				}
				_, err := tx.Exec(ctx, query)
				require.NoError(t, err, "install the exact absent or false control precondition inside this isolated case")
				err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					if kind == "start" {
						return callerSendSchemaInsertMap(ctx, probe, "review_loop_start_intents", callerSendSchemaStartRow(f, "manual", uuid.Nil))
					}
					owner := callerSendSchemaOwners()[0]
					if kind == "startup" {
						owner = callerSendSchemaOwners()[1]
					}
					return callerSendSchemaInsertOwner(ctx, probe, owner, f, callerSendSchemaShape(owner, f, "pending"))
				})
				callerSendSchemaSQLState(t, err, "55000", "absent false or unready control must not permit any new retained caller-send operation")
				for _, owner := range callerSendSchemaOwners() {
					require.NoError(t, callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error { return callerSendSchemaInsertOwner(ctx, probe, owner, f, nil) }), "the same disabled control must preserve all-NULL legacy owner creation")
				}
			})
		}
	}
	for _, kind := range []string{"dispatch pending", "dispatch attempting", "dispatch unknown", "dispatch attention", "startup pending", "startup attempting", "startup unknown", "startup attention", "prepared without loop", "automation caller without loop", "publication caller without loop"} {
		t.Run("highest down retains "+kind, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			if strings.HasPrefix(kind, "dispatch ") || strings.HasPrefix(kind, "startup ") {
				parts := strings.Split(kind, " ")
				owner := callerSendSchemaOwners()[0]
				if parts[0] == "startup" {
					owner = callerSendSchemaOwners()[1]
				}
				callerSendSchemaReachOwner(t, ctx, tx, owner, f, parts[1])
			} else {
				callerKind, caller := "manual", uuid.Nil
				var table string
				var callerID uuid.UUID
				if strings.HasPrefix(kind, "automation ") || strings.HasPrefix(kind, "publication ") {
					callerKind = strings.Split(kind, " ")[0]
					table, callerID, caller = callerSendSchemaCaller(t, ctx, tx, f, callerKind)
				}
				require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, callerKind, caller)), "retain creation before any loop is visible to ordinary scans")
				if table != "" {
					_, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{table}.Sanitize()+` SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, callerID, f.owner)
					require.NoError(t, err, "retain the exact caller creation binding before the highest down")
				}
			}
			before := callerSendSchemaAllRows(t, ctx, tx, f.org)
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := probe.Exec(ctx, callerSendSchemaMigration(t, 389, "down"))
				return err
			})
			callerSendSchemaSQLState(t, err, "55000", "the very first down must refuse every unresolved dispatch startup and retained-creation category")
			require.Equal(t, before, callerSendSchemaAllRows(t, ctx, tx, f.org), "a refused rollback must preserve all retained fields caller links and control flags")
			err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := probe.Exec(ctx, callerSendSchemaMigration(t, 384, "down"))
				return err
			})
			callerSendSchemaSQLState(t, err, "55000", "the final storage-removal boundary must independently recheck unresolved evidence")
			require.Equal(t, before, callerSendSchemaAllRows(t, ctx, tx, f.org), "a refused storage removal must not drop or clear any retained evidence")
		})
	}
	t.Run("missing required index cannot become ready", func(t *testing.T) {
		tx := callerSendSchemaBegin(t, ctx, pool)
		_, err := tx.Exec(ctx, `UPDATE caller_send_intent_control SET schema_ready=false,intents_enabled=false WHERE singleton`)
		require.NoError(t, err, "begin with an explicitly unready fixture before testing catalog readiness")
		_, err = tx.Exec(ctx, `DROP INDEX idx_cr_agent_results_dispatch_due`)
		require.NoError(t, err, "remove only the scoped disposable fixture index inside this rollback-owned transaction")
		err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
			_, err := probe.Exec(ctx, callerSendSchemaMigration(t, 389, "up"))
			return err
		})
		callerSendSchemaSQLState(t, err, "55000", "the highest up cannot advertise readiness without every required valid and ready index")
		var ready, enabled bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "read back the actual control row after the failed readiness check")
		require.Equal(t, [2]bool{}, [2]bool{ready, enabled}, "a failed readiness check must leave both gates false")
	})
	t.Run("failed concurrent index build cannot become ready", func(t *testing.T) {
		callerSendSchemaInvalidIndex(t, ctx, pool)
	})
	t.Run("highest down serializes a new intent", func(t *testing.T) {
		callerSendSchemaDownInsertRace(t, ctx, pool)
	})
}

func callerSendSchemaInvalidIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "own the complete committed synthetic duplicate fixture for a real concurrent-index failure")
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := callerSendSchemaSeed(t, ctx, tx)
	owner := callerSendSchemaOwners()[0]
	require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, nil), "the invalid-index test uses only ordinary legacy rows")
	f.owner = uuid.New()
	require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, nil), "two legitimate equal legacy statuses make the deliberate unique-index build fail")
	require.NoError(t, tx.Commit(ctx), "commit only synthetic no-intent rows before standalone concurrent DDL")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET schema_ready=false,intents_enabled=false WHERE singleton`)
	require.NoError(t, err, "never advertise readiness during the deliberate failed-index fixture")
	_, err = pool.Exec(ctx, `DROP INDEX CONCURRENTLY idx_cr_agent_results_dispatch_due`)
	require.NoError(t, err, "the disposable fixture alone removes the exact required index before reconstructing its failed-build state")
	_, err = pool.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY idx_cr_agent_results_dispatch_due ON code_review_agent_results(status)`)
	callerSendSchemaSQLState(t, err, "23505", "a real failed concurrent unique build must leave an invalid catalog entry rather than a mocked validity flag")
	var valid, ready bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT indisvalid,indisready FROM pg_index WHERE indexrelid='idx_cr_agent_results_dispatch_due'::regclass`).Scan(&valid, &ready), "inspect the actual failed concurrent-build catalog state")
	require.False(t, valid, "the readiness proof must exercise a genuinely INVALID index")
	_, err = pool.Exec(ctx, callerSendSchemaMigration(t, 389, "up"))
	callerSendSchemaSQLState(t, err, "55000", "an INVALID index with the expected name must not pass the highest readiness gate")
	var schemaReady, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&schemaReady, &enabled), "failed readiness must be read back independently of the failed DDL")
	require.Equal(t, [2]bool{}, [2]bool{schemaReady, enabled}, "the failed concurrent build must leave both control flags false")
	_, err = pool.Exec(ctx, `DROP INDEX CONCURRENTLY idx_cr_agent_results_dispatch_due`)
	require.NoError(t, err, "remove only the deliberate invalid fixture index through standalone concurrent DDL")
	_, err = pool.Exec(ctx, callerSendSchemaMigration(t, 385, "up"))
	require.NoError(t, err, "restore the actual scoped due index from the checked-in migration")
	_, err = pool.Exec(ctx, callerSendSchemaMigration(t, 389, "up"))
	require.NoError(t, err, "readiness can return only after the complete valid index has been restored")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "explicitly re-enable only this disposable fixture for the next sequential case")
}

func callerSendSchemaDownInsertRace(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	fixture, err := pool.Begin(ctx)
	require.NoError(t, err, "own the synthetic session fixture before starting the control-lock race")
	defer func() { _ = fixture.Rollback(context.Background()) }()
	f := callerSendSchemaSeed(t, ctx, fixture)
	require.NoError(t, fixture.Commit(ctx), "commit only parent rows so both independent race participants see the same exact scope")
	down, err := pool.Begin(ctx)
	require.NoError(t, err, "the actual highest down owns its own independent transaction")
	defer func() { _ = down.Rollback(context.Background()) }()
	var downPID int32
	require.NoError(t, down.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&downPID), "capture the actual down owner's backend for positive blocking evidence")
	_, err = down.Exec(ctx, callerSendSchemaMigration(t, 389, "down"))
	require.NoError(t, err, "the first down must lock and disable an otherwise empty retained-intent schema")
	writer, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve a second independent connection before attempting the concurrent intent INSERT")
	writerPID := int32(writer.Conn().PgConn().PID())
	workCtx, workCancel := context.WithTimeout(ctx, 10*time.Second)
	defer workCancel()
	result := make(chan error, 1)
	go func() {
		tx, err := writer.Begin(workCtx)
		if err != nil {
			result <- err
			return
		}
		owner := callerSendSchemaOwners()[0]
		err = callerSendSchemaInsertOwner(workCtx, tx, owner, f, callerSendSchemaShape(owner, f, "pending"))
		rollbackErr := tx.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
		result <- err
	}()
	joined := false
	defer func() {
		defer writer.Release()
		workCancel()
		// Releasing the lock owner and joining the worker precede returning its
		// physical connection, including every fatal assertion/timeout path.
		rollbackErr := down.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Errorf("release the race's down owner before joining its blocked writer: %v", rollbackErr)
		}
		if !joined {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
				t.Error("the bounded caller-schema insertion worker did not finish during cleanup")
			}
		}
	}()
	observed := false
	probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer probeCancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !observed && probeCtx.Err() == nil {
		require.NoError(t, pool.QueryRow(probeCtx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer))`, downPID, writerPID).Scan(&observed), "observe the exact down backend physically blocking the actual INSERT, not a sleep-based ordering assumption")
		if !observed {
			select {
			case <-ticker.C:
			case <-probeCtx.Done():
			}
		}
	}
	require.True(t, observed, "the actual highest-down transaction must positively fence the concurrent new-intent writer")
	require.NoError(t, down.Commit(ctx), "known down commit must publish the disabled gate before releasing the blocked writer")
	select {
	case err = <-result:
		joined = true
	case <-workCtx.Done():
		t.Fatal("the physically observed writer did not finish after the highest down committed")
	}
	callerSendSchemaSQLState(t, err, "55000", "the released writer must recheck the now-disabled committed control instead of creating an intent across the rollback boundary")
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&remaining), "verify the physically fenced writer left no retained result row")
	require.Equal(t, 0, remaining, "no intent may become committed after the highest rollback gate was disabled")
	_, err = pool.Exec(ctx, callerSendSchemaMigration(t, 389, "up"))
	require.NoError(t, err, "restore readiness through the actual highest up after the race ends")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "explicitly restore only the isolated fixture's opt-in after positive race proof")
}

func callerSendSchemaAllRows(t *testing.T, ctx context.Context, tx pgx.Tx, org uuid.UUID) []json.RawMessage {
	t.Helper()
	var rows []json.RawMessage
	for _, table := range []string{"code_review_agent_results", "session_review_loops", "session_review_loop_passes", "review_loop_start_intents", "caller_send_command_stamps", "automation_runs", "session_publications"} {
		var value json.RawMessage
		require.NoError(t, tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` r WHERE org_id=$1`, org).Scan(&value), "capture all exact synthetic retained and caller fields privately for rollback comparison")
		rows = append(rows, value)
	}
	var control json.RawMessage
	require.NoError(t, tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c)),'[]'::jsonb) FROM caller_send_intent_control c`).Scan(&control), "include the exact global control state in rollback preservation proof")
	return append(rows, control)
}

func callerSendSchemaJSONBBounds(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	const capBytes = 8 << 20
	for _, owner := range callerSendSchemaOwners() {
		for _, tt := range []struct {
			name     string
			bytes    int
			accepted bool
		}{
			{name: "near cap", bytes: capBytes - 1, accepted: true},
			{name: "exact raw cap", bytes: capBytes, accepted: true},
			{name: "one byte over raw cap", bytes: capBytes + 1},
		} {
			t.Run(owner.name+" "+tt.name, func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				row := callerSendSchemaShape(owner, f, "pending")
				var input string
				var rawSize, compactSize int
				// PostgreSQL itself renders the bound object; canonical compact
				// JSON and the JSONB presentation differ by real spacing bytes.
				err := tx.QueryRow(ctx, `WITH base AS (SELECT $1::jsonb AS value), sized AS (SELECT value || jsonb_build_object('synthetic_padding',repeat('x',$2-octet_length((value || jsonb_build_object('synthetic_padding',''))::text))) AS value FROM base) SELECT value::text,octet_length(value::text),octet_length(regexp_replace(value::text,': ',':','g')) FROM sized`, row["input"], tt.bytes).Scan(&input, &rawSize, &compactSize)
				require.NoError(t, err, "derive the actual stored JSONB presentation size instead of inferring storage fit from a codec preflight")
				require.Equal(t, tt.bytes, rawSize, "the server-rendered input must exercise the exact requested byte boundary")
				require.Less(t, compactSize, rawSize, "a compact representation must not be confused with the actual JSONB read representation")
				row["input"] = input
				err = callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					if err := callerSendSchemaInsertOwner(ctx, probe, owner, f, row); err != nil {
						return err
					}
					var actual string
					if err := probe.QueryRow(ctx, `SELECT `+owner.prefix+`input::text FROM `+pgx.Identifier{owner.table}.Sanitize()+` WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actual); err != nil {
						return err
					}
					require.Equal(t, input, actual, "the actual stored/read input must retain every byte of the full boundary object")
					return nil
				})
				if tt.accepted {
					require.NoError(t, err, "complete scoped inputs at or below the raw storage cap remain restorable")
				} else {
					callerSendSchemaSQLState(t, err, "23514", "compact-fit but JSONB-too-large input must fail before a retained command can commit")
					require.LessOrEqual(t, compactSize, capBytes, "the rejected case must genuinely fit compactly while exceeding the actual JSONB cap")
				}
				var counts [3]int
				require.NoError(t, tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM code_review_agent_results WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1)`, f.org).Scan(&counts[0], &counts[1], &counts[2]), "read all provisional retained owner/start residue after savepoint rollback")
				require.Equal(t, [3]int{}, counts, "successful probe rollback and refused preparation alike must leave no retained owner or start residue")
			})
		}
	}
	for _, field := range []string{"envelope", "startup_input", "dispatch_wake"} {
		for _, size := range []int{capBytes - 1, capBytes, capBytes + 1} {
			t.Run(fmt.Sprintf("%s presentation bytes %d", field, size), func(t *testing.T) {
				tx := callerSendSchemaBegin(t, ctx, pool)
				f := callerSendSchemaSeed(t, ctx, tx)
				row := callerSendSchemaStartRow(f, "manual", uuid.Nil)
				base := row[field]
				if field == "dispatch_wake" {
					base = callerSendSchemaShape(callerSendSchemaOwners()[0], f, "pending")["wake"]
				}
				var input string
				var actualSize int
				require.NoError(t, tx.QueryRow(ctx, `WITH base AS (SELECT $1::jsonb AS value), sized AS (SELECT value || jsonb_build_object('synthetic_padding',repeat('x',$2-octet_length((value || jsonb_build_object('synthetic_padding',''))::text))) AS value FROM base) SELECT value::text,octet_length(value::text) FROM sized`, base, size).Scan(&input, &actualSize), "construct each separately restored envelope command or wake using the actual JSONB presentation")
				require.Equal(t, size, actualSize, "the separately restored field must hit its exact raw PostgreSQL boundary")
				row[field] = input
				err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					var actual string
					if field == "dispatch_wake" {
						owner := callerSendSchemaOwners()[0]
						command := callerSendSchemaShape(owner, f, "pending")
						command["wake"] = input
						if err := callerSendSchemaInsertOwner(ctx, probe, owner, f, command); err != nil {
							return err
						}
						if err := probe.QueryRow(ctx, `SELECT dispatch_wake::text FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actual); err != nil {
							return err
						}
					} else {
						if err := callerSendSchemaInsertMap(ctx, probe, "review_loop_start_intents", row); err != nil {
							return err
						}
						if err := probe.QueryRow(ctx, `SELECT `+pgx.Identifier{field}.Sanitize()+`::text FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actual); err != nil {
							return err
						}
					}
					require.Equal(t, input, actual, "each accepted retained envelope command and wake must round-trip its entire actual server representation")
					return nil
				})
				if size <= capBytes {
					require.NoError(t, err, "at-cap separately restored fields must remain legal and readable without truncation")
				} else {
					callerSendSchemaSQLState(t, err, "23514", "every separately restored field must reject an actual JSONB presentation above its own raw cap")
				}
			})
		}
	}
}

func callerSendSchemaMigrationRoundTrip(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for version := 389; version >= 384; version-- {
		_, err := pool.Exec(ctx, callerSendSchemaMigration(t, version, "down"))
		require.NoError(t, err, "the actual empty-schema rollback must respect the highest gate before standalone concurrent index drops")
	}
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('caller_send_intent_control') IS NOT NULL`).Scan(&exists), "verify that the actual storage-removal boundary ran")
	require.False(t, exists, "full down must remove the passive caller-send storage rather than only toggle a flag")
	for version := 384; version <= 389; version++ {
		_, err := pool.Exec(ctx, callerSendSchemaMigration(t, version, "up"))
		require.NoError(t, err, "real up after down must recreate the complete validated schema and all standalone concurrent indexes")
	}
	var ready, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "read the actual recreated highest-gate singleton")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "up/down/up must finish ready but never implicitly enable intents")
	for _, direction := range []string{"down", "down", "up", "up"} {
		_, err := pool.Exec(ctx, callerSendSchemaMigration(t, 389, direction))
		require.NoError(t, err, "the highest disable/readiness gate must tolerate repeated down and up without enabling writes")
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "read back both flags after repeated highest-gate operations")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "repeated readiness checks must preserve explicit enablement as a separate authority")
}
