package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const callerDispatchStatusLockMetadataWriter = `UPDATE code_review_session_metadata m SET status='failed',status_code='reviewer_failed',status_message='Synthetic metadata-first terminal writer' WHERE m.org_id=$1 AND m.id=$2 RETURNING to_jsonb(m)`
const callerDispatchStatusLockResultWriter = `UPDATE code_review_agent_results r SET status='failed',raw_output='Synthetic terminal result writer' WHERE r.org_id=$1 AND r.session_id=$2 AND r.id=$3 RETURNING to_jsonb(r)`

// Each sequential case owns one disposable latest-schema clone and exactly
// three checkouts: holder, contender, warmed observer. These are real SQL lock
// schedules for the metadata-then-results writer pattern, not controller/send
// execution or migration-driver ledger proofs. All race changes roll back.
//
//nolint:paralleltest // Keep each three-checkout DDL lock schedule sequential so its fixture is dropped before the next schedule.
func TestCallerDispatchStatusMetadataResultLocksPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		version     int
		writerFirst bool
	}{
		{name: "highest_392_writer_first", version: 392, writerFirst: true},
		{name: "lowest_390_writer_first", version: 390, writerFirst: true},
		{name: "highest_392_down_first", version: 392},
		{name: "lowest_390_down_first", version: 390},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sequential DDL cases release/drop their entire fixture before the
			// next case; no fourth client or parallel schema mutation is needed.
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "each metadata/result lock schedule must retain the literal three-checkout cap")
			org, in := codeReviewDispatchPreparePGFixture(t, ctx, pool)
			resultID := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,structured_result) VALUES($1,$2,$3,$4,'reviewer','completed','{"synthetic_legacy_int64":9223372036854775807}')`, resultID, org, in.SessionID, in.AgentProvider)
			require.NoError(t, err, "seed one real all-NULL legacy result so a terminal writer can touch results without inventing unresolved work")
			if tt.version == 390 {
				setup := callerSendStampPGBegin(t, ctx, pool, false)
				callerDispatchStatusPGApply(t, ctx, setup, 392, "down")
				callerDispatchStatusPGApply(t, ctx, setup, 391, "down")
				require.NoError(t, setup.Commit(ctx), "reach the real allowance-removal precondition with new readiness false before the independent lowest-down schedule")
			}
			before := callerDispatchStatusPGSnapshotAt(t, ctx, pool, org)
			wantMetadata := callerDispatchStatusLockChangedRow(t, before.rows[7], map[string]string{
				"status": "failed", "status_code": "reviewer_failed", "status_message": "Synthetic metadata-first terminal writer",
			})
			wantResult := callerDispatchStatusLockChangedRow(t, before.rows[8], map[string]string{
				"status": "failed", "raw_output": "Synthetic terminal result writer",
			})
			conns := callerDispatchStatusPGConnections(t, ctx, pool)
			var observerPID uint32
			var queryBytes int
			require.NoError(t, conns[2].QueryRow(ctx, `SELECT pg_backend_pid(),(SELECT setting::integer FROM pg_settings WHERE name='track_activity_query_size')`).Scan(&observerPID, &queryBytes), "warm the independent catalog observer and read the server's actual query-text capacity before either lock owner starts")
			require.Equal(t, conns[2].Conn().PgConn().PID(), observerPID, "the warmed observer must be the exact reserved third backend")
			require.Greater(t, queryBytes, 1, "actual PostgreSQL activity text must have a usable finite byte capacity")
			first := callerDispatchStatusLockBegin(t, ctx, conns[0])
			second := callerDispatchStatusLockBegin(t, ctx, conns[1])
			raceCtx, raceCancel := context.WithCancel(ctx)
			finished := make(chan struct{})
			started := false
			callerDispatchStatusPGRaceCleanup(t, raceCancel, &started, finished, first, second)
			query := callerDispatchStatusMigrationSQL(t, tt.version, "down")
			outcome := make(chan callerDispatchStatusLockOutcome, 1)
			if tt.writerFirst {
				var metadata json.RawMessage
				require.NoError(t, first.QueryRow(ctx, callerDispatchStatusLockMetadataWriter, org, in.MetadataID).Scan(&metadata), "the real terminal-writer pattern must update metadata before requesting results")
				require.JSONEq(t, string(wantMetadata), string(metadata), "the holder must change exactly the intended metadata fields and preserve every other field")
				started = true
				go func() {
					_, execErr := second.Exec(raceCtx, query)
					outcome <- callerDispatchStatusLockOutcome{err: execErr}
					close(finished)
				}()
				mode := "ShareRowExclusiveLock"
				if tt.version == 390 {
					mode = "AccessExclusiveLock"
				}
				callerDispatchStatusLockWait(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), conns[0].Conn().PgConn().PID(), query, queryBytes, mode, finished)
				// Sensitivity gate: move only metadata to the end of either
				// real down LOCK list, leaving this test unchanged. That DDL
				// then holds results while waiting on metadata, so this exact
				// NOWAIT statement must fail with genuine PostgreSQL 55P03.
				_, err = first.Exec(ctx, `LOCK TABLE code_review_agent_results IN ROW EXCLUSIVE MODE NOWAIT`)
				require.NoError(t, err, "metadata-first down must leave results available to the existing metadata holder; metadata-last ablation must fail this actual results NOWAIT gate")
				_, err = first.Exec(ctx, `LOCK TABLE caller_send_intent_control IN ROW EXCLUSIVE MODE NOWAIT`)
				require.NoError(t, err, "down waiting on metadata must not acquire the control lock ahead of that metadata holder")
				callerDispatchStatusLockOnlyMetadata(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), mode)
				var result json.RawMessage
				require.NoError(t, first.QueryRow(ctx, callerDispatchStatusLockResultWriter, org, in.SessionID, resultID).Scan(&result), "the same actual metadata writer must then update its scoped result without waiting on the contending down")
				require.JSONEq(t, string(wantResult), string(result), "the metadata-then-results writer must preserve every unrelated result field, including original JSON integers")
				require.NoError(t, first.Rollback(ctx), "release the metadata/results writer through ordinary rollback after its two real updates are proved")
				actual := callerDispatchStatusLockReceive(t, ctx, outcome)
				require.NoError(t, actual.err, "the real highest or lowest down must resume without deadlock after the earlier writer releases its metadata lock")
				held := callerDispatchStatusPGSnapshotAt(t, ctx, second, org)
				require.Equal(t, before.rows, held.rows, "the actual down must retain every complete business row after the earlier synthetic writer rolled back")
				require.Equal(t, before.ledger, held.ledger, "raw DDL must not manufacture migration-driver ledger evidence")
				require.NoError(t, second.Rollback(ctx), "restore all actual down catalog/control changes by rolling back its owned unit")
			} else {
				callerDispatchStatusPGApply(t, ctx, first, tt.version, "down")
				held := callerDispatchStatusPGSnapshotAt(t, ctx, first, org)
				require.Equal(t, before.rows, held.rows, "the first down must hold its real locks without changing any complete business row")
				require.Equal(t, before.ledger, held.ledger, "the held raw down must leave the genuine fixture ledger unchanged")
				started = true
				go func() {
					var actual callerDispatchStatusLockOutcome
					actual.err = second.QueryRow(raceCtx, callerDispatchStatusLockMetadataWriter, org, in.MetadataID).Scan(&actual.metadata)
					if actual.err == nil {
						actual.err = second.QueryRow(raceCtx, callerDispatchStatusLockResultWriter, org, in.SessionID, resultID).Scan(&actual.result)
					}
					outcome <- actual
					close(finished)
				}()
				callerDispatchStatusLockWait(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), conns[0].Conn().PgConn().PID(), callerDispatchStatusLockMetadataWriter, queryBytes, "RowExclusiveLock", finished)
				callerDispatchStatusLockOnlyMetadata(t, ctx, conns[2], conns[1].Conn().PgConn().PID(), "RowExclusiveLock")
				require.NoError(t, first.Rollback(ctx), "release the first actual down by rolling back its complete catalog/control unit after proving the writer is blocked on metadata")
				actual := callerDispatchStatusLockReceive(t, ctx, outcome)
				require.NoError(t, actual.err, "the metadata-then-results writer must resume normally after either highest or lowest down releases its locks")
				require.JSONEq(t, string(wantMetadata), string(actual.metadata), "the resumed writer must update the complete exact metadata row")
				require.JSONEq(t, string(wantResult), string(actual.result), "the resumed writer must update the complete exact result row after metadata")
				written := callerDispatchStatusPGSnapshotAt(t, ctx, second, org)
				require.Equal(t, before.control, written.control, "the resumed writer must preserve every control field and timestamp")
				require.Equal(t, before.catalog, written.catalog, "rollback of the earlier down must restore the exact complete catalog before the writer resumes")
				require.Equal(t, before.ledger, written.ledger, "the ordinary writer cannot advance or dirty a migration ledger")
				for i := range before.rows {
					want := before.rows[i]
					if i == 7 {
						want = append(append([]byte{'['}, wantMetadata...), ']')
					} else if i == 8 {
						want = append(append([]byte{'['}, wantResult...), ']')
					}
					require.JSONEq(t, string(want), string(written.rows[i]), "the resumed writer must preserve all complete business rows except its exact declared metadata and result fields")
				}
				require.NoError(t, second.Rollback(ctx), "discard only this synthetic writer's changes by ordinary rollback")
			}
			require.Equal(t, before, callerDispatchStatusPGSnapshotAt(t, ctx, conns[2], org), "both complete race units must finish with exact baseline business/control/catalog/ledger snapshots and no partial DDL")
		})
	}
}

type callerDispatchStatusLockOutcome struct {
	metadata, result json.RawMessage
	err              error
}

func callerDispatchStatusLockBegin(t *testing.T, ctx context.Context, conn *pgxpool.Conn) pgx.Tx {
	t.Helper()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "own each pinned lock-schedule transaction before starting its competitor")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := tx.Rollback(cleanupCtx)
		require.True(t, err == nil || errors.Is(err, pgx.ErrTxClosed), "roll back each pinned unit before releasing its reserved connection")
	})
	return tx
}

func callerDispatchStatusLockChangedRow(t *testing.T, raw json.RawMessage, changes map[string]string) json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rows), "decode complete synthetic row arrays without converting original int64 fields through float64")
	require.Equal(t, 1, len(rows), "each isolated lock fixture must start with exactly its one metadata row or one legacy result")
	for field, value := range changes {
		encoded, err := json.Marshal(value)
		require.NoError(t, err, "encode only the exact expected changed terminal-writer field")
		rows[0][field] = encoded
	}
	expected, err := json.Marshal(rows[0])
	require.NoError(t, err, "retain the entire expected business row with every unrelated original field")
	return expected
}

func callerDispatchStatusLockWait(t *testing.T, ctx context.Context, observer *pgxpool.Conn, contender, holder uint32, query string, queryBytes int, mode string, finished <-chan struct{}) {
	t.Helper()
	if len(query) >= queryBytes {
		query = query[:queryBytes-1] // These finite migration/writer sources are ASCII.
	}
	for {
		select {
		case <-finished:
			require.FailNow(t, "the contender must be observed waiting on the exact metadata holder before it completes")
		case <-ctx.Done():
			require.FailNow(t, "the exact metadata PID/query/blocker gate must complete within its finite context")
		default:
		}
		var blocked bool
		require.NoError(t, observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid=$1::integer AND a.state='active' AND a.wait_event_type='Lock' AND a.query=$3 AND $2::integer=ANY(pg_blocking_pids(a.pid)) AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='relation' AND l.relation='code_review_session_metadata'::regclass AND l.mode=$4 AND NOT l.granted))`, contender, holder, query, mode).Scan(&blocked), "prove the exact backend, server-retained query text, metadata relation/mode and actual blocking owner without sleeps")
		if blocked {
			return
		}
	}
}

