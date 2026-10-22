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

func TestLockLifecycleScopeInTxQueryContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "policy verification only observes effective settings",
			query: lifecycleTransactionPolicyQuery,
			want: `SELECT current_setting('transaction_isolation'),
				COALESCE(current_setting('lifecycle143.writer_version', true), ''),
				current_setting('lock_timeout'), current_setting('statement_timeout')`,
		},
		{
			name:  "parent is tenant scoped without a visibility or eligibility filter",
			query: lifecycleSessionScopeLockQuery,
			want: `SELECT id, org_id, status, deleted_at, archived_at,
				admission_epoch, end_epoch, lifecycle_version
				FROM sessions WHERE org_id = $1 AND id = $2
				FOR NO KEY UPDATE NOWAIT`,
		},
		{
			name:  "lanes are exact parent scoped and locked in one ordered batch",
			query: lifecycleThreadScopeLockQuery,
			want: `SELECT id, org_id, session_id, status, archived_at,
				admission_epoch, end_epoch, lifecycle_version
				FROM session_threads
				WHERE org_id = $1 AND session_id = $2 AND id = ANY($3::uuid[])
				ORDER BY id FOR NO KEY UPDATE NOWAIT`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, strings.Join(strings.Fields(tt.want), " "), strings.Join(strings.Fields(tt.query), " "),
				"scope locking SQL must preserve exact tenant, parent, batch and lock semantics")
		})
	}
}

func TestLockLifecycleScopeInTxRejectsInvalidInputsBeforeQueries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		invalid  string
		wantText string
		wantErr  error
	}{
		{name: "nil context", invalid: "nil context", wantText: "nil context"},
		{name: "nil transaction", invalid: "nil transaction", wantText: "nil transaction"},
		{name: "typed nil transaction", invalid: "typed nil transaction", wantText: "nil transaction"},
		{name: "cancelled context", invalid: "cancelled context", wantErr: context.Canceled},
		{name: "expired deadline", invalid: "expired deadline", wantErr: context.DeadlineExceeded},
		{name: "missing deadline", invalid: "missing deadline", wantText: "deadline"},
		{name: "overlong deadline", invalid: "overlong deadline", wantText: "deadline"},
		{name: "zero organization", invalid: "zero organization", wantText: "org"},
		{name: "zero session", invalid: "zero session", wantText: "session"},
		{name: "zero lane", invalid: "zero lane", wantText: "thread"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newLifecycleScopeMockFixture()
			orgID := fixture.orgID
			scope := lifecycleScope{sessionID: fixture.sessionID, threadIDs: []uuid.UUID{fixture.first.id}}
			var tx pgx.Tx = mock
			switch tt.invalid {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var typedNil *pgxpool.Tx
				tx = typedNil
			case "cancelled context":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "expired deadline":
				expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				ctx = expired
			case "missing deadline":
				ctx = context.Background()
			case "overlong deadline":
				overlong, cancel := context.WithTimeout(context.Background(), 10*LifecycleTransactionTimeout)
				t.Cleanup(cancel)
				ctx = overlong
			case "zero organization":
				orgID = uuid.Nil
			case "zero session":
				scope.sessionID = uuid.Nil
			case "zero lane":
				scope.threadIDs = []uuid.UUID{fixture.first.id, uuid.Nil}
			}
			got, err := lockLifecycleScopeInTx(ctx, tx, orgID, scope)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "invalid caller context should retain its cancellation cause")
			} else {
				require.ErrorContains(t, err, tt.wantText, "invalid scope inputs should fail before any SQL")
			}
			require.Equal(t, lockedLifecycleScope{}, got, "invalid inputs must not expose a partial locked snapshot")
			require.NoError(t, mock.ExpectationsWereMet(), "input validation must not configure, query or settle the transaction")
		})
	}
}

