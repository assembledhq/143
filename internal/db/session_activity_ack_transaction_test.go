package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type inboxAckTransactionFixture struct {
	orgID, sessionID, threadID, runtimeID, token, oldPhaseID, newPhaseID, batchID uuid.UUID
	at                                                                            time.Time
}

func newInboxAckTransactionFixture() inboxAckTransactionFixture {
	return inboxAckTransactionFixture{orgID: uuid.New(), sessionID: uuid.New(), threadID: uuid.New(), runtimeID: uuid.New(), token: uuid.New(), oldPhaseID: uuid.New(), newPhaseID: uuid.New(), batchID: uuid.New(), at: time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)}
}

// The real atomic steering owner may settle its one transaction. Any nested
// Begin is rejected before it can create the savepoint this regression removes.
type inboxAckOwnedTx struct {
	pgx.Tx
	nestedBegins int
	commits      int
	rollbacks    int
	proof        *inboxAckCommitProof
}

func (tx *inboxAckOwnedTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nestedBegins++
	return nil, errors.New("nested acknowledgment transaction is forbidden")
}

func (tx *inboxAckOwnedTx) Commit(ctx context.Context) error {
	tx.commits++
	if tx.proof != nil {
		if err := tx.proof.inspect(ctx, tx.Tx); err != nil {
			return err
		}
	}
	return tx.Tx.Commit(ctx)
}

func (tx *inboxAckOwnedTx) Rollback(ctx context.Context) error {
	tx.rollbacks++
	return tx.Tx.Rollback(ctx)
}

type inboxAckStarter struct {
	TxStarter
	owned *inboxAckOwnedTx
	proof *inboxAckCommitProof
}

func (s *inboxAckStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if s.proof != nil {
		if err := tx.QueryRow(ctx, "SELECT txid_current(),now()").Scan(&s.proof.transactionID, &s.proof.transactionTime); err != nil {
			return nil, errors.Join(err, tx.Rollback(ctx))
		}
	}
	s.owned = &inboxAckOwnedTx{Tx: tx, proof: s.proof}
	return s.owned, nil
}

func inboxAckBatchRows(f inboxAckTransactionFixture, status models.InboxDeliveryBatchStatus) *pgxmock.Rows {
	at := f.at.Truncate(time.Microsecond)
	var startedAt, abandonedAt *time.Time
	if status == models.InboxDeliveryBatchStarted {
		startedAt = &at
	} else if status == models.InboxDeliveryBatchAbandoned {
		abandonedAt = &at
	}
	return pgxmock.NewRows(inboxBatchTestColumns).AddRow(f.batchID, f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(4), int64(5), status, at, startedAt, abandonedAt, at, at)
}

func inboxAckPhaseRows(f inboxAckTransactionFixture, completed bool) *pgxmock.Rows {
	at, started := f.at.Truncate(time.Microsecond), f.at.Truncate(time.Microsecond).Add(-time.Minute)
	id, number, status, trigger := f.oldPhaseID, 1, models.ActivityPhaseStatusCompleted, models.ActivityPhaseTriggerInitial
	var reason any = models.ActivityPhaseBoundarySteered
	var completedAt any = &at
	var batch, first, last any
	if !completed {
		id, number, status, trigger = f.newPhaseID, 2, models.ActivityPhaseStatusRunning, models.ActivityPhaseTriggerInboxBatch
		started, reason, completedAt = at, nil, nil
		sequenceStart, sequenceEnd := int64(4), int64(5)
		batch, first, last = &f.batchID, &sequenceStart, &sequenceEnd
	}
	return pgxmock.NewRows(activityPhaseTestColumns).AddRow(id, f.orgID, f.sessionID, f.threadID, 2, number, status, reason, started, completedAt, &f.runtimeID, trigger, batch, first, last, started, at)
}

func expectedInboxAckResult(f inboxAckTransactionFixture, concurrentReplay, noPhase bool) inboxAcknowledgmentResult {
	at := f.at.Truncate(time.Microsecond)
	result := inboxAcknowledgmentResult{batch: models.ThreadInboxDeliveryBatch{ID: f.batchID, OrgID: f.orgID, SessionID: f.sessionID, ThreadID: f.threadID, RuntimeID: f.runtimeID, SequenceStart: 4, SequenceEnd: 5, Status: models.InboxDeliveryBatchAcknowledged, AcknowledgedAt: at, CreatedAt: at, UpdatedAt: at}, concurrentReplay: concurrentReplay}
	if !concurrentReplay && !noPhase {
		result.completedPhase = &models.SessionActivityPhase{ID: f.oldPhaseID, OrgID: f.orgID, SessionID: f.sessionID, ThreadID: f.threadID, TurnNumber: 2, PhaseNumber: 1, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: models.ActivityPhaseBoundarySteered, StartedAt: at.Add(-time.Minute), CompletedAt: &at, RuntimeID: &f.runtimeID, TriggerKind: models.ActivityPhaseTriggerInitial, CreatedAt: at.Add(-time.Minute), UpdatedAt: at}
	}
	return result
}

