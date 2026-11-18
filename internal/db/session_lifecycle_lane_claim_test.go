package db

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Independent SQL goldens prevent shared constants from hiding dropped scope,
// token filtering, visibility filtering or a weaker/differently ranked lock.
const (
	laneClaimSQL = `SELECT id, org_id, session_id, current_turn,
		admission_claim_dispatch_id, admission_claim_token FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3`
	laneClaimReceiptSQL = `SELECT id, org_id, session_id, thread_id,
		session_epoch, thread_epoch, admission_claim_token, admission_claim_turn
		FROM session_input_dispatches
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
		FOR NO KEY UPDATE NOWAIT`
)

func TestLifecycleLaneClaimQueryContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, want, actual string }{
		{"locked exact lane without status filters", laneClaimSQL, lifecycleLaneClaimQuery},
		{"expected receipt identity before raw comparison", laneClaimReceiptSQL, lifecycleLaneClaimReceiptQuery},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, strings.Fields(tt.want), strings.Fields(tt.actual), "receipt reads must retain exact scope and raw NULL evidence")
		})
	}
}

func TestLifecycleLaneClaimRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"nil context", "nil transaction", "typed nil transaction", "cancelled", "deadline", "no budget", "long budget", "org", "session", "lane", "dispatch", "token", "session epoch", "lane epoch", "turn"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f, expected, _ := newLaneClaimUnitFixture()
			var tx pgx.Tx = mock
			switch scenario {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var absent *pgxpool.Tx
				tx = absent
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "no budget":
				ctx = context.Background()
			case "long budget":
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(context.Background(), 10*LifecycleTransactionTimeout)
				defer cancel()
			case "org":
				f.orgID = uuid.Nil
			case "session":
				expected.sessionID = uuid.Nil
			case "lane":
				expected.threadID = uuid.Nil
			case "dispatch":
				expected.dispatchID = uuid.Nil
			case "token":
				expected.token = uuid.Nil
			case "session epoch":
				expected.sessionEpoch = -1
			case "lane epoch":
				expected.threadEpoch = -1
			case "turn":
				expected.turn = -1
			}
			actual, err := verifyLifecycleLaneClaimInTx(ctx, tx, f.orgID, expected)
			require.Error(t, err, "invalid explicit input must reject before SQL or transaction changes")
			if scenario == "cancelled" {
				require.ErrorIs(t, err, context.Canceled, "caller cancellation must retain its exact cause")
			}
			if scenario == "deadline" {
				require.ErrorIs(t, err, context.DeadlineExceeded, "expired caller budget must retain its cause")
			}
			require.Equal(t, lifecycleLaneClaimObservation{}, actual, "invalid input must expose no usable matching result")
			require.NoError(t, mock.ExpectationsWereMet(), "validation must not issue SQL or settle the owner")
		})
	}
}

