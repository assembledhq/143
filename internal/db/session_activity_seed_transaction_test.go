package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type seedActivityTransactionFixture struct {
	orgID, sessionID, threadID, runtimeID, leaseToken, batchID, phaseID uuid.UUID
	messageIDs                                                          []int64
	turn                                                                int
	requestedAt                                                         time.Time
}

func newSeedActivityTransactionFixture() seedActivityTransactionFixture {
	return seedActivityTransactionFixture{
		orgID: uuid.New(), sessionID: uuid.New(), threadID: uuid.New(), runtimeID: uuid.New(), leaseToken: uuid.New(), batchID: uuid.New(), phaseID: uuid.New(),
		messageIDs: []int64{11, 12}, turn: 2, requestedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
	}
}

func callSeedActivityInTx(ctx context.Context, store *SessionActivityPhaseStore, tx pgx.Tx, f seedActivityTransactionFixture) (models.SessionActivityPhase, *models.ThreadInboxDeliveryBatch, error) {
	return store.StartSeedPhaseInTx(ctx, f.orgID, tx, f.sessionID, f.threadID, f.turn, f.runtimeID, f.leaseToken, f.messageIDs, f.requestedAt)
}

func callSeedActivityWrapper(ctx context.Context, store *SessionActivityPhaseStore, f seedActivityTransactionFixture) (models.SessionActivityPhase, *models.ThreadInboxDeliveryBatch, error) {
	return store.StartSeedPhase(ctx, f.orgID, f.sessionID, f.threadID, f.turn, f.runtimeID, f.leaseToken, f.messageIDs, f.requestedAt)
}

func TestSeedActivityTransactionPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct{ mode, failure string }{}
	for _, mode := range []string{"caller transaction", "legacy wrapper"} {
		for _, failure := range []string{"", "duplicate ids", "zero turn", "thread lock", "runtime lock", "materialize", "historical batches", "historical batches collect", "claim", "validate", "missing input", "unowned input", "sequence gap", "batch", "batch collect", "batch missing", "ack", "ack mismatch", "phase thread lock", "phase batch", "phase batch collect", "phase batch missing", "apply", "apply mismatch", "phase runtime lock", "retire", "phase insert", "phase insert collect", "watermark"} {
			tests = append(tests, struct{ mode, failure string }{mode, failure})
		}
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture := newSeedActivityTransactionFixture()
			if tt.failure == "duplicate ids" {
				fixture.messageIDs = []int64{11, 12, 12}
			} else if tt.failure == "zero turn" {
				fixture.turn = 0
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this seed operation's isolated SQL mock")
			t.Cleanup(mock.Close)
			mock.ExpectBegin()
			var caller pgx.Tx
			var wrapped *sessionPersistenceOwnedTx
			store := NewSessionActivityPhaseStore(mock)
			if tt.mode == "caller transaction" {
				caller, err = mock.Begin(ctx)
				require.NoError(t, err, "only the outer caller begins this transaction")
				wrapped = &sessionPersistenceOwnedTx{Tx: caller}
				store = NewSessionActivityPhaseStore(nil)
			}
			expectedPhase, expectedBatch, expectedErr := expectSeedActivityTransactionWrites(mock, fixture, tt.failure)
			if expectedErr != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			var phase models.SessionActivityPhase
			var batch *models.ThreadInboxDeliveryBatch
			if tt.mode == "caller transaction" {
				phase, batch, err = callSeedActivityInTx(ctx, store, wrapped, fixture)
				require.Empty(t, wrapped.settlements, "neither seed persistence nor its transitive phase helper may begin a savepoint, commit or roll back")
				if expectedErr != nil {
					require.NoError(t, caller.Rollback(ctx), "only the caller aborts the entire failed seed and phase unit")
				} else {
					require.NoError(t, caller.Commit(ctx), "only the caller makes seed, batch, phase and watermark writes durable")
				}
			} else {
				phase, batch, err = callSeedActivityWrapper(ctx, store, fixture)
			}
			if expectedErr != nil {
				require.EqualError(t, err, expectedErr.Error(), "each failure must retain its exact original seed or phase error wrapper")
				if errors.Is(expectedErr, ErrInboxBatchConflict) {
					require.ErrorIs(t, err, ErrInboxBatchConflict, "seed identity and row-count conflicts must retain the existing sentinel")
				} else if cause := errors.Unwrap(expectedErr); cause != nil {
					require.ErrorIs(t, err, cause, "wrapped SQL and collect failures must preserve the original database error classification")
				}
				require.Equal(t, models.SessionActivityPhase{}, phase, "failed units must return no accepted provider phase")
				require.Nil(t, batch, "failed units must return no accepted seed receipt, including after earlier writes")
			} else {
				require.NoError(t, err, "the extracted seed operation must preserve its legacy successful persistence sequence")
				require.Equal(t, expectedPhase, phase, "return the complete phase with normalized time and exact batch trigger")
				require.Equal(t, expectedBatch, batch, "return the original allocated batch shape with started status and normalized start time")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all seed and transitive phase statements must retain exact scoped arguments and original lock/write order")
		})
	}
}