func callerDispatchStatusLockOnlyMetadata(t *testing.T, ctx context.Context, observer *pgxpool.Conn, contender uint32, mode string) {
	t.Helper()
	var actual []string
	require.NoError(t, observer.QueryRow(ctx, `SELECT COALESCE(array_agg(c.relname||':'||l.mode||':'||l.granted::text ORDER BY c.relname,l.mode,l.granted),ARRAY[]::text[]) FROM pg_locks l JOIN pg_class c ON c.oid=l.relation WHERE l.pid=$1::integer AND l.locktype='relation' AND l.relation IN ('code_review_session_metadata'::regclass,'code_review_agent_results'::regclass,'caller_send_intent_control'::regclass)`, contender).Scan(&actual), "inspect all real metadata/results/control relation locks for this exact contender while the holder remains active")
	require.Equal(t, []string{"code_review_session_metadata:" + mode + ":false"}, actual, "a contender waiting first on metadata must not already hold results or control locks")
}

func callerDispatchStatusLockReceive(t *testing.T, ctx context.Context, outcome <-chan callerDispatchStatusLockOutcome) callerDispatchStatusLockOutcome {
	t.Helper()
	select {
	case actual := <-outcome:
		return actual
	case <-ctx.Done():
		require.FailNow(t, "the released actual database contender must finish before the finite race context expires")
		return callerDispatchStatusLockOutcome{}
	}
}