func expectInboxAckCore(mock pgxmock.PgxPoolIface, f inboxAckTransactionFixture, failure string) (inboxAcknowledgmentResult, error) {
	at := f.at.Truncate(time.Microsecond)
	databaseErr := errors.New("synthetic acknowledgment database failure")
	var expectedErr error
	query := func(name, prefix, collectPrefix, pattern string, args []any, rows *pgxmock.Rows) bool {
		expectation := mock.ExpectQuery(pattern).WithArgs(args...)
		if failure == name {
			expectation.WillReturnError(databaseErr)
			expectedErr = fmt.Errorf("%s: %w", prefix, databaseErr)
			return false
		}
		if failure == name+" collect" {
			expectation.WillReturnRows(rows.RowError(0, databaseErr))
			expectedErr = fmt.Errorf("%s: %w", collectPrefix, databaseErr)
			return false
		}
		expectation.WillReturnRows(rows)
		return true
	}
	exec := func(name, prefix, pattern string, args []any, count int64) bool {
		expectation := mock.ExpectExec(pattern).WithArgs(args...)
		if failure == name {
			expectation.WillReturnError(databaseErr)
			expectedErr = fmt.Errorf("%s: %w", prefix, databaseErr)
			return false
		}
		if failure == name+" mismatch" {
			expectation.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			expectedErr = ErrInboxBatchConflict
			return false
		}
		expectation.WillReturnResult(pgxmock.NewResult("UPDATE", count))
		return true
	}
	previous := int64(3)
	if failure == "behind cursor" {
		previous = 6
	} else if strings.HasPrefix(failure, "concurrent replay") {
		previous = 5
	}
	if !query("runtime", "lock thread runtime for inbox acknowledgment", "", `(?s)SELECT last_acked_sequence FROM thread_runtimes.+org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND id = \$4.+lease_token = \$5.+lease_expires_at > \$6.+FOR UPDATE`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, at}, pgxmock.NewRows([]string{"last_acked_sequence"}).AddRow(previous)) {
		return inboxAcknowledgmentResult{}, expectedErr
	}
	if previous > 5 {
		return inboxAcknowledgmentResult{}, ErrInboxBatchConflict
	}
	if previous == 5 {
		expectation := mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(5))
		switch failure {
		case "concurrent replay query":
			expectation.WillReturnError(databaseErr)
			return inboxAcknowledgmentResult{}, fmt.Errorf("get concurrently replayed inbox delivery batch: %w", databaseErr)
		case "concurrent replay missing":
			expectation.WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
			return inboxAcknowledgmentResult{}, ErrInboxBatchConflict
		case "concurrent replay collect":
			expectation.WillReturnRows(inboxAckBatchRows(f, models.InboxDeliveryBatchAcknowledged).RowError(0, databaseErr))
			return inboxAcknowledgmentResult{}, ErrInboxBatchConflict
		default:
			expectation.WillReturnRows(inboxAckBatchRows(f, models.InboxDeliveryBatchAcknowledged))
			return expectedInboxAckResult(f, true, true), nil
		}
	}
	matching := int64(2)
	if failure == "range mismatch" {
		matching = 1
	}
	if !query("count", "validate inbox delivery batch entries", "", `(?s)SELECT count\(\*\) FROM thread_inbox_entries.+org_id = \$1 AND session_id = \$2 AND thread_id = \$3.+runtime_id = \$4 AND sequence_no BETWEEN \$5 AND \$6.+delivery_state = 'delivered'`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(4), int64(5)}, pgxmock.NewRows([]string{"count"}).AddRow(matching)) {
		return inboxAcknowledgmentResult{}, expectedErr
	}
	if matching != 2 {
		return inboxAcknowledgmentResult{}, ErrInboxBatchConflict
	}
	if failure != "no phase" {
		rows := inboxAckPhaseRows(f, true)
		if failure == "close missing" {
			rows = pgxmock.NewRows(activityPhaseTestColumns)
		}
		if !query("close", "close steered activity phase", "collect steered activity phase", `(?s)UPDATE session_activity_phases.+WHERE org_id = \$1 AND id = \$2 AND session_id = \$4 AND thread_id = \$5.+runtime_id = \$6 AND status = 'running' AND started_at <= \$3`, []any{f.orgID, f.oldPhaseID, at, f.sessionID, f.threadID, f.runtimeID}, rows) {
			return inboxAcknowledgmentResult{}, expectedErr
		}
		if failure == "close missing" {
			return inboxAcknowledgmentResult{}, ErrActivityPhaseConflict
		}
	}
	if !query("batch", "insert inbox delivery batch", "collect acknowledged inbox delivery batch", "INSERT INTO thread_inbox_delivery_batches", []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(4), int64(5), at}, inboxAckBatchRows(f, models.InboxDeliveryBatchAcknowledged)) {
		return inboxAcknowledgmentResult{}, expectedErr
	}
	if !exec("ack", "mark inbox delivery batch entries acknowledged", `(?s)UPDATE thread_inbox_entries.+WHERE org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND runtime_id = \$4.+sequence_no BETWEEN \$5 AND \$6 AND delivery_state = 'delivered'`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(4), int64(5), at}, 2) {
		return inboxAcknowledgmentResult{}, expectedErr
	}
	if !exec("watermark", "advance inbox acknowledgment watermark", `(?s)UPDATE thread_runtimes.+WHERE org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND id = \$4.+lease_token = \$5 AND last_acked_sequence = \$8`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, int64(5), at, int64(3)}, 1) {
		return inboxAcknowledgmentResult{}, expectedErr
	}
	return expectedInboxAckResult(f, false, failure == "no phase"), nil
}

