package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const recheckReconciliationReceiptSQL = `SELECT session_id,thread_id,pull_request_id,expected_turn FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2 AND status IN ('failed','cancelled') FOR UPDATE`

const recheckReconciliationDrainSQL = `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
	AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
	AND NOT EXISTS(SELECT 1 FROM session_sandbox_holders h WHERE h.org_id=s.org_id AND h.session_id=s.id AND h.status IN ('active','draining') AND h.expires_at > now())
	AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
	AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
	FROM sessions s WHERE s.org_id=$1 AND s.id=$2 AND s.code_review_owner_pr_id=$3 FOR UPDATE`

const recheckReconciliationThreadSQL = `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status IN ('running','cancelled')`

const recheckReconciliationRecoverySQL = `SELECT current_turn,status FROM session_threads WHERE org_id=$1 AND id=$2 AND session_id=$3 FOR UPDATE`

const recheckReconciliationRootSQL = `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND code_review_owner_pr_id=$3 AND status IN ('running','cancelled')`

type recheckReconciliationOutcome struct {
	reconciled bool
	errorText  string
}

type recheckReconciliationCase struct {
	name           string
	failure        string
	recovery       bool
	recoveryOffset int
	recoveryStatus models.ThreadStatus
	rootRows       int64
	expected       recheckReconciliationOutcome
}

type recheckReconciliationFixture struct {
	orgID, assessmentID, sessionID, threadID, pullRequestID uuid.UUID
	turn                                                    int
}

func newRecheckReconciliationFixture() recheckReconciliationFixture {
	return recheckReconciliationFixture{
		orgID: uuid.New(), assessmentID: uuid.New(), sessionID: uuid.New(),
		threadID: uuid.New(), pullRequestID: uuid.New(), turn: 7,
	}
}

// This observer records settlement calls even when an unexpected deferred
// rollback's error would otherwise be discarded by the production wrapper.
type recheckReconciliationTx struct {
	pgx.Tx
	settlements []string
}

func (tx *recheckReconciliationTx) Begin(ctx context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return tx.Tx.Begin(ctx)
}

func (tx *recheckReconciliationTx) Commit(ctx context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return tx.Tx.Commit(ctx)
}

func (tx *recheckReconciliationTx) Rollback(ctx context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return tx.Tx.Rollback(ctx)
}

type recheckReconciliationStarter struct {
	TxStarter
	tx     *recheckReconciliationTx
	begins int
}

func (s *recheckReconciliationStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx.Tx = tx
	return s.tx, nil
}