func TestLockLifecycleScopeInTxPolicyFailuresDoNotLockRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		policy     []any
		queryError bool
		missing    bool
	}{
		{name: "isolation differs", policy: []any{"serializable", "1", "250ms", "1500ms"}},
		{name: "missing writer marker", policy: []any{"read committed", nil, "250ms", "1500ms"}},
		{name: "empty writer marker", policy: []any{"read committed", "", "250ms", "1500ms"}},
		{name: "older writer marker", policy: []any{"read committed", "0", "250ms", "1500ms"}},
		{name: "different writer marker", policy: []any{"read committed", "2", "250ms", "1500ms"}},
		{name: "unbounded lock timeout", policy: []any{"read committed", "1", "0", "1500ms"}},
		{name: "different lock timeout", policy: []any{"read committed", "1", "251ms", "1500ms"}},
		{name: "unbounded statement timeout", policy: []any{"read committed", "1", "250ms", "0"}},
		{name: "different statement timeout", policy: []any{"read committed", "1", "250ms", "1501ms"}},
		{name: "late policy scan error", policy: []any{"read committed", "1", "250ms", struct{}{}}},
		{name: "policy query error retains its cause", queryError: true},
		{name: "missing policy row", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newLifecycleScopeMockFixture()
			queryErr := errors.New("policy lookup unavailable")
			query := mock.ExpectQuery(lifecycleTransactionPolicyQuery)
			if tt.queryError {
				query.WillReturnError(queryErr)
			} else {
				rows := lifecycleScopePolicyRows()
				if !tt.missing {
					rows.AddRow(tt.policy...)
				}
				query.WillReturnRows(rows)
			}
			got, err := lockLifecycleScopeInTx(ctx, mock, fixture.orgID,
				lifecycleScope{sessionID: fixture.sessionID, threadIDs: []uuid.UUID{fixture.first.id}})
			require.Error(t, err, "unverified transaction policy must stop before parent or lane locks")
			if tt.queryError {
				require.ErrorIs(t, err, queryErr, "policy lookup error identity should survive wrapping")
			}
			if tt.missing {
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing policy row should retain its database cause")
			}
			require.Equal(t, lockedLifecycleScope{}, got, "policy failure must return no locked state")
			require.NoError(t, mock.ExpectationsWereMet(), "policy verification must only read settings and must not settle the transaction")
		})
	}
}

func TestLockLifecycleScopeInTxReturnsExactRawScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*lifecycleScopeMockFixture) []uuid.UUID
	}{
		{name: "nil lane list does not infer a primary lane", prepare: func(_ *lifecycleScopeMockFixture) []uuid.UUID { return nil }},
		{name: "empty lane list does not infer a primary lane", prepare: func(_ *lifecycleScopeMockFixture) []uuid.UUID { return []uuid.UUID{} }},
		{name: "one exact lane", prepare: func(f *lifecycleScopeMockFixture) []uuid.UUID { return []uuid.UUID{f.first.id} }},
		{name: "present zero end epochs stay distinct from absent ends", prepare: func(f *lifecycleScopeMockFixture) []uuid.UUID {
			zero := int64(0)
			f.session.epochs.EndEpoch = &zero
			f.first.epochs.EndEpoch = &zero
			return []uuid.UUID{f.first.id}
		}},
		{name: "unsorted duplicate lanes are copied sorted and deduplicated", prepare: func(f *lifecycleScopeMockFixture) []uuid.UUID {
			return []uuid.UUID{f.second.id, f.first.id, f.second.id, f.first.id}
		}},
		{name: "hidden terminal rows and unknown future statuses remain observations", prepare: func(f *lifecycleScopeMockFixture) []uuid.UUID {
			at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			end := int64(math.MaxInt64)
			f.session.status = models.SessionStatus("future-session-state")
			f.session.deletedAt, f.session.archivedAt = &at, &at
			f.session.epochs = LifecycleEpochState{AdmissionEpoch: math.MaxInt64, EndEpoch: &end, LifecycleVersion: math.MaxInt16}
			f.first.status = models.ThreadStatus("future-thread-state")
			f.first.archivedAt = &at
			f.first.epochs = LifecycleEpochState{AdmissionEpoch: 0, EndEpoch: &end, LifecycleVersion: math.MaxInt16}
			return []uuid.UUID{f.first.id}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newLifecycleScopeMockFixture()
			input := tt.prepare(&fixture)
			before := append([]uuid.UUID(nil), input...)
			if input != nil && len(input) == 0 {
				before = []uuid.UUID{}
			}
			want := lockedLifecycleScope{session: fixture.session, threads: []lifecycleLockedThread{}}
			if len(input) != 0 {
				want.threads = []lifecycleLockedThread{fixture.first}
				if len(input) > 1 {
					want.threads = append(want.threads, fixture.second)
				}
			}
			expectLifecycleScopePolicy(mock)
			mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID).
				WillReturnRows(lifecycleScopeSessionRows(fixture.session))
			if len(want.threads) != 0 {
				ids := make([]uuid.UUID, len(want.threads))
				for i, thread := range want.threads {
					ids[i] = thread.id
				}
				mock.ExpectQuery(lifecycleThreadScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID, ids).
					WillReturnRows(lifecycleScopeThreadRows(want.threads...)).RowsWillBeClosed()
			}
			deadline, hasDeadline := ctx.Deadline()
			got, err := lockLifecycleScopeInTx(ctx, mock, fixture.orgID,
				lifecycleScope{sessionID: fixture.sessionID, threadIDs: input})
			require.NoError(t, err, "verified caller transaction should lock exactly the requested scope")
			require.Equal(t, want, got, "locked snapshot must preserve every raw field without eligibility inference")
			require.Equal(t, before, input, "deduplicating and sorting lanes must not mutate the caller's slice")
			afterDeadline, afterHasDeadline := ctx.Deadline()
			require.Equal(t, hasDeadline, afterHasDeadline, "scope helper must preserve the caller's budget ownership")
			require.Equal(t, deadline, afterDeadline, "scope helper must not replace or extend the existing deadline")
			require.NoError(t, mock.ExpectationsWereMet(), "scope helper should read policy, lock the parent, then issue at most one lane batch")
		})
	}
}