func TestLifecycleLaneClaimRawComparison(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*lifecycleLaneClaimObservation, *lifecycleLaneClaimExpected)
		want lifecycleLaneClaimComparison
	}{
		{name: "exact", want: lifecycleLaneClaimExact},
		{name: "absent", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.claim = LifecycleAdmissionClaimState{}
		}, want: lifecycleLaneClaimAbsent},
		{name: "half pair", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.claim.Token = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "other half pair", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.claim.DispatchID = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "NULL token", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.receipt.token = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "NULL turn", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.receipt.turn = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "NULL session epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.receipt.sessionEpoch = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "NULL lane epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.receipt.threadEpoch = nil }, want: lifecycleLaneClaimIncomplete},
		{name: "zero pointer", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.claim.DispatchID = laneClaimPointer(uuid.Nil)
		}, want: lifecycleLaneClaimIncomplete},
		{name: "zero claim token", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.claim.Token = laneClaimPointer(uuid.Nil)
		}, want: lifecycleLaneClaimIncomplete},
		{name: "zero receipt token", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.token = laneClaimPointer(uuid.Nil)
		}, want: lifecycleLaneClaimIncomplete},
		{name: "negative receipt turn", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.turn = laneClaimPointer(int32(-1))
		}, want: lifecycleLaneClaimIncomplete},
		{name: "negative receipt epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.sessionEpoch = laneClaimPointer(int64(-1))
		}, want: lifecycleLaneClaimIncomplete},
		{name: "negative receipt lane epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.threadEpoch = laneClaimPointer(int64(-1))
		}, want: lifecycleLaneClaimIncomplete},
		{name: "successor dispatch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.claim.DispatchID = laneClaimPointer(uuid.UUID{15: 55})
		}, want: lifecycleLaneClaimMismatch},
		{name: "successor claim token", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.claim.Token = laneClaimPointer(uuid.UUID{15: 55})
		}, want: lifecycleLaneClaimMismatch},
		{name: "successor receipt token", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.token = laneClaimPointer(uuid.UUID{15: 55})
		}, want: lifecycleLaneClaimMismatch},
		{name: "new session epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.scope.session.epochs.AdmissionEpoch++
		}, want: lifecycleLaneClaimStale},
		{name: "new lane epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.scope.threads[0].epochs.AdmissionEpoch++
		}, want: lifecycleLaneClaimStale},
		{name: "recorded session epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.sessionEpoch = laneClaimPointer(int64(1))
		}, want: lifecycleLaneClaimStale},
		{name: "recorded lane epoch", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.threadEpoch = laneClaimPointer(int64(1))
		}, want: lifecycleLaneClaimStale},
		{name: "claimed turn", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) {
			o.receipt.turn = laneClaimPointer(int32(1))
		}, want: lifecycleLaneClaimStale},
		{name: "running claim follows last completed turn", edit: func(o *lifecycleLaneClaimObservation, e *lifecycleLaneClaimExpected) {
			o.currentTurn = e.turn - 1
			o.scope.threads[0].status = models.ThreadStatusRunning
		}, want: lifecycleLaneClaimExact},
		{name: "completed claim keeps receipt identity", edit: func(o *lifecycleLaneClaimObservation, e *lifecycleLaneClaimExpected) {
			o.currentTurn = e.turn
			o.scope.threads[0].status = models.ThreadStatusIdle
		}, want: lifecycleLaneClaimExact},
		{name: "later lane turn remains raw evidence", edit: func(o *lifecycleLaneClaimObservation, _ *lifecycleLaneClaimExpected) { o.currentTurn++ }, want: lifecycleLaneClaimExact},
		{name: "explicit zero is not NULL", edit: func(o *lifecycleLaneClaimObservation, e *lifecycleLaneClaimExpected) {
			e.sessionEpoch, e.threadEpoch, e.turn = 0, 0, 0
			o.scope.session.epochs.AdmissionEpoch, o.scope.threads[0].epochs.AdmissionEpoch = 0, 0
			o.currentTurn, o.receipt.turn = 0, laneClaimPointer(int32(0))
			o.receipt.sessionEpoch, o.receipt.threadEpoch = laneClaimPointer(int64(0)), laneClaimPointer(int64(0))
		}, want: lifecycleLaneClaimExact},
		{name: "full signed widths", edit: func(o *lifecycleLaneClaimObservation, e *lifecycleLaneClaimExpected) {
			e.sessionEpoch, e.threadEpoch, e.turn = math.MaxInt64, math.MaxInt64, math.MaxInt32
			o.scope.session.epochs.AdmissionEpoch, o.scope.threads[0].epochs.AdmissionEpoch = math.MaxInt64, math.MaxInt64
			o.currentTurn, o.receipt.turn = math.MaxInt32, laneClaimPointer(int32(math.MaxInt32))
			o.receipt.sessionEpoch, o.receipt.threadEpoch = laneClaimPointer(int64(math.MaxInt64)), laneClaimPointer(int64(math.MaxInt64))
		}, want: lifecycleLaneClaimExact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f, expected, want := newLaneClaimUnitFixture()
			if tt.edit != nil {
				tt.edit(&want, &expected)
			}
			want.comparison = tt.want
			expectLaneClaimLocks(mock, f.orgID, expected, want.scope)
			mock.ExpectQuery(laneClaimSQL).WithArgs(f.orgID, expected.sessionID, expected.threadID).WillReturnRows(laneClaimUnitRows(expected, f.orgID, want))
			mock.ExpectQuery(laneClaimReceiptSQL).WithArgs(f.orgID, expected.sessionID, expected.threadID, expected.dispatchID).WillReturnRows(laneClaimReceiptUnitRows(want.receipt))
			wrapper := &lifecycleScopeConcurrencyTx{Tx: mock}
			actual, err := verifyLifecycleLaneClaimInTx(ctx, wrapper, f.orgID, expected)
			require.NoError(t, err, "raw evidence should yield a comparison, never inferred eligibility")
			require.Equal(t, want, actual, "preserve every raw identity and independent NULL while classifying equality")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.NoError(t, mock.ExpectationsWereMet(), "one exact ordered read sequence must not discover other claims or mutate settings")
		})
	}
}