func TestCodeReviewRecheckStoreReconcileDrainedTerminalTurn(t *testing.T) {
	t.Parallel()
	tests := []recheckReconciliationCase{
		{name: "terminal receipt absent", failure: "receipt missing", expected: recheckReconciliationOutcome{}},
		{name: "receipt query failure", failure: "receipt query", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "receipt scan failure", failure: "receipt scan", expected: recheckReconciliationOutcome{errorText: "destination kind 'int' not supported for value kind 'string' of column 'expected_turn'"}},
		{name: "owned session absent", failure: "drain missing", expected: recheckReconciliationOutcome{}},
		{name: "drain query failure", failure: "drain query", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "drain scan failure", failure: "drain scan", expected: recheckReconciliationOutcome{errorText: "destination kind 'bool' not supported for value kind 'string' of column 'drained'"}},
		{name: "execution remains active", failure: "not drained", expected: recheckReconciliationOutcome{}},
		{name: "thread update failure", failure: "thread write", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "thread absent after zero row update", recovery: true, failure: "recovery missing", expected: recheckReconciliationOutcome{}},
		{name: "thread recovery query failure", recovery: true, failure: "recovery query", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "thread recovery scan failure", recovery: true, failure: "recovery scan", expected: recheckReconciliationOutcome{errorText: "destination kind 'int' not supported for value kind 'string' of column 'current_turn'"}},
		{name: "later idle turn cannot release root", recovery: true, recoveryOffset: 1, recoveryStatus: models.ThreadStatusIdle, expected: recheckReconciliationOutcome{}},
		{name: "earlier idle turn cannot release root", recovery: true, recoveryOffset: -1, recoveryStatus: models.ThreadStatusIdle, expected: recheckReconciliationOutcome{}},
		{name: "exact running turn cannot release root", recovery: true, recoveryStatus: models.ThreadStatusRunning, expected: recheckReconciliationOutcome{}},
		{name: "exact cancelled turn cannot release root", recovery: true, recoveryStatus: models.ThreadStatusCancelled, expected: recheckReconciliationOutcome{}},
		{name: "exact completed turn cannot release root", recovery: true, recoveryStatus: models.ThreadStatusCompleted, expected: recheckReconciliationOutcome{}},
		{name: "exact failed turn cannot release root", recovery: true, recoveryStatus: models.ThreadStatusFailed, expected: recheckReconciliationOutcome{}},
		{name: "root write failure after thread transition", failure: "root write", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "root write failure after exact idle recovery", recovery: true, recoveryStatus: models.ThreadStatusIdle, failure: "root write", expected: recheckReconciliationOutcome{errorText: "synthetic reconciliation failure"}},
		{name: "drained terminal thread releases root", rootRows: 1, expected: recheckReconciliationOutcome{reconciled: true}},
		{name: "exact idle turn releases root", recovery: true, recoveryStatus: models.ThreadStatusIdle, rootRows: 1, expected: recheckReconciliationOutcome{reconciled: true}},
		{name: "root already idle preserves successful outcome", expected: recheckReconciliationOutcome{reconciled: true}},
		{name: "recovered root already idle preserves successful outcome", recovery: true, recoveryStatus: models.ThreadStatusIdle, expected: recheckReconciliationOutcome{reconciled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"wrapper", "in tx"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
					require.NoError(t, err, "reconciliation should use a fresh isolated database mock")
					t.Cleanup(mock.Close)
					fixture := newRecheckReconciliationFixture()
					failure := errors.New("synthetic reconciliation failure")
					tx := &recheckReconciliationTx{Tx: mock}
					starter := &recheckReconciliationStarter{TxStarter: mock, tx: tx}
					if mode == "wrapper" {
						mock.ExpectBegin()
					}
					expectRecheckReconciliation(mock, fixture, tt, failure)
					if mode == "wrapper" {
						if tt.expected.reconciled {
							mock.ExpectCommit()
						}
						mock.ExpectRollback()
					}
					store := NewCodeReviewRecheckStore(starter)
					if mode == "in tx" {
						// A nil receiver database makes an accidental fallback fail;
						// only the separately supplied observed transaction is usable.
						store = NewCodeReviewRecheckStore(nil)
					}
					var reconciled bool
					if mode == "wrapper" {
						reconciled, err = store.ReconcileDrainedTerminalTurn(context.Background(), fixture.orgID, fixture.assessmentID)
					} else {
						reconciled, err = store.ReconcileDrainedTerminalTurnInTx(context.Background(), fixture.orgID, tx, fixture.assessmentID)
					}
					actual := recheckReconciliationOutcome{reconciled: reconciled}
					if err != nil {
						actual.errorText = err.Error()
					}
					require.Equal(t, tt.expected, actual, "both entry points should preserve the exact reconciliation result and failure")
					if tt.expected.errorText == failure.Error() {
						require.ErrorIs(t, err, failure, "database failures should retain their original error identity")
					}
					expectedBegins := 0
					var expectedSettlements []string
					if mode == "wrapper" {
						expectedBegins = 1
						if tt.expected.reconciled {
							expectedSettlements = []string{"commit", "rollback"}
						} else {
							expectedSettlements = []string{"rollback"}
						}
					}
					require.Equal(t, expectedBegins, starter.begins, "only the standalone wrapper should acquire one transaction")
					require.Equal(t, expectedSettlements, tx.settlements, "the caller transaction must never be begun or settled by the in-transaction seam")
					require.NoError(t, mock.ExpectationsWereMet(), "reconciliation should issue only the exact scoped and fenced SQL in order")
				})
			}
		})
	}
}

