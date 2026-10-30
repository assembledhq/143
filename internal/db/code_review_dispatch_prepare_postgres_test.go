package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// One root owns one fully migrated disposable database and at most three
// checkouts. Cases are sequential because they mutate the database-wide control
// singleton and exercise metadata lock ordering. This is installation/storage
// proof only: no send, controller, ambiguous commit or activation is exercised.
//
//nolint:paralleltest // Subcases share the control singleton and bounded lock-order fixture.
func TestCodeReviewDispatchPreparationPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
	require.Equal(t, int32(3), pool.Config().MaxConns, "installation fixtures must preserve the explicit three-connection budget")
	var version int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version), "read actual test server prerequisites")
	require.GreaterOrEqual(t, version, 150000, "the full unmodified migration chain requires PostgreSQL 15 or newer")
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "enable only this isolated synthetic database for retained-write tests")
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgxpool.Pool)
	}{
		{name: "exact result and original wake", run: codeReviewDispatchPreparePGExact},
		{name: "ordinary foreign dedupe collision", run: codeReviewDispatchPreparePGDedupe},
		{name: "outer rollback includes checkpoint", run: codeReviewDispatchPreparePGRollback},
		{name: "wake insert failure rolls back result statement", run: codeReviewDispatchPreparePGWakeFailure},
		{name: "fresh-only never upgrades a legacy row", run: codeReviewDispatchPreparePGLegacy},
		{name: "disabled missing and scoped owner refusals", run: codeReviewDispatchPreparePGRefusals},
		{name: "actual command and wake JSONB limits", run: codeReviewDispatchPreparePGJSONBBounds},
		{name: "metadata serialization and post-wait recheck", run: codeReviewDispatchPreparePGLocks},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseCtx, caseCancel := context.WithTimeout(ctx, 45*time.Second)
			defer caseCancel()
			tt.run(t, caseCtx, pool)
		})
	}
}

func codeReviewDispatchPreparePGFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, CodeReviewDispatchPreparation) {
	t.Helper()
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	org, session, primary := callerSendStampPGSession(t, ctx, tx, uuid.Nil)
	_, in := newCodeReviewDispatchPreparation(t)
	in.ResultID, in.SessionID, in.ThreadID, in.MetadataID = uuid.New(), session, primary, uuid.New()
	in.AgentModel = nil // The actual primary lane has no model override.
	in.Key = "cr-dispatch:v1:" + in.ResultID.String()
	in.ReviewOutputKey = "synthetic-original:" + in.MetadataID.String()
	codeReviewDispatchPreparationJSON(t, org, &in)
	integration, repo, pr, policy := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// Cases run sequentially in one disposable database. GitHub repository IDs
	// are globally unique, so each synthetic tenant needs a distinct test ID.
	rows := []struct {
		query string
		args  []any
	}{
		{`UPDATE sessions SET origin='code_review' WHERE org_id=$1 AND id=$2`, []any{org, session}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(SELECT COALESCE(max(github_id),0)+1 FROM repositories),'synthetic/reviewer','https://example.invalid/reviewer.git',1)`, []any{repo, org, integration}},
		{`INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,$3,1,'https://example.invalid/pr/1','synthetic/reviewer','Synthetic review','synthetic-head','synthetic-base')`, []any{pr, org, session}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy) VALUES($1,$2,1,'comment_only','{}','{}','{}','','')`, []any{policy, org}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'synthetic-base','synthetic-head','slash_command','running',$7)`, []any{in.MetadataID, org, session, repo, pr, policy, in.ReviewOutputKey}},
	}
	for _, row := range rows {
		_, err := tx.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed actual scoped synthetic parents through the unmodified full schema")
	}
	require.NoError(t, tx.Commit(ctx), "retain only the synthetic owner and lane before the caller's separate installation transaction")
	return org, in
}

func codeReviewDispatchPreparePGCounts(t *testing.T, ctx context.Context, q DBTX, org uuid.UUID, in CodeReviewDispatchPreparation) [2]int {
	t.Helper()
	var counts [2]int
	require.NoError(t, q.QueryRow(ctx, `SELECT (SELECT count(*) FROM code_review_agent_results WHERE org_id=$1 AND id=$2),(SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$3)`, org, in.ResultID, "code_review:"+in.ReviewOutputKey).Scan(&counts[0], &counts[1]), "count only this fixture's scoped result and original wake")
	return counts
}

func codeReviewDispatchPreparePGExact(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	prepared, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.NoError(t, err, "install one exact fresh reviewer intent and original wake")
	require.True(t, prepared.WakeInserted(), "the initial wake must report a genuine insertion")
	require.NotEqual(t, uuid.Nil, prepared.WakeJobID(), "a real wake insertion must retain its actual identity")
	actual := prepared.Result()
	expected := codeReviewDispatchPreparationExpected(in, org)
	expected.CreatedAt = actual.CreatedAt
	actualJSON, marshalErr := json.Marshal(actual)
	require.NoError(t, marshalErr, "encode only the public business result")
	expectedJSON, marshalErr := json.Marshal(expected)
	require.NoError(t, marshalErr, "encode the complete expected business result")
	require.JSONEq(t, string(expectedJSON), string(actualJSON), "all original business fields must be preserved without private intent exposure")
	var command, wake, payload, shape json.RawMessage
	var queue, kind, dedupe string
	var priority, maxAttempts int
	require.NoError(t, tx.QueryRow(ctx, `SELECT dispatch_input,dispatch_wake,jsonb_build_array(dispatch_version,dispatch_key,dispatch_digest,dispatch_thread_id,dispatch_state,dispatch_attempt_id,dispatch_attempt_seq,dispatch_attempted_at,dispatch_due_at IS NOT NULL,dispatch_observe_deadline,dispatch_attention_at,dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,dispatch_writer_seq,dispatch_updated_at IS NOT NULL),j.queue,j.job_type,j.payload,j.priority,j.dedupe_key,j.max_attempts FROM code_review_agent_results r JOIN jobs j ON j.org_id=r.org_id AND j.id=$3 WHERE r.org_id=$1 AND r.id=$2`, org, in.ResultID, prepared.WakeJobID()).Scan(&command, &wake, &shape, &queue, &kind, &payload, &priority, &dedupe, &maxAttempts), "read the actual retained JSONB, all state fields and actual wake row")
	require.JSONEq(t, string(in.Input), string(command), "storage must retain the original complete command rather than regenerate it")
	require.JSONEq(t, string(in.Wake), string(wake), "storage must preserve the entire frozen original wake envelope")
	var originalWake map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(in.Wake, &originalWake), "decode the expected frozen synthetic payload")
	require.JSONEq(t, string(originalWake["payload"]), string(payload), "the jobs payload must retain all nested and optional original controller fields")
	expectedShape, marshalErr := json.Marshal([]any{1, in.Key, in.Digest, in.ThreadID, "pending", nil, 0, nil, true, nil, nil, nil, nil, nil, nil, 0, true})
	require.NoError(t, marshalErr, "encode the complete expected fresh pending state")
	require.JSONEq(t, string(expectedShape), string(shape), "fresh installation must contain the exact zero-attempt zero-writer pending shape with database timestamps")
	require.Equal(t, []any{"agent", "run_code_review", 5, "code_review:" + in.ReviewOutputKey, 8}, []any{queue, kind, priority, dedupe, maxAttempts}, "wake enqueue must match the actual existing controller scheduling options")
	require.NoError(t, tx.Commit(ctx), "only the outer test owner may acknowledge installation commit")
	require.Equal(t, [2]int{1, 1}, codeReviewDispatchPreparePGCounts(t, ctx, pool, org, in), "known committed installation must retain exactly its result and wake")
}

func codeReviewDispatchPreparePGDedupe(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	foreign, existing := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic foreign wake tenant')`, foreign)
	require.NoError(t, err, "seed only synthetic foreign dedupe scope")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,dedupe_key) VALUES($1,$2,'agent','synthetic_existing','{"retain":"original foreign payload"}',$3)`, existing, foreign, "code_review:"+in.ReviewOutputKey)
	require.NoError(t, err, "retain an ordinary global queue/key collision without adopting it")
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	prepared, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.NoError(t, err, "ordinary jobs dedupe semantics must not turn collision into owner adoption")
	require.False(t, prepared.WakeInserted(), "a conflicting foreign row must not be called a newly inserted wake")
	require.Equal(t, uuid.Nil, prepared.WakeJobID(), "dedupe must expose no candidate or foreign consumer identity")
	require.NoError(t, tx.Commit(ctx), "the caller may commit installation with the explicitly reported ordinary dedupe no-op")
	require.Equal(t, [2]int{1, 0}, codeReviewDispatchPreparePGCounts(t, ctx, pool, org, in), "the caller result may exist without inventing a locally owned wake")
	var unchanged bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT job_type='synthetic_existing' AND payload='{"retain":"original foreign payload"}'::jsonb FROM jobs WHERE org_id=$1 AND id=$2`, foreign, existing).Scan(&unchanged), "read the original foreign job without publishing its data")
	require.True(t, unchanged, "a wake collision must not rewrite or adopt the existing payload")
}