func expectSeedActivityTransactionWrites(mock pgxmock.PgxPoolIface, f seedActivityTransactionFixture, failure string) (models.SessionActivityPhase, *models.ThreadInboxDeliveryBatch, error) {
	at := f.requestedAt.Truncate(time.Microsecond)
	databaseErr := errors.New("synthetic seed database failure")
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
	exec := func(name, prefix, pattern string, args []any, affected int64) bool {
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
		expectation.WillReturnResult(pgxmock.NewResult("UPDATE", affected))
		return true
	}
	scope := []any{f.orgID, f.sessionID, f.threadID}
	if !query("thread lock", "lock seed activity thread", "", `SELECT id FROM session_threads WHERE org_id = \$1 AND session_id = \$2 AND id = \$3 FOR UPDATE`, scope, pgxmock.NewRows([]string{"id"}).AddRow(f.threadID)) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	leaseArgs := []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, f.leaseToken, at}
	if !query("runtime lock", "lock seed runtime lease", "", `(?s)SELECT last_acked_sequence FROM thread_runtimes.+org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND id = \$4.+lease_token = \$5.+GREATEST\(\$6, clock_timestamp\(\)\).+FOR UPDATE`, leaseArgs, pgxmock.NewRows([]string{"last_acked_sequence"}).AddRow(int64(0))) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !exec("materialize", "backfill legacy seed inbox", `(?s)WITH missing AS.+m.org_id = \$1 AND m.session_id = \$2.+m.id = ANY\(\$4\).+INSERT INTO thread_inbox_entries`, []any{f.orgID, f.sessionID, f.threadID, f.messageIDs}, 2) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !query("historical batches", "lock historical seed batches", "collect locked seed batches", `(?s)SELECT b.id FROM thread_inbox_delivery_batches b.+b.org_id = \$1 AND b.session_id = \$2 AND b.thread_id = \$3.+ORDER BY b.sequence_start, b.id FOR UPDATE OF b`, []any{f.orgID, f.sessionID, f.threadID, f.messageIDs}, pgxmock.NewRows([]string{"id"}).AddRow(f.batchID)) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !exec("claim", "claim resumed seed entries", `(?s)UPDATE thread_inbox_entries e.+e.org_id = \$1 AND e.session_id = \$2 AND e.thread_id = \$3.+e.message_id = ANY\(\$5\).+e.applied_at IS NULL`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, f.messageIDs}, 2) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	total, owned, first, last := int64(2), int64(2), int64(1), int64(2)
	switch failure {
	case "missing input":
		total = 1
	case "unowned input":
		owned = 1
	case "sequence gap":
		first, last = 2, 3
	}
	if !query("validate", "validate resumed seed entries", "", `(?s)SELECT count\(\*\).+FROM thread_inbox_entries.+org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND message_id = ANY\(\$5\)`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, f.messageIDs}, pgxmock.NewRows([]string{"total", "owned", "first", "last"}).AddRow(total, owned, first, last)) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if failure == "missing input" || failure == "unowned input" || failure == "sequence gap" {
		return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
	}
	batchRows := seedActivityTransactionBatchRows(f, models.InboxDeliveryBatchAcknowledged)
	if failure == "batch missing" {
		batchRows = pgxmock.NewRows(inboxBatchTestColumns)
	}
	if !query("batch", "allocate resumed seed batch", "collect resumed seed batch", `(?s)INSERT INTO thread_inbox_delivery_batches AS b.+ON CONFLICT.+WHERE b.session_id = \$2 AND b.status = 'acknowledged' AND b.started_at IS NULL`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, first, last, at}, batchRows) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if failure == "batch missing" {
		return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
	}
	entryArgs := []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, first, last, at}
	if !exec("ack", "acknowledge resumed seed entries", `(?s)UPDATE thread_inbox_entries.+SET delivery_state = 'acked'.+WHERE org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND runtime_id = \$4.+delivery_state = 'delivering' AND applied_at IS NULL`, entryArgs, 2) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !query("phase thread lock", "lock activity phase thread", "", `SELECT id FROM session_threads WHERE org_id = \$1 AND session_id = \$2 AND id = \$3 FOR UPDATE`, scope, pgxmock.NewRows([]string{"id"}).AddRow(f.threadID)) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	startedBatchRows := seedActivityTransactionBatchRows(f, models.InboxDeliveryBatchStarted)
	if failure == "phase batch missing" {
		startedBatchRows = pgxmock.NewRows(inboxBatchTestColumns)
	}
	if !query("phase batch", "start inbox delivery batch", "collect started inbox delivery batch", `(?s)UPDATE thread_inbox_delivery_batches.+WHERE org_id = \$1 AND id = \$2 AND session_id = \$3 AND thread_id = \$4.+status = 'acknowledged'`, []any{f.orgID, f.batchID, f.sessionID, f.threadID, at}, startedBatchRows) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if failure == "phase batch missing" {
		return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
	}
	if !exec("apply", "mark inbox delivery batch entries applied", `(?s)UPDATE thread_inbox_entries.+SET applied_at = COALESCE\(applied_at, \$7\).+WHERE org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND runtime_id = \$4.+delivery_state = 'acked'`, entryArgs, 2) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !query("phase runtime lock", "validate active activity phase runtime lease", "", `(?s)SELECT id FROM thread_runtimes.+org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND id = \$4.+lease_token = \$5.+lease_expires_at > clock_timestamp\(\).+FOR UPDATE`, leaseArgs, pgxmock.NewRows([]string{"id"}).AddRow(f.runtimeID)) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !exec("retire", "retire terminal runtime activity phase", `WITH terminal_owners AS MATERIALIZED`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, at}, 0) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	phaseRows := pgxmock.NewRows(activityPhaseTestColumns).AddRow(f.phaseID, f.orgID, f.sessionID, f.threadID, f.turn, 1, models.ActivityPhaseStatusRunning, nil, at, nil, &f.runtimeID, models.ActivityPhaseTriggerInboxBatch, &f.batchID, &first, &last, at, at)
	if !query("phase insert", "insert activity phase", "collect inserted activity phase", `(?s)INSERT INTO session_activity_phases.+WHERE org_id = \$1 AND thread_id = \$3 AND turn_number = \$4`, []any{f.orgID, f.sessionID, f.threadID, f.turn, at, &f.runtimeID, models.ActivityPhaseTriggerInboxBatch, &f.batchID, &first, &last}, phaseRows) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	if !exec("watermark", "advance resumed seed watermark", `(?s)UPDATE thread_runtimes.+last_acked_sequence = \$5.+WHERE org_id = \$1 AND session_id = \$2 AND thread_id = \$3 AND id = \$4`, []any{f.orgID, f.sessionID, f.threadID, f.runtimeID, last}, 1) {
		return models.SessionActivityPhase{}, nil, expectedErr
	}
	phase := models.SessionActivityPhase{ID: f.phaseID, OrgID: f.orgID, SessionID: f.sessionID, ThreadID: f.threadID, TurnNumber: f.turn, PhaseNumber: 1, Status: models.ActivityPhaseStatusRunning, StartedAt: at, RuntimeID: &f.runtimeID, TriggerKind: models.ActivityPhaseTriggerInboxBatch, TriggerBatchID: &f.batchID, TriggerSequenceStart: &first, TriggerSequenceEnd: &last, CreatedAt: at, UpdatedAt: at}
	batch := &models.ThreadInboxDeliveryBatch{ID: f.batchID, OrgID: f.orgID, SessionID: f.sessionID, ThreadID: f.threadID, RuntimeID: f.runtimeID, SequenceStart: first, SequenceEnd: last, Status: models.InboxDeliveryBatchStarted, AcknowledgedAt: at, StartedAt: &at, CreatedAt: at, UpdatedAt: at}
	return phase, batch, nil
}