func TestLifecycleLaneClaimErrorsDiscardAllObservations(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"policy", "interlock", "busy", "parent", "lane batch", "claim query", "claim scan", "claim scope", "claim cancellation", "receipt query", "receipt missing", "receipt scan", "receipt org", "receipt parent", "receipt lane", "receipt identity", "receipt cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f, expected, observation := newLaneClaimUnitFixture()
			cause := errors.New("synthetic receipt read failure")
			wantErr := cause
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx = cancelCtx
			owner := &lifecycleScopeConcurrencyTx{Tx: mock}
			var tx pgx.Tx = owner
			type statement struct {
				sql  string
				args []any
				rows *pgxmock.Rows
				err  error
			}
			steps := []statement{
				{sql: lifecycleTransactionPolicyQuery, rows: lifecycleScopePolicyRows().AddRow("read committed", "1", "250ms", "1500ms")},
				{sql: cutoverSharedSQL, args: []any{"session_lifecycle_cutover:" + f.orgID.String() + ":" + expected.sessionID.String()}, rows: cutoverLockRows().AddRow(true)},
				{sql: lifecycleTransactionPolicyQuery, rows: lifecycleScopePolicyRows().AddRow("read committed", "1", "250ms", "1500ms")},
				{sql: lifecycleSessionScopeLockQuery, args: []any{f.orgID, expected.sessionID}, rows: lifecycleScopeSessionRows(observation.scope.session)},
				{sql: lifecycleThreadScopeLockQuery, args: []any{f.orgID, expected.sessionID, []uuid.UUID{expected.threadID}}, rows: lifecycleScopeThreadRows(observation.scope.threads...)},
				{sql: laneClaimSQL, args: []any{f.orgID, expected.sessionID, expected.threadID}, rows: laneClaimUnitRows(expected, f.orgID, observation)},
				{sql: laneClaimReceiptSQL, args: []any{f.orgID, expected.sessionID, expected.threadID, expected.dispatchID}, rows: laneClaimReceiptUnitRows(observation.receipt)},
			}
			last := len(steps) - 1
			switch scenario {
			case "policy":
				last = 0
				steps[last].err = cause
			case "interlock":
				last = 1
				steps[last].err = cause
			case "busy":
				last = 1
				steps[last].rows = cutoverLockRows().AddRow(false)
			case "parent":
				last = 3
				steps[last].err = cause
			case "lane batch":
				last = 4
				steps[last].err = cause
			case "claim query":
				last = 5
				steps[last].err = cause
			case "claim scan":
				last = 5
				steps[last].rows = pgxmock.NewRows([]string{"bad"}).AddRow("bad")
				wantErr = nil
			case "claim scope":
				last = 5
				steps[last].rows = laneClaimUnitRows(expected, uuid.New(), observation)
				wantErr = pgx.ErrNoRows
			case "claim cancellation":
				last = 5
				tx = &laneClaimCancellingTx{Tx: tx, query: laneClaimSQL, cancel: cancel}
				wantErr = context.Canceled
			case "receipt query":
				steps[last].err = cause
			case "receipt missing":
				steps[last].rows = laneClaimReceiptUnitRows()
				wantErr = pgx.ErrNoRows
			case "receipt scan":
				r := observation.receipt
				steps[last].rows = pgxmock.NewRows([]string{"id", "org_id", "session_id", "thread_id", "session_epoch", "thread_epoch", "admission_claim_token", "admission_claim_turn"}).AddRow(r.id, r.orgID, r.sessionID, r.threadID, r.sessionEpoch, r.threadEpoch, r.token, "not-an-integer")
				wantErr = nil
			case "receipt cancellation":
				tx = &laneClaimCancellingTx{Tx: tx, query: laneClaimReceiptSQL, cancel: cancel}
				wantErr = context.Canceled
			default:
				r := observation.receipt
				switch scenario {
				case "receipt org":
					r.orgID = uuid.New()
				case "receipt parent":
					r.sessionID = uuid.New()
				case "receipt lane":
					r.threadID = uuid.New()
				case "receipt identity":
					r.id = uuid.New()
				}
				steps[last].rows = laneClaimReceiptUnitRows(r)
				wantErr = pgx.ErrNoRows
			}
			// Expect only the prefix ending at the failed statement: no later
			// query, settings change, retry or settlement may follow an error.
			for _, step := range steps[:last+1] {
				query := mock.ExpectQuery(step.sql).WithArgs(step.args...)
				if step.err != nil {
					query.WillReturnError(step.err)
				} else {
					query.WillReturnRows(step.rows)
				}
			}
			actual, err := verifyLifecycleLaneClaimInTx(ctx, tx, f.orgID, expected)
			require.Error(t, err, "every failed prerequisite/read must fail closed")
			if scenario == "busy" {
				var busy *lifecycleCutoverInterlockBusyError
				require.ErrorAs(t, err, &busy, "only a real false try-lock produces the private busy cause")
			} else if wantErr != nil {
				require.ErrorIs(t, err, wantErr, "read and context causes must remain inspectable by the transaction owner")
			}
			require.Equal(t, lifecycleLaneClaimObservation{}, actual, "even a final receipt failure must discard the locked scope and claim")
			requireLifecycleScopeNoSettlement(t, owner)
			require.NoError(t, mock.ExpectationsWereMet(), "no later statement or owner settlement may follow failure")
		})
	}
}