func codeReviewDispatchPreparePGRollback(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	_, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET prompt_record_key='synthetic staged checkpoint' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "stage a caller checkpoint only inside its owned outer unit")
	_, err = PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.NoError(t, err, "prepare only provisional result and wake inside that same unit")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, pool, org, in), "another checkout must not see uncommitted result or wake")
	require.NoError(t, tx.Rollback(ctx), "the caller must be able to roll back result wake and caller checkpoint together")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, pool, org, in), "rollback must leave neither a result nor a wake")
	var checkpoint *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT prompt_record_key FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, org, in.MetadataID).Scan(&checkpoint), "read the caller checkpoint after rollback")
	require.Nil(t, checkpoint, "outer rollback must not strand a caller checkpoint without its installation")
}

func codeReviewDispatchPreparePGWakeFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	_, err := tx.Exec(ctx, `ALTER TABLE jobs ADD CONSTRAINT synthetic_dispatch_wake_refusal CHECK(job_type<>'run_code_review') NOT VALID`)
	require.NoError(t, err, "add a transaction-local synthetic wake refusal without disabling any real guard")
	_, err = tx.Exec(ctx, `SAVEPOINT synthetic_wake_failure`)
	require.NoError(t, err, "retain an inspection point before the atomic install statement")
	actual, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationStorage, "a real wake insertion refusal must fail the entire install statement")
	var cause *pgconn.PgError
	require.True(t, errors.As(err, &cause), "retain the original PostgreSQL cause privately for classification")
	require.Equal(t, "23514", cause.Code, "the test must exercise the actual synthetic CHECK refusal")
	require.Equal(t, "synthetic_dispatch_wake_refusal", cause.ConstraintName, "the intended wake CHECK must be the cause rather than an unrelated constraint")
	require.Equal(t, PreparedCodeReviewDispatch{}, actual, "no provisional result may escape the refused statement")
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT synthetic_wake_failure`)
	require.NoError(t, err, "recover only the test inspection savepoint after the expected statement refusal")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "a failed wake must not leave the result CTE partially installed")
	require.NoError(t, tx.Rollback(ctx), "remove the synthetic test constraint by ordinary outer rollback")
}

func codeReviewDispatchPreparePGLegacy(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	_, err := tx.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,raw_output,structured_result) VALUES($1,$2,$3,$4,'reviewer','queued',$5,$6::jsonb)`, in.ResultID, org, in.SessionID, in.AgentProvider, in.RawOutput, []byte(in.StructuredResult))
	require.NoError(t, err, "retain a truthful all-NULL legacy row at the original preminted result identity")
	_, err = tx.Exec(ctx, `SAVEPOINT synthetic_legacy_conflict`)
	require.NoError(t, err, "isolate the expected primary-key conflict for post-error inspection")
	_, err = PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
	require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationStorage, "fresh installation must not update or adopt a legacy row")
	var cause *pgconn.PgError
	require.True(t, errors.As(err, &cause), "retain the actual uniqueness refusal")
	require.Equal(t, "23505", cause.Code, "the original result identity must cause a genuine uniqueness refusal")
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT synthetic_legacy_conflict`)
	require.NoError(t, err, "inspect unchanged legacy data after statement rollback")
	var nulls int
	var preserved bool
	require.NoError(t, tx.QueryRow(ctx, `SELECT num_nulls(dispatch_version,dispatch_key,dispatch_input,dispatch_digest,dispatch_thread_id,dispatch_state,dispatch_attempt_id,dispatch_attempt_seq,dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,dispatch_attention_at,dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,dispatch_writer_seq,dispatch_updated_at,dispatch_wake),raw_output IS NOT DISTINCT FROM $3::text AND structured_result IS NOT DISTINCT FROM $4::jsonb FROM code_review_agent_results WHERE org_id=$1 AND id=$2`, org, in.ResultID, in.RawOutput, []byte(in.StructuredResult)).Scan(&nulls, &preserved), "inspect all nineteen private fields and the complete legacy business output")
	require.Equal(t, 19, nulls, "preparation must never turn legacy NULLs into a new intent")
	require.True(t, preserved, "a conflict must not rewrite the original legacy business result")
	require.Equal(t, [2]int{1, 0}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "a legacy identity collision must not enqueue any wake")
}

func codeReviewDispatchPreparePGRefusals(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	tests := []struct {
		name     string
		mutation string
		edit     func(*testing.T, uuid.UUID, *CodeReviewDispatchPreparation)
		want     error
	}{
		{name: "disabled control", mutation: `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton`, want: ErrCodeReviewDispatchPreparationDisabled},
		{name: "absent control", mutation: `DELETE FROM caller_send_intent_control WHERE singleton`, want: ErrCodeReviewDispatchPreparationDisabled},
		{name: "completed owner", mutation: `UPDATE code_review_session_metadata SET status='completed' WHERE org_id=$1 AND id=$2`, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "stale owner", mutation: `UPDATE code_review_session_metadata SET stale=true WHERE org_id=$1 AND id=$2`, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "missing exact metadata", edit: func(t *testing.T, org uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.MetadataID = uuid.New()
			codeReviewDispatchPreparationJSON(t, org, in)
		}, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "wrong original output", edit: func(t *testing.T, org uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.ReviewOutputKey = "synthetic-other-output"
			codeReviewDispatchPreparationJSON(t, org, in)
		}, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "missing exact lane", edit: func(t *testing.T, org uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.ThreadID = uuid.New()
			codeReviewDispatchPreparationJSON(t, org, in)
		}, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "real foreign tenant lane", edit: func(t *testing.T, org uuid.UUID, in *CodeReviewDispatchPreparation) {
			foreignTx := callerSendStampPGBegin(t, ctx, pool, false)
			_, _, foreignThread := callerSendStampPGSession(t, ctx, foreignTx, uuid.Nil)
			require.NoError(t, foreignTx.Commit(ctx), "retain a real foreign tenant lane without changing the original metadata owner")
			in.ThreadID = foreignThread
			codeReviewDispatchPreparationJSON(t, org, in)
		}, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "wrong lane model", edit: func(_ *testing.T, _ uuid.UUID, in *CodeReviewDispatchPreparation) {
			model := "synthetic-other-model"
			in.AgentModel = &model
		}, want: ErrCodeReviewDispatchPreparationIneligible},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential: singleton mutations and all parent edits must roll back.
			org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			if tt.mutation != "" {
				var err error
				if strings.Contains(tt.mutation, "$1") {
					_, err = tx.Exec(ctx, tt.mutation, org, in.MetadataID)
				} else {
					_, err = tx.Exec(ctx, tt.mutation)
				}
				require.NoError(t, err, "apply only this case's synthetic ineligibility")
			}
			if tt.edit != nil {
				tt.edit(t, org, &in)
			}
			actual, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
			require.ErrorIs(t, err, tt.want, "inactive disabled missing or differently scoped owners must refuse without fallback")
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "refusal must expose no provisional owner or consumer")
			require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "known refusal must write neither result nor wake")
			require.NoError(t, tx.Rollback(ctx), "restore singleton and owner shape after this synthetic case")
		})
	}
}

func codeReviewDispatchPreparePGJSONBBounds(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, part := range []string{"command", "wake"} {
		t.Run(part, func(t *testing.T) {
			// Sequential: large presentation checks share this one bounded pool.
			org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
			// An array of short strings has a compact representation below 8MiB,
			// while PostgreSQL adds separator spaces beyond 8MiB. Scope fixtures
			// here exercise actual storage bounds, not semantic codec acceptance.
			array := json.RawMessage("[" + strings.Repeat(`"x",`, 1800000) + `"x"]`)
			if part == "command" {
				in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "images", array)
			} else {
				var wake map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(in.Wake, &wake), "decode the unchanged scoped synthetic wake")
				payload := codeReviewDispatchPreparationReplace(t, wake["payload"], "synthetic_retained_array", array)
				in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "payload", payload)
			}
			raw := in.Input
			if part == "wake" {
				raw = in.Wake
			}
			require.LessOrEqual(t, len(raw), codeReviewDispatchPreparationMaxBytes, "the original compact envelope must fit before exercising PostgreSQL expansion")
			var actualBytes int
			require.NoError(t, pool.QueryRow(ctx, `SELECT octet_length(($1::jsonb)::text)`, []byte(raw)).Scan(&actualBytes), "measure the actual PostgreSQL representation rather than predict it in Go")
			require.Greater(t, actualBytes, codeReviewDispatchPreparationMaxBytes, "the test must reach the actual JSONB-only oversize boundary")
			tx := callerSendStampPGBegin(t, ctx, pool, false)
			_, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET prompt_record_key='synthetic staged checkpoint' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
			require.NoError(t, err, "stage caller checkpoint only inside the outer transaction")
			actual, err := PrepareCodeReviewDispatchInTx(ctx, org, tx, in)
			require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationTooLarge, "actual JSONB oversize must be a known pre-intent refusal without truncation or fallback")
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "oversize must not expose a partial installation")
			require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, tx, org, in), "both data-modifying CTEs must remain empty when either envelope is too large")
			require.NoError(t, tx.Rollback(ctx), "the outer owner must roll back any staged checkpoint on known preparation refusal")
			var checkpoint *string
			require.NoError(t, pool.QueryRow(ctx, `SELECT prompt_record_key FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, org, in.MetadataID).Scan(&checkpoint), "read only the safe checkpoint presence after rollback")
			require.Nil(t, checkpoint, "oversize must not leave a committed caller checkpoint")
		})
	}
}