func seedActivityTransactionBatchRows(f seedActivityTransactionFixture, status models.InboxDeliveryBatchStatus) *pgxmock.Rows {
	at := f.requestedAt.Truncate(time.Microsecond)
	var startedAt *time.Time
	if status == models.InboxDeliveryBatchStarted {
		startedAt = &at
	}
	return pgxmock.NewRows(inboxBatchTestColumns).AddRow(f.batchID, f.orgID, f.sessionID, f.threadID, f.runtimeID, int64(1), int64(2), status, at, startedAt, nil, at, at)
}

func TestSeedActivityTransactionInputValidation(t *testing.T) {
	t.Parallel()
	tests := []struct{ mode, invalid string }{}
	for _, mode := range []string{"caller transaction", "legacy wrapper"} {
		for _, invalid := range []string{"negative turn", "zero time", "nil ids", "empty ids"} {
			tests = append(tests, struct{ mode, invalid string }{mode, invalid})
		}
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.invalid, func(t *testing.T) {
			t.Parallel()
			fixture := newSeedActivityTransactionFixture()
			switch tt.invalid {
			case "negative turn":
				fixture.turn = -1
			case "zero time":
				fixture.requestedAt = time.Time{}
			case "nil ids":
				fixture.messageIDs = nil
			case "empty ids":
				fixture.messageIDs = []int64{}
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an input-validation mock with no allowed SQL or settlement")
			t.Cleanup(mock.Close)
			store := NewSessionActivityPhaseStore(mock)
			var phase models.SessionActivityPhase
			var batch *models.ThreadInboxDeliveryBatch
			if tt.mode == "caller transaction" {
				tx := &sessionPersistenceOwnedTx{}
				phase, batch, err = callSeedActivityInTx(context.Background(), store, tx, fixture)
				require.Empty(t, tx.settlements, "invalid caller input must not settle or create a transaction")
			} else {
				phase, batch, err = callSeedActivityWrapper(context.Background(), store, fixture)
			}
			require.EqualError(t, err, "start seed phase: invalid turn, time, or seed", "both entrypoints must preserve exact validation before any SQL and the wrapper's Begin")
			require.Equal(t, models.SessionActivityPhase{}, phase, "invalid input must return no provider phase")
			require.Nil(t, batch, "invalid input must return no seed batch")
			require.NoError(t, mock.ExpectationsWereMet(), "validation must not begin or query a transaction")
		})
	}
}

func TestSeedActivityTransactionRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *sessionPersistenceOwnedTx
	for _, tt := range []struct {
		name string
		tx   pgx.Tx
	}{{"nil interface", nil}, {"typed nil", typedNil}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			phase, batch, err := callSeedActivityInTx(context.Background(), NewSessionActivityPhaseStore(nil), tt.tx, newSeedActivityTransactionFixture())
			require.EqualError(t, err, "start seed phase: transaction is required", "a missing explicit transaction must fail before any fallback or SQL")
			require.Equal(t, models.SessionActivityPhase{}, phase, "missing transaction must return no provider phase")
			require.Nil(t, batch, "missing transaction must return no seed batch")
		})
	}
}

func TestSeedActivityTransactionLegacySettlementErrors(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"begin", "commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			fixture := newSeedActivityTransactionFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this legacy transaction-boundary mock")
			t.Cleanup(mock.Close)
			boundaryErr := errors.New("synthetic settlement failure")
			expected := "begin seed activity phase: synthetic settlement failure"
			if failure == "begin" {
				mock.ExpectBegin().WillReturnError(boundaryErr)
			} else {
				mock.ExpectBegin()
				_, _, expectedErr := expectSeedActivityTransactionWrites(mock, fixture, "")
				require.NoError(t, expectedErr, "configure the full successful persistence sequence before the failed commit")
				mock.ExpectCommit().WillReturnError(boundaryErr)
				mock.ExpectRollback()
				expected = "commit resumed seed activity: synthetic settlement failure"
			}
			phase, batch, err := callSeedActivityWrapper(context.Background(), NewSessionActivityPhaseStore(mock), fixture)
			require.EqualError(t, err, expected, "legacy Begin and Commit errors must retain their exact original wrappers")
			require.ErrorIs(t, err, boundaryErr, "the transaction owner must retain the original boundary error classification")
			require.Equal(t, models.SessionActivityPhase{}, phase, "unconfirmed commit must not return an accepted phase")
			require.Nil(t, batch, "unconfirmed commit must not return an accepted seed receipt")
			require.NoError(t, mock.ExpectationsWereMet(), "the wrapper alone must own one original transaction boundary and best-effort rollback")
		})
	}
}
