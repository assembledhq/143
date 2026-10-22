package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCutoverRetriesUseFreshUnitsAfterConfirmedAbort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, target string
		wrapped      bool
	}{
		{name: "shared actual conflict", target: "shared"},
		{name: "shared wrapped actual conflict", target: "shared", wrapped: true},
		{name: "exclusive actual conflict", target: "exclusive"},
		{name: "exclusive wrapped actual conflict", target: "exclusive", wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			for _, acquired := range []bool{false, true} {
				expectCutoverRetryAttempt(mock, fixture, tt.target, acquired)
				if acquired {
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
			}

			var events []string
			var transactions []*cutoverRetryTrackingTx
			var deadline time.Time
			var rollbackFinished time.Time
			var retryWait time.Duration
			starter := lifecycleTransactionStarterFunc(func(unit context.Context, options pgx.TxOptions) (pgx.Tx, error) {
				require.Equal(t, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, options, "every conflict retry must begin a fresh explicit READ COMMITTED unit")
				actualDeadline, ok := unit.Deadline()
				require.True(t, ok, "acquisition and every callback must retain the owner's deadline")
				if deadline.IsZero() {
					deadline = actualDeadline
				} else {
					require.Equal(t, deadline, actualDeadline, "retry must not reset or extend the whole lifecycle budget")
					require.Equal(t, "rollback", events[len(events)-1], "a replacement BeginTx must follow successful whole rollback")
					retryWait = time.Since(rollbackFinished)
				}
				tx, err := mock.BeginTx(unit, options)
				if err != nil {
					return nil, err
				}
				events = append(events, "begin")
				tracked := &cutoverRetryTrackingTx{Tx: tx, events: &events, rollbackFinished: &rollbackFinished}
				transactions = append(transactions, tracked)
				return tracked, nil
			})
			calls := 0
			err := runLifecycleTransaction(ctx, starter, func(unit context.Context, tx pgx.Tx) error {
				calls++
				events = append(events, "work")
				require.Same(t, transactions[calls-1], tx, "callback must receive this attempt's newly acquired transaction")
				actualDeadline, ok := unit.Deadline()
				require.True(t, ok, "callback must remain inside the single acquisition budget")
				require.Equal(t, deadline, actualDeadline, "every callback must revalidate within the unchanged deadline")
				_, lockErr := callCutoverUnitTarget(unit, tx, fixture.orgID, fixture.sessionID, tt.target)
				if calls == 1 {
					requireCutoverPrivateBusy(t, lockErr)
					if tt.wrapped {
						return fmt.Errorf("admission interlock: %w", lockErr)
					}
					return lockErr
				}
				require.NoError(t, lockErr, "fresh callback must perform policy and interlock revalidation rather than reuse the aborted result")
				return nil
			})
			require.NoError(t, err, "a successful fresh attempt may commit after an actual interlock conflict was fully aborted")
			require.Equal(t, 2, calls, "one actual conflict must cause exactly one fresh callback invocation")
			require.Len(t, transactions, 2, "one conflict should acquire exactly two transaction units")
			require.NotSame(t, transactions[0], transactions[1], "replacement work must use a fresh BeginTx result rather than the aborted unit")
			require.Equal(t, []string{"begin", "work", "rollback", "begin", "work", "commit"}, events,
				"only the final successful unit may commit and rollback must precede replacement acquisition")
			require.GreaterOrEqual(t, retryWait, 25*time.Millisecond, "replacement acquisition must respect the existing minimum jitter after rollback completes")
			require.NoError(t, mock.ExpectationsWereMet(), "every attempt must repeat setup and real try-lock work with no hidden settlement or extra acquisition")
		})
	}
}

func TestLifecycleCutoverRetryExhaustionPreservesLastActualConflict(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"shared", "exclusive"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			for range 3 {
				expectCutoverRetryAttempt(mock, fixture, target, false)
				mock.ExpectRollback()
			}
			calls := 0
			var last error
			err := runLifecycleTransaction(ctx, mock, func(unit context.Context, tx pgx.Tx) error {
				calls++
				_, lockErr := callCutoverUnitTarget(unit, tx, fixture.orgID, fixture.sessionID, target)
				requireCutoverPrivateBusy(t, lockErr)
				last = fmt.Errorf("attempt %d actual interlock: %w", calls, lockErr)
				return last
			})
			var contention *LifecycleContentionError
			require.ErrorAs(t, err, &contention, "three confirmed-aborted interlock conflicts must return the existing bounded contention result")
			require.Equal(t, 3, contention.Attempts, "the original unit and two retries must exhaust the unchanged three-attempt cap")
			require.Equal(t, 3, calls, "exhaustion must not acquire or execute a fourth attempt")
			require.ErrorIs(t, err, ErrLifecycleContention, "callers should recognize safe bounded exhaustion without parsing text")
			require.Same(t, last, contention.Cause, "exhaustion must preserve the final actual conflict rather than an earlier attempt")
			var busy *lifecycleCutoverInterlockBusyError
			require.ErrorAs(t, err, &busy, "private acquisition conflict must remain available through the public exhaustion result")
			require.NotNil(t, busy, "exhaustion must retain a real private conflict cause")
			var pgErr *pgconn.PgError
			require.False(t, errors.As(err, &pgErr), "actual advisory conflict exhaustion must not fabricate a PostgreSQL SQLSTATE")
			require.NoError(t, mock.ExpectationsWereMet(), "all three attempts must roll back and no attempt may commit")
		})
	}
}

