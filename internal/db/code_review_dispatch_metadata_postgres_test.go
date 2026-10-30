package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This factory is the sole old-behavior RED setup seam: an old-code tree can
// replace its body with return NewCodeReviewStore(pool), leaving both schedules
// and every database assertion unchanged. The normal test always injects the
// existing real runner; no test transaction implementation substitutes its work
// or settlement. The first schedule must fail on the uncancelled new child, not
// on compilation, construction, or an unobserved lock wait.
func codeReviewDispatchMetadataPGStore(t *testing.T, pool *pgxpool.Pool, transactions CodeReviewMetadataTransactions) *CodeReviewStore {
	t.Helper()
	store, err := NewCodeReviewStoreWithTerminalTransactions(pool, transactions)
	require.NoError(t, err, "construct the actual pool-owned metadata store with its explicitly supplied existing runner")
	return store
}

type codeReviewDispatchMetadataPGGraph struct {
	metadata, results, wakes json.RawMessage
	sendRows                 [3]int
}

func codeReviewDispatchMetadataPGReadGraph(ctx context.Context, q DBTX, org uuid.UUID, in CodeReviewDispatchPreparation) (codeReviewDispatchMetadataPGGraph, error) {
	var graph codeReviewDispatchMetadataPGGraph
	err := q.QueryRow(ctx, `SELECT to_jsonb(m),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_agent_results r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1 AND j.dedupe_key=$4),'[]'::jsonb),
		(SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2)
		FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$3`, org, in.SessionID, in.MetadataID, "code_review:"+in.ReviewOutputKey).
		Scan(&graph.metadata, &graph.results, &graph.wakes, &graph.sendRows[0], &graph.sendRows[1], &graph.sendRows[2])
	return graph, err
}

func codeReviewDispatchMetadataPGGraphAt(t *testing.T, ctx context.Context, q DBTX, org uuid.UUID, in CodeReviewDispatchPreparation) codeReviewDispatchMetadataPGGraph {
	t.Helper()
	graph, err := codeReviewDispatchMetadataPGReadGraph(ctx, q, org, in)
	require.NoError(t, err, "capture every scoped metadata/result/original-wake field and actual send-linkage cardinality")
	return graph
}

func codeReviewDispatchMetadataPGPool(t *testing.T, ctx context.Context, maxConns int32) (*pgxpool.Pool, *ThreadSendTransactions, uuid.UUID, CodeReviewDispatchPreparation) {
	t.Helper()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", maxConns)
	require.Equal(t, maxConns, pool.Config().MaxConns, "the isolated metadata schedule must retain its explicit checkout cap")
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "enable retained intents only in this isolated full-schema synthetic fixture")
	org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	runner, err := NewThreadSendTransactions(pool)
	require.NoError(t, err, "reuse the existing actual lifecycle transaction runner without a new budget or settlement seam")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, ctx, pool, org, in), "the fixture must contain metadata and lane seeds but no pre-existing result or wake")
	return pool, runner, org, in
}

func codeReviewDispatchMetadataPGBackend(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int32 {
	t.Helper()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err, "warm the sole free competitor checkout before its actual lock budget starts")
	defer conn.Release()
	var pid int32
	require.NoError(t, conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid), "identify the real isolated backend that the competing pool operation must acquire")
	return pid
}