func TestLockLifecycleScopeInTxFailuresDiscardAllSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stage string
	}{
		{name: "parent query failure", stage: "parent query"},
		{name: "parent missing from exact tenant", stage: "parent missing"},
		{name: "late parent scan failure", stage: "parent scan"},
		{name: "parent returned for a different tenant", stage: "parent org"},
		{name: "parent returned with a different identity", stage: "parent id"},
		{name: "lane query failure discards locked parent", stage: "lane query"},
		{name: "missing lane discards locked parent", stage: "lane missing"},
		{name: "late lane scan failure discards earlier lanes", stage: "lane scan"},
		{name: "lane iteration failure discards earlier lanes", stage: "lane iteration"},
		{name: "duplicate lane cannot satisfy requested membership", stage: "lane duplicate"},
		{name: "foreign tenant lane is rejected", stage: "lane org"},
		{name: "foreign parent lane is rejected", stage: "lane parent"},
		{name: "unrequested lane is rejected", stage: "lane identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newLifecycleScopeMockFixture()
			queryErr := errors.New("scope row lookup unavailable")
			expectLifecycleScopePolicy(mock)
			parentQuery := mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID)
			parentRows := lifecycleScopeSessionRows(fixture.session)
			switch tt.stage {
			case "parent query":
				parentQuery.WillReturnError(queryErr)
			case "parent missing":
				parentQuery.WillReturnRows(lifecycleScopeSessionRows())
			case "parent scan":
				parentRows = pgxmock.NewRows([]string{"id", "org_id", "status", "deleted_at", "archived_at", "admission_epoch", "end_epoch", "lifecycle_version"}).
					AddRow(fixture.sessionID, fixture.orgID, fixture.session.status, nil, nil, int64(73), nil, "invalid-version")
				parentQuery.WillReturnRows(parentRows)
			case "parent org":
				fixture.session.orgID = uuid.New()
				parentQuery.WillReturnRows(lifecycleScopeSessionRows(fixture.session))
			case "parent id":
				fixture.session.id = uuid.New()
				parentQuery.WillReturnRows(lifecycleScopeSessionRows(fixture.session))
			default:
				parentQuery.WillReturnRows(parentRows)
				laneQuery := mock.ExpectQuery(lifecycleThreadScopeLockQuery).
					WithArgs(fixture.orgID, fixture.sessionID, []uuid.UUID{fixture.first.id, fixture.second.id})
				switch tt.stage {
				case "lane query":
					laneQuery.WillReturnError(queryErr)
				case "lane missing":
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first)).RowsWillBeClosed()
				case "lane scan":
					rows := lifecycleScopeThreadRows(fixture.first)
					rows.AddRow(fixture.second.id, fixture.orgID, fixture.sessionID, fixture.second.status, nil, int64(17), nil, "invalid-version")
					laneQuery.WillReturnRows(rows).RowsWillBeClosed()
				case "lane iteration":
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first, fixture.second).RowError(1, queryErr)).RowsWillBeClosed()
				case "lane duplicate":
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first, fixture.first)).RowsWillBeClosed()
				case "lane org":
					fixture.second.orgID = uuid.New()
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first, fixture.second)).RowsWillBeClosed()
				case "lane parent":
					fixture.second.sessionID = uuid.New()
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first, fixture.second)).RowsWillBeClosed()
				case "lane identity":
					fixture.second.id = uuid.New()
					laneQuery.WillReturnRows(lifecycleScopeThreadRows(fixture.first, fixture.second)).RowsWillBeClosed()
				}
			}
			got, err := lockLifecycleScopeInTx(ctx, mock, fixture.orgID,
				lifecycleScope{sessionID: fixture.sessionID, threadIDs: []uuid.UUID{uuid.UUID{15: 2}, uuid.UUID{15: 1}}})
			require.Error(t, err, "failed row locking or exact membership checks must reject the scope")
			if tt.stage == "parent query" || tt.stage == "lane query" || tt.stage == "lane iteration" {
				require.ErrorIs(t, err, queryErr, "database and row iteration causes must survive scope error wrapping")
			}
			if tt.stage == "parent missing" {
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing scoped parent must retain the database's not-found cause")
			}
			require.Equal(t, lockedLifecycleScope{}, got, "later failure must discard the parent and every earlier lane snapshot")
			require.NoError(t, mock.ExpectationsWereMet(), "scope failure must neither retry nor settle the caller's transaction")
		})
	}
}