func TestLifecycleCutoverRetryRejectsNilAndLookalikeCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause func() error
	}{
		{name: "typed nil private conflict", cause: func() error {
			var busy *lifecycleCutoverInterlockBusyError
			return busy
		}},
		{name: "wrapped typed nil private conflict", cause: func() error {
			var busy *lifecycleCutoverInterlockBusyError
			return fmt.Errorf("outer: %w", busy)
		}},
		{name: "application text lookalike", cause: func() error { return errors.New("lifecycle cutover interlock busy") }},
		{name: "wrapped application text lookalike", cause: func() error {
			return fmt.Errorf("outer: %w", errors.New("lifecycle cutover interlock busy"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			cause := tt.cause()
			require.False(t, retryableLifecycleTransaction(cause), "only a real nonnil private cause may extend the existing retry policy")
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			expectCutoverRetrySetup(mock)
			mock.ExpectRollback()
			calls := 0
			err := runLifecycleTransaction(ctx, mock, func(context.Context, pgx.Tx) error {
				calls++
				return cause
			})
			require.ErrorIs(t, err, cause, "nonretryable private-nil or application error should retain its cause")
			require.Equal(t, 1, calls, "a nil typed cause or error text must not authorize callback replay")
			require.NotErrorIs(t, err, ErrLifecycleContention, "nonretryable application errors are not safe retry exhaustion")
			require.NoError(t, mock.ExpectationsWereMet(), "excluded causes must receive one cleanup and no replacement transaction")
		})
	}
}

func TestLifecycleCutoverRetryNeverReplaysBeginOrCommitFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, target, phase string
		wrapped             bool
	}{
		{name: "shared busy at begin", target: "shared", phase: "begin"},
		{name: "wrapped exclusive busy at begin", target: "exclusive", phase: "begin", wrapped: true},
		{name: "wrapped shared busy at commit", target: "shared", phase: "commit", wrapped: true},
		{name: "exclusive busy at commit", target: "exclusive", phase: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := actualCutoverBusyForRetryTest(t, tt.target)
			if tt.wrapped {
				cause = fmt.Errorf("transaction boundary: %w", cause)
			}
			require.True(t, retryableLifecycleTransaction(cause), "boundary test should use an otherwise eligible actual conflict")
			mock, ctx := newLifecycleScopeMock(t)
			begin := mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			wantCalls := 0
			if tt.phase == "begin" {
				begin.WillReturnError(cause)
			} else {
				wantCalls = 1
				expectCutoverRetrySetup(mock)
				mock.ExpectCommit().WillReturnError(cause)
				mock.ExpectRollback()
			}
			calls := 0
			err := runLifecycleTransaction(ctx, mock, func(context.Context, pgx.Tx) error {
				calls++
				return nil
			})
			require.ErrorIs(t, err, cause, "begin or commit failure must retain its eligible-looking cause without replay inference")
			require.Equal(t, wantCalls, calls, "retry eligibility cannot override begin or commit-phase exclusions")
			require.NotErrorIs(t, err, ErrLifecycleContention, "ambiguous boundary errors must not be presented as confirmed-aborted exhaustion")
			require.NoError(t, mock.ExpectationsWereMet(), "boundary failure must never start another transaction even after successful cleanup")
		})
	}
}

type cutoverRetryTrackingTx struct {
	pgx.Tx
	events           *[]string
	rollbackFinished *time.Time
}

func (tx *cutoverRetryTrackingTx) Commit(ctx context.Context) error {
	*tx.events = append(*tx.events, "commit")
	return tx.Tx.Commit(ctx)
}

func (tx *cutoverRetryTrackingTx) Rollback(ctx context.Context) error {
	err := tx.Tx.Rollback(ctx)
	if err == nil {
		*tx.events = append(*tx.events, "rollback")
		*tx.rollbackFinished = time.Now()
	}
	return err
}

func expectCutoverRetrySetup(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow("read committed"))
	mock.ExpectExec(expectedLifecycleSettings).WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func expectCutoverRetryAttempt(mock pgxmock.PgxPoolIface, fixture cutoverUnitFixture, target string, acquired bool) {
	mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	expectCutoverRetrySetup(mock)
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(cutoverTargetSQL(target)).WithArgs(fixture.key).WillReturnRows(cutoverLockRows().AddRow(acquired))
}

func actualCutoverBusyForRetryTest(t *testing.T, target string) error {
	t.Helper()
	mock, ctx := newLifecycleScopeMock(t)
	fixture := newCutoverUnitFixture()
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(cutoverTargetSQL(target)).WithArgs(fixture.key).WillReturnRows(cutoverLockRows().AddRow(false))
	_, err := callCutoverUnitTarget(ctx, mock, fixture.orgID, fixture.sessionID, target)
	requireCutoverPrivateBusy(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "eligible boundary cause must originate in an actual false advisory try-lock result")
	return err
}