func laneClaimPointer[T any](value T) *T { return &value }

func newLaneClaimUnitFixture() (lifecycleScopeMockFixture, lifecycleLaneClaimExpected, lifecycleLaneClaimObservation) {
	f := newLifecycleScopeMockFixture()
	f.session.epochs.AdmissionEpoch = 19
	e := lifecycleLaneClaimExpected{sessionID: f.sessionID, threadID: f.first.id, dispatchID: uuid.UUID{15: 41}, token: uuid.UUID{15: 42}, sessionEpoch: 19, threadEpoch: 23, turn: 7}
	o := lifecycleLaneClaimObservation{scope: lockedLifecycleScope{session: f.session, threads: []lifecycleLockedThread{f.first}}, claim: LifecycleAdmissionClaimState{DispatchID: laneClaimPointer(e.dispatchID), Token: laneClaimPointer(e.token)}, currentTurn: e.turn,
		receipt: lifecycleLaneClaimReceipt{id: e.dispatchID, orgID: f.orgID, sessionID: e.sessionID, threadID: e.threadID, sessionEpoch: laneClaimPointer(e.sessionEpoch), threadEpoch: laneClaimPointer(e.threadEpoch), token: laneClaimPointer(e.token), turn: laneClaimPointer(e.turn)}, comparison: lifecycleLaneClaimExact}
	return f, e, o
}

func expectLaneClaimLocks(mock pgxmock.PgxPoolIface, orgID uuid.UUID, e lifecycleLaneClaimExpected, scope lockedLifecycleScope) {
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(cutoverSharedSQL).WithArgs("session_lifecycle_cutover:" + orgID.String() + ":" + e.sessionID.String()).WillReturnRows(cutoverLockRows().AddRow(true))
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(orgID, e.sessionID).WillReturnRows(lifecycleScopeSessionRows(scope.session))
	mock.ExpectQuery(lifecycleThreadScopeLockQuery).WithArgs(orgID, e.sessionID, []uuid.UUID{e.threadID}).WillReturnRows(lifecycleScopeThreadRows(scope.threads...))
}

func laneClaimUnitRows(e lifecycleLaneClaimExpected, orgID uuid.UUID, o lifecycleLaneClaimObservation) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "session_id", "current_turn", "admission_claim_dispatch_id", "admission_claim_token"}).AddRow(e.threadID, orgID, e.sessionID, o.currentTurn, o.claim.DispatchID, o.claim.Token)
}

func laneClaimReceiptUnitRows(receipts ...lifecycleLaneClaimReceipt) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "thread_id", "session_epoch", "thread_epoch", "admission_claim_token", "admission_claim_turn"})
	for _, r := range receipts {
		rows.AddRow(r.id, r.orgID, r.sessionID, r.threadID, r.sessionEpoch, r.threadEpoch, r.token, r.turn)
	}
	return rows
}

type laneClaimCancellingTx struct {
	pgx.Tx
	query  string
	cancel context.CancelFunc
}

func (tx *laneClaimCancellingTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if strings.Join(strings.Fields(query), " ") == strings.Join(strings.Fields(tx.query), " ") {
		return laneClaimCancellingRow{Row: row, cancel: tx.cancel}
	}
	return row
}

type laneClaimCancellingRow struct {
	pgx.Row
	cancel context.CancelFunc
}

func (row laneClaimCancellingRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	row.cancel()
	return err
}