func TestLockLifecycleScopeInTxLeavesCallerTransactionOpen(t *testing.T) {
	t.Parallel()
	mock, ctx := newLifecycleScopeMock(t)
	fixture := newLifecycleScopeMockFixture()
	mock.ExpectBegin()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err, "caller should own the transaction before requesting its locks")
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID).
		WillReturnRows(lifecycleScopeSessionRows(fixture.session))
	mock.ExpectExec("SELECT 42").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectRollback()
	got, err := lockLifecycleScopeInTx(ctx, tx, fixture.orgID, lifecycleScope{sessionID: fixture.sessionID})
	require.NoError(t, err, "private scope locking should compose inside an already-owned transaction")
	require.Equal(t, lockedLifecycleScope{session: fixture.session, threads: []lifecycleLockedThread{}}, got, "session-only locking should not manufacture a lane")
	_, err = tx.Exec(ctx, "SELECT 42")
	require.NoError(t, err, "caller must retain control of work after obtaining the locked snapshot")
	require.NoError(t, tx.Rollback(ctx), "caller should settle its own transaction after all work")
	require.NoError(t, mock.ExpectationsWereMet(), "only the caller may begin, execute later work and roll back")
}

func TestLockLifecycleScopeInTxCancellationDiscardsReadSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
	}{
		{name: "cancelled after policy read never locks the parent", query: lifecycleTransactionPolicyQuery},
		{name: "cancelled after parent scan never locks a lane", query: lifecycleSessionScopeLockQuery},
		{name: "cancelled after lane iteration returns no locked snapshot", query: lifecycleThreadScopeLockQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, budgetCtx := newLifecycleScopeMock(t)
			ctx, cancel := context.WithCancel(budgetCtx)
			t.Cleanup(cancel)
			fixture := newLifecycleScopeMockFixture()
			expectLifecycleScopePolicy(mock)
			if tt.query != lifecycleTransactionPolicyQuery {
				mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID).
					WillReturnRows(lifecycleScopeSessionRows(fixture.session))
			}
			if tt.query == lifecycleThreadScopeLockQuery {
				mock.ExpectQuery(lifecycleThreadScopeLockQuery).WithArgs(fixture.orgID, fixture.sessionID, []uuid.UUID{fixture.first.id}).
					WillReturnRows(lifecycleScopeThreadRows(fixture.first)).RowsWillBeClosed()
			}
			tx := &lifecycleScopeCancellingTx{Tx: mock, query: tt.query, cancel: cancel}
			got, err := lockLifecycleScopeInTx(ctx, tx, fixture.orgID,
				lifecycleScope{sessionID: fixture.sessionID, threadIDs: []uuid.UUID{fixture.first.id}})
			require.ErrorIs(t, err, context.Canceled, "cancellation at a completed read boundary must preserve its context cause")
			require.Equal(t, lockedLifecycleScope{}, got, "cancellation must discard even successfully read parent and lane snapshots")
			require.NoError(t, mock.ExpectationsWereMet(), "cancelled read boundaries must not reset the budget, continue locking or settle the transaction")
		})
	}
}