func expectRecheckReconciliation(mock pgxmock.PgxPoolIface, f recheckReconciliationFixture, tt recheckReconciliationCase, failure error) {
	receipt := mock.ExpectQuery(recheckReconciliationReceiptSQL).WithArgs(f.orgID, f.assessmentID)
	receiptRows := pgxmock.NewRows([]string{"session_id", "thread_id", "pull_request_id", "expected_turn"})
	switch tt.failure {
	case "receipt query":
		receipt.WillReturnError(failure)
		return
	case "receipt missing":
		receipt.WillReturnRows(receiptRows)
		return
	case "receipt scan":
		receipt.WillReturnRows(receiptRows.AddRow(f.sessionID, f.threadID, f.pullRequestID, "invalid turn"))
		return
	default:
		receipt.WillReturnRows(receiptRows.AddRow(f.sessionID, f.threadID, f.pullRequestID, f.turn))
	}
	drain := mock.ExpectQuery(recheckReconciliationDrainSQL).WithArgs(f.orgID, f.sessionID, f.pullRequestID)
	drainRows := pgxmock.NewRows([]string{"drained"})
	switch tt.failure {
	case "drain query":
		drain.WillReturnError(failure)
		return
	case "drain missing":
		drain.WillReturnRows(drainRows)
		return
	case "drain scan":
		drain.WillReturnRows(drainRows.AddRow("invalid drain"))
		return
	case "not drained":
		drain.WillReturnRows(drainRows.AddRow(false))
		return
	default:
		drain.WillReturnRows(drainRows.AddRow(true))
	}
	thread := mock.ExpectExec(recheckReconciliationThreadSQL).WithArgs(f.orgID, f.threadID, f.sessionID, f.turn)
	if tt.failure == "thread write" {
		thread.WillReturnError(failure)
		return
	}
	if tt.recovery {
		thread.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		recovery := mock.ExpectQuery(recheckReconciliationRecoverySQL).WithArgs(f.orgID, f.threadID, f.sessionID)
		recoveryRows := pgxmock.NewRows([]string{"current_turn", "status"})
		switch tt.failure {
		case "recovery query":
			recovery.WillReturnError(failure)
			return
		case "recovery missing":
			recovery.WillReturnRows(recoveryRows)
			return
		case "recovery scan":
			recovery.WillReturnRows(recoveryRows.AddRow("invalid turn", models.ThreadStatusIdle))
			return
		default:
			recovery.WillReturnRows(recoveryRows.AddRow(f.turn+tt.recoveryOffset, tt.recoveryStatus))
		}
		if tt.recoveryOffset != 0 || tt.recoveryStatus != models.ThreadStatusIdle {
			return
		}
	} else {
		thread.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	root := mock.ExpectExec(recheckReconciliationRootSQL).WithArgs(f.orgID, f.sessionID, f.pullRequestID)
	if tt.failure == "root write" {
		root.WillReturnError(failure)
		return
	}
	root.WillReturnResult(pgxmock.NewResult("UPDATE", tt.rootRows))
}

func TestCodeReviewRecheckStoreReconcileDrainedTerminalTurnWrapperFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		failure  string
		expected recheckReconciliationOutcome
	}{
		{name: "begin failure does not run reconciliation", failure: "begin", expected: recheckReconciliationOutcome{errorText: "synthetic wrapper failure"}},
		{name: "commit failure retains historical successful boolean", failure: "commit", expected: recheckReconciliationOutcome{reconciled: true, errorText: "synthetic wrapper failure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "wrapper failure should use its own database mock")
			t.Cleanup(mock.Close)
			fixture := newRecheckReconciliationFixture()
			failure := errors.New("synthetic wrapper failure")
			tx := &recheckReconciliationTx{Tx: mock}
			starter := &recheckReconciliationStarter{TxStarter: mock, tx: tx}
			begin := mock.ExpectBegin()
			var expectedSettlements []string
			if tt.failure == "begin" {
				begin.WillReturnError(failure)
			} else {
				expectRecheckReconciliation(mock, fixture, recheckReconciliationCase{rootRows: 1}, failure)
				mock.ExpectCommit().WillReturnError(failure)
				mock.ExpectRollback()
				expectedSettlements = []string{"commit", "rollback"}
			}
			reconciled, err := NewCodeReviewRecheckStore(starter).ReconcileDrainedTerminalTurn(context.Background(), fixture.orgID, fixture.assessmentID)
			actual := recheckReconciliationOutcome{reconciled: reconciled}
			if err != nil {
				actual.errorText = err.Error()
			}
			require.Equal(t, tt.expected, actual, "the wrapper should preserve its historical boolean and original settlement failure")
			require.ErrorIs(t, err, failure, "the wrapper should preserve the exact begin or commit error")
			require.Equal(t, 1, starter.begins, "the wrapper should attempt acquisition only once")
			require.Equal(t, expectedSettlements, tx.settlements, "begin failure must not settle and commit failure must retain deferred rollback")
			require.NoError(t, mock.ExpectationsWereMet(), "wrapper failure should neither retry nor issue any extra SQL")
		})
	}
}

func TestCodeReviewRecheckStoreReconcileDrainedTerminalTurnInTxRejectsNil(t *testing.T) {
	t.Parallel()
	var typedNil *recheckReconciliationTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil transaction"},
		{name: "typed nil transaction", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// No receiver database exists: any attempted SQL or transaction
			// fallback would panic before returning the validation result.
			reconciled, err := NewCodeReviewRecheckStore(nil).ReconcileDrainedTerminalTurnInTx(context.Background(), uuid.New(), tt.tx, uuid.New())
			actual := recheckReconciliationOutcome{reconciled: reconciled}
			if err != nil {
				actual.errorText = err.Error()
			}
			require.Equal(t, recheckReconciliationOutcome{errorText: "reconcile drained terminal turn in tx: nil transaction"}, actual, "missing transactions must fail before any database work or settlement")
		})
	}
}