func TestInboxAckTransactionCore(t *testing.T) {
	t.Parallel()
	tests := []struct{ mode, failure string }{}
	for _, mode := range []string{"caller transaction", "legacy wrapper"} {
		for _, failure := range []string{"", "no phase", "runtime", "behind cursor", "count", "range mismatch", "close", "close missing", "close collect", "batch", "batch collect", "ack", "ack mismatch", "watermark", "watermark mismatch", "concurrent replay", "concurrent replay query", "concurrent replay missing", "concurrent replay collect"} {
			tests = append(tests, struct{ mode, failure string }{mode, failure})
		}
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			f := newInboxAckTransactionFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the acknowledgment sequence mock")
			t.Cleanup(mock.Close)
			store := NewSessionActivityPhaseStore(mock)
			if tt.mode == "legacy wrapper" {
				mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(5)).WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
			}
			mock.ExpectBegin()
			var caller pgx.Tx
			var tx *sessionPersistenceOwnedTx
			if tt.mode == "caller transaction" {
				caller, err = mock.Begin(context.Background())
				require.NoError(t, err, "only the caller begins the supplied transaction")
				tx = &sessionPersistenceOwnedTx{Tx: caller}
				store = NewSessionActivityPhaseStore(nil)
			}
			expected, expectedErr := expectInboxAckCore(mock, f, tt.failure)
			if expectedErr != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			active := &f.oldPhaseID
			if tt.failure == "no phase" {
				active = nil
			}
			var result inboxAcknowledgmentResult
			if tt.mode == "caller transaction" {
				result, err = store.acknowledgeInboxBatchInTx(context.Background(), f.orgID, tx, f.sessionID, f.threadID, f.runtimeID, f.token, active, 5, f.at)
				require.Empty(t, tx.settlements, "the core must not begin a savepoint, commit, roll back or borrow a receiver pool")
				if expectedErr != nil {
					require.NoError(t, caller.Rollback(context.Background()), "only the caller aborts a failed acknowledgment unit")
				} else {
					require.NoError(t, caller.Commit(context.Background()), "only the caller commits the composed acknowledgment unit")
				}
			} else {
				result.batch, result.completedPhase, err = store.AcknowledgeInboxBatchWithTransition(context.Background(), f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, active, 5, f.at)
				result.concurrentReplay = expected.concurrentReplay
			}
			if expectedErr != nil {
				require.EqualError(t, err, expectedErr.Error(), "the exact original query, collect or row-count error must reach the owner")
				if cause := errors.Unwrap(expectedErr); cause != nil {
					require.ErrorIs(t, err, cause, "wrapped database errors must retain their original classification")
				} else {
					require.ErrorIs(t, err, expectedErr, "acknowledgment and phase conflicts must retain their original sentinels")
				}
				require.Equal(t, inboxAcknowledgmentResult{}, result, "failed persistence must not return a successful receipt or phase transition")
			} else {
				require.NoError(t, err, "acknowledgment must retain its successful legacy result")
				require.Equal(t, expected, result, "the complete receipt and completed phase shape must remain identical across the extracted core and wrapper")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all core statements must preserve original order, scoped arguments, lease/time/range checks and settlement ownership")
		})
	}
}