// These wrappers cancel synchronously at completed read boundaries, avoiding
// timing sleeps while proving cancellation cannot leak a successful snapshot.
type lifecycleScopeCancellingTx struct {
	pgx.Tx
	query  string
	cancel context.CancelFunc
}

func (tx *lifecycleScopeCancellingTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if query == tx.query {
		return lifecycleScopeCancellingRow{Row: row, cancel: tx.cancel}
	}
	return row
}

func (tx *lifecycleScopeCancellingTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, query, args...)
	if err != nil || query != tx.query {
		return rows, err
	}
	return lifecycleScopeCancellingRows{Rows: rows, cancel: tx.cancel}, nil
}

type lifecycleScopeCancellingRow struct {
	pgx.Row
	cancel context.CancelFunc
}

func (row lifecycleScopeCancellingRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	row.cancel()
	return err
}

type lifecycleScopeCancellingRows struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (rows lifecycleScopeCancellingRows) Next() bool {
	more := rows.Rows.Next()
	if !more {
		rows.cancel()
	}
	return more
}

type lifecycleScopeMockFixture struct {
	orgID, sessionID uuid.UUID
	session          lifecycleLockedSession
	first, second    lifecycleLockedThread
}

func newLifecycleScopeMockFixture() lifecycleScopeMockFixture {
	orgID, sessionID := uuid.UUID{15: 101}, uuid.UUID{15: 102}
	return lifecycleScopeMockFixture{
		orgID: orgID, sessionID: sessionID,
		session: lifecycleLockedSession{id: sessionID, orgID: orgID, status: models.SessionStatusRunning},
		first: lifecycleLockedThread{id: uuid.UUID{15: 1}, orgID: orgID, sessionID: sessionID,
			status: models.ThreadStatusAwaitingInput, epochs: LifecycleEpochState{AdmissionEpoch: 23}},
		second: lifecycleLockedThread{id: uuid.UUID{15: 2}, orgID: orgID, sessionID: sessionID,
			status: models.ThreadStatusCompleted, epochs: LifecycleEpochState{AdmissionEpoch: 31}},
	}
}

func newLifecycleScopeMock(t *testing.T) (pgxmock.PgxPoolIface, context.Context) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	require.NoError(t, err, "each scope case should use a fresh exact-query mock")
	t.Cleanup(mock.Close)
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	t.Cleanup(cancel)
	return mock, ctx
}

func lifecycleScopePolicyRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"transaction_isolation", "writer_version", "lock_timeout", "statement_timeout"})
}

func expectLifecycleScopePolicy(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(lifecycleTransactionPolicyQuery).
		WillReturnRows(lifecycleScopePolicyRows().AddRow("read committed", "1", "250ms", "1500ms"))
}

func lifecycleScopeSessionRows(sessions ...lifecycleLockedSession) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "status", "deleted_at", "archived_at", "admission_epoch", "end_epoch", "lifecycle_version"})
	for _, session := range sessions {
		rows.AddRow(session.id, session.orgID, session.status, session.deletedAt, session.archivedAt,
			session.epochs.AdmissionEpoch, session.epochs.EndEpoch, session.epochs.LifecycleVersion)
	}
	return rows
}

func lifecycleScopeThreadRows(threads ...lifecycleLockedThread) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "status", "archived_at", "admission_epoch", "end_epoch", "lifecycle_version"})
	for _, thread := range threads {
		rows.AddRow(thread.id, thread.orgID, thread.sessionID, thread.status, thread.archivedAt,
			thread.epochs.AdmissionEpoch, thread.epochs.EndEpoch, thread.epochs.LifecycleVersion)
	}
	return rows
}