func codeReviewDispatchMetadataPGWait(t *testing.T, ctx context.Context, observer *pgxpool.Conn, competitor, holder int32, finished <-chan struct{}) {
	t.Helper()
	require.NotEqual(t, holder, competitor, "the exact competitor must not reuse the actual held metadata transaction")
	var blocked bool
	for !blocked {
		select {
		case <-finished:
			require.FailNow(t, "the competing operation must be positively observed waiting before it finishes")
		case <-ctx.Done():
			require.FailNow(t, "the actual metadata wait must be proven within the existing held runner's two-second unit")
		default:
		}
		err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
			WHERE a.datname=current_database() AND a.pid=$1::integer
			AND a.state='active' AND a.wait_event_type='Lock'
			AND $2::integer=ANY(pg_blocking_pids(a.pid)))`, competitor, holder).Scan(&blocked)
		require.NoError(t, err, "read fresh catalog evidence for this exact isolated-database competitor and its actual metadata holder")
	}
	require.True(t, blocked, "only a real holder-specific PostgreSQL Lock wait permits the first owner to commit")
}

func codeReviewDispatchMetadataPGObserver(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()
	observer, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve one independent catalog observer while leaving two checkouts for actual owners")
	t.Cleanup(observer.Release)
	var pid int32
	require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid), "warm the observer before either contended production statement starts")
	return observer
}

func codeReviewDispatchMetadataPGJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err, "encode only complete synthetic expected metadata or dispatch values")
	return raw
}

func codeReviewDispatchMetadataPGTime(t *testing.T, raw json.RawMessage) time.Time {
	t.Helper()
	var value time.Time
	require.NoError(t, json.Unmarshal(raw, &value), "read an actual database timestamp from the synthetic full-row snapshot")
	require.False(t, value.IsZero(), "an intentional database timestamp change must not be replaced with NULL or a zero time")
	return value
}

func codeReviewDispatchMetadataPGCancelled(t *testing.T, before, after codeReviewDispatchMetadataPGGraph, reason string, returned models.CodeReviewSessionMetadata) {
	t.Helper()
	var expected, actual map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before.metadata, &expected), "retain every original metadata column as the exact baseline")
	require.NoError(t, json.Unmarshal(after.metadata, &actual), "decode every actual terminal metadata column")
	expected["status"] = codeReviewDispatchMetadataPGJSON(t, "cancelled")
	expected["failure_reason"] = codeReviewDispatchMetadataPGJSON(t, reason)
	for _, key := range []string{"phase", "status_code", "status_message", "retry_at", "last_error_at"} {
		expected[key] = json.RawMessage("null")
	}
	expected["retryable_failure"] = json.RawMessage("false")
	completed := codeReviewDispatchMetadataPGTime(t, actual["completed_at"])
	require.NotNil(t, returned.CompletedAt, "the known committed metadata result must carry the actual completion timestamp")
	require.True(t, completed.Equal(*returned.CompletedAt), "the returned completion must equal the independently read database clock exactly")
	expected["completed_at"] = actual["completed_at"]
	require.JSONEq(t, string(codeReviewDispatchMetadataPGJSON(t, expected)), string(after.metadata), "CancelReview may change only its original documented terminal metadata fields")
	var expectedMetadata models.CodeReviewSessionMetadata
	require.NoError(t, json.Unmarshal(codeReviewDispatchMetadataPGJSON(t, expected), &expectedMetadata), "restore the complete expected public metadata value from the independently captured exact row")
	require.True(t, expectedMetadata.CreatedAt.Equal(returned.CreatedAt), "the returned metadata must preserve its original database creation instant")
	expectedMetadata.CreatedAt = returned.CreatedAt
	expectedMetadata.CompletedAt = returned.CompletedAt
	require.Equal(t, expectedMetadata, returned, "the actual store must return the complete exact metadata value after known settlement, not a partial cancellation descriptor")
	require.Equal(t, [3]int{}, after.sendRows, "metadata terminalization cannot send, install an inbox entry, or manufacture accepted-send stamps")
}

func codeReviewDispatchMetadataPGResultCancelled(t *testing.T, staged, after codeReviewDispatchMetadataPGGraph) {
	t.Helper()
	var expected, actual []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(staged.results, &expected), "decode the actual newly inserted complete pending result before its first commit")
	require.NoError(t, json.Unmarshal(after.results, &actual), "decode every complete committed reviewer result")
	require.Len(t, expected, 1, "the installer must have actually inserted one new child, not updated an already visible row")
	require.Len(t, actual, 1, "terminalization must retain exactly that original reviewer rather than replace or duplicate it")
	oldClock := codeReviewDispatchMetadataPGTime(t, expected[0]["dispatch_updated_at"])
	newClock := codeReviewDispatchMetadataPGTime(t, actual[0]["dispatch_updated_at"])
	require.False(t, newClock.Before(oldClock), "the later cancellation statement must preserve monotone dispatch update history")
	expected[0]["dispatch_state"] = codeReviewDispatchMetadataPGJSON(t, "cancelled")
	expected[0]["dispatch_due_at"] = json.RawMessage("null")
	expected[0]["dispatch_resolution"] = codeReviewDispatchMetadataPGJSON(t, "owner_cancelled")
	expected[0]["dispatch_writer_seq"] = json.RawMessage("1")
	expected[0]["dispatch_updated_at"] = actual[0]["dispatch_updated_at"]
	require.JSONEq(t, string(codeReviewDispatchMetadataPGJSON(t, expected)), string(after.results), "the new child must settle only pending admission, retaining every business field, original attempt field, immutable command, lane, digest and wake")
	require.JSONEq(t, string(staged.wakes), string(after.wakes), "cancellation must preserve the entire exact original wake row without rescheduling or creating a replacement")
}

func TestCodeReviewDispatchMetadataNewChildCancellationPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pool, runner, org, in := codeReviewDispatchMetadataPGPool(t, ctx, 3)
	baseline := codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in)
	observer := codeReviewDispatchMetadataPGObserver(t, ctx, pool)
	type heldUnit struct {
		ctx      context.Context
		pid      int32
		prepared PreparedCodeReviewDispatch
		graph    codeReviewDispatchMetadataPGGraph
	}
	held, release := make(chan heldUnit, 1), make(chan struct{})
	installDone, installFinished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(installFinished)
		installDone <- runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
			prepared, err := PrepareCodeReviewDispatchInTx(unitCtx, org, tx, in)
			if err != nil {
				return err
			}
			graph, err := codeReviewDispatchMetadataPGReadGraph(unitCtx, tx, org, in)
			if err != nil {
				return err
			}
			var pid int32
			if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			held <- heldUnit{ctx: unitCtx, pid: pid, prepared: prepared, graph: graph}
			select {
			case <-release:
				return nil
			case <-unitCtx.Done():
				return unitCtx.Err()
			}
		})
	}()
	t.Cleanup(func() { cancel(); <-installFinished })
	var installer heldUnit
	select {
	case installer = <-held:
	case err := <-installDone:
		require.FailNow(t, "the actual installer must stage its fresh child and wake before any terminalizer statement", "installer outcome: %v", err)
	}
	require.Equal(t, in.ResultID, installer.prepared.Result().ID, "the staged actual result must use the original independently preminted reviewer identity")
	require.True(t, installer.prepared.WakeInserted(), "the actual prepare statement must insert the original wake before the competing terminal statement begins")
	require.NotEqual(t, uuid.Nil, installer.prepared.WakeJobID(), "the staged wake must have an actual database job identity")
	require.JSONEq(t, string(baseline.metadata), string(installer.graph.metadata), "installation must hold the exact metadata owner without mutating its business state")
	var stagedRows, stagedWakes []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(installer.graph.results, &stagedRows), "inspect the actually staged fresh reviewer before cancellation starts")
	require.NoError(t, json.Unmarshal(installer.graph.wakes, &stagedWakes), "inspect the actually staged original job before cancellation starts")
	require.Len(t, stagedRows, 1, "the installer must have inserted a genuinely new reviewer before the first terminalizer statement")
	require.Len(t, stagedWakes, 1, "the same installer unit must have inserted its original wake before any cancellation query")
	require.Equal(t, json.RawMessage(`"pending"`), stagedRows[0]["dispatch_state"], "the actual newly installed child must still be pending when the terminalizer's statement snapshot is taken")
	require.Equal(t, json.RawMessage("0"), stagedRows[0]["dispatch_writer_seq"], "the fresh child must have no previous dispatch transition or hidden cancellation")
	require.Equal(t, json.RawMessage("0"), stagedRows[0]["dispatch_attempt_seq"], "the fresh child must have no previously started send attempt")
	require.Equal(t, json.RawMessage("null"), stagedRows[0]["dispatch_attempt_id"], "a pending installation must not fabricate an original send attempt")
	require.Equal(t, codeReviewDispatchMetadataPGJSON(t, installer.prepared.WakeJobID()), stagedWakes[0]["id"], "the actual staged original wake must match the preparation carrier's exact database identity")
	require.Equal(t, [2]int{0, 0}, codeReviewDispatchPreparePGCounts(t, installer.ctx, observer, org, in), "the new child and wake must be genuinely uncommitted and absent from the terminalizer's original snapshot")
	terminalPID := codeReviewDispatchMetadataPGBackend(t, installer.ctx, pool)
	store := codeReviewDispatchMetadataPGStore(t, pool, runner)
	type outcome struct {
		metadata models.CodeReviewSessionMetadata
		err      error
	}
	done, finished := make(chan outcome, 1), make(chan struct{})
	const reason = "synthetic metadata cancellation after new reviewer installation"
	go func() {
		defer close(finished)
		metadata, err := store.CancelReview(ctx, org, in.SessionID, reason)
		done <- outcome{metadata: metadata, err: err}
	}()
	t.Cleanup(func() { cancel(); <-finished })
	codeReviewDispatchMetadataPGWait(t, installer.ctx, observer, terminalPID, installer.pid, finished)
	outside := codeReviewDispatchMetadataPGGraphAt(t, installer.ctx, observer, org, in)
	require.Equal(t, baseline, outside, "before installer release the blocked terminalizer must expose neither metadata change nor the fresh child/wake outside their actual transactions")
	close(release)
	require.NoError(t, <-installDone, "only the existing actual runner may commit the installed child and original wake")
	actual := <-done
	require.NoError(t, actual.err, "the actual terminalizer must resume after the proven metadata wait and settle a later fresh child statement in the same committed unit")
	require.Equal(t, models.CodeReviewSessionStatusCancelled, actual.metadata.Status, "the known committed real CancelReview result must remain ordinary cancelled metadata")
	after := codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in)
	codeReviewDispatchMetadataPGCancelled(t, baseline, after, reason, actual.metadata)
	codeReviewDispatchMetadataPGResultCancelled(t, installer.graph, after)
}

func TestCodeReviewDispatchMetadataTerminalizationFirstPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pool, runner, org, in := codeReviewDispatchMetadataPGPool(t, ctx, 3)
	baseline := codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in)
	observer := codeReviewDispatchMetadataPGObserver(t, ctx, pool)
	type heldUnit struct {
		ctx      context.Context
		pid      int32
		metadata models.CodeReviewSessionMetadata
	}
	held, release := make(chan heldUnit, 1), make(chan struct{})
	done, finished := make(chan error, 1), make(chan struct{})
	const reason = "synthetic terminal metadata before reviewer installation"
	go func() {
		defer close(finished)
		done <- runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
			store, err := NewCodeReviewStoreInTx(tx)
			if err != nil {
				return err
			}
			metadata, err := store.CancelReview(unitCtx, org, in.SessionID, reason)
			if err != nil {
				return err
			}
			var pid int32
			if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			held <- heldUnit{ctx: unitCtx, pid: pid, metadata: metadata}
			select {
			case <-release:
				return nil
			case <-unitCtx.Done():
				return unitCtx.Err()
			}
		})
	}()
	t.Cleanup(func() { cancel(); <-finished })
	var terminalizer heldUnit
	select {
	case terminalizer = <-held:
	case err := <-done:
		require.FailNow(t, "the real supplied-tx terminalizer must hold actual metadata change and pending settlement before installation", "terminalizer outcome: %v", err)
	}
	require.Equal(t, models.CodeReviewSessionStatusCancelled, terminalizer.metadata.Status, "the held real owner unit must have actually changed metadata before the installer starts")
	installPID := codeReviewDispatchMetadataPGBackend(t, terminalizer.ctx, pool)
	type outcome struct {
		prepared PreparedCodeReviewDispatch
		err      error
	}
	installDone, installFinished := make(chan outcome, 1), make(chan struct{})
	go func() {
		defer close(installFinished)
		var prepared PreparedCodeReviewDispatch
		err := runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
			prepared = PreparedCodeReviewDispatch{}
			var err error
			prepared, err = PrepareCodeReviewDispatchInTx(unitCtx, org, tx, in)
			return err
		})
		if err != nil {
			prepared = PreparedCodeReviewDispatch{}
		}
		installDone <- outcome{prepared: prepared, err: err}
	}()
	t.Cleanup(func() { cancel(); <-installFinished })
	codeReviewDispatchMetadataPGWait(t, terminalizer.ctx, observer, installPID, terminalizer.pid, installFinished)
	outside := codeReviewDispatchMetadataPGGraphAt(t, terminalizer.ctx, observer, org, in)
	require.Equal(t, baseline, outside, "a held actual terminal transaction must expose no metadata change and the blocked installer must add no child or wake")
	close(release)
	require.NoError(t, <-done, "the original real transaction owner must commit metadata cancellation before any new installer can pass its share-lock recheck")
	actual := <-installDone
	require.ErrorIs(t, actual.err, ErrCodeReviewDispatchPreparationIneligible, "the actual post-wait preparation statement must refuse the now-terminal owner, not insert under its old statement snapshot")
	require.Equal(t, PreparedCodeReviewDispatch{}, actual.prepared, "post-wait refusal cannot expose a reviewer or replacement wake")
	after := codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in)
	codeReviewDispatchMetadataPGCancelled(t, baseline, after, reason, terminalizer.metadata)
	require.JSONEq(t, string(baseline.results), string(after.results), "terminalization-first ordering must retain the exact empty child set without a pending orphan")
	require.JSONEq(t, string(baseline.wakes), string(after.wakes), "terminalization-first ordering must not enqueue the blocked installer's original wake")
}

func TestCodeReviewDispatchMetadataOuterRollbackPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pool, runner, org, in := codeReviewDispatchMetadataPGPool(t, ctx, 1)
	baseline := codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in)
	rollbackCause := errors.New("synthetic known outer rollback after real pending settlement")
	var staged codeReviewDispatchMetadataPGGraph
	err := runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		staged = codeReviewDispatchMetadataPGGraph{}
		if _, err := PrepareCodeReviewDispatchInTx(unitCtx, org, tx, in); err != nil {
			return err
		}
		store, err := NewCodeReviewStoreInTx(tx)
		if err != nil {
			return err
		}
		if _, err := store.CancelReview(unitCtx, org, in.SessionID, "synthetic whole-unit rollback"); err != nil {
			return err
		}
		staged, err = codeReviewDispatchMetadataPGReadGraph(unitCtx, tx, org, in)
		if err != nil {
			return err
		}
		return rollbackCause
	})
	require.ErrorIs(t, err, rollbackCause, "only the real outer runner may roll back the actual metadata/result/wake unit after its callback fails")
	var stagedMetadata map[string]json.RawMessage
	var stagedResults []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(staged.metadata, &stagedMetadata), "prove the rollback reached actual metadata mutation rather than a configuration error")
	require.NoError(t, json.Unmarshal(staged.results, &stagedResults), "prove the rollback reached actual newly installed child settlement")
	require.Equal(t, json.RawMessage(`"cancelled"`), stagedMetadata["status"], "the real supplied-tx store must have staged metadata cancellation before the injected application error")
	require.Len(t, stagedResults, 1, "the same one-checkout unit must have actually inserted and then settled its original reviewer")
	require.Equal(t, json.RawMessage(`"cancelled"`), stagedResults[0]["dispatch_state"], "the same transaction must have reached actual private pending cancellation before rollback")
	require.Equal(t, baseline, codeReviewDispatchMetadataPGGraphAt(t, ctx, pool, org, in), "known outer rollback must restore the exact complete metadata and empty result/wake/send graph on the same one-checkout fixture")
}