func TestInboxAckTransactionStandaloneBoundaries(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"replay", "replay query error", "replay collect error", "begin error", "commit error", "concurrent replay commit error"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			f := newInboxAckTransactionFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the standalone boundary mock")
			t.Cleanup(mock.Close)
			failure := errors.New("synthetic boundary failure")
			var expectedErr error
			expected := inboxAcknowledgmentResult{}
			initial := mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(5))
			switch outcome {
			case "replay":
				initial.WillReturnRows(inboxAckBatchRows(f, models.InboxDeliveryBatchAcknowledged))
				expected = expectedInboxAckResult(f, false, true)
			case "replay query error":
				initial.WillReturnError(failure)
				expectedErr = fmt.Errorf("check replayed inbox delivery batch: %w", failure)
			case "replay collect error":
				initial.WillReturnRows(inboxAckBatchRows(f, models.InboxDeliveryBatchAcknowledged).RowError(0, failure))
				expectedErr = fmt.Errorf("collect replayed inbox delivery batch: %w", failure)
			default:
				initial.WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
				if outcome == "begin error" {
					mock.ExpectBegin().WillReturnError(failure)
					expectedErr = fmt.Errorf("begin acknowledge inbox delivery batch: %w", failure)
				} else {
					mock.ExpectBegin()
					coreCase, prefix := "", "commit acknowledged inbox delivery batch"
					if outcome == "concurrent replay commit error" {
						coreCase, prefix = "concurrent replay", "commit concurrently replayed inbox delivery batch"
					}
					_, coreErr := expectInboxAckCore(mock, f, coreCase)
					require.NoError(t, coreErr, "configure the unchanged core before its owner fails commit")
					mock.ExpectCommit().WillReturnError(failure)
					mock.ExpectRollback()
					expectedErr = fmt.Errorf("%s: %w", prefix, failure)
				}
			}
			batch, phase, err := NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatchWithTransition(context.Background(), f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, &f.oldPhaseID, 5, f.at)
			if expectedErr != nil {
				require.EqualError(t, err, expectedErr.Error(), "standalone read/Begin and each distinct Commit branch must preserve its exact original error prefix")
				require.ErrorIs(t, err, failure, "standalone boundary failures must preserve the original cause")
				require.Equal(t, models.ThreadInboxDeliveryBatch{}, batch, "unconfirmed or failed standalone work must return no accepted receipt")
				require.Nil(t, phase, "unconfirmed or failed standalone work must return no phase transition")
			} else {
				require.NoError(t, err, "initial durable replay must remain available without a transaction or live runtime lease")
				require.Equal(t, expected.batch, batch, "initial replay must return the complete original receipt")
				require.Nil(t, phase, "initial replay must not report another phase transition")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "initial replay must stay before Begin and each owner must retain its exact settlement behavior")
		})
	}
}

func TestInboxAckTransactionRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	var typedNil *sessionPersistenceOwnedTx
	for _, tt := range []struct {
		name string
		tx   pgx.Tx
		end  int64
		zero bool
		want string
	}{{"nil transaction", nil, 5, false, "acknowledge inbox delivery batch: transaction is required"}, {"typed nil transaction", typedNil, 5, false, "acknowledge inbox delivery batch: transaction is required"}, {"zero sequence", &sessionPersistenceOwnedTx{}, 0, false, "acknowledge inbox delivery batch: invalid sequence or acknowledgment time"}, {"zero time", &sessionPersistenceOwnedTx{}, 5, true, "acknowledge inbox delivery batch: invalid sequence or acknowledgment time"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newInboxAckTransactionFixture()
			if tt.zero {
				f.at = time.Time{}
			}
			result, err := NewSessionActivityPhaseStore(nil).acknowledgeInboxBatchInTx(context.Background(), f.orgID, tt.tx, f.sessionID, f.threadID, f.runtimeID, f.token, nil, tt.end, f.at)
			require.EqualError(t, err, tt.want, "invalid explicit core inputs must fail before any database access")
			require.Equal(t, inboxAcknowledgmentResult{}, result, "invalid core inputs must return no receipt or commit-branch metadata")
			if tx, ok := tt.tx.(*sessionPersistenceOwnedTx); ok && tx != nil {
				require.Empty(t, tx.settlements, "invalid core input must not settle any caller-owned transaction")
			}
		})
	}
}