func codeReviewDispatchPreparePGLocks(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	install := callerSendStampPGBegin(t, ctx, pool, false)
	_, err := PrepareCodeReviewDispatchInTx(ctx, org, install, in)
	require.NoError(t, err, "the installer must retain the exact active metadata share lock until outer settlement")
	cancelTx := callerSendStampPGBegin(t, ctx, pool, false)
	_, err = cancelTx.Exec(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err, "bound only this test cancellation wait without changing production budgets")
	_, err = cancelTx.Exec(ctx, `UPDATE code_review_session_metadata SET status='cancelled' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	var blocked *pgconn.PgError
	require.True(t, errors.As(err, &blocked), "the real metadata mutation must conflict with the installer's retained lock")
	require.Equal(t, "55P03", blocked.Code, "the lock proof must be a genuine PostgreSQL lock timeout")
	require.NoError(t, cancelTx.Rollback(ctx), "release the synthetic blocked cancellation transaction")
	require.NoError(t, install.Rollback(ctx), "discard the provisional installation before testing cancellation-first ordering")

	cancelTx = callerSendStampPGBegin(t, ctx, pool, false)
	_, err = cancelTx.Exec(ctx, `UPDATE code_review_session_metadata SET status='cancelled' WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "hold an uncommitted cancellation before the installer's metadata read")
	install = callerSendStampPGBegin(t, ctx, pool, false)
	var installerPID int32
	require.NoError(t, install.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&installerPID), "retain only the isolated test backend identity for lock observation")
	type outcome struct {
		value PreparedCodeReviewDispatch
		err   error
	}
	done := make(chan outcome, 1)
	prepareCtx, stopPrepare := context.WithCancel(ctx)
	joined := false
	defer func() {
		stopPrepare()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("isolated installer did not join after cancellation")
			}
		}
	}()
	go func() {
		value, prepareErr := PrepareCodeReviewDispatchInTx(prepareCtx, org, install, in)
		done <- outcome{value, prepareErr}
	}()
	// At most three checkouts: cancellation, installer, and this observer.
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, installerPID).Scan(&waiting)
		return queryErr == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "observe the actual installer lock wait before allowing cancellation to commit")
	require.NoError(t, cancelTx.Commit(ctx), "commit cancellation while the exact metadata-locking statement waits")
	var got outcome
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("installer did not return after the isolated metadata lock was released")
	}
	require.ErrorIs(t, got.err, ErrCodeReviewDispatchPreparationIneligible, "READ COMMITTED lock recheck must reject the owner that became cancelled while waiting")
	require.Equal(t, PreparedCodeReviewDispatch{}, got.value, "post-wait ineligibility must expose no provisional work")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, install, org, in), "cancellation-first ordering must install neither result nor wake")
	require.NoError(t, install.Rollback(ctx), "release the refused installer's supplied transaction")
}