func TestInboxAckTransactionStandaloneValidationBeforeReplay(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"zero sequence", "negative sequence", "zero time"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			f := newInboxAckTransactionFixture()
			sequenceEnd := int64(5)
			if invalid == "zero sequence" {
				sequenceEnd = 0
			} else if invalid == "negative sequence" {
				sequenceEnd = -1
			} else {
				f.at = time.Time{}
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create a standalone validation mock with no allowed replay query or Begin")
			t.Cleanup(mock.Close)
			batch, completed, err := NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatchWithTransition(context.Background(), f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, &f.oldPhaseID, sequenceEnd, f.at)
			require.EqualError(t, err, "acknowledge inbox delivery batch: invalid sequence or acknowledgment time", "standalone validation must retain its exact error before the initial replay read or transaction acquisition")
			require.Equal(t, models.ThreadInboxDeliveryBatch{}, batch, "invalid standalone input must return no receipt")
			require.Nil(t, completed, "invalid standalone input must return no phase transition")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid standalone arguments must not perform any SQL or settlement")
		})
	}
}

func TestInboxAckAtomicSteeringRejectsNestedBegin(t *testing.T) {
	t.Parallel()
	f := newInboxAckTransactionFixture()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create the actual atomic steering path mock")
	t.Cleanup(mock.Close)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM session_threads").WithArgs(f.orgID, f.sessionID, f.threadID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.threadID))
	mock.ExpectQuery("SELECT last_acked_sequence FROM thread_runtimes").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token).WillReturnRows(pgxmock.NewRows([]string{"last_acked_sequence"}).AddRow(int64(3)))
	mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(5)).WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
	expectedAck, err := expectInboxAckCore(mock, f, "")
	require.NoError(t, err, "configure only the shared locked acknowledgement sequence, without another replay query or Begin")
	at := f.at.Truncate(time.Microsecond)
	mock.ExpectQuery("SELECT id FROM session_threads").WithArgs(f.orgID, f.sessionID, f.threadID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.threadID))
	mock.ExpectQuery("UPDATE thread_inbox_delivery_batches").WithArgs(f.orgID, f.batchID, f.sessionID, f.threadID, at).WillReturnRows(inboxAckBatchRows(f, models.InboxDeliveryBatchStarted))
	mock.ExpectExec("UPDATE thread_inbox_entries").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(4), int64(5), at).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectQuery("SELECT id FROM thread_runtimes").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, at).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.runtimeID))
	mock.ExpectExec("WITH terminal_owners AS MATERIALIZED").WithArgs(f.orgID, f.sessionID, f.threadID, f.runtimeID, at).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	first, last := int64(4), int64(5)
	mock.ExpectQuery("INSERT INTO session_activity_phases").WithArgs(f.orgID, f.sessionID, f.threadID, 2, at, &f.runtimeID, models.ActivityPhaseTriggerInboxBatch, &f.batchID, &first, &last).WillReturnRows(inboxAckPhaseRows(f, false))
	mock.ExpectCommit()
	starter := &inboxAckStarter{TxStarter: mock}
	phase, batch, completed, err := NewSessionActivityPhaseStore(starter).AcknowledgeInboxBatchAndStartPhase(context.Background(), f.orgID, f.sessionID, f.threadID, f.runtimeID, f.token, &f.oldPhaseID, 2, 5, []int64{5, 4}, f.at)
	require.NoError(t, err, "the real atomic steering caller must complete while its transaction rejects every nested Begin")
	require.Equal(t, 0, starter.owned.nestedBegins, "acknowledgment composition must remove the real nested savepoint, not merely tolerate it")
	require.Equal(t, 1, starter.owned.commits, "only the atomic steering owner may commit the full boundary")
	require.Equal(t, 1, starter.owned.rollbacks, "the owner must retain its existing deferred rollback cleanup")
	expectedAck.batch.Status, expectedAck.batch.StartedAt = models.InboxDeliveryBatchStarted, &at
	require.Equal(t, expectedAck.batch, batch, "the atomic result must retain the same batch identity with started metadata")
	require.Equal(t, expectedAck.completedPhase, completed, "the atomic result must retain the exact closed steering phase")
	require.Equal(t, models.SessionActivityPhase{ID: f.newPhaseID, OrgID: f.orgID, SessionID: f.sessionID, ThreadID: f.threadID, TurnNumber: 2, PhaseNumber: 2, Status: models.ActivityPhaseStatusRunning, StartedAt: at, RuntimeID: &f.runtimeID, TriggerKind: models.ActivityPhaseTriggerInboxBatch, TriggerBatchID: &f.batchID, TriggerSequenceStart: &first, TriggerSequenceEnd: &last, CreatedAt: at, UpdatedAt: at}, phase, "the replacement phase must retain all exact trigger and timestamp fields")
	require.NoError(t, mock.ExpectationsWereMet(), "the actual steering path must execute one outer transaction and the original phase-start sequence")
}
